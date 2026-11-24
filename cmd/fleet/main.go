package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"clawbench-fleet/internal/config"
	"clawbench-fleet/internal/fleet"
	"clawbench-fleet/internal/httpapi"
	"clawbench-fleet/internal/store"
	"clawbench-fleet/internal/sysops"
	"clawbench-fleet/internal/version"
)

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "--version" || os.Args[1] == "-v") {
		fmt.Println(version.Version)
		return
	}
	if err := run(); err != nil {
		log.Fatalf("%v", err)
	}
}

func run() error {
	var (
		configPath = flag.String("config", "/etc/clawbench-fleet/config.yaml", "platform config file")
		devMode    = flag.Bool("dev", false, "run with an in-memory system backend (no root, no systemd)")
		dataDir    = flag.String("data-dir", "", "override data directory")
		listen     = flag.String("listen", "", "override listen address, e.g. 0.0.0.0:30000")
	)
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	if *dataDir != "" {
		cfg.DataDir = *dataDir
	}
	if *devMode {
		cfg.Dev = true
	}
	b, err := bootstrap(cfg, *devMode, *listen)
	if err != nil {
		return err
	}
	st, sys, svc := b.store, b.system, b.service
	// Remember the config file this process was started with: the systemd unit
	// written by "hand the platform to systemd" has to name the same one.
	svc.ConfigPath = *configPath
	defer func() { _ = st.Close() }()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	svc.Reconcile(ctx)
	svc.Run(ctx)

	// Instances are served a self-signed certificate by default. Creating it at
	// startup means a restarted server never hands out an instance TLS config
	// that points at nothing.
	if _, certErr := svc.EnsureCertificate(); certErr != nil {
		log.Printf("warning: certificate: %v", certErr)
	}

	api := httpapi.New(svc, cfg)
	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           api.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	certFile, keyFile, tlsErr := findTLS(cfg.TLS.CertDir)
	if tlsErr != nil {
		return fmt.Errorf("tls: %w", tlsErr)
	}

	fmt.Printf("Clawbench Fleet %s\n", version.Version)
	fmt.Printf("  mode:   %s\n", sys.Mode())
	fmt.Printf("  listen: %s\n", cfg.Listen)
	fmt.Printf("  data:   %s\n", cfg.DataDir)
	if b.created {
		if cfg.Dev {
			fmt.Printf("  admin:  admin / %s\n", b.initialPassword)
		} else {
			fmt.Printf("  admin:  admin（初始密码已写入 %s）\n", cfg.InitialPasswordPath())
		}
	} else {
		fmt.Printf("  admin:  use the password from %s\n", cfg.InitialPasswordPath())
	}

	errCh := make(chan error, 1)
	go func() {
		if certFile != "" {
			errCh <- srv.ListenAndServeTLS(certFile, keyFile)
			return
		}
		errCh <- srv.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	case serveErr := <-errCh:
		if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			return fmt.Errorf("http: %w", serveErr)
		}
	}
	return nil
}

// boot holds everything main needs from the bootstrap step.
type boot struct {
	store           *store.Store
	system          sysops.System
	service         *fleet.Service
	initialPassword string
	created         bool
}

// bootstrap finishes the configuration, opens the database and builds the
// service. The caller owns the returned store and must close it.
func bootstrap(cfg config.Config, devMode bool, listen string) (*boot, error) {
	if devMode {
		cfg.Dev = true
	}
	if err := applyOverrides(&cfg, devMode, listen); err != nil {
		return nil, err
	}
	if normErr := cfg.Normalize(); normErr != nil {
		return nil, fmt.Errorf("config: %w", normErr)
	}
	if dirErr := cfg.EnsureDirs(); dirErr != nil {
		return nil, fmt.Errorf("directories: %w", dirErr)
	}
	if !cfg.Dev && os.Geteuid() != 0 {
		log.Printf("warning: not running as root; useradd/systemctl will fail")
	}
	st, err := store.Open(cfg.DBPath())
	if err != nil {
		return nil, fmt.Errorf("database: %w", err)
	}
	sys := newSystem(cfg)
	svc, err := fleet.New(cfg, st, sys)
	if err != nil {
		_ = st.Close()
		return nil, fmt.Errorf("service: %w", err)
	}
	b := &boot{store: st, system: sys, service: svc}
	fixedAdminPassword := ""
	if cfg.Dev {
		fixedAdminPassword = "admin123"
	}
	b.initialPassword, b.created, err = fleet.BootstrapAdmin(cfg.InitialPasswordPath(), st, fixedAdminPassword)
	if err != nil {
		_ = st.Close()
		return nil, fmt.Errorf("admin bootstrap: %w", err)
	}
	return b, nil
}

// newSystem picks the simulated backend for --dev and the real one otherwise.
func newSystem(cfg config.Config) sysops.System {
	if cfg.Dev {
		return sysops.NewFake(filepath.Join(cfg.DataDir, "dev"), cfg.Clawbench.InstallDir)
	}
	return sysops.NewReal(cfg.System.SystemdDir, cfg.System.UserShell, cfg.System.HomeRoot)
}

// applyOverrides puts the --dev sandbox and the --listen override on top of
// the loaded configuration, then makes the data directory absolute.
func applyOverrides(cfg *config.Config, devMode bool, listen string) error {
	if devMode {
		if cfg.DataDir == "" || cfg.DataDir == "/var/lib/clawbench-fleet" {
			cfg.DataDir = ".fleet-dev"
		}
		abs, absErr := filepath.Abs(cfg.DataDir)
		if absErr != nil {
			return fmt.Errorf("data-dir: %w", absErr)
		}
		cfg.DataDir = abs
		cfg.Listen = "127.0.0.1:30000"
		cfg.System.HomeRoot = filepath.Join(cfg.DataDir, "dev", "homes")
		cfg.System.SystemdDir = filepath.Join(cfg.DataDir, "dev", "units")
		cfg.Clawbench.InstallDir = filepath.Join(cfg.DataDir, "dev", "opt")
		cfg.Backup.Enabled = false
	}
	if listen != "" {
		cfg.Listen = listen
	}
	return nil
}

func findTLS(dir string) (string, string, error) {
	if dir == "" {
		return "", "", nil
	}
	pairs := [][2]string{
		{"tls.crt", "tls.key"},
		{"cert.pem", "key.pem"},
		{"fullchain.pem", "privkey.pem"},
	}
	for _, p := range pairs {
		cert := filepath.Join(dir, p[0])
		key := filepath.Join(dir, p[1])
		if fileExists(cert) && fileExists(key) {
			return cert, key, nil
		}
	}
	return "", "", fmt.Errorf("证书目录 %s 里没有找到 tls.crt/tls.key（或 cert.pem/key.pem）", dir)
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
