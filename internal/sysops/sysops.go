package sysops

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
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
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
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
