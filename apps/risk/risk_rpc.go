package risk

// risk_rpc.go — the scorer, for a gate in ANOTHER PROCESS.
//
// cloud.SetRiskScorer is an in-process handoff and it has never had a producer,
// for a reason the seam states plainly: the model is in-process mutable state, so
// one binary learns and scores, and a scorer installed here would arm this child
// and nothing else. The pod forks one process per app — /cloud, /billing,
// /commerce, /risk are separate pids — so every gate outside this one read nil,
// took the absent exemption, and allowed unscored. The observability plane's
// event door was the same shape and learned it the expensive way; obsevents.go is
// gone and apps/o11y/obs_rpc.go is what replaced it. This is that.
//
// So the model is ASKED, not linked. One op, on this app's own socket, answering
// the one question a gate has: what should I do with this subject, right now.
//
// WHAT IT DOES NOT DO, and both are deliberate:
//
//	IT DOES NOT LEARN. Score is pure — it moves no counter and writes no row —
//	so screening a payment cannot teach the model that the payment was normal.
//	Learning is [ops.learn]'s, from the caller's own events, in its own call.
//
//	IT DOES NOT CHARGE. Every HTTP op on this surface gates on the caller's own
//	balance first ([ops.gate]), and that rule cannot cross to this one: the
//	gate that asks is the CREDIT DOOR, so the balance it would be charged
//	against is empty exactly when the customer is trying to fill it. A screen
//	that refuses a top-up because the account has no money is a control that
//	fires only on the customers it must not fire on. The per-tenant in-flight
//	slot still applies — that is a bound on this process, not a price.

import (
	"context"
	"strconv"
	"time"

	"github.com/hanzoai/cloud"
	contract "github.com/hanzoai/cloud/plane"
	"github.com/zap-proto/zip"
)

// The short reasons a scored verdict carries. Three values, closed, in one place:
// a gate writes them to its own record and an operator reads them there, so a
// sentence that drifted between two call sites would be two facts in the log.
const (
	// causeAboveCut — the event sat above the threshold in force and the model is
	// DECIDING, so it is evidence.
	causeAboveCut = "above the cut"
	// causeShadowCut — the event sat above the threshold and the model is testing
	// rather than deciding, so the outcome is unchanged and the fact is reported.
	// This is the whole value of a shadow deployment: what the model WOULD have
	// done, said out loud, on real traffic, changing nothing.
	causeShadowCut = "above the cut, in shadow"
	// causeWithinAppetite — the event sat at or below the threshold. Strictly
	// below-or-equal is inside the stated appetite (learn.go's cut is the upper
	// edge of the bucket that exhausts the review budget).
	causeWithinAppetite = "within appetite"
)

// exposeDecide publishes the scorer on the internal plane. Mount calls it.
func exposeDecide() {
	zip.Post[contract.RiskDecideIn, contract.RiskDecided](cloud.Plane(), "/risk/decide", planeDecide,
		zip.WithOperationID(contract.RiskDecide),
		zip.WithSummary("Judge one subject against the calling organisation's own model"))
}

// Judges one subject against the CALLING organisation's own model and answers
// what to do about it. It learns nothing, records nothing and moves no counter:
// the numbers it reads are that organisation's history as it stands.
//
// The organisation is the CALLER's, minted from the plane principal and never
// from this body — there is no field here that could name one. A model is trained
// on one organisation's own behaviour, so choosing which model answers would be
// the only cross-tenant read this plane has to offer.
//
// A model still WARMING declines with a reason and no score. That is the whole
// contract of the answer: the engine computes a score before it checks whether it
// has learned enough to have an opinion, so a refusal carries a populated number
// that means nothing, and publishing it would turn "no opinion" into "this is
// fine". Read `refusal` first; `scored` in the HTTP twin of this op says the same
// thing.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeDecide(ctx context.Context, in *contract.RiskDecideIn) (*contract.RiskDecided, error) {
	s := mounted
	if s == nil {
		// The op is registered by Mount, so reaching it with no service is a boot
		// order that changed, never a tenant's problem.
		return nil, zip.Errorf(503, "risk: this process serves the plane without having mounted the model")
	}
	o := ops{s: s}
	p, err := o.plane()
	if err != nil {
		return nil, err
	}
	if !knownStage(in.Stage) {
		return nil, zip.ErrBadRequest("'stage' must be one of " +
			cloud.StageSignup + ", " + cloud.StageUsage + ", " + cloud.StagePayment)
	}
	t, err := planeTenant(ctx, s.Brand)
	if err != nil {
		return nil, err
	}
	obs, err := decideEvent(in).observation(time.Now())
	if err != nil {
		return nil, err
	}
	// The SAME per-tenant in-flight slot every HTTP op takes through [ops.admit] —
	// the plane is a second door onto one model, so it cannot be a door with no
	// bound on it. admit itself is not reused because the ONE thing that differs is
	// the line above it: where the tenant comes from.
	if err := p.enter(t); err != nil {
		return nil, err
	}
	defer p.leave(t)
	d, err := p.score(t, obs)
	if err != nil {
		return nil, wrap(err)
	}
	return answer(d), nil
}

