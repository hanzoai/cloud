package paypal

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/payment/processor"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func newTestProvider() *Provider {
	return &Provider{
		BaseProcessor: processor.NewBaseProcessor(processor.PayPal, supportedCurrencies()),
		httpClient:    &http.Client{Timeout: 5 * time.Second},
	}
}

// configuredProvider returns a provider pointed at the given mock server.
// It pre-injects a valid token so token fetch is skipped for most tests.
func configuredProvider(serverURL string) *Provider {
	p := newTestProvider()
	p.config = Config{
		ClientID:     "test-client-id",
		ClientSecret: "test-secret",
		WebhookID:    "webhook-id-123",
		Sandbox:      true,
	}
	p.accessToken = "mock-token"
	p.tokenExpiry = time.Now().Add(1 * time.Hour)
	// Override the HTTP client to point at our mock server by intercepting requests.
	p.httpClient = &http.Client{
		Timeout:   5 * time.Second,
		Transport: &rewriteTransport{base: http.DefaultTransport, targetURL: serverURL},
	}
	return p
}

// rewriteTransport redirects all HTTP requests to the mock server URL.
type rewriteTransport struct {
	base      http.RoundTripper
	targetURL string
}

func (t *rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Replace the scheme+host with the mock server, keep path and query.
	req.URL.Scheme = "http"
	req.URL.Host = strings.TrimPrefix(t.targetURL, "http://")
	return t.base.RoundTrip(req)
}

// ---------------------------------------------------------------------------
// Tests: Type, IsAvailable, SupportedCurrencies, Configure
// ---------------------------------------------------------------------------

