package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultNormalizes(t *testing.T) {
	cfg := Default()
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("default config must be valid: %v", err)
	}
	if !filepath.IsAbs(cfg.DataDir) {
		t.Fatalf("data dir should be absolute, got %q", cfg.DataDir)
	}
	if cfg.Clawbench.NpmPackage != "@xulongzhe/clawbench" {
		t.Fatalf("npm package = %q", cfg.Clawbench.NpmPackage)
	}
}

func TestLoadMissingFileUsesDefaults(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "absent.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Listen != Default().Listen {
		t.Fatalf("listen = %q", cfg.Listen)
	}
}

func TestLoadOverridesDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	body := "listen: 127.0.0.1:39999\ndata_dir: /tmp/fleet-test-data\nbackup:\n  hour: 5\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Listen != "127.0.0.1:39999" || cfg.Backup.Hour != 5 {
		t.Fatalf("overrides not applied: %+v", cfg)
	}
	// Values not present in the file keep their defaults.
	if cfg.Clawbench.PortStart != Default().Clawbench.PortStart {
		t.Fatalf("port start = %d, want default", cfg.Clawbench.PortStart)
	}
}

func TestLoadBrokenYAML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("listen: [unclosed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("broken yaml should fail")
	}
}

func TestNormalizeRejectsBadValues(t *testing.T) {
	cases := map[string]func(*Config){
		"empty listen":        func(c *Config) { c.Listen = " " },
		"listen without port": func(c *Config) { c.Listen = "localhost" },
		"empty install dir":   func(c *Config) { c.Clawbench.InstallDir = "" },
		"port range":          func(c *Config) { c.Clawbench.PortStart, c.Clawbench.PortEnd = 30000, 20000 },
		"ports per tenant":    func(c *Config) { c.Clawbench.PortsPerTenant = 0 },
		"uid range":           func(c *Config) { c.Clawbench.UIDStart = 10 },
		"cpu quota":           func(c *Config) { c.Clawbench.DefaultCPUQuota = "lots" },
		"memory max":          func(c *Config) { c.Clawbench.DefaultMemoryMax = "many" },
		"tasks max":           func(c *Config) { c.Clawbench.DefaultTasksMax = 0 },
		"npm package":         func(c *Config) { c.Clawbench.NpmPackage = "clawbench" },
		"backup hour":         func(c *Config) { c.Backup.Hour = 24 },
	}
	for name, mutate := range cases {
		cfg := Default()
		mutate(&cfg)
		if err := cfg.Normalize(); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestNormalizeFillsDefaults(t *testing.T) {
	cfg := Default()
	cfg.Clawbench.NpmPackage = ""
	cfg.Backup.RetentionDays = 0
	cfg.Trash.RetentionDays = 0
	cfg.Audit.RetentionDays = 0
	cfg.System = SystemConfig{}
	if err := cfg.Normalize(); err != nil {
		t.Fatal(err)
	}
	if cfg.Clawbench.NpmPackage != "@xulongzhe/clawbench" {
		t.Fatalf("npm package = %q", cfg.Clawbench.NpmPackage)
	}
	if cfg.Backup.RetentionDays != 7 || cfg.Trash.RetentionDays != 30 || cfg.Audit.RetentionDays != 180 {
		t.Fatalf("retention defaults: %+v", cfg)
	}
	if cfg.System.HomeRoot != "/home" || cfg.System.SystemdDir != "/etc/systemd/system" || cfg.System.UserShell != "/usr/sbin/nologin" {
		t.Fatalf("system defaults: %+v", cfg.System)
	}
}

func TestPathHelpers(t *testing.T) {
	cfg := Default()
	cfg.DataDir = "/var/lib/fleet"
	cfg.Clawbench.InstallDir = "/opt/fleet"
	cfg.System.HomeRoot = "/srv/home"
	checks := map[string][2]string{
		"db":          {cfg.DBPath(), "/var/lib/fleet/fleet.db"},
		"backup":      {cfg.BackupDir(), "/var/lib/fleet/backups"},
		"trash":       {cfg.TrashDir(), "/var/lib/fleet/trash"},
		"upload":      {cfg.UploadDir(), "/var/lib/fleet/uploads"},
		"log":         {cfg.LogDir(), "/var/lib/fleet/logs"},
		"versions":    {cfg.VersionsDir(), "/opt/fleet/versions"},
		"current":     {cfg.CurrentLink(), "/opt/fleet/current"},
		"password":    {cfg.InitialPasswordPath(), "/var/lib/fleet/initial-password"},
		"tenant home": {cfg.TenantHome("alice"), "/srv/home/alice"},
		"tenant data": {cfg.TenantDataDir("alice"), "/srv/home/alice/.clawbench"},
		"tenant cfg":  {cfg.TenantConfigPath("alice"), "/srv/home/alice/.clawbench/config/config.yaml"},
		"unit name":   {cfg.UnitName("alice"), "clawbench-alice.service"},
		"unit path":   {cfg.UnitPath("alice"), "/etc/systemd/system/clawbench-alice.service"},
	}
	for name, pair := range checks {
		if pair[0] != pair[1] {
			t.Errorf("%s = %q, want %q", name, pair[0], pair[1])
		}
	}
}

func TestEnsureDirs(t *testing.T) {
	root := t.TempDir()
	cfg := Default()
	cfg.DataDir = filepath.Join(root, "data")
	cfg.Clawbench.InstallDir = filepath.Join(root, "opt")
	cfg.System.SystemdDir = filepath.Join(root, "units")
	if err := cfg.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{cfg.DataDir, cfg.BackupDir(), cfg.TrashDir(), cfg.UploadDir(), cfg.LogDir(), cfg.VersionsDir(), cfg.System.SystemdDir} {
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			t.Errorf("%s was not created: %v", dir, err)
		}
	}
	// Data directories must not be world-readable.
	info, err := os.Stat(cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		t.Errorf("data dir permissions = %v, want private", perm)
	}
}

func TestDetectPublicHost(t *testing.T) {
	host := DetectPublicHost()
	if host == "" {
		t.Fatal("host must not be empty")
	}
	if strings.Contains(host, "/") {
		t.Fatalf("host looks like a path: %q", host)
	}
}
