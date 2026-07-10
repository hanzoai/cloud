package recurly

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/payment/processor"
)

const (
	baseURL    = "https://v3.recurly.com"
	apiVersion = "v2021-02-25"
	userAgent  = "HanzoCommerce/1.0"
)

// Config holds Recurly API credentials.
type Config struct {
	APIKey    string
	Subdomain string
}

// Provider implements processor.PaymentProcessor for Recurly.
type Provider struct {
	*processor.BaseProcessor
	apiKey    string
	subdomain string
	client    *http.Client
}

func init() {
	processor.Register(&Provider{
		BaseProcessor: processor.NewBaseProcessor(processor.Recurly, supportedCurrencies()),
		client:        &http.Client{Timeout: 30 * time.Second},
	})
}

// NewProvider creates a configured Recurly provider instance.
func NewProvider(cfg Config) *Provider {
	p := &Provider{
		BaseProcessor: processor.NewBaseProcessor(processor.Recurly, supportedCurrencies()),
		client:        &http.Client{Timeout: 30 * time.Second},
	}
	p.Configure(cfg.APIKey, cfg.Subdomain)
	return p
}

// Configure sets the API key, optional subdomain, and marks the processor as available.
func (p *Provider) Configure(apiKey string, opts ...string) {
	p.apiKey = apiKey
	if len(opts) > 0 && opts[0] != "" {
		p.subdomain = opts[0]
	}
	if p.client == nil {
		p.client = &http.Client{Timeout: 30 * time.Second}
	}
	p.SetConfigured(apiKey != "")
}

// Type returns the processor type.
func (p *Provider) Type() processor.ProcessorType {
	return processor.Recurly
}

// IsAvailable reports whether the processor is configured.
func (p *Provider) IsAvailable(ctx context.Context) bool {
	return p.apiKey != ""
}

// SupportedCurrencies returns currencies supported by Recurly.
func (p *Provider) SupportedCurrencies() []currency.Type {
	return supportedCurrencies()
}

func supportedCurrencies() []currency.Type {
	return []currency.Type{
		currency.USD, currency.EUR, currency.GBP, currency.CAD,
		currency.AUD, currency.JPY, currency.CHF, currency.NZD,
		currency.MXN, currency.BRL, currency.DKK, currency.NOK,
		currency.SEK, currency.PLN, currency.CZK, currency.HUF,
	}
}

