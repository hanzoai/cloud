// Copyright © 2026 Hanzo AI. MIT License.

package cloud

// middleware_starter.go funds a new account the first time it presents a
// credential, and it is the funding path SpendGate is sequenced behind
// (middleware_spend.go: "no ensureStarterCredit runs anywhere"). It does not
// enable enforcement; flipping SwitchPaywallEnforced stays a separate act.
//
// WHY FIRST CONTACT, AFTER TWO SEAMS THAT LOOKED BETTER AND WERE NOT. The grant
// was first wired into cloud's /v1/iam/onboard first-run branch. It shipped in
// v1.801.241, was present in the binary, and NEVER FIRED — twice over:
//
//   - api.hanzo.ai/v1/iam/* is routed to the IAM SERVICE at the edge, so cloud's
//     onboard handler is not on that path at all. The provisioning that creates
//     most orgs happens in a process that has no ledger.
//   - on console.hanzo.ai, where cloud DOES serve it, `additional := cr.owner != ""`
//     reads the EFFECTIVE org. A fresh IAM-v2 signup lands in the shared signup org,
//     so cr.owner is non-empty, every signup takes the "additional" branch, and the
//     first-run branch is unreachable.
//
// Both failures share one shape: the trigger depended on WHICH HOST served the
// request and WHICH COMPONENT created the org, and both of those vary. This seam
// depends on neither. It asks the only question with a stable answer — "does the
// wallet this credential spends from exist yet?" — and it asks it in the binary
// that owns the ledger. A grant here cannot be routed around.
//
// COST. This is a hot path: it runs on every request, authenticated or not. The
// steady state is a map load and nothing else — see starterSeen. Only the FIRST
// request per wallet per process touches the ledger.

import (
	"context"
	"strings"
	"sync"

	"github.com/hanzoai/account"
	"github.com/hanzoai/cloud/clients/finance"
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/hanzoai/commerce/billing/credit"
	"github.com/hanzoai/commerce/billing/creditledger"
	"github.com/zap-proto/zip"
)

// starterSeen is the fast path, and the reason this is mountable app-wide: a wallet
// resolved once in this process is never looked at again. The key is the wallet
// SUBJECT, which is globally unique (an org account is the bare slug, a person's is
// "<org>/<name>"), so no ledger name is needed to disambiguate.
//
// It is a CACHE, not the idempotency barrier. Losing it on restart costs one extra
// ledger check per active wallet and can never double-grant: the barrier is the
// address-keyed Ref inside finance.Deposit's insert transaction. Nothing here is
// load-bearing for money, which is why an unbounded sync.Map is safe — it holds one
// small string per wallet the process has served, and a wallet only enters it by
// presenting a valid credential.
var starterSeen sync.Map // subject -> struct{}

// StarterGrant returns the middleware serve.go mounts ahead of the billing gates, so
// a brand-new account is funded BEFORE the gate on that same request evaluates it —
// otherwise the very first request of every new signup would 402 the moment
// enforcement is switched on, which is the failure this whole path exists to prevent.
//
// It never blocks, never fails a request, and never rejects: funding is not an
// authorization decision. Every outcome falls through to c.Next(). If the grant
// fails the account simply holds no credit, and the gate — which reads the ledger,
// not a grant flag — refuses it. There is no path where a failed grant is mistaken
// for funding.
func StarterGrant() zip.Handler {
	return func(c *zip.Ctx) error {
		if w, ok := starterWallet(c); ok {
			if _, seen := starterSeen.Load(w.Account); !seen {
				// Mark BEFORE attempting: a wallet is looked at once per process
				// whatever happens, so a persistently failing grant costs one attempt
				// rather than one per request. A restart retries it.
				starterSeen.Store(w.Account, struct{}{})
				_, _ = EnsureStarterCredit(c.Context(), w)
			}
		}
		return c.Next()
	}
}

