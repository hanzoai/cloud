// SPDX-License-Identifier: Apache-2.0 OR MIT

package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/bits"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// Strategy selects the stateless multi-provider scheduling policy.
type Strategy string

const (
	StrategyLowestCost        Strategy = "lowest_cost"
	StrategyLowestTTFT        Strategy = "lowest_ttft"
	StrategyWeightedSLOScore  Strategy = "weighted_slo_score"
	StrategyFailoverResilient Strategy = "failover_resilient"
)

// ValidStrategy reports whether s is a supported stateless scheduling strategy.
func ValidStrategy(s Strategy) bool {
	switch s {
	case StrategyLowestCost, StrategyLowestTTFT, StrategyWeightedSLOScore, StrategyFailoverResilient:
		return true
	default:
		return false
	}
}

// NormalizeStrategy returns s if valid or StrategyWeightedSLOScore as default.
func NormalizeStrategy(s string) Strategy {
	cand := Strategy(strings.TrimSpace(strings.ToLower(s)))
	if ValidStrategy(cand) {
		return cand
	}
	return StrategyWeightedSLOScore
}

// ApplyGatewayFeeChecked computes basePriceMicroUSD plus the gateway's
// competitive routing fee in basis points (100 bps = 1%) using exact 128-bit
// arithmetic, returning ErrInvalidQuote if the fee-inclusive price overflows
// uint64.
func ApplyGatewayFeeChecked(basePriceMicroUSD uint64, feeBps uint64) (uint64, error) {
	if feeBps == 0 || basePriceMicroUSD == 0 {
		return basePriceMicroUSD, nil
	}
	hi, lo := bits.Mul64(basePriceMicroUSD, feeBps)
	lo, carry := bits.Add64(lo, 9999, 0)
	hi += carry
	if hi >= 10000 {
		return 0, fmt.Errorf("%w: fee-inclusive price overflows uint64", ErrInvalidQuote)
	}
	fee, _ := bits.Div64(hi, lo, 10000)
	total, overflow := bits.Add64(basePriceMicroUSD, fee, 0)
	if overflow != 0 {
		return 0, fmt.Errorf("%w: fee-inclusive price overflows uint64", ErrInvalidQuote)
	}
	return total, nil
}

// ApplyGatewayFee returns basePriceMicroUSD plus the gateway's competitive
// routing fee in basis points (100 bps = 1%), saturating at math.MaxUint64 if
// the result would overflow uint64.
func ApplyGatewayFee(basePriceMicroUSD uint64, feeBps uint64) uint64 {
	total, err := ApplyGatewayFeeChecked(basePriceMicroUSD, feeBps)
	if err != nil {
		return math.MaxUint64
	}
	return total
}

// ProviderHealth tracks strictly ephemeral local observations on a single
// gateway instance. It is never shared across gateways; a newly started or
// forked gateway schedules immediately with an empty health table.
type ProviderHealth struct {
	ConsecutiveFailures int
	TotalSuccesses      int64
	TotalFailures       int64
	ObservedTTFTMs      float64
	LastFailureAt       time.Time
}

// CandidateQuote pairs a discovered worker with its cryptographically verified
// InferenceQuote and stateless scheduler score.
type CandidateQuote struct {
	Worker                      WorkerRecord   `json:"worker"`
	Quote                       InferenceQuote `json:"quote"`
	QuoteHash                   string         `json:"quoteHash"`
	EffectivePriceMicroUSDPer1M uint64         `json:"effectivePriceMicroUsdPer1m"`
	Score                       float64        `json:"score"`
}

