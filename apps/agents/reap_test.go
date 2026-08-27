package agents

// The reaper, driven through the REAL per-org stores and the REAL stop path, so
// what a reap does to a row — and what it stops costing — is measured rather than
// modelled. The policy (clocks.over) is a pure function and is tested as one; the
// pass is tested against rows planted relative to the wall clock it reads, because
// the end it writes comes from that same clock.
//
// Each test names the MUTATION that makes it fail.

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
)

// record is a logger that keeps what it was told, so a test can ask whether a reap
// was REPORTED as well as performed — an operator with no line has no way to answer
// "why did my agent stop".
type record struct {
	mu   sync.Mutex
	info []string
	warn []string
}

func (r *record) Info(msg string, _ ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.info = append(r.info, msg)
}

func (r *record) Warn(msg string, _ ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.warn = append(r.warn, msg)
}

func (r *record) infos() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.info...)
}
func (r *record) warns() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.warn...)
}

// testClocks is the policy every test here judges against: an hour of silence, a
// day of life. Written out rather than read through newClocks so no environment a
// developer happens to have set can change what these tests measure.
var testClocks = clocks{idle: time.Hour, maxLife: 24 * time.Hour}

// plant writes one session with every stamp at `at`, in the status given — the
// shape a surface leaves behind when it opens a run and then stops existing.
func plant(t *testing.T, sto *Store, id, status string, at int64) Session {
	t.Helper()
	x := Session{ID: id, Org: "acme", Agent: "coder", Actor: "acme/z", Status: status,
		RootID: id, StartedAt: at, CreatedAt: at, UpdatedAt: at, Payer: "acme", MeteredAt: at}
	if err := sto.CreateSession(context.Background(), x); err != nil {
		t.Fatalf("create session: %v", err)
	}
	return x
}

// reload reads a session back, which is the only statement about it worth
// asserting: the pass writes through the store, so an in-memory copy proves nothing.
func reload(t *testing.T, sto *Store, id string) Session {
	t.Helper()
	x, err := sto.GetSession(context.Background(), "acme", id)
	if err != nil {
		t.Fatalf("get session %s: %v", id, err)
	}
	return x
}

// ---- the policy ----

// TestOverReadsActivityAndNotLiveness is the whole rule in one table. `status` is a
// claim; updated_at is what the session has done.
//
// MUTATION: read x.StartedAt instead of x.UpdatedAt in over's idle branch and the
// "busy for hours" case is reaped — which is the one reap that destroys work.
func TestOverReadsActivityAndNotLiveness(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	ago := func(d time.Duration) int64 { return now.Add(-d).Unix() }

	for _, tc := range []struct {
		name string
		x    Session
		want bool
		says string
	}{
		{"fresh run", Session{Status: StatusRunning, StartedAt: ago(time.Minute), UpdatedAt: ago(time.Minute)}, false, ""},
		{"quiet for half the allowance", Session{Status: StatusRunning, StartedAt: ago(2 * time.Hour), UpdatedAt: ago(30 * time.Minute)}, false, ""},
		{"quiet past the allowance", Session{Status: StatusRunning, StartedAt: ago(3 * time.Hour), UpdatedAt: ago(3 * time.Hour)}, true, "no activity"},
		{"busy for hours", Session{Status: StatusRunning, StartedAt: ago(9 * time.Hour), UpdatedAt: ago(time.Minute)}, false, ""},
		{"busy past the ceiling", Session{Status: StatusRunning, StartedAt: ago(25 * time.Hour), UpdatedAt: ago(time.Minute)}, true, "past the"},
		{"paused past the allowance", Session{Status: StatusPaused, StartedAt: ago(5 * time.Hour), UpdatedAt: ago(5 * time.Hour)}, false, ""},
		{"paused past the ceiling", Session{Status: StatusPaused, StartedAt: ago(25 * time.Hour), UpdatedAt: ago(25 * time.Hour)}, true, "past the"},
		{"never stamped", Session{Status: StatusRunning}, false, ""},
		// Past the ceiling on every stamp, and already ended: there is nothing left
		// to do to it, and a second end written over the first would lose the one
		// somebody recorded.
		{"already ended", Session{Status: StatusRunning, StartedAt: ago(30 * time.Hour),
			UpdatedAt: ago(30 * time.Hour), EndedAt: ago(29 * time.Hour)}, false, ""},
		{"already terminal", Session{Status: StatusDone, StartedAt: ago(30 * time.Hour),
			UpdatedAt: ago(30 * time.Hour)}, false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			done, why := testClocks.over(tc.x, now)
			if done != tc.want {
				t.Fatalf("over = %v (%q), want %v", done, why, tc.want)
			}
			if tc.want && !strings.Contains(why, tc.says) {
				t.Fatalf("the reason was %q, which does not say %q", why, tc.says)
			}
			if !tc.want && why != "" {
				t.Fatalf("a session that is not over came with the reason %q", why)
			}
		})
	}
}

