package fleet

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"clawbench-fleet/internal/domain"
	"clawbench-fleet/internal/store"
)

func (s *Service) ListVersions() ([]domain.Version, error) {
	return s.Store.ListVersions()
}

func (s *Service) CurrentVersion() (*domain.Version, error) {
	return s.Store.CurrentVersion()
}

func (s *Service) UploadVersion(ctx context.Context, filename string, r io.Reader, maxBytes int64, ac AuditContext) (*domain.Version, error) {
	if err := os.MkdirAll(s.Cfg.UploadDir(), 0o700); err != nil {
		return nil, err
	}
	tmp, err := os.CreateTemp(s.Cfg.UploadDir(), "upload-*")
	if err != nil {
		return nil, err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	written, err := io.Copy(tmp, io.LimitReader(r, maxBytes+1))
	if err != nil {
		_ = tmp.Close()
		return nil, err
	}
	if closeErr := tmp.Close(); closeErr != nil {
		return nil, closeErr
	}
	if written > maxBytes {
		return nil, fmt.Errorf("升级包超过 %d MB 上限", maxBytes/1024/1024)
	}
	if written == 0 {
		return nil, errors.New("升级包是空文件")
	}
	if chmodErr := os.Chmod(tmpName, 0o755); chmodErr != nil { //nolint:gosec // the uploaded binary must be executable
		return nil, chmodErr
	}
	version, err := s.validateBinary(ctx, tmpName, filename)
	if err != nil {
		return nil, err
	}
	if _, getErr := s.Store.GetVersion(version); getErr == nil {
		return nil, fmt.Errorf("版本 %s 已经存在", version)
	}
	return s.installUploaded(tmpName, version, ac)
}

// installUploaded moves the validated upload into the versions directory,
// records it and makes it current when it is the first version.
func (s *Service) installUploaded(tmpName, version string, ac AuditContext) (*domain.Version, error) {
	destDir := filepath.Join(s.Cfg.VersionsDir(), version)
	if mkdirErr := os.MkdirAll(destDir, 0o755); mkdirErr != nil {
		return nil, mkdirErr
	}
	dest := filepath.Join(destDir, "clawbench")
	if renameErr := os.Rename(tmpName, dest); renameErr != nil {
		// Rename across filesystems falls back to a copy.
		if copyErr := copyFile(tmpName, dest, 0o755); copyErr != nil {
			return nil, copyErr
		}
	}
	sum, err := hashFile(dest)
	if err != nil {
		return nil, err
	}
	info, _ := os.Stat(dest)
	v := &domain.Version{
		Version: version, Path: dest, SHA256: sum, SizeBytes: info.Size(),
		UploadedAt: time.Now().Format(time.RFC3339), Status: "installed",
	}
	if err := s.Store.CreateVersion(v); err != nil {
		return nil, err
	}
	// The first upload becomes current automatically, otherwise nothing can
	// start.
	if _, err := s.Store.CurrentVersion(); errors.Is(err, store.ErrNotFound) {
		if err := s.SetCurrentVersion(version); err != nil {
			return nil, err
		}
		v.Status = "current"
	}
	s.Audit(ac, "upgrade.upload", nil, map[string]any{fieldVersion: version, "sha256": sum}, nil)
	return v, nil
}

func (s *Service) validateBinary(ctx context.Context, path, filename string) (string, error) {
	runCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(runCtx, path, "--version")
	out, err := cmd.Output()
	if err == nil {
		line := strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
		if versionRe.MatchString(line) {
			return line, nil
		}
		if line != "" {
			return "", fmt.Errorf("程序输出了无法识别的版本号：%q", line)
		}
	}
	// In dev mode we accept any executable and use the file name as the
	// version, so the whole upgrade flow can be exercised without a real
	// Clawbench binary.
	if s.Sys.Mode() == "dev" {
		base := filepath.Base(filename)
		if versionRe.MatchString(base) {
			return base, nil
		}
		base = strings.TrimSuffix(base, filepath.Ext(base))
		if base == "" || !versionRe.MatchString(base) {
			base = "dev-" + time.Now().Format("20060102-150405")
		}
		return base, nil
	}
	if err != nil {
		return "", fmt.Errorf("这个文件不能作为 Clawbench 运行：%w", err)
	}
	return "", errors.New("无法从程序里读出 --version")
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (s *Service) SetCurrentVersion(version string) error {
	if _, err := s.Store.GetVersion(version); err != nil {
		return fmt.Errorf("版本 %s 不存在", version)
	}
	if err := s.Store.SetCurrentVersion(version); err != nil {
		return err
	}
	return s.setCurrentSymlink(version)
}

func (s *Service) setCurrentSymlink(version string) error {
	link := s.Cfg.CurrentLink()
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		return err
	}
	tmp := link + ".tmp"
	_ = os.Remove(tmp)
	if err := os.Symlink(filepath.Join("versions", version), tmp); err != nil {
		return err
	}
	if err := os.Rename(tmp, link); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// Pilot restarts one tenant on a new version and rolls the symlink back if it
// does not come up. Nothing else is touched.
func (s *Service) Pilot(ctx context.Context, version string, tenantID int64, ac AuditContext) (*domain.Tenant, error) {
	t, getErr := s.tenantByID(tenantID)
	if getErr != nil {
		return nil, getErr
	}
	if t.Status != domain.StatusRunning {
		return nil, errors.New("试点租户必须处于运行中状态")
	}
	if err := s.beginUpgrade(); err != nil {
		return nil, err
	}
	defer s.endUpgrade()
	unlock := s.lockTenant(t.Username)
	defer unlock()
	if err := s.acquire(ctx); err != nil {
		return nil, err
	}
	defer s.release()

	old, _ := s.Store.CurrentVersion()
	oldVersion := ""
	if old != nil {
		oldVersion = old.Version
	}
	if oldVersion != version {
		if err := s.SetCurrentVersion(version); err != nil {
			return nil, err
		}
	}
	if err := s.Sys.Restart(ctx, t.UnitName); err != nil {
		s.rollbackPilot(ctx, t, oldVersion, ac)
		return nil, fmt.Errorf("试点重启失败：%w", err)
	}
	info, err := s.waitHealthy(ctx, t.Port, version, 60*time.Second)
	if err != nil {
		s.rollbackPilot(ctx, t, oldVersion, ac)
		return nil, fmt.Errorf("试点版本没有就绪：%w", err)
	}
	t.Status = domain.StatusRunning
	t.Version = info.Version
	t.LastHealthOK = true
	t.LastHealthAt = time.Now().Format(time.RFC3339)
	t.LastError = ""
	_ = s.Store.UpdateTenant(t)
	s.Audit(ac, "upgrade.pilot", t, map[string]any{fieldVersion: version, "previous": oldVersion}, nil)
	return t, nil
}

func (s *Service) rollbackPilot(ctx context.Context, t *domain.Tenant, oldVersion string, ac AuditContext) {
	if oldVersion == "" {
		return
	}
	_ = s.SetCurrentVersion(oldVersion)
	_ = s.Sys.Restart(ctx, t.UnitName)
	_, _ = s.waitHealthy(ctx, t.Port, oldVersion, 45*time.Second)
	s.Audit(ac, "upgrade.pilot_rollback", t, map[string]any{fieldVersion: oldVersion}, nil)
}

func (s *Service) ApplyUpgrade(ctx context.Context, version string, ac AuditContext) (*domain.Job, error) {
	if err := s.beginUpgrade(); err != nil {
		return nil, err
	}
	release := true
	defer func() {
		if release {
			s.endUpgrade()
		}
	}()
	if _, err := s.Store.GetVersion(version); err != nil {
		return nil, fmt.Errorf("版本 %s 不存在", version)
	}
	current, _ := s.Store.CurrentVersion()
	oldVersion := ""
	if current != nil {
		if current.Version == version {
			// Pilot already switched the symlink. Keep the previous version as
			// the rollback target instead of overwriting it with itself.
			if prev, err := s.Store.PreviousVersion(); err == nil {
				oldVersion = prev.Version
			}
		} else {
			oldVersion = current.Version
		}
	}
	tenants, err := s.Store.ListTenantsByStatus(domain.StatusRunning)
	if err != nil {
		return nil, err
	}
	if len(tenants) == 0 {
		return nil, errors.New("没有正在运行的租户可以升级")
	}
	payload, _ := json.Marshal(map[string]any{fieldVersion: version, "previous": oldVersion})
	job, err := s.Store.CreateJob("upgrade", string(payload), len(tenants))
	if err != nil {
		return nil, err
	}
	itemIDs := make([]int64, len(tenants))
	for i, t := range tenants {
		itemIDs[i], _ = s.Store.AddJobItem(job.ID, t.ID, t.Username)
	}
	if current == nil || current.Version != version {
		if err := s.SetCurrentVersion(version); err != nil {
			return nil, err
		}
	}
	go func() {
		defer s.endUpgrade()
		s.runUpgrade(job.ID, version, oldVersion, tenants, itemIDs, ac, "upgrade.apply")
	}()
	release = false
	return job, nil
}

func (s *Service) runUpgrade(jobID int64, version, oldVersion string, tenants []domain.Tenant, itemIDs []int64, ac AuditContext, auditAction string) {
	_ = s.Store.MarkJobRunning(jobID)
	results := make([]error, len(tenants))
	var mu sync.Mutex
	done, failed := 0, 0

	work := make(chan int)
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range work {
				t := tenants[i]
				_ = s.Store.UpdateJobItem(itemIDs[i], domain.JobRunning, "")
				err := s.restartForVersion(context.Background(), t, version)
				mu.Lock()
				results[i] = err
				done++
				if err != nil {
					failed++
					_ = s.Store.UpdateJobItem(itemIDs[i], domain.JobFailed, err.Error())
				} else {
					_ = s.Store.UpdateJobItem(itemIDs[i], domain.JobSucceeded, "")
				}
				_ = s.Store.UpdateJobProgress(jobID, done, failed)
				mu.Unlock()
			}
		}()
	}
	for i := range tenants {
		work <- i
	}
	close(work)
	wg.Wait()

	if failed > 0 {
		if oldVersion != "" {
			_ = s.SetCurrentVersion(oldVersion)
		}
		for i, t := range tenants {
			if results[i] == nil {
				_ = s.restartForVersion(context.Background(), t, oldVersion)
			}
		}
		result, _ := json.Marshal(map[string]any{"failed": failed, "rolled_back_to": oldVersion})
		_ = s.Store.FinishJob(jobID, domain.JobFailed, string(result))
		s.Audit(ac, auditAction, nil, map[string]any{"version": version, "failed": failed, "rolled_back_to": oldVersion}, errors.New("批量升级失败，已回滚"))
		return
	}
	result, _ := json.Marshal(map[string]any{"upgraded": len(tenants), "version": version})
	_ = s.Store.FinishJob(jobID, domain.JobSucceeded, string(result))
	s.Audit(ac, auditAction, nil, map[string]any{"version": version, "upgraded": len(tenants)}, nil)
}

