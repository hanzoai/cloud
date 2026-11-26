// SPDX-License-Identifier: Apache-2.0 OR MIT

package main

import "testing"

func TestRunBenchmarkFiveDimensionsAndNegativeRegimes(t *testing.T) {
	rep, err := runBenchmark(20, 4)
	if err != nil {
		t.Fatalf("runBenchmark failed: %v", err)
	}
	if rep.Performance.Completed != 20 || rep.Performance.CompletionRatePct != 100 {
		t.Fatalf("unexpected performance: %+v", rep.Performance)
	}
	if rep.Trust.ProbesCaught != rep.Trust.ProbesInjected || rep.Trust.FraudDetectionRatePct != 100 || rep.Trust.DoubleSpendsAllowed != 0 {
		t.Fatalf("unexpected trust metrics: %+v", rep.Trust)
	}
	if rep.Decentralization.HanzoIndependentSharePct != 100 || rep.Decentralization.IndependentFacilitators != 2 || rep.Decentralization.HanzoOperatedSettled != 0 {
		t.Fatalf("unexpected decentralization metrics: %+v", rep.Decentralization)
	}
	if !rep.Sovereignty.HashChainIntact || !rep.Sovereignty.SignaturesIntact || !rep.Sovereignty.ExportTamperDetected || !rep.Sovereignty.LiveMigrationZeroDowntime || !rep.Sovereignty.PostMigrationReplayBlocked {
		t.Fatalf("unexpected sovereignty metrics: %+v", rep.Sovereignty)
	}
	if rep.NegativeRegimes.MicroPromptL1GasDominance.L1FeeOverheadPct < 99.0 {
		t.Fatalf("expected L1 gas dominance >99%%, got %+v", rep.NegativeRegimes.MicroPromptL1GasDominance)
	}
	if rep.NegativeRegimes.OptimisticUnverifiedCtrl.ReplayDrainRatePct != 100 {
		t.Fatalf("expected 100%% replay drain on unverified control, got %+v", rep.NegativeRegimes.OptimisticUnverifiedCtrl)
	}
	if rep.NegativeRegimes.FacilitatorOutageFailover.CompletionRatePct != 100 || rep.NegativeRegimes.FacilitatorOutageFailover.AutoFailoversTriggered < 1 {
		t.Fatalf("unexpected failover regime: %+v", rep.NegativeRegimes.FacilitatorOutageFailover)
	}
}
