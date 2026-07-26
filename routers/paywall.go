// Package routers holds cross-cutting request filters that run on the unified cloud
// binary's edge, AFTER the identity boundary (SanitizeIdentity) has resolved the
// caller's principal — so every gate keys on a VALIDATED IAM owner claim, never a raw
// client header.
//
// paywall.go is the subscription gate. When enforcement is ON, a request to a gated
// /v1 product route from a validated org that has NO active paid plan is refused with
// 402 subscription_required, steering the caller to the upgrade page. Every route the
// sign-in / billing / plans / model-catalog / health surface needs to SELL and SERVICE
// that upgrade stays open, so the gate can never lock a user out of paying.
package routers

import (
	"context"
	"net/http"
	"strings"

	"github.com/hanzoai/cloud/clients/principal"
	"github.com/zap-proto/zip"
)

// PlanChecker is the ONE commerce read the paywall needs: does org X hold a LIVE
// (active or trialing) PAID plan, and which tier. It is a consumer-defined interface
// (idiomatic Go) satisfied structurally by the co-resident commerce client
// (clients/commerceclient.ActivePaidPlan) — an OPTIONAL capability resolved from
// deps.Commerce by type-assertion (mirrors types.ModelLister), so the narrow
// types.CommerceClient interface is untouched and a commerce build that cannot answer
// (split-deploy / disabled stub) yields a nil PlanChecker → the paywall fails OPEN.
//
//   - (tier, true,  nil) — a live paid plan; admit.
//   - ("",   false, nil) — resolved, NO live paid plan; the 402 case.
//   - (_,    _,     err) — machinery failure; admit (fail open — never lock out).
type PlanChecker interface {
	ActivePaidPlan(ctx context.Context, org string) (tier string, paid bool, err error)
}

// Paywall returns the subscription-gate middleware.
//
// enforced is READ PER REQUEST, not once at mount. That is the whole point: the value
// comes from the cockpit switch an owner flips at admin.hanzo.ai, so enforcement turns
// on and off within one flag-cache TTL with no redeploy and no CR edit. Evaluating it
// at mount time — as this did — meant the only way to flip the gate was to restart the
// binary, and it also meant the switch already registered in the cockpit governed
// nothing. A nil func is the dark default: a pure passthrough, ZERO behavior change.
//
// This package deliberately does NOT import the flag engine. It takes a func and stays
// a pure request filter, so the caller decides where the answer comes from.
//
// NOT YET WIRED to the cockpit, and the reason is structural: clients/flags imports the
// ROOT package (cloud.Deps/Handle/OrgStore/...), and the root package imports this one
// to mount the middleware — so root can never import flags, and no edge filter mounted
// from serve.go can read a switch. That is why the `paywall_enforced` switch registered
// in the cockpit governs clients/entitlements.RequireProduct (a leaf, which may import
// flags) and NOT this middleware, even though this one is what serve.go actually mounts.
// Closing that gap means either inverting flags off the root package, or moving
// enforcement onto RequireProduct at the gated route groups. Until then serve.go passes
// the PAYWALL_ENFORCED config value and a flip still needs a redeploy.
//
// plans is the commerce plan read; nil ⇒ fail open (commerce cannot answer, so the gate
// never blocks). It is resolved from deps.Commerce by the caller (serve.go) via a
// type-assertion to PlanChecker.
//
// Decision order — a request is ADMITTED unless the ONE definitive deny fires:
//  1. !enforced ................................ admit (dark ship).
//  2. non-/v1 path (SPA shell, static assets,
//     /healthz, /readyz, /zap) ................ admit (the paywall gates the /v1 product
//     API only — never the app that renders the upgrade prompt).
//  3. allow-listed /v1 path (auth, billing,
//     plans, models, health, entitlements) .... admit (the sell/service surface).
//  4. no validated principal ................... admit (an anonymous caller is the route's
//     own 401/403 to make; a 402 upgrade prompt is meaningless to someone not signed in,
//     and the org is untrusted anyway).
//  5. platform super-admin ..................... admit (operator bypass).
//  6. org unresolved / plans nil / plans error   admit (fail open — never lock out).
//  7. org HAS a live paid plan ................. admit.
//  8. otherwise ................................ 402 subscription_required.
func Paywall(enforced func() bool, plans PlanChecker) zip.Handler {
	if enforced == nil {
		// No reader wired: a pure passthrough, byte-identical to no middleware at all.
		return func(c *zip.Ctx) error { return c.Next() }
	}
	return func(c *zip.Ctx) error {
		if !enforced() {
			// Dark: one flag read off a hot in-memory snapshot, no authority consulted.
			return c.Next()
		}
		path := c.Path()
		if !gated(path) {
			return c.Next() // non-/v1 surface, or an allow-listed sell/service route.
		}
		if !principal.Validated(c) {
			return c.Next() // no validated principal → the route's own auth answers.
		}
		if principal.IsSuperAdmin(c) {
			return c.Next() // platform operator bypass.
		}
		org, ok := principal.Org(c)
		if !ok {
			return c.Next() // no trustworthy org → fail open.
		}
		if plans == nil {
			return c.Next() // commerce cannot answer → fail open.
		}
		_, paid, err := plans.ActivePaidPlan(c.Context(), org)
		if err != nil {
			// Machinery failure (commerce/plan unreachable) — an outage must NEVER lock
			// out a subscriber. Admit and log.
			c.Log().Warn("paywall: plan unverifiable; admitting (fail open)", "org", org, "path", path, "err", err)
			return c.Next()
		}
		if paid {
			return c.Next() // live paid plan — admit.
		}
		// The ONE deny: a validated, non-admin org with a resolvable-but-planless account
		// hitting a gated product route. 402 with the upgrade hint; the handler never runs.
		c.Log().Info("paywall: no active paid plan; 402 subscription_required", "org", org, "path", path)
		return c.JSON(http.StatusPaymentRequired, map[string]any{
			"error": "subscription_required",
			"plan":  "pro",
			"price": "$20/mo",
			"url":   "https://cloud.hanzo.ai/plans",
		})
	}
}

