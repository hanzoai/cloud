package coding

// park_test.go — a run the model API refused waits, and goes on by itself: the park is
// one durable step, the wake is a timer set from the rows and not a ticker, funds
// changing try the org's runs, and a try is a request in the ledger, so of any number of
// pods, one launches the run. Everything runs on both engines and, where pods talk, on
// both buses.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
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
	"github.com/hanzoai/cloud/sdk/coord/coordtest"
	"github.com/zap-proto/zip"
)

// memTrail is the audit trail of a test: every record it was handed, in order, and an
// error to refuse them with.
type memTrail struct {
	mu   sync.Mutex
	recs []audit.Record
	fail error
}

func (m *memTrail) Append(_ context.Context, rec audit.Record) (audit.Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail != nil {
		return audit.Record{}, m.fail
	}
	rec.Seq = uint64(len(m.recs) + 1)
	m.recs = append(m.recs, rec)
	return rec, nil
}

// records is every record taken, in order.
func (m *memTrail) records() []audit.Record {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.recs)
}

// refuse makes the trail answer err to every record from now on.
func (m *memTrail) refuse(err error) { m.mu.Lock(); m.fail = err; m.mu.Unlock() }

// sessionsApp stands in for the sessions app that holds a parked run's session: the same
// two guarded updates (park a running session, unpark a parked one, each won by one
// caller) and the read as the person whose run it is.
type sessionsApp struct {
	mu      sync.Mutex
	rows    map[string]*agents.Session
	parks   []string // every park, as "<session> <reason> <at>"
	unparks int
	// out is who IAM no longer lets act in an org, as "<org>/<person>"; ends is every
	// session the platform ended, as "<session>: <reason>".
	out  map[string]bool
	ends []string
}

func newSessionsApp(t *testing.T) *sessionsApp {
	t.Helper()
	a := &sessionsApp{rows: map[string]*agents.Session{}, out: map[string]bool{}}
	wasPark, wasUnpark, wasVisible := parkSession, unparkSession, visibleRun
	wasAct, wasEnd := mayAct, endSession
	wasAllowed, wasPooled, wasPayable, wasPlan := allowed, pooled, payable, onPlan
	// Every authority says what clears nothing and costs nothing: nobody is refused, nothing
	// is spent, nobody is on a plan. A test that asks something of one states it.
	allowed = func(context.Context, *client.AllowanceIn) (*client.Allowance, error) { return &client.Allowance{}, nil }
	pooled = func(context.Context, *client.PoolIn) (*client.Pool, error) {
		return &client.Pool{State: "available"}, nil
	}
	payable = func(context.Context, string, *client.AuthorizeIn) (*client.Verdict, error) {
		return &client.Verdict{OK: true}, nil
	}
	onPlan = func(context.Context, string, string) (string, error) { return "", nil }
	mayAct = func(_ context.Context, org, person string) (bool, error) {
		a.mu.Lock()
		defer a.mu.Unlock()
		return !a.out[org+"/"+person], nil
	}
	endSession = func(_ context.Context, org, id, reason string) (bool, error) {
		a.mu.Lock()
		defer a.mu.Unlock()
		x := a.rows[org+"/"+id]
		if x == nil || x.Status == agents.StatusStopped {
			return false, nil
		}
		x.Status, x.Park, x.ParkAt = agents.StatusStopped, "", 0
		a.ends = append(a.ends, id+": "+reason)
		return true, nil
	}
	parkSession = func(_ context.Context, org, id, reason string, at int64) (bool, error) {
		a.mu.Lock()
		defer a.mu.Unlock()
		x := a.rows[org+"/"+id]
		if x == nil || !(x.Status == agents.StatusRunning || x.Parked()) {
			return false, nil
		}
		x.Status, x.Park, x.ParkAt, x.Box = agents.StatusPaused, reason, at, ""
		a.parks = append(a.parks, id+" "+reason+" "+time.Unix(at, 0).UTC().Format(time.RFC3339))
		return true, nil
	}
	unparkSession = func(_ context.Context, org, id string) (bool, error) {
		a.mu.Lock()
		defer a.mu.Unlock()
		x := a.rows[org+"/"+id]
		if x == nil || !x.Parked() {
			return false, nil
		}
		x.Status, x.Park, x.ParkAt = agents.StatusRunning, "", 0
		a.unparks++
		return true, nil
	}
	visibleRun = func(ctx context.Context, id string) (agents.Session, error) {
		who := zip.CallerOf(ctx)
		a.mu.Lock()
		defer a.mu.Unlock()
		if x := a.rows[who.Org+"/"+id]; x != nil && x.Actor == who.Org+"/"+who.User {
			return *x, nil
		}
		return agents.Session{}, zip.ErrNotFound("session not found")
	}
	t.Cleanup(func() {
		parkSession, unparkSession, visibleRun = wasPark, wasUnpark, wasVisible
		mayAct, endSession = wasAct, wasEnd
		allowed, pooled, payable, onPlan = wasAllowed, wasPooled, wasPayable, wasPlan
	})
	return a
}

