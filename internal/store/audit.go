package store

import (
	"strings"

	"clawbench-fleet/internal/domain"
)

func (s *Store) AddAudit(e *domain.AuditEntry) error {
	if e.CreatedAt == "" {
		e.CreatedAt = now()
	}
	res, err := s.db.Exec(`INSERT INTO audit_logs(created_at, actor, action, tenant_id, tenant_name, detail, result, error, ip, user_agent)
		VALUES(?,?,?,?,?,?,?,?,?,?)`,
		e.CreatedAt, e.Actor, e.Action, e.TenantID, e.TenantName, e.Detail, e.Result, e.Error, e.IP, e.UserAgent)
	if err != nil {
		return err
	}
	e.ID, _ = res.LastInsertId()
	return nil
}

type AuditFilter struct {
	Actor    string
	Action   string
	Tenant   string
	From     string
	To       string
	Page     int
	PageSize int
}

func (s *Store) ListAudit(f AuditFilter) ([]domain.AuditEntry, int, error) {
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
	if f.Actor != "" {
		where = append(where, "actor=?")
		args = append(args, f.Actor)
	}
	if f.Action != "" {
		where = append(where, "action=?")
		args = append(args, f.Action)
	}
	if f.Tenant != "" {
		where = append(where, "(tenant_name LIKE ? OR tenant_id=?)")
		args = append(args, "%"+f.Tenant+"%", f.Tenant)
	}
	if f.From != "" {
		where = append(where, "created_at>=?")
		args = append(args, f.From)
	}
	if f.To != "" {
		where = append(where, "created_at<=?")
		args = append(args, f.To)
	}
	clause := strings.Join(where, " AND ")
	var total int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE `+clause, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, f.PageSize, (f.Page-1)*f.PageSize)
	rows, err := s.db.Query(`SELECT id, created_at, actor, action, tenant_id, tenant_name, detail, result, error, ip, user_agent
		FROM audit_logs WHERE `+clause+` ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = rows.Close() }()
	var out []domain.AuditEntry
	for rows.Next() {
		var e domain.AuditEntry
		if err := rows.Scan(&e.ID, &e.CreatedAt, &e.Actor, &e.Action, &e.TenantID, &e.TenantName, &e.Detail, &e.Result, &e.Error, &e.IP, &e.UserAgent); err != nil {
			return nil, 0, err
		}
		out = append(out, e)
	}
	return out, total, rows.Err()
}

func (s *Store) PurgeAuditBefore(cutoff string) (int64, error) {
	res, err := s.db.Exec(`DELETE FROM audit_logs WHERE created_at < ?`, cutoff)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (s *Store) RecentAudit(limit int) ([]domain.AuditEntry, error) {
	if limit < 1 {
		limit = 10
	}
	rows, err := s.db.Query(`SELECT id, created_at, actor, action, tenant_id, tenant_name, detail, result, error, ip, user_agent
		FROM audit_logs ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []domain.AuditEntry
	for rows.Next() {
		var e domain.AuditEntry
		if err := rows.Scan(&e.ID, &e.CreatedAt, &e.Actor, &e.Action, &e.TenantID, &e.TenantName, &e.Detail, &e.Result, &e.Error, &e.IP, &e.UserAgent); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
