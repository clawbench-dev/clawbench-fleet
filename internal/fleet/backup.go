package fleet

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"clawbench-fleet/internal/domain"
)

func (s *Service) BackupTenant(ctx context.Context, id int64, ac AuditContext) (*domain.Backup, error) {
	t, err := s.tenantByID(id)
	if err != nil {
		return nil, err
	}
	if t.Status == domain.StatusTrashed || t.Status == domain.StatusDeleting {
		return nil, errors.New("已删除的租户不能备份")
	}
	unlock := s.lockTenant(t.Username)
	defer unlock()
	if err := s.acquire(ctx); err != nil {
		return nil, err
	}
	defer s.release()

	dir := filepath.Join(s.Cfg.BackupDir(), t.Username, time.Now().Format("20060102-150405.000"))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	b := &domain.Backup{
		TenantID: t.ID, Username: t.Username, CreatedAt: time.Now().Format(time.RFC3339),
		Path: dir, Status: "ok",
	}
	recordFailure := func(cause error) (*domain.Backup, error) {
		b.Status = "failed"
		b.Error = cause.Error()
		_ = s.Store.CreateBackup(b)
		_ = os.RemoveAll(dir)
		s.Audit(ac, "backup.create", t, nil, cause)
		return nil, cause
	}

	dbSrc := filepath.Join(t.DataDir, "ClawBench.db")
	hasDB := false
	if _, err := os.Stat(dbSrc); err == nil {
		hasDB = true
		snapshot := filepath.Join(dir, "ClawBench.db")
		if err := s.snapshotDB(ctx, t, dbSrc, snapshot); err != nil {
			return recordFailure(fmt.Errorf("数据库快照失败：%w", err))
		}
		_ = os.Chmod(snapshot, 0o600)
		if err := integrityCheck(snapshot); err != nil {
			return recordFailure(fmt.Errorf("备份完整性检查失败：%w", err))
		}
		b.IntegrityOK = true
	}
	configDir := filepath.Join(t.DataDir, "config")
	if _, err := os.Stat(configDir); err == nil {
		if err := copyTree(configDir, filepath.Join(dir, "config")); err != nil {
			return recordFailure(fmt.Errorf("复制配置失败：%w", err))
		}
	}
	if !hasDB && !dirHasConfig(dir) {
		return recordFailure(errors.New("这个租户还没有可备份的数据"))
	}
	size, _ := dirSize(dir)
	b.SizeBytes = size
	if err := s.Store.CreateBackup(b); err != nil {
		return nil, err
	}
	s.pruneBackups(t)
	s.Audit(ac, "backup.create", t, map[string]any{"backup_id": b.ID, "size_bytes": size}, nil)
	return b, nil
}

func dirHasConfig(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, "config"))
	return err == nil
}

func (s *Service) snapshotDB(ctx context.Context, t *domain.Tenant, src, dst string) error {
	active, _ := s.Sys.IsActive(ctx, t.UnitName)
	if !active {
		return copyFile(src, dst, 0o600)
	}
	db, err := sql.Open("sqlite", src)
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, "PRAGMA busy_timeout=5000"); err != nil {
		return err
	}
	escaped := strings.ReplaceAll(dst, "'", "''")
	_, err = db.ExecContext(ctx, "VACUUM INTO '"+escaped+"'")
	return err
}

func integrityCheck(path string) error {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return err
	}
	defer db.Close()
	var result string
	if err := db.QueryRow("PRAGMA integrity_check").Scan(&result); err != nil {
		return err
	}
	if result != "ok" {
		return fmt.Errorf("integrity_check returned %q", result)
	}
	return nil
}

func (s *Service) pruneBackups(t *domain.Tenant) {
	retention := s.Settings().BackupRetention
	cutoff := time.Now().AddDate(0, 0, -retention).Format(time.RFC3339)
	old, err := s.Store.BackupsOlderThan(t.ID, cutoff)
	if err != nil {
		return
	}
	for _, b := range old {
		if isUnder(b.Path, s.Cfg.BackupDir()) {
			_ = os.RemoveAll(b.Path)
		}
		_ = s.Store.DeleteBackupRecord(b.ID)
	}
}

func (s *Service) BackupAll(ctx context.Context) {
	tenants, err := s.allTenants()
	if err != nil {
		return
	}
	ac := AuditContext{Actor: "system"}
	for _, t := range tenants {
		if t.Status == domain.StatusTrashed || t.Status == domain.StatusDeleting {
			continue
		}
		_, _ = s.BackupTenant(ctx, t.ID, ac)
	}
}

func (s *Service) ListBackups(id int64) ([]domain.Backup, error) {
	if _, err := s.tenantByID(id); err != nil {
		return nil, err
	}
	return s.Store.ListBackups(id)
}

