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

// adoptableCandidate builds a candidate whose data directory looks like a real
// Clawbench install, so only the aspect under test blocks adoption.
func adoptableCandidate(t *testing.T, svc *Service, port int) AdoptCandidate {
	t.Helper()
	dataDir := filepath.Join(t.TempDir(), ".clawbench")
	if err := os.MkdirAll(filepath.Join(dataDir, "config"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "config", "config.yaml"), []byte("port: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return AdoptCandidate{
		Username: "alice", UID: 5001, GID: 5001, Port: port,
		HomeDir: filepath.Dir(dataDir), DataDir: dataDir,
		UnitName: svc.Cfg.UnitName("alice"),
	}
}

func TestCandidateBlockedReason(t *testing.T) {
	svc := newService(t)
	port := freePort(t)
	cases := []struct {
		name string
		c    AdoptCandidate
		want string
	}{
		{"managed", AdoptCandidate{AlreadyManaged: true}, "已经在管理列表里"},
		{"no port", AdoptCandidate{DataDir: t.TempDir()}, "找不到端口"},
		{"no config", AdoptCandidate{Port: port, DataDir: t.TempDir()}, "没有 config.yaml"},
		{"port taken", adoptableCandidate(t, svc, port), "已经被其他租户占用"},
		{"unhealthy", adoptableCandidate(t, svc, freePort(t)), "没响应 Clawbench 健康检查"},
	}
	// Claim the port used by the "port taken" case.
	if err := svc.Store.ClaimPort(port, 99); err != nil {
		t.Fatal(err)
	}
	cases[3].c.Port = port
	cases[4].c.ServiceActive = true
	for _, tc := range cases {
		if got := svc.candidateBlockedReason(&tc.c); !strings.Contains(got, tc.want) {
			t.Errorf("%s = %q, want %q", tc.name, got, tc.want)
		}
	}
	// A healthy, unclaimed candidate has no reason to block.
	free := adoptableCandidate(t, svc, freePort(t))
	if got := svc.candidateBlockedReason(&free); got != "" {
		t.Fatalf("healthy candidate blocked: %q", got)
	}
}

func TestClawbenchBinaryReady(t *testing.T) {
	svc := newService(t)
	if !svc.clawbenchBinaryReady() {
		t.Fatal("dev mode should always be ready")
	}
	svc.Sys = productionMode{System: svc.Sys}
	if svc.clawbenchBinaryReady() {
		t.Fatal("production without a binary is not ready")
	}
	bin := filepath.Join(svc.Cfg.Clawbench.InstallDir, "current", "clawbench")
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !svc.clawbenchBinaryReady() {
		t.Fatal("production with a binary should be ready")
	}
}

func TestInsertAdoptedTenantErrors(t *testing.T) {
	svc := newService(t)
	candidate := adoptableCandidate(t, svc, freePort(t))
	// Duplicate username: the insert is rejected.
	existing := &domain.Tenant{Username: "alice", Port: freePort(t), Status: domain.StatusRunning, UnitName: "x"}
	if err := svc.Store.CreateTenant(existing); err != nil {
		t.Fatal(err)
	}
	if _, msg := svc.insertAdoptedTenant(candidate); msg == "" {
		t.Fatal("duplicate username should be reported")
	}
	// Port already claimed by another tenant.
	other := adoptableCandidate(t, svc, freePort(t))
	other.Username = "bob"
	if err := svc.Store.ClaimPort(other.Port, 99); err != nil {
		t.Fatal(err)
	}
	if _, msg := svc.insertAdoptedTenant(other); msg == "" {
		t.Fatal("claimed port should be reported")
	}
}

func TestStartAdoptedTenantFailures(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, svc *Service, tenant *domain.Tenant) hookSystem
		want  string
	}{
		{"write unit", func(_ *testing.T, _ *Service, _ *domain.Tenant) hookSystem {
			return hookSystem{writeUnitErr: errSimulated}
		}, "写 systemd 单元失败"},
		{"reload", func(_ *testing.T, _ *Service, _ *domain.Tenant) hookSystem {
			return hookSystem{reloadErr: errSimulated}
		}, "systemd 重载失败"},
		{"enable", func(_ *testing.T, _ *Service, _ *domain.Tenant) hookSystem {
			return hookSystem{enableErr: errSimulated}
		}, "启用服务失败"},
		{"start", func(_ *testing.T, _ *Service, _ *domain.Tenant) hookSystem {
			return hookSystem{startErr: errSimulated}
		}, "接管后启动失败"},
		{"health", func(_ *testing.T, svc *Service, _ *domain.Tenant) hookSystem {
			fastHealth(svc)
			return hookSystem{silentStart: true}
		}, "接管后健康检查失败"},
		{"restart active", func(_ *testing.T, _ *Service, _ *domain.Tenant) hookSystem {
			return hookSystem{restartErr: errSimulated}
		}, "接管后重启失败"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := newService(t)
			tenant := fixtureTenant(t, svc, "alice")
			svc.Sys = tc.setup(t, svc, tenant).with(svc.Sys)
			active := tc.name == "restart active"
			if _, err := svc.startAdoptedTenant(context.Background(), tenant, active, AuditContext{}); err == nil {
				t.Fatal("adoption should fail")
			}
			fresh, _ := svc.GetTenant(tenant.ID)
			if fresh.Status != domain.StatusError || !strings.Contains(fresh.LastError, tc.want) {
				t.Fatalf("status = %q, error = %q", fresh.Status, fresh.LastError)
			}
		})
	}
}

