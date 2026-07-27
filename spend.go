package cloud

// spend.go answers ONE question for the whole binary — MAY THIS PRINCIPAL SPEND? —
// and it is the only place that answer is computed.
//
// WHY IT MOVED HERE. The answer used to live in clients/entitlements, a leaf that
// imports this package. That is the wrong side of the dependency: the edge filters
// serve.go mounts live in THIS package, so they could never reach it. The result was
// three gates that each re-derived a different answer:
//
//   - routers.Paywall (mounted app-wide) asked only "does the org hold a paid PLAN?".
//     It has no credit leg at all, so enabling it would have 402'd every prepaid
//     customer — the gate was unusable, which is why it stayed dark forever. Deleted;
//     serve.go now mounts SpendGate.
//   - middleware_billing.BillingGate (mounted app-wide) asked "is price(path) > 0?"
//     and DefaultPrice returns 0 for every path, so it never evaluated ANYTHING.
//   - clients/entitlements.RequireProduct had the RIGHT answer and was mounted on
//     no route at all.
//
// So the binary shipped three paywalls and enforced none. One predicate, in the one
// package every gate can import, is the fix.
//
// TWO WAYS TO PAY, ONE ANSWER. The rule is "an active subscription OR a positive
// prepaid balance". Those are two INDEPENDENT facts held by two INDEPENDENT
// authorities, and Stand is the single place they compose:
//
//   - SUBSCRIPTION — resolved by the CALLER, passed in as a Licence. The two callers
//     ask genuinely different questions of commerce ("is org X licensed for the
//     studio product?" vs "does org X hold any live paid plan?"), so the licence leg
//     is an ARGUMENT, not a call made here. Composing it is this file's job; asking
//     it is not.
//   - PREPAID CREDIT — the caller's wallet on the native finance ledger, read here
//     because there is exactly one right way to read it (see below).
//
// THE ADDRESS IS LOAD-BEARING. A money gate that reads a different wallet than the
// debit writes is the bug this codebase has already shipped three times, every time
// by keying the ORG POOL — see clients/principal/wallet.go, which lists them. Read on
// the pool, this predicate would admit every member of the shared signup org for as
// long as the platform's own pool is funded, which is a total bypass and is precisely
// the live free-inference hole (apps/zen.go still gates the pool). So the credit leg
// reads principal.WalletOf's address and nothing else.
//
// CREDIT IS EXACT. finance.Balance returns money.Amount — 18-decimal atto-USD over
// big.Int. The admit boundary is Sign() > 0: zero denies, ONE ATTO admits. No
// threshold, no cent-flooring, no float anywhere on this path.
//
// PROOF, NOT ABSENCE. A caller is Unpaid only when BOTH authorities ANSWERED and both
// said no. If either could not answer, the standing is Unknown — this file never
// converts "the oracle is down" into "this customer has not paid". What to DO about
// an Unknown is enforcement's decision (middleware_spend.go), not the decide's.

import (
	"context"
	"strings"

	"github.com/hanzoai/cloud/clients/finance"
	"github.com/hanzoai/cloud/clients/principal"
)

// creditUnit is the asset the prepaid wallet is denominated in. One asset ships
// (USD, 18-decimal-exact); finance selects the ledger file from it.
const creditUnit = "usd"

// ── the subscription leg (supplied by the caller) ───────────────────────────────

// Licence is a subscription authority's ANSWER about one caller, already resolved.
// The zero value is LicenceUnknown, which is the honest default: a question that
// could not be asked has no answer, and "no answer" is never "not licensed".
type Licence uint8

const (
	// LicenceUnknown — the authority could not answer (commerce absent, query
	// failed, nil result). NOT a statement about the caller.
	LicenceUnknown Licence = iota
	// LicenceNone — the authority answered: no live subscription.
	LicenceNone
	// LicenceActive — the authority answered: a live subscription.
	LicenceActive
)

// ── the answer ──────────────────────────────────────────────────────────────────

// Standing is a caller's resolved commercial standing. The zero value is Unknown,
// which is the honest default: a question not yet asked has no answer, and "no
// answer" is never "has not paid".
type Standing uint8

const (
	// Unknown — at least one authority could not answer (commerce unreachable,
	// ledger unreadable, org unresolvable). NOT a statement about the caller.
	Unknown Standing = iota
	// Unpaid — BOTH authorities answered and both said no: no subscription and no
	// credit. The only proven refusal.
	Unpaid
	// Subscribed — a live subscription admits the caller.
	Subscribed
	// Funded — the wallet holds a positive prepaid balance to burn down.
	Funded
)

