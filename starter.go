// Copyright © 2026 Hanzo AI. MIT License.

package cloud

// starter.go PROVISIONS the credit a plan already includes, and it is the funding
// rung the spend gate stands on: middleware_spend.go reaches it at the one step
// where the answer would otherwise be 402, and admits when it lands.
//
// WHAT WAS DELETED AND WHY, BECAUSE THIS IS NOT THAT. An earlier starter grant ran
// as app-wide middleware, on first credential contact, and minted $5 into any wallet
// that did not exist yet. It was removed outright (41b23f12) on an argument that is
// correct and that this file has to answer rather than route around: credit into an
// org is an ADMIN decision, an automatic path that creates money is a mechanism
// rather than a feature, and a disabled money-mint is one flag away from an enabled
// one. Four things make this a different mechanism:
//
//	IT IS NOT MONEY. It is the plan's own advertised term — the "$5 free credit" the
//	catalog publishes — posted as a NON-CASH trial credit under the shared
//	`starter-credit` tag, which commerce's billing/bucket classifies into the Credit
//	bucket: spendable on non-premium metered usage, never refundable, never paid out.
//	Honouring what the catalog already promises is not an admin decision; it is the
//	catalog's decision, made when the plan was published.
//
//	IT IS NOT UNBOUNDED. One amount, from [credit.StarterCreditCents] — the constant
//	the whole stack shares and the catalog's entry plan advertises — and this function
//	takes no amount, so no request body and no caller can reach it.
//
//	IT IS NOT REPEATABLE. [starterRef] keys the deposit on the ADDRESS it credits, so
//	finance dedups it inside the same transaction as the insert. Once per wallet, ever.
//
//	IT IS NOT UNSCREENED. Every grant goes through [Decide] as a PRIVILEGED one, so a
//	signup the risk plane does not like receives nothing. That is the control for
//	"$5 × as many fake signups as you can make", which is the abuse a free grant
//	invites and the old one had no answer to.
//
// AND IT CANNOT BE ARMED ON ITS OWN. It has no switch. It is reached from ONE call
// site, inside the enforced branch of the spend ladder, so the mint and the refusal
// it cures are the same flag: there is no state in which money is being created and
// the paywall is not enforcing, and none in which the paywall is enforcing and a new
// customer cannot be funded. That is the structural answer to "one flag away".
//
// IT CANNOT RUN IN A PROCESS WITH NO LEDGER, and needs no branch to say so. The rung
// is reached only when [Stand] proved Unpaid, and Stand can only prove Unpaid when
// the ledger ANSWERED (creditIn's ok bit) — a split deploy answers Unknown and takes
// the unresolved posture instead. So the old cross-process grant op, which existed to
// let a binary with no books ask for money, has nothing to do here and stays deleted.

import (
	"strings"

	"github.com/hanzoai/account"
	"github.com/hanzoai/cloud/apps/finance"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/plane"
	"github.com/hanzoai/commerce/billing/credit"
	"github.com/hanzoai/commerce/billing/creditledger"
	"github.com/zap-proto/zip"
)

