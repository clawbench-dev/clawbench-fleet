package sysops

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func newFake(t *testing.T) *Fake {
	t.Helper()
	dir := t.TempDir()
	return NewFake(filepath.Join(dir, "dev"), filepath.Join(dir, "opt"))
}

func portString(n int) string {
	return strconv.Itoa(n)
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	tcpAddr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatal("listener is not TCP")
	}
	port := tcpAddr.Port
	_ = ln.Close()
	return port
}

func waitHealthy(t *testing.T, port int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for ctx.Err() == nil {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:"+portString(port)+"/api/health", http.NoBody)
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("instance on %d never became healthy", port)
}

func TestFakeUsers(t *testing.T) {
	f := newFake(t)
	ctx := context.Background()
	if f.Mode() != "dev" {
		t.Fatalf("mode = %q", f.Mode())
	}
	if exists, err := f.UserExists(ctx, "alice"); err != nil || exists {
		t.Fatalf("unknown user = %v %v", exists, err)
	}
	if err := f.CreateUser(ctx, "alice", 5001); err != nil {
		t.Fatal(err)
	}
	if exists, _ := f.UserExists(ctx, "alice"); !exists {
		t.Fatal("created user should exist")
	}
	uid, gid, err := f.LookupIDs(ctx, "alice")
	if err != nil || uid != 5001 || gid != 5001 {
		t.Fatalf("ids = %d %d %v", uid, gid, err)
	}
	if _, _, lookupErr := f.LookupIDs(ctx, "nobody"); lookupErr == nil {
		t.Fatal("unknown user should error")
	}
	if used, err := f.UsedUIDs(ctx); err != nil || !used[5001] {
		t.Fatalf("used uids = %v %v", used, err)
	}
	users, err := f.ListUsers(ctx)
	if err != nil || len(users) != 1 || users[0].Username != "alice" {
		t.Fatalf("list users = %+v %v", users, err)
	}
	if err := f.Chown(ctx, "/whatever", 1, 1); err != nil {
		t.Fatalf("chown should be a no-op: %v", err)
	}
	if err := f.DeleteUser(ctx, "alice"); err != nil {
		t.Fatal(err)
	}
	// The fake keeps the home directory, so UserExists still reports it: the
	// account is gone from the in-memory table, which LookupIDs reads.
	if _, _, checkErr := f.LookupIDs(ctx, "alice"); checkErr == nil {
		t.Fatal("deleted user should not be in the account table")
	}
}

func TestFakeUnits(t *testing.T) {
	f := newFake(t)
	ctx := context.Background()
	if _, err := f.ReadUnit(ctx, "missing.service"); err != nil {
		t.Fatalf("missing unit should read as empty: %v", err)
	}
	if err := f.Start(ctx, "missing.service"); err == nil {
		t.Fatal("starting a missing unit must fail")
	}
	port := freePort(t)
	content := "[Service]\nUser=bob\nExecStart=/opt/clawbench --port " + portString(port) + "\n"
	if err := f.WriteUnit(ctx, "clawbench-bob.service", content); err != nil {
		t.Fatal(err)
	}
	if got, err := f.ReadUnit(ctx, "clawbench-bob.service"); err != nil || got != content {
		t.Fatalf("read unit = %q %v", got, err)
	}
	if units, err := f.ListUnits(ctx); err != nil || len(units) != 1 {
		t.Fatalf("list units = %v %v", units, err)
	}
	if err := f.Enable(ctx, "clawbench-bob.service"); err != nil {
		t.Fatal(err)
	}
	if err := f.Disable(ctx, "clawbench-bob.service"); err != nil {
		t.Fatal(err)
	}
	if err := f.DaemonReload(ctx); err != nil {
		t.Fatal(err)
	}
	if err := f.Start(ctx, "clawbench-bob.service"); err != nil {
		t.Fatal(err)
	}
	waitHealthy(t, port)
	if active, err := f.IsActive(ctx, "clawbench-bob.service"); err != nil || !active {
		t.Fatalf("active = %v %v", active, err)
	}
	// Starting twice is a no-op.
	if err := f.Start(ctx, "clawbench-bob.service"); err != nil {
		t.Fatal(err)
	}
	listeners, err := f.Listeners(ctx)
	if err != nil || len(listeners) != 1 || listeners[0].Port != port || listeners[0].Username != "bob" {
		t.Fatalf("listeners = %+v %v", listeners, err)
	}
	if err := f.StopPortOwner(ctx, port, "bob"); err != nil {
		t.Fatal(err)
	}
	if active, _ := f.IsActive(ctx, "clawbench-bob.service"); active {
		t.Fatal("owner stop should have stopped the unit")
	}
	if err := f.Restart(ctx, "clawbench-bob.service"); err != nil {
		t.Fatal(err)
	}
	waitHealthy(t, port)
	journal, err := f.Journal(ctx, "clawbench-bob.service", 2)
	if err != nil || !strings.Contains(journal, "started") {
		t.Fatalf("journal = %q %v", journal, err)
	}
	if journal, _ := f.Journal(ctx, "missing.service", 1); journal != "" {
		t.Fatalf("journal for missing unit = %q", journal)
	}
	if m, err := f.Metrics(ctx, "clawbench-bob.service"); err != nil || m.PIDs == 0 {
		t.Fatalf("metrics = %+v %v", m, err)
	}
	if m, _ := f.Metrics(ctx, "missing.service"); m.PIDs != 0 {
		t.Fatalf("metrics for missing unit = %+v", m)
	}
	if err := f.RemoveUnit(ctx, "clawbench-bob.service"); err != nil {
		t.Fatal(err)
	}
	if units, _ := f.ListUnits(ctx); len(units) != 0 {
		t.Fatalf("unit not removed: %v", units)
	}
}

func TestFakeUnitWithoutPort(t *testing.T) {
	f := newFake(t)
	if err := f.WriteUnit(context.Background(), "clawbench-x.service", "[Service]\nUser=x\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.Start(context.Background(), "clawbench-x.service"); err == nil {
		t.Fatal("a unit without a port must not start")
	}
}

func TestFakeDiskUsage(t *testing.T) {
	f := newFake(t)
	home := filepath.Join(f.BaseDir, "homes", "alice")
	if err := os.MkdirAll(filepath.Join(home, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "sub", "a.txt"), []byte("12345"), 0o600); err != nil {
		t.Fatal(err)
	}
	size, err := f.DiskUsage(context.Background(), home)
	if err != nil || size != 5 {
		t.Fatalf("disk usage = %d %v", size, err)
	}
	if size, err := f.DiskUsage(context.Background(), filepath.Join(f.BaseDir, "absent")); err != nil || size != 0 {
		t.Fatalf("missing dir = %d %v", size, err)
	}
}

func TestFakeNpm(t *testing.T) {
	f := newFake(t)
	ctx := context.Background()
	versions, err := f.NpmVersions(ctx, "@scope/clawbench", "")
	if err != nil || len(versions) != 3 {
		t.Fatalf("versions = %v %v", versions, err)
	}
	if reg, err := f.NpmRegistry(ctx); err != nil || reg == "" {
		t.Fatalf("registry = %q %v", reg, err)
	}
	prefix := filepath.Join(t.TempDir(), "npm")
	if err := f.NpmInstall(ctx, prefix, "@scope/clawbench", "0.142.0", ""); err != nil {
		t.Fatal(err)
	}
	bin, err := NpmBinaryPath(prefix, "@scope/clawbench")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(bin)
	if err != nil || !strings.Contains(string(data), "0.142.0") {
		t.Fatalf("installed binary = %q %v", data, err)
	}
	if err := f.NpmInstall(ctx, prefix, "not-scoped", "1.0.0", ""); err == nil {
		t.Fatal("invalid package name must fail")
	}
}

func TestFakeRestoresUnitsOnRestart(t *testing.T) {
	f := newFake(t)
	port := freePort(t)
	content := "[Service]\nUser=carol\nExecStart=/opt/clawbench --port " + portString(port) + "\n"
	if err := f.WriteUnit(context.Background(), "clawbench-carol.service", content); err != nil {
		t.Fatal(err)
	}
	restarted := NewFake(f.BaseDir, f.InstallDir)
	if units, _ := restarted.ListUnits(context.Background()); len(units) != 1 {
		t.Fatalf("units after restart = %v", units)
	}
	waitHealthy(t, port)
	_ = restarted.Stop(context.Background(), "clawbench-carol.service")
	// Stop tolerates an unknown unit.
	if err := restarted.Stop(context.Background(), "unknown.service"); err != nil {
		t.Fatal(err)
	}
}
