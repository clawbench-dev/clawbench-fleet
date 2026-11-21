package fleet

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"clawbench-fleet/internal/domain"
	"clawbench-fleet/internal/sysops"

	_ "modernc.org/sqlite"
)

// nonDevSystem keeps the simulated backend but reports production mode, so
// dev-only shortcuts can be tested.
type nonDevSystem struct {
	sysops.System
}

func (nonDevSystem) Mode() string { return "systemd" }

// writeTenantDB creates a small SQLite file where the tenant's own database
// lives, so backup and restore have something real to copy.
func writeTenantDB(t *testing.T, dataDir, value string) {
	t.Helper()
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dataDir, "ClawBench.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS kv(k TEXT PRIMARY KEY, v TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO kv(k, v) VALUES('x', ?) ON CONFLICT(k) DO UPDATE SET v=excluded.v`, value); err != nil {
		t.Fatal(err)
	}
}

func readTenantValue(t *testing.T, dataDir string) string {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(dataDir, "ClawBench.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var value string
	if err := db.QueryRow(`SELECT v FROM kv WHERE k='x'`).Scan(&value); err != nil {
		t.Fatalf("read value: %v", err)
	}
	return value
}

func TestBackupFailurePaths(t *testing.T) {
	svc := newService(t)
	ctx := context.Background()
	alice := createRunning(t, svc, "alice")

	// A tenant with neither a database nor a config directory cannot be backed up.
	if err := os.RemoveAll(filepath.Join(alice.DataDir, "config")); err != nil {
		t.Fatal(err)
	}
	if _, os := svc.BackupTenant(ctx, alice.ID, AuditContext{}); os == nil {
		t.Fatal("empty tenant backup should fail")
	}
	// The failed attempt is still recorded so the operator can see why.
	backups, err := svc.ListBackups(alice.ID)
	if err != nil || len(backups) == 0 || backups[0].Status != statusFailed {
		t.Fatalf("failed backup not recorded: %+v %v", backups, err)
	}

	// A corrupt database is rejected by the integrity check.
	if err := os.MkdirAll(alice.DataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(alice.DataDir, "ClawBench.db"), []byte("not a database"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, os := svc.BackupTenant(ctx, alice.ID, AuditContext{}); os == nil {
		t.Fatal("corrupt database backup should fail")
	}

	if _, os := svc.BackupTenant(ctx, 999, AuditContext{}); os == nil {
		t.Fatal("missing tenant backup should fail")
	}
}

func TestBackupSnapshotOfRunningInstance(t *testing.T) {
	svc := newService(t)
	ctx := context.Background()
	alice := createRunning(t, svc, "alice")
	writeTenantDB(t, alice.DataDir, "before")
	backup, err := svc.BackupTenant(ctx, alice.ID, AuditContext{})
	if err != nil {
		t.Fatalf("backup: %v", err)
	}
	if backup.SizeBytes == 0 || !backup.IntegrityOK {
		t.Fatalf("backup = %+v", backup)
	}
	writeTenantDB(t, alice.DataDir, "after")
	if _, err := svc.RestoreBackup(ctx, alice.ID, backup.ID, AuditContext{}); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := readTenantValue(t, alice.DataDir); got != "before" {
		t.Fatalf("restored value = %q", got)
	}
	// Restoring a backup that belongs to another tenant is refused.
	if _, os := svc.RestoreBackup(ctx, 999, backup.ID, AuditContext{}); os == nil {
		t.Fatal("restore for missing tenant should fail")
	}
	if _, os := svc.RestoreBackup(ctx, alice.ID, 999, AuditContext{}); os == nil {
		t.Fatal("restore of missing backup should fail")
	}
}

func TestBackupTarAndDelete(t *testing.T) {
	svc := newService(t)
	ctx := context.Background()
	alice := createRunning(t, svc, "alice")
	writeTenantDB(t, alice.DataDir, "payload")
	backup, err := svc.BackupTenant(ctx, alice.ID, AuditContext{})
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := svc.WriteBackupTar(&buf, backup.ID); err != nil {
		t.Fatalf("tar: %v", err)
	}
	gz, err := gzip.NewReader(&buf)
	if err != nil {
		t.Fatalf("gzip: %v", err)
	}
	tr := tar.NewReader(gz)
	names := map[string]bool{}
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("tar read: %v", err)
		}
		names[hdr.Name] = true
	}
	if !names["ClawBench.db"] || !names["config/config.yaml"] {
		t.Fatalf("tar contents = %v", names)
	}
	if err := svc.WriteBackupTar(io.Discard, 999); err == nil {
		t.Fatal("missing backup tar should fail")
	}
	if err := svc.DeleteBackup(ctx, alice.ID, backup.ID, AuditContext{}); err != nil {
		t.Fatalf("delete backup: %v", err)
	}
	if err := svc.DeleteBackup(ctx, alice.ID, backup.ID, AuditContext{}); err == nil {
		t.Fatal("deleting twice should fail")
	}
	if err := svc.DeleteBackup(ctx, 999, backup.ID, AuditContext{}); err == nil {
		t.Fatal("delete for missing tenant should fail")
	}
}

func TestWriteBackupTarRejectsForeignPath(t *testing.T) {
	svc := newService(t)
	record := &domain.Backup{TenantID: 1, Username: "alice", Path: "/etc", Status: "ok"}
	if err := svc.Store.CreateBackup(record); err != nil {
		t.Fatal(err)
	}
	if err := svc.WriteBackupTar(io.Discard, record.ID); err == nil {
		t.Fatal("a backup outside the backup dir must not be served")
	}
	if err := svc.DeleteBackup(context.Background(), 1, record.ID, AuditContext{}); err == nil {
		t.Fatal("deleting a tenant that does not exist should fail")
	}
}

func TestBackupTenantOfTrashedTenant(t *testing.T) {
	svc := newService(t)
	ctx := context.Background()
	alice := createRunning(t, svc, "alice")
	if _, err := svc.DeleteTenant(ctx, alice.ID, "alice", AuditContext{}); err != nil {
		t.Fatal(err)
	}
	if _, svc := svc.BackupTenant(ctx, alice.ID, AuditContext{}); svc == nil {
		t.Fatal("trashed tenants cannot be backed up")
	}
}

func TestPruneBackups(t *testing.T) {
	svc := newService(t)
	alice := createRunning(t, svc, "alice")
	dir := filepath.Join(svc.Cfg.BackupDir(), "alice", "old")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	old := &domain.Backup{
		TenantID: alice.ID, Username: "alice", Path: dir, Status: "ok",
		CreatedAt: time.Now().AddDate(0, 0, -30).Format(time.RFC3339),
	}
	if err := svc.Store.CreateBackup(old); err != nil {
		t.Fatal(err)
	}
	svc.pruneBackups(alice)
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("old backup dir should be removed: %v", err)
	}
	if backups, _ := svc.ListBackups(alice.ID); len(backups) != 0 {
		t.Fatalf("old backup record should be removed: %+v", backups)
	}
}

