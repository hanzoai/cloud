// Copyright © 2026 Hanzo AI. MIT License.

package commerce

// What a customer may READ of an invoice, as plane ops — the list, one row, the PDF.
//
// READS ONLY, because the money here is prepaid. The lifecycle that raised a demand
// and then collected it — draft, issue, collect, void — priced work AFTER it was done,
// and this platform prices it before: a customer funds a wallet and spends it down, so
// a bill sent afterwards has nothing left to bill for. Nothing in this repo produced
// one either. The cycle that renders invoices from metered usage lives in the commerce
// module and is not mounted, routed or called here, so every one of those acts was a
// door a hand had to open, onto a table nothing filled.
//
// COLLECT IS WHY THEY WENT RATHER THAN WERE LEFT. It ran the credits → balance →
// card-on-file waterfall, so an issued invoice ended in a charge against a saved card
// in arrears — the one thing a prepaid balance exists to make unnecessary. Published
// on this plane it was reachable by every process the router spawns, not only by the
// REST door in front of it, so removing the door alone would have left the charge.
// takePayment (payments.go) remains the ONE way a card is charged, and it charges up
// front, for something the payer asked for.
//
// The reads stay because a customer's own history is theirs to read and reading it
// moves nothing. They answer an empty list to an org that was never billed.
//
// THEY ANSWER BY NAME RATHER THAN AT AN ADDRESS. /v1/billing is the billing
// capability's root (HIP-0018), and these rows are in the store this process
// owns, so the endpoint is billing's and the answer is this app's. apps/billing
// declares the REST addresses and relays each one here; the shapes are plane's,
// declared once and served without re-rendering, so the wire a customer reads is
// the wire this file produced.

import (
	"context"
	"net/http"

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
	zip.Post[plane.InvoiceRef, plane.Invoice](cloud.Plane(), "/billing/invoice/read", o.read,
		zip.WithOperationID(plane.BillingInvoiceRead),
		zip.WithSummary("Read one invoice"))
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

// invoiceAnswer is the ONE fault-to-error and view-to-out mapping the reads share,
// so they cannot disagree about how a 404 or a state refusal reads.
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
