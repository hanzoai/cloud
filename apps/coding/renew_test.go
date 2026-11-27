package coding

// renew_test.go — a run's model credential is short-lived and renewed by the host
// (key.go). The clock is a fake one the test advances, IAM is a server the test
// scripts, and the person's standing is the park's own mayAct, so each test says one
// thing about the renewal: that it is made before the credential ends, that a person
// removed is ended and one IAM cannot be asked about is waited for, that no run waits
// on another, and that nothing is renewed after the run is over.

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/apps/agents"
)

// fakeClock is the time a credential is renewed by: it moves when the test moves it. A
// timer's callback runs in a goroutine of its own, as a real timer's does.
type fakeClock struct {
	mu     sync.Mutex
	at     time.Time
	timers []*fakeTimer
	firing sync.WaitGroup
}

type fakeTimer struct {
	c              *fakeClock
	due            time.Time
	f              func()
	stopped, fired bool
}

func (t *fakeTimer) Stop() bool {
	t.c.mu.Lock()
	defer t.c.mu.Unlock()
	was := !t.stopped && !t.fired
	t.stopped = true
	return was
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *fakeClock) after(d time.Duration, f func()) stopper {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &fakeTimer{c: c, due: c.at.Add(d), f: f}
	c.timers = append(c.timers, t)
	return t
}

// advance moves the clock and starts every timer that has come due.
func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	c.at = c.at.Add(d)
	var due []*fakeTimer
	for _, t := range c.timers {
		if !t.stopped && !t.fired && !t.due.After(c.at) {
			t.fired = true
			due = append(due, t)
		}
	}
	c.mu.Unlock()
	slices.SortFunc(due, func(a, b *fakeTimer) int { return a.due.Compare(b.due) })
	for _, t := range due {
		c.firing.Add(1)
		go func() { defer c.firing.Done(); t.f() }()
	}
}

// tick is advance, and waits for what it started to finish.
func (c *fakeClock) tick(d time.Duration) {
	c.advance(d)
	c.firing.Wait()
}

// pending is how long from now each timer that has not fired or been stopped is set for.
func (c *fakeClock) pending() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []time.Duration
	for _, t := range c.timers {
		if !t.stopped && !t.fired {
			out = append(out, t.due.Sub(c.at))
		}
	}
	slices.Sort(out)
	return out
}

// holdClock makes the test the time a credential is renewed by.
func holdClock(t *testing.T) *fakeClock {
	t.Helper()
	c := &fakeClock{at: time.Now()}
	wasNow, wasAfter := credNow, credAfter
	credNow, credAfter = c.now, c.after
	t.Cleanup(func() { credNow, credAfter = wasNow, wasAfter })
	return c
}

// eventually waits for what a goroutine of the code under test will do.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for end := time.Now().Add(10 * time.Second); time.Now().Before(end); time.Sleep(2 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("never happened: %s", what)
}

// renewRig is a run's credential with everything around it faked: IAM, the person's standing,
// the audit trail, the clock, the box that takes a renewed token, and the run it ends.
type renewRig struct {
	t     *testing.T
	iam   *fakeIAM
	app   *sessionsApp
	clock *fakeClock

	mu       sync.Mutex
	planted  []string
	plantErr error
	// gate, when set, holds the box's answer until it is closed, as a sandbox slow to answer is: and
	// the box takes no notice of the run having ended meanwhile.
	gate   <-chan struct{}
	halts  []string
	causes []error
}

// newRealRig is newRenewRig with the runner reading its own identity through the real token source
// (tokensource.go) against the fake IAM, as production does.
func newRealRig(t *testing.T) *renewRig {
	t.Helper()
	r := newRenewRig(t)
	r.iam.useRealSource(t)
	return r
}

func newRenewRig(t *testing.T) *renewRig {
	t.Helper()
	r := &renewRig{t: t}
	r.iam = &fakeIAM{token: func(n int) string { return jwtWith("ai:inference", "tok-"+strconv.Itoa(n)) }, life: int(credLife.Seconds())}
	r.iam.serve(t)
	r.app = newSessionsApp(t)
	r.clock = holdClock(t)
	return r
}

func (r *renewRig) plant(_ context.Context, token string) error {
	r.mu.Lock()
	gate := r.gate
	r.mu.Unlock()
	if gate != nil {
		<-gate
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.plantErr != nil {
		return r.plantErr
	}
	r.planted = append(r.planted, token)
	return nil
}

func (r *renewRig) halt(reason string, cause error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.halts = append(r.halts, reason)
	r.causes = append(r.causes, cause)
}

func (r *renewRig) halted() []string { r.mu.Lock(); defer r.mu.Unlock(); return slices.Clone(r.halts) }
func (r *renewRig) placed() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.planted)
}

