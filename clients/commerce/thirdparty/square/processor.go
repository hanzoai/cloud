package square

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	square "github.com/square/square-go-sdk/v3"
	"github.com/square/square-go-sdk/v3/core"
	"github.com/square/square-go-sdk/v3/customers/client"
	"github.com/square/square-go-sdk/v3/option"
	"github.com/square/square-go-sdk/v3/payments"
	"github.com/square/square-go-sdk/v3/refunds"

	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/payment/processor"
)

// webhookMaxAge bounds how old a Square webhook may be before we reject it
// as a replay. Square stamps each notification with created_at; anything
// outside this window is refused even if the HMAC is valid. Mirrors the
// Stripe processor's 5-minute tolerance so both providers behave the same.
const webhookMaxAge = 5 * time.Minute

// SquareProcessor implements the processor.PaymentProcessor interface
type SquareProcessor struct {
	*processor.BaseProcessor
	accessToken   string
	locationID    string
	webhookSecret string
	// webhookURL is the EXACT notification URL configured in the Square
	// dashboard webhook subscription. Square signs HMAC-SHA256 over
	// notificationURL + rawBody, so we must verify against the configured
	// URL — never one derived from the inbound request Host, which a proxy
	// can rewrite or an attacker can spoof.
	webhookURL      string
	environment     string // "sandbox" or "production"
	paymentsClient  *payments.Client
	refundsClient   *refunds.Client
	customersClient *client.Client
}

// Config holds Square processor configuration
type Config struct {
	AccessToken   string
	LocationID    string
	WebhookSecret string
	// WebhookURL is the notification URL registered with Square's webhook
	// subscription (e.g. https://api.hanzo.ai/v1/billing/webhooks/square).
	// Required to validate live Square signatures; see SquareProcessor.webhookURL.
	WebhookURL  string
	Environment string // "sandbox" or "production"
}

// NewProcessor creates a new Square processor
func NewProcessor(cfg Config) *SquareProcessor {
	sp := &SquareProcessor{
		BaseProcessor: processor.NewBaseProcessor(processor.Square, SquareSupportedCurrencies()),
		accessToken:   cfg.AccessToken,
		locationID:    cfg.LocationID,
		webhookSecret: cfg.WebhookSecret,
		webhookURL:    cfg.WebhookURL,
		environment:   cfg.Environment,
	}

	if cfg.AccessToken != "" {
		sp.initClient()
		sp.SetConfigured(true)
	}

	return sp
}

// initClient initializes the Square client
func (sp *SquareProcessor) initClient() {
	opts := []option.RequestOption{
		option.WithToken(sp.accessToken),
	}

	if sp.environment == "sandbox" {
		opts = append(opts, option.WithBaseURL("https://connect.squareupsandbox.com"))
	}

	reqOpts := core.NewRequestOptions(opts...)
	sp.paymentsClient = payments.NewClient(reqOpts)
	sp.refundsClient = refunds.NewClient(reqOpts)
	sp.customersClient = client.NewClient(reqOpts)
}

// SquareSupportedCurrencies returns currencies Square supports
func SquareSupportedCurrencies() []currency.Type {
	return []currency.Type{
		currency.USD, currency.CAD, currency.GBP, currency.EUR,
		currency.AUD, currency.JPY,
	}
}

// Type returns the processor type
func (sp *SquareProcessor) Type() processor.ProcessorType {
	return processor.Square
}

// Environment returns "sandbox" or "production" — the resolved Square API
// environment this processor talks to (it drives the API base URL). Non-secret.
func (sp *SquareProcessor) Environment() string { return sp.environment }

// LocationID returns the configured Square merchant location id. Non-secret
// (it is also returned by the public /v1/billing/payment-config endpoint).
func (sp *SquareProcessor) LocationID() string { return sp.locationID }

