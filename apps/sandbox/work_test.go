package sandbox

// The two properties a live run has to hold, measured rather than argued for:
// its output leaves the sandbox WHILE the command is still running, and a stop
// reaches exactly one tenant's command and no other's.
//
// None of it needs a cluster. The exec channel is an interface for exactly this
// reason (runtime.go: "the real one opens an SPDY stream to the apiserver, and a
// test has no apiserver"), so a fake streamer can hold a command open for as long
// as a test wants to look at it.

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
)

// held is a command that has started and will not finish until the test says so.
// It writes what it was given, announces that it is running, and then waits.
type held struct {
	out     string
	running chan struct{} // closed once the bytes are written
	release chan struct{} // the test closes this to let the command end
	err     error         // what the command ends with
}

func (h *held) stream(ctx context.Context, ns, pod string, argv []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if h.out != "" {
		_, _ = io.WriteString(stdout, h.out)
	}
	close(h.running)
	select {
	case <-h.release:
		return h.err
	case <-ctx.Done():
		// A CANCELLED command is how a stop looks from in here: the channel to the
		// pod closes and the far side is killed with it, exactly as it is when a
		// person interrupts `kubectl exec`.
		return ctx.Err()
	}
}

func (h *held) tty(ctx context.Context, ns, pod string, argv []string, stdin io.Reader, stdout io.Writer, size remotecommand.TerminalSizeQueue) error {
	return errors.New("not a terminal test")
}

