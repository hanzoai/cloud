package coding

// ledger_test.go — a request that carries an id happens once: across pods, across a
// restart, and across a claimer that dies. Every test runs on both engines
// (sqltest.Engines) and on both buses (eachKV); a pod is a ledger of its own with a
// coord of its own over the one store, and on SQLite, whose file belongs to one
// process, the pods share its one handle.

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/coord"
	"github.com/hanzoai/cloud/sdk/coord/coordtest"
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/namespace"
)

// depot is the store the pods of one test share.
type depot struct {
	t    *testing.T
	open sqltest.Open
	db   *sql.DB
	// trail is the deployment's one audit trail, shared by every pod of the depot.
	trail memTrail
}

// replica is a pod: a ledger over the store, with the coord pod id names. The periods
// are the test's own.
func (s *depot) replica(pod func(string) *coord.Coord, id string, p periods) *ledger {
	s.t.Helper()
	db := s.db
	if db == nil || locus.DSN() != "" {
		db = s.open(namespace.System(), requestsSubsystem)
		if s.db == nil {
			s.db = db
		}
	}
	l, err := openLedger(db)
	if err != nil {
		s.t.Fatalf("openLedger: %v", err)
	}
	l.p = p.set()
	l.trail = &s.trail
	l.attach(pod(id))
	s.t.Cleanup(func() { l.attach(nil) })
	return l
}

// ledgerOf is a ledger on a store of its own, for a test that is not about the engines.
func ledgerOf(t *testing.T) *ledger {
	t.Helper()
	db, err := locus.Open(namespace.System(), requestsSubsystem, t.TempDir())
	if err != nil {
		t.Fatalf("open the store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	l, err := openLedger(db)
	if err != nil {
		t.Fatalf("openLedger: %v", err)
	}
	return l
}

// eachSite runs body on both buses and, under each, on both engines: a database of its
// own for every pair, so one leg's requests are not the next leg's.
func eachSite(t *testing.T, body func(t *testing.T, s *depot, pod func(string) *coord.Coord)) {
	t.Helper()
	eachKV(t, func(t *testing.T, pod func(string) *coord.Coord) {
		sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
			body(t, &depot{t: t, open: open}, pod)
		})
	})
}

var aKey = request{org: "acme", subject: "alice", scope: "start", id: "req-42"}

// runAs runs a start under the ledger and counts how often the work ran.
func runAs(l *ledger, k request, runs *atomic.Int32, session string, gate <-chan struct{}) (Accepted, error) {
	return once(context.Background(), l, k, func(*claim) (Accepted, error) {
		runs.Add(1)
		if gate != nil {
			<-gate
		}
		return Accepted{SessionID: session, Branch: "agent/" + session, Repo: "acme/api"}, nil
	})
}

// Of any number of pods handed one request, one runs the work, and every one of them
// answers its outcome.
//
// MUTATION: claim in a map of the process (the table this replaced) and each pod runs
// it.
func TestARequestRunsOnceAcrossReplicas(t *testing.T) {
	eachSite(t, func(t *testing.T, s *depot, pod func(string) *coord.Coord) {
		a := s.replica(pod, "cloud-0", periods{})
		b := s.replica(pod, "cloud-1", periods{})
		var runs atomic.Int32
		gate := make(chan struct{})
		time.AfterFunc(150*time.Millisecond, func() { close(gate) })

		const callers = 6
		outs := make([]Accepted, callers)
		errs := make([]error, callers)
		var wg sync.WaitGroup
		for i := range callers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				l := a
				if i%2 == 1 {
					l = b
				}
				outs[i], errs[i] = runAs(l, aKey, &runs, "sess_42", gate)
			}()
		}
		wg.Wait()
		if got := runs.Load(); got != 1 {
			t.Fatalf("the request ran %d times across two pods, want once", got)
		}
		for i := range callers {
			if errs[i] != nil || outs[i].SessionID != "sess_42" || outs[i].Branch != "agent/sess_42" {
				t.Fatalf("caller %d got (%+v, %v), want the first's outcome", i, outs[i], errs[i])
			}
		}

		// Another person's request with the same id, and another org's, are other requests.
		for _, other := range []request{
			{org: "acme", subject: "bob", scope: "start", id: "req-42"},
			{org: "globex", subject: "alice", scope: "start", id: "req-42"},
			{org: "acme", subject: "alice", scope: "steer/sess_42", id: "req-42"},
		} {
			got, err := runAs(a, other, &runs, "sess_other", nil)
			if err != nil || got.SessionID != "sess_other" {
				t.Fatalf("%+v answered (%+v, %v), want a request of its own", other, got, err)
			}
		}
	})
}

