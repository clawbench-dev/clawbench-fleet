package httpapi

import (
	"fmt"
	"net/http"
	"strconv"

	"clawbench-fleet/internal/domain"
	"clawbench-fleet/internal/fleet"
	"clawbench-fleet/internal/store"
)

func (s *Server) handleListTenants(w http.ResponseWriter, r *http.Request) {
	page := queryInt(r, "page", 1)
	size := queryInt(r, "page_size", 50)
	f := store.TenantFilter{
		Query:  r.URL.Query().Get("q"),
		Status: r.URL.Query().Get("status"),
		Page:   page, PageSize: size,
	}
	items, total, err := s.svc.ListTenants(f)
	if err != nil {
		writeServiceErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": items, "total": total, "page": page, "page_size": size,
	})
}

func (s *Server) handleCreateTenant(w http.ResponseWriter, r *http.Request) {
	var in fleet.CreateTenantInput
	if !decodeJSON(w, r, &in) {
		return
	}
	t, password, err := s.svc.CreateTenant(r.Context(), in, auditCtx(r))
	if t == nil {
		if err != nil {
			writeServiceErr(w, err)
		}
		return
	}
	resp := map[string]any{"tenant": s.svc.TenantDTO(*t), "password": password}
	if err != nil {
		resp["warning"] = err.Error()
	}
	writeJSON(w, http.StatusCreated, resp)
}

func (s *Server) handleGetTenant(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_id", err.Error())
		return
	}
	dto, err := s.svc.TenantDetail(id)
	if err != nil {
		writeServiceErr(w, err)
		return
	}
	backups, _ := s.svc.ListBackups(id)
	writeJSON(w, http.StatusOK, map[string]any{"tenant": dto, "backups": backups})
}

func (s *Server) handleUpdateTenant(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_id", err.Error())
		return
	}
	var in fleet.UpdateTenantInput
	if !decodeJSON(w, r, &in) {
		return
	}
	t, err := s.svc.UpdateTenant(r.Context(), id, in, auditCtx(r))
	if err != nil {
		writeServiceErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tenant": s.svc.TenantDTO(*t)})
}

func (s *Server) handleDeleteTenant(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_id", err.Error())
		return
	}
	var body struct {
		Confirm string `json:"confirm"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	t, err := s.svc.DeleteTenant(r.Context(), id, body.Confirm, auditCtx(r))
	if err != nil {
		writeServiceErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tenant": s.svc.TenantDTO(*t)})
}

func (s *Server) handleStart(w http.ResponseWriter, r *http.Request) {
	s.tenantAction(w, r, func(id int64) (*domain.Tenant, error) {
		return s.svc.StartTenant(r.Context(), id, auditCtx(r))
	})
}

func (s *Server) handleStop(w http.ResponseWriter, r *http.Request) {
	s.tenantAction(w, r, func(id int64) (*domain.Tenant, error) {
		return s.svc.StopTenant(r.Context(), id, auditCtx(r))
	})
}

func (s *Server) handleRestart(w http.ResponseWriter, r *http.Request) {
	s.tenantAction(w, r, func(id int64) (*domain.Tenant, error) {
		return s.svc.RestartTenant(r.Context(), id, auditCtx(r))
	})
}

func (s *Server) tenantAction(w http.ResponseWriter, r *http.Request, fn func(int64) (*domain.Tenant, error)) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_id", err.Error())
		return
	}
	t, err := fn(id)
	if err != nil {
		writeServiceErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tenant": s.svc.TenantDTO(*t)})
}

func (s *Server) handleResetPassword(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_id", err.Error())
		return
	}
	t, password, err := s.svc.ResetPassword(r.Context(), id, auditCtx(r))
	if err != nil {
		if t == nil {
			writeServiceErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"tenant": s.svc.TenantDTO(*t), "password": password, "warning": err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tenant": s.svc.TenantDTO(*t), "password": password})
}

func (s *Server) handleRepair(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_id", err.Error())
		return
	}
	t, password, err := s.svc.RepairTenant(r.Context(), id, auditCtx(r))
	if err != nil {
		if t == nil {
			writeServiceErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"tenant": s.svc.TenantDTO(*t), "password": password, "warning": err.Error(),
		})
		return
	}
	resp := map[string]any{"tenant": s.svc.TenantDTO(*t)}
	if password != "" {
		resp["password"] = password
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_id", err.Error())
		return
	}
	lines := queryInt(r, "lines", 500)
	if lines < 1 {
		lines = 1
	}
	if lines > 5000 {
		lines = 5000
	}
	out, err := s.svc.Logs(r.Context(), id, lines, auditCtx(r))
	if err != nil {
		writeServiceErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"lines": out})
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_id", err.Error())
		return
	}
	t, err := s.svc.GetTenant(id)
	if err != nil {
		writeServiceErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"metrics": s.svc.GetMetrics(id),
		"tenant":  s.svc.TenantDTO(*t),
	})
}

func (s *Server) handleBulk(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Action string  `json:"action"`
		IDs    []int64 `json:"ids"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	job, err := s.svc.BulkAction(r.Context(), body.Action, body.IDs, auditCtx(r))
	if err != nil {
		writeServiceErr(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"job": job})
}

