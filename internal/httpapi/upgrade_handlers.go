package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"clawbench-fleet/internal/store"
)

func (s *Server) handleListUpgrades(w http.ResponseWriter, r *http.Request) {
	versions, err := s.svc.ListVersions()
	if err != nil {
		writeServiceErr(w, err)
		return
	}
	current, _ := s.svc.CurrentVersion()
	jobs, _ := s.svc.ListRecentJobs(10)
	writeJSON(w, http.StatusOK, map[string]any{
		"versions": versions,
		"current":  current,
		"jobs":     jobs,
	})
}

func (s *Server) handleUploadVersion(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 600<<20)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_upload", "读取上传文件失败："+err.Error())
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_upload", "请选择 Clawbench 二进制文件")
		return
	}
	defer func() { _ = file.Close() }()
	v, err := s.svc.UploadVersion(r.Context(), header.Filename, file, 600<<20, auditCtx(r))
	if err != nil {
		writeServiceErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"version": v})
}

func (s *Server) handlePilot(w http.ResponseWriter, r *http.Request) {
	version := r.PathValue("version")
	var body struct {
		TenantID int64 `json:"tenant_id"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.TenantID <= 0 {
		writeErr(w, http.StatusBadRequest, "bad_tenant", "请选择试点租户")
		return
	}
	t, err := s.svc.Pilot(r.Context(), version, body.TenantID, auditCtx(r))
	if err != nil {
		writeServiceErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{fieldTenant: s.svc.TenantDTO(*t)})
}

func (s *Server) handleApplyUpgrade(w http.ResponseWriter, r *http.Request) {
	version := r.PathValue("version")
	job, err := s.svc.ApplyUpgrade(r.Context(), version, auditCtx(r))
	if err != nil {
		writeServiceErr(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{fieldJob: job})
}

func (s *Server) handleRollback(w http.ResponseWriter, r *http.Request) {
	job, err := s.svc.Rollback(r.Context(), auditCtx(r))
	if err != nil {
		writeServiceErr(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{fieldJob: job})
}

func (s *Server) handleSelfInfo(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.svc.SelfUpgradeInfo())
}

// handleSelfAdopt writes the platform's own systemd unit, enables it and
// restarts through it. Like the self upgrade, the reply is written before the
// process goes away: the page then waits for /api/health instead of treating
// the dropped connection as a failure.
func (s *Server) handleSelfAdopt(w http.ResponseWriter, r *http.Request) {
	res, err := s.svc.AdoptSelf(r.Context(), auditCtx(r))
	if err != nil && (res == nil || !res.Restarting) {
		writeServiceErr(w, err)
		return
	}
	body := map[string]any{"result": res, "restarting": res.Restarting}
	if err != nil {
		body["warning"] = err.Error()
	}
	writeJSON(w, http.StatusAccepted, body)
}

// handleSelfUpgrade replaces the platform binary and restarts the service.
// The reply is written before the restart so the browser knows the dropped
// connection is expected; the page then waits for /api/health to answer again.
func (s *Server) handleSelfUpgrade(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 600<<20)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_upload", "读取上传文件失败："+err.Error())
		return
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_upload", "请选择平台程序的二进制文件")
		return
	}
	defer func() { _ = file.Close() }()
	res, err := s.svc.UploadSelf(r.Context(), file, 600<<20, auditCtx(r))
	if err != nil && (res == nil || !res.Restarting) {
		writeServiceErr(w, err)
		return
	}
	// res.Restarting means the swap succeeded; a non-nil error then only says
	// the automatic restart did not go through, and the operator still needs
	// the paths and the version to finish by hand.
	body := map[string]any{"result": res, "restarting": res.Restarting}
	if err != nil {
		body["warning"] = err.Error()
	}
	writeJSON(w, http.StatusAccepted, body)
}

func (s *Server) handleJob(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_id", err.Error())
		return
	}
	job, err := s.svc.Job(id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "not_found", "任务不存在")
			return
		}
		writeServiceErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{fieldJob: job})
}

func (s *Server) handleJobArtifact(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_id", err.Error())
		return
	}
	name := r.PathValue("name")
	// Import jobs offer success/failure lists; a batch password reset offers
	// the generated passwords. Anything else is not a file this platform
	// produces.
	if name != "success.csv" && name != "failures.csv" && name != "passwords.csv" {
		writeErr(w, http.StatusNotFound, "not_found", "没有这个结果文件")
		return
	}
	data, ok := s.svc.Artifact(id, name)
	if !ok {
		writeErr(w, http.StatusNotFound, "not_found", "结果文件已过期，请重新执行")
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", "import-"+strconv.FormatInt(id, 10)+"-"+name))
	_, _ = w.Write(data)
}

// handleNpmVersions lists what npm has published. npm being missing or the
// network being down is reported as a warning instead of an error so the page
// still renders and the offline upload path stays usable.
func (s *Server) handleNpmVersions(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.svc.NpmCatalog(r.Context()))
}

func (s *Server) handleNpmInstall(w http.ResponseWriter, r *http.Request) {
	version := r.PathValue("version")
	v, err := s.svc.InstallFromNpm(r.Context(), version, auditCtx(r))
	if err != nil {
		writeServiceErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"version": v})
}
