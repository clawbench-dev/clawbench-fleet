package fleet

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"clawbench-fleet/internal/config"
	"clawbench-fleet/internal/domain"
	"clawbench-fleet/internal/store"
	"clawbench-fleet/internal/sysops"
)

const passwordSalt = "clawbench-salt"

type Service struct {
	Cfg   config.Config
	Store *store.Store
	Sys   sysops.System

	settingsMu sync.RWMutex
	settings   domain.Settings

	configMu     sync.RWMutex
	tenantConfig []domain.ConfigEntry

	locks     keyedMutex
	createMu  sync.Mutex
	upgradeMu sync.Mutex
	upgrading bool
	sem       chan struct{}

	metricsMu sync.RWMutex
	metrics   map[int64]domain.Metrics
	// lastSample is the time the last history point was written. It is only
	// touched by the metrics loop.
	lastSample time.Time

	artifactsMu sync.Mutex
	artifacts   map[int64]map[string][]byte
	artifactAt  map[int64]time.Time

	http *http.Client
	// https is used only after a loopback instance redirects the plain health
	// check to HTTPS. Instances generate their own certificate, so the chain is
	// not verifiable; the target is always this machine's loopback address and
	// the body still has to identify itself as Clawbench.
	https *http.Client
	// healthTimeout caps how long an operation waits for an instance to come
	// up. Production uses the 45–60s defaults at each call site; tests lower
	// it so failure paths do not sit in a real one-minute wait.
	healthTimeout time.Duration

	cpuMu   sync.Mutex
	cpuPrev cpuSample

	dailyMu   sync.Mutex
	lastDaily map[string]string

	orphansMu sync.RWMutex
	orphans   []string

	// provisioners tracks in-flight background creations so a synchronous
	// caller can find out when its tenant is really ready.
	provisioners *provisionTracker
}

type cpuSample struct {
	total uint64
	idle  uint64
	at    time.Time
}

func New(cfg config.Config, st *store.Store, sys sysops.System) (*Service, error) {
	def := SettingsFromConfig(cfg)
	stg, err := st.LoadSettings(def)
	if err != nil {
		return nil, err
	}
	if vErr := validateSettings(stg); vErr != nil {
		return nil, vErr
	}
	entries, err := st.LoadTenantConfig()
	if err != nil {
		return nil, err
	}
	if err := validateTenantConfig(entries); err != nil {
		return nil, err
	}
	if err := validateTLS(stg); err != nil {
		return nil, err
	}
	s := &Service{
		Cfg:          cfg,
		Store:        st,
		Sys:          sys,
		settings:     stg,
		tenantConfig: entries,
		sem:          make(chan struct{}, 4),
		metrics:      map[int64]domain.Metrics{},
		artifacts:    map[int64]map[string][]byte{},
		artifactAt:   map[int64]time.Time{},
		http:         &http.Client{Timeout: 3 * time.Second, CheckRedirect: noRedirect},
		https: &http.Client{
			Timeout:       3 * time.Second,
			CheckRedirect: noRedirect,
			Transport: &http.Transport{
				//nolint:gosec // the only URL this client sees is loopback https chosen by healthCheck, and the certificate is self-signed by design.
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
			},
		},
		healthTimeout: 60 * time.Second,
		lastDaily:     map[string]string{},
		provisioners:  &provisionTracker{done: map[int64]chan struct{}{}},
	}
	if err := st.SeedPorts(stg.PortStart, stg.PortEnd); err != nil {
		return nil, err
	}
	if _, err := st.MarkInterruptedJobs(); err != nil {
		return nil, err
	}
	return s, nil
}

