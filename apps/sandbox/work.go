package sandbox

// work.go — what a sandbox is DOING, while it is still doing it.
//
// A command used to be a box with two ends: post an argv, and some minutes later
// receive everything the program had said. For a twenty-five minute agentic run
// that is a blank screen with a verdict at the end, and nothing anywhere could
// interrupt it. Two things fix that, and they are one fact seen twice — a command
// in flight is ADDRESSABLE:
//
//	tell   its output is appended to the session watching it AS IT IS PRODUCED,
//	       so every surface reading that session watches the work happen
//	work   its cancel is held under the sandbox's id, so a caller can stop it
//
// NARRATION GOES WHERE EVERY OTHER RUN'S NARRATION ALREADY GOES. apps/agents owns
// the fleet's live run feed — one durable ordered event log per session, fanned
// out to GET /v1/agent/sessions/stream — and that is the ONE place a surface
// watches a run. This appends to it over the internal plane. It invents no second
// feed, and it does not touch POST /v1/event, which warehouses product events for
// the webhooks engine and has no live tail to read at all.
//
// THE ORG IS THE ONE THE PLANE PROVED. A caller names a session and never a
// tenant, so a command can only ever narrate into its own org's session: a
// session id belonging to somebody else is simply not present in the org this
// call acts for, and the append is refused on that side.

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud/plane"
)

// tellEvery is the shortest gap between two appends.
//
// A session's log is a durable ordered record, not a byte pipe, and a build
// prints thousands of lines: without a floor, one `npm install` would write a row
// per line and every watcher would spend its whole budget rendering a scroll
// nobody reads. Coalesced, a reader sees the latest work about once a second,
// which is as often as a person can take it in.
const tellEvery = time.Second

// tellCap bounds ONE appended chunk, and keeps the TAIL rather than the head. The
// far side refuses a payload over 64 KiB whole, so a burst has to be cut here —
// and what a program said most recently is what says why it is stuck.
const tellCap = 8 << 10

// tellPatience bounds one append. This call sits ON the path the pod's output
// takes, so a wedged reader must cost the command a moment and never its life.
const tellPatience = 5 * time.Second

// line is one narration event's body, in the vocabulary a session's log already
// speaks: `step` names a phase, `message` carries what it said. It is exactly
// what apps/coding mirrors for a coding run, so a reader that renders one renders
// this with no new case to learn.
type line struct {
	Step    string `json:"step,omitempty"`
	Message string `json:"message,omitempty"`
}

// tell appends a command's output to the session watching it.
//
// It is an io.Writer so the exec channel writes THROUGH it: the same bytes that
// fill the result buffer pass here on the way, and nothing is read twice. Both of
// a command's streams share one tell, which is why it locks — the exec channel
// writes stdout and stderr from different goroutines.
type tell struct {
	org, session string
	// blind redacts what the caller said must never be published. It is applied
	// HERE, at the moment a line becomes an event, because this is the writer the
	// bytes leave by — see [blinder] and plane.RunIn.Blind.
	blind *blinder

	mu   sync.Mutex
	buf  []byte
	at   time.Time
	dead bool
}

// newTell returns the sink for one command, or nil when no session was named. A
// nil *tell's methods are no-ops, so no caller has to branch on being watched.
func newTell(org, session string, blind *blinder) *tell {
	if strings.TrimSpace(org) == "" || strings.TrimSpace(session) == "" {
		return nil
	}
	return &tell{org: org, session: session, blind: blind}
}

func (t *tell) Write(p []byte) (int, error) {
	if t == nil {
		return len(p), nil
	}
	t.mu.Lock()
	t.buf = append(t.buf, p...)
	chunk := t.take(time.Now(), false)
	t.mu.Unlock()
	if chunk != "" {
		t.say("log", line{Message: chunk})
	}
	// The bytes are always CONSUMED. A short write aborts the exec stream, and a
	// stalled narration must never be able to kill the command it is narrating.
	return len(p), nil
}

// done flushes what is left and says how the command ended.
//
// It is the last thing a watcher hears, and it is said even when the command was
// stopped — a run that vanishes mid-sentence is the failure this file exists to
// remove. The exit event carries NO status, deliberately: `done` and `error` are
// the two words that end a watcher's progress line, and a command exiting is not
// the run ending.
func (t *tell) done(code int, err error) {
	if t == nil {
		return
	}
	t.mu.Lock()
	rest := t.take(time.Now(), true)
	t.mu.Unlock()
	if rest != "" {
		t.say("log", line{Message: rest})
	}
	t.say("tool-call", line{Step: "exit", Message: ending(code, err)})
}

