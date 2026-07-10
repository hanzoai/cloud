// Package solanapay implements the Solana Pay payment processor for Commerce.
// Uses the Solana Pay transfer request spec and Solana JSON-RPC directly.
package solanapay

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/payment/processor"
)

const (
	defaultRPCURL  = "https://api.mainnet-beta.solana.com"
	defaultTimeout = 30 * time.Second

	// USDC SPL token mint on Solana mainnet
	usdcMint = "EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v"
	// USDT SPL token mint on Solana mainnet
	usdtMint = "Es9vMFrzaCERmJfrF4H2FYD4KCoNkY11McCe8BenwNYB"
)

// Config holds Solana Pay configuration.
type Config struct {
	RPCURL           string
	RecipientAddress string
}

// Provider implements PaymentProcessor and CryptoProcessor for Solana Pay.
type Provider struct {
	*processor.BaseProcessor
	rpcURL           string
	recipientAddress string
	client           *http.Client
}

// NewProvider creates a configured Solana Pay provider instance.
func NewProvider(cfg Config) *Provider {
	rpc := cfg.RPCURL
	if rpc == "" {
		rpc = defaultRPCURL
	}
	p := &Provider{
		BaseProcessor:    processor.NewBaseProcessor(processor.SolanaPay, supportedCurrencies()),
		rpcURL:           rpc,
		recipientAddress: cfg.RecipientAddress,
		client:           &http.Client{Timeout: defaultTimeout},
	}
	if cfg.RecipientAddress != "" {
		p.SetConfigured(true)
	}
	return p
}

func init() {
	rpc := os.Getenv("SOLANA_RPC_URL")
	if rpc == "" {
		rpc = defaultRPCURL
	}
	recipient := os.Getenv("SOLANA_RECIPIENT_ADDRESS")

	p := &Provider{
		BaseProcessor:    processor.NewBaseProcessor(processor.SolanaPay, supportedCurrencies()),
		rpcURL:           rpc,
		recipientAddress: recipient,
		client:           &http.Client{Timeout: defaultTimeout},
	}
	if recipient != "" {
		p.SetConfigured(true)
	}
	processor.Register(p)
}

func supportedCurrencies() []currency.Type {
	return []currency.Type{"sol", "usdc", "usdt"}
}

// ---------------------------------------------------------------------------
// PaymentProcessor
// ---------------------------------------------------------------------------

// Charge generates a Solana Pay transfer request URL with a unique reference.
// The caller presents this URL (or QR code) to the payer. The reference key
// is used later via GetTransaction to detect on-chain confirmation.
func (p *Provider) Charge(ctx context.Context, req processor.PaymentRequest) (*processor.PaymentResult, error) {
	if err := processor.ValidateRequest(req); err != nil {
		return nil, err
	}
	if p.recipientAddress == "" {
		return nil, processor.NewPaymentError(processor.SolanaPay, "NO_RECIPIENT", "recipient address not configured", nil)
	}

	reference := uuid.New().String()

	// Build Solana Pay URL: solana:<recipient>?amount=<amount>&reference=<ref>[&spl-token=<mint>]
	// Amount is in whole units (e.g., SOL or USDC with decimals)
	amountStr := formatAmount(req.Currency, req.Amount)
	payURL := fmt.Sprintf("solana:%s?amount=%s&reference=%s", p.recipientAddress, amountStr, reference)

	mint := mintForCurrency(req.Currency)
	if mint != "" {
		payURL += "&spl-token=" + mint
	}

	if req.Description != "" {
		payURL += "&label=" + req.Description
	}

	return &processor.PaymentResult{
		Success:       true,
		TransactionID: reference,
		ProcessorRef:  reference,
		Status:        "pending",
		Metadata: map[string]interface{}{
			"solana_pay_url": payURL,
			"recipient":      p.recipientAddress,
			"reference":      reference,
		},
	}, nil
}

// Authorize is not supported for Solana Pay (on-chain payments are immediate).
func (p *Provider) Authorize(ctx context.Context, req processor.PaymentRequest) (*processor.PaymentResult, error) {
	return nil, processor.NewPaymentError(processor.SolanaPay, "NOT_SUPPORTED", "authorize not supported for on-chain payments", nil)
}

// Capture is not supported for Solana Pay.
func (p *Provider) Capture(ctx context.Context, transactionID string, amount currency.Cents) (*processor.PaymentResult, error) {
	return nil, processor.NewPaymentError(processor.SolanaPay, "NOT_SUPPORTED", "capture not supported for on-chain payments", nil)
}

