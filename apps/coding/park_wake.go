package coding

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud/apps/agents"
	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/funds"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/hanzoai/cloud/sdk/coord"
	"github.com/hanzoai/cloud/sdk/meter"
	"github.com/hanzoai/cloud/sdk/readiness"
	"github.com/zap-proto/zip"
)

var launched func(ctx context.Context, org, subject string, in client.CodingStartIn, log func(msg string, kv ...any), ts string, existing *agents.Session, parked *park) (Accepted, error)

func init() { launched = startSession }

// errStale is a try for a run that is no longer parked: stopped, or tried by another.
var errStale = errors.New("coding: that run is no longer parked")

// errNoTrail is a process that keeps no audit trail: a run that goes on as a person days
// after they asked for it, or holds a credential that renews as them, is never unrecorded.
var errNoTrail = errors.New("coding: this process keeps no audit trail, so it cannot act for a person unrecorded")

// accessRemoved is what a run is ended with when its person may no longer act in its org.
const accessRemoved = "access removed: the person who started this run can no longer act in this organization"

// Trail is the audit trail a try is written to before it is made (audit.Recorder).
type Trail interface {
	Append(ctx context.Context, rec audit.Record) (audit.Record, error)
}

// proceed makes one attempt at a parked run, by what: of any number of pods asked at once,
// in any number of restarts, one launches the run, and the rest answer what it answered.
// The attempt is the request, so an attempt that finished is never made again, however it
// ended; the park names the next. An error that is a resolved is an attempt that ended
// with the reason on the park; any other is an attempt that could not be made.
func (fl *fleet) proceed(ctx context.Context, p park, by string) error {
	p.trigger = by
	made := false
	out, err := requested(ctx, fl.ledger(), p.org, p.subject, scoped("park", p.session), strconv.FormatInt(p.attempt, 10),
		printOf(struct {
			Session string
			Attempt int64
		}{p.session, p.attempt}),
		func(ctx context.Context) (attempted, error) { made = true; return fl.relaunch(ctx, p) })
	switch {
	case errors.Is(err, errStale):
		return nil
	case err != nil:
		return err
	case out.Failed != "":
		status := http.StatusConflict
		if out.Denied {
			status = http.StatusForbidden
		}
		return resolved{errors.New(out.Failed), status}
	case !made && out.SessionID != "":
		// Answered from the ledger that the run was launched. If the run is still parked, the
		// answer is a dead end that every wake, funds event and Continue would be given for as
		// long as the claim stands. The claim of a relaunch settles after relaunch returns (the
		// start is given a context with no claim), so this is not a window the code opens: it is
		// the guard for a settled outcome whose park survived by another road. The attempt did
		// not finish; the next one is another request.
		if next, ok := fl.stranded(ctx, p); ok {
			return fl.proceed(ctx, next, by)
		}
	}
	return nil
}

// stranded reports a park whose attempt the ledger says launched the run while the run is
// still parked, and gives the park the next attempt, read back.
func (fl *fleet) stranded(ctx context.Context, p park) (park, bool) {
	l := fl.ledger()
	if x, err := visibleRun(fl.person(p), p.session); err != nil || !x.Parked() {
		return park{}, false
	}
	if ok, err := l.retime(ctx, p, p.at, p.tries, true); err != nil || !ok {
		return park{}, false
	}
	next, ok, err := l.parked(ctx, p.org, p.session)
	if err != nil || !ok || next.attempt == p.attempt {
		return park{}, false
	}
	fl.say("coding: a parked run was answered launched and is still parked; it is tried again", "session", p.session)
	return next, true
}

// person is the context a parked run is tried as: the person, stated as a follow-up states
// them, with nothing of the request that parked it.
func (fl *fleet) person(p park) context.Context {
	return zip.WithCaller(cloud.For(context.Background(), p.org), zip.Caller{Org: p.org, User: p.subject, Name: p.spec.Name})
}

