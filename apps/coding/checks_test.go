package coding

// checks_test.go pins when a run's work leaves its sandbox (#553): a steer keeps
// the tree for its follow-up and pushes nothing, a finished run pushes once its
// own checks are over, and a run whose checkout already held its branch's work is
// not told it changed nothing.

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/apps/agents"
	"github.com/hanzoai/cloud/client"
	"github.com/zap-proto/zip"
)

// pushAt is the index of the first push a run sent into the pod, -1 for none;
// pushes counts them.
func pushAt(p *pod) (int, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	at, n := -1, 0
	for i, argv := range p.ran {
		if argv[0] == "git" && slices.Contains(argv, "push") {
			if at < 0 {
				at = i
			}
			n++
		}
	}
	return at, n
}

// turnAt is the index of the n-th harness turn (0-based) a run sent, -1 for none.
func turnAt(p *pod, n int) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i, argv := range p.ran {
		if slices.Contains(argv, "--skip-git-repo-check") {
			if n == 0 {
				return i
			}
			n--
		}
	}
	return -1
}

// A STEER COMMITS AND PUSHES NOTHING. Its follow-up resumes this sandbox and works
// in this tree, and commits and pushes when its own checks are done. The steered
// run in the #464 re-test was stopped mid `make test` and its work was on the
// branch before the tests it was running had passed.
//
// MUTATION: drop the errFollowed case after the first turn in Run and the steered
// tree is committed and pushed.
func TestSandboxRun_ASteerKeepsTheTreeAndPushesNothing(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	tip := theSHA
	p := &pod{}
	p.answer = func(argv []string) client.Ran {
		switch {
		case slices.Contains(argv, "--skip-git-repo-check"): // `make test` is running when the steer arrives
			cancel(errFollowed)
			return client.Ran{ExitCode: 143}
		case slices.Contains(argv, "commit"):
			tip = newSHA
		case slices.Contains(argv, "rev-parse"):
			return client.Ran{Stdout: tip + "\n"}
		}
		return client.Ran{}
	}
	servePod(t, p)
	res, err := sandboxRunner{}.Run(ctx, "acme", "u_1", aRun(), nil)
	if err != nil {
		t.Fatalf("a steered run keeps its tree rather than failing: %v", err)
	}
	if res.Changed || res.CommitSha != "" {
		t.Fatalf("a steered run reported work on its branch: %+v", res)
	}
	if p.gitVerb("commit") || p.gitVerb("push") {
		t.Fatalf("a steered run committed or pushed:\n%s", strings.Join(p.lines(), "\n"))
	}
	p.mu.Lock()
	stops := p.stops
	p.mu.Unlock()
	if stops != 1 {
		t.Fatalf("the steered task must be stopped in the pod, stops=%d", stops)
	}
}

// halfDone is a turn that edited a file and ended on an announcement of the
// check it would run next; checked is the turn that ran it.
const (
	halfDone = `{"type":"thread.started","thread_id":"` + loopThread + `"}
{"type":"turn.started"}
{"type":"item.completed","item":{"id":"item_0","type":"file_change"}}
{"type":"item.completed","item":{"id":"item_1","type":"agent_message","text":"Now let me run the tests:"}}
{"type":"turn.completed","usage":{"input_tokens":900,"cached_input_tokens":0,"output_tokens":40}}
`
	checked = `{"type":"thread.started","thread_id":"` + loopThread + `"}
{"type":"turn.started"}
{"type":"item.completed","item":{"id":"item_0","type":"command_execution","command":"make test","exit_code":0}}
{"type":"item.completed","item":{"id":"item_1","type":"agent_message","text":"make test passes."}}
{"type":"turn.completed","usage":{"input_tokens":900,"cached_input_tokens":0,"output_tokens":40}}
`
)

