// SPDX-License-Identifier: Apache-2.0

package preble

import (
	"container/heap"
	"fmt"
	"math"
	"sort"
	"time"
)

// TraceRequest is one deterministic inference arrival in a discrete-event workload trace.
type TraceRequest struct {
	RequestID    string
	ArrivalMs    float64
	Tenant       string
	ACLScope     string
	TokenIDs     []uint64
	OutputTokens int
}

// ScaleOutEvent registers an additional GPU replica mid-trace (with optional warmup cold-start).
type ScaleOutEvent struct {
	AtMs    float64
	Replica ReplicaConfig
}

// WorkloadTrace defines a complete reproducible multi-GPU prompt workload trace.
type WorkloadTrace struct {
	Name            string
	Description     string
	InitialReplicas []ReplicaConfig
	ScaleOuts       []ScaleOutEvent
	Requests        []TraceRequest
	BlockSize       int
}

// PolicyResult captures full before/after acceptance metrics for one policy on one trace.
type PolicyResult struct {
	Policy                Policy  `json:"policy"`
	Requests              int     `json:"requests"`
	TotalPromptTokens     uint64  `json:"totalPromptTokens"`
	TotalMatchedTokens    uint64  `json:"totalMatchedTokens"`
	TotalUncachedTokens   uint64  `json:"totalUncachedTokens"`
	CacheReusePercent     float64 `json:"cacheReusePercent"`
	TTFTMinMs             float64 `json:"ttftMinMs"`
	TTFTP50Ms             float64 `json:"ttftP50Ms"`
	TTFTP95Ms             float64 `json:"ttftP95Ms"`
	TTFTP99Ms             float64 `json:"ttftP99Ms"`
	TTFTMaxMs             float64 `json:"ttftMaxMs"`
	TTFTMeanMs            float64 `json:"ttftMeanMs"`
	GPUUtilizationPercent float64 `json:"gpuUtilizationPercent"`
	LoadBalanceJain       float64 `json:"loadBalanceJain"`
	MakespanMs            float64 `json:"makespanMs"`
	ThroughputReqPerSec   float64 `json:"throughputReqPerSec"`
	KVEvictedBlocks       uint64  `json:"kvEvictedBlocks"`
	KVEvictedTokens       uint64  `json:"kvEvictedTokens"`
	HotPrefixReplications int     `json:"hotPrefixReplications"`
	PeakIndexNodes        int     `json:"peakIndexNodes"`
	DecisionP50Us         float64 `json:"decisionP50Us"`
	DecisionP95Us         float64 `json:"decisionP95Us"`
	DecisionP99Us         float64 `json:"decisionP99Us"`
	DecisionMeanUs        float64 `json:"decisionMeanUs"`
}

type simEventKind int

const (
	simEventScaleOut simEventKind = iota
	simEventCompletion
	simEventDecodeStart
	simEventArrival
)

const (
	maxSingleStreamDecodeTokPerSec = 80.0
	decodeTokenEpsilon             = 1e-6
)

type simEvent struct {
	atMs           float64
	kind           simEventKind
	seq            int
	scaleOut       *ScaleOutEvent
	req            *TraceRequest
	tenant         string
	reqID          string
	replicaID      string
	prefillStartMs float64
	outputTokens   int
	gen            uint64
}

type simEventHeap []*simEvent

func (h simEventHeap) Len() int { return len(h) }
func (h simEventHeap) Less(i, j int) bool {
	if h[i].atMs != h[j].atMs {
		return h[i].atMs < h[j].atMs
	}
	// At identical timestamps: scale-out first, then completions (freeing slots & decode
	// bandwidth), then decode starts, then new arrivals.
	if h[i].kind != h[j].kind {
		return h[i].kind < h[j].kind
	}
	return h[i].seq < h[j].seq
}
func (h simEventHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }
func (h *simEventHeap) Push(x any)   { *h = append(*h, x.(*simEvent)) }
func (h *simEventHeap) Pop() any {
	old := *h
	n := len(old)
	item := old[n-1]
	old[n-1] = nil
	*h = old[:n-1]
	return item
}

type timeInterval struct {
	start float64
	end   float64
}

type decodingStream struct {
	tenant          string
	reqID           string
	prefillStartMs  float64
	remainingTokens float64
}

type simReplicaTracker struct {
	id                 string
	cfg                ReplicaConfig
	joinedMs           float64
	intervals          []timeInterval
	placements         uint64
	decoding           []*decodingStream
	lastDecodeUpdateMs float64
	decodeGen          uint64
}

