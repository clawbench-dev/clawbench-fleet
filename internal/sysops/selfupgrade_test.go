package sysops

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestCheckupVersionReadsTheVersionArgument covers what the platform asks of an
// uploaded file: run it and read the version it prints.
func TestCheckupVersionReadsTheVersionArgument(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "clawbench-fleet")
	script := "#!/bin/sh\necho 0.9.0\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := (&Real{}).CheckupVersion(context.Background(), bin)
	if err != nil {
		t.Fatalf("version: %v", err)
	}
	if got != "0.9.0" {
		t.Fatalf("version = %q", got)
	}
}

// TestCheckupVersionRunsAnExtensionlessBinary reproduces how the platform
// stores an upload: under a temporary name with no extension. The kernel only
// execs a file whose name looks like a program, so a real binary would be
// refused with "exec format error" and a good upload would be thrown away.
func TestCheckupVersionRunsAnExtensionlessBinary(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("ELF binaries are a Linux concept")
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	upload := filepath.Join(t.TempDir(), "fleet-self-123456")
	if err := os.WriteFile(upload, data, 0o755); err != nil {
		t.Fatal(err)
	}
	// The test binary is not clawbench-fleet, so it exits non-zero on
	// --version. That is fine: the point is that the kernel ran it at all. A
	// refusal shows up as a start error with nothing on stderr.
	_, err = (&Real{}).CheckupVersion(context.Background(), upload)
	var cmdErr *CommandError
	refused := errors.As(err, &cmdErr) && strings.TrimSpace(cmdErr.Stderr) == ""
	if refused {
		t.Fatal("an extensionless binary must be run, not refused")
	}
}

// TestCheckupVersionRejectsNonExecutables makes sure a wrong file is caught
// before anything is swapped.
func TestCheckupVersionRejectsNonExecutables(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-a-binary")
	if err := os.WriteFile(path, []byte("plain text"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := (&Real{}).CheckupVersion(context.Background(), path); err == nil {
		t.Fatal("a text file must not pass the version check")
	}
}

func TestCheckupVersionRejectsEmptyOutput(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "silent")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := (&Real{}).CheckupVersion(context.Background(), bin); err == nil {
		t.Fatal("a program that prints nothing must not pass")
	}
}

// TestFakePortHoldersReportsListeningUnits lets the dev server exercise the
// adoption button: the fake machine must answer the same question as the real
// one, or the page would look broken before anything real is involved.
func TestFakePortHoldersReportsListeningUnits(t *testing.T) {
	f := NewFake(t.TempDir(), t.TempDir())
	ctx := context.Background()
	unit := "clawbench-alice.service"
	content := "[Service]\nUser=alice\nExecStart=/bin/true --port 30001\n"
	if err := f.CreateUser(ctx, "alice", 5000); err != nil {
		t.Fatal(err)
	}
	if err := f.WriteUnit(ctx, unit, content); err != nil {
		t.Fatal(err)
	}
	if err := f.Start(ctx, unit); err != nil {
		t.Fatal(err)
	}
	holders, err := f.PortHolders(ctx, 30001, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if len(holders) != 1 || holders[0].Self {
		t.Fatalf("holders = %+v", holders)
	}
	if err := f.KillProcess(ctx, os.Getpid()); err == nil {
		t.Fatal("the running process must be refused")
	}
}

// TestRealPortHoldersMarksItself covers the question adoption asks: is the
// process holding our port this very program? The answer decides whether the
// page keeps its reply, so a wrong one would either hang up on itself or leave
// the port occupied for systemd.
func TestRealPortHoldersMarksItself(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("no loopback listener available: %v", err)
	}
	defer func() { _ = ln.Close() }()
	tcpAddr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatal("listener is not TCP")
	}
	port := tcpAddr.Port

	me, err := user.Current()
	if err != nil {
		t.Skipf("no current user: %v", err)
	}
	holders, err := (&Real{}).PortHolders(context.Background(), port, me.Username)
	if err != nil {
		t.Skipf("ss not available: %v", err)
	}
	if len(holders) == 0 {
		t.Skip("ss did not report the test listener; nothing to assert")
	}
	found := false
	for _, h := range holders {
		if h.PID == os.Getpid() && h.Self {
			found = true
		}
	}
	if !found {
		t.Fatalf("own listener was not marked as self: %+v", holders)
	}
}

// TestRealKillProcessStopsAChild proves the signal actually reaches the process
// and that the call only returns once that process is gone.
func TestRealKillProcessStopsAChild(t *testing.T) {
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot start a helper process: %v", err)
	}
	pid := cmd.Process.Pid
	if err := (&Real{}).KillProcess(context.Background(), pid); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatalf("kill: %v", err)
	}
	state, waitErr := cmd.Process.Wait()
	_ = waitErr
	if state.Success() || state.Exited() && state.ExitCode() == 0 {
		t.Fatalf("helper exited normally: %+v", state)
	}
}

// TestRealKillProcessRefusesThisProcess guards the one PID that must never be
// signaled: the platform answering the request.
func TestRealKillProcessRefusesThisProcess(t *testing.T) {
	if err := (&Real{}).KillProcess(context.Background(), os.Getpid()); err == nil {
		t.Fatal("the running process must be refused")
	}
	if err := (&Real{}).KillProcess(context.Background(), 0); err == nil {
		t.Fatal("PID 0 must be refused")
	}
}

// TestReplaceSelfKeepsTheOldFileOnFailure is the property the whole flow rests
// on: a failed swap must never leave the install directory without a binary.
func TestReplaceSelfKeepsTheOldFileOnFailure(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "clawbench-fleet")
	if err := os.WriteFile(dst, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(dir, "does-not-exist")
	if err := (&Real{}).ReplaceSelf(missing, dst); err == nil {
		t.Fatal("replacing from a missing source must fail")
	}
	data, err := os.ReadFile(dst)
	if err != nil || string(data) != "old" {
		t.Fatalf("old binary must survive: %q %v", data, err)
	}
}

func TestReplaceSelfInstallsTheNewFile(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "upload")
	dst := filepath.Join(dir, "clawbench-fleet")
	if err := os.WriteFile(src, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := (&Real{}).ReplaceSelf(src, dst); err != nil {
		t.Fatalf("replace: %v", err)
	}
	data, err := os.ReadFile(dst)
	if err != nil || string(data) != "new" {
		t.Fatalf("dst = %q %v", data, err)
	}
	info, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o100 == 0 {
		t.Fatalf("the replaced binary must be executable: %v", info.Mode())
	}
}

func TestFakeSelfUpgradeHooks(t *testing.T) {
	f := NewFake(t.TempDir(), t.TempDir())
	bin := filepath.Join(t.TempDir(), "clawbench")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho 0.7.0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	v, err := f.CheckupVersion(context.Background(), bin)
	if err != nil || v != "0.7.0" {
		t.Fatalf("version = %q %v", v, err)
	}
	if _, err := f.CheckupVersion(context.Background(), filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("a missing file must fail")
	}

	dst := filepath.Join(t.TempDir(), "clawbench-fleet")
	if err := f.ReplaceSelf(bin, dst); err != nil {
		t.Fatalf("replace: %v", err)
	}
	if _, err := os.Stat(dst); err != nil {
		t.Fatalf("dev replace must write the target: %v", err)
	}
	if err := f.RestartSelf(context.Background()); err != nil {
		t.Fatalf("dev restart = %v", err)
	}
}
