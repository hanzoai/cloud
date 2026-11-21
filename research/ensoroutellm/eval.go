// Copyright 2026 Hanzo AI, Inc.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package ensoroutellm

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
)

// EvalDomain names the benchmark task categories in the reproducible evaluation suite.
type EvalDomain string

const (
	DomainSimpleQA         EvalDomain = "simple_qa_extraction"
	DomainMultilingualChat EvalDomain = "multilingual_chat_summary"
	DomainRAGSynthesis     EvalDomain = "rag_evidence_synthesis"
	DomainToolSchemaStrict EvalDomain = "tool_schema_orchestration"
	DomainLiveCodeSystems  EvalDomain = "livecode_systems_programming"
	DomainGPQAFormalMath   EvalDomain = "gpqa_formal_reasoning"
	DomainAdversarialTerse EvalDomain = "adversarial_terse_math"
)

// LabeledTask is one benchmark item with realistic prompt text, request metadata,
// and per-arm binary pass/fail outcomes across the catalog.
type LabeledTask struct {
	QueryID     string             `json:"query_id"`
	Domain      EvalDomain         `json:"domain"`
	Request     RouteRequest       `json:"request"`
	ArmOutcomes map[string]float64 `json:"arm_outcomes"`
}

func trueArmPassProb(domain EvalDomain, armID string, itemDifficulty float64) float64 {
	// Ground-truth latent capability matrix (probabilities in [0, 1] before
	// item-level difficulty perturbation). Note that the router NEVER observes
	// domain labels or this table; it only observes raw prompt text + request
	// metadata via ExtractFeatures.
	base := 0.50
	switch domain {
	case DomainSimpleQA:
		switch armID {
		case "zen-4-flash":
			base = 0.93
		case "ext-oss-70b-30d":
			base = 0.92
		case "zen-4-mini":
			base = 0.95
		case "zen-4-coder":
			base = 0.94
		default:
			base = 0.97
		}

	case DomainMultilingualChat:
		switch armID {
		case "zen-4-flash":
			base = 0.72
		case "ext-oss-70b-30d":
			base = 0.80
		case "zen-4-mini":
			base = 0.91
		case "zen-4-coder":
			base = 0.85
		case "zen-4-pro", "zen-4-pro-fast", "frontier-claude-sonnet":
			base = 0.94
		default:
			base = 0.96
		}

	case DomainRAGSynthesis:
		switch armID {
		case "zen-4-flash":
			base = 0.36
		case "ext-oss-70b-30d":
			base = 0.58
		case "zen-4-mini":
			base = 0.66
		case "zen-4-coder":
			base = 0.72
		case "zen-4-pro", "zen-4-pro-fast":
			base = 0.91
		case "frontier-claude-sonnet":
			base = 0.92
		case "zen-4-thinking", "frontier-o-reasoner-30d":
			base = 0.93
		case "zen-4-max":
			base = 0.95
		}

	case DomainToolSchemaStrict:
		switch armID {
		case "zen-4-flash":
			base = 0.40
		case "ext-oss-70b-30d":
			base = 0.25
		case "zen-4-mini":
			base = 0.70
		case "zen-4-coder":
			base = 0.91
		case "zen-4-pro", "zen-4-pro-fast":
			base = 0.92
		case "frontier-claude-sonnet":
			base = 0.93
		case "zen-4-thinking":
			base = 0.30
		case "frontier-o-reasoner-30d":
			base = 0.93
		case "zen-4-max":
			base = 0.95
		}

	case DomainLiveCodeSystems:
		switch armID {
		case "zen-4-flash":
			base = 0.20
		case "ext-oss-70b-30d":
			base = 0.44
		case "zen-4-mini":
			base = 0.48
		case "zen-4-coder":
			base = 0.90 // Specialist coder beats mid-tier generalists at $1.20/$4.80!
		case "zen-4-pro", "zen-4-pro-fast":
			base = 0.83
		case "frontier-claude-sonnet":
			base = 0.89
		case "zen-4-thinking", "frontier-o-reasoner-30d":
			base = 0.91
		case "zen-4-max":
			base = 0.93
		}

	case DomainGPQAFormalMath, DomainAdversarialTerse:
		switch armID {
		case "zen-4-flash":
			base = 0.14
		case "ext-oss-70b-30d":
			base = 0.26
		case "zen-4-mini":
			base = 0.28
		case "zen-4-coder":
			base = 0.52
		case "zen-4-pro", "zen-4-pro-fast":
			base = 0.64
		case "frontier-claude-sonnet":
			base = 0.72
		case "zen-4-thinking":
			base = 0.89 // Reasoner excels on formal math/GPQA at $4.50/$18 vs $12/$48!
		case "frontier-o-reasoner-30d":
			base = 0.90
		case "zen-4-max":
			base = 0.92
		}
	}

	logit := math.Log(base/(1.0-base)) - 1.15*itemDifficulty
	return sigmoid(logit)
}

