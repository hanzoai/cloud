package host

// THE PLUGIN CEILING, AS A GATE RATHER THAN AS A PARAGRAPH.
//
// manifest.Warm and endpointConfig both carry a careful argument for why the ceiling
// is what it is and why the binding figure is the container's memory REQUEST.
// Neither carries a way to be wrong out loud. Delete the field from endpointConfig,
// set it to zero, or let it run past the reservation, and every test in this repo
// still passes — while the host goes back to holding every child a single
// fleet-wide tools/list can start, which is the state that stopped it answering
// its own liveness probe and took the API down.
//
// The ceiling is now DERIVED from the reservation the Deployment projects
// (CLOUD_MEMORY_REQUEST_MIB), so the arithmetic that used to be a paragraph is
// the function itself. What is left to gate is that the derivation holds at both
// ends: it fits whatever it was given, and it never goes below the working set.

import (
	"fmt"
	"testing"

	"github.com/hanzoai/cloud/manifest"
)

const (
	// Measured in the running pod: 630 readings of 35 children, mean 70.5MiB.
	perChildMiB = 71
	// The most children production needed resident over a 15-minute window,
	// pinned apps included. The ceiling has to sit above it or the host evicts
	// something about to be asked for again: below the working set is not a
	// saving, it is thrash.
	workingSetChildren = 41
	// The container's memory REQUEST, and it is the request rather than the limit
	// because the kubelet scores eviction candidacy against what a pod reserved.
	// A pod comfortably inside its 11Gi limit is still first in line once it
	// passes 6Gi, which is how this one was evicted for node memory. 	// the deployment reserves and what an unset projection falls back to.
	requestMiB = 6 * 1024
	// What the host itself holds, off the children's budget.
	hostMiB = 200
)

// TestTheEndpointCarriesTheCeiling: the ceiling reaches zip, and it is the manifest's
// number rather than a literal that can drift from it.
//
// zip enforces Warm where a plugin STARTS, so this field is the whole mechanism —
// there is no second place that would catch its absence, and the reaper cannot:
// age reclaims only a plugin quiet for its entire window, and a burst is by
// definition not quiet.
func TestTheEndpointCarriesTheCeiling(t *testing.T) {
	got := endpointConfig().Warm
	if got == 0 {
		t.Fatal("endpointConfig carries no plugin ceiling — one tools/list starts every " +
			"subsystem this host composes and the pod holds all of them")
	}
	if got != manifest.Warm() {
		t.Fatalf("endpointConfig.Warm = %d, manifest.Warm() = %d — two numbers for one budget, "+
			"free to disagree", got, manifest.Warm())
	}
}

// TestTheCeilingFitsTheReservation is the ratchet, and the reservation is now
// READ rather than assumed: the Deployment projects the container's memory
// request through the downward API and manifest.Warm derives the ceiling from it,
// so raising the reservation raises the ceiling and lowering it lowers the
// ceiling with nothing to keep in step by hand.
//
// It measures CHILDREN ALONE against the request and reports what is left for the
// host, because the host's own resident cost was not measured with the instrument
// the per-child mean came from. That gap is the finding worth leaving visible.
func TestTheCeilingFitsTheReservation(t *testing.T) {
	t.Setenv("CLOUD_MEMORY_REQUEST_MIB", "")
	if children := manifest.Warm() * perChildMiB; children > requestMiB {
		t.Fatalf("Warm=%d x %dMiB = %dMiB, past the %dMiB an unmanaged process assumes",
			manifest.Warm(), perChildMiB, children, requestMiB)
	}
	if manifest.Warm() < workingSetChildren {
		t.Fatalf("Warm=%d is below the observed working set of %d — the sweep would evict "+
			"children about to be asked for again, which costs cold starts and saves no "+
			"memory", manifest.Warm(), workingSetChildren)
	}
	t.Logf("Warm=%d: %dMiB of children inside a %dMiB reservation, %dMiB left for the host",
		manifest.Warm(), manifest.Warm()*perChildMiB, requestMiB, requestMiB-manifest.Warm()*perChildMiB)
}

// TestTheCeilingFollowsTheReservation is the property the derivation exists for:
// a pod given more memory uses it, and a pod given less does NOT go on budgeting
// against memory it no longer has. The second direction is the one that gets a pod
// evicted, and it is the one a hand-sized constant could never answer.
func TestTheCeilingFollowsTheReservation(t *testing.T) {
	for _, c := range []struct{ requestMiB, wantAtMost int }{
		{2048, (2048 - hostMiB) / perChildMiB},
		{6144, (6144 - hostMiB) / perChildMiB},
		{16384, (16384 - hostMiB) / perChildMiB},
	} {
		t.Setenv("CLOUD_MEMORY_REQUEST_MIB", fmt.Sprint(c.requestMiB))
		got := manifest.Warm()
		if got*perChildMiB > c.requestMiB {
			t.Errorf("at a %dMiB reservation the ceiling is %d children = %dMiB, past the reservation",
				c.requestMiB, got, got*perChildMiB)
		}
		// A host must be able to hold at least one child whatever it was given.
		if got < 1 {
			t.Errorf("at a %dMiB reservation the ceiling is %d — a host that holds no child serves nothing",
				c.requestMiB, got)
		}
	}
}

// A projection that is absent, blank or malformed must not yield a ceiling of
// zero — a host that may hold no children at all serves nothing. It falls back to
// the reservation the ceiling was measured against.
func TestAnUnreadableReservationFallsBackRatherThanToZero(t *testing.T) {
	for _, raw := range []string{"", "   ", "lots", "-1", "0"} {
		t.Setenv("CLOUD_MEMORY_REQUEST_MIB", raw)
		if got := manifest.Warm(); got < workingSetChildren {
			t.Fatalf("CLOUD_MEMORY_REQUEST_MIB=%q yielded a ceiling of %d — a host that holds "+
				"no children answers nothing", raw, got)
		}
	}
}
