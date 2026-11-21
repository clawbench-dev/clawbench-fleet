package fleet

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"clawbench-fleet/internal/domain"
)

func TestStartStopGuards(t *testing.T) {
	svc := newService(t)
	ctx := context.Background()
	alice := createRunning(t, svc, "alice")
	if _, err := svc.StartTenant(ctx, 999, AuditContext{}); !errors.Is(err, ErrTenantNotFound) {
		t.Fatalf("start missing tenant = %v", err)
	}
	if _, err := svc.StopTenant(ctx, 999, AuditContext{}); !errors.Is(err, ErrTenantNotFound) {
		t.Fatalf("stop missing tenant = %v", err)
	}
	if _, err := svc.RestartTenant(ctx, 999, AuditContext{}); !errors.Is(err, ErrTenantNotFound) {
		t.Fatalf("restart missing tenant = %v", err)
	}
	if _, _, err := svc.ResetPassword(ctx, 999, AuditContext{}); !errors.Is(err, ErrTenantNotFound) {
		t.Fatalf("reset missing tenant = %v", err)
	}
	if _, _, err := svc.RepairTenant(ctx, 999, AuditContext{}); !errors.Is(err, ErrTenantNotFound) {
		t.Fatalf("repair missing tenant = %v", err)
	}
	if _, err := svc.UpdateTenant(ctx, 999, UpdateTenantInput{}, AuditContext{}); !errors.Is(err, ErrTenantNotFound) {
		t.Fatalf("update missing tenant = %v", err)
	}

	// A trashed tenant cannot be started.
	if _, err := svc.DeleteTenant(ctx, alice.ID, "alice", AuditContext{}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.StartTenant(ctx, alice.ID, AuditContext{}); err == nil {
		t.Fatal("a trashed tenant must not start")
	}
	if _, _, err := svc.RestoreTenant(ctx, alice.ID, AuditContext{}); err != nil {
		t.Fatal(err)
	}
}

func TestRestoreTenantConflict(t *testing.T) {
	svc := newService(t)
	ctx := context.Background()
	alice := createRunning(t, svc, "alice")
	if _, err := svc.DeleteTenant(ctx, alice.ID, "alice", AuditContext{}); err != nil {
		t.Fatal(err)
	}
	// Re-create the Linux account behind the platform's back: restoring must refuse.
	if _, err := svc.Sys.UserExists(ctx, "alice"); err != nil {
		t.Fatal(err)
	}
	if err := svc.Sys.CreateUser(ctx, "alice", 6000); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.RestoreTenant(ctx, alice.ID, AuditContext{}); err == nil || !strings.Contains(err.Error(), "Linux 用户已存在") {
		t.Fatalf("conflicting account = %v", err)
	}
}

func TestRestoreTenantTrashMeta(t *testing.T) {
	svc := newService(t)
	ctx := context.Background()
	alice := createRunning(t, svc, "alice")
	if _, err := svc.DeleteTenant(ctx, alice.ID, "alice", AuditContext{}); err != nil {
		t.Fatal(err)
	}
	trashed, _ := svc.GetTenant(alice.ID)
	// The metafile records the original UID; drop it so a new one is picked.
	if err := os.WriteFile(filepath.Join(trashed.TrashPath, "meta.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The config lives in the home directory, which moved to the trash.
	if err := os.Remove(filepath.Join(trashed.TrashPath, "home", ".clawbench", "config", "config.yaml")); err != nil {
		t.Fatal(err)
	}
	restored, password, err := svc.RestoreTenant(ctx, alice.ID, AuditContext{})
	if err != nil {
		t.Fatal(err)
	}
	// The config was lost with the trash, so a fresh password is generated...
	if password == "" {
		t.Fatal("a missing config should produce a new password")
	}
	// ...and the tenant is then usable.
	if restored.Status != domain.StatusRunning {
		t.Fatalf("restored = %+v", restored)
	}
}

func TestCreateTenantConflicts(t *testing.T) {
	svc := newService(t)
	ctx := context.Background()
	if _, _, err := svc.CreateTenantAsync(ctx, CreateTenantInput{Username: "root"}, AuditContext{}); err == nil {
		t.Fatal("reserved username must be rejected")
	}
	alice := createRunning(t, svc, "alice")
	if _, _, err := svc.CreateTenantAsync(ctx, CreateTenantInput{Username: "alice"}, AuditContext{}); err == nil {
		t.Fatal("duplicate username must be rejected")
	}
	// A leftover system account blocks creation.
	if err := svc.Sys.CreateUser(ctx, "bob", 6000); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.CreateTenantAsync(ctx, CreateTenantInput{Username: "bob"}, AuditContext{}); err == nil {
		t.Fatal("existing Linux user must be rejected")
	}
	// A tenant stuck in error asks the operator to remove it first.
	failed := &domain.Tenant{Username: "carol", Port: 1, Status: domain.StatusError, UnitName: svc.Cfg.UnitName("carol")}
	if err := svc.Store.CreateTenant(failed); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.CreateTenantAsync(ctx, CreateTenantInput{Username: "carol"}, AuditContext{}); err == nil {
		t.Fatal("a failed record must be cleared first")
	}
	// A trashed record with the same name is reported as such.
	if _, err := svc.DeleteTenant(ctx, alice.ID, "alice", AuditContext{}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.CreateTenantAsync(ctx, CreateTenantInput{Username: "alice"}, AuditContext{}); err == nil || !strings.Contains(err.Error(), "回收站") {
		t.Fatalf("trashed name = %v", err)
	}
}

func TestCreateTenantPortExhausted(t *testing.T) {
	svc := newService(t)
	ctx := context.Background()
	// Each tenant takes one port from a three-port range.
	for _, name := range []string{"alice", "bob"} {
		createRunning(t, svc, name)
	}
	if _, _, err := svc.CreateTenantAsync(ctx, CreateTenantInput{Username: "dave"}, AuditContext{}); !errors.Is(err, storePortExhausted()) {
		t.Fatalf("fourth tenant = %v", err)
	}
}

func TestCreateTenantWithOverrides(t *testing.T) {
	svc := newService(t)
	tenant, _, err := svc.CreateTenant(context.Background(), CreateTenantInput{
		Username: "dave", DisplayName: " Dave ", Notes: " ops ",
		CPUQuota: "50%", MemoryMax: "1G", TasksMax: 64,
	}, AuditContext{})
	if err != nil {
		t.Fatal(err)
	}
	if tenant.CPUQuota != "50%" || tenant.MemoryMax != "1G" || tenant.TasksMax != 64 || tenant.DisplayName != "Dave" || tenant.Notes != "ops" {
		t.Fatalf("override tenant = %+v", tenant)
	}
}

func TestBulkAndImportGuards(t *testing.T) {
	svc := newService(t)
	ctx := context.Background()
	if _, err := svc.BulkAction(ctx, "stop", nil, AuditContext{}); err == nil {
		t.Fatal("empty selection must be rejected")
	}
	if _, err := svc.BulkAction(ctx, "explode", []int64{1}, AuditContext{}); err == nil {
		t.Fatal("unknown action must be rejected")
	}
	if _, err := svc.ImportRows(ctx, nil, AuditContext{}); err == nil {
		t.Fatal("empty import must be rejected")
	}
}

func TestImportOfExistingNames(t *testing.T) {
	svc := newService(t)
	createRunning(t, svc, "alice")
	preview := svc.PreviewImport([]ImportRow{{Username: "alice"}, {Username: "  "}, {Username: "bob"}})
	if !strings.Contains(preview[0].Error, "已存在") {
		t.Fatalf("existing name = %+v", preview[0])
	}
	if preview[1].Error == "" {
		t.Fatalf("blank name = %+v", preview[1])
	}
	if preview[2].Error != "" {
		t.Fatalf("new name = %+v", preview[2])
	}
}

func TestDeleteFailureRestoresState(t *testing.T) {
	svc := newService(t)
	ctx := context.Background()
	alice := createRunning(t, svc, "alice")
	// Register a unit that cannot stop: the delete must roll the status back.
	if err := svc.Sys.Stop(ctx, alice.UnitName); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.DeleteTenant(ctx, alice.ID, "alice", AuditContext{}); err != nil {
		t.Fatalf("delete of a stopped instance should work: %v", err)
	}
	// A tenant whose home directory is gone cannot be moved to the trash.
	if _, _, err := svc.RestoreTenant(ctx, alice.ID, AuditContext{}); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(alice.HomeDir); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.DeleteTenant(ctx, alice.ID, "alice", AuditContext{}); err == nil {
		t.Fatal("deleting without a home directory should fail")
	}
	fresh, _ := svc.GetTenant(alice.ID)
	if fresh.Status == domain.StatusDeleting || fresh.Status == domain.StatusTrashed {
		t.Fatalf("failed delete left the tenant in %q", fresh.Status)
	}
}

func TestTenantDTOAndMetrics(t *testing.T) {
	svc := newService(t)
	alice := createRunning(t, svc, "alice")
	dto := svc.TenantDTO(*alice)
	if dto.Address == "" || dto.Protected {
		t.Fatalf("dto = %+v", dto)
	}
	svc.setMetrics(alice.ID, domain.Metrics{CPUPercent: 12.5, SampledAt: time.Now().Format(time.RFC3339)})
	if m := svc.GetMetrics(alice.ID); m.CPUPercent != 12.5 {
		t.Fatalf("metrics = %+v", m)
	}
	if _, err := svc.TenantDetail(999); err == nil {
		t.Fatal("detail for a missing tenant must fail")
	}
	items, total, err := svc.ListTenants(storeTenantFilter())
	if err != nil || total != 1 || len(items) != 1 {
		t.Fatalf("list = %d %d %v", len(items), total, err)
	}
}

func TestResetPasswordOfAdoptedTenantNeedsLookup(t *testing.T) {
	svc := newService(t)
	ctx := context.Background()
	alice := createRunning(t, svc, "alice")
	// A record without a UID (as an adopted record may be) needs a lookup first.
	alice.UID = 0
	if err := svc.Store.UpdateTenant(alice); err != nil {
		t.Fatal(err)
	}
	reset, password, err := svc.ResetPassword(ctx, alice.ID, AuditContext{})
	if err != nil || password == "" || reset.UID == 0 {
		t.Fatalf("reset = %+v %q %v", reset, password, err)
	}
}
