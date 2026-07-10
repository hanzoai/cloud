package lemonsqueezy

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
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/payment/processor"
)

const (
	baseURL     = "https://api.lemonsqueezy.com/v1"
	contentType = "application/vnd.api+json"
)

// Provider implements processor.PaymentProcessor for LemonSqueezy.
//
// LemonSqueezy is a checkout-based payment platform. Charge creates a hosted
// checkout session and returns the checkout URL. Authorize and Capture are not
// supported because LemonSqueezy does not separate authorization from capture.
type Provider struct {
	*processor.BaseProcessor

	apiKey           string
	storeID          string
	webhookSecret    string
	defaultVariantID string

	client *http.Client
}

// Config holds the configuration for the LemonSqueezy provider.
type Config struct {
	// APIKey is the LemonSqueezy API key (required).
	APIKey string

	// StoreID is the LemonSqueezy store identifier (required).
	StoreID string

	// WebhookSecret is the signing secret for webhook validation.
	WebhookSecret string

	// DefaultVariantID is the product variant used for one-off charges.
	DefaultVariantID string
}

func init() {
	processor.Register(&Provider{
		BaseProcessor: processor.NewBaseProcessor(processor.LemonSqueezy, supportedCurrencies()),
		client:        &http.Client{Timeout: 30 * time.Second},
	})
}

// NewProvider creates a configured LemonSqueezy provider instance.
func NewProvider(cfg Config) *Provider {
	p := &Provider{
		BaseProcessor: processor.NewBaseProcessor(processor.LemonSqueezy, supportedCurrencies()),
		client:        &http.Client{Timeout: 30 * time.Second},
	}
	p.Configure(cfg)
	return p
}

// Configure sets the provider credentials and marks it as available.
func (p *Provider) Configure(cfg Config) {
	p.apiKey = cfg.APIKey
	p.storeID = cfg.StoreID
	p.webhookSecret = cfg.WebhookSecret
	p.defaultVariantID = cfg.DefaultVariantID
	p.client = &http.Client{Timeout: 30 * time.Second}
	p.SetConfigured(cfg.APIKey != "" && cfg.StoreID != "")
}

// Type returns the processor type.
func (p *Provider) Type() processor.ProcessorType {
	return processor.LemonSqueezy
}

// IsAvailable reports whether the processor is configured.
func (p *Provider) IsAvailable(ctx context.Context) bool {
	return p.apiKey != "" && p.storeID != ""
}

// SupportedCurrencies returns currencies accepted by LemonSqueezy.
func (p *Provider) SupportedCurrencies() []currency.Type {
	return supportedCurrencies()
}

func supportedCurrencies() []currency.Type {
	return []currency.Type{
		currency.USD, currency.EUR, currency.GBP, currency.CAD,
		currency.AUD, currency.BRL, currency.MXN,
	}
}

// ---------- Charge (create checkout) ----------

// checkoutRequest is the JSON:API request body for POST /checkouts.
type checkoutRequest struct {
	Data checkoutData `json:"data"`
}

type checkoutData struct {
	Type          string             `json:"type"`
	Attributes    checkoutAttributes `json:"attributes"`
	Relationships checkoutRels       `json:"relationships"`
}

type checkoutAttributes struct {
	CustomPrice    int64              `json:"custom_price"`
	ProductOptions checkoutProdOpts   `json:"product_options"`
	CheckoutData   checkoutCustomData `json:"checkout_data"`
}

type checkoutProdOpts struct {
	Description string `json:"description,omitempty"`
	RedirectURL string `json:"redirect_url,omitempty"`
}

type checkoutCustomData struct {
	Email  string            `json:"email,omitempty"`
	Custom map[string]string `json:"custom,omitempty"`
}

type checkoutRels struct {
	Store   jsonAPIRelation `json:"store"`
	Variant jsonAPIRelation `json:"variant"`
}

type jsonAPIRelation struct {
	Data jsonAPIResourceID `json:"data"`
}

type jsonAPIResourceID struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

// jsonAPIResponse is the generic envelope for LemonSqueezy JSON:API responses.
type jsonAPIResponse struct {
	Data   json.RawMessage `json:"data"`
	Errors []apiError      `json:"errors,omitempty"`
}

type apiError struct {
	Status string `json:"status"`
	Title  string `json:"title"`
	Detail string `json:"detail"`
}

// checkoutResource holds the parsed checkout resource from the API.
type checkoutResource struct {
	ID         string `json:"id"`
	Attributes struct {
		URL       string `json:"url"`
		CreatedAt string `json:"created_at"`
	} `json:"attributes"`
}