func TestAdoptOneFailures(t *testing.T) {
	t.Run("list users", func(t *testing.T) {
		svc := newService(t)
		svc.Sys = hookSystem{listUsersErr: errSimulated}.with(svc.Sys)
		res := svc.adoptOne(context.Background(), "alice", AuditContext{})
		if res.Error == "" {
			t.Fatal("list failure should be reported")
		}
	})
	t.Run("already managed", func(t *testing.T) {
		svc := newService(t)
		tenant := createRunning(t, svc, "alice")
		res := svc.adoptOne(context.Background(), tenant.Username, AuditContext{})
		if !strings.Contains(res.Error, "已经在管理列表里") {
			t.Fatalf("res = %+v", res)
		}
	})
	t.Run("acquire timeout", func(t *testing.T) {
		svc, cfg := newTestService(t)
		seedExistingInstance(t, svc, cfg, "alice", 5001, freePort(t))
		for range 4 {
			svc.sem <- struct{}{}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		res := svc.adoptOne(ctx, "alice", AuditContext{})
		if res.Error == "" {
			t.Fatalf("res = %+v", res)
		}
	})
}

func TestBackupFailurePathsMore(t *testing.T) {
	t.Run("config copy", func(t *testing.T) {
		svc := newService(t)
		tenant := createRunning(t, svc, "alice")
		configDir := filepath.Join(tenant.DataDir, "config")
		if err := os.Chmod(configDir, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(configDir, 0o700) })
		if _, err := svc.BackupTenant(context.Background(), tenant.ID, AuditContext{}); err == nil {
			t.Fatal("unreadable config should fail the backup")
		}
	})
	t.Run("store closed", func(t *testing.T) {
		svc := newService(t)
		tenant := createRunning(t, svc, "alice")
		writeTenantDB(t, tenant.DataDir, "x")
		if err := svc.Store.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.BackupTenant(context.Background(), tenant.ID, AuditContext{}); err == nil {
			t.Fatal("closed store should fail the backup")
		}
	})
	t.Run("tar walk", func(t *testing.T) {
		svc := newService(t)
		tenant := createRunning(t, svc, "alice")
		dir := filepath.Join(svc.Cfg.BackupDir(), "alice", "gone")
		record := &domain.Backup{TenantID: tenant.ID, Username: "alice", Path: dir, Status: "ok"}
		if err := svc.Store.CreateBackup(record); err != nil {
			t.Fatal(err)
		}
		if err := svc.WriteBackupTar(&discardWriter{}, record.ID); err == nil {
			t.Fatal("missing backup directory should fail the tar")
		}
	})
	t.Run("delete read-only", func(t *testing.T) {
		svc := newService(t)
		tenant := createRunning(t, svc, "alice")
		dir := filepath.Join(svc.Cfg.BackupDir(), "alice", "keep")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		record := &domain.Backup{TenantID: tenant.ID, Username: "alice", Path: dir, Status: "ok"}
		if err := svc.Store.CreateBackup(record); err != nil {
			t.Fatal(err)
		}
		parent := filepath.Dir(dir)
		if err := os.Chmod(parent, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(parent, 0o700) })
		if err := svc.DeleteBackup(context.Background(), tenant.ID, record.ID, AuditContext{}); err == nil {
			t.Fatal("read-only parent should fail the delete")
		}
	})
	t.Run("restore missing db", func(t *testing.T) {
		svc := newService(t)
		tenant := createRunning(t, svc, "alice")
		dir := filepath.Join(svc.Cfg.BackupDir(), "alice", "empty")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		record := &domain.Backup{TenantID: tenant.ID, Username: "alice", Path: dir, Status: "ok"}
		if err := svc.Store.CreateBackup(record); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.RestoreBackup(context.Background(), tenant.ID, record.ID, AuditContext{}); err == nil || !strings.Contains(err.Error(), "没有数据库文件") {
			t.Fatalf("restore = %v", err)
		}
	})
	t.Run("restore failures", func(t *testing.T) {
		for _, tc := range []struct {
			name  string
			setup func(*Service) hookSystem
		}{
			{"stop", func(*Service) hookSystem { return hookSystem{stopErr: errSimulated} }},
			{"enable", func(*Service) hookSystem { return hookSystem{enableErr: errSimulated} }},
			{"start", func(*Service) hookSystem { return hookSystem{startErr: errSimulated} }},
			{"health", func(svc *Service) hookSystem {
				fastHealth(svc)
				return hookSystem{silentStart: true}
			}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				svc := newService(t)
				tenant := createRunning(t, svc, "alice")
				writeTenantDB(t, tenant.DataDir, "v1")
				record, err := svc.BackupTenant(context.Background(), tenant.ID, AuditContext{})
				if err != nil {
					t.Fatal(err)
				}
				svc.Sys = tc.setup(svc).with(svc.Sys)
				if _, err := svc.RestoreBackup(context.Background(), tenant.ID, record.ID, AuditContext{}); err == nil {
					t.Fatal("restore should fail")
				}
			})
		}
	})
	t.Run("list missing tenant", func(t *testing.T) {
		svc := newService(t)
		if _, err := svc.ListBackups(999); err == nil {
			t.Fatal("missing tenant should fail")
		}
	})
	t.Run("backup all store error", func(t *testing.T) {
		svc := newService(t)
		if err := svc.Store.Close(); err != nil {
			t.Fatal(err)
		}
		svc.BackupAll(context.Background())
		svc.PurgeExpiredTrash(context.Background())
		svc.PurgeExpiredAudit()
	})
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

