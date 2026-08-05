package risk

// determine_test.go — the rule that does not need the model, held to the one
// property it exists for: a fresh account's first large payment from a listed
// jurisdiction is stopped, and it is stopped WHILE THE MODEL HAS NO OPINION.
//
// That is the whole cut. Every other case here exists to show the rule is not
// simply refusing everything: fifty dollars from an unlisted jurisdiction still
// allows, and shadow still changes nothing at all.

import (
	"strings"
	"testing"

	"github.com/luxfi/aml/pkg/anomaly"

	"github.com/hanzoai/cloud"
	contract "github.com/hanzoai/cloud/plane"
)

// tenMillion is the payment the deliverable names, in the unit the wire carries.
const tenMillion = 10_000_000 * nanoPerUSD

// warming is the assessment the engine returns for an account it has never seen:
// a POPULATED score it has no confidence in, Scored false, and the reason. It is
// the state a fresh account is in at its first payment, and the state in which
// every judgement used to fall through to allow.
func warming(shadow bool) decided {
	return decided{
		A: anomaly.Assessment{
			Scored: false, Reason: anomaly.ReasonWarming,
			// 0.0 exactly: the deliverable's own wording, and the worst case — a
			// fusion that read the score would read this as maximally normal.
			Score: 0.0, Shadow: shadow,
		},
		Version: 0, Shape: "halfspace:fresh",
	}
}

// TestDetermine_AFreshLargePaymentFromAListedJurisdictionIsFrozen is THE test.
//
// Ten million dollars, from a jurisdiction the listing calls for countermeasures
// on, on an account the model has never seen — score 0.0, warming, no opinion.
// The old arrangement answered allow, because the model's refusal WAS the answer.
//
// Mutation proof: make [fuse] read d.A.Scored before applying the determination,
// or take the milder of the two verdicts instead of the severer, and this fails
// with action=allow. Drop `AF` from [defaultJurisdictions] and it fails with
// review — the value alone.
func TestDetermine_AFreshLargePaymentFromAListedJurisdictionIsFrozen(t *testing.T) {
	got := fuse(answer(warming(false)), determine("AF", tenMillion), false)

	if got.Action != cloud.ActionRestrict {
		t.Fatalf("action %q, want %q — a fresh account's first ten-million-dollar top-up from a "+
			"jurisdiction called for countermeasures proceeded because the MODEL had no opinion; "+
			"the rule beside it is what must not need one", got.Action, cloud.ActionRestrict)
	}
	// The cause has to name BOTH halves, because an operator reading this row is
	// being told why a payment was frozen and "geography" alone would not say.
	for _, want := range []string{"jurisdiction", "countermeasures", "value"} {
		if !strings.Contains(got.Cause, want) {
			t.Errorf("cause %q does not name %q — the reason a payment was frozen must state "+
				"which facts froze it", got.Cause, want)
		}
	}
	// The refusal still travels, and still carries no score. The rule decided the
	// outcome; it did not turn a declining model into a confident one.
	if got.Refusal != anomaly.ReasonWarming {
		t.Errorf("refusal %q, want %q — the model's own state is not erased by a rule that "+
			"overruled its silence", got.Refusal, anomaly.ReasonWarming)
	}
	if got.Score != 0 {
		t.Errorf("score %v travelled with a refusal", got.Score)
	}
}

// TestDetermine_TheRuleIsIndependentOfTheModelsWarmth — the same determination,
// against every state the engine can be in, INCLUDING the three refusals. A model
// with no opinion contributes an allow to the fusion and an allow cannot lower
// anything, which is the property stated as a table rather than argued.
//
// Mutation proof: return early from [fuse] when the assessment is unscored and
// every refusal row fails.
func TestDetermine_TheRuleIsIndependentOfTheModelsWarmth(t *testing.T) {
	for _, a := range []anomaly.Assessment{
		{Scored: false, Reason: anomaly.ReasonWarming, Score: 0.0},
		{Scored: false, Reason: anomaly.ReasonUnusable, Score: 0.0},
		{Scored: false, Reason: anomaly.ReasonUnidentified, Score: 0.0},
		{Scored: true, Score: 0.0, Cut: 0.5},              // scored, and maximally normal
		{Scored: true, Score: 0.9, Cut: 0.5, Alert: true}, // scored, and already alerting
	} {
		got := fuse(answer(decided{A: a, Shape: "halfspace:x"}), determine("AF", tenMillion), false)
		if got.Action != cloud.ActionRestrict {
			t.Errorf("model state %+v: action %q, want %q — the determination is not the model's "+
				"to soften", a, got.Action, cloud.ActionRestrict)
		}
	}
}

