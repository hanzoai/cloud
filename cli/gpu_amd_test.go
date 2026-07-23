package cli

import (
	"os"
	"path/filepath"
	"testing"
)

// TestParseRocmSmiCSV — the AMD GPU inventory names a card from its marketing
// series + gfx target, exactly as `rocm-smi --showproductname --csv` reports on
// evo's gfx1151 Radeon 8060S. This is the primary AMD detection path.
func TestParseRocmSmiCSV(t *testing.T) {
	// Real evo output (header + one card row).
	csv := []byte("device,Card Series,Card Model,Card Vendor,Card SKU,Subsystem ID,Device Rev,Node ID,GUID,GFX Version\n" +
		"card0,Radeon 8060S Graphics,0x1586,Advanced Micro Devices Inc. [AMD/ATI],STRXLGEN,-0x7fe3,0xc1,1,49819,gfx1151\n")
	gpus := parseRocmSmiCSV(csv)
	if len(gpus) != 1 {
		t.Fatalf("want 1 AMD GPU, got %d (%+v)", len(gpus), gpus)
	}
	if got, want := gpus[0].Name, "AMD Strix Halo \u00b7 Radeon 8060S Graphics (gfx1151)"; got != want {
		t.Errorf("name = %q, want %q", got, want)
	}
}

// TestGfxNameDecodesTargetVersion — the kfd fallback decodes gfx_target_version.
func TestGfxNameDecodesTargetVersion(t *testing.T) {
	for _, c := range []struct {
		v    int
		want string
	}{
		{110501, "gfx1151"}, // evo Radeon 8060S / Strix Halo
		{90012, "gfx9012"},  // sanity: MI-class encoding shape
		{100300, "gfx1030"}, // RDNA2
	} {
		if got := gfxName(c.v); got != c.want {
			t.Errorf("gfxName(%d) = %q, want %q", c.v, got, c.want)
		}
	}
}

// TestParseKfdTopology — the driver-only fallback (no rocm-smi) still finds the GPU
// from /sys/class/kfd: node 0 is the CPU (simd_count 0, skipped), node 1 is the
// gfx1151 GPU. Built against a fixture tree mirroring evo's real properties files.
func TestParseKfdTopology(t *testing.T) {
	root := t.TempDir()
	writeNode(t, root, "0", "simd_count 0\ngfx_target_version 0\n")
	writeNode(t, root, "1", "cpu_cores_count 0\nsimd_count 80\ngfx_target_version 110501\n")
	gpus := parseKfdTopology(root)
	if len(gpus) != 1 {
		t.Fatalf("want 1 GPU node (CPU node 0 skipped), got %d (%+v)", len(gpus), gpus)
	}
	if got, want := gpus[0].Name, "AMD Strix Halo (gfx1151)"; got != want {
		t.Errorf("name = %q, want %q", got, want)
	}
	if got, want := gpus[0].Arch, "gfx1151"; got != want {
		t.Errorf("arch = %q, want %q", got, want)
	}
}

// TestParseVulkaninfoSummary — the last resort keeps only AMD/Radeon devices so it
// never double-counts a card another vendor path already reported.
func TestParseVulkaninfoSummary(t *testing.T) {
	out := []byte("GPU0:\n\tdeviceName = AMD Radeon Graphics (RADV GFX1151)\n" +
		"GPU1:\n\tdeviceName = llvmpipe (LLVM 18.1.0, 256 bits)\n")
	gpus := parseVulkaninfoSummary(out)
	if len(gpus) != 1 {
		t.Fatalf("want 1 AMD device (llvmpipe filtered), got %d (%+v)", len(gpus), gpus)
	}
	if got := gpus[0].Name; got != "AMD Radeon Graphics (RADV GFX1151)" {
		t.Errorf("name = %q", got)
	}
}