// A process that restarts forgets everything it knew, and the retry a client makes
// after a deploy is still the same request: the store answers it.
//
// MUTATION: keep the outcome in memory and the fresh process runs it again.
func TestARetryAfterARestartAnswersTheSameRun(t *testing.T) {
	eachSite(t, func(t *testing.T, s *depot, pod func(string) *coord.Coord) {
		before := s.replica(pod, "cloud-0", periods{})
		var runs atomic.Int32
		first, err := runAs(before, aKey, &runs, "sess_42", nil)
		if err != nil || first.SessionID != "sess_42" {
			t.Fatalf("first: (%+v, %v)", first, err)
		}
		before.attach(nil) // the process is gone

		// A new process of the same pod: a new ledger, a new coord, the same store.
		after := s.replica(pod, "cloud-0", periods{})
		again, err := runAs(after, aKey, &runs, "sess_other", nil)
		if err != nil || again != first || runs.Load() != 1 {
			t.Fatalf("the retry after a restart answered (%+v, %v) after %d runs, want %+v and one run", again, err, runs.Load(), first)
		}
	})
}

// A duplicate of a request being worked on is told its outcome the moment it exists, over
// the bus, on this pod and on another. Nothing polls: the bound is an hour, and the
// duplicate returns in the time the message takes.
//
// MUTATION: settle without telling, and the duplicate waits out the hour.
func TestADuplicateIsToldTheOutcomeOnTheBus(t *testing.T) {
	eachSite(t, func(t *testing.T, s *depot, pod func(string) *coord.Coord) {
		hour := periods{Bound: time.Hour, Ceiling: 2 * time.Hour}
		a := s.replica(pod, "cloud-0", hour)
		b := s.replica(pod, "cloud-1", hour)
		var runs atomic.Int32
		gate := make(chan struct{})
		entered := make(chan struct{})
		first := make(chan Accepted, 1)
		go func() {
			out, _ := once(context.Background(), a, aKey, func(*claim) (Accepted, error) {
				runs.Add(1)
				close(entered)
				<-gate
				return Accepted{SessionID: "sess_42"}, nil
			})
			first <- out
		}()
		<-entered

		type answer struct {
			out Accepted
			err error
		}
		answers := make(chan answer, 2)
		for _, l := range []*ledger{a, b} { // a duplicate on the claimer's pod, and one on another
			go func() {
				out, err := runAs(l, aKey, &runs, "sess_dup", nil)
				answers <- answer{out, err}
			}()
		}
		// The duplicates are listening before the outcome exists.
		waitForListeners(t, a, b)
		close(gate)
		for range 2 {
			select {
			case got := <-answers:
				if got.err != nil || got.out.SessionID != "sess_42" {
					t.Fatalf("a duplicate answered (%+v, %v), want the first's outcome", got.out, got.err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("a duplicate was not told the outcome: it is waiting out the bound")
			}
		}
		if got := (<-first).SessionID; got != "sess_42" || runs.Load() != 1 {
			t.Fatalf("first answered %q after %d runs, want sess_42 after one", got, runs.Load())
		}
	})
}

// waitForListeners returns once every ledger has a duplicate listening for aKey.
func waitForListeners(t *testing.T, ls ...*ledger) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for _, l := range ls {
		for {
			l.mu.Lock()
			n := len(l.waits[aKey.topic()])
			l.mu.Unlock()
			if n > 0 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("a duplicate never began waiting")
			}
			time.Sleep(time.Millisecond)
		}
	}
}

// plant writes a claim as a process that has since died would have left it.
func plant(t *testing.T, s *depot, k request, owner string, age time.Duration) {
	t.Helper()
	now := time.Now()
	if _, err := s.db.Exec(`INSERT INTO requests (org, subject, scope, id, state, owner, session, outcome, created, claimed, expires)
VALUES ($1, $2, $3, $4, 'claimed', $5, '', '', $6, $6, $7)`,
		k.org, k.subject, k.scope, k.id, owner, now.Add(-age).UnixMilli(), now.Add(24*time.Hour).UnixMilli()); err != nil {
		t.Fatalf("plant: %v", err)
	}
}