func synthesizePromptForDomain(domain EvalDomain, idx int, tier int, rng *RNG) (string, int, int, ToolChoiceMode, int, ThinkingMode) {
	// tier in {0=easy, 1=medium, 2=hard} modulates realistic structural complexity
	// (constraints, code/equation depth, token length, and reasoning budget) within
	// each domain, mirroring LiveCodeBench Easy/Med/Hard and MMLU->GPQA progressions.
	variants := []string{"alpha", "beta", "gamma", "delta", "omega", "sigma"}
	tag := variants[rng.IntN(len(variants))]

	extraConstraints := ""
	if tier == 1 {
		extraConstraints = "\n- Constraint A: handle edge-case boundary conditions explicitly"
	} else if tier == 2 {
		extraConstraints = "\n- Constraint A: must prove strict invariant preservation step-by-step\n- Constraint B: verify every constraint under adversarial concurrency/skew\n1. Formalize precondition\n2. Derive complexity bound"
	}

	switch domain {
	case DomainSimpleQA:
		prompts := []string{
			fmt.Sprintf("Hello! In one sentence, define latency jitter and extract email ops-%s@example.org.", tag),
			fmt.Sprintf("Classify sentiment and shorten this greeting note (%s): Thanks for the quick update!", tag),
			fmt.Sprintf("What is the capital city referenced in ticket %d (%s)? Give a short tldr.", idx, tag),
		}
		return prompts[idx%len(prompts)] + extraConstraints, 70 + tier*90 + rng.IntN(50), 50 + tier*40 + rng.IntN(30), ToolChoiceNone, 0, ThinkingOff

	case DomainMultilingualChat:
		prompts := []string{
			fmt.Sprintf("Traduce y genera un resumen ejecutivo en español y português del reporte trimestral %s-%d manteniendo el tono formal.", tag, idx),
			fmt.Sprintf("Please translate and localize this bilingual customer notice (%s) into français and deutsch without losing idioms.", tag),
		}
		return prompts[idx%len(prompts)] + extraConstraints, 220 + tier*260 + rng.IntN(120), 140 + tier*90 + rng.IntN(60), ToolChoiceNone, 0, ThinkingOff

	case DomainRAGSynthesis:
		prompt := fmt.Sprintf(
			"Context block [Source A-%d]: According to source telemetry, storage shard %s reported 99.4%% availability.\n"+
				"Context block [Source B-%d]: Excerpt from incident postmortem contradicts shard %s metrics during failover.\n"+
				"Synthesize both passages, cross-reference every citation and evidence span:%s",
			idx, tag, idx, tag, extraConstraints,
		)
		return prompt, 1200 + tier*1800 + rng.IntN(600), 260 + tier*200 + rng.IntN(100), ToolChoiceNone, 0, ThinkingOff

	case DomainToolSchemaStrict:
		prompt := fmt.Sprintf(
			"Invoke the billing reconciliation function call using strict schema validation (openapi rfc3339 timestamps, idempotency_key=%s-%d).%s",
			tag, idx, extraConstraints,
		)
		return prompt, 350 + tier*450 + rng.IntN(180), 160 + tier*110 + rng.IntN(70), ToolChoiceAuto, 2 + tier*3, ThinkingOff

	case DomainLiveCodeSystems:
		codeBody := "```go\nfunc Add(a, b int) int { return a + b }\n```"
		if tier == 1 {
			codeBody = "```go\nfunc ScheduleBatch(ctx context.Context, jobs []Job) ([]Result, error) {\n    // implement mutex worker pool\n}\n```"
		} else if tier == 2 {
			codeBody = "```go\nfunc ReconcileDAG(ctx context.Context, g *Graph) error {\n    if g == nil { return nil }\n    // refactor goroutine leak, dynamic programming cut, and SQL select lock order\n}\n```"
		}
		prompt := fmt.Sprintf("Implement golang systems component (%s_%d):\n%s%s", tag, idx, codeBody, extraConstraints)
		return prompt, 400 + tier*750 + rng.IntN(250), 280 + tier*320 + rng.IntN(150), ToolChoiceNone, 0, ThinkingOff

	case DomainGPQAFormalMath:
		think := ThinkingOff
		mathExpr := "Solve linear equation 3x + 5 = 20."
		if tier == 1 {
			think = ThinkingAdaptive
			mathExpr = "Prove by induction that \\sum_{k=1}^{n} k = n(n+1)/2 under modulo constraint."
		} else if tier == 2 {
			think = ThinkingAdaptive
			mathExpr = "Prove eigenvalue inequality \\forall x \\neq 0, x^T M x \\ge \\lambda_{min} ||x||^2 = 1 and deduce Bayesian thermodynamic equilibrium."
		}
		prompt := fmt.Sprintf("Mathematical problem (%s_%d): %s%s", tag, idx, mathExpr, extraConstraints)
		return prompt, 260 + tier*520 + rng.IntN(200), 300 + tier*420 + rng.IntN(150), ToolChoiceNone, 0, think

	case DomainAdversarialTerse:
		// Intentionally devoid of math/LaTeX/code keywords and ultra-short (<25 tokens)
		// to test H4 expected failure mode on deceptively terse hard queries!
		primes := []int{47, 53, 59, 61, 67, 71, 73}
		p := primes[idx%len(primes)]
		prompt := fmt.Sprintf("Find the smallest positive whole number k where 2 to the k minus 1 divides evenly by %d (case %d).", p, idx)
		return prompt, 28, 320, ToolChoiceNone, 0, ThinkingOff

	default:
		return "Summarize status.", 64, 64, ToolChoiceNone, 0, ThinkingOff
	}
}

// GenerateStandardCorpus synthesizes n deterministic, domain-balanced labeled
// tasks across the six core benchmark domains with realistic Easy/Medium/Hard
// intra-domain difficulty tiers.
func GenerateStandardCorpus(n int, catalog []ModelArm, seed uint64) []LabeledTask {
	rng := NewRNG(seed)
	domains := []EvalDomain{
		DomainSimpleQA,
		DomainMultilingualChat,
		DomainRAGSynthesis,
		DomainToolSchemaStrict,
		DomainLiveCodeSystems,
		DomainGPQAFormalMath,
	}
	tierOffsets := [3]float64{-0.95, 0.0, 1.05}

	tasks := make([]LabeledTask, n)
	for i := 0; i < n; i++ {
		dom := domains[i%len(domains)]
		tier := (i / len(domains)) % 3
		diff := tierOffsets[tier] + 0.20*rng.NormFloat64()
		prompt, inTok, outTok, tcMode, toolCnt, thinkMode := synthesizePromptForDomain(dom, i, tier, rng)
		qid := fmt.Sprintf("qry-%04d", i)

		outcomes := make(map[string]float64, len(catalog))
		for _, arm := range catalog {
			p := trueArmPassProb(dom, arm.ID, diff)
			if rng.Float64() < p {
				outcomes[arm.ID] = 1.0
			} else {
				outcomes[arm.ID] = 0.0
			}
		}

		tasks[i] = LabeledTask{
			QueryID: qid,
			Domain:  dom,
			Request: RouteRequest{
				RequestID:            qid,
				Org:                  "org-eval-enterprise",
				Prompt:               prompt,
				EstInputTokens:       inTok,
				EstOutputTokens:      outTok,
				RequireZeroRetention: true,
				ToolChoice:           tcMode,
				ToolCount:            toolCnt,
				ThinkingMode:         thinkMode,
			},
			ArmOutcomes: outcomes,
		}
	}
	return tasks
}

