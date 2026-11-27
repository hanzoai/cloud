package coding

// inbox_test.go — what is said to a run no pod holds is kept, and given to the run
// once, in order, when it has an owner: after a rolling restart, between owners, while
// a lost run is resumed. A run that has ended refuses it.

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/apps/agents"
	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk/coord"
	"github.com/zap-proto/zip"
)

// sessionsAre makes the sessions app answer with these runs to the person they are
// for, and as an absent one to anybody else.
func sessionsAre(t *testing.T, runs ...agents.Session) {
	t.Helper()
	was := visibleRun
	visibleRun = func(ctx context.Context, id string) (agents.Session, error) {
		who := zip.CallerOf(ctx)
		for _, x := range runs {
			if x.ID == id && x.Org == who.Org && x.Actor == who.Org+"/"+who.User {
				return x, nil
			}
		}
		return agents.Session{}, zip.ErrNotFound("session not found")
	}
	t.Cleanup(func() { visibleRun = was })
}

func theRun(id, status string) agents.Session {
	return agents.Session{ID: id, Org: "acme", Actor: "acme/u-acme", Kind: kindCoding, Status: status, Box: "sbx_" + id}
}

// podOf is a pod: its flights over a ledger of its own.
func podOf(t *testing.T, s *depot, pod func(string) *coord.Coord, id string) (*fleet, *ledger) {
	t.Helper()
	c := pod(id)
	l := s.replica(func(string) *coord.Coord { return c }, id, periods{})
	fl := newFleet()
	fl.keep(l)
	fl.serve(c, nil)
	t.Cleanup(func() { fl.serve(nil, nil) })
	return fl, l
}

// taking is a run whose turn takes every instruction, and reports each.
type taking struct {
	mu   sync.Mutex
	said []string
	got  chan string
}

func newTaking() *taking { return &taking{got: make(chan string, 16)} }

func (k *taking) take(text string) bool {
	k.mu.Lock()
	k.said = append(k.said, text)
	k.mu.Unlock()
	k.got <- text
	return true
}

func (k *taking) all() []string {
	k.mu.Lock()
	defer k.mu.Unlock()
	return slices.Clone(k.said)
}

// flyTaking registers a working run on fl whose turn is k, as a resumed run is when it
// begins.
func flyTaking(t *testing.T, fl *fleet, session string, k *taking) *flight {
	t.Helper()
	ctx, stop := context.WithCancelCause(context.Background())
	f := &flight{org: "acme", subject: "u-acme", session: session, cancel: stop, done: make(chan struct{}), ctx: ctx,
		in: client.CodingStartIn{Repo: "api", Prompt: "add a health check"}}
	if k != nil {
		f.steerer = k.take
	}
	fl.fly(f)
	t.Cleanup(func() { f.forget() })
	return f
}

func tell(text, request string) client.CodingSteerIn {
	return client.CodingSteerIn{Subject: "u-acme", SessionID: "sess_q", Text: text, RequestID: request}
}

func heard(t *testing.T, k *taking) string {
	t.Helper()
	select {
	case text := <-k.got:
		return text
	case <-time.After(10 * time.Second):
		t.Fatal("the run was never given the instruction")
		return ""
	}
}

