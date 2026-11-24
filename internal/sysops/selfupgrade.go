package sysops

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// SelfUnit is the systemd unit the installer writes for the platform itself.
// RestartSelf addresses it by name: `systemctl restart` returns as soon as the
// unit is queued for a stop, and systemd marks the calling process as the one
// being stopped instead of killing it on the spot.
const SelfUnit = "clawbench-fleet.service"

// selfStopDelay keeps RestartSelf from returning before the HTTP reply has
// been flushed. systemd only stops the unit once `systemctl restart` exits, so
// this is a safety margin, not the mechanism.
const selfStopDelay = 500 * time.Millisecond

func (r *Real) CheckupVersion(ctx context.Context, path string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	out, err := run(ctx, path, "--version")
	if err != nil {
		var cmdErr *CommandError
		if errors.As(err, &cmdErr) && strings.TrimSpace(cmdErr.Stderr) == "" {
			return "", fmt.Errorf("这个文件不能在这台服务器上运行：%w", err)
		}
		return "", err
	}
	version := firstLine(strings.TrimSpace(out))
	if version == "" {
		return "", errors.New("程序没有输出版本号")
	}
	return version, nil
}

// ReplaceSelf installs src over dst. The old file is never moved aside first,
// so a failure at any step leaves a runnable binary in place; the service
// manager then has something to start.
func (r *Real) ReplaceSelf(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return os.Chmod(dst, 0o755) //nolint:gosec // the platform binary must be executable
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	// Staging in dst's directory keeps the final rename on one filesystem,
	// which is what makes it atomic when src arrived on another mount.
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".clawbench-fleet-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	// The mode has to be right before the rename: a chmod after it could fail
	// and leave dst replaced by a file nobody can execute.
	if chmodErr := tmp.Chmod(0o755); chmodErr != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return chmodErr
	}
	if _, err := io.Copy(tmp, in); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, dst); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return nil
}

// RestartSelf asks systemd to restart the platform. The caller is expected to
// have finished writing its response: once the restart is accepted the process
// is gone within a moment.
func (r *Real) RestartSelf(_ context.Context) error {
	// The context is deliberately detached from the caller's: cancelling it on
	// return would kill systemctl before it has queued the job.
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	cmd := exec.CommandContext(ctx, "systemctl", "restart", SelfUnit)
	if err := cmd.Start(); err != nil {
		cancel()
		return &CommandError{Name: "systemctl", Args: []string{"restart", SelfUnit}, Err: err}
	}
	// The reply is written by the caller right after this returns, so the exit
	// status cannot be part of the answer. It is checked in the background
	// instead: a refused restart means nothing will replace this process, and
	// ending it would leave the machine with no platform at all.
	go func() {
		defer cancel()
		if waitErr := cmd.Wait(); waitErr != nil {
			log.Printf("systemctl restart %s 失败，平台继续运行旧进程：%v", SelfUnit, waitErr)
			return
		}
		time.Sleep(selfStopDelay)
		_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)
	}()
	return nil
}