// relaunch goes on with a parked run in the claim proceed holds. Nothing of the request
// that parked it is left to trust, days later: the person is asked of IAM, the authority that
// refused the run is asked whether it still does, the payer is resolved as a credential with
// no billing claim is, and the try is audited before it is made.
func (fl *fleet) relaunch(ctx context.Context, p park) (attempted, error) {
	l := fl.ledger()
	_, log := fl.coord()
	as := fl.person(p)
	x, err := visibleRun(as, p.session)
	var he *zip.HTTPError
	switch {
	case errors.As(err, &he) && he.Status == http.StatusNotFound:
		_, _ = l.drop(ctx, p.org, p.session, p.attempt)
		return attempted{}, errStale
	case err != nil:
		return attempted{}, err // not read: the park stays, for the next event
	case !x.Parked():
		_, _ = l.drop(ctx, p.org, p.session, p.attempt)
		return attempted{}, errStale
	case p.trigger != byContinue && p.stale(time.Now()):
		return attempted{}, errStale // waited longer than a run goes on by itself for: a person's to say
	}
	ok, err := mayAct(as, p.org, p.subject)
	switch {
	case err != nil:
		return attempted{}, fmt.Errorf("coding: IAM could not say whether the run's person may act in %s: %w", p.org, err)
	case !ok:
		return fl.revoked(ctx, p)
	}
	if p.trigger != byContinue {
		switch s := standsFor(as, p.reason); {
		case s.holds:
			return fl.waits(ctx, p, s), nil
		case s.unread:
			return fl.remain(ctx, p, errors.New("an authority that refused the run could not be asked whether it still does")), nil
		}
	}
	// The intent is on the trail before the try is made, and its result after: a start that fails
	// is not on the trail as a success.
	if err := fl.audit(ctx, p, "coding.park.resume", audit.Outcome{Result: "intent"}); err != nil {
		return attempted{}, err
	}
	if p.trigger == byContinue {
		// A person's Continue begins a chain of its own: the day a run goes on by itself for
		// starts again from here.
		if err := l.unchain(ctx, p.org, p.session); err != nil {
			return attempted{}, err
		}
	}
	// The flight that parked the run, if this pod flew it, lets go before the run goes on:
	// two flights of one session are one hold for a stop to reach.
	fl.Lock()
	old := fl.m[p.session]
	fl.Unlock()
	if old != nil {
		select {
		case <-old.done:
		case <-time.After(followWait):
			fl.audited(ctx, p, audit.Outcome{Result: "error", Status: http.StatusConflict, Reason: "the run that parked has not let go"})
			return attempted{}, errors.New("coding: the run that parked has not let go")
		case <-ctx.Done():
			return attempted{}, ctx.Err()
		}
	}
	in := p.spec.In
	// The earlier run's sandbox is gone and its branch is on the remote: this run carries
	// that branch on, in the session it was parked in. A chat turn that followed the run
	// ended when it parked, so the run narrates for itself.
	in.After, in.Branch, in.RequestID, in.Conversation = "", "", "", ""
	in.Reply.Turn = ""
	acc, err := launched(as, p.org, p.subject, in, log, p.spec.TS, &x, &p)
	switch {
	case errors.Is(err, errStale):
		fl.audited(ctx, p, audit.Outcome{Result: "error", Status: http.StatusConflict, Reason: "no longer parked"})
		_, _ = l.drop(ctx, p.org, p.session, p.attempt) // stopped, or tried by another, while this one started
		return attempted{}, errStale
	case err != nil:
		fl.audited(ctx, p, audit.Outcome{Result: "error", Status: http.StatusServiceUnavailable, Reason: firstLine(err.Error())})
		return fl.remain(ctx, p, err), nil
	}
	fl.audited(ctx, p, audit.Outcome{Result: "success", Status: http.StatusOK})
	_, _ = l.drop(ctx, p.org, p.session, p.attempt)
	return attempted{Accepted: acc}, nil
}

// revoked ends a parked run whose person may no longer act in its org: the session is
// stopped, "access removed", and the park goes with it. The act is on the audit trail, but
// the run is ended whether or not the trail takes it. A session that could not be ended
// leaves the park, to be tried again.
func (fl *fleet) revoked(ctx context.Context, p park) (attempted, error) {
	l := fl.ledger()
	ctx = context.WithoutCancel(ctx)
	if _, err := endSession(ctx, p.org, p.session, accessRemoved); err != nil {
		return attempted{}, fmt.Errorf("coding: the run of a person who may no longer act could not be ended: %w", err)
	}
	_, _ = l.drop(ctx, p.org, p.session, 0)
	_ = l.unchain(ctx, p.org, p.session)
	if err := fl.audit(ctx, p, "coding.park.revoke", audit.Outcome{Result: "deny", Status: http.StatusForbidden, Reason: "access removed"}); err != nil {
		fl.say("coding: the end of a parked run was not recorded on the audit trail", "session", p.session, "err", err)
	}
	return attempted{Failed: accessRemoved, Denied: true}, nil
}

