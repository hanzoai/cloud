// Copyright 2026 Hanzo AI, Inc.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package ensoroutellm

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// EvalCorpusRecord is the canonical JSONL record accepted by both public
// evaluation runs (LiveBench Apache-2.0) and private internal Hanzo
// qualification runs (hanzoai/enso-bench). Either Prompt/Messages (raw text) or
// Features (pre-extracted content-free FeatureVec) may be provided.
type EvalCorpusRecord struct {
	QueryID            string                `json:"query_id"`
	Dataset            string                `json:"dataset,omitempty"`
	Category           string                `json:"category,omitempty"`
	Task               string                `json:"task,omitempty"`
	Prompt             string                `json:"prompt,omitempty"`
	Messages           []ConversationMessage `json:"messages,omitempty"`
	HasMedia           bool                  `json:"has_media,omitempty"`
	EstInputTokens     int                   `json:"est_input_tokens,omitempty"`
	EstOutputTokens    int                   `json:"est_output_tokens,omitempty"`
	ShippingRouterTask string                `json:"shipping_router_task,omitempty"`
	Features           []float64             `json:"features,omitempty"`
	ArmOutcomes        map[string]float64    `json:"arm_outcomes"`
	ArmLatenciesMs     map[string]int        `json:"arm_latencies_ms,omitempty"`
}

// LoadedCorpus bundles parsed evaluation items alongside integrity metadata.
type LoadedCorpus struct {
	Path            string
	SHA256          string
	DatasetName     string
	ContentFreeOnly bool
	Items           []EvalCorpusRecord
	ModelIDs        []string
	CategoryCounts  map[string]int
}

// LoadEvalCorpusJSONL loads and validates a JSONL evaluation corpus from disk.
// Enforces unique query_id values, bounded finite outcomes in [0, 1], valid
// token ceilings, and finite bounded pre-extracted features.
func LoadEvalCorpusJSONL(path string) (*LoadedCorpus, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read eval corpus %s: %w", path, err)
	}
	lc := &LoadedCorpus{
		Path:            path,
		SHA256:          sha256LFNormalized(raw),
		ContentFreeOnly: true,
		CategoryCounts:  make(map[string]int),
	}

	seenQIDs := make(map[string]bool)
	modelSet := make(map[string]bool)
	sc := bufio.NewScanner(strings.NewReader(string(raw)))
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var rec EvalCorpusRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			return nil, fmt.Errorf("corpus %s line %d: %w", path, lineNo, err)
		}
		if strings.TrimSpace(rec.QueryID) == "" {
			return nil, fmt.Errorf("corpus %s line %d: missing query_id", path, lineNo)
		}
		if seenQIDs[rec.QueryID] {
			return nil, fmt.Errorf("corpus %s line %d: duplicate query_id %q", path, lineNo, rec.QueryID)
		}
		seenQIDs[rec.QueryID] = true

		if rec.EstInputTokens < 0 || rec.EstInputTokens > MaxRequestTokens ||
			rec.EstOutputTokens < 0 || rec.EstOutputTokens > MaxRequestTokens {
			return nil, fmt.Errorf("corpus %s line %d: token counts out of bounds [0, %d]", path, lineNo, MaxRequestTokens)
		}
		if len(rec.ArmOutcomes) == 0 {
			return nil, fmt.Errorf("corpus %s line %d: empty arm_outcomes", path, lineNo)
		}
		for m, score := range rec.ArmOutcomes {
			if !isFinite(score) || score < 0 || score > 1 {
				return nil, fmt.Errorf("corpus %s line %d: invalid score %v for model %q", path, lineNo, score, m)
			}
			modelSet[m] = true
		}
		if rec.Prompt != "" || len(rec.Messages) > 0 {
			lc.ContentFreeOnly = false
		} else {
			if len(rec.Features) != FeatureDim {
				return nil, fmt.Errorf("corpus %s line %d: must provide either prompt/messages or %d-dim features (got %d)", path, lineNo, FeatureDim, len(rec.Features))
			}
			for d, fv := range rec.Features {
				if !isFinite(fv) || math.Abs(fv) > 1e6 {
					return nil, fmt.Errorf("corpus %s line %d: feature[%d] out of finite bounds (%v)", path, lineNo, d, fv)
				}
			}
		}
		if lc.DatasetName == "" && rec.Dataset != "" {
			lc.DatasetName = rec.Dataset
		}
		cat := rec.Category
		if cat == "" {
			cat = "unspecified"
		}
		lc.CategoryCounts[cat]++
		lc.Items = append(lc.Items, rec)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("scan corpus %s: %w", path, err)
	}
	if len(lc.Items) < 2 {
		return nil, fmt.Errorf("corpus %s requires at least 2 items for cross-validation, got %d", path, len(lc.Items))
	}
	if lc.DatasetName == "" {
		lc.DatasetName = "external_jsonl"
	}
	for m := range modelSet {
		lc.ModelIDs = append(lc.ModelIDs, m)
	}
	sort.Strings(lc.ModelIDs)
	return lc, nil
}

// ConvertRawCorpusToContentFreeJSONL reads a raw-prompt JSONL corpus and writes
// a content-free JSONL containing exact IEEE-754 Go FeatureVec values and
// ShippingClassify outputs (zero prompt text persisted).
func ConvertRawCorpusToContentFreeJSONL(inPath, outPath string) (string, int, error) {
	lc, err := LoadEvalCorpusJSONL(inPath)
	if err != nil {
		return "", 0, err
	}
	if lc.ContentFreeOnly {
		return "", 0, fmt.Errorf("input corpus %s is already content-free (no raw prompt or messages to extract)", inPath)
	}
	var buf strings.Builder
	for _, item := range lc.Items {
		req := corpusItemToRouteRequest(item, "org-eval")
		fv := corpusItemFeatures(item)
		inTok, outTok := ResolveTokenEstimates(req)
		task := item.ShippingRouterTask
		if item.Prompt != "" || len(item.Messages) > 0 || task == "" {
			task = string(ShippingClassify(corpusItemShippingRequest(item)))
		}
		fSlice := make([]float64, FeatureDim)
		copy(fSlice, fv[:])
		outRec := EvalCorpusRecord{
			QueryID:            item.QueryID,
			Dataset:            item.Dataset,
			Category:           item.Category,
			Task:               item.Task,
			HasMedia:           item.HasMedia,
			EstInputTokens:     inTok,
			EstOutputTokens:    outTok,
			ShippingRouterTask: task,
			Features:           fSlice,
			ArmOutcomes:        item.ArmOutcomes,
			ArmLatenciesMs:     item.ArmLatenciesMs,
		}
		b, err := json.Marshal(outRec)
		if err != nil {
			return "", 0, err
		}
		buf.Write(b)
		buf.WriteByte('\n')
	}
	outBytes := []byte(buf.String())
	if err := os.WriteFile(outPath, outBytes, 0o644); err != nil {
		return "", 0, err
	}
	return sha256LFNormalized(outBytes), len(lc.Items), nil
}

func corpusItemCombinedText(item EvalCorpusRecord) string {
	if len(item.Messages) == 0 {
		return item.Prompt
	}
	var sb strings.Builder
	sb.WriteString(item.Prompt)
	for _, m := range item.Messages {
		if m.Content != "" {
			if sb.Len() > 0 {
				sb.WriteByte('\n')
			}
			sb.WriteString(m.Content)
		}
	}
	return sb.String()
}

func corpusItemToRouteRequest(item EvalCorpusRecord, org string) RouteRequest {
	inTok := item.EstInputTokens
	if inTok <= 0 {
		inTok = EstimatePromptTokens(item.Prompt, item.Messages)
	}
	outTok := item.EstOutputTokens
	if outTok <= 0 {
		outTok = 256
	}
	req := RouteRequest{
		Org:             org,
		Prompt:          item.Prompt,
		Messages:        item.Messages,
		HasMedia:        item.HasMedia,
		EstInputTokens:  inTok,
		EstOutputTokens: outTok,
	}
	if len(item.Features) == FeatureDim && item.Prompt == "" && len(item.Messages) == 0 {
		var fv FeatureVec
		copy(fv[:], item.Features)
		req.PreExtractedFeatures = &fv
	}
	return req
}

func corpusItemFeatures(item EvalCorpusRecord) FeatureVec {
	if len(item.Features) == FeatureDim && item.Prompt == "" && len(item.Messages) == 0 {
		var fv FeatureVec
		copy(fv[:], item.Features)
		return fv
	}
	return ExtractFeatures(corpusItemToRouteRequest(item, "org-eval"))
}

