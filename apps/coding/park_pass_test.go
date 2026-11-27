package coding

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanzoai/cloud/apps/agents"
	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/funds"
	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/hanzoai/cloud/sdk/coord"
	"github.com/zap-proto/zip"
)

// RED729C-1 (LOW, liveness). The claimed fix recovers a panic in `attempts` (survive), but the goroutine askPass
// starts calls reconsider, and reconsider reads the parks and calls wanted -> planOf -> onPlan (a peer call to
// commerce) BEFORE attempts. A panic there is in a goroutine nobody recovers: the process dies, the park rows
// survive, and the next funds event for a plan does it again. fire() has the same shape (due/soonest are outside
// survive, but they are SQL only). The test calls reconsider the way askPass's goroutine does and fails if it panics.
//
// MUTATION: take the recover out of reconsider and the panic is the pass's, and the process's.
func TestRed729cAPanicInThePlanLookupIsNotTheProcessesToDieOf(t *testing.T) {
	eachSite(t, func(t *testing.T, s *depot, pod func(string) *coord.Coord) {
		app := newSessionsApp(t)
		a, la, _ := podWith(t, s, pod, "cloud-0")
		parkedAs(t, la, app, "acme", "u1", "sess_plan", time.Now().Unix()+3600)
		was := onPlan
		onPlan = func(context.Context, string, string) (string, error) { panic("commerce peer decoded a nil") }
		t.Cleanup(func() { onPlan = was })
		var panicked any
		func() {
			defer func() { panicked = recover() }()
			a.reconsider("", "go") // what the goroutine askPass starts runs, with nothing around it
		}()
		if panicked != nil {
			t.Fatalf("RED: reconsider panicked outside attempts' recover (%v); askPass's goroutine has no recover, so this is a process crash", panicked)
		}
	})
}

// RED729C-2 (INFO, expected GREEN). A panic AFTER the launch (the trail panics on the SUCCESS record, which is written
// after launched has unparked the session) must not relaunch: the claim is released by run() and the next try finds the
// session not parked. launched here unparks like the real startSession does.
func TestRed729cAPanicAfterTheLaunchDoesNotLaunchTwice(t *testing.T) {
	eachSite(t, func(t *testing.T, s *depot, pod func(string) *coord.Coord) {
		app := newSessionsApp(t)
		a, la, _ := podWith(t, s, pod, "cloud-0")
		var mu sync.Mutex
		var launches int
		was := launched
		launched = func(ctx context.Context, org, _ string, _ client.CodingStartIn, _ func(string, ...any), _ string, existing *agents.Session, _ *park) (Accepted, error) {
			ok, err := unparkSession(ctx, org, existing.ID)
			if err != nil || !ok {
				return Accepted{}, errStale
			}
			mu.Lock()
			launches++
			mu.Unlock()
			return Accepted{SessionID: existing.ID}, nil
		}
		t.Cleanup(func() { launched = was })
		la.trail = &laterPanics{}
		row := parkedAs(t, la, app, "acme", "u1", "sess_post", time.Now().Unix()-5)
		a.fire()
		_ = a.proceed(t.Context(), row, byWake)
		a.reconsider("acme", "")
		mu.Lock()
		defer mu.Unlock()
		if launches != 1 {
			t.Fatalf("launched %d times after a panic past the launch, want 1", launches)
		}
	})
}

// laterPanics takes the intent and panics on every record after it.
type laterPanics struct {
	mu sync.Mutex
	n  int
}

func (l *laterPanics) Append(_ context.Context, rec audit.Record) (audit.Record, error) {
	l.mu.Lock()
	l.n++
	n := l.n
	l.mu.Unlock()
	if n > 1 {
		panic("the trail's store is gone")
	}
	return rec, nil
}

// RED729C-3 (LOW). A deployment with no audit trail (CLOUD_AUDIT_DISABLED) parks a run -- parkRun does not look at the
// trail -- and then refuses every try (errNoTrail, and a person's Continue answers 503), so the run says "goes on when
// credits are added" and never does. Before this PR the same run failed with the model API's words. A park that cannot
// be gone on with must not be promised: parkRun should refuse when unset(l.trail), and the run fail as it did.
//
// MUTATION: leave the check out of parkRun and a process with no trail promises a run it cannot keep.
func TestRed729cAProcessWithNoTrailDoesNotParkARunItCannotGoOnWith(t *testing.T) {
	eachSite(t, func(t *testing.T, s *depot, pod func(string) *coord.Coord) {
		app := newSessionsApp(t)
		a, la, _ := podWith(t, s, pod, "cloud-0")
		la.trail = nil
		app.run("acme", "u1", "sess_notrail")
		err := a.parkRun(t.Context(), holding{org: "acme", session: "sess_notrail", subject: "u1",
			in: client.CodingStartIn{Repo: "api", Prompt: "add a health check"}}, Parked{Reason: agents.ParkLimit})
		if err == nil {
			t.Fatalf("RED: parked a run in a process that can never try it again (parks=%d)", app.parked())
		}
	})
}

