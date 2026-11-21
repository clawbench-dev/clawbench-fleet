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

var errSimulated = errors.New("simulated failure")

func fastHealth(svc *Service) {
	svc.healthTimeout = 150 * time.Millisecond
}

// fixtureTenant writes a tenant record without provisioning it, so a test can
// call provisionTenant (or a later step) with a known starting point.
func fixtureTenant(t *testing.T, svc *Service, username string) *domain.Tenant {
	t.Helper()
	tenant := &domain.Tenant{
		Username: username, DisplayName: username, UID: 5001, GID: 5001,
		Port: freePort(t), Status: domain.StatusCreating,
		CPUQuota: "200%", MemoryMax: "4G", TasksMax: 512,
		HomeDir: svc.Cfg.TenantHome(username), DataDir: svc.Cfg.TenantDataDir(username),
		UnitName: svc.Cfg.UnitName(username),
	}
	if err := svc.Store.CreateTenant(tenant); err != nil {
		t.Fatal(err)
	}
	return tenant
}

// blockConfigDir puts a regular file where the config directory should be, so
// writeTenantConfig fails at MkdirAll.
func blockConfigDir(t *testing.T, dataDir string) {
	t.Helper()
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(dataDir, "config")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "config"), []byte("not a dir"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestProvisionTenantFailurePaths(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, svc *Service, tenant *domain.Tenant) hookSystem
		want  string
	}{
		{
			name: "create user",
			setup: func(_ *testing.T, _ *Service, _ *domain.Tenant) hookSystem {
				return hookSystem{createUserErr: errSimulated}
			},
			want: "创建 Linux 用户失败",
		},
		{
			name: "write config",
			setup: func(t *testing.T, _ *Service, tenant *domain.Tenant) hookSystem {
				blockConfigDir(t, tenant.DataDir)
				return hookSystem{}
			},
			want: "写租户配置失败",
		},
		{
			name: "write unit",
			setup: func(_ *testing.T, _ *Service, _ *domain.Tenant) hookSystem {
				return hookSystem{writeUnitErr: errSimulated}
			},
			want: "写 systemd 单元失败",
		},
		{
			name: "reload units",
			setup: func(_ *testing.T, _ *Service, _ *domain.Tenant) hookSystem {
				return hookSystem{reloadErr: errSimulated}
			},
			want: "systemd 重载失败",
		},
		{
			name: "enable",
			setup: func(_ *testing.T, _ *Service, _ *domain.Tenant) hookSystem {
				return hookSystem{enableErr: errSimulated}
			},
			want: "启用服务失败",
		},
		{
			name: "start",
			setup: func(_ *testing.T, _ *Service, _ *domain.Tenant) hookSystem {
				return hookSystem{startErr: errSimulated}
			},
			want: "启动服务失败",
		},
		{
			name: "health",
			setup: func(_ *testing.T, svc *Service, _ *domain.Tenant) hookSystem {
				fastHealth(svc)
				return hookSystem{silentStart: true}
			},
			want: "实例已创建但未就绪",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := newService(t)
			tenant := fixtureTenant(t, svc, "alice")
			svc.Sys = tc.setup(t, svc, tenant).with(svc.Sys)
			svc.provisionTenant(context.Background(), tenant, "password-1", AuditContext{Actor: "tester"})
			fresh, err := svc.GetTenant(tenant.ID)
			if err != nil {
				t.Fatal(err)
			}
			if fresh.Status != domain.StatusError || !strings.Contains(fresh.LastError, tc.want) {
				t.Fatalf("status = %q, last error = %q (want %q)", fresh.Status, fresh.LastError, tc.want)
			}
		})
	}
}