func (s *Service) DeleteBackup(ctx context.Context, tenantID, backupID int64, ac AuditContext) error {
	t, err := s.tenantByID(tenantID)
	if err != nil {
		return err
	}
	b, err := s.Store.GetBackup(backupID)
	if err != nil || b.TenantID != tenantID {
		return errors.New("备份不存在")
	}
	if isUnder(b.Path, s.Cfg.BackupDir()) {
		if err := os.RemoveAll(b.Path); err != nil {
			return err
		}
	}
	if err := s.Store.DeleteBackupRecord(b.ID); err != nil {
		return err
	}
	s.Audit(ac, "backup.delete", t, map[string]any{"backup_id": b.ID}, nil)
	return nil
}

func (s *Service) RestoreBackup(ctx context.Context, tenantID, backupID int64, ac AuditContext) (*domain.Tenant, error) {
	t, err := s.tenantByID(tenantID)
	if err != nil {
		return nil, err
	}
	b, err := s.Store.GetBackup(backupID)
	if err != nil || b.TenantID != tenantID {
		return nil, errors.New("备份不存在")
	}
	src := filepath.Join(b.Path, "ClawBench.db")
	if _, err := os.Stat(src); err != nil {
		return nil, errors.New("这个备份里没有数据库文件")
	}
	if err := integrityCheck(src); err != nil {
		return nil, fmt.Errorf("备份数据库损坏：%w", err)
	}
	unlock := s.lockTenant(t.Username)
	defer unlock()
	if err := s.acquire(ctx); err != nil {
		return nil, err
	}
	defer s.release()

	if err := s.Sys.Stop(ctx, t.UnitName); err != nil {
		return nil, fmt.Errorf("停止实例失败：%w", err)
	}
	_ = s.Sys.Disable(ctx, t.UnitName)
	dbDst := filepath.Join(t.DataDir, "ClawBench.db")
	if _, err := os.Stat(dbDst); err == nil {
		backupName := dbDst + ".before-restore-" + time.Now().Format("20060102-150405")
		_ = copyFile(dbDst, backupName, 0o600)
	}
	_ = os.Remove(dbDst + "-wal")
	_ = os.Remove(dbDst + "-shm")
	if err := copyFile(src, dbDst, 0o600); err != nil {
		return nil, fmt.Errorf("恢复数据库失败：%w", err)
	}
	_ = os.Chown(dbDst, t.UID, t.GID)
	if err := s.Sys.Enable(ctx, t.UnitName); err != nil {
		return nil, err
	}
	if err := s.Sys.Start(ctx, t.UnitName); err != nil {
		_ = s.failTenant(t, "tenant.restore_backup", "恢复后启动失败", err, ac)
		return t, err
	}
	info, err := s.waitHealthy(ctx, t.Port, "", 45*time.Second)
	if err != nil {
		_ = s.failTenant(t, "tenant.restore_backup", "恢复后健康检查失败", err, ac)
		return t, err
	}
	t.Status = domain.StatusRunning
	t.Version = info.Version
	t.LastHealthOK = true
	t.LastHealthAt = time.Now().Format(time.RFC3339)
	t.LastError = ""
	_ = s.Store.UpdateTenant(t)
	s.Audit(ac, "backup.restore", t, map[string]any{"backup_id": b.ID}, nil)
	return t, nil
}

// WriteBackupTar streams a backup directory as a .tar.gz without loading it
// into memory.
func (s *Service) WriteBackupTar(w io.Writer, backupID int64) error {
	b, err := s.Store.GetBackup(backupID)
	if err != nil {
		return errors.New("备份不存在")
	}
	if !isUnder(b.Path, s.Cfg.BackupDir()) {
		return errors.New("备份路径不合法")
	}
	gz := gzip.NewWriter(w)
	defer gz.Close()
	tw := tar.NewWriter(gz)
	defer tw.Close()
	return filepath.WalkDir(b.Path, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(b.Path, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = filepath.ToSlash(rel)
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(tw, f)
		closeErr := f.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
}

func dirSize(path string) (int64, error) {
	var total int64
	err := filepath.WalkDir(path, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if info, err := d.Info(); err == nil {
			total += info.Size()
		}
		return nil
	})
	return total, err
}

func isUnder(path, root string) bool {
	absPath, err1 := filepath.Abs(path)
	absRoot, err2 := filepath.Abs(root)
	if err1 != nil || err2 != nil {
		return false
	}
	rel, err := filepath.Rel(absRoot, absPath)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}

// PurgeExpiredTrash permanently deletes tenants whose retention window ended.
func (s *Service) PurgeExpiredTrash(ctx context.Context) {
	days := s.Settings().TrashRetention
	cutoff := time.Now().AddDate(0, 0, -days)
	items, err := s.Store.TrashedTenants()
	if err != nil {
		return
	}
	ac := AuditContext{Actor: "system"}
	for _, t := range items {
		deleted, err := time.Parse(time.RFC3339, t.DeletedAt)
		if err != nil || deleted.After(cutoff) {
			continue
		}
		_ = s.PurgeTenant(ctx, t.ID, ac)
	}
}

func (s *Service) PurgeExpiredAudit() {
	days := s.Settings().AuditRetention
	cutoff := time.Now().AddDate(0, 0, -days).Format(time.RFC3339)
	_, _ = s.Store.PurgeAuditBefore(cutoff)
	_, _ = s.Store.PurgeExpiredSessions(time.Now().Format(time.RFC3339))
}