// revoke makes IAM say person may no longer act in org.
func (a *sessionsApp) revoke(org, person string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.out[org+"/"+person] = true
}

// ended is every session the platform ended, as "<session>: <reason>".
func (a *sessionsApp) ended() []string { a.mu.Lock(); defer a.mu.Unlock(); return slices.Clone(a.ends) }

// run adds a running coding run of person in org.
func (a *sessionsApp) run(org, person, id string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.rows[org+"/"+id] = &agents.Session{ID: id, Org: org, Actor: org + "/" + person, Kind: kindCoding, Status: agents.StatusRunning,
		Repo: "hanzo-apps/site", Base: "trunk", Branch: BranchFor(id), Box: "sbx_1"}
}

func (a *sessionsApp) get(org, id string) agents.Session {
	a.mu.Lock()
	defer a.mu.Unlock()
	return *a.rows[org+"/"+id]
}

func (a *sessionsApp) stop(org, id string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	x := a.rows[org+"/"+id]
	x.Status, x.Park, x.ParkAt = agents.StatusStopped, "", 0
}

// authorities makes the allowance app and the free pool say what a test needs.
func authorities(t *testing.T, resets func() int64, pool *client.Pool) {
	t.Helper()
	wasAllowed, wasPooled := allowed, pooled
	allowed = func(context.Context, *client.AllowanceIn) (*client.Allowance, error) {
		if at := resets(); at != 0 {
			return &client.Allowance{Spent: true, Resets: at, Window: "session", Limit: 3, Used: 3}, nil
		}
		return &client.Allowance{}, nil
	}
	pooled = func(context.Context, *client.PoolIn) (*client.Pool, error) {
		if pool == nil {
			return &client.Pool{State: "available"}, nil
		}
		return pool, nil
	}
	t.Cleanup(func() { allowed, pooled = wasAllowed, wasPooled })
}

func resetsAt(at int64) func() int64 { return func() int64 { return at } }

// The counters a run's goroutines move, read under the locks that guard them.
func (a *sessionsApp) parked() int   { a.mu.Lock(); defer a.mu.Unlock(); return len(a.parks) }
func (a *sessionsApp) unparked() int { a.mu.Lock(); defer a.mu.Unlock(); return a.unparks }
func boxesGivenBack(p *pod) int      { p.mu.Lock(); defer p.mu.Unlock(); return p.ends }
func leases(p *pod) int              { p.mu.Lock(); defer p.mu.Unlock(); return len(p.leased) }

// armed is when the pod's timer is set for, unix seconds, 0 when it is not.
func armed(fl *fleet) int64 { w := fl.alarm(); w.mu.Lock(); defer w.mu.Unlock(); return w.at }

// refusedRun is the rig of a run on hanzo-apps/site whose model API refuses its first
// turns: the next `refusals` agent turns end "exceeded retry limit, last status: 429", and
// the ones after answer. The branch a push names is on GitHub from then on.
func refusedRun(t *testing.T, refusals int, turns *atomic.Int32) *rig {
	t.Helper()
	return refusingRun(t, refusals, turns, "exceeded retry limit, last status: 429 Too Many Requests, request id: r1")
}

// refusingRun is refusedRun where the harness ends its refused turns with message.
func refusingRun(t *testing.T, refusals int, turns *atomic.Int32, message string) *rig {
	t.Helper()
	return refusingRunBy(t, refusals, turns, func(int) string { return message })
}

// refusingRunBy is refusingRun where turn n (from 1) is refused with message(n).
func refusingRunBy(t *testing.T, refusals int, turns *atomic.Int32, message func(n int) string) *rig {
	t.Helper()
	r := newRig()
	r.admit = pushes(map[string]string{"sub-zoe": "zoe-gh"}, nil)
	r.refs = map[string]string{}
	tip := theSHA
	var mu sync.Mutex
	r.pod.answer = func(argv []string) client.Ran {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case slices.Contains(argv, "--skip-git-repo-check"):
			if n := int(turns.Add(1)); n <= refusals {
				turn, _ := json.Marshal(map[string]any{"type": "turn.failed", "error": map[string]string{"message": message(n)}})
				return client.Ran{ExitCode: 1, Stdout: `{"type":"thread.started","thread_id":"t1"}` + "\n" + string(turn) + "\n"}
			}
			return client.Ran{Stdout: `{"type":"thread.started","thread_id":"t2"}` + "\n" +
				`{"type":"item.completed","item":{"type":"agent_message","text":"Fixed the header and pushed."}}` + "\n"}
		case slices.Contains(argv, "commit"):
			tip = newSHA
		case slices.Contains(argv, "push"):
			ref := argv[len(argv)-1]
			r.mu.Lock()
			r.refs[strings.TrimPrefix(ref, "HEAD:refs/heads/")] = newSHA
			r.mu.Unlock()
		case slices.Contains(argv, "rev-parse"):
			return client.Ran{Stdout: tip + "\n"}
		case slices.Contains(argv, "diff"):
			return client.Ran{Stdout: " 1 file changed\n"}
		}
		return client.Ran{}
	}
	r.serve(t)
	return r
}

