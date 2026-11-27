package coding

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/hanzoai/cloud/client"
	"github.com/zap-proto/zip"
)

// ---- fakes recording every client call for isolation + contract assertions ----

type openCall struct{ org, actor, agent, title, target, conversation string }
type eventCall struct {
	org, session, kind, actor string
	payload                   string
}
type closeCall struct{ org, session, status, err string }

type fakeSessions struct {
	mu      sync.Mutex
	id      string
	openErr error
	opened  []openCall
	events  []eventCall
	closes  []closeCall
	tags    []Tag
	// busy is the sandboxes another live run holds: a tag claiming one is refused,
	// as the agents store refuses it (TagSession).
	busy map[string]bool
	// onTag, when set, runs after each tag lands: a test's hand on the moment a start
	// has told its session what it runs, which is the moment before it spawns the run.
	onTag func()
	// refuse, when set and it answers an error, is what the session store says to an event
	// in place of recording it.
	refuse func(kind, payload string) error
}

func (f *fakeSessions) Tag(_ context.Context, _, _ string, t Tag) error {
	f.mu.Lock()
	if f.busy[t.Sandbox] {
		f.mu.Unlock()
		return zip.ErrConflict("agents: another run is still working in that sandbox")
	}
	f.tags = append(f.tags, t)
	hook := f.onTag
	f.mu.Unlock()
	if hook != nil {
		hook()
	}
	return nil
}

