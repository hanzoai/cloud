package adyen

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/payment/processor"
)

// Environment selects Adyen test or live endpoints.
type Environment string

const (
	Test Environment = "test"
	Live Environment = "live"
)

const (
	apiVersion     = "v71"
	testBaseURL    = "https://checkout-test.adyen.com"
	defaultTimeout = 30 * time.Second
)

// Config holds all Adyen configuration.
type Config struct {
	APIKey          string
	MerchantAccount string
	LiveURLPrefix   string // Required for live; e.g. "1797a841fbb37ca7-AdyenDemo"
	HMACKey         string // Hex-encoded HMAC key for webhook verification
	Environment     Environment
}

// Provider implements processor.PaymentProcessor for Adyen Checkout API v71.
type Provider struct {
	*processor.BaseProcessor
	config Config
	client *http.Client
}

func init() {
	processor.Register(&Provider{
		BaseProcessor: processor.NewBaseProcessor(processor.Adyen, supportedCurrencies()),
		client:        &http.Client{Timeout: defaultTimeout},
	})
}

// NewProvider creates a configured Adyen provider instance.
func NewProvider(cfg Config) *Provider {
	p := &Provider{
		BaseProcessor: processor.NewBaseProcessor(processor.Adyen, supportedCurrencies()),
		client:        &http.Client{Timeout: defaultTimeout},
	}
	p.Configure(cfg)
	return p
}

// Configure sets up the provider with Adyen credentials.
func (p *Provider) Configure(cfg Config) {
	p.config = cfg
	if p.client == nil {
		p.client = &http.Client{Timeout: defaultTimeout}
	}
	p.SetConfigured(cfg.APIKey != "" && cfg.MerchantAccount != "")
}

// Type returns the processor type.
func (p *Provider) Type() processor.ProcessorType {
	return processor.Adyen
}

// IsAvailable reports whether the processor is configured.
func (p *Provider) IsAvailable(ctx context.Context) bool {
	return p.config.APIKey != "" && p.config.MerchantAccount != ""
}

// SupportedCurrencies returns currencies supported by Adyen.
func (p *Provider) SupportedCurrencies() []currency.Type {
	return supportedCurrencies()
}

func supportedCurrencies() []currency.Type {
	return []currency.Type{
		currency.USD, currency.EUR, currency.GBP, currency.CAD,
		currency.AUD, currency.JPY, currency.CHF, currency.NZD,
		currency.SGD, currency.HKD, currency.NOK, currency.SEK,
		currency.DKK, currency.PLN, currency.BRL, currency.MXN,
		currency.KRW, currency.INR, currency.CNY, currency.ZAR,
		currency.CZK, currency.HUF, currency.RON, currency.BGN,
	}
}

// baseURL returns the correct Adyen checkout base URL for the configured environment.
func (p *Provider) baseURL() string {
	if p.config.Environment == Live {
		if p.config.LiveURLPrefix != "" {
			return fmt.Sprintf("https://%s-checkout-live.adyenpayments.com/checkout/%s",
				p.config.LiveURLPrefix, apiVersion)
		}
		return fmt.Sprintf("https://checkout-live.adyen.com/checkout/%s", apiVersion)
	}
	return fmt.Sprintf("%s/%s", testBaseURL, apiVersion)
}

// ------------------------------------------------------------------
// Adyen API request/response types
// ------------------------------------------------------------------

type adyenAmount struct {
	Value    int64  `json:"value"`
	Currency string `json:"currency"`
}

type adyenPaymentMethod struct {
	Type                string `json:"type"`
	EncryptedCardNumber string `json:"encryptedCardNumber,omitempty"`
	EncryptedExpiryMonth string `json:"encryptedExpiryMonth,omitempty"`
	EncryptedExpiryYear string `json:"encryptedExpiryYear,omitempty"`
	EncryptedSecurityCode string `json:"encryptedSecurityCode,omitempty"`
	StoredPaymentMethodID string `json:"storedPaymentMethodId,omitempty"`
	RecurringDetailReference string `json:"recurringDetailReference,omitempty"`
}

