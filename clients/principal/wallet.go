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
	return Wallet{Ledger: ledger, Account: Subject(c, ledger)}, true
}

// Subject resolves the ACCOUNT half of the address — the wallet key within ledger —
// by feeding account.Payer the credential the identity boundary minted. It is the ONE
// place a request becomes a wallet key, so the gate, the debit, the balance view and
// the paywall address the same wallet by construction rather than by four call sites
// that happen to agree.
//
// THE NAME HALF IS X-User-Name, NOT X-User-Id. Both minters set X-User-Id from the
// JWT `sub`, and IAM's `sub` is a UUID; X-User-Name is the IAM USERNAME the boundary
// mints from the validated `name`/`preferred_username` claim — expressly "the `name`
// half of <owner>/<name>" (auth_identity.go username). Reading the id instead
// addressed "hanzo/<uuid>", a wallet NO funding path can name: an admin grant credits
// the org pool, a finance deposit names "<org>/<username>", and the ai gate + debit
// resolve the username too. So it was a ghost — always $0, never fundable, and the
// paywall's credit leg read it. This is the third recurrence of one bug (see the two
// above): every time, two layers derived the same address two ways.
//
// ONE Payer CALL, so the signed claim ALWAYS decides. The name is resolved first and
// the credential is built once: a two-branch version that took a PayerOf shortcut when
// the username was absent silently dropped `billing_account`, and a token whose
// signature says "person:acme/bob" would have gated the acme POOL. The claim is the
// whole reason the module exists; it cannot be conditional on a header's shape.
func Subject(c *zip.Ctx, ledger string) string {
	name := strings.TrimSpace(c.Header("X-User-Name"))
	if name == "" {
		name = nameOf(strings.TrimSpace(c.User()))
	}
	return account.Payer(account.Credential{
		Owner:   ledger,
		Name:    name,
		Account: BillingAccount(c),
	}).Subject()
}

// nameOf reduces X-User-Id to the `name` half of an account key. The header's shape is
// path-dependent — the gateway historically minted the bare username, the in-binary
// direct-Bearer path mints the UUID `sub`, and callers hold it as an "<owner>/<name>"
// key — so the only fact common to all three is that anything after a slash is the
// name and anything without one is already the name. It is a parse, never a decision:
// Payer alone decides who the resulting credential pays.
func nameOf(id string) string {
	if i := strings.IndexByte(id, '/'); i >= 0 {
		return id[i+1:]
	}
	return id
}