func SettingsFromConfig(cfg config.Config) domain.Settings {
	return domain.Settings{
		PortStart:          cfg.Clawbench.PortStart,
		PortEnd:            cfg.Clawbench.PortEnd,
		PortsPerTenant:     cfg.Clawbench.PortsPerTenant,
		UIDStart:           cfg.Clawbench.UIDStart,
		UIDEnd:             cfg.Clawbench.UIDEnd,
		DefaultCPUQuota:    cfg.Clawbench.DefaultCPUQuota,
		DefaultMemoryMax:   cfg.Clawbench.DefaultMemoryMax,
		DefaultTasksMax:    cfg.Clawbench.DefaultTasksMax,
		DiskWarnGB:         cfg.Clawbench.DiskWarnGB,
		DefaultDiskQuotaGB: cfg.Clawbench.DiskQuotaGB,
		MetricsRetention:   7,
		BackupEnabled:      cfg.Backup.Enabled,
		BackupHour:         cfg.Backup.Hour,
		BackupRetention:    cfg.Backup.RetentionDays,
		TrashRetention:     cfg.Trash.RetentionDays,
		AuditRetention:     cfg.Audit.RetentionDays,
		AllowPortForward:   cfg.Clawbench.AllowPortForward,
		PublicHost:         cfg.Clawbench.PublicHost,
		GuestEnabled:       true,
		SSHEnabled:         cfg.SSH.Enabled,
		TLSEnabled:         cfg.TenantTLS.Enabled,
		TLSCertDir:         cfg.TenantTLS.CertDir,
	}
}

var (
	cpuQuotaRe  = regexp.MustCompile(`^[0-9]{1,4}%$`)
	memoryMaxRe = regexp.MustCompile(`^[0-9]{1,5}[KMGTP]?$`)
	usernameRe  = regexp.MustCompile(`^[a-z][a-z0-9_-]{2,31}$`)
	versionRe   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,63}$`)
)

var reservedNames = map[string]bool{
	"root": true, "daemon": true, "bin": true, "sys": true, "sync": true,
	"games": true, "man": true, "lp": true, "mail": true, "news": true,
	"uucp": true, "proxy": true, "www-data": true, "backup": true, "list": true,
	"irc": true, "gnats": true, "nobody": true, "systemd-network": true,
	"systemd-resolve": true, "messagebus": true, "sshd": true,
	"clawbench": true, platformName: true,
}

func ValidateUsername(username string) error {
	if !usernameRe.MatchString(username) {
		return errors.New("登录名必须是 3-32 位，以小写字母开头，只能包含小写字母、数字、下划线和短横线")
	}
	if reservedNames[username] {
		return fmt.Errorf("登录名 %q 是系统保留名", username)
	}
	return nil
}

func validateSettings(s domain.Settings) error {
	if err := validateRangeSettings(s); err != nil {
		return err
	}
	if err := validateQuotaSettings(s); err != nil {
		return err
	}
	if err := validateTLS(s); err != nil {
		return err
	}
	return validateRetentionSettings(s)
}

func validateTLS(s domain.Settings) error {
	if s.TLSEnabled && s.TLSCertDir != "" && !filepath.IsAbs(s.TLSCertDir) {
		return errors.New("证书目录必须是绝对路径")
	}
	return nil
}

// configKeyRe matches dotted lower-case keys such as "tls.cert_dir" or
// "chat.page_size". Keeping it strict means a typo is rejected here instead of
// silently landing in every instance's config.yaml.
var configKeyRe = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)*$`)

// reservedConfigKeys are the keys the platform owns: writing them from the
// generic key/value table would fight with port changes, password resets and
// the certificate distribution.
var reservedConfigKeys = map[string]bool{
	"port":                 true,
	"host":                 true,
	"password":             true,
	"tls.cert_dir":         true,
	"port_forward":         true,
	"port_forward.enabled": true,
}

