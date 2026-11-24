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

// A tenant whose database was never created (or was moved away) must not have
// its stored activity cleared: "we could not look" is not "nobody used it".
func TestRefreshLastActiveKeepsValueWithoutDatabase(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	tenant := domain.Tenant{
		Username:     "nodb",
		DisplayName:  "nodb",
		Status:       domain.StatusRunning,
		DataDir:      filepath.Join(t.TempDir(), "missing"),
		LastActiveAt: "2026-11-01T00:00:00Z",
	}
	if err := svc.Store.CreateTenant(&tenant); err != nil {
		t.Fatal(err)
	}

	svc.RefreshLastActive(ctx)

	fresh, err := svc.Store.GetTenant(tenant.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.LastActiveAt != "2026-11-01T00:00:00Z" {
		t.Fatalf("last_active_at = %q, want the stored value untouched", fresh.LastActiveAt)
	}
}

// An instance database from an older build may have no chat_history table at
// all. That is one tenant without a reading, not an error for the whole sweep:
// the neighbor with a readable database still gets updated.
func TestRefreshLastActiveToleratesOldSchema(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	old := domain.Tenant{Username: "old", Port: 32201, Status: domain.StatusRunning, DataDir: t.TempDir()}
	if err := svc.Store.CreateTenant(&old); err != nil {
		t.Fatal(err)
	}
	writeInstanceDB(t, old.DataDir, `CREATE TABLE something_else (id INTEGER PRIMARY KEY)`)

	good := domain.Tenant{Username: "good", Port: 32202, Status: domain.StatusRunning, DataDir: t.TempDir()}
	if err := svc.Store.CreateTenant(&good); err != nil {
		t.Fatal(err)
	}
	writeInstanceDB(t, good.DataDir,
		`CREATE TABLE chat_history (id INTEGER PRIMARY KEY AUTOINCREMENT, created_at DATETIME)`,
		`INSERT INTO chat_history(created_at) VALUES('2026-11-05 08:00:00')`,
	)

	svc.RefreshLastActive(ctx)

	freshOld, _ := svc.Store.GetTenant(old.ID)
	if freshOld.LastActiveAt != "" {
		t.Fatalf("old schema should leave the value empty, got %q", freshOld.LastActiveAt)
	}
	freshGood, _ := svc.Store.GetTenant(good.ID)
	want := time.Date(2026, 11, 5, 8, 0, 0, 0, time.UTC).Format(time.RFC3339)
	if freshGood.LastActiveAt != want {
		t.Fatalf("good tenant = %q, want %q", freshGood.LastActiveAt, want)
	}
}

// The daily sweep is for live accounts. A tenant already in the trash keeps
// whatever it had: the row is about to be deleted, and reading its database
// would only add noise.
func TestRefreshLastActiveSkipsTrashedTenants(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	tenant := domain.Tenant{Username: "gone", DisplayName: "gone", Status: domain.StatusTrashed, DataDir: t.TempDir()}
	if err := svc.Store.CreateTenant(&tenant); err != nil {
		t.Fatal(err)
	}
	writeInstanceDB(t, tenant.DataDir,
		`CREATE TABLE chat_history (id INTEGER PRIMARY KEY AUTOINCREMENT, created_at DATETIME)`,
		`INSERT INTO chat_history(created_at) VALUES('2026-11-05 08:00:00')`,
	)

	svc.RefreshLastActive(ctx)

	fresh, _ := svc.Store.GetTenant(tenant.ID)
	if fresh.LastActiveAt != "" {
		t.Fatalf("trashed tenant should be skipped, got %q", fresh.LastActiveAt)
	}
}

// The detail page calls RefreshTenantLastActive lazily when the stored value is
// missing. It writes through to the tenant it was given, so the caller can show
// the fresh value without a second query.
func TestRefreshTenantLastActiveUpdatesCallerCopy(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	tenant := domain.Tenant{Username: "lazy", DisplayName: "lazy", Status: domain.StatusRunning, DataDir: t.TempDir()}
	if err := svc.Store.CreateTenant(&tenant); err != nil {
		t.Fatal(err)
	}
	writeInstanceDB(t, tenant.DataDir,
		`CREATE TABLE chat_history (id INTEGER PRIMARY KEY AUTOINCREMENT, created_at DATETIME)`,
		`INSERT INTO chat_history(created_at) VALUES('2026-11-06 09:30:00')`,
	)

	svc.RefreshTenantLastActive(ctx, &tenant)

	want := time.Date(2026, 11, 6, 9, 30, 0, 0, time.UTC).Format(time.RFC3339)
	if tenant.LastActiveAt != want {
		t.Fatalf("caller copy = %q, want %q", tenant.LastActiveAt, want)
	}
	stored, _ := svc.Store.GetTenant(tenant.ID)
	if stored.LastActiveAt != want {
		t.Fatalf("stored = %q, want %q", stored.LastActiveAt, want)
	}

	// A second call with the same value must not fail or change anything.
	svc.RefreshTenantLastActive(ctx, &tenant)
	if tenant.LastActiveAt != want {
		t.Fatalf("unchanged value should stay put, got %q", tenant.LastActiveAt)
	}
}

// An instance with a database but no messages yet is a normal new tenant: it
// simply has nothing to report, and the sweep keeps going.
func TestRefreshTenantLastActiveWithoutMessages(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	tenant := domain.Tenant{Username: "fresh", DisplayName: "fresh", Status: domain.StatusRunning, DataDir: t.TempDir()}
	if err := svc.Store.CreateTenant(&tenant); err != nil {
		t.Fatal(err)
	}
	writeInstanceDB(t, tenant.DataDir, `CREATE TABLE chat_history (id INTEGER PRIMARY KEY AUTOINCREMENT, created_at DATETIME)`)

	svc.RefreshTenantLastActive(ctx, &tenant)

	if tenant.LastActiveAt != "" {
		t.Fatalf("no messages means no activity, got %q", tenant.LastActiveAt)
	}
}

// writeInstanceDB creates ClawBench.db in dataDir and runs the statements.
// It is the schema-agnostic sibling of newInstanceDB in activity_test.go.
func writeInstanceDB(t *testing.T, dataDir string, stmts ...string) {
	t.Helper()
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dataDir, "ClawBench.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
}
