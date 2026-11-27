package coding

// park_authority_test.go — a run goes on days after the request that parked it ended, so
// nothing of that request is trusted: IAM is asked whether its person may still act, the
// authority that refused it is asked whether it still does, the payer is resolved afresh and
// each try is audited; a try that was refused again costs nothing; a park older than a
// session may live is a person's to continue; and a try that cannot be made is made again,
// by someone, after a delay. Everything runs on both engines and both buses.

import (
	"context"
	"errors"
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
	"github.com/hanzoai/cloud/sdk/coord"
	"github.com/hanzoai/cloud/sdk/coord/coordtest"
	"github.com/zap-proto/zip"
)

// A person IAM no longer admits to the org (removed, disabled, deleted) does not have their
// run gone on with days after they asked for it, whoever asks: the clock, a change of funds, a
// press of Continue. The run is ended, "access removed", its park goes, nothing is started, and
// the end is on the audit trail as a denial.
//
// MUTATION: leave out the question to IAM and the run goes on as the person who is gone, with
// their wallet and their branch.
func TestAParkedRunOfAPersonIAMNoLongerAdmitsIsEndedAndNotGoneOn(t *testing.T) {
	eachSite(t, func(t *testing.T, s *depot, pod func(string) *coord.Coord) {
		app := newSessionsApp(t)
		k := &launcher{}
		k.install(t)
		a, la, _ := podWith(t, s, pod, "cloud-0")
		for _, by := range []string{byWake, byFunds, byContinue} {
			row := parkedAs(t, la, app, "acme", "u-"+by, "sess_"+by, time.Now().Unix()-5)
			app.revoke("acme", "u-"+by)
			err := a.proceed(t.Context(), row, by)
			var done resolved
			if !errors.As(err, &done) || !strings.Contains(err.Error(), "access removed") {
				t.Fatalf("%s: proceed answered %v, want the run ended, access removed", by, err)
			}
		}
		if got := k.started(); len(got) != 0 {
			t.Fatalf("runs of people who may not act were started: %v", got)
		}
		if rowsOf(t, la) != 0 {
			t.Fatal("the parks of runs that were ended are still held")
		}
		ends := app.ended()
		for _, by := range []string{byWake, byFunds, byContinue} {
			if !slices.Contains(ends, "sess_"+by+": "+accessRemoved) {
				t.Errorf("the platform ended %v, want sess_%s ended, access removed", ends, by)
			}
			if x := app.get("acme", "sess_"+by); x.Status != agents.StatusStopped || x.Parked() {
				t.Errorf("the session reads %+v", x)
			}
		}
		recs := s.trail.records()
		if len(recs) != 3 {
			t.Fatalf("the audit trail holds %d records, want one denial per run: %+v", len(recs), recs)
		}
		for _, r := range recs {
			if r.Action != "coding.park.revoke" || r.Outcome.Result != "deny" || r.Outcome.Status != 403 || r.Actor.Org != "acme" {
				t.Errorf("record %+v, want a denial of the person's run", r)
			}
		}
	})
}

// IAM that cannot say whether the person may act is not a no: the run is neither started nor
// ended. It stays parked, its attempt as it was (it was never made), one failure counted, and
// it is tried again after the delay.
//
// MUTATION: end the run when IAM errors and a blip in IAM kills every parked run; go on when
// IAM errors and a person who was removed goes on while IAM is down.
func TestAParkedRunWhoseIAMCannotBeAskedIsKeptAndTriedAgain(t *testing.T) {
	eachSite(t, func(t *testing.T, s *depot, pod func(string) *coord.Coord) {
		app := newSessionsApp(t)
		k := &launcher{}
		k.install(t)
		was := mayAct
		mayAct = func(context.Context, string, string) (bool, error) { return false, errors.New("iam: unavailable") }
		t.Cleanup(func() { mayAct = was })
		a, la, _ := podWith(t, s, pod, "cloud-0")
		first := parkedAs(t, la, app, "acme", "u-acme", "sess_i1", time.Now().Unix()-5)

		before := time.Now()
		a.fire()
		row, ok, _ := la.parked(t.Context(), "acme", "sess_i1")
		next := before.Add(retryAfter(1)).Unix()
		if !ok || row.tries != 1 || row.attempt != first.attempt || row.at < next || row.at > next+2 {
			t.Fatalf("the park reads %+v (%v), want one failure counted, the attempt as it was and a try %d", row, ok, next)
		}
		if got := armed(a); got != row.at {
			t.Fatalf("the wake is set for %d, want %d", got, row.at)
		}
		if len(k.started()) != 0 || len(app.ended()) != 0 {
			t.Fatalf("started %v, ended %v: IAM said nothing", k.started(), app.ended())
		}
		if x := app.get("acme", "sess_i1"); !x.Parked() {
			t.Fatalf("the session reads %+v, want it still parked", x)
		}
	})
}

