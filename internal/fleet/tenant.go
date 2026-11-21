package fleet

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"clawbench-fleet/internal/domain"
	"clawbench-fleet/internal/store"
)

func (s *Service) GetTenant(id int64) (*domain.Tenant, error) {
	return s.tenantByID(id)
}

func (s *Service) TenantDetail(id int64) (*TenantDTO, error) {
	t, err := s.tenantByID(id)
	if err != nil {
		return nil, err
	}
	// Disk usage is normally refreshed nightly. The detail page is the one
	// place where a stale 0 B would be actively misleading, so compute it once
	// if it has never been sampled.
	if t.DiskChecked == "" && t.Status != domain.StatusTrashed {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		if size, err := s.Sys.DiskUsage(ctx, t.HomeDir); err == nil {
			at := time.Now().Format(time.RFC3339)
			_ = s.Store.SetTenantDisk(t.ID, size, at)
			t.DiskBytes = size
			t.DiskChecked = at
		}
		cancel()
	}
	// 最近活跃时间同理：刚建好的实例还没被扫过时，详情页按需补一次，
	// 免得一直显示“—”。已经有值就不再重复打开数据库。
	if t.LastActiveAt == "" && t.Status != domain.StatusTrashed {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		s.RefreshTenantLastActive(ctx, t)
		cancel()
	}
	dto := s.TenantDTO(*t)
	return &dto, nil
}

func (s *Service) GetBackup(id int64) (*domain.Backup, error) {
	return s.Store.GetBackup(id)
}

type CreateTenantInput struct {
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	Notes       string `json:"notes"`
	CPUQuota    string `json:"cpu_quota"`
	MemoryMax   string `json:"memory_max"`
	TasksMax    int    `json:"tasks_max"`
}

type TenantDTO struct {
	domain.Tenant
	Address   string         `json:"address"`
	Metrics   domain.Metrics `json:"metrics"`
	Protected bool           `json:"protected"`
}

func (s *Service) TenantDTO(t domain.Tenant) TenantDTO {
	return TenantDTO{
		Tenant:    t,
		Address:   s.Address(t),
		Metrics:   s.GetMetrics(t.ID),
		Protected: domain.ProtectedUsername(t.Username),
	}
}

func (s *Service) ListTenants(f store.TenantFilter) ([]TenantDTO, int, error) {
	items, total, err := s.Store.ListTenants(f)
	if err != nil {
		return nil, 0, err
	}
	out := make([]TenantDTO, 0, len(items))
	for _, t := range items {
		out = append(out, s.TenantDTO(t))
	}
	return out, total, nil
}

func (s *Service) GetMetrics(id int64) domain.Metrics {
	s.metricsMu.RLock()
	defer s.metricsMu.RUnlock()
	return s.metrics[id]
}

func (s *Service) setMetrics(id int64, m domain.Metrics) {
	s.metricsMu.Lock()
	s.metrics[id] = m
	s.metricsMu.Unlock()
}

// CreateTenant runs the whole creation synchronously. The HTTP layer uses
// CreateTenantAsync instead so the page does not have to sit through the
// 60-second health wait; the CSV import still calls this one because it writes
// per-row results as it goes.
func (s *Service) CreateTenant(ctx context.Context, in CreateTenantInput, ac AuditContext) (*domain.Tenant, string, error) {
	t, password, err := s.CreateTenantAsync(ctx, in, ac)
	if err != nil {
		return t, password, err
	}
	// CreateTenantAsync returns while the tenant is still `creating`. Wait for
	// the background job so this caller gets the finished instance, which is
	// what the CSV import needs to write its per-row result.
	if msg, ok := s.waitForCreation(ctx, t.ID, 3*time.Minute); !ok {
		return t, password, errors.New(msg)
	}
	fresh, err := s.Store.GetTenant(t.ID)
	if err != nil {
		return t, password, nil
	}
	if fresh.Status == domain.StatusError {
		return fresh, password, fmt.Errorf("实例已创建但未就绪：%s", fresh.LastError)
	}
	return fresh, password, nil
}

