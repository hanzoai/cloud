package risk

// policy_wire.go — the DECISION REGIME on the wire, read and written at ONE
// address.
//
// # Why the write lives here and not beside the model
//
// Stating a regime and reading the history of regimes are the same plane's two
// verbs, so they share one address and one answer shape: PUT and GET on
// /v1/risk/policy both answer [riskPolicyOut]. That address used to be two —
// PUT /v1/risk/state/appetite wrote the regime and GET /v1/risk/policy read it —
// and the write answered a fifteen-field report of the whole model: what it had
// learned, its aggregate strain, its blind coordinates, its refusals. A call that
// changes three numbers does not know any of that; it was reporting a model it
// happened to be holding a lock on.
//
// # A restatement needs no flag, because the answer is a VALUE
//
// [plane.enact] is idempotent on the regime: an identical restatement mints no
// version. The write therefore answers the same [riskPolicyOut.Version] it was
// already on, and a caller that wants to know whether anything changed compares
// the value it got with the value it had. There is no `minted` boolean, because a
// flag describing the OPERATION is a second thing to keep true beside the value
// that already says it.
//
// # `state` was the wrong word for it
//
// The old address named the mutable spot the regime happened to sit in. A regime
// is not a spot: it is three numbers an organisation adopted, at a time, by a
// named identity, kept forever after under a version. That is the value, and
// /v1/risk/policy is what it is called.

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

// riskAppetiteIn restates the risk appetite, which is the decision the model is not
// permitted to make for itself.
type riskAppetiteIn struct {
	// Review is the share of the stream that may be sent for examination, in
	// (0, 0.5]. The alert threshold is derived from it as a quantile of the scores
	// actually observed, so the level is governed rather than tuned.
	Review float64 `json:"review"`
	// Sample is the share of below-the-line events retained for review, in
	// [0, 1]. It is the instrument that measures what the model missed; there are
	// no labels, so nothing else can.
	Sample float64 `json:"sample"`
	// Live turns the model out of shadow. It defaults to FALSE on every call, so
	// going live is always an explicit act and never a side effect of changing a
	// number.
	Live bool `json:"live"`
}

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
	// The version in force is read off the RESIDENCY, which is the model that would
	// answer the next score — see [plane.regimeNow] for why not the shelf.
	ver, err := p.regimeNow(t)
	if err != nil {
		return nil, wrap(err)
	}
	out, err := policyOut(p, t, ver)
	if err != nil {
		return nil, err
	}
	pay(1)
	return out, nil
}

// SetPolicy states the decision regime the caller organisation's model decides
// under: how much of its own stream may be sent for examination, how much of the
// rest is sampled to measure what was missed, and whether the model may change an
// outcome at all.
//
// The appetite is the decision a model is not permitted to make for itself: its
// output is a probability, so how likely it is to MISS something is a matter of
// policy that has to be stated, measured and reviewed rather than absorbed into a
// constant. The alert threshold is derived from it as a quantile of the scores
// actually observed, which is what keeps its meaning as the distribution drifts.
//
// It is DURABLE BEFORE IT IS IN FORCE. The regime is recorded as a new version on
// the organisation's own shelf before anything in memory moves, so a policy that
// cannot be written down is refused rather than answered from state the next
// rollout would silently undo.
//
// A RESTATEMENT OF THE REGIME IN FORCE MINTS NOTHING and answers the version
// already in force. Compare the version you receive with the version you had:
// unchanged means the numbers were the same, which is why there is no flag for it.
//
// Learned state survives the change. The model's identity covers its SHAPE — the
// inventory and the geometry — and not its appetite, so restating policy unlearns
// nothing. It also does not REPORT the learned state: what the model is is read
// from the model.
//
// Example: {"review":0.01,"sample":0.001,"live":false}
func (o ops) appetite(ctx context.Context, in *riskAppetiteIn) (*riskPolicyOut, error) {
	// The bounds on `review` and `sample` are NOT restated here. They live in
	// admitRegime, which every path that records a regime goes through — including
	// the one-time adoption of a regime that predates the record — so a rule stated
	// here as well would be a second spelling to disagree with the first.
	pay, err := o.gate(ctx, "appetite", 1)
	if err != nil {
		return nil, err
	}
	p, t, leave, err := o.admit(ctx)
	if err != nil {
		return nil, err
	}
	defer leave()
	ver, err := p.appetite(t, in.Review, in.Sample, in.Live, caller(ctx))
	if err != nil {
		return nil, wrap(err)
	}
	// THE VERSION THIS WRITE LEFT IN FORCE, not a fresh read of it. [plane.appetite]
	// returns it from inside the lock the change was made under; resolving it again
	// here would let two concurrent restatements each report the other's version, an
	// audit surface disagreeing with the record it describes.
	out, err := policyOut(p, t, ver)
	if err != nil {
		return nil, err
	}
	pay(1)
	return out, nil
}

// policyOut renders one organisation's policy history. It is the ONE projection,
// so the read and the write answer the same shape by construction rather than by
// two functions that agree today.
//
// The version in force is a PARAMETER and never re-read here: a write knows the
// version it left in force and a read resolves it from the residency, and those
// are different questions with the same answer only when nothing is concurrent.
func policyOut(p *plane, t tenant, inForce int) (*riskPolicyOut, error) {
	hist, disposed, err := p.history(t, policyVersions)
	if err != nil {
		return nil, wrap(err)
	}
	out := riskPolicyOut{
		Version: inForce, Disposed: disposed,
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