// A claim whose claimer died is taken over by exactly one duplicate, whatever the
// number of duplicates and pods: the work runs once more, and everyone answers it.
//
// MUTATION: let every duplicate that judges the claim abandoned run the work (drop the
// retake's match on the owner) and the work runs once per duplicate.
func TestADeadClaimerIsTakenOverByOne(t *testing.T) {
	eachSite(t, func(t *testing.T, s *depot, pod func(string) *coord.Coord) {
		short := periods{Bound: 20 * time.Millisecond}
		a := s.replica(pod, "cloud-0", short)
		b := s.replica(pod, "cloud-1", short)
		// The claimer was a pod that is not there: nothing answers for it.
		plant(t, s, aKey, "cloud-9#gone", time.Minute)

		var runs atomic.Int32
		const callers = 8
		outs := make([]Accepted, callers)
		errs := make([]error, callers)
		var wg sync.WaitGroup
		for i := range callers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				l := a
				if i%2 == 1 {
					l = b
				}
				outs[i], errs[i] = runAs(l, aKey, &runs, "sess_retaken", nil)
			}()
		}
		wg.Wait()
		if got := runs.Load(); got != 1 {
			t.Fatalf("a dead claimer's request was taken over %d times, want exactly once", got)
		}
		for i := range callers {
			if errs[i] != nil || outs[i].SessionID != "sess_retaken" {
				t.Fatalf("caller %d got (%+v, %v)", i, outs[i], errs[i])
			}
		}
	})
}

// A claimer that is slow and not dead is not taken over: it is asked, and answers that
// it is on the request. The duplicate waits for its outcome.
//
// MUTATION: judge every claim past the bound abandoned and the work runs twice.
func TestASlowClaimerIsNotTakenOver(t *testing.T) {
	eachSite(t, func(t *testing.T, s *depot, pod func(string) *coord.Coord) {
		short := periods{Bound: 20 * time.Millisecond}
		a := s.replica(pod, "cloud-0", short)
		b := s.replica(pod, "cloud-1", short)
		var runs atomic.Int32
		gate := make(chan struct{})
		entered := make(chan struct{})
		go func() {
			_, _ = once(context.Background(), a, aKey, func(*claim) (Accepted, error) {
				runs.Add(1)
				close(entered)
				<-gate
				return Accepted{SessionID: "sess_slow"}, nil
			})
		}()
		<-entered
		done := make(chan Accepted, 1)
		go func() {
			out, _ := runAs(b, aKey, &runs, "sess_dup", nil)
			done <- out
		}()
		// Many bounds pass with the claimer alive and working.
		select {
		case out := <-done:
			t.Fatalf("the duplicate returned %+v while the claimer was still working", out)
		case <-time.After(300 * time.Millisecond):
		}
		close(gate)
		select {
		case out := <-done:
			if out.SessionID != "sess_slow" || runs.Load() != 1 {
				t.Fatalf("the duplicate answered %+v after %d runs, want the claimer's and one run", out, runs.Load())
			}
		case <-time.After(10 * time.Second):
			t.Fatal("the duplicate never heard the claimer's outcome")
		}
	})
}

// A claimer that was retaken learns it when it settles, and so does not do what cannot
// be undone: the fence a start spawns its run behind.
//
// MUTATION: settle without matching the owner and the retaken claimer commits too.
func TestARetakenClaimerCannotSettle(t *testing.T) {
	eachSite(t, func(t *testing.T, s *depot, pod func(string) *coord.Coord) {
		a := s.replica(pod, "cloud-0", periods{})
		b := s.replica(pod, "cloud-1", periods{})
		settled := make(chan error, 1)
		entered, retaken := make(chan struct{}), make(chan struct{})
		go func() {
			_, _ = once(context.Background(), a, aKey, func(c *claim) (Accepted, error) {
				close(entered)
				<-retaken
				err := c.Settle(context.Background(), Accepted{SessionID: "sess_late"})
				settled <- err
				return Accepted{}, err
			})
		}()
		<-entered
		e, err := b.read(context.Background(), aKey)
		if err != nil {
			t.Fatal(err)
		}
		if won, _, err := b.retake(context.Background(), aKey, e); err != nil || !won {
			t.Fatalf("retake: %v, won=%v", err, won)
		}
		if won, _, _ := b.retake(context.Background(), aKey, e); won {
			t.Fatal("a second retake of the same claim also won")
		}
		close(retaken)
		if err := <-settled; !errors.Is(err, errClaim) {
			t.Fatalf("the retaken claimer settled with %v, want errClaim", err)
		}
	})
}

