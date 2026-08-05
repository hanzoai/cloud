package risk

// determine.go — the DETERMINATION: a rule over stated facts, beside a model over
// learned mass.
//
// # Why there is a second judge at all
//
// [learn.go]'s model answers one question — is this where this organisation's
// behaviour normally lives — and it answers it from that organisation's own
// history. That is the right question and it has a blind spot with a name: a
// FRESH account has no history, so the model is warming, so it declines, so the
// event is judged by nothing at all. The credit door
// (apps/commerce/risk.go) is exactly where that blind spot is expensive: a
// settled card charge mints spendable balance, and the first payment on a fresh
// account is the one the model can never have an opinion about.
//
// A ten-million-dollar top-up from a jurisdiction no anti-money-laundering
// supervision reaches is not anomalous — it is unprecedented, which scores as
// nothing, which is the point. It does not need a model. It needs a rule.
//
// # The two judges are INDEPENDENT, and that is the whole property
//
// This rule does not read the score, the cut, the shape or whether the model has
// warmed. It reads two stated facts — where the payer acted from and how much
// moves — and reaches its own verdict. So a warming model, a refusing model and a
// model that has never been planted all leave it untouched: [fuse] takes the
// SEVEREST of the two verdicts, and a model with no opinion contributes an allow,
// which cannot lower anything.
//
// That inverts the previous arrangement, where the model's refusal WAS the
// answer. It is also why the fusion is worst-of rather than a weighted blend: two
// judges of different kinds do not average into a third opinion, and a control
// that a single lenient judge can switch off is not a control.
//
// # It is a RISK tier, and never a sanctions determination
//
// A formal designation is a legal finding about a named party, made by the
// screening engine that holds the designations (luxfi/aml pkg/screen, published
// at /v1/aml). This binary does not link it and does not make one — the AML plane
// is its own deployment reached over the wire, so there is no in-process screen to
// defer to, and a rule that CALLED one would put a network hop on the credit
// door's request path inside a 150ms budget. What this reads instead is the
// jurisdiction LISTING ([policy.go]'s [jurisdictions]), which is a statement about
// a country rather than a finding about a person. Naming the difference is not
// pedantry: it is what keeps a risk control from being read as a legal conclusion
// it did not make.
//
// # It does not name a stage
//
// A jurisdiction is a fact about an event, not about a lifecycle moment, so
// nothing here branches on one. An event that moves no money simply does not
// reach the value half — the geography half is the whole rule for a signup, and
// that is the correct reading rather than a special case.

import (
	"github.com/hanzoai/cloud"
	contract "github.com/hanzoai/cloud/plane"
)

// The tiers [reference.Jurisdictions] answers with. Its package returns them as
// bare strings and publishes no constants, so they are spelled here once rather
// than at each comparison — a misspelling would silently place every country in
// neither tier, which reads as a world with nothing risky in it.
const (
	// tierAction — countermeasures are called for. This tier may freeze.
	tierAction = "action"
	// tierMonitoring — increased monitoring. This tier may examine, no further.
	tierMonitoring = "monitoring"
)

// The reasons a determination carries, in the same terse voice as the model's own
// ([causeAboveCut] and its siblings) and in one place for the same reason: a
// sentence that drifted between two call sites would be two facts in the log.
const (
	// causeCountermeasuresValue — both halves fired: a jurisdiction the listing
	// calls for countermeasures on, moving at least [freezeNano]. This is the only
	// determination that takes an outcome past review.
	causeCountermeasuresValue = "a jurisdiction called for countermeasures, at this value"
	// causeCountermeasures — the geography alone, at any value.
	causeCountermeasures = "a jurisdiction called for countermeasures"
	// causeMonitored — a jurisdiction under increased monitoring, at any value.
	causeMonitored = "a jurisdiction under increased monitoring"
	// causeValue — the value alone, from a jurisdiction carrying no signal.
	causeValue = "a value past the examining threshold"
	// causeUnplaced — a value past the freeze from a jurisdiction that could not
	// be assessed at all. It is deliberately NOT the same as "not listed": an
	// unusable listing must be loud, never silently clean.
	causeUnplaced = "a value past the freeze, from a jurisdiction that could not be placed"
)

// determination is what the rule concluded: an action from cloud's vocabulary and
// the reason that names which half of the rule reached it. The zero value is an
// allow with nothing to say, which is what "the rule found nothing" means.
type determination struct {
	Action string
	Cause  string
}

// fired reports whether the rule reached anything at all. An allow from this rule
// is the absence of a finding, not a clean bill of health — the model's answer is
// what stands in that case.
func (d determination) fired() bool { return d.Action != "" && d.Action != cloud.ActionAllow }

