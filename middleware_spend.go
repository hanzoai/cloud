package cloud

// middleware_spend.go APPLIES the spend predicate; spend.go COMPUTES it. This file
// decides only WHETHER and HOW to refuse: the kill switch, the fail posture, and the
// shape of the 402. It never restates policy.
//
// It replaces routers.Paywall, which serve.go mounted app-wide and which asked the
// wrong question — "does the org hold a paid PLAN?" with no credit leg — so turning
// it on would have 402'd every prepaid customer. That is why it shipped dark and
// stayed dark. SpendGate asks the question the money actually turns on: subscription
// OR prepaid credit, read at the wallet address the DEBIT writes.
//
// WHAT IT CLOSES. A stranger can self-signup today and run inference we pay a
// provider for, at zero balance. The reason is not missing auth — every gate on the
// path checks auth. It is that the two live gates check the WRONG THING:
//
//   - the edge BillingGate gates on price(path) > 0, and DefaultPrice returns 0
//     everywhere, so it never evaluates;
//   - zen's commerceGate (apps/zen.go) gates the caller's ORG POOL, and every
//     self-serve signup lands in the shared "hanzo" org whose pool is funded — so a
//     brand-new $0 account reads a six-figure balance and sails through;
//   - zen's own Tenant.Valid() is `t.Org != ""` — auth, not balance.
//
// SpendGate is one gate, at one place, on the one predicate, covering the LLM paths
// and the non-LLM resource trees alike (Billable), so neither can be fixed without
// the other.

import (
	"context"
	"net/http"

	"github.com/hanzoai/cloud/clients/principal"
	"github.com/zap-proto/zip"
)

// PlanChecker is the ONE commerce read this gate needs: does org X hold a LIVE
// (active or trialing) PAID plan? It is a consumer-defined interface satisfied
// structurally by the co-resident commerce client (clients/commerce.ActivePaidPlan) —
// an OPTIONAL capability resolved from Deps.Commerce by type-assertion, so the narrow
// types.CommerceClient interface is untouched and a commerce build that cannot answer
// (split deploy / disabled stub) simply yields LicenceUnknown.
//
//   - (tier, true,  nil) -> LicenceActive.
//   - ("",   false, nil) -> LicenceNone: resolved, no live paid plan.
//   - (_,    _,     err) -> LicenceUnknown: machinery failure, never a "no".
type PlanChecker interface {
	ActivePaidPlan(ctx context.Context, org string) (tier string, paid bool, err error)
}

// SpendGate returns the balance-and-subscription gate serve.go mounts app-wide.
//
// DEFAULT OFF, AND THAT IS A SEQUENCING DECISION, NOT TIMIDITY. There is no reachable
// starter-credit path in this binary today: the grant-starter route was deleted from
// commerce (last present in commerce v1.48.2), its replacement POST /v1/billing/credit
// is not registered in the co-resident build, and no ensureStarterCredit runs
// anywhere. So a brand-new signup's wallet is $0 with no self-service way to fund it.
// Enforcing on that state 402s every new signup on day one — trading a revenue leak
// for a total signup outage. The gate therefore ships behind the kill switch, proven
// by tests, and is flipped only after a funding path exists. Turning it on before then
// is the one way to make this worse.
//
// Enforcement is read PER REQUEST from the platform switch an owner flips at
// admin.hanzo.ai, so it turns on and off within one flag-cache TTL — no redeploy, no
// CR edit. Switch reports false before the flag engine mounts, so an unmounted switch
// never enforces.
//
// DECISION ORDER — a request is ADMITTED unless the one proven refusal fires:
//
//  1. enforcement OFF ........................ admit. Read FIRST, so a dark gate costs
//     one atomic load and touches no authority.
//  2. not Billable ........................... admit. Reads, the pay path, and every
//     non-metered route are never gated (see Billable / Reachable).
//  3. unvalidated principal .................. admit. An anonymous caller is the
//     route's own 401 to make; a 402 is meaningless to someone not signed in, and
//     the org on such a request is a forge anyway.
//  4. platform super-admin ................... admit. Platform sudo, including
//     masquerade, is strictly tighter than any purchasable tier and never 402s.
//  5. subscribed OR funded ................... admit.
//  6. UNRESOLVABLE standing .................. posture (paywall_strict), logged WARN.
//  7. proven unpaid .......................... 402 with the actionable refusal.
//
// FAIL POSTURE — argued, not inherited. The refusal in (7) requires PROOF: both
// authorities answered and both said no. An authority we could not reach yields
// Unknown, and by default an Unknown ADMITS. That is not "unpaid users get everything
// free whenever commerce hiccups" — it is "we never call a customer delinquent on
// evidence we do not have". The asymmetry decides it: refusing on an outage 402s every
// PAYING customer at once (a total product outage, irreversible), while admitting on
// an outage leaks access for the duration (bounded, recoverable, and separately capped
// — the subsystem meters still run their own fail-closed debit). Every fail-open admit
// logs at WARN with the reason, so a sustained leak pages rather than hides. When an
// owner wants the other trade, paywall_strict flips it live.
//
// An unresolvable WALLET is an Unknown, not an admit: principal.WalletOf refuses an
// unresolvable org, and "we could not work out who pays" must refuse AS UNKNOWN and
// take the strict posture, never be silently waved through as if it were free.
func SpendGate(commerce CommerceClient) zip.Handler {
	plans, _ := commerce.(PlanChecker)
	return func(c *zip.Ctx) error {
		if !Switch(SwitchPaywallEnforced) {
			return c.Next() // dark — byte-identical to no middleware at all.
		}
		if !Billable(c.Method(), c.Path()) {
			return c.Next()
		}
		if !principal.Validated(c) {
			return c.Next() // the route's own auth answers; a 402 would mask a 401.
		}
		if principal.IsSuperAdmin(c) {
			return c.Next() // platform sudo, masquerade included.
		}
		w, ok := principal.WalletOf(c)
		if !ok {
			// No resolvable payer. Unknown, not unpaid, and never a free pass.
			return unresolved(c, "no resolvable wallet")
		}
		switch s := Stand(c.Context(), licence(c.Context(), plans, w.Ledger), w); {
		case s.Admits():
			return c.Next()
		case s == Unknown:
			return unresolved(c, "billing authority unreadable")
		default:
			c.Log().Info("spend: no subscription and no credit", "org", w.Ledger, "account", w.Account, "path", c.Path())
			return Refuse(c, "", ReasonUnpaid)
		}
	}
}

