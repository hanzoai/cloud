package entitlements

// require.go is the ENTITLEMENT (commerce) side of this package — the SERVER-SIDE
// half of the unified Hanzo paywall. It is deliberately distinct from the
// ENABLEMENT store in entitlements.go (per the package doctrine "TWO AUTHORITIES,
// NEVER BRAIDED"): the store records the org's INTENT (which products it toggled
// on); this file ENFORCES the billing truth (whether the caller may use the product
// at all) on the live data plane, and projects it for the console shell
// (projection.go).
//
// It applies a verdict; it does not compute one. WHAT the caller's standing is
// lives in standing.go (subscription OR prepaid credit, resolved from the two
// authorities). This file decides only WHETHER and HOW to refuse: the admin kill
// switch, the invariant that the path to payment stays reachable, and the shape of
// the 402. Policy is never restated here — the plan→product rule lives once, in
// @hanzo/plans, resolved by commerce.CheckEntitlement.

import (
	"net/http"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/flags"
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/zap-proto/zip"
)

// appProducts is the canonical set of console "app" products the unified paywall
// projects, in the fixed key order the @hanzogui/shell useEntitlement hook reads.
// Each element is the REAL @hanzo/plans product id CheckEntitlement expects — the
// BARE id (commerce prepends "licensing.product:" itself), verified against
// @hanzo/plans v1.4.4 licensing.product_ids. This is the ONE source of the app-key
// set so the projection and any future consumer never re-list it.
//
// NOTE: "admin" is intentionally NOT here. It is not a commerce product — it is the
// platform-sudo predicate (principal.IsSuperAdmin / c.IsAdmin), strictly tighter
// than any purchasable tier — so the projection resolves it separately and it is
// never handed to CheckEntitlement.
var appProducts = []string{"studio", "bot", "world", "platform", "team"}

// ── admin switches (the ONE flag engine — clients/flags) ────────────────────────

// enforceKey is the kill switch, and it is the entire safety story for a change
// that can otherwise lock out every paying customer. OFF (the default) makes
// RequireProduct a pure passthrough that consults NO authority at all, so the gate
// ships dark, is flipped on under observation from the /v1/admin/flags cockpit, and
// can be killed INSTANTLY from that same cockpit if it misfires — no redeploy, hot
// within one eval TTL.
// enforceKey is the shared platform-switch key: the root package owns the string so
// the registry entry here, the evaluator, and the edge in serve.go cannot drift.
const enforceKey = cloud.SwitchPaywallEnforced

// strictKey is the posture on an UNRESOLVABLE standing (see the fail-posture note
// on RequireProduct). OFF = availability: an authority we cannot reach never
// refuses a customer. ON = revenue: an unresolvable standing refuses. It exists so
// the choice between those two is made live, by an owner watching real traffic,
// instead of being frozen into a constant at build time.
const strictKey = "paywall_strict"

func init() {
	flags.Register(flags.Def{
		Key: enforceKey, Category: "Launch", Type: flags.TypeBool, Default: "false",
		// PAYWALL_ENFORCED remains the fallback, so a deployment with no cockpit
		// write behaves exactly as it did before this switch governed anything.
		// The first write in admin.hanzo.ai takes over and hot-applies.
		Env:   "PAYWALL_ENFORCED",
		Label: "Paywall enforced",
		Desc: "Refuse gated product routes for a caller with neither an active subscription nor prepaid credit. " +
			"OFF = dark: the gate admits everything and consults no billing authority. This is the kill switch — " +
			"turning it off instantly restores full access platform-wide.",
	})
	flags.Register(flags.Def{
		Key: strictKey, Category: "Launch", Type: flags.TypeBool, Default: "false",
		Label: "Paywall strict (refuse when billing is unreadable)",
		Desc: "When the billing authorities cannot answer (commerce or the finance ledger down), ON refuses the " +
			"request and OFF admits it. OFF trades a bounded free-access window during an incident for never " +
			"locking a paying customer out; turn ON only while deliberately holding the line on revenue.",
	})
}

// Enforced reports whether the paywall is on, read live from the cockpit switch
// (store -> PAYWALL_ENFORCED -> false). It is the ONE reader of enforceKey outside
// this file's own gate: serve.go hands it to routers.Paywall so the middleware and
// RequireProduct can never disagree about whether enforcement is on.
func Enforced() bool { return flags.Bool(enforceKey) }

// ── the gate ────────────────────────────────────────────────────────────────────

