package coding

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/hanzoai/cloud/clients/agents"
)

// fakeRouter records every routed run handed to the Route seam.
type fakeRouter struct {
	mu     sync.Mutex
	runs   []RoutedRun
	err    error
	called int
}

func (f *fakeRouter) route(_ context.Context, run RoutedRun) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.called++
	f.runs = append(f.runs, run)
	return f.err
}

// fakeGate records the (org,target) it was asked to admit and returns a verdict.
type fakeGate struct {
	mu     sync.Mutex
	seen   [][2]string
	err    error
	called int
}

func (g *fakeGate) gate(_ context.Context, org, target string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.called++
	g.seen = append(g.seen, [2]string{org, target})
	return g.err
}

// routedDispatcher builds a Dispatcher wired for routing. The Runner is present
// but MUST NOT be called on a routed run.
func routedDispatcher(sess *fakeSessions, run *fakeRunner, router *fakeRouter, gate *fakeGate) (Dispatcher, *[]string) {
	var cloneCalls []string
	d := Dispatcher{
		Sessions: sess, Tracker: &fakeTracker{}, Runner: run,
		CloneURL: func(org, repo string) string {
			cloneCalls = append(cloneCalls, org+"/"+repo)
			return "https://git.test/v1/git/" + org + "/" + repo + ".git"
		},
		Route:      router.route,
		TargetGate: gate.gate,
	}
	return d, &cloneCalls
}

func routedReq() Req {
	// Deliberately NO credential — a routed run must not need one.
	return Req{Org: "acme", UserID: "u-1", AgentRef: "hanzo", Repo: "api", Prompt: "add a test", TargetID: "tgt_evo"}
}

// A routed run is ENQUEUED to the target and NEVER executed in the sandbox.
func TestRun_RoutedToTarget_EnqueuesNotLocal(t *testing.T) {
	sess := &fakeSessions{id: "sess_route1abc"}
	run := &fakeRunner{}
	router := &fakeRouter{}
	gate := &fakeGate{}
	d, cloneCalls := routedDispatcher(sess, run, router, gate)

	res := d.Run(context.Background(), routedReq())

	if !res.Routed || !res.OK || res.TargetID != "tgt_evo" {
		t.Fatalf("want routed+accepted to tgt_evo, got %+v", res)
	}
	// The local sandbox runner was NEVER invoked.
	if run.gotOrg != "" || run.gotReq.CloneURL != "" {
		t.Fatalf("the local runner must not run for a routed dispatch: %+v", run.gotReq)
	}
	// The gate was consulted for exactly this (org,target).
	if gate.called != 1 || gate.seen[0] != [2]string{"acme", "tgt_evo"} {
		t.Fatalf("liveness gate wrong: %+v", gate.seen)
	}
	// One durable enqueue, addressed to the target, org-scoped clone url, NO secret.
	if router.called != 1 {
		t.Fatalf("want exactly one enqueue, got %d", router.called)
	}
	rr := router.runs[0]
	if rr.Org != "acme" || rr.TargetID != "tgt_evo" || rr.SessionID != "sess_route1abc" {
		t.Fatalf("routed run mis-addressed: %+v", rr)
	}
	if rr.CloneURL != "https://git.test/v1/git/acme/api.git" {
		t.Fatalf("clone url must be org-scoped: %q", rr.CloneURL)
	}
	if rr.Branch != "agent/route1abc" {
		t.Fatalf("branch derived from session id: %q", rr.Branch)
	}
	// The session was opened ON the target so mission-control shows it there.
	if len(sess.opened) != 1 || sess.opened[0].target != "tgt_evo" {
		t.Fatalf("session must be opened on the target: %+v", sess.opened)
	}
	if len(*cloneCalls) != 1 || (*cloneCalls)[0] != "acme/api" {
		t.Fatalf("clone must target acme/api only: %v", *cloneCalls)
	}
	// A routed session stays live (the machine drives it to terminal) — not closed here.
	if len(sess.closes) != 0 {
		t.Fatalf("a queued routed run must not be closed at dispatch: %+v", sess.closes)
	}
}

