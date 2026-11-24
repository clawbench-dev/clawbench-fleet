package fleet

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"

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

// SelfConfigPath is where the platform reads its configuration from by
// default. The unit written by AdoptSelf names the file this process was
// actually started with; this constant is only the fallback for callers that
// do not set Service.ConfigPath.
const SelfConfigPath = "/etc/clawbench-fleet/config.yaml"

// configFilePath is the config file the running platform was started with.
func (s *Service) configFilePath() string {
	if s.ConfigPath != "" {
		return s.ConfigPath
	}
	return SelfConfigPath
}

// selfServiceContent is the unit file handed to systemd. It mirrors the one the
// installer ships, with the program's real path filled in: the operator may
// have built it somewhere else, and a unit naming the wrong file would leave
// the machine exactly as stuck as a missing unit does.
func selfServiceContent(binary, configPath string) string {
	return fmt.Sprintf(`[Unit]
Description=Clawbench Fleet - multi-tenant Clawbench instance console
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=root
ExecStart=%s --config %s
Restart=always
RestartSec=3
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
`, binary, configPath)
}

// AdoptSelfResult tells the page what the adoption did and how many processes
// it stopped. The reply is written before the process goes away, exactly like
// a self upgrade.
type AdoptSelfResult struct {
	ServicePath string `json:"service_path"`
	ConfigPath  string `json:"config_path"`
	Stopped     int    `json:"stopped"`
	Restarting  bool   `json:"restarting"`
}

// AdoptSelf hands the running platform to systemd.
//
// This is for the deployment that was started by hand with nohup: without a
// unit, nothing restarts it after an upgrade swaps the binary, and the page
// simply stops answering. Adding the unit is not enough on its own, though —
// the hand-started process is still holding the port — so it is stopped here.
// Only the processes actually listening on our port are touched.
func (s *Service) AdoptSelf(ctx context.Context, ac AuditContext) (*AdoptSelfResult, error) {
	if s.Sys.Mode() == selfModeDev {
		return nil, errors.New("开发模式下没有可托管的服务，请在正式部署的服务器上操作")
	}
	exe := s.SelfBinary()
	if exe == "" {
		return nil, errors.New("找不到当前平台程序的路径，无法托管")
	}
	// The unit starts the platform with this file. Handing the service to
	// systemd when the file is not there would stop the only running copy and
	// leave nothing to start: the page would simply stop answering.
	cfgPath := s.configFilePath()
	if _, statErr := os.Stat(cfgPath); statErr != nil {
		return nil, fmt.Errorf("找不到配置文件 %s，托管后平台会起不来。"+
			"请确认启动时的 --config 指向一个存在的文件再操作：%w", cfgPath, statErr)
	}
	// Nothing below may leave the platform down: the unit is written and enabled
	// first, so ending this process is always followed by systemd starting the
	// same program. The hand-started process holding the port is only stopped
	// once that safety net is in place.
	if writeErr := s.Sys.WriteUnit(ctx, sysops.SelfUnit, selfServiceContent(exe, cfgPath)); writeErr != nil {
		return nil, fmt.Errorf("写入服务定义失败：%w", writeErr)
	}
	if reloadErr := s.Sys.DaemonReload(ctx); reloadErr != nil {
		return nil, fmt.Errorf("服务定义已写入，但 systemd 没有重新加载：%w", reloadErr)
	}
	if enableErr := s.Sys.Enable(ctx, sysops.SelfUnit); enableErr != nil {
		return nil, fmt.Errorf("服务定义已写入，但设置开机自启失败：%w", enableErr)
	}
	stopped, stopErr := s.stopHandStartedSelf(ctx)
	if stopErr != nil {
		return nil, stopErr
	}
	s.Audit(ac, "self.adopt", nil, map[string]any{
		"binary": exe, "config": cfgPath, "stopped": stopped,
	}, nil)
	log.Printf("平台交给 systemd 托管：%s，配置文件 %s，已停掉 %d 个手工启动的进程",
		exe, cfgPath, stopped)

	result := &AdoptSelfResult{
		ServicePath: filepath.Join(s.Cfg.System.SystemdDir, sysops.SelfUnit),
		ConfigPath:  cfgPath,
		Stopped:     stopped,
		Restarting:  true,
	}
	if restartErr := s.Sys.RestartSelf(ctx); restartErr != nil {
		return result, fmt.Errorf("服务已经装好，但自动启动失败：%w。请手动执行 systemctl start %s",
			restartErr, sysops.SelfUnit)
	}
	return result, nil
}

// stopHandStartedSelf ends the processes listening on our own port before
// systemd starts the same address. The process running this very request is
// left alone: RestartSelf ends it a moment later, keeping the reply intact.
func (s *Service) stopHandStartedSelf(ctx context.Context) (int, error) {
	port, err := listenPort(s.Cfg.Listen)
	if err != nil {
		return 0, err
	}
	holders, err := s.Sys.PortHolders(ctx, port, runningUser())
	if err != nil {
		return 0, fmt.Errorf("查不到端口 %d 上是谁在监听：%w", port, err)
	}
	stopped := 0
	for _, h := range holders {
		if h.Self {
			continue
		}
		if killErr := s.Sys.KillProcess(ctx, h.PID); killErr != nil {
			return stopped, fmt.Errorf("停掉占用端口 %d 的旧进程失败：%w", port, killErr)
		}
		stopped++
	}
	return stopped, nil
}

// listenPort pulls the port out of a listen address such as "0.0.0.0:30000".
func listenPort(addr string) (int, error) {
	_, portStr, err := net.SplitHostPort(strings.TrimSpace(addr))
	if err != nil {
		return 0, fmt.Errorf("监听地址 %q 无法解析：%w", addr, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return 0, fmt.Errorf("监听地址 %q 里的端口不是数字：%w", addr, err)
	}
	return port, nil
}

// runningUser is the account the platform runs under. It decides whose
// processes count as "ours" when a port has to be freed.
func runningUser() string {
	if u, err := user.Current(); err == nil {
		return u.Username
	}
	return "root"
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
