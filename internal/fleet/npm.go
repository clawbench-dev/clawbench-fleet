package fleet

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"clawbench-fleet/internal/domain"
	"clawbench-fleet/internal/store"
	"clawbench-fleet/internal/sysops"
)

// NpmVersionInfo is one published version plus whether this machine already
// has it.
type NpmVersionInfo struct {
	Version   string `json:"version"`
	Installed bool   `json:"installed"`
}

// NpmCatalog is everything the upgrade page needs to show where versions come
// from: which package, which registry is actually in effect, and the versions.
type NpmCatalog struct {
	Package  string `json:"package"`
	Registry string `json:"registry"`
	// RegistryFrom is "config" when the platform config pins the registry,
	// otherwise "server" (npm's own .npmrc files and default).
	RegistryFrom string           `json:"registry_from"`
	Versions     []NpmVersionInfo `json:"versions"`
	Warning      string           `json:"warning"`
}

func (s *Service) NpmPackage() string { return s.Cfg.Clawbench.NpmPackage }

// normalizeVersion makes "v0.143.0" and "0.143.0" comparable. Clawbench
// builds report the git tag (with the leading v) while npm publishes the bare
// number, and the health endpoint reports whatever the binary was built with.
func normalizeVersion(v string) string {
	return strings.TrimPrefix(strings.TrimSpace(v), "v")
}

// NpmCatalog asks npm for the published versions and marks the ones this
// machine already has. Failures are reported as a message rather than an error
// because the page must keep working when the server has no npm or no network.
func (s *Service) NpmCatalog(ctx context.Context) NpmCatalog {
	out := NpmCatalog{
		Package:  s.Cfg.Clawbench.NpmPackage,
		Versions: []NpmVersionInfo{},
	}
	// Show the registry npm would really use. A configured override wins over
	// the server's own .npmrc, so there is no point asking npm in that case.
	if s.Cfg.Clawbench.NpmRegistry != "" {
		out.Registry = s.Cfg.Clawbench.NpmRegistry
		out.RegistryFrom = "config"
	} else if reg, err := s.Sys.NpmRegistry(ctx); err == nil {
		out.Registry = reg
		out.RegistryFrom = "server"
	} else {
		out.Warning = err.Error()
	}

	raw, err := s.Sys.NpmVersions(ctx, s.Cfg.Clawbench.NpmPackage, s.Cfg.Clawbench.NpmRegistry)
	if err != nil {
		out.Warning = err.Error()
		return out
	}
	installed, _ := s.Store.ListVersions()
	have := make(map[string]bool, len(installed))
	for _, v := range installed {
		have[normalizeVersion(v.Version)] = true
	}
	seen := make(map[string]bool, len(raw))
	for _, v := range raw {
		v = strings.TrimSpace(v)
		if v == "" || seen[v] || !versionRe.MatchString(v) {
			continue
		}
		seen[v] = true
		out.Versions = append(out.Versions, NpmVersionInfo{Version: v, Installed: have[normalizeVersion(v)]})
	}
	// npm returns oldest first; the page wants the newest at the top.
	sort.SliceStable(out.Versions, func(i, j int) bool {
		return compareVersions(out.Versions[i].Version, out.Versions[j].Version) > 0
	})
	return out
}

// npmUpgradeMu serializes downloads. Two npm installs at once would fight over
// the upload directory, and a scheduled pull racing a manual one would make the
// second one fail with a confusing "already installed".
var npmUpgradeMu sync.Mutex

// InstallFromNpm downloads one version into a throwaway directory and copies
// only the executable into the versions directory, so it ends up exactly where
// an uploaded binary would. The old version stays untouched and rollback keeps
// working.
func (s *Service) InstallFromNpm(ctx context.Context, version string, ac AuditContext) (*domain.Version, error) {
	version = strings.TrimSpace(version)
	if !versionRe.MatchString(version) {
		return nil, errors.New("版本号格式不对")
	}
	npmUpgradeMu.Lock()
	defer npmUpgradeMu.Unlock()

	return s.installFromNpmLocked(ctx, version, ac)
}