// A steer for a run no pod holds is kept, not failed. Said twice with one request id it
// is kept once. When the run's owner starts, it is given each instruction, in the order
// they were said, and each once.
//
// MUTATION: answer "not found" for a run no pod flies (the old behaviour) and nothing
// is queued; mark nothing delivered and the second drain gives them again.
func TestAnInstructionForARunNoPodHoldsIsKeptAndGivenOnce(t *testing.T) {
	eachSite(t, func(t *testing.T, s *depot, pod func(string) *coord.Coord) {
		sessionsAre(t, theRun("sess_q", agents.StatusRunning))
		a, la := podOf(t, s, pod, "cloud-0")
		b, _ := podOf(t, s, pod, "cloud-1")
		ctx := context.Background()

		// Said on pod b while no pod flies the run.
		for _, in := range []client.CodingSteerIn{tell("use the other endpoint", "r1"), tell("and add a test", "r2"), tell("use the other endpoint", "r1")} {
			if _, err := b.follow(ctx, "acme", "u-acme", in, nil); err != nil {
				t.Fatalf("a steer for a run no pod holds: %v", err)
			}
		}
		rows, err := la.inbox(ctx, "acme", "sess_q")
		if err != nil || len(rows) != 2 || rows[0].body != "use the other endpoint" || rows[1].body != "and add a test" ||
			rows[0].state != steerQueued || rows[1].state != steerQueued {
			t.Fatalf("the inbox holds %+v (%v), want the two instructions queued in order, the retry kept once", rows, err)
		}

		// The run's owner starts on pod a: it is given what was said, in order.
		turn := newTaking()
		f := flyTaking(t, a, "sess_q", turn)
		if first, second := heard(t, turn), heard(t, turn); first != "use the other endpoint" || second != "and add a test" {
			t.Fatalf("the run was given %q then %q, want the instructions in the order they were said", first, second)
		}
		f.drain() // told again, and again: nothing is given twice
		f.drain()
		if got := turn.all(); !slices.Equal(got, []string{"use the other endpoint", "and add a test"}) {
			t.Fatalf("the run was given %v, want each instruction once", got)
		}
		rows, _ = la.inbox(ctx, "acme", "sess_q")
		for _, r := range rows {
			if r.state != steerDelivered || r.delivered == 0 {
				t.Fatalf("an instruction the run took reads %+v, want delivered", r)
			}
		}
		seen := steersOf(rows)
		if len(seen) != 2 || seen[0].State != steerDelivered || seen[0].Delivered == "" || seen[0].RequestID != "r1" || seen[1].Text != "and add a test" {
			t.Fatalf("the run's changes show %+v", seen)
		}
	})
}

// An instruction said while the run is flying on another pod's turn, in the instant before
// the pod that wrote it could tell, reaches the owner by push: the pod that wrote it tells
// the bus, and the owner reads its inbox then, with no timer.
//
// MUTATION: write the row and tell no one, and the owner takes it at the next flight.
func TestAnInstructionIsPushedToTheOwner(t *testing.T) {
	eachSite(t, func(t *testing.T, s *depot, pod func(string) *coord.Coord) {
		sessionsAre(t, theRun("sess_q", agents.StatusRunning))
		a, _ := podOf(t, s, pod, "cloud-0")
		b, _ := podOf(t, s, pod, "cloud-1")
		turn := newTaking()
		f := flyTaking(t, a, "sess_q", turn)
		for f.drains.Load() == 0 { // the flight has read its inbox once, and found it empty
			time.Sleep(time.Millisecond)
		}

		// b does not fly the run and could not find it flying (queue is what follow does
		// then): the owner is told over the bus.
		if err := b.queue(context.Background(), "acme", "u-acme", tell("stop using the cache", "r9")); err != nil {
			t.Fatal(err)
		}
		if got := heard(t, turn); got != "stop using the cache" {
			t.Fatalf("the owner was given %q", got)
		}

		// And on the owner's own pod the flight is told directly: the bus does not
		// deliver a pod its own messages.
		if err := a.queue(context.Background(), "acme", "u-acme", tell("and the logger", "r10")); err != nil {
			t.Fatal(err)
		}
		if got := heard(t, turn); got != "and the logger" {
			t.Fatalf("the owner's own pod gave %q", got)
		}
	})
}

// A run that has ended refuses an instruction with the reason, and keeps nothing; one
// that is paused with no pod to continue it says so; a run that is not the person's, or
// not a coding run, is not there.
func TestAnInstructionForAnEndedRunIsRefused(t *testing.T) {
	eachSite(t, func(t *testing.T, s *depot, pod func(string) *coord.Coord) {
		other := theRun("sess_other", agents.StatusRunning)
		other.Actor = "acme/u-bob"
		chat := theRun("sess_chat", agents.StatusRunning)
		chat.Kind = "chat"
		sessionsAre(t, theRun("sess_q", agents.StatusRunning), theRun("sess_done", agents.StatusDone),
			theRun("sess_err", agents.StatusError), theRun("sess_stopped", agents.StatusStopped),
			theRun("sess_paused", agents.StatusPaused), other, chat)
		a, la := podOf(t, s, pod, "cloud-0")
		ctx := context.Background()

		for session, want := range map[string]int{
			"sess_done": http.StatusConflict, "sess_err": http.StatusConflict, "sess_stopped": http.StatusConflict,
			"sess_paused": http.StatusConflict, "sess_other": http.StatusNotFound, "sess_chat": http.StatusNotFound,
			"sess_nobody": http.StatusNotFound,
		} {
			in := tell("do it", "r-"+session)
			in.SessionID = session
			_, err := a.follow(ctx, "acme", "u-acme", in, nil)
			if code := codeOf(t, err); code != want {
				t.Errorf("%s: %v, want %d", session, err, want)
			}
			if rows, _ := la.inbox(ctx, "acme", session); len(rows) != 0 {
				t.Errorf("%s: %d instructions were queued for a run that cannot take them", session, len(rows))
			}
		}
		// Nothing to say is nothing to keep.
		if _, err := a.follow(ctx, "acme", "u-acme", tell("  ", "r-blank"), nil); codeOf(t, err) != http.StatusNotFound {
			t.Errorf("a blank instruction: %v, want not found", err)
		}
	})
}

