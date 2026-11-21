// Copyright 2026 Hanzo AI, Inc.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package preblelocality

// Request is a single inference request whose logical prompt is split into
// a reusable prefix span (multi-turn / shared-template KV) and a
// per-request suffix span.
type Request struct {
	ID         int
	Tenant     string
	Session    string
	PrefixLen  int
	SuffixLen  int
	PrefixHash uint64  // stable content hash of the logical prefix tokens
	Arrival    float64 // simulation timestamp of arrival (seconds)
}

// CacheKeyOf returns the salted prefix-cache key for a request.
func CacheKeyOf(req *Request) CacheKey {
	return CacheKey{Tenant: req.Tenant, Session: req.Session, Prefix: req.PrefixHash}
}

// Replica models one GPU serving replica: a bounded number of concurrent
// KV slots (fail-closed cap), a salted prefix cache, per-tenant warm
// residency with a cold-start cost, and aggregate prefill / per-stream
// decode throughput.
type Replica struct {
	ID         int
	Slots      int
	Cache      *PrefixCache
	PrefillTPS float64 // prefill tokens/sec
	DecodeTPS  float64 // decode tokens/sec per in-flight stream
	ColdStartS float64 // seconds to establish residency for a new tenant

	warm   map[string]bool
	active int

	BusyS     float64 // cumulative prefill+decode GPU seconds
	ColdS     float64 // cumulative cold-start (IO) seconds
	ColdN     int     // cumulative cold starts
	MaxActive int     // high-water mark of active slots (cap invariant)
}

// Warm reports whether the replica holds warm residency for tenant.
func (r *Replica) Warm(tenant string) bool {
	return r.warm[tenant]
}

// MarkWarm records that residency for tenant has been established.
func (r *Replica) MarkWarm(tenant string) {
	if r.warm == nil {
		r.warm = make(map[string]bool)
	}
	r.warm[tenant] = true
}

// Peek returns how many tokens of req's prefix span are resident without
// mutating LRU recency. Routing policies use Peek so that scoring never
// reshapes the cache it scores.
func (r *Replica) Peek(req *Request) int {
	tokens, ok := r.Cache.Peek(CacheKeyOf(req))
	if !ok {
		return 0
	}
	if tokens > req.PrefixLen {
		tokens = req.PrefixLen
	}
	return tokens
}

// HitTokens returns how many tokens of req's prefix span are resident,
// promoting the key to most-recently-used. Call this once service has
// actually been placed on the replica.
func (r *Replica) HitTokens(req *Request) int {
	tokens, ok := r.Cache.Lookup(CacheKeyOf(req))
	if !ok {
		return 0
	}
	if tokens > req.PrefixLen {
		tokens = req.PrefixLen
	}
	return tokens
}

// NewReplica constructs a replica with the given slot cap and cache budget.
func NewReplica(id, slots, cacheTokens int, prefillTPS, decodeTPS, coldStartS float64) *Replica {
	return &Replica{
		ID:         id,
		Slots:      slots,
		Cache:      NewPrefixCache(cacheTokens),
		PrefillTPS: prefillTPS,
		DecodeTPS:  decodeTPS,
		ColdStartS: coldStartS,
		warm:       make(map[string]bool),
	}
}

// ClusterState is the observation view a routing policy scores against.
// In Enso this is the gateway's in-memory replica view; here it is the
// authoritative simulator state.
type ClusterState struct {
	Replicas []*Replica
}

// Policy selects a replica for a request, or returns -1 when no replica
// has a free KV slot (fail-closed: the caller must queue, never overload).
type Policy interface {
	Name() string
	Select(req *Request, cs *ClusterState) int
}

// LeastLoadedPolicy routes to the replica with the fewest in-flight
// requests. Ties break to the lowest replica ID, so selection is
// deterministic. It ignores cache residency entirely (the baseline).
type LeastLoadedPolicy struct{}

// Name implements Policy.
func (LeastLoadedPolicy) Name() string { return "least-loaded" }

// Select implements Policy.
func (LeastLoadedPolicy) Select(_ *Request, cs *ClusterState) int {
	best := -1
	bestLoad := 0
	for _, r := range cs.Replicas {
		if r.active >= r.Slots {
			continue
		}
		if best < 0 || r.active < bestLoad || (r.active == bestLoad && r.ID < best) {
			best, bestLoad = r.ID, r.active
		}
	}
	return best
}

// CacheAwarePolicy implements HIP-1345 §2.3 routing: score each replica by
//
//	residencySaving - queueingEstimate - coldStartPenalty
//
// where residencySaving is the prefill time avoided by resident KV
// (probed without mutating cache recency), the queueing estimate charges
// QueueWeight seconds per in-flight request, and the cold-start penalty
// charges the full residency cost when the tenant is not yet warm on that
// replica. Ties break to the lowest replica ID (deterministic). Replicas
// at their slot cap are never selected.
type CacheAwarePolicy struct {
	QueueWeight float64 // seconds charged per in-flight request
}

// NewCacheAwarePolicy returns a policy with the given queueing weight.
func NewCacheAwarePolicy(queueWeight float64) CacheAwarePolicy {
	return CacheAwarePolicy{QueueWeight: queueWeight}
}

// Name implements Policy.
func (p CacheAwarePolicy) Name() string { return "cache-aware" }

// Select implements Policy.
func (p CacheAwarePolicy) Select(req *Request, cs *ClusterState) int {
	best := -1
	bestScore := 0.0
	for _, r := range cs.Replicas {
		if r.active >= r.Slots {
			continue
		}
		score := float64(r.Peek(req))/r.PrefillTPS - p.QueueWeight*float64(r.active)
		if !r.Warm(req.Tenant) {
			score -= r.ColdStartS
		}
		if best < 0 || score > bestScore || (score == bestScore && r.ID < best) {
			best, bestScore = r.ID, score
		}
	}
	return best
}

// Policies returns the two policies under comparison, in fixed order.
func Policies(queueWeight float64) []Policy {
	return []Policy{LeastLoadedPolicy{}, NewCacheAwarePolicy(queueWeight)}
}