func TestType(t *testing.T) {
	p := newTestProvider()
	if got := p.Type(); got != processor.PayPal {
		t.Errorf("Type() = %q, want %q", got, processor.PayPal)
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
	p.Configure(Config{ClientID: "", ClientSecret: ""})
	if p.IsAvailable(context.Background()) {
		t.Error("IsAvailable() = true with empty credentials, want false")
	}
}

func TestIsAvailable_PartialCredentials(t *testing.T) {
	p := newTestProvider()
	p.Configure(Config{ClientID: "id", ClientSecret: ""})
	if p.IsAvailable(context.Background()) {
		t.Error("IsAvailable() = true with partial credentials, want false")
	}
}

func TestConfigure_Valid(t *testing.T) {
	p := newTestProvider()
	p.Configure(Config{ClientID: "client-id", ClientSecret: "client-secret"})
	if !p.IsAvailable(context.Background()) {
		t.Error("IsAvailable() = false after valid Configure(), want true")
	}
}

func TestConfigure_WithSandbox(t *testing.T) {
	p := newTestProvider()
	p.Configure(Config{
		ClientID:     "client-id",
		ClientSecret: "client-secret",
		Sandbox:      true,
	})
	if !p.IsAvailable(context.Background()) {
		t.Error("IsAvailable() = false after Configure() with sandbox, want true")
	}
}

func TestConfigure_Reconfigure(t *testing.T) {
	p := newTestProvider()
	p.Configure(Config{ClientID: "id1", ClientSecret: "secret1"})
	if !p.IsAvailable(context.Background()) {
		t.Error("expected available after first configure")
	}
	p.Configure(Config{ClientID: "", ClientSecret: ""})
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

	expected := []currency.Type{currency.USD, currency.EUR, currency.GBP, currency.JPY}
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

func TestInterfaceCompliance(t *testing.T) {
	var _ processor.PaymentProcessor = newTestProvider()
}

// ---------------------------------------------------------------------------
// Tests: baseURL
// ---------------------------------------------------------------------------

func TestBaseURL_Sandbox(t *testing.T) {
	p := newTestProvider()
	p.config.Sandbox = true
	if got := p.baseURL(); got != sandboxBaseURL {
		t.Errorf("baseURL() = %q, want %q", got, sandboxBaseURL)
	}
}

func TestBaseURL_Live(t *testing.T) {
	p := newTestProvider()
	p.config.Sandbox = false
	if got := p.baseURL(); got != liveBaseURL {
		t.Errorf("baseURL() = %q, want %q", got, liveBaseURL)
	}
}

// ---------------------------------------------------------------------------
// Tests: checkAvailable
// ---------------------------------------------------------------------------

func TestCheckAvailable_NotConfigured(t *testing.T) {
	p := newTestProvider()
	if err := p.checkAvailable(); err == nil {
		t.Error("expected error for unconfigured provider")
	}
}

func TestCheckAvailable_Configured(t *testing.T) {
	p := newTestProvider()
	p.config = Config{ClientID: "id", ClientSecret: "secret"}
	if err := p.checkAvailable(); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Tests: centsToDecimal / decimalToCents
// ---------------------------------------------------------------------------

func TestCentsToDecimal_USD(t *testing.T) {
	got := centsToDecimal(1099, currency.USD)
	if got != "10.99" {
		t.Errorf("centsToDecimal(1099, USD) = %q, want %q", got, "10.99")
	}
}

func TestCentsToDecimal_JPY(t *testing.T) {
	got := centsToDecimal(500, currency.JPY)
	if got != "500" {
		t.Errorf("centsToDecimal(500, JPY) = %q, want %q", got, "500")
	}
}

func TestDecimalToCents(t *testing.T) {
	got := decimalToCents("10.99", currency.USD)
	if got != 1099 {
		t.Errorf("decimalToCents(10.99, USD) = %d, want 1099", got)
	}
}

// ---------------------------------------------------------------------------
// Tests: captureIDFromOrder / authorizationIDFromOrder
// ---------------------------------------------------------------------------

func TestCaptureIDFromOrder_WithCaptures(t *testing.T) {
	order := &paypalOrder{
		ID: "order-1",
		PurchaseUnits: []paypalPurchaseUnit{
			{
				Payments: &paypalPayments{
					Captures: []paypalCapture{{ID: "cap-1", Status: "COMPLETED"}},
				},
			},
		},
	}
	if got := captureIDFromOrder(order); got != "cap-1" {
		t.Errorf("captureIDFromOrder = %q, want cap-1", got)
	}
}

func TestCaptureIDFromOrder_NoCaptures(t *testing.T) {
	order := &paypalOrder{ID: "order-1"}
	if got := captureIDFromOrder(order); got != "order-1" {
		t.Errorf("captureIDFromOrder = %q, want order-1", got)
	}
}

func TestAuthorizationIDFromOrder_WithAuthorizations(t *testing.T) {
	order := &paypalOrder{
		ID: "order-1",
		PurchaseUnits: []paypalPurchaseUnit{
			{
				Payments: &paypalPayments{
					Authorizations: []paypalAuthorization{{ID: "auth-1", Status: "CREATED"}},
				},
			},
		},
	}
	if got := authorizationIDFromOrder(order); got != "auth-1" {
		t.Errorf("authorizationIDFromOrder = %q, want auth-1", got)
	}
}

func TestAuthorizationIDFromOrder_NoAuthorizations(t *testing.T) {
	order := &paypalOrder{ID: "order-1"}
	if got := authorizationIDFromOrder(order); got != "order-1" {
		t.Errorf("authorizationIDFromOrder = %q, want order-1", got)
	}
}

// ---------------------------------------------------------------------------
// Tests: OAuth2 token management
// ---------------------------------------------------------------------------

func TestGetAccessToken_CachedValid(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("should not have made a request; token is cached")
	}))
	defer server.Close()

	p := configuredProvider(server.URL)
	// Token is pre-set and valid.
	tok, err := p.getAccessToken(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tok != "mock-token" {
		t.Fatalf("expected cached token, got %q", tok)
	}
}

func TestRefreshToken_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != tokenPath {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Fatalf("expected POST, got %s", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"access_token": "new-token-abc",
			"token_type":   "Bearer",
			"expires_in":   3600,
		})
	}))
	defer server.Close()

	p := configuredProvider(server.URL)
	// Expire the cached token.
	p.accessToken = ""
	p.tokenExpiry = time.Time{}

	tok, err := p.refreshToken(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tok != "new-token-abc" {
		t.Fatalf("expected new-token-abc, got %q", tok)
	}
}

func TestRefreshToken_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":"invalid_client"}`))
	}))
	defer server.Close()

	p := configuredProvider(server.URL)
	p.accessToken = ""
	p.tokenExpiry = time.Time{}

	_, err := p.refreshToken(context.Background())
	if err == nil {
		t.Fatal("expected error for 401 token response")
	}
}

