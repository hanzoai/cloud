package loop

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"

	dev "github.com/hanzoai/dev/go"
	"github.com/hanzoai/namespace"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/cloud/sdk/principal"
	luxlog "github.com/luxfi/log"
)

// script is a host: the model says what it is told to, and the cell is a counter.
type script struct {
	mu      sync.Mutex
	asks    []func(context.Context, dev.Ask) dev.Reply
	effects []string
	emitted []string
	lease   string
}

func (s *script) Ask(ctx context.Context, a dev.Ask) (dev.Reply, error) {
	s.mu.Lock()
	if len(s.asks) == 0 {
		s.mu.Unlock()
		return dev.Reply{}, errors.New("no more asks scripted")
	}
	f := s.asks[0]
	s.asks = s.asks[1:]
	s.mu.Unlock()
	r := f(ctx, a)
	if err := ctx.Err(); err != nil {
		return dev.Reply{}, err
	}
	return r, nil
}

func (s *script) Effect(_ context.Context, lease string, op dev.Op) (string, bool, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if op.Exec == nil {
		return "", true, "", errors.New("only exec is scripted")
	}
	s.effects = append(s.effects, strings.Join(op.Exec.Argv, " "))
	if lease == "" {
		return "ran: " + strings.Join(op.Exec.Argv, " "), false, "cell-1", nil
	}
	return "ran: " + strings.Join(op.Exec.Argv, " "), false, "", nil
}

func (s *script) Emit(t string) { s.mu.Lock(); s.emitted = append(s.emitted, t); s.mu.Unlock() }

func text(t string) dev.Reply { return dev.Reply{Text: &t} }
func exec(argv ...string) dev.Reply {
	return dev.Reply{Calls: []dev.Call{{Exec: &dev.Exec{Argv: argv}}}}
}

func harness(t *testing.T, open sqltest.Open, h *script) *Loop {
	t.Helper()
	core, err := dev.Open(context.Background())
	if err != nil {
		t.Fatalf("open the core: %v", err)
	}
	t.Cleanup(func() { _ = core.Close(context.Background()) })
	st, err := Open(open(namespace.System(), "dev"))
	if err != nil {
		t.Fatal(err)
	}
	return &Loop{Core: core, Store: func(string) (*Store, error) { return st, nil }}
}

// A turn asks the model, does what it asks in a cell leased on first need, tells the
// model what happened, and ends with the model's words shown.
func TestATurnRunsToItsEnd(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		h := &script{asks: []func(context.Context, dev.Ask) dev.Reply{
			func(context.Context, dev.Ask) dev.Reply { return exec("echo", "hi") },
			func(_ context.Context, a dev.Ask) dev.Reply {
				b, _ := json.Marshal(a.Messages)
				if !strings.Contains(string(b), "ran: echo hi") {
					t.Errorf("the model was not told what the command printed: %s", b)
				}
				return text("it printed hi")
			},
		}}
		l := harness(t, open, h)
		if err := l.New("acme", "alice", "s1"); err != nil {
			t.Fatal(err)
		}
		if err := l.Turn(context.Background(), "acme", "alice", "s1", "say hi", h); err != nil {
			t.Fatalf("turn: %v", err)
		}
		if len(h.effects) != 1 || h.effects[0] != "echo hi" {
			t.Errorf("effects = %v, want the one command", h.effects)
		}
		if !strings.Contains(strings.Join(h.emitted, "|"), "it printed hi") {
			t.Errorf("the person was shown %v, want the model's words", h.emitted)
		}
		if s, _ := l.Get("acme", "alice", "s1"); s.Lease != "cell-1" || s.Status != statusLive {
			t.Errorf("session = %+v, want the cell recorded and live", s)
		}
	})
}

// A person's session is theirs: another person reads nothing and steps nothing.
func TestASessionIsItsOwnersAlone(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		l := harness(t, open, &script{})
		if err := l.New("acme", "alice", "s1"); err != nil {
			t.Fatal(err)
		}
		if _, err := l.Get("acme", "bob", "s1"); !errors.Is(err, ErrNoSession) {
			t.Errorf("bob read alice's session: %v", err)
		}
		if err := l.Turn(context.Background(), "acme", "bob", "s1", "hi", nil); !errors.Is(err, ErrNoSession) {
			t.Errorf("bob stepped alice's session: %v", err)
		}
	})
}

