package sysops

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// TestReplaceSelfCopiesAcrossFilesystems is the case the copy branch exists
// for: the upload arrived on another mount, so rename fails with EXDEV and the
// binary has to be copied rather than moved. /dev/shm is tmpfs on Linux, which
// gives a real second filesystem without root.
func TestReplaceSelfCopiesAcrossFilesystems(t *testing.T) {
	shared, err := os.MkdirTemp("/dev/shm", "fleet-replace-*")
	if err != nil {
		t.Skipf("/dev/shm is not a separate writable filesystem: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(shared) })

	want := []byte("#!/bin/sh\necho 1.2.3\n")
	src := filepath.Join(shared, "clawbench-fleet")
	if err := os.WriteFile(src, want, 0o600); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "clawbench-fleet")
	if err := (&Real{}).ReplaceSelf(src, dst); err != nil {
		t.Fatalf("cross-filesystem replace: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("installed content = %q, want %q", got, want)
	}
	info, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o100 == 0 {
		t.Fatalf("installed binary mode = %v, want executable", info.Mode())
	}
}

// TestReplaceSelfFailsWhenDestinationIsADirectory: nothing may be silently
// discarded when the swap cannot land.
func TestReplaceSelfFailsWhenDestinationIsADirectory(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "upload")
	if err := os.WriteFile(src, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "installed", "clawbench-fleet")
	if err := os.MkdirAll(dst, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := (&Real{}).ReplaceSelf(src, dst); err == nil {
		t.Fatal("replacing a directory should fail")
	}
	entries, err := os.ReadDir(filepath.Dir(dst))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() != "clawbench-fleet" {
			t.Fatalf("failed replace left a staging file %q behind", entry.Name())
		}
	}
}

// TestReplaceSelfCopiesIntoAMissingDestination covers the successful copy: dst
// does not exist yet, so the rename into place succeeds after staging.
func TestReplaceSelfCopiesIntoAMissingDestination(t *testing.T) {
	dir := t.TempDir()
	want := []byte("#!/bin/sh\necho 1.2.3\n")
	src := filepath.Join(dir, "upload")
	if err := os.WriteFile(src, want, 0o600); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "installed", "clawbench-fleet")
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		t.Fatal(err)
	}
	// Rename succeeds straight away here, which covers the fast path; the file
	// and its mode are what matter to the service manager.
	if err := (&Real{}).ReplaceSelf(src, dst); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("installed content = %q, want %q", got, want)
	}
	info, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o100 == 0 {
		t.Fatalf("installed binary mode = %v, want executable", info.Mode())
	}
}