func (s *Service) restartForVersion(ctx context.Context, t domain.Tenant, version string) error {
	unlock := s.lockTenant(t.Username)
	defer unlock()
	if err := s.acquire(ctx); err != nil {
		return err
	}
	defer s.release()
	if err := s.Sys.Restart(ctx, t.UnitName); err != nil {
		_ = s.failTenant(&t, "upgrade.restart", "升级重启失败", err, AuditContext{Actor: "system"})
		return err
	}
	info, err := s.waitHealthy(ctx, t.Port, version, 60*time.Second)
	if err != nil {
		_ = s.failTenant(&t, "upgrade.restart", "升级后健康检查失败", err, AuditContext{Actor: "system"})
		return err
	}
	t.Status = domain.StatusRunning
	t.Version = info.Version
	t.LastHealthOK = true
	t.LastHealthAt = time.Now().Format(time.RFC3339)
	t.LastError = ""
	_ = s.Store.UpdateTenant(&t)
	return nil
}

func (s *Service) Rollback(ctx context.Context, ac AuditContext) (*domain.Job, error) {
	if err := s.beginUpgrade(); err != nil {
		return nil, err
	}
	release := true
	defer func() {
		if release {
			s.endUpgrade()
		}
	}()
	previous, err := s.Store.PreviousVersion()
	if err != nil {
		return nil, errors.New("没有可回滚的上一版")
	}
	current, _ := s.Store.CurrentVersion()
	currentVersion := ""
	if current != nil {
		currentVersion = current.Version
	}
	tenants, err := s.Store.ListTenantsByStatus(domain.StatusRunning)
	if err != nil {
		return nil, err
	}
	if len(tenants) == 0 {
		return nil, errors.New("没有正在运行的租户可以回滚")
	}
	payload, _ := json.Marshal(map[string]any{"from": currentVersion, "to": previous.Version})
	job, err := s.Store.CreateJob("rollback", string(payload), len(tenants))
	if err != nil {
		return nil, err
	}
	itemIDs := make([]int64, len(tenants))
	for i, t := range tenants {
		itemIDs[i], _ = s.Store.AddJobItem(job.ID, t.ID, t.Username)
	}
	if err := s.SetCurrentVersion(previous.Version); err != nil {
		return nil, err
	}
	go func() {
		defer s.endUpgrade()
		s.runUpgrade(job.ID, previous.Version, currentVersion, tenants, itemIDs, ac, "upgrade.rollback")
	}()
	release = false
	return job, nil
}

func (s *Service) Job(id int64) (*domain.Job, error) {
	return s.Store.GetJob(id)
}
