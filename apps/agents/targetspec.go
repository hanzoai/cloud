package agents

import (
	"encoding/json"
	"math"
	"strings"

	"github.com/hanzoai/cloud/sdk/text"
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
	// GPUs is every accelerator the machine advertises, one entry each, capped at
	// 32 on write. Empty means the probe found none — and that is the answer a
	// Need is checked against, so a machine with no entry here clears no
	// accelerator floor. The list is not vendor-filtered: what satisfies a job is
	// counts and VRAM, never a brand (see Need).
	GPUs []GPU `json:"gpus,omitempty"`
	// ReadOnlyRuns says this machine's harness can run one routed run against a
	// read-only workspace, without commit, push or PR, and refuses one it cannot
	// honour. Absent or false means unsupported: a read-only run is never sent here.
	// It is advertised by the harness itself, not inferred from os/arch/GPUs.
	ReadOnlyRuns bool `json:"readOnlyRuns,omitempty"`
}

// Metrics is a machine's live state from the last heartbeat.
type Metrics struct {
	// Load1 is the machine's own one-minute load average — a count of runnable and
	// uninterruptible tasks, NOT a percentage and NOT already divided by core
	// count, so it is read against Spec.CPUs: 8.0 is idle on 16 cores and swamped
	// on 4. Coerced finite and non-negative on write, so 0 means either genuinely
	// idle or nothing reported.
	Load1 float64 `json:"load1,omitempty"`
	// Load5 is the same figure averaged over five minutes.
	Load5 float64 `json:"load5,omitempty"`
	// Load15 is the same figure over fifteen. The three together are what separate
	// a machine that is busy right now from one that has been busy all along —
	// which is the question a dispatcher is really asking.
	Load15  float64 `json:"load15,omitempty"`
	MemUsed int64   `json:"memUsed,omitempty"` // bytes
	MemFree int64   `json:"memFree,omitempty"` // bytes
	GPUUtil float64 `json:"gpuUtil,omitempty"` // 0..1 aggregate utilization
	// CPUUtil is the busy fraction of all cores over the sample window, 0..1.
	CPUUtil float64 `json:"cpuUtil,omitempty"`
	// CPUTemp is the CPU package temperature in °C.
	CPUTemp float64 `json:"cpuTemp,omitempty"`
	// GPUTemp is the hottest GPU's temperature in °C.
	GPUTemp float64 `json:"gpuTemp,omitempty"`
	// GPUPower is the total GPU power draw in watts.
	GPUPower float64 `json:"gpuPower,omitempty"`
	// GPUPowerLimit is the total enforced GPU power limit in watts; absent when the driver reports none.
	GPUPowerLimit float64 `json:"gpuPowerLimit,omitempty"`
	// GPUMemUsed is the memory held by GPU processes, in bytes.
	GPUMemUsed int64 `json:"gpuMemUsed,omitempty"`
	// GPUMemTotal is dedicated VRAM in bytes; absent when the GPU shares system memory (unified).
	GPUMemTotal int64 `json:"gpuMemTotal,omitempty"`
	// DiskUsed is the space used across local block filesystems, in bytes.
	DiskUsed int64 `json:"diskUsed,omitempty"`
	// DiskTotal is the size of those filesystems, in bytes.
	DiskTotal int64 `json:"diskTotal,omitempty"`
	// DiskRead is the block-device read rate in bytes per second.
	DiskRead float64 `json:"diskRead,omitempty"`
	// DiskWrite is the block-device write rate in bytes per second.
	DiskWrite float64 `json:"diskWrite,omitempty"`
	// NetRx is the receive rate of physical interfaces in bytes per second.
	NetRx float64 `json:"netRx,omitempty"`
	// NetTx is the transmit rate of physical interfaces in bytes per second.
	NetTx float64 `json:"netTx,omitempty"`
	// Model is the model id(s) served on this machine, comma-separated, at most 128 bytes.
	Model string `json:"model,omitempty"`
	// Decode is generation throughput over the sample window, in tokens per second.
	Decode float64 `json:"decode,omitempty"`
	// Prefill is prompt (prefill) throughput over the sample window, in tokens per second.
	Prefill float64 `json:"prefill,omitempty"`
	// TTFT is the mean time to first token, in seconds, of requests that started in the window.
	TTFT float64 `json:"ttft,omitempty"`
	// Running is the number of requests being served now.
	Running int `json:"running,omitempty"`
	// Waiting is the number of requests queued.
	Waiting int `json:"waiting,omitempty"`
	// KVCache is the KV-cache occupancy, 0..1.
	KVCache float64 `json:"kvCache,omitempty"`
	At      int64   `json:"at,omitempty"` // unix seconds, server-stamped
}

