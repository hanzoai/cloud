package paypal

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/payment/processor"
)

const (
	liveBaseURL    = "https://api-m.paypal.com"
	sandboxBaseURL = "https://api-m.sandbox.paypal.com"
	tokenPath      = "/v1/oauth2/token"
	ordersPath     = "/v2/checkout/orders"
	authCapture    = "/v2/payments/authorizations"
	capturesPath   = "/v2/payments/captures"
	webhookVerify  = "/v1/notifications/verify-webhook-signature"
)

// Config holds PayPal API credentials.
type Config struct {
	ClientID     string
	ClientSecret string
	WebhookID    string
	Sandbox      bool
}

// Provider implements processor.PaymentProcessor for PayPal REST API v2.
type Provider struct {
	*processor.BaseProcessor

	mu           sync.RWMutex
	config       Config
	httpClient   *http.Client
	accessToken  string
	tokenExpiry  time.Time
}

func init() {
	processor.Register(&Provider{
		BaseProcessor: processor.NewBaseProcessor(processor.PayPal, supportedCurrencies()),
		httpClient:    &http.Client{Timeout: 30 * time.Second},
	})
}

// NewProvider creates a configured PayPal provider instance.
func NewProvider(cfg Config) *Provider {
	p := &Provider{
		BaseProcessor: processor.NewBaseProcessor(processor.PayPal, supportedCurrencies()),
		httpClient:    &http.Client{Timeout: 30 * time.Second},
	}
	p.Configure(cfg)
	return p
}

// Configure sets PayPal credentials and marks the processor as available.
func (p *Provider) Configure(cfg Config) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.config = cfg
	p.accessToken = ""
	p.tokenExpiry = time.Time{}
	p.SetConfigured(cfg.ClientID != "" && cfg.ClientSecret != "")
}

// Type returns the processor type.
func (p *Provider) Type() processor.ProcessorType {
	return processor.PayPal
}

// IsAvailable reports whether the processor is configured.
func (p *Provider) IsAvailable(_ context.Context) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.config.ClientID != "" && p.config.ClientSecret != ""
}

// SupportedCurrencies returns currencies supported by PayPal.
func (p *Provider) SupportedCurrencies() []currency.Type {
	return supportedCurrencies()
}

func supportedCurrencies() []currency.Type {
	return []currency.Type{
		currency.USD, currency.EUR, currency.GBP, currency.CAD,
		currency.AUD, currency.JPY, currency.CHF, currency.NZD,
		currency.SGD, currency.HKD, currency.NOK, currency.SEK,
		currency.DKK, currency.PLN, currency.BRL, currency.MXN,
		currency.CZK, currency.HUF, currency.ILS, currency.MYR,
		currency.PHP, currency.TWD, currency.THB, currency.RUB,
		currency.INR,
	}
}

// ---------------------------------------------------------------------------
// PaymentProcessor interface
// ---------------------------------------------------------------------------

// Charge creates a PayPal order with CAPTURE intent and captures it immediately.
// If req.Token is set, it is treated as an existing approved order ID to capture.
func (p *Provider) Charge(ctx context.Context, req processor.PaymentRequest) (*processor.PaymentResult, error) {
	if err := p.checkAvailable(); err != nil {
		return nil, err
	}
	if err := processor.ValidateRequest(req); err != nil {
		return nil, err
	}

	orderID := req.Token
	if orderID == "" {
		// Create a new order with CAPTURE intent.
		created, err := p.createOrder(ctx, req, "CAPTURE")
		if err != nil {
			return nil, err
		}
		orderID = created.ID
	}

	// Capture the order.
	capture, err := p.captureOrder(ctx, orderID)
	if err != nil {
		return nil, err
	}

	result := &processor.PaymentResult{
		Success:       capture.Status == "COMPLETED",
		TransactionID: capture.ID,
		ProcessorRef:  captureIDFromOrder(capture),
		Status:        capture.Status,
		Metadata:      map[string]interface{}{"paypal_order_id": capture.ID},
	}
	if !result.Success {
		result.ErrorMessage = fmt.Sprintf("paypal: order capture status %s", capture.Status)
		result.Error = processor.ErrPaymentFailed
	}
	return result, nil
}