// checking is a pod whose run edits and announces its tests in its first turn and
// runs them in the second. during, when set, is what the second turn does in
// place of finishing.
func checking(during func() (client.Ran, bool)) *pod {
	tip, ran := theSHA, 0
	turns := []string{halfDone, checked}
	answers := []string{"Now let me run the tests:", "make test passes."}
	p := &pod{}
	p.answer = func(argv []string) client.Ran {
		switch {
		case slices.Contains(argv, "--skip-git-repo-check"):
			ran++
			if ran == 2 && during != nil {
				if r, ok := during(); ok {
					return r
				}
			}
			return client.Ran{Stdout: turns[min(ran, len(turns))-1]}
		case len(argv) == 2 && argv[0] == "cat" && argv[1] == answerPath:
			return client.Ran{Stdout: answers[min(max(ran, 1), len(answers))-1] + "\n"}
		case slices.Contains(argv, "commit"):
			if ran == 1 {
				tip = newSHA
			}
		case slices.Contains(argv, "rev-parse"):
			return client.Ran{Stdout: tip + "\n"}
		}
		return client.Ran{}
	}
	return p
}

// THE WORK IS PUSHED ONCE THE RUN'S CHECKS ARE OVER. A first turn that edited and
// ended announcing its tests is committed, asked to finish, and pushed after the
// turn that ran them — once. It was pushed the moment the first turn ended.
//
// MUTATION: deliver (commit and push) after the first turn instead of commit, and
// the push comes before the second turn.
func TestSandboxRun_TheWorkIsPushedOnceTheChecksAreOver(t *testing.T) {
	p := checking(nil)
	servePod(t, p)
	res, err := sandboxRunner{}.Run(context.Background(), "acme", "u_1", aRun(), nil)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !res.OK || !res.Changed || res.CommitSha != newSHA {
		t.Fatalf("the checked work was not delivered: %+v", res)
	}
	push, pushes := pushAt(p)
	if second := turnAt(p, 1); second < 0 || push < second || pushes != 1 {
		t.Fatalf("pushed %d times, first at %d; the turn that ran the checks is at %d:\n%s",
			pushes, push, second, strings.Join(p.lines(), "\n"))
	}
}

// A STEER DURING THE CHECKS PUSHES NOTHING EITHER: what the run committed is in
// the tree its follow-up resumes, and the follow-up pushes it.
//
// MUTATION: drop the errFollowed case after the checks in Run and the committed
// work is pushed.
func TestSandboxRun_ASteerDuringTheChecksPushesNothing(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	p := checking(func() (client.Ran, bool) {
		cancel(errFollowed)
		return client.Ran{ExitCode: 143}, true
	})
	servePod(t, p)
	res, err := sandboxRunner{}.Run(ctx, "acme", "u_1", aRun(), nil)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if _, pushes := pushAt(p); pushes != 0 || res.Changed {
		t.Fatalf("a run steered during its checks pushed %d times, reported %+v:\n%s", pushes, res, strings.Join(p.lines(), "\n"))
	}
	if !p.gitVerb("commit") {
		t.Fatal("the first turn's work was not committed in the tree")
	}
}

// A SANDBOX THAT FAILS UNDER THE CHECKS FAILS THE RUN, saying so. The pod of the
// #464 follow-up was evicted mid `make test`, and the run said "the agent said
// what it would do and changed nothing". What it had committed is kept.
//
// MUTATION: drop the err case from the checks' failure branch and the run is
// reported done (or unacted) instead of failed.
func TestSandboxRun_ASandboxThatFailsUnderTheChecksFailsTheRun(t *testing.T) {
	second := 0
	p := checking(nil)
	p.refuse = func(argv []string) error {
		if slices.Contains(argv, "--skip-git-repo-check") {
			if second++; second == 2 {
				return zip.Errorf(http.StatusBadGateway, `exec: waiting on pid 13154: urpc method "containerManager.WaitPID" failed: EOF`)
			}
		}
		return nil
	}
	servePod(t, p)
	res, err := sandboxRunner{}.Run(context.Background(), "acme", "u_1", aRun(), nil)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.OK || res.Unacted || !strings.Contains(res.Error, "the sandbox failed under the agent's turn") || !strings.Contains(res.Error, "WaitPID") {
		t.Fatalf("ok %v, unacted %v, error %q: want a failure naming the sandbox", res.OK, res.Unacted, res.Error)
	}
	if !res.Changed || !strings.Contains(res.Error, "its work so far is on agent/abc123def456") {
		t.Fatalf("the committed work was not kept on the branch: %+v", res)
	}
}

