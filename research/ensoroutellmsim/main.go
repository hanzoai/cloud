// Copyright 2026 Hanzo AI, Inc.
// SPDX-License-Identifier: Apache-2.0 OR MIT

// Command ensoroutellmsim runs the hanzoai/cloud#59 Acceptance Contract
// benchmark suite (canonical hanzoai/ai conf/models.yaml, shipping
// hanzoai/ai/router baseline, real public or private JSONL corpora,
// calibration/Pareto/CI metrics, live CPU routing latency quantiles, and
// fault-injection reliability checks).
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"text/tabwriter"
	"time"

	er "github.com/hanzoai/cloud/research/ensoroutellm"
)

func main() {
	defaultPricingPath := filepath.Join("research", "ensoroutellm", "testdata", "hanzo_pricing_snapshot.json")
	defaultCorpusPath := filepath.Join("research", "ensoroutellm", "testdata", "livebench_public_features.jsonl")
	mode := flag.String("mode", "acceptance", "execution mode: acceptance | extract-features | synthetic")
	modelsYAML := flag.String("models-yaml", filepath.Join("research", "ensoroutellm", "testdata", "hanzo_models_snapshot.yaml"), "path to canonical hanzoai/ai conf/models.yaml")
	pricingJSON := flag.String("pricing-json", defaultPricingPath, "optional path to pricing snapshot JSON (omitted automatically when -corpus is overridden)")
	corpusPath := flag.String("corpus", defaultCorpusPath, "path to raw-prompt or content-free JSONL evaluation corpus")
	inPath := flag.String("in", "", "input raw JSONL path for -mode extract-features")
	out := flag.String("out", "", "optional path to write JSON report or extracted features JSONL")
	seed := flag.Uint64("seed", er.DefaultEvalSeed, "deterministic SplitMix64 seed")
	folds := flag.Int("folds", 5, "stratified cross-validation folds")
	perfIters := flag.Int("perf-iters", 2000, "live CPU routing latency iterations")
	flag.Parse()

	explicitFlags := make(map[string]bool)
	flag.Visit(func(f *flag.Flag) {
		explicitFlags[f.Name] = true
	})
	effectivePricingJSON := *pricingJSON
	if !explicitFlags["pricing-json"] && explicitFlags["corpus"] && filepath.Clean(*corpusPath) != filepath.Clean(defaultCorpusPath) {
		effectivePricingJSON = ""
	}

	switch *mode {
	case "extract-features":
		if *inPath == "" || *out == "" {
			fmt.Fprintln(os.Stderr, "-mode extract-features requires both -in and -out")
			os.Exit(2)
		}
		sha, count, err := er.ConvertRawCorpusToContentFreeJSONL(*inPath, *out)
		if err != nil {
			fmt.Fprintln(os.Stderr, "extract-features:", err)
			os.Exit(1)
		}
		fmt.Printf("Extracted %d content-free records -> %s (sha256=%s)\n", count, *out, sha)

	case "synthetic":
		runSyntheticCLI(*seed, *out)

	case "acceptance":
		start := time.Now()
		report, err := er.RunAcceptanceBenchmark(er.AcceptanceConfig{
			ModelsYAMLPath:      *modelsYAML,
			PricingSnapshotPath: effectivePricingJSON,
			CorpusPath:          *corpusPath,
			Seed:                *seed,
			CVFolds:             *folds,
			PerfIterations:      *perfIters,
			BootstrapResamples:  500,
		})
		if err != nil {
			fmt.Fprintln(os.Stderr, "acceptance benchmark:", err)
			os.Exit(1)
		}
		elapsed := time.Since(start)

		if *out != "" {
			raw, err := er.MarshalAcceptanceJSON(report)
			if err != nil {
				fmt.Fprintln(os.Stderr, "marshal acceptance json:", err)
				os.Exit(1)
			}
			if dir := filepath.Dir(*out); dir != "" && dir != "." {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					fmt.Fprintln(os.Stderr, "mkdir output dir:", err)
					os.Exit(1)
				}
			}
			if err := os.WriteFile(*out, raw, 0o644); err != nil {
				fmt.Fprintln(os.Stderr, "write acceptance json:", err)
				os.Exit(1)
			}
		}

		w := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
		fmt.Fprintf(w, "=== Acceptance Benchmark (%s, n=%d, %d-fold CV, arms=%d, aggregate_only=%v) ===\n",
			report.DatasetName, report.CorpusSize, report.CVFolds, report.Catalog.ActiveArms, report.AggregateOnly)
		fmt.Fprintf(w, "Calibration: ECE=%.4f  Brier=%.4f  APGR=%.4f  CPT80=%.2f%%  CPT90=%.2f%%\n",
			report.Calibration.ECE10Bin, report.Calibration.Brier, report.Calibration.APGR,
			report.Calibration.CPT80Pct, report.Calibration.CPT90Pct)
		fmt.Fprintln(w, "POLICY\tACC\tCI95\tRET\t$/1K_QRY\tSAVE\tP50_MS\tP95_MS\tP99_MS\tARMS")
		for _, pm := range report.Policies {
			fmt.Fprintf(w, "%s\t%.2f%%\t[%.2f,%.2f]\t%.1f%%\t$%.4f\t%.1f%%\t%d\t%d\t%d\t%d\n",
				pm.PolicyName,
				pm.Accuracy*100,
				pm.AccuracyCI95[0]*100, pm.AccuracyCI95[1]*100,
				pm.QualityRetentionPct,
				pm.USDPer1kQueries,
				pm.USDCostSavingPct,
				pm.P50ModelLatencyMs, pm.P95ModelLatencyMs, pm.P99ModelLatencyMs,
				pm.UniqueModelsSelected,
			)
		}
		fmt.Fprintln(w)
		mc := report.MatchedBudgetCanonical
		ma := report.MatchedBudgetAdapted
		fmt.Fprintf(w, "Matched Budget vs %s: %s -> %+.2f pp (95%% CI [%+.2f, %+.2f] pp), cost %+.2f%% (matched=%v)\n",
			mc.BaselinePolicy, mc.CandidatePolicy, mc.AccuracyDeltaPP,
			mc.AccuracyDeltaCI95PP[0], mc.AccuracyDeltaCI95PP[1], mc.CostDeltaPct, mc.BudgetMatched)
		fmt.Fprintf(w, "Matched Budget vs %s: %s -> %+.2f pp (95%% CI [%+.2f, %+.2f] pp), cost %+.2f%% (matched=%v)\n\n",
			ma.BaselinePolicy, ma.CandidatePolicy, ma.AccuracyDeltaPP,
			ma.AccuracyDeltaCI95PP[0], ma.AccuracyDeltaCI95PP[1], ma.CostDeltaPct, ma.BudgetMatched)

		ps := report.Performance
		fmt.Fprintf(w, "Routing CPU Overhead (median prompt=%d bytes, %d iters):\n", ps.PromptLengthMedianBytes, ps.CandidateHybrid.Iterations)
		fmt.Fprintf(w, "  shipping_heuristic:   mean=%.0fns p50=%dns p95=%dns p99=%dns | %.0f qps (1g) / %.0f qps (8g)\n",
			ps.ShippingHeuristic.MeanNs, ps.ShippingHeuristic.P50Ns, ps.ShippingHeuristic.P95Ns, ps.ShippingHeuristic.P99Ns,
			ps.ShippingHeuristic.ThroughputQPS, ps.ShippingHeuristic.Concurrent8QPS)
		fmt.Fprintf(w, "  candidate_hybrid:     mean=%.0fns p50=%dns p95=%dns p99=%dns | %.0f qps (1g) / %.0f qps (8g)\n",
			ps.CandidateHybrid.MeanNs, ps.CandidateHybrid.P50Ns, ps.CandidateHybrid.P95Ns, ps.CandidateHybrid.P99Ns,
			ps.CandidateHybrid.ThroughputQPS, ps.CandidateHybrid.Concurrent8QPS)
		fmt.Fprintf(w, "  candidate_with_ledger: mean=%.0fns p50=%dns p95=%dns p99=%dns | %.0f qps (1g) / %.0f qps (8g)\n\n",
			ps.CandidateWithLedger.MeanNs, ps.CandidateWithLedger.P50Ns, ps.CandidateWithLedger.P95Ns, ps.CandidateWithLedger.P99Ns,
			ps.CandidateWithLedger.ThroughputQPS, ps.CandidateWithLedger.Concurrent8QPS)

		fmt.Fprintf(w, "Reliability Audit: %d/%d checks passed (all_passed=%v) | Suite completed in %s\n",
			report.Reliability.PassedChecks, report.Reliability.TotalChecks, report.Reliability.AllPassed, elapsed.Round(time.Millisecond))
		_ = w.Flush()

	default:
		fmt.Fprintf(os.Stderr, "unknown -mode %q (want acceptance, extract-features, or synthetic)\n", *mode)
		os.Exit(2)
	}
}

