package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"clawbench-fleet/internal/config"
	"clawbench-fleet/internal/domain"
	"clawbench-fleet/internal/fleet"
	"clawbench-fleet/internal/store"
	"clawbench-fleet/internal/version"
)

type Server struct {
	svc     *fleet.Service
	cfg     config.Config
	mux     *http.ServeMux
	limiter *loginLimiter
	reports *slidingLimiter
}

type ctxKey string

const adminKey ctxKey = "admin"

func New(svc *fleet.Service, cfg config.Config) *Server {
	s := &Server{
		svc:     svc,
		cfg:     cfg,
		mux:     http.NewServeMux(),
		limiter: newLoginLimiter(),
		reports: newSlidingLimiter(10, time.Hour),
	}
	s.routes()
	return s
}

func (s *Server) Handler() http.Handler {
	return s.withRecover(s.withHeaders(s.mux))
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /api/health", s.handleHealth)
	s.mux.HandleFunc("POST /api/auth/login", s.handleLogin)
	s.mux.HandleFunc("POST /api/auth/logout", s.handleLogout)
	s.mux.HandleFunc("GET /api/auth/me", s.protected(s.handleMe))
	s.mux.HandleFunc("POST /api/auth/password", s.protected(s.handleChangePassword))

	s.mux.HandleFunc("GET /api/overview", s.protected(s.handleOverview))

	s.mux.HandleFunc("GET /api/public/instances", s.handleGuestPage)
	s.mux.HandleFunc("POST /api/public/reports", s.handleGuestReport)
	s.mux.HandleFunc("GET /api/reports", s.protected(s.handleListReports))
	s.mux.HandleFunc("POST /api/reports/{id}/resolve", s.protected(s.handleResolveReport))

	s.mux.HandleFunc("GET /api/tenants", s.protected(s.handleListTenants))
	s.mux.HandleFunc("GET /api/tenants/scan", s.protected(s.handleScanTenants))
	s.mux.HandleFunc("POST /api/tenants/adopt", s.protected(s.handleAdoptTenants))
	s.mux.HandleFunc("POST /api/tenants", s.protected(s.handleCreateTenant))
	s.mux.HandleFunc("POST /api/tenants/bulk", s.protected(s.handleBulk))
	s.mux.HandleFunc("POST /api/tenants/import/preview", s.protected(s.handleImportPreview))
	s.mux.HandleFunc("POST /api/tenants/import", s.protected(s.handleImport))
	s.mux.HandleFunc("GET /api/tenants/{id}", s.protected(s.handleGetTenant))
	s.mux.HandleFunc("PATCH /api/tenants/{id}", s.protected(s.handleUpdateTenant))
	s.mux.HandleFunc("DELETE /api/tenants/{id}", s.protected(s.handleDeleteTenant))
	s.mux.HandleFunc("POST /api/tenants/{id}/start", s.protected(s.handleStart))
	s.mux.HandleFunc("POST /api/tenants/{id}/stop", s.protected(s.handleStop))
	s.mux.HandleFunc("POST /api/tenants/{id}/restart", s.protected(s.handleRestart))
	s.mux.HandleFunc("POST /api/tenants/{id}/reset-password", s.protected(s.handleResetPassword))
	s.mux.HandleFunc("POST /api/tenants/{id}/repair", s.protected(s.handleRepair))
	s.mux.HandleFunc("GET /api/tenants/{id}/logs", s.protected(s.handleLogs))
	s.mux.HandleFunc("GET /api/tenants/{id}/metrics", s.protected(s.handleMetrics))
	s.mux.HandleFunc("GET /api/tenants/{id}/backups", s.protected(s.handleListBackups))
	s.mux.HandleFunc("POST /api/tenants/{id}/backups", s.protected(s.handleCreateBackup))
	s.mux.HandleFunc("POST /api/tenants/{id}/backups/{backupID}/restore", s.protected(s.handleRestoreBackup))
	s.mux.HandleFunc("DELETE /api/tenants/{id}/backups/{backupID}", s.protected(s.handleDeleteBackup))
	s.mux.HandleFunc("GET /api/backups/{id}/download", s.protected(s.handleDownloadBackup))

	s.mux.HandleFunc("GET /api/trash", s.protected(s.handleListTrash))
	s.mux.HandleFunc("POST /api/trash/{id}/restore", s.protected(s.handleRestoreTrash))
	s.mux.HandleFunc("DELETE /api/trash/{id}", s.protected(s.handlePurgeTrash))

	s.mux.HandleFunc("GET /api/upgrades", s.protected(s.handleListUpgrades))
	s.mux.HandleFunc("GET /api/upgrades/npm", s.protected(s.handleNpmVersions))
	s.mux.HandleFunc("POST /api/upgrades/npm/{version}/install", s.protected(s.handleNpmInstall))
	s.mux.HandleFunc("POST /api/upgrades/upload", s.protected(s.handleUploadVersion))
	s.mux.HandleFunc("POST /api/upgrades/{version}/pilot", s.protected(s.handlePilot))
	s.mux.HandleFunc("POST /api/upgrades/{version}/apply", s.protected(s.handleApplyUpgrade))
	s.mux.HandleFunc("POST /api/upgrades/rollback", s.protected(s.handleRollback))

	s.mux.HandleFunc("GET /api/jobs/{id}", s.protected(s.handleJob))
	s.mux.HandleFunc("GET /api/jobs/{id}/artifacts/{name}", s.protected(s.handleJobArtifact))

	s.mux.HandleFunc("GET /api/audit", s.protected(s.handleAudit))
	s.mux.HandleFunc("GET /api/settings", s.protected(s.handleGetSettings))
	s.mux.HandleFunc("PATCH /api/settings", s.protected(s.handleUpdateSettings))

	s.mux.HandleFunc("/", s.serveStatic)
}

