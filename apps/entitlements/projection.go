package entitlements

// projection.go serves GET /v1/entitlements — the authoritative, per-caller
// projection of commerce ENTITLEMENT (the org's plan tier → which console apps it
// unlocks) that the @hanzogui/shell useEntitlement hook reads. It is the READ twin
// of RequireProduct (require.go): same ONE authority (commerce.CheckEntitlement),
// same distinctness from the ENABLEMENT store in entitlements.go.
//
// FAIL DIRECTION (deliberately OPPOSITE to RequireProduct). This is a READ the UI
// uses to decide what to SHOW, so it fails SAFE-TO-LOCKED, never 500:
//   - unvalidated principal ...... 403 (identity refusal, not infra).
//   - commerce nil / error ....... that app reports false (locked in the UI), 200.
//     The ENFORCEMENT path (RequireProduct) still fails OPEN, so functionality is
//     preserved during an outage even while the UI conservatively shows locked.
//   - definitive answer .......... the real per-org bool.
// The response ALWAYS carries all six app keys (never null), so the shell can read
// them unconditionally.

import (
	"context"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/zap-proto/zip"
)

// projectionView is the exact wire contract the @hanzogui/shell useEntitlement hook
// consumes: the resolved plan slug and a per-app licensed map. `apps` always holds
// the SAME six keys (studio, bot, world, platform, team, admin) so the client maps
// over it unconditionally; `tier` is "" when the org has no active licensing
// subscription (the shell treats that as its free/locked default).
type projectionView struct {
	// Tier is the plan slug commerce resolved for the org, or "" when the org has no
	// active licensing subscription — which the console treats as its free default.
	Tier string `json:"tier"`
	// Apps says, per console app, whether the org may open it. The SAME six keys are
	// always present (studio, bot, world, platform, team, admin), so a client maps
	// over it unconditionally; a key is false both when the plan does not grant the
	// app and when commerce could not be reached, because a read that decides what to
	// SHOW fails to LOCKED rather than to an error.
	Apps map[string]bool `json:"apps"`
}

// Projection reports which console apps the CALLER's org may open, and the plan slug
// that decides it. It is the READ side of the unified paywall: the org's plan tier
// resolved from commerce, which is a different authority from the enablement store
// behind GET /v1/orgs/{org}/entitlements (that one is the org's own on/off intent).
//
// It fails SAFE-TO-LOCKED, never 500: an unvalidated principal is a 403, but a
// commerce outage reports every app locked at 200 rather than breaking the shell.
// The ENFORCEMENT path still fails open, so functionality survives the same outage
// even while the UI conservatively shows locked.
func (o ops) projection(ctx context.Context, _ *noArgs) (*projectionView, error) {
	org, ok := principal.OrgFrom(ctx)
	if !ok {
		return nil, zip.ErrForbidden("no validated principal")
	}

	// "admin" is the platform-sudo predicate, not a commerce product: resolve it
	// from the unforgeable X-User-IsAdmin bit, never via CheckEntitlement. That bit
	// is not carried by the parked org, so it comes off the request — and off the
	// HTTP path, where there is no attested principal, it is simply false.
	superAdmin := false
	if c, ok := cloud.Request(ctx); ok {
		superAdmin = principal.IsSuperAdmin(c)
	}
	apps := map[string]bool{"admin": superAdmin}

	tier := ""
	for _, product := range shellApps {
		// A product no plan can grant is NOT LOCKED — nothing gates it. Reporting
		// false here is what made this projection tell every customer on every tier,
		// enterprise included, that studio/bot/platform were locked behind a purchase
		// that does not exist. The key stays because the console shell maps over these
		// names unconditionally and a missing one is a contract break; only the answer
		// changes, from a false lock to the truth.
		if !gated[product] {
			apps[product] = true
			continue
		}
		active, plan, resolved := o.s.licensed(ctx, org, product)
		apps[product] = active
		// The resolved plan slug is the same for every product of one org (it is the
		// org's subscription tier); capture the first commerce could name so a single
		// unlicensed-but-resolved product still yields the tier.
		if resolved && tier == "" && plan != "" {
			tier = plan
		}
	}

	return &projectionView{Tier: tier, Apps: apps}, nil
}

// noArgs is the input of an op that takes none: no body, no query, no path param.
type noArgs struct{}

// licensed reports whether org holds an ACTIVE entitlement for product, plus the
// plan slug commerce resolved. resolved is false when commerce could not answer
// (nil client or a machinery error): the READ path then reports the app LOCKED
// (fail-safe-to-locked for the UI) and returns 200 — distinct from RequireProduct's
// fail-OPEN on the enforcement path. A DEFINITIVE Active:false is (false, plan, true).
func (s *service) licensed(ctx context.Context, org, product string) (active bool, plan string, resolved bool) {
	if s.commerce == nil {
		return false, "", false
	}
	ent, err := s.commerce.CheckEntitlement(ctx, org, product)
	if err != nil {
		s.log.Warn("entitlements projection: check failed; reporting locked", "org", org, "product", product, "err", err)
		return false, "", false
	}
	if ent == nil {
		return false, "", false
	}
	return ent.Active, ent.Plan, true
}
