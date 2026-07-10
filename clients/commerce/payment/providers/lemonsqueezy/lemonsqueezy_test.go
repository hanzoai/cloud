package lemonsqueezy

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/payment/processor"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func newTestProvider() *Provider {
	return &Provider{
		BaseProcessor: processor.NewBaseProcessor(processor.LemonSqueezy, supportedCurrencies()),
	}
}

func configuredProvider(serverURL string) *Provider {
	p := &Provider{
		BaseProcessor:    processor.NewBaseProcessor(processor.LemonSqueezy, supportedCurrencies()),
		apiKey:           "test-api-key",
		storeID:          "store-123",
		webhookSecret:    "webhook-secret",
		defaultVariantID: "variant-456",
		client: &http.Client{
			Transport: &rewriteTransport{base: http.DefaultTransport, targetURL: serverURL},
		},
	}
	p.SetConfigured(true)
	return p
}

type rewriteTransport struct {
	base      http.RoundTripper
	targetURL string
}

func (t *rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.URL.Scheme = "http"
	req.URL.Host = strings.TrimPrefix(t.targetURL, "http://")
	return t.base.RoundTrip(req)
}

// ---------------------------------------------------------------------------
// Tests: Type, IsAvailable, SupportedCurrencies, Configure
// ---------------------------------------------------------------------------