// CreateTenantAsync records the tenant and returns as soon as the record
// exists; the slow part (useradd, systemd, waiting for the instance to come
// up) runs in the background. The password is generated up front so the caller
// can show it immediately.
func (s *Service) CreateTenantAsync(ctx context.Context, in CreateTenantInput, ac AuditContext) (*domain.Tenant, string, error) {
	username := strings.TrimSpace(in.Username)
	if err := ValidateUsername(username); err != nil {
		return nil, "", err
	}
	unlock := s.lockTenant(username)
	defer unlock()

	if existing, err := s.Store.GetTenantByUsername(username); err == nil {
		if existing.Status == domain.StatusTrashed {
			return nil, "", fmt.Errorf("回收站里还有同名租户，请先恢复或彻底删除")
		}
		if existing.Status == domain.StatusError {
			return nil, "", fmt.Errorf("上次创建 %s 失败并留下了记录，请先在列表里删掉它再重建", username)
		}
		return nil, "", fmt.Errorf("登录名 %s 已存在", username)
	}
	exists, err := s.Sys.UserExists(ctx, username)
	if err != nil {
		return nil, "", err
	}
	if exists {
		return nil, "", fmt.Errorf("Linux 用户 %s 已存在", username)
	}
	if err := s.acquire(ctx); err != nil {
		return nil, "", err
	}
	defer s.release()

	st := s.Settings()
	uid, err := s.allocateUID(ctx)
	if err != nil {
		return nil, "", err
	}

	s.createMu.Lock()
	ports, err := s.Store.AllocatePorts(st.PortStart, st.PortEnd, st.PortsPerTenant, 0)
	if err != nil {
		s.createMu.Unlock()
		return nil, "", err
	}
	t := &domain.Tenant{
		Username:    username,
		DisplayName: strings.TrimSpace(in.DisplayName),
		UID:         uid,
		GID:         uid,
		Port:        ports[0],
		Status:      domain.StatusCreating,
		CPUQuota:    firstNonEmpty(in.CPUQuota, st.DefaultCPUQuota),
		MemoryMax:   firstNonEmpty(in.MemoryMax, st.DefaultMemoryMax),
		TasksMax:    firstInt(in.TasksMax, st.DefaultTasksMax),
		HomeDir:     s.Cfg.TenantHome(username),
		DataDir:     s.Cfg.TenantDataDir(username),
		UnitName:    s.Cfg.UnitName(username),
		Notes:       strings.TrimSpace(in.Notes),
	}
	if err := s.Store.CreateTenant(t); err != nil {
		_ = s.Store.ReleasePorts(0)
		s.createMu.Unlock()
		return nil, "", err
	}
	if err := s.Store.AssignPorts(t.ID, ports); err != nil {
		_ = s.Store.DeleteTenant(t.ID)
		_ = s.Store.ReleasePorts(0)
		_ = s.Store.ReleasePorts(t.ID)
		s.createMu.Unlock()
		return nil, "", err
	}
	s.createMu.Unlock()

	password, err := GeneratePassword(12)
	if err != nil {
		s.rollbackCreate(ctx, t, true)
		return nil, "", err
	}

	// Hand the slow work to the background. The context here dies with the
	// HTTP request, so a caller hanging up would otherwise abort useradd or
	// the health wait halfway through.
	s.startProvision(t, password, ac)

	fresh, err := s.Store.GetTenant(t.ID)
	if err != nil {
		return t, password, nil
	}
	return fresh, password, nil
}

// startProvision kicks off the provisioning sequence in the background. It
// keeps a copy of the tenant so the caller's struct can be mutated freely.
func (s *Service) startProvision(t *domain.Tenant, password string, ac AuditContext) {
	snapshot := *t
	if s.provisioners == nil {
		s.provisioners = &provisionTracker{done: map[int64]chan struct{}{}}
	}
	ch := make(chan struct{})
	s.provisioners.mu.Lock()
	s.provisioners.done[snapshot.ID] = ch
	s.provisioners.mu.Unlock()

	go func() {
		defer func() {
			if r := recover(); r != nil {
				msg := fmt.Sprintf("创建过程意外中断：%v", r)
				_ = s.Store.SetTenantStatus(snapshot.ID, domain.StatusError, msg)
				s.Audit(ac, "tenant.create", &snapshot, map[string]any{"port": snapshot.Port, "uid": snapshot.UID}, errors.New(msg))
			}
			s.provisioners.mu.Lock()
			delete(s.provisioners.done, snapshot.ID)
			s.provisioners.mu.Unlock()
			close(ch)
		}()
		// Not tied to the request: the whole point is to outlive it.
		ctx := context.Background()
		if err := s.acquire(ctx); err != nil {
			_ = s.Store.SetTenantStatus(snapshot.ID, domain.StatusError, err.Error())
			return
		}
		defer s.release()
		s.provisionTenant(ctx, &snapshot, password, ac)
	}()
}

// waitForCreation blocks until the background provisioning for id finishes.
func (s *Service) waitForCreation(ctx context.Context, id int64, timeout time.Duration) (string, bool) {
	if s.provisioners == nil {
		return "", true
	}
	s.provisioners.mu.Lock()
	ch, ok := s.provisioners.done[id]
	s.provisioners.mu.Unlock()
	if !ok {
		return "", true
	}
	select {
	case <-ch:
		return "", true
	case <-time.After(timeout):
		return "实例还在后台创建中，稍后刷新列表查看结果", false
	case <-ctx.Done():
		return "实例还在后台创建中，稍后刷新列表查看结果", false
	}
}

