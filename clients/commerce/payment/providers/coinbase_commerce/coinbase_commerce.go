// Package coinbase_commerce implements the Coinbase Commerce payment processor for Commerce.
// Uses the Coinbase Commerce API v2 directly (no SDK dependency).
package coinbase_commerce

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
	baseURL        = "https://api.commerce.coinbase.com"
	defaultTimeout = 30 * time.Second
)

// Config holds Coinbase Commerce API credentials.
type Config struct {
	APIKey        string
	WebhookSecret string
}

// Provider implements processor.PaymentProcessor for Coinbase Commerce API v2.
type Provider struct {
	*processor.BaseProcessor
	apiKey        string
	webhookSecret string
	client        *http.Client
}

func init() {
	apiKey := os.Getenv("COINBASE_COMMERCE_API_KEY")
	webhookSecret := os.Getenv("COINBASE_COMMERCE_WEBHOOK_SECRET")

	p := &Provider{
		BaseProcessor: processor.NewBaseProcessor(processor.CoinbaseCommerce, supportedCurrencies()),
		apiKey:        apiKey,
		webhookSecret: webhookSecret,
		client:        &http.Client{Timeout: defaultTimeout},
	}

	if apiKey != "" {
		p.SetConfigured(true)
	}

	processor.Register(p)
}

// NewProvider creates a configured Coinbase Commerce provider instance.
func NewProvider(cfg Config) *Provider {
	p := &Provider{
		BaseProcessor: processor.NewBaseProcessor(processor.CoinbaseCommerce, supportedCurrencies()),
		apiKey:        cfg.APIKey,
		webhookSecret: cfg.WebhookSecret,
		client:        &http.Client{Timeout: defaultTimeout},
	}
	if cfg.APIKey != "" {
		p.SetConfigured(true)
	}
	return p
}

func supportedCurrencies() []currency.Type {
	return []currency.Type{
		currency.BTC, currency.ETH, "ltc", "usdc", "dai", "doge", "shib", "ape",
	}
}

// Type returns the processor type.
func (p *Provider) Type() processor.ProcessorType {
	return processor.CoinbaseCommerce
}

// IsAvailable reports whether the processor is configured.
func (p *Provider) IsAvailable(ctx context.Context) bool {
	return p.apiKey != ""
}

// SupportedCurrencies returns currencies this processor supports.
func (p *Provider) SupportedCurrencies() []currency.Type {
	return supportedCurrencies()
}

// ---------------------------------------------------------------------------
// PaymentProcessor
// ---------------------------------------------------------------------------

// Charge creates a Coinbase Commerce charge.
func (p *Provider) Charge(ctx context.Context, req processor.PaymentRequest) (*processor.PaymentResult, error) {
	if err := p.ensureAvailable(); err != nil {
		return nil, err
	}
	if err := processor.ValidateRequest(req); err != nil {
		return nil, err
	}

	name := req.Description
	if name == "" {
		name = "Payment"
	}

	body := chargeRequest{
		Name:        name,
		Description: req.Description,
		PricingType: "fixed_price",
		LocalPrice: chargePrice{
			Amount:   fmt.Sprintf("%.2f", float64(req.Amount)/100),
			Currency: strings.ToUpper(string(req.Currency)),
		},
	}

	if req.OrderID != "" {
		body.Metadata.OrderID = req.OrderID
	}
	if req.CustomerID != "" {
		body.Metadata.CustomerID = req.CustomerID
	}
	if cbURL, ok := req.Options["redirectURL"].(string); ok {
		body.RedirectURL = cbURL
	}
	if cancelURL, ok := req.Options["cancelURL"].(string); ok {
		body.CancelURL = cancelURL
	}

	var resp chargeResponseWrapper
	if err := p.post(ctx, "/charges", body, &resp); err != nil {
		return &processor.PaymentResult{
			Success:      false,
			ErrorMessage: err.Error(),
			Error:        err,
		}, err
	}

	return &processor.PaymentResult{
		Success:       true,
		TransactionID: resp.Data.ID,
		ProcessorRef:  resp.Data.Code,
		Status:        mapChargeStatus(resp.Data.Timeline),
		Metadata: map[string]interface{}{
			"hostedURL":  resp.Data.HostedURL,
			"code":       resp.Data.Code,
			"expiresAt":  resp.Data.ExpiresAt,
		},
	}, nil
}

