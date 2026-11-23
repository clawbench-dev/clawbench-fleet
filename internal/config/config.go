package config

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config is the platform's own configuration. It is deliberately small: all
// runtime state lives in SQLite, and only bootstrap/deployment knobs live here.
type Config struct {
	Listen    string          `yaml:"listen"`
	DataDir   string          `yaml:"data_dir"`
	Clawbench ClawbenchConfig `yaml:"clawbench"`
	Backup    BackupConfig    `yaml:"backup"`
	Trash     TrashConfig     `yaml:"trash"`
	Audit     AuditConfig     `yaml:"audit"`
	TLS       TLSConfig       `yaml:"tls"`
	TenantTLS TenantTLSConfig `yaml:"tenant_tls"`
	System    SystemConfig    `yaml:"system"`
	SSH       SSHConfig       `yaml:"ssh"`
	Dev       bool            `yaml:"dev"`
}

// SSHConfig controls the shell access given to tenant accounts. The platform
// only prepares the account (shell + password); it never edits sshd_config, so
// the server's own SSH policy stays in the operator's hands.
type SSHConfig struct {
	// Enabled is the default for newly created tenants. Each tenant can still
	// turn it off from its detail page.
	Enabled bool `yaml:"enabled"`
	// Shell is the login shell assigned to tenants with SSH enabled.
	Shell string `yaml:"shell"`
	// Port is used only for the ssh command shown in the UI. Zero means "ask
	// sshd" and fall back to 22.
	Port int `yaml:"port"`
}

type ClawbenchConfig struct {
	InstallDir       string `yaml:"install_dir"`
	PortStart        int    `yaml:"port_start"`
	PortEnd          int    `yaml:"port_end"`
	PortsPerTenant   int    `yaml:"ports_per_tenant"`
	UIDStart         int    `yaml:"uid_start"`
	UIDEnd           int    `yaml:"uid_end"`
	DefaultCPUQuota  string `yaml:"default_cpu_quota"`
	DefaultMemoryMax string `yaml:"default_memory_max"`
	DefaultTasksMax  int    `yaml:"default_tasks_max"`
	DiskWarnGB       int    `yaml:"disk_warn_gb"`
	// DiskQuotaGB is the default hard limit for new tenants (0 = no limit).
	DiskQuotaGB      int    `yaml:"disk_quota_gb"`
	AllowPortForward bool   `yaml:"allow_port_forward"`
	PublicHost       string `yaml:"public_host"`
	// NpmPackage is the wrapper package published to npm. The actual binary
	// comes from the per-platform package it depends on.
	NpmPackage string `yaml:"npm_package"`
	// NpmRegistry overrides the default registry, e.g. an intranet mirror.
	NpmRegistry string `yaml:"npm_registry"`
}

type BackupConfig struct {
	Enabled       bool `yaml:"enabled"`
	Hour          int  `yaml:"hour"`
	RetentionDays int  `yaml:"retention_days"`
}

type TrashConfig struct {
	RetentionDays int `yaml:"retention_days"`
}

type AuditConfig struct {
	RetentionDays int `yaml:"retention_days"`
}

type TLSConfig struct {
	// CertDir is the platform's own HTTPS certificate, serving the admin UI on
	// the listen address. Unrelated to the certificates shipped to instances.
	CertDir string `yaml:"cert_dir"`
}

// TenantTLSConfig controls the certificate the platform hands to every
// instance. Instances always speak HTTPS once this is on.
type TenantTLSConfig struct {
	Enabled bool `yaml:"enabled"`
	// CertDir holds the shared certificate. Empty means "<data_dir>/certs",
	// and a self-signed pair is generated there on first start.
	CertDir string `yaml:"cert_dir"`
}

type SystemConfig struct {
	HomeRoot   string `yaml:"home_root"`
	SystemdDir string `yaml:"systemd_dir"`
	UserShell  string `yaml:"user_shell"`
}

