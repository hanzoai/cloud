package coding

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/apps/agents"
	"github.com/zap-proto/zip"
)

// fakeRouter records every routed run handed to the Route client.
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
	mu       sync.Mutex
	seen     [][2]string
	dirs     []string
	readOnly []bool
	err      error
	called   int
}

func (g *fakeGate) gate(_ context.Context, org, target, dir string, readOnly bool) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.called++
	g.seen = append(g.seen, [2]string{org, target})
	g.dirs = append(g.dirs, dir)
	g.readOnly = append(g.readOnly, readOnly)
	return g.err
}

// routedDispatcher builds a Dispatcher wired for routing. The Runner is present
// but MUST NOT be called on a routed run.
func routedDispatcher(sess *fakeSessions, run *fakeRunner, router *fakeRouter, gate *fakeGate) (Dispatcher, *[]string) {
	var cloneCalls []string
	d := Dispatcher{
		Sessions: sess, PR: &fakePR{}, Runner: run,
		CloneURL: func(_ context.Context, org, actor, repo string) string {
			cloneCalls = append(cloneCalls, actor+"@"+org+"/"+repo)
			return "https://git.test/v1/git/" + org + "/" + repo + ".git"
		},
		Route:      router.route,
		TargetGate: gate.gate,
	}
	return d, &cloneCalls
}

func routedReq() Req {
	// Deliberately NO credential — a routed run must not need one. It DOES need an
	// actor: a routed run acts on a repository too, and the machine it is handed
	// to holds credentials broader than the caller's, so the entitlement is the
	// one thing both paths share.
	return Req{Org: "acme", UserID: "u-1", Actor: "zoe", AgentRef: "hanzo",
		Repo: "api", Prompt: "add a test", TargetID: "tgt_evo"}
}

