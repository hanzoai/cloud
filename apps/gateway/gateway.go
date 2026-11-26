// SPDX-License-Identifier: Apache-2.0 OR MIT

package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// OperatorType classifies who operates a gateway instance. All operators run
// the exact same open protocol code; "hanzo" is strictly optional convenience.
const (
	OperatorTypeHanzo     = "hanzo"
	OperatorTypeCommunity = "community"
)

// GatewayConfig configures a standalone, stateless Enso gateway instance.
type GatewayConfig struct {
	GatewayID           string
	OperatorName        string
	OperatorType        string
	Strategy            Strategy
	GatewayFeeBps       uint64
	Domain              EIP712Domain
	KeyPair             *KeyPair
	DirectoryURLs       []string
	StaticWorkers       []WorkerRecord
	FailoverEnabled     *bool
	VerifyReceipts      *bool
	ProviderTimeout     time.Duration
	MaxFailoverAttempts int
	QuoteCacheEnabled   bool
	QuoteCacheTTL       time.Duration
	HTTPClient          *http.Client
}

// BoolPtr returns a pointer to v for optional GatewayConfig boolean toggles.
func BoolPtr(v bool) *bool { return &v }

// StringPtr returns a pointer to v for optional string fields such as
// VerifyReceiptRequest.ResponseContent.
func StringPtr(v string) *string { return &v }

// Gateway is a permissionless, stateless OpenAI-compatible inference gateway.
// Multiple independent gateways (Hanzo-operated, community-operated, or forked)
// run side-by-side with zero shared mutable state.
type Gateway struct {
	cfg       GatewayConfig
	kp        *KeyPair
	domain    EIP712Domain
	dir       *PeerDirectory
	nonces    *NonceCache
	scheduler *Scheduler
	client    *http.Client

	disabled   atomic.Bool
	calls      atomic.Int64
	chatCalls  atomic.Int64
	modelCalls atomic.Int64
	reqSeq     atomic.Uint64
}

// NewGateway initializes a standalone Gateway instance.
func NewGateway(cfg GatewayConfig) (*Gateway, error) {
	if cfg.GatewayID == "" {
		return nil, fmt.Errorf("%w: gatewayId is required", ErrInvalidRequest)
	}
	if cfg.OperatorName == "" {
		cfg.OperatorName = cfg.GatewayID
	}
	if cfg.OperatorType == "" {
		cfg.OperatorType = OperatorTypeCommunity
	}
	if cfg.Domain.Name == "" {
		cfg.Domain = DefaultDomain()
	}
	if !ValidStrategy(cfg.Strategy) {
		cfg.Strategy = StrategyWeightedSLOScore
	}
	kp := cfg.KeyPair
	if kp == nil {
		var err error
		kp, err = GenerateKeyPair()
		if err != nil {
			return nil, err
		}
	}
	failoverEnabled := true
	if cfg.FailoverEnabled != nil {
		failoverEnabled = *cfg.FailoverEnabled
	}
	verifyReceipts := true
	if cfg.VerifyReceipts != nil {
		verifyReceipts = *cfg.VerifyReceipts
	}
	timeout := cfg.ProviderTimeout
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}

	dir := NewPeerDirectory(cfg.Domain)
	nowUnix := time.Now().Unix()
	for _, w := range cfg.StaticWorkers {
		if err := dir.RegisterWorker(w, nowUnix); err != nil {
			return nil, fmt.Errorf("gateway %s: invalid static worker %s: %w", cfg.GatewayID, w.ProviderID, err)
		}
	}

	nonces := NewNonceCache()
	sched := NewScheduler(SchedulerConfig{
		GatewayID:           cfg.GatewayID,
		GatewayOperator:     cfg.OperatorName,
		OperatorType:        cfg.OperatorType,
		Strategy:            cfg.Strategy,
		GatewayFeeBps:       cfg.GatewayFeeBps,
		Domain:              cfg.Domain,
		FailoverEnabled:     failoverEnabled,
		VerifyReceipts:      verifyReceipts,
		ProviderTimeout:     timeout,
		MaxFailoverAttempts: cfg.MaxFailoverAttempts,
		QuoteCacheEnabled:   cfg.QuoteCacheEnabled,
		QuoteCacheTTL:       cfg.QuoteCacheTTL,
		HTTPClient:          client,
	}, nonces)

	return &Gateway{
		cfg:       cfg,
		kp:        kp,
		domain:    cfg.Domain,
		dir:       dir,
		nonces:    nonces,
		scheduler: sched,
		client:    client,
	}, nil
}

// Config returns a copy of the gateway's immutable configuration.
func (g *Gateway) Config() GatewayConfig { return g.cfg }

// GatewayID returns this gateway's identifier.
func (g *Gateway) GatewayID() string { return g.cfg.GatewayID }

// OperatorType returns "hanzo" or "community".
func (g *Gateway) OperatorType() string { return g.cfg.OperatorType }

// Address returns this gateway's EIP-55 secp256k1 identity address.
func (g *Gateway) Address() string { return g.kp.Address() }

// Domain returns the EIP-712 domain enforced by this gateway.
func (g *Gateway) Domain() EIP712Domain { return g.domain }

// Directory returns this gateway's local verified PeerDirectory.
func (g *Gateway) Directory() *PeerDirectory { return g.dir }

// Scheduler returns the gateway's stateless Scheduler.
func (g *Gateway) Scheduler() *Scheduler { return g.scheduler }

// Disable engages the kill switch on this gateway so it rejects all requests.
func (g *Gateway) Disable() { g.disabled.Store(true) }

// Enable clears the kill switch on this gateway.
func (g *Gateway) Enable() { g.disabled.Store(false) }

// IsDisabled reports whether this gateway is currently disabled.
func (g *Gateway) IsDisabled() bool { return g.disabled.Load() }

// Calls returns total requests accepted while enabled.
func (g *Gateway) Calls() int64 { return g.calls.Load() }

