package coding

// park_wake_test.go — how a parked run is woken: by the time its refusal named, by funds
// changing, by a person, and by another pod after the one that parked it is gone. The run
// itself is stood in for (launched), since what is asked here is who tries it, when, and
// how many times; park_test.go runs a run through it whole.

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/apps/agents"
	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/funds"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/coord"
	"github.com/hanzoai/cloud/sdk/coord/coordtest"
	"github.com/zap-proto/zip"
)

// launcher stands in for the start of the run that goes on, and counts who it started.
type launcher struct {
	mu       sync.Mutex
	sessions []string
	prompts  []string
	stamps   []string
	callers  []zip.Caller
	parks    []park
	err      error
	// gate, when set, holds each start until it is closed, so two tries overlap.
	gate chan struct{}
}

func (k *launcher) install(t *testing.T) {
	t.Helper()
	was := launched
	launched = func(ctx context.Context, _, _ string, in client.CodingStartIn, _ func(string, ...any), ts string, existing *agents.Session, parked *park) (Accepted, error) {
		k.mu.Lock()
		gate, err := k.gate, k.err
		k.callers = append(k.callers, zip.CallerOf(ctx))
		k.parks = append(k.parks, *parked)
		k.sessions = append(k.sessions, existing.ID)
		k.prompts = append(k.prompts, in.Prompt)
		k.stamps = append(k.stamps, ts)
		k.mu.Unlock()
		if gate != nil {
			<-gate
		}
		if err != nil {
			return Accepted{}, err
		}
		return Accepted{SessionID: existing.ID}, nil
	}
	t.Cleanup(func() { launched = was })
}

func (k *launcher) started() []string {
	k.mu.Lock()
	defer k.mu.Unlock()
	return slices.Clone(k.sessions)
}

func (k *launcher) fail(err error) {
	k.mu.Lock()
	k.err = err
	k.mu.Unlock()
}

// podWith is a pod: its flights over a ledger of its own, and its wake, watching the
// deployment's bus. The session store is the one the test shares.
func podWith(t *testing.T, s *depot, pod func(string) *coord.Coord, id string) (*fleet, *ledger, *coord.Coord) {
	t.Helper()
	c := pod(id)
	l := s.replica(func(string) *coord.Coord { return c }, id, periods{})
	fl := newFleet()
	fl.keep(l)
	fl.serve(c, nil)
	t.Cleanup(func() { fl.serve(nil, nil) })
	stop := fl.watch(c)
	t.Cleanup(stop)
	return fl, l, c
}

// parkedAs holds a run of person in org as parked for a limit at at (unix seconds; 0 for no
// time).
func parkedAs(t *testing.T, l *ledger, app *sessionsApp, org, person, session string, at int64) park {
	t.Helper()
	return parkedFor(t, l, app, org, person, session, agents.ParkLimit, at)
}

// parkedFor holds a run of person in org as parked for reason at at.
func parkedFor(t *testing.T, l *ledger, app *sessionsApp, org, person, session, reason string, at int64) park {
	t.Helper()
	app.run(org, person, session)
	p := park{org: org, session: session, subject: person, reason: reason, at: at,
		spec: spec{In: client.CodingStartIn{Repo: "api", Prompt: "add a health check", Reply: client.Reply{Provider: slack, Channel: "C1", Thread: "T-" + session}}, TS: "1700000000.000100",
			Kept: Kept{Commit: newSHA}}}
	if err := l.park(t.Context(), p); err != nil {
		t.Fatal(err)
	}
	if ok, err := parkSession(t.Context(), org, session, reason, at); err != nil || !ok {
		t.Fatalf("park the session: %v %v", ok, err)
	}
	row, ok, err := l.parked(t.Context(), org, session)
	if err != nil || !ok {
		t.Fatalf("the park row: %v %v", ok, err)
	}
	return row
}

