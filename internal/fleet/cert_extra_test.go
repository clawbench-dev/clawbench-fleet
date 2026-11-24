package fleet

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"clawbench-fleet/internal/domain"
)

// TestDistributeCertWritesIntoTenantDir covers the normal path: with TLS on,
// each instance gets its own copy of the pair next to its config.
func TestDistributeCertWritesIntoTenantDir(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	tenant, _, err := svc.CreateTenant(ctx, CreateTenantInput{
		Username: "certuser", SSHEnabled: boolPtr(false),
	}, AuditContext{Actor: "tester"})
	if err != nil {
		t.Fatal(err)
	}
	if !svc.Settings().TLSEnabled {
		t.Skip("TLS is off in this fixture")
	}
	if err := svc.distributeCert(tenant); err != nil {
		t.Fatal(err)
	}
	dir := tenantCertDir(tenant)
	for _, name := range []string{fleetCertFile, fleetKeyFile} {
		if _, statErr := os.Stat(filepath.Join(dir, name)); statErr != nil {
			t.Fatalf("%s missing after distribution: %v", name, statErr)
		}
	}
	// The instance config must point at that directory, or the copy is unused.
	raw, err := os.ReadFile(svc.tenantConfigPath(tenant))
	if err != nil {
		t.Fatal(err)
	}
	if !contains(string(raw), dir) {
		t.Fatalf("config.yaml does not point at %s:\n%s", dir, raw)
	}
}

// TestDistributeCertRemovesCopyWhenTLSOff is the regression this guards: an
// operator turns HTTPS off, and every instance would otherwise keep loading a
// certificate the platform no longer maintains.
func TestDistributeCertRemovesCopyWhenTLSOff(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	tenant, _, err := svc.CreateTenant(ctx, CreateTenantInput{
		Username: "certoff", SSHEnabled: boolPtr(false),
	}, AuditContext{Actor: "tester"})
	if err != nil {
		t.Fatal(err)
	}

	// Put a stray pair there to stand in for a previous TLS-on run.
	dir := tenantCertDir(tenant)
	if mkErr := os.MkdirAll(dir, 0o700); mkErr != nil {
		t.Fatal(mkErr)
	}
	stray := filepath.Join(dir, fleetCertFile)
	if writeErr := os.WriteFile(stray, []byte("old"), 0o644); writeErr != nil {
		t.Fatal(writeErr)
	}

	st := svc.Settings()
	st.TLSEnabled = false
	if err := svc.UpdateSettings(st); err != nil {
		t.Fatal(err)
	}
	if err := svc.distributeCert(tenant); err != nil {
		t.Fatal(err)
	}
	if _, statErr := os.Stat(stray); !os.IsNotExist(statErr) {
		t.Fatalf("stale certificate kept after TLS was turned off: %v", statErr)
	}
}

// TestRemoveTenantCertIsIdempotent: the cleanup runs on every config rewrite,
// so a tenant that never had a copy must not produce an error.
func TestRemoveTenantCertIsIdempotent(t *testing.T) {
	svc, _ := newTestService(t)
	tenant := &domain.Tenant{Username: "never", DataDir: t.TempDir()}
	if err := svc.removeTenantCert(tenant); err != nil {
		t.Fatalf("removing a certificate that was never there: %v", err)
	}
}

// TestCertDirPrefersConfiguredPath: an operator can move certificates to a
// mounted volume, and every path must follow.
func TestCertDirPrefersConfiguredPath(t *testing.T) {
	svc, _ := newTestService(t)
	want := filepath.Join(t.TempDir(), "custom-certs")
	st := svc.Settings()
	st.TLSCertDir = want
	if err := svc.UpdateSettings(st); err != nil {
		t.Fatal(err)
	}
	if got := svc.CertDir(); got != want {
		t.Fatalf("CertDir = %q, want %q", got, want)
	}
	if got := svc.certPath(); got != filepath.Join(want, fleetCertFile) {
		t.Fatalf("certPath = %q", got)
	}
	if got := svc.keyPath(); got != filepath.Join(want, fleetKeyFile) {
		t.Fatalf("keyPath = %q", got)
	}
}

// TestCertificateIsSelfSignedAndCoversHosts is what the settings panel shows:
// an employee-facing certificate must be valid for the name employees type.
func TestCertificateIsSelfSignedAndCoversHosts(t *testing.T) {
	svc, _ := newTestService(t)
	if _, err := svc.EnsureCertificate(); err != nil {
		t.Fatal(err)
	}
	info := svc.CertInfo()
	if !info.Exists || !info.SelfSigned {
		t.Fatalf("cert info = %+v, want an existing self-signed pair", info)
	}
	if len(info.DNSNames) == 0 && len(info.IPs) == 0 {
		t.Fatalf("certificate covers no host at all: %+v", info)
	}
	if info.NotAfter == "" {
		t.Fatalf("expiry not reported: %+v", info)
	}
}

// TestCertificateCoversTheConfiguredPublicHost: employees type the public host,
// so the certificate has to carry that name or the browser warns even when the
// operator set everything up correctly.
func TestCertificateCoversTheConfiguredPublicHost(t *testing.T) {
	svc, _ := newTestService(t)
	st := svc.Settings()
	st.PublicHost = "fleet.example.com"
	if err := svc.UpdateSettings(st); err != nil {
		t.Fatal(err)
	}
	hosts := svc.certHosts()
	found := false
	for _, host := range hosts {
		if host == "fleet.example.com" {
			found = true
		}
	}
	if !found {
		t.Fatalf("certificate hosts %v do not include the public host", hosts)
	}
}

// TestEnsureCertificateReportsAnUnwritableDirectory: a host where the cert
// directory cannot be created has to say so up front, not fail at handshake
// time when every employee is locked out.
func TestEnsureCertificateReportsAnUnwritableDirectory(t *testing.T) {
	svc, _ := newTestService(t)
	st := svc.Settings()
	st.TLSEnabled = true
	// A regular file where the directory should be makes MkdirAll fail.
	blocked := filepath.Join(t.TempDir(), "certs")
	if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	st.TLSCertDir = blocked
	if err := svc.UpdateSettings(st); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.EnsureCertificate(); err == nil {
		t.Fatal("an unwritable certificate directory should be reported")
	}
}

// TestDistributeCertReportsAnUnusableTenantDirectory covers the case where the
// per-tenant certificate directory cannot be created. The push has to fail with
// a message naming the directory: silently leaving the instance without a
// certificate would make it refuse to start once HTTPS is on.
func TestDistributeCertReportsAnUnusableTenantDirectory(t *testing.T) {
	svc, _ := newTestService(t)
	if !svc.Settings().TLSEnabled {
		t.Skip("TLS is off in this fixture")
	}
	tenant := &domain.Tenant{Username: "blocked", DataDir: t.TempDir()}

	// A regular file where the cert directory should go makes MkdirAll fail.
	if err := os.MkdirAll(filepath.Dir(tenantCertDir(tenant)), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tenantCertDir(tenant), []byte("not a dir"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := svc.distributeCert(tenant)
	if err == nil {
		t.Fatal("a blocked cert directory must be reported")
	}
	if !strings.Contains(err.Error(), tenantCertDir(tenant)) {
		t.Fatalf("error should name the directory, got %v", err)
	}
}
