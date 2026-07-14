package coding

import (
	"context"
	"strings"
	"sync"
	"testing"
)

// ---- fakes recording every seam call for isolation + contract assertions ----

type openCall struct{ org, actor, agent, title string }
type eventCall struct {
	org, session, kind, actor string
	payload                   string
}
type closeCall struct{ org, session, status string }

type fakeSessions struct {
	mu      sync.Mutex
	id      string
	openErr error
	opened  []openCall
	events  []eventCall
	closes  []closeCall
}

func (f *fakeSessions) Open(_ context.Context, org, actor, agent, title string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.opened = append(f.opened, openCall{org, actor, agent, title})
	if f.openErr != nil {
		return "", f.openErr
	}
	return f.id, nil
}
func (f *fakeSessions) Log(_ context.Context, org, session, kind, actor string, payload []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, eventCall{org, session, kind, actor, string(payload)})
	return nil
}
func (f *fakeSessions) Close(_ context.Context, org, session, status string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closes = append(f.closes, closeCall{org, session, status})
	return nil
}

type fakeTracker struct {
	mu     sync.Mutex
	inputs []PRInput
	ref    PRRef
	err    error
}

func (f *fakeTracker) CreatePR(_ context.Context, in PRInput) (PRRef, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inputs = append(f.inputs, in)
	return f.ref, f.err
}

type fakeRunner struct {
	mu      sync.Mutex
	gotReq  RunRequest
	gotOrg  string
	gotUser string
	steps   []Step // steps to emit before returning
	result  RunResult
	err     error
}

func (f *fakeRunner) Run(_ context.Context, org, userID string, req RunRequest, onStep func(Step)) (RunResult, error) {
	f.mu.Lock()
	f.gotReq, f.gotOrg, f.gotUser = req, org, userID
	steps := f.steps
	f.mu.Unlock()
	for _, s := range steps {
		onStep(s)
	}
	return f.result, f.err
}

// dispatcherFor builds a Dispatcher whose git seams are org-aware fakes: CloneURL
// echoes the (org, repo) so a test can assert the sandbox is pointed only at the
// caller's namespace; VerifyRef succeeds for a configured branch.
func dispatcherFor(sess *fakeSessions, tr *fakeTracker, run *fakeRunner, verifyOK bool) (Dispatcher, *[]string) {
	var cloneCalls []string
	d := Dispatcher{
		Sessions: sess, Tracker: tr, Runner: run,
		CloneURL: func(org, repo string) string {
			cloneCalls = append(cloneCalls, org+"/"+repo)
			return "https://git.test/v1/git/" + org + "/" + repo + ".git"
		},
		VerifyRef: func(_ context.Context, _, _, branch string) (string, bool) {
			if verifyOK {
				return "verifiedsha", true
			}
			return "", false
		},
	}
	return d, &cloneCalls
}

const secretToken = "hk-SUPERSECRETcredential-value"

func baseReq() Req {
	return Req{
		Org: "acme", UserID: "u-123", AgentRef: "hanzo", Repo: "api",
		Prompt: "fix the null deref in handler", CredUser: "x-access-token", CredToken: secretToken,
	}
}

// ---- the crown-jewel tests ----

