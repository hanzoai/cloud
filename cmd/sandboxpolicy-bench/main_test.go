// SPDX-License-Identifier: Apache-2.0

package main

import (
	"testing"
)

func TestRunBenchmarksRegressionAndNegativeRegimes(t *testing.T) {
	rep, err := RunBenchmarks("../..", 1349)
	if err != nil {
		t.Fatalf("RunBenchmarks: %v", err)
	}

	// Matrix 1: >= 16 boundary cases, 100% blocked under EnforceDeny & EnforceSigkill,
	// 0% blocked under AuditOnly with 100% audit visibility & hash-chain validity.
	if rep.BoundaryMatrix.TotalCases < 16 {
		t.Fatalf("boundary cases = %d, want >= 16", rep.BoundaryMatrix.TotalCases)
	}
	if rep.BoundaryMatrix.AuditOnly.BlockRatePct != 0 || rep.BoundaryMatrix.AuditOnly.AuditVisibilityPct != 100 {
		t.Fatalf("AuditOnly unexpected: %+v", rep.BoundaryMatrix.AuditOnly)
	}
	if !rep.BoundaryMatrix.AuditOnly.AuditChainValid {
		t.Fatal("AuditOnly audit hash-chain invalid")
	}
	if rep.BoundaryMatrix.EnforceDeny.BlockRatePct != 100 || !rep.BoundaryMatrix.EnforceDeny.AuditChainValid {
		t.Fatalf("EnforceDeny unexpected: %+v", rep.BoundaryMatrix.EnforceDeny)
	}
	if rep.BoundaryMatrix.EnforceSigkill.BlockRatePct != 100 || !rep.BoundaryMatrix.EnforceSigkill.AuditChainValid {
		t.Fatalf("EnforceSigkill unexpected: %+v", rep.BoundaryMatrix.EnforceSigkill)
	}
	if rep.BoundaryMatrix.EnforceSigkill.BPFParityPct != 100 {
		t.Fatalf("EnforceSigkill BPF VM parity = %.2f%%, want 100%%", rep.BoundaryMatrix.EnforceSigkill.BPFParityPct)
	}

	// Matrix 2: >= 200 benign operations with 0.00% false-positive blocks.
	if rep.BenignMatrix.TotalOperations < 200 {
		t.Fatalf("benign ops = %d, want >= 200", rep.BenignMatrix.TotalOperations)
	}
	if rep.BenignMatrix.FalsePositiveBlocks != 0 || rep.BenignMatrix.FalsePositiveRatePct != 0 {
		t.Fatalf("unexpected false positives on benign agent workload: %+v", rep.BenignMatrix)
	}
	if rep.BenignMatrix.SyscallBPFParityPct != 100 {
		t.Fatalf("benign syscall BPF parity = %.2f%%, want 100%%", rep.BenignMatrix.SyscallBPFParityPct)
	}

	// Matrix 3 & Negative Regime 1: 10, 100, 1000 active rules; unindexed degrades sharply at 1000 rules.
	if len(rep.NegativeRegime1.ScalingRows) != 3 {
		t.Fatalf("expected 3 scaling rows, got %d", len(rep.NegativeRegime1.ScalingRows))
	}
	row1000 := rep.NegativeRegime1.ScalingRows[2]
	if row1000.ActiveRules != 1000 {
		t.Fatalf("last scale = %d, want 1000", row1000.ActiveRules)
	}
	if row1000.P50SlowdownRatio <= 2.0 {
		t.Fatalf("expected clear unindexed slowdown at 1000 rules, got %.2fx", row1000.P50SlowdownRatio)
	}
	if rep.NegativeRegime1.OverbroadPolicy.FalsePositiveBlocks == 0 {
		t.Fatal("expected overbroad policy negative regime to exhibit false positives")
	}

	// Negative Regime 2: userspace audit-all under 50k/s loop drops heavily; simulated EvalBPF allowlist fast-path drops 0.
	if rep.NegativeRegime2.MeasurementMethod != "userspace_go_cbpf_vm_simulation" {
		t.Fatalf("unexpected measurementMethod = %q", rep.NegativeRegime2.MeasurementMethod)
	}
	if rep.NegativeRegime2.UserspaceAuditAll.AuditDropped == 0 {
		t.Fatal("expected non-zero audit ring drops under 50k/s userspace audit-all backpressure")
	}
	if rep.NegativeRegime2.SimulatedBPFVMFilter.AuditDropped != 0 {
		t.Fatalf("expected 0 drops with simulated EvalBPF allowlist filter, got %d", rep.NegativeRegime2.SimulatedBPFVMFilter.AuditDropped)
	}
}