// GenerateAdversarialTerseSlice generates n deceptively short hard reasoning
// prompts (H4 expected failure mode stress slice).
func GenerateAdversarialTerseSlice(n int, catalog []ModelArm, seed uint64) []LabeledTask {
	rng := NewRNG(seed)
	tasks := make([]LabeledTask, n)
	for i := 0; i < n; i++ {
		diff := 0.25 + 0.25*math.Abs(rng.NormFloat64())
		prompt, inTok, outTok, tcMode, toolCnt, thinkMode := synthesizePromptForDomain(DomainAdversarialTerse, i, 2, rng)
		qid := fmt.Sprintf("adv-%04d", i)

		outcomes := make(map[string]float64, len(catalog))
		for _, arm := range catalog {
			p := trueArmPassProb(DomainAdversarialTerse, arm.ID, diff)
			if rng.Float64() < p {
				outcomes[arm.ID] = 1.0
			} else {
				outcomes[arm.ID] = 0.0
			}
		}

		tasks[i] = LabeledTask{
			QueryID: qid,
			Domain:  DomainAdversarialTerse,
			Request: RouteRequest{
				RequestID:            qid,
				Org:                  "org-eval-enterprise",
				Prompt:               prompt,
				EstInputTokens:       inTok,
				EstOutputTokens:      outTok,
				RequireZeroRetention: true,
				ToolChoice:           tcMode,
				ToolCount:            toolCnt,
				ThinkingMode:         thinkMode,
			},
			ArmOutcomes: outcomes,
		}
	}
	return tasks
}

// PolicyEvalMetrics summarizes cost, quality, latency, and retention metrics
// for one routing policy configuration across held-out folds.
type PolicyEvalMetrics struct {
	PolicyName           string  `json:"policy_name"`
	Queries              int     `json:"queries"`
	Accuracy             float64 `json:"accuracy"`
	QualityRetentionPct  float64 `json:"quality_retention_pct"`
	BlendedUSDPerMTok    float64 `json:"blended_usd_per_mtok"`
	USDPer1kQueries      float64 `json:"usd_per_1k_queries"`
	BlendedCostSavingPct float64 `json:"blended_cost_saving_pct"`
	USDCostSavingPct     float64 `json:"usd_cost_saving_pct"`
	MeanLatencyMs        float64 `json:"mean_latency_ms"`
	P95LatencyMs         int     `json:"p95_latency_ms"`
}

// CalibrationSummary captures out-of-fold probability calibration and RouteLLM
// Pareto-curve metrics (APGR, CPT-80, CPT-90).
type CalibrationSummary struct {
	ECE10Bin float64 `json:"ece_10_bin"`
	Brier    float64 `json:"brier_score"`
	APGR     float64 `json:"apgr"`
	CPT80Pct float64 `json:"cpt_80_spend_pct"`
	CPT90Pct float64 `json:"cpt_90_spend_pct"`
}

// GateScenarioResult records compliance on one capability-gate test scenario.
type GateScenarioResult struct {
	Scenario                 string `json:"scenario"`
	Requests                 int    `json:"requests"`
	UnconstrainedViolations  int    `json:"unconstrained_violations"`
	ConstrainedViolations    int    `json:"constrained_violations"`
	ExpectedRejectionHandled bool   `json:"expected_rejection_handled"`
}

// GateAuditSummary aggregates capability & security gate audit results.
type GateAuditSummary struct {
	TotalScenarios               int                  `json:"total_scenarios"`
	TotalRequests                int                  `json:"total_requests"`
	UnconstrainedTotalViolations int                  `json:"unconstrained_total_violations"`
	ConstrainedTotalViolations   int                  `json:"constrained_total_violations"`
	Scenarios                    []GateScenarioResult `json:"scenarios"`
}

// AdversarialSliceSummary captures H4 out-of-distribution terse prompt behavior.
type AdversarialSliceSummary struct {
	Queries              int     `json:"queries"`
	AlwaysStrongAccuracy float64 `json:"always_strong_accuracy"`
	AlwaysWeakAccuracy   float64 `json:"always_weak_accuracy"`
	LooseTol008Accuracy  float64 `json:"enso_loose_tol_008_accuracy"`
	TightTol002Accuracy  float64 `json:"enso_tight_tol_002_accuracy"`
	HighFloor085Accuracy float64 `json:"enso_abs_floor_085_accuracy"`
}

// SparseComparisonSummary compares isolated Logistic vs MF vs Enso Hybrid when
// training logs only observe 1 historical arm per query (85.7% missing labels).
type SparseComparisonSummary struct {
	Queries              int     `json:"queries"`
	ObservedArmsPerQuery int     `json:"observed_arms_per_query"`
	LogisticOnlyBrier    float64 `json:"logistic_only_brier"`
	MatrixFactBrier      float64 `json:"matrix_factorization_brier"`
	EnsoHybridBrier      float64 `json:"enso_hybrid_brier"`
	LogisticOnlyAcc      float64 `json:"logistic_only_rel004_acc"`
	MatrixFactAcc        float64 `json:"matrix_factorization_rel004_acc"`
	EnsoHybridAcc        float64 `json:"enso_hybrid_rel004_acc"`
	EnsoHybridSavingsPct float64 `json:"enso_hybrid_rel004_usd_savings_pct"`
}

// BenchmarkReport is the top-level deterministic JSON artifact committed in testdata/.
type BenchmarkReport struct {
	Schema           string                  `json:"schema"`
	Benchmark        string                  `json:"benchmark"`
	Issues           []string                `json:"issues"`
	TrackingHIP      string                  `json:"tracking_hip"`
	RelatedHIPs      []string                `json:"related_hips"`
	Seed             uint64                  `json:"seed"`
	DenseFolds       int                     `json:"dense_cv_folds"`
	DenseCorpusSize  int                     `json:"dense_corpus_size"`
	Calibration      CalibrationSummary      `json:"dense_calibration"`
	DensePolicies    []PolicyEvalMetrics     `json:"dense_policies"`
	SparseComparison SparseComparisonSummary `json:"sparse_bandit_comparison"`
	GateAudit        GateAuditSummary        `json:"capability_gate_audit"`
	AdversarialSlice AdversarialSliceSummary `json:"adversarial_short_prompt_slice"`
}

