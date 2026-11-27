package coding

// inbox_fit_test.go — an instruction reaches a run whole or waits. The task a run is given
// is cut at the prompt limit, so what is joined to it is joined whole, in order, as many as
// fit; the rest stay queued. And an instruction is kept only for a run a pod of this cloud
// can resume.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/apps/agents"
	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/coord"
	"github.com/hanzoai/cloud/sdk/coord/coordtest"
)

// What fits is each row that still fits, in order: one that does not is left, and the ones
// behind it that do fit are not held up by it. Carried rows keep their order.
func TestFitTakesWholeInstructionsInOrder(t *testing.T) {
	task := strings.Repeat("t", 2000)
	small := steer{body: "small"}
	big := steer{body: strings.Repeat("x", maxPromptLen-1000)}
	for name, c := range map[string]struct {
		rows       []steer
		took, left int
	}{
		"none":                        {nil, 0, 0},
		"all fit":                     {[]steer{small, small}, 2, 0},
		"one too big is stepped over": {[]steer{small, big, small}, 2, 1},
		"the first too big":           {[]steer{big, small}, 1, 1},
	} {
		took, left := fit(task, c.rows)
		if len(took) != c.took || len(left) != c.left {
			t.Errorf("%s: %d fit and %d left, want %d and %d", name, len(took), len(left), c.took, c.left)
		}
	}
	// A task already at the limit has room for nothing.
	if took, left := fit(strings.Repeat("t", maxPromptLen), []steer{small}); len(took) != 0 || len(left) != 1 {
		t.Errorf("a task at the limit was given %d rows", len(took))
	}
	// What fits, joined, is within the limit Run cuts at, whatever the task: one that says
	// nothing, one with blank lines around it, one that is long.
	for _, task := range []string{"", "\n\n", task, strings.Repeat("t", maxPromptLen-100)} {
		for _, rows := range [][]steer{{small, small}, {small, big}, {big, small, big, small}} {
			took, _ := fit(task, rows)
			if joined := strings.TrimSpace(withSaid(task, took)); len(joined) > maxPromptLen {
				t.Errorf("%d rows joined to a task of %d bytes make %d, past the limit", len(took), len(task), len(joined))
			}
		}
	}
	// A row no task can hold is told apart from one that only this task cannot.
	if !never(steer{body: strings.Repeat("z", maxPromptLen)}) || never(big) || never(small) {
		t.Error("never does not tell a row that fits no task from one that fits some")
	}
	// The widest a row can be is where a task has no room: one byte more fits none.
	if never(steer{body: strings.Repeat("z", widest)}) || !never(steer{body: strings.Repeat("z", widest+1)}) {
		t.Errorf("a row of %d bytes should be the widest that fits a task", widest)
	}
}

// What fits is found in one pass over the rows, not a join of every prefix: a thousand
// queued rows of 30 KiB are read in a blink, where the prefix search took seconds.
//
// MUTATION: search the prefixes (the old fit) and 2000 rows take seconds.
func TestFitIsOnePassOverTheRows(t *testing.T) {
	rows := make([]steer, 2000)
	for i := range rows {
		rows[i] = steer{seq: int64(i), body: strings.Repeat("x", 30000)}
	}
	rows[0].body = "small"
	began := time.Now()
	took, left := fit("go", rows)
	if len(took) != 2 || len(left) != len(rows)-2 { // the small one and the first 30 KiB one
		t.Fatalf("%d rows fit and %d are left", len(took), len(left))
	}
	if took := time.Since(began); took > 250*time.Millisecond {
		t.Fatalf("fit of %d rows took %v", len(rows), took)
	}
}

