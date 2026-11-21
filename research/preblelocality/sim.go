// Copyright 2026 Hanzo AI, Inc.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package preblelocality

import (
	"container/heap"
	"encoding/json"
	"math"
	"math/rand"
	"sort"
	"strconv"
)

// Metrics is the raw result of one simulated run. Latency, reuse and
// goodput statistics are computed over requests that arrived after the
// warmup window; cache, residency and utilization state evolve from time
// zero.
type Metrics struct {
	Scenario string `json:"scenario"`
	Policy   string `json:"policy"`
	Seed     int64  `json:"seed"`

	RequestsArrived int `json:"requests_arrived"`
	RequestsDone    int `json:"requests_done"`
	RequestsDropped int `json:"requests_dropped"`

	TTFTP50  float64 `json:"ttft_p50_s"`
	TTFTP99  float64 `json:"ttft_p99_s"`
	TTFTMean float64 `json:"ttft_mean_s"`
	TTFTMax  float64 `json:"ttft_max_s"`

	PrefixTokensSeen int64   `json:"prefix_tokens_seen"`
	PrefixTokensHit  int64   `json:"prefix_tokens_hit"`
	ReuseRatio       float64 `json:"reuse_ratio"`

	Replicas         int     `json:"replicas"`
	WallS            float64 `json:"wall_s"`
	BusyS            float64 `json:"busy_s"`
	ColdS            float64 `json:"cold_s"`
	GPUUtil          float64 `json:"gpu_util"`
	GoodputTPS       float64 `json:"goodput_tps"`
	SuffixTokensDone int64   `json:"suffix_tokens_done"`

	MaxQueueDepth  int    `json:"max_queue_depth"`
	CacheEvictions uint64 `json:"cache_evictions"`
	ColdStarts     int    `json:"cold_starts"`
	WarmupTrimmed  int    `json:"warmup_trimmed"`

	// Per-replica high-water mark of active slots (cap-invariant evidence).
	MaxActiveByReplica []int `json:"max_active_by_replica"`
	// Requests routed per tenant (skew evidence).
	TenantRequests []int `json:"tenant_requests"`
}

// SimConfig parameterizes one deterministic simulation run.
type SimConfig struct {
	Name        string
	Seed        int64
	DurationS   float64 // arrivals are generated in [0, DurationS)
	WarmupS     float64 // statistics start after this timestamp
	GraceS      float64 // extra time before the run is force-stopped
	ArrivalRate float64 // cluster-wide Poisson arrivals/sec

	Tenants        int
	ZipfS          float64 // >0 selects tenants by Zipf(s); <=0 uniform
	ReuseFreshProb float64 // probability an arrival starts a fresh session

	PrefixMin, PrefixMax int
	SuffixMin, SuffixMax int

	Replicas       int
	Slots          int
	CacheCapTokens int
	PrefillTPS     float64
	DecodeTPS      float64
	ColdStartS     float64

	QueueWeight float64 // cache-aware queueing weight
}

// ZipfSampler draws tenant indices in [0, imax] with P(k) ∝ (v+k)^-s.
// Hand-rolled over a precomputed CDF so behavior never depends on the
// standard library's parameter constraints.
type ZipfSampler struct {
	cdf []float64 // cdf[i] = P(index <= i)
}

// NewZipfSampler builds a Zipf(s, v) sampler over [0, imax].
func NewZipfSampler(rng *rand.Rand, s, v float64, imax int) *ZipfSampler {
	n := imax + 1
	w := make([]float64, n)
	total := 0.0
	for i := 0; i < n; i++ {
		w[i] = 1.0 / math.Pow(v+float64(i), s)
		total += w[i]
	}
	cdf := make([]float64, n)
	acc := 0.0
	for i := 0; i < n; i++ {
		acc += w[i] / total
		cdf[i] = acc
	}
	cdf[n-1] = 1.0
	return &ZipfSampler{cdf: cdf}
}

