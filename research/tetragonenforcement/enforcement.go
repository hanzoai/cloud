// Copyright 2026 Hanzo AI, Inc.
// SPDX-License-Identifier: Apache-2.0 OR MIT

// Package tetragonenforcement is the research harness for hanzoai/cloud#33
// ([Bounty], eBPF runtime enforcement for agent sandboxes, HIP-1349 draft):
// an escape-attempt matrix runner, a syscall-overhead microbenchmark, and
// the result model both CLIs share.
//
// It contains no eBPF: the kernel side is Cilium Tetragon (Apache-2.0),
// driven by the TracingPolicy YAMLs committed under policies/. This package
// is the user-space evidence collector: it attempts each escape class from
// a fresh child process, classifies the outcome (SIGKILL by enforcement,
// completion = leak), and writes machine-readable results.
//
// Standard library only; no changes to go.mod.
package tetragonenforcement

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// ProbeKind is the escape class a probe exercises.
type ProbeKind string

const (
	// ProbeCapability covers privilege escalation (setuid and friends).
	ProbeCapability ProbeKind = "capability-escalation"
	// ProbeNamespace covers namespace escape attempts (setns).
	ProbeNamespace ProbeKind = "namespace-escape"
	// ProbeModule covers kernel-module loading attempts.
	ProbeModule ProbeKind = "kernel-module-load"
	// ProbeEgress covers network egress outside the sandbox allowlist.
	ProbeEgress ProbeKind = "egress-connect"
	// ProbeSensitiveFile covers reads of host-sensitive files.
	ProbeSensitiveFile ProbeKind = "sensitive-file-read"
)

// Child exit codes used by the payload protocol (see tetragonharness).
const (
	// ExitOK means the payload ran to completion (its syscall was attempted
	// and the process was not killed).
	ExitOK = 0
	// ExitUnsupported means the payload needs an OS the child is not on.
	ExitUnsupported = 3
	// ExitSetup means the payload failed to set up (missing file, etc).
	ExitSetup = 4
)

// Probe describes one escape attempt or control scenario. Payload and
// Command are mutually exclusive: Payload re-executes the harness binary
// itself with the "payload" subcommand (no external binaries needed);
// Command runs an external program directly.
type Probe struct {
	ID            string    `json:"id"`
	Kind          ProbeKind `json:"kind"`
	Title         string    `json:"title"`
	Payload       string    `json:"payload,omitempty"`
	Command       []string  `json:"command,omitempty"`
	PolicyFile    string    `json:"policy_file"`
	ExpectBlocked bool      `json:"expect_blocked"`
	// RequiresLinux marks probes whose syscall does not exist on other
	// operating systems; the runner reports them as "skipped" elsewhere.
	RequiresLinux bool `json:"requires_linux"`
}

// ProbeStatus classifies the child outcome.
//
//	blocked         child was killed by a signal (enforcement worked)
//	completed       child ran to completion (its action was not stopped)
//	timeout         child had to be reaped after the per-probe deadline
//	error           the child could not be started or crashed before probe
//	skipped         probe requires an OS this runner is not on
type ProbeStatus string

const (
	StatusBlocked   ProbeStatus = "blocked"
	StatusCompleted ProbeStatus = "completed"
	StatusTimeout   ProbeStatus = "timeout"
	StatusError     ProbeStatus = "error"
	StatusSkipped   ProbeStatus = "skipped"
)

// ProbeResult is the recorded outcome of one probe under one mode.
type ProbeResult struct {
	ProbeID    string      `json:"probe_id"`
	Kind       ProbeKind   `json:"kind"`
	Title      string      `json:"title"`
	PolicyFile string      `json:"policy_file"`
	Mode       string      `json:"mode"`
	Status     ProbeStatus `json:"status"`
	ExitCode   int         `json:"exit_code"`
	Signal     string      `json:"signal,omitempty"`
	DurationMS int64       `json:"duration_ms"`
	StdoutTail string      `json:"stdout_tail,omitempty"`
	StderrTail string      `json:"stderr_tail,omitempty"`
	Error      string      `json:"error,omitempty"`
}

// RunMode is the Tetragon enforcement mode the operator reports the agent
// was running in for this pass. "none" means no Tetragon at all.
type RunMode string

const (
	ModeNone        RunMode = "none"
	ModeMonitor     RunMode = "monitor"
	ModeEnforcement RunMode = "enforcement"
)

// HostInfo captures the environment of one run so raw results are
// attributable (issue contract: raw results must be reproducible).
type HostInfo struct {
	OS              string `json:"os"`
	Arch            string `json:"arch"`
	Hostname        string `json:"hostname"`
	Kernel          string `json:"kernel,omitempty"`
	BTFDetected     bool   `json:"btf_detected"`
	GoVersion       string `json:"go_version"`
	TetragonVersion string `json:"tetragon_version,omitempty"`
}

