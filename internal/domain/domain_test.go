package domain

import "testing"

func TestProtectedUsername(t *testing.T) {
	if !ProtectedUsername("root") {
		t.Fatal("root must be protected")
	}
	for _, name := range []string{"alice", "root2", ""} {
		if ProtectedUsername(name) {
			t.Fatalf("%q should not be protected", name)
		}
	}
}

func TestTenantAddress(t *testing.T) {
	tenant := Tenant{Port: 21000}
	if got := tenant.Address(""); got != "http://127.0.0.1:21000" {
		t.Fatalf("default host address = %q", got)
	}
	if got := tenant.Address("10.0.0.5"); got != "http://10.0.0.5:21000" {
		t.Fatalf("explicit host address = %q", got)
	}
}

func TestStatusConstantsAreStable(t *testing.T) {
	// These strings are persisted and rendered by the frontend, so a rename is
	// a migration, not a refactor.
	pairs := map[string]string{
		StatusCreating: "creating",
		StatusRunning:  "running",
		StatusStopped:  "stopped",
		StatusError:    "error",
		StatusDeleting: "deleting",
		StatusTrashed:  "trashed",
	}
	for got, want := range pairs {
		if got != want {
			t.Fatalf("status constant changed: %q != %q", got, want)
		}
	}
	jobPairs := map[string]string{
		JobPending: "pending", JobRunning: "running", JobSucceeded: "succeeded",
		JobFailed: "failed", JobPartial: "partial",
	}
	for got, want := range jobPairs {
		if got != want {
			t.Fatalf("job constant changed: %q != %q", got, want)
		}
	}
}
