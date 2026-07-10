// Package circle implements the Circle Payments API processor for Commerce.
// Uses the Circle REST API v1 directly (no SDK dependency).
package circle

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/payment/processor"
)

const (
	productionBaseURL = "https://api.circle.com/v1"
	sandboxBaseURL    = "https://api-sandbox.circle.com/v1"
	defaultTimeout    = 30 * time.Second
)

// Config holds Circle API credentials.
type Config struct {
	APIKey      string
	Environment string // "sandbox" or "production"
}

// Provider implements PaymentProcessor and CryptoProcessor for Circle.
type Provider struct {
	*processor.BaseProcessor
	apiKey  string
	baseURL string
	client  *http.Client
}

// NewProvider creates a configured Circle provider instance.
func NewProvider(cfg Config) *Provider {
	base := productionBaseURL
	if cfg.Environment == "sandbox" {
		base = sandboxBaseURL
	}
	p := &Provider{
		BaseProcessor: processor.NewBaseProcessor(processor.Circle, supportedCurrencies()),
		apiKey:        cfg.APIKey,
		baseURL:       base,
		client:        &http.Client{Timeout: defaultTimeout},
	}
	if cfg.APIKey != "" {
		p.SetConfigured(true)
	}
	return p
}

func init() {
	apiKey := os.Getenv("CIRCLE_API_KEY")
	env := os.Getenv("CIRCLE_ENVIRONMENT")
	if env == "" {
		env = "production"
	}

	base := productionBaseURL
	if env == "sandbox" {
		base = sandboxBaseURL
	}

	p := &Provider{
		BaseProcessor: processor.NewBaseProcessor(processor.Circle, supportedCurrencies()),
		apiKey:        apiKey,
		baseURL:       base,
		client:        &http.Client{Timeout: defaultTimeout},
	}
	if apiKey != "" {
		p.SetConfigured(true)
	}
	processor.Register(p)
}

func supportedCurrencies() []currency.Type {
	return []currency.Type{"usdc", "eurc"}
}

// ---------------------------------------------------------------------------
// PaymentProcessor
// ---------------------------------------------------------------------------

// Charge creates a Circle payment with blockchain settlement.
func (p *Provider) Charge(ctx context.Context, req processor.PaymentRequest) (*processor.PaymentResult, error) {
	if err := processor.ValidateRequest(req); err != nil {
		return nil, err
	}

	idempotencyKey := uuid.New().String()

	body := map[string]interface{}{
		"idempotencyKey": idempotencyKey,
		"amount": map[string]interface{}{
			"amount":   formatAmount(req.Amount),
			"currency": mapCurrency(req.Currency),
		},
		"settlementCurrency": mapCurrency(req.Currency),
		"source": map[string]interface{}{
			"type": "blockchain",
		},
	}

	if req.Description != "" {
		body["description"] = req.Description
	}
	if req.Metadata != nil {
		body["metadata"] = req.Metadata
	}

	var resp circlePayment
	if err := p.post(ctx, "/payments", body, &resp); err != nil {
		return &processor.PaymentResult{
			Success:      false,
			ErrorMessage: err.Error(),
			Error:        err,
		}, err
	}

	return &processor.PaymentResult{
		Success:       true,
		TransactionID: resp.ID,
		ProcessorRef:  resp.ID,
		Status:        mapStatus(resp.Status),
		Metadata: map[string]interface{}{
			"circle_payment_id": resp.ID,
			"type":              resp.Type,
		},
	}, nil
}

// Authorize is not directly supported by Circle payments API.
func (p *Provider) Authorize(ctx context.Context, req processor.PaymentRequest) (*processor.PaymentResult, error) {
	return nil, processor.NewPaymentError(processor.Circle, "NOT_SUPPORTED", "authorize not supported for Circle payments", nil)
}

// Capture is not supported by Circle payments API.
func (p *Provider) Capture(ctx context.Context, transactionID string, amount currency.Cents) (*processor.PaymentResult, error) {
	return nil, processor.NewPaymentError(processor.Circle, "NOT_SUPPORTED", "capture not supported for Circle payments", nil)
}

