// Copyright © 2026 Hanzo AI. MIT License.

package commerce

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	commercebilling "github.com/hanzoai/commerce/api/billing"
	"github.com/zap-proto/zip"
)

// The invoice's WIRE PROJECTION — what a customer is shown they were charged, and
// the one place a payment FAULT is turned into a refusal rather than into a
// document. Neither had a test.

// TestAFaultIsARefusalAndNotAnInvoice is the branch that matters most here. A
// payment that faulted must not come back as an invoice at all: an answer shaped
// like a document reads as one, and a customer would be shown a charge the
// processor declined.
func TestAFaultIsARefusalAndNotAnInvoice(t *testing.T) {
	view := &commercebilling.InvoiceView{ID: "in_1", AmountDueCents: 5000}
	fault := &commercebilling.PaymentFault{Status: http.StatusPaymentRequired, Message: "card declined"}

	got, err := invoiceAnswer(view, fault)

	if err == nil {
		t.Fatal("a fault must refuse; answering the invoice beside it shows a charge that did not happen")
	}
	if got != nil {
		t.Errorf("a refusal carries no invoice, got %+v", got)
	}
	// The processor's own verdict reaches the caller — status and words.
	var he *zip.HTTPError
	if !errors.As(err, &he) {
		t.Fatalf("the refusal must carry a status, got %T", err)
	}
	if he.Status != http.StatusPaymentRequired {
		t.Errorf("status = %d, want the fault's %d", he.Status, http.StatusPaymentRequired)
	}
	if !strings.Contains(he.Msg, "card declined") {
		t.Errorf("the reason must be the processor's, got %q", he.Msg)
	}
}

// TestNoFaultAnswersTheInvoice is the other side of that branch.
func TestNoFaultAnswersTheInvoice(t *testing.T) {
	got, err := invoiceAnswer(&commercebilling.InvoiceView{ID: "in_1", Number: "0001"}, nil)
	if err != nil {
		t.Fatalf("a clean payment must answer: %v", err)
	}
	if got == nil || got.ID != "in_1" || got.Number != "0001" {
		t.Errorf("answered %+v, want the invoice", got)
	}
}

// TestEveryInvoiceAmountReachesTheWire pins the three amounts a customer reads.
// Due and paid are DIFFERENT facts — a partially paid invoice is the ordinary
// case — so collapsing either onto the other would misreport what is owed.
func TestEveryInvoiceAmountReachesTheWire(t *testing.T) {
	got := viewOf(&commercebilling.InvoiceView{
		ID:              "in_1",
		Number:          "0007",
		UserID:          "usr_1",
		CustomerEmail:   "buyer@example.test",
		Status:          "open",
		Currency:        "usd",
		SubtotalCents:   10000,
		AmountDueCents:  4000,
		AmountPaidCents: 6000,
		PaymentRef:      "pi_123",
		CreatedAt:       "2026-01-01T00:00:00Z",
	})

	if got == nil {
		t.Fatal("a view must project")
	}
	for _, c := range []struct {
		name      string
		got, want int64
	}{
		{"subtotal", got.SubtotalCents, 10000},
		{"amount due", got.AmountDueCents, 4000},
		{"amount paid", got.AmountPaidCents, 6000},
	} {
		if c.got != c.want {
			t.Errorf("%s = %d, want %d", c.name, c.got, c.want)
		}
	}
	for _, c := range []struct {
		name      string
		got, want string
	}{
		{"id", got.ID, "in_1"},
		{"number", got.Number, "0007"},
		{"user", got.UserID, "usr_1"},
		{"email", got.CustomerEmail, "buyer@example.test"},
		{"status", got.Status, "open"},
		{"currency", got.Currency, "usd"},
		{"payment ref", got.PaymentRef, "pi_123"},
		{"created", got.CreatedAt, "2026-01-01T00:00:00Z"},
	} {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q", c.name, c.got, c.want)
		}
	}
}

// TestEveryLineOfAnInvoiceIsProjected guards the loop a customer checks the total
// against.
func TestEveryLineOfAnInvoiceIsProjected(t *testing.T) {
	got := viewOf(&commercebilling.InvoiceView{Lines: []commercebilling.InvoiceLine{
		{Description: "Seats", Amount: 6000, Quantity: 3, UnitPrice: 2000},
		{Description: "Support", Amount: 4000, Quantity: 1, UnitPrice: 4000},
	}})

	if len(got.Lines) != 2 {
		t.Fatalf("projected %d lines, want 2", len(got.Lines))
	}
	first := got.Lines[0]
	if first.Description != "Seats" || first.Amount != 6000 || first.Quantity != 3 || first.UnitPrice != 2000 {
		t.Errorf("first line = %+v, want the seats line intact", first)
	}
	if got.Lines[1].Description != "Support" {
		t.Errorf("lines came back out of order: %+v", got.Lines)
	}
}

// TestNothingToProjectIsNothing pins the nil path, which the list op reaches for
// an invoice that is not there. A zero-valued invoice would answer with an empty
// document rather than with absence.
func TestNothingToProjectIsNothing(t *testing.T) {
	if got := viewOf(nil); got != nil {
		t.Errorf("viewOf(nil) = %+v, want nil", got)
	}
}
