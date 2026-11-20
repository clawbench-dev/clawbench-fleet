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
	UnitName       string   `json:"unit_name"`
	UnitExists     bool     `json:"unit_exists"`
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
	unitPortRe  = regexp.MustCompile(`--port\s+(\d+)`)
	unitDataRe  = regexp.MustCompile(`--data-dir\s+(\S+)`)
	unitCPURe   = regexp.MustCompile(`(?m)^CPUQuota=(\S+)`)
	unitMemRe   = regexp.MustCompile(`(?m)^MemoryMax=(\S+)`)
	unitTasksRe = regexp.MustCompile(`(?m)^TasksMax=(\d+)`)
)

// ScanAdoptable lists local accounts that already own a Clawbench data
// directory. Nothing is written: the admin reviews the list and then adopts
// what they choose.
func (s *Service) ScanAdoptable(ctx context.Context) ([]AdoptCandidate, error) {
	users, unitSet, managed, err := s.scanInputs(ctx)
	if err != nil {
		return nil, err
	}
	cands := make([]AdoptCandidate, 0, len(users))
	for _, u := range users {
		if c, ok := s.inspectUser(ctx, u, unitSet, managed); ok {
			cands = append(cands, c)
		}
	}
	s.enrichCandidates(ctx, cands)
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

func (s *Service) scanInputs(ctx context.Context) ([]sysops.UserInfo, map[string]bool, map[string]bool, error) {
	users, err := s.Sys.ListUsers(ctx)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("读取系统用户失败：%w", err)
	}
	units, err := s.Sys.ListUnits(ctx)
	if err != nil {
		units = nil
	}
	unitSet := make(map[string]bool, len(units))
	for _, u := range units {
		unitSet[u] = true
	}
	managed := map[string]bool{}
	if all, err := s.allTenants(); err == nil {
		for _, t := range all {
			managed[t.Username] = true
		}
	}
	return users, unitSet, managed, nil
}

// inspectUser turns one account into a candidate. ok is false for accounts
// that are not tenants at all (no data directory, system accounts, ...).
func (s *Service) inspectUser(ctx context.Context, u sysops.UserInfo, unitSet, managed map[string]bool) (AdoptCandidate, bool) {
	if u.Username == "" || u.Username == "clawbench-fleet" {
		return AdoptCandidate{}, false
	}
	if u.Home == "" || u.Home == "/" {
		return AdoptCandidate{}, false
	}
	c := AdoptCandidate{
		Username:       u.Username,
		DisplayName:    gecosName(u.Gecos),
		UID:            u.UID,
		GID:            u.GID,
		HomeDir:        u.Home,
		UnitName:       s.Cfg.UnitName(u.Username),
		AlreadyManaged: managed[u.Username],
		Protected:      domain.ProtectedUsername(u.Username),
	}
	var unitContent string
	if unitSet[c.UnitName] {
		c.UnitExists = true
		unitContent, _ = s.Sys.ReadUnit(ctx, c.UnitName)
	}
	c.DataDir = filepath.Join(u.Home, ".clawbench")
	if m := unitDataRe.FindStringSubmatch(unitContent); len(m) == 2 {
		c.DataDir = m[1]
	}
	info, err := os.Stat(c.DataDir)
	if err != nil || !info.IsDir() {
		return AdoptCandidate{}, false
	}

	c.Port = readConfigPort(filepath.Join(c.DataDir, "config", "config.yaml"))
	if c.Port == 0 {
		if m := unitPortRe.FindStringSubmatch(unitContent); len(m) == 2 {
			c.Port, _ = strconv.Atoi(m[1])
		}
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

	if c.AlreadyManaged {
		c.Blocked = "已经在管理列表里"
	} else if c.Port == 0 {
		c.Blocked = "找不到端口：数据目录里没有配置端口，也没有可用的 systemd 单元"
	} else if st, err := s.Store.PortState(c.Port); err == nil && (st == "allocated" || st == "reserved") {
		c.Blocked = fmt.Sprintf("端口 %d 已经被其他租户占用", c.Port)
	} else if !s.clawbenchBinaryReady() {
		c.Blocked = "平台还没有安装可用的 Clawbench 版本，请先到升级中心安装再纳管"
	}
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
	return c, true
}

// enrichCandidates fills in live state (service active, health, version).
// Health checks are I/O bound, so they run a few at a time.
func (s *Service) enrichCandidates(ctx context.Context, cands []AdoptCandidate) {
	var wg sync.WaitGroup
	sem := make(chan struct{}, 6)
	for i := range cands {
		c := &cands[i]
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			if active, err := s.Sys.IsActive(cctx, c.UnitName); err == nil {
				c.ServiceActive = active
			}
			if c.Port > 0 {
				if info, err := s.healthCheck(cctx, c.Port); err == nil {
					c.HealthOK = true
					c.Version = info.Version
				}
			}
			switch {
			case c.HealthOK:
				c.Status = domain.StatusRunning
			case c.ServiceActive:
				c.Status = domain.StatusError
			default:
				c.Status = domain.StatusStopped
			}
			if !c.HealthOK && c.Port > 0 && portListening(c.Port) {
				c.Warnings = append(c.Warnings, fmt.Sprintf("端口 %d 有程序在监听，但不是 Clawbench", c.Port))
			}
			if !c.UnitExists && c.HealthOK {
				c.Warnings = append(c.Warnings, "实例是手动启动的，纳入时会先停掉它再交给 systemd")
			}
		}()
	}
	wg.Wait()
}

