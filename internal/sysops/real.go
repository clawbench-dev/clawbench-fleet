package sysops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
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
	_ = os.Chmod(home, 0o700) //nolint:gosec // 0700 is the correct mode for a home directory
	// A nologin shell already blocks login; locking the password is a second
	// lock that also stops `su`. Failure here is not fatal.
	_, _ = run(ctx, "passwd", "-l", username)
	return nil
}

func (r *Real) DeleteUser(ctx context.Context, username string) error {
	_, err := run(ctx, "userdel", username)
	return err
}

// SetPassword feeds chpasswd through stdin so the password never shows up in
// the process list. chpasswd also updates /etc/shadow atomically.
func (r *Real) SetPassword(ctx context.Context, username, password string) error {
	if strings.ContainsAny(password, ":\n\r") {
		return errors.New("密码里不能包含冒号或换行")
	}
	_, err := runInput(ctx, 30*time.Second, username+":"+password+"\n", "chpasswd")
	return err
}

func (r *Real) SetShell(ctx context.Context, username, shell string) error {
	_, err := run(ctx, "usermod", "-s", shell, username)
	return err
}

func (r *Real) LockPassword(ctx context.Context, username string) error {
	_, err := run(ctx, "passwd", "-l", username)
	return err
}

// KillUserProcesses ends every process of the account so it can no longer
// write to disk. The lookups are done once, not per process, because scanning
// /proc for a large account gets slow.
func (r *Real) KillUserProcesses(ctx context.Context, username string) error {
	uid, _, err := r.LookupIDs(ctx, username)
	if err != nil {
		// A missing account has nothing to kill; anything else (a real command
		// failure) is passed on.
		exists, existsErr := r.UserExists(ctx, username)
		if existsErr == nil && !exists {
			return nil
		}
		return err
	}
	if uid <= 0 {
		return nil
	}
	if err := killUserProcs(ctx, uid, syscall.SIGTERM); err != nil {
		return err
	}
	if err := waitUserProcsGone(ctx, uid, 50); err == nil {
		return nil
	}
	if err := killUserProcs(ctx, uid, syscall.SIGKILL); err != nil {
		return err
	}
	return waitUserProcsGone(ctx, uid, 25)
}

func killUserProcs(ctx context.Context, uid int, sig syscall.Signal) error {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return err
	}
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if processOwnedBy(pid, uid) {
			_ = syscall.Kill(pid, sig)
		}
	}
	return nil
}

func waitUserProcsGone(ctx context.Context, uid, attempts int) error {
	for range attempts {
		if !userHasProcesses(uid) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
	return fmt.Errorf("账号还有进程没停下来")
}

// userHasProcesses ignores zombies: they no longer write to disk, and without
// a reaper they can linger for a while.
func userHasProcesses(uid int) bool {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return false
	}
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		if processOwnedBy(pid, uid) && !isZombie(pid) {
			return true
		}
	}
	return false
}

func isZombie(pid int) bool {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return true
	}
	fields := strings.Fields(string(data))
	return len(fields) > 2 && fields[2] == "Z"
}

// SSHStatus reads sshd's effective configuration with -T, which already
// accounts for files under sshd_config.d and Match blocks. A missing or
// misconfigured sshd is reported as a status, not an error: the platform must
// keep working on machines where SSH is intentionally absent.
func (r *Real) SSHStatus(ctx context.Context) (SSHStatus, error) {
	st := SSHStatus{Port: 22}
	sshd, ok := findSSHD()
	if !ok {
		st.Problem = "服务器上没有安装 sshd，员工无法通过 SSH 登录"
		return st, nil
	}
	st.Installed = true
	st.Running = sshdRunning(ctx)
	out, err := run(ctx, sshd, "-T")
	if err != nil {
		st.Problem = "读取 sshd 配置失败，请确认平台以 root 运行且 sshd 配置正确"
		return st, nil //nolint:nilerr // a broken sshd is a status the UI shows, not a platform failure
	}
	for _, line := range strings.Split(out, "\n") {
		key, value, found := strings.Cut(strings.TrimSpace(line), " ")
		if !found {
			continue
		}
		switch key {
		case "passwordauthentication":
			st.PasswordAuth = value == "yes"
		case "port":
			if port, convErr := strconv.Atoi(value); convErr == nil {
				st.Port = port
			}
		}
	}
	switch {
	case !st.Running:
		st.Problem = "sshd 没有在运行，员工暂时无法通过 SSH 登录"
	case !st.PasswordAuth:
		st.Problem = "sshd 关闭了密码登录（PasswordAuthentication no），员工无法用密码 SSH 登录"
	}
	return st, nil
}

