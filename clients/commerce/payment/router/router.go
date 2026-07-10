// Package router provides an intelligent multi-processor payment routing layer.
//
// The Router implements processor.PaymentProcessor so it can be used as a
// drop-in replacement anywhere a single processor is expected. Internally it
// delegates to real processors selected by one of five configurable strategies:
//
//   - PrimaryFallback: try a designated primary, fall back through the list
//   - RoundRobin: distribute requests evenly across processors
//   - CurrencyBased: route by currency code to a designated processor
//   - WeightedRandom: probabilistic distribution according to configured weights
//   - LeastLoad: pick the processor with the fewest in-flight requests
//
// Each processor is wrapped in a circuit breaker that opens after consecutive
// failures and probes with limited requests before fully closing again.
//
// Transaction IDs returned by the router are prefixed with the processor type
// (e.g. "square:ch_xxx") so that Capture and Refund operations can be routed
// back to the exact processor that handled the original authorization or charge.
package router

import (
	"context"
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/payment/processor"
)

// ---------------------------------------------------------------------------
// Strategy
// ---------------------------------------------------------------------------

// Strategy controls how the router selects a processor for each request.
type Strategy string

const (
	// PrimaryFallback tries the primary processor first, then iterates
	// through the Processors list on failure.
	PrimaryFallback Strategy = "primary_fallback"

	// RoundRobin distributes requests evenly across all configured processors.
	RoundRobin Strategy = "round_robin"

	// CurrencyBased selects a processor based on the payment currency.
	CurrencyBased Strategy = "currency_based"

	// WeightedRandom selects processors with probability proportional to
	// their configured weights.
	WeightedRandom Strategy = "weighted_random"

	// LeastLoad routes to the processor with the fewest in-flight requests.
	LeastLoad Strategy = "least_load"
)

// routerProcessorType is the ProcessorType returned by Router.Type().
const routerProcessorType processor.ProcessorType = "router"

// txIDSeparator separates the processor type prefix from the original
// transaction ID in routed transaction IDs.
const txIDSeparator = ":"

// ---------------------------------------------------------------------------
// Configuration
// ---------------------------------------------------------------------------

// Config controls the router's behaviour.
type Config struct {
	// Strategy selects the routing algorithm.
	Strategy Strategy

	// Primary is the preferred processor for PrimaryFallback strategy.
	Primary processor.ProcessorType

	// Processors is the ordered list of processors the router may use.
	// For PrimaryFallback the primary is tried first, then this list in order.
	Processors []processor.ProcessorType

	// CurrencyMap maps currency codes to processors (CurrencyBased strategy).
	CurrencyMap map[string]processor.ProcessorType

	// Weights assigns relative weights to processors (WeightedRandom strategy).
	Weights map[processor.ProcessorType]int

	// MaxRetries is the maximum number of fallback processors to try after
	// the first selection fails. 0 means try every configured processor.
	MaxRetries int

	// CircuitBreaker configures per-processor circuit breakers.
	CircuitBreaker CircuitBreakerConfig
}

// CircuitBreakerConfig tunes the per-processor circuit breaker.
type CircuitBreakerConfig struct {
	// FailureThreshold is the number of consecutive failures before the
	// breaker opens. Default 5.
	FailureThreshold int

	// ResetTimeout is how long the breaker stays open before transitioning
	// to half-open. Default 30s.
	ResetTimeout time.Duration

	// HalfOpenMax is the maximum number of probe requests allowed while the
	// breaker is half-open. Default 1.
	HalfOpenMax int
}

func (c CircuitBreakerConfig) withDefaults() CircuitBreakerConfig {
	if c.FailureThreshold <= 0 {
		c.FailureThreshold = 5
	}
	if c.ResetTimeout <= 0 {
		c.ResetTimeout = 30 * time.Second
	}
	if c.HalfOpenMax <= 0 {
		c.HalfOpenMax = 1
	}
	return c
}

// ---------------------------------------------------------------------------
// Circuit breaker
// ---------------------------------------------------------------------------

type cbState int

const (
	cbClosed   cbState = iota // normal operation
	cbOpen                    // rejecting all requests
	cbHalfOpen                // allowing limited probes
)

type circuitBreaker struct {
	mu          sync.Mutex
	state       cbState
	failures    int
	halfOpenReq int
	lastFailure time.Time
	config      CircuitBreakerConfig
}

func newCircuitBreaker(cfg CircuitBreakerConfig) *circuitBreaker {
	return &circuitBreaker{
		state:  cbClosed,
		config: cfg.withDefaults(),
	}
}

