package transport

// resume_test.go pins what a restart costs a channel: a pause, and never the answer.
//
// The failure is not a lost message. A process that dies mid-turn leaves either
// Slack's own "is working…" status up forever (nothing was posted, so nothing
// clears it) or a message frozen on a progress line above an answer nobody was
// left to deliver. Every assertion here is about what the CHANNEL ends up showing.

import (
	"context"
	"testing"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

func nowUnix() int64 { return time.Now().Unix() }

// withStore is a service that can remember a rendering, unlike service().
func withStore(t *testing.T) *cloud.Service[state] {
	t.Helper()
	return &cloud.Service[state]{Log: luxlog.New("test"), State: state{store: newStore(t)}}
}

// openTurns is every row on record, oldest first, whoever holds it.
func (st *store) openTurns(ctx context.Context) ([]rendering, error) {
	rows, err := st.db.QueryContext(ctx, `SELECT `+turnCols+` FROM channel_turn ORDER BY started_at, session`)
	if err != nil {
		return nil, err
	}
	return scanTurns(rows)
}

// lapse makes every row on record one nobody has renewed for long enough to be taken,
// as the rows of a process that has gone are.
func (st *store) lapse(t *testing.T) {
	t.Helper()
	if _, err := st.db.Exec(`UPDATE channel_turn SET renewed_at = 1`); err != nil {
		t.Fatal(err)
	}
}

func left(t *testing.T, s *cloud.Service[state]) []rendering {
	t.Helper()
	rows, err := s.State.store.openTurns(context.Background())
	if err != nil {
		t.Fatalf("openTurns: %v", err)
	}
	return rows
}

// the answer a turn reaches, as agents reports it to any reader starting at 0.
func concluded() []*client.TurnEvents {
	return []*client.TurnEvents{{
		Events: []client.TurnEvent{
			progress(1, "Looking at the repository"),
			{Seq: 2, Kind: client.TurnAnswer, Text: "the database ran out of connections"},
		},
		Next: 2, Outcome: client.TurnCompleted,
	}}
}

// While a turn is being shown there is a row for it, and once its conclusion has
// been shown there is not — so the next process inherits exactly the turns that
// were cut off, and none that finished.
func TestATurnIsOnRecordOnlyWhileItIsBeingShown(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		recordTurns(t)
		linked(t)
		(&agent{pages: concluded()}).install(t)
		s := withStore(t)

		h := &heard{}
		turn(s, listening(h, true), "acme", asked())

		if h.last().text != "the database ran out of connections" {
			t.Fatalf("the turn must still answer, got %+v", h.all())
		}
		if rows := left(t, s); len(rows) != 0 {
			t.Fatalf("a concluded turn left %d row(s) behind; the next start would show it again: %+v", len(rows), rows)
		}

	})
}

// THE CASE THE FILE EXISTS FOR. The previous process posted a progress line and
// died. This one edits THAT message into the answer: one message before the
// restart, the same one after it.
func TestAnInheritedTurnFinishesInTheMessageItsPredecessorPosted(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		recordTurns(t)
		(&agent{pages: concluded()}).install(t)
		s := withStore(t)
		m := asked()
		r := rendering{org: "acme", session: "sess_1", run: "run_1", subject: "u-acme",
			reply:   Message{Transport: m.Transport, Account: m.Account, Channel: m.Channel, ReplyTo: m.ReplyTo},
			message: "1700000000.000200", started: 1}
		s.State.store.openTurn(context.Background(), s, r)

		h := &heard{}
		ctx, cancel := turnContext("acme")
		defer cancel()
		_, span := turnTracer.Start(ctx, "test")
		defer span.End()
		render(ctx, s, listening(h, true), r, span)

		if h.posts() != 0 {
			t.Fatalf("an inherited turn posted %d NEW message(s); the channel would show two for one turn: %+v", h.posts(), h.all())
		}
		end := h.last()
		if end.text != "the database ran out of connections" || end.edit != "1700000000.000200" {
			t.Fatalf("the answer must replace the predecessor's message, got %+v", end)
		}
		if end.channel != m.Channel.ID || end.replyTo != m.ReplyTo {
			t.Fatalf("the answer landed outside the conversation: %+v", end)
		}
		if rows := left(t, s); len(rows) != 0 {
			t.Fatalf("the inherited turn was shown and is still on record: %+v", rows)
		}

	})
}

// The predecessor died BEFORE posting anything, which is the case that leaves
// Slack's own status up forever. There is no message to edit, so the answer is
// posted — once — and that post is what finally clears it.
func TestATurnCutOffBeforeItsFirstPostStillReachesTheChannel(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		recordTurns(t)
		(&agent{pages: concluded()}).install(t)
		s := withStore(t)
		m := asked()
		r := rendering{org: "acme", session: "sess_1", run: "run_1", subject: "u-acme",
			reply: Message{Transport: m.Transport, Account: m.Account, Channel: m.Channel, ReplyTo: m.ReplyTo}, started: 1}
		s.State.store.openTurn(context.Background(), s, r)

		h := &heard{}
		ctx, cancel := turnContext("acme")
		defer cancel()
		_, span := turnTracer.Start(ctx, "test")
		defer span.End()
		render(ctx, s, listening(h, true), r, span)

		if h.posts() != 1 {
			t.Fatalf("want exactly one message, saw %d: %+v", h.posts(), h.all())
		}
		if h.last().text != "the database ran out of connections" {
			t.Fatalf("the channel must end on the answer, got %+v", h.last())
		}

	})
}

