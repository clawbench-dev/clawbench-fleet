package fleet

import (
	"errors"
	"sort"

	"clawbench-fleet/internal/domain"
	"clawbench-fleet/internal/store"
)

type Overview struct {
	Host           HostStats           `json:"host"`
	Counts         map[string]int      `json:"counts"`
	Total          int                 `json:"total"`
	Anomalies      []TenantDTO         `json:"anomalies"`
	DiskWarnings   []TenantDTO         `json:"disk_warnings"`
	RecentAudit    []domain.AuditEntry `json:"recent_audit"`
	RecentJobs     []domain.Job        `json:"recent_jobs"`
	CurrentVersion string              `json:"current_version"`
	Orphans        []string            `json:"orphans"`
	Mode           string              `json:"mode"`
	BackupEnabled  bool                `json:"backup_enabled"`
}

func (s *Service) Overview() (*Overview, error) {
	counts, err := s.Store.CountByStatus()
	if err != nil {
		return nil, err
	}
	total := 0
	for _, n := range counts {
		total += n
	}
	anomalies, _, err := s.ListTenants(store.TenantFilter{Status: domain.StatusError, Page: 1, PageSize: 20})
	if err != nil {
		return nil, err
	}
	audit, err := s.Store.RecentAudit(12)
	if err != nil {
		return nil, err
	}
	jobs, _ := s.Store.RecentJobs(5)
	if jobs == nil {
		jobs = []domain.Job{}
	}
	if audit == nil {
		audit = []domain.AuditEntry{}
	}
	orphans := s.Orphans()
	if orphans == nil {
		orphans = []string{}
	}
	diskWarnings := s.diskWarnings()
	currentVersion := ""
	if v, err := s.Store.CurrentVersion(); err == nil {
		currentVersion = v.Version
	} else if !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	return &Overview{
		Host:           s.HostStats(),
		Counts:         counts,
		Total:          total,
		Anomalies:      anomalies,
		DiskWarnings:   diskWarnings,
		RecentAudit:    audit,
		RecentJobs:     jobs,
		CurrentVersion: currentVersion,
		Orphans:        orphans,
		Mode:           s.Sys.Mode(),
		BackupEnabled:  s.Settings().BackupEnabled,
	}, nil
}

// diskWarnings lists tenants above the configured disk threshold. Disk is
// intentionally not hard-limited (a real project can be large), so this is the
// "alert" half of the agreed policy.
func (s *Service) diskWarnings() []TenantDTO {
	st := s.Settings()
	out := []TenantDTO{}
	if st.DiskWarnGB <= 0 {
		return out
	}
	all, err := s.allTenants()
	if err != nil {
		return out
	}
	limit := int64(st.DiskWarnGB) * 1024 * 1024 * 1024
	for _, t := range all {
		if t.Status == domain.StatusTrashed || t.DiskBytes < limit {
			continue
		}
		out = append(out, s.TenantDTO(t))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].DiskBytes > out[j].DiskBytes })
	return out
}
