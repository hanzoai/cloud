package billing

// invoices.go serves what a customer may READ of an invoice under /v1/billing —
// the list, one row, and the PDF.
//
// READS ONLY, because the money here is prepaid. The acts that raised a demand and
// then collected it — draft, issue, collect, void — were the arrears half of a model
// this platform does not run: a customer funds a wallet and spends it down, so a bill
// sent after the fact has nothing to bill for. Nothing in this repo ever produced one
// (the cycle that renders invoices from metered usage lives in the commerce module and
// is not mounted here), and collect ended in a charge against the card on file, which
// is the one thing a prepaid balance exists to make unnecessary.
//
// The reads stay because a customer's own history is theirs to read and reading it
// moves nothing. They answer an empty list to an org that was never billed.
//
// Every one of them is a relay. The rows are in commerce's per-tenant datastore,
// which has ONE owner, so this app asks that owner BY NAME and answers with what
// it was handed. The shapes are plane's and are not re-rendered here: one
// declaration, so the wire a customer reads cannot drift from the wire commerce
// produced, and a change to either half is a change to a type both compile
// against.
//
// The org is the caller's, forwarded from the validated principal, and the
// SUBJECT is resolved here — by payer, the one rule that turns a request into a
// wallet — and never taken from the query. That is the pin the old co-resident
// chain applied with middleware, made structural: the plane input has a subject
// field and this file is the only thing that fills it.

import (
	"context"
	"net/http"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/openapi"
	"github.com/hanzoai/cloud/plane"
	commercepeer "github.com/hanzoai/cloud/plane/commerce"
	"github.com/zap-proto/zip"
)

// mountInvoices registers the invoice family. Called from routes.
func mountInvoices(app cloud.Router, o ops) {
	zapp := cloud.ZipApp(app)
	zip.Get(zapp, "/v1/billing/invoices", o.invoices)
	zip.Get(zapp, "/v1/billing/invoices/:id", o.invoice,
		zip.WithOperationID("getInvoice"),
		zip.WithSummary("Read one invoice"))
	app.Get("/v1/billing/invoices/:id/pdf", invoicePDF)
}

// The PDF is the one route in this family the wire keeps untyped, so its prose
// is declared beside the route rather than lifted off a typed handler. Declared
// through the same registry Register uses, so it renders only while the router
// actually serves the route.
func init() {
	openapi.Describe("/v1/billing/invoices/:id/pdf", http.MethodGet,
		"Download one invoice as a PDF",
		"Answers the invoice as an attachment — `application/pdf` under a "+
			"Content-Disposition naming the invoice number — rather than as a JSON "+
			"value, which is why this one route is untyped where its siblings are "+
			"typed: a PDF is bytes with a filename, and the two headers are the "+
			"whole contract.\n\n"+
			"The render is a PURE function of the invoice: one page, no timestamps "+
			"and no random ids, so the same invoice renders the same bytes however "+
			"often it is asked for and a retry after a dropped connection costs a "+
			"re-render and nothing else.\n\n"+
			"The invoice is read from the caller's own org, taken from the VALIDATED "+
			"IAM owner claim and never from a client header, and the lookup is scoped "+
			"at the storage layer — so an id belonging to another customer resolves to "+
			"nothing and answers 404 rather than being found and then refused.")
}

// Lists the caller's invoices, newest first, with the count beside them.
//
// It is scoped to the caller's own billing subject — the wallet this request
// bills from, resolved server-side — so a query cannot widen it to another
// customer of the same org. An org with no invoices is an empty list, not a
// refusal.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func (o ops) invoices(ctx context.Context, _ *noInput) (*plane.Invoices, error) {
	org, subject, err := payer(ctx)
	if err != nil {
		return nil, err
	}
	return ask(ctx, org, "invoices", func(ctx context.Context) (*plane.Invoices, error) {
		return commercepeer.BillingInvoices(ctx, &plane.InvoicesIn{Subject: subject})
	})
}

// Reads one invoice out of the caller's org.
//
// The org scopes the read by construction — the store is namespaced to it — so an
// id belonging to another tenant is not found rather than found and then filtered.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func (o ops) invoice(ctx context.Context, in *plane.InvoiceRef) (*plane.Invoice, error) {
	org, _, err := payer(ctx)
	if err != nil {
		return nil, err
	}
	return ask(ctx, org, "read invoice", func(ctx context.Context) (*plane.Invoice, error) {
		return commercepeer.BillingInvoiceRead(ctx, in)
	})
}