// A turn that dies after its command ran resumes by replaying the RESULT: the model is
// asked again where it was cut off, and the command is not run a second time.
func TestAResumedTurnNeverRepeatsACommand(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		cut := make(chan struct{})
		h := &script{asks: []func(context.Context, dev.Ask) dev.Reply{
			func(context.Context, dev.Ask) dev.Reply { return exec("git", "commit", "-m", "x") },
			func(ctx context.Context, _ dev.Ask) dev.Reply { close(cut); <-ctx.Done(); return text("") },
		}}
		l := harness(t, open, h)
		if err := l.New("acme", "alice", "s1"); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		go func() { <-cut; cancel() }()
		if err := l.Turn(ctx, "acme", "alice", "s1", "commit it", h); err == nil {
			t.Fatal("a turn cut off mid-way reported success")
		}

		h.mu.Lock()
		h.asks = []func(context.Context, dev.Ask) dev.Reply{func(context.Context, dev.Ask) dev.Reply { return text("committed") }}
		h.mu.Unlock()
		// The cut turn's hold lapses with its process; here its token is released by the
		// deferred leave, as a clean cancel does.
		if err := l.Turn(context.Background(), "acme", "alice", "s1", "", h); err != nil {
			t.Fatalf("resume: %v", err)
		}
		if n := len(h.effects); n != 1 {
			t.Errorf("the commit ran %d times across the cut and the resume, want once", n)
		}
		if !strings.Contains(strings.Join(h.emitted, "|"), "committed") {
			t.Errorf("the resumed turn showed %v", h.emitted)
		}
	})
}

// One op holds a session at a time.
func TestASessionStepsOneTurnAtATime(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		in := make(chan struct{})
		release := make(chan struct{})
		h := &script{asks: []func(context.Context, dev.Ask) dev.Reply{func(context.Context, dev.Ask) dev.Reply { close(in); <-release; return text("slow") }}}
		l := harness(t, open, h)
		if err := l.New("acme", "alice", "s1"); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- l.Turn(context.Background(), "acme", "alice", "s1", "first", h) }()
		<-in
		if err := l.Turn(context.Background(), "acme", "alice", "s1", "second", h); !errors.Is(err, ErrBusy) {
			t.Errorf("a second turn under a live one = %v, want ErrBusy", err)
		}
		close(release)
		if err := <-done; err != nil {
			t.Fatalf("first turn: %v", err)
		}
	})
}

// Submit records what the core asks for and does none of it; Drive does it, and does
// what is left after a cut. The split is what lets the work run in the background.
func TestSubmitDoesNothingAndDriveDoesIt(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		h := &script{asks: []func(context.Context, dev.Ask) dev.Reply{
			func(context.Context, dev.Ask) dev.Reply { return text("hello") },
		}}
		l := harness(t, open, h)
		if err := l.New("acme", "alice", "s1"); err != nil {
			t.Fatal(err)
		}
		if err := l.Submit(context.Background(), "acme", "alice", "s1", "hi"); err != nil {
			t.Fatalf("submit: %v", err)
		}
		if len(h.asks) != 1 || len(h.emitted) != 0 {
			t.Fatal("submit reached the model or the person")
		}
		if p, _ := l.Pending("acme", "alice", "s1"); !p {
			t.Fatal("submitted work is not pending")
		}
		if err := l.Drive(context.Background(), "acme", "alice", "s1", h); err != nil {
			t.Fatalf("drive: %v", err)
		}
		if p, _ := l.Pending("acme", "alice", "s1"); p {
			t.Error("work is still pending after drive")
		}
		lines, err := l.Lines("acme", "alice", "s1", 0)
		if err != nil || len(lines) != 1 || lines[0].Text != "hello" {
			t.Errorf("the record of what was shown = %v %v, want the one line", lines, err)
		}
		if more, _ := l.Lines("acme", "alice", "s1", lines[0].N); len(more) != 0 {
			t.Errorf("a watcher up to date was sent %v again", more)
		}
	})
}

