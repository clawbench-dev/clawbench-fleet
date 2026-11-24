package fleet

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"clawbench-fleet/internal/domain"
	"clawbench-fleet/internal/store"
)

func (s *Service) Run(ctx context.Context) {
	go s.healthLoop(ctx)
	go s.metricsLoop(ctx)
	go s.dailyLoop(ctx)
	// Disk usage used to have its own five-minute loop, but that walked every
	// home directory of every tenant and mostly did nothing: the samples are
	// written to SQLite and only refreshed at 04:00 by dailyLoop. Sizes are now
	// measured on demand — the 04:00 sweep, the instance detail page, the
	// start path and the manual refresh button — and the limit is enforced as
	// part of those paths (see enforceDiskLimits).
}

// enforceDiskLimits measures every running tenant that has a limit and stops
// the ones that went over. It is no longer on a timer: the daily sweep and the
// manual refresh call it, so a tenant is checked whenever its size is known.
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
			// A failed measurement must not be silent: the row keeps showing the
			// old value, and a tenant that was never measured shows 0 B, which
			// reads like an empty directory. Log it once per tenant per day so a
			// permanently broken path is visible without flooding the log.
			s.logDiskFailure(t, sizeErr)
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

// logDiskFailure records a measurement error at most once per tenant per day.
// The message carries the tenant and the error the du call returned.
func (s *Service) logDiskFailure(t domain.Tenant, err error) {
	if !s.shouldLogDiskFailure(t.Username) {
		return
	}
	log.Printf("磁盘统计失败 username=%s home=%s err=%v", t.Username, t.HomeDir, err)
}

// shouldLogDiskFailure reports whether this tenant's failure still needs a log
// line today. A permanently broken home directory would otherwise write one
// line per sweep forever.
func (s *Service) shouldLogDiskFailure(username string) bool {
	day := time.Now().Format("20060102")
	key := "diskfail:" + username
	s.dailyMu.Lock()
	defer s.dailyMu.Unlock()
	if s.lastDaily[key] == day {
		return false
	}
	s.lastDaily[key] = day
	return true
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
			s.logDiskFailure(t, err)
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

// RefreshTenantDisk measures one tenant right now and stores the result. It
// backs the manual refresh button: the daily sweep is the only scheduled
// measurement left, so an admin who just cleaned up a home directory should not
// have to wait for 04:00 to see the new number.
func (s *Service) RefreshTenantDisk(ctx context.Context, id int64, ac AuditContext) (*domain.Tenant, error) {
	t, err := s.tenantByID(id)
	if err != nil {
		return nil, err
	}
	if t.Status == domain.StatusTrashed {
		return nil, errors.New("回收站里的实例不统计磁盘")
	}
	unlock, ok := s.locks.TryLock(t.Username)
	if !ok {
		return nil, errors.New("该实例正在执行其他操作，请稍后重试")
	}
	defer unlock()

	measureCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	size, err := s.Sys.DiskUsage(measureCtx, t.HomeDir)
	if err != nil {
		s.logDiskFailure(*t, err)
		s.Audit(ac, "tenant.disk_refresh", t, map[string]any{"disk_bytes": t.DiskBytes}, err)
		return nil, fmt.Errorf("统计磁盘失败：%w", err)
	}
	at := time.Now().Format(time.RFC3339)
	if err := s.Store.SetTenantDisk(t.ID, size, at); err != nil {
		return nil, err
	}
	s.metricsMu.Lock()
	m := s.metrics[t.ID]
	m.DiskBytes = size
	s.metrics[t.ID] = m
	s.metricsMu.Unlock()

	s.Audit(ac, "tenant.disk_refresh", t, map[string]any{"disk_bytes": size}, nil)

	t.DiskBytes = size
	t.DiskChecked = at
	// Measuring is also the moment to enforce the limit, so a tenant that just
	// went over is stopped as soon as anyone looks at it.
	if t.Status == domain.StatusRunning && diskOverLimit(t, size) {
		s.enforceDiskLimit(ctx, t)
		if fresh, err := s.tenantByID(id); err == nil {
			return fresh, nil
		}
	}
	return t, nil
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
		if st := s.Settings(); st.NpmAutoEnabled && s.npmAutoDue(st, time.Now()) {
			go s.runNpmAutoCheck(ctx)
		}
	}
}

// runNpmAutoCheck is the scheduled wrapper around RunNpmAutoCheck. Once it has
// run today it stays quiet: an unchanged library turns every check into the
// same "already have it" result, and one audit row a day saying nothing
// happened is noise. Failures are logged so the operator can tell a typo in the
// package name from a machine without npm.
func (s *Service) runNpmAutoCheck(ctx context.Context) {
	checkCtx, cancel := context.WithTimeout(ctx, npmAutoTimeout)
	defer cancel()
	res, err := s.RunNpmAutoCheck(checkCtx, AuditContext{Actor: actorSystem})
	if err != nil {
		log.Printf("定时 npm 升级失败：%v", err)
	}
	if len(res.Installed) == 0 && err == nil {
		return
	}
	detail := map[string]any{"installed": res.Installed, "latest": res.Current}
	s.Audit(AuditContext{Actor: actorSystem}, "upgrade.npm_auto", nil, detail, err)
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
