package fleet

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"clawbench-fleet/internal/domain"
)

// quotaWorkers caps how many instances are rewritten at the same time. A
// running instance is restarted and waited on, so a large fleet must not be
// bounced all at once.
const quotaWorkers = 3

// ApplyDefaultQuotaAsync pushes the saved default resource limits onto every
// tenant that does not already use them. It returns a job immediately because
// each running instance has to restart and pass its health check.
func (s *Service) ApplyDefaultQuotaAsync(ctx context.Context, ac AuditContext) (*domain.Job, error) {
	st := s.Settings()
	targets, err := s.quotaTargets(st)
	if err != nil {
		return nil, err
	}
	if len(targets) == 0 {
		return nil, errors.New("没有需要应用默认资源上限的实例")
	}
	payload, _ := json.Marshal(map[string]any{
		fieldCPUQuota:  st.DefaultCPUQuota,
		fieldMemoryMax: st.DefaultMemoryMax,
		fieldTasksMax:  st.DefaultTasksMax,
	})
	job, err := s.Store.CreateJob("quota", string(payload), len(targets))
	if err != nil {
		return nil, err
	}
	itemIDs := make([]int64, 0, len(targets))
	for _, t := range targets {
		itemID, addErr := s.Store.AddJobItem(job.ID, t.ID, t.Username)
		if addErr != nil {
			return nil, addErr
		}
		itemIDs = append(itemIDs, itemID)
	}
	go s.runApplyQuota(context.WithoutCancel(ctx), job.ID, st, targets, itemIDs, ac)
	return job, nil
}

// quotaTargets lists the tenants a default-quota push would actually change.
// Instances in the trash or being deleted are not managed right now, and a
// tenant that is still being created will start with the new defaults anyway.
func (s *Service) quotaTargets(st domain.Settings) ([]domain.Tenant, error) {
	all, err := s.allTenants()
	if err != nil {
		return nil, err
	}
	out := make([]domain.Tenant, 0, len(all))
	for _, t := range all {
		switch t.Status {
		case domain.StatusTrashed, domain.StatusDeleting, domain.StatusCreating:
			continue
		}
		if quotaMatches(&t, st) {
			continue
		}
		out = append(out, t)
	}
	return out, nil
}

func quotaMatches(t *domain.Tenant, st domain.Settings) bool {
	return t.CPUQuota == st.DefaultCPUQuota &&
		t.MemoryMax == st.DefaultMemoryMax &&
		t.TasksMax == st.DefaultTasksMax
}

func (s *Service) runApplyQuota(ctx context.Context, jobID int64, st domain.Settings, targets []domain.Tenant, itemIDs []int64, ac AuditContext) {
	_ = s.Store.MarkJobRunning(jobID)
	var mu sync.Mutex
	done, failed := 0, 0
	sem := make(chan struct{}, quotaWorkers)
	var wg sync.WaitGroup
	for i := range targets {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			_ = s.Store.UpdateJobItem(itemIDs[i], domain.JobRunning, "")
			err := s.applyQuotaToTenant(ctx, targets[i].ID, st, ac)
			status, errText := domain.JobSucceeded, ""
			if err != nil {
				status, errText = domain.JobFailed, err.Error()
			}
			_ = s.Store.UpdateJobItem(itemIDs[i], status, errText)
			mu.Lock()
			done++
			if err != nil {
				failed++
			}
			_ = s.Store.UpdateJobProgress(jobID, done, failed)
			mu.Unlock()
		}(i)
	}
	wg.Wait()
	result, _ := json.Marshal(map[string]any{fieldDone: done, statusFailed: failed})
	final := domain.JobSucceeded
	if failed == done {
		final = domain.JobFailed
	} else if failed > 0 {
		final = domain.JobPartial
	}
	_ = s.Store.FinishJob(jobID, final, string(result))
}

// applyQuotaToTenant rewrites one instance with the default limits. Running
// instances restart so the new cgroup limits take effect; stopped and errored
// ones only get the new unit and pick it up on their next start.
func (s *Service) applyQuotaToTenant(ctx context.Context, id int64, st domain.Settings, ac AuditContext) error {
	t, err := s.tenantByID(id)
	if err != nil {
		return err
	}
	unlock := s.lockTenant(t.Username)
	defer unlock()
	if t.Status == domain.StatusCreating || t.Status == domain.StatusDeleting || t.Status == domain.StatusTrashed {
		return errors.New("实例状态已经变化（正在创建、删除或已进回收站），已跳过")
	}
	if quotaMatches(t, st) {
		return nil
	}
	if err := s.acquire(ctx); err != nil {
		return err
	}
	defer s.release()

	old := map[string]any{
		fieldCPUQuota: t.CPUQuota, fieldMemoryMax: t.MemoryMax, fieldTasksMax: t.TasksMax,
	}
	t.CPUQuota, t.MemoryMax, t.TasksMax = st.DefaultCPUQuota, st.DefaultMemoryMax, st.DefaultTasksMax
	if err := s.applyUnitChange(ctx, t, false, ac); err != nil {
		return fmt.Errorf("写入新配置后失败：%w", err)
	}
	if err := s.Store.UpdateTenant(t); err != nil {
		return err
	}
	s.Audit(ac, auditTenantUpdate, t, map[string]any{
		fieldCPUQuota: t.CPUQuota, fieldMemoryMax: t.MemoryMax, fieldTasksMax: t.TasksMax,
		"old": old, "source": "default_quota",
	}, nil)
	return nil
}
