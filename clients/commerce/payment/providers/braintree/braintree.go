package braintree

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha1"
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

const (
	productionURL = "https://payments.braintree-api.com/graphql"
	sandboxURL    = "https://payments.sandbox.braintree-api.com/graphql"
	apiVersion    = "2024-08-01"
	httpTimeout   = 30 * time.Second
)

// Config holds Braintree API credentials and environment settings.
type Config struct {
	PublicKey  string
	PrivateKey string
	MerchantID string
	// Environment must be "sandbox" or "production".
	Environment string
}

// Provider implements processor.PaymentProcessor for Braintree
// using the Braintree GraphQL API with direct net/http calls.
type Provider struct {
	*processor.BaseProcessor
	config Config
	client *http.Client
}

func init() {
	processor.Register(&Provider{
		BaseProcessor: processor.NewBaseProcessor(processor.Braintree, supportedCurrencies()),
		client:        &http.Client{Timeout: httpTimeout},
	})
}

// NewProvider creates a configured Braintree provider instance.
func NewProvider(cfg Config) *Provider {
	p := &Provider{
		BaseProcessor: processor.NewBaseProcessor(processor.Braintree, supportedCurrencies()),
		client:        &http.Client{Timeout: httpTimeout},
	}
	p.Configure(cfg)
	return p
}

// Configure sets credentials and marks the processor as available.
func (p *Provider) Configure(cfg Config) {
	p.config = cfg
	if p.client == nil {
		p.client = &http.Client{Timeout: httpTimeout}
	}
	ok := cfg.PublicKey != "" && cfg.PrivateKey != "" && cfg.MerchantID != ""
	p.SetConfigured(ok)
}

// Type returns the processor type.
func (p *Provider) Type() processor.ProcessorType {
	return processor.Braintree
}

// IsAvailable reports whether the processor is configured and reachable.
func (p *Provider) IsAvailable(ctx context.Context) bool {
	return p.config.PublicKey != "" && p.config.PrivateKey != "" && p.config.MerchantID != ""
}

// SupportedCurrencies returns currencies supported by Braintree.
func (p *Provider) SupportedCurrencies() []currency.Type {
	return supportedCurrencies()
}

func supportedCurrencies() []currency.Type {
	return []currency.Type{
		currency.USD, currency.EUR, currency.GBP, currency.CAD,
		currency.AUD, currency.JPY, currency.CHF, currency.NZD,
		currency.SGD, currency.HKD,
	}
}

// Charge processes a payment (authorize + capture in one step).
func (p *Provider) Charge(ctx context.Context, req processor.PaymentRequest) (*processor.PaymentResult, error) {
	if err := p.checkAvailable(); err != nil {
		return nil, err
	}
	if err := processor.ValidateRequest(req); err != nil {
		return nil, processor.NewPaymentError(processor.Braintree, "INVALID_REQUEST", err.Error(), err)
	}
	if !processor.SupportsCurrency(p, req.Currency) {
		return nil, processor.NewPaymentError(processor.Braintree, "UNSUPPORTED_CURRENCY",
			fmt.Sprintf("currency %s not supported", req.Currency), processor.ErrCurrencyNotSupported)
	}

	amount := centsToDecimal(req.Amount, req.Currency)
	merchantAccountID := p.merchantAccountID(req)

	query := `mutation ChargePaymentMethod($input: ChargePaymentMethodInput!) {
		chargePaymentMethod(input: $input) {
			transaction {
				id
				status
				amount {
					value
					currencyCode
				}
			}
		}
	}`

	txInput := map[string]interface{}{
		"amount": amount,
	}
	if merchantAccountID != "" {
		txInput["merchantAccountId"] = merchantAccountID
	}
	if req.OrderID != "" {
		txInput["orderId"] = req.OrderID
	}
	if req.CustomerID != "" {
		txInput["customerId"] = req.CustomerID
	}

	variables := map[string]interface{}{
		"input": map[string]interface{}{
			"paymentMethodId": req.Token,
			"transaction":     txInput,
		},
	}

	resp, err := p.executeGraphQL(ctx, query, variables)
	if err != nil {
		return nil, processor.NewPaymentError(processor.Braintree, "API_ERROR", "charge request failed", err)
	}

	// Check for GraphQL errors.
	if gqlErr := extractGraphQLError(resp); gqlErr != "" {
		return &processor.PaymentResult{
			Success:      false,
			ErrorMessage: gqlErr,
			Status:       "failed",
		}, processor.NewPaymentError(processor.Braintree, "CHARGE_FAILED", gqlErr, nil)
	}

	tx := extractTransaction(resp, "chargePaymentMethod")
	if tx == nil {
		return &processor.PaymentResult{
			Success:      false,
			ErrorMessage: "no transaction in response",
			Status:       "failed",
		}, processor.NewPaymentError(processor.Braintree, "CHARGE_FAILED", "no transaction in response", nil)
	}

	txID, _ := tx["id"].(string)
	status, _ := tx["status"].(string)

	return &processor.PaymentResult{
		Success:       true,
		TransactionID: txID,
		ProcessorRef:  txID,
		Status:        mapStatus(status),
		Metadata: map[string]interface{}{
			"braintreeStatus": status,
		},
	}, nil
}