// TestDetermine_TheRuleItself, over the bounds it is stated in. Each row is one
// branch, and together they are the whole rule.
func TestDetermine_TheRuleItself(t *testing.T) {
	for _, tc := range []struct {
		name    string
		country string
		nano    int64
		action  string
		cause   string
	}{{
		name: "a small payment from an unlisted jurisdiction is the ordinary case",
		// The negative control. A rule that freezes this is a rule nobody can ship.
		country: "US", nano: 50 * nanoPerUSD,
		action: cloud.ActionAllow,
	}, {
		name:    "the countermeasures tier at or above the freeze",
		country: "AF", nano: freezeNano,
		action: cloud.ActionRestrict, cause: causeCountermeasuresValue,
	}, {
		name:    "the countermeasures tier below the freeze is still examined",
		country: "AF", nano: 50 * nanoPerUSD,
		action: cloud.ActionReview, cause: causeCountermeasures,
	}, {
		name: "the monitoring tier is examined at any value, and never frozen",
		// The listing keeps two tiers because the required response differs. A
		// monitored jurisdiction moving ten million is looked at, not frozen.
		country: "HT", nano: tenMillion,
		action: cloud.ActionReview, cause: causeMonitored,
	}, {
		name:    "an unlisted jurisdiction moving a very large value is examined",
		country: "US", nano: tenMillion,
		action: cloud.ActionReview, cause: causeValue,
	}, {
		name:    "just below the examining threshold, nothing fires",
		country: "US", nano: reviewNano - 1,
		action: cloud.ActionAllow,
	}, {
		name: "a jurisdiction the gate could not state leaves the value half to decide",
		// Absent is ABSENT. The geography half does not run and does not invent a
		// verdict; the value half still does.
		country: "", nano: tenMillion,
		action: cloud.ActionReview, cause: causeValue,
	}, {
		name:    "a jurisdiction the gate could not state, at an ordinary value, is the ordinary case",
		country: "", nano: 50 * nanoPerUSD,
		action: cloud.ActionAllow,
	}, {
		name: "the code is read case-insensitively",
		// The edge states upper case and the listing is upper case, but a gate that
		// stated "af" must not silently fall out of the tier.
		country: "af", nano: tenMillion,
		action: cloud.ActionRestrict, cause: causeCountermeasuresValue,
	}} {
		t.Run(tc.name, func(t *testing.T) {
			got := determine(tc.country, tc.nano)
			if got.Action != tc.action {
				t.Errorf("action %q, want %q", got.Action, tc.action)
			}
			if tc.cause != "" && got.Cause != tc.cause {
				t.Errorf("cause %q, want %q", got.Cause, tc.cause)
			}
		})
	}
}

// TestDetermine_ShadowRecordsTheFindingAndChangesNothing.
//
// An organisation that has not armed its model has not armed this rule either.
// The finding is COMPUTED and RECORDED — that is what a shadow deployment is for
// — and the outcome is left exactly where the model left it.
//
// Mutation proof: drop the shadow branch from [fuse] and the action becomes
// restrict for an organisation that reviewed nothing.
func TestDetermine_ShadowRecordsTheFindingAndChangesNothing(t *testing.T) {
	got := fuse(answer(warming(true)), determine("AF", tenMillion), true)

	if got.Action != cloud.ActionAllow {
		t.Fatalf("action %q, want %q — a control nobody reviewed froze real money",
			got.Action, cloud.ActionAllow)
	}
	if !strings.Contains(got.Cause, "in shadow") {
		t.Errorf("cause %q does not say the finding was in shadow — an allow that looks clean "+
			"is exactly what a shadow deployment must not produce", got.Cause)
	}
	if !strings.Contains(got.Cause, "countermeasures") {
		t.Errorf("cause %q does not carry the finding at all — shadow reports what it WOULD "+
			"have done; it does not stay silent", got.Cause)
	}
}