// RequireProduct is the unified-paywall enforcement middleware a gated route group
// applies. It admits the caller when EITHER commercial leg holds — an ACTIVE
// @hanzo/plans entitlement for `product`, OR a positive prepaid balance to burn
// down — deferring entirely to the two authorities that own those facts
// (commerce.CheckEntitlement and the finance ledger). Both legs compose in ONE
// place, standing.Resolve; this middleware only applies the verdict.
//
// DECISION ORDER — a request is ADMITTED unless the one proven refusal fires:
//
//  1. enforcement OFF ....................... admit. The kill switch, read FIRST so
//     a dark gate costs one map lookup and touches no authority.
//  2. reachable path ........................ admit, unconditionally. The path to
//     payment is never gated (see `reachable`).
//  3. unvalidated principal ................. 403. An identity refusal, never an
//     infra one — there is no trustworthy org to gate on, and the restored
//     X-Org-Id on the bearer-less path is a forge. Mirrors principal.Org's
//     contract, which every other data-plane gate in this binary already applies.
//  4. platform super-admin .................. admit. Platform sudo is strictly
//     tighter than any purchasable tier; it is not a product and never 402s.
//  5. subscribed OR funded .................. admit.
//  6. UNRESOLVABLE standing ................. posture (`paywall_strict`), logged.
//  7. proven unpaid ......................... 402 with the actionable refusal.
//
// FAIL POSTURE — argued, not inherited. The refusal in (7) requires PROOF: both
// authorities answered and both said no (standing.Resolve). An authority we could
// not reach yields Unknown, and by default an Unknown ADMITS. That is not "unpaid
// users get everything free whenever commerce hiccups" — it is "we never call a
// customer delinquent on evidence we do not have". The asymmetry is what decides it:
// refusing on an outage 402s every PAYING customer at once (a total product outage,
// a support and churn event, irreversible), while admitting on an outage leaks
// access for the duration of that outage (bounded, recoverable, and separately
// capped — actual spend still runs through the metering gate and the rolling
// AI-spend cap, which have their own fail-closed posture). Every fail-open admit
// logs at WARN with the reason, so a sustained leak is a paging condition rather
// than a silent one. And when an owner wants the other trade, `paywall_strict`
// flips it live — the posture is a decision made under observation, not a constant.
//
// Apply it ONLY to route groups whose routes are ALL authenticated: step (3) refuses
// an unvalidated caller, so a genuinely public route must never be wrapped.
func RequireProduct(commerce cloud.CommerceClient, product string) zip.Handler {
	return func(c *zip.Ctx) error {
		if !flags.Bool(enforceKey) {
			return c.Next() // dark — byte-identical to no middleware at all.
		}
		if reachable(c.Path()) {
			return c.Next()
		}
		if !principal.Validated(c) {
			return zip.ErrForbidden("no validated principal")
		}
		if principal.IsSuperAdmin(c) {
			return c.Next() // platform sudo — tighter than any tier, never purchasable.
		}
		org, ok := principal.Org(c)
		if !ok {
			return zip.ErrForbidden("no validated principal")
		}
		// The credit leg reads the address the DEBIT writes (principal.WalletOf), not
		// the org pool — reading the pool would admit every member of the shared signup
		// org against the platform's own balance. A caller with no resolvable wallet
		// simply has no credit leg; the subscription leg still answers.
		w, _ := principal.WalletOf(c)
		switch s := Resolve(c.Context(), commerce, org, product, w); {
		case s.Admits():
			return c.Next()
		case s == Unknown:
			if flags.Bool(strictKey) {
				c.Log().Warn("paywall: standing unresolvable; refusing (strict)", "product", product, "org", org)
				return refuse(c, product, reasonUnresolved)
			}
			c.Log().Warn("paywall: standing unresolvable; admitting", "product", product, "org", org)
			return c.Next()
		default:
			// The ONE proven refusal: no plan licenses the product AND the wallet is empty.
			c.Log().Info("paywall: no subscription and no credit", "product", product, "org", org, "standing", s.String())
			return refuse(c, product, reasonUnpaid)
		}
	}
}

// ── the refusal ─────────────────────────────────────────────────────────────────

// The two reasons a 402 can carry. `unpaid` is the proven refusal; `unresolved` is
// the strict-posture refusal, and it is a DISTINCT code because the caller's cure is
// different — an unpaid caller must buy something, an unresolved one must retry.
const (
	reasonUnpaid     = "unpaid"
	reasonUnresolved = "unresolved"
)

