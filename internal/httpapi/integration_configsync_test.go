//go:build integration

package httpapi_test

import (
	"net/http"
	"testing"

	"clawbench-fleet/internal/domain"
	"clawbench-fleet/internal/fleet"
)

// tenantStatus reads one tenant's current status straight from the API.
func (p *platform) tenantStatus(id int64) string {
	p.t.Helper()
	var got struct {
		Tenant struct {
			Status string `json:"status"`
		} `json:"tenant"`
	}
	p.expect(http.MethodGet, "/api/tenants/"+idString(id), nil, http.StatusOK, &got)
	return got.Tenant.Status
}

// TestTenantConfigRoundTrip saves the unified config and reads it back. The
// push itself is a separate step, so saving must not touch any instance.
func TestTenantConfigRoundTrip(t *testing.T) {
	p := start(t)
	p.login()

	entries := []domain.ConfigEntry{
		{Key: "chat.page_size", Value: "25"},
		{Key: "chat.retention_days", Value: "30"},
	}
	var saved struct {
		TenantConfig []domain.ConfigEntry `json:"tenant_config"`
	}
	p.expect(http.MethodPut, "/api/settings/tenant-config", map[string]any{
		"entries": entries,
	}, http.StatusOK, &saved)
	if len(saved.TenantConfig) != len(entries) {
		t.Fatalf("saved tenant config = %+v, want %d entries", saved.TenantConfig, len(entries))
	}

	var reread struct {
		Settings     domain.Settings      `json:"settings"`
		TenantConfig []domain.ConfigEntry `json:"tenant_config"`
	}
	p.expect(http.MethodGet, "/api/settings", nil, http.StatusOK, &reread)
	if len(reread.TenantConfig) != len(entries) {
		t.Fatalf("reread tenant config = %+v, want %d entries", reread.TenantConfig, len(entries))
	}
	for i, want := range entries {
		if reread.TenantConfig[i] != want {
			t.Fatalf("reread entry %d = %+v, want %+v", i, reread.TenantConfig[i], want)
		}
	}
}

