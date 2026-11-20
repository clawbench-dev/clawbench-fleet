package httpapi

import (
	"net/http"
	"strconv"
	"time"

	"clawbench-fleet/internal/fleet"
)

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	ip := clientIP(r)
	if blocked, wait := s.limiter.blocked(ip); blocked {
		w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
		writeErr(w, http.StatusTooManyRequests, "too_many_attempts", "尝试次数太多，请稍后再试")
		return
	}
	admin, token, err := s.svc.Login(body.Username, body.Password, ip, r.UserAgent())
	if err != nil {
		s.limiter.fail(ip)
		s.svc.Audit(fleet.AuditContext{Actor: body.Username, IP: ip, UserAgent: r.UserAgent()},
			"auth.login", nil, nil, err)
		writeErr(w, http.StatusUnauthorized, "bad_credentials", err.Error())
		return
	}
	s.limiter.reset(ip)
	setSessionCookie(w, token, r.TLS != nil)
	s.svc.Audit(fleet.AuditContext{Actor: admin.Username, IP: ip, UserAgent: r.UserAgent()},
		"auth.login", nil, nil, nil)
	writeJSON(w, http.StatusOK, map[string]any{
		"admin":      admin,
		"expires_at": time.Now().Add(7 * 24 * time.Hour).Format(time.RFC3339),
	})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(fleet.SessionCookie); err == nil {
		s.svc.Logout(cookie.Value)
	}
	clearSessionCookie(w)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"admin": adminFrom(r)})
}

func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Current string `json:"current_password"`
		Next    string `json:"new_password"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	admin := adminFrom(r)
	if err := s.svc.ChangeAdminPassword(admin.ID, body.Current, body.Next); err != nil {
		writeErr(w, http.StatusBadRequest, "password_change_failed", err.Error())
		return
	}
	s.svc.Audit(auditCtx(r), "auth.password", nil, nil, nil)
	clearSessionCookie(w)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "relogin": true})
}