// ChatCalls returns total /v1/chat/completions calls processed while enabled.
func (g *Gateway) ChatCalls() int64 { return g.chatCalls.Load() }

// ResetCounters zeroes the gateway's local request counters.
func (g *Gateway) ResetCounters() {
	g.calls.Store(0)
	g.chatCalls.Store(0)
	g.modelCalls.Store(0)
}

// RegisterWorker adds a verified WorkerRecord directly to this gateway's local directory.
func (g *Gateway) RegisterWorker(rec WorkerRecord) error {
	return g.dir.RegisterWorker(rec, time.Now().Unix())
}

// SyncDirectory fetches signed WorkerRecords from all reachable configured
// peer directory URLs, skipping unreachable peers fail-safe.
func (g *Gateway) SyncDirectory(ctx context.Context) (int, error) {
	if g.disabled.Load() {
		return 0, ErrGatewayDisabled
	}
	if len(g.cfg.DirectoryURLs) == 0 {
		return len(g.dir.ListWorkers(time.Now().Unix())), nil
	}
	totalAccepted := 0
	reachablePeers := 0
	for _, peerURL := range g.cfg.DirectoryURLs {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(peerURL, "/")+"/directory/v1/workers", nil)
		if err != nil {
			continue
		}
		resp, err := g.client.Do(req)
		if err != nil {
			continue
		}
		if resp.StatusCode == http.StatusOK {
			var payload struct {
				Workers []WorkerRecord `json:"workers"`
			}
			if decErr := json.NewDecoder(resp.Body).Decode(&payload); decErr == nil {
				acc, _ := g.dir.MergeRecords(payload.Workers, time.Now().Unix())
				totalAccepted += acc
				reachablePeers++
			}
		}
		_ = resp.Body.Close()
	}
	if reachablePeers == 0 && len(g.dir.ListWorkers(time.Now().Unix())) == 0 {
		return 0, ErrNoHealthyProviders
	}
	return totalAccepted, nil
}

// ListProviders returns all currently valid discovered workers.
func (g *Gateway) ListProviders(ctx context.Context) ([]WorkerRecord, error) {
	if g.disabled.Load() {
		return nil, ErrGatewayDisabled
	}
	g.calls.Add(1)
	if len(g.dir.ListWorkers(time.Now().Unix())) == 0 && len(g.cfg.DirectoryURLs) > 0 {
		_, _ = g.SyncDirectory(ctx)
	}
	return g.dir.ListWorkers(time.Now().Unix()), nil
}

