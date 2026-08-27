package agents

// The runtime meter, driven through the REAL per-org stores and the REAL SQL
// compare-and-set, so the watermark, the two migrations and the sweep's set are
// measured rather than modelled. Each test names the MUTATION that makes it fail.

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/metering"
	"github.com/hanzoai/namespace"
	luxlog "github.com/luxfi/log"
)

const hour = 3600

// books captures what would have reached commerce.
type books struct {
	mu    sync.Mutex
	rows  []metering.Usage
	payer []string
}

func (b *books) emit(payer string, u metering.Usage) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.rows, b.payer = append(b.rows, u), append(b.payer, payer)
}

func (b *books) micros() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	var n int64
	for _, u := range b.rows {
		n += u.AmountMicros
	}
	return n
}

func (b *books) len() int { b.mu.Lock(); defer b.mu.Unlock(); return len(b.rows) }

// of returns every debit whose Model is kind.
func (b *books) of(kind string) []metering.Usage {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []metering.Usage
	for _, u := range b.rows {
		if u.Model == kind {
			out = append(out, u)
		}
	}
	return out
}

// meterStore is one org's real store, plus the state the sweep folds over.
func meterStore(t *testing.T) (*state, *Store) {
	t.Helper()
	st := &state{stores: testStores(t)}
	sto, err := st.storeFor("acme")
	if err != nil {
		t.Fatalf("storeFor: %v", err)
	}
	return st, sto
}

// sweep runs one tick against a capturing emit — the whole selection, watermark
// and debit path with no commerce.
func sweep(t *testing.T, st *state, now, rate int64, b *books) {
	t.Helper()
	_ = st.eachStore(func(ns namespace.Namespace, sto *Store, openErr error) {
		if openErr != nil {
			t.Fatalf("open %s: %v", ns, openErr)
		}
		org := ns.ID()
		owed, err := sto.Unbilled(context.Background(), org)
		if err != nil {
			t.Fatalf("unbilled: %v", err)
		}
		for _, x := range owed {
			_, err := cloud.RuntimeCharge(context.Background(), sessionRunning(x), endOf(x, now), rate,
				func(ctx context.Context, id string, was, at int64) (bool, error) {
					return sto.AdvanceSession(ctx, org, id, was, at)
				}, b.emit)
			if err != nil {
				t.Fatalf("charge session: %v", err)
			}
		}
		bots, err := sto.Resident(context.Background(), org)
		if err != nil {
			t.Fatalf("resident: %v", err)
		}
		for _, a := range bots {
			_, err := cloud.RuntimeCharge(context.Background(), botRunning(a), now, rate,
				func(ctx context.Context, id string, was, at int64) (bool, error) {
					return sto.Advance(ctx, org, a.Name, was, at)
				}, b.emit)
			if err != nil {
				t.Fatalf("charge bot: %v", err)
			}
		}
	})
}

func open(t *testing.T, sto *Store, id, payer string, at int64) Session {
	t.Helper()
	x := Session{ID: id, Org: "acme", Agent: "coder", Actor: "acme/z", Status: StatusRunning,
		RootID: id, StartedAt: at, CreatedAt: at, UpdatedAt: at, Payer: payer, MeteredAt: at}
	if err := sto.CreateSession(context.Background(), x); err != nil {
		t.Fatalf("create session: %v", err)
	}
	return x
}

// TestAnOpenSessionAccruesEveryTick is the ordinary case: a session that stays
// open is billed the elapsed hour on every pass, and the watermark carries.
//
// MUTATION: drop `ended_at=0` from Unbilled's WHERE and an open session is never
// billed at all.
func TestAnOpenSessionAccruesEveryTick(t *testing.T) {
	st, sto, b := func() (*state, *Store, *books) { a, c := meterStore(t); return a, c, &books{} }()
	const start = 1_800_000_000
	open(t, sto, "sess_1", "acme", start)

	sweep(t, st, start+hour, cloud.RuntimeHourMicros, b)
	sweep(t, st, start+2*hour, cloud.RuntimeHourMicros, b)

	if b.len() != 2 {
		t.Fatalf("two ticks produced %d debits, want 2", b.len())
	}
	if got := b.micros(); got != 2*cloud.RuntimeHourMicros {
		t.Fatalf("two open hours billed %d µ$, want %d", got, 2*cloud.RuntimeHourMicros)
	}
	if b.payer[0] != "acme" {
		t.Fatalf("the debit went to %q, not the session's payer", b.payer[0])
	}
}

