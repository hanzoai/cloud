// SPDX-License-Identifier: Apache-2.0

package preble

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"
)

// Policy identifies a GPU placement scheduling algorithm evaluated in the identical
// scheduler state and KV-cache harness.
type Policy string

const (
	// PolicyLeastLoaded routes each request to the GPU replica with minimum projected
	// compute queue wait (shipping baseline, prefix-agnostic).
	PolicyLeastLoaded Policy = "least_loaded"

	// PolicyRoundRobin distributes requests cyclically across registered GPU replicas
	// (naive stateless baseline).
	PolicyRoundRobin Policy = "round_robin"

	// PolicyPrefixGreedy always pins a request to the GPU replica with the longest
	// cached KV prefix regardless of queue saturation (demonstrates hot-prefix skew collapse).
	PolicyPrefixGreedy Policy = "prefix_greedy"

	// PolicyPrebleE2 implements Preble Exploitation-vs-Exploration placement balancing
	// cached KV prefix reuse, queued compute load, eviction pressure, scale-out cold-start
	// latency, and dynamic hot-prefix replication.
	PolicyPrebleE2 Policy = "preble_e2"
)

// ParsePolicy validates and normalizes a policy name. Empty defaults to PolicyPrebleE2.
func ParsePolicy(raw string) (Policy, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", string(PolicyPrebleE2):
		return PolicyPrebleE2, nil
	case string(PolicyLeastLoaded):
		return PolicyLeastLoaded, nil
	case string(PolicyRoundRobin):
		return PolicyRoundRobin, nil
	case string(PolicyPrefixGreedy):
		return PolicyPrefixGreedy, nil
	default:
		return "", fmt.Errorf("%w: unsupported policy %q", ErrInvalidRequest, raw)
	}
}

// ReplicaConfig defines hardware and capacity characteristics for one GPU serving replica.
type ReplicaConfig struct {
	// ID uniquely identifies this GPU replica (e.g., "gpu-0").
	ID string `json:"id"`
	// GPUModel is a human-readable accelerator descriptor (e.g., "H100-80GB").
	GPUModel string `json:"gpuModel"`
	// KVBudgetTokens is the exact KV-cache memory capacity in tokens.
	KVBudgetTokens int `json:"kvBudgetTokens"`
	// PrefillTokPerSec is the sustained prefill throughput in tokens per second.
	PrefillTokPerSec float64 `json:"prefillTokPerSec"`
	// DecodeTokPerSec is the aggregate batched decode throughput in tokens per second.
	DecodeTokPerSec float64 `json:"decodeTokPerSec"`
	// MaxConcurrent bounds simultaneous active requests on this replica.
	MaxConcurrent int `json:"maxConcurrent"`
	// WarmupDuration models replica cold-start initialization latency upon registration/scale-out.
	WarmupDuration time.Duration `json:"warmupDuration"`
}

func (c ReplicaConfig) withDefaults() (ReplicaConfig, error) {
	out := c
	out.ID = strings.TrimSpace(out.ID)
	if out.ID == "" || len(out.ID) > 128 {
		return out, fmt.Errorf("%w: replica id must be 1..128 chars", ErrInvalidConfig)
	}
	if out.GPUModel == "" {
		out.GPUModel = "H100-80GB"
	}
	if out.KVBudgetTokens == 0 {
		out.KVBudgetTokens = 131072
	}
	if out.KVBudgetTokens < DefaultBlockSize || out.KVBudgetTokens > MaxKVBudgetTokens {
		return out, fmt.Errorf("%w: kvBudgetTokens %d out of bounds", ErrInvalidConfig, out.KVBudgetTokens)
	}
	if out.PrefillTokPerSec == 0 {
		out.PrefillTokPerSec = 40000
	}
	if math.IsNaN(out.PrefillTokPerSec) || math.IsInf(out.PrefillTokPerSec, 0) || out.PrefillTokPerSec <= 0 || out.PrefillTokPerSec > 1e8 {
		return out, fmt.Errorf("%w: invalid prefillTokPerSec", ErrInvalidConfig)
	}
	if out.DecodeTokPerSec == 0 {
		out.DecodeTokPerSec = 1800
	}
	if math.IsNaN(out.DecodeTokPerSec) || math.IsInf(out.DecodeTokPerSec, 0) || out.DecodeTokPerSec <= 0 || out.DecodeTokPerSec > 1e7 {
		return out, fmt.Errorf("%w: invalid decodeTokPerSec", ErrInvalidConfig)
	}
	if out.MaxConcurrent == 0 {
		out.MaxConcurrent = 64
	}
	if out.MaxConcurrent < 1 || out.MaxConcurrent > 4096 {
		return out, fmt.Errorf("%w: maxConcurrent %d out of bounds", ErrInvalidConfig, out.MaxConcurrent)
	}
	if out.WarmupDuration < 0 || out.WarmupDuration > 10*time.Minute {
		return out, fmt.Errorf("%w: warmupDuration out of bounds", ErrInvalidConfig)
	}
	return out, nil
}

