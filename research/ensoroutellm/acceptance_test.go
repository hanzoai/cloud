// Copyright 2026 Hanzo AI, Inc.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package ensoroutellm

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestLoadHanzoModelsYAMLCanonicalSnapshot(t *testing.T) {
	cfg, err := LoadHanzoModelsYAMLFile(filepath.Join("testdata", "hanzo_models_snapshot.yaml"))
	if err != nil {
		t.Fatalf("LoadHanzoModelsYAMLFile: %v", err)
	}
	if cfg.Version != 1 {
		t.Fatalf("version = %d, want 1", cfg.Version)
	}
	if !cfg.Router.Enabled {
		t.Fatalf("expected router.enabled=true in canonical models.yaml")
	}
	for _, key := range []string{"code", "reasoning", "math", "creative", "vision", "long_context", "cheap_chat", "default"} {
		if len(cfg.Router.Prefer[key]) == 0 {
			t.Fatalf("missing canonical router.prefer[%q]", key)
		}
	}
	if cfg.Router.Prefer["code"][0] != "zen6-coder" {
		t.Fatalf("router.prefer[code][0] = %q, want zen6-coder", cfg.Router.Prefer["code"][0])
	}
	if haiku, ok := cfg.Models["claude-3-5-haiku"]; !ok || haiku.Pricing == nil || haiku.Pricing.InputPerMillion != 0.80 || haiku.Pricing.OutputPerMillion != 4.00 {
		t.Fatalf("unexpected claude-3-5-haiku entry: %+v", haiku)
	}
	if sonnet, ok := cfg.Models["claude-3-7-sonnet"]; !ok || len(sonnet.Fallbacks) == 0 || sonnet.Fallbacks[0].Provider != "do-ai" {
		t.Fatalf("expected do-ai fallback on claude-3-7-sonnet, got %+v", sonnet)
	}

	snap, err := LoadPricingSnapshotFile(filepath.Join("testdata", "hanzo_pricing_snapshot.json"))
	if err != nil {
		t.Fatalf("LoadPricingSnapshotFile: %v", err)
	}
	arms, err := BuildCatalogArms(cfg, snap, nil)
	if err != nil {
		t.Fatalf("BuildCatalogArms: %v", err)
	}
	if len(arms) < 10 {
		t.Fatalf("expected >= 10 chat arms, got %d", len(arms))
	}
	for _, a := range arms {
		if a.ID == "kai" || a.ID == "zen-scribe" || a.ID == "text-embedding-3-small" || a.ID == "wan2-2-t2v-a14b" || a.ID == "fireworks/qwen3-4b" || strings.HasPrefix(a.ID, "router:") {
			t.Fatalf("non-chat, hidden, meta-router, or pricing_only model %q leaked into default chat catalog", a.ID)
		}
	}

	// Explicitly requested pricing_only chat models MUST remain usable with their authoritative YAML pricing.
	poArms, err := BuildCatalogArms(cfg, nil, []string{"fireworks/glm-5-thinking", "mistral-small"})
	if err != nil {
		t.Fatalf("BuildCatalogArms pricing_only requested: %v", err)
	}
	if len(poArms) != 2 {
		t.Fatalf("expected 2 requested chat arms including pricing_only fireworks/glm-5-thinking, got %d", len(poArms))
	}
}

