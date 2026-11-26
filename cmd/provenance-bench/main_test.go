// SPDX-License-Identifier: Apache-2.0

package main

import (
	"path/filepath"
	"testing"
)

func TestRunBenchmarkAndAdversarialMatrix(t *testing.T) {
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	report, err := runBenchmark(repoRoot, "", 20, 40, 1349)
	if err != nil {
		t.Fatalf("runBenchmark failed: %v", err)
	}
	if report.Corpus.ArtifactCount < 5 {
		t.Fatalf("expected >= 5 real corpus artifacts, got %d", report.Corpus.ArtifactCount)
	}
	if report.Adversarial.TotalVectors != 16 {
		t.Fatalf("expected 16 adversarial vectors, got %d", report.Adversarial.TotalVectors)
	}
	if report.Adversarial.PassedCount != report.Adversarial.TotalVectors {
		for _, v := range report.Adversarial.Vectors {
			if !v.Passed {
				t.Errorf("vector %s failed: want %s, got %s (%s)", v.ID, v.ExpectedCode, v.ActualCode, v.ErrorDetail)
			}
		}
		t.Fatalf("adversarial refusal pass count %d/%d", report.Adversarial.PassedCount, report.Adversarial.TotalVectors)
	}
	if len(report.NegativeRegimes.InlineRawBlobVsDigestOnly) != 4 {
		t.Fatalf("expected 4 inline blob negative-regime points, got %d", len(report.NegativeRegimes.InlineRawBlobVsDigestOnly))
	}
	if report.NegativeRegimes.SmallFilesBatching.BatchSpeedupX <= 1.0 {
		t.Fatalf("expected multi-subject batching speedup > 1x, got %.2fx", report.NegativeRegimes.SmallFilesBatching.BatchSpeedupX)
	}
}