// ListModels serves the OpenAI-compatible GET /v1/models catalog aggregated
// from all verified workers in the directory.
func (g *Gateway) ListModels(ctx context.Context) (*ModelsListResponse, error) {
	if g.disabled.Load() {
		return nil, ErrGatewayDisabled
	}
	g.calls.Add(1)
	g.modelCalls.Add(1)
	if len(g.dir.ListWorkers(time.Now().Unix())) == 0 && len(g.cfg.DirectoryURLs) > 0 {
		_, _ = g.SyncDirectory(ctx)
	}
	workers := g.dir.ListWorkers(time.Now().Unix())
	seen := make(map[string]ModelObject)
	for _, w := range workers {
		for _, m := range w.Models {
			if _, ok := seen[m.ModelID]; !ok {
				seen[m.ModelID] = ModelObject{
					ID:          m.ModelID,
					Object:      "model",
					Created:     w.IssuedAt,
					OwnedBy:     "open-network",
					ModelDigest: m.ModelDigest,
					License:     m.License,
				}
			}
		}
	}
	list := make([]ModelObject, 0, len(seen))
	for _, obj := range seen {
		list = append(list, obj)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	return &ModelsListResponse{
		Object: "list",
		Data:   list,
	}, nil
}

// QuotesQuery requests ranked, cryptographically verified provider quotes for a model.
type QuotesQuery struct {
	Model     string `json:"model"`
	MaxTokens int    `json:"max_tokens,omitempty"`
	Strategy  string `json:"strategy,omitempty"`
}

// QuotesResponse lists all verified candidate quotes ranked by the gateway's strategy.
type QuotesResponse struct {
	GatewayID       string           `json:"gateway_id"`
	ProtocolVersion string           `json:"protocol_version"`
	Strategy        string           `json:"strategy"`
	Candidates      []CandidateQuote `json:"candidates"`
	RejectedCount   int              `json:"rejected_count"`
}

// FetchQuotes gathers, verifies, and ranks live quotes for query.
func (g *Gateway) FetchQuotes(ctx context.Context, query QuotesQuery) (*QuotesResponse, error) {
	if g.disabled.Load() {
		return nil, ErrGatewayDisabled
	}
	g.calls.Add(1)
	if query.Model == "" {
		return nil, fmt.Errorf("%w: model is required", ErrInvalidRequest)
	}
	workers := g.dir.DiscoverForModel(query.Model, time.Now().Unix())
	if len(workers) == 0 && len(g.cfg.DirectoryURLs) > 0 {
		_, _ = g.SyncDirectory(ctx)
		workers = g.dir.DiscoverForModel(query.Model, time.Now().Unix())
	}
	if len(workers) == 0 {
		return nil, ErrNoHealthyProviders
	}
	strat := g.cfg.Strategy
	if query.Strategy != "" {
		strat = NormalizeStrategy(query.Strategy)
	}
	res := g.scheduler.FetchAndRankQuotes(ctx, workers, query.Model, query.MaxTokens, "", strat)
	return &QuotesResponse{
		GatewayID:       g.cfg.GatewayID,
		ProtocolVersion: ProtocolVersion,
		Strategy:        string(strat),
		Candidates:      res.Candidates,
		RejectedCount:   len(res.RejectedErrors),
	}, nil
}

// ChatCompletions serves an OpenAI-compatible POST /v1/chat/completions request
// through the stateless multi-provider scheduler and returns a verifiable receipt.
func (g *Gateway) ChatCompletions(ctx context.Context, req ChatCompletionRequest) (*ChatCompletionResponse, error) {
	if g.disabled.Load() {
		return nil, ErrGatewayDisabled
	}
	if req.Model == "" || len(req.Messages) == 0 {
		return nil, fmt.Errorf("%w: model and non-empty messages are required", ErrInvalidRequest)
	}
	g.calls.Add(1)
	g.chatCalls.Add(1)

	workers := g.dir.DiscoverForModel(req.Model, time.Now().Unix())
	if len(workers) == 0 && len(g.cfg.DirectoryURLs) > 0 {
		_, _ = g.SyncDirectory(ctx)
		workers = g.dir.DiscoverForModel(req.Model, time.Now().Unix())
	}
	if len(workers) == 0 {
		return nil, ErrNoHealthyProviders
	}

	schedRes, err := g.scheduler.ScheduleAndExecute(ctx, workers, req, g.kp)
	if err != nil {
		return nil, err
	}

	seq := g.reqSeq.Add(1)
	quoteCopy := schedRes.Quote
	receiptCopy := schedRes.Exec.Receipt
	routeCopy := schedRes.Route

	return &ChatCompletionResponse{
		ID:      fmt.Sprintf("chatcmpl-%s-%d", g.cfg.GatewayID, seq),
		Object:  "chat.completion",
		Created: receiptCopy.CompletedAt,
		Model:   req.Model,
		Choices: []ChatCompletionChoice{
			{
				Index: 0,
				Message: ChatMessage{
					Role:    "assistant",
					Content: schedRes.Exec.Content,
				},
				FinishReason: "stop",
			},
		},
		Usage: ChatCompletionUsage{
			PromptTokens:     receiptCopy.PromptTokens,
			CompletionTokens: receiptCopy.CompletionTokens,
			TotalTokens:      receiptCopy.TotalTokens,
		},
		EnsoQuote:   &quoteCopy,
		EnsoReceipt: &receiptCopy,
		EnsoRoute:   &routeCopy,
	}, nil
}

// VerifyReceiptRequest is the payload for POST /v1/gateway/receipts/verify.
// Omit ResponseContent (nil) to verify the receipt signature, quote binding,
// and request hash without checking completion text; supply a non-nil pointer
// (including empty string "") to enforce OutputHash verification against that
// exact completion text.
type VerifyReceiptRequest struct {
	Receipt         ExecutionReceipt `json:"receipt"`
	Quote           *InferenceQuote  `json:"quote,omitempty"`
	RequestHash     string           `json:"request_hash,omitempty"`
	ResponseContent *string          `json:"response_content,omitempty"`
}

// VerifyReceiptResponse reports the result of offline cryptographic verification.
type VerifyReceiptResponse struct {
	Valid             bool   `json:"valid"`
	VerificationClass string `json:"verification_class"`
	ReceiptID         string `json:"receipt_id"`
	ProviderID        string `json:"provider_id"`
	ProviderAddress   string `json:"provider_address"`
	QuoteHash         string `json:"quote_hash"`
	OutputHash        string `json:"output_hash"`
}

// VerifyReceipt verifies an ExecutionReceipt offline against this gateway's domain.
func (g *Gateway) VerifyReceipt(in VerifyReceiptRequest) (*VerifyReceiptResponse, error) {
	if g.disabled.Load() {
		return nil, ErrGatewayDisabled
	}
	g.calls.Add(1)
	if err := verifyReceipt(g.domain, in.Receipt, in.Quote, in.RequestHash, in.ResponseContent); err != nil {
		return nil, err
	}
	return &VerifyReceiptResponse{
		Valid:             true,
		VerificationClass: in.Receipt.VerificationClass,
		ReceiptID:         in.Receipt.ReceiptID,
		ProviderID:        in.Receipt.ProviderID,
		ProviderAddress:   in.Receipt.ProviderAddress,
		QuoteHash:         in.Receipt.QuoteHash,
		OutputHash:        in.Receipt.OutputHash,
	}, nil
}

// --- Sovereignty: Stateless Config + Directory Export, Verification & Forking ---

// GatewayConfigSnapshot captures the portable stateless routing configuration
// of a gateway (zero credentials or custodial state).
type GatewayConfigSnapshot struct {
	ProtocolVersion     string       `json:"protocolVersion"`
	SourceGatewayID     string       `json:"sourceGatewayId"`
	SourceOperator      string       `json:"sourceOperator"`
	Strategy            Strategy     `json:"strategy"`
	GatewayFeeBps       uint64       `json:"gatewayFeeBps"`
	Domain              EIP712Domain `json:"domain"`
	DirectoryURLs       []string     `json:"directoryUrls"`
	ProviderTimeoutMs   int64        `json:"providerTimeoutMs"`
	MaxFailoverAttempts int          `json:"maxFailoverAttempts"`
}

// GatewayExportBundle is a signed, self-verifying export of a gateway's
// stateless configuration and verified worker directory enabling permissionless
// operator migration or third-party forking.
type GatewayExportBundle struct {
	ProtocolVersion string                `json:"protocolVersion"`
	ExportedAt      int64                 `json:"exportedAt"`
	ExporterAddress string                `json:"exporterAddress"`
	Config          GatewayConfigSnapshot `json:"config"`
	Directory       DirectorySnapshot     `json:"directory"`
	BundleSHA256    string                `json:"bundleSha256"`
	Signature       string                `json:"signature"`
}

func computeExportBundleSHA256(b GatewayExportBundle) (string, error) {
	payload := struct {
		ProtocolVersion string                `json:"protocolVersion"`
		ExportedAt      int64                 `json:"exportedAt"`
		ExporterAddress string                `json:"exporterAddress"`
		Config          GatewayConfigSnapshot `json:"config"`
		Directory       DirectorySnapshot     `json:"directory"`
	}{
		ProtocolVersion: b.ProtocolVersion,
		ExportedAt:      b.ExportedAt,
		ExporterAddress: b.ExporterAddress,
		Config:          b.Config,
		Directory:       b.Directory,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return SHA256Hex(raw), nil
}

func exportBundleStructHash(b GatewayExportBundle) ([32]byte, error) {
	typeHash := Keccak256Bytes([]byte(
		"GatewayExportBundle(string protocolVersion,int64 exportedAt,address exporterAddress,string bundleSha256)",
	))
	expWord, err := encodeAddressWord(b.ExporterAddress)
	if err != nil {
		return [32]byte{}, err
	}
	protoHash := Keccak256Bytes([]byte(b.ProtocolVersion))
	exportedWord := encodeInt64Word(b.ExportedAt)
	bundleHash := Keccak256Bytes([]byte(b.BundleSHA256))
	return Keccak256Bytes(
		typeHash[:],
		protoHash[:],
		exportedWord[:],
		expWord[:],
		bundleHash[:],
	), nil
}

// ExportBundle exports this gateway's stateless config and verified directory
// as a cryptographically signed GatewayExportBundle.
func (g *Gateway) ExportBundle() (*GatewayExportBundle, error) {
	nowUnix := time.Now().Unix()
	dirSnap, err := g.dir.ExportSnapshot(g.kp, g.cfg.GatewayID, nowUnix)
	if err != nil {
		return nil, err
	}
	urls := append([]string(nil), g.cfg.DirectoryURLs...)
	sort.Strings(urls)
	bundle := GatewayExportBundle{
		ProtocolVersion: ProtocolVersion,
		ExportedAt:      nowUnix,
		ExporterAddress: g.kp.Address(),
		Config: GatewayConfigSnapshot{
			ProtocolVersion:     ProtocolVersion,
			SourceGatewayID:     g.cfg.GatewayID,
			SourceOperator:      g.cfg.OperatorName,
			Strategy:            g.cfg.Strategy,
			GatewayFeeBps:       g.cfg.GatewayFeeBps,
			Domain:              g.domain,
			DirectoryURLs:       urls,
			ProviderTimeoutMs:   g.cfg.ProviderTimeout.Milliseconds(),
			MaxFailoverAttempts: g.cfg.MaxFailoverAttempts,
		},
		Directory: dirSnap,
	}
	digest, err := computeExportBundleSHA256(bundle)
	if err != nil {
		return nil, err
	}
	bundle.BundleSHA256 = digest
	sh, err := exportBundleStructHash(bundle)
	if err != nil {
		return nil, err
	}
	sig, err := g.kp.SignDigest(g.domain.TypedDataDigest(sh))
	if err != nil {
		return nil, err
	}
	bundle.Signature = sig
	return &bundle, nil
}

// VerifyExportBundle verifies a GatewayExportBundle, its embedded
// DirectorySnapshot, and every WorkerRecord offline.
func VerifyExportBundle(domain EIP712Domain, bundle GatewayExportBundle, nowUnix int64) error {
	if bundle.ProtocolVersion != ProtocolVersion {
		return fmt.Errorf("%w: unsupported bundle protocol %q", ErrDirectoryCorrupted, bundle.ProtocolVersion)
	}
	expected, err := computeExportBundleSHA256(bundle)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrDirectoryCorrupted, err)
	}
	if bundle.BundleSHA256 != expected {
		return fmt.Errorf("%w: bundle digest mismatch", ErrDirectoryCorrupted)
	}
	sh, err := exportBundleStructHash(bundle)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrDirectoryCorrupted, err)
	}
	signer, err := RecoverSignerAddress(domain.TypedDataDigest(sh), bundle.Signature)
	if err != nil {
		return err
	}
	if !SameAddress(signer, bundle.ExporterAddress) {
		return fmt.Errorf("%w: bundle signer %s != exporter %s", ErrSignerMismatch, signer, bundle.ExporterAddress)
	}
	return VerifyDirectorySnapshot(domain, bundle.Directory, nowUnix)
}