// The run that goes on is paid as IAM and the ledger say now, not as the request that parked
// it said days ago: the billing claim that request carried is not kept, and the lease of the run
// that goes on states a caller with no claim, whose payer is resolved as a credential that
// carries none is.
//
// MUTATION: keep the claim in the park and hand it back to the run that goes on, and a person
// whose plan or pool has changed since is billed to the account they were on.
func TestAParkedRunIsPaidAsTheLedgerSaysAndNotByAClaimOfAnEndedRequest(t *testing.T) {
	eachSite(t, func(t *testing.T, s *depot, pod func(string) *coord.Coord) {
		var turns atomic.Int32
		r := refusedRun(t, 1, &turns)
		app := newSessionsApp(t)
		var credited atomic.Bool
		authorities(t, func() int64 {
			if credited.Load() {
				return 0
			}
			return time.Now().Add(time.Hour).Unix()
		}, nil)
		c := pod("cloud-0")
		l := s.replica(func(string) *coord.Coord { return c }, "cloud-0", periods{})
		keeping(t, l)
		t.Cleanup(flights.watch(c))

		app.run("hanzo", "sub-zoe", "sess_abc123def456")
		ctx := zip.WithCaller(context.Background(), zip.Caller{Org: "hanzo", User: "sub-zoe", Name: "zoe", Account: "org:hanzo"})
		acc, err := r.startAs(t, ctx, "sub-zoe", theTask())
		if err != nil {
			t.Fatal(err)
		}
		var kept string
		if err := s.db.QueryRow(`SELECT spec FROM parks WHERE session = $1`, acc.SessionID).Scan(&kept); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(kept, "org:hanzo") || strings.Contains(strings.ToLower(kept), "account") {
			t.Fatalf("the park holds the billing claim of the request that parked it: %s", kept)
		}

		credited.Store(true)
		funds.Tell(c, funds.Event{Org: "hanzo", Kind: funds.Credit})
		waitFor(t, "the run to finish", func() bool { return slices.Contains(statusesOf(r), "done") })
		letGo(t, flights, acc.SessionID)
		r.pod.mu.Lock()
		lessees := slices.Clone(r.pod.lessees)
		r.pod.mu.Unlock()
		if len(lessees) != 2 {
			t.Fatalf("%d leases, want the run's and the one that went on", len(lessees))
		}
		if lessees[0].Account != "org:hanzo" {
			t.Fatalf("the first lease was asked as %+v, want the claim of its request", lessees[0])
		}
		if lessees[1].Account != "" || lessees[1].Name != "zoe" || lessees[1].User != "sub-zoe" || lessees[1].Org != "hanzo" {
			t.Fatalf("the lease of the run that went on was asked as %+v, want the person, with no claim", lessees[1])
		}
	})
}

