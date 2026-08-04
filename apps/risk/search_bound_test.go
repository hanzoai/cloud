package risk

// search_bound_test.go — the expensive half of a search is behind the bound that
// is supposed to bound it.
//
// THE DEFECT. begin() checked "is a search already running for this tenant?" and
// then, much later — after rolling the tenant's source planes and reading its
// WHOLE history out of the warehouse — set the flag that answers that question.
// Check-then-act, with two expensive warehouse operations in the window:
//
//	p.mu.Lock(); if p.running[t] { conflict }; p.mu.Unlock()   // ← the check
//	p.roll(ctx, t)                                             // ← rollup
//	rows(ctx, t, {limit: maxHistory})                          // ← full history
//	admit(len(hist))                                           // ← the ledger gate
//	p.mu.Lock(); p.running[t] = pending; p.mu.Unlock()         // ← the act
//
// So the "ONE RUN PER TENANT" bound bounded nothing that costs anything. One
// tenant's concurrency — up to maxInFlight = 32 — multiplied straight through into
// 32 concurrent full-history reads against the one warehouse every tenant shares,
// and the ledger gate that is supposed to make a search cost something is the LAST
// thing in the sequence, so all of it happens before anyone is asked to pay.
//
// The slot is now CLAIMED before any of that work, atomically with the check, so
// the bound binds on the dimension that is actually expensive.

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zap-proto/zip"
)

// historyReads counts the full-history reads a search does — the SELECT with the
// replay's own row limit on it, which is the expensive one. It is matched on the
// limit so a rollup's own reads are not counted as history reads.
func historyReads() int {
	n := 0
	for _, s := range probe.reads() {
		if strings.Contains(s.SQL, "LIMIT "+itoa(maxHistory)) {
			n++
		}
	}
	return n
}

// TestSearch_OneTenantsConcurrencyDoesNotMultiplyTheExpensiveRead is the blocker:
// N concurrent searches for ONE organisation must read that organisation's history
// ONCE, not N times.
func TestSearch_OneTenantsConcurrencyDoesNotMultiplyTheExpensiveRead(t *testing.T) {
	probe.reset(true)
	p := newTestPlane(t)
	holdFolds(t, p)
	k := key(t, brandA, orgA)

	// A history worth reading, so the read is a real one.
	for i := 0; i < 24; i++ {
		probe.hold(string(k), map[string]any{
			"subject_kind": kindAccount, "subject": "u_" + itoa(i%4),
			"bucket": surfaceAt(i + 1),
			"events": uint32(2), "spend_nano": int64(150_000_000),
		})
	}
	// Make the residency first, so the race under test is the SEARCH slot and not
	// the residency single-flight (which has its own proof).
	if _, err := p.resident(k); err != nil {
		t.Fatalf("resident: %v", err)
	}
	before := historyReads()

	const callers = 16
	var wg sync.WaitGroup
	accepted := make([]bool, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := p.begin(context.Background(), k, 24*time.Hour, free)
			accepted[i] = err == nil
		}(i)
	}
	wg.Wait()

	n := 0
	for _, ok := range accepted {
		if ok {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d of %d concurrent searches for ONE organisation were accepted, want exactly 1 — the run slot is not exclusive", n, callers)
	}
	if got := historyReads() - before; got != 1 {
		t.Fatalf("%d concurrent searches drove %d full-history reads of one organisation's surface, want 1 — "+
			"one tenant's concurrency multiplied straight through onto the shared warehouse", callers, got)
	}
}