// GetTransaction retrieves a Coinbase Commerce charge by ID.
func (p *Provider) GetTransaction(ctx context.Context, txID string) (*processor.Transaction, error) {
	if err := p.ensureAvailable(); err != nil {
		return nil, err
	}
	if txID == "" {
		return nil, processor.NewPaymentError(processor.CoinbaseCommerce, "INVALID_TRANSACTION",
			"transaction ID is required", nil)
	}

	var resp chargeResponseWrapper
	if err := p.get(ctx, "/charges/"+txID, &resp); err != nil {
		return nil, err
	}

	status := mapChargeStatus(resp.Data.Timeline)

	var createdAt int64
	if t, err := time.Parse(time.RFC3339, resp.Data.CreatedAt); err == nil {
		createdAt = t.Unix()
	}

	return &processor.Transaction{
		ID:           resp.Data.ID,
		ProcessorRef: resp.Data.Code,
		Type:         "charge",
		Status:       status,
		CreatedAt:    createdAt,
		UpdatedAt:    createdAt,
		Metadata: map[string]interface{}{
			"code":      resp.Data.Code,
			"hostedURL": resp.Data.HostedURL,
			"timeline":  resp.Data.Timeline,
		},
	}, nil
}

// Refund is not directly supported by Coinbase Commerce. Returns an error
// instructing the caller to process refunds manually through the Coinbase
// Commerce dashboard.
func (p *Provider) Refund(ctx context.Context, req processor.RefundRequest) (*processor.RefundResult, error) {
	return &processor.RefundResult{
		Success:      false,
		ErrorMessage: "coinbase commerce does not support programmatic refunds; process refunds manually via the Coinbase Commerce dashboard",
	}, processor.NewPaymentError(processor.CoinbaseCommerce, "REFUND_NOT_SUPPORTED",
		"coinbase commerce does not support programmatic refunds; process refunds manually via the Coinbase Commerce dashboard", nil)
}

