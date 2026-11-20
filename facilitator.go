package main

import (
	"context"
	"crypto/ecdsa"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
)

// Facilitator interface defines the contract for x402 payment facilitators.
// This allows multiple independent facilitators (Circle, Coinbase, custom).
type Facilitator interface {
	// Name returns the facilitator identifier
	Name() string
	// VerifyPayment verifies a payment authorization against the facilitator
	VerifyPayment(ctx context.Context, req *PaymentRequest) (*PaymentVerification, error)
	// Settle initiates settlement with the facilitator
	Settle(ctx context.Context, req *SettlementRequest) (*SettlementResult, error)
	// SupportedNetworks returns the networks this facilitator supports
	SupportedNetworks() []string
	// SupportedAssets returns the assets this facilitator supports per network
	SupportedAssets() map[string][]string
}

// PaymentRequest represents an x402 payment verification request
type PaymentRequest struct {
	X402Version      int                    `json:"x402Version"`
	Network          string                 `json:"network"`
	Asset            string                 `json:"asset"`
	Amount           string                 `json:"amount"`
	PayTo            string                 `json:"payTo"`
	Extra            map[string]interface{} `json:"extra,omitempty"`
	PaymentHeader    string                 `json:"paymentHeader"`
	PaymentSignature string                 `json:"paymentSignature"`
	Payload          map[string]interface{} `json:"payload,omitempty"`
}

// PaymentVerification represents the result of payment verification
type PaymentVerification struct {
	Valid       bool                   `json:"valid"`
	Error       string                 `json:"error,omitempty"`
	Payer       string                 `json:"payer,omitempty"`
	Transaction string                 `json:"transaction,omitempty"`
	Timestamp   int64                  `json:"timestamp"`
	Receipt     *PaymentReceipt        `json:"receipt,omitempty"`
}

// PaymentReceipt represents a verified payment receipt
type PaymentReceipt struct {
	TransactionHash string                 `json:"transactionHash"`
	BlockNumber     uint64                 `json:"blockNumber"`
	From            string                 `json:"from"`
	To              string                 `json:"to"`
	Value           string                 `json:"value"`
	Asset           string                 `json:"asset"`
	Network         string                 `json:"network"`
	Timestamp       int64                  `json:"timestamp"`
	Extra           map[string]interface{} `json:"extra,omitempty"`
}

// SettlementRequest represents a settlement request
type SettlementRequest struct {
	Payments []PaymentRequest `json:"payments"`
}

// SettlementResult represents the result of settlement
type SettlementResult struct {
	Success     bool              `json:"success"`
	Transaction string            `json:"transaction,omitempty"`
	Error       string            `json:"error,omitempty"`
	Receipts    []PaymentReceipt  `json:"receipts,omitempty"`
}

// ERC3009Authorization represents an EIP-3009 transferWithAuthorization
type ERC3009Authorization struct {
	From      common.Address `json:"from"`
	To        common.Address `json:"to"`
	Value     *big.Int       `json:"value"`
	ValidAfter uint64        `json:"validAfter"`
	ValidBefore uint64       `json:"validBefore"`
	Nonce     string         `json:"nonce"`
	Signature []byte         `json:"signature"`
}

// FacilitatorRegistry manages multiple facilitators
type FacilitatorRegistry struct {
	mu            sync.RWMutex
	facilitators  map[string]Facilitator
	defaultName   string
}

// NewFacilitatorRegistry creates a new facilitator registry
func NewFacilitatorRegistry() *FacilitatorRegistry {
	return &FacilitatorRegistry{
		facilitators: make(map[string]Facilitator),
	}
}

// Register adds a facilitator to the registry
func (r *FacilitatorRegistry) Register(name string, f Facilitator) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.facilitators[name] = f
	if r.defaultName == "" {
		r.defaultName = name
	}
}

// Get returns a facilitator by name
func (r *FacilitatorRegistry) Get(name string) (Facilitator, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	f, ok := r.facilitators[name]
	return f, ok
}

// Default returns the default facilitator
func (r *FacilitatorRegistry) Default() (Facilitator, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.defaultName == "" {
		return nil, false
	}
	f, ok := r.facilitators[r.defaultName]
	return f, ok
}

