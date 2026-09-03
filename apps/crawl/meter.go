package crawl

// meter.go — who pays for a render.
//
// Fetching a page is one http.Get and stays free: it costs a socket and a
// parse, and pricing a read is how a directory listing ends up billed like the
// work it lists. Rendering one is a different act. It leaves this process for a
// headless Chromium that boots a browser, executes the page's scripts and holds
// a pod for as long as forty-five seconds (browser.go) — dedicated compute, of
// the same class the fleet already meters for studio renders and ML predicts,
// and reached through a credential (CRAWL_API_TOKEN) that names a service with
// a capacity somebody buys.
//
// The surface declared cloud.Free, so none of that was authorized or recorded:
// an org at zero balance could escalate every fetch it made, and a page that
// renders is exactly the page an unbounded caller wants. What was missing was
// not only the debit but the STANDING check the declaration switches on — see
// Billable in spend.go, which reads Price and had been told this costs nothing.
//
// A REFUSAL KEEPS THE STATIC PAGE. Escalation has always been best-effort —
// "the render failed must never turn a page we already have into an error" — and
// a caller who cannot cover the render is one more way for it not to happen. So
// the fetch still answers, with whatever the static read produced. Only a page
// that could be read NO other way surfaces the refusal, and it surfaces as the
// reason, in the words the caller can act on.

import (
	"context"
	"sync/atomic"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/metering"
)

// feeEnv is the operator knob for what one render costs: CRAWL_FEE_CENTS_RENDER,
// or CRAWL_FEE_CENTS for every billed act on this surface.
const feeEnv = "CRAWL_FEE_CENTS"

// kind is the billed unit within the product — the act being charged for, which
// is the RENDER and never the fetch beside it.
const kind = "render"

// defaultFeeCents is one cent per rendered page.
//
// A policy default, and a deliberately small one: this is seconds of browser on
// a pod we run, not a per-call invoice from a vendor, so it is priced like the
// compute it is rather than at the platform's $1.00 provision fee — which is
// sized for creating a database and would make reading one page cost more than
// the answer built from it. Operators move it with the knob above; 0 makes
// rendering free again, and un-gated with it.
const defaultFeeCents int64 = 1

// meter is the per-org gate and debit, bound once by Mount.
//
// Package-level, and an atomic pointer, for exactly the reason the archive
// beside it is (see Bind): a render is reached from Read, which the answer
// engine's read stage calls in-process with no deps to thread, and Mount runs at
// boot while other subsystems are already serving.
var meter atomic.Pointer[cloud.Meter]

// bindMeter installs the process-wide meter. A nil meter leaves crawling fully
// functional and unbilled — Meter's own contract is that an absent
// ledger allows, which is the right posture for a deployment with no commerce.
func bindMeter(m *cloud.Meter) { meter.Store(m) }

// fee is what one render costs the caller.
func fee() int64 { return cloud.FeeCents(feeEnv, kind, defaultFeeCents) }

// Who pays is [cloud.PayerOf], read off the request so a caller can never name
// the wallet that pays. Two callers arrive with no wallet by design, and both are
// named in this package already: the shared-service-key caller, which scopeOf
// gives the shared corpus because it acts on nobody's behalf, and the in-process
// reader behind /v1/ask, which charges its own per-answer fee. Neither is gated
// and neither is debited.

// afford authorizes one render BEFORE the browser is asked, so a caller who
// cannot cover it is refused having spent nothing. It answers the refusal, which
// browse returns as its error — and every caller of browse already treats an
// error as "keep the static page".
// NO REQUIRE-PAYER RULE HERE, and the asymmetry with websearch is deliberate.
//
// There, an unattributable caller is refused the paid ENGINES because asking one
// sends money out of the company per query, and the caller still gets a real
// answer from the keyless engines beside it. Neither half holds here. A render
// runs on a pod we already have, whose bill arrives by the node-hour whether or
// not this page is rendered — so refusing saves nothing on any invoice — and the
// fallback is the static page, which for the single-page apps escalation exists to
// rescue is an empty shell. The rule would idle our own capacity to return a worse
// answer.
//
// What is left arriving with no wallet, now that a detached composer carries its
// caller, is the shared-service-key scrape: an internal caller with no tenant,
// which this platform intends to serve. Rate is the right control for that, and
// the shared key in front of it is already that control. Amplification is not a
// billing question wearing a different hat.
func afford(ctx context.Context, p cloud.Payer) (*cloud.Charge, error) {
	return meter.Load().Reserve(ctx, p, kind, fee())
}

// charge debits one render, after the browser has actually produced a page.
//
// After, and only on success: a render that returned nothing gave the caller
// nothing, and every one of those paths falls back to the static page. This is
// the ordinary rule that failed work is not billed, and it holds here because
// the work is ours — there is no vendor invoice to keep level with.
func charge(ch *cloud.Charge) {
	// Ref is left unset so the meter mints one: it names an ACT, and keyed on the
	// URL a page rendered twice would move money only once.
	ch.Debit(metering.Usage{Model: kind, AmountCents: fee()})
}