// ValidateWebhook verifies a Coinbase Commerce webhook signature
// (X-CC-Webhook-Signature, HMAC-SHA256 with webhook secret).
func (p *Provider) ValidateWebhook(ctx context.Context, payload []byte, signature string) (*processor.WebhookEvent, error) {
	if p.webhookSecret == "" {
		return nil, processor.ErrWebhookValidationFailed
	}
	if signature == "" {
		return nil, processor.ErrWebhookValidationFailed
	}

	mac := hmac.New(sha256.New, []byte(p.webhookSecret))
	mac.Write(payload)
	expectedSig := hex.EncodeToString(mac.Sum(nil))

	if !hmac.Equal([]byte(signature), []byte(expectedSig)) {
		return nil, processor.ErrWebhookValidationFailed
	}

	var evt webhookPayload
	if err := json.Unmarshal(payload, &evt); err != nil {
		return nil, fmt.Errorf("failed to parse coinbase commerce webhook: %w", err)
	}

	return &processor.WebhookEvent{
		ID:        evt.Event.ID,
		Type:      mapWebhookEventType(evt.Event.Type),
		Processor: processor.CoinbaseCommerce,
		Data: map[string]interface{}{
			"chargeId":   evt.Event.Data.ID,
			"chargeCode": evt.Event.Data.Code,
			"timeline":   evt.Event.Data.Timeline,
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
		return processor.NewPaymentError(processor.CoinbaseCommerce, "MARSHAL_ERROR",
			"failed to marshal request body", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+path, bytes.NewReader(jsonBody))
	if err != nil {
		return processor.NewPaymentError(processor.CoinbaseCommerce, "REQUEST_ERROR",
			"failed to create HTTP request", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-CC-Api-Key", p.apiKey)
	httpReq.Header.Set("X-CC-Version", "2018-03-22")

	return p.doRequest(httpReq, result)
}

func (p *Provider) get(ctx context.Context, path string, result interface{}) error {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+path, nil)
	if err != nil {
		return processor.NewPaymentError(processor.CoinbaseCommerce, "REQUEST_ERROR",
			"failed to create HTTP request", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-CC-Api-Key", p.apiKey)
	httpReq.Header.Set("X-CC-Version", "2018-03-22")

	return p.doRequest(httpReq, result)
}

func (p *Provider) doRequest(req *http.Request, result interface{}) error {
	resp, err := p.client.Do(req)
	if err != nil {
		return processor.NewPaymentError(processor.CoinbaseCommerce, "NETWORK_ERROR",
			"failed to send request to Coinbase Commerce", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return processor.NewPaymentError(processor.CoinbaseCommerce, "READ_ERROR",
			"failed to read Coinbase Commerce response", err)
	}

	if resp.StatusCode >= 400 {
		var apiErr cbcErrorResponse
		if json.Unmarshal(body, &apiErr) == nil && apiErr.Error.Message != "" {
			return processor.NewPaymentError(processor.CoinbaseCommerce, apiErr.Error.Type,
				fmt.Sprintf("coinbase commerce API error: %s", apiErr.Error.Message), nil)
		}
		return processor.NewPaymentError(processor.CoinbaseCommerce, fmt.Sprintf("HTTP_%d", resp.StatusCode),
			fmt.Sprintf("coinbase commerce API error (HTTP %d): %s", resp.StatusCode, string(body)), nil)
	}

	if result != nil {
		if err := json.Unmarshal(body, result); err != nil {
			return processor.NewPaymentError(processor.CoinbaseCommerce, "DECODE_ERROR",
				"failed to decode Coinbase Commerce response", err)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Coinbase Commerce API types
// ---------------------------------------------------------------------------

type chargePrice struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

type chargeMetadata struct {
	OrderID    string `json:"order_id,omitempty"`
	CustomerID string `json:"customer_id,omitempty"`
}

type chargeRequest struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	PricingType string         `json:"pricing_type"`
	LocalPrice  chargePrice    `json:"local_price"`
	Metadata    chargeMetadata `json:"metadata,omitempty"`
	RedirectURL string         `json:"redirect_url,omitempty"`
	CancelURL   string         `json:"cancel_url,omitempty"`
}

type timelineEntry struct {
	Status string `json:"status"`
	Time   string `json:"time"`
}

type chargeData struct {
	ID        string          `json:"id"`
	Code      string          `json:"code"`
	HostedURL string          `json:"hosted_url"`
	CreatedAt string          `json:"created_at"`
	ExpiresAt string          `json:"expires_at"`
	Timeline  []timelineEntry `json:"timeline"`
}

type chargeResponseWrapper struct {
	Data chargeData `json:"data"`
}

type webhookEvent struct {
	ID   string     `json:"id"`
	Type string     `json:"type"`
	Data chargeData `json:"data"`
}

type webhookPayload struct {
	Event webhookEvent `json:"event"`
}

type cbcErrorResponse struct {
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func (p *Provider) ensureAvailable() error {
	if p.apiKey == "" {
		return processor.NewPaymentError(processor.CoinbaseCommerce, "NOT_CONFIGURED",
			"coinbase commerce processor not configured", nil)
	}
	return nil
}

func mapChargeStatus(timeline []timelineEntry) string {
	if len(timeline) == 0 {
		return "pending"
	}

	latest := timeline[len(timeline)-1].Status
	switch strings.ToUpper(latest) {
	case "NEW":
		return "pending"
	case "PENDING":
		return "pending"
	case "COMPLETED", "CONFIRMED":
		return "succeeded"
	case "EXPIRED":
		return "expired"
	case "UNRESOLVED":
		return "action_required"
	case "RESOLVED":
		return "succeeded"
	case "CANCELED":
		return "canceled"
	default:
		return latest
	}
}

func mapWebhookEventType(eventType string) string {
	switch eventType {
	case "charge:created":
		return "payment.created"
	case "charge:confirmed":
		return "payment.confirmed"
	case "charge:completed":
		return "payment.completed"
	case "charge:failed":
		return "payment.failed"
	case "charge:delayed":
		return "payment.delayed"
	case "charge:pending":
		return "payment.pending"
	case "charge:resolved":
		return "payment.resolved"
	default:
		return "coinbase_commerce." + eventType
	}
}

// Compile-time interface check.
var _ processor.PaymentProcessor = (*Provider)(nil)
