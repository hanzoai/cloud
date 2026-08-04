package label

// deliver_test.go is about the one way a durable record still ends up missing
// from the answer key: it is recorded, the derived copy does not take it, and
// nothing ever tries again.
//
// That is not a hypothetical. The first cut of this plane mirrored only the rows
// that LANDED in a given request, and a redelivery of a fact already recorded
// lands nothing — so a warehouse outage produced a hole that no retry, from any
// caller, could ever close. A training join would then be missing exactly the
// labels that arrived during the outage, and a missing fraud label is
// indistinguishable from an honest customer. These tests pin the repair.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/apps/tenant"
	"github.com/zap-proto/zip"
)

// recorder stands in for the warehouse and remembers what it was asked to take.
type recorder struct {
	mu    sync.Mutex
	down  error
	sent  [][]string
	swept [][]string
}

func (r *recorder) plane() columnar {
	return columnar{
		send: func(_ context.Context, _ tenant.Key, facts []Fact) error {
			r.mu.Lock()
			defer r.mu.Unlock()
			if r.down != nil {
				return r.down
			}
			ids := make([]string, 0, len(facts))
			for _, f := range facts {
				ids = append(ids, f.ID)
			}
			r.sent = append(r.sent, ids)
			return nil
		},
		sweep: func(_ context.Context, _ tenant.Key, ids []string) error {
			r.mu.Lock()
			defer r.mu.Unlock()
			if r.down != nil {
				return r.down
			}
			r.swept = append(r.swept, ids)
			return nil
		},
	}
}

func (r *recorder) took() map[string]bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := map[string]bool{}
	for _, batch := range r.sent {
		for _, id := range batch {
			out[id] = true
		}
	}
	return out
}

func (r *recorder) stop(err error) { r.mu.Lock(); r.down = err; r.mu.Unlock() }
func (r *recorder) start()         { r.mu.Lock(); r.down = nil; r.mu.Unlock() }

// TestARedeliveryRepairsAWarehouseThatWasDown is the regression that named the
// mechanism.
//
// A webhook files a dispute while the warehouse is unreachable. The record is
// durable — that part always worked — but the derived copy does not have it. The
// webhook then redelivers, as webhooks do, and every fact resolves to
// `duplicate`: nothing new lands. Delivery keyed on what landed in THIS request
// therefore never sends the row again, and the hole is permanent.
//
// Delivery is a property of the store's cursor instead, so the redelivery carries
// the earlier row across as well.
func TestARedeliveryRepairsAWarehouseThatWasDown(t *testing.T) {
	w := &recorder{}
	app, _ := wireWith(t, "", w.plane())
	at := time.Now().UTC().Add(-200 * 24 * time.Hour).Truncate(time.Second)
	one := batch(assertion("transaction", "tx-1", at, at.Add(time.Hour), Productive, Dispute, "dp-1", 1))

	w.stop(errors.New("the warehouse is not connected"))
	first := post(t, app, "acme", "u_acme", one)
	if first.Recorded != 1 {
		t.Fatalf("the record did not land: %+v", first)
	}
	if first.Mirror == "" {
		t.Fatal("the derived copy was reported as taken while the warehouse was down")
	}
	if first.Pending != 1 {
		t.Fatalf("pending %d, want the one undelivered assertion", first.Pending)
	}
	id := first.Results[0].ID

	// The warehouse comes back. The SAME batch is redelivered: nothing new lands.
	w.start()
	again := post(t, app, "acme", "u_acme", one)
	if again.Recorded != 0 || again.Duplicate != 1 {
		t.Fatalf("the redelivery was not idempotent: %+v", again)
	}
	if again.Mirror != "" {
		t.Fatalf("the derived copy refused a healthy write: %s", again.Mirror)
	}
	if again.Pending != 0 {
		t.Fatalf("pending %d after the repair, want none", again.Pending)
	}
	if !w.took()[id] {
		t.Fatal("the row recorded during the outage never reached the derived copy; the answer key is missing a fraud label and nothing says so")
	}
}