// When the turn cannot take an instruction the run is stopped for it, and the follow-up
// carries everything still queued, in order. Nothing is delivered by stopping the run: a
// row is marked when the follow-up has started with it (TestAFollowUpCarriesWhatIsQueued).
func TestWhatTheTurnCannotTakeStopsTheRunAndStaysQueued(t *testing.T) {
	eachSite(t, func(t *testing.T, s *depot, pod func(string) *coord.Coord) {
		sessionsAre(t, theRun("sess_q", agents.StatusRunning))
		// The run never lands here, so its follow-up waits out followWait in the background;
		// the test ends first.
		a, la := podOf(t, s, pod, "cloud-0")
		ctx := context.Background()
		for _, in := range []client.CodingSteerIn{tell("first", "r1"), tell("second", "r2")} {
			if _, err := a.follow(ctx, "acme", "u-acme", in, nil); err != nil {
				t.Fatal(err)
			}
		}
		f := flyTaking(t, a, "sess_q", nil) // a turn that takes nothing
		select {
		case <-f.ctx.Done():
		case <-time.After(10 * time.Second):
			t.Fatal("the run was not stopped for the instructions waiting for it")
		}
		if cause := context.Cause(f.ctx); cause != errFollowed {
			t.Fatalf("the run's cause is %v, want stopped for a new instruction", cause)
		}
		f.draining.Lock() // the read has finished
		f.draining.Unlock()
		rows, _ := la.inbox(ctx, "acme", "sess_q")
		if len(rows) != 2 || rows[0].state != steerQueued || rows[1].state != steerQueued {
			t.Fatalf("the inbox reads %+v, want both still queued: no run has them yet", rows)
		}
		// The follow-up is started after this session, and carries what is queued on it.
		keeping(t, la)
		claim := carryAfter(ctx, "acme", "u-acme", "sess_q", "add a health check", nil)
		owed := claim.rows
		if len(owed) != 2 || owed[0].body != "first" || owed[1].body != "second" {
			t.Fatalf("the follow-up is owed %+v, want both, in order", owed)
		}
		claim.finish(ctx, nil) // the follow-up has been spawned with them
		rows, _ = la.inbox(ctx, "acme", "sess_q")
		if rows[0].state != steerDelivered || rows[1].state != steerDelivered {
			t.Fatalf("after the follow-up started the inbox reads %+v, want both delivered", rows)
		}
	})
}