// A FOLLOW-UP IN THE TREE ITS EARLIER RUN LEFT is measured from what the remote
// holds: a commit the earlier run made and never pushed (it was steered) is
// pushed, and commits it did push are not counted as this run's change.
//
// MUTATION: measure a resumed tree from its tip and the unpushed commit is never
// pushed; ignore the pushed mark and the pushed commits are this run's change.
func TestSandboxRun_AFollowUpMeasuresFromWhatTheRemoteHolds(t *testing.T) {
	const branch = "agent/abc123def456"
	for name, c := range map[string]struct {
		pushed  string // the pushed mark, "" for none
		changed bool
	}{
		"its earlier run pushed everything":    {pushed: newSHA, changed: false},
		"its earlier run's commit is unpushed": {pushed: "", changed: true},
	} {
		t.Run(name, func(t *testing.T) {
			p := &pod{answer: func(argv []string) client.Ran {
				switch {
				case slices.Contains(argv, "--skip-git-repo-check"):
					return client.Ran{Stdout: workedTurn}
				case slices.Contains(argv, "symbolic-ref"):
					return client.Ran{Stdout: branch + "\n"}
				case slices.Contains(argv, "rev-parse") && slices.Contains(argv, pushedRef+branch):
					if c.pushed == "" {
						return client.Ran{ExitCode: 1}
					}
					return client.Ran{Stdout: c.pushed + "\n"}
				case slices.Contains(argv, "rev-parse") && slices.Contains(argv, baseRef+branch):
					return client.Ran{Stdout: theSHA + "\n"}
				case slices.Contains(argv, "rev-parse"):
					return client.Ran{Stdout: newSHA + "\n"} // the tree's tip: the earlier run's commit
				}
				return client.Ran{}
			}}
			servePod(t, p)
			req := aRun()
			req.Sandbox = "sbx_1" // the sandbox the earlier run left, which the lease resumes
			res, err := sandboxRunner{}.Run(context.Background(), "acme", "u_1", req, nil)
			if err != nil {
				t.Fatalf("run: %v", err)
			}
			if res.Changed != c.changed || p.gitVerb("push") != c.changed {
				t.Fatalf("changed %v, pushed %v; want %v:\n%s", res.Changed, p.gitVerb("push"), c.changed, strings.Join(p.lines(), "\n"))
			}
		})
	}
}

// carriedTurn is a turn in loopThread on a branch that already held the work: it
// ran the checks and changed nothing, and said so in its own words.
const carriedTurn = `{"type":"thread.started","thread_id":"` + loopThread + `"}
{"type":"turn.started"}
{"type":"item.completed","item":{"id":"item_0","type":"command_execution","command":"make test","exit_code":0}}
{"type":"item.completed","item":{"id":"item_1","type":"agent_message","text":"The regeneration and gofmt edits are in, and make test passes."}}
{"type":"turn.completed","usage":{"input_tokens":900,"cached_input_tokens":0,"output_tokens":40}}
`

// A RUN WHOSE CHECKOUT STARTED ON ITS OWN BRANCH IS NOT TOLD IT CHANGED NOTHING.
// The branch held the earlier run's edits, so a turn that verified them and added
// none is done; one that ended on an announcement is asked to finish it, not to
// make a change. The #464 follow-up was asked to "make its change or say none is
// needed" with its edits already in.
//
// MUTATION: drop `|| carried` from the checks' condition and the finished turn is
// asked again; drop it from the prompt and the announcement is answered with settle.
func TestSandboxRun_ACarriedBranchIsNotAskedForAChange(t *testing.T) {
	const branch = "agent/abc123def456"
	for name, c := range map[string]struct {
		first, said string
		turns       int
		told        string
	}{
		"verified and done":        {carriedTurn, "The regeneration and gofmt edits are in, and make test passes.", 1, ""},
		"ended on an announcement": {announcedTurn, "Let me create the spec document.", 2, finishTurn},
	} {
		t.Run(name, func(t *testing.T) {
			ran := 0
			p := &pod{answer: func(argv []string) client.Ran {
				switch {
				case slices.Contains(argv, "--skip-git-repo-check"):
					ran++
					if ran == 1 {
						return client.Ran{Stdout: c.first}
					}
					return client.Ran{Stdout: carriedTurn}
				case len(argv) == 2 && argv[0] == "cat" && argv[1] == answerPath:
					if ran == 1 {
						return client.Ran{Stdout: c.said + "\n"}
					}
					return client.Ran{Stdout: "The regeneration and gofmt edits are in, and make test passes.\n"}
				case slices.Contains(argv, "rev-parse"):
					return client.Ran{Stdout: theSHA + "\n"}
				}
				return client.Ran{}
			}}
			servePod(t, p)
			req := aRun()
			req.BaseBranch = branch // the checkout starts on the run's own branch, which the remote holds
			res, err := sandboxRunner{}.Run(context.Background(), "acme", "u_1", req, nil)
			if err != nil {
				t.Fatalf("run: %v", err)
			}
			if ran != c.turns || res.Unacted || !res.OK {
				t.Fatalf("%d turns, unacted %v, ok %v; want %d turns, done: %+v", ran, res.Unacted, res.OK, c.turns, res)
			}
			if c.told != "" {
				if got := toldOf(harnessFed(p)[1]); got != c.told {
					t.Fatalf("the second turn was told %q, want %q", got, c.told)
				}
			}
		})
	}
}

