package cli

// gpu_spec_test.go — the host static-spec a `hanzo link` node reports so
// GET /v1/fleet can show its CPU arch, core count and total RAM (the fields a
// code-linked box already carries). Real telemetry only: arch is `uname -m`, cores
// are runtime.NumCPU, RAM is parsed from the OS — never a hardcoded machine.

import (
	"os/exec"
	"runtime"
	"strings"
	"testing"
)

func TestParseMemTotalKB(t *testing.T) {
	// A real /proc/meminfo head from a 128 GiB box. MemTotal is in kB; we report bytes.
	meminfo := []byte("MemTotal:       131923980 kB\nMemFree:         1048576 kB\nMemAvailable:  120000000 kB\n")
	if got, want := parseMemTotalKB(meminfo), int64(131923980)*1024; got != want {
		t.Fatalf("parseMemTotalKB = %d, want %d bytes", got, want)
	}
	// Absent / malformed input is reported as 0 (unknown), never a guess.
	for name, in := range map[string]string{
		"empty":       "",
		"no-memtotal": "MemFree: 100 kB\n",
		"malformed":   "MemTotal: notanumber kB\n",
		"no-value":    "MemTotal:\n",
	} {
		if got := parseMemTotalKB([]byte(in)); got != 0 {
			t.Fatalf("%s: parseMemTotalKB = %d, want 0", name, got)
		}
	}
}

// detectMemTotal reads the real host, so on Linux/macOS CI it must return a positive
// byte count — proof the reporter reads actual RAM rather than shipping 0.
func TestDetectMemTotalIsReal(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skipf("no MemTotal source on %s", runtime.GOOS)
	}
	got := detectMemTotal()
	if got <= 0 {
		t.Fatalf("detectMemTotal = %d, want the host's real RAM (>0)", got)
	}
	// Evidence: what THIS host actually reports (never hardcoded). On the GB10 spark
	// box this prints aarch64 + ~128 GiB read from /proc/meminfo.
	t.Logf("real host spec: arch=%s cpus=%d memory=%d bytes (%.1f GiB)",
		detectArch(), runtime.NumCPU(), got, float64(got)/(1<<30))
}

// detectArch must match the fleet's `uname -m` convention (aarch64 | x86_64 | arm64),
// NOT runtime.GOARCH (arm64 | amd64) — so a machine that appears as both a run-target
// and a linked worker shows ONE arch string on the board. On Linux uname -m is
// aarch64/x86_64; assert the real host agrees and is never GOARCH's amd64.
func TestDetectArchMatchesUnameConvention(t *testing.T) {
	got := detectArch()
	if got == "" {
		t.Fatal("detectArch returned empty; must fall back to runtime.GOARCH")
	}
	if out, err := exec.Command("uname", "-m").Output(); err == nil {
		if want := strings.TrimSpace(string(out)); want != "" && got != want {
			t.Fatalf("detectArch = %q, want `uname -m` %q (fleet convention)", got, want)
		}
	}
	// Guard the regression this test exists for: on Linux amd64 the value must be
	// x86_64, never GOARCH's "amd64".
	if runtime.GOOS == "linux" && runtime.GOARCH == "amd64" && got == "amd64" {
		t.Fatal("arch is GOARCH 'amd64'; the fleet convention is 'x86_64'")
	}
	t.Logf("detectArch=%q (GOARCH=%q)", got, runtime.GOARCH)
}

// buildRegistration must carry this host's detected arch (uname -m), cores (NumCPU)
// and RAM — so spark reports aarch64 and evo-2 reports x86_64, both ~128 GB, matching
// how the same machines already report as code-linked run-targets.
func TestBuildRegistrationCarriesHostSpec(t *testing.T) {
	const mem = int64(137438953472) // 128 GiB
	w := &worker{hostname: "spark", jobsNS: "gpu-jobs", arch: "aarch64", memory: mem}
	reg := w.buildRegistration()
	if reg.Arch != "aarch64" {
		t.Fatalf("Arch = %q, want the worker's detected arch %q", reg.Arch, "aarch64")
	}
	if reg.CPUs != runtime.NumCPU() {
		t.Fatalf("CPUs = %d, want runtime.NumCPU %d", reg.CPUs, runtime.NumCPU())
	}
	if reg.Memory != mem {
		t.Fatalf("Memory = %d, want the detected total %d", reg.Memory, mem)
	}
}