// RankCandidates deterministically ranks verified provider quotes according to
// strategy. It is a pure function with zero shared mutable state.
func RankCandidates(strategy Strategy, candidates []CandidateQuote, health map[string]ProviderHealth) []CandidateQuote {
	ranked := append([]CandidateQuote(nil), candidates...)
	if len(ranked) == 0 {
		return ranked
	}

	minPrice := ranked[0].EffectivePriceMicroUSDPer1M
	maxPrice := ranked[0].EffectivePriceMicroUSDPer1M
	minTTFT := float64(ranked[0].Quote.MaxTTFTMs)
	maxTTFT := float64(ranked[0].Quote.MaxTTFTMs)
	for _, c := range ranked[1:] {
		if c.EffectivePriceMicroUSDPer1M < minPrice {
			minPrice = c.EffectivePriceMicroUSDPer1M
		}
		if c.EffectivePriceMicroUSDPer1M > maxPrice {
			maxPrice = c.EffectivePriceMicroUSDPer1M
		}
		t := float64(c.Quote.MaxTTFTMs)
		if t < minTTFT {
			minTTFT = t
		}
		if t > maxTTFT {
			maxTTFT = t
		}
	}

	priceSpan := float64(maxPrice - minPrice)
	if priceSpan < 1 {
		priceSpan = 1
	}
	ttftSpan := maxTTFT - minTTFT
	if ttftSpan < 1 {
		ttftSpan = 1
	}

	for i := range ranked {
		normPrice := float64(ranked[i].EffectivePriceMicroUSDPer1M-minPrice) / priceSpan
		normTTFT := (float64(ranked[i].Quote.MaxTTFTMs) - minTTFT) / ttftSpan
		h := health[ranked[i].Worker.ProviderID]
		healthPenalty := math.Min(1.0, float64(h.ConsecutiveFailures)*0.5)
		if h.ObservedTTFTMs > 0 {
			obsNorm := math.Min(1.0, h.ObservedTTFTMs/math.Max(100.0, maxTTFT))
			normTTFT = 0.5*normTTFT + 0.5*obsNorm
		}
		switch strategy {
		case StrategyLowestCost:
			ranked[i].Score = float64(ranked[i].EffectivePriceMicroUSDPer1M)
		case StrategyLowestTTFT:
			ranked[i].Score = float64(ranked[i].Quote.MaxTTFTMs)
		case StrategyFailoverResilient:
			// Strongly prioritize zero-failure providers, then balanced SLO.
			ranked[i].Score = float64(h.ConsecutiveFailures)*10.0 + 0.35*normPrice + 0.45*normTTFT + 0.20*healthPenalty
		default: // StrategyWeightedSLOScore
			ranked[i].Score = 0.40*normPrice + 0.40*normTTFT + 0.20*healthPenalty
		}
	}

	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].Score != ranked[j].Score {
			return ranked[i].Score < ranked[j].Score
		}
		if ranked[i].EffectivePriceMicroUSDPer1M != ranked[j].EffectivePriceMicroUSDPer1M {
			return ranked[i].EffectivePriceMicroUSDPer1M < ranked[j].EffectivePriceMicroUSDPer1M
		}
		if ranked[i].Quote.MaxTTFTMs != ranked[j].Quote.MaxTTFTMs {
			return ranked[i].Quote.MaxTTFTMs < ranked[j].Quote.MaxTTFTMs
		}
		return ranked[i].Worker.ProviderID < ranked[j].Worker.ProviderID
	})
	return ranked
}

type cachedQuoteEntry struct {
	worker    WorkerRecord
	quote     InferenceQuote
	quoteHash string
	cachedAt  time.Time
}

// SchedulerConfig controls stateless scheduling, failover, and receipt
// verification behavior on a single gateway instance.
type SchedulerConfig struct {
	GatewayID           string
	GatewayOperator     string
	OperatorType        string
	Strategy            Strategy
	GatewayFeeBps       uint64
	Domain              EIP712Domain
	FailoverEnabled     bool
	VerifyReceipts      bool
	ProviderTimeout     time.Duration
	MaxFailoverAttempts int
	QuoteCacheEnabled   bool
	QuoteCacheTTL       time.Duration
	HTTPClient          *http.Client
}

// Scheduler executes stateless multi-provider discovery, quote verification,
// ranking, execution, failover recovery, and offline receipt verification.
type Scheduler struct {
	cfg    SchedulerConfig
	client *http.Client
	nonces *NonceCache

	healthMu sync.RWMutex
	health   map[string]ProviderHealth

	cacheMu    sync.RWMutex
	quoteCache map[string][]cachedQuoteEntry // keyed by modelID
}

// NewScheduler initializes a stateless Scheduler for a gateway instance.
func NewScheduler(cfg SchedulerConfig, nonces *NonceCache) *Scheduler {
	if cfg.Domain.Name == "" {
		cfg.Domain = DefaultDomain()
	}
	if !ValidStrategy(cfg.Strategy) {
		cfg.Strategy = StrategyWeightedSLOScore
	}
	if cfg.ProviderTimeout <= 0 {
		cfg.ProviderTimeout = 2 * time.Second
	}
	if cfg.MaxFailoverAttempts <= 0 {
		cfg.MaxFailoverAttempts = 4
	}
	if nonces == nil {
		nonces = NewNonceCache()
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: cfg.ProviderTimeout}
	}
	return &Scheduler{
		cfg:        cfg,
		client:     client,
		nonces:     nonces,
		health:     make(map[string]ProviderHealth),
		quoteCache: make(map[string][]cachedQuoteEntry),
	}
}