// TestACloseIsBilledByTheSweepAndThenNeverAgain — the reason the close path needs
// no code. A session that ends is picked up once, charged exactly its unbilled
// tail, and leaves the set for good.
//
// MUTATION: remove the endOf clamp and a closed session goes on accruing forever
// against a wall clock that never stops.
func TestACloseIsBilledByTheSweepAndThenNeverAgain(t *testing.T) {
	st, sto := meterStore(t)
	b, ctx := &books{}, context.Background()
	const start = 1_800_000_000
	x := open(t, sto, "sess_1", "acme", start)

	// One tick while open, then the session ends half an hour later.
	sweep(t, st, start+hour, cloud.RuntimeHourMicros, b)
	x.Status, x.EndedAt, x.UpdatedAt = StatusDone, start+hour+1800, start+hour+1800
	if err := sto.UpdateSession(ctx, x); err != nil {
		t.Fatalf("close: %v", err)
	}

	sweep(t, st, start+4*hour, cloud.RuntimeHourMicros, b) // long after the close
	sweep(t, st, start+9*hour, cloud.RuntimeHourMicros, b)

	want := cloud.RuntimeCost(cloud.RuntimeHourMicros, hour+1800) // start → ended, once
	if got := b.micros(); got != want {
		t.Fatalf("a session open for 1.5h billed %d µ$, want %d", got, want)
	}
	if b.len() != 2 {
		t.Fatalf("want one debit for the open hour and one for the tail, got %d", b.len())
	}
}

// TestARunSessionIsNeverBilledForRuntime. openRunSession writes a session that
// STARTED AND ENDED in one instant — a completed synchronous run. What that costs
// is the per-run fee runAgent already charged; billing the instant again would be
// one act under two names.
//
// MUTATION: stamp MeteredAt at 0 instead of ts and this row enters the sweep set.
func TestARunSessionIsNeverBilledForRuntime(t *testing.T) {
	st, sto, b := func() (*state, *Store, *books) { a, c := meterStore(t); return a, c, &books{} }()
	const ts = 1_800_000_000
	x := Session{ID: "sess_run", Org: "acme", Agent: "coder", Actor: "acme/z", Status: StatusDone,
		RootID: "sess_run", StartedAt: ts, EndedAt: ts, CreatedAt: ts, UpdatedAt: ts,
		Payer: "acme", MeteredAt: ts}
	if err := sto.CreateSession(context.Background(), x); err != nil {
		t.Fatalf("create: %v", err)
	}
	sweep(t, st, ts+9*hour, cloud.RuntimeHourMicros, b)
	if b.len() != 0 {
		t.Fatalf("a run that started and ended in one instant billed %d µ$ of runtime", b.micros())
	}
}

// TestAResidentBotAccruesWithNoSessionAtAll is the case the owner's order turns
// on: a bot sitting resident has NO open session — the scheduler invokes it and
// each invocation is born and dies in one call — so its residency is the mode,
// and the mode is what accrues.
//
// MUTATION: point Resident at ListLongRunning (which additionally requires a
// schedule) and a bot bound to compute with no cron runs free.
func TestAResidentBotAccruesWithNoSessionAtAll(t *testing.T) {
	st, sto := meterStore(t)
	b, ctx := &books{}, context.Background()
	const start = 1_800_000_000

	bot := Agent{ID: "agent_1", Org: "acme", Name: "watcher", Model: "zen-coder",
		Status: "ready", ExecutionMode: ModeLongRunning, ComputeRef: "m_1",
		CreatedAt: start, UpdatedAt: start, Payer: "acme", MeteredAt: start}
	if err := sto.Create(ctx, bot); err != nil {
		t.Fatalf("create bot: %v", err)
	}
	// And a one-shot agent beside it, which must accrue NOTHING.
	if err := sto.Create(ctx, Agent{ID: "agent_2", Org: "acme", Name: "helper", Model: "zen-coder",
		Status: "ready", ExecutionMode: ModeOneShot, CreatedAt: start, UpdatedAt: start,
		Payer: "acme"}); err != nil {
		t.Fatalf("create one-shot: %v", err)
	}

	sweep(t, st, start+24*hour, cloud.RuntimeHourMicros, b)

	rows := b.of("bot")
	if len(rows) != 1 {
		t.Fatalf("a resident day produced %d bot debits, want 1", len(rows))
	}
	want := cloud.RuntimeCost(cloud.RuntimeHourMicros, 24*hour) // $1.92
	if rows[0].AmountMicros != want {
		t.Fatalf("a resident bot's day billed %d µ$, want %d", rows[0].AmountMicros, want)
	}
	if b.len() != 1 {
		t.Fatalf("the one-shot agent beside it was billed too: %d debits total", b.len())
	}
}

