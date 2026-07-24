package entitlements

// require.go is the ENTITLEMENT (commerce) side of this package — the SERVER-SIDE
// half of the unified Hanzo paywall. It is deliberately distinct from the
// ENABLEMENT store in entitlements.go (per the package doctrine "TWO AUTHORITIES,
// NEVER BRAIDED"): the store records the org's INTENT (which products it toggled
// on); this file ENFORCES the billing truth (which products the org's plan grants)
// on the live data plane, and projects it for the console shell (projection.go).
//
// It is the enforcement authority behind the (bypassable-alone) client gate the
// @hanzogui/shell already ships. It NEVER restates the plan→product policy: that
// policy lives once, in @hanzo/plans, and is resolved by the ONE authority —
// commerce.CheckEntitlement (org → active subscriptions → plan tier → the flat
// `licensing.product:<id>` license features). We only apply that verdict here.

import (
	"net/http"

	"github.com/hanzoai/cloud"
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

// RequireProduct is the unified-paywall enforcement middleware a gated route group
// applies: it admits the request only when the caller's org holds an ACTIVE
// @hanzo/plans entitlement for `product`, deferring entirely to the ONE authority
// (commerce.CheckEntitlement) — the plan→product policy is never restated here.
//
// Trust + fail posture MIRRORS clients/team/entitle.go (a login/access gate), NOT
// the enable gate in entitlements.go (which fails CLOSED because enabling records
// paid intent). An access gate that failed closed on an outage would brick every
// paying user mid-incident, so:
//
//   - unvalidated principal .................. 403 (no trustworthy org to gate on;
//     an identity refusal, never an infra one — mirrors principal.Org's contract).
//   - commerce nil / CheckEntitlement error /
//     nil result (machinery could not answer) . ALLOW (fail OPEN, logged): a
//     commerce/datastore outage must NEVER lock out a user.
//   - DEFINITIVE not-licensed (valid result,
//     Active==false) ......................... 402 with the upgrade hint — the ONLY
//     path that denies, and only on a real "no plan licenses this product" answer.
//   - entitled .............................. next.
//
// Apply it ONLY to groups whose product is confirmed present in @hanzo/plans and
// whose routes are all authenticated (it 403s an unvalidated caller). As of plans
// v1.4.4 the studio/bot/world/platform products are ABSENT from the catalog, so
// gating those groups now would 402 every org — they stay unwired (see the markers
// in clients/{world,bots,platform}) until the catalog licenses them.
func RequireProduct(commerce cloud.CommerceClient, product string) zip.Handler {
	return func(c *zip.Ctx) error {
		org, ok := principal.Org(c)
		if !ok {
			return zip.ErrForbidden("no validated principal")
		}
		if commerce == nil {
			c.Log().Warn("entitlements: commerce unavailable; allowing (fail open)", "product", product, "org", org)
			return c.Next()
		}
		ent, err := commerce.CheckEntitlement(c.Context(), org, product)
		if err != nil {
			c.Log().Warn("entitlements: entitlement unverifiable; allowing (fail open)", "product", product, "org", org, "err", err)
			return c.Next()
		}
		if ent != nil && !ent.Active {
			// 402 Payment Required: a DEFINITIVE "no plan licenses this product"
			// answer. Mirrors entitlements.go:215 verbatim so the console routes it
			// to the SAME upgrade/purchase prompt as the enable gate.
			return zip.Errorf(http.StatusPaymentRequired, "product %q is not in this org's plan; upgrade in commerce to enable it", product)
		}
		// entitled — or a nil result with no error (ambiguous machinery): allow.
		return c.Next()
	}
}