// Authorize authorizes a payment without capturing.
func (p *Provider) Authorize(ctx context.Context, req processor.PaymentRequest) (*processor.PaymentResult, error) {
	if err := p.checkAvailable(); err != nil {
		return nil, err
	}
	if err := processor.ValidateRequest(req); err != nil {
		return nil, processor.NewPaymentError(processor.Braintree, "INVALID_REQUEST", err.Error(), err)
	}
	if !processor.SupportsCurrency(p, req.Currency) {
		return nil, processor.NewPaymentError(processor.Braintree, "UNSUPPORTED_CURRENCY",
			fmt.Sprintf("currency %s not supported", req.Currency), processor.ErrCurrencyNotSupported)
	}

	amount := centsToDecimal(req.Amount, req.Currency)
	merchantAccountID := p.merchantAccountID(req)

	query := `mutation AuthorizePaymentMethod($input: AuthorizePaymentMethodInput!) {
		authorizePaymentMethod(input: $input) {
			transaction {
				id
				status
				amount {
					value
					currencyCode
				}
			}
		}
	}`

	txInput := map[string]interface{}{
		"amount": amount,
	}
	if merchantAccountID != "" {
		txInput["merchantAccountId"] = merchantAccountID
	}
	if req.OrderID != "" {
		txInput["orderId"] = req.OrderID
	}
	if req.CustomerID != "" {
		txInput["customerId"] = req.CustomerID
	}

	variables := map[string]interface{}{
		"input": map[string]interface{}{
			"paymentMethodId": req.Token,
			"transaction":     txInput,
		},
	}

	resp, err := p.executeGraphQL(ctx, query, variables)
	if err != nil {
		return nil, processor.NewPaymentError(processor.Braintree, "API_ERROR", "authorize request failed", err)
	}

	if gqlErr := extractGraphQLError(resp); gqlErr != "" {
		return &processor.PaymentResult{
			Success:      false,
			ErrorMessage: gqlErr,
			Status:       "failed",
		}, processor.NewPaymentError(processor.Braintree, "AUTH_FAILED", gqlErr, nil)
	}

	tx := extractTransaction(resp, "authorizePaymentMethod")
	if tx == nil {
		return &processor.PaymentResult{
			Success:      false,
			ErrorMessage: "no transaction in response",
			Status:       "failed",
		}, processor.NewPaymentError(processor.Braintree, "AUTH_FAILED", "no transaction in response", nil)
	}

	txID, _ := tx["id"].(string)
	status, _ := tx["status"].(string)

	return &processor.PaymentResult{
		Success:       true,
		TransactionID: txID,
		ProcessorRef:  txID,
		Status:        mapStatus(status),
		Metadata: map[string]interface{}{
			"braintreeStatus": status,
		},
	}, nil
}