func TestBackupAll(t *testing.T) {
	svc := newService(t)
	alice := createRunning(t, svc, "alice")
	writeTenantDB(t, alice.DataDir, "x")
	svc.BackupAll(context.Background())
	if backups, _ := svc.ListBackups(alice.ID); len(backups) != 1 {
		t.Fatalf("backup all created %d backups", len(backups))
	}
}

func TestUpgradePilotAndRollback(t *testing.T) {
	svc := newService(t)
	ctx := context.Background()
	alice := createRunning(t, svc, "alice")
	for _, version := range []string{"0.1.0", "0.2.0"} {
		if _, err := svc.InstallFromNpm(ctx, version, AuditContext{}); err != nil {
			t.Fatal(err)
		}
	}
	if cur, _ := svc.CurrentVersion(); cur.Version != "0.1.0" {
		t.Fatalf("first install should be current, got %+v", cur)
	}
	if _, err := svc.Pilot(ctx, "0.2.0", alice.ID, AuditContext{}); err != nil {
		t.Fatalf("pilot: %v", err)
	}
	if cur, _ := svc.CurrentVersion(); cur.Version != "0.2.0" {
		t.Fatalf("pilot should move current, got %+v", cur)
	}
	if _, svc := svc.Pilot(ctx, "0.2.0", 999, AuditContext{}); svc == nil {
		t.Fatal("pilot on a missing tenant must fail")
	}

	// Switch back so there is a current and a previous version to roll between.
	if err := svc.SetCurrentVersion("0.1.0"); err != nil {
		t.Fatal(err)
	}
	previous, err := svc.Store.PreviousVersion()
	if err != nil {
		t.Fatalf("previous version: %v", err)
	}
	if previous.Version != "0.2.0" {
		t.Fatalf("previous = %+v", previous)
	}
	job, err := svc.Rollback(ctx, AuditContext{})
	if err != nil {
		t.Fatalf("rollback: %v", err)
	}
	waitJob(t, svc, job.ID, domain.JobSucceeded)
	if cur, _ := svc.CurrentVersion(); cur.Version != previous.Version {
		t.Fatalf("rollback should switch to %s, got %+v", previous.Version, cur)
	}
}

