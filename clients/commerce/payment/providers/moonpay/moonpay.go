// Package moonpay implements the MoonPay on-ramp processor for Commerce.
// Uses the MoonPay REST API v3 directly (no SDK dependency).
// MoonPay is a fiat-to-crypto on-ramp — Charge returns a widget URL
// for the customer to complete KYC and payment.
package moonpay

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/payment/processor"
)

const (
	baseURL        = "https://api.moonpay.com"
	defaultTimeout = 30 * time.Second
)

// Config holds MoonPay API credentials.
type Config struct {
	APIKey     string
	SecretKey  string
	WebhookKey string
}

// Provider implements PaymentProcessor for MoonPay.
type Provider struct {
	*processor.BaseProcessor
	apiKey     string
	secretKey  string
	webhookKey string
	client     *http.Client
}

// NewProvider creates a configured MoonPay provider instance.
func NewProvider(cfg Config) *Provider {
	p := &Provider{
		BaseProcessor: processor.NewBaseProcessor(processor.MoonPay, supportedCurrencies()),
		apiKey:        cfg.APIKey,
		secretKey:     cfg.SecretKey,
		webhookKey:    cfg.WebhookKey,
		client:        &http.Client{Timeout: defaultTimeout},
	}
	if cfg.APIKey != "" && cfg.SecretKey != "" {
		p.SetConfigured(true)
	}
	return p
}

func init() {
	apiKey := os.Getenv("MOONPAY_API_KEY")
	secretKey := os.Getenv("MOONPAY_SECRET_KEY")
	webhookKey := os.Getenv("MOONPAY_WEBHOOK_KEY")

	p := &Provider{
		BaseProcessor: processor.NewBaseProcessor(processor.MoonPay, supportedCurrencies()),
		apiKey:        apiKey,
		secretKey:     secretKey,
		webhookKey:    webhookKey,
		client:        &http.Client{Timeout: defaultTimeout},
	}
	if apiKey != "" && secretKey != "" {
		p.SetConfigured(true)
	}
	processor.Register(p)
}

func supportedCurrencies() []currency.Type {
	return []currency.Type{
		currency.BTC, currency.ETH, "sol", "usdc", "usdt",
		"matic", "avax", "lux",
		// MoonPay supports 80+ currencies; these are the most common.
		// Fiat input currencies (the baseCurrency in MoonPay terms):
		currency.USD, currency.EUR, currency.GBP, currency.CAD, currency.AUD,
	}
}

// ---------------------------------------------------------------------------
// PaymentProcessor
// ---------------------------------------------------------------------------

// Charge creates a MoonPay buy transaction (fiat to crypto on-ramp).
// Returns a widget URL for the customer to complete KYC and payment.
func (p *Provider) Charge(ctx context.Context, req processor.PaymentRequest) (*processor.PaymentResult, error) {
	if err := processor.ValidateRequest(req); err != nil {
		return nil, err
	}

	body := map[string]interface{}{
		"apiKey":             p.apiKey,
		"baseCurrencyAmount": float64(req.Amount) / 100.0, // Convert cents to dollars
	}

	// Determine base and crypto currencies from the request
	cryptoCurrency := "eth" // default
	baseCurrency := "usd"   // default

	if processor.IsCryptoCurrency(req.Currency) {
		cryptoCurrency = string(req.Currency)
	} else {
		baseCurrency = string(req.Currency)
	}

	body["currencyCode"] = cryptoCurrency
	body["baseCurrencyCode"] = baseCurrency

	// Wallet address from request metadata or Options
	if req.Address != "" {
		body["walletAddress"] = req.Address
	} else if addr, ok := req.Options["walletAddress"].(string); ok {
		body["walletAddress"] = addr
	}

	if req.CustomerID != "" {
		body["externalCustomerId"] = req.CustomerID
	}

	var resp moonpayTransaction
	if err := p.post(ctx, "/v3/transactions", body, &resp); err != nil {
		return &processor.PaymentResult{
			Success:      false,
			ErrorMessage: err.Error(),
			Error:        err,
		}, err
	}

	// Build the widget URL for the customer to complete the purchase
	widgetURL := p.buildWidgetURL(cryptoCurrency, baseCurrency, req)

	return &processor.PaymentResult{
		Success:       true,
		TransactionID: resp.ID,
		ProcessorRef:  resp.ID,
		Status:        mapStatus(resp.Status),
		Metadata: map[string]interface{}{
			"widget_url":      widgetURL,
			"crypto_currency": resp.CurrencyCode,
			"base_currency":   resp.BaseCurrencyCode,
			"status":          resp.Status,
		},
	}, nil
}

