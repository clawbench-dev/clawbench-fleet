package fleet

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"clawbench-fleet/internal/domain"
	"clawbench-fleet/internal/sysops"
)

// AdoptCandidate is one local account that looks like a Clawbench instance the
// platform does not know about yet.
type AdoptCandidate struct {
	Username       string   `json:"username"`
	DisplayName    string   `json:"display_name"`
	UID            int      `json:"uid"`
	GID            int      `json:"gid"`
	HomeDir        string   `json:"home_dir"`
	DataDir        string   `json:"data_dir"`
	Port           int      `json:"port"`
	PortSource     string   `json:"port_source"`
	UnitName       string   `json:"unit_name"`
	UnitExists     bool     `json:"unit_exists"`
	UnitActive     bool     `json:"unit_active"`
	ServiceActive  bool     `json:"service_active"`
	HealthOK       bool     `json:"health_ok"`
	Version        string   `json:"version"`
	CPUQuota       string   `json:"cpu_quota"`
	MemoryMax      string   `json:"memory_max"`
	TasksMax       int      `json:"tasks_max"`
	AlreadyManaged bool     `json:"already_managed"`
	Protected      bool     `json:"protected"`
	Status         string   `json:"status"`
	Blocked        string   `json:"blocked"`
	Warnings       []string `json:"warnings"`
}

type AdoptResult struct {
	Username string `json:"username"`
	TenantID int64  `json:"tenant_id,omitempty"`
	Status   string `json:"status"`
	Error    string `json:"error"`
}

var (
	unitPortRe  = regexp.MustCompile(`--port[=\s]+(\d+)`)
	unitDataRe  = regexp.MustCompile(`--data-dir[=\s]+(\S+)`)
	unitCPURe   = regexp.MustCompile(`(?m)^CPUQuota=(\S+)`)
	unitMemRe   = regexp.MustCompile(`(?m)^MemoryMax=(\S+)`)
	unitTasksRe = regexp.MustCompile(`(?m)^TasksMax=(\d+)`)
)

type scanEnv struct {
	users     []sysops.UserInfo
	unitSet   map[string]bool
	managed   map[string]bool
	listeners map[string][]sysops.Listener
}

// ScanAdoptable lists local accounts that already own a Clawbench data
// directory. Nothing is written: the admin reviews the list and then adopts
// what they choose.
func (s *Service) ScanAdoptable(ctx context.Context) ([]AdoptCandidate, error) {
	env, err := s.loadScanEnv(ctx)
	if err != nil {
		return nil, err
	}
	cands := make([]AdoptCandidate, 0, len(env.users))
	for _, u := range env.users {
		if c, ok := s.inspectUser(ctx, env, u); ok {
			cands = append(cands, c)
		}
	}
	s.enrichCandidates(ctx, env, cands)
	sort.Slice(cands, func(i, j int) bool {
		if cands[i].AlreadyManaged != cands[j].AlreadyManaged {
			return !cands[i].AlreadyManaged
		}
		return cands[i].Username < cands[j].Username
	})
	return cands, nil
}