func round4f(x float64) float64 {
	return math.Round(x*10_000) / 10_000
}

func round2f(x float64) float64 {
	return math.Round(x*100) / 100
}

type policySpec struct {
	name     string
	strategy RouterStrategy
	tol      float64
	floor    float64
}

// RunBenchmarkSuite executes the complete 4-part reproducible RouteLLM / Enso
// evaluation suite on fixed deterministic seeds.
func RunBenchmarkSuite(seed uint64) BenchmarkReport {
	catalog := DefaultCatalog()
	enterprisePolicy := ScopePolicy{
		PlanTier:             PlanEnterprise,
		HasPlanTier:          true,
		RequireZeroRetention: true,
	}

	// 1. Dense 5-fold Stratified Cross-Validation (n = 600 tasks across 6 domains).
	const denseN = 600
	const folds = 5
	corpus := GenerateStandardCorpus(denseN, catalog, seed)

	policies := []policySpec{
		{name: "always_strong(zen-4-max)", strategy: StrategyAlwaysStrong},
		{name: "always_weak(zen-4-flash)", strategy: StrategyAlwaysWeak},
		{name: "random_router", strategy: StrategyRandom},
		{name: "static_rule_prefer(HIP-0510)", strategy: StrategyStaticRule},
		{name: "routellm_mf_rel(tol=0.04)", strategy: StrategyRouteLLMMFConstrained, tol: 0.04},
		{name: "enso_hybrid_rel(tol=0.02)", strategy: StrategyEnsoHybridConstrained, tol: 0.02},
		{name: "enso_hybrid_rel(tol=0.04)", strategy: StrategyEnsoHybridConstrained, tol: 0.04},
		{name: "enso_hybrid_rel(tol=0.06)", strategy: StrategyEnsoHybridConstrained, tol: 0.06},
		{name: "enso_hybrid_abs(floor=0.80)", strategy: StrategyEnsoHybridConstrained, floor: 0.80},
	}

	type accum struct {
		passSum    float64
		blendedSum float64
		usdSum     float64
		latSum     int
		lats       []int
	}
	accMap := make(map[string]*accum, len(policies))
	for _, ps := range policies {
		accMap[ps.name] = &accum{lats: make([]int, 0, denseN)}
	}

	var oofPreds []float64
	var oofActuals []float64

	// Also collect out-of-fold weights per query so we can compute exact APGR & CPT
	// across a fine 13-point tolerance sweep.
	foldWeights := make([]*TrainedWeights, folds)

	for fold := 0; fold < folds; fold++ {
		var trainSamples []TrainingSample
		for i, task := range corpus {
			if i%folds == fold {
				continue
			}
			feats := ExtractFeatures(task.Request)
			for _, arm := range catalog {
				// Train on ZDR-eligible enterprise arms.
				if !arm.ZeroRetention || arm.FastModeOnly {
					continue
				}
				trainSamples = append(trainSamples, TrainingSample{
					QueryID:  task.QueryID,
					Features: feats,
					ModelID:  arm.ID,
					Passed:   task.ArmOutcomes[arm.ID],
				})
			}
		}

		tw := FitTrainedWeights(trainSamples, catalog, seed^uint64(fold+101)*0xbf58476d1ce4e5b9)
		foldWeights[fold] = tw

		router := NewEnsoRouter(catalog, tw)
		router.SetOrgPolicy("org-eval-enterprise", enterprisePolicy)

		catalogByID := make(map[string]ModelArm, len(catalog))
		for _, a := range catalog {
			catalogByID[a.ID] = a
		}

		for i, task := range corpus {
			if i%folds != fold {
				continue
			}
			feats := ExtractFeatures(task.Request)
			for _, arm := range catalog {
				if !arm.ZeroRetention || arm.FastModeOnly {
					continue
				}
				if p, ok := tw.PredictQuality(feats, arm.ID, false); ok {
					oofPreds = append(oofPreds, p)
					oofActuals = append(oofActuals, task.ArmOutcomes[arm.ID])
				}
			}

			for _, ps := range policies {
				req := task.Request
				req.QualityTolerance = ps.tol
				req.QualityFloor = ps.floor
				dec, err := router.Route(req, ps.strategy)
				if err != nil {
					continue
				}
				arm := catalogByID[dec.RoutedModel]
				ac := accMap[ps.name]
				ac.passSum += task.ArmOutcomes[dec.RoutedModel]
				ac.blendedSum += arm.BlendedUSDPerMTok()
				ac.usdSum += float64(dec.EstimatedCostMicros) / 1_000_000.0
				ac.latSum += dec.EstimatedLatencyMs
				ac.lats = append(ac.lats, dec.EstimatedLatencyMs)
			}
		}
	}

	strongAcc := accMap["always_strong(zen-4-max)"].passSum / float64(denseN)
	weakAcc := accMap["always_weak(zen-4-flash)"].passSum / float64(denseN)
	strongBlended := accMap["always_strong(zen-4-max)"].blendedSum / float64(denseN)
	strongUSD := accMap["always_strong(zen-4-max)"].usdSum / float64(denseN)
	weakUSD := accMap["always_weak(zen-4-flash)"].usdSum / float64(denseN)

	var denseMetrics []PolicyEvalMetrics
	for _, ps := range policies {
		ac := accMap[ps.name]
		sort.Ints(ac.lats)
		p95 := 0
		if len(ac.lats) > 0 {
			idx := (95 * len(ac.lats)) / 100
			if idx >= len(ac.lats) {
				idx = len(ac.lats) - 1
			}
			p95 = ac.lats[idx]
		}
		acc := ac.passSum / float64(denseN)
		meanBlended := ac.blendedSum / float64(denseN)
		meanUSD := ac.usdSum / float64(denseN)

		denseMetrics = append(denseMetrics, PolicyEvalMetrics{
			PolicyName:           ps.name,
			Queries:              denseN,
			Accuracy:             round4f(acc),
			QualityRetentionPct:  round2f((acc / strongAcc) * 100.0),
			BlendedUSDPerMTok:    round2f(meanBlended),
			USDPer1kQueries:      round4f(meanUSD * 1000.0),
			BlendedCostSavingPct: round2f((1.0 - meanBlended/strongBlended) * 100.0),
			USDCostSavingPct:     round2f((1.0 - meanUSD/strongUSD) * 100.0),
			MeanLatencyMs:        round2f(float64(ac.latSum) / float64(denseN)),
			P95LatencyMs:         p95,
		})
	}

	ece, brier := computeECEAndBrier(oofPreds, oofActuals, 10)
	apgr, cpt80, cpt90 := computeAPGRAndCPT(corpus, foldWeights, catalog, enterprisePolicy, weakAcc, strongAcc, weakUSD, strongUSD)

	calSummary := CalibrationSummary{
		ECE10Bin: round4f(ece),
		Brier:    round4f(brier),
		APGR:     round4f(apgr),
		CPT80Pct: round2f(cpt80 * 100.0),
		CPT90Pct: round2f(cpt90 * 100.0),
	}

	// 2. Sparse Bandit Partial-Feedback Comparison (n = 180 tasks, only 1 observed arm/query).
	sparseSummary := runSparseBanditExperiment(catalog, enterprisePolicy, seed^0x13579bdf2468ace0)

	// 3. Hard Capability & Security Gate Audit (12 scenarios * 11 items = 132 requests).
	// Fit global shared base weights ("*", HIP-0510 §3.1) across all non-fast-only
	// arms so unconstrained routing observes realistic quality curves on 30-day
	// retention and non-tool-choice arms.
	var fullPoolSamples []TrainingSample
	for _, task := range corpus[:480] {
		feats := ExtractFeatures(task.Request)
		for _, arm := range catalog {
			if arm.FastModeOnly {
				continue
			}
			fullPoolSamples = append(fullPoolSamples, TrainingSample{
				QueryID:  task.QueryID,
				Features: feats,
				ModelID:  arm.ID,
				Passed:   task.ArmOutcomes[arm.ID],
			})
		}
	}
	globalAuditWeights := FitTrainedWeights(fullPoolSamples, catalog, seed^0x3141592653589793)
	gateSummary := runCapabilityGateAudit(catalog, globalAuditWeights, seed^0x2468ace013579bdf)

	// 4. Adversarial Short-Prompt Slice (n = 60 OOD terse math queries — H4).
	advSlice := GenerateAdversarialTerseSlice(60, catalog, seed^0xfedcba9876543210)
	advRouter := NewEnsoRouter(catalog, foldWeights[0])
	advRouter.SetOrgPolicy("org-eval-enterprise", enterprisePolicy)
	advSummary := evaluateAdversarialSlice(advRouter, advSlice)

	return BenchmarkReport{
		Schema:           "hanzo.ensoroutellm.results/v1",
		Benchmark:        "enso-routellm-quality-constrained-routing",
		Issues:           []string{"https://github.com/hanzoai/cloud/issues/34", "https://github.com/hanzoai/cloud/issues/27"},
		TrackingHIP:      "HIP-1345 §2.6",
		RelatedHIPs:      []string{"HIP-0510", "HIP-1332", "HIP-1346", "HIP-1348 §1.1–§1.2"},
		Seed:             seed,
		DenseFolds:       folds,
		DenseCorpusSize:  denseN,
		Calibration:      calSummary,
		DensePolicies:    denseMetrics,
		SparseComparison: sparseSummary,
		GateAudit:        gateSummary,
		AdversarialSlice: advSummary,
	}
}

