package httpapi

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"clawbench-fleet/internal/fleet"
)

func TestUpgradeUploadAndJobs(t *testing.T) {
	c, platform := newTestClient(t)
	var list struct {
		Versions []map[string]any `json:"versions"`
	}
	if status := c.json(http.MethodGet, "/api/upgrades", nil, &list); status != http.StatusOK || len(list.Versions) != 0 {
		t.Fatalf("initial upgrades = %d %+v", status, list)
	}

	// --dev accepts any executable and takes the version from the file name.
	upload := func(name string) (int, []byte) {
		return c.upload("/api/upgrades/upload", "file", name, []byte("#!/bin/sh\nexit 0\n"))
	}
	if status, body := upload("9.8.7"); status != http.StatusCreated {
		t.Fatalf("upload = %d %s", status, body)
	}
	if status, _ := upload("9.8.7"); status != http.StatusBadRequest {
		t.Fatalf("duplicate upload = %d", status)
	}
	if status, _ := c.upload("/api/upgrades/upload", "file", "empty", nil); status != http.StatusBadRequest {
		t.Fatalf("empty upload = %d", status)
	}
	if status, _ := c.upload("/api/upgrades/upload", "missing", "x", nil); status != http.StatusBadRequest {
		t.Fatalf("missing field = %d", status)
	}

	var npm struct {
		Package      string `json:"package"`
		Registry     string `json:"registry"`
		RegistryFrom string `json:"registry_from"`
		Versions     []struct {
			Version   string `json:"version"`
			Installed bool   `json:"installed"`
		} `json:"versions"`
	}
	if status := c.json(http.MethodGet, "/api/upgrades/npm", nil, &npm); status != http.StatusOK || len(npm.Versions) != 3 {
		t.Fatalf("npm catalog = %d %+v", status, npm)
	}
	if status := c.json(http.MethodPost, "/api/upgrades/npm/0.142.0/install", nil, nil); status != http.StatusCreated {
		t.Fatalf("npm install = %d", status)
	}
	if status := c.json(http.MethodPost, "/api/upgrades/npm/not a version/install", nil, nil); status != http.StatusBadRequest {
		t.Fatalf("bad npm version = %d", status)
	}

	created := createTenant(t, c, "alice")
	waitForStatus(t, c, tenantID(t, created))

	if status := c.json(http.MethodPost, "/api/upgrades/0.142.0/pilot", map[string]any{}, nil); status != http.StatusBadRequest {
		t.Fatalf("pilot without tenant = %d", status)
	}
	var pilot struct {
		Tenant map[string]any `json:"tenant"`
	}
	if status := c.json(http.MethodPost, "/api/upgrades/0.142.0/pilot", map[string]any{"tenant_id": tenantID(t, created)}, &pilot); status != http.StatusOK {
		t.Fatalf("pilot = %d %+v", status, pilot)
	}
	if pilot.Tenant["version"] != "0.142.0" {
		t.Fatalf("pilot version = %v", pilot.Tenant["version"])
	}

	var job struct {
		Job map[string]any `json:"job"`
	}
	if status := c.json(http.MethodPost, "/api/upgrades/9.8.7/apply", nil, &job); status != http.StatusAccepted {
		t.Fatalf("apply = %d", status)
	}
	if status := c.json(http.MethodPost, "/api/upgrades/rollback", nil, nil); status != http.StatusAccepted && status != http.StatusBadRequest {
		t.Fatalf("rollback = %d", status)
	}
	if status := c.json(http.MethodPost, "/api/upgrades/9.9.9/apply", nil, nil); status != http.StatusBadRequest {
		t.Fatalf("apply unknown version = %d", status)
	}

	installed, err := platform.Svc.ListVersions()
	if err != nil || len(installed) < 2 {
		t.Fatalf("installed versions = %+v %v", installed, err)
	}
	if _, err := os.Stat(filepath.Join(platform.Cfg.Clawbench.InstallDir, "current")); err != nil {
		t.Fatalf("current symlink missing: %v", err)
	}
}

func TestBackupDownloadUnknown(t *testing.T) {
	c, _ := newTestClient(t)
	if status := c.json(http.MethodGet, "/api/backups/404/download", nil, nil); status != http.StatusNotFound {
		t.Fatalf("unknown backup download = %d", status)
	}
}

func TestJobArtifactRequiresImport(t *testing.T) {
	c, _ := newTestClient(t)
	if status := c.json(http.MethodGet, "/api/jobs/404/artifacts/success.csv", nil, nil); status != http.StatusNotFound {
		t.Fatalf("missing artifact = %d", status)
	}
}

func TestSettingsReportVersion(t *testing.T) {
	c, platform := newTestClient(t)
	if _, err := platform.Svc.InstallFromNpm(t.Context(), "0.143.0", fleet.AuditContext{Actor: "tester"}); err != nil {
		t.Fatal(err)
	}
	var got struct {
		CurrentVersion string `json:"current_version"`
		InstallDir     string `json:"install_dir"`
	}
	if status := c.json(http.MethodGet, "/api/settings", nil, &got); status != http.StatusOK {
		t.Fatalf("settings = %d", status)
	}
	if got.CurrentVersion != "0.143.0" || got.InstallDir == "" {
		t.Fatalf("settings payload = %+v", got)
	}
}

// TestSelfUpgradeEndpoints covers the platform-upgrade entry points. --dev has
// no installed binary to swap, so the page must be told it is unsupported and
// the upload must fail loudly instead of pretending the platform changed.
func TestSelfUpgradeEndpoints(t *testing.T) {
	c, _ := newTestClient(t)
	var info struct {
		Version   string `json:"version"`
		Binary    string `json:"binary"`
		Supported bool   `json:"supported"`
	}
	if status := c.json(http.MethodGet, "/api/upgrades/self", nil, &info); status != http.StatusOK {
		t.Fatalf("info = %d", status)
	}
	if info.Supported || info.Version == "" || info.Binary == "" {
		t.Fatalf("info = %+v", info)
	}
	status, body := c.upload("/api/upgrades/self", "file", "clawbench-fleet", []byte("#!/bin/sh\nexit 0\n"))
	if status != http.StatusBadRequest {
		t.Fatalf("dev upload = %d %s", status, body)
	}
}