func rowsOf(t *testing.T, l *ledger) int {
	t.Helper()
	all, err := l.parksOf(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	return len(all)
}

// Of any number of pods that fire one wake, one launches the run: a try is a request in
// the ledger named by the park, so the pods that lose wait for the one that has it and
// answer what it answered. The pods are asked at the same instant, and the start is slow.
//
// MUTATION: call relaunch without the ledger and every pod starts the run.
func TestTwoPodsFiringOneWakeLaunchTheRunOnce(t *testing.T) {
	eachSite(t, func(t *testing.T, s *depot, pod func(string) *coord.Coord) {
		app := newSessionsApp(t)
		k := &launcher{gate: make(chan struct{})}
		k.install(t)
		a, la, _ := podWith(t, s, pod, "cloud-0")
		b, _, _ := podWith(t, s, pod, "cloud-1")
		parkedAs(t, la, app, "acme", "u-acme", "sess_w1", time.Now().Unix()-5)
		time.AfterFunc(200*time.Millisecond, func() { close(k.gate) })

		var wg sync.WaitGroup
		for _, fl := range []*fleet{a, b} {
			wg.Add(1)
			go func() { defer wg.Done(); fl.fire() }()
		}
		wg.Wait()
		if got := k.started(); !slices.Equal(got, []string{"sess_w1"}) {
			t.Fatalf("the run was launched %v by two pods firing one wake, want once", got)
		}
		if rowsOf(t, la) != 0 {
			t.Fatal("the park is still held after the run went on")
		}

		// The same park tried again, on either pod, goes on with nothing: it was tried.
		parkedAs(t, la, app, "acme", "u-acme", "sess_w2", 0)
		row, _, _ := la.parked(t.Context(), "acme", "sess_w2")
		var wg2 sync.WaitGroup
		for _, fl := range []*fleet{a, b, a, b} {
			wg2.Add(1)
			go func() { defer wg2.Done(); _ = fl.proceed(t.Context(), row, byWake) }()
		}
		wg2.Wait()
		if got := k.started(); !slices.Equal(got, []string{"sess_w1", "sess_w2"}) {
			t.Fatalf("four tries of one park launched %v, want it once", got)
		}
	})
}

// A wake is made by the pod that leads it, and by no other: the pods that do not lead
// when one falls due ask again once after the time a dead lead's lease takes to pass, and
// find nothing due when it has been made. A lead that goes away without making it leaves it
// to the next pod that leads.
//
// MUTATION: fire on every pod (no lead) and the ledger alone keeps it to one launch, at the
// price of every pod reading every park; never ask again and a lead lost at the wrong
// instant loses the wake.
func TestOnlyTheLeadMakesAWakeAndAnotherMakesItWhenTheLeadIsGone(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		s := &depot{t: t, open: open}
		pods := coordtest.Replicas(t, 2)
		app := newSessionsApp(t)
		k := &launcher{}
		k.install(t)
		a, la, ca := podWith(t, s, pods, "cloud-0")
		b, _, cb := podWith(t, s, pods, "cloud-1")
		parkedAs(t, la, app, "acme", "u-acme", "sess_l1", time.Now().Unix()-5)

		// One of them leads. The other, finding the wake due while the lead is making it,
		// does not launch, and is set to ask again after the time a dead lead's lease takes
		// to pass.
		k.gate = make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(1)
		go func() { defer wg.Done(); a.fire() }()
		waitFor(t, "the lead to be making the wake", func() bool { return len(k.started()) == 1 })
		b.fire()
		close(k.gate)
		wg.Wait()
		if got := k.started(); !slices.Equal(got, []string{"sess_l1"}) {
			t.Fatalf("the wake launched %v, want the lead's single launch", got)
		}
		led, other, otherCoord := a, b, cb
		if armed(a) != 0 {
			t.Fatalf("the pod that made the wake is set to ask again for %d", armed(a))
		}
		if got, want := armed(other), time.Now().Add(otherCoord.Handover()).Unix(); got < want-3 || got > want+3 {
			t.Fatalf("the pod that did not lead is set for %d, want about %d (a lease's handover from now)", got, want)
		}
		// Asked again, with the wake made, it finds nothing due and sets nothing.
		other.fire()
		if armed(other) != 0 {
			t.Fatalf("a pod with nothing due is set for %d", armed(other))
		}

		// The lead goes away with the next wake not made: the other pod makes it when it leads.
		parkedAs(t, la, app, "acme", "u-acme", "sess_l2", time.Now().Unix()-5)
		leader := ca
		_ = led
		other.fire() // not the lead: it waits
		if got := k.started(); len(got) != 1 {
			t.Fatalf("a pod that does not lead launched %v", got)
		}
		leader.Close(t.Context())
		waitFor(t, "the other pod to make the wake once it leads", func() bool {
			other.fire()
			return len(k.started()) == 2
		})
		if got := k.started(); !slices.Equal(got, []string{"sess_l1", "sess_l2"}) {
			t.Fatalf("launched %v", got)
		}
	})
}

