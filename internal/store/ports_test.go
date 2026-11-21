package store

import (
	"errors"
	"path/filepath"
	"reflect"
	"testing"
)

func TestFindRun(t *testing.T) {
	cases := []struct {
		in    []int
		count int
		want  []int
	}{
		{[]int{1, 2, 3}, 2, []int{1, 2}},
		{[]int{1, 3, 4, 5}, 3, []int{3, 4, 5}},
		{[]int{1, 3, 5}, 2, nil},
		{[]int{9}, 1, []int{9}},
		{nil, 1, nil},
	}
	for _, c := range cases {
		got := findRun(c.in, c.count)
		if !reflect.DeepEqual(got, c.want) {
			t.Fatalf("findRun(%v,%d)=%v want %v", c.in, c.count, got, c.want)
		}
	}
}

func TestAllocatePorts(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "fleet.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if err := st.SeedPorts(24000, 24010); err != nil {
		t.Fatal(err)
	}

	first, err := st.AllocatePorts(24000, 24010, 3, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, []int{24000, 24001, 24002}) {
		t.Fatalf("first group = %v", first)
	}
	second, err := st.AllocatePorts(24000, 24010, 3, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(second, []int{24003, 24004, 24005}) {
		t.Fatalf("second group = %v", second)
	}

	if err := st.ReleasePorts(1); err != nil {
		t.Fatal(err)
	}
	again, err := st.AllocatePorts(24000, 24010, 3, 3)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(again, []int{24000, 24001, 24002}) {
		t.Fatalf("reused group = %v", again)
	}
}

func TestAllocatePortsExhausted(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "fleet.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if err := st.SeedPorts(25000, 25002); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AllocatePorts(25000, 25002, 3, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AllocatePorts(25000, 25002, 1, 2); !errors.Is(err, ErrPortExhausted) {
		t.Fatalf("want ErrPortExhausted, got %v", err)
	}
}

func TestClaimPort(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "fleet.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if err := st.SeedPorts(26000, 26002); err != nil {
		t.Fatal(err)
	}

	// A port outside the configured range is recorded on demand.
	if err := st.ClaimPort(27000, 1); err != nil {
		t.Fatal(err)
	}
	if state, err := st.PortState(27000); err != nil || state != "allocated" {
		t.Fatalf("state = %q err=%v", state, err)
	}
	// Claiming an owned port is idempotent.
	if err := st.ClaimPort(27000, 1); err != nil {
		t.Fatalf("same tenant should be allowed: %v", err)
	}
	// Another tenant must never take it.
	if err := st.ClaimPort(27000, 2); err == nil {
		t.Fatal("port owned by tenant 1 was stolen")
	}
	// Reserved ports (trashed tenants) are protected too.
	if _, err := st.AllocatePorts(26000, 26002, 1, 3); err != nil {
		t.Fatal(err)
	}
	if err := st.ReservePorts(3); err != nil {
		t.Fatal(err)
	}
	if err := st.ClaimPort(26000, 4); err == nil {
		t.Fatal("reserved port was stolen")
	}
}
