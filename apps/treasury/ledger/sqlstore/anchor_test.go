package sqlstore

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/namespace"
)

// replicas opens the book as n pods do: n stores, each with its own pool, on the one
// database. A SQLite file belongs to one process, so there the replicas share the
// file's one store.
func replicas(t *testing.T, open sqltest.Open, n int) []*Store {
	t.Helper()
	var out []*Store
	for range n {
		if len(out) > 0 && locus.DSN() == "" {
			out = append(out, out[0])
			continue
		}
		s, err := Opener(testMaster, "treasury", "")(namespace.System(), open(namespace.System(), "treasury"))
		if err != nil {
			t.Fatalf("Opener: %v", err)
		}
		t.Cleanup(func() { _ = s.Close() })
		out = append(out, s)
	}
	return out
}

// What the anchor remembers is the book's: a root committed through one replica is the
// last anchor on every replica, the signer bound through one is bound on all, and
// recording one does not forget the other.
func TestTheAnchorIsTheBooksNotAReplicas(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		ctx := context.Background()
		pods := replicas(t, open, 2)

		if _, ok, err := pods[0].Anchor(ctx); err != nil || ok {
			t.Fatalf("a book never anchored has an anchor: %v %v", ok, err)
		}
		if org, err := pods[0].SignerOrg(ctx); err != nil || org != "" {
			t.Fatalf("a book with no signer bound names %q, %v", org, err)
		}
		if err := pods[0].SetSignerOrg(ctx, "hanzo"); err != nil {
			t.Fatal(err)
		}
		if _, ok, _ := pods[1].Anchor(ctx); ok {
			t.Fatal("binding a signer made an anchor")
		}
		want := Anchor{Root: "0xabc", TxHash: "0xdef", Block: 7, At: 1_800_000_000, Entries: 12}
		if err := pods[0].SetAnchor(ctx, want); err != nil {
			t.Fatal(err)
		}
		if got, ok, err := pods[1].Anchor(ctx); err != nil || !ok || got != want {
			t.Fatalf("the other replica reads %+v, %v, %v; want %+v", got, ok, err, want)
		}
		if org, _ := pods[1].SignerOrg(ctx); org != "hanzo" {
			t.Fatalf("recording an anchor forgot the signer: %q", org)
		}
		next := Anchor{Root: "0x123", TxHash: "0x456", Block: 9, At: 1_800_000_100, Entries: 13}
		if err := pods[1].SetAnchor(ctx, next); err != nil {
			t.Fatal(err)
		}
		if got, _, _ := pods[0].Anchor(ctx); got != next {
			t.Fatalf("the first replica reads %+v after the second anchored; want %+v", got, next)
		}
	})
}

// Two replicas asked to anchor at once send in turn: of any number inside the lock at a
// moment there is one. A lock one replica holds is held against the others, and given
// back on release.
//
// MUTATION: return a release that holds nothing (no advisory lock, no mutex) and two
// replicas are inside together.
func TestOneReplicaSendsAnAnchorAtATime(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		pods := replicas(t, open, 3)
		var inside, most atomic.Int64
		var wg sync.WaitGroup
		for _, s := range pods {
			for range 2 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					release, err := s.Anchoring(context.Background())
					if err != nil {
						t.Errorf("Anchoring: %v", err)
						return
					}
					defer release()
					n := inside.Add(1)
					for {
						m := most.Load()
						if n <= m || most.CompareAndSwap(m, n) {
							break
						}
					}
					time.Sleep(30 * time.Millisecond)
					inside.Add(-1)
				}()
			}
		}
		wg.Wait()
		if most.Load() != 1 {
			t.Fatalf("%d replicas were inside the anchor lock together", most.Load())
		}
	})
}
