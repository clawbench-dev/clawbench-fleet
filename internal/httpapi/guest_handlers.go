package httpapi

import (
	"errors"
	"net/http"

	"clawbench-fleet/internal/fleet"
)

// handleGuestPage is the only tenant-shaped endpoint reachable without a
// login. It returns the deliberately small GuestPage payload.
func (s *Server) handleGuestPage(w http.ResponseWriter, r *http.Request) {
	page, err := s.svc.GuestPageFor(r.Host)
	if err != nil {
		writeServiceErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (s *Server) handleGuestReport(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		writeErr(w, http.StatusForbidden, "bad_origin", "请求来源不合法")
		return
	}
	ip := clientIP(r)
	if !s.reports.allow(ip) {
		writeErr(w, http.StatusTooManyRequests, "too_many_reports", "提交太频繁了，请过一会儿再试")
		return
	}
	var body struct {
		TenantID int64  `json:"tenant_id"`
		Message  string `json:"message"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if err := s.svc.SubmitGuestReport(body.TenantID, body.Message, ip); err != nil {
		if errors.Is(err, fleet.ErrGuestDisabled) {
			writeErr(w, http.StatusForbidden, "guest_disabled", err.Error())
			return
		}
		writeServiceErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true})
}

func (s *Server) handleListReports(w http.ResponseWriter, r *http.Request) {
	openOnly := r.URL.Query().Get("open") != "0"
	limit := queryInt(r, "limit", 100)
	if limit < 1 || limit > 500 {
		limit = 100
	}
	items, err := s.svc.ListGuestReports(openOnly, limit)
	if err != nil {
		writeServiceErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{fieldItems: items})
}

func (s *Server) handleResolveReport(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_id", err.Error())
		return
	}
	if err := s.svc.ResolveGuestReport(id, auditCtx(r)); err != nil {
		writeServiceErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