// key is a run's credential, minted for a run with no deadline of its own.
func (r *renewRig) key(org, person, run string) *bearer {
	r.t.Helper()
	b, err := key(r.t.Context(), loan{org: org, person: person, run: run, ttl: 24 * 3600, plant: r.plant, halt: r.halt})
	if err != nil || b == nil {
		r.t.Fatalf("key: %v / %v", b, err)
	}
	r.t.Cleanup(b.close)
	return b
}

func untilOf(b *bearer) time.Time { b.mu.Lock(); defer b.mu.Unlock(); return b.until }

func lifetimeOf(form url.Values) int { n, _ := strconv.Atoi(form.Get("lifetime")); return n }

func actions(m *memTrail) []string {
	var out []string
	for _, rec := range m.records() {
		out = append(out, rec.Action+":"+rec.Outcome.Result)
	}
	return out
}

// A credential is asked of IAM for a short life, not the run's day, and renewed before it
// ends: the one timer is set for between the margin and a quarter more before the end, nothing
// is asked until then, and the renewal is put in the box before it is the one in use. The
// token before it is still blinded and still revoked at the end.
//
// MUTATION: leave arm without its timer and the credential ends at its life.
func TestACredentialIsShortLivedAndRenewedBeforeItEnds(t *testing.T) {
	r := newRenewRig(t)
	r.app.run("acme", "alice", "sess_1")
	b := r.key("acme", "alice", "sess_1")
	first := b.current()

	if asked := lifetimeOf(r.iam.asks()[0]); asked < int(credLife.Seconds())-5 || asked > int(credLife.Seconds()) {
		t.Fatalf("the first delegation was asked for %ds, want %v, not the run's day", asked, credLife)
	}
	due := r.clock.pending()
	soonest, latest := credLife-credMargin-credMargin/4, credLife-credMargin
	if len(due) != 1 || due[0] < soonest || due[0] > latest {
		t.Fatalf("timers set for %v, want one between %v and %v: the margin, and up to a quarter of it earlier", due, soonest, latest)
	}

	r.clock.tick(due[0] - time.Second)
	if n := len(r.iam.asks()); n != 1 {
		t.Fatalf("IAM was asked %d times before the margin, want only the mint", n)
	}
	r.clock.tick(2 * time.Second)
	eventually(t, "the renewed token is in use", func() bool { return b.current() != first })

	if asks := r.iam.asks(); len(asks) != 2 || lifetimeOf(asks[1]) < int(credLife.Seconds())-5 {
		t.Fatalf("the renewal asked IAM %d times for %v, want one more for a fresh life", len(asks), asks)
	}
	if got := r.placed(); len(got) != 1 || got[0] != b.current() {
		t.Fatalf("the box was handed %v, want exactly the renewed token %q", got, b.current())
	}
	if known := b.known(); len(known) != 2 || known[0] != b.current() || known[1] != first {
		t.Fatalf("the blinded tokens are %v, want the renewed one and the one before", known)
	}
	if next := r.clock.pending(); len(next) != 1 || next[0] < soonest || next[0] > latest {
		t.Fatalf("after a renewal the timers are %v, want one for the next, the same margin ahead", next)
	}
	if len(r.halted()) != 0 {
		t.Fatalf("a renewed run was ended: %v", r.halted())
	}
	b.close()
	if got := r.iam.revokedTokens(); !slices.Equal(got, []string{b.current(), first}) {
		t.Fatalf("revoked %v at the end, want the token in use and the one before it", got)
	}
}

// A caller of key whose deadline falls inside one life gets a token for what it needs, as before, and holds
// no timer. (In production the run's context always carries the ceiling, so every renewing run does arm one;
// close cancels it.)
//
// MUTATION: arm without asking whether the token outlives the run, and every short run holds a
// timer it will never use.
func TestARunShorterThanOneLifeHoldsNoTimer(t *testing.T) {
	r := newRenewRig(t)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Minute)
	defer cancel()
	b, err := key(ctx, loan{org: "acme", person: "alice", run: "sess_short", plant: r.plant, halt: r.halt})
	if err != nil || b == nil {
		t.Fatalf("key: %v / %v", b, err)
	}
	defer b.close()
	if asked := lifetimeOf(r.iam.asks()[0]); asked > 600 || asked < 590 {
		t.Fatalf("a ten-minute run's delegation was asked for %ds, want the run's ten minutes", asked)
	}
	if due := r.clock.pending(); len(due) != 0 {
		t.Fatalf("a run that ends before its credential does holds timers %v", due)
	}
}