// All returns all registered facilitators
func (r *FacilitatorRegistry) All() []Facilitator {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]Facilitator, 0, len(r.facilitators))
	for _, f := range r.facilitators {
		result = append(result, f)
	}
	return result
}

// ReplayProtection provides nonce-based replay protection for payments
type ReplayProtection struct {
	mu           sync.RWMutex
	usedNonces   map[string]map[string]int64 // network -> nonce -> timestamp
	maxAge       time.Duration
	maxEntries   int
	cleanupInterval time.Duration
	stopCh       chan struct{}
}

// NewReplayProtection creates a new replay protection instance
func NewReplayProtection(maxAge time.Duration, maxEntries int) *ReplayProtection {
	rp := &ReplayProtection{
		usedNonces:      make(map[string]map[string]int64),
		maxAge:          maxAge,
		maxEntries:      maxEntries,
		cleanupInterval: time.Hour,
		stopCh:          make(chan struct{}),
	}
	go rp.cleanupLoop()
	return rp
}

// CheckAndMark checks if a nonce is already used and marks it if not
func (rp *ReplayProtection) CheckAndMark(network, nonce string) bool {
	rp.mu.Lock()
	defer rp.mu.Unlock()

	now := time.Now().Unix()
	if rp.usedNonces[network] == nil {
		rp.usedNonces[network] = make(map[string]int64)
	}

	// Check if nonce was used recently
	if ts, exists := rp.usedNonces[network][nonce]; exists {
		if now-ts < int64(rp.maxAge.Seconds()) {
			return false // Already used
		}
		// Expired, allow reuse
	}

	// Check capacity
	totalEntries := 0
	for _, m := range rp.usedNonces {
		totalEntries += len(m)
	}
	if totalEntries >= rp.maxEntries {
		// Remove oldest entries
		rp.evictOldest(now)
	}

	rp.usedNonces[network][nonce] = now
	return true
}

// evictOldest removes the oldest nonce entries
func (rp *ReplayProtection) evictOldest(now int64) {
	oldestNetwork := ""
	oldestNonce := ""
	oldestTime := now

	for network, nonces := range rp.usedNonces {
		for nonce, ts := range nonces {
			if ts < oldestTime {
				oldestTime = ts
				oldestNetwork = network
				oldestNonce = nonce
			}
		}
	}

	if oldestNetwork != "" && oldestNonce != "" {
		delete(rp.usedNonces[oldestNetwork], oldestNonce)
	}
}

// cleanupLoop periodically cleans up expired nonces
func (rp *ReplayProtection) cleanupLoop() {
	ticker := time.NewTicker(rp.cleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			rp.cleanup()
		case <-rp.stopCh:
			return
		}
	}
}

// cleanup removes expired nonces
func (rp *ReplayProtection) cleanup() {
	rp.mu.Lock()
	defer rp.mu.Unlock()

	now := time.Now().Unix()
	cutoff := now - int64(rp.maxAge.Seconds())

	for network, nonces := range rp.usedNonces {
		for nonce, ts := range nonces {
			if ts < cutoff {
				delete(nonces, nonce)
			}
		}
		if len(nonces) == 0 {
			delete(rp.usedNonces, network)
		}
	}
}

// Stop stops the cleanup loop
func (rp *ReplayProtection) Stop() {
	close(rp.stopCh)
}

// PaymentProcessor handles x402 payment processing
type PaymentProcessor struct {
	registry       *FacilitatorRegistry
	replay         *ReplayProtection
	receiptStore   *ReceiptStore
	priceConfig    map[string]string
	serviceNetworks map[string]string
	usdcDecimals   int
}

// NewPaymentProcessor creates a new payment processor
func NewPaymentProcessor(registry *FacilitatorRegistry, priceConfig map[string]string, serviceNetworks map[string]string) *PaymentProcessor {
	return &PaymentProcessor{
		registry:       registry,
		replay:         NewReplayProtection(24*time.Hour, 100000),
		receiptStore:   NewReceiptStore(),
		priceConfig:    priceConfig,
		serviceNetworks: serviceNetworks,
		usdcDecimals:   6,
	}
}