// A failure is not remembered. The claim is released and the next attempt runs, and a
// duplicate that was waiting on the failed attempt runs it itself.
func TestAFailedRequestIsReleased(t *testing.T) {
	eachSite(t, func(t *testing.T, s *depot, pod func(string) *coord.Coord) {
		hour := periods{Bound: time.Hour, Ceiling: 2 * time.Hour}
		a := s.replica(pod, "cloud-0", hour)
		b := s.replica(pod, "cloud-1", hour)
		boom := errors.New("boom")

		if _, err := once(context.Background(), a, aKey, func(*claim) (Accepted, error) { return Accepted{}, boom }); !errors.Is(err, boom) {
			t.Fatalf("first: %v, want boom", err)
		}
		var runs atomic.Int32
		if out, err := runAs(a, aKey, &runs, "sess_ok", nil); err != nil || out.SessionID != "sess_ok" || runs.Load() != 1 {
			t.Fatalf("the retry after a failure answered (%+v, %v) after %d runs, want it to run", out, err, runs.Load())
		}

		// A duplicate waiting on an attempt that fails takes the request over.
		k := request{org: "acme", subject: "alice", scope: "start", id: "req-fails"}
		entered, release := make(chan struct{}), make(chan struct{})
		failed := make(chan error, 1)
		go func() {
			_, err := once(context.Background(), a, k, func(*claim) (Accepted, error) {
				close(entered)
				<-release
				return Accepted{}, boom
			})
			failed <- err
		}()
		<-entered
		took := make(chan Accepted, 1)
		go func() {
			out, _ := runAs(b, k, &runs, "sess_dup", nil)
			took <- out
		}()
		waitForListenersOn(t, b, k)
		close(release)
		if err := <-failed; !errors.Is(err, boom) {
			t.Fatalf("the failed attempt: %v", err)
		}
		select {
		case out := <-took:
			if out.SessionID != "sess_dup" {
				t.Fatalf("the duplicate answered %+v, want it to have run the request itself", out)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("the duplicate was not told its claimer failed")
		}
	})
}

func waitForListenersOn(t *testing.T, l *ledger, k request) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		l.mu.Lock()
		n := len(l.waits[k.topic()])
		l.mu.Unlock()
		if n > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("a duplicate never began waiting")
		}
		time.Sleep(time.Millisecond)
	}
}

// An outcome is kept for a day and pruned after: by the pod that leads the sweep and
// by no other, and a request whose row has expired but is not pruned yet runs again as
// an absent one does.
func TestExpiredRequestsArePrunedByTheLeader(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		s := &depot{t: t, open: open}
		pods := coordtest.Replicas(t, 2)
		a, b := s.replica(pods, "cloud-0", periods{}), s.replica(pods, "cloud-1", periods{})
		var runs atomic.Int32
		old := request{org: "acme", subject: "alice", scope: "start", id: "req-old"}
		fresh := request{org: "acme", subject: "alice", scope: "start", id: "req-fresh"}
		for _, k := range []request{old, fresh} {
			if _, err := runAs(a, k, &runs, "sess_"+k.id, nil); err != nil {
				t.Fatal(err)
			}
		}
		// The default ttl is a day.
		var expires int64
		if err := s.db.QueryRow(`SELECT expires FROM requests WHERE id = $1`, "req-fresh").Scan(&expires); err != nil {
			t.Fatal(err)
		}
		if got := time.UnixMilli(expires).Sub(time.Now()); got < 23*time.Hour+59*time.Minute || got > 24*time.Hour {
			t.Fatalf("an outcome expires in %v, want a day", got)
		}
		// A steer said long ago is pruned with it.
		if err := a.enqueue(context.Background(), "acme", "sess_x", "alice", "req-steer", "hi"); err != nil {
			t.Fatal(err)
		}
		past := time.Now().Add(-time.Minute).UnixMilli()
		for _, q := range []string{`UPDATE requests SET expires = $1 WHERE id = 'req-old'`, `UPDATE steers SET expires = $1`} {
			if _, err := s.db.Exec(q, past); err != nil {
				t.Fatal(err)
			}
		}

		// An expired row that the sweep has not reached is an absent one: it runs again.
		again, err := runAs(a, old, &runs, "sess_again", nil)
		if err != nil || again.SessionID != "sess_again" {
			t.Fatalf("an expired request answered (%+v, %v), want it to run again", again, err)
		}
		if _, err := s.db.Exec(`UPDATE requests SET expires = $1 WHERE id = 'req-old'`, past); err != nil {
			t.Fatal(err)
		}

		ctx := context.Background()
		errA, errB := a.sweep(ctx), b.sweep(ctx)
		if (errA == nil) == (errB == nil) {
			t.Fatalf("both or neither pod led the sweep: %v, %v", errA, errB)
		}
		if errA != nil && !errors.Is(errA, coord.ErrNotHeld) || errB != nil && !errors.Is(errB, coord.ErrNotHeld) {
			t.Fatalf("a pod that did not lead answered %v / %v, want ErrNotHeld", errA, errB)
		}
		var requests, steers int
		_ = s.db.QueryRow(`SELECT COUNT(*) FROM requests`).Scan(&requests)
		_ = s.db.QueryRow(`SELECT COUNT(*) FROM steers`).Scan(&steers)
		if requests != 1 || steers != 0 {
			t.Fatalf("after the sweep %d requests and %d steers remain, want the fresh request alone", requests, steers)
		}
		if out, err := runAs(a, fresh, &runs, "sess_x", nil); err != nil || out.SessionID != "sess_req-fresh" {
			t.Fatalf("the fresh request answered (%+v, %v) after the sweep", out, err)
		}
	})
}

