package fleet

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"clawbench-fleet/internal/domain"
	"clawbench-fleet/internal/sysops"
)

// diskRecorder tracks the account changes a disk suspension makes.
type diskRecorder struct {
	sysops.System

	locked   map[string]bool
	killed   map[string]bool
	usage    map[string]int64
	lockErr  error
	killErr  error
	usageErr error
}

func newDiskRecorder() *diskRecorder {
	return &diskRecorder{locked: map[string]bool{}, killed: map[string]bool{}, usage: map[string]int64{}}
}

func (r *diskRecorder) LockPassword(ctx context.Context, username string) error {
	if r.lockErr != nil {
		return r.lockErr
	}
	r.locked[username] = true
	return nil
}

func (r *diskRecorder) KillUserProcesses(ctx context.Context, username string) error {
	if r.killErr != nil {
		return r.killErr
	}
	r.killed[username] = true
	return nil
}

func (r *diskRecorder) DiskUsage(ctx context.Context, path string) (int64, error) {
	if r.usageErr != nil {
		return 0, r.usageErr
	}
	for name, size := range r.usage {
		if strings.HasSuffix(path, "/"+name) {
			return size, nil
		}
	}
	return r.System.DiskUsage(ctx, path)
}

func newDiskTestService(t *testing.T) (*Service, *diskRecorder) {
	t.Helper()
	rec := newDiskRecorder()
	svc, _ := newTestServiceWith(t, func(inner sysops.System) sysops.System {
		rec.System = inner
		return rec
	})
	return svc, rec
}

func TestDiskLimitBytes(t *testing.T) {
	if diskLimitBytes(0) != 0 {
		t.Fatal("0 must mean no limit")
	}
	if diskLimitBytes(2) != 2*gib {
		t.Fatal("2 GB must be two gibibytes")
	}
	tc := &domain.Tenant{DiskQuotaGB: 1}
	if !diskOverLimit(tc, gib) || diskOverLimit(tc, gib-1) {
		t.Fatal("the limit must trigger at the exact bound")
	}
	unlimited := &domain.Tenant{}
	if diskOverLimit(unlimited, gib*100) {
		t.Fatal("no limit must never trigger")
	}
}

func TestEnforceDiskLimitStopsAndLocks(t *testing.T) {
	svc, rec := newDiskTestService(t)
	ctx := context.Background()
	tenant, _, err := svc.CreateTenant(ctx, CreateTenantInput{Username: "diskuser"}, AuditContext{})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	tenant.DiskQuotaGB = 1
	tenant.DiskBytes = 3 * gib
	svc.enforceDiskLimit(ctx, tenant)

	if tenant.Status != domain.StatusError {
		t.Fatalf("status = %s", tenant.Status)
	}
	if !strings.Contains(tenant.LastError, diskSuspendNote) || !strings.Contains(tenant.LastError, diskSSHLockedNote) {
		t.Fatalf("error = %q", tenant.LastError)
	}
	if !rec.locked["diskuser"] || !rec.killed["diskuser"] {
		t.Fatalf("locked=%v killed=%v", rec.locked, rec.killed)
	}
	if active, _ := svc.Sys.IsActive(ctx, tenant.UnitName); active {
		t.Fatal("instance should be stopped")
	}
}

func TestEnforceDiskLimitLeavesPasswordWhenSSHOff(t *testing.T) {
	svc, rec := newDiskTestService(t)
	tenant, _, err := svc.CreateTenant(context.Background(), CreateTenantInput{
		Username: "nossh", SSHEnabled: boolPtr(false),
	}, AuditContext{})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	tenant.DiskQuotaGB = 1
	tenant.DiskBytes = 2 * gib
	svc.enforceDiskLimit(context.Background(), tenant)
	if rec.locked["nossh"] {
		t.Fatal("a tenant without SSH has no password to lock")
	}
	if strings.Contains(tenant.LastError, diskSSHLockedNote) {
		t.Fatalf("error should not mention SSH: %q", tenant.LastError)
	}
}

