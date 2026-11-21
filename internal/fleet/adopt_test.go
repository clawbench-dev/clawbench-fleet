package fleet

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"clawbench-fleet/internal/config"
)

// seedExistingInstance creates a Linux account with a Clawbench data directory
// but no platform record, the situation the scan is meant to find.
func seedExistingInstance(t *testing.T, svc *Service, cfg config.Config, username string, uid, port int) string {
	t.Helper()
	ctx := context.Background()
	if err := svc.Sys.CreateUser(ctx, username, uid); err != nil {
		t.Fatalf("create user %s: %v", username, err)
	}
	home := filepath.Join(cfg.System.HomeRoot, username)
	dataDir := filepath.Join(home, ".clawbench")
	if err := os.MkdirAll(filepath.Join(dataDir, "config"), 0o700); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf("port: %d\npassword: sha256:keepme\n", port)
	if err := os.WriteFile(filepath.Join(dataDir, "config", "config.yaml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return home
}

func TestScanFindsExistingInstance(t *testing.T) {
	svc, cfg := newTestService(t)
	port := freePort(t)
	home := seedExistingInstance(t, svc, cfg, "alice", 5001, port)

	items, err := svc.ScanAdoptable(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("candidates = %d, want 1: %+v", len(items), items)
	}
	c := items[0]
	if c.Username != "alice" || c.Port != port || c.UID != 5001 {
		t.Fatalf("unexpected candidate: %+v", c)
	}
	if c.HomeDir != home {
		t.Fatalf("home = %q, want %q", c.HomeDir, home)
	}
	if c.Blocked != "" {
		t.Fatalf("candidate should be adoptable, blocked: %q", c.Blocked)
	}
	if c.UnitExists {
		t.Fatal("unit should not exist yet")
	}
	if c.Status != "stopped" {
		t.Fatalf("status = %q, want stopped", c.Status)
	}
}

func TestScanSkipsUsersWithoutDataDir(t *testing.T) {
	svc, _ := newTestService(t)
	if err := svc.Sys.CreateUser(context.Background(), "bob", 5002); err != nil {
		t.Fatal(err)
	}
	items, err := svc.ScanAdoptable(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("candidates = %+v, want none", items)
	}
}

func TestAdoptTenantTakesOverService(t *testing.T) {
	svc, cfg := newTestService(t)
	ctx := context.Background()
	port := freePort(t)
	seedExistingInstance(t, svc, cfg, "alice", 5001, port)

	results, err := svc.AdoptTenants(ctx, []string{"alice"}, AuditContext{Actor: "tester"})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Error != "" {
		t.Fatalf("adopt failed: %+v", results)
	}
	if results[0].Status != "running" {
		t.Fatalf("status = %q, want running", results[0].Status)
	}

	tenant, err := svc.Store.GetTenantByUsername("alice")
	if err != nil {
		t.Fatal(err)
	}
	if tenant.Port != port || tenant.UID != 5001 {
		t.Fatalf("tenant = %+v", tenant)
	}
	if tenant.Status != "running" {
		t.Fatalf("tenant status = %q", tenant.Status)
	}

	// Adoption must not change the employee's password.
	data, err := os.ReadFile(filepath.Join(tenant.DataDir, "config", "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "sha256:keepme") {
		t.Fatalf("password was overwritten: %s", data)
	}
	// The port was claimed so a new tenant cannot take it.
	if state, err := svc.Store.PortState(port); err != nil || state != "allocated" {
		t.Fatalf("port state = %q err=%v", state, err)
	}

	again, err := svc.ScanAdoptable(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 1 || !again[0].AlreadyManaged || again[0].Blocked == "" {
		t.Fatalf("adopted tenant should be marked managed: %+v", again)
	}
}

func TestAdoptRefusesPortOwnedByAnotherTenant(t *testing.T) {
	svc, cfg := newTestService(t)
	ctx := context.Background()
	created, _, err := svc.CreateTenant(ctx, CreateTenantInput{Username: "bob"}, AuditContext{Actor: "tester"})
	if err != nil {
		t.Fatal(err)
	}
	seedExistingInstance(t, svc, cfg, "alice", 5001, created.Port)

	items, err := svc.ScanAdoptable(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var alice *AdoptCandidate
	for i := range items {
		if items[i].Username == "alice" {
			alice = &items[i]
		}
	}
	if alice == nil {
		t.Fatal("alice not found")
	}
	if !strings.Contains(alice.Blocked, "已经被其他租户占用") {
		t.Fatalf("blocked = %q", alice.Blocked)
	}

	results, err := svc.AdoptTenants(ctx, []string{"alice"}, AuditContext{Actor: "tester"})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Error == "" {
		t.Fatalf("adoption should be refused: %+v", results)
	}
	if _, svc := svc.Store.GetTenantByUsername("alice"); svc == nil {
		t.Fatal("alice should not have been recorded")
	}
}

func TestRootIsProtected(t *testing.T) {
	svc, cfg := newTestService(t)
	ctx := context.Background()
	port := freePort(t)
	seedExistingInstance(t, svc, cfg, "root", 0, port)

	results, err := svc.AdoptTenants(ctx, []string{"root"}, AuditContext{Actor: "tester"})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Error != "" {
		t.Fatalf("adopt root failed: %+v", results)
	}
	tenant, err := svc.Store.GetTenantByUsername("root")
	if err != nil {
		t.Fatal(err)
	}
	if !svc.TenantDTO(*tenant).Protected {
		t.Fatal("root should be marked protected")
	}
	if _, svc := svc.DeleteTenant(ctx, tenant.ID, "root", AuditContext{Actor: "tester"}); svc == nil {
		t.Fatal("deleting root must fail")
	}
	if _, err := svc.StopTenant(ctx, tenant.ID, AuditContext{Actor: "tester"}); err != nil {
		t.Fatalf("root should still be stoppable: %v", err)
	}
}

// A Clawbench started with --port does not write that port back to
// config.yaml, so the scan has to trust the listening socket instead of the
// file. This reproduces the "all ports look like 21000" report.
func TestScanUsesActualListeningPort(t *testing.T) {
	svc, cfg := newTestService(t)
	ctx := context.Background()
	port := freePort(t)
	home := seedExistingInstance(t, svc, cfg, "alice", 5001, 21000)

	// An older setup started the instance by hand under its own unit name.
	unit := "clawbench-legacy.service"
	content := fmt.Sprintf("[Service]\nUser=alice\nExecStart=/opt/clawbench/current/clawbench --port %d --data-dir %s/.clawbench\n", port, home)
	if err := svc.Sys.WriteUnit(ctx, unit, content); err != nil {
		t.Fatal(err)
	}
	if err := svc.Sys.Start(ctx, unit); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svc.Sys.Stop(context.Background(), unit) })

	items, err := svc.ScanAdoptable(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("candidates = %+v", items)
	}
	c := items[0]
	if c.Port != port {
		t.Fatalf("port = %d, want the listening port %d (config said 21000)", c.Port, port)
	}
	if c.PortSource != "运行中的进程" {
		t.Fatalf("port source = %q", c.PortSource)
	}
	if !c.HealthOK || !c.ServiceActive {
		t.Fatalf("live instance not detected: %+v", c)
	}
	if c.UnitExists {
		t.Fatal("the standard unit does not exist yet")
	}

	results, err := svc.AdoptTenants(ctx, []string{"alice"}, AuditContext{Actor: "tester"})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Error != "" {
		t.Fatalf("adopt failed: %+v", results)
	}
	tenant, err := svc.Store.GetTenantByUsername("alice")
	if err != nil {
		t.Fatal(err)
	}
	if tenant.Port != port {
		t.Fatalf("adopted port = %d, want %d", tenant.Port, port)
	}
	// The config is rewritten with the real port so a later restart without
	// the flag still binds the right socket.
	raw, err := os.ReadFile(filepath.Join(tenant.DataDir, "config", "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), fmt.Sprintf("port: %d", port)) {
		t.Fatalf("config not updated with the real port: %s", raw)
	}
	if !strings.Contains(string(raw), "sha256:keepme") {
		t.Fatalf("password lost: %s", raw)
	}
}

// A running instance also opens sockets for port forwarding. The main port is
// the one recorded in config.yaml, so the scan must not pick a forwarding
// socket just because it happens to be listed first.
func TestScanPrefersConfigPortOverForwardingPort(t *testing.T) {
	svc, cfg := newTestService(t)
	ctx := context.Background()
	main := freePort(t)
	fwd := freePort(t)
	for fwd == main {
		fwd = freePort(t)
	}
	home := seedExistingInstance(t, svc, cfg, "alice", 5001, main)

	for i, port := range []int{fwd, main} { // forwarding socket listed first
		unit := fmt.Sprintf("clawbench-legacy-%d.service", i)
		content := fmt.Sprintf("[Service]\nUser=alice\nExecStart=/opt/clawbench/current/clawbench --port %d --data-dir %s/.clawbench\n", port, home)
		if err := svc.Sys.WriteUnit(ctx, unit, content); err != nil {
			t.Fatal(err)
		}
		if err := svc.Sys.Start(ctx, unit); err != nil {
			t.Fatal(err)
		}
		name := unit
		t.Cleanup(func() { _ = svc.Sys.Stop(context.Background(), name) })
	}

	items, err := svc.ScanAdoptable(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("candidates = %+v", items)
	}
	if items[0].Port != main {
		t.Fatalf("port = %d, want the main port %d (forwarding port was %d)", items[0].Port, main, fwd)
	}
}