// ProcessPayment processes an x402 payment for a service
func (p *PaymentProcessor) ProcessPayment(ctx context.Context, path string, req *http.Request) (*PaymentVerification, int, error) {
	// Get service config
	network, ok := p.serviceNetworks[path]
	if !ok {
		return nil, http.StatusNotFound, errors.New("service not found")
	}

	price := p.priceConfig[path]
	if price == "" {
		price = "0.005" // default
	}

	// Parse x402 headers
	paymentHeader := req.Header.Get("X-PAYMENT")
	paymentSig := req.Header.Get("X-PAYMENT-SIGNATURE")

	if paymentHeader == "" || paymentSig == "" {
		// Return 402 with manifest
		return p.create402Response(path, network, price), http.StatusPaymentRequired, nil
	}

	// Parse payment authorization
	auth, err := p.parsePaymentAuthorization(paymentHeader, paymentSig, network)
	if err != nil {
		return nil, http.StatusBadRequest, fmt.Errorf("invalid payment authorization: %w", err)
	}

	// Check replay protection
	if !p.replay.CheckAndMark(network, auth.Nonce) {
		return nil, http.StatusConflict, errors.New("replay detected: nonce already used")
	}

	// Verify with facilitator
	facilitator, ok := p.registry.Default()
	if !ok {
		return nil, http.StatusInternalServerError, errors.New("no facilitator configured")
	}

	paymentReq := &PaymentRequest{
		X402Version:      1,
		Network:          network,
		Asset:            "USDC", // default
		Amount:           price,
		PayTo:            "", // will be filled by facilitator
		PaymentHeader:    paymentHeader,
		PaymentSignature: paymentSig,
		Payload:          map[string]interface{}{"path": path},
	}

	verification, err := facilitator.VerifyPayment(ctx, paymentReq)
	if err != nil {
		return nil, http.StatusInternalServerError, fmt.Errorf("verification failed: %w", err)
	}

	if !verification.Valid {
		return verification, http.StatusPaymentRequired, nil
	}

	// Store receipt
	receipt := verification.Receipt
	if receipt != nil {
		p.receiptStore.Store(receipt)
	}

	return verification, http.StatusOK, nil
}

// create402Response creates a 402 response with payment manifest
func (p *PaymentProcessor) create402Response(path, network, price string) *PaymentVerification {
	amountUnits := p.toBaseUnits(price)
	return &PaymentVerification{
		Valid: false,
		Error: "Payment required",
		Receipt: &PaymentReceipt{
			Value:     price,
			Asset:     "USDC",
			Network:   network,
			Timestamp: time.Now().Unix(),
			Extra: map[string]interface{}{
				"x402Version":       1,
				"accepts":           []string{"exact"},
				"maxAmount":         price,
				"asset":             "USDC",
				"payTo":             "", // filled by facilitator
				"network":           network,
				"maxTimeoutSeconds": 60,
				"description":       fmt.Sprintf("Payment required for %s", path),
				"amountUnits":       amountUnits,
			},
		},
	}
}

// toBaseUnits converts USDC amount to base units (6 decimals)
func (p *PaymentProcessor) toBaseUnits(amount string) string {
	f, _ := new(big.Float).SetString(amount)
	multiplier := new(big.Float).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(p.usdcDecimals)), nil))
	result := new(big.Float).Mul(f, multiplier)
	i, _ := result.Int(nil)
	return i.String()
}

// parsePaymentAuthorization parses the x402 payment authorization header
func (p *PaymentProcessor) parsePaymentAuthorization(header, sig, network string) (*ERC3009Authorization, error) {
	// For EVM networks, parse as ERC-3009
	if network == "base" || network == "ethereum" || network == "arbitrum" || network == "optimism" {
		return p.parseERC3009(header, sig)
	}
	// For Solana, parse differently
	if network == "solana" {
		return p.parseSolana(header, sig)
	}
	return nil, errors.New("unsupported network")
}