// planeTenant mints the tenant a PLANE call acts for: the org the CALLER stated,
// qualified by this deployment's own brand.
//
// It is [tenantOf]'s sibling and deliberately not tenantOf itself. tenantOf reads
// the principal cloud.Bridge parks on a REQUEST — the right source for the HTTP
// surface, and absent on this one, where there is no request at all. A plane op
// that called it would fail closed on every call. The two mints agree on
// everything that matters: the brand is the deployment's, the org comes from a
// server-resolved identity, and neither can be named in a body.
//
// An empty org is refused by [qualify] — "no org, so the request acts for no
// tenant" — so a peer that states nothing gets no model.
func planeTenant(ctx context.Context, brandID string) (tenant, error) {
	t, err := qualify(brandID, cloud.Who(ctx).Org)
	if err != nil {
		return "", zip.ErrForbidden(err.Error())
	}
	return t, nil
}

// knownStage reports whether the moment is one cloud declares. The set is read
// from the seam rather than restated here, so a fourth stage is added in one
// place.
func knownStage(stage string) bool {
	switch stage {
	case cloud.StageSignup, cloud.StageUsage, cloud.StagePayment:
		return true
	}
	return false
}

// decideEvent projects one plane question onto the model's own event.
//
// It mints NO id, and the observation constructor gives it a random one. An id is
// what a recorded event deduplicates on, and this call records nothing — handing
// it a stable id would suggest a convergence that has nothing to converge.
func decideEvent(in *contract.RiskDecideIn) riskEvent {
	return riskEvent{
		Kind:    in.Kind,
		Subject: in.Subject,
		Nano:    nanoOf(signal(in.Signals, contract.SignalNano)),
		Peer:    signal(in.Signals, contract.SignalPeer),
		Device:  signal(in.Signals, contract.SignalDevice),
		At:      signal(in.Signals, contract.SignalAt),
	}
}

// signal reads one observation out of what the gate stated. A linear read of a
// short list rather than a map, because the four names this scorer knows are
// fixed and a map would allocate one per decision to answer four questions.
func signal(list []contract.Signal, name string) string {
	for _, s := range list {
		if s.Name == name {
			return s.Value
		}
	}
	return ""
}

// nanoOf reads the value moved. A value that is absent OR unreadable is ABSENT:
// the value features then read BLIND rather than being told the amount was zero,
// which is [cloud.Facts]' rule applied to the one signal that is a number.
//
// It does not refuse. A gate that spells an amount wrong has a bug, and the
// bug's blast radius must not be a refused payment — this op answers a
// privileged gate, so an error here is a denial. The cost is visible instead of
// silent: a blind coordinate is counted per dimension and reported on the
// organisation's own model state.
func nanoOf(v string) int64 {
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// answer projects one verdict onto the wire, and it is where the model's
// vocabulary becomes the fleet's. It is [verdict]'s twin — that one answers the
// HTTP surface, this one answers a gate — and both read the same [decided].
//
// TWO RULES, and both are the difference between a control and a rumour.
//
// A REFUSAL CARRIES NO SCORE. The engine assigns the score before it checks
// whether the model has warmed, so a declining model returns a populated number
// that means nothing. Publishing it would let a caller read a refusal as a clean
// result, which is the exact inversion this surface exists to prevent.
//
// AN ALERT IS A REVIEW, NEVER A BLOCK. cloud's own vocabulary says it: "a
// statistical judgement may reach here and no further on its own". This model is
// exactly that — a density estimate over one organisation's own behaviour — so
// the furthest it may take a decision by itself is to summon a person. Block is
// reserved for a determination that is not purely statistical, and nothing here
// makes one. Review still PROCEEDS ([cloud.RiskVerdict.Allowed]), so the customer
// is served and the decision is on the record.
//
// In shadow an above-the-cut event is an ALLOW that says so, because shadow is
// where a model earns the right to be trusted and a shadow that changed outcomes
// would not be one.
func answer(d decided) *contract.RiskDecided {
	a := d.A
	out := &contract.RiskDecided{
		Action:  cloud.ActionAllow,
		Refusal: a.Reason,
		Shape:   d.Shape,
		Policy:  d.Version,
	}
	if !a.Scored {
		return out
	}
	out.Score = a.Score
	switch {
	case a.Alert:
		out.Action, out.Cause = cloud.ActionReview, causeAboveCut
	case a.Score > a.Cut:
		out.Cause = causeShadowCut
	default:
		out.Cause = causeWithinAppetite
	}
	return out
}
