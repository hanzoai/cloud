package coding

import (
	"context"
	"errors"
	"net/http"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanzoai/cloud/apps/agents"
	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/funds"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/hanzoai/cloud/sdk/coord"
	"github.com/zap-proto/zip"
)

// RED729B-1 (MED, liveness). The wake is armed to the EARLIEST park only (arm keeps the sooner timer), so the
// end-of-pass re-arm in fire() is what wakes the second park. Two runs parked for two different times must
// both go on with no other event. (If fire() did not re-arm, the second would wait for a funds event.)
func TestRed729bTwoParksAtDistinctTimesBothWakeWithNoOtherEvent(t *testing.T) {
	eachSite(t, func(t *testing.T, s *depot, pod func(string) *coord.Coord) {
		app := newSessionsApp(t)
		k := &launcher{}
		k.install(t)
		a, la, _ := podWith(t, s, pod, "cloud-0")
		now := time.Now().Unix()
		parkedAs(t, la, app, "acme", "u1", "sess_w1", now+1)
		parkedAs(t, la, app, "acme", "u2", "sess_w2", now+3)
		a.told(now + 1)
		a.told(now + 3)
		waitFor(t, "both parked runs to be gone on", func() bool { return len(k.started()) == 2 })
	})
}

// RED729B-2 (LOW). A try whose claim was SETTLED (an attempt that finished and launched) while its park row is
// still there -- a pod killed between Settle and the unpark/drop in startSession -- is a dead end: every later
// wake, funds event and Continue is answered the cached "accepted" outcome (proceed returns nil), the run is
// never launched, and Continue says success. Only Stop gets out.
func TestRed729bASettledAttemptWithTheParkStillThereIsNotADeadEnd(t *testing.T) {
	eachSite(t, func(t *testing.T, s *depot, pod func(string) *coord.Coord) {
		app := newSessionsApp(t)
		k := &launcher{}
		k.install(t)
		a, la, _ := podWith(t, s, pod, "cloud-0")
		row := parkedAs(t, la, app, "acme", "u1", "sess_dead", time.Now().Unix()-5)
		// What a pod that settled the claim and was killed before unparking leaves behind.
		if _, err := requested(t.Context(), la, row.org, row.subject, scoped("park", row.session), strconv.FormatInt(row.attempt, 10),
			printOf(struct {
				Session string
				Attempt int64
			}{row.session, row.attempt}),
			func(ctx context.Context) (attempted, error) {
				return attempted{Accepted: Accepted{SessionID: row.session}}, nil
			}); err != nil {
			t.Fatal(err)
		}
		for _, by := range []string{byWake, byContinue} {
			if err := a.proceed(t.Context(), row, by); err != nil {
				t.Fatalf("%s: %v", by, err)
			}
		}
		if got := k.started(); len(got) == 0 {
			t.Errorf("RED: the wake and the person's Continue were both answered 'done' and the run was never started; the park is still held (%d row) and the session still reads parked=%v",
				rowsOf(t, la), app.get("acme", "sess_dead").Parked())
		}
	})
}

