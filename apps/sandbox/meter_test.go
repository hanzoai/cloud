package sandbox

// The runtime meter, against the REAL schema and the real compare-and-set — the
// store is `openStore` over a file, so the watermark, the migration and the
// idempotency are measured rather than modelled.
//
// Every test names the MUTATION that makes it fail.

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/metering"
	luxlog "github.com/luxfi/log"
)

// ledger captures what would have reached commerce.
type ledger struct {
	mu   sync.Mutex
	rows []metering.Usage
	who  []string
}

func (l *ledger) emit(payer string, u metering.Usage) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.rows, l.who = append(l.rows, u), append(l.who, payer)
}

func (l *ledger) micros() int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	var n int64
	for _, u := range l.rows {
		n += u.AmountMicros
	}
	return n
}

func (l *ledger) len() int { l.mu.Lock(); defer l.mu.Unlock(); return len(l.rows) }

// lease writes one lease this org is holding, already metered through `at`.
func lease(t *testing.T, st *Store, id, org, payer string, at int64) Sandbox {
	t.Helper()
	m := Sandbox{
		ID: id, Org: org, Kind: KindSandbox, Class: "dev", Status: "running",
		Payer: payer, MeteredAt: at, CreatedAt: at, LastUsedAt: at,
	}
	if err := st.Put(context.Background(), m); err != nil {
		t.Fatalf("put: %v", err)
	}
	return m
}

// charge runs one span through the real store the way [bill] does.
func charge(t *testing.T, st *Store, m Sandbox, now, rate int64, l *ledger) int64 {
	t.Helper()
	micros, err := cloud.RuntimeCharge(context.Background(), running(m), now, rate,
		func(ctx context.Context, id string, was, at int64) (bool, error) {
			return st.Advance(ctx, m.Org, id, was, at)
		}, l.emit)
	if err != nil {
		t.Fatalf("charge: %v", err)
	}
	return micros
}

// TestTheWatermarkSurvivesTheStore is the whole reason the column exists: a lease
// billed through an instant must read back billed through that instant, so a
// restart resumes rather than re-charging.
//
// MUTATION: drop `metered_at` from selectCols and this reads 0 and re-bills the
// span on the next tick.
func TestTheWatermarkSurvivesTheStore(t *testing.T) {
	st, ctx := memStore(t), context.Background()
	const start, now = 1_800_000_000, 1_800_003_600
	lease(t, st, "m_1", "acme", "acme", start)

	won, err := st.Advance(ctx, "acme", "m_1", start, now)
	if err != nil || !won {
		t.Fatalf("advance: won=%v err=%v", won, err)
	}
	back, err := st.Get(ctx, "acme", "m_1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if back.MeteredAt != now {
		t.Fatalf("the watermark did not persist: %d, want %d", back.MeteredAt, now)
	}
	if back.Payer != "acme" {
		t.Fatalf("the payer did not persist: %q", back.Payer)
	}
}

// TestOnlyOneSweeperOwnsASpan drives the REAL SQL compare-and-set from 25
// goroutines. Two pods, a sweep racing an end-of-lease, a retry after a timeout —
// all read the same watermark, and exactly one may charge for the span.
//
// MUTATION: drop `AND metered_at=?` from Advance's WHERE clause and this fails with
// 25 debits for one hour of one sandbox.
func TestOnlyOneSweeperOwnsASpan(t *testing.T) {
	st, l := memStore(t), &ledger{}
	const start, now = 1_800_000_000, 1_800_003_600
	m := lease(t, st, "m_1", "acme", "acme", start)

	var wg sync.WaitGroup
	for range 25 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = cloud.RuntimeCharge(context.Background(), running(m), now, cloud.RuntimeHourMicros,
				func(ctx context.Context, id string, was, at int64) (bool, error) {
					return st.Advance(ctx, m.Org, id, was, at)
				}, l.emit)
		}()
	}
	wg.Wait()

	if n := l.len(); n != 1 {
		t.Fatalf("25 sweepers over one span produced %d debits, want exactly 1", n)
	}
	if got := l.micros(); got != cloud.RuntimeHourMicros {
		t.Fatalf("one held hour billed %d µ$, want %d", got, cloud.RuntimeHourMicros)
	}
	if l.who[0] != "acme" {
		t.Fatalf("the debit went to %q, not the lease's payer", l.who[0])
	}
}

