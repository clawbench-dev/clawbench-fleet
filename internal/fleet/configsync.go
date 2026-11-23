package fleet

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"clawbench-fleet/internal/domain"
)

// configWorkers caps how many instances are rewritten at once. A running
// instance restarts and must pass its health check, so a big fleet is not
// bounced in one burst.
const configWorkers = 3

const auditConfigApply = "settings.config_apply"

// ApplyTenantConfigAsync writes the saved unified config into every managed
// instance. It returns a job immediately: each running instance restarts.
func (s *Service) ApplyTenantConfigAsync(ctx context.Context, ac AuditContext) (*domain.Job, error) {
	targets, err := s.configTargets()
	if err != nil {
		return nil, err
	}
	if len(targets) == 0 {
		return nil, errors.New("没有需要下发配置的实例")
	}
	job, err := s.Store.CreateJob("config", "{}", len(targets))
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
	go s.runApplyConfig(context.WithoutCancel(ctx), job.ID, targets, itemIDs, ac)
	return job, nil
}

// ApplyTenantConfigTo is the single-instance version used by the detail page.
func (s *Service) ApplyTenantConfigTo(ctx context.Context, id int64, ac AuditContext) error {
	t, err := s.tenantByID(id)
	if err != nil {
		return err
	}
	unlock := s.lockTenant(t.Username)
	defer unlock()
	if t.Status == domain.StatusCreating || t.Status == domain.StatusDeleting || t.Status == domain.StatusTrashed {
		return errors.New("实例状态已经变化（正在创建、删除或已进回收站），已跳过")
	}
	if err := s.acquire(ctx); err != nil {
		return err
	}
	defer s.release()

	// Keep the password: writeTenantConfig only rewrites the hash when a new
	// password is passed, so an empty string leaves the current one alone.
	if err := s.writeTenantConfig(t, ""); err != nil {
		return fmt.Errorf("写入配置后失败：%w", err)
	}
	if t.Status == domain.StatusRunning {
		if err := s.restartAfterUpdate(ctx, t, ac); err != nil {
			return err
		}
	}
	s.Audit(ac, auditConfigApply, t, map[string]any{"source": "unified_config"}, nil)
	return nil
}

// configTargets lists the tenants a full push touches. Trashed and half-deleted
// instances are skipped, and a tenant still being created already gets the
// current settings when its config is first written.
func (s *Service) configTargets() ([]domain.Tenant, error) {
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
		out = append(out, t)
	}
	return out, nil
}

func (s *Service) runApplyConfig(ctx context.Context, jobID int64, targets []domain.Tenant, itemIDs []int64, ac AuditContext) {
	_ = s.Store.MarkJobRunning(jobID)
	var mu sync.Mutex
	done, failed := 0, 0
	sem := make(chan struct{}, configWorkers)
	var wg sync.WaitGroup
	for i := range targets {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			_ = s.Store.UpdateJobItem(itemIDs[i], domain.JobRunning, "")
			err := s.ApplyTenantConfigTo(ctx, targets[i].ID, ac)
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
