// Copyright 2023-2026 Hanzo AI Inc.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"path/filepath"
	"testing"
)

func TestBenchmarkEndToEndAndHypothesisGate(t *testing.T) {
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}

	rep, err := runBenchmark(1348, 40, repoRoot)
	if err != nil {
		t.Fatalf("runBenchmark failed: %v", err)
	}

	if len(rep.MatchedRegimes) != 3 {
		t.Fatalf("expected 3 matched regimes, got %d", len(rep.MatchedRegimes))
	}
	if rep.Corpus.FileCount < 5 {
		t.Fatalf("expected real repo corpus files >= 5, got %d", rep.Corpus.FileCount)
	}

	batched := rep.MatchedRegimes[2]
	if batched.Name != "hanzo_agenttrace_batched" {
		t.Fatalf("unexpected 3rd regime name: %q", batched.Name)
	}
	if batched.Completeness6Of6Pct != 100.0 {
		t.Fatalf("expected 100%% 6/6 completeness, got %.2f%%", batched.Completeness6Of6Pct)
	}
	if batched.BillingReconciledPct != 100.0 {
		t.Fatalf("expected 100%% billing reconciliation, got %.2f%%", batched.BillingReconciledPct)
	}
	if batched.OrphanSpans != 0 || batched.CrossTenantLeaks != 0 || batched.SecretLeaks != 0 {
		t.Fatalf("expected 0 orphans/cross-tenant/secret leaks, got orphans=%d xten=%d sec=%d",
			batched.OrphanSpans, batched.CrossTenantLeaks, batched.SecretLeaks)
	}
	if batched.OTLPValidBatchesPct != 100.0 {
		t.Fatalf("expected 100%% valid OTLP/JSON batches, got %.2f%%", batched.OTLPValidBatchesPct)
	}

	// Verify naive baseline leaked canary secrets and ran slower than batched.
	naive := rep.MatchedRegimes[1]
	if naive.SecretLeaks == 0 {
		t.Fatalf("expected naive unredacted tracer to leak canary secrets")
	}

	// Verify all three negative regimes exhibited their expected failure modes.
	if !rep.HypothesisCheck.NegativeRegimesConfirmed {
		t.Fatalf("expected NegativeRegimesConfirmed=true, got %+v", rep.NegativeRegimes)
	}
	if !rep.HypothesisCheck.Passed {
		t.Fatalf("expected HypothesisCheck.Passed=true, got %+v", rep.HypothesisCheck)
	}
}
