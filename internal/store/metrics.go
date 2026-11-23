package store

import (
	"time"

	"clawbench-fleet/internal/domain"
)

// AddMetricSample records one usage point. Samples are written by a slow
// background loop, so the table stays small enough for SQLite to answer the
// trend queries quickly.
func (s *Store) AddMetricSample(tenantID int64, m domain.MetricSample) error {
	_, err := s.db.Exec(`INSERT INTO metric_samples(tenant_id, at, cpu_percent, memory_bytes, pids, disk_bytes)
		VALUES(?,?,?,?,?,?)`,
		tenantID, m.At, m.CPUPercent, m.MemoryBytes, m.PIDs, m.DiskBytes)
	return err
}

// MetricHistory returns the samples for tenantID newer than since, oldest
// first, capped at limit rows.
func (s *Store) MetricHistory(tenantID int64, since time.Time, limit int) ([]domain.MetricSample, error) {
	if limit < 1 {
		limit = 1000
	}
	rows, err := s.db.Query(`SELECT at, cpu_percent, memory_bytes, pids, disk_bytes
		FROM metric_samples WHERE tenant_id=? AND at>=? ORDER BY at LIMIT ?`,
		tenantID, since.Format(time.RFC3339), limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []domain.MetricSample{}
	for rows.Next() {
		var m domain.MetricSample
		if err := rows.Scan(&m.At, &m.CPUPercent, &m.MemoryBytes, &m.PIDs, &m.DiskBytes); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// PurgeMetricSamples drops history older than cutoff and returns how many rows
// went away.
func (s *Store) PurgeMetricSamples(cutoff time.Time) (int64, error) {
	res, err := s.db.Exec(`DELETE FROM metric_samples WHERE at<?`, cutoff.Format(time.RFC3339))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// PurgeMetricSamplesForTenant removes a single tenant's history, used when the
// tenant is deleted for good.
func (s *Store) PurgeMetricSamplesForTenant(tenantID int64) error {
	_, err := s.db.Exec(`DELETE FROM metric_samples WHERE tenant_id=?`, tenantID)
	return err
}
