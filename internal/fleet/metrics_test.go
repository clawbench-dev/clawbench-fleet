package fleet

import (
	"context"
	"testing"
	"time"

	"clawbench-fleet/internal/domain"
)

func TestRecordMetricSamplesAndHistory(t *testing.T) {
	svc, _ := newDiskTestService(t)
	ctx := context.Background()
	tenant, _, err := svc.CreateTenant(ctx, CreateTenantInput{Username: "sampled"}, AuditContext{})
	if err != nil {
		t.Fatal(err)
	}
	svc.RefreshMetrics(ctx)
	svc.lastSample = time.Time{}
	svc.recordMetricSamples(ctx)

	samples, err := svc.MetricHistory(tenant.ID, time.Hour)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(samples) != 1 {
		t.Fatalf("samples = %d, want 1", len(samples))
	}

	// A second call within the interval writes nothing.
	svc.recordMetricSamples(ctx)
	again, _ := svc.MetricHistory(tenant.ID, time.Hour)
	if len(again) != 1 {
		t.Fatalf("samples = %d, want still 1", len(again))
	}
}

func TestMetricHistoryDefaultsAndLimits(t *testing.T) {
	svc, _ := newDiskTestService(t)
	ctx := context.Background()
	tenant, _, err := svc.CreateTenant(ctx, CreateTenantInput{Username: "window"}, AuditContext{})
	if err != nil {
		t.Fatal(err)
	}
	for _, window := range []time.Duration{0, -time.Hour, 30 * 24 * time.Hour} {
		if _, err := svc.MetricHistory(tenant.ID, window); err != nil {
			t.Fatalf("window %s: %v", window, err)
		}
	}
	if _, err := svc.MetricHistory(9999, time.Hour); err == nil {
		t.Fatal("unknown tenant must fail")
	}
}

func TestPurgeMetricSamples(t *testing.T) {
	svc, _ := newDiskTestService(t)
	ctx := context.Background()
	tenant, _, err := svc.CreateTenant(ctx, CreateTenantInput{Username: "purge"}, AuditContext{})
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-10 * 24 * time.Hour).Format(time.RFC3339)
	fresh := time.Now().Format(time.RFC3339)
	for _, at := range []string{old, fresh} {
		if err := svc.Store.AddMetricSample(tenant.ID, domain.MetricSample{At: at}); err != nil {
			t.Fatal(err)
		}
	}
	n, err := svc.Store.PurgeMetricSamples(time.Now().Add(-7 * 24 * time.Hour))
	if err != nil || n != 1 {
		t.Fatalf("purged %d (%v)", n, err)
	}
	left, _ := svc.MetricHistory(tenant.ID, 30*24*time.Hour)
	if len(left) != 1 {
		t.Fatalf("left = %d", len(left))
	}

	if err := svc.Store.PurgeMetricSamplesForTenant(tenant.ID); err != nil {
		t.Fatal(err)
	}
	left, _ = svc.MetricHistory(tenant.ID, 30*24*time.Hour)
	if len(left) != 0 {
		t.Fatalf("tenant history not removed: %d", len(left))
	}
}

func TestMetricsRetentionValidation(t *testing.T) {
	svc, _ := newDiskTestService(t)
	next := svc.Settings()
	next.MetricsRetention = 0
	if err := svc.UpdateSettings(next); err == nil {
		t.Fatal("retention 0 must be rejected")
	}
	next.MetricsRetention = 200
	if err := svc.UpdateSettings(next); err == nil {
		t.Fatal("retention 200 must be rejected")
	}
	next.MetricsRetention = 7
	if err := svc.UpdateSettings(next); err != nil {
		t.Fatalf("retention 7 should be valid: %v", err)
	}
}

func TestDefaultDiskQuotaAppliedOnCreate(t *testing.T) {
	svc, _ := newDiskTestService(t)
	next := svc.Settings()
	next.DefaultDiskQuotaGB = 8
	if err := svc.UpdateSettings(next); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	tenant, _, err := svc.CreateTenant(ctx, CreateTenantInput{Username: "quota8"}, AuditContext{})
	if err != nil {
		t.Fatal(err)
	}
	if tenant.DiskQuotaGB != 8 {
		t.Fatalf("disk quota = %d, want 8", tenant.DiskQuotaGB)
	}

	zero := 0
	explicit, _, err := svc.CreateTenant(ctx, CreateTenantInput{Username: "quota0", DiskQuotaGB: &zero}, AuditContext{})
	if err != nil {
		t.Fatal(err)
	}
	if explicit.DiskQuotaGB != 0 {
		t.Fatalf("explicit 0 must mean no limit, got %d", explicit.DiskQuotaGB)
	}

	neg := -1
	if _, _, err := svc.CreateTenant(ctx, CreateTenantInput{Username: "quotaNeg", DiskQuotaGB: &neg}, AuditContext{}); err == nil {
		t.Fatal("negative quota must be rejected")
	}
}

func TestUpdateTenantDiskQuota(t *testing.T) {
	svc, _ := newDiskTestService(t)
	ctx := context.Background()
	tenant, _, err := svc.CreateTenant(ctx, CreateTenantInput{Username: "quotaedit"}, AuditContext{})
	if err != nil {
		t.Fatal(err)
	}
	quota := 5
	updated, err := svc.UpdateTenant(ctx, tenant.ID, UpdateTenantInput{DiskQuotaGB: &quota}, AuditContext{})
	if err != nil {
		t.Fatalf("update quota: %v", err)
	}
	if updated.DiskQuotaGB != 5 {
		t.Fatalf("quota = %d", updated.DiskQuotaGB)
	}
	neg := -1
	if _, err := svc.UpdateTenant(ctx, tenant.ID, UpdateTenantInput{DiskQuotaGB: &neg}, AuditContext{}); err == nil {
		t.Fatal("negative quota must be rejected")
	}
}