// Each try of a parked run is on the audit trail before it is made (its intent), as the person it
// is the run of, with what made it and the attempt, and how it ended after (its result); and a
// process whose trail will not take the intent makes no try at all.
//
// MUTATION: leave the record out and nothing says a run went on as a person days after they
// asked; write it after the start and a trail that is down lets the run go.
func TestEachTryOfAParkedRunIsAuditedBeforeItIsMade(t *testing.T) {
	eachSite(t, func(t *testing.T, s *depot, pod func(string) *coord.Coord) {
		app := newSessionsApp(t)
		k := &launcher{}
		k.install(t)
		a, la, _ := podWith(t, s, pod, "cloud-0")
		rows := map[string]park{}
		for _, by := range []string{byWake, byFunds, byContinue} {
			rows[by] = parkedAs(t, la, app, "acme", "u-"+by, "sess_"+by, time.Now().Unix()-5)
		}
		for _, by := range []string{byWake, byFunds, byContinue} {
			if err := a.proceed(t.Context(), rows[by], by); err != nil {
				t.Fatalf("%s: %v", by, err)
			}
		}
		recs := s.trail.records()
		if len(recs) != 6 {
			t.Fatalf("the trail holds %d records, want an intent and a result for each of the three tries: %+v", len(recs), recs)
		}
		for i, by := range []string{byWake, byFunds, byContinue} {
			for j, result := range []string{"intent", "success"} {
				r := recs[2*i+j]
				if r.Action != "coding.park.resume" || r.Actor.Org != "acme" || r.Actor.Sub != "u-"+by || r.Resource.Type != "agent-session" || r.Resource.ID != "sess_"+by ||
					r.Outcome.Result != result || !strings.Contains(string(r.After), `"trigger":"`+by+`"`) || !strings.Contains(string(r.After), `"reason":"limit"`) ||
					!strings.Contains(string(r.After), `"attempt":`+strconv.FormatInt(rows[by].attempt, 10)) {
					t.Errorf("record %d = %+v (%s), want the %s of the %s try of sess_%s as u-%s, naming attempt %d", 2*i+j, r, r.After, result, by, by, by, rows[by].attempt)
				}
			}
		}

		// A trail that is down: no try.
		s.trail.refuse(errors.New("the trail is down"))
		blocked := parkedAs(t, la, app, "acme", "u-down", "sess_down", time.Now().Unix()-5)
		if err := a.proceed(t.Context(), blocked, byContinue); err == nil {
			t.Fatal("a try went on with an audit trail that would not take it")
		}
		if got := k.started(); slices.Contains(got, "sess_down") {
			t.Fatalf("started %v with the trail down", got)
		}
		s.trail.refuse(nil)
		if err := a.proceed(t.Context(), blocked, byContinue); err != nil {
			t.Fatal(err)
		}
		if !slices.Contains(k.started(), "sess_down") {
			t.Fatalf("started %v once the trail was back", k.started())
		}
	})
}

// A process that keeps no audit trail does not go on with a parked run.
//
// MUTATION: skip the check for a missing trail and a deployment without one goes on unrecorded.
func TestAProcessWithNoAuditTrailGoesOnWithNothing(t *testing.T) {
	eachSite(t, func(t *testing.T, s *depot, pod func(string) *coord.Coord) {
		app := newSessionsApp(t)
		k := &launcher{}
		k.install(t)
		a, la, _ := podWith(t, s, pod, "cloud-0")
		la.trail = nil
		row := parkedAs(t, la, app, "acme", "u-acme", "sess_n1", time.Now().Unix()-5)
		if err := a.proceed(t.Context(), row, byContinue); !errors.Is(err, errNoTrail) {
			t.Fatalf("proceed answered %v, want %v", err, errNoTrail)
		}
		if len(k.started()) != 0 {
			t.Fatalf("started %v", k.started())
		}
	})
}

