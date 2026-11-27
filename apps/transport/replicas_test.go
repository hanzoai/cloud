package transport

// Transport runs on every replica, and a rolling update starts a pod beside the one
// it replaces. These tests stand a second replica beside the first: on hanzo-sql a
// second pool on the same store, which is what another pod is; on SQLite the same
// file under a holder of its own, because a file belongs to one process.

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/dialect"
	"github.com/hanzoai/cloud/sdk/mint"
	luxlog "github.com/luxfi/log"

	"github.com/hanzoai/cloud/sdk"
)

// replicaOf is another replica's store beside st.
func replicaOf(t *testing.T, st *store) *store {
	t.Helper()
	if !dialect.Of(st.db).Postgres() {
		other := *st
		other.holder = mint.ID("pod")
		return &other
	}
	other, err := openStore(t.TempDir())
	if err != nil {
		t.Fatalf("a second replica's store: %v", err)
	}
	t.Cleanup(func() { _ = other.Close() })
	return other
}

func serviceOn(st *store) *cloud.Service[state] {
	return &cloud.Service[state]{Log: luxlog.New("test"), State: state{store: st}}
}

// turnOf is a rendering of the asked turn under session.
func turnOf(session string) rendering {
	m := asked()
	return rendering{org: "acme", session: session, run: "run_" + session, subject: "u-acme",
		reply:   Message{Transport: m.Transport, Account: m.Account, Channel: m.Channel, ReplyTo: m.ReplyTo},
		message: "1700000000.000200", started: nowUnix()}
}

func holderOf(t *testing.T, st *store, session string) string {
	t.Helper()
	var holder string
	if err := st.db.QueryRow(`SELECT holder FROM channel_turn WHERE session = $1`, session).Scan(&holder); err != nil {
		t.Fatalf("holder of %s: %v", session, err)
	}
	return holder
}

// A replica starting beside another that is showing a turn leaves it alone: it does not
// show it a second time, and the row stays the first replica's. (A rolling update starts
// the new pod while the old one is still rendering.)
func TestAStartingReplicaLeavesALiveTurnAlone(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		recordTurns(t)
		(&agent{pages: concluded()}).install(t)
		h := &heard{}
		prev := transports
		transports = []transport{listening(h, true)}
		t.Cleanup(func() { transports = prev })

		showing := serviceOn(newStore(t))
		starting := serviceOn(replicaOf(t, showing.State.store))
		showing.State.store.openTurn(context.Background(), showing, turnOf("live"))

		resume(starting)

		if n := len(h.all()); n != 0 {
			t.Fatalf("a starting replica showed a turn another is showing: %+v", h.all())
		}
		if got := holderOf(t, starting.State.store, "live"); got != showing.State.store.holder {
			t.Fatalf("the turn is held by %q, want the replica showing it, %q", got, showing.State.store.holder)
		}
	})
}

// A turn whose replica is gone is taken by one replica, however many look for it at once.
func TestATurnWhoseReplicaIsGoneIsTakenByOneReplica(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		dead := newStore(t)
		a, b := replicaOf(t, dead), replicaOf(t, dead)
		dead.openTurn(context.Background(), serviceOn(dead), turnOf("orphan"))
		dead.lapse(t)

		var wg sync.WaitGroup
		got := make([][]rendering, 2)
		for i, st := range []*store{a, b} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				var err error
				if got[i], err = st.claimTurns(context.Background(), time.Now().UnixMilli()); err != nil {
					t.Errorf("claim: %v", err)
				}
			}()
		}
		wg.Wait()
		if n := len(got[0]) + len(got[1]); n != 1 {
			t.Fatalf("%d replicas took one orphaned turn: %+v", n, got)
		}
	})
}

// A replica that renders a turn renews its row, so neighbours never see it lapse, and
// the row lapses a few beats after the replica stops.
func TestARenderingReplicaKeepsItsTurn(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		was := turnBeat
		turnBeat = 20 * time.Millisecond
		t.Cleanup(func() { turnBeat = was })
		st := newStore(t)
		s := serviceOn(st)
		other := replicaOf(t, st)
		st.openTurn(context.Background(), s, turnOf("kept"))
		st.lapse(t)
		l := st.keep(s, "acme", "kept")

		// Long enough for the row to lapse several times over if nothing renewed it.
		time.Sleep(4 * time.Duration(staleAfter()) * time.Millisecond)
		if rows, err := other.claimTurns(context.Background(), time.Now().UnixMilli()); err != nil || len(rows) != 0 {
			t.Fatalf("a neighbour took a turn that is being rendered: %+v (%v)", rows, err)
		}
		l.stop()
		waitFor(t, func() bool {
			rows, err := other.claimTurns(context.Background(), time.Now().UnixMilli()+staleAfter())
			return err == nil && len(rows) == 1
		})
	})
}

