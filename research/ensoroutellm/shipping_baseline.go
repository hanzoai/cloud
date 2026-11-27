// Copyright 2026 Hanzo AI, Inc.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package ensoroutellm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"math/rand"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ShippingTask mirrors github.com/hanzoai/ai/router.Task verbatim so the
// benchmark baseline matches the shipping Enso router in hanzoai/ai@7d0b017.
type ShippingTask string

const (
	ShippingTaskCode        ShippingTask = "code"
	ShippingTaskReasoning   ShippingTask = "reasoning"
	ShippingTaskMath        ShippingTask = "math"
	ShippingTaskCreative    ShippingTask = "creative"
	ShippingTaskVision      ShippingTask = "vision"
	ShippingTaskLongContext ShippingTask = "long_context"
	ShippingTaskCheapChat   ShippingTask = "cheap_chat"
	ShippingTaskGeneral     ShippingTask = "general"

	ShippingDefaultTaskKey  = "default"
	ShippingSourceEngine    = "engine"
	ShippingSourceHeuristic = "heuristic"
	ShippingSourceExplore   = "explore"
	ShippingSourceOverride  = "override"
)

// ShippingRequest mirrors github.com/hanzoai/ai/router.Request.
type ShippingRequest struct {
	Text         string       `json:"text"`
	ApproxTokens int          `json:"approx_tokens"`
	HasMedia     bool         `json:"has_media"`
	TaskHint     ShippingTask `json:"task_hint,omitempty"`
}

// ShippingSlo mirrors github.com/hanzoai/ai/router.Slo (USD per 1k tokens & ms).
type ShippingSlo struct {
	MaxCost      float64 `json:"max_cost"`
	MaxLatencyMs int     `json:"max_latency_ms"`
}

// ShippingClassify is a line-for-line parity port of hanzoai/ai/router.Classify
// (which itself mirrors engine/hanzo-router/src/classify.rs).
func ShippingClassify(req ShippingRequest) ShippingTask {
	if req.TaskHint != "" {
		return req.TaskHint
	}
	if req.HasMedia {
		return ShippingTaskVision
	}
	if req.ApproxTokens >= 32000 {
		return ShippingTaskLongContext
	}
	t := strings.ToLower(req.Text)
	has := func(words ...string) bool {
		for _, w := range words {
			if strings.Contains(t, w) {
				return true
			}
		}
		return false
	}
	switch {
	case strings.Contains(t, "```") || has("code", "function", "bug", "compile", "stack trace", "refactor"):
		return ShippingTaskCode
	case has("prove", "theorem", "integral", "equation", "calculate", "solve for"):
		return ShippingTaskMath
	case has("why", "reason", "step by step", "analyze", "explain how", "trade-off"):
		return ShippingTaskReasoning
	case has("write a story", "poem", "creative", "imagine", "brainstorm"):
		return ShippingTaskCreative
	case req.ApproxTokens <= 16:
		return ShippingTaskCheapChat
	default:
		return ShippingTaskGeneral
	}
}

// ShippingPolicy mirrors github.com/hanzoai/ai/router.Policy.
type ShippingPolicy struct {
	Prefer      map[string][]string
	CostCeiling float64
}

// ForTask returns the first model in Prefer[task] accepted by known, falling back
// to Prefer["default"]. Mirrors hanzoai/ai/router.Policy.ForTask.
func (p ShippingPolicy) ForTask(task ShippingTask, known func(string) bool) string {
	if m := p.pick(string(task), known); m != "" {
		return m
	}
	return p.pick(ShippingDefaultTaskKey, known)
}

func (p ShippingPolicy) pick(key string, known func(string) bool) string {
	for _, id := range p.Prefer[key] {
		if known == nil || known(id) {
			return id
		}
	}
	return ""
}

// Candidates returns every servable arm for task in preference order.
func (p ShippingPolicy) Candidates(task ShippingTask, known func(string) bool) []string {
	if c := p.candidates(string(task), known); len(c) > 0 {
		return c
	}
	return p.candidates(ShippingDefaultTaskKey, known)
}

func (p ShippingPolicy) candidates(key string, known func(string) bool) []string {
	var out []string
	for _, id := range p.Prefer[key] {
		if known == nil || known(id) {
			out = append(out, id)
		}
	}
	return out
}

// ShippingStrategy mirrors github.com/hanzoai/ai/router.Strategy.
type ShippingStrategy string

