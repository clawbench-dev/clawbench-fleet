package fleet

import (
	"context"
	"os"
	"strings"
	"testing"

	"clawbench-fleet/internal/domain"
	"clawbench-fleet/internal/store"
)

// TestApplyTenantConfigWritesEveryInstance pushes the unified config into a
// small fleet and checks the value actually lands in each config.yaml.
func TestApplyTenantConfigWritesEveryInstance(t *testing.T) {
	svc := newService(t)
	ctx := context.Background()
	ac := AuditContext{Actor: "tester"}
	alice := createRunning(t, svc, "alice")
	bob := createRunning(t, svc, "bob")

	if err := svc.UpdateTenantConfig([]domain.ConfigEntry{{Key: "chat.page_size", Value: "42"}}); err != nil {
		t.Fatal(err)
	}
	job, err := svc.ApplyTenantConfigAsync(ctx, ac)
	if err != nil {
		t.Fatal(err)
	}
	if job.Total != 2 {
		t.Fatalf("job total = %d, want 2", job.Total)
	}
	waitJob(t, svc, job.ID, domain.JobSucceeded)

	for _, id := range []int64{alice.ID, bob.ID} {
		got, getErr := svc.GetTenant(id)
		if getErr != nil {
			t.Fatal(getErr)
		}
		if got.Status != domain.StatusRunning {
			t.Fatalf("%s status = %s, want running after a config push", got.Username, got.Status)
		}
		data, readErr := os.ReadFile(svc.tenantConfigPath(got))
		if readErr != nil {
			t.Fatalf("read %s config: %v", got.Username, readErr)
		}
		if !strings.Contains(string(data), "page_size: 42") {
			t.Fatalf("%s config missing the pushed value:\n%s", got.Username, data)
		}
	}
	// The push restarts each instance, so every tenant must have been audited.
	events, total, err := svc.ListAudit(store.AuditFilter{Action: auditConfigApply, PageSize: 50})
	if err != nil {
		t.Fatal(err)
	}
	if total == 0 || len(events) == 0 {
		t.Fatal("config push left no audit trail")
	}
}

// TestApplyTenantConfigWithNoInstance is the error the settings page shows when
// the platform manages nothing yet: no job, just an explanation.
func TestApplyTenantConfigWithNoInstance(t *testing.T) {
	svc := newService(t)
	if _, err := svc.ApplyTenantConfigAsync(context.Background(), AuditContext{Actor: "tester"}); err == nil {
		t.Fatal("a push with no instance should fail")
	} else if !strings.Contains(err.Error(), "没有需要下发配置的实例") {
		t.Fatalf("error = %q, want the empty-fleet message", err)
	}
}

// TestConfigTargetsSkipUnmanagedStates makes sure a trashed or half-created
// tenant is never rewritten: doing so would touch a directory that is being
// moved or removed.
func TestConfigTargetsSkipUnmanagedStates(t *testing.T) {
	svc := newService(t)
	ctx := context.Background()
	ac := AuditContext{Actor: "tester"}
	alice := createRunning(t, svc, "alice")

	bob := createRunning(t, svc, "bob")
	if _, err := svc.DeleteTenant(ctx, bob.ID, bob.Username, ac); err != nil {
		t.Fatal(err)
	}

	targets, err := svc.configTargets()
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 || targets[0].ID != alice.ID {
		t.Fatalf("targets = %+v, want only alice (%d)", targets, alice.ID)
	}
}

// TestApplyTenantConfigToTrashedTenant covers the guard on the detail page:
// pushing into a trashed instance would write into the recycle bin.
func TestApplyTenantConfigToTrashedTenant(t *testing.T) {
	svc := newService(t)
	ctx := context.Background()
	ac := AuditContext{Actor: "tester"}
	alice := createRunning(t, svc, "alice")
	if _, err := svc.DeleteTenant(ctx, alice.ID, alice.Username, ac); err != nil {
		t.Fatal(err)
	}
	if err := svc.ApplyTenantConfigTo(ctx, alice.ID, ac); err == nil {
		t.Fatal("pushing config into a trashed tenant should fail")
	}
}

// TestApplyTenantConfigToMissingTenant is the 404 path.
func TestApplyTenantConfigToMissingTenant(t *testing.T) {
	svc := newService(t)
	if err := svc.ApplyTenantConfigTo(context.Background(), 999999, AuditContext{Actor: "tester"}); err == nil {
		t.Fatal("pushing config to a missing tenant should fail")
	}
}

// TestApplyTenantConfigMarksPartialWhenOneInstanceFails is the regression that
// matters for a batch push: one broken instance must not hide behind an
// all-green job, and the healthy ones must still be updated.
func TestApplyTenantConfigMarksPartialWhenOneInstanceFails(t *testing.T) {
	svc := newService(t)
	ctx := context.Background()
	ac := AuditContext{Actor: "tester"}
	good := createRunning(t, svc, "alice")
	broken := createRunning(t, svc, "bob")

	if err := svc.UpdateTenantConfig([]domain.ConfigEntry{{Key: "chat.page_size", Value: "7"}}); err != nil {
		t.Fatal(err)
	}
	// A tenant whose data directory cannot be written is enough to make its
	// own push fail while the other one keeps working.
	cfgPath := svc.tenantConfigPath(broken)
	if err := os.RemoveAll(cfgPath); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(cfgPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgPath+"/blocker", []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	job, err := svc.ApplyTenantConfigAsync(ctx, ac)
	if err != nil {
		t.Fatal(err)
	}
	waitJob(t, svc, job.ID, domain.JobPartial)

	raw, readErr := os.ReadFile(svc.tenantConfigPath(good))
	if readErr != nil {
		t.Fatalf("healthy instance was not updated: %v", readErr)
	}
	if !strings.Contains(string(raw), "page_size: 7") {
		t.Fatalf("healthy instance kept the old config:\n%s", raw)
	}
}
