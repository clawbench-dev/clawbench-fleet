package fleet

import (
	"context"

	"clawbench-fleet/internal/sysops"
)

// hookSystem wraps the simulated backend and fails the operations a test
// names. Everything else still behaves like the real fake, so a failure test
// only has to describe the one broken step.
type hookSystem struct {
	sysops.System

	createUserErr  error
	deleteUserErr  error
	lookupErr      error
	usedUIDsErr    error
	writeUnitErr   error
	reloadErr      error
	enableErr      error
	disableErr     error
	startErr       error
	stopErr        error
	restartErr     error
	stopPortErr    error
	isActiveErr    error
	journalErr     error
	metricsErr     error
	diskUsageErr   error
	listUsersErr   error
	listUnitsErr   error
	listenersErr   error
	npmVersionsErr error
	npmRegistryErr error
	npmInstallErr  error
	npmInstallNoop bool

	// silentStart makes Start/Restart succeed without bringing an instance up,
	// which is how a health-check timeout is produced quickly.
	silentStart bool
	// silentRestart also stops the old instance first, so the health check has
	// nothing to answer on.
	silentRestart bool
}

func (h hookSystem) CreateUser(ctx context.Context, username string, uid int) error {
	if h.createUserErr != nil {
		return h.createUserErr
	}
	return h.System.CreateUser(ctx, username, uid)
}

func (h hookSystem) DeleteUser(ctx context.Context, username string) error {
	if h.deleteUserErr != nil {
		return h.deleteUserErr
	}
	return h.System.DeleteUser(ctx, username)
}

func (h hookSystem) LookupIDs(ctx context.Context, username string) (int, int, error) {
	if h.lookupErr != nil {
		return 0, 0, h.lookupErr
	}
	return h.System.LookupIDs(ctx, username)
}

func (h hookSystem) UsedUIDs(ctx context.Context) (map[int]bool, error) {
	if h.usedUIDsErr != nil {
		return nil, h.usedUIDsErr
	}
	return h.System.UsedUIDs(ctx)
}

func (h hookSystem) WriteUnit(ctx context.Context, unit, content string) error {
	if h.writeUnitErr != nil {
		return h.writeUnitErr
	}
	return h.System.WriteUnit(ctx, unit, content)
}

func (h hookSystem) DaemonReload(ctx context.Context) error {
	if h.reloadErr != nil {
		return h.reloadErr
	}
	return h.System.DaemonReload(ctx)
}

func (h hookSystem) Enable(ctx context.Context, unit string) error {
	if h.enableErr != nil {
		return h.enableErr
	}
	return h.System.Enable(ctx, unit)
}

func (h hookSystem) Disable(ctx context.Context, unit string) error {
	if h.disableErr != nil {
		return h.disableErr
	}
	return h.System.Disable(ctx, unit)
}

func (h hookSystem) Start(ctx context.Context, unit string) error {
	if h.startErr != nil {
		return h.startErr
	}
	if h.silentStart {
		return nil
	}
	return h.System.Start(ctx, unit)
}

func (h hookSystem) Stop(ctx context.Context, unit string) error {
	if h.stopErr != nil {
		return h.stopErr
	}
	return h.System.Stop(ctx, unit)
}

func (h hookSystem) Restart(ctx context.Context, unit string) error {
	if h.restartErr != nil {
		return h.restartErr
	}
	if h.silentRestart {
		_ = h.System.Stop(ctx, unit)
		return nil
	}
	if h.silentStart {
		return nil
	}
	return h.System.Restart(ctx, unit)
}

func (h hookSystem) StopPortOwner(ctx context.Context, port int, username string) error {
	if h.stopPortErr != nil {
		return h.stopPortErr
	}
	return h.System.StopPortOwner(ctx, port, username)
}

func (h hookSystem) IsActive(ctx context.Context, unit string) (bool, error) {
	if h.isActiveErr != nil {
		return false, h.isActiveErr
	}
	return h.System.IsActive(ctx, unit)
}

func (h hookSystem) Journal(ctx context.Context, unit string, lines int) (string, error) {
	if h.journalErr != nil {
		return "", h.journalErr
	}
	return h.System.Journal(ctx, unit, lines)
}

func (h hookSystem) Metrics(ctx context.Context, unit string) (sysops.Metrics, error) {
	if h.metricsErr != nil {
		return sysops.Metrics{}, h.metricsErr
	}
	return h.System.Metrics(ctx, unit)
}

func (h hookSystem) DiskUsage(ctx context.Context, path string) (int64, error) {
	if h.diskUsageErr != nil {
		return 0, h.diskUsageErr
	}
	return h.System.DiskUsage(ctx, path)
}

func (h hookSystem) ListUsers(ctx context.Context) ([]sysops.UserInfo, error) {
	if h.listUsersErr != nil {
		return nil, h.listUsersErr
	}
	return h.System.ListUsers(ctx)
}

func (h hookSystem) ListUnits(ctx context.Context) ([]string, error) {
	if h.listUnitsErr != nil {
		return nil, h.listUnitsErr
	}
	return h.System.ListUnits(ctx)
}

func (h hookSystem) Listeners(ctx context.Context) ([]sysops.Listener, error) {
	if h.listenersErr != nil {
		return nil, h.listenersErr
	}
	return h.System.Listeners(ctx)
}

func (h hookSystem) NpmVersions(ctx context.Context, pkg, registry string) ([]string, error) {
	if h.npmVersionsErr != nil {
		return nil, h.npmVersionsErr
	}
	return h.System.NpmVersions(ctx, pkg, registry)
}

func (h hookSystem) NpmRegistry(ctx context.Context) (string, error) {
	if h.npmRegistryErr != nil {
		return "", h.npmRegistryErr
	}
	return h.System.NpmRegistry(ctx)
}

func (h hookSystem) NpmInstall(ctx context.Context, prefix, pkg, version, registry string) error {
	if h.npmInstallErr != nil {
		return h.npmInstallErr
	}
	if h.npmInstallNoop {
		return nil
	}
	return h.System.NpmInstall(ctx, prefix, pkg, version, registry)
}

// with attaches the real backend; a table test only names the broken step.
func (h hookSystem) with(sys sysops.System) hookSystem {
	h.System = sys
	return h
}

// productionMode reports systemd mode while keeping the simulated backend, so
// dev-only shortcuts (adoption binary check, upload validation) are exercised.
type productionMode struct {
	sysops.System
}

func (productionMode) Mode() string { return "systemd" }