func TestDiskGuardBeforeStart(t *testing.T) {
	svc, rec := newDiskTestService(t)
	tenant, _, err := svc.CreateTenant(context.Background(), CreateTenantInput{Username: "guard"}, AuditContext{})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if err := svc.diskGuardBeforeStart(context.Background(), tenant); err != nil {
		t.Fatalf("no limit should always pass: %v", err)
	}

	tenant.DiskQuotaGB = 1
	rec.usage["guard"] = 2 * gib
	if err := svc.diskGuardBeforeStart(context.Background(), tenant); err == nil {
		t.Fatal("over-limit tenant must not start")
	}
	rec.usage["guard"] = gib / 2
	if err := svc.diskGuardBeforeStart(context.Background(), tenant); err != nil {
		t.Fatalf("under-limit tenant should start: %v", err)
	}
	rec.usageErr = errors.New("du failed")
	if err := svc.diskGuardBeforeStart(context.Background(), tenant); err != nil {
		t.Fatalf("a failed measurement must not block the start: %v", err)
	}
}

func TestStartTenantRefusesWhenOverDiskLimit(t *testing.T) {
	svc, rec := newDiskTestService(t)
	ctx := context.Background()
	tenant, _, err := svc.CreateTenant(ctx, CreateTenantInput{Username: "bigdisk"}, AuditContext{})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := svc.StopTenant(ctx, tenant.ID, AuditContext{}); err != nil {
		t.Fatal(err)
	}
	quota := 1
	if _, err := svc.UpdateTenant(ctx, tenant.ID, UpdateTenantInput{DiskQuotaGB: &quota}, AuditContext{}); err != nil {
		t.Fatalf("set quota: %v", err)
	}
	rec.usage["bigdisk"] = 5 * gib
	if _, err := svc.StartTenant(ctx, tenant.ID, AuditContext{}); err == nil {
		t.Fatal("start should be refused while over the limit")
	}
}