// SchedulerConfig configures the Preble E2 placement scheduler.
type SchedulerConfig struct {
	Policy                Policy
	BlockSize             int
	SkewQueueThresholdMs  float64
	SkewLoadRatio         float64
	EvictionWeightMs      float64
	DecodeContentionAlpha float64
	MaxInFlightRequests   int
	Clock                 func() time.Time
}

func (c SchedulerConfig) withDefaults() (SchedulerConfig, error) {
	out := c
	if out.Policy == "" {
		out.Policy = PolicyPrebleE2
	}
	if _, err := ParsePolicy(string(out.Policy)); err != nil {
		return out, err
	}
	if out.BlockSize == 0 {
		out.BlockSize = DefaultBlockSize
	}
	if out.BlockSize < 1 || out.BlockSize > 4096 {
		return out, fmt.Errorf("%w: blockSize %d out of bounds [1, 4096]", ErrInvalidConfig, out.BlockSize)
	}
	if out.SkewQueueThresholdMs <= 0 {
		out.SkewQueueThresholdMs = 35.0
	}
	if out.SkewLoadRatio <= 1.0 {
		out.SkewLoadRatio = 1.5
	}
	if out.EvictionWeightMs <= 0 {
		out.EvictionWeightMs = 1.0
	}
	if out.DecodeContentionAlpha <= 0 {
		out.DecodeContentionAlpha = 0.06
	}
	if out.MaxInFlightRequests <= 0 {
		out.MaxInFlightRequests = 16384
	}
	if out.Clock == nil {
		out.Clock = func() time.Time { return time.Now().UTC() }
	}
	return out, nil
}

// PlacementRequest describes one prompt inference request to place on a GPU replica.
type PlacementRequest struct {
	// RequestID uniquely identifies this request (generated if empty).
	RequestID string
	// Tenant is the server-stamped authenticated organization/tenant ID (strictly isolated).
	Tenant string
	// ACLScope further partitions KV prefix reuse within a tenant (e.g., project/role/workspace).
	ACLScope string
	// TokenIDs provides pre-tokenized prompt token IDs (takes precedence over Prompt).
	TokenIDs []uint64
	// Prompt provides raw prompt text tokenized deterministically if TokenIDs is empty.
	Prompt string
	// OutputTokens is the estimated or scheduled decode token length.
	OutputTokens int
	// Policy optionally overrides the scheduler's default policy for this call.
	Policy Policy
}

// PlacementDecision records the chosen GPU replica and full E2 explainability metrics.
type PlacementDecision struct {
	RequestID           string  `json:"requestId"`
	Tenant              string  `json:"tenant"`
	ACLScope            string  `json:"aclScope"`
	ReplicaID           string  `json:"replicaId"`
	Policy              Policy  `json:"policy"`
	PromptTokens        int     `json:"promptTokens"`
	MatchedTokens       int     `json:"matchedTokens"`
	UncachedTokens      int     `json:"uncachedTokens"`
	CacheReuseRatio     float64 `json:"cacheReuseRatio"`
	EstimatedQueueMs    float64 `json:"estimatedQueueMs"`
	EstimatedPrefillMs  float64 `json:"estimatedPrefillMs"`
	EstimatedColdMs     float64 `json:"estimatedColdMs"`
	EstimatedEvictMs    float64 `json:"estimatedEvictMs"`
	EstimatedTTFTMs     float64 `json:"estimatedTtftMs"`
	ReplicatedHotPrefix bool    `json:"replicatedHotPrefix"`
	EvictedBlocks       int     `json:"evictedBlocks"`
	DecisionLatencyNs   int64   `json:"decisionLatencyNs"`
}

