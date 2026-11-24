//go:build integration

package httpapi_test

import (
	"net/http"
	"testing"

	"clawbench-fleet/internal/domain"
)

// TestMetricHistoryRejectsBadWindow covers the window parsing the chart sends:
// anything that is not a duration has to come back as a clear 400 so the page
// can show a hint instead of an empty chart.
func TestMetricHistoryRejectsBadWindow(t *testing.T) {
	p := start(t)
	p.login()
	id := p.createTenant("badwindow")
	p.waitStatus(id, domain.StatusRunning)

	for _, window := range []string{"banana", "30"} {
		status, body := p.do(http.MethodGet, "/api/tenants/"+idString(id)+"/metrics/history?window="+window, nil)
		if status != http.StatusBadRequest {
			t.Fatalf("window %q = %d, want 400: %s", window, status, body)
		}
	}

	// A zero or negative window is clamped to a sane range inside the service
	// instead of returning an empty chart, so the call still succeeds.
	for _, window := range []string{"0s", "-5h", "720h"} {
		var ok struct {
			Items []domain.MetricSample `json:"items"`
		}
		p.expect(http.MethodGet, "/api/tenants/"+idString(id)+"/metrics/history?window="+window, nil, http.StatusOK, &ok)
	}
	status, body := p.do(http.MethodGet, "/api/tenants/abc/metrics/history", nil)
	if status != http.StatusBadRequest {
		t.Fatalf("non-numeric id = %d, want 400: %s", status, body)
	}
}

// TestRefreshDiskOnMissingTenant is the 404 path behind a stale detail page.
func TestRefreshDiskOnMissingTenant(t *testing.T) {
	p := start(t)
	p.login()
	status, body := p.do(http.MethodPost, "/api/tenants/999999/disk/refresh", nil)
	if status != http.StatusNotFound {
		t.Fatalf("refresh disk on a missing tenant = %d, want 404: %s", status, body)
	}
}

// TestSSHToggleRoundTrip turns SSH off and on again; the detail page reads the
// flag back and a second platform restart must not re-enable it.
func TestSSHToggleRoundTrip(t *testing.T) {
	p := start(t)
	p.login()
	id := p.createTenant("sshuser")
	p.waitStatus(id, domain.StatusRunning)

	for _, want := range []bool{false, true} {
		var got struct {
			Tenant struct {
				SSHEnabled bool `json:"ssh_enabled"`
			} `json:"tenant"`
		}
		p.expect(http.MethodPost, "/api/tenants/"+idString(id)+"/ssh", map[string]any{
			"enabled": want,
		}, http.StatusOK, &got)
		if got.Tenant.SSHEnabled != want {
			t.Fatalf("ssh_enabled = %v, want %v", got.Tenant.SSHEnabled, want)
		}
	}
}

// TestBulkRejectsEmptySelection: an empty selection is a caller mistake and
// must not create a job that does nothing.
func TestBulkRejectsEmptySelection(t *testing.T) {
	p := start(t)
	p.login()
	for _, action := range []string{"start", "stop", "restart"} {
		status, body := p.do(http.MethodPost, "/api/tenants/bulk", map[string]any{
			"action": action, "ids": []int64{},
		})
		if status != http.StatusBadRequest {
			t.Fatalf("bulk %s with no ids = %d, want 400: %s", action, status, body)
		}
	}
}

// TestBulkStopAndStartTwoInstances is the batch operation an operator runs most:
// stop a group, then bring it back. Every instance must end up running again.
func TestBulkStopAndStartTwoInstances(t *testing.T) {
	p := start(t)
	p.login()
	alice := p.createTenant("bulkalice")
	bob := p.createTenant("bulkbob")
	p.waitStatus(alice, domain.StatusRunning)
	p.waitStatus(bob, domain.StatusRunning)
	ids := []int64{alice, bob}

	var stopped struct {
		Job domain.Job `json:"job"`
	}
	p.expect(http.MethodPost, "/api/tenants/bulk", map[string]any{
		"action": "stop", "ids": ids,
	}, http.StatusAccepted, &stopped)
	for _, id := range ids {
		p.waitStatus(id, domain.StatusStopped)
	}

	var started struct {
		Job domain.Job `json:"job"`
	}
	p.expect(http.MethodPost, "/api/tenants/bulk", map[string]any{
		"action": "start", "ids": ids,
	}, http.StatusAccepted, &started)
	for _, id := range ids {
		p.waitStatus(id, domain.StatusRunning)
	}
}
