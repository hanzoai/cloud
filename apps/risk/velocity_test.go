package risk

// velocity_test.go — THE VELOCITY HALVES, MADE REAL AT THE CREDIT ENDPOINT.
//
// The pace and fan-out rules were STRUCTURALLY dead there — not mis-tuned, but unable
// to reach a correct answer for any input:
//
//	THE ENDPOINT STATED NO LINK IDENTIFIER, so the fan-out half could not fire at
//	all, and a half that cannot fire reads exactly like a half that found nothing.
//
//	NOTHING TAUGHT THE MODEL ANYTHING. A decide records nothing by design and the
//	published learn endpoint is an organisation calling itself — and at a self-serve
//	credit endpoint the organisation IS the payer. Five payments of eleven thousand
//	dollars looked like five first payments.
//
//	THE ACCRUAL COULD BE ERASED. A negative taught amount subtracts from the very
//	sum the bound is read off, which is self-suppression by the party being bounded.

import (
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
	contract "github.com/hanzoai/cloud/plane"
)

// TestFan_ThePeerAxisIsReachableFromTheCreditDoor.
//
// [onFan] is the only half of the rule that can see account farming, because farming
// is unremarkable from every account taken by itself — the pattern exists ONLY in
// what the accounts share. It reads two link identifiers, and a link identifier no
// endpoint states does not exist: with the credit endpoint stating neither, this half
// could not fire for any input at all, and a half that cannot fire reads exactly like
// a half that found nothing.
//
// The endpoint now states the address our own edge resolved on the peer axis, so this
// drives the rule the way the endpoint drives it: twenty nominally unrelated payers
// behind one address, then a first ordinary payment from the twenty-first.
//
// Mutation proof: drop [plane.SignalPeer] from [paymentFacts] (or the peer link from
// [plane.prior]) and this fails with action=allow.
func TestFan_ThePeerAxisIsReachableFromTheCreditDoor(t *testing.T) {
	probe.reset(true)
	mountApp(t)
	p := mounted.State.plane
	holdFolds(t, p)
	k := key(t, brandA, orgA)
	arm(t, p, k)

	// One settled payment each, from [fanSubjects] distinct payers, all behind one
	// address. The count per subject is one and every value is trivial, so nothing but
	// the fan-out can fire.
	const address = "203.0.113.7"
	at := time.Now().UTC().Add(-30 * time.Minute)
	batch := make([]observation, 0, fanSubjects)
	for i := range fanSubjects {
		batch = append(batch, ob(t, "farm_"+itoa(i), kindPayer, "u_farm_"+itoa(i), 5,
			at.Add(time.Duration(i)*time.Second), address))
	}
	if _, err := p.learn(k, batch...); err != nil {
		t.Fatalf("learn: %v", err)
	}

	out, err := planeDecide(asPeer(orgA), &contract.RiskDecideIn{
		Stage: cloud.StagePayment, Kind: contract.KindPayer, Subject: "u_farm_next",
		Signals: []contract.Signal{
			{Name: contract.SignalNano, Value: "5000000000"},
			{Name: contract.SignalPeer, Value: address},
		},
	})
	if err != nil {
		t.Fatalf("planeDecide: %v", err)
	}
	if out.Refusal == "" {
		t.Fatal("the model answered with an opinion — this test is meaningless unless it is warming")
	}
	if out.Action != cloud.ActionReview {
		t.Fatalf("action %q, want %q — the twenty-first payer behind one address was allowed, "+
			"so the fan-out is unreachable from this endpoint", out.Action, cloud.ActionReview)
	}
	if !strings.Contains(out.Cause, axisPeer) {
		t.Errorf("cause %q does not name the identifier the payers share", out.Cause)
	}
}

