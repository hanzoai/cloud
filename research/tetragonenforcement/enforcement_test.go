// Copyright 2026 Hanzo AI, Inc.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package tetragonenforcement

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestProbeMatrixShape locks the deliverable structure: five escape
// classes plus one benign control, each escape probe bound to a committed
// policy file.
func TestProbeMatrixShape(t *testing.T) {
	probes := ProbeMatrix()
	if len(probes) != 6 {
		t.Fatalf("matrix must have 6 probes (5 escapes + 1 control), got %d", len(probes))
	}
	kinds := map[ProbeKind]bool{}
	ids := map[string]bool{}
	escapes := 0
	controls := 0
	for _, p := range probes {
		if p.ID == "" {
			t.Fatal("probe with empty ID")
		}
		if ids[p.ID] {
			t.Fatalf("duplicate probe id %q", p.ID)
		}
		ids[p.ID] = true
		if p.Payload == "" && len(p.Command) == 0 {
			t.Fatalf("probe %s has neither payload nor command", p.ID)
		}
		if p.ExpectBlocked {
			escapes++
			if p.PolicyFile == "" {
				t.Fatalf("escape probe %s must reference a policy file", p.ID)
			}
		} else {
			controls++
			if p.PolicyFile != "" {
				t.Fatalf("control probe %s must not reference a policy file", p.ID)
			}
		}
		if p.Kind == "control" {
			// control probe kind is reserved for the benign control
			if p.ExpectBlocked {
				t.Fatal("control probe must not expect blocking")
			}
		} else {
			kinds[p.Kind] = true
		}
	}
	if escapes != 5 || controls != 1 {
		t.Fatalf("expected 5 escapes + 1 control, got %d + %d", escapes, controls)
	}
	for _, k := range []ProbeKind{ProbeCapability, ProbeNamespace, ProbeModule, ProbeEgress, ProbeSensitiveFile} {
		if !kinds[k] {
			t.Fatalf("escape class %s not covered by the matrix", k)
		}
	}
	if got := len(EscapeProbes()); got != 5 {
		t.Fatalf("EscapeProbes must return 5, got %d", got)
	}
}

// TestPoliciesExistAndAnchor is a policy-regression test: every probe's
// committed TracingPolicy must exist and keep its enforcement anchor (the
// kernel hook plus a blocking action). If Tetragon's policy language
// changes and a policy stops matching, this test fails before any Linux
// run does.
func TestPoliciesExistAndAnchor(t *testing.T) {
	anchors := map[string]string{
		"priv-esc-setuid":       "sys_setuid",
		"namespace-setns":       "sys_setns",
		"kernel-module-finit":   "security_kernel_read_file",
		"egress-testnet":        "tcp_connect",
		"sensitive-file-shadow": "security_file_permission",
	}
	for _, p := range ProbeMatrix() {
		if p.PolicyFile == "" {
			continue
		}
		b, err := os.ReadFile(filepath.Join(".", p.PolicyFile))
		if err != nil {
			t.Fatalf("policy for %s missing: %v", p.ID, err)
		}
		body := string(b)
		if !strings.Contains(body, "kind: TracingPolicy") {
			t.Fatalf("policy %s: missing TracingPolicy kind", p.PolicyFile)
		}
		anchor := anchors[p.ID]
		if !strings.Contains(body, anchor) {
			t.Fatalf("policy %s: missing enforcement anchor %q", p.PolicyFile, anchor)
		}
		if !strings.Contains(body, "Sigkill") {
			t.Fatalf("policy %s: missing Sigkill enforcement action", p.PolicyFile)
		}
	}
}