func TestType(t *testing.T) {
	p := newTestProvider()
	if got := p.Type(); got != processor.LemonSqueezy {
		t.Errorf("Type() = %q, want %q", got, processor.LemonSqueezy)
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
	p.Configure(Config{APIKey: "", StoreID: ""})
	if p.IsAvailable(context.Background()) {
		t.Error("IsAvailable() = true with empty credentials, want false")
	}
}

func TestIsAvailable_PartialCredentials_APIKeyOnly(t *testing.T) {
	p := newTestProvider()
	p.Configure(Config{APIKey: "key", StoreID: ""})
	if p.IsAvailable(context.Background()) {
		t.Error("IsAvailable() = true with only API key, want false")
	}
}

func TestIsAvailable_PartialCredentials_StoreIDOnly(t *testing.T) {
	p := newTestProvider()
	p.Configure(Config{APIKey: "", StoreID: "store"})
	if p.IsAvailable(context.Background()) {
		t.Error("IsAvailable() = true with only store ID, want false")
	}
}

func TestConfigure_Valid(t *testing.T) {
	p := newTestProvider()
	p.Configure(Config{APIKey: "test-key", StoreID: "store-123"})
	if !p.IsAvailable(context.Background()) {
		t.Error("IsAvailable() = false after valid Configure(), want true")
	}
}

func TestConfigure_WithWebhookSecret(t *testing.T) {
	p := newTestProvider()
	p.Configure(Config{
		APIKey:        "test-key",
		StoreID:       "store-123",
		WebhookSecret: "whsec_test",
	})
	if !p.IsAvailable(context.Background()) {
		t.Error("IsAvailable() = false after Configure() with webhook secret, want true")
	}
}

func TestConfigure_WithDefaultVariant(t *testing.T) {
	p := newTestProvider()
	p.Configure(Config{
		APIKey:           "test-key",
		StoreID:          "store-123",
		DefaultVariantID: "variant-456",
	})
	if !p.IsAvailable(context.Background()) {
		t.Error("IsAvailable() = false after Configure() with default variant, want true")
	}
}

func TestConfigure_Reconfigure(t *testing.T) {
	p := newTestProvider()
	p.Configure(Config{APIKey: "key", StoreID: "store"})
	if !p.IsAvailable(context.Background()) {
		t.Error("expected available after first configure")
	}
	p.Configure(Config{APIKey: "", StoreID: ""})
	if p.IsAvailable(context.Background()) {
		t.Error("expected not available after reconfigure with empty credentials")
	}
}

func TestSupportedCurrencies(t *testing.T) {
	p := newTestProvider()
	currencies := p.SupportedCurrencies()
	if len(currencies) == 0 {
		t.Fatal("SupportedCurrencies() returned empty slice")
	}

	found := false
	for _, c := range currencies {
		if c == currency.USD {
			found = true
			break
		}
	}
	if !found {
		t.Error("SupportedCurrencies() does not include USD")
	}
}

func TestSupportedCurrencies_ContainsExpected(t *testing.T) {
	p := newTestProvider()
	currencies := p.SupportedCurrencies()

	expected := []currency.Type{currency.USD, currency.EUR, currency.GBP, currency.BRL}
	for _, want := range expected {
		found := false
		for _, got := range currencies {
			if got == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("SupportedCurrencies() missing %s", want)
		}
	}
}

func TestSupportedCurrencies_Count(t *testing.T) {
	p := newTestProvider()
	currencies := p.SupportedCurrencies()
	if len(currencies) != 7 {
		t.Errorf("SupportedCurrencies() returned %d currencies, want 7", len(currencies))
	}
}

func TestInterfaceCompliance(t *testing.T) {
	var _ processor.PaymentProcessor = newTestProvider()
}

// ---------------------------------------------------------------------------
// Tests: Helper functions
// ---------------------------------------------------------------------------

func TestMapOrderStatus(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"paid", "completed"},
		{"Paid", "completed"},
		{"pending", "pending"},
		{"failed", "failed"},
		{"refunded", "refunded"},
		{"partial_refund", "partially_refunded"},
		{"other", "other"},
	}
	for _, tt := range tests {
		if got := mapOrderStatus(tt.input); got != tt.want {
			t.Errorf("mapOrderStatus(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestApiErrorToPaymentError(t *testing.T) {
	errs := []apiError{
		{Status: "422", Title: "Validation Error", Detail: "Amount is required"},
	}
	pe := apiErrorToPaymentError(errs)
	if pe.Code != "422" {
		t.Errorf("code = %q, want 422", pe.Code)
	}
	if pe.Message != "Amount is required" {
		t.Errorf("message = %q, want Amount is required", pe.Message)
	}
}

func TestApiErrorToPaymentError_Empty(t *testing.T) {
	pe := apiErrorToPaymentError(nil)
	if pe.Code != "UNKNOWN" {
		t.Errorf("code = %q, want UNKNOWN", pe.Code)
	}
}

func TestEnsureConfigured_NotConfigured(t *testing.T) {
	p := newTestProvider()
	if err := p.ensureConfigured(); err == nil {
		t.Error("expected error for unconfigured provider")
	}
}

func TestEnsureConfigured_Configured(t *testing.T) {
	p := newTestProvider()
	p.apiKey = "key"
	p.storeID = "store"
	if err := p.ensureConfigured(); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Tests: Charge
// ---------------------------------------------------------------------------

func TestCharge_NotConfigured(t *testing.T) {
	p := newTestProvider()
	_, err := p.Charge(context.Background(), processor.PaymentRequest{
		Amount: 1000, Currency: currency.USD,
	})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestCharge_InvalidRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer server.Close()
	p := configuredProvider(server.URL)

	_, err := p.Charge(context.Background(), processor.PaymentRequest{
		Amount: 0, Currency: currency.USD,
	})
	if err == nil {
		t.Fatal("expected error for zero amount")
	}
}

func TestCharge_MissingVariantID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer server.Close()
	p := configuredProvider(server.URL)
	p.defaultVariantID = ""

	_, err := p.Charge(context.Background(), processor.PaymentRequest{
		Amount: 1000, Currency: currency.USD,
	})
	if err == nil {
		t.Fatal("expected error for missing variant ID")
	}
}

func TestCharge_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/checkouts") {
			t.Fatalf("unexpected: %s %s", r.Method, r.URL.Path)
		}

		// Verify auth header.
		if auth := r.Header.Get("Authorization"); auth != "Bearer test-api-key" {
			t.Errorf("Authorization = %q, want Bearer test-api-key", auth)
		}

		w.Header().Set("Content-Type", "application/vnd.api+json")
		resp := jsonAPIResponse{
			Data: json.RawMessage(`{"id":"chk-1","attributes":{"url":"https://checkout.lemonsqueezy.com/123","created_at":"2026-01-15T10:00:00Z"}}`),
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	p := configuredProvider(server.URL)
	result, err := p.Charge(context.Background(), processor.PaymentRequest{
		Amount:      2500,
		Currency:    currency.USD,
		OrderID:     "ord-1",
		CustomerID:  "cust-1",
		Description: "Test checkout",
		Metadata:    map[string]interface{}{"email": "test@example.com"},
		Options:     map[string]interface{}{"redirect_url": "https://example.com/thanks"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatal("expected success")
	}
	if result.ProcessorRef != "chk-1" {
		t.Errorf("ProcessorRef = %q, want chk-1", result.ProcessorRef)
	}
	if result.TransactionID != "ord-1" {
		t.Errorf("TransactionID = %q, want ord-1", result.TransactionID)
	}
	checkoutURL, ok := result.Metadata["checkout_url"].(string)
	if !ok || checkoutURL == "" {
		t.Error("expected checkout_url in metadata")
	}
}

func TestCharge_WithCustomVariantID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body checkoutRequest
		json.NewDecoder(r.Body).Decode(&body)
		if body.Data.Relationships.Variant.Data.ID != "custom-variant" {
			t.Errorf("variant ID = %q, want custom-variant", body.Data.Relationships.Variant.Data.ID)
		}
		w.Header().Set("Content-Type", "application/vnd.api+json")
		json.NewEncoder(w).Encode(jsonAPIResponse{
			Data: json.RawMessage(`{"id":"chk-2","attributes":{"url":"https://checkout.lemonsqueezy.com/456"}}`),
		})
	}))
	defer server.Close()

	p := configuredProvider(server.URL)
	_, err := p.Charge(context.Background(), processor.PaymentRequest{
		Amount:   1000,
		Currency: currency.USD,
		Options:  map[string]interface{}{"variant_id": "custom-variant"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCharge_APIErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		json.NewEncoder(w).Encode(jsonAPIResponse{
			Errors: []apiError{
				{Status: "422", Title: "Validation Error", Detail: "Invalid amount"},
			},
		})
	}))
	defer server.Close()

	p := configuredProvider(server.URL)
	_, err := p.Charge(context.Background(), processor.PaymentRequest{
		Amount: 1000, Currency: currency.USD,
	})
	if err == nil {
		t.Fatal("expected error for API errors in response")
	}
}

