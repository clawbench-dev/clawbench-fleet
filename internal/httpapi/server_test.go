package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"

	"clawbench-fleet/internal/testutil"
)

// client is a logged-in admin talking to the real handler stack.
type client struct {
	t    *testing.T
	srv  *httptest.Server
	http *http.Client
}

func newTestClient(t *testing.T) (*client, *testutil.Platform) {
	t.Helper()
	platform := testutil.New(t)
	platform.BootstrapAdmin(t)
	srv := httptest.NewServer(New(platform.Svc, platform.Cfg).Handler())
	t.Cleanup(srv.Close)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	c := &client{t: t, srv: srv, http: &http.Client{Jar: jar}}
	status, body := c.request(http.MethodPost, "/api/auth/login", map[string]string{
		"username": "admin", "password": testutil.AdminLogin,
	})
	if status != http.StatusOK {
		t.Fatalf("login = %d %s", status, body)
	}
	return c, platform
}

// request sends JSON (if body is not nil) and returns the status and raw body.
func (c *client) request(method, path string, body any) (int, []byte) {
	c.t.Helper()
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			c.t.Fatal(err)
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, c.srv.URL+path, reader)
	if err != nil {
		c.t.Fatal(err)
	}
	req.Header.Set("Origin", c.srv.URL)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		c.t.Fatal(err)
	}
	return resp.StatusCode, data
}

// json sends a request and decodes the JSON reply.
func (c *client) json(method, path string, body, out any) int {
	c.t.Helper()
	status, data := c.request(method, path, body)
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			c.t.Fatalf("%s %s: decode %q: %v", method, path, data, err)
		}
	}
	return status
}

func (c *client) upload(path, field, filename string, content []byte) (int, []byte) {
	c.t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, err := mw.CreateFormFile(field, filename)
	if err != nil {
		c.t.Fatal(err)
	}
	if _, writeErr := part.Write(content); writeErr != nil {
		c.t.Fatal(writeErr)
	}
	if closeErr := mw.Close(); closeErr != nil {
		c.t.Fatal(closeErr)
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, c.srv.URL+path, &buf)
	if err != nil {
		c.t.Fatal(err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Origin", c.srv.URL)
	resp, err := c.http.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, data
}

func TestHealthAndSecurityHeaders(t *testing.T) {
	platform := testutil.New(t)
	srv := httptest.NewServer(New(platform.Svc, platform.Cfg).Handler())
	defer srv.Close()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/api/health", http.NoBody)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("health = %d", resp.StatusCode)
	}
	if got := resp.Header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("nosniff header = %q", got)
	}
	if got := resp.Header.Get("Cache-Control"); got != "no-store" {
		t.Fatalf("api cache header = %q", got)
	}
	var health map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&health); err != nil {
		t.Fatal(err)
	}
	if health["app"] != "clawbench-fleet" {
		t.Fatalf("health payload = %v", health)
	}
}

func TestProtectedEndpointsRequireLogin(t *testing.T) {
	platform := testutil.New(t)
	srv := httptest.NewServer(New(platform.Svc, platform.Cfg).Handler())
	defer srv.Close()
	for _, path := range []string{"/api/tenants", "/api/overview", "/api/settings"} {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+path, http.NoBody)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s without login = %d", path, resp.StatusCode)
		}
	}
}

func TestLoginRejectsBadPasswordAndCrossOrigin(t *testing.T) {
	platform := testutil.New(t)
	platform.BootstrapAdmin(t)
	srv := httptest.NewServer(New(platform.Svc, platform.Cfg).Handler())
	defer srv.Close()
	post := func(body string, origin string) *http.Response {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL+"/api/auth/login", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	for _, tc := range []struct {
		body   string
		origin string
		want   int
	}{
		{`{"username":"admin","password":"wrong-password"}`, "", http.StatusUnauthorized},
		{`{"username":"admin","password":"` + testutil.AdminLogin + `"}`, "http://evil.example", http.StatusForbidden},
		{`{not json`, "", http.StatusBadRequest},
	} {
		resp := post(tc.body, tc.origin)
		_ = resp.Body.Close()
		if resp.StatusCode != tc.want {
			t.Fatalf("login %s from %q = %d, want %d", tc.body, tc.origin, resp.StatusCode, tc.want)
		}
	}
}

func TestBadIDIsRejected(t *testing.T) {
	c, _ := newTestClient(t)
	for _, path := range []string{"/api/tenants/abc", "/api/tenants/0", "/api/tenants/-1"} {
		if status, _ := c.request(http.MethodGet, path, nil); status != http.StatusBadRequest {
			t.Fatalf("%s = %d, want 400", path, status)
		}
	}
	if status, _ := c.request(http.MethodGet, "/api/tenants/999", nil); status != http.StatusNotFound {
		t.Fatalf("missing tenant = %d, want 404", status)
	}
}
