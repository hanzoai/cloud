package braintree

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
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
		BaseProcessor: processor.NewBaseProcessor(processor.Braintree, supportedCurrencies()),
	}
}

func configuredProvider(serverURL string) *Provider {
	p := &Provider{
		BaseProcessor: processor.NewBaseProcessor(processor.Braintree, supportedCurrencies()),
		config: Config{
			PublicKey:   "pub-key",
			PrivateKey:  "priv-key",
			MerchantID:  "merchant-id",
			Environment: "sandbox",
		},
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

func graphqlResp(data map[string]interface{}) map[string]interface{} {
	return map[string]interface{}{"data": data}
}

func graphqlError(msg string) map[string]interface{} {
	return map[string]interface{}{
		"errors": []interface{}{
			map[string]interface{}{"message": msg},
		},
	}
}

// ---------------------------------------------------------------------------
// Tests: Type, IsAvailable, SupportedCurrencies, Configure
// ---------------------------------------------------------------------------

func TestType(t *testing.T) {
	p := newTestProvider()
	if got := p.Type(); got != processor.Braintree {
		t.Errorf("Type() = %q, want %q", got, processor.Braintree)
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
	p.Configure(Config{PublicKey: "", PrivateKey: "", MerchantID: ""})
	if p.IsAvailable(context.Background()) {
		t.Error("IsAvailable() = true with empty credentials, want false")
	}
}

func TestIsAvailable_PartialCredentials_MissingMerchant(t *testing.T) {
	p := newTestProvider()
	p.Configure(Config{PublicKey: "pub", PrivateKey: "priv", MerchantID: ""})
	if p.IsAvailable(context.Background()) {
		t.Error("IsAvailable() = true with missing merchant ID, want false")
	}
}

func TestIsAvailable_PartialCredentials_MissingPrivateKey(t *testing.T) {
	p := newTestProvider()
	p.Configure(Config{PublicKey: "pub", PrivateKey: "", MerchantID: "merch"})
	if p.IsAvailable(context.Background()) {
		t.Error("IsAvailable() = true with missing private key, want false")
	}
}

func TestConfigure_Valid(t *testing.T) {
	p := newTestProvider()
	p.Configure(Config{
		PublicKey:   "pub-key",
		PrivateKey:  "priv-key",
		MerchantID:  "merchant-id",
		Environment: "sandbox",
	})
	if !p.IsAvailable(context.Background()) {
		t.Error("IsAvailable() = false after valid Configure(), want true")
	}
}

func TestConfigure_Production(t *testing.T) {
	p := newTestProvider()
	p.Configure(Config{
		PublicKey:   "pub-key",
		PrivateKey:  "priv-key",
		MerchantID:  "merchant-id",
		Environment: "production",
	})
	if !p.IsAvailable(context.Background()) {
		t.Error("IsAvailable() = false after production Configure(), want true")
	}
}

func TestConfigure_Reconfigure(t *testing.T) {
	p := newTestProvider()
	p.Configure(Config{PublicKey: "p", PrivateKey: "p", MerchantID: "m"})
	if !p.IsAvailable(context.Background()) {
		t.Error("expected available after first configure")
	}
	p.Configure(Config{PublicKey: "", PrivateKey: "", MerchantID: ""})
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

	expected := []currency.Type{currency.USD, currency.EUR, currency.GBP, currency.CAD, currency.AUD}
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
// Tests: Helper functions
// ---------------------------------------------------------------------------

func TestGraphqlEndpoint_Sandbox(t *testing.T) {
	p := newTestProvider()
	p.config.Environment = "sandbox"
	if got := p.graphqlEndpoint(); got != sandboxURL {
		t.Errorf("graphqlEndpoint() = %q, want %q", got, sandboxURL)
	}
}

func TestGraphqlEndpoint_Production(t *testing.T) {
	p := newTestProvider()
	p.config.Environment = "production"
	if got := p.graphqlEndpoint(); got != productionURL {
		t.Errorf("graphqlEndpoint() = %q, want %q", got, productionURL)
	}
}

func TestAuthHeader(t *testing.T) {
	p := newTestProvider()
	p.config.PublicKey = "pub"
	p.config.PrivateKey = "priv"
	expected := "Basic " + base64.StdEncoding.EncodeToString([]byte("pub:priv"))
	if got := p.authHeader(); got != expected {
		t.Errorf("authHeader() = %q, want %q", got, expected)
	}
}

func TestCentsToDecimal_USD(t *testing.T) {
	got := centsToDecimal(1099, currency.USD)
	if got != "10.99" {
		t.Errorf("centsToDecimal(1099, USD) = %q, want 10.99", got)
	}
}

func TestCentsToDecimal_JPY(t *testing.T) {
	got := centsToDecimal(500, currency.JPY)
	if got != "500" {
		t.Errorf("centsToDecimal(500, JPY) = %q, want 500", got)
	}
}

func TestMapStatus(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"SUBMITTED_FOR_SETTLEMENT", "pending"},
		{"SETTLING", "pending"},
		{"SETTLED", "completed"},
		{"AUTHORIZED", "authorized"},
		{"VOIDED", "voided"},
		{"PROCESSOR_DECLINED", "declined"},
		{"GATEWAY_REJECTED", "declined"},
		{"FAILED", "failed"},
		{"AUTHORIZATION_EXPIRED", "failed"},
		{"SETTLEMENT_DECLINED", "failed"},
		{"REFUNDED", "refunded"},
		{"SOME_NEW_STATUS", "some_new_status"},
	}
	for _, tt := range tests {
		if got := mapStatus(tt.input); got != tt.want {
			t.Errorf("mapStatus(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestExtractGraphQLError_NoErrors(t *testing.T) {
	resp := map[string]interface{}{"data": map[string]interface{}{}}
	if got := extractGraphQLError(resp); got != "" {
		t.Errorf("expected empty string, got %q", got)
	}
}

func TestExtractGraphQLError_WithErrors(t *testing.T) {
	resp := map[string]interface{}{
		"errors": []interface{}{
			map[string]interface{}{"message": "error one"},
			map[string]interface{}{"message": "error two"},
		},
	}
	got := extractGraphQLError(resp)
	if !strings.Contains(got, "error one") || !strings.Contains(got, "error two") {
		t.Errorf("expected both error messages, got %q", got)
	}
}

func TestExtractGraphQLError_ErrorsNotSlice(t *testing.T) {
	resp := map[string]interface{}{"errors": "not a slice"}
	if got := extractGraphQLError(resp); got != "" {
		t.Errorf("expected empty for non-slice errors, got %q", got)
	}
}

func TestExtractGraphQLError_EmptyErrors(t *testing.T) {
	resp := map[string]interface{}{"errors": []interface{}{}}
	if got := extractGraphQLError(resp); got != "" {
		t.Errorf("expected empty for empty errors, got %q", got)
	}
}

func TestExtractGraphQLError_NoMessage(t *testing.T) {
	resp := map[string]interface{}{
		"errors": []interface{}{
			map[string]interface{}{"code": "123"},
		},
	}
	if got := extractGraphQLError(resp); got != "unknown graphql error" {
		t.Errorf("expected 'unknown graphql error', got %q", got)
	}
}

func TestExtractTransaction_ValidData(t *testing.T) {
	resp := map[string]interface{}{
		"data": map[string]interface{}{
			"chargePaymentMethod": map[string]interface{}{
				"transaction": map[string]interface{}{
					"id":     "tx-1",
					"status": "SETTLED",
				},
			},
		},
	}
	tx := extractTransaction(resp, "chargePaymentMethod")
	if tx == nil {
		t.Fatal("expected non-nil transaction")
	}
	if tx["id"] != "tx-1" {
		t.Errorf("id = %v, want tx-1", tx["id"])
	}
}

func TestExtractTransaction_MissingMutation(t *testing.T) {
	resp := map[string]interface{}{
		"data": map[string]interface{}{},
	}
	tx := extractTransaction(resp, "chargePaymentMethod")
	if tx != nil {
		t.Error("expected nil for missing mutation")
	}
}

func TestExtractTransaction_NoData(t *testing.T) {
	resp := map[string]interface{}{}
	tx := extractTransaction(resp, "chargePaymentMethod")
	if tx != nil {
		t.Error("expected nil for missing data")
	}
}

func TestMerchantAccountID_FromOptions(t *testing.T) {
	p := newTestProvider()
	p.config.MerchantID = "default-merchant"
	req := processor.PaymentRequest{
		Options: map[string]interface{}{"merchantAccountId": "custom-merchant"},
	}
	if got := p.merchantAccountID(req); got != "custom-merchant" {
		t.Errorf("merchantAccountID = %q, want custom-merchant", got)
	}
}

func TestMerchantAccountID_Default(t *testing.T) {
	p := newTestProvider()
	p.config.MerchantID = "default-merchant"
	req := processor.PaymentRequest{}
	if got := p.merchantAccountID(req); got != "default-merchant" {
		t.Errorf("merchantAccountID = %q, want default-merchant", got)
	}
}

// ---------------------------------------------------------------------------
// Tests: Charge
// ---------------------------------------------------------------------------

func TestCharge_NotConfigured(t *testing.T) {
	p := newTestProvider()
	_, err := p.Charge(context.Background(), processor.PaymentRequest{
		Amount: 1000, Currency: currency.USD, Token: "tok",
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

func TestCharge_UnsupportedCurrency(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer server.Close()
	p := configuredProvider(server.URL)

	_, err := p.Charge(context.Background(), processor.PaymentRequest{
		Amount: 1000, Currency: currency.Type("zzz"), Token: "tok",
	})
	if err == nil {
		t.Fatal("expected error for unsupported currency")
	}
}

func TestCharge_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(graphqlResp(map[string]interface{}{
			"chargePaymentMethod": map[string]interface{}{
				"transaction": map[string]interface{}{
					"id":     "bt-tx-1",
					"status": "SUBMITTED_FOR_SETTLEMENT",
					"amount": map[string]interface{}{
						"value":        "10.00",
						"currencyCode": "USD",
					},
				},
			},
		}))
	}))
	defer server.Close()

	p := configuredProvider(server.URL)
	result, err := p.Charge(context.Background(), processor.PaymentRequest{
		Amount:      1000,
		Currency:    currency.USD,
		Token:       "tok-1",
		OrderID:     "ord-1",
		CustomerID:  "cust-1",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatal("expected success")
	}
	if result.TransactionID != "bt-tx-1" {
		t.Errorf("TransactionID = %q, want bt-tx-1", result.TransactionID)
	}
	if result.Status != "pending" {
		t.Errorf("Status = %q, want pending", result.Status)
	}
}

func TestCharge_GraphQLError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(graphqlError("Invalid payment method"))
	}))
	defer server.Close()

	p := configuredProvider(server.URL)
	result, err := p.Charge(context.Background(), processor.PaymentRequest{
		Amount: 1000, Currency: currency.USD, Token: "bad-tok",
	})
	if err == nil {
		t.Fatal("expected error for GraphQL error")
	}
	if result == nil || result.Success {
		t.Fatal("expected non-nil unsuccessful result")
	}
}

func TestCharge_NoTransaction(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(graphqlResp(map[string]interface{}{
			"chargePaymentMethod": map[string]interface{}{},
		}))
	}))
	defer server.Close()

	p := configuredProvider(server.URL)
	result, err := p.Charge(context.Background(), processor.PaymentRequest{
		Amount: 1000, Currency: currency.USD, Token: "tok",
	})
	if err == nil {
		t.Fatal("expected error for missing transaction")
	}
	if result == nil || result.Success {
		t.Fatal("expected unsuccessful result")
	}
}