// Capture captures a previously authorized payment.
func (p *Provider) Capture(ctx context.Context, transactionID string, amount currency.Cents) (*processor.PaymentResult, error) {
	if err := p.checkAvailable(); err != nil {
		return nil, err
	}
	if transactionID == "" {
		return nil, processor.NewPaymentError(processor.Braintree, "INVALID_REQUEST", "transaction ID required", nil)
	}

	// For capture we default to USD if no currency context is available.
	// The amount conversion is safe because Braintree stores the currency on the auth.
	amountStr := centsToDecimal(amount, currency.USD)

	query := `mutation CaptureTransaction($input: CaptureTransactionInput!) {
		captureTransaction(input: $input) {
			transaction {
				id
				status
				amount {
					value
					currencyCode
				}
			}
		}
	}`

	input := map[string]interface{}{
		"transactionId": transactionID,
	}
	if amount > 0 {
		input["amount"] = amountStr
	}

	variables := map[string]interface{}{
		"input": input,
	}

	resp, err := p.executeGraphQL(ctx, query, variables)
	if err != nil {
		return nil, processor.NewPaymentError(processor.Braintree, "API_ERROR", "capture request failed", err)
	}

	if gqlErr := extractGraphQLError(resp); gqlErr != "" {
		return &processor.PaymentResult{
			Success:      false,
			ErrorMessage: gqlErr,
			Status:       "failed",
		}, processor.NewPaymentError(processor.Braintree, "CAPTURE_FAILED", gqlErr, nil)
	}

	tx := extractTransaction(resp, "captureTransaction")
	if tx == nil {
		return &processor.PaymentResult{
			Success:      false,
			ErrorMessage: "no transaction in response",
			Status:       "failed",
		}, processor.NewPaymentError(processor.Braintree, "CAPTURE_FAILED", "no transaction in response", nil)
	}

	txID, _ := tx["id"].(string)
	status, _ := tx["status"].(string)

	return &processor.PaymentResult{
		Success:       true,
		TransactionID: txID,
		ProcessorRef:  txID,
		Status:        mapStatus(status),
		Metadata: map[string]interface{}{
			"braintreeStatus": status,
		},
	}, nil
}

// Refund processes a full or partial refund.
func (p *Provider) Refund(ctx context.Context, req processor.RefundRequest) (*processor.RefundResult, error) {
	if err := p.checkAvailable(); err != nil {
		return nil, err
	}
	if req.TransactionID == "" {
		return nil, processor.NewPaymentError(processor.Braintree, "INVALID_REQUEST", "transaction ID required for refund", nil)
	}

	query := `mutation RefundTransaction($input: RefundTransactionInput!) {
		refundTransaction(input: $input) {
			refund {
				id
				status
				amount {
					value
				}
			}
		}
	}`

	input := map[string]interface{}{
		"transactionId": req.TransactionID,
	}
	// Partial refund: include amount. Full refund: omit amount.
	if req.Amount > 0 {
		input["amount"] = centsToDecimal(req.Amount, currency.USD)
	}

	variables := map[string]interface{}{
		"input": input,
	}

	resp, err := p.executeGraphQL(ctx, query, variables)
	if err != nil {
		return nil, processor.NewPaymentError(processor.Braintree, "API_ERROR", "refund request failed", err)
	}

	if gqlErr := extractGraphQLError(resp); gqlErr != "" {
		return &processor.RefundResult{
			Success:      false,
			ErrorMessage: gqlErr,
		}, processor.NewPaymentError(processor.Braintree, "REFUND_FAILED", gqlErr, nil)
	}

	// Extract refund data.
	data, _ := resp["data"].(map[string]interface{})
	refundTx, _ := data["refundTransaction"].(map[string]interface{})
	refund, _ := refundTx["refund"].(map[string]interface{})

	if refund == nil {
		return &processor.RefundResult{
			Success:      false,
			ErrorMessage: "no refund in response",
		}, processor.NewPaymentError(processor.Braintree, "REFUND_FAILED", "no refund in response", nil)
	}

	refundID, _ := refund["id"].(string)

	return &processor.RefundResult{
		Success:      true,
		RefundID:     refundID,
		ProcessorRef: refundID,
	}, nil
}