// provisionTenant does the expensive part of creating a tenant: the Linux
// account, the config, the systemd unit, then waiting for the instance to
// answer its health check. Failures leave the record in place with a reason so
// the operator can see what went wrong and retry or clean up.
func (s *Service) provisionTenant(ctx context.Context, t *domain.Tenant, password string, ac AuditContext) {
	if err := s.Sys.CreateUser(ctx, t.Username, t.UID); err != nil {
		s.failCreate(ctx, t, ac, fmt.Errorf("创建 Linux 用户失败：%w", err))
		return
	}
	if uid, gid, err := s.Sys.LookupIDs(ctx, t.Username); err == nil {
		t.UID, t.GID = uid, gid
	}
	if err := s.writeTenantConfig(t, password); err != nil {
		s.failCreate(ctx, t, ac, fmt.Errorf("写租户配置失败：%w", err))
		return
	}
	if err := s.writeUnit(ctx, t); err != nil {
		s.failCreate(ctx, t, ac, fmt.Errorf("写 systemd 单元失败：%w", err))
		return
	}
	if err := s.reloadUnits(ctx); err != nil {
		s.failCreate(ctx, t, ac, fmt.Errorf("systemd 重载失败：%w", err))
		return
	}
	if err := s.Sys.Enable(ctx, t.UnitName); err != nil {
		s.failCreate(ctx, t, ac, fmt.Errorf("启用服务失败：%w", err))
		return
	}
	if err := s.Sys.Start(ctx, t.UnitName); err != nil {
		s.failCreate(ctx, t, ac, fmt.Errorf("启动服务失败：%w", err))
		return
	}

	info, err := s.waitHealthy(ctx, t.Port, "", 60*time.Second)
	if err != nil {
		s.failCreate(ctx, t, ac, fmt.Errorf("实例已创建但未就绪：%w", err))
		return
	}

	t.Status = domain.StatusRunning
	t.Version = info.Version
	t.LastHealthOK = true
	t.LastHealthAt = time.Now().Format(time.RFC3339)
	t.LastError = ""
	_ = s.Store.UpdateTenant(t)
	s.Audit(ac, "tenant.create", t, map[string]any{"port": t.Port, "uid": t.UID}, nil)
}

// failCreate records why a creation stopped. The tenant stays in the list
// with an error so the operator can see it and clean up; the Linux user and
// any unit that were created are left alone because partially built state is
// easier to inspect than state that silently vanished.
func (s *Service) failCreate(ctx context.Context, t *domain.Tenant, ac AuditContext, err error) {
	t.Status = domain.StatusError
	t.LastError = err.Error()
	t.LastHealthOK = false
	t.LastHealthAt = time.Now().Format(time.RFC3339)
	_ = s.Store.UpdateTenant(t)
	s.Audit(ac, "tenant.create", t, map[string]any{"port": t.Port, "uid": t.UID}, err)
}

// rollbackCreate undoes whatever exists for a tenant whose record was already
// written. When deleteRecord is false the database row survives so the error
// stays visible.
func (s *Service) rollbackCreate(ctx context.Context, t *domain.Tenant, deleteRecord bool) {
	_ = s.Sys.Stop(ctx, t.UnitName)
	_ = s.Sys.Disable(ctx, t.UnitName)
	_ = s.Sys.RemoveUnit(ctx, t.UnitName)
	_ = s.reloadUnits(ctx)
	_ = s.Sys.DeleteUser(ctx, t.Username)
	_ = os.RemoveAll(t.HomeDir)
	if deleteRecord {
		_ = s.Store.DeleteTenant(t.ID)
		_ = s.Store.ReleasePorts(t.ID)
	}
}

type provisionTracker struct {
	mu   sync.Mutex
	done map[int64]chan struct{}
}

func firstNonEmpty(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
}

func firstInt(v, fallback int) int {
	if v <= 0 {
		return fallback
	}
	return v
}

type UpdateTenantInput struct {
	DisplayName *string `json:"display_name"`
	Notes       *string `json:"notes"`
	CPUQuota    *string `json:"cpu_quota"`
	MemoryMax   *string `json:"memory_max"`
	TasksMax    *int    `json:"tasks_max"`
}

func (s *Service) UpdateTenant(ctx context.Context, id int64, in UpdateTenantInput, ac AuditContext) (*domain.Tenant, error) {
	t, err := s.tenantByID(id)
	if err != nil {
		return nil, err
	}
	unlock := s.lockTenant(t.Username)
	defer unlock()
	if in.DisplayName != nil {
		t.DisplayName = strings.TrimSpace(*in.DisplayName)
	}
	if in.Notes != nil {
		t.Notes = strings.TrimSpace(*in.Notes)
	}
	if in.CPUQuota != nil {
		if !cpuQuotaRe.MatchString(*in.CPUQuota) {
			return nil, errors.New("CPU 上限格式不对，例如 200%")
		}
		t.CPUQuota = *in.CPUQuota
	}
	if in.MemoryMax != nil {
		if !memoryMaxRe.MatchString(*in.MemoryMax) {
			return nil, errors.New("内存上限格式不对，例如 4G")
		}
		t.MemoryMax = *in.MemoryMax
	}
	if in.TasksMax != nil {
		if *in.TasksMax < 1 {
			return nil, errors.New("进程数上限必须大于 0")
		}
		t.TasksMax = *in.TasksMax
	}
	if err := s.writeUnit(ctx, t); err != nil {
		return nil, err
	}
	if err := s.reloadUnits(ctx); err != nil {
		return nil, err
	}
	if t.Status == domain.StatusRunning {
		if err := s.Sys.Restart(ctx, t.UnitName); err != nil {
			_ = s.failTenant(t, "tenant.update", "修改配额后重启失败", err, ac)
			return t, err
		}
		if info, err := s.waitHealthy(ctx, t.Port, "", 45*time.Second); err != nil {
			_ = s.failTenant(t, "tenant.update", "修改配额后健康检查失败", err, ac)
			return t, err
		} else {
			t.Version = info.Version
		}
	}
	t.LastError = ""
	_ = s.Store.UpdateTenant(t)
	s.Audit(ac, "tenant.update", t, map[string]any{
		"cpu_quota": t.CPUQuota, "memory_max": t.MemoryMax, "tasks_max": t.TasksMax,
	}, nil)
	return t, nil
}