// Authorize is not supported for MoonPay on-ramp.
func (p *Provider) Authorize(ctx context.Context, req processor.PaymentRequest) (*processor.PaymentResult, error) {
	return nil, processor.NewPaymentError(processor.MoonPay, "NOT_SUPPORTED", "authorize not supported for on-ramp transactions", nil)
}

// Capture is not supported for MoonPay on-ramp.
func (p *Provider) Capture(ctx context.Context, transactionID string, amount currency.Cents) (*processor.PaymentResult, error) {
	return nil, processor.NewPaymentError(processor.MoonPay, "NOT_SUPPORTED", "capture not supported for on-ramp transactions", nil)
}

// Refund is not supported for MoonPay crypto purchases.
func (p *Provider) Refund(ctx context.Context, req processor.RefundRequest) (*processor.RefundResult, error) {
	return &processor.RefundResult{
		Success:      false,
		ErrorMessage: "refunds not supported for MoonPay on-ramp purchases",
	}, processor.NewPaymentError(processor.MoonPay, "NOT_SUPPORTED", "refunds not supported for on-ramp purchases", nil)
}

// GetTransaction retrieves a MoonPay transaction by ID.
func (p *Provider) GetTransaction(ctx context.Context, txID string) (*processor.Transaction, error) {
	var resp moonpayTransaction
	if err := p.get(ctx, "/v3/transactions/"+txID, &resp); err != nil {
		return nil, err
	}

	return &processor.Transaction{
		ID:           resp.ID,
		ProcessorRef: resp.ID,
		Type:         "onramp",
		Amount:       currency.Cents(resp.BaseCurrencyAmount * 100),
		Currency:     currency.Type(resp.BaseCurrencyCode),
		Status:       mapStatus(resp.Status),
		CreatedAt:    parseTime(resp.CreatedAt),
		UpdatedAt:    parseTime(resp.UpdatedAt),
		Metadata: map[string]interface{}{
			"crypto_currency":    resp.CurrencyCode,
			"crypto_amount":      resp.QuoteCurrencyAmount,
			"wallet_address":     resp.WalletAddress,
			"failure_reason":     resp.FailureReason,
			"base_currency_code": resp.BaseCurrencyCode,
		},
	}, nil
}

// ValidateWebhook verifies a MoonPay webhook signature (HMAC-SHA256).
func (p *Provider) ValidateWebhook(ctx context.Context, payload []byte, signature string) (*processor.WebhookEvent, error) {
	if p.webhookKey == "" {
		return nil, processor.ErrWebhookValidationFailed
	}

	// MoonPay signs webhooks with HMAC-SHA256 using the webhook key
	mac := hmac.New(sha256.New, []byte(p.webhookKey))
	mac.Write(payload)
	expectedSig := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	if !hmac.Equal([]byte(signature), []byte(expectedSig)) {
		return nil, processor.ErrWebhookValidationFailed
	}

	var event moonpayWebhookEvent
	if err := json.Unmarshal(payload, &event); err != nil {
		return nil, fmt.Errorf("failed to parse moonpay webhook: %w", err)
	}

	return &processor.WebhookEvent{
		ID:        event.Data.ID,
		Type:      mapWebhookType(event.Type),
		Processor: processor.MoonPay,
		Data: map[string]interface{}{
			"transaction_id":   event.Data.ID,
			"status":           event.Data.Status,
			"crypto_currency":  event.Data.CurrencyCode,
			"wallet_address":   event.Data.WalletAddress,
			"failure_reason":   event.Data.FailureReason,
		},
		Timestamp: parseTime(event.Data.UpdatedAt),
	}, nil
}

// ---------------------------------------------------------------------------
// HTTP helpers
// ---------------------------------------------------------------------------