// InvalidateQuoteCache clears any local cached standing quote metadata.
func (s *Scheduler) InvalidateQuoteCache() {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	s.quoteCache = make(map[string][]cachedQuoteEntry)
}

func (s *Scheduler) snapshotHealth() map[string]ProviderHealth {
	s.healthMu.RLock()
	defer s.healthMu.RUnlock()
	cp := make(map[string]ProviderHealth, len(s.health))
	for k, v := range s.health {
		cp[k] = v
	}
	return cp
}

func (s *Scheduler) recordSuccess(providerID string, ttftMs float64) {
	s.healthMu.Lock()
	defer s.healthMu.Unlock()
	h := s.health[providerID]
	h.ConsecutiveFailures = 0
	h.TotalSuccesses++
	if h.ObservedTTFTMs == 0 {
		h.ObservedTTFTMs = ttftMs
	} else {
		h.ObservedTTFTMs = 0.7*h.ObservedTTFTMs + 0.3*ttftMs
	}
	s.health[providerID] = h
}

func (s *Scheduler) recordFailure(providerID string) {
	s.healthMu.Lock()
	defer s.healthMu.Unlock()
	h := s.health[providerID]
	h.ConsecutiveFailures++
	h.TotalFailures++
	h.LastFailureAt = time.Now()
	s.health[providerID] = h
}

// QuoteFetchResult captures verified candidates and rejected quote errors.
type QuoteFetchResult struct {
	Candidates      []CandidateQuote
	RejectedErrors  []error
	CacheHit        bool
	RefreshDuration time.Duration
}

// FetchAndRankQuotes queries eligible workers for signed quotes (or uses a warm
// TTL cache for candidate pre-screening), verifies every quote offline, and
// ranks them according to strategy.
func (s *Scheduler) FetchAndRankQuotes(
	ctx context.Context,
	workers []WorkerRecord,
	modelID string,
	maxTokens int,
	requestHash string,
	strategy Strategy,
) QuoteFetchResult {
	if !ValidStrategy(strategy) {
		strategy = s.cfg.Strategy
	}
	if maxTokens <= 0 {
		maxTokens = 128
	}
	start := time.Now()

	// If standing quote caching is enabled and fresh within QuoteCacheTTL,
	// reuse the cached price/TTFT ranking and fetch a fresh execution quote
	// only when dispatching (avoiding K-way fan-out on every request).
	if s.cfg.QuoteCacheEnabled && s.cfg.QuoteCacheTTL > 0 && requestHash != "" {
		if cached, ok := s.getWarmCache(modelID, workers); ok {
			cands := make([]CandidateQuote, 0, len(cached))
			var rejected []error
			for _, e := range cached {
				effPrice, err := ApplyGatewayFeeChecked(e.quote.PriceMicroUSDPer1M, s.cfg.GatewayFeeBps)
				if err != nil {
					rejected = append(rejected, fmt.Errorf("provider %s: %w", e.worker.ProviderID, err))
					continue
				}
				cands = append(cands, CandidateQuote{
					Worker:                      e.worker,
					Quote:                       e.quote,
					QuoteHash:                   e.quoteHash,
					EffectivePriceMicroUSDPer1M: effPrice,
				})
			}
			ranked := RankCandidates(strategy, cands, s.snapshotHealth())
			return QuoteFetchResult{
				Candidates:      ranked,
				RejectedErrors:  rejected,
				CacheHit:        true,
				RefreshDuration: time.Since(start),
			}
		}
	}

	type fetchOut struct {
		cand CandidateQuote
		ok   bool
		err  error
	}

	results := make([]fetchOut, len(workers))
	var wg sync.WaitGroup
	for i, w := range workers {
		wg.Add(1)
		go func(idx int, rec WorkerRecord) {
			defer wg.Done()
			q, qh, effPrice, err := s.fetchSingleQuote(ctx, rec, modelID, maxTokens, requestHash)
			if err != nil {
				results[idx] = fetchOut{err: fmt.Errorf("provider %s: %w", rec.ProviderID, err)}
				return
			}
			results[idx] = fetchOut{
				cand: CandidateQuote{
					Worker:                      rec,
					Quote:                       q,
					QuoteHash:                   qh,
					EffectivePriceMicroUSDPer1M: effPrice,
				},
				ok: true,
			}
		}(i, w)
	}
	wg.Wait()

	cands := make([]CandidateQuote, 0, len(workers))
	var rejected []error
	cacheEntries := make([]cachedQuoteEntry, 0, len(workers))
	now := time.Now()
	for _, r := range results {
		if r.ok {
			cands = append(cands, r.cand)
			cacheEntries = append(cacheEntries, cachedQuoteEntry{
				worker:    r.cand.Worker,
				quote:     r.cand.Quote,
				quoteHash: r.cand.QuoteHash,
				cachedAt:  now,
			})
		} else if r.err != nil {
			rejected = append(rejected, r.err)
		}
	}

	if s.cfg.QuoteCacheEnabled && s.cfg.QuoteCacheTTL > 0 && len(cacheEntries) > 0 {
		s.putWarmCache(modelID, cacheEntries)
	}

	ranked := RankCandidates(strategy, cands, s.snapshotHealth())
	return QuoteFetchResult{
		Candidates:      ranked,
		RejectedErrors:  rejected,
		CacheHit:        false,
		RefreshDuration: time.Since(start),
	}
}

