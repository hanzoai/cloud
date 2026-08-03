package risk

// policy_wire.go — the policy history on the wire: who may read it, and what one
// version reads as.

import (
	"context"
	"time"

	"github.com/hanzoai/cloud"
)

// caller is the identity a policy change is recorded against.
//
// It comes from the VALIDATED principal on the request and never from a body: an
// attributable record whose attribution the caller chose is not attributable. Off
// the HTTP path there is no request and so no identity, and the honest answer is
// the empty one — [plane.enact] refuses it rather than recording an anonymous
// change.
func caller(ctx context.Context) string {
	c, ok := cloud.Request(ctx)
	if !ok {
		return ""
	}
	return c.User()
}

// riskPolicyIn takes nothing off the wire. The whole input is the caller's
// validated principal, which is what decides whose history is reported.
type riskPolicyIn struct{}

// riskPolicyOut is one organisation's own policy history: every distinct decision
// regime it has adopted, newest first.
type riskPolicyOut struct {
	// Version is the version in force — the one every score currently cites. Zero
	// means no regime has ever been stated and the default posture, shadow, is in
	// force.
	Version int `json:"version"`
	// History is the retained versions, newest first.
	History []riskPolicyVersion `json:"history"`
	// Disposed is how many versions retention has taken. It is NOT a silence: a
	// history bounded on disk must say what it no longer holds, because a decision
	// citing a disposed version can no longer be reconstructed from this record.
	Disposed int `json:"disposed"`
	// Retained is how many versions this organisation's history holds at most,
	// derived from the byte budget its rows are a multiple of.
	Retained int `json:"retained"`
	// Changes is how many DISTINCT regimes may be adopted per Window. A restatement
	// identical to the regime in force mints no version and is not counted against
	// it.
	Changes int `json:"changes"`
	// Window is the period Changes is measured over.
	Window string `json:"window"`
}

// riskPolicyVersion is one regime as it entered force.
type riskPolicyVersion struct {
	// Version names this regime in this organisation's history.
	Version int `json:"version"`
	// Review is the share of the stream the regime states may be examined. The
	// threshold in force is derived from it, which is why a decision is only
	// defensible against the version that produced it.
	Review float64 `json:"review"`
	// Sample is the share of below-the-line events the regime retains for review.
	Sample float64 `json:"sample"`
	// Live is whether the model was permitted to change an outcome under it.
	Live bool `json:"live"`
	// By is the identity that stated it, stamped server-side from the validated
	// principal at the moment it entered force.
	By string `json:"by"`
	// At is when it entered force, RFC 3339, from the server clock.
	At string `json:"at"`
}

// Policy reports the caller organisation's own decision-regime history: every
// distinct regime it has adopted, which version is in force, and what retention
// has taken.
//
// WHY IT EXISTS. Every score cites the version it was decided under
// ([riskScoreOut.Policy]), and the threshold that score was measured against is
// derived from the appetite that version states. Restate the appetite and, without
// this record, every earlier decision becomes unreconstructible — the cut it was
// judged by no longer exists anywhere. An adverse decision that cannot be
// explained against the policy in force when it was taken cannot be defended.
//
// It covers ONE organisation. The history is on that organisation's own shelf, so
// another's versions are not filtered out of the answer — they are not in the file
// the answer is read from.
func (o ops) policy(ctx context.Context, _ *riskPolicyIn) (*riskPolicyOut, error) {
	pay, err := o.gate(ctx, "policy", 1)
	if err != nil {
		return nil, err
	}
	p, t, leave, err := o.admit(ctx)
	if err != nil {
		return nil, err
	}
	defer leave()
	hist, disposed, err := p.history(t, policyVersions)
	if err != nil {
		return nil, wrap(err)
	}
	ver, err := p.regimeNow(t)
	if err != nil {
		return nil, wrap(err)
	}
	pay(1)
	out := riskPolicyOut{
		Version: ver, Disposed: disposed,
		Retained: policyVersions, Changes: maxPolicyPerWindow, Window: policyWindow.String(),
		History: make([]riskPolicyVersion, 0, len(hist)),
	}
	for _, e := range hist {
		out.History = append(out.History, riskPolicyVersion{
			Version: e.Version, Review: e.Regime.Review, Sample: e.Regime.Sample,
			Live: e.Regime.Live, By: e.By, At: e.At.Format(time.RFC3339),
		})
	}
	return &out, nil
}
