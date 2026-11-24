package sysops

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func newFakeForTest(t *testing.T) *Fake {
	t.Helper()
	return NewFake(t.TempDir(), t.TempDir())
}

func startFakeUnit(t *testing.T, f *Fake, unit, user string, port int) {
	t.Helper()
	ctx := context.Background()
	if err := f.CreateUser(ctx, user, 5000); err != nil {
		t.Fatal(err)
	}
	content := "[Service]\nUser=" + user + "\nExecStart=/bin/true --port " + strconv.Itoa(port) + "\n"
	if err := f.WriteUnit(ctx, unit, content); err != nil {
		t.Fatal(err)
	}
	if err := f.Start(ctx, unit); err != nil {
		t.Fatal(err)
	}
}

// TestFakeKillUserProcessesOnlyTouchesThatUser: locking an account's SSH must
// not stop a colleague's instance.
func TestFakeKillUserProcessesOnlyTouchesThatUser(t *testing.T) {
	f := newFakeForTest(t)
	ctx := context.Background()
	startFakeUnit(t, f, "clawbench-alice.service", "alice", 21001)
	startFakeUnit(t, f, "clawbench-bob.service", "bob", 21002)

	if err := f.KillUserProcesses(ctx, "alice"); err != nil {
		t.Fatal(err)
	}
	if active, err := f.IsActive(ctx, "clawbench-alice.service"); err != nil || active {
		t.Fatalf("alice should be stopped: %v %v", active, err)
	}
	if active, err := f.IsActive(ctx, "clawbench-bob.service"); err != nil || !active {
		t.Fatalf("bob must keep running: %v %v", active, err)
	}
}

// TestFakeKillUserProcessesWithoutAUnit is the common case on a fresh account:
// nothing to stop, and that is not an error.
func TestFakeKillUserProcessesWithoutAUnit(t *testing.T) {
	f := newFakeForTest(t)
	if err := f.KillUserProcesses(context.Background(), "nobody"); err != nil {
		t.Fatalf("an account with no process should be a no-op: %v", err)
	}
}

// TestFakeRestartUnknownUnit reports the mistake instead of silently doing
// nothing, which would look like a successful restart.
func TestFakeRestartUnknownUnit(t *testing.T) {
	f := newFakeForTest(t)
	if err := f.Restart(context.Background(), "clawbench-ghost.service"); err == nil {
		t.Fatal("restarting a unit that does not exist should fail")
	}
}

// TestFakeReplaceSelfRequiresTheSource: an upload that vanished must not wipe
// the dev binary.
func TestFakeReplaceSelfRequiresTheSource(t *testing.T) {
	f := newFakeForTest(t)
	dst := filepath.Join(t.TempDir(), "clawbench-fleet")
	if err := os.WriteFile(dst, []byte("current"), 0o755); err != nil {
		t.Fatal(err)
	}
	err := f.ReplaceSelf(filepath.Join(t.TempDir(), "missing"), dst)
	if err == nil {
		t.Fatal("a missing source should fail")
	}
	if data, readErr := os.ReadFile(dst); readErr != nil || string(data) != "current" {
		t.Fatalf("dev binary must survive: %q %v", data, readErr)
	}
}