// Refund creates a Circle return/refund for a payment.
func (p *Provider) Refund(ctx context.Context, req processor.RefundRequest) (*processor.RefundResult, error) {
	idempotencyKey := uuid.New().String()

	body := map[string]interface{}{
		"idempotencyKey": idempotencyKey,
		"paymentId":      req.TransactionID,
	}

	if req.Amount > 0 {
		body["amount"] = map[string]interface{}{
			"amount":   formatAmount(req.Amount),
			"currency": "USD",
		}
	}

	if req.Reason != "" {
		body["reason"] = req.Reason
	}

	var resp circleReturn
	if err := p.post(ctx, "/returns", body, &resp); err != nil {
		return &processor.RefundResult{
			Success:      false,
			ErrorMessage: err.Error(),
			Error:        err,
		}, err
	}

	return &processor.RefundResult{
		Success:      true,
		RefundID:     resp.ID,
		ProcessorRef: resp.ID,
	}, nil
}

// GetTransaction retrieves a Circle payment by ID.
func (p *Provider) GetTransaction(ctx context.Context, txID string) (*processor.Transaction, error) {
	var resp circlePayment
	if err := p.get(ctx, "/payments/"+txID, &resp); err != nil {
		return nil, err
	}

	return &processor.Transaction{
		ID:           resp.ID,
		ProcessorRef: resp.ID,
		Type:         "payment",
		Amount:       parseAmount(resp.Amount.Amount),
		Currency:     currency.Type(resp.Amount.Currency),
		Status:       mapStatus(resp.Status),
		CreatedAt:    parseTime(resp.CreateDate),
		UpdatedAt:    parseTime(resp.UpdateDate),
		Metadata: map[string]interface{}{
			"type":   resp.Type,
			"source": resp.Source,
		},
	}, nil
}

// ValidateWebhook verifies a Circle notification signature (SNS-based).
// Circle uses AWS SNS for webhooks. The signature verification uses the
// SigningCertURL from the SNS message to validate the signature.
func (p *Provider) ValidateWebhook(ctx context.Context, payload []byte, signature string) (*processor.WebhookEvent, error) {
	var msg snsMessage
	if err := json.Unmarshal(payload, &msg); err != nil {
		return nil, processor.ErrWebhookValidationFailed
	}

	// Verify SNS signature
	if err := p.verifySNSSignature(ctx, &msg); err != nil {
		return nil, processor.ErrWebhookValidationFailed
	}

	// Parse the Circle notification from the SNS message
	var notification circleNotification
	if err := json.Unmarshal([]byte(msg.Message), &notification); err != nil {
		return nil, fmt.Errorf("failed to parse circle notification: %w", err)
	}

	return &processor.WebhookEvent{
		ID:        msg.MessageID,
		Type:      mapNotificationType(notification.NotificationType),
		Processor: processor.Circle,
		Data:      notification.Payment,
		Timestamp: parseTime(msg.Timestamp),
	}, nil
}

// ---------------------------------------------------------------------------
// CryptoProcessor
// ---------------------------------------------------------------------------

// GenerateAddress creates a new deposit address on a specified chain.
func (p *Provider) GenerateAddress(ctx context.Context, customerID string, chain string) (string, error) {
	if !isSupportedChain(chain) {
		return "", processor.NewPaymentError(processor.Circle, "UNSUPPORTED_CHAIN", fmt.Sprintf("chain %s not supported", chain), nil)
	}

	idempotencyKey := uuid.New().String()

	body := map[string]interface{}{
		"idempotencyKey": idempotencyKey,
		"currency":       "USD",
		"chain":          mapChain(chain),
	}

	// Use the master wallet. In production, you would look up or create
	// a wallet per customer.
	var resp circleAddress
	if err := p.post(ctx, "/wallets/1/addresses", body, &resp); err != nil {
		return "", err
	}

	return resp.Address, nil
}

