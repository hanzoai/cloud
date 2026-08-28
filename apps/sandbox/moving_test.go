package sandbox

// WHAT THE FLEET DOES TO A RUNNING METER.
//
// ha_test.go proves the meter's arithmetic across two files. This one proves what
// happens when the fleet MOVES underneath it: a pod dies between a lease ending
// and its deletion shipping, the election hands an org to somebody else mid-sweep,
// the object store goes away, and a deploy changes the label a pod was created
// with. Each of those was a live defect — a customer billed for a sandbox that had
// not existed for the length of an outage, a deposed replica deleting the new
// owner's Kubernetes pods, an S3 blip leaving every expired lease holding a node
// for ever — and each test here is the reproduction that named it.
//
// They are written to FAIL if the vulnerability exists, and every one of them was
// watched failing before it was watched passing.

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/internal/twopod"
	"github.com/hanzoai/namespace"
	luxlog "github.com/luxfi/log"
)

// redSweep is meterRuntime's body with an EXPLICIT clock, so a test can ask what
// the meter bills at a chosen instant instead of waiting for one. Same store,
// same Held, same ship, same emit — nothing here is a reimplementation of the
// arithmetic, only of the calendar.
func redSweep(t *testing.T, s *Service, org string, now int64) {
	t.Helper()
	ctx := context.Background()
	_ = s.State.stores.Each(func(ns namespace.Namespace, st *Store, openErr error) {
		if openErr != nil || !s.State.stores.Owned(ns) {
			return
		}
		held, err := st.Held(ctx, ns.ID())
		if err != nil {
			t.Fatalf("held: %v", err)
		}
		rows := make([]cloud.Running, 0, len(held))
		for _, m := range held {
			rows = append(rows, running(ctx, m))
		}
		id := ns.ID()
		_, _ = cloud.RuntimeSweep(ctx, rows, now,
			func(ctx context.Context, sid string, was, at int64) (bool, error) {
				return st.Advance(ctx, id, sid, was, at)
			},
			func() (bool, error) { return s.State.stores.Sync(ns) },
			s.State.debit,
		)
	})
}

// =============================================================================
// A LEASE THAT WAS ENDED IS STILL RUNNING ON THE DURABLE COPY.
//
// End() and reap's end() both do: bill (which SHIPS the watermark), stop the pod,
// then DELETE THE ROW — and nothing ships the delete. The only Sync in the whole
// package is the meter's. So the last acked snapshot carries the lease as
// `running` with a watermark at the moment it was ended, and a successor that
// hydrates it bills from there to whenever it next sweeps, for a sandbox that has
// not existed since.
//
// This is not a double charge of one span. It is a charge for a span in which
// nothing ran, and it is UNBOUNDED — it grows with the time between the crash and
// the successor's first sweep.
// =============================================================================
func TestAnEndedLeaseStaysEnded(t *testing.T) {
	f := twopod.New(t, "pod-a", "pod-b")
	const org = "acme"
	ctx := context.Background()
	books := &ledger{}

	first := replica(t, f.Owner(org))
	first.State.debit = books.emit

	// A lease taken two hours ago, made durable.
	past := time.Now().Unix() - 2*3600
	m := occupy(t, first, org, "m_ended", past)
	ship(t, first, org)

	st, err := storeFor(first, org)
	if err != nil {
		t.Fatalf("storeFor: %v", err)
	}

	// THE CUSTOMER ENDS IT. This is api.go End's tail verbatim: retire claims the
	// tail, stops the pod (a no-op with no cluster), drops the row, ships, and bills
	// only if that ship was acknowledged.
	retire(first, ctx, st, m, false)
	billedAtEnd := books.micros()
	if billedAtEnd == 0 {
		t.Fatal("the end path billed nothing — the harness is not exercising the meter")
	}
	if _, err := st.Get(ctx, org, m.ID); err == nil {
		t.Fatal("the row survived the delete on the owner's own copy")
	}

	// THE POD DIES WITHOUT A GRACEFUL CLOSE. No CloseAll, no eviction — the case
	// the durable plane exists for. Its volume goes with it.
	f.Stop(f.Owner(org).ID)

	next := replica(t, f.Owner(org))
	next.State.debit = books.emit
	nst, err := storeFor(next, org)
	if err != nil {
		t.Fatalf("the successor could not open the org: %v", err)
	}

	// THE FINDING, part one: the ended lease is back, and it is `running`.
	back, err := nst.Get(ctx, org, m.ID)
	if err != nil {
		t.Logf("PASS: the successor does not carry the ended lease")
		return
	}
	t.Errorf("resurrection: a lease the customer ENDED is on the successor's hydrated copy as status=%q, metered_at=%d — "+
		"the delete was never shipped, so the durable snapshot still holds it", back.Status, back.MeteredAt)

	// THE FINDING, part two: it accrues. One hour after the crash, the successor's
	// ordinary sweep charges the customer a full hour for a sandbox that is gone.
	books.reset()
	redSweep(t, next, org, time.Now().Unix()+3600)
	if n := books.micros(); n > 0 {
		t.Errorf("resurrection: the successor billed %d µ$ (%d debits) for an hour AFTER the lease was ended and its pod deleted",
			n, books.len())
	}
}

