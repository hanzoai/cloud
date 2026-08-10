package risk

// risk_rpc_test.go — the internal scorer, held to the three properties that make
// it safe to put a payment behind it.
//
//	THE TENANT IS THE CALLER'S. Not a field, not a subject that looks like a key,
//	not the HTTP principal (there is no request here at all).
//	A REFUSAL CARRIES NO SCORE. The engine populates one before it decides it has
//	no opinion, and publishing that number reads as a clean bill of health.
//	AN ALERT IS A REVIEW. A density estimate may summon a person and may not
//	block by itself, and in shadow it changes nothing at all.

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/luxfi/aml/pkg/anomaly"

	"github.com/hanzoai/cloud"
	contract "github.com/hanzoai/cloud/plane"
	"github.com/zap-proto/zip"
)

// asPeer is a plane call: a context with NO request behind it, carrying the org
// the caller states. It is the one place zip reads a stated caller, and it is how
// every peer reaches this op.
func asPeer(org string) context.Context { return cloud.For(context.Background(), org) }

// TestPlaneTenant_IsMintedFromTheCallerAndNothingElse.
//
// Mutation proof: read the org from anywhere but cloud.Who and the second case
// stops failing closed.
func TestPlaneTenant_IsMintedFromTheCallerAndNothingElse(t *testing.T) {
	got, err := planeTenant(asPeer(orgA), brandA)
	if err != nil {
		t.Fatalf("planeTenant: %v", err)
	}
	if want := key(t, brandA, orgA); got != want {
		t.Errorf("tenant %q, want %q — the mint must qualify the CALLER's org with this deployment's brand", got, want)
	}

	// No caller is no tenant. A peer that states nothing gets no model, rather
	// than the deployment's brand over an empty org.
	if _, err := planeTenant(context.Background(), brandA); err == nil {
		t.Error("a call with no stated caller resolved a tenant — an unidentified peer must reach no model")
	}

	// The BRAND half is the deployment's. Two organisations with the same name
	// under two brands are two tenants, and this is where that holds.
	other, err := planeTenant(asPeer(orgA), brandB)
	if err != nil {
		t.Fatalf("planeTenant(brandB): %v", err)
	}
	if other == got {
		t.Error("the same org under two brands minted one tenant — the brand half is not in the key")
	}
}

// TestRiskDecideIn_CannotNameAnOrg is the STRUCTURAL half of the isolation: a
// caller cannot spoof a tenant it cannot spell. The contract carries no org, no
// tenant and no brand, so there is no field for a handler to be tempted by and no
// wire for one to arrive on.
//
// Mutation proof: add an Org field to plane.RiskDecideIn and this names it.
func TestRiskDecideIn_CannotNameAnOrg(t *testing.T) {
	rt := reflect.TypeFor[contract.RiskDecideIn]()
	for i := 0; i < rt.NumField(); i++ {
		name := strings.ToLower(rt.Field(i).Name)
		for _, banned := range []string{"org", "tenant", "brand", "owner"} {
			if strings.Contains(name, banned) {
				t.Errorf("plane.RiskDecideIn.%s names the tenant — the organisation whose model answers "+
					"rides the caller, never the argument", rt.Field(i).Name)
			}
		}
	}
}

// TestPlaneDecide_ASpoofedSubjectCannotCrossTenants is the BEHAVIOURAL half.
//
// The subject is the one string a caller does control, so the attack is to spell
// another tenant's key in it. It buys nothing: the subject is an identifier
// WITHIN the caller's tenant, and the only residency the call creates is the
// caller's own.
//
// Mutation proof: mint the tenant from in.Subject and the residency check names
// the wrong key.
func TestPlaneDecide_ASpoofedSubjectCannotCrossTenants(t *testing.T) {
	probe.reset(true)
	mountApp(t)

	out, err := planeDecide(asPeer(orgA), &contract.RiskDecideIn{
		Stage: cloud.StagePayment, Kind: contract.KindAccount,
		// Every shape of "be someone else" a subject can carry.
		Subject: brandB + "/" + orgB,
	})
	if err != nil {
		t.Fatalf("planeDecide: %v", err)
	}
	if out.Action != cloud.ActionAllow {
		t.Errorf("action %q, want %q — a warming model changes no outcome", out.Action, cloud.ActionAllow)
	}

	p := mounted.State.plane
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.res) != 1 {
		t.Fatalf("%d residencies after one call, want 1", len(p.res))
	}
	if _, ok := p.res[key(t, brandA, orgA)]; !ok {
		var held []string
		for k := range p.res {
			held = append(held, string(k))
		}
		t.Errorf("the call landed in %v, not in the CALLER's tenant — a subject named the model", held)
	}
}