// Refusal is the 402 body. A bare 402 is useless to the console shell, so this names
// WHAT is gated, WHY, and every way to cure it — one Cure per admit leg, so the body
// is the structural mirror of the gate and can never drift from it.
//
// The cure paths are RELATIVE and same-origin, deliberately: a hard-coded
// cloud.hanzo.ai would brand a Lux / Zoo / Pars deployment as Hanzo, and this binary
// white-labels by host. They point at the two API surfaces the shell already reads
// and that `reachable` guarantees are never gated — /v1/plans (what to buy, with
// prices, from @hanzo/plans) and /v1/billing (where to pay, subscribe or top up).
type Refusal struct {
	Error   string `json:"error"`   // stable machine code: always "payment_required"
	Product string `json:"product"` // the gated @hanzo/plans product id
	Reason  string `json:"reason"`  // "unpaid" | "unresolved"
	Message string `json:"message"` // one human sentence
	Cure    []Cure `json:"cure"`    // the ways to fix it, in the order to offer them
}

// Cure is one way out of a 402: Kind names the admit leg it satisfies
// ("subscribe" | "credit"), URL where to do it.
type Cure struct {
	Kind string `json:"kind"`
	URL  string `json:"url"`
}

// cures are the two ways to cure a refusal — exactly the two legs standing.Resolve
// admits on, in the order the console should offer them (a subscription is the
// steady state; prepay is the no-commitment path).
var cures = []Cure{
	{Kind: "subscribe", URL: "/v1/plans"},
	{Kind: "credit", URL: "/v1/billing"},
}

func refuse(c *zip.Ctx, product, reason string) error {
	msg := "this org has no active subscription for " + product + " and no prepaid credit"
	if reason == reasonUnresolved {
		msg = "billing could not be verified for " + product + "; retry shortly"
	}
	return c.JSON(http.StatusPaymentRequired, Refusal{
		Error:   "payment_required",
		Product: product,
		Reason:  reason,
		Message: msg,
		Cure:    cures,
	})
}

// ── the invariant: the path to payment is never gated ───────────────────────────

// reachable reports whether a path must be served REGARDLESS of standing.
//
// This is not a scope selector — which routes this middleware guards is decided by
// where it is applied. It is a SAFETY PROPERTY of the gate: a customer who has not
// yet paid must still be able to reach the paths that let them pay, and a lapsed one
// must be able to cure their own lapse. Gate those and you deadlock every prospect
// and every lapsed customer at once — a self-inflicted total revenue stop that looks
// like success in a naive test, because everything returns 402. So the list lives
// INSIDE the gate: no future wiring mistake can gate the pay path, whatever it wraps.
//
// Gating too little is a revenue leak we fix next week. Gating too much is an outage.
// The list is therefore deliberately generous, and anything ambiguous belongs on it.
//
// Traced from the console subscribe flow (console src/components/products/
// PlansModule.tsx → src/lib/api/plans.ts): PlansApi.plans() reads /v1/billing/plans
// through the per-tenant billing proxy, and checkout drives the /v1/billing/* money
// surface (subscribe/subscriptions/balance/payment-methods/usage/invoices/credit/
// deposit/topup/gpu/spend-alerts/payment-config) — which also carries the INBOUND
// PROVIDER WEBHOOKS at /v1/billing/webhooks/:provider. Gating an inbound payment
// webhook loses payments outright, so that prefix is load-bearing twice over.
func reachable(path string) bool {
	// Liveness / readiness / metrics — a gate must never hide whether the process
	// is up, or an incident becomes invisible at exactly the wrong moment.
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
	// The auth routes moved when the /v1 surface was namespaced (/v1/signin →
	// /v1/auth/signin, /v1/get-account → /v1/auth/account). This list matches by
	// STRING, so it does not follow them: the old spellings here would put a 402
	// in front of sign-in. Pinned by TestAuthRoutesAreNeverPaywalled.
	switch path {
	case "/v1/auth/signin", // auth: session bootstrap (the console posts the OAuth code here).
		"/v1/auth/signout",
		"/v1/auth/account", // auth: the account read AuthGate loads before anything else.
		"/v1/entitlements": // this paywall's OWN projection — what the shell renders the upgrade UI from.
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

// reachableTrees are the /v1 sub-trees the paywall may never refuse. Every entry is
// written as a root WITH its trailing slash and covers the bare root too.
var reachableTrees = []string{
	"/v1/billing/",      // the whole money surface: subscribe, top up, AND the inbound provider webhooks.
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