func validateTenantConfig(entries []domain.ConfigEntry) error {
	seen := map[string]bool{}
	for _, e := range entries {
		key := strings.TrimSpace(e.Key)
		if key == "" {
			return errors.New("统一配置里有空的配置项名称")
		}
		if !configKeyRe.MatchString(key) {
			return fmt.Errorf("配置项名称 %q 不合法：只能用小写字母、数字、下划线和点（例如 chat.page_size）", e.Key)
		}
		if reservedConfigKeys[key] {
			return fmt.Errorf("配置项 %q 由平台自己维护，不能在这里覆盖", key)
		}
		if seen[key] {
			return fmt.Errorf("配置项 %q 重复了", key)
		}
		if strings.ContainsAny(e.Value, "\n\r") {
			return fmt.Errorf("配置项 %q 的值不能包含换行", key)
		}
		seen[key] = true
	}
	return nil
}

func validateRangeSettings(s domain.Settings) error {
	if s.PortStart < 1 || s.PortEnd > 65535 || s.PortStart >= s.PortEnd {
		return fmt.Errorf("端口范围不合法：%d-%d", s.PortStart, s.PortEnd)
	}
	if s.PortsPerTenant < 1 || s.PortsPerTenant > 10 {
		return errors.New("每个租户预留端口数必须在 1-10 之间")
	}
	if s.UIDStart < 1000 || s.UIDEnd > 60000 || s.UIDStart >= s.UIDEnd {
		return fmt.Errorf("UID 范围不合法：%d-%d", s.UIDStart, s.UIDEnd)
	}
	if s.DiskWarnGB < 0 {
		return errors.New("磁盘提醒阈值不能是负数（0 表示关闭提醒）")
	}
	if s.DefaultDiskQuotaGB < 0 {
		return errors.New("默认磁盘上限不能是负数（0 表示不限制）")
	}
	if s.MetricsRetention < 1 || s.MetricsRetention > 90 {
		return errors.New("用量历史保留天数必须在 1-90 之间")
	}
	return nil
}

func validateQuotaSettings(s domain.Settings) error {
	if !cpuQuotaRe.MatchString(s.DefaultCPUQuota) {
		return fmt.Errorf("CPU 上限格式不对：%q（例如 200%%）", s.DefaultCPUQuota)
	}
	if !memoryMaxRe.MatchString(s.DefaultMemoryMax) {
		return fmt.Errorf("内存上限格式不对：%q（例如 4G）", s.DefaultMemoryMax)
	}
	if s.DefaultTasksMax < 1 {
		return errors.New("进程数上限必须大于 0")
	}
	return nil
}

func validateRetentionSettings(s domain.Settings) error {
	if s.BackupHour < 0 || s.BackupHour > 23 {
		return errors.New("备份时间必须是 0-23 点")
	}
	if s.BackupRetention < 1 {
		return errors.New("备份保留天数必须大于 0")
	}
	if s.TrashRetention < 1 {
		return errors.New("回收站保留天数必须大于 0")
	}
	if s.AuditRetention < 1 {
		return errors.New("审计保留天数必须大于 0")
	}
	return nil
}

func (s *Service) Settings() domain.Settings {
	s.settingsMu.RLock()
	defer s.settingsMu.RUnlock()
	return s.settings
}

func (s *Service) UpdateSettings(next domain.Settings) error {
	if err := validateSettings(next); err != nil {
		return err
	}
	if err := s.Store.SaveSettings(next); err != nil {
		return err
	}
	if err := s.Store.SeedPorts(next.PortStart, next.PortEnd); err != nil {
		return err
	}
	s.settingsMu.Lock()
	s.settings = next
	s.settingsMu.Unlock()
	return nil
}

// TenantConfig is the unified config pushed into every instance's config.yaml.
func (s *Service) TenantConfig() []domain.ConfigEntry {
	s.configMu.RLock()
	defer s.configMu.RUnlock()
	out := make([]domain.ConfigEntry, len(s.tenantConfig))
	copy(out, s.tenantConfig)
	return out
}

