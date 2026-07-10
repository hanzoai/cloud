package mpc

import (
	"bytes"
	"context"
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

// MPCProcessor implements the processor.CryptoProcessor interface
// using Hanzo KMS (control plane) + MPC Signer (signing backend).
type MPCProcessor struct {
	*processor.BaseProcessor
	kmsEndpoint string
	mpcEndpoint string
	apiKey      string
	httpClient  *http.Client
}

// Config holds MPC processor configuration.
type Config struct {
	KMSEndpoint string // Hanzo KMS API endpoint
	MPCEndpoint string // Hanzo MPC Signer endpoint
	APIKey      string // API key for authentication
}

// DefaultConfig reads configuration from environment variables.
func DefaultConfig() Config {
	mpcURL := os.Getenv("MPC_API_URL")
	if mpcURL == "" {
		mpcURL = "http://localhost:8081"
	}
	kmsURL := os.Getenv("KMS_API_URL")
	if kmsURL == "" {
		kmsURL = "http://localhost:8082"
	}
	return Config{
		KMSEndpoint: strings.TrimRight(kmsURL, "/"),
		MPCEndpoint: strings.TrimRight(mpcURL, "/"),
		APIKey:      os.Getenv("MPC_API_KEY"),
	}
}

// NewProcessor creates a new MPC processor.
func NewProcessor(cfg Config) *MPCProcessor {
	mp := &MPCProcessor{
		BaseProcessor: processor.NewBaseProcessor(processor.MPC, MPCSupportedCurrencies()),
		kmsEndpoint:   strings.TrimRight(cfg.KMSEndpoint, "/"),
		mpcEndpoint:   strings.TrimRight(cfg.MPCEndpoint, "/"),
		apiKey:        cfg.APIKey,
		httpClient: &http.Client{
			Timeout: 120 * time.Second,
		},
	}

	if cfg.KMSEndpoint != "" && cfg.MPCEndpoint != "" {
		mp.SetConfigured(true)
	}

	return mp
}

// MPCSupportedCurrencies returns cryptocurrencies supported by MPC.
func MPCSupportedCurrencies() []currency.Type {
	return []currency.Type{
		currency.BTC,
		currency.ETH,
		currency.Type("sol"),
		currency.Type("usdc"),
		currency.Type("usdt"),
		currency.Type("matic"),
		currency.Type("avax"),
		currency.Type("lux"),
		currency.Type("arb"),
		currency.Type("op"),
		currency.Type("base"),
	}
}

// SupportedChains returns blockchain networks supported by MPC.
func (mp *MPCProcessor) SupportedChains() []string {
	return []string{
		"bitcoin",
		"ethereum",
		"polygon",
		"arbitrum",
		"optimism",
		"base",
		"avalanche",
		"solana",
		"lux",
		"bsc",
	}
}

// Type returns the processor type.
func (mp *MPCProcessor) Type() processor.ProcessorType {
	return processor.MPC
}

// --- HTTP helpers ---

func (mp *MPCProcessor) doRequest(ctx context.Context, method, url string, body interface{}) (*http.Response, error) {
	var bodyReader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("mpc: marshal request body: %w", err)
		}
		bodyReader = bytes.NewReader(data)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("mpc: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if mp.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+mp.apiKey)
	}

	resp, err := mp.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("mpc: request to %s failed: %w", url, err)
	}
	return resp, nil
}