// audit writes what is done to a parked run, as the person it is the run of, to the audit
// trail. A process with no trail refuses: the trail may be a nil recorder behind a non-nil
// interface (an audit trail disabled for the deployment), which is no trail.
func (fl *fleet) audit(ctx context.Context, p park, action string, out audit.Outcome) error {
	return fl.record(ctx, p.org, p.subject, p.session, "parked", action, out, struct {
		Trigger string `json:"trigger"`
		Attempt int64  `json:"attempt"`
		Reason  string `json:"reason"`
	}{p.trigger, p.attempt, p.reason})
}

// record writes what is done as a person in one of their runs, whichever way it came
// (method), to the audit trail; after is what it says of the act. A process with no trail
// refuses (errNoTrail).
func (fl *fleet) record(ctx context.Context, org, subject, session, method, action string, out audit.Outcome, after any) error {
	l := fl.ledger()
	if l == nil || unset(l.trail) {
		return errNoTrail
	}
	said, _ := json.Marshal(after)
	_, err := l.trail.Append(cloud.For(ctx, org), audit.Record{
		Actor:    audit.Actor{Org: org, Sub: subject},
		Action:   action,
		Resource: audit.Resource{Type: "agent-session", ID: session},
		Auth:     audit.AuthContext{Method: method},
		Outcome:  out,
		After:    said,
	})
	if err != nil {
		return fmt.Errorf("coding: the audit trail did not take the act: %w", err)
	}
	return nil
}

// audited writes how a try ended, after the intent. The try has been made and is answered
// whatever the trail says, so a trail that will not take it is logged.
func (fl *fleet) audited(ctx context.Context, p park, out audit.Outcome) {
	if err := fl.audit(context.WithoutCancel(ctx), p, "coding.park.resume", out); err != nil {
		fl.say("coding: how a parked run's try ended was not recorded on the audit trail", "session", p.session, "err", err)
	}
}

// unset reports a trail that is nil, or an interface holding a nil pointer.
func unset(t Trail) bool {
	if t == nil {
		return true
	}
	v := reflect.ValueOf(t)
	return v.Kind() == reflect.Pointer && v.IsNil()
}

// ---- the wake --------------------------------------------------------------

// alarm is the one timer of a pod.
type alarm struct {
	mu    sync.Mutex
	timer *time.Timer
	at    int64 // when it is armed for, unix seconds; 0 when it is not
	// again is that the timer is the second asking of a pod that was not the lead: it
	// asks once, and then not again.
	again bool
}

func (fl *fleet) alarm() *alarm {
	fl.Lock()
	defer fl.Unlock()
	if fl.bell == nil {
		fl.bell = &alarm{}
	}
	return fl.bell
}

// waking is the coord the wake tells and leads on: the ledger's, else the process's.
func (fl *fleet) waking() *coord.Coord {
	if l := fl.ledger(); l != nil {
		if c := l.coord(); c != nil {
			return c
		}
	}
	return coord.Default()
}

// arm sets the timer to at, if that is sooner than the time it is set to.
func (fl *fleet) arm(at int64) {
	if at <= 0 {
		return
	}
	w := fl.alarm()
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.timer != nil && w.at > 0 && w.at <= at {
		return
	}
	if w.timer != nil {
		w.timer.Stop()
	}
	w.at, w.again = at, false
	w.timer = time.AfterFunc(max(time.Until(time.Unix(at, 0)), 0), fl.fire)
}

// armAfter sets the timer to d from now whatever it is set to: the second asking of a pod
// that was not the lead.
func (fl *fleet) armAfter(d time.Duration) {
	w := fl.alarm()
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.timer != nil {
		w.timer.Stop()
	}
	w.at, w.again = time.Now().Add(d).Unix(), true
	w.timer = time.AfterFunc(d, fl.fire)
}

