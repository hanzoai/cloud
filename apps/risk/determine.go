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
// event is judged by nothing at all. The credit endpoint
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
// endpoint's request path inside a 150ms budget. What this reads instead is the
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
//
// # Three halves, two kinds of fact
//
// [onEvent] decides on the EVENT's own stated facts: where the payer acted from
// and how much moves. [onPace] and [onFan] decide on the organisation's OWN
// AGGREGATES — what those identifiers had already done when this event arrived —
// which is a fact about history that no single event can carry. They are the two
// halves a point-in-time test structurally cannot make:
//
//	PACE     several payments, each unremarkable, arriving faster or accruing
//	         further than one payment is allowed to. Splitting a payment to stay
//	         under a threshold is the typology the value half alone can never see,
//	         because every piece of it is legal by construction.
//	FAN-OUT  one device or one counterparty tying together subjects that are
//	         nominally unrelated. Account farming looks like ordinary behaviour
//	         from every account taken by itself, and like one actor from above.
//
// All three read stated bounds and none of them reads the model, so [severest]
// composes them the same way [fuse] composes the result with the model's: the
// severest stands, every finding is reported, and a judge with no opinion cannot
// lower another's.

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

// The reasons the AGGREGATE halves carry. Each names the finding and is completed
// with the identifier it was found on, which comes from a closed set (ring.go) and
// never from a caller — an operator reading a frozen payment has to be told which
// of the three the bound was reached on, because the answer selects the
// investigation.
const (
	// causeBurstValue — both halves of the pace rule fired: more events than the
	// stated bound inside the burst window, accruing at least [freezeNano]. This
	// is the only aggregate determination that takes an outcome past review.
	causeBurstValue = "a burst of events accruing past the freeze, for this "
	// causeAccrued — the accrued value alone, past the examining threshold. It is
	// the value half applied to a WINDOW rather than to one event, which is what
	// makes a payment split into pieces visible at all.
	causeAccrued = "a value accrued past the examining threshold, for this "
	// causeBurst — the count alone, at any value.
	causeBurst = "more events in the burst window than the stated bound, for this "
	// causeShared — one identifier tying together more distinct subjects than the
	// stated bound.
	causeShared = "more distinct subjects than the stated bound share this "
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
//
// IT IS THE SEVERITY THAT ANSWERS, not a comparison against two spellings, because
// the vocabulary is a ranking and this question is about rank: has this rule
// reached something STRICTER than proceeding. Asked as "not empty and not allow" it
// answered yes for a string outside the vocabulary — which [severest] then let LEAD
// (it is only ever compared against other fired determinations) and [fuse] then let
// stand, contradicting the one thing both of them document: that an unrecognised
// action ranks below allow ([cloud.Severity] returns -1) and can never become the
// answer. Nothing in this package mints one today; the guarantee is worth having
// from the predicate rather than from the fact that nobody has broken it yet.
func (d determination) fired() bool {
	return cloud.Severity(d.Action) > cloud.Severity(cloud.ActionAllow)
}

// determine is the DETERMINATION: the severest of what this event's own stated
// facts say about it and what this organisation's own aggregates already held
// about its identifiers.
//
// seen is [plane.prior]'s reading. Its ZERO VALUE is "the aggregates said
// nothing", which leaves the two aggregate halves silent and the event's own
// facts the whole rule — the correct reading for a subject with no history, and
// the one that keeps the geography determination exactly what it was.
func determine(country string, nano int64, seen reading) determination {
	return severest(onEvent(country, nano), onPace(seen), onFan(seen))
}

// onEvent judges one event against the stated bounds: where the payer acted
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
func onEvent(country string, nano int64) determination {
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
		// at this endpoint the reduced ceiling is zero — the top-up does not settle —
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

// onPace is the VELOCITY half: what one of this event's identifiers had already
// done inside the aggregates' burst window.
//
// WHY THE VALUE HALF IS NOT ENOUGH ON ITS OWN. [onEvent] bounds what ONE payment
// may move, so the way past it is arithmetic available to anyone: send five
// payments of eleven thousand instead of one of fifty-five. Every piece is under
// the bound by construction, so no point-in-time test can see it — which is
// precisely the typology the aggregates exist for (velocity's own package prose
// names it, and the EBA names it as "split to circumvent reporting limits").
//
// SO THE VALUE BOUNDS ARE THE SAME NUMBERS, READ OVER A WINDOW. [reviewNano] and
// [freezeNano] are not restated here with different values: what one payment may
// not move, an hour of payments may not move either. One statement of the
// organisation's value appetite, two readings of it — a second pair of numbers
// would be a second appetite nobody stated.
//
// THE COUNT NEEDS ITS OWN BOUND because there is no existing one: a thousand
// one-dollar top-ups is a finding about the count and about nothing else.
// [burstEvents] is it.
//
// THE FREEZE TIER IS A CONJUNCTION, exactly as the geography half's is. Geography
// may freeze only where the ACTION tier meets the freeze value; pace may freeze
// only where a burst meets the freeze value. A count alone never freezes — a busy
// hour is not a determination that anything is wrong — and an accrual alone never
// does either, because [reviewNano] is a reason to look and says so.
//
// It reads EVERY axis the event names and takes the severest, because an
// identifier is not more or less suspicious for being the subject rather than the
// device: the bound is about one identifier doing too much, whichever one it is.
func onPace(seen reading) determination {
	out := determination{}
	for _, w := range seen.Pace {
		switch {
		case w.Events >= burstEvents && w.Nano >= freezeNano:
			// The one aggregate path past review, and it is a STATED rule over stated
			// facts rather than a statistical judgement — which is the kind of
			// determination cloud's vocabulary reserves the severer actions for.
			//
			// RESTRICT AND NOT BLOCK, for [onGeography]'s reason: the reduced ceiling at
			// this endpoint is zero, and a finding that a party may not transact at all is
			// the AML plane's to make about a person. This is about a count and a sum.
			out = severest(out, determination{cloud.ActionRestrict, causeBurstValue + w.Axis})
		case w.Nano >= reviewNano:
			out = severest(out, determination{cloud.ActionReview, causeAccrued + w.Axis})
		case w.Events >= burstEvents:
			out = severest(out, determination{cloud.ActionReview, causeBurst + w.Axis})
		}
	}
	return out
}

// onFan is the FAN-OUT half: how many distinct subjects one of this event's LINK
// identifiers — its device, its counterparty — already ties together.
//
// WHAT IT SEES THAT NOTHING ELSE DOES. Account farming is unremarkable from every
// account taken by itself: each one signs up once, tops up once, behaves once.
// The only place the pattern exists is in what the accounts SHARE, and a rule
// that reads one subject's history at a time is looking at the wrong subject. The
// engine's own feature inventory says the same of this axis — "activity across a
// network of connected persons rather than one customer" — and the fan-out is
// that network stated as a number.
//
// REVIEW AND NO FURTHER, at any count. A shared device is evidence about a
// relationship and not about a payment: a household, a shared office and a farm
// are the same shape from here, and only a person can tell them apart. Review
// PROCEEDS ([cloud.RiskVerdict.Allowed]) — the customer is served and the finding
// is on the record — which is what makes it the right and the only ceiling for a
// signal this ambiguous. That is also why the count bound is generous rather than
// tight, and why the counterparty is read at the same bound as the device: a
// popular merchant and a collection account are indistinguishable from a count
// alone, so the only defensible response to either is a look.
func onFan(seen reading) determination {
	out := determination{}
	for _, s := range seen.Shared {
		if s.Subjects >= fanSubjects {
			out = severest(out, determination{cloud.ActionReview, causeShared + s.Axis})
		}
	}
	return out
}

// severest composes several half-rules into the one determination they reach
// together.
//
// THE SEVEREST STANDS, for the reason [fuse] takes the severest of the rule and
// the model: these halves read different facts, so there is no average of them
// that means anything, and taking the milder would let a half that found nothing
// switch off a half that found something. It is the same composition one level
// down, which is why it is the same word.
//
// EVERY FINDING IS REPORTED and the one that SET the action leads, again for
// [fuse]'s reason: they are separate findings about one event and an operator
// reading the record has to see all of them. A half that did not fire contributes
// no reason, because "nothing found" is not a finding.
//
// WITH NOTHING FIRED the mildest recognised action still stands, so an allow from
// a half that RAN is not lost behind the empty determination of a half that had
// no reading to run on. An unrecognised action ranks below allow
// ([cloud.Severity]), so it can never become the answer.
func severest(ds ...determination) determination {
	lead := -1
	for i, d := range ds {
		if d.fired() && (lead < 0 || cloud.Severity(d.Action) > cloud.Severity(ds[lead].Action)) {
			lead = i
		}
	}
	if lead < 0 {
		out := determination{}
		for _, d := range ds {
			if cloud.Severity(d.Action) > cloud.Severity(out.Action) {
				out = d
			}
		}
		return out
	}
	out := ds[lead]
	for i, d := range ds {
		if i != lead && d.fired() {
			out.Cause += "; " + d.Cause
		}
	}
	return out
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
