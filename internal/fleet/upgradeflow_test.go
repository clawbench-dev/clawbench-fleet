package fleet

import (
	"context"
	"testing"

	"clawbench-fleet/internal/domain"
)

func TestApplyUpgradeWithPilotedVersion(t *testing.T) {
	svc := newService(t)
	ctx := context.Background()
	alice := createRunning(t, svc, "alice")
	for _, v := range []string{"0.1.0", "0.2.0"} {
		if _, err := svc.InstallFromNpm(ctx, v, AuditContext{}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := svc.Pilot(ctx, "0.2.0", alice.ID, AuditContext{}); err != nil {
		t.Fatal(err)
	}
	// Applying the version that is already current keeps 0.1.0 as rollback.
	job, err := svc.ApplyUpgrade(ctx, "0.2.0", AuditContext{})
	if err != nil {
		t.Fatal(err)
	}
	waitJob(t, svc, job.ID, domain.JobSucceeded)
	prev, err := svc.Store.PreviousVersion()
	if err != nil || prev.Version != "0.1.0" {
		t.Fatalf("previous = %+v %v", prev, err)
	}
}

func TestCreateTenantReportsProvisioningError(t *testing.T) {
	svc := newService(t)
	ctx := context.Background()
	// useradd fails: the record stays, and the caller is told why.
	svc.Sys = failingCreateUser{System: svc.Sys}
	tenant, _, err := svc.CreateTenant(ctx, CreateTenantInput{Username: "alice"}, AuditContext{})
	if err == nil {
		t.Fatal("a failed provisioning must be reported")
	}
	if tenant == nil {
		t.Fatal("the record should still be returned for inspection")
	}
	fresh, getErr := svc.GetTenant(tenant.ID)
	if getErr != nil {
		t.Fatal(getErr)
	}
	if fresh.Status != domain.StatusError || fresh.LastError == "" {
		t.Fatalf("failed provisioning = %+v", fresh)
	}
}

func TestPasswordGenerationNeverFails(t *testing.T) {
	for range 20 {
		p, err := GeneratePassword(12)
		if err != nil || len(p) != 12 {
			t.Fatalf("password = %q %v", p, err)
		}
	}
}
