package agents

import (
	"context"
	"sync"
	"testing"
	"time"
)

func mkRun(org, target, sess string) RoutedRun {
	return RoutedRun{Org: org, TargetID: target, SessionID: sess, Repo: "api", Branch: "agent/" + sess}
}

// A claimed run comes back to exactly one claimer, then its report reaches the
// offerer that is awaiting it.
func TestMailbox_OfferClaimReport(t *testing.T) {
	m := newMailbox()
	off := m.Offer(mkRun("acme", "tgt_1", "sess_1"))

	got, ok := m.Claim(context.Background(), "acme", "tgt_1")
	if !ok || got.SessionID != "sess_1" {
		t.Fatalf("claim wrong: ok=%v run=%+v", ok, got)
	}

	done := make(chan RoutedResult, 1)
	go func() {
		res, _ := off.Await(context.Background())
		done <- res
	}()
	if !m.Report("acme", "tgt_1", "sess_1", RoutedResult{OK: true, CommitSha: "abc"}) {
		t.Fatal("report should deliver to the awaiting offer")
	}
	select {
	case res := <-done:
		if !res.OK || res.CommitSha != "abc" {
			t.Fatalf("await got wrong result: %+v", res)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("await never received the reported result")
	}
}

// THE tenant + machine boundary: a claim for (org,target) can NEVER surface a run
// offered for a different org OR a different target — it is a property of the key.
func TestMailbox_CrossTenantAndCrossMachineIsolation(t *testing.T) {
	m := newMailbox()
	m.Offer(mkRun("orgB", "tgt_Y", "sess_foreign_org"))
	m.Offer(mkRun("acme", "tgt_Y", "sess_foreign_machine"))
	m.Offer(mkRun("acme", "tgt_X", "sess_mine"))

	// A claim for (acme, tgt_X) gets ONLY acme/tgt_X's run.
	got, ok := m.Claim(context.Background(), "acme", "tgt_X")
	if !ok || got.SessionID != "sess_mine" {
		t.Fatalf("claim leaked across a boundary: ok=%v run=%+v", ok, got)
	}
	// And that queue is now empty — no foreign run fell through.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, ok := m.Claim(ctx, "acme", "tgt_X"); ok {
		t.Fatal("a foreign run must never be claimable as acme/tgt_X")
	}

	// A Report can only complete a run under its exact key: reporting the foreign
	// machine's session under tgt_X does nothing.
	if m.Report("acme", "tgt_X", "sess_foreign_machine", RoutedResult{OK: true}) {
		t.Fatal("report crossed the machine boundary")
	}
	if m.Report("acme", "tgt_Y", "sess_foreign_org", RoutedResult{OK: true}) {
		t.Fatal("report crossed the org boundary")
	}
}

// Two racing claimers, one run: exactly one wins.
func TestMailbox_NoDoubleClaim(t *testing.T) {
	m := newMailbox()
	m.Offer(mkRun("acme", "tgt_1", "sess_1"))

	var wins int
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()
			if _, ok := m.Claim(ctx, "acme", "tgt_1"); ok {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("exactly one claimer must win, got %d", wins)
	}
}

// A claim with no work times out on ctx and reports no run — fail closed, never hang.
func TestMailbox_ClaimTimesOut(t *testing.T) {
	m := newMailbox()
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	if _, ok := m.Claim(ctx, "acme", "tgt_empty"); ok {
		t.Fatal("an empty mailbox must not yield a run")
	}
}

// The durable owner's Await unblocks (fail-closed) when its budget ctx fires with
// no report — the machine never claimed, or claimed and died.
func TestMailbox_AwaitFailsClosedOnDeadline(t *testing.T) {
	m := newMailbox()
	off := m.Offer(mkRun("acme", "tgt_1", "sess_1"))
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	if _, ok := off.Await(ctx); ok {
		t.Fatal("await must fail closed when the deadline fires without a report")
	}
}

// A re-offer of the same run (workflow retry / cloud restart) supersedes the stale
// offer: the old waiter unblocks abandoned, and the fresh run is claimable.
func TestMailbox_ReOfferSupersedes(t *testing.T) {
	m := newMailbox()
	old := m.Offer(mkRun("acme", "tgt_1", "sess_1"))

	// re-offer BEFORE anyone claims the first
	fresh := m.Offer(mkRun("acme", "tgt_1", "sess_1"))

	// old is abandoned
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, ok := old.Await(ctx); ok {
		t.Fatal("the superseded offer must not complete")
	}

	// exactly one claimable run remains, and reporting reaches the fresh offer
	got, ok := m.Claim(context.Background(), "acme", "tgt_1")
	if !ok || got.SessionID != "sess_1" {
		t.Fatalf("fresh run not claimable: %+v", got)
	}
	if _, ok := m.Claim(ctxShort(), "acme", "tgt_1"); ok {
		t.Fatal("the stale offer must not have left a duplicate in the queue")
	}
	done := make(chan struct{})
	go func() { fresh.Await(context.Background()); close(done) }()
	if !m.Report("acme", "tgt_1", "sess_1", RoutedResult{OK: true}) {
		t.Fatal("report must reach the fresh offer")
	}
	<-done
}

// Report for an unknown/already-finished run is a clean false.
func TestMailbox_ReportUnknownIsNoOp(t *testing.T) {
	m := newMailbox()
	if m.Report("acme", "tgt_1", "nope", RoutedResult{OK: true}) {
		t.Fatal("report for an unknown run must be a no-op")
	}
}

func ctxShort() context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	_ = cancel
	return ctx
}
