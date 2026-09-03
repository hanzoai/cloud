package cloud

// Tests for the agent-runtime meter. They drive the real functions with an
// in-memory watermark and a capturing emit, so the whole exactly-once path is
// exercised with no commerce, no store and no clock.
//
// Each test names the MUTATION that makes it fail, because a money test that
// passes against a broken meter is worse than no test: it is a green build over a
// revenue leak.

import (
	"context"
	"encoding/json"
	"math"
	"strings"
	"sync"
	"testing"

	"github.com/hanzoai/cloud/metering"
	"github.com/hanzoai/plans"
)

// watermarks is a store's compare-and-set on a row's runtime watermark, in memory.
// The real ones are SQL (`UPDATE … SET metered_at=? WHERE id=? AND metered_at=?`)
// and this reproduces exactly what makes them safe: the update lands only if the
// value the caller read is still there.
type watermarks struct {
	mu sync.Mutex
	at map[string]int64
	// unconditional models the MUTATION every concurrency test here is against:
	// a CAS that forgot its WHERE clause.
	unconditional bool
}

func newWatermarks() *watermarks { return &watermarks{at: map[string]int64{}} }

func (w *watermarks) advance(_ context.Context, id string, was, now int64) (bool, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.unconditional && w.at[id] != was {
		return false, nil
	}
	w.at[id] = now
	return true, nil
}

// debits captures what the meter would have sent to the ledger.
type debits struct {
	mu   sync.Mutex
	rows []metering.Usage
}

func (d *debits) emit(_ string, u metering.Usage) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.rows = append(d.rows, u)
}

func (d *debits) micros() int64 {
	d.mu.Lock()
	defer d.mu.Unlock()
	var n int64
	for _, u := range d.rows {
		n += u.AmountMicros
	}
	return n
}

func (d *debits) len() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.rows)
}

// TestTheFloorIsTheCatalogsRate pins the compiled floor to the published price.
//
// The rate is a PRODUCT decision, published in @hanzo/plans seats.json, and the
// floor here is a Go constant because money is not a float. Those are two writings
// of one number, which is two chances to disagree — so this test does the
// conversion in the one place a wrong answer is a red build.
//
// MUTATION: change RuntimeHourMicros to 8_000 (a plausible slip: $0.08 read as
// cents rather than dollars) and this fails naming both numbers.
func TestTheFloorIsTheCatalogsRate(t *testing.T) {
	data, err := plans.Data()
	if err != nil {
		t.Fatalf("read the published catalog: %v", err)
	}
	raw, ok := data["seats.json"]
	if !ok {
		t.Fatal("seats.json is absent from the catalog: the rate has no published source")
	}
	var seats struct {
		Runtime struct {
			AgentHourUSD float64 `json:"agentHourUSD"`
		} `json:"runtime"`
	}
	b, err := json.Marshal(raw)
	if err != nil {
		t.Fatalf("re-encode seats.json: %v", err)
	}
	if err := json.Unmarshal(b, &seats); err != nil {
		t.Fatalf("decode seats.json: %v", err)
	}
	if seats.Runtime.AgentHourUSD <= 0 {
		t.Fatal("the catalog publishes no runtime.agentHourUSD")
	}
	want := int64(math.Round(seats.Runtime.AgentHourUSD * 1e6))
	if RuntimeHourMicros != want {
		t.Fatalf("the floor and the catalog disagree: RuntimeHourMicros = %d µ$, "+
			"seats.json runtime.agentHourUSD = %v ($%v/hour = %d µ$)",
			RuntimeHourMicros, seats.Runtime.AgentHourUSD, seats.Runtime.AgentHourUSD, want)
	}
}