// =============================================================================
// A DEPOSED OWNER GOES ON REAPING.
//
// Owned() reads Durable.owned, which is set at Hydrate and cleared ONLY when a
// Sync is refused as a stale round. Nothing re-evaluates it against membership.
// So the claim in OrgStore.Owned's own doc comment — "a pod that has lost an org
// stops sweeping it within one refresh" — has no mechanism behind it: there is no
// refresh, and the pod goes on believing it owns the org until it happens to
// attempt a ship.
//
// The reaper is the act that cannot be taken back: end() stops a KUBERNETES POD
// before anything learns the ship would be refused, and for a row that is not
// `holding` (a failed start) there is no ship at all, so the belief is never
// corrected.
// =============================================================================
func TestADeposedOwnerStopsReaping(t *testing.T) {
	ctx := context.Background()
	f := twopod.New(t, "pod-a")
	a := replica(t, f.Owner("probe"))

	// Open a spread of orgs while pod-a is the ONLY writer, so it owns them all.
	var orgs []string
	for i := range 24 {
		o := "t" + strconv.Itoa(i)
		orgs = append(orgs, o)
		past := time.Now().Unix() - 2*3600
		m := occupy(t, a, o, "m_"+o, past)
		m.ExpiresAt = past + 3600 // its lease is OVER: anything that sweeps it ends it
		st, err := storeFor(a, o)
		if err != nil {
			t.Fatalf("storeFor(%s): %v", o, err)
		}
		if err := st.Put(ctx, m); err != nil {
			t.Fatalf("put(%s): %v", o, err)
		}
		ship(t, a, o)
		if !a.State.stores.Owned(mustNS(t, o)) {
			t.Fatalf("pod-a does not own %s while it is the only writer", o)
		}
	}

	// A SECOND REPLICA ARRIVES — a scale-up, or the other half of a rolling deploy.
	f.Start("pod-b")

	// Find an org the election has moved to pod-b.
	moved := ""
	for _, o := range orgs {
		if f.Owner(o).ID == "pod-b" {
			moved = o
			break
		}
	}
	if moved == "" {
		t.Skip("the election kept every org on pod-a; nothing was deposed")
	}

	// The new owner opens it: Hydrate acquires the lease at a strictly higher
	// round, which is what deposes pod-a.
	b := replica(t, f.Pod("pod-b"))
	if _, err := storeFor(b, moved); err != nil {
		t.Fatalf("the new owner could not open %s: %v", moved, err)
	}
	if !b.State.stores.Owned(mustNS(t, moved)) {
		t.Fatalf("the elected owner of %s does not hold it", moved)
	}

	// THE FINDING: pod-a still believes it owns the org.
	if a.State.stores.Owned(mustNS(t, moved)) {
		t.Errorf("deposition: pod-a was deposed as owner of %s and Owned() still answers true — "+
			"nothing re-reads membership, so the cheap gate never closes", moved)
	}

	// And it acts on the belief: the deposed pod ends the lease and (in a cluster)
	// deletes the tenant's pod.
	sweep(ctx, a)
	ast, err := storeFor(a, moved)
	if err != nil {
		t.Fatalf("storeFor: %v", err)
	}
	if _, err := ast.Get(ctx, moved, "m_"+moved); err != nil {
		t.Errorf("deposition: the DEPOSED pod ran end() on %s — it reached rt.stop(), which deletes "+
			"a Kubernetes pod the new owner is responsible for", moved)
	}
}