type adyenPaymentRequest struct {
	Amount             adyenAmount        `json:"amount"`
	Reference          string             `json:"reference"`
	MerchantAccount    string             `json:"merchantAccount"`
	PaymentMethod      adyenPaymentMethod `json:"paymentMethod"`
	ShopperInteraction string             `json:"shopperInteraction"`
	Channel            string             `json:"channel"`
	ShopperReference   string             `json:"shopperReference,omitempty"`
	CaptureDelayHours  *int               `json:"captureDelayHours,omitempty"`
	ShopperStatement   string             `json:"shopperStatement,omitempty"`
	Metadata           map[string]string  `json:"metadata,omitempty"`
	ReturnURL          string             `json:"returnUrl,omitempty"`
}

type adyenPaymentResponse struct {
	PSPReference   string                 `json:"pspReference"`
	ResultCode     string                 `json:"resultCode"`
	RefusalReason  string                 `json:"refusalReason,omitempty"`
	RefusalReasonCode string              `json:"refusalReasonCode,omitempty"`
	Amount         *adyenAmount           `json:"amount,omitempty"`
	MerchantReference string              `json:"merchantReference,omitempty"`
	AdditionalData map[string]interface{} `json:"additionalData,omitempty"`
}

type adyenCaptureRequest struct {
	Amount          adyenAmount       `json:"amount"`
	MerchantAccount string            `json:"merchantAccount"`
	Reference       string            `json:"reference,omitempty"`
	Metadata        map[string]string `json:"metadata,omitempty"`
}

type adyenCaptureResponse struct {
	PSPReference   string `json:"pspReference"`
	Status         string `json:"status"`
	Reference      string `json:"reference,omitempty"`
	PaymentPSPReference string `json:"paymentPspReference,omitempty"`
	Amount         *adyenAmount `json:"amount,omitempty"`
}

type adyenRefundRequest struct {
	Amount          adyenAmount       `json:"amount"`
	MerchantAccount string            `json:"merchantAccount"`
	Reference       string            `json:"reference,omitempty"`
	Metadata        map[string]string `json:"metadata,omitempty"`
}

type adyenRefundResponse struct {
	PSPReference   string `json:"pspReference"`
	Status         string `json:"status"`
	Reference      string `json:"reference,omitempty"`
	PaymentPSPReference string `json:"paymentPspReference,omitempty"`
	Amount         *adyenAmount `json:"amount,omitempty"`
}

type adyenErrorResponse struct {
	Status    int    `json:"status"`
	ErrorCode string `json:"errorCode"`
	Message   string `json:"message"`
	ErrorType string `json:"errorType"`
}

// Webhook types

type adyenNotificationRequest struct {
	Live              string                    `json:"live"`
	NotificationItems []adyenNotificationItemWrap `json:"notificationItems"`
}

type adyenNotificationItemWrap struct {
	NotificationRequestItem adyenNotificationItem `json:"NotificationRequestItem"`
}

type adyenNotificationItem struct {
	Amount             adyenAmount            `json:"amount"`
	EventCode          string                 `json:"eventCode"`
	EventDate          string                 `json:"eventDate"`
	MerchantAccountCode string                `json:"merchantAccountCode"`
	MerchantReference  string                 `json:"merchantReference"`
	Operations         []string               `json:"operations,omitempty"`
	OriginalReference  string                 `json:"originalReference,omitempty"`
	PaymentMethod      string                 `json:"paymentMethod"`
	PSPReference       string                 `json:"pspReference"`
	Reason             string                 `json:"reason,omitempty"`
	Success            string                 `json:"success"`
	AdditionalData     map[string]interface{} `json:"additionalData,omitempty"`
}

// ------------------------------------------------------------------
// PaymentProcessor implementation
// ------------------------------------------------------------------

