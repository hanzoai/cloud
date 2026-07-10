package processor_test

import (
	"context"
	"os"
	"testing"

	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/payment/processor"
	"github.com/hanzoai/cloud/clients/commerce/payment/providers/stripe"
	square "github.com/hanzoai/cloud/clients/commerce/thirdparty/square"
)

// withDefaultDisabledPolicy guarantees COMMERCE_DISABLED_PROCESSORS is UNSET for
// the test, so DefaultConfig() applies the safe default (Stripe disabled). t.Setenv
// cannot unset, so we save/unset/restore by hand.
func withDefaultDisabledPolicy(t *testing.T) {
	t.Helper()
	if v, ok := os.LookupEnv("COMMERCE_DISABLED_PROCESSORS"); ok {
		if err := os.Unsetenv("COMMERCE_DISABLED_PROCESSORS"); err != nil {
			t.Fatalf("unset COMMERCE_DISABLED_PROCESSORS: %v", err)
		}
		t.Cleanup(func() { _ = os.Setenv("COMMERCE_DISABLED_PROCESSORS", v) })
	}
}

// configuredStripe returns a REAL Stripe provider whose IsAvailable() is true —
// i.e. a Stripe secret key IS hydrated. The decisive property under test is that
// such a Stripe is STILL never selected for a new charge.
func configuredStripe() processor.PaymentProcessor {
	return stripe.NewProvider(stripe.Config{SecretKey: "sk_live_DECISIVE_FAKE"})
}

// configuredSquare returns a REAL Square processor with creds (IsAvailable()==true).
func configuredSquare() processor.PaymentProcessor {
	return square.NewProcessor(square.Config{
		AccessToken: "sq-access", LocationID: "sq-loc", Environment: "production",
	})
}

// fakeProcessor is a minimal PaymentProcessor for branches where a real provider
// is heavy (crypto/MPC). BaseProcessor supplies Type/SupportedCurrencies/
// IsAvailable/Authorize/Capture; we add the rest.
type fakeProcessor struct{ *processor.BaseProcessor }

func newFakeProcessor(t processor.ProcessorType, available bool, cur []currency.Type) *fakeProcessor {
	bp := processor.NewBaseProcessor(t, cur)
	bp.SetConfigured(available)
	return &fakeProcessor{BaseProcessor: bp}
}

func (f *fakeProcessor) Charge(context.Context, processor.PaymentRequest) (*processor.PaymentResult, error) {
	return &processor.PaymentResult{Success: true, TransactionID: "fake", Status: "succeeded"}, nil
}
func (f *fakeProcessor) Refund(context.Context, processor.RefundRequest) (*processor.RefundResult, error) {
	return &processor.RefundResult{Success: true}, nil
}
func (f *fakeProcessor) GetTransaction(context.Context, string) (*processor.Transaction, error) {
	return &processor.Transaction{ID: "fake"}, nil
}
func (f *fakeProcessor) ValidateWebhook(context.Context, []byte, string) (*processor.WebhookEvent, error) {
	return &processor.WebhookEvent{}, nil
}

// THE DECISIVE TEST: a USD top-up selects Square, NEVER Stripe, even when a
// Stripe secret key IS hydrated (Stripe.IsAvailable()==true). Proves the disable
// is deterministic, not credential-dependent.
func TestSelectProcessor_USD_NeverStripe_EvenWhenStripeSecretKeySet(t *testing.T) {
	withDefaultDisabledPolicy(t)
	reg := processor.NewRegistry(processor.DefaultConfig())
	reg.Register(configuredStripe()) // IsAvailable()==true (secret key hydrated)
	reg.Register(configuredSquare())

	proc, err := reg.SelectProcessor(context.Background(), processor.PaymentRequest{
		Amount: 500, Currency: currency.USD,
	})
	if err != nil {
		t.Fatalf("SelectProcessor(USD): %v", err)
	}
	if proc.Type() != processor.Square {
		t.Fatalf("USD selected %q; want square — Stripe must be un-selectable even with a hydrated secret key", proc.Type())
	}
}

// Stripe is never selected for ANY Square-supported fiat currency under the default config.
func TestSelectProcessor_StripeNeverSelected_AllSquareFiat(t *testing.T) {
	withDefaultDisabledPolicy(t)
	reg := processor.NewRegistry(processor.DefaultConfig())
	reg.Register(configuredStripe())
	reg.Register(configuredSquare())

	for _, cur := range []currency.Type{currency.USD, currency.EUR, currency.GBP, currency.CAD, currency.AUD, currency.JPY} {
		proc, err := reg.SelectProcessor(context.Background(), processor.PaymentRequest{Amount: 200, Currency: cur})
		if err != nil {
			t.Fatalf("SelectProcessor(%s): %v", cur, err)
		}
		if proc.Type() == processor.Stripe {
			t.Fatalf("currency %s selected Stripe; Stripe must never be selected", cur)
		}
		if proc.Type() != processor.Square {
			t.Fatalf("currency %s selected %q; want square", cur, proc.Type())
		}
	}
}