func TestRollbackCreate(t *testing.T) {
	svc := newService(t)
	ctx := context.Background()
	tenant := fixtureTenant(t, svc, "alice")
	if err := svc.Sys.CreateUser(ctx, "alice", tenant.UID); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(tenant.HomeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := svc.Sys.WriteUnit(ctx, tenant.UnitName, "[Service]\n"); err != nil {
		t.Fatal(err)
	}
	svc.rollbackCreate(ctx, tenant, true)
	if _, err := svc.GetTenant(tenant.ID); err == nil {
		t.Fatal("rollback should delete the record")
	}
	if _, err := os.Stat(tenant.HomeDir); !os.IsNotExist(err) {
		t.Fatalf("rollback should remove the home directory: %v", err)
	}

	// deleteRecord=false keeps the record so the operator can inspect it.
	kept := fixtureTenant(t, svc, "bob")
	svc.rollbackCreate(ctx, kept, false)
	if _, err := svc.GetTenant(kept.ID); err != nil {
		t.Fatalf("record should survive: %v", err)
	}
}

func TestWaitForCreation(t *testing.T) {
	svc := newService(t)
	svc.provisioners = &provisionTracker{done: map[int64]chan struct{}{1: make(chan struct{})}}
	if msg, ok := svc.waitForCreation(context.Background(), 2, time.Second); !ok || msg != "" {
		t.Fatalf("unknown id = %q %v", msg, ok)
	}
	if msg, ok := svc.waitForCreation(context.Background(), 1, 20*time.Millisecond); ok || msg == "" {
		t.Fatalf("timeout = %q %v", msg, ok)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if msg, ok := svc.waitForCreation(ctx, 1, time.Second); ok || msg == "" {
		t.Fatalf("cancelled = %q %v", msg, ok)
	}
	close(svc.provisioners.done[1])
	if msg, ok := svc.waitForCreation(context.Background(), 1, time.Second); !ok || msg != "" {
		t.Fatalf("closed channel = %q %v", msg, ok)
	}
}

func TestStartTenantFailurePaths(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, svc *Service, tenant *domain.Tenant) hookSystem
		want  string
	}{
		{
			name: "enable",
			setup: func(_ *testing.T, _ *Service, _ *domain.Tenant) hookSystem {
				return hookSystem{enableErr: errSimulated}
			},
			want: "启动失败",
		},
		{
			name: "start",
			setup: func(_ *testing.T, _ *Service, _ *domain.Tenant) hookSystem {
				return hookSystem{startErr: errSimulated}
			},
			want: "启动失败",
		},
		{
			name: "health",
			setup: func(t *testing.T, svc *Service, tenant *domain.Tenant) hookSystem {
				if err := svc.Sys.Stop(context.Background(), tenant.UnitName); err != nil {
					t.Fatal(err)
				}
				fastHealth(svc)
				return hookSystem{silentStart: true}
			},
			want: "启动后健康检查失败",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := newService(t)
			tenant := createRunning(t, svc, "alice")
			svc.Sys = tc.setup(t, svc, tenant).with(svc.Sys)
			if _, err := svc.StartTenant(context.Background(), tenant.ID, AuditContext{}); err == nil {
				t.Fatal("start should fail")
			}
			fresh, _ := svc.GetTenant(tenant.ID)
			if fresh.Status != domain.StatusError || !strings.Contains(fresh.LastError, tc.want) {
				t.Fatalf("status = %q, error = %q", fresh.Status, fresh.LastError)
			}
		})
	}
}

func TestStopTenantFailurePaths(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup hookSystem
		want  string
	}{
		{"disable", hookSystem{disableErr: errSimulated}, "停止失败"},
		{"stop", hookSystem{stopErr: errSimulated}, "停止失败"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := newService(t)
			tenant := createRunning(t, svc, "alice")
			svc.Sys = tc.setup.with(svc.Sys)
			if _, err := svc.StopTenant(context.Background(), tenant.ID, AuditContext{}); err == nil {
				t.Fatal("stop should fail")
			}
			fresh, _ := svc.GetTenant(tenant.ID)
			if fresh.Status != domain.StatusError || !strings.Contains(fresh.LastError, tc.want) {
				t.Fatalf("status = %q, error = %q", fresh.Status, fresh.LastError)
			}
		})
	}
}

func TestRestartTenantFailurePaths(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, svc *Service) hookSystem
		want  string
	}{
		{"restart", func(_ *testing.T, _ *Service) hookSystem { return hookSystem{restartErr: errSimulated} }, "重启失败"},
		{"health", func(t *testing.T, svc *Service) hookSystem {
			fastHealth(svc)
			return hookSystem{silentRestart: true}
		}, "重启后健康检查失败"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := newService(t)
			tenant := createRunning(t, svc, "alice")
			svc.Sys = tc.setup(t, svc).with(svc.Sys)
			if _, err := svc.RestartTenant(context.Background(), tenant.ID, AuditContext{}); err == nil {
				t.Fatal("restart should fail")
			}
			fresh, _ := svc.GetTenant(tenant.ID)
			if fresh.Status != domain.StatusError || !strings.Contains(fresh.LastError, tc.want) {
				t.Fatalf("status = %q, error = %q", fresh.Status, fresh.LastError)
			}
		})
	}
}