func corpusItemShippingRequest(item EvalCorpusRecord) ShippingRequest {
	inTok := item.EstInputTokens
	if inTok <= 0 {
		inTok = EstimatePromptTokens(item.Prompt, item.Messages)
	}
	text := corpusItemCombinedText(item)
	sr := ShippingRequest{
		Text:         text,
		ApproxTokens: inTok,
		HasMedia:     item.HasMedia,
	}
	if text == "" && item.ShippingRouterTask != "" {
		sr.TaskHint = ShippingTask(item.ShippingRouterTask)
	}
	return sr
}

// CalibrationBin summarizes one decile bin of a reliability diagram.
type CalibrationBin struct {
	BinIndex   int     `json:"bin_index"`
	Count      int     `json:"count"`
	MeanPred   float64 `json:"mean_pred"`
	MeanActual float64 `json:"mean_actual"`
}

// AcceptancePolicyMetrics reports aggregate quality, cost, coverage, latency
// quantiles, and bootstrap 95% confidence intervals for one routing policy.
type AcceptancePolicyMetrics struct {
	PolicyName           string     `json:"policy_name"`
	Queries              int        `json:"queries"`
	RoutedQueries        int        `json:"routed_queries"`
	UnroutedQueries      int        `json:"unrouted_queries"`
	Accuracy             float64    `json:"accuracy"`
	AccuracyCI95         [2]float64 `json:"accuracy_ci95"`
	QualityRetentionPct  float64    `json:"quality_retention_pct"`
	BlendedUSDPerMTok    float64    `json:"blended_usd_per_mtok"`
	USDPer1kQueries      float64    `json:"usd_per_1k_queries"`
	USDPer1kCI95         [2]float64 `json:"usd_per_1k_ci95"`
	USDCostSavingPct     float64    `json:"usd_cost_saving_pct"`
	MeanModelLatencyMs   float64    `json:"mean_model_latency_ms"`
	P50ModelLatencyMs    int        `json:"p50_model_latency_ms"`
	P95ModelLatencyMs    int        `json:"p95_model_latency_ms"`
	P99ModelLatencyMs    int        `json:"p99_model_latency_ms"`
	UniqueModelsSelected int        `json:"unique_models_selected"`
}

// MatchedBudgetComparison compares nested-CV budget-matched candidate routing
// (tolerance calibrated strictly on training folds, never test-fold peeking)
// directly against a shipping hanzoai/ai/router baseline.
type MatchedBudgetComparison struct {
	BaselinePolicy      string     `json:"baseline_policy"`
	BaselineAccuracy    float64    `json:"baseline_accuracy"`
	BaselineUSDPer1k    float64    `json:"baseline_usd_per_1k"`
	BaselineUnrouted    int        `json:"baseline_unrouted_queries"`
	CandidatePolicy     string     `json:"candidate_policy"`
	CandidateAccuracy   float64    `json:"candidate_accuracy"`
	CandidateUSDPer1k   float64    `json:"candidate_usd_per_1k"`
	AccuracyDeltaPP     float64    `json:"accuracy_delta_pp"`
	AccuracyDeltaCI95PP [2]float64 `json:"accuracy_delta_ci95_pp"`
	CostDeltaPct        float64    `json:"cost_delta_pct"`
	BudgetMatched       bool       `json:"budget_matched"`
}

// ParetoPoint records one operating point along the quality-cost Pareto curve.
type ParetoPoint struct {
	Tolerance       float64 `json:"tolerance"`
	Accuracy        float64 `json:"accuracy"`
	USDPer1kQueries float64 `json:"usd_per_1k_queries"`
	USDCostSaving   float64 `json:"usd_cost_saving_pct"`
}

// RoutingPerfSummary records live CPU routing latency quantiles and sustained
// throughput for both stateless routing and ledger-recording routing.
type RoutingPerfSummary struct {
	Iterations     int     `json:"iterations"`
	MeanNs         float64 `json:"mean_ns"`
	P50Ns          int64   `json:"p50_ns"`
	P95Ns          int64   `json:"p95_ns"`
	P99Ns          int64   `json:"p99_ns"`
	ThroughputQPS  float64 `json:"throughput_qps"`
	Concurrent8QPS float64 `json:"concurrent_8_qps"`
}

// PerformanceComparison compares shipping hanzoai/ai/router vs candidate overhead.
type PerformanceComparison struct {
	PromptLengthMedianBytes int                `json:"prompt_length_median_bytes"`
	ShippingHeuristic       RoutingPerfSummary `json:"shipping_heuristic"`
	CandidateHybrid         RoutingPerfSummary `json:"candidate_hybrid"`
	CandidateWithLedger     RoutingPerfSummary `json:"candidate_with_ledger"`
}

// ReliabilityCheckResult summarizes one sanitized fault-injection check
// (contains zero concrete private model IDs).
type ReliabilityCheckResult struct {
	Category string `json:"category"`
	Check    string `json:"check"`
	Passed   bool   `json:"passed"`
	Detail   string `json:"detail"`
}

// ReliabilitySummary aggregates all fault-injection & capability checks.
type ReliabilitySummary struct {
	TotalChecks  int                      `json:"total_checks"`
	PassedChecks int                      `json:"passed_checks"`
	AllPassed    bool                     `json:"all_passed"`
	Checks       []ReliabilityCheckResult `json:"checks"`
}

// CatalogProvenance records canonical config digests consumed by the run.
type CatalogProvenance struct {
	ModelsYAMLSHA256      string         `json:"models_yaml_sha256"`
	PricingSnapshotSHA256 string         `json:"pricing_snapshot_sha256,omitempty"`
	ActiveArms            int            `json:"active_arms"`
	PriceSources          map[string]int `json:"price_sources"`
}

// AcceptanceReport is the strictly aggregate-only JSON receipt matching
// hanzoai/cloud#59's Acceptance Contract (zero prompts, zero query IDs,
// zero concrete model IDs, zero per-item labels; safe for internal private runs).
type AcceptanceReport struct {
	Schema                 string                    `json:"schema"`
	Benchmark              string                    `json:"benchmark"`
	PR                     string                    `json:"pr"`
	AggregateOnly          bool                      `json:"aggregate_only"`
	DatasetName            string                    `json:"dataset_name"`
	DatasetSHA256          string                    `json:"dataset_sha256"`
	ContentFreeCorpus      bool                      `json:"content_free_corpus"`
	Seed                   uint64                    `json:"seed"`
	CVFolds                int                       `json:"cv_folds"`
	CorpusSize             int                       `json:"corpus_size"`
	CategoryCounts         map[string]int            `json:"category_counts"`
	Catalog                CatalogProvenance         `json:"catalog"`
	Calibration            CalibrationSummary        `json:"calibration"`
	ReliabilityBins        []CalibrationBin          `json:"reliability_bins"`
	Policies               []AcceptancePolicyMetrics `json:"policies"`
	MatchedBudgetCanonical MatchedBudgetComparison   `json:"matched_budget_vs_canonical_router"`
	MatchedBudgetAdapted   MatchedBudgetComparison   `json:"matched_budget_vs_pool_adapted_router"`
	ParetoFrontier         []ParetoPoint             `json:"pareto_frontier"`
	Performance            PerformanceComparison     `json:"performance"`
	Reliability            ReliabilitySummary        `json:"reliability"`
}

// AcceptanceConfig controls RunAcceptanceBenchmark execution.
type AcceptanceConfig struct {
	ModelsYAMLPath      string
	PricingSnapshotPath string
	CorpusPath          string
	Seed                uint64
	CVFolds             int
	PerfIterations      int
	BootstrapResamples  int
}

type perQueryObs struct {
	routed    bool
	acc       float64
	costUSD   float64
	blended   float64
	latencyMs int
	routedArm string
}

