// Copyright 2026 Hanzo AI, Inc.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	er "github.com/hanzoai/cloud/research/ensoroutellm"
)

func TestCLIEndToEndExecutionAndJSON(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping full CLI benchmark build/run in -short mode")
	}
	tmp := t.TempDir()
	binName := "ensoroutellmsim"
	goBinName := "go"
	if runtime.GOOS == "windows" {
		binName += ".exe"
		goBinName = "go.exe"
	}
	binPath := filepath.Join(tmp, binName)
	outJSON := filepath.Join(tmp, "acceptance.json")

	buildCmd := exec.Command(goBinName, "build", "-o", binPath, ".")
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("go build ensoroutellmsim failed: %v\n%s", err, string(out))
	}

	rootDir := filepath.Join("..", "..")
	runCmd := exec.Command(
		binPath,
		"-mode", "acceptance",
		"-models-yaml", filepath.Join(rootDir, "research", "ensoroutellm", "testdata", "hanzo_models_snapshot.yaml"),
		"-pricing-json", filepath.Join(rootDir, "research", "ensoroutellm", "testdata", "hanzo_pricing_snapshot.json"),
		"-corpus", filepath.Join(rootDir, "research", "ensoroutellm", "testdata", "livebench_public_features.jsonl"),
		"-perf-iters", "500",
		"-out", outJSON,
	)
	stdout, err := runCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("ensoroutellmsim acceptance run failed: %v\n%s", err, string(stdout))
	}
	if !strings.Contains(string(stdout), "Reliability Audit:") || !strings.Contains(string(stdout), "Matched Budget vs") {
		t.Fatalf("CLI stdout missing expected acceptance summary sections:\n%s", string(stdout))
	}

	raw, err := os.ReadFile(outJSON)
	if err != nil {
		t.Fatalf("read CLI output JSON: %v", err)
	}
	var rep er.AcceptanceReport
	if err := json.Unmarshal(raw, &rep); err != nil {
		t.Fatalf("unmarshal CLI output JSON: %v", err)
	}
	if rep.Schema != "hanzo.ensoroutellm.acceptance/v2" || !rep.AggregateOnly || !rep.Reliability.AllPassed {
		t.Fatalf("unexpected CLI acceptance report: schema=%q agg=%v rel=%+v", rep.Schema, rep.AggregateOnly, rep.Reliability)
	}
}