// A park on one pod sets the timer of every other, over the bus, with no read of the rows;
// a pod that starts later, or after the one that parked it is gone, reads the rows once and
// sets its own. A park with no time sets none. The earliest wins and a later one does not
// move it.
//
// MUTATION: do not tell the bus and the other pods learn it when they restart; read no rows
// at start and a run parked before a restart is never woken.
func TestAParkOnOnePodSetsTheWakeOfTheOthers(t *testing.T) {
	eachSite(t, func(t *testing.T, s *depot, pod func(string) *coord.Coord) {
		app := newSessionsApp(t)
		a, la, _ := podWith(t, s, pod, "cloud-0")
		b, _, _ := podWith(t, s, pod, "cloud-1")
		soon, later := time.Now().Add(time.Hour).Unix(), time.Now().Add(5*time.Hour).Unix()

		app.run("acme", "u-acme", "sess_t1")
		h := holding{org: "acme", session: "sess_t1", subject: "u-acme", in: client.CodingStartIn{Repo: "api", Prompt: "p"}}
		if err := a.parkRun(t.Context(), h, Parked{Reason: agents.ParkLimit, At: later}); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "the other pod to hear when the run is tried", func() bool { return armed(b) == later })
		if armed(a) != later {
			t.Fatalf("the pod that parked it is set for %d, want %d", armed(a), later)
		}
		app.run("acme", "u-acme", "sess_t2")
		h.session = "sess_t2"
		if err := a.parkRun(t.Context(), h, Parked{Reason: agents.ParkLimit, At: soon}); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "the earlier time to win", func() bool { return armed(b) == soon && armed(a) == soon })
		app.run("acme", "u-acme", "sess_t3")
		h.session = "sess_t3"
		if err := a.parkRun(t.Context(), h, Parked{Reason: agents.ParkLimit, At: later + 60}); err != nil {
			t.Fatal(err)
		}
		time.Sleep(50 * time.Millisecond)
		if armed(a) != soon || armed(b) != soon {
			t.Fatalf("a later park moved the wake: %d, %d", armed(a), armed(b))
		}

		// A pod that comes up afterwards reads the rows, once, and is set for the earliest.
		c, _, _ := podWith(t, s, pod, "cloud-2")
		if armed(c) != soon {
			t.Fatalf("a pod that started after the park is set for %d, want %d", armed(c), soon)
		}
		_ = la
	})
}

// A crash between the park and the run going on loses nothing: the park is a row and the
// session's update, so a pod that comes up later (or any other) wakes the run at the time,
// from the row, and no pod that parked it need be alive.
//
// MUTATION: keep the wake in the memory of the pod that parked the run and it is lost with it.
func TestAParkedRunIsWokenFromItsRowAfterThePodThatParkedItIsGone(t *testing.T) {
	eachSite(t, func(t *testing.T, s *depot, pod func(string) *coord.Coord) {
		app := newSessionsApp(t)
		k := &launcher{}
		k.install(t)
		// The pod that parks it has no wake of its own to speak of: it parks and is gone.
		c0 := pod("cloud-0")
		l0 := s.replica(func(string) *coord.Coord { return c0 }, "cloud-0", periods{})
		dead := newFleet()
		dead.keep(l0)
		app.run("acme", "u-acme", "sess_c1")
		at := time.Now().Add(1500 * time.Millisecond).Unix()
		h := holding{org: "acme", session: "sess_c1", subject: "u-acme", in: client.CodingStartIn{Repo: "api", Prompt: "p"}}
		if err := dead.parkRun(t.Context(), h, Parked{Reason: agents.ParkLimit, At: at}); err != nil {
			t.Fatal(err)
		}
		dead.alarm().mu.Lock()
		if dead.alarm().timer != nil {
			dead.alarm().timer.Stop()
		}
		dead.alarm().mu.Unlock()
		c0.Close(t.Context())
		if got := k.started(); len(got) != 0 {
			t.Fatalf("launched %v before the time", got)
		}

		// Another pod comes up: it reads the row and wakes the run by itself.
		podWith(t, s, pod, "cloud-1")
		waitFor(t, "the run to be woken from its row", func() bool { return len(k.started()) == 1 })
		if time.Now().Unix() < at {
			t.Fatalf("the run was woken before %d", at)
		}
		if got := k.started(); !slices.Equal(got, []string{"sess_c1"}) {
			t.Fatalf("launched %v", got)
		}
	})
}

