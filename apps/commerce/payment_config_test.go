package commerce

import (
	"encoding/json"
	"testing"

	"github.com/hanzoai/cloud/client"
	commercebilling "github.com/hanzoai/commerce/api/billing"
)

// The browser picks its card form from `stripe` and `cards`. A plane that drops
// them answers Square-only for an org commerce serves with Stripe, so checkout
// never mounts Stripe's form.
func TestPaymentConfigCarriesStripeAndCards(t *testing.T) {
	in := commercebilling.PaymentConfig{
		Provider: "square",
		Stripe:   commercebilling.StripePublicConfig{PublishableKey: "pk_test_abc", Environment: "sandbox", Enabled: true},
		Cards:    map[string]string{"usd": "stripe"},
	}
	out := paymentConfigOf(&in)
	if out.Stripe == nil || !out.Stripe.Enabled || out.Stripe.PublishableKey != "pk_test_abc" || out.Stripe.Environment != "sandbox" {
		t.Fatalf("stripe = %+v, want enabled pk_test_abc sandbox", out.Stripe)
	}
	if len(out.Cards) != 1 || out.Cards[0] != (client.PaymentCard{Currency: "usd", Processor: "stripe"}) {
		t.Fatalf("cards = %v, want usd -> stripe", out.Cards)
	}
	// The browser reads cards as an object, whatever layout the plane carries.
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		Cards  map[string]string `json:"cards"`
		Stripe struct {
			PublishableKey string `json:"publishableKey"`
		} `json:"stripe"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	if wire.Cards["usd"] != "stripe" || wire.Stripe.PublishableKey != "pk_test_abc" {
		t.Fatalf("settings JSON = %s, want cards.usd=stripe and stripe.publishableKey", raw)
	}
	if paymentConfigOf(&commercebilling.PaymentConfig{}).Stripe != nil {
		t.Fatal("an org with no Stripe key reported a Stripe posture")
	}
}