// A try the clock or funds make asks the authority that refused the run first. Still refused,
// it costs one read: no sandbox is leased, no turn is spent, the run is not unparked, and the
// park waits for the time the authority names. Cleared, it goes on.
//
// MUTATION: leave out the question and the run is unparked, a sandbox leased, a clone made and
// a turn spent to be refused again, once per event, per run.
func TestAStillRefusedTryBootsNoSandbox(t *testing.T) {
	eachSite(t, func(t *testing.T, s *depot, pod func(string) *coord.Coord) {
		var turns atomic.Int32
		r := refusedRun(t, 1000, &turns)
		app := newSessionsApp(t)
		resets := time.Now().Add(2 * time.Hour).Unix()
		var spent atomic.Bool
		spent.Store(true)
		authorities(t, func() int64 {
			if spent.Load() {
				return resets
			}
			return 0
		}, nil)
		c := pod("cloud-0")
		l := s.replica(func(string) *coord.Coord { return c }, "cloud-0", periods{})
		keeping(t, l)
		t.Cleanup(flights.watch(c))

		acc := parkedRun(t, r, app, theTask())
		first, _, _ := l.parked(t.Context(), "hanzo", acc.SessionID)
		leasesBefore, turnsBefore := leases(r.pod), turns.Load()

		// A cap changes in the org. It does not clear what refused this run.
		funds.Tell(c, funds.Event{Org: "hanzo", Kind: funds.Cap})
		waitFor(t, "the attempt to finish", func() bool {
			p, ok, _ := l.parked(t.Context(), "hanzo", acc.SessionID)
			return ok && p.attempt != first.attempt
		})
		p, _, _ := l.parked(t.Context(), "hanzo", acc.SessionID)
		if got := leases(r.pod) - leasesBefore; got != 0 || turns.Load() != turnsBefore || app.unparked() != 0 {
			t.Fatalf("a refusal that stands leased %d sandboxes, ran %d turns and unparked %d times, want nothing", got, turns.Load()-turnsBefore, app.unparked())
		}
		if p.at != resets || p.tries != 0 || !app.get("hanzo", acc.SessionID).Parked() {
			t.Fatalf("the park reads %+v, want it kept for the authority's time %d, no failure counted", p, resets)
		}

		// The authority lets it go: the next event goes on.
		spent.Store(false)
		funds.Tell(c, funds.Event{Org: "hanzo", Kind: funds.Credit})
		waitFor(t, "the run to go on", func() bool { return app.unparked() == 1 })
		letGo(t, flights, acc.SessionID)
	})
}

// A refusal by the payer is asked of the prepaid gate: no funds, the run waits (with no time,
// since credits name none); funds, it goes on.
//
// MUTATION: skip the gate for a payment park and every credit event that does not fund the payer
// boots a sandbox for a run that is refused again.
func TestAPaymentParkWaitsForTheGateToSayThePayerCanPay(t *testing.T) {
	eachSite(t, func(t *testing.T, s *depot, pod func(string) *coord.Coord) {
		app := newSessionsApp(t)
		k := &launcher{}
		k.install(t)
		var broke atomic.Bool
		broke.Store(true)
		payable = func(_ context.Context, org string, in *client.AuthorizeIn) (*client.Verdict, error) {
			if org != "acme" || in.Subject != "acme" {
				t.Errorf("the gate was asked about %q for %q, want the org's pool", in.Subject, org)
			}
			if broke.Load() {
				return &client.Verdict{NoFunds: true}, nil
			}
			return &client.Verdict{OK: true}, nil
		}
		_, la, ca := podWith(t, s, pod, "cloud-0")
		first := parkedFor(t, la, app, "acme", "u-acme", "sess_p1", agents.ParkPayment, 0)

		funds.Tell(ca, funds.Event{Org: "acme", Kind: funds.Credit})
		waitFor(t, "the attempt to finish", func() bool {
			p, _, _ := la.parked(t.Context(), "acme", "sess_p1")
			return p.attempt != first.attempt
		})
		if len(k.started()) != 0 {
			t.Fatalf("a payer with no funds had the run started: %v", k.started())
		}
		broke.Store(false)
		funds.Tell(ca, funds.Event{Org: "acme", Kind: funds.Credit})
		waitFor(t, "the run to go on", func() bool { return len(k.started()) == 1 })
	})
}