// TestFakeReplaceSelfKeepsABackup: dev has no service manager to fall back on,
// so the previous binary has to be kept next to it.
func TestFakeReplaceSelfKeepsABackup(t *testing.T) {
	f := newFakeForTest(t)
	dir := t.TempDir()
	src := filepath.Join(dir, "upload")
	dst := filepath.Join(dir, "clawbench-fleet")
	if err := os.WriteFile(src, []byte("new"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := f.ReplaceSelf(src, dst); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(dst); err != nil || string(data) != "new" {
		t.Fatalf("dst = %q %v", data, err)
	}
	if data, err := os.ReadFile(dst + ".bak"); err != nil || string(data) != "new" {
		t.Fatalf("backup = %q %v", data, err)
	}
}

// TestFakePortHoldersRejectsUnknownUser: adoption asks about an account that
// may already be gone, and the answer has to be an error rather than silence.
func TestFakePortHoldersRejectsUnknownUser(t *testing.T) {
	f := newFakeForTest(t)
	if _, err := f.PortHolders(context.Background(), 21001, "ghost"); err == nil {
		t.Fatal("an unknown account should be reported")
	}
}

// TestFakePortHoldersIgnoresStoppedUnits: a stopped instance no longer holds
// its port, so adoption must not treat it as a conflict.
func TestFakePortHoldersIgnoresStoppedUnits(t *testing.T) {
	f := newFakeForTest(t)
	ctx := context.Background()
	startFakeUnit(t, f, "clawbench-alice.service", "alice", 21001)
	if err := f.Stop(ctx, "clawbench-alice.service"); err != nil {
		t.Fatal(err)
	}
	holders, err := f.PortHolders(ctx, 21001, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if len(holders) != 0 {
		t.Fatalf("holders = %+v, want none for a stopped unit", holders)
	}
}

// TestFakeKillProcessRefusesItself is the safety rail: the dev backend must
// never kill the server that is serving the page.
func TestFakeKillProcessRefusesItself(t *testing.T) {
	f := newFakeForTest(t)
	if err := f.KillProcess(context.Background(), os.Getpid()); err == nil {
		t.Fatal("killing the dev server itself must be refused")
	}
}

// TestFakeRestartSelfIsANoOp: --dev has no unit, and restarting would end the
// session in progress.
func TestFakeRestartSelfIsANoOp(t *testing.T) {
	f := newFakeForTest(t)
	if err := f.RestartSelf(context.Background()); err != nil {
		t.Fatalf("dev restart should be a no-op: %v", err)
	}
}

// TestFakeCurrentVersionReadsTheInstalledBinary keeps the version the upgrade
// page shows in step with what is actually installed: it follows the "current"
// symlink rather than reporting a fixed string.
func TestFakeCurrentVersionReadsTheInstalledBinary(t *testing.T) {
	f := newFakeForTest(t)
	// Without a symlink the fake reports its own dev version.
	if got := f.currentVersion(); got != "dev-1.0.0" {
		t.Fatalf("currentVersion without a link = %q", got)
	}
	version := filepath.Join(f.InstallDir, "versions", "0.9.0")
	if err := os.MkdirAll(version, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(version, filepath.Join(f.InstallDir, "current")); err != nil {
		t.Fatal(err)
	}
	if got := f.currentVersion(); got != "0.9.0" {
		t.Fatalf("currentVersion = %q, want 0.9.0", got)
	}
}

// TestFakeListUsersReportsARealReadError: a homes directory that exists but
// cannot be read is a host problem the page must show, not "no users".
func TestFakeListUsersReportsARealReadError(t *testing.T) {
	f := newFakeForTest(t)
	homes := filepath.Join(f.BaseDir, "homes")
	// A regular file where the directory should be makes ReadDir fail with
	// something other than "not exist". NewFake already created the directory,
	// so replace it.
	if err := os.RemoveAll(homes); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(homes, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.ListUsers(context.Background()); err == nil {
		t.Fatal("an unreadable homes directory should be reported")
	}
}

// TestFakeListUsersSkipsPlainFiles: a stray file in homes must not be shown as
// an account.
func TestFakeListUsersSkipsPlainFiles(t *testing.T) {
	f := newFakeForTest(t)
	homes := filepath.Join(f.BaseDir, "homes")
	if err := os.MkdirAll(homes, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(homes, ".keep"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	users, err := f.ListUsers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range users {
		if u.Username == ".keep" {
			t.Fatal("a plain file must not be listed as an account")
		}
	}
}

// TestFakeCreateUserFailsWhenHomeCannotBeMade: a host out of space or with a
// read-only home root has to surface as an error.
func TestFakeCreateUserFailsWhenHomeCannotBeMade(t *testing.T) {
	f := newFakeForTest(t)
	// Occupy the homes path with a file so MkdirAll cannot create below it.
	homes := filepath.Join(f.BaseDir, "homes")
	if err := os.RemoveAll(homes); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(homes, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := f.CreateUser(context.Background(), "alice", 5000); err == nil {
		t.Fatal("creating a user without a writable home should fail")
	}
}