// GetBalance retrieves the balance for a Circle wallet.
func (p *Provider) GetBalance(ctx context.Context, address string, chain string) (*processor.Balance, error) {
	// Circle API uses wallet IDs not raw addresses for balance queries.
	// The address parameter here is treated as a wallet ID.
	var resp circleWallet
	if err := p.get(ctx, "/wallets/"+address, &resp); err != nil {
		return nil, err
	}

	var available currency.Cents
	for _, bal := range resp.Balances {
		if bal.Currency == "USD" {
			available += parseAmount(bal.Amount)
		}
	}

	return &processor.Balance{
		Available: available,
		Currency:  "usdc",
	}, nil
}

// EstimateFee returns an estimated fee for a Circle payment.
// Circle absorbs gas fees for USDC transfers in most cases.
func (p *Provider) EstimateFee(ctx context.Context, req processor.PaymentRequest) (currency.Cents, error) {
	// Circle typically absorbs network fees for USDC transfers.
	return 0, nil
}

// SupportedChains returns the blockchain networks supported by Circle.
func (p *Provider) SupportedChains() []string {
	return []string{"ethereum", "polygon", "solana", "avalanche", "base", "arbitrum"}
}

// ---------------------------------------------------------------------------
// HTTP helpers
// ---------------------------------------------------------------------------

func (p *Provider) post(ctx context.Context, path string, body interface{}, result interface{}) error {
	data, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("circle marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	return p.doRequest(req, result)
}

func (p *Provider) get(ctx context.Context, path string, result interface{}) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+path, nil)
	if err != nil {
		return err
	}
	return p.doRequest(req, result)
}

func (p *Provider) doRequest(req *http.Request, result interface{}) error {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.apiKey)

	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("circle request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("circle read body: %w", err)
	}

	if resp.StatusCode >= 400 {
		var apiErr circleAPIError
		if json.Unmarshal(body, &apiErr) == nil && apiErr.Message != "" {
			return processor.NewPaymentError(
				processor.Circle,
				fmt.Sprintf("%d", apiErr.Code),
				apiErr.Message,
				nil,
			)
		}
		return fmt.Errorf("circle API error (HTTP %d): %s", resp.StatusCode, string(body))
	}

	if result != nil {
		// Circle wraps responses in a "data" envelope
		var envelope struct {
			Data json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(body, &envelope); err != nil {
			return fmt.Errorf("circle decode envelope: %w", err)
		}
		if err := json.Unmarshal(envelope.Data, result); err != nil {
			return fmt.Errorf("circle decode response: %w", err)
		}
	}
	return nil
}

// verifySNSSignature verifies an AWS SNS message signature by fetching
// the signing certificate and validating the PKCS#7/SHA256 signature.
func (p *Provider) verifySNSSignature(ctx context.Context, msg *snsMessage) error {
	if msg.SigningCertURL == "" || msg.Signature == "" {
		return fmt.Errorf("missing signing cert URL or signature")
	}

	// Fetch the signing certificate
	certReq, err := http.NewRequestWithContext(ctx, http.MethodGet, msg.SigningCertURL, nil)
	if err != nil {
		return err
	}
	certResp, err := p.client.Do(certReq)
	if err != nil {
		return fmt.Errorf("fetch signing cert: %w", err)
	}
	defer certResp.Body.Close()

	certBody, err := io.ReadAll(certResp.Body)
	if err != nil {
		return fmt.Errorf("read signing cert: %w", err)
	}

	block, _ := pem.Decode(certBody)
	if block == nil {
		return fmt.Errorf("failed to decode PEM certificate")
	}

	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return fmt.Errorf("parse certificate: %w", err)
	}

	// Build the string to sign per SNS spec
	signStr := buildSNSSignString(msg)
	sigBytes, err := base64.StdEncoding.DecodeString(msg.Signature)
	if err != nil {
		return fmt.Errorf("decode signature: %w", err)
	}

	hash := sha256.Sum256([]byte(signStr))
	pubKey, ok := cert.PublicKey.(*ecdsa.PublicKey)
	if !ok {
		// Fall back to RSA verification via cert.CheckSignature
		if err := cert.CheckSignature(x509.SHA256WithRSA, []byte(signStr), sigBytes); err != nil {
			return fmt.Errorf("RSA signature verification failed: %w", err)
		}
		return nil
	}

	if !ecdsa.VerifyASN1(pubKey, hash[:], sigBytes) {
		return fmt.Errorf("ECDSA signature verification failed")
	}
	return nil
}

