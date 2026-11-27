package sandbox

import "testing"

// A sandbox is a microVM on somebody's node and it leased FREE: nothing gated it
// and nothing recorded it. Tabs now leases one from a button, so the meter matters
// — but shipping the meter must not also ship a price. The first anyone hears of
// a charge cannot be a 402 on a button that worked yesterday.
func TestALeaseIsFreeUntilSomebodyPricesIt(t *testing.T) {
	if got := ResourceFee("exec"); got != 0 {
		t.Fatalf("an unconfigured exec costs %d cents; every class has been free and shipping the meter must not change that", got)
	}
	for _, class := range []string{"dev", "desktop", ""} {
		if got := ResourceFee(class); got != 0 {
			t.Errorf("ResourceFee(%q) = %d, want 0", class, got)
		}
	}
}

// Per class, because a desktop is not a throwaway exec and one number for both is
// a price nobody chose.
func TestPricePerClassBeatsTheFallback(t *testing.T) {
	t.Setenv("SANDBOX_FEE_CENTS", "10")
	t.Setenv("SANDBOX_FEE_CENTS_DESKTOP", "250")

	if got := ResourceFee("desktop"); got != 250 {
		t.Errorf("desktop = %d, want its own 250 rather than the fallback", got)
	}
	if got := ResourceFee("exec"); got != 10 {
		t.Errorf("exec = %d, want the fallback 10", got)
	}
}

// A typo must not price a class in either direction — not free by accident, and
// not charged by accident.
func TestAnUnreadableValueIsFreeNotGuessed(t *testing.T) {
	for _, bad := range []string{"-1", "free", "1.50", "$2"} {
		t.Setenv("SANDBOX_FEE_CENTS_EXEC", bad)
		if got := ResourceFee("exec"); got != 0 {
			t.Errorf("SANDBOX_FEE_CENTS_EXEC=%q gave %d, want 0", bad, got)
		}
	}
}
