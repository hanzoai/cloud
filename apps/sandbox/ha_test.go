package sandbox

// TWO PODS, TWO FILES, ONE ORG — the arrangement the runtime meter is deployed
// into and the one its unit tests could not see.
//
// Every other test in this package opens ONE store and drives the meter over it.
// That harness is right about arithmetic and blind to the whole class of defect
// this file is about, because it cannot represent the hazard: two replicas each
// hold the org's sandbox.db on their OWN volume, each reads its own copy, and each
// wins its own compare-and-set on the watermark. `Advance`'s `WHERE metered_at=?`
// — the line every existing test leans on for exactly-once — serializes writers
// within a file and says nothing whatever about two of them.
//
// So each test here starts a fleet (internal/twopod), lets the election pick an
// owner, and drives the REAL meter on both replicas. Each names the MUTATION that
// makes it fail, and each mutation was run.

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/internal/twopod"
	"github.com/hanzoai/cloud/metering"
	"github.com/hanzoai/namespace"
	luxlog "github.com/luxfi/log"
)

// replica builds the sandbox service one pod runs: its own volume's stores, and a
// money sink this test can read.
func replica(t *testing.T, p *twopod.Pod) *Service {
	t.Helper()
	// The two facts that make a replica a replica: its own volume, and its own
	// durability over the shared object store. Peers says what a fleet is — more
	// than one writer — which is the whole reason the gate has anything to decide.
	t.Setenv("CLOUD_DATA_DIR", p.Dir)
	b := cloud.NewBase(cloud.Deps{Durable: p.Durable, Peers: true}, "sandbox")
	b.Log = luxlog.NewNoOpLogger()
	stores := cloud.NewOrgStore(b, "sandbox", openStore)
	t.Cleanup(func() { _ = stores.CloseAll() })
	return &Service{
		Base: cloud.Base{Log: luxlog.NewNoOpLogger()},
		State: state{
			stores: stores,
			clk:    newClocks(),
			// Set by the caller: the sink is the one thing these tests watch.
			debit: func(string, metering.Usage) {},
		},
	}
}

// occupy opens one lease on this replica's copy of the org's store, with its
// watermark already an hour behind — so the very next sweep owes exactly one hour
// and any second debit for it is visible as a doubling rather than as a rounding.
func occupy(t *testing.T, s *Service, org, id string, at int64) Sandbox {
	t.Helper()
	st, err := storeFor(s, org)
	if err != nil {
		t.Fatalf("storeFor(%s): %v", org, err)
	}
	m := Sandbox{
		ID: id, Org: org, Kind: KindSandbox, Class: "dev", Status: "running",
		Payer: org, MeteredAt: at, CreatedAt: at, LastUsedAt: at,
		ExpiresAt: at + 24*3600,
	}
	if err := st.Put(context.Background(), m); err != nil {
		t.Fatalf("put %s: %v", id, err)
	}
	return m
}

// TestOneSpanIsOneDebitOnTwoPods is the whole finding.
//
// Both replicas hold the org's store. Both run the meter — which is what the
// deployment does, since every pod mounts every subsystem and every sweep is
// started by Mount. The span may be charged ONCE.
//
// MUTATION: delete the `if !s.State.stores.Owned(ns) { return }` line in
// meterRuntime and this fails with two debits for one hour of one sandbox — the
// customer billed twice for a lease they took once.
func TestOneSpanIsOneDebitOnTwoPods(t *testing.T) {
	f := twopod.New(t, "pod-a", "pod-b")
	const org = "acme"
	hour := time.Now().Unix() - 3600

	owner, other := replica(t, f.Owner(org)), replica(t, f.Other(org))
	books := &ledger{}
	owner.State.debit = books.emit
	other.State.debit = books.emit

	// The lease is taken on the OWNER, which is where a request routes. The
	// non-owner then opens the same org — a hydrate, read-only — exactly as it does
	// the moment anything on that pod touches the org.
	occupy(t, owner, org, "m_one", hour)
	ship(t, owner, org)
	theirs, err := other.State.stores.For(mustNS(t, org))
	if err != nil {
		t.Fatalf("the non-owner could not open the org: %v", err)
	}

	meterRuntime(context.Background(), owner)
	meterRuntime(context.Background(), other)

	if n := books.len(); n != 1 {
		t.Fatalf("two pods swept one org holding one lease and produced %d debits, want 1: %v",
			n, books.rows)
	}
	if got, want := books.micros(), cloud.RuntimeHourMicros; got != want {
		t.Fatalf("one held hour billed %d µ$, want %d", got, want)
	}

	// AND THE NON-OWNER DID NOT EVEN CLAIM THE SPAN. The count above holds for two
	// independent reasons — the gate declines the pass, and the ship would refuse
	// the debit anyway — so it cannot tell which of them is working. This can: an
	// ungated pass wins the CAS on its OWN copy of the file (nothing about that
	// write is fenced) and leaves the watermark moved. That is the state a later
	// promotion has to discard, and the state a reader of the wrong replica reports
	// billing from.
	m, err := theirs.Get(context.Background(), org, "m_one")
	if err != nil {
		t.Fatalf("the non-owner's copy: %v", err)
	}
	if m.MeteredAt != hour {
		t.Fatalf("a replica that does not own the org moved its own watermark from %d to %d — "+
			"it swept an org it was told it does not bill", hour, m.MeteredAt)
	}
}