// TestRuntimeCostIsTheRateTimesTheHours reads the conversion in ONE direction
// against arithmetic tied to the rate.
//
// A round trip cannot see a wrong factor — multiply and divide by the same wrong
// number and the floor comes back unchanged — which is why this asks for absolute
// answers rather than for self-consistency.
//
// MUTATION: change secsPerHour to 60 and every row but the zero ones fails.
func TestRuntimeCostIsTheRateTimesTheHours(t *testing.T) {
	const hour = 3600
	for _, tc := range []struct {
		name string
		rate int64
		secs int64
		want int64
	}{
		{"one hour at the floor is eight cents", RuntimeHourMicros, hour, 80_000},
		{"half an hour is four cents", RuntimeHourMicros, hour / 2, 40_000},
		{"a quarter hour is two cents", RuntimeHourMicros, hour / 4, 20_000},
		{"a day is $1.92", RuntimeHourMicros, 24 * hour, 1_920_000},
		{"a month of a resident bot is $57.60", RuntimeHourMicros, 30 * 24 * hour, 57_600_000},
		{"one second rounds half-up", RuntimeHourMicros, 1, 22},
		{"a published zero is free, never rounded up", 0, hour, 0},
		{"no span is no charge", RuntimeHourMicros, 0, 0},
		{"a backwards clock is no charge", RuntimeHourMicros, -60, 0},
		{"a negative rate is no charge", -1, hour, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := RuntimeCost(tc.rate, tc.secs); got != tc.want {
				t.Fatalf("RuntimeCost(%d, %d) = %d µ$, want %d", tc.rate, tc.secs, got, tc.want)
			}
		})
	}
}

// TestTheStarterCreditBuysSixtyTwoAndAHalfHours states the free-usage story as
// arithmetic: a new org's $5 starter credit IS the free tier, and this is how much
// runtime it buys. Nothing anywhere grants included hours, and this is the test
// that would go red if somebody made the rate mean something else.
func TestTheStarterCreditBuysSixtyTwoAndAHalfHours(t *testing.T) {
	const starterCreditMicros int64 = 5_000_000 // $5.00
	const wantSecs = 62*3600 + 1800             // 62.5 hours
	if got := RuntimeCost(RuntimeHourMicros, wantSecs); got != starterCreditMicros {
		t.Fatalf("$5 of starter credit buys %d seconds at %d µ$/hour, "+
			"which prices at %d µ$, want %d", wantSecs, RuntimeHourMicros, got, starterCreditMicros)
	}
}

// TestFirstSightStartsTheClockAndChargesNothing: a row the meter has never seen
// starts its clock now. Shipping the meter must never back-charge for hours that
// were free when they ran.
//
// MUTATION: drop the `r.MeteredAt <= 0` branch and this bills the whole epoch.
func TestFirstSightStartsTheClockAndChargesNothing(t *testing.T) {
	w, d := newWatermarks(), &debits{}
	const now = 1_800_000_000

	micros, err := RuntimeCharge(context.Background(),
		Running{ID: "s1", Payer: "acme", Model: "session", Rate: RuntimeHourMicros}, now, w.advance, d.emit)
	if err != nil {
		t.Fatalf("charge: %v", err)
	}
	if micros != 0 || d.len() != 0 {
		t.Fatalf("first sight billed %d µ$ in %d rows, want nothing", micros, d.len())
	}
	if w.at["s1"] != now {
		t.Fatalf("first sight left the watermark at %d, want %d", w.at["s1"], now)
	}
}

// TestASpanIsChargedOnceHoweverManySweepersRunIt is the money invariant: 25
// concurrent sweepers over one row move the money exactly once.
//
// This is what the compare-and-set is FOR. Two pods ticking the same org, a sweep
// racing a close, a retry after a timeout — all of them read the same watermark and
// exactly one of them may own the span.
//
// MUTATION: set w.unconditional (a CAS that forgot its WHERE clause) and this fails
// with 25 debits and 25 hours of charge for one hour of runtime.
func TestASpanIsChargedOnceHoweverManySweepersRunIt(t *testing.T) {
	w, d := newWatermarks(), &debits{}
	const start, now = 1_800_000_000, 1_800_003_600 // exactly one hour
	w.at["s1"] = start

	var wg sync.WaitGroup
	for range 25 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = RuntimeCharge(context.Background(),
				Running{ID: "s1", Payer: "acme", Model: "session", Rate: RuntimeHourMicros, MeteredAt: start},
				now, w.advance, d.emit)
		}()
	}
	wg.Wait()

	if n := d.len(); n != 1 {
		t.Fatalf("25 sweepers over one span produced %d debits, want exactly 1", n)
	}
	if got := d.micros(); got != RuntimeHourMicros {
		t.Fatalf("one hour of runtime billed %d µ$, want %d", got, RuntimeHourMicros)
	}
}

