// Copyright 2026 Hanzo AI, Inc.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	er "github.com/hanzoai/cloud/research/ensoroutellm"
)

func TestCLIEndToEndExecutionAndJSON(t *testing.T) {
	tmp := t.TempDir()
	binName := "ensoroutellmsim"
	goBinName := "go"
	if runtime.GOOS == "windows" {
		binName += ".exe"
		goBinName += ".exe"
	}
	bin := filepath.Join(tmp, binName)
	outJSON := filepath.Join(tmp, "results.json")

	goCmdPath := filepath.Join(runtime.GOROOT(), "bin", goBinName)
	buildCmd := exec.Command(goCmdPath, "build", "-o", bin, ".")
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("go build failed: %v\n%s", err, string(out))
	}

	runCmd := exec.Command(bin, "-out", outJSON)
	stdout, err := runCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("cli run failed: %v\n%s", err, string(stdout))
	}
	if len(stdout) == 0 {
		t.Fatal("expected formatted table output on stdout")
	}

	raw, err := os.ReadFile(outJSON)
	if err != nil {
		t.Fatalf("read json output: %v", err)
	}
	var rep er.BenchmarkReport
	if err := json.Unmarshal(raw, &rep); err != nil {
		t.Fatalf("unmarshal BenchmarkReport: %v", err)
	}
	if rep.DenseCorpusSize != 600 || rep.GateAudit.ConstrainedTotalViolations != 0 {
		t.Fatalf("unexpected CLI report contents: corpus=%d violations=%d",
			rep.DenseCorpusSize, rep.GateAudit.ConstrainedTotalViolations)
	}
}
