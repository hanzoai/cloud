// Copyright © 2026 Hanzo AI. MIT License.

package commerce

// The INVOICE LIFECYCLE as typed ops — raise, issue, collect, void, read.
//
// The lifecycle was already implemented and already correct; what it was not was
// REACHABLE. commerce implements the whole of it, and cloud mounted exactly two
// routes off it — the list and the PDF, both GETs (mount.go's billingRead table).
// So an org could read invoices it had no way to create. `tools/list` published
// no invoice tool of any kind, which is why "invoice a customer" was not a step an
// agent could take.
//
// These five ops close that. Each delegates to the SAME core commerce's own HTTP
// handlers now delegate to (invoice_core.go), so mounting the lifecycle here adds
// doors rather than a second lifecycle — which matters most for collect, where a
// second implementation would mean a second idempotency guard and, eventually, an
// invoice charged twice.
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
)

// InvoiceLineIn is one charge to put on an invoice.
type InvoiceLineIn struct {
	// Description is the human-readable line, e.g. "Advisory retainer — August".
	Description string `json:"description"`
	// Amount is the line total in whole cents (250000 is $2,500.00).
	Amount int64 `json:"amount"`
	// Quantity is the number of units, when the line is metered. Optional.
	Quantity int64 `json:"quantity,omitempty"`
	// UnitPrice is the per-unit price in cents, when the line is metered. Optional.
	UnitPrice int64 `json:"unitPrice,omitempty"`
}

// RaiseInvoiceIn is a draft invoice to raise against a customer.
type RaiseInvoiceIn struct {
	// UserID identifies the customer being billed, within the caller's own org.
	// Required — an invoice with no addressee is not an invoice.
	UserID string `json:"userId"`
	// CustomerEmail is where the invoice is sent. Optional.
	CustomerEmail string `json:"customerEmail,omitempty"`
	// Currency is the ISO 4217 code, lower-cased. Empty means usd.
	Currency string `json:"currency,omitempty"`
	// Lines are the charges. The invoice subtotal and amount due are COMPUTED
	// from these — there is no total field to send, because a total that
	// disagreed with its own lines would bill a number nobody could derive.
	Lines []InvoiceLineIn `json:"lines,omitempty"`
}

// InvoiceOut is an invoice.
type InvoiceOut struct {
	// ID is the invoice id — what the issue, collect and void ops address.
	ID string `json:"id"`
	// Number is the human-facing invoice number, e.g. "INV-0042". A draft has
	// none; issuing assigns it.
	Number string `json:"number,omitempty"`
	// UserID is the customer billed.
	UserID string `json:"userId"`
	// CustomerEmail is where it is sent.
	CustomerEmail string `json:"customerEmail,omitempty"`
	// Status is draft, open, paid, void or uncollectible. A draft is not
	// collectible; issuing moves it to open.
	Status string `json:"status"`
	// Currency is the ISO 4217 code.
	Currency string `json:"currency"`
	// SubtotalCents is the sum of the lines.
	SubtotalCents int64 `json:"subtotalCents"`
	// AmountDueCents is what remains collectible.
	AmountDueCents int64 `json:"amountDueCents"`
	// AmountPaidCents is what has been collected so far.
	AmountPaidCents int64 `json:"amountPaidCents"`
	// Lines are the charges on the invoice.
	Lines []InvoiceLineIn `json:"lines,omitempty"`
	// PaymentRef is the processor reference for the collection, once paid.
	PaymentRef string `json:"paymentRef,omitempty"`
	// CreatedAt is when the draft was raised, RFC3339.
	CreatedAt string `json:"createdAt,omitempty"`
}

// InvoiceRefIn names one invoice to act on.
type InvoiceRefIn struct {
	// ID is the invoice id.
	ID string `json:"id"`
}

// CollectOut is the outcome of attempting to collect an invoice.
type CollectOut struct {
	// Invoice is the invoice AFTER the attempt — its status is the authority on
	// what happened, not this struct's other fields.
	Invoice *InvoiceOut `json:"invoice"`
	// Paid reports whether the invoice is now settled in full. A false here with
	// no error is a DECLINE: the invoice stays open and may be collected again.
	Paid bool `json:"paid"`
	// CreditUsedCents is how much was covered by credit grants.
	CreditUsedCents int64 `json:"creditUsedCents"`
	// BalanceUsedCents is how much was covered by prepaid balance.
	BalanceUsedCents int64 `json:"balanceUsedCents"`
	// CardChargedCents is how much was charged to the card on file.
	CardChargedCents int64 `json:"cardChargedCents"`
	// ProcessorRef is the processor's reference for any card charge — the field
	// that proves money moved at the gateway rather than only in our ledger.
	ProcessorRef string `json:"processorRef,omitempty"`
	// Reason explains a decline or partial collection. Empty on success.
	Reason string `json:"reason,omitempty"`
}

