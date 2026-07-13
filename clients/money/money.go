// Package money is the ONE exact USD money type for the Hanzo finance stack. A value
// is a signed integer count of ATTO-USD (1e-18 USD) held in a big.Int — the same
// 18-decimal fixed-point unit an EVM/ERC-20 balance uses, so an off-chain Amount and
// an on-chain uint256 credit balance are THE SAME NUMBER with no conversion or rounding
// at the boundary. No float64 ever touches a balance, and every per-token AI price is
// represented and billed EXACTLY — there is no cent-flooring and no fractional-cent
// skim, at any scale.
//
// An Amount is IMMUTABLE: every operation returns a new value and the wrapped big.Int
// is never mutated after construction, so an Amount is a safe value object to copy,
// compare, and share. The zero value is a valid 0.
//
// UNIT CHOICE. Atto-USD (18 decimals) is exact for any decimal price with up to 18
// fractional digits — vastly finer than any real bill — and matches the native chain,
// so credits can settle on-chain later byte-for-byte. Cents/dollars appear ONLY at the
// human/API edge (FromCents, ParseUSD, USD) and are converted to/from atto exactly.
package money

import (
	"fmt"
	"math/big"
	"strings"
)

// Decimals is the fixed-point scale: 18 atto-USD == 1 USD. The EVM/ERC-20 unit.
const Decimals = 18

// scale = 10^18 (atto per USD); centScale = 10^16 (atto per cent). Computed once.
var (
	scale     = pow10(Decimals)
	centScale = pow10(Decimals - 2)
	million   = big.NewInt(1_000_000)
	two       = big.NewInt(2)
)

func pow10(n int) *big.Int {
	return new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(n)), nil)
}

// Amount is an exact USD value in atto-USD (1e-18), immutable and big.Int-backed.
type Amount struct {
	atto *big.Int // nil == 0; never mutated after construction
}

// Zero is the additive identity.
func Zero() Amount { return Amount{} }

// int returns a non-nil, non-aliased big.Int of the atto value (safe to mutate).
func (a Amount) int() *big.Int {
	if a.atto == nil {
		return new(big.Int)
	}
	return new(big.Int).Set(a.atto)
}

// FromAtto wraps a raw atto-USD magnitude (copied — the caller's big.Int is not retained).
func FromAtto(atto *big.Int) Amount {
	if atto == nil {
		return Amount{}
	}
	return Amount{atto: new(big.Int).Set(atto)}
}

// FromCents converts integer cents (a human/legacy unit) to atto exactly: cents × 1e16.
func FromCents(cents int64) Amount {
	v := big.NewInt(cents)
	return Amount{atto: v.Mul(v, centScale)}
}

// ParseAtto parses a signed integer atto-USD string (the storage/on-chain form).
func ParseAtto(s string) (Amount, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Amount{}, nil
	}
	v, ok := new(big.Int).SetString(s, 10)
	if !ok {
		return Amount{}, fmt.Errorf("money: invalid atto %q", s)
	}
	return Amount{atto: v}, nil
}

// ParseUSD parses a decimal USD string ("6.60", "-0.00132", "100") to an EXACT atto
// Amount — no float. Up to 18 fractional digits are honored; more is an error rather
// than a silent truncation (we never quietly drop money).
func ParseUSD(s string) (Amount, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Amount{}, nil
	}
	neg := false
	switch s[0] {
	case '+':
		s = s[1:]
	case '-':
		neg, s = true, s[1:]
	}
	intPart, fracPart := s, ""
	if i := strings.IndexByte(s, '.'); i >= 0 {
		intPart, fracPart = s[:i], s[i+1:]
	}
	if intPart == "" {
		intPart = "0"
	}
	if len(fracPart) > Decimals {
		return Amount{}, fmt.Errorf("money: %q has more than %d fractional digits", s, Decimals)
	}
	digits := intPart + fracPart
	v, ok := new(big.Int).SetString(digits, 10)
	if !ok {
		return Amount{}, fmt.Errorf("money: invalid USD %q", s)
	}
	// shift the fractional part up to full atto scale
	v.Mul(v, pow10(Decimals-len(fracPart)))
	if neg {
		v.Neg(v)
	}
	return Amount{atto: v}, nil
}