// parseERC3009 parses an ERC-3009 authorization
func (p *PaymentProcessor) parseERC3009(header, sig string) (*ERC3009Authorization, error) {
	// Parse the header which should contain the authorization data
	var authData map[string]interface{}
	if err := json.Unmarshal([]byte(header), &authData); err != nil {
		return nil, err
	}

	// Extract fields
	fromStr, _ := authData["from"].(string)
	toStr, _ := authData["to"].(string)
	valueStr, _ := authData["value"].(string)
	validAfter, _ := authData["validAfter"].(float64)
	validBefore, _ := authData["validBefore"].(float64)
	nonce, _ := authData["nonce"].(string)

	from := common.HexToAddress(fromStr)
	to := common.HexToAddress(toStr)
	value, ok := new(big.Int).SetString(valueStr, 10)
	if !ok {
		return nil, errors.New("invalid value")
	}

	sigBytes, err := hex.DecodeString(strings.TrimPrefix(sig, "0x"))
	if err != nil {
		return nil, err
	}

	return &ERC3009Authorization{
		From:        from,
		To:          to,
		Value:       value,
		ValidAfter:  uint64(validAfter),
		ValidBefore: uint64(validBefore),
		Nonce:       nonce,
		Signature:   sigBytes,
	}, nil
}

// parseSolana parses a Solana payment authorization
func (p *PaymentProcessor) parseSolana(header, sig string) (*ERC3009Authorization, error) {
	// Solana uses different authorization format
	// For now, return a basic auth struct
	return &ERC3009Authorization{
		Nonce: header, // use header as nonce for replay protection
	}, nil
}

// ReceiptStore stores payment receipts
type ReceiptStore struct {
	mu       sync.RWMutex
	receipts map[string]*PaymentReceipt
}

// NewReceiptStore creates a new receipt store
func NewReceiptStore() *ReceiptStore {
	return &ReceiptStore{
		receipts: make(map[string]*PaymentReceipt),
	}
}

// Store stores a receipt
func (rs *ReceiptStore) Store(receipt *PaymentReceipt) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	key := fmt.Sprintf("%s:%d", receipt.Network, receipt.BlockNumber)
	rs.receipts[key] = receipt
}

// Get retrieves a receipt
func (rs *ReceiptStore) Get(network string, blockNumber uint64) (*PaymentReceipt, bool) {
	rs.mu.RLock()
	defer rs.mu.RUnlock()
	key := fmt.Sprintf("%s:%d", network, blockNumber)
	r, ok := rs.receipts[key]
	return r, ok
}

// List returns all receipts for a network
func (rs *ReceiptStore) List(network string) []*PaymentReceipt {
	rs.mu.RLock()
	defer rs.mu.RUnlock()
	var result []*PaymentReceipt
	prefix := network + ":"
	for k, r := range rs.receipts {
		if strings.HasPrefix(k, prefix) {
			result = append(result, r)
		}
	}
	return result
}

// CircleFacilitator implements Facilitator for Circle's x402 facilitator
type CircleFacilitator struct {
	name          string
	facilitatorURL string
	client        *http.Client
	apiKey        string
}

// NewCircleFacilitator creates a new Circle facilitator
func NewCircleFacilitator(facilitatorURL, apiKey string) *CircleFacilitator {
	return &CircleFacilitator{
		name:          "circle",
		facilitatorURL: facilitatorURL,
		client:        &http.Client{Timeout: 30 * time.Second},
		apiKey:        apiKey,
	}
}

func (f *CircleFacilitator) Name() string { return f.name }

func (f *CircleFacilitator) SupportedNetworks() []string {
	return []string{"base", "ethereum", "arbitrum", "optimism", "polygon"}
}

func (f *CircleFacilitator) SupportedAssets() map[string][]string {
	return map[string][]string{
		"base":       {"USDC"},
		"ethereum":   {"USDC"},
		"arbitrum":   {"USDC"},
		"optimism":   {"USDC"},
		"polygon":    {"USDC"},
	}
}