// fund provisions w's included credit and reports whether the wallet now holds
// money. False is the ordinary answer — it means this request stays refused — and it
// is never an error: funding is not an authorization decision, so a wallet that could
// not be funded is simply a wallet the gate goes on to refuse, in its own words.
//
// The three legs are asked cheapest-first and each one alone is decisive:
// [opening] reads the books, the screen asks the model, and the post writes.
func fund(c *zip.Ctx, w principal.Wallet) bool {
	if !opening(c, w) {
		return false
	}
	if !admitted(c, w) {
		return false
	}
	led := creditledger.Get()
	if led == nil {
		// Commerce's credit ledger is not injected in this process. The books are
		// here (Stand proved that) but the ONE credit-write path is not, and cloud
		// does not grow a second one to cover it.
		return false
	}
	// THE AMOUNT IS THE CATALOG'S, read from the constant the catalog is checked
	// against (starter_test.go). This call takes no amount from anywhere else.
	//
	// THE TAG IS WHAT MAKES IT PROMOTIONAL. billing/bucket.DepositKind reads
	// `starter-credit` into the Credit bucket — non-cash, non-refundable,
	// non-payout-able, and gated out of premium models and GPUs upstream — so this
	// grant is spendable on ordinary inference and is not a prepaid deposit. No
	// ExpiresAt: cloud's finance ledger holds no expiry, and stating a term the
	// books cannot enforce would be a promise made by a comment.
	_, cents, err := led.Credit(c.Context(), creditledger.CreditInput{
		Org:            w.Ledger,
		Subject:        w.Account,
		Currency:       creditUnit,
		Reason:         "welcome",
		Tag:            credit.StarterCreditTag,
		IdempotencyKey: starterRef(w.Account),
		AmountCents:    credit.StarterCreditCents,
	})
	if err != nil {
		c.Log().Warn("starter: the included credit could not be posted",
			"org", w.Ledger, "account", w.Account, "err", err)
		return false
	}
	// THE BALANCE DECIDES, NOT THE CALL. cents is the account read back by the same
	// authority, address and currency the gate's own credit leg reads (ledger.Credit
	// → finance.Balance), so this is that answer and not a second opinion about it.
	// It matters on the one path where a post succeeds and funds nothing: a REPLAY,
	// where the ref was already spent, returns the original entry and a zero balance.
	if cents <= 0 {
		return false
	}
	c.Log().Info("starter: a new account received its plan's included credit",
		"org", w.Ledger, "account", w.Account, "cents", credit.StarterCreditCents, "balance", cents)
	return true
}

// opening reports whether w is a wallet at the START of an account's life — the only
// wallet a starter credit belongs to.
//
// THE ZERO BALANCE IS NOT ASKED HERE, because it is already proven. This runs only
// where [Stand] returned Unpaid, which requires BOTH authorities to have answered and
// both to have said no — so "no subscription" and "no credit" are the ladder's
// findings, not facts to go and re-read. Re-reading them would be a second opinion
// about the same books, one request apart from the first.
//
// NEW IS NOT UNSEEN, and lifetime usage is the leg that tells them apart. A zero
// balance alone says nothing about history: an account that burned through its credit
// reads exactly like one that never had any, and on the day enforcement is switched
// on EVERY unfunded org in the fleet reaches this rung for the first time. Granting on
// a zero balance would hand a retroactive credit to all of them. So a wallet qualifies
// only if it has never spent. (Known edge, stated: an account refunded to exactly zero
// that never metered anything reads as new and is granted once. It cannot recur —
// [starterRef] is permanent.)
//
// THE SHARED SIGNUP ORG IS EXCLUDED. Its members are strangers to each other, so
// account.Payer resolves each to a PERSON wallet inside Hanzo's own brand books.
// Someone parked there has a login, not an account: funding them would put consumer
// credit into the platform's own ledger, and would pay the same human twice when IAM's
// first-run provision moves them into an org of their own and funds that. It is also
// the cheapest identity on the platform to mint, which is the other half of the reason.
//
// ONLY THE CALLER'S HOME ORG IS FUNDED, and that is the multiplication guard. The payer
// of record is the SELECTED org, and IAM's signed `orgs` claim lets a person act in any
// org they belong to — so funding "whichever wallet is paying" would mint one credit per
// org to anyone who can create them, which is unbounded. Membership alone is not enough
// either: a founder is a member of every org they create. What distinguishes the one org
// that is theirs is POSITION — IAM builds the set home-first from the authoritative user
// row, so orgs[0] is their own and everything joined or created follows it.
// [principal.Owner] IS that value, read from the same header SanitizeIdentity mints it
// into. Orgs created as ADDITIONAL never move the caller, so they can never occupy
// position 0 and are never funded.
//
// Comparison is BYTE-EXACT and safely so: the identity boundary admits a selected org
// only on equality with an entry of the signed set, and Owner is entry zero of that same
// set. A case difference means the two values did not come from one claim, which is a
// reason to withhold rather than to normalise until they match.
func opening(c *zip.Ctx, w principal.Wallet) bool {
	if w.Ledger == "" || w.Account == "" {
		return false
	}
	if strings.EqualFold(w.Ledger, account.SignupOrg) {
		return false
	}
	if home := principal.Owner(c); home == "" || home != w.Ledger {
		return false
	}
	fin := finance.Current()
	if fin == nil {
		return false
	}
	// Lifetime usage, on the LIVE books. finance sums this per org, which for a real
	// customer org is the wallet itself. An unreadable sum is not a zero one: a
	// history we could not read is a history we must not grant against.
	used, err := fin.SumUsageSince(c.Context(), w.Ledger, false, 0)
	return err == nil && used == 0
}