func TestCharge_NetworkError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	server.Close()

	p := configuredProvider(server.URL)
	_, err := p.Charge(context.Background(), processor.PaymentRequest{
		Amount: 1000, Currency: currency.USD, Token: "tok",
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
		Amount: 1000, Currency: currency.USD, Token: "tok",
	})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestAuthorize_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(graphqlResp(map[string]interface{}{
			"authorizePaymentMethod": map[string]interface{}{
				"transaction": map[string]interface{}{
					"id":     "bt-auth-1",
					"status": "AUTHORIZED",
				},
			},
		}))
	}))
	defer server.Close()

	p := configuredProvider(server.URL)
	result, err := p.Authorize(context.Background(), processor.PaymentRequest{
		Amount: 2000, Currency: currency.USD, Token: "tok",
		OrderID: "ord-1", CustomerID: "cust-1",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatal("expected success")
	}
	if result.Status != "authorized" {
		t.Errorf("Status = %q, want authorized", result.Status)
	}
}

func TestAuthorize_GraphQLError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(graphqlError("Authorization failed"))
	}))
	defer server.Close()

	p := configuredProvider(server.URL)
	_, err := p.Authorize(context.Background(), processor.PaymentRequest{
		Amount: 1000, Currency: currency.USD, Token: "tok",
	})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestAuthorize_NoTransaction(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(graphqlResp(map[string]interface{}{
			"authorizePaymentMethod": map[string]interface{}{},
		}))
	}))
	defer server.Close()

	p := configuredProvider(server.URL)
	_, err := p.Authorize(context.Background(), processor.PaymentRequest{
		Amount: 1000, Currency: currency.USD, Token: "tok",
	})
	if err == nil {
		t.Fatal("expected error for missing transaction")
	}
}

