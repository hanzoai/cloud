package billing

import (
	"context"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/finance"
	"github.com/hanzoai/cloud/apps/principal"
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
	if fin := finance.Current(); fin != nil {
		bal, err := fin.Balance(ctx, org, subject, "usd", false)
		if err != nil {
			return 0, true, err
		}
		return bal.Cents(), true, nil
	}
	// No ledger in THIS process, which is the normal case and not a gap: the
	// prepaid ledger is per-org SQLite with one writer, so only the process that
	// mounts commerce opens it. Ask that process over the internal plane rather
	// than reporting "not configured" for a ledger that exists one socket away —
	// which is what answered 501 on a funded account once apps became their own
	// binaries.
	// For(org) names the tenant whose books to read; the callee takes the org from
	// that capability and the payload cannot name one, so the subject is all that
	// travels.
	out, err := cloud.Dial("commerce").For(org).Call(ctx, "finance.balance",
		cloud.PutBalanceReq(subject, "usd"))
	if err != nil {
		// No ledger in this process AND no peer serving one. That is the SPLIT
		// DEPLOY, which already has an answer: the caller reads commerce over its
		// configured URL. Reporting "not resolved here" hands it back rather than
		// making the socket the only path — which would 502 a deployment that is
		// working exactly as designed.
		return 0, false, nil
	}
	cents, cerr := cloud.I64(out)
	if cerr != nil {
		// The peer ANSWERED and the reply did not parse. That is a real failure, not
		// an absent ledger, and it must surface: a corrupt reply rendered as zero is
		// a funded account shown as broke.
		return 0, true, cerr
	}
	return cents, true, nil
}
