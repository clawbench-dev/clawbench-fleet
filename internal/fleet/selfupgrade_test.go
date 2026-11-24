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
	if info := svc.SelfUpgradeInfo(context.Background()); info["supported"] != false {
		t.Fatalf("info = %+v", info)
	}
}

// TestUploadSelfReplacesAndRestarts covers the happy path: the uploaded file is
// handed to the system layer, the old binary is backed up first, and the
// service is restarted exactly once.
func TestUploadSelfReplacesAndRestarts(t *testing.T) {
	svc, sys := withSelfSys(t)
	if err := svc.Sys.WriteUnit(context.Background(), sysops.SelfUnit, "[Service]\n"); err != nil {
		t.Fatal(err)
	}
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
			if err := svc.Sys.WriteUnit(context.Background(), sysops.SelfUnit, "[Service]\n"); err != nil {
				t.Fatal(err)
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
	if err := svc.Sys.WriteUnit(context.Background(), sysops.SelfUnit, "[Service]\n"); err != nil {
		t.Fatal(err)
	}
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
	if err := svc.Sys.WriteUnit(context.Background(), sysops.SelfUnit, "[Service]\n"); err != nil {
		t.Fatal(err)
	}
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

// TestAdoptSelfAllowsAMissingConfigFile: config.Load falls back to the built-in
// defaults when the file is absent, so the unit still starts and adoption is
// safe. Refusing here would block the common by-hand deployment that never
// created /etc/clawbench-fleet/config.yaml.
func TestAdoptSelfAllowsAMissingConfigFile(t *testing.T) {
	svc, sys := withSelfSys(t)
	svc.ConfigPath = filepath.Join(t.TempDir(), "nope.yaml")
	sys.holders = []sysops.PortHolder{{PID: 1}}
	res, err := svc.AdoptSelf(context.Background(), AuditContext{Actor: "tester"})
	if err != nil {
		t.Fatalf("a missing config file must not block adoption: %v", err)
	}
	if res.ConfigPath != svc.ConfigPath {
		t.Fatalf("config path = %q, want %q", res.ConfigPath, svc.ConfigPath)
	}
	if sys.restarted != 1 {
		t.Fatalf("adoption must finish: order=%v restarted=%d", sys.order, sys.restarted)
	}
}

// TestAdoptSelfRefusesWhenTheConfigFileIsUnreadable: only "not there" is known
// to be harmless. Any other stat failure leaves us unable to tell, and stopping
// the only running copy on a guess would take the page down.
func TestAdoptSelfRefusesWhenTheConfigFileIsUnreadable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can stat through a chmod 000 directory")
	}
	svc, sys := withSelfSys(t)
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	svc.ConfigPath = filepath.Join(dir, "config.yaml")
	sys.holders = []sysops.PortHolder{{PID: 1}}
	if _, err := svc.AdoptSelf(context.Background(), AuditContext{Actor: "tester"}); err == nil {
		t.Fatal("an unreadable config path must be reported")
	}
	if len(sys.order) != 0 || len(sys.killedPID) != 0 || sys.restarted != 0 {
		t.Fatalf("nothing may be touched: %v %v %d", sys.order, sys.killedPID, sys.restarted)
	}
}

// TestUploadSelfRefusesBeforeAdoption protects the deployment started by hand:
// the swap happens before the restart is requested, so upgrading first would
// leave the new binary on disk with no unit to start it and nothing that brings
// the platform back once the hand-started process exits.
func TestUploadSelfRefusesBeforeAdoption(t *testing.T) {
	svc, sys := withSelfSys(t)
	_, err := svc.UploadSelf(context.Background(), strings.NewReader("new build"), 1<<20, AuditContext{Actor: "tester"})
	if err == nil {
		t.Fatal("an un-adopted platform must refuse the self upgrade")
	}
	if !strings.Contains(err.Error(), "托管") {
		t.Fatalf("err = %v", err)
	}
	if len(sys.replaced) != 0 || sys.restarted != 0 {
		t.Fatalf("nothing may be touched: replaced=%v restarted=%d", sys.replaced, sys.restarted)
	}
}

