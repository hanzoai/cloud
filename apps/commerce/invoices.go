// Copyright © 2026 Hanzo AI. MIT License.

package commerce

// The INVOICE LIFECYCLE as plane ops — raise, issue, collect, void, read.
//
// The lifecycle was already implemented and already correct; what it was not was
// REACHABLE. commerce implements the whole of it, and cloud mounted exactly two
// routes off it — the list and the PDF, both GETs. So an org could read invoices
// it had no way to create, and `tools/list` published no invoice tool of any
// kind, which is why "invoice a customer" was not a step an agent could take.
//
// These five ops close that. Each delegates to the SAME core commerce's own HTTP
// handlers delegate to (invoice_core.go), so the lifecycle gains endpoints rather
// than a second lifecycle — which matters most for collect, where a second
// implementation would mean a second idempotency guard and, eventually, an
// invoice charged twice.
//
// THEY ANSWER BY NAME RATHER THAN AT AN ADDRESS. /v1/billing is the billing
// capability's root (HIP-0018), and these rows are in the store this process
// owns, so the endpoint is billing's and the answer is this app's. apps/billing
// declares the five REST addresses and relays each one here; the shapes are
// plane's, declared once and served without re-rendering, so the wire a customer
// reads is the wire this file produced.
//
// RAISE AND ISSUE ARE SEPARATE, deliberately. A raised invoice is a DRAFT and is
// not collectible; issuing is what makes it a demand for payment and gives it its
// number. An agent that could only do both at once could never let a human read a
// draft before it went out.
//
// COLLECT IS NOT A SECOND WAY TO TAKE A PAYMENT. takePayment (payments.go) charges
// a card token directly; collectInvoice runs the credits → balance → card-on-file
// waterfall against an issued invoice. They are different questions — "charge this
// card now" and "settle this debt however it can be settled" — and they meet at
// the same per-org Square processor underneath.

import (
	"context"
	"net/http"
	"strings"

	commercebilling "github.com/hanzoai/commerce/api/billing"
	"github.com/hanzoai/commerce/events"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/client"
	cloudds "github.com/hanzoai/cloud/datastore"
	"github.com/hanzoai/cloud/sdk"
)

// invoiceOps binds the invoice ops to the subsystem. Method values, not
// closures, so zipdoc can lift each op's prose into the registry.
type invoiceOps struct{}

// exposeInvoices publishes the invoice lifecycle on the plane. Mount calls it.
func exposeInvoices() {
	o := invoiceOps{}
	cloud.Plane().Post("/billing/invoice/raise", o.raise,
		zip.WithOperationID(client.BillingInvoiceRaise),
		zip.WithSummary("Raise a draft invoice against a customer"))
	cloud.Plane().Post("/billing/invoice/read", o.read,
		zip.WithOperationID(client.BillingInvoiceRead),
		zip.WithSummary("Read one invoice"))
	cloud.Plane().Post("/billing/invoice/issue", o.issue,
		zip.WithOperationID(client.BillingInvoiceIssue),
		zip.WithSummary("Issue a draft invoice, making it collectible"))
	cloud.Plane().Post("/billing/invoice/collect", o.collect,
		zip.WithOperationID(client.BillingInvoiceCollect),
		zip.WithSummary("Collect an issued invoice from credits, balance, then card"))
	cloud.Plane().Post("/billing/invoice/pay", o.pay,
		zip.WithOperationID(client.BillingInvoicePay),
		zip.WithSummary("Pay an invoice with the buyer present"))
	cloud.Plane().Post("/billing/invoice/void", o.void,
		zip.WithOperationID(client.BillingInvoiceVoid),
		zip.WithSummary("Void a draft or issued invoice"))
	cloud.Plane().Post("/billing/invoice/pdf", o.pdf,
		zip.WithOperationID(client.BillingInvoicePDF),
		zip.WithSummary("Render one invoice as a PDF"))
	cloud.Plane().Post("/billing/invoices", o.list,
		zip.WithOperationID(client.BillingInvoices),
		zip.WithSummary("Invoices for this subject"))
}

// Lists one subject's invoices, newest first, with the count and — when a full
// page answers — the cursor beside them. The page is bounded by the store's
// own rule: an absent limit answers its default and a limit past the max
// answers the max, so one response cannot grow with the history.
//
// It sends the STORE'S own projection of an invoice — the billing period, the
// tax and discount lines, the attempt count — which is a wider shape than the
// lifecycle's [client.Invoice] because it answers a different question: "what
// have I been billed" rather than "what is on this invoice". The two are not
// folded together; shapes that overlap are still two shapes.
//
// Every timestamp becomes RFC3339 text on the way out, because a time value
// crosses this plane as an empty struct and would arrive as the zero instant
// with nothing reporting the loss.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func (invoiceOps) list(ctx context.Context, in *client.InvoicesIn) (*client.Invoices, error) {
	org, exists, err := existingOrgOf(ctx, "invoices")
	if err != nil {
		return nil, err
	}
	offset, err := commercebilling.PageOffset(in.Cursor)
	if err != nil {
		return nil, zip.Errorf(400, "invoices: %v", err)
	}
	effective, _ := commercebilling.PageParams(in.Limit, offset)
	if !exists {
		return &client.Invoices{Rows: []client.BillingInvoice{}}, nil
	}
	rows, ierr := commercebilling.ListInvoices(ctx, org, in.Subject, in.Status, in.SubscriptionID, in.Limit, offset)
	if ierr != nil {
		return nil, zip.Errorf(502, "invoices: %v", ierr)
	}
	out := make([]client.BillingInvoice, 0, len(rows))
	for _, v := range rows {
		out = append(out, billingInvoiceWire(v))
	}
	listing := &client.Invoices{Rows: out, Count: len(out)}
	// A full page is the only shape that may hold a next page.
	if len(out) == effective {
		listing.Cursor = commercebilling.PageCursor(offset + effective)
	}
	return listing, nil
}

