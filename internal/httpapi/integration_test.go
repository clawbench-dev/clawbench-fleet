//go:build integration

// Package httpapi integration tests drive the full platform over real HTTP:
// the handler stack, the service layer, SQLite on disk and the simulated
// system backend. Nothing is mocked at the HTTP boundary, so a change that
// breaks routing, cookies, JSON shapes or the tenant lifecycle fails here.
package httpapi_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"clawbench-fleet/internal/domain"
	"clawbench-fleet/internal/httpapi"
	"clawbench-fleet/internal/testutil"

	_ "modernc.org/sqlite"
)

const adminPassword = testutil.AdminLogin

type platform struct {
	t    *testing.T
	core *testutil.Platform
	srv  *httptest.Server
	http *http.Client
}

func start(t *testing.T) *platform {
	t.Helper()
	core := testutil.New(t)
	core.BootstrapAdmin(t)
	srv := httptest.NewServer(httpapi.New(core.Svc, core.Cfg).Handler())
	t.Cleanup(srv.Close)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &platform{t: t, core: core, srv: srv, http: &http.Client{Jar: jar, Timeout: 30 * time.Second}}
}

func (p *platform) do(method, path string, body any) (int, []byte) {
	p.t.Helper()
	var reader io.Reader
	if body != nil {
		switch v := body.(type) {
		case []byte:
			reader = bytes.NewReader(v)
		default:
			data, err := json.Marshal(v)
			if err != nil {
				p.t.Fatal(err)
			}
			reader = bytes.NewReader(data)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, p.srv.URL+path, reader)
	if err != nil {
		p.t.Fatal(err)
	}
	req.Header.Set("Origin", p.srv.URL)
	if _, ok := body.([]byte); !ok && body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := p.http.Do(req)
	if err != nil {
		p.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		p.t.Fatal(err)
	}
	return resp.StatusCode, data
}

func (p *platform) expect(method, path string, body any, want int, out any) {
	p.t.Helper()
	status, data := p.do(method, path, body)
	if status != want {
		p.t.Fatalf("%s %s = %d, want %d: %s", method, path, status, want, data)
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			p.t.Fatalf("%s %s: decode %s: %v", method, path, data, err)
		}
	}
}

func (p *platform) login() {
	p.t.Helper()
	p.expect(http.MethodPost, "/api/auth/login", map[string]string{"username": "admin", "password": adminPassword}, http.StatusOK, nil)
}

