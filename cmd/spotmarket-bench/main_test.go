// SPDX-License-Identifier: Apache-2.0 OR MIT

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/apps/spotmarket"
)

func TestWriteDossierMatchesRawResults(t *testing.T) {
	cfg := spotmarket.DefaultSimConfig()
	report, err := spotmarket.RunBenchmarkSuite(cfg)
	if err != nil {
		t.Fatalf("RunBenchmarkSuite: %v", err)
	}

	dir := t.TempDir()
	if err := writeDossierFiles(dir, report); err != nil {
		t.Fatalf("writeDossierFiles: %v", err)
	}

	rawBytes, err := os.ReadFile(filepath.Join(dir, "raw_results.json"))
	if err != nil {
		t.Fatalf("read raw_results.json: %v", err)
	}
	var decoded spotmarket.BenchmarkReport
	if err := json.Unmarshal(rawBytes, &decoded); err != nil {
		t.Fatalf("unmarshal raw_results.json: %v", err)
	}

	scoreBytes, err := os.ReadFile(filepath.Join(dir, "score_table.md"))
	if err != nil {
		t.Fatalf("read score_table.md: %v", err)
	}
	readmeBytes, err := os.ReadFile(filepath.Join(dir, "README.md"))
	if err != nil {
		t.Fatalf("read README.md: %v", err)
	}

	wantPrimaryCost := fmt.Sprintf("$%.4f", decoded.PrimarySpot.RealizedCostPer1MUsefulUSD)
	wantBaselineCost := fmt.Sprintf("$%.4f", decoded.FixedBaseline.RealizedCostPer1MUsefulUSD)
	if !strings.Contains(string(scoreBytes), wantPrimaryCost) || !strings.Contains(string(scoreBytes), wantBaselineCost) {
		t.Fatalf("score_table.md missing exact cost figures %s / %s", wantPrimaryCost, wantBaselineCost)
	}
	if !strings.Contains(string(readmeBytes), wantPrimaryCost) || !strings.Contains(string(readmeBytes), wantBaselineCost) {
		t.Fatalf("README.md missing exact cost figures %s / %s", wantPrimaryCost, wantBaselineCost)
	}
}
