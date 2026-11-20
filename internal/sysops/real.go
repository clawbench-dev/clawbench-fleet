package sysops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Real talks to the actual machine. It is only constructed in production mode;
// the binary must run as root for most of these calls to succeed.
type Real struct {
	systemdDir string
	userShell  string
	homeRoot   string

	mu      sync.Mutex
	cpuLast map[string]cpuSample
}

type cpuSample struct {
	usec int64
	at   time.Time
}

func NewReal(systemdDir, userShell, homeRoot string) *Real {
	return &Real{
		systemdDir: systemdDir,
		userShell:  userShell,
		homeRoot:   homeRoot,
		cpuLast:    map[string]cpuSample{},
	}
}

func (r *Real) Mode() string { return "systemd" }

func (r *Real) UserExists(ctx context.Context, username string) (bool, error) {
	_, err := run(ctx, "id", "-u", username)
	if err == nil {
		return true, nil
	}
	var cmdErr *CommandError
	if errors.As(err, &cmdErr) {
		var exit *exec.ExitError
		if errors.As(cmdErr.Err, &exit) && exit.ExitCode() == 1 {
			return false, nil
		}
	}
	return false, err
}

func (r *Real) CreateUser(ctx context.Context, username string, uid int) error {
	args := []string{
		"--create-home",
		"--shell", r.userShell,
		"--user-group",
		"--comment", "Clawbench tenant",
	}
	if uid > 0 {
		args = append(args, "--uid", strconv.Itoa(uid))
	}
	args = append(args, username)
	if _, err := run(ctx, "useradd", args...); err != nil {
		return err
	}
	home := filepath.Join(r.homeRoot, username)
	_ = os.Chmod(home, 0o700)
	// A nologin shell already blocks login; locking the password is a second
	// lock that also stops `su`. Failure here is not fatal.
	_, _ = run(ctx, "passwd", "-l", username)
	return nil
}

func (r *Real) DeleteUser(ctx context.Context, username string) error {
	_, err := run(ctx, "userdel", username)
	return err
}

func (r *Real) LookupIDs(ctx context.Context, username string) (int, int, error) {
	uidOut, err := run(ctx, "id", "-u", username)
	if err != nil {
		return 0, 0, err
	}
	gidOut, err := run(ctx, "id", "-g", username)
	if err != nil {
		return 0, 0, err
	}
	uid, err := strconv.Atoi(strings.TrimSpace(uidOut))
	if err != nil {
		return 0, 0, fmt.Errorf("parse uid %q: %w", uidOut, err)
	}
	gid, err := strconv.Atoi(strings.TrimSpace(gidOut))
	if err != nil {
		return 0, 0, fmt.Errorf("parse gid %q: %w", gidOut, err)
	}
	return uid, gid, nil
}

func (r *Real) UsedUIDs(ctx context.Context) (map[int]bool, error) {
	data, err := os.ReadFile("/etc/passwd")
	if err != nil {
		return nil, err
	}
	out := map[int]bool{}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Split(line, ":")
		if len(fields) < 4 {
			continue
		}
		if uid, err := strconv.Atoi(fields[2]); err == nil {
			out[uid] = true
		}
	}
	return out, nil
}

func (r *Real) Chown(ctx context.Context, path string, uid, gid int) error {
	_, err := run(ctx, "chown", "-R", fmt.Sprintf("%d:%d", uid, gid), path)
	return err
}

func (r *Real) WriteUnit(ctx context.Context, unit, content string) error {
	return os.WriteFile(filepath.Join(r.systemdDir, unit), []byte(content), 0o644)
}

