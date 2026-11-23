package fleet

import (
	"context"
	"time"

	"clawbench-fleet/internal/domain"
	"clawbench-fleet/internal/store"
)

func (s *Service) Run(ctx context.Context) {
	go s.healthLoop(ctx)
	go s.metricsLoop(ctx)
	go s.dailyLoop(ctx)
	go s.diskLoop(ctx)
}

// diskLoop enforces the per-tenant disk limit. Measuring every home directory
// walks the whole tree, so it runs every few minutes rather than with the
// ten-second metrics loop.
func (s *Service) diskLoop(ctx context.Context) {
	timer := time.NewTimer(15 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		s.enforceDiskLimits(ctx)
		timer.Reset(5 * time.Minute)
	}
}

func (s *Service) enforceDiskLimits(ctx context.Context) {
	tenants, err := s.allTenants()
	if err != nil {
		return
	}
	for i := range tenants {
		t := tenants[i]
		if t.Status != domain.StatusRunning || t.DiskQuotaGB <= 0 {
			continue
		}
		unlock, ok := s.locks.TryLock(t.Username)
		if !ok {
			continue
		}
		checkCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
		size, sizeErr := s.Sys.DiskUsage(checkCtx, t.HomeDir)
		cancel()
		if sizeErr != nil {
			unlock()
			continue
		}
		t.DiskBytes = size
		_ = s.Store.SetTenantDisk(t.ID, size, time.Now().Format(time.RFC3339))
		if diskOverLimit(&t, size) {
			s.enforceDiskLimit(ctx, &t)
		}
		unlock()
	}
}

func (s *Service) healthLoop(ctx context.Context) {
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		s.checkAllHealth(ctx)
		timer.Reset(30 * time.Second)
	}
}

func (s *Service) checkAllHealth(ctx context.Context) {
	tenants, err := s.Store.ListTenantsByStatus(domain.StatusRunning, domain.StatusError)
	if err != nil {
		return
	}
	for _, t := range tenants {
		unlock, ok := s.locks.TryLock(t.Username)
		if !ok {
			continue
		}
		s.checkTenantHealth(ctx, t)
		unlock()
	}
}

func (s *Service) checkTenantHealth(ctx context.Context, t domain.Tenant) {
	checkCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	active, err := s.Sys.IsActive(checkCtx, t.UnitName)
	if err != nil || !active {
		msg := "服务未运行"
		if err != nil {
			msg = err.Error()
		}
		if t.Status != domain.StatusError || t.LastError != msg {
			_ = s.Store.SetTenantStatus(t.ID, domain.StatusError, msg)
		}
		return
	}
	info, err := s.healthCheck(checkCtx, t.Port)
	if err != nil {
		if t.Status != domain.StatusError {
			_ = s.Store.SetTenantStatus(t.ID, domain.StatusError, "健康检查失败："+err.Error())
		}
		return
	}
	if t.Status != domain.StatusRunning {
		_ = s.Store.SetTenantStatus(t.ID, domain.StatusRunning, "")
	}
	_ = s.Store.SetTenantHealth(t.ID, true, info.Version, time.Now().Format(time.RFC3339))
}

func (s *Service) metricsLoop(ctx context.Context) {
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		s.RefreshMetrics(ctx)
		s.recordMetricSamples(ctx)
		timer.Reset(10 * time.Second)
	}
}

// recordMetricSamples appends one history point per running tenant every five
// minutes. That is frequent enough to see a spike after the fact and cheap
// enough to keep for a week.
func (s *Service) recordMetricSamples(ctx context.Context) {
	if time.Since(s.lastSample) < sampleInterval {
		return
	}
	s.lastSample = time.Now()
	at := s.lastSample.Format(time.RFC3339)
	tenants, err := s.Store.ListTenantsByStatus(domain.StatusRunning)
	if err != nil {
		return
	}
	for _, t := range tenants {
		m := s.GetMetrics(t.ID)
		_ = s.Store.AddMetricSample(t.ID, domain.MetricSample{
			At:          at,
			CPUPercent:  m.CPUPercent,
			MemoryBytes: m.MemoryBytes,
			PIDs:        m.PIDs,
			DiskBytes:   t.DiskBytes,
		})
	}
	s.purgeMetricSamples(ctx)
}

// purgeMetricSamples drops history older than the retention window once a day.
func (s *Service) purgeMetricSamples(ctx context.Context) {
	retention := s.Settings().MetricsRetention
	if retention <= 0 || !s.shouldRun("metrics", 4) {
		return
	}
	cutoff := time.Now().Add(-time.Duration(retention) * 24 * time.Hour)
	_, _ = s.Store.PurgeMetricSamples(cutoff)
	_ = ctx
}

// MetricHistory returns the usage history for one tenant over the last window.
func (s *Service) MetricHistory(id int64, window time.Duration) ([]domain.MetricSample, error) {
	if _, err := s.tenantByID(id); err != nil {
		return nil, err
	}
	if window <= 0 {
		window = 24 * time.Hour
	}
	if window > 7*24*time.Hour {
		window = 7 * 24 * time.Hour
	}
	return s.Store.MetricHistory(id, time.Now().Add(-window), 5000)
}

