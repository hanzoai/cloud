package recurly

import (
	"context"
	"encoding/json"
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
		BaseProcessor: processor.NewBaseProcessor(processor.Recurly, supportedCurrencies()),
		client:        &http.Client{Timeout: 5 * time.Second},
	}
}

func configuredProvider(serverURL string) *Provider {
	p := &Provider{
		BaseProcessor: processor.NewBaseProcessor(processor.Recurly, supportedCurrencies()),
		apiKey:        "test-api-key",
		subdomain:     "testco",
		client: &http.Client{
			Timeout:   5 * time.Second,
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
	if got := p.Type(); got != processor.Recurly {
		t.Errorf("Type() = %q, want %q", got, processor.Recurly)
	}
}

func TestIsAvailable_NotConfigured(t *testing.T) {
	p := newTestProvider()
	if p.IsAvailable(context.Background()) {
		t.Error("IsAvailable() = true for unconfigured provider, want false")
	}
}

func TestIsAvailable_EmptyAPIKey(t *testing.T) {
	p := newTestProvider()
	p.Configure("")
	if p.IsAvailable(context.Background()) {
		t.Error("IsAvailable() = true with empty API key, want false")
	}
}

func TestConfigure_Valid(t *testing.T) {
	p := newTestProvider()
	p.Configure("test-api-key")
	if !p.IsAvailable(context.Background()) {
		t.Error("IsAvailable() = false after valid Configure(), want true")
	}
}

func TestConfigure_WithSubdomain(t *testing.T) {
	p := newTestProvider()
	p.Configure("test-api-key", "mycompany")
	if !p.IsAvailable(context.Background()) {
		t.Error("IsAvailable() = false after Configure() with subdomain, want true")
	}
}

func TestConfigure_Reconfigure(t *testing.T) {
	p := newTestProvider()
	p.Configure("key1")
	if !p.IsAvailable(context.Background()) {
		t.Error("expected available after first configure")
	}
	p.Configure("")
	if p.IsAvailable(context.Background()) {
		t.Error("expected not available after reconfigure with empty key")
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

	expected := []currency.Type{currency.USD, currency.EUR, currency.GBP, currency.CAD, currency.JPY}
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

func TestCentsToDecimalString(t *testing.T) {
	tests := []struct {
		cents       currency.Cents
		zeroDecimal bool
		want        string
	}{
		{1099, false, "10.99"},
		{500, true, "500.0"},
		{0, false, "0.00"},
		{1, false, "0.01"},
	}
	for _, tt := range tests {
		got := centsToDecimalString(tt.cents, tt.zeroDecimal)
		if got != tt.want {
			t.Errorf("centsToDecimalString(%d, %v) = %q, want %q", tt.cents, tt.zeroDecimal, got, tt.want)
		}
	}
}

func TestDecimalToCents(t *testing.T) {
	tests := []struct {
		amount float64
		cur    currency.Type
		want   currency.Cents
	}{
		{10.99, currency.USD, 1099},
		{500, currency.JPY, 500},
	}
	for _, tt := range tests {
		got := decimalToCents(tt.amount, tt.cur)
		if got != tt.want {
			t.Errorf("decimalToCents(%f, %s) = %d, want %d", tt.amount, tt.cur, got, tt.want)
		}
	}
}

func TestResolveAccountCode(t *testing.T) {
	p := newTestProvider()

	// With CustomerID.
	req := processor.PaymentRequest{CustomerID: "cust-1"}
	if got := p.resolveAccountCode(req); got != "cust-1" {
		t.Errorf("resolveAccountCode = %q, want cust-1", got)
	}

	// With OrderID only.
	req = processor.PaymentRequest{OrderID: "ord-1"}
	if got := p.resolveAccountCode(req); got != "ord-1" {
		t.Errorf("resolveAccountCode = %q, want ord-1", got)
	}

	// With nothing: generates a unique code.
	req = processor.PaymentRequest{}
	got := p.resolveAccountCode(req)
	if !strings.HasPrefix(got, "hanzo-") {
		t.Errorf("resolveAccountCode = %q, want prefix hanzo-", got)
	}
}

func TestParseAPIError_Structured(t *testing.T) {
	p := newTestProvider()
	body, _ := json.Marshal(recurlyErrorResponse{
		Error: struct {
			Type    string                   `json:"type"`
			Message string                   `json:"message"`
			Params  []map[string]interface{} `json:"params"`
		}{
			Type:    "validation",
			Message: "Amount is invalid",
		},
	})

	got := p.parseAPIError(body)
	if !strings.Contains(got, "validation") || !strings.Contains(got, "Amount is invalid") {
		t.Errorf("parseAPIError = %q", got)
	}
}

func TestParseAPIError_Unstructured(t *testing.T) {
	p := newTestProvider()
	got := p.parseAPIError([]byte("plain text error"))
	if got != "plain text error" {
		t.Errorf("parseAPIError = %q, want plain text error", got)
	}
}

func TestMapWebhookType(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"new_payment_notification", "payment.created"},
		{"successful_payment_notification", "payment.succeeded"},
		{"failed_payment_notification", "payment.failed"},
		{"new_invoice_notification", "invoice.created"},
		{"past_due_invoice_notification", "invoice.past_due"},
		{"closed_invoice_notification", "invoice.closed"},
		{"new_subscription_notification", "subscription.created"},
		{"renewed_subscription_notification", "subscription.renewed"},
		{"expired_subscription_notification", "subscription.expired"},
		{"canceled_subscription_notification", "subscription.canceled"},
		{"updated_subscription_notification", "subscription.updated"},
		{"reactivated_account_notification", "account.reactivated"},
		{"new_account_notification", "account.created"},
		{"canceled_account_notification", "account.canceled"},
		{"billing_info_updated_notification", "billing_info.updated"},
		{"billing_info_update_failed_notification", "billing_info.update_failed"},
		{"successful_refund_notification", "refund.succeeded"},
		{"void_payment_notification", "payment.voided"},
		{"new_dunning_event_notification", "dunning.created"},
		{"unknown_type", "unknown_type"},
	}
	for _, tt := range tests {
		got := mapWebhookType(tt.input)
		if got != tt.want {
			t.Errorf("mapWebhookType(%q) = %q, want %q", tt.input, got, tt.want)
		}
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

func TestCharge_UnsupportedCurrency(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer server.Close()
	p := configuredProvider(server.URL)

	_, err := p.Charge(context.Background(), processor.PaymentRequest{
		Amount: 1000, Currency: currency.Type("zzz"),
	})
	if err == nil {
		t.Fatal("expected error for unsupported currency")
	}
}

func TestCharge_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/purchases") {
			t.Fatalf("unexpected: %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(recurlyInvoiceCollection{
			Object: "invoice_collection",
			ChargeInvoice: recurlyInvoice{
				ID:     "inv-100",
				Number: "1001",
				State:  "paid",
				Transactions: []recurlyTransaction{
					{ID: "txn-100", UUID: "uuid-100", Status: "success"},
				},
			},
		})
	}))
	defer server.Close()

	p := configuredProvider(server.URL)
	result, err := p.Charge(context.Background(), processor.PaymentRequest{
		Amount:      2500,
		Currency:    currency.USD,
		Token:       "tok-1",
		CustomerID:  "cust-1",
		Description: "Test charge",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatal("expected success")
	}
	if result.TransactionID != "txn-100" {
		t.Errorf("TransactionID = %q, want txn-100", result.TransactionID)
	}
	if result.ProcessorRef != "uuid-100" {
		t.Errorf("ProcessorRef = %q, want uuid-100", result.ProcessorRef)
	}
}

