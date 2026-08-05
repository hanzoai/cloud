package risk

// determine_test.go — the rule that does not need the model, held to the one
// property it exists for: a fresh account's first large payment from a listed
// jurisdiction is stopped, and it is stopped WHILE THE MODEL HAS NO OPINION.
//
// That is the whole cut. Every other case here exists to show the rule is not
// simply refusing everything: fifty dollars from an unlisted jurisdiction still
// allows, and shadow still changes nothing at all.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/luxfi/aml/pkg/anomaly"
	"github.com/luxfi/aml/pkg/velocity"

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
	got := fuse(answer(warming(false)), determine("AF", tenMillion, reading{}), false)

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
		got := fuse(answer(decided{A: a, Shape: "halfspace:x"}), determine("AF", tenMillion, reading{}), false)
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
			got := determine(tc.country, tc.nano, reading{})
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
	got := fuse(answer(warming(true)), determine("AF", tenMillion, reading{}), true)

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
	got := fuse(answer(alerting), determine("AF", tenMillion, reading{}), false)

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
		got := fuse(answer(d), determine("US", 50*nanoPerUSD, reading{}), d.A.Shadow)
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

	arm(t, mounted.State.plane, key(t, brandA, orgA))

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

// TestActions_TheScorerNeverBlocks — this app's whole vocabulary tops out at
// RESTRICT, and that is load-bearing OUTSIDE this app.
//
// Two rules meet here and both already say it in prose. [answer]: "an alert is a
// review, never a block — a statistical judgement may reach here and no further on
// its own". [onGeography]: "restrict and not block — block is reserved for a
// finding that this party may not transact at all", which is the AML plane's to
// make about a person and not this one's about a country and a number.
//
// WHAT DEPENDS ON IT. The credit door tells a DETERMINATION apart from a
// no-decision by the pair (action == block AND a refusal), because
// [cloud.riskUnavailable] is then the only thing that can have produced it. Emit
// block from here — with a warming model's refusal still riding along, which the
// fuse deliberately preserves — and that gate reads a working freeze as a scorer
// outage and answers 503 "try again in a moment": an invitation to retry the
// payment it just froze.
//
// It is STRUCTURAL rather than a sample of decisions, because the claim is "no
// path", and no finite set of scored events can establish that. Comments are
// invisible to it — the prose above names block freely; only a resolved reference
// counts.
func TestActions_TheScorerNeverBlocks(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse the package: %v", err)
	}
	var found int
	for _, pkg := range pkgs {
		for name, file := range pkg.Files {
			ast.Inspect(file, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				id, ok := sel.X.(*ast.Ident)
				if !ok || id.Name != "cloud" {
					return true
				}
				switch sel.Sel.Name {
				case "ActionBlock", "ActionChallenge":
					found++
					t.Errorf("%s:%d names cloud.%s — this app's vocabulary is allow, review and "+
						"restrict, and the credit door reads a block CARRYING a refusal as the fail "+
						"policy's no-decision rather than as a determination",
						name, fset.Position(sel.Pos()).Line, sel.Sel.Name)
				}
				return true
			})
		}
	}
	// The scan must actually be looking at this package: a filter that matched no
	// file would pass the assertion above vacuously.
	if len(pkgs) == 0 {
		t.Fatal("the scan parsed no package at all — the assertion above proves nothing")
	}
	for _, pkg := range pkgs {
		if len(pkg.Files) < 2 {
			t.Fatalf("the scan parsed %d file(s) of package %s — the assertion above proves nothing",
				len(pkg.Files), pkg.Name)
		}
	}
	// And it must be able to SEE the vocabulary it is looking for, or a renamed
	// import would silently disarm it.
	if _, err := os.Stat("determine.go"); err != nil {
		t.Fatalf("determine.go: %v", err)
	}
	if !usesCloudAction(t, fset, pkgs) {
		t.Error("no file resolves any cloud.Action* at all — the scan cannot see the vocabulary " +
			"it is policing, so its silence means nothing")
	}
	_ = found
}