// A replica that stalls for longer than a lease finds its turn taken, stops rendering it
// without a word, and does not forget the row the other replica now holds.
func TestAReplicaThatLostItsTurnStopsAndLeavesTheRow(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		was := turnBeat
		turnBeat = 20 * time.Millisecond
		t.Cleanup(func() { turnBeat = was })
		stalled := newStore(t)
		s := serviceOn(stalled)
		taker := replicaOf(t, stalled)
		stalled.openTurn(context.Background(), s, turnOf("taken"))
		l := stalled.keep(s, "acme", "taken")
		defer l.stop()

		// Its beats stop landing for a lease (a stalled process), and the row is taken.
		rows, err := taker.claimTurns(context.Background(), time.Now().UnixMilli()+10*staleAfter())
		if err != nil || len(rows) != 1 {
			t.Fatalf("the lapsed turn was not taken: %+v (%v)", rows, err)
		}
		waitFor(t, l.lost)

		stalled.closeTurn(context.Background(), s, "acme", "taken")
		if got := holderOf(t, taker, "taken"); got != taker.holder {
			t.Fatalf("the turn is held by %q after the stalled replica let go, want %q", got, taker.holder)
		}
	})
}

// A replica that leaves gives its turns up, and a neighbour takes them at once.
func TestALeavingReplicaGivesItsTurnsUp(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		leaving := newStore(t)
		staying := replicaOf(t, leaving)
		leaving.openTurn(context.Background(), serviceOn(leaving), turnOf("handed"))
		if rows, _ := staying.claimTurns(context.Background(), time.Now().UnixMilli()); len(rows) != 0 {
			t.Fatalf("a live turn was taken before its replica left: %+v", rows)
		}
		if err := leaving.leave(context.Background()); err != nil {
			t.Fatal(err)
		}
		rows, err := staying.claimTurns(context.Background(), time.Now().UnixMilli())
		if err != nil || len(rows) != 1 || rows[0].session != "handed" {
			t.Fatalf("a neighbour did not take the turns a leaving replica gave up: %+v (%v)", rows, err)
		}
	})
}

// A rendering whose turn another replica has taken stops at its next read, and says nothing
// more in the conversation: the other replica concludes the message it is now showing.
func TestARenderingThatLostItsTurnStopsWithoutAWord(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		recordTurns(t)
		linked(t)
		was := turnBeat
		turnBeat = 20 * time.Millisecond
		t.Cleanup(func() { turnBeat = was })
		// One progress line, then silence: the turn is mid-flight until it is stopped.
		a := &agent{pages: []*client.TurnEvents{{Events: []client.TurnEvent{progress(1, "Looking at the repository")}, Next: 1}}}
		a.install(t)
		h := &heard{}
		stalled := serviceOn(newStore(t))
		taker := replicaOf(t, stalled.State.store)
		r := turnOf("fenced")
		r.message = ""
		stalled.State.store.openTurn(context.Background(), stalled, r)

		finished := make(chan struct{})
		go func() {
			defer close(finished)
			ctx, cancel := turnContext("acme")
			defer cancel()
			_, span := turnTracer.Start(ctx, "test")
			defer span.End()
			render(ctx, stalled, listening(h, true), r, span)
		}()
		// END THE TURN before this test returns, as the other tests that leave one running do.
		t.Cleanup(func() {
			a.mu.Lock()
			a.halted = append(a.halted, client.SessionMatchIn{SessionID: "sess_1"})
			a.mu.Unlock()
			<-finished
		})
		waitFor(t, func() bool { return len(h.all()) == 2 }) // "Working…", and the line of progress that edits it
		said := len(h.all())

		// A lease has run out: the taker asks at a time a few leases on from the stalled
		// replica's last beat, as it would have a few leases after it stopped.
		if rows, err := taker.claimTurns(context.Background(), time.Now().UnixMilli()+10*staleAfter()); err != nil || len(rows) != 1 {
			t.Fatalf("the lapsed turn was not taken: %+v (%v)", rows, err)
		}
		select {
		case <-finished:
		case <-time.After(5 * time.Second):
			t.Fatal("a rendering went on showing a turn another replica took")
		}
		if n := len(h.all()); n != said {
			t.Fatalf("the replica that lost the turn said %d more thing(s) in the conversation: %+v", n-said, h.all())
		}
		if got := holderOf(t, taker, "fenced"); got != taker.holder {
			t.Fatalf("the row is held by %q, want the replica that took it, %q", got, taker.holder)
		}
	})
}
