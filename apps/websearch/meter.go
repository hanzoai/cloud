package websearch

// meter.go — who pays when a search leaves the free tier.
//
// Most of this package scrapes public result pages and costs nothing. Two
// engines do not: Brave sells a subscription (brave.go) and Mojeek sells an API
// against a prepaid balance (mojeek_api.go, which reads back "denied due to
// insufficient balance" when it runs out). Both spend a vendor's money per
// query, and the surface declared cloud.Free — so the edge charged nothing, the
// standing gate did not apply, and nothing downstream debited anybody. The
// vendor invoice was the only record that the calls had happened.
//
// A CREDENTIAL IS WHAT MAKES AN ENGINE PAID, so it is what this file keys on. An
// engine with no key held is unavailable (Brave) or falls back to its scrape
// (Mojeek); either way nothing is bought and nothing is owed. That makes the
// keyless deployment — the free tier of this product — byte-for-byte unchanged
// by everything here.
//
// OUT OF FUNDS DROPS THE PAID ENGINES; IT DOES NOT FAIL THE SEARCH. That follows
// the rule the rest of the package already keeps: an engine that cannot answer
// contributes zero and the request is still served by whatever else is enabled.
// It is also the honest posture for money — we decline to spend a vendor's money
// on a caller who cannot cover it, and the caller gets exactly what a keyless
// deployment gets. mojeek_api.go states the same tier model from the other side:
// "an engine that stops working when a bill goes unpaid is an outage, not a
// downgrade." So the failure is closed on SPEND and open on ANSWER.

import (
	"context"
	"sync/atomic"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/metering"
)

// feeEnv is the operator knob for what one search over a paid engine costs:
// WEBSEARCH_FEE_CENTS_BRAVE / _MOJEEK per engine, WEBSEARCH_FEE_CENTS for both.
const feeEnv = "WEBSEARCH_FEE_CENTS"

// defaultFeeCents is one cent per paid-engine answer.
//
// It is a policy default sized against the vendors' own per-query list prices —
// both sell thousands of queries for single-digit dollars, so a cent covers a
// call with room — and not a claim about what search is worth. The scale matches
// the neighbouring product decision: answer's `search` mode is 2c for a search
// plus a synthesis. Operators move it with the knob above; 0 makes a paid engine
// free again, and un-gated with it (Meter.Authorize's own costCents<=0 rule).
const defaultFeeCents int64 = 1

// meter is the per-org gate and debit, bound once by Mount.
//
// Package-level for the reason the logger beside it is (see setLogger): the
// three entry points into search — the two handlers here and compose.go's
// in-process caller — do not all have a meter to pass, and metaSearch is a pure
// function over the enabled engines that every one of them reaches. An atomic
// pointer rather than a plain var because Mount runs at boot while other
// subsystems are already serving, exactly as crawl's archive is bound.
var meter atomic.Pointer[cloud.Meter]

// bindMeter installs the process-wide meter. A nil meter leaves search fully
// functional and unbilled, which is what an unconfigured deployment should get:
// Meter's own contract is that an absent ledger allows.
func bindMeter(m *cloud.Meter) { meter.Store(m) }

// paid reports whether asking this engine spends a vendor's money HERE — the
// engine sells its API and this deployment holds the key. Keyless engines and
// unfunded credentials both answer false, so the free tier is never charged.
func paid(e engine) bool {
	switch e.name {
	case braveName:
		return braveKey() != ""
	case mojeekName:
		return mojeekKey() != ""
	}
	return false
}

// fee is what one answer from engine costs the caller.
func fee(engine string) int64 { return cloud.FeeCents(feeEnv, engine, defaultFeeCents) }

// renderFee is what one browser render costs. It reads the CRAWL knob, not this
// package's, because it is the same pod apps/crawl prices and an operator setting
// a render price must not have to set it twice.
func renderFee() int64 { return cloud.FeeCents("CRAWL_FEE_CENTS", render, defaultFeeCents) }

// affordRender authorizes one browser render before the pod is asked for it. A
// caller with no wallet is not billed and not gated — the render is our own
// capacity, and the asymmetry with the paid engines is argued in apps/crawl/meter.go.
func affordRender(ctx context.Context) (*cloud.Charge, error) {
	return meter.Load().Reserve(ctx, cloud.PayerOf(ctx), render, renderFee())
}