// usesCloudAction reports whether the package names ANY cloud.Action* constant, so
// the scan above is known to be able to see one.
func usesCloudAction(t *testing.T, fset *token.FileSet, pkgs map[string]*ast.Package) bool {
	t.Helper()
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			var seen bool
			ast.Inspect(file, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				if id, ok := sel.X.(*ast.Ident); ok && id.Name == "cloud" &&
					strings.HasPrefix(sel.Sel.Name, "Action") {
					seen = true
				}
				return true
			})
			if seen {
				return true
			}
		}
	}
	return false
}

// ── the aggregate halves ─────────────────────────────────────────────────────
//
// The event's own facts are what the geography half decides on, and a payment
// split into pieces defeats them by construction: every piece is under the bound
// because that is what splitting it is for. These two halves decide on what the
// organisation's aggregates ALREADY HELD about the identifiers carrying it, which
// is the only place the pattern exists.

// burst is a reading of one axis at a stated count and accrued value, in the
// shape [plane.prior] produces.
func burst(axis string, events int, usd float64) reading {
	return reading{Pace: []paced{{Axis: axis, Events: events, Nano: nanoOfUSD(usd), Span: time.Hour}}}
}

// TestPace_ABurstIsReviewedAndAFundedBurstIsFrozen — the whole velocity rule, over
// the bounds it is stated in. Each row is one branch.
//
// Mutation proof: drop the count branch and the burst rows fall to allow; drop the
// conjunction from the freeze branch and a busy hour moving pocket change freezes
// a customer.
func TestPace_ABurstIsReviewedAndAFundedBurstIsFrozen(t *testing.T) {
	for _, tc := range []struct {
		name   string
		seen   reading
		action string
		cause  string
	}{{
		name: "one ordinary payment on a subject with an ordinary history",
		// The negative control. A rule that reviews this is a rule nobody can ship.
		seen:   burst(axisSubject, 3, 300),
		action: cloud.ActionAllow,
	}, {
		name:   "just under the burst bound, nothing fires",
		seen:   burst(axisSubject, burstEvents-1, 300),
		action: cloud.ActionAllow,
	}, {
		name:   "a burst at an ordinary value is examined",
		seen:   burst(axisSubject, burstEvents, 300),
		action: cloud.ActionReview, cause: causeBurst + axisSubject,
	}, {
		name: "a value ACCRUED past the examining threshold is examined, at any count",
		// The typology the value half cannot see: five payments of eleven thousand
		// are each under [reviewNano] and together are not.
		seen:   burst(axisSubject, 5, 55_000),
		action: cloud.ActionReview, cause: causeAccrued + axisSubject,
	}, {
		name:   "a burst accruing past the freeze is frozen",
		seen:   burst(axisSubject, burstEvents, 12_000),
		action: cloud.ActionRestrict, cause: causeBurstValue + axisSubject,
	}, {
		name: "an accrual past the freeze but under the burst count is only examined",
		// The conjunction, held from the other side: money alone never freezes, for
		// the same reason [reviewNano] is a look and not a refusal.
		seen:   burst(axisSubject, 2, 60_000),
		action: cloud.ActionReview, cause: causeAccrued + axisSubject,
	}, {
		name: "the bound is about ONE identifier and not about the subject",
		// The device carrying a burst is the same finding as the subject carrying
		// one, and it names the device so an investigator looks at the right thing.
		seen:   burst(axisDevice, burstEvents, 300),
		action: cloud.ActionReview, cause: causeBurst + axisDevice,
	}, {
		name:   "a counterparty pair carrying a funded burst is frozen and says so",
		seen:   burst(axisPair, burstEvents, 12_000),
		action: cloud.ActionRestrict, cause: causeBurstValue + axisPair,
	}, {
		name: "aggregates that said nothing decide nothing",
		// The reading a fresh subject produces. It must be silence and never a
		// finding, or every first payment is a finding.
		seen:   reading{},
		action: cloud.ActionAllow,
	}} {
		t.Run(tc.name, func(t *testing.T) {
			if got := onPace(tc.seen); got.fired() != (tc.action != cloud.ActionAllow) {
				t.Fatalf("the pace half %v on %+v", got, tc.seen)
			}
			got := determine("US", 50*nanoPerUSD, tc.seen)
			if got.Action != tc.action {
				t.Errorf("action %q, want %q", got.Action, tc.action)
			}
			if tc.cause != "" && got.Cause != tc.cause {
				t.Errorf("cause %q, want %q", got.Cause, tc.cause)
			}
		})
	}
}