// Charge processes a one-time payment via Recurly purchase endpoint.
func (p *Provider) Charge(ctx context.Context, req processor.PaymentRequest) (*processor.PaymentResult, error) {
	if err := p.checkAvailable(ctx); err != nil {
		return nil, err
	}
	if err := processor.ValidateRequest(req); err != nil {
		return nil, processor.NewPaymentError(processor.Recurly, "INVALID_REQUEST", err.Error(), err)
	}
	if !processor.SupportsCurrency(p, req.Currency) {
		return nil, processor.NewPaymentError(processor.Recurly, "UNSUPPORTED_CURRENCY",
			fmt.Sprintf("currency %s not supported", req.Currency), processor.ErrCurrencyNotSupported)
	}

	body := p.buildPurchaseBody(req, "automatic")
	resp, err := p.doRequest(ctx, http.MethodPost, "/purchases", body)
	if err != nil {
		return nil, processor.NewPaymentError(processor.Recurly, "API_ERROR", "recurly charge request failed", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, processor.NewPaymentError(processor.Recurly, "READ_ERROR", "failed to read response", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return p.handleErrorResponse(respBody, resp.StatusCode, "charge")
	}

	return p.parsePurchaseResponse(respBody)
}

// Authorize creates a purchase with manual collection (pending invoice).
func (p *Provider) Authorize(ctx context.Context, req processor.PaymentRequest) (*processor.PaymentResult, error) {
	if err := p.checkAvailable(ctx); err != nil {
		return nil, err
	}
	if err := processor.ValidateRequest(req); err != nil {
		return nil, processor.NewPaymentError(processor.Recurly, "INVALID_REQUEST", err.Error(), err)
	}
	if !processor.SupportsCurrency(p, req.Currency) {
		return nil, processor.NewPaymentError(processor.Recurly, "UNSUPPORTED_CURRENCY",
			fmt.Sprintf("currency %s not supported", req.Currency), processor.ErrCurrencyNotSupported)
	}

	body := p.buildPurchaseBody(req, "manual")
	resp, err := p.doRequest(ctx, http.MethodPost, "/purchases", body)
	if err != nil {
		return nil, processor.NewPaymentError(processor.Recurly, "API_ERROR", "recurly authorize request failed", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, processor.NewPaymentError(processor.Recurly, "READ_ERROR", "failed to read response", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return p.handleErrorResponse(respBody, resp.StatusCode, "authorize")
	}

	result, err := p.parsePurchaseResponse(respBody)
	if err != nil {
		return nil, err
	}
	result.Status = "authorized"
	return result, nil
}

// Capture collects a pending invoice (from a manual-collection purchase).
// The transactionID should be the invoice ID returned from Authorize.
func (p *Provider) Capture(ctx context.Context, transactionID string, amount currency.Cents) (*processor.PaymentResult, error) {
	if err := p.checkAvailable(ctx); err != nil {
		return nil, err
	}
	if transactionID == "" {
		return nil, processor.NewPaymentError(processor.Recurly, "INVALID_REQUEST", "transaction ID (invoice ID) required", nil)
	}

	// Recurly capture = collect a pending invoice.
	path := fmt.Sprintf("/invoices/%s/collect", transactionID)
	resp, err := p.doRequest(ctx, http.MethodPut, path, nil)
	if err != nil {
		return nil, processor.NewPaymentError(processor.Recurly, "API_ERROR", "recurly capture request failed", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, processor.NewPaymentError(processor.Recurly, "READ_ERROR", "failed to read response", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return p.handleErrorResponse(respBody, resp.StatusCode, "capture")
	}

	var invoice recurlyInvoice
	if err := json.Unmarshal(respBody, &invoice); err != nil {
		return nil, processor.NewPaymentError(processor.Recurly, "PARSE_ERROR", "failed to parse capture response", err)
	}

	txnID := invoice.ID
	processorRef := invoice.Number
	if len(invoice.Transactions) > 0 {
		txnID = invoice.Transactions[0].ID
		processorRef = invoice.Transactions[0].UUID
	}

	return &processor.PaymentResult{
		Success:       invoice.State == "paid",
		TransactionID: txnID,
		ProcessorRef:  processorRef,
		Status:        invoice.State,
		Metadata: map[string]interface{}{
			"invoice_id":     invoice.ID,
			"invoice_number": invoice.Number,
			"invoice_state":  invoice.State,
		},
	}, nil
}

// Refund processes a refund on an invoice.
// The TransactionID on the RefundRequest should be the invoice ID.
func (p *Provider) Refund(ctx context.Context, req processor.RefundRequest) (*processor.RefundResult, error) {
	if err := p.checkAvailable(ctx); err != nil {
		return nil, err
	}
	if req.TransactionID == "" {
		return nil, processor.NewPaymentError(processor.Recurly, "INVALID_REQUEST", "transaction ID (invoice ID) required for refund", nil)
	}
	if req.Amount <= 0 {
		return nil, processor.NewPaymentError(processor.Recurly, "INVALID_REQUEST", "refund amount must be positive", nil)
	}

	// If the TransactionID is a transaction (starts with a transaction prefix),
	// we first look up the transaction to get the invoice ID.
	invoiceID := req.TransactionID
	if strings.HasPrefix(req.TransactionID, "a]") || !strings.HasPrefix(req.TransactionID, "/") {
		// Attempt to look up as a transaction to find the associated invoice.
		txn, err := p.fetchTransaction(ctx, req.TransactionID)
		if err == nil && txn.InvoiceID != "" {
			invoiceID = txn.InvoiceID
		}
		// If lookup fails, proceed with the ID as-is (it may already be an invoice ID).
	}

	reason := req.Reason
	if reason == "" {
		reason = "Refund"
	}

	refundBody := recurlyRefundRequest{
		Type:                "amount",
		Amount:              centsToDecimalString(req.Amount, false),
		CreditCustomerNotes: reason,
	}

	bodyBytes, err := json.Marshal(refundBody)
	if err != nil {
		return nil, processor.NewPaymentError(processor.Recurly, "MARSHAL_ERROR", "failed to build refund body", err)
	}

	path := fmt.Sprintf("/invoices/%s/refund", invoiceID)
	resp, err := p.doRequest(ctx, http.MethodPost, path, bodyBytes)
	if err != nil {
		return nil, processor.NewPaymentError(processor.Recurly, "API_ERROR", "recurly refund request failed", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, processor.NewPaymentError(processor.Recurly, "READ_ERROR", "failed to read refund response", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		apiErr := p.parseAPIError(respBody)
		return &processor.RefundResult{
			Success:      false,
			ErrorMessage: fmt.Sprintf("recurly refund failed (HTTP %d): %s", resp.StatusCode, apiErr),
			Error:        processor.ErrRefundFailed,
		}, nil
	}

	var invoice recurlyInvoice
	if err := json.Unmarshal(respBody, &invoice); err != nil {
		return nil, processor.NewPaymentError(processor.Recurly, "PARSE_ERROR", "failed to parse refund response", err)
	}

	return &processor.RefundResult{
		Success:      true,
		RefundID:     invoice.ID,
		ProcessorRef: invoice.Number,
	}, nil
}

// GetTransaction retrieves a transaction from Recurly.
func (p *Provider) GetTransaction(ctx context.Context, txID string) (*processor.Transaction, error) {
	if err := p.checkAvailable(ctx); err != nil {
		return nil, err
	}
	if txID == "" {
		return nil, processor.NewPaymentError(processor.Recurly, "INVALID_REQUEST", "transaction ID required", nil)
	}

	txn, err := p.fetchTransaction(ctx, txID)
	if err != nil {
		return nil, err
	}

	amountCents := decimalToCents(txn.Amount, currency.Type(strings.ToLower(txn.Currency)))

	var createdAt, updatedAt int64
	if t, err := time.Parse(time.RFC3339, txn.CreatedAt); err == nil {
		createdAt = t.Unix()
	}
	if t, err := time.Parse(time.RFC3339, txn.UpdatedAt); err == nil {
		updatedAt = t.Unix()
	}

	txType := "charge"
	if txn.Type == "refund" {
		txType = "refund"
	}

	return &processor.Transaction{
		ID:           txn.ID,
		ProcessorRef: txn.UUID,
		Type:         txType,
		Amount:       amountCents,
		Currency:     currency.Type(strings.ToLower(txn.Currency)),
		Status:       txn.Status,
		CustomerID:   txn.AccountCode,
		Metadata: map[string]interface{}{
			"uuid":             txn.UUID,
			"payment_method":   txn.PaymentMethod,
			"collection_method": txn.CollectionMethod,
			"origin":           txn.Origin,
		},
		CreatedAt: createdAt,
		UpdatedAt: updatedAt,
	}, nil
}

// ValidateWebhook parses a Recurly webhook notification.
// Recurly sends XML push notifications; there is no cryptographic signature verification.
// IP allowlisting is recommended for production security.
func (p *Provider) ValidateWebhook(ctx context.Context, payload []byte, signature string) (*processor.WebhookEvent, error) {
	if err := p.checkAvailable(ctx); err != nil {
		return nil, err
	}
	if len(payload) == 0 {
		return nil, processor.NewPaymentError(processor.Recurly, "INVALID_WEBHOOK", "empty webhook payload", processor.ErrWebhookValidationFailed)
	}

	// Recurly webhooks are XML. We parse the root element to determine event type,
	// then extract nested data.
	event, err := parseWebhookXML(payload)
	if err != nil {
		return nil, processor.NewPaymentError(processor.Recurly, "PARSE_ERROR", "failed to parse webhook XML", err)
	}

	return event, nil
}

// --- Internal helpers ---

func (p *Provider) checkAvailable(ctx context.Context) error {
	if !p.IsAvailable(ctx) {
		return processor.NewPaymentError(processor.Recurly, "NOT_CONFIGURED", "recurly processor not configured", nil)
	}
	return nil
}

func (p *Provider) authHeader() string {
	encoded := base64.StdEncoding.EncodeToString([]byte(p.apiKey + ":"))
	return "Basic " + encoded
}

func (p *Provider) doRequest(ctx context.Context, method, path string, body []byte) (*http.Response, error) {
	url := baseURL + path

	var bodyReader io.Reader
	if body != nil {
		bodyReader = bytes.NewReader(body)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", p.authHeader())
	req.Header.Set("Accept", "application/vnd.recurly."+apiVersion)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", userAgent)

	return p.client.Do(req)
}

func (p *Provider) buildPurchaseBody(req processor.PaymentRequest, collectionMethod string) []byte {
	cur := req.Currency.Code()
	amountStr := centsToDecimal(req.Amount, req.Currency)

	description := req.Description
	if description == "" {
		description = "Payment"
	}

	purchase := recurlyPurchase{
		Currency: cur,
		Account: recurlyAccount{
			Code: p.resolveAccountCode(req),
			BillingInfo: &recurlyBillingInfo{
				TokenID: req.Token,
			},
		},
		LineItems: []recurlyLineItem{
			{
				Currency:    cur,
				UnitAmount:  amountStr,
				Quantity:    1,
				Type:        "charge",
				Description: description,
			},
		},
		CollectionMethod: collectionMethod,
	}

	// Omit billing_info if no token provided (account may already have payment on file).
	if req.Token == "" {
		purchase.Account.BillingInfo = nil
	}

	bodyBytes, _ := json.Marshal(purchase)
	return bodyBytes
}

func (p *Provider) resolveAccountCode(req processor.PaymentRequest) string {
	if req.CustomerID != "" {
		return req.CustomerID
	}
	if req.OrderID != "" {
		return req.OrderID
	}
	// Generate a unique code from timestamp as last resort.
	return fmt.Sprintf("hanzo-%d", time.Now().UnixNano())
}

func (p *Provider) parsePurchaseResponse(body []byte) (*processor.PaymentResult, error) {
	var collection recurlyInvoiceCollection
	if err := json.Unmarshal(body, &collection); err != nil {
		return nil, processor.NewPaymentError(processor.Recurly, "PARSE_ERROR", "failed to parse purchase response", err)
	}

	invoice := collection.ChargeInvoice
	if invoice.ID == "" {
		// Some responses return the invoice directly rather than wrapped in a collection.
		var directInvoice recurlyInvoice
		if err := json.Unmarshal(body, &directInvoice); err == nil && directInvoice.ID != "" {
			invoice = directInvoice
		}
	}

	if invoice.ID == "" {
		return nil, processor.NewPaymentError(processor.Recurly, "EMPTY_RESPONSE", "no invoice returned from purchase", nil)
	}

	result := &processor.PaymentResult{
		Success: invoice.State == "paid" || invoice.State == "pending",
		Status:  invoice.State,
		Metadata: map[string]interface{}{
			"invoice_id":     invoice.ID,
			"invoice_number": invoice.Number,
			"invoice_state":  invoice.State,
			"subtotal":       invoice.Subtotal,
			"total":          invoice.Total,
		},
	}

	if len(invoice.Transactions) > 0 {
		txn := invoice.Transactions[0]
		result.TransactionID = txn.ID
		result.ProcessorRef = txn.UUID
		result.Status = txn.Status
		result.Success = txn.Status == "success"

		if txn.StatusMessage != "" && txn.Status != "success" {
			result.ErrorMessage = txn.StatusMessage
		}
	} else {
		// No transactions yet (manual collection); use invoice ID as reference.
		result.TransactionID = invoice.ID
		result.ProcessorRef = invoice.Number
	}

	return result, nil
}

func (p *Provider) handleErrorResponse(body []byte, statusCode int, operation string) (*processor.PaymentResult, error) {
	apiErr := p.parseAPIError(body)
	errCode := "API_ERROR"

	switch statusCode {
	case 401:
		errCode = "AUTHENTICATION_FAILED"
	case 403:
		errCode = "FORBIDDEN"
	case 404:
		errCode = "NOT_FOUND"
	case 422:
		errCode = "VALIDATION_ERROR"
	case 429:
		errCode = "RATE_LIMITED"
	}

	var baseErr error
	switch {
	case statusCode == 422 && strings.Contains(strings.ToLower(apiErr), "declined"):
		baseErr = processor.ErrPaymentDeclined
	case statusCode == 422 && strings.Contains(strings.ToLower(apiErr), "insufficient"):
		baseErr = processor.ErrInsufficientFunds
	default:
		baseErr = processor.ErrPaymentFailed
	}

	return nil, processor.NewPaymentError(processor.Recurly, errCode,
		fmt.Sprintf("recurly %s failed (HTTP %d): %s", operation, statusCode, apiErr), baseErr)
}

func (p *Provider) parseAPIError(body []byte) string {
	var errResp recurlyErrorResponse
	if err := json.Unmarshal(body, &errResp); err == nil && errResp.Error.Message != "" {
		msg := errResp.Error.Message
		if errResp.Error.Type != "" {
			msg = errResp.Error.Type + ": " + msg
		}
		return msg
	}
	if len(body) > 256 {
		return string(body[:256])
	}
	return string(body)
}

func (p *Provider) fetchTransaction(ctx context.Context, txID string) (*recurlyTransaction, error) {
	path := fmt.Sprintf("/transactions/%s", txID)
	resp, err := p.doRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, processor.NewPaymentError(processor.Recurly, "API_ERROR", "failed to fetch transaction", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, processor.NewPaymentError(processor.Recurly, "READ_ERROR", "failed to read transaction response", err)
	}

	if resp.StatusCode == 404 {
		return nil, processor.NewPaymentError(processor.Recurly, "NOT_FOUND", "transaction not found", processor.ErrTransactionNotFound)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		apiErr := p.parseAPIError(respBody)
		return nil, processor.NewPaymentError(processor.Recurly, "API_ERROR",
			fmt.Sprintf("recurly get transaction failed (HTTP %d): %s", resp.StatusCode, apiErr), nil)
	}

	var txn recurlyTransaction
	if err := json.Unmarshal(respBody, &txn); err != nil {
		return nil, processor.NewPaymentError(processor.Recurly, "PARSE_ERROR", "failed to parse transaction response", err)
	}

	return &txn, nil
}

// --- Webhook XML parsing ---

// parseWebhookXML parses Recurly's XML push notifications.
// Recurly webhook types include: new_payment_notification, successful_payment_notification,
// failed_payment_notification, new_subscription_notification, expired_subscription_notification,
// renewed_subscription_notification, canceled_subscription_notification, etc.
func parseWebhookXML(payload []byte) (*processor.WebhookEvent, error) {
	// First pass: detect the root element name (= event type).
	decoder := xml.NewDecoder(bytes.NewReader(payload))
	var rootName string
	for {
		tok, err := decoder.Token()
		if err != nil {
			return nil, fmt.Errorf("failed to read XML root element: %w", err)
		}
		if se, ok := tok.(xml.StartElement); ok {
			rootName = se.Name.Local
			break
		}
	}

	if rootName == "" {
		return nil, fmt.Errorf("no root element found in webhook XML")
	}

	// Map Recurly notification names to normalized event types.
	eventType := mapWebhookType(rootName)

	// Second pass: extract structured data from the notification.
	data := extractWebhookData(payload, rootName)

	// Extract a unique ID if available.
	eventID := ""
	if txnUUID, ok := data["transaction_uuid"].(string); ok && txnUUID != "" {
		eventID = txnUUID
	} else if invoiceID, ok := data["invoice_id"].(string); ok && invoiceID != "" {
		eventID = invoiceID
	} else if subID, ok := data["subscription_id"].(string); ok && subID != "" {
		eventID = subID
	} else {
		eventID = fmt.Sprintf("recurly-%d", time.Now().UnixNano())
	}

	return &processor.WebhookEvent{
		ID:        eventID,
		Type:      eventType,
		Processor: processor.Recurly,
		Data:      data,
		Timestamp: time.Now().Unix(),
	}, nil
}

func mapWebhookType(rootName string) string {
	switch rootName {
	case "new_payment_notification":
		return "payment.created"
	case "successful_payment_notification":
		return "payment.succeeded"
	case "failed_payment_notification":
		return "payment.failed"
	case "new_invoice_notification":
		return "invoice.created"
	case "past_due_invoice_notification":
		return "invoice.past_due"
	case "closed_invoice_notification":
		return "invoice.closed"
	case "new_subscription_notification":
		return "subscription.created"
	case "renewed_subscription_notification":
		return "subscription.renewed"
	case "expired_subscription_notification":
		return "subscription.expired"
	case "canceled_subscription_notification":
		return "subscription.canceled"
	case "updated_subscription_notification":
		return "subscription.updated"
	case "reactivated_account_notification":
		return "account.reactivated"
	case "new_account_notification":
		return "account.created"
	case "canceled_account_notification":
		return "account.canceled"
	case "billing_info_updated_notification":
		return "billing_info.updated"
	case "billing_info_update_failed_notification":
		return "billing_info.update_failed"
	case "successful_refund_notification":
		return "refund.succeeded"
	case "void_payment_notification":
		return "payment.voided"
	case "new_dunning_event_notification":
		return "dunning.created"
	default:
		return rootName
	}
}

// extractWebhookData parses common nested elements from Recurly XML notifications.
// Recurly notifications contain nested <account>, <transaction>, <invoice>,
// <subscription> elements. We flatten them into a map.
func extractWebhookData(payload []byte, rootName string) map[string]interface{} {
	data := map[string]interface{}{
		"notification_type": rootName,
	}

	// Parse with a generic approach: attempt known wrapper structures.
	var notification webhookNotification
	if err := xml.Unmarshal(payload, &notification); err == nil {
		if notification.Account.Code != "" {
			data["account_code"] = notification.Account.Code
			data["account_email"] = notification.Account.Email
			data["account_first_name"] = notification.Account.FirstName
			data["account_last_name"] = notification.Account.LastName
		}
		if notification.Transaction.ID != "" {
			data["transaction_id"] = notification.Transaction.ID
			data["transaction_uuid"] = notification.Transaction.UUID
			data["transaction_amount"] = notification.Transaction.AmountInCents
			data["transaction_currency"] = notification.Transaction.Currency
			data["transaction_status"] = notification.Transaction.Status
		}
		if notification.Invoice.ID != "" {
			data["invoice_id"] = notification.Invoice.ID
			data["invoice_number"] = notification.Invoice.InvoiceNumber
			data["invoice_state"] = notification.Invoice.State
			data["invoice_total"] = notification.Invoice.TotalInCents
			data["invoice_currency"] = notification.Invoice.Currency
		}
		if notification.Subscription.ID != "" {
			data["subscription_id"] = notification.Subscription.ID
			data["subscription_plan"] = notification.Subscription.PlanCode
			data["subscription_state"] = notification.Subscription.State
			data["subscription_quantity"] = notification.Subscription.Quantity
		}
	}

	return data
}

// --- Currency conversion helpers ---

// centsToDecimal converts cents to a decimal string for Recurly API.
// For zero-decimal currencies (JPY, etc.), cents are already the base amount.
func centsToDecimal(cents currency.Cents, cur currency.Type) float64 {
	return cur.ToFloat(cents)
}

// centsToDecimalString converts cents to a string decimal for JSON fields.
func centsToDecimalString(cents currency.Cents, zeroDecimal bool) string {
	if zeroDecimal {
		return fmt.Sprintf("%d.0", cents)
	}
	whole := int64(cents) / 100
	frac := int64(cents) % 100
	if frac < 0 {
		frac = -frac
	}
	return fmt.Sprintf("%d.%02d", whole, frac)
}

// decimalToCents converts a decimal amount from Recurly to cents.
func decimalToCents(amount float64, cur currency.Type) currency.Cents {
	if cur.IsZeroDecimal() {
		return currency.Cents(int64(amount))
	}
	return currency.Cents(int64(amount * 100))
}

// --- Recurly API request/response types ---

type recurlyPurchase struct {
	Currency         string            `json:"currency"`
	Account          recurlyAccount    `json:"account"`
	LineItems        []recurlyLineItem `json:"line_items"`
	CollectionMethod string            `json:"collection_method"`
}

type recurlyAccount struct {
	Code        string              `json:"code"`
	BillingInfo *recurlyBillingInfo `json:"billing_info,omitempty"`
}

type recurlyBillingInfo struct {
	TokenID string `json:"token_id"`
}

type recurlyLineItem struct {
	Currency    string  `json:"currency"`
	UnitAmount  float64 `json:"unit_amount"`
	Quantity    int     `json:"quantity"`
	Type        string  `json:"type"`
	Description string  `json:"description"`
}

type recurlyInvoiceCollection struct {
	Object        string         `json:"object"`
	ChargeInvoice recurlyInvoice `json:"charge_invoice"`
	CreditInvoice recurlyInvoice `json:"credit_invoices"`
}

type recurlyInvoice struct {
	ID           string               `json:"id"`
	Object       string               `json:"object"`
	Number       string               `json:"number"`
	State        string               `json:"state"`
	Type         string               `json:"type"`
	Currency     string               `json:"currency"`
	Subtotal     float64              `json:"subtotal"`
	Total        float64              `json:"total"`
	Transactions []recurlyTransaction `json:"transactions"`
	CreatedAt    string               `json:"created_at"`
	UpdatedAt    string               `json:"updated_at"`
}

type recurlyTransaction struct {
	ID               string  `json:"id"`
	Object           string  `json:"object"`
	UUID             string  `json:"uuid"`
	Type             string  `json:"type"`
	Status           string  `json:"status"`
	StatusMessage    string  `json:"status_message"`
	Amount           float64 `json:"amount"`
	Currency         string  `json:"currency"`
	PaymentMethod    string  `json:"payment_method_object"`
	CollectionMethod string  `json:"collection_method"`
	Origin           string  `json:"origin"`
	AccountCode      string  `json:"-"`
	InvoiceID        string  `json:"-"`
	CreatedAt        string  `json:"created_at"`
	UpdatedAt        string  `json:"updated_at"`
}

// UnmarshalJSON handles the nested account and invoice references in transaction responses.
func (t *recurlyTransaction) UnmarshalJSON(data []byte) error {
	// Use an alias to prevent infinite recursion.
	type Alias recurlyTransaction
	aux := &struct {
		*Alias
		Account *struct {
			Code string `json:"code"`
			ID   string `json:"id"`
		} `json:"account"`
		Invoice *struct {
			ID string `json:"id"`
		} `json:"invoice"`
	}{
		Alias: (*Alias)(t),
	}

	if err := json.Unmarshal(data, aux); err != nil {
		return err
	}
	if aux.Account != nil {
		t.AccountCode = aux.Account.Code
	}
	if aux.Invoice != nil {
		t.InvoiceID = aux.Invoice.ID
	}
	return nil
}

type recurlyErrorResponse struct {
	Error struct {
		Type    string                   `json:"type"`
		Message string                   `json:"message"`
		Params  []map[string]interface{} `json:"params"`
	} `json:"error"`
}

type recurlyRefundRequest struct {
	Type                string `json:"type"`
	Amount              string `json:"amount"`
	CreditCustomerNotes string `json:"credit_customer_notes"`
}

// --- Webhook XML types ---

type webhookNotification struct {
	XMLName      xml.Name                `xml:""`
	Account      webhookAccount          `xml:"account"`
	Transaction  webhookTransaction      `xml:"transaction"`
	Invoice      webhookInvoice          `xml:"invoice"`
	Subscription webhookSubscription     `xml:"subscription"`
}

type webhookAccount struct {
	Code      string `xml:"account_code"`
	Email     string `xml:"email"`
	FirstName string `xml:"first_name"`
	LastName  string `xml:"last_name"`
}

type webhookTransaction struct {
	ID            string `xml:"id"`
	UUID          string `xml:"uuid"`
	AmountInCents string `xml:"amount_in_cents"`
	Currency      string `xml:"currency"`
	Status        string `xml:"status"`
}

type webhookInvoice struct {
	ID            string `xml:"id"`
	InvoiceNumber string `xml:"invoice_number"`
	State         string `xml:"state"`
	TotalInCents  string `xml:"total_in_cents"`
	Currency      string `xml:"currency"`
}

type webhookSubscription struct {
	ID       string `xml:"id"`
	PlanCode string `xml:"plan>plan_code"`
	State    string `xml:"state"`
	Quantity string `xml:"quantity"`
}

// Compile-time interface check.
var _ processor.PaymentProcessor = (*Provider)(nil)
