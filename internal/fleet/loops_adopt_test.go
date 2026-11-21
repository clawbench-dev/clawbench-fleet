package fleet

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"clawbench-fleet/internal/sysops"
)

func TestLoopsStartAndStop(t *testing.T) {
	svc := newService(t)
	ctx, cancel := context.WithCancel(context.Background())
	svc.Run(ctx)
	// Give the goroutines a moment, then shut them down; the loops must exit
	// on cancellation without leaking.
	time.Sleep(50 * time.Millisecond)
	cancel()
	time.Sleep(50 * time.Millisecond)
}

func TestListRecentJobsAndTrash(t *testing.T) {
	svc := newService(t)
	alice := createRunning(t, svc, "alice")
	if _, err := svc.ListRecentJobs(5); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.DeleteTenant(context.Background(), alice.ID, "alice", AuditContext{}); err != nil {
		t.Fatal(err)
	}
	trash, err := svc.ListTrash()
	if err != nil || len(trash) != 1 {
		t.Fatalf("trash = %+v %v", trash, err)
	}
	// A record with an unparsable deletion time gets the full retention window.
	broken, _ := svc.GetTenant(alice.ID)
	broken.DeletedAt = "someday"
	if err := svc.Store.UpdateTenant(broken); err != nil {
		t.Fatal(err)
	}
	if trash, _ = svc.ListTrash(); trash[0].DaysLeft != svc.Settings().TrashRetention {
		t.Fatalf("days left = %d", trash[0].DaysLeft)
	}
}

func TestAdoptExistingInstance(t *testing.T) {
	svc, cfg := newTestService(t)
	port := freePort(t)
	home := seedExistingInstance(t, svc, cfg, "alice", 5001, port)
	// Start the instance by hand, the way an admin would before the platform
	// knew about it: write the unit and start it through the backend.
	if err := svc.Sys.WriteUnit(context.Background(), svc.Cfg.UnitName("alice"),
		"[Service]\nUser=alice\nExecStart=/opt/clawbench/current/clawbench --port "+strconv.Itoa(port)+" --data-dir "+filepath.Join(home, ".clawbench")+"\n"); err != nil {
		t.Fatal(err)
	}
	if err := svc.Sys.Start(context.Background(), svc.Cfg.UnitName("alice")); err != nil {
		t.Fatal(err)
	}

	results, err := svc.AdoptTenants(context.Background(), []string{"alice", "ghost"}, AuditContext{Actor: "tester"})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("results = %+v", results)
	}
	if results[0].Error != "" || results[0].TenantID == 0 {
		t.Fatalf("alice adopt = %+v", results[0])
	}
	if results[1].Error == "" {
		t.Fatalf("ghost adopt = %+v", results[1])
	}
	tenant, err := svc.GetTenant(results[0].TenantID)
	if err != nil || tenant.Port != port || tenant.Status != "running" {
		t.Fatalf("adopted tenant = %+v %v", tenant, err)
	}
	// The employee's existing password hash survived adoption.
	data, err := os.ReadFile(filepath.Join(home, ".clawbench", "config", "config.yaml"))
	if err != nil || !strings.Contains(string(data), "sha256:keepme") {
		t.Fatalf("adopted config = %s %v", data, err)
	}
	// Adopting again is refused.
	again, err := svc.AdoptTenants(context.Background(), []string{"alice"}, AuditContext{})
	if err != nil || again[0].Error == "" {
		t.Fatalf("second adopt = %+v %v", again, err)
	}
}

func TestAdoptGuards(t *testing.T) {
	svc, _ := newTestService(t)
	if _, err := svc.AdoptTenants(context.Background(), []string{" ", ""}, AuditContext{}); err == nil {
		t.Fatal("empty selection must be rejected")
	}
	// An account without a data directory is not a tenant.
	if err := svc.Sys.CreateUser(context.Background(), "alice", 5001); err != nil {
		t.Fatal(err)
	}
	results, err := svc.AdoptTenants(context.Background(), []string{"alice"}, AuditContext{})
	if err != nil || results[0].Error == "" {
		t.Fatalf("plain account = %+v %v", results, err)
	}
}

func TestListenerAndConfigHelpers(t *testing.T) {
	if !isClawbenchCommand("clawbench --port 21000") {
		t.Fatal("clawbench command should be recognized")
	}
	if isClawbenchCommand("clawbench-fleet --dev") {
		t.Fatal("the platform itself is not an instance")
	}
	if got := dataDirFromCmd("clawbench --data-dir /srv/a --port 1"); got != "/srv/a" {
		t.Fatalf("data dir = %q", got)
	}
	cases := map[string]string{
		"clawbench --data-dir /srv/a": "/srv/a",
		"/usr/bin/clawbench":          "/home/x/.clawbench",
		"python3 server.py":           "",
	}
	for cmd, want := range cases {
		l := sysops.Listener{Username: "alice", Port: 21000, Command: cmd, Home: "/home/x", Cwd: "/tmp"}
		got := listenerDataDir(l)
		if got != want {
			t.Errorf("listenerDataDir(%q) = %q, want %q", cmd, got, want)
		}
	}
	if got := gecosName("Alice Zhang,room 1,555"); got != "Alice Zhang" {
		t.Fatalf("gecos = %q", got)
	}
	if got := gecosName("  Bob  "); got != "Bob" {
		t.Fatalf("gecos = %q", got)
	}
}

func TestReadConfigPort(t *testing.T) {
	dir := t.TempDir()
	if got := readConfigPort(filepath.Join(dir, "missing.yaml")); got != 0 {
		t.Fatalf("missing file = %d", got)
	}
	if err := os.WriteFile(filepath.Join(dir, "bad.yaml"), []byte("port: [\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := readConfigPort(filepath.Join(dir, "bad.yaml")); got != 0 {
		t.Fatalf("broken yaml = %d", got)
	}
	for body, want := range map[string]int{
		"port: 21000\n":     21000,
		"port: \"21001\"\n": 21001,
		"port: 21002.5\n":   21002,
		"other: 1\n":        0,
	} {
		path := filepath.Join(dir, "cfg.yaml")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := readConfigPort(path); got != want {
			t.Errorf("%q -> %d, want %d", body, got, want)
		}
	}
}
