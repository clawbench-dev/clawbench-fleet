package fleet

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func healthHandler(app, version string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/health" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"app": app, "version": version})
	}
}

func portOf(t *testing.T, srv *httptest.Server) int {
	t.Helper()
	addr, ok := srv.Listener.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatal("test server is not TCP")
	}
	return addr.Port
}

func TestHealthCheckPlainHTTP(t *testing.T) {
	svc, _ := newTestService(t)
	srv := httptest.NewServer(healthHandler("clawbench", "v1.2.3"))
	defer srv.Close()

	info, err := svc.healthCheck(context.Background(), portOf(t, srv))
	if err != nil {
		t.Fatalf("health: %v", err)
	}
	if info.Version != "v1.2.3" {
		t.Fatalf("version = %q", info.Version)
	}
}

// An instance with TLS enabled answers plain http with a 301 to https on the
// same port and serves a self-signed certificate. The health check has to
// follow that hop and still validate the body.
func TestHealthCheckFollowsHTTPSRedirect(t *testing.T) {
	svc, _ := newTestService(t)
	tlsSrv := httptest.NewTLSServer(healthHandler("clawbench", "v0.146.0"))
	defer tlsSrv.Close()

	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, fmt.Sprintf("https://127.0.0.1:%d/api/health", portOf(t, tlsSrv)), http.StatusMovedPermanently)
	}))
	defer plain.Close()

	info, err := svc.healthCheck(context.Background(), portOf(t, plain))
	if err != nil {
		t.Fatalf("health over https: %v", err)
	}
	if info.App != "clawbench" || info.Version != "v0.146.0" {
		t.Fatalf("health = %+v", info)
	}
}

// The redirect target comes from the instance, so the platform must not be
// talked into fetching an arbitrary address.
func TestHealthCheckRefusesNonLoopbackRedirect(t *testing.T) {
	svc, _ := newTestService(t)
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://example.com/api/health", http.StatusMovedPermanently)
	}))
	defer plain.Close()

	if _, err := svc.healthCheck(context.Background(), portOf(t, plain)); err == nil {
		t.Fatal("non-loopback redirect should fail the health check")
	}
}

// Accepting the certificate must not mean accepting any answer: the body still
// has to identify itself as Clawbench.
func TestHealthCheckRejectsNonClawbenchOverHTTPS(t *testing.T) {
	svc, _ := newTestService(t)
	tlsSrv := httptest.NewTLSServer(healthHandler("something-else", "v0.146.0"))
	defer tlsSrv.Close()

	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, fmt.Sprintf("https://127.0.0.1:%d/api/health", portOf(t, tlsSrv)), http.StatusMovedPermanently)
	}))
	defer plain.Close()

	if _, err := svc.healthCheck(context.Background(), portOf(t, plain)); err == nil {
		t.Fatal("non-clawbench health response should fail")
	}
}
