package sysops

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestNewRealDefaults(t *testing.T) {
	r := NewReal("/etc/systemd/system", "/usr/sbin/nologin", "/home")
	if r.Mode() != "systemd" {
		t.Fatalf("mode = %q", r.Mode())
	}
	if r.cpuLast == nil {
		t.Fatal("cpu sample map must be initialized")
	}
}

func TestRealUnitsOnDisk(t *testing.T) {
	dir := t.TempDir()
	r := NewReal(dir, "/usr/sbin/nologin", t.TempDir())
	ctx := context.Background()
	if content, err := r.ReadUnit(ctx, "missing.service"); err != nil || content != "" {
		t.Fatalf("missing unit = %q %v", content, err)
	}
	if err := r.WriteUnit(ctx, "clawbench-a.service", "[Service]\n"); err != nil {
		t.Fatal(err)
	}
	if err := r.WriteUnit(ctx, "clawbench-fleet.service", "[Service]\n"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "other.service"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	units, err := r.ListUnits(ctx)
	if err != nil || len(units) != 1 || units[0] != "clawbench-a.service" {
		t.Fatalf("list units = %v %v", units, err)
	}
	if content, err := r.ReadUnit(ctx, "clawbench-a.service"); err != nil || content != "[Service]\n" {
		t.Fatalf("read = %q %v", content, err)
	}
	if err := r.RemoveUnit(ctx, "clawbench-a.service"); err != nil {
		t.Fatal(err)
	}
	if err := r.RemoveUnit(ctx, "clawbench-a.service"); err != nil {
		t.Fatalf("removing a missing unit is not an error: %v", err)
	}
}

func TestRealUsedUIDsAndUsers(t *testing.T) {
	r := NewReal(t.TempDir(), "/usr/sbin/nologin", "/home")
	if os.Geteuid() != 0 {
		// /etc/passwd is world-readable, so this works unprivileged too.
		if _, err := r.UsedUIDs(context.Background()); err != nil {
			t.Fatalf("used uids: %v", err)
		}
	}
	uids, err := r.UsedUIDs(context.Background())
	if err != nil || len(uids) == 0 || !uids[0] {
		t.Fatalf("root uid should be in use: %v %v", len(uids), err)
	}
	users, err := r.ListUsers(context.Background())
	if err != nil || len(users) == 0 {
		t.Fatalf("list users = %d %v", len(users), err)
	}
	found := false
	for _, u := range users {
		if u.Username == "root" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("root should be listed")
	}
}

func TestRealUserQueries(t *testing.T) {
	if _, err := exec.LookPath("id"); err != nil {
		t.Skip("id command not available")
	}
	r := NewReal(t.TempDir(), "/usr/sbin/nologin", "/home")
	ctx := context.Background()
	nobody := "nobody"
	if _, _, err := r.LookupIDs(ctx, nobody); err != nil {
		t.Skip("nobody account not present")
	}
	uid, gid, err := r.LookupIDs(ctx, nobody)
	if err != nil || uid <= 0 || gid <= 0 {
		t.Fatalf("lookup = %d %d %v", uid, gid, err)
	}
	if exists, err := r.UserExists(ctx, nobody); err != nil || !exists {
		t.Fatalf("nobody should exist: %v %v", exists, err)
	}
	missing := "clawbench-no-such-user-" + strconv.Itoa(os.Getpid())
	if exists, err := r.UserExists(ctx, missing); err != nil || exists {
		t.Fatalf("missing user = %v %v", exists, err)
	}
	if err := r.Chown(ctx, filepath.Join(t.TempDir(), "absent"), uid, gid); err == nil {
		t.Fatal("chown of a missing path should fail")
	}
}

func TestRealDiskUsage(t *testing.T) {
	if _, err := exec.LookPath("du"); err != nil {
		t.Skip("du command not available")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "f"), make([]byte, 10), 0o600); err != nil {
		t.Fatal(err)
	}
	r := NewReal(t.TempDir(), "/usr/sbin/nologin", "/home")
	size, err := r.DiskUsage(context.Background(), dir)
	if err != nil || size < 10 {
		t.Fatalf("disk usage = %d %v", size, err)
	}
	if size, err := r.DiskUsage(context.Background(), filepath.Join(dir, "absent")); err != nil || size != 0 {
		t.Fatalf("absent path = %d %v", size, err)
	}
}

func TestRealJournalLimits(t *testing.T) {
	if _, err := exec.LookPath("journalctl"); err != nil {
		t.Skip("journalctl not available")
	}
	r := NewReal(t.TempDir(), "/usr/sbin/nologin", "/home")
	// A unit that does not exist still returns whatever journalctl printed,
	// plus its error; the important part is that it does not panic or hang.
	_, _ = r.Journal(context.Background(), "clawbench-nonexistent.service", 0)
}

func TestPortHelpers(t *testing.T) {
	if got := portFromAddr("127.0.0.1:21000"); got != 21000 {
		t.Fatalf("port = %d", got)
	}
	if got := portFromAddr("[::]:443"); got != 443 {
		t.Fatalf("ipv6 port = %d", got)
	}
	if got := portFromAddr("no-port"); got != 0 {
		t.Fatalf("invalid address = %d", got)
	}
	if portOpen(1) {
		t.Fatal("port 1 should not be open in the sandbox")
	}
}

func TestWaitPortClosed(t *testing.T) {
	// Port 1 is closed, so the wait returns on the first probe.
	if err := waitPortClosed(context.Background(), 1, 1); err != nil {
		t.Fatalf("closed port = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// An open port plus a cancelled context must report the cancellation.
	ln := listenLocal(t)
	defer func() { _ = ln.Close() }()
	if err := waitPortClosed(ctx, ln.port, 5); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled wait = %v", err)
	}
}

func TestCommandError(t *testing.T) {
	err := &CommandError{Name: "npm", Args: []string{"view"}, Stderr: " 404 not found \n", Err: errors.New("exit status 1")}
	if got := err.Error(); !strings.Contains(got, "404 not found") || strings.Contains(got, "\n") {
		t.Fatalf("error text = %q", got)
	}
	bare := &CommandError{Name: "ss", Err: errors.New("not found")}
	if got := bare.Error(); !strings.Contains(got, "not found") {
		t.Fatalf("bare error text = %q", got)
	}
	if !errors.Is(bare, bare.Err) {
		t.Fatal("CommandError should unwrap its cause")
	}
}

func TestRunCapturesOutput(t *testing.T) {
	out, err := runWithin(context.Background(), shortTimeout, "sh", "-c", "echo hello")
	if err != nil || strings.TrimSpace(out) != "hello" {
		t.Fatalf("run = %q %v", out, err)
	}
	if _, err := runWithin(context.Background(), shortTimeout, "sh", "-c", "echo boom >&2; exit 3"); err == nil {
		t.Fatal("failing command should return an error")
	} else {
		var cmdErr *CommandError
		if !errors.As(err, &cmdErr) || !strings.Contains(cmdErr.Stderr, "boom") {
			t.Fatalf("command error = %#v", err)
		}
	}
	if _, err := runWithin(context.Background(), shortTimeout, "clawbench-no-such-command"); err == nil {
		t.Fatal("missing binary should be an error")
	}
}