// The DEFAULT (no target) path is entirely unchanged: the local runner executes,
// the router is never touched.
func TestRun_NoTarget_LocalPathUnchanged(t *testing.T) {
	sess := &fakeSessions{id: "sess_local1"}
	run := &fakeRunner{result: RunResult{Branch: "agent/local1", Changed: true, OK: true}}
	router := &fakeRouter{}
	gate := &fakeGate{}
	d, _ := routedDispatcher(sess, run, router, gate)
	// A local run DOES need a credential.
	req := Req{Org: "acme", UserID: "u-1", AgentRef: "hanzo", Repo: "api", Prompt: "fix it", CredToken: "hk-secret"}

	res := d.Run(context.Background(), req)

	if res.Routed {
		t.Fatalf("a run with no target must NOT be routed: %+v", res)
	}
	if !res.OK || run.gotOrg != "acme" {
		t.Fatalf("the local runner must execute the default path: %+v", res)
	}
	if router.called != 0 || gate.called != 0 {
		t.Fatalf("routing seams must be untouched on the local path: route=%d gate=%d", router.called, gate.called)
	}
	// The session was opened WITHOUT a target (local open path).
	if len(sess.opened) != 1 || sess.opened[0].target != "" {
		t.Fatalf("local session must open with no target: %+v", sess.opened)
	}
}

// A dead / stale / unknown target fails closed at dispatch — nothing is enqueued,
// the runner is never called, and no session is opened.
func TestRun_RoutedDeadTarget_FailsClosed(t *testing.T) {
	sess := &fakeSessions{id: "sess_x"}
	run := &fakeRunner{}
	router := &fakeRouter{}
	gate := &fakeGate{err: errors.New("target has no live runner")}
	d, _ := routedDispatcher(sess, run, router, gate)

	res := d.Run(context.Background(), routedReq())

	if res.OK || !strings.Contains(res.Error, "not available") {
		t.Fatalf("a dead target must fail closed, got %+v", res)
	}
	if router.called != 0 {
		t.Fatal("nothing may be enqueued to a dead target")
	}
	if run.gotOrg != "" {
		t.Fatal("the local runner must NEVER run as a fallback for a dead target")
	}
	if len(sess.opened) != 0 {
		t.Fatalf("no session should open for an unavailable target: %+v", sess.opened)
	}
}

// If the durable enqueue fails, the run fails closed and the (already-open)
// session is closed ERROR — never left a zombie and never run locally.
func TestRun_RoutedEnqueueFails_FailsClosed_SessionErrored(t *testing.T) {
	sess := &fakeSessions{id: "sess_enq"}
	run := &fakeRunner{}
	router := &fakeRouter{err: errors.New("engine not ready")}
	gate := &fakeGate{}
	d, _ := routedDispatcher(sess, run, router, gate)

	res := d.Run(context.Background(), routedReq())

	if res.OK || !strings.Contains(res.Error, "queue the routed run") {
		t.Fatalf("a failed enqueue must fail closed, got %+v", res)
	}
	if run.gotOrg != "" {
		t.Fatal("a failed enqueue must NOT fall back to local execution")
	}
	if len(sess.closes) != 1 || sess.closes[0].status != statusError {
		t.Fatalf("the session must be closed error, got %+v", sess.closes)
	}
}

// A routed run needs NO credential (branches before the credential gate) and the
// routed run carries no secret field at all.
func TestRun_Routed_NeedsNoCredential_CarriesNoSecret(t *testing.T) {
	sess := &fakeSessions{id: "sess_nocred"}
	router := &fakeRouter{}
	gate := &fakeGate{}
	d, _ := routedDispatcher(sess, &fakeRunner{}, router, gate)

	req := routedReq()
	req.CredToken = "" // explicitly none
	res := d.Run(context.Background(), req)

	if !res.OK || !res.Routed {
		t.Fatalf("routed run must succeed without a credential, got %+v", res)
	}
	// No event payload carries anything credential-shaped; the RoutedRun struct has
	// no credential field, so this is structural — assert the enqueued run + events.
	rr := router.runs[0]
	if rr.Prompt != "add a test" {
		t.Fatalf("routed run prompt wrong: %+v", rr)
	}
	for _, e := range sess.events {
		if e.org != "acme" || e.session != "sess_nocred" {
			t.Fatalf("routed event escaped tenant/session scope: %+v", e)
		}
	}
}

