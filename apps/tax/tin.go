package tax

// tin.go — a taxpayer identification number: what makes one well-formed, how it
// is shown, and how it is kept.
//
// A TIN IS NEVER AT REST IN THE CLEAR. The store seals it as a field (internal/org,
// field.go) under its OWNER's key before it is written — the key cek derives from the
// deployment master for that org's namespace, bound to the table, column and row it sits
// in — and it is opened only to be masked, to be printed on a form the law requires it on,
// or to be read in full by an admin of a payer the payee granted it to, with that read on
// the audit trail. A TIN in memory is a TIN value; one on disk is the store's sealed text.
//
// The number never appears in a log line, an error, an audit record or a
// response except the one full read, and every type here that could carry it
// renders it masked.

import (
	"errors"
	"fmt"
	"strings"
)

// TINType is which box of Form W-9 Part I the number is in.
type TINType string

const (
	// SSN is the individual box: a social security number or an ITIN.
	SSN TINType = "ssn"
	// EIN is an employer identification number.
	EIN TINType = "ein"
)

// TIN is nine digits and what kind they are. Its String is the masked form, so
// a TIN that reaches a formatter by accident prints the last four and nothing
// else.
type TIN struct {
	Type   TINType
	digits string
}

// String masks the number the way a payee statement may show it: the first five
// digits replaced (Pub 1099, "Truncating payee's TIN on payee statements").
func (t TIN) String() string {
	if len(t.digits) != 9 {
		return ""
	}
	if t.Type == EIN {
		return "**-***" + t.digits[5:]
	}
	return "***-**-" + t.digits[5:]
}

// GoString keeps %#v masked too.
func (t TIN) GoString() string { return t.String() }

// Zero reports that no number is held.
func (t TIN) Zero() bool { return t.digits == "" }

// full is the number as the IRS reads it: nine digits with the form's hyphens.
// Only a return filed with the IRS and the audited full read ever call it.
func (t TIN) full() string {
	if len(t.digits) != 9 {
		return ""
	}
	if t.Type == EIN {
		return t.digits[:2] + "-" + t.digits[2:]
	}
	return t.digits[:3] + "-" + t.digits[3:5] + "-" + t.digits[5:]
}

// errTIN is every malformed-number refusal. The message names the rule broken
// and never repeats the number.
var errTIN = errors.New("tax: TIN")

// ParseTIN reads a TIN of the stated type, or says which rule it breaks.
//
// An EIN's first two digits must be a prefix the IRS assigns ("How EINs are
// assigned and valid EIN prefixes", irs.gov). An SSN never begins 000, 666 or
// 900–999, and never has group 00 or serial 0000 (SSA, Social Security Number
// Randomization). An ITIN goes in the SSN box and begins with 9, its fourth and
// fifth digits in 50–65, 70–88, 90–92 or 94–99 (Pub 4757). Nothing here says the
// number is ASSIGNED, or assigned to this name: that is IRS TIN Matching, which
// only a payer enrolled in IRS e-Services can run (see match in w9.go).
func ParseTIN(typ TINType, s string) (TIN, error) {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == ' ':
		default:
			return TIN{}, fmt.Errorf("%w: only digits, hyphens and spaces", errTIN)
		}
	}
	d := b.String()
	if len(d) != 9 {
		return TIN{}, fmt.Errorf("%w: nine digits", errTIN)
	}
	switch typ {
	case EIN:
		if !einPrefix[d[:2]] {
			return TIN{}, fmt.Errorf("%w: an EIN prefix the IRS does not assign", errTIN)
		}
	case SSN:
		if d[0] == '9' {
			if !itinGroup(d[3:5]) {
				return TIN{}, fmt.Errorf("%w: an ITIN's fourth and fifth digits are 50–65, 70–88, 90–92 or 94–99", errTIN)
			}
			break
		}
		if d[:3] == "000" || d[:3] == "666" || d[3:5] == "00" || d[5:] == "0000" {
			return TIN{}, fmt.Errorf("%w: an SSN the SSA does not assign", errTIN)
		}
	default:
		return TIN{}, fmt.Errorf("%w: type is ssn or ein", errTIN)
	}
	return TIN{Type: typ, digits: d}, nil
}

// einPrefix is every prefix the IRS lists as valid, across its campuses, the
// Internet, and the SBA ("How EINs are assigned and valid EIN prefixes",
// irs.gov, read 2026-09-23). 00, 07–09, 17–19, 28–29, 49, 69–70, 78–79, 89 and
// 96–97 are not assigned.
var einPrefix = func() map[string]bool {
	m := map[string]bool{}
	for _, p := range strings.Fields(`01 02 03 04 05 06 10 11 12 13 14 15 16 20 21 22 23 24 25 26 27
		30 31 32 33 34 35 36 37 38 39 40 41 42 43 44 45 46 47 48 50 51 52 53 54 55 56 57 58 59
		60 61 62 63 64 65 66 67 68 71 72 73 74 75 76 77 80 81 82 83 84 85 86 87 88
		90 91 92 93 94 95 98 99`) {
		m[p] = true
	}
	return m
}()

func itinGroup(g string) bool {
	n := int(g[0]-'0')*10 + int(g[1]-'0')
	return (n >= 50 && n <= 65) || (n >= 70 && n <= 88) || (n >= 90 && n <= 92) || n >= 94
}

// pack is the text a TIN is sealed as: its type and its nine digits. The zero TIN packs to
// the empty string, which the store keeps empty.
func (t TIN) pack() string {
	if t.Zero() {
		return ""
	}
	return string(t.Type) + ":" + t.digits
}

// unpackTIN reads what pack wrote. Nothing packed is the zero TIN.
func unpackTIN(s string) (TIN, error) {
	if s == "" {
		return TIN{}, nil
	}
	typ, d, ok := strings.Cut(s, ":")
	if !ok || len(d) != 9 {
		return TIN{}, fmt.Errorf("tax: a sealed TIN is malformed")
	}
	return TIN{Type: TINType(typ), digits: d}, nil
}