// The ledger is text: a request id Postgres would refuse is refused on both engines,
// before it touches the store.
func TestARequestIDMustBeText(t *testing.T) {
	eachSite(t, func(t *testing.T, s *depot, pod func(string) *coord.Coord) {
		a := s.replica(pod, "cloud-0", periods{})
		var runs atomic.Int32
		for _, id := range []string{"a\x00b", "a\xffb"} {
			_, err := runAs(a, request{org: "acme", subject: "alice", scope: "start", id: id}, &runs, "sess", nil)
			if code := codeOf(t, err); code != http.StatusBadRequest || runs.Load() != 0 {
				t.Fatalf("%q: %v, runs=%d, want 400 and no run", id, err, runs.Load())
			}
		}
	})
}

// A request id is kept whole or refused: one longer than the bound is a 400, never cut to
// fit, so two ids that differ only past the cut are not one request and a cut cannot land
// inside a rune.
//
// MUTATION: cut the id at the bound (the old rule) and a/1 and a/2 are the same request.
func TestARequestIDLongerThanTheBoundIsRefused(t *testing.T) {
	l := ledgerOf(t)
	ctx := t.Context()
	var runs atomic.Int32
	ask := func(id string) error {
		_, err := requested(ctx, l, "acme", "alice", "start", id, "", func(context.Context) (Accepted, error) {
			runs.Add(1)
			return Accepted{SessionID: "sess_a"}, nil
		})
		return err
	}
	bound := strings.Repeat("a", maxRequestIDLen)
	for id, want := range map[string]int{
		bound:                                  0,
		bound + "1":                            http.StatusBadRequest,
		bound + "2":                            http.StatusBadRequest,
		strings.Repeat("é", maxRequestIDLen/2): 0, // exactly the bound, two bytes a rune
		"a" + strings.Repeat("é", maxRequestIDLen/2): http.StatusBadRequest, // a cut at the bound would split the last rune
	} {
		err := ask(id)
		if want == 0 && err != nil || want != 0 && codeOf(t, err) != want {
			t.Errorf("an id of %d bytes: %v, want status %d", len(id), err, want)
		}
	}
	if runs.Load() != 2 {
		t.Errorf("the work ran %d times, want only for the two ids that fit", runs.Load())
	}
}

