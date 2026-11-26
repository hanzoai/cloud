// SPDX-License-Identifier: Apache-2.0 OR MIT

package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zap-proto/zip"
)

// FaultMode injects deterministic or adversarial provider behaviors over real
// loopback HTTP to exercise failover, fail-closed verification, and negative regimes.
type FaultMode string

const (
	FaultNone                  FaultMode = "none"
	FaultCrash500              FaultMode = "crash_500"
	FaultTimeout               FaultMode = "timeout"
	FaultCorruptOutput         FaultMode = "corrupt_output"
	FaultMismatchedReceiptHash FaultMode = "mismatched_receipt_hash"
	FaultForgedQuoteSig        FaultMode = "forged_quote_sig"
	FaultExpiredQuote          FaultMode = "expired_quote"
	FaultHighSSignature        FaultMode = "high_s_signature"
	FaultReplayNonce           FaultMode = "replay_nonce"
	FaultIntermittent          FaultMode = "intermittent"
)

// WorkerConfig configures an independent loopback HTTP inference worker node.
type WorkerConfig struct {
	ProviderID         string
	Region             string
	Models             []ModelSpec
	PriceMicroUSDPer1M uint64
	AdvertisedTTFTMs   int64
	SimulatedTTFT      time.Duration
	SimulatedGenDelay  time.Duration
	QuoteTTL           time.Duration
	CapacityRPM        int
	Domain             EIP712Domain
	KeyPair            *KeyPair
	// PriceChurnStep shifts the quoted price every PriceChurnInterval quotes
	// to test boundary behavior under rapid quote churn.
	PriceChurnStep     uint64
	PriceChurnInterval uint64
}

// QuoteRequest is sent by any gateway to a worker's /worker/v1/quote endpoint.
type QuoteRequest struct {
	ModelID     string `json:"modelId"`
	MaxTokens   int    `json:"maxTokens"`
	RequestHash string `json:"requestHash"`
	GatewayID   string `json:"gatewayId"`
}

// ExecuteRequest is sent by a gateway to a worker's /worker/v1/execute endpoint
// carrying the worker's own previously signed InferenceQuote.
type ExecuteRequest struct {
	Quote           InferenceQuote        `json:"quote"`
	Request         ChatCompletionRequest `json:"request"`
	GatewayID       string                `json:"gatewayId"`
	GatewayOperator string                `json:"gatewayOperator"`
}

// ExecuteResponse is returned by a worker with completion text, usage, latency,
// and a provider-signed ExecutionReceipt.
type ExecuteResponse struct {
	Content          string           `json:"content"`
	PromptTokens     int              `json:"promptTokens"`
	CompletionTokens int              `json:"completionTokens"`
	TTFTMs           float64          `json:"ttftMs"`
	TotalLatencyMs   float64          `json:"totalLatencyMs"`
	Receipt          ExecutionReceipt `json:"receipt"`
}

// WorkerNode is a real standalone HTTP inference worker holding its own
// ephemeral secp256k1 identity and serving the open HIP-1350 worker protocol.
type WorkerNode struct {
	mu           sync.RWMutex
	cfg          WorkerConfig
	kp           *KeyPair
	domain       EIP712Domain
	server       *httptest.Server
	faultMode    FaultMode
	faultRatePct int
	timeoutDelay time.Duration
	staticNonce  string

	quoteCalls   atomic.Int64
	executeCalls atomic.Int64
	failedCalls  atomic.Int64
	nonceSeq     atomic.Uint64
}

// NewWorkerNode constructs and starts a real loopback HTTP WorkerNode.
func NewWorkerNode(cfg WorkerConfig) (*WorkerNode, error) {
	if cfg.ProviderID == "" || len(cfg.Models) == 0 {
		return nil, fmt.Errorf("%w: worker requires providerId and at least one model", ErrInvalidRequest)
	}
	if cfg.Domain.Name == "" {
		cfg.Domain = DefaultDomain()
	}
	kp := cfg.KeyPair
	if kp == nil {
		var err error
		kp, err = GenerateKeyPair()
		if err != nil {
			return nil, err
		}
	}
	if cfg.QuoteTTL <= 0 {
		cfg.QuoteTTL = 30 * time.Second
	}
	if cfg.CapacityRPM <= 0 {
		cfg.CapacityRPM = 600
	}
	if cfg.Region == "" {
		cfg.Region = "us-west"
	}
	w := &WorkerNode{
		cfg:          cfg,
		kp:           kp,
		domain:       cfg.Domain,
		faultMode:    FaultNone,
		timeoutDelay: 500 * time.Millisecond,
		staticNonce:  "replayed-static-nonce-0001",
	}
	w.server = httptest.NewServer(w.Handler())
	return w, nil
}

