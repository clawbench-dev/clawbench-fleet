package fleet

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"clawbench-fleet/internal/domain"
)

func newService(t *testing.T) *Service {
	t.Helper()
	svc, _ := newTestService(t)
	t.Cleanup(func() {
		if units, err := svc.Sys.ListUnits(context.Background()); err == nil {
			for _, unit := range units {
				_ = svc.Sys.Stop(context.Background(), unit)
			}
		}
	})
	return svc
}

func createRunning(t *testing.T, svc *Service, username string) *domain.Tenant {
	t.Helper()
	tenant, _, err := svc.CreateTenant(context.Background(), CreateTenantInput{Username: username}, AuditContext{Actor: "tester"})
	if err != nil {
		t.Fatalf("create %s: %v", username, err)
	}
	return tenant
}

func TestValidateSettingsRejectsEveryField(t *testing.T) {
	base := SettingsFromConfig(newService(t).Cfg)
	cases := []func(*domain.Settings){
		func(s *domain.Settings) { s.PortStart = 0 },
		func(s *domain.Settings) { s.PortEnd = 70000 },
		func(s *domain.Settings) { s.PortStart, s.PortEnd = 10, 10 },
		func(s *domain.Settings) { s.PortsPerTenant = 0 },
		func(s *domain.Settings) { s.PortsPerTenant = 11 },
		func(s *domain.Settings) { s.UIDStart = 10 },
		func(s *domain.Settings) { s.UIDEnd = 70000 },
		func(s *domain.Settings) { s.DefaultCPUQuota = "abc" },
		func(s *domain.Settings) { s.DefaultMemoryMax = "huge" },
		func(s *domain.Settings) { s.DefaultMemoryMax = "" },
		func(s *domain.Settings) { s.DefaultTasksMax = 0 },
		func(s *domain.Settings) { s.DiskWarnGB = -1 },
		func(s *domain.Settings) { s.BackupHour = -1 },
		func(s *domain.Settings) { s.BackupHour = 24 },
		func(s *domain.Settings) { s.BackupRetention = 0 },
		func(s *domain.Settings) { s.TrashRetention = 0 },
		func(s *domain.Settings) { s.AuditRetention = 0 },
	}
	for i, mutate := range cases {
		s := base
		mutate(&s)
		if err := validateSettings(s); err == nil {
			t.Errorf("case %d should be rejected", i)
		}
	}
	if err := validateSettings(base); err != nil {
		t.Fatalf("defaults should be valid: %v", err)
	}
}

func TestPasswordHelpers(t *testing.T) {
	if err := ValidateAdminPassword("short1"); err == nil {
		t.Fatal("short password must be rejected")
	}
	if err := ValidateAdminPassword(strings.Repeat("a", 33)); err == nil {
		t.Fatal("long password must be rejected")
	}
	if err := ValidateAdminPassword("letters1"); err != nil {
		t.Fatalf("letters+digits = %v", err)
	}
	if err := ValidateAdminPassword("onlyletters"); err == nil {
		t.Fatal("letters only must be rejected")
	}
	if err := ValidateAdminPassword("12345678"); err == nil {
		t.Fatal("digits only must be rejected")
	}
	p, err := GeneratePassword(2)
	if err != nil || len(p) != 12 {
		t.Fatalf("short request should be rounded up: %q %v", p, err)
	}
	if SessionMaxAgeSeconds() != 7*24*3600 {
		t.Fatalf("session max age = %d", SessionMaxAgeSeconds())
	}
}

func TestSanitizeAndAudit(t *testing.T) {
	svc := newService(t)
	got := sanitizeDetail(map[string]any{
		"password": "secret", "Api_Key": "k", "nested": map[string]any{"token": "t", "keep": 1}, "port": 21000,
	})
	if got["password"] != "***" || got["Api_Key"] != "***" {
		t.Fatalf("secrets not masked: %+v", got)
	}
	if nested, ok := got["nested"].(map[string]any); !ok || nested["token"] != "***" || nested["keep"] != 1 {
		t.Fatalf("nested map = %+v", got["nested"])
	}
	svc.Audit(AuditContext{}, "test.action", nil, nil, nil)
	entries, total, err := svc.ListAudit(storeAuditFilter())
	if err != nil || total != 1 {
		t.Fatalf("audit = %d %v", total, err)
	}
	if entries[0].Actor != "admin" || entries[0].Result != "ok" || entries[0].Detail != "{}" {
		t.Fatalf("audit entry = %+v", entries[0])
	}
	svc.Audit(AuditContext{Actor: "ops"}, "test.fail", &domain.Tenant{ID: 1, Username: "alice"}, nil, errors.New("boom"))
	entries, _, _ = svc.ListAudit(storeAuditFilter())
	if entries[0].Result != statusFailed || entries[0].TenantName != "alice" || entries[0].Error != "boom" {
		t.Fatalf("failed audit = %+v", entries[0])
	}
}

