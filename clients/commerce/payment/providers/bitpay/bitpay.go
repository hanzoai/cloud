// Package bitpay implements the BitPay payment processor for Commerce.
// Uses the BitPay REST API v2 directly (no SDK dependency).
package bitpay

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
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
	testBaseURL    = "https://test.bitpay.com"
	prodBaseURL    = "https://bitpay.com"
	defaultTimeout = 30 * time.Second
)

// Config holds BitPay API credentials.
type Config struct {
	APIToken    string
	Environment string // "test" or "prod"
}

// Provider implements processor.PaymentProcessor for BitPay API v2.
type Provider struct {
	*processor.BaseProcessor
	apiToken    string
	environment string
	client      *http.Client
}

func init() {
	token := os.Getenv("BITPAY_API_TOKEN")
	env := os.Getenv("BITPAY_ENVIRONMENT")
	if env == "" {
		env = "test"
	}

	p := &Provider{
		BaseProcessor: processor.NewBaseProcessor(processor.BitPay, supportedCurrencies()),
		apiToken:      token,
		environment:   env,
		client:        &http.Client{Timeout: defaultTimeout},
	}

	if token != "" {
		p.SetConfigured(true)
	}

	processor.Register(p)
}

// NewProvider creates a configured BitPay provider instance.
func NewProvider(cfg Config) *Provider {
	env := cfg.Environment
	if env == "" {
		env = "test"
	}
	p := &Provider{
		BaseProcessor: processor.NewBaseProcessor(processor.BitPay, supportedCurrencies()),
		apiToken:      cfg.APIToken,
		environment:   env,
		client:        &http.Client{Timeout: defaultTimeout},
	}
	if cfg.APIToken != "" {
		p.SetConfigured(true)
	}
	return p
}

func supportedCurrencies() []currency.Type {
	return []currency.Type{
		currency.BTC, "bch", currency.ETH, "usdc", "gusd", "pax", "busd",
		"xrp", "doge", "ltc", "shib",
	}
}

// Type returns the processor type.
func (p *Provider) Type() processor.ProcessorType {
	return processor.BitPay
}

// IsAvailable reports whether the processor is configured.
func (p *Provider) IsAvailable(ctx context.Context) bool {
	return p.apiToken != ""
}

// SupportedCurrencies returns currencies this processor supports.
func (p *Provider) SupportedCurrencies() []currency.Type {
	return supportedCurrencies()
}

func (p *Provider) baseURL() string {
	if p.environment == "prod" {
		return prodBaseURL
	}
	return testBaseURL
}

// ---------------------------------------------------------------------------
// PaymentProcessor
// ---------------------------------------------------------------------------

// Charge creates a BitPay invoice.
func (p *Provider) Charge(ctx context.Context, req processor.PaymentRequest) (*processor.PaymentResult, error) {
	if err := p.ensureAvailable(); err != nil {
		return nil, err
	}
	if err := processor.ValidateRequest(req); err != nil {
		return nil, err
	}

	body := invoiceRequest{
		Price:    float64(req.Amount) / 100, // cents to major unit
		Currency: strings.ToUpper(string(req.Currency)),
		Token:    p.apiToken,
	}

	if req.OrderID != "" {
		body.OrderID = req.OrderID
	}
	if req.Description != "" {
		body.ItemDesc = req.Description
	}
	if req.CustomerID != "" {
		body.Buyer.Email = req.CustomerID
	}
	if email, ok := req.Metadata["email"].(string); ok {
		body.Buyer.Email = email
	}
	if name, ok := req.Metadata["name"].(string); ok {
		body.Buyer.Name = name
	}
	if cbURL, ok := req.Options["notificationURL"].(string); ok {
		body.NotificationURL = cbURL
	}
	if redirURL, ok := req.Options["redirectURL"].(string); ok {
		body.RedirectURL = redirURL
	}

	var resp invoiceResponse
	if err := p.post(ctx, "/invoices", body, &resp); err != nil {
		return &processor.PaymentResult{
			Success:      false,
			ErrorMessage: err.Error(),
			Error:        err,
		}, err
	}

	return &processor.PaymentResult{
		Success:       true,
		TransactionID: resp.Data.ID,
		ProcessorRef:  resp.Data.ID,
		Status:        mapInvoiceStatus(resp.Data.Status),
		Metadata: map[string]interface{}{
			"invoiceUrl":    resp.Data.URL,
			"bitpayStatus":  resp.Data.Status,
			"expirationTime": resp.Data.ExpirationTime,
		},
	}, nil
}

