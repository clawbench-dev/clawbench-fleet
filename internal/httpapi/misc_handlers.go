package httpapi

import (
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"

	"clawbench-fleet/internal/store"
	"clawbench-fleet/internal/version"
	"clawbench-fleet/internal/web"
)

func (s *Server) handleOverview(w http.ResponseWriter, r *http.Request) {
	ov, err := s.svc.Overview()
	if err != nil {
		writeServiceErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ov)
}

func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request) {
	f := store.AuditFilter{
		Actor:    r.URL.Query().Get("actor"),
		Action:   r.URL.Query().Get("action"),
		Tenant:   r.URL.Query().Get("tenant"),
		From:     r.URL.Query().Get("from"),
		To:       r.URL.Query().Get("to"),
		Page:     queryInt(r, "page", 1),
		PageSize: queryInt(r, "page_size", 50),
	}
	items, total, err := s.svc.ListAudit(f)
	if err != nil {
		writeServiceErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		fieldItems: items, "total": total, "page": f.Page, "page_size": f.PageSize,
	})
}

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	current, _ := s.svc.CurrentVersion()
	currentVersion := ""
	if current != nil {
		currentVersion = current.Version
	}
	sshStatus, _ := s.svc.SSHStatus(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{
		"settings":        s.svc.Settings(),
		"mode":            s.svc.Sys.Mode(),
		"public_host":     s.svc.PublicHost(),
		"install_dir":     s.cfg.Clawbench.InstallDir,
		"current_version": currentVersion,
		"fleet_version":   version.Version,
		"ssh_status":      sshStatus,
	})
}

func (s *Server) handleUpdateSettings(w http.ResponseWriter, r *http.Request) {
	next := s.svc.Settings()
	if !decodeJSON(w, r, &next) {
		return
	}
	if err := s.svc.UpdateSettings(next); err != nil {
		writeServiceErr(w, err)
		return
	}
	s.svc.Audit(auditCtx(r), "settings.update", nil, map[string]any{
		"port_start": next.PortStart, "port_end": next.PortEnd,
		"backup_retention_days": next.BackupRetention, "trash_retention_days": next.TrashRetention,
	}, nil)
	writeJSON(w, http.StatusOK, map[string]any{"settings": s.svc.Settings()})
}

func (s *Server) handleApplyDefaultQuota(w http.ResponseWriter, r *http.Request) {
	job, err := s.svc.ApplyDefaultQuotaAsync(r.Context(), auditCtx(r))
	if err != nil {
		writeServiceErr(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{fieldJob: job})
}

func (s *Server) serveStatic(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		writeErr(w, http.StatusNotFound, "not_found", "接口不存在")
		return
	}
	fsys := web.FS()
	name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
	if name == "" || name == "." {
		name = "index.html"
	}
	if data, err := fs.ReadFile(fsys, name); err == nil {
		if strings.HasPrefix(name, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		if ct := mime.TypeByExtension(path.Ext(name)); ct != "" {
			w.Header().Set("Content-Type", ct)
		}
		_, _ = w.Write(data) //nolint:gosec // data comes from the embedded frontend bundle, not user input
		return
	}
	if path.Ext(name) != "" {
		http.NotFound(w, r)
		return
	}
	data, err := fs.ReadFile(fsys, "index.html")
	if err != nil {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<!doctype html><title>Clawbench Fleet</title><p>前端还没有构建。运行 ./build.sh 后再试。</p>"))
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(data)
}
