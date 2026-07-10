package stripe

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/payment/processor"
)

// newTestProvider creates a Provider pointing at a test HTTP server.
func newTestProvider(t *testing.T, handler http.HandlerFunc) (*Provider, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	p := &Provider{
		BaseProcessor:  processor.NewBaseProcessor(processor.Stripe, supportedCurrencies()),
		secretKey:      "sk_test_REDACTED",
		publishableKey: "pk_test_REDACTED",
		webhookSecret:  "whsec_test_fake",
		client:         srv.Client(),
	}
	p.SetConfigured(true)
	return p, srv
}

// overrideBaseURL temporarily patches the baseURL for testing.
func overridePost(p *Provider, srvURL string) func(ctx context.Context, path string, result interface{}, body string) error {
	return func(ctx context.Context, path string, result interface{}, body string) error {
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, srvURL+path, nil)
		req.SetBasicAuth(p.secretKey, "")
		req.Header.Set("Stripe-Version", apiVersion)
		resp, err := p.client.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		return json.NewDecoder(resp.Body).Decode(result)
	}
}

func TestType(t *testing.T) {
	p := &Provider{
		BaseProcessor: processor.NewBaseProcessor(processor.Stripe, supportedCurrencies()),
	}
	if p.Type() != processor.Stripe {
		t.Errorf("expected %s, got %s", processor.Stripe, p.Type())
	}
}

func TestIsAvailable(t *testing.T) {
	p := &Provider{
		BaseProcessor: processor.NewBaseProcessor(processor.Stripe, supportedCurrencies()),
	}
	if p.IsAvailable(context.Background()) {
		t.Error("expected not available without secret key")
	}

	p.secretKey = "sk_test_REDACTED"
	if !p.IsAvailable(context.Background()) {
		t.Error("expected available with secret key")
	}
}

func TestSupportedCurrencies(t *testing.T) {
	currencies := supportedCurrencies()
	if len(currencies) < 10 {
		t.Errorf("expected at least 10 currencies, got %d", len(currencies))
	}

	has := func(c currency.Type) bool {
		for _, cc := range currencies {
			if cc == c {
				return true
			}
		}
		return false
	}

	for _, c := range []currency.Type{currency.USD, currency.EUR, currency.GBP} {
		if !has(c) {
			t.Errorf("missing required currency %s", c)
		}
	}
}

func TestValidateWebhook_ValidSignature(t *testing.T) {
	p := &Provider{
		BaseProcessor: processor.NewBaseProcessor(processor.Stripe, supportedCurrencies()),
		webhookSecret: "whsec_test_secret",
	}

	payload := []byte(`{"id":"evt_123","type":"payment_intent.succeeded","data":{"object":{"id":"pi_123","amount":1000}}}`)
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)

	// Generate valid signature
	signedPayload := timestamp + "." + string(payload)
	mac := hmac.New(sha256.New, []byte(p.webhookSecret))
	mac.Write([]byte(signedPayload))
	sig := hex.EncodeToString(mac.Sum(nil))

	header := fmt.Sprintf("t=%s,v1=%s", timestamp, sig)

	evt, err := p.ValidateWebhook(context.Background(), payload, header)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if evt.ID != "evt_123" {
		t.Errorf("expected event ID evt_123, got %s", evt.ID)
	}
	if evt.Type != "payment.completed" {
		t.Errorf("expected type payment.completed, got %s", evt.Type)
	}
	if evt.Processor != processor.Stripe {
		t.Errorf("expected processor stripe, got %s", evt.Processor)
	}
}

func TestValidateWebhook_InvalidSignature(t *testing.T) {
	p := &Provider{
		BaseProcessor: processor.NewBaseProcessor(processor.Stripe, supportedCurrencies()),
		webhookSecret: "whsec_test_secret",
	}

	payload := []byte(`{"id":"evt_123","type":"payment_intent.succeeded"}`)
	header := "t=12345,v1=invalidsig"

	_, err := p.ValidateWebhook(context.Background(), payload, header)
	if err != processor.ErrWebhookValidationFailed {
		t.Errorf("expected ErrWebhookValidationFailed, got %v", err)
	}
}