// Charge processes a payment
func (sp *SquareProcessor) Charge(ctx context.Context, req processor.PaymentRequest) (*processor.PaymentResult, error) {
	if err := processor.ValidateRequest(req); err != nil {
		return nil, err
	}

	idempotencyKey := uuid.New().String()

	paymentReq := &square.CreatePaymentRequest{
		SourceID:       req.Token,
		IdempotencyKey: idempotencyKey,
		AmountMoney: &square.Money{
			Amount:   square.Int64(int64(req.Amount)),
			Currency: squareCurrency(req.Currency),
		},
		LocationID:   square.String(sp.locationID),
		Autocomplete: square.Bool(true), // Capture immediately
	}

	if req.CustomerID != "" {
		paymentReq.CustomerID = square.String(req.CustomerID)
	}

	if req.Description != "" {
		paymentReq.Note = square.String(req.Description)
	}

	if req.OrderID != "" {
		paymentReq.ReferenceID = square.String(req.OrderID)
	}

	resp, err := sp.paymentsClient.Create(ctx, paymentReq)
	if err != nil {
		return &processor.PaymentResult{
			Success:      false,
			Error:        err,
			ErrorMessage: err.Error(),
		}, err
	}

	payment := resp.Payment
	fee := currency.Cents(0)
	if len(payment.ProcessingFee) > 0 && payment.ProcessingFee[0].AmountMoney != nil {
		fee = currency.Cents(*payment.ProcessingFee[0].AmountMoney.Amount)
	}

	paymentStatus := ""
	if payment.Status != nil {
		paymentStatus = *payment.Status
	}

	return &processor.PaymentResult{
		Success:       true,
		TransactionID: *payment.ID,
		ProcessorRef:  *payment.ID,
		Fee:           fee,
		Status:        paymentStatus,
		Metadata: map[string]interface{}{
			"receipt_url": payment.ReceiptURL,
			"order_id":    payment.OrderID,
		},
	}, nil
}

// Authorize authorizes a payment without capturing
func (sp *SquareProcessor) Authorize(ctx context.Context, req processor.PaymentRequest) (*processor.PaymentResult, error) {
	if err := processor.ValidateRequest(req); err != nil {
		return nil, err
	}

	idempotencyKey := uuid.New().String()

	paymentReq := &square.CreatePaymentRequest{
		SourceID:       req.Token,
		IdempotencyKey: idempotencyKey,
		AmountMoney: &square.Money{
			Amount:   square.Int64(int64(req.Amount)),
			Currency: squareCurrency(req.Currency),
		},
		LocationID:   square.String(sp.locationID),
		Autocomplete: square.Bool(false), // Authorize only
	}

	if req.CustomerID != "" {
		paymentReq.CustomerID = square.String(req.CustomerID)
	}

	if req.Description != "" {
		paymentReq.Note = square.String(req.Description)
	}

	resp, err := sp.paymentsClient.Create(ctx, paymentReq)
	if err != nil {
		return &processor.PaymentResult{
			Success:      false,
			Error:        err,
			ErrorMessage: err.Error(),
		}, err
	}

	return &processor.PaymentResult{
		Success:       true,
		TransactionID: *resp.Payment.ID,
		ProcessorRef:  *resp.Payment.ID,
		Status:        "authorized",
		Metadata: map[string]interface{}{
			"order_id": resp.Payment.OrderID,
		},
	}, nil
}

// Capture captures a previously authorized payment
func (sp *SquareProcessor) Capture(ctx context.Context, transactionID string, amount currency.Cents) (*processor.PaymentResult, error) {
	resp, err := sp.paymentsClient.Complete(ctx, &square.CompletePaymentRequest{
		PaymentID: transactionID,
	})
	if err != nil {
		return &processor.PaymentResult{
			Success:      false,
			Error:        err,
			ErrorMessage: err.Error(),
		}, err
	}

	payment := resp.Payment
	fee := currency.Cents(0)
	if len(payment.ProcessingFee) > 0 && payment.ProcessingFee[0].AmountMoney != nil {
		fee = currency.Cents(*payment.ProcessingFee[0].AmountMoney.Amount)
	}

	return &processor.PaymentResult{
		Success:       true,
		TransactionID: *payment.ID,
		ProcessorRef:  *payment.ID,
		Fee:           fee,
		Status:        "captured",
	}, nil
}