// Charge processes a payment (authorize + auto-capture).
func (p *Provider) Charge(ctx context.Context, req processor.PaymentRequest) (*processor.PaymentResult, error) {
	if err := p.ensureAvailable(); err != nil {
		return nil, err
	}
	if err := processor.ValidateRequest(req); err != nil {
		return nil, err
	}

	return p.doPayment(ctx, req, false)
}

// Authorize authorizes a payment without capturing (manual capture).
func (p *Provider) Authorize(ctx context.Context, req processor.PaymentRequest) (*processor.PaymentResult, error) {
	if err := p.ensureAvailable(); err != nil {
		return nil, err
	}
	if err := processor.ValidateRequest(req); err != nil {
		return nil, err
	}

	return p.doPayment(ctx, req, true)
}

// doPayment performs a /payments call. When authOnly is true, captureDelayHours
// is set to -1 (manual capture); otherwise Adyen uses its default auto-capture.
func (p *Provider) doPayment(ctx context.Context, req processor.PaymentRequest, authOnly bool) (*processor.PaymentResult, error) {
	pm := buildPaymentMethod(req)

	reference := req.OrderID
	if reference == "" {
		reference = fmt.Sprintf("txn_%d", time.Now().UnixNano())
	}

	body := adyenPaymentRequest{
		Amount: adyenAmount{
			Value:    int64(req.Amount),
			Currency: req.Currency.Code(),
		},
		Reference:          reference,
		MerchantAccount:    p.config.MerchantAccount,
		PaymentMethod:      pm,
		ShopperInteraction: "Ecommerce",
		Channel:            "Web",
	}

	if req.CustomerID != "" {
		body.ShopperReference = req.CustomerID
	}

	if req.Description != "" {
		body.ShopperStatement = req.Description
	}

	if authOnly {
		manualCapture := -1
		body.CaptureDelayHours = &manualCapture
	}

	if req.Metadata != nil {
		body.Metadata = stringifyMap(req.Metadata)
	}

	// Allow callers to pass returnUrl via options (required for some payment methods).
	if retURL, ok := req.Options["returnUrl"].(string); ok && retURL != "" {
		body.ReturnURL = retURL
	}

	var resp adyenPaymentResponse
	if err := p.post(ctx, "/payments", body, &resp); err != nil {
		return nil, err
	}

	result := &processor.PaymentResult{
		TransactionID: resp.PSPReference,
		ProcessorRef:  resp.PSPReference,
		Metadata: map[string]interface{}{
			"resultCode":        resp.ResultCode,
			"merchantReference": reference,
		},
	}

	switch resp.ResultCode {
	case "Authorised":
		result.Success = true
		if authOnly {
			result.Status = "authorized"
		} else {
			result.Status = "succeeded"
		}
	case "Pending", "Received":
		result.Success = true
		result.Status = "pending"
	case "Refused":
		result.Success = false
		result.Status = "failed"
		result.ErrorMessage = formatRefusal(resp.RefusalReason, resp.RefusalReasonCode)
		result.Error = processor.NewPaymentError(processor.Adyen, resp.RefusalReasonCode,
			result.ErrorMessage, nil)
	case "Error":
		result.Success = false
		result.Status = "error"
		result.ErrorMessage = resp.RefusalReason
		result.Error = processor.NewPaymentError(processor.Adyen, "API_ERROR",
			resp.RefusalReason, nil)
	case "RedirectShopper", "IdentifyShopper", "ChallengeShopper", "PresentToShopper":
		// 3DS or redirect-based flows: return the action data so the caller can handle it.
		result.Success = true
		result.Status = "action_required"
		result.Metadata["resultCode"] = resp.ResultCode
	default:
		result.Success = false
		result.Status = "unknown"
		result.ErrorMessage = fmt.Sprintf("unexpected resultCode: %s", resp.ResultCode)
		result.Error = processor.NewPaymentError(processor.Adyen, "UNKNOWN_RESULT",
			result.ErrorMessage, nil)
	}

	return result, nil
}