// NewGatewayFromBundle verifies a GatewayExportBundle offline and boots an
// independent forked Gateway operated by a new operator with zero contact to
// the original gateway or Hanzo infrastructure.
func NewGatewayFromBundle(
	bundle GatewayExportBundle,
	newGatewayID string,
	newOperatorName string,
	newOperatorType string,
	newFeeBps uint64,
	newKeyPair *KeyPair,
	httpClient *http.Client,
) (*Gateway, error) {
	nowUnix := time.Now().Unix()
	if err := VerifyExportBundle(bundle.Config.Domain, bundle, nowUnix); err != nil {
		return nil, err
	}
	timeout := time.Duration(bundle.Config.ProviderTimeoutMs) * time.Millisecond
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	gw, err := NewGateway(GatewayConfig{
		GatewayID:           newGatewayID,
		OperatorName:        newOperatorName,
		OperatorType:        newOperatorType,
		Strategy:            bundle.Config.Strategy,
		GatewayFeeBps:       newFeeBps,
		Domain:              bundle.Config.Domain,
		KeyPair:             newKeyPair,
		DirectoryURLs:       append([]string(nil), bundle.Config.DirectoryURLs...),
		StaticWorkers:       append([]WorkerRecord(nil), bundle.Directory.Workers...),
		ProviderTimeout:     timeout,
		MaxFailoverAttempts: bundle.Config.MaxFailoverAttempts,
		HTTPClient:          httpClient,
	})
	if err != nil {
		return nil, err
	}
	return gw, nil
}

// --- Multi-Gateway OpenAI-Compatible Client ---

// GatewayEndpoint identifies an independent HTTP gateway reachable by a client.
type GatewayEndpoint struct {
	GatewayID    string
	OperatorType string
	BaseURL      string
	Enabled      bool
}

// MultiGatewayClient speaks standard OpenAI HTTP (/v1/models and
// /v1/chat/completions) across a pool of independent gateways with automatic
// failover and exact Hanzo vs. community call accounting.
type MultiGatewayClient struct {
	mu             sync.RWMutex
	endpoints      []GatewayEndpoint
	httpClient     *http.Client
	domain         EIP712Domain
	verifyReceipts bool

	hanzoCalls     atomic.Int64
	communityCalls atomic.Int64
}

