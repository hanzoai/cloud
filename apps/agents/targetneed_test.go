package agents

import "testing"

// The three machines this capability plane exists for, as they ACTUALLY advertise
// themselves today — measured on the boxes, not imagined. All three are 128 GB
// unified-memory accelerators from three different vendors, and all three report
// VRAM 0, each for its own reason:
//
//   - spark  NVIDIA GB10: `nvidia-smi --query-gpu=memory.total` answers "[N/A]"
//     (Grace Blackwell has no discrete VRAM), and parse_nvidia's int parse of
//     "[N/A]" fails -> 0.
//   - dbc    Apple M4 Max: `system_profiler SPDisplaysDataType` emits NO
//     "VRAM (Total):" line on Apple Silicon -> 0.
//   - evo    AMD Radeon 8060S (gfx1151): no nvidia-smi, so the probe falls back to
//     lspci, which carries no memory at all (parse_lspci hardcodes memory: 0) and
//     names the part "Device 1586" because the PCI id is unresolved. rocm-smi DOES
//     report both the real model and the VRAM, and is not consulted.
//
// Holding them here as data means a probe change that starts advertising real
// accelerator memory shows up as these fixtures changing, in one place.
var (
	spark = Spec{OS: "linux", Arch: "arm64", CPUs: 20, Memory: 128 << 30,
		GPUs: []GPU{{Vendor: "nvidia", Model: "GB10", Memory: 0}}}
	dbc = Spec{OS: "darwin", Arch: "arm64", CPUs: 16, Memory: 128 << 30,
		GPUs: []GPU{{Vendor: "apple", Model: "Apple M4 Max", Memory: 0}}}
	evo = Spec{OS: "linux", Arch: "amd64", CPUs: 32, Memory: 128 << 30,
		GPUs: []GPU{{Vendor: "amd", Model: "Advanced Micro Devices, Inc. [AMD/ATI] Device 1586", Memory: 0}}}
	laptop = Spec{OS: "darwin", Arch: "arm64", CPUs: 8, Memory: 16 << 30}
)

// THE point of the whole exercise: ONE requirement, satisfied by three vendors.
// Under `nvidia.com/gpu` only spark could ever match; two boxes that can run the
// same hanzo-kernel source were unroutable because the contract named a vendor.
func TestNeed_OneGPURequirementIsSatisfiedByEveryVendor(t *testing.T) {
	need := Need{GPUs: 1}
	for _, m := range []struct {
		name string
		spec Spec
	}{{"spark/nvidia", spark}, {"dbc/apple", dbc}, {"evo/amd", evo}} {
		if !m.spec.Satisfies(need) {
			t.Errorf("%s: a machine with an accelerator must satisfy Need{GPUs:1}", m.name)
		}
	}
	if laptop.Satisfies(need) {
		t.Error("a machine with no accelerator must NOT satisfy Need{GPUs:1}")
	}
}

// There is no vendor in Need, so no phrasing of a requirement can prefer one. This
// asserts the ABSENCE of the hardcode: swapping only the vendor never changes the
// answer.
func TestNeed_VendorIsNotAMatchableFact(t *testing.T) {
	need := Need{GPUs: 1, CPUs: 4}
	base := Spec{OS: "linux", Arch: "arm64", CPUs: 8, Memory: 64 << 30}
	for _, vendor := range []string{"nvidia", "amd", "apple", "intel", "", "totally-new-vendor"} {
		s := base
		s.GPUs = []GPU{{Vendor: vendor, Model: "x", Memory: 8 << 30}}
		if !s.Satisfies(need) {
			t.Errorf("vendor %q changed the routing answer; vendor must not be matchable", vendor)
		}
	}
}

// Unknown memory must never clear a floor, or a 70B job lands on a box that cannot
// hold it. Today that refuses all three lab boxes -- the honest answer, and the
// reason the probe must learn to report accelerator-addressable memory.
func TestNeed_UnknownVRAMFailsClosed(t *testing.T) {
	need := Need{GPUs: 1, VRAM: 40 << 30}
	for _, m := range []struct {
		name string
		spec Spec
	}{{"spark", spark}, {"dbc", dbc}, {"evo", evo}} {
		if m.spec.Satisfies(need) {
			t.Errorf("%s advertises VRAM 0; an unknown must not satisfy a %d-byte floor", m.name, need.VRAM)
		}
	}
	// The same machine, once it advertises what its accelerator can address, fits.
	honest := spark
	honest.GPUs = []GPU{{Vendor: "nvidia", Model: "GB10", Memory: 128 << 30}}
	if !honest.Satisfies(need) {
		t.Error("a machine advertising 128G of accelerator memory must satisfy a 40G floor")
	}
}

// A VRAM floor with no explicit count still implies an accelerator, so it can never
// be silently satisfied by a machine that has none.
func TestNeed_VRAMFloorImpliesAnAccelerator(t *testing.T) {
	if laptop.Satisfies(Need{VRAM: 1 << 30}) {
		t.Error("a VRAM floor must not be a no-op on a machine with no accelerator")
	}
}

func TestNeed_ZeroNeedIsSatisfiedByAnything(t *testing.T) {
	if !(Need{}).IsZero() {
		t.Fatal("the zero Need must report IsZero")
	}
	for _, s := range []Spec{spark, dbc, evo, laptop, {}} {
		if !s.Satisfies(Need{}) {
			t.Error("the zero Need constrains nothing and must be satisfied by any machine")
		}
	}
}

func TestNeed_CountFloorsAndPlatform(t *testing.T) {
	two := Spec{OS: "linux", Arch: "amd64", CPUs: 64, Memory: 512 << 30, GPUs: []GPU{
		{Vendor: "amd", Model: "a", Memory: 48 << 30},
		{Vendor: "amd", Model: "b", Memory: 16 << 30},
	}}
	cases := []struct {
		name string
		spec Spec
		need Need
		want bool
	}{
		{"count met", two, Need{GPUs: 2}, true},
		{"count exceeded", two, Need{GPUs: 3}, false},
		{"only one clears the vram floor", two, Need{GPUs: 2, VRAM: 32 << 30}, false},
		{"one is enough at that floor", two, Need{GPUs: 1, VRAM: 32 << 30}, true},
		{"cpu floor met", evo, Need{CPUs: 32}, true},
		{"cpu floor missed", laptop, Need{CPUs: 32}, false},
		{"host memory floor met", dbc, Need{Memory: 64 << 30}, true},
		{"host memory floor missed", laptop, Need{Memory: 64 << 30}, false},
		{"os match is case-folded", dbc, Need{OS: "Darwin"}, true},
		{"os mismatch", dbc, Need{OS: "linux"}, false},
		{"arch match", spark, Need{Arch: "arm64"}, true},
		{"arch mismatch", spark, Need{Arch: "amd64"}, false},
		{"arch is orthogonal to os", evo, Need{OS: "linux", Arch: "arm64"}, false},
	}
	for _, c := range cases {
		if got := c.spec.Satisfies(c.need); got != c.want {
			t.Errorf("%s: Satisfies(%+v) = %v, want %v", c.name, c.need, got, c.want)
		}
	}
}
