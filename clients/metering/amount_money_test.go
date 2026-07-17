package metering

import (
	"math/big"
	"testing"

	"github.com/hanzoai/cloud/clients/money"
)

// Verifies amountMoney precedence: typed Amount wins (18-dp native, no floor),
// then micros, then cents, then zero.
func TestUsageAmountMoney(t *testing.T) {
	// Typed Amount at 18-dp: 0.00132 USD = 1320000000000000 atto (1e18=$1).
	atto, ok := new(big.Int).SetString("1320000000000000", 10) // 0.00132 USD = 1.32e15 atto
	if !ok {
		t.Fatal("parse atto")
	}
	typed := money.FromAtto(atto)
	u := Usage{User: "x", Amount: typed}
	got := u.amountMoney()
	if got.Cents() != 0 {
		t.Errorf("typed 0.00132 USD: cents=%d want 0 (sub-cent, not floored up)", got.Cents())
	}
	if got.Atto().Cmp(atto) != 0 {
		t.Errorf("typed 0.00132 USD: atto=%s want 1320000000000000 (exact, no floor)", got.Atto())
	}

	// Legacy micros: 2000 micros = 0.002 USD → reconstructed 18-dp exact.
	u2 := Usage{User: "x", AmountMicros: 2000}
	got2 := u2.amountMoney()
	want2 := new(big.Int).Mul(big.NewInt(2000), big.NewInt(1_000_000_000_000)) // 2000e12 atto
	if got2.Atto().Cmp(want2) != 0 {
		t.Errorf("micros 2000: atto=%s want %s", got2.Atto(), want2)
	}

	// Legacy cents: 50 cents.
	u3 := Usage{User: "x", AmountCents: 50}
	if u3.amountMoney().Cents() != 50 {
		t.Errorf("cents 50: got %d", u3.amountMoney().Cents())
	}

	// Zero.
	empty := Usage{User: "x"}
	if !empty.amountMoney().IsZero() {
		t.Error("empty usage should be zero")
	}

	// Typed wins over legacy fields when both set.
	u4 := Usage{User: "x", Amount: typed, AmountCents: 999, AmountMicros: 999}
	if u4.amountMoney().Atto().Cmp(atto) != 0 {
		t.Errorf("typed should win over legacy fields: got %s", u4.amountMoney().Atto())
	}
}