// GetTransaction retrieves a BitPay invoice by ID.
func (p *Provider) GetTransaction(ctx context.Context, txID string) (*processor.Transaction, error) {
	if err := p.ensureAvailable(); err != nil {
		return nil, err
	}
	if txID == "" {
		return nil, processor.NewPaymentError(processor.BitPay, "INVALID_TRANSACTION",
			"transaction ID is required", nil)
	}

	var resp invoiceResponse
	if err := p.get(ctx, "/invoices/"+txID, &resp); err != nil {
		return nil, err
	}

	return &processor.Transaction{
		ID:           resp.Data.ID,
		ProcessorRef: resp.Data.ID,
		Type:         "invoice",
		Amount:       currency.Cents(resp.Data.Price * 100),
		Currency:     currency.Type(strings.ToLower(resp.Data.Currency)),
		Status:       mapInvoiceStatus(resp.Data.Status),
		CreatedAt:    resp.Data.InvoiceTime / 1000, // ms to s
		UpdatedAt:    resp.Data.CurrentTime / 1000,
		Metadata: map[string]interface{}{
			"bitpayStatus": resp.Data.Status,
			"orderId":      resp.Data.OrderID,
		},
	}, nil
}

// Refund creates a BitPay refund request.
func (p *Provider) Refund(ctx context.Context, req processor.RefundRequest) (*processor.RefundResult, error) {
	if err := p.ensureAvailable(); err != nil {
		return nil, err
	}
	if req.TransactionID == "" {
		return nil, processor.NewPaymentError(processor.BitPay, "INVALID_TRANSACTION",
			"transaction ID is required for refund", nil)
	}
	if req.Amount <= 0 {
		return nil, processor.NewPaymentError(processor.BitPay, "INVALID_AMOUNT",
			"refund amount must be positive", nil)
	}

	body := refundRequest{
		Token:     p.apiToken,
		InvoiceID: req.TransactionID,
		Amount:    float64(req.Amount) / 100,
		Preview:   false,
		Immediate: false,
	}

	var resp refundResponse
	if err := p.post(ctx, "/refunds", body, &resp); err != nil {
		return &processor.RefundResult{
			Success:      false,
			ErrorMessage: err.Error(),
			Error:        err,
		}, err
	}

	return &processor.RefundResult{
		Success:      resp.Data.Status == "created" || resp.Data.Status == "pending",
		RefundID:     resp.Data.ID,
		ProcessorRef: resp.Data.ID,
	}, nil
}

// ValidateWebhook verifies a BitPay webhook signature (HMAC-SHA256 with API token).
func (p *Provider) ValidateWebhook(ctx context.Context, payload []byte, signature string) (*processor.WebhookEvent, error) {
	if p.apiToken == "" {
		return nil, processor.ErrWebhookValidationFailed
	}
	if signature == "" {
		return nil, processor.ErrWebhookValidationFailed
	}

	mac := hmac.New(sha256.New, []byte(p.apiToken))
	mac.Write(payload)
	expectedSig := hex.EncodeToString(mac.Sum(nil))

	if !hmac.Equal([]byte(signature), []byte(expectedSig)) {
		return nil, processor.ErrWebhookValidationFailed
	}

	var evt webhookPayload
	if err := json.Unmarshal(payload, &evt); err != nil {
		return nil, fmt.Errorf("failed to parse bitpay webhook: %w", err)
	}

	return &processor.WebhookEvent{
		ID:        evt.Data.ID,
		Type:      mapWebhookEventType(evt.Event.Name),
		Processor: processor.BitPay,
		Data: map[string]interface{}{
			"invoiceId": evt.Data.ID,
			"status":    evt.Data.Status,
			"price":     evt.Data.Price,
			"currency":  evt.Data.Currency,
			"orderId":   evt.Data.OrderID,
		},
		Timestamp: time.Now().Unix(),
	}, nil
}

// ---------------------------------------------------------------------------
// HTTP helpers
// ---------------------------------------------------------------------------

