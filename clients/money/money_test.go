package money

import "testing"

func TestFromCentsAndBack(t *testing.T) {
	if got := FromCents(1).AttoString(); got != "10000000000000000" { // 1e16
		t.Fatalf("1 cent = %s atto, want 1e16", got)
	}
	if got := FromCents(1).Cents(); got != 1 {
		t.Fatalf("round-trip cents = %d, want 1", got)
	}
	// $100k prefund migrates cents→atto exactly: 10,000,000 cents × 1e16 = 1e23.
	if got := FromCents(10_000_000).AttoString(); got != "100000000000000000000000" {
		t.Fatalf("$100k = %s atto", got)
	}
	if got := FromCents(10_000_000).String(); got != "100000" {
		t.Fatalf("$100k = %q USD", got)
	}
}

func TestParseUSDExact(t *testing.T) {
	cases := map[string]string{
		"6.60":    "6600000000000000000", // 6.6e18
		"1.80":    "1800000000000000000",
		"0.30":    "300000000000000000",
		"0.00132": "1320000000000000",
		"100":     "100000000000000000000", // 100 × 1e18
		"-0.5":    "-500000000000000000",
	}
	for in, wantAtto := range cases {
		a, err := ParseUSD(in)
		if err != nil {
			t.Fatalf("ParseUSD(%q): %v", in, err)
		}
		if a.AttoString() != wantAtto {
			t.Errorf("ParseUSD(%q) = %s atto, want %s", in, a.AttoString(), wantAtto)
		}
		// round-trips through the human string form
		if in == "6.60" && a.String() != "6.6" {
			t.Errorf("String(6.60) = %q, want 6.6", a.String())
		}
	}
	if _, err := ParseUSD("0.0000000000000000001"); err == nil { // 19 fractional digits
		t.Errorf("expected error for >18 fractional digits (never silently truncate money)")
	}
}

func TestTokenCostNeverFloorsToZero(t *testing.T) {
	price := mustUSD(t, "6.60") // zen3-omni output $/1M

	// The exact leak case: a 200-token completion. Cents-flooring dropped this to 0.
	got := TokenCost(200, price)
	if got.IsZero() {
		t.Fatal("200-token cost floored to zero — the leak is back")
	}
	if got.AttoString() != "1320000000000000" { // 200 × 6.6e18 / 1e6 = 1.32e15
		t.Errorf("TokenCost(200,$6.60) = %s atto, want 1.32e15", got.AttoString())
	}
	if got.String() != "0.00132" {
		t.Errorf("TokenCost(200,$6.60) = %q USD, want 0.00132", got.String())
	}

	// Even a single cheap token bills a positive, exact amount.
	one := TokenCost(1, mustUSD(t, "0.30"))
	if one.IsZero() || one.AttoString() != "300000000000" { // 1 × 0.3e18 / 1e6 = 3e11
		t.Errorf("TokenCost(1,$0.30) = %s atto, want 3e11 (never zero)", one.AttoString())
	}
}

func TestArithmeticAndImmutability(t *testing.T) {
	a := FromCents(100) // $1.00
	b := TokenCost(200, mustUSD(t, "6.60"))
	sum := a.Add(b)
	// a must be unchanged (immutable value object)
	if a.String() != "1" {
		t.Fatalf("Add mutated receiver: a = %q", a.String())
	}
	if sum.String() != "1.00132" {
		t.Fatalf("1 + 0.00132 = %q, want 1.00132", sum.String())
	}
	rem := a.Sub(b)
	if rem.Cmp(a) >= 0 {
		t.Fatalf("a - b should be < a")
	}
	if a.Sub(a).Sign() != 0 || !a.Sub(a).IsZero() {
		t.Fatalf("a - a should be zero")
	}
	if FromCents(-5).Sign() != -1 || !FromCents(-5).IsNeg() {
		t.Fatalf("negative sign wrong")
	}
}

func mustUSD(t *testing.T, s string) Amount {
	t.Helper()
	a, err := ParseUSD(s)
	if err != nil {
		t.Fatalf("ParseUSD(%q): %v", s, err)
	}
	return a
}
