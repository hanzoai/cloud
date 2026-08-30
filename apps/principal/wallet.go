package principal

// wallet.go completes the money trio this package already carries: BillingOrg
// answers WHICH LEDGER pays, BillingAccount carries the signed claim naming the
// payer, and Payer is the resolved ADDRESS the two produce together.
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
//   - build.go installFinance: "Keying both hooks on the org collapsed every member
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
	"context"
	"strings"

	"github.com/hanzoai/account"
	"github.com/zap-proto/zip"
)

// THE ADDRESS IS ONE VALUE, AND IT IS account.Account. There used to be a Wallet
// struct here holding two strings, Ledger and Account, and it was a lossy copy of a
// value that already exists: an account.Account answers Org() for the books and
// Subject() for the key within them, and cannot be built with the halves swapped.
// Splitting it into a pair is what let a gate read one half while a debit spent the
// other — twice, in the incidents recorded above. So nothing here flattens: these
// functions return the resolved account, and a caller that needs a string asks it
// for the half it means at the moment it needs one.

// Payer is the money address this request spends from — the resolved account, and
// the zero Account when the request may not touch money at all: no validated
// principal (never key a ledger on a restored, client-forged X-Org-Id, or an
// anonymous caller could probe and drain a victim org's balance), or no resolvable
// org.
//
// It exists because the in-handler meter (cloud.Meter) used to address money
// with ONE string, and the string it was handed was the LEDGER — the org. For a
// tenant org that is the same value account.Payer returns, so nothing looked wrong;
// in the SHARED SIGNUP ORG it is not, and that is exactly where a self-serve stranger
// lives. There, the balance the customer is shown, the edge gate, and the paywall all
// read <org>/<username> while every metered product debited the bare <org> — the
// platform's own pool. A stranger's top-up was therefore unspendable, and their usage
// landed on Hanzo's books. spend.go names this premise ("prepaid billing is per-org")
// as false in so many words; this is the accessor that lets the meter stop assuming
// it, and Gate now takes the account so no surface can pass the other one by mistake.
//
// The ledger half is the SELECTED org (BillingOrg) — the org the caller switched
// into, which the trust boundary already proved they belong to; a masquerading
// SuperAdmin is the one exception and spends from its own books. The account half is
// resolved by the ONE rule (account.Payer) from the signed `billing_account` claim,
// falling back to that rule's own legacy branch for a pre-claim token.
//
// AN UNRESOLVABLE ORG REFUSES, and refuses by being ZERO rather than by a second
// return value. An earlier version discarded BillingOrg's ok-bit and answered with an
// EMPTY ledger and ok=true — and an empty ledger is not "no ledger", it is "whatever
// the next layer substitutes". The metering client substituted the BRAND org, so a
// principal whose owner claim carried a zero-width rune was gated against Hanzo's
// balance and, had the debit not errored on the empty org, would have spent it. There
// is no substitute payer. A zero Account reaches Meter.Authorize as ErrNoLedger —
// the same fail-closed answer an empty org gave — so a caller hands this straight to
// the gate and wants the gate's own refusal, not a second branch of its own.
func Payer(c *zip.Ctx) account.Account {
	ledger, ok := BillingOrg(c) // composes Validated; refuses an unresolvable org
	if !ok {
		return account.Account{}
	}
	return PayerIn(c, ledger)
}

// PayerFor resolves the account of a NAMED member, for a caller that is addressing
// someone other than itself — an operator crediting a customer, a reconciliation
// moving a parked balance. It is the deposit-side twin of Payer and it asks the
// SAME rule, so a credit lands where the gate will look: name a member of a pooled
// tenant org and Payer answers that org's pool, because in a pooled org there is no
// member wallet to credit and money put in one could never be spent.
//
// It takes an org and a bare name rather than a request because there is no request
// — the target is not the caller. There is no credential to read, so no signed
// `billing_account` claim participates: this resolves where a member's OWN token
// would resolve, which is the account their spend will be gated on.
//
// A blank name is the org itself (the pool). A name is a NAME, not a key: a "/" in
// it would silently address a different account than the one asked for, so it is
// refused rather than flattened.
//
// THE BLANK NAME IS ADDRESSED, NOT INFERRED. account.Payer refuses a nameless
// credential in the signup org, because there a missing name means a token FAILED
// to resolve its person and answering with the org would hand a stranger the
// platform's own balance. That refusal is about credentials, and this function has
// none — it says so three paragraphs up: the target is not the caller, and there is
// no request to read. Here a blank name is a caller deliberately naming the ORG'S
// account, which is how the platform pool itself is credited. Routing it through
// the credential rule would make funding that pool impossible in the one org that
// holds it, so the org account is addressed directly and Payer is asked only when
// there is a name for it to resolve.
func PayerFor(org, name string) account.Account {
	org = strings.TrimSpace(org)
	name = strings.TrimSpace(name)
	if org == "" || strings.Contains(name, "/") {
		return account.Account{}
	}
	acct := account.Org(org)
	if name != "" {
		acct = account.Payer(account.Credential{Owner: org, Name: name})
	}
	return acct
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
func PayerIn(c *zip.Ctx, ledger string) account.Account {
	name := strings.TrimSpace(c.Header("X-User-Name"))
	if name == "" {
		name = nameOf(strings.TrimSpace(c.User()))
	}
	return account.Payer(account.Credential{
		Owner:   ledger,
		Name:    name,
		Account: BillingAccount(c),
	})
}

// nameOf reduces X-User-Id to the `name` half of an account key. The header's shape is
// path-dependent — the gateway historically minted the bare username, the in-binary
// direct-Bearer path mints the UUID `sub`, and callers hold it as an "<owner>/<name>"
// key — so the only fact common to all three is that anything after a slash is the
// name and anything without one is already the name. It is a parse, never a decision:
// Payer alone decides who the resulting credential pays.
func nameOf(id string) string {
	if _, after, ok := strings.Cut(id, "/"); ok {
		return after
	}
	return id
}

// PayerFrom is [Payer] where only the CONTEXT crossed the client — the ZAP plane,
// MCP's tools/call, and the fleet-agent transport, none of which carry an HTTP
// request. It is the Org/OrgFrom pair again: one fact, one rule, read from either
// side.
//
// THE HOLE IT CLOSES. Every metered surface resolved its payer from the request
// alone, so on a transport with no request the wallet came back empty — and empty
// means "nobody to bill", which every meter correctly treats as no gate and no
// debit.
// The result was that an identified caller who reached a paid operation over the
// agent plane got it free and unrecorded: a carrier number ordered on a zero
// balance, with the platform paying. The caller was never anonymous; only the
// TRANSPORT was different, and money must not be a property of the transport.
//
// It asks the SAME two questions the request path asks, through the same
// functions: OrgOf refuses a blank user or org (the plane's form of "validated" —
// these fields are server-minted by the identity boundary, never client-stated),
// ledgerOf applies the masquerade rule, and account.Payer alone decides the
// account within the ledger. The one input it cannot see is the signed
// `billing_account` claim, which rides an HTTP header; absent it, account.Payer
// falls back to exactly the rule it uses for a pre-claim token.
func PayerFrom(ctx context.Context) account.Account {
	c := zip.CallerOf(ctx)
	org, ok := OrgOf(c.User, c.Org)
	if !ok {
		return account.Account{}
	}
	name := strings.TrimSpace(c.Name)
	if name == "" {
		name = nameOf(strings.TrimSpace(c.User))
	}
	return account.Payer(account.Credential{
		Owner: ledgerOf(org, strings.TrimSpace(c.Owner), c.Admin),
		Name:  name,
	})
}
