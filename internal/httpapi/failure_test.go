package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"clawbench-fleet/internal/sysops"
)

// brokenRestart keeps the simulated backend but makes every restart fail, so
// the "operation partially succeeded" branches can be exercised over HTTP.
type brokenRestart struct {
	sysops.System
}

func (brokenRestart) Restart(context.Context, string) error { return errors.New("restart failed") }
func (brokenRestart) Start(context.Context, string) error   { return errors.New("start failed") }

// raw sends a body exactly as given (used for malformed JSON and multipart).
func (c *client) raw(method, path, contentType string, body []byte) int {
	c.t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), method, c.srv.URL+path, bytes.NewReader(body))
	if err != nil {
		c.t.Fatal(err)
	}
	req.Header.Set("Origin", c.srv.URL)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

func (c *client) rawWithOrigin(method, path, contentType, origin string, body []byte) int {
	c.t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), method, c.srv.URL+path, bytes.NewReader(body))
	if err != nil {
		c.t.Fatal(err)
	}
	req.Header.Set("Origin", origin)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

func TestGuestHandlerErrors(t *testing.T) {
	c, platform := newTestClient(t)
	created := createTenant(t, c, "alice")
	id := tenantID(t, created)
	waitForStatus(t, c, id)

	// Broken body (before the rate limit kicks in).
	if status := c.raw(http.MethodPost, "/api/public/reports", "application/json", []byte(`{not json`)); status != http.StatusBadRequest {
		t.Fatalf("broken body = %d", status)
	}
	// Rate limiting: the broken-body request above already used one slot, so
	// nine more pass and the next is refused.
	for i := range 9 {
		if status, body := c.request(http.MethodPost, "/api/public/reports", map[string]any{"tenant_id": id, "message": "打不开"}); status != http.StatusCreated {
			t.Fatalf("report %d = %d %s", i, status, body)
		}
	}
	if status, _ := c.request(http.MethodPost, "/api/public/reports", map[string]any{"tenant_id": id, "message": "打不开"}); status != http.StatusTooManyRequests {
		t.Fatalf("eleventh report = %d, want 429", status)
	}
	// A foreign Origin is rejected.
	body, _ := json.Marshal(map[string]any{"tenant_id": id, "message": "hi"})
	if status := c.rawWithOrigin(http.MethodPost, "/api/public/reports", "application/json", "http://evil.example", body); status != http.StatusForbidden {
		t.Fatalf("bad origin = %d", status)
	}
	_ = platform
}

func TestHandlerServiceErrors(t *testing.T) {
	c, platform := newTestClient(t)
	created := createTenant(t, c, "alice")
	id := tenantID(t, created)
	waitForStatus(t, c, id)
	// Close the store: every handler that touches it now reports an error.
	if err := platform.Store.Close(); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/overview", "/api/audit", "/api/trash", "/api/tenants", "/api/public/instances"} {
		if status, body := c.request(http.MethodGet, path, nil); status < 400 {
			t.Fatalf("%s after close = %d %s", path, status, body)
		}
	}
	for _, path := range []string{
		"/api/tenants/" + itoa(id),
		"/api/tenants/" + itoa(id) + "/backups",
		"/api/tenants/" + itoa(id) + "/logs",
		"/api/tenants/" + itoa(id) + "/metrics",
	} {
		if status, _ := c.request(http.MethodGet, path, nil); status < 400 {
			t.Fatalf("%s after close = %d", path, status)
		}
	}
	if status, _ := c.request(http.MethodPost, "/api/tenants/"+itoa(id)+"/backups", nil); status < 400 {
		t.Fatalf("create backup after close = %d", status)
	}
}