func (s *Service) Logs(ctx context.Context, id int64, lines int, ac AuditContext) (string, error) {
	t, err := s.tenantByID(id)
	if err != nil {
		return "", err
	}
	out, err := s.Sys.Journal(ctx, t.UnitName, lines)
	if err != nil && out == "" {
		return "", err
	}
	s.Audit(ac, "tenant.logs", t, map[string]any{"lines": lines}, nil)
	return out, nil
}

func (s *Service) StartTenant(ctx context.Context, id int64, ac AuditContext) (*domain.Tenant, error) {
	t, err := s.tenantByID(id)
	if err != nil {
		return nil, err
	}
	unlock := s.lockTenant(t.Username)
	defer unlock()
	if t.Status == domain.StatusTrashed || t.Status == domain.StatusDeleting {
		return nil, errors.New("租户已删除或正在删除")
	}
	if err := s.acquire(ctx); err != nil {
		return nil, err
	}
	defer s.release()

	if err := s.Sys.Enable(ctx, t.UnitName); err != nil {
		return nil, s.failTenant(t, "tenant.start", "启动失败", err, ac)
	}
	if err := s.Sys.Start(ctx, t.UnitName); err != nil {
		return nil, s.failTenant(t, "tenant.start", "启动失败", err, ac)
	}
	info, err := s.waitHealthy(ctx, t.Port, "", 45*time.Second)
	if err != nil {
		return nil, s.failTenant(t, "tenant.start", "启动后健康检查失败", err, ac)
	}
	t.Status = domain.StatusRunning
	t.Version = info.Version
	t.LastHealthOK = true
	t.LastHealthAt = time.Now().Format(time.RFC3339)
	t.LastError = ""
	_ = s.Store.UpdateTenant(t)
	s.Audit(ac, "tenant.start", t, nil, nil)
	return t, nil
}

func (s *Service) StopTenant(ctx context.Context, id int64, ac AuditContext) (*domain.Tenant, error) {
	t, err := s.tenantByID(id)
	if err != nil {
		return nil, err
	}
	unlock := s.lockTenant(t.Username)
	defer unlock()
	if err := s.acquire(ctx); err != nil {
		return nil, err
	}
	defer s.release()
	if err := s.Sys.Disable(ctx, t.UnitName); err != nil {
		return nil, s.failTenant(t, "tenant.stop", "停止失败", err, ac)
	}
	if err := s.Sys.Stop(ctx, t.UnitName); err != nil {
		return nil, s.failTenant(t, "tenant.stop", "停止失败", err, ac)
	}
	t.Status = domain.StatusStopped
	t.LastHealthOK = false
	t.LastError = ""
	_ = s.Store.UpdateTenant(t)
	s.Audit(ac, "tenant.stop", t, nil, nil)
	return t, nil
}

func (s *Service) RestartTenant(ctx context.Context, id int64, ac AuditContext) (*domain.Tenant, error) {
	t, err := s.tenantByID(id)
	if err != nil {
		return nil, err
	}
	unlock := s.lockTenant(t.Username)
	defer unlock()
	if err := s.acquire(ctx); err != nil {
		return nil, err
	}
	defer s.release()
	if err := s.Sys.Restart(ctx, t.UnitName); err != nil {
		return nil, s.failTenant(t, "tenant.restart", "重启失败", err, ac)
	}
	info, err := s.waitHealthy(ctx, t.Port, "", 45*time.Second)
	if err != nil {
		return nil, s.failTenant(t, "tenant.restart", "重启后健康检查失败", err, ac)
	}
	t.Status = domain.StatusRunning
	t.Version = info.Version
	t.LastHealthOK = true
	t.LastHealthAt = time.Now().Format(time.RFC3339)
	t.LastError = ""
	_ = s.Store.UpdateTenant(t)
	s.Audit(ac, "tenant.restart", t, nil, nil)
	return t, nil
}

func (s *Service) failTenant(t *domain.Tenant, auditAction, action string, cause error, ac AuditContext) error {
	t.Status = domain.StatusError
	t.LastError = action + "：" + cause.Error()
	t.LastHealthOK = false
	_ = s.Store.UpdateTenant(t)
	s.Audit(ac, auditAction, t, nil, cause)
	return fmt.Errorf("%s：%w", action, cause)
}