// UpdateTenantConfig validates and saves the unified config. Nothing is pushed
// to instances here: a push restarts running instances, so it is a separate,
// explicit step.
func (s *Service) UpdateTenantConfig(entries []domain.ConfigEntry) error {
	clean := make([]domain.ConfigEntry, 0, len(entries))
	for _, e := range entries {
		e.Key = strings.TrimSpace(e.Key)
		if e.Key == "" && e.Value == "" {
			continue
		}
		clean = append(clean, e)
	}
	if err := validateTenantConfig(clean); err != nil {
		return err
	}
	if err := s.Store.SaveTenantConfig(clean); err != nil {
		return err
	}
	s.configMu.Lock()
	s.tenantConfig = clean
	s.configMu.Unlock()
	return nil
}

func (s *Service) PublicHost() string {
	st := s.Settings()
	if st.PublicHost != "" {
		return st.PublicHost
	}
	return config.DetectPublicHost()
}

// Address builds a tenant's URL. An operator-typed public host always wins;
// otherwise the host the admin is already using in the browser is reused, so
// a server reachable at 10.0.0.5 hands out 10.0.0.5 rather than whatever
// interface DetectPublicHost happens to pick first.
func (s *Service) Address(t domain.Tenant) string {
	return s.AddressFor(t, "")
}

// AddressFor is Address with the caller's host (the browser's Host header)
// as the fallback. An empty browserHost keeps the old auto-detection.
func (s *Service) AddressFor(t domain.Tenant, browserHost string) string {
	if st := s.Settings(); st.PublicHost != "" {
		return t.Address(st.PublicHost)
	}
	if host := normalizeHost(browserHost); host != "" {
		return t.Address(host)
	}
	return t.Address(config.DetectPublicHost())
}

// normalizeHost strips the port and IPv6 brackets from a Host header so it can
// be recombined with a tenant's own port.
func normalizeHost(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if host, _, err := net.SplitHostPort(raw); err == nil {
		raw = host
	}
	raw = strings.Trim(raw, "[]")
	// A loopback host in the browser would produce useless employee links, so
	// fall back to auto-detection in that case.
	if ip := net.ParseIP(raw); ip != nil && ip.IsLoopback() {
		return ""
	}
	if strings.EqualFold(raw, "localhost") {
		return ""
	}
	return raw
}

// --- locking ---

type keyedMutex struct {
	mu sync.Mutex
	m  map[string]*sync.Mutex
}

func (k *keyedMutex) Lock(key string) func() {
	k.mu.Lock()
	if k.m == nil {
		k.m = map[string]*sync.Mutex{}
	}
	m, ok := k.m[key]
	if !ok {
		m = &sync.Mutex{}
		k.m[key] = m
	}
	k.mu.Unlock()
	m.Lock()
	return m.Unlock
}

// TryLock is used by background loops so a long operation on one tenant never
// stalls the health sweep for everyone else.
func (k *keyedMutex) TryLock(key string) (func(), bool) {
	k.mu.Lock()
	if k.m == nil {
		k.m = map[string]*sync.Mutex{}
	}
	m, ok := k.m[key]
	if !ok {
		m = &sync.Mutex{}
		k.m[key] = m
	}
	k.mu.Unlock()
	if !m.TryLock() {
		return nil, false
	}
	return m.Unlock, true
}

func (s *Service) lockTenant(username string) func() { return s.locks.Lock(username) }