func Default() Config {
	return Config{
		Listen:  "0.0.0.0:30000",
		DataDir: "/var/lib/clawbench-fleet",
		Clawbench: ClawbenchConfig{
			InstallDir:       "/opt/clawbench-fleet",
			PortStart:        21000,
			PortEnd:          29999,
			PortsPerTenant:   3,
			UIDStart:         3000,
			UIDEnd:           9999,
			DefaultCPUQuota:  "200%",
			DefaultMemoryMax: "4G",
			DefaultTasksMax:  512,
			DiskWarnGB:       5,
			DiskQuotaGB:      0,
			NpmPackage:       "@xulongzhe/clawbench",
		},
		Backup:    BackupConfig{Enabled: true, Hour: 3, RetentionDays: 7},
		Trash:     TrashConfig{RetentionDays: 30},
		Audit:     AuditConfig{RetentionDays: 180},
		TLS:       TLSConfig{},
		TenantTLS: TenantTLSConfig{Enabled: true},
		System: SystemConfig{
			HomeRoot:   "/home",
			SystemdDir: "/etc/systemd/system",
			UserShell:  "/usr/sbin/nologin",
		},
		SSH: SSHConfig{
			Enabled: true,
			Shell:   "/bin/bash",
			Port:    0,
		},
	}
}

// Load reads path over the defaults. A missing file is not an error: the
// defaults are usable for a first run and for --dev.
func Load(path string) (Config, error) {
	cfg := Default()
	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				return cfg, err
			}
		} else if err := yaml.Unmarshal(data, &cfg); err != nil {
			return cfg, fmt.Errorf("parse %s: %w", path, err)
		}
	}
	if err := cfg.Normalize(); err != nil {
		return cfg, err
	}
	return cfg, nil
}

var (
	cpuQuotaRe  = regexp.MustCompile(`^[0-9]{1,4}%$`)
	memoryMaxRe = regexp.MustCompile(`^[0-9]{1,5}[KMGTP]?$`)
)

func (c *Config) Normalize() error {
	if err := c.normalizeEndpoint(); err != nil {
		return err
	}
	if err := c.normalizeClawbench(); err != nil {
		return err
	}
	c.normalizeRetention()
	c.normalizeSystem()
	c.normalizeSSH()
	return nil
}

func (c *Config) normalizeEndpoint() error {
	if strings.TrimSpace(c.Listen) == "" {
		return errors.New("listen must not be empty")
	}
	if _, _, err := net.SplitHostPort(c.Listen); err != nil {
		return fmt.Errorf("listen %q is not host:port: %w", c.Listen, err)
	}
	abs, err := filepath.Abs(c.DataDir)
	if err != nil {
		return fmt.Errorf("data_dir: %w", err)
	}
	c.DataDir = abs
	return nil
}

func (c *Config) normalizeClawbench() error {
	if c.Clawbench.InstallDir == "" {
		return errors.New("clawbench.install_dir must not be empty")
	}
	if c.Clawbench.PortStart < 1 || c.Clawbench.PortEnd > 65535 || c.Clawbench.PortStart >= c.Clawbench.PortEnd {
		return fmt.Errorf("invalid port range %d-%d", c.Clawbench.PortStart, c.Clawbench.PortEnd)
	}
	if c.Clawbench.PortsPerTenant < 1 {
		return errors.New("clawbench.ports_per_tenant must be at least 1")
	}
	if c.Clawbench.UIDStart < 1000 || c.Clawbench.UIDEnd > 60000 || c.Clawbench.UIDStart >= c.Clawbench.UIDEnd {
		return fmt.Errorf("invalid uid range %d-%d", c.Clawbench.UIDStart, c.Clawbench.UIDEnd)
	}
	if err := validateDefaultQuota(c.Clawbench.DefaultCPUQuota, c.Clawbench.DefaultMemoryMax, c.Clawbench.DefaultTasksMax); err != nil {
		return err
	}
	if strings.TrimSpace(c.Clawbench.NpmPackage) == "" {
		c.Clawbench.NpmPackage = "@xulongzhe/clawbench"
	}
	if !strings.Contains(c.Clawbench.NpmPackage, "/") {
		return fmt.Errorf("npm_package %q 必须是 @scope/name 形式", c.Clawbench.NpmPackage)
	}
	if c.Clawbench.DiskQuotaGB < 0 {
		return fmt.Errorf("disk_quota_gb must not be negative")
	}
	if c.Backup.Hour < 0 || c.Backup.Hour > 23 {
		return fmt.Errorf("backup.hour must be 0-23, got %d", c.Backup.Hour)
	}
	return nil
}