func computeECEAndBrier(preds, actuals []float64, numBins int) (float64, float64) {
	if len(preds) == 0 {
		return 0, 0
	}
	binCnt := make([]int, numBins)
	binPredSum := make([]float64, numBins)
	binActSum := make([]float64, numBins)
	var brierSum float64

	for i, p := range preds {
		y := actuals[i]
		diff := p - y
		brierSum += diff * diff
		b := int(p * float64(numBins))
		if b >= numBins {
			b = numBins - 1
		}
		if b < 0 {
			b = 0
		}
		binCnt[b]++
		binPredSum[b] += p
		binActSum[b] += y
	}

	var ece float64
	n := float64(len(preds))
	for b := 0; b < numBins; b++ {
		if binCnt[b] == 0 {
			continue
		}
		avgP := binPredSum[b] / float64(binCnt[b])
		avgY := binActSum[b] / float64(binCnt[b])
		ece += (float64(binCnt[b]) / n) * math.Abs(avgP-avgY)
	}
	return ece, brierSum / n
}

func computeAPGRAndCPT(
	corpus []LabeledTask,
	foldWeights []*TrainedWeights,
	catalog []ModelArm,
	pol ScopePolicy,
	weakAcc, strongAcc, weakUSD, strongUSD float64,
) (float64, float64, float64) {
	gap := strongAcc - weakAcc
	if gap <= 1e-9 || strongUSD <= 1e-9 {
		return 1.0, 0, 0
	}

	tols := []float64{0.005, 0.015, 0.03, 0.045, 0.06, 0.08, 0.11, 0.15, 0.22, 0.35, 0.60, 0.80, 0.95}
	type pt struct {
		costRatio float64
		pgr       float64
	}
	weakCR := weakUSD / strongUSD
	if weakCR < 0 {
		weakCR = 0
	}
	if weakCR > 1 {
		weakCR = 1
	}
	pts := []pt{
		{costRatio: 0.0, pgr: 0.0},
		{costRatio: weakCR, pgr: 0.0},
		{costRatio: 1.0, pgr: 1.0},
	}

	folds := len(foldWeights)
	for _, tol := range tols {
		var passSum, usdSum float64
		for fold := 0; fold < folds; fold++ {
			router := NewEnsoRouter(catalog, foldWeights[fold])
			router.SetOrgPolicy("org-eval-enterprise", pol)
			for i, task := range corpus {
				if i%folds != fold {
					continue
				}
				req := task.Request
				req.QualityTolerance = tol
				dec, err := router.Route(req, StrategyEnsoHybridConstrained)
				if err != nil {
					continue
				}
				passSum += task.ArmOutcomes[dec.RoutedModel]
				usdSum += float64(dec.EstimatedCostMicros) / 1_000_000.0
			}
		}
		acc := passSum / float64(len(corpus))
		usd := usdSum / float64(len(corpus))
		pgr := (acc - weakAcc) / gap
		if pgr < 0 {
			pgr = 0
		}
		if pgr > 1 {
			pgr = 1
		}
		cr := usd / strongUSD
		if cr > 1 {
			cr = 1
		}
		pts = append(pts, pt{costRatio: cr, pgr: pgr})
	}

	sort.Slice(pts, func(i, j int) bool {
		if pts[i].costRatio != pts[j].costRatio {
			return pts[i].costRatio < pts[j].costRatio
		}
		return pts[i].pgr < pts[j].pgr
	})

	// Trapezoidal integration of PGR over normalized spend ratio in [0, 1].
	var area float64
	for i := 1; i < len(pts); i++ {
		dx := pts[i].costRatio - pts[i-1].costRatio
		if dx > 0 {
			area += 0.5 * (pts[i].pgr + pts[i-1].pgr) * dx
		}
	}

	cpt := func(targetPGR float64) float64 {
		best := 1.0
		for _, p := range pts {
			if p.pgr >= targetPGR && p.costRatio < best {
				best = p.costRatio
			}
		}
		return best
	}

	return area, cpt(0.80), cpt(0.90)
}