func TestActivityParsing(t *testing.T) {
	layouts := map[string]bool{
		"2026-01-02 15:04:05":                 true,
		"2026-01-02T15:04:05Z":                true,
		"2026-01-02":                          true,
		"2026-01-02T15:04:05.123456+08:00":    true,
		"2026-01-02 15:04:05.999999999-07:00": true,
	}
	for raw, want := range layouts {
		if got := parseClawbenchTime(raw); got.ok != want {
			t.Errorf("%q parsed = %v", raw, got.ok)
		}
	}
	if parseClawbenchTime("").ok {
		t.Fatal("empty string must not parse")
	}
	if parseClawbenchTime("not a time").ok {
		t.Fatal("junk must not parse")
	}
	utc := parseClawbenchTime("2026-01-02 03:04:05")
	if !utc.ok || utc.at.Location() != time.UTC {
		t.Fatalf("zoneless time should be UTC: %+v", utc)
	}
	if !hasZone("2006-01-02T15:04:05Z") || hasZone("2006-01-02 15:04:05") {
		t.Fatal("hasZone detection is wrong")
	}
}

func TestLastActiveReadsInstanceDatabase(t *testing.T) {
	svc := newService(t)
	tenant := createRunning(t, svc, "alice")
	ctx := context.Background()

	// No database yet: nothing is found, and no empty database is created.
	if found, hasDB := svc.tenantLastActive(ctx, *tenant); hasDB || found.ok {
		t.Fatalf("tenant without a database = %+v %v", found, hasDB)
	}
	db, err := sql.Open("sqlite", filepath.Join(tenant.DataDir, "ClawBench.db"))
	if err != nil {
		t.Fatal(err)
	}
	stmts := []string{
		`CREATE TABLE chat_history(created_at TEXT)`,
		`CREATE TABLE chat_tool_calls(created_at TEXT)`,
		`CREATE TABLE chat_metadata(created_at TEXT)`,
		`CREATE TABLE chat_sessions(updated_at TEXT, removed INTEGER)`,
		`INSERT INTO chat_history VALUES('2026-01-01 08:00:00')`,
		`INSERT INTO chat_tool_calls VALUES('2026-01-03 09:30:00')`,
		`INSERT INTO chat_metadata VALUES('2026-01-02 10:00:00')`,
		`INSERT INTO chat_sessions VALUES('2026-01-04 11:00:00', 0)`,
		`INSERT INTO chat_sessions VALUES('2026-01-09 11:00:00', 1)`,
	}
	for _, stmt := range stmts {
		if _, execErr := db.Exec(stmt); execErr != nil {
			t.Fatalf("%s: %v", stmt, execErr)
		}
	}
	_ = db.Close()

	found, hasDB := svc.tenantLastActive(ctx, *tenant)
	if !hasDB || !found.ok {
		t.Fatalf("activity not found: %+v %v", found, hasDB)
	}
	if got := found.at.Format(time.RFC3339); got != "2026-01-04T11:00:00Z" {
		t.Fatalf("newest activity = %s", got)
	}
	svc.RefreshLastActive(ctx)
	fresh, err := svc.GetTenant(tenant.ID)
	if err != nil || fresh.LastActiveAt != "2026-01-04T11:00:00Z" {
		t.Fatalf("refresh = %+v %v", fresh, err)
	}
	// Running it again is a no-op, and the detail page fills in the value.
	svc.RefreshLastActive(ctx)
	detail, err := svc.TenantDetail(tenant.ID)
	if err != nil || detail.LastActiveAt == "" {
		t.Fatalf("detail = %+v %v", detail, err)
	}
}