// Funds changing try the parked runs of the org they are about, and only those: the other
// orgs' stay parked. An event about no org (a cap on a plan) tries them all. A run tried is
// not tried again by the same event told twice, or by the same event on two pods.
//
// MUTATION: try every org's runs on any event and the other org's goes on with credits it
// was not given; try none and the run waits for a time that may be tomorrow.
func TestFundsChangingTryTheirOrgsParkedRunsAndOnlyThose(t *testing.T) {
	eachSite(t, func(t *testing.T, s *depot, pod func(string) *coord.Coord) {
		app := newSessionsApp(t)
		k := &launcher{}
		k.install(t)
		_, la, ca := podWith(t, s, pod, "cloud-0")
		_, _, cb := podWith(t, s, pod, "cloud-1")
		parkedAs(t, la, app, "acme", "u-acme", "sess_f1", 0)
		parkedAs(t, la, app, "acme", "u-acme", "sess_f2", time.Now().Add(time.Hour).Unix())
		parkedAs(t, la, app, "globex", "u-globex", "sess_f3", 0)

		// Told on one pod, heard by both: the org's runs go on once each.
		funds.Tell(ca, funds.Event{Org: "acme", Kind: funds.Credit})
		waitFor(t, "acme's runs to be tried", func() bool { return len(k.started()) == 2 })
		time.Sleep(150 * time.Millisecond)
		got := k.started()
		slices.Sort(got)
		if !slices.Equal(got, []string{"sess_f1", "sess_f2"}) {
			t.Fatalf("a credit for acme tried %v, want acme's two runs and not globex's", got)
		}
		funds.Tell(cb, funds.Event{Org: "acme", Kind: funds.Plan})
		time.Sleep(150 * time.Millisecond)
		if len(k.started()) != 2 {
			t.Fatalf("an event told again tried %v", k.started())
		}

		// An event that names nobody tries nobody.
		funds.Tell(cb, funds.Event{Kind: funds.Cap})
		time.Sleep(150 * time.Millisecond)
		if len(k.started()) != 2 {
			t.Fatalf("an event that names nobody tried %v", k.started())
		}
	})
}

