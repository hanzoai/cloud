package x402

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/payment/processor"
)

// Facilitator handles payment verification and settlement for x402 transactions.
// It accepts a signed ERC-3009 transferWithAuthorization, verifies the payer's
// signature, checks on-chain allowance/balance, and executes the transfer.
type Facilitator struct {
	// registry provides access to payment processors (MPC, etc.)
	registry *processor.Registry

	// httpClient for calling external facilitator service (if remote)
	httpClient *http.Client

	// remoteURL is the URL of a remote facilitator service (optional).
	// If empty, the facilitator handles settlement locally via the processor registry.
	remoteURL string

	// tokenName is the ERC-20 token name for EIP-712 domain (e.g., "USD Coin").
	tokenName string

	// tokenVersion is the ERC-20 token version for EIP-712 domain (e.g., "2").
	tokenVersion string
}

// FacilitatorConfig holds configuration for creating a Facilitator.
type FacilitatorConfig struct {
	// Registry is the processor registry for executing payments.
	Registry *processor.Registry

	// RemoteURL is the URL of a remote facilitator service.
	// If set, settlement is delegated to this service.
	RemoteURL string

	// TokenName is the ERC-20 token name (default: "USD Coin").
	TokenName string

	// TokenVersion is the ERC-20 token version (default: "2").
	TokenVersion string
}

// NewFacilitator creates a new Facilitator.
func NewFacilitator(cfg FacilitatorConfig) *Facilitator {
	tokenName := cfg.TokenName
	if tokenName == "" {
		tokenName = "USD Coin"
	}
	tokenVersion := cfg.TokenVersion
	if tokenVersion == "" {
		tokenVersion = "2"
	}

	return &Facilitator{
		registry:     cfg.Registry,
		httpClient:   &http.Client{Timeout: 30 * time.Second},
		remoteURL:    strings.TrimRight(cfg.RemoteURL, "/"),
		tokenName:    tokenName,
		tokenVersion: tokenVersion,
	}
}

// Settle verifies and executes a payment authorization.
// It validates the ERC-3009 signature, checks that the authorization matches
// the payment request, and either settles locally via MPC or delegates to
// a remote facilitator service.
func (f *Facilitator) Settle(ctx context.Context, req *PaymentRequest, auth *PaymentAuthorization) (*PaymentReceipt, error) {
	// Validate the authorization matches the request.
	if auth.Value != req.Amount {
		return nil, fmt.Errorf("x402: payment amount mismatch: auth=%s, request=%s", auth.Value, req.Amount)
	}

	// Verify time bounds.
	now := time.Now().Unix()
	if now > auth.ValidBefore {
		return nil, fmt.Errorf("x402: authorization expired")
	}
	if now < auth.ValidAfter {
		return nil, fmt.Errorf("x402: authorization not yet valid")
	}

	// If a remote facilitator is configured, delegate to it.
	if f.remoteURL != "" {
		return f.settleRemote(ctx, req, auth)
	}

	// Local settlement: verify signature and execute via processor registry.
	return f.settleLocal(ctx, req, auth)
}

