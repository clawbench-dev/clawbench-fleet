package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"clawbench-fleet/internal/fleet"
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
	defer file.Close()
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
	writeJSON(w, http.StatusOK, map[string]any{"tenant": s.svc.TenantDTO(*t)})
}

func (s *Server) handleApplyUpgrade(w http.ResponseWriter, r *http.Request) {
	version := r.PathValue("version")
	job, err := s.svc.ApplyUpgrade(r.Context(), version, auditCtx(r))
	if err != nil {
		writeServiceErr(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"job": job})
}

func (s *Server) handleRollback(w http.ResponseWriter, r *http.Request) {
	job, err := s.svc.Rollback(r.Context(), auditCtx(r))
	if err != nil {
		writeServiceErr(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"job": job})
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
	writeJSON(w, http.StatusOK, map[string]any{"job": job})
}

func (s *Server) handleJobArtifact(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_id", err.Error())
		return
	}
	name := r.PathValue("name")
	if name != "success.csv" && name != "failures.csv" {
		writeErr(w, http.StatusNotFound, "not_found", "没有这个结果文件")
		return
	}
	data, ok := s.svc.Artifact(id, name)
	if !ok {
		writeErr(w, http.StatusNotFound, "not_found", "结果文件已过期，请重新导入")
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
	versions, warning := s.svc.ListNpmVersions(r.Context())
	if versions == nil {
		versions = []fleet.NpmVersionInfo{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"package":  s.svc.NpmPackage(),
		"versions": versions,
		"warning":  warning,
	})
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
