package platform

import "testing"

// buildMinutesCents is the usage-native build meter's arithmetic: the debit must
// equal round(wall-clock minutes × rate) with integer-exact math, and 0 for any
// non-billable span or rate.
func TestBuildMinutesCents(t *testing.T) {
	for _, tc := range []struct {
		name            string
		start, end, fee int64
		want            int64
	}{
		{"exactly one minute at 100c", 0, 60, 100, 100},     // 1.0 min × 100c
		{"one and a half minutes at 100c", 0, 90, 100, 150}, // 1.5 min × 100c
		{"half minute at 100c", 0, 30, 100, 50},             // 0.5 min × 100c
		{"five minutes at 20c", 100, 400, 20, 100},          // 5.0 min × 20c
		{"one second at 60c", 0, 1, 60, 1},                  // 1/60 min × 60c = 1
		{"one second at 100c rounds", 0, 1, 100, 2},         // 1.6667c → 2
		{"instant build", 0, 0, 100, 0},
		{"clock skew (end before start)", 400, 100, 100, 0},
		{"free rate", 0, 600, 0, 0},
		{"negative rate ignored", 0, 600, -5, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := buildMinutesCents(tc.start, tc.end, tc.fee); got != tc.want {
				t.Fatalf("buildMinutesCents(%d,%d,%d) = %d, want %d", tc.start, tc.end, tc.fee, got, tc.want)
			}
		})
	}
}