// TestTheDebitFollowsThePayerAndNotTheOrg. A platform SuperAdmin leasing inside
// somebody else's org spends their OWN wallet — that is what platform sudo means —
// so a recurring charge keyed on the row's org would bill the tenant being
// inspected for the operator inspecting them.
//
// MUTATION: build cloud.Running with m.Org instead of m.Payer and this fails
// naming the wrong wallet.
func TestTheDebitFollowsThePayerAndNotTheOrg(t *testing.T) {
	st, l := memStore(t), &ledger{}
	const start, now = 1_800_000_000, 1_800_003_600
	m := lease(t, st, "m_1", "acme", "hanzo/z", start) // operator's wallet, tenant's org

	if got := charge(t, st, m, now, cloud.RuntimeHourMicros, l); got != cloud.RuntimeHourMicros {
		t.Fatalf("billed %d µ$, want %d", got, cloud.RuntimeHourMicros)
	}
	if l.who[0] != "hanzo/z" {
		t.Fatalf("the operator's hour was billed to %q, want the operator's own wallet", l.who[0])
	}
}

// TestALeaseTheMeterHasNeverSeenStartsItsClock — a row written before this meter
// existed carries no watermark and no payer, and neither is guessed.
//
// MUTATION: default MeteredAt to CreatedAt in the migration and every lease open at
// rollout is billed for its whole life on the first tick.
func TestALeaseTheMeterHasNeverSeenStartsItsClock(t *testing.T) {
	st, l, ctx := memStore(t), &ledger{}, context.Background()
	const now = 1_800_000_000
	// The pre-meter shape: created long ago, never billed, no payer recorded.
	m := Sandbox{ID: "m_old", Org: "acme", Kind: KindSandbox, Class: "dev", Status: "running",
		CreatedAt: now - 30*86400, LastUsedAt: now - 30*86400}
	if err := st.Put(ctx, m); err != nil {
		t.Fatalf("put: %v", err)
	}
	if got := charge(t, st, m, now, cloud.RuntimeHourMicros, l); got != 0 || l.len() != 0 {
		t.Fatalf("a lease from before the meter was billed %d µ$ in %d rows, want nothing", got, l.len())
	}
}

// TestALeaseThatNeverCameUpIsNotBilled. The lease fee is recorded only once the
// sandbox runs, "metering a lease that failed to start would bill for a pod nobody
// got"; runtime obeys the same rule through the same predicate.
//
// MUTATION: delete the `holding` check in bill and an errored lease bills forever,
// because nothing reaps a row in that status.
func TestALeaseThatNeverCameUpIsNotBilled(t *testing.T) {
	st, l, ctx := memStore(t), &ledger{}, context.Background()
	const start, now = 1_800_000_000, 1_800_003_600
	m := lease(t, st, "m_1", "acme", "acme", start)
	m.Status = "error"
	m.Error = "image pull backoff"
	if err := st.Put(ctx, m); err != nil {
		t.Fatalf("put: %v", err)
	}
	bill(&Service{Base: cloud.Base{Log: luxlog.NewNoOpLogger()}}, ctx, st, m)
	if l.len() != 0 {
		t.Fatalf("a lease that never came up was billed %d times", l.len())
	}
	// And it is the SAME predicate the capacity count uses, so occupancy and money
	// cannot disagree about what a held sandbox is.
	if n, err := st.LiveOfClass(ctx, "acme", "dev"); err != nil || n != 0 {
		t.Fatalf("occupancy counts %d errored leases, money counts 0 — they must agree (err %v)", n, err)
	}
}

// TestHeldIsNotTruncated. List is LIMIT 200; a set money is computed over may not
// be. Past row 200 a truncated read does not mean a shorter page, it means free
// runtime.
//
// MUTATION: point Held at List and this fails at 201.
func TestHeldIsNotTruncated(t *testing.T) {
	st, ctx := memStore(t), context.Background()
	const n = 250
	for i := range n {
		lease(t, st, "m_"+string(rune('a'+i%26))+string(rune('a'+i/26)), "acme", "acme", 1_800_000_000)
	}
	got, err := st.Held(ctx, "acme")
	if err != nil {
		t.Fatalf("held: %v", err)
	}
	if len(got) != n {
		t.Fatalf("Held returned %d of %d leases — the rest would run free", len(got), n)
	}
	if page, err := st.List(ctx, "acme", "", "running"); err != nil || len(page) != 200 {
		t.Fatalf("the control failed: List should still be the 200-row page, got %d (err %v)", len(page), err)
	}
}