// perStreamDecodeRate returns the per-request decode throughput (tok/s) when active
// concurrent decode streams share a replica's aggregate DecodeTokPerSec budget.
// Each stream is bounded individually by memory-bandwidth speed (~80 tok/s) and
// collectively by aggregateDecodeTokPerSec / active.
func perStreamDecodeRate(active int, aggregateDecodeTokPerSec float64) float64 {
	if active <= 0 || aggregateDecodeTokPerSec <= 0 {
		return 0
	}
	return math.Min(maxSingleStreamDecodeTokPerSec, aggregateDecodeTokPerSec/float64(active))
}

func (rt *simReplicaTracker) advanceDecode(nowMs float64) {
	if len(rt.decoding) > 0 && nowMs > rt.lastDecodeUpdateMs {
		dtSec := (nowMs - rt.lastDecodeUpdateMs) / 1000.0
		rate := perStreamDecodeRate(len(rt.decoding), rt.cfg.DecodeTokPerSec)
		consumed := rate * dtSec
		for _, s := range rt.decoding {
			s.remainingTokens -= consumed
		}
	}
	rt.lastDecodeUpdateMs = nowMs
}

func (rt *simReplicaTracker) scheduleNextCompletion(nowMs float64, pq *simEventHeap, seq *int) {
	rt.decodeGen++
	if len(rt.decoding) == 0 {
		return
	}
	rate := perStreamDecodeRate(len(rt.decoding), rt.cfg.DecodeTokPerSec)
	if rate <= 0 {
		return
	}
	minRem := rt.decoding[0].remainingTokens
	for _, s := range rt.decoding[1:] {
		if s.remainingTokens < minRem {
			minRem = s.remainingTokens
		}
	}
	if minRem < 0 {
		minRem = 0
	}
	nextMs := nowMs + (minRem/rate)*1000.0
	heap.Push(pq, &simEvent{
		atMs:      nextMs,
		kind:      simEventCompletion,
		seq:       *seq,
		replicaID: rt.id,
		gen:       rt.decodeGen,
	})
	*seq++
}