// A ROUTED RUN WITH NO ACTOR IS REFUSED, and nothing is enqueued.
//
// This is the confused deputy the credential gate did not cover: the sandbox
// path needed an actor to be handed a key, and the routed path needed none at
// all — so naming a repository you cannot read was enough to have a machine that
// CAN read it clone the repository and stream it into your session.
func TestRun_Routed_RefusesWithNoActor(t *testing.T) {
	sess := &fakeSessions{id: "sess_noactor"}
	router := &fakeRouter{}
	gate := &fakeGate{}
	d, cloneCalls := routedDispatcher(sess, &fakeRunner{}, router, gate)

	req := routedReq()
	req.Actor = ""
	res := d.Run(context.Background(), req)

	if res.OK {
		t.Fatalf("a routed run with no forge identity was accepted: %+v", res)
	}
	if router.called != 0 {
		t.Fatal("a run with no entitlement was enqueued to a machine")
	}
	if len(*cloneCalls) != 0 {
		t.Fatalf("the repository was resolved for a caller with no identity: %v", *cloneCalls)
	}
	if len(sess.opened) != 0 {
		t.Fatal("a session was opened for a refused run")
	}
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
	if run.gotOrg != "" || run.gotReq.Remote != "" {
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
	if len(*cloneCalls) != 1 || (*cloneCalls)[0] != "zoe@acme/api" {
		t.Fatalf("clone must target acme/api only: %v", *cloneCalls)
	}
	// A routed session stays live (the machine drives it to terminal) — not closed here.
	if len(sess.closes) != 0 {
		t.Fatalf("a queued routed run must not be closed at dispatch: %+v", sess.closes)
	}
}

// A ROUTED RUN'S BUDGET IS CAPPED at routedBudget, four hours, whatever a sandbox run may be given. It
// rides to the machine and to the durable engine's activity deadline, so a caller naming a week would
// hold a routed slot and a mailbox offer for a week; the run carries the bound instead, and the
// engine's deadline follows from what it carries. The bound did not move when the sandbox run's did.
//
// MUTATION: bound a routed run by ceiling and its slot is held for a day.
func TestRun_Routed_TimeoutIsCappedAtTheRoutedBudget(t *testing.T) {
	if routedBudget != 4*time.Hour || routedBudget >= ceiling() {
		t.Fatalf("a routed run may be given %v and a sandbox run %v: the routed bound is not the old four hours", routedBudget, ceiling())
	}
	budget := int(routedBudget.Seconds())
	for _, c := range []struct{ asked, want int }{
		{0, budget},
		{-5, budget},
		{60, 60},
		{budget, budget},
		{budget + 1, budget},
		{10 * 86400, budget},
	} {
		sess := &fakeSessions{id: "sess_route1abc"}
		router := &fakeRouter{}
		d, _ := routedDispatcher(sess, &fakeRunner{}, router, &fakeGate{})
		req := routedReq()
		req.TimeoutSeconds = c.asked
		if res := d.Run(context.Background(), req); !res.OK {
			t.Fatalf("asked %d: the routed run was not accepted: %s", c.asked, res.Error)
		}
		if got := router.runs[0].TimeoutSeconds; got != c.want {
			t.Errorf("asked %d: the routed run carries %ds, want %ds", c.asked, got, c.want)
		}
	}
	if got, bound := routedStartToClose(10*86400), claimDeadline+routedBudget; got != bound {
		t.Fatalf("ten days' timeout gives the engine a %s deadline, want %s", got, bound)
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
	req := Req{Org: "acme", UserID: "u-1", AgentRef: "hanzo", Repo: "api", Prompt: "fix it",
		Remote: "git@git.test:acme/api.git", Key: "k", Known: "git.test ssh-ed25519 AAAAPIN"}

	res := d.Run(context.Background(), req)

	if res.Routed {
		t.Fatalf("a run with no target must NOT be routed: %+v", res)
	}
	if !res.OK || run.gotOrg != "acme" {
		t.Fatalf("the local runner must execute the default path: %+v", res)
	}
	if router.called != 0 || gate.called != 0 {
		t.Fatalf("routing clients must be untouched on the local path: route=%d gate=%d", router.called, gate.called)
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
	req.Key, req.Remote = "", "" // explicitly none
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
	// No Route / TargetGate clients.
	d := Dispatcher{Sessions: sess, PR: &fakePR{}, Runner: run,
		CloneURL: func(_ context.Context, org, actor, repo string) string {
			return "https://git.test/v1/git/" + org + "/" + repo + ".git"
		}}

	res := d.Run(context.Background(), routedReq())
	if res.OK || !strings.Contains(res.Error, "routing is not available") {
		t.Fatalf("unwired routing must fail closed, got %+v", res)
	}
	if run.gotOrg != "" {
		t.Fatal("must not run locally when routing is unwired but a target was chosen")
	}
}

// ---- routed completion parity (#48 I2): verify + PR + session close ----

func finalizeDispatcher(sess *fakeSessions, tr *fakePR, verifyOK bool) Dispatcher {
	return Dispatcher{
		Sessions: sess, PR: tr,
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
	tr := &fakePR{ref: PRRef{Identifier: "API-9"}}
	d := finalizeDispatcher(sess, tr, true)
	in := RoutedRun{Org: "acme", SessionID: "sess_r", Repo: "api", Base: "main", Branch: "agent/r", Prompt: "add a test", Actor: "u-1", AgentRef: "hanzo", Pull: true}

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
	tr := &fakePR{}
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
	tr := &fakePR{}
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
	tr := &fakePR{}
	verifyCalled := false
	d := Dispatcher{Sessions: sess, PR: tr, VerifyRef: func(context.Context, string, string, string) (string, bool) {
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

// NewDispatcher wires the routed completion client, so the durable delivery activity
// reaches this dispatcher's verify/PR/session clients. Without it, a routed run's
// session would never close.
func TestNewDispatcher_WiresRoutedFinalizer(t *testing.T) {
	prev := routedFinalizer
	t.Cleanup(func() { routedFinalizer = prev })
	routedFinalizer = nil
	_ = NewDispatcher(nil)
	if routedFinalizer == nil {
		t.Fatal("NewDispatcher must wire the routed completion client (else routed sessions never close)")
	}
}

// finalizeRoutedDurable bridges the durable agents types to coding's without dropping
// the attribution the completion needs.
func TestFinalizeRoutedDurable_BridgesFields(t *testing.T) {
	sess := &fakeSessions{}
	tr := &fakePR{ref: PRRef{Identifier: "API-1"}}
	d := finalizeDispatcher(sess, tr, true)
	d.finalizeRoutedDurable(context.Background(),
		agents.RoutedRun{Org: "acme", SessionID: "s", Repo: "api", Branch: "agent/b", AgentRef: "hanzo", Actor: "u-9", Pull: true},
		agents.RoutedResult{OK: true, Changed: true, Branch: "agent/b", CommitSha: "beef"})
	if len(tr.inputs) != 1 || tr.inputs[0].Assignee != "hanzo" || tr.inputs[0].Head != "agent/b" {
		t.Fatalf("bridge dropped attribution: %+v", tr.inputs)
	}
	if len(sess.closes) != 1 || sess.closes[0].status != statusDone {
		t.Fatalf("bridge must drive the session to done: %+v", sess.closes)
	}
}

// callerSessions refuses a call with no org, as the plane's session client does.
type callerSessions struct{ *fakeSessions }

func (c callerSessions) Log(ctx context.Context, org, session, kind, actor string, payload []byte) error {
	if zip.CallerOf(ctx).Org == "" {
		return zip.ErrForbidden("agents: this op needs the org on the call")
	}
	return c.fakeSessions.Log(ctx, org, session, kind, actor, payload)
}

func (c callerSessions) Close(ctx context.Context, org, session, status string, errStr ...string) error {
	if zip.CallerOf(ctx).Org == "" {
		return zip.ErrForbidden("agents: this op needs the org on the call")
	}
	return c.fakeSessions.Close(ctx, org, session, status, errStr...)
}

// The durable engine ends a routed run on a context that names nobody; the completion
// states its person's org itself, or the session is never logged to or closed.
func TestFinalizeRoutedStatesTheOrgOfTheCall(t *testing.T) {
	for name, res := range map[string]RoutedResult{
		"no changes": {OK: true, Changed: false},
		"a failure":  {OK: false, Error: "the agent crashed"},
	} {
		t.Run(name, func(t *testing.T) {
			fake := &fakeSessions{}
			d := Dispatcher{Sessions: callerSessions{fake}, PR: &fakePR{}}
			d.finalizeRouted(context.Background(), RoutedRun{Org: "acme", SessionID: "s", Repo: "api", Actor: "acme/z"}, res)
			if len(fake.closes) != 1 {
				t.Fatalf("the session was closed %d times, want once: %+v", len(fake.closes), fake.closes)
			}
			if len(fake.events) == 0 {
				t.Fatal("the completion logged nothing to the session")
			}
		})
	}
}

// A run in a workspace the platform made needs no actor and no address: the machine
// holds no credential there, and the run's own key names the address when it is offered.
func TestRun_Routed_InAMadeWorkspaceNeedsNoActorAndNoAddress(t *testing.T) {
	for name, tc := range map[string]struct {
		mode string
		read bool
	}{"a build": {modeBuild, false}, "a plan": {modePlan, true}} {
		t.Run(name, func(t *testing.T) {
			router := &fakeRouter{}
			d, cloneCalls := routedDispatcher(&fakeSessions{id: "sess_made"}, &fakeRunner{}, router, &fakeGate{})

			req := routedReq()
			req.Actor, req.Made, req.Mode = "", true, tc.mode
			res := d.Run(context.Background(), req)

			if !res.OK || !res.Routed {
				t.Fatalf("a routed run in a made workspace was refused: %+v", res)
			}
			if len(*cloneCalls) != 0 {
				t.Fatalf("an address was resolved for a run whose own key names it: %v", *cloneCalls)
			}
			if got := router.runs[0]; !got.Made || got.Read != tc.read || got.CloneURL != "" {
				t.Fatalf("enqueued %+v, want Made, Read=%v and no address", got, tc.read)
			}
		})
	}
}

// A run that needs a key and is refused one is failed and its session closed, not
// offered to a machine without it and not retried.
func TestDeliverRoutedRun_FailsARunTheForgeWillNotGrant(t *testing.T) {
	prevGrant, prevFinal := routedGranter, routedFinalizer
	t.Cleanup(func() { routedGranter, routedFinalizer = prevGrant, prevFinal })
	routedGranter = func(context.Context, agents.RoutedRun) (*agents.RoutedGrant, func(), error) {
		return nil, nil, errors.New("no key for you")
	}
	var finished agents.RoutedResult
	routedFinalizer = func(_ context.Context, _ agents.RoutedRun, res agents.RoutedResult) { finished = res }

	res, err := DeliverRoutedRunActivity(context.Background(), agents.RoutedRun{Org: "acme", SessionID: "sess_g", Made: true})
	if err != nil {
		t.Fatalf("a refused grant is a failed run, not an activity to retry: %v", err)
	}
	if res.OK || !strings.Contains(res.Error, "no key for you") || finished.Error != res.Error {
		t.Fatalf("result %+v, completion %+v", res, finished)
	}
}

// The durable engine hands the delivery activity a context that names nobody, but
// the grant a run on a GitHub mirror needs is minted over the plane, where the org
// travels on the caller and never as an argument. Without the run's org on that
// call the mint answers "no org on the call" and the run fails before it is offered.
// The person the dispatch validated must survive the same hop.
func TestDeliverRoutedRun_GivesTheGrantTheRunsOrgAndActor(t *testing.T) {
	prevGrant, prevFinal := routedGranter, routedFinalizer
	t.Cleanup(func() { routedGranter, routedFinalizer = prevGrant, prevFinal })
	routedFinalizer = func(context.Context, agents.RoutedRun, agents.RoutedResult) {}
	var seen zip.Caller
	called := false
	routedGranter = func(ctx context.Context, in agents.RoutedRun) (*agents.RoutedGrant, func(), error) {
		called = true
		seen = zip.CallerOf(ctx)
		return nil, nil, errors.New("stop here") // end the activity without waiting on a machine
	}
	in := agents.RoutedRun{Org: "acme", Actor: "acme/u-1", SessionID: "sess_g", Made: true}
	DeliverRoutedRunActivity(context.Background(), in)
	if !called {
		t.Fatal("the run was never offered its repository access")
	}
	if seen.Org != "acme" {
		t.Fatalf("grant called with org %q, want %q (the run's validated tenant)", seen.Org, in.Org)
	}
	if seen.User != "acme/u-1" {
		t.Fatalf("grant called with user %q, want %q", seen.User, in.Actor)
	}
}

// The key is withdrawn when the run ends, here by the deadline with nobody claiming it.
func TestDeliverRoutedRun_WithdrawsTheKeyWhenTheRunEnds(t *testing.T) {
	prev := routedGranter
	t.Cleanup(func() { routedGranter = prev })
	withdrawn := false
	routedGranter = func(context.Context, agents.RoutedRun) (*agents.RoutedGrant, func(), error) {
		return &agents.RoutedGrant{Remote: "ssh://git/acme/api.git", Key: "k", Known: "h"}, func() { withdrawn = true }, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := DeliverRoutedRunActivity(ctx, agents.RoutedRun{Org: "acme", TargetID: "tgt", SessionID: "sess_w", Made: true}); err == nil {
		t.Fatal("a run nobody claimed was reported complete")
	}
	if !withdrawn {
		t.Fatal("the run ended and its key was left")
	}
}

// A read-only routed run carries the flag from Req to Route: the gate is asked
// with it and the machine receives it, while the deploy-key Read flag stays false.
func TestRun_RoutedReadOnly_CarriesToGateAndRoute(t *testing.T) {
	sess := &fakeSessions{id: "sess_ro_route"}
	router := &fakeRouter{}
	gate := &fakeGate{}
	d, _ := routedDispatcher(sess, &fakeRunner{}, router, gate)

	req := routedReq()
	req.ReadOnly = true
	res := d.Run(context.Background(), req)

	if !res.Routed || !res.OK {
		t.Fatalf("a read-only routed run was refused: %+v", res)
	}
	if len(gate.readOnly) != 1 || !gate.readOnly[0] {
		t.Fatalf("the gate was not asked with readOnly: %+v", gate.readOnly)
	}
	if len(router.runs) != 1 || !router.runs[0].ReadOnly {
		t.Fatalf("the machine did not receive readOnly: %+v", router.runs)
	}
}

// A read-only run that reports no changes is an ANSWER: closed done, no PR and no
// ref verification. One that reports changes is refused — it did what it was told
// not to — and gets no push, PR or "done" either.
func TestFinalizeRouted_ReadOnly_AnswersOnly(t *testing.T) {
	sess := &fakeSessions{}
	tr := &fakePR{}
	verifyCalled := false
	d := Dispatcher{Sessions: sess, PR: tr, VerifyRef: func(context.Context, string, string, string) (string, bool) {
		verifyCalled = true
		return "cafe", true
	}}
	in := RoutedRun{Org: "acme", SessionID: "s", Repo: "api", ReadOnly: true}
	d.finalizeRouted(context.Background(), in, RoutedResult{OK: true, Changed: false})
	if verifyCalled || len(tr.inputs) != 0 || len(sess.closes) != 1 || sess.closes[0].status != statusDone {
		t.Fatalf("a read-only answer was pushed or not closed done: verify=%v pr=%d closes=%+v", verifyCalled, len(tr.inputs), sess.closes)
	}

	sess = &fakeSessions{}
	verifyCalled = false
	d = Dispatcher{Sessions: sess, PR: tr, VerifyRef: func(context.Context, string, string, string) (string, bool) {
		verifyCalled = true
		return "cafe", true
	}}
	d.finalizeRouted(context.Background(), in, RoutedResult{OK: true, Changed: true, CommitSha: "cafe"})
	if verifyCalled || len(tr.inputs) != 0 || len(sess.closes) != 1 || sess.closes[0].status != statusError {
		t.Fatalf("a read-only run that wrote was accepted: verify=%v pr=%d closes=%+v", verifyCalled, len(tr.inputs), sess.closes)
	}
}

// A completion in a made workspace that cannot read the workspace closes the session
// in error rather than verifying the branch in the wrong namespace.
func TestFinalizeRouted_AMadeWorkspaceThatCannotBeReadFailsTheRun(t *testing.T) {
	sess := &fakeSessions{}
	d := finalizeDispatcher(sess, &fakePR{}, true)
	d.finalizeRouted(context.Background(), RoutedRun{Org: "acme", SessionID: "s", Repo: "api", Branch: "agent/s", Made: true},
		RoutedResult{OK: true, Changed: true, CommitSha: "cafe"})
	if len(sess.closes) != 1 || sess.closes[0].status != statusError {
		t.Fatalf("closes = %+v, want one error", sess.closes)
	}
}