// TestObserve_ASettledPaymentTeachesTheModel is HIGH-2 end to end, over the op a
// settling process actually calls.
//
// It is the test the whole batch turns on. Before it, nothing in the fleet taught the
// credit endpoint's rule anything: decide records nothing, the published learn
// endpoint is an organisation calling itself, and at a self-serve credit endpoint the
// organisation IS the payer — so five payments of eleven thousand dollars looked like
// five first payments, every one of them under every stated bound by construction.
//
// Five settle here, and the sixth decide sees fifty-five thousand of prior accrual.
//
// Mutation proof: stop calling [plane.learn] from [planeObserve] and the final decide
// allows; key the observation on anything but the settlement and the replay below
// double-counts.
func TestObserve_ASettledPaymentTeachesTheModel(t *testing.T) {
	probe.reset(true)
	mountApp(t)
	p := mounted.State.plane
	holdFolds(t, p)
	k := key(t, brandA, orgA)
	arm(t, p, k)

	// FIVE SETTLED TOP-UPS OF ELEVEN THOUSAND. Each is far under [reviewNano], so no
	// point-in-time test can see any of them — which is the typology the accrual
	// exists for.
	const each = "11000000000000" // $11,000 in nano-USD
	for i := range 5 {
		out, err := planeObserve(asPeer(orgA), &contract.RiskObserveIn{
			Stage: cloud.StagePayment, Kind: contract.KindPayer, Subject: "u_split",
			Settlement: "sq_pay_" + itoa(i),
			Signals:    []contract.Signal{{Name: contract.SignalNano, Value: each}},
		})
		if err != nil {
			t.Fatalf("observe settlement %d: %v", i, err)
		}
		if out.Learned != 1 {
			t.Fatalf("settlement %d learned %d, want 1", i, out.Learned)
		}
	}

	// IDEMPOTENT UNDER REPLAY. Settlement is at-least-once — a retried request, a
	// replayed webhook, a redelivered event — and velocity that double-counted one
	// would freeze a customer for paying once.
	replay, err := planeObserve(asPeer(orgA), &contract.RiskObserveIn{
		Stage: cloud.StagePayment, Kind: contract.KindPayer, Subject: "u_split",
		Settlement: "sq_pay_2",
		Signals:    []contract.Signal{{Name: contract.SignalNano, Value: each}},
	})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if replay.Learned != 0 {
		t.Errorf("a replayed settlement learned %d, want 0 — the same money is counted twice "+
			"and the accrual describes our delivery guarantees rather than the payer", replay.Learned)
	}

	// THE ACCRUAL IS NOW REAL. The sixth payment's own value is unremarkable; what is
	// remarkable is the fifty-five thousand that already settled.
	out, err := planeDecide(asPeer(orgA), &contract.RiskDecideIn{
		Stage: cloud.StagePayment, Kind: contract.KindPayer, Subject: "u_split",
		Signals: []contract.Signal{{Name: contract.SignalNano, Value: each}},
	})
	if err != nil {
		t.Fatalf("planeDecide: %v", err)
	}
	if out.Refusal == "" {
		t.Fatal("the model answered with an opinion — this test is meaningless unless it is warming")
	}
	if out.Action != cloud.ActionReview {
		t.Fatalf("action %q, want %q — five settled payments of $11,000 accrued $55,000 past a "+
			"$50,000 examining bound and the sixth was allowed, which is the split-payment "+
			"typology the accrual exists to see", out.Action, cloud.ActionReview)
	}
	if !strings.Contains(out.Cause, causeAccrued) {
		t.Errorf("cause %q does not name the accrual", out.Cause)
	}
}