// parkedRun starts a run on r whose model is refused, and returns when it has parked.
func parkedRun(t *testing.T, r *rig, app *sessionsApp, in client.CodingStartIn) Accepted {
	t.Helper()
	app.run("hanzo", "sub-zoe", "sess_abc123def456")
	acc, err := r.startAs(t, asCaller("sub-zoe"), "sub-zoe", in)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	return acc
}

func theTask() client.CodingStartIn {
	return client.CodingStartIn{Repo: "github.com/hanzo-apps/site", Prompt: "fix the header", Pull: true}
}

// eventsOf is the events a kind of a session recorded, as their JSON.
func eventsOf(r *rig, kind string) []string {
	r.sessions.mu.Lock()
	defer r.sessions.mu.Unlock()
	var out []string
	for _, e := range r.sessions.events {
		if e.kind == kind {
			out = append(out, e.payload)
		}
	}
	return out
}

// statusesOf is the status words a session was told, in order.
func statusesOf(r *rig) []string {
	var out []string
	for _, p := range eventsOf(r, kindStatus) {
		var e struct {
			Status string `json:"status"`
		}
		_ = json.Unmarshal([]byte(p), &e)
		out = append(out, e.Status)
	}
	return out
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// letGo waits for the run flying session to let go.
func letGo(t *testing.T, fl *fleet, session string) {
	t.Helper()
	waitFor(t, "the run to land", func() bool {
		fl.Lock()
		defer fl.Unlock()
		return fl.m[session] == nil
	})
}

// A turn the model API refused for a usage limit parks the run. Its work is on its branch,
// its sandbox is given back, the park is one row, the session is told once, and every place
// that says what a run is doing says parked, why, and when it goes on: its stream, its
// card, its progress. Nothing was closed, and no pull request was opened for work that is
// not done. When credits are added the run goes on in the SAME session, from the branch,
// in a sandbox of its own, with the task it was asked and a line saying it is continuing,
// and finishes: the pull request is opened once, for the work of both runs.
//
// MUTATION: end the run failed on a 429 (the old behaviour) and there is no park, a closed
// session and nothing to go on; park without keeping the work and the branch is empty;
// without ending the sandbox the box is held for the whole wait.
func TestARunTheModelAPIRefusedParksAndGoesOnWhenCreditsAreAdded(t *testing.T) {
	eachSite(t, func(t *testing.T, s *depot, pod func(string) *coord.Coord) {
		var turns atomic.Int32
		r := refusedRun(t, 1, &turns)
		app := newSessionsApp(t)
		resets := time.Now().Add(5 * time.Hour).Unix()
		var credited atomic.Bool
		authorities(t, func() int64 { // the allowance is spent for five hours, until credits are added
			if credited.Load() {
				return 0
			}
			return resets
		}, nil)
		c := pod("cloud-0")
		l := s.replica(func(string) *coord.Coord { return c }, "cloud-0", periods{})
		keeping(t, l)
		stop := flights.watch(c)
		t.Cleanup(stop)

		acc := parkedRun(t, r, app, theTask())

		// Parked: the work is on the branch, the sandbox is given back, one row.
		r.mu.Lock()
		held := r.refs[acc.Branch]
		r.mu.Unlock()
		if held != newSHA {
			t.Fatalf("the refused run's work is not on %s (%v):\n%s", acc.Branch, held, strings.Join(r.pod.lines(), "\n"))
		}
		if boxesGivenBack(r.pod) != 1 {
			t.Fatalf("a parked run ended %d sandboxes, want its box given back", boxesGivenBack(r.pod))
		}
		if len(r.todo.inputs) != 0 || len(r.pulls) != 0 {
			t.Fatalf("a pull request was opened for a run that is not done: %v %v", r.todo.inputs, r.pulls)
		}
		r.sessions.mu.Lock()
		closes := len(r.sessions.closes)
		r.sessions.mu.Unlock()
		if closes != 0 {
			t.Fatalf("a parked run's session was closed %d times", closes)
		}
		x := app.get("hanzo", acc.SessionID)
		if !x.Parked() || x.Park != agents.ParkLimit || x.ParkAt != resets || app.parked() != 1 {
			t.Fatalf("the session reads %+v after %v, want one park for the limit at %d", x, app.parks, resets)
		}
		rows, err := l.parksOf(t.Context(), "hanzo")
		if err != nil || len(rows) != 1 || rows[0].session != acc.SessionID || rows[0].at != resets || rows[0].reason != agents.ParkLimit ||
			rows[0].spec.In.Prompt != "fix the header" || rows[0].spec.Kept.Commit != newSHA || rows[0].subject != "sub-zoe" {
			t.Fatalf("the park row is %+v (%v), want the run as it was asked, one row", rows, err)
		}

		// What the run says about itself.
		say := agents.ParkSaid(agents.ParkLimit, resets)
		var parked struct{ Status, Reason, Message, ResumeAt string }
		for _, p := range eventsOf(r, kindStatus) {
			var e struct{ Status, Reason, Message, ResumeAt string }
			_ = json.Unmarshal([]byte(p), &e)
			if e.Status == statusParked {
				parked = e
			}
		}
		if parked.Message != say || parked.Reason != agents.ParkLimit || parked.ResumeAt != time.Unix(resets, 0).UTC().Format(time.RFC3339) {
			t.Fatalf("the stream's parked event is %+v, want %q", parked, say)
		}
		if slices.Contains(statusesOf(r), "error") || slices.Contains(statusesOf(r), "done") {
			t.Fatalf("a parked run reported %v", statusesOf(r))
		}
		var activity string
		for _, p := range eventsOf(r, kindProgress) {
			var e struct{ Phase, Activity string }
			_ = json.Unmarshal([]byte(p), &e)
			activity = e.Phase + " " + e.Activity
		}
		if activity != "blocked "+say {
			t.Fatalf("the run's progress reads %q, want blocked and its sentence", activity)
		}
		line, terminal := renderLine(kindStatus, []byte(`{"status":"parked","message":"`+say+`","changed":true,"branch":"`+acc.Branch+`"}`))
		if !terminal || !strings.Contains(line, say) || !strings.Contains(line, "Work so far is on `"+acc.Branch+"`.") {
			t.Fatalf("the card reads %q (terminal %v)", line, terminal)
		}
		// A timer is set for the time the authority named, and no other.
		if got := armed(flights); got != resets {
			t.Fatalf("the wake is set for %d, want the authority's %d", got, resets)
		}

		// Credits are added: the run goes on in its session, from its branch.
		leasesBefore := leases(r.pod)
		credited.Store(true)
		funds.Tell(c, funds.Event{Org: "hanzo", Kind: funds.Credit})
		waitFor(t, "the run to be unparked", func() bool { return app.unparked() == 1 })
		waitFor(t, "the run to finish", func() bool {
			if slices.Contains(statusesOf(r), "error") {
				t.Fatalf("the run that went on failed: %v\n%v", eventsOf(r, kindStatus), r.sessions.closes)
			}
			return slices.Contains(statusesOf(r), "done")
		})
		letGo(t, flights, acc.SessionID)

		if got := statusesOf(r); !slices.Contains(got, statusContinued) {
			t.Fatalf("the stream never said the run continued: %v", got)
		}
		// It says what made it go on, and the try is on the audit trail as the person's.
		var continued struct{ Status, Trigger string }
		for _, p := range eventsOf(r, kindStatus) {
			var e struct{ Status, Trigger string }
			_ = json.Unmarshal([]byte(p), &e)
			if e.Status == statusContinued {
				continued = e
			}
		}
		if continued.Trigger != byFunds {
			t.Fatalf("the continued event names the trigger %q, want %q", continued.Trigger, byFunds)
		}
		recs := s.trail.records()
		if len(recs) != 2 || recs[0].Outcome.Result != "intent" || recs[1].Outcome.Result != "success" {
			t.Fatalf("the audit trail holds %+v, want the intent of the one try and its success", recs)
		}
		for _, r := range recs {
			if r.Action != "coding.park.resume" || r.Actor.Org != "hanzo" || r.Actor.Sub != "sub-zoe" || r.Resource.ID != acc.SessionID || !strings.Contains(string(r.After), `"trigger":"funds"`) {
				t.Fatalf("the audit record %+v is not the try, by funds, as the person", r)
			}
		}
		if leases(r.pod) != leasesBefore+1 {
			t.Fatalf("the run that went on leased %d sandboxes, want one of its own", leases(r.pod)-leasesBefore)
		}
		turnsRun, fed := 0, ""
		r.pod.mu.Lock()
		for i, argv := range r.pod.ran {
			if slices.Contains(argv, "--skip-git-repo-check") {
				turnsRun++
				fed = r.pod.fed[i]
			}
		}
		r.pod.mu.Unlock()
		if turnsRun != 2 || !strings.Contains(fed, continuingLine) || !strings.Contains(fed, "fix the header") {
			t.Fatalf("the run that went on was given %q (%d turns), want its task and the line that it continues", fed, turnsRun)
		}
		if cmd, ok := r.pod.did("-b " + acc.Branch); !ok {
			t.Fatalf("the run that went on did not clone its branch %s:\n%s", acc.Branch, strings.Join(r.pod.lines(), "\n"))
		} else if !strings.Contains(cmd, "clone") {
			t.Fatalf("the branch was not carried on by a clone: %q", cmd)
		}
		if len(r.todo.inputs) != 1 || r.todo.inputs[0].Head != acc.Branch {
			t.Fatalf("the pull request was filed %v, want once, for the branch both runs wrote", r.todo.inputs)
		}
		// The park is released by the run that went on, after its turns: wait for it
		// rather than reading once, which a loaded runner loses.
		waitFor(t, "the park released after the run went on", func() bool {
			again, _ := l.parksOf(t.Context(), "hanzo")
			x := app.get("hanzo", acc.SessionID)
			return len(again) == 0 && !x.Parked() && x.Park == ""
		})

		// The same credit told again goes on with nothing: there is nothing parked.
		funds.Tell(c, funds.Event{Org: "hanzo", Kind: funds.Credit})
		time.Sleep(100 * time.Millisecond)
		if app.unparked() != 1 || leases(r.pod) != leasesBefore+1 {
			t.Fatalf("a second credit launched again: %d unparks, %d leases", app.unparked(), leases(r.pod))
		}
	})
}

// The wake is the time the authority named, taken from it, and the run goes on when it
// comes with no one saying so: a timer set from the rows, one launch.
//
// MUTATION: never arm the timer from a park and the run waits for credits that may never
// come; arm it at a guess (midnight) and it goes on at the wrong time.
func TestAParkedRunGoesOnWhenItsLimitClears(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		s := &depot{t: t, open: open}
		pod := coordtest.Memory(t)
		var turns atomic.Int32
		r := refusedRun(t, 1, &turns)
		app := newSessionsApp(t)
		// The allowance app says the person's window reopens two seconds from now.
		resets := time.Now().Add(2 * time.Second).Unix()
		authorities(t, resetsAt(resets), nil)
		c := pod("cloud-0")
		keeping(t, s.replica(func(string) *coord.Coord { return c }, "cloud-0", periods{}))
		t.Cleanup(flights.watch(c))

		acc := parkedRun(t, r, app, theTask())
		if app.unparked() != 0 {
			t.Fatal("the run went on before its limit cleared")
		}
		waitFor(t, "the run to go on by itself", func() bool { return slices.Contains(statusesOf(r), "done") })
		letGo(t, flights, acc.SessionID)
		if time.Now().Unix() < resets {
			t.Fatalf("the run went on before %d", resets)
		}
		if app.unparked() != 1 || turns.Load() != 2 {
			t.Fatalf("%d unparks and %d turns, want one launch", app.unparks, turns.Load())
		}
		if len(r.todo.inputs) != 1 {
			t.Fatalf("the pull request was filed %d times", len(r.todo.inputs))
		}
	})
}