// ---------------------------------------------------------------------------
// Tests: Capture
// ---------------------------------------------------------------------------

func TestCapture_NotConfigured(t *testing.T) {
	p := newTestProvider()
	_, err := p.Capture(context.Background(), "tx-1", 1000)
	if err == nil {
		t.Fatal("expected error")
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

func TestCapture_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(graphqlResp(map[string]interface{}{
			"captureTransaction": map[string]interface{}{
				"transaction": map[string]interface{}{
					"id":     "bt-cap-1",
					"status": "SUBMITTED_FOR_SETTLEMENT",
				},
			},
		}))
	}))
	defer server.Close()

	p := configuredProvider(server.URL)
	result, err := p.Capture(context.Background(), "bt-auth-1", 1500)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatal("expected success")
	}
}

func TestCapture_FullAmount(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(graphqlResp(map[string]interface{}{
			"captureTransaction": map[string]interface{}{
				"transaction": map[string]interface{}{
					"id":     "bt-cap-2",
					"status": "SETTLED",
				},
			},
		}))
	}))
	defer server.Close()

	p := configuredProvider(server.URL)
	result, err := p.Capture(context.Background(), "bt-auth-2", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != "completed" {
		t.Errorf("Status = %q, want completed", result.Status)
	}
}

func TestCapture_GraphQLError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(graphqlError("Transaction not found"))
	}))
	defer server.Close()

	p := configuredProvider(server.URL)
	_, err := p.Capture(context.Background(), "bad-tx", 1000)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestCapture_NoTransaction(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(graphqlResp(map[string]interface{}{
			"captureTransaction": map[string]interface{}{},
		}))
	}))
	defer server.Close()

	p := configuredProvider(server.URL)
	_, err := p.Capture(context.Background(), "tx-1", 1000)
	if err == nil {
		t.Fatal("expected error for missing transaction")
	}
}