// TestDetermine_TheModelsOwnReasonSurvivesTheFusion. Two judges, two findings,
// one event — and an operator has to see both. The rule's reason leads because it
// is the one that set the action.
func TestDetermine_TheModelsOwnReasonSurvivesTheFusion(t *testing.T) {
	alerting := decided{A: anomaly.Assessment{Scored: true, Score: 0.9, Cut: 0.5, Alert: true}}
	got := fuse(answer(alerting), determine("AF", tenMillion), false)

	if !strings.Contains(got.Cause, causeCountermeasuresValue) {
		t.Errorf("cause %q lost the rule's finding", got.Cause)
	}
	if !strings.Contains(got.Cause, causeAboveCut) {
		t.Errorf("cause %q replaced the model's finding instead of keeping it beside the rule's — "+
			"they are two findings about one event", got.Cause)
	}
	if got.Action != cloud.ActionRestrict {
		t.Errorf("action %q, want the severer of the two (%q)", got.Action, cloud.ActionRestrict)
	}
}

// TestDetermine_ANonFiringRuleLeavesTheModelsAnswerWhole — the rule must be
// invisible when it finds nothing. This is what keeps the ordinary path exactly
// as it was.
func TestDetermine_ANonFiringRuleLeavesTheModelsAnswerWhole(t *testing.T) {
	for _, d := range []decided{
		warming(false),
		{A: anomaly.Assessment{Scored: true, Score: 0.9, Cut: 0.5, Alert: true}},
		{A: anomaly.Assessment{Scored: true, Score: 0.2, Cut: 0.5}},
	} {
		want := answer(d)
		got := fuse(answer(d), determine("US", 50*nanoPerUSD), d.A.Shadow)
		if *got != *want {
			t.Errorf("a rule that found nothing changed the answer:\n got %+v\nwant %+v", *got, *want)
		}
	}
}

// TestDetermine_TheStatedBoundsCannotDisableTheRule.
//
// The thresholds are policy, and a policy that read as zero would not be a
// permissive setting — it would be the rule silently switched off in one
// direction and firing on everything in the other. Both are refused HERE, where a
// number is changed, rather than discovered at a credit door.
func TestDetermine_TheStatedBoundsCannotDisableTheRule(t *testing.T) {
	switch {
	case freezeNano <= 0:
		t.Error("the freeze threshold is not positive, so every payment reaches it and the rule " +
			"freezes the product")
	case reviewNano <= 0:
		t.Error("the examining threshold is not positive, so every payment is examined")
	case freezeNano > reviewNano:
		t.Error("the freeze threshold is above the examining one, so a value that freezes a listed " +
			"jurisdiction would not even be examined from an unlisted one")
	}
	// And the listing itself must be able to answer. An empty or undated one
	// reports every country on earth as unlisted, which is a world with nothing
	// risky in it and nobody would notice.
	if _, err := jurisdictions().Jurisdiction("AF"); err != nil {
		t.Fatalf("the jurisdiction listing cannot assess any country: %v", err)
	}
	if tier, _ := jurisdictions().Jurisdiction("AF"); tier != tierAction {
		t.Errorf("AF is in tier %q, want %q — the deliverable's own jurisdiction", tier, tierAction)
	}
	if tier, _ := jurisdictions().Jurisdiction("US"); tier != "" {
		t.Errorf("US is in tier %q, want no tier — a listing that lists everywhere lists nowhere", tier)
	}
}