// An authority that cannot be read has not said the refusal cleared: a try the clock or funds
// make then goes nowhere, and is made again after a delay, one failure counted.
//
// MUTATION: treat an unreadable authority as cleared and an outage of the allowance app boots a
// sandbox for every parked run.
func TestAnAuthorityThatCannotBeReadIsNotTakenToHaveCleared(t *testing.T) {
	eachSite(t, func(t *testing.T, s *depot, pod func(string) *coord.Coord) {
		app := newSessionsApp(t)
		k := &launcher{}
		k.install(t)
		allowed = func(context.Context, *client.AllowanceIn) (*client.Allowance, error) {
			return nil, errors.New("allowance: unavailable")
		}
		a, la, _ := podWith(t, s, pod, "cloud-0")
		first := parkedAs(t, la, app, "acme", "u-acme", "sess_u1", time.Now().Unix()-5)
		a.fire()
		row, _, _ := la.parked(t.Context(), "acme", "sess_u1")
		if len(k.started()) != 0 || row.tries != 1 || row.attempt == first.attempt || row.at <= time.Now().Unix() {
			t.Fatalf("started %v; the park reads %+v, want nothing started, one failure counted, another attempt, a later try", k.started(), row)
		}
	})
}

// An event that names no org and no plan tries nobody, however it reaches the listener.
//
// MUTATION: take an unnamed event for every org's and one SuperAdmin edit of any plan tries every
// parked run in the deployment.
func TestAnEventThatNamesNobodyTriesNobody(t *testing.T) {
	eachSite(t, func(t *testing.T, s *depot, pod func(string) *coord.Coord) {
		app := newSessionsApp(t)
		k := &launcher{}
		k.install(t)
		fl, la, _ := podWith(t, s, pod, "cloud-0")
		parkedAs(t, la, app, "acme", "u-acme", "sess_e1", 0)
		fl.funded(funds.Event{Kind: funds.Credit})
		fl.funded(funds.Event{Kind: funds.Cap})
		time.Sleep(150 * time.Millisecond)
		if len(k.started()) != 0 {
			t.Fatalf("an event that names nobody tried %v", k.started())
		}
		fl.funded(funds.Event{Org: "acme", Kind: funds.Credit})
		waitFor(t, "the named org's run to be tried", func() bool { return len(k.started()) == 1 })
	})
}

// A cap on a plan names the plan, and the runs tried are those of the orgs on it.
//
// MUTATION: try every org's runs for a plan's cap and the orgs on other plans are tried too.
func TestAPlanCapTriesTheOrgsOnThatPlanAndNoOther(t *testing.T) {
	eachSite(t, func(t *testing.T, s *depot, pod func(string) *coord.Coord) {
		app := newSessionsApp(t)
		k := &launcher{}
		k.install(t)
		onPlan = func(_ context.Context, org, _ string) (string, error) {
			return map[string]string{"acme": "go", "globex": "pro", "initech": ""}[org], nil
		}
		_, la, ca := podWith(t, s, pod, "cloud-0")
		parkedAs(t, la, app, "acme", "u-acme", "sess_l1", 0)
		parkedAs(t, la, app, "globex", "u-globex", "sess_l2", 0)
		parkedAs(t, la, app, "initech", "u-initech", "sess_l3", 0)
		funds.Tell(ca, funds.Event{Plan: "go", Kind: funds.Cap})
		waitFor(t, "the plan's run to be tried", func() bool { return len(k.started()) >= 1 })
		time.Sleep(150 * time.Millisecond)
		if got := k.started(); !slices.Equal(got, []string{"sess_l1"}) {
			t.Fatalf("a cap on the go plan tried %v, want acme's run alone", got)
		}
	})
}

// Tries for one org (or plan) are made under one lock, so two events for it do not try its runs
// at once; another org's are not held up.
//
// MUTATION: return an unlocked release and two events for one org try its runs together.
func TestTriesForOneOrgAreMadeOneAtATime(t *testing.T) {
	fl := newFleet()
	release := fl.serial("acme\x00")
	entered := make(chan struct{})
	go func() {
		again := fl.serial("acme\x00")
		close(entered)
		again()
	}()
	other := fl.serial("globex\x00")
	other()
	select {
	case <-entered:
		t.Fatal("a second try for the org began while the first was in hand")
	case <-time.After(100 * time.Millisecond):
	}
	release()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the second try never began once the first was done")
	}
}

