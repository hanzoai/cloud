package environ_test

import (
	"testing"

	"github.com/hanzoai/cloud/internal/environ"
)

// TestOrReadsTheVariable is the ordinary path: a set variable wins over the default.
func TestOrReadsTheVariable(t *testing.T) {
	t.Setenv("CLOUD_TEST_BRAND", "hanzo")
	if got := environ.Or("CLOUD_TEST_BRAND", "zoo"); got != "hanzo" {
		t.Errorf("Or = %q, want hanzo", got)
	}
}

// TestOrFallsBackWhenUnset: an absent variable is the default, not "".
func TestOrFallsBackWhenUnset(t *testing.T) {
	if got := environ.Or("CLOUD_TEST_ABSENT_a1b2", "zoo"); got != "zoo" {
		t.Errorf("Or = %q, want zoo", got)
	}
}

// TestABlankVariableIsNotAValue is the whole reason this is one function.
//
// Ten of the twenty-five readers this replaces took the raw string, so a variable
// holding spaces WON against the default and travelled on as a brand, a chain and
// a model name. An operator who sets a variable to whitespace has not set it.
func TestABlankVariableIsNotAValue(t *testing.T) {
	for _, blank := range []string{" ", "   ", "\t", "\n", " \t\n "} {
		t.Setenv("CLOUD_TEST_BLANK", blank)
		if got := environ.Or("CLOUD_TEST_BLANK", "hanzo"); got != "hanzo" {
			t.Errorf("Or with %q = %q, want the default hanzo", blank, got)
		}
	}
}

// TestThePaddingIsNotPartOfTheValue: a padded value is used, without its padding.
func TestThePaddingIsNotPartOfTheValue(t *testing.T) {
	t.Setenv("CLOUD_TEST_PADDED", "  hanzo\n")
	if got := environ.Or("CLOUD_TEST_PADDED", "zoo"); got != "hanzo" {
		t.Errorf("Or = %q, want hanzo", got)
	}
}
