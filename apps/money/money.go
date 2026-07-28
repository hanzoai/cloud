// Package money is the ONE exact money value for the Hanzo cloud finance stack: a USD
// balance carried at 18-decimal (EVM/ERC-20) precision, so an off-chain ledger amount and
// an on-chain uint256 credit balance are THE SAME INTEGER — no conversion or rounding at
// the boundary. Every per-token AI price is represented and billed EXACTLY; there is no
// cent-flooring and no fractional-cent skim, at any scale.
//
// The exact-number machinery (big.Int fixed-point, no float, no precision ceiling) is NOT
// reimplemented here — it lives ONCE in github.com/hanzoai/money + github.com/hanzoai/decimal,
// the shared money value for the whole stack. This package is the thin policy layer that
// pins the cloud's credit unit to 18-decimal USD and nothing else; it is the single place
// that decision lives.
//
// An Amount is IMMUTABLE — every operation returns a new value — and the zero value is a
// valid 0.
package money

import (
	"fmt"
	"math/big"
	"strings"

	"github.com/hanzoai/decimal"
	hz "github.com/hanzoai/money"
)

// Decimals is the fixed-point scale of the credit unit: 18 (the EVM/ERC-20 unit). The
// smallest representable amount is 10^-18 USD.
const Decimals = 18

// creditUSD is the cloud's ONE money unit: US dollars at 18-decimal precision, so the
// stored integer equals the on-chain uint256 credit balance byte-for-byte. Cents and
// dollars appear ONLY at the human/API edge (FromCents, ParseUSD, Cents) and convert
// to/from this unit exactly.
var creditUSD = hz.Currency{Code: "USD", Decimals: Decimals, Symbol: "$", Numeric: "840", Name: "US Dollar credit"}

// Amount is an exact USD credit value (18-decimal, big.Int-backed, immutable). It wraps the
// shared money.Amount, fixed to the credit unit.
type Amount struct{ a hz.Amount }

// Zero is the additive identity.
func Zero() Amount { return Amount{a: hz.Zero(creditUSD)} }

// FromCents converts integer cents (a human/legacy unit) to an exact credit Amount.
func FromCents(cents int64) Amount { return Amount{a: hz.New(decimal.New(cents, 2), creditUSD)} }

// FromAtto wraps a raw atto-USD magnitude — 18 decimals, the storage/on-chain form (the
// value an EVM uint256 credit balance holds). A nil magnitude is 0.
//
// It is named for the UNIT it takes, not for its Go type. As FromInt it read as "make an
// Amount from an integer", and money.Amount.Minor() also returns an integer — so
// FromInt(x.Minor()) type-checked, read fine, and fed CENTS to an 18-decimal constructor,
// understating every zen debit by 10^16 until v1.801.44. FromAtto(x.Cents()) cannot read
// fine. A name that states the unit refuses the bug the type system cannot see.
func FromAtto(units *big.Int) Amount {
	if units == nil {
		return Zero()
	}
	return Amount{a: hz.FromMinorBig(units, creditUSD)}
}

// FromDecimal wraps an exact decimal USD value in the credit unit — the typed counterpart
// of ParseUSD, with no string round-trip. The decimal IS the value; the credit unit's 18
// decimals are its storage scale, so nothing is rescaled and nothing is rounded here.
//
// This is the ONE way to carry a value priced as a shared money.Amount (whose Currency may
// declare a COARSER minor unit — money.USD declares 2) into the credit unit. Take the
// decimal, never Amount.Minor(): Minor() rescales the value to the CURRENCY's minor unit,
// so an 18-dp value tagged money.USD comes back as CENTS, and cents fed to an 18-dp
// constructor understate by 10^16.
func FromDecimal(d decimal.Decimal) Amount { return Amount{a: hz.New(d, creditUSD)} }

// ParseInt parses a signed 18-decimal integer string (the storage/on-chain form). An empty
// string is 0.
func ParseInt(s string) (Amount, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Zero(), nil
	}
	v, ok := new(big.Int).SetString(s, 10)
	if !ok {
		return Amount{}, fmt.Errorf("money: invalid integer %q", s)
	}
	return Amount{a: hz.FromMinorBig(v, creditUSD)}, nil
}