func runSparseBanditExperiment(catalog []ModelArm, pol ScopePolicy, seed uint64) SparseComparisonSummary {
	const n = 180
	const folds = 5
	corpus := GenerateStandardCorpus(n, catalog, seed)
	rng := NewRNG(seed ^ 0x9988776655443322)

	var eligArms []ModelArm
	for _, a := range catalog {
		if a.ZeroRetention && !a.FastModeOnly {
			eligArms = append(eligArms, a)
		}
	}

	var lrBrierSum, mfBrierSum, hybBrierSum float64
	var brierCnt int
	var lrPass, mfPass, hybPass, strongUSD, hybUSD float64

	for fold := 0; fold < folds; fold++ {
		var sparseTrain []TrainingSample
		for i, task := range corpus {
			if i%folds == fold {
				continue
			}
			feats := ExtractFeatures(task.Request)
			// Realistic partial-feedback bandit log (1 observed arm/query = 85.7% missing
			// rewards, logged by an 88% domain-bucketed historical heuristic + 12% uniform
			// exploration across eligible ZDR arms).
			var chosenArmID string
			if rng.Float64() < 0.88 {
				switch ClassifyCoarseBucket(feats) {
				case BucketCheapChat:
					chosenArmID = "zen-4-flash"
				case BucketCode, BucketToolSchema:
					chosenArmID = "zen-4-coder"
				case BucketMath, BucketReasoning:
					chosenArmID = "zen-4-max"
				default:
					chosenArmID = "zen-4-mini"
				}
			} else {
				chosenArmID = eligArms[rng.IntN(len(eligArms))].ID
			}
			sparseTrain = append(sparseTrain, TrainingSample{
				QueryID:  task.QueryID,
				Features: feats,
				ModelID:  chosenArmID,
				Passed:   task.ArmOutcomes[chosenArmID],
			})
		}

		tw := FitTrainedWeights(sparseTrain, catalog, seed^uint64(fold+7))

		rHyb := NewEnsoRouter(catalog, tw)
		rHyb.SetOrgPolicy("org-eval-enterprise", pol)

		for i, task := range corpus {
			if i%folds != fold {
				continue
			}
			feats := ExtractFeatures(task.Request)
			for _, arm := range eligArms {
				y := task.ArmOutcomes[arm.ID]
				pMF, _ := tw.PredictQualityByKind(feats, arm.ID, PredictorMFOnly)
				pHyb, _ := tw.PredictQualityByKind(feats, arm.ID, PredictorHybrid)
				pLR, _ := tw.PredictQualityByKind(feats, arm.ID, PredictorLROnly)

				mfBrierSum += (pMF - y) * (pMF - y)
				hybBrierSum += (pHyb - y) * (pHyb - y)
				lrBrierSum += (pLR - y) * (pLR - y)
				brierCnt++
			}

			req := task.Request
			req.QualityTolerance = 0.04

			dStrong, _ := rHyb.Route(req, StrategyAlwaysStrong)
			strongUSD += float64(dStrong.EstimatedCostMicros) / 1_000_000.0

			dMF, _ := rHyb.Route(req, StrategyRouteLLMMFConstrained)
			mfPass += task.ArmOutcomes[dMF.RoutedModel]

			dHyb, _ := rHyb.Route(req, StrategyEnsoHybridConstrained)
			hybPass += task.ArmOutcomes[dHyb.RoutedModel]
			hybUSD += float64(dHyb.EstimatedCostMicros) / 1_000_000.0

			dLR, _ := rHyb.Route(req, StrategyLogisticConstrained)
			lrPass += task.ArmOutcomes[dLR.RoutedModel]
		}
	}

	return SparseComparisonSummary{
		Queries:              n,
		ObservedArmsPerQuery: 1,
		LogisticOnlyBrier:    round4f(lrBrierSum / float64(brierCnt)),
		MatrixFactBrier:      round4f(mfBrierSum / float64(brierCnt)),
		EnsoHybridBrier:      round4f(hybBrierSum / float64(brierCnt)),
		LogisticOnlyAcc:      round4f(lrPass / float64(n)),
		MatrixFactAcc:        round4f(mfPass / float64(n)),
		EnsoHybridAcc:        round4f(hybPass / float64(n)),
		EnsoHybridSavingsPct: round2f((1.0 - hybUSD/strongUSD) * 100.0),
	}
}