func TestApplyUpgradeGuards(t *testing.T) {
	svc := newService(t)
	ctx := context.Background()
	if _, svc := svc.ApplyUpgrade(ctx, "9.9.9", AuditContext{}); svc == nil {
		t.Fatal("unknown version must be rejected")
	}
	if _, err := svc.InstallFromNpm(ctx, "0.1.0", AuditContext{}); err != nil {
		t.Fatal(err)
	}
	if _, svc := svc.ApplyUpgrade(ctx, "0.1.0", AuditContext{}); svc == nil {
		t.Fatal("applying with no running tenants must fail")
	}
	if _, svc := svc.Rollback(ctx, AuditContext{}); svc == nil {
		t.Fatal("rollback without a previous version must fail")
	}
}

func TestBeginUpgradeSerializes(t *testing.T) {
	svc := newService(t)
	if err := svc.beginUpgrade(); err != nil {
		t.Fatal(err)
	}
	if err := svc.beginUpgrade(); err == nil {
		t.Fatal("a second upgrade must be refused")
	}
	svc.endUpgrade()
	if err := svc.beginUpgrade(); err != nil {
		t.Fatal(err)
	}
	svc.endUpgrade()
}

func TestUploadVersionRealMode(t *testing.T) {
	svc := newService(t)
	ctx := context.Background()
	// Switch the backend out of dev mode: the uploaded file is then expected to
	// be a real Clawbench binary.
	svc.Sys = nonDevSystem{System: svc.Sys}
	if _, svc := svc.UploadVersion(ctx, "clawbench", strings.NewReader("not a binary"), 1<<20, AuditContext{}); svc == nil {
		t.Fatal("a non-executable upload must be rejected outside dev mode")
	}
	if _, svc := svc.UploadVersion(ctx, "clawbench", strings.NewReader(""), 1<<20, AuditContext{}); svc == nil {
		t.Fatal("an empty upload must be rejected")
	}
	if _, svc := svc.UploadVersion(ctx, "clawbench", strings.NewReader(strings.Repeat("x", 100)), 10, AuditContext{}); svc == nil {
		t.Fatal("an oversized upload must be rejected")
	}
}

func TestHashFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(path, []byte("abc"), 0o600); err != nil {
		t.Fatal(err)
	}
	sum, err := hashFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// sha256("abc")
	if sum != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Fatalf("sha256 = %s", sum)
	}
	if _, os := hashFile(filepath.Join(filepath.Dir(path), "missing")); os == nil {
		t.Fatal("missing file must fail")
	}
}

func TestLocateNpmBinaryFallback(t *testing.T) {
	prefix := t.TempDir()
	// Layout the package somewhere the expected path does not look.
	fallback := filepath.Join(prefix, "node_modules", "@other", "pkg", "bin", "clawbench")
	if err := os.MkdirAll(filepath.Dir(fallback), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fallback, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := locateNpmBinary(prefix, "@scope/clawbench")
	if err != nil || got == "" || !strings.HasSuffix(got, "clawbench") {
		t.Fatalf("locate = %q %v", got, err)
	}
	if _, os := locateNpmBinary(t.TempDir(), "@scope/clawbench"); os == nil {
		t.Fatal("empty install must report a missing binary")
	}
	// An unscoped package name still falls back to the generic search.
	if got, err := locateNpmBinary(prefix, "not-scoped"); err != nil || got != fallback {
		t.Fatalf("unscoped fallback = %q %v", got, err)
	}
}