func TestRefreshToken_EmptyToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"access_token": "",
			"token_type":   "Bearer",
			"expires_in":   3600,
		})
	}))
	defer server.Close()

	p := configuredProvider(server.URL)
	p.accessToken = ""
	p.tokenExpiry = time.Time{}

	_, err := p.refreshToken(context.Background())
	if err == nil {
		t.Fatal("expected error for empty access token")
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
		t.Fatal("expected error for unconfigured provider")
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

func TestCharge_Success_NewOrder(t *testing.T) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")

		switch {
		case r.URL.Path == ordersPath && r.Method == http.MethodPost && callCount == 1:
			// Create order.
			json.NewEncoder(w).Encode(paypalOrder{
				ID:     "ORDER-123",
				Status: "CREATED",
			})
		case strings.HasSuffix(r.URL.Path, "/capture") && r.Method == http.MethodPost:
			// Capture order.
			json.NewEncoder(w).Encode(paypalOrder{
				ID:     "ORDER-123",
				Status: "COMPLETED",
				PurchaseUnits: []paypalPurchaseUnit{{
					Payments: &paypalPayments{
						Captures: []paypalCapture{{ID: "CAP-456", Status: "COMPLETED"}},
					},
				}},
			})
		default:
			t.Fatalf("unexpected request: %s %s (call #%d)", r.Method, r.URL.Path, callCount)
		}
	}))
	defer server.Close()

	p := configuredProvider(server.URL)
	result, err := p.Charge(context.Background(), processor.PaymentRequest{
		Amount:      2500,
		Currency:    currency.USD,
		OrderID:     "ord-1",
		CustomerID:  "cust-1",
		Description: "Test charge",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error: %s", result.ErrorMessage)
	}
	if result.TransactionID != "ORDER-123" {
		t.Errorf("TransactionID = %q, want ORDER-123", result.TransactionID)
	}
	if result.ProcessorRef != "CAP-456" {
		t.Errorf("ProcessorRef = %q, want CAP-456", result.ProcessorRef)
	}
}

func TestCharge_Success_ExistingToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// With a token, only capture is called.
		if strings.HasSuffix(r.URL.Path, "/capture") {
			json.NewEncoder(w).Encode(paypalOrder{
				ID:     "EXISTING-ORDER",
				Status: "COMPLETED",
			})
		} else {
			t.Fatalf("unexpected: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	p := configuredProvider(server.URL)
	result, err := p.Charge(context.Background(), processor.PaymentRequest{
		Amount:   1000,
		Currency: currency.USD,
		Token:    "EXISTING-ORDER",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatal("expected success")
	}
}

func TestCharge_NotCompleted(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(paypalOrder{
			ID:     "ORDER-PENDING",
			Status: "PENDING",
		})
	}))
	defer server.Close()

	p := configuredProvider(server.URL)
	result, err := p.Charge(context.Background(), processor.PaymentRequest{
		Amount:   1000,
		Currency: currency.USD,
		Token:    "ORDER-PENDING",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Fatal("expected failure for PENDING status")
	}
}

func TestCharge_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"name":    "UNPROCESSABLE_ENTITY",
			"message": "The requested action could not be performed.",
		})
	}))
	defer server.Close()

	p := configuredProvider(server.URL)
	_, err := p.Charge(context.Background(), processor.PaymentRequest{
		Amount: 1000, Currency: currency.USD,
	})
	if err == nil {
		t.Fatal("expected error for API failure")
	}
}

func TestCharge_NetworkError(t *testing.T) {
	// Server that's already closed.
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
// Tests: Authorize
// ---------------------------------------------------------------------------

func TestAuthorize_NotConfigured(t *testing.T) {
	p := newTestProvider()
	_, err := p.Authorize(context.Background(), processor.PaymentRequest{
		Amount: 1000, Currency: currency.USD,
	})
	if err == nil {
		t.Fatal("expected error for unconfigured provider")
	}
}

func TestAuthorize_Success(t *testing.T) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")

		switch {
		case r.URL.Path == ordersPath && callCount == 1:
			json.NewEncoder(w).Encode(paypalOrder{ID: "ORDER-AUTH", Status: "CREATED"})
		case strings.HasSuffix(r.URL.Path, "/authorize"):
			json.NewEncoder(w).Encode(paypalOrder{
				ID:     "ORDER-AUTH",
				Status: "COMPLETED",
				PurchaseUnits: []paypalPurchaseUnit{{
					Payments: &paypalPayments{
						Authorizations: []paypalAuthorization{{ID: "AUTH-789", Status: "CREATED"}},
					},
				}},
			})
		default:
			t.Fatalf("unexpected: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	p := configuredProvider(server.URL)
	result, err := p.Authorize(context.Background(), processor.PaymentRequest{
		Amount: 5000, Currency: currency.USD,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatal("expected success")
	}
	if result.ProcessorRef != "AUTH-789" {
		t.Errorf("ProcessorRef = %q, want AUTH-789", result.ProcessorRef)
	}
}

func TestAuthorize_WithExistingToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(paypalOrder{
			ID:     "TOKEN-ORDER",
			Status: "COMPLETED",
		})
	}))
	defer server.Close()

	p := configuredProvider(server.URL)
	result, err := p.Authorize(context.Background(), processor.PaymentRequest{
		Amount: 1000, Currency: currency.USD, Token: "TOKEN-ORDER",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatal("expected success")
	}
}