// allow returns true if the circuit breaker permits a request.
func (cb *circuitBreaker) allow() bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	switch cb.state {
	case cbClosed:
		return true
	case cbOpen:
		if time.Since(cb.lastFailure) >= cb.config.ResetTimeout {
			cb.state = cbHalfOpen
			cb.halfOpenReq = 0
			return true
		}
		return false
	case cbHalfOpen:
		if cb.halfOpenReq < cb.config.HalfOpenMax {
			cb.halfOpenReq++
			return true
		}
		return false
	}
	return false
}

// success records a successful request.
func (cb *circuitBreaker) success() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.failures = 0
	cb.halfOpenReq = 0
	cb.state = cbClosed
}

// failure records a failed request.
func (cb *circuitBreaker) failure() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.failures++
	cb.lastFailure = time.Now()

	switch cb.state {
	case cbHalfOpen:
		// Any failure in half-open immediately re-opens the breaker.
		cb.state = cbOpen
	case cbClosed:
		if cb.failures >= cb.config.FailureThreshold {
			cb.state = cbOpen
		}
	}
}

// ---------------------------------------------------------------------------
// Router
// ---------------------------------------------------------------------------

// Router is a PaymentProcessor that delegates to real processors via
// configurable strategies and per-processor circuit breakers.
type Router struct {
	*processor.BaseProcessor
	config   Config
	registry *processor.Registry
	breakers map[processor.ProcessorType]*circuitBreaker
	mu       sync.RWMutex

	// rrCounter is an atomically incremented round-robin counter.
	rrCounter uint64

	// inflight tracks per-processor in-flight request counts.
	inflight map[processor.ProcessorType]*int64

	// rng is used for weighted random selection; guarded by rngMu.
	rng   *rand.Rand
	rngMu sync.Mutex
}