// rescan reads the earliest time any park is tried, whether past or to come, and arms for
// it: what a process does when it starts, and when the bus says it may have missed a park.
func (fl *fleet) rescan() {
	l := fl.ledger()
	if l == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), writeWait)
	defer cancel()
	at, err := l.soonest(ctx, 0, time.Now())
	if err != nil {
		fl.say("coding: could not read when the parked runs are tried", "err", err)
		return
	}
	fl.arm(at)
}

// attempts makes an attempt at each park in turn, by what, while hold says this process
// may. An attempt that could not be made is kept for later.
func (fl *fleet) attempts(ctx context.Context, ps []park, by string, hold func() error) {
	for _, p := range ps {
		if hold != nil && hold() != nil {
			return
		}
		err := fl.survive(func() error { return fl.proceed(ctx, p, by) })
		var done resolved
		switch {
		case err == nil:
		case errors.As(err, &done):
			fl.say("coding: a parked run was not gone on with", "session", p.session, "why", done.Error())
		default:
			fl.say("coding: a parked run was not tried", "session", p.session, "err", err)
			fl.later(ctx, p, err)
		}
	}
}

// survive runs one try and answers its error, or a panic as an error: a park that makes a try
// panic is a failed try, kept for later with its delay, and not a process that dies in the
// goroutine of a timer and dies again at every start, because the park is still there.
func (fl *fleet) survive(try func() error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("coding: a try of a parked run panicked: %v", r)
		}
	}()
	return try()
}