// TestOverheadPercentTable locks the overhead arithmetic and validation.
func TestOverheadPercentTable(t *testing.T) {
	cases := []struct {
		base, other, want float64
		wantErr           bool
	}{
		{1000, 1000, 0, false},
		{1000, 980, 2, false},
		{1000, 1020, -2, false},
		{0, 100, 0, true},
		{-1, 100, 0, true},
	}
	for _, c := range cases {
		got, err := OverheadPercent(c.base, c.other)
		if c.wantErr {
			if err == nil {
				t.Fatalf("OverheadPercent(%g,%g): want error", c.base, c.other)
			}
			continue
		}
		if err != nil {
			t.Fatalf("OverheadPercent(%g,%g): %v", c.base, c.other, err)
		}
		if got != c.want {
			t.Fatalf("OverheadPercent(%g,%g) = %g, want %g", c.base, c.other, got, c.want)
		}
	}
}

// TestBuildBenchReports checks merging, pointer fields and noise flagging.
func TestBuildBenchReports(t *testing.T) {
	base := []BenchSample{
		{Workload: "open-close", OpsPerSec: 1000},
		{Workload: "loopback-connect", OpsPerSec: 500},
	}
	mon := []BenchSample{{Workload: "open-close", OpsPerSec: 995}}
	enf := []BenchSample{
		{Workload: "open-close", OpsPerSec: 990},
		{Workload: "loopback-connect", OpsPerSec: 510}, // faster than base = noise
	}
	reps := BuildBenchReports(base, mon, enf)
	if len(reps) != 2 {
		t.Fatalf("want 2 workload reports, got %d", len(reps))
	}
	oc := reps[0]
	if oc.Workload != "open-close" || oc.Monitor == nil || oc.Enforcement == nil {
		t.Fatalf("open-close report incomplete: %+v", oc)
	}
	if oc.OverheadMonitorPct == nil || *oc.OverheadMonitorPct != 0.5 {
		t.Fatalf("monitor overhead = %v, want 0.5", oc.OverheadMonitorPct)
	}
	if oc.OverheadEnforcementPct == nil || *oc.OverheadEnforcementPct != 1 {
		t.Fatalf("enforcement overhead = %v, want 1", oc.OverheadEnforcementPct)
	}
	lb := reps[1]
	if lb.Monitor != nil {
		t.Fatal("missing monitor sample must be omitted, not guessed")
	}
	if !lb.Noise || lb.OverheadEnforcementPct == nil || *lb.OverheadEnforcementPct != -2 {
		t.Fatalf("noise sample must be kept raw and flagged, got %+v", lb)
	}
}

// TestBenignPayloadInProcess verifies the benign payload logic directly
// (portable, works on every OS). The full self-exec path is exercised by
// the CLI; executing the *test binary* with payload args is not meaningful
// under `go test`.
func TestBenignPayloadInProcess(t *testing.T) {
	if code := RunPayload("benign"); code != ExitOK {
		t.Fatalf("benign payload must return %d, got %d", ExitOK, code)
	}
}

// TestRunTimeoutClassifiesTimeout verifies the per-probe deadline path
// with a long-running external command (works on every OS).
func TestRunTimeoutClassifiesTimeout(t *testing.T) {
	var cmd []string
	switch runtime.GOOS {
	case "windows":
		// `timeout /t` rejects non-console stdin; ping -n sleeps without it.
		cmd = []string{"ping", "-n", "30", "127.0.0.1"}
	default:
		cmd = []string{"sleep", "30"}
	}
	p := Probe{ID: "timeout-check", Kind: "control", Command: cmd, ExpectBlocked: false}
	start := time.Now()
	res := RunProbe(context.Background(), p, RunOptions{Mode: ModeNone, TimeoutPerProbe: 1200 * time.Millisecond})
	if res.Status != StatusTimeout {
		t.Fatalf("want timeout status, got %+v", res)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("timeout probe ran too long: %s", d)
	}
}

// TestRunUnsupportedPayloadSkipped verifies the honest skip path for
// Linux-only payloads off Linux.
func TestRunUnsupportedPayloadSkipped(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("linux hosts can actually run this payload")
	}
	p := ProbeMatrix()[0] // priv-esc-setuid, RequiresLinux
	if !p.RequiresLinux {
		t.Fatal("first matrix probe must be RequiresLinux")
	}
	res := RunProbe(context.Background(), p, RunOptions{Mode: ModeNone})
	if res.Status != StatusSkipped {
		t.Fatalf("linux-only probe off linux must be skipped, got %+v", res)
	}
}

