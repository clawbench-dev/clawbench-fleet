package store

import (
	"errors"
	"testing"

	"clawbench-fleet/internal/domain"
)

func TestAdminAndSessions(t *testing.T) {
	st := openTestStore(t)
	if n, err := st.CountAdmins(); err != nil || n != 0 {
		t.Fatalf("count = %d %v", n, err)
	}
	admin, err := st.CreateAdmin("admin", "hash-1")
	if err != nil || admin.ID == 0 {
		t.Fatalf("create admin = %+v %v", admin, err)
	}
	if n, _ := st.CountAdmins(); n != 1 {
		t.Fatalf("count after insert = %d", n)
	}
	if _, err := st.GetAdminByUsername("nobody"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing admin = %v", err)
	}
	got, err := st.GetAdminByID(admin.ID)
	if err != nil || got.PasswordHash != "hash-1" || got.Username != "admin" {
		t.Fatalf("get admin = %+v %v", got, err)
	}
	if err := st.UpdateAdminPassword(admin.ID, "hash-2"); err != nil {
		t.Fatal(err)
	}
	if got, _ = st.GetAdminByID(admin.ID); got.PasswordHash != "hash-2" {
		t.Fatalf("password not updated: %+v", got)
	}
	if err := st.TouchAdminLogin(admin.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ = st.GetAdminByUsername("admin"); got.LastLoginAt == "" {
		t.Fatal("last_login_at not recorded")
	}

	sess := &domain.Session{TokenHash: "t1", AdminID: admin.ID, CreatedAt: "2026-01-01T00:00:00Z", ExpiresAt: "2026-01-08T00:00:00Z", IP: "10.0.0.1", UserAgent: "ua"}
	if err := st.CreateSession(sess); err != nil {
		t.Fatal(err)
	}
	if got, err := st.GetSession("t1"); err != nil || got.IP != "10.0.0.1" || got.UserAgent != "ua" {
		t.Fatalf("get session = %+v %v", got, err)
	}
	if _, err := st.GetSession("nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing session = %v", err)
	}
	expired := &domain.Session{TokenHash: "t2", AdminID: admin.ID, CreatedAt: "2020-01-01T00:00:00Z", ExpiresAt: "2020-01-02T00:00:00Z"}
	if err := st.CreateSession(expired); err != nil {
		t.Fatal(err)
	}
	removed, err := st.PurgeExpiredSessions("2021-01-01T00:00:00Z")
	if err != nil || removed != 1 {
		t.Fatalf("purge expired = %d %v", removed, err)
	}
	if err := st.DeleteSession("t1"); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteSessionsForAdmin(admin.ID); err != nil {
		t.Fatal(err)
	}
}

func TestBackups(t *testing.T) {
	st := openTestStore(t)
	if err := st.CreateBackup(&domain.Backup{TenantID: 1, Username: "alice", Path: "/b/1", Status: "ok", IntegrityOK: true}); err != nil {
		t.Fatal(err)
	}
	old := &domain.Backup{TenantID: 1, Username: "alice", Path: "/b/0", Status: "ok", CreatedAt: "2000-01-01T00:00:00Z"}
	if err := st.CreateBackup(old); err != nil {
		t.Fatal(err)
	}
	list, err := st.ListBackups(1)
	if err != nil || len(list) != 2 {
		t.Fatalf("list = %d %v", len(list), err)
	}
	got, err := st.GetBackup(list[0].ID)
	if err != nil || got.Username != "alice" || !got.IntegrityOK {
		t.Fatalf("get = %+v %v", got, err)
	}
	if _, err := st.GetBackup(999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing backup = %v", err)
	}
	stale, err := st.BackupsOlderThan(1, "2001-01-01T00:00:00Z")
	if err != nil || len(stale) != 1 || stale[0].Path != "/b/0" {
		t.Fatalf("older than = %+v %v", stale, err)
	}
	if err := st.DeleteBackupRecord(old.ID); err != nil {
		t.Fatal(err)
	}
	if list, _ = st.ListBackups(1); len(list) != 1 {
		t.Fatalf("delete did not apply: %d", len(list))
	}
}