func (r *Real) RemoveUnit(ctx context.Context, unit string) error {
	err := os.Remove(filepath.Join(r.systemdDir, unit))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (r *Real) DaemonReload(ctx context.Context) error {
	_, err := run(ctx, "systemctl", "daemon-reload")
	return err
}

func (r *Real) Enable(ctx context.Context, unit string) error {
	_, err := run(ctx, "systemctl", "enable", unit)
	return err
}

func (r *Real) Disable(ctx context.Context, unit string) error {
	_, err := run(ctx, "systemctl", "disable", unit)
	return err
}

func (r *Real) Start(ctx context.Context, unit string) error {
	_, err := run(ctx, "systemctl", "start", unit)
	return err
}

func (r *Real) Stop(ctx context.Context, unit string) error {
	_, err := run(ctx, "systemctl", "stop", unit)
	return err
}

func (r *Real) Restart(ctx context.Context, unit string) error {
	_, err := run(ctx, "systemctl", "restart", unit)
	return err
}

func (r *Real) IsActive(ctx context.Context, unit string) (bool, error) {
	out, err := run(ctx, "systemctl", "is-active", unit)
	state := strings.TrimSpace(out)
	if state == "active" || state == "reloading" || state == "activating" {
		return true, nil
	}
	if err != nil {
		// is-active exits non-zero for inactive units; that is a valid answer.
		return false, nil
	}
	return false, nil
}

func (r *Real) Journal(ctx context.Context, unit string, lines int) (string, error) {
	if lines < 1 {
		lines = 200
	}
	if lines > 5000 {
		lines = 5000
	}
	out, err := run(ctx, "journalctl", "-u", unit, "-n", strconv.Itoa(lines), "--no-pager", "-o", "short-iso")
	if err != nil {
		return out, err
	}
	return out, nil
}

func (r *Real) Metrics(ctx context.Context, unit string) (Metrics, error) {
	base := filepath.Join("/sys/fs/cgroup/system.slice", unit)
	if _, err := os.Stat(base); err != nil {
		// Some distros put custom units directly under /sys/fs/cgroup.
		alt := filepath.Join("/sys/fs/cgroup", unit)
		if _, altErr := os.Stat(alt); altErr == nil {
			base = alt
		} else {
			return Metrics{}, nil
		}
	}
	var m Metrics
	if data, err := os.ReadFile(filepath.Join(base, "memory.current")); err == nil {
		m.MemoryBytes, _ = strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	}
	if data, err := os.ReadFile(filepath.Join(base, "pids.current")); err == nil {
		m.PIDs, _ = strconv.Atoi(strings.TrimSpace(string(data)))
	}
	if data, err := os.ReadFile(filepath.Join(base, "cpu.stat")); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			fields := strings.Fields(line)
			if len(fields) == 2 && fields[0] == "usage_usec" {
				usec, _ := strconv.ParseInt(fields[1], 10, 64)
				nowT := time.Now()
				r.mu.Lock()
				last, ok := r.cpuLast[unit]
				r.cpuLast[unit] = cpuSample{usec: usec, at: nowT}
				r.mu.Unlock()
				if ok {
					elapsed := nowT.Sub(last.at).Seconds()
					if elapsed > 0 {
						m.CPUPercent = float64(usec-last.usec) / 1e6 / elapsed * 100
					}
				}
			}
		}
	}
	return m, nil
}

func (r *Real) DiskUsage(ctx context.Context, path string) (int64, error) {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	out, err := run(ctx, "du", "-sb", "--", path)
	if err != nil {
		return 0, err
	}
	fields := strings.Fields(out)
	if len(fields) == 0 {
		return 0, nil
	}
	n, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil {
		return 0, err
	}
	return n, nil
}

func (r *Real) ListUnits(ctx context.Context) ([]string, error) {
	entries, err := os.ReadDir(r.systemdDir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, "clawbench-") && strings.HasSuffix(name, ".service") && name != "clawbench-fleet.service" {
			out = append(out, name)
		}
	}
	return out, nil
}

func (r *Real) NpmVersions(ctx context.Context, pkg, registry string) ([]string, error) {
	if _, err := exec.LookPath("npm"); err != nil {
		return nil, errors.New("服务器上没有 npm，请先安装 Node.js，或改用上传安装包的方式")
	}
	args := []string{"view", pkg, "versions", "--json"}
	if registry != "" {
		args = append(args, "--registry", registry)
	}
	out, err := run(ctx, "npm", args...)
	if err != nil {
		return nil, err
	}
	return parseNpmVersions(out)
}

func (r *Real) NpmInstall(ctx context.Context, prefix, pkg, version, registry string) error {
	if _, err := exec.LookPath("npm"); err != nil {
		return errors.New("服务器上没有 npm，请先安装 Node.js，或改用上传安装包的方式")
	}
	if err := os.MkdirAll(prefix, 0o700); err != nil {
		return err
	}
	args := []string{
		"install",
		"--prefix", prefix,
		"--no-audit", "--no-fund", "--loglevel=error",
		"--omit=dev",
	}
	if registry != "" {
		args = append(args, "--registry", registry)
	}
	args = append(args, pkg+"@"+version)
	// Downloading a release binary can take a while on a slow link.
	_, err := runWithin(ctx, 5*time.Minute, "npm", args...)
	return err
}

// parseNpmVersions accepts both shapes npm returns: a bare string when the
// package has a single version, and an array otherwise.
func parseNpmVersions(raw string) ([]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("npm 没有返回任何版本")
	}
	var list []string
	if err := json.Unmarshal([]byte(raw), &list); err == nil {
		return list, nil
	}
	var single string
	if err := json.Unmarshal([]byte(raw), &single); err == nil {
		return []string{single}, nil
	}
	return nil, fmt.Errorf("无法解析 npm 返回的版本列表：%s", firstLine(raw))
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