func TestModelsYAMLParserHardeningAndFailClosedDefaults(t *testing.T) {
	// 1. Indented fallbacks, block lists for prefer & outputs, $0/$0 free model, fail-closed capabilities.
	yml := `
version: 1
default_pricing:
  input_per_million: 1.00
  output_per_million: 4.00
router:
  enabled: true
  prefer:
    code:
      - free-coder
      - paid-coder
    default:
      - free-coder
models:
  free-coder:
    provider: hanzo-zen
    upstream: free-coder
    pricing: {input:0.0, output:0.0}
    fallbacks:
      - provider: backup-p
        upstream: free-coder-b
  paid-coder:
    provider: hanzo-zen
    upstream: paid-coder
    tools: true
    zero_retention: true
    pricing:
      input: 2.00
      output: 6.00
  decision-only:
    provider: kai
    upstream: kai
    outputs:
      - decision
`
	cfg, err := LoadHanzoModelsYAML([]byte(yml))
	if err != nil {
		t.Fatalf("LoadHanzoModelsYAML valid block YAML failed: %v", err)
	}
	if len(cfg.Router.Prefer["code"]) != 2 || cfg.Router.Prefer["code"][0] != "free-coder" {
		t.Fatalf("block router.prefer not parsed: %+v", cfg.Router.Prefer)
	}
	if len(cfg.Models["free-coder"].Fallbacks) != 1 || cfg.Models["free-coder"].Fallbacks[0].Provider != "backup-p" {
		t.Fatalf("indented fallback not parsed: %+v", cfg.Models["free-coder"].Fallbacks)
	}
	arms, err := BuildCatalogArms(cfg, nil, nil)
	if err != nil {
		t.Fatalf("BuildCatalogArms: %v", err)
	}
	if len(arms) != 2 {
		t.Fatalf("expected 2 chat arms (decision-only excluded), got %d", len(arms))
	}
	// free-coder must preserve explicit $0/$0 pricing and fail-closed ZeroRetention=false & Tools=false.
	if arms[0].ID != "free-coder" || arms[0].InputUSDPerMTok != 0 || arms[0].OutputUSDPerMTok != 0 || arms[0].PriceSource != "models_yaml" {
		t.Fatalf("explicit $0/$0 free model overwritten: %+v", arms[0])
	}
	if arms[0].ZeroRetention || arms[0].SupportsForcedToolChoice || arms[0].SupportsVision {
		t.Fatalf("undeclared capabilities must default fail-closed (false), got %+v", arms[0])
	}
	if arms[1].ID != "paid-coder" || !arms[1].ZeroRetention || !arms[1].SupportsForcedToolChoice {
		t.Fatalf("explicitly declared capabilities not honored: %+v", arms[1])
	}

	// 2. Reject negative, NaN, Inf, malformed, or empty YAML.
	for _, bad := range []string{
		"",
		"not: valid: only",
		"version: 1\nmodels:\n  m1:\n    pricing: {input: -1.0, output: 2.0}\n",
		"version: 1\nmodels:\n  m1:\n    pricing: {input: NaN, output: 2.0}\n",
		"version: 1\nmodels:\n  m1:\n    pricing: {input: Inf, output: 2.0}\n",
		"version: 1\nmodels:\n  m1:\n    pricing: {input: bogus, output: 2.0}\n",
	} {
		if _, err := LoadHanzoModelsYAML([]byte(bad)); err == nil {
			t.Errorf("expected error for invalid YAML %q, got nil", bad)
		}
	}
}

