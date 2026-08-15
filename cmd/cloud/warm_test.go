package main

// THE PLUGIN CEILING, AS A GATE RATHER THAN AS A PARAGRAPH.
//
// manifest.Warm and doorConfig both carry a careful argument for why the number
// is 36 and why the binding figure is the container's memory REQUEST. Neither
// carries a way to be wrong out loud. Delete the field from doorConfig, set it to
// zero, or raise it past the reservation, and every test in this repo still
// passes — while the host goes back to holding every child a single fleet-wide
// tools/list can start, which is the state that stopped it answering its own
// liveness probe and took the API down.
//
// So the argument is arithmetic here, over constants named for what measured
// them. A change to the ceiling now has to move a number a reader can check.

import (
	"testing"

	"github.com/hanzoai/cloud/manifest"
)

const (
	// Measured in the running pod: 33 children, mean 167MiB, largest 251MiB, and
	// the distribution is FLAT — no subsystem dominates, so the count is the bill.
	perChildMiB = 167
	// What the host held at the moment that was measured. The ceiling has to sit
	// above it or the sweep evicts something about to be asked for again: below
	// the working set is not a saving, it is thrash.
	workingSetChildren = 33
	// The container's memory REQUEST, and it is the request rather than the limit
	// because the kubelet scores eviction candidacy against what a pod reserved.
	// A pod comfortably inside its 11Gi limit is still first in line once it
	// passes 6Gi, which is how this one was evicted for node memory.
	requestMiB = 6 * 1024
)

// TestTheDoorCarriesTheCeiling: the ceiling reaches zip, and it is the manifest's
// number rather than a literal that can drift from it.
//
// zip enforces Warm where a plugin STARTS, so this field is the whole mechanism —
// there is no second place that would catch its absence, and the reaper cannot:
// age reclaims only a plugin quiet for its entire window, and a burst is by
// definition not quiet.
func TestTheDoorCarriesTheCeiling(t *testing.T) {
	got := doorConfig().Warm
	if got == 0 {
		t.Fatal("doorConfig carries no plugin ceiling — one tools/list starts every " +
			"subsystem this host composes and the pod holds all of them")
	}
	if got != manifest.Warm {
		t.Fatalf("doorConfig.Warm = %d, manifest.Warm = %d — two numbers for one budget, "+
			"free to disagree", got, manifest.Warm)
	}
}

// TestTheCeilingFitsTheReservation is the ratchet. Raising Warm is allowed; doing
// it without moving the reservation is not.
//
// It measures CHILDREN ALONE against the request, and reports what is left for
// the host rather than asserting on it, because the host's own resident cost was
// not measured with the same instrument the 167MiB came from. That gap is the
// finding worth leaving visible: at 36 the children fit with 132MiB to spare, and
// manifest.Warm's own note allows the host ~200MiB. One of those two numbers is
// wrong and it is not knowable from inside this process — so the test holds the
// half it can prove and names the half it cannot.
func TestTheCeilingFitsTheReservation(t *testing.T) {
	children := manifest.Warm * perChildMiB
	if children > requestMiB {
		t.Fatalf("Warm=%d x %dMiB = %dMiB, past the %dMiB reservation — the pod becomes "+
			"an eviction candidate under node pressure while still inside its limit. "+
			"Raise the request in universe (charts/app/values/hanzo/cloud.yaml) in the "+
			"same change, or lower the ceiling",
			manifest.Warm, perChildMiB, children, requestMiB)
	}
	if manifest.Warm < workingSetChildren {
		t.Fatalf("Warm=%d is below the observed working set of %d — the sweep would evict "+
			"children about to be asked for again, which costs cold starts and saves no "+
			"memory", manifest.Warm, workingSetChildren)
	}
	t.Logf("Warm=%d: %dMiB of children inside a %dMiB reservation, %dMiB left for the host "+
		"(manifest.Warm's note assumes ~200MiB — that is the tension, not a failure)",
		manifest.Warm, children, requestMiB, requestMiB-children)
}
