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
	"github.com/hanzoai/commerce/thirdparty/kms"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/plane"
)

// invoiceOps binds the invoice ops to the subsystem. Method values, not
// closures, so zipdoc can lift each op's prose into the registry.
type invoiceOps struct{}

// exposeInvoices publishes the invoice lifecycle on the plane. Mount calls it.
func exposeInvoices() {
	o := invoiceOps{}
	zip.Post[plane.RaiseIn, plane.Invoice](cloud.Plane(), "/billing/invoice/raise", o.raise,
		zip.WithOperationID(plane.BillingInvoiceRaise),
		zip.WithSummary("Raise a draft invoice against a customer"))
	zip.Post[plane.InvoiceRef, plane.Invoice](cloud.Plane(), "/billing/invoice/read", o.read,
		zip.WithOperationID(plane.BillingInvoiceRead),
		zip.WithSummary("Read one invoice"))
	zip.Post[plane.InvoiceRef, plane.Invoice](cloud.Plane(), "/billing/invoice/issue", o.issue,
		zip.WithOperationID(plane.BillingInvoiceIssue),
		zip.WithSummary("Issue a draft invoice, making it collectible"))
	zip.Post[plane.InvoiceRef, plane.Collected](cloud.Plane(), "/billing/invoice/collect", o.collect,
		zip.WithOperationID(plane.BillingInvoiceCollect),
		zip.WithSummary("Collect an issued invoice from credits, balance, then card"))
	zip.Post[plane.InvoiceRef, plane.Invoice](cloud.Plane(), "/billing/invoice/void", o.void,
		zip.WithOperationID(plane.BillingInvoiceVoid),
		zip.WithSummary("Void a draft or issued invoice"))
	zip.Post[plane.InvoiceRef, plane.Document](cloud.Plane(), "/billing/invoice/pdf", o.pdf,
		zip.WithOperationID(plane.BillingInvoicePDF),
		zip.WithSummary("Render one invoice as a PDF"))
	zip.Post[plane.InvoicesIn, plane.Invoices](cloud.Plane(), "/billing/invoices", o.list,
		zip.WithOperationID(plane.BillingInvoices),
		zip.WithSummary("Invoices for this subject"))
}

