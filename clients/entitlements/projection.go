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
	"net/http"

	"github.com/hanzoai/cloud/clients/principal"
	"github.com/zap-proto/zip"
)

// projectionView is the exact wire contract the @hanzogui/shell useEntitlement hook
// consumes: the resolved plan slug and a per-app licensed map. `apps` always holds
// the SAME six keys (studio, bot, world, platform, team, admin) so the client maps
// over it unconditionally; `tier` is "" when the org has no active licensing
// subscription (the shell treats that as its free/locked default).
type projectionView struct {
	Tier string          `json:"tier"`
	Apps map[string]bool `json:"apps"`
}

// projection serves GET /v1/entitlements for the CALLER's org.
func (s *service) projection(c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("no validated principal")
	}

	// "admin" is the platform-sudo predicate, not a commerce product: resolve it
	// from the unforgeable X-User-IsAdmin bit, never via CheckEntitlement.
	apps := map[string]bool{"admin": principal.IsSuperAdmin(c)}

	tier := ""
	for _, product := range appProducts {
		active, plan, resolved := s.licensed(c.Context(), org, product)
		apps[product] = active
		// The resolved plan slug is the same for every product of one org (it is the
		// org's subscription tier); capture the first commerce could name so a single
		// unlicensed-but-resolved product still yields the tier.
		if resolved && tier == "" && plan != "" {
			tier = plan
		}
	}

	return c.JSON(http.StatusOK, projectionView{Tier: tier, Apps: apps})
}

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