func runCapabilityGateAudit(catalog []ModelArm, baseWeights *TrainedWeights, seed uint64) GateAuditSummary {
	const perScenario = 11
	baseTasks := GenerateStandardCorpus(perScenario, catalog, seed)

	catalogByID := make(map[string]ModelArm, len(catalog))
	for _, a := range catalog {
		catalogByID[a.ID] = a
	}

	router := NewEnsoRouter(catalog, baseWeights)
	router.SetOrgPolicy("org-free", ScopePolicy{PlanTier: PlanFree, HasPlanTier: true})
	router.SetOrgPolicy("org-starter", ScopePolicy{PlanTier: PlanStarter, HasPlanTier: true})
	router.SetOrgPolicy("org-pro-zdr", ScopePolicy{PlanTier: PlanPro, HasPlanTier: true, RequireZeroRetention: true})
	router.SetOrgPolicy("org-blocked-ext", ScopePolicy{
		PlanTier:         PlanEnterprise,
		HasPlanTier:      true,
		BlockedProviders: []string{"ext-batchcloud", "openai-standard"},
	})
	router.SetOrgPolicy("org-ent", ScopePolicy{PlanTier: PlanEnterprise, HasPlanTier: true})

	type scenDef struct {
		name             string
		expectReject     bool
		prepare          func(base RouteRequest, idx int) RouteRequest
		violatesIfChosen func(arm ModelArm, req RouteRequest, pol EffectivePolicy) bool
	}

	scenarios := []scenDef{
		{
			name: "zero_retention_required",
			prepare: func(b RouteRequest, _ int) RouteRequest {
				b.Org = "org-pro-zdr"
				b.Prompt = "Hello! Shorten greeting note." // tempts unconstrained router toward cheap 30d model
				b.EstInputTokens = 120
				b.EstOutputTokens = 60
				b.RequireZeroRetention = true
				b.QualityFloor = 0.70
				return b
			},
			violatesIfChosen: func(arm ModelArm, _ RouteRequest, _ EffectivePolicy) bool {
				return !arm.ZeroRetention || arm.RetentionDays > 0
			},
		},
		{
			name: "thinking_family_anthropic_continuity",
			prepare: func(b RouteRequest, _ int) RouteRequest {
				b.Org = "org-ent"
				b.PriorThinkingFamily = "anthropic-extended"
				b.RequireThinkingContinuity = true
				b.QualityTolerance = 0.15
				return b
			},
			violatesIfChosen: func(arm ModelArm, _ RouteRequest, _ EffectivePolicy) bool {
				return arm.ThinkingFamily != "anthropic-extended"
			},
		},
		{
			name: "pinned_turn_model_reasoning_lock",
			prepare: func(b RouteRequest, _ int) RouteRequest {
				b.Org = "org-ent"
				b.PinnedTurnModel = "zen-4-pro"
				b.QualityTolerance = 0.25
				return b
			},
			violatesIfChosen: func(arm ModelArm, _ RouteRequest, _ EffectivePolicy) bool {
				return arm.ID != "zen-4-pro"
			},
		},
		{
			name: "cross_family_thinking_block_sanitization",
			prepare: func(b RouteRequest, _ int) RouteRequest {
				b.Org = "org-pro-zdr"
				b.RequireThinkingContinuity = false // Explicit cross-family switch allowed, must strip foreign blocks!
				b.Messages = []ConversationMessage{{
					Role:    "assistant",
					Content: "Prior step conclusion.",
					ThinkingBlocks: []ThinkingBlock{{
						ThinkingFamily: "openai-reasoning",
						SourceModel:    "frontier-o-reasoner-30d",
						Signature:      "sig-foreign",
						Summary:        "account-bound reasoning trace",
					}},
				}}
				return b
			},
			violatesIfChosen: func(arm ModelArm, _ RouteRequest, _ EffectivePolicy) bool {
				return arm.ThinkingFamily == "openai-reasoning" // ZDR org cannot pick 30d openai-reasoning
			},
		},
		{
			name: "forced_tool_choice_required",
			prepare: func(b RouteRequest, _ int) RouteRequest {
				b.Org = "org-pro-zdr"
				b.RequireZeroRetention = true
				b.Prompt = "Prove theorem by mathematical induction and eigenvalue inequality."
				b.ToolChoice = ToolChoiceRequired
				b.ToolCount = 3
				b.QualityFloor = 0.78 // tempts unconstrained router toward zen-4-thinking or ext-oss-70b-30d!
				return b
			},
			violatesIfChosen: func(arm ModelArm, _ RouteRequest, _ EffectivePolicy) bool {
				return !arm.SupportsForcedToolChoice || !arm.ZeroRetention
			},
		},
		{
			name: "forced_tool_choice_named_function",
			prepare: func(b RouteRequest, _ int) RouteRequest {
				b.Org = "org-ent"
				b.RequireZeroRetention = false
				b.Prompt = "Hello! Shorten greeting note and extract email."
				b.ToolChoice = ToolChoiceNamedFunction
				b.ToolCount = 1
				b.QualityFloor = 0.80 // tempts unconstrained router toward cheap ext-oss-70b-30d (no forced tool support!)
				return b
			},
			violatesIfChosen: func(arm ModelArm, _ RouteRequest, _ EffectivePolicy) bool {
				return !arm.SupportsForcedToolChoice
			},
		},
		{
			name: "plan_entitlement_free_tier",
			prepare: func(b RouteRequest, _ int) RouteRequest {
				b.Org = "org-free"
				b.RequireZeroRetention = true
				b.QualityFloor = 0.90 // tempts unconstrained router toward pro/enterprise arms!
				return b
			},
			violatesIfChosen: func(arm ModelArm, _ RouteRequest, _ EffectivePolicy) bool {
				return arm.MinPlanTier > PlanFree
			},
		},
		{
			name: "plan_entitlement_starter_tier",
			prepare: func(b RouteRequest, _ int) RouteRequest {
				b.Org = "org-starter"
				b.RequireZeroRetention = true
				b.QualityFloor = 0.92
				return b
			},
			violatesIfChosen: func(arm ModelArm, _ RouteRequest, _ EffectivePolicy) bool {
				return arm.MinPlanTier > PlanStarter
			},
		},
		{
			name: "org_blocked_external_providers",
			prepare: func(b RouteRequest, _ int) RouteRequest {
				b.Org = "org-blocked-ext"
				b.RequireZeroRetention = false
				b.QualityFloor = 0.70
				return b
			},
			violatesIfChosen: func(arm ModelArm, _ RouteRequest, _ EffectivePolicy) bool {
				return arm.Provider == "ext-batchcloud" || arm.Provider == "openai-standard"
			},
		},
		{
			name:         "fast_mode_fail_closed_zero_prepaid",
			expectReject: true,
			prepare: func(b RouteRequest, _ int) RouteRequest {
				b.Org = "org-ent"
				b.FastMode = true
				b.HasPrepaidBalance = true
				b.PrepaidBalanceMicros = 0
				return b
			},
			violatesIfChosen: func(_ ModelArm, _ RouteRequest, _ EffectivePolicy) bool {
				return true // Any served call at $0 prepaid is a gate violation!
			},
		},
		{
			name:         "fast_mode_fail_closed_low_prepaid",
			expectReject: true,
			prepare: func(b RouteRequest, _ int) RouteRequest {
				b.Org = "org-ent"
				b.FastMode = true
				b.EstInputTokens = 4000
				b.EstOutputTokens = 2000 // cheapest fast arm zen-4-flash costs 4000*0.15 + 2000*0.60 = 1800 micros
				b.HasPrepaidBalance = true
				b.PrepaidBalanceMicros = 50 // < 1800 micros!
				return b
			},
			violatesIfChosen: func(_ ModelArm, _ RouteRequest, _ EffectivePolicy) bool {
				return true
			},
		},
		{
			name: "fast_mode_funded_prepaid",
			prepare: func(b RouteRequest, _ int) RouteRequest {
				b.Org = "org-ent"
				b.FastMode = true
				b.HasPrepaidBalance = true
				b.PrepaidBalanceMicros = 1_000_000 // $1.00 funded
				b.QualityFloor = 0.85
				return b
			},
			violatesIfChosen: func(arm ModelArm, req RouteRequest, _ EffectivePolicy) bool {
				cost := EstimateCostMicros(arm, req.EstInputTokens, req.EstOutputTokens)
				return !arm.FastModeCapable || cost > req.PrepaidBalanceMicros
			},
		},
	}

	var results []GateScenarioResult
	totalUnconViol := 0
	totalConViol := 0
	totalReqs := 0

	for _, sc := range scenarios {
		unconViol := 0
		conViol := 0
		rejectOK := true

		for idx, bt := range baseTasks {
			totalReqs++
			req := sc.prepare(bt.Request, idx)
			pol := router.EffectivePolicyFor(req.Org, req.Project)

			// 1. Unconstrained RouteLLM baseline
			uDec, uErr := router.Route(req, StrategyUnconstrainedRouteLLM)
			if uErr == nil {
				uArm := catalogByID[uDec.RoutedModel]
				if sc.violatesIfChosen(uArm, req, pol) {
					unconViol++
				} else if sc.name == "cross_family_thinking_block_sanitization" && uDec.StrippedThinkingBlocks != 1 && uArm.ThinkingFamily != "openai-reasoning" {
					unconViol++
				}
			}

			// 2. Proposed Capability-Constrained Enso Router
			cDec, cErr := router.Route(req, StrategyEnsoHybridConstrained)
			if sc.expectReject {
				if !IsRouteErrorCode(cErr, ErrCodeBudgetExhausted) {
					conViol++
					rejectOK = false
				}
			} else {
				if cErr != nil {
					conViol++
					rejectOK = false
				} else {
					if sc.violatesIfChosen(catalogByID[cDec.RoutedModel], req, pol) {
						conViol++
					}
					if sc.name == "cross_family_thinking_block_sanitization" && cDec.StrippedThinkingBlocks != 1 {
						conViol++
					}
				}
			}
		}

		totalUnconViol += unconViol
		totalConViol += conViol
		results = append(results, GateScenarioResult{
			Scenario:                 sc.name,
			Requests:                 perScenario,
			UnconstrainedViolations:  unconViol,
			ConstrainedViolations:    conViol,
			ExpectedRejectionHandled: rejectOK,
		})
	}

	return GateAuditSummary{
		TotalScenarios:               len(scenarios),
		TotalRequests:                totalReqs,
		UnconstrainedTotalViolations: totalUnconViol,
		ConstrainedTotalViolations:   totalConViol,
		Scenarios:                    results,
	}
}