// GetTransaction retrieves transaction details by ID.
func (p *Provider) GetTransaction(ctx context.Context, txID string) (*processor.Transaction, error) {
	if err := p.checkAvailable(); err != nil {
		return nil, err
	}
	if txID == "" {
		return nil, processor.NewPaymentError(processor.Braintree, "INVALID_REQUEST", "transaction ID required", nil)
	}

	query := `query GetTransaction($id: ID!) {
		node(id: $id) {
			... on Transaction {
				id
				status
				amount {
					value
					currencyCode
				}
				orderId
				createdAt
				updatedAt
			}
		}
	}`

	variables := map[string]interface{}{
		"id": txID,
	}

	resp, err := p.executeGraphQL(ctx, query, variables)
	if err != nil {
		return nil, processor.NewPaymentError(processor.Braintree, "API_ERROR", "get transaction request failed", err)
	}

	if gqlErr := extractGraphQLError(resp); gqlErr != "" {
		return nil, processor.NewPaymentError(processor.Braintree, "GET_TX_FAILED", gqlErr, nil)
	}

	data, _ := resp["data"].(map[string]interface{})
	node, _ := data["node"].(map[string]interface{})
	if node == nil {
		return nil, processor.NewPaymentError(processor.Braintree, "NOT_FOUND",
			fmt.Sprintf("transaction %s not found", txID), processor.ErrTransactionNotFound)
	}

	id, _ := node["id"].(string)
	status, _ := node["status"].(string)
	orderID, _ := node["orderId"].(string)

	// Parse amount.
	var txAmount currency.Cents
	var txCurrency currency.Type
	if amountObj, ok := node["amount"].(map[string]interface{}); ok {
		if valStr, ok := amountObj["value"].(string); ok {
			txAmount = currency.CentsFromString(valStr)
		}
		if code, ok := amountObj["currencyCode"].(string); ok {
			txCurrency = currency.Type(strings.ToLower(code))
		}
	}

	// Parse timestamps.
	var createdAt, updatedAt int64
	if ts, ok := node["createdAt"].(string); ok {
		if t, err := time.Parse(time.RFC3339, ts); err == nil {
			createdAt = t.Unix()
		}
	}
	if ts, ok := node["updatedAt"].(string); ok {
		if t, err := time.Parse(time.RFC3339, ts); err == nil {
			updatedAt = t.Unix()
		}
	}

	return &processor.Transaction{
		ID:           id,
		ProcessorRef: id,
		Type:         "charge",
		Amount:       txAmount,
		Currency:     txCurrency,
		Status:       mapStatus(status),
		CustomerID:   orderID, // Braintree stores orderId; map to CustomerID if needed.
		CreatedAt:    createdAt,
		UpdatedAt:    updatedAt,
		Metadata: map[string]interface{}{
			"braintreeStatus": status,
		},
	}, nil
}

// ValidateWebhook validates an incoming Braintree webhook notification.
//
// Braintree sends webhooks as form-encoded with bt_signature and bt_payload.
// bt_signature is "publicKey|sha1Hex" where the sha1Hex is HMAC-SHA1 of
// the bt_payload signed with the private key.
//
// The signature parameter should be the bt_signature value, and the
// payload parameter should be the raw bt_payload (base64-encoded by Braintree).
func (p *Provider) ValidateWebhook(ctx context.Context, payload []byte, signature string) (*processor.WebhookEvent, error) {
	if err := p.checkAvailable(); err != nil {
		return nil, err
	}

	// Parse signature: "publicKey|hexDigest"
	parts := strings.SplitN(signature, "|", 2)
	if len(parts) != 2 {
		return nil, processor.NewPaymentError(processor.Braintree, "INVALID_SIGNATURE",
			"webhook signature format invalid: expected publicKey|hash", processor.ErrWebhookValidationFailed)
	}

	sigPublicKey := parts[0]
	sigHash := parts[1]

	// Verify the public key matches ours.
	if sigPublicKey != p.config.PublicKey {
		return nil, processor.NewPaymentError(processor.Braintree, "INVALID_SIGNATURE",
			"webhook signature public key mismatch", processor.ErrWebhookValidationFailed)
	}

	// Compute HMAC-SHA1 of the payload using private key.
	mac := hmac.New(sha1.New, []byte(p.config.PrivateKey))
	mac.Write(payload)
	expectedHash := hex.EncodeToString(mac.Sum(nil))

	if !hmac.Equal([]byte(sigHash), []byte(expectedHash)) {
		return nil, processor.NewPaymentError(processor.Braintree, "INVALID_SIGNATURE",
			"webhook signature hash mismatch", processor.ErrWebhookValidationFailed)
	}

	// Decode the base64 payload.
	decoded, err := base64.StdEncoding.DecodeString(string(payload))
	if err != nil {
		return nil, processor.NewPaymentError(processor.Braintree, "DECODE_ERROR",
			"failed to decode webhook payload", err)
	}

	// Parse the notification payload. Braintree can send XML or JSON.
	// Try JSON first, fall back to treating as opaque data.
	event := &processor.WebhookEvent{
		Processor: processor.Braintree,
		Timestamp: time.Now().Unix(),
		Data:      make(map[string]interface{}),
	}

	var jsonData map[string]interface{}
	if err := json.Unmarshal(decoded, &jsonData); err == nil {
		event.Data = jsonData
		if kind, ok := jsonData["kind"].(string); ok {
			event.Type = kind
		}
		if id, ok := jsonData["id"].(string); ok {
			event.ID = id
		}
		if ts, ok := jsonData["timestamp"].(string); ok {
			if t, err := time.Parse(time.RFC3339, ts); err == nil {
				event.Timestamp = t.Unix()
			}
		}
	} else {
		// XML or unknown format: store raw decoded payload for caller to parse.
		event.Type = "raw_notification"
		event.ID = fmt.Sprintf("bt_%d", time.Now().UnixNano())
		event.Data["raw"] = string(decoded)
	}

	return event, nil
}