// admitted asks the risk plane whether this account may receive its credit, and it is
// the whole of the anti-abuse control on free money.
//
// IT IS A PRIVILEGED QUESTION, STATED AND NOT DERIVED. [Privileged] classifies the
// REQUEST's own path, and the request here is an inference call — not a grant surface
// — so an unset bit would leave a scorer outage minting balance on every unfunded
// wallet that asks. Setting it selects [Decide]'s fail-CLOSED branch: a scorer that is
// installed and cannot answer WITHHOLDS the grant. The caller is not harmed by that —
// they get the gate's ordinary 402 and can pay — which is exactly the asymmetry that
// makes failing closed correct here and failing open correct on the traffic path.
//
// AN ABSENT SCORER STILL GRANTS, and that is [Decide]'s policy rather than a hole
// opened here: refusing to onboard customers because the risk app is not part of a
// deployment is not a defense, it is a product that cannot be operated. The same
// exemption covers a scorer that has deadlocked. Both carry a Refusal, both are logged,
// and neither is silent.
//
// THE STAGE IS SIGNUP because that is the question being asked — is this account real,
// and is it one account? — and the SUBJECT is the payer, on the same key the credit
// door's own screen writes its settlements under. One key, one history: a fraudster's
// grants and their payments accrue together instead of on two identities that each look
// quiet.
//
// WHAT IS STATED IS WHAT WAS OBSERVED. The address our own edge resolved and the
// jurisdiction it resolved from it — the geography and device-fan-out axes. The VALUE
// axis is deliberately absent: every starter credit is the same amount, so stating it
// would be a constant dressed as an observation. [Facts] drops whatever the edge could
// not resolve, because an axis sent as the empty string pools every caller we could not
// place into one very busy identity.
func admitted(c *zip.Ctx, w principal.Wallet) bool {
	ip := ClientIP(c)
	v := Decide(c.Context(), w.Ledger, RiskQuery{
		Stage:      StageSignup,
		Subject:    RiskSubject{Kind: plane.KindPayer, ID: w.Account},
		Privileged: true,
		Signals: Facts(map[string]string{
			"ip":                ip,
			plane.SignalPeer:    ip,
			plane.SignalCountry: ClientCountry(c),
		}),
	})
	// EVERY decision is recorded, allows included: an unscored allow and a clean one
	// are different rows and the Refusal is what tells them apart.
	c.Log().Info("starter: screened",
		"org", w.Ledger, "account", w.Account, "action", v.Action, "scored", v.Scored(),
		"refusal", v.Refusal, "cause", v.Cause, "score", v.Score, "shape", v.Shape, "policy", v.Policy)
	return v.Allowed()
}

// starterRef is the grant's idempotency key: the ADDRESS it credits, and nothing else.
//
// THE ADDRESS AND NOT THE ORG. finance dedups on (kind, ref) WITHIN one org's books,
// and in an org whose members hold their own wallets two members share the org and not
// the address — so an org-keyed ref would credit the first member and answer the second
// with ErrRefTaken, a grant refused because somebody else already had one. The address
// is the org itself for a pooled tenant, so the two coincide exactly where they should.
//
// It carries no timestamp, nonce, request id or user id, deliberately: any of those
// would make a retry a SECOND grant, which is the whole failure this key prevents. A
// re-login, a redeploy, a retried request and two concurrent requests all derive this
// same string, and finance dedups it inside the same transaction as the insert.
func starterRef(subject string) string {
	return "starter:" + strings.ToLower(strings.TrimSpace(subject))
}