func TestCharge_DirectInvoiceResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Some responses return invoice directly, not in a collection.
		json.NewEncoder(w).Encode(recurlyInvoice{
			ID:     "inv-direct",
			Number: "2001",
			State:  "pending",
		})
	}))
	defer server.Close()

	p := configuredProvider(server.URL)
	result, err := p.Charge(context.Background(), processor.PaymentRequest{
		Amount: 1000, Currency: currency.USD,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.TransactionID != "inv-direct" {
		t.Errorf("TransactionID = %q, want inv-direct", result.TransactionID)
	}
}

func TestCharge_NoToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify billing_info is omitted.
		var body map[string]interface{}
		json.NewDecoder(r.Body).Decode(&body)
		acct := body["account"].(map[string]interface{})
		if _, ok := acct["billing_info"]; ok {
			t.Error("billing_info should be omitted when no token")
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(recurlyInvoiceCollection{
			ChargeInvoice: recurlyInvoice{
				ID: "inv-200", Number: "2002", State: "paid",
			},
		})
	}))
	defer server.Close()

	p := configuredProvider(server.URL)
	_, err := p.Charge(context.Background(), processor.PaymentRequest{
		Amount: 1000, Currency: currency.USD,
		// No Token.
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCharge_APIError_Declined(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(422)
		json.NewEncoder(w).Encode(recurlyErrorResponse{
			Error: struct {
				Type    string                   `json:"type"`
				Message string                   `json:"message"`
				Params  []map[string]interface{} `json:"params"`
			}{
				Type:    "transaction",
				Message: "Transaction declined",
			},
		})
	}))
	defer server.Close()

	p := configuredProvider(server.URL)
	_, err := p.Charge(context.Background(), processor.PaymentRequest{
		Amount: 1000, Currency: currency.USD, Token: "tok",
	})
	if err == nil {
		t.Fatal("expected error for declined")
	}
}