// A resumed task with a backlog that does not fit it is not cut: the run is given the
// instructions that fit, whole and in order, and the session records those, and the rest
// stay queued. The instruction that does not fit is not marked delivered with its tail
// dropped.
//
// MUTATION: join every row (the old rule) and Run cuts the last one, which is marked
// delivered and recorded.
func TestAResumedTaskIsNotCutToHoldWhatWasSaid(t *testing.T) {
	pod := coordtest.Memory(t)
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		r := gitHubRun(t, map[string]string{})
		s := &depot{t: t, open: open}
		l := s.replica(pod, "cloud-0", periods{})
		keeping(t, l)
		const session, org = "sess_abc123def456", "hanzo"
		ctx := t.Context()
		tail := "TAILMARK-the-instruction-that-matters"
		big := strings.Repeat("x", 31000) + tail
		for _, in := range []struct{ id, text string }{{"r1", "use the other endpoint"}, {"r2", big}, {"r3", "and add a test"}} {
			if err := l.enqueue(ctx, org, session, "sub-zoe", in.id, in.text); err != nil {
				t.Fatal(err)
			}
		}
		x := agents.Session{ID: session, Org: org, Actor: org + "/sub-zoe", Kind: kindCoding, Box: "sbx_old",
			Repo: "hanzo-apps/site", Base: "main", Branch: "agent/abc", Status: agents.StatusRunning}
		engineOnce = sync.Once{}
		t.Cleanup(func() { engineOnce = sync.Once{} })
		if !Resume(asCaller("sub-zoe"), x, client.CodingStartIn{Prompt: strings.Repeat("t", 2000)}, nil) {
			t.Fatal("not resumed")
		}
		flights.Lock()
		f := flights.m[session]
		flights.Unlock()
		if f != nil {
			select {
			case <-f.done:
			case <-time.After(20 * time.Second):
				t.Fatal("never ended")
			}
		}

		r.pod.mu.Lock()
		fed := strings.Join(r.pod.fed, "\n---\n")
		r.pod.mu.Unlock()
		if !strings.Contains(fed, "use the other endpoint") {
			t.Fatalf("the instruction that fits was not given to the run (%d bytes fed)", len(fed))
		}
		if strings.Contains(fed, "xxxxxxxxxx") || strings.Contains(fed, tail) || !strings.Contains(fed, "and add a test") {
			t.Fatalf("the run was given an instruction that did not fit, or not the one behind it that did (%d bytes fed)", len(fed))
		}
		rows, err := l.inbox(ctx, org, session)
		if err != nil || len(rows) != 3 || rows[0].state != steerDelivered || rows[1].state != steerQueued || rows[2].state != steerDelivered {
			t.Fatalf("the inbox reads %+v (%v), want the two that fit delivered and the one that did not queued", rows, err)
		}
		r.sessions.mu.Lock()
		defer r.sessions.mu.Unlock()
		recorded := 0
		for _, e := range r.sessions.events {
			if e.kind == kindMessage && strings.Contains(e.payload, `"queued":true`) {
				recorded++
				if strings.Contains(e.payload, tail) {
					t.Fatalf("the session recorded the instruction that was not given: %s", e.payload[:80])
				}
			}
		}
		if recorded != 2 {
			t.Fatalf("the session recorded %d queued instructions, want the two the run was given", recorded)
		}
		if cause := context.Cause(flightCtx(f)); errors.Is(cause, errFollowed) {
			t.Fatalf("the resumed run was stopped for an instruction no follow-up could carry: %v", cause)
		}
	})
}

func flightCtx(f *flight) context.Context {
	if f == nil {
		return context.Background()
	}
	return f.ctx
}

// An instruction that fits no task beside the run's own is not left for a follow-up that
// could not carry it either: the run is not stopped for it. A live turn takes an
// instruction of any size, so it waits queued until one begins, and is told then.
//
// MUTATION: redirect without asking whether a follow-up could carry it, and the run is
// stopped for an instruction nobody can give it.
func TestAnInstructionTooLongBesideATaskWaitsForALiveTurn(t *testing.T) {
	l := ledgerOf(t)
	fl := newFleet()
	fl.keep(l)
	ctx := t.Context()
	big := strings.Repeat("x", maxPromptLen-1000)
	if err := l.enqueue(ctx, "acme", "sess_q", "u-acme", "r1", big); err != nil {
		t.Fatal(err)
	}
	runCtx, stop := context.WithCancelCause(context.Background())
	f := &flight{org: "acme", subject: "u-acme", session: "sess_q", cancel: stop, done: make(chan struct{}), ctx: runCtx,
		in: client.CodingStartIn{Repo: "api", Prompt: strings.Repeat("t", 4000)}}
	fl.fly(f)
	t.Cleanup(func() { f.forget() })
	for f.drains.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	f.draining.Lock()
	f.draining.Unlock()
	if cause := context.Cause(runCtx); errors.Is(cause, errFollowed) {
		t.Fatalf("the run was stopped for an instruction no follow-up could carry: %v", cause)
	}
	if rows, _ := l.inbox(ctx, "acme", "sess_q"); rows[0].state != steerQueued {
		t.Fatalf("the instruction reads %s, want queued", rows[0].state)
	}

	// A turn begins: what waited is handed to it, whole.
	turn := newTaking()
	f.setSteerer(turn.take)
	if got := heard(t, turn); got != big {
		t.Fatalf("the turn was given %d bytes, want the whole %d", len(got), len(big))
	}
}

