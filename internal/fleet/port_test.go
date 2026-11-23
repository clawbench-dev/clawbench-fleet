package fleet

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

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

func TestUpdateTenantRejectsInvalidStates(t *testing.T) {
	t.Run("invalid port", func(t *testing.T) {
		svc := newService(t)
		alice := createRunning(t, svc, "alice")
		zero := 0
		if _, err := svc.UpdateTenant(context.Background(), alice.ID, UpdateTenantInput{Port: &zero}, AuditContext{}); err == nil {
			t.Fatal("port 0 must be rejected")
		}
	})
	t.Run("same port", func(t *testing.T) {
		svc := newService(t)
		alice := createRunning(t, svc, "alice")
		same := alice.Port
		if _, err := svc.UpdateTenant(context.Background(), alice.ID, UpdateTenantInput{Port: &same}, AuditContext{}); err != nil {
			t.Fatalf("same port should be a no-op: %v", err)
		}
	})
	t.Run("trashed", func(t *testing.T) {
		svc := newService(t)
		alice := createRunning(t, svc, "alice")
		if _, err := svc.DeleteTenant(context.Background(), alice.ID, "alice", AuditContext{}); err != nil {
			t.Fatal(err)
		}
		port := alice.Port + 1
		if _, err := svc.UpdateTenant(context.Background(), alice.ID, UpdateTenantInput{Port: &port}, AuditContext{}); err == nil {
			t.Fatal("trashed tenant must not change its port")
		}
	})
	t.Run("creating", func(t *testing.T) {
		svc := newService(t)
		alice := createRunning(t, svc, "alice")
		if err := svc.Store.SetTenantStatus(alice.ID, domain.StatusCreating, ""); err != nil {
			t.Fatal(err)
		}
		name := "Alice"
		if _, err := svc.UpdateTenant(context.Background(), alice.ID, UpdateTenantInput{DisplayName: &name}, AuditContext{}); err == nil {
			t.Fatal("tenant being created must not be edited")
		}
	})
}

func TestUpdateTenantRejectsPortUsedByAnotherProgram(t *testing.T) {
	svc := newService(t)
	ctx := context.Background()
	alice := createRunning(t, svc, "alice")

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	tcpAddr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatal("listener is not TCP")
	}
	busy := tcpAddr.Port
	if _, err := svc.UpdateTenant(ctx, alice.ID, UpdateTenantInput{Port: &busy}, AuditContext{}); err == nil {
		t.Fatal("a port already listened on by another program must be rejected")
	}
}

func TestUpdateTenantFailurePaths(t *testing.T) {
	cases := []struct {
		name string
		sys  hookSystem
	}{
		{"write unit", hookSystem{writeUnitErr: errSimulated}},
		{"reload", hookSystem{reloadErr: errSimulated}},
		{"restart", hookSystem{restartErr: errSimulated}},
		{"health", hookSystem{silentRestart: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := newService(t)
			svc.healthTimeout = 150 * time.Millisecond
			alice := createRunning(t, svc, "alice")
			svc.Sys = tc.sys.with(svc.Sys)
			quota := "150%"
			if _, err := svc.UpdateTenant(context.Background(), alice.ID, UpdateTenantInput{CPUQuota: &quota}, AuditContext{}); err == nil {
				t.Fatal("update should fail")
			}
		})
	}
}

func TestApplyPortChangeRollsBackWhenConfigWriteFails(t *testing.T) {
	svc := newService(t)
	alice := createRunning(t, svc, "alice")
	configDir := filepath.Join(alice.DataDir, "config")
	if err := os.Chmod(configDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(configDir, 0o700) })

	moved := *alice
	moved.Port = alice.Port + 1
	if err := svc.applyPortChange(&moved, alice.Port); err == nil {
		t.Fatal("config write failure should abort the port change")
	}
	if moved.Port != alice.Port {
		t.Fatalf("port not rolled back: %d", moved.Port)
	}
}

func TestApplyPortChangeRejectsConflict(t *testing.T) {
	svc := newService(t)
	alice := createRunning(t, svc, "alice")
	bob := createRunning(t, svc, "bob")

	moved := *alice
	moved.Port = bob.Port
	if err := svc.applyPortChange(&moved, alice.Port); err == nil {
		t.Fatal("moving onto another tenant's port must fail")
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