func TestAuthorize_NotCompleted(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(paypalOrder{ID: "O-1", Status: "PAYER_ACTION_REQUIRED"})
	}))
	defer server.Close()

	p := configuredProvider(server.URL)
	result, err := p.Authorize(context.Background(), processor.PaymentRequest{
		Amount: 1000, Currency: currency.USD, Token: "O-1",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Fatal("expected failure for non-COMPLETED status")
	}
}

// ---------------------------------------------------------------------------
// Tests: Capture
// ---------------------------------------------------------------------------

func TestCapture_NotConfigured(t *testing.T) {
	p := newTestProvider()
	_, err := p.Capture(context.Background(), "auth-id", 1000)
	if err == nil {
		t.Fatal("expected error for unconfigured provider")
	}
}

func TestCapture_EmptyTransactionID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer server.Close()
	p := configuredProvider(server.URL)

	_, err := p.Capture(context.Background(), "", 1000)
	if err == nil {
		t.Fatal("expected error for empty transaction ID")
	}
}

func TestCapture_Success_WithAmount(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(paypalCapture{
			ID:     "CAP-100",
			Status: "COMPLETED",
		})
	}))
	defer server.Close()

	p := configuredProvider(server.URL)
	result, err := p.Capture(context.Background(), "AUTH-100", 2500)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatal("expected success")
	}
	if result.TransactionID != "CAP-100" {
		t.Errorf("TransactionID = %q, want CAP-100", result.TransactionID)
	}
}

func TestCapture_Success_FullAmount(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(paypalCapture{
			ID:     "CAP-200",
			Status: "COMPLETED",
		})
	}))
	defer server.Close()

	p := configuredProvider(server.URL)
	result, err := p.Capture(context.Background(), "AUTH-200", 0) // 0 = full amount
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.TransactionID != "CAP-200" {
		t.Errorf("TransactionID = %q, want CAP-200", result.TransactionID)
	}
}

func TestCapture_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"name":    "RESOURCE_NOT_FOUND",
			"message": "The specified resource does not exist.",
		})
	}))
	defer server.Close()

	p := configuredProvider(server.URL)
	_, err := p.Capture(context.Background(), "NONEXISTENT", 1000)
	if err == nil {
		t.Fatal("expected error for 404")
	}
}

// ---------------------------------------------------------------------------
// Tests: Refund
// ---------------------------------------------------------------------------

func TestRefund_NotConfigured(t *testing.T) {
	p := newTestProvider()
	_, err := p.Refund(context.Background(), processor.RefundRequest{
		TransactionID: "cap-1", Amount: 1000,
	})
	if err == nil {
		t.Fatal("expected error for unconfigured provider")
	}
}

func TestRefund_EmptyTransactionID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer server.Close()
	p := configuredProvider(server.URL)

	_, err := p.Refund(context.Background(), processor.RefundRequest{
		TransactionID: "", Amount: 1000,
	})
	if err == nil {
		t.Fatal("expected error for empty transaction ID")
	}
}

func TestRefund_Success_PartialAmount(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(paypalRefund{
			ID:     "REF-100",
			Status: "COMPLETED",
		})
	}))
	defer server.Close()

	p := configuredProvider(server.URL)
	result, err := p.Refund(context.Background(), processor.RefundRequest{
		TransactionID: "CAP-100",
		Amount:        500,
		Reason:        "Customer request",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatal("expected success")
	}
	if result.RefundID != "REF-100" {
		t.Errorf("RefundID = %q, want REF-100", result.RefundID)
	}
}

func TestRefund_Success_FullAmount(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(paypalRefund{ID: "REF-200", Status: "COMPLETED"})
	}))
	defer server.Close()

	p := configuredProvider(server.URL)
	result, err := p.Refund(context.Background(), processor.RefundRequest{
		TransactionID: "CAP-200",
		Amount:        0, // full
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatal("expected success")
	}
}