// TestTheCeilingIsAskedFirst — a session past BOTH clocks is reported against the
// ceiling, so a bug in the activity stamp can delay a reap by at most max-life and
// never past it.
//
// MUTATION: move the max-life branch below the idle branch and this reads "no
// activity", which tells an operator the wrong thing about a session that has been
// running for two days.
func TestTheCeilingIsAskedFirst(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	x := Session{Status: StatusRunning, StartedAt: now.Add(-48 * time.Hour).Unix(),
		UpdatedAt: now.Add(-48 * time.Hour).Unix()}
	done, why := testClocks.over(x, now)
	if !done || !strings.Contains(why, "past the") {
		t.Fatalf("a session past both clocks reported %v %q, want the ceiling", done, why)
	}
}

// TestAClockSetToZeroIsNoClock — a policy with no ceiling still reaps on idle, and
// one with no idle allowance still has a ceiling. The two are independent, which is
// what makes an operator able to lengthen one without removing the other.
//
// MUTATION: drop the `c.maxLife > 0` / `c.idle > 0` guards and a zero reaps
// everything the instant it is created.
func TestAClockSetToZeroIsNoClock(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	old := Session{Status: StatusRunning, StartedAt: now.Add(-48 * time.Hour).Unix(),
		UpdatedAt: now.Add(-48 * time.Hour).Unix()}
	fresh := Session{Status: StatusRunning, StartedAt: now.Unix(), UpdatedAt: now.Unix()}

	if done, why := (clocks{idle: time.Hour}).over(old, now); !done || !strings.Contains(why, "no activity") {
		t.Fatalf("with no ceiling an idle session reported %v %q, want an idle reap", done, why)
	}
	if done, _ := (clocks{idle: time.Hour}).over(fresh, now); done {
		t.Fatalf("with no ceiling a fresh session was reaped")
	}
	if done, why := (clocks{maxLife: 24 * time.Hour}).over(old, now); !done || !strings.Contains(why, "past the") {
		t.Fatalf("with no idle allowance an old session reported %v %q, want the ceiling", done, why)
	}
	if done, _ := (clocks{maxLife: 24 * time.Hour}).over(
		Session{Status: StatusRunning, StartedAt: now.Add(-9 * time.Hour).Unix(),
			UpdatedAt: now.Add(-9 * time.Hour).Unix()}, now); done {
		t.Fatalf("with no idle allowance a nine-hour-quiet session was reaped anyway")
	}
}

// ---- the pass ----

// TestAnIdleSessionIsReapedAndStopsAccruing is the leak this file exists to close,
// end to end: a run whose client stopped existing is ended, and the meter's span
// closes with it instead of billing $0.08 an hour forever.
//
// MUTATION: delete the stopOne call in reap and the row stays `running`, the two
// later ticks bill two more hours, and the assertion on b.len() names it.
func TestAnIdleSessionIsReapedAndStopsAccruing(t *testing.T) {
	st, sto := meterStore(t)
	b, ctx := &books{}, context.Background()
	now := time.Now().Unix()
	start := now - 3*hour
	plant(t, sto, "sess_leaked", StatusRunning, start)

	// One ordinary hour of accrual while nobody suspected anything.
	sweep(t, st, start+hour, cloud.RuntimeHourMicros, b)
	billedWhileOpen := b.len()

	reap(ctx, st, &record{}, testClocks)

	x := reload(t, sto, "sess_leaked")
	if x.EndedAt == 0 {
		t.Fatalf("a session idle for three hours was not ended")
	}
	if !isTerminalStatus(x.Status) {
		t.Fatalf("a reaped session is %q, which is still live", x.Status)
	}

	// The tail, then two ticks long after it. The tail is charged once; the ticks
	// after it charge nothing, which is what "stops accruing" means.
	sweep(t, st, x.EndedAt+hour, cloud.RuntimeHourMicros, b)
	tail := b.len()
	sweep(t, st, x.EndedAt+5*hour, cloud.RuntimeHourMicros, b)
	sweep(t, st, x.EndedAt+9*hour, cloud.RuntimeHourMicros, b)

	if tail != billedWhileOpen+1 {
		t.Fatalf("the tail produced %d debits, want exactly one", tail-billedWhileOpen)
	}
	if b.len() != tail {
		t.Fatalf("a reaped session billed %d more times after it ended", b.len()-tail)
	}
	if want := cloud.RuntimeCost(cloud.RuntimeHourMicros, x.EndedAt-start); b.micros() != want {
		t.Fatalf("a session alive for %ds billed %d µ$, want %d", x.EndedAt-start, b.micros(), want)
	}
}