func TestCharge_HTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("server error"))
	}))
	defer server.Close()

	p := configuredProvider(server.URL)
	_, err := p.Charge(context.Background(), processor.PaymentRequest{
		Amount: 1000, Currency: currency.USD,
	})
	if err == nil {
		t.Fatal("expected error for 500")
	}
}

func TestCharge_NetworkError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	server.Close()

	p := configuredProvider(server.URL)
	_, err := p.Charge(context.Background(), processor.PaymentRequest{
		Amount: 1000, Currency: currency.USD,
	})
	if err == nil {
		t.Fatal("expected error for network failure")
	}
}

// ---------------------------------------------------------------------------
// Tests: Authorize (not supported)
// ---------------------------------------------------------------------------

func TestAuthorize_NotSupported(t *testing.T) {
	p := newTestProvider()
	p.apiKey = "key"
	p.storeID = "store"

	_, err := p.Authorize(context.Background(), processor.PaymentRequest{
		Amount: 1000, Currency: currency.USD,
	})
	if err == nil {
		t.Fatal("expected error for unsupported operation")
	}
	pe, ok := err.(*processor.PaymentError)
	if !ok {
		t.Fatal("expected PaymentError")
	}
	if pe.Code != "NOT_SUPPORTED" {
		t.Errorf("code = %q, want NOT_SUPPORTED", pe.Code)
	}
}

// ---------------------------------------------------------------------------
// Tests: Capture (not supported)
// ---------------------------------------------------------------------------

func TestCapture_NotSupported(t *testing.T) {
	p := newTestProvider()
	p.apiKey = "key"
	p.storeID = "store"

	_, err := p.Capture(context.Background(), "tx-1", 1000)
	if err == nil {
		t.Fatal("expected error for unsupported operation")
	}
	pe, ok := err.(*processor.PaymentError)
	if !ok {
		t.Fatal("expected PaymentError")
	}
	if pe.Code != "NOT_SUPPORTED" {
		t.Errorf("code = %q, want NOT_SUPPORTED", pe.Code)
	}
}

// ---------------------------------------------------------------------------
// Tests: Refund
// ---------------------------------------------------------------------------