// Summary aggregates probe outcomes. Leaked and FalsePositives are the two
// failure counters; both must be zero before a sandbox configuration is
// allowed to move from monitor to enforcement (see README §8).
type Summary struct {
	Total          int `json:"total"`
	Blocked        int `json:"blocked"`
	Leaked         int `json:"leaked"`
	FalsePositives int `json:"false_positives"`
	Observed       int `json:"observed"`
	Completed      int `json:"completed"`
	Timeouts       int `json:"timeouts"`
	Errors         int `json:"errors"`
	Skipped        int `json:"skipped"`
}

// RunResult is the machine-readable record of one escape-matrix pass.
type RunResult struct {
	RunID      string        `json:"run_id"`
	StartedUTC time.Time     `json:"started_utc"`
	Mode       string        `json:"mode"`
	Host       HostInfo      `json:"host"`
	Probes     []ProbeResult `json:"probes"`
	Summary    Summary       `json:"summary"`
}

// RunOptions parameterizes a matrix pass.
type RunOptions struct {
	Mode            RunMode
	TimeoutPerProbe time.Duration
	// TetragonVersion is recorded verbatim from the operator (-tetragon-version).
	TetragonVersion string
	// TailLimit bounds captured stdout/stderr per probe (bytes).
	TailLimit int
}

// ProbeMatrix returns the five escape classes of cloud#33 plus one benign
// control. Each escape probe maps to the committed TracingPolicy that
// should stop it; the benign control must never be stopped (the
// false-positive detector).
func ProbeMatrix() []Probe {
	return []Probe{
		{
			ID:            "priv-esc-setuid",
			Kind:          ProbeCapability,
			Title:         "Attempt setuid(0) from a non-allowlisted sandbox process",
			Payload:       "setuid",
			PolicyFile:    "policies/sandbox-privilege-escalation.yaml",
			ExpectBlocked: true,
			RequiresLinux: true,
		},
		{
			ID:            "namespace-setns",
			Kind:          ProbeNamespace,
			Title:         "Attempt setns() on a pid namespace from a non-allowlisted binary",
			Payload:       "setns",
			PolicyFile:    "policies/sandbox-namespace-escape.yaml",
			ExpectBlocked: true,
			RequiresLinux: true,
		},
		{
			ID:            "kernel-module-finit",
			Kind:          ProbeModule,
			Title:         "Attempt finit_module() kernel module load from a sandbox process",
			Payload:       "kernel-module",
			PolicyFile:    "policies/sandbox-kernel-modules.yaml",
			ExpectBlocked: true,
			RequiresLinux: true,
		},
		{
			ID:            "egress-testnet",
			Kind:          ProbeEgress,
			Title:         "TCP connect to a non-allowlisted address (TEST-NET-3)",
			Payload:       "egress",
			PolicyFile:    "policies/sandbox-egress-enforce.yaml",
			ExpectBlocked: true,
			RequiresLinux: false, // the attempt is portable; enforcement results need Linux
		},
		{
			ID:            "sensitive-file-shadow",
			Kind:          ProbeSensitiveFile,
			Title:         "Read attempt on /etc/shadow from a sandbox process",
			Payload:       "sensitive-file",
			PolicyFile:    "policies/sandbox-sensitive-files.yaml",
			ExpectBlocked: true,
			RequiresLinux: true,
		},
		{
			ID:            "benign-control",
			Kind:          "control",
			Title:         "Temp-file write/read plus loopback dial; must never be killed",
			Payload:       "benign",
			PolicyFile:    "",
			ExpectBlocked: false,
			RequiresLinux: false,
		},
	}
}

// EscapeProbes returns only the escape-class probes (not the control).
func EscapeProbes() []Probe {
	var out []Probe
	for _, p := range ProbeMatrix() {
		if p.ExpectBlocked {
			out = append(out, p)
		}
	}
	return out
}

// limitedBuffer captures at most limit bytes (tails of child output).
type limitedBuffer struct {
	buf   bytes.Buffer
	limit int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.buf.Len() < b.limit {
		n := b.limit - b.buf.Len()
		if n > len(p) {
			n = len(p)
		}
		b.buf.Write(p[:n])
	}
	return len(p), nil //nolint:errcheck // best-effort capture
}