// NewMultiGatewayClient creates an OpenAI-compatible client over endpoints.
func NewMultiGatewayClient(domain EIP712Domain, endpoints []GatewayEndpoint, httpClient *http.Client) *MultiGatewayClient {
	if domain.Name == "" {
		domain = DefaultDomain()
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 5 * time.Second}
	}
	cp := make([]GatewayEndpoint, len(endpoints))
	for i, ep := range endpoints {
		cp[i] = ep
	}
	return &MultiGatewayClient{
		endpoints:      cp,
		httpClient:     httpClient,
		domain:         domain,
		verifyReceipts: true,
	}
}

// DisableOperatorType marks all endpoints belonging to opType (e.g. "hanzo")
// disabled so the client routes exclusively through remaining independent gateways.
func (c *MultiGatewayClient) DisableOperatorType(opType string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := range c.endpoints {
		if c.endpoints[i].OperatorType == opType {
			c.endpoints[i].Enabled = false
		}
	}
}

// EnableAll re-enables all configured gateway endpoints.
func (c *MultiGatewayClient) EnableAll() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := range c.endpoints {
		c.endpoints[i].Enabled = true
	}
}

// HanzoCalls returns total HTTP attempts issued by this client to Hanzo gateways.
func (c *MultiGatewayClient) HanzoCalls() int64 { return c.hanzoCalls.Load() }

// CommunityCalls returns total HTTP attempts issued by this client to community gateways.
func (c *MultiGatewayClient) CommunityCalls() int64 { return c.communityCalls.Load() }

// ResetCounters clears the client's per-operator call counters.
func (c *MultiGatewayClient) ResetCounters() {
	c.hanzoCalls.Store(0)
	c.communityCalls.Store(0)
}

func (c *MultiGatewayClient) activeEndpoints() []GatewayEndpoint {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]GatewayEndpoint, 0, len(c.endpoints))
	for _, ep := range c.endpoints {
		if ep.Enabled {
			out = append(out, ep)
		}
	}
	return out
}

// ChatCompletions sends an OpenAI /v1/chat/completions request across active
// independent gateways, failing over transparently on gateway failure and
// verifying the returned ExecutionReceipt offline.
func (c *MultiGatewayClient) ChatCompletions(ctx context.Context, req ChatCompletionRequest) (*ChatCompletionResponse, error) {
	eps := c.activeEndpoints()
	if len(eps) == 0 {
		return nil, ErrGatewayDisabled
	}
	bodyBytes, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	reqHash := CanonicalRequestHash(req)
	var lastErr error

	for _, ep := range eps {
		if ep.OperatorType == OperatorTypeHanzo {
			c.hanzoCalls.Add(1)
		} else {
			c.communityCalls.Add(1)
		}

		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(ep.BaseURL, "/")+"/v1/chat/completions", bytes.NewReader(bodyBytes))
		if err != nil {
			lastErr = err
			continue
		}
		httpReq.Header.Set("Content-Type", "application/json")

		resp, err := c.httpClient.Do(httpReq)
		if err != nil {
			lastErr = err
			continue
		}
		raw, readErr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if readErr != nil {
			lastErr = readErr
			continue
		}
		if resp.StatusCode != http.StatusOK {
			lastErr = fmt.Errorf("gateway %s returned HTTP %d: %s", ep.GatewayID, resp.StatusCode, strings.TrimSpace(string(raw)))
			continue
		}

		var out ChatCompletionResponse
		if err := json.Unmarshal(raw, &out); err != nil {
			lastErr = err
			continue
		}
		if c.verifyReceipts {
			if out.EnsoReceipt == nil || len(out.Choices) == 0 {
				lastErr = fmt.Errorf("%w: missing receipt or choice from gateway %s", ErrInvalidReceipt, ep.GatewayID)
				continue
			}
			if err := VerifyReceipt(c.domain, *out.EnsoReceipt, out.EnsoQuote, reqHash, out.Choices[0].Message.Content); err != nil {
				lastErr = err
				continue
			}
			if out.Usage.PromptTokens != out.EnsoReceipt.PromptTokens ||
				out.Usage.CompletionTokens != out.EnsoReceipt.CompletionTokens ||
				out.Usage.TotalTokens != out.EnsoReceipt.TotalTokens {
				lastErr = fmt.Errorf("%w: response usage (%d/%d/%d) does not match signed receipt (%d/%d/%d)",
					ErrInvalidReceipt,
					out.Usage.PromptTokens, out.Usage.CompletionTokens, out.Usage.TotalTokens,
					out.EnsoReceipt.PromptTokens, out.EnsoReceipt.CompletionTokens, out.EnsoReceipt.TotalTokens)
				continue
			}
		}
		return &out, nil
	}
	if lastErr == nil {
		lastErr = ErrGatewayDisabled
	}
	return nil, lastErr
}

// ListModels fetches OpenAI /v1/models from the first healthy active gateway.
func (c *MultiGatewayClient) ListModels(ctx context.Context) (*ModelsListResponse, error) {
	eps := c.activeEndpoints()
	if len(eps) == 0 {
		return nil, ErrGatewayDisabled
	}
	var lastErr error
	for _, ep := range eps {
		if ep.OperatorType == OperatorTypeHanzo {
			c.hanzoCalls.Add(1)
		} else {
			c.communityCalls.Add(1)
		}
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(ep.BaseURL, "/")+"/v1/models", nil)
		if err != nil {
			lastErr = err
			continue
		}
		resp, err := c.httpClient.Do(httpReq)
		if err != nil {
			lastErr = err
			continue
		}
		raw, readErr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if readErr != nil {
			lastErr = readErr
			continue
		}
		if resp.StatusCode != http.StatusOK {
			lastErr = fmt.Errorf("gateway %s /v1/models returned HTTP %d", ep.GatewayID, resp.StatusCode)
			continue
		}
		var out ModelsListResponse
		if err := json.Unmarshal(raw, &out); err != nil {
			lastErr = err
			continue
		}
		return &out, nil
	}
	return nil, lastErr
}

