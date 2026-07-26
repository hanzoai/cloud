package entitlements

// standing.go answers the ONE question the paywall asks, and nothing else: how does
// this caller STAND, commercially, for `product` right now? It is pure decide — no
// HTTP, no flags, no refusal shaping. require.go applies the answer.
//
// TWO WAYS TO PAY, ONE ANSWER. The rule is "an active monthly subscription OR a
// positive pay-as-you-go pre-pay balance". Those are two INDEPENDENT facts held by
// two INDEPENDENT authorities, and this file is the single place they compose:
//
//   - SUBSCRIPTION — commerce.CheckEntitlement(org, product). The plan→product
//     policy lives ONCE, in @hanzo/plans, and is resolved there. We read its
//     verdict; we never restate it.
//   - PREPAID CREDIT — the caller's wallet on the native finance ledger
//     (clients/finance, published at boot as finance.Current()). That is the SAME
//     ledger the ai gate reads, the edge meter debits, and an admin grant credits,
//     read at the SAME address (principal.Wallet) — see below.
//
// THE ADDRESS IS LOAD-BEARING. A money gate that reads a different wallet than the
// debit writes is the bug this codebase has already shipped twice, both times by
// keying the ORG POOL: "every new signup lives in 'hanzo', so a brand-new $0
// account read HANZO's balance and sailed through the gate" (build.go wireFinance).
// Read on the org pool, this paywall would admit every free signup in the shared
// org for as long as the platform's own pool is funded — a total bypass. So the
// credit leg reads principal.WalletOf's address and nothing else.
//
// CREDIT IS EXACT. finance.Balance returns money.Amount — 18-decimal atto-USD over
// big.Int, the same integer an on-chain uint256 credit balance holds. The admit
// boundary is Sign() > 0: zero denies, ONE ATTO admits. There is no threshold, no
// cent-flooring, and no float anywhere on this path.
//
// PROOF, NOT ABSENCE. A caller is Unpaid only when BOTH authorities ANSWERED and
// both said no. If either could not answer, the standing is Unknown — this file
// never converts "the oracle is down" into "this customer has not paid". What to
// DO about an Unknown is enforcement's decision (require.go), not the decide's.

import (
	"context"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/finance"
	"github.com/hanzoai/cloud/clients/principal"
)

// creditUnit is the asset the prepaid wallet is denominated in. One asset ships
// (USD, 18-decimal-exact); finance selects the ledger file from it.
const creditUnit = "usd"

// Standing is a caller's resolved commercial standing for one product. The zero
// value is Unknown, which is the honest default: a question not yet asked has no
// answer, and "no answer" is never "has not paid".
type Standing uint8

const (
	// Unknown — at least one authority could not answer (commerce not co-resident,
	// entitlement query failed, ledger unreadable). NOT a statement about the caller.
	Unknown Standing = iota
	// Unpaid — BOTH authorities answered and both said no: no plan licenses the
	// product and the wallet holds no credit. The only proven refusal.
	Unpaid
	// Subscribed — an active @hanzo/plans entitlement licenses the product.
	Subscribed
	// Funded — the wallet holds a positive prepaid balance to burn down.
	Funded
)

// Admits reports whether this standing lets a request through on its own merits.
// Unknown does NOT admit here — it is not an admit, it is an absence, and the
// posture that resolves it is enforcement's (require.go), deliberately not hidden
// inside this predicate.
func (s Standing) Admits() bool { return s == Subscribed || s == Funded }

// String renders the standing for logs.
func (s Standing) String() string {
	switch s {
	case Unpaid:
		return "unpaid"
	case Subscribed:
		return "subscribed"
	case Funded:
		return "funded"
	default:
		return "unknown"
	}
}

// Resolve reports how a caller stands for product.
//
//	org     — the tenant whose PLAN is checked (the validated, effective org).
//	product — the @hanzo/plans product id, BARE (commerce prepends the
//	          "licensing.product:" prefix itself).
//	w       — the money address the credit leg reads (principal.WalletOf).
//
// commerce may be nil (not co-resident); the finance ledger may be unpublished
// (split deploy). Either absence makes that leg unresolvable, never a "no".
//
// The legs are evaluated cheapest-decisive-first: an entitled caller never touches
// the ledger. A caller with no subscription always does — that is the pay-as-you-go
// path, and it is the common case for a prepaid customer.
func Resolve(ctx context.Context, commerce cloud.CommerceClient, org, product string, w principal.Wallet) Standing {
	entOK, entActive := licensedBy(ctx, commerce, org, product)
	if entOK && entActive {
		return Subscribed
	}
	creditOK, funded := creditIn(ctx, w)
	if creditOK && funded {
		return Funded
	}
	if entOK && creditOK {
		// Both authorities answered; both said no. This — and only this — is proof.
		return Unpaid
	}
	return Unknown
}

// licensedBy asks the ONE entitlement authority whether org's plan licenses product.
// ok is false when commerce could not answer (nil client, query error, or a nil
// result with no error — machinery that returned nothing has not said "no").
func licensedBy(ctx context.Context, commerce cloud.CommerceClient, org, product string) (ok, active bool) {
	if commerce == nil {
		return false, false
	}
	ent, err := commerce.CheckEntitlement(ctx, org, product)
	if err != nil || ent == nil {
		return false, false
	}
	return true, ent.Active
}

// creditIn reads the caller's prepaid balance from the native finance ledger and
// reports whether it is positive. ok is false when the ledger is not published
// (split deploy / money layer not co-resident), when the request carries no wallet,
// or when the read FAILS — "a balance that cannot be read is unknown, never zero"
// (clients/finance.Balance). A zero balance IS an answer: (true, false).
//
// The read is against the LIVE books (test=false). Sandbox money must never buy
// live product access.
func creditIn(ctx context.Context, w principal.Wallet) (ok, funded bool) {
	if w.Account == "" {
		return false, false
	}
	fin := finance.Current()
	if fin == nil {
		return false, false
	}
	bal, err := fin.Balance(ctx, w.Ledger, w.Account, creditUnit, false)
	if err != nil {
		return false, false
	}
	return true, bal.Sign() > 0
}
