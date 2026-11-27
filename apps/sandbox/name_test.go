package sandbox

import (
	"testing"

	"github.com/hanzoai/cloud/sdk/meter"
)

// Every class a lease can take reads as words on the bill, for its lease, its hours
// and its disk. A class added here without a name in metering would bill as its raw
// unit ("gpu/visor"), which is what a customer saw before the names existed.
func TestEveryClassIsNamedOnTheBill(t *testing.T) {
	for class := range classes {
		for _, unit := range []string{class + "/visor", "sandbox/" + class, "sandbox/" + class + "/disk"} {
			if meter.UnitName(unit) == unit {
				t.Errorf("sandbox class %q bills as %q with no name in metering.UnitName", class, unit)
			}
		}
	}
}
