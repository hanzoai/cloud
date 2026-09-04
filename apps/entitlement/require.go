package entitlement

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
	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/flags"
)

// appProducts is the canonical set of console "app" products the unified paywall
// projects, in the fixed key order the @hanzogui/shell useEntitlement hook reads.
// Each element is the REAL @hanzo/plans product id CheckEntitlement expects — the
// BARE id (commerce prepends "licensing.product:" itself), verified against
// @hanzo/plans v1.4.4 licensing.product_ids. This is the ONE source of the app-key
// set so the projection and any future consumer never re-list it.
//
// NOTE: "studio", "bot" and "platform" are deliberately absent, for the reason the
// catalog states: no plan grants them. Listing one here reports it LOCKED to every
// customer on every tier, enterprise included. A gate may only claim a product some
// plan can actually grant; claiming one nothing grants is not a stricter gate, it is
// a false one.
//
// When they launch they come back ATOMICALLY — this list and the granting plans in the same
// change, never half-wired. That is the invariant the contract test enforces, and it is why
// it caught this.
//
// NOTE: "world" is no longer here. It was never a `licensing.product_ids` grant in any
// catalog version; world access was conveyed by the tier-level `bundles` field pointing
// at separate world-free/pro/team/enterprise PLANS, and @hanzo/plans carried that at
// v1.4.4 and DELETED both the field and those plans at v1.4.11. Asking the licence
// authority about a product it has never licensed answers Active:false for every org on
// every tier, so the projection reported world LOCKED to every customer unconditionally —
// a lock no purchase could lift. World is not a separately billable product; its limits
// resolve to the free floor (apps/world/entitlement.go), which is now the whole answer
// rather than a degraded one.
//
// NOTE: "admin" is intentionally NOT here. It is not a commerce product — it is the
// platform-sudo predicate (principal.IsSuperAdmin / c.IsAdmin), strictly tighter
// than any purchasable tier — so the projection resolves it separately and it is
// never handed to CheckEntitlement.
var appProducts = []string{"crm", "team"}

// shellApps is what the projection must ALWAYS answer with: @hanzogui/shell maps
// over exactly these names, so a key it does not find is a contract break in the
// console rather than a smaller answer. It is deliberately WIDER than appProducts —
// the gate governs what is licence-checked, this governs what is reported, and the
// two stopped being the same list the moment cloud stopped claiming products no plan
// can grant.
//
// FOLLOW-UP (not this change): @hanzogui/shell's APP_ENTITLEMENTS still advertises
// studio/bot/world/platform to the browser as tier "pro", client-side, against
// GET /v1/billing/subscriptions. That is a third vocabulary and the only place the
// product intent is written down. Reconciling the console with the server is its own
// piece of work.
var shellApps = []string{"studio", "bot", "world", "platform", "team"}

// gated indexes appProducts: the products the licence authority is actually asked
// about. Derived, never hand-copied — a product added to appProducts is gated the
// moment it appears there.
var gated = func() map[string]bool {
	m := make(map[string]bool, len(appProducts))
	for _, p := range appProducts {
		m[p] = true
	}
	return m
}()

// ── admin switches (the ONE flag engine — clients/flags) ────────────────────────

// enforceKey is the kill switch for the paywall SpendGate mounts, and it is the
// entire safety story for a gate that can otherwise lock out every paying customer.
// OFF (the default) admits everything and consults no billing authority, so the gate
// is flipped on under observation from the /v1/admin/flags cockpit and can be killed
// INSTANTLY from that same cockpit if it misfires — no redeploy, hot within one eval
// TTL.
//
// The root package owns the string so this registry entry, the evaluator, and the
// edge that reads it cannot drift.
const enforceKey = cloud.SwitchPaywallEnforced

// strictKey is the posture on an UNRESOLVABLE standing. OFF = availability: an
// authority we cannot reach never refuses a customer. ON = revenue: an unresolvable
// standing refuses. It exists so the choice between those two is made live, by an
// owner watching real traffic, instead of being frozen into a constant at build time.
const strictKey = cloud.SwitchPaywallStrict

func init() {
	flags.Register(flags.Def{
		Key: enforceKey, Category: "Launch", Type: flags.TypeBool, Default: "false",
		// NO Env FALLBACK, deliberately, and TestSwitchesDefaultOff pins it. An env
		// var that can turn the gate ON cannot be turned OFF from the cockpit — the
		// kill switch would be defeated by the very variable that armed the gate,
		// which is the one failure mode this switch exists to prevent. The cockpit is
		// the single source of truth.
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
