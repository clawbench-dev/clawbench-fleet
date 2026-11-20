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

func (s *Service) NpmPackage() string { return s.Cfg.Clawbench.NpmPackage }

// normalizeVersion makes "v0.143.0" and "0.143.0" comparable. Clawbench
// builds report the git tag (with the leading v) while npm publishes the bare
// number, and the health endpoint reports whatever the binary was built with.
func normalizeVersion(v string) string {
	return strings.TrimPrefix(strings.TrimSpace(v), "v")
}

// ListNpmVersions asks npm for the published versions and marks the ones this
// machine already has. A failure is returned as a message rather than an error
// because the page must keep working when the server has no npm or no network.
func (s *Service) ListNpmVersions(ctx context.Context) ([]NpmVersionInfo, string) {
	raw, err := s.Sys.NpmVersions(ctx, s.Cfg.Clawbench.NpmPackage, s.Cfg.Clawbench.NpmRegistry)
	if err != nil {
		return nil, err.Error()
	}
	installed, _ := s.Store.ListVersions()
	have := make(map[string]bool, len(installed))
	for _, v := range installed {
		have[normalizeVersion(v.Version)] = true
	}
	out := make([]NpmVersionInfo, 0, len(raw))
	seen := make(map[string]bool, len(raw))
	for _, v := range raw {
		v = strings.TrimSpace(v)
		if v == "" || seen[v] || !versionRe.MatchString(v) {
			continue
		}
		seen[v] = true
		out = append(out, NpmVersionInfo{Version: v, Installed: have[normalizeVersion(v)]})
	}
	// npm returns oldest first; the page wants the newest at the top.
	sort.SliceStable(out, func(i, j int) bool { return compareVersions(out[i].Version, out[j].Version) > 0 })
	return out, ""
}

// InstallFromNpm downloads one version into a throwaway directory and copies
// only the executable into the versions directory, so it ends up exactly where
// an uploaded binary would. The old version stays untouched and rollback keeps
// working.
func (s *Service) InstallFromNpm(ctx context.Context, version string, ac AuditContext) (*domain.Version, error) {
	version = strings.TrimSpace(version)
	if !versionRe.MatchString(version) {
		return nil, errors.New("版本号格式不对")
	}
	pkg := s.Cfg.Clawbench.NpmPackage

	prefix := filepath.Join(s.Cfg.UploadDir(), fmt.Sprintf("npm-%s-%d", version, time.Now().UnixNano()))
	if err := os.MkdirAll(prefix, 0o700); err != nil {
		return nil, err
	}
	defer os.RemoveAll(prefix)

	if err := s.Sys.NpmInstall(ctx, prefix, pkg, version, s.Cfg.Clawbench.NpmRegistry); err != nil {
		return nil, fmt.Errorf("从 npm 安装 %s@%s 失败：%w", pkg, version, err)
	}
	src, err := locateNpmBinary(prefix, pkg)
	if err != nil {
		return nil, err
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
	if _, err := s.Store.GetVersion(label); err == nil {
		_ = os.Remove(tmp)
		return nil, fmt.Errorf("版本 %s 已经安装过了", label)
	}
	destDir := filepath.Join(s.Cfg.VersionsDir(), label)
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		_ = os.Remove(tmp)
		return nil, err
	}
	dest := filepath.Join(destDir, "clawbench")
	if err := os.Rename(tmp, dest); err != nil {
		_ = os.Remove(tmp)
		return nil, err
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
		"version": label, "npm_package": pkg, "npm_version": version, "sha256": sum,
	}, nil)
	return v, nil
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