// Authorize creates a PayPal order with AUTHORIZE intent and authorizes it.
// If req.Token is set, it is treated as an existing approved order ID to authorize.
func (p *Provider) Authorize(ctx context.Context, req processor.PaymentRequest) (*processor.PaymentResult, error) {
	if err := p.checkAvailable(); err != nil {
		return nil, err
	}
	if err := processor.ValidateRequest(req); err != nil {
		return nil, err
	}

	orderID := req.Token
	if orderID == "" {
		created, err := p.createOrder(ctx, req, "AUTHORIZE")
		if err != nil {
			return nil, err
		}
		orderID = created.ID
	}

	auth, err := p.authorizeOrder(ctx, orderID)
	if err != nil {
		return nil, err
	}

	authID := authorizationIDFromOrder(auth)
	result := &processor.PaymentResult{
		Success:       auth.Status == "COMPLETED",
		TransactionID: auth.ID,
		ProcessorRef:  authID,
		Status:        auth.Status,
		Metadata: map[string]interface{}{
			"paypal_order_id":         auth.ID,
			"paypal_authorization_id": authID,
		},
	}
	if !result.Success {
		result.ErrorMessage = fmt.Sprintf("paypal: order authorize status %s", auth.Status)
		result.Error = processor.ErrPaymentFailed
	}
	return result, nil
}

// Capture captures a previously authorized payment.
// transactionID is the PayPal authorization ID returned in Authorize metadata.
func (p *Provider) Capture(ctx context.Context, transactionID string, amount currency.Cents) (*processor.PaymentResult, error) {
	if err := p.checkAvailable(); err != nil {
		return nil, err
	}
	if transactionID == "" {
		return nil, processor.ErrInvalidPaymentRequest
	}

	body := map[string]interface{}{
		"amount": map[string]string{
			"value":         centsToDecimal(amount, currency.USD),
			"currency_code": "USD",
		},
	}
	// If amount is 0, capture the full authorized amount (omit body).
	var payload []byte
	var err error
	if amount > 0 {
		payload, err = json.Marshal(body)
		if err != nil {
			return nil, p.payErr("MARSHAL_ERROR", "failed to marshal capture body", err)
		}
	}

	path := fmt.Sprintf("%s/%s/capture", authCapture, transactionID)
	resp, err := p.doRequest(ctx, http.MethodPost, path, payload)
	if err != nil {
		return nil, err
	}

	var capture paypalCapture
	if err := json.Unmarshal(resp, &capture); err != nil {
		return nil, p.payErr("PARSE_ERROR", "failed to parse capture response", err)
	}

	return &processor.PaymentResult{
		Success:       capture.Status == "COMPLETED",
		TransactionID: capture.ID,
		ProcessorRef:  capture.ID,
		Status:        capture.Status,
		Metadata:      map[string]interface{}{"paypal_capture_id": capture.ID},
	}, nil
}

// Refund refunds a captured payment.
// req.TransactionID is the PayPal capture ID.
func (p *Provider) Refund(ctx context.Context, req processor.RefundRequest) (*processor.RefundResult, error) {
	if err := p.checkAvailable(); err != nil {
		return nil, err
	}
	if req.TransactionID == "" {
		return nil, processor.ErrInvalidPaymentRequest
	}

	body := make(map[string]interface{})
	if req.Amount > 0 {
		body["amount"] = map[string]string{
			"value":         centsToDecimal(req.Amount, currency.USD),
			"currency_code": "USD",
		}
	}
	if req.Reason != "" {
		body["note_to_payer"] = req.Reason
	}

	var payload []byte
	var err error
	if len(body) > 0 {
		payload, err = json.Marshal(body)
		if err != nil {
			return nil, p.payErr("MARSHAL_ERROR", "failed to marshal refund body", err)
		}
	}

	path := fmt.Sprintf("%s/%s/refund", capturesPath, req.TransactionID)
	resp, err := p.doRequest(ctx, http.MethodPost, path, payload)
	if err != nil {
		return &processor.RefundResult{
			Success:      false,
			Error:        err,
			ErrorMessage: err.Error(),
		}, err
	}

	var refund paypalRefund
	if err := json.Unmarshal(resp, &refund); err != nil {
		return nil, p.payErr("PARSE_ERROR", "failed to parse refund response", err)
	}

	success := refund.Status == "COMPLETED"
	result := &processor.RefundResult{
		Success:      success,
		RefundID:     refund.ID,
		ProcessorRef: refund.ID,
	}
	if !success {
		result.ErrorMessage = fmt.Sprintf("paypal: refund status %s", refund.Status)
		result.Error = processor.ErrRefundFailed
	}
	return result, nil
}