func TestShippingBaselineParityAndEngineHTTPAdapter(t *testing.T) {
	// Mirrors all 15 cases from hanzoai/ai/router/classify_test.go.
	cases := []struct {
		name string
		req  ShippingRequest
		want ShippingTask
	}{
		{"task hint wins", ShippingRequest{Text: "write a poem", TaskHint: ShippingTaskMath}, ShippingTaskMath},
		{"media implies vision", ShippingRequest{Text: "what is this", HasMedia: true}, ShippingTaskVision},
		{"long context by tokens", ShippingRequest{Text: "summarize", ApproxTokens: 40000}, ShippingTaskLongContext},
		{"code fence", ShippingRequest{Text: "```go\nfmt.Println()\n```", ApproxTokens: 50}, ShippingTaskCode},
		{"code keyword", ShippingRequest{Text: "please refactor this function", ApproxTokens: 50}, ShippingTaskCode},
		{"code bug keyword", ShippingRequest{Text: "there is a bug in the compile step", ApproxTokens: 50}, ShippingTaskCode},
		{"math keyword", ShippingRequest{Text: "prove this theorem about the integral", ApproxTokens: 50}, ShippingTaskMath},
		{"math solve for", ShippingRequest{Text: "solve for x in this equation", ApproxTokens: 50}, ShippingTaskMath},
		{"reasoning keyword", ShippingRequest{Text: "explain how this works step by step", ApproxTokens: 50}, ShippingTaskReasoning},
		{"reasoning why", ShippingRequest{Text: "why does the trade-off matter", ApproxTokens: 50}, ShippingTaskReasoning},
		{"creative keyword", ShippingRequest{Text: "write a story about a dragon", ApproxTokens: 50}, ShippingTaskCreative},
		{"creative brainstorm", ShippingRequest{Text: "brainstorm some names", ApproxTokens: 50}, ShippingTaskCreative},
		{"cheap chat short", ShippingRequest{Text: "hi", ApproxTokens: 2}, ShippingTaskCheapChat},
		{"general fallback", ShippingRequest{Text: "tell me about the weather today please", ApproxTokens: 100}, ShippingTaskGeneral},
		{"code before math", ShippingRequest{Text: "there is a bug, prove the theorem", ApproxTokens: 50}, ShippingTaskCode},
	}
	for _, tc := range cases {
		if got := ShippingClassify(tc.req); got != tc.want {
			t.Errorf("%s: ShippingClassify = %q, want %q", tc.name, got, tc.want)
		}
	}

	// Mirrors TestLastResortRespectsAllowlist from hanzoai/ai/router/routing_policy_test.go.
	cli := NewShippingClient(
		ShippingPolicy{Prefer: map[string][]string{ShippingDefaultTaskKey: {"dear", "cheap"}}},
		func(string) bool { return false },
		func(id string) bool { return id == "cheap" },
	)
	if d := cli.RouteDecision(context.Background(), ShippingRequest{Text: "hi"}, ShippingSlo{}); d.Model != "cheap" {
		t.Fatalf("last resort routed to %q, want cheap", d.Model)
	}

	// Verify EngineAdapter: tiny positive Slo.MaxCost must fail-closed (never truncate to 0/unlimited),
	// negative Slo.MaxCost returns 400, and /route/observe buffers validated observations.
	cat := DefaultCatalog()
	r := NewEnsoRouter(cat, nil)
	r.SetOrgPolicy("org-http", ScopePolicy{PlanTier: PlanEnterprise, HasPlanTier: true})
	adapter := NewEngineAdapter(r, "org-http", 0.0)
	srv := httptest.NewServer(adapter.Handler())
	defer srv.Close()

	// Tiny positive MaxCost ($1e-9 / 1k tokens) -> rounds up to 1 micro-USD -> rejects 422 instead of truncating to 0!
	respTiny, err := http.Post(srv.URL+"/route", "application/json", bytes.NewBufferString(`{"prompt":"hello world","slo":{"max_cost":1e-9}}`))
	if err != nil {
		t.Fatal(err)
	}
	_ = respTiny.Body.Close()
	if respTiny.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("tiny positive Slo.MaxCost status = %d, want 422 (fail-closed, not truncated to unlimited)", respTiny.StatusCode)
	}

	respNeg, err := http.Post(srv.URL+"/route", "application/json", bytes.NewBufferString(`{"prompt":"hello","slo":{"max_cost":-1}}`))
	if err != nil {
		t.Fatal(err)
	}
	_ = respNeg.Body.Close()
	if respNeg.StatusCode != http.StatusBadRequest {
		t.Fatalf("negative Slo.MaxCost status = %d, want 400", respNeg.StatusCode)
	}

	// Valid /route/observe records content-free observation; wrong dim rejects 400.
	validFeats := make([]float64, FeatureDim)
	validFeats[FeatureDim-1] = 1.0
	obsBody, _ := json.Marshal(EngineObserveRequest{User: "u1", Org: "org-http", Features: validFeats, Model: "zen-4-flash", Reward: 1.0})
	respObs, err := http.Post(srv.URL+"/route/observe", "application/json", bytes.NewReader(obsBody))
	if err != nil {
		t.Fatal(err)
	}
	_ = respObs.Body.Close()
	if respObs.StatusCode != http.StatusAccepted || len(adapter.ObservedSamples()) != 1 {
		t.Fatalf("expected 202 and 1 buffered sample, got status=%d len=%d", respObs.StatusCode, len(adapter.ObservedSamples()))
	}

	badObs, _ := json.Marshal(EngineObserveRequest{User: "u1", Org: "org-http", Features: []float64{1.0}, Model: "zen-4-flash", Reward: 1.0})
	respBadObs, err := http.Post(srv.URL+"/route/observe", "application/json", bytes.NewReader(badObs))
	if err != nil {
		t.Fatal(err)
	}
	_ = respBadObs.Body.Close()
	if respBadObs.StatusCode != http.StatusBadRequest {
		t.Fatalf("wrong feature dim status = %d, want 400", respBadObs.StatusCode)
	}

	// Verify ShippingClient HTTP /route delegation + 503 heuristic fallback.
	var hits int32
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&hits, 1) == 1 {
			_ = json.NewEncoder(w).Encode(EngineRouteResponse{Model: "cheap", Task: "code", Confidence: 0.91})
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer stub.Close()

	cli2 := NewShippingClient(
		ShippingPolicy{Prefer: map[string][]string{"code": {"fallback-coder"}}},
		func(id string) bool { return id == "cheap" || id == "fallback-coder" },
		nil,
	)
	cli2.Endpoint = stub.URL
	cli2.Timeout = 100 * time.Millisecond

	d1 := cli2.RouteDecision(context.Background(), ShippingRequest{Text: "refactor function", ApproxTokens: 40}, ShippingSlo{})
	if d1.Model != "cheap" || d1.Source != ShippingSourceEngine {
		t.Fatalf("expected engine route, got %+v", d1)
	}
	d2 := cli2.RouteDecision(context.Background(), ShippingRequest{Text: "refactor function", ApproxTokens: 40}, ShippingSlo{})
	if d2.Model != "fallback-coder" || d2.Source != ShippingSourceHeuristic || cli2.Misses() != 1 {
		t.Fatalf("expected heuristic fallback on 503, got %+v misses=%d", d2, cli2.Misses())
	}
}