// RED729C-4 (LOW, wrong word, no clock). refusedReason reads EVERY 429 as the model API's usage limit. The sandbox app
// answers 429 for the org's concurrent-sandbox QUOTA ("has 100 sandboxes running, its quota of 100; end one ...",
// apps/sandbox/quota.go within). A run that goes on and meets that is parked "usage limit ... goes on when credits are
// added" with no time (clearsAt asks the allowance app and the pool, which hold nothing), nothing counts a failure
// and no clock wakes it; only a funds event or a Continue does, and credits cannot clear a running-sandbox quota.
// It should be the kind of failure the code already handles as 'nobody's refusal': Retry, with a time.
//
// MUTATION: read every lease 429 as a usage limit (the old behaviour) and a sandbox quota parks with
// no time and no failure counted.
func TestRed729cASandboxQuotaIsNotAUsageLimit(t *testing.T) {
	authorities(t, resetsAt(0), nil)
	sess := &fakeSessions{id: "sess_abc123def456"}
	quota := zip.Errorf(http.StatusTooManyRequests, "acme has 100 sandboxes running, its quota of 100; end one, or ask support for a higher quota")
	run := &fakeRunner{err: admission{quota}}
	d, _ := dispatcherFor(sess, &fakePR{}, run, true)
	var held []Parked
	d.Park = func(_ context.Context, p Parked) error { held = append(held, p); return nil }
	req := baseReq()
	req.SessionID, req.Parked, req.Kept = "sess_abc123def456", true, Kept{Commit: newSHA}
	req.Reason = agents.ParkLimit
	d.Run(t.Context(), req)
	if len(held) != 1 {
		t.Fatalf("parked %d times", len(held))
	}
	if p := held[0]; p.At == 0 && p.Tries == 0 {
		t.Fatalf("RED: a sandbox quota 429 parked the run as %+v: a usage limit with no time and no failure counted; no clock will ever try it again", p)
	}
}

// RED729C-5 (LOW, bound not strict). The sweep drops a chain with no park older than a day. A relaunched run has no
// park row while it runs, so a run in flight across the chain's day boundary loses its chain to the sweep, and when it is
// refused again its park begins a NEW chain (created = now): another full day of going on by itself. The bound is
// MaxLife of parking measured from the first park only if the sweep happened not to run during the flight.
//
// MUTATION: sweep a chain with no park after one day (not two) and the re-park begins another.
func TestRed729cAChainIsNotForgottenByTheSweepWhileItsRunIsFlying(t *testing.T) {
	eachSite(t, func(t *testing.T, s *depot, pod func(string) *coord.Coord) {
		app := newSessionsApp(t)
		_, la, _ := podWith(t, s, pod, "cloud-0")
		row := parkedAs(t, la, app, "acme", "u1", "sess_flying", time.Now().Unix()+3600)
		// 23h50m into the chain the clock relaunched it: the park is dropped, the run flies.
		start := time.Now().Add(-agents.MaxLife() + 10*time.Minute).UnixMilli()
		if _, err := la.db.ExecContext(t.Context(), `UPDATE chains SET since = $1`, start); err != nil {
			t.Fatal(err)
		}
		if _, err := la.drop(t.Context(), row.org, row.session, row.attempt); err != nil {
			t.Fatal(err)
		}
		// It flies past the day, and the sweep runs while it does.
		if _, err := la.db.ExecContext(t.Context(), `UPDATE chains SET since = $1`, time.Now().Add(-agents.MaxLife()-time.Minute).UnixMilli()); err != nil {
			t.Fatal(err)
		}
		if _, err := la.prune(t.Context()); err != nil {
			t.Fatal(err)
		}
		// Refused again: parked.
		if err := la.park(t.Context(), row); err != nil {
			t.Fatal(err)
		}
		again, _, _ := la.parked(t.Context(), "acme", "sess_flying")
		if !again.stale(time.Now()) {
			t.Fatalf("RED: the chain began %v ago and the re-park is not stale (created %d): the sweep forgot the chain while its run flew", agents.MaxLife()+time.Minute, again.created)
		}
	})
}