func (s *Service) installFromNpmLocked(ctx context.Context, version string, ac AuditContext) (*domain.Version, error) {
	pkg := s.Cfg.Clawbench.NpmPackage

	prefix := filepath.Join(s.Cfg.UploadDir(), fmt.Sprintf("npm-%s-%d", version, time.Now().UnixNano()))
	if err := os.MkdirAll(prefix, 0o700); err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(prefix) }()

	if err := s.Sys.NpmInstall(ctx, prefix, pkg, version, s.Cfg.Clawbench.NpmRegistry); err != nil {
		return nil, fmt.Errorf("从 npm 安装 %s@%s 失败：%w", pkg, version, err)
	}
	src, locateErr := locateNpmBinary(prefix, pkg)
	if locateErr != nil {
		return nil, locateErr
	}

	// Copy first, then validate the copy: the temporary npm tree is about to be
	// deleted and the versions directory is what systemd will execute.
	tmp := filepath.Join(s.Cfg.VersionsDir(), fmt.Sprintf(".incoming-%s-%d", version, time.Now().UnixNano()))
	if err := copyFile(src, tmp, 0o755); err != nil {
		return nil, fmt.Errorf("复制 npm 里的程序失败：%w", err)
	}
	label, err := s.validateBinary(ctx, tmp, filepath.Base(src))
	if err != nil {
		_ = os.Remove(tmp)
		return nil, err
	}
	if _, getErr := s.Store.GetVersion(label); getErr == nil {
		_ = os.Remove(tmp)
		return nil, fmt.Errorf("版本 %s 已经安装过了", label)
	}
	destDir := filepath.Join(s.Cfg.VersionsDir(), label)
	if mkdirErr := os.MkdirAll(destDir, 0o755); mkdirErr != nil {
		_ = os.Remove(tmp)
		return nil, mkdirErr
	}
	dest := filepath.Join(destDir, "clawbench")
	if renameErr := os.Rename(tmp, dest); renameErr != nil {
		_ = os.Remove(tmp)
		return nil, renameErr
	}
	sum, err := hashFile(dest)
	if err != nil {
		return nil, err
	}
	info, _ := os.Stat(dest)
	v := &domain.Version{
		Version: label, Path: dest, SHA256: sum, SizeBytes: info.Size(),
		UploadedAt: time.Now().Format(time.RFC3339), Status: "installed",
	}
	if err := s.Store.CreateVersion(v); err != nil {
		return nil, err
	}
	if _, err := s.Store.CurrentVersion(); errors.Is(err, store.ErrNotFound) {
		if err := s.SetCurrentVersion(label); err != nil {
			return nil, err
		}
		v.Status = "current"
	}
	s.Audit(ac, "upgrade.npm_install", nil, map[string]any{
		fieldVersion: label, "npm_package": pkg, "npm_version": version, "sha256": sum,
	}, nil)
	return v, nil
}

// NpmAutoResult is what one scheduled check did, for the audit log and tests.
type NpmAutoResult struct {
	// Installed lists the versions that were downloaded and added to the
	// version library during this run.
	Installed []string
	// Current is the newest published version, whether or not it was new here.
	Current string
}