// AdoptTenants registers existing instances as tenants. Each account is
// inspected again so the client cannot inject a username that was not scanned.
func (s *Service) AdoptTenants(ctx context.Context, usernames []string, ac AuditContext) ([]AdoptResult, error) {
	names := make([]string, 0, len(usernames))
	for _, name := range usernames {
		if name = strings.TrimSpace(name); name != "" {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return nil, errors.New("没有选中要纳入的账号")
	}
	results := make([]AdoptResult, 0, len(names))
	adopted := 0
	for _, name := range names {
		res := s.adoptOne(ctx, name, ac)
		if res.Error == "" {
			adopted++
		}
		results = append(results, res)
	}
	s.Audit(ac, "tenant.adopt", nil, map[string]any{"total": len(results), "adopted": adopted}, nil)
	return results, nil
}

func (s *Service) loadScanEnv(ctx context.Context) (*scanEnv, error) {
	users, err := s.Sys.ListUsers(ctx)
	if err != nil {
		return nil, fmt.Errorf("读取系统用户失败：%w", err)
	}
	env := &scanEnv{
		users:     users,
		unitSet:   map[string]bool{},
		managed:   map[string]bool{},
		listeners: map[string][]sysops.Listener{},
	}
	if units, err := s.Sys.ListUnits(ctx); err == nil {
		for _, u := range units {
			env.unitSet[u] = true
		}
	}
	if all, err := s.allTenants(); err == nil {
		for _, t := range all {
			env.managed[t.Username] = true
		}
	}
	// Listening sockets are optional: without ss the scan still works, it just
	// falls back to the unit or config file for the port.
	if listeners, err := s.Sys.Listeners(ctx); err == nil {
		for _, l := range listeners {
			if l.Username != "" {
				env.listeners[l.Username] = append(env.listeners[l.Username], l)
			}
		}
	}
	return env, nil
}

// inspectUser turns one account into a candidate. ok is false for accounts
// that are not tenants at all (no data directory, system accounts, ...).
func (s *Service) inspectUser(ctx context.Context, env *scanEnv, u sysops.UserInfo) (AdoptCandidate, bool) {
	if !isTenantAccount(u) {
		return AdoptCandidate{}, false
	}
	c := s.baseCandidate(env, u)
	unitContent := s.unitContentFor(ctx, env, &c)
	c.DataDir = resolveDataDir(u, unitContent, env.listeners[u.Username])
	if !dirExists(c.DataDir) {
		return AdoptCandidate{}, false
	}
	if dirs := listenerDataDirs(env.listeners[u.Username]); len(dirs) > 1 {
		c.Warnings = append(c.Warnings, fmt.Sprintf("这个用户同时跑了多个实例（%d 个数据目录），平台按 %s 纳管", len(dirs), c.DataDir))
	}
	applyUnitSettings(&c, unitContent)
	return c, true
}

// isTenantAccount filters out system accounts and the platform's own account.
func isTenantAccount(u sysops.UserInfo) bool {
	return u.Username != "" && u.Username != "clawbench-fleet" && u.Home != "" && u.Home != "/"
}

func (s *Service) baseCandidate(env *scanEnv, u sysops.UserInfo) AdoptCandidate {
	return AdoptCandidate{
		Username:       u.Username,
		DisplayName:    gecosName(u.Gecos),
		UID:            u.UID,
		GID:            u.GID,
		HomeDir:        u.Home,
		UnitName:       s.Cfg.UnitName(u.Username),
		AlreadyManaged: env.managed[u.Username],
		Protected:      domain.ProtectedUsername(u.Username),
	}
}

// unitContentFor reads the systemd unit when one exists and records that fact
// on the candidate. A missing unit is not an error: the config file is enough.
func (s *Service) unitContentFor(ctx context.Context, env *scanEnv, c *AdoptCandidate) string {
	if !env.unitSet[c.UnitName] {
		return ""
	}
	c.UnitExists = true
	content, _ := s.Sys.ReadUnit(ctx, c.UnitName)
	return content
}

// resolveDataDir prefers the --data-dir recorded in the unit, then the home
// directory default, and finally a directory seen in a listening process.
func resolveDataDir(u sysops.UserInfo, unitContent string, listeners []sysops.Listener) string {
	dir := filepath.Join(u.Home, ".clawbench")
	if m := unitDataRe.FindStringSubmatch(unitContent); len(m) == 2 {
		dir = m[1]
	}
	dirs := listenerDataDirs(listeners)
	if !dirExists(dir) && len(dirs) > 0 {
		dir = dirs[0]
	}
	return dir
}

// applyUnitSettings copies the limits the instance was started with. The unit
// wins; the config file is the fallback for the port only.
func applyUnitSettings(c *AdoptCandidate, unitContent string) {
	c.PortSource = "配置文件"
	if m := unitPortRe.FindStringSubmatch(unitContent); len(m) == 2 {
		c.Port, _ = strconv.Atoi(m[1])
		c.PortSource = "systemd 单元"
	} else if port := readConfigPort(filepath.Join(c.DataDir, "config", "config.yaml")); port > 0 {
		c.Port = port
	}
	if m := unitCPURe.FindStringSubmatch(unitContent); len(m) == 2 && cpuQuotaRe.MatchString(m[1]) {
		c.CPUQuota = m[1]
	}
	if m := unitMemRe.FindStringSubmatch(unitContent); len(m) == 2 && memoryMaxRe.MatchString(m[1]) {
		c.MemoryMax = m[1]
	}
	if m := unitTasksRe.FindStringSubmatch(unitContent); len(m) == 2 {
		c.TasksMax, _ = strconv.Atoi(m[1])
	}
}

// enrichCandidates finds the instance that is actually running for each
// account, fills in health and version, then decides what can be adopted.
// Health checks are I/O bound, so they run a few at a time.
func (s *Service) enrichCandidates(ctx context.Context, env *scanEnv, cands []AdoptCandidate) {
	var wg sync.WaitGroup
	sem := make(chan struct{}, 6)
	for i := range cands {
		c := &cands[i]
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			cctx, cancel := context.WithTimeout(ctx, 8*time.Second)
			defer cancel()
			if active, err := s.Sys.IsActive(cctx, c.UnitName); err == nil {
				c.UnitActive = active
			}

			// The socket is the truth: a manually started instance may listen
			// on a port that neither the config file nor a unit mentions.
			if live, count := s.findLiveInstance(cctx, env.listeners[c.Username], c.DataDir, c.Port); live != nil {
				c.Port = live.port
				c.PortSource = "运行中的进程"
				c.HealthOK = live.httpOK
				c.ServiceActive = true
				c.Version = live.version
				if count > 1 {
					c.Warnings = append(c.Warnings, fmt.Sprintf("这个数据目录有 %d 个进程在监听，平台按端口 %d 纳管", count, live.port))
				}
			} else if c.Port > 0 {
				if info, err := s.healthCheck(cctx, c.Port); err == nil {
					c.HealthOK = true
					c.Version = info.Version
				}
			}

			switch {
			case c.HealthOK:
				c.Status = domain.StatusRunning
			case c.UnitActive:
				c.Status = domain.StatusError
			default:
				c.Status = domain.StatusStopped
			}
			s.finalizeCandidate(c)
		}()
	}
	wg.Wait()
}