func (s *Service) acquire(ctx context.Context) error {
	select {
	case s.sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Service) release() { <-s.sem }

// beginUpgrade serializes the whole version-switching workflow. Restarting a
// subset of tenants while another upgrade is running would leave the fleet on
// an unpredictable mix of versions.
func (s *Service) beginUpgrade() error {
	s.upgradeMu.Lock()
	defer s.upgradeMu.Unlock()
	if s.upgrading {
		return errors.New("已有升级或回滚任务正在进行，请等它结束")
	}
	s.upgrading = true
	return nil
}

func (s *Service) endUpgrade() {
	s.upgradeMu.Lock()
	s.upgrading = false
	s.upgradeMu.Unlock()
}

// --- passwords ---

const passwordAlphabet = "abcdefghjkmnpqrstuvwxyzABCDEFGHJKMNPQRSTUVWXYZ23456789"

func GeneratePassword(length int) (string, error) {
	if length < 8 {
		length = 12
	}
	buf := make([]byte, length)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	out := make([]byte, length)
	hasLetter, hasDigit := false, false
	for i, b := range buf {
		c := passwordAlphabet[int(b)%len(passwordAlphabet)]
		out[i] = c
		if c >= '0' && c <= '9' {
			hasDigit = true
		} else {
			hasLetter = true
		}
	}
	// Guarantee the Clawbench password policy (letters + digits).
	if !hasDigit {
		out[0] = '7'
	}
	if !hasLetter {
		out[1] = 'k'
	}
	return string(out), nil
}

func HashPassword(password string) string {
	sum := sha256.Sum256([]byte(password + passwordSalt))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// --- tenant config ---

func (s *Service) writeTenantConfig(t *domain.Tenant, password string) error {
	path := s.tenantConfigPath(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	cfg := map[string]any{}
	if data, err := os.ReadFile(path); err == nil {
		_ = yamlUnmarshal(data, &cfg)
	}
	cfg["port"] = t.Port
	cfg["host"] = "0.0.0.0"
	if password != "" {
		cfg["password"] = HashPassword(password)
	}
	pf, _ := cfg["port_forward"].(map[string]any)
	if pf == nil {
		pf = map[string]any{}
	}
	pf["enabled"] = s.Settings().AllowPortForward
	pf["transport"] = "h2"
	cfg["port_forward"] = pf

	// The certificate copy lives next to the tenant's config so the files can
	// be owned by that tenant. Only point Clawbench at it once it is there.
	if s.Settings().TLSEnabled {
		if err := s.distributeCert(t); err != nil {
			return err
		}
		cfg["tls"] = map[string]any{"cert_dir": tenantCertDir(t)}
	} else if tlsCfg, ok := cfg["tls"].(map[string]any); ok {
		// Dropping the section is what turns HTTPS back off: Clawbench falls
		// back to plain HTTP when cert_dir is missing or empty.
		delete(tlsCfg, "cert_dir")
		if len(tlsCfg) == 0 {
			delete(cfg, "tls")
		}
	}

	// Unified config last, so it wins over anything already in the file.
	applyTenantConfigEntries(cfg, s.TenantConfig())

	data, err := yamlMarshal(cfg)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".config-*.yaml")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if t.UID > 0 {
		_ = os.Chown(tmpName, t.UID, t.GID)
	}
	return os.Rename(tmpName, path)
}

// tenantConfigPath follows the tenant record rather than the configured home
// root, so adopted tenants keep working even when their home is /root or an
// older location.
func (s *Service) tenantConfigPath(t *domain.Tenant) string {
	return filepath.Join(t.DataDir, "config", "config.yaml")
}

// applyTenantConfigEntries writes the platform's key/value table into cfg.
// Keys are dotted paths ("chat.page_size") and each segment becomes a YAML
// mapping level, so an operator can set nested settings without a schema.
func applyTenantConfigEntries(cfg map[string]any, entries []domain.ConfigEntry) {
	for _, e := range entries {
		key := strings.TrimSpace(e.Key)
		if key == "" {
			continue
		}
		setConfigPath(cfg, strings.Split(key, "."), parseConfigValue(e.Value))
	}
}

func setConfigPath(cfg map[string]any, path []string, value any) {
	for i, seg := range path {
		if i == len(path)-1 {
			cfg[seg] = value
			return
		}
		next, ok := cfg[seg].(map[string]any)
		if !ok {
			// A scalar already sits where a section should be. The platform
			// value wins, so replace it with a mapping.
			next = map[string]any{}
			cfg[seg] = next
		}
		cfg = next
	}
}

// parseConfigValue keeps numbers and booleans as real YAML types; everything
// else stays a string. Writing "20" as a string would break Clawbench settings
// that expect an integer.
func parseConfigValue(raw string) any {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "true":
		return true
	case "false":
		return false
	}
	if n, err := strconv.Atoi(raw); err == nil {
		return n
	}
	if f, err := strconv.ParseFloat(raw, 64); err == nil {
		return f
	}
	return raw
}

// --- health ---

// noRedirect stops the health clients from following redirects on their own,
// so a redirect can be inspected instead of silently followed.
func noRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

// healthTLSRedirect reports that the instance asked for the health check over
// HTTPS, carrying the loopback URL to retry.
type healthTLSRedirect struct{ url string }

func (e *healthTLSRedirect) Error() string { return "health endpoint requires https" }

// healthCheck probes the instance. An instance with a certificate serves TLS
// on its only port and rejects a plain-HTTP request outright (the TLS handshake
// fails and net/http answers 400), so HTTPS is tried first. Plain HTTP stays as
// the fallback for instances that have no certificate, including ones this
// platform did not ship a certificate to.
func (s *Service) healthCheck(ctx context.Context, port int) (*domain.HealthInfo, error) {
	info, tlsErr := s.getHealth(ctx, s.https, fmt.Sprintf("https://127.0.0.1:%d/api/health", port))
	if tlsErr == nil {
		return info, nil
	}
	httpInfo, httpErr := s.getHealth(ctx, s.http, fmt.Sprintf("http://127.0.0.1:%d/api/health", port))
	if httpErr == nil {
		return httpInfo, nil
	}
	var redirect *healthTLSRedirect
	if errors.As(httpErr, &redirect) {
		return s.getHealth(ctx, s.https, redirect.url)
	}
	return nil, httpErr
}

func (s *Service) getHealth(ctx context.Context, client *http.Client, rawURL string) (*domain.HealthInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, http.NoBody)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		if loc, lerr := resp.Location(); lerr == nil && loc.Scheme == "https" && isLoopbackHost(loc.Hostname()) {
			return nil, &healthTLSRedirect{url: loc.String()}
		}
		return nil, fmt.Errorf("health returned HTTP %d", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("health returned HTTP %d", resp.StatusCode)
	}
	var info domain.HealthInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, err
	}
	if info.App != "clawbench" {
		return nil, fmt.Errorf("health app is %q, not clawbench", info.App)
	}
	return &info, nil
}

