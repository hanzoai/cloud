package adyen

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
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
		BaseProcessor: processor.NewBaseProcessor(processor.Adyen, supportedCurrencies()),
	}
}

// configuredProvider returns a provider backed by the given httptest server.
func configuredProvider(serverURL string) *Provider {
	p := &Provider{
		BaseProcessor: processor.NewBaseProcessor(processor.Adyen, supportedCurrencies()),
		config: Config{
			APIKey:          "test-api-key",
			MerchantAccount: "TestMerchant",
			HMACKey:         hex.EncodeToString([]byte("test-hmac-key-00")),
			Environment:     Test,
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

// ---------------------------------------------------------------------------
// Tests: Type, IsAvailable, SupportedCurrencies, Configure
// ---------------------------------------------------------------------------

func TestType(t *testing.T) {
	p := newTestProvider()
	if got := p.Type(); got != processor.Adyen {
		t.Errorf("Type() = %q, want %q", got, processor.Adyen)
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
	p.Configure(Config{APIKey: "", MerchantAccount: ""})
	if p.IsAvailable(context.Background()) {
		t.Error("IsAvailable() = true with empty credentials, want false")
	}
}

func TestIsAvailable_PartialCredentials_APIKeyOnly(t *testing.T) {
	p := newTestProvider()
	p.Configure(Config{APIKey: "key", MerchantAccount: ""})
	if p.IsAvailable(context.Background()) {
		t.Error("IsAvailable() = true with only API key, want false")
	}
}

func TestIsAvailable_PartialCredentials_MerchantOnly(t *testing.T) {
	p := newTestProvider()
	p.Configure(Config{APIKey: "", MerchantAccount: "merchant"})
	if p.IsAvailable(context.Background()) {
		t.Error("IsAvailable() = true with only merchant account, want false")
	}
}

func TestConfigure_Valid(t *testing.T) {
	p := newTestProvider()
	p.Configure(Config{APIKey: "test-key", MerchantAccount: "TestMerchant"})
	if !p.IsAvailable(context.Background()) {
		t.Error("IsAvailable() = false after valid Configure(), want true")
	}
}

func TestConfigure_LiveEnvironment(t *testing.T) {
	p := newTestProvider()
	p.Configure(Config{
		APIKey:          "live-key",
		MerchantAccount: "LiveMerchant",
		LiveURLPrefix:   "1797a841fbb37ca7-Demo",
		Environment:     Live,
	})
	if !p.IsAvailable(context.Background()) {
		t.Error("IsAvailable() = false after live Configure(), want true")
	}
}

func TestConfigure_Reconfigure(t *testing.T) {
	p := newTestProvider()
	p.Configure(Config{APIKey: "key", MerchantAccount: "merchant"})
	if !p.IsAvailable(context.Background()) {
		t.Error("expected available after first configure")
	}
	p.Configure(Config{APIKey: "", MerchantAccount: ""})
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

	expected := []currency.Type{currency.USD, currency.EUR, currency.GBP, currency.JPY, currency.KRW}
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

func TestBaseURL_Test(t *testing.T) {
	p := newTestProvider()
	p.config.Environment = Test
	want := fmt.Sprintf("%s/%s", testBaseURL, apiVersion)
	if got := p.baseURL(); got != want {
		t.Errorf("baseURL() = %q, want %q", got, want)
	}
}

func TestBaseURL_Live_WithPrefix(t *testing.T) {
	p := newTestProvider()
	p.config.Environment = Live
	p.config.LiveURLPrefix = "prefix123"
	want := fmt.Sprintf("https://prefix123-checkout-live.adyenpayments.com/checkout/%s", apiVersion)
	if got := p.baseURL(); got != want {
		t.Errorf("baseURL() = %q, want %q", got, want)
	}
}

func TestBaseURL_Live_WithoutPrefix(t *testing.T) {
	p := newTestProvider()
	p.config.Environment = Live
	p.config.LiveURLPrefix = ""
	want := fmt.Sprintf("https://checkout-live.adyen.com/checkout/%s", apiVersion)
	if got := p.baseURL(); got != want {
		t.Errorf("baseURL() = %q, want %q", got, want)
	}
}

// ---------------------------------------------------------------------------
// Tests: ensureAvailable
// ---------------------------------------------------------------------------

func TestEnsureAvailable_NotConfigured(t *testing.T) {
	p := newTestProvider()
	if err := p.ensureAvailable(); err == nil {
		t.Error("expected error")
	}
}

func TestEnsureAvailable_Configured(t *testing.T) {
	p := newTestProvider()
	p.config = Config{APIKey: "k", MerchantAccount: "m"}
	if err := p.ensureAvailable(); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Tests: Helper functions
// ---------------------------------------------------------------------------

func TestIsStoredPaymentMethodID(t *testing.T) {
	tests := []struct {
		token string
		want  bool
	}{
		{"8234567890123456", true},
		{"8000000000000000", true},
		{"1234567890123456", false}, // doesn't start with 8
		{"82345678", false},         // too short
		{"823456789012345x", false}, // non-digit
		{"", false},
	}
	for _, tt := range tests {
		if got := isStoredPaymentMethodID(tt.token); got != tt.want {
			t.Errorf("isStoredPaymentMethodID(%q) = %v, want %v", tt.token, got, tt.want)
		}
	}
}

func TestFormatRefusal(t *testing.T) {
	tests := []struct {
		reason, code, want string
	}{
		{"", "", "payment refused"},
		{"Insufficient funds", "", "Insufficient funds"},
		{"", "2", "refused (code: 2)"},
		{"Insufficient funds", "2", "Insufficient funds (code: 2)"},
	}
	for _, tt := range tests {
		if got := formatRefusal(tt.reason, tt.code); got != tt.want {
			t.Errorf("formatRefusal(%q, %q) = %q, want %q", tt.reason, tt.code, got, tt.want)
		}
	}
}

func TestMapCaptureStatus(t *testing.T) {
	if got := mapCaptureStatus("received"); got != "capture_pending" {
		t.Errorf("mapCaptureStatus(received) = %q, want capture_pending", got)
	}
	if got := mapCaptureStatus("failed"); got != "failed" {
		t.Errorf("mapCaptureStatus(failed) = %q, want failed", got)
	}
}

func TestMapWebhookEventType(t *testing.T) {
	tests := []struct {
		code, success, want string
	}{
		{"AUTHORISATION", "true", "payment.authorized"},
		{"AUTHORISATION", "false", "payment.refused"},
		{"CAPTURE", "true", "payment.captured"},
		{"CAPTURE", "false", "payment.capture_failed"},
		{"CAPTURE_FAILED", "true", "payment.capture_failed"},
		{"CANCELLATION", "true", "payment.cancelled"},
		{"CANCELLATION", "false", "payment.cancel_failed"},
		{"REFUND", "true", "refund.succeeded"},
		{"REFUND", "false", "refund.failed"},
		{"REFUND_FAILED", "true", "refund.failed"},
		{"REFUNDED_REVERSED", "true", "refund.reversed"},
		{"CHARGEBACK", "true", "dispute.created"},
		{"CHARGEBACK_REVERSED", "true", "dispute.reversed"},
		{"SECOND_CHARGEBACK", "true", "dispute.second_chargeback"},
		{"NOTIFICATION_OF_CHARGEBACK", "true", "dispute.notification"},
		{"PREARBITRATION_LOST", "true", "dispute.lost"},
		{"PREARBITRATION_WON", "true", "dispute.won"},
		{"REQUEST_FOR_INFORMATION", "true", "dispute.information_requested"},
		{"REPORT_AVAILABLE", "true", "report.available"},
		{"PAIDOUT_REVERSED", "true", "payout.reversed"},
		{"PAYOUT_DECLINE", "true", "payout.declined"},
		{"PAYOUT_EXPIRE", "true", "payout.expired"},
		{"PAYOUT_THIRDPARTY", "true", "payout.succeeded"},
		{"PAYOUT_THIRDPARTY", "false", "payout.failed"},
		{"RECURRING_CONTRACT", "true", "token.created"},
		{"RECURRING_CONTRACT", "false", "token.failed"},
		{"UNKNOWN_EVENT", "true", "adyen.unknown_event.true"},
	}
	for _, tt := range tests {
		got := mapWebhookEventType(tt.code, tt.success)
		if got != tt.want {
			t.Errorf("mapWebhookEventType(%q, %q) = %q, want %q", tt.code, tt.success, got, tt.want)
		}
	}
}

func TestStringifyMap(t *testing.T) {
	m := map[string]interface{}{
		"key1": "value1",
		"key2": 42,
		"key3": true,
	}
	result := stringifyMap(m)
	if result["key1"] != "value1" {
		t.Errorf("key1 = %q, want value1", result["key1"])
	}
	if result["key2"] != "42" {
		t.Errorf("key2 = %q, want 42", result["key2"])
	}
	if result["key3"] != "true" {
		t.Errorf("key3 = %q, want true", result["key3"])
	}
}

func TestBuildPaymentMethod_StoredPayment(t *testing.T) {
	req := processor.PaymentRequest{
		Token:   "8234567890123456",
		Options: map[string]interface{}{},
	}
	pm := buildPaymentMethod(req)
	if pm.StoredPaymentMethodID != "8234567890123456" {
		t.Errorf("StoredPaymentMethodID = %q, want stored token", pm.StoredPaymentMethodID)
	}
}

func TestBuildPaymentMethod_RecurringDetail(t *testing.T) {
	req := processor.PaymentRequest{
		Token: "some-token",
		Options: map[string]interface{}{
			"recurringDetailReference": "rd-ref-123",
		},
	}
	pm := buildPaymentMethod(req)
	if pm.RecurringDetailReference != "rd-ref-123" {
		t.Errorf("RecurringDetailReference = %q, want rd-ref-123", pm.RecurringDetailReference)
	}
}

func TestBuildPaymentMethod_EncryptedCard(t *testing.T) {
	req := processor.PaymentRequest{
		Token: "encrypted-card-number",
		Options: map[string]interface{}{
			"encryptedExpiryMonth":    "enc-month",
			"encryptedExpiryYear":     "enc-year",
			"encryptedSecurityCode":   "enc-cvc",
			"paymentMethodType":       "ideal",
		},
	}
	pm := buildPaymentMethod(req)
	if pm.EncryptedCardNumber != "encrypted-card-number" {
		t.Error("EncryptedCardNumber mismatch")
	}
	if pm.EncryptedExpiryMonth != "enc-month" {
		t.Error("EncryptedExpiryMonth mismatch")
	}
	if pm.Type != "ideal" {
		t.Errorf("Type = %q, want ideal", pm.Type)
	}
}

func TestBuildPaymentMethod_Default(t *testing.T) {
	req := processor.PaymentRequest{
		Options: map[string]interface{}{},
	}
	pm := buildPaymentMethod(req)
	if pm.Type != "scheme" {
		t.Errorf("Type = %q, want scheme", pm.Type)
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

func TestCharge_Authorised(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(adyenPaymentResponse{
			PSPReference: "PSP-123",
			ResultCode:   "Authorised",
		})
	}))
	defer server.Close()

	p := configuredProvider(server.URL)
	result, err := p.Charge(context.Background(), processor.PaymentRequest{
		Amount:      1500,
		Currency:    currency.USD,
		Token:       "enc-card",
		CustomerID:  "cust-1",
		Description: "Test charge",
		OrderID:     "ord-1",
		Metadata:    map[string]interface{}{"key": "val"},
		Options:     map[string]interface{}{"returnUrl": "https://example.com/return"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatal("expected success")
	}
	if result.TransactionID != "PSP-123" {
		t.Errorf("TransactionID = %q, want PSP-123", result.TransactionID)
	}
	if result.Status != "succeeded" {
		t.Errorf("Status = %q, want succeeded", result.Status)
	}
}

func TestCharge_Refused(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(adyenPaymentResponse{
			PSPReference:      "PSP-456",
			ResultCode:        "Refused",
			RefusalReason:     "Insufficient funds",
			RefusalReasonCode: "2",
		})
	}))
	defer server.Close()

	p := configuredProvider(server.URL)
	result, err := p.Charge(context.Background(), processor.PaymentRequest{
		Amount: 1000, Currency: currency.USD, Token: "tok",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Fatal("expected failure")
	}
	if result.Status != "failed" {
		t.Errorf("Status = %q, want failed", result.Status)
	}
}

func TestCharge_Pending(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(adyenPaymentResponse{
			PSPReference: "PSP-P",
			ResultCode:   "Pending",
		})
	}))
	defer server.Close()

	p := configuredProvider(server.URL)
	result, err := p.Charge(context.Background(), processor.PaymentRequest{
		Amount: 1000, Currency: currency.USD, Token: "tok",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatal("Pending should be treated as success")
	}
	if result.Status != "pending" {
		t.Errorf("Status = %q, want pending", result.Status)
	}
}

func TestCharge_Error(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(adyenPaymentResponse{
			PSPReference:  "PSP-E",
			ResultCode:    "Error",
			RefusalReason: "System error",
		})
	}))
	defer server.Close()

	p := configuredProvider(server.URL)
	result, err := p.Charge(context.Background(), processor.PaymentRequest{
		Amount: 1000, Currency: currency.USD, Token: "tok",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Fatal("expected failure")
	}
	if result.Status != "error" {
		t.Errorf("Status = %q, want error", result.Status)
	}
}

func TestCharge_RedirectShopper(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(adyenPaymentResponse{
			PSPReference: "PSP-3DS",
			ResultCode:   "RedirectShopper",
		})
	}))
	defer server.Close()

	p := configuredProvider(server.URL)
	result, err := p.Charge(context.Background(), processor.PaymentRequest{
		Amount: 1000, Currency: currency.USD, Token: "tok",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatal("RedirectShopper should be success (action required)")
	}
	if result.Status != "action_required" {
		t.Errorf("Status = %q, want action_required", result.Status)
	}
}

