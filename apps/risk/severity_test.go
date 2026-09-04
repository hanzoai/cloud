package risk

// severity_test.go — AN ACTION THE VOCABULARY DOES NOT RANK CANNOT BE A FINDING.
//
// [severest] and [fuse] both document that such an action ranks BELOW allow and can
// never become the answer, and both relied on [determination.fired] to hold it —
// which, asked as "not empty and not allow", answered yes for exactly those strings.

import (
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
	contract "github.com/hanzoai/cloud/client"
)

// TestDetermine_AnUnrecognisedActionNeverWins.
//
// [severest] and [fuse] both document that an action outside cloud's vocabulary
// ranks BELOW allow and can never become the answer, and both relied on [fired] to
// hold it. Asked as "not empty and not allow", fired answered YES for such a string —
// so a determination carrying one was treated as a finding, could LEAD the composition
// (it is only ever ranked against other fired determinations) and would then stand.
//
// Nothing in this package mints one today. The guarantee is worth having from the
// predicate rather than from the fact that nobody has broken it yet.
//
// Mutation proof: restore `d.Action != "" && d.Action != cloud.ActionAllow` and the
// unrecognised action leads the composition here.
func TestDetermine_AnUnrecognisedActionNeverWins(t *testing.T) {
	for _, bad := range []string{"deny", "BLOCK", "allowed", "restrict ", "escalate"} {
		if (determination{Action: bad, Cause: "a rule nobody wrote"}).fired() {
			t.Errorf("%q reads as a FINDING, and an action the vocabulary does not rank cannot "+
				"be one", bad)
		}
		// Composed against a real finding, the recognised one stands and the
		// unrecognised one contributes nothing — not even its reason.
		got := severest(
			determination{Action: bad, Cause: "a rule nobody wrote"},
			determination{Action: cloud.ActionReview, Cause: causeValue},
		)
		if got.Action != cloud.ActionReview {
			t.Errorf("severest picked %q over %q", got.Action, cloud.ActionReview)
		}
		if strings.Contains(got.Cause, "nobody wrote") {
			t.Errorf("the unrecognised determination's reason was recorded as a finding: %q", got.Cause)
		}
		// And it cannot raise a verdict through the fusion either.
		out := &contract.RiskDecided{Action: cloud.ActionAllow}
		fuse(out, determination{Action: bad, Cause: "a rule nobody wrote"}, false)
		if out.Action != cloud.ActionAllow {
			t.Errorf("fuse raised an allow to %q on an action outside the vocabulary", out.Action)
		}
	}
}
