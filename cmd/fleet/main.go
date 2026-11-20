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
	var (
		configPath = flag.String("config", "/etc/clawbench-fleet/config.yaml", "platform config file")
		devMode    = flag.Bool("dev", false, "run with an in-memory system backend (no root, no systemd)")
		dataDir    = flag.String("data-dir", "", "override data directory")
		listen     = flag.String("listen", "", "override listen address, e.g. 0.0.0.0:30000")
	)
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	if *dataDir != "" {
		cfg.DataDir = *dataDir
	}
	if *devMode {
		cfg.Dev = true
		if cfg.DataDir == "" || cfg.DataDir == "/var/lib/clawbench-fleet" {
			cfg.DataDir = ".fleet-dev"
		}
		abs, err := filepath.Abs(cfg.DataDir)
		if err != nil {
			log.Fatalf("data-dir: %v", err)
		}
		cfg.DataDir = abs
		cfg.Listen = "127.0.0.1:30000"
		cfg.System.HomeRoot = filepath.Join(cfg.DataDir, "dev", "homes")
		cfg.System.SystemdDir = filepath.Join(cfg.DataDir, "dev", "units")
		cfg.Clawbench.InstallDir = filepath.Join(cfg.DataDir, "dev", "opt")
		cfg.Backup.Enabled = false
	}
	if *listen != "" {
		cfg.Listen = *listen
	}
	if err := cfg.Normalize(); err != nil {
		log.Fatalf("config: %v", err)
	}
	if err := cfg.EnsureDirs(); err != nil {
		log.Fatalf("directories: %v", err)
	}
	if !cfg.Dev && os.Geteuid() != 0 {
		log.Printf("warning: not running as root; useradd/systemctl will fail")
	}

	st, err := store.Open(cfg.DBPath())
	if err != nil {
		log.Fatalf("database: %v", err)
	}
	defer st.Close()

	var sys sysops.System
	if cfg.Dev {
		sys = sysops.NewFake(filepath.Join(cfg.DataDir, "dev"), cfg.Clawbench.InstallDir)
	} else {
		sys = sysops.NewReal(cfg.System.SystemdDir, cfg.System.UserShell, cfg.System.HomeRoot)
	}
	svc, err := fleet.New(cfg, st, sys)
	if err != nil {
		log.Fatalf("service: %v", err)
	}

	fixedAdminPassword := ""
	if cfg.Dev {
		fixedAdminPassword = "admin123"
	}
	initialPassword, created, err := fleet.BootstrapAdmin(cfg.InitialPasswordPath(), st, fixedAdminPassword)
	if err != nil {
		log.Fatalf("admin bootstrap: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	svc.Reconcile(ctx)
	svc.Run(ctx)

	api := httpapi.New(svc, cfg)
	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           api.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	certFile, keyFile, tlsErr := findTLS(cfg.TLS.CertDir)
	if tlsErr != nil {
		log.Fatalf("tls: %v", tlsErr)
	}

	fmt.Printf("Clawbench Fleet %s\n", version.Version)
	fmt.Printf("  mode:   %s\n", sys.Mode())
	fmt.Printf("  listen: %s\n", cfg.Listen)
	fmt.Printf("  data:   %s\n", cfg.DataDir)
	if created {
		if cfg.Dev {
			fmt.Printf("  admin:  admin / %s\n", initialPassword)
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
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("http: %v", err)
		}
	}
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
