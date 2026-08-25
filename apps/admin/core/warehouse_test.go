package core

// What these pin:
//
//  1. A QUOTED integer is a number. The datastore sends 64-bit integers as
//     strings under output_format_json_quote_64bit_integers, and any
//     toString()/formatted column arrives as a string whatever its type. A
//     coercer without the string arm answers 0 for such a column, and a wrong
//     zero on a spend board is indistinguishable from a real one.
//  2. Every label the enum renders reads back over the window it names.
//     WarehouseRange and WarehouseSince are two readings of ONE table, so a
//     member cannot be half-added; what is left to check is that the table's
//     labels and lookbacks line up, and that anything outside it widens to the
//     default rather than failing.

import (
	"testing"
	"time"
)

func TestQuotedNumberReadsAsTheNumber(t *testing.T) {
	if got := CHInt64("1200"); got != 1200 {
		t.Errorf(`CHInt64("1200") = %d, want 1200`, got)
	}
	if got := CHInt64(" 42 "); got != 42 {
		t.Errorf(`CHInt64(" 42 ") = %d, want 42`, got)
	}
	if got := CHFloat64("1.5"); got != 1.5 {
		t.Errorf(`CHFloat64("1.5") = %v, want 1.5`, got)
	}
	// A column that is not a number at all still reads as an honest zero.
	if CHInt64("nope") != 0 || CHFloat64("nope") != 0 || CHInt64(nil) != 0 {
		t.Error("a non-numeric cell must read 0, never a parse artefact")
	}
}

func TestEveryLabelReadsBackOverTheWindowItNames(t *testing.T) {
	named := map[string]time.Duration{
		"24h": 24 * time.Hour,
		"7d":  7 * 24 * time.Hour,
		"30d": 30 * 24 * time.Hour,
	}
	if len(warehouseWindow) != len(named) {
		t.Fatalf("the enum has %d members, this test names %d", len(warehouseWindow), len(named))
	}
	// Anything outside the enum widens to the default rather than failing.
	for in, label := range map[string]string{
		"24h": "24h", " 7d ": "7d", "30d": "30d", "": "30d", "xyz": "30d",
	} {
		if got := WarehouseRange(in); got != label {
			t.Errorf("WarehouseRange(%q) = %q, want %q", in, got, label)
		}
	}
	for label, want := range named {
		if warehouseWindow[label] != want {
			t.Errorf("%q covers %v, want %v", label, warehouseWindow[label], want)
		}
		got := time.Now().UTC().Sub(WarehouseSince(label))
		if d := got - want; d < -2*time.Second || d > 2*time.Second {
			t.Errorf("%q reads back %v, want ≈%v", label, got, want)
		}
	}
}
