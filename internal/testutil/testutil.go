// Package testutil builds a throwaway platform (config, database, simulated
// system backend) so HTTP and end-to-end tests can run without root, systemd
// or a real host.
package testutil

import (
	"context"
	"net"
	"path/filepath"
	"testing"

	"clawbench-fleet/internal/config"
	"clawbench-fleet/internal/fleet"
	"clawbench-fleet/internal/store"
	"clawbench-fleet/internal/sysops"
)

// FreePort asks the kernel for an unused TCP port.
func FreePort(t *testing.T) int {
	t.Helper()
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("free port: %v", err)
	}
	defer func() { _ = ln.Close() }()
	tcpAddr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatal("listener is not TCP")
	}
	port := tcpAddr.Port
	_ = ln.Close()
	return port
}

// Platform is one isolated platform instance owned by a test.
type Platform struct {
	Svc   *fleet.Service
	Store *store.Store
	Cfg   config.Config
	Sys   *sysops.Fake
}

// New returns a platform rooted in t.TempDir(). The port range and UID range
// are tiny so capacity limits can be exercised; two port groups are available.
func New(t *testing.T) *Platform {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Default()
	cfg.DataDir = filepath.Join(dir, "data")
	cfg.Clawbench.InstallDir = filepath.Join(dir, "opt")
	port := FreePort(t)
	cfg.Clawbench.PortStart = port
	cfg.Clawbench.PortEnd = port + 2
	cfg.Clawbench.PortsPerTenant = 1
	cfg.Clawbench.UIDStart = 5000
	cfg.Clawbench.UIDEnd = 5010
	cfg.System.HomeRoot = filepath.Join(cfg.DataDir, "dev", "homes")
	cfg.System.SystemdDir = filepath.Join(cfg.DataDir, "dev", "units")
	cfg.Backup.Enabled = false
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if err := cfg.EnsureDirs(); err != nil {
		t.Fatalf("ensure dirs: %v", err)
	}
	st, err := store.Open(cfg.DBPath())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	sys := sysops.NewFake(filepath.Join(cfg.DataDir, "dev"), cfg.Clawbench.InstallDir)
	svc, err := fleet.New(cfg, st, sys)
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := contextWithTimeout()
		defer cancel()
		for _, unit := range mustUnits(t, sys) {
			_ = sys.Stop(ctx, unit)
		}
	})
	return &Platform{Svc: svc, Store: st, Cfg: cfg, Sys: sys}
}

// AdminLogin is the password used by Bootstrap.
const AdminLogin = "test-password-1"

// BootstrapAdmin creates the admin account with a known password.
func (p *Platform) BootstrapAdmin(t *testing.T) {
	t.Helper()
	if _, _, err := fleet.BootstrapAdmin(p.Cfg.InitialPasswordPath(), p.Store, AdminLogin); err != nil {
		t.Fatalf("bootstrap admin: %v", err)
	}
}