// Renders one invoice as a PDF — the bytes and the filename they are offered
// under, so the endpoint that serves the download does not need its own renderer.
//
// The render is a pure function of the invoice: no timestamps, no random ids, so
// the same invoice renders the same bytes however many times it is asked for,
// and a retry after a dropped connection costs a re-render and nothing else.
//
// The org scopes the lookup at the storage layer, so a foreign id resolves to
// nothing and is a 404 rather than a filtered hit.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func (invoiceOps) pdf(ctx context.Context, in *client.InvoiceRef) (*client.Document, error) {
	org, err := orgOf(ctx, "invoice pdf")
	if err != nil {
		return nil, err
	}
	doc, derr := commercebilling.InvoicePDF(ctx, org, in.ID, in.Brand)
	if derr != nil {
		return nil, zip.Errorf(http.StatusNotFound, "invoice pdf: %v", derr)
	}
	return &client.Document{Filename: doc.Filename, Body: doc.Body}, nil
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
func (invoiceOps) raise(ctx context.Context, in *client.RaiseIn) (*client.Invoice, error) {
	org, err := orgOf(ctx, "raise invoice")
	if err != nil {
		return nil, err
	}
	core := commercebilling.InvoiceIn{
		UserID:        strings.TrimSpace(in.UserID),
		CustomerEmail: strings.TrimSpace(in.CustomerEmail),
		Currency:      in.Currency,
	}
	for _, l := range in.Lines {
		core.Lines = append(core.Lines, commercebilling.InvoiceLine{
			Description: l.Description,
			Amount:      l.Amount,
			Quantity:    l.Quantity,
			UnitPrice:   l.UnitPrice,
		})
	}
	v, f := commercebilling.RaiseInvoice(ctx, org, core)
	return invoiceAnswer(v, f)
}

// Reads one invoice out of the caller's org.
//
// The org scopes the read by construction — the store is namespaced to it — so an
// id belonging to another tenant is not found rather than found and then filtered.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func (invoiceOps) read(ctx context.Context, in *client.InvoiceRef) (*client.Invoice, error) {
	org, err := orgOf(ctx, "read invoice")
	if err != nil {
		return nil, err
	}
	v, f := commercebilling.ReadInvoice(ctx, org, in.ID)
	return invoiceAnswer(v, f)
}

// Pays an open invoice with the buyer present. The answer is paid, declined (with the
// reason) or authentication_required (with what the buyer finishes). Paying twice is
// one payment: a paid invoice answers paid and charges nothing.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func (invoiceOps) pay(ctx context.Context, in *client.InvoiceRef) (*client.InvoicePayment, error) {
	org, err := orgOf(ctx, "pay invoice")
	if err != nil {
		return nil, err
	}
	p, f := commercebilling.PayInvoiceNow(ctx, org, in.ID, eventsFrom(ctx))
	if f != nil {
		return nil, fail(f)
	}
	out, ierr := into[client.InvoicePayment](p)
	if ierr != nil {
		return nil, ierr
	}
	mirrorOrgInvoices(org)
	mirrorOrgBook(org.Name)
	if out != nil && out.Status == "paid" && !org.TestMode() {
		var amt int64
		cur := "usd"
		ref := ""
		if out.Invoice != nil {
			amt = out.Invoice.AmountPaidCents
			if amt == 0 {
				amt = max(out.Invoice.SubtotalCents, 0)
			}
			if out.Invoice.Currency != "" {
				cur = out.Invoice.Currency
			}
			ref = out.Invoice.PaymentRef
		}
		mirrorCommerceLedgerFact(cloudds.CommerceLedgerFact{
			EntryID:     deterministicLedgerEntryID("invoice_paid", org.Name, in.ID, ref),
			Org:         org.Name,
			Product:     "billing",
			Kind:        "invoice_paid",
			Sign:        1,
			AmountMinor: amt,
			CashMinor:   amt,
			Currency:    cur,
		})
	}
	return out, nil
}

