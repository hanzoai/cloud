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
// request may not touch money at all (no validated principal — never key a ledger
// on a restored, client-forged X-Org-Id, or an anonymous caller could probe and
// drain a victim org's balance).
//
// Ledger is the HOME org (BillingOrg: the validated `owner` claim, effective-org
// fallback), NOT the effective X-Org-Id — so a platform SuperAdmin masquerading
// into another org spends from the admin org's books, never the org being acted on.
// Account is resolved by the ONE rule (account.Payer) from the signed
// `billing_account` claim, falling back to Payer's legacy rule for a pre-claim
// token. An org-less validated principal keeps the bare subject: no org names no
// account, but a subject can still gate.
func WalletOf(c *zip.Ctx) (Wallet, bool) {
	if !Validated(c) {
		return Wallet{}, false
	}
	ledger, _ := BillingOrg(c) // "" only for a validated principal with no usable org
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
