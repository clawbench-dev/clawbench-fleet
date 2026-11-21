package httpapi

import (
	"net/http"
	"strconv"
	"strings"
	"testing"
)

func TestAuthFlow(t *testing.T) {
	c, _ := newTestClient(t)
	var me struct {
		Admin map[string]any `json:"admin"`
	}
	if status := c.json(http.MethodGet, "/api/auth/me", nil, &me); status != http.StatusOK || me.Admin["username"] != "admin" {
		t.Fatalf("me = %d %+v", status, me)
	}
	if status := c.json(http.MethodPost, "/api/auth/password", map[string]string{
		"current_password": "wrong-password", "new_password": "another-pass-2",
	}, nil); status != http.StatusBadRequest {
		t.Fatalf("wrong current password = %d", status)
	}
	if status := c.json(http.MethodPost, "/api/auth/password", map[string]string{
		"current_password": "test-password-1", "new_password": "short",
	}, nil); status != http.StatusBadRequest {
		t.Fatalf("weak new password = %d", status)
	}
	var changed struct {
		Relogin bool `json:"relogin"`
	}
	if status := c.json(http.MethodPost, "/api/auth/password", map[string]string{
		"current_password": "test-password-1", "new_password": "a-better-pass-2",
	}, &changed); status != http.StatusOK || !changed.Relogin {
		t.Fatalf("change password = %d %+v", status, changed)
	}
	// Every session is revoked, so the old cookie no longer works.
	if status := c.json(http.MethodGet, "/api/auth/me", nil, nil); status != http.StatusUnauthorized {
		t.Fatalf("me after password change = %d", status)
	}
	if status := c.json(http.MethodPost, "/api/auth/login", map[string]string{
		"username": "admin", "password": "a-better-pass-2",
	}, nil); status != http.StatusOK {
		t.Fatalf("login with new password = %d", status)
	}
	if status := c.json(http.MethodPost, "/api/auth/logout", nil, nil); status != http.StatusOK {
		t.Fatalf("logout = %d", status)
	}
	if status := c.json(http.MethodGet, "/api/auth/me", nil, nil); status != http.StatusUnauthorized {
		t.Fatalf("me after logout = %d", status)
	}
}

func TestLoginLimiterBlocksAfterRepeatedFailures(t *testing.T) {
	platform := testutilPlatform(t)
	srv := newServer(t, platform)
	post := func() int {
		status, _ := postJSON(t, srv.URL+"/api/auth/login", `{"username":"admin","password":"nope-nope-nope"}`)
		return status
	}
	for i := range 5 {
		if status := post(); status != http.StatusUnauthorized {
			t.Fatalf("attempt %d = %d", i+1, status)
		}
	}
	if status := post(); status != http.StatusTooManyRequests {
		t.Fatalf("sixth attempt = %d, want 429", status)
	}
}

func TestGuestPageAndReports(t *testing.T) {
	c, platform := newTestClient(t)
	created := createTenant(t, c, "alice")
	id := tenantID(t, created)
	waitForStatus(t, c, id)

	var page struct {
		Enabled bool             `json:"enabled"`
		Items   []map[string]any `json:"items"`
	}
	if status := c.json(http.MethodGet, "/api/public/instances", nil, &page); status != http.StatusOK || !page.Enabled || len(page.Items) != 1 {
		t.Fatalf("guest page = %d %+v", status, page)
	}
	if _, ok := page.Items[0]["uid"]; ok {
		t.Fatal("guest page must not expose internal fields")
	}

	if status := c.json(http.MethodPost, "/api/public/reports", map[string]any{"tenant_id": id, "message": "打不开"}, nil); status != http.StatusCreated {
		t.Fatalf("submit report = %d", status)
	}
	// A second click within ten minutes is treated as success, not a duplicate.
	if status := c.json(http.MethodPost, "/api/public/reports", map[string]any{"tenant_id": id, "message": "打不开"}, nil); status != http.StatusCreated {
		t.Fatalf("duplicate report = %d", status)
	}
	if status := c.json(http.MethodPost, "/api/public/reports", map[string]any{"tenant_id": 999, "message": "x"}, nil); status != http.StatusBadRequest {
		t.Fatalf("report for unknown tenant = %d", status)
	}
	if status := c.json(http.MethodPost, "/api/public/reports", map[string]any{"tenant_id": id, "message": strings.Repeat("长", 301)}, nil); status != http.StatusBadRequest {
		t.Fatalf("overlong report = %d", status)
	}

	var reports struct {
		Items []map[string]any `json:"items"`
	}
	if status := c.json(http.MethodGet, "/api/reports", nil, &reports); status != http.StatusOK || len(reports.Items) != 1 {
		t.Fatalf("reports = %d %+v", status, reports)
	}
	reportID, ok := reports.Items[0]["id"].(float64)
	if !ok {
		t.Fatalf("report has no id: %+v", reports.Items[0])
	}
	if status := c.json(http.MethodPost, "/api/reports/"+strconv.FormatInt(int64(reportID), 10)+"/resolve", nil, nil); status != http.StatusOK {
		t.Fatalf("resolve = %d", status)
	}
	if status := c.json(http.MethodPost, "/api/reports/"+strconv.FormatInt(int64(reportID), 10)+"/resolve", nil, nil); status != http.StatusNotFound {
		t.Fatalf("resolve twice = %d, want 404", status)
	}

	// Turning the guest page off hides the instances and rejects reports.
	settings := platform.Svc.Settings()
	settings.GuestEnabled = false
	if err := platform.Svc.UpdateSettings(settings); err != nil {
		t.Fatal(err)
	}
	if status := c.json(http.MethodGet, "/api/public/instances", nil, &page); status != http.StatusOK || page.Enabled {
		t.Fatalf("disabled guest page = %d %+v", status, page)
	}
	if status := c.json(http.MethodPost, "/api/public/reports", map[string]any{"tenant_id": id, "message": "hi"}, nil); status != http.StatusForbidden {
		t.Fatalf("report while disabled = %d", status)
	}
}