// A park older than a session may live is not gone on with by itself, by the clock or by funds,
// and the wake is not armed for it; a person pressing Continue goes on.
//
// MUTATION: leave out the age and a run parked a week ago goes on, as a person whose
// circumstances have changed, from a night's refusal.
func TestAParkOlderThanASessionMayLiveGoesOnOnlyWhenAPersonSaysSo(t *testing.T) {
	eachSite(t, func(t *testing.T, s *depot, pod func(string) *coord.Coord) {
		app := newSessionsApp(t)
		k := &launcher{}
		k.install(t)
		a, la, _ := podWith(t, s, pod, "cloud-0")
		parkedAs(t, la, app, "acme", "u-acme", "sess_o1", time.Now().Unix()-5)
		old := time.Now().Add(-agents.MaxLife() - time.Hour).UnixMilli()
		if _, err := la.db.ExecContext(t.Context(), `UPDATE parks SET created = $1`, old); err != nil {
			t.Fatal(err)
		}
		row, _, _ := la.parked(t.Context(), "acme", "sess_o1")
		if !row.stale(time.Now()) {
			t.Fatalf("a park made %d is not stale", row.created)
		}

		// The clock's two reads pass it over: it is not due, and nothing is armed for it.
		if due, err := la.due(t.Context(), time.Now()); err != nil || len(due) != 0 {
			t.Fatalf("an aged park is due: %+v (%v)", due, err)
		}
		if at, err := la.soonest(t.Context(), 0, time.Now()); err != nil || at != 0 {
			t.Fatalf("the earliest time to wake at is %d (%v) with only an aged park", at, err)
		}
		a.fire()
		a.rescan()
		a.funded(funds.Event{Org: "acme", Kind: funds.Credit})
		if err := a.proceed(t.Context(), row, byWake); err != nil { // a try already in hand when the age passed
			t.Fatal(err)
		}
		time.Sleep(150 * time.Millisecond)
		if len(k.started()) != 0 || armed(a) != 0 {
			t.Fatalf("an aged park was gone on with (%v) or armed for (%d)", k.started(), armed(a))
		}
		if err := a.proceed(t.Context(), row, byContinue); err != nil {
			t.Fatal(err)
		}
		if got := k.started(); !slices.Equal(got, []string{"sess_o1"}) {
			t.Fatalf("Continue started %v", got)
		}

		// One a minute inside the age is the clock's.
		young := parkedAs(t, la, app, "acme", "u-young", "sess_o2", time.Now().Unix()-5)
		if _, err := la.db.ExecContext(t.Context(), `UPDATE parks SET created = $1 WHERE session = 'sess_o2'`, time.Now().Add(-agents.MaxLife()+time.Minute).UnixMilli()); err != nil {
			t.Fatal(err)
		}
		young, _, _ = func() (park, bool, error) { return la.parked(t.Context(), "acme", "sess_o2") }()
		if young.stale(time.Now()) {
			t.Fatalf("a park a minute inside the age is stale: %+v", young)
		}
		a.fire()
		if !slices.Contains(k.started(), "sess_o2") {
			t.Fatalf("a park inside the age was not gone on with: %v", k.started())
		}
	})
}

// A change of funds passes over a park that has waited longer than a run goes on by itself for,
// and one waiting out the delay of an attempt that failed; it tries a fresh one.
//
// MUTATION: leave out either and every event takes a claim and a read for each aged park there
// ever was, or hurries a start that failed a moment ago.
func TestAChangeOfFundsPassesOverAgedAndFailingParks(t *testing.T) {
	eachSite(t, func(t *testing.T, s *depot, pod func(string) *coord.Coord) {
		app := newSessionsApp(t)
		fl, la, _ := podWith(t, s, pod, "cloud-0")
		parkedAs(t, la, app, "acme", "u-aged", "sess_aged", 0)
		parkedAs(t, la, app, "acme", "u-failing", "sess_failing", 0)
		parkedAs(t, la, app, "acme", "u-fresh", "sess_fresh", 0)
		if _, err := la.db.ExecContext(t.Context(), `UPDATE parks SET created = $1 WHERE session = 'sess_aged'`, time.Now().Add(-agents.MaxLife()-time.Minute).UnixMilli()); err != nil {
			t.Fatal(err)
		}
		if _, err := la.db.ExecContext(t.Context(), `UPDATE parks SET tries = 1, resume_at = $1 WHERE session = 'sess_failing'`, time.Now().Add(time.Minute).Unix()); err != nil {
			t.Fatal(err)
		}
		all, _ := la.parksOf(t.Context(), "acme")
		var got []string
		for _, p := range fl.wanted(t.Context(), all, "", time.Now()) {
			got = append(got, p.session)
		}
		if !slices.Equal(got, []string{"sess_fresh"}) {
			t.Fatalf("a change of funds tries %v, want the fresh park alone", got)
		}
	})
}