// The message's id is what makes a turn editable by a process that did not post
// it, so it has to be on record the moment it exists — not at the end, when the
// process that knew it is the one that is gone.
func TestTheMessageIsOnRecordAsSoonAsItIsPosted(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		recordTurns(t)
		linked(t)
		// One progress line, then silence: the turn is mid-flight when we look.
		a := &agent{pages: []*client.TurnEvents{
			{Events: []client.TurnEvent{progress(1, "Looking at the repository")}, Next: 1},
		}}
		a.install(t)
		s := withStore(t)

		h := &heard{}
		finished := make(chan struct{})
		go func() { defer close(finished); turn(s, listening(h, true), "acme", asked()) }()
		// END THE TURN before this test returns. readTurn is a package-level stub, so a
		// reader left running would go on to call the NEXT test's stub and eat the
		// pages that test scripted — a failure that shows up somewhere else entirely.
		t.Cleanup(func() {
			a.mu.Lock()
			a.halted = append(a.halted, client.SessionMatchIn{SessionID: "sess_1"})
			a.mu.Unlock()
			<-finished
		})
		waitFor(t, func() bool { return h.posts() == 1 })
		waitFor(t, func() bool { rows := left(t, s); return len(rows) == 1 && rows[0].message != "" })

		rows := left(t, s)
		if got, want := rows[0].message, h.all()[0].id; got != want {
			t.Fatalf("recorded message %q, but the channel's message is %q: a resume would post a second one", got, want)
		}
		if rows[0].session != "sess_1" || rows[0].run != "run_1" || rows[0].subject != "u-acme" {
			t.Fatalf("the row cannot find its turn again: %+v", rows[0])
		}
		if rows[0].reply.Channel.ID != "C0FAKE" || rows[0].reply.ReplyTo != "1699999999.000100" || rows[0].reply.Channel.Kind != ChannelThread {
			t.Fatalf("the row lost where the turn answers: %+v", rows[0].reply)
		}

	})
}

// A turn nobody can show any more is dropped, not delivered: an answer arriving
// under a conversation that moved on a day ago is worse than the frozen line.
func TestAnOldOrUnshowableTurnIsDroppedNotDelivered(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		recordTurns(t)
		(&agent{pages: concluded()}).install(t)
		h := &heard{}
		prev := transports
		transports = []transport{listening(h, true)}
		t.Cleanup(func() { transports = prev })

		s := withStore(t)
		m := asked()
		reply := Message{Transport: m.Transport, Account: m.Account, Channel: m.Channel, ReplyTo: m.ReplyTo}
		s.State.store.openTurn(context.Background(), s, rendering{org: "acme", session: "old", subject: "u",
			reply: reply, message: "1.0", started: 1}) // 1970: far past resumeWithin
		gone := reply
		gone.Transport = "a-transport-this-build-does-not-carry"
		s.State.store.openTurn(context.Background(), s, rendering{org: "acme", session: "gone", subject: "u",
			reply: gone, message: "2.0", started: nowUnix()})
		s.State.store.lapse(t) // their process is gone

		resume(s)

		if rows := left(t, s); len(rows) != 0 {
			t.Fatalf("unshowable turns must be forgotten, still on record: %+v", rows)
		}
		if n := len(h.all()); n != 0 {
			t.Fatalf("an unshowable turn was delivered anyway: %+v", h.all())
		}

	})
}

// THE WIRING, through the real entry point. Everything above calls render or
// resume directly, so it would all stay green with the one `go resume(s)` line
// deleted from Use and the bug fully back. This one writes a turn into the store
// file, mounts the app the way apps.go does, and asserts the CHANNEL is finished
// from Use alone.
func TestMountingCarriesOnTheTurnsLeftBehind(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		recordTurns(t)
		(&agent{pages: concluded()}).install(t)
		h := &heard{}
		prev := transports
		transports = []transport{listening(h, true)}
		t.Cleanup(func() { transports = prev })

		// The previous process: it recorded a turn and its message, and then it died.
		dir := t.TempDir()
		st, err := openStore(dir)
		if err != nil {
			t.Fatalf("openStore: %v", err)
		}
		m := asked()
		dead := &cloud.Service[state]{Log: luxlog.New("test"), State: state{store: st}}
		st.openTurn(context.Background(), dead, rendering{org: "acme", session: "sess_1", run: "run_1", subject: "u-acme",
			reply:   Message{Transport: m.Transport, Account: m.Account, Channel: m.Channel, ReplyTo: m.ReplyTo},
			message: "1700000000.000200", started: nowUnix()})
		st.lapse(t) // it died, and nobody has renewed the row since
		if err := st.Close(); err != nil {
			t.Fatalf("close: %v", err)
		}

		// This process.
		t.Setenv("CLOUD_DATA_DIR", dir)
		t.Setenv("CLOUD_DOMAIN", "api.hanzo.ai")
		app := zip.New(zip.Config{Logger: luxlog.New("test")})
		compose(app)
		if err := Use(app, cloud.Deps{}); err != nil {
			t.Fatalf("Use: %v", err)
		}
		t.Cleanup(func() { _ = Shutdown(context.Background()) })

		waitFor(t, func() bool { return len(h.all()) > 0 && h.last().text == "the database ran out of connections" })
		if h.posts() != 0 || h.last().edit != "1700000000.000200" {
			t.Fatalf("mounting must finish the predecessor's message in place, got %+v", h.all())
		}
		waitFor(t, func() bool { return len(left(t, mounted.Load())) == 0 })

	})
}
