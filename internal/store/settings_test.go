package store

import (
	"testing"
	"time"

	"clawbench-fleet/internal/domain"
)

// TestTenantConfigRoundTrip covers the settings row that holds the unified
// config. It is stored separately from Settings because it is a list.
func TestTenantConfigRoundTrip(t *testing.T) {
	st := openTestStore(t)

	// A fresh database has no config yet and must not error.
	got, err := st.LoadTenantConfig()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("empty database returned %+v, want no entries", got)
	}

	entries := []domain.ConfigEntry{
		{Key: "chat.page_size", Value: "25"},
		{Key: "chat.retention_days", Value: "30"},
	}
	if err := st.SaveTenantConfig(entries); err != nil {
		t.Fatal(err)
	}
	got, err = st.LoadTenantConfig()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(entries) {
		t.Fatalf("loaded %+v, want %d entries", got, len(entries))
	}
	for i, want := range entries {
		if got[i] != want {
			t.Fatalf("entry %d = %+v, want %+v", i, got[i], want)
		}
	}

	// Saving again replaces the previous list rather than appending to it.
	if err := st.SaveTenantConfig([]domain.ConfigEntry{{Key: "chat.page_size", Value: "50"}}); err != nil {
		t.Fatal(err)
	}
	got, err = st.LoadTenantConfig()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Value != "50" {
		t.Fatalf("after overwrite = %+v, want one entry with value 50", got)
	}

	// nil is stored as an empty list, so reading back is consistent.
	if err := st.SaveTenantConfig(nil); err != nil {
		t.Fatal(err)
	}
	if got, err = st.LoadTenantConfig(); err != nil || len(got) != 0 {
		t.Fatalf("after saving nil = %+v %v, want no entries", got, err)
	}
}

// TestLoadTenantConfigToleratesBrokenJSON: a bad row must not stop the platform
// from starting, so it degrades to "no unified config".
func TestLoadTenantConfigToleratesBrokenJSON(t *testing.T) {
	st := openTestStore(t)
	if _, err := st.db.Exec(`INSERT INTO settings(key, value) VALUES('tenant_config', ?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value`, "{not json"); err != nil {
		t.Fatal(err)
	}
	got, err := st.LoadTenantConfig()
	if err != nil {
		t.Fatalf("broken row should not error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("broken row returned %+v, want no entries", got)
	}
}

// TestPurgeMetricSamples removes history older than the cutoff.
func TestPurgeMetricSamples(t *testing.T) {
	st := openTestStore(t)
	tenant := newTenant("purge", 21000, domain.StatusRunning)
	if err := st.CreateTenant(tenant); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for _, at := range []time.Time{now.Add(-48 * time.Hour), now.Add(-time.Hour)} {
		if err := st.AddMetricSample(tenant.ID, domain.MetricSample{
			At: at.Format(time.RFC3339), CPUPercent: 1,
		}); err != nil {
			t.Fatal(err)
		}
	}
	deleted, err := st.PurgeMetricSamples(now.Add(-24 * time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 1 {
		t.Fatalf("purged %d rows, want 1", deleted)
	}
	left, err := st.MetricHistory(tenant.ID, now.Add(-72*time.Hour), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 1 {
		t.Fatalf("history after purge = %+v, want the recent sample only", left)
	}
}

// TestPurgeMetricSamplesForTenant clears one tenant and leaves the others alone.
func TestPurgeMetricSamplesForTenant(t *testing.T) {
	st := openTestStore(t)
	alice := newTenant("purgealice", 21000, domain.StatusRunning)
	bob := newTenant("purgebob", 21001, domain.StatusRunning)
	for _, tenant := range []*domain.Tenant{alice, bob} {
		if err := st.CreateTenant(tenant); err != nil {
			t.Fatal(err)
		}
		if err := st.AddMetricSample(tenant.ID, domain.MetricSample{
			At: time.Now().Format(time.RFC3339), CPUPercent: 1,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.PurgeMetricSamplesForTenant(alice.ID); err != nil {
		t.Fatal(err)
	}
	since := time.Now().Add(-time.Hour)
	if got, err := st.MetricHistory(alice.ID, since, 10); err != nil || len(got) != 0 {
		t.Fatalf("alice history = %+v %v, want empty", got, err)
	}
	if got, err := st.MetricHistory(bob.ID, since, 10); err != nil || len(got) != 1 {
		t.Fatalf("bob history = %+v %v, want his sample kept", got, err)
	}
	// Purging a tenant that has no history is not an error.
	if err := st.PurgeMetricSamplesForTenant(999999); err != nil {
		t.Fatalf("purging an unknown tenant: %v", err)
	}
}

// TestOpenRejectsUnwritablePath checks the startup error path is reported
// rather than a half-open store.
func TestOpenRejectsUnwritablePath(t *testing.T) {
	if _, err := Open("/proc/definitely-not-writable/fleet.db"); err == nil {
		t.Fatal("opening a database in a read-only location should fail")
	}
}

func TestSaveSettingsPersistsAcrossRead(t *testing.T) {
	st := openTestStore(t)
	stg := domain.Settings{PortStart: 21000, PortEnd: 21010, PublicHost: "fleet.example.com"}
	if err := st.SaveSettings(stg); err != nil {
		t.Fatal(err)
	}
	got, err := st.LoadSettings(domain.Settings{})
	if err != nil {
		t.Fatal(err)
	}
	if got.PublicHost != stg.PublicHost || got.PortStart != stg.PortStart {
		t.Fatalf("settings = %+v, want %+v", got, stg)
	}
	// A second save updates the same row instead of inserting another.
	stg.PublicHost = "other.example.com"
	if err := st.SaveSettings(stg); err != nil {
		t.Fatal(err)
	}
	var rows int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM settings WHERE key='platform'`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("settings rows = %d, want 1", rows)
	}
	if got, _ = st.LoadSettings(domain.Settings{}); got.PublicHost != "other.example.com" {
		t.Fatalf("settings after update = %+v", got)
	}
}