// TestANonOwnerNeitherBillsNorReaps. The gate is not only about money: `end` stops
// a POD, and a pod that has been deleted does not come back. A file write can be
// fenced after the fact — the loser's ship is refused — but a tenant's sandbox
// cannot be un-deleted, so the non-owner must not reach the act at all.
//
// MUTATION: delete the Owned gate in `sweep` and the non-owner ends a lease that
// is an hour past its expiry on a pod it does not own.
func TestANonOwnerNeitherBillsNorReaps(t *testing.T) {
	f := twopod.New(t, "pod-a", "pod-b")
	const org = "acme"
	books := &ledger{}

	owner := replica(t, f.Owner(org))
	other := replica(t, f.Other(org))
	owner.State.debit, other.State.debit = books.emit, books.emit

	// A lease whose time is UP: taken two hours ago on a one-hour lease. Anything
	// that sweeps it will try to end it.
	past := time.Now().Unix() - 2*3600
	m := occupy(t, owner, org, "m_over", past)
	m.ExpiresAt = past + 3600
	st, err := storeFor(owner, org)
	if err != nil {
		t.Fatalf("storeFor: %v", err)
	}
	if err := st.Put(context.Background(), m); err != nil {
		t.Fatalf("put: %v", err)
	}

	// SHIP IT, THEN LET THE NON-OWNER HYDRATE. Both halves are load-bearing and
	// leaving either out makes this test vacuous: without the ship the durable copy
	// is empty, and without the open the non-owner's volume holds no orgs directory
	// at all — so `Each` enumerates nothing and the sweep passes by having nothing
	// to sweep rather than by declining to. That is the arrangement production is
	// NOT in: the moment anything on that pod touches the org, it has the file.
	ship(t, owner, org)
	other.State.stores.For(mustNS(t, org))

	// Now the non-owner sweeps, over a real copy of a lease that is really over. It
	// must touch nothing: no debit, and the row still standing on both copies.
	sweep(context.Background(), other)
	meterRuntime(context.Background(), other)

	if n := books.len(); n != 0 {
		t.Fatalf("a replica that does not own the org emitted %d debits: %v", n, books.rows)
	}
	if _, err := st.Get(context.Background(), org, m.ID); err != nil {
		t.Fatalf("a replica that does not own the org ended its lease: %v", err)
	}
	theirs, err := other.State.stores.For(mustNS(t, org))
	if err != nil {
		t.Fatalf("the non-owner's store: %v", err)
	}
	if _, err := theirs.Get(context.Background(), org, m.ID); err != nil {
		t.Fatalf("a replica that does not own the org deleted the row from its own copy: %v", err)
	}
}

// mustNS is the org's name, or the test fails. Every store call here takes a
// namespace and none of them is interesting.
func mustNS(t *testing.T, org string) namespace.Namespace {
	t.Helper()
	ns, err := cloud.OrgNamespace(org, "")
	if err != nil {
		t.Fatalf("namespace %s: %v", org, err)
	}
	return ns
}

