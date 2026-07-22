package visor

// fleet_spec_test.go — a linked node's CPU arch + core count + total RAM must
// survive the CLI→server decode (fleetRegistration) and land on the /v1/fleet board
// (byoUnit → fleetSpec), the SAME fields a code-linked run-target carries. This is
// what makes evo-2 (x86_64 / Strix Halo) and spark (aarch64 / GB10) show real arch +
// 128 GB on the world Fleet panel, not just their GPU. Arch is the fleet's `uname -m`
// convention — the SAME string these machines report as code-linked run-targets.

import (
	"encoding/json"
	"testing"

	"github.com/hanzoai/cloud/clients/samples"
)

// cliHostSpecJSON is exactly what `hanzo link` writes as the fleet presence
// activity's Input for spark (GB10, aarch64, 128 GiB).
const cliHostSpecJSON = `{
  "hostname": "spark",
  "os": "linux",
  "arch": "aarch64",
  "cpus": 20,
  "memory": 137438953472,
  "version": "1.50.0",
  "jobQueue": "gpu-jobs",
  "gpus": [{"name": "NVIDIA GB10", "memoryTotal": "122880 MiB"}]
}`

func TestFleetRegistrationDecodesHostSpec(t *testing.T) {
	var reg fleetRegistration
	if err := json.Unmarshal([]byte(cliHostSpecJSON), &reg); err != nil {
		t.Fatalf("decode CLI registration Input: %v", err)
	}
	if reg.Arch != "aarch64" || reg.CPUs != 20 || reg.Memory != 137438953472 {
		t.Fatalf("host spec dropped on decode: arch=%q cpus=%d memory=%d", reg.Arch, reg.CPUs, reg.Memory)
	}
}

func TestByoUnitCarriesHostSpec(t *testing.T) {
	var reg fleetRegistration
	if err := json.Unmarshal([]byte(cliHostSpecJSON), &reg); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// Mirror the mapping byoWorkers performs, then project onto the board.
	w := byoWorker{
		ID: "spark", Hostname: reg.Hostname, Provider: "byo", Status: "online",
		Os: reg.Os, Arch: reg.Arch, CPUs: reg.CPUs, Memory: reg.Memory, GPUs: reg.GPUs,
	}
	u := byoUnit(w)
	if u.Spec == nil {
		t.Fatal("byoUnit dropped the spec — arch/memory would be MISSING on the board")
	}
	if u.Spec.Arch != "aarch64" {
		t.Fatalf("Arch = %q, want aarch64 (the panel renders it as ARM64)", u.Spec.Arch)
	}
	if u.Spec.CPUs != 20 {
		t.Fatalf("CPUs = %d, want 20", u.Spec.CPUs)
	}
	if u.Spec.Memory != 137438953472 {
		t.Fatalf("Memory = %d, want 137438953472 (128 GiB)", u.Spec.Memory)
	}
	if u.Spec.GPUs != 1 || u.Spec.GPUModel != "NVIDIA GB10" {
		t.Fatalf("GPU summary regressed: %+v", u.Spec)
	}
	if u.Source != samples.SourceBYO || u.Kind != samples.KindWorker {
		t.Fatalf("identity/tag regressed: source=%s kind=%s", u.Source, u.Kind)
	}
}

// A CPU-only worker that reported no arch/memory (an older CLI) still renders — the
// spec is simply omitted, never fabricated.
func TestByoUnitOmitsUnknownSpec(t *testing.T) {
	u := byoUnit(byoWorker{ID: "old", Hostname: "old", Provider: "byo", Status: "online"})
	if u.Spec != nil {
		t.Fatalf("an all-zero spec must be omitted, not invented: %+v", u.Spec)
	}
	if u.Unit != "old" || u.Source != samples.SourceBYO {
		t.Fatalf("identity regressed: %+v", u)
	}
}