func runSyntheticCLI(seed uint64, out string) {
	start := time.Now()
	report := er.RunBenchmarkSuite(seed)
	elapsed := time.Since(start)

	if out != "" {
		raw, err := er.MarshalBenchmarkJSON(report)
		if err != nil {
			fmt.Fprintln(os.Stderr, "marshal benchmark json:", err)
			os.Exit(1)
		}
		if err := os.WriteFile(out, raw, 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "write benchmark json:", err)
			os.Exit(1)
		}
	}

	w := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
	fmt.Fprintf(w, "=== Synthetic Unit-Fixture Suite (%d tasks, ECE=%.4f, Brier=%.4f, APGR=%.4f) ===\n",
		report.DenseCorpusSize, report.Calibration.ECE10Bin, report.Calibration.Brier, report.Calibration.APGR)
	fmt.Fprintln(w, "POLICY\tACC\tRETENTION\t$/MTOK\t$/1K_QRY\tUSD_SAVE\tP95_LAT")
	for _, pm := range report.DensePolicies {
		fmt.Fprintf(w, "%s\t%.2f%%\t%.2f%%\t$%.2f\t$%.4f\t%.2f%%\t%dms\n",
			pm.PolicyName, pm.Accuracy*100, pm.QualityRetentionPct,
			pm.BlendedUSDPerMTok, pm.USDPer1kQueries, pm.USDCostSavingPct, pm.P95LatencyMs)
	}
	fmt.Fprintf(w, "\nSynthetic suite completed in %s\n", elapsed.Round(time.Millisecond))
	_ = w.Flush()
}