// A time the authority names beyond the age a run goes on by itself for is not named: the park
// waits for credits or a person, and says so, rather than promising a time it will not keep.
//
// MUTATION: return the authority's time whatever it is and a park says it goes on in three days,
// when at that time it is a person's to continue.
func TestAWakeBeyondTheAgeIsNotNamed(t *testing.T) {
	ctx := zip.WithCaller(context.Background(), zip.Caller{Org: "acme", User: "u-acme"})
	newSessionsApp(t)
	soon := time.Now().Add(2 * time.Hour).Unix()
	authorities(t, resetsAt(soon), nil)
	if got := clearsAt(ctx, agents.ParkLimit); got.Unix() != soon {
		t.Fatalf("clearsAt = %v, want the authority's %d", got, soon)
	}
	authorities(t, resetsAt(time.Now().Add(agents.MaxLife()+time.Hour).Unix()), nil)
	if got := clearsAt(ctx, agents.ParkLimit); !got.IsZero() {
		t.Fatalf("clearsAt = %v, want none for a time past the age", got)
	}
}

// A due row whose try errored (IAM, the audit trail, the ledger, the sessions store could not
// answer) is tried again by the lead after a delay, counted, until it is made.
//
// MUTATION: arm only for times after the pass began and the row is due forever with nobody armed
// to try it again.
func TestADueRowWhoseTryErroredIsTriedAgain(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		s := &depot{t: t, open: open}
		pods := coordtest.Memory(t)
		app := newSessionsApp(t)
		k := &launcher{}
		k.install(t)
		wasRetry := retryAfter
		retryAfter = func(int64) time.Duration { return 50 * time.Millisecond }
		t.Cleanup(func() { retryAfter = wasRetry })
		var down atomic.Bool
		down.Store(true)
		real := visibleRun
		visibleRun = func(ctx context.Context, id string) (agents.Session, error) {
			if down.Load() {
				return agents.Session{}, errors.New("sessions store busy")
			}
			return real(ctx, id)
		}
		t.Cleanup(func() { visibleRun = real })
		a, la, _ := podWith(t, s, pods, "cloud-0")
		parkedAs(t, la, app, "acme", "u-acme", "sess_x1", time.Now().Unix()-5)

		a.fire() // the store is busy, so the try errors
		row, _, _ := la.parked(t.Context(), "acme", "sess_x1")
		if row.tries < 1 {
			t.Fatalf("the park reads %+v, want the failed try counted", row)
		}
		down.Store(false) // the store heals; the lead's own timer, set for the delay, tries the run again
		waitFor(t, "the lead to try the run again", func() bool { return len(k.started()) >= 1 })
		if got := k.started(); got[0] != "sess_x1" {
			t.Fatalf("started %v", got)
		}
		if rowsOf(t, la) != 0 {
			t.Fatal("the park is still held after the run went on")
		}
	})
}

