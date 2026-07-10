package x402

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"time"
)

// Signer signs EIP-712 typed data digests. Implementations include private key
// signers, hardware wallet signers, and MPC signers.
type Signer interface {
	// Address returns the signer's Ethereum address.
	Address() string

	// SignDigest signs a 32-byte EIP-712 digest and returns a 65-byte
	// [R(32) || S(32) || V(1)] signature.
	SignDigest(digest [32]byte) ([]byte, error)
}

// Client handles x402 payment flows for HTTP clients. When a request returns
// HTTP 402, the client automatically constructs a payment authorization,
// signs it with the provided Signer, and retries the request.
type Client struct {
	// httpClient is the underlying HTTP client.
	httpClient *http.Client

	// signer signs payment authorizations.
	signer Signer

	// maxPayment is the maximum amount (in token smallest units) the client
	// will pay per request. Zero means no limit.
	maxPayment *big.Int

	// tokenName is the ERC-20 token name for EIP-712 domain.
	tokenName string

	// tokenVersion is the ERC-20 token version for EIP-712 domain.
	tokenVersion string
}

// ClientConfig holds configuration for creating an x402 Client.
type ClientConfig struct {
	// Signer signs payment authorizations.
	Signer Signer

	// MaxPayment is the maximum amount per request in token smallest units.
	// Zero or nil means no limit.
	MaxPayment *big.Int

	// HTTPClient is the underlying HTTP client. If nil, a default client is used.
	HTTPClient *http.Client

	// TokenName is the ERC-20 token name (default: "USD Coin").
	TokenName string

	// TokenVersion is the ERC-20 token version (default: "2").
	TokenVersion string
}

// NewClient creates a new x402-aware HTTP client.
func NewClient(cfg ClientConfig) *Client {
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	tokenName := cfg.TokenName
	if tokenName == "" {
		tokenName = "USD Coin"
	}
	tokenVersion := cfg.TokenVersion
	if tokenVersion == "" {
		tokenVersion = "2"
	}
	return &Client{
		httpClient:   httpClient,
		signer:       cfg.Signer,
		maxPayment:   cfg.MaxPayment,
		tokenName:    tokenName,
		tokenVersion: tokenVersion,
	}
}

// Do sends an HTTP request and handles x402 payment challenges.
// If the server responds with 402, the client signs a payment authorization
// and retries the request once.
func (c *Client) Do(req *http.Request) (*http.Response, error) {
	// Make the initial request.
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}

	// If not 402, return as-is.
	if resp.StatusCode != http.StatusPaymentRequired {
		return resp, nil
	}

	// Parse the payment request from the 402 response.
	paymentReqHeader := resp.Header.Get(HeaderPaymentRequest)
	if paymentReqHeader == "" {
		// 402 without payment request header; return the response as-is.
		return resp, nil
	}

	// Close the 402 response body before retrying.
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	paymentReq, err := ParsePaymentRequest(paymentReqHeader)
	if err != nil {
		return nil, fmt.Errorf("x402 client: %w", err)
	}

	// Check payment amount against limit.
	if c.maxPayment != nil && c.maxPayment.Sign() > 0 {
		amount, ok := new(big.Int).SetString(paymentReq.Amount, 10)
		if !ok {
			return nil, fmt.Errorf("x402 client: invalid payment amount in request: %s", paymentReq.Amount)
		}
		if amount.Cmp(c.maxPayment) > 0 {
			return nil, fmt.Errorf("x402 client: payment amount %s exceeds limit %s", paymentReq.Amount, c.maxPayment.String())
		}
	}

	// Create the payment authorization.
	auth, err := c.createAuthorization(paymentReq)
	if err != nil {
		return nil, fmt.Errorf("x402 client: create authorization: %w", err)
	}

	// Clone the request and add the payment header.
	retryReq := req.Clone(req.Context())
	retryReq.Header.Set(HeaderPaymentAuthorization, auth.MarshalHeader())

	// If the original request had a body, it may have been consumed.
	// The caller should use GetBody if available.
	if req.GetBody != nil {
		body, err := req.GetBody()
		if err != nil {
			return nil, fmt.Errorf("x402 client: get request body for retry: %w", err)
		}
		retryReq.Body = body
	}

	return c.httpClient.Do(retryReq)
}