// The follow-up is a run: it carries what is queued, in order, in its task, and the rows
// are delivered once it has started. One that cannot start has taken nothing.
//
// MUTATION: mark the rows when the follow-up is scheduled, and the failed start loses them.
func TestAFollowUpCarriesWhatIsQueued(t *testing.T) {
	for _, c := range []struct {
		name      string
		admitted  bool
		wantState string
	}{{"started", true, steerDelivered}, {"could not start", false, steerQueued}} {
		t.Run(c.name, func(t *testing.T) {
			r := newRig()
			if c.admitted {
				r.admit = pushes(nil, admins("hanzo/sub-bob"))
			}
			r.pod.answer = func(argv []string) client.Ran {
				if slices.Contains(argv, "rev-parse") {
					return client.Ran{Stdout: theSHA + "\n"}
				}
				return client.Ran{}
			}
			r.serve(t)
			engineOnce = sync.Once{}
			t.Cleanup(func() { engineOnce = sync.Once{} })
			l := ledgerOf(t)
			keeping(t, l)
			fl := newFleet()
			fl.keep(l)
			sessionsAre(t, agents.Session{ID: "sess_prev", Org: "hanzo", Actor: "hanzo/sub-bob", Kind: kindCoding, Status: agents.StatusStopped})
			ctx := t.Context()
			for _, in := range []struct{ id, text string }{{"r1", "use the other endpoint"}, {"r2", "and add a test"}} {
				if err := l.enqueue(ctx, "hanzo", "sess_prev", "sub-bob", in.id, in.text); err != nil {
					t.Fatal(err)
				}
			}
			f := &flight{org: "hanzo", subject: "sub-bob", session: "sess_prev", done: make(chan struct{}), ctx: context.Background(), fleet: fl,
				in: client.CodingStartIn{Repo: "github.com/hanzo-apps/site", Prompt: "fix the header"}}
			close(f.done)
			f.follow("sub-bob", client.CodingSteerIn{}, false, func(string, ...any) {})
			flights.Lock()
			next := flights.m["sess_abc123def456"]
			flights.Unlock()
			if next != nil {
				<-next.done
			}

			rows, err := l.inbox(ctx, "hanzo", "sess_prev")
			if err != nil || len(rows) != 2 || rows[0].state != c.wantState || rows[1].state != c.wantState {
				t.Fatalf("the inbox reads %+v (%v), want both %s", rows, err, c.wantState)
			}
			r.pod.mu.Lock()
			fed := strings.Join(r.pod.fed, "\n---\n")
			r.pod.mu.Unlock()
			carried := strings.Contains(fed, "Then the person who asked said:\n\nuse the other endpoint\n\nand add a test")
			if carried != c.admitted {
				t.Fatalf("the follow-up's task carries the instructions: %v, want %v:\n%s", carried, c.admitted, fed)
			}
		})
	}
}

// The task a run is given is what it was asked and, in order, what was said since.
func TestWhatWasSaidJoinsTheTask(t *testing.T) {
	rows := []steer{{body: "first"}, {body: " second "}}
	if want := "add a health check\n\nThen the person who asked said:\n\nfirst\n\nsecond"; withSaid("add a health check", rows) != want {
		t.Fatalf("the task is %q, want %q", withSaid("add a health check", rows), want)
	}
	if got := Said("add a health check", nil); got != "add a health check" {
		t.Fatalf("a run nothing was said to is given %q", got)
	}
	// A run that is not resumed reads nothing, whatever is queued.
	ctx := context.Background()
	keeping(t, ledgerOf(t))
	if err := flights.ledger().enqueue(ctx, "acme", "sess_q", "u-acme", "r1", "first"); err != nil {
		t.Fatal(err)
	}
	if got := backlog(ctx, nil, "acme", "sess_q", "u-acme", "add a health check", nil); len(got) != 0 {
		t.Fatalf("a run that is not resumed was given %v", got)
	}
}

// An instruction said to a run that is flying is said once however often it is asked and on
// whichever pod: the first delivers it, and a retry, on the pod that asked or the one that
// flies the run, answers without saying it again.
//
// MUTATION: skip the ledger in follow and the turn is told three times.
func TestASteerIsSaidOncePerRequest(t *testing.T) {
	eachSite(t, func(t *testing.T, s *depot, pod func(string) *coord.Coord) {
		flying, _ := podOf(t, s, pod, "cloud-0")
		asked, _ := podOf(t, s, pod, "cloud-1")
		turn := newTaking()
		flyTaking(t, flying, "sess_q", turn)
		ctx := context.Background()

		in := tell("use the other endpoint", "s1")
		for _, pod := range []*fleet{asked, flying, asked} {
			if _, err := pod.follow(ctx, "acme", "u-acme", in, nil); err != nil {
				t.Fatalf("steer: %v", err)
			}
		}
		if got := turn.all(); !slices.Equal(got, []string{"use the other endpoint"}) {
			t.Fatalf("the turn was told %v, want the instruction once", got)
		}
		// Another request id is another instruction.
		if _, err := asked.follow(ctx, "acme", "u-acme", tell("and add a test", "s2"), nil); err != nil {
			t.Fatal(err)
		}
		if got := turn.all(); len(got) != 2 || got[1] != "and add a test" {
			t.Fatalf("the turn was told %v, want both instructions, each once", got)
		}
	})
}