func (s *Service) ResetPassword(ctx context.Context, id int64, ac AuditContext) (*domain.Tenant, string, error) {
	t, err := s.tenantByID(id)
	if err != nil {
		return nil, "", err
	}
	unlock := s.lockTenant(t.Username)
	defer unlock()
	if err := s.acquire(ctx); err != nil {
		return nil, "", err
	}
	defer s.release()
	password, err := GeneratePassword(12)
	if err != nil {
		return nil, "", err
	}
	if t.UID == 0 {
		if t.UID, t.GID, err = s.Sys.LookupIDs(ctx, t.Username); err != nil {
			return nil, "", err
		}
	}
	if err := s.writeTenantConfig(t, password); err != nil {
		return nil, "", fmt.Errorf("写配置失败：%w", err)
	}
	if err := s.Sys.Restart(ctx, t.UnitName); err != nil {
		_ = s.failTenant(t, "tenant.reset_password", "重置密码后重启失败", err, ac)
		return t, password, fmt.Errorf("新密码已写入，但重启失败：%w", err)
	}
	if info, err := s.waitHealthy(ctx, t.Port, "", 45*time.Second); err != nil {
		_ = s.failTenant(t, "tenant.reset_password", "重置密码后健康检查失败", err, ac)
		return t, password, fmt.Errorf("新密码已写入，但实例未就绪：%w", err)
	} else {
		t.Version = info.Version
	}
	t.Status = domain.StatusRunning
	t.LastHealthOK = true
	t.LastHealthAt = time.Now().Format(time.RFC3339)
	t.LastError = ""
	_ = s.Store.UpdateTenant(t)
	s.Audit(ac, "tenant.reset_password", t, nil, nil)
	return t, password, nil
}

func (s *Service) RepairTenant(ctx context.Context, id int64, ac AuditContext) (*domain.Tenant, string, error) {
	t, err := s.tenantByID(id)
	if err != nil {
		return nil, "", err
	}
	unlock := s.lockTenant(t.Username)
	defer unlock()
	if err := s.acquire(ctx); err != nil {
		return nil, "", err
	}
	defer s.release()

	if t.UID == 0 {
		if t.UID, t.GID, err = s.Sys.LookupIDs(ctx, t.Username); err != nil {
			return nil, "", fmt.Errorf("Linux 用户不存在，无法修复：%w", err)
		}
	}
	newPassword := ""
	if _, err := os.Stat(s.tenantConfigPath(t)); errors.Is(err, os.ErrNotExist) {
		if newPassword, err = GeneratePassword(12); err != nil {
			return nil, "", err
		}
	}
	if err := s.writeTenantConfig(t, newPassword); err != nil {
		return nil, "", err
	}
	if err := s.writeUnit(ctx, t); err != nil {
		return nil, "", err
	}
	if err := s.reloadUnits(ctx); err != nil {
		return nil, "", err
	}
	if err := s.Sys.Restart(ctx, t.UnitName); err != nil {
		_ = s.failTenant(t, "tenant.repair", "修复后重启失败", err, ac)
		return t, newPassword, err
	}
	info, err := s.waitHealthy(ctx, t.Port, "", 45*time.Second)
	if err != nil {
		_ = s.failTenant(t, "tenant.repair", "修复后健康检查失败", err, ac)
		return t, newPassword, err
	}
	t.Status = domain.StatusRunning
	t.Version = info.Version
	t.LastHealthOK = true
	t.LastHealthAt = time.Now().Format(time.RFC3339)
	t.LastError = ""
	_ = s.Store.UpdateTenant(t)
	s.Audit(ac, "tenant.repair", t, nil, nil)
	return t, newPassword, nil
}

type trashMeta struct {
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	UID         int    `json:"uid"`
	GID         int    `json:"gid"`
	Port        int    `json:"port"`
	DeletedAt   string `json:"deleted_at"`
	HomeDir     string `json:"home_dir"`
	SizeBytes   int64  `json:"size_bytes"`
}