// ReplicaStatus summarizes live non-tenant-specific utilization and KV state of one GPU replica.
type ReplicaStatus struct {
	ID                  string  `json:"id"`
	GPUModel            string  `json:"gpuModel"`
	KVBudgetTokens      int     `json:"kvBudgetTokens"`
	KVUsedTokens        int     `json:"kvUsedTokens"`
	KVPinnedTokens      int     `json:"kvPinnedTokens"`
	KVFreeTokens        int     `json:"kvFreeTokens"`
	KVOccupancyRatio    float64 `json:"kvOccupancyRatio"`
	ActiveRequests      int     `json:"activeRequests"`
	ActivePrefillTokens int     `json:"activePrefillTokens"`
	ActiveDecodeTokens  int     `json:"activeDecodeTokens"`
	TotalPlacements     uint64  `json:"totalPlacements"`
	EvictedBlocks       uint64  `json:"evictedBlocks"`
	EvictedTokens       uint64  `json:"evictedTokens"`
	WarmingUp           bool    `json:"warmingUp"`
}

// TenantMetrics summarizes prefix reuse and latency statistics strictly for one tenant.
type TenantMetrics struct {
	Tenant                  string  `json:"tenant"`
	TotalRequests           uint64  `json:"totalRequests"`
	TotalPromptTokens       uint64  `json:"totalPromptTokens"`
	TotalMatchedTokens      uint64  `json:"totalMatchedTokens"`
	CacheReuseRatio         float64 `json:"cacheReuseRatio"`
	HotPrefixReplications   uint64  `json:"hotPrefixReplications"`
	AvgDecisionLatencyUs    float64 `json:"avgDecisionLatencyUs"`
	ClusterReplicaCount     int     `json:"clusterReplicaCount"`
	ClusterTotalEvictions   uint64  `json:"clusterTotalEvictions"`
	ClusterKVOccupancyRatio float64 `json:"clusterKvOccupancyRatio"`
}

type replicaState struct {
	cfg                 ReplicaConfig
	registeredAt        time.Time
	warmReadyAt         time.Time
	prefillFreeAt       time.Time
	activeRequests      int
	activePrefillTokens int
	activeDecodeTokens  int
	totalPlacements     uint64
}

type activeReq struct {
	id             string
	tenant         string
	aclScope       string
	replicaID      string
	uncachedTokens int
	outputTokens   int
	pinned         []BlockHash
	placedAt       time.Time
}

type inFlightKey struct {
	tenant    string
	requestID string
}

type tenantStat struct {
	requests           uint64
	promptTokens       uint64
	matchedTokens      uint64
	replications       uint64
	totalDecisionLatNs uint64
}

// Scheduler implements the concurrency-safe Preble E2 prompt-locality GPU placement engine
// and its matched baseline routing policies.
type Scheduler struct {
	mu       sync.Mutex
	cfg      SchedulerConfig
	index    *PrefixIndex
	replicas map[string]*replicaState
	order    []string
	rrCursor uint64
	inFlight map[inFlightKey]*activeReq
	tStats   map[string]*tenantStat
}

// NewScheduler constructs a Preble scheduler with the given configuration.
func NewScheduler(cfg SchedulerConfig) (*Scheduler, error) {
	norm, err := cfg.withDefaults()
	if err != nil {
		return nil, err
	}
	return &Scheduler{
		cfg:      norm,
		index:    NewPrefixIndex(norm.BlockSize),
		replicas: make(map[string]*replicaState),
		inFlight: make(map[inFlightKey]*activeReq),
		tStats:   make(map[string]*tenantStat),
	}, nil
}

// Policy returns the scheduler's default placement policy.
func (s *Scheduler) Policy() Policy {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg.Policy
}

// AddReplica registers a GPU replica using the scheduler's clock.
func (s *Scheduler) AddReplica(cfg ReplicaConfig) error {
	return s.AddReplicaAt(s.cfg.Clock(), cfg)
}