// RunAcceptanceBenchmark executes the Acceptance Contract evaluation on real
// public or private JSONL data against canonical hanzoai/ai conf/models.yaml
// and the shipping hanzoai/ai/router baseline, emitting strictly aggregate receipts.
func RunAcceptanceBenchmark(cfg AcceptanceConfig) (AcceptanceReport, error) {
	if cfg.Seed == 0 {
		cfg.Seed = DefaultEvalSeed
	}
	if cfg.CVFolds < 2 {
		cfg.CVFolds = 5
	}
	if cfg.PerfIterations <= 0 {
		cfg.PerfIterations = 2000
	}
	if cfg.BootstrapResamples <= 0 {
		cfg.BootstrapResamples = 500
	}

	modelsCfg, err := LoadHanzoModelsYAMLFile(cfg.ModelsYAMLPath)
	if err != nil {
		return AcceptanceReport{}, err
	}
	var pricingSnap *PricingSnapshot
	if cfg.PricingSnapshotPath != "" {
		pricingSnap, err = LoadPricingSnapshotFile(cfg.PricingSnapshotPath)
		if err != nil {
			return AcceptanceReport{}, err
		}
	}
	corpus, err := LoadEvalCorpusJSONL(cfg.CorpusPath)
	if err != nil {
		return AcceptanceReport{}, err
	}
	if cfg.CVFolds > len(corpus.Items) {
		cfg.CVFolds = len(corpus.Items)
	}

	arms, err := BuildCatalogArms(modelsCfg, pricingSnap, corpus.ModelIDs)
	if err != nil {
		return AcceptanceReport{}, err
	}
	if len(arms) < 2 {
		return AcceptanceReport{}, fmt.Errorf("need at least 2 chat-capable arms overlapping corpus, got %d", len(arms))
	}

	armByID := make(map[string]ModelArm, len(arms))
	poolSet := make(map[string]bool, len(arms))
	priceSources := make(map[string]int)
	for _, a := range arms {
		armByID[a.ID] = a
		poolSet[a.ID] = true
		priceSources[a.PriceSource]++
	}

	// Build stratified fold assignments grouped by category/task.
	folds := assignStratifiedFolds(corpus.Items, cfg.CVFolds, cfg.Seed)

	canonicalPolicy := ShippingPolicy{Prefer: modelsCfg.Router.Prefer, CostCeiling: modelsCfg.Router.CostCeiling}
	adaptedPolicy := PoolAdaptedShippingPolicy(modelsCfg, arms)

	type evalSpec struct {
		name     string
		kind     string // "shipping_canonical" | "shipping_adapted" | "best_single_budget" | "enso"
		strategy RouterStrategy
		tol      float64
	}
	specs := []evalSpec{
		{name: "always_strong", kind: "enso", strategy: StrategyAlwaysStrong},
		{name: "always_weak", kind: "enso", strategy: StrategyAlwaysWeak},
		{name: "random_router", kind: "enso", strategy: StrategyRandom},
		{name: "best_single_budget_arm_cv", kind: "best_single_budget"},
		{name: "hanzo_ai_router_canonical", kind: "shipping_canonical"},
		{name: "hanzo_ai_router_pool_adapted", kind: "shipping_adapted"},
		{name: "routellm_mf_rel(tol=0.04)", kind: "enso", strategy: StrategyRouteLLMMFConstrained, tol: 0.04},
		{name: "enso_hybrid_rel(tol=0.00)", kind: "enso", strategy: StrategyEnsoHybridConstrained, tol: 0.00},
		{name: "enso_hybrid_rel(tol=0.01)", kind: "enso", strategy: StrategyEnsoHybridConstrained, tol: 0.01},
		{name: "enso_hybrid_rel(tol=0.02)", kind: "enso", strategy: StrategyEnsoHybridConstrained, tol: 0.02},
		{name: "enso_hybrid_rel(tol=0.04)", kind: "enso", strategy: StrategyEnsoHybridConstrained, tol: 0.04},
		{name: "enso_hybrid_rel(tol=0.06)", kind: "enso", strategy: StrategyEnsoHybridConstrained, tol: 0.06},
		{name: "enso_hybrid_rel(tol=0.10)", kind: "enso", strategy: StrategyEnsoHybridConstrained, tol: 0.10},
		{name: "enso_hybrid_rel(tol=0.16)", kind: "enso", strategy: StrategyEnsoHybridConstrained, tol: 0.16},
	}

	n := len(corpus.Items)
	obsByPolicy := make([][]perQueryObs, len(specs))
	for i := range specs {
		obsByPolicy[i] = make([]perQueryObs, n)
	}

	// Pareto sweep grid (strictly relative tolerance with QualityRetentionRatio=1.0).
	paretoTols := []float64{0.00, 0.01, 0.02, 0.03, 0.04, 0.06, 0.08, 0.10, 0.12, 0.16, 0.20, 0.25}
	paretoObs := make([][]perQueryObs, len(paretoTols))
	for i := range paretoTols {
		paretoObs[i] = make([]perQueryObs, n)
	}

	// Nested-CV budget-matched out-of-fold predictions (zero test-set peeking!).
	nestedCandCanon := make([]perQueryObs, n)
	nestedCandAdapt := make([]perQueryObs, n)
	var nestedTolsCanon, nestedTolsAdapt []float64

	var calPreds, calActuals []float64
	entPol := ScopePolicy{PlanTier: PlanEnterprise, HasPlanTier: true}

	// Highest blended price arm defines the 25% budget threshold for best_single_budget_arm_cv.
	maxBlended := arms[len(arms)-1].BlendedUSDPerMTok()

	var lastWeights *TrainedWeights
	for fold := 0; fold < cfg.CVFolds; fold++ {
		var trainIndices []int
		var trainSamples []TrainingSample
		for idx, item := range corpus.Items {
			if folds[idx] == fold {
				continue
			}
			trainIndices = append(trainIndices, idx)
			fv := corpusItemFeatures(item)
			for _, arm := range arms {
				if y, ok := item.ArmOutcomes[arm.ID]; ok {
					trainSamples = append(trainSamples, TrainingSample{
						QueryID:  item.QueryID,
						Features: fv,
						ModelID:  arm.ID,
						Passed:   y,
					})
				}
			}
		}

		// Select best single budget arm strictly on training fold (<= 25% of strong blended rate).
		bestSingleArm := selectBestSingleBudgetArm(corpus.Items, trainIndices, arms, maxBlended*0.25)

		// Nested inner calibration on training folds ONLY to choose budget-matched tolerances
		// for canonical and pool-adapted shipping baselines without touching held-out fold!
		tolCanon, tolAdapt := calibrateNestedTolerances(
			corpus.Items, trainIndices, arms, armByID, entPol,
			canonicalPolicy, adaptedPolicy, paretoTols, cfg.Seed^uint64(fold+31)*0x517cc1b7,
		)
		nestedTolsCanon = append(nestedTolsCanon, tolCanon)
		nestedTolsAdapt = append(nestedTolsAdapt, tolAdapt)

		tw := FitTrainedWeights(trainSamples, arms, cfg.Seed^uint64(fold+1)*0x9e3779b9)
		lastWeights = tw
		r := NewEnsoRouter(arms, tw)
		r.SetOrgPolicy("org-eval-acceptance", entPol)
		randRouter := NewEnsoRouter(arms, nil)
		randRouter.SetOrgPolicy("org-eval-acceptance", entPol)

		for idx, item := range corpus.Items {
			if folds[idx] != fold {
				continue
			}
			fv := corpusItemFeatures(item)
			for _, arm := range arms {
				if y, ok := item.ArmOutcomes[arm.ID]; ok {
					p, _ := tw.PredictQualityByKind(fv, arm.ID, PredictorHybrid)
					calPreds = append(calPreds, p)
					calActuals = append(calActuals, y)
				}
			}

			// Gate eligible arms strictly to arms that have ground-truth labels on this query
			// so sparse private corpora never silently score missing labels as 0.0.
			missingArms := unavailableForItem(arms, item.ArmOutcomes)
			itemKnown := func(id string) bool {
				if !poolSet[id] {
					return false
				}
				_, ok := item.ArmOutcomes[id]
				return ok
			}
			canonCli := NewShippingClient(canonicalPolicy, itemKnown, itemKnown)
			adaptCli := NewShippingClient(adaptedPolicy, itemKnown, itemKnown)

			baseReq := corpusItemToRouteRequest(item, "org-eval-acceptance")
			baseReq.UnavailableModels = missingArms
			inTok, outTok := ResolveTokenEstimates(baseReq)
			shipReq := corpusItemShippingRequest(item)

			recordChosen := func(chosenID string) perQueryObs {
				if chosenID == "" {
					return perQueryObs{routed: false}
				}
				y, ok := item.ArmOutcomes[chosenID]
				if !ok {
					return perQueryObs{routed: false}
				}
				arm := armByID[chosenID]
				lat := EstimateLatencyMs(arm, outTok)
				if customLat, okLat := item.ArmLatenciesMs[chosenID]; okLat && customLat > 0 {
					lat = customLat
				}
				return perQueryObs{
					routed:    true,
					acc:       y,
					costUSD:   float64(EstimateCostMicros(arm, inTok, outTok)) / 1_000_000.0,
					blended:   arm.BlendedUSDPerMTok(),
					latencyMs: lat,
					routedArm: chosenID,
				}
			}

			for sIdx, sp := range specs {
				var chosenID string
				switch sp.kind {
				case "shipping_canonical":
					dec := canonCli.RouteDecision(context.Background(), shipReq, ShippingSlo{})
					chosenID = dec.Model
				case "shipping_adapted":
					dec := adaptCli.RouteDecision(context.Background(), shipReq, ShippingSlo{})
					chosenID = dec.Model
				case "best_single_budget":
					if _, ok := item.ArmOutcomes[bestSingleArm]; ok {
						chosenID = bestSingleArm
					}
				default:
					req := baseReq
					if sp.strategy == StrategyRandom {
						// Deterministic seed + QueryID guarantees identical random draws on raw vs content-free!
						req.RequestID = fmt.Sprintf("s%d-%s", cfg.Seed, item.QueryID)
						dec, rErr := randRouter.Route(req, StrategyRandom)
						if rErr != nil {
							return AcceptanceReport{}, fmt.Errorf("route %s failed on item %d: %w", sp.name, idx, rErr)
						}
						chosenID = dec.RoutedModel
					} else {
						if sp.strategy == StrategyEnsoHybridConstrained || sp.strategy == StrategyRouteLLMMFConstrained {
							req.QualityRetentionRatio = 1.0
							req.QualityTolerance = sp.tol
						}
						dec, rErr := r.Route(req, sp.strategy)
						if rErr != nil {
							return AcceptanceReport{}, fmt.Errorf("route %s failed on item %d: %w", sp.name, idx, rErr)
						}
						chosenID = dec.RoutedModel
					}
				}
				obsByPolicy[sIdx][idx] = recordChosen(chosenID)
			}

			for pIdx, tol := range paretoTols {
				req := baseReq
				req.QualityRetentionRatio = 1.0
				req.QualityTolerance = tol
				dec, rErr := r.Route(req, StrategyEnsoHybridConstrained)
				if rErr != nil {
					return AcceptanceReport{}, fmt.Errorf("pareto route tol=%.2f failed: %w", tol, rErr)
				}
				paretoObs[pIdx][idx] = recordChosen(dec.RoutedModel)
			}

			// Evaluate nested-CV budget-matched tolerances blindly on held-out test fold.
			reqNC := baseReq
			reqNC.QualityRetentionRatio = 1.0
			reqNC.QualityTolerance = tolCanon
			decNC, errNC := r.Route(reqNC, StrategyEnsoHybridConstrained)
			if errNC != nil {
				return AcceptanceReport{}, fmt.Errorf("nested canonical route failed: %w", errNC)
			}
			nestedCandCanon[idx] = recordChosen(decNC.RoutedModel)

			reqNA := baseReq
			reqNA.QualityRetentionRatio = 1.0
			reqNA.QualityTolerance = tolAdapt
			decNA, errNA := r.Route(reqNA, StrategyEnsoHybridConstrained)
			if errNA != nil {
				return AcceptanceReport{}, fmt.Errorf("nested adapted route failed: %w", errNA)
			}
			nestedCandAdapt[idx] = recordChosen(decNA.RoutedModel)
		}
	}

	strongAcc, strongUSD, _, _ := meanAccCost(obsByPolicy[0])
	weakAcc, _, _, _ := meanAccCost(obsByPolicy[1])

	policies := make([]AcceptancePolicyMetrics, len(specs))
	for i, sp := range specs {
		obs := obsByPolicy[i]
		acc, usd, routedCnt, unroutedCnt := meanAccCost(obs)
		var blendedSum, latSum float64
		lats := make([]int, 0, routedCnt)
		uniq := make(map[string]bool)
		accs := make([]float64, 0, routedCnt)
		usd1k := make([]float64, 0, routedCnt)
		for _, o := range obs {
			if !o.routed {
				continue
			}
			blendedSum += o.blended
			latSum += float64(o.latencyMs)
			lats = append(lats, o.latencyMs)
			uniq[o.routedArm] = true
			accs = append(accs, o.acc)
			usd1k = append(usd1k, o.costUSD*1000.0)
		}
		sort.Ints(lats)
		accCI := bootstrapMeanCI95(accs, cfg.BootstrapResamples, cfg.Seed^uint64(i+101))
		usdCI := bootstrapMeanCI95(usd1k, cfg.BootstrapResamples, cfg.Seed^uint64(i+202))

		retPct := 0.0
		if strongAcc > 0 && routedCnt > 0 {
			retPct = (acc / strongAcc) * 100.0
		}
		savePct := 0.0
		if strongUSD > 0 && routedCnt > 0 {
			savePct = (1.0 - usd/strongUSD) * 100.0
		}
		meanBlended := 0.0
		meanLat := 0.0
		if routedCnt > 0 {
			meanBlended = blendedSum / float64(routedCnt)
			meanLat = latSum / float64(routedCnt)
		}
		policies[i] = AcceptancePolicyMetrics{
			PolicyName:           sp.name,
			Queries:              len(obs),
			RoutedQueries:        routedCnt,
			UnroutedQueries:      unroutedCnt,
			Accuracy:             round4f(acc),
			AccuracyCI95:         [2]float64{round4f(accCI[0]), round4f(accCI[1])},
			QualityRetentionPct:  round2f(retPct),
			BlendedUSDPerMTok:    round2f(meanBlended),
			USDPer1kQueries:      round4f(usd * 1000.0),
			USDPer1kCI95:         [2]float64{round4f(usdCI[0]), round4f(usdCI[1])},
			USDCostSavingPct:     round2f(savePct),
			MeanModelLatencyMs:   round1f(meanLat),
			P50ModelLatencyMs:    percentileInt(lats, 50),
			P95ModelLatencyMs:    percentileInt(lats, 95),
			P99ModelLatencyMs:    percentileInt(lats, 99),
			UniqueModelsSelected: len(uniq),
		}
	}

	// Compute Pareto frontier, APGR, CPT80, CPT90 using standard RouteLLM cost ratio (usd / strongUSD)
	// over actual non-enveloped operating points (no hidden regressions!).
	paretoFrontier := make([]ParetoPoint, len(paretoTols))
	curvePts := make([]paretoPt, 0, len(paretoTols)+2)
	gap := strongAcc - weakAcc
	degenerateGap := gap <= 1e-6 || strongUSD <= 1e-12
	if !degenerateGap {
		weakRatio := 0.0
		if _, wUSD, rc, _ := meanAccCost(obsByPolicy[1]); rc > 0 {
			weakRatio = wUSD / strongUSD
		}
		curvePts = append(curvePts, paretoPt{costRatio: weakRatio, pgr: 0.0}, paretoPt{costRatio: 1.0, pgr: 1.0})
	}
	for i, tol := range paretoTols {
		acc, usd, rc, _ := meanAccCost(paretoObs[i])
		savePct := 0.0
		if strongUSD > 0 && rc > 0 {
			savePct = (1.0 - usd/strongUSD) * 100.0
		}
		paretoFrontier[i] = ParetoPoint{
			Tolerance:       tol,
			Accuracy:        round4f(acc),
			USDPer1kQueries: round4f(usd * 1000.0),
			USDCostSaving:   round2f(savePct),
		}
		if !degenerateGap && rc > 0 {
			cr := usd / strongUSD
			if cr < 0 {
				cr = 0
			}
			if cr > 1 {
				cr = 1
			}
			pgr := (acc - weakAcc) / gap
			if pgr < 0 {
				pgr = 0
			}
			if pgr > 1 {
				pgr = 1
			}
			curvePts = append(curvePts, paretoPt{costRatio: cr, pgr: pgr})
		}
	}
	apgr, cpt80, cpt90 := computeActualCurveAPGRAndCPT(curvePts, degenerateGap)

	// Matched-budget comparisons using nested-CV out-of-fold candidate (zero test peeking!).
	matchCanonical := buildNestedMatchedComparison(
		"hanzo_ai_router_canonical", obsByPolicy[4], nestedCandCanon, nestedTolsCanon, cfg.BootstrapResamples, cfg.Seed^0x5151,
	)
	matchAdapted := buildNestedMatchedComparison(
		"hanzo_ai_router_pool_adapted", obsByPolicy[5], nestedCandAdapt, nestedTolsAdapt, cfg.BootstrapResamples, cfg.Seed^0x6262,
	)

	ece, brier, bins := computeDetailedCalibration(calPreds, calActuals)

	// Measure live CPU routing overhead (stateless + ledger) at corpus-representative prompt lengths.
	perfRouter := NewEnsoRouter(arms, lastWeights)
	perfRouter.SetOrgPolicy("org-eval-acceptance", entPol)
	adaptPerfCli := NewShippingClient(adaptedPolicy, func(id string) bool { return poolSet[id] }, func(id string) bool { return poolSet[id] })
	perfSummary := MeasureRoutingPerformance(adaptPerfCli, perfRouter, corpus.Items, cfg.PerfIterations)

	// Run sanitized, catalog-agnostic reliability & capability mismatch audit.
	reliability := RunReliabilityAudit(modelsCfg, arms, lastWeights)

	snapSHA := ""
	if pricingSnap != nil {
		snapSHA = pricingSnap.SHA256
	}

	return AcceptanceReport{
		Schema:            "hanzo.ensoroutellm.acceptance/v2",
		Benchmark:         "quality_constrained_enso_routing_acceptance",
		PR:                "https://github.com/hanzoai/cloud/pull/59",
		AggregateOnly:     true,
		DatasetName:       corpus.DatasetName,
		DatasetSHA256:     corpus.SHA256,
		ContentFreeCorpus: corpus.ContentFreeOnly,
		Seed:              cfg.Seed,
		CVFolds:           cfg.CVFolds,
		CorpusSize:        len(corpus.Items),
		CategoryCounts:    corpus.CategoryCounts,
		Catalog: CatalogProvenance{
			ModelsYAMLSHA256:      modelsCfg.SHA256,
			PricingSnapshotSHA256: snapSHA,
			ActiveArms:            len(arms),
			PriceSources:          priceSources,
		},
		Calibration: CalibrationSummary{
			ECE10Bin: round4f(ece),
			Brier:    round4f(brier),
			APGR:     round4f(apgr),
			CPT80Pct: round2f(cpt80 * 100.0),
			CPT90Pct: round2f(cpt90 * 100.0),
		},
		ReliabilityBins:        bins,
		Policies:               policies,
		MatchedBudgetCanonical: matchCanonical,
		MatchedBudgetAdapted:   matchAdapted,
		ParetoFrontier:         paretoFrontier,
		Performance:            perfSummary,
		Reliability:            reliability,
	}, nil
}