// gated reports whether the paywall may enforce on this request path: it must be a /v1
// product API route AND not on the allow-list. Everything else — the SPA shell + static
// assets on non-/v1 paths, /healthz, /readyz, the /zap WebSocket upgrade, and the whole
// sell/service surface — is admitted so the gate can NEVER block loading the app or
// paying for it.
func gated(path string) bool {
	p := canonical(path)
	if !strings.HasPrefix(p, "/v1/") {
		return false // not a /v1 product API route (SPA, static, /healthz, /readyz, /zap).
	}
	return !allowlisted(p)
}

// canonical folds the two ways a request path can differ from the form written in
// the lists below while meaning the same route: ASCII case, and a trailing slash.
// Both predicates read the folded value, so the gate decides once and cannot
// disagree with itself.
//
// Case matters in BOTH directions, which is why folding — not a second list — is
// the fix:
//
//   - `/V1/chat/completions` used to miss the `/v1/` prefix, so gated() returned
//     false and the request skipped the paywall entirely. A gate whose bypass is
//     a shift key is not a gate.
//   - `/v1/IAM/callback` used to miss the `/v1/iam/` allow prefix, so an OAuth
//     callback would have been refused 402 — locking a user out of signing in.
//
// The trailing slash is the same shape of bug against the exact-match list:
// `/v1/signin/` matched no case in allowlisted() and no allow prefix, so it
// gated. `/v1/` itself is left alone (nothing is trimmed below the prefix), so a
// bare version root keeps whatever the router already did with it.
func canonical(path string) string {
	p := strings.ToLower(path)
	if len(p) > len("/v1/") {
		p = strings.TrimRight(p, "/")
	}
	return p
}

// allowlisted reports whether a /v1 path is EXEMPT from the paywall — every route the
// console needs to authenticate a user and let them SEE and BUY a plan, plus the
// liveness surface. Blocking any of these would brick the path to payment (a paywall in
// front of the pay button is catastrophic), so the list is deliberately generous.
//
// Traced from the console subscribe flow (console src/components/products/PlansModule.tsx
// → src/lib/api/plans.ts): PlansApi.plans() reads GET /v1/billing/plans through the
// per-tenant billing proxy, and checkout drives the /v1/billing/* money surface
// (subscribe/subscriptions/balance/payment-methods/usage/invoices/credit/deposit/topup/
// gpu/spend-alerts/payment-config). Auth is /v1/signin, /v1/signout, /v1/get-account and
// the /v1/iam/* login/OAuth/OIDC callbacks; /v1/models is the model catalog the shell
// reads; /v1/entitlements is the product projection the shell renders the upgrade UI from.
// Its argument is already canonical() — gated() is the only caller, so every entry
// below is written in lower case with no trailing slash and compared against a
// value in that same form.
func allowlisted(path string) bool {
	// Liveness/health — never gated (mirrors DefaultPrice's probe carve-out).
	if path == "/v1/health" || strings.HasSuffix(path, "/health") {
		return true
	}
	// Exact single-route exemptions.
	switch path {
	case "/v1/signin", // auth: session bootstrap (console posts the OAuth code here).
		"/v1/signout",
		"/v1/get-account",  // auth: the account read AuthGate loads before anything else.
		"/v1/plans",        // plans catalog root (@hanzo/plans subsystem).
		"/v1/models",       // OpenAI-compatible model catalog (discovery).
		"/v1/entitlements": // the shell's product projection — renders the upgrade UI.
		return true
	}
	// Prefix exemptions (sub-trees).
	for _, p := range allowPrefixes {
		if strings.HasPrefix(path, p) {
			return true
		}
	}
	return false
}

// allowPrefixes are the /v1 sub-trees the paywall never gates.
var allowPrefixes = []string{
	"/v1/billing/", // the whole commerce billing + subscribe money surface.
	"/v1/iam/",     // IAM login / OAuth token exchange / .well-known OIDC discovery.
	"/v1/plans/",   // plans catalog sub-routes (resolve, entitlements, cloud, gpu, …).
	"/v1/models/",  // model retrieve (/v1/models/:id).
}
