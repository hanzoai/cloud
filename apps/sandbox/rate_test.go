package sandbox

import (
	"context"
	"testing"

	"github.com/hanzoai/cloud"
)

// The cheapest sandbox costs the platform's one agent-hour, and a bigger envelope
// costs more. Both halves are the claim: a floor that drifted up would price the
// smallest lease above what an agent session pays for the same hour, and a ceiling
// that collapsed to the floor would sell most of a node for a sixteenth of one.
//
// MUTATION: give android the default micros and this fails, which is the whole
// point — the class table is where an envelope and its price are stated together.
func TestTheSmallestClassCostsOneAgentHourAndABiggerOneCostsMore(t *testing.T) {
	ctx := context.Background()

	for _, class := range []string{"exec", "dev", "desktop"} {
		if got := rate(ctx, class); got != cloud.RuntimeHourMicros {
			t.Errorf("%s holds the default envelope, so it costs one agent-hour: got %d, want %d",
				class, got, cloud.RuntimeHourMicros)
		}
	}

	// Twelve times the memory of the default envelope, and a node packs sixteen of
	// those — so an android lease displaces twelve and is charged for twelve.
	if got, want := rate(ctx, "android"), 12*cloud.RuntimeHourMicros; got != want {
		t.Errorf("android holds twelve times the memory: got %d, want %d", got, want)
	}
}

// A class the table has never heard of is not free. It is at least an envelope,
// and the one answer certainly wrong is to bill nothing for it.
//
// MUTATION: return 0 for an unknown class and this fails.
func TestAnUnknownClassFallsBackToTheRuntimeRateRatherThanToFree(t *testing.T) {
	if got := rate(context.Background(), "no-such-class"); got != cloud.RuntimeHourMicros {
		t.Errorf("an unknown envelope billed %d; it must fall back to %d, never to nothing",
			got, cloud.RuntimeHourMicros)
	}
}

// Every class the API will accept carries a price. A class added to the table with
// no micros would be served, held, and billed at the fallback by accident rather
// than by decision.
//
// MUTATION: drop `micros` from any row in `classes` and this fails.
func TestEveryServedClassStatesItsOwnPrice(t *testing.T) {
	for name, c := range classes {
		if c.micros <= 0 {
			t.Errorf("class %q is served but states no price, so its hour is charged by accident", name)
		}
	}
}