// An instruction is kept only for a run a pod of this cloud can fly or resume. One on a
// machine of the person's own is never flown by a pod, and one with no sandbox cannot be
// resumed: both answer as they did before the inbox, not found, and keep nothing.
//
// MUTATION: queue whatever is running and the steer is acked queued for a run nobody takes.
func TestAnInstructionForARunNoPodCanResumeIsNotKept(t *testing.T) {
	eachSite(t, func(t *testing.T, s *depot, pod func(string) *coord.Coord) {
		onAMachine := theRun("sess_machine", agents.StatusRunning)
		onAMachine.Target = "machine_1"
		noSandbox := theRun("sess_nobox", agents.StatusRunning)
		noSandbox.Box = ""
		sessionsAre(t, onAMachine, noSandbox, theRun("sess_q", agents.StatusRunning))
		b, lb := podOf(t, s, pod, "cloud-1")
		ctx := context.Background()
		for _, session := range []string{"sess_machine", "sess_nobox"} {
			in := tell("use the other endpoint", "r-"+session)
			in.SessionID = session
			st, err := b.follow(ctx, "acme", "u-acme", in, nil)
			if codeOf(t, err) != http.StatusNotFound || st.Queued {
				t.Errorf("%s: answered (%+v, %v), want not found", session, st, err)
			}
			if rows, _ := lb.inbox(ctx, "acme", session); len(rows) != 0 {
				t.Errorf("%s: %d instructions were kept for a run no pod can resume", session, len(rows))
			}
		}
		// A run a pod can resume is still kept.
		if st, err := b.follow(ctx, "acme", "u-acme", tell("use the other endpoint", "r-ok"), nil); err != nil || !st.Queued {
			t.Fatalf("an instruction for a resumable run answered (%+v, %v), want queued", st, err)
		}
	})
}

// A start after a run carries what was left queued on it: the instructions its person said
// that no run took, whole and in order, as many as fit, in the new run's task. They are
// delivered once the new run has been spawned with them; another person's, and any that did
// not fit, stay queued and expire with the inbox.
//
// MUTATION: start without reading the earlier run's inbox and they are never carried.
func TestAStartAfterARunCarriesWhatWasLeftQueued(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		pod := coordtest.Memory(t)
		s := &depot{t: t, open: open}
		r := gitHubRun(t, map[string]string{})
		l := s.replica(pod, "cloud-0", periods{})
		keeping(t, l)
		earlier := agents.Session{ID: "sess_before", Org: "hanzo", Actor: "hanzo/sub-zoe", Kind: kindCoding, Status: agents.StatusError}
		sessionsAre(t, earlier)
		ctx := t.Context()
		for _, in := range []struct{ id, who, text string }{
			{"r1", "sub-zoe", "use the other endpoint"},
			{"r2", "sub-zoe", "and add a test"},
			{"r3", "sub-bob", "someone else's word"},
			// fits a task that says nothing else, and no task beside this one's
			{"r4", "sub-zoe", strings.Repeat("y", maxPromptLen-len(saidLead)-2-5)},
			{"r5", "sub-zoe", strings.Repeat("z", maxPromptLen)}, // fits no task at all
			{"r6", "sub-zoe", "last word"},                       // behind them, and fits
		} {
			if err := l.enqueue(ctx, "hanzo", "sess_before", in.who, in.id, in.text); err != nil {
				t.Fatal(err)
			}
		}

		// A start that follows no run carries nothing.
		if _, err := r.startAs(t, asCaller("sub-zoe"), "sub-zoe", client.CodingStartIn{Repo: "github.com/hanzo-apps/site", Prompt: "fix the header"}); err != nil {
			t.Fatal(err)
		}
		if rows, _ := l.inbox(ctx, "hanzo", "sess_before"); rows[0].state != steerQueued {
			t.Fatalf("a start after nothing took %+v", rows[0])
		}

		acc, err := r.startAs(t, asCaller("sub-zoe"), "sub-zoe",
			client.CodingStartIn{Repo: "github.com/hanzo-apps/site", Prompt: "fix the header", After: "sess_before"})
		if err != nil {
			t.Fatal(err)
		}
		// The answer says what became of the queue: three in the task (the third is the one
		// behind the two that did not fit), one left that fits no task beside this one's, one
		// dropped that fits none, and in words.
		if acc.Carried != 3 || acc.Left != 1 || acc.Dropped != 1 || !strings.Contains(acc.Note, "3 of the instructions") ||
			!strings.Contains(acc.Note, "1 did not fit") || !strings.Contains(acc.Note, "1 fit no task (more than "+strconv.Itoa(widest)+" bytes)") {
			t.Fatalf("the start answered %+v, want 3 carried, 1 left and 1 dropped, said in its note", acc)
		}
		r.pod.mu.Lock()
		fed := strings.Join(r.pod.fed, "\n---\n")
		r.pod.mu.Unlock()
		if !strings.Contains(fed, "Then the person who asked said:\n\nuse the other endpoint\n\nand add a test\n\nlast word") {
			t.Fatalf("the new run's task does not carry the three, in order, over the one that did not fit:\n%s", fed)
		}
		if strings.Contains(fed, "someone else's word") || strings.Contains(fed, "yyyyyyyyyy") || strings.Contains(fed, "zzzzzzzzzz") {
			t.Fatal("the new run was given another person's instruction, or one that did not fit")
		}
		rows, err := l.inbox(ctx, "hanzo", "sess_before")
		want := []string{steerDelivered, steerDelivered, steerQueued, steerQueued, steerDropped, steerDelivered}
		got := make([]string, len(rows))
		for i, row := range rows {
			got[i] = row.state
		}
		if err != nil || !slices.Equal(got, want) {
			t.Fatalf("the inbox reads %v (%v), want %v", got, err, want)
		}
	})
}

