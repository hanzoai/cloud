package tasks

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
)

// TestMain hands cek a throwaway master key. The store opens through cek, which
// refuses to open a data plane unencrypted, so these tests run the REAL
// encrypted path — the same code a deployment runs, not a way around it.
func TestMain(m *testing.M) {
	_ = os.Setenv("CLOUD_KMS_MASTER_KEY_REF", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	os.Exit(m.Run())
}

// store builds the subsystem over a fresh directory, exactly as Mount does.
func store(t *testing.T) *cloud.Service[state] {
	t.Helper()
	st, err := build(cloud.Base{DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	t.Cleanup(func() { _ = Shutdown(context.Background()) })
	return &cloud.Service[state]{State: st}
}

// as returns a context carrying a validated caller in org, the way the identity
// boundary hands one to a typed handler.
func as(org string) context.Context {
	return zip.WithCaller(context.Background(), zip.Caller{Org: org, User: "u@" + org})
}

// status is the HTTP status an error carries. Asserting on it is asserting on
// the contract: 404 and 409 are what a worker sees and branches on.
func status(err error) int {
	var he *zip.HTTPError
	if errors.As(err, &he) {
		return he.Status
	}
	return 0
}

// enqueue puts one task in the queue or fails the test; the tests that are about
// something else should not spell this out.
func enqueue(t *testing.T, s *cloud.Service[state], ctx context.Context, in *SubmitIn) *Task {
	t.Helper()
	task, err := submit(s)(ctx, in)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	return task
}

// expire pushes a task's lease deadline into the past. Time passing is the one
// thing a test cannot wait for honestly, so it is simulated at the store rather
// than by sleeping through it.
func expire(t *testing.T, s *cloud.Service[state], org, id string) {
	t.Helper()
	if _, err := s.State.db.Exec(`UPDATE tasks SET lease = 1 WHERE org = ? AND id = ?`, org, id); err != nil {
		t.Fatalf("expire lease: %v", err)
	}
}

// What the queue accepts. A kind is how a worker finds its work, and a payload
// is what it acts on, so both are checked before anything is stored.
func TestSubmitRejects(t *testing.T) {
	s, ctx := store(t), as("acme")
	for _, tc := range []struct {
		name     string
		in       SubmitIn
		ok       bool
		attempts int
	}{
		{name: "a plain kind", in: SubmitIn{Kind: "build", Payload: json.RawMessage(`{"repo":"x"}`)}, ok: true, attempts: defaultAttempts},
		{name: "underscores and digits", in: SubmitIn{Kind: "send_email_2", Payload: json.RawMessage(`[]`)}, ok: true, attempts: defaultAttempts},
		{name: "a stated attempt count", in: SubmitIn{Kind: "build", Payload: json.RawMessage(`1`), MaxAttempts: 7}, ok: true, attempts: 7},
		{name: "a negative attempt count takes the default", in: SubmitIn{Kind: "build", Payload: json.RawMessage(`1`), MaxAttempts: -1}, ok: true, attempts: defaultAttempts},

		{name: "no kind", in: SubmitIn{Payload: json.RawMessage(`{}`)}},
		{name: "an upper-case kind", in: SubmitIn{Kind: "Build", Payload: json.RawMessage(`{}`)}},
		{name: "a kind with a space", in: SubmitIn{Kind: "build now", Payload: json.RawMessage(`{}`)}},
		{name: "a kind that is really SQL", in: SubmitIn{Kind: "build'; DELETE FROM tasks --", Payload: json.RawMessage(`{}`)}},
		{name: "an over-long kind", in: SubmitIn{Kind: strings.Repeat("k", 65), Payload: json.RawMessage(`{}`)}},
		{name: "no payload", in: SubmitIn{Kind: "build"}},
		{name: "a payload that is not JSON", in: SubmitIn{Kind: "build", Payload: json.RawMessage(`{`)}},
		{name: "a payload one byte over the limit", in: SubmitIn{Kind: "build", Payload: json.RawMessage(`"` + strings.Repeat("x", MaxPayload) + `"`)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := submit(s)(ctx, &tc.in)
			if !tc.ok {
				if status(err) != 400 {
					t.Fatalf("submit = %v, want 400", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("submit = %v, want it accepted", err)
			}
			if got.Status != Queued {
				t.Fatalf("a fresh task is %q, want %q", got.Status, Queued)
			}
			if got.Attempts != 0 {
				t.Fatalf("a fresh task has %d attempts, want 0", got.Attempts)
			}
			if got.MaxAttempts != tc.attempts {
				t.Fatalf("maxAttempts = %d, want %d", got.MaxAttempts, tc.attempts)
			}
		})
	}
}

// The ordinary life of a task: submitted, leased, completed.
func TestLeaseAndComplete(t *testing.T) {
	s, ctx := store(t), as("acme")
	made := enqueue(t, s, ctx, &SubmitIn{Kind: "build", Payload: json.RawMessage(`{"repo":"x"}`)})

	got, err := lease(s)(ctx, &LeaseIn{Seconds: 60})
	if err != nil {
		t.Fatalf("lease: %v", err)
	}
	if got == nil {
		t.Fatal("lease found nothing with a task queued")
	}
	if got.ID != made.ID {
		t.Fatalf("leased %s, want %s", got.ID, made.ID)
	}
	if got.Status != Leased || got.Attempts != 1 {
		t.Fatalf("leased task is %q with %d attempts, want %q with 1", got.Status, got.Attempts, Leased)
	}
	if got.Lease == nil {
		t.Fatal("a leased task carries no deadline; a worker cannot tell how long it has")
	}
	if string(got.Payload) != `{"repo":"x"}` {
		t.Fatalf("payload = %s, want the bytes that went in", got.Payload)
	}

	// While it is held, the queue has nothing else to give.
	if next, err := lease(s)(ctx, &LeaseIn{}); err != nil || next != nil {
		t.Fatalf("lease of a held queue = %v, %v; want nothing", next, err)
	}

	done, err := complete(s)(ctx, &CompleteIn{ID: made.ID, Result: json.RawMessage(`{"ok":true}`)})
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if done.Status != Done || string(done.Result) != `{"ok":true}` {
		t.Fatalf("completed task = %q with result %s, want %q with the result", done.Status, done.Result, Done)
	}
	if done.Lease != nil {
		t.Fatal("a finished task still carries a lease deadline")
	}

	// A finished task stays finished, and is never handed out again.
	if again, err := lease(s)(ctx, &LeaseIn{}); err != nil || again != nil {
		t.Fatalf("lease after complete = %v, %v; want nothing", again, err)
	}
	if _, err := complete(s)(ctx, &CompleteIn{ID: made.ID, Result: json.RawMessage(`{}`)}); status(err) != 409 {
		t.Fatalf("completing a done task = %v, want 409", err)
	}
}

// An empty queue is the ordinary state of a polling worker, so it is an empty
// answer and not an error.
func TestLeaseEmptyQueue(t *testing.T) {
	s, ctx := store(t), as("acme")
	got, err := lease(s)(ctx, &LeaseIn{})
	if err != nil {
		t.Fatalf("lease on an empty queue = %v, want no error", err)
	}
	if got != nil {
		t.Fatalf("lease on an empty queue handed out %+v", got)
	}
}

// A kind is how one queue carries unrelated work: a worker asks for what it can
// do and is never handed anything else.
func TestLeaseByKind(t *testing.T) {
	s, ctx := store(t), as("acme")
	build := enqueue(t, s, ctx, &SubmitIn{Kind: "build", Payload: json.RawMessage(`1`)})
	email := enqueue(t, s, ctx, &SubmitIn{Kind: "email", Payload: json.RawMessage(`2`)})

	if got, err := lease(s)(ctx, &LeaseIn{Kind: "nothing_of_the_sort"}); err != nil || got != nil {
		t.Fatalf("lease of an absent kind = %v, %v; want nothing", got, err)
	}
	got, err := lease(s)(ctx, &LeaseIn{Kind: "email"})
	if err != nil || got == nil || got.ID != email.ID {
		t.Fatalf("lease of kind email = %v, %v; want %s", got, err, email.ID)
	}
	// The kindless lease takes whatever is left.
	got, err = lease(s)(ctx, &LeaseIn{})
	if err != nil || got == nil || got.ID != build.ID {
		t.Fatalf("lease with no kind = %v, %v; want %s", got, err, build.ID)
	}
	if _, err := lease(s)(ctx, &LeaseIn{Kind: "Not A Kind"}); status(err) != 400 {
		t.Fatalf("lease of a malformed kind = %v, want 400", err)
	}
}

// A worker that dies acknowledges nothing. Its lease runs out and the work goes
// back in the queue — that is the whole reason a lease has a deadline.
func TestExpiredLeaseRequeues(t *testing.T) {
	s, ctx := store(t), as("acme")
	made := enqueue(t, s, ctx, &SubmitIn{Kind: "build", Payload: json.RawMessage(`1`)})

	first, err := lease(s)(ctx, &LeaseIn{Seconds: 60})
	if err != nil || first == nil {
		t.Fatalf("lease: %v, %v", first, err)
	}
	expire(t, s, "acme", made.ID)

	// A read reports what a lease would do, not what the column happens to say.
	got, err := get(s)(ctx, &GetIn{ID: made.ID})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status != Queued {
		t.Fatalf("a task with an expired lease reads as %q, want %q", got.Status, Queued)
	}
	if got.Lease != nil {
		t.Fatal("a task with an expired lease still advertises a deadline")
	}
	out, err := list(s)(ctx, &ListIn{Status: Queued})
	if err != nil || len(out.Tasks) != 1 {
		t.Fatalf("listing queued tasks = %v, %v; want the requeued one", out, err)
	}

	// And the next worker really does get it.
	second, err := lease(s)(ctx, &LeaseIn{Seconds: 60})
	if err != nil || second == nil || second.ID != made.ID {
		t.Fatalf("lease after expiry = %v, %v; want %s", second, err, made.ID)
	}
	if second.Attempts != 2 {
		t.Fatalf("attempts = %d after a second lease, want 2", second.Attempts)
	}

	// The worker whose lease ran out no longer speaks for the task.
	if _, err := complete(s)(ctx, &CompleteIn{ID: made.ID, Result: json.RawMessage(`{}`)}); err != nil {
		t.Fatalf("the current holder must still be able to complete: %v", err)
	}
}

// A failure spends an attempt. While attempts remain the task goes back in the
// queue; when they are gone it stops.
func TestFailRetriesThenGivesUp(t *testing.T) {
	s, ctx := store(t), as("acme")
	made := enqueue(t, s, ctx, &SubmitIn{Kind: "build", Payload: json.RawMessage(`1`), MaxAttempts: 2})

	for attempt, want := range map[int]string{1: Queued, 2: Failed} {
		_ = attempt
		_ = want
	}
	for _, want := range []string{Queued, Failed} {
		got, err := lease(s)(ctx, &LeaseIn{Seconds: 60})
		if err != nil || got == nil {
			t.Fatalf("lease = %v, %v; want the task", got, err)
		}
		failed, err := fail(s)(ctx, &FailIn{ID: made.ID, Error: "the build broke"})
		if err != nil {
			t.Fatalf("fail: %v", err)
		}
		if failed.Status != want {
			t.Fatalf("after attempt %d the task is %q, want %q", failed.Attempts, failed.Status, want)
		}
		if failed.Error != "the build broke" {
			t.Fatalf("error = %q, want the reason the worker gave", failed.Error)
		}
	}

	// Attempts are spent: nothing hands it out again.
	if got, err := lease(s)(ctx, &LeaseIn{}); err != nil || got != nil {
		t.Fatalf("lease after the last attempt = %v, %v; want nothing", got, err)
	}
	if _, err := fail(s)(ctx, &FailIn{ID: made.ID, Error: "again"}); status(err) != 409 {
		t.Fatalf("failing a finished task = %v, want 409", err)
	}
	if _, err := fail(s)(ctx, &FailIn{ID: made.ID}); status(err) != 400 {
		t.Fatalf("failing with no reason = %v, want 400", err)
	}
}

// Cancelling stops work that has not finished. A task that already ended keeps
// the ending it had.
func TestCancel(t *testing.T) {
	s, ctx := store(t), as("acme")

	queued := enqueue(t, s, ctx, &SubmitIn{Kind: "build", Payload: json.RawMessage(`1`)})
	got, err := cancel(s)(ctx, &CancelIn{ID: queued.ID})
	if err != nil {
		t.Fatalf("cancel a queued task: %v", err)
	}
	if got.Status != Cancelled {
		t.Fatalf("cancelled task is %q, want %q", got.Status, Cancelled)
	}
	if next, err := lease(s)(ctx, &LeaseIn{}); err != nil || next != nil {
		t.Fatalf("a cancelled task was handed out: %v, %v", next, err)
	}

	held := enqueue(t, s, ctx, &SubmitIn{Kind: "build", Payload: json.RawMessage(`2`)})
	if _, err := lease(s)(ctx, &LeaseIn{Seconds: 60}); err != nil {
		t.Fatalf("lease: %v", err)
	}
	if got, err := cancel(s)(ctx, &CancelIn{ID: held.ID}); err != nil || got.Status != Cancelled {
		t.Fatalf("cancel a leased task = %v, %v; want it cancelled", got, err)
	}
	if _, err := complete(s)(ctx, &CompleteIn{ID: held.ID, Result: json.RawMessage(`{}`)}); status(err) != 409 {
		t.Fatalf("completing a cancelled task = %v, want 409", err)
	}

	for _, tc := range []struct {
		name string
		id   string
		want int
	}{
		{"a task that already ended", queued.ID, 409},
		{"a task that never existed", "no such id", 404},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := cancel(s)(ctx, &CancelIn{ID: tc.id}); status(err) != tc.want {
				t.Fatalf("cancel = %v, want %d", err, tc.want)
			}
		})
	}
}

// Acknowledgements name a task and a state. Getting either wrong is a different
// answer, because a caller does something different about each.
func TestAcknowledgeRefusals(t *testing.T) {
	s, ctx := store(t), as("acme")
	made := enqueue(t, s, ctx, &SubmitIn{Kind: "build", Payload: json.RawMessage(`1`)})

	for _, tc := range []struct {
		name string
		call func(id string) error
	}{
		{"complete", func(id string) error {
			_, err := complete(s)(ctx, &CompleteIn{ID: id, Result: json.RawMessage(`{}`)})
			return err
		}},
		{"fail", func(id string) error {
			_, err := fail(s)(ctx, &FailIn{ID: id, Error: "nope"})
			return err
		}},
	} {
		t.Run(tc.name+" a queued task", func(t *testing.T) {
			if err := tc.call(made.ID); status(err) != 409 {
				t.Fatalf("%s without holding a lease = %v, want 409", tc.name, err)
			}
		})
		t.Run(tc.name+" a task that is not there", func(t *testing.T) {
			if err := tc.call("no such id"); status(err) != 404 {
				t.Fatalf("%s of an unknown task = %v, want 404", tc.name, err)
			}
		})
	}
	if _, err := get(s)(ctx, &GetIn{ID: "no such id"}); status(err) != 404 {
		t.Fatalf("get of an unknown task = %v, want 404", err)
	}
}

// Listing: newest first, filtered by the status a read would report, bounded by
// the store's own page size.
func TestList(t *testing.T) {
	s, ctx := store(t), as("acme")
	for range 3 {
		enqueue(t, s, ctx, &SubmitIn{Kind: "build", Payload: json.RawMessage(`1`)})
	}
	held, err := lease(s)(ctx, &LeaseIn{Seconds: 60})
	if err != nil || held == nil {
		t.Fatalf("lease: %v, %v", held, err)
	}

	for _, tc := range []struct {
		name   string
		in     ListIn
		want   int
		status int
	}{
		{name: "everything", in: ListIn{}, want: 3},
		{name: "the queued ones", in: ListIn{Status: Queued}, want: 2},
		{name: "the leased one", in: ListIn{Status: Leased}, want: 1},
		{name: "none are done", in: ListIn{Status: Done}, want: 0},
		{name: "a page", in: ListIn{Limit: 2}, want: 2},
		{name: "over the ceiling clamps", in: ListIn{Limit: maxLimit + 1}, want: 3},
		{name: "an invented status", in: ListIn{Status: "in_progress"}, status: 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := list(s)(ctx, &tc.in)
			if tc.status != 0 {
				if status(err) != tc.status {
					t.Fatalf("list = %v, want %d", err, tc.status)
				}
				return
			}
			if err != nil {
				t.Fatalf("list: %v", err)
			}
			if len(out.Tasks) != tc.want {
				t.Fatalf("list returned %d tasks, want %d", len(out.Tasks), tc.want)
			}
			for i := 1; i < len(out.Tasks); i++ {
				if out.Tasks[i].Created.After(out.Tasks[i-1].Created) {
					t.Fatalf("task %d is newer than the one before it; listings are newest first", i)
				}
			}
		})
	}
}

// The tenant boundary. Two orgs share one file, so every statement filters on
// the org — and a queue makes that sharper than a store does, because leasing
// across the boundary would mean one tenant's worker RUNNING another's work.
func TestTenantIsolation(t *testing.T) {
	s := store(t)
	acme, other, local := as("acme"), as("other"), context.Background()

	mine := enqueue(t, s, acme, &SubmitIn{Kind: "build", Payload: json.RawMessage(`{"secret":true}`)})
	theirs := enqueue(t, s, other, &SubmitIn{Kind: "build", Payload: json.RawMessage(`{"secret":false}`)})

	// Each org's lease finds its own task and only its own, however hard the
	// other one polls.
	for _, tc := range []struct {
		name string
		ctx  context.Context
		want string
	}{
		{"acme", acme, mine.ID},
		{"other", other, theirs.ID},
	} {
		t.Run("lease as "+tc.name, func(t *testing.T) {
			got, err := lease(s)(tc.ctx, &LeaseIn{Seconds: 60})
			if err != nil || got == nil {
				t.Fatalf("lease = %v, %v; want a task", got, err)
			}
			if got.ID != tc.want {
				t.Fatalf("leased %s, want %s: a worker was handed another tenant's work", got.ID, tc.want)
			}
			if next, err := lease(s)(tc.ctx, &LeaseIn{}); err != nil || next != nil {
				t.Fatalf("a second lease found %v; the other org's task must be invisible", next)
			}
		})
	}
	// The local namespace has its own queue, which is to say none of theirs.
	if got, err := lease(s)(local, &LeaseIn{}); err != nil || got != nil {
		t.Fatalf("the local namespace leased %v; it must see no org's work", got)
	}

	for _, tc := range []struct {
		name string
		ctx  context.Context
		id   string
	}{
		{"another org", other, mine.ID},
		{"the local namespace", local, mine.ID},
		{"acme reaching for other", acme, theirs.ID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := get(s)(tc.ctx, &GetIn{ID: tc.id}); status(err) != 404 {
				t.Fatalf("get = %v, want 404; a task outside the caller's org does not exist to it", err)
			}
			if _, err := complete(s)(tc.ctx, &CompleteIn{ID: tc.id, Result: json.RawMessage(`{}`)}); status(err) != 404 {
				t.Fatalf("complete = %v, want 404", err)
			}
			if _, err := fail(s)(tc.ctx, &FailIn{ID: tc.id, Error: "not mine"}); status(err) != 404 {
				t.Fatalf("fail = %v, want 404", err)
			}
			if _, err := cancel(s)(tc.ctx, &CancelIn{ID: tc.id}); status(err) != 404 {
				t.Fatalf("cancel = %v, want 404", err)
			}
		})
	}

	for _, tc := range []struct {
		name string
		ctx  context.Context
		want string
	}{
		{"acme", acme, mine.ID},
		{"other", other, theirs.ID},
	} {
		t.Run("list as "+tc.name, func(t *testing.T) {
			out, err := list(s)(tc.ctx, &ListIn{})
			if err != nil {
				t.Fatalf("list: %v", err)
			}
			if len(out.Tasks) != 1 || out.Tasks[0].ID != tc.want {
				t.Fatalf("list = %+v, want exactly %s", out.Tasks, tc.want)
			}
		})
	}

	// A caller with an org but NO validated user is the forge the boundary is
	// there for: it lands in the local namespace, never in the org it named.
	forged := zip.WithCaller(context.Background(), zip.Caller{Org: "acme"})
	if got, err := lease(s)(forged, &LeaseIn{}); err != nil || got != nil {
		t.Fatalf("an unvalidated caller naming acme leased %v; want nothing", got)
	}
}

// THE test. The claim is the reason this package exists: workers racing for the
// same task must resolve to one winner, because the alternative is the same job
// running twice. Two shapes of the same property — many workers on one task, and
// many workers on many tasks, where a double hand-out shows up as a duplicate.
func TestLeaseIsExclusive(t *testing.T) {
	const workers = 24

	t.Run("one task, many workers", func(t *testing.T) {
		s, ctx := store(t), as("acme")
		made := enqueue(t, s, ctx, &SubmitIn{Kind: "build", Payload: json.RawMessage(`1`)})

		got, errs := race(workers, func() (*Task, error) {
			return lease(s)(ctx, &LeaseIn{Seconds: 60})
		})

		winners := 0
		for i, err := range errs {
			if err != nil {
				t.Fatalf("worker %d: %v", i, err)
			}
			if got[i] == nil {
				continue
			}
			winners++
			if got[i].ID != made.ID {
				t.Fatalf("worker %d leased %s, want the only task %s", i, got[i].ID, made.ID)
			}
			if got[i].Attempts != 1 {
				t.Fatalf("worker %d holds a task on attempt %d; a single claim counts once", i, got[i].Attempts)
			}
		}
		if winners != 1 {
			t.Fatalf("%d of %d workers leased the same task; exactly 1 may", winners, workers)
		}
	})

	t.Run("many tasks, many workers", func(t *testing.T) {
		s, ctx := store(t), as("acme")
		const tasks = 8
		for range tasks {
			enqueue(t, s, ctx, &SubmitIn{Kind: "build", Payload: json.RawMessage(`1`)})
		}

		got, errs := race(workers, func() (*Task, error) {
			return lease(s)(ctx, &LeaseIn{Seconds: 60})
		})

		held := map[string]int{}
		for i, err := range errs {
			if err != nil {
				t.Fatalf("worker %d: %v", i, err)
			}
			if got[i] != nil {
				held[got[i].ID]++
			}
		}
		for id, n := range held {
			if n != 1 {
				t.Fatalf("task %s was handed to %d workers at once", id, n)
			}
		}
		if len(held) != tasks {
			t.Fatalf("%d of %d tasks were claimed; %d workers polling an %d-deep queue must drain it",
				len(held), tasks, workers, tasks)
		}
	})
}

// race runs fn in n goroutines released together and collects what each got.
// Releasing them from one channel is what makes them contend rather than queue
// up politely behind each other's startup.
func race(n int, fn func() (*Task, error)) ([]*Task, []error) {
	got, errs := make([]*Task, n), make([]error, n)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			got[i], errs[i] = fn()
		}()
	}
	close(start)
	wg.Wait()
	return got, errs
}

