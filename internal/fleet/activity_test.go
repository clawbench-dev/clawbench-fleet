package fleet

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"clawbench-fleet/internal/domain"
)

// newInstanceDB writes a minimal ClawBench.db the way a real instance has one:
// chat_history plus the auxiliary tables, with UTC timestamps.
func newInstanceDB(t *testing.T, dataDir string, historyTimes ...string) string {
	t.Helper()
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dataDir, "ClawBench.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	stmts := []string{
		`CREATE TABLE chat_history (id INTEGER PRIMARY KEY AUTOINCREMENT, project_id INTEGER NOT NULL DEFAULT 0,
			role TEXT NOT NULL, content TEXT NOT NULL, session_id TEXT, created_at DATETIME DEFAULT CURRENT_TIMESTAMP)`,
		`CREATE TABLE chat_sessions (id TEXT PRIMARY KEY, updated_at DATETIME DEFAULT CURRENT_TIMESTAMP, removed INTEGER NOT NULL DEFAULT 0)`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	for _, at := range historyTimes {
		if _, err := db.Exec(`INSERT INTO chat_history(role, content, created_at) VALUES('user','hi',?)`, at); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func TestParseClawbenchTime(t *testing.T) {
	// SQLite CURRENT_TIMESTAMP writes UTC without a zone marker.
	got := parseClawbenchTime("2026-11-21 06:09:02")
	if !got.ok {
		t.Fatal("did not parse")
	}
	if want := time.Date(2026, 11, 21, 6, 9, 2, 0, time.UTC); !got.at.Equal(want) {
		t.Fatalf("got %s, want %s", got.at, want)
	}
	// RFC3339 with a fraction is what Go-side writes look like.
	if got := parseClawbenchTime("2026-11-21T14:09:02.123456789+08:00"); !got.ok ||
		got.at.UTC().Hour() != 6 {
		t.Fatalf("rfc3339: %+v", got)
	}
	if got := parseClawbenchTime(""); got.ok {
		t.Fatal("empty string should not parse")
	}
	if got := parseClawbenchTime("not a time"); got.ok {
		t.Fatal("garbage should not parse")
	}
}

func TestRefreshLastActiveReadsInstanceDatabase(t *testing.T) {
	svc, cfg := newTestService(t)
	ctx := context.Background()

	tenant := &domain.Tenant{
		Username: "alice", Port: 32111, UnitName: "clawbench-alice.service",
		HomeDir: filepath.Join(cfg.System.HomeRoot, "alice"),
		DataDir: cfg.TenantDataDir("alice"), Status: domain.StatusRunning,
	}
	if err := svc.Store.CreateTenant(tenant); err != nil {
		t.Fatal(err)
	}
	newInstanceDB(t, tenant.DataDir, "2026-11-01 02:00:00", "2026-11-21 06:09:02")

	svc.RefreshLastActive(ctx)

	fresh, err := svc.Store.GetTenant(tenant.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 11, 21, 6, 9, 2, 0, time.UTC).Format(time.RFC3339)
	if fresh.LastActiveAt != want {
		t.Fatalf("last_active_at = %q, want %q", fresh.LastActiveAt, want)
	}
}

func TestRefreshLastActiveIgnoresSessionUpdates(t *testing.T) {
	svc, cfg := newTestService(t)
	ctx := context.Background()

	tenant := &domain.Tenant{
		Username: "bob", Port: 32112, UnitName: "clawbench-bob.service",
		HomeDir: filepath.Join(cfg.System.HomeRoot, "bob"),
		DataDir: cfg.TenantDataDir("bob"), Status: domain.StatusRunning,
	}
	if err := svc.Store.CreateTenant(tenant); err != nil {
		t.Fatal(err)
	}
	// The newest session is archived, which is later than any message. Only the
	// message time counts, so the archived session must not win.
	dbPath := newInstanceDB(t, tenant.DataDir, "2026-11-10 10:00:00")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	stmts := []string{
		`INSERT INTO chat_sessions(id, updated_at, removed) VALUES('live','2026-11-10 10:00:00',0)`,
		`INSERT INTO chat_sessions(id, updated_at, removed) VALUES('archived','2026-12-01 09:00:00',1)`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	_ = db.Close()

	svc.RefreshLastActive(ctx)

	fresh, _ := svc.Store.GetTenant(tenant.ID)
	want := time.Date(2026, 11, 10, 10, 0, 0, 0, time.UTC).Format(time.RFC3339)
	if fresh.LastActiveAt != want {
		t.Fatalf("last_active_at = %q, want %q", fresh.LastActiveAt, want)
	}
}

func TestRefreshLastActiveWithoutDatabaseDoesNotCreateOne(t *testing.T) {
	svc, cfg := newTestService(t)
	ctx := context.Background()

	tenant := &domain.Tenant{
		Username: "carol", Port: 32113, UnitName: "clawbench-carol.service",
		HomeDir: filepath.Join(cfg.System.HomeRoot, "carol"),
		DataDir: cfg.TenantDataDir("carol"), Status: domain.StatusRunning,
	}
	if err := svc.Store.CreateTenant(tenant); err != nil {
		t.Fatal(err)
	}
	svc.RefreshLastActive(ctx)

	if _, err := os.Stat(filepath.Join(tenant.DataDir, "ClawBench.db")); !os.IsNotExist(err) {
		t.Fatalf("probe must not create the instance database: %v", err)
	}
	fresh, _ := svc.Store.GetTenant(tenant.ID)
	if fresh.LastActiveAt != "" {
		t.Fatalf("last_active_at should stay empty, got %q", fresh.LastActiveAt)
	}
}

func TestTenantDetailFillsLastActiveOnDemand(t *testing.T) {
	svc, cfg := newTestService(t)
	tenant := &domain.Tenant{
		Username: "dave", Port: 32114, UnitName: "clawbench-dave.service",
		HomeDir: filepath.Join(cfg.System.HomeRoot, "dave"),
		DataDir: cfg.TenantDataDir("dave"), Status: domain.StatusRunning,
	}
	if err := svc.Store.CreateTenant(tenant); err != nil {
		t.Fatal(err)
	}
	newInstanceDB(t, tenant.DataDir, "2026-11-19 08:30:00")

	dto, err := svc.TenantDetail(tenant.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 11, 19, 8, 30, 0, 0, time.UTC).Format(time.RFC3339)
	if dto.LastActiveAt != want {
		t.Fatalf("detail last_active_at = %q, want %q", dto.LastActiveAt, want)
	}
}