const (
	maxGPUs      = 32   // an absurd count is a bug or an attack, not a real host
	maxSpecField = 64   // os/arch/gpu vendor/model
	maxCPUs      = 8192 // clamps a garbage core count

	maxTemp    = 200     // °C
	maxPower   = 100000  // W
	maxRate    = 1e12    // B/s, disk and network
	maxTokRate = 1e7     // tok/s, decode and prefill
	maxTTFT    = 3600    // s
	maxReqs    = 1000000 // running, waiting
	maxModel   = 128     // bytes
)

// IsZero reports an all-empty spec (nothing worth storing).
func (s Spec) IsZero() bool {
	return s.OS == "" && s.Arch == "" && s.CPUs == 0 && s.Memory == 0 && len(s.GPUs) == 0 && !s.ReadOnlyRuns
}

// IsZero reports an all-empty metrics sample.
func (m Metrics) IsZero() bool { return m == Metrics{} }

// Sanitize bounds every field so a target row stays small and well-formed no matter
// what a client sends: strings trimmed + length-capped, counts/sizes non-negative and
// clamped, GPU list truncated, floats coerced finite. It is total (never errors) so
// the write path can always proceed with a safe value.
func (s Spec) Sanitize() Spec {
	out := Spec{
		OS:           clampStr(s.OS, maxSpecField),
		Arch:         clampStr(s.Arch, maxSpecField),
		CPUs:         clampInt(s.CPUs, maxCPUs),
		Memory:       nonNegI64(s.Memory),
		ReadOnlyRuns: s.ReadOnlyRuns,
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
		Load1:         nonNegF(m.Load1),
		Load5:         nonNegF(m.Load5),
		Load15:        nonNegF(m.Load15),
		MemUsed:       nonNegI64(m.MemUsed),
		MemFree:       nonNegI64(m.MemFree),
		GPUUtil:       clampF01(m.GPUUtil),
		CPUUtil:       clampF01(m.CPUUtil),
		CPUTemp:       clampF(m.CPUTemp, maxTemp),
		GPUTemp:       clampF(m.GPUTemp, maxTemp),
		GPUPower:      clampF(m.GPUPower, maxPower),
		GPUPowerLimit: clampF(m.GPUPowerLimit, maxPower),
		GPUMemUsed:    nonNegI64(m.GPUMemUsed),
		GPUMemTotal:   nonNegI64(m.GPUMemTotal),
		DiskUsed:      nonNegI64(m.DiskUsed),
		DiskTotal:     nonNegI64(m.DiskTotal),
		DiskRead:      clampF(m.DiskRead, maxRate),
		DiskWrite:     clampF(m.DiskWrite, maxRate),
		NetRx:         clampF(m.NetRx, maxRate),
		NetTx:         clampF(m.NetTx, maxRate),
		Model:         clampStr(m.Model, maxModel),
		Decode:        clampF(m.Decode, maxTokRate),
		Prefill:       clampF(m.Prefill, maxTokRate),
		TTFT:          clampF(m.TTFT, maxTTFT),
		Running:       clampInt(m.Running, maxReqs),
		Waiting:       clampInt(m.Waiting, maxReqs),
		KVCache:       clampF01(m.KVCache),
	}
}

