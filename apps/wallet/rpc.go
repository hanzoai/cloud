package wallet

import (
	"context"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/client"
	"github.com/zap-proto/zip"
)

// rpc.go — payee resolution, published for the process that settles.
//
// A settlement needs two things about the seller's wallet: the ADDRESS the 402
// challenge names, and the LEDGER SUBJECT the credit is written to. Both are rows
// in this store, and the store has one process. x402 reached them through the
// live singleton, which is nil in its own binary — so a priced listing resolved
// to no payee and every purchase 503'd.
//
// THE ORG RIDES THE CAPABILITY, which is what makes this safe to publish at all.
// The lookup is scoped to the caller's org exactly as getWallet is, so a caller can
// only resolve a wallet inside the org it is acting for — and x402 acts for the
// PUBLISHER of the listing, an org that came off the listing row and never off the
// buyer's request. A buyer cannot redirect a credit over a socket for the same
// reason it could not in memory: it never chose the org either way.

// exposePayee publishes payee resolution. Mount calls it.
func exposePayee() {
	zip.Post[client.PayeeIn, client.Payee](cloud.Plane(), "/wallets/payee", planePayee,
		zip.WithOperationID(client.WalletsPayee),
		zip.WithSummary("Resolve a payout wallet to its address and ledger subject"))
}

// Payee resolves one payout wallet to the two things a settlement needs: the
// ADDRESS a payment challenge names, and the LEDGER SUBJECT the earnings credit is
// written to.
//
// The wallet is looked up ONLY within the org the caller is acting for, exactly as
// every other read of this store is, so a settlement can only ever be paid into a
// wallet of the org that published the listing. Not-found is an ANSWER rather than
// an error — a listing naming somebody else's wallet must produce it, and the caller
// refuses on it — because an error would read as an outage and invite a retry
// against a fact that will not change.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planePayee(ctx context.Context, in *client.PayeeIn) (*client.Payee, error) {
	org := cloud.Who(ctx).Org
	if org == "" {
		return nil, zip.ErrForbidden("payee: no org on the call")
	}
	if in.WalletID == "" {
		return nil, zip.ErrBadRequest("payee: no wallet id")
	}
	target, ok := ResolvePaymentTarget(ctx, org, in.WalletID)
	if !ok {
		return &client.Payee{}, nil
	}
	return &client.Payee{Found: true, Address: target.Address, Subject: target.Subject}, nil
}