// A suspended session takes no turn, keeps its work, and carries on when resumed.
func TestASuspendedSessionTakesNoTurnUntilResumed(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		h := &script{asks: []func(context.Context, dev.Ask) dev.Reply{
			func(context.Context, dev.Ask) dev.Reply { return exec("ls") },
			func(context.Context, dev.Ask) dev.Reply { return text("listed") },
		}}
		l := harness(t, open, h)
		if err := l.New("acme", "alice", "s1"); err != nil {
			t.Fatal(err)
		}
		if err := l.Turn(context.Background(), "acme", "alice", "s1", "list", h); err != nil {
			t.Fatal(err)
		}
		lease, err := l.Suspend(context.Background(), "acme", "alice", "s1")
		if err != nil || lease != "cell-1" {
			t.Fatalf("suspend = %q %v, want the cell it held", lease, err)
		}
		if err := l.Submit(context.Background(), "acme", "alice", "s1", "again"); !errors.Is(err, ErrSuspended) {
			t.Errorf("a suspended session took a turn: %v", err)
		}
		if err := l.Resume("acme", "alice", "s1"); err == nil {
			t.Error("a session resumed while its cell was still held")
		}
		if err := l.Unleased("acme", "s1", lease); err != nil {
			t.Fatal(err)
		}
		if err := l.Resume("acme", "alice", "s1"); err != nil {
			t.Fatalf("resume: %v", err)
		}
		h.mu.Lock()
		h.asks = []func(context.Context, dev.Ask) dev.Reply{
			func(context.Context, dev.Ask) dev.Reply { return exec("pwd") },
			func(context.Context, dev.Ask) dev.Reply { return text("again") },
		}
		h.mu.Unlock()
		if err := l.Turn(context.Background(), "acme", "alice", "s1", "again", h); err != nil {
			t.Fatalf("a resumed session took no turn: %v", err)
		}
		if s, _ := l.Get("acme", "alice", "s1"); s.Lease != "cell-1" {
			t.Errorf("a resumed session's next command leased %q, want a cell leased afresh", s.Lease)
		}
	})
}

// An ended session is not resumed: asking for it again answers that there is no such
// session, not a session that will take no turn.
func TestAnEndedSessionIsNotResumed(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		l := harness(t, open, &script{})
		if err := l.New("acme", "alice", "s1"); err != nil {
			t.Fatal(err)
		}
		if _, err := l.Stop("acme", "alice", "s1"); err != nil {
			t.Fatal(err)
		}
		if err := l.Resume("acme", "alice", "s1"); !errors.Is(err, ErrNoSession) {
			t.Errorf("resume of an ended session = %v, want ErrNoSession", err)
		}
	})
}

// A claim that cannot be made durable is taken back and its command is not run: a
// replica that is not the org's writer performs nothing.
func TestACommandWhoseClaimCannotBeShippedIsNotRun(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		h := &script{asks: []func(context.Context, dev.Ask) dev.Reply{
			func(context.Context, dev.Ask) dev.Reply { return exec("rm", "-rf", "build") },
		}}
		l := harness(t, open, h)
		if err := l.New("acme", "alice", "s1"); err != nil {
			t.Fatal(err)
		}
		if err := l.Submit(context.Background(), "acme", "alice", "s1", "clean"); err != nil {
			t.Fatal(err)
		}
		l.Ship = func(string) error { return errors.New("not the elected writer") }
		if err := l.Drive(context.Background(), "acme", "alice", "s1", h); err == nil {
			t.Fatal("a drive reported success though nothing could be made durable")
		}
		if len(h.effects) != 0 {
			t.Errorf("ran %v on a replica that could not ship its claim", h.effects)
		}
	})
}

// interrupting is a host that records the cells it was asked to stop.
type interrupting struct {
	*script
	stopped []string
}

func (i *interrupting) Interrupt(_ context.Context, lease string) error {
	i.stopped = append(i.stopped, lease)
	return nil
}

// A turn that died holding a cell may have left a command running in it, so the turn
// that takes the session over stops it before it does anything.
func TestATakeOverInterruptsTheDeadTurnsCellFirst(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		h := &interrupting{script: &script{asks: []func(context.Context, dev.Ask) dev.Reply{
			func(context.Context, dev.Ask) dev.Reply { return exec("sleep", "9") },
			func(context.Context, dev.Ask) dev.Reply { return text("ok") },
		}}}
		l := harness(t, open, h.script)
		if err := l.New("acme", "alice", "s1"); err != nil {
			t.Fatal(err)
		}
		if err := l.Turn(context.Background(), "acme", "alice", "s1", "go", h); err != nil {
			t.Fatal(err)
		}
		st, _ := l.Store("acme")
		// A turn took the session and its process died: the hold is still named, and has lapsed.
		if _, err := st.db.Exec(`UPDATE session SET turn = 'dead', held = 1 WHERE id = 's1'`); err != nil {
			t.Fatal(err)
		}
		if err := l.Drive(context.Background(), "acme", "alice", "s1", h); err != nil {
			t.Fatalf("the takeover: %v", err)
		}
		if len(h.stopped) != 1 || h.stopped[0] != "cell-1" {
			t.Errorf("the takeover interrupted %v, want the dead turn's cell", h.stopped)
		}
	})
}