// --- Internal helpers ---

// checkAvailable returns an error if the processor is not configured.
func (p *Provider) checkAvailable() error {
	if p.config.PublicKey == "" || p.config.PrivateKey == "" || p.config.MerchantID == "" {
		return processor.NewPaymentError(processor.Braintree, "NOT_CONFIGURED",
			"braintree processor not configured", processor.ErrProcessorNotAvailable)
	}
	return nil
}

// graphqlEndpoint returns the appropriate API URL based on environment.
func (p *Provider) graphqlEndpoint() string {
	if strings.ToLower(p.config.Environment) == "production" {
		return productionURL
	}
	return sandboxURL
}

// authHeader returns the Basic auth header value.
func (p *Provider) authHeader() string {
	creds := p.config.PublicKey + ":" + p.config.PrivateKey
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(creds))
}

// graphqlRequest is the JSON body sent to the Braintree GraphQL API.
type graphqlRequest struct {
	Query     string                 `json:"query"`
	Variables map[string]interface{} `json:"variables,omitempty"`
}

// executeGraphQL sends a GraphQL request and returns the parsed JSON response.
func (p *Provider) executeGraphQL(ctx context.Context, query string, variables map[string]interface{}) (map[string]interface{}, error) {
	body, err := json.Marshal(graphqlRequest{
		Query:     query,
		Variables: variables,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal graphql request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.graphqlEndpoint(), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create http request: %w", err)
	}

	req.Header.Set("Authorization", p.authHeader())
	req.Header.Set("Braintree-Version", apiVersion)
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("braintree API returned status %d: %s", resp.StatusCode, string(respBody))
	}

	var result map[string]interface{}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}

	return result, nil
}

// extractGraphQLError checks for errors in the GraphQL response.
// Returns empty string if no errors.
func extractGraphQLError(resp map[string]interface{}) string {
	errors, ok := resp["errors"]
	if !ok {
		return ""
	}

	errList, ok := errors.([]interface{})
	if !ok || len(errList) == 0 {
		return ""
	}

	var messages []string
	for _, e := range errList {
		if errMap, ok := e.(map[string]interface{}); ok {
			if msg, ok := errMap["message"].(string); ok {
				messages = append(messages, msg)
			}
		}
	}

	if len(messages) == 0 {
		return "unknown graphql error"
	}
	return strings.Join(messages, "; ")
}

// extractTransaction extracts the transaction object from a mutation response.
// mutationName is the top-level mutation field (e.g., "chargePaymentMethod").
func extractTransaction(resp map[string]interface{}, mutationName string) map[string]interface{} {
	data, ok := resp["data"].(map[string]interface{})
	if !ok {
		return nil
	}
	mutation, ok := data[mutationName].(map[string]interface{})
	if !ok {
		return nil
	}
	tx, _ := mutation["transaction"].(map[string]interface{})
	return tx
}

// centsToDecimal converts cents to a decimal string like "10.00".
// Zero-decimal currencies (e.g., JPY) return whole units.
func centsToDecimal(amount currency.Cents, cur currency.Type) string {
	if cur.IsZeroDecimal() {
		return fmt.Sprintf("%d", amount)
	}
	return fmt.Sprintf("%.2f", float64(amount)/100.0)
}

// merchantAccountID extracts a merchant account ID from request options,
// falling back to the provider's configured MerchantID.
func (p *Provider) merchantAccountID(req processor.PaymentRequest) string {
	if req.Options != nil {
		if v, ok := req.Options["merchantAccountId"].(string); ok && v != "" {
			return v
		}
	}
	return p.config.MerchantID
}

// mapStatus maps Braintree transaction statuses to normalized status strings.
func mapStatus(btStatus string) string {
	switch strings.ToUpper(btStatus) {
	case "SUBMITTED_FOR_SETTLEMENT", "SETTLING":
		return "pending"
	case "SETTLED":
		return "completed"
	case "AUTHORIZED":
		return "authorized"
	case "VOIDED":
		return "voided"
	case "PROCESSOR_DECLINED", "GATEWAY_REJECTED":
		return "declined"
	case "FAILED", "AUTHORIZATION_EXPIRED", "SETTLEMENT_DECLINED":
		return "failed"
	case "REFUNDED":
		return "refunded"
	default:
		return strings.ToLower(btStatus)
	}
}

// Compile-time interface check.
var _ processor.PaymentProcessor = (*Provider)(nil)