func TestResetRepairAndRestoreWarnings(t *testing.T) {
	c, platform := newTestClient(t)
	created := createTenant(t, c, "alice")
	id := tenantID(t, created)
	waitForStatus(t, c, id)
	platform.Svc.Sys = brokenRestart{System: platform.Svc.Sys}

	var reset struct {
		Password string `json:"password"`
		Warning  string `json:"warning"`
	}
	if status := c.json(http.MethodPost, "/api/tenants/"+itoa(id)+"/reset-password", nil, &reset); status != http.StatusOK {
		t.Fatalf("reset = %d %+v", status, reset)
	}
	if reset.Password == "" || reset.Warning == "" {
		t.Fatalf("reset should warn: %+v", reset)
	}
	var repair struct {
		Warning string `json:"warning"`
	}
	if status := c.json(http.MethodPost, "/api/tenants/"+itoa(id)+"/repair", nil, &repair); status != http.StatusOK {
		t.Fatalf("repair = %d %+v", status, repair)
	}
	if repair.Warning == "" {
		t.Fatalf("repair should warn: %+v", repair)
	}
	if status := c.json(http.MethodDelete, "/api/tenants/"+itoa(id), map[string]string{"confirm": "alice"}, nil); status != http.StatusOK {
		t.Fatalf("delete = %d", status)
	}
	var restore struct {
		Warning string `json:"warning"`
	}
	if status := c.json(http.MethodPost, "/api/trash/"+itoa(id)+"/restore", nil, &restore); status != http.StatusOK {
		t.Fatalf("restore = %d %+v", status, restore)
	}
	if restore.Warning == "" {
		t.Fatalf("restore should warn: %+v", restore)
	}
}

func TestLogsAndMetricsLimits(t *testing.T) {
	c, _ := newTestClient(t)
	created := createTenant(t, c, "alice")
	id := tenantID(t, created)
	waitForStatus(t, c, id)
	for _, query := range []string{"?lines=0", "?lines=99999", "?lines=abc"} {
		if status, body := c.request(http.MethodGet, "/api/tenants/"+itoa(id)+"/logs"+query, nil); status != http.StatusOK {
			t.Fatalf("logs%s = %d %s", query, status, body)
		}
	}
	if status, _ := c.request(http.MethodGet, "/api/tenants/"+itoa(id)+"/metrics", nil); status != http.StatusOK {
		t.Fatalf("metrics = %d", status)
	}
}

func TestImportHandlerErrors(t *testing.T) {
	c, platform := newTestClient(t)
	cases := []struct {
		name     string
		field    string
		filename string
		body     []byte
		want     int
	}{
		{"wrong field", "upload", "a.csv", []byte("username\nalice\n"), http.StatusBadRequest},
		{"empty csv", "file", "a.csv", []byte(""), http.StatusBadRequest},
		{"only header", "file", "a.csv", []byte("username\n"), http.StatusBadRequest},
		{"missing username column", "file", "a.csv", []byte("name\nalice\n"), http.StatusBadRequest},
		{"broken row", "file", "a.csv", []byte("username\n\"unclosed\n"), http.StatusBadRequest},
	}
	for _, tc := range cases {
		if status, body := c.upload("/api/tenants/import/preview", tc.field, tc.filename, tc.body); status != tc.want {
			t.Fatalf("%s = %d, want %d: %s", tc.name, status, tc.want, body)
		}
	}
	// More than 1000 rows is rejected before any work starts.
	var big strings.Builder
	big.WriteString("username\n")
	for i := range 1001 {
		big.WriteString("user")
		big.WriteString(strings.Repeat("x", 3))
		big.WriteString(string(rune('a' + i%26)))
		big.WriteString("\n")
	}
	if status, _ := c.upload("/api/tenants/import/preview", "file", "big.csv", []byte(big.String())); status != http.StatusBadRequest {
		t.Fatalf("1001 rows = %d", status)
	}
	// A malformed multipart body is rejected.
	if status := c.raw(http.MethodPost, "/api/tenants/import/preview", "multipart/form-data; boundary=nope", []byte("not multipart")); status != http.StatusBadRequest {
		t.Fatalf("broken multipart = %d", status)
	}
	// Import itself fails when the store is gone.
	if err := platform.Store.Close(); err != nil {
		t.Fatal(err)
	}
	if status, _ := c.upload("/api/tenants/import", "file", "a.csv", []byte("username\nalice\n")); status < 400 {
		t.Fatalf("import after close = %d", status)
	}
}

func TestStaticAndJobErrors(t *testing.T) {
	c, _ := newTestClient(t)
	if status, _ := c.request(http.MethodGet, "/assets/missing.js", nil); status != http.StatusNotFound {
		t.Fatalf("missing asset = %d", status)
	}
	if status, _ := c.request(http.MethodGet, "/api/missing", nil); status != http.StatusNotFound {
		t.Fatalf("missing api = %d", status)
	}
	if status, _ := c.request(http.MethodGet, "/api/jobs/abc/artifacts/success.csv", nil); status != http.StatusBadRequest {
		t.Fatalf("bad job id = %d", status)
	}
}