// ---------------------------------------------------------------------------
// Tests: Refund
// ---------------------------------------------------------------------------

func TestRefund_NotConfigured(t *testing.T) {
	p := newTestProvider()
	_, err := p.Refund(context.Background(), processor.RefundRequest{
		TransactionID: "tx-1", Amount: 500,
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
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(graphqlResp(map[string]interface{}{
			"refundTransaction": map[string]interface{}{
				"refund": map[string]interface{}{
					"id":     "bt-ref-1",
					"status": "SETTLED",
					"amount": map[string]interface{}{"value": "5.00"},
				},
			},
		}))
	}))
	defer server.Close()

	p := configuredProvider(server.URL)
	result, err := p.Refund(context.Background(), processor.RefundRequest{
		TransactionID: "bt-tx-1",
		Amount:        500,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatal("expected success")
	}
	if result.RefundID != "bt-ref-1" {
		t.Errorf("RefundID = %q, want bt-ref-1", result.RefundID)
	}
}

func TestRefund_FullRefund(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(graphqlResp(map[string]interface{}{
			"refundTransaction": map[string]interface{}{
				"refund": map[string]interface{}{
					"id":     "bt-ref-2",
					"status": "SETTLED",
				},
			},
		}))
	}))
	defer server.Close()

	p := configuredProvider(server.URL)
	result, err := p.Refund(context.Background(), processor.RefundRequest{
		TransactionID: "bt-tx-2",
		Amount:        0, // full
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatal("expected success")
	}
}