// AddReplicaAt registers a GPU replica at timestamp now (for deterministic simulation and tests).
func (s *Scheduler) AddReplicaAt(now time.Time, cfg ReplicaConfig) error {
	norm, err := cfg.withDefaults()
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.replicas) >= MaxReplicas {
		return fmt.Errorf("%w: max replicas (%d) reached", ErrInvalidConfig, MaxReplicas)
	}
	if _, exists := s.replicas[norm.ID]; exists {
		return fmt.Errorf("%w: %s", ErrReplicaExists, norm.ID)
	}
	if err := s.index.AddReplica(norm.ID, norm.KVBudgetTokens); err != nil {
		return err
	}
	s.replicas[norm.ID] = &replicaState{
		cfg:           norm,
		registeredAt:  now,
		warmReadyAt:   now.Add(norm.WarmupDuration),
		prefillFreeAt: now.Add(norm.WarmupDuration),
	}
	s.order = append(s.order, norm.ID)
	sort.Strings(s.order)
	return nil
}

// RemoveReplica removes a GPU replica and prunes all prefix cache entries held on it.
func (s *Scheduler) RemoveReplica(id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("%w: empty replica id", ErrInvalidRequest)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.replicas[id]; !ok {
		return fmt.Errorf("%w: %s", ErrReplicaNotFound, id)
	}
	if err := s.index.RemoveReplica(id); err != nil {
		return err
	}
	delete(s.replicas, id)
	for reqID, ar := range s.inFlight {
		if ar.replicaID == id {
			delete(s.inFlight, reqID)
		}
	}
	filtered := s.order[:0]
	for _, rid := range s.order {
		if rid != id {
			filtered = append(filtered, rid)
		}
	}
	s.order = filtered
	return nil
}

// Place selects a GPU replica for req using the scheduler's wall clock.
func (s *Scheduler) Place(req PlacementRequest) (PlacementDecision, error) {
	return s.PlaceAt(s.cfg.Clock(), req)
}

