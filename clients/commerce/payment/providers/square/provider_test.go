package square

import (
	"context"
	"testing"

	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/payment/processor"
)

func newTestProvider() *Provider {
	return &Provider{
		BaseProcessor: processor.NewBaseProcessor(processor.Square, SupportedCurrenciesForTest()),
	}
}

// SupportedCurrenciesForTest re-exports the inner currency list without
// requiring the test to import thirdparty/ directly.
func SupportedCurrenciesForTest() []currency.Type {
	p := NewProvider(Config{AccessToken: "x", LocationID: "x"})
	return p.SupportedCurrencies()
}

func TestType(t *testing.T) {
	p := newTestProvider()
	if got := p.Type(); got != processor.Square {
		t.Errorf("Type() = %q, want %q", got, processor.Square)
	}
}

func TestIsAvailable_NotConfigured(t *testing.T) {
	p := newTestProvider()
	if p.IsAvailable(context.Background()) {
		t.Error("IsAvailable() = true for unconfigured provider, want false")
	}
}

func TestIsAvailable_EmptyCredentials(t *testing.T) {
	p := newTestProvider()
	p.Configure(Config{AccessToken: "", LocationID: ""})
	if p.IsAvailable(context.Background()) {
		t.Error("IsAvailable() = true with empty credentials, want false")
	}
}

func TestIsAvailable_MissingLocation(t *testing.T) {
	p := newTestProvider()
	p.Configure(Config{AccessToken: "tok", LocationID: ""})
	if p.IsAvailable(context.Background()) {
		t.Error("IsAvailable() = true with missing location ID, want false")
	}
}

func TestIsAvailable_MissingAccessToken(t *testing.T) {
	p := newTestProvider()
	p.Configure(Config{AccessToken: "", LocationID: "L1"})
	if p.IsAvailable(context.Background()) {
		t.Error("IsAvailable() = true with missing access token, want false")
	}
}

func TestConfigure_Valid(t *testing.T) {
	p := newTestProvider()
	p.Configure(Config{
		ApplicationID:       "app-id",
		AccessToken:         "tok",
		LocationID:          "L1",
		WebhookSignatureKey: "wh-key",
		Environment:         "sandbox",
	})
	if !p.IsAvailable(context.Background()) {
		t.Error("IsAvailable() = false after valid Configure(), want true")
	}
	if got := p.ApplicationID(); got != "app-id" {
		t.Errorf("ApplicationID() = %q, want app-id", got)
	}
	if got := p.LocationID(); got != "L1" {
		t.Errorf("LocationID() = %q, want L1", got)
	}
	if got := p.Environment(); got != "sandbox" {
		t.Errorf("Environment() = %q, want sandbox", got)
	}
}

func TestConfigure_Production(t *testing.T) {
	p := newTestProvider()
	p.Configure(Config{
		AccessToken: "tok",
		LocationID:  "L1",
		Environment: "production",
	})
	if !p.IsAvailable(context.Background()) {
		t.Error("IsAvailable() = false after production Configure(), want true")
	}
	if got := p.Environment(); got != "production" {
		t.Errorf("Environment() = %q, want production", got)
	}
}

func TestConfigure_Reconfigure_KeysRotate(t *testing.T) {
	p := newTestProvider()
	p.Configure(Config{AccessToken: "old", LocationID: "L1"})
	if !p.IsAvailable(context.Background()) {
		t.Fatal("expected available after first configure")
	}
	// Simulate an admin UI credential rotation.
	p.Configure(Config{AccessToken: "new", LocationID: "L2"})
	if p.LocationID() != "L2" {
		t.Errorf("LocationID after rotation = %q, want L2", p.LocationID())
	}
}

func TestConfigure_EmptyDisables(t *testing.T) {
	p := newTestProvider()
	p.Configure(Config{AccessToken: "tok", LocationID: "L1"})
	if !p.IsAvailable(context.Background()) {
		t.Fatal("expected available after valid configure")
	}
	p.Configure(Config{AccessToken: "", LocationID: ""})
	if p.IsAvailable(context.Background()) {
		t.Error("expected not available after reconfigure with empty credentials")
	}
}

func TestSupportedCurrencies_IncludesUSD(t *testing.T) {
	p := NewProvider(Config{AccessToken: "x", LocationID: "x"})
	found := false
	for _, c := range p.SupportedCurrencies() {
		if c == currency.USD {
			found = true
			break
		}
	}
	if !found {
		t.Error("SupportedCurrencies() does not include USD")
	}
}

func TestNewProvider_UnconfiguredReturnsNoopCapable(t *testing.T) {
	// Empty config should produce a Provider that reports not-available
	// rather than panic at use.
	p := NewProvider(Config{})
	if p.IsAvailable(context.Background()) {
		t.Error("empty NewProvider should not be available")
	}
}

func TestInterfaceCompliance(t *testing.T) {
	var _ processor.PaymentProcessor = newTestProvider()
}

// Charge / Authorize / Refund / ValidateWebhook on an unconfigured
// provider must return NOT_CONFIGURED, not panic.

func TestCharge_Unconfigured(t *testing.T) {
	p := newTestProvider()
	_, err := p.Charge(context.Background(), processor.PaymentRequest{
		Amount: 100, Currency: currency.USD, Token: "cnon:card-nonce-ok",
	})
	if err == nil {
		t.Fatal("expected error on unconfigured Charge")
	}
}

func TestAuthorize_Unconfigured(t *testing.T) {
	p := newTestProvider()
	_, err := p.Authorize(context.Background(), processor.PaymentRequest{
		Amount: 100, Currency: currency.USD, Token: "cnon:card-nonce-ok",
	})
	if err == nil {
		t.Fatal("expected error on unconfigured Authorize")
	}
}

func TestCapture_Unconfigured(t *testing.T) {
	p := newTestProvider()
	_, err := p.Capture(context.Background(), "tx", 100)
	if err == nil {
		t.Fatal("expected error on unconfigured Capture")
	}
}

func TestRefund_Unconfigured(t *testing.T) {
	p := newTestProvider()
	_, err := p.Refund(context.Background(), processor.RefundRequest{
		TransactionID: "tx", Amount: 100,
	})
	if err == nil {
		t.Fatal("expected error on unconfigured Refund")
	}
}

func TestGetTransaction_Unconfigured(t *testing.T) {
	p := newTestProvider()
	_, err := p.GetTransaction(context.Background(), "tx")
	if err == nil {
		t.Fatal("expected error on unconfigured GetTransaction")
	}
}

func TestValidateWebhook_Unconfigured(t *testing.T) {
	p := newTestProvider()
	_, err := p.ValidateWebhook(context.Background(), []byte(`{}`), "sig")
	if err == nil {
		t.Fatal("expected error on unconfigured ValidateWebhook")
	}
}

func TestCancelAuthorization_Unconfigured(t *testing.T) {
	p := newTestProvider()
	err := p.CancelAuthorization(context.Background(), "tx")
	if err == nil {
		t.Fatal("expected error on unconfigured CancelAuthorization")
	}
}
