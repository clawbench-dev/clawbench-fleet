package store

import (
	"database/sql"
	"errors"

	"clawbench-fleet/internal/domain"
)

func (s *Store) CreateBackup(b *domain.Backup) error {
	if b.CreatedAt == "" {
		b.CreatedAt = now()
	}
	res, err := s.db.Exec(`INSERT INTO backups(tenant_id, username, created_at, path, size_bytes, status, error, integrity_ok)
		VALUES(?,?,?,?,?,?,?,?)`,
		b.TenantID, b.Username, b.CreatedAt, b.Path, b.SizeBytes, b.Status, b.Error, b.IntegrityOK)
	if err != nil {
		return err
	}
	b.ID, _ = res.LastInsertId()
	return nil
}

func (s *Store) ListBackups(tenantID int64) ([]domain.Backup, error) {
	rows, err := s.db.Query(`SELECT id, tenant_id, username, created_at, path, size_bytes, status, error, integrity_ok
		FROM backups WHERE tenant_id=? ORDER BY created_at DESC, id DESC`, tenantID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []domain.Backup
	for rows.Next() {
		var b domain.Backup
		if err := rows.Scan(&b.ID, &b.TenantID, &b.Username, &b.CreatedAt, &b.Path, &b.SizeBytes, &b.Status, &b.Error, &b.IntegrityOK); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func (s *Store) GetBackup(id int64) (*domain.Backup, error) {
	var b domain.Backup
	err := s.db.QueryRow(`SELECT id, tenant_id, username, created_at, path, size_bytes, status, error, integrity_ok
		FROM backups WHERE id=?`, id).Scan(&b.ID, &b.TenantID, &b.Username, &b.CreatedAt, &b.Path, &b.SizeBytes, &b.Status, &b.Error, &b.IntegrityOK)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &b, nil
}

func (s *Store) DeleteBackupRecord(id int64) error {
	_, err := s.db.Exec(`DELETE FROM backups WHERE id=?`, id)
	return err
}

func (s *Store) BackupsOlderThan(tenantID int64, cutoff string) ([]domain.Backup, error) {
	rows, err := s.db.Query(`SELECT id, tenant_id, username, created_at, path, size_bytes, status, error, integrity_ok
		FROM backups WHERE tenant_id=? AND created_at < ?`, tenantID, cutoff)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []domain.Backup
	for rows.Next() {
		var b domain.Backup
		if err := rows.Scan(&b.ID, &b.TenantID, &b.Username, &b.CreatedAt, &b.Path, &b.SizeBytes, &b.Status, &b.Error, &b.IntegrityOK); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}