// An observation made elsewhere — the person's own machine ran the command — is recorded
// and told to the core, and the host never runs the command.
func TestAnAnswerMadeElsewhereIsToldToTheCore(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		h := &script{asks: []func(context.Context, dev.Ask) dev.Reply{
			func(context.Context, dev.Ask) dev.Reply { return exec("make") },
			func(_ context.Context, a dev.Ask) dev.Reply {
				b, _ := json.Marshal(a.Messages)
				if !strings.Contains(string(b), "built on my laptop") {
					t.Errorf("the model was not told the answer: %s", b)
				}
				return text("thanks")
			},
		}}
		l := harness(t, open, h)
		if err := l.New("acme", "alice", "s1"); err != nil {
			t.Fatal(err)
		}
		ctx := context.Background()
		if err := l.Submit(ctx, "acme", "alice", "s1", "build"); err != nil {
			t.Fatal(err)
		}
		// This host cannot reach a cell, so the drive stops with the command pending.
		if err := l.Drive(ctx, "acme", "alice", "s1", &noCell{script: h}); err == nil {
			t.Fatal("a drive with no cell reported success")
		}
		st, _ := l.Store("acme")
		r, _ := st.get("s1")
		var work []dev.Action
		_ = json.Unmarshal(r.Pending, &work)
		if len(work) != 1 || work[0].Op.Exec == nil {
			t.Fatalf("pending = %+v, want the one command", work)
		}
		id := int64(work[0].ID)

		if err := l.Answer(ctx, "acme", "alice", "s1", Observation{ID: id, Out: "built on my laptop"}); err != nil {
			t.Fatalf("answer: %v", err)
		}
		if err := l.Drive(ctx, "acme", "alice", "s1", h); err != nil {
			t.Fatalf("drive after the answer: %v", err)
		}
		if len(h.effects) != 0 {
			t.Errorf("the host ran %v for a command answered elsewhere", h.effects)
		}
		if err := l.Answer(ctx, "acme", "alice", "s1", Observation{ID: 99}); err == nil {
			t.Error("an answer to an action nobody asked for was taken")
		}
	})
}

// noCell is a host that cannot reach a cell.
type noCell struct{ *script }

func (n *noCell) Effect(context.Context, string, dev.Op) (string, bool, string, error) {
	return "", false, "", errors.New("no cell")
}

// A chat thread's workspace action goes through the journal as a turn's does: done
// once under its thread and id, answered from its row when it comes again, refused
// when the id is recorded as another action. Another thread's action of the same id
// is its own. A connection that has lost the session runs nothing.
func TestAThreadsWorkspaceActionIsClaimedInTheJournal(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		ctx := context.Background()
		h := &script{}
		l := harness(t, open, h)
		if err := l.New("acme", "alice", "w1"); err != nil {
			t.Fatal(err)
		}
		held, err := l.Hold(ctx, "acme", "alice", "w1")
		if err != nil {
			t.Fatal(err)
		}
		defer held.Release()
		act := func(id uint64, argv ...string) dev.Action {
			return dev.Action{ID: id, Op: dev.Op{Exec: &dev.Exec{Argv: argv}}}
		}

		out, err := held.Perform(ctx, "t1", act(3, "ls"), h)
		if err != nil || out.ID != 3 || out.Failed || out.Text != "ran: ls" {
			t.Fatalf("first perform = %+v %v, want the command's output under id 3", out, err)
		}
		if held.Lease() != "cell-1" {
			t.Errorf("the connection holds cell %q, want the one the host leased", held.Lease())
		}
		if s, _ := l.Get("acme", "alice", "w1"); s.Lease != "cell-1" {
			t.Errorf("the session names cell %q, want the one the host leased", s.Lease)
		}

		again, err := held.Perform(ctx, "t1", act(3, "ls"), h)
		if err != nil || again != out {
			t.Errorf("the same action again = %+v %v, want the recorded answer %+v", again, err, out)
		}
		other, err := held.Perform(ctx, "t2", act(3, "pwd"), h)
		if err != nil || other.Failed || other.Text != "ran: pwd" {
			t.Errorf("another thread's id 3 = %+v %v, want it run as its own action", other, err)
		}
		differs, err := held.Perform(ctx, "t1", act(3, "rm", "-rf", "x"), h)
		if err != nil || !differs.Failed || !strings.Contains(differs.Text, "different action") {
			t.Errorf("a different action under a recorded id = %+v %v, want it refused", differs, err)
		}
		if got := strings.Join(h.effects, "|"); got != "ls|pwd" {
			t.Errorf("the cell ran %q, want each action once", got)
		}
		if _, err := held.Perform(ctx, "t1", dev.Action{ID: 4, Op: dev.Op{Model: &dev.Ask{}}}, h); err == nil {
			t.Error("a model ask was taken as a workspace action")
		}

		st, err := l.Store("acme")
		if err != nil {
			t.Fatal(err)
		}
		rows, err := st.journal("w1", 0)
		if err != nil {
			t.Fatal(err)
		}
		var seen []string
		for _, e := range rows {
			if e.Answer == nil {
				t.Errorf("journal row %s/%d has no answer", e.Thread, e.ID)
			}
			seen = append(seen, e.Thread+"/"+strconv.FormatInt(e.ID, 10))
		}
		if strings.Join(seen, " ") != "t1/3 t2/3" {
			t.Errorf("the journal holds %v, want the two threads' actions", seen)
		}

		if _, err := l.Stop("acme", "alice", "w1"); err != nil {
			t.Fatal(err)
		}
		lost, err := held.Perform(ctx, "t1", act(5, "touch", "y"), h)
		if err != nil || !lost.Failed || !strings.Contains(lost.Text, "no longer holds") {
			t.Errorf("a perform after the session was stopped = %+v %v, want it refused", lost, err)
		}
		if len(h.effects) != 2 {
			t.Errorf("the cell ran %v after the session was stopped", h.effects[2:])
		}
	})
}