// TestPlaneDecide_AWarmingModelDeclinesWithoutAScore is the trap this op exists
// not to fall into: the engine assigns a score BEFORE it checks whether it has
// learned enough to have one, so a fresh tenant's refusal carries a populated,
// meaningless number.
//
// Mutation proof: copy the score onto the answer unconditionally and this fails.
func TestPlaneDecide_AWarmingModelDeclinesWithoutAScore(t *testing.T) {
	probe.reset(true)
	mountApp(t)

	out, err := planeDecide(asPeer(orgA), &contract.RiskDecideIn{
		Stage: cloud.StagePayment, Kind: contract.KindAccount, Subject: "u_412",
		Signals: []contract.Signal{{Name: contract.SignalNano, Value: "420000000"}},
	})
	if err != nil {
		t.Fatalf("planeDecide: %v", err)
	}
	if out.Refusal == "" {
		t.Fatal("a model that has learned nothing answered without a refusal — silence reads as innocence")
	}
	if out.Score != 0 {
		t.Errorf("a refusal carried score %v — a declining model's score is arithmetic, not an opinion", out.Score)
	}
	if out.Action != cloud.ActionAllow {
		t.Errorf("action %q, want %q — an absent opinion must not change an outcome", out.Action, cloud.ActionAllow)
	}
}

// TestPlaneDecide_RefusesAMomentItDoesNotModel. The stage is a closed set both
// ends read from cloud, and a scorer that judged an unrecognised moment would be
// answering a question neither end had agreed on.
func TestPlaneDecide_RefusesAMomentItDoesNotModel(t *testing.T) {
	probe.reset(true)
	mountApp(t)

	for _, stage := range []string{"", "checkout", "PAYMENT"} {
		_, err := planeDecide(asPeer(orgA), &contract.RiskDecideIn{
			Stage: stage, Kind: contract.KindAccount, Subject: "u_412",
		})
		var he *zip.HTTPError
		if !asHTTP(err, &he) || he.Status != 400 {
			t.Errorf("stage %q: err %v, want a 400 — an unrecognised moment is a refusal, not a verdict", stage, err)
		}
	}
	// And the three cloud declares are all accepted.
	for _, stage := range []string{cloud.StageSignup, cloud.StageUsage, cloud.StagePayment} {
		if !knownStage(stage) {
			t.Errorf("stage %q is declared by cloud and refused here — the two ends read one set", stage)
		}
	}
}

// TestPlaneDecide_RefusesAKindItCannotPlace: the kind namespaces the subject, so
// an unknown one would judge a different entity than the caller meant. It is the
// observation constructor's own bound, reached through this door too.
func TestPlaneDecide_RefusesAKindItCannotPlace(t *testing.T) {
	probe.reset(true)
	mountApp(t)

	if _, err := planeDecide(asPeer(orgA), &contract.RiskDecideIn{
		Stage: cloud.StagePayment, Kind: "acount", Subject: "u_412",
	}); err == nil {
		t.Error("a misspelled kind was judged — a subject in no namespace is a verdict about nobody")
	}
	// The three the contract publishes are exactly the three this model places.
	for _, kind := range []string{contract.KindPerson, contract.KindSession, contract.KindAccount} {
		if !known(kind) {
			t.Errorf("the contract publishes kind %q and this model cannot place it — one set, two spellings", kind)
		}
	}
}

// TestAnswer_ARefusalCarriesNoScore, on the projection itself, over the three
// refusals the engine declares.
func TestAnswer_ARefusalCarriesNoScore(t *testing.T) {
	for _, reason := range []string{anomaly.ReasonWarming, anomaly.ReasonUnusable, anomaly.ReasonUnidentified} {
		got := answer(decided{
			// Scored false with a populated score is exactly what the engine returns.
			A:       anomaly.Assessment{Scored: false, Reason: reason, Score: 0.93, Cut: 0.5},
			Version: 7, Shape: "halfspace:abc",
		})
		switch {
		case got.Refusal != reason:
			t.Errorf("refusal %q, want %q", got.Refusal, reason)
		case got.Score != 0:
			t.Errorf("%s: score %v travelled with a refusal", reason, got.Score)
		case got.Action != cloud.ActionAllow:
			t.Errorf("%s: action %q, want allow — a model with no opinion changes no outcome", reason, got.Action)
		case got.Shape != "halfspace:abc" || got.Policy != 7:
			t.Errorf("%s: the answer lost the shape/policy that pins it to a model", reason)
		}
	}
}