func TestResetPasswordFailurePaths(t *testing.T) {
	t.Run("lookup", func(t *testing.T) {
		svc := newService(t)
		tenant := createRunning(t, svc, "alice")
		tenant.UID = 0
		if err := svc.Store.UpdateTenant(tenant); err != nil {
			t.Fatal(err)
		}
		svc.Sys = hookSystem{lookupErr: errSimulated}.with(svc.Sys)
		if _, _, err := svc.ResetPassword(context.Background(), tenant.ID, AuditContext{}); err == nil {
			t.Fatal("lookup failure should abort the reset")
		}
	})
	t.Run("write config", func(t *testing.T) {
		svc := newService(t)
		tenant := createRunning(t, svc, "alice")
		blockConfigDir(t, tenant.DataDir)
		if _, _, err := svc.ResetPassword(context.Background(), tenant.ID, AuditContext{}); err == nil || !strings.Contains(err.Error(), "写配置失败") {
			t.Fatalf("write config = %v", err)
		}
	})
	t.Run("restart", func(t *testing.T) {
		svc := newService(t)
		tenant := createRunning(t, svc, "alice")
		svc.Sys = hookSystem{restartErr: errSimulated}.with(svc.Sys)
		_, password, err := svc.ResetPassword(context.Background(), tenant.ID, AuditContext{})
		if err == nil || password == "" || !strings.Contains(err.Error(), "重启失败") {
			t.Fatalf("restart = %q %v", password, err)
		}
	})
	t.Run("health", func(t *testing.T) {
		svc := newService(t)
		tenant := createRunning(t, svc, "alice")
		fastHealth(svc)
		svc.Sys = hookSystem{silentRestart: true}.with(svc.Sys)
		_, _, err := svc.ResetPassword(context.Background(), tenant.ID, AuditContext{})
		if err == nil || !strings.Contains(err.Error(), "实例未就绪") {
			t.Fatalf("health = %v", err)
		}
	})
}

func TestRepairTenantFailurePaths(t *testing.T) {
	t.Run("lookup", func(t *testing.T) {
		svc := newService(t)
		tenant := createRunning(t, svc, "alice")
		tenant.UID = 0
		if err := svc.Store.UpdateTenant(tenant); err != nil {
			t.Fatal(err)
		}
		svc.Sys = hookSystem{lookupErr: errSimulated}.with(svc.Sys)
		if _, _, err := svc.RepairTenant(context.Background(), tenant.ID, AuditContext{}); err == nil || !strings.Contains(err.Error(), "无法修复") {
			t.Fatalf("lookup = %v", err)
		}
	})
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, svc *Service, tenant *domain.Tenant) hookSystem
		want  string
	}{
		{"write config", func(t *testing.T, _ *Service, tenant *domain.Tenant) hookSystem {
			blockConfigDir(t, tenant.DataDir)
			return hookSystem{}
		}, "config"},
		{"write unit", func(_ *testing.T, _ *Service, _ *domain.Tenant) hookSystem {
			return hookSystem{writeUnitErr: errSimulated}
		}, "simulated"},
		{"reload", func(_ *testing.T, _ *Service, _ *domain.Tenant) hookSystem {
			return hookSystem{reloadErr: errSimulated}
		}, "simulated"},
		{"restart", func(_ *testing.T, _ *Service, _ *domain.Tenant) hookSystem {
			return hookSystem{restartErr: errSimulated}
		}, "simulated"},
		{"health", func(_ *testing.T, svc *Service, _ *domain.Tenant) hookSystem {
			fastHealth(svc)
			return hookSystem{silentRestart: true}
		}, "实例"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := newService(t)
			tenant := createRunning(t, svc, "alice")
			svc.Sys = tc.setup(t, svc, tenant).with(svc.Sys)
			if _, _, err := svc.RepairTenant(context.Background(), tenant.ID, AuditContext{}); err == nil {
				t.Fatal("repair should fail")
			}
		})
	}
}