func TestCharge_UnknownResultCode(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(adyenPaymentResponse{
			PSPReference: "PSP-UNK",
			ResultCode:   "SomethingNew",
		})
	}))
	defer server.Close()

	p := configuredProvider(server.URL)
	result, err := p.Charge(context.Background(), processor.PaymentRequest{
		Amount: 1000, Currency: currency.USD, Token: "tok",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Fatal("expected failure for unknown result code")
	}
	if result.Status != "unknown" {
		t.Errorf("Status = %q, want unknown", result.Status)
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

func TestCharge_NoOrderID_GeneratesReference(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body adyenPaymentRequest
		json.NewDecoder(r.Body).Decode(&body)
		if body.Reference == "" {
			t.Error("expected non-empty auto-generated reference")
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(adyenPaymentResponse{
			PSPReference: "PSP-AUTO",
			ResultCode:   "Authorised",
		})
	}))
	defer server.Close()

	p := configuredProvider(server.URL)
	result, err := p.Charge(context.Background(), processor.PaymentRequest{
		Amount: 1000, Currency: currency.USD, Token: "tok",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatal("expected success")
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
		var body adyenPaymentRequest
		json.NewDecoder(r.Body).Decode(&body)
		if body.CaptureDelayHours == nil || *body.CaptureDelayHours != -1 {
			t.Error("expected captureDelayHours=-1 for authorize")
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(adyenPaymentResponse{
			PSPReference: "PSP-AUTH",
			ResultCode:   "Authorised",
		})
	}))
	defer server.Close()

	p := configuredProvider(server.URL)
	result, err := p.Authorize(context.Background(), processor.PaymentRequest{
		Amount: 2000, Currency: currency.EUR, Token: "tok",
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

// ---------------------------------------------------------------------------
// Tests: Capture
// ---------------------------------------------------------------------------

func TestCapture_NotConfigured(t *testing.T) {
	p := newTestProvider()
	_, err := p.Capture(context.Background(), "psp-1", 1000)
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
		t.Fatal("expected error")
	}
}

func TestCapture_ZeroAmount(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer server.Close()
	p := configuredProvider(server.URL)

	_, err := p.Capture(context.Background(), "psp-1", 0)
	if err == nil {
		t.Fatal("expected error for zero amount")
	}
}

func TestCapture_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/captures") {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(adyenCaptureResponse{
			PSPReference:        "CAP-PSP-1",
			Status:              "received",
			PaymentPSPReference: "PSP-ORIG",
		})
	}))
	defer server.Close()

	p := configuredProvider(server.URL)
	result, err := p.Capture(context.Background(), "PSP-ORIG", 1500)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatal("expected success")
	}
	if result.Status != "capture_pending" {
		t.Errorf("Status = %q, want capture_pending", result.Status)
	}
}