type liveInstance struct {
	port    int
	dataDir string
	version string
	httpOK  bool
}

// findLiveInstance picks the listening socket that belongs to this tenant.
// A user may run several Clawbench instances (one per data directory), so the
// data directory inferred from the command line is the signal; the returned
// count is how many sockets point at the same directory.
// findLiveInstance picks the listening socket that belongs to this tenant.
// A user may run several Clawbench instances (one per data directory), so the
// data directory inferred from the command line is the signal. An instance
// also opens extra sockets for port forwarding, so the port from the config or
// unit wins when one of the sockets matches it.
func (s *Service) findLiveInstance(ctx context.Context, listeners []sysops.Listener, preferDir string, preferPort int) (*liveInstance, int) {
	var first *liveInstance
	pids := map[int]bool{}
	checked := 0
	for _, l := range listeners {
		if l.Port < 1 || checked >= 8 {
			continue
		}
		dir := listenerDataDir(l)
		if dir == "" || filepath.Clean(dir) != filepath.Clean(preferDir) {
			continue
		}
		key := l.PID
		if key <= 0 {
			key = -l.Port
		}
		pids[key] = true

		// The main port is the identity even when it does not answer plain
		// HTTP (an instance with TLS enabled, for example). Extra sockets are
		// only used when no main port is known.
		if preferPort > 0 && l.Port == preferPort {
			live := &liveInstance{port: l.Port, dataDir: dir}
			if info, err := s.healthCheck(ctx, l.Port); err == nil {
				live.version = info.Version
				live.httpOK = true
			}
			return live, len(pids)
		}

		checked++
		info, err := s.healthCheck(ctx, l.Port)
		if err != nil {
			continue
		}
		if first == nil {
			first = &liveInstance{port: l.Port, dataDir: dir, version: info.Version, httpOK: true}
		}
	}
	return first, len(pids)
}

