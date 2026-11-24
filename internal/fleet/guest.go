package fleet

import (
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"clawbench-fleet/internal/domain"
	"clawbench-fleet/internal/store"
)

var ErrGuestDisabled = errors.New("员工自助页还没有开启")

// GuestInstance is the slice of a tenant that may be shown to everyone.
// It mirrors the columns of the admin tenant list minus everything that only
// makes sense inside the machine: no home directory, no UID/GID, no notes, no
// systemd unit, no last error.
type GuestInstance struct {
	ID           int64          `json:"id"`
	Username     string         `json:"username"`
	DisplayName  string         `json:"display_name"`
	Port         int            `json:"port"`
	Address      string         `json:"address"`
	Status       string         `json:"status"`
	Version      string         `json:"version"`
	Metrics      domain.Metrics `json:"metrics"`
	DiskBytes    int64          `json:"disk_bytes"`
	DiskChecked  string         `json:"disk_checked_at"`
	LastActiveAt string         `json:"last_active_at"`
	DiskWarn     bool           `json:"disk_warn"`
}

type GuestPage struct {
	Enabled  bool            `json:"enabled"`
	Notice   string          `json:"notice"`
	Help     string          `json:"help"`
	Items    []GuestInstance `json:"items"`
	Total    int             `json:"total"`
	Page     int             `json:"page"`
	PageSize int             `json:"page_size"`
}

// GuestQuery is what the public page may filter on. It is a subset of the
// admin listing so the same table can be driven by either endpoint.
type GuestQuery struct {
	Query    string
	Status   string
	Page     int
	PageSize int
}

func (s *Service) GuestPage() (*GuestPage, error) {
	return s.GuestPageFor("", GuestQuery{})
}

// GuestPageFor is GuestPage with the visitor's browser host used to build the
// instance addresses.
func (s *Service) GuestPageFor(browserHost string, q GuestQuery) (*GuestPage, error) {
	if q.Page < 1 {
		q.Page = 1
	}
	if q.PageSize < 1 {
		q.PageSize = 20
	}
	if q.PageSize > 200 {
		q.PageSize = 200
	}
	// A huge page number would overflow the offset arithmetic and quietly fall
	// back to the first page, which looks like a working (but wrong) answer.
	if q.Page > 10000 {
		q.Page = 10000
	}
	switch q.Status {
	case domain.StatusRunning, domain.StatusStopped, domain.StatusError:
	default:
		// The public page only offers these three filters; anything else is
		// either internal (deleting, trashed) or a typo.
		q.Status = ""
	}
	st := s.Settings()
	page := &GuestPage{
		Enabled:  st.GuestEnabled,
		Notice:   st.GuestNotice,
		Help:     st.GuestHelp,
		Items:    []GuestInstance{},
		Page:     q.Page,
		PageSize: q.PageSize,
	}
	if !st.GuestEnabled {
		return page, nil
	}
	// Half-deleted instances are filtered in SQL so they neither show up nor
	// occupy a slot on the page; notes are left out of the search because they
	// are the admin's internal bookkeeping.
	items, total, err := s.Store.ListTenants(store.TenantFilter{
		Query:        q.Query,
		Status:       q.Status,
		Page:         q.Page,
		PageSize:     q.PageSize,
		HideDeleting: true,
		SkipNotes:    true,
	})
	if err != nil {
		return nil, err
	}
	diskLimit := int64(st.DiskWarnGB) * 1024 * 1024 * 1024
	for _, t := range items {
		page.Items = append(page.Items, s.guestInstance(t, browserHost, diskLimit))
	}
	page.Total = total
	return page, nil
}

func (s *Service) guestInstance(t domain.Tenant, browserHost string, diskLimit int64) GuestInstance {
	return GuestInstance{
		ID:           t.ID,
		Username:     t.Username,
		DisplayName:  t.DisplayName,
		Port:         t.Port,
		Address:      s.AddressFor(t, browserHost),
		Status:       t.Status,
		Version:      t.Version,
		Metrics:      s.GetMetrics(t.ID),
		DiskBytes:    t.DiskBytes,
		DiskChecked:  t.DiskChecked,
		LastActiveAt: t.LastActiveAt,
		DiskWarn:     diskLimit > 0 && t.DiskBytes >= diskLimit,
	}
}

// SubmitGuestReport records a "my instance won't open" note. Duplicate clicks
// inside ten minutes are treated as success so the button can be pressed twice.
func (s *Service) SubmitGuestReport(tenantID int64, message, ip string) error {
	if !s.Settings().GuestEnabled {
		return ErrGuestDisabled
	}
	t, err := s.tenantByID(tenantID)
	if err != nil {
		return errors.New("找不到这个实例，请刷新页面后重试")
	}
	if t.Status == domain.StatusTrashed || t.Status == domain.StatusDeleting {
		return errors.New("找不到这个实例，请刷新页面后重试")
	}
	message = strings.TrimSpace(message)
	if utf8.RuneCountInString(message) > 300 {
		return errors.New("说明最多 300 个字")
	}
	nowT := time.Now()
	since := nowT.Add(-10 * time.Minute).Format(time.RFC3339)
	if dup, _ := s.Store.HasOpenGuestReportSince(t.ID, since); dup {
		return nil
	}
	name := t.DisplayName
	if name == "" {
		name = t.Username
	}
	return s.Store.CreateGuestReport(&domain.GuestReport{
		TenantID:   t.ID,
		TenantName: name,
		Message:    message,
		CreatedAt:  nowT.Format(time.RFC3339),
		IP:         ip,
	})
}

func (s *Service) ListGuestReports(openOnly bool, limit int) ([]domain.GuestReport, error) {
	return s.Store.ListGuestReports(openOnly, limit)
}

func (s *Service) ResolveGuestReport(id int64, ac AuditContext) error {
	found, err := s.Store.GetGuestReport(id)
	if err != nil {
		return errors.New("这条报障记录不存在")
	}
	if err := s.Store.ResolveGuestReport(id, auditActor(ac.Actor), time.Now().Format(time.RFC3339)); err != nil {
		return err
	}
	var t *domain.Tenant
	if found.TenantID > 0 {
		t, _ = s.tenantByID(found.TenantID)
	}
	s.Audit(ac, "guest.report.resolve", t, map[string]any{"report_id": id}, nil)
	return nil
}
