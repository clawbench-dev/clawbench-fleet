package fleet

import (
	"context"
	"testing"
	"time"

	"clawbench-fleet/internal/domain"
	"clawbench-fleet/internal/store"
)

// The scheduler inside dailyLoop is three independent decisions: is the
// feature on, is it the configured hour, and has it not run today yet. The
// tests above cover the first two pieces one at a time; this one drives the
// same body dailyLoop runs, so a change to the order they are applied in shows
// up here rather than in production.
func TestNpmAutoScheduleDecidesOncePerDay(t *testing.T) {
	svc := newService(t)
	ctx := context.Background()
	now := time.Now()

	st := svc.Settings()
	st.NpmAutoEnabled = true
	st.NpmAutoHour = now.Hour()
	if err := svc.UpdateSettings(st); err != nil {
		t.Fatal(err)
	}

	// What dailyLoop does on every tick.
	consider := func() bool {
		st := svc.Settings()
		return st.NpmAutoEnabled && svc.npmAutoDue(st, now)
	}

	if !consider() {
		t.Fatal("enabled, right hour, not run yet: the tick should start the check")
	}
	if consider() {
		t.Fatal("the same hour must not start a second check")
	}

	// Turning the feature off keeps it quiet even at the right hour.
	st = svc.Settings()
	st.NpmAutoEnabled = false
	if err := svc.UpdateSettings(st); err != nil {
		t.Fatal(err)
	}
	if consider() {
		t.Fatal("a disabled schedule must never start a check")
	}

	// A scheduled run leaves exactly the audit row the page shows.
	svc.runNpmAutoCheck(ctx)
	entries, total, err := svc.ListAudit(store.AuditFilter{Action: "upgrade.npm_auto", PageSize: 10})
	if err != nil || total != 1 {
		t.Fatalf("audit = %d %+v %v", total, entries, err)
	}
	if entries[0].Result != statusOK {
		t.Fatalf("result = %q, want ok", entries[0].Result)
	}
}

// Weekly mode is a weekday filter in front of the daily bookkeeping. Monday is
// not the configured day, so neither the first nor a repeated look may fire.
func TestNpmAutoWeeklyStaysQuietOffItsDay(t *testing.T) {
	svc := newService(t)
	weekly := domain.Settings{NpmAutoHour: time.Now().Hour(), NpmAutoWeekly: true}
	monday := time.Date(2025, 5, 12, time.Now().Hour(), 0, 0, 0, time.Local)
	if monday.Weekday() != time.Monday {
		t.Fatalf("fixture date is %s, want Monday", monday.Weekday())
	}
	if svc.npmAutoDue(weekly, monday) {
		t.Fatal("weekly schedule must not fire on Monday")
	}
	if svc.npmAutoDue(weekly, monday) {
		t.Fatal("weekly schedule must stay quiet on a repeat look")
	}
	// Daily mode on the same day is allowed to fire exactly once.
	daily := domain.Settings{NpmAutoHour: time.Now().Hour()}
	if !svc.npmAutoDue(daily, monday) {
		t.Fatal("daily schedule should fire at its hour")
	}
	if svc.npmAutoDue(daily, monday) {
		t.Fatal("daily schedule should fire only once")
	}
}

// A check that finds nothing new (every published version is already here) is
// not worth an audit row: the operator would get one line a day saying nothing
// happened. A failed check is worth a line.
func TestRunNpmAutoCheckSilentWhenNothingIsNew(t *testing.T) {
	svc := newService(t)
	ctx := context.Background()

	svc.runNpmAutoCheck(ctx) // first run fills the library and writes one row
	if _, total, _ := svc.ListAudit(store.AuditFilter{Action: "upgrade.npm_auto", PageSize: 10}); total != 1 {
		t.Fatalf("first run should leave one audit row, got %d", total)
	}

	svc.runNpmAutoCheck(ctx) // nothing new this time
	entries, total, err := svc.ListAudit(store.AuditFilter{Action: "upgrade.npm_auto", PageSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 {
		t.Fatalf("a no-op check must not add audit rows: %d %+v", total, entries)
	}
}

// A broken registry has to leave a failed audit row behind; silence would look
// exactly like "nothing new today".
func TestRunNpmAutoCheckRecordsFailure(t *testing.T) {
	svc := newService(t)
	svc.Sys = hookSystem{npmRegistryErr: errSimulated}.with(svc.Sys)

	svc.runNpmAutoCheck(context.Background())

	entries, total, err := svc.ListAudit(store.AuditFilter{Action: "upgrade.npm_auto", PageSize: 10})
	if err != nil || total != 1 {
		t.Fatalf("audit = %d %+v %v", total, entries, err)
	}
	if entries[0].Result != statusFailed {
		t.Fatalf("result = %q, want failed", entries[0].Result)
	}
}