// RunProbe executes one probe and classifies the outcome. Exported so the
// CLI and tests share exactly one code path.
func RunProbe(ctx context.Context, p Probe, opts RunOptions) ProbeResult {
	res := ProbeResult{
		ProbeID:    p.ID,
		Kind:       p.Kind,
		Title:      p.Title,
		PolicyFile: p.PolicyFile,
		Mode:       string(opts.Mode),
		ExitCode:   -1,
	}
	timeout := opts.TimeoutPerProbe
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	tail := opts.TailLimit
	if tail <= 0 {
		tail = 4096
	}

	if p.RequiresLinux && runtime.GOOS != "linux" {
		res.Status = StatusSkipped
		res.Error = "probe requires linux"
		return res
	}

	self, err := os.Executable()
	if err != nil && p.Payload != "" {
		res.Status = StatusError
		res.Error = fmt.Sprintf("resolve self: %v", err)
		return res
	}

	var cmd *exec.Cmd
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if p.Payload != "" {
		cmd = exec.CommandContext(cctx, self, "payload", p.Payload)
	} else if len(p.Command) > 0 {
		cmd = exec.CommandContext(cctx, p.Command[0], p.Command[1:]...)
	} else {
		res.Status = StatusError
		res.Error = "probe has neither payload nor command"
		return res
	}

	var so, se limitedBuffer
	so.limit, se.limit = tail, tail
	cmd.Stdout = &so
	cmd.Stderr = &se

	start := time.Now()
	runErr := cmd.Run()
	res.DurationMS = time.Since(start).Milliseconds()
	res.StdoutTail = strings.ToValidUTF8(so.buf.String(), "\uFFFD")
	res.StderrTail = strings.ToValidUTF8(se.buf.String(), "\uFFFD")

	if runErr == nil {
		res.Status = StatusCompleted
		res.ExitCode = 0
		return res
	}
	if ctx.Err() != nil {
		// The parent context was cancelled; the classification is moot.
		res.Status = StatusError
		res.Error = "parent context cancelled: " + ctx.Err().Error()
		return res
	}
	if cctx.Err() == context.DeadlineExceeded {
		res.Status = StatusTimeout
		res.Error = fmt.Sprintf("probe exceeded %s", timeout)
		return res
	}
	if signaled, sigName := signalOf(runErr); signaled {
		res.Status = StatusBlocked
		res.Signal = sigName
		res.ExitCode = -1
		return res
	}
	if ee, ok := runErr.(*exec.ExitError); ok {
		res.ExitCode = ee.ExitCode()
		res.Status = StatusCompleted
		return res
	}
	res.Status = StatusError
	res.Error = runErr.Error()
	return res
}

// Summarize aggregates probe results under the given run mode. Mode-aware:
// under monitor an ExpectBlocked probe that completes is the *expected*
// observation (event-only mode), under enforcement it is a leak.
func Summarize(mode string, rs []ProbeResult) Summary {
	s := Summary{Total: len(rs)}
	for _, r := range rs {
		switch {
		case r.Status == StatusSkipped:
			s.Skipped++
		case r.Status == StatusError:
			s.Errors++
		case r.Status == StatusTimeout:
			s.Timeouts++
		case r.Status == StatusBlocked:
			if r.ExpectBlockedEquivalent() && mode != string(ModeNone) {
				s.Blocked++
			} else {
				// Either the probe is a control (must never be killed) or
				// no agent is running at all (nothing can legitimately
				// block anything) — both are false positives.
				s.FalsePositives++
			}
		case r.Status == StatusCompleted:
			if r.ExpectBlockedEquivalent() {
				switch mode {
				case string(ModeMonitor):
					s.Observed++
				case string(ModeEnforcement):
					s.Leaked++
				default:
					s.Completed++
				}
			} else {
				s.Completed++
			}
		}
	}
	return s
}

// ExpectBlockedEquivalent reports whether this result belongs to a probe
// that expected to be blocked. It re-derives the expectation from the
// matrix so Summarize stays a pure function over results.
func (r ProbeResult) ExpectBlockedEquivalent() bool {
	for _, p := range ProbeMatrix() {
		if p.ID == r.ProbeID {
			return p.ExpectBlocked
		}
	}
	return false
}

// gatherHostInfo fills HostInfo best-effort (portable).
func GatherHostInfo(tetragonVersion string) HostInfo {
	h := HostInfo{
		OS:              runtime.GOOS,
		Arch:            runtime.GOARCH,
		GoVersion:       runtime.Version(),
		TetragonVersion: tetragonVersion,
		BTFDetected:     btfAvailable(),
	}
	if hn, err := os.Hostname(); err == nil {
		h.Hostname = hn
	}
	h.Kernel = kernelVersion()
	return h
}

// NewRunID builds a stable run identifier: mode + UTC timestamp.
func NewRunID(mode RunMode, t time.Time) string {
	return fmt.Sprintf("%s-%s", mode, t.UTC().Format("20060102T150405Z"))
}

// MarshalResult renders a RunResult as indented JSON (committed format).
func MarshalResult(r RunResult) ([]byte, error) {
	return json.MarshalIndent(r, "", "  ")
}

// String renders the summary as a compact human-readable line.
func (s Summary) String() string {
	return fmt.Sprintf("total=%d blocked=%d leaked=%d false_positives=%d observed=%d completed=%d timeouts=%d errors=%d skipped=%d",
		s.Total, s.Blocked, s.Leaked, s.FalsePositives, s.Observed, s.Completed, s.Timeouts, s.Errors, s.Skipped)
}