// What the run's own turn was handed, and whose mark was missed, is not carried again by the
// follow-up the run is stopped for: it is marked delivered first, and the follow-up is
// started with what the turn did not take.
//
// MUTATION: carry every queued row and the follow-up repeats what the run was told.
func TestAFollowUpDoesNotRepeatWhatTheRunWasHanded(t *testing.T) {
	l := ledgerOf(t)
	keeping(t, l)
	fl := newFleet()
	fl.keep(l)
	ctx := t.Context()
	for _, in := range []struct{ id, text string }{{"r1", "S1"}, {"r2", "S2"}} {
		if err := l.enqueue(ctx, "acme", "sess_q", "u-acme", in.id, in.text); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := l.db.Exec(`CREATE TRIGGER nomark BEFORE UPDATE ON steers BEGIN SELECT RAISE(ABORT, 'blip'); END`); err != nil {
		t.Fatal(err)
	}
	turn := newTaking()
	runCtx, stop := context.WithCancelCause(context.Background())
	f := &flight{org: "acme", subject: "u-acme", session: "sess_q", cancel: stop, done: make(chan struct{}), ctx: runCtx,
		in: client.CodingStartIn{Repo: "api", Prompt: "add a health check"}}
	f.steerer = func(text string) bool { return text == "S1" && turn.take(text) } // takes S1, then no more
	fl.fly(f)
	t.Cleanup(func() { f.forget() })
	heard(t, turn)
	for f.drains.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	f.draining.Lock()
	f.draining.Unlock()
	if _, err := l.db.Exec(`DROP TRIGGER nomark`); err != nil {
		t.Fatal(err)
	}
	f.settle()
	owed := carryAfter(ctx, "acme", "u-acme", "sess_q", "add a health check", nil).rows
	if len(owed) != 1 || owed[0].body != "S2" {
		t.Fatalf("the follow-up is owed %+v, want S2 alone", owed)
	}
	if rows, _ := l.inbox(ctx, "acme", "sess_q"); rows[0].state != steerDelivered {
		t.Fatalf("what the run was handed reads %s, want delivered", rows[0].state)
	}
}

// A run whose harness takes nothing while it works is told by being stopped, and its
// follow-up carries what is queued in one task. An instruction that would not fit there
// beside the task and what is already queued is refused when it is said, naming the limit,
// and nothing is kept: a run is not left to be stopped for a word it never gets. A smaller
// one after it is still taken, and still redirects the run. A run that has live turns (dev)
// takes one of any size, and it waits for a turn.
//
// MUTATION: accept whatever is said (or ask no harness) and the 15 KB word is acked queued,
// kept for a follow-up that cannot carry it, and the run is never redirected.
func TestAnInstructionNoFollowUpCouldCarryIsRefusedWhenSaid(t *testing.T) {
	eachSite(t, func(t *testing.T, s *depot, pod func(string) *coord.Coord) {
		sessionsAre(t, theRun("sess_q", agents.StatusRunning), theRun("sess_dev", agents.StatusRunning))
		a, la := podOf(t, s, pod, "cloud-0")
		fly := func(session, tool string) (*flight, context.Context) {
			ctx, stop := context.WithCancelCause(context.Background())
			f := &flight{org: "acme", subject: "u-acme", session: session, cancel: stop, done: make(chan struct{}), ctx: ctx,
				in: client.CodingStartIn{Repo: "api", Prompt: strings.Repeat("t", 20000), Tool: tool}} // steerer stays nil: no live turn yet
			a.fly(f)
			t.Cleanup(func() { f.forget() })
			return f, ctx
		}
		ctx := t.Context()
		said := func(session, text, request string) client.CodingSteerIn {
			in := tell(text, request)
			in.SessionID = session
			return in
		}

		f, runCtx := fly("sess_q", "codex")
		_, err := f.followed(ctx, "u-acme", said("sess_q", strings.Repeat("e", 15000), "r1"), nil)
		if codeOf(t, err) != http.StatusBadRequest || !strings.Contains(err.Error(), strconv.Itoa(maxPromptLen)) {
			t.Fatalf("an instruction no follow-up could carry answered %v, want 400 naming the limit (%d)", err, maxPromptLen)
		}
		time.Sleep(100 * time.Millisecond)
		if cause := context.Cause(runCtx); cause != nil {
			t.Fatalf("the run was stopped for an instruction that was refused: %v", cause)
		}
		if rows, _ := la.inbox(ctx, "acme", "sess_q"); len(rows) != 0 {
			t.Fatalf("a refused instruction was kept: %+v", rows)
		}
		// A smaller word is taken, and redirects the run.
		st, err := f.followed(ctx, "u-acme", said("sess_q", "also: use port 8080", "r2"), nil)
		if err != nil || !st.Queued {
			t.Fatalf("a small instruction answered (%+v, %v), want queued", st, err)
		}
		select {
		case <-runCtx.Done():
		case <-time.After(10 * time.Second):
			t.Fatal("the small instruction never redirected the run")
		}
		if cause := context.Cause(runCtx); cause != errFollowed {
			t.Fatalf("the run's cause is %v, want stopped for a new instruction", cause)
		}

		// A dev run takes it as a live turn, whatever its size: kept, and the run goes on.
		dev, devCtx := fly("sess_dev", "")
		st, err = dev.followed(ctx, "u-acme", said("sess_dev", strings.Repeat("e", 15000), "d1"), nil)
		if err != nil || !st.Queued {
			t.Fatalf("a dev run's big instruction answered (%+v, %v), want queued", st, err)
		}
		time.Sleep(100 * time.Millisecond)
		if cause := context.Cause(devCtx); cause != nil {
			t.Fatalf("a dev run was stopped for an instruction its next turn takes: %v", cause)
		}
	})
}

// A word for a run no pod holds is kept only if it can reach the run: it fits, beside what
// is already waiting, in the one task a resume carries. One that does not is refused when
// it is said, naming the limit. A retry of one already kept is not counted twice.
//
// MUTATION: keep whatever is running and the 15 KB word is acked queued behind the 20 KB
// one, where no resume can carry both.
func TestAnInstructionForARunNoPodHoldsIsRefusedWhenItWillNotFit(t *testing.T) {
	eachSite(t, func(t *testing.T, s *depot, pod func(string) *coord.Coord) {
		sessionsAre(t, theRun("sess_q", agents.StatusRunning))
		b, lb := podOf(t, s, pod, "cloud-1")
		ctx := context.Background()
		if st, err := b.follow(ctx, "acme", "u-acme", tell(strings.Repeat("a", 20000), "r1"), nil); err != nil || !st.Queued {
			t.Fatalf("a 20 KB instruction answered (%+v, %v), want queued", st, err)
		}
		// Its request asked again, as a retaken claim does: kept already, and not counted
		// twice against the task.
		if err := b.queue(ctx, "acme", "u-acme", tell(strings.Repeat("a", 20000), "r1")); err != nil {
			t.Fatalf("the retry of a kept instruction: %v", err)
		}
		_, err := b.follow(ctx, "acme", "u-acme", tell(strings.Repeat("b", 15000), "r2"), nil)
		if codeOf(t, err) != http.StatusBadRequest || !strings.Contains(err.Error(), strconv.Itoa(maxPromptLen)) {
			t.Fatalf("an instruction that fits no resume beside the queue answered %v, want 400 naming the limit (%d)", err, maxPromptLen)
		}
		if rows, _ := lb.inbox(ctx, "acme", "sess_q"); len(rows) != 1 {
			t.Fatalf("the inbox holds %d instructions, want only the first", len(rows))
		}
		if st, err := b.follow(ctx, "acme", "u-acme", tell("small", "r3"), nil); err != nil || !st.Queued {
			t.Fatalf("a small instruction answered (%+v, %v), want queued", st, err)
		}
	})
}

// Of any number of starts after one run, each instruction left on it goes to one: the rows
// are claimed one by one, and a claim a start lost is not its to carry. A start that read
// the rows before another claimed them finds them gone.
//
// MUTATION: claim without matching the state the row was read in (or mark with no claim,
// as the carry did) and two starts carry the same instruction.
func TestTwoStartsAfterOneRunCarryEachInstructionOnce(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		pod := coordtest.Memory(t)
		s := &depot{t: t, open: open}
		l := s.replica(pod, "cloud-0", periods{})
		keeping(t, l)
		sessionsAre(t, agents.Session{ID: "sess_before", Org: "hanzo", Actor: "hanzo/sub-zoe", Kind: kindCoding, Status: agents.StatusError})
		ctx := t.Context()
		for i := range 12 {
			if err := l.enqueue(ctx, "hanzo", "sess_before", "sub-zoe", fmt.Sprintf("r%d", i), fmt.Sprintf("word %d", i)); err != nil {
				t.Fatal(err)
			}
		}

		// Two starts that read the same rows before either claimed one: one claims each.
		rows, err := l.unfinished(ctx, "hanzo", "sess_before", "")
		if err != nil || len(rows) != 12 {
			t.Fatalf("the rows read as %d (%v), want 12", len(rows), err)
		}
		now := l.now().UnixMilli()
		first, err1 := l.move(ctx, rows[0], steerCarrying, now)
		second, err2 := l.move(ctx, rows[0], steerCarrying, now+1)
		if err1 != nil || err2 != nil || !first || second {
			t.Fatalf("two claims of one row answered (%v, %v) and (%v, %v), want exactly the first to win", first, err1, second, err2)
		}

		// Many starts at once: no instruction is in two tasks, and every one claimed is
		// in one.
		var (
			mu    sync.Mutex
			seen  = map[string]int{}
			start = make(chan struct{})
			wg    sync.WaitGroup
		)
		for range 16 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				c := carryAfter(ctx, "hanzo", "sub-zoe", "sess_before", "go", nil)
				mu.Lock()
				defer mu.Unlock()
				for _, row := range c.rows {
					seen[row.body]++
				}
			}()
		}
		close(start)
		wg.Wait()
		for body, n := range seen {
			if n != 1 {
				t.Errorf("%q went into %d tasks, want 1", body, n)
			}
		}
		var claimed int
		all, _ := l.inbox(ctx, "hanzo", "sess_before")
		for _, row := range all {
			if row.state == steerCarrying {
				claimed++
			}
		}
		if len(seen) != claimed-1 { // the first row was claimed by the test, above
			t.Errorf("%d instructions went into a task and %d are claimed", len(seen), claimed-1)
		}
	})
}