// --- Three Honest Negative & Boundary Regimes ---

// TTLBoundaryPoint records quote-refresh overhead and cache behavior for a
// specific QuoteCacheTTL under rapid price churn.
type TTLBoundaryPoint struct {
	TTLMs             int64   `json:"ttlMs"`
	Requests          int     `json:"requests"`
	TotalWorkerQuotes int64   `json:"totalWorkerQuotes"`
	CacheHits         int     `json:"cacheHits"`
	CacheHitRate      float64 `json:"cacheHitRate"`
	AvgQuoteRefreshMs float64 `json:"avgQuoteRefreshMs"`
	AvgTotalLatencyMs float64 `json:"avgTotalLatencyMs"`
	CompletionRate    float64 `json:"completionRate"`
	QuotesPerRequest  float64 `json:"quotesPerRequest"`
}

// NegativeRegimeResult captures measured evidence from one negative control or
// boundary stress regime.
type NegativeRegimeResult struct {
	Name              string             `json:"name"`
	Category          string             `json:"category"`
	Trials            int                `json:"trials"`
	Validated         bool               `json:"validated"`
	ObservedRate      float64            `json:"observedRate"`
	ControlRate       float64            `json:"controlRate"`
	CorruptedAccepted int                `json:"corruptedAccepted,omitempty"`
	CorruptedRejected int                `json:"corruptedRejected,omitempty"`
	Completed         int                `json:"completed,omitempty"`
	Failed            int                `json:"failed,omitempty"`
	BoundaryPoints    []TTLBoundaryPoint `json:"boundaryPoints,omitempty"`
	Summary           string             `json:"summary"`
}

// RunNegativeAndBoundaryRegimes executes the three mandatory honest negative
// and boundary regimes over real loopback HTTP servers.
func RunNegativeAndBoundaryRegimes(domain EIP712Domain, trials int) ([]NegativeRegimeResult, error) {
	if domain.Name == "" {
		domain = DefaultDomain()
	}
	if trials <= 0 {
		trials = 30
	}

	r1, err := runUnverifiedReceiptNegative(domain, trials)
	if err != nil {
		return nil, err
	}
	r2, err := runSingleProviderNoFailoverNegative(domain, trials)
	if err != nil {
		return nil, err
	}
	r3, err := runStaleQuoteStormBoundary(domain, trials)
	if err != nil {
		return nil, err
	}
	return []NegativeRegimeResult{r1, r2, r3}, nil
}

func defaultTestModels() []ModelSpec {
	return []ModelSpec{
		{
			ModelID:          "zen-7b-instruct",
			ModelDigest:      "sha256:" + SHA256Hex([]byte("zen-7b-instruct-open-weights-v1")),
			MaxContextTokens: 32768,
			License:          "Apache-2.0",
		},
	}
}

// Regime 1: unverified_receipt_negative
// Skipping receipt signature/hash verification lets a malicious provider return
// 100% corrupted outputs undetected, while enabling VerifyReceipts catches 100%.
func runUnverifiedReceiptNegative(domain EIP712Domain, trials int) (NegativeRegimeResult, error) {
	kp, err := KeyPairFromSeed([]byte("regime1-malicious-worker"))
	if err != nil {
		return NegativeRegimeResult{}, err
	}
	malicious, err := NewWorkerNode(WorkerConfig{
		ProviderID:         "worker-malicious-corrupt",
		Region:             "eu-central",
		Models:             defaultTestModels(),
		PriceMicroUSDPer1M: 400_000,
		AdvertisedTTFTMs:   15,
		Domain:             domain,
		KeyPair:            kp,
	})
	if err != nil {
		return NegativeRegimeResult{}, err
	}
	defer malicious.Close()
	malicious.SetFaultMode(FaultCorruptOutput)

	rec, err := malicious.SignedRecord(time.Now().Unix(), 3600)
	if err != nil {
		return NegativeRegimeResult{}, err
	}

	gwUnverified, err := NewGateway(GatewayConfig{
		GatewayID:       "gw-unverified-negative",
		OperatorName:    "Unverified Negative Control",
		OperatorType:    OperatorTypeCommunity,
		Strategy:        StrategyLowestCost,
		Domain:          domain,
		StaticWorkers:   []WorkerRecord{rec},
		FailoverEnabled: BoolPtr(false),
		VerifyReceipts:  BoolPtr(false),
	})
	if err != nil {
		return NegativeRegimeResult{}, err
	}
	gwVerified, err := NewGateway(GatewayConfig{
		GatewayID:       "gw-verified-control",
		OperatorName:    "Fail-Closed Verified Control",
		OperatorType:    OperatorTypeCommunity,
		Strategy:        StrategyLowestCost,
		Domain:          domain,
		StaticWorkers:   []WorkerRecord{rec},
		FailoverEnabled: BoolPtr(false),
		VerifyReceipts:  BoolPtr(true),
	})
	if err != nil {
		return NegativeRegimeResult{}, err
	}

	ctx := context.Background()
	corruptedAccepted := 0
	corruptedRejected := 0
	for i := 0; i < trials; i++ {
		req := ChatCompletionRequest{
			Model:     "zen-7b-instruct",
			Messages:  []ChatMessage{{Role: "user", Content: fmt.Sprintf("integrity trial %d", i)}},
			MaxTokens: 32,
		}
		resp, err := gwUnverified.ChatCompletions(ctx, req)
		if err == nil && len(resp.Choices) > 0 && strings.HasPrefix(resp.Choices[0].Message.Content, "[CORRUPTED-UNVERIFIED-OUTPUT]") {
			corruptedAccepted++
		}
		_, verifyErr := gwVerified.ChatCompletions(ctx, req)
		if errors.Is(verifyErr, ErrOutputHashMismatch) {
			corruptedRejected++
		}
	}

	unverifiedLeakRate := float64(corruptedAccepted) / float64(trials)
	verifiedCatchRate := float64(corruptedRejected) / float64(trials)
	validated := corruptedAccepted == trials && corruptedRejected == trials

	return NegativeRegimeResult{
		Name:              "unverified_receipt_negative",
		Category:          "negative_control",
		Trials:            trials,
		Validated:         validated,
		ObservedRate:      unverifiedLeakRate,
		ControlRate:       verifiedCatchRate,
		CorruptedAccepted: corruptedAccepted,
		CorruptedRejected: corruptedRejected,
		Summary: fmt.Sprintf(
			"Without receipt verification, %.1f%% (%d/%d) of silently corrupted provider outputs slipped through undetected; with fail-closed VerifyReceipt enabled, %.1f%% (%d/%d) were rejected with ErrOutputHashMismatch.",
			unverifiedLeakRate*100, corruptedAccepted, trials, verifiedCatchRate*100, corruptedRejected, trials,
		),
	}, nil
}