// TestAnswer_AnAlertIsAReviewAndShadowChangesNothing — the whole action mapping,
// in one table.
//
// BLOCK IS ABSENT ON PURPOSE. cloud's own vocabulary says a statistical judgement
// "may reach [review] and no further on its own", and this model is exactly one.
//
// Mutation proof: return ActionBlock on an alert and the first row fails; drop
// the shadow row's cause and the second stops reporting what the model would have
// done.
func TestAnswer_AnAlertIsAReviewAndShadowChangesNothing(t *testing.T) {
	for _, tc := range []struct {
		name         string
		a            anomaly.Assessment
		action, why  string
		wantTheScore float64
	}{
		{
			name:   "live, above the cut — a person is summoned and the request proceeds",
			a:      anomaly.Assessment{Scored: true, Score: 0.9, Cut: 0.5, Alert: true},
			action: cloud.ActionReview, why: causeAboveCut, wantTheScore: 0.9,
		},
		{
			name:   "shadow, above the cut — nothing changes and the fact is stated",
			a:      anomaly.Assessment{Scored: true, Score: 0.9, Cut: 0.5, Shadow: true},
			action: cloud.ActionAllow, why: causeShadowCut, wantTheScore: 0.9,
		},
		{
			name:   "at the cut — inside the stated appetite",
			a:      anomaly.Assessment{Scored: true, Score: 0.5, Cut: 0.5},
			action: cloud.ActionAllow, why: causeWithinAppetite, wantTheScore: 0.5,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := answer(decided{A: tc.a, Version: 3, Shape: "halfspace:def"})
			if got.Action != tc.action {
				t.Errorf("action %q, want %q", got.Action, tc.action)
			}
			if got.Cause != tc.why {
				t.Errorf("cause %q, want %q", got.Cause, tc.why)
			}
			if got.Score != tc.wantTheScore {
				t.Errorf("score %v, want %v — a scored answer carries its own number", got.Score, tc.wantTheScore)
			}
			if got.Refusal != "" {
				t.Errorf("a scored answer carried refusal %q", got.Refusal)
			}
		})
	}
}

// TestDecideEvent_ReadsTheSignalsItKnowsAndBlindsWhatItCannot.
//
// An amount it cannot read is ABSENT rather than zero: the value features then
// read blind, which is a fact the model state reports, instead of being told the
// payment moved nothing.
func TestDecideEvent_ReadsTheSignalsItKnowsAndBlindsWhatItCannot(t *testing.T) {
	ev := decideEvent(&contract.RiskDecideIn{
		Kind: contract.KindAccount, Subject: "u_1",
		Signals: []contract.Signal{
			{Name: contract.SignalNano, Value: "420000000"},
			{Name: contract.SignalPeer, Value: "mer_7"},
			{Name: contract.SignalDevice, Value: "dev_9"},
			{Name: "ip", Value: "203.0.113.7"}, // a name this scorer does not read
		},
	})
	if ev.Nano != 420_000_000 || ev.Peer != "mer_7" || ev.Device != "dev_9" {
		t.Errorf("the event lost a signal it knows: %+v", ev)
	}
	if ev.At != "" {
		t.Errorf("'at' was invented as %q — an unstated time is now, decided downstream", ev.At)
	}
	for _, bad := range []string{"", "4.2e8", "420000000 ", "0x10", "nine"} {
		if got := nanoOf(bad); got != 0 {
			t.Errorf("nanoOf(%q) = %d, want 0 — an amount we cannot read is one we do not have", bad, got)
		}
	}
}

// asHTTP is errors.As for zip's HTTP error, kept local so the assertions above
// read as one line.
func asHTTP(err error, target **zip.HTTPError) bool {
	he, ok := err.(*zip.HTTPError)
	if ok {
		*target = he
	}
	return ok
}