func writeNode(t *testing.T, root, id, props string) {
	t.Helper()
	dir := filepath.Join(root, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "properties"), []byte(props), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestPickAmdMemUnified — an APU whose VRAM is a token carve-out reports the
// machine's unified RAM snapped to hardware capacity (evo: 1 GiB VRAM, 118 GiB
// GTT, 124.4 GiB kernel-visible of 128 GiB physical → 128 GiB); a discrete card
// keeps its VRAM.
func TestPickAmdMemUnified(t *testing.T) {
	if miB, unified := pickAmdMem(1024, 120832, 127411); !unified || miB != 131072 {
		t.Errorf("APU: got (%d, %v), want (131072, true)", miB, unified)
	}
	if miB, unified := pickAmdMem(24576, 8192, 127411); unified || miB != 24576 {
		t.Errorf("discrete: got (%d, %v), want (24576, false)", miB, unified)
	}
}

// TestSnapUnified — a small firmware gap snaps up to the DIMM capacity; a big
// carve-out is real lost capacity and stays as-is; exact multiples stand.
func TestSnapUnified(t *testing.T) {
	for _, c := range []struct{ in, want int64 }{
		{127411, 131072}, // evo: 124.4 GiB visible of 128 GiB physical
		{124610, 131072}, // spark GB10: 121.7 GiB visible of 128 GiB physical (~6.3 GiB firmware)
		{131072, 131072}, // exact 128 GiB
		{119194, 119194}, // ~116 GiB visible (16 GiB BIOS carve) — 11.6 GiB gap, keep honest
		{63488, 65536},   // 62 GiB visible of 64 GiB
	} {
		if got := snapUnified(c.in); got != c.want {
			t.Errorf("snapUnified(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}

// TestAmdAPUName — the board renders the APU as the processor the owner bought,
// normalized from the BIOS shouting; non-Ryzen hosts opt out.
func TestAmdAPUName(t *testing.T) {
	if got, want := amdAPUName("AMD RYZEN AI MAX+ 395 w/ Radeon 8060S"), "AMD Ryzen AI Max+ 395 w/ Radeon 8060S"; got != want {
		t.Errorf("amdAPUName = %q, want %q", got, want)
	}
	if got := amdAPUName("Intel(R) Core(TM) i9-14900K"); got != "" {
		t.Errorf("non-Ryzen host must opt out, got %q", got)
	}
}

// TestApuProcessorNames — only unified-memory cards are renamed; a discrete
// card on the same host keeps its own identity.
func TestApuProcessorNames(t *testing.T) {
	gpus := []gpuInfo{
		{Name: "AMD Strix Halo \u00b7 Radeon 8060S Graphics (gfx1151)", Arch: "gfx1151", Unified: true},
		{Name: "Radeon RX 7900 XTX (gfx1100)", Arch: "gfx1100"},
	}
	out := apuProcessorNames(gpus, "AMD RYZEN AI MAX+ 395 w/ Radeon 8060S")
	if got, want := out[0].Name, "AMD Ryzen AI Max+ 395 w/ Radeon 8060S (gfx1151)"; got != want {
		t.Errorf("APU name = %q, want %q", got, want)
	}
	if got, want := out[1].Name, "Radeon RX 7900 XTX (gfx1100)"; got != want {
		t.Errorf("discrete name = %q, want %q", got, want)
	}
}

// TestParseNvidiaSmiCSV — a GB10-class unified SoC ("[N/A]" VRAM) reports the
// machine RAM snapped to capacity + its sm arch; a discrete card keeps its VRAM.
func TestParseNvidiaSmiCSV(t *testing.T) {
	out := []byte("NVIDIA GB10, [N/A], 12.1\n")
	gpus := parseNvidiaSmiCSV(out, 124610)
	if len(gpus) != 1 {
		t.Fatalf("want 1 GPU, got %d", len(gpus))
	}
	g := gpus[0]
	if g.Name != "NVIDIA GB10" || g.MemoryTotal != "131072 MiB" || !g.Unified || g.Arch != "sm_121" {
		t.Errorf("GB10 = %+v", g)
	}
	disc := parseNvidiaSmiCSV([]byte("NVIDIA GeForce RTX 4090, 24564 MiB, 8.9\n"), 124610)
	if d := disc[0]; d.MemoryTotal != "24564 MiB" || d.Unified || d.Arch != "sm_89" {
		t.Errorf("discrete = %+v", d)
	}
}