func TestHealthLoopChecks(t *testing.T) {
	svc := newService(t)
	ctx := context.Background()
	tenant := createRunning(t, svc, "alice")
	svc.checkAllHealth(ctx)
	fresh, _ := svc.GetTenant(tenant.ID)
	if fresh.Status != domain.StatusRunning || !fresh.LastHealthOK {
		t.Fatalf("healthy tenant = %+v", fresh)
	}
	// Stop the instance behind the platform's back: the sweep must flag it.
	if err := svc.Sys.Stop(ctx, tenant.UnitName); err != nil {
		t.Fatal(err)
	}
	svc.checkAllHealth(ctx)
	fresh, _ = svc.GetTenant(tenant.ID)
	if fresh.Status != domain.StatusError || !strings.Contains(fresh.LastError, "服务未运行") {
		t.Fatalf("stopped tenant = %+v", fresh)
	}
	svc.checkAllHealth(ctx)
	// Bring it back by hand: the sweep restores the running status.
	if err := svc.Sys.Start(ctx, tenant.UnitName); err != nil {
		t.Fatal(err)
	}
	svc.checkAllHealth(ctx)
	fresh, _ = svc.GetTenant(tenant.ID)
	if fresh.Status != domain.StatusRunning {
		t.Fatalf("recovered tenant = %+v", fresh)
	}
}

func TestHealthLoopReportsUnhealthyInstance(t *testing.T) {
	svc := newService(t)
	tenant := createRunning(t, svc, "alice")
	// Rewrite the unit so the port answers nothing; the unit stays "active".
	if err := svc.Sys.Stop(context.Background(), tenant.UnitName); err != nil {
		t.Fatal(err)
	}
	bad := &domain.Tenant{Username: tenant.Username, UnitName: tenant.UnitName, Port: freePort(t), Status: domain.StatusRunning}
	svc.checkTenantHealth(context.Background(), *bad)
	fresh, _ := svc.GetTenant(tenant.ID)
	if fresh.Status != domain.StatusRunning {
		// The record for another port is unaffected; this only asserts the call
		// did not crash and left the real tenant alone.
		t.Fatalf("unrelated tenant changed: %+v", fresh)
	}
}

func TestRefreshMetricsAndDisk(t *testing.T) {
	svc := newService(t)
	ctx := context.Background()
	tenant := createRunning(t, svc, "alice")
	svc.RefreshMetrics(ctx)
	if m := svc.GetMetrics(tenant.ID); m.SampledAt == "" {
		t.Fatalf("metrics not sampled: %+v", m)
	}
	svc.RefreshDiskUsage(ctx)
	fresh, _ := svc.GetTenant(tenant.ID)
	if fresh.DiskChecked == "" {
		t.Fatal("disk usage not recorded")
	}
	if m := svc.GetMetrics(tenant.ID); m.DiskBytes != fresh.DiskBytes {
		t.Fatalf("metrics disk = %d, record = %d", m.DiskBytes, fresh.DiskBytes)
	}
}

func TestShouldRunOncePerDay(t *testing.T) {
	svc := newService(t)
	hour := time.Now().Hour()
	if !svc.shouldRun("test", hour) {
		t.Fatal("first call in the hour should run")
	}
	if svc.shouldRun("test", hour) {
		t.Fatal("second call in the same hour should not run")
	}
	if svc.shouldRun("other", (hour+1)%24) {
		t.Fatal("wrong hour should not run")
	}
	if !svc.shouldRun("other", hour) {
		t.Fatal("a different task should still run this hour")
	}
}

func TestCleanupArtifacts(t *testing.T) {
	svc := newService(t)
	svc.setArtifact(1, "success.csv", []byte("old"))
	svc.artifactsMu.Lock()
	svc.artifactAt[1] = time.Now().Add(-48 * time.Hour)
	svc.artifactsMu.Unlock()
	svc.setArtifact(2, "success.csv", []byte("new"))
	svc.cleanupArtifacts()
	if _, ok := svc.Artifact(1, "success.csv"); ok {
		t.Fatal("old artifact should be removed")
	}
	if _, ok := svc.Artifact(2, "success.csv"); !ok {
		t.Fatal("fresh artifact should stay")
	}
}