// ---------------------------------------------------------------------------
// Circle API types
// ---------------------------------------------------------------------------

type circlePayment struct {
	ID         string      `json:"id"`
	Type       string      `json:"type"`
	Status     string      `json:"status"`
	Amount     circleAmount `json:"amount"`
	Source     interface{} `json:"source"`
	CreateDate string      `json:"createDate"`
	UpdateDate string      `json:"updateDate"`
}

type circleAmount struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

type circleReturn struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

type circleAddress struct {
	Address  string `json:"address"`
	Currency string `json:"currency"`
	Chain    string `json:"chain"`
}

type circleWallet struct {
	WalletID string         `json:"walletId"`
	Balances []circleAmount `json:"balances"`
}

type circleAPIError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type circleNotification struct {
	NotificationType string                 `json:"notificationType"`
	Payment          map[string]interface{} `json:"payment"`
}

type snsMessage struct {
	Type            string `json:"Type"`
	MessageID       string `json:"MessageId"`
	Message         string `json:"Message"`
	Timestamp       string `json:"Timestamp"`
	Signature       string `json:"Signature"`
	SigningCertURL  string `json:"SigningCertURL"`
	SignatureVersion string `json:"SignatureVersion"`
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// formatAmount converts Cents to a Circle amount string (e.g. "10.50").
func formatAmount(amount currency.Cents) string {
	whole := int64(amount) / 100
	frac := int64(amount) % 100
	return fmt.Sprintf("%d.%02d", whole, frac)
}

// parseAmount converts a Circle amount string (e.g. "10.50") to Cents.
func parseAmount(s string) currency.Cents {
	var whole, frac int64
	_, _ = fmt.Sscanf(s, "%d.%d", &whole, &frac)
	return currency.Cents(whole*100 + frac)
}

// parseTime converts a Circle ISO 8601 timestamp to Unix epoch seconds.
func parseTime(s string) int64 {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return 0
	}
	return t.Unix()
}

func mapCurrency(c currency.Type) string {
	switch c {
	case "usdc":
		return "USD"
	case "eurc":
		return "EUR"
	default:
		return "USD"
	}
}

func mapChain(chain string) string {
	switch chain {
	case "ethereum":
		return "ETH"
	case "polygon":
		return "MATIC"
	case "solana":
		return "SOL"
	case "avalanche":
		return "AVAX"
	case "base":
		return "BASE"
	case "arbitrum":
		return "ARB"
	default:
		return chain
	}
}

func isSupportedChain(chain string) bool {
	switch chain {
	case "ethereum", "polygon", "solana", "avalanche", "base", "arbitrum":
		return true
	}
	return false
}

func mapStatus(status string) string {
	switch status {
	case "confirmed":
		return "completed"
	case "paid":
		return "completed"
	case "pending":
		return "pending"
	case "failed":
		return "failed"
	case "action_required":
		return "action_required"
	default:
		return status
	}
}

func mapNotificationType(nt string) string {
	switch nt {
	case "payments":
		return "payment.completed"
	case "returns":
		return "refund.completed"
	case "chargebacks":
		return "dispute.created"
	default:
		return nt
	}
}

func buildSNSSignString(msg *snsMessage) string {
	// SNS Notification signing string format
	s := "Message\n" + msg.Message + "\n"
	s += "MessageId\n" + msg.MessageID + "\n"
	s += "Timestamp\n" + msg.Timestamp + "\n"
	s += "Type\n" + msg.Type + "\n"
	return s
}

// Compile-time interface checks.
var (
	_ processor.PaymentProcessor = (*Provider)(nil)
	_ processor.CryptoProcessor  = (*Provider)(nil)
)