// NewRouter creates a Router backed by the given registry and config.
// It initialises a circuit breaker for every processor listed in
// config.Processors.
func NewRouter(registry *processor.Registry, config Config) *Router {
	config.CircuitBreaker = config.CircuitBreaker.withDefaults()

	// Collect the union of all processor currencies.
	allCurrencies := make(map[currency.Type]struct{})
	for _, pt := range config.Processors {
		p, err := registry.Get(pt)
		if err != nil {
			continue
		}
		for _, c := range p.SupportedCurrencies() {
			allCurrencies[c] = struct{}{}
		}
	}
	currencies := make([]currency.Type, 0, len(allCurrencies))
	for c := range allCurrencies {
		currencies = append(currencies, c)
	}

	breakers := make(map[processor.ProcessorType]*circuitBreaker, len(config.Processors))
	inflight := make(map[processor.ProcessorType]*int64, len(config.Processors))
	for _, pt := range config.Processors {
		breakers[pt] = newCircuitBreaker(config.CircuitBreaker)
		v := int64(0)
		inflight[pt] = &v
	}

	base := processor.NewBaseProcessor(routerProcessorType, currencies)
	base.SetConfigured(true)

	return &Router{
		BaseProcessor: base,
		config:        config,
		registry:      registry,
		breakers:      breakers,
		inflight:      inflight,
		rng:           rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

// ---------------------------------------------------------------------------
// PaymentProcessor interface
// ---------------------------------------------------------------------------

// Type returns "router".
func (r *Router) Type() processor.ProcessorType {
	return routerProcessorType
}

// Charge processes a payment through a selected processor.
func (r *Router) Charge(ctx context.Context, req processor.PaymentRequest) (*processor.PaymentResult, error) {
	return r.routePayment(ctx, req, func(p processor.PaymentProcessor, rq processor.PaymentRequest) (*processor.PaymentResult, error) {
		return p.Charge(ctx, rq)
	})
}

// Authorize authorizes a payment without capturing.
func (r *Router) Authorize(ctx context.Context, req processor.PaymentRequest) (*processor.PaymentResult, error) {
	return r.routePayment(ctx, req, func(p processor.PaymentProcessor, rq processor.PaymentRequest) (*processor.PaymentResult, error) {
		return p.Authorize(ctx, rq)
	})
}

// Capture captures a previously authorized payment.
// The transactionID must be router-prefixed ("processor:txid").
func (r *Router) Capture(ctx context.Context, transactionID string, amount currency.Cents) (*processor.PaymentResult, error) {
	pt, rawID, err := r.parseTransactionID(transactionID)
	if err != nil {
		return nil, err
	}

	p, err := r.getProcessor(ctx, pt)
	if err != nil {
		return nil, err
	}

	result, err := p.Capture(ctx, rawID, amount)
	if err != nil {
		return nil, err
	}

	r.prefixResult(result, pt)
	return result, nil
}

// Refund processes a refund on the processor that handled the original charge.
func (r *Router) Refund(ctx context.Context, req processor.RefundRequest) (*processor.RefundResult, error) {
	pt, rawID, err := r.parseTransactionID(req.TransactionID)
	if err != nil {
		return nil, err
	}

	p, err := r.getProcessor(ctx, pt)
	if err != nil {
		return nil, err
	}

	// Send with the raw (un-prefixed) transaction ID.
	original := req.TransactionID
	req.TransactionID = rawID
	result, err := p.Refund(ctx, req)

	if err != nil {
		req.TransactionID = original
		return nil, err
	}

	// Prefix the refund ID so downstream can route refund-of-refund if needed.
	if result != nil && result.RefundID != "" {
		result.RefundID = prefixID(pt, result.RefundID)
	}
	return result, nil
}

// GetTransaction searches all processors for the given transaction.
// If the ID is router-prefixed, only the matching processor is queried.
func (r *Router) GetTransaction(ctx context.Context, txID string) (*processor.Transaction, error) {
	// Try parsing a prefixed ID first.
	if pt, rawID, err := r.parseTransactionID(txID); err == nil {
		p, pErr := r.getProcessor(ctx, pt)
		if pErr != nil {
			return nil, pErr
		}
		tx, tErr := p.GetTransaction(ctx, rawID)
		if tErr != nil {
			return nil, tErr
		}
		if tx != nil {
			tx.ID = prefixID(pt, tx.ID)
		}
		return tx, nil
	}

	// No prefix: try each processor.
	for _, pt := range r.config.Processors {
		p, err := r.registry.Get(pt)
		if err != nil {
			continue
		}
		if !p.IsAvailable(ctx) {
			continue
		}
		tx, err := p.GetTransaction(ctx, txID)
		if err != nil {
			continue
		}
		if tx != nil {
			tx.ID = prefixID(pt, tx.ID)
			return tx, nil
		}
	}

	return nil, processor.ErrTransactionNotFound
}

// ValidateWebhook tries each configured processor until one validates.
func (r *Router) ValidateWebhook(ctx context.Context, payload []byte, signature string) (*processor.WebhookEvent, error) {
	for _, pt := range r.config.Processors {
		p, err := r.registry.Get(pt)
		if err != nil {
			continue
		}
		if !p.IsAvailable(ctx) {
			continue
		}
		event, err := p.ValidateWebhook(ctx, payload, signature)
		if err == nil && event != nil {
			return event, nil
		}
	}
	return nil, processor.ErrWebhookValidationFailed
}

// SupportedCurrencies returns the union of all processors' currencies.
func (r *Router) SupportedCurrencies() []currency.Type {
	// The base processor was initialized with the full union.
	return r.BaseProcessor.SupportedCurrencies()
}

// IsAvailable returns true if any configured processor is available.
func (r *Router) IsAvailable(ctx context.Context) bool {
	for _, pt := range r.config.Processors {
		p, err := r.registry.Get(pt)
		if err != nil {
			continue
		}
		if p.IsAvailable(ctx) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Internal: routing
// ---------------------------------------------------------------------------

// paymentFunc abstracts Charge and Authorize so both can share routing logic.
type paymentFunc func(processor.PaymentProcessor, processor.PaymentRequest) (*processor.PaymentResult, error)

// routePayment selects a processor, executes fn, and falls back on failure.
func (r *Router) routePayment(ctx context.Context, req processor.PaymentRequest, fn paymentFunc) (*processor.PaymentResult, error) {
	candidates := r.selectCandidates(ctx, req)
	if len(candidates) == 0 {
		return nil, processor.NewPaymentError(routerProcessorType, "NO_PROCESSOR",
			fmt.Sprintf("no processor available for currency %s", req.Currency), nil)
	}

	maxAttempts := len(candidates)
	if r.config.MaxRetries > 0 && r.config.MaxRetries+1 < maxAttempts {
		maxAttempts = r.config.MaxRetries + 1
	}

	var lastErr error
	for i := 0; i < maxAttempts; i++ {
		pt := candidates[i]

		cb := r.getBreaker(pt)
		if cb != nil && !cb.allow() {
			continue
		}

		p, err := r.registry.Get(pt)
		if err != nil {
			continue
		}
		if !p.IsAvailable(ctx) {
			continue
		}

		// Track inflight.
		counter := r.getInflight(pt)
		if counter != nil {
			atomic.AddInt64(counter, 1)
		}

		result, err := fn(p, req)

		if counter != nil {
			atomic.AddInt64(counter, -1)
		}

		if err != nil {
			lastErr = err
			if cb != nil {
				cb.failure()
			}
			continue
		}

		if result != nil && !result.Success {
			lastErr = fmt.Errorf("processor %s: %s", pt, result.ErrorMessage)
			if cb != nil {
				cb.failure()
			}
			continue
		}

		// Success.
		if cb != nil {
			cb.success()
		}
		r.prefixResult(result, pt)
		return result, nil
	}

	if lastErr != nil {
		return nil, processor.NewPaymentError(routerProcessorType, "ALL_FAILED",
			"all processors failed", lastErr)
	}
	return nil, processor.NewPaymentError(routerProcessorType, "NO_PROCESSOR",
		"no processor could handle the request", nil)
}

// selectCandidates returns an ordered slice of processor types to try.
// The order depends on the configured strategy.
func (r *Router) selectCandidates(ctx context.Context, req processor.PaymentRequest) []processor.ProcessorType {
	switch r.config.Strategy {
	case PrimaryFallback:
		return r.candidatesPrimaryFallback(ctx, req)
	case RoundRobin:
		return r.candidatesRoundRobin(ctx, req)
	case CurrencyBased:
		return r.candidatesCurrencyBased(ctx, req)
	case WeightedRandom:
		return r.candidatesWeightedRandom(ctx, req)
	case LeastLoad:
		return r.candidatesLeastLoad(ctx, req)
	default:
		return r.candidatesPrimaryFallback(ctx, req)
	}
}

func (r *Router) candidatesPrimaryFallback(_ context.Context, _ processor.PaymentRequest) []processor.ProcessorType {
	seen := make(map[processor.ProcessorType]bool, len(r.config.Processors)+1)
	result := make([]processor.ProcessorType, 0, len(r.config.Processors)+1)

	if r.config.Primary != "" {
		result = append(result, r.config.Primary)
		seen[r.config.Primary] = true
	}
	for _, pt := range r.config.Processors {
		if !seen[pt] {
			result = append(result, pt)
			seen[pt] = true
		}
	}
	return result
}

func (r *Router) candidatesRoundRobin(_ context.Context, _ processor.PaymentRequest) []processor.ProcessorType {
	n := len(r.config.Processors)
	if n == 0 {
		return nil
	}

	idx := atomic.AddUint64(&r.rrCounter, 1) - 1
	result := make([]processor.ProcessorType, n)
	for i := 0; i < n; i++ {
		result[i] = r.config.Processors[(int(idx)+i)%n]
	}
	return result
}

func (r *Router) candidatesCurrencyBased(_ context.Context, req processor.PaymentRequest) []processor.ProcessorType {
	seen := make(map[processor.ProcessorType]bool, len(r.config.Processors)+1)
	result := make([]processor.ProcessorType, 0, len(r.config.Processors)+1)

	// Currency-specific processor first.
	if r.config.CurrencyMap != nil {
		if pt, ok := r.config.CurrencyMap[string(req.Currency)]; ok {
			result = append(result, pt)
			seen[pt] = true
		}
	}

	// Fallback to primary.
	if r.config.Primary != "" && !seen[r.config.Primary] {
		result = append(result, r.config.Primary)
		seen[r.config.Primary] = true
	}

	// Then the rest.
	for _, pt := range r.config.Processors {
		if !seen[pt] {
			result = append(result, pt)
			seen[pt] = true
		}
	}
	return result
}

func (r *Router) candidatesWeightedRandom(_ context.Context, _ processor.PaymentRequest) []processor.ProcessorType {
	n := len(r.config.Processors)
	if n == 0 {
		return nil
	}
	if len(r.config.Weights) == 0 {
		// No weights: fall back to random shuffle.
		return r.shuffled()
	}

	// Build cumulative weight table.
	type entry struct {
		pt     processor.ProcessorType
		weight int
	}
	entries := make([]entry, 0, n)
	for _, pt := range r.config.Processors {
		w := r.config.Weights[pt]
		if w <= 0 {
			w = 1
		}
		entries = append(entries, entry{pt, w})
	}

	// Weighted shuffle: repeatedly pick from remaining entries
	// proportional to weight, then remove from pool.
	result := make([]processor.ProcessorType, 0, n)
	remaining := make([]entry, len(entries))
	copy(remaining, entries)

	r.rngMu.Lock()
	defer r.rngMu.Unlock()

	for len(remaining) > 0 {
		total := 0
		for _, e := range remaining {
			total += e.weight
		}
		pick := r.rng.Intn(total)
		cum := 0
		chosen := 0
		for i, e := range remaining {
			cum += e.weight
			if pick < cum {
				chosen = i
				break
			}
		}
		result = append(result, remaining[chosen].pt)
		remaining[chosen] = remaining[len(remaining)-1]
		remaining = remaining[:len(remaining)-1]
	}
	return result
}

func (r *Router) candidatesLeastLoad(_ context.Context, _ processor.PaymentRequest) []processor.ProcessorType {
	n := len(r.config.Processors)
	if n == 0 {
		return nil
	}

	type entry struct {
		pt   processor.ProcessorType
		load int64
	}
	entries := make([]entry, n)
	for i, pt := range r.config.Processors {
		var load int64
		if c := r.getInflight(pt); c != nil {
			load = atomic.LoadInt64(c)
		}
		entries[i] = entry{pt, load}
	}

	// Simple insertion sort by load (stable for equal loads).
	for i := 1; i < len(entries); i++ {
		key := entries[i]
		j := i - 1
		for j >= 0 && entries[j].load > key.load {
			entries[j+1] = entries[j]
			j--
		}
		entries[j+1] = key
	}

	result := make([]processor.ProcessorType, n)
	for i, e := range entries {
		result[i] = e.pt
	}
	return result
}

// shuffled returns config.Processors in random order.
func (r *Router) shuffled() []processor.ProcessorType {
	n := len(r.config.Processors)
	result := make([]processor.ProcessorType, n)
	copy(result, r.config.Processors)

	r.rngMu.Lock()
	defer r.rngMu.Unlock()

	for i := n - 1; i > 0; i-- {
		j := r.rng.Intn(i + 1)
		result[i], result[j] = result[j], result[i]
	}
	return result
}

// ---------------------------------------------------------------------------
// Internal: helpers
// ---------------------------------------------------------------------------

// prefixID creates a router-format transaction ID: "processorType:rawID".
func prefixID(pt processor.ProcessorType, rawID string) string {
	return string(pt) + txIDSeparator + rawID
}

// parseTransactionID splits a router-format transaction ID into processor type
// and raw ID. If the ID has no prefix, it tries all processors (returns error).
func (r *Router) parseTransactionID(txID string) (processor.ProcessorType, string, error) {
	idx := strings.Index(txID, txIDSeparator)
	if idx <= 0 {
		return "", "", fmt.Errorf("transaction ID %q has no processor prefix", txID)
	}
	pt := processor.ProcessorType(txID[:idx])
	rawID := txID[idx+1:]
	if rawID == "" {
		return "", "", fmt.Errorf("transaction ID %q has empty raw ID", txID)
	}
	return pt, rawID, nil
}

// prefixResult adds the processor type prefix to the TransactionID in a result.
func (r *Router) prefixResult(result *processor.PaymentResult, pt processor.ProcessorType) {
	if result == nil {
		return
	}
	if result.TransactionID != "" {
		result.TransactionID = prefixID(pt, result.TransactionID)
	}
}

// getProcessor fetches a processor from the registry.
func (r *Router) getProcessor(ctx context.Context, pt processor.ProcessorType) (processor.PaymentProcessor, error) {
	p, err := r.registry.Get(pt)
	if err != nil {
		return nil, processor.NewPaymentError(routerProcessorType, "PROCESSOR_NOT_FOUND",
			fmt.Sprintf("processor %s not found in registry", pt), err)
	}
	if !p.IsAvailable(ctx) {
		return nil, processor.NewPaymentError(routerProcessorType, "PROCESSOR_UNAVAILABLE",
			fmt.Sprintf("processor %s is not available", pt), nil)
	}
	return p, nil
}

// getBreaker returns the circuit breaker for a processor type, or nil.
func (r *Router) getBreaker(pt processor.ProcessorType) *circuitBreaker {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.breakers[pt]
}

// getInflight returns the inflight counter for a processor type, or nil.
func (r *Router) getInflight(pt processor.ProcessorType) *int64 {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.inflight[pt]
}

// ---------------------------------------------------------------------------
// Compile-time interface check
// ---------------------------------------------------------------------------

var _ processor.PaymentProcessor = (*Router)(nil)