// An id reused for another request is refused, not answered with the first request's
// outcome: a retry has the body it first had, and a request with another body is not a
// retry. The duplicate is refused whether the first is done or still being worked on.
//
// MUTATION: drop the comparison and the second request is answered the first's session.
func TestAReusedRequestIDWithAnotherBodyIsRefused(t *testing.T) {
	eachSite(t, func(t *testing.T, s *depot, pod func(string) *coord.Coord) {
		hour := periods{Bound: time.Hour, Ceiling: 2 * time.Hour}
		a := s.replica(pod, "cloud-0", hour)
		b := s.replica(pod, "cloud-1", hour)
		ctx := t.Context()
		var runs atomic.Int32
		ask := func(l *ledger, id, body, session string, gate <-chan struct{}) (Accepted, error) {
			return requested(ctx, l, "acme", "alice", "start", id, printOf(body), func(context.Context) (Accepted, error) {
				runs.Add(1)
				if gate != nil {
					<-gate
				}
				return Accepted{SessionID: session}, nil
			})
		}

		// Done: the same body is the stored outcome; another body is refused naming the id.
		if out, err := ask(a, "key-1", "fix the header", "sess_1", nil); err != nil || out.SessionID != "sess_1" {
			t.Fatalf("first: (%+v, %v)", out, err)
		}
		if out, err := ask(b, "key-1", "fix the header", "sess_x", nil); err != nil || out.SessionID != "sess_1" {
			t.Fatalf("a retry on another pod answered (%+v, %v), want the first's session", out, err)
		}
		out, err := ask(b, "key-1", "fix the footer", "sess_x", nil)
		if codeOf(t, err) != http.StatusUnprocessableEntity || !strings.Contains(err.Error(), "key-1") || out.SessionID != "" {
			t.Fatalf("another body under the same id answered (%+v, %v), want 422 naming the id", out, err)
		}

		// Claimed: refused at once, not after the first finishes.
		gate, entered := make(chan struct{}), make(chan struct{})
		go func() {
			_, _ = requested(ctx, a, "acme", "alice", "start", "key-2", printOf("fix the header"), func(context.Context) (Accepted, error) {
				close(entered)
				<-gate
				return Accepted{SessionID: "sess_2"}, nil
			})
		}()
		<-entered
		if _, err := ask(b, "key-2", "fix the footer", "sess_x", nil); codeOf(t, err) != http.StatusUnprocessableEntity {
			t.Fatalf("another body under an id being worked on: %v, want 422", err)
		}
		close(gate)
		if runs.Load() != 1 {
			t.Fatalf("the work ran %d times from here, want only the first", runs.Load())
		}
	})
}

// A request id that cannot be kept is refused, never dropped: one from a credential that
// names no person, or for an op that names no run, would be run again by every retry.
func TestARequestIDThatCannotBeKeptIsRefused(t *testing.T) {
	l := ledgerOf(t)
	ctx := t.Context()
	var runs atomic.Int32
	run := func(org, subject, scope, id string) error {
		_, err := requested(ctx, l, org, subject, scope, id, "", func(context.Context) (Accepted, error) {
			runs.Add(1)
			return Accepted{}, nil
		})
		return err
	}
	for name, c := range map[string][4]string{
		"no person":           {"acme", "", "merge/sess_1", "m1"},
		"no org":              {"", "alice", "merge/sess_1", "m1"},
		"no run for the op":   {"acme", "alice", "", "m1"},
		"a start with no one": {"acme", "", "start", "m1"},
	} {
		if err := run(c[0], c[1], c[2], c[3]); codeOf(t, err) != http.StatusBadRequest {
			t.Errorf("%s: %v, want 400", name, err)
		}
	}
	if runs.Load() != 0 {
		t.Fatalf("a request id that could not be kept ran its work %d times", runs.Load())
	}
	// With no id there is nothing to keep, and the request is run as asked.
	if err := run("acme", "", "", ""); err != nil || runs.Load() != 1 {
		t.Fatalf("a request with no id: %v after %d runs, want it run as asked", err, runs.Load())
	}
}

// A duplicate looks at a claimer that is working once, when the claim has stood the
// bound, and not again every bound after: it sleeps on the bus. It is told the outcome the
// moment there is one.
//
// MUTATION: ask again every bound and the claimer is asked dozens of times.
func TestADuplicateAsksAWorkingClaimerOnce(t *testing.T) {
	eachSite(t, func(t *testing.T, s *depot, pod func(string) *coord.Coord) {
		short := periods{Bound: 20 * time.Millisecond, Ceiling: time.Hour}
		a := s.replica(pod, "cloud-0", short)
		b := s.replica(pod, "cloud-1", short)
		var asked atomic.Int32
		a.coord().Serve(askAlive, func(ctx context.Context, from string, body []byte) ([]byte, bool) {
			asked.Add(1)
			return a.serveAlive(ctx, from, body)
		})
		var runs atomic.Int32
		gate, entered := make(chan struct{}), make(chan struct{})
		go func() {
			_, _ = once(context.Background(), a, aKey, func(*claim) (Accepted, error) {
				runs.Add(1)
				close(entered)
				<-gate
				return Accepted{SessionID: "sess_slow"}, nil
			})
		}()
		<-entered
		done := make(chan Accepted, 1)
		go func() {
			out, _ := runAs(b, aKey, &runs, "sess_dup", nil)
			done <- out
		}()
		time.Sleep(500 * time.Millisecond) // twenty-five bounds
		close(gate)
		select {
		case out := <-done:
			if out.SessionID != "sess_slow" || runs.Load() != 1 {
				t.Fatalf("the duplicate answered %+v after %d runs, want the claimer's and one run", out, runs.Load())
			}
		case <-time.After(10 * time.Second):
			t.Fatal("the duplicate was not told the outcome")
		}
		if got := asked.Load(); got != 1 {
			t.Fatalf("the claimer was asked %d times, want once", got)
		}
	})
}