// A run's changes show what was said to it while no pod held it, and whether the run has
// taken each: queued until it has, delivered after.
//
// MUTATION: leave the inbox out of Changes and a person cannot tell whether their
// instruction arrived.
func TestChangesShowQueuedAndDeliveredSteers(t *testing.T) {
	_, sessions := servesBox(t)
	stored(t)
	l := ledgerOf(t)
	keeping(t, l)
	x := sessions[runSession]
	ctx := t.Context()
	if got, err := Changes(asCaller("sub-zoe"), runSession); err != nil || len(got.Steers) != 0 {
		t.Fatalf("a run nothing was said to shows %+v (%v), want no steers", got.Steers, err)
	}
	for _, in := range []struct{ id, text string }{{"r1", "use the other endpoint"}, {"r2", "and add a test"}} {
		if err := l.enqueue(ctx, x.Org, x.ID, "sub-zoe", in.id, in.text); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := l.queued(ctx, x.Org, x.ID)
	if err != nil || len(rows) != 2 {
		t.Fatalf("queued: %+v (%v)", rows, err)
	}
	if ok, err := l.receive(ctx, rows[0]); err != nil || !ok {
		t.Fatalf("receive: %v, %v", ok, err)
	}

	got, err := Changes(asCaller("sub-zoe"), runSession)
	if err != nil {
		t.Fatalf("Changes: %v", err)
	}
	if len(got.Steers) != 2 {
		t.Fatalf("the changes show %+v, want the two instructions", got.Steers)
	}
	first, second := got.Steers[0], got.Steers[1]
	if first.Text != "use the other endpoint" || first.State != steerDelivered || first.Delivered == "" || first.RequestID != "r1" {
		t.Fatalf("the first instruction reads %+v, want delivered", first)
	}
	if second.Text != "and add a test" || second.State != steerQueued || second.Delivered != "" || second.Said == "" {
		t.Fatalf("the second instruction reads %+v, want queued", second)
	}
}

// An instruction said straight to a flying run does not overtake the ones queued before
// it: every instruction is a row, and the flight hands rows to the run by seq. Here the
// flight's first read of its inbox is slow, and a second instruction arrives meanwhile.
//
// MUTATION: hand a direct steer to the turn without the inbox, and S2 is told before S1.
func TestADirectSteerDoesNotOvertakeTheBacklog(t *testing.T) {
	eachSite(t, func(t *testing.T, s *depot, pod func(string) *coord.Coord) {
		sessionsAre(t, theRun("sess_q", agents.StatusRunning))
		a, _ := podOf(t, s, pod, "cloud-0")
		b, _ := podOf(t, s, pod, "cloud-1")
		ctx := context.Background()
		if _, err := b.follow(ctx, "acme", "u-acme", tell("S1 first", "r1"), nil); err != nil {
			t.Fatal(err)
		}
		turn := newTaking()
		ctx2, stop := context.WithCancelCause(context.Background())
		f := &flight{org: "acme", subject: "u-acme", session: "sess_q", cancel: stop, done: make(chan struct{}), ctx: ctx2}
		f.steerer = turn.take
		f.draining.Lock() // the flight is slow to read its inbox (a slow database)
		a.fly(f)
		t.Cleanup(func() { f.forget() })
		st, err := a.follow(ctx, "acme", "u-acme", tell("S2 second", "r2"), nil)
		if err != nil {
			t.Fatal(err)
		}
		if !st.Queued {
			t.Fatalf("an instruction behind a backlog answered %+v, want it queued", st)
		}
		f.draining.Unlock()
		heard(t, turn)
		heard(t, turn)
		if got := turn.all(); !slices.Equal(got, []string{"S1 first", "S2 second"}) {
			t.Fatalf("the run was told %v, want S1 then S2", got)
		}
	})
}

// A steer whose claimer dies after the run was told, before it settles, is not told to
// the run again by the retry that retakes the claim: the instruction was recorded when it
// was applied, in the same step, and the retry finds it.
//
// MUTATION: apply a steer without recording it first, and the run is told twice.
func TestASteerReplayedAfterItsClaimerDiesIsSaidOnce(t *testing.T) {
	eachSite(t, func(t *testing.T, s *depot, pod func(string) *coord.Coord) {
		sessionsAre(t, theRun("sess_q", agents.StatusRunning))
		flying, _ := podOf(t, s, pod, "cloud-0")
		claimer, lc := podOf(t, s, pod, "cloud-1")
		retry, lr := podOf(t, s, pod, "cloud-2")
		lr.p = periods{Bound: 100 * time.Millisecond}.set()
		turn := newTaking()
		flyTaking(t, flying, "sess_q", turn)
		ctx := context.Background()
		in := tell("do X", "r1")
		k := request{org: "acme", subject: "u-acme", scope: scoped("steer", "sess_q"), id: "r1"}
		if mine, _, _, err := lc.take(ctx, k); err != nil || !mine {
			t.Fatalf("claim: %v %v", mine, err)
		}
		if _, err := claimer.followElsewhere(ctx, "acme", "u-acme", in); err != nil {
			t.Fatal(err)
		}
		heard(t, turn)
		lc.attach(nil) // the claimer's pod is gone before it settles
		if _, err := retry.follow(ctx, "acme", "u-acme", in, nil); err != nil {
			t.Fatal(err)
		}
		if got := turn.all(); !slices.Equal(got, []string{"do X"}) {
			t.Fatalf("the run was told %v, want the instruction once", got)
		}
	})
}

// A mark that fails leaves the row queued, and the flight remembers having handed it to
// its run: the next read marks it and tells the run nothing.
//
// MUTATION: forget what was handed, and the live run is told S1 twice.
func TestAFailedMarkIsNotHandedAgain(t *testing.T) {
	l := ledgerOf(t)
	fl := newFleet()
	fl.keep(l)
	ctx := t.Context()
	if err := l.enqueue(ctx, "acme", "sess_q", "u-acme", "r1", "S1"); err != nil {
		t.Fatal(err)
	}
	if _, err := l.db.Exec(`CREATE TRIGGER nomark BEFORE UPDATE ON steers BEGIN SELECT RAISE(ABORT, 'blip'); END`); err != nil {
		t.Fatal(err)
	}
	turn := newTaking()
	f := flyTaking(t, fl, "sess_q", turn)
	heard(t, turn)
	for f.drains.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	if rows, _ := l.inbox(ctx, "acme", "sess_q"); rows[0].state != steerQueued {
		t.Fatalf("the mark failed and the row reads %s, want queued", rows[0].state)
	}
	if _, err := l.db.Exec(`DROP TRIGGER nomark`); err != nil {
		t.Fatal(err)
	}
	f.drain()
	if got := turn.all(); !slices.Equal(got, []string{"S1"}) {
		t.Fatalf("the live run was told %v, want S1 once", got)
	}
	if rows, _ := l.inbox(ctx, "acme", "sess_q"); rows[0].state != steerDelivered {
		t.Fatalf("the next read left the row %s, want delivered", rows[0].state)
	}
}

// The answer says what happened to the instruction: queued when it is kept for a run that
// has not taken it, and nothing when the run has it.
//
// MUTATION: answer queued for a delivered steer, or nothing for a kept one.
func TestTheAnswerSaysQueuedOnlyForAKeptInstruction(t *testing.T) {
	eachSite(t, func(t *testing.T, s *depot, pod func(string) *coord.Coord) {
		sessionsAre(t, theRun("sess_q", agents.StatusRunning))
		flying, _ := podOf(t, s, pod, "cloud-0")
		asked, _ := podOf(t, s, pod, "cloud-1")
		ctx := context.Background()

		kept, err := asked.follow(ctx, "acme", "u-acme", tell("kept", "k1"), nil) // no pod flies the run
		if err != nil || !kept.Queued {
			t.Fatalf("an instruction for a run no pod flies answered (%+v, %v), want queued", kept, err)
		}
		turn := newTaking()
		f := flyTaking(t, flying, "sess_q", turn)
		heard(t, turn) // the owner takes what was kept
		for f.drains.Load() == 0 {
			time.Sleep(time.Millisecond)
		}
		f.draining.Lock() // its read is over: nothing is ahead of what comes next
		f.draining.Unlock()
		for _, from := range []*fleet{flying, asked} {
			told, err := from.follow(ctx, "acme", "u-acme", tell("told "+from.ledger().id, "t-"+from.ledger().id), nil)
			if err != nil || told.Queued {
				t.Fatalf("an instruction the run took answered (%+v, %v), want it delivered", told, err)
			}
		}
		// The public answer carries it: this process flies nothing, so the instruction is kept.
		keeping(t, flying.ledger())
		ack, err := Follow(ctx, "acme", "u-acme", tell("and again", "k2"), nil)
		if err != nil || !ack.OK || !ack.Queued {
			t.Fatalf("Follow answered (%+v, %v), want ok and queued", ack, err)
		}
	})
}

// A pod of the old version does not know the inbox: it reads none and writes none, and
// its flights hand nothing from it. What was queued for a run it flies waits, untouched
// and visible, and the first pod of this version to fly that run hands it over.
func TestAPodThatKeepsNoLedgerLeavesTheInboxAlone(t *testing.T) {
	eachSite(t, func(t *testing.T, s *depot, pod func(string) *coord.Coord) {
		sessionsAre(t, theRun("sess_q", agents.StatusRunning))
		old := newFleet() // keeps no ledger, as a pod of the version before this one
		current, lc := podOf(t, s, pod, "cloud-1")
		ctx := context.Background()
		if err := current.queue(ctx, "acme", "u-acme", tell("said while the old pod resumed it", "r1")); err != nil {
			t.Fatal(err)
		}

		// The old pod resumes the run: its flight reads nothing, hands nothing, marks nothing.
		oldTurn := newTaking()
		oldFlight := flyTaking(t, old, "sess_q", oldTurn)
		old.signal("acme", "sess_q")
		oldFlight.drain()
		if got := oldTurn.all(); len(got) != 0 {
			t.Fatalf("an old pod's flight was handed %v from an inbox it does not know", got)
		}
		if rows, _ := lc.inbox(ctx, "acme", "sess_q"); len(rows) != 1 || rows[0].state != steerQueued {
			t.Fatalf("the inbox reads %+v, want the row queued, as it was", rows)
		}

		// The run's next owner is a pod of this version: it hands the row over, once.
		oldFlight.forget()
		turn := newTaking()
		flyTaking(t, current, "sess_q", turn)
		if got := heard(t, turn); got != "said while the old pod resumed it" {
			t.Fatalf("the next owner was handed %q", got)
		}
	})
}

// An instruction is as long as a task prompt may be, the limit a start already has, and
// no longer: it is refused, not cut.
func TestAnInstructionIsAsLongAsATaskMayBe(t *testing.T) {
	l := ledgerOf(t)
	ctx := t.Context()
	if err := l.enqueue(ctx, "acme", "sess_q", "u-acme", "r1", strings.Repeat("a", maxPromptLen)); err != nil {
		t.Fatalf("an instruction of %d bytes: %v", maxPromptLen, err)
	}
	if err := l.enqueue(ctx, "acme", "sess_q", "u-acme", "r2", strings.Repeat("a", maxPromptLen+1)); codeOf(t, err) != http.StatusBadRequest {
		t.Fatalf("an instruction past the limit: %v, want 400", err)
	}
}

// An instruction the turn cannot take stops the run for a follow-up, and the answer says
// it is kept: it is not delivered until that follow-up has started.
//
// MUTATION: answer delivered when the read found the row still waiting.
func TestAnInstructionTheTurnCannotTakeAnswersQueued(t *testing.T) {
	eachSite(t, func(t *testing.T, s *depot, pod func(string) *coord.Coord) {
		a, la := podOf(t, s, pod, "cloud-0")
		f := flyTaking(t, a, "sess_q", nil) // a turn that takes nothing
		for f.drains.Load() == 0 {
			time.Sleep(time.Millisecond)
		}
		f.draining.Lock()
		f.draining.Unlock()
		st, err := a.follow(context.Background(), "acme", "u-acme", tell("change course", "c1"), nil)
		if err != nil || !st.Queued {
			t.Fatalf("an instruction the turn would not take answered (%+v, %v), want queued", st, err)
		}
		if cause := context.Cause(f.ctx); cause != errFollowed {
			t.Fatalf("the run's cause is %v, want stopped for a new instruction", cause)
		}
		if rows, _ := la.inbox(context.Background(), "acme", "sess_q"); len(rows) != 1 || rows[0].state != steerQueued {
			t.Fatalf("the inbox reads %+v, want the row queued until the follow-up starts", rows)
		}
	})
}

// A steer replayed on the pod that flies the run after its mark failed is handed once: the
// flight remembers what its run was handed, whichever call asks.
func TestAReplayedSteerAfterAFailedMarkIsHandedOnce(t *testing.T) {
	l := ledgerOf(t)
	fl := newFleet()
	fl.keep(l)
	sessionsAre(t, theRun("sess_q", agents.StatusRunning))
	turn := newTaking()
	f := flyTaking(t, fl, "sess_q", turn)
	ctx := context.Background()
	for f.drains.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	if _, err := l.db.Exec(`CREATE TRIGGER nomark BEFORE UPDATE ON steers BEGIN SELECT RAISE(ABORT, 'blip'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := fl.follow(ctx, "acme", "u-acme", tell("do X", "r1"), nil); err != nil {
		t.Fatal(err)
	}
	heard(t, turn)
	if _, err := f.followed(ctx, "u-acme", tell("do X", "r1"), nil); err != nil { // the replay
		t.Fatal(err)
	}
	if _, err := l.db.Exec(`DROP TRIGGER nomark`); err != nil {
		t.Fatal(err)
	}
	f.drain()
	if got := turn.all(); !slices.Equal(got, []string{"do X"}) {
		t.Fatalf("the run was told %v, want it once", got)
	}
	if rows, _ := l.inbox(ctx, "acme", "sess_q"); len(rows) != 1 || rows[0].state != steerDelivered {
		t.Fatalf("the inbox reads %+v, want the one row delivered", rows)
	}
}

// Instructions said from three pods, before a flight and while a slow turn is taking the
// backlog, are told to the run in the order they were acknowledged.
func TestSteersFromEveryPodAreToldInTheOrderTheyWereAcked(t *testing.T) {
	eachSite(t, func(t *testing.T, s *depot, pod func(string) *coord.Coord) {
		sessionsAre(t, theRun("sess_q", agents.StatusRunning))
		a, _ := podOf(t, s, pod, "cloud-0")
		b, _ := podOf(t, s, pod, "cloud-1")
		c, _ := podOf(t, s, pod, "cloud-2")
		ctx := context.Background()
		for i, txt := range []string{"S1", "S2", "S3"} {
			if _, err := []*fleet{b, c, b}[i].follow(ctx, "acme", "u-acme", tell(txt, "r"+txt), nil); err != nil {
				t.Fatal(err)
			}
		}
		slow := &slowTaking{taking: newTaking()}
		runCtx, stop := context.WithCancelCause(context.Background())
		f := &flight{org: "acme", subject: "u-acme", session: "sess_q", cancel: stop, done: make(chan struct{}), ctx: runCtx}
		f.steerer = slow.take
		a.fly(f)
		t.Cleanup(func() { f.forget() })
		for i, txt := range []string{"S4", "S5", "S6", "S7"} {
			if _, err := []*fleet{a, b, a, c}[i].follow(ctx, "acme", "u-acme", tell(txt, "r"+txt), nil); err != nil {
				t.Fatal(err)
			}
		}
		for range 7 {
			heard(t, slow.taking)
		}
		if got, want := slow.all(), []string{"S1", "S2", "S3", "S4", "S5", "S6", "S7"}; !slices.Equal(got, want) {
			t.Fatalf("the run was told %v, want %v", got, want)
		}
	})
}

// slowTaking is a turn that takes its time over each instruction.
type slowTaking struct{ *taking }

func (k *slowTaking) take(text string) bool {
	time.Sleep(20 * time.Millisecond)
	return k.taking.take(text)
}

// The same request id with the instruction spelled another way (padded) is the same
// instruction, whatever else the retry carries (the message it narrates into, the thread);
// a different instruction under the id is refused.
func TestASteerRetriedInAnotherSpellingIsTheSameInstruction(t *testing.T) {
	eachSite(t, func(t *testing.T, s *depot, pod func(string) *coord.Coord) {
		sessionsAre(t, theRun("sess_q", agents.StatusRunning))
		b, _ := podOf(t, s, pod, "cloud-1")
		c, _ := podOf(t, s, pod, "cloud-2")
		ctx := context.Background()
		if _, err := b.follow(ctx, "acme", "u-acme", tell("use the other endpoint", "r1"), nil); err != nil {
			t.Fatal(err)
		}
		if _, err := c.follow(ctx, "acme", "u-acme", tell("something else entirely", "r1"), nil); codeOf(t, err) != http.StatusUnprocessableEntity {
			t.Fatalf("another instruction under r1: %v, want 422", err)
		}
		if _, err := c.follow(ctx, "acme", "u-acme", tell("  use the other endpoint \n", "r1"), nil); err != nil {
			t.Fatalf("the padded retry: %v", err)
		}
		in := tell("use the other endpoint", "r1")
		in.Update, in.Reply = "1731.22", client.Reply{Thread: "999"}
		if _, err := c.follow(ctx, "acme", "u-acme", in, nil); err != nil {
			t.Fatalf("a retry that narrates elsewhere: %v", err)
		}
	})
}