func TestCharge_APIError_401(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		w.Write([]byte(`{"error":{"type":"authentication","message":"invalid api key"}}`))
	}))
	defer server.Close()

	p := configuredProvider(server.URL)
	_, err := p.Charge(context.Background(), processor.PaymentRequest{
		Amount: 1000, Currency: currency.USD, Token: "tok",
	})
	if err == nil {
		t.Fatal("expected error for 401")
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

func TestCharge_EmptyInvoiceResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(recurlyInvoiceCollection{})
	}))
	defer server.Close()

	p := configuredProvider(server.URL)
	_, err := p.Charge(context.Background(), processor.PaymentRequest{
		Amount: 1000, Currency: currency.USD,
	})
	if err == nil {
		t.Fatal("expected error for empty response")
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
		t.Fatal("expected error")
	}
}

func TestAuthorize_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]interface{}
		json.NewDecoder(r.Body).Decode(&body)
		if body["collection_method"] != "manual" {
			t.Error("expected collection_method=manual for authorize")
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(recurlyInvoiceCollection{
			ChargeInvoice: recurlyInvoice{
				ID:     "inv-auth",
				Number: "3001",
				State:  "pending",
			},
		})
	}))
	defer server.Close()

	p := configuredProvider(server.URL)
	result, err := p.Authorize(context.Background(), processor.PaymentRequest{
		Amount: 3000, Currency: currency.USD, Token: "tok",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != "authorized" {
		t.Errorf("Status = %q, want authorized", result.Status)
	}
}

func TestAuthorize_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(422)
		w.Write([]byte(`{"error":{"type":"validation","message":"insufficient funds"}}`))
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

// ---------------------------------------------------------------------------
// Tests: Capture
// ---------------------------------------------------------------------------

func TestCapture_NotConfigured(t *testing.T) {
	p := newTestProvider()
	_, err := p.Capture(context.Background(), "inv-1", 1000)
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
		t.Fatal("expected error for empty ID")
	}
}

func TestCapture_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || !strings.Contains(r.URL.Path, "/collect") {
			t.Fatalf("unexpected: %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(recurlyInvoice{
			ID:     "inv-cap",
			Number: "4001",
			State:  "paid",
			Transactions: []recurlyTransaction{
				{ID: "txn-cap", UUID: "uuid-cap"},
			},
		})
	}))
	defer server.Close()

	p := configuredProvider(server.URL)
	result, err := p.Capture(context.Background(), "inv-auth", 2500)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatal("expected success")
	}
	if result.TransactionID != "txn-cap" {
		t.Errorf("TransactionID = %q, want txn-cap", result.TransactionID)
	}
}

func TestCapture_NoTransactions(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(recurlyInvoice{
			ID:     "inv-no-txn",
			Number: "4002",
			State:  "paid",
		})
	}))
	defer server.Close()

	p := configuredProvider(server.URL)
	result, err := p.Capture(context.Background(), "inv-auth-2", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.TransactionID != "inv-no-txn" {
		t.Errorf("TransactionID = %q, want inv-no-txn (fallback to invoice ID)", result.TransactionID)
	}
}

func TestCapture_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		w.Write([]byte(`{"error":{"type":"not_found","message":"invoice not found"}}`))
	}))
	defer server.Close()

	p := configuredProvider(server.URL)
	_, err := p.Capture(context.Background(), "inv-missing", 1000)
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
		TransactionID: "inv-1", Amount: 500,
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

