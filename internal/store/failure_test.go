package store

import (
	"path/filepath"
	"testing"

	"clawbench-fleet/internal/domain"
)

func closedStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "fleet.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	return st
}

func TestClosedStorePortErrors(t *testing.T) {
	st := closedStore(t)
	if err := st.SeedPorts(21000, 21010); err == nil {
		t.Fatal("seed on a closed store should fail")
	}
	if _, err := st.AllocatePorts(21000, 21010, 1, 0); err == nil {
		t.Fatal("allocate on a closed store should fail")
	}
	if err := st.ReservePorts(1); err == nil {
		t.Fatal("reserve on a closed store should fail")
	}
	if err := st.SetPortsAllocated(1); err == nil {
		t.Fatal("set allocated on a closed store should fail")
	}
	if err := st.ReleasePorts(1); err == nil {
		t.Fatal("release on a closed store should fail")
	}
	if err := st.AssignPorts(1, []int{21000}); err == nil {
		t.Fatal("assign on a closed store should fail")
	}
	if _, err := st.PortsForTenant(1); err == nil {
		t.Fatal("ports for tenant on a closed store should fail")
	}
	if err := st.ClaimPort(21000, 1); err == nil {
		t.Fatal("claim on a closed store should fail")
	}
	if _, err := st.PortState(21000); err == nil {
		t.Fatal("port state on a closed store should fail")
	}
	if _, err := st.FreePortGroups(21000, 21010, 1); err == nil {
		t.Fatal("free groups on a closed store should fail")
	}
}

func TestClosedStoreGuestErrors(t *testing.T) {
	st := closedStore(t)
	if err := st.CreateGuestReport(&domain.GuestReport{TenantID: 1}); err == nil {
		t.Fatal("create report on a closed store should fail")
	}
	if _, err := st.ListGuestReports(true, 10); err == nil {
		t.Fatal("list reports on a closed store should fail")
	}
	if _, err := st.GetGuestReport(1); err == nil {
		t.Fatal("get report on a closed store should fail")
	}
	if _, err := st.CountOpenGuestReports(); err == nil {
		t.Fatal("count reports on a closed store should fail")
	}
	if _, err := st.HasOpenGuestReportSince(1, "2026-01-01T00:00:00Z"); err == nil {
		t.Fatal("open report lookup on a closed store should fail")
	}
	if err := st.ResolveGuestReport(1, "admin", "2026-01-01T00:00:00Z"); err == nil {
		t.Fatal("resolve report on a closed store should fail")
	}
}

func TestClosedStoreVersionErrors(t *testing.T) {
	st := closedStore(t)
	if err := st.CreateVersion(&domain.Version{Version: "1.0.0"}); err == nil {
		t.Fatal("create version on a closed store should fail")
	}
	if _, err := st.ListVersions(); err == nil {
		t.Fatal("list versions on a closed store should fail")
	}
	if _, err := st.GetVersion("1.0.0"); err == nil {
		t.Fatal("get version on a closed store should fail")
	}
	if _, err := st.CurrentVersion(); err == nil {
		t.Fatal("current version on a closed store should fail")
	}
	if _, err := st.PreviousVersion(); err == nil {
		t.Fatal("previous version on a closed store should fail")
	}
	if err := st.SetCurrentVersion("1.0.0"); err == nil {
		t.Fatal("set current on a closed store should fail")
	}
	if err := st.DeleteVersion("1.0.0"); err == nil {
		t.Fatal("delete version on a closed store should fail")
	}
}

func TestClosedStoreTenantErrors(t *testing.T) {
	st := closedStore(t)
	if err := st.CreateTenant(newTenant("alice", 21001, domain.StatusRunning)); err == nil {
		t.Fatal("create tenant on a closed store should fail")
	}
	if _, err := st.GetTenant(1); err == nil {
		t.Fatal("get tenant on a closed store should fail")
	}
	if _, err := st.GetTenantByUsername("alice"); err == nil {
		t.Fatal("get by username on a closed store should fail")
	}
	if err := st.UpdateTenant(newTenant("alice", 21001, domain.StatusRunning)); err == nil {
		t.Fatal("update tenant on a closed store should fail")
	}
	if _, _, err := st.ListTenants(TenantFilter{}); err == nil {
		t.Fatal("list tenants on a closed store should fail")
	}
	if _, err := st.ListTenantsByStatus(domain.StatusRunning); err == nil {
		t.Fatal("list by status on a closed store should fail")
	}
	if err := st.DeleteTenant(1); err == nil {
		t.Fatal("delete tenant on a closed store should fail")
	}
	if _, err := st.AllTenantUIDs(); err == nil {
		t.Fatal("uids on a closed store should fail")
	}
	if err := st.SetTenantStatus(1, domain.StatusRunning, ""); err == nil {
		t.Fatal("set status on a closed store should fail")
	}
	if err := st.SetTenantHealth(1, true, "1.0.0", ""); err == nil {
		t.Fatal("set health on a closed store should fail")
	}
	if err := st.SetTenantDisk(1, 1, ""); err == nil {
		t.Fatal("set disk on a closed store should fail")
	}
	if err := st.SetTenantVersion(1, "1.0.0"); err == nil {
		t.Fatal("set version on a closed store should fail")
	}
	if err := st.SetTenantLastActive(1, ""); err == nil {
		t.Fatal("set last active on a closed store should fail")
	}
	if _, err := st.CountByStatus(); err == nil {
		t.Fatal("count by status on a closed store should fail")
	}
	if _, err := st.TrashedTenants(); err == nil {
		t.Fatal("trashed tenants on a closed store should fail")
	}
}