// Charge creates a LemonSqueezy checkout session.
//
// The checkout URL is returned in PaymentResult.Metadata["checkout_url"].
// The caller should redirect the customer to this URL to complete payment.
// TransactionID is set to the request's OrderID for correlation.
func (p *Provider) Charge(ctx context.Context, req processor.PaymentRequest) (*processor.PaymentResult, error) {
	if err := p.ensureConfigured(); err != nil {
		return nil, err
	}
	if err := processor.ValidateRequest(req); err != nil {
		return nil, processor.NewPaymentError(processor.LemonSqueezy, "INVALID_REQUEST", err.Error(), err)
	}

	variantID := p.defaultVariantID
	if v, ok := req.Options["variant_id"].(string); ok && v != "" {
		variantID = v
	}
	if variantID == "" {
		return nil, processor.NewPaymentError(processor.LemonSqueezy, "MISSING_VARIANT",
			"variant_id is required: set DefaultVariantID or pass options.variant_id", nil)
	}

	redirectURL, _ := req.Options["redirect_url"].(string)
	email, _ := req.Metadata["email"].(string)

	body := checkoutRequest{
		Data: checkoutData{
			Type: "checkouts",
			Attributes: checkoutAttributes{
				CustomPrice: int64(req.Amount),
				ProductOptions: checkoutProdOpts{
					Description: req.Description,
					RedirectURL: redirectURL,
				},
				CheckoutData: checkoutCustomData{
					Email: email,
					Custom: map[string]string{
						"order_id":    req.OrderID,
						"customer_id": req.CustomerID,
					},
				},
			},
			Relationships: checkoutRels{
				Store: jsonAPIRelation{
					Data: jsonAPIResourceID{Type: "stores", ID: p.storeID},
				},
				Variant: jsonAPIRelation{
					Data: jsonAPIResourceID{Type: "variants", ID: variantID},
				},
			},
		},
	}

	respBody, err := p.doRequest(ctx, http.MethodPost, "/checkouts", body)
	if err != nil {
		return nil, err
	}

	var apiResp jsonAPIResponse
	if err := json.Unmarshal(respBody, &apiResp); err != nil {
		return nil, processor.NewPaymentError(processor.LemonSqueezy, "PARSE_ERROR",
			"failed to parse checkout response", err)
	}
	if len(apiResp.Errors) > 0 {
		return nil, apiErrorToPaymentError(apiResp.Errors)
	}

	var checkout checkoutResource
	if err := json.Unmarshal(apiResp.Data, &checkout); err != nil {
		return nil, processor.NewPaymentError(processor.LemonSqueezy, "PARSE_ERROR",
			"failed to parse checkout data", err)
	}

	return &processor.PaymentResult{
		Success:       true,
		TransactionID: req.OrderID,
		ProcessorRef:  checkout.ID,
		Status:        "pending",
		Metadata: map[string]interface{}{
			"checkout_url": checkout.Attributes.URL,
			"checkout_id":  checkout.ID,
		},
	}, nil
}

// ---------- Authorize / Capture (not supported) ----------

// Authorize is not supported by LemonSqueezy. It returns NOT_SUPPORTED.
func (p *Provider) Authorize(ctx context.Context, req processor.PaymentRequest) (*processor.PaymentResult, error) {
	return nil, processor.NewPaymentError(processor.LemonSqueezy, "NOT_SUPPORTED",
		"lemonsqueezy does not support authorize/capture; use Charge to create a checkout", nil)
}

// Capture is not supported by LemonSqueezy. It returns NOT_SUPPORTED.
func (p *Provider) Capture(ctx context.Context, transactionID string, amount currency.Cents) (*processor.PaymentResult, error) {
	return nil, processor.NewPaymentError(processor.LemonSqueezy, "NOT_SUPPORTED",
		"lemonsqueezy does not support authorize/capture; use Charge to create a checkout", nil)
}

// ---------- Refund ----------

// refundRequest is the JSON:API body for POST /orders/{id}/refund.
type refundRequest struct {
	Data refundData `json:"data"`
}

type refundData struct {
	Type       string           `json:"type"`
	ID         string           `json:"id"`
	Attributes refundAttributes `json:"attributes"`
}

type refundAttributes struct {
	Amount int64 `json:"amount"`
}

// orderResource represents a parsed LemonSqueezy order.
type orderResource struct {
	ID         string `json:"id"`
	Attributes struct {
		StoreID         int64  `json:"store_id"`
		Identifier      string `json:"identifier"`
		OrderNumber     int64  `json:"order_number"`
		Currency        string `json:"currency"`
		CurrencyRate    string `json:"currency_rate"`
		Total           int64  `json:"total"`
		SubtotalUSD     int64  `json:"subtotal_usd"`
		TaxUSD          int64  `json:"tax_usd"`
		TotalUSD        int64  `json:"total_usd"`
		Refunded        bool   `json:"refunded"`
		RefundedAt      string `json:"refunded_at"`
		Status          string `json:"status"`
		StatusFormatted string `json:"status_formatted"`
		CreatedAt       string `json:"created_at"`
		UpdatedAt       string `json:"updated_at"`
	} `json:"attributes"`
}