// TestTenantConfigRejectsBadInput covers the validation branches the settings
// page relies on: an empty key, a platform-owned key, a duplicate and a value
// carrying a newline (which would corrupt the generated YAML).
func TestTenantConfigRejectsBadInput(t *testing.T) {
	p := start(t)
	p.login()

	cases := []struct {
		name    string
		entries []domain.ConfigEntry
	}{
		{"empty key", []domain.ConfigEntry{{Key: "", Value: "x"}}},
		{"reserved key", []domain.ConfigEntry{{Key: "port", Value: "21000"}}},
		{"duplicate key", []domain.ConfigEntry{{Key: "chat.page_size", Value: "10"}, {Key: "chat.page_size", Value: "20"}}},
		{"newline in value", []domain.ConfigEntry{{Key: "chat.page_size", Value: "10\n11"}}},
		{"uppercase key", []domain.ConfigEntry{{Key: "Chat.PageSize", Value: "10"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, body := p.do(http.MethodPut, "/api/settings/tenant-config", map[string]any{"entries": tc.entries})
			if status != http.StatusBadRequest {
				t.Fatalf("%s = %d, want 400: %s", tc.name, status, body)
			}
		})
	}
}

// TestApplyConfigRequiresInstance makes sure a push with nothing to push to is
// reported as an error instead of silently creating an empty job.
func TestApplyConfigRequiresInstance(t *testing.T) {
	p := start(t)
	p.login()

	status, body := p.do(http.MethodPost, "/api/settings/apply-config", nil)
	if status != http.StatusBadRequest && status != http.StatusConflict && status != http.StatusUnprocessableEntity {
		t.Fatalf("apply config with no instance = %d, want a client error: %s", status, body)
	}
}

// TestApplyConfigToTenant pushes the unified config into one running instance
// and expects it to come back healthy.
func TestApplyConfigToTenant(t *testing.T) {
	p := start(t)
	p.login()

	p.expect(http.MethodPut, "/api/settings/tenant-config", map[string]any{
		"entries": []domain.ConfigEntry{{Key: "chat.page_size", Value: "40"}},
	}, http.StatusOK, nil)

	id := p.createTenant("cfgapply")
	p.waitStatus(id, domain.StatusRunning)

	var applied struct {
		Tenant domain.Tenant `json:"tenant"`
	}
	p.expect(http.MethodPost, "/api/tenants/"+idString(id)+"/apply-config", nil, http.StatusOK, &applied)
	if applied.Tenant.ID != id {
		t.Fatalf("apply config returned tenant %d, want %d", applied.Tenant.ID, id)
	}
	if status := p.tenantStatus(id); status != domain.StatusRunning {
		t.Fatalf("tenant status after config push = %q, want %q", status, domain.StatusRunning)
	}
}

// TestApplyConfigToMissingTenant is the 404 path the detail page hits when a
// tenant is deleted in another tab.
func TestApplyConfigToMissingTenant(t *testing.T) {
	p := start(t)
	p.login()
	status, body := p.do(http.MethodPost, "/api/tenants/999999/apply-config", nil)
	if status != http.StatusNotFound {
		t.Fatalf("apply config to missing tenant = %d, want 404: %s", status, body)
	}
}

// TestCertInfoAndRegenerate covers the certificate panel: the info endpoint
// answers even when no certificate exists yet, and regenerating one produces a
// usable self-signed pair.
func TestCertInfoAndRegenerate(t *testing.T) {
	p := start(t)
	p.login()

	// The shared platform in tests has TLS switched off, so turn it on first;
	// otherwise regenerating is a documented no-op.
	settings := p.core.Svc.Settings()
	settings.TLSEnabled = true
	if err := p.core.Svc.UpdateSettings(settings); err != nil {
		t.Fatalf("enable TLS: %v", err)
	}

	var before struct {
		Certificate fleet.CertInfo `json:"certificate"`
	}
	p.expect(http.MethodGet, "/api/settings/cert", nil, http.StatusOK, &before)
	if !before.Certificate.Enabled {
		t.Fatal("certificate info still reports TLS as disabled")
	}
	if before.Certificate.Dir == "" {
		t.Fatal("certificate info carries no destination directory")
	}

	var regenerated struct {
		Certificate fleet.CertInfo `json:"certificate"`
	}
	p.expect(http.MethodPost, "/api/settings/cert/regenerate", nil, http.StatusOK, &regenerated)
	if !regenerated.Certificate.Exists || !regenerated.Certificate.SelfSigned {
		t.Fatalf("regenerate produced no usable certificate: %+v", regenerated.Certificate)
	}
	if regenerated.Certificate.NotAfter == "" {
		t.Fatalf("regenerated certificate has no expiry: %+v", regenerated.Certificate)
	}
}

// TestRefreshDiskAndMetricsHistory walks the two endpoints the tenant detail
// page polls: an on-demand disk refresh and the usage history window.
func TestRefreshDiskAndMetricsHistory(t *testing.T) {
	p := start(t)
	p.login()

	id := p.createTenant("diskmetrics")
	p.waitStatus(id, domain.StatusRunning)

	var refreshed struct {
		Tenant domain.Tenant `json:"tenant"`
	}
	p.expect(http.MethodPost, "/api/tenants/"+idString(id)+"/disk/refresh", nil, http.StatusOK, &refreshed)
	if refreshed.Tenant.ID != id {
		t.Fatalf("refresh disk returned tenant %d, want %d", refreshed.Tenant.ID, id)
	}

	var history struct {
		Items  []domain.Metrics `json:"items"`
		Window string           `json:"window"`
	}
	p.expect(http.MethodGet, "/api/tenants/"+idString(id)+"/metrics/history?window=24h", nil, http.StatusOK, &history)
	if history.Window != "24h0m0s" {
		t.Fatalf("history window = %q, want 24h0m0s", history.Window)
	}

	status, body := p.do(http.MethodGet, "/api/tenants/"+idString(id)+"/metrics/history?window=banana", nil)
	if status != http.StatusBadRequest {
		t.Fatalf("history with a bad window = %d, want 400: %s", status, body)
	}
}