const (
	ShippingStrategyEnso      ShippingStrategy = "enso"
	ShippingStrategyHeuristic ShippingStrategy = "heuristic"
)

// ShippingRoutingPolicy mirrors github.com/hanzoai/ai/router.RoutingPolicy.
type ShippingRoutingPolicy struct {
	Strategy  ShippingStrategy
	Overrides map[string]string
	Enabled   func(string) bool
	Prefer    ShippingPolicy
}

func (rp ShippingRoutingPolicy) overrideModel(task ShippingTask, enabled func(string) bool) (string, bool) {
	for _, key := range [2]string{string(task), ShippingDefaultTaskKey} {
		if m := rp.Overrides[key]; m != "" && (enabled == nil || enabled(m)) {
			return m, true
		}
	}
	return "", false
}

// ShippingDecision mirrors github.com/hanzoai/ai/router.Decision.
type ShippingDecision struct {
	Model      string
	Task       ShippingTask
	Confidence float64
	Source     string
	Features   []float64
}

// EngineRouteRequest mirrors hanzoai/ai/router's POST /route wire request.
type EngineRouteRequest struct {
	Prompt string      `json:"prompt"`
	Tasks  []string    `json:"tasks,omitempty"`
	Slo    ShippingSlo `json:"slo"`
}

// EngineRouteResponse mirrors hanzoai/ai/router's POST /route wire reply.
type EngineRouteResponse struct {
	Model      string    `json:"model"`
	Task       string    `json:"task"`
	Confidence float64   `json:"confidence"`
	Features   []float64 `json:"features,omitempty"`
}

// EngineObserveRequest mirrors hanzoai/ai/router's POST /route/observe payload.
type EngineObserveRequest struct {
	User     string    `json:"user"`
	Org      string    `json:"org"`
	Features []float64 `json:"features"`
	Model    string    `json:"model"`
	Reward   float64   `json:"reward"`
}

// ShippingClient mirrors github.com/hanzoai/ai/router.Client verbatim (override ->
// engine /route with timeout fallback -> explore -> heuristic(Known) -> heuristic(Allow)).
type ShippingClient struct {
	Endpoint string
	Timeout  time.Duration
	Policy   ShippingPolicy
	HTTP     *http.Client
	Known    func(string) bool
	Allow    func(string) bool
	Explore  float64
	Rand     *rand.Rand
	misses   *atomic.Uint64
}

// NewShippingClient constructs a parity client for hanzoai/ai/router with its own
// engine fallback counter (safe under concurrent tests).
func NewShippingClient(policy ShippingPolicy, known, allow func(string) bool) ShippingClient {
	return ShippingClient{
		Timeout: 150 * time.Millisecond,
		Policy:  policy,
		Known:   known,
		Allow:   allow,
		misses:  &atomic.Uint64{},
	}
}

// Misses returns how many engine /route calls fell back to the heuristic.
func (c ShippingClient) Misses() uint64 {
	if c.misses == nil {
		return 0
	}
	return c.misses.Load()
}

// RouteDecision resolves req under the zero RoutingPolicy.
func (c ShippingClient) RouteDecision(ctx context.Context, req ShippingRequest, slo ShippingSlo) ShippingDecision {
	return c.RouteDecisionFor(ctx, req, slo, ShippingRoutingPolicy{})
}

// RouteDecisionFor matches hanzoai/ai/router.Client.RouteDecisionFor precedence:
// override -> engine (if enabled & reachable) -> explore -> heuristic(Known) -> heuristic(Allow).
func (c ShippingClient) RouteDecisionFor(ctx context.Context, req ShippingRequest, slo ShippingSlo, rp ShippingRoutingPolicy) ShippingDecision {
	task := ShippingClassify(req)

	ec := c
	if rp.Enabled != nil {
		base := c.Known
		ec.Known = func(m string) bool { return rp.Enabled(m) && (base == nil || base(m)) }
		ec.Allow = rp.Enabled
	}
	if len(rp.Prefer.Prefer) > 0 {
		ec.Policy = rp.Prefer
	}

	if m, ok := rp.overrideModel(task, ec.Known); ok {
		return ShippingDecision{Model: m, Task: task, Source: ShippingSourceOverride}
	}
	if rp.Strategy != ShippingStrategyHeuristic && ec.Endpoint != "" {
		if d, ok := ec.routeEngine(ctx, req, slo); ok {
			return d
		}
	}
	if ec.Explore > 0 {
		if m, ok := ec.exploreArm(task); ok {
			return ShippingDecision{Model: m, Task: task, Source: ShippingSourceExplore}
		}
	}

	m := ec.Policy.ForTask(task, ec.Known)
	if m == "" {
		m = ec.Policy.ForTask(task, ec.Allow)
	}
	return ShippingDecision{Model: m, Task: task, Source: ShippingSourceHeuristic}
}