// Draw returns a sample in [0, imax] using rng.
func (z *ZipfSampler) Draw(rng *rand.Rand) int {
	u := rng.Float64()
	lo, hi := 0, len(z.cdf)-1
	for lo < hi {
		mid := (lo + hi) / 2
		if z.cdf[mid] < u {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return lo
}

type sessionInfo struct {
	name      string
	hash      uint64
	prefixLen int
}

type eventKind int

const (
	evArrival eventKind = iota
	evComplete
)

type event struct {
	t       float64
	seq     int64
	kind    eventKind
	req     *Request
	replica int // evComplete target
}

type eventQueue []*event

func (q eventQueue) Len() int { return len(q) }
func (q eventQueue) Less(i, j int) bool {
	if q[i].t != q[j].t {
		return q[i].t < q[j].t
	}
	return q[i].seq < q[j].seq
}
func (q eventQueue) Swap(i, j int) { q[i], q[j] = q[j], q[i] }
func (q *eventQueue) Push(x any)   { *q = append(*q, x.(*event)) }
func (q *eventQueue) Pop() any {
	old := *q
	n := len(old)
	it := old[n-1]
	old[n-1] = nil
	*q = old[:n-1]
	return it
}

// RunSim executes one deterministic run of cfg under policy and returns
// the raw metrics. Identical (cfg, policy) pairs always produce identical
// output: the RNG is seeded and every ordering decision is total.
//
// Model semantics:
//
//   - Arrivals are Poisson at cfg.ArrivalRate; each arrival selects a
//     tenant (uniform or Zipf), then a session: with probability
//     ReuseFreshProb a fresh session (unique prefix), otherwise an
//     existing session of that tenant (repeated prefix — the Preble
//     observation that production traffic repeats prompt material).
//   - The policy routes the request to a replica with a free KV slot; if
//     every replica is at its cap the request joins a FIFO cluster queue
//     retried on every slot release.
//   - Service start pays the cold-start residency cost for tenants not
//     yet warm on the replica; prefill recomputes only tokens not resident
//     in the replica's salted cache; TTFT = queue + cold start + prefill.
//   - On completion the replica publishes the prefix into its salted LRU
//     cache (evicting least-recently-used keys under the token budget).
func RunSim(cfg SimConfig, policy Policy) Metrics {
	rng := rand.New(rand.NewSource(cfg.Seed))

	// ---- workload generation pass (decoupled from the event loop) ----
	type genReq struct {
		t   float64
		req *Request
	}
	gen := make([]genReq, 0, int(cfg.ArrivalRate*cfg.DurationS)+16)
	sessions := make([][]sessionInfo, cfg.Tenants)
	var zipf *ZipfSampler
	if cfg.ZipfS > 0 {
		zipf = NewZipfSampler(rng, cfg.ZipfS, 1, cfg.Tenants-1)
	}
	var sessionSeq int
	t := 0.0
	for {
		t += rng.ExpFloat64() / cfg.ArrivalRate
		if t >= cfg.DurationS {
			break
		}
		tenant := 0
		if zipf != nil {
			tenant = zipf.Draw(rng)
		} else {
			tenant = rng.Intn(cfg.Tenants)
		}
		var sess sessionInfo
		if rng.Float64() < cfg.ReuseFreshProb || len(sessions[tenant]) == 0 {
			sess = sessionInfo{
				name:      "s" + strconv.Itoa(sessionSeq),
				hash:      rng.Uint64(),
				prefixLen: cfg.PrefixMin + rng.Intn(cfg.PrefixMax-cfg.PrefixMin+1),
			}
			sessionSeq++
			sessions[tenant] = append(sessions[tenant], sess)
		} else {
			// Repeat an existing session, biased toward recently opened ones:
			// production agent traffic concentrates reuse in the last few
			// sessions (multi-turn bursts, repeated tool templates).
			list := sessions[tenant]
			recent := 8
			if recent > len(list) {
				recent = len(list)
			}
			sess = list[len(list)-recent+rng.Intn(recent)]
		}
		gen = append(gen, genReq{t: t, req: &Request{
			ID:         len(gen),
			Tenant:     "t" + strconv.Itoa(tenant),
			Session:    sess.name,
			PrefixLen:  sess.prefixLen,
			SuffixLen:  cfg.SuffixMin + rng.Intn(cfg.SuffixMax-cfg.SuffixMin+1),
			PrefixHash: sess.hash,
			Arrival:    t,
		}})
	}

	// ---- replica construction ----
	replicas := make([]*Replica, cfg.Replicas)
	for i := range replicas {
		replicas[i] = NewReplica(i, cfg.Slots, cfg.CacheCapTokens, cfg.PrefillTPS, cfg.DecodeTPS, cfg.ColdStartS)
	}
	cs := &ClusterState{Replicas: replicas}

	m := Metrics{Scenario: cfg.Name, Policy: policy.Name(), Seed: cfg.Seed, Replicas: cfg.Replicas}
	m.MaxActiveByReplica = make([]int, cfg.Replicas)
	m.TenantRequests = make([]int, cfg.Tenants)

	var (
		evq        eventQueue
		seq        int64
		pending    []*Request
		wall       float64
		ttfts      []float64
		prefixHit  int64
		prefixSeen int64
		suffixDone int64
	)
	heap.Init(&evq)
	for _, g := range gen {
		seq++
		heap.Push(&evq, &event{t: g.t, seq: seq, kind: evArrival, req: g.req})
	}

	publishPrefix := func(r *Replica, req *Request) {
		if req.PrefixLen > 0 {
			r.Cache.Publish(CacheKeyOf(req), req.PrefixLen)
		}
	}
	startService := func(at float64, r *Replica, req *Request) {
		cold := 0.0
		if !r.Warm(req.Tenant) {
			cold = r.ColdStartS
			r.MarkWarm(req.Tenant)
			r.ColdS += cold
			r.ColdN++
		}
		hit := r.HitTokens(req)
		prefillTokens := req.PrefixLen + req.SuffixLen - hit
		prefillS := float64(prefillTokens) / r.PrefillTPS
		decodeS := float64(req.SuffixLen) / r.DecodeTPS
		tStart := at + cold
		ttft := tStart + prefillS - req.Arrival
		r.active++
		if r.active > r.MaxActive {
			r.MaxActive = r.active
		}
		r.BusyS += prefillS + decodeS
		seq++
		heap.Push(&evq, &event{t: tStart + prefillS + decodeS, seq: seq, kind: evComplete, req: req, replica: r.ID})
		if req.Arrival >= cfg.WarmupS {
			ttfts = append(ttfts, ttft)
			prefixSeen += int64(req.PrefixLen)
			prefixHit += int64(hit)
			suffixDone += int64(req.SuffixLen)
		}
	}
	// tryAdmit retries the FIFO cluster queue under the current policy;
	// called after every slot release. Starts service at the release time.
	tryAdmit := func(at float64) {
		for len(pending) > 0 {
			req := pending[0]
			sel := policy.Select(req, cs)
			if sel < 0 {
				return
			}
			pending = pending[1:]
			startService(at, replicas[sel], req)
		}
	}

	for evq.Len() > 0 {
		ev := heap.Pop(&evq).(*event)
		wall = ev.t
		switch ev.kind {
		case evArrival:
			m.RequestsArrived++
			if idx, ok := tenantIndexOf(ev.req.Tenant); ok && idx < len(m.TenantRequests) {
				m.TenantRequests[idx]++
			}
			sel := policy.Select(ev.req, cs)
			if sel < 0 {
				pending = append(pending, ev.req)
				if d := len(pending); d > m.MaxQueueDepth {
					m.MaxQueueDepth = d
				}
				continue
			}
			startService(ev.t, replicas[sel], ev.req)
		case evComplete:
			r := replicas[ev.replica]
			publishPrefix(r, ev.req)
			r.active--
			m.RequestsDone++
			tryAdmit(ev.t)
		}
		if wall > cfg.DurationS+cfg.GraceS {
			break
		}
	}

	m.RequestsDropped = m.RequestsArrived - m.RequestsDone
	for _, r := range replicas {
		m.MaxActiveByReplica[r.ID] = r.MaxActive
		m.BusyS += r.BusyS
		m.ColdS += r.ColdS
		m.ColdStarts += r.ColdN
		m.CacheEvictions += r.Cache.Evictions
	}
	m.WallS = wall
	measureS := cfg.DurationS - cfg.WarmupS
	if measureS <= 0 {
		measureS = wall
	}
	m.SuffixTokensDone = suffixDone
	m.GoodputTPS = float64(m.SuffixTokensDone) / measureS
	if wall > 0 {
		m.GPUUtil = m.BusyS / (float64(cfg.Replicas*cfg.Slots) * wall)
	}
	if prefixSeen > 0 {
		m.ReuseRatio = float64(prefixHit) / float64(prefixSeen)
	}
	if len(ttfts) > 0 {
		sorted := append([]float64(nil), ttfts...)
		sort.Float64s(sorted)
		m.TTFTP50 = sorted[len(sorted)/2]
		idx := (len(sorted)*99+99)/100 - 1
		if idx < 0 {
			idx = 0
		}
		m.TTFTP99 = sorted[idx]
		var sum float64
		for _, v := range ttfts {
			sum += v
		}
		m.TTFTMean = sum / float64(len(ttfts))
		m.TTFTMax = sorted[len(sorted)-1]
		m.WarmupTrimmed = len(gen) - len(ttfts)
	}
	return m
}

func tenantIndexOf(t string) (int, bool) {
	if len(t) < 2 || t[0] != 't' {
		return 0, false
	}
	n := 0
	for i := 1; i < len(t); i++ {
		if t[i] < '0' || t[i] > '9' {
			return 0, false
		}
		n = n*10 + int(t[i]-'0')
	}
	return n, true
}

// ScenarioPresets returns the six workload regimes required by
// hanzoai/cloud#35: a balanced baseline, tenant skew, a cold-start burst,
// eviction pressure, a zero-reuse negative control, and saturation.
//
// The shared base models the regime HIP-1345 §2.3 targets (agentic serving
// with large reusable prompt spans): mean prefix ≈ 37k tokens dominates
// service time (1.84 s prefill at 20k tok/s) over short decode bursts
// (0.64 s at 500 tok/s per stream). Arrival rates are set to ~0.65 of the
// no-reuse cluster capacity (saturation: ~0.96).
func ScenarioPresets() []SimConfig {
	base := func(name string, seed int64) SimConfig {
		return SimConfig{
			Name:           name,
			Seed:           seed,
			DurationS:      1800,
			WarmupS:        300,
			GraceS:         3600,
			ArrivalRate:    8.5,
			Tenants:        64,
			ZipfS:          0,
			ReuseFreshProb: 0.35,
			PrefixMin:      8192,
			PrefixMax:      65536,
			SuffixMin:      128,
			SuffixMax:      512,
			Replicas:       8,
			Slots:          4,
			CacheCapTokens: 524288,
			PrefillTPS:     20000,
			DecodeTPS:      500,
			ColdStartS:     2.5,
			QueueWeight:    0.6,
		}
	}
	balanced := base("baseline_balanced", 20261117)

	skew := base("tenant_skew_zipf", 20261117)
	skew.ZipfS = 1.2

	burst := base("cold_start_burst", 20261117)
	burst.Tenants = 192
	burst.ColdStartS = 4.0
	burst.ReuseFreshProb = 0.6
	burst.ZipfS = 1.1
	burst.ArrivalRate = 8.0

	evict := base("eviction_pressure", 20261117)
	evict.ZipfS = 1.1
	evict.CacheCapTokens = 131072

	zero := base("zero_reuse_negative", 20261117)
	zero.ReuseFreshProb = 1.0

	sat := base("saturation_high", 20261117)
	sat.ArrivalRate = 12.4

	return []SimConfig{balanced, skew, burst, evict, zero, sat}
}

// MatrixSeeds are the seeds used across the scenario matrix.
var MatrixSeeds = []int64{20261117, 20261118, 20261119}

// MatrixPolicyWeight is the queueing weight used by the cache-aware
// policy across the matrix. Calibrated to mean service time / slots
// (≈ 2.5 s / 4): the marginal tail delay an extra in-flight request adds.
const MatrixPolicyWeight = 0.6

// RunMatrix executes every preset x policy x seed and returns the raw
// results in deterministic order.
func RunMatrix() []Metrics {
	var out []Metrics
	for _, cfg := range ScenarioPresets() {
		for _, policy := range Policies(MatrixPolicyWeight) {
			for _, seed := range MatrixSeeds {
				c := cfg
				c.Seed = seed
				out = append(out, RunSim(c, policy))
			}
		}
	}
	return out
}

// MatrixJSON renders results as indented JSON (the committed raw format).
func MatrixJSON(ms []Metrics) ([]byte, error) {
	return json.MarshalIndent(ms, "", "  ")
}