// TestFan_AnIdentifierSharedAcrossSubjectsIsExamined — the fan-out rule, over its
// one bound and both identifiers it reads.
//
// Mutation proof: delete the comparison and the shared rows fall to allow; change
// it to `>` and the row AT the bound stops firing.
func TestFan_AnIdentifierSharedAcrossSubjectsIsExamined(t *testing.T) {
	for _, tc := range []struct {
		name   string
		seen   reading
		action string
		cause  string
	}{{
		name: "a device a household shares is the ordinary case",
		// The negative control, and the reason the bound is generous.
		seen:   reading{Shared: []shared{{Axis: axisDevice, Subjects: 4}}},
		action: cloud.ActionAllow,
	}, {
		name:   "just under the bound, nothing fires",
		seen:   reading{Shared: []shared{{Axis: axisDevice, Subjects: fanSubjects - 1}}},
		action: cloud.ActionAllow,
	}, {
		name:   "a device tying together more subjects than the bound is examined",
		seen:   reading{Shared: []shared{{Axis: axisDevice, Subjects: fanSubjects}}},
		action: cloud.ActionReview, cause: causeShared + axisDevice,
	}, {
		name:   "a counterparty tying together more subjects than the bound is examined",
		seen:   reading{Shared: []shared{{Axis: axisPeer, Subjects: fanSubjects}}},
		action: cloud.ActionReview, cause: causeShared + axisPeer,
	}, {
		name: "and it is examined and NEVER frozen, however wide the network",
		// A shared identifier is evidence about a relationship, not about a payment.
		// A household, an office and a farm are the same shape from here.
		seen:   reading{Shared: []shared{{Axis: axisDevice, Subjects: 100 * fanSubjects}}},
		action: cloud.ActionReview, cause: causeShared + axisDevice,
	}} {
		t.Run(tc.name, func(t *testing.T) {
			got := determine("US", 50*nanoPerUSD, tc.seen)
			if got.Action != tc.action {
				t.Errorf("action %q, want %q", got.Action, tc.action)
			}
			if tc.cause != "" && got.Cause != tc.cause {
				t.Errorf("cause %q, want %q", got.Cause, tc.cause)
			}
		})
	}
}

// TestAggregates_AreIndependentOfTheModelsWarmth is the property both halves exist
// for, stated as a table against every state the engine can be in.
//
// A fresh account is exactly where account farming and card testing live, and a
// fresh account's model is warming — so a rule these could not fire under a
// refusal would be a rule that is off precisely where it is needed. A model with
// no opinion contributes an allow to the fusion and an allow cannot lower
// anything.
//
// Mutation proof: return early from [fuse] when the assessment is unscored, or
// read d.A.Scored anywhere in [determine], and every refusal row fails.
func TestAggregates_AreIndependentOfTheModelsWarmth(t *testing.T) {
	for _, seen := range []struct {
		name   string
		seen   reading
		action string
	}{
		{"a funded burst", burst(axisSubject, burstEvents, 12_000), cloud.ActionRestrict},
		{"a burst on a device", burst(axisDevice, burstEvents, 10), cloud.ActionReview},
		{"a shared device", reading{Shared: []shared{{Axis: axisDevice, Subjects: fanSubjects}}}, cloud.ActionReview},
	} {
		for _, a := range []anomaly.Assessment{
			{Scored: false, Reason: anomaly.ReasonWarming, Score: 0.0},
			{Scored: false, Reason: anomaly.ReasonUnusable, Score: 0.0},
			{Scored: false, Reason: anomaly.ReasonUnidentified, Score: 0.0},
			{Scored: true, Score: 0.0, Cut: 0.5},              // scored, and maximally normal
			{Scored: true, Score: 0.9, Cut: 0.5, Alert: true}, // scored, and already alerting
		} {
			// The unlisted jurisdiction at an ordinary value is deliberate: the
			// geography half finds NOTHING here, so the action can only have come from
			// the aggregate half under test.
			got := fuse(answer(decided{A: a, Shape: "halfspace:x"}),
				determine("US", 50*nanoPerUSD, seen.seen), false)
			if got.Action != seen.action {
				t.Errorf("%s, model state %+v: action %q, want %q — the aggregate determination "+
					"is not the model's to soften", seen.name, a, got.Action, seen.action)
			}
		}
	}
}