func (s *Server) handleListBackups(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_id", err.Error())
		return
	}
	items, err := s.svc.ListBackups(id)
	if err != nil {
		writeServiceErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleCreateBackup(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_id", err.Error())
		return
	}
	b, err := s.svc.BackupTenant(r.Context(), id, auditCtx(r))
	if err != nil {
		writeServiceErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"backup": b})
}

func (s *Server) handleRestoreBackup(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_id", err.Error())
		return
	}
	backupID, err := strconv.ParseInt(r.PathValue("backupID"), 10, 64)
	if err != nil || backupID <= 0 {
		writeErr(w, http.StatusBadRequest, "bad_backup_id", "备份编号不合法")
		return
	}
	t, err := s.svc.RestoreBackup(r.Context(), id, backupID, auditCtx(r))
	if err != nil {
		writeServiceErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tenant": s.svc.TenantDTO(*t)})
}

func (s *Server) handleDeleteBackup(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_id", err.Error())
		return
	}
	backupID, err := strconv.ParseInt(r.PathValue("backupID"), 10, 64)
	if err != nil || backupID <= 0 {
		writeErr(w, http.StatusBadRequest, "bad_backup_id", "备份编号不合法")
		return
	}
	if err := s.svc.DeleteBackup(r.Context(), id, backupID, auditCtx(r)); err != nil {
		writeServiceErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleDownloadBackup(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_id", err.Error())
		return
	}
	b, err := s.svc.GetBackup(id)
	if err != nil {
		writeServiceErr(w, err)
		return
	}
	t, _ := s.svc.GetTenant(b.TenantID)
	if t != nil {
		s.svc.Audit(auditCtx(r), "backup.download", t, map[string]any{"backup_id": b.ID}, nil)
	}
	filename := fmt.Sprintf("%s-%s.tar.gz", b.Username, b.CreatedAt)
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", "attachment; filename="+strconv.Quote(filename))
	if err := s.svc.WriteBackupTar(w, id); err != nil {
		// Headers are already sent; the client sees a truncated download.
		return
	}
}

func (s *Server) handleListTrash(w http.ResponseWriter, r *http.Request) {
	items, err := s.svc.ListTrash()
	if err != nil {
		writeServiceErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleRestoreTrash(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_id", err.Error())
		return
	}
	t, password, err := s.svc.RestoreTenant(r.Context(), id, auditCtx(r))
	if err != nil {
		if t == nil {
			writeServiceErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"tenant": s.svc.TenantDTO(*t), "password": password, "warning": err.Error(),
		})
		return
	}
	resp := map[string]any{"tenant": s.svc.TenantDTO(*t)}
	if password != "" {
		resp["password"] = password
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handlePurgeTrash(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_id", err.Error())
		return
	}
	if err := s.svc.PurgeTenant(r.Context(), id, auditCtx(r)); err != nil {
		writeServiceErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
