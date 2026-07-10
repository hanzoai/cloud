package billing

import (
	"testing"

	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"

	. "github.com/hanzoai/cloud/clients/commerce/types"
)

// Square nests the changed resource under data.object.payment. The processor
// passes data.object through as the event's Data map, so the settlement parser
// must unwrap the "payment" key before reading id/status/amount.
func TestSettlementParsing_SquareNestedPayment(t *testing.T) {
	// Shape of a real Square payment.updated event's data.object.
	data := Map{
		"payment": map[string]interface{}{
			"id":           "pay_ABC123",
			"status":       "COMPLETED",
			"reference_id": "user-42",
			"amount_money": map[string]interface{}{
				"amount":   float64(2500),
				"currency": "USD",
			},
		},
	}

	pay := unwrapObject(data, "payment")
	if got := stringField(pay, "id"); got != "pay_ABC123" {
		t.Fatalf("payment id = %q, want pay_ABC123 (unwrap failed)", got)
	}
	if got := stringField(pay, "status"); got != "COMPLETED" {
		t.Fatalf("status = %q, want COMPLETED", got)
	}
	if got := stringField(pay, "reference_id"); got != "user-42" {
		t.Fatalf("reference_id = %q, want user-42", got)
	}

	amount, cur := settlementAmount(pay)
	if amount != currency.Cents(2500) {
		t.Fatalf("amount = %d, want 2500", amount)
	}
	if cur != currency.USD {
		t.Fatalf("currency = %q, want usd", cur)
	}
}

// Processors that expose fields at the top level (no nesting) must still parse.
func TestSettlementParsing_FlatFallback(t *testing.T) {
	data := Map{
		"id":       "pay_FLAT",
		"status":   "completed",
		"currency": "eur",
		"amount":   float64(199),
	}
	pay := unwrapObject(data, "payment") // no "payment" key -> returns data itself
	if got := stringField(pay, "id"); got != "pay_FLAT" {
		t.Fatalf("flat id = %q, want pay_FLAT", got)
	}
	amount, cur := settlementAmount(pay)
	if amount != currency.Cents(199) || cur != currency.Type("eur") {
		t.Fatalf("flat amount/cur = %d/%q, want 199/eur", amount, cur)
	}
}

func TestIsSettlementEvent(t *testing.T) {
	settling := []string{"payment.completed", "payment.updated", "invoice.paid"}
	for _, ty := range settling {
		if !isSettlementEvent(ty) {
			t.Errorf("%s should be a settlement event", ty)
		}
	}
	for _, ty := range []string{"payment.created", "refund.created", "dispute.created", ""} {
		if isSettlementEvent(ty) {
			t.Errorf("%s should NOT be a settlement event", ty)
		}
	}
}

// The Square signature header must be picked up by the ingress.
func TestPickSignatureHeader_Square(t *testing.T) {
	h := map[string][]string{"X-Square-Hmacsha256-Signature": {"sig123"}}
	if got := pickSignatureHeader(h, "square"); got != "sig123" {
		t.Fatalf("square signature header not picked: %q", got)
	}
	// Also found without a hint (header-name fallback).
	if got := pickSignatureHeader(h, ""); got != "sig123" {
		t.Fatalf("square signature header not picked without hint: %q", got)
	}
}