// A claimer that answers yes and never finishes is stuck, not working: at the ceiling the
// duplicate takes the request over, and the stuck attempt cannot settle it.
func TestAStuckClaimerIsRetakenAtTheCeiling(t *testing.T) {
	eachSite(t, func(t *testing.T, s *depot, pod func(string) *coord.Coord) {
		short := periods{Bound: 20 * time.Millisecond, Ceiling: 300 * time.Millisecond}
		a := s.replica(pod, "cloud-0", short)
		b := s.replica(pod, "cloud-1", short)
		var runs atomic.Int32
		stuck, entered := make(chan struct{}), make(chan struct{})
		t.Cleanup(func() { close(stuck) })
		settled := make(chan error, 1)
		go func() {
			_, _ = once(context.Background(), a, aKey, func(c *claim) (Accepted, error) {
				close(entered)
				<-stuck
				settled <- c.Settle(context.Background(), Accepted{SessionID: "sess_late"})
				return Accepted{SessionID: "sess_late"}, nil
			})
		}()
		<-entered
		began := time.Now()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		out, err := once(ctx, b, aKey, func(*claim) (Accepted, error) {
			runs.Add(1)
			return Accepted{SessionID: "sess_retaken"}, nil
		})
		if err != nil || out.SessionID != "sess_retaken" || runs.Load() != 1 {
			t.Fatalf("the duplicate answered (%+v, %v) after %d runs, want it to have taken the request over", out, err, runs.Load())
		}
		if took := time.Since(began); took < 250*time.Millisecond {
			t.Fatalf("the request was taken over after %v, before the ceiling", took)
		}
	})
}

// Two bodies that start reads as one request are one request under an id: the aliases
// folded, the text trimmed, the branch written as it will be, the defaults filled. A retry
// spelled another way is a retry; a body that asks for something else is not.
//
// MUTATION: hash the body as it came (the old rule) and a retry spelled `task` is refused.
func TestEquivalentStartsAreOneRequest(t *testing.T) {
	base := client.CodingStartIn{Repo: "r", Prompt: "fix"}
	with := func(f func(*client.CodingStartIn)) client.CodingStartIn {
		in := base
		f(&in)
		return in
	}
	print := func(in client.CodingStartIn) string { return printOf(normalized(in)) }
	for name, pair := range map[string][2]client.CodingStartIn{
		"task for prompt":    {base, with(func(in *client.CodingStartIn) { in.Prompt, in.Task = "", "fix" })},
		"padding":            {base, with(func(in *client.CodingStartIn) { in.Prompt, in.Repo = " fix\n", " r " })},
		"instructions alias": {with(func(in *client.CodingStartIn) { in.Instructions = "x" }), with(func(in *client.CodingStartIn) { in.DeveloperInstructions = " x\n" })},
		"branch spelling":    {with(func(in *client.CodingStartIn) { in.Branch = "fix" }), with(func(in *client.CodingStartIn) { in.Branch = "agent/fix" })},
		"the default mode":   {base, with(func(in *client.CodingStartIn) { in.Mode = "build" })},
		"issue spelling":     {with(func(in *client.CodingStartIn) { in.Issue = "464" }), with(func(in *client.CodingStartIn) { in.Issue = "#464" })},
		"the id itself":      {with(func(in *client.CodingStartIn) { in.RequestID = "a" }), with(func(in *client.CodingStartIn) { in.RequestID = "b" })},
	} {
		if print(pair[0]) != print(pair[1]) {
			t.Errorf("%s: the same request fingerprints as two", name)
		}
	}
	for name, other := range map[string]client.CodingStartIn{
		"another prompt":     with(func(in *client.CodingStartIn) { in.Prompt = "fix it twice" }),
		"another repository": with(func(in *client.CodingStartIn) { in.Repo = "s" }),
		"another branch":     with(func(in *client.CodingStartIn) { in.Branch = "other" }),
		"a pull request":     with(func(in *client.CodingStartIn) { in.Pull = true }),
		"another mode":       with(func(in *client.CodingStartIn) { in.Mode = "plan" }),
		"a run after one":    with(func(in *client.CodingStartIn) { in.After = "sess_1" }),
		"another effort":     with(func(in *client.CodingStartIn) { in.Model, in.Effort = "zen4", "high" }),
	} {
		if print(base) == print(other) {
			t.Errorf("%s: a different request fingerprints as the same", name)
		}
	}
	// start reads a base given and a base defaulted from `after` differently, so the two
	// are not one request under a reused id.
	defaulted := with(func(in *client.CodingStartIn) { in.After = "sess_1" })
	given := with(func(in *client.CodingStartIn) { in.After, in.Base = "sess_1", BranchFor("sess_1") })
	if print(defaulted) == print(given) {
		t.Error("a base given and a base defaulted from `after` fingerprint as one request")
	}
}

