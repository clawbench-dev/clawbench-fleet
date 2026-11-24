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

	// holders is what PortHolders reports; pid 1 stands in for another process
	// holding the platform's port, and os.Getpid() for the platform itself.
	holders      []sysops.PortHolder
	portHoldErr  error
	killedPID    []int
	killErr      error
	writeUnitErr error

	replaced  []string
	restarted int

	// order records the steps that matter, so a test can prove the service is
	// installed before anything is stopped.
	order []string
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
	// AdoptSelf refuses to run without the config file the unit will name.
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(cfgPath, []byte("listen: 0.0.0.0:30000\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	svc.ConfigPath = cfgPath
	return svc, sys
}

// PortHolders is answered from the test's script instead of the fake machine:
// what the platform does with a hand-started process is exactly what the tests
// need to control.
func (s *selfSys) PortHolders(ctx context.Context, port int, username string) ([]sysops.PortHolder, error) {
	if s.portHoldErr != nil {
		return nil, s.portHoldErr
	}
	return s.holders, nil
}

func (s *selfSys) KillProcess(ctx context.Context, pid int) error {
	if s.killErr != nil {
		return s.killErr
	}
	s.killedPID = append(s.killedPID, pid)
	s.order = append(s.order, "kill")
	return nil
}

func (s *selfSys) WriteUnit(ctx context.Context, unit, content string) error {
	if s.writeUnitErr != nil {
		return s.writeUnitErr
	}
	s.order = append(s.order, "write")
	return s.Fake.WriteUnit(ctx, unit, content)
}

func (s *selfSys) Enable(ctx context.Context, unit string) error {
	s.order = append(s.order, "enable")
	return s.Fake.Enable(ctx, unit)
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

// TestAdoptSelfInstallsTheServiceBeforeStoppingAnything is the property the
// whole button rests on. The hand-started process is what answers the page, so
// stopping it before the unit exists would leave nothing to start it again.
func TestAdoptSelfInstallsTheServiceBeforeStoppingAnything(t *testing.T) {
	svc, sys := withSelfSys(t)
	sys.holders = []sysops.PortHolder{{PID: 1}}
	res, err := svc.AdoptSelf(context.Background(), AuditContext{Actor: "tester"})
	if err != nil {
		t.Fatalf("adopt: %v", err)
	}
	if !res.Restarting || res.Stopped != 1 {
		t.Fatalf("result = %+v", res)
	}
	want := []string{"write", "enable", "kill"}
	if strings.Join(sys.order, ",") != strings.Join(want, ",") {
		t.Fatalf("order = %v, want %v", sys.order, want)
	}
	if sys.restarted != 1 {
		t.Fatalf("restarted = %d", sys.restarted)
	}
	unit, readErr := sys.ReadUnit(context.Background(), sysops.SelfUnit)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !strings.Contains(unit, svc.SelfBinary()+" --config "+svc.ConfigPath) {
		t.Fatalf("unit does not start the running binary:\n%s", unit)
	}
	if !strings.Contains(unit, "WantedBy=multi-user.target") {
		t.Fatalf("unit is not enabled on boot:\n%s", unit)
	}
}

// TestAdoptSelfLeavesThisProcessAlone guards the reply: the platform marks its
// own PID in the port holder list, and ending it here would drop the response.
func TestAdoptSelfLeavesThisProcessAlone(t *testing.T) {
	svc, sys := withSelfSys(t)
	sys.holders = []sysops.PortHolder{{PID: os.Getpid(), Self: true}}
	res, err := svc.AdoptSelf(context.Background(), AuditContext{Actor: "tester"})
	if err != nil {
		t.Fatalf("adopt: %v", err)
	}
	if res.Stopped != 0 || len(sys.killedPID) != 0 {
		t.Fatalf("killed = %v", sys.killedPID)
	}
}

// TestAdoptSelfChangesNothingWhenTheUnitCannotBeWritten keeps the promise the
// page makes: a failure before the swap leaves the machine as it was.
func TestAdoptSelfChangesNothingWhenTheUnitCannotBeWritten(t *testing.T) {
	svc, sys := withSelfSys(t)
	sys.writeUnitErr = errors.New("read-only file system")
	sys.holders = []sysops.PortHolder{{PID: 1}}
	if _, err := svc.AdoptSelf(context.Background(), AuditContext{Actor: "tester"}); err == nil {
		t.Fatal("a failed write must be reported")
	}
	if len(sys.killedPID) != 0 || sys.restarted != 0 {
		t.Fatalf("killed = %v restarted = %d", sys.killedPID, sys.restarted)
	}
}

// TestAdoptSelfRefusesWhenTheConfigFileIsMissing protects a custom --config
// deployment: handing it to a unit that names a missing file would stop the
// only running copy and start nothing.
func TestAdoptSelfRefusesWhenTheConfigFileIsMissing(t *testing.T) {
	svc, sys := withSelfSys(t)
	svc.ConfigPath = filepath.Join(t.TempDir(), "nope.yaml")
	sys.holders = []sysops.PortHolder{{PID: 1}}
	if _, err := svc.AdoptSelf(context.Background(), AuditContext{Actor: "tester"}); err == nil {
		t.Fatal("a missing config file must be reported")
	}
	if len(sys.order) != 0 || len(sys.killedPID) != 0 || sys.restarted != 0 {
		t.Fatalf("nothing may be touched: %v %v %d", sys.order, sys.killedPID, sys.restarted)
	}
}

// TestAdoptSelfIsRefusedInDev: dev has no unit to hand over to.
func TestAdoptSelfIsRefusedInDev(t *testing.T) {
	svc := newService(t)
	if _, err := svc.AdoptSelf(context.Background(), AuditContext{}); err == nil {
		t.Fatal("dev mode must refuse adoption")
	}
}

// TestListenPortReadsTheConfiguredAddress covers the address shapes an operator
// can put in the config file.
func TestListenPortReadsTheConfiguredAddress(t *testing.T) {
	port, err := listenPort("0.0.0.0:30000")
	if err != nil || port != 30000 {
		t.Fatalf("port = %d err = %v", port, err)
	}
	if _, err := listenPort("30000"); err == nil {
		t.Fatal("an address without a host must be an error")
	}
}