// The renewal is a fresh check of the person, the park's own (mayAct): one IAM no longer lets
// act in the org is not delegated to again. The run is ended "access removed", as a parked
// run of theirs is, the tokens are revoked at once, nothing is put in the box, the act is on the
// trail as a denial, and nothing is asked of IAM again.
//
// MUTATION: delegate without asking mayAct and a removed person is renewed for as long as IAM
// would delegate.
func TestAPersonRemovedIsEndedAtTheirNextRenewal(t *testing.T) {
	r := newRenewRig(t)
	r.app.run("acme", "alice", "sess_1")
	b := r.key("acme", "alice", "sess_1")
	first := b.current()

	r.app.revoke("acme", "alice")
	r.clock.tick(credLife - credMargin)
	eventually(t, "the run is ended", func() bool { return len(r.halted()) == 1 })

	if got := r.halted()[0]; got != accessRemoved {
		t.Fatalf("the run was ended for %q, want %q", got, accessRemoved)
	}
	if !errors.Is(r.causes[0], agents.ErrStopped) || !errors.Is(r.causes[0], errRevoked) {
		t.Fatalf("the run was stopped with %v, want a stop that keeps its work", r.causes[0])
	}
	if !b.removed() {
		t.Fatal("the credential does not say its person was removed, so the run's sandbox would be kept for them")
	}
	if n := len(r.iam.asks()); n != 1 {
		t.Fatalf("IAM was asked to delegate %d times, want only the mint: a removed person is not delegated to", n)
	}
	if got := r.placed(); len(got) != 0 {
		t.Fatalf("the box was handed %v after the person was removed", got)
	}
	if got := r.iam.revokedTokens(); !slices.Equal(got, []string{first}) {
		t.Fatalf("revoked %v, want the token the person held, at once", got)
	}
	recs := r.iam.trail.records()
	last := recs[len(recs)-1]
	if last.Action != "coding.run.revoke" || last.Outcome.Result != "deny" || last.Outcome.Status != http.StatusForbidden ||
		last.Actor.Sub != "alice" || last.Actor.Org != "acme" || last.Resource.ID != "sess_1" {
		t.Fatalf("the trail holds %+v, want the denial as alice's, in acme, of her session", last)
	}
	r.clock.tick(3 * time.Hour)
	if n := len(r.iam.asks()); n != 1 || len(r.clock.pending()) != 0 {
		t.Fatalf("after the run was ended IAM was asked %d times and %v timers remain", n, r.clock.pending())
	}
}

// IAM saying the person may not be acted for is the same ending, whichever door says it; a
// refusal that is about this deployment (its own client credential, its actor token) is not a
// person's loss and is waited out like an IAM that cannot be reached.
//
// MUTATION: read every refusal as a denial and a rotated client secret ends every run in
// flight as "access removed".
func TestAnIAMRefusalIsADenialOnlyWhenItIsAboutThePerson(t *testing.T) {
	for name, c := range map[string]struct {
		status int
		denied bool
	}{
		"access denied":  {http.StatusForbidden, true},
		"invalid client": {http.StatusUnauthorized, false},
		"invalid grant":  {http.StatusBadRequest, false},
		"unavailable":    {http.StatusServiceUnavailable, false},
		"rate limited":   {http.StatusTooManyRequests, false},
	} {
		t.Run(name, func(t *testing.T) {
			r := newRenewRig(t)
			r.iam.answer = func(n int, _ url.Values) int {
				if n == 1 {
					return 0
				}
				return c.status
			}
			r.app.run("acme", "alice", "sess_1")
			b := r.key("acme", "alice", "sess_1")
			r.clock.tick(credLife - credMargin)
			eventually(t, "IAM was asked again", func() bool { return len(r.iam.asks()) == 2 })
			eventually(t, "the run is ended or waits", func() bool { return len(r.halted()) == 1 || len(r.clock.pending()) == 1 })
			if got := len(r.halted()) == 1; got != c.denied {
				t.Fatalf("IAM answered %d: ended = %v, want %v", c.status, r.halted(), c.denied)
			}
			if b.removed() != c.denied {
				t.Fatalf("IAM answered %d: removed = %v, want %v", c.status, b.removed(), c.denied)
			}
		})
	}
}

