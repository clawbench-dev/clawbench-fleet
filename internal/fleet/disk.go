package fleet

import (
	"context"
	"fmt"
	"strings"
	"time"

	"clawbench-fleet/internal/domain"
)

// diskSuspendNote is appended to the instance status when the disk limit stops
// it, so the list shows why the instance is down instead of a vague error.
const diskSuspendNote = "磁盘用量超过上限"

// diskSSHLockedNote marks a suspension that also took SSH away. The start path
// looks for it, so a tenant that was already broken before the suspension is
// not "restored" by accident.
const diskSSHLockedNote = "SSH 已锁定"

const gib = 1024 * 1024 * 1024

// diskLimitBytes converts a tenant's limit in GB to bytes; 0 means unlimited.
func diskLimitBytes(gb int) int64 {
	if gb <= 0 {
		return 0
	}
	return int64(gb) * gib
}

// diskOverLimit reports whether usage is at or above the tenant's limit.
func diskOverLimit(t *domain.Tenant, used int64) bool {
	limit := diskLimitBytes(t.DiskQuotaGB)
	return limit > 0 && used >= limit
}

// enforceDiskLimit stops a tenant that has grown past its limit. Stopping is
// the only lever that really frees the disk: the instance is what writes the
// files. SSH is locked too, otherwise the employee could keep filling the disk
// through the shell.
func (s *Service) enforceDiskLimit(ctx context.Context, t *domain.Tenant) {
	if t.Status == domain.StatusRunning {
		_ = s.Sys.Disable(ctx, t.UnitName)
		_ = s.Sys.Stop(ctx, t.UnitName)
	}
	_ = s.killTenantProcesses(ctx, t)
	msg := fmt.Sprintf("%s（已用 %s，上限 %d GB），实例已停止，清理或调大上限后点「启动」恢复",
		diskSuspendNote, formatGB(t.DiskBytes), t.DiskQuotaGB)
	if t.SSHEnabled {
		// Only the password is locked. The shell is kept so the admin does not
		// have to reset SSH access as well; a shell behind a locked password
		// is harmless.
		_ = s.Sys.LockPassword(ctx, t.Username)
		msg += "。" + diskSSHLockedNote + "，启动后需要重置密码才能再 SSH"
	}
	t.Status = domain.StatusError
	t.LastHealthOK = false
	t.LastError = msg
	_ = s.Store.UpdateTenant(t)
	s.Audit(AuditContext{Actor: actorSystem}, "tenant.disk_suspend", t,
		map[string]any{"disk_bytes": t.DiskBytes, "disk_quota_gb": t.DiskQuotaGB}, nil)
}

// killTenantProcesses ends whatever the employee left running outside the
// instance: a background job, a shell started over SSH, or a download into the
// home directory. Without this a "stopped" tenant can still write to disk.
func (s *Service) killTenantProcesses(ctx context.Context, t *domain.Tenant) error {
	killCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	return s.Sys.KillUserProcesses(killCtx, t.Username)
}

// diskGuardBeforeStart refuses to start (or restart) an instance that is still
// over its limit, which would immediately fill the disk again.
func (s *Service) diskGuardBeforeStart(ctx context.Context, t *domain.Tenant) error {
	if t.DiskQuotaGB <= 0 {
		return nil
	}
	used, err := s.Sys.DiskUsage(ctx, t.HomeDir)
	if err != nil {
		return nil //nolint:nilerr // a failed measurement must not block a legitimate start
	}
	t.DiskBytes = used
	_ = s.Store.SetTenantDisk(t.ID, used, time.Now().Format(time.RFC3339))
	if diskOverLimit(t, used) {
		return fmt.Errorf("磁盘还超着上限（已用 %s，上限 %d GB）：请先删掉一些文件或调大上限，再启动。目录：%s",
			formatGB(used), t.DiskQuotaGB, t.HomeDir)
	}
	return nil
}

// clearSuspensionNotes rewrites the error a disk suspension left behind once
// the instance runs again. The password itself cannot be unlocked without the
// plaintext, so the admin still has to reset it; what this removes is the
// misleading "SSH 已锁定" message.
func (s *Service) clearSuspensionNotes(t *domain.Tenant) {
	if !strings.Contains(t.LastError, diskSuspendNote) {
		return
	}
	if strings.Contains(t.LastError, diskSSHLockedNote) {
		t.LastError = "实例已恢复运行；SSH 密码已在上次停用磁盘时锁定，需要重置密码后才能再登录"
		return
	}
	t.LastError = ""
}

// formatGB renders a byte count with one decimal for human-facing messages.
func formatGB(bytes int64) string {
	if bytes < gib {
		return fmt.Sprintf("%.0f MB", float64(bytes)/(1024*1024))
	}
	return fmt.Sprintf("%.1f GB", float64(bytes)/gib)
}