// RED729B-3 (MED, bound). The age a run goes on by itself for was measured from the LAST park: park() set
// created=now on every park, and a relaunch drops the row, so a run refused again started a new row. A task
// that is refused every window was gone on by itself without end, each time within MaxLife of its latest park,
// and Unpark restarts the session's own started_at, so the reaper's MaxLife never reached it either. The chain's
// age is now kept apart from the park a relaunch drops (the chains table), and begins again only on a person's
// Continue. The relaunch is the production one: the park the try launched is dropped by its attempt.
//
// MUTATION: write created=now in park() (the old behaviour) and a re-park forgets 20 hours of the chain.
func TestRed729bAReparkedRunKeepsTheAgeOfItsChain(t *testing.T) {
	eachSite(t, func(t *testing.T, s *depot, pod func(string) *coord.Coord) {
		app := newSessionsApp(t)
		_, la, _ := podWith(t, s, pod, "cloud-0")
		row := parkedAs(t, la, app, "acme", "u1", "sess_chain", time.Now().Unix()+3600)
		twentyHours := time.Now().Add(-20 * time.Hour).UnixMilli()
		if _, err := la.db.ExecContext(t.Context(), `UPDATE chains SET since = $1 WHERE org = $2 AND session = $3`, twentyHours, row.org, row.session); err != nil {
			t.Fatal(err)
		}
		// The wake relaunched it (the park the try launched is dropped) and the run was refused again 20 hours
		// into the chain.
		if _, err := la.drop(t.Context(), row.org, row.session, row.attempt); err != nil {
			t.Fatal(err)
		}
		row.spec.Kept.Commit = newSHA
		if err := la.park(t.Context(), row); err != nil {
			t.Fatal(err)
		}
		again, _, _ := la.parked(t.Context(), "acme", "sess_chain")
		if age := time.Since(time.UnixMilli(again.created)); age < 19*time.Hour {
			t.Errorf("RED: after a re-park the chain is %v old; 20h of it were forgotten, so a run refused in every window is gone on by itself for ever (agents.MaxLife is %v)", age.Round(time.Minute), agents.MaxLife())
		}
	})
}

// RED729B-4 (LOW). Every funds event starts a goroutine (go fl.reconsider) that queues on the org's lock and,
// when it gets it, re-reads the org's parks and probes each one: nothing coalesces a burst. A burst of deposits
// (a bulk grant, a replayed processor webhook) puts a goroutine per event on every pod.
func TestRed729bABurstOfFundsEventsDoesNotQueueAGoroutinePerEvent(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		s := &depot{t: t, open: open}
		l := s.replica(func(string) *coord.Coord { return nil }, "cloud-0", periods{})
		fl := newFleet()
		fl.keep(l)
		hold := fl.serial("acme\x00") // a pass for acme is in hand (a slow start)
		defer hold()
		before := runtime.NumGoroutine()
		for i := 0; i < 2000; i++ {
			fl.funded(funds.Event{Org: "acme", Kind: funds.Credit})
		}
		time.Sleep(200 * time.Millisecond)
		if grew := runtime.NumGoroutine() - before; grew > 100 {
			t.Errorf("RED: 2000 events for one org left %d goroutines waiting on its lock; each will re-read the org's parks and re-probe each one in turn", grew)
		}
	})
}

// RED729B-0 (HIGH when CLOUD_AUDIT_DISABLED is set: a crash loop). plugin/agent/coding.go hands deps.Audit, a
// *audit.Recorder, to coding.Keep as a Trail. cloud.Recorder returns (nil, nil) when CLOUD_AUDIT_DISABLED is set,
// so the Trail is a NON-NIL interface holding a nil *Recorder: fl.audit's `l.trail == nil` guard (errNoTrail) is
// bypassed and Append dereferences the nil receiver. The panic is in the wake's timer goroutine (fire) or in
// reconsider's goroutine, neither of which recovers, so the process dies; the park row is untouched, so every
// restart (rescan -> fire) dies again.
func TestRed729bATrailThatIsANilRecorderRefusesAndDoesNotPanic(t *testing.T) {
	eachSite(t, func(t *testing.T, s *depot, pod func(string) *coord.Coord) {
		app := newSessionsApp(t)
		k := &launcher{}
		k.install(t)
		a, la, _ := podWith(t, s, pod, "cloud-0")
		var none *audit.Recorder // deps.Audit with CLOUD_AUDIT_DISABLED=true
		la.trail = none
		row := parkedAs(t, la, app, "acme", "u1", "sess_nil", time.Now().Unix()-5)
		var panicked any
		var err error
		func() {
			defer func() { panicked = recover() }()
			err = a.proceed(t.Context(), row, byWake)
		}()
		if panicked != nil {
			t.Fatalf("RED: a process whose audit trail is disabled panicked resuming a parked run: %v", panicked)
		}
		if err == nil || len(k.started()) != 0 {
			t.Fatalf("a process with no trail must refuse the try: err=%v started=%v", err, k.started())
		}
	})
}

