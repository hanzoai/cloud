package billing

import (
	"context"
	"errors"
	"fmt"

	"github.com/hanzoai/cloud/client"
	commercepeer "github.com/hanzoai/cloud/client/commerce"
	"github.com/hanzoai/cloud/finance"
	"github.com/hanzoai/cloud/money"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/meter"
	"github.com/hanzoai/cloud/sdk/network"
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
func subjectFor(c *zip.Ctx, org string) string { return meter.PayerIn(c, org).Subject() }

// funds is a wallet's spendable balance in whole USD cents, floored, and where it came
// from: cash is money the customer paid, credit is money the platform minted, line is
// what is left of the org's credit line. cash + line + credit == available, and all
// three pay for work. due is what the account owes and limit its credit line; both
// are nil when the ledger that answered does not report them — unknown, never zero.
type funds struct {
	available, cash, credit int64
	line, due, limit        *int64
}

// prepaid is what pays for a third party's model: cash and the line left, in whole
// cents. Granted credit is not in it.
func (f funds) prepaid() int64 {
	n := f.cash
	if f.line != nil {
		n += *f.line
	}
	return n
}

// fundsCents returns the caller's spendable prepaid balance from the co-resident
// finance ledger. ok is false ONLY when this deployment runs no commerce at all (split
// deploy) and the caller must fall back to the commerce S2S read; a non-nil err is a REAL
// read failure and must be surfaced, never rendered as a zero balance — a balance that
// cannot be read is unknown, and unknown is not "broke".
//
// "No commerce here" is a fact only the ROUTER can state (cloud.ErrNoPeer), never one
// inferred from a call that failed. A dead peer read as an absent one is a silent
// downgrade to a fallback that is unconfigured in exactly the deployments where the plane
// is the real path — an outage wearing a working deployment's answer.
//
// The split is finance.Funds, computed once in the process that owns the ledger; this
// only reads it.
func fundsCents(ctx context.Context, org, subject string) (out funds, ok bool, err error) {
	if fin := finance.Current(); fin != nil {
		funder, is := fin.(finance.Funder)
		if !is {
			return funds{}, true, errors.New("balance: this ledger cannot say where a balance came from")
		}
		f, err := funder.Funds(ctx, org, subject, network.SandboxOnly())
		if err != nil {
			return funds{}, true, err
		}
		got, err := fundsOf(&client.Balance{
			Amount: wire(f.Total()), Cash: wire(f.Cash), Credit: wire(f.Credit),
			Line: wire(f.Line), Owed: wire(f.Owed), Limit: wire(f.Limit),
		})
		return got, true, err
	}
	// No ledger in THIS process, which is the normal case and not a gap: the
	// prepaid ledger is per-org SQLite with one writer, so only the process that
	// mounts commerce opens it. Ask that process over the internal plane rather
	// than reporting "not configured" for a ledger that exists one socket away.
	// For(org) names the tenant whose books to read; the callee takes the org from
	// that capability and the payload cannot name one, so the subject is all that
	// travels.
	bal, err := commercepeer.FinanceBalance(cloud.For(ctx, org), &client.BalanceIn{Subject: subject, Currency: "usd"})
	if err != nil {
		if errors.Is(err, cloud.ErrNoPeer) {
			// The router ANSWERED, from the manifest it owns: this deployment runs no
			// commerce. That is the SPLIT DEPLOY, and the caller reads commerce over its
			// configured URL.
			return funds{}, false, nil
		}
		// The peer is HERE and the read failed: an outage, never "fall back". ok=true
		// routes it to balance()'s warn + 502, which names it.
		return funds{}, true, fmt.Errorf("balance: commerce ledger read: %w", err)
	}
	if bal == nil {
		// A void reply is not a zero balance. Nothing was read, so nothing is known.
		return funds{}, true, fmt.Errorf("balance: commerce answered nothing")
	}
	got, err := fundsOf(bal)
	return got, true, err
}

// wire carries a ledger amount as the plane does; a zero value carries no currency,
// and is the zero it means.
func wire(a money.Amount) client.Money {
	if a.IsZero() {
		a = money.Zero()
	}
	return client.Amount(a.Unwrap())
}

// fundsOf reads the ledger's balance into whole cents — the ONE reading, for the
// co-resident ledger and the plane alike.
//
// Round DOWN, explicitly, because these figures are SPENT AGAINST: `available` is
// what hanzoai/ai's balance gate reads. Minor() RESCALES and hanzoai/decimal's
// Rescale rounds half-away-from-zero — 4.995 comes back as 500 cents — so a charge
// would be admitted against a balance that cannot cover it. FloorMinor is that choice
// made once, in plane, for every caller that compares rather than debits. A reply
// that does not parse is a real failure, never zero. What is owed rounds UP: a debt
// is never shown smaller than it is.
func fundsOf(bal *client.Balance) (funds, error) {
	var out funds
	var err error
	if out.available, err = bal.Amount.FloorMinor(); err != nil {
		return funds{}, err
	}
	if out.cash, err = bal.Cash.FloorMinor(); err != nil {
		return funds{}, err
	}
	if out.credit, err = bal.Credit.FloorMinor(); err != nil {
		return funds{}, err
	}
	// A ledger that does not report the line leaves these empty: absent, not zero.
	if bal.Line.Decimal == "" || bal.Owed.Decimal == "" || bal.Limit.Decimal == "" {
		return out, nil
	}
	line, err := bal.Line.FloorMinor()
	if err != nil {
		return funds{}, err
	}
	owed, err := bal.Owed.Parse()
	if err != nil {
		return funds{}, err
	}
	due := money.FromDecimal(owed.Decimal()).CentsUp()
	limit, err := bal.Limit.FloorMinor()
	if err != nil {
		return funds{}, err
	}
	out.line, out.due, out.limit = &line, &due, &limit
	return out, nil
}