// IAM down and then up: the renewal is asked again after the delay a failed claim waits (30s,
// doubling), spread a quarter later at most, while the token still lives, and the run goes on
// with the renewed token. The delays are the park's own (retryAfter).
//
// MUTATION: give up on the first failure and the run ends at a blip.
func TestIAMDownThenUpRenewsAndKeepsTheRun(t *testing.T) {
	r := newRenewRig(t)
	r.iam.setAnswer(func(n int, _ url.Values) int {
		if n == 2 || n == 3 {
			return http.StatusServiceUnavailable
		}
		return 0
	})
	r.app.run("acme", "alice", "sess_1")
	b := r.key("acme", "alice", "sess_1")
	first := b.current()

	r.clock.tick(credLife - credMargin)
	eventually(t, "the first retry is set", func() bool { return len(r.clock.pending()) == 1 && len(r.iam.asks()) == 2 })
	if due := r.clock.pending()[0]; due < retryAfter(1) || due > retryAfter(1)+retryAfter(1)/4 {
		t.Fatalf("first retry in %v, want %v and up to a quarter more", due, retryAfter(1))
	}
	if b.current() != first {
		t.Fatal("a failed renewal replaced the token in use")
	}
	r.clock.tick(retryAfter(1) + retryAfter(1)/4)
	eventually(t, "the second retry is set", func() bool { return len(r.clock.pending()) == 1 && len(r.iam.asks()) == 3 })
	if due := r.clock.pending()[0]; due < retryAfter(2) || due > retryAfter(2)+retryAfter(2)/4 {
		t.Fatalf("second retry in %v, want it doubled to %v and up to a quarter more", due, retryAfter(2))
	}
	r.clock.tick(retryAfter(2) + retryAfter(2)/4)
	eventually(t, "the renewal lands", func() bool { return b.current() != first })
	if got := r.placed(); len(got) != 1 || got[0] != b.current() {
		t.Fatalf("the box was handed %v, want the renewed token once", got)
	}
	if len(r.halted()) != 0 {
		t.Fatalf("a run IAM was briefly down for was ended: %v", r.halted())
	}
	if due := r.clock.pending(); len(due) != 1 || due[0] < credLife-credMargin-credMargin/4 {
		t.Fatalf("after the renewal the next is %v away, want a margin before the new life ends", due)
	}
}

// IAM down until the token ends: the run is ended with a clear reason, never "access removed",
// its work kept (a stop), and nothing is renewed as anyone else. The last try is at the token's
// own end, so a credential is never spent a moment past it.
//
// MUTATION: retry without a bound and a run goes on, unable to call its model, for ever.
func TestATokenThatExpiresWithIAMDownEndsTheRun(t *testing.T) {
	r := newRenewRig(t)
	r.iam.setAnswer(func(n int, _ url.Values) int {
		if n == 1 {
			return 0
		}
		return http.StatusServiceUnavailable
	})
	r.app.run("acme", "alice", "sess_1")
	b := r.key("acme", "alice", "sess_1")

	r.clock.tick(credLife - credMargin)
	for step := 0; step < 40 && len(r.halted()) == 0; step++ {
		eventually(t, "a retry is set or the run is ended", func() bool { return len(r.clock.pending()) == 1 || len(r.halted()) == 1 })
		if len(r.halted()) == 1 {
			break
		}
		next := r.clock.pending()[0]
		if until := untilOf(b).Sub(r.clock.now()); next > until {
			t.Fatalf("a retry is set %v ahead, past the token's end in %v", next, until)
		}
		r.clock.tick(next)
	}
	eventually(t, "the run is ended", func() bool { return len(r.halted()) == 1 })
	if got := r.halted()[0]; got != unrenewed || !errors.Is(r.causes[0], errLapsed) || !errors.Is(r.causes[0], agents.ErrStopped) {
		t.Fatalf("the run was ended for %q / %v, want %q as a stop that keeps its work", got, r.causes[0], unrenewed)
	}
	if b.removed() {
		t.Fatal("a person IAM merely could not be asked about was said to be removed")
	}
	if !r.clock.now().Equal(untilOf(b)) {
		t.Fatalf("ended at %v, want the token's own end %v", r.clock.now(), untilOf(b))
	}
	if got := actions(r.iam.trail); !slices.Contains(got, "coding.run.lapse:error") {
		t.Fatalf("the trail holds %v, want the lapse", got)
	}
	if len(r.clock.pending()) != 0 || len(r.placed()) != 0 {
		t.Fatalf("after the run was ended %v timers remain and the box was handed %v", r.clock.pending(), r.placed())
	}
}