// TestAnOutageIsCarriedForwardByTheNextWrite. The other half: the caller never
// retries the failed batch, it simply files the next one. The backlog rides
// along, because delivery reads the store and not the request.
func TestAnOutageIsCarriedForwardByTheNextWrite(t *testing.T) {
	w := &recorder{}
	app, _ := wireWith(t, "", w.plane())
	at := time.Now().UTC().Add(-200 * 24 * time.Hour).Truncate(time.Second)

	w.stop(errors.New("the warehouse is not connected"))
	lost := post(t, app, "acme", "u_acme", batch(
		assertion("transaction", "tx-lost", at, at.Add(time.Hour), Productive, Dispute, "dp-lost", 1)))

	w.start()
	next := post(t, app, "acme", "u_acme", batch(
		assertion("transaction", "tx-next", at, at.Add(2*time.Hour), Productive, Dispute, "dp-next", 1)))

	took := w.took()
	if !took[lost.Results[0].ID] {
		t.Fatal("the assertion filed during the outage was never delivered")
	}
	if !took[next.Results[0].ID] {
		t.Fatal("the assertion that triggered the delivery was not itself delivered")
	}
	if next.Pending != 0 {
		t.Fatalf("pending %d after both were sent, want none", next.Pending)
	}
}

// TestPendingIsExactAndNotMerelyASecond.
//
// A whole batch shares one write second, so a mark holding only the second cannot
// say whether that second is finished: it either re-sends the batch on every call
// or steps over the members it never sent. The first shape reports a permanent
// phantom backlog an operator learns to ignore; the second drops labels. The
// store's own position is exact.
func TestPendingIsExactAndNotMerelyASecond(t *testing.T) {
	w := &recorder{}
	app, s := wireWith(t, "", w.plane())
	at := time.Now().UTC().Add(-200 * 24 * time.Hour).Truncate(time.Second)

	facts := make([]string, 0, 8)
	for i := range 8 {
		facts = append(facts, assertion("transaction", fmt.Sprintf("tx-%d", i),
			at, at.Add(time.Duration(i)*time.Hour), Productive, Dispute, fmt.Sprintf("dp-%d", i), 1))
	}
	out := post(t, app, "acme", "u_acme", batch(facts...))
	if out.Recorded != 8 {
		t.Fatalf("recorded %d of 8", out.Recorded)
	}
	if out.Pending != 0 {
		t.Fatalf("pending %d immediately after a whole batch was delivered, want none — the mark cannot resolve a single second", out.Pending)
	}

	// And the cursor really did stop inside that second rather than past it: a
	// store-level drain with a limit smaller than the batch must hand back the
	// remainder, never skip it.
	st := storeOf(t, s, "acme")
	ctx := t.Context()
	first, err := st.undelivered(ctx, 0, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 3 {
		t.Fatalf("a bounded drain read %d rows, want 3", len(first))
	}
	cur := cursor(first[2].Seq)
	rest, err := st.undelivered(ctx, cur, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(rest) != 5 {
		t.Fatalf("the remainder of the same second is %d rows, want 5 — the cursor stepped over rows it never sent", len(rest))
	}
	n, err := st.pending(ctx, cur, maxPending)
	if err != nil {
		t.Fatal(err)
	}
	if n != 5 {
		t.Fatalf("pending reports %d, want 5", n)
	}
	seen := map[string]bool{}
	for _, f := range append(append([]Fact{}, first...), rest...) {
		if seen[f.ID] {
			t.Fatalf("%s was read twice across the cut", f.ID)
		}
		seen[f.ID] = true
	}
	if len(seen) != 8 {
		t.Fatalf("the two halves cover %d rows of 8", len(seen))
	}
}

// TestTheCursorNeverMovesBackwards. Delivery is bounded and repeated, so an
// attempt that read an older batch must not un-deliver a newer one — a mark that
// could regress would re-send an unbounded history on every call and, worse,
// would make "pending" a number that goes up after a success.
func TestTheCursorNeverMovesBackwards(t *testing.T) {
	w := &recorder{}
	_, s := wireWith(t, "", w.plane())
	st := storeOf(t, s, "acme")
	ctx := t.Context()

	if err := st.advance(ctx, 2000); err != nil {
		t.Fatal(err)
	}
	for _, older := range []cursor{1, 1999} {
		if err := st.advance(ctx, older); err != nil {
			t.Fatal(err)
		}
		got, err := st.mark(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if got != 2000 {
			t.Fatalf("advancing to %d moved the mark back to %d", older, got)
		}
	}
	if err := st.advance(ctx, 2001); err != nil {
		t.Fatal(err)
	}
	got, _ := st.mark(ctx)
	if got != 2001 {
		t.Fatalf("the mark did not advance: %d", got)
	}
}

// TestTheCursorCannotStepOverAWriteItNeverSaw is the regression for a permanent,
// silent hole in the answer key.
//
// The mark was the pair (wrote, id): a write second and a CONTENT DIGEST. That is
// not the order rows commit in. Two writes land in one second — an ordinary batch,
// or two concurrent requests — and whichever digest sorts higher sets the mark; a
// row that commits afterwards inside the same second with a lower digest is
// already BEHIND it. The mark only moves forward, so no retry can ever reach that
// row, and pending() answers zero because it asks the same predicate. A missing
// fraud label reads exactly like an honest customer.
//
// Reproduced here with no concurrency at all, which is the point: the defect is
// not a race that needs winning, it is an ORDER that was never the writer's. Two
// deliveries, one write second, the second row's digest chosen to sort below the
// first's.
func TestTheCursorCannotStepOverAWriteItNeverSaw(t *testing.T) {
	w := &recorder{}
	_, s := wireWith(t, "", w.plane())
	st := storeOf(t, s, "acme")
	ctx := t.Context()
	tn, err := tenant.Mint("hanzo", "acme")
	if err != nil {
		t.Fatal(err)
	}

	// One write second, shared by both assertions.
	second := time.Now().UTC().Add(-200 * 24 * time.Hour).Truncate(time.Second)
	at := second.Add(-24 * time.Hour)
	var high, low Fact
	for i := range 200 {
		f, err := admit(Fact{Kind: KindTransaction, Subject: fmt.Sprintf("tx-%03d", i),
			At: at, Seen: at, Disposition: Productive, Source: Dispute,
			Evidence: fmt.Sprintf("dp-%03d", i), By: "svc", Confidence: 1}, second)
		if err != nil {
			t.Fatal(err)
		}
		if high.ID == "" || f.ID > high.ID {
			high = f
		}
		if low.ID == "" || f.ID < low.ID {
			low = f
		}
	}
	if high.ID == low.ID || !high.Wrote.Equal(low.Wrote) {
		t.Fatalf("the fixture is wrong: need two facts in one write second with different digests")
	}

	// The first commits and is delivered. The mark is now at its position.
	if _, err := st.record(ctx, high); err != nil {
		t.Fatal(err)
	}
	if _, _, err := deliver(ctx, tn, st, w.plane()); err != nil {
		t.Fatal(err)
	}
	if !w.took()[high.ID] {
		t.Fatal("the first assertion was not delivered")
	}

	// The second commits inside the SAME second, with a lower digest.
	if _, err := st.record(ctx, low); err != nil {
		t.Fatal(err)
	}
	at2, err := st.mark(ctx)
	if err != nil {
		t.Fatal(err)
	}
	n, err := st.pending(ctx, at2, maxPending)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("pending reports %d after a write the delivery never saw, want 1 — the cursor stepped over it and no retry can reach it", n)
	}
	if _, _, err := deliver(ctx, tn, st, w.plane()); err != nil {
		t.Fatal(err)
	}
	if !w.took()[low.ID] {
		t.Fatal("the second assertion never reached the derived copy: the answer key is missing a fraud label and pending said zero")
	}
}

// TestConcurrentWritesAreAllDelivered is the same defect through the real router,
// which is where it was measured: forty single-label POSTs from one org, landing
// inside the same write seconds, each one triggering a delivery. Every recorded
// assertion must reach the derived copy, and the plane must not report itself
// caught up while any of them has not.
func TestConcurrentWritesAreAllDelivered(t *testing.T) {
	w := &recorder{}
	app, s := wireWith(t, "", w.plane())
	at := time.Now().UTC().Add(-200 * 24 * time.Hour).Truncate(time.Second)

	// One store open before the storm, so the first request is not the one that
	// creates the file.
	_ = storeOf(t, s, "acme")

	const n = 40
	ids := make([]string, n)
	var wg sync.WaitGroup
	wg.Add(n)
	for i := range n {
		go func(i int) {
			defer wg.Done()
			out := post(t, app, "acme", "u_acme", batch(assertion("transaction",
				fmt.Sprintf("tx-%02d", i), at, at.Add(time.Duration(i)*time.Minute),
				Productive, Dispute, fmt.Sprintf("dp-%02d", i), 1)))
			if len(out.Results) == 1 {
				ids[i] = out.Results[0].ID
			}
		}(i)
	}
	wg.Wait()

	// One more delivery, so nothing is blamed on a request that simply raced to
	// the end: if a row is still missing after this, the cursor is past it and
	// NOTHING can ever send it.
	st := storeOf(t, s, "acme")
	tn, err := tenant.Mint("hanzo", "acme")
	if err != nil {
		t.Fatal(err)
	}
	if _, pending, err := deliver(t.Context(), tn, st, w.plane()); err != nil || pending != 0 {
		t.Fatalf("a final delivery left %d pending: %v", pending, err)
	}

	took := w.took()
	missing := 0
	for i, id := range ids {
		if id == "" {
			t.Fatalf("request %d recorded nothing", i)
		}
		if !took[id] {
			missing++
		}
	}
	if missing != 0 {
		t.Fatalf("%d of %d durable assertions never reached the derived copy, and the plane reports itself caught up", missing, n)
	}
}

// TestOneTenantsDeliveryIsItsOwn. The cursor lives in the tenant's own file, so
// a neighbour's outage cannot advance past this tenant's rows and a neighbour's
// backlog cannot be counted into this tenant's answer.
func TestOneTenantsDeliveryIsItsOwn(t *testing.T) {
	w := &recorder{}
	app, s := wireWith(t, "", w.plane())
	at := time.Now().UTC().Add(-200 * 24 * time.Hour).Truncate(time.Second)

	w.stop(errors.New("down"))
	post(t, app, "acme", "u_acme", batch(
		assertion("transaction", "tx-a", at, at.Add(time.Hour), Productive, Dispute, "dp-a", 1)))
	w.start()
	post(t, app, "globex", "u_globex", batch(
		assertion("transaction", "tx-g", at, at.Add(time.Hour), Productive, Dispute, "dp-g", 1)))

	acme, err := storeOf(t, s, "acme").pending(t.Context(), cursorOf(t, s, "acme"), maxPending)
	if err != nil {
		t.Fatal(err)
	}
	if acme != 1 {
		t.Fatalf("acme reports %d pending; globex's successful delivery moved acme's mark", acme)
	}
	globex, err := storeOf(t, s, "globex").pending(t.Context(), cursorOf(t, s, "globex"), maxPending)
	if err != nil {
		t.Fatal(err)
	}
	if globex != 0 {
		t.Fatalf("globex reports %d pending; acme's outage was counted into it", globex)
	}
}

// ── the bounded reads ────────────────────────────────────────────────────────

// TestAReadTooWideIsRefusedRatherThanTruncated.
//
// Neither read can be paged. A precedence rule applied to a truncated assertion
// set returns a confident WRONG winner, and a coverage number over a truncated
// window understates what is judged — both are answers that look fine and are
// not. So both refuse, and the refusal is a 422 the caller can act on rather
// than a 500 that says the plane is broken.
func TestAReadTooWideIsRefusedRatherThanTruncated(t *testing.T) {
	w := &recorder{}
	app, s := wireWith(t, "", w.plane())
	at := time.Now().UTC().Add(-200 * 24 * time.Hour).Truncate(time.Second)
	facts := make([]string, 0, 4)
	for i := range 4 {
		facts = append(facts, assertion("transaction", fmt.Sprintf("tx-%d", i),
			at, at.Add(time.Duration(i)*time.Hour), Productive, Dispute, fmt.Sprintf("dp-%d", i), 1))
	}
	post(t, app, "acme", "u_acme", batch(facts...))

	st := storeOf(t, s, "acme")
	ctx := t.Context()
	if _, err := st.window(ctx, at.Add(-time.Hour), at.Add(time.Hour), 2); !errors.Is(err, errTooWide) {
		t.Fatalf("a window over the bound returned %v, want errTooWide", err)
	}
	if _, err := st.window(ctx, at.Add(-time.Hour), at.Add(time.Hour), 100); err != nil {
		t.Fatalf("a window inside the bound was refused: %v", err)
	}
	want := []Fact{
		{Kind: KindTransaction, Subject: "tx-0", At: at},
		{Kind: KindTransaction, Subject: "tx-1", At: at},
		{Kind: KindTransaction, Subject: "tx-2", At: at},
	}
	if _, err := st.forSubjects(ctx, want, 2); !errors.Is(err, errTooWide) {
		t.Fatalf("a subject set over the bound returned %v, want errTooWide", err)
	}
	got, err := st.forSubjects(ctx, want, 100)
	if err != nil || len(got) != 3 {
		t.Fatalf("a subject set inside the bound read %d rows: %v", len(got), err)
	}
	// And the op layer turns exactly that sentinel into a refusal the caller can
	// act on, never a 500.
	if code := status(readErr(fmt.Errorf("%w: x", errTooWide))); code != 422 {
		t.Fatalf("a too-wide read surfaces as %d, want 422", code)
	}
	if code := status(readErr(errors.New("disk on fire"))); code != 500 {
		t.Fatalf("a store failure surfaces as %d, want 500", code)
	}
}

// ── the two-phase disposal ───────────────────────────────────────────────────

// TestADisposalThatCannotReachTheDerivedCopyDoesNotHappen.
//
// The derived copy is disposed of BEFORE the record, because the record's ids are
// what the columnar delete binds: remove the record first and the warehouse rows
// can no longer be named by anything. So a warehouse that cannot be reached must
// stop the whole sweep — and answer with a status that means "try again", not one
// that means "done".
//
// This is a compliance property, not an availability one. A disposal that
// reported success while leaving rows behind is a tenant told its data is gone
// when it is not.
func TestADisposalThatCannotReachTheDerivedCopyDoesNotHappen(t *testing.T) {
	w := &recorder{}
	app, s := wireWith(t, "", w.plane())
	// Written EIGHT YEARS AGO. The record has to be planted at the store rather
	// than posted, and that is the point of `wrote`: retention measures the server
	// clock at the write, never the caller's `at` or `seen`, so a tenant cannot age
	// its own compliance record out by back-dating the event it judges.
	ancient := time.Now().UTC().Add(-8 * 365 * 24 * time.Hour).Truncate(time.Second)
	f, err := admit(Fact{Kind: KindTransaction, Subject: "tx-ancient", At: ancient, Seen: ancient,
		Disposition: Productive, Source: Dispute, Evidence: "dp-1", By: "svc", Confidence: 1}, ancient)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := storeOf(t, s, "acme").record(t.Context(), f); err != nil {
		t.Fatal(err)
	}
	before := fmt.Sprintf(`{"before":%q}`, time.Now().UTC().Add(-minRetention-24*time.Hour).Format(time.RFC3339))

	w.stop(errors.New("the warehouse is not connected"))
	code, raw := req(t, app, http.MethodPost, "/v1/risk/labels/dispose", "acme", "u_acme", before)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("a disposal with no reachable warehouse = %d %s, want 503", code, raw)
	}
	if n := count(t, app, "acme"); n != 1 {
		t.Fatalf("the record was disposed of anyway: %d left, want 1", n)
	}

	// The warehouse comes back and the same sweep completes, columnar half first.
	w.start()
	code, raw = req(t, app, http.MethodPost, "/v1/risk/labels/dispose", "acme", "u_acme", before)
	if code != http.StatusOK {
		t.Fatalf("the retried disposal = %d %s, want 200", code, raw)
	}
	var out riskDisposeOut
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if out.Disposed != 1 || out.Total != 0 {
		t.Fatalf("the retried disposal removed %d and left %d: %+v", out.Disposed, out.Total, out)
	}
	if n := count(t, app, "acme"); n != 0 {
		t.Fatalf("%d records survived a successful disposal", n)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.swept) != 1 || len(w.swept[0]) != 1 {
		t.Fatalf("the columnar half was asked to dispose of %v, want exactly the one identified record", w.swept)
	}
}

// count reads how many assertions a tenant holds, over the wire.
func count(t *testing.T, app *zip.App, org string) int {
	t.Helper()
	code, raw := req(t, app, http.MethodGet, "/v1/risk/labels", org, "u_"+org, "")
	if code != http.StatusOK {
		t.Fatalf("list = %d %s", code, raw)
	}
	var out riskLabelsOut
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out.Count
}
