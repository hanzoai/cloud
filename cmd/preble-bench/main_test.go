// SPDX-License-Identifier: Apache-2.0

package main

import (
	"testing"

	"github.com/hanzoai/cloud/apps/preble"
)

func TestRunBenchmarksReproducibleAndIsolated(t *testing.T) {
	receipt, err := runBenchmarks("../..", 1346, preble.DefaultBlockSize)
	if err != nil {
		t.Fatalf("runBenchmarks: %v", err)
	}
	if receipt.Corpus.FilesRead < 5 || receipt.Corpus.TotalTokens < 5000 {
		t.Fatalf("expected substantial real repo corpus, got files=%d tokens=%d",
			receipt.Corpus.FilesRead, receipt.Corpus.TotalTokens)
	}
	if len(receipt.Traces) != 4 {
		t.Fatalf("expected 4 traces, got %d", len(receipt.Traces))
	}
	if !receipt.Isolation.AllIsolated || receipt.Isolation.CrossTenantLeaks != 0 || receipt.Isolation.CrossACLLeaks != 0 {
		t.Fatalf("isolation probe failed: %+v", receipt.Isolation)
	}

	byName := make(map[string]TraceBenchmarkResult, len(receipt.Traces))
	for _, tr := range receipt.Traces {
		if !tr.MemoryStrictlyBounded {
			t.Errorf("trace %s exceeded aggregate KV block bound", tr.Name)
		}
		if len(tr.Policies) != 4 {
			t.Errorf("trace %s expected 4 policies, got %d", tr.Name, len(tr.Policies))
		}
		byName[tr.Name] = tr
	}

	// Verify multi-turn coding beats least_loaded substantially on p99 TTFT.
	if mt := byName["multi_turn_agent_coding"]; mt.PrebleVsLeastLoadedP99 <= 1.2 {
		t.Errorf("expected multi_turn_agent_coding Preble speedup > 1.2x vs least_loaded, got %.2fx", mt.PrebleVsLeastLoadedP99)
	}

	// Verify skewed hot prefix beats BOTH least_loaded and prefix_greedy.
	if sk := byName["skewed_hot_prefix"]; sk.PrebleVsLeastLoadedP99 <= 1.1 || sk.PrebleVsGreedyP99 <= 1.5 {
		t.Errorf("expected skewed_hot_prefix Preble to beat least_loaded (%.2fx) and prefix_greedy (%.2fx)",
			sk.PrebleVsLeastLoadedP99, sk.PrebleVsGreedyP99)
	}

	// Verify cold_start_and_eviction triggers real evictions and beats least_loaded.
	if cs := byName["cold_start_and_eviction"]; cs.PrebleVsLeastLoadedP99 <= 1.05 {
		t.Errorf("expected cold_start_and_eviction Preble speedup > 1.05x vs least_loaded, got %.2fx", cs.PrebleVsLeastLoadedP99)
	}

	// Verify negative regime unique_zero_reuse_prompts has ~0% reuse and does not degrade p99 TTFT.
	if nr := byName["unique_zero_reuse_prompts"]; nr.PrebleVsLeastLoadedP99 < 0.92 {
		t.Errorf("negative regime degraded p99 TTFT vs least_loaded: %.2fx", nr.PrebleVsLeastLoadedP99)
	}
}