func TestRefund_ZeroAmount(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer server.Close()
	p := configuredProvider(server.URL)

	_, err := p.Refund(context.Background(), processor.RefundRequest{
		TransactionID: "inv-1", Amount: 0,
	})
	if err == nil {
		t.Fatal("expected error for zero amount")
	}
}

func TestRefund_Success_InvoiceID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.Contains(r.URL.Path, "/refund") {
			// Could be a transaction lookup, return 404 to fall through.
			if strings.Contains(r.URL.Path, "/transactions/") {
				w.WriteHeader(404)
				return
			}
			t.Fatalf("unexpected: %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(recurlyInvoice{
			ID:     "inv-refund",
			Number: "5001",
			State:  "paid",
		})
	}))
	defer server.Close()

	p := configuredProvider(server.URL)
	result, err := p.Refund(context.Background(), processor.RefundRequest{
		TransactionID: "inv-orig",
		Amount:        500,
		Reason:        "Test refund",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatal("expected success")
	}
	if result.RefundID != "inv-refund" {
		t.Errorf("RefundID = %q, want inv-refund", result.RefundID)
	}
}

func TestRefund_WithTransactionLookup(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/transactions/") && r.Method == http.MethodGet {
			// Return transaction with invoice reference.
			json.NewEncoder(w).Encode(map[string]interface{}{
				"id":      "txn-lookup",
				"uuid":    "uuid-lookup",
				"status":  "success",
				"invoice": map[string]interface{}{"id": "inv-from-txn"},
			})
			return
		}
		if strings.Contains(r.URL.Path, "/refund") {
			json.NewEncoder(w).Encode(recurlyInvoice{
				ID: "inv-ref-2", Number: "6001", State: "paid",
			})
			return
		}
	}))
	defer server.Close()

	p := configuredProvider(server.URL)
	result, err := p.Refund(context.Background(), processor.RefundRequest{
		TransactionID: "txn-lookup",
		Amount:        500,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatal("expected success")
	}
}

func TestRefund_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/transactions/") {
			w.WriteHeader(404)
			return
		}
		w.WriteHeader(422)
		w.Write([]byte(`{"error":{"type":"validation","message":"cannot refund"}}`))
	}))
	defer server.Close()

	p := configuredProvider(server.URL)
	result, err := p.Refund(context.Background(), processor.RefundRequest{
		TransactionID: "inv-bad",
		Amount:        500,
	})
	// Refund returns unsuccessful result without error for API errors.
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Fatal("expected failure")
	}
}

// ---------------------------------------------------------------------------
// Tests: GetTransaction
// ---------------------------------------------------------------------------

func TestGetTransaction_NotConfigured(t *testing.T) {
	p := newTestProvider()
	_, err := p.GetTransaction(context.Background(), "txn-1")
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
		json.NewEncoder(w).Encode(map[string]interface{}{
			"id":                    "txn-500",
			"uuid":                  "uuid-500",
			"type":                  "purchase",
			"status":                "success",
			"amount":                25.0,
			"currency":              "USD",
			"payment_method_object": "credit_card",
			"collection_method":     "automatic",
			"origin":                "api",
			"created_at":            "2026-01-15T10:00:00Z",
			"updated_at":            "2026-01-15T10:05:00Z",
			"account":              map[string]interface{}{"code": "acct-1"},
			"invoice":              map[string]interface{}{"id": "inv-1"},
		})
	}))
	defer server.Close()

	p := configuredProvider(server.URL)
	tx, err := p.GetTransaction(context.Background(), "txn-500")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tx.ID != "txn-500" {
		t.Errorf("ID = %q, want txn-500", tx.ID)
	}
	if tx.ProcessorRef != "uuid-500" {
		t.Errorf("ProcessorRef = %q, want uuid-500", tx.ProcessorRef)
	}
	if tx.Type != "charge" {
		t.Errorf("Type = %q, want charge", tx.Type)
	}
	if tx.Status != "success" {
		t.Errorf("Status = %q, want success", tx.Status)
	}
	if tx.CustomerID != "acct-1" {
		t.Errorf("CustomerID = %q, want acct-1", tx.CustomerID)
	}
}