func TestCapture_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		json.NewEncoder(w).Encode(adyenErrorResponse{
			Status:    422,
			ErrorCode: "167",
			Message:   "Original pspReference required for this operation",
			ErrorType: "validation",
		})
	}))
	defer server.Close()

	p := configuredProvider(server.URL)
	_, err := p.Capture(context.Background(), "BAD-PSP", 1000)
	if err == nil {
		t.Fatal("expected error for API failure")
	}
}

// ---------------------------------------------------------------------------
// Tests: Refund
// ---------------------------------------------------------------------------

func TestRefund_NotConfigured(t *testing.T) {
	p := newTestProvider()
	_, err := p.Refund(context.Background(), processor.RefundRequest{
		TransactionID: "psp-1", Amount: 500,
	})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestRefund_EmptyTransactionID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer server.Close()
	p := configuredProvider(server.URL)

	_, err := p.Refund(context.Background(), processor.RefundRequest{
		TransactionID: "", Amount: 500,
	})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestRefund_ZeroAmount(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer server.Close()
	p := configuredProvider(server.URL)

	_, err := p.Refund(context.Background(), processor.RefundRequest{
		TransactionID: "psp-1", Amount: 0,
	})
	if err == nil {
		t.Fatal("expected error for zero amount")
	}
}

func TestRefund_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(adyenRefundResponse{
			PSPReference:        "REF-PSP-1",
			Status:              "received",
			PaymentPSPReference: "PSP-ORIG",
		})
	}))
	defer server.Close()

	p := configuredProvider(server.URL)
	result, err := p.Refund(context.Background(), processor.RefundRequest{
		TransactionID: "PSP-ORIG",
		Amount:        500,
		Metadata:      map[string]interface{}{"reason": "customer request"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatal("expected success")
	}
	if result.RefundID != "REF-PSP-1" {
		t.Errorf("RefundID = %q, want REF-PSP-1", result.RefundID)
	}
}

// ---------------------------------------------------------------------------
// Tests: GetTransaction
// ---------------------------------------------------------------------------

func TestGetTransaction_NotConfigured(t *testing.T) {
	p := newTestProvider()
	_, err := p.GetTransaction(context.Background(), "psp-1")
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
	p := newTestProvider()
	p.config = Config{APIKey: "key", MerchantAccount: "merch"}

	tx, err := p.GetTransaction(context.Background(), "PSP-999")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tx.ID != "PSP-999" {
		t.Errorf("ID = %q, want PSP-999", tx.ID)
	}
	if tx.Status != "unknown" {
		t.Errorf("Status = %q, want unknown", tx.Status)
	}
	if tx.Type != "charge" {
		t.Errorf("Type = %q, want charge", tx.Type)
	}
}

// ---------------------------------------------------------------------------
// Tests: ValidateWebhook
// ---------------------------------------------------------------------------

func TestValidateWebhook_NotConfigured(t *testing.T) {
	p := newTestProvider()
	_, err := p.ValidateWebhook(context.Background(), []byte("{}"), "")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestValidateWebhook_InvalidPayload(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer server.Close()
	p := configuredProvider(server.URL)

	_, err := p.ValidateWebhook(context.Background(), []byte("not-json"), "")
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestValidateWebhook_EmptyNotifications(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer server.Close()
	p := configuredProvider(server.URL)

	payload, _ := json.Marshal(adyenNotificationRequest{
		NotificationItems: []adyenNotificationItemWrap{},
	})
	_, err := p.ValidateWebhook(context.Background(), payload, "")
	if err == nil {
		t.Fatal("expected error for empty notifications")
	}
}

func TestValidateWebhook_MissingHMAC(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer server.Close()
	p := configuredProvider(server.URL)

	payload, _ := json.Marshal(adyenNotificationRequest{
		NotificationItems: []adyenNotificationItemWrap{{
			NotificationRequestItem: adyenNotificationItem{
				PSPReference:        "psp-1",
				MerchantAccountCode: "TestMerchant",
				EventCode:           "AUTHORISATION",
				Success:             "true",
				AdditionalData:      map[string]interface{}{},
			},
		}},
	})

	_, err := p.ValidateWebhook(context.Background(), payload, "")
	if err == nil {
		t.Fatal("expected error for missing HMAC")
	}
}

func TestValidateWebhook_InvalidHMAC(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer server.Close()
	p := configuredProvider(server.URL)

	payload, _ := json.Marshal(adyenNotificationRequest{
		NotificationItems: []adyenNotificationItemWrap{{
			NotificationRequestItem: adyenNotificationItem{
				PSPReference:        "psp-1",
				MerchantAccountCode: "TestMerchant",
				EventCode:           "AUTHORISATION",
				Success:             "true",
				AdditionalData: map[string]interface{}{
					"hmacSignature": "bad-signature",
				},
			},
		}},
	})

	_, err := p.ValidateWebhook(context.Background(), payload, "")
	if err == nil {
		t.Fatal("expected error for invalid HMAC")
	}
}

func TestValidateWebhook_MerchantMismatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer server.Close()
	p := configuredProvider(server.URL)
	p.config.HMACKey = "" // skip HMAC check

	payload, _ := json.Marshal(adyenNotificationRequest{
		NotificationItems: []adyenNotificationItemWrap{{
			NotificationRequestItem: adyenNotificationItem{
				PSPReference:        "psp-1",
				MerchantAccountCode: "WrongMerchant",
				EventCode:           "AUTHORISATION",
				Success:             "true",
			},
		}},
	})

	_, err := p.ValidateWebhook(context.Background(), payload, "")
	if err == nil {
		t.Fatal("expected error for merchant mismatch")
	}
}

func TestValidateWebhook_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer server.Close()
	p := configuredProvider(server.URL)

	// Compute valid HMAC.
	item := adyenNotificationItem{
		PSPReference:        "psp-1",
		MerchantReference:   "ref-1",
		Amount:              adyenAmount{Value: 1000, Currency: "EUR"},
		MerchantAccountCode: "TestMerchant",
		EventCode:           "AUTHORISATION",
		EventDate:           "2026-01-15T10:00:00+00:00",
		Success:             "true",
	}

	signingString := strings.Join([]string{
		item.PSPReference,
		item.MerchantReference,
		fmt.Sprintf("%d", item.Amount.Value),
		item.Amount.Currency,
		item.EventCode,
		item.Success,
	}, ":")

	keyBytes, _ := hex.DecodeString(p.config.HMACKey)
	mac := hmac.New(sha256.New, keyBytes)
	mac.Write([]byte(signingString))
	validSig := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	item.AdditionalData = map[string]interface{}{
		"hmacSignature": validSig,
	}

	payload, _ := json.Marshal(adyenNotificationRequest{
		Live:              "false",
		NotificationItems: []adyenNotificationItemWrap{{NotificationRequestItem: item}},
	})

	event, err := p.ValidateWebhook(context.Background(), payload, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if event.ID != "psp-1" {
		t.Errorf("event ID = %q, want psp-1", event.ID)
	}
	if event.Type != "payment.authorized" {
		t.Errorf("event Type = %q, want payment.authorized", event.Type)
	}
	if event.Processor != processor.Adyen {
		t.Errorf("Processor = %q, want adyen", event.Processor)
	}
}

func TestValidateWebhook_NoHMACKey_SkipsVerification(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer server.Close()
	p := configuredProvider(server.URL)
	p.config.HMACKey = "" // No HMAC = skip verification

	payload, _ := json.Marshal(adyenNotificationRequest{
		NotificationItems: []adyenNotificationItemWrap{{
			NotificationRequestItem: adyenNotificationItem{
				PSPReference:        "psp-2",
				MerchantAccountCode: "TestMerchant",
				EventCode:           "CAPTURE",
				EventDate:           "2026-01-15T10:00:00+00:00",
				Success:             "true",
			},
		}},
	})

	event, err := p.ValidateWebhook(context.Background(), payload, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if event.Type != "payment.captured" {
		t.Errorf("event Type = %q, want payment.captured", event.Type)
	}
}

func TestValidateWebhook_SignatureFromParameter(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer server.Close()
	p := configuredProvider(server.URL)

	item := adyenNotificationItem{
		PSPReference:        "psp-3",
		MerchantReference:   "",
		Amount:              adyenAmount{Value: 500, Currency: "USD"},
		MerchantAccountCode: "TestMerchant",
		EventCode:           "REFUND",
		Success:             "true",
	}

	signingString := strings.Join([]string{
		item.PSPReference, "", "500", "USD", "REFUND", "true",
	}, ":")

	keyBytes, _ := hex.DecodeString(p.config.HMACKey)
	mac := hmac.New(sha256.New, keyBytes)
	mac.Write([]byte(signingString))
	validSig := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	// No hmacSignature in additionalData; pass via signature parameter.
	payload, _ := json.Marshal(adyenNotificationRequest{
		NotificationItems: []adyenNotificationItemWrap{{NotificationRequestItem: item}},
	})

	event, err := p.ValidateWebhook(context.Background(), payload, validSig)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if event.Type != "refund.succeeded" {
		t.Errorf("event Type = %q, want refund.succeeded", event.Type)
	}
}

// ---------------------------------------------------------------------------
// Tests: post() error handling
// ---------------------------------------------------------------------------

func TestPost_HTTPError_StructuredAdyenError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		json.NewEncoder(w).Encode(adyenErrorResponse{
			Status:    403,
			ErrorCode: "010",
			Message:   "Not allowed",
			ErrorType: "security",
		})
	}))
	defer server.Close()

	p := configuredProvider(server.URL)
	var resp adyenPaymentResponse
	err := p.post(context.Background(), "/payments", map[string]string{"test": "yes"}, &resp)
	if err == nil {
		t.Fatal("expected error for 403")
	}
	if pe, ok := err.(*processor.PaymentError); ok {
		if pe.Code != "010" {
			t.Errorf("code = %q, want 010", pe.Code)
		}
	}
}

func TestPost_HTTPError_UnstructuredBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		w.Write([]byte("gateway timeout"))
	}))
	defer server.Close()

	p := configuredProvider(server.URL)
	var resp adyenPaymentResponse
	err := p.post(context.Background(), "/payments", map[string]string{}, &resp)
	if err == nil {
		t.Fatal("expected error for 502")
	}
	if pe, ok := err.(*processor.PaymentError); ok {
		if pe.Code != "HTTP_502" {
			t.Errorf("code = %q, want HTTP_502", pe.Code)
		}
	}
}
