package sysops

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestFakeClosedUnitDoesNotComeUp(t *testing.T) {
	f := newFake(t)
	ctx := context.Background()
	port := freePort(t)
	content := "[Service]\nUser=alice\nExecStart=/opt/clawbench --port " + portString(port) + "\n# clawbench-fake: closed\n"
	if err := f.WriteUnit(ctx, "clawbench-alice.service", content); err != nil {
		t.Fatal(err)
	}
	if err := f.Start(ctx, "clawbench-alice.service"); err != nil {
		t.Fatalf("a closed unit should start silently: %v", err)
	}
	if active, err := f.IsActive(ctx, "clawbench-alice.service"); err != nil || active {
		t.Fatalf("closed unit = %v %v", active, err)
	}
	if listeners, _ := f.Listeners(ctx); len(listeners) != 0 {
		t.Fatalf("closed unit should not listen: %+v", listeners)
	}
}

func TestFakeRestartMissingUnit(t *testing.T) {
	f := newFake(t)
	if err := f.Restart(context.Background(), "missing.service"); err == nil {
		t.Fatal("restarting a missing unit must fail")
	}
}

func TestFakeLoadUnitsWithBrokenFile(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "dev")
	if err := os.MkdirAll(filepath.Join(base, "units"), 0o755); err != nil {
		t.Fatal(err)
	}
	// No --port: loadUnits records it, and Start reports the problem.
	if err := os.WriteFile(filepath.Join(base, "units", "clawbench-broken.service"), []byte("[Service]\nUser=bob\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := NewFake(base, filepath.Join(dir, "opt"))
	if units, _ := f.ListUnits(context.Background()); len(units) != 1 {
		t.Fatalf("broken unit not loaded: %v", units)
	}
}

func TestFakeListUsersPlaceholderUID(t *testing.T) {
	f := newFake(t)
	home := filepath.Join(f.BaseDir, "homes", "restored")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	users, err := f.ListUsers(context.Background())
	if err != nil || len(users) != 1 {
		t.Fatalf("users = %+v %v", users, err)
	}
	if users[0].UID < 3000 {
		t.Fatalf("restored user should get a placeholder uid, got %d", users[0].UID)
	}
}

func TestFakeReadsUnitFromDisk(t *testing.T) {
	f := newFake(t)
	// Write a unit file directly: it is not in the in-memory map.
	path := filepath.Join(f.BaseDir, "units", "clawbench-disk.service")
	if err := os.WriteFile(path, []byte("[Service]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	content, err := f.ReadUnit(context.Background(), "clawbench-disk.service")
	if err != nil || content != "[Service]\n" {
		t.Fatalf("read from disk = %q %v", content, err)
	}
	if err := f.RemoveUnit(context.Background(), "clawbench-disk.service"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("unit file should be gone: %v", err)
	}
}

func TestFakeJournalLimitsAndMissingUnit(t *testing.T) {
	f := newFake(t)
	ctx := context.Background()
	if journal, err := f.Journal(ctx, "missing.service", 10); err != nil || journal != "" {
		t.Fatalf("missing journal = %q %v", journal, err)
	}
	port := freePort(t)
	content := "[Service]\nUser=alice\nExecStart=/opt/clawbench --port " + portString(port) + "\n"
	if err := f.WriteUnit(ctx, "clawbench-alice.service", content); err != nil {
		t.Fatal(err)
	}
	if err := f.Start(ctx, "clawbench-alice.service"); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Stop(ctx, "clawbench-alice.service") }()
	// A line count larger than the log returns everything.
	if journal, err := f.Journal(ctx, "clawbench-alice.service", 1000); err != nil || journal == "" {
		t.Fatalf("journal = %q %v", journal, err)
	}
	// Zero and negative line counts also return everything.
	for _, lines := range []int{0, -1} {
		if journal, err := f.Journal(ctx, "clawbench-alice.service", lines); err != nil || journal == "" {
			t.Fatalf("journal(%d) = %q %v", lines, journal, err)
		}
	}
}

func TestNpmBinaryPathErrors(t *testing.T) {
	if _, err := NpmBinaryPath("/tmp", "not-scoped"); err == nil {
		t.Fatal("unscoped package must be rejected")
	}
	if _, err := NpmPlatformPackage("@scope"); err == nil {
		t.Fatal("missing name must be rejected")
	}
	if _, err := NpmPlatformPackage("scope/name"); err == nil {
		t.Fatal("missing @ must be rejected")
	}
}
