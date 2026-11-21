package store

import (
	"database/sql"
	"errors"
	"fmt"
	"sort"
)

// SeedPorts makes sure every port in [start,end] has a row. Existing rows are
// left untouched so a restart never frees a port that is still in use.
func (s *Store) SeedPorts(start, end int) error {
	if start < 1 || end > 65535 || start > end {
		return fmt.Errorf("invalid port range %d-%d", start, end)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.Prepare(`INSERT OR IGNORE INTO ports(port, tenant_id, state, updated_at) VALUES(?, NULL, 'free', ?)`)
	if err != nil {
		return err
	}
	defer func() { _ = stmt.Close() }()
	ts := now()
	for p := start; p <= end; p++ {
		if _, err := stmt.Exec(p, ts); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// AllocatePorts reserves count consecutive free ports and assigns them to
// tenantID. All-or-nothing: a partial group is never returned.
func (s *Store) AllocatePorts(start, end, count int, tenantID int64) ([]int, error) {
	if count < 1 {
		return nil, errors.New("count must be positive")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	rows, err := tx.Query(`SELECT port FROM ports WHERE state='free' AND port BETWEEN ? AND ? ORDER BY port`, start, end)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var free []int
	for rows.Next() {
		var p int
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		free = append(free, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Ints(free)

	group := findRun(free, count)
	if group == nil {
		return nil, ErrPortExhausted
	}
	ts := now()
	for _, p := range group {
		res, err := tx.Exec(`UPDATE ports SET state='allocated', tenant_id=?, updated_at=? WHERE port=? AND state='free'`,
			tenantID, ts, p)
		if err != nil {
			return nil, err
		}
		n, _ := res.RowsAffected()
		if n != 1 {
			return nil, fmt.Errorf("port %d was taken concurrently", p)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return group, nil
}

func findRun(sorted []int, count int) []int {
	if count <= 0 || len(sorted) < count {
		return nil
	}
	if count == 1 {
		return []int{sorted[0]}
	}
	run := []int{sorted[0]}
	for i := 1; i < len(sorted); i++ {
		if sorted[i] == sorted[i-1]+1 {
			run = append(run, sorted[i])
			if len(run) == count {
				return run
			}
		} else {
			run = []int{sorted[i]}
		}
	}
	return nil
}

func (s *Store) ReservePorts(tenantID int64) error {
	_, err := s.db.Exec(`UPDATE ports SET state='reserved', updated_at=? WHERE tenant_id=?`, now(), tenantID)
	return err
}

func (s *Store) SetPortsAllocated(tenantID int64) error {
	_, err := s.db.Exec(`UPDATE ports SET state='allocated', updated_at=? WHERE tenant_id=?`, now(), tenantID)
	return err
}

func (s *Store) ReleasePorts(tenantID int64) error {
	_, err := s.db.Exec(`UPDATE ports SET state='free', tenant_id=NULL, updated_at=? WHERE tenant_id=?`, now(), tenantID)
	return err
}

// AssignPorts attaches a freshly allocated port group to a tenant. Ports are
// allocated with tenant_id=0 while the tenant row is being created, then
// claimed here in the same critical section.
func (s *Store) AssignPorts(tenantID int64, ports []int) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, p := range ports {
		if _, err := tx.Exec(`UPDATE ports SET tenant_id=? WHERE port=? AND state='allocated'`, tenantID, p); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) PortsForTenant(tenantID int64) ([]int, error) {
	rows, err := s.db.Query(`SELECT port FROM ports WHERE tenant_id=? ORDER BY port`, tenantID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []int
	for rows.Next() {
		var p int
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ClaimPort attaches a specific port to tenantID. Adopted tenants already have
// a port chosen by their existing instance, so the allocator is not used; a
// port owned by another tenant is never stolen.
func (s *Store) ClaimPort(port int, tenantID int64) error {
	if port < 1 || port > 65535 {
		return fmt.Errorf("invalid port %d", port)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	var owner int64
	err = tx.QueryRow(`SELECT COALESCE(tenant_id, 0) FROM ports WHERE port=?`, port).Scan(&owner)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		// Port outside the configured range; record it on demand.
	case err != nil:
		return err
	case owner != 0 && owner != tenantID:
		return fmt.Errorf("端口 %d 已经被其他租户占用", port)
	}
	if _, err := tx.Exec(`INSERT INTO ports(port, tenant_id, state, updated_at) VALUES(?,?,'allocated',?)
		ON CONFLICT(port) DO UPDATE SET tenant_id=excluded.tenant_id, state='allocated', updated_at=excluded.updated_at`,
		port, tenantID, now()); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) PortState(port int) (string, error) {
	var state string
	err := s.db.QueryRow(`SELECT state FROM ports WHERE port=?`, port).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return state, err
}

// FreePortGroups counts how many complete groups of size consecutive free
// ports are available. The import preview uses it to tell the admin how many
// more tenants the server can take before running out of ports.
func (s *Store) FreePortGroups(start, end, size int) (int, error) {
	if size < 1 {
		return 0, nil
	}
	rows, err := s.db.Query(`SELECT port FROM ports WHERE state='free' AND port BETWEEN ? AND ? ORDER BY port`, start, end)
	if err != nil {
		return 0, err
	}
	defer func() { _ = rows.Close() }()
	var free []int
	for rows.Next() {
		var p int
		if err := rows.Scan(&p); err != nil {
			return 0, err
		}
		free = append(free, p)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	groups, run := 0, 0
	for i, p := range free {
		if i > 0 && p == free[i-1]+1 {
			run++
		} else {
			run = 1
		}
		if run == size {
			groups++
			run = 0
		}
	}
	return groups, nil
}
