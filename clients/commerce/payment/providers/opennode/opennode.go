// Package opennode implements the OpenNode (Lightning) payment processor for Commerce.
// Uses the OpenNode API v2 directly (no SDK dependency).
package opennode

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
	"strconv"
	"time"

	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/payment/processor"
)

const (
	devBaseURL     = "https://dev-api.opennode.com"
	liveBaseURL    = "https://api.opennode.com"
	defaultTimeout = 30 * time.Second
)

// Config holds OpenNode API credentials.
type Config struct {
	APIKey      string
	Environment string // "dev" or "live"
}

// Provider implements processor.PaymentProcessor for OpenNode API v2.
type Provider struct {
	*processor.BaseProcessor
	apiKey      string
	environment string
	client      *http.Client
}

func init() {
	apiKey := os.Getenv("OPENNODE_API_KEY")
	env := os.Getenv("OPENNODE_ENVIRONMENT")
	if env == "" {
		env = "dev"
	}

	p := &Provider{
		BaseProcessor: processor.NewBaseProcessor(processor.OpenNode, supportedCurrencies()),
		apiKey:        apiKey,
		environment:   env,
		client:        &http.Client{Timeout: defaultTimeout},
	}

	if apiKey != "" {
		p.SetConfigured(true)
	}

	processor.Register(p)
}

// NewProvider creates a configured OpenNode provider instance.
func NewProvider(cfg Config) *Provider {
	env := cfg.Environment
	if env == "" {
		env = "dev"
	}
	p := &Provider{
		BaseProcessor: processor.NewBaseProcessor(processor.OpenNode, supportedCurrencies()),
		apiKey:        cfg.APIKey,
		environment:   env,
		client:        &http.Client{Timeout: defaultTimeout},
	}
	if cfg.APIKey != "" {
		p.SetConfigured(true)
	}
	return p
}

func supportedCurrencies() []currency.Type {
	return []currency.Type{
		currency.BTC,
	}
}

// Type returns the processor type.
func (p *Provider) Type() processor.ProcessorType {
	return processor.OpenNode
}

// IsAvailable reports whether the processor is configured.
func (p *Provider) IsAvailable(ctx context.Context) bool {
	return p.apiKey != ""
}

// SupportedCurrencies returns currencies this processor supports.
func (p *Provider) SupportedCurrencies() []currency.Type {
	return supportedCurrencies()
}

func (p *Provider) baseURL() string {
	if p.environment == "live" {
		return liveBaseURL
	}
	return devBaseURL
}

// ---------------------------------------------------------------------------
// PaymentProcessor
// ---------------------------------------------------------------------------

// Charge creates an OpenNode Lightning charge (amount in satoshis).
func (p *Provider) Charge(ctx context.Context, req processor.PaymentRequest) (*processor.PaymentResult, error) {
	if err := p.ensureAvailable(); err != nil {
		return nil, err
	}
	if err := processor.ValidateRequest(req); err != nil {
		return nil, err
	}

	// Amount is in cents (satoshis for BTC). OpenNode expects satoshis.
	body := chargeRequest{
		Amount:      int64(req.Amount),
		Description: req.Description,
		Currency:    "btc",
	}

	if req.OrderID != "" {
		body.OrderID = req.OrderID
	}
	if req.CustomerID != "" {
		body.CustomerEmail = req.CustomerID
	}
	if email, ok := req.Metadata["email"].(string); ok {
		body.CustomerEmail = email
	}
	if name, ok := req.Metadata["name"].(string); ok {
		body.CustomerName = name
	}
	if cbURL, ok := req.Options["callbackURL"].(string); ok {
		body.CallbackURL = cbURL
	}
	if successURL, ok := req.Options["successURL"].(string); ok {
		body.SuccessURL = successURL
	}
	if autoSettle, ok := req.Options["autoSettle"].(bool); ok {
		body.AutoSettle = autoSettle
	}

	var resp chargeResponseWrapper
	if err := p.post(ctx, "/v2/charges", body, &resp); err != nil {
		return &processor.PaymentResult{
			Success:      false,
			ErrorMessage: err.Error(),
			Error:        err,
		}, err
	}

	metadata := map[string]interface{}{
		"lightningInvoice": resp.Data.LightningInvoice.PayReq,
		"onchainAddress":   resp.Data.ChainInvoice.Address,
		"hostedCheckoutURL": resp.Data.HostedCheckoutURL,
		"status":           resp.Data.Status,
	}

	return &processor.PaymentResult{
		Success:       true,
		TransactionID: resp.Data.ID,
		ProcessorRef:  resp.Data.ID,
		Status:        mapChargeStatus(resp.Data.Status),
		Metadata:      metadata,
	}, nil
}