// GetTransaction retrieves order details by order ID.
func (p *Provider) GetTransaction(ctx context.Context, txID string) (*processor.Transaction, error) {
	if err := p.checkAvailable(); err != nil {
		return nil, err
	}
	if txID == "" {
		return nil, processor.ErrTransactionNotFound
	}

	path := fmt.Sprintf("%s/%s", ordersPath, txID)
	resp, err := p.doRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}

	var order paypalOrder
	if err := json.Unmarshal(resp, &order); err != nil {
		return nil, p.payErr("PARSE_ERROR", "failed to parse order response", err)
	}

	tx := &processor.Transaction{
		ID:           order.ID,
		ProcessorRef: order.ID,
		Status:       order.Status,
		Metadata: map[string]interface{}{
			"intent":    order.Intent,
			"payer":     order.Payer,
			"links":     order.Links,
			"raw_order": order,
		},
	}

	// Extract amount from first purchase unit.
	if len(order.PurchaseUnits) > 0 {
		pu := order.PurchaseUnits[0]
		tx.Currency = currency.Type(strings.ToLower(pu.Amount.CurrencyCode))
		tx.Amount = decimalToCents(pu.Amount.Value, tx.Currency)
	}

	// Map PayPal status to transaction type.
	switch order.Intent {
	case "CAPTURE":
		tx.Type = "charge"
	case "AUTHORIZE":
		tx.Type = "authorize"
	default:
		tx.Type = "charge"
	}

	if order.CreateTime != "" {
		if t, err := time.Parse(time.RFC3339, order.CreateTime); err == nil {
			tx.CreatedAt = t.Unix()
		}
	}
	if order.UpdateTime != "" {
		if t, err := time.Parse(time.RFC3339, order.UpdateTime); err == nil {
			tx.UpdatedAt = t.Unix()
		}
	}

	return tx, nil
}

