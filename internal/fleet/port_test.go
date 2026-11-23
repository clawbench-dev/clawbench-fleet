package fleet

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"

	"clawbench-fleet/internal/domain"
)

func TestUpdateTenantChangesPort(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	alice := createRunning(t, svc, "alice")
	oldPort := alice.Port
	// The test service configures exactly two ports and alice took the first,
	// so the second one is free and already inside the managed range.
	newPort := oldPort + 1
	if newPort > svc.Settings().PortEnd {
		newPort = oldPort - 1
	}

	updated, err := svc.UpdateTenant(ctx, alice.ID, UpdateTenantInput{Port: &newPort}, AuditContext{Actor: "tester"})
	if err != nil {
		t.Fatalf("change port: %v", err)
	}
	if updated.Port != newPort {
		t.Fatalf("port = %d, want %d", updated.Port, newPort)
	}
	if updated.Status != domain.StatusRunning {
		t.Fatalf("status = %q, want running", updated.Status)
	}

	// Both the instance config and the systemd unit must carry the new port,
	// otherwise the next restart would move back to the old one.
	raw, err := os.ReadFile(svc.tenantConfigPath(updated))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "port: "+strconv.Itoa(newPort)) {
		t.Fatalf("config not updated: %s", raw)
	}
	unit, err := svc.Sys.ReadUnit(ctx, updated.UnitName)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(unit, "--port "+strconv.Itoa(newPort)) {
		t.Fatalf("unit not updated: %s", unit)
	}

	// The instance is reachable on the new port and the old one is released.
	if _, err := svc.healthCheck(ctx, newPort); err != nil {
		t.Fatalf("instance not healthy on the new port: %v", err)
	}
	if state, err := svc.Store.PortState(oldPort); err != nil || state != "free" {
		t.Fatalf("old port state = %q err=%v, want free", state, err)
	}
	if state, err := svc.Store.PortState(newPort); err != nil || state != "allocated" {
		t.Fatalf("new port state = %q err=%v, want allocated", state, err)
	}
}

func TestUpdateTenantRejectsPortOwnedByAnotherTenant(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	alice := createRunning(t, svc, "alice")
	bob := createRunning(t, svc, "bob")
	taken := bob.Port

	if _, err := svc.UpdateTenant(ctx, alice.ID, UpdateTenantInput{Port: &taken}, AuditContext{}); err == nil {
		t.Fatal("moving onto another tenant's port must fail")
	}
	got, err := svc.GetTenant(alice.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Port != alice.Port {
		t.Fatalf("port changed despite the error: %d, want %d", got.Port, alice.Port)
	}
}

func TestUpdateTenantDisplayNameDoesNotRestart(t *testing.T) {
	svc := newService(t)
	ctx := context.Background()
	alice := createRunning(t, svc, "alice")
	before, err := svc.Sys.Journal(ctx, alice.UnitName, 100)
	if err != nil {
		t.Fatal(err)
	}

	name := "Alice"
	updated, err := svc.UpdateTenant(ctx, alice.ID, UpdateTenantInput{DisplayName: &name}, AuditContext{})
	if err != nil {
		t.Fatalf("update display name: %v", err)
	}
	if updated.DisplayName != "Alice" {
		t.Fatalf("display name = %q", updated.DisplayName)
	}
	after, err := svc.Sys.Journal(ctx, alice.UnitName, 100)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(after, "started on") != strings.Count(before, "started on") {
		t.Fatal("changing the display name must not restart the instance")
	}
}
