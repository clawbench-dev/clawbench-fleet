package fleet

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// install-hooks.mjs hands the same .git/hooks directory to every worktree.
// Baking the main checkout's path into the scripts would make a push from a
// worktree run the full gate against someone else's files — green here, red in
// CI, or the reverse. The scripts must resolve the checkout when they run.
//
// This is a regression test for that: it installs the hooks into a throwaway
// repo, then asserts none of them mentions the absolute path of this checkout.
func TestInstallHooksResolvesCheckoutAtRunTime(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is not installed")
	}
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(repo, "scripts", "install-hooks.mjs")
	if _, statErr := os.Stat(script); statErr != nil {
		t.Skipf("install-hooks.mjs not found: %v", statErr)
	}

	// A separate git repo stands in for a worktree: it has its own .git/hooks,
	// exactly like a real one.
	dir := t.TempDir()
	if out, runErr := exec.Command("git", "-C", dir, "init", "--quiet").CombinedOutput(); runErr != nil {
		t.Fatalf("git init: %v %s", runErr, out)
	}
	// The script resolves the repo from its own location, so copy it into a
	// scripts/ directory inside the throwaway repo.
	if err := os.MkdirAll(filepath.Join(dir, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(script)
	if err != nil {
		t.Fatal(err)
	}
	copied := filepath.Join(dir, "scripts", "install-hooks.mjs")
	if err := os.WriteFile(copied, content, 0o644); err != nil {
		t.Fatal(err)
	}
	if out, runErr := exec.Command("node", copied).CombinedOutput(); runErr != nil {
		t.Fatalf("install: %v %s", runErr, out)
	}

	for _, name := range []string{"pre-commit", "pre-push"} {
		if failures := inspectHook(t, filepath.Join(dir, ".git", "hooks", name), repo); len(failures) > 0 {
			t.Fatalf("%s: %s", name, strings.Join(failures, "; "))
		}
	}
}

// The installed hooks must be executable, or git silently ignores them.
func TestInstallHooksMakesThemExecutable(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is not installed")
	}
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(repo, "scripts", "install-hooks.mjs")
	if _, statErr := os.Stat(script); statErr != nil {
		t.Skipf("install-hooks.mjs not found: %v", statErr)
	}

	dir := t.TempDir()
	if out, runErr := exec.Command("git", "-C", dir, "init", "--quiet").CombinedOutput(); runErr != nil {
		t.Fatalf("git init: %v %s", runErr, out)
	}
	if err := os.MkdirAll(filepath.Join(dir, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(script)
	if err != nil {
		t.Fatal(err)
	}
	copied := filepath.Join(dir, "scripts", "install-hooks.mjs")
	if err := os.WriteFile(copied, content, 0o644); err != nil {
		t.Fatal(err)
	}
	if out, runErr := exec.Command("node", copied).CombinedOutput(); runErr != nil {
		t.Fatalf("install: %v %s", runErr, out)
	}
	for _, name := range []string{"pre-commit", "pre-push"} {
		info, statErr := os.Stat(filepath.Join(dir, ".git", "hooks", name))
		if statErr != nil {
			t.Fatalf("%s: %v", name, statErr)
		}
		if info.Mode().Perm()&0o111 == 0 {
			t.Fatalf("%s mode = %v, want an executable", name, info.Mode())
		}
	}
}

// The check above is only worth keeping if it fails on the shape the bug had.
// This drives the same assertion against a pre-fix script built here.
func TestInstallHooksCheckRejectsABakedPath(t *testing.T) {
	dir := t.TempDir()
	if out, err := exec.Command("git", "-C", dir, "init", "--quiet").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	// Exactly what the script wrote before the fix.
	baked := "#!/bin/sh\n# Pre-push hook\ncd \"" + dir + "\" || exit 1\nexec ./scripts/pre-push-checks.sh\n"
	hook := filepath.Join(dir, ".git", "hooks", "pre-push")
	if err := os.WriteFile(hook, []byte(baked), 0o755); err != nil {
		t.Fatal(err)
	}

	failures := inspectHook(t, hook, dir)
	if len(failures) == 0 {
		t.Fatal("a hook that bakes in the checkout must be reported")
	}
}

// inspectHook applies the two rules the real test asserts — no baked-in path,
// and the checkout resolved at run time — and returns what went wrong.
func inspectHook(t *testing.T, hook, repo string) []string {
	t.Helper()
	body, err := os.ReadFile(hook)
	if err != nil {
		t.Fatalf("%s: %v", hook, err)
	}
	text := string(body)
	var failures []string
	if strings.Contains(text, repo) {
		failures = append(failures, "bakes in the checkout path")
	}
	if !strings.Contains(text, "git rev-parse --show-toplevel") {
		failures = append(failures, "does not resolve the checkout at run time")
	}
	return failures
}
