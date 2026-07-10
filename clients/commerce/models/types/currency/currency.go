package currency

import (
	"math/big"
	"strconv"
	"strings"
)

type Type string

// Does the currency not have a decimal convention such as Japanese Yen (¥100) instead
// of USD ($1.00)
func (t Type) IsZeroDecimal() bool {
	switch t {
	case BIF, CLP, DJF, GNF, JPY, KMF, KRW, MGA, PYG, RWF, VND, VUV, XAF, XOF, XPF:
		return true
	}

	return false
}

// Stringifies currency with symbol
func (t Type) ToString(c Cents) string {
	// Handle positives
	if c >= 0 {
		return t.Symbol() + t.ToStringNoSymbol(c)
	}

	// Handle Negatives
	seg1 := t.Symbol()
	seg2 := t.ToStringNoSymbol(c)

	return string(seg2[0]) + seg1 + seg2[1:]
}

// Stringifies currency with no
func (t Type) ToStringNoSymbol(c Cents) string {
	if t.IsZeroDecimal() {
		return strconv.Itoa(int(c))
	}
	cents := strconv.Itoa(int(c) % 100)
	if len(cents) < 2 {
		cents = "0" + cents
	}
	return strconv.Itoa(int(c)/100) + "." + cents
}

// Convert to float representation based on decimal convention
func (t Type) ToFloat(c Cents) float64 {
	if t.IsZeroDecimal() {
		return float64(c)
	}
	return float64(c) / 100.0
}

// Give the currency's Symbol + Code string
func (t Type) Label() string {
	return t.Symbol() + " " + t.Code()
}

// Give the currency's Code
func (t Type) Code() string {
	return strings.ToUpper(string(t))
}

// ------ More or Less Crypto Specific ------

// Is this a supported cryptocurrency
func (t Type) IsCrypto() bool {
	switch t {
	case BTC, ETH, XBT:
		return true
	}

	return false
}

// Since pricing things in a crypto minimal denomination exceed int64 and the
// minimal domination is worth so little, we generally use a larger
// denomination of the currency by convention that can capture the minimal
// relatable values.
//
// This returns the ratio of convention denomination to minimal denomination
func (t Type) MinimalUnitFactor() *big.Int {
	switch t {
	case ETH:
		//ETH is priced in Gwei or 1e-9 ETH or 0.000000001 ETH
		//Gwei or 1e9 ETH or 1,000,000,000 Wei so convert to wei
		return big.NewInt(1e9)
	}

	return big.NewInt(1)
}

func (t Type) ToMinimalUnits(c Cents) *big.Int {
	b := big.NewInt(int64(c))
	return b.Mul(b, t.MinimalUnitFactor())
}

func (t Type) FromMinimalUnits(b *big.Int) Cents {
	c := big.NewInt(0).Set(b)
	c = c.Div(c, t.MinimalUnitFactor())
	return Cents(c.Int64())
}
