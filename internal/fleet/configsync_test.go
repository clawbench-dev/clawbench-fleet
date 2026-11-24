package fleet

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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

// TestQuotaAndConfigPushSkipTenantsThatChangedMidFlight covers the race the
// per-tenant guard exists for: a tenant enters the trash (or starts being
// deleted) after the job was created but before its turn came. That instance
// must be skipped, not resurrected.
func TestQuotaAndConfigPushSkipTenantsThatChangedMidFlight(t *testing.T) {
	svc := newService(t)
	ctx := context.Background()
	ac := AuditContext{Actor: "tester"}
	tenant := createRunning(t, svc, "alice")

	// Both per-instance entry points share the same guard, so one tenant in
	// the trash is enough to cover both.
	if _, err := svc.DeleteTenant(ctx, tenant.ID, "alice", ac); err != nil {
		t.Fatal(err)
	}
	if err := svc.ApplyTenantConfigTo(ctx, tenant.ID, ac); err == nil {
		t.Fatal("a config push must skip a trashed tenant")
	}
	st := svc.Settings()
	if err := svc.applyQuotaToTenant(ctx, tenant.ID, st, ac); err == nil {
		t.Fatal("a quota push must skip a trashed tenant")
	}
}

// TestQuotaPushLeavesMatchingTenantsAlone makes sure the default-quota push is
// a no-op for an instance that already runs with the defaults: restarting
// every instance on every click would be a needless outage.
func TestQuotaPushLeavesMatchingTenantsAlone(t *testing.T) {
	svc := newService(t)
	ctx := context.Background()
	ac := AuditContext{Actor: "tester"}
	tenant := createRunning(t, svc, "bob")

	if err := svc.applyQuotaToTenant(ctx, tenant.ID, svc.Settings(), ac); err != nil {
		t.Fatalf("an instance already at the defaults should be left alone: %v", err)
	}
	got, err := svc.GetTenant(tenant.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.StatusRunning {
		t.Fatalf("status = %s, want the instance left running", got.Status)
	}
}

// TestConfigFilePathPrefersTheConfiguredValue pins the file the adopt service
// unit will name. Getting it wrong hands systemd a unit pointing at the wrong
// config, and the platform does not come back after the restart.
func TestConfigFilePathPrefersTheConfiguredValue(t *testing.T) {
	svc := newService(t)
	if got := svc.configFilePath(); got != SelfConfigPath {
		t.Fatalf("default config path = %q, want %q", got, SelfConfigPath)
	}
	svc.ConfigPath = "/tmp/custom-config.yaml"
	if got := svc.configFilePath(); got != "/tmp/custom-config.yaml" {
		t.Fatalf("configured path = %q", got)
	}
}

// TestAdoptJobFailsCleanlyWhenTheHostCannotBeScanned covers a background job
// whose very first step fails. Every item must be marked failed and the job
// itself closed, otherwise the page shows a progress bar that never finishes.
func TestAdoptJobFailsCleanlyWhenTheHostCannotBeScanned(t *testing.T) {
	svc := newService(t)
	svc.Sys = hookSystem{listUsersErr: errors.New("扫描失败")}.with(svc.Sys)

	job, err := svc.AdoptTenantsAsync(context.Background(), []string{"alice", "bob"}, AuditContext{Actor: "tester"})
	if err != nil {
		t.Fatal(err)
	}
	waitJob(t, svc, job.ID, domain.JobFailed)
}

// TestValidateTLSRejectsRelativeCertDir protects the one setting that has to be
// absolute: a relative certificate directory is resolved against whatever the
// service manager happens to use as a working directory, so the pair lands
// somewhere nobody looks and every instance fails to start.
func TestValidateTLSRejectsRelativeCertDir(t *testing.T) {
	svc := newService(t)
	st := svc.Settings()
	st.TLSEnabled = true
	st.TLSCertDir = "certs"
	if err := validateTLS(st); err == nil {
		t.Fatal("a relative certificate directory must be rejected")
	}
	st.TLSCertDir = filepath.Join(t.TempDir(), "certs")
	if err := validateTLS(st); err != nil {
		t.Fatalf("an absolute certificate directory should be accepted: %v", err)
	}
	// With HTTPS off the directory is not used at all, so it is not checked.
	st.TLSEnabled = false
	st.TLSCertDir = "certs"
	if err := validateTLS(st); err != nil {
		t.Fatalf("the check must not fire while HTTPS is off: %v", err)
	}
}