// take answers the bytes to send now, or "" when it is not yet their turn. force
// sends whatever is left whatever the clock says — the end of a command must not
// lose its last words to a rate limit. Called under the lock.
func (t *tell) take(now time.Time, force bool) string {
	if t.dead || len(t.buf) == 0 {
		return ""
	}
	if !force && now.Sub(t.at) < tellEvery {
		return ""
	}
	b := t.buf
	// REDACT FIRST, THEN CUT — the order is the whole of it.
	//
	// The stream is chopped by a CLOCK, not by content, so a program that writes
	// half a key, waits, and writes the rest defeats a fixed-string replacement
	// with neither half matching. Cutting first and redacting the pieces does not
	// fix that: it just moves the split, and the emitted piece can be all but one
	// byte of the secret.
	//
	// So the whole buffer is hidden first, which replaces every COMPLETE secret in
	// it. The only thing that can still be in the clear is a secret straddling the
	// END of the buffer, and that is at most one byte short of the longest one —
	// so holding back exactly that many bytes retains it whole for the next
	// flush, where the rest of it will have arrived.
	s := t.blind.hide(string(b))
	// TRUNCATE AFTER HIDING, for the same reason the cut below happens after it:
	// dropping the head of the buffer first would discard the front of a secret
	// straddling that boundary and emit its tail in the clear. Hidden first, every
	// complete secret is already a marker, so the cut can only land in ordinary
	// text or in one.
	if len(s) > tellCap {
		s = s[len(s)-tellCap:]
	}
	keep := 0
	if !force {
		if keep = t.blind.carry(); keep > tellCap/2 {
			keep = tellCap / 2
		}
		if keep > len(s) {
			keep = len(s)
		}
	}
	// A forced flush keeps nothing: `done` is a watcher's last word and must not
	// be swallowed by the carry-over.
	t.buf, t.at = append(t.buf[:0], s[len(s)-keep:]...), now
	return s[:len(s)-keep]
}

// say appends one event to the session, best-effort.
//
// A narration failure must never fail the command it narrates — the work is real
// and the commentary is not — so a failed append RETIRES this tell and the
// command runs on in silence rather than paying the timeout again per chunk.
func (t *tell) say(kind string, l line) {
	t.mu.Lock()
	dead := t.dead
	t.mu.Unlock()
	if dead {
		return
	}
	l.Message, l.Step = t.blind.hide(l.Message), t.blind.hide(l.Step)
	payload, err := json.Marshal(l)
	if err != nil {
		return
	}
	// Belt AND braces, on the ENCODED form: a secret containing a quote, a
	// backslash or a newline is re-spelled by the JSON encoder, so the escaped
	// form can survive a replacement made on the plain one.
	payload = []byte(t.blind.hide(string(payload)))
	// A DETACHED, TENANT-STATED context. The command's own may already be
	// cancelled — a stop is exactly that case — and the last thing a stopped run
	// says is the part a watcher most needs. plane.For supplies the org where
	// there is no request behind the context, which a fresh Background is.
	ctx, cancel := context.WithTimeout(plane.For(context.Background(), t.org), tellPatience)
	defer cancel()
	if _, err := plane.Ask[plane.SessionEventIn, plane.CodingAck](ctx, "agent", plane.AgentsSessionEvent,
		&plane.SessionEventIn{Org: t.org, SessionID: t.session, Kind: kind, Payload: payload}); err != nil {
		t.mu.Lock()
		t.dead = true
		t.mu.Unlock()
	}
}

// ending is how a command's last line reads. A transport failure is named as
// itself rather than folded into an exit code, because "the program returned 1"
// and "we lost the channel" are different facts and a watcher acts on them
// differently.
func ending(code int, err error) string {
	if err != nil {
		return "ended: " + err.Error()
	}
	return "exit " + strconv.Itoa(code)
}

// ─────────────────────────────────────────────────────────────────────────────
// The interrupt
// ─────────────────────────────────────────────────────────────────────────────

// work is every command this process has in flight, held by the sandbox it runs
// in, so a caller can reach one and stop it.
//
// IT IS PROCESS-LOCAL, and that is a bound with a stated cost rather than an
// oversight. A cancel belongs to a live goroutine, so it exists only where the
// exec stream does; cloud-api runs one replica (universe:
// charts/app/values/hanzo/cloud.yaml, replicas: 1), so today a stop always
// reaches the process holding the command. The day that number changes, a stop
// that lands elsewhere honestly reports interrupting nothing — it never claims a
// kill it did not perform, which is the property worth having when an assumption
// stops holding.
type work struct {
	mu   sync.Mutex
	next int
	in   map[string]map[int]context.CancelFunc
}

func newWork() *work { return &work{in: map[string]map[int]context.CancelFunc{}} }

// start records one command's cancel under its sandbox and answers the func that
// forgets it. Every start is paired with that func or the set grows a dead cancel
// per command — which is why the caller defers it beside the cancel itself.
func (w *work) start(sandbox string, stop context.CancelFunc) func() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.next++
	id := w.next
	if w.in[sandbox] == nil {
		w.in[sandbox] = map[int]context.CancelFunc{}
	}
	w.in[sandbox][id] = stop
	return func() {
		w.mu.Lock()
		defer w.mu.Unlock()
		delete(w.in[sandbox], id)
		if len(w.in[sandbox]) == 0 {
			delete(w.in, sandbox)
		}
	}
}

// stop cancels every command running in one sandbox and answers how many it
// interrupted. Cancelling ends the exec stream, and the kubelet kills the process
// on the far side of a closed channel — the same thing that happens when a person
// interrupts `kubectl exec`.
//
// The cancels run OUTSIDE the lock: each unwinds a command, whose own deferred
// work reaches back here to forget it, and a stop holding the lock while that
// happened would wait on itself.
func (w *work) stop(sandbox string) int {
	w.mu.Lock()
	stops := make([]context.CancelFunc, 0, len(w.in[sandbox]))
	for _, c := range w.in[sandbox] {
		stops = append(stops, c)
	}
	w.mu.Unlock()
	for _, c := range stops {
		c()
	}
	return len(stops)
}
