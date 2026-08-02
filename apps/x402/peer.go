package x402

import (
	"context"
	"fmt"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/money"
	"github.com/hanzoai/cloud/apps/wallets"
	"github.com/hanzoai/cloud/plane"
)

// peer.go — the three things a settlement needs that this process does not own.
//
// x402 is the RAIL and nothing else. What a resource costs and who is paid is the
// marketplace's table; which address and ledger subject a payout wallet is, is
// wallets'; the books both halves are written to are commerce's. Each of those was
// reached through a process-global — x402.reg, wallets' mounted singleton,
// finance.Current — and the fleet runs one binary per app, so in the shipped x402
// process all three are nil and the rail could enforce nothing.
//
// So each is asked of the process that owns it, exactly as resource_billing_peer.go
// asks commerce whether a create may run. The in-process seam stays the FAST PATH
// when the owner happens to be co-resident (a test, a fused composition); the plane
// is the same policy over a socket when it is not. One policy, two transports.
//
// Every one of these fails CLOSED, and each says so in its own terms:
//
//   - the PRICE is unknown ⇒ error, never "free". A rail that reads its own
//     outage as "nothing is priced" sells everything for nothing.
//   - the PAYEE is unknown ⇒ not resolvable ⇒ 503. Nothing is served, nothing moves.
//   - the LEDGER is unreachable ⇒ error ⇒ no settlement, no receipt, no dispatch.
//
// The PRICE has no exception, and it held one: cloud.ErrNoPeer — read as "no
// marketplace in this fleet" — answered "nothing is priced". The plane cannot report
// that fact. reach() calls an app absent when it cannot reach it and no ROUTER is
// there to say otherwise (plane.go, and a killed peer leaves a socket file that
// refuses every connection), so a marketplace that DIED read as a fleet that never
// had one — and this process, which cannot price anything without that table, then
// answered free for the entire catalogue.
//
// The exception bought nothing either: manifest/apps.go IS the fleet and it lists
// marketplace beside x402, so a rail deployed without its table is a
// misconfiguration, and one that refuses loudly costs less than one that quietly
// sells the shop for nothing.

const (
	peerMarketplace = "marketplace"
	peerWallets     = "wallets"
	peerCommerce    = "commerce"

	// peerCallTimeout bounds one hop. A settlement is on the caller's request path,
	// so a peer that hangs must become a refusal rather than a held connection.
	peerCallTimeout = 10 * time.Second
)

// peerCtx states the tenant ONE hop acts for, on a context with no request behind
// it — the one place zip reads a stated caller.
//
// A request-derived context cannot express it. zip prefers the gateway's assertion
// and returns before it looks at a stated caller, so cloud.For inside a typed
// handler forwards the INBOUND org and silently drops the one named here. The
// payee's org is not the payer's, so that would credit the buyer's own ledger with
// the seller's earnings and nothing would look wrong anywhere.
//
// It also detaches from the caller's cancellation, for the reason the resource
// meter's debit does: money already owed must not be abandoned because a client
// hung up mid-settlement.
func peerCtx(org string) (context.Context, context.CancelFunc) {
	return context.WithTimeout(cloud.For(context.Background(), org), peerCallTimeout)
}

// pricePeer asks the marketplace what a resource costs, when the table is not in
// this process. Its answer is the same triple the in-process Registry returns.
func pricePeer(ctx context.Context, resource string) (Terms, bool, error) {
	cctx, cancel := context.WithTimeout(ctx, peerCallTimeout)
	defer cancel()

	out, err := cloud.Ask[plane.PriceIn, plane.Priced](cctx, peerMarketplace, plane.MarketPrice,
		&plane.PriceIn{Resource: resource})
	switch {
	case err != nil:
		// Unreached is UNKNOWN, and an unknown price is never zero.
		return Terms{}, false, fmt.Errorf("x402: price %s: %w", resource, err)
	case out == nil:
		// A void reply from a price table is not "free". Nothing answered.
		return Terms{}, false, fmt.Errorf("x402: price %s: marketplace answered nothing", resource)
	case !out.Priced:
		return Terms{}, false, nil
	}
	amount, err := out.Amount.Parse()
	if err != nil {
		// A price that cannot be read is not a price of zero.
		return Terms{}, false, fmt.Errorf("x402: price %s: %w", resource, err)
	}
	if amount.Sign() <= 0 {
		return Terms{}, false, nil
	}
	return Terms{
		Amount:            money.FromDecimal(amount.Decimal()),
		RecipientOrg:      out.RecipientOrg,
		RecipientWalletID: out.RecipientWalletID,
		Token:             out.Token,
		Network:           out.Network,
		ChainID:           out.ChainID,
	}, true, nil
}

// payeeOf resolves the payout wallet a listing named — in this process when wallets
// is here, over the plane when it is not.
//
// The org is the LISTING's publisher and is stated as the tenant the call acts for,
// so the lookup is scoped exactly as the in-process one is: wallets resolves an id
// only within the org it is asked for, and a wallet outside that org resolves to
// nothing. A buyer cannot redirect a credit over a socket any more than it could in
// memory, because the org never came from the buyer either way.
func payeeOf(ctx context.Context, org, walletID string) (wallets.PaymentTarget, bool) {
	if target, ok := wallets.ResolvePaymentTarget(ctx, org, walletID); ok {
		return target, true
	}
	if wallets.Mounted() {
		return wallets.PaymentTarget{}, false // wallets is HERE and says no such wallet
	}
	return payeePeer(org, walletID)
}

func payeePeer(org, walletID string) (wallets.PaymentTarget, bool) {
	cctx, cancel := peerCtx(org)
	defer cancel()

	out, err := cloud.Ask[plane.PayeeIn, plane.Payee](cctx, peerWallets, plane.WalletsPayee,
		&plane.PayeeIn{WalletID: walletID})
	if err != nil || out == nil || !out.Found {
		return wallets.PaymentTarget{}, false
	}
	return wallets.PaymentTarget{Address: out.Address, Org: org, Subject: out.Subject}, true
}

// debitPeer debits the payer through the process that owns the ledger. It is the
// metering op resource_billing_peer.go already uses — one meter, one debit shape —
// with the settlement id as the request id, which is what makes a retried
// authorization charge once.
func debitPeer(st *Settlement, amount money.Amount) error {
	cctx, cancel := peerCtx(st.PayerOrg)
	defer cancel()

	_, err := cloud.Ask[plane.RecordIn, plane.Recorded](cctx, peerCommerce, plane.FinanceRecord,
		&plane.RecordIn{
			Subject: st.PayerOrg,
			Amount:  plane.Amount(amount.Unwrap()),
			Usage: plane.Usage{
				Model:     st.Resource,
				Provider:  providerLabel,
				Service:   providerLabel,
				RequestID: st.ID,
			},
		})
	if err != nil {
		return fmt.Errorf("x402: debit payer over the plane: %w", err)
	}
	return nil
}

// creditPeer credits a subject through the process that owns the ledger, keyed on
// ref so the credit is exactly-once whatever retries it.
func creditPeer(org, subject string, amount money.Amount, ref, notes string) error {
	cctx, cancel := peerCtx(org)
	defer cancel()

	_, err := cloud.Ask[plane.CreditIn, plane.Credited](cctx, peerCommerce, plane.FinanceCredit,
		&plane.CreditIn{
			Subject: subject,
			Amount:  plane.Amount(amount.Unwrap()),
			Ref:     ref,
			Notes:   notes,
			Tags:    "x402",
		})
	if err != nil {
		return fmt.Errorf("x402: credit %s over the plane: %w", subject, err)
	}
	return nil
}
