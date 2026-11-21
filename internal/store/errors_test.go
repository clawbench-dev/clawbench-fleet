package store

import (
	"os"
	"path/filepath"
	"testing"

	"clawbench-fleet/internal/domain"
)

func TestOpenCreatesPrivateDirectory(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "nested", "deeper", "fleet.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		t.Fatalf("database permissions = %v, want private", perm)
	}
	if info, err := os.Stat(filepath.Dir(path)); err != nil || info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("data directory should be private: %v", err)
	}
}

func TestClosedStoreReportsErrors(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "fleet.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if err := st.Ping(); err == nil {
		t.Fatal("ping on a closed store should fail")
	}
	if _, err := st.CountAdmins(); err == nil {
		t.Fatal("query on a closed store should fail")
	}
	if _, _, err := st.ListTenants(TenantFilter{}); err == nil {
		t.Fatal("listing on a closed store should fail")
	}
	if _, err := st.LoadSettings(domain.Settings{PortStart: 1, PortEnd: 2}); err == nil {
		t.Fatal("settings on a closed store should fail")
	}
}

func TestAddColumnIfMissingAlreadyPresent(t *testing.T) {
	st := openTestStore(t)
	// The migration helper is idempotent: the column exists after Open.
	if err := st.addColumnIfMissing("tenants", "last_active_at", "TEXT NOT NULL DEFAULT ''"); err != nil {
		t.Fatalf("existing column = %v", err)
	}
	if err := st.addColumnIfMissing("tenants", "brand_new", "TEXT NOT NULL DEFAULT ''"); err != nil {
		t.Fatalf("new column = %v", err)
	}
	if err := st.addColumnIfMissing("tenants", "brand_new", "TEXT NOT NULL DEFAULT ''"); err != nil {
		t.Fatalf("repeated add = %v", err)
	}
	// A missing table is reported as an error.
	if err := st.addColumnIfMissing("no_such_table", "x", "TEXT"); err == nil {
		t.Fatal("missing table should fail")
	}
}

func TestCorruptSettingsFallsBack(t *testing.T) {
	st := openTestStore(t)
	if _, err := st.db.Exec(`INSERT INTO settings(key, value) VALUES('platform', 'not json')`); err != nil {
		t.Fatal(err)
	}
	def := domain.Settings{PortStart: 1, PortEnd: 2}
	got, err := st.LoadSettings(def)
	if err == nil || got != def {
		t.Fatalf("corrupt settings = %+v %v", got, err)
	}
}