// A pod that is not the lead while a run falls due asks once more, after the time a dead
// lead's lease would take to pass, and then no more: a lead that is alive keeps what it could
// not try for a later time, and the pod that finds the lead gone is the lead.
//
// MUTATION: re-arm the non-lead whenever a row is due and it asks without end.
func TestAPodThatIsNotTheLeadAsksOnceMoreAndNoMore(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		s := &depot{t: t, open: open}
		pod := coordtest.Replicas(t, 2)
		app := newSessionsApp(t)
		k := &launcher{gate: make(chan struct{})}
		k.install(t)
		a, la, _ := podWith(t, s, pod, "cloud-0")
		b, _, _ := podWith(t, s, pod, "cloud-1")
		release := sync.OnceFunc(func() { close(k.gate) })
		t.Cleanup(release) // a failed test must not leave the lead blocked in its start
		parkedAs(t, la, app, "acme", "u-acme", "sess_n1", time.Now().Unix()-5)
		parkedAs(t, la, app, "acme", "u-acme", "sess_n2", time.Now().Unix()-5)
		done := make(chan struct{})
		go func() { defer close(done); a.fire() }() // a leads, and is busy with the first
		waitFor(t, "the lead to be at its first try", func() bool { return len(k.started()) == 1 })

		b.fire()
		if armed(b) == 0 {
			t.Fatal("the pod that is not the lead did not arm to ask once more")
		}
		b.fire()
		if got := armed(b); got != 0 {
			t.Fatalf("the pod that is not the lead is armed for %d after asking twice", got)
		}
		if len(k.started()) != 1 {
			t.Fatalf("the pod that is not the lead started %v", k.started())
		}
		release()
		<-done
	})
}

// A park told on the bus while a pod is starting, between its listening and its reading of the
// rows, is not lost: the pod listens first, then reads.
//
// MUTATION: read the rows first and listen after, and a park told in between is heard by
// nobody and its run is not woken until the next restart.
func TestAParkToldWhileAPodIsStartingIsNotMissed(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		s := &depot{t: t, open: open}
		pods := coordtest.Replicas(t, 2)
		l := s.replica(pods, "cloud-1", periods{})
		fl := newFleet()
		fl.keep(l)
		other := pods("cloud-0")
		at := time.Now().Add(time.Hour).Unix()
		was := scanned
		scanned = func() {
			_ = other.Publish(wakeTopic, []byte(strconv.FormatInt(at, 10)))
			time.Sleep(150 * time.Millisecond) // long enough for the bus to have delivered it to whoever listens
		}
		t.Cleanup(func() { scanned = was })
		stop := fl.watch(pods("cloud-1"))
		t.Cleanup(stop)
		waitFor(t, "the pod to hear the park told while it started", func() bool { return armed(fl) == at })
	})
}

// A try that fails and is retried by every duplicate that waited on it, from the same stale
// snapshot, is one start and not three: the attempt finished, so the duplicates are answered
// that it failed and make no start of their own.
//
// MUTATION: release the claim of a failed start (the old behaviour) and each duplicate retakes
// it and starts the run.
func TestAFailedTryIsNotMadeAgainByItsDuplicates(t *testing.T) {
	eachSite(t, func(t *testing.T, s *depot, pod func(string) *coord.Coord) {
		app := newSessionsApp(t)
		k := &launcher{gate: make(chan struct{})}
		k.install(t)
		k.fail(errors.New("forge refused"))
		fl, l, _ := podWith(t, s, pod, "cloud-0")
		row := parkedAs(t, l, app, "acme", "u-acme", "sess_d1", 0)

		var wg sync.WaitGroup
		errs := make([]error, 3)
		for i := range errs {
			wg.Add(1)
			go func() { defer wg.Done(); errs[i] = fl.proceed(t.Context(), row, byContinue) }()
		}
		time.Sleep(150 * time.Millisecond)
		close(k.gate)
		wg.Wait()
		if got := k.started(); !slices.Equal(got, []string{"sess_d1"}) {
			t.Fatalf("three duplicates of one try made %d starts (%v)", len(got), got)
		}
		for i, err := range errs {
			var done resolved
			if !errors.As(err, &done) {
				t.Errorf("duplicate %d answered %v, want the failure the first got", i, err)
			}
		}
		after, _, _ := l.parked(t.Context(), "acme", "sess_d1")
		if after.attempt == row.attempt || after.tries != 1 {
			t.Fatalf("the park reads %+v, want another attempt and one failure counted", after)
		}
		// The next attempt is a new request: it is made.
		k.fail(nil)
		if err := fl.proceed(t.Context(), after, byContinue); err != nil {
			t.Fatal(err)
		}
		if len(k.started()) != 2 {
			t.Fatalf("the next attempt was not made: %v", k.started())
		}
	})
}