func (p *Provider) post(ctx context.Context, path string, body interface{}, result interface{}) error {
	data, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("moonpay marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	return p.doRequest(req, result)
}

func (p *Provider) get(ctx context.Context, path string, result interface{}) error {
	u := baseURL + path
	if !strings.Contains(u, "?") {
		u += "?apiKey=" + p.apiKey
	} else {
		u += "&apiKey=" + p.apiKey
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	return p.doRequest(req, result)
}

func (p *Provider) doRequest(req *http.Request, result interface{}) error {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	// MoonPay uses API key in query params for GET, and in body for POST.
	// For authenticated endpoints, add the secret key header.
	if p.secretKey != "" {
		req.Header.Set("Authorization", "Api-Key "+p.secretKey)
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("moonpay request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("moonpay read body: %w", err)
	}

	if resp.StatusCode >= 400 {
		var apiErr moonpayAPIError
		if json.Unmarshal(body, &apiErr) == nil && apiErr.Message != "" {
			return processor.NewPaymentError(
				processor.MoonPay,
				apiErr.Type,
				apiErr.Message,
				nil,
			)
		}
		return fmt.Errorf("moonpay API error (HTTP %d): %s", resp.StatusCode, string(body))
	}

	if result != nil {
		if err := json.Unmarshal(body, result); err != nil {
			return fmt.Errorf("moonpay decode response: %w", err)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// URL signing
// ---------------------------------------------------------------------------

// signURL signs a MoonPay widget URL with the secret key (HMAC-SHA256).
func (p *Provider) signURL(rawURL string) string {
	if p.secretKey == "" {
		return rawURL
	}
	// MoonPay signs the query string portion of the URL
	parts := strings.SplitN(rawURL, "?", 2)
	if len(parts) < 2 {
		return rawURL
	}
	mac := hmac.New(sha256.New, []byte(p.secretKey))
	mac.Write([]byte("?" + parts[1]))
	sig := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	return rawURL + "&signature=" + sig
}

// buildWidgetURL constructs a signed MoonPay widget URL.
func (p *Provider) buildWidgetURL(cryptoCurrency, baseCurrency string, req processor.PaymentRequest) string {
	u := fmt.Sprintf("https://buy.moonpay.com?apiKey=%s&currencyCode=%s&baseCurrencyCode=%s&baseCurrencyAmount=%s",
		p.apiKey, cryptoCurrency, baseCurrency, fmt.Sprintf("%.2f", float64(req.Amount)/100.0))

	if req.Address != "" {
		u += "&walletAddress=" + req.Address
	} else if addr, ok := req.Options["walletAddress"].(string); ok {
		u += "&walletAddress=" + addr
	}

	if req.CustomerID != "" {
		u += "&externalCustomerId=" + req.CustomerID
	}

	return p.signURL(u)
}

// ---------------------------------------------------------------------------
// MoonPay API types
// ---------------------------------------------------------------------------

type moonpayTransaction struct {
	ID                  string  `json:"id"`
	Status              string  `json:"status"` // waitingPayment, pending, completed, failed
	CurrencyCode        string  `json:"currencyCode"`
	BaseCurrencyCode    string  `json:"baseCurrencyCode"`
	BaseCurrencyAmount  float64 `json:"baseCurrencyAmount"`
	QuoteCurrencyAmount float64 `json:"quoteCurrencyAmount"`
	WalletAddress       string  `json:"walletAddress"`
	FailureReason       string  `json:"failureReason"`
	CreatedAt           string  `json:"createdAt"`
	UpdatedAt           string  `json:"updatedAt"`
}

type moonpayWebhookEvent struct {
	Type string `json:"type"`
	Data struct {
		ID            string `json:"id"`
		Status        string `json:"status"`
		CurrencyCode  string `json:"currencyCode"`
		WalletAddress string `json:"walletAddress"`
		FailureReason string `json:"failureReason"`
		UpdatedAt     string `json:"updatedAt"`
	} `json:"data"`
}

type moonpayAPIError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func mapStatus(status string) string {
	switch status {
	case "completed":
		return "completed"
	case "pending":
		return "pending"
	case "waitingPayment":
		return "awaiting_payment"
	case "failed":
		return "failed"
	default:
		return status
	}
}

func mapWebhookType(t string) string {
	switch t {
	case "transaction_created":
		return "payment.created"
	case "transaction_updated":
		return "payment.updated"
	case "transaction_completed":
		return "payment.completed"
	case "transaction_failed":
		return "payment.failed"
	default:
		return t
	}
}

func parseTime(s string) int64 {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return 0
	}
	return t.Unix()
}

// Compile-time interface check.
var _ processor.PaymentProcessor = (*Provider)(nil)