// A renewal that hangs is that run's alone: the other renews on time. Nothing is shared between
// runs but IAM, and no lock is held across a call to it.
//
// MUTATION: hold one package lock across fresh() and B's renewal waits for A's.
func TestOneRunsRenewalHangingDoesNotDelayAnother(t *testing.T) {
	r := newRealRig(t)
	r.app.run("acme", "alice", "sess_a")
	r.app.run("acme", "bob", "sess_b")
	a := r.key("acme", "alice", "sess_a")
	b := r.key("acme", "bob", "sess_b")
	firstA, firstB := a.current(), b.current()

	// A's call to IAM hangs; B's is answered.
	release := make(chan struct{})
	r.iam.setHold(func(form url.Values) <-chan struct{} {
		if form.Get("run") == "sess_a" {
			return release
		}
		return nil
	})
	r.clock.advance(credLife - credMargin)
	eventually(t, "B renews while A's call hangs", func() bool { return b.current() != firstB })
	if a.current() != firstA {
		t.Fatal("A's renewal finished while its call to IAM was held")
	}
	if len(r.halted()) != 0 {
		t.Fatalf("a run was ended: %v", r.halted())
	}
	// B's next renewal is armed: nothing of A's hang is in its way.
	if due := r.clock.pending(); len(due) != 1 {
		t.Fatalf("timers %v while A hangs, want B's next alone", due)
	}
	close(release)
	r.clock.firing.Wait()
	eventually(t, "A renews once IAM answers", func() bool { return a.current() != firstA })
}

// The deployment's own token is one refresh for every run that needs it, and a run waits for it only as
// long as the run lasts: a renewal held on a token endpoint that hangs leaves when its run ends, without
// waiting for the endpoint's thirty seconds; and runs that wait together wait on one refresh, not on one
// each. Against the real source: the grants here are too short to be cached, so every delegation needs a
// fresh one.
//
// MUTATION: read the token with actor.Token() in Delegate and a run that has ended is still waiting; take
// a lock across the fetch and the second run waits out the first's.
func TestARenewalWaitingOnTheMachineTokenLeavesWhenItsRunDoes(t *testing.T) {
	hold := func(t *testing.T) (*renewRig, chan struct{}) {
		r := newRealRig(t)
		r.iam.mu.Lock()
		r.iam.actorLife = 5
		r.iam.mu.Unlock()
		release := make(chan struct{})
		t.Cleanup(func() {
			select {
			case <-release:
			default:
				close(release)
			}
		})
		return r, release
	}
	t.Run("the run ends", func(t *testing.T) {
		r, release := hold(t)
		r.app.run("acme", "alice", "sess_a")
		a := r.key("acme", "alice", "sess_a")
		r.iam.setActorHold(func() <-chan struct{} { return release })
		granted := r.iam.actorGrants()
		r.clock.advance(credLife - credMargin)
		eventually(t, "the renewal is waiting on the machine token", func() bool { return r.iam.actorGrants() > granted })

		a.close()
		gone := make(chan struct{})
		go func() { r.clock.firing.Wait(); close(gone) }()
		select {
		case <-gone:
		case <-time.After(5 * time.Second):
			t.Fatal("a renewal of a run that had ended was still waiting on the machine token endpoint")
		}
		if n := len(r.iam.asks()); n != 1 {
			t.Fatalf("IAM was asked to delegate %d times, want only the mint", n)
		}
	})
	t.Run("two runs wait on one refresh", func(t *testing.T) {
		r, release := hold(t)
		r.app.run("acme", "alice", "sess_a")
		r.app.run("acme", "bob", "sess_b")
		a := r.key("acme", "alice", "sess_a")
		b := r.key("acme", "bob", "sess_b")
		firstA, firstB := a.current(), b.current()
		r.iam.setActorHold(func() <-chan struct{} { return release })
		granted := r.iam.actorGrants()

		r.clock.advance(credLife - credMargin)
		eventually(t, "the renewals are waiting on the machine token", func() bool { return r.iam.actorGrants() > granted })
		if a.current() != firstA || b.current() != firstB {
			t.Fatal("a run was handed a token while the machine token endpoint was held")
		}
		close(release)
		eventually(t, "both renew once the machine token comes", func() bool { return a.current() != firstA && b.current() != firstB })
		if n := r.iam.actorGrants() - granted; n != 1 {
			t.Fatalf("the machine token was asked %d times for two runs, want the one refresh they shared", n)
		}
	})
}