func TestRun_HappyPath_PushVerifyPR_NoCredentialLeak(t *testing.T) {
	sess := &fakeSessions{id: "sess_abc123def456"}
	tr := &fakeTracker{ref: PRRef{Identifier: "API-1", ProjectKey: "API", Number: 1}}
	run := &fakeRunner{
		steps:  []Step{{Type: "step", Step: "clone", Status: "ok"}, {Type: "log", Message: "editing handler.go"}, {Type: "step", Step: "push", Status: "ok"}},
		result: RunResult{Branch: "agent/abc123def456", CommitSha: "deadbeef", Diffstat: "1 file changed", Changed: true, OK: true},
	}
	d, cloneCalls := dispatcherFor(sess, tr, run, true)

	res := d.Run(context.Background(), baseReq())

	if !res.OK || !res.Changed || !res.Verified {
		t.Fatalf("want OK+Changed+Verified, got %+v", res)
	}
	if res.PR.Identifier != "API-1" {
		t.Fatalf("want PR API-1, got %q", res.PR.Identifier)
	}
	if res.CommitSha != "verifiedsha" {
		t.Fatalf("commit should be the authoritative verified tip, got %q", res.CommitSha)
	}
	// Clone URL pointed ONLY at the caller's org/repo.
	if len(*cloneCalls) != 1 || (*cloneCalls)[0] != "acme/api" {
		t.Fatalf("clone must target acme/api only, got %v", *cloneCalls)
	}
	// Runner received the caller's org + credential.
	if run.gotOrg != "acme" || run.gotUser != "u-123" || run.gotReq.CredToken != secretToken {
		t.Fatalf("runner req wrong: org=%q user=%q credSet=%v", run.gotOrg, run.gotUser, run.gotReq.CredToken != "")
	}
	// Session lifecycle: opened once, closed done.
	if len(sess.opened) != 1 || sess.opened[0].org != "acme" || sess.opened[0].agent != "hanzo" {
		t.Fatalf("open wrong: %+v", sess.opened)
	}
	if len(sess.closes) != 1 || sess.closes[0].status != statusDone {
		t.Fatalf("want one done close, got %+v", sess.closes)
	}
	// PR fields correct.
	if len(tr.inputs) != 1 {
		t.Fatalf("want one PR create, got %d", len(tr.inputs))
	}
	pr := tr.inputs[0]
	if pr.Org != "acme" || pr.Repo != "api" || pr.Head != "agent/abc123def456" || pr.Assignee != "hanzo" {
		t.Fatalf("PR input wrong: %+v", pr)
	}

	// CREDENTIAL NEVER LEAKS: no session event payload nor the PR body carries the token.
	for _, e := range sess.events {
		if strings.Contains(e.payload, "SUPERSECRET") {
			t.Fatalf("credential leaked into session event %q: %s", e.kind, e.payload)
		}
		// every event is scoped to the caller org + the opened session
		if e.org != "acme" || e.session != "sess_abc123def456" {
			t.Fatalf("event escaped tenant/session scope: %+v", e)
		}
	}
	if strings.Contains(pr.Body, "SUPERSECRET") || strings.Contains(pr.Title, "SUPERSECRET") {
		t.Fatalf("credential leaked into PR row: %+v", pr)
	}
}

func TestRun_EventVocabulary_StepIsToolCall_LogIsLog(t *testing.T) {
	sess := &fakeSessions{id: "sess_x"}
	run := &fakeRunner{
		steps:  []Step{{Type: "step", Step: "clone"}, {Type: "log", Message: "hi"}},
		result: RunResult{Branch: "agent/x", Changed: true, OK: true},
	}
	d, _ := dispatcherFor(sess, &fakeTracker{}, run, true)
	d.Run(context.Background(), baseReq())

	var toolCalls, logs, statuses int
	for _, e := range sess.events {
		switch e.kind {
		case kindToolCall:
			toolCalls++
		case kindLog:
			logs++
		case kindStatus:
			statuses++
		default:
			t.Fatalf("unexpected event kind %q", e.kind)
		}
	}
	if toolCalls != 1 || logs != 1 {
		t.Fatalf("want step->tool-call(1) and log->log(1), got tool=%d log=%d", toolCalls, logs)
	}
	if statuses < 2 { // started + done at minimum
		t.Fatalf("want at least started+done status events, got %d", statuses)
	}
}

