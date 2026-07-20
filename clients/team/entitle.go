package team

// The billing gate for workspace login, in ONE place. selectWorkspace — the one
// chokepoint every client passes on its way to a workspace token — asks entitle
// whether the org's plan licenses the team product, mirroring how
// clients/entitlements separates the two kinds of "no":
//
//   - INFRA error (commerce not co-resident, plans vocabulary unreachable) —
//     cannot verify ⇒ ALLOW. Unlike the product-enable gate (which 503s), a
//     login gate that fails closed on an outage would brick every team session
//     mid-rollout, so it degrades to open and logs.
//   - DEFINITIVE "no entitlement" (resolution succeeded, no plan licenses
//     'team') ⇒ OBSERVE: log the denial and admit. Self-serve checkout does
//     not exist yet, so a 402 here locks users out of a dead end — proven live
//     2026-07-20 when the blocking form of this gate 402'd every org without a
//     subscription row and broke all logins. Enforcement (the 402 with the
//     upgrade destination) returns when the card-on-file subscribe path ships.
//
// Personal AI plans license 'team' with an invited-guest cap (the plan
// entitlement key `team.guests` in @hanzo/plans subscription.json); org team
// plans carry no cap. The cap is enforced by join order: the workspace's first
// `cap` guests keep access, later ones are refused until the org upgrades.

import "context"

// productTeam is the licensing product id the team surface is sold as.
const productTeam = "team"

// roleGuest is the members.role value an invited guest carries.
const roleGuest = "guest"

// guestCapKey is the @hanzo/plans entitlement key carrying the invited-guest cap.
const guestCapKey = "team.guests"

// upgradeURL is the destination a 402 points the client at.
const upgradeURL = "https://billing.hanzo.ai"

// statusPaymentRequired is the 402 body: a platform Status whose params carry
// the product and the upgrade destination.
func statusPaymentRequired(msg string) Status {
	return Status{Severity: "ERROR", Code: "account:status:PaymentRequired", Params: map[string]any{
		"message": msg, "product": productTeam, "upgradeUrl": upgradeURL,
	}}
}

// entitle returns nil to admit, or the 402 Status to refuse. See the file
// header for the fail-open/fail-closed contract.
func (g *api) entitle(ctx context.Context, org, role, workspaceID, account string) *Status {
	if g.commerce == nil {
		return nil // commerce not wired — infra absence, never blocks login
	}
	ent, err := g.commerce.CheckEntitlement(ctx, org, productTeam)
	if err != nil {
		g.log.Warn("team: entitlement unverifiable, admitting", "org", org, "err", err)
		return nil
	}
	if ent == nil || !ent.Active {
		g.log.Warn("team: org lacks a team entitlement, admitting (observe)", "org", org)
		return nil
	}
	if role != roleGuest || g.planEnt == nil {
		return nil
	}
	ents, err := g.planEnt(ctx, ent.Plan)
	if err != nil {
		g.log.Warn("team: plan entitlements unverifiable, admitting", "plan", ent.Plan, "err", err)
		return nil
	}
	limit, capped := intEntitlement(ents[guestCapKey])
	if !capped {
		return nil // no team.guests key — this plan's guests are uncapped
	}
	if rank := g.accounts.GuestRank(ctx, workspaceID, account); rank > limit {
		g.log.Warn("team: guest over plan cap, admitting (observe)", "org", org, "limit", limit, "rank", rank)
		return nil
	}
	return nil
}

// intEntitlement reads a numeric plan-entitlement value (JSON numbers decode as
// float64; fakes may use int). ok=false for absent or non-numeric.
func intEntitlement(v any) (n int, ok bool) {
	switch x := v.(type) {
	case float64:
		return int(x), true
	case int:
		return x, true
	default:
		return 0, false
	}
}