// A continuation that still announces after being asked to finish has not finished,
// and neither has one whose conversation says its token budget is spent: both fail
// saying what they last said.
//
// MUTATION: drop the guard after the checks and both runs close done.
func TestSandboxRun_AContinuationThatKeepsAnnouncingIsNotDone(t *testing.T) {
	const branch = "agent/abc123def456"
	const spentTurn = `{"type":"thread.started","thread_id":"` + loopThread + `"}
{"type":"turn.started"}
{"type":"item.completed","item":{"id":"item_0","type":"agent_message","text":"The thread goal has reached its token budget, so I'm wrapping up this turn without starting new substantive work."}}
{"type":"turn.completed","usage":{"input_tokens":28178485,"cached_input_tokens":26357360,"output_tokens":59976}}
`
	for name, c := range map[string]struct {
		turn, said string
		turns      int
	}{
		"announces again when asked to finish": {announcedTurn, "Let me create the spec document.", 2},
		"its conversation's budget is spent":   {spentTurn, "The thread goal has reached its token budget, so I'm wrapping up this turn without starting new substantive work.", 1},
	} {
		t.Run(name, func(t *testing.T) {
			ran := 0
			p := &pod{answer: func(argv []string) client.Ran {
				switch {
				case slices.Contains(argv, "--skip-git-repo-check"):
					ran++
					return client.Ran{Stdout: c.turn}
				case len(argv) == 2 && argv[0] == "cat" && argv[1] == answerPath:
					return client.Ran{Stdout: c.said + "\n"}
				case slices.Contains(argv, "rev-parse"):
					return client.Ran{Stdout: theSHA + "\n"}
				}
				return client.Ran{}
			}}
			servePod(t, p)
			req := aRun()
			req.BaseBranch = branch // a continuation: the checkout starts on the run's own branch
			res, err := sandboxRunner{}.Run(context.Background(), "acme", "u_1", req, nil)
			if err != nil {
				t.Fatalf("run: %v", err)
			}
			if !res.Unacted || res.Changed {
				t.Fatalf("unacted %v, changed %v after %d turns; want it unacted: %+v", res.Unacted, res.Changed, ran, res)
			}
			if ran != c.turns {
				t.Fatalf("the harness ran %d times, want %d", ran, c.turns)
			}
		})
	}
}