// Close shuts down the loopback HTTP listener.
func (w *WorkerNode) Close() {
	if w.server != nil {
		w.server.Close()
	}
}

// URL returns the loopback HTTP base URL of this worker.
func (w *WorkerNode) URL() string {
	if w.server == nil {
		return ""
	}
	return w.server.URL
}

// ProviderID returns the worker's identifier.
func (w *WorkerNode) ProviderID() string { return w.cfg.ProviderID }

// Address returns the worker's EIP-55 secp256k1 address.
func (w *WorkerNode) Address() string { return w.kp.Address() }

// KeyPair returns the worker's runtime secp256k1 keypair.
func (w *WorkerNode) KeyPair() *KeyPair { return w.kp }

// QuoteCalls returns the total number of /worker/v1/quote requests served.
func (w *WorkerNode) QuoteCalls() int64 { return w.quoteCalls.Load() }

// ExecuteCalls returns the total number of /worker/v1/execute requests received.
func (w *WorkerNode) ExecuteCalls() int64 { return w.executeCalls.Load() }

// FailedCalls returns the number of injected fault responses emitted.
func (w *WorkerNode) FailedCalls() int64 { return w.failedCalls.Load() }

// SetFaultMode dynamically configures fault injection on the live worker.
func (w *WorkerNode) SetFaultMode(mode FaultMode) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.faultMode = mode
}

// SetFaultRatePercent configures the deterministic fault rate [0, 100] used
// when FaultMode is FaultIntermittent.
func (w *WorkerNode) SetFaultRatePercent(pct int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	w.faultRatePct = pct
}

// SetTimeoutDelay configures how long FaultTimeout stalls before responding.
func (w *WorkerNode) SetTimeoutDelay(d time.Duration) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.timeoutDelay = d
}

// SetPriceMicroUSDPer1M updates the worker's base quoted price.
func (w *WorkerNode) SetPriceMicroUSDPer1M(price uint64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.cfg.PriceMicroUSDPer1M = price
}

// SetQuoteTTL updates the lifetime of newly issued quotes.
func (w *WorkerNode) SetQuoteTTL(ttl time.Duration) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.cfg.QuoteTTL = ttl
}

// SignedRecord builds and EIP-712 signs a current WorkerRecord for directory publication.
func (w *WorkerNode) SignedRecord(nowUnix int64, ttlSeconds int64) (WorkerRecord, error) {
	if nowUnix <= 0 {
		nowUnix = time.Now().Unix()
	}
	if ttlSeconds <= 0 {
		ttlSeconds = 3600
	}
	w.mu.RLock()
	models := append([]ModelSpec(nil), w.cfg.Models...)
	price := w.cfg.PriceMicroUSDPer1M
	ttft := w.cfg.AdvertisedTTFTMs
	capRPM := w.cfg.CapacityRPM
	region := w.cfg.Region
	pid := w.cfg.ProviderID
	w.mu.RUnlock()

	rec := WorkerRecord{
		ProtocolVersion:    ProtocolVersion,
		ProviderID:         pid,
		EndpointURL:        w.URL(),
		Region:             region,
		Models:             models,
		PriceMicroUSDPer1M: price,
		AdvertisedTTFTMs:   ttft,
		CapacityRPM:        capRPM,
		IssuedAt:           nowUnix,
		ExpiresAt:          nowUnix + ttlSeconds,
	}
	if err := SignWorkerRecord(w.domain, w.kp, &rec); err != nil {
		return WorkerRecord{}, err
	}
	return rec, nil
}

// Handler returns the net/http handler implementing /worker/v1/* derived from
// MountWorker's typed zip.App operation registry (with OpenAPI and MCP projections).
func (w *WorkerNode) Handler() http.Handler {
	app, err := NewWorkerApp(w, nil)
	if err != nil {
		return http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
			http.Error(rw, err.Error(), http.StatusInternalServerError)
		})
	}
	return zipHTTPHandler(app)
}

func (w *WorkerNode) opRecord(_ context.Context, _ *emptyIn) (*WorkerRecord, error) {
	rec, err := w.SignedRecord(time.Now().Unix(), 3600)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "%s", err.Error())
	}
	return &rec, nil
}