// A run that ends takes its credential with it: the timer is let go, the tokens are revoked at
// IAM, and a renewal that was in flight when it ended adopts nothing. A call to IAM it was making is
// abandoned; a token it had already been given is revoked unused, and a second close is harmless.
//
// MUTATION: leave out the done check in adopt and a renewal after the run adopts a token no
// one revokes.
func TestAnEndedRunRenewsNothingAndLeavesNothingLive(t *testing.T) {
	t.Run("before the timer fires", func(t *testing.T) {
		r := newRenewRig(t)
		r.app.run("acme", "alice", "sess_1")
		b := r.key("acme", "alice", "sess_1")
		first := b.current()
		if len(r.clock.pending()) != 1 {
			t.Fatalf("timers %v, want the one", r.clock.pending())
		}
		b.close()
		if due := r.clock.pending(); len(due) != 0 {
			t.Fatalf("the run ended and its timer was left set: %v", due)
		}
		r.clock.tick(5 * time.Hour)
		if n := len(r.iam.asks()); n != 1 || b.current() != first {
			t.Fatalf("IAM was asked %d times after the run ended, want only the mint", n)
		}
		if got := r.iam.revokedTokens(); !slices.Equal(got, []string{first}) {
			t.Fatalf("revoked %v, want the run's token", got)
		}
	})
	t.Run("a call to IAM in flight", func(t *testing.T) {
		r := newRenewRig(t)
		release := make(chan struct{})
		defer close(release)
		r.app.run("acme", "alice", "sess_1")
		b := r.key("acme", "alice", "sess_1")
		first := b.current()
		r.iam.setHold(func(url.Values) <-chan struct{} { return release })

		r.clock.advance(credLife - credMargin)
		eventually(t, "the renewal is in flight", func() bool { return len(r.iam.asks()) == 2 })
		b.close()
		r.clock.firing.Wait()
		if due := r.clock.pending(); len(due) != 0 {
			t.Fatalf("timers remain after the run ended: %v", due)
		}
		if b.current() != first || len(r.placed()) != 0 {
			t.Fatalf("a renewal abandoned by the run's end left token %q and %v in the box", b.current(), r.placed())
		}
		r.clock.tick(5 * time.Hour)
		if n := len(r.iam.asks()); n != 2 {
			t.Fatalf("IAM was asked %d times, want none after the run ended", n)
		}
		b.close() // twice is harmless
		if got := r.iam.revokedTokens(); !slices.Equal(got, []string{first}) {
			t.Fatalf("revoked %v, want the run's token once", got)
		}
	})
	t.Run("a token already given", func(t *testing.T) {
		r := newRenewRig(t)
		gate := make(chan struct{})
		r.mu.Lock()
		r.gate = gate
		r.mu.Unlock()
		r.app.run("acme", "alice", "sess_1")
		b := r.key("acme", "alice", "sess_1")
		first := b.current()

		r.clock.advance(credLife - credMargin)
		eventually(t, "IAM has given a second token and the box has not answered", func() bool { return len(r.iam.asks()) == 2 })
		b.close()
		close(gate) // the box answers after the run is over
		r.clock.firing.Wait()

		if b.current() != first {
			t.Fatal("a renewal that finished after the run ended became the token in use")
		}
		late := jwtWith("ai:inference", "tok-2")
		eventually(t, "every token is revoked", func() bool { return len(r.iam.revokedTokens()) == 2 })
		if got := r.iam.revokedTokens(); !slices.Contains(got, first) || !slices.Contains(got, late) {
			t.Fatalf("revoked %v, want the run's token and the one given as it ended", got)
		}
		if due := r.clock.pending(); len(due) != 0 {
			t.Fatalf("timers remain after the run ended: %v", due)
		}
	})
}

// A token the box did not take is not the token in use: the credential in the box and the one the
// next turn starts with are the same. One the box SAID it did not take (the exec ran and failed) is
// revoked unused. One whose answer was lost (a timeout, a dropped stream) may be in the file the harness
// reads, so it is neither revoked nor adopted: it is blinded and kept, a retry puts another there, and the
// one the retry lands replaces it and has it revoked.
//
// MUTATION: adopt before planting and a command starts with a token the harness never saw; revoke on
// any error and the harness is left a revoked token in its file.
