package affiliates

import (
	"strconv"
	"testing"

	"github.com/hanzoai/cloud/clients/flags"
)

// L1 was already per-affiliate (Affiliate.RateBps, negotiated and stored on the row).
// L2 and L3 were compile-time constants, so the upline schedule was the one part of
// the affiliate economy that still needed a redeploy to change — the same shape the
// margin was moved off. These prove they are cockpit switches now.
func TestUplineRates_AreRegisteredAdminSwitches(t *testing.T) {
	for _, tc := range []struct {
		key  string
		dflt int64
	}{
		{l2RateKey, defaultL2RateBps},
		{l3RateKey, defaultL3RateBps},
	} {
		t.Run(tc.key, func(t *testing.T) {
			var def *flags.Def
			for _, d := range flags.Defs() {
				if d.Key == tc.key {
					dd := d
					def = &dd
					break
				}
			}
			if def == nil {
				t.Fatalf("%s is not registered — it would not appear in admin.hanzo.ai, and flags.Int would return 0, silently zeroing that upline level", tc.key)
			}
			if def.Type != flags.TypeInt {
				t.Errorf("Type = %v, want TypeInt", def.Type)
			}
			if def.Env != "" {
				t.Errorf("Env = %q, want empty — the schedule must not be settable by environment variable", def.Env)
			}
			if def.ReadOnly {
				t.Error("ReadOnly — the point is that we can change the schedule at any time")
			}
			if def.Default != strconv.FormatInt(tc.dflt, 10) {
				t.Errorf("Default = %q, want %d", def.Default, tc.dflt)
			}
		})
	}
}

// Unset resolves to the policy defaults, not 0. Same dangerous direction as the
// margin: 0 is a legitimate rate ("this level accrues nothing"), so zero-on-missing
// would silently switch off the whole upline instead of failing loudly.
func TestUplineRates_UnsetAreTheDefaults(t *testing.T) {
	l2, l3 := uplineRates()
	if l2 != defaultL2RateBps || l3 != defaultL3RateBps {
		t.Fatalf("uplineRates() = (%d,%d), want (%d,%d) — a missing value must not zero an upline level", l2, l3, defaultL2RateBps, defaultL3RateBps)
	}
}

// The pair is clamped TOGETHER, because the invariant that binds them is a property
// of the sum. Half-applying an edit would leave a schedule nobody chose.
func TestClampUplineRates_FallsBackTogether(t *testing.T) {
	for _, tc := range []struct {
		name           string
		l2, l3         int64
		wantL2, wantL3 int64
	}{
		{"defaults pass through", 500, 200, 500, 200},
		{"zero is legal at both levels", 0, 0, 0, 0},
		{"negative L2 refuses the PAIR", -1, 200, defaultL2RateBps, defaultL3RateBps},
		{"negative L3 refuses the PAIR", 500, -1, defaultL2RateBps, defaultL3RateBps},
		{"sum over 100% refuses the pair", 9000, 2000, defaultL2RateBps, defaultL3RateBps},
		{"sum exactly 100% is legal (leaves L1 at 0)", 9000, 1000, 9000, 1000},
		{"raised but still within budget", 1500, 900, 1500, 900},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l2, l3 := clampUplineRates(tc.l2, tc.l3)
			if l2 != tc.wantL2 || l3 != tc.wantL3 {
				t.Fatalf("clampUplineRates(%d,%d) = (%d,%d), want (%d,%d)", tc.l2, tc.l3, l2, l3, tc.wantL2, tc.wantL3)
			}
		})
	}
}

// THE money invariant, and the reason maxL1RateBps had to stop being a constant: the
// whole schedule must fit inside the margin, so L1's cap is exactly what L2+L3 leave.
// Held against every pair the clamp admits — including the ones that fall back — so a
// cockpit edit can never make the platform pay out more than it earned.
func TestMaxL1Rate_KeepsTheWholeScheduleInsideTheMargin(t *testing.T) {
	for _, p := range [][2]int64{
		{500, 200}, {0, 0}, {9000, 1000}, {1500, 900}, {-1, 200}, {9000, 2000},
	} {
		l2, l3 := clampUplineRates(p[0], p[1])
		cap := bpsDenom - l2 - l3
		if cap < 0 {
			t.Fatalf("pair (%d,%d) -> (%d,%d) yields a NEGATIVE L1 cap %d — no rate could be set", p[0], p[1], l2, l3, cap)
		}
		if cap+l2+l3 != bpsDenom {
			t.Fatalf("schedule does not close: L1cap(%d)+L2(%d)+L3(%d) = %d, want %d", cap, l2, l3, cap+l2+l3, bpsDenom)
		}
	}
	// With nothing set, the live cap is the historical 9300 — this change must not
	// move the default schedule, only make it editable.
	if got := maxL1RateBps(); got != bpsDenom-defaultL2RateBps-defaultL3RateBps {
		t.Fatalf("maxL1RateBps() = %d, want %d (the unchanged default cap)", got, bpsDenom-defaultL2RateBps-defaultL3RateBps)
	}
}

// levelRateBps must read the switches per call, not a boot snapshot, and must pay the
// affiliate's OWN negotiated rate at L1 rather than any platform value.
func TestLevelRateBps_ReadsLiveAndHonoursTheNegotiatedL1(t *testing.T) {
	a := Affiliate{RateBps: 3333}
	if got := levelRateBps(1, a); got != 3333 {
		t.Fatalf("L1 = %d, want the affiliate's own 3333", got)
	}
	if got := levelRateBps(2, a); got != defaultL2RateBps {
		t.Fatalf("L2 = %d, want %d", got, defaultL2RateBps)
	}
	if got := levelRateBps(3, a); got != defaultL3RateBps {
		t.Fatalf("L3 = %d, want %d", got, defaultL3RateBps)
	}
	// Beyond the depth cap nothing accrues — the chain terminates rather than
	// falling through to some default rate.
	for _, lvl := range []int{0, maxDepth + 1, 99} {
		if got := levelRateBps(lvl, a); got != 0 {
			t.Fatalf("level %d = %d, want 0", lvl, got)
		}
	}
}
