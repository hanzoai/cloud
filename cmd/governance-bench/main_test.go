// SPDX-License-Identifier: Apache-2.0 OR MIT

package main

import "testing"

func TestRunBenchmarkAcceptanceAndAdversarialMatrix(t *testing.T) {
	res, err := runBenchmark(15, 200)
	if err != nil {
		t.Fatalf("runBenchmark: %v", err)
	}
	if res.ScenarioSummary.ParameterBefore != "250" || res.ScenarioSummary.ParameterAfter != "175" {
		t.Fatalf("unexpected parameter transition: %s -> %s", res.ScenarioSummary.ParameterBefore, res.ScenarioSummary.ParameterAfter)
	}
	if res.ScenarioSummary.HanzoAdminOverridePresent {
		t.Fatalf("expected zero Hanzo admin override")
	}
	if res.AdversarialMatrix.TotalAttacks < 18 || res.AdversarialMatrix.RejectionRatePct != 100.0 {
		t.Fatalf("expected 100%% rejection on >= 18 attacks, got %d/%d (%.1f%%)",
			res.AdversarialMatrix.RejectedAttacks, res.AdversarialMatrix.TotalAttacks, res.AdversarialMatrix.RejectionRatePct)
	}
	if !res.ForkReplay.LosslessStateParity || !res.ForkReplay.PostForkProposalExecuted || !res.ForkReplay.CrossForkReplayRejected || !res.ForkReplay.Gen2BundleVerified {
		t.Fatalf("fork replay verification failed: %+v", res.ForkReplay)
	}
	if res.Performance.VotesPerSec <= 0 || res.Performance.ProposalsPerSec <= 0 {
		t.Fatalf("expected positive throughput metrics: %+v", res.Performance)
	}
}