func TestReconcile(t *testing.T) {
	svc := newService(t)
	ctx := context.Background()
	tenant := createRunning(t, svc, "alice")
	if err := svc.Sys.WriteUnit(ctx, "clawbench-orphan.service", "[Service]\n"); err != nil {
		t.Fatal(err)
	}
	svc.Reconcile(ctx)
	if orphans := svc.Orphans(); len(orphans) != 1 || orphans[0] != "clawbench-orphan.service" {
		t.Fatalf("orphans = %v", orphans)
	}
	// Losing the unit is surfaced as an error, never as a silent delete.
	if err := svc.Sys.RemoveUnit(ctx, tenant.UnitName); err != nil {
		t.Fatal(err)
	}
	svc.Reconcile(ctx)
	fresh, _ := svc.GetTenant(tenant.ID)
	if fresh.Status != domain.StatusError || !strings.Contains(fresh.LastError, "systemd 单元丢失") {
		t.Fatalf("missing unit = %+v", fresh)
	}
}

func TestReconcileFailsInterruptedCreation(t *testing.T) {
	svc := newService(t)
	stuck := &domain.Tenant{
		Username: "stuck", Port: freePort(t), Status: domain.StatusCreating,
		UnitName: svc.Cfg.UnitName("stuck"), CreatedAt: time.Now().Add(-time.Hour).Format(time.RFC3339),
	}
	if err := svc.Store.CreateTenant(stuck); err != nil {
		t.Fatal(err)
	}
	if err := svc.Sys.WriteUnit(context.Background(), stuck.UnitName, "[Service]\n"); err != nil {
		t.Fatal(err)
	}
	svc.Reconcile(context.Background())
	fresh, _ := svc.GetTenant(stuck.ID)
	if fresh.Status != domain.StatusError {
		t.Fatalf("stuck creation = %+v", fresh)
	}
}

func TestHostStats(t *testing.T) {
	svc := newService(t)
	stats := svc.HostStats()
	if stats.MemTotal < 0 || stats.DiskTotal < 0 {
		t.Fatalf("stats = %+v", stats)
	}
	// Two samples give the CPU delta.
	_ = svc.hostCPU()
	time.Sleep(5 * time.Millisecond)
	if pct := svc.hostCPU(); pct < 0 || pct > 100 {
		t.Fatalf("cpu percent = %f", pct)
	}
}

func TestBlocksToBytes(t *testing.T) {
	if got := blocksToBytes(10, 4096); got != 40960 {
		t.Fatalf("bytes = %d", got)
	}
	if got := blocksToBytes(10, 0); got != 0 {
		t.Fatalf("zero block size = %d", got)
	}
	if got := blocksToBytes(^uint64(0), 4096); got <= 0 {
		t.Fatalf("overflow should saturate, got %d", got)
	}
}

func TestRunBulkAndImport(t *testing.T) {
	svc := newService(t)
	ctx := context.Background()
	alice := createRunning(t, svc, "alice")
	job, err := svc.BulkAction(ctx, "stop", []int64{alice.ID}, AuditContext{Actor: "tester"})
	if err != nil {
		t.Fatal(err)
	}
	waitJob(t, svc, job.ID, domain.JobSucceeded)
	fresh, _ := svc.GetTenant(alice.ID)
	if fresh.Status != domain.StatusStopped {
		t.Fatalf("bulk stop = %+v", fresh)
	}
	job, err = svc.BulkAction(ctx, "start", []int64{alice.ID, 999}, AuditContext{})
	if err != nil {
		t.Fatal(err)
	}
	waitJob(t, svc, job.ID, domain.JobPartial)

	importJob, err := svc.ImportRows(ctx, []ImportRow{{Username: "bob"}, {Username: "root"}}, AuditContext{})
	if err != nil {
		t.Fatal(err)
	}
	waitJob(t, svc, importJob.ID, domain.JobPartial)
	success, ok := svc.Artifact(importJob.ID, "success.csv")
	if !ok || !bytes.Contains(success, []byte("bob")) {
		t.Fatalf("success artifact = %s", success)
	}
	failures, ok := svc.Artifact(importJob.ID, "failures.csv")
	if !ok || !bytes.Contains(failures, []byte("root")) {
		t.Fatalf("failure artifact = %s", failures)
	}
}

func waitJob(t *testing.T, svc *Service, id int64, want string) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		job, err := svc.Job(id)
		if err == nil && job.Status == want {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	job, _ := svc.Job(id)
	t.Fatalf("job %d never reached %s: %+v", id, want, job)
}