// A claimed instruction is not lost when its start is: the claim is given back when the
// start is refused, and is abandoned after the ceiling a claim on a request has, when the
// pod died between claiming and spawning. Either way the next start after the run carries
// it. A late finish of the abandoned claim does not undo the claim that replaced it.
//
// MUTATION: never give a claim back (or never let one expire) and the instruction is
// carried by nobody; let finish move a row it no longer holds and it is delivered twice over.
func TestAClaimedInstructionComesBackWhenItsStartIsGone(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		pod := coordtest.Memory(t)
		s := &depot{t: t, open: open}
		l := s.replica(pod, "cloud-0", periods{Ceiling: 150 * time.Millisecond})
		keeping(t, l)
		sessionsAre(t, agents.Session{ID: "sess_before", Org: "hanzo", Actor: "hanzo/sub-zoe", Kind: kindCoding, Status: agents.StatusError})
		ctx := t.Context()
		for i, text := range []string{"first", "second"} {
			if err := l.enqueue(ctx, "hanzo", "sess_before", "sub-zoe", fmt.Sprintf("r%d", i), text); err != nil {
				t.Fatal(err)
			}
		}
		bodies := func(rows []steer) string {
			var out []string
			for _, row := range rows {
				out = append(out, row.body)
			}
			return strings.Join(out, ",")
		}
		carry := func() carrying { return carryAfter(ctx, "hanzo", "sub-zoe", "sess_before", "go", nil) }

		held := carry()
		if bodies(held.rows) != "first,second" {
			t.Fatalf("the start claimed %q, want both", bodies(held.rows))
		}
		if again := carry(); len(again.rows) != 0 {
			t.Fatalf("a second start took %q while the first held them", bodies(again.rows))
		}
		held.release(ctx, nil) // the start was refused
		lost := carry()
		if bodies(lost.rows) != "first,second" {
			t.Fatalf("after the start was refused the next claimed %q, want both again", bodies(lost.rows))
		}

		// This start dies holding them. Until the ceiling nobody takes them; after it, the
		// next start does.
		if early := carry(); len(early.rows) != 0 {
			t.Fatalf("a start took %q before the claim was old", bodies(early.rows))
		}
		time.Sleep(300 * time.Millisecond)
		next := carry()
		if bodies(next.rows) != "first,second" {
			t.Fatalf("after the ceiling the next start claimed %q, want both", bodies(next.rows))
		}
		lost.finish(ctx, nil) // the dead start wakes: its claim is not the rows' now
		rows, _ := l.inbox(ctx, "hanzo", "sess_before")
		if rows[0].state != steerCarrying || rows[1].state != steerCarrying {
			t.Fatalf("a late finish moved rows it no longer held: %+v", rows)
		}
		next.finish(ctx, nil)
		rows, _ = l.inbox(ctx, "hanzo", "sess_before")
		if rows[0].state != steerDelivered || rows[1].state != steerDelivered {
			t.Fatalf("the claimant's finish left %+v, want both delivered", rows)
		}
	})
}

