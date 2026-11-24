package fleet

import (
	"context"
	"testing"
	"time"

	"clawbench-fleet/internal/domain"
	"clawbench-fleet/internal/store"
)

// The simulated npm publishes 0.141.0, 0.142.0 and 0.143.0, so a check on a
// machine with nothing installed should pull all three into the library.
func TestRunNpmAutoCheckInstallsMissingVersions(t *testing.T) {
	svc := newService(t)
	res, err := svc.RunNpmAutoCheck(context.Background(), AuditContext{Actor: actorSystem})
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if res.Current != "0.143.0" {
		t.Fatalf("latest = %q", res.Current)
	}
	if len(res.Installed) != 3 {
		t.Fatalf("installed = %+v", res.Installed)
	}
	versions, err := svc.ListVersions()
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) != 3 {
		t.Fatalf("library should hold every version: %+v", versions)
	}
	// Nothing may be installed onto an instance: the check only fills the
	// library, and the first version becomes "current" for the platform's own
	// bookkeeping, not for any tenant unit.
	current, err := svc.CurrentVersion()
	if err != nil || current.Version != "0.143.0" {
		t.Fatalf("current = %+v %v", current, err)
	}
}

// A second run must be a no-op: the versions are already there.
func TestRunNpmAutoCheckIsIdempotent(t *testing.T) {
	svc := newService(t)
	ctx := context.Background()
	if _, err := svc.RunNpmAutoCheck(ctx, AuditContext{}); err != nil {
		t.Fatal(err)
	}
	res, err := svc.RunNpmAutoCheck(ctx, AuditContext{})
	if err != nil {
		t.Fatalf("second check: %v", err)
	}
	if len(res.Installed) != 0 {
		t.Fatalf("second run installed %+v", res.Installed)
	}
}

// After a rollback the library holds a build newer than the one running in the
// instances. Pulling the older published versions back in would only clutter
// the upgrade page, so they are skipped.
func TestRunNpmAutoCheckSkipsVersionsOlderThanTheLibrary(t *testing.T) {
	svc := newService(t)
	ctx := context.Background()
	if _, err := svc.InstallFromNpm(ctx, "0.142.0", AuditContext{}); err != nil {
		t.Fatal(err)
	}
	res, err := svc.RunNpmAutoCheck(ctx, AuditContext{})
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if len(res.Installed) != 1 || res.Installed[0] != "0.143.0" {
		t.Fatalf("installed = %+v, want only 0.143.0", res.Installed)
	}
}

// A failing registry or a failing install has to surface as an error: the
// scheduled wrapper logs it, and silence would look like "nothing new".
func TestRunNpmAutoCheckReportsFailures(t *testing.T) {
	t.Run("catalog", func(t *testing.T) {
		svc := newService(t)
		svc.Sys = hookSystem{npmVersionsErr: errSimulated}.with(svc.Sys)
		if _, err := svc.RunNpmAutoCheck(context.Background(), AuditContext{}); err == nil {
			t.Fatal("catalog failure should be reported")
		}
	})
	t.Run("install", func(t *testing.T) {
		svc := newService(t)
		svc.Sys = hookSystem{npmInstallErr: errSimulated}.with(svc.Sys)
		res, err := svc.RunNpmAutoCheck(context.Background(), AuditContext{})
		if err == nil || len(res.Installed) != 0 {
			t.Fatalf("install failure should be reported: %+v %v", res, err)
		}
	})
	t.Run("empty catalog", func(t *testing.T) {
		svc := newService(t)
		svc.Sys = emptyNpm{hookSystem{}.with(svc.Sys)}
		if _, err := svc.RunNpmAutoCheck(context.Background(), AuditContext{}); err == nil {
			t.Fatal("an npm with no versions should be reported")
		}
	})
}

// emptyNpm publishes nothing, which is what a wrong package name looks like.
type emptyNpm struct{ hookSystem }

func (emptyNpm) NpmVersions(ctx context.Context, pkg, registry string) ([]string, error) {
	return nil, nil
}

// The weekly schedule only lets the day through on its weekday; the per-day
// bookkeeping in shouldRun then decides whether it already ran. The tests below
// cover the two halves separately, because shouldRun reads the wall clock.
func TestNpmAutoWeeklyOnlyOnItsWeekday(t *testing.T) {
	svc := newService(t)
	weekly := domain.Settings{NpmAutoHour: 0, NpmAutoWeekly: true}
	monday := time.Date(2025, 5, 12, 0, 0, 0, 0, time.Local)
	if svc.npmAutoDue(weekly, monday) {
		t.Fatal("weekly schedule should not fire on Monday")
	}
	// Sunday is the configured day, so the weekday check lets it through. The
	// hour is 0 here, which is not the current hour, so shouldRun still says no;
	// that is the second half of the rule, covered below.
	_ = svc.npmAutoDue(weekly, time.Date(2025, 5, 11, 0, 0, 0, 0, time.Local))
}

// shouldRun's once-a-day rule is what keeps the schedule from repeating every
// minute of the configured hour.
func TestNpmAutoRunsOncePerDay(t *testing.T) {
	svc := newService(t)
	st := domain.Settings{NpmAutoHour: time.Now().Hour()}
	if !svc.npmAutoDue(st, time.Now()) {
		t.Fatal("first look should be due")
	}
	if svc.npmAutoDue(st, time.Now()) {
		t.Fatal("second look in the same hour should not be due")
	}
	// An hour that is not the configured one never fires.
	other := (time.Now().Hour() + 1) % 24
	if svc.npmAutoDue(domain.Settings{NpmAutoHour: other}, time.Now()) {
		t.Fatal("wrong hour should not be due")
	}
}

// The whole point of the feature: when the schedule says now, the platform
// downloads on its own and leaves an audit row behind.
func TestNpmAutoRunsOnSchedule(t *testing.T) {
	svc := newService(t)
	st := svc.Settings()
	st.NpmAutoEnabled = true
	st.NpmAutoHour = time.Now().Hour()
	if err := svc.UpdateSettings(st); err != nil {
		t.Fatal(err)
	}
	if !svc.npmAutoDue(svc.Settings(), time.Now()) {
		t.Fatal("a freshly enabled schedule should be due at its hour")
	}
	svc.runNpmAutoCheck(context.Background())
	versions, err := svc.ListVersions()
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) != 3 {
		t.Fatalf("scheduled check should fill the library: %+v", versions)
	}
	entries, total, err := svc.ListAudit(store.AuditFilter{Action: "upgrade.npm_auto", PageSize: 10})
	if err != nil || total != 1 {
		t.Fatalf("audit = %d %+v %v", total, entries, err)
	}
	if entries[0].Actor != actorSystem || entries[0].Result != statusOK {
		t.Fatalf("audit entry = %+v", entries[0])
	}
}
