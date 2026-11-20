package store

import (
	"database/sql"
	"errors"

	"clawbench-fleet/internal/domain"
)

func (s *Store) CreateGuestReport(r *domain.GuestReport) error {
	if r.CreatedAt == "" {
		r.CreatedAt = now()
	}
	res, err := s.db.Exec(`INSERT INTO guest_reports
		(tenant_id, tenant_name, message, created_at, ip, handled_at, handled_by)
		VALUES (?,?,?,?,?,'','')`,
		r.TenantID, r.TenantName, r.Message, r.CreatedAt, r.IP)
	if err != nil {
		return err
	}
	r.ID, _ = res.LastInsertId()
	return nil
}

// ListGuestReports returns reports newest first. openOnly hides the ones an
// admin has already marked as handled.
func (s *Store) ListGuestReports(openOnly bool, limit int) ([]domain.GuestReport, error) {
	if limit < 1 {
		limit = 50
	}
	query := `SELECT id, tenant_id, tenant_name, message, created_at, ip, handled_at, handled_by
		FROM guest_reports`
	if openOnly {
		query += ` WHERE handled_at=''`
	}
	query += ` ORDER BY id DESC LIMIT ?`
	rows, err := s.db.Query(query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.GuestReport{}
	for rows.Next() {
		var r domain.GuestReport
		if err := rows.Scan(&r.ID, &r.TenantID, &r.TenantName, &r.Message, &r.CreatedAt,
			&r.IP, &r.HandledAt, &r.HandledBy); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) GetGuestReport(id int64) (*domain.GuestReport, error) {
	var r domain.GuestReport
	err := s.db.QueryRow(`SELECT id, tenant_id, tenant_name, message, created_at, ip, handled_at, handled_by
		FROM guest_reports WHERE id=?`, id).
		Scan(&r.ID, &r.TenantID, &r.TenantName, &r.Message, &r.CreatedAt, &r.IP, &r.HandledAt, &r.HandledBy)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

func (s *Store) CountOpenGuestReports() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM guest_reports WHERE handled_at=''`).Scan(&n)
	return n, err
}

// HasOpenGuestReportSince reports whether the tenant already has an unhandled
// report newer than since, so repeated clicks do not pile up.
func (s *Store) HasOpenGuestReportSince(tenantID int64, since string) (bool, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM guest_reports
		WHERE tenant_id=? AND handled_at='' AND created_at>=?`, tenantID, since).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return n > 0, err
}

func (s *Store) ResolveGuestReport(id int64, by, at string) error {
	res, err := s.db.Exec(`UPDATE guest_reports SET handled_at=?, handled_by=?
		WHERE id=? AND handled_at=''`, at, by, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
