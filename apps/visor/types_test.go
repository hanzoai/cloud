package visor

import "testing"

func TestParseSizeSlug(t *testing.T) {
	cases := []struct {
		slug  string
		vcpu  int
		memGB int
	}{
		{"s-4vcpu-8gb", 4, 8},
		{"g-8vcpu-32gb", 8, 32},
		{"so-2vcpu-16gb", 2, 16},
		{"S-4VCPU-8GB", 4, 8},        // case-insensitive
		{"gpu-h100x8-640gb", 0, 640}, // no vcpu token, gb present
		{"plain-name", 0, 0},
		{"", 0, 0},
	}
	for _, c := range cases {
		vcpu, memGB := parseSizeSlug(c.slug)
		if vcpu != c.vcpu || memGB != c.memGB {
			t.Errorf("parseSizeSlug(%q) = (%d,%d), want (%d,%d)", c.slug, vcpu, memGB, c.vcpu, c.memGB)
		}
	}
}

func TestNormalizeMem(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{"8gb", "8 GB"},
		{"8 GB", "8 GB"},
		{"8gib", "8 GB"},
		{"32GB", "32 GB"},
		{"8192mb", "8 GB"}, // MB rounds to GB
		{"8192 MB", "8 GB"},
		{"8192", "8 GB"}, // bare >= 1024 treated as MB
		{"16384", "16 GB"},
		{"7800mb", "8 GB"}, // (7800+512)/1024 = 8
		{"512", "512 GB"},  // bare < 1024 treated as GB
		{"16", "16 GB"},
		{"", ""},      // honest omission
		{"many", ""},  // unparseable
		{"8.5gb", ""}, // non-integer unparseable
		{"0", ""},     // zero is not a real size
		{"gb", ""},    // unit without a number
	}
	for _, c := range cases {
		if got := normalizeMem(c.raw); got != c.want {
			t.Errorf("normalizeMem(%q) = %q, want %q", c.raw, got, c.want)
		}
	}
}

func TestToMachineViewMemAndVcpu(t *testing.T) {
	// DO CPU droplet: no explicit CpuSize/MemSize, both recovered from the slug.
	v := toMachineView(visorMachine{Name: "n1", Size: "s-4vcpu-8gb"})
	if v.Vcpu == nil || *v.Vcpu != 4 {
		t.Errorf("slug vcpu: got %v, want 4", v.Vcpu)
	}
	if v.Mem != "8 GB" {
		t.Errorf("slug mem: got %q, want %q", v.Mem, "8 GB")
	}

	// Explicit integer CpuSize wins over the slug's vcpu.
	v = toMachineView(visorMachine{Name: "n2", CpuSize: "16", Size: "s-4vcpu-8gb"})
	if v.Vcpu == nil || *v.Vcpu != 16 {
		t.Errorf("explicit cpuSize: got %v, want 16", v.Vcpu)
	}

	// Explicit MemSize wins over the slug's gb figure.
	v = toMachineView(visorMachine{Name: "n3", MemSize: "16gb", Size: "s-4vcpu-8gb"})
	if v.Mem != "16 GB" {
		t.Errorf("explicit memSize: got %q, want %q", v.Mem, "16 GB")
	}
	if v.Vcpu == nil || *v.Vcpu != 4 {
		t.Errorf("slug vcpu fallback with explicit mem: got %v, want 4", v.Vcpu)
	}

	// GPU slug: its gb is VRAM (gpu-h100x8-640gb -> 640 = VRAM), NEVER system RAM.
	// With no explicit MemSize, mem stays empty rather than surfacing the VRAM figure.
	v = toMachineView(visorMachine{Name: "g1", Size: "gpu-h100x8-640gb"})
	if v.Mem != "" {
		t.Errorf("gpu slug mem: got %q, want empty (640gb is VRAM, not system RAM)", v.Mem)
	}
	if v.GPU != "H100" {
		t.Errorf("gpu slug model: got %q, want H100", v.GPU)
	}

	// A GPU node with a real MemSize still reports its true system RAM.
	v = toMachineView(visorMachine{Name: "g2", Size: "gpu-h100x8-640gb", MemSize: "1920gb"})
	if v.Mem != "1920 GB" {
		t.Errorf("gpu slug + real memSize: got %q, want %q", v.Mem, "1920 GB")
	}

	// Neither a numeric CpuSize nor a slug figure -> honest omission, no fabrication.
	v = toMachineView(visorMachine{Name: "n4", Type: "custom-node"})
	if v.Vcpu != nil {
		t.Errorf("no vcpu source: got %v, want nil", v.Vcpu)
	}
	if v.Mem != "" {
		t.Errorf("no mem source: got %q, want empty", v.Mem)
	}
}