// TestObserve_ABurstSplitAcrossTwoDoorsAccruesOnOneSubject — the accrual half of the
// CROSS-ENDPOINT proof, over the shape the settling process actually states.
//
// commerce has ONE card money move and the binary opens TWO addresses onto it: the
// browser's top-up and the agent's typed payment (apps/commerce risk.go, payments.go).
// Only one of them used to be screened, and screening the second one is worth nothing
// unless the two ACCRUE TOGETHER — an attacker with a stolen card does not care which
// URL takes it, so a per-endpoint accrual halves every velocity bound just by
// alternating.
//
// This drives the plane the way the two endpoints drive it: six settlements naming ONE
// payer, alternating between the two endpoints' gateway references, each far under
// [reviewNano] so no point-in-time test can see any of them. What is remarkable is the
// sum.
//
// The commerce half — that both endpoints really do resolve one payer and state
// distinct keys — is proven at the endpoints, in
// [commerce.TestPayments_ABurstSplitAcrossBothDoorsIsOneAccrual].
//
// Mutation proof: namespace the observation by endpoint (prefix the subject, or the
// tenant, with which address it came from) and the final decide allows, because each
// half of the burst is then a history of its own with nothing over the bound.
func TestObserve_ABurstSplitAcrossTwoDoorsAccruesOnOneSubject(t *testing.T) {
	probe.reset(true)
	mountApp(t)
	p := mounted.State.plane
	holdFolds(t, p)
	k := key(t, brandA, orgA)
	arm(t, p, k)

	// $11,000 six times, alternating endpoints. The subject is ONE payer, because that
	// is what both endpoints resolve through the one payer rule.
	const each = "11000000000000"
	const payer = "acme"
	doors := []string{"sq_pay_browser_", "sq_pay_typed_"}
	for i := range 6 {
		out, err := planeObserve(asPeer(orgA), &contract.RiskObserveIn{
			Stage: cloud.StagePayment, Kind: contract.KindPayer, Subject: payer,
			Settlement: doors[i%2] + itoa(i),
			Signals:    []contract.Signal{{Name: contract.SignalNano, Value: each}},
		})
		if err != nil {
			t.Fatalf("observe settlement %d: %v", i, err)
		}
		if out.Learned != 1 {
			t.Fatalf("settlement %d learned %d, want 1 — a real payment deduplicated away", i, out.Learned)
		}
	}

	// IDEMPOTENT ACROSS THE ENDPOINTS TOO. One payment reached through both addresses
	// carries the gateway's SAME payment id (both endpoints return it out of one core),
	// so the second arrival is an inert replay rather than a second count.
	// Double-counting here would freeze a customer for paying once.
	replay, err := planeObserve(asPeer(orgA), &contract.RiskObserveIn{
		Stage: cloud.StagePayment, Kind: contract.KindPayer, Subject: payer,
		Settlement: "sq_pay_typed_1",
		Signals:    []contract.Signal{{Name: contract.SignalNano, Value: each}},
	})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if replay.Learned != 0 {
		t.Errorf("the same gateway payment arriving through the other endpoint learned %d, want 0 — "+
			"one payment is counted twice and the accrual describes our routing rather than "+
			"the payer", replay.Learned)
	}

	// AND THE BURST IS VISIBLE WHOLE. Sixty-six thousand accrued across two addresses,
	// past the examining bound, on the seventh ordinary-looking payment.
	out, err := planeDecide(asPeer(orgA), &contract.RiskDecideIn{
		Stage: cloud.StagePayment, Kind: contract.KindPayer, Subject: payer,
		Signals: []contract.Signal{{Name: contract.SignalNano, Value: each}},
	})
	if err != nil {
		t.Fatalf("planeDecide: %v", err)
	}
	if out.Refusal == "" {
		t.Fatal("the model answered with an opinion — this test is meaningless unless it is warming")
	}
	if out.Action != cloud.ActionReview {
		t.Fatalf("action %q, want %q — six settled payments of $11,000 split across the two credit "+
			"endpoints accrued $66,000 past a $50,000 examining bound and the seventh was allowed, "+
			"which is the same split-payment typology one endpoint at a time", out.Action, cloud.ActionReview)
	}
	if !strings.Contains(out.Cause, causeAccrued) {
		t.Errorf("cause %q does not name the accrual", out.Cause)
	}
}

// TestObserve_RefusesAnObservationItCannotConvergeOn. The settlement id is the whole
// of the idempotency, so an observation without one would take a random id and every
// retry would count the money again. It is refused rather than defaulted.
func TestObserve_RefusesAnObservationItCannotConvergeOn(t *testing.T) {
	probe.reset(true)
	mountApp(t)
	p := mounted.State.plane
	holdFolds(t, p)

	for _, settlement := range []string{"", "   "} {
		if _, err := planeObserve(asPeer(orgA), &contract.RiskObserveIn{
			Stage: cloud.StagePayment, Kind: contract.KindPayer, Subject: "u_1",
			Settlement: settlement,
			Signals:    []contract.Signal{{Name: contract.SignalNano, Value: "1000000000"}},
		}); err == nil {
			t.Errorf("an observation with settlement %q was accepted — its id would be random, "+
				"so every retry counts the same money again", settlement)
		}
	}
}

