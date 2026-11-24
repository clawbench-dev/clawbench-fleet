package store

import (
	"testing"

	"clawbench-fleet/internal/domain"
)

// Settings are stored as one JSON blob, and an upgrade adds fields to the
// struct. Saving after the upgrade must keep the new fields, and reading a blob
// written by the previous build must keep the defaults for what it lacks.
func TestNpmScheduleSettingsPersist(t *testing.T) {
	st := openTestStore(t)

	want := domain.Settings{
		PortStart:      21000,
		PortEnd:        21010,
		NpmAutoEnabled: true,
		NpmAutoWeekly:  true,
		NpmAutoHour:    3,
	}
	if err := st.SaveSettings(want); err != nil {
		t.Fatal(err)
	}
	got, err := st.LoadSettings(domain.Settings{})
	if err != nil {
		t.Fatal(err)
	}
	if !got.NpmAutoEnabled || !got.NpmAutoWeekly || got.NpmAutoHour != 3 {
		t.Fatalf("npm schedule = %+v, want the saved values", got)
	}

	// A row written before these fields existed decodes into the defaults the
	// caller passes in, so an old database behaves like a fresh one.
	if _, err := st.db.Exec(`UPDATE settings SET value='{"port_start":21000,"port_end":21010}' WHERE key='platform'`); err != nil {
		t.Fatal(err)
	}
	defaults := domain.Settings{NpmAutoHour: 4}
	got, err = st.LoadSettings(defaults)
	if err != nil {
		t.Fatal(err)
	}
	if got.NpmAutoEnabled || got.NpmAutoWeekly || got.NpmAutoHour != 4 {
		t.Fatalf("old row = %+v, want the passed-in defaults", got)
	}
}