// Capture captures a previously authorized payment.
func (p *Provider) Capture(ctx context.Context, transactionID string, amount currency.Cents) (*processor.PaymentResult, error) {
	if err := p.ensureAvailable(); err != nil {
		return nil, err
	}
	if transactionID == "" {
		return nil, processor.NewPaymentError(processor.Adyen, "INVALID_TRANSACTION",
			"transaction ID is required for capture", nil)
	}
	if amount <= 0 {
		return nil, processor.NewPaymentError(processor.Adyen, "INVALID_AMOUNT",
			"capture amount must be positive", nil)
	}

	// Adyen captures require currency; default to USD if not specified in metadata.
	// Callers should pass the currency via options on the original auth if needed.
	cur := "USD"

	body := adyenCaptureRequest{
		Amount: adyenAmount{
			Value:    int64(amount),
			Currency: cur,
		},
		MerchantAccount: p.config.MerchantAccount,
		Reference:       fmt.Sprintf("cap_%d", time.Now().UnixNano()),
	}

	endpoint := fmt.Sprintf("/payments/%s/captures", transactionID)

	var resp adyenCaptureResponse
	if err := p.post(ctx, endpoint, body, &resp); err != nil {
		return nil, err
	}

	return &processor.PaymentResult{
		Success:       resp.Status == "received",
		TransactionID: resp.PSPReference,
		ProcessorRef:  resp.PSPReference,
		Status:        mapCaptureStatus(resp.Status),
		Metadata: map[string]interface{}{
			"paymentPspReference": resp.PaymentPSPReference,
			"status":             resp.Status,
		},
	}, nil
}

// Refund processes a refund against a captured payment.
func (p *Provider) Refund(ctx context.Context, req processor.RefundRequest) (*processor.RefundResult, error) {
	if err := p.ensureAvailable(); err != nil {
		return nil, err
	}
	if req.TransactionID == "" {
		return nil, processor.NewPaymentError(processor.Adyen, "INVALID_TRANSACTION",
			"transaction ID is required for refund", nil)
	}
	if req.Amount <= 0 {
		return nil, processor.NewPaymentError(processor.Adyen, "INVALID_AMOUNT",
			"refund amount must be positive", nil)
	}

	cur := "USD"

	body := adyenRefundRequest{
		Amount: adyenAmount{
			Value:    int64(req.Amount),
			Currency: cur,
		},
		MerchantAccount: p.config.MerchantAccount,
		Reference:       fmt.Sprintf("ref_%d", time.Now().UnixNano()),
	}

	if req.Metadata != nil {
		body.Metadata = stringifyMap(req.Metadata)
	}

	endpoint := fmt.Sprintf("/payments/%s/refunds", req.TransactionID)

	var resp adyenRefundResponse
	if err := p.post(ctx, endpoint, body, &resp); err != nil {
		return nil, err
	}

	return &processor.RefundResult{
		Success:      resp.Status == "received",
		RefundID:     resp.PSPReference,
		ProcessorRef: resp.PSPReference,
	}, nil
}

// GetTransaction retrieves transaction details. Adyen does not expose a direct
// GET endpoint for individual transactions via the Checkout API. Instead, the
// pspReference from the original payment response serves as the canonical
// transaction identifier.
//
// This implementation returns a minimal Transaction populated from the
// pspReference. For full transaction history, use Adyen's Reports or Data API.
func (p *Provider) GetTransaction(ctx context.Context, txID string) (*processor.Transaction, error) {
	if err := p.ensureAvailable(); err != nil {
		return nil, err
	}
	if txID == "" {
		return nil, processor.NewPaymentError(processor.Adyen, "INVALID_TRANSACTION",
			"transaction ID is required", nil)
	}

	// Adyen Checkout API has no single-transaction GET. Return a reference
	// transaction. Real enrichment should come from webhook data or the
	// Adyen Reporting API, stored locally.
	now := time.Now().Unix()
	return &processor.Transaction{
		ID:           txID,
		ProcessorRef: txID,
		Type:         "charge",
		Status:       "unknown",
		CreatedAt:    now,
		UpdatedAt:    now,
		Metadata: map[string]interface{}{
			"note": "adyen checkout API does not support single-transaction retrieval; use webhook data or reporting API",
		},
	}, nil
}

