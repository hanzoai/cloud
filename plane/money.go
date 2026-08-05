package plane

import (
	"fmt"
	"math/big"

	"github.com/hanzoai/money"
)

// The ONE conversion between an amount in memory and an amount on the wire.
// Both ends call these, so an amount cannot be packed by one rule and read by
// another — the failure that turns an exact ledger into a reconciliation
// problem, and the reason this is a function rather than a convention.

// Amount renders an exact amount for the wire.
func Amount(a money.Amount) Money {
	return Money{Decimal: a.String(), Currency: a.Currency().Code}
}

// Parse reads one back.
//
// A malformed amount is an ERROR, never a zero. A gate that read an
// unparseable charge as "nothing to authorize" would let the work through
// free, and a statement that read one as zero would show a customer a balance
// they cannot reconcile — both are silent, and both are worse than refusing.
func (m Money) Parse() (money.Amount, error) {
	a, err := money.ParseAmount(m.Decimal, m.Currency)
	if err != nil {
		return money.Amount{}, fmt.Errorf("amount %q %q: %w", m.Decimal, m.Currency, err)
	}
	return a, nil
}

// Minor reads the amount as a count of its currency's smallest unit — cents for
// USD — for the callers that still hold money in an int64.
//
// It refuses anything it cannot answer EXACTLY, in both directions:
//
//   - Too large for an int64. Silently wrapping a balance is how a funded
//     account reads as overdrawn.
//   - Finer than the unit. Minor() rescales to the currency's decimals, so a
//     sub-cent amount would come back rounded — 0.005 USD as one cent — and the
//     platform books credits at eighteen decimals, where per-token charges are
//     routinely finer than a cent. A rounded debit is a ledger that drifts by an
//     amount nobody can find, which is the whole reason the wire carries an
//     exact decimal instead of an integer.
//
// A caller that genuinely wants a rounded figure — a display, a summary — should
// round explicitly, where the choice is visible.
func (m Money) Minor() (int64, error) {
	a, err := m.Parse()
	if err != nil {
		return 0, err
	}
	u := a.Minor()
	if !u.IsInt64() {
		return 0, fmt.Errorf("amount %s %s exceeds int64 minor units", m.Decimal, m.Currency)
	}
	if back := money.FromMinorBig(u, a.Currency()); a.Cmp(back) != 0 {
		return 0, fmt.Errorf("amount %s %s is finer than its minor unit; round explicitly", m.Decimal, m.Currency)
	}
	return u.Int64(), nil
}

// FloorMinor reads the amount as a count of its currency's smallest unit,
// rounding toward NEGATIVE INFINITY — the explicit rounding Minor() tells its
// caller to make, made once here so every caller makes the same one.
//
// DOWN, never up, is the only safe direction for money a gate spends against.
// money.Amount.Minor() rescales, and hanzoai/decimal's Rescale rounds
// HALF-AWAY-FROM-ZERO (decimal.go:145) — it does not truncate. So a balance of
// 4.995 USD came back as 500 cents and passed a 500-cent charge the account
// could not cover; the debit that follows is exact, so the difference lands as
// a negative balance nobody authorized. Rounding down can only ever refuse
// slightly early, which is what a fail-closed gate should do.
//
// Use it for a BALANCE — a figure something is compared against. Never for a
// DEBIT: an amount being taken must be exact, and Minor() refusing is correct
// there.
func (m Money) FloorMinor() (int64, error) {
	a, err := m.Parse()
	if err != nil {
		return 0, err
	}
	unit := a.Currency().Decimals
	d := a.Decimal()
	coef, scale := d.Coef(), d.Scale()
	if scale <= unit {
		coef.Mul(coef, pow10(unit-scale)) // widening is exact
	} else {
		// big.Int.Div is Euclidean: with a positive divisor it IS floor, so a
		// negative balance floors further from zero rather than drifting back
		// toward it the way truncation would.
		coef.Div(coef, pow10(scale-unit))
	}
	if !coef.IsInt64() {
		return 0, fmt.Errorf("amount %s %s exceeds int64 minor units", m.Decimal, m.Currency)
	}
	return coef.Int64(), nil
}

func pow10(n int32) *big.Int {
	return new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(n)), nil)
}