// ValidateWebhook verifies a PayPal webhook notification.
// signature should contain the JSON-encoded transmission headers:
//
//	{"transmission_id":"...", "transmission_time":"...", "cert_url":"...",
//	 "auth_algo":"...", "transmission_sig":"..."}
//
// payload is the raw webhook event body.
func (p *Provider) ValidateWebhook(ctx context.Context, payload []byte, signature string) (*processor.WebhookEvent, error) {
	if err := p.checkAvailable(); err != nil {
		return nil, err
	}

	p.mu.RLock()
	webhookID := p.config.WebhookID
	p.mu.RUnlock()

	if webhookID == "" {
		return nil, processor.ErrWebhookValidationFailed
	}

	// Parse the transmission headers from the signature string.
	var headers struct {
		TransmissionID  string `json:"transmission_id"`
		TransmissionTime string `json:"transmission_time"`
		CertURL         string `json:"cert_url"`
		AuthAlgo        string `json:"auth_algo"`
		TransmissionSig string `json:"transmission_sig"`
	}
	if err := json.Unmarshal([]byte(signature), &headers); err != nil {
		return nil, p.payErr("WEBHOOK_PARSE", "failed to parse webhook signature headers", err)
	}

	// Parse the event body to include in verification request.
	var eventBody json.RawMessage
	if err := json.Unmarshal(payload, &eventBody); err != nil {
		return nil, p.payErr("WEBHOOK_PARSE", "failed to parse webhook payload", err)
	}

	verifyReq := map[string]interface{}{
		"auth_algo":         headers.AuthAlgo,
		"cert_url":          headers.CertURL,
		"transmission_id":   headers.TransmissionID,
		"transmission_sig":  headers.TransmissionSig,
		"transmission_time": headers.TransmissionTime,
		"webhook_id":        webhookID,
		"webhook_event":     eventBody,
	}

	reqBody, err := json.Marshal(verifyReq)
	if err != nil {
		return nil, p.payErr("MARSHAL_ERROR", "failed to marshal verify request", err)
	}

	resp, err := p.doRequest(ctx, http.MethodPost, webhookVerify, reqBody)
	if err != nil {
		return nil, err
	}

	var verifyResp struct {
		VerificationStatus string `json:"verification_status"`
	}
	if err := json.Unmarshal(resp, &verifyResp); err != nil {
		return nil, p.payErr("PARSE_ERROR", "failed to parse verification response", err)
	}

	if verifyResp.VerificationStatus != "SUCCESS" {
		return nil, processor.ErrWebhookValidationFailed
	}

	// Parse the original event.
	var event struct {
		ID           string                 `json:"id"`
		EventType    string                 `json:"event_type"`
		Resource     map[string]interface{} `json:"resource"`
		CreateTime   string                 `json:"create_time"`
		ResourceType string                 `json:"resource_type"`
		Summary      string                 `json:"summary"`
	}
	if err := json.Unmarshal(payload, &event); err != nil {
		return nil, p.payErr("PARSE_ERROR", "failed to parse webhook event", err)
	}

	var ts int64
	if event.CreateTime != "" {
		if t, err := time.Parse(time.RFC3339, event.CreateTime); err == nil {
			ts = t.Unix()
		}
	}

	return &processor.WebhookEvent{
		ID:        event.ID,
		Type:      event.EventType,
		Processor: processor.PayPal,
		Data: map[string]interface{}{
			"resource":      event.Resource,
			"resource_type": event.ResourceType,
			"summary":       event.Summary,
		},
		Timestamp: ts,
	}, nil
}

// ---------------------------------------------------------------------------
// OAuth2 token management
// ---------------------------------------------------------------------------

// getAccessToken returns a valid access token, refreshing if expired.
func (p *Provider) getAccessToken(ctx context.Context) (string, error) {
	p.mu.RLock()
	token := p.accessToken
	expiry := p.tokenExpiry
	p.mu.RUnlock()

	// Return cached token if still valid with 60s buffer.
	if token != "" && time.Now().Before(expiry.Add(-60*time.Second)) {
		return token, nil
	}

	return p.refreshToken(ctx)
}

// refreshToken fetches a new OAuth2 access token from PayPal.
func (p *Provider) refreshToken(ctx context.Context) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Double-check after acquiring write lock.
	if p.accessToken != "" && time.Now().Before(p.tokenExpiry.Add(-60*time.Second)) {
		return p.accessToken, nil
	}

	base := p.baseURL()
	body := strings.NewReader("grant_type=client_credentials")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+tokenPath, body)
	if err != nil {
		return "", p.payErr("TOKEN_REQUEST", "failed to create token request", err)
	}
	req.SetBasicAuth(p.config.ClientID, p.config.ClientSecret)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return "", p.payErr("TOKEN_NETWORK", "failed to fetch access token", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", p.payErr("TOKEN_READ", "failed to read token response", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", p.payErr("TOKEN_ERROR",
			fmt.Sprintf("paypal token endpoint returned %d: %s", resp.StatusCode, string(respBody)), nil)
	}

	var tokenResp struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.Unmarshal(respBody, &tokenResp); err != nil {
		return "", p.payErr("TOKEN_PARSE", "failed to parse token response", err)
	}

	if tokenResp.AccessToken == "" {
		return "", p.payErr("TOKEN_EMPTY", "paypal returned empty access token", nil)
	}

	p.accessToken = tokenResp.AccessToken
	p.tokenExpiry = time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second)

	return p.accessToken, nil
}