// licence resolves the subscription leg for the spend gate. A nil PlanChecker (commerce
// not co-resident) and any query error are both LicenceUnknown — machinery that could
// not answer has not said "no".
func licence(ctx context.Context, plans PlanChecker, org string) Licence {
	if plans == nil || org == "" {
		return LicenceUnknown
	}
	_, paid, err := plans.ActivePaidPlan(ctx, org)
	if err != nil {
		return LicenceUnknown
	}
	if paid {
		return LicenceActive
	}
	return LicenceNone
}

// unresolved applies the posture for a standing nobody could determine. OFF (the
// default) admits and WARNs; paywall_strict refuses with the DISTINCT unresolved code,
// because the caller's cure is different — an unpaid caller must buy something, an
// unresolved one must retry.
func unresolved(c *zip.Ctx, why string) error {
	if Switch(SwitchPaywallStrict) {
		c.Log().Warn("spend: standing unresolvable; refusing (strict)", "why", why, "path", c.Path())
		return Refuse(c, "", ReasonUnresolved)
	}
	c.Log().Warn("spend: standing unresolvable; admitting", "why", why, "path", c.Path())
	return c.Next()
}

// ── the refusal ─────────────────────────────────────────────────────────────────

// The two reasons a 402 can carry, spelled once for every gate. They are DISTINCT
// codes because the caller's cure is different: an unpaid caller must buy something,
// an unresolved one must retry.
const (
	ReasonUnpaid     = "unpaid"
	ReasonUnresolved = "unresolved"
)

// Refusal is the 402 body. A bare 402 is useless to the console shell, so this names
// WHAT is gated, WHY, and every way to cure it — one Cure per admit leg, so the body
// is the structural mirror of the predicate and can never drift from it.
//
// The cure paths are RELATIVE and same-origin, deliberately: a hard-coded
// cloud.hanzo.ai would brand a Lux / Zoo / Pars deployment as Hanzo, and this binary
// white-labels by host. They point at the two API surfaces the shell already reads and
// that Reachable guarantees are never gated — /v1/plans (what to buy, with prices) and
// /v1/billing (where to pay, subscribe or top up).
type Refusal struct {
	Error   string `json:"error"`             // stable machine code: always "payment_required"
	Product string `json:"product,omitempty"` // the gated product id, when the gate is product-scoped
	Reason  string `json:"reason"`            // "unpaid" | "unresolved"
	Message string `json:"message"`           // one human sentence
	Cure    []Cure `json:"cure"`              // the ways to fix it, in the order to offer them
}

// Cure is one way out of a 402: Kind names the admit leg it satisfies
// ("subscribe" | "credit"), URL where to do it.
type Cure struct {
	Kind string `json:"kind"`
	URL  string `json:"url"`
}

// cures are the two ways to cure a refusal — exactly the two legs Stand admits on, in
// the order to offer them (a subscription is the steady state; prepay is the
// no-commitment path).
var cures = []Cure{
	{Kind: "subscribe", URL: "/v1/plans"},
	{Kind: "credit", URL: "/v1/billing"},
}

// Refuse renders the ONE 402 shape every spend gate uses. product is empty for the
// edge gate (which gates spend itself, not a product) and set by a product-scoped
// gate; `for <product>` is the only difference it makes to the sentence.
func Refuse(c *zip.Ctx, product, reason string) error {
	scope := ""
	if product != "" {
		scope = " for " + product
	}
	msg := "no active subscription" + scope + " and no prepaid credit"
	if reason == ReasonUnresolved {
		msg = "billing could not be verified" + scope + "; retry shortly"
	}
	return c.JSON(http.StatusPaymentRequired, Refusal{
		Error:   "payment_required",
		Product: product,
		Reason:  reason,
		Message: msg,
		Cure:    cures,
	})
}
