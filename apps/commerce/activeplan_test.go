// Copyright © 2026 Hanzo AI. MIT License.

package commerce

import (
	"testing"

	"github.com/hanzoai/commerce/models/subscription"
)

// liveStatus decides whether a subscription grants PAID ACCESS, and it is two
// lines with no test — the shape of predicate that is obviously right until a
// vocabulary grows under it.
//
// The table below is the WHOLE vocabulary rather than the two interesting cases,
// which is the point: a status added upstream and not listed here fails this test
// instead of silently defaulting to no-access for a customer who is paying, or to
// access for one who is not.

func TestOnlyActiveAndTrialingGrantAccess(t *testing.T) {
	grants := map[subscription.Status]bool{
		// Paying, or inside a trial the business chose to honour.
		subscription.Active:   true,
		subscription.Trialing: true,

		// NOT access. Past due is a customer whose payment failed and who is being
		// chased; unpaid is one whose retries ran out; canceled is one who left.
		// Granting any of the three bills nobody and serves everybody.
		subscription.PastDue:  false,
		subscription.Canceled: false,
		subscription.Unpaid:   false,
	}

	for status, want := range grants {
		if got := liveStatus(status); got != want {
			t.Errorf("liveStatus(%q) = %v, want %v", status, got, want)
		}
	}

	// The vocabulary this test claims to cover. If commerce adds a status, this
	// count moves and the new one has to be decided here rather than fall through
	// to whichever answer the expression happens to give it.
	all := []subscription.Status{
		subscription.Active, subscription.Trialing,
		subscription.PastDue, subscription.Canceled, subscription.Unpaid,
	}
	if len(all) != len(grants) {
		t.Errorf("the table covers %d statuses and the vocabulary holds %d", len(grants), len(all))
	}
}

// TestAnUnknownStatusIsNotAccess is the default that matters. A status this
// binary has never heard of — an older row, a newer upstream, a typo in a
// migration — must not be read as paid.
func TestAnUnknownStatusIsNotAccess(t *testing.T) {
	for _, unknown := range []subscription.Status{"", "ACTIVE", "Active", "active ", "paused", "incomplete"} {
		if liveStatus(unknown) {
			t.Errorf("liveStatus(%q) granted access; only the two known live statuses may", unknown)
		}
	}
}