func unavailableForItem(arms []ModelArm, outcomes map[string]float64) map[string]bool {
	var missing map[string]bool
	for _, a := range arms {
		if _, ok := outcomes[a.ID]; !ok {
			if missing == nil {
				missing = make(map[string]bool)
			}
			missing[a.ID] = true
		}
	}
	return missing
}

func selectBestSingleBudgetArm(items []EvalCorpusRecord, trainIndices []int, arms []ModelArm, maxBlended float64) string {
	bestID := arms[0].ID
	bestMean := -1.0
	for _, a := range arms {
		if a.BlendedUSDPerMTok() > maxBlended+1e-9 {
			continue
		}
		var sum float64
		var cnt int
		for _, idx := range trainIndices {
			if y, ok := items[idx].ArmOutcomes[a.ID]; ok {
				sum += y
				cnt++
			}
		}
		if cnt > 0 {
			m := sum / float64(cnt)
			if m > bestMean {
				bestMean = m
				bestID = a.ID
			}
		}
	}
	return bestID
}

// calibrateNestedTolerances selects budget-matched relative tolerances strictly
// on trainIndices using full k-fold stratified inner out-of-fold CV (k=3) and
// a one-standard-error (1-SE) upper confidence bound on inner validation spend
// (mean + 1*SE <= baseline spend) so held-out test folds are never consulted
// while preventing finite-sample budget overshoots.
func calibrateNestedTolerances(
	items []EvalCorpusRecord,
	trainIndices []int,
	arms []ModelArm,
	armByID map[string]ModelArm,
	entPol ScopePolicy,
	canonicalPolicy, adaptedPolicy ShippingPolicy,
	tols []float64,
	seed uint64,
) (float64, float64) {
	if len(trainIndices) < 4 {
		return 0.04, 0.04
	}
	innerK := 3
	if len(trainIndices) < 12 {
		innerK = 2
	}
	trainItems := make([]EvalCorpusRecord, len(trainIndices))
	for pos, idx := range trainIndices {
		trainItems[pos] = items[idx]
	}
	innerFolds := assignStratifiedFolds(trainItems, innerK, seed^0x3141592653589793)

	poolSet := make(map[string]bool, len(arms))
	for _, a := range arms {
		poolSet[a.ID] = true
	}

	var canonUSD, adaptUSD float64
	var canonCnt, adaptCnt int
	tolAcc := make([]float64, len(tols))
	tolUSD := make([]float64, len(tols))
	tolUSDSq := make([]float64, len(tols))
	tolCnt := make([]int, len(tols))

	for f := 0; f < innerK; f++ {
		var innerTrain, innerVal []int
		for pos, idx := range trainIndices {
			if innerFolds[pos] == f {
				innerVal = append(innerVal, idx)
			} else {
				innerTrain = append(innerTrain, idx)
			}
		}
		if len(innerTrain) == 0 || len(innerVal) == 0 {
			continue
		}
		var samples []TrainingSample
		for _, idx := range innerTrain {
			it := items[idx]
			fv := corpusItemFeatures(it)
			for _, a := range arms {
				if y, ok := it.ArmOutcomes[a.ID]; ok {
					samples = append(samples, TrainingSample{
						QueryID:  it.QueryID,
						Features: fv,
						ModelID:  a.ID,
						Passed:   y,
					})
				}
			}
		}
		tw := FitTrainedWeights(samples, arms, seed^uint64(f+1)*0x9e3779b97f4a7c15)
		r := NewEnsoRouter(arms, tw)
		r.SetOrgPolicy("org-inner-cal", entPol)

		for _, idx := range innerVal {
			it := items[idx]
			reqBase := corpusItemToRouteRequest(it, "org-inner-cal")
			reqBase.UnavailableModels = unavailableForItem(arms, it.ArmOutcomes)
			inTok, outTok := ResolveTokenEstimates(reqBase)
			shipReq := corpusItemShippingRequest(it)

			itemKnown := func(id string) bool {
				if !poolSet[id] {
					return false
				}
				_, ok := it.ArmOutcomes[id]
				return ok
			}
			if d := NewShippingClient(canonicalPolicy, itemKnown, itemKnown).RouteDecision(context.Background(), shipReq, ShippingSlo{}); d.Model != "" {
				if _, ok := it.ArmOutcomes[d.Model]; ok {
					canonUSD += float64(EstimateCostMicros(armByID[d.Model], inTok, outTok)) / 1_000_000.0
					canonCnt++
				}
			}
			if d := NewShippingClient(adaptedPolicy, itemKnown, itemKnown).RouteDecision(context.Background(), shipReq, ShippingSlo{}); d.Model != "" {
				if _, ok := it.ArmOutcomes[d.Model]; ok {
					adaptUSD += float64(EstimateCostMicros(armByID[d.Model], inTok, outTok)) / 1_000_000.0
					adaptCnt++
				}
			}

			for ti, tol := range tols {
				req := reqBase
				req.QualityRetentionRatio = 1.0
				req.QualityTolerance = tol
				dec, err := r.Route(req, StrategyEnsoHybridConstrained)
				if err != nil {
					continue
				}
				if y, ok := it.ArmOutcomes[dec.RoutedModel]; ok {
					cost := float64(EstimateCostMicros(armByID[dec.RoutedModel], inTok, outTok)) / 1_000_000.0
					tolAcc[ti] += y
					tolUSD[ti] += cost
					tolUSDSq[ti] += cost * cost
					tolCnt[ti]++
				}
			}
		}
	}

	pickForBudget := func(targetUSD float64, hasTarget bool) float64 {
		if !hasTarget {
			return 0.04
		}
		// Require inner OOF mean spend + 1 standard error <= baseline budget
		// (1-SE safe-budget rule) to control finite-sample spend variance without
		// consulting the held-out outer test fold.
		bestIdx := -1
		bestAcc := -1.0
		for ti := range tols {
			if tolCnt[ti] == 0 {
				continue
			}
			nf := float64(tolCnt[ti])
			meanUSD := tolUSD[ti] / nf
			variance := tolUSDSq[ti]/nf - meanUSD*meanUSD
			if variance < 0 {
				variance = 0
			}
			seUSD := math.Sqrt(variance / nf)
			meanAcc := tolAcc[ti] / nf
			if meanUSD+seUSD <= targetUSD+1e-9 && meanAcc > bestAcc {
				bestAcc = meanAcc
				bestIdx = ti
			}
		}
		if bestIdx >= 0 {
			return tols[bestIdx]
		}
		// Fallback to lowest-spend tolerance if no point satisfied budget.
		lowIdx := len(tols) - 1
		lowUSD := math.Inf(1)
		for ti := range tols {
			if tolCnt[ti] == 0 {
				continue
			}
			if m := tolUSD[ti] / float64(tolCnt[ti]); m < lowUSD {
				lowUSD = m
				lowIdx = ti
			}
		}
		return tols[lowIdx]
	}

	var cTarget, aTarget float64
	if canonCnt > 0 {
		cTarget = canonUSD / float64(canonCnt)
	}
	if adaptCnt > 0 {
		aTarget = adaptUSD / float64(adaptCnt)
	}
	return pickForBudget(cTarget, canonCnt > 0), pickForBudget(aTarget, adaptCnt > 0)
}