// A start that lost its claim on the request spawns nothing, and gives back what it had
// claimed from the run it follows: the next start after that run carries it.
//
// MUTATION: refuse the start without giving the claim back and the instruction is held by
// a start that never ran, and the next start carries nothing; set the oversized one aside
// before the start is committed and the refused start has dropped it for good.
func TestAStartThatLostItsClaimGivesBackWhatItCarried(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		s := &depot{t: t, open: open}
		pod := coordtest.Memory(t)
		r := gitHubRun(t, map[string]string{})
		l := s.replica(pod, "cloud-0", periods{})
		keeping(t, l)
		sessionsAre(t, agents.Session{ID: "sess_before", Org: "hanzo", Actor: "hanzo/sub-zoe", Kind: kindCoding, Status: agents.StatusError})
		if err := l.enqueue(t.Context(), "hanzo", "sess_before", "sub-zoe", "r1", "UNIQUEWORD"); err != nil {
			t.Fatal(err)
		}
		// One that fits no task: it is set aside when a run is spawned, not by a start that
		// is refused, so the retry still has it to report.
		if err := l.enqueue(t.Context(), "hanzo", "sess_before", "sub-zoe", "r2", strings.Repeat("z", maxPromptLen)); err != nil {
			t.Fatal(err)
		}
		r.sessions.onTag = func() {
			if _, err := s.db.Exec(`UPDATE requests SET owner = 'cloud-9#thief' WHERE id = 'req-lost'`); err != nil {
				t.Errorf("take over: %v", err)
			}
		}
		in := client.CodingStartIn{Repo: "github.com/hanzo-apps/site", Prompt: "go on", After: "sess_before", RequestID: "req-lost"}
		if _, err := r.startAs(t, asCaller("sub-zoe"), "sub-zoe", in); codeOf(t, err) != http.StatusConflict {
			t.Fatalf("a start that lost its claim answered %v, want 409", err)
		}
		if rows, _ := l.inbox(t.Context(), "hanzo", "sess_before"); rows[0].state != steerQueued || rows[1].state != steerQueued {
			t.Fatalf("the instructions read %s and %s after the refused start, want both queued", rows[0].state, rows[1].state)
		}
		r.sessions.onTag = nil
		in.RequestID = "req-ok"
		acc, err := r.startAs(t, asCaller("sub-zoe"), "sub-zoe", in)
		if err != nil || acc.Carried != 1 || acc.Dropped != 1 {
			t.Fatalf("the next start after the run answered (%+v, %v), want it to carry one and report one dropped", acc, err)
		}
		if rows, _ := l.inbox(t.Context(), "hanzo", "sess_before"); rows[0].state != steerDelivered || rows[1].state != steerDropped {
			t.Fatalf("after the run was spawned the instructions read %s and %s, want delivered and dropped", rows[0].state, rows[1].state)
		}
	})
}