// A pass that panics before its tries (the plan lookup) backs the parks it was choosing from off, as a
// try that panicked is: one failure counted, a later time, the wake armed for it. It leaves no pass
// standing, so the next event for the key starts one, and a burst of events during the panic is not
// stuck behind a pending flag that nobody will clear.
//
// MUTATION: leave the back-off out of the recover and the parks wait on an event that may not come;
// leave the key standing and no event for it ever runs a pass again.
func TestAPanicInAPassBacksTheParksOffAndFreesTheKey(t *testing.T) {
	eachSite(t, func(t *testing.T, s *depot, pod func(string) *coord.Coord) {
		app := newSessionsApp(t)
		k := &launcher{}
		k.install(t)
		a, la, _ := podWith(t, s, pod, "cloud-0")
		first := parkedAs(t, la, app, "acme", "u1", "sess_boom", time.Now().Unix()+7200)
		var calls int
		var mu sync.Mutex
		onPlan = func(context.Context, string, string) (string, error) {
			mu.Lock()
			calls++
			mu.Unlock()
			panic("commerce peer decoded a nil")
		}
		for range 50 {
			a.funded(funds.Event{Plan: "go", Kind: funds.Cap})
		}
		waitFor(t, "the passes to finish", func() bool { a.Lock(); defer a.Unlock(); return len(a.passes) == 0 })
		row, _, _ := la.parked(t.Context(), "acme", "sess_boom")
		if row.tries != first.tries+1 || row.at > time.Now().Add(retryAfter(1)+time.Minute).Unix() || row.at < time.Now().Unix() {
			t.Fatalf("the park reads %+v, want one failure counted and the next try a delay away", row)
		}
		mu.Lock()
		before := calls
		mu.Unlock()
		if _, err := la.db.ExecContext(t.Context(), `UPDATE parks SET tries = 0`); err != nil { // no longer backed off
			t.Fatal(err)
		}
		a.funded(funds.Event{Plan: "go", Kind: funds.Cap}) // the key is free
		waitFor(t, "another pass to run", func() bool { mu.Lock(); defer mu.Unlock(); return calls > before })
		waitFor(t, "that pass to be gone", func() bool { a.Lock(); defer a.Unlock(); return len(a.passes) == 0 })
		if len(k.started()) != 0 {
			t.Fatalf("started %v", k.started())
		}
	})
}

// The wake's own goroutine recovers too: a panic in a pass of the timer arms it again after a delay
// and does not take the process down. Here the ledger has no store, which is a nil dereference in the
// timer's goroutine outside every try.
//
// MUTATION: take the recover out of fire and the timer's goroutine panics the process.
func TestAPanicInTheWakeIsRecoveredAndTheWakeArmedAgain(t *testing.T) {
	eachSite(t, func(t *testing.T, s *depot, pod func(string) *coord.Coord) {
		a, la, _ := podWith(t, s, pod, "cloud-0")
		la.db = nil
		var panicked any
		func() {
			defer func() { panicked = recover() }()
			a.fire()
		}()
		if panicked != nil {
			t.Fatalf("the wake panicked out of its goroutine: %v", panicked)
		}
		if armed(a) == 0 {
			t.Fatal("the wake was not armed again after a panic")
		}
	})
}

// A pass that cannot even back its parks off (the delay itself panics) still frees its key: whatever
// escapes a pass leaves no pass standing and no pending flag, so the next event for the key runs one.
//
// MUTATION: take the guard out of the pass goroutine and the key stands for ever, every later event
// for it marking a pass pending that nobody will run.
func TestAPassThatCannotEvenBackOffStillFreesItsKey(t *testing.T) {
	eachSite(t, func(t *testing.T, s *depot, pod func(string) *coord.Coord) {
		app := newSessionsApp(t)
		a, la, _ := podWith(t, s, pod, "cloud-0")
		parkedAs(t, la, app, "acme", "u1", "sess_stuck", time.Now().Unix()+7200)
		var calls atomic.Int32
		onPlan = func(context.Context, string, string) (string, error) {
			calls.Add(1)
			panic("commerce peer decoded a nil")
		}
		was := retryAfter
		retryAfter = func(int64) time.Duration { panic("the delay cannot be worked out") }
		t.Cleanup(func() { retryAfter = was })
		a.funded(funds.Event{Plan: "go", Kind: funds.Cap})
		a.funded(funds.Event{Plan: "go", Kind: funds.Cap})
		waitFor(t, "the pass to be gone", func() bool { a.Lock(); defer a.Unlock(); return len(a.passes) == 0 })
		before := calls.Load()
		a.funded(funds.Event{Plan: "go", Kind: funds.Cap})
		waitFor(t, "the next event to run a pass", func() bool { return calls.Load() > before })
		waitFor(t, "that pass to be gone", func() bool { a.Lock(); defer a.Unlock(); return len(a.passes) == 0 })
	})
}