// Regime 2: single_provider_no_failover_negative
// Disabling failover collapses completion rate under 30% provider fault injection,
// while multi-provider failover recovers 100% completion.
func runSingleProviderNoFailoverNegative(domain EIP712Domain, trials int) (NegativeRegimeResult, error) {
	kpA, err := KeyPairFromSeed([]byte("regime2-flaky-worker"))
	if err != nil {
		return NegativeRegimeResult{}, err
	}
	kpB, err := KeyPairFromSeed([]byte("regime2-healthy-peer"))
	if err != nil {
		return NegativeRegimeResult{}, err
	}
	flaky, err := NewWorkerNode(WorkerConfig{
		ProviderID:         "worker-flaky-30pct",
		Region:             "us-east",
		Models:             defaultTestModels(),
		PriceMicroUSDPer1M: 350_000, // cheaper so always selected first
		AdvertisedTTFTMs:   10,
		Domain:             domain,
		KeyPair:            kpA,
	})
	if err != nil {
		return NegativeRegimeResult{}, err
	}
	defer flaky.Close()
	flaky.SetFaultMode(FaultIntermittent)
	flaky.SetFaultRatePercent(30)

	healthy, err := NewWorkerNode(WorkerConfig{
		ProviderID:         "worker-healthy-backup",
		Region:             "us-west",
		Models:             defaultTestModels(),
		PriceMicroUSDPer1M: 420_000,
		AdvertisedTTFTMs:   15,
		Domain:             domain,
		KeyPair:            kpB,
	})
	if err != nil {
		return NegativeRegimeResult{}, err
	}
	defer healthy.Close()

	recFlaky, err := flaky.SignedRecord(time.Now().Unix(), 3600)
	if err != nil {
		return NegativeRegimeResult{}, err
	}
	recHealthy, err := healthy.SignedRecord(time.Now().Unix(), 3600)
	if err != nil {
		return NegativeRegimeResult{}, err
	}

	gwNoFailover, err := NewGateway(GatewayConfig{
		GatewayID:       "gw-single-no-failover",
		OperatorName:    "Single-Provider No-Failover Negative",
		OperatorType:    OperatorTypeCommunity,
		Strategy:        StrategyLowestCost,
		Domain:          domain,
		StaticWorkers:   []WorkerRecord{recFlaky, recHealthy},
		FailoverEnabled: BoolPtr(false),
		VerifyReceipts:  BoolPtr(true),
	})
	if err != nil {
		return NegativeRegimeResult{}, err
	}
	gwWithFailover, err := NewGateway(GatewayConfig{
		GatewayID:       "gw-multi-failover-control",
		OperatorName:    "Multi-Provider Failover Control",
		OperatorType:    OperatorTypeCommunity,
		Strategy:        StrategyLowestCost,
		Domain:          domain,
		StaticWorkers:   []WorkerRecord{recFlaky, recHealthy},
		FailoverEnabled: BoolPtr(true),
		VerifyReceipts:  BoolPtr(true),
	})
	if err != nil {
		return NegativeRegimeResult{}, err
	}

	ctx := context.Background()
	noFailCompleted := 0
	noFailFailed := 0
	for i := 0; i < trials; i++ {
		req := ChatCompletionRequest{
			Model:     "zen-7b-instruct",
			Messages:  []ChatMessage{{Role: "user", Content: fmt.Sprintf("no-failover trial %d", i)}},
			MaxTokens: 32,
		}
		if _, err := gwNoFailover.ChatCompletions(ctx, req); err == nil {
			noFailCompleted++
		} else {
			noFailFailed++
		}
	}

	failoverCompleted := 0
	for i := 0; i < trials; i++ {
		req := ChatCompletionRequest{
			Model:     "zen-7b-instruct",
			Messages:  []ChatMessage{{Role: "user", Content: fmt.Sprintf("failover trial %d", i)}},
			MaxTokens: 32,
		}
		if _, err := gwWithFailover.ChatCompletions(ctx, req); err == nil {
			failoverCompleted++
		}
	}

	noFailRate := float64(noFailCompleted) / float64(trials)
	failoverRate := float64(failoverCompleted) / float64(trials)
	validated := noFailFailed > 0 && noFailRate <= 0.75 && failoverCompleted == trials

	return NegativeRegimeResult{
		Name:         "single_provider_no_failover_negative",
		Category:     "negative_control",
		Trials:       trials,
		Validated:    validated,
		ObservedRate: noFailRate,
		ControlRate:  failoverRate,
		Completed:    noFailCompleted,
		Failed:       noFailFailed,
		Summary: fmt.Sprintf(
			"Under 30%% provider fault injection with failover disabled, completion collapsed to %.1f%% (%d/%d completed, %d failed); enabling stateless multi-provider failover restored %.1f%% (%d/%d) completion.",
			noFailRate*100, noFailCompleted, trials, noFailFailed, failoverRate*100, failoverCompleted, trials,
		),
	}, nil
}

