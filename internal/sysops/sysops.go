package sysops

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Metrics is a point-in-time sample for one unit.
type Metrics struct {
	CPUPercent  float64
	MemoryBytes int64
	PIDs        int
}

// System is the narrow slice of the operating system the platform needs.
// Everything here is implemented twice: once with real commands (Real) and
// once in memory (Fake) so the whole product can be exercised without root.
type System interface {
	Mode() string

	UserExists(ctx context.Context, username string) (bool, error)
	CreateUser(ctx context.Context, username string, uid int) error
	DeleteUser(ctx context.Context, username string) error
	LookupIDs(ctx context.Context, username string) (uid, gid int, err error)
	UsedUIDs(ctx context.Context) (map[int]bool, error)
	Chown(ctx context.Context, path string, uid, gid int) error

	WriteUnit(ctx context.Context, unit, content string) error
	RemoveUnit(ctx context.Context, unit string) error
	DaemonReload(ctx context.Context) error
	Enable(ctx context.Context, unit string) error
	Disable(ctx context.Context, unit string) error
	Start(ctx context.Context, unit string) error
	Stop(ctx context.Context, unit string) error
	Restart(ctx context.Context, unit string) error
	IsActive(ctx context.Context, unit string) (bool, error)
	Journal(ctx context.Context, unit string, lines int) (string, error)
	Metrics(ctx context.Context, unit string) (Metrics, error)
	DiskUsage(ctx context.Context, path string) (int64, error)
	ListUnits(ctx context.Context) ([]string, error)

	// NpmVersions lists the versions published for an npm package. registry is
	// empty when the server's default registry should be used.
	NpmVersions(ctx context.Context, pkg, registry string) ([]string, error)
	// NpmInstall installs pkg@version into prefix, never into the global npm
	// prefix, so several versions can be kept side by side.
	NpmInstall(ctx context.Context, prefix, pkg, version, registry string) error
	// NpmRegistry reports the registry npm would actually use, after the
	// server's own .npmrc files are taken into account.
	NpmRegistry(ctx context.Context) (string, error)
}

// NpmPlatformPackage maps a wrapper package such as "@scope/clawbench" to the
// per-platform package that actually ships the executable, for example
// "@scope/clawbench-linux-x64". Clawbench publishes the binary this way so the
// main package stays tiny.
func NpmPlatformPackage(pkg string) (string, error) {
	scope, name, ok := strings.Cut(strings.TrimSpace(pkg), "/")
	if !ok || !strings.HasPrefix(scope, "@") || name == "" {
		return "", fmt.Errorf("npm 包名 %q 不是 @scope/name 形式", pkg)
	}
	var suffix string
	switch runtime.GOOS + "/" + runtime.GOARCH {
	case "linux/amd64":
		suffix = "linux-x64"
	case "linux/arm64":
		suffix = "linux-arm64"
	case "darwin/amd64":
		suffix = "darwin-x64"
	case "darwin/arm64":
		suffix = "darwin-arm64"
	case "windows/amd64":
		suffix = "win32-x64"
	default:
		return "", fmt.Errorf("npm 包不支持 %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	return scope + "/" + name + "-" + suffix, nil
}

// NpmBinaryPath is where the executable ends up after installing pkg into
// prefix.
func NpmBinaryPath(prefix, pkg string) (string, error) {
	platform, err := NpmPlatformPackage(pkg)
	if err != nil {
		return "", err
	}
	scope, name, _ := strings.Cut(platform, "/")
	return filepath.Join(prefix, "node_modules", scope, name, "bin", "clawbench"), nil
}

// CommandError carries the command line and stderr so the UI can show a useful
// message instead of "exit status 1".
type CommandError struct {
	Name   string
	Args   []string
	Stderr string
	Err    error
}

func (e *CommandError) Error() string {
	msg := strings.TrimSpace(e.Stderr)
	if msg == "" {
		msg = e.Err.Error()
	}
	return fmt.Sprintf("%s %s: %s", e.Name, strings.Join(e.Args, " "), msg)
}

func (e *CommandError) Unwrap() error { return e.Err }

func run(ctx context.Context, name string, args ...string) (string, error) {
	return runWithin(ctx, 60*time.Second, name, args...)
}

func runWithin(ctx context.Context, timeout time.Duration, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		var exit *exec.ExitError
		stderr := string(out)
		if !errors.As(err, &exit) {
			stderr = ""
		}
		return string(out), &CommandError{Name: name, Args: args, Stderr: stderr, Err: err}
	}
	return string(out), nil
}