// Refund issues a refund for a LemonSqueezy order.
//
// TransactionID in the RefundRequest must be the LemonSqueezy order ID.
func (p *Provider) Refund(ctx context.Context, req processor.RefundRequest) (*processor.RefundResult, error) {
	if err := p.ensureConfigured(); err != nil {
		return nil, err
	}
	if req.TransactionID == "" {
		return nil, processor.NewPaymentError(processor.LemonSqueezy, "INVALID_REQUEST",
			"transaction_id (order ID) is required for refund", nil)
	}

	endpoint := fmt.Sprintf("/orders/%s/refund", req.TransactionID)

	body := refundRequest{
		Data: refundData{
			Type: "orders",
			ID:   req.TransactionID,
			Attributes: refundAttributes{
				Amount: int64(req.Amount),
			},
		},
	}

	respBody, err := p.doRequest(ctx, http.MethodPost, endpoint, body)
	if err != nil {
		return nil, err
	}

	var apiResp jsonAPIResponse
	if err := json.Unmarshal(respBody, &apiResp); err != nil {
		return nil, processor.NewPaymentError(processor.LemonSqueezy, "PARSE_ERROR",
			"failed to parse refund response", err)
	}
	if len(apiResp.Errors) > 0 {
		return nil, apiErrorToPaymentError(apiResp.Errors)
	}

	var order orderResource
	if err := json.Unmarshal(apiResp.Data, &order); err != nil {
		return nil, processor.NewPaymentError(processor.LemonSqueezy, "PARSE_ERROR",
			"failed to parse refund order data", err)
	}

	return &processor.RefundResult{
		Success:      order.Attributes.Refunded,
		RefundID:     order.ID,
		ProcessorRef: order.ID,
	}, nil
}

// ---------- GetTransaction ----------

// GetTransaction retrieves a LemonSqueezy order by its ID.
func (p *Provider) GetTransaction(ctx context.Context, txID string) (*processor.Transaction, error) {
	if err := p.ensureConfigured(); err != nil {
		return nil, err
	}
	if txID == "" {
		return nil, processor.NewPaymentError(processor.LemonSqueezy, "INVALID_REQUEST",
			"transaction ID (order ID) is required", nil)
	}

	endpoint := fmt.Sprintf("/orders/%s", txID)

	respBody, err := p.doRequest(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}

	var apiResp jsonAPIResponse
	if err := json.Unmarshal(respBody, &apiResp); err != nil {
		return nil, processor.NewPaymentError(processor.LemonSqueezy, "PARSE_ERROR",
			"failed to parse order response", err)
	}
	if len(apiResp.Errors) > 0 {
		e := apiResp.Errors[0]
		if e.Status == "404" {
			return nil, processor.ErrTransactionNotFound
		}
		return nil, apiErrorToPaymentError(apiResp.Errors)
	}

	var order orderResource
	if err := json.Unmarshal(apiResp.Data, &order); err != nil {
		return nil, processor.NewPaymentError(processor.LemonSqueezy, "PARSE_ERROR",
			"failed to parse order data", err)
	}

	txType := "charge"
	if order.Attributes.Refunded {
		txType = "refund"
	}

	var createdAt, updatedAt int64
	if t, err := time.Parse(time.RFC3339, order.Attributes.CreatedAt); err == nil {
		createdAt = t.Unix()
	}
	if t, err := time.Parse(time.RFC3339, order.Attributes.UpdatedAt); err == nil {
		updatedAt = t.Unix()
	}

	cur := currency.Type(strings.ToLower(order.Attributes.Currency))

	return &processor.Transaction{
		ID:           order.ID,
		ProcessorRef: order.Attributes.Identifier,
		Type:         txType,
		Amount:       currency.Cents(order.Attributes.Total),
		Currency:     cur,
		Status:       mapOrderStatus(order.Attributes.Status),
		CustomerID:   "",
		Metadata: map[string]interface{}{
			"order_number":     order.Attributes.OrderNumber,
			"refunded":         order.Attributes.Refunded,
			"status_formatted": order.Attributes.StatusFormatted,
			"total_usd":        order.Attributes.TotalUSD,
		},
		CreatedAt: createdAt,
		UpdatedAt: updatedAt,
	}, nil
}

// ---------- ValidateWebhook ----------

