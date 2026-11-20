package fleet

import (
	"context"
	"database/sql"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"clawbench-fleet/internal/config"
	"clawbench-fleet/internal/domain"
	"clawbench-fleet/internal/store"
	"clawbench-fleet/internal/sysops"

	_ "modernc.org/sqlite"
)

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func newTestService(t *testing.T) (*Service, config.Config) {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Default()
	cfg.DataDir = filepath.Join(dir, "data")
	cfg.Clawbench.InstallDir = filepath.Join(dir, "opt")
	port := freePort(t)
	cfg.Clawbench.PortStart = port
	cfg.Clawbench.PortEnd = port + 1
	cfg.Clawbench.PortsPerTenant = 1
	cfg.Clawbench.UIDStart = 5000
	cfg.Clawbench.UIDEnd = 5010
	cfg.System.HomeRoot = filepath.Join(cfg.DataDir, "dev", "homes")
	cfg.System.SystemdDir = filepath.Join(cfg.DataDir, "dev", "units")
	cfg.Backup.Enabled = false
	if err := cfg.Normalize(); err != nil {
		t.Fatal(err)
	}
	if err := cfg.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(cfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	sys := sysops.NewFake(filepath.Join(cfg.DataDir, "dev"), cfg.Clawbench.InstallDir)
	svc, err := New(cfg, st, sys)
	if err != nil {
		t.Fatal(err)
	}
	return svc, cfg
}

func TestTenantLifecycle(t *testing.T) {
	svc, cfg := newTestService(t)
	ctx := context.Background()
	ac := AuditContext{Actor: "tester"}

	tenant, password, err := svc.CreateTenant(ctx, CreateTenantInput{
		Username: "alice", DisplayName: "张三", Notes: "研发部",
	}, ac)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if tenant.Status != domain.StatusRunning {
		t.Fatalf("status = %s", tenant.Status)
	}
	if len(password) != 12 {
		t.Fatalf("password length = %d", len(password))
	}
	if tenant.UID < 5000 || tenant.UID > 5010 {
		t.Fatalf("uid = %d", tenant.UID)
	}

	configData, err := os.ReadFile(cfg.TenantConfigPath("alice"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if !strings.Contains(string(configData), "sha256:") {
		t.Fatalf("config has no password hash:\n%s", configData)
	}
	if !strings.Contains(string(configData), "port: "+strconv.Itoa(tenant.Port)) {
		t.Fatalf("config has wrong port:\n%s", configData)
	}

	unitData, err := os.ReadFile(cfg.UnitPath("alice"))
	if err != nil {
		t.Fatalf("read unit: %v", err)
	}
	unit := string(unitData)
	for _, want := range []string{"User=alice", "--managed", "--port " + strconv.Itoa(tenant.Port), "CPUQuota=200%", "MemoryMax=4G"} {
		if !strings.Contains(unit, want) {
			t.Fatalf("unit missing %q:\n%s", want, unit)
		}
	}

	if info, err := svc.healthCheck(ctx, tenant.Port); err != nil || info.App != "clawbench" {
		t.Fatalf("health: %+v %v", info, err)
	}

	tenant, err = svc.StopTenant(ctx, tenant.ID, ac)
	if err != nil || tenant.Status != domain.StatusStopped {
		t.Fatalf("stop: %v %v", tenant, err)
	}
	if _, err := svc.healthCheck(ctx, tenant.Port); err == nil {
		t.Fatal("instance should be down after stop")
	}

	tenant, err = svc.StartTenant(ctx, tenant.ID, ac)
	if err != nil || tenant.Status != domain.StatusRunning {
		t.Fatalf("start: %v %v", tenant, err)
	}

	before, _ := os.ReadFile(cfg.TenantConfigPath("alice"))
	tenant, newPassword, err := svc.ResetPassword(ctx, tenant.ID, ac)
	if err != nil {
		t.Fatalf("reset password: %v", err)
	}
	if newPassword == password {
		t.Fatal("new password equals old password")
	}
	after, _ := os.ReadFile(cfg.TenantConfigPath("alice"))
	if string(before) == string(after) {
		t.Fatal("config did not change after password reset")
	}

	// Create a real SQLite database so backup/restore has something to copy.
	dbPath := filepath.Join(tenant.DataDir, "ClawBench.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE kv(k TEXT PRIMARY KEY, v TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO kv(k,v) VALUES('x','before')`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	backup, err := svc.BackupTenant(ctx, tenant.ID, ac)
	if err != nil {
		t.Fatalf("backup: %v", err)
	}
	if backup.Status != "ok" || !backup.IntegrityOK {
		t.Fatalf("backup = %+v", backup)
	}

	db, _ = sql.Open("sqlite", dbPath)
	if _, err := db.Exec(`UPDATE kv SET v='after' WHERE k='x'`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	tenant, err = svc.RestoreBackup(ctx, tenant.ID, backup.ID, ac)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	db, _ = sql.Open("sqlite", dbPath)
	var value string
	if err := db.QueryRow(`SELECT v FROM kv WHERE k='x'`).Scan(&value); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if value != "before" {
		t.Fatalf("restored value = %q", value)
	}

	// Delete to trash, then restore.
	trashed, err := svc.DeleteTenant(ctx, tenant.ID, "alice", ac)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if trashed.Status != domain.StatusTrashed {
		t.Fatalf("status = %s", trashed.Status)
	}
	if _, err := os.Stat(tenant.HomeDir); !os.IsNotExist(err) {
		t.Fatalf("home still exists: %v", err)
	}
	trash, err := svc.ListTrash()
	if err != nil || len(trash) != 1 {
		t.Fatalf("trash: %v %v", trash, err)
	}
	if trash[0].DaysLeft <= 0 {
		t.Fatalf("days left = %d", trash[0].DaysLeft)
	}

	restored, generated, err := svc.RestoreTenant(ctx, tenant.ID, ac)
	if err != nil {
		t.Fatalf("restore tenant: %v", err)
	}
	if restored.Status != domain.StatusRunning {
		t.Fatalf("status after restore = %s", restored.Status)
	}
	if generated != "" {
		t.Fatalf("config should have been preserved, got new password %q", generated)
	}
	if restored.Port != tenant.Port {
		t.Fatalf("port changed: %d -> %d", tenant.Port, restored.Port)
	}

	// Delete and purge, then make sure the port is reusable.
	if _, err := svc.DeleteTenant(ctx, tenant.ID, "alice", ac); err != nil {
		t.Fatalf("delete again: %v", err)
	}
	if err := svc.PurgeTenant(ctx, tenant.ID, ac); err != nil {
		t.Fatalf("purge: %v", err)
	}
	if _, err := svc.GetTenant(tenant.ID); err == nil {
		t.Fatal("tenant still exists after purge")
	}

	bob, _, err := svc.CreateTenant(ctx, CreateTenantInput{Username: "bob"}, ac)
	if err != nil {
		t.Fatalf("create bob: %v", err)
	}
	if bob.Port != tenant.Port {
		t.Fatalf("port not reused: got %d want %d", bob.Port, tenant.Port)
	}
	if _, err := svc.StopTenant(ctx, bob.ID, ac); err != nil {
		t.Fatal(err)
	}
}

func TestPreviewImport(t *testing.T) {
	svc, _ := newTestService(t)
	rows := []ImportRow{
		{Username: "alice"},
		{Username: "alice"},
		{Username: "bad name"},
		{Username: "root"},
	}
	preview := svc.PreviewImport(rows)
	if len(preview) != 4 {
		t.Fatalf("preview length = %d", len(preview))
	}
	if preview[0].Error != "" {
		t.Fatalf("first row should be valid: %s", preview[0].Error)
	}
	if preview[1].Error == "" {
		t.Fatal("duplicate should be rejected")
	}
	if preview[2].Error == "" {
		t.Fatal("invalid name should be rejected")
	}
	if preview[3].Error == "" {
		t.Fatal("reserved name should be rejected")
	}
}

func TestPreviewImportPortCapacity(t *testing.T) {
	svc, _ := newTestService(t)
	st := svc.Settings()
	st.PortsPerTenant = 2 // the test range only has two ports, so one group
	if err := svc.UpdateSettings(st); err != nil {
		t.Fatal(err)
	}
	preview := svc.PreviewImport([]ImportRow{{Username: "alice"}, {Username: "bob"}})
	if preview[0].Error != "" {
		t.Fatalf("first row should fit: %s", preview[0].Error)
	}
	if preview[1].Error == "" {
		t.Fatal("second row should be rejected because there is only one port group")
	}
}

func TestDiskWarning(t *testing.T) {
	svc, _ := newTestService(t)
	st := svc.Settings()
	st.DiskWarnGB = 1
	if err := svc.UpdateSettings(st); err != nil {
		t.Fatal(err)
	}
	tenant, _, err := svc.CreateTenant(context.Background(), CreateTenantInput{Username: "dave"}, AuditContext{Actor: "tester"})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Store.SetTenantDisk(tenant.ID, 2*1024*1024*1024, time.Now().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	ov, err := svc.Overview()
	if err != nil {
		t.Fatal(err)
	}
	if len(ov.DiskWarnings) != 1 || ov.DiskWarnings[0].Username != "dave" {
		t.Fatalf("disk warnings = %+v", ov.DiskWarnings)
	}
	if _, err := svc.StopTenant(context.Background(), tenant.ID, AuditContext{Actor: "tester"}); err != nil {
		t.Fatal(err)
	}
}

func TestPurgeRequiresTrash(t *testing.T) {
	svc, _ := newTestService(t)
	ac := AuditContext{Actor: "tester"}
	tenant, _, err := svc.CreateTenant(context.Background(), CreateTenantInput{Username: "erin"}, ac)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.PurgeTenant(context.Background(), tenant.ID, ac); err == nil {
		t.Fatal("purge must be rejected while the tenant is not in the trash")
	}
	if _, err := svc.StopTenant(context.Background(), tenant.ID, ac); err != nil {
		t.Fatal(err)
	}
}

func TestSettingsValidation(t *testing.T) {
	svc, _ := newTestService(t)
	bad := svc.Settings()
	bad.PortStart = bad.PortEnd
	if err := svc.UpdateSettings(bad); err == nil {
		t.Fatal("invalid port range should be rejected")
	}
	bad = svc.Settings()
	bad.DefaultCPUQuota = "lots"
	if err := svc.UpdateSettings(bad); err == nil {
		t.Fatal("invalid cpu quota should be rejected")
	}
}
