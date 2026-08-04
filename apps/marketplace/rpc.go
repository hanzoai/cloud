package marketplace

import (
	"context"
	"net/http"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/plane"
	"github.com/zap-proto/zip"
)

// rpc.go — the price table, published for the process that enforces it.
//
// x402 is the rail and this is the table; they were joined by a process-global
// (x402.Publish) and the fleet runs one binary per app, so in the x402 process the
// table is nil. Reading a nil table as "nothing is priced" is how every listed tool
// became free the moment the fleet split, so the rail asks the owner instead.
//
// It is the SAME lookup the in-process seam serves — registry.Price, one method,
// one store — projected onto the wire. There is no second price rule here, because
// a second rule is how a gate and a settlement come to disagree.
//
// THE RECIPIENT IS THE ROW'S, on the reply and never in a request. Who publishes a
// listing and which wallet it pays into are properties of the listing; a buyer that
// could state either could buy at its own price or redirect the credit. The op's
// input has one field and it names the resource.
//
// It reads NO tenant and therefore requires none. A price is the shop window: the
// same public listings, the same figure, for everyone. Demanding a caller org here
// would be inventing an authority that does not exist — and it would break a
// documented path, because the rail asks this before it asks anything else, so that
// an unpriced tool dispatched with no attested caller at all (the CLI's LocalInvoke)
// still runs. Nothing unpriced needs a payer.

// exposePrice publishes the price table. Mount calls it.
func exposePrice(store *Store) {
	g := &registry{store: store}
	zip.Post[plane.PriceIn, plane.Priced](cloud.Plane(), "/market/price", g.planePrice,
		zip.WithOperationID(plane.MarketPrice),
		zip.WithSummary("Resolve what one resource costs and who is paid"))
}

// Price resolves what one resource costs and which wallet receives payment for it —
// the listing store's answer, projected onto the internal plane for the process that
// enforces payment.
//
// priced=false means FREE, and it is an ANSWER: an unlisted tool, a listing with no
// price, and any resource that is not a tool id all land there. A store failure is an
// ERROR instead, because a caller must never read "I could not look it up" as "it
// costs nothing".
//
// It takes no tenant and reads none: the listings it answers from are the PUBLIC
// ones, identical for every caller.
//
// The recipient org and wallet come off the listing ROW — its publisher, and the
// payout wallet that publisher named — so they are on the reply and could not be on
// the request. A buyer that could state either could buy at its own price or redirect
// the credit.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func (g *registry) planePrice(ctx context.Context, in *plane.PriceIn) (*plane.Priced, error) {
	if in.Resource == "" {
		return nil, zip.ErrBadRequest("price: no resource")
	}
	terms, priced, err := g.Price(ctx, in.Resource)
	if err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "price %s: %v", in.Resource, err)
	}
	if !priced {
		return &plane.Priced{}, nil
	}
	return &plane.Priced{
		Priced:            true,
		Amount:            plane.Amount(terms.Amount.Unwrap()),
		RecipientOrg:      terms.RecipientOrg,
		RecipientWalletID: terms.RecipientWalletID,
		Asset:             terms.Asset,
		Network:           terms.Network,
	}, nil
}
