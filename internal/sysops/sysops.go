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

// UserInfo is one local account, used when scanning the machine for instances
// that were created before the platform was installed.
type UserInfo struct {
	Username string
	UID      int
	GID      int
	Home     string
	Gecos    string
}

// Listener is one process listening on a TCP port. Clawbench instances started
// with --port do not write that port back to config.yaml, so the socket is the
// only reliable source for the port an existing instance actually uses.
type Listener struct {
	Username string
	PID      int
	Port     int
	Command  string
	// Home and Cwd help infer the data directory when the process was started
	// without an explicit --data-dir.
	Home string
	Cwd  string
}

// SSHStatus is what the platform can learn about the machine's SSH server
// without changing its configuration. The platform only prepares tenant
// accounts; whether sshd accepts them is the operator's policy.
type SSHStatus struct {
	Installed    bool   `json:"installed"`
	Running      bool   `json:"running"`
	PasswordAuth bool   `json:"password_auth"`
	Port         int    `json:"port"`
	Problem      string `json:"problem"`
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

	// SetPassword installs a password for username. It is used both for the
	// Clawbench web login and, when SSH is enabled, for the Linux account.
	SetPassword(ctx context.Context, username, password string) error
	// SetShell changes the login shell, e.g. from nologin to bash.
	SetShell(ctx context.Context, username, shell string) error
	// LockPassword locks the account password so SSH cannot be used until a
	// new password is set.
	LockPassword(ctx context.Context, username string) error
	// KillUserProcesses ends every process owned by username, including ones
	// that escaped the systemd unit (an SSH shell, a background build). It is
	// used when a tenant has to be shut down for exceeding its disk limit.
	KillUserProcesses(ctx context.Context, username string) error
	// SSHStatus reports whether this machine's sshd exists, runs, and accepts
	// password logins. It never edits sshd_config.
	SSHStatus(ctx context.Context) (SSHStatus, error)

	// ListUsers enumerates local accounts. Adopting an existing instance
	// starts from this list, not from the database.
	ListUsers(ctx context.Context) ([]UserInfo, error)
	// Listeners enumerates TCP sockets that are currently listening, with the
	// owning account and command line.
	Listeners(ctx context.Context) ([]Listener, error)

	WriteUnit(ctx context.Context, unit, content string) error
	// ReadUnit returns an existing unit file, or "" when it does not exist.
	ReadUnit(ctx context.Context, unit string) (string, error)
	RemoveUnit(ctx context.Context, unit string) error
	DaemonReload(ctx context.Context) error
	Enable(ctx context.Context, unit string) error
	Disable(ctx context.Context, unit string) error
	Start(ctx context.Context, unit string) error
	Stop(ctx context.Context, unit string) error
	Restart(ctx context.Context, unit string) error
	IsActive(ctx context.Context, unit string) (bool, error)
	// StopPortOwner terminates the process listening on port if (and only if)
	// it runs as username. It is used when taking over an instance that was
	// started by hand instead of by systemd.
	StopPortOwner(ctx context.Context, port int, username string) error
	Journal(ctx context.Context, unit string, lines int) (string, error)
	Metrics(ctx context.Context, unit string) (Metrics, error)
	DiskUsage(ctx context.Context, path string) (int64, error)
	ListUnits(ctx context.Context) ([]string, error)

	// NpmVersions lists the versions published for an npm package. registry is
	// empty when the server's default registry should be used.
	NpmVersions(ctx context.Context, pkg, registry string) ([]string, error)
	// NpmRegistry reports the registry npm would actually use, after the
	// server's own .npmrc files are taken into account.
	NpmRegistry(ctx context.Context) (string, error)
	// NpmInstall installs pkg@version into prefix, never into the global npm
	// prefix, so several versions can be kept side by side.
	NpmInstall(ctx context.Context, prefix, pkg, version, registry string) error

	// CheckupVersion reports the version of the Clawbench binary at path.
	CheckupVersion(ctx context.Context, path string) (string, error)
	// ReplaceSelf swaps dst with src atomically enough that dst is either the
	// old binary or the new one, never missing: the platform keeps running on
	// the old file until the service manager starts it again.
	ReplaceSelf(src, dst string) error
	// RestartSelf restarts the platform's own systemd unit. It issues the
	// command and returns without waiting for the unit to come back, because
	// the restarted process is this one.
	RestartSelf(ctx context.Context) error
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
	return runInput(ctx, timeout, "", name, args...)
}

// runInput is runWithin with something on stdin. chpasswd is the one command
// that needs it: the password must not appear on the command line, where every
// user on the machine could read it from ps.
func runInput(ctx context.Context, timeout time.Duration, input, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	if input != "" {
		cmd.Stdin = strings.NewReader(input)
	}
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
