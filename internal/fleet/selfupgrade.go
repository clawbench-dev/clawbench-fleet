package fleet

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"

	"clawbench-fleet/internal/sysops"
	"clawbench-fleet/internal/version"
)

// SelfUpgradeResult is what the operator gets back after uploading a new
// build of the platform itself. The response is written before the process
// goes away, so Restarting tells the page to wait for the service to come back
// rather than treat the dropped connection as an error.
type SelfUpgradeResult struct {
	CurrentVersion  string `json:"current_version"`
	UploadedVersion string `json:"uploaded_version"`
	BackupPath      string `json:"backup_path"`
	Restarting      bool   `json:"restarting"`
}

// SelfBinary is the running platform binary, or "" when it cannot be located.
func (s *Service) SelfBinary() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	if resolved, evalErr := filepath.EvalSymlinks(exe); evalErr == nil {
		exe = resolved
	}
	return exe
}

// selfModeDev is the simulated backend's mode string. The local constant keeps
// this file independent of the other mode checks elsewhere in the package.
const selfModeDev = "dev"

// SelfUpgradeInfo describes the platform itself for the upgrade page.
func (s *Service) SelfUpgradeInfo() map[string]any {
	exe := s.SelfBinary()
	return map[string]any{
		fieldVersion: version.Version,
		"binary":     exe,
		// A dev server is not installed under a versioned path, so replacing
		// its binary would swap the file the operator built without anything
		// restarting it. The page uses this to explain why it is disabled.
		"supported": s.Sys.Mode() != selfModeDev,
		"mode":      s.Sys.Mode(),
	}
}

// UploadSelf replaces the platform binary with the uploaded file, keeps a
// copy of the old one and restarts the service so the new build takes over.
//
// Everything that can fail runs before the swap: the upload is written to a
// temporary file, asked for its version, and only then installed. A failure
// therefore leaves the platform exactly as it was, still running and still
// able to serve a retry. Once the restart has been requested there is no way
// to report anything: the reply has to be out by then.
func (s *Service) UploadSelf(ctx context.Context, r io.Reader, maxBytes int64, ac AuditContext) (*SelfUpgradeResult, error) {
	if s.Sys.Mode() == selfModeDev {
		return nil, errors.New("开发模式下不能替换平台自身，请在正式部署的服务器上操作")
	}
	exe := s.SelfBinary()
	if exe == "" {
		return nil, errors.New("找不到当前平台程序的路径，无法升级")
	}
	dir := filepath.Dir(exe)
	// Failing here is a permission problem the operator can fix (the service
	// runs as root, the directory may still be read-only). Checking now beats
	// finding out after the upload.
	if probeErr := os.MkdirAll(s.Cfg.UploadDir(), 0o700); probeErr != nil {
		return nil, fmt.Errorf("上传目录不可写：%w", probeErr)
	}
	if !dirWritable(dir) {
		return nil, fmt.Errorf("程序目录 %s 不可写，无法升级", dir)
	}

	tmpName, err := s.saveSelfUpload(r, maxBytes)
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.Remove(tmpName) }()
	uploaded, err := s.Sys.CheckupVersion(ctx, tmpName)
	if err != nil {
		return nil, fmt.Errorf("这个文件不是可用的平台程序：%w", err)
	}
	if uploaded == version.Version {
		return nil, fmt.Errorf("上传的版本 %s 和当前运行的版本相同", uploaded)
	}

	backup := exe + ".bak"
	if copyErr := copyFile(exe, backup, 0o755); copyErr != nil {
		return nil, fmt.Errorf("备份当前程序失败，未做任何改动：%w", copyErr)
	}
	if replaceErr := s.Sys.ReplaceSelf(tmpName, exe); replaceErr != nil {
		return nil, fmt.Errorf("替换程序失败，仍然运行旧版本：%w", replaceErr)
	}
	log.Printf("平台升级：%s -> %s，备份在 %s", version.Version, uploaded, backup)
	s.Audit(ac, "self.upgrade", nil, map[string]any{
		"from": version.Version, "to": uploaded, "backup_path": backup,
	}, nil)

	result := &SelfUpgradeResult{
		CurrentVersion:  version.Version,
		UploadedVersion: uploaded,
		BackupPath:      backup,
		Restarting:      true,
	}
	if restartErr := s.Sys.RestartSelf(ctx); restartErr != nil {
		// The new binary is on disk but this process is still the old one. Say
		// so with both facts, because a silent success would leave the operator
		// believing the new version is running.
		return result, fmt.Errorf("新版本已经写入，但自动重启失败：%w。请手动执行 systemctl restart %s",
			restartErr, sysops.SelfUnit)
	}
	return result, nil
}

// dirWritable reports whether a temporary file can be created in dir. Testing
// beats inspecting mode bits: read-only mounts, ACLs and the service sandbox
// all matter and none of them show up in os.Stat.
func dirWritable(dir string) bool {
	f, err := os.CreateTemp(dir, ".fleet-probe-*")
	if err != nil {
		return false
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
	return true
}

// saveSelfUpload writes the upload to the upload directory and returns the
// temporary path. It enforces the size limit and leaves the file executable,
// so the very next step can ask it for its version.
func (s *Service) saveSelfUpload(r io.Reader, maxBytes int64) (string, error) {
	tmp, err := os.CreateTemp(s.Cfg.UploadDir(), "fleet-self-*")
	if err != nil {
		return "", err
	}
	tmpName := tmp.Name()
	written, err := io.Copy(tmp, io.LimitReader(r, maxBytes+1))
	if err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return "", err
	}
	if closeErr := tmp.Close(); closeErr != nil {
		_ = os.Remove(tmpName)
		return "", closeErr
	}
	if written > maxBytes {
		_ = os.Remove(tmpName)
		return "", fmt.Errorf("升级包超过 %d MB 上限", maxBytes/1024/1024)
	}
	if written == 0 {
		_ = os.Remove(tmpName)
		return "", errors.New("升级包是空文件")
	}
	if chmodErr := os.Chmod(tmpName, 0o755); chmodErr != nil { //nolint:gosec // the uploaded binary must be executable
		_ = os.Remove(tmpName)
		return "", chmodErr
	}
	return tmpName, nil
}
