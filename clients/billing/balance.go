package billing

import (
	"context"

	"github.com/hanzoai/cloud/clients/finance"
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/zap-proto/zip"
)

// The ONE prepaid-balance read for the customer surface (/v1/billing/balance and the
// /v1/finance/balance projection).
//
// WHY IT IS NOT A COMMERCE PROXY. Co-resident, commerce registers its routes on the
// HOST's zip app (apps/commerce.go mountCommerce → commerce.Embed with EmbedConfig.App),
// and commerceinproc publishes that SAME shared app as the S2S "commerce" transport,
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

// subjectFor resolves the billing subject for the caller. It is a one-line call to
// principal.Subject — the ONE place a request becomes a wallet key — so this view and
// the gate that admits or refuses the same caller cannot resolve two wallets. cloud and
// ai each keeping their own copy of the rule is what let them drift apart (the console
// view scoping to the org while the gate scoped to "org/user"), so the view showed a
// funded org while the gate refused the member.
//
// org is the VALIDATED principal org (the ledger); principal.Subject names the wallet
// within it, preferring the minted X-User-Name over the UUID X-User-Id and honoring the
// signed `billing_account` claim.
func subjectFor(c *zip.Ctx, org string) string { return principal.Subject(c, org) }

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