// =============================================================================
// A FAILED START IS REAPED BY A DEPOSED POD WITH NO SHIP TO CORRECT IT.
//
// bill() returns immediately for a row that is not `holding`, so ending an
// `error` row performs NO Sync at all. Whatever the pod believed about ownership
// when the sweep began, it still believes when the sweep ends — the one path that
// could have refused it is never taken.
// =============================================================================
func TestAFailedStartIsReapedThroughAnOutage(t *testing.T) {
	ctx := context.Background()
	f := twopod.New(t, "pod-a", "pod-b")
	const org = "acme"
	books := &ledger{}

	owner := replica(t, f.Owner(org))
	owner.State.debit = books.emit
	now := time.Now().Unix()

	st, err := storeFor(owner, org)
	if err != nil {
		t.Fatalf("storeFor: %v", err)
	}
	bad := Sandbox{
		ID: "m_failed", Org: org, Kind: KindSandbox, Class: "exec", Status: "error",
		Error: "ImagePullBackOff", Payer: org,
		MeteredAt: now, CreatedAt: now, LastUsedAt: now, ExpiresAt: now + 900,
	}
	if err := st.Put(ctx, bad); err != nil {
		t.Fatalf("put: %v", err)
	}
	ship(t, owner, org)

	// Take the object store away entirely: no ship can succeed from here.
	f.Store.Down()
	defer f.Store.Up()

	sweep(ctx, owner)

	// THE FENCE IS CONSULTED, AND IT SAYS YES. Ownership is the lease plus the
	// election, both answerable with no I/O, so an unreachable object store does not
	// move it — this replica still holds the org and is still the only thing that
	// will end its expired leases. What the outage takes away is the CLAIM: the ship
	// is attempted and refused, so nothing is billed and nothing is durable, and the
	// successor re-reaps a row it still sees.
	//
	// Refusing to reap here would be the R-4b defect wearing this row's clothes: an
	// S3 outage would leave every failed start holding a pod for ever.
	if !owner.State.stores.Owned(mustNS(t, org)) {
		t.Fatal("outage: the fence was never consulted for the reap of an error row")
	}
	if _, err := st.Get(ctx, org, bad.ID); err == nil {
		t.Error("outage: an `error` row survived an outage — its pod outlives every clock and every counter")
	}
	if n := books.len(); n != 0 {
		t.Errorf("outage: the reap emitted %d debits with nothing durable behind them", n)
	}
}

// =============================================================================
// THE UNPROVABLE REGIME IS NOT SILENT, AND IT IS NOT SAFE.
//
// SayDurability tells the operator that on a multi-writer deployment with no
// fence "NOTHING is billed and every span defers". Two things are wrong with
// that:
//
//  1. the END path bills anyway — bill() consults no Owned() gate and its ship is
//     OrgStore.Sync, which acks trivially when dur is nil;
//  2. the reaper, the orphan sweep and the lease extender all stop, so sandbox
//     pods run forever, unbilled, on a deployment that merely lost its object
//     store at boot.
//
// peered() over-detects a bare CLOUD_PEER_SELECTOR, so a SINGLE-replica
// deployment reaches this state.
// =============================================================================
func TestTheUnprovableRegimeReapsAndClaimsNothing(t *testing.T) {
	ctx := context.Background()
	const org = "acme"
	books := &ledger{}

	b := cloud.NewBase(cloud.Deps{DataDir: t.TempDir(), Durable: nil, Peers: true}, "sandbox")
	b.Log = luxlog.NewNoOpLogger()
	stores := cloud.NewOrgStore(b, "sandbox", openStore)
	t.Cleanup(func() { _ = stores.CloseAll() })
	s := &Service{
		Base:  cloud.Base{Log: luxlog.NewNoOpLogger()},
		State: state{stores: stores, clk: newClocks(), debit: books.emit},
	}

	// THE REGIME IS THE SHIP'S ANSWER, NOT THE GATE'S. Owned answers TRUE here and
	// must: with no fence a replica's orgs are the ones on its own volume, so it is
	// the only thing that will ever end their leases. What the regime forbids is
	// CLAIMING, and that is the ship — which refuses, because no round orders two
	// writers.
	if !s.State.stores.Owned(mustNS(t, org)) {
		t.Fatal("the reaper is gated off in the unprovable regime — expired leases would keep their pods for ever")
	}
	if acked, err := s.State.stores.Sync(mustNS(t, org)); acked || err == nil {
		t.Fatalf("the ship acked with several writers and no fence: acked=%v err=%v", acked, err)
	}

	past := time.Now().Unix() - 2*3600
	m := occupy(t, s, org, "m_x", past)
	m.ExpiresAt = past + 3600 // over its lease by an hour
	st, err := storeFor(s, org)
	if err != nil {
		t.Fatalf("storeFor: %v", err)
	}
	if err := st.Put(ctx, m); err != nil {
		t.Fatalf("put: %v", err)
	}

	// (2) the reaper does nothing at all, so an expired lease keeps its pod.
	sweep(ctx, s)
	if _, err := st.Get(ctx, org, m.ID); err == nil {
		t.Errorf("unprovable: a lease an hour past its expiry survived the sweep in the unprovable regime — " +
			"an object-store outage must not stop lifetimes, or sandbox pods run for ever unbilled")
	}

	// (1) and the end path must not bill either: the sweep may run in this regime
	// (a lease still has to end) but nothing here can be acknowledged.
	m2, gerr := st.Get(ctx, org, m.ID)
	if gerr != nil {
		m2 = m // already retired by the sweep above; bill the value we hold
	}
	retire(s, ctx, st, m2, false)
	if n := books.micros(); n > 0 {
		t.Errorf("unprovable: SayDurability reports that NOTHING is billed in this regime, and the end path emitted "+
			"%d µ$ over %d debits — the ship must refuse where no fence can order two writers",
			n, books.len())
	}
}

