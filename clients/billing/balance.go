package billing

import (
	"context"
	"strings"

	"github.com/hanzoai/account"
	"github.com/hanzoai/cloud/clients/finance"
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/zap-proto/zip"
)

// The ONE prepaid-balance read for the customer surface (/v1/billing/balance and the
// /v1/finance/balance projection).
//
// WHY IT IS NOT A COMMERCE PROXY. Co-resident, commerce registers its routes on the
// HOST's zip app (apps/commerce.go mountCommerce → commerce.Embed with EmbedConfig.App),
// and the commerce transport publishes that SAME shared app as the S2S "commerce" transport,
// which re-dispatches BY PATH. commerce's own billing routes are NOT registered in this
// binary — api.Route(), which registers GET /v1/billing/balance, is called only from
// commerce's mount.go, which is behind `//go:build cloud` and never compiled (cloud ships
// -tags "libsqlite3 sqlite_fts5"). So a GET of "/v1/billing/balance" through the S2S seam
// matches the ONLY registration of that path — the handler below — and re-enters it with
// no principal, which answers "sign in to view billing". The proxy was calling itself.
//
// WHERE THE MONEY IS. Co-resident, the prepaid wallet lives in cloud's OWN finance ledger
// (clients/finance, per-org double-entry SQLite): wireFinance (build.go) points the ai
// prepaid gate's balance read at it, the edge meter debits it, and an admin grant credits
// it (clients/admin/core.grantDeposit prefers finance.Current() for exactly this reason).
// So the customer's balance is read from that ledger DIRECTLY — no HTTP hop, nothing to
// self-dispatch, and the number shown is the number that admits or refuses a request.
// The commerce S2S read stays as the split-deploy fallback, unchanged.

// subjectFor resolves the billing subject for the caller by CALLING the ONE rule,
// ai/object.Payer — the same function the ai prepaid gate (routers/filter_balance.go
// resolveBillingKey) and the usage debit resolve. It is the SHARED resolver for every
// commerce-projected read in this package (the balance read AND the finance reads), so
// there is exactly one copy of the rule. cloud and ai each keeping their own copy is
// what let them drift apart (cloud's console view scoping to the org while the gate
// scoped to "org/user"), so the view showed a funded org while the gate refused the
// member. One function, one rule, one wallet.
//
// org is the VALIDATED principal org. The name half uses the SAME precedence as
// clients/account.resolveCaller: X-User-Name (the IAM username the identity boundary mints
// from the validated `name` claim — expressly "the `name` half of <owner>/<name>"), then
// X-User-Id. Both are authorityHeaders — stripped on ingress and re-injected only from
// verified claims — so neither is a client value.
//
// The X-User-Id fallback goes through the shared account.PayerOf because that header's shape is
// path-dependent: the gateway historically minted X-User-Id == the username, while the
// in-binary direct-Bearer path mints the UUID subject, and callers hold it as an
// "<owner>/<name>" key. PayerOf is the parse ai already uses to fold that key form back
// to the payer, so the two agree by construction instead of by a re-implemented split.
//
// KNOWN RESIDUAL: a validated principal carrying NEITHER X-User-Name NOR an "<owner>/<name>"
// X-User-Id (i.e. a bare username id) folds to the org pool. That requires a JWT with no
// `name` and no `preferred_username`, since the boundary mints X-User-Name from either;
// production tokens carry one. Called out for review rather than papered over.
func subjectFor(c *zip.Ctx, org string) string {
	if name := strings.TrimSpace(c.Header("X-User-Name")); name != "" {
		// Hand Payer the account the credential NAMES (the validated `billing_account`
		// claim, minted into X-Billing-Account-Id). The ai gate reads the same claim,
		// so this view and that gate resolve one wallet. Reading it here is what keeps
		// them from drifting the way the org-vs-"org/user" split once did — except
		// that split was two rules, and this would be one rule fed two different
		// credentials, which reads the same to a user: a funded balance the gate
		// refuses. Absent ⟹ Payer's legacy rule, exactly today's answer.
		return account.Payer(account.Credential{Owner: org, Name: name, Account: principal.BillingAccount(c)}).Subject()
	}
	return account.PayerOf(org, strings.TrimSpace(c.User())).Subject()
}

// availableCents returns the caller's spendable prepaid balance from the co-resident
// finance ledger. ok is false when no finance ledger is published (split deploy) and the
// caller must fall back to the commerce S2S read; a non-nil err is a REAL read failure and
// must be surfaced, never rendered as a zero balance — a balance that cannot be read is
// unknown, and unknown is not "broke".
func availableCents(ctx context.Context, org, subject string) (cents int64, ok bool, err error) {
	fin := finance.Current()
	if fin == nil {
		return 0, false, nil
	}
	bal, err := fin.Balance(ctx, org, subject, "usd", false)
	if err != nil {
		return 0, true, err
	}
	return bal.Cents(), true, nil
}
