package fleet

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"clawbench-fleet/internal/domain"
	"clawbench-fleet/internal/sysops"
)

// sshRecorder wraps the simulated system and remembers the account changes
// that SSH provisioning makes, which the in-memory fake does not expose.
type sshRecorder struct {
	sysops.System

	shells    map[string]string
	passwords map[string]string
	locked    map[string]bool

	shellErr    error
	passwordErr error
	lockErr     error
	restartErr  error
	status      sysops.SSHStatus
	statusErr   error
}

func newSSHRecorder() *sshRecorder {
	return &sshRecorder{
		shells:    map[string]string{},
		passwords: map[string]string{},
		locked:    map[string]bool{},
		status:    sysops.SSHStatus{Installed: true, Running: true, PasswordAuth: true, Port: 22},
	}
}

func (r *sshRecorder) SetShell(ctx context.Context, username, shell string) error {
	if r.shellErr != nil {
		return r.shellErr
	}
	r.shells[username] = shell
	return nil
}

func (r *sshRecorder) SetPassword(ctx context.Context, username, password string) error {
	if r.passwordErr != nil {
		return r.passwordErr
	}
	r.passwords[username] = password
	r.locked[username] = false
	return nil
}

func (r *sshRecorder) LockPassword(ctx context.Context, username string) error {
	if r.lockErr != nil {
		return r.lockErr
	}
	delete(r.passwords, username)
	r.locked[username] = true
	return nil
}

func (r *sshRecorder) SSHStatus(ctx context.Context) (sysops.SSHStatus, error) {
	return r.status, r.statusErr
}

func (r *sshRecorder) Restart(ctx context.Context, unit string) error {
	if r.restartErr != nil {
		return r.restartErr
	}
	return r.System.Restart(ctx, unit)
}

func newSSHTestService(t *testing.T) (*Service, *sshRecorder) {
	t.Helper()
	rec := newSSHRecorder()
	svc, _ := newTestServiceWith(t, func(inner sysops.System) sysops.System {
		rec.System = inner
		return rec
	})
	return svc, rec
}