// ValidateWebhook validates an Adyen webhook notification using HMAC-SHA256.
//
// Adyen computes the HMAC over a concatenation of specific notification fields
// in a defined order, separated by colons. The signature is base64-encoded and
// sent inside each NotificationRequestItem's additionalData.hmacSignature.
//
// The `signature` parameter is accepted for interface compliance but the actual
// HMAC is read from the parsed notification payload (additionalData.hmacSignature).
func (p *Provider) ValidateWebhook(ctx context.Context, payload []byte, signature string) (*processor.WebhookEvent, error) {
	if err := p.ensureAvailable(); err != nil {
		return nil, err
	}

	var notif adyenNotificationRequest
	if err := json.Unmarshal(payload, &notif); err != nil {
		return nil, processor.NewPaymentError(processor.Adyen, "INVALID_PAYLOAD",
			"failed to parse webhook payload", err)
	}

	if len(notif.NotificationItems) == 0 {
		return nil, processor.NewPaymentError(processor.Adyen, "EMPTY_NOTIFICATION",
			"webhook contains no notification items", nil)
	}

	// Process the first notification item. In practice, Adyen sends one item
	// per webhook request for standard integrations.
	item := notif.NotificationItems[0].NotificationRequestItem

	// HMAC verification: if a key is configured, enforce signature validation.
	// If no key is configured (e.g. development/test environment), skip verification.
	if p.config.HMACKey != "" {
		hmacSig, _ := item.AdditionalData["hmacSignature"].(string)
		if hmacSig == "" && signature != "" {
			hmacSig = signature
		}
		if hmacSig == "" {
			return nil, fmt.Errorf("%w: missing hmacSignature", processor.ErrWebhookValidationFailed)
		}
		if !p.verifyHMAC(item, hmacSig) {
			return nil, fmt.Errorf("%w: HMAC mismatch", processor.ErrWebhookValidationFailed)
		}
	}

	// Verify merchant account matches.
	if item.MerchantAccountCode != p.config.MerchantAccount {
		return nil, fmt.Errorf("%w: merchant account mismatch", processor.ErrWebhookValidationFailed)
	}

	eventType := mapWebhookEventType(item.EventCode, item.Success)

	var ts int64
	if t, err := time.Parse("2006-01-02T15:04:05-07:00", item.EventDate); err == nil {
		ts = t.Unix()
	} else {
		ts = time.Now().Unix()
	}

	return &processor.WebhookEvent{
		ID:        item.PSPReference,
		Type:      eventType,
		Processor: processor.Adyen,
		Data: map[string]interface{}{
			"eventCode":         item.EventCode,
			"success":           item.Success,
			"pspReference":      item.PSPReference,
			"originalReference": item.OriginalReference,
			"merchantReference": item.MerchantReference,
			"paymentMethod":     item.PaymentMethod,
			"reason":            item.Reason,
			"amount":            item.Amount,
			"live":              notif.Live,
		},
		Timestamp: ts,
	}, nil
}

// ------------------------------------------------------------------
// HMAC verification
// ------------------------------------------------------------------