// A run refused again parks again, with the reason and the time the refusal now names. It
// does not go round: the park without a time sets no timer, so nothing tries it until
// credits are added or a person asks, however long it is left.
//
// MUTATION: retry a refused try at once (or on a ticker) and the run is a tight loop of
// sandboxes; park it again at the time that just fell due and the same.
func TestARunRefusedAgainParksAgainAndDoesNotLoop(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		s := &depot{t: t, open: open}
		pod := coordtest.Memory(t)
		var turns atomic.Int32
		r := refusedRun(t, 2, &turns) // the first turn and the one after the first try
		app := newSessionsApp(t)
		// The allowance says an hour at first. When credits are added it says nothing is
		// spent, so the run is tried; the model API refuses it again, and the allowance now
		// says it is spent for four hours.
		hour, later := time.Now().Add(time.Hour).Unix(), time.Now().Add(4*time.Hour).Unix()
		var cleared atomic.Bool
		authorities(t, func() int64 {
			switch {
			case turns.Load() >= 2:
				return later
			case cleared.Load():
				return 0
			}
			return hour
		}, nil)
		c := pod("cloud-0")
		l := s.replica(func(string) *coord.Coord { return c }, "cloud-0", periods{})
		keeping(t, l)
		t.Cleanup(flights.watch(c))

		acc := parkedRun(t, r, app, theTask())
		first, _, _ := l.parked(t.Context(), "hanzo", acc.SessionID)

		cleared.Store(true)
		funds.Tell(c, funds.Event{Org: "hanzo", Kind: funds.Credit})
		waitFor(t, "the run to be refused again", func() bool { return turns.Load() == 2 })
		waitFor(t, "the run to park again", func() bool {
			p, ok, _ := l.parked(t.Context(), "hanzo", acc.SessionID)
			return ok && p.attempt != first.attempt && p.at == later
		})
		letGo(t, flights, acc.SessionID)
		x := app.get("hanzo", acc.SessionID)
		if !x.Parked() || x.ParkAt != later || app.parked() != 2 {
			t.Fatalf("the session reads %+v after %v, want it parked again at %d", x, app.parks, later)
		}
		// The timer stays set for the earlier time: it only ever moves sooner. When it falls
		// due it finds the run's row says later, tries nothing, and is set for that.
		if got := armed(flights); got != first.at {
			t.Fatalf("the wake is set for %d, want the earlier park's %d", got, first.at)
		}
		if turns.Load() != 2 || app.unparked() != 1 {
			t.Fatalf("%d turns and %d launches, want one try", turns.Load(), app.unparked())
		}

	})
}

