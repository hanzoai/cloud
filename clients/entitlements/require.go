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
// switch and the fail posture. WHAT the caller's standing is, the invariant that the
// path to payment stays reachable (cloud.Reachable), and the shape of the 402
// (cloud.Refuse) all live in the ROOT package now, because the edge filters serve.go
// mounts could never import this leaf and so grew their own divergent copies. This
// file is what is genuinely product-scoped and nothing more: the licence leg
// (CheckEntitlement for one product) and its application. Policy is never restated
// here — the plan→product rule lives once, in
// @hanzo/plans, resolved by commerce.CheckEntitlement.

import (
	"context"

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
const strictKey = cloud.SwitchPaywallStrict

func init() {
	flags.Register(flags.Def{
		Key: enforceKey, Category: "Launch", Type: flags.TypeBool, Default: "false",
		// NO Env FALLBACK, deliberately, and TestSwitchesDefaultOff pins it. An env
		// var that can turn the gate ON cannot be turned OFF from the cockpit — the
		// kill switch would be defeated by the very variable that armed the gate,
		// which is the one failure mode this switch exists to prevent. serve.go used
		// to OR a PAYWALL_ENFORCED config field on top of the switch and had exactly
		// that hole; the field is gone. The cockpit is the single source of truth.
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
		if cloud.Reachable(c.Path()) {
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
		switch s := cloud.Stand(c.Context(), licensedBy(c.Context(), commerce, org, product), w); {
		case s.Admits():
			return c.Next()
		case s == cloud.Unknown:
			if flags.Bool(strictKey) {
				c.Log().Warn("paywall: standing unresolvable; refusing (strict)", "product", product, "org", org)
				return cloud.Refuse(c, product, cloud.ReasonUnresolved)
			}
			c.Log().Warn("paywall: standing unresolvable; admitting", "product", product, "org", org)
			return c.Next()
		default:
			// The ONE proven refusal: no plan licenses the product AND the wallet is empty.
			c.Log().Info("paywall: no subscription and no credit", "product", product, "org", org, "standing", s.String())
			return cloud.Refuse(c, product, cloud.ReasonUnpaid)
		}
	}
}

// ── the subscription leg ────────────────────────────────────────────────────────

// licensedBy asks the ONE entitlement authority whether org's plan licenses product,
// and reports the answer in the shape the shared predicate composes. commerce may be
// nil (not co-resident); a query error or a nil result with no error is machinery that
// returned nothing, and machinery that returned nothing has NOT said "no" — all three
// are cloud.LicenceUnknown, never cloud.LicenceNone.
//
// The plan->product policy lives ONCE, in @hanzo/plans, resolved by CheckEntitlement.
// We read its verdict; we never restate it.
func licensedBy(ctx context.Context, commerce cloud.CommerceClient, org, product string) cloud.Licence {
	if commerce == nil {
		return cloud.LicenceUnknown
	}
	ent, err := commerce.CheckEntitlement(ctx, org, product)
	if err != nil || ent == nil {
		return cloud.LicenceUnknown
	}
	if ent.Active {
		return cloud.LicenceActive
	}
	return cloud.LicenceNone
}