// ValidateWebhook verifies the HMAC-SHA256 signature of a LemonSqueezy webhook
// and parses the event payload.
//
// LemonSqueezy sends the hex-encoded HMAC-SHA256 digest in the X-Signature header.
// The signature parameter should contain the value from that header.
func (p *Provider) ValidateWebhook(ctx context.Context, payload []byte, signature string) (*processor.WebhookEvent, error) {
	if err := p.ensureConfigured(); err != nil {
		return nil, err
	}
	if p.webhookSecret == "" {
		return nil, processor.NewPaymentError(processor.LemonSqueezy, "NOT_CONFIGURED",
			"webhook secret is not configured", nil)
	}

	mac := hmac.New(sha256.New, []byte(p.webhookSecret))
	mac.Write(payload)
	expected := hex.EncodeToString(mac.Sum(nil))

	if !hmac.Equal([]byte(expected), []byte(signature)) {
		return nil, processor.ErrWebhookValidationFailed
	}

	// Parse the webhook payload.
	var raw struct {
		Meta struct {
			EventName  string            `json:"event_name"`
			CustomData map[string]string `json:"custom_data"`
		} `json:"meta"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(payload, &raw); err != nil {
		return nil, processor.NewPaymentError(processor.LemonSqueezy, "PARSE_ERROR",
			"failed to parse webhook payload", err)
	}

	// Extract the resource ID from the data envelope.
	var resourceID struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(raw.Data, &resourceID)

	// Build a flat data map for the webhook event.
	data := make(map[string]interface{})
	data["event_name"] = raw.Meta.EventName
	data["resource_id"] = resourceID.ID
	if raw.Meta.CustomData != nil {
		for k, v := range raw.Meta.CustomData {
			data["custom_"+k] = v
		}
	}
	// Include the full data object for downstream consumers.
	var fullData interface{}
	if err := json.Unmarshal(raw.Data, &fullData); err == nil {
		data["resource"] = fullData
	}

	return &processor.WebhookEvent{
		ID:        resourceID.ID,
		Type:      raw.Meta.EventName,
		Processor: processor.LemonSqueezy,
		Data:      data,
		Timestamp: time.Now().Unix(),
	}, nil
}

// ---------- HTTP helpers ----------

// doRequest performs an authenticated HTTP request to the LemonSqueezy API.
func (p *Provider) doRequest(ctx context.Context, method, path string, body interface{}) ([]byte, error) {
	var reqBody io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, processor.NewPaymentError(processor.LemonSqueezy, "MARSHAL_ERROR",
				"failed to marshal request body", err)
		}
		reqBody = bytes.NewReader(b)
	}

	url := baseURL + path
	httpReq, err := http.NewRequestWithContext(ctx, method, url, reqBody)
	if err != nil {
		return nil, processor.NewPaymentError(processor.LemonSqueezy, "REQUEST_ERROR",
			"failed to create HTTP request", err)
	}

	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	httpReq.Header.Set("Accept", contentType)
	if body != nil {
		httpReq.Header.Set("Content-Type", contentType)
	}

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, processor.NewPaymentError(processor.LemonSqueezy, "HTTP_ERROR",
			"LemonSqueezy API request failed", err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, processor.NewPaymentError(processor.LemonSqueezy, "READ_ERROR",
			"failed to read API response", err)
	}

	if resp.StatusCode >= 400 {
		// Try to extract structured errors from the response.
		var apiResp jsonAPIResponse
		if json.Unmarshal(respBytes, &apiResp) == nil && len(apiResp.Errors) > 0 {
			return nil, apiErrorToPaymentError(apiResp.Errors)
		}
		return nil, processor.NewPaymentError(processor.LemonSqueezy,
			"HTTP_"+strconv.Itoa(resp.StatusCode),
			fmt.Sprintf("LemonSqueezy API returned %d: %s", resp.StatusCode, string(respBytes)),
			nil)
	}

	return respBytes, nil
}

// ensureConfigured returns an error if the provider is not configured.
func (p *Provider) ensureConfigured() error {
	if p.apiKey == "" || p.storeID == "" {
		return processor.NewPaymentError(processor.LemonSqueezy, "NOT_CONFIGURED",
			"lemonsqueezy processor not configured: API key and store ID are required", nil)
	}
	return nil
}

// apiErrorToPaymentError converts LemonSqueezy API errors to a PaymentError.
func apiErrorToPaymentError(errs []apiError) *processor.PaymentError {
	if len(errs) == 0 {
		return processor.NewPaymentError(processor.LemonSqueezy, "UNKNOWN", "unknown API error", nil)
	}
	e := errs[0]
	return processor.NewPaymentError(processor.LemonSqueezy, e.Status, e.Detail, nil)
}

// mapOrderStatus normalizes LemonSqueezy order statuses to common status strings.
func mapOrderStatus(status string) string {
	switch strings.ToLower(status) {
	case "paid":
		return "completed"
	case "pending":
		return "pending"
	case "failed":
		return "failed"
	case "refunded":
		return "refunded"
	case "partial_refund":
		return "partially_refunded"
	default:
		return status
	}
}

// Compile-time interface check.
var _ processor.PaymentProcessor = (*Provider)(nil)