// TestOneSpanIsOneActAtTheLedger: the ref is deterministic in the span, so a retry
// of the same window is one movement of money even if it reaches the ledger twice.
// The CAS makes a second attempt rare; this makes it free.
//
// MUTATION: build the ref from anything the span does not determine — a fresh
// random id, or the wall clock — and the two refs differ, which is a caller charged
// twice for one hour the day a debit is re-sent after a lost reply.
func TestOneSpanIsOneActAtTheLedger(t *testing.T) {
	const start, now = 1_800_000_000, 1_800_003_600
	d := &debits{}
	// Two charges of the IDENTICAL span: the store answered the first CAS, then the
	// reply was lost and the caller ran the same window again.
	for range 2 {
		w := newWatermarks()
		w.at["s1"] = start
		if _, err := RuntimeCharge(context.Background(),
			Running{ID: "s1", Payer: "acme", Model: "session", Rate: RuntimeHourMicros, MeteredAt: start},
			now, w.advance, d.emit); err != nil {
			t.Fatalf("charge: %v", err)
		}
	}
	if d.len() != 2 {
		t.Fatalf("the harness did not run the span twice: %d debits", d.len())
	}
	if d.rows[0].Ref != d.rows[1].Ref {
		t.Fatalf("one span produced two act names, so the ledger cannot collapse them: %q vs %q",
			d.rows[0].Ref, d.rows[1].Ref)
	}
	if ref := d.rows[0].Ref; !strings.Contains(ref, "s1") ||
		!strings.Contains(ref, "1800000000") || !strings.Contains(ref, "1800003600") {
		t.Fatalf("the act name does not name the row and both edges of its span: %q", ref)
	}
}

// TestTwoSpansOfOneRowAreTwoActs is the other half: distinct windows must NOT
// collapse, or an hour billed after an hour is charged once.
//
// MUTATION: drop the window from the ref (key on the row alone) and this fails —
// which is the mistake that reads as "idempotent" and is in fact "free after the
// first hour".
func TestTwoSpansOfOneRowAreTwoActs(t *testing.T) {
	w, d := newWatermarks(), &debits{}
	const t0, t1, t2 = 1_800_000_000, 1_800_003_600, 1_800_007_200
	w.at["s1"] = t0

	for _, span := range [][2]int64{{t0, t1}, {t1, t2}} {
		if _, err := RuntimeCharge(context.Background(),
			Running{ID: "s1", Payer: "acme", Model: "session", Rate: RuntimeHourMicros, MeteredAt: span[0]},
			span[1], w.advance, d.emit); err != nil {
			t.Fatalf("charge: %v", err)
		}
	}
	if d.len() != 2 {
		t.Fatalf("two hours produced %d debits, want 2", d.len())
	}
	if d.rows[0].Ref == d.rows[1].Ref {
		t.Fatalf("two spans share one act name %q, so the second hour is free", d.rows[0].Ref)
	}
	if got := d.micros(); got != 2*RuntimeHourMicros {
		t.Fatalf("two hours billed %d µ$, want %d", got, 2*RuntimeHourMicros)
	}
}