// TestSearch_ARefusedRunReleasesTheSlot keeps the claim honest in the other
// direction: claiming early must not leave a tenant permanently unable to search
// when the run is refused before it starts. Without the release, ONE empty-history
// search would 409 that organisation's every later search forever — a bound that
// binds too hard is still a bound that broke the product.
func TestSearch_ARefusedRunReleasesTheSlot(t *testing.T) {
	probe.reset(true)
	p := newTestPlane(t)
	holdFolds(t, p)
	k := key(t, brandA, orgA)

	// Empty surface: begin refuses with "history is empty" AFTER the claim.
	if _, err := p.begin(context.Background(), k, 24*time.Hour, free); err == nil {
		t.Fatal("a search over an empty history must be refused")
	}
	// The slot must be free again.
	p.mu.Lock()
	held, running := p.running[k]
	p.mu.Unlock()
	if running {
		t.Fatalf("a refused search left the run slot held by %q — this organisation can never search again", held.ID)
	}

	// And a real one now works.
	for i := 0; i < 8; i++ {
		probe.hold(string(k), map[string]any{
			"subject_kind": kindAccount, "subject": "u_" + itoa(i%2),
			"bucket": surfaceAt(i + 1),
			"events": uint32(2), "spend_nano": int64(150_000_000),
		})
	}
	if _, err := p.begin(context.Background(), k, 24*time.Hour, free); err != nil {
		t.Fatalf("after a refused search the next one must be admitted, got %v", err)
	}
}

// TestSearch_TheSlotIsPerTenant: the bound is one tenant's own, so a search
// running for one organisation must never refuse another's. A process-wide slot
// would be the shared-cap defect wearing a different hat.
func TestSearch_TheSlotIsPerTenant(t *testing.T) {
	probe.reset(true)
	p := newTestPlane(t)
	holdFolds(t, p)
	a, b := key(t, brandA, orgA), key(t, brandA, orgB)
	for _, k := range []tenant{a, b} {
		for i := 0; i < 8; i++ {
			probe.hold(string(k), map[string]any{
				"subject_kind": kindAccount, "subject": "u_" + itoa(i%2),
				"bucket": surfaceAt(i + 1),
				"events": uint32(2), "spend_nano": int64(150_000_000),
			})
		}
	}
	if _, err := p.begin(context.Background(), a, 24*time.Hour, free); err != nil {
		t.Fatalf("organisation A's search: %v", err)
	}
	if _, err := p.begin(context.Background(), b, 24*time.Hour, free); err != nil {
		t.Fatalf("organisation A's search refused organisation B's: %v — the slot is process-wide, not per tenant", err)
	}
}

// ── the other half of the same bound: the RATE, not only the concurrency ─────
//
// The slot above stops ONE tenant's concurrency from multiplying the expensive
// read. It does not stop that tenant asking again, and again: the setup ran
// BEFORE ANY GATE AT ALL — the ledger check was the LAST step of begin — so a
// caller with no balance drove a roll of up to four source planes and a
// full-window read on the one warehouse every tenant shares, was refused at the
// end, and paid for none of it, as often as it cared to ask.
//
// A search is now priced TWICE, each half before the half it prices: the surface
// from the WINDOW, which the caller states and which is therefore known before
// anything runs, and the grid from the MEASURED history. Pricing the grid on its
// upper bound instead would close the same hole and price out every small tenant
// — maxHistory × the whole grid, whatever that organisation's history holds.

// asked is one call the plane made to the money seam.
type asked struct {
	Kind string
	N    int
}

// meterer is a recording money seam: it remembers every gate and every meter, and
// refuses when told to. It is the whole ledger a plane test needs — the real one
// has its own fixture ([mountBilled]) and its own tests.
type meterer struct {
	deny  error
	gates []asked
	paid  []asked
}

func (m *meterer) charge(kind string, n int) (func(int), error) {
	m.gates = append(m.gates, asked{kind, n})
	if m.deny != nil {
		return nil, m.deny
	}
	return func(done int) { m.paid = append(m.paid, asked{kind, done}) }, nil
}

