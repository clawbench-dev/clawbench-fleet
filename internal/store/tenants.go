package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"clawbench-fleet/internal/domain"
)

const tenantCols = `id, username, display_name, uid, gid, port, status, version,
	cpu_quota, memory_max, tasks_max, home_dir, data_dir, unit_name, notes, ssh_enabled,
	disk_bytes, disk_quota_gb, disk_checked_at, last_health_at, last_health_ok, last_active_at, last_error,
	created_at, updated_at, deleted_at, trash_path`

func scanTenant(row interface{ Scan(...any) error }) (*domain.Tenant, error) {
	var t domain.Tenant
	err := row.Scan(
		&t.ID, &t.Username, &t.DisplayName, &t.UID, &t.GID, &t.Port, &t.Status, &t.Version,
		&t.CPUQuota, &t.MemoryMax, &t.TasksMax, &t.HomeDir, &t.DataDir, &t.UnitName, &t.Notes,
		&t.SSHEnabled, &t.DiskBytes, &t.DiskQuotaGB, &t.DiskChecked, &t.LastHealthAt, &t.LastHealthOK,
		&t.LastActiveAt, &t.LastError, &t.CreatedAt, &t.UpdatedAt, &t.DeletedAt, &t.TrashPath,
	)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func (s *Store) CreateTenant(t *domain.Tenant) error {
	if t.CreatedAt == "" {
		t.CreatedAt = now()
	}
	t.UpdatedAt = now()
	res, err := s.db.Exec(`INSERT INTO tenants
		(username, display_name, uid, gid, port, status, version, cpu_quota, memory_max, tasks_max,
		 home_dir, data_dir, unit_name, notes, ssh_enabled, disk_bytes, disk_quota_gb, disk_checked_at,
		 last_health_at, last_health_ok, last_active_at, last_error, created_at, updated_at, deleted_at, trash_path)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		t.Username, t.DisplayName, t.UID, t.GID, t.Port, t.Status, t.Version, t.CPUQuota,
		t.MemoryMax, t.TasksMax, t.HomeDir, t.DataDir, t.UnitName, t.Notes, t.SSHEnabled,
		t.DiskBytes, t.DiskQuotaGB, t.DiskChecked, t.LastHealthAt, t.LastHealthOK, t.LastActiveAt,
		t.LastError, t.CreatedAt, t.UpdatedAt, t.DeletedAt, t.TrashPath)
	if err != nil {
		return err
	}
	t.ID, _ = res.LastInsertId()
	return nil
}

func (s *Store) UpdateTenant(t *domain.Tenant) error {
	t.UpdatedAt = now()
	_, err := s.db.Exec(`UPDATE tenants SET
		display_name=?, uid=?, gid=?, port=?, status=?, version=?, cpu_quota=?, memory_max=?,
		tasks_max=?, home_dir=?, data_dir=?, unit_name=?, notes=?, ssh_enabled=?, disk_bytes=?,
		disk_quota_gb=?, disk_checked_at=?, last_health_at=?, last_health_ok=?, last_active_at=?,
		last_error=?, updated_at=?, deleted_at=?, trash_path=?
		WHERE id=?`,
		t.DisplayName, t.UID, t.GID, t.Port, t.Status, t.Version, t.CPUQuota, t.MemoryMax,
		t.TasksMax, t.HomeDir, t.DataDir, t.UnitName, t.Notes, t.SSHEnabled, t.DiskBytes,
		t.DiskQuotaGB, t.DiskChecked, t.LastHealthAt, t.LastHealthOK, t.LastActiveAt,
		t.LastError, t.UpdatedAt, t.DeletedAt, t.TrashPath, t.ID)
	return err
}

func (s *Store) GetTenant(id int64) (*domain.Tenant, error) {
	t, err := scanTenant(s.db.QueryRow(`SELECT `+tenantCols+` FROM tenants WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return t, err
}

func (s *Store) GetTenantByUsername(username string) (*domain.Tenant, error) {
	t, err := scanTenant(s.db.QueryRow(`SELECT `+tenantCols+` FROM tenants WHERE username=?`, username))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return t, err
}

type TenantFilter struct {
	Query          string
	Status         string
	Page           int
	PageSize       int
	IncludeTrashed bool
}

func (s *Store) ListTenants(f TenantFilter) ([]domain.Tenant, int, error) {
	if f.Page < 1 {
		f.Page = 1
	}
	if f.PageSize < 1 {
		f.PageSize = 50
	}
	if f.PageSize > 200 {
		f.PageSize = 200
	}
	where := []string{"1=1"}
	args := []any{}
	if f.Status != "" {
		where = append(where, "status=?")
		args = append(args, f.Status)
	} else if !f.IncludeTrashed {
		where = append(where, "status<>?")
		args = append(args, domain.StatusTrashed)
	}
	if q := strings.TrimSpace(f.Query); q != "" {
		where = append(where, "(username LIKE ? OR display_name LIKE ? OR notes LIKE ?)")
		like := "%" + q + "%"
		args = append(args, like, like, like)
	}
	clause := strings.Join(where, " AND ")

	var total int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM tenants WHERE `+clause, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, f.PageSize, (f.Page-1)*f.PageSize)
	rows, err := s.db.Query(`SELECT `+tenantCols+` FROM tenants WHERE `+clause+
		` ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = rows.Close() }()
	out := []domain.Tenant{}
	for rows.Next() {
		t, err := scanTenant(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *t)
	}
	return out, total, rows.Err()
}

func (s *Store) ListTenantsByStatus(statuses ...string) ([]domain.Tenant, error) {
	if len(statuses) == 0 {
		return nil, nil
	}
	ph := strings.TrimSuffix(strings.Repeat("?,", len(statuses)), ",")
	args := make([]any, len(statuses))
	for i, st := range statuses {
		args[i] = st
	}
	rows, err := s.db.Query(`SELECT `+tenantCols+` FROM tenants WHERE status IN (`+ph+`) ORDER BY id`, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []domain.Tenant
	for rows.Next() {
		t, err := scanTenant(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

func (s *Store) DeleteTenant(id int64) error {
	_, err := s.db.Exec(`DELETE FROM tenants WHERE id=?`, id)
	return err
}

// AllTenantUIDs returns every UID ever recorded, including trashed tenants.
// UID allocation must not reuse one while its data still exists on disk.
func (s *Store) AllTenantUIDs() (map[int]bool, error) {
	rows, err := s.db.Query(`SELECT uid FROM tenants WHERE uid > 0`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[int]bool{}
	for rows.Next() {
		var uid int
		if err := rows.Scan(&uid); err != nil {
			return nil, err
		}
		out[uid] = true
	}
	return out, rows.Err()
}

func (s *Store) SetTenantStatus(id int64, status, lastError string) error {
	_, err := s.db.Exec(`UPDATE tenants SET status=?, last_error=?, updated_at=? WHERE id=?`,
		status, lastError, now(), id)
	return err
}

func (s *Store) SetTenantHealth(id int64, ok bool, version, at string) error {
	_, err := s.db.Exec(`UPDATE tenants SET last_health_ok=?, last_health_at=?, version=?, updated_at=? WHERE id=?`,
		ok, at, version, now(), id)
	return err
}

func (s *Store) SetTenantDisk(id int64, bytes int64, at string) error {
	_, err := s.db.Exec(`UPDATE tenants SET disk_bytes=?, disk_checked_at=?, updated_at=? WHERE id=?`,
		bytes, at, now(), id)
	return err
}

func (s *Store) SetTenantVersion(id int64, version string) error {
	_, err := s.db.Exec(`UPDATE tenants SET version=?, updated_at=? WHERE id=?`, version, now(), id)
	return err
}

// SetTenantLastActive records the newest message time seen in the tenant's own
// database. Unlike UpdateTenant it does not touch updated_at: activity is
// observed passively and must not look like an admin change to the record.
func (s *Store) SetTenantLastActive(id int64, at string) error {
	_, err := s.db.Exec(`UPDATE tenants SET last_active_at=? WHERE id=?`, at, id)
	return err
}

func (s *Store) CountByStatus() (map[string]int, error) {
	rows, err := s.db.Query(`SELECT status, COUNT(*) FROM tenants GROUP BY status`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]int{}
	for rows.Next() {
		var status string
		var n int
		if err := rows.Scan(&status, &n); err != nil {
			return nil, err
		}
		out[status] = n
	}
	return out, rows.Err()
}

func (s *Store) TrashedTenants() ([]domain.Tenant, error) {
	return s.ListTenantsByStatus(domain.StatusTrashed)
}

func (s *Store) ErrNoSuchTenant(id int64) error {
	return fmt.Errorf("tenant %d: %w", id, ErrNotFound)
}