// isLoopbackHost keeps the HTTPS retry on this machine. The redirect comes from
// the instance itself, so anything else would turn the health check into a
// request to an address the instance picks.
func isLoopbackHost(host string) bool {
	return host == "127.0.0.1" || host == "localhost" || host == "::1"
}

func (s *Service) waitHealthy(ctx context.Context, port int, wantVersion string, timeout time.Duration) (*domain.HealthInfo, error) {
	if s.healthTimeout > 0 && timeout > s.healthTimeout {
		timeout = s.healthTimeout
	}
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		info, err := s.healthCheck(ctx, port)
		if err == nil {
			// Clawbench reports the git tag ("v0.143.0") while npm publishes
			// "0.143.0"; compare without the prefix so an npm install is not
			// rejected as "wrong version".
			if wantVersion == "" || normalizeVersion(info.Version) == normalizeVersion(wantVersion) {
				return info, nil
			}
			lastErr = fmt.Errorf("版本还是 %s，等待 %s", info.Version, wantVersion)
		} else {
			lastErr = err
		}
		if time.Now().After(deadline) {
			if lastErr == nil {
				lastErr = errors.New("健康检查超时")
			}
			return nil, fmt.Errorf("实例没有在 %s 内就绪：%w", timeout, lastErr)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// --- audit ---

type AuditContext struct {
	Actor     string
	IP        string
	UserAgent string
}

func (s *Service) Audit(ctx AuditContext, action string, t *domain.Tenant, detail map[string]any, err error) {
	if detail == nil {
		detail = map[string]any{}
	}
	data, _ := json.Marshal(sanitizeDetail(detail))
	entry := &domain.AuditEntry{
		Actor:     auditActor(ctx.Actor),
		Action:    action,
		Detail:    string(data),
		Result:    "ok",
		IP:        ctx.IP,
		UserAgent: ctx.UserAgent,
	}
	if t != nil {
		entry.TenantID = t.ID
		entry.TenantName = t.Username
	}
	if err != nil {
		entry.Result = statusFailed
		entry.Error = err.Error()
	}
	_ = s.Store.AddAudit(entry)
}

func auditActor(actor string) string {
	if strings.TrimSpace(actor) == "" {
		return "admin"
	}
	return actor
}

func sanitizeDetail(in map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range in {
		lower := strings.ToLower(k)
		if strings.Contains(lower, "password") || strings.Contains(lower, "secret") ||
			strings.Contains(lower, "token") || strings.Contains(lower, "api_key") {
			out[k] = "***"
			continue
		}
		switch val := v.(type) {
		case map[string]any:
			out[k] = sanitizeDetail(val)
		default:
			out[k] = v
		}
	}
	return out
}

// blocksToBytes converts a filesystem block count to bytes without letting a
// 32-bit/64-bit conversion silently overflow.
func blocksToBytes(blocks uint64, blockSize int64) int64 {
	if blockSize <= 0 {
		return 0
	}
	if blocks > uint64(math.MaxInt64)/uint64(blockSize) {
		return math.MaxInt64
	}
	return int64(blocks) * blockSize //nolint:gosec // overflow checked above
}

// --- host stats ---

type HostStats struct {
	CPUPercent float64 `json:"cpu_percent"`
	MemTotal   int64   `json:"mem_total"`
	MemUsed    int64   `json:"mem_used"`
	DiskTotal  int64   `json:"disk_total"`
	DiskFree   int64   `json:"disk_free"`
	Load1      float64 `json:"load1"`
}

func (s *Service) HostStats() HostStats {
	var st HostStats
	st.CPUPercent = s.hostCPU()
	if data, err := os.ReadFile("/proc/meminfo"); err == nil {
		var total, available int64
		for _, line := range strings.Split(string(data), "\n") {
			fields := strings.Fields(line)
			if len(fields) < 2 {
				continue
			}
			switch fields[0] {
			case "MemTotal:":
				total, _ = strconv.ParseInt(fields[1], 10, 64)
			case "MemAvailable:":
				available, _ = strconv.ParseInt(fields[1], 10, 64)
			}
		}
		st.MemTotal = total * 1024
		st.MemUsed = (total - available) * 1024
	}
	if data, err := os.ReadFile("/proc/loadavg"); err == nil {
		fields := strings.Fields(string(data))
		if len(fields) > 0 {
			st.Load1, _ = strconv.ParseFloat(fields[0], 64)
		}
	}
	// Measure the disk that backs the tenants' home directories, not the one
	// holding the fleet's own data: the numbers here describe how much room the
	// tenants have left. DataDir may live on a different filesystem entirely.
	// Fall back to the root filesystem when the home root does not exist, so a
	// slim install never reports zero total space.
	if st.DiskTotal, st.DiskFree = s.diskOf(s.Cfg.System.HomeRoot); st.DiskTotal == 0 {
		st.DiskTotal, st.DiskFree = s.diskOf("/")
	}
	return st
}

// diskOf reports the total and available bytes of the filesystem holding path.
// It returns zeros when path cannot be measured.
func (s *Service) diskOf(path string) (total, free int64) {
	var fs syscall.Statfs_t
	if err := syscall.Statfs(path, &fs); err != nil {
		return 0, 0
	}
	return blocksToBytes(fs.Blocks, fs.Bsize), blocksToBytes(fs.Bavail, fs.Bsize)
}

func (s *Service) hostCPU() float64 {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0
	}
	line := strings.SplitN(string(data), "\n", 2)[0]
	fields := strings.Fields(line)
	if len(fields) < 5 || fields[0] != "cpu" {
		return 0
	}
	var vals []uint64
	for _, f := range fields[1:] {
		v, _ := strconv.ParseUint(f, 10, 64)
		vals = append(vals, v)
	}
	var total uint64
	for _, v := range vals {
		total += v
	}
	idle := vals[3]
	if len(vals) > 4 {
		idle += vals[4]
	}
	nowT := time.Now()
	s.cpuMu.Lock()
	prev := s.cpuPrev
	s.cpuPrev = cpuSample{total: total, idle: idle, at: nowT}
	s.cpuMu.Unlock()
	if prev.at.IsZero() || total <= prev.total {
		return 0
	}
	dt := float64(total - prev.total)
	di := float64(idle - prev.idle)
	if dt <= 0 {
		return 0
	}
	pct := (dt - di) / dt * 100
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	return pct
}

// --- helpers ---

// ErrTenantNotFound is returned when the id does not name an existing tenant.
// It wraps store.ErrNotFound so the HTTP layer can answer 404.
var ErrTenantNotFound = fmt.Errorf("租户不存在: %w", store.ErrNotFound)

func (s *Service) tenantByID(id int64) (*domain.Tenant, error) {
	t, err := s.Store.GetTenant(id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, ErrTenantNotFound
	}
	return t, err
}

// allTenants pages through the list so a deployment with more than one page
// still gets complete background processing.
func (s *Service) allTenants() ([]domain.Tenant, error) {
	var out []domain.Tenant
	for page := 1; ; page++ {
		items, total, err := s.Store.ListTenants(store.TenantFilter{Page: page, PageSize: 200, IncludeTrashed: true})
		if err != nil {
			return nil, err
		}
		out = append(out, items...)
		if len(items) == 0 || len(out) >= total {
			break
		}
	}
	return out, nil
}

func (s *Service) Orphans() []string {
	s.orphansMu.RLock()
	defer s.orphansMu.RUnlock()
	return append([]string(nil), s.orphans...)
}

func (s *Service) allocateUID(ctx context.Context) (int, error) {
	used, err := s.Sys.UsedUIDs(ctx)
	if err != nil {
		return 0, err
	}
	if dbUIDs, err := s.Store.AllTenantUIDs(); err == nil {
		for uid := range dbUIDs {
			used[uid] = true
		}
	}
	st := s.Settings()
	for uid := st.UIDStart; uid <= st.UIDEnd; uid++ {
		if !used[uid] {
			return uid, nil
		}
	}
	return 0, errors.New("UID 范围里没有空闲值了")
}

func (s *Service) unitContent(t *domain.Tenant) string {
	return fmt.Sprintf(`[Unit]
Description=Clawbench instance for %s
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=%s
Group=%s
WorkingDirectory=%s
Environment=HOME=%s
Environment=PATH=/usr/local/bin:/usr/bin:/bin
ExecStart=%s/current/clawbench --port %d --data-dir %s
Restart=always
RestartSec=3
TimeoutStopSec=30
LimitNOFILE=65536
CPUQuota=%s
MemoryMax=%s
TasksMax=%d
NoNewPrivileges=true

[Install]
WantedBy=multi-user.target
`,
		t.Username, t.Username, t.Username, t.HomeDir, t.HomeDir,
		s.Cfg.Clawbench.InstallDir, t.Port, t.DataDir,
		t.CPUQuota, t.MemoryMax, t.TasksMax)
}

func (s *Service) writeUnit(ctx context.Context, t *domain.Tenant) error {
	return s.Sys.WriteUnit(ctx, t.UnitName, s.unitContent(t))
}

func (s *Service) reloadUnits(ctx context.Context) error {
	return s.Sys.DaemonReload(ctx)
}