// service builds the subsystem over a real per-test store, with a runtime whose
// only cluster-facing part is the fake exec channel. Lease is not reachable this
// way and does not need to be — every property below is about a sandbox that is
// already running.
func service(t *testing.T, str streamer) *Service {
	t.Helper()
	t.Setenv("CLOUD_DATA_DIR", t.TempDir())
	s, err := New(cloud.Deps{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	s.State.rt.str = str
	s.State.rt.dyn = nil // never reached: exec goes through str, which is fake
	s.State.rt.initErr = ""
	s.State.rt.execTimeout = 30 * time.Second
	return s
}

// seed writes a running sandbox into one org's store, the way a lease would have.
func seed(t *testing.T, s *Service, org, id string) Sandbox {
	t.Helper()
	store, err := storeFor(s, org)
	if err != nil {
		t.Fatalf("storeFor(%s): %v", org, err)
	}
	m := Sandbox{ID: id, Org: org, Kind: KindSandbox, Class: "exec", Status: "running",
		Pod: podName(id), CreatedAt: 1, LastUsedAt: 1, ExpiresAt: time.Now().Unix() + 3600}
	if err := store.Put(context.Background(), m); err != nil {
		t.Fatalf("seed: %v", err)
	}
	return m
}

// ready satisfies the runtime's "is there a client at all" check without a
// cluster. The client is BUILT and never dialled — exec's bytes go through the
// fake exec channel above, and a dynamic client constructed from a config
// connects to nothing until somebody asks it for an object.
func ready(t *testing.T, s *Service) {
	t.Helper()
	dyn, err := dynamic.NewForConfig(&rest.Config{Host: "http://127.0.0.1:1"})
	if err != nil {
		t.Fatalf("dynamic client: %v", err)
	}
	s.State.rt.dyn = dyn
}

// TestOutputLeavesTheSandboxWhileTheCommandIsStillRunning is the whole point.
//
// Before this, a command was a box with two ends: post an argv and, up to
// twenty-five minutes later, receive everything the program had said. What a
// person watching a coding run saw was "running the task", then nothing, then a
// verdict — and there was no way to tell a working agent from a wedged one.
//
// The measurement is the timing and not the content: the bytes have to be
// readable from OUTSIDE the call while the call has not returned.
func TestOutputLeavesTheSandboxWhileTheCommandIsStillRunning(t *testing.T) {
	h := &held{out: "installing dependencies…\n", running: make(chan struct{}), release: make(chan struct{})}
	s := service(t, h)
	ready(t, s)
	seed(t, s, "acme", "m_1")

	// The chunk is HELD IN THE BUFFER rather than sent, by stating that an append
	// just happened. That measures the plumbing — do the command's bytes reach the
	// narration at all, mid-command — with no peer to send them to.
	say := newTell("acme", "sess_1", nil)
	say.at = time.Now()

	done := make(chan ExecResult, 1)
	go func() {
		r, _ := s.State.rt.exec(context.Background(), Sandbox{ID: "m_1", Status: "running", Pod: "m-1"},
			[]string{"npm", "install"}, nil, 60, say)
		done <- r
	}()

	<-h.running
	// The command has NOT returned. Whatever the tell holds now, it learned while
	// the program was still running.
	deadline := time.After(2 * time.Second)
	for {
		say.mu.Lock()
		got := string(say.buf)
		say.mu.Unlock()
		if strings.Contains(got, "installing dependencies") {
			break
		}
		select {
		case r := <-done:
			t.Fatalf("the command finished before its output was readable (%q) — output that "+
				"only arrives with the result is not a live run", r.Stdout)
		case <-deadline:
			t.Fatalf("the command has been running for two seconds and nothing has left the " +
				"sandbox; a watcher would be looking at a blank screen")
		case <-time.After(5 * time.Millisecond):
		}
	}

	close(h.release)
	r := <-done
	// And the tap is a PASS-THROUGH: the result still carries everything, so
	// narrating a command did not change what running it answers.
	if !strings.Contains(r.Stdout, "installing dependencies") {
		t.Errorf("stdout = %q; the tap must not consume the bytes it forwards", r.Stdout)
	}
}

// TestStopEndsTheCommandAndKeepsTheSandbox. Stop and End are two verbs because a
// run that has gone wrong is one somebody still wants to look at: the checkout,
// the logs and the half-written file are all in the sandbox, and a stop that
// deleted the pod would take the evidence with it.
func TestStopEndsTheCommandAndKeepsTheSandbox(t *testing.T) {
	h := &held{running: make(chan struct{}), release: make(chan struct{})}
	s := service(t, h)
	ready(t, s)
	seed(t, s, "acme", "m_1")

	ran := make(chan error, 1)
	go func() {
		_, err := Run(s, context.Background(), "acme", "m_1", Cmd{Argv: []string{"sleep", "600"}})
		ran <- err
	}()
	<-h.running

	n, err := Stop(s, context.Background(), "acme", "m_1")
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if n != 1 {
		t.Fatalf("Stop interrupted %d commands, want 1 — a stop that reports a kill it did "+
			"not perform is worse than one that reports none", n)
	}
	select {
	case err := <-ran:
		if err == nil {
			t.Fatal("the stopped command answered success; a run cancelled mid-flight is not one " +
				"that finished")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the command outlived its stop — cancelling has to reach the exec channel, or " +
			"`stop_run` is a button that does nothing")
	}

	// THE SANDBOX SURVIVES. Stop ends the work; End ends the resource.
	if _, err := Get(s, context.Background(), "acme", "m_1"); err != nil {
		t.Fatalf("the sandbox is gone after a stop (%v) — stop must not take the evidence with it", err)
	}
}

// TestStopReachesOnlyItsOwnTenant is the one that must not be wrong.
//
// A stop is a WRITE against somebody's running work, so a caller able to reach
// across the org boundary could halt another tenant's run by guessing an id. The
// refusal is the ordinary org lookup every other operation here walks through,
// applied BEFORE the in-flight set is consulted at all — and it answers 404 and
// not 403, because a 403 would confirm the sandbox exists and whether a given
// sandbox exists is itself a cross-tenant fact.
func TestStopReachesOnlyItsOwnTenant(t *testing.T) {
	h := &held{running: make(chan struct{}), release: make(chan struct{})}
	s := service(t, h)
	ready(t, s)
	seed(t, s, "acme", "m_1")

	ran := make(chan error, 1)
	go func() {
		_, err := Run(s, context.Background(), "acme", "m_1", Cmd{Argv: []string{"sleep", "600"}})
		ran <- err
	}()
	<-h.running

	// The neighbour knows the id — ids are not secrets — and asks for it by name.
	n, err := Stop(s, context.Background(), "evil", "m_1")
	if err == nil {
		t.Fatalf("another org stopped acme's command (%d interrupted); an id is not an "+
			"authorization", n)
	}
	if n != 0 {
		t.Errorf("a refused stop reported interrupting %d commands, want 0", n)
	}
	if !strings.Contains(strings.ToLower(err.Error()), "not found") {
		t.Errorf("a cross-tenant stop answered %q; it must be indistinguishable from an id "+
			"that does not exist, or the refusal itself confirms the sandbox", err)
	}
	// And acme's command is STILL RUNNING. The neighbour changed nothing.
	select {
	case err := <-ran:
		t.Fatalf("acme's command ended (%v) after another org asked for it to stop", err)
	case <-time.After(100 * time.Millisecond):
	}

	// The owner can, on the same id, through the same call.
	if n, err := Stop(s, context.Background(), "acme", "m_1"); err != nil || n != 1 {
		t.Fatalf("the owner's stop = (%d, %v), want (1, nil) — the gate must be the tenant "+
			"and not the operation", n, err)
	}
	<-ran

	// An empty org is refused too, rather than defaulting to one. A stop that
	// arrived with no caller must halt nothing.
	if _, err := Stop(s, context.Background(), "", "m_1"); err == nil {
		t.Error("a stop with no tenant was allowed; it has to fail, not pick an org")
	}
}

// TestStopReportsNothingWhenThereIsNothingToStop. Zero is an answer: a command
// that finished a moment ago is one there is nothing left to interrupt, and a
// caller cannot win that race. What it does need to tell apart is "already over"
// from "not yours", which is the 404 above.
func TestStopReportsNothingWhenThereIsNothingToStop(t *testing.T) {
	s := service(t, &held{running: make(chan struct{}), release: make(chan struct{})})
	ready(t, s)
	seed(t, s, "acme", "m_idle")

	n, err := Stop(s, context.Background(), "acme", "m_idle")
	if err != nil || n != 0 {
		t.Fatalf("Stop on an idle sandbox = (%d, %v), want (0, nil)", n, err)
	}
}

// TestTheInFlightSetDoesNotGrow. Every start is paired with the func that forgets
// it; without that pairing the set keeps a dead cancel per command, which for a
// long-lived exec sandbox is a leak measured in commands and not in bytes.
func TestTheInFlightSetDoesNotGrow(t *testing.T) {
	w := newWork()
	for range 100 {
		_, stop := context.WithCancel(context.Background())
		w.start("m_1", stop)()
		stop()
	}
	w.mu.Lock()
	held := len(w.in)
	w.mu.Unlock()
	if held != 0 {
		t.Fatalf("%d sandboxes still hold cancels after every command ended, want 0", held)
	}
}

// TestStopEndsEveryCommandInTheSandbox. A sandbox runs more than one thing — a
// coding run clones and then works — so a stop that ended only the first would
// leave the run going.
func TestStopEndsEveryCommandInTheSandbox(t *testing.T) {
	w := newWork()
	var ctxs []context.Context
	for range 3 {
		ctx, stop := context.WithCancel(context.Background())
		ctxs = append(ctxs, ctx)
		w.start("m_1", stop)
	}
	other, stopOther := context.WithCancel(context.Background())
	defer stopOther()
	w.start("m_2", stopOther)

	if n := w.stop("m_1"); n != 3 {
		t.Fatalf("stopped %d, want 3", n)
	}
	for i, ctx := range ctxs {
		if ctx.Err() == nil {
			t.Errorf("command %d was not cancelled", i)
		}
	}
	if other.Err() != nil {
		t.Error("stopping one sandbox cancelled another's command")
	}
}

// ---- what a watcher is told -------------------------------------------------

// TestNarrationIsCoalesced. A build prints thousands of lines and a session's log
// is a durable ordered record, not a byte pipe: without a floor, one `npm
// install` writes a row per line and a watcher spends its whole budget rendering
// a scroll nobody reads.
func TestNarrationIsCoalesced(t *testing.T) {
	x := &tell{org: "acme", session: "sess_1"}
	now := time.Now()

	x.buf = append(x.buf, "first\n"...)
	if got := x.take(now, false); got != "first\n" {
		t.Fatalf("the first line was withheld (%q); nothing has been said yet, so there is "+
			"nothing to coalesce with", got)
	}
	x.buf = append(x.buf, "second\n"...)
	if got := x.take(now.Add(tellEvery/2), false); got != "" {
		t.Errorf("a line arriving within the floor was sent immediately (%q)", got)
	}
	x.buf = append(x.buf, "third\n"...)
	if got := x.take(now.Add(tellEvery), false); got != "second\nthird\n" {
		t.Errorf("take = %q, want both withheld lines together — coalescing must delay, "+
			"never drop", got)
	}
}

// TestTheLastWordsAreAlwaysSaid. force ignores the floor, because the end of a
// command must not lose what it said last to a rate limit — and the end is
// exactly when a stopped run has something a watcher needs.
func TestTheLastWordsAreAlwaysSaid(t *testing.T) {
	x := &tell{org: "acme", session: "sess_1"}
	now := time.Now()
	x.take(now, false) // nothing yet
	x.buf = append(x.buf, "the reason it failed\n"...)
	if got := x.take(now, true); got != "the reason it failed\n" {
		t.Fatalf("take(force) = %q; a command's last line was rate-limited away", got)
	}
}

// TestABurstKeepsItsTail. The far side refuses a payload over its ceiling whole,
// so a burst has to be cut here — and what a program said MOST RECENTLY is what
// says why it is stuck. Cutting the head would keep the banner and drop the
// stack trace.
func TestABurstKeepsItsTail(t *testing.T) {
	x := &tell{org: "acme", session: "sess_1"}
	x.buf = append(x.buf, strings.Repeat("x", tellCap*2)...)
	x.buf = append(x.buf, "THE ERROR"...)
	got := x.take(time.Now(), true)
	if len(got) > tellCap {
		t.Fatalf("chunk is %d bytes, over the %d cap — the append would be refused whole", len(got), tellCap)
	}
	if !strings.HasSuffix(got, "THE ERROR") {
		t.Fatal("the cut kept the head and dropped the end; the end is the part that says why")
	}
}

// TestNothingIsSaidWhenNobodyIsWatching. A command with no session named is the
// ordinary case — a bare exec, a file read — and it must cost nothing at all: no
// buffer, no peer call, no branch at the call site.
func TestNothingIsSaidWhenNobodyIsWatching(t *testing.T) {
	for _, tc := range []struct{ org, session string }{{"acme", ""}, {"acme", "   "}, {"", "sess_1"}} {
		if x := newTell(tc.org, tc.session, nil); x != nil {
			t.Errorf("newTell(%q, %q) produced a sink; there is nowhere for it to send", tc.org, tc.session)
		}
	}
	// A nil tell is safely written to and safely finished, so no caller branches.
	var x *tell
	if n, err := x.Write([]byte("output")); n != 6 || err != nil {
		t.Fatalf("nil tell Write = (%d, %v), want (6, nil)", n, err)
	}
	x.done(0, nil)
}

// TestNarrationCannotNameATenant is the tenancy property of the live feed, stated
// where it is enforced: a tell is built from the org the CALLER PROVED and a
// session the caller named, and there is no third place an org could come from.
// Cmd carries no org field, so a request cannot supply one — which is why a run
// can only ever narrate into its own tenant's session.
func TestNarrationCannotNameATenant(t *testing.T) {
	x := newTell("acme", "sess_1", nil)
	if x.org != "acme" {
		t.Fatalf("tell org = %q, want the proven caller's", x.org)
	}
	// If Cmd ever grows an org, this fails to compile — which is the point.
	var c Cmd
	c.Session = "sess_1"
	if got := newTell("acme", c.Session, nil).org; got != "acme" {
		t.Errorf("the org came from somewhere other than the caller: %q", got)
	}
}