func TestAggregateOnlyReceiptContainsNoPrivateDataAndSupportsCustomPrivateCatalogs(t *testing.T) {
	tmpDir := t.TempDir()
	privateCorpus := filepath.Join(tmpDir, "private_enso_bench_canary.jsonl")
	canaryPrompt := "CANARY_SECRET_CUSTOMER_PROMPT_9918273645"
	canaryQID := "CANARY_PRIVATE_QUERY_ID_9918273645"
	canaryModel := "CANARY_PRIVATE_MODEL_ID_9918273645"

	var lines []string
	for i := 0; i < 20; i++ {
		cat := "code"
		prompt := canaryPrompt + " implement Go mutex function"
		if i%2 == 1 {
			cat = "creative"
			prompt = canaryPrompt + " write a story about distributed systems"
		}
		rec := EvalCorpusRecord{
			QueryID:         canaryQID + "_" + string(rune('a'+i)),
			Dataset:         "private_canary",
			Category:        cat,
			Task:            "internal",
			Prompt:          prompt,
			EstInputTokens:  64,
			EstOutputTokens: 128,
			ArmOutcomes: map[string]float64{
				"zen6-flash": 0.25,
				"zen6-coder": 0.85,
				canaryModel:  0.95,
			},
		}
		b, err := json.Marshal(rec)
		if err != nil {
			t.Fatal(err)
		}
		lines = append(lines, string(b))
	}
	if err := os.WriteFile(privateCorpus, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	rep, err := RunAcceptanceBenchmark(AcceptanceConfig{
		ModelsYAMLPath:     filepath.Join("testdata", "hanzo_models_snapshot.yaml"),
		CorpusPath:         privateCorpus,
		Seed:               DefaultEvalSeed,
		CVFolds:            4,
		PerfIterations:     200,
		BootstrapResamples: 100,
	})
	if err != nil {
		t.Fatalf("RunAcceptanceBenchmark on private Zen6/canary corpus failed: %v", err)
	}
	if !rep.Reliability.AllPassed {
		t.Fatalf("expected all catalog-agnostic reliability checks to pass on private catalog, got %+v", rep.Reliability)
	}
	// Pool-adapted shipping baseline must route 100% of queries on arbitrary private Zen6/canary models (0 unrouted!).
	for _, pm := range rep.Policies {
		if pm.PolicyName == "hanzo_ai_router_pool_adapted" && pm.UnroutedQueries != 0 {
			t.Fatalf("expected 0 unrouted queries for pool-adapted baseline on private catalog, got %d", pm.UnroutedQueries)
		}
	}

	raw, err := MarshalAcceptanceJSON(rep)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	if !rep.AggregateOnly {
		t.Fatalf("expected aggregate_only=true")
	}
	if strings.Contains(s, canaryPrompt) || strings.Contains(s, canaryQID) || strings.Contains(s, canaryModel) {
		t.Fatalf("private prompt, query_id, or internal model_id leaked into aggregate-only receipt!")
	}
}

func TestLiveBenchPublicEvaluationAcceptance(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping full LiveBench 5-fold acceptance evaluation in -short mode")
	}
	cfg := AcceptanceConfig{
		ModelsYAMLPath:      filepath.Join("testdata", "hanzo_models_snapshot.yaml"),
		PricingSnapshotPath: filepath.Join("testdata", "hanzo_pricing_snapshot.json"),
		CorpusPath:          filepath.Join("testdata", "livebench_public_features.jsonl"),
		Seed:                DefaultEvalSeed,
		CVFolds:             5,
		PerfIterations:      500,
		BootstrapResamples:  500,
	}
	rep, err := RunAcceptanceBenchmark(cfg)
	if err != nil {
		t.Fatalf("RunAcceptanceBenchmark: %v", err)
	}
	if !rep.AggregateOnly || rep.CorpusSize != 318 || rep.Catalog.ActiveArms != 11 {
		t.Fatalf("unexpected report header: agg=%v n=%d arms=%d", rep.AggregateOnly, rep.CorpusSize, rep.Catalog.ActiveArms)
	}
	if rep.Calibration.ECE10Bin > 0.05 {
		t.Fatalf("expected calibrated ECE <= 0.05, got %.4f", rep.Calibration.ECE10Bin)
	}
	if rep.Calibration.APGR < 0.80 {
		t.Fatalf("expected APGR >= 0.80 on LiveBench, got %.4f", rep.Calibration.APGR)
	}
	// Verify tol=0.00 executes distinct max-quality routing (NOT silently aliased to tol=0.04!).
	if len(rep.ParetoFrontier) < 5 {
		t.Fatalf("expected >= 5 Pareto points, got %d", len(rep.ParetoFrontier))
	}
	if rep.ParetoFrontier[0].USDPer1kQueries <= rep.ParetoFrontier[4].USDPer1kQueries {
		t.Fatalf("expected tol=0.00 spend (%.4f) > tol=0.04 spend (%.4f)", rep.ParetoFrontier[0].USDPer1kQueries, rep.ParetoFrontier[4].USDPer1kQueries)
	}
	if !rep.MatchedBudgetCanonical.BudgetMatched || rep.MatchedBudgetCanonical.AccuracyDeltaPP <= 0 || rep.MatchedBudgetCanonical.AccuracyDeltaCI95PP[0] <= 0 {
		t.Fatalf("expected significant nested matched-budget win over canonical shipping router, got %+v", rep.MatchedBudgetCanonical)
	}
	if !rep.MatchedBudgetAdapted.BudgetMatched || rep.MatchedBudgetAdapted.AccuracyDeltaPP <= 0 {
		t.Fatalf("expected nested matched-budget win over pool-adapted shipping router, got %+v", rep.MatchedBudgetAdapted)
	}
	if !rep.Reliability.AllPassed || rep.Reliability.TotalChecks < 14 {
		t.Fatalf("expected all reliability checks to pass, got %d/%d", rep.Reliability.PassedChecks, rep.Reliability.TotalChecks)
	}
	if rep.Performance.CandidateHybrid.P95Ns <= 0 || rep.Performance.CandidateHybrid.ThroughputQPS <= 0 || rep.Performance.PromptLengthMedianBytes < 1000 {
		t.Fatalf("invalid live performance metrics: %+v", rep.Performance)
	}

	// Verify deterministic fields match committed testdata/acceptance_results.json exactly.
	committedBytes, err := os.ReadFile(filepath.Join("testdata", "acceptance_results.json"))
	if err != nil {
		t.Fatalf("read committed acceptance_results.json: %v", err)
	}
	var committed AcceptanceReport
	if err := json.Unmarshal(committedBytes, &committed); err != nil {
		t.Fatalf("unmarshal committed acceptance_results.json: %v", err)
	}
	repDet := rep
	committedDet := committed
	repDet.Performance = PerformanceComparison{}
	committedDet.Performance = PerformanceComparison{}
	a, _ := MarshalAcceptanceJSON(repDet)
	b, _ := MarshalAcceptanceJSON(committedDet)
	if string(a) != string(b) {
		t.Fatalf("committed testdata/acceptance_results.json drifted from live deterministic evaluation")
	}
}