// fire is the timer: the parks that have fallen due are tried by the pod that leads the
// wake, and the timer is set to the next time any is. A pod that is not the lead while one
// is due asks again once, after the time a dead lead's lease takes to pass, and not again:
// a lead that is alive keeps what it could not try for a later time (later), and the pod
// that finds the lead gone is the lead.
func (fl *fleet) fire() {
	// A pod being replaced hands its parked runs to the next; it tries none.
	if readiness.Draining() {
		return
	}
	w := fl.alarm()
	w.mu.Lock()
	again := w.again
	w.timer, w.at, w.again = nil, 0, false
	w.mu.Unlock()
	l := fl.ledger()
	if l == nil {
		return
	}
	// The goroutine of a timer is nobody's to recover: a panic here would be the process's, at
	// every start, because the parks that cause it are still held. The wake is armed again after
	// a delay, as for a try that could not be made.
	defer func() {
		if r := recover(); r != nil {
			fl.say("coding: the wake panicked; it is armed again after a delay", "panic", r)
			fl.arm(time.Now().Add(retryAfter(1)).Unix())
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	started := time.Now()
	due, err := l.due(ctx, started)
	if err != nil {
		fl.say("coding: could not read the parked runs that are due", "err", err)
		return
	}
	if len(due) > 0 {
		c := fl.waking()
		lead := c.Lead(wakeLoop)
		err = lead.Do(ctx, func(ctx context.Context) {
			fl.attempts(ctx, due, byWake, func() error { return lead.Hold(ctx) })
		})
		if errors.Is(err, coord.ErrNotHeld) && !again {
			fl.armAfter(c.Handover())
			return
		}
	}
	at, err := l.soonest(ctx, started.Unix(), started)
	if err != nil {
		fl.say("coding: could not read when the parked runs are next tried", "err", err)
		return
	}
	fl.arm(at)
}

// watch makes this pod wake the runs parked on it, hear when another pod parks one, and
// try the runs of an org or a plan when its funds change. It listens first and reads the
// rows after, so a park told in between is not missed. The function it answers stops it.
func (fl *fleet) watch(c *coord.Coord) func() {
	if c == nil {
		c = coord.Default()
	}
	w := fl.alarm()
	offs := []func(){
		c.Subscribe(wakeTopic, func(_ string, payload []byte) {
			if at, err := strconv.ParseInt(string(payload), 10, 64); err == nil {
				fl.arm(at)
			}
		}, fl.rescan),
		funds.Hear(c, fl.funded, func() { fl.askPass("", "") }),
	}
	scanned()
	fl.rescan()
	return func() {
		for _, off := range offs {
			off()
		}
		w.mu.Lock()
		if w.timer != nil {
			w.timer.Stop()
			w.timer, w.at, w.again = nil, 0, false
		}
		w.mu.Unlock()
	}
}

// scanned is called between listening and reading the rows (watch), as a value so a test
// can tell a park there.
var scanned = func() {}

// funded tries the parked runs of the org or the plan an event names. An event that names
// nobody tries nobody.
func (fl *fleet) funded(e funds.Event) {
	if e.Names() {
		fl.askPass(e.Org, e.Plan)
	}
}

// askPass asks for a pass over the parks of an org or a plan (both empty: of every org). Of any
// number of events for one key there is at most one pass running and one waiting behind it,
// which finds what the events changed: a burst of deposits is two passes, not a goroutine per
// deposit each reading and asking the authorities again.
func (fl *fleet) askPass(org, plan string) {
	key := org + "\x00" + plan
	fl.Lock()
	if fl.passes == nil {
		fl.passes = map[string]*pass{}
	}
	if q := fl.passes[key]; q != nil {
		q.again = true
		fl.Unlock()
		return
	}
	q := &pass{}
	fl.passes[key] = q
	fl.Unlock()
	go func() {
		// Whatever escapes a pass leaves no pass standing: the key is free for the next event.
		defer func() {
			if r := recover(); r != nil {
				fl.say("coding: a pass over parked runs panicked", "org", org, "plan", plan, "panic", r)
				fl.Lock()
				delete(fl.passes, key)
				fl.Unlock()
			}
		}()
		for {
			fl.reconsider(org, plan)
			fl.Lock()
			if !q.again {
				delete(fl.passes, key)
				fl.Unlock()
				return
			}
			q.again = false
			fl.Unlock()
		}
	}()
}

// pass is a pass over the parks of one key that is running; again is that another was asked for
// while it ran.
type pass struct{ again bool }

// serial is the lock tries for one org or plan are made under, so two events for it do not
// try its runs at once.
func (fl *fleet) serial(key string) func() {
	fl.Lock()
	if fl.tryMu == nil {
		fl.tryMu = map[string]*sync.Mutex{}
	}
	mu := fl.tryMu[key]
	if mu == nil {
		mu = &sync.Mutex{}
		fl.tryMu[key] = mu
	}
	fl.Unlock()
	mu.Lock()
	return mu.Unlock
}

// reconsider tries the parked runs of org, or of every org on plan, or of every org when
// both are empty (what a pod does when the bus says it may have missed an event). A run
// whose attempts are failing waits out its delay, and one that has waited longer than a
// run goes on by itself for is a person's. What each run's refusal says is asked of the
// authority that made it before a sandbox is leased, so a change that clears nothing costs
// one read per run.
func (fl *fleet) reconsider(org, plan string) {
	l := fl.ledger()
	if l == nil {
		return
	}
	defer fl.serial(org + "\x00" + plan)()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	var ps []park
	defer func() {
		// A panic before the tries (the plan lookup is a peer call) is in a goroutine nobody
		// recovers. The parks it was to choose from are backed off, as a try that panicked is,
		// so the wake tries them after a delay and nothing waits on this pass.
		if r := recover(); r != nil {
			fl.say("coding: a pass over parked runs panicked; they are tried again after a delay", "org", org, "plan", plan, "panic", r)
			now := time.Now()
			for _, p := range ps {
				if !p.stale(now) {
					fl.later(ctx, p, fmt.Errorf("a pass over them panicked: %v", r))
				}
			}
		}
	}()
	ps, err := l.parksOf(ctx, org)
	if err != nil {
		fl.say("coding: could not read the parked runs to try", "org", org, "err", err)
		return
	}
	fl.attempts(ctx, fl.wanted(ctx, ps, plan, time.Now()), byFunds, nil)
}

// wanted is the parks of ps a change of funds may try now: not one that has waited longer than
// a run goes on by itself for, not one waiting out the delay of an attempt that failed, and,
// for a plan, only those whose payer is on it.
func (fl *fleet) wanted(ctx context.Context, ps []park, plan string, now time.Time) []park {
	var out []park
	for _, p := range ps {
		switch {
		case p.stale(now), p.tries > 0 && p.at > now.Unix():
		case plan != "" && fl.planOf(ctx, p) != plan:
		default:
			out = append(out, p)
		}
	}
	return out
}

// planOf is the plan the payer of a parked run is on, "" when it cannot be read.
func (fl *fleet) planOf(ctx context.Context, p park) string {
	as := zip.WithCaller(cloud.For(context.Background(), p.org), zip.Caller{Org: p.org, User: p.subject, Name: p.spec.Name})
	payer := meter.PayerFrom(as)
	if payer.Zero() {
		return ""
	}
	plan, err := onPlan(ctx, p.org, payer.Subject())
	if err != nil {
		fl.say("coding: the plan of a parked run's payer could not be read", "session", p.session, "err", err)
		return ""
	}
	return plan
}

// released cancels the park of a session that was stopped: it will not be tried.
func (fl *fleet) released(org, session string) {
	l := fl.ledger()
	if l == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), writeWait)
	defer cancel()
	if _, err := l.drop(ctx, org, session, 0); err != nil {
		fl.say("coding: a stopped run's park was not removed; the next try removes it", "session", session, "err", err)
	}
	if err := l.unchain(ctx, org, session); err != nil {
		fl.say("coding: a stopped run's chain was not removed; the sweep removes it", "session", session, "err", err)
	}
}