// A journal written before actions named their thread keeps every row, as the
// session's own core's, and takes threads' actions beside them. Legacy-file upgrade
// (pre-thread SQLite schema with triggers), so SQLite-only by nature.
func TestAJournalFromBeforeThreadsKeepsItsRows(t *testing.T) {
	t.Setenv(locus.Env, "")
	dir := t.TempDir()
	db, err := locus.Open(namespace.System(), "dev", dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for _, q := range []string{
		`CREATE TABLE session (
		  id TEXT PRIMARY KEY, owner TEXT NOT NULL, lease TEXT NOT NULL DEFAULT '',
		  turn TEXT NOT NULL DEFAULT '', held INTEGER NOT NULL DEFAULT 0, seq INTEGER NOT NULL DEFAULT 0,
		  state BLOB, pending BLOB, frames BLOB, turns INTEGER NOT NULL DEFAULT 0,
		  status TEXT NOT NULL, updated TEXT NOT NULL)`,
		`CREATE TABLE action (
		  session TEXT NOT NULL, id INTEGER NOT NULL, seq INTEGER NOT NULL DEFAULT 0,
		  kind TEXT NOT NULL, body BLOB NOT NULL, dispatched TEXT NOT NULL,
		  done TEXT NOT NULL DEFAULT '', result BLOB, PRIMARY KEY (session, id))`,
		`CREATE TRIGGER journal_insert AFTER INSERT ON action BEGIN
		  UPDATE session SET seq = seq + 1 WHERE id = NEW.session;
		  UPDATE action SET seq = (SELECT seq FROM session WHERE id = NEW.session)
		   WHERE session = NEW.session AND id = NEW.id;
		END`,
		`INSERT INTO session (id, owner, status, updated) VALUES ('s1', 'alice', 'idle', '2026-11-21T00:00:00.000000000Z')`,
		`INSERT INTO action (session, id, kind, body, dispatched, done, result)
		 VALUES ('s1', 1, 'exec', '{"id":1,"kind":"exec","body":{"argv":["ls"],"cwd":null}}', 'a', 'b', '{"id":1,"exit":0,"out":"x"}')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("the old journal: %v", err)
		}
	}
	st, err := Open(db)
	if err != nil {
		t.Fatalf("open an old journal: %v", err)
	}
	rows, err := st.journal("s1", 0)
	if err != nil || len(rows) != 1 || rows[0].Thread != "" || rows[0].ID != 1 || rows[0].Seq != 1 ||
		rows[0].Answer == nil || rows[0].Answer.Out != "x" {
		t.Fatalf("the old journal reads %+v %v, want its one row, answered", rows, err)
	}
	if ok, err := st.take("s1", "tok", "", statusIdle); err != nil || !ok {
		t.Fatalf("take: %v %v", ok, err)
	}
	body := json.RawMessage(`{"argv":["ls"],"cwd":null}`)
	obs, seen, err := st.claim("s1", "tok", Action{ID: 1, Kind: "exec", Body: body})
	if err != nil || !seen || obs.Out != "x" {
		t.Errorf("the old row's id claimed again = %+v %v %v, want its answer", obs, seen, err)
	}
	if _, seen, err := st.claim("s1", "tok", Action{ID: 1, Kind: "exec", Body: body, Thread: "t1"}); err != nil || seen {
		t.Errorf("a thread's id 1 = seen %v %v, want it minted beside the old row", seen, err)
	}
	if rows, _ := st.journal("s1", 1); len(rows) != 1 || rows[0].Thread != "t1" || rows[0].Seq != 2 {
		t.Errorf("after the old row the journal reads %+v, want the thread's row next in order", rows)
	}
}

// An existing SQLite dev.db file moves cleanly to hanzo-sql when CLOUD_SQL_MOVE
// is set, preserving sessions, journal sequence numbers, actions, and emits, and
// continuing monotonic seq numbering on PostgreSQL.
func TestDevMoveToSQL(t *testing.T) {
	dir := t.TempDir()
	ns := principal.MustOrgNamespace("acme", "")

	// 1. Create a local SQLite dev.db with a session, an answered action, and an emit.
	t.Setenv(locus.Env, "")
	b := cloud.Base{Log: luxlog.New("test"), DataDir: dir}
	fileStores := cloud.NewOrgStore(b, "dev", OpenStore)
	fs, err := fileStores.For(ns)
	if err != nil {
		t.Fatalf("open file store: %v", err)
	}
	if err := fs.create(row{ID: "s1", Owner: "alice", Status: statusIdle}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if ok, err := fs.take("s1", "tok1", "", statusIdle); err != nil || !ok {
		t.Fatalf("take: %v %v", ok, err)
	}
	body := json.RawMessage(`{"argv":["echo","hi"]}`)
	if _, seen, err := fs.claim("s1", "tok1", Action{ID: 1, Kind: "exec", Body: body}); err != nil || seen {
		t.Fatalf("claim: seen=%v err=%v", seen, err)
	}
	if err := fs.complete("s1", "", 1, Observation{ID: 1, Exit: 0, Out: "hi"}); err != nil {
		t.Fatalf("complete: %v", err)
	}
	if n, err := fs.appendEmit("s1", "hello"); err != nil || n != 1 {
		t.Fatalf("emit: n=%d err=%v", n, err)
	}
	if err := fileStores.CloseAll(); err != nil {
		t.Fatalf("close file store: %v", err)
	}

	// 2. Point at PostgreSQL with CLOUD_SQL_MOVE=lazy and reopen through OrgStore.
	sqltest.Server(t)
	t.Setenv(locus.MoveEnv, string(locus.Lazy))
	pgStores := cloud.NewOrgStore(b, "dev", OpenStore)
	t.Cleanup(func() { _ = pgStores.CloseAll() })
	ps, err := pgStores.For(ns)
	if err != nil {
		t.Fatalf("open moved store: %v", err)
	}
	place, err := locus.Where(ns, "dev", dir)
	if err != nil || place != locus.OnServer {
		t.Fatalf("place after move = %v (%v), want OnServer", place, err)
	}
	got, err := ps.get("s1")
	if err != nil || got.Seq != 2 || got.Owner != "alice" {
		t.Fatalf("session after move = %+v (%v), want seq=2 owner=alice", got, err)
	}
	entries, err := ps.journal("s1", 0)
	if err != nil || len(entries) != 1 || entries[0].Seq != 2 || entries[0].Answer == nil || entries[0].Answer.Out != "hi" {
		t.Fatalf("journal after move = %+v (%v)", entries, err)
	}
	lines, err := ps.emitsAfter("s1", 0)
	if err != nil || len(lines) != 1 || lines[0].Text != "hello" {
		t.Fatalf("emits after move = %+v (%v)", lines, err)
	}
	// Continuing writes on PostgreSQL advance seq monotonically past 2.
	if _, seen, err := ps.claim("s1", "tok1", Action{ID: 2, Kind: "exec", Body: body}); err != nil || seen {
		t.Fatalf("claim 2 on pg: seen=%v err=%v", seen, err)
	}
	entries, err = ps.journal("s1", 2)
	if err != nil || len(entries) != 1 || entries[0].ID != 2 || entries[0].Seq != 3 {
		t.Fatalf("journal after pg claim = %+v (%v), want id=2 seq=3", entries, err)
	}
}