// Lists one subject's invoices, with the count beside them.
//
// It sends the STORE'S own projection of an invoice — the billing period, the
// tax and discount lines, the attempt count — which is a wider shape than the
// lifecycle's [plane.Invoice] because it answers a different question: "what
// have I been billed" rather than "what is on this invoice". The two are not
// folded together; shapes that overlap are still two shapes.
//
// Every timestamp becomes RFC3339 text on the way out, because a time value
// crosses this plane as an empty struct and would arrive as the zero instant
// with nothing reporting the loss.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func (invoiceOps) list(ctx context.Context, in *plane.InvoicesIn) (*plane.Invoices, error) {
	org, err := payingOrg(ctx, "invoices")
	if err != nil {
		return nil, err
	}
	rows, ierr := commercebilling.ListInvoices(ctx, org, in.Subject, in.Status, in.SubscriptionID)
	if ierr != nil {
		return nil, zip.Errorf(502, "invoices: %v", ierr)
	}
	out := make([]plane.BillingInvoice, 0, len(rows))
	for _, v := range rows {
		row := plane.BillingInvoice{
			ID: v.ID, UserID: v.UserID, CustomerEmail: v.CustomerEmail,
			SubscriptionID: v.SubscriptionID,
			PeriodStart:    stamp(v.PeriodStart), PeriodEnd: stamp(v.PeriodEnd),
			Subtotal: v.Subtotal, Tax: v.Tax, Discount: v.Discount,
			CreditApplied: v.CreditApplied, AmountDue: v.AmountDue, AmountPaid: v.AmountPaid,
			Currency: string(v.Currency), Status: string(v.Status),
			PaymentMethod: v.PaymentMethod, PaymentRef: v.PaymentRef,
			Number: v.Number, NumberStr: v.NumberStr, AttemptCount: v.AttemptCount,
			CreatedAt: stamp(v.CreatedAt), UpdatedAt: stamp(v.UpdatedAt),
		}
		if v.DueDate != nil {
			row.DueDate = stamp(*v.DueDate)
		}
		if v.PaidAt != nil {
			row.PaidAt = stamp(*v.PaidAt)
		}
		if v.VoidedAt != nil {
			row.VoidedAt = stamp(*v.VoidedAt)
		}
		// The lines are left NIL when there are none, deliberately: the shape this
		// reproduces sends `null` for an invoice with no lines, and an empty array
		// is a different answer to "were there any".
		for _, l := range v.LineItems {
			row.LineItems = append(row.LineItems, plane.InvoiceLineItem{
				ID: l.Id, Type: string(l.Type), Description: l.Description,
				MeterID: l.MeterId, Quantity: l.Quantity, UnitPrice: l.UnitPrice,
				PlanID: l.PlanId, PlanName: l.PlanName,
				Amount: l.Amount, Currency: string(l.Currency),
				PeriodStart: stamp(l.PeriodStart), PeriodEnd: stamp(l.PeriodEnd),
			})
		}
		out = append(out, row)
	}
	return &plane.Invoices{Rows: out, Count: len(out)}, nil
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
func (invoiceOps) pdf(ctx context.Context, in *plane.InvoiceRef) (*plane.Document, error) {
	org, err := payingOrg(ctx, "invoice pdf")
	if err != nil {
		return nil, err
	}
	doc, derr := commercebilling.InvoicePDF(ctx, org, in.ID)
	if derr != nil {
		return nil, zip.Errorf(http.StatusNotFound, "invoice pdf: %v", derr)
	}
	return &plane.Document{Filename: doc.Filename, Body: doc.Body}, nil
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
func (invoiceOps) raise(ctx context.Context, in *plane.RaiseIn) (*plane.Invoice, error) {
	org, err := payingOrg(ctx, "raise invoice")
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
func (invoiceOps) read(ctx context.Context, in *plane.InvoiceRef) (*plane.Invoice, error) {
	org, err := payingOrg(ctx, "read invoice")
	if err != nil {
		return nil, err
	}
	v, f := commercebilling.ReadInvoice(ctx, org, in.ID)
	return invoiceAnswer(v, f)
}

// Issues a draft invoice: moves it to OPEN, assigns its number, and makes it
// collectible.
//
// Only a draft can be issued. An invoice already open, paid or void is refused
// with the state machine's own reason rather than being silently re-issued, which
// would mint a second number for one debt.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func (invoiceOps) issue(ctx context.Context, in *plane.InvoiceRef) (*plane.Invoice, error) {
	org, err := payingOrg(ctx, "issue invoice")
	if err != nil {
		return nil, err
	}
	v, f := commercebilling.IssueInvoice(ctx, org, in.ID, eventsFrom(ctx))
	return invoiceAnswer(v, f)
}

// Voids a draft or issued invoice — the cancel.
//
// A paid invoice cannot be voided: money has moved, and the correction for that
// is a refund, not an erasure. The state machine refuses it and that refusal is
// the answer.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func (invoiceOps) void(ctx context.Context, in *plane.InvoiceRef) (*plane.Invoice, error) {
	org, err := payingOrg(ctx, "void invoice")
	if err != nil {
		return nil, err
	}
	v, f := commercebilling.VoidInvoiceIn(ctx, org, in.ID, eventsFrom(ctx))
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
func (invoiceOps) collect(ctx context.Context, in *plane.InvoiceRef) (*plane.Collected, error) {
	org, err := payingOrg(ctx, "collect invoice")
	if err != nil {
		return nil, err
	}
	res, f := commercebilling.CollectInvoice(ctx, org, in.ID, kmsFrom(ctx), eventsFrom(ctx))
	if f != nil {
		return nil, zip.Errorf(f.Status, "%s", f.Message)
	}
	return &plane.Collected{
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
func invoiceAnswer(v *commercebilling.InvoiceView, f *commercebilling.PaymentFault) (*plane.Invoice, error) {
	if f != nil {
		return nil, zip.Errorf(f.Status, "%s", f.Message)
	}
	return viewOf(v), nil
}

// viewOf projects commerce's typed invoice onto this surface's wire type.
func viewOf(v *commercebilling.InvoiceView) *plane.Invoice {
	if v == nil {
		return nil
	}
	out := &plane.Invoice{
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
	for _, l := range v.Lines {
		out.Lines = append(out.Lines, plane.InvoiceLine{
			Description: l.Description,
			Amount:      l.Amount,
			Quantity:    l.Quantity,
			UnitPrice:   l.UnitPrice,
		})
	}
	return out
}

// eventsFrom and kmsFrom lift the two side channels these ops use: the request's
// copy when a request is being served, and the process-wide one the embed built
// when a PEER asked by name. The peer half is not a nicety — a plane call carries
// no fiber locals, so without it collect would reach Square with whatever
// credentials the environment happened to hold rather than the ones the tenant
// configured, and every settled charge would go unreported to analytics.
//
// Both remain optional: a missing analytics collector must never fail a money
// move, and a missing KMS client is the dev/test posture where credentials come
// from the environment.
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

func kmsFrom(ctx context.Context) *kms.CachedClient {
	if c, ok := cloud.Request(ctx); ok {
		if v := c.Locals("kms"); v != nil {
			if k, ok := v.(*kms.CachedClient); ok {
				return k
			}
		}
	}
	if e := currentEmbedded(); e != nil && e.App() != nil {
		return e.App().KMS
	}
	return nil
}