// A start that dies holding the rows it claimed leaves them carrying, and the retry of that
// request, which is the only attempt at it that runs, takes them back at once: it does not
// start a run without them and leave them to the ceiling. Another request waits for the
// ceiling.
//
// MUTATION: write no request id into the claim and the retry carries nothing; let any
// start take a claim back and the other request carries what the dead attempt holds.
func TestARetriedStartTakesBackWhatItsDeadAttemptClaimed(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		s := &depot{t: t, open: open}
		pod := coordtest.Memory(t)
		r := gitHubRun(t, map[string]string{})
		l := s.replica(pod, "cloud-0", periods{})
		keeping(t, l)
		sessionsAre(t, agents.Session{ID: "sess_before", Org: "hanzo", Actor: "hanzo/sub-zoe", Kind: kindCoding, Status: agents.StatusError})
		ctx := t.Context()
		for i, text := range []string{"FIRSTWORD", "SECONDWORD"} {
			if err := l.enqueue(ctx, "hanzo", "sess_before", "sub-zoe", fmt.Sprintf("r%d", i), text); err != nil {
				t.Fatal(err)
			}
		}

		// The attempt at req-dead claims them and dies: no release, no finish.
		dead := withClaim(ctx, &claim{l: l, k: request{org: "hanzo", subject: "sub-zoe", scope: "start", id: "req-dead"}})
		if held := carryAfter(dead, "hanzo", "sub-zoe", "sess_before", "go on", nil); len(held.rows) != 2 {
			t.Fatalf("the attempt that died claimed %d rows, want 2", len(held.rows))
		}

		in := client.CodingStartIn{Repo: "github.com/hanzo-apps/site", Prompt: "go on", After: "sess_before", RequestID: "req-other"}
		other, err := r.startAs(t, asCaller("sub-zoe"), "sub-zoe", in)
		if err != nil || other.Carried != 0 {
			t.Fatalf("another request answered (%+v, %v), want it to leave what the dead attempt holds", other, err)
		}
		in.RequestID = "req-dead"
		retry, err := r.startAs(t, asCaller("sub-zoe"), "sub-zoe", in)
		if err != nil || retry.Carried != 2 {
			t.Fatalf("the retry answered (%+v, %v), want it to carry the two rows its dead attempt claimed", retry, err)
		}
		r.pod.mu.Lock()
		fed := strings.Join(r.pod.fed, "\n---\n")
		r.pod.mu.Unlock()
		if !strings.Contains(fed, "FIRSTWORD\n\nSECONDWORD") {
			t.Fatalf("the retry's run was not given the rows:\n%s", fed)
		}
		if rows, _ := l.inbox(ctx, "hanzo", "sess_before"); rows[0].state != steerDelivered || rows[1].state != steerDelivered {
			t.Fatalf("the rows read %s and %s, want both delivered", rows[0].state, rows[1].state)
		}
	})
}