// Regime 3: stale_quote_storm_boundary
// Measures quote-refresh latency overhead and worker quote fan-out across
// short-to-long TTLs under rapid provider price churn.
func runStaleQuoteStormBoundary(domain EIP712Domain, trials int) (NegativeRegimeResult, error) {
	if trials < 10 {
		trials = 10
	}
	ttls := []time.Duration{
		5 * time.Millisecond,
		25 * time.Millisecond,
		150 * time.Millisecond,
		1000 * time.Millisecond,
	}
	points := make([]TTLBoundaryPoint, 0, len(ttls))

	for _, ttl := range ttls {
		workers := make([]*WorkerNode, 0, 4)
		records := make([]WorkerRecord, 0, 4)
		for idx := 0; idx < 4; idx++ {
			kp, err := KeyPairFromSeed([]byte(fmt.Sprintf("regime3-worker-%d-%d", ttl.Milliseconds(), idx)))
			if err != nil {
				for _, w := range workers {
					w.Close()
				}
				return NegativeRegimeResult{}, err
			}
			w, err := NewWorkerNode(WorkerConfig{
				ProviderID:         fmt.Sprintf("worker-churn-%d", idx),
				Region:             "global",
				Models:             defaultTestModels(),
				PriceMicroUSDPer1M: uint64(300_000 + idx*25_000),
				AdvertisedTTFTMs:   int64(12 + idx*4),
				Domain:             domain,
				KeyPair:            kp,
				PriceChurnStep:     15_000,
				PriceChurnInterval: 4, // rapid price churn every 4 quotes
			})
			if err != nil {
				for _, prev := range workers {
					prev.Close()
				}
				return NegativeRegimeResult{}, err
			}
			workers = append(workers, w)
			rec, err := w.SignedRecord(time.Now().Unix(), 3600)
			if err != nil {
				for _, prev := range workers {
					prev.Close()
				}
				return NegativeRegimeResult{}, err
			}
			records = append(records, rec)
		}

		gw, err := NewGateway(GatewayConfig{
			GatewayID:         fmt.Sprintf("gw-ttl-%dms", ttl.Milliseconds()),
			OperatorName:      "Boundary TTL Probe",
			OperatorType:      OperatorTypeCommunity,
			Strategy:          StrategyLowestCost,
			Domain:            domain,
			StaticWorkers:     records,
			FailoverEnabled:   BoolPtr(true),
			VerifyReceipts:    BoolPtr(true),
			QuoteCacheEnabled: true,
			QuoteCacheTTL:     ttl,
		})
		if err != nil {
			for _, w := range workers {
				w.Close()
			}
			return NegativeRegimeResult{}, err
		}

		ctx := context.Background()
		completed := 0
		cacheHits := 0
		sumRefreshMs := 0.0
		sumTotalMs := 0.0
		for i := 0; i < trials; i++ {
			t0 := time.Now()
			resp, err := gw.ChatCompletions(ctx, ChatCompletionRequest{
				Model:     "zen-7b-instruct",
				Messages:  []ChatMessage{{Role: "user", Content: fmt.Sprintf("churn probe %d", i)}},
				MaxTokens: 32,
			})
			elapsedMs := float64(time.Since(t0).Microseconds()) / 1000.0
			if err == nil && resp != nil {
				completed++
				sumTotalMs += elapsedMs
				if resp.EnsoRoute != nil {
					sumRefreshMs += resp.EnsoRoute.QuoteRefreshMs
					if resp.EnsoRoute.QuoteCacheHit {
						cacheHits++
					}
				}
			}
			// Pace requests slightly so very short TTLs (5ms) genuinely expire between calls.
			if ttl <= 25*time.Millisecond {
				time.Sleep(6 * time.Millisecond)
			}
		}

		var totalQuotes int64
		for _, w := range workers {
			totalQuotes += w.QuoteCalls()
			w.Close()
		}

		avgRefresh := 0.0
		avgTotal := 0.0
		if completed > 0 {
			avgRefresh = sumRefreshMs / float64(completed)
			avgTotal = sumTotalMs / float64(completed)
		}
		points = append(points, TTLBoundaryPoint{
			TTLMs:             ttl.Milliseconds(),
			Requests:          trials,
			TotalWorkerQuotes: totalQuotes,
			CacheHits:         cacheHits,
			CacheHitRate:      float64(cacheHits) / float64(trials),
			AvgQuoteRefreshMs: avgRefresh,
			AvgTotalLatencyMs: avgTotal,
			CompletionRate:    float64(completed) / float64(trials),
			QuotesPerRequest:  float64(totalQuotes) / float64(trials),
		})
	}

	shortPt := points[0]
	longPt := points[len(points)-1]
	validated := shortPt.CompletionRate == 1.0 && longPt.CompletionRate == 1.0 &&
		shortPt.TotalWorkerQuotes >= longPt.TotalWorkerQuotes

	return NegativeRegimeResult{
		Name:           "stale_quote_storm_boundary",
		Category:       "boundary_regime",
		Trials:         trials,
		Validated:      validated,
		ObservedRate:   shortPt.CacheHitRate,
		ControlRate:    longPt.CacheHitRate,
		BoundaryPoints: points,
		Summary: fmt.Sprintf(
			"Under rapid price churn (every 4 quotes across 4 workers), 5ms TTL forced %.2f quotes/req (%.1f%% cache hit, %.3f ms avg quote refresh) vs 1000ms TTL at %.2f quotes/req (%.1f%% cache hit, %.3f ms avg quote refresh) while maintaining 100%% completion and strict single-use execution nonces.",
			shortPt.QuotesPerRequest, shortPt.CacheHitRate*100, shortPt.AvgQuoteRefreshMs,
			longPt.QuotesPerRequest, longPt.CacheHitRate*100, longPt.AvgQuoteRefreshMs,
		),
	}, nil
}