func (s *Service) adoptOne(ctx context.Context, username string, ac AuditContext) AdoptResult {
	res := AdoptResult{Username: username}
	unlock := s.lockTenant(username)
	defer unlock()

	if _, err := s.Store.GetTenantByUsername(username); err == nil {
		res.Error = "已经在管理列表里"
		return res
	}
	users, unitSet, managed, err := s.scanInputs(ctx)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	var target *sysops.UserInfo
	for i := range users {
		if users[i].Username == username {
			target = &users[i]
			break
		}
	}
	if target == nil {
		res.Error = "系统里没有这个用户"
		return res
	}
	c, ok := s.inspectUser(ctx, *target, unitSet, managed)
	if !ok {
		res.Error = "这个用户没有 Clawbench 数据目录"
		return res
	}
	cands := []AdoptCandidate{c}
	s.enrichCandidates(ctx, cands)
	c = cands[0]
	if c.Blocked != "" {
		res.Error = c.Blocked
		return res
	}

	if err := s.acquire(ctx); err != nil {
		res.Error = err.Error()
		return res
	}
	defer s.release()

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
	if err := s.Store.CreateTenant(t); err != nil {
		s.createMu.Unlock()
		res.Error = err.Error()
		return res
	}
	if err := s.Store.ClaimPort(t.Port, t.ID); err != nil {
		_ = s.Store.DeleteTenant(t.ID)
		s.createMu.Unlock()
		res.Error = err.Error()
		return res
	}
	s.createMu.Unlock()
	res.TenantID = t.ID

	// An empty password keeps the employee's existing password: adoption must
	// not silently lock anyone out.
	if err := s.writeTenantConfig(t, ""); err != nil {
		_ = s.Store.DeleteTenant(t.ID)
		_ = s.Store.ReleasePorts(t.ID)
		res.Error = "写租户配置失败：" + err.Error()
		return res
	}
	if err := s.writeUnit(ctx, t); err != nil {
		return s.adoptFailed(t, res, "写 systemd 单元失败", err, ac)
	}
	if err := s.reloadUnits(ctx); err != nil {
		return s.adoptFailed(t, res, "systemd 重载失败", err, ac)
	}
	if err := s.Sys.Enable(ctx, t.UnitName); err != nil {
		return s.adoptFailed(t, res, "启用服务失败", err, ac)
	}

	if c.ServiceActive {
		if err := s.Sys.Restart(ctx, t.UnitName); err != nil {
			return s.adoptFailed(t, res, "接管后重启失败", err, ac)
		}
	} else {
		// The instance may have been started by hand. Stop only a process that
		// belongs to this very user; anything else is reported, never killed.
		if portListening(t.Port) {
			if err := s.Sys.StopPortOwner(ctx, t.Port, t.Username); err != nil {
				return s.adoptFailed(t, res, "停掉旧进程失败", err, ac)
			}
		}
		if err := s.Sys.Start(ctx, t.UnitName); err != nil {
			return s.adoptFailed(t, res, "接管后启动失败", err, ac)
		}
	}

	info, err := s.waitHealthy(ctx, t.Port, "", 45*time.Second)
	if err != nil {
		return s.adoptFailed(t, res, "接管后健康检查失败", err, ac)
	}
	t.Status = domain.StatusRunning
	t.Version = info.Version
	t.LastHealthOK = true
	t.LastHealthAt = time.Now().Format(time.RFC3339)
	t.LastError = ""
	_ = s.Store.UpdateTenant(t)
	s.Audit(ac, "tenant.adopt", t, map[string]any{"port": t.Port, "uid": t.UID}, nil)
	res.Status = domain.StatusRunning
	return res
}

func (s *Service) adoptFailed(t *domain.Tenant, res AdoptResult, action string, cause error, ac AuditContext) AdoptResult {
	t.Status = domain.StatusError
	t.LastError = action + "：" + cause.Error()
	t.LastHealthOK = false
	_ = s.Store.UpdateTenant(t)
	s.Audit(ac, "tenant.adopt", t, map[string]any{"port": t.Port}, cause)
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
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 400*time.Millisecond)
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
