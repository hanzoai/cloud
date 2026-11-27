package coding

// extend.go — more time for a run that is working.
//
// A run is held to the time it was asked for (TimeoutSeconds): at that moment it
// keeps its work on its branch and ends (errTimeUp), and a follow-up (`after`)
// carries the task on. That is the right end for a run nobody is watching, and
// the wrong one for a run somebody is: a task expected to take five minutes that
// is at minute five, three edits from done, should be given two more minutes, not
// restarted.
//
// So the time a run is held to is a CLOCK a caller may move later while the run
// works. The context's own deadline stays where it was — the longest the run may
// ever take (the ceiling) — because a plane call reads its read deadline off the
// context's (sdk/plane/readdeadline.go), and a context with no deadline would hold
// every long call to the short default again. What ends the run at its time is
// the clock's timer, which cancels the run with the same cause the deadline
// would, so a run ended by it is kept and told exactly as one the deadline ended.

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud/apps/agents"
	"github.com/hanzoai/cloud/client"
	"github.com/zap-proto/zip"
)

// ceiling is the longest a run may take, from its start, and the furthest its time may be
// moved to: the longest a session may live (agents.MaxLife, a day), the same figure the
// session reaper holds every session to. A run asked for more takes this, and the answer to an
// extension says what it got.
func ceiling() time.Duration { return agents.MaxLife() }

// errSpent is an extension asked of a run whose time is already up.
var errSpent = errors.New("coding: that run's time is up; start a new run with `after` set to this session to carry on")

// clock is the time a run is held to.
type clock struct {
	mu    sync.Mutex
	timer *time.Timer
	until time.Time // when the run is ended for time
	hard  time.Time // the latest until may be moved to
	over  bool      // the timer fired: the run is out of time
	stop  context.CancelCauseFunc
}

// newClock holds the run to soft, and lets it be moved as late as hard.
func newClock(now time.Time, soft, hard time.Duration, stop context.CancelCauseFunc) *clock {
	k := &clock{until: now.Add(soft), hard: now.Add(max(soft, hard)), stop: stop}
	k.timer = time.AfterFunc(soft, k.fire)
	return k
}

// fire ends the run for time.
func (k *clock) fire() {
	k.mu.Lock()
	k.over = true
	k.mu.Unlock()
	k.stop(errTimeUp)
}

// end lets the timer go with the run.
func (k *clock) end() { k.timer.Stop() }

// extend moves the run's time later by d, as far as the ceiling allows, and
// answers when it is up now. A run already at its ceiling, or out of time, is
// refused, because an answer that quietly changed nothing would read as success.
func (k *clock) extend(now time.Time, d time.Duration) (time.Time, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.over {
		return k.until, errSpent
	}
	if !k.until.Before(k.hard) {
		return k.until, zip.Errorf(http.StatusConflict, "coding: that run is already held for as long as a run may be (%s)", ceiling().Round(time.Minute))
	}
	next := k.until.Add(d)
	if next.After(k.hard) {
		next = k.hard
	}
	if !k.timer.Reset(next.Sub(now)) {
		// The timer had fired as we took the lock: the run is ending.
		k.over = true
		return k.until, errSpent
	}
	k.until = next
	return next, nil
}

// left is how long the run has, from now.
func (k *clock) left(now time.Time) time.Duration {
	k.mu.Lock()
	defer k.mu.Unlock()
	return max(0, k.until.Sub(now))
}

// Extend gives a running coding run more time: seconds added to the time it is
// held to, as far as the ceiling allows. It answers when the run's time is up
// now, so a caller that asked for more than the ceiling allows reads what it got.
//
// The run must be the caller's own and still working: another person's is
// refused, one that is paused or out of time is a conflict, and one no pod is
// running — or one sent to a machine of the org's own, which keeps its own
// time — is not found.
//
// With a request id the time is added once, however often and wherever it is asked:
// a retry answers what the first answered (ledger.go).
func Extend(ctx context.Context, org, subject string, in *client.CodingExtendIn) (*client.CodingExtended, error) {
	return flights.extend(ctx, org, subject, in)
}

func (fl *fleet) extend(ctx context.Context, org, subject string, in *client.CodingExtendIn) (*client.CodingExtended, error) {
	if in.Seconds <= 0 {
		return nil, zip.ErrBadRequest("coding: seconds is how much longer the run may work, and must be above zero")
	}
	return requested(ctx, fl.ledger(), org, subject, scoped("extend", in.Session), in.RequestID,
		printOf(struct {
			Session string
			Seconds int
		}{strings.TrimSpace(in.Session), in.Seconds}),
		func(ctx context.Context) (*client.CodingExtended, error) {
			f := fl.flying(org, in.Session)
			if f == nil {
				return fl.extendElsewhere(org, subject, in)
			}
			return f.extended(ctx, subject, in)
		})
}

// flying is the run of session this pod flies for org, if it has a clock to extend.
func (fl *fleet) flying(org, session string) *flight {
	fl.Lock()
	f := fl.m[strings.TrimSpace(session)]
	fl.Unlock()
	if f == nil || f.org != org || f.clock == nil {
		return nil
	}
	return f
}

// extended is the extension of a run this pod flies. The effect is made once per request
// id, by the pod that makes it: the asker's claim is held by a pod that can die after the
// run has been given its time and before it says so, and the retry that retakes the claim
// asks this pod again. So the time is added under a claim of its own, here, and a replay
// finds the answer it was given and adds nothing.
func (f *flight) extended(ctx context.Context, subject string, in *client.CodingExtendIn) (*client.CodingExtended, error) {
	if f.subject != subject {
		return nil, zip.ErrForbidden("coding: only the person whose run this is can give it more time")
	}
	var l *ledger
	if f.fleet != nil {
		l = f.fleet.ledger()
	}
	id, err := requestID(in.RequestID)
	if err != nil {
		return nil, err
	}
	if l == nil || id == "" {
		return f.prolong(in)
	}
	return once(ctx, l, request{org: f.org, subject: subject, scope: scoped("extended", f.session), id: id,
		print: printOf(struct {
			Session string
			Seconds int
		}{f.session, in.Seconds})},
		func(*claim) (*client.CodingExtended, error) { return f.prolong(in) })
}

// prolong moves the run's clock.
func (f *flight) prolong(in *client.CodingExtendIn) (*client.CodingExtended, error) {
	f.mu.Lock()
	paused := f.paused
	f.mu.Unlock()
	if paused {
		return nil, zip.Errorf(http.StatusConflict, "coding: that run is paused; continue it instead")
	}
	now := time.Now()
	until, err := f.clock.extend(now, time.Duration(in.Seconds)*time.Second)
	if errors.Is(err, errSpent) {
		return nil, zip.Errorf(http.StatusConflict, "%s", err.Error())
	}
	if err != nil {
		return nil, err
	}
	return &client.CodingExtended{Until: until.UTC().Format(time.RFC3339), Seconds: int(until.Sub(now).Seconds())}, nil
}