func (w *WorkerNode) opQuote(_ context.Context, req *QuoteRequest) (*InferenceQuote, error) {
	if req == nil {
		return nil, zip.Errorf(http.StatusBadRequest, "invalid quote request json")
	}
	qNum := uint64(w.quoteCalls.Add(1))
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 128
	}

	w.mu.RLock()
	mode := w.faultMode
	basePrice := w.cfg.PriceMicroUSDPer1M
	ttftMs := w.cfg.AdvertisedTTFTMs
	ttl := w.cfg.QuoteTTL
	churnStep := w.cfg.PriceChurnStep
	churnInterval := w.cfg.PriceChurnInterval
	staticNonce := w.staticNonce
	var matched ModelSpec
	var found bool
	for _, m := range w.cfg.Models {
		if m.ModelID == req.ModelID {
			matched = m
			found = true
			break
		}
	}
	w.mu.RUnlock()

	if !found {
		return nil, zip.Errorf(http.StatusNotFound, "model not supported by worker")
	}

	effectivePrice := basePrice
	if churnStep > 0 && churnInterval > 0 {
		bucket := (qNum / churnInterval) % 5
		effectivePrice = basePrice + bucket*churnStep
	}

	now := time.Now()
	issuedAt := now.Unix()
	ttlSec := int64(ttl / time.Second)
	if ttlSec < 1 {
		ttlSec = 1
	}
	expiresAt := issuedAt + ttlSec

	var nonce string
	if mode == FaultReplayNonce {
		nonce = staticNonce
	} else {
		seq := w.nonceSeq.Add(1)
		rnd, err := RandomNonceHex()
		if err != nil {
			return nil, zip.Errorf(http.StatusInternalServerError, "%s", err.Error())
		}
		nonce = fmt.Sprintf("%s-%d-%s", w.cfg.ProviderID, seq, rnd[:12])
	}

	if mode == FaultExpiredQuote {
		issuedAt = now.Unix() - 120
		expiresAt = now.Unix() - 10
	}

	quote := InferenceQuote{
		QuoteID:            fmt.Sprintf("qt-%s-%d", w.cfg.ProviderID, qNum),
		ProtocolVersion:    ProtocolVersion,
		ProviderID:         w.cfg.ProviderID,
		ModelID:            matched.ModelID,
		ModelDigest:        matched.ModelDigest,
		PriceMicroUSDPer1M: effectivePrice,
		MaxTTFTMs:          ttftMs,
		MaxTokens:          maxTokens,
		IssuedAt:           issuedAt,
		ExpiresAt:          expiresAt,
		Nonce:              nonce,
		RequestHash:        req.RequestHash,
	}
	if err := SignQuote(w.domain, w.kp, &quote); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "%s", err.Error())
	}

	switch mode {
	case FaultForgedQuoteSig:
		quote.Signature = flipLastHexChar(quote.Signature)
		w.failedCalls.Add(1)
	case FaultHighSSignature:
		if highS, err := MalleateSignatureHighS(quote.Signature); err == nil {
			quote.Signature = highS
		}
		w.failedCalls.Add(1)
	case FaultExpiredQuote:
		w.failedCalls.Add(1)
	}

	return &quote, nil
}

