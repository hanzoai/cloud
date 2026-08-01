package agents

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/hanzoai/cloud"
)

// sessions_seq_test.go pins the event log's ordering guarantee at the seam it
// now depends on.
//
// AppendEvent allocates a session's next Seq by reading MAX(seq)+1 and then
// inserting. That read-then-write is only atomic because the org's file is
// served on ONE connection, and until the per-org split this package set that
// itself in openStore (db.SetMaxOpenConns(1)). It no longer does: the file is
// opened by cloud.OrgDB, and the guarantee now rests on a line in another
// package. An invariant that load-bearing must be asserted where it is relied
// upon, not assumed from a neighbour's source.
//
// Seq is not decoration. It is a subscriber's resume cursor — ListEvents reads
// `seq > since` and ListControlAfter reads the same way — so a duplicate seq
// makes a watcher skip an event and a gap makes it stall.

// TestAppendEventSequenceIsDenseAndUniqueUnderConcurrency writes one session's
// events from many goroutines at once and requires the result to be exactly
// 1..N, each once. A store opened with more than one connection interleaves the
// MAX(seq) reads and produces duplicates, which is what this refuses.
func TestAppendEventSequenceIsDenseAndUniqueUnderConcurrency(t *testing.T) {
	st := &state{stores: testStores(t)}
	sto := storeOf(t, st, "acme")
	ctx := context.Background()

	if err := sto.CreateSession(ctx, mkSession("acme", "sess_c", "", "sess_c")); err != nil {
		t.Fatalf("create: %v", err)
	}

	const writers, each = 8, 25
	var wg sync.WaitGroup
	errs := make(chan error, writers*each)
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < each; i++ {
				_, err := sto.AppendEvent(ctx, Event{
					ID: fmt.Sprintf("evt_%d_%d", w, i), SessionID: "sess_c", Org: "acme",
					Kind: KindLog, Actor: "acme/u1", Payload: `{"n":1}`, CreatedAt: int64(i),
				})
				if err != nil {
					errs <- err
				}
			}
		}(w)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("append: %v", err)
	}

	evs, err := sto.ListEvents(ctx, "acme", "sess_c", 0, 10000)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(evs) != writers*each {
		t.Fatalf("got %d events, want %d", len(evs), writers*each)
	}
	seen := make(map[int64]bool, len(evs))
	for _, e := range evs {
		if seen[e.Seq] {
			t.Fatalf("seq %d appears twice — a watcher resuming past it would skip an event", e.Seq)
		}
		seen[e.Seq] = true
	}
	for want := int64(1); want <= int64(writers*each); want++ {
		if !seen[want] {
			t.Fatalf("seq %d missing — a watcher resuming at %d would stall", want, want-1)
		}
	}
	// ListEvents must hand them back in seq order, because that order IS the
	// resume contract.
	for i, e := range evs {
		if e.Seq != int64(i+1) {
			t.Fatalf("event %d has seq %d — ListEvents is not ordered by seq", i, e.Seq)
		}
	}
}

// TestOrgFilesAreSingleWriter asserts the property the sequence rests on,
// directly and at the seam that now owns it: a per-org file cloud.OrgDB opened
// serves on exactly one connection. If this ever changes, the test above starts
// failing intermittently and this one says why in a single line.
func TestOrgFilesAreSingleWriter(t *testing.T) {
	db, err := cloud.OrgDB(t.TempDir(), "acme", "", "agents")
	if err != nil {
		t.Fatalf("OrgDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if got := db.Stats().MaxOpenConnections; got != 1 {
		t.Fatalf("per-org file allows %d connections; AppendEvent's MAX(seq)+1 "+
			"read-then-write is only atomic at 1", got)
	}
}