// Routing that is not wired must fail closed — never fall through to the sandbox
// with a target set.
func TestRun_RoutedButRoutingUnwired_FailsClosed(t *testing.T) {
	sess := &fakeSessions{id: "sess_x"}
	run := &fakeRunner{}
	// No Route / TargetGate seams.
	d := Dispatcher{Sessions: sess, Tracker: &fakeTracker{}, Runner: run,
		CloneURL: func(org, repo string) string { return "https://git.test/v1/git/" + org + "/" + repo + ".git" }}

	res := d.Run(context.Background(), routedReq())
	if res.OK || !strings.Contains(res.Error, "routing is not available") {
		t.Fatalf("unwired routing must fail closed, got %+v", res)
	}
	if run.gotOrg != "" {
		t.Fatal("must not run locally when routing is unwired but a target was chosen")
	}
}

// ---- routed completion parity (#48 I2): verify + PR + session close ----

func finalizeDispatcher(sess *fakeSessions, tr *fakeTracker, verifyOK bool) Dispatcher {
	return Dispatcher{
		Sessions: sess, Tracker: tr,
		VerifyRef: func(_ context.Context, _, _, _ string) (string, bool) {
			if verifyOK {
				return "verifiedsha", true
			}
			return "", false
		},
	}
}

// A routed run that reported CHANGES and whose branch verifies gets the SAME cloud-
// side completion as the local path: the PR is filed and the session is closed done.
func TestFinalizeRouted_ChangedVerifyPasses_FilesPR_ClosesDone(t *testing.T) {
	sess := &fakeSessions{}
	tr := &fakeTracker{ref: PRRef{Identifier: "API-9"}}
	d := finalizeDispatcher(sess, tr, true)
	in := RoutedRun{Org: "acme", SessionID: "sess_r", Repo: "api", Base: "main", Branch: "agent/r", Prompt: "add a test", Actor: "u-1", AgentRef: "hanzo"}

	d.finalizeRouted(context.Background(), in, RoutedResult{OK: true, Changed: true, Branch: "agent/r", CommitSha: "cafe", Diffstat: "1 file changed"})

	if len(tr.inputs) != 1 {
		t.Fatalf("a verified changed run must file exactly one PR, got %d", len(tr.inputs))
	}
	pr := tr.inputs[0]
	if pr.Org != "acme" || pr.Repo != "api" || pr.Head != "agent/r" || pr.Assignee != "hanzo" {
		t.Fatalf("routed PR mis-filed: %+v", pr)
	}
	// The verified tip from OUR storage wins over the machine's self-report.
	if !strings.Contains(pr.Body, "verifiedsha") {
		t.Fatalf("PR body should carry the verified tip: %q", pr.Body)
	}
	if len(sess.closes) != 1 || sess.closes[0].org != "acme" || sess.closes[0].status != statusDone {
		t.Fatalf("routed session must close done, got %+v", sess.closes)
	}
	// No event carries a secret-shaped field (structural: RoutedRun has none).
	for _, e := range sess.events {
		if e.org != "acme" || e.session != "sess_r" {
			t.Fatalf("routed completion event escaped tenant/session scope: %+v", e)
		}
	}
}

// A routed run whose branch does NOT verify fails closed: no PR, session closed error
// — trust the tips we can read, not the machine's self-report.
func TestFinalizeRouted_VerifyFails_NoPR_ClosesError(t *testing.T) {
	sess := &fakeSessions{}
	tr := &fakeTracker{}
	d := finalizeDispatcher(sess, tr, false) // verify fails
	in := RoutedRun{Org: "acme", SessionID: "s", Repo: "api", Branch: "agent/r"}

	d.finalizeRouted(context.Background(), in, RoutedResult{OK: true, Changed: true, Branch: "agent/r", CommitSha: "cafe"})

	if len(tr.inputs) != 0 {
		t.Fatalf("a routed run whose branch did not land must file no PR, got %d", len(tr.inputs))
	}
	if len(sess.closes) != 1 || sess.closes[0].status != statusError {
		t.Fatalf("verify-fail must close the session error, got %+v", sess.closes)
	}
}

