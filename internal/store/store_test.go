package store

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"clawbench-fleet/internal/domain"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "fleet.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.SeedPorts(21000, 21010); err != nil {
		t.Fatal(err)
	}
	return st
}

func newTenant(username string, port int, status string) *domain.Tenant {
	return &domain.Tenant{
		Username: username, DisplayName: username, UID: 5000, GID: 5000, Port: port,
		Status: status, CPUQuota: "200%", MemoryMax: "4G", TasksMax: 512,
		HomeDir: "/home/" + username, DataDir: "/home/" + username + "/.clawbench",
		UnitName: "clawbench-" + username + ".service",
	}
}

func TestTenantSSHEnabledRoundTrip(t *testing.T) {
	st := openTestStore(t)
	tenant := newTenant("alice", 21000, domain.StatusRunning)
	tenant.SSHEnabled = true
	if err := st.CreateTenant(tenant); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetTenant(tenant.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.SSHEnabled {
		t.Fatal("ssh_enabled was not persisted")
	}
	got.SSHEnabled = false
	if err := st.UpdateTenant(got); err != nil {
		t.Fatal(err)
	}
	again, err := st.GetTenant(tenant.ID)
	if err != nil {
		t.Fatal(err)
	}
	if again.SSHEnabled {
		t.Fatal("ssh_enabled update was not persisted")
	}
}

func TestChangePortMovesAndRejectsConflicts(t *testing.T) {
	st := openTestStore(t)
	alice := newTenant("alice", 21000, domain.StatusRunning)
	bob := newTenant("bob", 21003, domain.StatusRunning)
	for _, tenant := range []*domain.Tenant{alice, bob} {
		if err := st.CreateTenant(tenant); err != nil {
			t.Fatal(err)
		}
	}
	portsA, err := st.AllocatePorts(21000, 21010, 3, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AssignPorts(alice.ID, portsA); err != nil {
		t.Fatal(err)
	}
	portsB, err := st.AllocatePorts(21000, 21010, 3, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AssignPorts(bob.ID, portsB); err != nil {
		t.Fatal(err)
	}

	if err := st.ChangePort(alice.ID, portsA[0], portsA[1]); err != nil {
		t.Fatalf("move to own free port: %v", err)
	}
	if state, err := st.PortState(portsA[0]); err != nil || state != "free" {
		t.Fatalf("old port state = %q %v", state, err)
	}
	if state, err := st.PortState(portsA[1]); err != nil || state != "allocated" {
		t.Fatalf("new port state = %q %v", state, err)
	}

	if err := st.ChangePort(alice.ID, portsA[1], portsB[0]); err == nil {
		t.Fatal("must not steal a port owned by another tenant")
	}
	if err := st.ChangePort(alice.ID, portsA[1], portsA[1]); err != nil {
		t.Fatalf("same port should be a no-op: %v", err)
	}
	if err := st.ChangePort(alice.ID, portsA[1], 0); err == nil {
		t.Fatal("invalid port must be rejected")
	}
	// A port outside the configured range has no row and is recorded on demand.
	if err := st.ChangePort(alice.ID, portsA[1], 21500); err != nil {
		t.Fatalf("move outside range: %v", err)
	}
	if state, err := st.PortState(21500); err != nil || state != "allocated" {
		t.Fatalf("on-demand port state = %q %v", state, err)
	}
}

func TestChangePortReportsClosedDatabase(t *testing.T) {
	st := openTestStore(t)
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if err := st.ChangePort(1, 21000, 21001); err == nil {
		t.Fatal("ChangePort should fail after the database is closed")
	}
}

func TestChangePortReportsDatabaseErrors(t *testing.T) {
	t.Run("query", func(t *testing.T) {
		st := openTestStore(t)
		if _, err := st.db.Exec(`DROP TABLE ports`); err != nil {
			t.Fatal(err)
		}
		if err := st.ChangePort(1, 21000, 21001); err == nil {
			t.Fatal("missing ports table should fail the lookup")
		}
	})
	t.Run("insert", func(t *testing.T) {
		st := openTestStore(t)
		if err := st.ClaimPort(21000, 1); err != nil {
			t.Fatal(err)
		}
		if _, err := st.db.Exec(`CREATE TRIGGER fail_insert BEFORE INSERT ON ports BEGIN SELECT RAISE(ABORT, 'no insert'); END`); err != nil {
			t.Fatal(err)
		}
		if err := st.ChangePort(1, 21000, 21500); err == nil {
			t.Fatal("insert trigger should fail the move")
		}
	})
	t.Run("release", func(t *testing.T) {
		st := openTestStore(t)
		if err := st.ClaimPort(21000, 1); err != nil {
			t.Fatal(err)
		}
		if _, err := st.db.Exec(`CREATE TRIGGER fail_update BEFORE UPDATE ON ports BEGIN SELECT RAISE(ABORT, 'no update'); END`); err != nil {
			t.Fatal(err)
		}
		if err := st.ChangePort(1, 21000, 21500); err == nil {
			t.Fatal("update trigger should fail the release of the old port")
		}
	})
}

func TestOpenIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fleet.db")
	first, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.SaveSettings(domain.Settings{PortStart: 1, PortEnd: 2}); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = second.Close() }()
	got, err := second.LoadSettings(domain.Settings{})
	if err != nil || got.PortStart != 1 {
		t.Fatalf("settings survived reopen: %+v %v", got, err)
	}
	if err := second.Ping(); err != nil {
		t.Fatalf("ping: %v", err)
	}
}

func TestSettingsRoundTrip(t *testing.T) {
	st := openTestStore(t)
	def := domain.Settings{PortStart: 100, PortEnd: 200}
	got, err := st.LoadSettings(def)
	if err != nil || got != def {
		t.Fatalf("missing settings should fall back to defaults: %+v %v", got, err)
	}
	want := domain.Settings{PortStart: 300, PortEnd: 400, GuestEnabled: true, GuestNotice: "hi"}
	if err := st.SaveSettings(want); err != nil {
		t.Fatal(err)
	}
	got, err = st.LoadSettings(def)
	if err != nil || got != want {
		t.Fatalf("round trip = %+v (%v)", got, err)
	}
	// Saving again updates in place instead of failing on the primary key.
	want.GuestNotice = "changed"
	if err := st.SaveSettings(want); err != nil {
		t.Fatal(err)
	}
	if got, _ = st.LoadSettings(def); got.GuestNotice != "changed" {
		t.Fatalf("update did not apply: %+v", got)
	}
}

func TestTenantCRUD(t *testing.T) {
	st := openTestStore(t)
	tenant := newTenant("alice", 21001, domain.StatusStopped)
	if err := st.CreateTenant(tenant); err != nil {
		t.Fatal(err)
	}
	if tenant.ID == 0 {
		t.Fatal("insert should fill the id")
	}
	got, err := st.GetTenant(tenant.ID)
	if err != nil || got.Username != "alice" {
		t.Fatalf("get = %+v (%v)", got, err)
	}
	if _, err := st.GetTenant(999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing tenant should be ErrNotFound, got %v", err)
	}
	byName, err := st.GetTenantByUsername("alice")
	if err != nil || byName.ID != tenant.ID {
		t.Fatalf("get by name = %+v (%v)", byName, err)
	}
	if _, err := st.GetTenantByUsername("nobody"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing username should be ErrNotFound, got %v", err)
	}

	got.DisplayName = "Alice"
	got.Status = domain.StatusRunning
	got.Notes = "changed"
	if err := st.UpdateTenant(got); err != nil {
		t.Fatal(err)
	}
	got2, _ := st.GetTenant(tenant.ID)
	if got2.DisplayName != "Alice" || got2.Status != domain.StatusRunning || got2.Notes != "changed" {
		t.Fatalf("update not applied: %+v", got2)
	}

	if err := st.SetTenantStatus(tenant.ID, domain.StatusError, "boom"); err != nil {
		t.Fatal(err)
	}
	got2, _ = st.GetTenant(tenant.ID)
	if got2.Status != domain.StatusError || got2.LastError != "boom" {
		t.Fatalf("status = %+v", got2)
	}
	if err := st.SetTenantHealth(tenant.ID, true, "1.2.3", "2026-01-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	got2, _ = st.GetTenant(tenant.ID)
	if !got2.LastHealthOK || got2.Version != "1.2.3" {
		t.Fatalf("health = %+v", got2)
	}
	if err := st.SetTenantDisk(tenant.ID, 4096, "2026-01-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetTenantVersion(tenant.ID, "2.0.0"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetTenantLastActive(tenant.ID, "2026-02-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	got2, _ = st.GetTenant(tenant.ID)
	if got2.DiskBytes != 4096 || got2.Version != "2.0.0" || got2.LastActiveAt != "2026-02-01T00:00:00Z" {
		t.Fatalf("per-column setters = %+v", got2)
	}

	if err := st.DeleteTenant(tenant.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetTenant(tenant.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("tenant should be gone, got %v", err)
	}
}

func TestDuplicateUsernameRejected(t *testing.T) {
	st := openTestStore(t)
	if err := st.CreateTenant(newTenant("alice", 21001, domain.StatusRunning)); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateTenant(newTenant("alice", 21002, domain.StatusRunning)); err == nil {
		t.Fatal("duplicate username must be rejected by the unique index")
	}
}

func TestListTenantsFiltersAndPages(t *testing.T) {
	st := openTestStore(t)
	if err := st.CreateTenant(newTenant("alice", 21001, domain.StatusRunning)); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateTenant(newTenant("bob", 21002, domain.StatusStopped)); err != nil {
		t.Fatal(err)
	}
	trashed := newTenant("carol", 21003, domain.StatusTrashed)
	trashed.DeletedAt = "2026-01-01T00:00:00Z"
	if err := st.CreateTenant(trashed); err != nil {
		t.Fatal(err)
	}

	items, total, err := st.ListTenants(TenantFilter{})
	if err != nil || total != 2 || len(items) != 2 {
		t.Fatalf("default listing hides trash: %d %d %v", len(items), total, err)
	}
	items, _, err = st.ListTenants(TenantFilter{IncludeTrashed: true})
	if err != nil || len(items) != 3 {
		t.Fatalf("include trashed: %d %v", len(items), err)
	}
	items, total, err = st.ListTenants(TenantFilter{Status: domain.StatusStopped})
	if err != nil || total != 1 || items[0].Username != "bob" {
		t.Fatalf("status filter: %+v %d %v", items, total, err)
	}
	items, total, err = st.ListTenants(TenantFilter{Query: "ali"})
	if err != nil || total != 1 || items[0].Username != "alice" {
		t.Fatalf("query filter: %+v %d %v", items, total, err)
	}
	items, total, err = st.ListTenants(TenantFilter{Page: 2, PageSize: 1})
	if err != nil || total != 2 || len(items) != 1 {
		t.Fatalf("paging: %+v %d %v", items, total, err)
	}
	// Page size is clamped instead of rejected.
	items, _, err = st.ListTenants(TenantFilter{PageSize: 10000})
	if err != nil || len(items) != 2 {
		t.Fatalf("huge page size: %d %v", len(items), err)
	}
}

func TestListTenantsByStatusAndCounts(t *testing.T) {
	st := openTestStore(t)
	if err := st.CreateTenant(newTenant("alice", 21001, domain.StatusRunning)); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateTenant(newTenant("bob", 21002, domain.StatusRunning)); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateTenant(newTenant("carol", 21003, domain.StatusError)); err != nil {
		t.Fatal(err)
	}
	running, err := st.ListTenantsByStatus(domain.StatusRunning)
	if err != nil || len(running) != 2 {
		t.Fatalf("running = %d %v", len(running), err)
	}
	if got, err := st.ListTenantsByStatus(); err != nil || got != nil {
		t.Fatalf("no statuses should return nil, got %v %v", got, err)
	}
	counts, err := st.CountByStatus()
	if err != nil || counts[domain.StatusRunning] != 2 || counts[domain.StatusError] != 1 {
		t.Fatalf("counts = %+v (%v)", counts, err)
	}
	uids, err := st.AllTenantUIDs()
	if err != nil || len(uids) != 1 || !uids[5000] {
		t.Fatalf("uids = %+v (%v)", uids, err)
	}
	if err := st.ErrNoSuchTenant(7); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ErrNoSuchTenant should wrap ErrNotFound: %v", err)
	}
}

func TestPortStateAndClaim(t *testing.T) {
	st := openTestStore(t)
	state, err := st.PortState(21000)
	if err != nil || state != "free" {
		t.Fatalf("seeded port = %q %v", state, err)
	}
	if _, err := st.PortState(1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown port = %v", err)
	}
	if err := st.ClaimPort(21000, 42); err != nil {
		t.Fatal(err)
	}
	if state, _ = st.PortState(21000); state != "allocated" {
		t.Fatalf("claimed port = %q", state)
	}
	if err := st.ClaimPort(21000, 7); err == nil {
		t.Fatal("claiming another tenant's port must fail")
	}
	if err := st.ClaimPort(21000, 42); err != nil {
		t.Fatalf("re-claiming own port must succeed: %v", err)
	}
	if err := st.ClaimPort(0, 42); err == nil {
		t.Fatal("invalid port must be rejected")
	}
	// A port outside the seeded range is recorded on demand.
	if err := st.ClaimPort(65000, 42); err != nil {
		t.Fatalf("out-of-range claim should be recorded: %v", err)
	}
}

func TestReserveReleaseAndAssignPorts(t *testing.T) {
	st := openTestStore(t)
	group, err := st.AllocatePorts(21000, 21010, 2, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AssignPorts(9, group); err != nil {
		t.Fatal(err)
	}
	ports, err := st.PortsForTenant(9)
	if err != nil || len(ports) != 2 {
		t.Fatalf("ports for tenant = %v %v", ports, err)
	}
	if err := st.ReservePorts(9); err != nil {
		t.Fatal(err)
	}
	if err := st.SetPortsAllocated(9); err != nil {
		t.Fatal(err)
	}
	if err := st.ReleasePorts(9); err != nil {
		t.Fatal(err)
	}
	if ports, _ = st.PortsForTenant(9); len(ports) != 0 {
		t.Fatalf("released ports still attached: %v", ports)
	}
	if err := st.SeedPorts(70000, 70010); err == nil {
		t.Fatal("invalid range must be rejected")
	}
	if _, allocErr := st.AllocatePorts(21000, 21010, 0, 0); allocErr == nil {
		t.Fatal("count 0 must be rejected")
	}
	if _, err := st.AllocatePorts(21000, 21001, 5, 0); !errors.Is(err, ErrPortExhausted) {
		t.Fatalf("exhausted range = %v", err)
	}
	groups, err := st.FreePortGroups(21000, 21010, 2)
	if err != nil || groups != 5 {
		t.Fatalf("free groups = %d %v", groups, err)
	}
	if groups, _ = st.FreePortGroups(21000, 21010, 0); groups != 0 {
		t.Fatalf("zero size = %d", groups)
	}
}

func TestAuditLog(t *testing.T) {
	st := openTestStore(t)
	for i, actor := range []string{"admin", "system", "admin"} {
		if err := st.AddAudit(&domain.AuditEntry{
			Actor: actor, Action: "tenant.create", TenantID: int64(i + 1),
			TenantName: "alice", Detail: "{}", Result: "ok",
		}); err != nil {
			t.Fatal(err)
		}
	}
	items, total, err := st.ListAudit(AuditFilter{})
	if err != nil || total != 3 || len(items) != 3 {
		t.Fatalf("list = %d %d %v", len(items), total, err)
	}
	if items[0].CreatedAt == "" {
		t.Fatal("created_at should be filled in")
	}
	items, total, err = st.ListAudit(AuditFilter{Actor: "admin"})
	if err != nil || total != 2 || len(items) != 2 {
		t.Fatalf("actor filter = %d %d %v", len(items), total, err)
	}
	_, total, _ = st.ListAudit(AuditFilter{Action: "tenant.delete"})
	if total != 0 {
		t.Fatalf("action filter = %d", total)
	}
	_, total, _ = st.ListAudit(AuditFilter{Tenant: "ali"})
	if total != 3 {
		t.Fatalf("tenant filter = %d", total)
	}
	_, total, _ = st.ListAudit(AuditFilter{From: "2000-01-01T00:00:00Z", To: "2999-01-01T00:00:00Z"})
	if total != 3 {
		t.Fatalf("time filter = %d", total)
	}
	items, total, _ = st.ListAudit(AuditFilter{Page: 2, PageSize: 2})
	if total != 3 || len(items) != 1 {
		t.Fatalf("paging = %d %d", len(items), total)
	}
	recent, err := st.RecentAudit(2)
	if err != nil || len(recent) != 2 {
		t.Fatalf("recent = %d %v", len(recent), err)
	}
	if recent[0].ID < recent[1].ID {
		t.Fatal("recent audit should be newest first")
	}
	if recent, _ = st.RecentAudit(0); len(recent) != 3 {
		t.Fatalf("zero limit should default to 10, got %d", len(recent))
	}
	deleted, err := st.PurgeAuditBefore("2999-01-01T00:00:00Z")
	if err != nil || deleted != 3 {
		t.Fatalf("purge = %d %v", deleted, err)
	}
}

func TestMetricSamplesRoundTrip(t *testing.T) {
	st := openTestStore(t)
	tenant := newTenant("metrics", 21000, domain.StatusRunning)
	if err := st.CreateTenant(tenant); err != nil {
		t.Fatal(err)
	}
	at := time.Now().Format(time.RFC3339)
	if err := st.AddMetricSample(tenant.ID, domain.MetricSample{
		At: at, CPUPercent: 12.5, MemoryBytes: 1024, PIDs: 3, DiskBytes: 2048,
	}); err != nil {
		t.Fatal(err)
	}
	got, err := st.MetricHistory(tenant.ID, time.Now().Add(-time.Hour), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].CPUPercent != 12.5 || got[0].DiskBytes != 2048 || got[0].At != at {
		t.Fatalf("history = %+v", got)
	}
	// The limit is applied and the default is a sane cap.
	if _, err := st.MetricHistory(tenant.ID, time.Now().Add(-time.Hour), 0); err != nil {
		t.Fatal(err)
	}
	if _, err := st.MetricHistory(tenant.ID, time.Now().Add(time.Hour), 10); err != nil {
		t.Fatal(err)
	}
}

func TestTenantDiskQuotaRoundTrip(t *testing.T) {
	st := openTestStore(t)
	tenant := newTenant("disk", 21000, domain.StatusRunning)
	tenant.DiskQuotaGB = 12
	if err := st.CreateTenant(tenant); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetTenant(tenant.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.DiskQuotaGB != 12 {
		t.Fatalf("disk quota = %d", got.DiskQuotaGB)
	}
	got.DiskQuotaGB = 0
	if err := st.UpdateTenant(got); err != nil {
		t.Fatal(err)
	}
	again, _ := st.GetTenant(tenant.ID)
	if again.DiskQuotaGB != 0 {
		t.Fatalf("disk quota after update = %d", again.DiskQuotaGB)
	}
}
