package host

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
//   - zen's commerceGate (apps/zen/zen.go) gates the caller's ORG POOL, and every
//     self-serve signup lands in the shared "hanzo" org whose pool is funded — so a
//     brand-new $0 account reads a six-figure balance and sails through;
//   - zen's own Tenant.Valid() is `t.Org != ""` — auth, not balance.
//
// SpendGate is one gate, at one place, on the one predicate, covering the LLM paths
// and the non-LLM resource trees alike (Billable), so neither can be fixed without
// the other.

import (
	"context"
	"github.com/hanzoai/cloud/sdk"
	"net/http"

	"github.com/hanzoai/cloud/sdk/meter"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/zap-proto/zip"
)

// PlanChecker is the ONE commerce read this gate needs: does org X hold a LIVE
// (active or trialing) PAID plan? It is a consumer-defined interface satisfied
// structurally by the co-resident commerce client (clients/commerce.ActivePaidPlan) —
// an OPTIONAL capability resolved from Deps.Commerce by type-assertion, so the narrow
// CommerceClient interface is untouched and a commerce build that cannot answer
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
// DEFAULT OFF, AND THE FLIP IS AN OWNER'S ACT. A brand-new org's wallet is $0 and
// nothing funds it at signup — there is no starter credit, and every tier's daily
// allowance is 0 (commerce billing/tier) — so enforcing refuses a new account's first
// metered call until it subscribes or adds credit. The refusal names the console
// page that does either (BillingURL), which is what makes that a checkout rather
// than a dead end.
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
//  6. UNRESOLVABLE standing .................. 402 unresolved (retry), logged WARN.
//  7. proven unpaid .......................... 402 with the actionable refusal.
//
// FAIL CLOSED. A caller is admitted on a standing an authority CONFIRMED, and on
// nothing else. The two refusals stay distinct because their cures differ: (7)
// requires PROOF — both authorities answered and both said no — and names the page
// that buys standing, while (6) says only that standing could not be confirmed and
// its cure is a retry. Each leg admits on its own authority's answer, so an
// unreachable commerce still admits a caller the ledger shows funded, and an
// unreadable ledger still admits a caller commerce shows subscribed: one authority
// being unreachable refuses only the callers whose standing rests on it alone. Every
// unresolved refusal logs at WARN with the reason, so a sustained one pages.
//
// The Free plan's pooled lane is not decided here. With the switch off (step 1) the
// gate is inert, and ai serves the pool behind its own allowance counter
// (apps/ai: spent), whose posture this gate does not change.
//
// An unresolvable WALLET is an Unknown, not an admit: principal.WalletOf refuses an
// unresolvable org, and "we could not work out who pays" must refuse AS UNKNOWN and
// take the strict posture, never be silently waved through as if it were free.
func SpendGate(commerce cloud.CommerceClient) zip.Handler {
	plans, _ := commerce.(PlanChecker)
	allow, _ := commerce.(AllowanceChecker)
	return func(c *zip.Ctx) error {
		if reason := standing(c, c.Method(), c.Path(), plans, allow); reason != "" {
			return Refuse(c, "", reason)
		}
		return c.Next()
	}
}

// standing IS the ladder above — every step of it — returning "" to admit and
// otherwise the reason to refuse with. It is split out of SpendGate for one
// reason: the ladder decides, and SpendGate only renders, and the OP CLIENT (Toll)
// has to reach the same decision through a channel that cannot write a body. Two
// copies of a seven-step money ladder is the drift this codebase has paid for
// three times; there is one, and both clients call it.
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
func standing(c *zip.Ctx, method, path string, plans PlanChecker, allow AllowanceChecker) string {
	if !Switch(SwitchPaywallEnforced) {
		return "" // dark — byte-identical to no gate at all.
	}
	if !Billable(method, path) {
		return ""
	}
	if c == nil || !principal.Validated(c) {
		return "" // the route's own auth answers; a 402 would mask a 401.
	}
	if principal.Super(c) {
		return "" // platform sudo, masquerade included.
	}
	w := meter.Payer(c)
	if w.Zero() {
		// No resolvable payer. Unknown, not unpaid, and never a free pass.
		return unresolved(c, "no resolvable wallet", path)
	}
	switch s := Stand(c.Context(), licence(c.Context(), plans, w.Org()), AllowanceIn(c.Context(), allow, w), w); {
	case s.Admits():
		return ""
	case s == Unknown:
		return unresolved(c, "billing authority unreadable", path)
	default:
		// No subscription and no credit is a refusal. An account is free to make;
		// the models are what cost.
		c.Log().Info("spend: no subscription and no credit", "org", w.Org(), "account", w.Subject(), "path", path)
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

// unresolved refuses a standing nobody could confirm, with the DISTINCT unresolved
// code, because the caller's cure is different — an unpaid caller must buy something,
// an unresolved one must retry.
func unresolved(c *zip.Ctx, why, path string) string {
	c.Log().Warn("spend: standing unresolvable; refusing", "why", why, "path", path)
	return ReasonUnresolved
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
// that Reachable guarantees are never gated — /v1/plan (what to buy, with prices) and
// /v1/billing (where to pay, subscribe or top up).
//
// A person needs a page rather than an API address, so an unpaid refusal also
// names the page that clears it on the caller's own brand (BillingURL: Hanzo's
// checkout, hanzo.ai/pay/amount/card, where a brand has one), in the sentence and
// as billingUrl. An agent reads the same two members every other
// money refusal carries (errmap.go guided): its brand's manifest and the API that
// takes a deposit. An unresolved one names none of the three: its cure is a retry.
type Refusal struct {
	Error      string `json:"error"`                // stable machine code: always "payment_required"
	Product    string `json:"product,omitempty"`    // the gated product id, when the gate is product-scoped
	Reason     string `json:"reason"`               // "unpaid" | "unresolved"
	Message    string `json:"message"`              // one human sentence
	Cure       []Cure `json:"cure"`                 // the ways to fix it, in the order to offer them
	BillingURL string `json:"billingUrl,omitempty"` // the page that fixes it, for a person
	Agent      string `json:"agent,omitempty"`      // the brand's agent manifest (AgentURL)
	Topup      string `json:"topup,omitempty"`      // the API that funds the balance (TopupPath)
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
	{Kind: "subscribe", URL: "/v1/plan"},
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
	if reason == ReasonUnresolved {
		return c.JSON(http.StatusPaymentRequired, Refusal{
			Error:   "payment_required",
			Product: product,
			Reason:  reason,
			Message: "billing could not be verified" + scope + "; retry shortly",
			Cure:    cures,
		})
	}
	url := cloud.BillingURL(c.Host(), "payment_required")
	return c.JSON(http.StatusPaymentRequired, Refusal{
		Error:      "payment_required",
		Product:    product,
		Reason:     reason,
		Message:    cloud.At("no active subscription"+scope+" and no prepaid credit; subscribe or add credits", url),
		Cure:       cures,
		BillingURL: url,
		Agent:      cloud.AgentURL(c.Host()),
		Topup:      cloud.TopupPath,
	})
}
