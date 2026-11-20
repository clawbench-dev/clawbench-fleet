package sysops

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Fake is an in-memory operating system used by --dev. It creates real
// directories under BaseDir and starts a tiny HTTP server for each running
// instance so the UI, health checks and upgrade flow work end to end without
// root or systemd.
type Fake struct {
	BaseDir    string
	InstallDir string

	mu    sync.Mutex
	users map[string]int
	units map[string]*fakeUnit
}

type fakeUnit struct {
	content   string
	port      int
	username  string
	enabled   bool
	server    *http.Server
	startedAt time.Time
	logs      []string
}

var (
	fakePortRe = regexp.MustCompile(`--port\s+(\d+)`)
	fakeUserRe = regexp.MustCompile(`(?m)^User=(\S+)`)
)

func NewFake(baseDir, installDir string) *Fake {
	f := &Fake{
		BaseDir:    baseDir,
		InstallDir: installDir,
		users:      map[string]int{},
		units:      map[string]*fakeUnit{},
	}
	_ = os.MkdirAll(filepath.Join(baseDir, "homes"), 0o755)
	_ = os.MkdirAll(filepath.Join(baseDir, "units"), 0o755)
	f.loadUnits()
	return f
}

// loadUnits restores units from disk so restarting the dev server brings the
// demo instances back. Production state comes from systemd, not this file.
func (f *Fake) loadUnits() {
	entries, err := os.ReadDir(filepath.Join(f.BaseDir, "units"))
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".service") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(f.BaseDir, "units", entry.Name()))
		if err != nil {
			continue
		}
		content := string(data)
		port := 0
		if m := fakePortRe.FindStringSubmatch(content); len(m) == 2 {
			port, _ = strconv.Atoi(m[1])
		}
		username := ""
		if m := fakeUserRe.FindStringSubmatch(content); len(m) == 2 {
			username = m[1]
		}
		f.units[entry.Name()] = &fakeUnit{content: content, port: port, username: username, enabled: true}
		_ = f.Start(context.Background(), entry.Name())
	}
}

func (f *Fake) Mode() string { return "dev" }

func (f *Fake) UserExists(ctx context.Context, username string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.users[username]
	if ok {
		return true, nil
	}
	_, err := os.Stat(filepath.Join(f.BaseDir, "homes", username))
	return err == nil, nil
}

func (f *Fake) CreateUser(ctx context.Context, username string, uid int) error {
	home := filepath.Join(f.BaseDir, "homes", username)
	if err := os.MkdirAll(home, 0o700); err != nil {
		return err
	}
	f.mu.Lock()
	f.users[username] = uid
	f.mu.Unlock()
	return nil
}

func (f *Fake) DeleteUser(ctx context.Context, username string) error {
	f.mu.Lock()
	delete(f.users, username)
	f.mu.Unlock()
	return nil
}

func (f *Fake) LookupIDs(ctx context.Context, username string) (int, int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	uid, ok := f.users[username]
	if !ok {
		return 0, 0, fmt.Errorf("user %s not found", username)
	}
	return uid, uid, nil
}

func (f *Fake) UsedUIDs(ctx context.Context) (map[int]bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[int]bool{}
	for _, uid := range f.users {
		out[uid] = true
	}
	return out, nil
}

func (f *Fake) Chown(ctx context.Context, path string, uid, gid int) error {
	return nil
}

