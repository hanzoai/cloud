package billing

import (
	"context"
	"errors"
	"fmt"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/finance"
	"github.com/hanzoai/cloud/plane"
	"github.com/zap-proto/zip"
)

// The ONE prepaid-balance read for the customer surface (/v1/billing/balance).
//
// WHY IT IS NOT A COMMERCE PROXY. Co-resident, commerce registers its routes on the
// HOST's zip app (apps/commerce/mount.go Mount → commerce.Embed with EmbedConfig.App),
// and the commerce transport publishes that SAME shared app as the S2S "commerce" transport,
// which re-dispatches BY PATH. commerce's own billing routes are NOT registered in this
// binary — api.Route(), which registers GET /v1/billing/balance, is called only from
// commerce's mount.go, which is behind `//go:build cloud` and never compiled (cloud ships
// -tags "libsqlite3 sqlite_fts5"). So a GET of "/v1/billing/balance" through the S2S client
// matches the ONLY registration of that path — the handler below — and re-enters it with
// no principal, which answers "sign in to view billing". The proxy was calling itself.
//
// WHERE THE MONEY IS. Co-resident, the prepaid wallet lives in cloud's OWN finance ledger
// (clients/finance, per-org double-entry SQLite): installFinance (build.go) points the ai
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
func subjectFor(c *zip.Ctx, org string) string { return principal.PayerIn(c, org).Subject() }

// availableCents returns the caller's spendable prepaid balance from the co-resident
// finance ledger. ok is false ONLY when this deployment runs no commerce at all (split
// deploy) and the caller must fall back to the commerce S2S read; a non-nil err is a REAL
// read failure and must be surfaced, never rendered as a zero balance — a balance that
// cannot be read is unknown, and unknown is not "broke".
//
// "No commerce here" is a fact only the ROUTER can state (cloud.ErrNoPeer), never one
// inferred from a call that failed. A dead peer read as an absent one is a silent
// downgrade to a fallback that is unconfigured in exactly the deployments where the plane
// is the real path — an outage wearing a working deployment's answer.
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
	out, err := cloud.Ask[plane.BalanceIn, plane.Balance](cloud.For(ctx, org), "commerce",
		plane.FinanceBalance, &plane.BalanceIn{Subject: subject, Currency: "usd"})
	if err != nil {
		if errors.Is(err, cloud.ErrNoPeer) {
			// The router ANSWERED, from the manifest it owns: this deployment runs
			// no commerce. That is the SPLIT DEPLOY, which already has an answer —
			// the caller reads commerce over its configured URL. Handing it back
			// rather than making the socket the only path is what keeps a
			// deployment working exactly as designed off a 502.
			return 0, false, nil
		}
		// The peer is HERE and the read failed. Ask's contract names ErrNoPeer as
		// the only error a caller may read as "fall back"; every other one is an
		// outage. Reading them all as absence is what hid this outage for three
		// days: a stale socket meant commerce was never woken, the Ask failed, the
		// error was dropped on this line, and balance() fell into the commerce
		// proxy — which is unconfigured in this deployment, so the customer saw
		// "billing is not configured" and ai's fail-CLOSED gate saw a refusal and
		// answered 503 balance_unavailable for every paid completion in the fleet.
		// Nothing logged the reason, because nothing had it. ok=true routes it to
		// balance()'s warn + 502, which names it.
		return 0, true, fmt.Errorf("balance: commerce ledger read: %w", err)
	}
	if out == nil {
		// A void reply is not a zero balance. Nothing was read, so nothing is known.
		return 0, true, fmt.Errorf("balance: commerce answered nothing")
	}
	// Round DOWN, explicitly, because this figure is SPENT AGAINST.
	//
	// plane.Money.Minor() refuses a value finer than a cent rather than round
	// behind the caller — right for a debit, and its own doc says a caller that
	// wants a rounded figure "should round explicitly, where the choice is
	// visible". This view never rounded, so a real balance made the read FAIL:
	// live on 2026-08-03 the org held $149,913.078983985999994361 and every read
	// answered 502 "billing upstream unreachable" — a misleading label, since
	// nothing upstream was involved — which the console rendered as "Unavailable"
	// while the money sat there. It worsens as usage accumulates, because a longer
	// history makes a sub-cent tail likelier.
	//
	// The direction is not cosmetic, and this comment used to get it backwards by
	// calling the number a display. It is not. It is `available` on
	// /v1/billing/balance — the field hanzoai/ai's balance gate reads over the S2S
	// HTTP path. Money is spent against it.
	//
	// Minor() RESCALES, and hanzoai/decimal's Rescale rounds HALF-AWAY-FROM-ZERO
	// (decimal.go:145) — it does not truncate. Measured: 4.995 comes back as 500
	// cents, so a 500-cent charge was admitted against a balance that could not
	// cover it, and the exact debit that follows leaves a negative balance nobody
	// authorized. FloorMinor is that choice made once, in plane, for every caller
	// that compares rather than debits.
	cents, perr := out.Amount.FloorMinor()
	if perr != nil {
		// The peer ANSWERED and the reply did not parse. That is a real failure, not
		// an absent ledger, and it must surface: a corrupt reply rendered as zero is
		// a funded account shown as broke.
		return 0, true, perr
	}
	return cents, true, nil
}