// TestACloseBillsTheTailAndNeverTheSweptSpan: a close is a charge with the row's
// end for its right-hand edge, so a sweep followed by a close bills the whole
// duration exactly once and no second of it twice.
func TestACloseBillsTheTailAndNeverTheSweptSpan(t *testing.T) {
	w, d := newWatermarks(), &debits{}
	const start = 1_800_000_000
	const swept = start + 900   // a 15-minute tick
	const closed = start + 1500 // the session ended 10 minutes later
	w.at["s1"] = start

	if _, err := RuntimeCharge(context.Background(),
		Running{ID: "s1", Payer: "acme", Model: "session", Rate: RuntimeHourMicros, MeteredAt: start},
		swept, w.advance, d.emit); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if _, err := RuntimeCharge(context.Background(),
		Running{ID: "s1", Payer: "acme", Model: "session", Rate: RuntimeHourMicros, MeteredAt: w.at["s1"]},
		closed, w.advance, d.emit); err != nil {
		t.Fatalf("close: %v", err)
	}
	want := RuntimeCost(RuntimeHourMicros, closed-start)
	if got := d.micros(); got != want {
		t.Fatalf("a 1500s session swept once then closed billed %d µ$, want %d (the whole span, once)", got, want)
	}
}

// shipped is a ship that acked — the ordinary case, so a test about arithmetic
// says nothing about durability. The tests that are about the ship supply their
// own.
func shipped() (bool, error) { return true, nil }

// TestSweepChargesEveryRowAndCountsOnlyThePaidOnes.
func TestSweepChargesEveryRowAndCountsOnlyThePaidOnes(t *testing.T) {
	w, d := newWatermarks(), &debits{}
	const start, now = 1_800_000_000, 1_800_003_600
	w.at["a"], w.at["b"] = start, start

	// Each row states its own price, because the rate is a fact about what is
	// running: a pass holds rows of different kinds and one rate for all of them is
	// how an expensive envelope is sold at a cheap one's price.
	const hr = RuntimeHourMicros
	rows := []Running{
		{ID: "a", Payer: "acme", Model: "session", Rate: hr, MeteredAt: start},
		{ID: "b", Payer: "hanzo/z", Model: "bot", Rate: hr, MeteredAt: start},
		{ID: "c", Payer: "acme", Model: "session", Rate: hr},    // first sight: clock starts, no charge
		{ID: "d", Model: "session", Rate: hr, MeteredAt: start}, // no payer: nothing to bill
		{ID: "", Payer: "acme", Rate: hr, MeteredAt: start},     // no row id: nothing to bill
		{ID: "e", Payer: "acme", Rate: hr, MeteredAt: now + 60}, // watermark ahead of now
	}
	n, err := RuntimeSweep(context.Background(), rows, now, w.advance, shipped, d.emit)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if n != 2 {
		t.Fatalf("sweep billed %d rows, want 2", n)
	}
	if got := d.micros(); got != 2*RuntimeHourMicros {
		t.Fatalf("two rows of one hour billed %d µ$, want %d", got, 2*RuntimeHourMicros)
	}
	if w.at["c"] != now {
		t.Fatalf("a first-sight row did not start its clock: %d", w.at["c"])
	}
}

// TestAPublishedZeroIsFree: a rate row of zero is a price — something the platform
// meters and gives away — and it must charge nothing rather than a rounded-up
// minimum.
func TestAPublishedZeroIsFree(t *testing.T) {
	w, d := newWatermarks(), &debits{}
	const start, now = 1_800_000_000, 1_800_003_600
	w.at["s1"] = start

	micros, err := RuntimeCharge(context.Background(),
		Running{ID: "s1", Payer: "acme", Model: "session", Rate: 0, MeteredAt: start},
		now, w.advance, d.emit)
	if err != nil {
		t.Fatalf("charge: %v", err)
	}
	if micros != 0 || d.len() != 0 {
		t.Fatalf("a published zero billed %d µ$ in %d rows, want nothing", micros, d.len())
	}
	if w.at["s1"] != now {
		t.Fatal("a free span must still advance the watermark, or it is re-read forever")
	}
}