func TestDeleteTenantFailurePaths(t *testing.T) {
	t.Run("stop", func(t *testing.T) {
		svc := newService(t)
		tenant := createRunning(t, svc, "alice")
		svc.Sys = hookSystem{stopErr: errSimulated}.with(svc.Sys)
		if _, err := svc.DeleteTenant(context.Background(), tenant.ID, "alice", AuditContext{}); err == nil {
			t.Fatal("stop failure should abort the delete")
		}
		fresh, _ := svc.GetTenant(tenant.ID)
		if fresh.Status == domain.StatusDeleting {
			t.Fatal("status should be rolled back")
		}
	})
	t.Run("trash dir", func(t *testing.T) {
		svc := newService(t)
		tenant := createRunning(t, svc, "alice")
		if err := os.RemoveAll(svc.Cfg.TrashDir()); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(svc.Cfg.TrashDir(), []byte("file"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.DeleteTenant(context.Background(), tenant.ID, "alice", AuditContext{}); err == nil {
			t.Fatal("trash dir failure should abort the delete")
		}
	})
	t.Run("move home", func(t *testing.T) {
		svc := newService(t)
		tenant := createRunning(t, svc, "alice")
		if err := os.RemoveAll(tenant.HomeDir); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.DeleteTenant(context.Background(), tenant.ID, "alice", AuditContext{}); err == nil || !strings.Contains(err.Error(), "移动家目录失败") {
			t.Fatalf("move = %v", err)
		}
	})
	t.Run("delete user", func(t *testing.T) {
		svc := newService(t)
		tenant := createRunning(t, svc, "alice")
		svc.Sys = hookSystem{deleteUserErr: errSimulated}.with(svc.Sys)
		if _, err := svc.DeleteTenant(context.Background(), tenant.ID, "alice", AuditContext{}); err == nil || !strings.Contains(err.Error(), "删除 Linux 用户失败") {
			t.Fatalf("delete user = %v", err)
		}
	})
}

func TestRestoreTenantFailurePaths(t *testing.T) {
	setup := func(t *testing.T) (*Service, *domain.Tenant) {
		t.Helper()
		svc := newService(t)
		tenant := createRunning(t, svc, "alice")
		if _, err := svc.DeleteTenant(context.Background(), tenant.ID, "alice", AuditContext{}); err != nil {
			t.Fatal(err)
		}
		return svc, tenant
	}
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, svc *Service, tenant *domain.Tenant) hookSystem
	}{
		{"create user", func(_ *testing.T, _ *Service, _ *domain.Tenant) hookSystem {
			return hookSystem{createUserErr: errSimulated}
		}},
		{"move home", func(t *testing.T, svc *Service, tenant *domain.Tenant) hookSystem {
			trashed, err := svc.GetTenant(tenant.ID)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.RemoveAll(filepath.Join(trashed.TrashPath, "home")); err != nil {
				t.Fatal(err)
			}
			return hookSystem{}
		}},
		{"write unit", func(_ *testing.T, _ *Service, _ *domain.Tenant) hookSystem {
			return hookSystem{writeUnitErr: errSimulated}
		}},
		{"reload", func(_ *testing.T, _ *Service, _ *domain.Tenant) hookSystem {
			return hookSystem{reloadErr: errSimulated}
		}},
		{"enable", func(_ *testing.T, _ *Service, _ *domain.Tenant) hookSystem {
			return hookSystem{enableErr: errSimulated}
		}},
		{"start", func(_ *testing.T, _ *Service, _ *domain.Tenant) hookSystem {
			return hookSystem{startErr: errSimulated}
		}},
		{"health", func(_ *testing.T, svc *Service, _ *domain.Tenant) hookSystem {
			fastHealth(svc)
			return hookSystem{silentStart: true}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, tenant := setup(t)
			svc.Sys = tc.setup(t, svc, tenant).with(svc.Sys)
			if _, _, err := svc.RestoreTenant(context.Background(), tenant.ID, AuditContext{}); err == nil {
				t.Fatal("restore should fail")
			}
		})
	}
}