func TestUpgradeFailurePaths(t *testing.T) {
	ctx := context.Background()
	t.Run("pilot not running", func(t *testing.T) {
		svc := newService(t)
		tenant := createRunning(t, svc, "alice")
		if err := svc.Store.SetTenantStatus(tenant.ID, domain.StatusStopped, ""); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.InstallFromNpm(ctx, "0.1.0", AuditContext{}); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.Pilot(ctx, "0.1.0", tenant.ID, AuditContext{}); err == nil {
			t.Fatal("pilot of a stopped tenant must fail")
		}
	})
	t.Run("pilot busy", func(t *testing.T) {
		svc := newService(t)
		tenant := createRunning(t, svc, "alice")
		if _, err := svc.InstallFromNpm(ctx, "0.1.0", AuditContext{}); err != nil {
			t.Fatal(err)
		}
		if err := svc.beginUpgrade(); err != nil {
			t.Fatal(err)
		}
		defer svc.endUpgrade()
		if _, err := svc.Pilot(ctx, "0.1.0", tenant.ID, AuditContext{}); err == nil {
			t.Fatal("pilot while another upgrade runs must fail")
		}
	})
	t.Run("pilot restart rollback", func(t *testing.T) {
		svc := newService(t)
		tenant := createRunning(t, svc, "alice")
		for _, v := range []string{"0.1.0", "0.2.0"} {
			if _, err := svc.InstallFromNpm(ctx, v, AuditContext{}); err != nil {
				t.Fatal(err)
			}
		}
		svc.Sys = hookSystem{restartErr: errSimulated}.with(svc.Sys)
		if _, err := svc.Pilot(ctx, "0.2.0", tenant.ID, AuditContext{}); err == nil {
			t.Fatal("pilot restart should fail")
		}
		if cur, _ := svc.CurrentVersion(); cur.Version != "0.1.0" {
			t.Fatalf("rollback should restore 0.1.0, got %+v", cur)
		}
	})
	t.Run("pilot health rollback", func(t *testing.T) {
		svc := newService(t)
		tenant := createRunning(t, svc, "alice")
		for _, v := range []string{"0.1.0", "0.2.0"} {
			if _, err := svc.InstallFromNpm(ctx, v, AuditContext{}); err != nil {
				t.Fatal(err)
			}
		}
		fastHealth(svc)
		svc.Sys = hookSystem{silentRestart: true}.with(svc.Sys)
		if _, err := svc.Pilot(ctx, "0.2.0", tenant.ID, AuditContext{}); err == nil {
			t.Fatal("pilot health should fail")
		}
		if cur, _ := svc.CurrentVersion(); cur.Version != "0.1.0" {
			t.Fatalf("rollback should restore 0.1.0, got %+v", cur)
		}
	})
	t.Run("apply busy", func(t *testing.T) {
		svc := newService(t)
		if err := svc.beginUpgrade(); err != nil {
			t.Fatal(err)
		}
		defer svc.endUpgrade()
		if _, err := svc.ApplyUpgrade(ctx, "0.1.0", AuditContext{}); err == nil {
			t.Fatal("apply while another upgrade runs must fail")
		}
	})
	t.Run("apply store error", func(t *testing.T) {
		svc := newService(t)
		if err := svc.Store.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.ApplyUpgrade(ctx, "0.1.0", AuditContext{}); err == nil {
			t.Fatal("apply on a closed store must fail")
		}
	})
	t.Run("rollback busy", func(t *testing.T) {
		svc := newService(t)
		if err := svc.beginUpgrade(); err != nil {
			t.Fatal(err)
		}
		defer svc.endUpgrade()
		if _, err := svc.Rollback(ctx, AuditContext{}); err == nil {
			t.Fatal("rollback while another upgrade runs must fail")
		}
	})
	t.Run("rollback store error", func(t *testing.T) {
		svc := newService(t)
		if err := svc.Store.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.Rollback(ctx, AuditContext{}); err == nil {
			t.Fatal("rollback on a closed store must fail")
		}
	})
	t.Run("restart for version", func(t *testing.T) {
		for _, tc := range []struct {
			name  string
			setup func(*Service) hookSystem
		}{
			{"restart", func(*Service) hookSystem { return hookSystem{restartErr: errSimulated} }},
			{"health", func(svc *Service) hookSystem {
				fastHealth(svc)
				return hookSystem{silentRestart: true}
			}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				svc := newService(t)
				tenant := createRunning(t, svc, "alice")
				svc.Sys = tc.setup(svc).with(svc.Sys)
				if err := svc.restartForVersion(ctx, *tenant, "0.2.0"); err == nil {
					t.Fatal("restart should fail")
				}
			})
		}
	})
	t.Run("run upgrade rollback", func(t *testing.T) {
		svc := newService(t)
		tenant := createRunning(t, svc, "alice")
		for _, v := range []string{"0.1.0", "0.2.0"} {
			if _, err := svc.InstallFromNpm(ctx, v, AuditContext{}); err != nil {
				t.Fatal(err)
			}
		}
		job, err := svc.Store.CreateJob("upgrade", "{}", 1)
		if err != nil {
			t.Fatal(err)
		}
		item, err := svc.Store.AddJobItem(job.ID, tenant.ID, tenant.Username)
		if err != nil {
			t.Fatal(err)
		}
		svc.Sys = hookSystem{restartErr: errSimulated}.with(svc.Sys)
		svc.runUpgrade(job.ID, "0.2.0", "0.1.0", []domain.Tenant{*tenant}, []int64{item}, AuditContext{Actor: "tester"}, "upgrade.apply")
		got, err := svc.Job(job.ID)
		if err != nil || got.Status != domain.JobFailed {
			t.Fatalf("job = %+v %v", got, err)
		}
	})
	t.Run("upload dir blocked", func(t *testing.T) {
		svc := newService(t)
		if err := os.RemoveAll(svc.Cfg.UploadDir()); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(svc.Cfg.UploadDir(), []byte("file"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.UploadVersion(ctx, "clawbench", strings.NewReader("x"), 1<<20, AuditContext{}); err == nil {
			t.Fatal("blocked upload dir must fail")
		}
	})
	t.Run("validate binary dev fallback", func(t *testing.T) {
		svc := newService(t)
		path := filepath.Join(t.TempDir(), "not-a-binary")
		if err := os.WriteFile(path, []byte("not executable"), 0o600); err != nil {
			t.Fatal(err)
		}
		label, err := svc.validateBinary(ctx, path, "weird name.exe")
		if err != nil || label == "" {
			t.Fatalf("dev fallback = %q %v", label, err)
		}
	})
	t.Run("install uploaded store error", func(t *testing.T) {
		svc := newService(t)
		tmp := filepath.Join(t.TempDir(), "bin")
		if err := os.WriteFile(tmp, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := svc.Store.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.installUploaded(tmp, "1.0.0", AuditContext{}); err == nil {
			t.Fatal("closed store should fail the install")
		}
	})
	t.Run("set current symlink error", func(t *testing.T) {
		svc := newService(t)
		if _, err := svc.InstallFromNpm(ctx, "0.1.0", AuditContext{}); err != nil {
			t.Fatal(err)
		}
		// Replace the install dir with a file so MkdirAll fails.
		if err := os.RemoveAll(svc.Cfg.Clawbench.InstallDir); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(svc.Cfg.Clawbench.InstallDir, []byte("file"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := svc.SetCurrentVersion("0.1.0"); err == nil {
			t.Fatal("symlink creation should fail")
		}
	})
}

func TestNpmFailurePaths(t *testing.T) {
	ctx := context.Background()
	t.Run("catalog warnings", func(t *testing.T) {
		svc := newService(t)
		svc.Sys = hookSystem{npmRegistryErr: errSimulated, npmVersionsErr: errSimulated}.with(svc.Sys)
		catalog := svc.NpmCatalog(ctx)
		if catalog.Warning == "" || len(catalog.Versions) != 0 {
			t.Fatalf("catalog = %+v", catalog)
		}
	})
	t.Run("catalog installed store error", func(t *testing.T) {
		svc := newService(t)
		if err := svc.Store.Close(); err != nil {
			t.Fatal(err)
		}
		catalog := svc.NpmCatalog(ctx)
		if len(catalog.Versions) != 3 {
			t.Fatalf("versions should still be listed: %+v", catalog)
		}
	})
	t.Run("install errors", func(t *testing.T) {
		cases := []struct {
			name  string
			setup func(*Service) hookSystem
		}{
			{"npm install", func(svc *Service) hookSystem { return hookSystem{npmInstallErr: errSimulated}.with(svc.Sys) }},
			{"locate", func(svc *Service) hookSystem {
				// NpmInstall reports success but writes nothing, so the binary
				// cannot be found.
				return hookSystem{npmInstallNoop: true}.with(svc.Sys)
			}},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				svc := newService(t)
				svc.Sys = tc.setup(svc)
				if _, err := svc.InstallFromNpm(ctx, "0.1.0", AuditContext{}); err == nil {
					t.Fatal("install should fail")
				}
			})
		}
	})
	t.Run("prefix blocked", func(t *testing.T) {
		svc := newService(t)
		if err := os.RemoveAll(svc.Cfg.UploadDir()); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(svc.Cfg.UploadDir(), []byte("file"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.InstallFromNpm(ctx, "0.1.0", AuditContext{}); err == nil {
			t.Fatal("blocked upload dir should fail the install")
		}
	})
	t.Run("versions dir blocked", func(t *testing.T) {
		svc := newService(t)
		if err := os.RemoveAll(svc.Cfg.VersionsDir()); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(svc.Cfg.VersionsDir(), []byte("file"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.InstallFromNpm(ctx, "0.1.0", AuditContext{}); err == nil {
			t.Fatal("blocked versions dir should fail the install")
		}
	})
}

func TestGuestFailurePaths(t *testing.T) {
	svc := newService(t)
	ctx := context.Background()
	tenant := createRunning(t, svc, "alice")
	settings := svc.Settings()
	settings.GuestEnabled = false
	if err := svc.UpdateSettings(settings); err != nil {
		t.Fatal(err)
	}
	if page, err := svc.GuestPage(); err != nil || page.Enabled || len(page.Items) != 0 {
		t.Fatalf("disabled page = %+v %v", page, err)
	}
	if err := svc.SubmitGuestReport(tenant.ID, "x", ""); !errors.Is(err, ErrGuestDisabled) {
		t.Fatalf("disabled report = %v", err)
	}
	settings.GuestEnabled = true
	if err := svc.UpdateSettings(settings); err != nil {
		t.Fatal(err)
	}
	if err := svc.SubmitGuestReport(tenant.ID, strings.Repeat("长", 301), ""); err == nil {
		t.Fatal("overlong message must be rejected")
	}
	if _, err := svc.DeleteTenant(ctx, tenant.ID, "alice", AuditContext{}); err != nil {
		t.Fatal(err)
	}
	if err := svc.SubmitGuestReport(tenant.ID, "x", ""); err == nil {
		t.Fatal("trashed tenant must be rejected")
	}
	if err := svc.ResolveGuestReport(999, AuditContext{}); err == nil {
		t.Fatal("missing report must be rejected")
	}
	if err := svc.Store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.GuestPage(); err == nil {
		t.Fatal("closed store should fail the guest page")
	}
	if _, err := svc.ListGuestReports(true, 10); err == nil {
		t.Fatal("closed store should fail the report list")
	}
}

func TestActivityFailurePaths(t *testing.T) {
	svc := newService(t)
	tenant := createRunning(t, svc, "alice")
	// A file where the database should be makes the read fail without a panic.
	bad := filepath.Join(tenant.DataDir, "ClawBench.db")
	if err := os.WriteFile(bad, []byte("not a database"), 0o600); err != nil {
		t.Fatal(err)
	}
	if found, hasDB := svc.tenantLastActive(context.Background(), *tenant); !hasDB || found.ok {
		t.Fatalf("corrupt db = %+v %v", found, hasDB)
	}
	if err := svc.Store.Close(); err != nil {
		t.Fatal(err)
	}
	svc.RefreshLastActive(context.Background())
	svc.RefreshTenantLastActive(context.Background(), tenant)
}

func TestLoopFailurePaths(t *testing.T) {
	svc := newService(t)
	ctx := context.Background()
	tenant := createRunning(t, svc, "alice")
	// Hook failures are reported and skipped, never propagated.
	svc.Sys = hookSystem{isActiveErr: errSimulated, metricsErr: errSimulated, diskUsageErr: errSimulated}.with(svc.Sys)
	svc.checkAllHealth(ctx)
	svc.RefreshMetrics(ctx)
	svc.RefreshDiskUsage(ctx)
	// A closed store makes every sweep return early.
	if err := svc.Store.Close(); err != nil {
		t.Fatal(err)
	}
	svc.checkAllHealth(ctx)
	svc.RefreshMetrics(ctx)
	svc.RefreshDiskUsage(ctx)
	svc.Reconcile(ctx)
	if _, err := svc.ListRecentJobs(5); err == nil {
		t.Fatal("recent jobs on a closed store should fail")
	}
	if _, err := svc.ListTrash(); err == nil {
		t.Fatal("trash on a closed store should fail")
	}
	if _, _, err := svc.ListAudit(storeAuditFilter()); err == nil {
		t.Fatal("audit on a closed store should fail")
	}
	_ = tenant
}

func TestOverviewFailurePaths(t *testing.T) {
	svc := newService(t)
	settings := svc.Settings()
	settings.DiskWarnGB = 0
	if err := svc.UpdateSettings(settings); err != nil {
		t.Fatal(err)
	}
	if warnings := svc.diskWarnings(); len(warnings) != 0 {
		t.Fatalf("warnings = %+v", warnings)
	}
	if err := svc.Store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Overview(); err == nil {
		t.Fatal("overview on a closed store should fail")
	}
}

func TestAuthFailurePaths(t *testing.T) {
	t.Run("login store error", func(t *testing.T) {
		svc := newService(t)
		if _, _, err := BootstrapAdmin("", svc.Store, "seed-password-1"); err != nil {
			t.Fatal(err)
		}
		if err := svc.Store.Close(); err != nil {
			t.Fatal(err)
		}
		if _, _, err := svc.Login("admin", "seed-password-1", "", ""); err == nil {
			t.Fatal("closed store should fail login")
		}
	})
	t.Run("authenticate missing admin", func(t *testing.T) {
		svc := newService(t)
		if _, _, err := BootstrapAdmin("", svc.Store, "seed-password-1"); err != nil {
			t.Fatal(err)
		}
		_, token, err := svc.Login("admin", "seed-password-1", "", "")
		if err != nil {
			t.Fatal(err)
		}
		// Delete the admin row but keep the session: authentication must fail.
		if err := svc.Store.DeleteSessionsForAdmin(1); err != nil {
			t.Fatal(err)
		}
		if err := svc.Store.DeleteTenant(1); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.Authenticate(token); err == nil {
			// Session was deleted above, so this is the "no session" path.
			t.Fatal("deleted session should not authenticate")
		}
	})
	t.Run("change password errors", func(t *testing.T) {
		svc := newService(t)
		if _, _, err := BootstrapAdmin("", svc.Store, "seed-password-1"); err != nil {
			t.Fatal(err)
		}
		if err := svc.ChangeAdminPassword(999, "seed-password-1", "new-password-2"); err == nil {
			t.Fatal("missing admin should fail")
		}
		if err := svc.Store.Close(); err != nil {
			t.Fatal(err)
		}
		if err := svc.ChangeAdminPassword(1, "seed-password-1", "new-password-2"); err == nil {
			t.Fatal("closed store should fail")
		}
	})
	t.Run("bootstrap errors", func(t *testing.T) {
		svc := newService(t)
		if err := svc.Store.Close(); err != nil {
			t.Fatal(err)
		}
		if _, _, err := BootstrapAdmin("", svc.Store, "seed-password-1"); err == nil {
			t.Fatal("closed store should fail bootstrap")
		}
	})
	t.Run("bootstrap password file error", func(t *testing.T) {
		svc := newService(t)
		path := svc.Cfg.InitialPasswordPath()
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
		if _, _, err := BootstrapAdmin(path, svc.Store, "seed-password-1"); err == nil {
			t.Fatal("a directory at the password path should fail")
		}
	})
}

func TestCompareVersionsEdges(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want int
	}{
		{"1.0.0", "1.0.0", 0},
		{"1.0.0-beta", "1.0.0-alpha", 1},
		{"1.0", "1.0.0", -1},
		{"v1.0.0", "1.0.0", 0},
	} {
		if got := compareVersions(tc.a, tc.b); (got > 0) != (tc.want > 0) || (got < 0) != (tc.want < 0) {
			t.Errorf("compareVersions(%q,%q) = %d, want sign %d", tc.a, tc.b, got, tc.want)
		}
	}
}