type paretoPt struct {
	costRatio float64
	pgr       float64
}

// computeActualCurveAPGRAndCPT integrates PGR over actual sorted spend ratios
// (usd / strongUSD) without taking a running-max upper envelope, normalizing by
// the [weakRatio, 1.0] spend span so APGR spans [0, 1] cleanly across both wide
// and narrow-price catalogs while still penalizing non-monotonic regressions.
func computeActualCurveAPGRAndCPT(pts []paretoPt, degenerate bool) (float64, float64, float64) {
	if degenerate || len(pts) < 2 {
		return 0, 1, 1
	}
	sort.Slice(pts, func(i, j int) bool {
		if pts[i].costRatio != pts[j].costRatio {
			return pts[i].costRatio < pts[j].costRatio
		}
		return pts[i].pgr < pts[j].pgr
	})
	var area float64
	for i := 1; i < len(pts); i++ {
		dx := pts[i].costRatio - pts[i-1].costRatio
		if dx > 0 {
			area += 0.5 * (pts[i].pgr + pts[i-1].pgr) * dx
		}
	}
	span := pts[len(pts)-1].costRatio - pts[0].costRatio
	if span > 1e-9 {
		area /= span
	}
	if area < 0 {
		area = 0
	}
	if area > 1 {
		area = 1
	}
	cpt := func(target float64) float64 {
		best := 1.0
		for _, p := range pts {
			if p.pgr >= target && p.costRatio < best {
				best = p.costRatio
			}
		}
		return best
	}
	return area, cpt(0.80), cpt(0.90)
}

