package agents

// orphan_test.go — a coding run whose process went away ends, with a reason a
// reader can act on, and one that is working, or only just started, does not.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/internal/sqltest"
)

// codingSession is a running coding run's session, last heard from at seen.
func codingSession(t *testing.T, sto *Store, id string, seen int64) {
	t.Helper()
	if err := sto.CreateSession(context.Background(), Session{
		ID: id, Org: "acme", Agent: "hanzo", Actor: "acme/z", Status: StatusRunning, RootID: id,
		Kind: kindCoding, Branch: "agent/" + id, Environment: "sandbox",
		StartedAt: seen, CreatedAt: seen, UpdatedAt: seen, MeteredAt: seen,
	}); err != nil {
		t.Fatalf("create %s: %v", id, err)
	}
}

// The rule: a coding run quiet past the grace is lost unless some pod holds it; one
// heard from inside the grace is not lost at all.
func TestACodingRunNobodyHoldsIsAnOrphanAfterTheGrace(t *testing.T) {
	now := time.Now()
	old := now.Add(-orphanGrace - time.Second).Unix()
	fresh := now.Add(-orphanGrace / 2).Unix()

	gone := Session{ID: "sess_lost", Status: StatusRunning, Kind: kindCoding, StartedAt: old, UpdatedAt: old}
	if !lost(gone, now) {
		t.Fatal("a coding run quiet past the grace was not read as lost")
	}
	if young := (Session{ID: "sess_young", Status: StatusRunning, Kind: kindCoding, StartedAt: fresh, UpdatedAt: fresh}); lost(young, now) {
		t.Fatal("a coding run heard from inside the grace was read as lost")
	}
	if paused := (Session{ID: "sess_paused", Status: StatusPaused, Kind: kindCoding, StartedAt: old, UpdatedAt: old}); lost(paused, now) {
		t.Fatal("a paused coding run was read as lost")
	}
	if other := (Session{ID: "sess_cli", Status: StatusRunning, StartedAt: old, UpdatedAt: old}); lost(other, now) {
		t.Fatal("a session that is no run's view was read as lost")
	}

	held, err := running.held(context.Background())
	if err != nil || held["sess_lost"] {
		t.Fatalf("a coding run nobody holds was read as held: %v %v", held, err)
	}
	_, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	hold("sess_lost", cancel)
	defer release("sess_lost")
	held, err = running.held(context.Background())
	if err != nil || !held["sess_lost"] {
		t.Fatalf("a coding run held here was not read as held: %v %v", held, err)
	}
}

// The sweep ends a lost coding run as an error, and both the session and its last
// event say what happened and how to go on — the status event a coding run's
// waiter reads for its end.
func TestALostCodingRunEndsWithAReasonToGoOnFrom(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		st, sto := meterStore(t)
		codingSession(t, sto, "sess_gone", time.Now().Add(-orphanGrace-time.Minute).Unix())
		codingSession(t, sto, "sess_busy", time.Now().Unix())

		reap(context.Background(), st, &record{}, testClocks)

		x := reload(t, sto, "sess_gone")
		if x.Status != StatusError || x.EndedAt == 0 || !strings.Contains(x.Error, "follow-up") {
			t.Fatalf("lost run: status %q ended %d error %q, want an error naming the follow-up", x.Status, x.EndedAt, x.Error)
		}
		if p := progressOf(x); p.Phase != StatusError || p.Activity != x.Error {
			t.Fatalf("progress of a lost run = %+v, want the error and its reason", p)
		}
		last, ok, err := sto.LastEvent(context.Background(), "acme", "sess_gone")
		if err != nil || !ok || last.Kind != KindStatus {
			t.Fatalf("last event = %+v %v %v, want a status", last, ok, err)
		}
		var body map[string]any
		if err := json.Unmarshal([]byte(last.Payload), &body); err != nil || body["status"] != "error" || body["branch"] != "agent/sess_gone" {
			t.Fatalf("status payload = %s", last.Payload)
		}
		if got := reload(t, sto, "sess_busy").Status; got != StatusRunning {
			t.Fatalf("a run heard from a moment ago was ended: %q", got)
		}

	})
}

// A run routed to a machine is not an orphan while that machine's runner is
// polling: the work is in its process, no pod holds it, and it reports once, when
// it ends. It is lost like any other once the machine stops polling.
func TestARunOnALiveTargetIsNotAnOrphan(t *testing.T) {
	ends := map[string]func(context.Context, *state, *Store){
		"the sweep": func(ctx context.Context, st *state, _ *Store) { reap(ctx, st, &record{}, testClocks) },
		"a read": func(ctx context.Context, st *state, sto *Store) {
			st.orphaned(ctx, sto, "acme", "sess_live")
			st.orphaned(ctx, sto, "acme", "sess_dead")
		},
	}
	for name, end := range ends {
		t.Run(name, func(t *testing.T) { aRunOnALiveTarget(t, end) })
	}
}

func aRunOnALiveTarget(t *testing.T, end func(context.Context, *state, *Store)) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		st, sto := meterStore(t)
		ctx := context.Background()
		now := time.Now()
		old := now.Add(-orphanGrace - time.Minute).Unix()

		routed := func(id, target string) {
			t.Helper()
			if err := sto.CreateSession(ctx, Session{
				ID: id, Org: "acme", Agent: "hanzo", Actor: "acme/z", Status: StatusRunning, RootID: id,
				Kind: kindCoding, Environment: target, StartedAt: old, CreatedAt: old, UpdatedAt: old, MeteredAt: old,
			}); err != nil {
				t.Fatalf("create %s: %v", id, err)
			}
		}
		routed("sess_live", "tgt_live")
		routed("sess_dead", "tgt_dead")
		for _, tgt := range []string{"tgt_live", "tgt_dead"} {
			if err := sto.UpsertClaimKeyHash(ctx, "acme", tgt, "hash", old); err != nil {
				t.Fatal(err)
			}
		}
		if err := sto.StampServing(ctx, "acme", "tgt_live", now.Unix()); err != nil {
			t.Fatal(err)
		}
		if err := sto.StampServing(ctx, "acme", "tgt_dead", now.Add(-servingTTL-time.Minute).Unix()); err != nil {
			t.Fatal(err)
		}

		end(ctx, st, sto)

		if got := reload(t, sto, "sess_live").Status; got != StatusRunning {
			t.Fatalf("a run on a machine that is polling was ended: %q", got)
		}
		if got := reload(t, sto, "sess_dead").Status; got != StatusError {
			t.Fatalf("a run on a machine that stopped polling was left running: %q", got)
		}
	})
}