// TestSummarizeModeAwareness locks the mode-aware classification rules.
func TestSummarizeModeAwareness(t *testing.T) {
	esc := ProbeResult{ProbeID: "priv-esc-setuid", Status: StatusCompleted}
	ctl := ProbeResult{ProbeID: "benign-control", Status: StatusCompleted}
	killedEsc := ProbeResult{ProbeID: "priv-esc-setuid", Status: StatusBlocked}
	killedCtl := ProbeResult{ProbeID: "benign-control", Status: StatusBlocked}

	// Enforcement: completed escape = leak; killed escape = blocked;
	// killed control = false positive; completed control = completed.
	s := Summarize(string(ModeEnforcement), []ProbeResult{esc, ctl, killedEsc, killedCtl})
	if s.Blocked != 1 || s.Leaked != 1 || s.FalsePositives != 1 || s.Completed != 1 {
		t.Fatalf("enforcement summary wrong: %+v", s)
	}

	// Monitor: completed escape = observed (expected in event-only mode).
	s = Summarize(string(ModeMonitor), []ProbeResult{esc, ctl})
	if s.Observed != 1 || s.Leaked != 0 || s.Completed != 1 {
		t.Fatalf("monitor summary wrong: %+v", s)
	}

	// None: nothing can be legitimately blocked.
	s = Summarize(string(ModeNone), []ProbeResult{killedEsc, ctl})
	if s.FalsePositives != 1 || s.Completed != 1 {
		t.Fatalf("none summary wrong: %+v", s)
	}
}

// TestRunResultJSONRoundtrip guards the committed result format.
func TestRunResultJSONRoundtrip(t *testing.T) {
	r := RunResult{
		RunID:      NewRunID(ModeEnforcement, time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)),
		StartedUTC: time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC),
		Mode:       string(ModeEnforcement),
		Host:       HostInfo{OS: "linux", Arch: "amd64", BTFDetected: true},
		Probes: []ProbeResult{
			{ProbeID: "priv-esc-setuid", Kind: ProbeCapability, Status: StatusBlocked, Signal: "SIGKILL", ExitCode: -1},
		},
		Summary: Summarize(string(ModeEnforcement), []ProbeResult{{ProbeID: "priv-esc-setuid", Status: StatusBlocked}}),
	}
	b, err := MarshalResult(r)
	if err != nil {
		t.Fatal(err)
	}
	var back RunResult
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back.RunID != r.RunID || back.Probes[0].Signal != "SIGKILL" || back.Summary.Blocked != 1 {
		t.Fatalf("roundtrip mismatch: %+v", back)
	}
}

// TestRunBenchSmoke runs both workloads briefly and requires positive
// throughput.
func TestRunBenchSmoke(t *testing.T) {
	for _, wl := range []BenchWorkload{BenchOpenClose, BenchLoopbackConnect} {
		ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
		s, err := RunBench(ctx, BenchConfig{Workload: wl, Mode: ModeNone, Duration: 400 * time.Millisecond, Workers: 2})
		cancel()
		if err != nil {
			t.Fatalf("%s: %v", wl, err)
		}
		if s.Ops <= 0 || s.OpsPerSec <= 0 {
			t.Fatalf("%s: no throughput recorded: %+v", wl, s)
		}
	}
}

// TestSchemaFileParses guards the committed results schema.
func TestSchemaFileParses(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("results", "schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := json.Unmarshal(b, &schema); err != nil {
		t.Fatalf("schema.json is not valid JSON: %v", err)
	}
	if schema["$schema"] == nil {
		t.Fatal("schema.json missing $schema")
	}
	defs, ok := schema["definitions"].(map[string]any)
	if !ok {
		t.Fatal("schema.json missing definitions")
	}
	for _, name := range []string{"run_result", "bench_reports"} {
		if defs[name] == nil {
			t.Fatalf("schema.json missing definition %q", name)
		}
	}
}