// A run refused every time it is tried, by an authority that names no time, parks with no
// time and is tried once per credit event: three events, three launches, and no more. The
// timer is never set.
//
// MUTATION: set the timer to now when no time is named and the run is tried in a loop.
func TestAParkWithoutATimeSetsNoTimerAndIsTriedOncePerEvent(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		s := &depot{t: t, open: open}
		pod := coordtest.Memory(t)
		var turns atomic.Int32
		r := refusedRun(t, 1000, &turns)
		app := newSessionsApp(t)
		authorities(t, resetsAt(0), nil)
		c := pod("cloud-0")
		l := s.replica(func(string) *coord.Coord { return c }, "cloud-0", periods{})
		keeping(t, l)
		t.Cleanup(flights.watch(c))

		acc := parkedRun(t, r, app, theTask())
		if armed(flights) != 0 {
			t.Fatalf("a park with no time set the wake for %d", armed(flights))
		}
		for i := 1; i <= 3; i++ {
			funds.Tell(c, funds.Event{Org: "hanzo", Kind: funds.Credit})
			waitFor(t, "the run to be tried and parked again", func() bool {
				return int(turns.Load()) == i+1 && app.parked() == i+1
			})
			letGo(t, flights, acc.SessionID)
		}
		// Left alone it is not tried: no timer, no ticker.
		time.Sleep(300 * time.Millisecond)
		if turns.Load() != 4 || armed(flights) != 0 {
			t.Fatalf("a run refused each time ran %d turns and set the wake for %d, want 4 and none", turns.Load(), armed(flights))
		}
		x := app.get("hanzo", acc.SessionID)
		if !x.Parked() || x.ParkAt != 0 || x.Park != agents.ParkLimit {
			t.Fatalf("the session reads %+v, want parked for the limit with no time", x)
		}
		var said []string
		for _, p := range eventsOf(r, kindStatus) {
			var e struct{ Status, Message string }
			_ = json.Unmarshal([]byte(p), &e)
			if e.Status == statusParked {
				said = append(said, e.Message)
			}
		}
		if want := agents.ParkSaid(agents.ParkLimit, 0); said[len(said)-1] != want {
			t.Fatalf("the last parked event says %q, want %q", said[len(said)-1], want)
		}
	})
}