func TestGetTransaction_RefundType(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"id":         "txn-ref",
			"uuid":       "uuid-ref",
			"type":       "refund",
			"status":     "success",
			"amount":     5.0,
			"currency":   "USD",
			"created_at": "2026-01-15T10:00:00Z",
			"updated_at": "2026-01-15T10:00:00Z",
		})
	}))
	defer server.Close()

	p := configuredProvider(server.URL)
	tx, err := p.GetTransaction(context.Background(), "txn-ref")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tx.Type != "refund" {
		t.Errorf("Type = %q, want refund", tx.Type)
	}
}

func TestGetTransaction_NotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		w.Write([]byte(`{"error":{"type":"not_found","message":"not found"}}`))
	}))
	defer server.Close()

	p := configuredProvider(server.URL)
	_, err := p.GetTransaction(context.Background(), "txn-missing")
	if err == nil {
		t.Fatal("expected error for 404")
	}
}

// ---------------------------------------------------------------------------
// Tests: ValidateWebhook
// ---------------------------------------------------------------------------

func TestValidateWebhook_NotConfigured(t *testing.T) {
	p := newTestProvider()
	_, err := p.ValidateWebhook(context.Background(), []byte("<xml/>"), "")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestValidateWebhook_EmptyPayload(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer server.Close()
	p := configuredProvider(server.URL)

	_, err := p.ValidateWebhook(context.Background(), []byte{}, "")
	if err == nil {
		t.Fatal("expected error for empty payload")
	}
}

func TestValidateWebhook_InvalidXML(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer server.Close()
	p := configuredProvider(server.URL)

	_, err := p.ValidateWebhook(context.Background(), []byte("not xml at all <<<"), "")
	if err == nil {
		t.Fatal("expected error for invalid XML")
	}
}

func TestValidateWebhook_SuccessfulPayment(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer server.Close()
	p := configuredProvider(server.URL)

	xml := `<?xml version="1.0" encoding="UTF-8"?>
<successful_payment_notification>
  <account>
    <account_code>acct-1</account_code>
    <email>test@example.com</email>
    <first_name>John</first_name>
    <last_name>Doe</last_name>
  </account>
  <transaction>
    <id>txn-100</id>
    <uuid>uuid-100</uuid>
    <amount_in_cents>2500</amount_in_cents>
    <currency>USD</currency>
    <status>success</status>
  </transaction>
</successful_payment_notification>`

	event, err := p.ValidateWebhook(context.Background(), []byte(xml), "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if event.Type != "payment.succeeded" {
		t.Errorf("Type = %q, want payment.succeeded", event.Type)
	}
	if event.ID != "uuid-100" {
		t.Errorf("ID = %q, want uuid-100", event.ID)
	}
	if event.Processor != processor.Recurly {
		t.Errorf("Processor = %q, want recurly", event.Processor)
	}
}

func TestValidateWebhook_SubscriptionNotification(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer server.Close()
	p := configuredProvider(server.URL)

	xml := `<?xml version="1.0" encoding="UTF-8"?>
<new_subscription_notification>
  <subscription>
    <id>sub-1</id>
    <plan><plan_code>gold</plan_code></plan>
    <state>active</state>
    <quantity>1</quantity>
  </subscription>
</new_subscription_notification>`

	event, err := p.ValidateWebhook(context.Background(), []byte(xml), "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if event.Type != "subscription.created" {
		t.Errorf("Type = %q, want subscription.created", event.Type)
	}
	if event.ID != "sub-1" {
		t.Errorf("ID = %q, want sub-1", event.ID)
	}
}

func TestValidateWebhook_InvoiceNotification(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer server.Close()
	p := configuredProvider(server.URL)

	xml := `<?xml version="1.0" encoding="UTF-8"?>
<new_invoice_notification>
  <invoice>
    <id>inv-1</id>
    <invoice_number>1001</invoice_number>
    <state>open</state>
    <total_in_cents>5000</total_in_cents>
    <currency>USD</currency>
  </invoice>
</new_invoice_notification>`

	event, err := p.ValidateWebhook(context.Background(), []byte(xml), "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if event.Type != "invoice.created" {
		t.Errorf("Type = %q, want invoice.created", event.Type)
	}
	if event.ID != "inv-1" {
		t.Errorf("ID = %q, want inv-1", event.ID)
	}
}

// ---------------------------------------------------------------------------
// Tests: handleErrorResponse edge cases
// ---------------------------------------------------------------------------

func TestHandleErrorResponse_RateLimited(t *testing.T) {
	p := newTestProvider()
	p.apiKey = "key"

	body, _ := json.Marshal(recurlyErrorResponse{
		Error: struct {
			Type    string                   `json:"type"`
			Message string                   `json:"message"`
			Params  []map[string]interface{} `json:"params"`
		}{
			Type:    "rate_limited",
			Message: "Too many requests",
		},
	})

	_, err := p.handleErrorResponse(body, 429, "charge")
	if err == nil {
		t.Fatal("expected error")
	}
	pe, ok := err.(*processor.PaymentError)
	if !ok {
		t.Fatal("expected PaymentError")
	}
	if pe.Code != "RATE_LIMITED" {
		t.Errorf("code = %q, want RATE_LIMITED", pe.Code)
	}
}

func TestHandleErrorResponse_InsufficientFunds(t *testing.T) {
	p := newTestProvider()
	p.apiKey = "key"

	body := []byte(`{"error":{"type":"transaction","message":"insufficient funds"}}`)
	_, err := p.handleErrorResponse(body, 422, "charge")
	if err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// Tests: authHeader
// ---------------------------------------------------------------------------

func TestAuthHeader(t *testing.T) {
	p := newTestProvider()
	p.apiKey = "my-api-key"
	got := p.authHeader()
	if !strings.HasPrefix(got, "Basic ") {
		t.Errorf("authHeader = %q, want Basic prefix", got)
	}
}

// ---------------------------------------------------------------------------
// Tests: Configure - nil client init
// ---------------------------------------------------------------------------

func TestConfigure_NilClient(t *testing.T) {
	p := &Provider{
		BaseProcessor: processor.NewBaseProcessor(processor.Recurly, supportedCurrencies()),
		client:        nil, // nil so Configure creates one
	}
	p.Configure("test-key")
	if !p.IsAvailable(context.Background()) {
		t.Error("expected available after Configure with nil client")
	}
}

// ---------------------------------------------------------------------------
// Tests: Authorize - additional error paths
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

func TestAuthorize_UnsupportedCurrency(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer server.Close()
	p := configuredProvider(server.URL)

	_, err := p.Authorize(context.Background(), processor.PaymentRequest{
		Amount: 1000, Currency: currency.Type("zzz"),
	})
	if err == nil {
		t.Fatal("expected error for unsupported currency")
	}
}

func TestAuthorize_NetworkError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	server.Close()
	p := configuredProvider(server.URL)

	_, err := p.Authorize(context.Background(), processor.PaymentRequest{
		Amount: 1000, Currency: currency.USD, Token: "tok",
	})
	if err == nil {
		t.Fatal("expected error for network failure")
	}
}

func TestAuthorize_ParseError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("not json"))
	}))
	defer server.Close()
	p := configuredProvider(server.URL)

	_, err := p.Authorize(context.Background(), processor.PaymentRequest{
		Amount: 1000, Currency: currency.USD, Token: "tok",
	})
	if err == nil {
		t.Fatal("expected error for unparseable response")
	}
}