// Refund processes a refund
func (sp *SquareProcessor) Refund(ctx context.Context, req processor.RefundRequest) (*processor.RefundResult, error) {
	// Prefer a caller-supplied DETERMINISTIC idempotency key so a retried refund
	// (same key) is de-duplicated by Square — the money move is idempotent even
	// across pods/mutexes. Fall back to a random key only for legacy callers that
	// don't supply one (non-idempotent — a retry would double-refund).
	idempotencyKey := req.IdempotencyKey
	if idempotencyKey == "" {
		idempotencyKey = uuid.New().String()
	}
	// Square caps the idempotency key at 45 chars; a sha256-hex derived key is
	// 64, so truncate deterministically (still collision-safe for our scope).
	if len(idempotencyKey) > 45 {
		idempotencyKey = idempotencyKey[:45]
	}

	refundReq := &square.RefundPaymentRequest{
		IdempotencyKey: idempotencyKey,
		PaymentID:      square.String(req.TransactionID),
		AmountMoney: &square.Money{
			Amount:   square.Int64(int64(req.Amount)),
			Currency: squareCurrency(currency.USD), // Would get from transaction
		},
	}

	if req.Reason != "" {
		refundReq.Reason = square.String(req.Reason)
	}

	resp, err := sp.refundsClient.RefundPayment(ctx, refundReq)
	if err != nil {
		return &processor.RefundResult{
			Success:      false,
			Error:        err,
			ErrorMessage: err.Error(),
		}, err
	}

	return &processor.RefundResult{
		Success:      true,
		RefundID:     resp.Refund.ID,
		ProcessorRef: resp.Refund.ID,
	}, nil
}

// GetTransaction retrieves transaction details
func (sp *SquareProcessor) GetTransaction(ctx context.Context, txID string) (*processor.Transaction, error) {
	resp, err := sp.paymentsClient.Get(ctx, &square.GetPaymentsRequest{
		PaymentID: txID,
	})
	if err != nil {
		return nil, err
	}

	payment := resp.Payment
	fee := currency.Cents(0)
	if len(payment.ProcessingFee) > 0 && payment.ProcessingFee[0].AmountMoney != nil {
		fee = currency.Cents(*payment.ProcessingFee[0].AmountMoney.Amount)
	}

	createdAt := int64(0)
	if payment.CreatedAt != nil {
		if t, err := time.Parse(time.RFC3339, *payment.CreatedAt); err == nil {
			createdAt = t.Unix()
		}
	}

	customerID := ""
	if payment.CustomerID != nil {
		customerID = *payment.CustomerID
	}

	paymentStatus := ""
	if payment.Status != nil {
		paymentStatus = *payment.Status
	}

	return &processor.Transaction{
		ID:           *payment.ID,
		ProcessorRef: *payment.ID,
		Type:         "charge",
		Amount:       currency.Cents(*payment.AmountMoney.Amount),
		Currency:     currency.Type(*payment.AmountMoney.Currency),
		Status:       paymentStatus,
		Fee:          fee,
		CustomerID:   customerID,
		CreatedAt:    createdAt,
		UpdatedAt:    createdAt,
	}, nil
}