// Released is told of every session a stop ended, so the park of a stopped run is
// cancelled with it.
func Released(org, session string) { flights.released(org, session) }

// answered is an error of a try that a person asked for (Continue), as the person is told it:
// a run whose person may not act is forbidden, a start that failed is a conflict that says why
// (the run stays parked and is tried again), and a try that could not be made at all is
// unavailable. An error that is already an answer stands.
func answered(err error) error {
	var he *zip.HTTPError
	var done resolved
	switch {
	case err == nil, errors.As(err, &he):
		return err
	case errors.As(err, &done):
		return zip.Errorf(done.status, "%s", done.Error())
	}
	return zip.Errorf(http.StatusServiceUnavailable, "%s", err.Error())
}

// ---- a word to a parked run -------------------------------------------------

// parkedWord is a person's word to a run no pod flies, when the run is parked: Continue
// (no text) tries it now, and an instruction is queued, never refused, to go with it. The
// run is found by its session, or by the thread it narrates in. handled says it was a
// parked run's.
func (fl *fleet) parkedWord(ctx context.Context, org, subject string, in client.CodingSteerIn) (st steered, handled bool, err error) {
	l := fl.ledger()
	if l == nil {
		return steered{}, false, nil
	}
	ps, err := l.parksOf(ctx, org)
	if err != nil {
		return steered{}, false, unavailable(err)
	}
	var p park
	found := false
	for _, q := range ps {
		if id := strings.TrimSpace(in.SessionID); id != "" {
			found = q.session == id
		} else if in.Reply.Channel != "" {
			to := q.spec.In.Reply
			found = q.subject == in.Subject && to.Provider == in.Reply.Provider && to.Channel == in.Reply.Channel && to.Thread == in.Reply.Thread
		}
		if found {
			p = q
			break
		}
	}
	if !found {
		return steered{}, false, nil
	}
	if p.subject != subject {
		return steered{}, true, zip.ErrNotFound("coding: nothing of yours is running there")
	}
	text := strings.TrimSpace(in.Text)
	if text == "" {
		// Pressed on a message, the run goes on in that one.
		if ts := strings.TrimSpace(in.Update); ts != "" {
			p.spec.TS = ts
		}
		return steered{}, true, answered(fl.proceed(ctx, p, byContinue))
	}
	rid, err := ident(in.RequestID)
	if err != nil {
		return steered{}, true, err
	}
	if err := l.room(ctx, org, p.session, rid, "", text); err != nil {
		return steered{}, true, err
	}
	if err := l.enqueue(ctx, org, p.session, subject, rid, text); err != nil {
		var he *zip.HTTPError
		if errors.As(err, &he) {
			return steered{}, true, err
		}
		return steered{}, true, unavailable(err)
	}
	return steered{Queued: true}, true, nil
}

// ---- the session -----------------------------------------------------------

// The session's park and unpark, as values so a test can stand in for the sessions app
// that holds them (the run is in that app's process, as visibleRun is).
var (
	parkSession   = agents.ParkSession
	unparkSession = agents.UnparkSession
)