// TestAggregates_ShadowRecordsTheFindingAndChangesNothing. An organisation that
// has not armed its model has not armed these rules either: the finding is
// COMPUTED and RECORDED, and the outcome is left exactly where the model left it.
//
// This is the state EVERY organisation is in today, so it is the state that must
// be proven harmless. Mutation proof: drop the shadow branch from [fuse] and a
// control nobody reviewed freezes real money.
func TestAggregates_ShadowRecordsTheFindingAndChangesNothing(t *testing.T) {
	got := fuse(answer(warming(true)), determine("US", 50*nanoPerUSD,
		burst(axisSubject, burstEvents, 12_000)), true)

	if got.Action != cloud.ActionAllow {
		t.Fatalf("action %q, want %q — a control nobody reviewed froze real money",
			got.Action, cloud.ActionAllow)
	}
	if !strings.Contains(got.Cause, "in shadow") {
		t.Errorf("cause %q does not say the finding was in shadow", got.Cause)
	}
	if !strings.Contains(got.Cause, "burst") {
		t.Errorf("cause %q does not carry the finding at all — shadow reports what it WOULD "+
			"have done; it does not stay silent", got.Cause)
	}
}

// TestDetermine_TheSeverestOfTheThreeHalvesStandsAndEveryFindingIsReported.
//
// Three judges of different kinds reach one answer. Taking the milder would let a
// half that found nothing switch off a half that found something, and dropping a
// reason would leave an operator reading one finding about an event that produced
// three.
func TestDetermine_TheSeverestOfTheThreeHalvesStandsAndEveryFindingIsReported(t *testing.T) {
	// A monitored jurisdiction (review), a funded burst (restrict) and a shared
	// device (review), on one event.
	seen := burst(axisSubject, burstEvents, 12_000)
	seen.Shared = []shared{{Axis: axisDevice, Subjects: fanSubjects}}
	got := determine("HT", 50*nanoPerUSD, seen)

	if got.Action != cloud.ActionRestrict {
		t.Fatalf("action %q, want the severest of the three (%q)", got.Action, cloud.ActionRestrict)
	}
	// The reason that SET the action leads, and the others stand beside it.
	if !strings.HasPrefix(got.Cause, causeBurstValue+axisSubject) {
		t.Errorf("cause %q does not lead with the finding that set the action", got.Cause)
	}
	for _, want := range []string{causeMonitored, causeShared + axisDevice} {
		if !strings.Contains(got.Cause, want) {
			t.Errorf("cause %q lost the finding %q — three findings about one event, and an "+
				"operator has to see all of them", got.Cause, want)
		}
	}
}

// TestDetermine_TheAggregateBoundsCannotDisableTheRules.
//
// The bounds are policy, and a policy that read as zero would not be a permissive
// setting — it would be the rule firing on everything in one direction and
// silently switched off in the other. Both are refused HERE, where a number is
// changed, rather than discovered at a credit door.
func TestDetermine_TheAggregateBoundsCannotDisableTheRules(t *testing.T) {
	// THE COUNT MUST BE ABOVE WHAT A FOLD ALONE PRODUCES. A tenant's own surface
	// folds in one observation per (subject, featureBucket); at or under that
	// figure the rule fires on every continuously active customer, and on this
	// organisation's own history the moment a residency rebuilds.
	perWindow := int(burstWindow() / featureBucket)
	switch {
	case burstEvents <= 0:
		t.Error("the burst bound is not positive, so every event is a burst and the rule reviews " +
			"the whole product")
	case burstEvents <= perWindow:
		t.Errorf("the burst bound (%d) is at or under what a FOLD alone puts in the burst window "+
			"(%d = %s / %s), so this organisation's own history trips it",
			burstEvents, perWindow, burstWindow(), featureBucket)
	case burstEvents >= recordRows:
		t.Errorf("the burst bound (%d) is past what the tenant's retained record can hold (%d), "+
			"so no traffic can ever reach it and the rule is off with nothing to see",
			burstEvents, recordRows)
	}
	// THE FAN MUST BE ABOVE ONE. One distinct subject is EVERY identifier, so a
	// bound of one turns "shared" into "exists"; a bound of zero is also the
	// query's own LIMIT, which returns nothing and switches the rule off.
	switch {
	case fanSubjects <= 1:
		t.Error("the fan-out bound is one or less, so every device and every counterparty is " +
			"shared and the rule reviews the whole product")
	case fanSubjects >= recordRows:
		t.Errorf("the fan-out bound (%d) is past what the tenant's retained record can hold (%d), "+
			"so it can never be reached and the rule is off with nothing to see", fanSubjects, recordRows)
	}
	// And the aggregates must actually KEEP the window the count is read over, or
	// every reading is a zero that looks exactly like a quiet subject.
	if _, kept := newRings().pace(velocity.Key{OrgID: "o", Kind: anomaly.AxisAccount, Value: "s"}); !kept {
		t.Fatal("the aggregates keep no window, so the burst bound is read over nothing")
	}
}