func TestValidateWebhook_ExpiredTimestamp(t *testing.T) {
	p := &Provider{
		BaseProcessor: processor.NewBaseProcessor(processor.Stripe, supportedCurrencies()),
		webhookSecret: "whsec_test_secret",
	}

	payload := []byte(`{"id":"evt_123","type":"test"}`)
	// 10 minutes ago — should be rejected
	oldTimestamp := strconv.FormatInt(time.Now().Add(-10*time.Minute).Unix(), 10)

	signedPayload := oldTimestamp + "." + string(payload)
	mac := hmac.New(sha256.New, []byte(p.webhookSecret))
	mac.Write([]byte(signedPayload))
	sig := hex.EncodeToString(mac.Sum(nil))

	header := fmt.Sprintf("t=%s,v1=%s", oldTimestamp, sig)

	_, err := p.ValidateWebhook(context.Background(), payload, header)
	if err != processor.ErrWebhookValidationFailed {
		t.Errorf("expected ErrWebhookValidationFailed for expired timestamp, got %v", err)
	}
}

func TestValidateWebhook_EmptySecret(t *testing.T) {
	p := &Provider{
		BaseProcessor: processor.NewBaseProcessor(processor.Stripe, supportedCurrencies()),
		webhookSecret: "",
	}

	_, err := p.ValidateWebhook(context.Background(), []byte("test"), "t=1,v1=abc")
	if err != processor.ErrWebhookValidationFailed {
		t.Errorf("expected ErrWebhookValidationFailed with empty secret, got %v", err)
	}
}

func TestMapEventType(t *testing.T) {
	cases := map[string]string{
		"payment_intent.succeeded":         "payment.completed",
		"payment_intent.payment_failed":    "payment.failed",
		"charge.refunded":                  "refund.succeeded",
		"charge.dispute.created":           "dispute.created",
		"customer.subscription.created":    "subscription.created",
		"customer.subscription.deleted":    "subscription.canceled",
		"invoice.paid":                     "invoice.paid",
		"invoice.payment_failed":           "invoice.payment_failed",
		"customer.created":                 "customer.created",
		"unknown.event.type":               "unknown.event.type",
	}
	for input, expected := range cases {
		got := mapEventType(input)
		if got != expected {
			t.Errorf("mapEventType(%q) = %q, want %q", input, got, expected)
		}
	}
}

func TestMapRefundReason(t *testing.T) {
	cases := map[string]string{
		"duplicate":   "duplicate",
		"fraudulent":  "fraudulent",
		"fraud":       "fraudulent",
		"other":       "requested_by_customer",
		"":            "requested_by_customer",
	}
	for input, expected := range cases {
		got := mapRefundReason(input)
		if got != expected {
			t.Errorf("mapRefundReason(%q) = %q, want %q", input, got, expected)
		}
	}
}

func TestParseSignatureHeader(t *testing.T) {
	header := "t=1614556828,v1=abc123,v0=oldval"
	parts := parseSignatureHeader(header)

	if parts["t"] != "1614556828" {
		t.Errorf("t = %q, want 1614556828", parts["t"])
	}
	if parts["v1"] != "abc123" {
		t.Errorf("v1 = %q, want abc123", parts["v1"])
	}
	if parts["v0"] != "oldval" {
		t.Errorf("v0 = %q, want oldval", parts["v0"])
	}
}

func TestMapSubscription(t *testing.T) {
	sub := &stripeSub{
		ID:                 "sub_123",
		Status:             "active",
		Customer:           "cus_456",
		CurrentPeriodStart: 1700000000,
		CurrentPeriodEnd:   1702592000,
		CancelAtPeriodEnd:  false,
		Metadata:           map[string]interface{}{"plan": "pro"},
	}
	sub.Items.Data = append(sub.Items.Data, struct {
		ID    string `json:"id"`
		Price struct {
			ID string `json:"id"`
		} `json:"price"`
		Quantity int `json:"quantity"`
	}{
		ID: "si_789",
		Price: struct {
			ID string `json:"id"`
		}{ID: "price_abc"},
		Quantity: 1,
	})

	result := mapSubscription(sub)

	if result.ID != "sub_123" {
		t.Errorf("ID = %q, want sub_123", result.ID)
	}
	if result.CustomerID != "cus_456" {
		t.Errorf("CustomerID = %q, want cus_456", result.CustomerID)
	}
	if result.PlanID != "price_abc" {
		t.Errorf("PlanID = %q, want price_abc", result.PlanID)
	}
	if result.Status != "active" {
		t.Errorf("Status = %q, want active", result.Status)
	}
	if result.CancelAtPeriodEnd {
		t.Error("CancelAtPeriodEnd should be false")
	}
}

func TestInterfaceCompliance(t *testing.T) {
	var _ processor.PaymentProcessor = (*Provider)(nil)
	var _ processor.SubscriptionProcessor = (*Provider)(nil)
	var _ processor.CustomerProcessor = (*Provider)(nil)
}