// Continue is a word to a parked run: it tries the run now, once, however many times it is
// pressed and whether or not the wake falls due with it. Stop, said any time, cancels the
// wake: the run is not tried at its time, nor by funds, and a park left behind a stopped
// session is removed by the first try that finds the session stopped.
//
// MUTATION: Continue that does not take the try's request and the wake launches a second run;
// stop that leaves the row and the timer and the stopped run goes on.
func TestContinueTriesAParkedRunOnceAndStopCancelsItsWake(t *testing.T) {
	eachSite(t, func(t *testing.T, s *depot, pod func(string) *coord.Coord) {
		app := newSessionsApp(t)
		k := &launcher{gate: make(chan struct{})}
		k.install(t)
		a, la, _ := podWith(t, s, pod, "cloud-0")
		b, _, _ := podWith(t, s, pod, "cloud-1")
		parkedAs(t, la, app, "acme", "u-acme", "sess_k1", time.Now().Unix()-5)
		time.AfterFunc(200*time.Millisecond, func() { close(k.gate) })

		// Continue, pressed twice on two pods, with the wake falling due beside it.
		in := client.CodingSteerIn{Subject: "u-acme", SessionID: "sess_k1"}
		var wg sync.WaitGroup
		for _, fl := range []*fleet{a, b} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if _, err := fl.follow(t.Context(), "acme", "u-acme", in, nil); err != nil {
					t.Errorf("Continue on a parked run: %v", err)
				}
			}()
		}
		wg.Add(1)
		go func() { defer wg.Done(); a.fire() }()
		wg.Wait()
		if got := k.started(); !slices.Equal(got, []string{"sess_k1"}) {
			t.Fatalf("Continue and the wake launched %v, want one launch", got)
		}

		// Another person's Continue finds nothing of theirs there.
		parkedAs(t, la, app, "acme", "u-acme", "sess_k2", 0)
		_, err := b.follow(t.Context(), "acme", "u-bob", client.CodingSteerIn{Subject: "u-bob", SessionID: "sess_k2"}, nil)
		if he, ok := err.(*zip.HTTPError); !ok || he.Status != http.StatusNotFound {
			t.Fatalf("another person's Continue answered %v, want 404", err)
		}

		// Stop: the park goes, the wake finds nothing, funds find nothing.
		app.run("acme", "u-acme", "sess_k3")
		at := time.Now().Add(1100 * time.Millisecond).Unix()
		if err := a.parkRun(t.Context(), holding{org: "acme", session: "sess_k3", subject: "u-acme", in: client.CodingStartIn{Repo: "api", Prompt: "p"}},
			Parked{Reason: agents.ParkLimit, At: at}); err != nil {
			t.Fatal(err)
		}
		if armed(a) != at {
			t.Fatalf("the wake is set for %d, want %d", armed(a), at)
		}
		app.stop("acme", "sess_k3")
		a.released("acme", "sess_k3") // what the sessions app tells when it stops a session
		if _, ok, _ := la.parked(t.Context(), "acme", "sess_k3"); ok {
			t.Fatal("the park of a stopped run is still held")
		}
		waitFor(t, "the wake to fall due and find nothing", func() bool { return time.Now().Unix() > at && armed(a) == 0 })
		time.Sleep(100 * time.Millisecond)
		if slices.Contains(k.started(), "sess_k3") {
			t.Fatalf("a stopped run was launched: %v", k.started())
		}

		// A park left behind a stopped session (the stop could not tell) is removed by the try.
		left := parkedAs(t, la, app, "acme", "u-acme", "sess_k4", 0)
		app.stop("acme", "sess_k4")
		before := len(k.started())
		if err := a.proceed(t.Context(), left, byContinue); err != nil {
			t.Fatalf("a try of a stopped run answered %v, want it dropped quietly", err)
		}
		if len(k.started()) != before {
			t.Fatalf("a stopped run was launched: %v", k.started())
		}
		if _, ok, _ := la.parked(t.Context(), "acme", "sess_k4"); ok {
			t.Fatal("the park of a stopped session is still held after a try")
		}
	})
}

// A start that cannot be made (the repository's host is down, GitHub will not admit the
// run now) leaves the run parked, saying why, and tried again after a delay that doubles
// with each failure, counted on the row: the attempt is finished, so the next is another
// request in the ledger. A change of funds does not hurry it (it waits on a start, not on
// funds); a person pressing Continue does.
//
// MUTATION: leave the park's time where it was and the wake fires at once, in a loop; end the
// run failed and a host that was down for a minute costs the person their hour; set no time
// and a run that could not start is never tried again by itself.
func TestAStartThatCannotBeMadeLeavesTheRunParkedAndTriedAgainAfterADelay(t *testing.T) {
	eachSite(t, func(t *testing.T, s *depot, pod func(string) *coord.Coord) {
		app := newSessionsApp(t)
		k := &launcher{}
		k.install(t)
		k.fail(zip.Errorf(http.StatusServiceUnavailable, "the forge is unavailable"))
		a, la, ca := podWith(t, s, pod, "cloud-0")
		first := parkedAs(t, la, app, "acme", "u-acme", "sess_r1", time.Now().Unix()-5)

		before := time.Now()
		a.fire()
		if got := k.started(); len(got) != 1 {
			t.Fatalf("launched %v", got)
		}
		row, ok, _ := la.parked(t.Context(), "acme", "sess_r1")
		next := before.Add(retryAfter(1)).Unix()
		if !ok || row.at < next || row.at > next+2 || row.tries != 1 || row.attempt == first.attempt || row.reason != agents.ParkLimit {
			t.Fatalf("the park reads %+v (%v), want the same reason, one failure counted, another attempt and a try a delay away (%d)", row, ok, next)
		}
		if x := app.get("acme", "sess_r1"); !x.Parked() || x.ParkAt != row.at {
			t.Fatalf("the session reads %+v, want it still parked, for %d", x, row.at)
		}
		if got := armed(a); got != row.at {
			t.Fatalf("the wake is set for %d, want the retry's %d", got, row.at)
		}
		time.Sleep(200 * time.Millisecond)
		if len(k.started()) != 1 {
			t.Fatalf("a run that could not start was tried again before its delay: %v", k.started())
		}

		// The host is back. Credits are added, but the run is waiting out the delay of a start
		// that failed, and a change of funds is not what it waits on; a person who presses
		// Continue goes on at once.
		k.fail(nil)
		funds.Tell(ca, funds.Event{Org: "acme", Kind: funds.Credit})
		time.Sleep(150 * time.Millisecond)
		if len(k.started()) != 1 {
			t.Fatalf("a change of funds tried a run that is waiting out a failed start: %v", k.started())
		}
		if err := a.proceed(t.Context(), row, byContinue); err != nil {
			t.Fatal(err)
		}
		if len(k.started()) != 2 {
			t.Fatalf("Continue did not go on: %v", k.started())
		}
	})
}