// TestAnUnackedShipEmitsNothingAndTheNextOwnerBillsTheSpan is the case red named,
// and it is the one where doing the obvious thing is wrong.
//
// The owner wins the local compare-and-set — it owns the span — and THEN the
// object store cannot be reached. The watermark has moved on this pod's copy and
// is not durable. Two rules follow and they are not symmetric:
//
//   - the debit is NOT emitted, because a charge whose justification is not
//     durable can be charged again by whoever picks the org up next;
//   - the watermark is NOT rolled back, because an unacked ship is not a failed
//     ship — the object may have landed and only the answer been lost — so a
//     replica that undoes its advance on a timeout re-bills a span the durable
//     copy already accounts for. That is the double charge, arrived at by trying
//     to be careful.
//
// What makes the span safe is that the DURABLE watermark never moved. The next
// replica to open this org hydrates that copy and bills the span exactly once,
// which is what the second half of this test measures.
//
// MUTATION: emit before the ship (move the emit loop above `ship()` in
// cloud.RuntimeSweep) and the span is billed here AND again by the successor.
func TestAnUnackedShipEmitsNothingAndTheNextOwnerBillsTheSpan(t *testing.T) {
	f := twopod.New(t, "pod-a", "pod-b")
	const org = "acme"
	hour := time.Now().Unix() - 3600
	books := &ledger{}

	first := replica(t, f.Owner(org))
	first.State.debit = books.emit

	// The lease is taken, an hour ago, and SHIPPED — so the durable copy carries the
	// row and a watermark an hour behind. Shipped without sweeping, because a sweep
	// would bill that hour and settle it, and the span this test is about has to be
	// one that is still owed when the store goes away.
	occupy(t, first, org, "m_ship", hour)
	ship(t, first, org)

	// Now the store is unreachable. The sweep claims the hour locally — it wins the
	// compare-and-set, so no other replica can claim it — and cannot ship it.
	f.Store.Down()
	meterRuntime(context.Background(), first)
	if n := books.len(); n != 0 {
		t.Fatalf("a span whose watermark could not be shipped was billed %d times: %v", n, books.rows)
	}
	f.Store.Up()

	// The owner is rescheduled. Its volume goes with it; the successor opens the
	// org from the object store, which still carries the watermark of the LAST
	// ACKED sweep — so the hour that was claimed and never shipped is owed, and is
	// billed here for the first and only time.
	f.Stop(f.Owner(org).ID)
	next := replica(t, f.Owner(org))
	next.State.debit = books.emit
	if _, err := storeFor(next, org); err != nil {
		t.Fatalf("the successor could not open the org: %v", err)
	}
	meterRuntime(context.Background(), next)

	if n := books.len(); n != 1 {
		t.Fatalf("the successor billed the unshipped span %d times, want exactly 1: %v", n, books.rows)
	}
}

// ship makes this replica's copy of the org durable, and fails the test if it
// cannot — a test that quietly proceeds on an unshipped state is measuring the
// local file and calling it the fleet.
func ship(t *testing.T, s *Service, org string) {
	t.Helper()
	ns, err := cloud.OrgNamespace(org, "")
	if err != nil {
		t.Fatalf("namespace %s: %v", org, err)
	}
	acked, err := s.State.stores.Sync(ns)
	if err != nil || !acked {
		t.Fatalf("ship %s: acked=%v err=%v", org, acked, err)
	}
}

// TestAFailedStartIsReapedAndNotProtected — the sandbox that was invisible to
// every defence at once.
//
// `start` gives up WAITING for a pod; it does not stop it. So a slow image pull
// past SANDBOX_START_TIMEOUT_SEC leaves the row `error` and the pod alive, running
// the caller's code. Three separate mechanisms then look at it and each reads "not
// running" as "not there": the meter skips it (`holding` is pending|running), the
// per-class ceiling skips it for the same reason, and the ORPHAN sweep protects it
// — a pod whose id a row still claims is by definition not an orphan. Free
// compute, for as long as the node lasts, and no counter anywhere moves.
//
// The rule that closes it is in one place: an `error` row is over (clocks.over),
// so the ordinary sweep ends it and takes the pod with it.
//
// MUTATION: remove the `m.Status == "error"` clause from clocks.over and the row
// stands for ever, because no clock below it can fire on a row nothing updates.
func TestAFailedStartIsReapedAndNotProtected(t *testing.T) {
	f := twopod.New(t, "pod-a")
	const org = "acme"
	s := replica(t, f.Owner(org))
	now := time.Now().Unix()

	st, err := storeFor(s, org)
	if err != nil {
		t.Fatalf("storeFor: %v", err)
	}
	// The row a failed start leaves: minted seconds ago, so no lifetime clock is
	// anywhere near firing, and only its STATUS says it is finished.
	bad := Sandbox{
		ID: "m_failed", Org: org, Kind: KindSandbox, Class: "exec", Status: "error",
		Error: "pod m-failed not running after 2m0s: ImagePullBackOff",
		Payer: org, MeteredAt: now, CreatedAt: now, LastUsedAt: now, ExpiresAt: now + 900,
	}
	if err := st.Put(context.Background(), bad); err != nil {
		t.Fatalf("put: %v", err)
	}

	if done, why := s.State.clk.over(bad, time.Now()); !done {
		t.Fatal("a sandbox whose start failed is not over, so nothing will ever stop its pod")
	} else if why != "start-failed" {
		t.Fatalf("the reason on the row's last line is %q, want start-failed", why)
	}

	sweep(context.Background(), s)
	if _, err := st.Get(context.Background(), org, bad.ID); err == nil {
		t.Fatal("the sweep left a failed start standing — its pod outlives every clock and every counter")
	}
}