func clampStr(s string, n int) string {
	return strings.ToValidUTF8(text.Cut(strings.TrimSpace(s), n), "")
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

// clampF returns a finite float in [0,hi] (NaN/Inf/negative → 0).
func clampF(f, hi float64) float64 {
	f = nonNegF(f)
	if f > hi {
		return hi
	}
	return f
}

// clampF01 returns a finite float in [0,1] (utilization).
func clampF01(f float64) float64 { return clampF(f, 1) }

// Need is what a job requires OF a machine, written in the SAME vocabulary a machine
// advertises. It is the other half of Spec: Spec says what a machine has, Need says
// what a job wants, and Satisfies is the ONE place the two meet.
//
// THERE IS NO VENDOR FIELD, AND THAT IS THE POINT. `resourcesPerNode.limits.
// "nvidia.com/gpu"` is not a requirement, it is one vendor's name for a requirement —
// baking it into the scheduler contract is what made a GPU job unroutable to an AMD or
// Apple machine that could have run it. A job needs ACCELERATORS with enough memory;
// which vendor satisfies that is the machine's business, and hanzo-kernel lowers one
// kernel source to CUDA/ROCm/Vulkan/Metal precisely so the job never has to care.
// Re-adding a vendor here would reintroduce the hardcode as a value, so it stays out:
// a requirement no advertised capability can express is not a requirement.
//
// The zero Need is "anything will do" — every field is a floor that only constrains
// when set, so an unrelated caller is never forced to describe a machine it does not
// care about.
type Need struct {
	GPUs   int    `json:"gpus,omitempty"`   // accelerators required
	VRAM   int64  `json:"vram,omitempty"`   // bytes each accelerator must address
	CPUs   int    `json:"cpus,omitempty"`   // logical cores
	Memory int64  `json:"memory,omitempty"` // host RAM bytes
	OS     string `json:"os,omitempty"`     // linux | darwin | windows
	Arch   string `json:"arch,omitempty"`   // amd64 | arm64 | ...
}

// IsZero reports a Need that constrains nothing.
func (n Need) IsZero() bool {
	return n.GPUs == 0 && n.VRAM == 0 && n.CPUs == 0 && n.Memory == 0 && n.OS == "" && n.Arch == ""
}

// Satisfies reports whether this machine's advertised capability meets a job's Need.
// It is a pure function of two values — no clock, no store, no vendor table — so the
// dispatch gate, a scheduler and a UI preview all get the same answer from the same
// rule, and a test can state a fleet as data.
//
// UNKNOWN IS NOT ENOUGH. A machine that advertises VRAM 0 does not satisfy a VRAM
// floor: 0 means "the probe could not tell", and admitting it would route a 70B job
// to a machine that cannot hold it. This is deliberately fail-closed, and it is why
// the probe reporting truthful accelerator memory matters — on a unified-memory
// machine (Apple Silicon, an NVIDIA GB10, an AMD APU) nvidia-smi/system_profiler/lspci
// report no discrete VRAM, so such a box advertises 0 and is refused by any VRAM floor
// until it advertises the memory its accelerator can actually address.
func (s Spec) Satisfies(n Need) bool {
	if n.CPUs > 0 && s.CPUs < n.CPUs {
		return false
	}
	if n.Memory > 0 && s.Memory < n.Memory {
		return false
	}
	if n.OS != "" && !strings.EqualFold(strings.TrimSpace(s.OS), strings.TrimSpace(n.OS)) {
		return false
	}
	if n.Arch != "" && !strings.EqualFold(strings.TrimSpace(s.Arch), strings.TrimSpace(n.Arch)) {
		return false
	}
	// Accelerators: a VRAM floor implies at least one, so "vram only" is not a silent
	// no-op on a machine with no GPU at all.
	want := n.GPUs
	if want == 0 && n.VRAM > 0 {
		want = 1
	}
	if want == 0 {
		return true
	}
	fit := 0
	for _, g := range s.GPUs {
		if n.VRAM > 0 && g.Memory < n.VRAM {
			continue // 0 (unknown) never clears a floor
		}
		fit++
	}
	return fit >= want
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