func (w *WorkerNode) opExecute(ctx context.Context, req *ExecuteRequest) (*ExecuteResponse, error) {
	if req == nil {
		return nil, zip.Errorf(http.StatusBadRequest, "invalid execute request json")
	}
	execNum := w.executeCalls.Add(1)
	start := time.Now()

	w.mu.RLock()
	mode := w.faultMode
	faultRate := w.faultRatePct
	simTTFT := w.cfg.SimulatedTTFT
	simGen := w.cfg.SimulatedGenDelay
	timeoutDelay := w.timeoutDelay
	w.mu.RUnlock()

	if mode == FaultCrash500 {
		w.failedCalls.Add(1)
		return nil, zip.Errorf(http.StatusInternalServerError, "simulated provider crash")
	}
	if mode == FaultIntermittent && faultRate > 0 {
		// Exact deterministic Bresenham spacing matching faultRate% for any N.
		prev := ((execNum - 1) * int64(faultRate)) / 100
		curr := (execNum * int64(faultRate)) / 100
		if curr > prev {
			w.failedCalls.Add(1)
			return nil, zip.Errorf(http.StatusServiceUnavailable, "simulated intermittent provider fault")
		}
	}
	if mode == FaultTimeout {
		w.failedCalls.Add(1)
		select {
		case <-time.After(timeoutDelay):
			return nil, zip.Errorf(http.StatusGatewayTimeout, "provider timed out")
		case <-ctx.Done():
			return nil, zip.Errorf(http.StatusGatewayTimeout, "request canceled")
		}
	}

	if err := VerifyQuote(w.domain, req.Quote, time.Now().Unix()); err != nil {
		return nil, zip.Errorf(http.StatusBadRequest, "%s", err.Error())
	}
	if !SameAddress(req.Quote.ProviderAddress, w.kp.Address()) {
		return nil, zip.Errorf(http.StatusBadRequest, "quote signed by different provider")
	}
	reqHash := CanonicalRequestHash(req.Request)
	if req.Quote.RequestHash != "" && req.Quote.RequestHash != reqHash {
		return nil, zip.Errorf(http.StatusBadRequest, "%s: quote requestHash (%s) != request hash (%s)", ErrInvalidQuote, req.Quote.RequestHash, reqHash)
	}
	if req.Request.Model != "" && req.Request.Model != req.Quote.ModelID {
		return nil, zip.Errorf(http.StatusBadRequest, "%s: request model (%s) != quote model (%s)", ErrInvalidQuote, req.Request.Model, req.Quote.ModelID)
	}

	completionTokens := req.Request.MaxTokens
	if completionTokens <= 0 {
		completionTokens = 64
	}
	if completionTokens > 512 {
		completionTokens = 512
	}
	if completionTokens > req.Quote.MaxTokens {
		return nil, zip.Errorf(http.StatusBadRequest, "%s: requested completion tokens (%d) exceeds quote maxTokens (%d)",
			ErrInvalidQuote, completionTokens, req.Quote.MaxTokens)
	}

	if simTTFT > 0 {
		select {
		case <-time.After(simTTFT):
		case <-ctx.Done():
			return nil, zip.Errorf(http.StatusGatewayTimeout, "request canceled")
		}
	}
	ttftMs := float64(time.Since(start).Microseconds()) / 1000.0

	if simGen > 0 {
		select {
		case <-time.After(simGen):
		case <-ctx.Done():
			return nil, zip.Errorf(http.StatusGatewayTimeout, "request canceled")
		}
	}

	promptTokens := estimatePromptTokens(req.Request.Messages)

	// Deterministic completion tied to model digest and canonical request hash.
	content := buildDeterministicCompletion(req.Quote.ModelID, req.Quote.ModelDigest, reqHash, req.Request.Messages, completionTokens)
	outputHash := ComputeOutputHash(content)

	qh, err := QuoteHash(w.domain, req.Quote)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "%s", err.Error())
	}
	totalLatencyMs := float64(time.Since(start).Microseconds()) / 1000.0

	receipt := ExecutionReceipt{
		ReceiptID:         fmt.Sprintf("rcpt-%s-%d", w.cfg.ProviderID, execNum),
		ProtocolVersion:   ProtocolVersion,
		VerificationClass: ReceiptVerificationClass,
		GatewayID:         req.GatewayID,
		GatewayOperator:   req.GatewayOperator,
		ProviderID:        w.cfg.ProviderID,
		ModelID:           req.Quote.ModelID,
		ModelDigest:       req.Quote.ModelDigest,
		QuoteID:           req.Quote.QuoteID,
		QuoteHash:         qh,
		QuoteNonce:        req.Quote.Nonce,
		RequestHash:       reqHash,
		OutputHash:        outputHash,
		PromptTokens:      promptTokens,
		CompletionTokens:  completionTokens,
		TotalTokens:       promptTokens + completionTokens,
		TTFTMs:            ttftMs,
		TotalLatencyMs:    totalLatencyMs,
		CompletedAt:       time.Now().Unix(),
	}

	if mode == FaultMismatchedReceiptHash {
		receipt.OutputHash = ComputeOutputHash("forged-alternate-output-digest")
		w.failedCalls.Add(1)
	}

	if err := SignReceipt(w.domain, w.kp, &receipt); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "%s", err.Error())
	}

	returnedContent := content
	if mode == FaultCorruptOutput {
		// Provider returns silently corrupted inference text while holding a
		// receipt signed over the honest output hash; fail-closed receipt
		// verification catches this immediately, whereas skipping verification
		// allows the corruption through.
		returnedContent = "[CORRUPTED-UNVERIFIED-OUTPUT] " + content
		w.failedCalls.Add(1)
	}

	return &ExecuteResponse{
		Content:          returnedContent,
		PromptTokens:     promptTokens,
		CompletionTokens: completionTokens,
		TTFTMs:           ttftMs,
		TotalLatencyMs:   totalLatencyMs,
		Receipt:          receipt,
	}, nil
}