// GetTransaction retrieves an OpenNode charge by ID.
func (p *Provider) GetTransaction(ctx context.Context, txID string) (*processor.Transaction, error) {
	if err := p.ensureAvailable(); err != nil {
		return nil, err
	}
	if txID == "" {
		return nil, processor.NewPaymentError(processor.OpenNode, "INVALID_TRANSACTION",
			"transaction ID is required", nil)
	}

	var resp chargeResponseWrapper
	if err := p.get(ctx, "/v2/charge/"+txID, &resp); err != nil {
		return nil, err
	}

	var createdAt int64
	if resp.Data.CreatedAt > 0 {
		createdAt = resp.Data.CreatedAt
	}

	return &processor.Transaction{
		ID:           resp.Data.ID,
		ProcessorRef: resp.Data.ID,
		Type:         "lightning_invoice",
		Amount:       currency.Cents(resp.Data.Amount),
		Currency:     currency.BTC,
		Status:       mapChargeStatus(resp.Data.Status),
		CreatedAt:    createdAt,
		UpdatedAt:    createdAt,
		Metadata: map[string]interface{}{
			"opennodeStatus":    resp.Data.Status,
			"description":       resp.Data.Description,
			"lightningInvoice":  resp.Data.LightningInvoice.PayReq,
		},
	}, nil
}

// Refund creates an OpenNode refund.
func (p *Provider) Refund(ctx context.Context, req processor.RefundRequest) (*processor.RefundResult, error) {
	if err := p.ensureAvailable(); err != nil {
		return nil, err
	}
	if req.TransactionID == "" {
		return nil, processor.NewPaymentError(processor.OpenNode, "INVALID_TRANSACTION",
			"transaction ID is required for refund", nil)
	}

	body := refundRequest{
		CheckoutID: req.TransactionID,
	}

	if req.Metadata != nil {
		if addr, ok := req.Metadata["address"].(string); ok {
			body.Address = addr
		}
	}

	var resp refundResponseWrapper
	if err := p.post(ctx, "/v2/refunds", body, &resp); err != nil {
		return &processor.RefundResult{
			Success:      false,
			ErrorMessage: err.Error(),
			Error:        err,
		}, err
	}

	return &processor.RefundResult{
		Success:      resp.Data.Status == "confirmed" || resp.Data.Status == "pending",
		RefundID:     resp.Data.ID,
		ProcessorRef: resp.Data.ID,
	}, nil
}