func TestEnforceDiskLimitsLoopStopsOnlyOverLimit(t *testing.T) {
	svc, rec := newDiskTestService(t)
	ctx := context.Background()
	small, _, err := svc.CreateTenant(ctx, CreateTenantInput{Username: "small"}, AuditContext{})
	if err != nil {
		t.Fatal(err)
	}
	big, _, err := svc.CreateTenant(ctx, CreateTenantInput{Username: "big"}, AuditContext{})
	if err != nil {
		t.Fatal(err)
	}
	quota := 1
	for _, id := range []int64{small.ID, big.ID} {
		if _, err := svc.UpdateTenant(ctx, id, UpdateTenantInput{DiskQuotaGB: &quota}, AuditContext{}); err != nil {
			t.Fatal(err)
		}
	}
	rec.usage["small"] = gib / 10
	rec.usage["big"] = 4 * gib
	svc.enforceDiskLimits(ctx)

	fresh, err := svc.GetTenant(big.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Status != domain.StatusError {
		t.Fatalf("big tenant status = %s", fresh.Status)
	}
	ok, err := svc.GetTenant(small.ID)
	if err != nil {
		t.Fatal(err)
	}
	if ok.Status != domain.StatusRunning {
		t.Fatalf("small tenant status = %s", ok.Status)
	}
	if ok.DiskBytes != gib/10 {
		t.Fatalf("disk bytes not refreshed: %d", ok.DiskBytes)
	}
}

func TestEnforceDiskLimitsSkipsNoQuotaAndErrors(t *testing.T) {
	svc, rec := newDiskTestService(t)
	ctx := context.Background()
	tenant, _, err := svc.CreateTenant(ctx, CreateTenantInput{Username: "skipme"}, AuditContext{})
	if err != nil {
		t.Fatal(err)
	}
	rec.usage["skipme"] = 9 * gib
	svc.enforceDiskLimits(ctx) // no limit set: nothing happens
	fresh, _ := svc.GetTenant(tenant.ID)
	if fresh.Status != domain.StatusRunning {
		t.Fatalf("tenant without a limit was stopped: %s", fresh.Status)
	}

	quota := 1
	if _, err := svc.UpdateTenant(ctx, tenant.ID, UpdateTenantInput{DiskQuotaGB: &quota}, AuditContext{}); err != nil {
		t.Fatal(err)
	}
	rec.usageErr = errors.New("du failed")
	svc.enforceDiskLimits(ctx)
	fresh, _ = svc.GetTenant(tenant.ID)
	if fresh.Status != domain.StatusRunning {
		t.Fatalf("measurement failure must not stop anything: %s", fresh.Status)
	}
}

func TestRestoreSuspendedSSHClearsNote(t *testing.T) {
	svc, _ := newDiskTestService(t)
	tenant := &domain.Tenant{LastError: "别的错误"}
	svc.clearSuspensionNotes(tenant)
	if tenant.LastError != "别的错误" {
		t.Fatal("unrelated errors must be left alone")
	}

	tenant.LastError = diskSuspendNote + "。没有 SSH"
	svc.clearSuspensionNotes(tenant)
	if tenant.LastError != "" {
		t.Fatalf("error = %q", tenant.LastError)
	}

	tenant.LastError = diskSuspendNote + "。" + diskSSHLockedNote
	svc.clearSuspensionNotes(tenant)
	if !strings.Contains(tenant.LastError, "重置密码") {
		t.Fatalf("error = %q", tenant.LastError)
	}
}

func TestStartTenantBringsBackSuspendedSSH(t *testing.T) {
	svc, _ := newDiskTestService(t)
	ctx := context.Background()
	tenant, _, err := svc.CreateTenant(ctx, CreateTenantInput{Username: "resume"}, AuditContext{})
	if err != nil {
		t.Fatal(err)
	}
	tenant.DiskQuotaGB = 1
	tenant.DiskBytes = 2 * gib
	svc.enforceDiskLimit(ctx, tenant)

	// StartTenant clears the error, so keep what the suspension recorded.
	if !strings.Contains(tenant.LastError, diskSSHLockedNote) {
		t.Fatalf("suspension should lock SSH: %q", tenant.LastError)
	}
	started, err := svc.StartTenant(ctx, tenant.ID, AuditContext{})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	// The instance is running again, and the suspension explanation is gone.
	if started.Status != domain.StatusRunning || started.LastError != "" {
		t.Fatalf("started = %+v", started)
	}
}

func TestBulkQuotaValidationAndApply(t *testing.T) {
	svc, _ := newDiskTestService(t)
	ctx := context.Background()
	a, _, err := svc.CreateTenant(ctx, CreateTenantInput{Username: "bulka"}, AuditContext{})
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := svc.CreateTenant(ctx, CreateTenantInput{Username: "bulkb"}, AuditContext{})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := svc.Bulk(ctx, BulkRequest{Action: "quota", IDs: []int64{a.ID}, CPUQuota: "x"}, AuditContext{}); err == nil {
		t.Fatal("bad CPU quota must be rejected before starting a job")
	}
	if _, err := svc.Bulk(ctx, BulkRequest{Action: "nope", IDs: []int64{a.ID}}, AuditContext{}); err == nil {
		t.Fatal("unknown action must be rejected")
	}

	job, err := svc.Bulk(ctx, BulkRequest{
		Action: "quota", IDs: []int64{a.ID, b.ID},
		CPUQuota: "150%", MemoryMax: "2G", TasksMax: 256,
	}, AuditContext{Actor: "tester"})
	if err != nil {
		t.Fatalf("bulk quota: %v", err)
	}
	waitJobFinished(t, svc, job.ID)
	for _, id := range []int64{a.ID, b.ID} {
		got, _ := svc.GetTenant(id)
		if got.CPUQuota != "150%" || got.MemoryMax != "2G" || got.TasksMax != 256 {
			t.Fatalf("tenant %d quota = %+v", id, got)
		}
	}
}

func TestBulkResetPasswordWritesDownload(t *testing.T) {
	svc, _ := newDiskTestService(t)
	ctx := context.Background()
	a, _, err := svc.CreateTenant(ctx, CreateTenantInput{Username: "resa"}, AuditContext{})
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := svc.CreateTenant(ctx, CreateTenantInput{Username: "resb"}, AuditContext{})
	if err != nil {
		t.Fatal(err)
	}
	job, err := svc.Bulk(ctx, BulkRequest{Action: "reset-password", IDs: []int64{a.ID, b.ID, 9999}}, AuditContext{})
	if err != nil {
		t.Fatalf("bulk reset: %v", err)
	}
	waitJobFinished(t, svc, job.ID)
	data, ok := svc.Artifact(job.ID, "passwords.csv")
	if !ok {
		t.Fatal("passwords.csv was not written")
	}
	csv := string(data)
	if !strings.Contains(csv, "resa") || !strings.Contains(csv, "resb") {
		t.Fatalf("csv = %q", csv)
	}
	finished, err := svc.Store.GetJob(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if finished.Status != domain.JobPartial {
		t.Fatalf("status = %s, want partial because one id does not exist", finished.Status)
	}
}

// waitJobFinished blocks until the job reaches any terminal state.
func waitJobFinished(t *testing.T, svc *Service, id int64) *domain.Job {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		job, err := svc.Job(id)
		if err == nil && (job.Status == domain.JobSucceeded || job.Status == domain.JobFailed || job.Status == domain.JobPartial) {
			return job
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("job %d never finished", id)
	return nil
}