func (c ShippingClient) exploreArm(t ShippingTask) (string, bool) {
	f, ni := rand.Float64, rand.Intn
	if c.Rand != nil {
		f, ni = c.Rand.Float64, c.Rand.Intn
	}
	if f() >= c.Explore {
		return "", false
	}
	cands := c.Policy.Candidates(t, c.Known)
	if len(cands) < 2 {
		return "", false
	}
	return cands[1+ni(len(cands)-1)], true
}

func (c ShippingClient) recordMiss() (ShippingDecision, bool) {
	if c.misses != nil {
		c.misses.Add(1)
	}
	return ShippingDecision{}, false
}

func (c ShippingClient) routeEngine(ctx context.Context, req ShippingRequest, slo ShippingSlo) (ShippingDecision, bool) {
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 150 * time.Millisecond
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	body, err := json.Marshal(EngineRouteRequest{Prompt: req.Text, Slo: slo})
	if err != nil {
		return c.recordMiss()
	}
	url := strings.TrimRight(c.Endpoint, "/") + "/route"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return c.recordMiss()
	}
	httpReq.Header.Set("Content-Type", "application/json")
	cli := c.HTTP
	if cli == nil {
		cli = http.DefaultClient
	}
	resp, err := cli.Do(httpReq)
	if err != nil {
		return c.recordMiss()
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return c.recordMiss()
	}
	var out EngineRouteResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&out); err != nil {
		return c.recordMiss()
	}
	if out.Model != "" && (c.Known == nil || c.Known(out.Model)) {
		return ShippingDecision{
			Model:      out.Model,
			Task:       ShippingTask(out.Task),
			Confidence: out.Confidence,
			Source:     ShippingSourceEngine,
			Features:   out.Features,
		}, true
	}
	if out.Task != "" {
		if m := c.Policy.ForTask(ShippingTask(out.Task), c.Known); m != "" {
			return ShippingDecision{
				Model:      m,
				Task:       ShippingTask(out.Task),
				Confidence: out.Confidence,
				Source:     ShippingSourceEngine,
				Features:   out.Features,
			}, true
		}
	}
	return c.recordMiss()
}

// DefaultMaxObservedSamples bounds in-memory /route/observe buffering so long-lived
// shadow adapters cannot leak unbounded memory.
const DefaultMaxObservedSamples = 4096

// EngineAdapter wraps an EnsoRouter to serve hanzoai/ai/router.Client's wire
// contract (POST /route & POST /route/observe) while buffering validated
// content-free online observations in a bounded ring buffer for subsequent
// promote-or-keep retraining.
type EngineAdapter struct {
	router      *EnsoRouter
	defaultOrg  string
	qualityTol  float64
	maxObserved int
	obsSeq      atomic.Uint64
	mu          sync.Mutex
	observed    []TrainingSample
}

// NewEngineAdapter constructs an HTTP-servable adapter for hanzoai/ai/router.Client
// with a bounded in-memory observation buffer (DefaultMaxObservedSamples).
func NewEngineAdapter(r *EnsoRouter, defaultOrg string, qualityTol float64) *EngineAdapter {
	return &EngineAdapter{
		router:      r,
		defaultOrg:  defaultOrg,
		qualityTol:  qualityTol,
		maxObserved: DefaultMaxObservedSamples,
	}
}

// SetMaxObservedSamples configures the bounded ring-buffer capacity for /route/observe
// (0 validates observations without retaining them in memory).
func (a *EngineAdapter) SetMaxObservedSamples(limit int) {
	if limit < 0 {
		limit = 0
	}
	a.mu.Lock()
	a.maxObserved = limit
	if limit == 0 {
		a.observed = nil
	} else if len(a.observed) > limit {
		a.observed = append([]TrainingSample(nil), a.observed[len(a.observed)-limit:]...)
	}
	a.mu.Unlock()
}