// PlaceAt selects a GPU replica at timestamp now, updates KV prefix reservations and
// in-flight load state, and measures exact wall-clock decision latency.
func (s *Scheduler) PlaceAt(now time.Time, req PlacementRequest) (PlacementDecision, error) {
	wallStart := time.Now()

	tenant := strings.TrimSpace(req.Tenant)
	if tenant == "" {
		return PlacementDecision{}, fmt.Errorf("%w: tenant is required", ErrInvalidRequest)
	}
	aclScope := strings.TrimSpace(req.ACLScope)
	if len(tenant) > 256 || len(aclScope) > 256 {
		return PlacementDecision{}, fmt.Errorf("%w: tenant or aclScope too long", ErrInvalidRequest)
	}
	if req.OutputTokens < 0 || req.OutputTokens > MaxPromptTokens {
		return PlacementDecision{}, fmt.Errorf("%w: outputTokens %d out of bounds", ErrInvalidRequest, req.OutputTokens)
	}

	tokens := req.TokenIDs
	if len(tokens) == 0 && req.Prompt != "" {
		tokens = TokenizeText(req.Prompt)
	}
	if len(tokens) == 0 {
		return PlacementDecision{}, fmt.Errorf("%w: either tokenIds or prompt must be non-empty", ErrInvalidRequest)
	}
	if len(tokens) > MaxPromptTokens {
		return PlacementDecision{}, fmt.Errorf("%w: prompt tokens %d exceed max %d", ErrInvalidRequest, len(tokens), MaxPromptTokens)
	}

	policy := req.Policy
	if policy == "" {
		policy = s.cfg.Policy
	} else {
		var err error
		policy, err = ParsePolicy(string(policy))
		if err != nil {
			return PlacementDecision{}, err
		}
	}

	promptTokens := len(tokens)
	// Causal LLM inference must always prefill at least 1 token to sample the first output token.
	maxReusableTokens := promptTokens - 1
	if maxReusableTokens < 0 {
		maxReusableTokens = 0
	}

	localityAware := policy == PolicyPrebleE2 || policy == PolicyPrefixGreedy

	// For locality-aware policies, block hashing is part of the timed scheduling decision.
	// For prefix-agnostic baselines (least_loaded, round_robin), block hashing is performed
	// after the routing decision so DecisionLatencyNs reflects their true unindexed routing cost
	// while still accounting identical KV cache hits and evictions in the harness.
	var blocks []TokenBlock
	if localityAware {
		var err error
		blocks, err = HashTokenBlocks(tenant, aclScope, tokens, s.cfg.BlockSize)
		if err != nil {
			return PlacementDecision{}, err
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.order) == 0 {
		return PlacementDecision{}, ErrNoReplicas
	}
	if len(s.inFlight) >= s.cfg.MaxInFlightRequests {
		return PlacementDecision{}, fmt.Errorf("%w: max in-flight requests (%d) reached", ErrInvalidRequest, s.cfg.MaxInFlightRequests)
	}

	reqID := strings.TrimSpace(req.RequestID)
	if reqID == "" {
		reqID = newRequestID()
	}
	flightKey := inFlightKey{tenant: tenant, requestID: reqID}
	if _, dup := s.inFlight[flightKey]; dup {
		return PlacementDecision{}, fmt.Errorf("%w: duplicate active requestId %q", ErrInvalidRequest, reqID)
	}

	var match PrefixMatch
	if localityAware {
		match = s.index.Match(tenant, aclScope, blocks)
	}

	type candidateEval struct {
		id         string
		matched    int
		uncached   int
		queueMs    float64
		prefillMs  float64
		coldMs     float64
		evictMs    float64
		totalMs    float64
		evictEst   EvictionEstimate
		activeReqs int
	}

	evalReplica := func(id string) candidateEval {
		r := s.replicas[id]
		matched := match.MatchedTokens[id]
		if matched > maxReusableTokens {
			matched = maxReusableTokens
		}
		uncached := promptTokens - matched
		if uncached < 1 {
			uncached = 1
		}

		coldMs := 0.0
		readyAt := now
		if r.warmReadyAt.After(now) {
			coldMs = float64(r.warmReadyAt.Sub(now)) / float64(time.Millisecond)
			readyAt = r.warmReadyAt
		}
		queueMs := 0.0
		if r.prefillFreeAt.After(readyAt) {
			queueMs = float64(r.prefillFreeAt.Sub(readyAt)) / float64(time.Millisecond)
		}

		effPrefill := r.cfg.PrefillTokPerSec / (1.0 + s.cfg.DecodeContentionAlpha*float64(r.activeRequests))
		prefillMs := (float64(uncached) / effPrefill) * 1000.0

		var evictEst EvictionEstimate
		evictMs := 0.0
		if localityAware {
			uncachedBlocks := (uncached + s.cfg.BlockSize - 1) / s.cfg.BlockSize
			evictEst = s.index.EstimateEviction(id, uncachedBlocks)
			if evictEst.WouldOverflowPin {
				evictMs = 500.0 + float64(evictEst.BlocksToEvict)*2.0
			} else if evictEst.BlocksToEvict > 0 && evictEst.AvgEvictedReuse > 0 {
				// Physically grounded eviction cost: each evicted block with positive reuse
				// history will require re-prefill of blockSize tokens on future hits, capped
				// at 50% of prefill cost so eviction steering never induces excessive queue skew.
				blockRefillMs := (float64(s.cfg.BlockSize) / r.cfg.PrefillTokPerSec) * 1000.0
				reuseFactor := math.Min(evictEst.AvgEvictedReuse, 1.0)
				evictMs = float64(evictEst.BlocksToEvict) * blockRefillMs * reuseFactor * s.cfg.EvictionWeightMs
				if maxEvict := prefillMs * 0.5; evictMs > maxEvict {
					evictMs = maxEvict
				}
			}
		}

		// Concurrency overload soft barrier if replica is at MaxConcurrent.
		concPenalty := 0.0
		if r.activeRequests >= r.cfg.MaxConcurrent {
			concPenalty = 1000.0 + float64(r.activeRequests-r.cfg.MaxConcurrent)*50.0
		}

		return candidateEval{
			id:         id,
			matched:    matched,
			uncached:   uncached,
			queueMs:    queueMs,
			prefillMs:  prefillMs,
			coldMs:     coldMs,
			evictMs:    evictMs,
			totalMs:    coldMs + queueMs + prefillMs + evictMs + concPenalty,
			evictEst:   evictEst,
			activeReqs: r.activeRequests,
		}
	}

	var chosen candidateEval
	replicatedHot := false

	switch policy {
	case PolicyRoundRobin:
		idx := int(s.rrCursor % uint64(len(s.order)))
		s.rrCursor++
		chosen = evalReplica(s.order[idx])

	case PolicyLeastLoaded:
		bestLoad := math.Inf(1)
		for i, id := range s.order {
			ev := evalReplica(id)
			// Pure load metric: cold start + queue wait + active request contention + full prefill time.
			r := s.replicas[id]
			effPrefill := r.cfg.PrefillTokPerSec / (1.0 + s.cfg.DecodeContentionAlpha*float64(r.activeRequests))
			fullPrefillMs := (float64(promptTokens) / effPrefill) * 1000.0
			loadScore := ev.coldMs + ev.queueMs + fullPrefillMs + float64(ev.activeReqs)*2.0
			if i == 0 || loadScore < bestLoad {
				bestLoad = loadScore
				chosen = ev
			}
		}

	case PolicyPrefixGreedy:
		bestMatched := -1
		bestQueue := math.Inf(1)
		for i, id := range s.order {
			ev := evalReplica(id)
			q := ev.coldMs + ev.queueMs + float64(ev.activeReqs)*2.0
			if i == 0 || ev.matched > bestMatched || (ev.matched == bestMatched && q < bestQueue) {
				bestMatched = ev.matched
				bestQueue = q
				chosen = ev
			}
		}

	case PolicyPrebleE2:
		var bestOverall candidateEval
		var bestWarm candidateEval
		var bestCold candidateEval
		hasWarm := false
		hasCold := false

		for i, id := range s.order {
			ev := evalReplica(id)
			if i == 0 || ev.totalMs < bestOverall.totalMs {
				bestOverall = ev
			}
			if ev.matched > 0 {
				if !hasWarm || ev.totalMs < bestWarm.totalMs {
					bestWarm = ev
					hasWarm = true
				}
			} else {
				if !hasCold || ev.totalMs < bestCold.totalMs {
					bestCold = ev
					hasCold = true
				}
			}
		}

		chosen = bestOverall

		// Preble E2 Exploitation vs. Exploration with work-conservation guard:
		// Replicating a cached prefix onto a cold GPU creates extra prefill FLOPs
		// (matchedTokens of redundant prefill).
		//  1. When a cold GPU has idle/light headroom (coldWait < SkewQueueThresholdMs)
		//     and a hot prefix is concurrently saturating its warm replica(s)
		//     (DeepestActive >= 2), amortize cold prefill and replicate proactively.
		//  2. When ALL cold GPUs are themselves already queued/saturated
		//     (coldWait >= SkewQueueThresholdMs), avoid non-work-conserving replication
		//     unless bestWarm's queue skew genuinely exceeds 2x the extra prefill cost
		//     (preventing cache thrashing under cluster-wide overload).
		if hasWarm && hasCold {
			warmWait := bestWarm.coldMs + bestWarm.queueMs
			coldWait := bestCold.coldMs + bestCold.queueMs
			extraPrefillMs := bestCold.prefillMs - bestWarm.prefillMs
			if extraPrefillMs < 0 {
				extraPrefillMs = 0
			}

			if chosen.matched == 0 && coldWait >= s.cfg.SkewQueueThresholdMs && !bestWarm.evictEst.WouldOverflowPin {
				// Cluster is saturated: only break locality if warm replica is severely skewed.
				if warmWait-coldWait < 2.0*extraPrefillMs+bestCold.evictMs {
					chosen = bestWarm
				}
			}

			if chosen.matched > 0 {
				skewed := warmWait >= s.cfg.SkewQueueThresholdMs &&
					warmWait >= s.cfg.SkewLoadRatio*(coldWait+1.0) &&
					coldWait < s.cfg.SkewQueueThresholdMs &&
					!bestCold.evictEst.WouldOverflowPin

				concurrentHotPrefix := match.DeepestActive >= 2
				if skewed && concurrentHotPrefix {
					amort := 2.0
					if match.DeepestActive >= 4 {
						amort = 3.0
					}
					amortizedCold := coldWait + (bestCold.prefillMs / amort) + bestCold.evictMs
					if amortizedCold < bestWarm.totalMs {
						chosen = bestCold
						replicatedHot = true
					}
				}
			}
		}
		if !replicatedHot && match.MaxMatchedTokens > 0 && chosen.matched == 0 {
			replicatedHot = true
		}
	}

	var decisionLatNs int64
	if !localityAware {
		// Capture pure routing latency for prefix-agnostic baselines before simulator KV bookkeeping.
		decisionLatNs = time.Since(wallStart).Nanoseconds()
		var err error
		blocks, err = HashTokenBlocks(tenant, aclScope, tokens, s.cfg.BlockSize)
		if err != nil {
			return PlacementDecision{}, err
		}
	}

	// Reserve and pin blocks on the chosen replica (exact KV hit/miss/eviction accounting
	// for ALL policies — even baselines that didn't consult the prefix tree).
	pinned, actualMatched, evictedBlocks, err := s.index.ReserveAndPin(tenant, aclScope, chosen.id, blocks)
	if err != nil {
		return PlacementDecision{}, err
	}
	if actualMatched > maxReusableTokens {
		actualMatched = maxReusableTokens
	}
	actualUncached := promptTokens - actualMatched
	if actualUncached < 1 {
		actualUncached = 1
	}

	r := s.replicas[chosen.id]
	startAt := now
	if r.warmReadyAt.After(startAt) {
		startAt = r.warmReadyAt
	}
	if r.prefillFreeAt.After(startAt) {
		startAt = r.prefillFreeAt
	}
	effPrefill := r.cfg.PrefillTokPerSec / (1.0 + s.cfg.DecodeContentionAlpha*float64(r.activeRequests))
	prefillSec := float64(actualUncached) / effPrefill
	prefillDur := time.Duration(prefillSec * float64(time.Second))
	r.prefillFreeAt = startAt.Add(prefillDur)
	r.activeRequests++
	r.activePrefillTokens += actualUncached
	r.activeDecodeTokens += req.OutputTokens
	r.totalPlacements++

	s.inFlight[flightKey] = &activeReq{
		id:             reqID,
		tenant:         tenant,
		aclScope:       aclScope,
		replicaID:      chosen.id,
		uncachedTokens: actualUncached,
		outputTokens:   req.OutputTokens,
		pinned:         pinned,
		placedAt:       now,
	}

	if localityAware {
		decisionLatNs = time.Since(wallStart).Nanoseconds()
	}
	if decisionLatNs < 0 {
		decisionLatNs = 0
	}

	st, ok := s.tStats[tenant]
	if !ok {
		st = &tenantStat{}
		s.tStats[tenant] = st
	}
	st.requests++
	st.promptTokens += uint64(promptTokens)
	st.matchedTokens += uint64(actualMatched)
	if replicatedHot {
		st.replications++
	}
	st.totalDecisionLatNs += uint64(decisionLatNs)

	reuseRatio := 0.0
	if promptTokens > 0 {
		reuseRatio = float64(actualMatched) / float64(promptTokens)
	}
	actualPrefillMs := prefillSec * 1000.0
	return PlacementDecision{
		RequestID:           reqID,
		Tenant:              tenant,
		ACLScope:            aclScope,
		ReplicaID:           chosen.id,
		Policy:              policy,
		PromptTokens:        promptTokens,
		MatchedTokens:       actualMatched,
		UncachedTokens:      actualUncached,
		CacheReuseRatio:     reuseRatio,
		EstimatedQueueMs:    chosen.queueMs,
		EstimatedPrefillMs:  actualPrefillMs,
		EstimatedColdMs:     chosen.coldMs,
		EstimatedEvictMs:    chosen.evictMs,
		EstimatedTTFTMs:     chosen.coldMs + chosen.queueMs + actualPrefillMs,
		ReplicatedHotPrefix: replicatedHot,
		EvictedBlocks:       evictedBlocks,
		DecisionLatencyNs:   decisionLatNs,
	}, nil
}

// Complete releases an in-flight request strictly owned by tenant using the wall clock.
func (s *Scheduler) Complete(tenant, requestID string) error {
	return s.CompleteAt(s.cfg.Clock(), tenant, requestID)
}

// CompleteAt releases an in-flight request strictly owned by tenant at timestamp now.
// Requests belonging to another tenant return ErrRequestNotFound (zero existence leak).
func (s *Scheduler) CompleteAt(now time.Time, tenant, requestID string) error {
	tenant = strings.TrimSpace(tenant)
	requestID = strings.TrimSpace(requestID)
	if tenant == "" || requestID == "" {
		return ErrRequestNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	flightKey := inFlightKey{tenant: tenant, requestID: requestID}
	ar, ok := s.inFlight[flightKey]
	if !ok {
		return ErrRequestNotFound
	}
	delete(s.inFlight, flightKey)

	s.index.Unpin(ar.replicaID, ar.pinned)
	if r, exists := s.replicas[ar.replicaID]; exists {
		if r.activeRequests > 0 {
			r.activeRequests--
		}
		r.activePrefillTokens -= ar.uncachedTokens
		if r.activePrefillTokens < 0 {
			r.activePrefillTokens = 0
		}
		r.activeDecodeTokens -= ar.outputTokens
		if r.activeDecodeTokens < 0 {
			r.activeDecodeTokens = 0
		}
		if r.activeRequests == 0 && r.prefillFreeAt.After(now) {
			r.prefillFreeAt = now
		}
	}
	return nil
}

// Replicas returns aggregate status of all registered GPU replicas (no tenant prefix data).
func (s *Scheduler) Replicas() []ReplicaStatus {
	return s.ReplicasAt(s.cfg.Clock())
}

// ReplicasAt returns aggregate status of all registered GPU replicas at timestamp now.
func (s *Scheduler) ReplicasAt(now time.Time) []ReplicaStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]ReplicaStatus, 0, len(s.order))
	for _, id := range s.order {
		r := s.replicas[id]
		snap, _ := s.index.ReplicaSnapshot(id)
		occ := 0.0
		if snap.MaxTokens > 0 {
			occ = float64(snap.UsedTokens) / float64(snap.MaxTokens)
		}
		out = append(out, ReplicaStatus{
			ID:                  r.cfg.ID,
			GPUModel:            r.cfg.GPUModel,
			KVBudgetTokens:      snap.MaxTokens,
			KVUsedTokens:        snap.UsedTokens,
			KVPinnedTokens:      snap.PinnedTokens,
			KVFreeTokens:        snap.FreeTokens,
			KVOccupancyRatio:    occ,
			ActiveRequests:      r.activeRequests,
			ActivePrefillTokens: r.activePrefillTokens,
			ActiveDecodeTokens:  r.activeDecodeTokens,
			TotalPlacements:     r.totalPlacements,
			EvictedBlocks:       snap.EvictedBlocks,
			EvictedTokens:       snap.EvictedTokens,
			WarmingUp:           r.warmReadyAt.After(now),
		})
	}
	return out
}