func (s *Server) protected(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead && !sameOrigin(r) {
			writeErr(w, http.StatusForbidden, "bad_origin", "请求来源不合法")
			return
		}
		cookie, err := r.Cookie(fleet.SessionCookie)
		if err != nil || cookie.Value == "" {
			writeErr(w, http.StatusUnauthorized, "unauthorized", "请先登录")
			return
		}
		admin, err := s.svc.Authenticate(cookie.Value)
		if err != nil {
			clearSessionCookie(w)
			writeErr(w, http.StatusUnauthorized, "unauthorized", err.Error())
			return
		}
		ctx := context.WithValue(r.Context(), adminKey, admin)
		next(w, r.WithContext(ctx))
	}
}

func adminFrom(r *http.Request) *domain.Admin {
	admin, _ := r.Context().Value(adminKey).(*domain.Admin)
	return admin
}

func auditCtx(r *http.Request) fleet.AuditContext {
	ac := fleet.AuditContext{IP: clientIP(r), UserAgent: r.UserAgent()}
	if admin := adminFrom(r); admin != nil {
		ac.Actor = admin.Username
	}
	return ac
}

func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Host, r.Host)
}

func (s *Server) withHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Cache-Control", "no-store")
		}
		if r.URL.Path == "/guest" {
			w.Header().Set("X-Robots-Tag", "noindex")
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) withRecover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("panic serving %s %s: %v", r.Method, r.URL.Path, rec)
				writeErr(w, http.StatusInternalServerError, "internal", "服务端内部错误")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"app":     "clawbench-fleet",
		"version": version.Version,
		"mode":    s.svc.Sys.Mode(),
		"time":    time.Now().Format(time.RFC3339),
	})
}

// --- helpers ---

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]any{"code": code, "message": message},
	})
}

func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_json", "请求内容格式不正确："+err.Error())
		return false
	}
	return true
}

func pathID(r *http.Request) (int64, error) {
	raw := r.PathValue("id")
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("无效的编号 %q", raw)
	}
	return id, nil
}

func queryInt(r *http.Request, name string, def int) int {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return def
	}
	return n
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func setSessionCookie(w http.ResponseWriter, token string, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     fleet.SessionCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   secure,
		MaxAge:   fleet.SessionMaxAgeSeconds(),
	})
}

func clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     fleet.SessionCookie,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   -1,
	})
}

func isNotFound(err error) bool { return false }

func writeServiceErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeErr(w, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, store.ErrPortExhausted):
		writeErr(w, http.StatusConflict, "port_exhausted", err.Error())
	default:
		writeErr(w, http.StatusBadRequest, "operation_failed", err.Error())
	}
}

// --- login limiter ---

type loginLimiter struct {
	mu sync.Mutex
	m  map[string]*loginAttempt
}

type loginAttempt struct {
	failures int
	blocked  time.Time
}

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{m: map[string]*loginAttempt{}}
}

func (l *loginLimiter) blocked(ip string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	a := l.m[ip]
	if a == nil {
		return false, 0
	}
	if time.Now().Before(a.blocked) {
		return true, time.Until(a.blocked)
	}
	return false, 0
}

func (l *loginLimiter) fail(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	a := l.m[ip]
	if a == nil {
		a = &loginAttempt{}
		l.m[ip] = a
	}
	a.failures++
	if a.failures >= 5 {
		minutes := 1 << uint(min(a.failures-5, 4))
		if minutes > 15 {
			minutes = 15
		}
		a.blocked = time.Now().Add(time.Duration(minutes) * time.Minute)
	}
}

func (l *loginLimiter) reset(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.m, ip)
}

// slidingLimiter caps how often one IP may hit a public endpoint inside a
// time window. Used for the guest report button.
type slidingLimiter struct {
	mu     sync.Mutex
	m      map[string][]time.Time
	max    int
	window time.Duration
}

func newSlidingLimiter(max int, window time.Duration) *slidingLimiter {
	return &slidingLimiter{m: map[string][]time.Time{}, max: max, window: window}
}

func (l *slidingLimiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	nowT := time.Now()
	cutoff := nowT.Add(-l.window)
	kept := make([]time.Time, 0, len(l.m[key])+1)
	for _, t := range l.m[key] {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	if len(kept) >= l.max {
		l.m[key] = kept
		return false
	}
	l.m[key] = append(kept, nowT)
	// Keep the map from growing forever on a long-running host.
	if len(l.m) > 4096 {
		for k, v := range l.m {
			if len(v) == 0 || !v[len(v)-1].After(cutoff) {
				delete(l.m, k)
			}
		}
	}
	return true
}
