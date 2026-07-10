package functions

import "testing"

// gbSecondsCents is the whole point of usage-native compute billing: the
// integer-cent debit must equal round(GB-seconds × rate), GB=1024 MB, with no
// float drift and a hard 0 for any non-billable input.
func TestGBSecondsCents(t *testing.T) {
	for _, tc := range []struct {
		name              string
		durMs, memMB, fee int64
		want              int64
	}{
		{"one GB one second at 100c", 1000, 1024, 100, 100}, // 1.0 GB-s × 100c
		{"one GB one second at 1c", 1000, 1024, 1, 1},       // 1.0 GB-s × 1c
		{"half GB two seconds at 50c", 2000, 512, 50, 50},   // 1.0 GB-s × 50c
		{"256Mi 100ms at 100c rounds up", 100, 256, 100, 3}, // 0.025 GB-s × 100c = 2.5 → 3
		{"512Mi 500ms at 200c", 500, 512, 200, 50},          // 0.25 GB-s × 200c = 50
		{"2Gi 3s at 10c", 3000, 2048, 10, 60},               // 6.0 GB-s × 10c
		{"zero duration", 0, 1024, 100, 0},
		{"zero memory", 1000, 0, 100, 0},
		{"free rate", 1000, 1024, 0, 0},
		{"negative rate ignored", 1000, 1024, -5, 0},
		{"tiny charge rounds to zero", 1, 1, 1, 0}, // 1e-9 GB-s × 1c → 0
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := gbSecondsCents(tc.durMs, tc.memMB, tc.fee); got != tc.want {
				t.Fatalf("gbSecondsCents(%d,%d,%d) = %d, want %d", tc.durMs, tc.memMB, tc.fee, got, tc.want)
			}
		})
	}
}

// memLimitMB must turn a k8s memory quantity into whole MB (GB=1024) and degrade
// a missing/garbage value to the 256 default rather than 0 or a wild number.
func TestMemLimitMB(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want int64
	}{
		{"256Mi", 256},
		{"512Mi", 512},
		{"128M", 128},
		{"1Gi", 1024},
		{"2Gi", 2048},
		{"1G", 1024},
		{" 512Mi ", 512}, // trimmed
		{"", defaultMemMB},
		{"garbage", defaultMemMB},
		{"512", defaultMemMB},    // bare number (ambiguous units) → default
		{"0Mi", defaultMemMB},    // non-positive → default
		{"1024Ki", defaultMemMB}, // unsupported unit → default
	} {
		t.Run(tc.in, func(t *testing.T) {
			if got := memLimitMB(tc.in); got != tc.want {
				t.Fatalf("memLimitMB(%q) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}
