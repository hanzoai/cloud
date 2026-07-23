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

// TestPickAmdMemUnified — an APU whose VRAM is a token carve-out reports the GTT
// unified pool (evo: 1 GiB VRAM vs ~118 GiB GTT); a discrete card keeps its VRAM.
func TestPickAmdMemUnified(t *testing.T) {
	if miB, unified := pickAmdMem(1024, 120832); !unified || miB != 120832 {
		t.Errorf("APU: got (%d, %v), want (120832, true)", miB, unified)
	}
	if miB, unified := pickAmdMem(24576, 8192); unified || miB != 24576 {
		t.Errorf("discrete: got (%d, %v), want (24576, false)", miB, unified)
	}
}