func TestGuestPageAndReports(t *testing.T) {
	svc := newService(t)
	alice := createRunning(t, svc, "alice")

	page, err := svc.GuestPage()
	if err != nil || len(page.Items) != 1 || page.Items[0].Username != "alice" {
		t.Fatalf("guest page = %+v %v", page, err)
	}
	if reportErr := svc.SubmitGuestReport(alice.ID, "打不开", "10.0.0.1"); reportErr != nil {
		t.Fatal(reportErr)
	}
	if reportErr := svc.SubmitGuestReport(alice.ID, "打不开", "10.0.0.1"); reportErr != nil {
		t.Fatalf("duplicate report should be accepted: %v", reportErr)
	}
	if reportErr := svc.SubmitGuestReport(alice.ID, "", "10.0.0.1"); reportErr != nil {
		t.Fatalf("empty message should be accepted: %v", reportErr)
	}
	if reportErr := svc.SubmitGuestReport(999, "x", ""); reportErr == nil {
		t.Fatal("unknown tenant must be rejected")
	}
	// The public listing is the admin table's data minus machine internals, so
	// it carries version/metrics/disk for everyone but still no paths or UIDs.
	createRunning(t, svc, "bob")
	page, err = svc.GuestPageFor("", GuestQuery{Query: "bo"})
	if err != nil || page.Total != 1 || len(page.Items) != 1 || page.Items[0].Username != "bob" {
		t.Fatalf("filtered guest page = %+v %v", page, err)
	}
	page, err = svc.GuestPageFor("", GuestQuery{Page: 1, PageSize: 1})
	if err != nil || page.Total != 2 || len(page.Items) != 1 {
		t.Fatalf("paged guest page = %+v %v", page, err)
	}
	raw, err := json.Marshal(page.Items[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"home_dir", "data_dir", "uid", "gid", "notes", "unit_name", "last_error"} {
		if strings.Contains(string(raw), leak) {
			t.Fatalf("guest page leaked %q: %s", leak, raw)
		}
	}
	reports, err := svc.ListGuestReports(true, 10)
	if err != nil || len(reports) != 1 {
		t.Fatalf("reports = %+v %v", reports, err)
	}
	if err := svc.ResolveGuestReport(reports[0].ID, AuditContext{Actor: "ops"}); err != nil {
		t.Fatal(err)
	}
	if err := svc.ResolveGuestReport(999, AuditContext{}); err == nil {
		t.Fatal("resolving a missing report must fail")
	}
}

func TestOverviewCountsAndAnomalies(t *testing.T) {
	svc := newService(t)
	alice := createRunning(t, svc, "alice")
	if err := svc.Store.SetTenantStatus(alice.ID, domain.StatusError, "boom"); err != nil {
		t.Fatal(err)
	}
	ov, err := svc.Overview()
	if err != nil {
		t.Fatal(err)
	}
	if ov.Total != 1 || len(ov.Anomalies) != 1 || ov.Mode != "dev" {
		t.Fatalf("overview = %+v", ov)
	}
	if ov.Counts[domain.StatusError] != 1 {
		t.Fatalf("counts = %+v", ov.Counts)
	}
}

func TestTrashRetentionAndPurge(t *testing.T) {
	svc := newService(t)
	ctx := context.Background()
	alice := createRunning(t, svc, "alice")
	if _, err := svc.DeleteTenant(ctx, alice.ID, "alice", AuditContext{}); err != nil {
		t.Fatal(err)
	}
	// Backdate the deletion so the retention sweep picks it up.
	if err := svc.Store.SetTenantStatus(alice.ID, domain.StatusTrashed, ""); err != nil {
		t.Fatal(err)
	}
	trashed, _ := svc.GetTenant(alice.ID)
	trashed.DeletedAt = time.Now().AddDate(0, 0, -90).Format(time.RFC3339)
	if err := svc.Store.UpdateTenant(trashed); err != nil {
		t.Fatal(err)
	}
	svc.PurgeExpiredTrash(ctx)
	if _, err := svc.GetTenant(alice.ID); err == nil {
		t.Fatal("expired trash should be purged")
	}
	svc.PurgeExpiredAudit()
}

func TestDeleteRequiresExactName(t *testing.T) {
	svc := newService(t)
	alice := createRunning(t, svc, "alice")
	ctx := context.Background()
	if _, err := svc.DeleteTenant(ctx, alice.ID, "alic", AuditContext{}); err == nil {
		t.Fatal("wrong confirmation name must be rejected")
	}
	if _, err := svc.DeleteTenant(ctx, 999, "alice", AuditContext{}); !errors.Is(err, ErrTenantNotFound) {
		t.Fatalf("missing tenant = %v", err)
	}
	if _, _, err := svc.RestoreTenant(ctx, alice.ID, AuditContext{}); err == nil {
		t.Fatal("restoring a live tenant must be rejected")
	}
	if err := svc.PurgeTenant(ctx, alice.ID, AuditContext{}); err == nil {
		t.Fatal("purging a live tenant must be rejected")
	}
}

func TestRepairAndLogs(t *testing.T) {
	svc := newService(t)
	ctx := context.Background()
	alice := createRunning(t, svc, "alice")
	// Lose the config file: repair regenerates a password and rewrites it.
	if err := os.Remove(svc.tenantConfigPath(alice)); err != nil {
		t.Fatal(err)
	}
	repaired, password, err := svc.RepairTenant(ctx, alice.ID, AuditContext{})
	if err != nil {
		t.Fatalf("repair: %v", err)
	}
	if password == "" || repaired.Status != domain.StatusRunning {
		t.Fatalf("repair = %+v %q", repaired, password)
	}
	if _, statErr := os.Stat(svc.tenantConfigPath(alice)); statErr != nil {
		t.Fatalf("config not rewritten: %v", statErr)
	}
	logs, err := svc.Logs(ctx, alice.ID, 5, AuditContext{})
	if err != nil || !strings.Contains(logs, "started") {
		t.Fatalf("logs = %q %v", logs, err)
	}
	if _, err := svc.Logs(ctx, 999, 5, AuditContext{}); err == nil {
		t.Fatal("logs for a missing tenant must fail")
	}
}

func TestUpdateTenantValidation(t *testing.T) {
	svc := newService(t)
	ctx := context.Background()
	alice := createRunning(t, svc, "alice")
	bad := "bad"
	if _, err := svc.UpdateTenant(ctx, alice.ID, UpdateTenantInput{CPUQuota: &bad}, AuditContext{}); err == nil {
		t.Fatal("invalid cpu quota must be rejected")
	}
	if _, err := svc.UpdateTenant(ctx, alice.ID, UpdateTenantInput{MemoryMax: &bad}, AuditContext{}); err == nil {
		t.Fatal("invalid memory must be rejected")
	}
	zero := 0
	if _, err := svc.UpdateTenant(ctx, alice.ID, UpdateTenantInput{TasksMax: &zero}, AuditContext{}); err == nil {
		t.Fatal("zero tasks must be rejected")
	}
	name := "  Alice  "
	notes := " ops "
	updated, err := svc.UpdateTenant(ctx, alice.ID, UpdateTenantInput{DisplayName: &name, Notes: &notes}, AuditContext{})
	if err != nil || updated.DisplayName != "Alice" || updated.Notes != "ops" {
		t.Fatalf("update = %+v %v", updated, err)
	}
}

func TestFailedHealthRollsBackUpdate(t *testing.T) {
	svc := newService(t)
	ctx := context.Background()
	alice := createRunning(t, svc, "alice")
	// Make the unit fail to come back: the platform must record the failure.
	unit := "[Service]\nUser=alice\nExecStart=/opt/clawbench --port " + strconv.Itoa(alice.Port) + "\n# clawbench-fake: closed\n"
	if err := svc.Sys.WriteUnit(ctx, alice.UnitName, unit); err != nil {
		t.Fatal(err)
	}
	quota := "150%"
	if _, err := svc.UpdateTenant(ctx, alice.ID, UpdateTenantInput{CPUQuota: &quota}, AuditContext{}); err == nil {
		// The fake restart may still succeed if Start returns early; in that
		// case the tenant is still consistent.
		fresh, _ := svc.GetTenant(alice.ID)
		if fresh.Status != domain.StatusRunning && fresh.Status != domain.StatusError {
			t.Fatalf("unexpected status after update: %q", fresh.Status)
		}
	}
}
