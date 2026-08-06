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

	"github.com/hanzoai/cloud/apps/principal"
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
// DEFAULT OFF, AND THAT IS A SEQUENCING DECISION, NOT TIMIDITY. Enforcing costs
// nothing a legitimate new customer cannot pay only because the FUNDING RUNG exists
// below it (step 7, starter.go): a brand-new org's wallet is $0, and without a path
// that funds it, flipping this switch would 402 every new account from its first
// request. That rung is what makes the flip a pricing decision rather than a signup
// that dead-ends; the flip itself stays an owner's deliberate act.
//
// THE RUNG HAS NO SWITCH OF ITS OWN, and that is the point. It is reached from ONE
// place — inside this ladder, past the enforcement check — so the credit and the
// refusal it cures are the same flag. There is no state where money is being created
// and the paywall is not enforcing, which is the whole objection to an automatic
// grant that sits behind a flag of its own (see starter.go).
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
//  7. new, and the screen allows ............. admit, on the plan's included credit,
//     granted once (starter.go). Asked only here, where the answer is otherwise 402.
//  8. proven unpaid .......................... 402 with the actionable refusal.
//
// FAIL POSTURE — argued, not inherited. The refusal in (8) requires PROOF: both
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
		if reason := standing(c, c.Method(), c.Path(), plans); reason != "" {
			return Refuse(c, "", reason)
		}
		return c.Next()
	}
}

// standing IS the ladder above — every step of it — returning "" to admit and
// otherwise the reason to refuse with. It is split out of SpendGate for one
// reason: the ladder decides, and SpendGate only renders, and the OP SEAM (Toll)
// has to reach the same decision through a channel that cannot write a body. Two
// copies of a seven-step money ladder is the drift this codebase has paid for
// three times; there is one, and both seams call it.
//
// c may be nil — an operation reached from the command line runs with no request
// behind it at all. That is the unvalidated case, already step 3, so it needs no
// branch of its own: a caller with no attested principal is the route's own 401 to
// make, and a 402 in front of it would mask that. The CHARGE leg is where an
// absent payer refuses, because there the absence is decisive: you cannot debit
// nobody.
//
// method and path name the OPERATION, not the transport. Over MCP the request is
// POST /mcp and over the ZAP plane POST /.well-known/zip/op/<name>; neither is a
// billable tree, so a ladder fed the request's own path admits every metered
// operation ever reached through one.
func standing(c *zip.Ctx, method, path string, plans PlanChecker) string {
	if !Switch(SwitchPaywallEnforced) {
		return "" // dark — byte-identical to no gate at all.
	}
	if !Billable(method, path) {
		return ""
	}
	if c == nil || !principal.Validated(c) {
		return "" // the route's own auth answers; a 402 would mask a 401.
	}
	if principal.IsSuperAdmin(c) {
		return "" // platform sudo, masquerade included.
	}
	w, ok := principal.WalletOf(c)
	if !ok {
		// No resolvable payer. Unknown, not unpaid, and never a free pass.
		return unresolved(c, "no resolvable wallet", path)
	}
	switch s := Stand(c.Context(), licence(c.Context(), plans, w.Ledger), w); {
	case s.Admits():
		return ""
	case s == Unknown:
		return unresolved(c, "billing authority unreadable", path)
	default:
		// BEFORE REFUSING, FUND. This is the last point at which an account that has
		// simply never been funded can still be told apart from one that will not pay,
		// and the only point at which the difference matters — so the plan's included
		// credit is provisioned HERE (starter.go) rather than on some earlier pass that
		// would have to guess whether it was needed.
		//
		// It is asked after Unpaid is PROVEN, which is what lets it be cheap and what
		// lets it be safe: both authorities have answered, so the rung inherits "no
		// subscription and no credit" as a finding instead of re-reading it, and it can
		// never fire for a caller who already has either.
		if fund(c, w) {
			return "" // funded on this request; the wallet the debit writes now holds money.
		}
		c.Log().Info("spend: no subscription and no credit", "org", w.Ledger, "account", w.Account, "path", path)
		return ReasonUnpaid
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
func unresolved(c *zip.Ctx, why, path string) string {
	if Switch(SwitchPaywallStrict) {
		c.Log().Warn("spend: standing unresolvable; refusing (strict)", "why", why, "path", path)
		return ReasonUnresolved
	}
	c.Log().Warn("spend: standing unresolvable; admitting", "why", why, "path", path)
	return ""
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