func (s *Scheduler) getWarmCache(modelID string, workers []WorkerRecord) ([]cachedQuoteEntry, bool) {
	s.cacheMu.RLock()
	entries, ok := s.quoteCache[modelID]
	s.cacheMu.RUnlock()
	if !ok || len(entries) == 0 {
		return nil, false
	}
	now := time.Now()
	nowUnix := now.Unix()
	allowed := make(map[string]WorkerRecord, len(workers))
	for _, w := range workers {
		allowed[w.ProviderID] = w
	}
	valid := make([]cachedQuoteEntry, 0, len(entries))
	for _, e := range entries {
		if now.Sub(e.cachedAt) > s.cfg.QuoteCacheTTL || nowUnix > e.quote.ExpiresAt {
			return nil, false
		}
		if w, exists := allowed[e.worker.ProviderID]; exists {
			e.worker = w
			valid = append(valid, e)
		}
	}
	if len(valid) == 0 {
		return nil, false
	}
	return valid, true
}

func (s *Scheduler) putWarmCache(modelID string, entries []cachedQuoteEntry) {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	s.quoteCache[modelID] = entries
}

func (s *Scheduler) fetchSingleQuote(
	ctx context.Context,
	rec WorkerRecord,
	modelID string,
	maxTokens int,
	requestHash string,
) (InferenceQuote, string, uint64, error) {
	reqCtx, cancel := context.WithTimeout(ctx, s.cfg.ProviderTimeout)
	defer cancel()

	body, err := json.Marshal(QuoteRequest{
		ModelID:     modelID,
		MaxTokens:   maxTokens,
		RequestHash: requestHash,
		GatewayID:   s.cfg.GatewayID,
	})
	if err != nil {
		return InferenceQuote{}, "", 0, err
	}
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, strings.TrimRight(rec.EndpointURL, "/")+"/worker/v1/quote", bytes.NewReader(body))
	if err != nil {
		return InferenceQuote{}, "", 0, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		return InferenceQuote{}, "", 0, fmt.Errorf("%w: %v", ErrProviderUnavailable, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return InferenceQuote{}, "", 0, fmt.Errorf("%w: quote status %d", ErrProviderUnavailable, resp.StatusCode)
	}

	var q InferenceQuote
	if err := json.NewDecoder(resp.Body).Decode(&q); err != nil {
		return InferenceQuote{}, "", 0, fmt.Errorf("%w: decode quote: %v", ErrInvalidQuote, err)
	}
	if err := VerifyQuote(s.cfg.Domain, q, time.Now().Unix()); err != nil {
		return InferenceQuote{}, "", 0, err
	}
	if !SameAddress(q.ProviderAddress, rec.OperatorAddress) || q.ProviderID != rec.ProviderID {
		return InferenceQuote{}, "", 0, fmt.Errorf("%w: quote provider %s (%s) does not match directory record %s (%s)",
			ErrSignerMismatch, q.ProviderID, q.ProviderAddress, rec.ProviderID, rec.OperatorAddress)
	}
	spec, ok := rec.FindModel(modelID)
	if !ok || q.ModelDigest != spec.ModelDigest {
		return InferenceQuote{}, "", 0, fmt.Errorf("%w: quote model digest mismatch", ErrInvalidQuote)
	}
	if requestHash != "" && q.RequestHash != requestHash {
		return InferenceQuote{}, "", 0, fmt.Errorf("%w: quote requestHash mismatch", ErrInvalidQuote)
	}
	effPrice, err := ApplyGatewayFeeChecked(q.PriceMicroUSDPer1M, s.cfg.GatewayFeeBps)
	if err != nil {
		return InferenceQuote{}, "", 0, err
	}
	qh, err := QuoteHash(s.cfg.Domain, q)
	if err != nil {
		return InferenceQuote{}, "", 0, err
	}
	return q, qh, effPrice, nil
}