// A park holds the run as it was asked, and nothing a credential is made of: the task, the
// model, the repository, the thread; never a key, a token or the delegated credential.
func TestAParkHoldsNoCredential(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		s := &depot{t: t, open: open}
		pod := coordtest.Memory(t)
		var turns atomic.Int32
		r := refusedRun(t, 1, &turns)
		app := newSessionsApp(t)
		authorities(t, resetsAt(0), nil)
		l := s.replica(pod, "cloud-0", periods{})
		keeping(t, l)

		in := theTask()
		in.Model, in.Effort = "enso-pro", "high"
		acc := parkedRun(t, r, app, in)
		var spec string
		if err := s.db.QueryRow(`SELECT spec FROM parks WHERE session = $1`, acc.SessionID).Scan(&spec); err != nil {
			t.Fatal(err)
		}
		for _, secret := range []string{ghToken, "BEGIN OPENSSH", "HANZO_GIT_TOKEN", theMachine} {
			if strings.Contains(spec, secret) {
				t.Fatalf("the park holds %q: %s", secret, spec)
			}
		}
		var got spec0
		if err := json.Unmarshal([]byte(spec), &got); err != nil || got.In.Model != "enso-pro" || got.In.Effort != "high" || got.In.Repo != "github.com/hanzo-apps/site" {
			t.Fatalf("the park holds %s (%v), want the run as it was asked", spec, err)
		}
	})
}