// RED729B-5 (MED, liveness + wrong word). sandboxrunner wraps EVERY lease error as admission, and
// refusedReason answers "a 429 is a limit, anything else is money". A run that went on from a park and meets a
// lease that failed for a reason that is nobody's money (no sandbox capacity, the sandbox app rolling, a
// timeout) is parked again as "payment refused", with no time: no clock wakes it, only credits being added or a
// person's Continue, and the person is told they were refused payment.
func TestRed729bALeaseThatFailsForCapacityIsNotAPaymentRefusal(t *testing.T) {
	for name, err := range map[string]error{
		"no capacity":   zip.Errorf(http.StatusServiceUnavailable, "no sandbox capacity; try again"),
		"a timeout":     context.DeadlineExceeded,
		"gateway error": zip.Errorf(http.StatusBadGateway, "sandbox app unreachable"),
	} {
		t.Run(name, func(t *testing.T) {
			authorities(t, resetsAt(0), nil)
			sess := &fakeSessions{id: "sess_abc123def456"}
			run := &fakeRunner{err: admission{err}}
			d, _ := dispatcherFor(sess, &fakePR{}, run, true)
			var held []Parked
			d.Park = func(_ context.Context, p Parked) error { held = append(held, p); return nil }
			req := baseReq()
			req.SessionID, req.Parked, req.Kept = "sess_abc123def456", true, Kept{Commit: newSHA, Diffstat: "1 file changed"}
			d.Run(t.Context(), req)
			for _, p := range held {
				if p.Reason == agents.ParkPayment && p.At == 0 {
					t.Errorf("RED: a lease that failed with %v re-parked the run as %q with no time; no clock will try it again and the person is told their payment was refused", err, p.Reason)
				}
			}
		})
	}
}

// A trail that panics when it is written (a nil recorder behind a non-nil interface is only one way)
// is a failed try, kept for later with its delay: the panic is not the process's to die of, in the
// goroutine of a timer, at every start, because the park that causes it is still there.
//
// MUTATION: call proceed from attempts without survive and the panic takes the pass, and the test,
// down with it.
func TestAPanickingTryIsAFailedTryAndNotACrash(t *testing.T) {
	eachSite(t, func(t *testing.T, s *depot, pod func(string) *coord.Coord) {
		app := newSessionsApp(t)
		k := &launcher{}
		k.install(t)
		a, la, _ := podWith(t, s, pod, "cloud-0")
		la.trail = panickingTrail{}
		first := parkedAs(t, la, app, "acme", "u1", "sess_boom", time.Now().Unix()-5)

		a.fire() // the wake's timer goroutine: a panic here is the end of the process
		row, ok, _ := la.parked(t.Context(), "acme", "sess_boom")
		if !ok || row.tries != 1 || row.attempt != first.attempt || row.at <= time.Now().Unix() {
			t.Fatalf("the park reads %+v (%v), want one failed try counted, the attempt as it was, and a later time", row, ok)
		}
		a.reconsider("acme", "") // and the goroutine of a funds event
		if row, _, _ = la.parked(t.Context(), "acme", "sess_boom"); row.tries < 1 {
			t.Fatalf("the park reads %+v", row)
		}
		if len(k.started()) != 0 {
			t.Fatalf("started %v with a trail that panics", k.started())
		}
	})
}

// panickingTrail is an audit trail that cannot be written.
type panickingTrail struct{}

func (panickingTrail) Append(context.Context, audit.Record) (audit.Record, error) {
	panic("the trail's store is gone")
}