// A word to a parked run: an instruction is queued, never refused, whichever pod takes it,
// found by its session or by the thread it narrates in; and one too long for any task is
// refused where it is said, not acked and left to rot.
func TestAnInstructionForAParkedRunIsQueued(t *testing.T) {
	eachSite(t, func(t *testing.T, s *depot, pod func(string) *coord.Coord) {
		app := newSessionsApp(t)
		a, la, _ := podWith(t, s, pod, "cloud-0")
		b, _, _ := podWith(t, s, pod, "cloud-1")
		parkedAs(t, la, app, "acme", "u-acme", "sess_i1", 0)
		ctx := t.Context()

		st, err := b.follow(ctx, "acme", "u-acme", client.CodingSteerIn{Subject: "u-acme", SessionID: "sess_i1", Text: "use the other endpoint", RequestID: "r1"}, nil)
		if err != nil || !st.Queued {
			t.Fatalf("an instruction for a parked run answered (%+v, %v), want it queued", st, err)
		}
		// By its thread, the way a message in a chat thread names its run, and replayed.
		st, err = a.follow(ctx, "acme", "u-acme", client.CodingSteerIn{Subject: "u-acme", Text: "and add a test",
			Reply: client.Reply{Provider: slack, Channel: "C1", Thread: "T-sess_i1"}}, nil)
		if err != nil || !st.Queued {
			t.Fatalf("an instruction by thread answered (%+v, %v), want it queued", st, err)
		}
		if _, err := a.follow(ctx, "acme", "u-acme", client.CodingSteerIn{Subject: "u-acme", SessionID: "sess_i1", Text: "use the other endpoint", RequestID: "r1"}, nil); err != nil {
			t.Fatal(err)
		}
		rows, err := la.queued(ctx, "acme", "sess_i1")
		if err != nil || len(rows) != 2 || rows[0].body != "use the other endpoint" || rows[1].body != "and add a test" {
			t.Fatalf("the inbox holds %+v (%v), want the two instructions, in order, the replay once", rows, err)
		}
		// Nothing was launched by saying it: it waits for the run to go on.
		if x := app.get("acme", "sess_i1"); !x.Parked() {
			t.Fatalf("an instruction unparked the run: %+v", x)
		}
		// A word with no session and no thread names no run.
		if _, err := a.follow(ctx, "acme", "u-acme", client.CodingSteerIn{Subject: "u-acme", Text: "hello"}, nil); err == nil {
			t.Fatal("an instruction that names no run was kept for a parked one")
		}
		// One that fits no task is refused.
		_, err = a.follow(ctx, "acme", "u-acme", client.CodingSteerIn{Subject: "u-acme", SessionID: "sess_i1", Text: strings.Repeat("x", maxPromptLen)}, nil)
		if he, ok := err.(*zip.HTTPError); !ok || he.Status != http.StatusBadRequest {
			t.Fatalf("an instruction too long for a task answered %v, want 400", err)
		}
	})
}