func TestRefund_GraphQLError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(graphqlError("Cannot refund"))
	}))
	defer server.Close()

	p := configuredProvider(server.URL)
	_, err := p.Refund(context.Background(), processor.RefundRequest{
		TransactionID: "bt-tx-1", Amount: 500,
	})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestRefund_NoRefund(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(graphqlResp(map[string]interface{}{
			"refundTransaction": map[string]interface{}{},
		}))
	}))
	defer server.Close()

	p := configuredProvider(server.URL)
	_, err := p.Refund(context.Background(), processor.RefundRequest{
		TransactionID: "bt-tx-1", Amount: 500,
	})
	if err == nil {
		t.Fatal("expected error for missing refund")
	}
}

// ---------------------------------------------------------------------------
// Tests: GetTransaction
// ---------------------------------------------------------------------------

func TestGetTransaction_NotConfigured(t *testing.T) {
	p := newTestProvider()
	_, err := p.GetTransaction(context.Background(), "tx-1")
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
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(graphqlResp(map[string]interface{}{
			"node": map[string]interface{}{
				"id":     "bt-tx-100",
				"status": "SETTLED",
				"amount": map[string]interface{}{
					"value":        "25.00",
					"currencyCode": "USD",
				},
				"orderId":   "ord-100",
				"createdAt": "2026-01-15T10:00:00Z",
				"updatedAt": "2026-01-15T10:05:00Z",
			},
		}))
	}))
	defer server.Close()

	p := configuredProvider(server.URL)
	tx, err := p.GetTransaction(context.Background(), "bt-tx-100")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tx.ID != "bt-tx-100" {
		t.Errorf("ID = %q, want bt-tx-100", tx.ID)
	}
	if tx.Status != "completed" {
		t.Errorf("Status = %q, want completed", tx.Status)
	}
	if tx.Amount != 2500 {
		t.Errorf("Amount = %d, want 2500", tx.Amount)
	}
	if tx.Currency != currency.USD {
		t.Errorf("Currency = %q, want usd", tx.Currency)
	}
	if tx.CreatedAt == 0 {
		t.Error("CreatedAt should be non-zero")
	}
}

func TestGetTransaction_NotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(graphqlResp(map[string]interface{}{
			"node": nil,
		}))
	}))
	defer server.Close()

	p := configuredProvider(server.URL)
	_, err := p.GetTransaction(context.Background(), "nonexistent")
	if err == nil {
		t.Fatal("expected error for not found")
	}
}