// settleLocal handles settlement using the local processor registry.
func (f *Facilitator) settleLocal(ctx context.Context, req *PaymentRequest, auth *PaymentAuthorization) (*PaymentReceipt, error) {
	// Verify the ERC-3009 signature.
	if err := f.verifyAuthorization(req, auth); err != nil {
		return nil, fmt.Errorf("x402: signature verification failed: %w", err)
	}

	// Execute the payment via the MPC crypto processor.
	if f.registry == nil {
		return nil, fmt.Errorf("x402: no processor registry configured")
	}

	cryptoProc, err := f.registry.GetCrypto(processor.MPC)
	if err != nil {
		return nil, fmt.Errorf("x402: MPC processor not available: %w", err)
	}

	// Parse amount for the charge request.
	// USDC amounts are in 6-decimal units; convert to cents (USD).
	amount, ok := new(big.Int).SetString(auth.Value, 10)
	if !ok {
		return nil, fmt.Errorf("x402: invalid payment amount: %s", auth.Value)
	}
	// 1 USDC = 1,000,000 smallest units = 100 cents, so divide by 10,000
	centsAmount := new(big.Int).Div(amount, big.NewInt(10000)).Int64()

	// Create a charge request through the MPC processor.
	result, err := cryptoProc.Charge(ctx, processor.PaymentRequest{
		Amount:     currency.Cents(centsAmount),
		Currency:   "usdc",
		CustomerID: auth.From,
		Address:    req.Payee,
		Chain:      chainFromNetwork(req.Network, req.ChainID),
		Metadata: map[string]interface{}{
			"x402":      true,
			"nonce":     auth.Nonce,
			"resource":  req.Resource,
			"signature": auth.Signature,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("x402: payment execution failed: %w", err)
	}

	txHash := ""
	if result.Metadata != nil {
		if h, ok := result.Metadata["txHash"].(string); ok {
			txHash = h
		}
	}

	// Compute the request hash for the receipt.
	requestHash := computeRequestHash(req)

	return &PaymentReceipt{
		RequestHash: requestHash,
		Payer:       auth.From,
		Payee:       req.Payee,
		Amount:      req.Amount,
		TxHash:      txHash,
		Timestamp:   time.Now().Unix(),
		Success:     result.Success,
	}, nil
}

// settleRemote delegates settlement to a remote facilitator service.
func (f *Facilitator) settleRemote(ctx context.Context, req *PaymentRequest, auth *PaymentAuthorization) (*PaymentReceipt, error) {
	payload := struct {
		Request       *PaymentRequest       `json:"request"`
		Authorization *PaymentAuthorization `json:"authorization"`
	}{
		Request:       req,
		Authorization: auth,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("x402: marshal settlement request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, f.remoteURL+"/api/v1/settle", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("x402: build settlement request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := f.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("x402: settlement request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("x402: read settlement response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("x402: facilitator returned %d: %s", resp.StatusCode, string(respBody))
	}

	var receipt PaymentReceipt
	if err := json.Unmarshal(respBody, &receipt); err != nil {
		return nil, fmt.Errorf("x402: decode settlement response: %w", err)
	}

	return &receipt, nil
}

// verifyAuthorization verifies the ERC-3009 transferWithAuthorization signature.
func (f *Facilitator) verifyAuthorization(req *PaymentRequest, auth *PaymentAuthorization) error {
	// Strip 0x prefix from signature.
	sigHex := auth.Signature
	if strings.HasPrefix(sigHex, "0x") || strings.HasPrefix(sigHex, "0X") {
		sigHex = sigHex[2:]
	}

	sigBytes, err := hex.DecodeString(sigHex)
	if err != nil {
		return fmt.Errorf("invalid signature hex: %w", err)
	}

	// Compute the EIP-712 digest for transferWithAuthorization.
	digest, err := f.computeEIP712Digest(req, auth)
	if err != nil {
		return fmt.Errorf("compute EIP-712 digest: %w", err)
	}

	// Verify that the recovered signer matches auth.From.
	return VerifySignature(digest, sigBytes, auth.From)
}

// computeEIP712Digest computes the EIP-712 typed data hash for an ERC-3009
// transferWithAuthorization.
func (f *Facilitator) computeEIP712Digest(req *PaymentRequest, auth *PaymentAuthorization) ([32]byte, error) {
	// Domain separator
	domainSep := EIP712DomainSeparator(f.tokenName, f.tokenVersion, req.ChainID, req.Token)

	// Struct hash: TransferWithAuthorization(address from, address to, uint256 value, uint256 validAfter, uint256 validBefore, bytes32 nonce)
	typeHash := Keccak256([]byte(
		"TransferWithAuthorization(address from,address to,uint256 value,uint256 validAfter,uint256 validBefore,bytes32 nonce)",
	))

	value, ok := new(big.Int).SetString(auth.Value, 10)
	if !ok {
		return [32]byte{}, fmt.Errorf("invalid value: %s", auth.Value)
	}

	nonceHex := auth.Nonce
	if strings.HasPrefix(nonceHex, "0x") || strings.HasPrefix(nonceHex, "0X") {
		nonceHex = nonceHex[2:]
	}
	nonceBytes, err := hex.DecodeString(nonceHex)
	if err != nil {
		return [32]byte{}, fmt.Errorf("invalid nonce hex: %w", err)
	}
	var nonce [32]byte
	copy(nonce[32-len(nonceBytes):], nonceBytes)

	// ABI-encode the struct fields
	encoded := make([]byte, 0, 224) // 7 * 32 bytes
	encoded = append(encoded, typeHash[:]...)
	encoded = append(encoded, padLeft(addressToBytes(auth.From), 32)...)
	encoded = append(encoded, padLeft(addressToBytes(auth.To), 32)...)
	encoded = append(encoded, padLeft(value.Bytes(), 32)...)
	encoded = append(encoded, padLeft(big.NewInt(auth.ValidAfter).Bytes(), 32)...)
	encoded = append(encoded, padLeft(big.NewInt(auth.ValidBefore).Bytes(), 32)...)
	encoded = append(encoded, nonce[:]...)

	structHash := Keccak256(encoded)

	// Final EIP-712 digest: keccak256("\x19\x01" || domainSeparator || structHash)
	digestInput := make([]byte, 0, 66)
	digestInput = append(digestInput, 0x19, 0x01)
	digestInput = append(digestInput, domainSep[:]...)
	digestInput = append(digestInput, structHash[:]...)

	return Keccak256(digestInput), nil
}

// computeRequestHash computes a hash of the payment request for receipt tracking.
func computeRequestHash(req *PaymentRequest) string {
	data, _ := json.Marshal(req)
	hash := Keccak256(data)
	return fmt.Sprintf("0x%x", hash[:])
}

// chainFromNetwork maps a network name and chain ID to a chain string for the processor.
func chainFromNetwork(network string, chainID int64) string {
	switch network {
	case "lux":
		return "lux"
	case "ethereum":
		return "ethereum"
	case "polygon":
		return "polygon"
	case "arbitrum":
		return "arbitrum"
	case "optimism":
		return "optimism"
	case "base":
		return "base"
	default:
		// Fall back to chain ID mapping.
		switch chainID {
		case 1:
			return "ethereum"
		case 137:
			return "polygon"
		case 42161:
			return "arbitrum"
		case 10:
			return "optimism"
		case 8453:
			return "base"
		case 96369:
			return "lux"
		default:
			return "ethereum"
		}
	}
}
