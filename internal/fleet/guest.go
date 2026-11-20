package fleet

import (
	"errors"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"clawbench-fleet/internal/domain"
)

var ErrGuestDisabled = errors.New("员工自助页还没有开启")

// GuestInstance is the small slice of a tenant that may be shown to everyone.
// Keep it minimal: no home directory, no UID, no notes, no metrics.
type GuestInstance struct {
	ID          int64  `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	Port        int    `json:"port"`
	Address     string `json:"address"`
	Status      string `json:"status"`
	DiskWarn    bool   `json:"disk_warn"`
}

type GuestPage struct {
	Enabled bool            `json:"enabled"`
	Notice  string          `json:"notice"`
	Help    string          `json:"help"`
	Items   []GuestInstance `json:"items"`
}

func (s *Service) GuestPage() (*GuestPage, error) {
	st := s.Settings()
	page := &GuestPage{
		Enabled: st.GuestEnabled,
		Notice:  st.GuestNotice,
		Help:    st.GuestHelp,
		Items:   []GuestInstance{},
	}
	if !st.GuestEnabled {
		return page, nil
	}
	all, err := s.allTenants()
	if err != nil {
		return nil, err
	}
	diskLimit := int64(st.DiskWarnGB) * 1024 * 1024 * 1024
	for _, t := range all {
		// A trashed or half-deleted instance is not something an employee can
		// open, so it must not appear on the public page.
		if t.Status == domain.StatusTrashed || t.Status == domain.StatusDeleting {
			continue
		}
		page.Items = append(page.Items, GuestInstance{
			ID:          t.ID,
			Username:    t.Username,
			DisplayName: t.DisplayName,
			Port:        t.Port,
			Address:     s.Address(t),
			Status:      t.Status,
			DiskWarn:    diskLimit > 0 && t.DiskBytes >= diskLimit,
		})
	}
	sort.Slice(page.Items, func(i, j int) bool {
		return page.Items[i].Username < page.Items[j].Username
	})
	return page, nil
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