// TestAReapSaysWhyOnTheSessionItself — the reason reaches the person whose run
// ended, not only the operator's log. It rides the same control event a stop does,
// so a surface that already handles a stop needs no new code to handle this.
//
// MUTATION: pass "" for why in reap and the payload's message is empty — a run that
// vanished with no explanation, which is the complaint this whole file answers.
func TestAReapSaysWhyOnTheSessionItself(t *testing.T) {
	st, sto := meterStore(t)
	ctx := context.Background()
	start := time.Now().Unix() - 3*hour
	plant(t, sto, "sess_leaked", StatusRunning, start)

	log := &record{}
	reap(ctx, st, log, testClocks)

	e, ok, err := sto.LastEvent(ctx, "acme", "sess_leaked")
	if err != nil || !ok {
		t.Fatalf("last event: %v (found=%v)", err, ok)
	}
	if e.Kind != KindControl {
		t.Fatalf("the reap wrote a %q event, want %q", e.Kind, KindControl)
	}
	var p controlPayload
	if err := json.Unmarshal([]byte(e.Payload), &p); err != nil {
		t.Fatalf("payload: %v", err)
	}
	if p.Command != CmdStop {
		t.Fatalf("the control command is %q, want %q", p.Command, CmdStop)
	}
	if !strings.Contains(p.Message, "no activity") {
		t.Fatalf("the session was told %q, which does not say why it ended", p.Message)
	}
	if len(log.infos()) != 1 {
		t.Fatalf("the reap was reported %d times, want once", len(log.infos()))
	}
	if w := log.warns(); len(w) != 0 {
		t.Fatalf("an ordinary reap warned: %v", w)
	}
}

// TestABusySessionIsNeverReaped is the other half of correctness, and the more
// expensive one to get wrong: reaping a working run destroys work, where leaving a
// leaked one costs money. A run that reported a minute ago is untouched however
// long it has been going.
//
// MUTATION: make over ignore StatusPaused and read only the elapsed life, and both
// of these are ended.
func TestABusySessionIsNeverReaped(t *testing.T) {
	st, sto := meterStore(t)
	ctx := context.Background()
	now := time.Now().Unix()

	// Started nine hours ago, said something a minute ago.
	busy := plant(t, sto, "sess_busy", StatusRunning, now-9*hour)
	busy.UpdatedAt = now - 60
	if err := sto.UpdateSession(ctx, busy); err != nil {
		t.Fatalf("stamp activity: %v", err)
	}
	// Paused five hours ago by somebody who went to lunch.
	plant(t, sto, "sess_paused", StatusPaused, now-5*hour)

	reap(ctx, st, &record{}, testClocks)

	for _, id := range []string{"sess_busy", "sess_paused"} {
		if x := reload(t, sto, id); x.EndedAt != 0 || isTerminalStatus(x.Status) {
			t.Fatalf("%s was reaped: status=%q endedAt=%d", id, x.Status, x.EndedAt)
		}
	}
}

// TestTheCeilingReachesEveryLiveSession — the backstop, and it is what makes
// leaving paused out of the idle rule safe. A run still reporting every minute, and
// a pause nobody came back to, both end at the ceiling.
//
// MUTATION: return early from over for a non-running status BEFORE the max-life
// branch and the paused row here lives forever.
func TestTheCeilingReachesEveryLiveSession(t *testing.T) {
	st, sto := meterStore(t)
	ctx := context.Background()
	now := time.Now().Unix()

	busy := plant(t, sto, "sess_marathon", StatusRunning, now-30*hour)
	busy.UpdatedAt = now - 60
	if err := sto.UpdateSession(ctx, busy); err != nil {
		t.Fatalf("stamp activity: %v", err)
	}
	plant(t, sto, "sess_forgotten", StatusPaused, now-30*hour)

	reap(ctx, st, &record{}, testClocks)

	for _, id := range []string{"sess_marathon", "sess_forgotten"} {
		x := reload(t, sto, id)
		if x.EndedAt == 0 || !isTerminalStatus(x.Status) {
			t.Fatalf("%s outlived the ceiling: status=%q endedAt=%d", id, x.Status, x.EndedAt)
		}
	}
}

// TestTheReaperOnlyReadsLiveSessions — a session that already ended is not in the
// set at all, so a reap cannot rewrite an end somebody else recorded, and the pass
// does not grow with an org's history.
//
// MUTATION: drop `ended_at=0` from Store.Live and every session the org has ever
// held comes back — the count below is 2 — so the pass grows with history instead
// of with what is running.
func TestTheReaperOnlyReadsLiveSessions(t *testing.T) {
	st, sto := meterStore(t)
	ctx := context.Background()
	now := time.Now().Unix()

	done := plant(t, sto, "sess_done", StatusRunning, now-9*hour)
	done.Status, done.EndedAt, done.UpdatedAt = StatusDone, now-8*hour, now-8*hour
	if err := sto.UpdateSession(ctx, done); err != nil {
		t.Fatalf("close: %v", err)
	}
	plant(t, sto, "sess_leaked", StatusRunning, now-9*hour)

	live, err := sto.Live(ctx, "acme")
	if err != nil {
		t.Fatalf("live: %v", err)
	}
	if len(live) != 1 || live[0].ID != "sess_leaked" {
		t.Fatalf("Live returned %d rows (%v), want only the open one", len(live), idsOf(live))
	}

	reap(ctx, st, &record{}, testClocks)

	if x := reload(t, sto, "sess_done"); x.EndedAt != now-8*hour {
		t.Fatalf("the reap moved an already-recorded end from %d to %d", now-8*hour, x.EndedAt)
	}
}

func idsOf(xs []Session) []string {
	out := make([]string, 0, len(xs))
	for _, x := range xs {
		out = append(out, x.ID)
	}
	return out
}