func TestClosedStoreRecordErrors(t *testing.T) {
	st := closedStore(t)
	if _, err := st.CreateAdmin("admin", "hash"); err == nil {
		t.Fatal("create admin on a closed store should fail")
	}
	if _, err := st.GetAdminByID(1); err == nil {
		t.Fatal("get admin on a closed store should fail")
	}
	if _, err := st.GetAdminByUsername("admin"); err == nil {
		t.Fatal("get admin by name on a closed store should fail")
	}
	if err := st.UpdateAdminPassword(1, "hash"); err == nil {
		t.Fatal("update password on a closed store should fail")
	}
	if err := st.TouchAdminLogin(1); err == nil {
		t.Fatal("touch login on a closed store should fail")
	}
	if err := st.CreateSession(&domain.Session{TokenHash: "t"}); err == nil {
		t.Fatal("create session on a closed store should fail")
	}
	if _, err := st.GetSession("t"); err == nil {
		t.Fatal("get session on a closed store should fail")
	}
	if err := st.DeleteSession("t"); err == nil {
		t.Fatal("delete session on a closed store should fail")
	}
	if err := st.DeleteSessionsForAdmin(1); err == nil {
		t.Fatal("delete sessions on a closed store should fail")
	}
	if _, err := st.PurgeExpiredSessions(""); err == nil {
		t.Fatal("purge sessions on a closed store should fail")
	}
	if err := st.CreateBackup(&domain.Backup{TenantID: 1}); err == nil {
		t.Fatal("create backup on a closed store should fail")
	}
	if _, err := st.ListBackups(1); err == nil {
		t.Fatal("list backups on a closed store should fail")
	}
	if _, err := st.GetBackup(1); err == nil {
		t.Fatal("get backup on a closed store should fail")
	}
	if err := st.DeleteBackupRecord(1); err == nil {
		t.Fatal("delete backup on a closed store should fail")
	}
	if _, err := st.BackupsOlderThan(1, ""); err == nil {
		t.Fatal("old backups on a closed store should fail")
	}
	if _, err := st.CreateJob("x", "{}", 1); err == nil {
		t.Fatal("create job on a closed store should fail")
	}
	if _, err := st.GetJob(1); err == nil {
		t.Fatal("get job on a closed store should fail")
	}
	if err := st.UpdateJobProgress(1, 1, 0); err == nil {
		t.Fatal("update progress on a closed store should fail")
	}
	if err := st.MarkJobRunning(1); err == nil {
		t.Fatal("mark running on a closed store should fail")
	}
	if err := st.FinishJob(1, "ok", "{}"); err == nil {
		t.Fatal("finish job on a closed store should fail")
	}
	if _, err := st.AddJobItem(1, 1, "a"); err == nil {
		t.Fatal("add item on a closed store should fail")
	}
	if err := st.UpdateJobItem(1, "ok", ""); err == nil {
		t.Fatal("update item on a closed store should fail")
	}
	if _, err := st.ListJobItems(1); err == nil {
		t.Fatal("list items on a closed store should fail")
	}
	if _, err := st.RecentJobs(1); err == nil {
		t.Fatal("recent jobs on a closed store should fail")
	}
	if _, err := st.MarkInterruptedJobs(); err == nil {
		t.Fatal("mark interrupted on a closed store should fail")
	}
	if err := st.AddAudit(&domain.AuditEntry{}); err == nil {
		t.Fatal("add audit on a closed store should fail")
	}
	if _, _, err := st.ListAudit(AuditFilter{}); err == nil {
		t.Fatal("list audit on a closed store should fail")
	}
	if _, err := st.RecentAudit(1); err == nil {
		t.Fatal("recent audit on a closed store should fail")
	}
	if _, err := st.PurgeAuditBefore(""); err == nil {
		t.Fatal("purge audit on a closed store should fail")
	}
	if err := st.SaveSettings(domain.Settings{}); err == nil {
		t.Fatal("save settings on a closed store should fail")
	}
}

// TestClosedStoreConfigAndListErrors covers the settings and tenant-list reads
// the platform makes on every page load. A failed query there has to surface as
// an error rather than an empty list, or the UI would show "no tenants" while
// the database is actually broken.
func TestClosedStoreConfigAndListErrors(t *testing.T) {
	st := closedStore(t)
	if _, err := st.LoadTenantConfig(); err == nil {
		t.Fatal("loading the unified config from a closed store should fail")
	}
	if err := st.SaveTenantConfig([]domain.ConfigEntry{{Key: "a", Value: "b"}}); err == nil {
		t.Fatal("saving the unified config to a closed store should fail")
	}
	filters := []TenantFilter{
		{},
		{Status: domain.StatusRunning},
		{IncludeTrashed: true, Query: "ali"},
		{HideDeleting: true, SkipNotes: true, Query: "ali"},
	}
	for _, f := range filters {
		if _, _, err := st.ListTenants(f); err == nil {
			t.Fatalf("listing tenants with %+v on a closed store should fail", f)
		}
	}
}
