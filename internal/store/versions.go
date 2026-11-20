package store

import (
	"database/sql"
	"errors"

	"clawbench-fleet/internal/domain"
)

func (s *Store) CreateVersion(v *domain.Version) error {
	if v.UploadedAt == "" {
		v.UploadedAt = now()
	}
	res, err := s.db.Exec(`INSERT INTO versions(version, path, sha256, size_bytes, uploaded_at, status)
		VALUES(?,?,?,?,?,?)`, v.Version, v.Path, v.SHA256, v.SizeBytes, v.UploadedAt, v.Status)
	if err != nil {
		return err
	}
	v.ID, _ = res.LastInsertId()
	return nil
}

func (s *Store) ListVersions() ([]domain.Version, error) {
	rows, err := s.db.Query(`SELECT id, version, path, sha256, size_bytes, uploaded_at, status
		FROM versions ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Version
	for rows.Next() {
		var v domain.Version
		if err := rows.Scan(&v.ID, &v.Version, &v.Path, &v.SHA256, &v.SizeBytes, &v.UploadedAt, &v.Status); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) GetVersion(version string) (*domain.Version, error) {
	var v domain.Version
	err := s.db.QueryRow(`SELECT id, version, path, sha256, size_bytes, uploaded_at, status
		FROM versions WHERE version=?`, version).
		Scan(&v.ID, &v.Version, &v.Path, &v.SHA256, &v.SizeBytes, &v.UploadedAt, &v.Status)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &v, nil
}

func (s *Store) CurrentVersion() (*domain.Version, error) {
	var v domain.Version
	err := s.db.QueryRow(`SELECT id, version, path, sha256, size_bytes, uploaded_at, status
		FROM versions WHERE status='current' LIMIT 1`).
		Scan(&v.ID, &v.Version, &v.Path, &v.SHA256, &v.SizeBytes, &v.UploadedAt, &v.Status)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &v, nil
}

func (s *Store) PreviousVersion() (*domain.Version, error) {
	var v domain.Version
	err := s.db.QueryRow(`SELECT id, version, path, sha256, size_bytes, uploaded_at, status
		FROM versions WHERE status='previous' ORDER BY id DESC LIMIT 1`).
		Scan(&v.ID, &v.Version, &v.Path, &v.SHA256, &v.SizeBytes, &v.UploadedAt, &v.Status)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &v, nil
}

// SetCurrentVersion moves the "current" marker and keeps exactly one previous
// version so rollback has a target.
func (s *Store) SetCurrentVersion(version string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var oldCurrent string
	_ = tx.QueryRow(`SELECT version FROM versions WHERE status='current' LIMIT 1`).Scan(&oldCurrent)

	if _, err := tx.Exec(`UPDATE versions SET status='installed' WHERE status IN ('current','previous')`); err != nil {
		return err
	}
	if oldCurrent != "" && oldCurrent != version {
		if _, err := tx.Exec(`UPDATE versions SET status='previous' WHERE version=?`, oldCurrent); err != nil {
			return err
		}
	}
	res, err := tx.Exec(`UPDATE versions SET status='current' WHERE version=?`, version)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return ErrNotFound
	}
	return tx.Commit()
}

func (s *Store) DeleteVersion(version string) error {
	_, err := s.db.Exec(`DELETE FROM versions WHERE version=?`, version)
	return err
}