// tagged is every fact the run told its session, folded the way the store folds
// them: a later non-empty field wins, an empty one leaves the earlier.
func (f *fakeSessions) tagged() Tag {
	f.mu.Lock()
	defer f.mu.Unlock()
	var t Tag
	for _, x := range f.tags {
		t.Kind = cmpOr(x.Kind, t.Kind)
		t.Repo = cmpOr(x.Repo, t.Repo)
		t.Base = cmpOr(x.Base, t.Base)
		t.Branch = cmpOr(x.Branch, t.Branch)
		t.Environment = cmpOr(x.Environment, t.Environment)
		t.PR = cmpOr(x.PR, t.PR)
		t.Mode = cmpOr(x.Mode, t.Mode)
		t.Sandbox = cmpOr(x.Sandbox, t.Sandbox)
		t.Project = cmpOr(x.Project, t.Project)
	}
	return t
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func (f *fakeSessions) Open(_ context.Context, org, actor, agent, title string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.opened = append(f.opened, openCall{org, actor, agent, title, "", ""})
	if f.openErr != nil {
		return "", f.openErr
	}
	return f.id, nil
}
func (f *fakeSessions) OpenOn(_ context.Context, org, actor, agent, title, target, conversation string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.opened = append(f.opened, openCall{org, actor, agent, title, target, conversation})
	if f.openErr != nil {
		return "", f.openErr
	}
	return f.id, nil
}
func (f *fakeSessions) Log(_ context.Context, org, session, kind, actor string, payload []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.refuse != nil {
		if err := f.refuse(kind, string(payload)); err != nil {
			return err
		}
	}
	f.events = append(f.events, eventCall{org, session, kind, actor, string(payload)})
	return nil
}
func (f *fakeSessions) Close(_ context.Context, org, session, status string, errStr ...string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	var errVal string
	if len(errStr) > 0 {
		errVal = errStr[0]
	}
	f.closes = append(f.closes, closeCall{org, session, status, errVal})
	return nil
}

type fakePR struct {
	mu     sync.Mutex
	inputs []PRInput
	ref    PRRef
	err    error
}

func (f *fakePR) Open(_ context.Context, in PRInput) (PRRef, error) {
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

// dispatcherFor builds a Dispatcher whose git clients are org-aware fakes: CloneURL
// echoes the (org, repo) — it backs the ROUTED path, where a customer's machine
// gets an address and brings its own credential; VerifyRef succeeds for a
// configured branch.
func dispatcherFor(sess *fakeSessions, tr *fakePR, run *fakeRunner, verifyOK bool) (Dispatcher, *[]string) {
	var cloneCalls []string
	d := Dispatcher{
		Sessions: sess, PR: tr, Runner: run,
		CloneURL: func(_ context.Context, org, actor, repo string) string {
			cloneCalls = append(cloneCalls, actor+"@"+org+"/"+repo)
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

const secretToken = "-----BEGIN OPENSSH PRIVATE KEY-----\nSUPERSECRETKEYMATERIAL\n-----END OPENSSH PRIVATE KEY-----"

// baseReq is what Start hands Run: the repository's SSH remote and the run's own
// key, resolved together from ONE grant, so they cannot disagree about which
// repository the run may write.
func baseReq() Req {
	return Req{
		Org: "acme", UserID: "u-123", AgentRef: "hanzo", Repo: "api",
		Prompt: "fix the null deref in handler",
		Remote: "git@git.test:acme/api.git", Key: secretToken, Known: "git.test ssh-ed25519 AAAAPIN",
		// The base request is one that asks for its pull request; a run asks for
		// none unless it says so (TestStart_ARunOpensNoPullRequestUnlessAskedTo).
		Pull: true,
	}
}

// ---- the crown-jewel tests ----

func TestRun_HappyPath_PushVerifyPR_NoCredentialLeak(t *testing.T) {
	sess := &fakeSessions{id: "sess_abc123def456"}
	tr := &fakePR{ref: PRRef{Identifier: "API-1", ProjectKey: "API", Number: 1}}
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
	// The sandbox is pointed ONLY at the caller's own repository, and at the one
	// address its key opens. It does not go through the CloneURL client at all —
	// that would be a second answer to "which repository", able to disagree with
	// the credential.
	if len(*cloneCalls) != 0 {
		t.Fatalf("a sandbox run resolved an address separately from its grant: %v", *cloneCalls)
	}
	if run.gotReq.Remote != "git@git.test:acme/api.git" {
		t.Fatalf("the sandbox was pointed at %q, not the caller's own repo", run.gotReq.Remote)
	}
	// Runner received the caller's org + credential.
	if run.gotOrg != "acme" || run.gotUser != "u-123" || run.gotReq.Key != secretToken {
		t.Fatalf("runner req wrong: org=%q user=%q credSet=%v", run.gotOrg, run.gotUser, run.gotReq.Key != "")
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

// A proposal is titled by the change its commit names, not by the request that
// asked for it; with no subject read, the prompt's headline still titles it.
func TestRun_PRTitleIsTheCommitSubject(t *testing.T) {
	for _, c := range []struct{ subject, want string }{
		{"Rename the hero CTA to Get Started", "api: Rename the hero CTA to Get Started"},
		{"", "api: fix the null deref in handler"},
	} {
		tr := &fakePR{ref: PRRef{Identifier: "API-1"}}
		run := &fakeRunner{result: RunResult{Branch: "agent/abc123def456", CommitSha: "deadbeef", Changed: true, OK: true, Subject: c.subject}}
		d, _ := dispatcherFor(&fakeSessions{id: "sess_abc123def456"}, tr, run, true)
		d.Run(context.Background(), baseReq())
		if len(tr.inputs) != 1 || tr.inputs[0].Title != c.want {
			t.Fatalf("subject %q: want title %q, got %+v", c.subject, c.want, tr.inputs)
		}
	}
}

// A title that already says what kind of change it is ("fix:", "feat(api):",
// "docs!:") is the title; only a bare one is prefixed with the repository.
func TestCodingTitle_KeepsAConventionalPrefix(t *testing.T) {
	for in, want := range map[string]string{
		"fix(coding): pass reviewers through": "fix(coding): pass reviewers through",
		"feat: draft pull requests":           "feat: draft pull requests",
		"docs!: say so":                       "docs!: say so",
		"api: already prefixed":               "api: already prefixed",
		"# api: markdown heading prompt":      "api: markdown heading prompt",
		"## fix(coding): markdown heading":    "fix(coding): markdown heading",
		"fix the null deref in handler":       "api: fix the null deref in handler",
		"":                                    "Agent changes to api",
	} {
		if got := codingTitle("api", in); got != want {
			t.Errorf("codingTitle(api, %q) = %q, want %q", in, got, want)
		}
	}
}

func TestPRBodyListsAtomicCommitsInChronologicalOrder(t *testing.T) {
	// Commits from artifact.go arrive newest-first; prBody renders oldest-first.
	body := prBody(
		"# api: split large files and add tests",
		"main", "agent/abc123", "2222222222abcdef", " 2 files changed", "sess_1", "zoe", "#42",
		client.CodingCommit{SHA: "2222222222abcdef", Message: "test(coding): add file-size gate"},
		client.CodingCommit{SHA: "1111111111abcdef", Message: "refactor(coding): split modules under 1,000 lines"},
	)
	firstIdx := strings.Index(body, "- `1111111111` refactor(coding): split modules under 1,000 lines")
	secondIdx := strings.Index(body, "- `2222222222` test(coding): add file-size gate")
	if firstIdx < 0 || secondIdx < 0 || firstIdx >= secondIdx {
		t.Fatalf("expected chronological atomic commit list in PR body, got:\n%s", body)
	}
	if strings.Contains(body, "**Task:** #") {
		t.Fatalf("expected markdown heading marker stripped from Task line, got:\n%s", body)
	}
}

// A run asked for a draft, assignees and reviewers carries them to the pull
// request it opens.
func TestRun_TheProposalCarriesDraftAssigneesAndReviewers(t *testing.T) {
	tr := &fakePR{ref: PRRef{Identifier: "API-1"}}
	run := &fakeRunner{result: RunResult{Branch: "agent/abc123def456", CommitSha: "deadbeef", Changed: true, OK: true}}
	d, _ := dispatcherFor(&fakeSessions{id: "sess_abc123def456"}, tr, run, true)
	req := baseReq()
	req.Proposal = Proposal{Draft: true, Assignees: []string{"zeekay"}, Reviewers: []string{"Darkhorse7stars"}}
	d.Run(context.Background(), req)
	if len(tr.inputs) != 1 {
		t.Fatalf("opened %d proposals", len(tr.inputs))
	}
	in := tr.inputs[0]
	if !in.Draft || strings.Join(in.Assignees, ",") != "zeekay" || strings.Join(in.Reviewers, ",") != "Darkhorse7stars" {
		t.Fatalf("the proposal was opened as %+v", in)
	}
}

func TestRun_EventVocabulary_StepIsToolCall_LogIsLog(t *testing.T) {
	sess := &fakeSessions{id: "sess_x"}
	run := &fakeRunner{
		steps:  []Step{{Type: "step", Step: "clone"}, {Type: "log", Message: "hi"}},
		result: RunResult{Branch: "agent/x", Changed: true, OK: true},
	}
	d, _ := dispatcherFor(sess, &fakePR{}, run, true)
	d.Run(context.Background(), baseReq())

	var messages, toolCalls, logs, statuses int
	for _, e := range sess.events {
		switch e.kind {
		case kindMessage:
			messages++
		case kindToolCall:
			toolCalls++
		case kindLog:
			logs++
		case kindStatus:
			statuses++
		case kindProgress: // the run's own word on how far along it is (progress_run_test.go)
		default:
			t.Fatalf("unexpected event kind %q", e.kind)
		}
	}
	if messages != 1 || toolCalls != 1 || logs != 1 {
		t.Fatalf("want message(1), step->tool-call(1) and log->log(1), got msg=%d tool=%d log=%d", messages, toolCalls, logs)
	}
	if statuses < 2 { // started + done at minimum
		t.Fatalf("want at least started+done status events, got %d", statuses)
	}
}

func TestRun_NoChanges_NoPR_DoneNotError(t *testing.T) {
	sess := &fakeSessions{id: "sess_x"}
	tr := &fakePR{}
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
	tr := &fakePR{}
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
	tr := &fakePR{}
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
	d, _ := dispatcherFor(sess, &fakePR{}, &fakeRunner{}, true)

	req := baseReq()
	req.Key = ""
	res := d.Run(context.Background(), req)
	if res.OK || !strings.Contains(res.Error, "git is not available") {
		t.Fatalf("want credential fail-closed, got %+v", res)
	}
	if len(sess.opened) != 0 {
		t.Fatalf("must not open a session without a credential")
	}
}

func TestRun_MissingRepoOrPrompt_FailsClosed(t *testing.T) {
	d, _ := dispatcherFor(&fakeSessions{id: "s"}, &fakePR{}, &fakeRunner{}, true)
	if r := d.Run(context.Background(), Req{Org: "acme", Remote: "r", Key: "k", Prompt: "do it"}); r.OK || r.Error == "" {
		t.Fatalf("missing repo must fail: %+v", r)
	}
	if r := d.Run(context.Background(), Req{Org: "acme", Repo: "api", Remote: "r", Key: "k"}); r.OK || r.Error == "" {
		t.Fatalf("missing prompt must fail: %+v", r)
	}
}

func TestRun_PRFailure_DoesNotFailRun(t *testing.T) {
	sess := &fakeSessions{id: "sess_x"}
	tr := &fakePR{err: context.Canceled}
	run := &fakeRunner{result: RunResult{Branch: "agent/x", Changed: true, OK: true}}
	d, _ := dispatcherFor(sess, tr, run, true)

	res := d.Run(context.Background(), baseReq())
	if !res.OK { // branch is pushed+verified; a PR-row failure is a side-effect, not a run failure
		t.Fatalf("todo failure must not fail the run, got %+v", res)
	}
	if res.PR.Identifier != "" {
		t.Fatalf("no PR ref when todo failed")
	}
	if sess.closes[0].status != statusDone {
		t.Fatalf("run still done despite todo failure")
	}
}

// THE SESSION NAMES ITS SANDBOX. The step that leased it tags the session with
// its id, and the step's own event carries the id and the class as fields, so a
// board showing the run's terminal reads a fact and never parses a sentence.
func TestRun_TheLeasedStepTagsTheSessionWithItsSandbox(t *testing.T) {
	sess := &fakeSessions{id: "sess_x"}
	run := &fakeRunner{
		steps: []Step{
			{Type: "step", Step: "lease", Message: "leasing a dev sandbox", Status: "running"},
			{Type: "step", Step: "leased", Message: "sandbox m_0123456789abcdef01234567 (dev)", Status: "running",
				Sandbox: "m_0123456789abcdef01234567", Class: "dev"},
		},
		result: RunResult{OK: true},
	}
	d, _ := dispatcherFor(sess, &fakePR{}, run, true)
	d.Run(context.Background(), baseReq())

	if got := sess.tagged().Sandbox; got != "m_0123456789abcdef01234567" {
		t.Fatalf("the session was tagged with sandbox %q", got)
	}
	var leased map[string]any
	for _, e := range sess.events {
		var p map[string]any
		if json.Unmarshal([]byte(e.payload), &p) == nil && p["step"] == "leased" {
			leased = p
		}
		if strings.Contains(e.payload, `"step":"lease"`) && strings.Contains(e.payload, `"sandbox"`) {
			t.Fatalf("a step that leased nothing names a sandbox: %s", e.payload)
		}
	}
	if leased["sandbox"] != "m_0123456789abcdef01234567" || leased["class"] != "dev" {
		t.Fatalf("the leased step's event is %v, want its sandbox and class as fields", leased)
	}
}

// A run an issue asked for proposes a change that closes it, and is tracked BY
// that issue: no second card for the same work. A run no issue asked for closes
// nothing and files its own card, as before.
func TestRun_AnIssuesRunClosesItAndFilesNoSecondCard(t *testing.T) {
	for _, issue := range []string{"", "#41", "hanzo-inc/cloud#464"} {
		tr := &fakePR{ref: PRRef{Identifier: "API-1"}}
		run := &fakeRunner{result: RunResult{Branch: "agent/abc123def456", CommitSha: "deadbeef", Changed: true, OK: true}}
		d, _ := dispatcherFor(&fakeSessions{id: "sess_abc123def456"}, tr, run, true)
		var cards int
		d.Issue = func(context.Context, string, client.IssueIn) error { cards++; return nil }
		req := baseReq()
		req.Issue = issue
		d.Run(context.Background(), req)
		if len(tr.inputs) != 1 {
			t.Fatalf("issue %q: want one proposal, got %d", issue, len(tr.inputs))
		}
		closes := strings.Contains(tr.inputs[0].Body, "\nCloses "+issue+"\n")
		if closes != (issue != "") {
			t.Fatalf("issue %q: the proposal closes it = %v; body:\n%s", issue, closes, tr.inputs[0].Body)
		}
		if (cards > 0) == (issue != "") {
			t.Fatalf("issue %q: %d cards filed; an issue's run is tracked by the issue alone", issue, cards)
		}
	}
}

// AN ISSUE IS NAMED ON THE RUN'S REPOSITORY OR ON ANOTHER (#553): a number, or
// owner/name#number, which is how an iam run links hanzo-inc/cloud#464. Anything
// else is refused, since the reference is written into the pull request.
//
// MUTATION: drop the owner/name alternative from issueRE and the cross-repository
// references are refused.
func TestClosing(t *testing.T) {
	for in, want := range map[client.Issue]string{
		"": "", "0": "", "41": "#41", "#41": "#41", " #41 ": "#41",
		"hanzo-inc/cloud#464": "hanzo-inc/cloud#464",
		"hanzoai/iam.go#7":    "hanzoai/iam.go#7",
	} {
		if got, err := Closing(in); err != nil || got != want {
			t.Errorf("Closing(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []client.Issue{"-1", "#0", "x", "cloud#464", "hanzo-inc/cloud464", "a/b/c#1",
		"hanzo-inc/..#4", "hanzo-inc/cloud#4\nCloses #9", "-x/cloud#4", "https://github.com/a/b/issues/4"} {
		if got, err := Closing(bad); err == nil {
			t.Errorf("Closing(%q) = %q, want it refused", bad, got)
		}
	}
}

// THE WIRE TAKES AN ISSUE AS A NUMBER OR AS A STRING, so a caller that sends the
// bare number names the same issue it always did.
func TestIssueReadsANumberOrAString(t *testing.T) {
	for body, want := range map[string]client.Issue{
		`{"issue":464}`: "464", `{"issue":"hanzo-inc/cloud#464"}`: "hanzo-inc/cloud#464", `{}`: "", `{"issue":null}`: "",
	} {
		var in client.CodingStartIn
		if err := json.Unmarshal([]byte(body), &in); err != nil || in.Issue != want {
			t.Errorf("%s read %q, %v; want %q", body, in.Issue, err, want)
		}
	}
	var in client.CodingStartIn
	if err := json.Unmarshal([]byte(`{"issue":true}`), &in); err == nil {
		t.Errorf("a boolean issue was read as %q", in.Issue)
	}
}

// A run an issue asked for with nowhere to talk narrates in the org's issue
// channel; an org the setting does not name has none.
func TestIssueChannel(t *testing.T) {
	t.Setenv("CODING_ISSUE_CHANNEL", "lux:C0LUX, hanzo:C0HANZO")
	if got := issueChannel("Hanzo"); got != (client.Reply{Provider: "slack", Channel: "C0HANZO"}) {
		t.Fatalf("hanzo = %+v", got)
	}
	if got := issueChannel("zoo"); got != (client.Reply{}) {
		t.Fatalf("an org with no channel got %+v", got)
	}
}

// A BUILD THAT DID NOTHING IS NOT DONE when the run can say why — it had no code
// to work on, or its agent stopped mid-task — and it closes error with that
// reason. One that read real code and answered is done, as before.
func TestRun_AHollowBuildFailsWithItsReason(t *testing.T) {
	for _, c := range []struct {
		name   string
		made   bool
		from   string
		result RunResult
		status string
		says   string
	}{
		{name: "an empty checkout", result: RunResult{OK: true, Empty: true, Answer: "This is a fresh repository."},
			status: statusError, says: "api holds no code"},
		{name: "a turn with no last message", result: RunResult{OK: true, Unanswered: true,
			Answer: "I'll create the specification document under docs/rfcs/."},
			status: statusError, says: "stopped mid-task without a final answer and changed nothing; the last thing it said was: I'll create"},
		{name: "an announcement it neither acted on nor withdrew", result: RunResult{OK: true, Unacted: true,
			Answer: "Let me create the spec document for the Agent Lease & Handoff API."},
			status: statusError, says: "said what it would do and changed nothing, even when asked to make the change or say none was needed; the last thing it said was: Let me create"},
		{name: "an announcement with a sandbox preview port still fails", result: RunResult{OK: true, Unacted: true,
			Answer:    "Now let me run pnpm install and start the patch flow.",
			Artifacts: []client.Artifact{{Name: "port 9999", Kind: kindPreview}}},
			status: statusError, says: "said what it would do and changed nothing"},
		{name: "a continuation that ended on the announcement again", from: "agent/abc123", result: RunResult{OK: true, Unacted: true,
			Answer: "Let me review the current state of all files:"},
			status: statusError, says: "said what it would do and added nothing to the work already on agent/abc123, even when asked to finish; the last thing it said was: Let me review"},
		{name: "a conversation whose token budget is spent", from: "agent/abc123", result: RunResult{OK: true, Unacted: true,
			Answer: "The thread goal has reached its token budget, so I'm wrapping up this turn without starting new substantive work."},
			status: statusError, says: "conversation has used its token budget, so it wrapped up without doing the work; start a new run from this branch"},
		{name: "an answer that nothing needed changing", result: RunResult{OK: true, Answer: "The test already covers it."},
			status: statusDone},
		{name: "an empty checkout the run filled", result: RunResult{OK: true, Empty: true, Changed: true, CommitSha: "abc"},
			status: statusDone},
		{name: "an empty checkout with something kept", result: RunResult{OK: true, Empty: true,
			Artifacts: []client.Artifact{{Name: "port 3000", Kind: kindPreview}}}, status: statusDone},
		{name: "a new project's one-README repository", made: true, result: RunResult{OK: true, Empty: true,
			Answer: "Nothing to build yet."}, status: statusDone},
	} {
		t.Run(c.name, func(t *testing.T) {
			sess := &fakeSessions{id: "sess_x"}
			tr := &fakePR{}
			d, _ := dispatcherFor(sess, tr, &fakeRunner{result: c.result}, true)
			req := baseReq()
			req.Made = c.made
			req.From = c.from
			res := d.Run(context.Background(), req)
			if len(sess.closes) != 1 || sess.closes[0].status != c.status {
				t.Fatalf("closed %+v, want %s", sess.closes, c.status)
			}
			if c.status == statusDone {
				return
			}
			if res.OK || !strings.Contains(res.Error, c.says) {
				t.Fatalf("the run failed with %q, want it to say %q", res.Error, c.says)
			}
			if len(tr.inputs) != 0 {
				t.Fatalf("a hollow run filed a proposal: %+v", tr.inputs)
			}
			var last eventCall
			for _, e := range sess.events {
				if e.kind == kindStatus {
					last = e
				}
			}
			if !strings.Contains(last.payload, `"status":"error"`) || !strings.Contains(last.payload, c.says[:min(len(c.says), 20)]) {
				t.Fatalf("the session's last status does not carry the reason: %s", last.payload)
			}
		})
	}
}

// A multi-line ask has its first line as the session's title and its whole text
// as the opening user message, so the run page and Try again carry the rest of
// the lines rather than only the headline.
func TestRun_RecordsFullMultilinePromptAsUserMessage(t *testing.T) {
	sess := &fakeSessions{id: "sess_x"}
	d, _ := dispatcherFor(sess, &fakePR{}, &fakeRunner{result: RunResult{OK: true, Answer: "Checked."}}, true)
	req := baseReq()
	req.Prompt = "check this pr -\nhttps://github.com/ClickHouse/ClickHouse/pull/115724\nextra context"
	d.Run(context.Background(), req)

	if len(sess.opened) != 1 || sess.opened[0].title != "api: check this pr -" {
		t.Fatalf("title = %+v, want first line only", sess.opened)
	}
	if len(sess.events) == 0 || sess.events[0].kind != kindMessage {
		t.Fatalf("first event = %+v, want kindMessage", sess.events)
	}
	var msg struct {
		Role string `json:"role"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(sess.events[0].payload), &msg); err != nil {
		t.Fatalf("decode user message: %v", err)
	}
	if msg.Role != "user" || msg.Text != req.Prompt {
		t.Fatalf("user message = %+v, want role=user text=%q", msg, req.Prompt)
	}
}