type spec0 = struct {
	In client.CodingStartIn `json:"in"`
}

// A steer said to a parked run is queued, never refused, and goes with the run when it goes
// on: in its task, once, in order, and marked delivered. Said again with the same request
// id it is the same instruction; it is not said to the run a second time by a second try.
//
// MUTATION: refuse a steer to a paused run (the old 409) and the person's word is lost;
// leave the inbox unread by the run that goes on and the word never arrives; mark nothing
// delivered and the next run to go on says it again.
func TestASteerSaidToAParkedRunRidesTheRelaunchOnce(t *testing.T) {
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

		say := func(text, id string) steered {
			t.Helper()
			st, err := flights.follow(t.Context(), "hanzo", "sub-zoe", client.CodingSteerIn{Subject: "sub-zoe", SessionID: acc.SessionID, Text: text, RequestID: id}, nil)
			if err != nil {
				t.Fatalf("a steer to a parked run: %v", err)
			}
			return st
		}
		if st := say("use the other endpoint", "s1"); !st.Queued {
			t.Fatal("a steer to a parked run was not queued")
		}
		say("and add a test", "s2")
		say("use the other endpoint", "s1") // the same word again
		if app.unparked() != 0 {
			t.Fatal("saying something launched the run")
		}

		funds.Tell(c, funds.Event{Org: "hanzo", Kind: funds.Credit})
		waitFor(t, "the run to finish", func() bool { return slices.Contains(statusesOf(r), "done") })
		letGo(t, flights, acc.SessionID)

		var fed string
		r.pod.mu.Lock()
		for i, argv := range r.pod.ran {
			if slices.Contains(argv, "--skip-git-repo-check") {
				fed = r.pod.fed[i] // the last turn: the run that went on
			}
		}
		r.pod.mu.Unlock()
		first, second := strings.Index(fed, "use the other endpoint"), strings.Index(fed, "and add a test")
		if first < 0 || second < first || strings.Count(fed, "use the other endpoint") != 1 || strings.Count(fed, "and add a test") != 1 {
			t.Fatalf("the run that went on was told %q, want each instruction once, in order", fed)
		}
		rows, err := l.inbox(t.Context(), "hanzo", acc.SessionID)
		if err != nil || len(rows) != 2 {
			t.Fatalf("the inbox holds %+v (%v)", rows, err)
		}
		for _, row := range rows {
			if row.state != steerDelivered {
				t.Fatalf("an instruction the run took reads %+v, want delivered", row)
			}
		}
	})
}

// A run that went on from a park and is refused before its harness begins (the pre-flight
// or the lease says no: a spent cap, no funds) parks again, for the reason it was refused,
// with the work it already kept; any other error from its runner is the run's failure.
//
// MUTATION: fail the run on a refused admission and an hour's work is a dead end after all.
func TestARunThatWentOnAndIsRefusedAdmissionParksAgain(t *testing.T) {
	for name, c := range map[string]struct {
		err    error
		reason string
		parks  bool
	}{
		"the pre-flight refuses":   {admission{unpayable{errors.New("coding: this org has spent its cap for the period; a sandbox cannot start until it rolls over")}}, agents.ParkPayment, true},
		"the lease is out of cash": {admission{zip.Errorf(http.StatusPaymentRequired, "top up to lease more")}, agents.ParkPayment, true},
		"the lease is limited":     {admission{zip.Errorf(http.StatusTooManyRequests, "the free lane is used")}, agents.ParkLimit, true},
		"the harness died":         {errors.New("coding: run: sandbox lost"), "", false},
	} {
		t.Run(name, func(t *testing.T) {
			authorities(t, resetsAt(0), nil)
			sess := &fakeSessions{id: "sess_abc123def456"}
			run := &fakeRunner{err: c.err}
			d, _ := dispatcherFor(sess, &fakePR{}, run, true)
			var held []Parked
			d.Park = func(_ context.Context, p Parked) error { held = append(held, p); return nil }
			req := baseReq()
			req.SessionID, req.Parked, req.Kept = "sess_abc123def456", true, Kept{Commit: newSHA, Diffstat: "1 file changed"}
			res := d.Run(t.Context(), req)
			if !c.parks {
				if res.Parked || len(held) != 0 || res.OK || len(sess.closes) != 1 || sess.closes[0].status != statusError {
					t.Fatalf("a failed runner left %+v / %v / %v, want the run failed", res, held, sess.closes)
				}
				return
			}
			if !res.Parked || res.OK || len(held) != 1 || held[0].Reason != c.reason || held[0].Kept.Commit != newSHA || len(sess.closes) != 0 {
				t.Fatalf("a refused admission left %+v / %v / closes %v, want it parked for %s with its kept work", res, held, sess.closes, c.reason)
			}
			if got := statusesOfSession(sess); !slices.Equal(got, []string{statusContinued, statusParked}) {
				t.Fatalf("the session was told %v, want that it continued and parked", got)
			}
		})
	}
}