// Refund is not supported for Solana Pay (on-chain transactions are irreversible).
func (p *Provider) Refund(ctx context.Context, req processor.RefundRequest) (*processor.RefundResult, error) {
	return &processor.RefundResult{
		Success:      false,
		ErrorMessage: "refunds not supported for on-chain Solana payments",
	}, processor.NewPaymentError(processor.SolanaPay, "NOT_SUPPORTED", "refunds not supported for on-chain payments", nil)
}

// GetTransaction queries Solana RPC for signatures matching the reference address.
func (p *Provider) GetTransaction(ctx context.Context, txID string) (*processor.Transaction, error) {
	// txID is the reference key. Use getSignaturesForAddress to find matching transactions.
	resp, err := p.rpcCall(ctx, "getSignaturesForAddress", []interface{}{txID, map[string]interface{}{"limit": 1}})
	if err != nil {
		return nil, fmt.Errorf("solana rpc getSignaturesForAddress: %w", err)
	}

	var sigs []rpcSignature
	if err := json.Unmarshal(resp, &sigs); err != nil {
		return nil, fmt.Errorf("solana decode signatures: %w", err)
	}

	if len(sigs) == 0 {
		return &processor.Transaction{
			ID:     txID,
			Type:   "transfer",
			Status: "pending",
		}, nil
	}

	sig := sigs[0]
	status := "confirmed"
	if sig.ConfirmationStatus == "finalized" {
		status = "finalized"
	}
	if sig.Err != nil {
		status = "failed"
	}

	return &processor.Transaction{
		ID:           txID,
		ProcessorRef: sig.Signature,
		Type:         "transfer",
		Status:       status,
		CreatedAt:    sig.BlockTime,
		UpdatedAt:    sig.BlockTime,
		Metadata: map[string]interface{}{
			"signature":           sig.Signature,
			"slot":                sig.Slot,
			"confirmation_status": sig.ConfirmationStatus,
		},
	}, nil
}

// ValidateWebhook verifies a Solana transaction signature exists on-chain.
// The payload should contain the transaction signature. We verify it by fetching
// the transaction and confirming it is finalized.
func (p *Provider) ValidateWebhook(ctx context.Context, payload []byte, signature string) (*processor.WebhookEvent, error) {
	// For Solana Pay, "webhook validation" means verifying a transaction signature on-chain.
	txSig := string(payload)
	if signature != "" {
		txSig = signature
	}

	resp, err := p.rpcCall(ctx, "getTransaction", []interface{}{txSig, map[string]interface{}{"encoding": "json", "maxSupportedTransactionVersion": 0}})
	if err != nil {
		return nil, fmt.Errorf("solana rpc getTransaction: %w", err)
	}

	if string(resp) == "null" {
		return nil, processor.ErrTransactionNotFound
	}

	var tx rpcTransaction
	if err := json.Unmarshal(resp, &tx); err != nil {
		return nil, fmt.Errorf("solana decode transaction: %w", err)
	}

	if tx.Meta.Err != nil {
		return nil, processor.NewPaymentError(processor.SolanaPay, "TX_FAILED", "transaction failed on-chain", nil)
	}

	return &processor.WebhookEvent{
		ID:        txSig,
		Type:      "payment.confirmed",
		Processor: processor.SolanaPay,
		Data: map[string]interface{}{
			"signature": txSig,
			"slot":      tx.Slot,
			"block_time": tx.BlockTime,
		},
		Timestamp: tx.BlockTime,
	}, nil
}

// ---------------------------------------------------------------------------
// CryptoProcessor
// ---------------------------------------------------------------------------

// GenerateAddress returns the recipient address. Solana Pay uses a single
// recipient with unique reference keys to distinguish payments.
func (p *Provider) GenerateAddress(ctx context.Context, customerID string, chain string) (string, error) {
	if chain != "solana" {
		return "", processor.NewPaymentError(processor.SolanaPay, "UNSUPPORTED_CHAIN", fmt.Sprintf("chain %s not supported, use solana", chain), nil)
	}
	if p.recipientAddress == "" {
		return "", processor.NewPaymentError(processor.SolanaPay, "NO_RECIPIENT", "recipient address not configured", nil)
	}
	return p.recipientAddress, nil
}