// A run refused in every window is not gone on by itself for ever: the day it goes on by itself
// for is the chain's. A park of a chain more than that old is a person's to continue, however it
// was written, and a person's Continue begins a chain of its own.
//
// MUTATION: begin the chain again at every park and this run is gone on by itself night after night;
// leave the chain standing through a Continue and a person who pressed it gets a day that is already
// spent.
func TestAChainPastItsDayIsContinueOnlyAndAContinueBeginsAnother(t *testing.T) {
	eachSite(t, func(t *testing.T, s *depot, pod func(string) *coord.Coord) {
		app := newSessionsApp(t)
		k := &launcher{}
		k.install(t)
		a, la, _ := podWith(t, s, pod, "cloud-0")
		old := parkedAs(t, la, app, "acme", "u1", "sess_old", time.Now().Unix()-5)
		if _, err := la.db.ExecContext(t.Context(), `UPDATE chains SET since = $1`, time.Now().Add(-agents.MaxLife()-time.Hour).UnixMilli()); err != nil {
			t.Fatal(err)
		}
		// Refused again: parked in the same chain, and past its day at once.
		if _, err := la.drop(t.Context(), old.org, old.session, old.attempt); err != nil {
			t.Fatal(err)
		}
		if err := la.park(t.Context(), old); err != nil {
			t.Fatal(err)
		}
		row, _, _ := la.parked(t.Context(), "acme", "sess_old")
		if !row.stale(time.Now()) {
			t.Fatalf("a park in a chain begun %v ago is not stale: created %d", agents.MaxLife()+time.Hour, row.created)
		}
		a.fire()
		a.reconsider("acme", "")
		time.Sleep(100 * time.Millisecond)
		if len(k.started()) != 0 {
			t.Fatalf("a park past its chain's day was gone on with: %v", k.started())
		}
		// A person presses Continue: it goes on, and the chain is begun again.
		if err := a.proceed(t.Context(), row, byContinue); err != nil {
			t.Fatal(err)
		}
		var n int
		if err := la.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM chains WHERE session = 'sess_old'`).Scan(&n); err != nil || n != 0 {
			t.Fatalf("the chain stands after a Continue: %d rows (%v)", n, err)
		}
		if err := la.park(t.Context(), old); err != nil {
			t.Fatal(err)
		}
		fresh, _, _ := la.parked(t.Context(), "acme", "sess_old")
		if fresh.stale(time.Now()) || time.Since(time.UnixMilli(fresh.created)) > time.Minute {
			t.Fatalf("the park after a Continue is in a chain begun at %d, want now", fresh.created)
		}
		if got := k.parks[len(k.parks)-1]; got.trigger != byContinue {
			t.Fatalf("the run that went on was handed %+v", got)
		}
	})
}

// A try the clock makes hands the run it launches the chain it is in and the count of starts that
// failed, so that when it is refused again it parks in that chain; a Continue hands it neither.
//
// MUTATION: hand over nothing and a start that fails for capacity is retried at the first delay for
// ever, and a chain begins again at every park.
func TestARunIsHandedItsChainAndItsCount(t *testing.T) {
	eachSite(t, func(t *testing.T, s *depot, pod func(string) *coord.Coord) {
		app := newSessionsApp(t)
		k := &launcher{}
		k.install(t)
		a, la, _ := podWith(t, s, pod, "cloud-0")
		row := parkedAs(t, la, app, "acme", "u1", "sess_h1", time.Now().Unix()-5)
		if _, err := la.db.ExecContext(t.Context(), `UPDATE parks SET tries = 3, created = $1`, time.Now().Add(-5*time.Hour).UnixMilli()); err != nil {
			t.Fatal(err)
		}
		row, _, _ = la.parked(t.Context(), "acme", "sess_h1")
		if err := a.proceed(t.Context(), row, byWake); err != nil {
			t.Fatal(err)
		}
		got := k.parks[0]
		if got.trigger != byWake || got.tries != 3 || got.created != row.created || got.reason != agents.ParkLimit {
			t.Fatalf("the run was handed %+v, want the park as it was, tried by the wake", got)
		}
	})
}

// A run that goes on and meets a lease that fails for a reason that is nobody's refusal parks as it
// was parked, for the time its next attempt is due and one failure more, and says what failed. A
// real refusal (a 402, a 429, a spent cap) is still a park for money or a limit, with the
// authority's time or none.
//
// MUTATION: map every admission failure to a reason (the old behaviour) and a rolling sandbox app
// reads to the person as a refusal of payment that only credits can clear.
func TestALeaseThatFailsForCapacityParksAsItWasAndSaysWhatFailed(t *testing.T) {
	authorities(t, resetsAt(0), nil)
	sess := &fakeSessions{id: "sess_abc123def456"}
	run := &fakeRunner{err: admission{zip.Errorf(http.StatusServiceUnavailable, "no sandbox capacity; try again")}}
	d, _ := dispatcherFor(sess, &fakePR{}, run, true)
	var held []Parked
	d.Park = func(_ context.Context, p Parked) error { held = append(held, p); return nil }
	req := baseReq()
	req.SessionID, req.Parked, req.Kept = "sess_abc123def456", true, Kept{Commit: newSHA}
	req.Reason, req.Tries, req.Chain = agents.ParkPayment, 2, time.Now().Add(-time.Hour).UnixMilli()
	before := time.Now()
	d.Run(t.Context(), req)
	if len(held) != 1 {
		t.Fatalf("parked %d times", len(held))
	}
	next := before.Add(retryAfter(3)).Unix()
	if p := held[0]; p.Reason != agents.ParkPayment || p.Tries != 3 || p.At < next || p.At > next+2 {
		t.Fatalf("parked as %+v, want the reason it had, a third failure, and a time %d", p, next)
	}
	said := ""
	for _, e := range sess.events {
		if e.kind == kindStatus && strings.Contains(e.payload, statusParked) {
			said = e.payload
		}
	}
	if !strings.Contains(said, "no sandbox capacity") {
		t.Fatalf("the parked event does not say what failed: %s", said)
	}

	// A real refusal is a refusal.
	for name, c := range map[string]struct {
		err    error
		reason string
	}{
		"a 402":       {zip.Errorf(http.StatusPaymentRequired, "top up"), agents.ParkPayment},
		"a spent cap": {unpayable{errors.New("spent its cap")}, agents.ParkPayment},
	} {
		sess := &fakeSessions{id: "sess_abc123def456"}
		d, _ := dispatcherFor(sess, &fakePR{}, &fakeRunner{err: admission{c.err}}, true)
		var held []Parked
		d.Park = func(_ context.Context, p Parked) error { held = append(held, p); return nil }
		d.Run(t.Context(), req)
		if len(held) != 1 || held[0].Reason != c.reason || held[0].Tries != 0 {
			t.Errorf("%s parked as %+v, want %s with no failure counted", name, held, c.reason)
		}
	}
}

// The day a run goes on by itself for is the chain's: a park whose authority names a time past the
// chain's day names none.
//
// MUTATION: bound the time from now, not from the chain's start, and a run in a chain 23 hours old
// is promised a time it will not be gone on at.
func TestAParkBeyondTheChainsDayNamesNoTime(t *testing.T) {
	authorities(t, resetsAt(0), &client.Pool{State: "exhausted", Resets: time.Now().Add(2 * time.Hour).Unix()})
	sess := &fakeSessions{id: "sess_abc123def456"}
	run := &fakeRunner{err: admission{zip.Errorf(http.StatusTooManyRequests, "the free lane is used")}}
	d, _ := dispatcherFor(sess, &fakePR{}, run, true)
	var held []Parked
	d.Park = func(_ context.Context, p Parked) error { held = append(held, p); return nil }
	req := baseReq()
	req.SessionID, req.Parked, req.Kept = "sess_abc123def456", true, Kept{Commit: newSHA}
	req.Chain = time.Now().Add(-agents.MaxLife() + 30*time.Minute).UnixMilli()
	d.Run(t.Context(), req)
	if len(held) != 1 || held[0].At != 0 {
		t.Fatalf("parked as %+v, want no time: the authority's is past the chain's day", held)
	}
	// In a chain that is young the same authority's time stands.
	held = nil
	req.Chain = time.Now().Add(-time.Hour).UnixMilli()
	sess = &fakeSessions{id: "sess_abc123def456"}
	d, _ = dispatcherFor(sess, &fakePR{}, &fakeRunner{err: admission{zip.Errorf(http.StatusTooManyRequests, "the free lane is used")}}, true)
	d.Park = func(_ context.Context, p Parked) error { held = append(held, p); return nil }
	d.Run(t.Context(), req)
	if len(held) != 1 || held[0].At == 0 {
		t.Fatalf("parked as %+v, want the authority's time inside the chain's day", held)
	}
}

// An event that arrives while a pass is running is not lost: the pass has read the parks, so what
// the event changed is for the pass that waits behind it. A park made after the running pass read the
// rows is tried by it, once the first is done; and a burst is two passes, not a goroutine per event.
//
// MUTATION: drop the waiting pass and the second run is not tried until something else happens.
func TestAnEventDuringAPassGetsItsOwnPass(t *testing.T) {
	eachSite(t, func(t *testing.T, s *depot, pod func(string) *coord.Coord) {
		app := newSessionsApp(t)
		k := &launcher{gate: make(chan struct{})}
		k.install(t)
		release := sync.OnceFunc(func() { close(k.gate) })
		t.Cleanup(release)
		fl, la, _ := podWith(t, s, pod, "cloud-0")
		parkedAs(t, la, app, "acme", "u1", "sess_first", 0)
		fl.funded(funds.Event{Org: "acme", Kind: funds.Credit})
		waitFor(t, "the first pass to be in its first try", func() bool { return len(k.started()) == 1 })

		parkedAs(t, la, app, "acme", "u2", "sess_second", 0) // made after the running pass read the rows
		before := runtime.NumGoroutine()
		for range 500 {
			fl.funded(funds.Event{Org: "acme", Kind: funds.Credit})
		}
		if grew := runtime.NumGoroutine() - before; grew > 20 {
			t.Errorf("500 events left %d goroutines", grew)
		}
		release()
		waitFor(t, "the waiting pass to try the second run", func() bool { return len(k.started()) == 2 })
		waitFor(t, "the passes to finish", func() bool { fl.Lock(); defer fl.Unlock(); return len(fl.passes) == 0 })
		if got := k.started(); !slices.Equal(got, []string{"sess_first", "sess_second"}) {
			t.Fatalf("started %v", got)
		}
	})
}

// A run that goes on and meets a lease that fails for capacity, twice, is parked each time with one
// failure more counted: the count and the chain are handed from the park to the run that goes on, and
// back to the park it makes. The run is the real one, through the start.
//
// MUTATION: leave the count out of the run's request and every failed lease is the first, tried again
// after the first delay for ever.
func TestAStartThatFailsForCapacityTwiceCountsBothAndKeepsItsChain(t *testing.T) {
	eachSite(t, func(t *testing.T, s *depot, pod func(string) *coord.Coord) {
		var turns atomic.Int32
		r := refusedRun(t, 1, &turns)
		app := newSessionsApp(t)
		authorities(t, resetsAt(0), nil)
		c := pod("cloud-0")
		l := s.replica(func(string) *coord.Coord { return c }, "cloud-0", periods{})
		keeping(t, l)
		t.Cleanup(flights.watch(c))

		acc := parkedRun(t, r, app, theTask())
		first, _, _ := l.parked(t.Context(), "hanzo", acc.SessionID)
		r.pod.mu.Lock()
		r.pod.leaseErr = func() error { return zip.Errorf(http.StatusServiceUnavailable, "no sandbox capacity; try again") }
		r.pod.mu.Unlock()

		// The first failed start: credits are added, the run goes on, and its lease fails.
		funds.Tell(c, funds.Event{Org: "hanzo", Kind: funds.Credit})
		waitFor(t, "the run to be parked for a failed start", func() bool {
			p, ok, _ := l.parked(t.Context(), "hanzo", acc.SessionID)
			return ok && p.attempt != first.attempt && p.tries == 1
		})
		letGo(t, flights, acc.SessionID)
		p, _, _ := l.parked(t.Context(), "hanzo", acc.SessionID)
		if p.reason != agents.ParkLimit || p.at <= time.Now().Unix() || p.created != first.created {
			t.Fatalf("parked as %+v, want the reason it had, a later time and the chain it was in (%d)", p, first.created)
		}

		// Its time comes (made due by hand), the sweep has forgotten the chain, and the lease fails
		// again: the second failure, in the chain the run was handed.
		if _, err := l.db.ExecContext(t.Context(), `UPDATE parks SET resume_at = $1`, time.Now().Unix()-1); err != nil {
			t.Fatal(err)
		}
		if _, err := l.db.ExecContext(t.Context(), `DELETE FROM chains`); err != nil {
			t.Fatal(err)
		}
		flights.fire()
		waitFor(t, "the second failed start to be counted", func() bool {
			q, ok, _ := l.parked(t.Context(), "hanzo", acc.SessionID)
			return ok && q.attempt != p.attempt && q.tries == 2
		})
		letGo(t, flights, acc.SessionID)
		q, _, _ := l.parked(t.Context(), "hanzo", acc.SessionID)
		if q.created != first.created {
			t.Fatalf("the chain was forgotten: created %d, was %d", q.created, first.created)
		}
		if said := statusesOf(r); !slices.Contains(said, statusContinued) {
			t.Fatalf("the stream: %v", said)
		}
	})
}

// A person who presses Continue is answered as one: a run whose person may not act is forbidden,
// a start that failed is a conflict that says why, and a try that could not be made is unavailable;
// never a bare error that reads as a fault.
//
// MUTATION: return the try's error as it is and each of these is a 500.
func TestContinueIsAnsweredWithAStatusAndTheReason(t *testing.T) {
	eachSite(t, func(t *testing.T, s *depot, pod func(string) *coord.Coord) {
		app := newSessionsApp(t)
		k := &launcher{}
		k.install(t)
		a, la, _ := podWith(t, s, pod, "cloud-0")
		status := func(err error) (int, string) {
			t.Helper()
			var he *zip.HTTPError
			if !errors.As(err, &he) {
				t.Fatalf("answered %v, which is not a status", err)
			}
			return he.Status, he.Error()
		}
		ask := func(session string) error {
			_, handled, err := a.parkedWord(t.Context(), "acme", "u-"+session, client.CodingSteerIn{SessionID: session})
			if !handled {
				t.Fatalf("%s was not taken as a parked run's", session)
			}
			return err
		}

		parkedAs(t, la, app, "acme", "u-gone", "gone", 0)
		app.revoke("acme", "u-gone")
		if code, why := status(ask("gone")); code != http.StatusForbidden || !strings.Contains(why, "access removed") {
			t.Errorf("a person who may not act was answered %d %q, want 403 and why", code, why)
		}

		parkedAs(t, la, app, "acme", "u-fails", "fails", 0)
		k.fail(errors.New("forge refused"))
		if code, why := status(ask("fails")); code != http.StatusConflict || !strings.Contains(why, "forge refused") {
			t.Errorf("a start that failed was answered %d %q, want 409 and why", code, why)
		}
		k.fail(nil)

		parkedAs(t, la, app, "acme", "u-notrail", "notrail", 0)
		s.trail.refuse(errors.New("the trail is down"))
		if code, why := status(ask("notrail")); code != http.StatusServiceUnavailable || !strings.Contains(why, "trail") {
			t.Errorf("a try the trail would not take was answered %d %q, want 503 and why", code, why)
		}
		s.trail.refuse(nil)
	})
}

// A start that fails is on the trail as an intent and then as an error, never as a success.
//
// MUTATION: write the one record before the start as a success and a failed start reads as one.
func TestAStartThatFailedIsNotOnTheTrailAsASuccess(t *testing.T) {
	eachSite(t, func(t *testing.T, s *depot, pod func(string) *coord.Coord) {
		app := newSessionsApp(t)
		k := &launcher{}
		k.install(t)
		k.fail(errors.New("forge refused"))
		a, la, _ := podWith(t, s, pod, "cloud-0")
		row := parkedAs(t, la, app, "acme", "u1", "sess_f1", time.Now().Unix()-5)
		_ = a.proceed(t.Context(), row, byWake)
		var results []string
		for _, r := range s.trail.records() {
			results = append(results, r.Outcome.Result)
		}
		if !slices.Equal(results, []string{"intent", "error"}) {
			t.Fatalf("the trail holds %v, want the intent and the error", results)
		}
	})
}

// A run that is stopped leaves neither its park nor its chain: nothing of it is tried, and a
// session that is never parked again has no chain to be swept.
//
// MUTATION: leave the chain standing when a run is stopped and its rows outlive it.
func TestAStoppedRunLeavesNoChain(t *testing.T) {
	eachSite(t, func(t *testing.T, s *depot, pod func(string) *coord.Coord) {
		app := newSessionsApp(t)
		fl, la, _ := podWith(t, s, pod, "cloud-0")
		parkedAs(t, la, app, "acme", "u1", "sess_stopped", 0)
		fl.released("acme", "sess_stopped")
		var parks, chains int
		if err := la.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM parks`).Scan(&parks); err != nil {
			t.Fatal(err)
		}
		if err := la.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM chains`).Scan(&chains); err != nil {
			t.Fatal(err)
		}
		if parks != 0 || chains != 0 {
			t.Fatalf("a stopped run left %d parks and %d chains", parks, chains)
		}
	})
}

// The sweep drops the chain of a session that has no park and began so long ago that no run of it can
// be flying (twice the day a run goes on by itself for), and nothing else: not a chain with a park, not
// a young one, not one a run may be flying in.
//
// MUTATION: drop every chain and a park's chain is lost under it; drop none and a finished
// session's chain stands for ever.
func TestTheSweepDropsOnlySpentChains(t *testing.T) {
	eachSite(t, func(t *testing.T, s *depot, pod func(string) *coord.Coord) {
		app := newSessionsApp(t)
		_, la, _ := podWith(t, s, pod, "cloud-0")
		parkedAs(t, la, app, "acme", "u1", "sess_held", 0)  // a park, an old chain
		parkedAs(t, la, app, "acme", "u2", "sess_young", 0) // a park, a young chain
		parkedAs(t, la, app, "acme", "u3", "sess_spent", 0)
		parkedAs(t, la, app, "acme", "u4", "sess_recent", 0)
		old := time.Now().Add(-2*agents.MaxLife() - time.Hour).UnixMilli()
		for _, id := range []string{"sess_held", "sess_spent"} {
			if _, err := la.db.ExecContext(t.Context(), `UPDATE chains SET since = $1 WHERE session = $2`, old, id); err != nil {
				t.Fatal(err)
			}
		}
		// The runs of the last two went on and finished: no park is left.
		for _, id := range []string{"sess_spent", "sess_recent"} {
			if _, err := la.db.ExecContext(t.Context(), `DELETE FROM parks WHERE session = $1`, id); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := la.prune(t.Context()); err != nil {
			t.Fatal(err)
		}
		rows, err := la.db.QueryContext(t.Context(), `SELECT session FROM chains ORDER BY session`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var left []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				t.Fatal(err)
			}
			left = append(left, id)
		}
		if want := []string{"sess_held", "sess_recent", "sess_young"}; !slices.Equal(left, want) {
			t.Fatalf("the chains left are %v, want %v", left, want)
		}
	})
}