// burstWindow is the window [onPace]'s count bound is actually read over: the
// narrowest one a fresh ring set keeps. It is MEASURED from the rings rather than
// restated, because the whole point of taking the window instead of naming it is
// that no second statement of it can drift.
func burstWindow() time.Duration {
	w, _ := newRings().pace(velocity.Key{OrgID: "o", Kind: anomaly.AxisAccount, Value: "s"})
	return w.Span
}

// TestPace_OverTheWire is the velocity deliverable through the op a gate actually
// calls, on an ARMED organisation, against REAL aggregates filled by that
// organisation's own learn door — and with a model that has learned far too
// little to have an opinion.
//
// It is what proves the reading, the axes and the bounds are wired end to end
// rather than only inside the pure functions above.
//
// Mutation proof: drop [plane.prior] from [planeDecide] and this fails with
// action=allow; read the widest window instead of the narrowest and the burst
// dissolves into a month.
func TestPace_OverTheWire(t *testing.T) {
	probe.reset(true)
	mountApp(t)
	p := mounted.State.plane
	holdFolds(t, p)
	k := key(t, brandA, orgA)
	arm(t, p, k)

	// A funded burst on ONE subject: past the count bound, accruing past the freeze
	// and nowhere near the examining threshold, so only the conjunction can fire.
	at := time.Now().UTC().Add(-10 * time.Minute)
	batch := make([]observation, 0, burstEvents)
	for i := 0; i < burstEvents; i++ {
		batch = append(batch, ob(t, "burst_"+itoa(i), kindAccount, "u_fast", 200,
			at.Add(time.Duration(i)*time.Second)))
	}
	if _, err := p.learn(k, batch...); err != nil {
		t.Fatalf("learn: %v", err)
	}

	out, err := planeDecide(asPeer(orgA), &contract.RiskDecideIn{
		Stage: cloud.StagePayment, Kind: contract.KindAccount, Subject: "u_fast",
		// One more ordinary payment. Its own value is far under every stated bound,
		// so the event's own facts find nothing and the aggregates are the whole rule.
		Signals: []contract.Signal{{Name: contract.SignalNano, Value: "200000000000"}},
	})
	if err != nil {
		t.Fatalf("planeDecide: %v", err)
	}
	if out.Refusal == "" {
		t.Fatal("the model answered with an opinion — this test is meaningless unless it is warming")
	}
	if out.Action != cloud.ActionRestrict {
		t.Fatalf("action %q, want %q — over the wire, sixty payments accruing twelve thousand "+
			"dollars in ten minutes were allowed because the MODEL had no opinion",
			out.Action, cloud.ActionRestrict)
	}
	if !strings.Contains(out.Cause, "burst") || !strings.Contains(out.Cause, axisSubject) {
		t.Errorf("cause %q does not name the burst and the identifier it was found on", out.Cause)
	}
}

