package risk

// resident_bound_test.go — the resident bound has an OPERATING POINT, and it is
// held as ONE property rather than as four separate ones.
//
// WHAT THIS ADDS OVER [TestEviction_IsCountedOnTheProbe] AND
// [TestEviction_WritesTheVictimsStateDownFirst], WHICH ALREADY EXIST. Measured,
// not assumed: each of the three mutations that disarm this bound is killed by
// that pair too, so this is not a gap closure and is not claimed as one. It is
// kept for two differences that only show up under load rather than at one
// sample. The pair checks the count once, just past the bound; this drives
// maxResident+8 organisations and asserts SERVED and BOUNDED at EVERY step, so a
// bound that holds for one arrival and not for the ninth is caught. And the
// pair's lossless leg can `t.Skip` itself when the victim happens to still be
// resident, which is a leg that can stop testing without anyone noticing; this
// one has no such exit.
//
// The bound is what keeps 64 × (8 MiB of rings + its model) inside the
// deployment's 9 GiB GOMEMLIMIT, on a binary that runs at ONE replica with
// Recreate, so disarming it is an OOM and an OOM is a total outage.
//
// A bigger constant is not the fix and never was. The operating point is FOUR
// properties together, and each one is a way the bound could be wrong:
//
//	SERVED    — the (maxResident+1)th organisation gets an answer. A bound that
//	            refuses the whole risk surface to the next tenant has no operating
//	            point at all; it has a cliff.
//	BOUNDED   — the resident count never exceeds maxResident. Otherwise the bound
//	            is decorative and the process dies instead.
//	LOUD      — eviction is counted and reported on the probe. Eviction is lossless,
//	            so a climbing count is the ONLY sign it is happening.
//	LOSSLESS  — an evicted organisation's learned state comes back. This is the
//	            entire justification for a process-wide bound being allowed to
//	            exist next to per-tenant state; without it, one tenant's arrival
//	            costs another tenant its model, which is the cross-tenant defect
//	            wearing a capacity hat.

import (
	"strconv"
	"testing"
	"time"
)

// TestResident_TheBoundHasAnOperatingPoint drives more organisations through the
// plane than it can hold and holds all four properties at once.
func TestResident_TheBoundHasAnOperatingPoint(t *testing.T) {
	probe.reset(true)
	p := newTestPlane(t)
	holdFolds(t, p)

	// The first organisation learns something, so its eviction is a state loss if
	// eviction is not lossless.
	first := key(t, brandA, "orgevict")
	teach(t, p, first, stream(120, time.Now().UTC().Add(-3*time.Hour)))
	learned, _, err := p.state(first)
	if err != nil {
		t.Fatalf("state of the first organisation: %v", err)
	}
	if learned.Learned == 0 {
		t.Fatal("the first organisation learned nothing — its eviction would prove nothing")
	}

	// Past the bound, one organisation at a time, so the LRU is well defined and
	// `first` is the least recently used.
	const past = 8
	for i := 0; i < maxResident+past; i++ {
		k := key(t, brandA, "orgbound"+strconv.Itoa(i))
		// SERVED: every one of them gets a residency, including the ones past the
		// bound. A refusal here is the cliff.
		if _, err := p.resident(k); err != nil {
			t.Fatalf("organisation %d of %d was refused a residency: %v — past the bound the surface has no operating point",
				i+1, maxResident+past, err)
		}
		// BOUNDED: never more than the bound, at any point along the way.
		p.mu.Lock()
		held := len(p.res)
		p.mu.Unlock()
		if held > maxResident {
			t.Fatalf("after %d organisations the plane holds %d residents against a bound of %d — the bound is decorative",
				i+1, held, maxResident)
		}
	}

	// LOUD: the count climbed and the probe reports it.
	held, built, evicted, _ := p.residents()
	if evicted == 0 {
		t.Fatalf("%d organisations went through a plane that holds %d and nothing was evicted (held=%d built=%d) — "+
			"either the bound is not binding or its pressure is invisible", maxResident+past+1, maxResident, held, built)
	}
	if held != maxResident {
		t.Fatalf("the plane settled at %d residents, want exactly the bound %d", held, maxResident)
	}

	// LOSSLESS: the evicted organisation's model comes back with what it learned.
	// Reading it makes it resident again, which is the rebuild the bound promises.
	back, _, err := p.state(first)
	if err != nil {
		t.Fatalf("the evicted organisation's state could not be read back: %v", err)
	}
	if back.Learned != learned.Learned {
		t.Fatalf("an evicted organisation came back having learned %d of %d — eviction cost a tenant its model, "+
			"which is what makes a process-wide bound unacceptable", back.Learned, learned.Learned)
	}
}
