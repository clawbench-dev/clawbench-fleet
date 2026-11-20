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
	if _, err := svc.Store.GetTenantByUsername("alice"); err == nil {
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
	if _, err := svc.DeleteTenant(ctx, tenant.ID, "root", AuditContext{Actor: "tester"}); err == nil {
		t.Fatal("deleting root must fail")
	}
	if _, err := svc.StopTenant(ctx, tenant.ID, AuditContext{Actor: "tester"}); err != nil {
		t.Fatalf("root should still be stoppable: %v", err)
	}
}