// TestFan_OverTheWire is the fan-out deliverable through the same op: twenty
// nominally unrelated subjects, one device, and a first ordinary payment from the
// twenty-first.
//
// Every one of those accounts is unremarkable taken by itself, which is the whole
// point — the finding exists only in what they share.
//
// Mutation proof: drop the device from [plane.prior]'s links and this fails with
// action=allow; count the subjects without DISTINCT and it fires on one busy
// account.
func TestFan_OverTheWire(t *testing.T) {
	probe.reset(true)
	mountApp(t)
	p := mounted.State.plane
	holdFolds(t, p)
	k := key(t, brandA, orgA)
	arm(t, p, k)

	// One event each, from [fanSubjects] distinct subjects, all on one device. The
	// count is far under the burst bound and the value far under every value bound,
	// so nothing but the fan-out can fire.
	at := time.Now().UTC().Add(-time.Hour)
	batch := make([]observation, 0, fanSubjects)
	for i := 0; i < fanSubjects; i++ {
		batch = append(batch, ob(t, "farm_"+itoa(i), kindAccount, "u_farm_"+itoa(i), 5,
			at.Add(time.Duration(i)*time.Second), "", "d_shared"))
	}
	if _, err := p.learn(k, batch...); err != nil {
		t.Fatalf("learn: %v", err)
	}

	out, err := planeDecide(asPeer(orgA), &contract.RiskDecideIn{
		Stage: cloud.StagePayment, Kind: contract.KindAccount, Subject: "u_farm_next",
		Signals: []contract.Signal{
			{Name: contract.SignalNano, Value: "5000000000"},
			{Name: contract.SignalDevice, Value: "d_shared"},
		},
	})
	if err != nil {
		t.Fatalf("planeDecide: %v", err)
	}
	if out.Refusal == "" {
		t.Fatal("the model answered with an opinion — this test is meaningless unless it is warming")
	}
	if out.Action != cloud.ActionReview {
		t.Fatalf("action %q, want %q — over the wire, the twenty-first account on one device "+
			"was allowed", out.Action, cloud.ActionReview)
	}
	if !strings.Contains(out.Cause, axisDevice) {
		t.Errorf("cause %q does not name the device the subjects share", out.Cause)
	}
}

// TestAggregates_OverTheWireAnOrdinaryEventIsAllowed is the negative control for
// both halves, and it is the row that makes every assertion above mean something:
// the same armed organisation, the same door, real aggregates holding real
// history, and one ordinary low-value payment ALLOWS.
//
// A rule that reviewed this is a rule nobody can ship.
func TestAggregates_OverTheWireAnOrdinaryEventIsAllowed(t *testing.T) {
	probe.reset(true)
	mountApp(t)
	p := mounted.State.plane
	holdFolds(t, p)
	k := key(t, brandA, orgA)
	arm(t, p, k)

	// An ordinary customer: a handful of payments, its own device, its own
	// counterparty. Everything below every stated bound.
	at := time.Now().UTC().Add(-30 * time.Minute)
	batch := make([]observation, 0, 8)
	for i := 0; i < 8; i++ {
		batch = append(batch, ob(t, "ok_"+itoa(i), kindAccount, "u_ordinary", 120,
			at.Add(time.Duration(i)*time.Minute), "merchant", "d_own"))
	}
	if _, err := p.learn(k, batch...); err != nil {
		t.Fatalf("learn: %v", err)
	}

	out, err := planeDecide(asPeer(orgA), &contract.RiskDecideIn{
		Stage: cloud.StagePayment, Kind: contract.KindAccount, Subject: "u_ordinary",
		Signals: []contract.Signal{
			{Name: contract.SignalNano, Value: "120000000000"}, // one hundred and twenty dollars
			{Name: contract.SignalCountry, Value: "US"},
			{Name: contract.SignalDevice, Value: "d_own"},
			{Name: contract.SignalPeer, Value: "merchant"},
		},
	})
	if err != nil {
		t.Fatalf("planeDecide: %v", err)
	}
	if out.Action != cloud.ActionAllow {
		t.Fatalf("action %q, want %q — an ordinary customer's ordinary payment was stopped "+
			"(cause %q)", out.Action, cloud.ActionAllow, out.Cause)
	}
}

// arm states a live regime on ONE TEST TENANT, and nothing else. It states that
// regime on this test's own temporary data directory; it is not a deployment, and
// no organisation anywhere is armed by it.
func arm(t *testing.T, p *plane, k tenant) {
	t.Helper()
	if _, err := p.appetite(k, 0.02, 0.10, true /* live */, "u_"+orgA); err != nil {
		t.Fatalf("appetite: %v", err)
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