// TestDetermine_OverTheWire is the same deliverable through the op a gate
// actually calls, on an ARMED organisation, with a model that has learned
// nothing. It is what proves the signal name, the amount and the posture are
// wired end to end rather than only inside the pure functions above.
//
// Mutation proof: misspell [contract.SignalCountry] at either end and this fails
// with action=allow — the exact silent failure the shared spelling exists to
// prevent.
func TestDetermine_OverTheWire(t *testing.T) {
	probe.reset(true)
	mountApp(t)

	// ARMING ONE TEST TENANT, and nothing else. This states a regime on this
	// test's own temporary data directory; it is not a deployment, and no
	// organisation anywhere is armed by it.
	p := mounted.State.plane
	if _, err := p.appetite(key(t, brandA, orgA), 0.02, 0.10, true /* live */, "u_"+orgA); err != nil {
		t.Fatalf("appetite: %v", err)
	}

	out, err := planeDecide(asPeer(orgA), &contract.RiskDecideIn{
		Stage: cloud.StagePayment, Kind: contract.KindAccount, Subject: "u_fresh",
		Signals: []contract.Signal{
			{Name: contract.SignalNano, Value: "10000000000000000"}, // ten million USD
			{Name: contract.SignalCountry, Value: "AF"},
		},
	})
	if err != nil {
		t.Fatalf("planeDecide: %v", err)
	}
	if out.Refusal == "" {
		t.Fatal("the model answered with an opinion — this test is meaningless unless it is warming")
	}
	if out.Action != cloud.ActionRestrict {
		t.Fatalf("action %q, want %q — over the wire, a fresh account's ten-million-dollar top-up "+
			"from a listed jurisdiction was allowed", out.Action, cloud.ActionRestrict)
	}
	if !strings.Contains(out.Cause, "countermeasures") || !strings.Contains(out.Cause, "value") {
		t.Errorf("cause %q does not name the geography and the value", out.Cause)
	}
}

// TestDetermine_OverTheWireInShadowIsUnchanged — the same call against an
// organisation that armed nothing. This is the state EVERY organisation is in
// today, so it is the state that must be proven harmless.
func TestDetermine_OverTheWireInShadowIsUnchanged(t *testing.T) {
	probe.reset(true)
	mountApp(t)

	out, err := planeDecide(asPeer(orgA), &contract.RiskDecideIn{
		Stage: cloud.StagePayment, Kind: contract.KindAccount, Subject: "u_fresh",
		Signals: []contract.Signal{
			{Name: contract.SignalNano, Value: "10000000000000000"},
			{Name: contract.SignalCountry, Value: "AF"},
		},
	})
	if err != nil {
		t.Fatalf("planeDecide: %v", err)
	}
	if out.Action != cloud.ActionAllow {
		t.Fatalf("action %q, want %q — an organisation that armed nothing had a payment frozen",
			out.Action, cloud.ActionAllow)
	}
	if !strings.Contains(out.Cause, "in shadow") {
		t.Errorf("cause %q does not report the finding it would have acted on", out.Cause)
	}
}

// TestSeverity_OrdersTheVocabularyMostPermissiveFirst. The fusion is only correct
// if this is, and the constants' own comment is the specification.
func TestSeverity_OrdersTheVocabularyMostPermissiveFirst(t *testing.T) {
	order := []string{
		cloud.ActionAllow, cloud.ActionReview, cloud.ActionChallenge,
		cloud.ActionRestrict, cloud.ActionBlock,
	}
	for i := 1; i < len(order); i++ {
		if cloud.Severity(order[i]) <= cloud.Severity(order[i-1]) {
			t.Errorf("%q does not rank above %q", order[i], order[i-1])
		}
	}
	// An action outside the vocabulary must never win a fusion.
	for _, bad := range []string{"", "allowed", "BLOCK", "deny"} {
		if cloud.Severity(bad) >= cloud.Severity(cloud.ActionAllow) {
			t.Errorf("unrecognised action %q ranks at or above allow, so a typo could become "+
				"the outcome", bad)
		}
	}
}
