package fleet

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"clawbench-fleet/internal/domain"
)

func TestEnsureCertificateGeneratesAndReuses(t *testing.T) {
	svc, _ := newTestService(t)
	path, err := svc.EnsureCertificate()
	if err != nil {
		t.Fatal(err)
	}
	if path == "" {
		t.Fatal("TLS is on by default, a certificate should be created")
	}
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// A second call must reuse the pair, not mint a new one: employees should
	// only have to accept the warning once.
	if _, err := svc.EnsureCertificate(); err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("certificate was regenerated on a second call")
	}
	info := svc.CertInfo()
	if !info.Exists || !info.SelfSigned {
		t.Fatalf("cert info = %+v", info)
	}
	if len(info.IPs) == 0 {
		t.Fatal("certificate should cover at least one IP")
	}
}

func TestCertificateIsValidForLoopback(t *testing.T) {
	svc, _ := newTestService(t)
	path, err := svc.EnsureCertificate()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(data)
	if block == nil {
		t.Fatal("not a PEM file")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	// The health check talks to 127.0.0.1 over https, so loopback must be on
	// the certificate or the instance would fail its own health check.
	found := false
	for _, ip := range cert.IPAddresses {
		if ip.String() == "127.0.0.1" {
			found = true
		}
	}
	if !found {
		t.Fatalf("certificate IPs = %v", cert.IPAddresses)
	}
}

func TestRegenerateCertificateReplacesPair(t *testing.T) {
	svc, _ := newTestService(t)
	path, err := svc.EnsureCertificate()
	if err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	if _, err := svc.RegenerateCertificate(); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if bytes.Equal(before, after) {
		t.Fatal("regenerate should produce a new certificate")
	}
}

func TestEnsureCertificateSkippedWhenDisabled(t *testing.T) {
	svc, _ := newTestService(t)
	st := svc.Settings()
	st.TLSEnabled = false
	if err := svc.UpdateSettings(st); err != nil {
		t.Fatal(err)
	}
	path, err := svc.EnsureCertificate()
	if err != nil {
		t.Fatal(err)
	}
	if path != "" {
		t.Fatalf("TLS off should not create a certificate, got %q", path)
	}
}

func TestTenantConfigWrittenIntoInstanceFile(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	tenant, _, err := svc.CreateTenant(ctx, CreateTenantInput{
		Username: "cfguser", SSHEnabled: boolPtr(false),
	}, AuditContext{Actor: "tester"})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.UpdateTenantConfig([]domain.ConfigEntry{
		{Key: "chat.page_size", Value: "50"},
		{Key: "appearance.panel_opacity", Value: "0.5"},
		{Key: "kb.enabled", Value: "true"},
	}); err != nil {
		t.Fatal(err)
	}
	// Rewriting the config is what a push does; no restart needed for the file.
	if err := svc.writeTenantConfig(tenant, ""); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(svc.tenantConfigPath(tenant))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, want := range []string{"page_size: 50", "panel_opacity: 0.5", "enabled: true"} {
		if !contains(text, want) {
			t.Fatalf("config.yaml missing %q:\n%s", want, text)
		}
	}
	// The platform's own keys must still be there.
	if !contains(text, "port:") || !contains(text, "host: 0.0.0.0") {
		t.Fatalf("platform keys lost:\n%s", text)
	}
}

func TestTenantConfigTlsSectionFollowsSetting(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	tenant, _, err := svc.CreateTenant(ctx, CreateTenantInput{
		Username: "tlsuser", SSHEnabled: boolPtr(false),
	}, AuditContext{Actor: "tester"})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.writeTenantConfig(tenant, ""); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(svc.tenantConfigPath(tenant))
	if !contains(string(raw), "cert_dir:") {
		t.Fatalf("TLS on should point at a certificate:\n%s", raw)
	}
	// The tenant must have its own copy, owned by it, next to its config.
	if _, statErr := os.Stat(filepath.Join(tenantCertDir(tenant), "cert.pem")); statErr != nil {
		t.Fatalf("tenant certificate copy missing: %v", statErr)
	}

	st := svc.Settings()
	st.TLSEnabled = false
	if err := svc.UpdateSettings(st); err != nil {
		t.Fatal(err)
	}
	if err := svc.writeTenantConfig(tenant, ""); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(svc.tenantConfigPath(tenant))
	if contains(string(raw), "cert_dir:") {
		t.Fatalf("TLS off should drop cert_dir:\n%s", raw)
	}
}

func TestValidateTenantConfigRejectsReservedAndBadKeys(t *testing.T) {
	bad := [][]domain.ConfigEntry{
		{{Key: "", Value: "x"}},
		{{Key: "Port", Value: "1"}},
		{{Key: "port", Value: "1"}},
		{{Key: "password", Value: "x"}},
		{{Key: "a..b", Value: "x"}},
		{{Key: "chat.page_size", Value: "a\nb"}},
		{{Key: "chat.page_size", Value: "1"}, {Key: "chat.page_size", Value: "2"}},
	}
	for _, entries := range bad {
		if err := validateTenantConfig(entries); err == nil {
			t.Fatalf("%v should be rejected", entries)
		}
	}
	good := []domain.ConfigEntry{{Key: "chat.page_size", Value: "20"}}
	if err := validateTenantConfig(good); err != nil {
		t.Fatalf("valid entries rejected: %v", err)
	}
}

func TestAddressForPrefersPublicHostThenBrowser(t *testing.T) {
	svc, _ := newTestService(t)
	tenant := domain.Tenant{Port: 21000}

	st := svc.Settings()
	st.PublicHost = "cb.example.com"
	if err := svc.UpdateSettings(st); err != nil {
		t.Fatal(err)
	}
	if got := svc.AddressFor(tenant, "10.0.0.9:30000"); got != "https://cb.example.com:21000" {
		t.Fatalf("public host should win, got %q", got)
	}

	st.PublicHost = ""
	if err := svc.UpdateSettings(st); err != nil {
		t.Fatal(err)
	}
	if got := svc.AddressFor(tenant, "10.0.0.9:30000"); got != "https://10.0.0.9:21000" {
		t.Fatalf("browser host should be reused, got %q", got)
	}
	// localhost in the browser is useless to an employee, so fall back.
	if got := svc.AddressFor(tenant, "localhost:30000"); got == "https://localhost:21000" {
		t.Fatalf("loopback browser host should not be reused: %q", got)
	}
	if got := svc.AddressFor(tenant, "[::1]:30000"); got == "https://::1:21000" {
		t.Fatalf("ipv6 loopback should not be reused: %q", got)
	}
}

func TestNormalizeHostStripsPortAndBrackets(t *testing.T) {
	cases := map[string]string{
		"10.0.0.5:30000": "10.0.0.5",
		"10.0.0.5":       "10.0.0.5",
		"[fd00::1]:443":  "fd00::1",
		"localhost":      "",
		"127.0.0.1:80":   "",
		"":               "",
		"  cb.local  ":   "cb.local",
	}
	for in, want := range cases {
		if got := normalizeHost(in); got != want {
			t.Fatalf("normalizeHost(%q) = %q, want %q", in, got, want)
		}
	}
}

func contains(haystack, needle string) bool {
	return strings.Contains(haystack, needle)
}