// ---------------------------------------------------------------------------
// Tests: Capture - additional error paths
// ---------------------------------------------------------------------------

func TestCapture_NetworkError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	server.Close()
	p := configuredProvider(server.URL)

	_, err := p.Capture(context.Background(), "inv-1", 1000)
	if err == nil {
		t.Fatal("expected error for network failure")
	}
}

func TestCapture_ParseError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("not json"))
	}))
	defer server.Close()
	p := configuredProvider(server.URL)

	_, err := p.Capture(context.Background(), "inv-1", 1000)
	if err == nil {
		t.Fatal("expected error for unparseable response")
	}
}

// ---------------------------------------------------------------------------
// Tests: Refund - additional error paths
// ---------------------------------------------------------------------------

func TestRefund_NetworkError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	server.Close()
	p := configuredProvider(server.URL)

	_, err := p.Refund(context.Background(), processor.RefundRequest{
		TransactionID: "inv-1", Amount: 500,
	})
	if err == nil {
		t.Fatal("expected error for network failure")
	}
}

// ---------------------------------------------------------------------------
// Tests: GetTransaction - network error
// ---------------------------------------------------------------------------

func TestGetTransaction_NetworkError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	server.Close()
	p := configuredProvider(server.URL)

	_, err := p.GetTransaction(context.Background(), "txn-1")
	if err == nil {
		t.Fatal("expected error for network failure")
	}
}