// starterWallet resolves the wallet a request spends from, or ok=false when this
// request must not be funded.
//
// The address is principal.WalletOf — the SAME resolution the spend gate performs
// (middleware_billing.go identityFromCtx) and the same one account.Payer gives the ai
// balance reader. It is not re-derived here, because a second derivation is a second
// rule, and two rules disagreeing about who pays is the bug this package's wallet.go
// documents twice.
//
// THE SHARED SIGNUP ORG IS EXCLUDED, deliberately. Its members are strangers to each
// other (hanzoai/account: "a shared org is not a shared wallet"), so Payer resolves
// each to a PERSON account inside Hanzo's own staff org. Someone parked there has not
// created an account yet — they have a login. Funding those wallets would put consumer
// starter credit inside the platform's own books and pay out twice for one human, who
// gets their grant when they land in a real org.
//
// ONLY THE CALLER'S HOME ORG IS FUNDED, and this is the abuse gate. The payer of
// record is the SELECTED org (principal.BillingOrg reads X-Org-Id; IAM's signed `orgs`
// claim lets a person pick any org they belong to). So a person who belongs to five
// orgs can present five different wallets, and funding "whichever wallet is paying"
// would mint $5 per org to anyone who can create orgs — which is unlimited, and whose
// personal slugs auto-suffix. Requiring the selected org to be the caller's HOME org
// (X-User-Owner, the validated `owner` claim, which no client can set) makes the grant
// follow the person rather than the selection.
//
// It costs nothing legitimate. IAM's first-run provision MOVES the founder into the
// org it creates, so a real new account's home IS the new org and it is funded. Orgs
// created as ADDITIONAL are explicitly created without moving the caller
// (clients/account: "create it WITHOUT moving them"), so they are never anyone's home
// and never funded — which is the intended rule, stated once, enforced by the address
// rather than by a flag on a handler that only one of several paths reaches.
func starterWallet(c *zip.Ctx) (principal.Wallet, bool) {
	w, ok := principal.WalletOf(c)
	if !ok || w.Ledger == "" || w.Account == "" {
		return principal.Wallet{}, false
	}
	if strings.EqualFold(w.Ledger, account.SignupOrg) {
		return principal.Wallet{}, false
	}
	if home := principal.Owner(c); home == "" || !strings.EqualFold(home, w.Ledger) {
		return principal.Wallet{}, false // acting in an org that is not this caller's home
	}
	return w, true
}

// EnsureStarterCredit grants the one-time starter credit to wallet and reports the
// balance it left behind, or (0,nil) when the wallet was not eligible.
//
// THE ADDRESS IS THE CALLER'S, NOT A GUESS. It credits (Ledger, Account) exactly as
// principal.WalletOf resolved it, passing Account through as CreditInput.Subject. For
// a real org Payer yields the org pool and Subject is the bare slug; the day IAM mints
// per-member `billing_account` claims it yields a person account and the same field
// carries it. Either way the credit lands where the gate looks, because both come from
// one call to account.Payer.
//
// ELIGIBILITY IS "NEW", NOT "UNSEEN". A first-contact trigger sees every EXISTING
// account too — on the first request after a deploy, every org in the fleet is
// unseen. Granting on unseen alone would hand a retroactive $5 to the entire customer
// base. So a wallet qualifies only if it has no money and no history: a zero balance
// AND zero lifetime usage. A funded org is skipped, an org that has ever spent is
// skipped, and a genuinely new account passes. (Known edge, stated: an old account
// that was refunded to exactly zero and never metered reads as new and is granted
// once. It cannot recur — the Ref below is permanent.)
//
// AMOUNT IS SERVER-AUTHORITATIVE. credit.StarterCreditCents, the shared constant, read
// from the server's own dependency. This function takes no amount and no request body,
// so there is no client field that could reach it.
func EnsureStarterCredit(ctx context.Context, w principal.Wallet) (int64, error) {
	led := creditledger.Get()
	fin := finance.Current()
	if led == nil || fin == nil {
		return 0, nil // money layer not co-resident (split deploy): nothing to grant into
	}

	bal, err := fin.Balance(ctx, w.Ledger, w.Account, "usd", false)
	if err != nil {
		return 0, err
	}
	if bal.Cents() != 0 {
		return 0, nil // funded — not a new account
	}
	// Lifetime usage. finance sums this per ORG; for a real org the wallet IS the org
	// pool, so the two coincide. It is the leg that distinguishes "never had money"
	// from "spent it all", which a zero balance alone cannot.
	used, err := fin.SumUsageSince(ctx, w.Ledger, false, 0)
	if err != nil {
		return 0, err
	}
	if used != 0 {
		return 0, nil // has spent before — not a new account
	}

	_, balanceCents, err := led.Credit(ctx, creditledger.CreditInput{
		Org:            w.Ledger,
		Subject:        w.Account,
		Currency:       "usd",
		Reason:         "welcome",
		Tag:            credit.StarterCreditTag,
		IdempotencyKey: starterRef(w.Account),
		AmountCents:    credit.StarterCreditCents,
	})
	if err != nil {
		return 0, err
	}
	return balanceCents, nil
}

// starterRef is the grant's idempotency key: the ADDRESS it credits, and nothing else.
// Keying on the address is what makes this once per ACCOUNT rather than once per
// session, per login, or per process — a retry, a re-login, a restart that empties
// starterSeen, and two concurrent requests all derive the same key, and finance dedups
// on it inside the same transaction as the insert. It deliberately carries no
// timestamp, nonce, request id or user id: any of those would make a replay a SECOND
// grant, which is the whole failure this key exists to prevent.
func starterRef(subject string) string {
	return "starter:" + strings.ToLower(strings.TrimSpace(subject))
}
