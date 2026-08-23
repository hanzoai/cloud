package billing

// peer.go is how this app reaches the store behind its own address.
//
// /v1/billing is the billing capability's root and the rows behind most of it
// are commerce's, in a per-tenant datastore with ONE writer. So every family in
// this package that is not the ledger's own read is a call BY NAME to that
// owner, and this file holds the one rule they share.
//
// The rule is about ABSENCE, and it is the only thing worth centralising. A peer
// that is not running is a fact only the router may state (cloud.ErrNoPeer, read
// off the manifest it owns); every other failure is an outage and must reach the
// customer as one. Inferring absence from a failed call is how a dead ledger once
// became a phantom split deploy — the read fell through to an HTTP proxy that is
// unset in exactly the deployment where the plane is the real path, and the
// prepaid gate answered 503 for every paid request in the fleet with nothing
// logging the reason.
//
// Here there is nowhere to fall through TO: commerce holds the invoices, the
// cards and the spend caps, and no second copy exists to read. So absence is
// 503 with the deployment named, and a refusal the peer CHOSE — 402 unfunded,
// 403 not yours, 404 no such thing — crosses whole and is answered verbatim.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/plane"
	commercepeer "github.com/hanzoai/cloud/plane/commerce"
	"github.com/zap-proto/zip"
)

// ask runs one peer call for org and normalises the two failures a caller must
// never confuse. what names the act, so a 503 says which endpoint could not be
// answered rather than only that something could not.
//
// The org rides cloud.For: an inbound request's own principal always wins over
// what a caller stated, so this supplies the tenant for the case where there is
// no request and can never launder one where there is.
func ask[T any](ctx context.Context, org, what string, call func(context.Context) (*T, error)) (*T, error) {
	out, err := call(cloud.For(ctx, org))
	if err != nil {
		if errors.Is(err, cloud.ErrNoPeer) {
			return nil, zip.Errorf(http.StatusServiceUnavailable,
				"%s: this deployment runs no commerce", what)
		}
		// Anything the peer CHOSE keeps its status: a 402 is unfunded, a 404 is
		// no such row, and flattening either into a 502 would tell a customer
		// their invoice is missing when their card was declined.
		var he *zip.HTTPError
		if errors.As(err, &he) {
			return nil, he
		}
		return nil, zip.Errorf(http.StatusBadGateway, "%s: %v", what, err)
	}
	if out == nil {
		// A void reply is not an empty answer. Nothing was read, so nothing is
		// known, and reporting "none" would be inventing a fact.
		return nil, zip.Errorf(http.StatusBadGateway, "%s: commerce answered nothing", what)
	}
	return out, nil
}

// principalOrg is the tenant a read acts for, and nothing else.
//
// The ORG-scoped reads use it rather than payer: they do not need a wallet, and
// resolving one they will not use would tie who an org-scoped read answers for
// to a rule about which account inside that org a request bills from. Two
// questions, two resolvers.
func principalOrg(ctx context.Context) (string, error) {
	org, ok := principal.OrgFrom(ctx)
	if !ok {
		return "", zip.ErrUnauthorized("sign in to view billing")
	}
	return org, nil
}

// payerOf resolves the wallet a RAW handler acts for. It is payer's twin for the
// two endpoints that serve something other than a JSON value, and it resolves the
// same two facts by the same rules, so a raw endpoint and a typed one can never
// disagree about whose money they are describing.
func payerOf(c *zip.Ctx) (org, subject string, err error) {
	o, ok := principal.Org(c)
	if !ok {
		return "", "", zip.ErrUnauthorized("sign in to view billing")
	}
	return o, subjectFor(c, o), nil
}

// serviceOrg is the tenant for the one read whose caller is a SERVICE rather
// than a person: the metering edge, presenting a token and the gateway-pinned
// org with no user behind it.
//
// It admits that principal where the customer reads do not, and it is the only
// place in this package that does. The reason it must is the consumer's own
// failure mode: the edge reads any non-2xx as fail-open, so refusing a caller
// this read exists for does not fail the request, it lifts every ceiling in the
// org and says nothing.
func serviceOrg(ctx context.Context) (string, error) {
	if c, ok := cloud.Request(ctx); ok {
		if org, ok := readerOrg(c); ok {
			return org, nil
		}
	}
	return "", zip.ErrUnauthorized("sign in to view billing")
}

// atoiOr64 reads a numeric query value, falling back to def for anything that is
// not a number.
func atoiOr64(s string, def int64) int64 {
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return n
	}
	return def
}

// invoicePDF serves the rendered invoice as an attachment.
//
// It is a RAW handler where its five siblings are typed, and the reason is the
// body: a PDF is bytes with a filename, not a JSON value, so there is no shape
// to declare and the two headers are the contract. The bytes themselves cross
// the plane fine — the render is a pure function of the invoice, bounded by one
// page — which is what lets the download keep its address instead of following
// the renderer into another capability's root.
func invoicePDF(c *zip.Ctx) error {
	org, ok := readerOrg(c)
	if !ok {
		return zip.ErrUnauthorized("sign in to view billing")
	}
	doc, err := ask(c.Context(), org, "invoice pdf", func(ctx context.Context) (*plane.Document, error) {
		return commercepeer.BillingInvoicePDF(ctx, &plane.InvoiceRef{ID: c.Param("id")})
	})
	if err != nil {
		return err
	}
	c.SetHeader("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.pdf"`, doc.Filename))
	c.SetHeader("Content-Type", "application/pdf")
	return c.Bytes(http.StatusOK, doc.Body)
}
