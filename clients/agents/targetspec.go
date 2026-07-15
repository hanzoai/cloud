package agents

import (
	"encoding/json"
	"math"
	"strings"
)

// targetspec.go is the machine-capability value plane for a run-target: two orthogonal
// values a linked computer carries so mission-control can answer "which machine, and
// can it run this?" without copying the fact onto every session.
//
//   - Spec — what the machine IS (os/arch/cpus/memory/gpus): static, rarely changes.
//   - Metrics — what the machine is DOING now (loadavg/memory/gpu-util): the last
//     heartbeat, with At = the server second it was recorded (the staleness clock).
//
// `hanzo code --link` captures both from explicit system sources (never the process
// environment) and reports them on the target. They are stored as JSON on the target
// row (agent_targets.spec / .metrics) — one column per concept, extensible without
// schema churn — and every field is bounded on write (sanitize) so a hostile or buggy
// client can never bloat the row or smuggle a non-finite float that would break JSON.

// GPU is one accelerator on a machine.
type GPU struct {
	Vendor string `json:"vendor,omitempty"` // nvidia | amd | apple | intel | ...
	Model  string `json:"model,omitempty"`  // "GB10", "8060S", "RTX 4090"
	Memory int64  `json:"memory,omitempty"` // VRAM bytes, 0 = unknown
}

// Spec is a machine's static capability.
type Spec struct {
	OS     string `json:"os,omitempty"`     // linux | darwin | windows
	Arch   string `json:"arch,omitempty"`   // amd64 | arm64 | ...
	CPUs   int    `json:"cpus,omitempty"`   // logical cores
	Memory int64  `json:"memory,omitempty"` // total RAM, bytes
	GPUs   []GPU  `json:"gpus,omitempty"`
}

// Metrics is a machine's live state from the last heartbeat.
type Metrics struct {
	Load1   float64 `json:"load1,omitempty"`
	Load5   float64 `json:"load5,omitempty"`
	Load15  float64 `json:"load15,omitempty"`
	MemUsed int64   `json:"memUsed,omitempty"` // bytes
	MemFree int64   `json:"memFree,omitempty"` // bytes
	GPUUtil float64 `json:"gpuUtil,omitempty"` // 0..1 aggregate utilization
	At      int64   `json:"at,omitempty"`      // unix seconds, server-stamped
}

const (
	maxGPUs      = 32   // an absurd count is a bug or an attack, not a real host
	maxSpecField = 64   // os/arch/gpu vendor/model
	maxCPUs      = 8192 // clamps a garbage core count
)

// IsZero reports an all-empty spec (nothing worth storing).
func (s Spec) IsZero() bool {
	return s.OS == "" && s.Arch == "" && s.CPUs == 0 && s.Memory == 0 && len(s.GPUs) == 0
}

// IsZero reports an all-empty metrics sample.
func (m Metrics) IsZero() bool {
	return m.Load1 == 0 && m.Load5 == 0 && m.Load15 == 0 &&
		m.MemUsed == 0 && m.MemFree == 0 && m.GPUUtil == 0 && m.At == 0
}

// Sanitize bounds every field so a target row stays small and well-formed no matter
// what a client sends: strings trimmed + length-capped, counts/sizes non-negative and
// clamped, GPU list truncated, floats coerced finite. It is total (never errors) so
// the write path can always proceed with a safe value.
func (s Spec) Sanitize() Spec {
	out := Spec{
		OS:     clampStr(s.OS, maxSpecField),
		Arch:   clampStr(s.Arch, maxSpecField),
		CPUs:   clampInt(s.CPUs, maxCPUs),
		Memory: nonNegI64(s.Memory),
	}
	for i, g := range s.GPUs {
		if i >= maxGPUs {
			break
		}
		g = GPU{Vendor: clampStr(g.Vendor, maxSpecField), Model: clampStr(g.Model, maxSpecField), Memory: nonNegI64(g.Memory)}
		if g == (GPU{}) {
			continue
		}
		out.GPUs = append(out.GPUs, g)
	}
	return out
}

// Sanitize coerces a metrics sample into a safe, finite range. It does NOT set At —
// the server stamps that so a client can never backdate or forge the staleness clock.
func (m Metrics) Sanitize() Metrics {
	return Metrics{
		Load1:   nonNegF(m.Load1),
		Load5:   nonNegF(m.Load5),
		Load15:  nonNegF(m.Load15),
		MemUsed: nonNegI64(m.MemUsed),
		MemFree: nonNegI64(m.MemFree),
		GPUUtil: clampF01(m.GPUUtil),
	}
}

func clampStr(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return strings.ToValidUTF8(s[:n], "")
	}
	return s
}

func clampInt(i, hi int) int {
	if i < 0 {
		return 0
	}
	if i > hi {
		return hi
	}
	return i
}

func nonNegI64(i int64) int64 {
	if i < 0 {
		return 0
	}
	return i
}

// nonNegF returns a finite, non-negative float (NaN/Inf/negative → 0), so a hostile
// loadavg can never poison the JSON encode or the display.
func nonNegF(f float64) float64 {
	if math.IsNaN(f) || math.IsInf(f, 0) || f < 0 {
		return 0
	}
	return f
}

// clampF01 returns a finite float in [0,1] (utilization).
func clampF01(f float64) float64 {
	if math.IsNaN(f) || math.IsInf(f, 0) || f < 0 {
		return 0
	}
	if f > 1 {
		return 1
	}
	return f
}

// encodeSpec/decodeSpec + encodeMetrics/decodeMetrics are the column codecs. An empty
// value encodes to "" (a NULL-equivalent the column defaults to), and a malformed
// stored blob decodes to the zero value rather than failing a whole target read.
func encodeSpec(s Spec) string {
	if s.IsZero() {
		return ""
	}
	b, err := json.Marshal(s)
	if err != nil {
		return ""
	}
	return string(b)
}

func decodeSpec(raw string) Spec {
	if strings.TrimSpace(raw) == "" {
		return Spec{}
	}
	var s Spec
	if json.Unmarshal([]byte(raw), &s) != nil {
		return Spec{}
	}
	return s
}

func encodeMetrics(m Metrics) string {
	if m.IsZero() {
		return ""
	}
	b, err := json.Marshal(m)
	if err != nil {
		return ""
	}
	return string(b)
}

func decodeMetrics(raw string) Metrics {
	if strings.TrimSpace(raw) == "" {
		return Metrics{}
	}
	var m Metrics
	if json.Unmarshal([]byte(raw), &m) != nil {
		return Metrics{}
	}
	return m
}