// TenantStats returns prefix reuse and latency metrics strictly scoped to tenant plus
// aggregate cluster health counters.
func (s *Scheduler) TenantStats(tenant string) TenantMetrics {
	s.mu.Lock()
	defer s.mu.Unlock()

	var totalEvictions uint64
	var totalUsed, totalMax int
	for _, id := range s.order {
		snap, ok := s.index.ReplicaSnapshot(id)
		if !ok {
			continue
		}
		totalEvictions += snap.EvictedBlocks
		totalUsed += snap.UsedTokens
		totalMax += snap.MaxTokens
	}
	occ := 0.0
	if totalMax > 0 {
		occ = float64(totalUsed) / float64(totalMax)
	}

	m := TenantMetrics{
		Tenant:                  tenant,
		ClusterReplicaCount:     len(s.order),
		ClusterTotalEvictions:   totalEvictions,
		ClusterKVOccupancyRatio: occ,
	}
	if st, ok := s.tStats[tenant]; ok {
		m.TotalRequests = st.requests
		m.TotalPromptTokens = st.promptTokens
		m.TotalMatchedTokens = st.matchedTokens
		m.HotPrefixReplications = st.replications
		if st.promptTokens > 0 {
			m.CacheReuseRatio = float64(st.matchedTokens) / float64(st.promptTokens)
		}
		if st.requests > 0 {
			m.AvgDecisionLatencyUs = float64(st.totalDecisionLatNs) / float64(st.requests) / 1000.0
		}
	}
	return m
}

// IndexNodeCount returns the exact number of live radix tree nodes (for memory bound verification).
func (s *Scheduler) IndexNodeCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.index.NodeCount()
}

func newRequestID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("req-%d", time.Now().UnixNano())
	}
	return "req-" + hex.EncodeToString(b[:])
}