// A duplicate whose claimer's pod is closed, not merely silent, is taken over: the duplicate
// runs, and does not wait for a ceiling it does not need.
func TestADuplicateRunsOnceTheClaimersPodIsClosed(t *testing.T) {
	eachSite(t, func(t *testing.T, s *depot, pod func(string) *coord.Coord) {
		a := s.replica(pod, "cloud-0", periods{Bound: 50 * time.Millisecond})
		gone := pod("cloud-9")
		dead := s.replica(func(string) *coord.Coord { return gone }, "cloud-9", periods{})
		k := request{org: "acme", subject: "alice", scope: "start", id: "req-gone", print: "p"}
		if mine, _, _, err := dead.take(context.Background(), k); err != nil || !mine {
			t.Fatal(mine, err)
		}
		dead.attach(nil)
		gone.Close(context.Background())
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		ran := false
		if _, err := once(ctx, a, k, func(*claim) (int, error) { ran = true; return 1, nil }); err != nil || !ran {
			t.Fatalf("a duplicate of a claim whose pod is closed: ran=%v err=%v", ran, err)
		}
	})
}

// A request settled under an older fingerprint recipe answers its retry on the request id
// alone: the recipe changing between a request and its retry (a deploy) is not the caller
// changing the body, so it is not a 422. A fingerprint of this version still refuses
// another body, and every fingerprint made now says its version.
//
// MUTATION: compare an older fingerprint with this one's and the settled request's retry
// is refused 422 across the deploy.
func TestARequestSettledUnderAnOlderFingerprintAnswersItsRetry(t *testing.T) {
	eachSite(t, func(t *testing.T, s *depot, pod func(string) *coord.Coord) {
		l := s.replica(pod, "cloud-0", periods{})
		ctx := t.Context()
		var runs atomic.Int32
		ask := func(id, print, session string) (Accepted, error) {
			return requested(ctx, l, "acme", "alice", "start", id, print, func(context.Context) (Accepted, error) {
				runs.Add(1)
				return Accepted{SessionID: session}, nil
			})
		}
		if !strings.HasPrefix(printOf("x"), "v"+strconv.Itoa(printVersion)+":") {
			t.Fatalf("a fingerprint %q does not say its version", printOf("x"))
		}

		// Settled by a pod whose recipe wrote no version.
		older := strings.Repeat("ab", 32)
		if out, err := ask("key-old", older, "sess_old"); err != nil || out.SessionID != "sess_old" {
			t.Fatalf("first: (%+v, %v)", out, err)
		}
		if out, err := ask("key-old", printOf("fix the header"), "sess_new"); err != nil || out.SessionID != "sess_old" {
			t.Fatalf("the retry across the deploy answered (%+v, %v), want the stored outcome", out, err)
		}
		if runs.Load() != 1 {
			t.Fatalf("the work ran %d times, want only the first", runs.Load())
		}

		// This version's fingerprints still tell bodies apart.
		if _, err := ask("key-new", printOf("fix the header"), "sess_1"); err != nil {
			t.Fatal(err)
		}
		if _, err := ask("key-new", printOf("fix the footer"), "sess_2"); codeOf(t, err) != http.StatusUnprocessableEntity {
			t.Fatalf("another body under an id of this version: %v, want 422", err)
		}
	})
}
