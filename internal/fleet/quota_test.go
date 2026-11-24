package fleet

import (
	"context"
	"os"
	"strings"
	"testing"

	"clawbench-fleet/internal/domain"
)

func TestApplyDefaultQuotaToAllInstances(t *testing.T) {
	svc := newService(t)
	ctx := context.Background()
	ac := AuditContext{Actor: "tester"}
	alice := createRunning(t, svc, "alice")
	bob := createRunning(t, svc, "bob")

	// Alice has a custom quota that the push has to overwrite.
	cpu, mem, tasks := "100%", "1G", 64
	if _, err := svc.UpdateTenant(ctx, alice.ID, UpdateTenantInput{
		CPUQuota: &cpu, MemoryMax: &mem, TasksMax: &tasks,
	}, ac); err != nil {
		t.Fatal(err)
	}
	st := svc.Settings()
	st.DefaultCPUQuota, st.DefaultMemoryMax, st.DefaultTasksMax = "300%", "8G", 1024
	if err := svc.UpdateSettings(st); err != nil {
		t.Fatal(err)
	}

	job, err := svc.ApplyDefaultQuotaAsync(ctx, ac)
	if err != nil {
		t.Fatal(err)
	}
	if job.Total != 2 {
		t.Fatalf("job total = %d, want 2", job.Total)
	}
	waitJob(t, svc, job.ID, domain.JobSucceeded)

	for _, id := range []int64{alice.ID, bob.ID} {
		got, getErr := svc.GetTenant(id)
		if getErr != nil {
			t.Fatal(getErr)
		}
		if got.CPUQuota != "300%" || got.MemoryMax != "8G" || got.TasksMax != 1024 {
			t.Fatalf("%s quota = %+v", got.Username, got)
		}
		if got.Status != domain.StatusRunning {
			t.Fatalf("%s status = %s, want running", got.Username, got.Status)
		}
	}

	unit, err := os.ReadFile(svc.Cfg.UnitPath("alice"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"CPUQuota=300%", "MemoryMax=8G", "TasksMax=1024"} {
		if !strings.Contains(string(unit), want) {
			t.Fatalf("unit missing %q:\n%s", want, unit)
		}
	}

	// Applying a second time has nothing left to do and must not create a job.
	if _, err := svc.ApplyDefaultQuotaAsync(ctx, ac); err == nil {
		t.Fatal("second apply should report that nothing needs changing")
	}
}

func TestQuotaTargetsSkipUnmanagedStates(t *testing.T) {
	svc := newService(t)
	ctx := context.Background()
	ac := AuditContext{Actor: "tester"}
	alice := createRunning(t, svc, "alice")
	bob := createRunning(t, svc, "bob")

	st := svc.Settings()
	st.DefaultCPUQuota, st.DefaultMemoryMax, st.DefaultTasksMax = "300%", "8G", 1024
	if err := svc.UpdateSettings(st); err != nil {
		t.Fatal(err)
	}
	// Alice is already at the target, so only Bob would be touched.
	cpu, mem, tasks := st.DefaultCPUQuota, st.DefaultMemoryMax, st.DefaultTasksMax
	if _, err := svc.UpdateTenant(ctx, alice.ID, UpdateTenantInput{
		CPUQuota: &cpu, MemoryMax: &mem, TasksMax: &tasks,
	}, ac); err != nil {
		t.Fatal(err)
	}
	if err := svc.Store.SetTenantStatus(bob.ID, domain.StatusCreating, ""); err != nil {
		t.Fatal(err)
	}

	targets, err := svc.quotaTargets(st)
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 0 {
		t.Fatalf("targets = %+v, want none", targets)
	}
	if _, err := svc.ApplyDefaultQuotaAsync(ctx, ac); err == nil {
		t.Fatal("nothing to apply should be an error")
	}

	// The per-tenant step is defensive: a tenant that already matches is a
	// no-op, and one that is being created is skipped instead of rewritten.
	if err := svc.applyQuotaToTenant(ctx, alice.ID, st, ac); err != nil {
		t.Fatalf("matching tenant: %v", err)
	}
	if err := svc.applyQuotaToTenant(ctx, bob.ID, st, ac); err == nil {
		t.Fatal("a tenant being created must be skipped")
	}
}

func TestRunApplyQuotaRecordsFailure(t *testing.T) {
	svc := newService(t)
	ctx := context.Background()
	job, err := svc.Store.CreateJob("quota", "{}", 1)
	if err != nil {
		t.Fatal(err)
	}
	itemID, err := svc.Store.AddJobItem(job.ID, 999, "ghost")
	if err != nil {
		t.Fatal(err)
	}
	svc.runApplyQuota(ctx, job.ID, svc.Settings(), []domain.Tenant{{ID: 999, Username: "ghost"}}, []int64{itemID}, AuditContext{})

	got, err := svc.Job(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.JobFailed {
		t.Fatalf("job status = %s, want failed", got.Status)
	}
	if len(got.Items) != 1 || got.Items[0].Status != domain.JobFailed || got.Items[0].Error == "" {
		t.Fatalf("job items = %+v", got.Items)
	}
}