// RunSimulation executes policy against trace in a deterministic discrete-event GPU
// cluster simulator invoking the exact production Scheduler.PlaceAt/CompleteAt methods.
func RunSimulation(trace WorkloadTrace, policy Policy, baseCfg SchedulerConfig) (PolicyResult, error) {
	if len(trace.InitialReplicas) == 0 {
		return PolicyResult{}, fmt.Errorf("%w: trace has no initial replicas", ErrInvalidConfig)
	}
	if len(trace.Requests) == 0 {
		return PolicyResult{}, fmt.Errorf("%w: trace has no requests", ErrInvalidConfig)
	}

	cfg := baseCfg
	cfg.Policy = policy
	if trace.BlockSize > 0 {
		cfg.BlockSize = trace.BlockSize
	}

	epoch := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	msToTime := func(ms float64) time.Time {
		return epoch.Add(time.Duration(ms * float64(time.Millisecond)))
	}

	sched, err := NewScheduler(cfg)
	if err != nil {
		return PolicyResult{}, err
	}

	trackers := make(map[string]*simReplicaTracker, len(trace.InitialReplicas)+len(trace.ScaleOuts))
	for _, rc := range trace.InitialReplicas {
		if err := sched.AddReplicaAt(epoch, rc); err != nil {
			return PolicyResult{}, err
		}
		norm, _ := rc.withDefaults()
		trackers[norm.ID] = &simReplicaTracker{
			id:       norm.ID,
			cfg:      norm,
			joinedMs: 0,
		}
	}

	pq := &simEventHeap{}
	heap.Init(pq)
	seq := 0
	for i := range trace.ScaleOuts {
		ev := &trace.ScaleOuts[i]
		heap.Push(pq, &simEvent{
			atMs:     ev.AtMs,
			kind:     simEventScaleOut,
			seq:      seq,
			scaleOut: ev,
		})
		seq++
	}
	for i := range trace.Requests {
		req := &trace.Requests[i]
		heap.Push(pq, &simEvent{
			atMs: req.ArrivalMs,
			kind: simEventArrival,
			seq:  seq,
			req:  req,
		})
		seq++
	}

	ttfts := make([]float64, 0, len(trace.Requests))
	decLatsUs := make([]float64, 0, len(trace.Requests))
	var totalPrompt, totalMatched, totalUncached uint64
	var hotReplications int
	var peakNodes int
	var makespanMs float64

	completeFinished := func(rt *simReplicaTracker, nowMs float64, now time.Time, forceMin bool) {
		threshold := decodeTokenEpsilon
		if forceMin && len(rt.decoding) > 0 {
			minRem := rt.decoding[0].remainingTokens
			for _, s := range rt.decoding[1:] {
				if s.remainingTokens < minRem {
					minRem = s.remainingTokens
				}
			}
			if minRem > threshold && minRem < 1e-3 {
				threshold = minRem + 1e-9
			}
		}
		remaining := rt.decoding[:0]
		for _, s := range rt.decoding {
			if s.remainingTokens <= threshold {
				_ = sched.CompleteAt(now, s.tenant, s.reqID)
				rt.intervals = append(rt.intervals, timeInterval{start: s.prefillStartMs, end: nowMs})
				if nowMs > makespanMs {
					makespanMs = nowMs
				}
			} else {
				remaining = append(remaining, s)
			}
		}
		rt.decoding = remaining
	}

	for pq.Len() > 0 {
		ev := heap.Pop(pq).(*simEvent)
		now := msToTime(ev.atMs)

		switch ev.kind {
		case simEventScaleOut:
			if err := sched.AddReplicaAt(now, ev.scaleOut.Replica); err != nil {
				return PolicyResult{}, err
			}
			norm, _ := ev.scaleOut.Replica.withDefaults()
			trackers[norm.ID] = &simReplicaTracker{
				id:                 norm.ID,
				cfg:                norm,
				joinedMs:           ev.atMs,
				lastDecodeUpdateMs: ev.atMs,
			}

		case simEventCompletion:
			rt := trackers[ev.replicaID]
			if rt == nil || ev.gen != rt.decodeGen {
				continue
			}
			rt.advanceDecode(ev.atMs)
			completeFinished(rt, ev.atMs, now, true)
			rt.scheduleNextCompletion(ev.atMs, pq, &seq)

		case simEventDecodeStart:
			rt := trackers[ev.replicaID]
			if rt == nil {
				continue
			}
			if ev.outputTokens <= 0 {
				_ = sched.CompleteAt(now, ev.tenant, ev.reqID)
				rt.intervals = append(rt.intervals, timeInterval{start: ev.prefillStartMs, end: ev.atMs})
				if ev.atMs > makespanMs {
					makespanMs = ev.atMs
				}
				continue
			}
			rt.advanceDecode(ev.atMs)
			completeFinished(rt, ev.atMs, now, false)
			rt.decoding = append(rt.decoding, &decodingStream{
				tenant:          ev.tenant,
				reqID:           ev.reqID,
				prefillStartMs:  ev.prefillStartMs,
				remainingTokens: float64(ev.outputTokens),
			})
			rt.scheduleNextCompletion(ev.atMs, pq, &seq)

		case simEventArrival:
			tr := ev.req
			dec, err := sched.PlaceAt(now, PlacementRequest{
				RequestID:    tr.RequestID,
				Tenant:       tr.Tenant,
				ACLScope:     tr.ACLScope,
				TokenIDs:     tr.TokenIDs,
				OutputTokens: tr.OutputTokens,
				Policy:       policy,
			})
			if err != nil {
				return PolicyResult{}, fmt.Errorf("place %s (%s): %w", tr.RequestID, policy, err)
			}

			if nodes := sched.IndexNodeCount(); nodes > peakNodes {
				peakNodes = nodes
			}

			ttftMs := dec.EstimatedTTFTMs
			ttfts = append(ttfts, ttftMs)
			decLatsUs = append(decLatsUs, float64(dec.DecisionLatencyNs)/1000.0)
			totalPrompt += uint64(dec.PromptTokens)
			totalMatched += uint64(dec.MatchedTokens)
			totalUncached += uint64(dec.UncachedTokens)
			if dec.ReplicatedHotPrefix {
				hotReplications++
			}

			rt := trackers[dec.ReplicaID]
			rt.placements++

			prefillStartMs := ev.atMs + dec.EstimatedColdMs + dec.EstimatedQueueMs
			decodeStartMs := ev.atMs + ttftMs
			heap.Push(pq, &simEvent{
				atMs:           decodeStartMs,
				kind:           simEventDecodeStart,
				seq:            seq,
				tenant:         tr.Tenant,
				reqID:          dec.RequestID,
				replicaID:      dec.ReplicaID,
				prefillStartMs: prefillStartMs,
				outputTokens:   tr.OutputTokens,
			})
			seq++
		}
	}

	// Compute exact aggregate KV evictions across all replicas.
	var totalEvictedBlocks, totalEvictedTokens uint64
	for _, st := range sched.ReplicasAt(msToTime(makespanMs)) {
		totalEvictedBlocks += st.EvictedBlocks
		totalEvictedTokens += st.EvictedTokens
	}

	// Compute exact union-of-busy-intervals GPU utilization and Jain's fairness index.
	var totalBusyMs, totalAvailableMs float64
	var sumP, sumP2 float64
	for _, rt := range trackers {
		avail := makespanMs - rt.joinedMs
		if avail <= 0 {
			avail = makespanMs
		}
		totalAvailableMs += avail
		totalBusyMs += mergedIntervalDuration(rt.intervals)
		p := float64(rt.placements)
		sumP += p
		sumP2 += p * p
	}
	gpuUtilPct := 0.0
	if totalAvailableMs > 0 {
		gpuUtilPct = (totalBusyMs / totalAvailableMs) * 100.0
		if gpuUtilPct > 100.0 {
			gpuUtilPct = 100.0
		}
	}
	jain := 0.0
	if len(trackers) > 0 && sumP2 > 0 {
		jain = (sumP * sumP) / (float64(len(trackers)) * sumP2)
	}

	reusePct := 0.0
	if totalPrompt > 0 {
		reusePct = (float64(totalMatched) / float64(totalPrompt)) * 100.0
	}
	throughput := 0.0
	if makespanMs > 0 {
		throughput = float64(len(trace.Requests)) / (makespanMs / 1000.0)
	}

	sort.Float64s(ttfts)
	sort.Float64s(decLatsUs)

	return PolicyResult{
		Policy:                policy,
		Requests:              len(trace.Requests),
		TotalPromptTokens:     totalPrompt,
		TotalMatchedTokens:    totalMatched,
		TotalUncachedTokens:   totalUncached,
		CacheReusePercent:     round2(reusePct),
		TTFTMinMs:             round2(ttfts[0]),
		TTFTP50Ms:             round2(percentile(ttfts, 0.50)),
		TTFTP95Ms:             round2(percentile(ttfts, 0.95)),
		TTFTP99Ms:             round2(percentile(ttfts, 0.99)),
		TTFTMaxMs:             round2(ttfts[len(ttfts)-1]),
		TTFTMeanMs:            round2(meanFloat64(ttfts)),
		GPUUtilizationPercent: round2(gpuUtilPct),
		LoadBalanceJain:       round4(jain),
		MakespanMs:            round2(makespanMs),
		ThroughputReqPerSec:   round2(throughput),
		KVEvictedBlocks:       totalEvictedBlocks,
		KVEvictedTokens:       totalEvictedTokens,
		HotPrefixReplications: hotReplications,
		PeakIndexNodes:        peakNodes,
		DecisionP50Us:         round2(percentile(decLatsUs, 0.50)),
		DecisionP95Us:         round2(percentile(decLatsUs, 0.95)),
		DecisionP99Us:         round2(percentile(decLatsUs, 0.99)),
		DecisionMeanUs:        round2(meanFloat64(decLatsUs)),
	}, nil
}

func mergedIntervalDuration(intervals []timeInterval) float64 {
	if len(intervals) == 0 {
		return 0
	}
	sorted := make([]timeInterval, len(intervals))
	copy(sorted, intervals)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].start != sorted[j].start {
			return sorted[i].start < sorted[j].start
		}
		return sorted[i].end < sorted[j].end
	})
	total := 0.0
	curStart := sorted[0].start
	curEnd := sorted[0].end
	for _, iv := range sorted[1:] {
		if iv.start <= curEnd {
			if iv.end > curEnd {
				curEnd = iv.end
			}
		} else {
			total += curEnd - curStart
			curStart = iv.start
			curEnd = iv.end
		}
	}
	total += curEnd - curStart
	return total
}

func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	if p <= 0 {
		return sorted[0]
	}
	if p >= 1 {
		return sorted[len(sorted)-1]
	}
	idx := int(math.Ceil(p*float64(len(sorted)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

func meanFloat64(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	sum := 0.0
	for _, x := range xs {
		sum += x
	}
	return sum / float64(len(xs))
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }
func round4(v float64) float64 { return math.Round(v*10000) / 10000 }