// ListUsers enumerates the simulated homes directory. Users restored from a
// previous dev run have no UID in memory, so they get a stable placeholder.
func (f *Fake) ListUsers(ctx context.Context) ([]UserInfo, error) {
	entries, err := os.ReadDir(filepath.Join(f.BaseDir, "homes"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	f.mu.Lock()
	known := make(map[string]int, len(f.users))
	for name, uid := range f.users {
		known[name] = uid
	}
	f.mu.Unlock()

	var out []UserInfo
	for i, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		uid, ok := known[name]
		if !ok {
			uid = 3000 + i
		}
		out = append(out, UserInfo{
			Username: name,
			UID:      uid,
			GID:      uid,
			Home:     filepath.Join(f.BaseDir, "homes", name),
		})
	}
	return out, nil
}

func (f *Fake) ReadUnit(ctx context.Context, unit string) (string, error) {
	f.mu.Lock()
	u, ok := f.units[unit]
	f.mu.Unlock()
	if ok {
		return u.content, nil
	}
	data, err := os.ReadFile(filepath.Join(f.BaseDir, "units", unit))
	if os.IsNotExist(err) {
		return "", nil
	}
	return string(data), err
}

func (f *Fake) StopPortOwner(ctx context.Context, port int, username string) error {
	return nil
}

func (f *Fake) WriteUnit(ctx context.Context, unit, content string) error {
	port := 0
	if m := fakePortRe.FindStringSubmatch(content); len(m) == 2 {
		port, _ = strconv.Atoi(m[1])
	}
	username := ""
	if m := fakeUserRe.FindStringSubmatch(content); len(m) == 2 {
		username = m[1]
	}
	f.mu.Lock()
	u, ok := f.units[unit]
	if !ok {
		u = &fakeUnit{}
		f.units[unit] = u
	}
	u.content = content
	u.port = port
	u.username = username
	f.mu.Unlock()
	return os.WriteFile(filepath.Join(f.BaseDir, "units", unit), []byte(content), 0o644)
}

func (f *Fake) RemoveUnit(ctx context.Context, unit string) error {
	f.mu.Lock()
	u, ok := f.units[unit]
	f.mu.Unlock()
	if ok {
		f.stopUnit(u, unit)
	}
	f.mu.Lock()
	delete(f.units, unit)
	f.mu.Unlock()
	_ = os.Remove(filepath.Join(f.BaseDir, "units", unit))
	return nil
}

func (f *Fake) DaemonReload(ctx context.Context) error { return nil }

func (f *Fake) Enable(ctx context.Context, unit string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if u, ok := f.units[unit]; ok {
		u.enabled = true
	}
	return nil
}

func (f *Fake) Disable(ctx context.Context, unit string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if u, ok := f.units[unit]; ok {
		u.enabled = false
	}
	return nil
}

func (f *Fake) Start(ctx context.Context, unit string) error {
	f.mu.Lock()
	u, ok := f.units[unit]
	if !ok {
		f.mu.Unlock()
		return fmt.Errorf("unit %s not found", unit)
	}
	if u.server != nil {
		f.mu.Unlock()
		return nil
	}
	port := u.port
	username := u.username
	version := f.currentVersion()
	f.mu.Unlock()

	if port < 1 {
		return fmt.Errorf("unit %s has no port", unit)
	}
	ln, err := net.Listen("tcp", ":"+strconv.Itoa(port))
	if err != nil {
		return fmt.Errorf("listen on %d: %w", port, err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"app":      "clawbench",
			"version":  f.currentVersion(),
			"protocol": 1,
			"pid":      os.Getpid(),
		})
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<!doctype html><html><head><meta charset="utf-8"><title>Clawbench dev</title>
<style>body{font-family:system-ui,sans-serif;background:#0f172a;color:#e2e8f0;display:grid;place-items:center;height:100vh;margin:0}
.card{background:#1e293b;padding:40px 56px;border-radius:16px;border:1px solid #334155;text-align:center}
code{color:#93c5fd}</style></head><body><div class="card">
<h1>Clawbench 开发实例</h1><p>用户：<code>%s</code></p><p>版本：<code>%s</code></p><p>端口：<code>%d</code></p>
<p style="color:#94a3b8">这是 --dev 模式下的模拟实例，不是真的 Clawbench。</p></div></body></html>`,
			username, version, port)
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(ln) }()

	f.mu.Lock()
	u.server = srv
	u.startedAt = time.Now()
	u.logs = append(u.logs, fmt.Sprintf("%s started on :%d (version %s)", time.Now().Format(time.RFC3339), port, version))
	f.mu.Unlock()
	return nil
}

func (f *Fake) Stop(ctx context.Context, unit string) error {
	f.mu.Lock()
	u, ok := f.units[unit]
	f.mu.Unlock()
	if !ok {
		return nil
	}
	f.stopUnit(u, unit)
	return nil
}

func (f *Fake) stopUnit(u *fakeUnit, unit string) {
	f.mu.Lock()
	srv := u.server
	u.server = nil
	u.logs = append(u.logs, fmt.Sprintf("%s stopped", time.Now().Format(time.RFC3339)))
	f.mu.Unlock()
	if srv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}
}

func (f *Fake) Restart(ctx context.Context, unit string) error {
	if err := f.Stop(ctx, unit); err != nil {
		return err
	}
	return f.Start(ctx, unit)
}

func (f *Fake) IsActive(ctx context.Context, unit string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.units[unit]
	return ok && u.server != nil, nil
}

func (f *Fake) Journal(ctx context.Context, unit string, lines int) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.units[unit]
	if !ok {
		return "", nil
	}
	if lines < 1 || lines > len(u.logs) {
		lines = len(u.logs)
	}
	start := len(u.logs) - lines
	if start < 0 {
		start = 0
	}
	return strings.Join(u.logs[start:], "\n") + "\n", nil
}

func (f *Fake) Metrics(ctx context.Context, unit string) (Metrics, error) {
	f.mu.Lock()
	u, ok := f.units[unit]
	f.mu.Unlock()
	if !ok || u.server == nil {
		return Metrics{}, nil
	}
	seed := len(unit)
	return Metrics{
		CPUPercent:  0.4 + float64(seed%7)*0.6,
		MemoryBytes: int64(80+seed%40) * 1024 * 1024,
		PIDs:        2 + seed%5,
	}, nil
}

func (f *Fake) DiskUsage(ctx context.Context, path string) (int64, error) {
	var total int64
	err := filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if info, err := d.Info(); err == nil {
			total += info.Size()
		}
		return nil
	})
	if os.IsNotExist(err) {
		return 0, nil
	}
	return total, err
}

func (f *Fake) ListUnits(ctx context.Context) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.units))
	for unit := range f.units {
		out = append(out, unit)
	}
	return out, nil
}

func (f *Fake) currentVersion() string {
	target, err := os.Readlink(filepath.Join(f.InstallDir, "current"))
	if err != nil {
		return "dev-1.0.0"
	}
	return filepath.Base(target)
}

// NpmVersions pretends the package has a few published versions so the whole
// npm install flow can be exercised in --dev.
func (f *Fake) NpmVersions(ctx context.Context, pkg, registry string) ([]string, error) {
	return []string{"0.141.0", "0.142.0", "0.143.0"}, nil
}

// NpmInstall writes a tiny shell script where the real binary would be, so the
// version validation and the symlink switch behave exactly like production.
func (f *Fake) NpmInstall(ctx context.Context, prefix, pkg, version, registry string) error {
	bin, err := NpmBinaryPath(prefix, pkg)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		return err
	}
	script := "#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then echo " + version + "; exit 0; fi\nexit 0\n"
	return os.WriteFile(bin, []byte(script), 0o755)
}

func (f *Fake) NpmRegistry(ctx context.Context) (string, error) {
	return "https://registry.npmjs.org/", nil
}