func assignStratifiedFolds(items []EvalCorpusRecord, k int, seed uint64) []int {
	groups := make(map[string][]int)
	var keys []string
	for i, it := range items {
		key := it.Category + "/" + it.Task
		if _, ok := groups[key]; !ok {
			keys = append(keys, key)
		}
		groups[key] = append(groups[key], i)
	}
	sort.Strings(keys)
	rng := NewRNG(seed ^ 0xabcdef1234567890)
	folds := make([]int, len(items))
	cursor := 0
	for _, key := range keys {
		idxs := groups[key]
		rng.Shuffle(len(idxs), func(i, j int) { idxs[i], idxs[j] = idxs[j], idxs[i] })
		for _, idx := range idxs {
			folds[idx] = cursor % k
			cursor++
		}
	}
	return folds
}

func round1f(x float64) float64 {
	return math.Round(x*10.0) / 10.0
}

func meanAccCost(obs []perQueryObs) (float64, float64, int, int) {
	var a, c float64
	var routed, unrouted int
	for _, o := range obs {
		if !o.routed {
			unrouted++
			continue
		}
		a += o.acc
		c += o.costUSD
		routed++
	}
	if routed == 0 {
		return 0, 0, 0, unrouted
	}
	n := float64(routed)
	return a / n, c / n, routed, unrouted
}

func buildNestedMatchedComparison(
	baseName string,
	baseObs []perQueryObs,
	candObs []perQueryObs,
	foldTols []float64,
	resamples int,
	seed uint64,
) MatchedBudgetComparison {
	baseAcc, baseUSD, baseRouted, baseUnrouted := meanAccCost(baseObs)
	candAcc, candUSD, candRouted, _ := meanAccCost(candObs)

	var deltas []float64
	var deltaSum float64
	for j := range baseObs {
		if baseObs[j].routed && candObs[j].routed {
			d := (candObs[j].acc - baseObs[j].acc) * 100.0
			deltas = append(deltas, d)
			deltaSum += d
		}
	}
	pairedDeltaPP := 0.0
	if len(deltas) > 0 {
		pairedDeltaPP = deltaSum / float64(len(deltas))
	}
	ci := bootstrapMeanCI95(deltas, resamples, seed)
	costDeltaPct := 0.0
	if baseUSD > 0 && candRouted > 0 {
		costDeltaPct = ((candUSD - baseUSD) / baseUSD) * 100.0
	}

	// Format median selected nested tolerance for readability.
	tolsCopy := append([]float64(nil), foldTols...)
	sort.Float64s(tolsCopy)
	medTol := 0.04
	if len(tolsCopy) > 0 {
		medTol = tolsCopy[len(tolsCopy)/2]
	}

	budgetMatched := baseUnrouted == 0 && baseRouted > 0 && candRouted > 0 && candUSD <= baseUSD+1e-9

	return MatchedBudgetComparison{
		BaselinePolicy:      baseName,
		BaselineAccuracy:    round4f(baseAcc),
		BaselineUSDPer1k:    round4f(baseUSD * 1000.0),
		BaselineUnrouted:    baseUnrouted,
		CandidatePolicy:     fmt.Sprintf("enso_hybrid_nested_cv(med_tol=%.2f)", medTol),
		CandidateAccuracy:   round4f(candAcc),
		CandidateUSDPer1k:   round4f(candUSD * 1000.0),
		AccuracyDeltaPP:     round2f(pairedDeltaPP),
		AccuracyDeltaCI95PP: [2]float64{round2f(ci[0]), round2f(ci[1])},
		CostDeltaPct:        round2f(costDeltaPct),
		BudgetMatched:       budgetMatched,
	}
}

func bootstrapMeanCI95(vals []float64, resamples int, seed uint64) [2]float64 {
	n := len(vals)
	if n == 0 {
		return [2]float64{0, 0}
	}
	rng := NewRNG(seed)
	means := make([]float64, resamples)
	invN := 1.0 / float64(n)
	for b := 0; b < resamples; b++ {
		var sum float64
		for i := 0; i < n; i++ {
			sum += vals[rng.IntN(n)]
		}
		means[b] = sum * invN
	}
	sort.Float64s(means)
	loIdx := int(math.Floor(0.025 * float64(resamples-1)))
	hiIdx := int(math.Ceil(0.975 * float64(resamples-1)))
	return [2]float64{means[loIdx], means[hiIdx]}
}

func computeDetailedCalibration(preds, actuals []float64) (float64, float64, []CalibrationBin) {
	if len(preds) == 0 {
		return 0, 0, nil
	}
	const numBins = 10
	var brierSum float64
	binCnt := make([]int, numBins)
	binPred := make([]float64, numBins)
	binAct := make([]float64, numBins)

	for i, p := range preds {
		y := actuals[i]
		diff := p - y
		brierSum += diff * diff
		b := int(p * float64(numBins))
		if b < 0 {
			b = 0
		}
		if b >= numBins {
			b = numBins - 1
		}
		binCnt[b]++
		binPred[b] += p
		binAct[b] += y
	}

	n := float64(len(preds))
	var ece float64
	bins := make([]CalibrationBin, 0, numBins)
	for b := 0; b < numBins; b++ {
		if binCnt[b] == 0 {
			continue
		}
		mp := binPred[b] / float64(binCnt[b])
		ma := binAct[b] / float64(binCnt[b])
		ece += (float64(binCnt[b]) / n) * math.Abs(mp-ma)
		bins = append(bins, CalibrationBin{
			BinIndex:   b,
			Count:      binCnt[b],
			MeanPred:   round4f(mp),
			MeanActual: round4f(ma),
		})
	}
	return ece, brierSum / n, bins
}

