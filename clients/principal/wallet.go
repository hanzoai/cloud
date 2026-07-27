package principal

// wallet.go completes the money trio this package already carries: BillingOrg
// answers WHICH LEDGER pays, BillingAccount carries the signed claim naming the
// payer, and Wallet is the resolved ADDRESS the two produce together.
//
// hanzoai/account states the invariant this file exists to make unavoidable:
// "(Org, Subject) IS the money's address — Org names the ledger that holds it,
// Subject the key within that ledger. A deposit credits that address, the gate
// reads it, a usage debit spends it. One address, one answer, every caller."
//
// It is ONE function because the alternative has already shipped twice, and both
// times the same way — a gate keyed on the ORG POOL while the debit spent a
// PERSON's wallet:
//
//   - build.go wireFinance: "Keying both hooks on the org collapsed every member
//     onto the tenant's pool wallet: every new signup lives in 'hanzo', so a
//     brand-new $0 account read HANZO's balance and sailed through the gate."
//   - middleware_billing.go identityFromCtx: "this gate checked the pool's balance
//     while ai debited the person's, so a funded pool green-lit a request whose
//     usage drained an empty personal wallet, and an empty pool 402'd a funded
//     person."
//
// So any NEW money gate resolves its address here rather than re-deriving it. The
// rule itself is not implemented here either: it is account.Payer, the pure leaf
// every layer that touches money calls.

import (
	"strings"

	"github.com/hanzoai/account"
	"github.com/zap-proto/zip"
)

// Wallet is the money address a request spends from. Ledger names the org's books;
// Account is the key within them. For a normal caller Ledger is their own org and
// Account is either that org's pool or their personal account — account.Payer, and
// only account.Payer, decides which.
type Wallet struct {
	Ledger  string
	Account string
}

// WalletOf resolves the wallet this request spends from, or ok=false when the
// request may not touch money at all: no validated principal (never key a ledger on
// a restored, client-forged X-Org-Id, or an anonymous caller could probe and drain a
// victim org's balance), or no resolvable org.
//
// Ledger is the SELECTED org (BillingOrg) — the org the caller switched into, which
// the trust boundary already proved they belong to; a masquerading SuperAdmin is the
// one exception and spends from its own books. Account is resolved by the ONE rule
// (account.Payer) from the signed `billing_account` claim, falling back to Payer's
// legacy rule for a pre-claim token.
//
// AN UNRESOLVABLE ORG REFUSES. It used to discard BillingOrg's ok-bit and return a
// wallet with an EMPTY ledger, ok=true — and an empty ledger is not "no ledger", it
// is "whatever the next layer substitutes". The metering client substituted the
// BRAND org, so a principal whose owner claim carried a zero-width rune was gated
// against Hanzo's balance and, had the debit not errored on the empty org, would have
// spent it. There is no substitute payer; the ok-bit is the answer and it propagates.
func WalletOf(c *zip.Ctx) (Wallet, bool) {
	ledger, ok := BillingOrg(c) // composes Validated; refuses an unresolvable org
	if !ok {
		return Wallet{}, false
	}
	sub := strings.TrimSpace(c.User())
	acct := account.Payer(account.Credential{
		Owner:   ledger,
		Name:    sub,
		Account: BillingAccount(c),
	}).Subject()
	if acct == "" {
		acct = sub
	}
	return Wallet{Ledger: ledger, Account: acct}, true
}