// ParseUSD parses a decimal USD string ("6.60", "-0.00132", "100") to an EXACT Amount — no
// float. Up to 18 fractional digits are honored; more is an error rather than a silent
// truncation (we never quietly drop money).
func ParseUSD(s string) (Amount, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Zero(), nil
	}
	d, err := decimal.Parse(s)
	if err != nil {
		return Amount{}, fmt.Errorf("money: invalid USD %q: %w", s, err)
	}
	if d.Scale() > Decimals {
		return Amount{}, fmt.Errorf("money: %q has more than %d fractional digits", s, Decimals)
	}
	return Amount{a: hz.New(d, creditUSD)}, nil
}

// TokenCost is the EXACT cost of n tokens at pricePerMillion USD/1M-tokens: n × price / 1e6,
// rounded half-away-from-zero at 10^-18 — so a bill is never floored to zero and never skims
// a fraction.
func TokenCost(tokens int, pricePerMillion Amount) Amount {
	if tokens <= 0 || pricePerMillion.IsZero() {
		return Zero()
	}
	cost := pricePerMillion.a.Decimal().
		Mul(decimal.New(int64(tokens), 0)).
		Quo(decimal.New(1_000_000, 0), Decimals)
	return Amount{a: hz.New(cost, creditUSD)}
}

// Add returns a + b. (The credit unit is fixed, so the shared add can never mismatch.)
func (a Amount) Add(b Amount) Amount { s, _ := a.a.Add(b.a); return Amount{a: s} }

// Sub returns a − b.
func (a Amount) Sub(b Amount) Amount { s, _ := a.a.Sub(b.a); return Amount{a: s} }

// Neg returns −a.
func (a Amount) Neg() Amount { return Amount{a: a.a.Neg()} }

// Cmp reports −1, 0, +1 as a <, ==, > b.
func (a Amount) Cmp(b Amount) int { return a.a.Cmp(b.a) }

// Sign reports −1, 0, +1 as a <, ==, > 0.
func (a Amount) Sign() int { return a.a.Sign() }

// IsZero reports whether a == 0.
func (a Amount) IsZero() bool { return a.a.IsZero() }

// IsNeg reports whether a < 0.
func (a Amount) IsNeg() bool { return a.a.Sign() < 0 }

// Atto returns a fresh big.Int of the atto-USD magnitude — 18 decimals, the on-chain
// uint256 value. The unit is in the name: this is NOT interchangeable with
// money.Amount.Minor(), which renders the CURRENCY's minor unit (money.USD declares 2, so
// it returns cents). Both are "an integer"; they differ by 10^16.
func (a Amount) Atto() *big.Int { return a.a.Minor() }

// AttoString is the canonical STORAGE form: the signed atto-USD integer as a decimal
// string (exact, on-chain-identical, sortable at fixed width by the caller).
func (a Amount) AttoString() string { return a.a.MinorString() }

// Cents rounds the value to whole cents (half-away-from-zero) — a human/legacy display unit
// ONLY; never use it inside money math (it is lossy by construction).
func (a Amount) Cents() int64 { return a.a.Decimal().Round(2).Coef().Int64() }

// String renders the value as a trimmed decimal USD string ("6.6", "0.00132", "-0.5", "0")
// — the human/JSON form. Exact: derived from the integer coefficient, never a float.
func (a Amount) String() string { return a.a.String() }

// MarshalJSON emits the exact decimal USD string ("0.00132") — a STRING, never a JSON
// number, so no consumer can reintroduce a float rounding error.
func (a Amount) MarshalJSON() ([]byte, error) { return []byte(`"` + a.a.String() + `"`), nil }

// UnmarshalJSON accepts either a quoted decimal USD string or a bare decimal number (parsed
// exactly, without float).
func (a *Amount) UnmarshalJSON(b []byte) error {
	s := strings.Trim(strings.TrimSpace(string(b)), `"`)
	if s == "null" || s == "" {
		*a = Zero()
		return nil
	}
	parsed, err := ParseUSD(s)
	if err != nil {
		return err
	}
	*a = parsed
	return nil
}