func percentileInt(sorted []int, pct int) int {
	if len(sorted) == 0 {
		return 0
	}
	idx := (pct*(len(sorted)-1) + 50) / 100
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

func percentileInt64(sorted []int64, pct int) int64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := (pct*(len(sorted)-1) + 50) / 100
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

// MeasureRoutingPerformance benchmarks live CPU routing latency quantiles
// (p50/p95/p99) and sustained single- and 8-goroutine throughput side-by-side
// for shipping hanzoai/ai/router vs EnsoRouter (both stateless and with
// in-memory ledger writes). On content-free corpora, prompts are expanded to
// match each item's recorded token length so feature-extraction work reflects
// real corpus prompt lengths honestly.
func MeasureRoutingPerformance(
	shipping ShippingClient,
	candidate *EnsoRouter,
	items []EvalCorpusRecord,
	iters int,
) PerformanceComparison {
	if iters < 200 {
		iters = 200
	}
	baseSnippets := []string{
		"Write and optimize a Go function that merges overlapping intervals, handles concurrent access with mutex safety, and proves O(n log n) time complexity. ",
		"Analyze the quarterly service reliability metrics across regions, synthesize root causes for tail latency spikes, and reconcile every evidence span. ",
		"Prove by mathematical induction that for every integer n >= 1 the recurrence converges under the stated boundary constraints and inequality. ",
		"Implement a JSON schema validator with strict RFC3339 timestamp checks and idempotent webhook processing in Python FastAPI. ",
	}
	catHasVision := false
	maxCtx := 0
	for _, a := range candidate.catalog {
		if a.SupportsVision {
			catHasVision = true
		}
		if a.ContextWindow > maxCtx {
			maxCtx = a.ContextWindow
		}
	}

	shipReqs := make([]ShippingRequest, len(items))
	ensoReqs := make([]RouteRequest, len(items))
	promptLens := make([]int, len(items))

	for i, it := range items {
		txt := corpusItemCombinedText(it)
		if txt == "" {
			// Match the actual item's recorded byte budget (~4 bytes/token) using general technical text.
			targetBytes := it.EstInputTokens * 4
			if targetBytes < 120 {
				targetBytes = 120
			}
			if targetBytes > 8192 {
				targetBytes = 8192
			}
			snip := baseSnippets[i%len(baseSnippets)]
			reps := (targetBytes + len(snip) - 1) / len(snip)
			txt = strings.Repeat(snip, reps)[:targetBytes]
		}
		promptLens[i] = len(txt)
		approxTok := it.EstInputTokens
		if approxTok <= 0 {
			approxTok = EstimatePromptTokens(txt, nil)
		}
		probeTok := approxTok
		if maxCtx > 512 && probeTok+256 > maxCtx {
			probeTok = maxCtx - 256
		}
		probeMedia := it.HasMedia && catHasVision
		shipReqs[i] = ShippingRequest{Text: txt, ApproxTokens: probeTok, HasMedia: probeMedia}
		ensoReqs[i] = RouteRequest{
			Org:                   "org-eval-acceptance",
			Prompt:                txt,
			HasMedia:              probeMedia,
			EstInputTokens:        probeTok,
			EstOutputTokens:       256,
			QualityRetentionRatio: 1.0,
			QualityTolerance:      0.04,
		}
		// Ensure every perf probe exercises full feature extraction + scoring rather than fast 422 exits.
		if _, err := candidate.Route(ensoReqs[i], StrategyEnsoHybridConstrained); err != nil {
			ensoReqs[i].HasMedia = false
			ensoReqs[i].EstInputTokens = 256
			shipReqs[i].HasMedia = false
			shipReqs[i].ApproxTokens = 256
		}
	}
	sort.Ints(promptLens)
	medBytes := percentileInt(promptLens, 50)

	measure := func(fn func(i int), batchSize int) RoutingPerfSummary {
		for i := 0; i < 32; i++ {
			fn(i % len(items))
		}
		if batchSize < 1 {
			batchSize = 1
		}
		numSamples := iters / batchSize
		if numSamples < 50 {
			numSamples = 50
			batchSize = 1
		}
		samples := make([]int64, numSamples)
		var totalNs int64
		startAll := time.Now()
		for s := 0; s < numSamples; s++ {
			t0 := time.Now()
			for b := 0; b < batchSize; b++ {
				fn((s*batchSize + b) % len(items))
			}
			d := time.Since(t0).Nanoseconds() / int64(batchSize)
			samples[s] = d
			totalNs += d
		}
		wallSec := time.Since(startAll).Seconds()
		sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })

		const workers = 8
		perWorker := iters / workers
		if perWorker < 50 {
			perWorker = 50
		}
		var wg sync.WaitGroup
		wg.Add(workers)
		cStart := time.Now()
		for w := 0; w < workers; w++ {
			offset := w * 17
			go func() {
				defer wg.Done()
				for i := 0; i < perWorker; i++ {
					fn((offset + i) % len(items))
				}
			}()
		}
		wg.Wait()
		cWall := time.Since(cStart).Seconds()

		totalCalls := numSamples * batchSize
		qps := 0.0
		if wallSec > 0 {
			qps = float64(totalCalls) / wallSec
		}
		cqps := 0.0
		if cWall > 0 {
			cqps = float64(workers*perWorker) / cWall
		}
		return RoutingPerfSummary{
			Iterations:     totalCalls,
			MeanNs:         round1f(float64(totalNs) / float64(numSamples)),
			P50Ns:          percentileInt64(samples, 50),
			P95Ns:          percentileInt64(samples, 95),
			P99Ns:          percentileInt64(samples, 99),
			ThroughputQPS:  round1f(qps),
			Concurrent8QPS: round1f(cqps),
		}
	}

	ctx := context.Background()
	var slo ShippingSlo
	// Use batchSize=1 across all three paths so p50/p95/p99 tail quantiles are strictly comparable.
	shipPerf := measure(func(i int) {
		_ = shipping.RouteDecision(ctx, shipReqs[i], slo)
	}, 1)
	candPerf := measure(func(i int) {
		_, _ = candidate.Route(ensoReqs[i], StrategyEnsoHybridConstrained)
	}, 1)

	// Measure with in-memory ledger writes enabled (bounded rotating request IDs so
	// benchmark measures lock contention honestly without leaking unbounded RAM).
	ledgerRouter := NewEnsoRouter(candidate.catalog, candidate.baseWeights)
	ledgerRouter.SetOrgPolicy("org-eval-acceptance", ScopePolicy{PlanTier: PlanEnterprise, HasPlanTier: true})
	ledgerPerf := measure(func(i int) {
		req := ensoReqs[i]
		req.RequestID = fmt.Sprintf("bench-%d", i%64)
		_, _ = ledgerRouter.Route(req, StrategyEnsoHybridConstrained)
	}, 1)

	return PerformanceComparison{
		PromptLengthMedianBytes: medBytes,
		ShippingHeuristic:       shipPerf,
		CandidateHybrid:         candPerf,
		CandidateWithLedger:     ledgerPerf,
	}
}