func (p *Provider) post(ctx context.Context, path string, body interface{}, result interface{}) error {
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return processor.NewPaymentError(processor.BitPay, "MARSHAL_ERROR",
			"failed to marshal request body", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL()+path, bytes.NewReader(jsonBody))
	if err != nil {
		return processor.NewPaymentError(processor.BitPay, "REQUEST_ERROR",
			"failed to create HTTP request", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-Accept-Version", "2.0.0")
	httpReq.Header.Set("X-Identity", "")

	return p.doRequest(httpReq, result)
}

func (p *Provider) get(ctx context.Context, path string, result interface{}) error {
	u := p.baseURL() + path + "?token=" + p.apiToken

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return processor.NewPaymentError(processor.BitPay, "REQUEST_ERROR",
			"failed to create HTTP request", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-Accept-Version", "2.0.0")

	return p.doRequest(httpReq, result)
}

func (p *Provider) doRequest(req *http.Request, result interface{}) error {
	resp, err := p.client.Do(req)
	if err != nil {
		return processor.NewPaymentError(processor.BitPay, "NETWORK_ERROR",
			"failed to send request to BitPay", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return processor.NewPaymentError(processor.BitPay, "READ_ERROR",
			"failed to read BitPay response", err)
	}

	if resp.StatusCode >= 400 {
		var apiErr bitpayErrorResponse
		if json.Unmarshal(body, &apiErr) == nil && apiErr.Error != "" {
			return processor.NewPaymentError(processor.BitPay, fmt.Sprintf("HTTP_%d", resp.StatusCode),
				fmt.Sprintf("bitpay API error: %s", apiErr.Error), nil)
		}
		return processor.NewPaymentError(processor.BitPay, fmt.Sprintf("HTTP_%d", resp.StatusCode),
			fmt.Sprintf("bitpay API error (HTTP %d): %s", resp.StatusCode, string(body)), nil)
	}

	if result != nil {
		if err := json.Unmarshal(body, result); err != nil {
			return processor.NewPaymentError(processor.BitPay, "DECODE_ERROR",
				"failed to decode BitPay response", err)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// BitPay API types
// ---------------------------------------------------------------------------

type invoiceBuyer struct {
	Email string `json:"email,omitempty"`
	Name  string `json:"name,omitempty"`
}

type invoiceRequest struct {
	Token           string       `json:"token"`
	Price           float64      `json:"price"`
	Currency        string       `json:"currency"`
	OrderID         string       `json:"orderId,omitempty"`
	ItemDesc        string       `json:"itemDesc,omitempty"`
	Buyer           invoiceBuyer `json:"buyer,omitempty"`
	NotificationURL string       `json:"notificationURL,omitempty"`
	RedirectURL     string       `json:"redirectURL,omitempty"`
}

type invoiceData struct {
	ID             string  `json:"id"`
	URL            string  `json:"url"`
	Status         string  `json:"status"`
	Price          float64 `json:"price"`
	Currency       string  `json:"currency"`
	OrderID        string  `json:"orderId"`
	InvoiceTime    int64   `json:"invoiceTime"`
	ExpirationTime int64   `json:"expirationTime"`
	CurrentTime    int64   `json:"currentTime"`
}

type invoiceResponse struct {
	Data invoiceData `json:"data"`
}

type refundRequest struct {
	Token     string  `json:"token"`
	InvoiceID string  `json:"invoiceId"`
	Amount    float64 `json:"amount"`
	Preview   bool    `json:"preview"`
	Immediate bool    `json:"immediate"`
}

type refundData struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

type refundResponse struct {
	Data refundData `json:"data"`
}

type webhookPayload struct {
	Event struct {
		Name string `json:"name"`
	} `json:"event"`
	Data invoiceData `json:"data"`
}

type bitpayErrorResponse struct {
	Error string `json:"error"`
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func (p *Provider) ensureAvailable() error {
	if p.apiToken == "" {
		return processor.NewPaymentError(processor.BitPay, "NOT_CONFIGURED",
			"bitpay processor not configured", nil)
	}
	return nil
}

func mapInvoiceStatus(status string) string {
	switch status {
	case "new":
		return "pending"
	case "paid":
		return "paid"
	case "confirmed":
		return "confirmed"
	case "complete":
		return "succeeded"
	case "expired":
		return "expired"
	case "invalid":
		return "failed"
	default:
		return status
	}
}

func mapWebhookEventType(name string) string {
	switch name {
	case "invoice_paidInFull":
		return "payment.completed"
	case "invoice_confirmed":
		return "payment.confirmed"
	case "invoice_completed":
		return "payment.completed"
	case "invoice_expired":
		return "payment.expired"
	case "invoice_failedToConfirm":
		return "payment.failed"
	case "invoice_declined":
		return "payment.declined"
	case "invoice_refundComplete":
		return "refund.succeeded"
	default:
		return "bitpay." + name
	}
}

// Compile-time interface check.
var _ processor.PaymentProcessor = (*Provider)(nil)