// ValidateWebhook verifies an OpenNode webhook signature (HMAC-SHA256).
func (p *Provider) ValidateWebhook(ctx context.Context, payload []byte, signature string) (*processor.WebhookEvent, error) {
	if p.apiKey == "" {
		return nil, processor.ErrWebhookValidationFailed
	}
	if signature == "" {
		return nil, processor.ErrWebhookValidationFailed
	}

	mac := hmac.New(sha256.New, []byte(p.apiKey))
	mac.Write(payload)
	expectedSig := hex.EncodeToString(mac.Sum(nil))

	if !hmac.Equal([]byte(signature), []byte(expectedSig)) {
		return nil, processor.ErrWebhookValidationFailed
	}

	var evt webhookPayload
	if err := json.Unmarshal(payload, &evt); err != nil {
		return nil, fmt.Errorf("failed to parse opennode webhook: %w", err)
	}

	return &processor.WebhookEvent{
		ID:        evt.ID,
		Type:      mapWebhookEventType(evt.Status),
		Processor: processor.OpenNode,
		Data: map[string]interface{}{
			"chargeId":    evt.ID,
			"status":      evt.Status,
			"description": evt.Description,
			"orderId":     evt.OrderID,
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
		return processor.NewPaymentError(processor.OpenNode, "MARSHAL_ERROR",
			"failed to marshal request body", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL()+path, bytes.NewReader(jsonBody))
	if err != nil {
		return processor.NewPaymentError(processor.OpenNode, "REQUEST_ERROR",
			"failed to create HTTP request", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", p.apiKey)

	return p.doRequest(httpReq, result)
}

func (p *Provider) get(ctx context.Context, path string, result interface{}) error {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL()+path, nil)
	if err != nil {
		return processor.NewPaymentError(processor.OpenNode, "REQUEST_ERROR",
			"failed to create HTTP request", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", p.apiKey)

	return p.doRequest(httpReq, result)
}

func (p *Provider) doRequest(req *http.Request, result interface{}) error {
	resp, err := p.client.Do(req)
	if err != nil {
		return processor.NewPaymentError(processor.OpenNode, "NETWORK_ERROR",
			"failed to send request to OpenNode", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return processor.NewPaymentError(processor.OpenNode, "READ_ERROR",
			"failed to read OpenNode response", err)
	}

	if resp.StatusCode >= 400 {
		var apiErr opennodeErrorResponse
		if json.Unmarshal(body, &apiErr) == nil && apiErr.Message != "" {
			return processor.NewPaymentError(processor.OpenNode, fmt.Sprintf("HTTP_%d", resp.StatusCode),
				fmt.Sprintf("opennode API error: %s", apiErr.Message), nil)
		}
		return processor.NewPaymentError(processor.OpenNode, fmt.Sprintf("HTTP_%d", resp.StatusCode),
			fmt.Sprintf("opennode API error (HTTP %d): %s", resp.StatusCode, string(body)), nil)
	}

	if result != nil {
		if err := json.Unmarshal(body, result); err != nil {
			return processor.NewPaymentError(processor.OpenNode, "DECODE_ERROR",
				"failed to decode OpenNode response", err)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// OpenNode API types
// ---------------------------------------------------------------------------

type chargeRequest struct {
	Amount        int64  `json:"amount"`
	Description   string `json:"description,omitempty"`
	Currency      string `json:"currency"`
	OrderID       string `json:"order_id,omitempty"`
	CustomerEmail string `json:"customer_email,omitempty"`
	CustomerName  string `json:"customer_name,omitempty"`
	CallbackURL   string `json:"callback_url,omitempty"`
	SuccessURL    string `json:"success_url,omitempty"`
	AutoSettle    bool   `json:"auto_settle,omitempty"`
}

type lightningInvoice struct {
	PayReq string `json:"payreq"`
}

type chainInvoice struct {
	Address string `json:"address"`
}

type chargeData struct {
	ID                string           `json:"id"`
	Status            string           `json:"status"`
	Amount            int64            `json:"amount"`
	Description       string           `json:"description"`
	CreatedAt         int64            `json:"created_at"`
	LightningInvoice  lightningInvoice `json:"lightning_invoice"`
	ChainInvoice      chainInvoice     `json:"chain_invoice"`
	HostedCheckoutURL string           `json:"hosted_checkout_url"`
}

// UnmarshalJSON handles the amount field which may be a string or number.
func (c *chargeData) UnmarshalJSON(data []byte) error {
	type Alias chargeData
	aux := &struct {
		Amount json.RawMessage `json:"amount"`
		*Alias
	}{
		Alias: (*Alias)(c),
	}
	if err := json.Unmarshal(data, aux); err != nil {
		return err
	}

	if len(aux.Amount) > 0 {
		// Try number first
		if err := json.Unmarshal(aux.Amount, &c.Amount); err != nil {
			// Try string
			var s string
			if err := json.Unmarshal(aux.Amount, &s); err == nil {
				c.Amount, _ = strconv.ParseInt(s, 10, 64)
			}
		}
	}
	return nil
}

type chargeResponseWrapper struct {
	Data chargeData `json:"data"`
}

type refundRequest struct {
	CheckoutID string `json:"checkout_id"`
	Address    string `json:"address,omitempty"`
}

type refundData struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

type refundResponseWrapper struct {
	Data refundData `json:"data"`
}

type webhookPayload struct {
	ID          string `json:"id"`
	Status      string `json:"status"`
	Description string `json:"description"`
	OrderID     string `json:"order_id"`
}

type opennodeErrorResponse struct {
	Message string `json:"message"`
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func (p *Provider) ensureAvailable() error {
	if p.apiKey == "" {
		return processor.NewPaymentError(processor.OpenNode, "NOT_CONFIGURED",
			"opennode processor not configured", nil)
	}
	return nil
}

func mapChargeStatus(status string) string {
	switch status {
	case "unpaid":
		return "pending"
	case "processing":
		return "processing"
	case "paid":
		return "succeeded"
	case "underpaid":
		return "underpaid"
	case "expired":
		return "expired"
	case "refunded":
		return "refunded"
	default:
		return status
	}
}

func mapWebhookEventType(status string) string {
	switch status {
	case "paid":
		return "payment.completed"
	case "processing":
		return "payment.processing"
	case "underpaid":
		return "payment.underpaid"
	case "expired":
		return "payment.expired"
	case "refunded":
		return "refund.succeeded"
	default:
		return "opennode." + status
	}
}

// Compile-time interface check.
var _ processor.PaymentProcessor = (*Provider)(nil)