func TestRefund_StatusNotCompleted(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(paypalRefund{ID: "REF-300", Status: "PENDING"})
	}))
	defer server.Close()

	p := configuredProvider(server.URL)
	result, err := p.Refund(context.Background(), processor.RefundRequest{
		TransactionID: "CAP-300",
		Amount:        500,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Fatal("expected failure for PENDING status")
	}
}

func TestRefund_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"name":"CAPTURE_FULLY_REFUNDED","message":"already refunded"}`))
	}))
	defer server.Close()

	p := configuredProvider(server.URL)
	result, err := p.Refund(context.Background(), processor.RefundRequest{
		TransactionID: "CAP-DONE",
		Amount:        500,
	})
	if err == nil {
		t.Fatal("expected error for API failure")
	}
	// Refund returns both a result and error on API failure.
	if result == nil || result.Success {
		t.Fatal("expected non-nil unsuccessful result")
	}
}

// ---------------------------------------------------------------------------
// Tests: GetTransaction
// ---------------------------------------------------------------------------

func TestGetTransaction_NotConfigured(t *testing.T) {
	p := newTestProvider()
	_, err := p.GetTransaction(context.Background(), "ORDER-1")
	if err == nil {
		t.Fatal("expected error for unconfigured provider")
	}
}

func TestGetTransaction_EmptyID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer server.Close()
	p := configuredProvider(server.URL)

	_, err := p.GetTransaction(context.Background(), "")
	if err == nil {
		t.Fatal("expected error for empty transaction ID")
	}
}

func TestGetTransaction_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(paypalOrder{
			ID:     "ORDER-500",
			Intent: "CAPTURE",
			Status: "COMPLETED",
			PurchaseUnits: []paypalPurchaseUnit{{
				Amount: paypalAmount{CurrencyCode: "USD", Value: "25.00"},
			}},
			CreateTime: "2026-01-15T10:00:00Z",
			UpdateTime: "2026-01-15T10:05:00Z",
		})
	}))
	defer server.Close()

	p := configuredProvider(server.URL)
	tx, err := p.GetTransaction(context.Background(), "ORDER-500")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tx.ID != "ORDER-500" {
		t.Errorf("ID = %q, want ORDER-500", tx.ID)
	}
	if tx.Status != "COMPLETED" {
		t.Errorf("Status = %q, want COMPLETED", tx.Status)
	}
	if tx.Type != "charge" {
		t.Errorf("Type = %q, want charge", tx.Type)
	}
	if tx.Currency != currency.USD {
		t.Errorf("Currency = %q, want usd", tx.Currency)
	}
	if tx.CreatedAt == 0 {
		t.Error("CreatedAt should be non-zero")
	}
}

func TestGetTransaction_AuthorizeIntent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(paypalOrder{
			ID:     "ORDER-600",
			Intent: "AUTHORIZE",
			Status: "COMPLETED",
		})
	}))
	defer server.Close()

	p := configuredProvider(server.URL)
	tx, err := p.GetTransaction(context.Background(), "ORDER-600")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tx.Type != "authorize" {
		t.Errorf("Type = %q, want authorize", tx.Type)
	}
}

func TestGetTransaction_NoPurchaseUnits(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(paypalOrder{
			ID:     "ORDER-700",
			Status: "CREATED",
		})
	}))
	defer server.Close()

	p := configuredProvider(server.URL)
	tx, err := p.GetTransaction(context.Background(), "ORDER-700")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// No purchase units means zero amount, empty currency.
	if tx.Amount != 0 {
		t.Errorf("Amount = %d, want 0", tx.Amount)
	}
}

// ---------------------------------------------------------------------------
// Tests: ValidateWebhook
// ---------------------------------------------------------------------------

func TestValidateWebhook_NotConfigured(t *testing.T) {
	p := newTestProvider()
	_, err := p.ValidateWebhook(context.Background(), []byte("{}"), "{}")
	if err == nil {
		t.Fatal("expected error for unconfigured provider")
	}
}

func TestValidateWebhook_NoWebhookID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer server.Close()

	p := configuredProvider(server.URL)
	p.config.WebhookID = ""

	_, err := p.ValidateWebhook(context.Background(), []byte("{}"), "{}")
	if err == nil {
		t.Fatal("expected error for missing webhook ID")
	}
}

func TestValidateWebhook_InvalidSignatureJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer server.Close()

	p := configuredProvider(server.URL)
	_, err := p.ValidateWebhook(context.Background(), []byte("{}"), "not-json")
	if err == nil {
		t.Fatal("expected error for invalid signature JSON")
	}
}

func TestValidateWebhook_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == webhookVerify {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{
				"verification_status": "SUCCESS",
			})
			return
		}
		t.Fatalf("unexpected: %s %s", r.Method, r.URL.Path)
	}))
	defer server.Close()

	p := configuredProvider(server.URL)

	sig, _ := json.Marshal(map[string]string{
		"transmission_id":   "t-1",
		"transmission_time": "2026-01-15T10:00:00Z",
		"cert_url":          "https://example.com/cert",
		"auth_algo":         "SHA256withRSA",
		"transmission_sig":  "abc123",
	})

	payload, _ := json.Marshal(map[string]interface{}{
		"id":         "WH-EVT-1",
		"event_type": "PAYMENT.CAPTURE.COMPLETED",
		"resource":   map[string]string{"id": "CAP-1"},
		"create_time": "2026-01-15T10:00:00Z",
	})

	event, err := p.ValidateWebhook(context.Background(), payload, string(sig))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if event.ID != "WH-EVT-1" {
		t.Errorf("event ID = %q, want WH-EVT-1", event.ID)
	}
	if event.Type != "PAYMENT.CAPTURE.COMPLETED" {
		t.Errorf("event Type = %q, want PAYMENT.CAPTURE.COMPLETED", event.Type)
	}
	if event.Processor != processor.PayPal {
		t.Errorf("Processor = %q, want paypal", event.Processor)
	}
}

func TestValidateWebhook_VerificationFailed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"verification_status": "FAILURE",
		})
	}))
	defer server.Close()

	p := configuredProvider(server.URL)

	sig, _ := json.Marshal(map[string]string{
		"transmission_id":   "t-1",
		"transmission_time": "2026-01-15T10:00:00Z",
		"cert_url":          "https://example.com/cert",
		"auth_algo":         "SHA256withRSA",
		"transmission_sig":  "bad-sig",
	})

	_, err := p.ValidateWebhook(context.Background(), []byte(`{"id":"e1","event_type":"test"}`), string(sig))
	if err == nil {
		t.Fatal("expected error for verification FAILURE")
	}
}

// ---------------------------------------------------------------------------
// Tests: doRequest error paths
// ---------------------------------------------------------------------------

func TestDoRequest_NonPaypalAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal error"))
	}))
	defer server.Close()

	p := configuredProvider(server.URL)
	_, err := p.doRequest(context.Background(), http.MethodGet, "/test", nil)
	if err == nil {
		t.Fatal("expected error for 500")
	}
	if pe, ok := err.(*processor.PaymentError); ok {
		if pe.Code != "API_ERROR" {
			t.Errorf("error code = %q, want API_ERROR", pe.Code)
		}
	}
}

func TestDoRequest_PaypalNamedError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(paypalAPIError{
			Name:    "INVALID_REQUEST",
			Message: "Request is not well-formed.",
		})
	}))
	defer server.Close()

	p := configuredProvider(server.URL)
	_, err := p.doRequest(context.Background(), http.MethodPost, "/test", []byte("{}"))
	if err == nil {
		t.Fatal("expected error")
	}
	if pe, ok := err.(*processor.PaymentError); ok {
		if pe.Code != "INVALID_REQUEST" {
			t.Errorf("error code = %q, want INVALID_REQUEST", pe.Code)
		}
	}
}

// ---------------------------------------------------------------------------
// Tests: payErr helper
// ---------------------------------------------------------------------------

func TestPayErr(t *testing.T) {
	p := newTestProvider()
	err := p.payErr("CODE", "message", fmt.Errorf("inner"))
	if err == nil {
		t.Fatal("expected non-nil error")
	}
	if err.Code != "CODE" {
		t.Errorf("code = %q, want CODE", err.Code)
	}
	if err.Processor != processor.PayPal {
		t.Errorf("processor = %q, want paypal", err.Processor)
	}
}

// ---------------------------------------------------------------------------
// Tests: Authorize error paths (coverage gaps)
// ---------------------------------------------------------------------------

func TestAuthorize_InvalidRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer server.Close()
	p := configuredProvider(server.URL)

	_, err := p.Authorize(context.Background(), processor.PaymentRequest{
		Amount: 0, Currency: currency.USD,
	})
	if err == nil {
		t.Fatal("expected error for zero amount")
	}
}

func TestAuthorize_CreateOrderError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"name":"INVALID_REQUEST","message":"bad"}`))
	}))
	defer server.Close()
	p := configuredProvider(server.URL)

	_, err := p.Authorize(context.Background(), processor.PaymentRequest{
		Amount: 1000, Currency: currency.USD,
		// No Token => calls createOrder which will fail.
	})
	if err == nil {
		t.Fatal("expected error from createOrder failure")
	}
}