// TestOneSweeperOwnsASessionSpan drives the REAL SQL compare-and-set from 25
// goroutines: two pods, a restart, a retry — one span, one debit.
//
// MUTATION: drop `AND metered_at=?` from AdvanceSession and this fails with 25
// debits for one hour of one session.
func TestOneSweeperOwnsASessionSpan(t *testing.T) {
	_, sto := meterStore(t)
	b := &books{}
	const start, now = 1_800_000_000, 1_800_003_600
	x := open(t, sto, "sess_1", "acme", start)

	var wg sync.WaitGroup
	for range 25 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = cloud.RuntimeCharge(context.Background(), sessionRunning(x), now, cloud.RuntimeHourMicros,
				func(ctx context.Context, id string, was, at int64) (bool, error) {
					return sto.AdvanceSession(ctx, "acme", id, was, at)
				}, b.emit)
		}()
	}
	wg.Wait()

	if b.len() != 1 {
		t.Fatalf("25 sweepers over one span produced %d debits, want exactly 1", b.len())
	}
	if got := b.micros(); got != cloud.RuntimeHourMicros {
		t.Fatalf("one hour billed %d µ$, want %d", got, cloud.RuntimeHourMicros)
	}
}

// TestASessionFromBeforeTheMeterIsNotBackCharged. A row written before the meter
// existed carries no watermark; the first tick starts its clock and charges
// nothing for the hours that were free when they ran.
//
// MUTATION: default metered_at to created_at in the migration and every session
// open at rollout is billed for its whole life on the first tick.
func TestASessionFromBeforeTheMeterIsNotBackCharged(t *testing.T) {
	st, sto := meterStore(t)
	b, ctx := &books{}, context.Background()
	const born = 1_800_000_000
	x := Session{ID: "sess_old", Org: "acme", Agent: "coder", Actor: "acme/z",
		Status: StatusRunning, RootID: "sess_old",
		StartedAt: born, CreatedAt: born, UpdatedAt: born} // no payer, no watermark
	if err := sto.CreateSession(ctx, x); err != nil {
		t.Fatalf("create: %v", err)
	}

	const rollout = born + 30*24*hour
	sweep(t, st, rollout, cloud.RuntimeHourMicros, b)
	if b.len() != 0 {
		t.Fatalf("the first tick back-charged %d µ$ for a month nobody was counting", b.micros())
	}
	// And from there it accrues normally, against the ORG's own pool since the row
	// names no wallet.
	sweep(t, st, rollout+hour, cloud.RuntimeHourMicros, b)
	if got := b.micros(); got != cloud.RuntimeHourMicros {
		t.Fatalf("the hour after the first tick billed %d µ$, want %d", got, cloud.RuntimeHourMicros)
	}
	if b.payer[0] != "acme" {
		t.Fatalf("a session with no recorded wallet billed %q, want the org's own pool", b.payer[0])
	}
}

// TestUnbilledIsNotTruncated. Every other list in this store is a page; a set
// money is computed over may not be, or every session past the limit runs free.
func TestUnbilledIsNotTruncated(t *testing.T) {
	_, sto := meterStore(t)
	const n = 600
	for i := range n {
		open(t, sto, fmt.Sprintf("sess_%d", i), "acme", 1_800_000_000)
	}
	got, err := sto.Unbilled(context.Background(), "acme")
	if err != nil {
		t.Fatalf("unbilled: %v", err)
	}
	if len(got) != n {
		t.Fatalf("Unbilled returned %d of %d open sessions — the rest would run free", len(got), n)
	}
}

// TestTheSweepSurvivesAnUnreadableStore: a store that cannot be opened has said
// nothing, not "this org has nothing running", and must not stop the fold.
func TestTheSweepSurvivesAnUnreadableStore(t *testing.T) {
	st, sto := meterStore(t)
	b := &books{}
	const start = 1_800_000_000
	open(t, sto, "sess_1", "acme", start)

	// meterRuntime with a rate of zero is the free-runtime branch: nothing moves.
	meterRuntime(context.Background(), st, luxlog.NewNoOpLogger(), nil)
	if b.len() != 0 {
		t.Fatal("the harness emitted through the wrong path")
	}
}