// ScheduleResult contains the executed inference response, verified quote &
// receipt, and transparent routing metadata.
type ScheduleResult struct {
	Exec  ExecuteResponse
	Quote InferenceQuote
	Route RouteMetadata
}

// ScheduleAndExecute discovers quotes, ranks providers, and dispatches req
// with automatic failover recovery across competing healthy peers.
func (s *Scheduler) ScheduleAndExecute(
	ctx context.Context,
	workers []WorkerRecord,
	req ChatCompletionRequest,
	gatewayKey *KeyPair,
) (*ScheduleResult, error) {
	if len(workers) == 0 {
		return nil, ErrNoHealthyProviders
	}
	strategy := s.cfg.Strategy
	if req.Strategy != "" {
		strategy = NormalizeStrategy(req.Strategy)
	}
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 64
	}
	reqHash := CanonicalRequestHash(req)

	qRes := s.FetchAndRankQuotes(ctx, workers, req.Model, maxTokens, reqHash, strategy)
	if len(qRes.Candidates) == 0 {
		if len(qRes.RejectedErrors) > 0 {
			return nil, fmt.Errorf("%w: %w", ErrNoHealthyProviders, qRes.RejectedErrors[0])
		}
		return nil, ErrNoHealthyProviders
	}

	maxAttempts := len(qRes.Candidates)
	if !s.cfg.FailoverEnabled {
		maxAttempts = 1
	} else if s.cfg.MaxFailoverAttempts > 0 && maxAttempts > s.cfg.MaxFailoverAttempts {
		maxAttempts = s.cfg.MaxFailoverAttempts
	}

	var failedProviders []string
	var lastErr error
	extraQuoteDur := time.Duration(0)

	for attempt := 0; attempt < maxAttempts; attempt++ {
		cand := qRes.Candidates[attempt]
		execQuote := cand.Quote
		execQuoteHash := cand.QuoteHash
		effPrice := cand.EffectivePriceMicroUSDPer1M

		// When ranking came from a warm standing quote cache, or when a slow
		// earlier attempt consumed this backup quote's lifetime, fetch a fresh
		// single-use signed quote bound to this RequestHash before consuming
		// the nonce and dispatching.
		nowUnix := time.Now().Unix()
		if qRes.CacheHit || nowUnix >= execQuote.ExpiresAt {
			qStart := time.Now()
			freshQ, freshQH, freshEffPrice, err := s.fetchSingleQuote(ctx, cand.Worker, req.Model, maxTokens, reqHash)
			extraQuoteDur += time.Since(qStart)
			if err != nil {
				s.recordFailure(cand.Worker.ProviderID)
				failedProviders = append(failedProviders, cand.Worker.ProviderID)
				lastErr = err
				continue
			}
			// If rapid price churn shifted the winner's price above cached,
			// invalidate cache so subsequent requests re-rank cleanly.
			if qRes.CacheHit && freshQ.PriceMicroUSDPer1M != cand.Quote.PriceMicroUSDPer1M {
				s.InvalidateQuoteCache()
			}
			execQuote = freshQ
			execQuoteHash = freshQH
			effPrice = freshEffPrice
		} else {
			checkedPrice, err := ApplyGatewayFeeChecked(execQuote.PriceMicroUSDPer1M, s.cfg.GatewayFeeBps)
			if err != nil {
				s.recordFailure(cand.Worker.ProviderID)
				failedProviders = append(failedProviders, cand.Worker.ProviderID)
				lastErr = err
				continue
			}
			effPrice = checkedPrice
		}

		// Enforce local per-gateway replay nonce guard before execution.
		if err := s.nonces.Consume(execQuote.Nonce, execQuote.ExpiresAt, time.Now().Unix()); err != nil {
			s.recordFailure(cand.Worker.ProviderID)
			failedProviders = append(failedProviders, cand.Worker.ProviderID)
			lastErr = err
			continue
		}

		execResp, err := s.executeOnWorker(ctx, cand.Worker, execQuote, req)
		if err != nil {
			s.recordFailure(cand.Worker.ProviderID)
			failedProviders = append(failedProviders, cand.Worker.ProviderID)
			lastErr = err
			continue
		}

		verifyStart := time.Now()
		if s.cfg.VerifyReceipts {
			if err := VerifyReceipt(s.cfg.Domain, execResp.Receipt, &execQuote, reqHash, execResp.Content); err != nil {
				s.recordFailure(cand.Worker.ProviderID)
				failedProviders = append(failedProviders, cand.Worker.ProviderID)
				lastErr = err
				continue
			}
			if execResp.PromptTokens != execResp.Receipt.PromptTokens ||
				execResp.CompletionTokens != execResp.Receipt.CompletionTokens {
				s.recordFailure(cand.Worker.ProviderID)
				failedProviders = append(failedProviders, cand.Worker.ProviderID)
				lastErr = fmt.Errorf("%w: execute envelope token counts (%d, %d) do not match signed receipt (%d, %d)",
					ErrInvalidReceipt,
					execResp.PromptTokens, execResp.CompletionTokens,
					execResp.Receipt.PromptTokens, execResp.Receipt.CompletionTokens)
				continue
			}
		}
		if gatewayKey != nil {
			if err := SignGatewayAttestation(s.cfg.Domain, gatewayKey, &execResp.Receipt); err != nil {
				lastErr = err
				continue
			}
		}
		verifyMs := float64(time.Since(verifyStart).Microseconds()) / 1000.0

		s.recordSuccess(cand.Worker.ProviderID, execResp.TTFTMs)
		_ = execQuoteHash

		return &ScheduleResult{
			Exec:  execResp,
			Quote: execQuote,
			Route: RouteMetadata{
				GatewayID:                   s.cfg.GatewayID,
				GatewayOperator:             s.cfg.GatewayOperator,
				OperatorType:                s.cfg.OperatorType,
				Strategy:                    string(strategy),
				ProviderID:                  cand.Worker.ProviderID,
				CandidatesEvaluated:         len(qRes.Candidates),
				Attempts:                    attempt + 1,
				FailedProviders:             failedProviders,
				FailoverOccurred:            attempt > 0,
				QuoteCacheHit:               qRes.CacheHit,
				QuoteRefreshMs:              float64((qRes.RefreshDuration + extraQuoteDur).Microseconds()) / 1000.0,
				VerifyLatencyMs:             verifyMs,
				GatewayFeeBps:               s.cfg.GatewayFeeBps,
				ProviderPriceMicroUSDPer1M:  execQuote.PriceMicroUSDPer1M,
				EffectivePriceMicroUSDPer1M: effPrice,
			},
		}, nil
	}

	if lastErr == nil {
		lastErr = ErrNoHealthyProviders
	}
	if errors.Is(lastErr, ErrReplayNonce) || errors.Is(lastErr, ErrInvalidReceipt) ||
		errors.Is(lastErr, ErrOutputHashMismatch) || errors.Is(lastErr, ErrInvalidSignature) ||
		errors.Is(lastErr, ErrQuoteExpired) {
		return nil, lastErr
	}
	return nil, fmt.Errorf("%w: %w", ErrNoHealthyProviders, lastErr)
}