// ---------------------------------------------------------------------------
// HTTP helpers
// ---------------------------------------------------------------------------

func (p *Provider) baseURL() string {
	if p.config.Sandbox {
		return sandboxBaseURL
	}
	return liveBaseURL
}

// doRequest executes an authenticated request against the PayPal API.
func (p *Provider) doRequest(ctx context.Context, method, path string, body []byte) ([]byte, error) {
	token, err := p.getAccessToken(ctx)
	if err != nil {
		return nil, err
	}

	base := p.baseURL()
	var reqBody io.Reader
	if body != nil {
		reqBody = bytes.NewReader(body)
	}

	req, err := http.NewRequestWithContext(ctx, method, base+path, reqBody)
	if err != nil {
		return nil, p.payErr("REQUEST_BUILD", "failed to build request", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, p.payErr("NETWORK_ERROR", "paypal api request failed", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, p.payErr("READ_ERROR", "failed to read paypal response", err)
	}

	// PayPal returns 200, 201, or 204 for success.
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return respBody, nil
	}

	// Parse PayPal error response for detail.
	var apiErr paypalAPIError
	if json.Unmarshal(respBody, &apiErr) == nil && apiErr.Name != "" {
		return nil, p.payErr(apiErr.Name,
			fmt.Sprintf("paypal %s %s: %s - %s", method, path, apiErr.Name, apiErr.Message), nil)
	}

	return nil, p.payErr("API_ERROR",
		fmt.Sprintf("paypal %s %s returned %d: %s", method, path, resp.StatusCode, string(respBody)), nil)
}

// ---------------------------------------------------------------------------
// PayPal order operations
// ---------------------------------------------------------------------------

// createOrder creates a new PayPal order.
func (p *Provider) createOrder(ctx context.Context, req processor.PaymentRequest, intent string) (*paypalOrder, error) {
	cur := req.Currency
	orderReq := map[string]interface{}{
		"intent": intent,
		"purchase_units": []map[string]interface{}{
			{
				"amount": map[string]string{
					"currency_code": cur.Code(),
					"value":         centsToDecimal(req.Amount, cur),
				},
			},
		},
	}

	// Add description if present.
	if req.Description != "" {
		pu := orderReq["purchase_units"].([]map[string]interface{})
		pu[0]["description"] = req.Description
	}

	// Add order reference.
	if req.OrderID != "" {
		pu := orderReq["purchase_units"].([]map[string]interface{})
		pu[0]["reference_id"] = req.OrderID
		pu[0]["invoice_id"] = req.OrderID
	}

	// Add custom metadata.
	if req.CustomerID != "" {
		pu := orderReq["purchase_units"].([]map[string]interface{})
		pu[0]["custom_id"] = req.CustomerID
	}

	body, err := json.Marshal(orderReq)
	if err != nil {
		return nil, p.payErr("MARSHAL_ERROR", "failed to marshal order request", err)
	}

	resp, err := p.doRequest(ctx, http.MethodPost, ordersPath, body)
	if err != nil {
		return nil, err
	}

	var order paypalOrder
	if err := json.Unmarshal(resp, &order); err != nil {
		return nil, p.payErr("PARSE_ERROR", "failed to parse create order response", err)
	}

	return &order, nil
}

// captureOrder captures an approved order.
func (p *Provider) captureOrder(ctx context.Context, orderID string) (*paypalOrder, error) {
	path := fmt.Sprintf("%s/%s/capture", ordersPath, orderID)

	// PayPal expects an empty JSON body or no body for capture.
	resp, err := p.doRequest(ctx, http.MethodPost, path, []byte("{}"))
	if err != nil {
		return nil, err
	}

	var order paypalOrder
	if err := json.Unmarshal(resp, &order); err != nil {
		return nil, p.payErr("PARSE_ERROR", "failed to parse capture order response", err)
	}

	return &order, nil
}

// authorizeOrder authorizes an approved order.
func (p *Provider) authorizeOrder(ctx context.Context, orderID string) (*paypalOrder, error) {
	path := fmt.Sprintf("%s/%s/authorize", ordersPath, orderID)

	resp, err := p.doRequest(ctx, http.MethodPost, path, []byte("{}"))
	if err != nil {
		return nil, err
	}

	var order paypalOrder
	if err := json.Unmarshal(resp, &order); err != nil {
		return nil, p.payErr("PARSE_ERROR", "failed to parse authorize order response", err)
	}

	return &order, nil
}

// ---------------------------------------------------------------------------
// Amount conversion
// ---------------------------------------------------------------------------

// centsToDecimal converts currency.Cents to a PayPal decimal string.
// Zero-decimal currencies (JPY, etc.) are returned as whole integers.
func centsToDecimal(amount currency.Cents, cur currency.Type) string {
	if cur.IsZeroDecimal() {
		return fmt.Sprintf("%d", amount)
	}
	return fmt.Sprintf("%.2f", float64(amount)/100.0)
}

// decimalToCents converts a PayPal decimal string to currency.Cents.
func decimalToCents(value string, cur currency.Type) currency.Cents {
	return currency.CentsFromString(value)
}

// ---------------------------------------------------------------------------
// PayPal API response types (internal)
// ---------------------------------------------------------------------------

type paypalOrder struct {
	ID             string              `json:"id"`
	Intent         string              `json:"intent"`
	Status         string              `json:"status"`
	PurchaseUnits  []paypalPurchaseUnit `json:"purchase_units"`
	Payer          interface{}         `json:"payer"`
	Links          interface{}         `json:"links"`
	CreateTime     string              `json:"create_time"`
	UpdateTime     string              `json:"update_time"`
}

type paypalPurchaseUnit struct {
	ReferenceID string          `json:"reference_id"`
	Amount      paypalAmount    `json:"amount"`
	Payments    *paypalPayments `json:"payments,omitempty"`
	Description string          `json:"description"`
	CustomID    string          `json:"custom_id"`
	InvoiceID   string          `json:"invoice_id"`
}

type paypalAmount struct {
	CurrencyCode string `json:"currency_code"`
	Value        string `json:"value"`
}

type paypalPayments struct {
	Captures       []paypalCapture       `json:"captures,omitempty"`
	Authorizations []paypalAuthorization `json:"authorizations,omitempty"`
	Refunds        []paypalRefund        `json:"refunds,omitempty"`
}

type paypalCapture struct {
	ID     string       `json:"id"`
	Status string       `json:"status"`
	Amount paypalAmount `json:"amount"`
}

type paypalAuthorization struct {
	ID     string       `json:"id"`
	Status string       `json:"status"`
	Amount paypalAmount `json:"amount"`
}

type paypalRefund struct {
	ID     string       `json:"id"`
	Status string       `json:"status"`
	Amount paypalAmount `json:"amount"`
}

type paypalAPIError struct {
	Name    string `json:"name"`
	Message string `json:"message"`
	DebugID string `json:"debug_id"`
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// captureIDFromOrder extracts the first capture ID from order purchase units.
func captureIDFromOrder(order *paypalOrder) string {
	for _, pu := range order.PurchaseUnits {
		if pu.Payments != nil && len(pu.Payments.Captures) > 0 {
			return pu.Payments.Captures[0].ID
		}
	}
	return order.ID
}

// authorizationIDFromOrder extracts the first authorization ID from order purchase units.
func authorizationIDFromOrder(order *paypalOrder) string {
	for _, pu := range order.PurchaseUnits {
		if pu.Payments != nil && len(pu.Payments.Authorizations) > 0 {
			return pu.Payments.Authorizations[0].ID
		}
	}
	return order.ID
}

func (p *Provider) checkAvailable() error {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.config.ClientID == "" || p.config.ClientSecret == "" {
		return processor.NewPaymentError(processor.PayPal, "NOT_CONFIGURED", "paypal processor not configured", nil)
	}
	return nil
}

func (p *Provider) payErr(code, msg string, err error) *processor.PaymentError {
	return processor.NewPaymentError(processor.PayPal, code, msg, err)
}

// Compile-time interface check.
var _ processor.PaymentProcessor = (*Provider)(nil)