// TestTheDebitCarriesTheScopeTheCapSumsOver: project and service ride the debit, so
// a per-scope spend cap can see runtime spend at all.
func TestTheDebitCarriesTheScopeTheCapSumsOver(t *testing.T) {
	w, d := newWatermarks(), &debits{}
	const start, now = 1_800_000_000, 1_800_003_600
	w.at["s1"] = start

	if _, err := RuntimeCharge(context.Background(),
		Running{ID: "s1", Payer: "acme", Project: "atlas", Model: "sandbox/dev", Rate: RuntimeHourMicros, MeteredAt: start},
		now, w.advance, d.emit); err != nil {
		t.Fatalf("charge: %v", err)
	}
	u := d.rows[0]
	if u.Project != "atlas" {
		t.Fatalf("the debit lost its project scope: %q", u.Project)
	}
	if u.Service != RuntimeMeter {
		t.Fatalf("the debit does not name the meter: service = %q, want %q", u.Service, RuntimeMeter)
	}
	if u.Model != "sandbox/dev" {
		t.Fatalf("the debit does not say what ran: %q", u.Model)
	}
	if u.AmountCents != 0 {
		t.Fatal("runtime bills in micro-USD; a cents field set beside it is a second amount")
	}
}

// TestAFreeSpanIsStillShipped — the ship follows the WATERMARK, not the debit.
//
// A published price of zero is a price: something the platform meters and gives
// away. Nothing is charged and the span still HAPPENED, so the watermark moves,
// because the watermark is what says the span is accounted for.
//
// Shipping only when money was emitted leaves every one of those advances local.
// The durable watermark stays at the start of the promotion, and the first replica
// to open the org afterwards bills the whole free week at whatever the price is by
// then, in one debit, to every tenant — the retroactive charge RuntimeCharge
// already refuses inside a process, arriving across pods instead.
//
// MUTATION: gate the ship on `len(held) == 0` rather than on `moved` and this
// fails: the watermarks move and nothing carries them.
func TestAFreeSpanIsStillShipped(t *testing.T) {
	w, d := newWatermarks(), &debits{}
	const start, now = 1_800_000_000, 1_800_003_600
	w.at["s1"], w.at["s2"] = start, start

	ships := 0
	ship := func() (bool, error) { ships++; return true, nil }

	rows := []Running{
		{ID: "s1", Payer: "acme", Model: "session", Rate: 0, MeteredAt: start},
		{ID: "s2", Payer: "acme", Model: "session", Rate: 0, MeteredAt: start},
	}
	n, err := RuntimeSweep(context.Background(), rows, now, w.advance, ship, d.emit)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if n != 0 || d.len() != 0 {
		t.Fatalf("a free hour billed %d rows / %d debits, want none", n, d.len())
	}
	if w.at["s1"] != now || w.at["s2"] != now {
		t.Fatalf("a free hour left the watermarks at %d/%d, want %d", w.at["s1"], w.at["s2"], now)
	}
	if ships != 1 {
		t.Fatalf("two free spans shipped %d times, want exactly 1 — an advance nothing carries "+
			"is an advance the next owner has never seen", ships)
	}
}

// TestAPassThatClaimsNothingShipsNothing is the other side of that rule: a sweep
// over rows already billed through `now` writes nothing, so it owes no round trip
// to an object store. Without it every idle org would ship its whole database
// every tick for no reason.
func TestAPassThatClaimsNothingShipsNothing(t *testing.T) {
	w, d := newWatermarks(), &debits{}
	const now = 1_800_003_600
	w.at["s1"] = now

	ships := 0
	ship := func() (bool, error) { ships++; return true, nil }

	rows := []Running{{ID: "s1", Payer: "acme", Model: "session", Rate: RuntimeHourMicros, MeteredAt: now}}
	if _, err := RuntimeSweep(context.Background(), rows, now, w.advance, ship, d.emit); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if ships != 0 {
		t.Fatalf("a pass that claimed nothing shipped %d times, want 0", ships)
	}
}