func (f *CircleFacilitator) VerifyPayment(ctx context.Context, req *PaymentRequest) (*PaymentVerification, error) {
	// Call Circle's facilitator API
	url := f.facilitatorURL + "/verify"
	body, _ := json.Marshal(req)

	reqHTTP, _ := http.NewRequestWithContext(ctx, "POST", url, strings.NewReader(string(body)))
	reqHTTP.Header.Set("Content-Type", "application/json")
	if f.apiKey != "" {
		reqHTTP.Header.Set("Authorization", "Bearer "+f.apiKey)
	}

	resp, err := f.client.Do(reqHTTP)
	if err != nil {
		return &PaymentVerification{Valid: false, Error: err.Error()}, nil
	}
	defer resp.Body.Close()

	var result PaymentVerification
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return &PaymentVerification{Valid: false, Error: err.Error()}, nil
	}
	return &result, nil
}

func (f *CircleFacilitator) Settle(ctx context.Context, req *SettlementRequest) (*SettlementResult, error) {
	url := f.facilitatorURL + "/settle"
	body, _ := json.Marshal(req)

	reqHTTP, _ := http.NewRequestWithContext(ctx, "POST", url, strings.NewReader(string(body)))
	reqHTTP.Header.Set("Content-Type", "application/json")
	if f.apiKey != "" {
		reqHTTP.Header.Set("Authorization", "Bearer "+f.apiKey)
	}

	resp, err := f.client.Do(reqHTTP)
	if err != nil {
		return &SettlementResult{Success: false, Error: err.Error()}, nil
	}
	defer resp.Body.Close()

	var result SettlementResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return &SettlementResult{Success: false, Error: err.Error()}, nil
	}
	return &result, nil
}

// X402Handler handles x402 middleware for HTTP handlers
type X402Handler struct {
	processor *PaymentProcessor
	path      string
	handler   http.HandlerFunc
}

// NewX402Handler creates a new x402 middleware handler
func NewX402Handler(processor *PaymentProcessor, path string, handler http.HandlerFunc) *X402Handler {
	return &X402Handler{
		processor: processor,
		path:      path,
		handler:   handler,
	}
}

// ServeHTTP implements http.Handler
func (x *X402Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	verification, status, err := x.processor.ProcessPayment(ctx, x.path, r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if status == http.StatusPaymentRequired {
		// Return 402 with payment manifest
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusPaymentRequired)
		json.NewEncoder(w).Encode(verification)
		return
	}

	if status != http.StatusOK {
		http.Error(w, verification.Error, status)
		return
	}

	// Payment verified, add payer info to context and call handler
	ctx = context.WithValue(ctx, "x402_payer", verification.Payer)
	ctx = context.WithValue(ctx, "x402_tx", verification.Transaction)
	r = r.WithContext(ctx)
	x.handler(w, r)
}

// VerifyERC3009Signature verifies an ERC-3009 signature
func VerifyERC3009Signature(auth *ERC3009Authorization, chainID *big.Int) (bool, error) {
	// Construct the EIP-712 typed data hash for transferWithAuthorization
	// This is a simplified version - full implementation requires proper EIP-712 domain separator
	
	// For now, we do basic validation
	if auth.From == (common.Address{}) || auth.To == (common.Address{}) {
		return false, errors.New("invalid addresses")
	}
	if auth.Value == nil || auth.Value.Sign() <= 0 {
		return false, errors.New("invalid value")
	}
	if auth.ValidAfter >= auth.ValidBefore {
		return false, errors.New("invalid validity period")
	}
	if auth.Nonce == "" {
		return false, errors.New("missing nonce")
	}
	if len(auth.Signature) != 65 {
		return false, errors.New("invalid signature length")
	}

	// Verify signature against the authorization data
	// In production, this would reconstruct the EIP-712 typed data and verify
	// For now, we do basic structural validation
	return true, nil
}

// SignERC3009 signs an ERC-3009 authorization (for testing/client use)
func SignERC3009(auth *ERC3009Authorization, privateKey *ecdsa.PrivateKey) ([]byte, error) {
	// In production, this would create the proper EIP-712 typed data hash
	// and sign it. For now, return a dummy signature.
	return make([]byte, 65), nil
}