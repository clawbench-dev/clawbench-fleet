package fleet

import (
	"context"
	"testing"

	"clawbench-fleet/internal/sysops"
)

// TestOwnCertDirInProductionMode covers the branch the dev backend skips: under
// systemd the certificate directory has to be handed to the tenant, otherwise
// the instance cannot read its own certificate.
func TestOwnCertDirInProductionMode(t *testing.T) {
	svc := newService(t)
	svc.Sys = productionMode{System: svc.Sys}

	tenant := createRunning(t, svc, "certowner")
	if err := svc.ownCertDir(tenant, tenantCertDir(tenant)); err != nil {
		t.Fatalf("handing the certificate directory over: %v", err)
	}
}

// TestOwnTenantDataDirInProductionMode is the same property for the data
// directory: a root-owned data dir makes the instance fail with permission
// denied on its first write.
func TestOwnTenantDataDirInProductionMode(t *testing.T) {
	svc := newService(t)
	svc.Sys = productionMode{System: svc.Sys}

	tenant := createRunning(t, svc, "dataowner")
	if err := svc.ownTenantDataDir(tenant); err != nil {
		t.Fatalf("handing the data directory over: %v", err)
	}
}

// TestOwnershipSkippedWithoutAUID: adopted tenants can lack a UID, and asking
// for a chown to uid 0 would hand root's files to root and silently pass.
func TestOwnershipSkippedWithoutAUID(t *testing.T) {
	svc := newService(t)
	svc.Sys = productionMode{System: svc.Sys}

	tenant := createRunning(t, svc, "nouid")
	tenant.UID, tenant.GID = 0, 0
	if err := svc.ownTenantDataDir(tenant); err != nil {
		t.Fatalf("a tenant without a UID should be skipped, got %v", err)
	}
}

// TestOwnershipFailsWhenChownFails makes sure a refused chown is reported: the
// instance would fail later with a confusing permission error instead.
func TestOwnershipFailsWhenChownFails(t *testing.T) {
	svc := newService(t)
	tenant := createRunning(t, svc, "chownfail")
	tenant.UID, tenant.GID = 5000, 5000
	// Switch only after the tenant exists: a failing chown during creation
	// would abort the account setup instead of exercising this guard.
	svc.Sys = chownFails{System: productionMode{System: svc.Sys}}
	if err := svc.ownTenantDataDir(tenant); err == nil {
		t.Fatal("a failing chown must be reported")
	}
}

// chownFails refuses every ownership change while keeping the rest working.
type chownFails struct {
	sysops.System
}

func (chownFails) Chown(context.Context, string, int, int) error {
	return context.DeadlineExceeded
}
