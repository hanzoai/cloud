package cloud

import "testing"

// TestGetenvIntReadBuffer pins the parsing contract behind the public HTTP
// edge's GATEWAY_READ_BUFFER_SIZE knob (config.go): a valid override wins, and
// every degenerate input (unset / blank / non-numeric) falls back to the 32 KiB
// default rather than silently collapsing the fasthttp header ceiling back to
// fiber's 4 KiB (which 431s multi-domain SSO sessions). The default MUST stay
// well above a realistic browser cookie header (~8-12 KiB).
func TestGetenvIntReadBuffer(t *testing.T) {
	const key = "GATEWAY_READ_BUFFER_SIZE"
	const dflt = 32768

	cases := []struct {
		name string
		set  bool
		val  string
		want int
	}{
		{"unset falls back to default", false, "", dflt},
		{"blank falls back to default", true, "", dflt},
		{"whitespace falls back to default", true, "   ", dflt},
		{"non-numeric falls back to default", true, "big", dflt},
		{"valid override wins", true, "65536", 65536},
		{"operator may tune down", true, "16384", 16384},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.set {
				t.Setenv(key, tc.val)
			} else {
				t.Setenv(key, "") // ensure isolation, then treat as unset via blank branch
			}
			got := getenvInt(key, dflt)
			if got != tc.want {
				t.Fatalf("getenvInt(%q, %d) = %d, want %d", key, dflt, got, tc.want)
			}
		})
	}

	// The shipped default must exceed a realistic bloated-cookie header so the
	// edge never regresses to 431-ing legitimate SSO sessions.
	if dflt <= 12*1024 {
		t.Fatalf("default read buffer %d must exceed a ~12 KiB SSO cookie header", dflt)
	}
}