func TestRefund_NotConfigured(t *testing.T) {
	p := newTestProvider()
	_, err := p.Refund(context.Background(), processor.RefundRequest{
		TransactionID: "ord-1", Amount: 500,
	})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestRefund_EmptyTransactionID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer server.Close()
	p := configuredProvider(server.URL)

	_, err := p.Refund(context.Background(), processor.RefundRequest{Amount: 500})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestRefund_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/orders/") || !strings.HasSuffix(r.URL.Path, "/refund") {
			t.Fatalf("unexpected: %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/vnd.api+json")
		json.NewEncoder(w).Encode(jsonAPIResponse{
			Data: json.RawMessage(`{
				"id": "ord-1",
				"attributes": {
					"refunded": true,
					"status": "refunded"
				}
			}`),
		})
	}))
	defer server.Close()

	p := configuredProvider(server.URL)
	result, err := p.Refund(context.Background(), processor.RefundRequest{
		TransactionID: "ord-1",
		Amount:        500,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatal("expected success")
	}
	if result.RefundID != "ord-1" {
		t.Errorf("RefundID = %q, want ord-1", result.RefundID)
	}
}

func TestRefund_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		json.NewEncoder(w).Encode(jsonAPIResponse{
			Errors: []apiError{
				{Status: "404", Title: "Not Found", Detail: "Order not found"},
			},
		})
	}))
	defer server.Close()

	p := configuredProvider(server.URL)
	_, err := p.Refund(context.Background(), processor.RefundRequest{
		TransactionID: "ord-bad",
		Amount:        500,
	})
	if err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// Tests: GetTransaction
// ---------------------------------------------------------------------------

func TestGetTransaction_NotConfigured(t *testing.T) {
	p := newTestProvider()
	_, err := p.GetTransaction(context.Background(), "ord-1")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestGetTransaction_EmptyID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer server.Close()
	p := configuredProvider(server.URL)

	_, err := p.GetTransaction(context.Background(), "")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestGetTransaction_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("expected GET, got %s", r.Method)
		}
		w.Header().Set("Content-Type", "application/vnd.api+json")
		json.NewEncoder(w).Encode(jsonAPIResponse{
			Data: json.RawMessage(`{
				"id": "ord-100",
				"attributes": {
					"store_id": 1,
					"identifier": "IDENT-100",
					"order_number": 12345,
					"currency": "USD",
					"total": 2500,
					"subtotal_usd": 2500,
					"total_usd": 2500,
					"refunded": false,
					"status": "paid",
					"status_formatted": "Paid",
					"created_at": "2026-01-15T10:00:00Z",
					"updated_at": "2026-01-15T10:05:00Z"
				}
			}`),
		})
	}))
	defer server.Close()

	p := configuredProvider(server.URL)
	tx, err := p.GetTransaction(context.Background(), "ord-100")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tx.ID != "ord-100" {
		t.Errorf("ID = %q, want ord-100", tx.ID)
	}
	if tx.ProcessorRef != "IDENT-100" {
		t.Errorf("ProcessorRef = %q, want IDENT-100", tx.ProcessorRef)
	}
	if tx.Type != "charge" {
		t.Errorf("Type = %q, want charge", tx.Type)
	}
	if tx.Status != "completed" {
		t.Errorf("Status = %q, want completed", tx.Status)
	}
	if tx.Amount != 2500 {
		t.Errorf("Amount = %d, want 2500", tx.Amount)
	}
	if tx.CreatedAt == 0 {
		t.Error("CreatedAt should be non-zero")
	}
}

func TestGetTransaction_Refunded(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		json.NewEncoder(w).Encode(jsonAPIResponse{
			Data: json.RawMessage(`{
				"id": "ord-ref",
				"attributes": {
					"identifier": "IDENT-REF",
					"refunded": true,
					"status": "refunded",
					"total": 1000,
					"currency": "EUR",
					"created_at": "2026-01-15T10:00:00Z",
					"updated_at": "2026-01-15T10:00:00Z"
				}
			}`),
		})
	}))
	defer server.Close()

	p := configuredProvider(server.URL)
	tx, err := p.GetTransaction(context.Background(), "ord-ref")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tx.Type != "refund" {
		t.Errorf("Type = %q, want refund", tx.Type)
	}
	if tx.Status != "refunded" {
		t.Errorf("Status = %q, want refunded", tx.Status)
	}
}

func TestGetTransaction_NotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		json.NewEncoder(w).Encode(jsonAPIResponse{
			Errors: []apiError{
				{Status: "404", Title: "Not Found", Detail: "Order not found"},
			},
		})
	}))
	defer server.Close()

	p := configuredProvider(server.URL)
	_, err := p.GetTransaction(context.Background(), "ord-missing")
	if err == nil {
		t.Fatal("expected error for not found")
	}
}