// Get is a convenience method for GET requests with x402 support.
func (c *Client) Get(url string) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	return c.Do(req)
}

// createAuthorization creates a signed ERC-3009 payment authorization.
func (c *Client) createAuthorization(paymentReq *PaymentRequest) (*PaymentAuthorization, error) {
	if c.signer == nil {
		return nil, fmt.Errorf("no signer configured")
	}

	// Generate a random nonce.
	nonceBytes := make([]byte, 32)
	if _, err := rand.Read(nonceBytes); err != nil {
		return nil, fmt.Errorf("generate nonce: %w", err)
	}
	nonce := "0x" + hex.EncodeToString(nonceBytes)

	now := time.Now().Unix()
	validAfter := now
	validBefore := now + paymentReq.ValidFor

	auth := &PaymentAuthorization{
		From:        c.signer.Address(),
		To:          paymentReq.Facilitator,
		Value:       paymentReq.Amount,
		ValidAfter:  validAfter,
		ValidBefore: validBefore,
		Nonce:       nonce,
	}

	// Compute the EIP-712 digest.
	digest, err := c.computeDigest(paymentReq, auth)
	if err != nil {
		return nil, fmt.Errorf("compute digest: %w", err)
	}

	// Sign the digest.
	sig, err := c.signer.SignDigest(digest)
	if err != nil {
		return nil, fmt.Errorf("sign digest: %w", err)
	}

	auth.Signature = "0x" + hex.EncodeToString(sig)
	return auth, nil
}

// computeDigest computes the EIP-712 typed data hash for signing.
func (c *Client) computeDigest(paymentReq *PaymentRequest, auth *PaymentAuthorization) ([32]byte, error) {
	// Domain separator.
	domainSep := EIP712DomainSeparator(c.tokenName, c.tokenVersion, paymentReq.ChainID, paymentReq.Token)

	// Type hash for TransferWithAuthorization.
	typeHash := Keccak256([]byte(
		"TransferWithAuthorization(address from,address to,uint256 value,uint256 validAfter,uint256 validBefore,bytes32 nonce)",
	))

	value, ok := new(big.Int).SetString(auth.Value, 10)
	if !ok {
		return [32]byte{}, fmt.Errorf("invalid value: %s", auth.Value)
	}

	nonceHex := auth.Nonce
	if len(nonceHex) > 2 && (nonceHex[:2] == "0x" || nonceHex[:2] == "0X") {
		nonceHex = nonceHex[2:]
	}
	nonceBytes, err := hex.DecodeString(nonceHex)
	if err != nil {
		return [32]byte{}, fmt.Errorf("invalid nonce: %w", err)
	}
	var nonce [32]byte
	copy(nonce[32-len(nonceBytes):], nonceBytes)

	// ABI-encode the struct fields.
	encoded := make([]byte, 0, 224)
	encoded = append(encoded, typeHash[:]...)
	encoded = append(encoded, padLeft(addressToBytes(auth.From), 32)...)
	encoded = append(encoded, padLeft(addressToBytes(auth.To), 32)...)
	encoded = append(encoded, padLeft(value.Bytes(), 32)...)
	encoded = append(encoded, padLeft(big.NewInt(auth.ValidAfter).Bytes(), 32)...)
	encoded = append(encoded, padLeft(big.NewInt(auth.ValidBefore).Bytes(), 32)...)
	encoded = append(encoded, nonce[:]...)

	structHash := Keccak256(encoded)

	// Final EIP-712 digest.
	digestInput := make([]byte, 0, 66)
	digestInput = append(digestInput, 0x19, 0x01)
	digestInput = append(digestInput, domainSep[:]...)
	digestInput = append(digestInput, structHash[:]...)

	return Keccak256(digestInput), nil
}