func (s *Service) finalizeCandidate(c *AdoptCandidate) {
	c.Blocked = s.candidateBlockedReason(c)
	s.addCandidateWarnings(c)
}

// candidateBlockedReason returns why an account cannot be adopted, or "".
func (s *Service) candidateBlockedReason(c *AdoptCandidate) string {
	switch {
	case c.AlreadyManaged:
		return "已经在管理列表里"
	case c.Port == 0:
		return "找不到端口：进程没在跑，配置文件和 systemd 单元里也没有端口"
	case !configExists(c.DataDir):
		return "数据目录里没有 config.yaml，纳管会丢掉原有密码，暂不纳管"
	case s.portTaken(c.Port):
		return fmt.Sprintf("端口 %d 已经被其他租户占用", c.Port)
	case c.ServiceActive && !c.HealthOK:
		return fmt.Sprintf("端口 %d 上的实例没响应 HTTP 健康检查（可能启用了 HTTPS 或还没启动完），平台按 HTTP 管理，暂不纳管", c.Port)
	case !s.clawbenchBinaryReady():
		return "平台还没有安装可用的 Clawbench 版本，请先到升级中心安装再纳管"
	}
	return ""
}

func (s *Service) addCandidateWarnings(c *AdoptCandidate) {
	if c.Protected {
		c.Warnings = append(c.Warnings, "root 是系统账号，纳入后不能删除")
	}
	if c.Port > 0 {
		st := s.Settings()
		if c.Port < st.PortStart || c.Port > st.PortEnd {
			c.Warnings = append(c.Warnings, fmt.Sprintf("端口 %d 不在当前端口段 %d-%d 内，新租户不会用到它", c.Port, st.PortStart, st.PortEnd))
		}
	}
	if !c.UnitExists {
		c.Warnings = append(c.Warnings, "没有 systemd 单元，纳入时会新建并启动")
	}
	if c.ServiceActive && !c.UnitExists {
		c.Warnings = append(c.Warnings, "实例是手动启动的，纳入时会先停掉它再交给 systemd")
	}
	if !c.HealthOK && !c.ServiceActive && c.Port > 0 && portListening(c.Port) {
		c.Warnings = append(c.Warnings, fmt.Sprintf("端口 %d 有程序在监听，但不是 Clawbench", c.Port))
	}
	if c.PortSource == "配置文件" && !c.HealthOK {
		c.Warnings = append(c.Warnings, "实例没在运行，端口取自配置文件，可能和它启动时实际用的端口不一致")
	}
}

func (s *Service) portTaken(port int) bool {
	st, err := s.Store.PortState(port)
	return err == nil && (st == "allocated" || st == "reserved")
}

func configExists(dataDir string) bool {
	_, err := os.Stat(filepath.Join(dataDir, "config", "config.yaml"))
	return err == nil
}

// listenerDataDirs returns the distinct data directories used by a user's
// listening Clawbench processes.
func listenerDataDirs(listeners []sysops.Listener) []string {
	var out []string
	seen := map[string]bool{}
	for _, l := range listeners {
		dir := listenerDataDir(l)
		if dir == "" || seen[dir] {
			continue
		}
		seen[dir] = true
		out = append(out, dir)
	}
	return out
}

// listenerDataDir infers the data directory of a listening process. An
// explicit --data-dir wins; a plain `clawbench` started from a shell or a unit
// uses $HOME/.clawbench.
func listenerDataDir(l sysops.Listener) string {
	if dir := dataDirFromCmd(l.Command); dir != "" {
		return filepath.Clean(dir)
	}
	if !isClawbenchCommand(l.Command) {
		return ""
	}
	if l.Home != "" {
		return filepath.Clean(filepath.Join(l.Home, ".clawbench"))
	}
	if l.Cwd != "" {
		return filepath.Clean(filepath.Join(l.Cwd, ".clawbench"))
	}
	return ""
}