func TestAuthorize_AuthorizeOrderError(t *testing.T) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		if callCount == 1 {
			// Return authorize error.
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"name":"ORDER_NOT_APPROVED","message":"buyer has not approved"}`))
		}
	}))
	defer server.Close()
	p := configuredProvider(server.URL)

	_, err := p.Authorize(context.Background(), processor.PaymentRequest{
		Amount: 1000, Currency: currency.USD,
		Token: "ORDER-UNAPPROVED",
	})
	if err == nil {
		t.Fatal("expected error from authorizeOrder failure")
	}
}

// ---------------------------------------------------------------------------
// Tests: Capture error path - captureOrder parse error
// ---------------------------------------------------------------------------

func TestCapture_InvalidResponseJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("not json"))
	}))
	defer server.Close()
	p := configuredProvider(server.URL)

	_, err := p.Capture(context.Background(), "AUTH-100", 1000)
	if err == nil {
		t.Fatal("expected error for invalid JSON response")
	}
}

// ---------------------------------------------------------------------------
// Tests: Refund error path - parse error
// ---------------------------------------------------------------------------

func TestRefund_InvalidResponseJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("not json"))
	}))
	defer server.Close()
	p := configuredProvider(server.URL)

	_, err := p.Refund(context.Background(), processor.RefundRequest{
		TransactionID: "CAP-100", Amount: 500,
	})
	if err == nil {
		t.Fatal("expected error for invalid JSON response")
	}
}

// ---------------------------------------------------------------------------
// Tests: GetTransaction - parse error, API error
// ---------------------------------------------------------------------------

func TestGetTransaction_InvalidResponseJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("not json"))
	}))
	defer server.Close()
	p := configuredProvider(server.URL)

	_, err := p.GetTransaction(context.Background(), "ORDER-BAD")
	if err == nil {
		t.Fatal("expected error for invalid JSON response")
	}
}

func TestGetTransaction_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"name":"RESOURCE_NOT_FOUND","message":"not found"}`))
	}))
	defer server.Close()
	p := configuredProvider(server.URL)

	_, err := p.GetTransaction(context.Background(), "ORDER-MISSING")
	if err == nil {
		t.Fatal("expected error for 404")
	}
}