// The authorities say when a limit clears: the allowance app's reset for the person and the
// free pool's refill, the later holding. Nothing is made up: a refusal by the payer, an
// authority that cannot be asked, one that says the window is open or its instant is past,
// names no time.
func TestWhatClearsARefusalIsTheAuthoritysTime(t *testing.T) {
	soon, later := time.Now().Add(time.Hour).Unix(), time.Now().Add(3*time.Hour).Unix()
	ctx := zip.WithCaller(t.Context(), zip.Caller{Org: "hanzo", User: "sub-zoe", Name: "zoe"})
	for name, c := range map[string]struct {
		reason string
		allow  *client.Allowance
		aerr   error
		pool   *client.Pool
		perr   error
		want   int64
	}{
		"the person's window reopens":    {agents.ParkLimit, &client.Allowance{Spent: true, Resets: soon}, nil, &client.Pool{State: "available"}, nil, soon},
		"the pool refills later":         {agents.ParkLimit, &client.Allowance{Spent: true, Resets: soon}, nil, &client.Pool{State: "exhausted", Resets: later}, nil, later},
		"only the pool ran dry":          {agents.ParkLimit, &client.Allowance{}, nil, &client.Pool{State: "busy", Resets: soon}, nil, soon},
		"a window that is open":          {agents.ParkLimit, &client.Allowance{Spent: false, Resets: soon}, nil, &client.Pool{State: "available"}, nil, 0},
		"an instant already past":        {agents.ParkLimit, &client.Allowance{Spent: true, Resets: time.Now().Unix() - 10}, nil, &client.Pool{State: "available"}, nil, 0},
		"neither can be asked":           {agents.ParkLimit, nil, zip.ErrNotFound("allowance"), nil, zip.ErrNotFound("pool"), 0},
		"a payer's refusal names none":   {agents.ParkPayment, &client.Allowance{Spent: true, Resets: soon}, nil, &client.Pool{State: "exhausted", Resets: later}, nil, 0},
		"the pool alone, the person not": {agents.ParkLimit, nil, zip.ErrNotFound("allowance"), &client.Pool{State: "exhausted", Resets: later}, nil, later},
	} {
		wasAllowed, wasPooled := allowed, pooled
		allowed = func(context.Context, *client.AllowanceIn) (*client.Allowance, error) { return c.allow, c.aerr }
		pooled = func(context.Context, *client.PoolIn) (*client.Pool, error) { return c.pool, c.perr }
		var got int64
		if at := clearsAt(ctx, c.reason); !at.IsZero() {
			got = at.Unix()
		}
		allowed, pooled = wasAllowed, wasPooled
		if got != c.want {
			t.Errorf("%s: clearsAt = %d, want %d", name, got, c.want)
		}
	}
}

// Nothing here polls. A pod with no parked run has no timer; a park with no time sets none;
// a wake with nothing due sets none; and the files that make the wake and tell funds
// contain no ticker: the only timers are armed for a time a row names, or a lease's handover.
func TestNothingPollsAParkedRun(t *testing.T) {
	eachSite(t, func(t *testing.T, s *depot, pod func(string) *coord.Coord) {
		app := newSessionsApp(t)
		a, la, _ := podWith(t, s, pod, "cloud-0")
		if armed(a) != 0 {
			t.Fatalf("a pod with nothing parked is set for %d", armed(a))
		}
		parkedAs(t, la, app, "acme", "u-acme", "sess_n1", 0)
		a.rescan()
		a.fire()
		if armed(a) != 0 || armed(a) != 0 {
			t.Fatalf("a park with no time set a timer for %d", armed(a))
		}
	})
	for _, file := range []string{"park.go", "park_wake.go", "../../internal/funds/funds.go"} {
		src, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(src, func(n ast.Node) bool {
			if sel, ok := n.(*ast.SelectorExpr); ok {
				if x, ok := sel.X.(*ast.Ident); ok && x.Name == "time" && (sel.Sel.Name == "NewTicker" || sel.Sel.Name == "Tick") {
					t.Errorf("%s uses time.%s: a parked run is woken by a time or an event, never a ticker", file, sel.Sel.Name)
				}
			}
			return true
		})
	}
}

