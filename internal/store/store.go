package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"clawbench-fleet/internal/domain"

	// Register the pure-Go SQLite driver used by every store query.
	_ "modernc.org/sqlite"
)

var (
	ErrNotFound      = errors.New("not found")
	ErrPortExhausted = errors.New("no consecutive free ports in range")
)

type Store struct {
	db *sql.DB
}

func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	// A single connection keeps writes serialized. The platform is an admin
	// console, not a high-throughput service, and this avoids SQLITE_BUSY
	// surprises between the request path and background workers.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA busy_timeout=5000",
		"PRAGMA foreign_keys=ON",
		"PRAGMA synchronous=NORMAL",
	} {
		if _, err := db.Exec(pragma); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("%s: %w", pragma, err)
		}
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	// The database holds the admin password hash and audit records; keep it
	// root-only even if the process umask was permissive.
	_ = os.Chmod(path, 0o600)
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS tenants (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			username TEXT NOT NULL UNIQUE,
			display_name TEXT NOT NULL DEFAULT '',
			uid INTEGER NOT NULL DEFAULT 0,
			gid INTEGER NOT NULL DEFAULT 0,
			port INTEGER NOT NULL UNIQUE,
			status TEXT NOT NULL,
			version TEXT NOT NULL DEFAULT '',
			cpu_quota TEXT NOT NULL DEFAULT '',
			memory_max TEXT NOT NULL DEFAULT '',
			tasks_max INTEGER NOT NULL DEFAULT 0,
			home_dir TEXT NOT NULL DEFAULT '',
			data_dir TEXT NOT NULL DEFAULT '',
			unit_name TEXT NOT NULL DEFAULT '',
			notes TEXT NOT NULL DEFAULT '',
			ssh_enabled INTEGER NOT NULL DEFAULT 0,
			disk_bytes INTEGER NOT NULL DEFAULT 0,
			disk_quota_gb INTEGER NOT NULL DEFAULT 0,
			disk_checked_at TEXT NOT NULL DEFAULT '',
			last_health_at TEXT NOT NULL DEFAULT '',
			last_health_ok INTEGER NOT NULL DEFAULT 0,
			last_active_at TEXT NOT NULL DEFAULT '',
			last_error TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			deleted_at TEXT NOT NULL DEFAULT '',
			trash_path TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE INDEX IF NOT EXISTS idx_tenants_status ON tenants(status)`,
		`CREATE INDEX IF NOT EXISTS idx_tenants_created ON tenants(created_at DESC)`,
		`CREATE TABLE IF NOT EXISTS ports (
			port INTEGER PRIMARY KEY,
			tenant_id INTEGER,
			state TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_ports_state ON ports(state)`,
		`CREATE TABLE IF NOT EXISTS backups (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			tenant_id INTEGER NOT NULL,
			username TEXT NOT NULL,
			created_at TEXT NOT NULL,
			path TEXT NOT NULL,
			size_bytes INTEGER NOT NULL DEFAULT 0,
			status TEXT NOT NULL,
			error TEXT NOT NULL DEFAULT '',
			integrity_ok INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE INDEX IF NOT EXISTS idx_backups_tenant ON backups(tenant_id, created_at DESC)`,
		`CREATE TABLE IF NOT EXISTS audit_logs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			created_at TEXT NOT NULL,
			actor TEXT NOT NULL,
			action TEXT NOT NULL,
			tenant_id INTEGER,
			tenant_name TEXT NOT NULL DEFAULT '',
			detail TEXT NOT NULL DEFAULT '{}',
			result TEXT NOT NULL,
			error TEXT NOT NULL DEFAULT '',
			ip TEXT NOT NULL DEFAULT '',
			user_agent TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE INDEX IF NOT EXISTS idx_audit_created ON audit_logs(created_at DESC)`,
		`CREATE TABLE IF NOT EXISTS admins (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			username TEXT NOT NULL UNIQUE,
			password_hash TEXT NOT NULL,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			last_login_at TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE TABLE IF NOT EXISTS sessions (
			token_hash TEXT PRIMARY KEY,
			admin_id INTEGER NOT NULL,
			created_at TEXT NOT NULL,
			expires_at TEXT NOT NULL,
			ip TEXT NOT NULL DEFAULT '',
			user_agent TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE INDEX IF NOT EXISTS idx_sessions_expires ON sessions(expires_at)`,
		`CREATE TABLE IF NOT EXISTS versions (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			version TEXT NOT NULL UNIQUE,
			path TEXT NOT NULL,
			sha256 TEXT NOT NULL,
			size_bytes INTEGER NOT NULL,
			uploaded_at TEXT NOT NULL,
			status TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS jobs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			type TEXT NOT NULL,
			status TEXT NOT NULL,
			total INTEGER NOT NULL DEFAULT 0,
			done INTEGER NOT NULL DEFAULT 0,
			failed INTEGER NOT NULL DEFAULT 0,
			payload TEXT NOT NULL DEFAULT '{}',
			result TEXT NOT NULL DEFAULT '{}',
			created_at TEXT NOT NULL,
			started_at TEXT NOT NULL DEFAULT '',
			finished_at TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE TABLE IF NOT EXISTS job_items (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			job_id INTEGER NOT NULL,
			tenant_id INTEGER,
			name TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL,
			error TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE INDEX IF NOT EXISTS idx_job_items_job ON job_items(job_id)`,
		`CREATE TABLE IF NOT EXISTS settings (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS guest_reports (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			tenant_id INTEGER NOT NULL,
			tenant_name TEXT NOT NULL DEFAULT '',
			message TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			ip TEXT NOT NULL DEFAULT '',
			handled_at TEXT NOT NULL DEFAULT '',
			handled_by TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE INDEX IF NOT EXISTS idx_guest_reports_created ON guest_reports(created_at DESC)`,
		`CREATE TABLE IF NOT EXISTS metric_samples (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			tenant_id INTEGER NOT NULL,
			at TEXT NOT NULL,
			cpu_percent REAL NOT NULL DEFAULT 0,
			memory_bytes INTEGER NOT NULL DEFAULT 0,
			pids INTEGER NOT NULL DEFAULT 0,
			disk_bytes INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE INDEX IF NOT EXISTS idx_metric_samples_tenant ON metric_samples(tenant_id, at)`,
		`CREATE INDEX IF NOT EXISTS idx_metric_samples_at ON metric_samples(at)`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.Exec(stmt); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
	}
	// Databases created before last_active_at existed need the column added;
	// SQLite has no "ADD COLUMN IF NOT EXISTS", so check first.
	if err := s.addColumnIfMissing("tenants", "last_active_at", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	// Existing tenants keep SSH off until an operator turns it on: upgrading
	// the platform must not silently open a login shell on old accounts.
	if err := s.addColumnIfMissing("tenants", "ssh_enabled", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	// Existing tenants start without a disk limit, the same way they started
	// without SSH: an upgrade must not suddenly stop anyone's instance.
	if err := s.addColumnIfMissing("tenants", "disk_quota_gb", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	return nil
}

// addColumnIfMissing is a tiny migration helper for columns added after the
// first release. ALTER TABLE ADD COLUMN is cheap and never rewrites rows.
func (s *Store) addColumnIfMissing(table, column, decl string) error {
	rows, err := s.db.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	found := false
	for rows.Next() {
		var name string
		if scanErr := rows.Scan(&name); scanErr != nil {
			return scanErr
		}
		if name == column {
			found = true
		}
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return rowsErr
	}
	if found {
		return nil
	}
	_, err = s.db.Exec(`ALTER TABLE ` + table + ` ADD COLUMN ` + column + ` ` + decl)
	return err
}

func now() string { return time.Now().Format(time.RFC3339) }

func (s *Store) LoadSettings(def domain.Settings) (domain.Settings, error) {
	var raw string
	err := s.db.QueryRow(`SELECT value FROM settings WHERE key='platform'`).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return def, nil
	}
	if err != nil {
		return def, err
	}
	out := def
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return def, err
	}
	return out, nil
}

func (s *Store) SaveSettings(v domain.Settings) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO settings(key, value) VALUES('platform', ?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value`, string(data))
	return err
}

// LoadTenantConfig returns the unified config entries pushed to instances. It
// lives in its own settings row because it is a list: keeping it inside the
// scalar Settings value would make Settings non-comparable for no reason.
func (s *Store) LoadTenantConfig() ([]domain.ConfigEntry, error) {
	var raw string
	err := s.db.QueryRow(`SELECT value FROM settings WHERE key='tenant_config'`).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return []domain.ConfigEntry{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := []domain.ConfigEntry{}
	// Broken JSON is treated as "no unified config" rather than an error: the
	// row is advisory data, and failing here would block the whole platform
	// from starting over one bad value.
	if uErr := json.Unmarshal([]byte(raw), &out); uErr != nil {
		return []domain.ConfigEntry{}, nil //nolint:nilerr // see above
	}
	return out, nil
}

func (s *Store) SaveTenantConfig(entries []domain.ConfigEntry) error {
	if entries == nil {
		entries = []domain.ConfigEntry{}
	}
	data, err := json.Marshal(entries)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO settings(key, value) VALUES('tenant_config', ?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value`, string(data))
	return err
}

func (s *Store) Ping() error { return s.db.Ping() }