func TestGetTransaction_GraphQLError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(graphqlError("Access denied"))
	}))
	defer server.Close()

	p := configuredProvider(server.URL)
	_, err := p.GetTransaction(context.Background(), "tx-1")
	if err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// Tests: ValidateWebhook
// ---------------------------------------------------------------------------

func TestValidateWebhook_NotConfigured(t *testing.T) {
	p := newTestProvider()
	_, err := p.ValidateWebhook(context.Background(), []byte("payload"), "sig")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestValidateWebhook_InvalidSignatureFormat(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer server.Close()
	p := configuredProvider(server.URL)

	_, err := p.ValidateWebhook(context.Background(), []byte("payload"), "no-pipe-here")
	if err == nil {
		t.Fatal("expected error for invalid signature format")
	}
}

func TestValidateWebhook_PublicKeyMismatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer server.Close()
	p := configuredProvider(server.URL)

	_, err := p.ValidateWebhook(context.Background(), []byte("payload"), "wrong-key|abc123")
	if err == nil {
		t.Fatal("expected error for public key mismatch")
	}
}

func TestValidateWebhook_HashMismatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer server.Close()
	p := configuredProvider(server.URL)

	_, err := p.ValidateWebhook(context.Background(), []byte("payload"), "pub-key|bad-hash")
	if err == nil {
		t.Fatal("expected error for hash mismatch")
	}
}

func TestValidateWebhook_Success_JSONPayload(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer server.Close()
	p := configuredProvider(server.URL)

	eventData, _ := json.Marshal(map[string]interface{}{
		"kind":      "subscription_charged_successfully",
		"id":        "evt-1",
		"timestamp": "2026-01-15T10:00:00Z",
	})
	encodedPayload := base64.StdEncoding.EncodeToString(eventData)

	// Compute valid HMAC.
	mac := hmac.New(sha1.New, []byte("priv-key"))
	mac.Write([]byte(encodedPayload))
	validHash := hex.EncodeToString(mac.Sum(nil))

	sig := "pub-key|" + validHash

	event, err := p.ValidateWebhook(context.Background(), []byte(encodedPayload), sig)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if event.ID != "evt-1" {
		t.Errorf("event ID = %q, want evt-1", event.ID)
	}
	if event.Type != "subscription_charged_successfully" {
		t.Errorf("event Type = %q", event.Type)
	}
	if event.Processor != processor.Braintree {
		t.Errorf("Processor = %q, want braintree", event.Processor)
	}
}

func TestValidateWebhook_Success_XMLPayload(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer server.Close()
	p := configuredProvider(server.URL)

	xmlPayload := `<notification><kind>check</kind></notification>`
	encodedPayload := base64.StdEncoding.EncodeToString([]byte(xmlPayload))

	mac := hmac.New(sha1.New, []byte("priv-key"))
	mac.Write([]byte(encodedPayload))
	validHash := hex.EncodeToString(mac.Sum(nil))

	sig := "pub-key|" + validHash

	event, err := p.ValidateWebhook(context.Background(), []byte(encodedPayload), sig)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// XML falls back to raw_notification type.
	if event.Type != "raw_notification" {
		t.Errorf("event Type = %q, want raw_notification", event.Type)
	}
}

// ---------------------------------------------------------------------------
// Tests: executeGraphQL error paths
// ---------------------------------------------------------------------------

func TestExecuteGraphQL_HTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("server error"))
	}))
	defer server.Close()

	p := configuredProvider(server.URL)
	_, err := p.executeGraphQL(context.Background(), "query{}", nil)
	if err == nil {
		t.Fatal("expected error for 500")
	}
}

func TestExecuteGraphQL_InvalidJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("not json"))
	}))
	defer server.Close()

	p := configuredProvider(server.URL)
	_, err := p.executeGraphQL(context.Background(), "query{}", nil)
	if err == nil {
		t.Fatal("expected error for invalid JSON response")
	}
}