// ---------------------------------------------------------------------------
// Tests: ValidateWebhook
// ---------------------------------------------------------------------------

func TestValidateWebhook_NotConfigured(t *testing.T) {
	p := newTestProvider()
	_, err := p.ValidateWebhook(context.Background(), []byte("{}"), "sig")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestValidateWebhook_NoWebhookSecret(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer server.Close()
	p := configuredProvider(server.URL)
	p.webhookSecret = ""

	_, err := p.ValidateWebhook(context.Background(), []byte("{}"), "sig")
	if err == nil {
		t.Fatal("expected error for missing webhook secret")
	}
}

func TestValidateWebhook_InvalidSignature(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer server.Close()
	p := configuredProvider(server.URL)

	_, err := p.ValidateWebhook(context.Background(), []byte("{}"), "bad-sig")
	if err == nil {
		t.Fatal("expected error for invalid signature")
	}
}

func TestValidateWebhook_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer server.Close()
	p := configuredProvider(server.URL)

	payload, _ := json.Marshal(map[string]interface{}{
		"meta": map[string]interface{}{
			"event_name": "order_created",
			"custom_data": map[string]string{
				"order_id": "ord-1",
			},
		},
		"data": map[string]interface{}{
			"id": "ls-123",
			"attributes": map[string]interface{}{
				"status": "paid",
			},
		},
	})

	// Compute valid signature.
	mac := hmac.New(sha256.New, []byte("webhook-secret"))
	mac.Write(payload)
	validSig := hex.EncodeToString(mac.Sum(nil))

	event, err := p.ValidateWebhook(context.Background(), payload, validSig)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if event.ID != "ls-123" {
		t.Errorf("event ID = %q, want ls-123", event.ID)
	}
	if event.Type != "order_created" {
		t.Errorf("event Type = %q, want order_created", event.Type)
	}
	if event.Processor != processor.LemonSqueezy {
		t.Errorf("Processor = %q, want lemonsqueezy", event.Processor)
	}
	// Check custom data is flattened.
	if v, ok := event.Data["custom_order_id"].(string); !ok || v != "ord-1" {
		t.Errorf("custom_order_id = %v, want ord-1", event.Data["custom_order_id"])
	}
}

func TestValidateWebhook_InvalidPayloadJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer server.Close()
	p := configuredProvider(server.URL)

	payload := []byte("not json")
	mac := hmac.New(sha256.New, []byte("webhook-secret"))
	mac.Write(payload)
	validSig := hex.EncodeToString(mac.Sum(nil))

	_, err := p.ValidateWebhook(context.Background(), payload, validSig)
	if err == nil {
		t.Fatal("expected error for invalid JSON payload")
	}
}

// ---------------------------------------------------------------------------
// Tests: doRequest error handling
// ---------------------------------------------------------------------------

func TestDoRequest_HTTPError_Structured(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		json.NewEncoder(w).Encode(jsonAPIResponse{
			Errors: []apiError{
				{Status: "422", Title: "Unprocessable", Detail: "Bad data"},
			},
		})
	}))
	defer server.Close()

	p := configuredProvider(server.URL)
	_, err := p.doRequest(context.Background(), http.MethodGet, "/test", nil)
	if err == nil {
		t.Fatal("expected error for 422")
	}
	pe, ok := err.(*processor.PaymentError)
	if !ok {
		t.Fatal("expected PaymentError")
	}
	if pe.Code != "422" {
		t.Errorf("code = %q, want 422", pe.Code)
	}
}

func TestDoRequest_HTTPError_Unstructured(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		w.Write([]byte("gateway error"))
	}))
	defer server.Close()

	p := configuredProvider(server.URL)
	_, err := p.doRequest(context.Background(), http.MethodGet, "/test", nil)
	if err == nil {
		t.Fatal("expected error for 502")
	}
	pe, ok := err.(*processor.PaymentError)
	if !ok {
		t.Fatal("expected PaymentError")
	}
	if pe.Code != "HTTP_502" {
		t.Errorf("code = %q, want HTTP_502", pe.Code)
	}
}