// An EXPLICIT stripe preference (Options["processor"]) is denied when Stripe is
// disabled — it falls through to Square, mirroring the registry's deny contract.
func TestSelectProcessor_ExplicitStripePreference_DeniedFallsThroughToSquare(t *testing.T) {
	withDefaultDisabledPolicy(t)
	reg := processor.NewRegistry(processor.DefaultConfig())
	reg.Register(configuredStripe())
	reg.Register(configuredSquare())

	for _, pref := range []interface{}{"stripe", processor.Stripe} {
		proc, err := reg.SelectProcessor(context.Background(), processor.PaymentRequest{
			Amount: 100, Currency: currency.USD,
			Options: map[string]interface{}{"processor": pref},
		})
		if err != nil {
			t.Fatalf("SelectProcessor(explicit %v): %v", pref, err)
		}
		if proc.Type() != processor.Square {
			t.Fatalf("explicit stripe pref %v must be denied and fall through to square; got %q", pref, proc.Type())
		}
	}
}

// Stripe is SO un-selectable that a Stripe-only currency (Square unsupported)
// yields NO processor rather than falling back to Stripe. This is the intended
// "an org with only Stripe creds can't charge" behavior.
func TestSelectProcessor_StripeOnlyCurrency_YieldsNoProcessor(t *testing.T) {
	withDefaultDisabledPolicy(t)
	reg := processor.NewRegistry(processor.DefaultConfig())
	reg.Register(configuredStripe()) // supports CHF
	reg.Register(configuredSquare()) // does NOT support CHF

	if _, err := reg.SelectProcessor(context.Background(), processor.PaymentRequest{
		Amount: 100, Currency: currency.CHF,
	}); err == nil {
		t.Fatal("CHF (Square-unsupported, Stripe-only) must yield NO processor since Stripe is disabled; got one")
	}
}

// Crypto routing is unchanged: a crypto currency selects the crypto processor (MPC).
func TestSelectProcessor_Crypto_SelectsMPC(t *testing.T) {
	withDefaultDisabledPolicy(t)
	reg := processor.NewRegistry(processor.DefaultConfig())
	reg.Register(configuredStripe())
	reg.Register(configuredSquare())
	reg.Register(newFakeProcessor(processor.MPC, true, []currency.Type{currency.BTC}))

	proc, err := reg.SelectProcessor(context.Background(), processor.PaymentRequest{Amount: 1000, Currency: currency.BTC})
	if err != nil {
		t.Fatalf("SelectProcessor(BTC): %v", err)
	}
	if proc.Type() != processor.MPC {
		t.Fatalf("crypto selected %q; want mpc", proc.Type())
	}
}

// Default policy disables Stripe ONLY (Square stays enabled).
func TestDisabledByPolicy_DefaultDisablesStripeOnly(t *testing.T) {
	withDefaultDisabledPolicy(t)
	if !processor.DisabledByPolicy(processor.Stripe) {
		t.Fatal("default policy must disable Stripe")
	}
	if processor.DisabledByPolicy(processor.Square) {
		t.Fatal("default policy must NOT disable Square")
	}
}

// M1: COMMERCE_DISABLED_PROCESSORS empty or whitespace (a k8s placeholder) must
// NOT re-enable Stripe — it keeps the safe default {Stripe}.
func TestDisabledByPolicy_EmptyKeepsDefault(t *testing.T) {
	for _, v := range []string{"", "   "} {
		t.Setenv("COMMERCE_DISABLED_PROCESSORS", v)
		if !processor.DisabledByPolicy(processor.Stripe) {
			t.Fatalf("COMMERCE_DISABLED_PROCESSORS=%q must keep Stripe disabled (safe default)", v)
		}
	}
}

// The explicit sentinel "none" re-enables Stripe, but Square stays first in
// priority for a USD charge; with only Stripe registered, Stripe becomes selectable.
func TestSelectProcessor_EnvOverride_NoneReenablesStripeBehindSquare(t *testing.T) {
	t.Setenv("COMMERCE_DISABLED_PROCESSORS", "none")
	if processor.DisabledByPolicy(processor.Stripe) {
		t.Fatal(`COMMERCE_DISABLED_PROCESSORS=none must disable nothing`)
	}

	reg := processor.NewRegistry(processor.DefaultConfig())
	reg.Register(configuredStripe())
	reg.Register(configuredSquare())
	proc, err := reg.SelectProcessor(context.Background(), processor.PaymentRequest{Amount: 100, Currency: currency.USD})
	if err != nil {
		t.Fatalf("SelectProcessor(USD): %v", err)
	}
	if proc.Type() != processor.Square {
		t.Fatalf("with Stripe re-enabled, USD still prefers Square first; got %q", proc.Type())
	}

	regStripeOnly := processor.NewRegistry(processor.DefaultConfig())
	regStripeOnly.Register(configuredStripe())
	proc2, err := regStripeOnly.SelectProcessor(context.Background(), processor.PaymentRequest{Amount: 100, Currency: currency.USD})
	if err != nil {
		t.Fatalf("SelectProcessor(USD) stripe-only: %v", err)
	}
	if proc2.Type() != processor.Stripe {
		t.Fatalf(`with COMMERCE_DISABLED_PROCESSORS=none and only Stripe registered, want stripe; got %q`, proc2.Type())
	}
}

// The deny list REPLACES the default: disabling Square leaves Stripe enabled.
func TestDisabledByPolicy_EnvOverride_ReplacesDefault(t *testing.T) {
	t.Setenv("COMMERCE_DISABLED_PROCESSORS", "square")
	if !processor.DisabledByPolicy(processor.Square) {
		t.Fatal("COMMERCE_DISABLED_PROCESSORS=square must disable Square")
	}
	if processor.DisabledByPolicy(processor.Stripe) {
		t.Fatal("COMMERCE_DISABLED_PROCESSORS=square replaces the default, so Stripe is NOT disabled")
	}
}