func (s *Service) DeleteTenant(ctx context.Context, id int64, confirmName string, ac AuditContext) (*domain.Tenant, error) {
	t, err := s.tenantByID(id)
	if err != nil {
		return nil, err
	}
	if domain.ProtectedUsername(t.Username) {
		return nil, errors.New("root 是系统账号，不允许删除")
	}
	if strings.TrimSpace(confirmName) != t.Username {
		return nil, errors.New("请完整输入登录名以确认删除")
	}
	unlock := s.lockTenant(t.Username)
	defer unlock()
	if err := s.acquire(ctx); err != nil {
		return nil, err
	}
	defer s.release()

	disk, _ := s.Sys.DiskUsage(ctx, t.HomeDir)
	oldStatus := t.Status
	t.Status = domain.StatusDeleting
	_ = s.Store.UpdateTenant(t)

	_ = s.Sys.Disable(ctx, t.UnitName)
	if err := s.Sys.Stop(ctx, t.UnitName); err != nil {
		_ = s.Store.SetTenantStatus(t.ID, oldStatus, err.Error())
		return nil, fmt.Errorf("停止服务失败：%w", err)
	}
	_ = s.Sys.RemoveUnit(ctx, t.UnitName)
	_ = s.reloadUnits(ctx)

	trashDir := filepath.Join(s.Cfg.TrashDir(), fmt.Sprintf("%s-%s", t.Username, time.Now().Format("20060102-150405")))
	if err := os.MkdirAll(trashDir, 0o700); err != nil {
		_ = s.Store.SetTenantStatus(t.ID, oldStatus, err.Error())
		return nil, err
	}
	trashHome := filepath.Join(trashDir, "home")
	if err := movePath(t.HomeDir, trashHome); err != nil {
		_ = s.Store.SetTenantStatus(t.ID, oldStatus, err.Error())
		return nil, fmt.Errorf("移动家目录失败：%w", err)
	}
	meta := trashMeta{
		Username: t.Username, DisplayName: t.DisplayName, UID: t.UID, GID: t.GID,
		Port: t.Port, DeletedAt: time.Now().Format(time.RFC3339), HomeDir: t.HomeDir, SizeBytes: disk,
	}
	metaData, _ := json.MarshalIndent(meta, "", "  ")
	_ = os.WriteFile(filepath.Join(trashDir, "meta.json"), metaData, 0o600)

	if err := s.Sys.DeleteUser(ctx, t.Username); err != nil {
		// Put the data back so the account and its files stay consistent.
		_ = movePath(trashHome, t.HomeDir)
		_ = os.RemoveAll(trashDir)
		_ = s.Store.SetTenantStatus(t.ID, oldStatus, err.Error())
		return nil, fmt.Errorf("删除 Linux 用户失败：%w", err)
	}

	_ = s.Store.ReservePorts(t.ID)
	t.Status = domain.StatusTrashed
	t.DeletedAt = time.Now().Format(time.RFC3339)
	t.TrashPath = trashDir
	t.DiskBytes = disk
	t.LastHealthOK = false
	t.LastError = ""
	_ = s.Store.UpdateTenant(t)
	s.Audit(ac, "tenant.delete", t, map[string]any{"trash_path": trashDir, "size_bytes": disk}, nil)
	return t, nil
}

func (s *Service) RestoreTenant(ctx context.Context, id int64, ac AuditContext) (*domain.Tenant, string, error) {
	t, err := s.tenantByID(id)
	if err != nil {
		return nil, "", err
	}
	if t.Status != domain.StatusTrashed {
		return nil, "", errors.New("这个租户不在回收站里")
	}
	unlock := s.lockTenant(t.Username)
	defer unlock()
	if err := s.acquire(ctx); err != nil {
		return nil, "", err
	}
	defer s.release()

	if existing, err := s.Store.GetTenantByUsername(t.Username); err == nil && existing.ID != t.ID {
		return nil, "", errors.New("同名租户已存在，不能恢复")
	}
	if exists, err := s.Sys.UserExists(ctx, t.Username); err != nil {
		return nil, "", err
	} else if exists {
		return nil, "", errors.New("同名 Linux 用户已存在，不能恢复")
	}

	meta := trashMeta{}
	if data, err := os.ReadFile(filepath.Join(t.TrashPath, "meta.json")); err == nil {
		_ = json.Unmarshal(data, &meta)
	}
	uid := t.UID
	if meta.UID > 0 {
		uid = meta.UID
	}
	if uid == 0 {
		if uid, err = s.allocateUID(ctx); err != nil {
			return nil, "", err
		}
	} else {
		used, _ := s.Sys.UsedUIDs(ctx)
		if dbUIDs, err := s.Store.AllTenantUIDs(); err == nil {
			for u := range dbUIDs {
				if u != uid {
					used[u] = true
				}
			}
		}
		if used[uid] {
			if uid, err = s.allocateUID(ctx); err != nil {
				return nil, "", err
			}
		}
	}
	if err := s.Sys.CreateUser(ctx, t.Username, uid); err != nil {
		return nil, "", fmt.Errorf("重建 Linux 用户失败：%w", err)
	}
	trashHome := filepath.Join(t.TrashPath, "home")
	if err := movePath(trashHome, t.HomeDir); err != nil {
		_ = s.Sys.DeleteUser(ctx, t.Username)
		return nil, "", fmt.Errorf("移回家目录失败：%w", err)
	}
	if actualUID, gid, err := s.Sys.LookupIDs(ctx, t.Username); err == nil {
		t.UID, t.GID = actualUID, gid
	}
	_ = s.Sys.Chown(ctx, t.HomeDir, t.UID, t.GID)

	newPassword := ""
	if _, err := os.Stat(s.tenantConfigPath(t)); errors.Is(err, os.ErrNotExist) {
		if newPassword, err = GeneratePassword(12); err != nil {
			return nil, "", err
		}
	}
	if err := s.writeTenantConfig(t, newPassword); err != nil {
		return nil, "", err
	}
	if err := s.writeUnit(ctx, t); err != nil {
		return nil, "", err
	}
	if err := s.reloadUnits(ctx); err != nil {
		return nil, "", err
	}
	if err := s.Sys.Enable(ctx, t.UnitName); err != nil {
		return nil, "", err
	}
	if err := s.Sys.Start(ctx, t.UnitName); err != nil {
		_ = s.failTenant(t, "tenant.restore", "恢复后启动失败", err, ac)
		return t, newPassword, err
	}
	info, err := s.waitHealthy(ctx, t.Port, "", 45*time.Second)
	if err != nil {
		_ = s.failTenant(t, "tenant.restore", "恢复后健康检查失败", err, ac)
		return t, newPassword, err
	}
	t.Status = domain.StatusRunning
	t.Version = info.Version
	t.LastHealthOK = true
	t.LastHealthAt = time.Now().Format(time.RFC3339)
	t.LastError = ""
	trashPath := t.TrashPath
	t.DeletedAt = ""
	t.TrashPath = ""
	_ = s.Store.UpdateTenant(t)
	_ = s.Store.SetPortsAllocated(t.ID)
	if trashPath != "" {
		_ = os.RemoveAll(trashPath)
	}
	s.Audit(ac, "tenant.restore", t, nil, nil)
	return t, newPassword, nil
}