func findSSHD() (string, bool) {
	if path, err := exec.LookPath("sshd"); err == nil {
		return path, true
	}
	for _, path := range []string{"/usr/sbin/sshd", "/sbin/sshd", "/usr/local/sbin/sshd"} {
		if _, err := os.Stat(path); err == nil {
			return path, true
		}
	}
	return "", false
}

func sshdRunning(ctx context.Context) bool {
	for _, unit := range []string{"ssh", "sshd"} {
		out, err := run(ctx, "systemctl", "is-active", unit)
		if err == nil && strings.TrimSpace(out) == "active" {
			return true
		}
	}
	// No systemd (containers, unusual distros): a listening port is the best
	// signal available without parsing ps.
	return portOpen(22)
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

var ssPidRe = regexp.MustCompile(`pid=(\d+)`)

// StopPortOwner stops whatever is listening on port, but only when every
// process behind that socket belongs to username. Anything else is reported
// instead of killed: the platform must never take down a stranger's process.
func (r *Real) StopPortOwner(ctx context.Context, port int, username string) error {
	uid, _, err := r.LookupIDs(ctx, username)
	if err != nil {
		return err
	}
	out, err := run(ctx, "ss", "-ltnp", fmt.Sprintf("sport = :%d", port))
	if err != nil {
		return err
	}
	pids := map[int]bool{}
	for _, m := range ssPidRe.FindAllStringSubmatch(out, -1) {
		if pid, err := strconv.Atoi(m[1]); err == nil {
			pids[pid] = true
		}
	}
	if len(pids) == 0 {
		return fmt.Errorf("端口 %d 上有进程在监听，但查不到它的进程号，请手动停止", port)
	}
	for pid := range pids {
		if !processOwnedBy(pid, uid) {
			return fmt.Errorf("端口 %d 被其他账号的进程占用（PID %d），平台不会去停它", port, pid)
		}
	}
	for pid := range pids {
		_ = syscall.Kill(pid, syscall.SIGTERM)
	}
	if err := waitPortClosed(ctx, port, 50); err == nil {
		return nil
	}
	for pid := range pids {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
	if err := waitPortClosed(context.Background(), port, 25); err == nil {
		return nil
	}
	return fmt.Errorf("端口 %d 上的进程停不下来", port)
}

// waitPortClosed polls until nothing listens on port, at most attempts times.
func waitPortClosed(ctx context.Context, port, attempts int) error {
	for range attempts {
		if !portOpen(port) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
	return fmt.Errorf("端口 %d 还在监听", port)
}

func processOwnedBy(pid, uid int) bool {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "Uid:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return false
		}
		realUID, err := strconv.Atoi(fields[1])
		return err == nil && realUID == uid
	}
	return false
}

func portOpen(port int) bool {
	conn, err := (&net.Dialer{Timeout: 300 * time.Millisecond}).DialContext(context.Background(), "tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func (r *Real) Chown(ctx context.Context, path string, uid, gid int) error {
	_, err := run(ctx, "chown", "-R", fmt.Sprintf("%d:%d", uid, gid), path)
	return err
}

func (r *Real) ListUsers(ctx context.Context) ([]UserInfo, error) {
	data, err := os.ReadFile("/etc/passwd")
	if err != nil {
		return nil, err
	}
	var out []UserInfo
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Split(line, ":")
		if len(fields) < 7 {
			continue
		}
		uid, err := strconv.Atoi(fields[2])
		if err != nil {
			continue
		}
		gid, err := strconv.Atoi(fields[3])
		if err != nil {
			continue
		}
		out = append(out, UserInfo{
			Username: fields[0], UID: uid, GID: gid, Home: fields[5], Gecos: fields[4],
		})
	}
	return out, nil
}

func (r *Real) ReadUnit(ctx context.Context, unit string) (string, error) {
	data, err := os.ReadFile(filepath.Join(r.systemdDir, unit))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// Listeners reads the listening TCP sockets once and enriches each with the
// owning account and command line. ss prints one line per address family, so
// a process bound to both 0.0.0.0 and :: shows up twice for the same port.
func (r *Real) Listeners(ctx context.Context) ([]Listener, error) {
	out, err := run(ctx, "ss", "-ltnpH")
	if err != nil {
		return nil, err
	}
	users, err := r.ListUsers(ctx)
	if err != nil {
		return nil, err
	}
	nameByUID := map[int]string{}
	for _, u := range users {
		nameByUID[u.UID] = u.Username
	}

	seen := map[[2]int]bool{}
	var res []Listener
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 || fields[0] != "LISTEN" {
			continue
		}
		port := portFromAddr(fields[3])
		if port == 0 {
			continue
		}
		for _, m := range ssPidRe.FindAllStringSubmatch(strings.Join(fields[4:], " "), -1) {
			pid, err := strconv.Atoi(m[1])
			if err != nil || pid <= 0 {
				continue
			}
			key := [2]int{pid, port}
			if seen[key] {
				continue
			}
			seen[key] = true
			uid, command, home, cwd := processDetails(pid)
			res = append(res, Listener{
				Username: nameByUID[uid],
				PID:      pid,
				Port:     port,
				Command:  command,
				Home:     home,
				Cwd:      cwd,
			})
		}
	}
	return res, nil
}

func portFromAddr(addr string) int {
	if i := strings.LastIndexByte(addr, ':'); i >= 0 {
		if port, err := strconv.Atoi(addr[i+1:]); err == nil {
			return port
		}
	}
	return 0
}

func processDetails(pid int) (int, string, string, string) {
	uid := -1
	if data, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid)); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if !strings.HasPrefix(line, "Uid:") {
				continue
			}
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				uid, _ = strconv.Atoi(fields[1])
			}
			break
		}
	}
	command, home := "", ""
	if data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid)); err == nil {
		command = strings.TrimSpace(strings.ReplaceAll(string(data), "\x00", " "))
	}
	if data, err := os.ReadFile(fmt.Sprintf("/proc/%d/environ", pid)); err == nil {
		for _, part := range strings.Split(string(data), "\x00") {
			if strings.HasPrefix(part, "HOME=") {
				home = strings.TrimPrefix(part, "HOME=")
				break
			}
		}
	}
	cwd, _ := os.Readlink(fmt.Sprintf("/proc/%d/cwd", pid))
	return uid, command, home, cwd
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
		return false, nil //nolint:nilerr // non-zero exit is the "inactive" answer
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
	// -x keeps du on the home directory's own filesystem. Without it a mount
	// point inside the home (or, worse, the home itself being a mount) drags in
	// whatever else lives on that device. -b counts apparent bytes, so the
	// number matches what the files claim to be rather than the blocks used.
	out, err := run(ctx, "du", "-sbx", "--", path)
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

// NpmRegistry asks npm itself, so ~/.npmrc, /etc/npmrc and the built-in
// default are all reflected. The platform only overrides this when
// npm_registry is set in its own config.
func (r *Real) NpmRegistry(ctx context.Context) (string, error) {
	if _, err := exec.LookPath("npm"); err != nil {
		return "", errors.New("服务器上没有 npm")
	}
	out, err := run(ctx, "npm", "config", "get", "registry")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}