func TestCreateTenantWithSSH(t *testing.T) {
	svc, rec := newSSHTestService(t)
	ctx := context.Background()

	tenant, password, err := svc.CreateTenant(ctx, CreateTenantInput{Username: "alice"}, AuditContext{})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !tenant.SSHEnabled {
		t.Fatal("new tenant should have SSH enabled by default")
	}
	if got := rec.shells["alice"]; got != svc.Cfg.SSH.Shell {
		t.Fatalf("shell = %q want %q", got, svc.Cfg.SSH.Shell)
	}
	if got := rec.passwords["alice"]; got != password {
		t.Fatalf("linux password = %q want %q", got, password)
	}
	config, err := os.ReadFile(svc.Cfg.TenantConfigPath("alice"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(config), "sha256:") {
		t.Fatalf("config has no password hash:\n%s", config)
	}
}

func TestCreateTenantWithoutSSH(t *testing.T) {
	svc, rec := newSSHTestService(t)
	ctx := context.Background()

	tenant, _, err := svc.CreateTenant(ctx, CreateTenantInput{
		Username: "bob", SSHEnabled: boolPtr(false),
	}, AuditContext{})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if tenant.SSHEnabled {
		t.Fatal("tenant should not have SSH")
	}
	if _, ok := rec.shells["bob"]; ok {
		t.Fatal("shell should not be changed when SSH is off")
	}
	if _, ok := rec.passwords["bob"]; ok {
		t.Fatal("password should not be set when SSH is off")
	}
}

func TestSetTenantSSHTogglesAccess(t *testing.T) {
	svc, rec := newSSHTestService(t)
	ctx := context.Background()
	ac := AuditContext{Actor: "tester"}

	tenant, initial, err := svc.CreateTenant(ctx, CreateTenantInput{
		Username: "carol", SSHEnabled: boolPtr(false),
	}, ac)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	enabled, password, err := svc.SetTenantSSH(ctx, tenant.ID, true, ac)
	if err != nil {
		t.Fatalf("enable: %v", err)
	}
	if !enabled.SSHEnabled || password == "" || password == initial {
		t.Fatalf("enable = %+v password %q", enabled, password)
	}
	if got := rec.shells["carol"]; got != svc.Cfg.SSH.Shell {
		t.Fatalf("shell after enable = %q", got)
	}
	if got := rec.passwords["carol"]; got != password {
		t.Fatalf("linux password after enable = %q want %q", got, password)
	}

	disabled, password, err := svc.SetTenantSSH(ctx, tenant.ID, false, ac)
	if err != nil {
		t.Fatalf("disable: %v", err)
	}
	if disabled.SSHEnabled || password != "" {
		t.Fatalf("disable = %+v password %q", disabled, password)
	}
	if got := rec.shells["carol"]; got != svc.Cfg.System.UserShell {
		t.Fatalf("shell after disable = %q want %q", got, svc.Cfg.System.UserShell)
	}
	if !rec.locked["carol"] {
		t.Fatal("password should be locked after disable")
	}

	_, again, err := svc.SetTenantSSH(ctx, tenant.ID, true, ac)
	if err != nil {
		t.Fatalf("re-enable: %v", err)
	}
	if again == password || again == initial {
		t.Fatal("re-enabling should generate a fresh password")
	}
}

func TestSetTenantSSHRejectedWhenPlatformDisablesIt(t *testing.T) {
	svc, _ := newSSHTestService(t)
	ctx := context.Background()
	tenant, _, err := svc.CreateTenant(ctx, CreateTenantInput{
		Username: "dave", SSHEnabled: boolPtr(false),
	}, AuditContext{})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	svc.Cfg.SSH.Enabled = false
	if _, _, err := svc.SetTenantSSH(ctx, tenant.ID, true, AuditContext{}); err == nil {
		t.Fatal("enabling SSH must fail when the platform disables it")
	}
}

func TestSetTenantSSHOnTrashedTenantFails(t *testing.T) {
	svc, _ := newSSHTestService(t)
	ctx := context.Background()
	tenant, _, err := svc.CreateTenant(ctx, CreateTenantInput{Username: "erin"}, AuditContext{})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := svc.DeleteTenant(ctx, tenant.ID, "erin", AuditContext{}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, _, err := svc.SetTenantSSH(ctx, tenant.ID, false, AuditContext{}); err == nil {
		t.Fatal("changing SSH on a trashed tenant should fail")
	}
}

func TestSetTenantSSHPutsShellBackWhenPasswordFails(t *testing.T) {
	svc, rec := newSSHTestService(t)
	ctx := context.Background()
	tenant, _, err := svc.CreateTenant(ctx, CreateTenantInput{
		Username: "frank", SSHEnabled: boolPtr(false),
	}, AuditContext{})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	rec.passwordErr = errors.New("chpasswd: boom")
	if _, _, err := svc.SetTenantSSH(ctx, tenant.ID, true, AuditContext{}); err == nil {
		t.Fatal("enable should fail when the password cannot be set")
	}
	if got := rec.shells["frank"]; got != svc.Cfg.System.UserShell {
		t.Fatalf("shell after failure = %q want %q", got, svc.Cfg.System.UserShell)
	}
}

func TestResetPasswordUpdatesLinuxPassword(t *testing.T) {
	svc, rec := newSSHTestService(t)
	ctx := context.Background()
	tenant, _, err := svc.CreateTenant(ctx, CreateTenantInput{Username: "grace"}, AuditContext{})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	_, password, err := svc.ResetPassword(ctx, tenant.ID, AuditContext{})
	if err != nil {
		t.Fatalf("reset: %v", err)
	}
	if got := rec.passwords["grace"]; got != password {
		t.Fatalf("linux password after reset = %q want %q", got, password)
	}
}

func TestRestoreTenantWithSSHRegeneratesPassword(t *testing.T) {
	svc, rec := newSSHTestService(t)
	ctx := context.Background()
	tenant, _, err := svc.CreateTenant(ctx, CreateTenantInput{Username: "heidi"}, AuditContext{})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := svc.DeleteTenant(ctx, tenant.ID, "heidi", AuditContext{}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	restored, password, err := svc.RestoreTenant(ctx, tenant.ID, AuditContext{})
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if !restored.SSHEnabled || password == "" {
		t.Fatalf("restore = %+v password %q", restored, password)
	}
	if got := rec.passwords["heidi"]; got != password {
		t.Fatalf("linux password after restore = %q want %q", got, password)
	}
}

func TestSSHStatusUsesConfiguredPort(t *testing.T) {
	svc, rec := newSSHTestService(t)
	rec.status.Port = 2222
	svc.Cfg.SSH.Port = 2200
	st, err := svc.SSHStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.Port != 2200 {
		t.Fatalf("port = %d want 2200", st.Port)
	}
}

func TestSSHStatusPropagatesProbeError(t *testing.T) {
	svc, rec := newSSHTestService(t)
	rec.statusErr = errors.New("probe failed")
	if _, err := svc.SSHStatus(context.Background()); err == nil {
		t.Fatal("probe error should be returned")
	}
}

func TestTenantSSHEnabledDefaultsFromSettings(t *testing.T) {
	svc, _ := newSSHTestService(t)
	ctx := context.Background()
	svc.Cfg.SSH.Enabled = false
	tenant, _, err := svc.CreateTenant(ctx, CreateTenantInput{Username: "ivan"}, AuditContext{})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if tenant.SSHEnabled {
		t.Fatal("global switch off should force SSH off")
	}
	if tenant.Status != domain.StatusRunning {
		t.Fatalf("status = %s", tenant.Status)
	}
}

func TestCreateTenantFailsWhenSSHPasswordCannotBeSet(t *testing.T) {
	svc, rec := newSSHTestService(t)
	rec.passwordErr = errors.New("chpasswd: boom")
	_, _, err := svc.CreateTenant(context.Background(), CreateTenantInput{Username: "judy"}, AuditContext{})
	if err == nil || !strings.Contains(err.Error(), "设置 SSH 密码失败") {
		t.Fatalf("create error = %v", err)
	}
}

func TestDisableSSHFailureKeepsTenantEnabled(t *testing.T) {
	svc, rec := newSSHTestService(t)
	ctx := context.Background()
	tenant, _, err := svc.CreateTenant(ctx, CreateTenantInput{Username: "kate"}, AuditContext{})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	rec.shellErr = errors.New("usermod: boom")
	if _, _, err := svc.SetTenantSSH(ctx, tenant.ID, false, AuditContext{}); err == nil {
		t.Fatal("disable should fail when the shell cannot be changed")
	}
	rec.shellErr = nil
	rec.lockErr = errors.New("passwd: boom")
	if _, _, err := svc.SetTenantSSH(ctx, tenant.ID, false, AuditContext{}); err == nil {
		t.Fatal("disable should fail when the password cannot be locked")
	}
	fresh, err := svc.GetTenant(tenant.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !fresh.SSHEnabled {
		t.Fatal("tenant should still report SSH enabled after a failed disable")
	}
}

func TestEnableSSHRestartFailureMarksTenantError(t *testing.T) {
	svc, rec := newSSHTestService(t)
	ctx := context.Background()
	tenant, _, err := svc.CreateTenant(ctx, CreateTenantInput{
		Username: "laura", SSHEnabled: boolPtr(false),
	}, AuditContext{})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	rec.restartErr = errors.New("systemctl: boom")
	updated, password, err := svc.SetTenantSSH(ctx, tenant.ID, true, AuditContext{})
	if err == nil {
		t.Fatal("enable should report the restart failure")
	}
	if password == "" || updated.Status != domain.StatusError {
		t.Fatalf("enable = %+v password %q", updated, password)
	}
}

func TestResetPasswordFailsWhenSSHPasswordCannotBeSet(t *testing.T) {
	svc, rec := newSSHTestService(t)
	ctx := context.Background()
	tenant, _, err := svc.CreateTenant(ctx, CreateTenantInput{Username: "mallory"}, AuditContext{})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	rec.passwordErr = errors.New("chpasswd: boom")
	if _, _, err := svc.ResetPassword(ctx, tenant.ID, AuditContext{}); err == nil {
		t.Fatal("reset should fail when the Linux password cannot be set")
	}
}

func TestRepairTenantRestoresSSHShellAndPassword(t *testing.T) {
	svc, rec := newSSHTestService(t)
	ctx := context.Background()
	tenant, _, err := svc.CreateTenant(ctx, CreateTenantInput{Username: "nina"}, AuditContext{})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := os.Remove(svc.tenantConfigPath(tenant)); err != nil {
		t.Fatal(err)
	}
	repaired, password, err := svc.RepairTenant(ctx, tenant.ID, AuditContext{})
	if err != nil {
		t.Fatalf("repair: %v", err)
	}
	if !repaired.SSHEnabled || password == "" {
		t.Fatalf("repair = %+v password %q", repaired, password)
	}
	if got := rec.shells["nina"]; got != svc.Cfg.SSH.Shell {
		t.Fatalf("shell after repair = %q", got)
	}
	if got := rec.passwords["nina"]; got != password {
		t.Fatalf("linux password after repair = %q want %q", got, password)
	}
}

func TestRepairTenantKeepsExistingSSHPassword(t *testing.T) {
	svc, rec := newSSHTestService(t)
	ctx := context.Background()
	tenant, _, err := svc.CreateTenant(ctx, CreateTenantInput{Username: "peter"}, AuditContext{})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	repaired, password, err := svc.RepairTenant(ctx, tenant.ID, AuditContext{})
	if err != nil {
		t.Fatalf("repair: %v", err)
	}
	if !repaired.SSHEnabled || password != "" {
		t.Fatalf("repair = %+v password %q", repaired, password)
	}
	if got := rec.shells["peter"]; got != svc.Cfg.SSH.Shell {
		t.Fatalf("shell after repair = %q", got)
	}
}

func TestRepairTenantWithoutSSHLeavesShellAlone(t *testing.T) {
	svc, rec := newSSHTestService(t)
	ctx := context.Background()
	tenant, _, err := svc.CreateTenant(ctx, CreateTenantInput{
		Username: "quinn", SSHEnabled: boolPtr(false),
	}, AuditContext{})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := os.Remove(svc.tenantConfigPath(tenant)); err != nil {
		t.Fatal(err)
	}
	repaired, password, err := svc.RepairTenant(ctx, tenant.ID, AuditContext{})
	if err != nil {
		t.Fatalf("repair: %v", err)
	}
	if repaired.SSHEnabled || password == "" {
		t.Fatalf("repair = %+v password %q", repaired, password)
	}
	if _, ok := rec.shells["quinn"]; ok {
		t.Fatal("repair must not touch the shell when SSH is off")
	}
}

func TestRestoreTenantFailsWhenSSHSetupFails(t *testing.T) {
	svc, rec := newSSHTestService(t)
	ctx := context.Background()
	tenant, _, err := svc.CreateTenant(ctx, CreateTenantInput{Username: "olivia"}, AuditContext{})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := svc.DeleteTenant(ctx, tenant.ID, "olivia", AuditContext{}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	rec.shellErr = errors.New("usermod: boom")
	if _, _, err := svc.RestoreTenant(ctx, tenant.ID, AuditContext{}); err == nil {
		t.Fatal("restore should fail when SSH setup fails")
	}
}

// TestSetTenantSSHRejectsHalfBuiltTenant covers the window where a tenant row
// exists but the account is still being created (or torn down). Changing SSH
// then would race useradd/usermod, so the call has to be refused.
func TestSetTenantSSHRejectsHalfBuiltTenant(t *testing.T) {
	svc, _ := newSSHTestService(t)
	ctx := context.Background()
	tenant, _, err := svc.CreateTenant(ctx, CreateTenantInput{Username: "frank"}, AuditContext{})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	for _, status := range []string{domain.StatusCreating, domain.StatusDeleting} {
		tenant.Status = status
		if err := svc.Store.UpdateTenant(tenant); err != nil {
			t.Fatal(err)
		}
		if _, _, sshErr := svc.SetTenantSSH(ctx, tenant.ID, false, AuditContext{}); sshErr == nil {
			t.Fatalf("changing SSH while %s must be refused", status)
		}
	}
}
