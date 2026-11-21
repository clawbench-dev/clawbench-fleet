package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"clawbench-fleet/internal/testutil"
)

// testutilPlatform builds an isolated platform with an admin account.
func testutilPlatform(t *testing.T) *testutil.Platform {
	t.Helper()
	platform := testutil.New(t)
	platform.BootstrapAdmin(t)
	return platform
}

func newServer(t *testing.T, platform *testutil.Platform) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(New(platform.Svc, platform.Cfg).Handler())
	t.Cleanup(srv.Close)
	return srv
}

// postJSON sends a raw JSON body with a matching Origin header.
func postJSON(t *testing.T, url, body string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", strings.TrimSuffix(url, "/api/auth/login"))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out json.RawMessage
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}