// =============================================================================
// THE ORPHAN SWEEP'S NEW ORG GATE HAS NO TEST, AND THE LABEL FOLD CHANGED.
//
// orgKey() replaced slug() as the value of hanzo.ai/org on every sandbox pod and
// every project disk. The two folds AGREE on a clean lowercase org and DISAGREE
// on every other one, so a pod created by the previous image carries a label the
// new sweep's owned-org set cannot match — permanently, because an orphan is by
// definition never recreated.
// =============================================================================
func TestTheSweepKnowsEveryLabelItEverWrote(t *testing.T) {
	ctx := context.Background()
	// The five shapes the two folds disagree on and agree on. A clean lowercase
	// label folds identically through both; anything else gains a digest through
	// namespace.Sanitize and does not through slug.
	for _, o := range []string{"acme", "acme.co", "Acme", "acme_corp", "a-b"} {
		f := twopod.New(t, "pod-a")
		s := replica(t, f.Owner(o))
		occupy(t, s, o, "m_"+slug(o), time.Now().Unix())
		ship(t, s, o)

		_, mine := known(ctx, s)
		if mine == nil {
			t.Fatalf("known() could not read the stores for %q", o)
		}
		// A pod of this org carries one of these two, depending on which image
		// created it. The sweep has to recognise a pod it did not create.
		for _, label := range []string{orgKey(o), slug(o)} {
			if !mine[label] {
				t.Errorf("label: org %q — a pod labelled %q is skipped by the orphan sweep for ever, "+
					"because the owned set is %v", o, label, mine)
			}
		}
	}
}

// =============================================================================
// A MUTATION THAT SHOULD BITE AND DOES NOT.
//
// Nothing in this package tests orphans(), known()'s owned set, or orgKey(). The
// test below is the one the diff owes: it asserts that a pod belonging to an org
// this replica owns is judged, and one belonging to an org it does not is not.
// Written against the CURRENT code it passes; it is here to show what is absent,
// and it does not fail — which is the finding.
// =============================================================================
func TestTheOrphanSweepJudgesOnlyItsOwnOrgs(t *testing.T) {
	ctx := context.Background()
	f := twopod.New(t, "pod-a", "pod-b")
	const org = "acme"

	owner := replica(t, f.Owner(org))
	occupy(t, owner, org, "m_k", time.Now().Unix())
	ship(t, owner, org)

	claimed, mine := known(ctx, &cloud.Service[state]{
		Base:  cloud.Base{Log: luxlog.NewNoOpLogger()},
		State: owner.State,
	})
	if claimed == nil {
		t.Fatal("known() returned nil for a readable store")
	}
	if !claimed["m_k"] {
		t.Fatal("known() did not claim a live sandbox")
	}
	if !mine[mustNS(t, org).ID()] {
		t.Fatal("known() did not report the owner's own org as owned")
	}
	t.Logf("known(): %d claimed ids, owned orgs = %v", len(claimed), mine)

	// The other half — that a NON-owner reports an empty owned set, so every pod in
	// the cluster is skipped by its orphan sweep — has no test in the diff either.
	other := replica(t, f.Other(org))
	if _, err := storeFor(other, org); err != nil {
		t.Fatalf("open on the non-owner: %v", err)
	}
	_, theirs := known(ctx, &cloud.Service[state]{
		Base:  cloud.Base{Log: luxlog.NewNoOpLogger()},
		State: other.State,
	})
	if len(theirs) != 0 {
		t.Fatalf("the non-owner reported owned orgs: %v", theirs)
	}
	t.Log("coverage: the non-owner's orphan sweep judges NOTHING — every pod in the sandbox namespace is skipped")
}