// validateDefaultQuota checks the resource limits applied to new tenants.
func validateDefaultQuota(cpuQuota, memoryMax string, tasksMax int) error {
	if !cpuQuotaRe.MatchString(cpuQuota) {
		return fmt.Errorf("invalid default_cpu_quota %q (want e.g. 200%%)", cpuQuota)
	}
	if !memoryMaxRe.MatchString(memoryMax) {
		return fmt.Errorf("invalid default_memory_max %q (want e.g. 4G)", memoryMax)
	}
	if tasksMax < 1 {
		return errors.New("default_tasks_max must be positive")
	}
	return nil
}

func (c *Config) normalizeRetention() {
	if c.Backup.RetentionDays < 1 {
		c.Backup.RetentionDays = 7
	}
	if c.Trash.RetentionDays < 1 {
		c.Trash.RetentionDays = 30
	}
	if c.Audit.RetentionDays < 1 {
		c.Audit.RetentionDays = 180
	}
}

func (c *Config) normalizeSystem() {
	if c.System.HomeRoot == "" {
		c.System.HomeRoot = "/home"
	}
	if c.System.SystemdDir == "" {
		c.System.SystemdDir = "/etc/systemd/system"
	}
	if c.System.UserShell == "" {
		c.System.UserShell = "/usr/sbin/nologin"
	}
}

func (c *Config) normalizeSSH() {
	if strings.TrimSpace(c.SSH.Shell) == "" {
		c.SSH.Shell = "/bin/bash"
	}
	if c.SSH.Port < 0 || c.SSH.Port > 65535 {
		c.SSH.Port = 0
	}
}

func (c Config) DBPath() string      { return filepath.Join(c.DataDir, "fleet.db") }
func (c Config) BackupDir() string   { return filepath.Join(c.DataDir, "backups") }
func (c Config) TrashDir() string    { return filepath.Join(c.DataDir, "trash") }
func (c Config) UploadDir() string   { return filepath.Join(c.DataDir, "uploads") }
func (c Config) LogDir() string      { return filepath.Join(c.DataDir, "logs") }
func (c Config) VersionsDir() string { return filepath.Join(c.Clawbench.InstallDir, "versions") }
func (c Config) CurrentLink() string { return filepath.Join(c.Clawbench.InstallDir, "current") }
func (c Config) InitialPasswordPath() string {
	return filepath.Join(c.DataDir, "initial-password")
}

func (c Config) TenantHome(username string) string {
	return filepath.Join(c.System.HomeRoot, username)
}

func (c Config) TenantDataDir(username string) string {
	return filepath.Join(c.TenantHome(username), ".clawbench")
}

func (c Config) TenantConfigPath(username string) string {
	return filepath.Join(c.TenantDataDir(username), "config", "config.yaml")
}

func (c Config) UnitName(username string) string {
	return "clawbench-" + username + ".service"
}

func (c Config) UnitPath(username string) string {
	return filepath.Join(c.System.SystemdDir, c.UnitName(username))
}

func (c Config) EnsureDirs() error {
	private := []string{c.DataDir, c.BackupDir(), c.TrashDir(), c.UploadDir(), c.LogDir()}
	for _, dir := range private {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("create %s: %w", dir, err)
		}
		_ = os.Chmod(dir, 0o700) //nolint:gosec // 0700 is the correct mode for a data directory
	}
	public := []string{c.Clawbench.InstallDir, c.VersionsDir(), c.System.SystemdDir}
	for _, dir := range public {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create %s: %w", dir, err)
		}
	}
	return nil
}

// DetectPublicHost returns the first non-loopback IPv4 address, used to build
// the address shown to employees when public_host is not configured.
func DetectPublicHost() string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return "127.0.0.1"
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip == nil || ip.IsLoopback() {
				continue
			}
			if v4 := ip.To4(); v4 != nil {
				return v4.String()
			}
		}
	}
	return "127.0.0.1"
}