func TestDoRequest_GETNoBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		// Verify no Content-Type header for GET without body.
		if ct := r.Header.Get("Content-Type"); ct != "" {
			t.Errorf("Content-Type should be empty for GET, got %q", ct)
		}
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.Write([]byte(`{"data":{}}`))
	}))
	defer server.Close()

	p := configuredProvider(server.URL)
	_, err := p.doRequest(context.Background(), http.MethodGet, "/test", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Tests: Charge - parse error in checkout response
// ---------------------------------------------------------------------------

func TestCharge_ParseCheckoutResponseError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.Write([]byte("not json"))
	}))
	defer server.Close()
	p := configuredProvider(server.URL)

	_, err := p.Charge(context.Background(), processor.PaymentRequest{
		Amount: 1000, Currency: currency.USD,
	})
	if err == nil {
		t.Fatal("expected error for unparseable response")
	}
}

func TestCharge_ParseCheckoutDataError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		json.NewEncoder(w).Encode(jsonAPIResponse{
			Data: json.RawMessage(`"not an object"`),
		})
	}))
	defer server.Close()
	p := configuredProvider(server.URL)

	_, err := p.Charge(context.Background(), processor.PaymentRequest{
		Amount: 1000, Currency: currency.USD,
	})
	if err == nil {
		t.Fatal("expected error for invalid checkout data")
	}
}

// ---------------------------------------------------------------------------
// Tests: Refund - parse error paths
// ---------------------------------------------------------------------------

func TestRefund_ParseResponseError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.Write([]byte("not json"))
	}))
	defer server.Close()
	p := configuredProvider(server.URL)

	_, err := p.Refund(context.Background(), processor.RefundRequest{
		TransactionID: "ord-1", Amount: 500,
	})
	if err == nil {
		t.Fatal("expected error for unparseable response")
	}
}

func TestRefund_ParseOrderDataError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		json.NewEncoder(w).Encode(jsonAPIResponse{
			Data: json.RawMessage(`"not an object"`),
		})
	}))
	defer server.Close()
	p := configuredProvider(server.URL)

	_, err := p.Refund(context.Background(), processor.RefundRequest{
		TransactionID: "ord-1", Amount: 500,
	})
	if err == nil {
		t.Fatal("expected error for invalid order data")
	}
}

func TestRefund_NetworkError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	server.Close()
	p := configuredProvider(server.URL)

	_, err := p.Refund(context.Background(), processor.RefundRequest{
		TransactionID: "ord-1", Amount: 500,
	})
	if err == nil {
		t.Fatal("expected error for network failure")
	}
}

// ---------------------------------------------------------------------------
// Tests: GetTransaction - parse error paths
// ---------------------------------------------------------------------------

func TestGetTransaction_ParseResponseError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		w.Write([]byte("not json"))
	}))
	defer server.Close()
	p := configuredProvider(server.URL)

	_, err := p.GetTransaction(context.Background(), "ord-bad")
	if err == nil {
		t.Fatal("expected error for unparseable response")
	}
}

func TestGetTransaction_ParseOrderDataError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		json.NewEncoder(w).Encode(jsonAPIResponse{
			Data: json.RawMessage(`"not an object"`),
		})
	}))
	defer server.Close()
	p := configuredProvider(server.URL)

	_, err := p.GetTransaction(context.Background(), "ord-bad")
	if err == nil {
		t.Fatal("expected error for invalid order data")
	}
}

func TestGetTransaction_NetworkError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	server.Close()
	p := configuredProvider(server.URL)

	_, err := p.GetTransaction(context.Background(), "ord-bad")
	if err == nil {
		t.Fatal("expected error for network failure")
	}
}

func TestGetTransaction_NonNotFoundError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.api+json")
		json.NewEncoder(w).Encode(jsonAPIResponse{
			Errors: []apiError{
				{Status: "500", Title: "Server Error", Detail: "Internal error"},
			},
		})
	}))
	defer server.Close()
	p := configuredProvider(server.URL)

	_, err := p.GetTransaction(context.Background(), "ord-err")
	if err == nil {
		t.Fatal("expected error for 500 API error")
	}
}

// ---------------------------------------------------------------------------
// Tests: doRequest - network error
// ---------------------------------------------------------------------------

func TestDoRequest_NetworkError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	server.Close()
	p := configuredProvider(server.URL)

	_, err := p.doRequest(context.Background(), http.MethodGet, "/test", nil)
	if err == nil {
		t.Fatal("expected error for network failure")
	}
}