// ValidateWebhook validates an incoming webhook and parses the event.
//
// Square signs each notification as
//
//	base64(HMAC-SHA256(signatureKey, notificationURL + rawBody))
//
// where notificationURL is the EXACT URL configured in the dashboard webhook
// subscription — not the body alone, and not a URL derived from the inbound
// request (which a proxy can rewrite). The signature arrives base64-encoded in
// the x-square-hmacsha256-signature header. We decode both the header and our
// computed digest to raw bytes and compare with hmac.Equal for a clean
// constant-time check.
//
// In addition to the existing event_id idempotency, events older than
// webhookMaxAge are rejected as replays.
func (sp *SquareProcessor) ValidateWebhook(ctx context.Context, payload []byte, signature string) (*processor.WebhookEvent, error) {
	if sp.webhookSecret == "" {
		return nil, processor.ErrWebhookValidationFailed
	}
	if sp.webhookURL == "" {
		// Square's documented signature is HMAC over notificationURL+body. With
		// no configured URL we cannot reproduce it and must refuse rather than
		// silently fall back to a body-only scheme that live Square would never
		// match. Configure SQUARE_WEBHOOK_URL to the exact dashboard URL.
		return nil, fmt.Errorf("%w: square webhook URL not configured", processor.ErrWebhookValidationFailed)
	}

	// Square signs notificationURL + rawBody (NOT the body alone).
	// See https://developer.squareup.com/docs/webhooks/step3validate
	mac := hmac.New(sha256.New, []byte(sp.webhookSecret))
	mac.Write([]byte(sp.webhookURL))
	mac.Write(payload)
	expected := mac.Sum(nil)

	// Constant-time comparison defeats timing analysis on the signature.
	// Square sends the signature base64-encoded; decode to compare raw bytes.
	provided, err := base64.StdEncoding.DecodeString(strings.TrimSpace(signature))
	if err != nil {
		return nil, processor.ErrWebhookValidationFailed
	}
	if !hmac.Equal(provided, expected) {
		return nil, processor.ErrWebhookValidationFailed
	}

	// Parse the webhook event JSON
	var evt struct {
		MerchantID string `json:"merchant_id"`
		Type       string `json:"type"`
		EventID    string `json:"event_id"`
		CreatedAt  string `json:"created_at"`
		Data       struct {
			Type   string                 `json:"type"`
			ID     string                 `json:"id"`
			Object map[string]interface{} `json:"object"`
		} `json:"data"`
	}

	if err := json.Unmarshal(payload, &evt); err != nil {
		return nil, fmt.Errorf("failed to parse Square webhook: %w", err)
	}

	// Replay protection: reject events whose created_at is older than the
	// tolerance. Mirrors the Stripe processor's timestamp check.
	ts := time.Now().Unix()
	if evt.CreatedAt != "" {
		t, perr := time.Parse(time.RFC3339, evt.CreatedAt)
		if perr != nil {
			return nil, fmt.Errorf("%w: unparseable created_at %q", processor.ErrWebhookValidationFailed, evt.CreatedAt)
		}
		if time.Since(t) > webhookMaxAge {
			return nil, fmt.Errorf("%w: event %s older than %s", processor.ErrWebhookValidationFailed, evt.EventID, webhookMaxAge)
		}
		ts = t.Unix()
	}

	return &processor.WebhookEvent{
		ID:        evt.EventID,
		Type:      mapSquareEventType(evt.Type),
		Processor: processor.Square,
		Data:      evt.Data.Object,
		Timestamp: ts,
	}, nil
}

// mapSquareEventType converts Square event types to normalized types.
func mapSquareEventType(sqType string) string {
	switch sqType {
	case "payment.completed":
		return "payment.completed"
	case "payment.created":
		return "payment.created"
	case "payment.updated":
		return "payment.updated"
	case "refund.created":
		return "refund.created"
	case "refund.updated":
		return "refund.updated"
	case "invoice.payment_made":
		return "invoice.paid"
	case "subscription.created":
		return "subscription.created"
	case "subscription.updated":
		return "subscription.updated"
	case "dispute.created":
		return "dispute.created"
	case "dispute.state.changed":
		return "dispute.updated"
	case "customer.created":
		return "customer.created"
	case "customer.updated":
		return "customer.updated"
	case "customer.deleted":
		return "customer.deleted"
	default:
		return sqType
	}
}

// CancelAuthorization voids a previously authorized (uncaptured) payment.
// Call this immediately after a successful pre-auth to release the hold.
func (sp *SquareProcessor) CancelAuthorization(ctx context.Context, paymentID string) error {
	_, err := sp.paymentsClient.Cancel(ctx, &square.CancelPaymentsRequest{
		PaymentID: paymentID,
	})
	if err != nil {
		return fmt.Errorf("cancel authorization %s: %w", paymentID, err)
	}
	return nil
}

// IsAvailable checks if the processor is configured and available
func (sp *SquareProcessor) IsAvailable(ctx context.Context) bool {
	return sp.accessToken != "" && sp.locationID != "" && sp.paymentsClient != nil
}

// squareCurrency converts currency.Type to Square currency pointer.
// Square requires uppercase ISO 4217 codes (e.g. "USD" not "usd").
func squareCurrency(c currency.Type) *square.Currency {
	curr := square.Currency(strings.ToUpper(string(c)))
	return &curr
}

// Ensure SquareProcessor implements PaymentProcessor
var _ processor.PaymentProcessor = (*SquareProcessor)(nil)