func (p *platform) multipart(path, field, filename string, content []byte) (int, []byte) {
	p.t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, err := mw.CreateFormFile(field, filename)
	if err != nil {
		p.t.Fatal(err)
	}
	if _, err := part.Write(content); err != nil {
		p.t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		p.t.Fatal(err)
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, p.srv.URL+path, &buf)
	if err != nil {
		p.t.Fatal(err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Origin", p.srv.URL)
	resp, err := p.http.Do(req)
	if err != nil {
		p.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, data
}

func (p *platform) createTenant(username string) int64 {
	p.t.Helper()
	var created struct {
		Tenant   map[string]any `json:"tenant"`
		Password string         `json:"password"`
	}
	p.expect(http.MethodPost, "/api/tenants", map[string]any{"username": username}, http.StatusCreated, &created)
	if created.Password == "" {
		p.t.Fatal("create should return a password")
	}
	return int64(created.Tenant["id"].(float64))
}

func (p *platform) waitStatus(id int64, want string) {
	p.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		var got struct {
			Tenant struct {
				Status string `json:"status"`
			} `json:"tenant"`
		}
		if status, _ := p.do(http.MethodGet, "/api/tenants/"+idString(id), nil); status == http.StatusOK {
			_ = json.Unmarshal(mustBody(p, http.MethodGet, "/api/tenants/"+idString(id)), &got)
			if got.Tenant.Status == want {
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	p.t.Fatalf("tenant %d never reached %s", id, want)
}

func mustBody(p *platform, method, path string) []byte {
	_, data := p.do(method, path, nil)
	return data
}

func idString(id int64) string { return fmt.Sprintf("%d", id) }

func TestIntegrationUnauthenticated(t *testing.T) {
	p := start(t)
	for _, path := range []string{"/api/tenants", "/api/overview", "/api/upgrades", "/api/audit", "/api/trash"} {
		if status, _ := p.do(http.MethodGet, path, nil); status != http.StatusUnauthorized {
			t.Fatalf("%s without login = %d", path, status)
		}
	}
	if status, _ := p.do(http.MethodGet, "/api/public/instances", nil); status != http.StatusOK {
		t.Fatalf("guest page should be public, got %d", status)
	}
	if status, _ := p.do(http.MethodGet, "/api/health", nil); status != http.StatusOK {
		t.Fatalf("health = %d", status)
	}
}

func TestIntegrationTenantLifecycle(t *testing.T) {
	p := start(t)
	p.login()
	id := p.createTenant("alice")
	p.waitStatus(id, domain.StatusRunning)

	// The instance writes a real config file under the simulated home.
	configPath := p.core.Cfg.TenantConfigPath("alice")
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("tenant config: %v", err)
	}
	if !strings.Contains(string(data), "sha256:") || !strings.Contains(string(data), "port:") {
		t.Fatalf("tenant config = %s", data)
	}

	var list struct {
		Items []struct {
			Username string `json:"username"`
			Address  string `json:"address"`
			Metrics  struct {
				SampledAt string `json:"sampled_at"`
			} `json:"metrics"`
		} `json:"items"`
		Total int `json:"total"`
	}
	p.expect(http.MethodGet, "/api/tenants?page=1&page_size=10", nil, http.StatusOK, &list)
	if list.Total != 1 || list.Items[0].Username != "alice" || !strings.HasPrefix(list.Items[0].Address, "http://") {
		t.Fatalf("list = %+v", list)
	}

	var detail struct {
		Tenant struct {
			Status    string `json:"status"`
			Protected bool   `json:"protected"`
			DiskBytes int64  `json:"disk_bytes"`
		} `json:"tenant"`
	}
	p.expect(http.MethodGet, "/api/tenants/"+idString(id), nil, http.StatusOK, &detail)
	if detail.Tenant.Status != domain.StatusRunning || detail.Tenant.Protected {
		t.Fatalf("detail = %+v", detail.Tenant)
	}

	// Stop, start and restart all leave the instance healthy.
	for _, action := range []string{"stop", "start", "restart"} {
		p.expect(http.MethodPost, "/api/tenants/"+idString(id)+"/"+action, nil, http.StatusOK, nil)
	}
	p.waitStatus(id, domain.StatusRunning)

	// A password reset rewrites the config and the instance keeps running.
	var reset struct {
		Password string `json:"password"`
	}
	p.expect(http.MethodPost, "/api/tenants/"+idString(id)+"/reset-password", nil, http.StatusOK, &reset)
	if reset.Password == "" {
		t.Fatal("reset should return a new password")
	}
	after, err := os.ReadFile(configPath)
	if err != nil || bytes.Equal(data, after) {
		t.Fatalf("config should change after a password reset: %v", err)
	}

	// Logs come from the simulated journal.
	var logs struct {
		Lines string `json:"lines"`
	}
	p.expect(http.MethodGet, "/api/tenants/"+idString(id)+"/logs?lines=50", nil, http.StatusOK, &logs)
	if !strings.Contains(logs.Lines, "started") {
		t.Fatalf("logs = %q", logs.Lines)
	}

	// Delete, inspect the trash, restore.
	p.expect(http.MethodDelete, "/api/tenants/"+idString(id), map[string]string{"confirm": "alice"}, http.StatusOK, nil)
	var trash struct {
		Items []struct {
			Username string `json:"username"`
			DaysLeft int    `json:"days_left"`
		} `json:"items"`
	}
	p.expect(http.MethodGet, "/api/trash", nil, http.StatusOK, &trash)
	if len(trash.Items) != 1 || trash.Items[0].DaysLeft <= 0 {
		t.Fatalf("trash = %+v", trash)
	}
	p.expect(http.MethodPost, "/api/trash/"+idString(id)+"/restore", nil, http.StatusOK, nil)
	p.waitStatus(id, domain.StatusRunning)

	// Purge for good.
	p.expect(http.MethodDelete, "/api/tenants/"+idString(id), map[string]string{"confirm": "alice"}, http.StatusOK, nil)
	p.expect(http.MethodDelete, "/api/trash/"+idString(id), nil, http.StatusOK, nil)
	if status, _ := p.do(http.MethodGet, "/api/tenants/"+idString(id), nil); status != http.StatusNotFound {
		t.Fatalf("purged tenant = %d", status)
	}

	// The audit trail has a record of every step.
	var audit struct {
		Items []struct {
			Action string `json:"action"`
		} `json:"items"`
		Total int `json:"total"`
	}
	p.expect(http.MethodGet, "/api/audit?page=1&page_size=100", nil, http.StatusOK, &audit)
	seen := map[string]bool{}
	for _, item := range audit.Items {
		seen[item.Action] = true
	}
	for _, want := range []string{"tenant.create", "tenant.stop", "tenant.start", "tenant.reset_password", "tenant.delete", "tenant.restore", "tenant.purge", "auth.login"} {
		if !seen[want] {
			t.Errorf("audit is missing %s (have %v)", want, seen)
		}
	}
}

func TestIntegrationBackupRestore(t *testing.T) {
	p := start(t)
	p.login()
	id := p.createTenant("bob")
	p.waitStatus(id, domain.StatusRunning)

	// Give the tenant a database with a value we can check after a restore.
	dataDir := p.core.Cfg.TenantDataDir("bob")
	db, err := sql.Open("sqlite", filepath.Join(dataDir, "ClawBench.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE kv(k TEXT PRIMARY KEY, v TEXT); INSERT INTO kv VALUES('x','before')`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	var backup struct {
		Backup struct {
			ID          int64 `json:"id"`
			IntegrityOK bool  `json:"integrity_ok"`
		} `json:"backup"`
	}
	p.expect(http.MethodPost, "/api/tenants/"+idString(id)+"/backups", nil, http.StatusCreated, &backup)
	if !backup.Backup.IntegrityOK {
		t.Fatal("backup integrity check failed")
	}

	db, err = sql.Open("sqlite", filepath.Join(dataDir, "ClawBench.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE kv SET v='after'`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	p.expect(http.MethodPost, "/api/tenants/"+idString(id)+"/backups/"+idString(backup.Backup.ID)+"/restore", nil, http.StatusOK, nil)

	db, err = sql.Open("sqlite", filepath.Join(dataDir, "ClawBench.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var value string
	if err := db.QueryRow(`SELECT v FROM kv`).Scan(&value); err != nil {
		t.Fatal(err)
	}
	if value != "before" {
		t.Fatalf("restored value = %q", value)
	}

	// The download is a real tar.gz.
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, p.srv.URL+"/api/backups/"+idString(backup.Backup.ID)+"/download", http.NoBody)
	resp, err := p.http.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	head := make([]byte, 2)
	_, _ = io.ReadFull(resp.Body, head)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || head[0] != 0x1f || head[1] != 0x8b {
		t.Fatalf("download = %d %x", resp.StatusCode, head)
	}
}

func TestIntegrationUpgradeFlow(t *testing.T) {
	p := start(t)
	p.login()
	id := p.createTenant("carol")
	p.waitStatus(id, domain.StatusRunning)

	// Install two versions: one from npm, one uploaded.
	p.expect(http.MethodPost, "/api/upgrades/npm/0.141.0/install", nil, http.StatusCreated, nil)
	if status, body := p.multipart("/api/upgrades/upload", "file", "0.140.0", []byte("#!/bin/sh\nexit 0\n")); status != http.StatusCreated {
		t.Fatalf("upload = %d %s", status, body)
	}

	var catalog struct {
		Versions []struct {
			Version   string `json:"version"`
			Installed bool   `json:"installed"`
		} `json:"versions"`
	}
	p.expect(http.MethodGet, "/api/upgrades/npm", nil, http.StatusOK, &catalog)
	installed := 0
	for _, v := range catalog.Versions {
		if v.Installed {
			installed++
		}
	}
	if installed != 1 {
		t.Fatalf("installed count = %d in %+v", installed, catalog.Versions)
	}

	// Pilot moves one tenant onto the new version.
	var pilot struct {
		Tenant struct {
			Version string `json:"version"`
		} `json:"tenant"`
	}
	p.expect(http.MethodPost, "/api/upgrades/0.141.0/pilot", map[string]any{"tenant_id": id}, http.StatusOK, &pilot)
	if pilot.Tenant.Version != "0.141.0" {
		t.Fatalf("pilot version = %q", pilot.Tenant.Version)
	}
	// Applying the upgrade restarts every running tenant and finishes the job.
	var job struct {
		Job struct {
			ID int64 `json:"id"`
		} `json:"job"`
	}
	p.expect(http.MethodPost, "/api/upgrades/0.140.0/apply", nil, http.StatusAccepted, &job)
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		var got struct {
			Job struct {
				Status string `json:"status"`
			} `json:"job"`
		}
		p.expect(http.MethodGet, "/api/jobs/"+idString(job.Job.ID), nil, http.StatusOK, &got)
		if got.Job.Status == domain.JobSucceeded {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	cur, err := p.core.Svc.CurrentVersion()
	if err != nil || cur.Version != "0.140.0" {
		t.Fatalf("current version = %+v %v", cur, err)
	}
	// The symlink systemd would start points at the current version.
	target, err := os.Readlink(p.core.Cfg.CurrentLink())
	if err != nil || !strings.HasSuffix(target, "0.140.0") {
		t.Fatalf("current symlink = %q %v", target, err)
	}
}

func TestIntegrationImportAndGuestReports(t *testing.T) {
	p := start(t)
	p.login()

	csvBody := []byte("\xef\xbb\xbfusername,display_name,notes\ndave,Dave,ops\nerin,Erin,\nroot,,bad\n")
	var preview struct {
		Importable int `json:"importable"`
		Invalid    int `json:"invalid"`
	}
	if status, body := p.multipart("/api/tenants/import/preview", "file", "tenants.csv", csvBody); status != http.StatusOK {
		t.Fatalf("preview = %d %s", status, body)
	} else if err := json.Unmarshal(body, &preview); err != nil {
		t.Fatal(err)
	}
	if preview.Importable != 2 || preview.Invalid != 1 {
		t.Fatalf("preview = %+v", preview)
	}

	var job struct {
		Job struct {
			ID int64 `json:"id"`
		} `json:"job"`
	}
	if status, body := p.multipart("/api/tenants/import", "file", "tenants.csv", csvBody); status != http.StatusAccepted {
		t.Fatalf("import = %d %s", status, body)
	} else if err := json.Unmarshal(body, &job); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		var got struct {
			Job struct {
				Status string `json:"status"`
				Items  []struct {
					Status string `json:"status"`
				} `json:"items"`
			} `json:"job"`
		}
		p.expect(http.MethodGet, "/api/jobs/"+idString(job.Job.ID), nil, http.StatusOK, &got)
		if got.Job.Status == domain.JobPartial {
			if len(got.Job.Items) != 3 {
				t.Fatalf("job items = %+v", got.Job.Items)
			}
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	// Employees can report a broken instance from the public page.
	var page struct {
		Items []struct {
			ID int64 `json:"id"`
		} `json:"items"`
	}
	p.expect(http.MethodGet, "/api/public/instances", nil, http.StatusOK, &page)
	if len(page.Items) != 2 {
		t.Fatalf("guest page items = %d", len(page.Items))
	}
	p.expect(http.MethodPost, "/api/public/reports", map[string]any{"tenant_id": page.Items[0].ID, "message": "打不开"}, http.StatusCreated, nil)
	var reports struct {
		Items []struct {
			ID int64 `json:"id"`
		} `json:"items"`
	}
	p.expect(http.MethodGet, "/api/reports", nil, http.StatusOK, &reports)
	if len(reports.Items) != 1 {
		t.Fatalf("reports = %+v", reports)
	}
	p.expect(http.MethodPost, "/api/reports/"+idString(reports.Items[0].ID)+"/resolve", nil, http.StatusOK, nil)
}

func TestIntegrationOverviewAndSettings(t *testing.T) {
	p := start(t)
	p.login()
	id := p.createTenant("frank")
	p.waitStatus(id, domain.StatusRunning)

	var overview struct {
		Total          int    `json:"total"`
		Mode           string `json:"mode"`
		CurrentVersion string `json:"current_version"`
		Host           struct {
			MemTotal  int64 `json:"mem_total"`
			DiskTotal int64 `json:"disk_total"`
		} `json:"host"`
	}
	p.expect(http.MethodGet, "/api/overview", nil, http.StatusOK, &overview)
	if overview.Total != 1 || overview.Mode != "dev" || overview.Host.DiskTotal == 0 {
		t.Fatalf("overview = %+v", overview)
	}

	var settings struct {
		Settings domain.Settings `json:"settings"`
	}
	p.expect(http.MethodGet, "/api/settings", nil, http.StatusOK, &settings)
	settings.Settings.GuestNotice = "维护中"
	p.expect(http.MethodPatch, "/api/settings", settings.Settings, http.StatusOK, nil)
	p.expect(http.MethodGet, "/api/settings", nil, http.StatusOK, &settings)
	if settings.Settings.GuestNotice != "维护中" {
		t.Fatalf("settings not saved: %+v", settings.Settings)
	}
	settings.Settings.TrashRetention = 0
	p.expect(http.MethodPatch, "/api/settings", settings.Settings, http.StatusBadRequest, nil)
}