// invoiceOps binds the invoice ops to the subsystem. Method values, not
// closures, so zipdoc can lift each op's prose into the registry.
type invoiceOps struct{}

// exposeInvoices publishes the invoice lifecycle. Mount calls it.
//
// The addresses sit under the /v1/billing/invoices prefix commerce already owns
// and the console already reads, so an invoice raised by an agent shows up in the
// same list a human is looking at without anything being told where to look.
func exposeInvoices(app *zip.App) {
	o := invoiceOps{}
	zip.Post(app, "/v1/billing/invoices", o.raise,
		zip.WithOperationID("raiseInvoice"),
		zip.WithSummary("Raise a draft invoice against a customer"),
		zip.WithTags("invoices"),
		zip.WithStatus(http.StatusCreated))
	zip.Get(app, "/v1/billing/invoices/:id", o.read,
		zip.WithOperationID("getInvoice"),
		zip.WithSummary("Read one invoice"),
		zip.WithTags("invoices"))
	zip.Post(app, "/v1/billing/invoices/:id/issue", o.issue,
		zip.WithOperationID("issueInvoice"),
		zip.WithSummary("Issue a draft invoice, making it collectible"),
		zip.WithTags("invoices"))
	zip.Post(app, "/v1/billing/invoices/:id/collect", o.collect,
		zip.WithOperationID("collectInvoice"),
		zip.WithSummary("Collect an issued invoice from credits, balance, then card"),
		zip.WithTags("invoices"))
	zip.Post(app, "/v1/billing/invoices/:id/void", o.void,
		zip.WithOperationID("voidInvoice"),
		zip.WithSummary("Void a draft or issued invoice"),
		zip.WithTags("invoices"))
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
func (invoiceOps) raise(ctx context.Context, in *RaiseInvoiceIn) (*InvoiceOut, error) {
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
func (invoiceOps) read(ctx context.Context, in *InvoiceRefIn) (*InvoiceOut, error) {
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
func (invoiceOps) issue(ctx context.Context, in *InvoiceRefIn) (*InvoiceOut, error) {
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
func (invoiceOps) void(ctx context.Context, in *InvoiceRefIn) (*InvoiceOut, error) {
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
func (invoiceOps) collect(ctx context.Context, in *InvoiceRefIn) (*CollectOut, error) {
	org, err := payingOrg(ctx, "collect invoice")
	if err != nil {
		return nil, err
	}
	res, f := commercebilling.CollectInvoice(ctx, org, in.ID, kmsFrom(ctx), eventsFrom(ctx))
	if f != nil {
		return nil, zip.Errorf(f.Status, "%s", f.Message)
	}
	return &CollectOut{
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
func invoiceAnswer(v *commercebilling.InvoiceView, f *commercebilling.PaymentFault) (*InvoiceOut, error) {
	if f != nil {
		return nil, zip.Errorf(f.Status, "%s", f.Message)
	}
	return viewOf(v), nil
}

// viewOf projects commerce's typed invoice onto this surface's wire type.
func viewOf(v *commercebilling.InvoiceView) *InvoiceOut {
	if v == nil {
		return nil
	}
	out := &InvoiceOut{
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
		out.Lines = append(out.Lines, InvoiceLineIn{
			Description: l.Description,
			Amount:      l.Amount,
			Quantity:    l.Quantity,
			UnitPrice:   l.UnitPrice,
		})
	}
	return out
}

// eventsFrom and kmsFrom lift the two request-scoped side channels off the
// request a typed op is serving, when there is one. Both are optional by design:
// a missing analytics collector must never fail a money move, and a missing KMS
// client is the dev/test posture where credentials come from the environment.
func eventsFrom(ctx context.Context) *events.Client {
	c, ok := cloud.Request(ctx)
	if !ok {
		return nil
	}
	if v := c.Locals("events"); v != nil {
		if ev, ok := v.(*events.Client); ok {
			return ev
		}
	}
	return nil
}

func kmsFrom(ctx context.Context) *kms.CachedClient {
	c, ok := cloud.Request(ctx)
	if !ok {
		return nil
	}
	if v := c.Locals("kms"); v != nil {
		if k, ok := v.(*kms.CachedClient); ok {
			return k
		}
	}
	return nil
}