// TestUploadSelfIsAllowedAfterAdoption: once the unit is on disk, the restart
// has a target and the upgrade is safe.
func TestUploadSelfIsAllowedAfterAdoption(t *testing.T) {
	svc, sys := withSelfSys(t)
	if err := svc.Sys.WriteUnit(context.Background(), sysops.SelfUnit, "[Service]\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UploadSelf(context.Background(), strings.NewReader("new build"), 1<<20, AuditContext{Actor: "tester"}); err != nil {
		t.Fatalf("upload after adoption: %v", err)
	}
	if len(sys.replaced) != 1 || sys.restarted != 1 {
		t.Fatalf("replaced=%v restarted=%d", sys.replaced, sys.restarted)
	}
}

// TestSelfUpgradeInfoReportsAdoption: the adopt button hides once the unit
// exists, and it is the unit file — not anything the process remembers — that
// says so, because a restart wipes everything the process kept in memory.
func TestSelfUpgradeInfoReportsAdoption(t *testing.T) {
	svc, _ := withSelfSys(t)
	if info := svc.SelfUpgradeInfo(context.Background()); info["adopted"] != false {
		t.Fatalf("before adoption: %+v", info)
	}
	svc.ConfigPath = filepath.Join(t.TempDir(), "config.yaml")
	if err := svc.Sys.WriteUnit(context.Background(), sysops.SelfUnit, "[Service]\n"); err != nil {
		t.Fatal(err)
	}
	if info := svc.SelfUpgradeInfo(context.Background()); info["adopted"] != true {
		t.Fatalf("after adoption: %+v", info)
	}
}

// TestSelfUpgradeInfoTreatsAnEmptyUnitAsNotAdopted: adoption is a lasting fact
// only because the unit file survives a restart. A unit that exists but is
// empty would not start anything, so the page must keep offering the button —
// pressing it only rewrites the same file.
func TestSelfUpgradeInfoTreatsAnEmptyUnitAsNotAdopted(t *testing.T) {
	svc, _ := withSelfSys(t)
	if err := svc.Sys.WriteUnit(context.Background(), sysops.SelfUnit, "   \n"); err != nil {
		t.Fatal(err)
	}
	if info := svc.SelfUpgradeInfo(context.Background()); info["adopted"] != false {
		t.Fatalf("an empty unit is not adoption: %+v", info)
	}
	// The same blank unit must not unlock the upgrade either.
	if _, err := svc.UploadSelf(context.Background(), strings.NewReader("new build"), 1<<20, AuditContext{Actor: "tester"}); err == nil {
		t.Fatal("an empty unit must not count as a restart target")
	}
}

// TestSelfUpgradeInfoTreatsAnUnreadableUnitAsNotAdopted covers the other half
// of the same rule: if the unit cannot be read, the platform cannot promise a
// restart, and offering the upgrade would be a guess.
func TestSelfUpgradeInfoTreatsAnUnreadableUnitAsNotAdopted(t *testing.T) {
	svc, sys := withSelfSys(t)
	// A unit that is a non-empty directory at the path the backend reads makes
	// the read fail without needing root, which is what an unreadable file looks
	// like from here. The fake also caches units in memory, so a fresh instance
	// pointed at that directory is what actually exercises the error path.
	bad := t.TempDir()
	if err := os.MkdirAll(filepath.Join(bad, "units", sysops.SelfUnit, "x"), 0o700); err != nil {
		t.Fatal(err)
	}
	svc.Sys = sysops.NewFake(bad, sys.InstallDir)
	if info := svc.SelfUpgradeInfo(context.Background()); info["adopted"] != false {
		t.Fatalf("an unreadable unit is not adoption: %+v", info)
	}
}

// TestSelfUpgradeInfoNeverClaimsAdoptionInDev keeps the status line out of the
// dev server, which has no unit to point at.
func TestSelfUpgradeInfoNeverClaimsAdoptionInDev(t *testing.T) {
	svc := newService(t)
	if info := svc.SelfUpgradeInfo(context.Background()); info["adopted"] != false {
		t.Fatalf("dev: %+v", info)
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