// A run that went on and is refused again before it changed anything still has the work
// the park before it kept: the next park holds that commit, so the run that finally
// finishes is done with it.
//
// MUTATION: hold only what this run changed and a second park forgets the first's work, and
// the run that goes on after it is done with nothing.
func TestAParkThatChangedNothingKeepsTheEarlierWork(t *testing.T) {
	sess := &fakeSessions{id: "sess_abc123def456"}
	run := &fakeRunner{result: RunResult{Park: &Parking{Reason: agents.ParkLimit}}}
	d, _ := dispatcherFor(sess, &fakePR{}, run, true)
	var held []Parked
	d.Park = func(_ context.Context, p Parked) error { held = append(held, p); return nil }
	req := baseReq()
	req.SessionID, req.Parked, req.Kept = "sess_abc123def456", true, Kept{Commit: newSHA, Diffstat: "1 file changed", Subject: "fix the header"}
	res := d.Run(t.Context(), req)
	if !res.Parked || len(held) != 1 || held[0].Kept != req.Kept || !res.Changed {
		t.Fatalf("the second park holds %+v (%+v), want the first's work", held, res)
	}
	// A run that did change something holds its own.
	run.result = RunResult{Park: &Parking{Reason: agents.ParkLimit}, Changed: true, CommitSha: theSHA, Diffstat: "2 files changed"}
	held = nil
	d.Run(t.Context(), req)
	if len(held) != 1 || held[0].Kept.Commit != theSHA || held[0].Kept.Diffstat != "2 files changed" {
		t.Fatalf("a run that changed something holds %+v, want its own commit", held)
	}
}

func statusesOfSession(f *fakeSessions) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, e := range f.events {
		if e.kind != kindStatus {
			continue
		}
		var s struct{ Status string }
		_ = json.Unmarshal([]byte(e.payload), &s)
		out = append(out, s.Status)
	}
	return out
}

// A run that went on and adds nothing more is done with the work it kept on its branch:
// its pull request is opened for that work, once. A run that kept nothing, or was not a
// continuation, is done with nothing, as before.
//
// MUTATION: read the second run's empty diff as "no changes needed" and the hour's work
// never reaches a pull request.
func TestARunThatWentOnAndAddedNothingIsDoneWithItsKeptWork(t *testing.T) {
	for name, c := range map[string]struct {
		parked bool
		kept   Kept
		opens  int
	}{
		"it kept work":          {true, Kept{Commit: newSHA, Diffstat: "1 file changed", Subject: "fix the header"}, 1},
		"it kept nothing":       {true, Kept{}, 0},
		"it did not continue":   {false, Kept{Commit: newSHA}, 0},
		"it kept work, no pull": {true, Kept{Commit: newSHA}, 0},
	} {
		t.Run(name, func(t *testing.T) {
			sess := &fakeSessions{id: "sess_abc123def456"}
			tr := &fakePR{ref: PRRef{Identifier: "SITE-1"}}
			run := &fakeRunner{result: RunResult{OK: true, Changed: false, Answer: "Nothing more to do."}}
			d, _ := dispatcherFor(sess, tr, run, true)
			req := baseReq()
			req.SessionID, req.Parked, req.Kept = "sess_abc123def456", c.parked, c.kept
			req.Pull = c.opens > 0 || name == "it kept nothing" || name == "it did not continue"
			if name == "it kept work, no pull" {
				req.Pull = false
			}
			res := d.Run(t.Context(), req)
			if len(tr.inputs) != c.opens {
				t.Fatalf("%d pull requests opened, want %d (%+v)", len(tr.inputs), c.opens, res)
			}
			if c.opens == 1 && (!res.Changed || !res.OK || tr.inputs[0].Head != res.Branch) {
				t.Fatalf("the run reads %+v / %+v, want it done, with a pull request for its branch", res, tr.inputs)
			}
		})
	}
}