// TokenCost is the EXACT cost of n tokens at pricePerMillion USD/1M-tokens:
// n × price / 1e6, computed entirely in atto (big.Int) with round-half-up on the
// sub-atto remainder — so a bill is never floored to zero and never skims a fraction.
func TokenCost(tokens int, pricePerMillion Amount) Amount {
	if tokens <= 0 || pricePerMillion.IsZero() {
		return Amount{}
	}
	num := pricePerMillion.int()
	num.Mul(num, big.NewInt(int64(tokens))) // atto × tokens
	q, r := new(big.Int).QuoRem(num, million, new(big.Int))
	// round half up on the /1e6 remainder (r is non-negative; price/tokens are ≥ 0 here)
	if r.Sign() != 0 {
		if new(big.Int).Mul(r, two).Cmp(million) >= 0 {
			q.Add(q, big.NewInt(1))
		}
	}
	return Amount{atto: q}
}

// Add returns a + b.
func (a Amount) Add(b Amount) Amount { return Amount{atto: a.int().Add(a.int(), b.int())} }

// Sub returns a − b.
func (a Amount) Sub(b Amount) Amount { return Amount{atto: a.int().Sub(a.int(), b.int())} }

// Neg returns −a.
func (a Amount) Neg() Amount { return Amount{atto: a.int().Neg(a.int())} }

// Cmp reports −1, 0, +1 as a <, ==, > b.
func (a Amount) Cmp(b Amount) int { return a.int().Cmp(b.int()) }

// Sign reports −1, 0, +1 as a <, ==, > 0.
func (a Amount) Sign() int {
	if a.atto == nil {
		return 0
	}
	return a.atto.Sign()
}

// IsZero reports whether a == 0.
func (a Amount) IsZero() bool { return a.Sign() == 0 }

// IsNeg reports whether a < 0.
func (a Amount) IsNeg() bool { return a.Sign() < 0 }

// Atto returns a fresh big.Int of the atto magnitude (the on-chain uint256 value).
func (a Amount) Atto() *big.Int { return a.int() }

// AttoString is the canonical STORAGE form: the signed integer atto as a decimal string
// (exact, sortable with fixed width by the caller, on-chain-identical).
func (a Amount) AttoString() string { return a.int().String() }

// Cents rounds the value to whole cents (round-half-up) — a human/legacy display unit
// ONLY; never use it inside money math (it is lossy by construction).
func (a Amount) Cents() int64 {
	v := a.int()
	neg := v.Sign() < 0
	if neg {
		v.Neg(v)
	}
	q, r := new(big.Int).QuoRem(v, centScale, new(big.Int))
	if new(big.Int).Mul(r, two).Cmp(centScale) >= 0 {
		q.Add(q, big.NewInt(1))
	}
	if neg {
		q.Neg(q)
	}
	return q.Int64()
}

// MarshalJSON emits the exact decimal USD string ("0.00132") — a STRING, never a JSON
// number, so no consumer can reintroduce a float rounding error.
func (a Amount) MarshalJSON() ([]byte, error) {
	return []byte(`"` + a.String() + `"`), nil
}

// UnmarshalJSON accepts either a quoted decimal USD string or a bare decimal number
// (parsed exactly, without float).
func (a *Amount) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	if s == "null" || s == "" {
		*a = Amount{}
		return nil
	}
	s = strings.Trim(s, `"`)
	parsed, err := ParseUSD(s)
	if err != nil {
		return err
	}
	*a = parsed
	return nil
}

// String renders the value as a trimmed decimal USD string ("6.6", "0.00132", "-0.5",
// "0") — the human/JSON form. Exact: derived from the integer atto, not a float.
func (a Amount) String() string {
	v := a.int()
	neg := v.Sign() < 0
	if neg {
		v.Neg(v)
	}
	q, r := new(big.Int).QuoRem(v, scale, new(big.Int))
	out := q.String()
	if r.Sign() != 0 {
		frac := fmt.Sprintf("%0*s", Decimals, r.String())
		frac = strings.TrimRight(frac, "0")
		out += "." + frac
	}
	if neg {
		out = "-" + out
	}
	return out
}
