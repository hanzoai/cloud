// SPDX-License-Identifier: Apache-2.0 OR MIT

package main

import "testing"

func TestRunBenchmarkAllDimensionsAndNegativeRegimes(t *testing.T) {
	rep, err := RunBenchmark(25)
	if err != nil {
		t.Fatalf("RunBenchmark: %v", err)
	}
	if !rep.E2EAcceptance.AllAcceptanceStepsPassed {
		t.Fatalf("E2E acceptance failed: %+v", rep.E2EAcceptance)
	}
	if rep.Trust.ScenariosPassed != rep.Trust.ScenariosTotal || rep.Trust.DoublePayoutCount != 0 || rep.Trust.SubjectiveSlashes != 0 || rep.Trust.ConservationDrift != 0 {
		t.Fatalf("trust invariants failed: %+v", rep.Trust)
	}
	if rep.Decentralization.HanzoDependencies != 0 || rep.Decentralization.IndependenceRatePct != 100.0 {
		t.Fatalf("decentralization invariant failed: %+v", rep.Decentralization)
	}
	if !rep.Sovereignty.LedgerOfflineValid || !rep.Sovereignty.LedgerTamperCaught || !rep.Sovereignty.SnapshotImportSucceeded || !rep.Sovereignty.SnapshotTamperCaught || rep.Sovereignty.MigrationFidelityPct != 100.0 || rep.Sovereignty.PostMigrationDrift != 0 {
		t.Fatalf("sovereignty invariant failed: %+v", rep.Sovereignty)
	}
	if !rep.NegativeRegimes.ZeroBondGriefing.DefaultPolicyBlocksZeroBond || rep.NegativeRegimes.ZeroBondGriefing.ZeroBondGriefingSuccessPct != 100.0 {
		t.Fatalf("zero_bond_griefing_negative failed: %+v", rep.NegativeRegimes.ZeroBondGriefing)
	}
	if rep.NegativeRegimes.UnverifiedInstantRelease.InstantReleaseTheftPct != 100.0 || rep.NegativeRegimes.UnverifiedInstantRelease.OptimisticFraudCaughtPct != 100.0 {
		t.Fatalf("unverified_instant_release_negative failed: %+v", rep.NegativeRegimes.UnverifiedInstantRelease)
	}
	if rep.NegativeRegimes.NonDeterministicSampling.EngineFalseSlashRatePct != 0.0 || !rep.NegativeRegimes.NonDeterministicSampling.EquivocationStillSlashable {
		t.Fatalf("non_deterministic_llm_sampling_boundary failed: %+v", rep.NegativeRegimes.NonDeterministicSampling)
	}
}