func TestModelsYAMLRegressionHardeningRound2(t *testing.T) {
	// P1-1: empty pricing: {} or COGS-only pricing must NOT turn a paid model into $0/$0.
	// P1-2: same-indent block lists under router.prefer.<task> must be parsed.
	// P1-3: hidden and pricing_only entries must remain excluded on default discovery even if present in snap.
	raw := `version: 1
default_pricing:
  input_per_million: 2.00
  output_per_million: 6.00
router:
  enabled: true
  prefer:
    code:
    - paid-empty-price
    - paid-cogs-only
models:
  paid-empty-price:
    provider: custom
    pricing: {}
  paid-cogs-only:
    provider: custom
    pricing:
      cost_in_per_million: 0.40
      cost_out_per_million: 0.80
  secret-hidden:
    provider: custom
    hidden: true
    pricing: {input: 1.0, output: 2.0}
  legacy-billing-only:
    provider: custom
    pricing_only: true
    pricing: {input: 1.0, output: 2.0}
`
	cfg, err := LoadHanzoModelsYAML([]byte(raw))
	if err != nil {
		t.Fatalf("LoadHanzoModelsYAML: %v", err)
	}
	if len(cfg.Router.Prefer["code"]) != 2 || cfg.Router.Prefer["code"][0] != "paid-empty-price" {
		t.Fatalf("same-indent router.prefer block list dropped: %+v", cfg.Router.Prefer["code"])
	}
	snap := &PricingSnapshot{
		Models: map[string]PricingSnapshotEntry{
			"secret-hidden":       {InputPerMillion: 1.0, OutputPerMillion: 2.0},
			"legacy-billing-only": {InputPerMillion: 1.0, OutputPerMillion: 2.0},
		},
	}
	arms, err := BuildCatalogArms(cfg, snap, nil)
	if err != nil {
		t.Fatalf("BuildCatalogArms: %v", err)
	}
	if len(arms) != 2 {
		t.Fatalf("expected 2 non-hidden chat arms, got %d: %+v", len(arms), arms)
	}
	for _, a := range arms {
		if a.ID == "secret-hidden" || a.ID == "legacy-billing-only" {
			t.Fatalf("hidden/pricing_only model %q leaked through snapshot merge", a.ID)
		}
		if a.InputUSDPerMTok != 2.00 || a.OutputUSDPerMTok != 6.00 {
			t.Fatalf("model %s should fall back to default_pricing (2/6), got (%.2f/%.2f)", a.ID, a.InputUSDPerMTok, a.OutputUSDPerMTok)
		}
	}

	// Negative context_window must return an explicit parse error.
	badCtx := `version: 1
models:
  bad-ctx:
    provider: custom
    context_window: -128
`
	if _, err := LoadHanzoModelsYAML([]byte(badCtx)); err == nil {
		t.Fatalf("expected error on negative context_window")
	}

	// LF vs CRLF must yield identical normalized SHA-256 digest.
	crlf := strings.ReplaceAll(raw, "\n", "\r\n")
	cfgCRLF, err := LoadHanzoModelsYAML([]byte(crlf))
	if err != nil {
		t.Fatalf("LoadHanzoModelsYAML(CRLF): %v", err)
	}
	if cfg.SHA256 != cfgCRLF.SHA256 {
		t.Fatalf("EOL drift in models.yaml SHA256: LF=%s CRLF=%s", cfg.SHA256, cfgCRLF.SHA256)
	}
}