func estimatePromptTokens(msgs []ChatMessage) int {
	total := 0
	for _, m := range msgs {
		words := len(strings.Fields(m.Content))
		if words == 0 {
			words = 1
		}
		total += words + 4
	}
	if total < 8 {
		total = 8
	}
	return total
}

func buildDeterministicCompletion(modelID, modelDigest, reqHash string, msgs []ChatMessage, targetTokens int) string {
	lastPrompt := ""
	if len(msgs) > 0 {
		lastPrompt = msgs[len(msgs)-1].Content
	}
	seedTag := SHA256Hex([]byte(modelID), []byte(modelDigest), []byte(reqHash))[:16]
	var b strings.Builder
	fmt.Fprintf(&b, "[%s|%s] Verified response for prompt %q:", modelID, seedTag, truncatePrompt(lastPrompt, 64))
	for i := 0; i < targetTokens/8; i++ {
		fmt.Fprintf(&b, " tok%02d_%s", i, seedTag[i%8:i%8+4])
	}
	return b.String()
}

func truncatePrompt(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

func flipLastHexChar(s string) string {
	if len(s) == 0 {
		return "0"
	}
	last := s[len(s)-1]
	if last == '0' {
		return s[:len(s)-1] + "1"
	}
	return s[:len(s)-1] + "0"
}

// --- Decentralized Multi-Peer Directory HTTP Server ---

// DirectoryPeerServer runs an independently administered loopback HTTP peer
// directory supporting open worker registration, gossip sync, and signed
// snapshot export with zero Hanzo lock-in.
type DirectoryPeerServer struct {
	PeerID   string
	Operator string
	dir      *PeerDirectory
	kp       *KeyPair
	server   *httptest.Server
	disabled atomic.Bool
	calls    atomic.Int64
}

// NewDirectoryPeerServer creates and starts a loopback HTTP DirectoryPeerServer.
func NewDirectoryPeerServer(peerID, operator string, domain EIP712Domain, kp *KeyPair) (*DirectoryPeerServer, error) {
	if peerID == "" {
		return nil, fmt.Errorf("%w: empty peerId", ErrInvalidRequest)
	}
	if domain.Name == "" {
		domain = DefaultDomain()
	}
	if kp == nil {
		var err error
		kp, err = GenerateKeyPair()
		if err != nil {
			return nil, err
		}
	}
	d := &DirectoryPeerServer{
		PeerID:   peerID,
		Operator: operator,
		dir:      NewPeerDirectory(domain),
		kp:       kp,
	}
	d.server = httptest.NewServer(d.Handler())
	return d, nil
}

// Close stops the HTTP listener.
func (d *DirectoryPeerServer) Close() {
	if d.server != nil {
		d.server.Close()
	}
}

// URL returns the base URL of this peer directory server.
func (d *DirectoryPeerServer) URL() string {
	if d.server == nil {
		return ""
	}
	return d.server.URL
}

// Directory returns the underlying PeerDirectory store.
func (d *DirectoryPeerServer) Directory() *PeerDirectory { return d.dir }

// Disable simulates shutting down or partitioning this directory peer.
func (d *DirectoryPeerServer) Disable() { d.disabled.Store(true) }

// Enable re-enables this directory peer.
func (d *DirectoryPeerServer) Enable() { d.disabled.Store(false) }

// Calls returns total HTTP requests received by this directory peer.
func (d *DirectoryPeerServer) Calls() int64 { return d.calls.Load() }

// RegisterWorker validates and registers a signed WorkerRecord locally.
func (d *DirectoryPeerServer) RegisterWorker(rec WorkerRecord) error {
	return d.dir.RegisterWorker(rec, time.Now().Unix())
}

// SyncFromPeer fetches workers from peerURL over HTTP and merges valid records.
func (d *DirectoryPeerServer) SyncFromPeer(ctx context.Context, peerURL string, client *http.Client) (int, int, error) {
	if client == nil {
		client = &http.Client{Timeout: 3 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(peerURL, "/")+"/directory/v1/workers", nil)
	if err != nil {
		return 0, 0, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, 0, fmt.Errorf("%w: directory peer returned status %d", ErrProviderUnavailable, resp.StatusCode)
	}
	var payload struct {
		Workers []WorkerRecord `json:"workers"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return 0, 0, err
	}
	accepted, rejected := d.dir.MergeRecords(payload.Workers, time.Now().Unix())
	return accepted, rejected, nil
}

// GossipToPeer pushes this peer's verified records to peerURL over HTTP.
func (d *DirectoryPeerServer) GossipToPeer(ctx context.Context, peerURL string, client *http.Client) error {
	if client == nil {
		client = &http.Client{Timeout: 3 * time.Second}
	}
	body, err := json.Marshal(struct {
		Workers []WorkerRecord `json:"workers"`
	}{Workers: d.dir.ListWorkers(time.Now().Unix())})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(peerURL, "/")+"/directory/v1/gossip", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: gossip target returned %d", ErrProviderUnavailable, resp.StatusCode)
	}
	return nil
}

// DirectoryWorkersResponse is the typed payload for GET /directory/v1/workers.
type DirectoryWorkersResponse struct {
	PeerID          string         `json:"peerId"`
	Operator        string         `json:"operator"`
	ProtocolVersion string         `json:"protocolVersion"`
	Workers         []WorkerRecord `json:"workers"`
}

// DirectoryRegisterResponse is the typed payload for POST /directory/v1/workers.
type DirectoryRegisterResponse struct {
	Registered bool   `json:"registered"`
	ProviderID string `json:"providerId"`
}

// DirectoryGossipRequest is the typed payload for POST /directory/v1/gossip.
type DirectoryGossipRequest struct {
	Workers []WorkerRecord `json:"workers"`
}

// DirectoryGossipResponse is the typed response for POST /directory/v1/gossip.
type DirectoryGossipResponse struct {
	Accepted int `json:"accepted"`
	Rejected int `json:"rejected"`
	Total    int `json:"total"`
}

// Handler serves /directory/v1/workers, /directory/v1/gossip, and
// /directory/v1/snapshot derived from MountDirectory's typed zip.App operation
// registry (with OpenAPI and MCP projections).
func (d *DirectoryPeerServer) Handler() http.Handler {
	app, err := NewDirectoryApp(d, nil)
	if err != nil {
		return http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
			http.Error(rw, err.Error(), http.StatusInternalServerError)
		})
	}
	return zipHTTPHandler(app)
}

func (d *DirectoryPeerServer) opListWorkers(_ context.Context, _ *emptyIn) (*DirectoryWorkersResponse, error) {
	d.calls.Add(1)
	if d.disabled.Load() {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "%s", ErrGatewayDisabled.Error())
	}
	return &DirectoryWorkersResponse{
		PeerID:          d.PeerID,
		Operator:        d.Operator,
		ProtocolVersion: ProtocolVersion,
		Workers:         d.dir.ListWorkers(time.Now().Unix()),
	}, nil
}

func (d *DirectoryPeerServer) opRegisterWorker(_ context.Context, rec *WorkerRecord) (*DirectoryRegisterResponse, error) {
	d.calls.Add(1)
	if d.disabled.Load() {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "%s", ErrGatewayDisabled.Error())
	}
	if rec == nil {
		return nil, zip.Errorf(http.StatusBadRequest, "invalid worker record json")
	}
	if err := d.dir.RegisterWorker(*rec, time.Now().Unix()); err != nil {
		return nil, zip.Errorf(http.StatusUnprocessableEntity, "%s", err.Error())
	}
	return &DirectoryRegisterResponse{
		Registered: true,
		ProviderID: rec.ProviderID,
	}, nil
}

func (d *DirectoryPeerServer) opGossip(_ context.Context, payload *DirectoryGossipRequest) (*DirectoryGossipResponse, error) {
	d.calls.Add(1)
	if d.disabled.Load() {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "%s", ErrGatewayDisabled.Error())
	}
	if payload == nil {
		return nil, zip.Errorf(http.StatusBadRequest, "invalid gossip payload")
	}
	accepted, rejected := d.dir.MergeRecords(payload.Workers, time.Now().Unix())
	return &DirectoryGossipResponse{
		Accepted: accepted,
		Rejected: rejected,
		Total:    len(d.dir.ListWorkers(time.Now().Unix())),
	}, nil
}

func (d *DirectoryPeerServer) opSnapshot(_ context.Context, _ *emptyIn) (*DirectorySnapshot, error) {
	d.calls.Add(1)
	if d.disabled.Load() {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "%s", ErrGatewayDisabled.Error())
	}
	snap, err := d.dir.ExportSnapshot(d.kp, d.PeerID, time.Now().Unix())
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "%s", err.Error())
	}
	return &snap, nil
}