func (s *Scheduler) executeOnWorker(
	ctx context.Context,
	rec WorkerRecord,
	quote InferenceQuote,
	req ChatCompletionRequest,
) (ExecuteResponse, error) {
	reqCtx, cancel := context.WithTimeout(ctx, s.cfg.ProviderTimeout)
	defer cancel()

	body, err := json.Marshal(ExecuteRequest{
		Quote:           quote,
		Request:         req,
		GatewayID:       s.cfg.GatewayID,
		GatewayOperator: s.cfg.GatewayOperator,
	})
	if err != nil {
		return ExecuteResponse{}, err
	}
	httpReq, err := http.NewRequestWithContext(reqCtx, http.MethodPost, strings.TrimRight(rec.EndpointURL, "/")+"/worker/v1/execute", bytes.NewReader(body))
	if err != nil {
		return ExecuteResponse{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := s.client.Do(httpReq)
	if err != nil {
		return ExecuteResponse{}, fmt.Errorf("%w: %v", ErrProviderUnavailable, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ExecuteResponse{}, fmt.Errorf("%w: execute status %d", ErrProviderUnavailable, resp.StatusCode)
	}
	var out ExecuteResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return ExecuteResponse{}, fmt.Errorf("%w: decode execute response: %v", ErrInvalidReceipt, err)
	}
	return out, nil
}