// chargeRender debits one render, after the browser has actually returned a page.
func chargeRender(ch *cloud.Charge) {
	ch.Debit(metering.Usage{Model: render, AmountCents: renderFee()})
}

// Who pays is [cloud.PayerOf]: the wallet, the project scope a per-scope cap
// sums over, and the attribution a ledger row carries, all read off the request
// so a caller can never name the wallet that pays. A caller with no wallet — the
// in-process composer behind /v1/ask, which charges its own per-answer fee, and
// the shared-service-key caller — is neither gated nor billed, and that case
// lives in cloud.Payer rather than being decided again here.

// afford returns the engines this search may actually ask, refusing only the
// paid ones the caller cannot cover. The free engines are never gated: they cost
// nobody anything, and gating a read that spends nothing is how a balance view
// ends up 402ing.
//
// The whole paid set is authorized as ONE amount rather than engine by engine,
// because they run concurrently and a balance that covers each of them
// separately need not cover both together.
func afford(ctx context.Context, engs []engine) ([]engine, *cloud.Charge) {
	var total int64
	for _, e := range engs {
		if paid(e) {
			total += fee(e.name)
		}
	}
	if total <= 0 {
		return engs, nil // nothing here is bought, so there is nothing to authorize.
	}
	// NO PAYER, NO PURCHASE. Everywhere else an absent wallet means "nobody to
	// bill", and the surface proceeds — correct when the act costs us nothing. Here
	// it would mean buying a vendor's query for a caller we cannot name or charge,
	// which is the platform paying for work it cannot attribute. Two callers arrive
	// this way: the agent tool dispatcher, which manufactures its own context and
	// carries no identity at all, and any in-process composer that has not carried
	// its caller across a detach.
	//
	// So they get exactly what an out-of-funds caller gets, and what a deployment
	// holding no key gets: the keyless engines. Search still answers; only the
	// bought tier is withheld. That is this package's own tier model, applied to
	// the one question it had not been asked.
	p := cloud.PayerOf(ctx)
	if !p.Billable() {
		return free(engs), nil
	}
	// The WHOLE paid set is authorized as one amount: they run concurrently, and a
	// balance that covers each of them separately need not cover both together.
	ch, err := meter.Load().Reserve(ctx, p, kind, total)
	if err == nil {
		return engs, ch
	}
	return free(engs), nil
}

// free is the keyless subset — what search degrades to whenever the bought tier
// is unavailable, for whatever reason. One answer, so "out of funds", "no key" and
// "nobody to bill" cannot drift into three different products.
func free(engs []engine) []engine {
	out := make([]engine, 0, len(engs))
	for _, e := range engs {
		if !paid(e) {
			out = append(out, e)
		}
	}
	return out
}

// The two billed units on this surface. A search is the answer a caller asked for;
// a render is a browser held on the crawl pod to read ONE engine's result page
// when the static fetch parsed to nothing.
//
// A RENDER IS ITS OWN ACT, at its own price, because it is its own cost. It fires
// per ENGINE per search when WEBSEARCH_RENDER is on, so a single query can hold
// the pod several times over — and it fires for the keyless engines too, which
// paid() correctly reports free: the ENGINE costs nothing and the browser does.
// Reading the same pod through a second entry point does not make it cheaper, and
// declaring this surface Metered for its search fee never covered it.
const (
	kind   = "search"
	render = "render"
)

// charge debits one fee per paid engine that ANSWERED.
//
// ANSWERED, not "was asked". A paid engine that came back `failed` — the shape
// an exhausted balance or a quota refusal takes — served the caller nothing, and
// what they read came from the free engines beside it. brave.go states the
// pricing rule this implements: the customer pays for the ANSWER and not for our
// upstream call, which is why a CACHED answer is billed exactly like a fresh one
// (the cache reports `answered`, and what we save by not re-asking is margin).
// A reader tempted to make the cache a discount would be changing the product,
// not fixing a pass-through.
func charge(ch *cloud.Charge, answers []answer) {
	var total int64
	for _, a := range answers {
		if a.outcome == answered && paid(engineByName[a.engine]) {
			total += fee(a.engine)
		}
	}
	if total <= 0 {
		return // the charge's own Release, deferred by the caller, gives the hold back.
	}
	// Ref is left unset so the meter mints one: it is the ledger's idempotency
	// key and it names an ACT, so keying it on the query would move money only
	// the first time anybody searched a given word.
	ch.Debit(metering.Usage{Model: kind, AmountCents: total})
}
