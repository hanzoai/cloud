// Copyright 2026 Hanzo AI, Inc.
// SPDX-License-Identifier: Apache-2.0 OR MIT

// Command ensoroutellmsim executes the 4-part Enso / RouteLLM benchmark suite
// from research/ensoroutellm (hanzoai/cloud#34 & #27, HIP-1345 §2.6), prints
// formatted before/after tables and live CPU routing latency, and optionally
// writes the deterministic raw JSON report to disk.
package main

import (
	"flag"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	er "github.com/hanzoai/cloud/research/ensoroutellm"
)

func main() {
	out := flag.String("out", "", "optional path to write raw JSON benchmark report")
	seed := flag.Uint64("seed", er.DefaultEvalSeed, "deterministic SplitMix64 benchmark seed")
	flag.Parse()

	start := time.Now()
	report := er.RunBenchmarkSuite(*seed)
	elapsed := time.Since(start)

	if *out != "" {
		raw, err := er.MarshalBenchmarkJSON(report)
		if err != nil {
			fmt.Fprintln(os.Stderr, "marshal benchmark json:", err)
			os.Exit(1)
		}
		if err := os.WriteFile(*out, raw, 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "write benchmark json:", err)
			os.Exit(1)
		}
	}

	// Measure pure CPU per-decision latency over 5,000 live Route() calls.
	cat := er.DefaultCatalog()
	corpus := er.GenerateStandardCorpus(100, cat, *seed)
	var samples []er.TrainingSample
	for _, t := range corpus {
		f := er.ExtractFeatures(t.Request)
		for _, a := range cat {
			if a.ZeroRetention && !a.FastModeOnly {
				samples = append(samples, er.TrainingSample{QueryID: t.QueryID, Features: f, ModelID: a.ID, Passed: t.ArmOutcomes[a.ID]})
			}
		}
	}
	tw := er.FitTrainedWeights(samples, cat, *seed)
	benchRouter := er.NewEnsoRouter(cat, tw)
	benchRouter.SetOrgPolicy("org-eval-enterprise", er.ScopePolicy{PlanTier: er.PlanEnterprise, HasPlanTier: true, RequireZeroRetention: true})
	const latIters = 5000
	latStart := time.Now()
	for i := 0; i < latIters; i++ {
		req := corpus[i%len(corpus)].Request
		req.RequestID = "" // hot routing path without ledger map allocation
		if _, err := benchRouter.Route(req, er.StrategyEnsoHybridConstrained); err != nil {
			fmt.Fprintf(os.Stderr, "benchmark route error: %v\n", err)
			os.Exit(1)
		}
	}
	usPerDecision := float64(time.Since(latStart).Nanoseconds()) / float64(latIters) / 1000.0

	w := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
	fmt.Fprintf(w, "=== 1. Dense 5-Fold Stratified CV (%d tasks, ECE=%.4f, Brier=%.4f, APGR=%.4f, CPT80=%.2f%%, CPT90=%.2f%%) ===\n",
		report.DenseCorpusSize,
		report.Calibration.ECE10Bin,
		report.Calibration.Brier,
		report.Calibration.APGR,
		report.Calibration.CPT80Pct,
		report.Calibration.CPT90Pct,
	)
	fmt.Fprintln(w, "POLICY\tACC\tRETENTION\t$/MTOK\t$/1K_QRY\tMTOK_SAVE\tUSD_SAVE\tMEAN_LAT\tP95_LAT")
	for _, pm := range report.DensePolicies {
		fmt.Fprintf(w, "%s\t%.2f%%\t%.2f%%\t$%.2f\t$%.4f\t%.2f%%\t%.2f%%\t%.1fms\t%dms\n",
			pm.PolicyName,
			pm.Accuracy*100,
			pm.QualityRetentionPct,
			pm.BlendedUSDPerMTok,
			pm.USDPer1kQueries,
			pm.BlendedCostSavingPct,
			pm.USDCostSavingPct,
			pm.MeanLatencyMs,
			pm.P95LatencyMs,
		)
	}
	fmt.Fprintln(w)
	sc := report.SparseComparison
	fmt.Fprintf(w, "=== 2. Sparse Bandit Partial-Feedback CV (%d queries, %d observed arm/query = 85.7%% missing) ===\n",
		sc.Queries, sc.ObservedArmsPerQuery)
	fmt.Fprintf(w, "Logistic-only: Brier=%.4f Acc=%.2f%%  |  MatrixFact(x^TWp): Brier=%.4f Acc=%.2f%%  |  EnsoHybrid: Brier=%.4f Acc=%.2f%% (USD Savings=%.2f%%)\n\n",
		sc.LogisticOnlyBrier, sc.LogisticOnlyAcc*100,
		sc.MatrixFactBrier, sc.MatrixFactAcc*100,
		sc.EnsoHybridBrier, sc.EnsoHybridAcc*100,
		sc.EnsoHybridSavingsPct,
	)

	fmt.Fprintf(w, "=== 3. HIP-1345 §2.6 Capability & Security Gate Audit (%d scenarios, %d requests) ===\n",
		report.GateAudit.TotalScenarios, report.GateAudit.TotalRequests)
	fmt.Fprintln(w, "SCENARIO\tREQS\tUNCONSTRAINED_VIOL\tCONSTRAINED_VIOL\tREJECT_OK")
	for _, gs := range report.GateAudit.Scenarios {
		fmt.Fprintf(w, "%s\t%d\t%d\t%d\t%v\n",
			gs.Scenario, gs.Requests, gs.UnconstrainedViolations, gs.ConstrainedViolations, gs.ExpectedRejectionHandled)
	}
	fmt.Fprintf(w, "TOTAL GATE VIOLATIONS:\t%d\t%d\t%d\t-\n\n",
		report.GateAudit.TotalRequests,
		report.GateAudit.UnconstrainedTotalViolations,
		report.GateAudit.ConstrainedTotalViolations,
	)

	adv := report.AdversarialSlice
	fmt.Fprintf(w, "=== 4. Expected Failure Mode (H4: %d OOD Terse Math Prompts Without Lexical Cues) ===\n", adv.Queries)
	fmt.Fprintf(w, "always_strong=%.2f%%  always_weak=%.2f%%  enso_loose(tol=0.08)=%.2f%%  enso_tight(tol=0.02)=%.2f%%  enso_floor(0.85)=%.2f%%\n",
		adv.AlwaysStrongAccuracy*100,
		adv.AlwaysWeakAccuracy*100,
		adv.LooseTol008Accuracy*100,
		adv.TightTol002Accuracy*100,
		adv.HighFloor085Accuracy*100,
	)
	fmt.Fprintf(w, "\nSuite completed in %s | Live CPU decision latency: %.2f µs/op (%d iters)\n",
		elapsed.Round(time.Millisecond), usPerDecision, latIters)
	_ = w.Flush()
}