// verifyHMAC validates the Adyen notification HMAC signature.
//
// The signing string is built by concatenating the following fields with ':'
// separators, in this exact order:
//   pspReference + merchantReference + amount.value + amount.currency +
//   eventCode + success
//
// The HMAC key is hex-encoded in Adyen's Customer Area. It is decoded to raw
// bytes before computing HMAC-SHA256. The expected signature is base64-encoded.
func (p *Provider) verifyHMAC(item adyenNotificationItem, expectedSig string) bool {
	keyBytes, err := hex.DecodeString(p.config.HMACKey)
	if err != nil {
		return false
	}

	signingString := strings.Join([]string{
		item.PSPReference,
		item.MerchantReference,
		fmt.Sprintf("%d", item.Amount.Value),
		item.Amount.Currency,
		item.EventCode,
		item.Success,
	}, ":")

	mac := hmac.New(sha256.New, keyBytes)
	mac.Write([]byte(signingString))
	computed := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	return hmac.Equal([]byte(computed), []byte(expectedSig))
}

// ------------------------------------------------------------------
// HTTP transport
// ------------------------------------------------------------------

// post sends a JSON POST request to the Adyen API and decodes the response.
func (p *Provider) post(ctx context.Context, endpoint string, body interface{}, result interface{}) error {
	url := p.baseURL() + endpoint

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return processor.NewPaymentError(processor.Adyen, "MARSHAL_ERROR",
			"failed to marshal request body", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(jsonBody))
	if err != nil {
		return processor.NewPaymentError(processor.Adyen, "REQUEST_ERROR",
			"failed to create HTTP request", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-API-Key", p.config.APIKey)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return processor.NewPaymentError(processor.Adyen, "NETWORK_ERROR",
			"failed to send request to Adyen", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return processor.NewPaymentError(processor.Adyen, "READ_ERROR",
			"failed to read Adyen response", err)
	}

	// Adyen returns 2xx for successful API calls, including refused payments.
	// 4xx/5xx indicate request-level errors (bad auth, invalid request, etc.).
	if resp.StatusCode >= 400 {
		var apiErr adyenErrorResponse
		if json.Unmarshal(respBody, &apiErr) == nil && apiErr.Message != "" {
			return processor.NewPaymentError(processor.Adyen, apiErr.ErrorCode,
				fmt.Sprintf("adyen API error (HTTP %d): %s", resp.StatusCode, apiErr.Message), nil)
		}
		return processor.NewPaymentError(processor.Adyen, fmt.Sprintf("HTTP_%d", resp.StatusCode),
			fmt.Sprintf("adyen API error (HTTP %d): %s", resp.StatusCode, string(respBody)), nil)
	}

	if err := json.Unmarshal(respBody, result); err != nil {
		return processor.NewPaymentError(processor.Adyen, "DECODE_ERROR",
			"failed to decode Adyen response", err)
	}

	return nil
}

// ------------------------------------------------------------------
// Helpers
// ------------------------------------------------------------------

// ensureAvailable returns an error if the provider is not configured.
func (p *Provider) ensureAvailable() error {
	if p.config.APIKey == "" || p.config.MerchantAccount == "" {
		return processor.NewPaymentError(processor.Adyen, "NOT_CONFIGURED",
			"adyen processor not configured", nil)
	}
	return nil
}

// buildPaymentMethod constructs the paymentMethod object from the request.
//
// Token handling:
//   - If Token looks like a stored payment method ID (starts with "8" and is 16 digits),
//     it is treated as a storedPaymentMethodId.
//   - Otherwise, Token is treated as an encrypted card number (client-side encryption).
//   - Additional encrypted fields can be passed via Options:
//     "encryptedExpiryMonth", "encryptedExpiryYear", "encryptedSecurityCode"
func buildPaymentMethod(req processor.PaymentRequest) adyenPaymentMethod {
	pm := adyenPaymentMethod{
		Type: "scheme",
	}

	token := req.Token

	// Check if this is a stored payment method reference.
	if isStoredPaymentMethodID(token) {
		pm.StoredPaymentMethodID = token
		return pm
	}

	// Check for recurring detail reference.
	if rdRef, ok := req.Options["recurringDetailReference"].(string); ok && rdRef != "" {
		pm.RecurringDetailReference = rdRef
		return pm
	}

	// Treat token as encrypted card number from Adyen client-side encryption.
	if token != "" {
		pm.EncryptedCardNumber = token
	}

	if v, ok := req.Options["encryptedExpiryMonth"].(string); ok {
		pm.EncryptedExpiryMonth = v
	}
	if v, ok := req.Options["encryptedExpiryYear"].(string); ok {
		pm.EncryptedExpiryYear = v
	}
	if v, ok := req.Options["encryptedSecurityCode"].(string); ok {
		pm.EncryptedSecurityCode = v
	}

	// Allow overriding payment method type (e.g. "ideal", "applepay", "googlepay").
	if pmType, ok := req.Options["paymentMethodType"].(string); ok && pmType != "" {
		pm.Type = pmType
	}

	return pm
}

// isStoredPaymentMethodID heuristically detects Adyen stored payment method IDs.
// These are typically 16-digit numeric strings starting with "8".
func isStoredPaymentMethodID(token string) bool {
	if len(token) != 16 {
		return false
	}
	for _, c := range token {
		if c < '0' || c > '9' {
			return false
		}
	}
	return token[0] == '8'
}

// formatRefusal formats a human-readable refusal message.
func formatRefusal(reason, code string) string {
	if reason == "" && code == "" {
		return "payment refused"
	}
	if code == "" {
		return reason
	}
	if reason == "" {
		return fmt.Sprintf("refused (code: %s)", code)
	}
	return fmt.Sprintf("%s (code: %s)", reason, code)
}

// mapCaptureStatus maps Adyen capture status to our internal status.
func mapCaptureStatus(status string) string {
	switch status {
	case "received":
		return "capture_pending"
	default:
		return status
	}
}

// mapWebhookEventType maps Adyen eventCode + success to a normalized event type.
func mapWebhookEventType(eventCode, success string) string {
	ok := success == "true"
	switch eventCode {
	case "AUTHORISATION":
		if ok {
			return "payment.authorized"
		}
		return "payment.refused"
	case "CAPTURE":
		if ok {
			return "payment.captured"
		}
		return "payment.capture_failed"
	case "CAPTURE_FAILED":
		return "payment.capture_failed"
	case "CANCELLATION":
		if ok {
			return "payment.cancelled"
		}
		return "payment.cancel_failed"
	case "REFUND":
		if ok {
			return "refund.succeeded"
		}
		return "refund.failed"
	case "REFUND_FAILED":
		return "refund.failed"
	case "REFUNDED_REVERSED":
		return "refund.reversed"
	case "CHARGEBACK":
		return "dispute.created"
	case "CHARGEBACK_REVERSED":
		return "dispute.reversed"
	case "SECOND_CHARGEBACK":
		return "dispute.second_chargeback"
	case "NOTIFICATION_OF_CHARGEBACK":
		return "dispute.notification"
	case "PREARBITRATION_LOST":
		return "dispute.lost"
	case "PREARBITRATION_WON":
		return "dispute.won"
	case "REQUEST_FOR_INFORMATION":
		return "dispute.information_requested"
	case "REPORT_AVAILABLE":
		return "report.available"
	case "PAIDOUT_REVERSED":
		return "payout.reversed"
	case "PAYOUT_DECLINE":
		return "payout.declined"
	case "PAYOUT_EXPIRE":
		return "payout.expired"
	case "PAYOUT_THIRDPARTY":
		if ok {
			return "payout.succeeded"
		}
		return "payout.failed"
	case "RECURRING_CONTRACT":
		if ok {
			return "token.created"
		}
		return "token.failed"
	default:
		return fmt.Sprintf("adyen.%s.%s", strings.ToLower(eventCode), success)
	}
}

// stringifyMap converts map[string]interface{} to map[string]string for Adyen metadata.
func stringifyMap(m map[string]interface{}) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = fmt.Sprintf("%v", v)
	}
	return out
}

// Compile-time interface check.
var _ processor.PaymentProcessor = (*Provider)(nil)