// ---------------------------------------------------------------------------
// Tests: ValidateWebhook - parse errors
// ---------------------------------------------------------------------------

func TestValidateWebhook_InvalidPayloadJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer server.Close()
	p := configuredProvider(server.URL)

	sig, _ := json.Marshal(map[string]string{
		"transmission_id":   "t-1",
		"transmission_time": "2026-01-15T10:00:00Z",
		"cert_url":          "https://example.com/cert",
		"auth_algo":         "SHA256withRSA",
		"transmission_sig":  "abc",
	})
	_, err := p.ValidateWebhook(context.Background(), []byte("not-json"), string(sig))
	if err == nil {
		t.Fatal("expected error for invalid payload JSON")
	}
}

func TestValidateWebhook_VerifyAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal error"))
	}))
	defer server.Close()
	p := configuredProvider(server.URL)

	sig, _ := json.Marshal(map[string]string{
		"transmission_id":   "t-1",
		"transmission_time": "2026-01-15T10:00:00Z",
		"cert_url":          "https://example.com/cert",
		"auth_algo":         "SHA256withRSA",
		"transmission_sig":  "abc",
	})
	_, err := p.ValidateWebhook(context.Background(), []byte(`{"id":"e1","event_type":"test"}`), string(sig))
	if err == nil {
		t.Fatal("expected error for verify API error")
	}
}

func TestValidateWebhook_VerifyResponseParseError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("not json"))
	}))
	defer server.Close()
	p := configuredProvider(server.URL)

	sig, _ := json.Marshal(map[string]string{
		"transmission_id":   "t-1",
		"transmission_time": "2026-01-15T10:00:00Z",
		"cert_url":          "https://example.com/cert",
		"auth_algo":         "SHA256withRSA",
		"transmission_sig":  "abc",
	})
	_, err := p.ValidateWebhook(context.Background(), []byte(`{"id":"e1","event_type":"test"}`), string(sig))
	if err == nil {
		t.Fatal("expected error for unparseable verification response")
	}
}

// ---------------------------------------------------------------------------
// Tests: getAccessToken - expired token triggers refresh
// ---------------------------------------------------------------------------