// A run that goes on and is refused at once parks again before the try that launched it
// has returned. The new park is another attempt and survives the try's cleanup, which
// removes the park it tried and no other: a session parked with no row is never woken.
//
// MUTATION: remove the row by session alone and the run's second park is deleted by the
// try that launched it.
func TestAParkMadeWhileTheTryReturnsSurvivesIt(t *testing.T) {
	eachSite(t, func(t *testing.T, s *depot, pod func(string) *coord.Coord) {
		app := newSessionsApp(t)
		a, la, _ := podWith(t, s, pod, "cloud-0")
		first := parkedAs(t, la, app, "acme", "u-acme", "sess_q1", 0)
		later := time.Now().Add(time.Hour).Unix()
		was := launched
		launched = func(ctx context.Context, org, _ string, in client.CodingStartIn, _ func(string, ...any), _ string, existing *agents.Session, _ *park) (Accepted, error) {
			// The run that is launched is refused at once and parks again, as a run does from
			// its own goroutine, while the start has yet to return.
			if ok, err := unparkSession(ctx, org, existing.ID); err != nil || !ok {
				t.Errorf("unpark: %v %v", ok, err)
			}
			err := a.parkRun(ctx, holding{org: org, session: existing.ID, subject: "u-acme", in: in}, Parked{Reason: agents.ParkLimit, At: later})
			if err != nil {
				t.Errorf("park again: %v", err)
			}
			return Accepted{SessionID: existing.ID}, nil
		}
		t.Cleanup(func() { launched = was })

		if err := a.proceed(t.Context(), first, byWake); err != nil {
			t.Fatal(err)
		}
		row, ok, err := la.parked(t.Context(), "acme", "sess_q1")
		if err != nil || !ok || row.at != later || row.attempt <= first.attempt {
			t.Fatalf("the park reads %+v (%v, %v), want the run's second park, at %d, after the first", row, ok, err, later)
		}
		if x := app.get("acme", "sess_q1"); !x.Parked() || x.ParkAt != later {
			t.Fatalf("the session reads %+v", x)
		}
	})
}

// The run that parked, if this pod flew it, is let go before the run goes on: two flights
// of one session are one hold for a stop to reach, and the earlier one's release would take
// the later one's with it.
//
// MUTATION: launch without waiting and the run that goes on holds no stop.
func TestAParkedRunIsNotGoneOnUntilItsFlightHasLetGo(t *testing.T) {
	eachSite(t, func(t *testing.T, s *depot, pod func(string) *coord.Coord) {
		app := newSessionsApp(t)
		k := &launcher{}
		k.install(t)
		a, la, _ := podWith(t, s, pod, "cloud-0")
		row := parkedAs(t, la, app, "acme", "u-acme", "sess_h1", 0)
		f, _ := flyOn(t, a, "sess_h1", "u-acme", client.Reply{})

		done := make(chan error, 1)
		go func() { done <- a.proceed(t.Context(), row, byWake) }()
		time.Sleep(150 * time.Millisecond)
		if got := k.started(); len(got) != 0 {
			t.Fatalf("the run went on while the flight that parked it was still held: %v", got)
		}
		f.forget()
		close(f.done)
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if got := k.started(); !slices.Equal(got, []string{"sess_h1"}) {
			t.Fatalf("launched %v", got)
		}
	})
}

// Continue pressed on a message goes on in that message; more time asked of a parked run
// is a conflict that says to continue it, not a run that is not there.
//
// MUTATION: ignore the pressed message and the run narrates in the card it parked on; leave
// Extend as it was and a parked run reads as no run.
func TestContinueGoesOnInThePressedMessageAndMoreTimeIsAConflict(t *testing.T) {
	eachSite(t, func(t *testing.T, s *depot, pod func(string) *coord.Coord) {
		app := newSessionsApp(t)
		k := &launcher{}
		k.install(t)
		a, la, _ := podWith(t, s, pod, "cloud-0")
		parkedAs(t, la, app, "acme", "u-acme", "sess_e1", 0)

		_, err := a.extend(t.Context(), "acme", "u-acme", &client.CodingExtendIn{Session: "sess_e1", Seconds: 60})
		if he, ok := err.(*zip.HTTPError); !ok || he.Status != http.StatusConflict || !strings.Contains(he.Msg, "parked") {
			t.Fatalf("more time for a parked run answered %v, want a conflict saying it is parked", err)
		}
		_, err = a.extend(t.Context(), "acme", "u-bob", &client.CodingExtendIn{Session: "sess_e1", Seconds: 60})
		if he, ok := err.(*zip.HTTPError); !ok || he.Status != http.StatusNotFound {
			t.Fatalf("more time asked by another person answered %v, want 404", err)
		}

		if _, err := a.follow(t.Context(), "acme", "u-acme", client.CodingSteerIn{Subject: "u-acme", SessionID: "sess_e1", Update: "1700000999.000200"}, nil); err != nil {
			t.Fatal(err)
		}
		if len(k.stamps) != 1 || k.stamps[0] != "1700000999.000200" {
			t.Fatalf("the run went on in message %v, want the one Continue was pressed on", k.stamps)
		}
	})
}
