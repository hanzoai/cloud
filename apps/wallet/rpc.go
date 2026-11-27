package wallet

import (
	"context"
	"net/http"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/iam"
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
	cloud.Plane().Post("/wallets/payee", planePayee,
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

// exposeWallets publishes the org's wallet list. Use calls it.
//
// /v1/principal reads an org's addresses to say what the org holds and to screen
// them against the digital currency addresses the sanctions lists designate. The
// list is the caller's own — client.WalletsIn carries nothing — and a wallet is
// answered as its address and custody kind: the key ref never leaves this store.
func exposeWallets(s *cloud.Service[state]) {
	cloud.Plane().Post("/wallets/list", lister{s}.list,
		zip.WithOperationID(client.WalletsList),
		zip.WithSummary("The caller's wallets: addresses and custody, never key material"))
}

// lister binds the service to the plane handler, so it is a named method
// zipdoc can lift prose from.
type lister struct{ s *cloud.Service[state] }

// Answers every wallet of the caller's org, newest first: id, name, chain, the
// on-chain address, the custody kind, and the agent a wallet is scoped to when it
// is one agent's. A wallet still being provisioned, with no address yet, is
// answered with an empty address rather than left out.
func (w lister) list(ctx context.Context, _ *client.WalletsIn) (*client.Wallets, error) {
	org, ok := cloud.Tenant(ctx)
	if !ok {
		return nil, zip.ErrForbidden("wallets: no org on the call")
	}
	rows, err := w.s.State.store.listWallets(ctx, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "wallets: %v", err)
	}
	out := &client.Wallets{Wallets: make([]iam.WalletBrief, 0, len(rows))}
	for _, r := range rows {
		out.Wallets = append(out.Wallets, iam.WalletBrief{ID: r.ID, Name: r.Name, Chain: r.Chain,
			Address: r.Address, Custody: string(r.Custody), Agent: r.Agent})
	}
	return out, nil
}
