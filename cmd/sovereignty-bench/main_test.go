// SPDX-License-Identifier: Apache-2.0 OR MIT

package main

import "testing"

func TestBenchmarkSmoke(t *testing.T) {
	rep, err := runBenchmark(10, 2, 5)
	if err != nil {
		t.Fatalf("runBenchmark failed: %v", err)
	}
	if !rep.ZIPE2EPassed {
		t.Fatalf("expected ZIPE2EPassed=true")
	}
	if rep.Trust.Passed != rep.Trust.Total || rep.Trust.DetectionRate != 1.0 {
		t.Fatalf("expected 100%% trust detection, got %d/%d", rep.Trust.Passed, rep.Trust.Total)
	}
	if rep.Decentralization.IndependentShare != 1.0 || rep.Decentralization.HanzoCallsAfterKill != 0 {
		t.Fatalf("expected 100%% decentralization with 0 post-kill Hanzo calls, got share=%.4f calls=%d",
			rep.Decentralization.IndependentShare, rep.Decentralization.HanzoCallsAfterKill)
	}
	if !rep.Sovereignty.ForkBundleVerified || rep.Sovereignty.ConservationDrift != 0 {
		t.Fatalf("expected verified fork bundle and 0 drift, got %+v", rep.Sovereignty)
	}
	if len(rep.NegativeRegimes) != 3 {
		t.Fatalf("expected 3 negative/boundary regimes, got %d", len(rep.NegativeRegimes))
	}
	for _, r := range rep.NegativeRegimes {
		if !r.Validated {
			t.Errorf("negative regime %s failed validation: %+v", r.Name, r)
		}
	}
	if rep.Performance.MeasuredTokensPerJob != 128 || rep.Cost.MeasuredTokensPerJob != 128 {
		t.Fatalf("expected MeasuredTokensPerJob=128, got perf=%d cost=%d", rep.Performance.MeasuredTokensPerJob, rep.Cost.MeasuredTokensPerJob)
	}
}

func TestFlipFirstHexByteBitAlwaysMutates(t *testing.T) {
	for _, sample := range []string{"ff010203", "00010203", "aabbccdd"} {
		out, err := flipFirstHexByteBit(sample)
		if err != nil {
			t.Fatalf("flipFirstHexByteBit(%s): %v", sample, err)
		}
		if out == sample {
			t.Fatalf("flipFirstHexByteBit(%s) returned unchanged ciphertext", sample)
		}
	}
}

func TestComputeCostModelUsesMeasuredTokenCount(t *testing.T) {
	perf := PerformanceSection{
		OfflineReceiptVerify:  LatencyStats{MeanUs: 1000.0},
		SovereignDataRecovery: LatencyStats{MeanUs: 1000.0},
		PeerAndModelDiscovery: LatencyStats{MeanUs: 1033.53},
		MeasuredTokensPerJob:  128,
	}
	cost := computeCostModel(perf)
	// 3033.53 us/job * (1_000_000 / 128 jobs) / 1e6 = 23.699453 sec per 1M measured tokens.
	if cost.MeasuredLocalCPUsPer1MTokens < 23.0 || cost.MeasuredLocalCPUsPer1MTokens > 24.5 {
		t.Fatalf("expected MeasuredLocalCPUsPer1MTokens ~23.699453 for 128-token jobs, got %f", cost.MeasuredLocalCPUsPer1MTokens)
	}
	if cost.TokensPerJob != 10_000 || cost.MeasuredTokensPerJob != 128 {
		t.Fatalf("unexpected token basis: modeled=%d measured=%d", cost.TokensPerJob, cost.MeasuredTokensPerJob)
	}
}