// TestObserve_CannotBePreEmptedThroughThePublicLearnDoor.
//
// The record deduplicates on (tenant, id) and the FIRST writer of an id wins. So an
// id this plane will later mint for itself is an id a caller can claim in advance,
// after which the plane's own observation lands as a duplicate — inert, silent, and
// exactly as clean-looking as an organisation with nothing to hide. At a self-serve
// credit endpoint the caller and the payer are the same party, so that is the velocity
// bound switched off by the thing it bounds.
//
// Mutation proof: delete the [reservedOf] guard in [riskEvent.observation] and the
// settlement below learns nothing, because the caller got there first.
func TestObserve_CannotBePreEmptedThroughThePublicLearnDoor(t *testing.T) {
	probe.reset(true)
	mountApp(t)
	p := mounted.State.plane
	holdFolds(t, p)
	k := key(t, brandA, orgA)

	// The public endpoint refuses every namespace this plane mints for itself, and says
	// which one — a silent rewrite would let the caller believe their id landed.
	for _, ns := range reserved {
		if _, err := (riskEvent{
			ID: ns + "guessed", Kind: kindPayer, Subject: "u_pre", Nano: 1_000_000_000,
		}).observation(time.Now().UTC()); err == nil {
			t.Errorf("the public learn endpoint accepted an id in this plane's own %q namespace", ns)
		}
	}

	// And the settlement it protects still lands, because nothing could have claimed
	// its id.
	out, err := planeObserve(asPeer(orgA), &contract.RiskObserveIn{
		Stage: cloud.StagePayment, Kind: contract.KindPayer, Subject: "u_pre",
		Settlement: "guessed",
		Signals:    []contract.Signal{{Name: contract.SignalNano, Value: "1000000000"}},
	})
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	if out.Learned != 1 {
		t.Fatalf("the settlement learned %d, want 1 — its id was already taken, so the payment "+
			"is untaught and nothing says so", out.Learned)
	}
	_ = k
}

// TestObserve_RefusesANegativeValue.
//
// The aggregates accrue a SUM and the stated value bounds are read off it, so a
// negative amount is not a small event — it is a subtraction from the finding. One
// taught event of minus fifty thousand cancels an hour of real payments, the
// converted accrual floors at zero, and the pace rule goes quiet with nothing to see.
// At a self-serve credit endpoint the payer IS the organisation, so that is
// self-suppression for the price of one authenticated call.
//
// Mutation proof: remove the sign check from [observe] and the accrual below reads
// zero and the rule allows.
func TestObserve_RefusesANegativeValue(t *testing.T) {
	probe.reset(true)
	p := newTestPlane(t)
	holdFolds(t, p)
	k := key(t, brandA, orgA)
	at := time.Now().UTC()

	if _, err := observe("neg", actor{Kind: kindPayer, Subject: "u_neg"}, -50_000, at); err == nil {
		t.Fatal("a negative value was admitted at the one entry point every bound is applied at")
	}
	// The wire path refuses it too, in its own words, because that is where a caller
	// reaches it.
	if _, err := (riskEvent{
		ID: "neg", Kind: kindPayer, Subject: "u_neg", Nano: -50_000 * nanoPerUSD,
	}).observation(at); err == nil {
		t.Fatal("the learn wire admitted a negative amount")
	}

	// And the accrual it would have erased survives: real payments past the examining
	// bound stay past it, because there is no observation that subtracts from them.
	batch := []observation{
		ob(t, "pay_1", kindPayer, "u_neg", 30_000, at.Add(-2*time.Minute)),
		ob(t, "pay_2", kindPayer, "u_neg", 30_000, at.Add(-time.Minute)),
	}
	if _, err := p.learn(k, batch...); err != nil {
		t.Fatalf("learn: %v", err)
	}
	seen, err := p.prior(k, ob(t, "judged", kindPayer, "u_neg", 100, at))
	if err != nil {
		t.Fatalf("prior: %v", err)
	}
	if got := onPace(seen); got.Action != cloud.ActionReview {
		t.Errorf("sixty thousand dollars of settled payments in two minutes returned %q (%q), "+
			"want %q", got.Action, got.Cause, cloud.ActionReview)
	}
}
