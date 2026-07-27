// Copyright © 2026 Hanzo AI. MIT License.

package account

// starter.go grants the one-time signup credit, and it is the funding path
// SpendGate is waiting on. That gate ships dark for one stated reason
// (middleware_spend.go): "no ensureStarterCredit runs anywhere", so a brand-new
// signup's wallet is $0 with no self-service way to fund it, and enforcing on that
// state trades a revenue leak for a total signup outage. This file removes the
// reason; flipping the switch stays a separate, deliberate act.
//
// WHERE IT FIRES, AND WHY HERE. A grant is only real if it lands on the address the
// gate reads, so the seam has to be somewhere that knows that address. Three places
// were candidates and two cannot answer:
//
//   - IAM. It owns provisioning, but it has no commerce client and no ledger, and its
//     own schema forbids the write — schema/user.go: "Balance ... is authoritative in
//     Commerce, not here — do not write it from IAM". Granting there would mean a new
//     IAM→commerce dependency inside the provisioning TRANSACTION, where a commerce
//     blip becomes a failed signup. Money must not be able to break identity.
//   - Commerce, keyed on a first-seen org. Commerce holds the ledger but not the
//     credential, and WHO PAYS is a property of the credential (account.Payer). An
//     org-keyed hook cannot name a member's account, and it never sees a signup that
//     has not spent yet.
//   - Cloud's onboarding handler — HERE. It is the one place holding both: it is the
//     account-creation event (it drives the IAM provision), and it runs in the binary
//     that owns the ledger the gate reads, so the grant is an in-process ledger write
//     with no second service to be down.
//
// ONBOARDING IS THE ACCOUNT-CREATION EVENT FOR MONEY, and that is measured, not
// assumed. A federated signup (GitHub/Google/GitLab) and an email-code signup both
// create a USER but NO org — IAM's federation path calls users.Create and stops. Such
// a user lands in the shared signup org, which is not a tenant and whose members are
// strangers; they have no org of their own until they come through here. So every
// real path into an ACCOUNT — federated, email-code, or a named org — converges on
// this handler, and a grant here fires once for each of them.
//
// ADDITIONAL ORGS GET NOTHING, and that is the abuse gate. First-run onboarding
// happens once per human (afterwards they have an org, so every later create is
// "additional"), but creating additional orgs is unlimited and personal slugs
// auto-suffix, so granting on every create would be a scriptable $5 mint. The caller
// therefore invokes this ONLY on the first-run branch.

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/hanzoai/commerce/billing/credit"
	"github.com/hanzoai/commerce/billing/creditledger"
)

// errNoCreditLedger reports that no credit ledger is co-resident, so there is
// nothing to grant into. In the unified binary commerce is always embedded and the
// ledger is always injected (apps/commerce.go), so this is a split-deploy/dev shape,
// not a runtime error worth failing onboarding over — the caller logs and continues.
var errNoCreditLedger = errors.New("starter credit: no credit ledger co-resident")

// starterRef is the grant's idempotency key: the ADDRESS it credits, and nothing
// else. Keying on the address is what makes the grant exactly-once per ACCOUNT
// rather than per session, per login, or per attempt — a retry, a double-submit, a
// re-login and two concurrent onboards all derive the same key, and finance dedups
// on it inside the same transaction as the insert. It deliberately contains no
// timestamp, nonce, user id or request id: any of those would make a replay a SECOND
// grant, which is the whole failure this key exists to prevent.
func starterRef(org string) string { return "starter:" + strings.ToLower(strings.TrimSpace(org)) }

// EnsureStarterCredit grants the one-time starter credit to a newly provisioned org
// and returns the org's resulting balance in cents.
//
// THE ADDRESS. It credits the org's POOL account, because that is the account the
// gate reads for a real org — not a guess, a consequence of account.Payer, the ONE
// rule both sides call. The gate resolves its address as
// principal.WalletOf → account.Payer(...).Subject(); with no `billing_account` claim
// minted anywhere in IAM v2 today, Payer takes its documented fallback, and for an
// owner that is not the shared signup org that fallback is Org(owner) — whose
// Subject() IS the bare slug. An empty CreditInput.Subject means exactly that pool
// account (apps/ledger.go). So grant and gate address one wallet by construction, and
// the day IAM starts minting per-member accounts, Subject is the field that carries
// the difference — which is why the seam takes an address rather than an org name.
//
// THE AMOUNT IS SERVER-AUTHORITATIVE. It is credit.StarterCreditCents, the shared
// constant, read from the server's own dependency — never a request field. Nothing a
// client sends reaches this call.
//
// FAILURE IS NOT AN ALLOW. If this returns an error the account simply holds no
// credit: the gate reads the ledger, not a grant flag, so an ungranted account reads
// $0 and is refused. There is no path where a failed grant is mistaken for funding.
//
// EXPIRY IS REQUESTED BUT NOT YET ENFORCED, stated here rather than assumed. The
// StarterCreditDays window is passed on the input and honoured by commerce's own
// datastore path, but the CO-RESIDENT adapter (apps/ledger.go) drops it — cloud's
// finance DepositInput carries no expiry field, so in production the $5 does not
// expire. It is sent so the intent travels with the grant and lands the day the
// ledger can express it; nothing here should be read as proof that it lapses.
func EnsureStarterCredit(ctx context.Context, org string) (int64, error) {
	org = strings.ToLower(strings.TrimSpace(org))
	if org == "" {
		return 0, errors.New("starter credit: org is required")
	}
	led := creditledger.Get()
	if led == nil {
		return 0, errNoCreditLedger
	}
	expires := time.Now().AddDate(0, 0, credit.StarterCreditDays).UTC()
	_, balanceCents, err := led.Credit(ctx, creditledger.CreditInput{
		Org: org,
		// Subject empty == the org POOL, the account the gate reads for a real org.
		Currency:       "usd",
		Reason:         "welcome",
		Tag:            credit.StarterCreditTag,
		IdempotencyKey: starterRef(org),
		AmountCents:    credit.StarterCreditCents,
		ExpiresAt:      &expires,
	})
	if err != nil {
		return 0, err
	}
	return balanceCents, nil
}