// TestTheOldestLeasesAreSweptToo. The sweep read `List`, which is
// `ORDER BY created_at DESC LIMIT 200`, so an org past two hundred sandboxes had
// its OLDEST dropped from every pass — and the oldest are the ones furthest past
// their lease. Billed for ever, ended never, and the bigger the org the more of
// them there are.
//
// It is the same defect `IDs` and `Held` were each given their own unbounded query
// to avoid; the sweep was the one that kept the page.
//
// MUTATION: put `st.List(ctx, ns.ID(), "", "running")` back in `sweep` and the
// oldest leases here are neither billed nor ended.
func TestTheOldestLeasesAreSweptToo(t *testing.T) {
	f := twopod.New(t, "pod-a")
	const org, held = "acme", 205
	books := &ledger{}
	s := replica(t, f.Owner(org))
	s.State.debit = books.emit

	st, err := storeFor(s, org)
	if err != nil {
		t.Fatalf("storeFor: %v", err)
	}
	// 205 leases, each a minute older than the last, every one an hour into its
	// span and every one an hour past a one-hour lease. The five oldest are what
	// LIMIT 200 was dropping.
	base := time.Now().Unix() - 2*3600
	for i := range held {
		at := base - int64(i)*60
		m := Sandbox{
			ID: "m_" + strconv.Itoa(i), Org: org, Kind: KindSandbox, Class: "dev",
			Status: "running", Payer: org, MeteredAt: at, CreatedAt: at,
			LastUsedAt: at, ExpiresAt: at + 3600,
		}
		if err := st.Put(context.Background(), m); err != nil {
			t.Fatalf("put %d: %v", i, err)
		}
	}

	sweep(context.Background(), s)

	// Every one of them was over, so every one is gone and every one paid its tail.
	left, err := st.Rows(context.Background(), org)
	if err != nil {
		t.Fatalf("rows: %v", err)
	}
	if len(left) != 0 {
		t.Fatalf("%d of %d leases survived a sweep that should have ended all of them — "+
			"the oldest are the ones a page drops", len(left), held)
	}
	if n := books.len(); n != held {
		t.Fatalf("%d leases produced %d debits, want %d: the ones a page drops are billed by nobody",
			held, n, held)
	}
}

// TestADepositionInsideOnePassStopsTheStop — the second fence check, and why one
// is not enough.
//
// The sweep asks who owns the org and then walks every one of its rows. A lease
// can be re-elected away inside that walk, and the answer that mattered was taken
// at the top. So `settle` asks again on the line before the pod is stopped — it is
// a lock and an HRW over the live member set, no I/O, so asking per row is free.
//
// MUTATION: delete the Owned check at the top of settle and this passes, because
// nothing between the sweep's gate and rt.stop re-reads the election.
func TestADepositionInsideOnePassStopsTheStop(t *testing.T) {
	ctx := context.Background()
	f := twopod.New(t, "pod-a")
	const org = "acme"

	a := replica(t, f.Owner(org))
	past := time.Now().Unix() - 2*3600
	m := occupy(t, a, org, "m_mid", past)
	m.ExpiresAt = past + 3600
	st, err := storeFor(a, org)
	if err != nil {
		t.Fatalf("storeFor: %v", err)
	}
	if err := st.Put(ctx, m); err != nil {
		t.Fatalf("put: %v", err)
	}
	ship(t, a, org)

	// The election moves while this replica holds the row in hand: a second pod
	// arrives and takes the org. This is the sweep's own walk, one row in.
	f.Start("pod-b")
	if f.Owner(org).ID != "pod-b" {
		t.Skip("the election kept the org here; nothing was deposed")
	}
	b := replica(t, f.Pod("pod-b"))
	if _, err := storeFor(b, org); err != nil {
		t.Fatalf("the new owner could not open the org: %v", err)
	}

	// The deposed replica now reaches settle for a row it read while it still owned
	// the org. It must not stop the pod, and it must not drop the row.
	if acked, serr := settle(a, ctx, st, mustNS(t, org), m, false); acked || serr == nil {
		t.Fatalf("a deposed replica settled a lease: acked=%v err=%v", acked, serr)
	}
	if _, err := st.Get(ctx, org, m.ID); err != nil {
		t.Fatal("a deposed replica dropped a row the new owner is responsible for")
	}
}