// TestSearch_ARefusedCallerNeverReachesTheWarehouse is the blocker: the gate on
// the surface read must run BEFORE the surface read.
//
// It is proved by what the warehouse SAW. Asserting only that the call was
// refused would pass with the gate still last, because it was refused then too —
// after the work.
func TestSearch_ARefusedCallerNeverReachesTheWarehouse(t *testing.T) {
	probe.reset(true)
	p := newTestPlane(t)
	holdFolds(t, p)
	k := key(t, brandA, orgA)
	// A history worth reading, so the read this test counts is a real one.
	for i := 0; i < 24; i++ {
		probe.hold(string(k), map[string]any{
			"subject_kind": kindAccount, "subject": "u_" + itoa(i%4),
			"bucket": surfaceAt(i + 1),
			"events": uint32(2), "spend_nano": int64(150_000_000),
		})
	}
	// Residency first, so what is counted below is the SEARCH's warehouse work and
	// not the rebuild that any first touch pays.
	if _, err := p.resident(k); err != nil {
		t.Fatalf("resident: %v", err)
	}
	before := len(probe.all())

	books := &meterer{deny: zip.Errorf(402, "insufficient balance")}
	if _, err := p.begin(context.Background(), k, 7*24*time.Hour, books.charge); err == nil {
		t.Fatal("a search whose gate refuses must be refused")
	}
	if n := len(probe.all()) - before; n != 0 {
		t.Fatalf("a caller refused for want of balance still drove %d warehouse statements — the gate runs "+
			"AFTER the work it is supposed to gate, so the roll and the full-window read are free and repeatable", n)
	}
	// And it was priced on the WINDOW, which is the only size known that early. A
	// surface priced at zero is a gate that is present and does not gate.
	if len(books.gates) != 1 || books.gates[0].N != windowScreens(7*24*time.Hour) {
		t.Fatalf("the surface read was gated as %+v, want one gate of %d screens (the window it rolls and reads)",
			books.gates, windowScreens(7*24*time.Hour))
	}
	if len(books.paid) != 0 {
		t.Fatalf("a refused search metered %+v — nothing ran", books.paid)
	}
}

// TestSearch_BothHalvesArePricedForWhatTheyAre: the surface on the window, the
// grid on the measured history. One price for both would be wrong in both
// directions — a flat fee under-prices the grid, and the grid's upper bound
// over-prices every tenant whose history is small.
func TestSearch_BothHalvesArePricedForWhatTheyAre(t *testing.T) {
	probe.reset(true)
	p := newTestPlane(t)
	holdFolds(t, p)
	k := key(t, brandA, orgA)
	const events = 12
	for i := 0; i < events; i++ {
		probe.hold(string(k), map[string]any{
			"subject_kind": kindAccount, "subject": "u_" + itoa(i%3),
			"bucket": surfaceAt(i + 1),
			"events": uint32(2), "spend_nano": int64(150_000_000),
		})
	}
	const days = 7
	books := &meterer{}
	run, err := p.begin(context.Background(), k, days*24*time.Hour, books.charge)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	// The grid runs behind the accept; close the plane so it has ended and the
	// books are final.
	if err := p.close(context.Background()); err != nil {
		t.Fatalf("close: %v", err)
	}
	// The grid is gated for SIXTY-FIVE passes over the history, not sixty-four: every
	// candidate, plus the one that FITS the winner into a value this organisation can
	// adopt. A gate of sixty-four would be a bound the meter is free to exceed.
	passes := len(candidates()) + 1
	want := []asked{{"search", days}, {"search", run.Events * passes}}
	if len(books.gates) != 2 || books.gates[0] != want[0] || books.gates[1] != want[1] {
		t.Fatalf("the run gated %+v, want %+v — the surface is priced from its window and the grid from its measured history", books.gates, want)
	}
	// METERED ON WHAT WAS DONE. The surface for the window it rolled and read; the
	// grid for the trials that actually ran, which after a close is not the whole
	// grid.
	if len(books.paid) != 2 {
		t.Fatalf("the run metered %+v, want one debit per half", books.paid)
	}
	if books.paid[0] != (asked{"search", days}) {
		t.Fatalf("the surface metered %+v, want %d screens", books.paid[0], days)
	}
	if books.paid[1].N > run.Events*passes {
		t.Fatalf("the grid metered %d screens, more than the %d it was admitted for — the meter is running on the "+
			"accepted size and not on the work", books.paid[1].N, run.Events*passes)
	}
}
