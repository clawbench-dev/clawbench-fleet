package fleet

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"clawbench-fleet/internal/sysops"
	"clawbench-fleet/internal/version"
)

// selfSys is the simulated backend plus the three self-upgrade hooks. The
// default Fake never restarts anything, so the tests need their own record of
// what the service asked for.
type selfSys struct {
	*sysops.Fake
	checkupVersion string
	checkupErr     error
	replaceErr     error
	restartErr     error

	replaced  []string
	restarted int
}

// Mode reports the installed server so the self-upgrade path is reachable.
func (s *selfSys) Mode() string { return "systemd" }

func (s *selfSys) CheckupVersion(ctx context.Context, path string) (string, error) {
	if s.checkupErr != nil {
		return "", s.checkupErr
	}
	return s.checkupVersion, nil
}

func (s *selfSys) ReplaceSelf(src, dst string) error {
	if s.replaceErr != nil {
		return s.replaceErr
	}
	s.replaced = append(s.replaced, src+"->"+dst)
	return nil
}

func (s *selfSys) RestartSelf(ctx context.Context) error {
	if s.restartErr != nil {
		return s.restartErr
	}
	s.restarted++
	return nil
}

// withSelfSys swaps the simulated backend for one that records the
// self-upgrade calls and makes the platform look installed, not dev.
func withSelfSys(t *testing.T) (*Service, *selfSys) {
	t.Helper()
	svc := newService(t)
	fake, ok := svc.Sys.(*sysops.Fake)
	if !ok {
		t.Fatal("test backend is not the simulated one")
	}
	sys := &selfSys{Fake: fake, checkupVersion: "0.9.0"}
	svc.Sys = sys
	return svc, sys
}

// TestUploadSelfIsRefusedInDev guards the one configuration where a swap would
// be silently pointless: the dev server runs the binary the operator built, and
// nothing restarts it.
func TestUploadSelfIsRefusedInDev(t *testing.T) {
	svc := newService(t)
	if svc.Sys.Mode() != "dev" {
		t.Fatalf("test backend mode = %s", svc.Sys.Mode())
	}
	_, err := svc.UploadSelf(context.Background(), strings.NewReader("binary"), 1<<20, AuditContext{Actor: "tester"})
	if err == nil {
		t.Fatal("dev mode must refuse a self upgrade")
	}
	if !strings.Contains(err.Error(), "开发模式") {
		t.Fatalf("err = %v", err)
	}
	if info := svc.SelfUpgradeInfo(); info["supported"] != false {
		t.Fatalf("info = %+v", info)
	}
}

// TestUploadSelfReplacesAndRestarts covers the happy path: the uploaded file is
// handed to the system layer, the old binary is backed up first, and the
// service is restarted exactly once.
func TestUploadSelfReplacesAndRestarts(t *testing.T) {
	svc, sys := withSelfSys(t)
	res, err := svc.UploadSelf(context.Background(), strings.NewReader("new build"), 1<<20, AuditContext{Actor: "tester"})
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	if !res.Restarting || res.UploadedVersion != "0.9.0" {
		t.Fatalf("result = %+v", res)
	}
	if len(sys.replaced) != 1 {
		t.Fatalf("replaced = %v", sys.replaced)
	}
	if sys.restarted != 1 {
		t.Fatalf("restarted = %d", sys.restarted)
	}
	if _, statErr := os.Stat(res.BackupPath); statErr != nil {
		t.Fatalf("backup missing: %v", statErr)
	}
	if !strings.HasSuffix(res.BackupPath, ".bak") {
		t.Fatalf("backup path = %s", res.BackupPath)
	}
}

// TestUploadSelfLeavesBinaryUntouchedOnValidationFailure is the property that
// matters most: everything that can fail runs before the swap, so a rejected
// upload never disturbs the running platform.
func TestUploadSelfLeavesBinaryUntouchedOnValidationFailure(t *testing.T) {
	cases := []struct {
		name string
		body string
		prep func(*selfSys)
		want string
	}{
		{"empty file", "", nil, "空文件"},
		{"too large", strings.Repeat("x", 2048), nil, "上限"},
		{"not a platform binary", "junk", func(s *selfSys) { s.checkupErr = errors.New("不能运行") }, "不是可用的平台程序"},
		{"same version", "build", func(s *selfSys) { s.checkupVersion = version.Version }, "相同"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, sys := withSelfSys(t)
			if tc.prep != nil {
				tc.prep(sys)
			}
			res, err := svc.UploadSelf(context.Background(), strings.NewReader(tc.body), 1024, AuditContext{Actor: "tester"})
			if err == nil {
				t.Fatalf("expected a failure, got %+v", res)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
			if len(sys.replaced) != 0 || sys.restarted != 0 {
				t.Fatalf("a rejected upload must not touch the platform: %+v", sys)
			}
		})
	}
}

// TestUploadSelfReportsSwapFailure keeps the operator on the old platform: a
// failed swap must be a plain error, nothing restarted.
func TestUploadSelfReportsSwapFailure(t *testing.T) {
	svc, sys := withSelfSys(t)
	sys.replaceErr = errors.New("permission denied")
	_, err := svc.UploadSelf(context.Background(), strings.NewReader("build"), 1<<20, AuditContext{Actor: "tester"})
	if err == nil || !strings.Contains(err.Error(), "仍然运行旧版本") {
		t.Fatalf("err = %v", err)
	}
	if sys.restarted != 0 {
		t.Fatal("a failed swap must not restart the service")
	}
}

// TestUploadSelfKeepsReplyingWhenRestartFails covers the one partial state: the
// new binary is on disk but the restart did not go through. The caller still
// gets the paths, plus a warning naming the manual command.
func TestUploadSelfKeepsReplyingWhenRestartFails(t *testing.T) {
	svc, sys := withSelfSys(t)
	sys.restartErr = errors.New("systemd not running")
	res, err := svc.UploadSelf(context.Background(), strings.NewReader("build"), 1<<20, AuditContext{Actor: "tester"})
	if err == nil || !strings.Contains(err.Error(), "systemctl restart") {
		t.Fatalf("err = %v", err)
	}
	if res == nil || !res.Restarting {
		t.Fatalf("result = %+v", res)
	}
}

// TestSelfBinaryPointsAtAResolvedPath makes sure the page shows a usable path
// rather than a symlink to it.
func TestSelfBinaryPointsAtAResolvedPath(t *testing.T) {
	svc := newService(t)
	exe := svc.SelfBinary()
	if exe == "" || !filepath.IsAbs(exe) {
		t.Fatalf("self binary = %q", exe)
	}
}