// RunReliabilityAudit verifies all four reliability contract domains over any
// catalog (LiveBench, Zen6, or private enso-bench) without leaking any concrete
// model IDs into aggregate-only check detail strings.
func RunReliabilityAudit(modelsCfg *HanzoModelsConfig, arms []ModelArm, weights *TrainedWeights) ReliabilitySummary {
	r := NewEnsoRouter(arms, weights)
	r.SetOrgPolicy("org-rel", ScopePolicy{PlanTier: PlanEnterprise, HasPlanTier: true})

	armByID := make(map[string]ModelArm, len(arms))
	poolSet := make(map[string]bool, len(arms))
	hasVisionArm := false
	hasToolsArm := false
	hasLongCtxArm := false
	for _, a := range arms {
		armByID[a.ID] = a
		poolSet[a.ID] = true
		if a.SupportsVision {
			hasVisionArm = true
		}
		if a.SupportsForcedToolChoice {
			hasToolsArm = true
		}
		if a.ContextWindow >= 151000 {
			hasLongCtxArm = true
		}
	}

	var checks []ReliabilityCheckResult
	add := func(cat, name string, ok bool, detail string) {
		checks = append(checks, ReliabilityCheckResult{
			Category: cat,
			Check:    name,
			Passed:   ok,
			Detail:   detail,
		})
	}

	// 1. Budget exhaustion
	{
		_, err := r.Route(RouteRequest{
			Org: "org-rel", Prompt: "hello", EstInputTokens: 200, EstOutputTokens: 100,
			FastMode: true, HasPrepaidBalance: true, PrepaidBalanceMicros: 0,
		}, StrategyEnsoHybridConstrained)
		var re *RouteError
		ok := IsRouteErrorCode(err, ErrCodeBudgetExhausted)
		if ok {
			re = err.(*RouteError)
			ok = re.HTTPStatus == 402 && re.Layer == "tenant_ledger" && re.BillingTopUpHint
		}
		add("budget_exhausted", "fast_mode_zero_prepaid_402", ok, "fail-closed HTTP 402 tenant_ledger with top-up hint")

		// Exclude $0/$0 free arms when testing that paid arms fail closed under MaxCostMicros=1;
		// if free arms exist, also verify they remain routable at 0 micro-USD.
		freeArms := make(map[string]bool)
		for _, a := range arms {
			if a.InputUSDPerMTok == 0 && a.OutputUSDPerMTok == 0 {
				freeArms[a.ID] = true
			}
		}
		if len(freeArms) < len(arms) {
			_, errSLO := r.Route(RouteRequest{
				Org: "org-rel", Prompt: "hello", EstInputTokens: 5000, EstOutputTokens: 2000,
				MaxCostMicros: 1, UnavailableModels: freeArms,
			}, StrategyEnsoHybridConstrained)
			okSLO := IsRouteErrorCode(errSLO, ErrCodeNoEligibleModel)
			if okSLO && len(freeArms) > 0 {
				decFree, errFree := r.Route(RouteRequest{
					Org: "org-rel", Prompt: "hello", EstInputTokens: 5000, EstOutputTokens: 2000,
					MaxCostMicros: 1,
				}, StrategyEnsoHybridConstrained)
				okSLO = errFree == nil && freeArms[decFree.RoutedModel] && decFree.EstimatedCostMicros == 0
			}
			add("budget_exhausted", "slo_max_cost_ceiling_422", okSLO, "tight MaxCostMicros rejects paid arms with 422 GateSLOCeiling (while permitting $0/$0 free arms if present)")
		} else {
			decFree, errFree := r.Route(RouteRequest{
				Org: "org-rel", Prompt: "hello", EstInputTokens: 5000, EstOutputTokens: 2000,
				MaxCostMicros: 1,
			}, StrategyEnsoHybridConstrained)
			add("budget_exhausted", "slo_max_cost_ceiling_422", errFree == nil && decFree.EstimatedCostMicros == 0, "all catalog arms are zero-cost; tight MaxCostMicros permits $0/$0 arms cleanly")
		}
	}

	// 2. Unavailable models (derived dynamically from the active catalog, never hardcoded)
	{
		downCount := 3
		if len(arms)-1 < downCount {
			downCount = len(arms) - 1
		}
		unavail := make(map[string]bool, downCount)
		// Disable the highest-priced arms in the active pool.
		for i := len(arms) - 1; i >= len(arms)-downCount; i-- {
			unavail[arms[i].ID] = true
		}
		dec, err := r.Route(RouteRequest{
			Org: "org-rel", Prompt: "Write Go code to sort a slice.", EstInputTokens: 120, EstOutputTokens: 120,
			UnavailableModels: unavail,
		}, StrategyEnsoHybridConstrained)
		ok := err == nil && !unavail[dec.RoutedModel]
		add("unavailable_models", "outage_failover_skips_down_arms", ok, fmt.Sprintf("routed cleanly to healthy backup arm while %d primary arms unavailable", downCount))

		allDown := make(map[string]bool, len(arms))
		for _, a := range arms {
			allDown[a.ID] = true
		}
		_, errAll := r.Route(RouteRequest{
			Org: "org-rel", Prompt: "hi", EstInputTokens: 20, EstOutputTokens: 20, UnavailableModels: allDown,
		}, StrategyEnsoHybridConstrained)
		add("unavailable_models", "total_outage_fails_loud_422", IsRouteErrorCode(errAll, ErrCodeNoEligibleModel), "all arms down returns HTTP 422 no_eligible_model")

		// ShippingClient hard allowlist invariant using synthetic arm labels (never leaks real IDs).
		sc := NewShippingClient(
			ShippingPolicy{Prefer: map[string][]string{"default": {"arm-disabled", "arm-allowed"}}},
			func(string) bool { return false },
			func(id string) bool { return id == "arm-allowed" },
		)
		sd := sc.RouteDecision(context.Background(), ShippingRequest{Text: "hello", ApproxTokens: 30}, ShippingSlo{})
		add("unavailable_models", "shipping_last_resort_honors_allowlist", sd.Model == "arm-allowed", "relaxed servability to org-allowed arm without bypassing Allow")
	}

	// 3. Capability mismatch (handles both capable and fail-closed catalogs cleanly)
	{
		decVis, errVis := r.Route(RouteRequest{
			Org: "org-rel", Prompt: "Describe this chart.", HasMedia: true, EstInputTokens: 200, EstOutputTokens: 120,
		}, StrategyEnsoHybridConstrained)
		if hasVisionArm {
			add("capability_mismatch", "vision_blocks_text_only_models", errVis == nil && armByID[decVis.RoutedModel].SupportsVision, "vision request routed strictly to vision-capable arm")
		} else {
			add("capability_mismatch", "vision_blocks_text_only_models", IsRouteErrorCode(errVis, ErrCodeNoEligibleModel), "vision request failed closed with 422 when catalog has no vision arm")
		}

		decTools, errTools := r.Route(RouteRequest{
			Org: "org-rel", Prompt: "Call weather tool.", ToolChoice: ToolChoiceRequired, ToolCount: 2, EstInputTokens: 120, EstOutputTokens: 100,
		}, StrategyEnsoHybridConstrained)
		if hasToolsArm {
			add("capability_mismatch", "forced_tool_choice_blocks_non_tool_arms", errTools == nil && armByID[decTools.RoutedModel].SupportsForcedToolChoice, "forced tool request routed strictly to tool-capable arm")
		} else {
			add("capability_mismatch", "forced_tool_choice_blocks_non_tool_arms", IsRouteErrorCode(errTools, ErrCodeNoEligibleModel), "forced tool request failed closed with 422 when catalog has no tool-capable arm")
		}

		decCtx, errCtx := r.Route(RouteRequest{
			Org: "org-rel", Prompt: "Summarize long transcript.", EstInputTokens: 150000, EstOutputTokens: 1000,
		}, StrategyEnsoHybridConstrained)
		if hasLongCtxArm {
			add("capability_mismatch", "context_window_blocks_128k_arms", errCtx == nil && armByID[decCtx.RoutedModel].ContextWindow >= 151000, "151k-token request routed strictly to >=200k-context arm")
		} else {
			add("capability_mismatch", "context_window_blocks_128k_arms", IsRouteErrorCode(errCtx, ErrCodeNoEligibleModel), "151k-token request failed closed with 422 when all arms have <=128k window")
		}

		nonChatExcluded := true
		for _, a := range arms {
			var def HanzoModelDef
			if modelsCfg != nil {
				def = modelsCfg.Models[a.ID]
			}
			if IsNonChatModel(a.ID, def) {
				nonChatExcluded = false
			}
		}
		add("capability_mismatch", "non_chat_models_excluded_from_chat_pool", nonChatExcluded, "decision/speech/embedding/video models excluded from chat routing pool")
	}

	// 4. Fallback behavior (HTTP engine healthy, 500, timeout, cold-start, strict quality floor)
	{
		okSrv := httptest.NewServer(NewEnsoEngineHTTPHandler(r, "org-rel", 0.04))
		defer okSrv.Close()
		pol := PoolAdaptedShippingPolicy(modelsCfg, arms)
		cli := NewShippingClient(pol, func(id string) bool { return poolSet[id] }, func(id string) bool { return poolSet[id] })
		cli.Endpoint = okSrv.URL
		dEngine := cli.RouteDecision(context.Background(), ShippingRequest{Text: "Write a Go function to merge intervals.", ApproxTokens: 60}, ShippingSlo{})
		add("fallback_behavior", "shipping_client_delegates_to_enso_http_adapter", dEngine.Source == ShippingSourceEngine && poolSet[dEngine.Model], "HTTP /route delegated cleanly to Enso adapter")

		err500 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) }))
		defer err500.Close()
		cli.Endpoint = err500.URL
		d500 := cli.RouteDecision(context.Background(), ShippingRequest{Text: "Write a Go function.", ApproxTokens: 40}, ShippingSlo{})
		add("fallback_behavior", "engine_500_falls_back_to_heuristic", d500.Source == ShippingSourceHeuristic && d500.Model != "" && cli.Misses() == 1, fmt.Sprintf("HTTP 500 fell back cleanly to heuristic (misses=%d)", cli.Misses()))

		slowSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			time.Sleep(80 * time.Millisecond)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"model":"slow-engine-arm","task":"general","confidence":0.9}`))
		}))
		defer slowSrv.Close()
		cli.Endpoint = slowSrv.URL
		cli.Timeout = 15 * time.Millisecond
		dSlow := cli.RouteDecision(context.Background(), ShippingRequest{Text: "hi", ApproxTokens: 4}, ShippingSlo{})
		add("fallback_behavior", "engine_timeout_falls_back_within_cap", dSlow.Source == ShippingSourceHeuristic && dSlow.Model != "" && cli.Misses() == 2, fmt.Sprintf("engine timeout fell back cleanly to heuristic (misses=%d)", cli.Misses()))

		coldRouter := NewEnsoRouter(arms, nil)
		coldRouter.SetOrgPolicy("org-rel", ScopePolicy{PlanTier: PlanEnterprise, HasPlanTier: true})
		dCold, errCold := coldRouter.Route(RouteRequest{Org: "org-rel", Prompt: "Explain HTTP/2 multiplexing.", EstInputTokens: 40, EstOutputTokens: 100}, StrategyEnsoHybridConstrained)
		add("fallback_behavior", "cold_start_nil_weights_uses_rule_fallback", errCold == nil && dCold.FallbackUsed == "cold_start_rule", "cold start with nil weights fell back cleanly to rule path")

		_, errStrict := r.Route(RouteRequest{
			Org: "org-rel", Prompt: "Hard question", EstInputTokens: 60, EstOutputTokens: 60,
			QualityFloor: 1.0, StrictQualityFloor: true,
		}, StrategyEnsoHybridConstrained)
		add("fallback_behavior", "strict_quality_floor_rejects_422", IsRouteErrorCode(errStrict, ErrCodeQualityFloorUnmet), "unachievable strict quality floor returns HTTP 422")
	}

	passed := 0
	for _, c := range checks {
		if c.Passed {
			passed++
		}
	}
	return ReliabilitySummary{
		TotalChecks:  len(checks),
		PassedChecks: passed,
		AllPassed:    passed == len(checks),
		Checks:       checks,
	}
}

// MarshalAcceptanceJSON serializes an AcceptanceReport as canonical indented JSON.
func MarshalAcceptanceJSON(report AcceptanceReport) ([]byte, error) {
	b, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}