// A LONG TURN SAYS WHAT IT IS DOING (#553). A turn is one exec that answers when
// it ends, so the #464 follow-up's progress said nothing for the half hour its
// `make test` ran. While a turn runs, its beat reads the harness's output and says
// the command it is in — as progress, and as nothing else.
//
// MUTATION: drop beating from turn and no beat is said.
func TestSandboxRun_ALongTurnSaysWhatItIsDoing(t *testing.T) {
	prev := beatEvery
	beatEvery = 20 * time.Millisecond
	t.Cleanup(func() { beatEvery = prev })
	var read []string
	p := &pod{
		answer: func(argv []string) client.Ran {
			switch {
			case argv[0] == "tail" && slices.Contains(argv, "-c"):
				read = append(read, argv[len(argv)-1])
				return client.Ran{Stdout: `{"type":"thread.started","thread_id":"t2"}
{"type":"item.started","item":{"id":"item_1","type":"command_execution","command":"go version"}}
{"type":"item.completed","item":{"id":"item_1","type":"command_execution","command":"go version","exit_code":0}}
{"type":"item.started","item":{"id":"item_2","type":"command_execution","command":"bash -lc \"make test\""}}
`}
			case slices.Contains(argv, "--skip-git-repo-check"):
				return client.Ran{Stdout: workedTurn}
			case slices.Contains(argv, "rev-parse"):
				return client.Ran{Stdout: theSHA + "\n"}
			}
			return client.Ran{}
		},
		refuse: func(argv []string) error {
			if slices.Contains(argv, "--skip-git-repo-check") {
				time.Sleep(300 * time.Millisecond) // the turn is long
			}
			return nil
		},
	}
	servePod(t, p)
	var mu sync.Mutex
	var beats, others []Step
	res, err := sandboxRunner{}.Run(context.Background(), "acme", "u_1", aRun(), func(s Step) {
		mu.Lock()
		defer mu.Unlock()
		if s.Type == "beat" {
			beats = append(beats, s)
		} else {
			others = append(others, s)
		}
	})
	if err != nil || !res.OK {
		t.Fatalf("run: %v %+v", err, res)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(beats) == 0 || beats[len(beats)-1].Message != "running `make test`" {
		t.Fatalf("the turn said %+v while it ran, want that it is running make test", beats)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	group := ""
	for _, argv := range p.ran {
		for i, a := range argv {
			if strings.HasPrefix(a, "g=$1; shift") && i+2 < len(argv) {
				group = argv[i+2]
			}
		}
	}
	if len(read) == 0 || group == "" || read[0] != group+".d/o" {
		t.Fatalf("the beat read %q, want the turn's own output %s.d/o", read, group)
	}
}

// A BEAT MOVES THE RUN'S PROGRESS AND NOTHING ELSE: no step in the transcript and
// no edit of the thread's message, which has a budget a minute's beat would spend.
//
// MUTATION: mirror a beat as a step and the transcript gains a tool-call.
func TestRun_ABeatIsProgressOnly(t *testing.T) {
	sess := &fakeSessions{id: "sess_abc123def456"}
	run := &fakeRunner{steps: []Step{{Type: "beat", Message: "running `make test` for 12m", Status: "running"}},
		result: RunResult{OK: true}}
	d, _ := dispatcherFor(sess, &fakePR{}, run, true)
	d.Run(context.Background(), baseReq())
	sess.mu.Lock()
	defer sess.mu.Unlock()
	saw := false
	for _, e := range sess.events {
		if strings.Contains(e.payload, "make test") {
			if e.kind != kindProgress {
				t.Fatalf("a beat was mirrored as %s: %s", e.kind, e.payload)
			}
			saw = true
		}
	}
	if !saw {
		t.Fatalf("the beat did not reach the session's progress: %+v", sess.events)
	}
}

// lastAt is the index of the last git command with verb, -1 for none.
func lastAt(p *pod, verb string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	at := -1
	for i, argv := range p.ran {
		if argv[0] == "git" && slices.Contains(argv, verb) {
			at = i
		}
	}
	return at
}

// A STEERED RUN SAVES WHAT IT CHANGED beside the run, uncommitted work and all,
// so the work outlives a sandbox its follow-up cannot resume — one that did not
// start, or a sandbox retired before it did.
//
// MUTATION: drop the save from steered and nothing of the tree is kept.
func TestSandboxRun_ASteeredRunSavesItsTree(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	p := &pod{answer: func(argv []string) client.Ran {
		switch {
		case slices.Contains(argv, "--skip-git-repo-check"):
			cancel(errFollowed)
			return client.Ran{ExitCode: 143}
		case slices.Contains(argv, "rev-parse"):
			return client.Ran{Stdout: theSHA + "\n"}
		}
		return client.Ran{}
	}}
	servePod(t, p)
	if _, err := (sandboxRunner{}).Run(ctx, "acme", "u_1", aRun(), nil); err != nil {
		t.Fatalf("run: %v", err)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, argv := range p.ran {
		if len(argv) > 3 && argv[0] == "bash" && argv[2] == changeScript && argv[4] == theSHA {
			return
		}
	}
	t.Fatalf("the steered run did not save what it changed since %s", theSHA)
}

// A FOLLOW-UP THAT FAILS DOES NOT END THE SANDBOX IT RESUMED: that sandbox holds
// the steered run's work, unpushed, and ending it took the work with the disk.
//
// MUTATION: end a failed run's sandbox whether or not it resumed it.
func TestSandboxRun_AFailedFollowUpKeepsTheSandboxItResumed(t *testing.T) {
	const branch = "agent/abc123def456"
	p := &pod{answer: func(argv []string) client.Ran {
		switch {
		case slices.Contains(argv, "--skip-git-repo-check"):
			return client.Ran{ExitCode: 1, Stderr: "error: exceeded retry limit, last status: 400 Bad Request\n"}
		case slices.Contains(argv, "symbolic-ref"):
			return client.Ran{Stdout: branch + "\n"}
		case slices.Contains(argv, "rev-parse") && slices.Contains(argv, pushedRef+branch):
			return client.Ran{ExitCode: 1}
		case slices.Contains(argv, "rev-parse") && slices.Contains(argv, baseRef+branch):
			return client.Ran{Stdout: theSHA + "\n"}
		case slices.Contains(argv, "rev-parse"):
			return client.Ran{Stdout: newSHA + "\n"}
		}
		return client.Ran{}
	}}
	servePod(t, p)
	req := aRun()
	req.Sandbox = "sbx_1"
	res, err := sandboxRunner{}.Run(context.Background(), "acme", "u_1", req, nil)
	if err != nil || res.OK {
		t.Fatalf("run: %v %+v, want a failed run", err, res)
	}
	p.mu.Lock()
	ends := p.ends
	p.mu.Unlock()
	if ends != 0 {
		t.Fatalf("a failed follow-up ended the sandbox it resumed (%d ends)", ends)
	}
}

// A CHECK TURN CUT SHORT IS COMMITTED BEFORE THE PUSH: stopped, or with its
// sandbox failing under it, what it wrote reaches the branch with the rest.
//
// MUTATION: drop the commit after a cut turn and the push carries only the
// first turn's commit.
func TestSandboxRun_ACheckTurnCutShortIsCommittedBeforeThePush(t *testing.T) {
	for name, cut := range map[string]func(context.CancelCauseFunc, *pod){
		"stopped": func(cancel context.CancelCauseFunc, p *pod) {
			p.answer = wrapTurn(p.answer, 2, func() (client.Ran, bool) {
				cancel(errStopped)
				return client.Ran{ExitCode: 143}, true
			})
		},
		"its sandbox failed": func(_ context.CancelCauseFunc, p *pod) {
			n := 0
			p.refuse = func(argv []string) error {
				if slices.Contains(argv, "--skip-git-repo-check") {
					if n++; n == 2 {
						return zip.Errorf(http.StatusBadGateway, "exec: urpc EOF")
					}
				}
				return nil
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancelCause(context.Background())
			p := checking(nil)
			cut(cancel, p)
			servePod(t, p)
			if _, err := (sandboxRunner{}).Run(ctx, "acme", "u_1", aRun(), nil); err != nil {
				t.Fatalf("run: %v", err)
			}
			second, commit, push := turnAt(p, 1), lastAt(p, "commit"), lastAt(p, "push")
			if second < 0 || commit < second || push < commit {
				t.Fatalf("turn at %d, last commit at %d, push at %d: the cut turn was not committed before the push:\n%s",
					second, commit, push, strings.Join(p.lines(), "\n"))
			}
		})
	}
}

// errStopped is a person's stop, as the session delivers it.
var errStopped = fmt.Errorf("%w: by its owner", agents.ErrStopped)

// wrapTurn answers the n-th harness turn (1-based) with during, and every other
// command as answer does.
func wrapTurn(answer func([]string) client.Ran, n int, during func() (client.Ran, bool)) func([]string) client.Ran {
	turns := 0
	return func(argv []string) client.Ran {
		if slices.Contains(argv, "--skip-git-repo-check") {
			if turns++; turns == n {
				if r, ok := during(); ok {
					return r
				}
			}
		}
		return answer(argv)
	}
}

// A COMMIT THAT FAILS DURING THE CHECKS DOES NOT DROP THE PUSH: what the turns
// before it committed is pushed, and the run fails saying why.
//
// MUTATION: return the commit's error from the checks and nothing is pushed.
func TestSandboxRun_ACommitThatFailsInTheChecksStillPushes(t *testing.T) {
	adds := 0
	p := checking(nil)
	p.refuse = func(argv []string) error {
		if argv[0] == "git" && slices.Contains(argv, "add") {
			if adds++; adds == 2 {
				return zip.Errorf(http.StatusBadGateway, "exec: urpc EOF")
			}
		}
		return nil
	}
	servePod(t, p)
	res, err := sandboxRunner{}.Run(context.Background(), "acme", "u_1", aRun(), nil)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.OK || !res.Changed || !p.gitVerb("push") {
		t.Fatalf("ok %v, changed %v, pushed %v: want the first turn's work pushed and the run failed", res.OK, res.Changed, p.gitVerb("push"))
	}
}

// A POD LOST UNDER A CHECK TURN IS REARMED BEFORE THE PUSH. apps/sandbox replaces
// it with its disk and without /tmp, so the run's key is gone, and the first call
// to reach it is refused while it is replaced. The key is written again, and the
// work is pushed.
//
// MUTATION: drop rearm and the push finds no key.
func TestSandboxRun_APodLostUnderTheChecksIsRearmedBeforeThePush(t *testing.T) {
	var mu sync.Mutex
	evicted, armed, refused := false, false, false
	p := checking(nil)
	answer := p.answer
	p.answer = func(argv []string) client.Ran {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case len(argv) > 2 && argv[0] == "sh" && strings.HasPrefix(argv[2], "umask 077"):
			armed = !evicted || refused // a key written to the pod before it was replaced is gone
		case argv[0] == "git" && slices.Contains(argv, "push") && evicted && !armed:
			return client.Ran{ExitCode: 128, Stderr: "Load key \"/tmp/hanzo-run/key\": No such file or directory\n"}
		}
		return answer(argv)
	}
	turns := 0
	p.refuse = func(argv []string) error {
		mu.Lock()
		defer mu.Unlock()
		if slices.Contains(argv, "--skip-git-repo-check") {
			if turns++; turns == 2 {
				evicted, armed = true, false
				return zip.Errorf(http.StatusBadGateway, `exec: urpc method "containerManager.WaitPID" failed: EOF`)
			}
		}
		if evicted && !refused && !slices.Contains(argv, "--skip-git-repo-check") {
			refused = true
			return zip.Errorf(http.StatusServiceUnavailable, "sandbox sbx_1 lost its pod and has a new one under the same id; run it again")
		}
		return nil
	}
	servePod(t, p)
	res, err := sandboxRunner{}.Run(context.Background(), "acme", "u_1", aRun(), nil)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !res.Changed || !strings.Contains(res.Error, "its work so far is on agent/abc123def456") {
		t.Fatalf("the work was not pushed after the pod was replaced: %+v", res)
	}
}

// A LOCAL RUN MARKS WHAT IT KEPT, as a pushed one does, so a follow-up in its
// workspace does not count the earlier run's commits as its own change.
//
// MUTATION: drop the mark from push's local case.
func TestSandboxRun_ALocalRunMarksWhatItKept(t *testing.T) {
	tip := theSHA
	p := &pod{answer: func(argv []string) client.Ran {
		switch {
		case slices.Contains(argv, "--skip-git-repo-check"):
			return client.Ran{Stdout: workedTurn}
		case slices.Contains(argv, "commit"):
			tip = newSHA
		case slices.Contains(argv, "rev-parse"):
			return client.Ran{Stdout: tip + "\n"}
		}
		return client.Ran{}
	}}
	servePod(t, p)
	req := aRun()
	req.Local = true
	res, err := sandboxRunner{}.Run(context.Background(), "acme", "u_1", req, nil)
	if err != nil || !res.Changed || p.gitVerb("push") {
		t.Fatalf("run: %v %+v, pushed %v", err, res, p.gitVerb("push"))
	}
	if _, ok := p.did("update-ref " + pushedRef + "agent/abc123def456 " + newSHA); !ok {
		t.Fatalf("the local run did not mark what it kept:\n%s", strings.Join(p.lines(), "\n"))
	}
}

// otherThread is a check turn that answered in a conversation it was not sent
// to: a turn that never saw the task.
const otherThread = `{"type":"thread.started","thread_id":"0000aaaa-1111-2222-3333-444455556666"}
{"type":"turn.started"}
{"type":"item.completed","item":{"id":"item_0","type":"file_change"}}
{"type":"item.completed","item":{"id":"item_1","type":"agent_message","text":"done."}}
{"type":"turn.completed","usage":{"input_tokens":900,"cached_input_tokens":0,"output_tokens":40}}
`

// A CHECK TURN THAT FAILED, OR ANSWERED IN ANOTHER CONVERSATION, IS NOT THE RUN'S
// WORK: its edits are not committed, and the push carries the first turn's. Only
// a turn cut short by the run's own end or by its sandbox is committed after it.
//
// MUTATION: mark every failed check turn cut and its edits are committed and pushed.
func TestSandboxRun_AFailedCheckTurnIsNotDelivered(t *testing.T) {
	const third = "3333333333333333333333333333333333333333"
	for name, second := range map[string]client.Ran{
		"its harness exited 1":                {ExitCode: 1, Stdout: halfDone, Stderr: "error: stream disconnected\n"},
		"it answered in another conversation": {Stdout: otherThread},
	} {
		t.Run(name, func(t *testing.T) {
			tip, ran := theSHA, 0
			p := &pod{}
			p.answer = func(argv []string) client.Ran {
				switch {
				case slices.Contains(argv, "--skip-git-repo-check"):
					if ran++; ran == 1 {
						return client.Ran{Stdout: halfDone}
					}
					return second
				case len(argv) == 2 && argv[0] == "cat" && argv[1] == answerPath:
					return client.Ran{Stdout: "Now let me run the tests:\n"}
				case slices.Contains(argv, "commit"):
					if ran == 1 {
						tip = newSHA
					} else {
						tip = third // what the failed turn left, were it committed
					}
				case slices.Contains(argv, "rev-parse"):
					return client.Ran{Stdout: tip + "\n"}
				}
				return client.Ran{}
			}
			servePod(t, p)
			res, err := sandboxRunner{}.Run(context.Background(), "acme", "u_1", aRun(), nil)
			if err != nil {
				t.Fatalf("run: %v", err)
			}
			if res.CommitSha != newSHA || lastAt(p, "commit") > turnAt(p, 1) {
				t.Fatalf("the failed check turn was delivered: sha %s, last commit at %d after the turn at %d:\n%s",
					res.CommitSha, lastAt(p, "commit"), turnAt(p, 1), strings.Join(p.lines(), "\n"))
			}
			if !p.gitVerb("push") {
				t.Fatal("the first turn's work was not pushed")
			}
		})
	}
}

// A POD REPLACED UNDER THE COMMIT AFTER A CHECK TURN IS REARMED TOO. The replaced
// pod has lost $HOME, so git refuses the checkout ("dubious ownership") until its
// trust is put back, and the first turn's commit is pushed.
//
// MUTATION: rearm only when the turn itself failed and the push never happens.
func TestSandboxRun_APodReplacedAtTheCommitIsRearmed(t *testing.T) {
	var mu sync.Mutex
	adds, replaced, trusted := 0, false, false
	p := checking(nil)
	inner := p.answer
	p.answer = func(argv []string) client.Ran {
		mu.Lock()
		if slices.Contains(argv, "safe.directory") && replaced {
			trusted = true
		}
		mu.Unlock()
		return inner(argv)
	}
	p.refuse = func(argv []string) error {
		mu.Lock()
		defer mu.Unlock()
		if argv[0] == "git" && slices.Contains(argv, "add") {
			if adds++; adds == 2 {
				replaced = true
				return zip.Errorf(http.StatusServiceUnavailable, "sandbox sbx_1 lost its pod (Evicted) and has a new one under the same id")
			}
		}
		if replaced && !trusted && argv[0] == "git" && !slices.Contains(argv, "config") {
			return zip.Errorf(http.StatusBadGateway, "fatal: detected dubious ownership in repository at '/work'")
		}
		return nil
	}
	servePod(t, p)
	res, err := sandboxRunner{}.Run(context.Background(), "acme", "u_1", aRun(), nil)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !res.Changed || !p.gitVerb("push") || res.OK {
		t.Fatalf("changed %v, pushed %v, ok %v (%s): want the first turn's work pushed and the run failed",
			res.Changed, p.gitVerb("push"), res.OK, res.Error)
	}
}