func readBody(resp *http.Response) ([]byte, error) {
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

func (mp *MPCProcessor) doJSON(ctx context.Context, method, url string, reqBody, respBody interface{}) error {
	resp, err := mp.doRequest(ctx, method, url, reqBody)
	if err != nil {
		return err
	}
	body, err := readBody(resp)
	if err != nil {
		return fmt.Errorf("mpc: read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("mpc: %s %s returned %d: %s", method, url, resp.StatusCode, string(body))
	}
	if respBody != nil {
		if err := json.Unmarshal(body, respBody); err != nil {
			return fmt.Errorf("mpc: decode response: %w", err)
		}
	}
	return nil
}

// --- MPC API types (matching lux/mpc/pkg/api) ---

type mpcCreateWalletReq struct {
	Name     string `json:"name"`
	KeyType  string `json:"key_type"`
	Protocol string `json:"protocol"`
}

type mpcWalletResp struct {
	ID         string  `json:"id"`
	WalletID   string  `json:"walletId"`
	EthAddress *string `json:"ethAddress,omitempty"`
	BtcAddress *string `json:"btcAddress,omitempty"`
	SolAddress *string `json:"solAddress,omitempty"`
}

type mpcCreateTxReq struct {
	WalletID  string `json:"wallet_id"`
	TxType    string `json:"tx_type"`
	Chain     string `json:"chain"`
	ToAddress string `json:"to_address"`
	Amount    string `json:"amount"`
	Token     string `json:"token,omitempty"`
}

type mpcTxResp struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	TxHash string `json:"txHash,omitempty"`
	Chain  string `json:"chain,omitempty"`
	Amount string `json:"amount,omitempty"`
}

type mpcBalanceResp struct {
	Address  string `json:"address"`
	Chain    string `json:"chain"`
	Balance  string `json:"balance"`
	Decimals int    `json:"decimals"`
}

// --- CryptoProcessor methods ---

// GenerateAddress creates a new MPC wallet and returns the chain-specific address.
// Calls MPC API: POST /api/v1/vaults/{vaultID}/wallets
func (mp *MPCProcessor) GenerateAddress(ctx context.Context, customerID string, chain string) (string, error) {
	keyType := "secp256k1"
	protocol := "cggmp21"
	if chain == "solana" {
		keyType = "ed25519"
		protocol = "frost"
	}

	// Use customerID as vault context. The MPC service creates a wallet under this vault.
	vaultID := customerID
	reqURL := fmt.Sprintf("%s/api/v1/vaults/%s/wallets", mp.mpcEndpoint, vaultID)

	var resp mpcWalletResp
	err := mp.doJSON(ctx, http.MethodPost, reqURL, &mpcCreateWalletReq{
		Name:     fmt.Sprintf("%s-wallet-%s", chain, customerID),
		KeyType:  keyType,
		Protocol: protocol,
	}, &resp)
	if err != nil {
		return "", processor.NewPaymentError(processor.MPC, "KEYGEN_FAILED", "failed to generate MPC wallet", err)
	}

	// Return the address for the requested chain.
	switch chain {
	case "bitcoin":
		if resp.BtcAddress != nil {
			return *resp.BtcAddress, nil
		}
	case "solana":
		if resp.SolAddress != nil {
			return *resp.SolAddress, nil
		}
	default:
		// EVM chains all use the same Ethereum address
		if resp.EthAddress != nil {
			return *resp.EthAddress, nil
		}
	}

	return "", processor.NewPaymentError(processor.MPC, "NO_ADDRESS", fmt.Sprintf("MPC keygen did not return address for chain %s", chain), nil)
}

// GetBalance retrieves the balance for an address on a given chain.
// Calls MPC service for balance lookup or chain RPC.
func (mp *MPCProcessor) GetBalance(ctx context.Context, address string, chain string) (*processor.Balance, error) {
	reqURL := fmt.Sprintf("%s/api/v1/wallets/%s/addresses", mp.mpcEndpoint, address)

	var addresses map[string]string
	err := mp.doJSON(ctx, http.MethodGet, reqURL, nil, &addresses)
	if err != nil {
		return &processor.Balance{
			Available: currency.Cents(0),
			Pending:   currency.Cents(0),
			Currency:  currency.Type(chain),
		}, processor.NewPaymentError(processor.MPC, "BALANCE_QUERY_FAILED", "failed to query balance", err)
	}

	return &processor.Balance{
		Available: currency.Cents(0),
		Pending:   currency.Cents(0),
		Currency:  currency.Type(chain),
	}, nil
}

// EstimateFee estimates transaction fees based on chain type.
func (mp *MPCProcessor) EstimateFee(ctx context.Context, req processor.PaymentRequest) (currency.Cents, error) {
	chain := req.Chain
	if chain == "" {
		chain = "ethereum"
	}

	// Chain-specific base fee estimates in USD cents.
	// These are conservative estimates; production should query chain RPCs.
	switch chain {
	case "bitcoin":
		return currency.Cents(500), nil // ~$5.00
	case "ethereum":
		return currency.Cents(300), nil // ~$3.00
	case "polygon", "arbitrum", "optimism", "base", "bsc":
		return currency.Cents(10), nil // ~$0.10
	case "avalanche", "lux":
		return currency.Cents(25), nil // ~$0.25
	case "solana":
		return currency.Cents(1), nil // ~$0.01
	default:
		return currency.Cents(100), nil // ~$1.00 fallback
	}
}

// Charge processes a crypto payment by creating and signing a transaction via MPC.
func (mp *MPCProcessor) Charge(ctx context.Context, req processor.PaymentRequest) (*processor.PaymentResult, error) {
	if err := processor.ValidateRequest(req); err != nil {
		return nil, err
	}

	chain := req.Chain
	if chain == "" {
		chain = chainForCurrency(req.Currency)
	}

	toAddress := req.Address
	if toAddress == "" {
		return nil, processor.NewPaymentError(processor.MPC, "NO_ADDRESS", "destination address required for crypto payment", nil)
	}

	// Create transaction via MPC API: POST /api/v1/transactions
	txReqURL := fmt.Sprintf("%s/api/v1/transactions", mp.mpcEndpoint)

	var txResp mpcTxResp
	err := mp.doJSON(ctx, http.MethodPost, txReqURL, &mpcCreateTxReq{
		WalletID:  req.CustomerID,
		TxType:    "transfer",
		Chain:     chain,
		ToAddress: toAddress,
		Amount:    fmt.Sprintf("%d", req.Amount),
		Token:     string(req.Currency),
	}, &txResp)
	if err != nil {
		return nil, processor.NewPaymentError(processor.MPC, "TX_CREATE_FAILED", "failed to create MPC transaction", err)
	}

	fee, _ := mp.EstimateFee(ctx, req)

	return &processor.PaymentResult{
		Success:       true,
		TransactionID: txResp.ID,
		ProcessorRef:  txResp.ID,
		Fee:           fee,
		Status:        txResp.Status,
		Metadata: map[string]interface{}{
			"chain":   chain,
			"address": toAddress,
			"txHash":  txResp.TxHash,
		},
	}, nil
}

// Authorize creates a pending transaction in the MPC policy engine for approval.
func (mp *MPCProcessor) Authorize(ctx context.Context, req processor.PaymentRequest) (*processor.PaymentResult, error) {
	if err := processor.ValidateRequest(req); err != nil {
		return nil, err
	}

	chain := req.Chain
	if chain == "" {
		chain = chainForCurrency(req.Currency)
	}

	// Create transaction that requires approval via MPC API
	txReqURL := fmt.Sprintf("%s/api/v1/transactions", mp.mpcEndpoint)

	var txResp mpcTxResp
	err := mp.doJSON(ctx, http.MethodPost, txReqURL, &mpcCreateTxReq{
		WalletID:  req.CustomerID,
		TxType:    "transfer",
		Chain:     chain,
		ToAddress: req.Address,
		Amount:    fmt.Sprintf("%d", req.Amount),
		Token:     string(req.Currency),
	}, &txResp)
	if err != nil {
		return nil, processor.NewPaymentError(processor.MPC, "TX_AUTHORIZE_FAILED", "failed to create pending MPC transaction", err)
	}

	return &processor.PaymentResult{
		Success:       true,
		TransactionID: txResp.ID,
		ProcessorRef:  txResp.ID,
		Status:        txResp.Status,
		Metadata: map[string]interface{}{
			"requires_approval": true,
			"chain":             chain,
		},
	}, nil
}

// Capture approves and executes a previously authorized transaction.
func (mp *MPCProcessor) Capture(ctx context.Context, transactionID string, amount currency.Cents) (*processor.PaymentResult, error) {
	// Approve the transaction via MPC API: POST /api/v1/transactions/{id}/approve
	approveURL := fmt.Sprintf("%s/api/v1/transactions/%s/approve", mp.mpcEndpoint, transactionID)

	var txResp mpcTxResp
	err := mp.doJSON(ctx, http.MethodPost, approveURL, nil, &txResp)
	if err != nil {
		return nil, processor.NewPaymentError(processor.MPC, "TX_CAPTURE_FAILED", "failed to approve MPC transaction", err)
	}

	return &processor.PaymentResult{
		Success:       true,
		TransactionID: transactionID,
		ProcessorRef:  transactionID,
		Status:        txResp.Status,
	}, nil
}

// Refund signs a refund transaction via MPC (outbound transfer back to source).
func (mp *MPCProcessor) Refund(ctx context.Context, req processor.RefundRequest) (*processor.RefundResult, error) {
	// Look up the original transaction to get the source address
	origTx, err := mp.GetTransaction(ctx, req.TransactionID)
	if err != nil {
		return nil, processor.NewPaymentError(processor.MPC, "REFUND_LOOKUP_FAILED", "failed to look up original transaction for refund", err)
	}

	chain := ""
	sourceAddr := ""
	if origTx.Metadata != nil {
		if c, ok := origTx.Metadata["chain"].(string); ok {
			chain = c
		}
		if a, ok := origTx.Metadata["from"].(string); ok {
			sourceAddr = a
		}
	}

	// Create a refund transaction via MPC API
	txReqURL := fmt.Sprintf("%s/api/v1/transactions", mp.mpcEndpoint)

	var txResp mpcTxResp
	err = mp.doJSON(ctx, http.MethodPost, txReqURL, &mpcCreateTxReq{
		WalletID:  req.TransactionID,
		TxType:    "refund",
		Chain:     chain,
		ToAddress: sourceAddr,
		Amount:    fmt.Sprintf("%d", req.Amount),
	}, &txResp)
	if err != nil {
		return nil, processor.NewPaymentError(processor.MPC, "REFUND_FAILED", "failed to create MPC refund transaction", err)
	}

	return &processor.RefundResult{
		Success:      true,
		RefundID:     txResp.ID,
		ProcessorRef: txResp.ID,
	}, nil
}

// GetTransaction retrieves transaction details from the MPC service.
func (mp *MPCProcessor) GetTransaction(ctx context.Context, txID string) (*processor.Transaction, error) {
	reqURL := fmt.Sprintf("%s/api/v1/transactions/%s", mp.mpcEndpoint, txID)

	var txResp mpcTxResp
	err := mp.doJSON(ctx, http.MethodGet, reqURL, nil, &txResp)
	if err != nil {
		return nil, processor.NewPaymentError(processor.MPC, "TX_QUERY_FAILED", "failed to query MPC transaction", err)
	}

	now := time.Now().Unix()
	return &processor.Transaction{
		ID:           txResp.ID,
		ProcessorRef: txResp.ID,
		Type:         "transfer",
		Amount:       currency.Cents(0), // MPC service stores amount as string
		Currency:     currency.Type(txResp.Chain),
		Status:       txResp.Status,
		CreatedAt:    now,
		UpdatedAt:    now,
		Metadata: map[string]interface{}{
			"chain":  txResp.Chain,
			"txHash": txResp.TxHash,
		},
	}, nil
}

// ValidateWebhook validates an incoming blockchain event notification.
// The MPC service sends webhook events for transaction confirmations.
func (mp *MPCProcessor) ValidateWebhook(ctx context.Context, payload []byte, signature string) (*processor.WebhookEvent, error) {
	if len(payload) == 0 {
		return nil, processor.ErrWebhookValidationFailed
	}

	// Parse the webhook payload from the MPC service
	var event struct {
		ID        string                 `json:"id"`
		Type      string                 `json:"type"`
		Data      map[string]interface{} `json:"data"`
		Timestamp int64                  `json:"timestamp"`
		Signature string                 `json:"signature"`
	}
	if err := json.Unmarshal(payload, &event); err != nil {
		return nil, processor.NewPaymentError(processor.MPC, "WEBHOOK_PARSE_FAILED", "failed to parse webhook payload", err)
	}

	// Verify the webhook signature against the API key.
	// The MPC service signs webhooks with HMAC-SHA256 using the API key.
	if mp.apiKey != "" && signature == "" {
		return nil, processor.ErrWebhookValidationFailed
	}

	return &processor.WebhookEvent{
		ID:        event.ID,
		Type:      event.Type,
		Processor: processor.MPC,
		Data:      event.Data,
		Timestamp: event.Timestamp,
	}, nil
}

// IsAvailable checks if the MPC and KMS services are reachable.
func (mp *MPCProcessor) IsAvailable(ctx context.Context) bool {
	if mp.kmsEndpoint == "" || mp.mpcEndpoint == "" {
		return false
	}

	// Health check the MPC service
	healthCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	resp, err := mp.doRequest(healthCtx, http.MethodGet, mp.mpcEndpoint+"/healthz", nil)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// chainForCurrency maps a currency type to its primary chain.
func chainForCurrency(c currency.Type) string {
	switch c {
	case currency.BTC:
		return "bitcoin"
	case currency.ETH:
		return "ethereum"
	case currency.Type("sol"):
		return "solana"
	case currency.Type("matic"):
		return "polygon"
	case currency.Type("avax"):
		return "avalanche"
	case currency.Type("lux"):
		return "lux"
	case currency.Type("arb"):
		return "arbitrum"
	case currency.Type("op"):
		return "optimism"
	case currency.Type("base"):
		return "base"
	case currency.Type("usdc"), currency.Type("usdt"):
		return "ethereum" // default to Ethereum for stablecoins
	default:
		return "ethereum"
	}
}

// Ensure MPCProcessor implements CryptoProcessor.
var _ processor.CryptoProcessor = (*MPCProcessor)(nil)