// ObservedSamples returns a defensive copy of content-free samples received via POST /route/observe.
func (a *EngineAdapter) ObservedSamples() []TrainingSample {
	a.mu.Lock()
	defer a.mu.Unlock()
	cp := make([]TrainingSample, len(a.observed))
	copy(cp, a.observed)
	return cp
}

// Handler returns the http.Handler serving /route and /route/observe.
func (a *EngineAdapter) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/route", func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var wire EngineRouteRequest
		if err := json.NewDecoder(io.LimitReader(req.Body, 1<<20)).Decode(&wire); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if !isFinite(wire.Slo.MaxCost) || wire.Slo.MaxCost < 0 || wire.Slo.MaxLatencyMs < 0 {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		approxTok := EstimatePromptTokens(wire.Prompt, nil)
		task := ShippingClassify(ShippingRequest{Text: wire.Prompt, ApproxTokens: approxTok})

		rr := RouteRequest{
			Org:                   a.defaultOrg,
			Prompt:                wire.Prompt,
			EstInputTokens:        approxTok,
			EstOutputTokens:       256,
			QualityRetentionRatio: 1.0,
			QualityTolerance:      a.qualityTol,
		}
		if wire.Slo.MaxCost > 0 {
			// hanzoai/ai/router Slo.MaxCost is USD per 1k tokens -> convert to micro-USD
			// with fail-closed floor >= 1 micro-USD (never truncate tiny positive cap to 0!)
			// and MaxInt64 saturation.
			totalToks := float64(rr.EstInputTokens + rr.EstOutputTokens)
			microsFloat := math.Ceil((wire.Slo.MaxCost / 1000.0) * totalToks * 1_000_000.0)
			if !isFinite(microsFloat) || microsFloat >= 9.223372036854774e18 {
				rr.MaxCostMicros = math.MaxInt64
			} else if microsFloat < 1 {
				rr.MaxCostMicros = 1
			} else {
				rr.MaxCostMicros = int64(microsFloat)
			}
		}
		if wire.Slo.MaxLatencyMs > 0 {
			rr.MaxLatencyMs = wire.Slo.MaxLatencyMs
		}

		// Extract features once and pass via PreExtractedFeatures to avoid double work.
		fv := ExtractFeatures(rr)
		rr.PreExtractedFeatures = &fv

		if a.router == nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		dec, err := a.router.Route(rr, StrategyEnsoHybridConstrained)
		if err != nil {
			var re *RouteError
			if errors.As(err, &re) && re.HTTPStatus >= 400 && re.HTTPStatus < 600 {
				w.WriteHeader(re.HTTPStatus)
			} else {
				w.WriteHeader(http.StatusUnprocessableEntity)
			}
			return
		}
		feats := make([]float64, FeatureDim)
		copy(feats, fv[:])
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(EngineRouteResponse{
			Model:      dec.RoutedModel,
			Task:       string(task),
			Confidence: dec.PredictedQuality,
			Features:   feats,
		})
	})
	mux.HandleFunc("/route/observe", func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var obs EngineObserveRequest
		if err := json.NewDecoder(io.LimitReader(req.Body, 1<<16)).Decode(&obs); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if obs.Model == "" || !isFinite(obs.Reward) || obs.Reward < 0 || obs.Reward > 1 || len(obs.Features) != FeatureDim {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var fv FeatureVec
		for i := 0; i < FeatureDim; i++ {
			if !isFinite(obs.Features[i]) || math.Abs(obs.Features[i]) > 1e6 {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			fv[i] = obs.Features[i]
		}
		seq := a.obsSeq.Add(1)
		a.mu.Lock()
		if a.maxObserved > 0 {
			sample := TrainingSample{
				QueryID:  obs.Org + ":" + obs.User + ":" + time.Now().UTC().Format("20060102T150405.000000000") + ":" + strconv.FormatUint(seq, 10),
				Features: fv,
				ModelID:  obs.Model,
				Passed:   obs.Reward,
			}
			if len(a.observed) >= a.maxObserved {
				copy(a.observed, a.observed[1:])
				a.observed[len(a.observed)-1] = sample
			} else {
				a.observed = append(a.observed, sample)
			}
		}
		a.mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
	})
	return mux
}

// NewEnsoEngineHTTPHandler constructs a stateless http.Handler backed by
// EngineAdapter (validates /route/observe without retaining unreachable samples
// in memory; callers that want to drain observations should use NewEngineAdapter).
func NewEnsoEngineHTTPHandler(r *EnsoRouter, defaultOrg string, qualityTol float64) http.Handler {
	a := NewEngineAdapter(r, defaultOrg, qualityTol)
	a.SetMaxObservedSamples(0)
	return a.Handler()
}