// A queue that cannot reach its data must not accept work it will drop.
func TestBuildFailsClosed(t *testing.T) {
	if _, err := build(cloud.Base{}); err == nil {
		t.Fatal("build with no DataDir succeeded; it must fail the mount")
	}
}

// Shutdown runs on a path where it may be called twice, or never have opened
// anything. Neither is an error.
func TestShutdownIsIdempotent(t *testing.T) {
	store(t)
	if err := Shutdown(context.Background()); err != nil {
		t.Fatalf("first shutdown: %v", err)
	}
	if err := Shutdown(context.Background()); err != nil {
		t.Fatalf("second shutdown: %v", err)
	}
}

// The routes are the contract the lead wires and every projection reads. Pinning
// them here means a typo in a path or an operation id fails in this package
// rather than in the composed document.
func TestRoutes(t *testing.T) {
	app := zip.New(zip.Config{AppName: "tasks-test"})
	routes(app, store(t))
	d := app.Declaration()

	want := map[string]string{
		"POST /v1/tasks":              "tasks_submit",
		"GET /v1/tasks":               "tasks_list",
		"POST /v1/tasks/lease":        "tasks_lease",
		"GET /v1/tasks/:id":           "tasks_get",
		"POST /v1/tasks/:id/complete": "tasks_complete",
		"POST /v1/tasks/:id/fail":     "tasks_fail",
		"DELETE /v1/tasks/:id":        "tasks_cancel",
	}
	got := map[string]bool{}
	for _, r := range d.Routes {
		got[r.Method+" "+r.Pattern] = true
	}
	for route := range want {
		if !got[route] {
			t.Errorf("route %q is not registered; declared routes are %+v", route, d.Routes)
		}
	}
	ops := map[string]bool{}
	for _, op := range d.Ops {
		ops[op] = true
	}
	for _, id := range want {
		if !ops[id] {
			t.Errorf("operation %q is not registered; declared ops are %v", id, d.Ops)
		}
	}
	if len(d.Ops) != len(want) {
		t.Errorf("declared %d ops, want %d: every route here must be a TYPED op", len(d.Ops), len(want))
	}
}
