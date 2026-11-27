package money

import "strconv"

// Cents is a whole number of US-dollar cents — what a ledger row, a price and a
// stored figure ARE. Amount is the other half of this package and answers a
// different question: it carries atto precision so a per-token cost can be
// multiplied without losing a fraction of a cent, and it rounds INTO Cents at the
// edge (Cents, CentsUp, CentsDown) where a figure becomes money someone owes.
//
// The underlying type is int64, so arithmetic is ordinary integer math and the
// JSON wire is the raw integer — String is a fmt method and encoding/json does not
// consult it, so declaring how a figure READS cannot change how it is STORED.
type Cents int64

// String renders the figure as it is written: "$1.00", "$0.07", "-$12.34", "$0.00".
//
// IT IS A METHOD SO THE SYMBOL TRAVELS WITH THE VALUE. Three packages rendered
// this and no two agreed — one omitted the symbol and dropped ".00" for a whole
// dollar, so the same figure read "$999" in one sentence and "$999.00" in another
// and a caller had to write the "$" into its own format string to make up the
// difference. A type that knows how it is written leaves nothing for a call site
// to get wrong.
//
// The sign leads, ahead of the symbol: "-$12.34" is the form a reader parses as
// one negative figure, where "$-12.34" reads as a symbol with an expression after
// it.
func (c Cents) String() string {
	sign := ""
	if c < 0 {
		sign, c = "-", -c
	}
	whole, frac := int64(c)/100, int64(c)%100
	return sign + "$" + strconv.FormatInt(whole, 10) + "." + twoDigits(frac)
}

// twoDigits pads a cents remainder to exactly two places. It is written out rather
// than left to %02d because the fixed width is the whole point of the rendering:
// seven cents is "$0.07", never "$0.7".
func twoDigits(n int64) string {
	if n < 10 {
		return "0" + strconv.FormatInt(n, 10)
	}
	return strconv.FormatInt(n, 10)
}