// GetBalance queries the SOL or SPL token balance for an address.
func (p *Provider) GetBalance(ctx context.Context, address string, chain string) (*processor.Balance, error) {
	if chain != "solana" {
		return nil, processor.NewPaymentError(processor.SolanaPay, "UNSUPPORTED_CHAIN", fmt.Sprintf("chain %s not supported", chain), nil)
	}

	// Get native SOL balance
	resp, err := p.rpcCall(ctx, "getBalance", []interface{}{address})
	if err != nil {
		return nil, fmt.Errorf("solana rpc getBalance: %w", err)
	}

	var balResp rpcBalanceResult
	if err := json.Unmarshal(resp, &balResp); err != nil {
		return nil, fmt.Errorf("solana decode balance: %w", err)
	}

	// Convert lamports to a Cents-compatible value (1 SOL = 1e9 lamports)
	return &processor.Balance{
		Available: currency.Cents(balResp.Value),
		Currency:  "sol",
	}, nil
}

// EstimateFee returns an estimated transaction fee for Solana (~5000 lamports).
func (p *Provider) EstimateFee(ctx context.Context, req processor.PaymentRequest) (currency.Cents, error) {
	// Solana base fee is 5000 lamports (0.000005 SOL) per signature.
	// SPL token transfers require ~2 signatures.
	mint := mintForCurrency(req.Currency)
	if mint != "" {
		return 10000, nil // ~2 signatures for SPL transfer
	}
	return 5000, nil // 1 signature for native SOL transfer
}

// SupportedChains returns the chains supported by this processor.
func (p *Provider) SupportedChains() []string {
	return []string{"solana"}
}

// ---------------------------------------------------------------------------
// Solana JSON-RPC client
// ---------------------------------------------------------------------------

func (p *Provider) rpcCall(ctx context.Context, method string, params []interface{}) (json.RawMessage, error) {
	body := rpcRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  method,
		Params:  params,
	}

	data, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal rpc request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.rpcURL, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("solana rpc request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("solana rpc read body: %w", err)
	}

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("solana rpc error (HTTP %d): %s", resp.StatusCode, string(respBody))
	}

	var rpcResp rpcResponse
	if err := json.Unmarshal(respBody, &rpcResp); err != nil {
		return nil, fmt.Errorf("solana rpc decode: %w", err)
	}

	if rpcResp.Error != nil {
		return nil, fmt.Errorf("solana rpc error %d: %s", rpcResp.Error.Code, rpcResp.Error.Message)
	}

	return rpcResp.Result, nil
}

// ---------------------------------------------------------------------------
// RPC types
// ---------------------------------------------------------------------------

type rpcRequest struct {
	JSONRPC string        `json:"jsonrpc"`
	ID      int           `json:"id"`
	Method  string        `json:"method"`
	Params  []interface{} `json:"params"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int             `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcSignature struct {
	Signature          string      `json:"signature"`
	Slot               uint64      `json:"slot"`
	BlockTime          int64       `json:"blockTime"`
	ConfirmationStatus string      `json:"confirmationStatus"`
	Err                interface{} `json:"err"`
}

type rpcTransaction struct {
	Slot      uint64 `json:"slot"`
	BlockTime int64  `json:"blockTime"`
	Meta      struct {
		Err interface{} `json:"err"`
		Fee uint64      `json:"fee"`
	} `json:"meta"`
}

type rpcBalanceResult struct {
	Value uint64 `json:"value"`
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func mintForCurrency(c currency.Type) string {
	switch c {
	case "usdc":
		return usdcMint
	case "usdt":
		return usdtMint
	default:
		return "" // native SOL
	}
}

// formatAmount converts Cents to a decimal string appropriate for Solana Pay.
// SOL: 9 decimals (cents are lamports). USDC/USDT: 6 decimals.
func formatAmount(c currency.Type, amount currency.Cents) string {
	switch c {
	case "usdc", "usdt":
		// amount is in smallest unit (e.g. 1_000_000 = 1 USDC)
		whole := int64(amount) / 1_000_000
		frac := int64(amount) % 1_000_000
		if frac == 0 {
			return strconv.FormatInt(whole, 10)
		}
		return fmt.Sprintf("%d.%06d", whole, frac)
	default:
		// SOL: amount is in lamports (1 SOL = 1e9 lamports)
		whole := int64(amount) / 1_000_000_000
		frac := int64(amount) % 1_000_000_000
		if frac == 0 {
			return strconv.FormatInt(whole, 10)
		}
		return fmt.Sprintf("%d.%09d", whole, frac)
	}
}

// Compile-time interface checks.
var (
	_ processor.PaymentProcessor = (*Provider)(nil)
	_ processor.CryptoProcessor  = (*Provider)(nil)
)