// determine judges one event against the stated bounds: where the payer acted
// from, and how much moves.
//
// The two halves are ordered because they are not symmetric. Geography decides
// FIRST and alone where it can, because a jurisdiction the listing calls for
// countermeasures on is a finding at any value; the value half is what remains
// for the jurisdictions the listing says nothing about. Only the ACTION tier may
// take an outcome past review, which is the listing's own distinction honoured
// rather than restated: the two tiers exist because the required response
// differs, and collapsing them would lose exactly the choice this rule has to
// make.
//
// country is ISO 3166-1 alpha-2, or empty when the asking gate could not state
// one. Empty is ABSENT and not "somewhere unremarkable": the geography half
// simply does not run, the value half still does, and nothing here invents a
// jurisdiction from silence.
func determine(country string, nano int64) determination {
	if d, ok := onGeography(country, nano); ok {
		return d
	}
	if nano >= reviewNano {
		return determination{cloud.ActionReview, causeValue}
	}
	return determination{Action: cloud.ActionAllow}
}

// onGeography is the geography half, and it reports whether it decided at all.
// Separating the halves is what makes each one testable against its own bound
// instead of through the other.
//
// A listing that CANNOT ANSWER is not a listing that answered "no". The reference
// package refuses an empty or undated listing for precisely that reason, and the
// refusal is honoured here rather than swallowed: a large value from a
// jurisdiction nobody could place is examined and says so. Below that value the
// rule declines to decide on geography and leaves the event to the value half —
// escalating every payment on a broken listing would be a control that takes the
// product down instead of defending it.
func onGeography(country string, nano int64) (determination, bool) {
	tier, err := jurisdictions().Jurisdiction(country)
	if err != nil {
		if nano >= freezeNano {
			return determination{cloud.ActionReview, causeUnplaced}, true
		}
		return determination{}, false
	}
	switch {
	case tier == tierAction && nano >= freezeNano:
		// The one path past review. A statistical judgement may not reach here on
		// its own (cloud's vocabulary says so) — but this is not one: it is a
		// stated rule over stated facts, which is the kind of determination the
		// vocabulary reserves the severer actions for.
		//
		// RESTRICT AND NOT BLOCK. Restrict is "proceeds at a reduced ceiling", and
		// at this door the reduced ceiling is zero — the top-up does not settle —
		// while block is reserved for a finding that this party may not transact
		// at all. That finding is the AML plane's to make about a person; this one
		// is about a country and a number, so it freezes the payment and summons a
		// person rather than declaring the payer prohibited.
		return determination{cloud.ActionRestrict, causeCountermeasuresValue}, true
	case tier == tierAction:
		return determination{cloud.ActionReview, causeCountermeasures}, true
	case tier == tierMonitoring:
		return determination{cloud.ActionReview, causeMonitored}, true
	}
	return determination{}, false
}

// fuse composes the two judgements into the one answer a gate receives.
//
// THE SEVEREST STANDS ([cloud.Severity]). The two judges are of different kinds —
// one reads learned mass, the other reads stated facts — so there is no average
// of them that means anything, and taking the milder would let either one switch
// the other off. A model with no opinion contributes an allow, which is why a
// warming model cannot lower a determination.
//
// THE RULE'S REASON IS RECORDED WHENEVER IT FIRED, even when the model's verdict
// was already the severer one and even in shadow. The two are separate findings
// about one event and an operator reading the record has to be able to see both,
// so the model's own reason is kept BESIDE the rule's rather than replaced by it.
//
// SHADOW CHANGES THE OUTCOME BACK, AND ONLY THE OUTCOME. An organisation whose
// model is in shadow has not armed anything, and a determination that froze a
// payment there would be a control nobody reviewed acting on real money. So the
// action is left exactly as the model left it and the cause says what WOULD have
// happened — which is the same arrangement [answer] already makes for an
// above-the-cut score in shadow ([causeShadowCut]), for the same reason. It is
// the whole value of a shadow deployment: the finding, on real traffic, changing
// nothing.
func fuse(out *contract.RiskDecided, det determination, shadow bool) *contract.RiskDecided {
	if !det.fired() {
		return out
	}
	if out.Cause != "" {
		out.Cause = det.Cause + "; " + out.Cause
	} else {
		out.Cause = det.Cause
	}
	if shadow {
		out.Cause += ", in shadow"
		return out
	}
	if cloud.Severity(det.Action) > cloud.Severity(out.Action) {
		out.Action = det.Action
	}
	return out
}
