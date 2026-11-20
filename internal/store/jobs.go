package store

import (
	"database/sql"
	"errors"

	"clawbench-fleet/internal/domain"
)

func (s *Store) CreateJob(kind, payload string, total int) (*domain.Job, error) {
	ts := now()
	res, err := s.db.Exec(`INSERT INTO jobs(type, status, total, done, failed, payload, result, created_at)
		VALUES(?,?,?,0,0,?,?,?)`, kind, domain.JobPending, total, payload, "{}", ts)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return &domain.Job{ID: id, Type: kind, Status: domain.JobPending, Total: total, Payload: payload, Result: "{}", CreatedAt: ts}, nil
}

func (s *Store) GetJob(id int64) (*domain.Job, error) {
	var j domain.Job
	err := s.db.QueryRow(`SELECT id, type, status, total, done, failed, payload, result, created_at, started_at, finished_at
		FROM jobs WHERE id=?`, id).
		Scan(&j.ID, &j.Type, &j.Status, &j.Total, &j.Done, &j.Failed, &j.Payload, &j.Result, &j.CreatedAt, &j.StartedAt, &j.FinishedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	items, err := s.ListJobItems(id)
	if err != nil {
		return nil, err
	}
	j.Items = items
	return &j, nil
}

func (s *Store) UpdateJobProgress(id int64, done, failed int) error {
	_, err := s.db.Exec(`UPDATE jobs SET done=?, failed=? WHERE id=?`, done, failed, id)
	return err
}

func (s *Store) MarkJobRunning(id int64) error {
	_, err := s.db.Exec(`UPDATE jobs SET status=?, started_at=? WHERE id=?`, domain.JobRunning, now(), id)
	return err
}

func (s *Store) FinishJob(id int64, status, result string) error {
	_, err := s.db.Exec(`UPDATE jobs SET status=?, result=?, finished_at=? WHERE id=?`, status, result, now(), id)
	return err
}

func (s *Store) AddJobItem(jobID int64, tenantID int64, name string) (int64, error) {
	res, err := s.db.Exec(`INSERT INTO job_items(job_id, tenant_id, name, status) VALUES(?,?,?,?)`,
		jobID, tenantID, name, domain.JobPending)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) UpdateJobItem(id int64, status, errText string) error {
	_, err := s.db.Exec(`UPDATE job_items SET status=?, error=? WHERE id=?`, status, errText, id)
	return err
}

func (s *Store) ListJobItems(jobID int64) ([]domain.JobItem, error) {
	rows, err := s.db.Query(`SELECT id, job_id, tenant_id, name, status, error FROM job_items WHERE job_id=? ORDER BY id`, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.JobItem
	for rows.Next() {
		var it domain.JobItem
		if err := rows.Scan(&it.ID, &it.JobID, &it.TenantID, &it.Name, &it.Status, &it.Error); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

func (s *Store) RecentJobs(limit int) ([]domain.Job, error) {
	if limit < 1 {
		limit = 10
	}
	rows, err := s.db.Query(`SELECT id, type, status, total, done, failed, payload, result, created_at, started_at, finished_at
		FROM jobs ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Job
	for rows.Next() {
		var j domain.Job
		if err := rows.Scan(&j.ID, &j.Type, &j.Status, &j.Total, &j.Done, &j.Failed, &j.Payload, &j.Result, &j.CreatedAt, &j.StartedAt, &j.FinishedAt); err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// MarkInterruptedJobs is called on startup: a job that was running when the
// platform died cannot be resumed safely, so it is reported as failed.
func (s *Store) MarkInterruptedJobs() (int64, error) {
	res, err := s.db.Exec(`UPDATE jobs SET status=?, finished_at=?, result=?
		WHERE status IN (?,?)`,
		domain.JobFailed, now(), `{"error":"platform restarted before the job finished"}`,
		domain.JobPending, domain.JobRunning)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
