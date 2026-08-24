package cloud

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/hanzoai/cloud/internal/org"
)

// A failed open must not wedge the namespace it failed on.
//
// The durable path publishes an in-flight record, releases c.mu, and runs the
// hydrate and the caller-supplied open with the lock down so a slow object store
// cannot stall another org. Everything that arrives for the same namespace while
// that is happening waits on the record's channel. So retiring the record and
// closing that channel is not bookkeeping — it is the only thing that ever wakes
// those waiters, and it has to happen on EVERY exit from the open.
//
// It used to happen only on a normal return. A panic anywhere in the hydrate or
// the open therefore left the entry in the map with its channel open forever:
// every later caller for that org blocked, permanently, for the life of the
// process. Nothing logged it, the pod stayed Ready, and only the namespaces that
// had already opened cleanly kept working — which reads exactly like a partial
// outage of unknown cause.
//
// Mutation-checked: drop the `defer` in forNS and the second open below never
// returns, so this test fails on the timeout rather than silently passing.
func TestFailedOpenDoesNotWedgeTheNamespace(t *testing.T) {
	cond := &recordingStore{objects: map[string]versioned{}}
	members := org.NewMembership("pod-0", org.StaticSource(org.Member{ID: "pod-0", Addr: "pod-0"}), time.Minute)
	if err := members.Start(context.Background()); err != nil {
		t.Fatalf("membership: %v", err)
	}
	dur := org.NewDurability(cond, members, nil, org.WithCheckpoint(durableCheckpoint))

	explode := true
	store := NewOrgStore(Base{DataDir: t.TempDir(), Durable: dur}, "widget",
		func(db *sql.DB) (*sql.DB, error) {
			if explode {
				explode = false
				panic("open exploded")
			}
			return db, nil
		})
	t.Cleanup(func() { _ = store.CloseAll() })

	ns := MustOrgNamespace("wedged", "")

	// The first open panics. Recovering it is what an HTTP layer does, and it is
	// why this failure never crashed the process — it just poisoned one org.
	func() {
		defer func() {
			if r := recover(); r == nil {
				t.Error("expected the injected open to panic")
			}
		}()
		_, _ = store.For(ns)
	}()

	// The second open must RETURN. Whether it succeeds or reports an error is not
	// the point — the point is that it is not still waiting.
	done := make(chan error, 1)
	go func() {
		_, err := store.For(ns)
		done <- err
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("second open never returned: the in-flight record was never retired, so this namespace is wedged for the life of the process")
	}
}
