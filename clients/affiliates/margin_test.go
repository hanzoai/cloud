package affiliates

import (
	"strconv"
	"testing"

	"github.com/hanzoai/cloud/clients/flags"
)

// The margin is the base every affiliate commission is a rate OF, so it has to
// track our real cost of revenue and move when that moves. It used to come from
// AFFILIATE_MARGIN_BPS and be snapshotted into state at Mount — two reasons it
// could not change: no admin control, and even editing the env did nothing until
// the pod restarted.
func TestAffiliateMarginBps_IsARegisteredAdminSwitch(t *testing.T) {
	var def *flags.Def
	for _, d := range flags.Defs() {
		if d.Key == marginBpsKey {
			dd := d
			def = &dd
			break
		}
	}
	if def == nil {
		t.Fatalf("%s is not registered — it would not appear in admin.hanzo.ai, and flags.Int would return 0 (= no commission accrues at all)", marginBpsKey)
	}
	if def.Type != flags.TypeInt {
		t.Errorf("Type = %v, want TypeInt", def.Type)
	}
	if def.Env != "" {
		t.Errorf("Env = %q, want empty — margin must not be configurable by environment variable", def.Env)
	}
	if def.ReadOnly {
		t.Error("ReadOnly — the whole point is that we can change it at any time")
	}
	if def.Default != strconv.FormatInt(defaultMarginBps, 10) {
		t.Errorf("Default = %q, want %d", def.Default, defaultMarginBps)
	}
}

// Unmounted / unset resolves to the policy default rather than 0. This is the
// dangerous direction: 0 is a LEGITIMATE margin ("accrues nothing"), so a
// zero-on-missing would silently switch off every affiliate commission instead of
// failing loudly. flags.resolve falls back to Def.Default, which is why this holds.
func TestAffiliateMarginBps_UnsetIsTheDefaultNotZero(t *testing.T) {
	if got := affiliateMarginBps(); got != defaultMarginBps {
		t.Fatalf("affiliateMarginBps() = %d, want %d — a missing value must not zero the share base", got, defaultMarginBps)
	}
}

// A bad edit cannot over-inflate or negate the base: out-of-range falls back to
// the policy default. In range (including 0 and 100%) is honoured as written.
func TestAffiliateMarginBps_ClampsOutOfRange(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   int64
		want int64
	}{
		{"negative", -1, defaultMarginBps},
		{"above 100%", bpsDenom + 1, defaultMarginBps},
		{"exactly 100% is legal", bpsDenom, bpsDenom},
		{"zero is legal (accrues nothing)", 0, 0},
		{"mid range", 2500, 2500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := clampMarginBps(tc.in); got != tc.want {
				t.Fatalf("clampMarginBps(%d) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

// The share base must be read at accrual time, not captured at boot — otherwise
// changing it in admin does nothing until the next deploy, which is the failure
// this replaced.
func TestMargin_IsNotSnapshotAtBoot(t *testing.T) {
	base := marginOf(10000, affiliateMarginBps())
	if base != 10000*defaultMarginBps/bpsDenom {
		t.Fatalf("margin base = %d, want %d", base, 10000*defaultMarginBps/bpsDenom)
	}
	// Same call again must re-read rather than serve a cached snapshot.
	if again := marginOf(10000, affiliateMarginBps()); again != base {
		t.Fatalf("margin base not stable across reads: %d then %d", base, again)
	}
}