func evaluateAdversarialSlice(router *EnsoRouter, tasks []LabeledTask) AdversarialSliceSummary {
	var strongSum, weakSum, looseSum, tightSum, floorSum float64
	n := float64(len(tasks))

	for _, t := range tasks {
		dStrong, _ := router.Route(t.Request, StrategyAlwaysStrong)
		strongSum += t.ArmOutcomes[dStrong.RoutedModel]

		dWeak, _ := router.Route(t.Request, StrategyAlwaysWeak)
		weakSum += t.ArmOutcomes[dWeak.RoutedModel]

		rLoose := t.Request
		rLoose.QualityTolerance = 0.08
		dLoose, _ := router.Route(rLoose, StrategyEnsoHybridConstrained)
		looseSum += t.ArmOutcomes[dLoose.RoutedModel]

		rTight := t.Request
		rTight.QualityTolerance = 0.02
		dTight, _ := router.Route(rTight, StrategyEnsoHybridConstrained)
		tightSum += t.ArmOutcomes[dTight.RoutedModel]

		rFloor := t.Request
		rFloor.QualityFloor = 0.85
		dFloor, _ := router.Route(rFloor, StrategyEnsoHybridConstrained)
		floorSum += t.ArmOutcomes[dFloor.RoutedModel]
	}

	return AdversarialSliceSummary{
		Queries:              len(tasks),
		AlwaysStrongAccuracy: round4f(strongSum / n),
		AlwaysWeakAccuracy:   round4f(weakSum / n),
		LooseTol008Accuracy:  round4f(looseSum / n),
		TightTol002Accuracy:  round4f(tightSum / n),
		HighFloor085Accuracy: round4f(floorSum / n),
	}
}

// DefaultEvalSeed is the fixed reproducible seed used for testdata/results.json.
const DefaultEvalSeed uint64 = 20261134

// MarshalBenchmarkJSON serializes report as canonical indented JSON.
func MarshalBenchmarkJSON(report BenchmarkReport) ([]byte, error) {
	b, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}