func (s *Service) RefreshMetrics(ctx context.Context) {
	tenants, err := s.Store.ListTenantsByStatus(domain.StatusRunning)
	if err != nil {
		return
	}
	for _, t := range tenants {
		m, err := s.Sys.Metrics(ctx, t.UnitName)
		if err != nil {
			continue
		}
		s.setMetrics(t.ID, domain.Metrics{
			CPUPercent:  m.CPUPercent,
			MemoryBytes: m.MemoryBytes,
			PIDs:        m.PIDs,
			DiskBytes:   t.DiskBytes,
			SampledAt:   time.Now().Format(time.RFC3339),
		})
	}
}

func (s *Service) RefreshDiskUsage(ctx context.Context) {
	tenants, err := s.allTenants()
	if err != nil {
		return
	}
	for _, t := range tenants {
		if t.Status == domain.StatusTrashed {
			continue
		}
		size, err := s.Sys.DiskUsage(ctx, t.HomeDir)
		if err != nil {
			continue
		}
		_ = s.Store.SetTenantDisk(t.ID, size, time.Now().Format(time.RFC3339))
		s.metricsMu.Lock()
		m := s.metrics[t.ID]
		m.DiskBytes = size
		s.metrics[t.ID] = m
		s.metricsMu.Unlock()
	}
}

func (s *Service) dailyLoop(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		st := s.Settings()
		if st.BackupEnabled && s.shouldRun("backup", st.BackupHour) {
			go s.BackupAll(ctx)
		}
		if s.shouldRun("disk", 4) {
			go s.RefreshDiskUsage(ctx)
		}
		// 最近活跃时间放在 7 点：要挨个打开每个实例的数据库，比健康检查重，
		// 一天一次足够，也赶在管理员上班前把结果摆出来。
		if s.shouldRun("activity", 7) {
			go s.RefreshLastActive(ctx)
		}
		if s.shouldRun("trash", 5) {
			go s.PurgeExpiredTrash(ctx)
		}
		if s.shouldRun("audit", 5) {
			go s.PurgeExpiredAudit()
		}
		if s.shouldRun("artifacts", 6) {
			s.cleanupArtifacts()
		}
	}
}

func (s *Service) shouldRun(key string, hour int) bool {
	now := time.Now()
	if now.Hour() != hour {
		return false
	}
	day := now.Format("20060102")
	s.dailyMu.Lock()
	defer s.dailyMu.Unlock()
	if s.lastDaily[key] == day {
		return false
	}
	s.lastDaily[key] = day
	return true
}

func (s *Service) cleanupArtifacts() {
	cutoff := time.Now().Add(-24 * time.Hour)
	s.artifactsMu.Lock()
	defer s.artifactsMu.Unlock()
	for id, at := range s.artifactAt {
		if at.Before(cutoff) {
			delete(s.artifacts, id)
			delete(s.artifactAt, id)
		}
	}
}

// Reconcile compares the database with the actual systemd units. It never
// deletes anything: mismatches are surfaced as errors or orphans for the admin
// to resolve.
func (s *Service) Reconcile(ctx context.Context) {
	units, err := s.Sys.ListUnits(ctx)
	if err != nil {
		return
	}
	unitSet := map[string]bool{}
	for _, u := range units {
		unitSet[u] = true
	}
	tenants, err := s.allTenants()
	if err != nil {
		return
	}
	known := map[string]bool{}
	for _, t := range tenants {
		if t.Status == domain.StatusTrashed {
			continue
		}
		known[t.UnitName] = true
		if !unitSet[t.UnitName] {
			_ = s.Store.SetTenantStatus(t.ID, domain.StatusError, "systemd 单元丢失，可尝试修复")
			continue
		}
		if t.Status == domain.StatusCreating {
			if created, err := time.Parse(time.RFC3339, t.CreatedAt); err == nil && time.Since(created) > 10*time.Minute {
				_ = s.Store.SetTenantStatus(t.ID, domain.StatusError, "创建过程中断，可尝试修复或删除")
			}
		}
		if t.Status == domain.StatusRunning {
			s.checkTenantHealth(ctx, t)
		}
	}
	var orphans []string
	for _, u := range units {
		if !known[u] {
			orphans = append(orphans, u)
		}
	}
	s.orphansMu.Lock()
	s.orphans = orphans
	s.orphansMu.Unlock()
}

// ListRecentJobs is used by the overview page.
func (s *Service) ListRecentJobs(limit int) ([]domain.Job, error) {
	return s.Store.RecentJobs(limit)
}

// ListTrash returns trashed tenants with their remaining retention time.
type TrashItem struct {
	domain.Tenant
	DaysLeft int `json:"days_left"`
}

func (s *Service) ListTrash() ([]TrashItem, error) {
	items, err := s.Store.TrashedTenants()
	if err != nil {
		return nil, err
	}
	retention := s.Settings().TrashRetention
	out := make([]TrashItem, 0, len(items))
	for _, t := range items {
		left := retention
		if deleted, err := time.Parse(time.RFC3339, t.DeletedAt); err == nil {
			left = retention - int(time.Since(deleted).Hours()/24)
			if left < 0 {
				left = 0
			}
		}
		out = append(out, TrashItem{Tenant: t, DaysLeft: left})
	}
	return out, nil
}

// ListAudit exposes the store filter for the API layer.
func (s *Service) ListAudit(f store.AuditFilter) ([]domain.AuditEntry, int, error) {
	return s.Store.ListAudit(f)
}
