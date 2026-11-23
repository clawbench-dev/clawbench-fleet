package sysops

import (
	"context"
	"testing"
	"time"
)

func TestFakeSSHAccountLifecycle(t *testing.T) {
	f := NewFake(t.TempDir(), t.TempDir())
	ctx := context.Background()
	if err := f.CreateUser(ctx, "alice", 5000); err != nil {
		t.Fatal(err)
	}
	if got := f.shells["alice"]; got != "/usr/sbin/nologin" {
		t.Fatalf("initial shell = %q", got)
	}
	if err := f.SetShell(ctx, "alice", "/bin/bash"); err != nil {
		t.Fatal(err)
	}
	if err := f.SetPassword(ctx, "alice", "abc12345"); err != nil {
		t.Fatal(err)
	}
	if got := f.shells["alice"]; got != "/bin/bash" {
		t.Fatalf("shell = %q", got)
	}
	if got := f.passwords["alice"]; got != "abc12345" {
		t.Fatalf("password = %q", got)
	}
	if err := f.LockPassword(ctx, "alice"); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.passwords["alice"]; ok {
		t.Fatal("password should be gone after locking")
	}
	if err := f.DeleteUser(ctx, "alice"); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.shells["alice"]; ok {
		t.Fatal("shell record should be removed with the user")
	}
}

func TestFakeSSHMethodsRequireUser(t *testing.T) {
	f := NewFake(t.TempDir(), t.TempDir())
	ctx := context.Background()
	if err := f.SetPassword(ctx, "nobody", "abc12345"); err == nil {
		t.Fatal("SetPassword should fail for a missing user")
	}
	if err := f.SetShell(ctx, "nobody", "/bin/bash"); err == nil {
		t.Fatal("SetShell should fail for a missing user")
	}
	if err := f.LockPassword(ctx, "nobody"); err == nil {
		t.Fatal("LockPassword should fail for a missing user")
	}
}

func TestFakeSSHStatusIsHealthy(t *testing.T) {
	f := NewFake(t.TempDir(), t.TempDir())
	st, err := f.SSHStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !st.Installed || !st.Running || !st.PasswordAuth || st.Port != 22 {
		t.Fatalf("status = %+v", st)
	}
}

func TestRealSSHStatusNeverFailsTheCaller(t *testing.T) {
	r := NewReal(t.TempDir(), "/usr/sbin/nologin", t.TempDir())
	st, err := r.SSHStatus(context.Background())
	if err != nil {
		t.Fatalf("SSHStatus should report problems as status, got error: %v", err)
	}
	if st.Port == 0 {
		t.Fatal("port should fall back to 22")
	}
}

func TestRealSetPasswordRejectsUnsafeInput(t *testing.T) {
	r := NewReal(t.TempDir(), "/usr/sbin/nologin", t.TempDir())
	if err := r.SetPassword(context.Background(), "alice", "bad:password"); err == nil {
		t.Fatal("colon in password must be rejected")
	}
	if err := r.SetPassword(context.Background(), "alice", "line\nbreak"); err == nil {
		t.Fatal("newline in password must be rejected")
	}
}

func TestRunInputFeedsStdin(t *testing.T) {
	out, err := runInput(context.Background(), time.Second, "alice:s3cret\n", "cat")
	if err != nil {
		t.Fatal(err)
	}
	if out != "alice:s3cret\n" {
		t.Fatalf("output = %q", out)
	}
}

func TestFakeKillUserProcessesStopsUnit(t *testing.T) {
	f := NewFake(t.TempDir(), t.TempDir())
	ctx := context.Background()
	if err := f.CreateUser(ctx, "alice", 5000); err != nil {
		t.Fatal(err)
	}
	if err := f.KillUserProcesses(ctx, "alice"); err != nil {
		t.Fatalf("killing an idle account should succeed: %v", err)
	}
}

func TestRealKillUserProcessesForMissingAccount(t *testing.T) {
	r := NewReal(t.TempDir(), "/usr/sbin/nologin", t.TempDir())
	// The account does not exist on the test machine, which must be a no-op
	// rather than an error that blocks the disk limit.
	if err := r.KillUserProcesses(context.Background(), "clawbench-no-such-user-xyz"); err != nil {
		t.Fatalf("missing account = %v", err)
	}
}