func isClawbenchCommand(command string) bool {
	lower := strings.ToLower(command)
	return strings.Contains(lower, "clawbench") && !strings.Contains(lower, "clawbench-fleet")
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func dataDirFromCmd(command string) string {
	if m := unitDataRe.FindStringSubmatch(command); len(m) == 2 {
		return m[1]
	}
	return ""
}

func (s *Service) adoptOne(ctx context.Context, username string, ac AuditContext) AdoptResult {
	res := AdoptResult{Username: username}
	unlock := s.lockTenant(username)
	defer unlock()

	if _, err := s.Store.GetTenantByUsername(username); err == nil {
		res.Error = "已经在管理列表里"
		return res
	}
	env, envErr := s.loadScanEnv(ctx)
	if envErr != nil {
		res.Error = envErr.Error()
		return res
	}
	c, blocked := s.findCandidate(ctx, env, username)
	if blocked != "" {
		res.Error = blocked
		return res
	}
	if err := s.acquire(ctx); err != nil {
		res.Error = err.Error()
		return res
	}
	defer s.release()

	t, insertErr := s.insertAdoptedTenant(c)
	if insertErr != "" {
		res.Error = insertErr
		return res
	}
	res.TenantID = t.ID

	// An empty password keeps the employee's existing password: adoption must
	// not silently lock anyone out.
	if err := s.writeTenantConfig(t, ""); err != nil {
		_ = s.Store.DeleteTenant(t.ID)
		_ = s.Store.ReleasePorts(t.ID)
		res.Error = "写租户配置失败：" + err.Error()
		return res
	}
	if failed, err := s.startAdoptedTenant(ctx, t, c.UnitActive, ac); err != nil {
		return failed
	}
	t.Status = domain.StatusRunning
	t.LastHealthOK = true
	t.LastHealthAt = time.Now().Format(time.RFC3339)
	t.LastError = ""
	_ = s.Store.UpdateTenant(t)
	s.Audit(ac, "tenant.adopt", t, map[string]any{fieldPort: t.Port, fieldUID: t.UID}, nil)
	res.Status = domain.StatusRunning
	return res
}

// findCandidate re-inspects one account so a client cannot adopt a username
// that was never scanned. The returned string is the reason it cannot be
// adopted, or "" when it can.
func (s *Service) findCandidate(ctx context.Context, env *scanEnv, username string) (AdoptCandidate, string) {
	var target *sysops.UserInfo
	for i := range env.users {
		if env.users[i].Username == username {
			target = &env.users[i]
			break
		}
	}
	if target == nil {
		return AdoptCandidate{}, "系统里没有这个用户"
	}
	c, ok := s.inspectUser(ctx, env, *target)
	if !ok {
		return AdoptCandidate{}, "这个用户没有 Clawbench 数据目录"
	}
	cands := []AdoptCandidate{c}
	s.enrichCandidates(ctx, env, cands)
	return cands[0], cands[0].Blocked
}

// insertAdoptedTenant writes the tenant row and claims its port atomically.
// The returned string is an error message for the per-tenant result.
func (s *Service) insertAdoptedTenant(c AdoptCandidate) (*domain.Tenant, string) {
	st := s.Settings()
	t := &domain.Tenant{
		Username:    c.Username,
		DisplayName: c.DisplayName,
		UID:         c.UID,
		GID:         c.GID,
		Port:        c.Port,
		Status:      domain.StatusCreating,
		CPUQuota:    firstNonEmpty(c.CPUQuota, st.DefaultCPUQuota),
		MemoryMax:   firstNonEmpty(c.MemoryMax, st.DefaultMemoryMax),
		TasksMax:    firstInt(c.TasksMax, st.DefaultTasksMax),
		HomeDir:     c.HomeDir,
		DataDir:     c.DataDir,
		UnitName:    s.Cfg.UnitName(c.Username),
		Notes:       "从已有实例纳入",
	}
	s.createMu.Lock()
	defer s.createMu.Unlock()
	if err := s.Store.CreateTenant(t); err != nil {
		return nil, err.Error()
	}
	if err := s.Store.ClaimPort(t.Port, t.ID); err != nil {
		_ = s.Store.DeleteTenant(t.ID)
		return nil, err.Error()
	}
	return t, ""
}

// startAdoptedTenant writes the unit, takes over any running process and waits
// for the health check. On failure it returns the failed-adoption result.
func (s *Service) startAdoptedTenant(ctx context.Context, t *domain.Tenant, unitActive bool, ac AuditContext) (AdoptResult, error) {
	res := AdoptResult{Username: t.Username, TenantID: t.ID}
	if err := s.writeUnit(ctx, t); err != nil {
		return s.adoptFailed(t, res, "写 systemd 单元失败", err, ac), err
	}
	if err := s.reloadUnits(ctx); err != nil {
		return s.adoptFailed(t, res, "systemd 重载失败", err, ac), err
	}
	if err := s.Sys.Enable(ctx, t.UnitName); err != nil {
		return s.adoptFailed(t, res, "启用服务失败", err, ac), err
	}
	if unitActive {
		if err := s.Sys.Restart(ctx, t.UnitName); err != nil {
			return s.adoptFailed(t, res, "接管后重启失败", err, ac), err
		}
	} else {
		// The instance may have been started by hand, or by an older unit with
		// another name. Stop only a process that belongs to this very user;
		// anything else is reported, never killed.
		if portListening(t.Port) {
			if err := s.Sys.StopPortOwner(ctx, t.Port, t.Username); err != nil {
				return s.adoptFailed(t, res, "停掉旧进程失败", err, ac), err
			}
		}
		if err := s.Sys.Start(ctx, t.UnitName); err != nil {
			return s.adoptFailed(t, res, "接管后启动失败", err, ac), err
		}
	}
	info, err := s.waitHealthy(ctx, t.Port, "", 45*time.Second)
	if err != nil {
		return s.adoptFailed(t, res, "接管后健康检查失败", err, ac), err
	}
	t.Version = info.Version
	return res, nil
}

func (s *Service) adoptFailed(t *domain.Tenant, res AdoptResult, action string, cause error, ac AuditContext) AdoptResult {
	t.Status = domain.StatusError
	t.LastError = action + "：" + cause.Error()
	t.LastHealthOK = false
	_ = s.Store.UpdateTenant(t)
	s.Audit(ac, "tenant.adopt", t, map[string]any{fieldPort: t.Port}, cause)
	res.Status = domain.StatusError
	res.Error = action + "：" + cause.Error()
	return res
}

func readConfigPort(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	cfg := map[string]any{}
	if err := yamlUnmarshal(data, &cfg); err != nil {
		return 0
	}
	switch v := cfg["port"].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case string:
		n, _ := strconv.Atoi(strings.TrimSpace(v))
		return n
	}
	return 0
}

func gecosName(gecos string) string {
	if i := strings.IndexByte(gecos, ','); i >= 0 {
		gecos = gecos[:i]
	}
	return strings.TrimSpace(gecos)
}

func portListening(port int) bool {
	conn, err := (&net.Dialer{Timeout: 400 * time.Millisecond}).DialContext(context.Background(), "tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// clawbenchBinaryReady guards adoption: taking an instance over means running
// it from the platform's version directory, so that directory has to exist
// first. Dev mode simulates the binary and is always ready.
func (s *Service) clawbenchBinaryReady() bool {
	if s.Sys.Mode() == "dev" {
		return true
	}
	_, err := os.Stat(filepath.Join(s.Cfg.Clawbench.InstallDir, "current", "clawbench"))
	return err == nil
}