// A claim a request takes back is later than the one it replaces, whatever the clocks: the
// attempt that held it, if it was only slow, can no longer move the rows.
//
// MUTATION: stamp the retaken claim with the time of day alone and, on a clock behind the
// one that made the first claim, it is earlier, and the slow attempt's time still matches.
func TestARetakenClaimIsLaterThanTheOneItReplaces(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		s := &depot{t: t, open: open}
		pod := coordtest.Memory(t)
		l := s.replica(pod, "cloud-0", periods{})
		keeping(t, l)
		sessionsAre(t, agents.Session{ID: "sess_before", Org: "hanzo", Actor: "hanzo/sub-zoe", Kind: kindCoding, Status: agents.StatusError})
		ctx := t.Context()
		if err := l.enqueue(ctx, "hanzo", "sess_before", "sub-zoe", "r1", "word"); err != nil {
			t.Fatal(err)
		}
		holder := withClaim(ctx, &claim{l: l, k: request{org: "hanzo", subject: "sub-zoe", scope: "start", id: "req-a"}})
		slow := carryAfter(holder, "hanzo", "sub-zoe", "sess_before", "go", nil)
		if len(slow.rows) != 1 {
			t.Fatalf("the first attempt claimed %d rows", len(slow.rows))
		}
		ahead := l.now().Add(time.Hour).UnixMilli() // a pod whose clock is an hour fast made the claim
		if _, err := s.db.Exec(`UPDATE steers SET delivered = $1 WHERE state LIKE 'carrying%'`, ahead); err != nil {
			t.Fatal(err)
		}
		slow.rows[0].delivered = ahead
		retry := carryAfter(holder, "hanzo", "sub-zoe", "sess_before", "go", nil)
		if len(retry.rows) != 1 || retry.rows[0].delivered <= ahead {
			t.Fatalf("the retry claimed %+v, want the row, later than the claim it replaced (%d)", retry.rows, ahead)
		}
		slow.release(ctx, nil) // the slow attempt is refused at last, and gives its rows back
		var state string
		if err := s.db.QueryRow(`SELECT state FROM steers WHERE request = 'r1'`).Scan(&state); err != nil || !claimed(state) {
			t.Fatalf("the row reads %q (%v) after the slow attempt gave its rows back, want it still held by the retry", state, err)
		}
	})
}

// A refused instruction says what works. A run whose task is already near the limit has
// nothing waiting for it and takes no more: stop it or let it end. One refused beside what
// is waiting is told to wait for the run to take that. Neither says "start after" to a run
// that is still running, which is a 409.
//
// MUTATION: the old message ("with the 0 waiting ... or start a run with `after`") says
// neither.
func TestARefusedInstructionSaysWhatWorks(t *testing.T) {
	eachSite(t, func(t *testing.T, s *depot, pod func(string) *coord.Coord) {
		sessionsAre(t, theRun("sess_q", agents.StatusRunning), theRun("sess_r", agents.StatusRunning))
		a, _ := podOf(t, s, pod, "cloud-0")
		fly := func(session string, prompt int) *flight {
			ctx, stop := context.WithCancelCause(context.Background())
			f := &flight{org: "acme", subject: "u-acme", session: session, cancel: stop, done: make(chan struct{}), ctx: ctx,
				in: client.CodingStartIn{Repo: "api", Prompt: strings.Repeat("t", prompt), Tool: "codex"}}
			a.fly(f)
			t.Cleanup(func() { f.forget() })
			return f
		}
		said := func(session, text, request string) client.CodingSteerIn {
			in := tell(text, request)
			in.SessionID = session
			return in
		}
		ctx := t.Context()

		// Nothing waiting, and the task leaves no room for even a short word.
		_, err := fly("sess_q", maxPromptLen-1).followed(ctx, "u-acme", said("sess_q", "thirteen byte", "r1"), nil)
		msg := ""
		if err != nil {
			msg = err.Error()
		}
		if codeOf(t, err) != http.StatusBadRequest || strings.Contains(msg, "waiting") || !strings.Contains(msg, "stop the run or wait for it to end") {
			t.Fatalf("a word beside a task at the limit answered %v, want 400 telling the person to stop the run or wait for it to end", err)
		}

		// Something waiting, for a run no pod flies: wait for the run to take it.
		if _, err := a.follow(ctx, "acme", "u-acme", said("sess_r", strings.Repeat("a", 20000), "r2"), nil); err != nil {
			t.Fatal(err)
		}
		_, err = a.follow(ctx, "acme", "u-acme", said("sess_r", strings.Repeat("b", 15000), "r3"), nil)
		msg = ""
		if err != nil {
			msg = err.Error()
		}
		if codeOf(t, err) != http.StatusBadRequest || !strings.Contains(msg, "with the 1 waiting") || !strings.Contains(msg, "wait for the run to take what is queued") {
			t.Fatalf("a word beside what is waiting answered %v, want 400 telling the person to wait for the run to take it", err)
		}
	})
}
