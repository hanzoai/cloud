// SPDX-License-Identifier: Apache-2.0 OR MIT

package main

import "testing"

func TestRunBenchmarkAllDimensionsPass(t *testing.T) {
	rep, err := runBenchmark(60)
	if err != nil {
		t.Fatalf("runBenchmark: %v", err)
	}
	if !rep.AcceptanceFlow.Passed {
		t.Fatalf("acceptance flow did not pass: %+v", rep.AcceptanceFlow)
	}
	if rep.Dimensions.Trust.DetectionRatePct != 100.0 {
		t.Fatalf("trust detection rate = %.2f%%, want 100%%", rep.Dimensions.Trust.DetectionRatePct)
	}
	if rep.Dimensions.Decentralization.DecentralizationPct != 100.0 || rep.Dimensions.Decentralization.HanzoOperatedDependencies != 0 {
		t.Fatalf("unexpected decentralization metrics: %+v", rep.Dimensions.Decentralization)
	}
	if !rep.Dimensions.Sovereignty.OfflineVerifyPassed || rep.Dimensions.Sovereignty.MigrationFidelityPct != 100.0 || !rep.Dimensions.Sovereignty.TamperedExportDetected {
		t.Fatalf("unexpected sovereignty metrics: %+v", rep.Dimensions.Sovereignty)
	}
	if rep.NegativeRegimes.NFTOnlyNegative.NFTOnlyBypassRatePct != 100.0 || rep.NegativeRegimes.NFTOnlyNegative.RealEnforcerDetectionPct != 100.0 {
		t.Fatalf("unexpected NFT-only negative control: %+v", rep.NegativeRegimes.NFTOnlyNegative)
	}
	if len(rep.NegativeRegimes.StaleCacheRevocation.Samples) < 2 || rep.NegativeRegimes.StaleCacheRevocation.Samples[0].CompromisedCallsBypassed != 0 {
		t.Fatalf("unexpected stale-cache revocation samples: %+v", rep.NegativeRegimes.StaleCacheRevocation.Samples)
	}
	if rep.NegativeRegimes.SybilWashTrading.UnpaidForgedBlockedPct != 100.0 || rep.NegativeRegimes.SybilWashTrading.DirectSelfFeedbackBlockedPct != 100.0 {
		t.Fatalf("unexpected sybil wash trading regime: %+v", rep.NegativeRegimes.SybilWashTrading)
	}
}