// A routed run that reported NO changes closes the session done with no PR.
func TestFinalizeRouted_NoChanges_NoPR_ClosesDone(t *testing.T) {
	sess := &fakeSessions{}
	tr := &fakeTracker{}
	d := finalizeDispatcher(sess, tr, true)
	d.finalizeRouted(context.Background(), RoutedRun{Org: "acme", SessionID: "s", Repo: "api"}, RoutedResult{OK: true, Changed: false})

	if len(tr.inputs) != 0 {
		t.Fatalf("no-changes must file no PR, got %d", len(tr.inputs))
	}
	if len(sess.closes) != 1 || sess.closes[0].status != statusDone {
		t.Fatalf("no-changes must close done, got %+v", sess.closes)
	}
}

// A routed run the machine reported as FAILED closes the session error, no PR — and
// even VerifyRef is never consulted (there is nothing to verify).
func TestFinalizeRouted_ReportedError_ClosesError_NoPR(t *testing.T) {
	sess := &fakeSessions{}
	tr := &fakeTracker{}
	verifyCalled := false
	d := Dispatcher{Sessions: sess, Tracker: tr, VerifyRef: func(context.Context, string, string, string) (string, bool) {
		verifyCalled = true
		return "", true
	}}
	d.finalizeRouted(context.Background(), RoutedRun{Org: "acme", SessionID: "s", Repo: "api"}, RoutedResult{OK: false, Error: "the agent crashed"})

	if len(tr.inputs) != 0 {
		t.Fatalf("a failed routed run must file no PR, got %d", len(tr.inputs))
	}
	if verifyCalled {
		t.Fatal("a failed run has nothing to verify — VerifyRef must not run")
	}
	if len(sess.closes) != 1 || sess.closes[0].status != statusError {
		t.Fatalf("a failed routed run must close error, got %+v", sess.closes)
	}
	// The machine's error text reaches the session, never lost.
	sawErr := false
	for _, e := range sess.events {
		if strings.Contains(e.payload, "the agent crashed") {
			sawErr = true
		}
	}
	if !sawErr {
		t.Fatalf("the reported error must be mirrored into the session: %+v", sess.events)
	}
}

// NewDispatcher wires the routed completion seam, so the durable delivery activity
// reaches this dispatcher's verify/PR/session seams. Without it, a routed run's
// session would never close.
func TestNewDispatcher_WiresRoutedFinalizer(t *testing.T) {
	prev := routedFinalizer
	t.Cleanup(func() { routedFinalizer = prev })
	routedFinalizer = nil
	_ = NewDispatcher(
		func(_, _ string) string { return "https://git.test" },
		func(context.Context, string, string, string) (string, bool) { return "", true },
		nil,
	)
	if routedFinalizer == nil {
		t.Fatal("NewDispatcher must wire the routed completion seam (else routed sessions never close)")
	}
}

// finalizeRoutedDurable bridges the durable agents types to coding's without dropping
// the attribution the completion needs.
func TestFinalizeRoutedDurable_BridgesFields(t *testing.T) {
	sess := &fakeSessions{}
	tr := &fakeTracker{ref: PRRef{Identifier: "API-1"}}
	d := finalizeDispatcher(sess, tr, true)
	d.finalizeRoutedDurable(context.Background(),
		agents.RoutedRun{Org: "acme", SessionID: "s", Repo: "api", Branch: "agent/b", AgentRef: "hanzo", Actor: "u-9"},
		agents.RoutedResult{OK: true, Changed: true, Branch: "agent/b", CommitSha: "beef"})
	if len(tr.inputs) != 1 || tr.inputs[0].Assignee != "hanzo" || tr.inputs[0].Head != "agent/b" {
		t.Fatalf("bridge dropped attribution: %+v", tr.inputs)
	}
	if len(sess.closes) != 1 || sess.closes[0].status != statusDone {
		t.Fatalf("bridge must drive the session to done: %+v", sess.closes)
	}
}