func (s *Service) PurgeTenant(ctx context.Context, id int64, ac AuditContext) error {
	t, err := s.tenantByID(id)
	if err != nil {
		return err
	}
	if t.Status != domain.StatusTrashed {
		return errors.New("只能彻底删除回收站里的租户")
	}
	if domain.ProtectedUsername(t.Username) {
		return errors.New("root 是系统账号，不允许删除")
	}
	unlock := s.lockTenant(t.Username)
	defer unlock()
	if t.TrashPath != "" {
		if err := os.RemoveAll(t.TrashPath); err != nil {
			return err
		}
	}
	if err := s.Store.DeleteTenant(t.ID); err != nil {
		return err
	}
	if err := s.Store.ReleasePorts(t.ID); err != nil {
		return err
	}
	s.Audit(ac, "tenant.purge", t, nil, nil)
	return nil
}

// --- bulk ---

func (s *Service) BulkAction(ctx context.Context, action string, ids []int64, ac AuditContext) (*domain.Job, error) {
	if len(ids) == 0 {
		return nil, errors.New("没有选中租户")
	}
	switch action {
	case "start", "stop", "restart":
	default:
		return nil, fmt.Errorf("不支持的批量操作 %q", action)
	}
	payload, _ := json.Marshal(map[string]any{"action": action, "ids": ids})
	job, err := s.Store.CreateJob("bulk", string(payload), len(ids))
	if err != nil {
		return nil, err
	}
	itemIDs := make([]int64, 0, len(ids))
	for _, id := range ids {
		t, err := s.Store.GetTenant(id)
		name := fmt.Sprintf("#%d", id)
		if err == nil {
			name = t.Username
		}
		itemID, err := s.Store.AddJobItem(job.ID, id, name)
		if err != nil {
			return nil, err
		}
		itemIDs = append(itemIDs, itemID)
	}
	go s.runBulk(job.ID, action, ids, itemIDs, ac)
	return job, nil
}

func (s *Service) runBulk(jobID int64, action string, ids, itemIDs []int64, ac AuditContext) {
	_ = s.Store.MarkJobRunning(jobID)
	done, failed := 0, 0
	for i, id := range ids {
		_ = s.Store.UpdateJobItem(itemIDs[i], domain.JobRunning, "")
		var err error
		switch action {
		case "start":
			_, err = s.StartTenant(context.Background(), id, ac)
		case "stop":
			_, err = s.StopTenant(context.Background(), id, ac)
		case "restart":
			_, err = s.RestartTenant(context.Background(), id, ac)
		}
		done++
		status := domain.JobSucceeded
		errText := ""
		if err != nil {
			failed++
			status = domain.JobFailed
			errText = err.Error()
		}
		_ = s.Store.UpdateJobItem(itemIDs[i], status, errText)
		_ = s.Store.UpdateJobProgress(jobID, done, failed)
	}
	result, _ := json.Marshal(map[string]any{"done": done, "failed": failed})
	final := domain.JobSucceeded
	if failed == done {
		final = domain.JobFailed
	} else if failed > 0 {
		final = domain.JobPartial
	}
	_ = s.Store.FinishJob(jobID, final, string(result))
}

// --- import ---

type ImportRow struct {
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	Notes       string `json:"notes"`
}

type ImportPreviewRow struct {
	Row int `json:"row"`
	ImportRow
	Error string `json:"error"`
}