func TestReliabilityFreeArmAndEngineAdapterBoundsAndLargeFeatures(t *testing.T) {
	// P1-4: Catalog containing a $0/$0 free arm alongside paid arms must pass 14/14 reliability checks.
	raw := `version: 1
router:
  enabled: true
  prefer:
    default: ["free-arm", "paid-arm"]
models:
  free-arm:
    provider: zen
    pricing: {input: 0.0, output: 0.0}
    context_window: 128000
    tools: true
  paid-arm:
    provider: zen
    pricing: {input: 3.0, output: 12.0}
    context_window: 200000
    vision: true
    tools: true
`
	cfg, err := LoadHanzoModelsYAML([]byte(raw))
	if err != nil {
		t.Fatalf("LoadHanzoModelsYAML: %v", err)
	}
	arms, err := BuildCatalogArms(cfg, nil, nil)
	if err != nil {
		t.Fatalf("BuildCatalogArms: %v", err)
	}

	// P1-8: FitTrainedWeights must not panic on large finite features (1e5).
	var bigFV FeatureVec
	for d := 0; d < FeatureDim; d++ {
		bigFV[d] = 1e5
	}
	largeSamples := []TrainingSample{
		{QueryID: "q1", Features: bigFV, ModelID: "free-arm", Passed: 0.0},
		{QueryID: "q2", Features: bigFV, ModelID: "paid-arm", Passed: 1.0},
		{QueryID: "q3", Features: bigFV, ModelID: "free-arm", Passed: 1.0},
		{QueryID: "q4", Features: bigFV, ModelID: "paid-arm", Passed: 1.0},
	}
	tw := FitTrainedWeights(largeSamples, arms, DefaultEvalSeed)
	if tw == nil || tw.RouterModelRevision == "" {
		t.Fatalf("FitTrainedWeights failed on large finite features")
	}

	// Verify 14/14 reliability checks pass with a $0/$0 free arm in the catalog.
	rel := RunReliabilityAudit(cfg, arms, tw)
	if !rel.AllPassed || rel.TotalChecks < 14 {
		t.Fatalf("RunReliabilityAudit failed on catalog with $0/$0 free arm: %+v", rel)
	}

	// P2-6: NaN in PreExtractedFeatures must be rejected with HTTP 400.
	r := NewEnsoRouter(arms, tw)
	r.SetOrgPolicy("org-test", ScopePolicy{PlanTier: PlanEnterprise, HasPlanTier: true})
	var nanFV FeatureVec
	nanFV[0] = math.NaN()
	_, err = r.Route(RouteRequest{
		Org:                  "org-test",
		Prompt:               "hello",
		PreExtractedFeatures: &nanFV,
	}, StrategyEnsoHybridConstrained)
	if !IsRouteErrorCode(err, ErrCodeInvalidRequestParams) {
		t.Fatalf("expected 400 ErrCodeInvalidRequestParams for NaN PreExtractedFeatures, got %v", err)
	}

	// P1-7: EngineAdapter bounded ring buffer and unique QueryIDs across rapid /route/observe calls.
	adapter := NewEngineAdapter(r, "org-test", 0.04)
	adapter.SetMaxObservedSamples(3)
	srv := httptest.NewServer(adapter.Handler())
	defer srv.Close()
	validObs, _ := json.Marshal(EngineObserveRequest{
		Model: "paid-arm", Reward: 1.0, Features: make([]float64, FeatureDim), Org: "org-test", User: "u1",
	})
	for i := 0; i < 5; i++ {
		resp, err := http.Post(srv.URL+"/route/observe", "application/json", bytes.NewReader(validObs))
		if err != nil {
			t.Fatalf("POST /route/observe[%d]: %v", i, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusAccepted {
			t.Fatalf("status[%d] = %d, want 202", i, resp.StatusCode)
		}
	}
	gotObs := adapter.ObservedSamples()
	if len(gotObs) != 3 {
		t.Fatalf("bounded ring buffer expected 3 samples, got %d", len(gotObs))
	}
	seenQIDs := make(map[string]bool)
	for _, s := range gotObs {
		if seenQIDs[s.QueryID] {
			t.Fatalf("duplicate QueryID across rapid /route/observe calls: %q", s.QueryID)
		}
		seenQIDs[s.QueryID] = true
	}

	// P1-9: ConvertRawCorpusToContentFreeJSONL must reject already content-free corpora.
	outTmp := filepath.Join(t.TempDir(), "should_fail.jsonl")
	if _, _, err := ConvertRawCorpusToContentFreeJSONL(filepath.Join("testdata", "livebench_public_features.jsonl"), outTmp); err == nil {
		t.Fatalf("expected error when converting an already content-free JSONL")
	}
}

func TestAcceptanceSchemaAndManifestContract(t *testing.T) {
	// Validate acceptance_schema.json required keys against committed acceptance_results.json.
	schemaBytes, err := os.ReadFile(filepath.Join("results", "acceptance_schema.json"))
	if err != nil {
		t.Fatalf("read acceptance_schema.json: %v", err)
	}
	var schema struct {
		Required   []string                   `json:"required"`
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(schemaBytes, &schema); err != nil {
		t.Fatalf("parse acceptance_schema.json: %v", err)
	}

	receiptBytes, err := os.ReadFile(filepath.Join("testdata", "acceptance_results.json"))
	if err != nil {
		t.Fatalf("read acceptance_results.json: %v", err)
	}
	var receiptMap map[string]json.RawMessage
	if err := json.Unmarshal(receiptBytes, &receiptMap); err != nil {
		t.Fatalf("parse acceptance_results.json: %v", err)
	}
	for _, reqKey := range schema.Required {
		if _, ok := receiptMap[reqKey]; !ok {
			t.Fatalf("acceptance_results.json missing schema-required key %q", reqKey)
		}
	}
	for k := range receiptMap {
		if _, ok := schema.Properties[k]; !ok {
			t.Fatalf("acceptance_results.json has undeclared key %q violating additionalProperties:false", k)
		}
	}

	// Verify dataset_manifest.json SHA-256 digests match tracked testdata files on disk.
	manifestBytes, err := os.ReadFile(filepath.Join("testdata", "dataset_manifest.json"))
	if err != nil {
		t.Fatalf("read dataset_manifest.json: %v", err)
	}
	var manifest struct {
		PublicDataset struct {
			Derived struct {
				Path   string `json:"path"`
				SHA256 string `json:"sha256"`
			} `json:"derived_content_free_corpus"`
		} `json:"public_dataset"`
		CanonicalHanzoConfig struct {
			ModelsYAML struct {
				Path   string `json:"path"`
				SHA256 string `json:"sha256"`
			} `json:"models_yaml_snapshot"`
			PricingSnapshot struct {
				Path   string `json:"path"`
				SHA256 string `json:"sha256"`
			} `json:"pricing_snapshot"`
		} `json:"canonical_hanzo_config"`
	}
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatalf("parse dataset_manifest.json: %v", err)
	}
	repoRoot := filepath.Join("..", "..")
	fileDigests := [][2]string{
		{manifest.PublicDataset.Derived.Path, manifest.PublicDataset.Derived.SHA256},
		{manifest.CanonicalHanzoConfig.ModelsYAML.Path, manifest.CanonicalHanzoConfig.ModelsYAML.SHA256},
		{manifest.CanonicalHanzoConfig.PricingSnapshot.Path, manifest.CanonicalHanzoConfig.PricingSnapshot.SHA256},
	}
	for _, fd := range fileDigests {
		b, err := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash(fd[0])))
		if err != nil {
			t.Fatalf("read manifest file %s: %v", fd[0], err)
		}
		if got := sha256LFNormalized(b); got != fd[1] {
			t.Fatalf("manifest sha256 mismatch for %s: disk=%s manifest=%s", fd[0], got, fd[1])
		}
	}
}