// PoolAdaptedShippingPolicy builds a non-strawman shipping heuristic policy over
// any servable evaluation catalog (LiveBench, Zen6, or private enso-bench) by
// keeping canonical conf/models.yaml prefer lists first and dynamically appending
// servable catalog arms ordered by task role and blended price so every task
// bucket resolves cleanly to an eligible specialist in arms.
func PoolAdaptedShippingPolicy(cfg *HanzoModelsConfig, arms []ModelArm) ShippingPolicy {
	if cfg == nil {
		cfg = &HanzoModelsConfig{}
	}
	prefer := make(map[string][]string, len(cfg.Router.Prefer)+2)
	for k, list := range cfg.Router.Prefer {
		prefer[k] = append([]string(nil), list...)
	}

	// Sort arms cheapest -> most expensive for deterministic dynamic fallback lists.
	byPrice := make([]ModelArm, len(arms))
	copy(byPrice, arms)
	sort.Slice(byPrice, func(i, j int) bool {
		bi, bj := byPrice[i].BlendedUSDPerMTok(), byPrice[j].BlendedUSDPerMTok()
		if bi != bj {
			return bi < bj
		}
		return byPrice[i].ID < byPrice[j].ID
	})

	poolSet := make(map[string]bool, len(arms))
	var cheapToExp, expToCheap, midTier, visionArms, reasoningArms []string
	for _, a := range byPrice {
		poolSet[a.ID] = true
		cheapToExp = append(cheapToExp, a.ID)
		if a.SupportsVision {
			visionArms = append(visionArms, a.ID)
		}
		if a.ThinkingFamily != "" || a.MandatoryThinking {
			reasoningArms = append(reasoningArms, a.ID)
		}
	}
	for i := len(byPrice) - 1; i >= 0; i-- {
		expToCheap = append(expToCheap, byPrice[i].ID)
	}
	// Mid-to-upper tier starts near the median arm so default/creative does not
	// collapse onto either the weakest or the most expensive arm.
	if len(byPrice) > 0 {
		mid := len(byPrice) / 2
		for i := mid; i < len(byPrice); i++ {
			midTier = append(midTier, byPrice[i].ID)
		}
		for i := mid - 1; i >= 0; i-- {
			midTier = append(midTier, byPrice[i].ID)
		}
	}

	appendUnique := func(task string, extras ...string) {
		seen := make(map[string]bool, len(prefer[task]))
		for _, id := range prefer[task] {
			seen[id] = true
		}
		for _, id := range extras {
			if poolSet[id] && !seen[id] {
				prefer[task] = append(prefer[task], id)
				seen[id] = true
			}
		}
	}

	// Known specialist hints if present, followed by catalog-agnostic dynamic orderings.
	appendUnique("code", "claude-3-7-sonnet", "fireworks/deepseek-v3", "o3-mini", "gpt-4o")
	appendUnique("code", midTier...)
	appendUnique("reasoning", "o3-mini", "fireworks/deepseek-r1", "claude-3-7-sonnet", "o1")
	appendUnique("reasoning", reasoningArms...)
	appendUnique("reasoning", expToCheap...)
	appendUnique("math", "o3-mini", "fireworks/deepseek-r1", "o1", "fireworks/deepseek-v3")
	appendUnique("math", reasoningArms...)
	appendUnique("math", expToCheap...)
	appendUnique("creative", "claude-3-7-sonnet", "gpt-4o", "fireworks/deepseek-v3")
	appendUnique("creative", midTier...)
	appendUnique("vision", visionArms...)
	appendUnique("vision", midTier...)
	appendUnique("long_context", "claude-3-7-sonnet", "gpt-4o", "fireworks/deepseek-v3")
	appendUnique("long_context", midTier...)
	appendUnique("cheap_chat", "gpt-4o-mini", "mistral-small", "claude-3-5-haiku")
	appendUnique("cheap_chat", cheapToExp...)
	appendUnique("default", "gpt-4o", "claude-3-7-sonnet", "gpt-4o-mini", "mistral-small")
	appendUnique("default", midTier...)

	return ShippingPolicy{
		Prefer:      prefer,
		CostCeiling: cfg.Router.CostCeiling,
	}
}