// publishedVersions asks npm what exists and returns the newest version as
// well as the ones this machine should download.
func (s *Service) publishedVersions(ctx context.Context) (latest string, missing []string, err error) {
	catalog := s.NpmCatalog(ctx)
	if catalog.Warning != "" {
		return "", nil, errors.New(catalog.Warning)
	}
	for _, v := range catalog.Versions {
		if latest == "" || compareVersions(v.Version, latest) > 0 {
			latest = v.Version
		}
	}
	if latest == "" {
		return "", nil, errors.New("npm 上没有找到可用的版本")
	}
	// "Older than what is already here" is the interesting case: after a
	// rollback the library holds a newer build than the one running, and
	// pulling old versions back in would only clutter the page.
	newestLocal := ""
	if versions, listErr := s.Store.ListVersions(); listErr == nil {
		for _, v := range versions {
			if newestLocal == "" || compareVersions(v.Version, newestLocal) > 0 {
				newestLocal = v.Version
			}
		}
	}
	for _, v := range catalog.Versions {
		if v.Installed {
			continue
		}
		if newestLocal != "" && compareVersions(v.Version, newestLocal) < 0 {
			continue
		}
		missing = append(missing, v.Version)
	}
	return latest, missing, nil
}

// RunNpmAutoCheck downloads every published version this machine does not have
// yet, skipping the ones that are older than what it already has. It never
// touches a tenant: the versions land in the version library and wait for an
// operator to pilot or apply them.
//
// Existing instances keep running, so the only cost of installing too much is a
// few megabytes of disk. Downloading everything (rather than only the newest)
// means an operator who skipped a week can still pilot an intermediate version.
func (s *Service) RunNpmAutoCheck(ctx context.Context, ac AuditContext) (NpmAutoResult, error) {
	out := NpmAutoResult{Installed: []string{}}
	latest, missing, err := s.publishedVersions(ctx)
	out.Current = latest
	if err != nil {
		return out, err
	}

	var failures []string
	for _, version := range missing {
		if _, installErr := s.InstallFromNpm(ctx, version, ac); installErr != nil {
			failures = append(failures, fmt.Sprintf("%s：%v", version, installErr))
			continue
		}
		out.Installed = append(out.Installed, version)
	}
	if len(failures) > 0 {
		return out, fmt.Errorf("有 %d 个版本没装成功：%s", len(failures), strings.Join(failures, "；"))
	}
	return out, nil
}

// npmAutoDue reports whether the scheduled check has already run today. The
// weekly mode additionally requires the configured weekday, which keeps the
// check from firing again on every following day of that week.
func (s *Service) npmAutoDue(st domain.Settings, now time.Time) bool {
	if st.NpmAutoWeekly && now.Weekday() != npmWeeklyDay {
		return false
	}
	return s.shouldRun(npmDailyKey, st.NpmAutoHour)
}

// locateNpmBinary finds the executable inside an installed npm tree. The
// expected path is derived from the package name; if a future layout moves it,
// fall back to looking for any scoped package that ships bin/clawbench.
func locateNpmBinary(prefix, pkg string) (string, error) {
	if p, err := sysops.NpmBinaryPath(prefix, pkg); err == nil {
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p, nil
		}
	}
	matches, _ := filepath.Glob(filepath.Join(prefix, "node_modules", "*", "*", "bin", "clawbench"))
	for _, m := range matches {
		if fi, err := os.Stat(m); err == nil && !fi.IsDir() {
			return m, nil
		}
	}
	return "", fmt.Errorf("npm 装完了，但没找到可执行文件（包 %s）", pkg)
}

// compareVersions orders dotted version numbers numerically and falls back to
// plain string comparison for anything unusual.
func compareVersions(a, b string) int {
	as := strings.Split(normalizeVersion(a), ".")
	bs := strings.Split(normalizeVersion(b), ".")
	for i := 0; i < len(as) && i < len(bs); i++ {
		ai, aerr := strconv.Atoi(as[i])
		bi, berr := strconv.Atoi(bs[i])
		if aerr == nil && berr == nil {
			if ai != bi {
				if ai < bi {
					return -1
				}
				return 1
			}
			continue
		}
		if as[i] != bs[i] {
			if as[i] < bs[i] {
				return -1
			}
			return 1
		}
	}
	switch {
	case len(as) < len(bs):
		return -1
	case len(as) > len(bs):
		return 1
	}
	return 0
}