func (s *Service) PreviewImport(rows []ImportRow) []ImportPreviewRow {
	seen := map[string]bool{}
	out := make([]ImportPreviewRow, 0, len(rows))
	for i, row := range rows {
		row.Username = strings.TrimSpace(row.Username)
		errText := ""
		if err := ValidateUsername(row.Username); err != nil {
			errText = err.Error()
		} else if seen[row.Username] {
			errText = "文件内登录名重复"
		} else if existing, err := s.Store.GetTenantByUsername(row.Username); err == nil {
			if existing.Status == domain.StatusTrashed {
				errText = "回收站里已有同名租户"
			} else {
				errText = "登录名已存在"
			}
		}
		seen[row.Username] = true
		out = append(out, ImportPreviewRow{Row: i + 2, ImportRow: row, Error: errText})
	}
	// Ports are the hard capacity limit. Tell the admin before they start a
	// batch that would run out halfway through.
	st := s.Settings()
	if capacity, err := s.Store.FreePortGroups(st.PortStart, st.PortEnd, st.PortsPerTenant); err == nil {
		valid := 0
		for i := range out {
			if out[i].Error != "" {
				continue
			}
			valid++
			if valid > capacity {
				out[i].Error = fmt.Sprintf("端口不够：当前最多还能建 %d 个租户", capacity)
			}
		}
	}
	return out
}

func (s *Service) ImportRows(ctx context.Context, rows []ImportRow, ac AuditContext) (*domain.Job, error) {
	if len(rows) == 0 {
		return nil, errors.New("CSV 里没有数据行")
	}
	payload, _ := json.Marshal(map[string]any{"rows": len(rows)})
	job, err := s.Store.CreateJob("import", string(payload), len(rows))
	if err != nil {
		return nil, err
	}
	itemIDs := make([]int64, 0, len(rows))
	for _, row := range rows {
		itemID, err := s.Store.AddJobItem(job.ID, 0, strings.TrimSpace(row.Username))
		if err != nil {
			return nil, err
		}
		itemIDs = append(itemIDs, itemID)
	}
	go s.runImport(job.ID, rows, itemIDs, ac)
	return job, nil
}

func (s *Service) runImport(jobID int64, rows []ImportRow, itemIDs []int64, ac AuditContext) {
	_ = s.Store.MarkJobRunning(jobID)
	var success bytes.Buffer
	successCSV := csv.NewWriter(&success)
	_ = successCSV.Write([]string{"username", "display_name", "address", "password"})
	var failures bytes.Buffer
	failCSV := csv.NewWriter(&failures)
	_ = failCSV.Write([]string{"username", "error"})

	done, failed := 0, 0
	for i, row := range rows {
		_ = s.Store.UpdateJobItem(itemIDs[i], domain.JobRunning, "")
		t, password, err := s.CreateTenant(context.Background(), CreateTenantInput{
			Username: row.Username, DisplayName: row.DisplayName, Notes: row.Notes,
		}, ac)
		done++
		if err != nil {
			failed++
			_ = s.Store.UpdateJobItem(itemIDs[i], domain.JobFailed, err.Error())
			_ = failCSV.Write([]string{row.Username, err.Error()})
		} else {
			_ = s.Store.UpdateJobItem(itemIDs[i], domain.JobSucceeded, "")
			_ = successCSV.Write([]string{t.Username, t.DisplayName, s.Address(*t), password})
		}
		_ = s.Store.UpdateJobProgress(jobID, done, failed)
	}
	successCSV.Flush()
	failCSV.Flush()
	s.setArtifact(jobID, "success.csv", success.Bytes())
	s.setArtifact(jobID, "failures.csv", failures.Bytes())

	result, _ := json.Marshal(map[string]any{"done": done, "failed": failed})
	final := domain.JobSucceeded
	if failed == done {
		final = domain.JobFailed
	} else if failed > 0 {
		final = domain.JobPartial
	}
	_ = s.Store.FinishJob(jobID, final, string(result))
	s.Audit(ac, "tenant.import", nil, map[string]any{"total": len(rows), "failed": failed}, nil)
}

func (s *Service) setArtifact(jobID int64, name string, data []byte) {
	s.artifactsMu.Lock()
	defer s.artifactsMu.Unlock()
	m := s.artifacts[jobID]
	if m == nil {
		m = map[string][]byte{}
		s.artifacts[jobID] = m
	}
	m[name] = data
	s.artifactAt[jobID] = time.Now()
}

func (s *Service) Artifact(jobID int64, name string) ([]byte, bool) {
	s.artifactsMu.Lock()
	defer s.artifactsMu.Unlock()
	data, ok := s.artifacts[jobID][name]
	return data, ok
}

// --- file helpers ---

func movePath(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	if err := copyTree(src, dst); err != nil {
		return err
	}
	return os.RemoveAll(src)
}

func copyTree(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return copyFile(src, dst, info.Mode())
	}
	if err := os.MkdirAll(dst, info.Mode().Perm()); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		s := filepath.Join(src, entry.Name())
		d := filepath.Join(dst, entry.Name())
		fi, err := entry.Info()
		if err != nil {
			return err
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(s)
			if err != nil {
				return err
			}
			if err := os.Symlink(target, d); err != nil {
				return err
			}
			continue
		}
		if entry.IsDir() {
			if err := copyTree(s, d); err != nil {
				return err
			}
			continue
		}
		if !fi.Mode().IsRegular() {
			continue
		}
		if err := copyFile(s, d, fi.Mode()); err != nil {
			return err
		}
	}
	return nil
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode.Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
