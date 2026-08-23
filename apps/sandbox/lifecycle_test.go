// Copyright © 2026 Hanzo AI. MIT License.

package sandbox

import (
	"testing"
	"time"

	"github.com/hanzoai/cloud/plane"
)

// THE TWO PEOPLE THE OLD SINGLE CLOCK GOT WRONG, as a test.
//
// One number could not be right for both, because they are not one question:
// somebody reading for twenty minutes is not idle, and a tab closed twenty
// minutes ago is not busy. What separates them is presence, which this process
// cannot observe for itself — so the clock that decides is chosen by whether a
// watcher has said it is there recently.
func TestTheClockDependsOnWhetherAnyoneIsWatching(t *testing.T) {
	c := clocks{connected: time.Hour, disconnected: 15 * time.Minute, absolute: 8 * time.Hour}
	now := time.Now()
	ago := func(d time.Duration) int64 { return now.Add(-d).Unix() }

	for _, tc := range []struct {
		what string
		m    Sandbox
		over bool
		why  string
	}{
		{
			// The bug this file exists to end: reading is what the tool is FOR.
			"someone reading for 20 minutes",
			Sandbox{CreatedAt: ago(time.Hour), LastUsedAt: ago(20 * time.Minute), ConnectedAt: ago(5 * time.Second)},
			false, "",
		},
		{
			// Same untouched span, nobody there. The pod is the expensive half and
			// the volume survives the reap, so coming back costs a pod, not a
			// checkout.
			"a tab closed 20 minutes ago",
			Sandbox{CreatedAt: ago(time.Hour), LastUsedAt: ago(20 * time.Minute), ConnectedAt: ago(20 * time.Minute)},
			true, "idle-disconnected",
		},
		{
			// A watcher whose stream died: the stamp is a fact with an EXPIRY, so
			// nobody has to turn it off. Three missed beats and it stops counting —
			// which moves this onto the short clock but does NOT reap it, because
			// the short clock is measured from the moment they left. A refresh or a
			// train tunnel is exactly this, and it gets its fifteen minutes.
			"a stream that dropped a moment ago",
			Sandbox{CreatedAt: ago(time.Hour), LastUsedAt: ago(20 * time.Minute), ConnectedAt: ago(4 * plane.AttachEvery)},
			false, "",
		},
		{
			// The same stream, long enough gone that the grace is spent.
			"a stream that dropped and never came back",
			Sandbox{CreatedAt: ago(time.Hour), LastUsedAt: ago(40 * time.Minute), ConnectedAt: ago(20 * time.Minute)},
			true, "idle-disconnected",
		},
		{
			// Watched, but past even the generous clock.
			"watched and untouched for two hours",
			Sandbox{CreatedAt: ago(3 * time.Hour), LastUsedAt: ago(2 * time.Hour), ConnectedAt: ago(5 * time.Second)},
			true, "idle-connected",
		},
		{
			// The ceiling is asked FIRST, so a presence stamp that somehow never
			// goes stale can delay a reap by at most the ceiling and never past it.
			"watched forever, past the ceiling",
			Sandbox{CreatedAt: ago(9 * time.Hour), LastUsedAt: ago(time.Second), ConnectedAt: ago(time.Second)},
			true, "max-life",
		},
		{
			"a lease that ran out",
			Sandbox{CreatedAt: ago(time.Minute), LastUsedAt: ago(time.Second), ConnectedAt: ago(time.Second), ExpiresAt: ago(time.Second)},
			true, "lease-expired",
		},
		{
			// PRESENCE IS NOT ATTENTION WHILE WATCHED. An open tab on a sandbox
			// nobody has run anything in for two hours is the abandonment the
			// generous clock is sized for — an hour is already longer than the
			// longest single command the runtime allows. If a beat refreshed this,
			// `connected` could never run out and the knob for it would be inert.
			"watched, but no command in two hours",
			Sandbox{CreatedAt: ago(3 * time.Hour), LastUsedAt: ago(2 * time.Hour), ConnectedAt: ago(time.Second)},
			true, "idle-connected",
		},
	} {
		over, why := c.over(tc.m, now)
		if over != tc.over || why != tc.why {
			t.Errorf("%s: over=%v %q — want %v %q", tc.what, over, why, tc.over, tc.why)
		}
	}
}

// The ceiling is one fact, so the promise and the sweep read the same number. A
// row that says it lives for a day while the reaper ends it in eight hours is a
// promise the caller plans around and does not get.
func TestALeaseIsNeverSoldPastTheCeiling(t *testing.T) {
	c := clocks{absolute: 8 * time.Hour}
	if got := c.capTTL(86400); got != int(8*time.Hour/time.Second) {
		t.Errorf("a day-long request was sold as %ds, want the ceiling", got)
	}
	if got := c.capTTL(900); got != 900 {
		t.Errorf("a request inside the ceiling was changed to %ds", got)
	}
	// No ceiling configured means no clamp, not a clamp to zero.
	if got := (clocks{}).capTTL(900); got != 900 {
		t.Errorf("with no ceiling the request became %ds", got)
	}
}
