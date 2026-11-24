package fleet

import (
	"context"
	"errors"
	"fmt"
	"time"

	"clawbench-fleet/internal/domain"
	"clawbench-fleet/internal/sysops"
)

// SSHStatus reports the machine's SSH readiness. The configured port wins over
// whatever sshd reports, which is the only way to describe a server whose
// sshd is behind a port forward.
func (s *Service) SSHStatus(ctx context.Context) (sysops.SSHStatus, error) {
	st, err := s.Sys.SSHStatus(ctx)
	if err != nil {
		return st, err
	}
	if s.Cfg.SSH.Port > 0 {
		st.Port = s.Cfg.SSH.Port
	}
	return st, nil
}

// SetTenantSSH turns shell access on or off for one tenant. Turning it on
// generates a fresh password and makes it the Clawbench password too, so the
// employee still has exactly one password. Turning it off leaves the Clawbench
// login alone and only takes away the shell.
func (s *Service) SetTenantSSH(ctx context.Context, id int64, enabled bool, ac AuditContext) (*domain.Tenant, string, error) {
	t, err := s.tenantByID(id)
	if err != nil {
		return nil, "", err
	}
	unlock := s.lockTenant(t.Username)
	defer unlock()
	switch t.Status {
	case domain.StatusCreating, domain.StatusDeleting:
		return nil, "", errors.New("实例正在创建或删除，请稍后再改 SSH")
	case domain.StatusTrashed:
		return nil, "", errors.New("回收站里的租户不能修改 SSH，请先恢复")
	}
	if enabled == t.SSHEnabled {
		return t, "", nil
	}
	if enabled && !s.Cfg.SSH.Enabled {
		return nil, "", errors.New("平台配置里关闭了 SSH（config.yaml 的 ssh.enabled）")
	}
	if acquireErr := s.acquire(ctx); acquireErr != nil {
		return nil, "", acquireErr
	}
	defer s.release()

	if !enabled {
		disabled, disableErr := s.disableTenantSSH(ctx, t, ac)
		return disabled, "", disableErr
	}
	return s.enableTenantSSH(ctx, t, ac)
}

func (s *Service) disableTenantSSH(ctx context.Context, t *domain.Tenant, ac AuditContext) (*domain.Tenant, error) {
	if err := s.revokeSSHAccess(ctx, t); err != nil {
		return t, err
	}
	t.SSHEnabled = false
	if err := s.Store.UpdateTenant(t); err != nil {
		return t, err
	}
	s.Audit(ac, "tenant.ssh", t, map[string]any{"enabled": false}, nil)
	return t, nil
}

func (s *Service) enableTenantSSH(ctx context.Context, t *domain.Tenant, ac AuditContext) (*domain.Tenant, string, error) {
	password, err := GeneratePassword(12)
	if err != nil {
		return nil, "", err
	}
	if err := s.applySSHAccess(ctx, t, password); err != nil {
		return t, "", err
	}
	if err := s.writeTenantConfig(t, password); err != nil {
		// The account is already open; put it back rather than leave a shell
		// whose password nobody knows.
		_ = s.revokeSSHAccess(ctx, t)
		return t, "", fmt.Errorf("写配置失败：%w", err)
	}
	t.SSHEnabled = true
	// A running instance has to pick up the new Clawbench password. A stopped
	// or errored one keeps it for the next start; starting it here would be a
	// surprise.
	if t.Status == domain.StatusRunning {
		if err := s.restartForSSH(ctx, t, ac); err != nil {
			return t, password, err
		}
	}
	if err := s.Store.UpdateTenant(t); err != nil {
		return t, password, err
	}
	s.Audit(ac, "tenant.ssh", t, map[string]any{"enabled": true}, nil)
	return t, password, nil
}

// restartForSSH restarts a running instance so the new password takes effect,
// and records the failure on the tenant when it does not come back.
func (s *Service) restartForSSH(ctx context.Context, t *domain.Tenant, ac AuditContext) error {
	if err := s.Sys.Restart(ctx, t.UnitName); err != nil {
		return s.failTenant(t, "tenant.ssh", "开启 SSH 后重启失败", err, ac)
	}
	info, err := s.waitHealthy(ctx, t.Port, "", 45*time.Second)
	if err != nil {
		return s.failTenant(t, "tenant.ssh", "开启 SSH 后健康检查失败", err, ac)
	}
	t.Version = info.Version
	t.Status = domain.StatusRunning
	t.LastHealthOK = true
	t.LastHealthAt = time.Now().Format(time.RFC3339)
	t.LastError = ""
	return nil
}