func TestRun_NoChanges_NoPR_DoneNotError(t *testing.T) {
	sess := &fakeSessions{id: "sess_x"}
	tr := &fakeTracker{}
	run := &fakeRunner{result: RunResult{Changed: false, OK: true}}
	d, _ := dispatcherFor(sess, tr, run, true)

	res := d.Run(context.Background(), baseReq())
	if !res.OK || res.Changed {
		t.Fatalf("want OK && !Changed, got %+v", res)
	}
	if len(tr.inputs) != 0 {
		t.Fatalf("no PR should be filed when nothing changed, got %d", len(tr.inputs))
	}
	if len(sess.closes) != 1 || sess.closes[0].status != statusDone {
		t.Fatalf("want done close, got %+v", sess.closes)
	}
}

func TestRun_RunnerError_ClosesError_NoPR(t *testing.T) {
	sess := &fakeSessions{id: "sess_x"}
	tr := &fakeTracker{}
	run := &fakeRunner{err: context.DeadlineExceeded}
	d, _ := dispatcherFor(sess, tr, run, true)

	res := d.Run(context.Background(), baseReq())
	if res.OK || res.Error == "" {
		t.Fatalf("want failure, got %+v", res)
	}
	if len(tr.inputs) != 0 {
		t.Fatalf("no PR on failure")
	}
	if len(sess.closes) != 1 || sess.closes[0].status != statusError {
		t.Fatalf("want error close, got %+v", sess.closes)
	}
}

func TestRun_VerifyRefFails_FailsClosed_NoPR(t *testing.T) {
	sess := &fakeSessions{id: "sess_x"}
	tr := &fakeTracker{}
	run := &fakeRunner{result: RunResult{Branch: "agent/x", Changed: true, OK: true}}
	d, _ := dispatcherFor(sess, tr, run, false) // verify fails

	res := d.Run(context.Background(), baseReq())
	if res.OK || !strings.Contains(res.Error, "not found in native git") {
		t.Fatalf("a claimed-but-absent push must fail closed, got %+v", res)
	}
	if len(tr.inputs) != 0 {
		t.Fatalf("no PR when the branch didn't land")
	}
	if len(sess.closes) != 1 || sess.closes[0].status != statusError {
		t.Fatalf("want error close, got %+v", sess.closes)
	}
}

func TestRun_MissingCredential_FailsBeforeOpeningSession(t *testing.T) {
	sess := &fakeSessions{id: "sess_x"}
	d, _ := dispatcherFor(sess, &fakeTracker{}, &fakeRunner{}, true)

	req := baseReq()
	req.CredToken = ""
	res := d.Run(context.Background(), req)
	if res.OK || !strings.Contains(res.Error, "credential") {
		t.Fatalf("want credential fail-closed, got %+v", res)
	}
	if len(sess.opened) != 0 {
		t.Fatalf("must not open a session without a credential")
	}
}

func TestRun_MissingRepoOrPrompt_FailsClosed(t *testing.T) {
	d, _ := dispatcherFor(&fakeSessions{id: "s"}, &fakeTracker{}, &fakeRunner{}, true)
	if r := d.Run(context.Background(), Req{Org: "acme", CredToken: "x", Prompt: "do it"}); r.OK || r.Error == "" {
		t.Fatalf("missing repo must fail: %+v", r)
	}
	if r := d.Run(context.Background(), Req{Org: "acme", Repo: "api", CredToken: "x"}); r.OK || r.Error == "" {
		t.Fatalf("missing prompt must fail: %+v", r)
	}
}

func TestRun_TrackerFailure_DoesNotFailRun(t *testing.T) {
	sess := &fakeSessions{id: "sess_x"}
	tr := &fakeTracker{err: context.Canceled}
	run := &fakeRunner{result: RunResult{Branch: "agent/x", Changed: true, OK: true}}
	d, _ := dispatcherFor(sess, tr, run, true)

	res := d.Run(context.Background(), baseReq())
	if !res.OK { // branch is pushed+verified; a PR-row failure is a side-effect, not a run failure
		t.Fatalf("tracker failure must not fail the run, got %+v", res)
	}
	if res.PR.Identifier != "" {
		t.Fatalf("no PR ref when tracker failed")
	}
	if sess.closes[0].status != statusDone {
		t.Fatalf("run still done despite tracker failure")
	}
}
