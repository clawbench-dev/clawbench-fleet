package sysops

import (
	"context"
	"os"
	"path/filepath"
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