func TestJobs(t *testing.T) {
	st := openTestStore(t)
	job, err := st.CreateJob("import", `{"rows":2}`, 2)
	if err != nil || job.ID == 0 || job.Status != domain.JobPending {
		t.Fatalf("create job = %+v %v", job, err)
	}
	first, err := st.AddJobItem(job.ID, 0, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddJobItem(job.ID, 0, "bob"); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkJobRunning(job.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateJobItem(first, domain.JobSucceeded, ""); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateJobProgress(job.ID, 1, 0); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetJob(job.ID)
	if err != nil || got.Status != domain.JobRunning || got.Done != 1 || len(got.Items) != 2 {
		t.Fatalf("job = %+v %v", got, err)
	}
	if got.Items[0].Status != domain.JobSucceeded {
		t.Fatalf("item status = %q", got.Items[0].Status)
	}
	if err := st.FinishJob(job.ID, domain.JobPartial, `{"done":2}`); err != nil {
		t.Fatal(err)
	}
	if got, _ = st.GetJob(job.ID); got.Status != domain.JobPartial || got.FinishedAt == "" {
		t.Fatalf("finished job = %+v", got)
	}
	if _, err := st.GetJob(999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing job = %v", err)
	}

	running, err := st.CreateJob("upgrade", "{}", 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.MarkJobRunning(running.ID); err != nil {
		t.Fatal(err)
	}
	pending, _ := st.CreateJob("bulk", "{}", 1)
	interrupted, err := st.MarkInterruptedJobs()
	if err != nil || interrupted != 2 {
		t.Fatalf("running and pending jobs should be failed on restart: %d %v", interrupted, err)
	}
	if got, _ = st.GetJob(running.ID); got.Status != domain.JobFailed {
		t.Fatalf("running job after restart = %q", got.Status)
	}
	if got, _ = st.GetJob(pending.ID); got.Status != domain.JobFailed {
		t.Fatalf("pending job after restart = %q", got.Status)
	}
	if _, err := st.MarkInterruptedJobs(); err != nil {
		t.Fatal(err)
	}
	recent, err := st.RecentJobs(2)
	if err != nil || len(recent) != 2 || recent[0].ID < recent[1].ID {
		t.Fatalf("recent = %+v %v", recent, err)
	}
	if all, _ := st.RecentJobs(0); len(all) != 3 {
		t.Fatalf("zero limit should return everything up to the default, got %d", len(all))
	}
}

func TestVersions(t *testing.T) {
	st := openTestStore(t)
	if _, err := st.CurrentVersion(); !errors.Is(err, ErrNotFound) {
		t.Fatalf("no current version yet: %v", err)
	}
	for _, v := range []string{"1.0.0", "1.1.0", "1.2.0"} {
		ver := &domain.Version{Version: v, Path: "/opt/" + v, SHA256: "sum", SizeBytes: 10, Status: "installed"}
		if err := st.CreateVersion(ver); err != nil {
			t.Fatal(err)
		}
		if ver.ID == 0 {
			t.Fatal("version id should be filled in")
		}
	}
	got, err := st.GetVersion("1.1.0")
	if err != nil || got.Path != "/opt/1.1.0" {
		t.Fatalf("get version = %+v %v", got, err)
	}
	if _, err := st.GetVersion("9.9.9"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing version = %v", err)
	}
	if err := st.SetCurrentVersion("1.0.0"); err != nil {
		t.Fatal(err)
	}
	if cur, _ := st.CurrentVersion(); cur.Version != "1.0.0" {
		t.Fatalf("current = %+v", cur)
	}
	if _, err := st.PreviousVersion(); !errors.Is(err, ErrNotFound) {
		t.Fatalf("first switch has no previous version: %v", err)
	}
	if err := st.SetCurrentVersion("1.2.0"); err != nil {
		t.Fatal(err)
	}
	if prev, _ := st.PreviousVersion(); prev.Version != "1.0.0" {
		t.Fatalf("previous = %+v", prev)
	}
	if err := st.SetCurrentVersion("9.9.9"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown version = %v", err)
	}
	if cur, _ := st.CurrentVersion(); cur.Version != "1.2.0" {
		t.Fatalf("failed switch must not move current, got %+v", cur)
	}
	versions, err := st.ListVersions()
	if err != nil || len(versions) != 3 {
		t.Fatalf("list = %d %v", len(versions), err)
	}
	if err := st.DeleteVersion("1.1.0"); err != nil {
		t.Fatal(err)
	}
	if versions, _ = st.ListVersions(); len(versions) != 2 {
		t.Fatalf("delete did not apply: %d", len(versions))
	}
}

func TestGuestReports(t *testing.T) {
	st := openTestStore(t)
	first := &domain.GuestReport{TenantID: 1, TenantName: "alice", Message: "can not open", IP: "10.0.0.1"}
	if err := st.CreateGuestReport(first); err != nil || first.ID == 0 {
		t.Fatalf("create = %+v %v", first, err)
	}
	second := &domain.GuestReport{TenantID: 2, TenantName: "bob", Message: "slow"}
	if err := st.CreateGuestReport(second); err != nil {
		t.Fatal(err)
	}
	list, err := st.ListGuestReports(true, 10)
	if err != nil || len(list) != 2 {
		t.Fatalf("open list = %d %v", len(list), err)
	}
	if list[0].ID < list[1].ID {
		t.Fatal("guest reports should be newest first")
	}
	if list, _ = st.ListGuestReports(true, 0); len(list) != 2 {
		t.Fatalf("zero limit should default, got %d", len(list))
	}
	if n, err := st.CountOpenGuestReports(); err != nil || n != 2 {
		t.Fatalf("open count = %d %v", n, err)
	}
	got, err := st.GetGuestReport(first.ID)
	if err != nil || got.Message != "can not open" || got.IP != "10.0.0.1" {
		t.Fatalf("get = %+v %v", got, err)
	}
	if _, err := st.GetGuestReport(999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing report = %v", err)
	}
	if dup, err := st.HasOpenGuestReportSince(1, "2000-01-01T00:00:00Z"); err != nil || !dup {
		t.Fatalf("recent open report should be found: %v %v", dup, err)
	}
	if dup, _ := st.HasOpenGuestReportSince(1, "2999-01-01T00:00:00Z"); dup {
		t.Fatal("future cutoff should not match")
	}
	if err := st.ResolveGuestReport(first.ID, "admin", "2026-01-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if err := st.ResolveGuestReport(first.ID, "admin", "2026-01-02T00:00:00Z"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("resolving twice = %v", err)
	}
	if n, _ := st.CountOpenGuestReports(); n != 1 {
		t.Fatalf("open count after resolve = %d", n)
	}
	if list, _ = st.ListGuestReports(false, 10); len(list) != 2 {
		t.Fatalf("resolved reports still listed: %d", len(list))
	}
}