// Admits reports whether this standing lets a request through on its own merits.
// Unknown does NOT admit here — it is not an admit, it is an absence, and the
// posture that resolves it is enforcement's, deliberately not hidden inside this
// predicate.
func (s Standing) Admits() bool { return s == Subscribed || s == Funded }

// String renders the standing for logs.
func (s Standing) String() string {
	switch s {
	case Unpaid:
		return "unpaid"
	case Subscribed:
		return "subscribed"
	case Funded:
		return "funded"
	default:
		return "unknown"
	}
}

// Stand composes the two independent legs into the one answer.
//
//	lic — the subscription authority's already-resolved answer.
//	w   — the money address the credit leg reads (principal.WalletOf).
//
// The legs are evaluated cheapest-decisive-first: a licensed caller never touches
// the ledger. A caller with no subscription always does — that is the pay-as-you-go
// path, and it is the common case for a prepaid customer.
func Stand(ctx context.Context, lic Licence, w principal.Wallet) Standing {
	if lic == LicenceActive {
		return Subscribed
	}
	creditOK, funded := creditIn(ctx, w)
	if creditOK && funded {
		return Funded
	}
	if lic == LicenceNone && creditOK {
		// Both authorities answered; both said no. This — and only this — is proof.
		return Unpaid
	}
	return Unknown
}

// creditIn reads the caller's prepaid balance from the native finance ledger and
// reports whether it is positive. ok is false when the ledger is not published
// (split deploy / money layer not co-resident), when the request carries no wallet,
// or when the read FAILS — "a balance that cannot be read is unknown, never zero"
// (clients/finance.Balance). A zero balance IS an answer: (true, false).
//
// The read is against the LIVE books (test=false). Sandbox money must never buy
// live product access.
func creditIn(ctx context.Context, w principal.Wallet) (ok, funded bool) {
	if w.Account == "" {
		return false, false
	}
	fin := finance.Current()
	if fin == nil {
		return false, false
	}
	bal, err := fin.Balance(ctx, w.Ledger, w.Account, creditUnit, false)
	if err != nil {
		return false, false
	}
	return true, bal.Sign() > 0
}

// ── billable(path) — split OUT of price(path) ───────────────────────────────────

// Billable reports whether a request CONSUMES resource somebody has to pay a
// provider for, and therefore requires standing before it runs.
//
// THIS IS THE BUG THE SPLIT EXISTS TO KILL. Authorization and pricing were one
// int64: DefaultPrice returned the edge charge, and BillingGate read `cents <= 0` as
// "do not gate". Every LLM path prices at 0 there — deliberately, because the ai and
// zen subsystems meter their own token costs and an edge charge would double-bill —
// so "we charge nothing HERE" silently meant "we authorize NOTHING here", and the
// same fusion made every resource kind an operator prices at 0 un-gated as well
// (ResourceMeter.Gate: `costCents <= 0 -> nil`). One int64 answering two questions is
// why the LLM leak and the non-LLM gap are a single bug. They are two questions now:
// Billable says WHETHER standing is required, DefaultPrice says WHAT the edge charges.
// Pricing something at zero can no longer un-authorize it.
//
// READS ARE NEVER BILLABLE. GET/HEAD/OPTIONS pass unconditionally — gating reads
// already caused one outage, a balance view that 402s is unusable, and no read this
// binary serves calls a paid provider.
//
// The path sets are MEASURED, not invented:
//   - inference — the exact paths zen's Claim owns (zen@v1.4.2 proxy.go) plus ai's
//     own tree. These are the free-inference hole.
//   - meteredTrees — the subsystems that construct a ResourceMeter (the non-LLM
//     auth-not-balance gap). /v1/commerce/ and /v1/o11y/ are in BillingGate's
//     selfMeteredPrefixes but are deliberately NOT here: one is the pay path itself
//     and the other is telemetry ingest, and neither spends a provider's money.
func Billable(method, path string) bool {
	switch method {
	case "GET", "HEAD", "OPTIONS":
		return false
	}
	if Reachable(path) {
		return false // the path to payment is never gated. Ever.
	}
	if inference[path] {
		return true
	}
	for _, t := range meteredTrees {
		if path == strings.TrimSuffix(t, "/") || strings.HasPrefix(path, t) {
			return true
		}
	}
	return false
}

// inference is the exact set of bare completion endpoints. zen's Claim owns
// /v1/messages, /v1/chat/completions, /v1/chat and /v1/completions (zen proxy.go);
// /v1/embeddings and /v1/responses fall through to ai's catch-all. Both families
// call a paid upstream, so both need standing regardless of which one serves.
var inference = map[string]bool{
	"/v1/messages":         true,
	"/v1/chat/completions": true,
	"/v1/chat":             true,
	"/v1/completions":      true,
	"/v1/embeddings":       true,
	"/v1/responses":        true,
}