func TestGetAccessToken_ExpiredTriggersRefresh(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == tokenPath {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"access_token": "refreshed-token",
				"token_type":   "Bearer",
				"expires_in":   3600,
			})
			return
		}
		t.Fatalf("unexpected: %s %s", r.Method, r.URL.Path)
	}))
	defer server.Close()

	p := configuredProvider(server.URL)
	// Set token as expired.
	p.accessToken = "old-token"
	p.tokenExpiry = time.Now().Add(-10 * time.Second) // expired

	tok, err := p.getAccessToken(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tok != "refreshed-token" {
		t.Errorf("token = %q, want refreshed-token", tok)
	}
}

// ---------------------------------------------------------------------------
// Tests: doRequest - token refresh failure
// ---------------------------------------------------------------------------

func TestDoRequest_TokenRefreshFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		w.Write([]byte(`{"error":"invalid_client"}`))
	}))
	defer server.Close()

	p := configuredProvider(server.URL)
	p.accessToken = ""
	p.tokenExpiry = time.Time{}

	_, err := p.doRequest(context.Background(), http.MethodGet, "/test", nil)
	if err == nil {
		t.Fatal("expected error when token refresh fails")
	}
}

// ---------------------------------------------------------------------------
// Tests: createOrder - marshal error path (covered by body builder)
// ---------------------------------------------------------------------------

func TestCreateOrder_ParseError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("not json"))
	}))
	defer server.Close()
	p := configuredProvider(server.URL)

	_, err := p.createOrder(context.Background(), processor.PaymentRequest{
		Amount: 1000, Currency: currency.USD,
	}, "CAPTURE")
	if err == nil {
		t.Fatal("expected error for unparseable createOrder response")
	}
}

// ---------------------------------------------------------------------------
// Tests: captureOrder / authorizeOrder error paths
// ---------------------------------------------------------------------------

func TestCaptureOrder_ParseError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("not json"))
	}))
	defer server.Close()
	p := configuredProvider(server.URL)

	_, err := p.captureOrder(context.Background(), "ORDER-BAD")
	if err == nil {
		t.Fatal("expected error for unparseable captureOrder response")
	}
}

func TestAuthorizeOrder_ParseError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("not json"))
	}))
	defer server.Close()
	p := configuredProvider(server.URL)

	_, err := p.authorizeOrder(context.Background(), "ORDER-BAD")
	if err == nil {
		t.Fatal("expected error for unparseable authorizeOrder response")
	}
}

func TestCaptureOrder_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"name":"ORDER_NOT_APPROVED","message":"buyer did not approve"}`))
	}))
	defer server.Close()
	p := configuredProvider(server.URL)

	_, err := p.captureOrder(context.Background(), "ORDER-UNAPPROVED")
	if err == nil {
		t.Fatal("expected error for API error")
	}
}

func TestAuthorizeOrder_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"name":"ORDER_NOT_APPROVED","message":"buyer did not approve"}`))
	}))
	defer server.Close()
	p := configuredProvider(server.URL)

	_, err := p.authorizeOrder(context.Background(), "ORDER-UNAPPROVED")
	if err == nil {
		t.Fatal("expected error for API error")
	}
}

// ---------------------------------------------------------------------------
// Tests: Charge - createOrder error path (no token)
// ---------------------------------------------------------------------------

func TestCharge_CreateOrderAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(paypalAPIError{
			Name:    "INVALID_REQUEST",
			Message: "Missing required field",
		})
	}))
	defer server.Close()
	p := configuredProvider(server.URL)

	_, err := p.Charge(context.Background(), processor.PaymentRequest{
		Amount: 1000, Currency: currency.USD,
		// No Token => createOrder called, which fails.
	})
	if err == nil {
		t.Fatal("expected error from createOrder failure")
	}
}

func TestCharge_CaptureOrderError(t *testing.T) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		if callCount == 1 && r.URL.Path == ordersPath {
			// createOrder succeeds.
			json.NewEncoder(w).Encode(paypalOrder{ID: "ORDER-OK", Status: "CREATED"})
		} else {
			// captureOrder fails.
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(paypalAPIError{Name: "ORDER_NOT_APPROVED", Message: "not approved"})
		}
	}))
	defer server.Close()
	p := configuredProvider(server.URL)

	_, err := p.Charge(context.Background(), processor.PaymentRequest{
		Amount: 1000, Currency: currency.USD,
		// No Token => createOrder then captureOrder.
	})
	if err == nil {
		t.Fatal("expected error from captureOrder failure")
	}
}