// Issues a draft invoice: moves it to OPEN, assigns its number, and makes it
// collectible.
//
// Only a draft can be issued. An invoice already open, paid or void is refused
// with the state machine's own reason rather than being silently re-issued, which
// would mint a second number for one debt.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func (invoiceOps) issue(ctx context.Context, in *client.InvoiceRef) (*client.Invoice, error) {
	org, err := orgOf(ctx, "issue invoice")
	if err != nil {
		return nil, err
	}
	v, f := commercebilling.IssueInvoice(ctx, org, in.ID, eventsFrom(ctx))
	if f == nil {
		mirrorOrgInvoices(org)
	}
	return invoiceAnswer(v, f)
}

// Voids a draft or issued invoice — the cancel.
//
// A paid invoice cannot be voided: money has moved, and the correction for that
// is a refund, not an erasure. The state machine refuses it and that refusal is
// the answer.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func (invoiceOps) void(ctx context.Context, in *client.InvoiceRef) (*client.Invoice, error) {
	org, err := orgOf(ctx, "void invoice")
	if err != nil {
		return nil, err
	}
	v, f := commercebilling.VoidInvoiceIn(ctx, org, in.ID, eventsFrom(ctx))
	if f == nil {
		mirrorOrgInvoices(org)
	}
	return invoiceAnswer(v, f)
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
func (invoiceOps) collect(ctx context.Context, in *client.InvoiceRef) (*client.Collected, error) {
	org, err := orgOf(ctx, "collect invoice")
	if err != nil {
		return nil, err
	}
	res, f := commercebilling.CollectInvoice(ctx, org, in.ID, eventsFrom(ctx))
	if f != nil {
		return nil, zip.Errorf(f.Status, "%s", f.Message)
	}
	mirrorOrgInvoices(org)
	if res.Paid {
		mirrorOrgBook(org.Name)
		if !org.TestMode() {
			totalPaid := res.CreditUsedCents + res.BalanceUsedCents + res.CardChargedCents
			cashPaid := res.BalanceUsedCents + res.CardChargedCents
			mirrorCommerceLedgerFact(cloudds.CommerceLedgerFact{
				EntryID:     deterministicLedgerEntryID("invoice_paid", org.Name, in.ID, res.ProcessorRef),
				Org:         org.Name,
				Product:     "billing",
				Kind:        "invoice_paid",
				Sign:        1,
				AmountMinor: totalPaid,
				CashMinor:   cashPaid,
				CreditMinor: res.CreditUsedCents,
				Currency:    string(res.Invoice.Currency),
			})
		}
	}
	return &client.Collected{
		Invoice:          viewOf(res.Invoice),
		Paid:             res.Paid,
		CreditUsedCents:  res.CreditUsedCents,
		BalanceUsedCents: res.BalanceUsedCents,
		CardChargedCents: res.CardChargedCents,
		ProcessorRef:     res.ProcessorRef,
		Reason:           res.Reason,
	}, nil
}

// invoiceAnswer is the ONE fault-to-error and view-to-out mapping the five ops
// share, so they cannot disagree about how a 404 or a state refusal reads.
func invoiceAnswer(v *commercebilling.InvoiceView, f *commercebilling.PaymentFault) (*client.Invoice, error) {
	if f != nil {
		return nil, zip.Errorf(f.Status, "%s", f.Message)
	}
	return viewOf(v), nil
}

// viewOf projects commerce's typed invoice onto this surface's wire type.
func viewOf(v *commercebilling.InvoiceView) *client.Invoice {
	if v == nil {
		return nil
	}
	out := &client.Invoice{
		ID:              v.ID,
		Number:          v.Number,
		UserID:          v.UserID,
		CustomerEmail:   v.CustomerEmail,
		Status:          v.Status,
		Currency:        v.Currency,
		SubtotalCents:   v.SubtotalCents,
		AmountDueCents:  v.AmountDueCents,
		AmountPaidCents: v.AmountPaidCents,
		PaymentRef:      v.PaymentRef,
		CreatedAt:       v.CreatedAt,
	}
	if v.Quote != nil {
		out.Quote, _ = into[client.PriceQuote](v.Quote)
	}
	for _, l := range v.Lines {
		out.Lines = append(out.Lines, client.InvoiceLine{
			Description: l.Description,
			Amount:      l.Amount,
			Quantity:    l.Quantity,
			UnitPrice:   l.UnitPrice,
		})
	}
	return out
}

// eventsFrom lifts the analytics collector these ops report to: the request's copy
// when a request is being served, and the process-wide one the embed built when a
// PEER asked by name. The peer half is not a nicety — a plane call carries no fiber
// locals, so without it every settled charge would go unreported to analytics.
//
// It remains optional: a missing analytics collector must never fail a money move.
func eventsFrom(ctx context.Context) *events.Client {
	if c, ok := cloud.Request(ctx); ok {
		if v := c.Locals("events"); v != nil {
			if ev, ok := v.(*events.Client); ok {
				return ev
			}
		}
	}
	if e := currentEmbedded(); e != nil && e.App() != nil {
		return e.App().Events
	}
	return nil
}