// A lease that is refused with a 429 is a usage limit when the authority that makes them says one
// stands, with its time; the same 429 when none does is a failure to start, retried after a delay.
//
// MUTATION: park every 429 as a limit and the sandbox quota above reads as a window that will reopen.
func TestALeaseThatIsRefusedWith429IsALimitOnlyWhereAnAuthorityHoldsIt(t *testing.T) {
	quota := admission{zip.Errorf(http.StatusTooManyRequests, "the free lane is used")}
	resets := time.Now().Add(2 * time.Hour).Unix()
	for name, c := range map[string]struct {
		pool  *client.Pool
		limit bool
	}{
		"the pool is exhausted":   {&client.Pool{State: "exhausted", Resets: resets}, true},
		"nothing holds the limit": {nil, false},
	} {
		t.Run(name, func(t *testing.T) {
			authorities(t, resetsAt(0), c.pool)
			sess := &fakeSessions{id: "sess_abc123def456"}
			d, _ := dispatcherFor(sess, &fakePR{}, &fakeRunner{err: quota}, true)
			var held []Parked
			d.Park = func(_ context.Context, p Parked) error { held = append(held, p); return nil }
			req := baseReq()
			req.SessionID, req.Parked, req.Kept, req.Reason = "sess_abc123def456", true, Kept{Commit: newSHA}, agents.ParkLimit
			d.Run(t.Context(), req)
			if len(held) != 1 {
				t.Fatalf("parked %d times", len(held))
			}
			if c.limit && (held[0].At != resets || held[0].Tries != 0) {
				t.Errorf("parked as %+v, want a usage limit at %d with no failure counted", held[0], resets)
			}
			if !c.limit && (held[0].At == 0 || held[0].Tries != 1) {
				t.Errorf("parked as %+v, want a failed start counted, retried at a time", held[0])
			}
		})
	}
}

// A run the model API refused in a process that keeps no audit trail fails as it did before it
// could park, and says why; it is not left promising a try that cannot be made.
//
// MUTATION: swallow the park's refusal and the run reads parked while nothing can ever try it.
func TestARunThatCannotBeParkedFailsAndSaysWhy(t *testing.T) {
	authorities(t, resetsAt(0), nil)
	sess := &fakeSessions{id: "sess_abc123def456"}
	d, _ := dispatcherFor(sess, &fakePR{}, &fakeRunner{err: admission{zip.Errorf(http.StatusPaymentRequired, "top up")}}, true)
	d.Park = func(context.Context, Parked) error { return errNoTrail }
	req := baseReq()
	req.SessionID, req.Parked, req.Kept = "sess_abc123def456", true, Kept{Commit: newSHA}
	res := d.Run(t.Context(), req)
	if res.Parked || res.OK || len(sess.closes) != 1 || sess.closes[0].status != statusError || !strings.Contains(res.Error, "audit trail") {
		t.Fatalf("the run left %+v / closes %v, want it failed and saying that no trail is kept", res, sess.closes)
	}
}

// A chain the sweep dropped while its run flew is put back from the start the run carries, not begun
// again: the run that goes on is handed the chain's start (Req.Chain), hands it back in the park it
// makes, and the park is created in the chain it was. A run begun by a person's Continue carries none.
//
// MUTATION: hand the run no chain and the park after a forgotten chain begins another day; keep the
// carried start out of the write and the same.
func TestAChainTheSweepForgotIsPutBackFromWhatTheRunCarried(t *testing.T) {
	eachSite(t, func(t *testing.T, s *depot, pod func(string) *coord.Coord) {
		app := newSessionsApp(t)
		_, la, _ := podWith(t, s, pod, "cloud-0")
		row := parkedAs(t, la, app, "acme", "u1", "sess_back", time.Now().Unix()+3600)
		began := time.Now().Add(-23 * time.Hour).UnixMilli()
		if _, err := la.db.ExecContext(t.Context(), `DELETE FROM chains`); err != nil { // the sweep forgot it
			t.Fatal(err)
		}
		if _, err := la.drop(t.Context(), row.org, row.session, row.attempt); err != nil {
			t.Fatal(err)
		}
		row.created = began // what the run that was gone on with carried
		if err := la.park(t.Context(), row); err != nil {
			t.Fatal(err)
		}
		again, _, _ := la.parked(t.Context(), "acme", "sess_back")
		if again.created != began {
			t.Fatalf("the park is in a chain begun at %d, want the %d the run carried", again.created, began)
		}
		var since int64
		if err := la.db.QueryRowContext(t.Context(), `SELECT since FROM chains WHERE session = 'sess_back'`).Scan(&since); err != nil || since != began {
			t.Fatalf("the chain stands as %d (%v), want it put back as %d", since, err, began)
		}
	})
}
