package store

import (
	"database/sql"
	"errors"

	"clawbench-fleet/internal/domain"
)

func (s *Store) CountAdmins() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM admins`).Scan(&n)
	return n, err
}

func (s *Store) CreateAdmin(username, passwordHash string) (*domain.Admin, error) {
	ts := now()
	res, err := s.db.Exec(`INSERT INTO admins(username, password_hash, created_at, updated_at) VALUES(?,?,?,?)`,
		username, passwordHash, ts, ts)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return &domain.Admin{ID: id, Username: username, PasswordHash: passwordHash, CreatedAt: ts, UpdatedAt: ts}, nil
}

func (s *Store) GetAdminByUsername(username string) (*domain.Admin, error) {
	return s.scanAdmin(s.db.QueryRow(`SELECT id, username, password_hash, created_at, updated_at, last_login_at
		FROM admins WHERE username=?`, username))
}

func (s *Store) GetAdminByID(id int64) (*domain.Admin, error) {
	return s.scanAdmin(s.db.QueryRow(`SELECT id, username, password_hash, created_at, updated_at, last_login_at
		FROM admins WHERE id=?`, id))
}

func (s *Store) scanAdmin(row interface{ Scan(...any) error }) (*domain.Admin, error) {
	var a domain.Admin
	err := row.Scan(&a.ID, &a.Username, &a.PasswordHash, &a.CreatedAt, &a.UpdatedAt, &a.LastLoginAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &a, nil
}

func (s *Store) UpdateAdminPassword(id int64, hash string) error {
	_, err := s.db.Exec(`UPDATE admins SET password_hash=?, updated_at=? WHERE id=?`, hash, now(), id)
	return err
}

func (s *Store) TouchAdminLogin(id int64) error {
	_, err := s.db.Exec(`UPDATE admins SET last_login_at=? WHERE id=?`, now(), id)
	return err
}

func (s *Store) CreateSession(sess *domain.Session) error {
	_, err := s.db.Exec(`INSERT INTO sessions(token_hash, admin_id, created_at, expires_at, ip, user_agent)
		VALUES(?,?,?,?,?,?)`, sess.TokenHash, sess.AdminID, sess.CreatedAt, sess.ExpiresAt, sess.IP, sess.UserAgent)
	return err
}

func (s *Store) GetSession(tokenHash string) (*domain.Session, error) {
	var sess domain.Session
	err := s.db.QueryRow(`SELECT token_hash, admin_id, created_at, expires_at, ip, user_agent
		FROM sessions WHERE token_hash=?`, tokenHash).
		Scan(&sess.TokenHash, &sess.AdminID, &sess.CreatedAt, &sess.ExpiresAt, &sess.IP, &sess.UserAgent)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &sess, nil
}

func (s *Store) DeleteSession(tokenHash string) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE token_hash=?`, tokenHash)
	return err
}

func (s *Store) DeleteSessionsForAdmin(adminID int64) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE admin_id=?`, adminID)
	return err
}

func (s *Store) PurgeExpiredSessions(cutoff string) (int64, error) {
	res, err := s.db.Exec(`DELETE FROM sessions WHERE expires_at < ?`, cutoff)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