// TestABusyLeaseIsExtended is the reaper's own mechanism, and it had never run: the
// UPDATE named a table (`sandboxes`) this schema has never had, so every call
// errored into a Warn line and ExpiresAt never moved after the lease was taken.
// The cost was the failure `extendWhenUnder`/`extendBy` exist to prevent — a long
// build reaped at its TTL while somebody was working in it.
//
// MUTATION: put the `es` back on the table name and this fails naming the error.
func TestABusyLeaseIsExtended(t *testing.T) {
	st, ctx := memStore(t), context.Background()
	const start = 1_800_000_000
	m := lease(t, st, "m_1", "acme", "acme", start)
	m.ExpiresAt = start + 900
	if err := st.Put(ctx, m); err != nil {
		t.Fatalf("put: %v", err)
	}
	if err := st.Extend(ctx, "acme", "m_1", start+3600); err != nil {
		t.Fatalf("extend: %v", err)
	}
	back, err := st.Get(ctx, "acme", "m_1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if back.ExpiresAt != start+3600 {
		t.Fatalf("the lease was not extended: expiresAt %d, want %d", back.ExpiresAt, start+3600)
	}
	// Monotonic: a stale caller may not pull a lease in and kill a sandbox early.
	if err := st.Extend(ctx, "acme", "m_1", start+60); err != nil {
		t.Fatalf("extend backwards: %v", err)
	}
	if back, _ = st.Get(ctx, "acme", "m_1"); back.ExpiresAt != start+3600 {
		t.Fatalf("a stale extend moved the lease backwards to %d", back.ExpiresAt)
	}
}

// TestAFreeHourStillMovesTheClock — a published price of zero is a PRICE, and the
// span it covers has to be accounted for or restoring the price bills it all again.
//
// The promo week, seen from the tenant's side: runtime is free Monday to Friday, an
// operator retunes the rate on Saturday, and every lease held through the promotion
// is charged for all of it at the new rate, in one debit.
//
// The two end-of-lease paths never had this — they call [bill] unconditionally —
// which is why the sweep was the one place it hid.
//
// MUTATION: restore `if rate <= 0 { return }` at the top of meterRuntime and the
// watermark never leaves the start of the promotion.
func TestAFreeHourStillMovesTheClock(t *testing.T) {
	ctx := context.Background()
	stores := cloud.NewOrgStore[*Store](cloud.Base{DataDir: t.TempDir()}, "sandbox", openStore)
	t.Cleanup(func() { _ = stores.CloseAll() })
	s := &Service{Base: cloud.Base{Log: luxlog.NewNoOpLogger()}, State: state{stores: stores}}

	st, err := storeFor(s, "acme")
	if err != nil {
		t.Fatalf("storeFor: %v", err)
	}
	start := time.Now().Unix() - 5*3600
	m := lease(t, st, "m_free", "acme", "acme", start)

	paid := cloud.RuntimeRate
	t.Cleanup(func() { cloud.RuntimeRate = paid })
	cloud.RuntimeRate = func(context.Context) int64 { return 0 }
	meterRuntime(ctx, s)
	cloud.RuntimeRate = paid

	after, err := st.Get(ctx, m.Org, m.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if after.MeteredAt <= start {
		t.Fatalf("five free hours left the watermark at %d (the lease was taken at %d): the day the "+
			"price returns, all five are billed at the new rate", after.MeteredAt, start)
	}

	// The price returns. The next span may cost only what has run SINCE.
	l := &ledger{}
	if got := charge(t, st, after, after.MeteredAt+3600, cloud.RuntimeHourMicros, l); got != cloud.RuntimeHourMicros {
		t.Fatalf("the first paid hour billed %d µ$, want %d — the free hours are in there", got, cloud.RuntimeHourMicros)
	}
}
