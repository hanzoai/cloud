package sandbox

import (
	"testing"

	"github.com/hanzoai/cloud/boundary"
)

// TestTheTableIsTheClosedSet pins the scheduler's table against the shared
// leaf. The two hold DIFFERENT facts about one set — this package knows whether
// a boundary has a kernel and can back a volume, the installer knows which
// handler runs it — so neither can be derived from the other, and only the NAMES
// are shared. A boundary added to one side and not the other is exactly the
// drift the leaf exists to prevent: a handler nobody schedules, or a class the
// machine cannot run, neither of which has a symptom where it happens.
func TestTheTableIsTheClosedSet(t *testing.T) {
	got := Boundaries()
	if len(got) != len(boundary.Names) {
		t.Fatalf("table has %d boundaries, boundary.Names has %d: %v vs %v",
			len(got), len(boundary.Names), got, boundary.Names)
	}
	for i, name := range boundary.Names {
		if got[i] != name {
			t.Errorf("boundary %d: table says %q, boundary.Names says %q", i, got[i], name)
		}
	}
}
