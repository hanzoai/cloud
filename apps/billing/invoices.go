package billing

// invoices.go serves the invoice family of /v1/billing — the list, the five
// lifecycle acts, and the PDF.
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
	zip.Post(zapp, "/v1/billing/invoices", o.raiseInvoice,
		zip.WithOperationID("raiseInvoice"),
		zip.WithSummary("Raise a draft invoice against a customer"),
		zip.WithStatus(http.StatusCreated))
	zip.Get(zapp, "/v1/billing/invoices/:id", o.invoice,
		zip.WithOperationID("getInvoice"),
		zip.WithSummary("Read one invoice"))
	zip.Post(zapp, "/v1/billing/invoices/:id/issue", o.issueInvoice,
		zip.WithOperationID("issueInvoice"),
		zip.WithSummary("Issue a draft invoice, making it collectible"))
	zip.Post(zapp, "/v1/billing/invoices/:id/collect", o.collectInvoice,
		zip.WithOperationID("collectInvoice"),
		zip.WithSummary("Collect an issued invoice from credits, balance, then card"))
	zip.Post(zapp, "/v1/billing/invoices/:id/void", o.voidInvoice,
		zip.WithOperationID("voidInvoice"),
		zip.WithSummary("Void a draft or issued invoice"))
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
			"value, which is why this one route is untyped where its five siblings "+
			"are typed: a PDF is bytes with a filename, and the two headers are the "+
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

// Raises a DRAFT invoice against a customer in the caller's own org.
//
// The invoice is not collectible yet: a draft exists so it can be read and
// corrected, and issueInvoice is the separate act that turns it into a demand for
// payment. The subtotal and amount due are computed from the lines, so there is
// no total to send and none to get wrong.
//
// The billing org is the caller's, taken from the validated principal, so an
// invoice can only ever be raised on the caller's own books.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func (o ops) raiseInvoice(ctx context.Context, in *plane.RaiseIn) (*plane.Invoice, error) {
	org, _, err := payer(ctx)
	if err != nil {
		return nil, err
	}
	return ask(ctx, org, "raise invoice", func(ctx context.Context) (*plane.Invoice, error) {
		return commercepeer.BillingInvoiceRaise(ctx, in)
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

// Issues a draft invoice: moves it to OPEN, assigns its number, and makes it
// collectible.
//
// Only a draft can be issued. An invoice already open, paid or void is refused
// with the state machine's own reason rather than being silently re-issued, which
// would mint a second number for one debt.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func (o ops) issueInvoice(ctx context.Context, in *plane.InvoiceRef) (*plane.Invoice, error) {
	org, _, err := payer(ctx)
	if err != nil {
		return nil, err
	}
	return ask(ctx, org, "issue invoice", func(ctx context.Context) (*plane.Invoice, error) {
		return commercepeer.BillingInvoiceIssue(ctx, in)
	})
}

// Voids a draft or issued invoice — the cancel.
//
// A paid invoice cannot be voided: money has moved, and the correction for that
// is a refund, not an erasure. The state machine refuses it and that refusal is
// the answer.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func (o ops) voidInvoice(ctx context.Context, in *plane.InvoiceRef) (*plane.Invoice, error) {
	org, _, err := payer(ctx)
	if err != nil {
		return nil, err
	}
	return ask(ctx, org, "void invoice", func(ctx context.Context) (*plane.Invoice, error) {
		return commercepeer.BillingInvoiceVoid(ctx, in)
	})
}

// Collects an issued invoice: credit grants first, then prepaid balance, then the
// card on file — the same waterfall the dunning workflow runs.
//
// A DECLINE IS NOT AN ERROR. It answers with paid=false, a reason, and the
// invoice still open, because a declined collection is a normal business outcome
// that must remain retryable — and because sealing it as a failure would wedge
// dunning behind a replayed decline. Only a successful collection is sealed, so a
// retry of a paid invoice replays the receipt instead of charging again.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func (o ops) collectInvoice(ctx context.Context, in *plane.InvoiceRef) (*plane.Collected, error) {
	org, _, err := payer(ctx)
	if err != nil {
		return nil, err
	}
	return ask(ctx, org, "collect invoice", func(ctx context.Context) (*plane.Collected, error) {
		return commercepeer.BillingInvoiceCollect(ctx, in)
	})
}