// ---------------------------------------------------------------------------
// Tests: handleErrorResponse - additional codes
// ---------------------------------------------------------------------------

func TestHandleErrorResponse_Forbidden(t *testing.T) {
	p := newTestProvider()
	p.apiKey = "key"

	body := []byte(`{"error":{"type":"forbidden","message":"access denied"}}`)
	_, err := p.handleErrorResponse(body, 403, "charge")
	if err == nil {
		t.Fatal("expected error")
	}
	pe, ok := err.(*processor.PaymentError)
	if !ok {
		t.Fatal("expected PaymentError")
	}
	if pe.Code != "FORBIDDEN" {
		t.Errorf("code = %q, want FORBIDDEN", pe.Code)
	}
}

func TestHandleErrorResponse_Unauthorized(t *testing.T) {
	p := newTestProvider()
	p.apiKey = "key"

	body := []byte(`{"error":{"type":"unauthorized","message":"invalid api key"}}`)
	_, err := p.handleErrorResponse(body, 401, "charge")
	if err == nil {
		t.Fatal("expected error")
	}
	pe, ok := err.(*processor.PaymentError)
	if !ok {
		t.Fatal("expected PaymentError")
	}
	if pe.Code != "AUTHENTICATION_FAILED" {
		t.Errorf("code = %q, want AUTHENTICATION_FAILED", pe.Code)
	}
}

func TestHandleErrorResponse_NotFound(t *testing.T) {
	p := newTestProvider()
	p.apiKey = "key"

	body := []byte(`{"error":{"type":"not_found","message":"resource not found"}}`)
	_, err := p.handleErrorResponse(body, 404, "charge")
	if err == nil {
		t.Fatal("expected error")
	}
	pe, ok := err.(*processor.PaymentError)
	if !ok {
		t.Fatal("expected PaymentError")
	}
	if pe.Code != "NOT_FOUND" {
		t.Errorf("code = %q, want NOT_FOUND", pe.Code)
	}
}

// ---------------------------------------------------------------------------
// Tests: parseWebhookXML - fallback event ID
// ---------------------------------------------------------------------------

func TestValidateWebhook_NoEntityIDs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer server.Close()
	p := configuredProvider(server.URL)

	xml := `<?xml version="1.0" encoding="UTF-8"?>
<reactivated_account_notification>
  <account>
    <account_code>acct-1</account_code>
  </account>
</reactivated_account_notification>`

	event, err := p.ValidateWebhook(context.Background(), []byte(xml), "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if event.Type != "account.reactivated" {
		t.Errorf("Type = %q, want account.reactivated", event.Type)
	}
	if !strings.HasPrefix(event.ID, "recurly-") {
		t.Errorf("expected fallback ID with recurly- prefix, got %q", event.ID)
	}
}

// ---------------------------------------------------------------------------
// Tests: fetchTransaction - error paths
// ---------------------------------------------------------------------------

func TestFetchTransaction_NetworkError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	server.Close()
	p := configuredProvider(server.URL)

	_, err := p.fetchTransaction(context.Background(), "txn-1")
	if err == nil {
		t.Fatal("expected error for network failure")
	}
}

func TestFetchTransaction_ServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		w.Write([]byte(`{"error":{"type":"server","message":"internal"}}`))
	}))
	defer server.Close()
	p := configuredProvider(server.URL)

	_, err := p.fetchTransaction(context.Background(), "txn-1")
	if err == nil {
		t.Fatal("expected error for 500")
	}
}

func TestFetchTransaction_ParseError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("not json"))
	}))
	defer server.Close()
	p := configuredProvider(server.URL)

	_, err := p.fetchTransaction(context.Background(), "txn-1")
	if err == nil {
		t.Fatal("expected error for unparseable response")
	}
}

// ---------------------------------------------------------------------------
// Tests: Charge - parsePurchaseResponse error path
// ---------------------------------------------------------------------------

func TestCharge_ParsePurchaseResponseError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("not json at all"))
	}))
	defer server.Close()
	p := configuredProvider(server.URL)

	_, err := p.Charge(context.Background(), processor.PaymentRequest{
		Amount: 1000, Currency: currency.USD, Token: "tok",
	})
	if err == nil {
		t.Fatal("expected error for unparseable response")
	}
}