// meteredTrees are the subsystem trees whose handlers debit a ledger — every
// NewResourceMeter construction site, plus ai's own tree. Written as a root WITH its
// trailing slash; the bare root matches too.
var meteredTrees = []string{
	"/v1/ai/",         // LLM token costs (ai self-meters).
	"/v1/agents/",     // per-run agent fee.
	"/v1/agent/",      // the agent orchestrator's round.
	"/v1/mcp/",        // per-tool dispatch.
	"/v1/functions/",  // serverless invoke.
	"/v1/s3/",         // object-storage data plane.
	"/v1/storage/",    // clients/storage NewResourceMeter(deps, "s3").
	"/v1/ml/",         // clients/ml NewResourceMeter(deps, "compute").
	"/v1/visor/",      // clients/visor NewResourceMeter(deps, "compute").
	"/v1/security/",   // clients/security scan fee.
	"/v1/projects/",   // clients/projects hosting fee.
	"/v1/cloudflare/", // clients/cloudflare provisioning.
}

// ── the invariant: the path to payment is never gated ───────────────────────────

// Reachable reports whether a path must be served REGARDLESS of standing.
//
// This is not a scope selector — which routes a gate guards is decided by where it is
// applied. It is a SAFETY PROPERTY: a customer who has not yet paid must still be
// able to reach the paths that let them pay, and a lapsed one must be able to cure
// their own lapse. Gate those and you deadlock every prospect and every lapsed
// customer at once — a self-inflicted total revenue stop that looks like success in a
// naive test, because everything returns 402. So the list lives INSIDE the predicate:
// no future wiring mistake can gate the pay path, whatever it wraps.
//
// Gating too little is a revenue leak we fix next week. Gating too much is an outage.
// The list is therefore deliberately generous, and anything ambiguous belongs on it.
//
// Traced from the console subscribe flow (console src/components/products/
// PlansModule.tsx -> src/lib/api/plans.ts): PlansApi.plans() reads /v1/billing/plans
// through the per-tenant billing proxy, and checkout drives the /v1/billing/* money
// surface — which also carries the INBOUND PROVIDER WEBHOOKS at
// /v1/billing/webhooks/:provider. Gating an inbound payment webhook loses payments
// outright, so that prefix is load-bearing twice over.
func Reachable(path string) bool {
	// Liveness / readiness / metrics — a gate must never hide whether the process is
	// up, or an incident becomes invisible at exactly the wrong moment.
	switch path {
	case "/health", "/healthz", "/readyz", "/livez", "/metrics":
		return true
	}
	if strings.HasSuffix(path, "/health") {
		return true // the per-subsystem HIP-0106 probe, /v1/<name>/health.
	}
	if !strings.HasPrefix(path, "/v1/") {
		return true // the SPA shell + static assets that render the paywall screen itself.
	}
	switch path {
	case "/v1/signin", // auth: session bootstrap (the console posts the OAuth code here).
		"/v1/signout",
		"/v1/get-account",  // auth: the account read AuthGate loads before anything else.
		"/v1/entitlements": // the paywall's OWN projection — what the shell renders upgrade UI from.
		return true
	}
	for _, sub := range reachableTrees {
		// A sub-tree covers its own ROOT as well as everything under it. Matching only
		// "<root>/" is how a paywall ends up refusing the exact URL its own 402 points
		// at — one missing slash and the cure is behind the gate. One rule, both forms.
		if path == strings.TrimSuffix(sub, "/") || strings.HasPrefix(path, sub) {
			return true
		}
	}
	return false
}

// reachableTrees are the /v1 sub-trees a spend gate may never refuse. Every entry is
// written as a root WITH its trailing slash and covers the bare root too.
var reachableTrees = []string{
	"/v1/billing/",      // the whole money surface: subscribe, top up, AND the inbound provider webhooks.
	"/v1/commerce/",     // the co-resident commerce plane the checkout and tenant reads drive.
	"/v1/iam/",          // IAM login / OAuth token exchange / .well-known OIDC discovery.
	"/v1/account/",      // the account surface the shell renders before any purchase decision.
	"/v1/admin/",        // platform sudo — including the cockpit that holds this gate's kill switch.
	"/v1/plans/",        // the plans catalog — WHAT to buy (@hanzo/plans) — and its sub-routes.
	"/v1/models/",       // the model catalog the shell reads for discovery, and /v1/models/:id.
	"/v1/orgs/",         // org read + switch: the shell must resolve which org it is buying for.
	"/v1/waitlist/",     // admission's join API — an un-admitted user must still reach it.
	"/v1/flags/",        // the guard's public mode read; also how the kill switch is observed.
	"/v1/entitlements/", // per-org enablement reads/writes that sit beside the projection.
}
