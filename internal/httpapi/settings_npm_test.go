package httpapi

import (
	"net/http"
	"testing"

	"clawbench-fleet/internal/domain"
)

// The three npm-schedule fields are part of Settings like any other, so the
// settings endpoint has to carry them out and back. Without this the schedule
// card in the UI would silently save nothing.
func TestSettingsRoundTripsNpmSchedule(t *testing.T) {
	c, _ := newTestClient(t)

	var got struct {
		Settings domain.Settings `json:"settings"`
	}
	if status := c.json(http.MethodGet, "/api/settings", nil, &got); status != http.StatusOK {
		t.Fatalf("get settings = %d", status)
	}
	// Defaults are off; an unconfigured platform must not start downloading.
	if got.Settings.NpmAutoEnabled || got.Settings.NpmAutoWeekly {
		t.Fatalf("npm schedule should default to off: %+v", got.Settings)
	}

	next := got.Settings
	next.NpmAutoEnabled = true
	next.NpmAutoWeekly = true
	next.NpmAutoHour = 3
	if status := c.json(http.MethodPatch, "/api/settings", next, nil); status != http.StatusOK {
		t.Fatalf("update settings = %d", status)
	}

	var after struct {
		Settings domain.Settings `json:"settings"`
	}
	if status := c.json(http.MethodGet, "/api/settings", nil, &after); status != http.StatusOK {
		t.Fatalf("get settings = %d", status)
	}
	if !after.Settings.NpmAutoEnabled || !after.Settings.NpmAutoWeekly || after.Settings.NpmAutoHour != 3 {
		t.Fatalf("npm schedule did not persist: %+v", after.Settings)
	}
}

// An hour outside 0-23 is a configuration the scheduler can never match, so it
// is rejected at the door rather than accepted and silently ignored.
func TestSettingsRejectsBadNpmHour(t *testing.T) {
	c, _ := newTestClient(t)

	var got struct {
		Settings domain.Settings `json:"settings"`
	}
	if status := c.json(http.MethodGet, "/api/settings", nil, &got); status != http.StatusOK {
		t.Fatalf("get settings = %d", status)
	}
	for _, hour := range []int{-1, 24} {
		next := got.Settings
		next.NpmAutoHour = hour
		if status := c.json(http.MethodPatch, "/api/settings", next, nil); status != http.StatusBadRequest {
			t.Fatalf("npm_auto_hour=%d should be rejected, got %d", hour, status)
		}
	}
}
