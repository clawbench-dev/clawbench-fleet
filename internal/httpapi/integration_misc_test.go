//go:build integration

package httpapi_test

import (
	"net/http"
	"testing"

	"clawbench-fleet/internal/domain"
)

// TestAuditFiltersMatchWhatTheOperatorTyped covers the filter bar: each query
// parameter has to narrow the list instead of being ignored.
func TestAuditFiltersMatchWhatTheOperatorTyped(t *testing.T) {
	p := start(t)
	p.login()
	alice := p.createTenant("auditalice")
	p.waitStatus(alice, domain.StatusRunning)
	p.expect(http.MethodPost, "/api/tenants/"+idString(alice)+"/stop", nil, http.StatusOK, nil)

	var all struct {
		Items []domain.AuditEntry `json:"items"`
		Total int                 `json:"total"`
	}
	p.expect(http.MethodGet, "/api/audit?page_size=200", nil, http.StatusOK, &all)
	if all.Total == 0 {
		t.Fatal("expected audit entries after creating and stopping a tenant")
	}

	// Filtering by action keeps only that action.
	var byAction struct {
		Items []domain.AuditEntry `json:"items"`
	}
	p.expect(http.MethodGet, "/api/audit?action=tenant.stop&page_size=200", nil, http.StatusOK, &byAction)
	if len(byAction.Items) == 0 {
		t.Fatal("filtering by tenant.stop returned nothing")
	}
	for _, entry := range byAction.Items {
		if entry.Action != "tenant.stop" {
			t.Fatalf("action filter returned %q", entry.Action)
		}
	}

	// Filtering by a tenant name keeps only that tenant.
	var byTenant struct {
		Items []domain.AuditEntry `json:"items"`
	}
	p.expect(http.MethodGet, "/api/audit?tenant=auditalice&page_size=200", nil, http.StatusOK, &byTenant)
	if len(byTenant.Items) == 0 {
		t.Fatal("filtering by tenant name returned nothing")
	}
	for _, entry := range byTenant.Items {
		if entry.TenantName != "auditalice" {
			t.Fatalf("tenant filter returned %q", entry.TenantName)
		}
	}

	// An unknown actor matches nothing but is still a valid request.
	var none struct {
		Items []domain.AuditEntry `json:"items"`
	}
	p.expect(http.MethodGet, "/api/audit?actor=ghost", nil, http.StatusOK, &none)
	if len(none.Items) != 0 {
		t.Fatalf("unknown actor returned %d entries", len(none.Items))
	}
}

// TestCertInfoReportsDisabledWhenTLSOff is what the settings page renders after
// HTTPS is turned off: the panel must not claim a certificate is in use.
func TestCertInfoReportsDisabledWhenTLSOff(t *testing.T) {
	p := start(t)
	p.login()
	settings := p.core.Svc.Settings()
	settings.TLSEnabled = false
	if err := p.core.Svc.UpdateSettings(settings); err != nil {
		t.Fatal(err)
	}
	var info struct {
		Certificate struct {
			Enabled bool `json:"enabled"`
			Exists  bool `json:"exists"`
		} `json:"certificate"`
	}
	p.expect(http.MethodGet, "/api/settings/cert", nil, http.StatusOK, &info)
	if info.Certificate.Enabled {
		t.Fatalf("certificate reported as enabled while HTTPS is off: %+v", info.Certificate)
	}
}

// TestOverviewCountsEveryState is what the dashboard shows: a running instance
// and one in the recycle bin must each be counted where the page expects them.
func TestOverviewCountsEveryState(t *testing.T) {
	p := start(t)
	p.login()
	running := p.createTenant("ovrunning")
	p.waitStatus(running, domain.StatusRunning)

	trashed := p.createTenant("ovtrashed")
	p.waitStatus(trashed, domain.StatusRunning)
	p.expect(http.MethodDelete, "/api/tenants/"+idString(trashed), map[string]string{
		"confirm": "ovtrashed",
	}, http.StatusOK, nil)

	var ov struct {
		Counts map[string]int `json:"counts"`
		Total  int            `json:"total"`
	}
	p.expect(http.MethodGet, "/api/overview", nil, http.StatusOK, &ov)
	if ov.Counts[domain.StatusRunning] < 1 {
		t.Fatalf("overview running = %d, want at least 1: %+v", ov.Counts[domain.StatusRunning], ov.Counts)
	}
	if ov.Counts[domain.StatusTrashed] < 1 {
		t.Fatalf("overview trashed = %d, want at least 1: %+v", ov.Counts[domain.StatusTrashed], ov.Counts)
	}
	if ov.Total < 2 {
		t.Fatalf("overview total = %d, want at least 2", ov.Total)
	}
}
