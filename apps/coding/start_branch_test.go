package coding

// start_branch_test.go pins which branch a run writes and whether it proposes
// it: the name a caller gives is the name the run pushes, a follow-up carries on
// the branch its earlier run wrote, and a run asked to open no pull request
// pushes and stops there.

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/apps/agents"
	"github.com/hanzoai/cloud/client"
	"github.com/zap-proto/zip"
)

// A NAME BECOMES A BRANCH UNDER agent/, OR IS REFUSED. It is the one place a
// caller's word reaches a ref a run writes, so the namespace is a property of this
// function and not of every caller that remembers to check.
func TestAgentBranch(t *testing.T) {
	for name, c := range map[string]struct {
		in, want string
		bad      bool
	}{
		"none is none":                     {"", "", false},
		"blank is none":                    {"  ", "", false},
		"a name gets the prefix":           {"fix-header", "agent/fix-header", false},
		"the prefix is not doubled":        {"agent/fix-header", "agent/fix-header", false},
		"dots and underscores are names":   {"v1.2_rc", "agent/v1.2_rc", false},
		"main is a name under agent/":      {"main", "agent/main", false},
		"a slash would nest":               {"fix/header", "", true},
		"a dot-dot is not a ref":           {"a..b", "", true},
		"a lock suffix is not a ref":       {"x.lock", "", true},
		"a trailing dot is not a ref":      {"x.", "", true},
		"a leading dash is a flag":         {"-x", "", true},
		"an option is a flag":              {"--upload-pack=x", "", true},
		"a space is not a ref":             {"a b", "", true},
		"the prefix alone names nothing":   {"agent/", "", true},
		"the parent of the namespace":      {"../main", "", true},
		"the unprefixed default":           {"refs/heads/main", "", true},
		"a name past the bound is refused": {strings.Repeat("a", 65), "", true},
	} {
		got, err := AgentBranch(c.in)
		if (err != nil) != c.bad || got != c.want {
			t.Errorf("%s: AgentBranch(%q) = %q, %v; want %q, refused=%v", name, c.in, got, err, c.want, c.bad)
		}
		if got != "" && !strings.HasPrefix(got, agentPrefix) {
			t.Errorf("%s: %q is outside the namespace a run may write", name, got)
		}
	}
}

// gitHubRun is the rig of a run on hanzo-apps/site whose pushes land: the branch
// a push names is on GitHub from then on, as it is for a real one.
func gitHubRun(t *testing.T, held map[string]string) *rig {
	t.Helper()
	r := newRig()
	r.admit = pushes(map[string]string{"sub-zoe": "zoe-gh"}, nil)
	r.refs = held
	tip := theSHA
	r.pod.answer = func(argv []string) client.Ran {
		switch {
		case slices.Contains(argv, "commit"):
			tip = newSHA
		case slices.Contains(argv, "push"):
			ref := argv[len(argv)-1]
			r.mu.Lock()
			r.refs[strings.TrimPrefix(ref, "HEAD:refs/heads/")] = newSHA
			r.mu.Unlock()
		case slices.Contains(argv, "rev-parse"):
			return client.Ran{Stdout: tip + "\n"}
		case slices.Contains(argv, "diff"):
			return client.Ran{Stdout: " 1 file changed\n"}
		}
		return client.Ran{}
	}
	r.serve(t)
	return r
}

// A BRANCH THE CALLER NAMES IS THE BRANCH THE RUN PUSHES, and the one its pull
// request, its session and its answer all say. It was ignored: every run wrote a
// branch named after its session, whatever was asked.
func TestStart_ARunWritesTheBranchItWasNamed(t *testing.T) {
	r := gitHubRun(t, map[string]string{})
	acc, err := r.startAs(t, asCaller("sub-zoe"), "sub-zoe",
		client.CodingStartIn{Repo: "github.com/hanzo-apps/site", Prompt: "fix the header", Branch: "fix-header", Pull: true})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if acc.Branch != "agent/fix-header" {
		t.Fatalf("the run answered branch %q, want the one it was named", acc.Branch)
	}
	if push, ok := r.pod.did(" push "); !ok || !strings.HasSuffix(push, " HEAD:refs/heads/agent/fix-header") {
		t.Fatalf("the run did not push the named branch:\n%s", strings.Join(r.pod.lines(), "\n"))
	}
	if len(r.pulls) != 1 || r.pulls[0]["head"] != "agent/fix-header" || r.pulls[0]["base"] != "trunk" {
		t.Fatalf("the proposal is not the named branch into the default: %v", r.pulls)
	}
	if got := r.sessions.tagged(); got.Branch != "agent/fix-header" || got.Base != "trunk" {
		t.Fatalf("the session says %+v, want the named branch off the default", got)
	}
	if clone, _ := r.pod.did(" clone "); !strings.Contains(clone, " -b trunk ") {
		t.Fatalf("a branch that does not exist yet is cut from the default, not cloned: %q", clone)
	}
}

// A NAME THAT IS NOT A BRANCH A RUN MAY WRITE IS REFUSED BEFORE ANYTHING IS SPENT:
// no session is opened and no sandbox leased.
func TestStart_ABranchOutsideTheNamespaceIsRefused(t *testing.T) {
	for _, name := range []string{"../main", "a/b", "-x", "a..b", "x.lock"} {
		r := gitHubRun(t, map[string]string{})
		if _, err := r.startAs(t, asCaller("sub-zoe"), "sub-zoe",
			client.CodingStartIn{Repo: "github.com/hanzo-apps/site", Prompt: "fix", Branch: name}); err == nil {
			t.Fatalf("branch %q was accepted", name)
		}
		r.sessions.mu.Lock()
		opened := len(r.sessions.opened)
		r.sessions.mu.Unlock()
		r.pod.mu.Lock()
		leased := len(r.pod.leased)
		r.pod.mu.Unlock()
		if opened != 0 || leased != 0 {
			t.Fatalf("branch %q opened %d sessions and leased %d sandboxes before it was refused", name, opened, leased)
		}
	}
}

// A BRANCH THAT ALREADY EXISTS IS CARRIED ON BY A RUN THAT NAMES IT (#553), as the
// op says: the checkout starts on it, the run adds a commit and pushes it without
// force, and the proposal still targets the default. It was refused unless the run
// was chained by `after` onto the session that wrote the branch.
//
// MUTATION: refuse a named branch that exists in Start and the run never starts.
func TestStart_ANamedBranchThatExistsIsCarriedOn(t *testing.T) {
	r := gitHubRun(t, map[string]string{"agent/fix-header": theSHA})
	acc, err := r.startAs(t, asCaller("sub-zoe"), "sub-zoe",
		client.CodingStartIn{Repo: "github.com/hanzo-apps/site", Prompt: "and the footer", Branch: "agent/fix-header", Pull: true})
	if err != nil {
		t.Fatalf("a run naming a branch that exists was refused: %v", err)
	}
	if acc.Branch != "agent/fix-header" {
		t.Fatalf("the run answered branch %q, want the one it named", acc.Branch)
	}
	if clone, ok := r.pod.did(" clone "); !ok || !strings.Contains(clone, " -b agent/fix-header ") {
		t.Fatalf("the run was not started on the branch that exists: %q", clone)
	}
	push, ok := r.pod.did(" push ")
	if !ok || !strings.HasSuffix(push, " HEAD:refs/heads/agent/fix-header") || strings.Contains(push, "--force") || strings.Contains(push, " +HEAD") {
		t.Fatalf("the run did not add to the branch without force: %q", push)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.pulls) != 1 || r.pulls[0]["head"] != "agent/fix-header" || r.pulls[0]["base"] != "trunk" {
		t.Fatalf("the proposal is not the named branch into the default: %v", r.pulls)
	}
}

// A RUN MAY RESOLVE AN ISSUE IN ANOTHER REPOSITORY (#553): an iam run names
// hanzo-inc/cloud#464 and its pull request closes that one, not #464 of its own
// repository. A reference that is not an issue is refused before a session is
// opened or a sandbox leased.
//
// MUTATION: write `Closes #` + the number in prBody and the proposal closes the
// wrong issue.
func TestStart_AnIssueInAnotherRepositoryIsTheOneClosed(t *testing.T) {
	r := gitHubRun(t, map[string]string{})
	if _, err := r.startAs(t, asCaller("sub-zoe"), "sub-zoe", client.CodingStartIn{Repo: "github.com/hanzo-apps/site",
		Prompt: "fix the header", Issue: "hanzo-inc/cloud#464", Pull: true}); err != nil {
		t.Fatalf("start: %v", err)
	}
	r.mu.Lock()
	pulls := r.pulls
	r.mu.Unlock()
	if len(pulls) != 1 || !strings.Contains(pulls[0]["body"], "\nCloses hanzo-inc/cloud#464\n") {
		t.Fatalf("the proposal does not close the issue it was named for: %v", pulls)
	}

	bad := gitHubRun(t, map[string]string{})
	if _, err := bad.startAs(t, asCaller("sub-zoe"), "sub-zoe", client.CodingStartIn{Repo: "github.com/hanzo-apps/site",
		Prompt: "fix the header", Issue: "cloud#464\nCloses #1"}); err == nil {
		t.Fatal("a reference that is not an issue was accepted")
	}
	bad.sessions.mu.Lock()
	opened := len(bad.sessions.opened)
	bad.sessions.mu.Unlock()
	if opened != 0 {
		t.Fatalf("a refused issue opened %d sessions", opened)
	}
}

// THE RUN THAT WROTE A BRANCH CARRIES IT ON, by `after`, and naming that same branch
// beside it is the same follow-up: not a pull request from the branch into itself.
// The run's checkout starts on the branch, so its push adds to it and cannot be
// refused as a rival history.
//
// MUTATION: drop `|| named == earlier.Branch` and the base is the branch itself.
func TestStart_TheRunThatWroteABranchCarriesItOn(t *testing.T) {
	const earlierBranch = "agent/fix-header"
	was := visibleRun
	visibleRun = func(_ context.Context, id string) (agents.Session, error) {
		return agents.Session{ID: id, Kind: kindCoding, Repo: "hanzo-apps/site", Base: "trunk", Branch: earlierBranch}, nil
	}
	t.Cleanup(func() { visibleRun = was })
	r := gitHubRun(t, map[string]string{earlierBranch: theSHA})
	if _, err := r.startAs(t, asCaller("sub-zoe"), "sub-zoe",
		client.CodingStartIn{Repo: "github.com/hanzo-apps/site", After: "sess_earlier", Prompt: "and the footer",
			Branch: earlierBranch, Pull: true}); err != nil {
		t.Fatalf("start: %v", err)
	}
	clone, ok := r.pod.did(" clone ")
	if !ok || !strings.Contains(clone, " -b agent/fix-header ") {
		t.Fatalf("the run was not started on the branch that exists: %q", clone)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.pulls) != 1 || r.pulls[0]["base"] != "trunk" || r.pulls[0]["head"] != "agent/fix-header" {
		t.Fatalf("the proposal must still target the default, not the branch itself: %v", r.pulls)
	}
}

// A FOLLOW-UP WRITES THE BRANCH ITS EARLIER RUN WROTE. It cut a new one from it,
// so one piece of work became a chain of branches and pull requests; now the same
// branch is pushed to and the proposal keeps the base the first run had.
func TestStart_AFollowUpWritesTheBranchOfItsEarlierRun(t *testing.T) {
	const earlierBranch = "agent/fix-header"
	for name, c := range map[string]struct {
		repo         string
		held         map[string]string
		wantBranch   string
		wantClone    string
		wantPullBase string
	}{
		"pushed, so it is built on": {
			"hanzo-apps/site", map[string]string{earlierBranch: theSHA, "dev": theSHA},
			earlierBranch, " -b " + earlierBranch + " ", "dev"},
		"never pushed, so it is cut again from the earlier base": {
			"hanzo-apps/site", map[string]string{"dev": theSHA},
			earlierBranch, " -b dev ", "dev"},
		"another repository's branch is not carried over": {
			"hanzo-apps/other", map[string]string{"dev": theSHA, "trunk": theSHA},
			"", "", "dev"},
	} {
		t.Run(name, func(t *testing.T) {
			was := visibleRun
			visibleRun = func(_ context.Context, id string) (agents.Session, error) {
				return agents.Session{ID: id, Kind: kindCoding, Repo: c.repo, Base: "dev", Branch: earlierBranch}, nil
			}
			t.Cleanup(func() { visibleRun = was })
			r := gitHubRun(t, c.held)
			acc, err := r.startAs(t, asCaller("sub-zoe"), "sub-zoe",
				client.CodingStartIn{Repo: "github.com/hanzo-apps/site", After: "sess_earlier", Prompt: "now add tests", Pull: true})
			if err != nil {
				t.Fatalf("start: %v", err)
			}
			if c.repo != "hanzo-apps/site" {
				if acc.Branch == earlierBranch {
					t.Fatalf("a follow-up on another repository wrote the earlier run's branch %q", acc.Branch)
				}
				return
			}
			if acc.Branch != c.wantBranch {
				t.Fatalf("the follow-up wrote %q, want its earlier run's %q", acc.Branch, c.wantBranch)
			}
			if clone, _ := r.pod.did(" clone "); !strings.Contains(clone, c.wantClone) {
				t.Fatalf("the follow-up cloned %q, want %q", clone, c.wantClone)
			}
			if push, ok := r.pod.did(" push "); !ok || !strings.HasSuffix(push, " HEAD:refs/heads/"+earlierBranch) {
				t.Fatalf("the follow-up did not push the earlier branch:\n%s", strings.Join(r.pod.lines(), "\n"))
			}
			r.mu.Lock()
			defer r.mu.Unlock()
			if len(r.pulls) != 1 || r.pulls[0]["head"] != earlierBranch || r.pulls[0]["base"] != c.wantPullBase {
				t.Fatalf("the follow-up proposed %v, want %s into %s", r.pulls, earlierBranch, c.wantPullBase)
			}
			if got := r.sessions.tagged(); got.Branch != earlierBranch || got.Base != c.wantPullBase {
				t.Fatalf("the session says %+v, want the earlier branch and base", got)
			}
		})
	}
}

// A CHECKOUT ALREADY ON THE RUN'S BRANCH STAYS THERE: a follow-up resumes a tree
// that is on it, or clones it, and cutting the branch again would fail on a name
// that exists. The first commit the branch started from is kept, not moved.
func TestWorkspace_ABranchThatIsCheckedOutIsNotCutAgain(t *testing.T) {
	const branch = "agent/fix-header"
	for name, c := range map[string]struct {
		checkedOut, marked bool
		held               bool // the checkout holds the branch locally, though it is not on it
	}{
		"a fresh clone is cut and marked":         {false, false, false},
		"a resumed tree on the branch is left":    {true, true, true},
		"a clone of the branch is marked, once":   {true, false, true},
		"a tree off the branch is cut, not moved": {false, true, false},
		// A tree off its branch that holds it is switched to, never cut again.
		// MUTATION: cut whenever the checkout is off the branch and this runs
		// `switch -c` on a name that exists.
		"a tree off a branch it holds is switched to it": {false, true, true},
	} {
		t.Run(name, func(t *testing.T) {
			r := gitHubRun(t, map[string]string{})
			answer := r.pod.answer
			r.pod.answer = func(argv []string) client.Ran {
				switch {
				case slices.Contains(argv, "symbolic-ref"):
					if c.checkedOut {
						return client.Ran{Stdout: branch + "\n"}
					}
					return client.Ran{Stdout: "trunk\n"}
				case slices.Contains(argv, "for-each-ref") && slices.Contains(argv, headsRef+branch):
					if c.held {
						return client.Ran{Stdout: headsRef + branch + "\n"}
					}
					return client.Ran{}
				case slices.Contains(argv, "--verify") && slices.Contains(argv, baseRef+branch):
					if !c.marked {
						return client.Ran{ExitCode: 1}
					}
				}
				return answer(argv)
			}
			if _, err := r.startAs(t, asCaller("sub-zoe"), "sub-zoe",
				client.CodingStartIn{Repo: "github.com/hanzo-apps/site", Prompt: "x", Branch: "fix-header"}); err != nil {
				t.Fatalf("start: %v", err)
			}
			_, cut := r.pod.did("switch -c " + branch)
			_, switched := r.pod.did("switch " + branch)
			_, marked := r.pod.did("update-ref " + baseRef + branch)
			if cut == (c.checkedOut || c.held) {
				t.Fatalf("checked out=%v, held=%v but the branch was cut=%v:\n%s", c.checkedOut, c.held, cut, strings.Join(r.pod.lines(), "\n"))
			}
			if !c.checkedOut && c.held && !switched {
				t.Fatalf("a held branch off which the tree stands was not switched to:\n%s", strings.Join(r.pod.lines(), "\n"))
			}
			if marked == c.marked {
				t.Fatalf("marked=%v but where it started was written=%v:\n%s", c.marked, marked, strings.Join(r.pod.lines(), "\n"))
			}
		})
	}
}

// A RUN PUSHES ITS BRANCH AND STOPS THERE UNLESS IT IS ASKED FOR A PULL REQUEST.
// The pull request is the step after the work, taken when the work is ready: the
// branch is verified and reported, and nothing is proposed, on the host or the
// board, until a run is started with pull set.
//
// MUTATION: open the proposal on `!c.pull` and the unset case opens one.
func TestStart_ARunOpensNoPullRequestUnlessAskedTo(t *testing.T) {
	for name, c := range map[string]struct {
		pull  bool
		pulls int
	}{
		"unset opens none": {false, 0},
		"true opens one":   {true, 1},
	} {
		t.Run(name, func(t *testing.T) {
			r := gitHubRun(t, map[string]string{})
			acc, err := r.startAs(t, asCaller("sub-zoe"), "sub-zoe",
				client.CodingStartIn{Repo: "github.com/hanzo-apps/site", Prompt: "fix the header", Pull: c.pull})
			if err != nil {
				t.Fatalf("start: %v", err)
			}
			r.mu.Lock()
			defer r.mu.Unlock()
			if _, ok := r.pod.did(" push "); !ok {
				t.Fatalf("the branch was not pushed:\n%s", strings.Join(r.pod.lines(), "\n"))
			}
			if len(r.pulls) != c.pulls || len(r.todo.inputs) != c.pulls {
				t.Fatalf("pull=%v proposed %d on the host and %d on the board, want %d", c.pull, len(r.pulls), len(r.todo.inputs), c.pulls)
			}
			if got := r.sessions.tagged(); got.Branch != acc.Branch || (c.pulls == 0) != (got.PR == "") {
				t.Fatalf("pull=%v: the session says %+v", c.pull, got)
			}
			if c.pulls == 0 {
				if n := len(r.sessions.closes); n != 1 || r.sessions.closes[0].status != statusDone {
					t.Fatalf("the run with no pull request did not end done: %+v", r.sessions.closes)
				}
			}
		})
	}
}

// A ROUTED RUN CARRIES THE ASK TO THE COMPLETION, which runs minutes later in
// another process: the machine pushes, and cloud verifies — and proposes only if
// it was asked to.
func TestRouted_ARunOpensNoPullRequestUnlessAskedTo(t *testing.T) {
	r := newRig()
	r.admit = pushes(nil, admins("hanzo/sub-bob"))
	r.tip = newSHA
	r.serve(t)

	var routed RoutedRun
	d := Dispatcher{
		Sessions: r.sessions, PR: planePR{},
		Route:      func(_ context.Context, run RoutedRun) error { routed = run; return nil },
		TargetGate: func(context.Context, string, string, string, bool) error { return nil },
	}
	res := d.Run(context.Background(), Req{
		Org: "hanzo", UserID: "sub-bob", Repo: "hanzo-apps/site", Prompt: "fix it", TargetID: "tgt_1",
		Actor: "sub-bob", Home: "hanzo-apps/site", SessionID: "sess_abc123def456",
		Branch: "agent/fix-header",
	})
	if !res.OK || routed.Branch != "agent/fix-header" || routed.Pull {
		t.Fatalf("the machine was not handed the named branch, or was handed an ask nobody made: ok=%v %+v (%s)", res.OK, routed, res.Error)
	}

	d.finalizeRouted(context.Background(), routed, RoutedResult{OK: true, Changed: true, CommitSha: newSHA})
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.pulls) != 0 {
		t.Fatalf("a routed run not asked for a pull request proposed %v", r.pulls)
	}
	if n := len(r.sessions.closes); n != 1 || r.sessions.closes[0].status != statusDone {
		t.Fatalf("the routed session did not close done: %+v", r.sessions.closes)
	}
}

// ONE RUN AT A TIME IN ONE SANDBOX. A follow-up of a run still working, or paused
// with its work waiting to be continued, would share its sandbox and push to its
// branch beside it: it is refused 409 before anything is opened, whoever named the
// run — a person, an automation, or a model in a conversation.
func TestStart_ARunStillWorkingIsNotCarriedOn(t *testing.T) {
	for _, status := range []string{agents.StatusRunning, agents.StatusPaused} {
		t.Run(status, func(t *testing.T) {
			was := visibleRun
			visibleRun = func(_ context.Context, id string) (agents.Session, error) {
				return agents.Session{ID: id, Kind: kindCoding, Repo: "hanzo-apps/site", Status: status, Branch: "agent/fix-header"}, nil
			}
			t.Cleanup(func() { visibleRun = was })
			r := gitHubRun(t, map[string]string{"agent/fix-header": theSHA})
			_, err := r.startAs(t, asCaller("sub-zoe"), "sub-zoe",
				client.CodingStartIn{Repo: "github.com/hanzo-apps/site", After: "sess_earlier", Prompt: "and the footer"})
			var he *zip.HTTPError
			if !errors.As(err, &he) || he.Status != http.StatusConflict {
				t.Fatalf("carrying on a %s run answered %v, want 409", status, err)
			}
			r.sessions.mu.Lock()
			opened := len(r.sessions.opened)
			r.sessions.mu.Unlock()
			if opened != 0 {
				t.Fatalf("a refused follow-up opened %d sessions", opened)
			}
		})
	}
}

// WHEN ANOTHER RUN HOLDS THE EARLIER RUN'S SANDBOX, A NEW RUN AUTOMATICALLY
// PROVISIONS A FRESH PARALLEL SANDBOX AND ISOLATES ONTO ITS OWN BRANCH instead
// of refusing 409 "another run is still working in that sandbox".
func TestStart_ABusySandboxProvisionsAFreshParallelSandbox(t *testing.T) {
	was := visibleRun
	visibleRun = func(_ context.Context, id string) (agents.Session, error) {
		return agents.Session{ID: id, Kind: kindCoding, Repo: "hanzo-apps/site", Status: agents.StatusStopped,
			Base: "main", Branch: "agent/fix-header", Box: "box_a"}, nil
	}
	t.Cleanup(func() { visibleRun = was })
	r := gitHubRun(t, map[string]string{"agent/fix-header": theSHA, "main": theSHA})
	r.sessions.mu.Lock()
	r.sessions.busy = map[string]bool{"box_a": true}
	r.sessions.mu.Unlock()
	acc, err := r.startAs(t, asCaller("sub-zoe"), "sub-zoe",
		client.CodingStartIn{Repo: "github.com/hanzo-apps/site", After: "sess_a", Prompt: "and the footer"})
	if err != nil {
		t.Fatalf("parallel run after busy sandbox failed: %v", err)
	}
	if acc.Branch == "" || acc.Branch == "agent/fix-header" || !strings.HasPrefix(acc.Branch, "agent/") {
		t.Fatalf("parallel run must isolate onto its own agent branch, got %q", acc.Branch)
	}
	r.sessions.mu.Lock()
	defer r.sessions.mu.Unlock()
	for _, tg := range r.sessions.tags {
		if tg.Sandbox == "box_a" {
			t.Fatalf("the parallel run must not claim the busy sandbox box_a: %+v", r.sessions.tags)
		}
	}
	r.pod.mu.Lock()
	leased := append([]client.LeaseIn(nil), r.pod.leased...)
	r.pod.mu.Unlock()
	if len(leased) != 1 || leased[0].ID != "" {
		t.Fatalf("expected one fresh parallel sandbox lease (ID=\"\"), got %+v", leased)
	}
}

// A PARALLEL BRANCH, DIFFERENT REPO, OR EXPLICIT `parallel: true` RUNS IN
// PARALLEL EVEN WHILE `after` IS STILL RUNNING OR PAUSED, provisioning a fresh
// sandbox and its own isolated branch without failing 409.
func TestStart_ActiveSessionAllowsParallelBranchRepoOrFlag(t *testing.T) {
	for name, tc := range map[string]struct {
		earlierRepo string
		in          client.CodingStartIn
		wantBranch  string
	}{
		"explicit parallel flag": {
			earlierRepo: "hanzo-apps/site",
			in:          client.CodingStartIn{Repo: "github.com/hanzo-apps/site", After: "sess_earlier", Prompt: "parallel task", Parallel: true},
		},
		"distinct named branch": {
			earlierRepo: "hanzo-apps/site",
			in:          client.CodingStartIn{Repo: "github.com/hanzo-apps/site", After: "sess_earlier", Branch: "other-work", Prompt: "branch task"},
			wantBranch:  "agent/other-work",
		},
		"different repository": {
			earlierRepo: "hanzo-apps/other",
			in:          client.CodingStartIn{Repo: "github.com/hanzo-apps/site", After: "sess_earlier", Prompt: "other repo task"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			was := visibleRun
			visibleRun = func(_ context.Context, id string) (agents.Session, error) {
				return agents.Session{ID: id, Kind: kindCoding, Repo: tc.earlierRepo, Status: agents.StatusRunning,
					Base: "main", Branch: "agent/fix-header", Box: "box_a"}, nil
			}
			t.Cleanup(func() { visibleRun = was })
			r := gitHubRun(t, map[string]string{"agent/fix-header": theSHA, "main": theSHA})
			acc, err := r.startAs(t, asCaller("sub-zoe"), "sub-zoe", tc.in)
			if err != nil {
				t.Fatalf("parallel start failed: %v", err)
			}
			if acc.Branch == "agent/fix-header" {
				t.Fatalf("parallel run reused active branch %q", acc.Branch)
			}
			if tc.wantBranch != "" && acc.Branch != tc.wantBranch {
				t.Fatalf("branch = %q, want %q", acc.Branch, tc.wantBranch)
			}
			r.pod.mu.Lock()
			leased := append([]client.LeaseIn(nil), r.pod.leased...)
			r.pod.mu.Unlock()
			if len(leased) != 1 || leased[0].ID != "" {
				t.Fatalf("expected fresh sandbox lease (ID=\"\"), got %+v", leased)
			}
		})
	}
}

// PASSING `steer: true` WITH `after` REDIRECTS OR QUEUES INTO THE ACTIVE
// SESSION INSTEAD OF FAILING 409.
func TestStart_SteerFlagRedirectsActiveSession(t *testing.T) {
	const activeID = "sess_077ebdeb5444162f21dfc10b4306db68"
	was := visibleRun
	visibleRun = func(_ context.Context, id string) (agents.Session, error) {
		return agents.Session{ID: id, Kind: kindCoding, Repo: "hanzo-apps/site", Status: agents.StatusRunning,
			Base: "main", Branch: "agent/fix-header", Box: "box_a"}, nil
	}
	t.Cleanup(func() { visibleRun = was })

	r := gitHubRun(t, map[string]string{"agent/fix-header": theSHA})
	var steeredText string
	ctx, stop := context.WithCancelCause(context.Background())
	t.Cleanup(func() { stop(nil) })
	f := &flight{
		org: "hanzo", subject: "sub-zoe", session: activeID, cancel: stop,
		done: make(chan struct{}), ctx: ctx,
		in: client.CodingStartIn{Repo: "github.com/hanzo-apps/site", Prompt: "initial task"},
	}
	f.setSteerer(func(text string) bool {
		steeredText = text
		return true
	})
	fly(f)
	t.Cleanup(func() { f.forget() })

	acc, err := Start(asCaller("sub-zoe"), "hanzo", "sub-zoe",
		client.CodingStartIn{Repo: "github.com/hanzo-apps/site", After: activeID, Prompt: "also update the header", Steer: true}, nil)
	if err != nil {
		t.Fatalf("start with steer=true failed: %v", err)
	}
	if !acc.Steered || acc.SessionID != activeID || acc.Branch != "agent/fix-header" {
		t.Fatalf("unexpected steered acceptance: %+v", acc)
	}
	if steeredText != "also update the header" {
		t.Fatalf("steerer received %q, want %q", steeredText, "also update the header")
	}
	r.sessions.mu.Lock()
	opened := len(r.sessions.opened)
	r.sessions.mu.Unlock()
	if opened != 0 {
		t.Fatalf("steering an active session must not open a second session, opened %d", opened)
	}
}

// TERMINAL STATUS EVENTS INCLUDE LIVE SANDBOX PREVIEW URLS so /dev's Preview
// pane immediately reflects the sandbox's running app without polling artifacts.
func TestFinishedIncludesPreviewLinks(t *testing.T) {
	st := finished(RunResult{
		OK: true,
		Artifacts: []client.Artifact{
			{Name: "changes.patch", Kind: kindPatch},
			{Name: "port 3000", Kind: kindPreview, URL: "https://sandbox-2a8bb714-preview-3000.hanzo.app/", Port: 3000, Sandbox: "m_2a8bb714a84c1c5b95b1a2fa"},
		},
	}, map[string]any{"status": "done"})
	if st["preview"] != "https://sandbox-2a8bb714-preview-3000.hanzo.app/" {
		t.Fatalf("preview = %v, want sandbox preview url", st["preview"])
	}
	previews, ok := st["previews"].([]previewLink)
	if !ok || len(previews) != 1 || previews[0].Port != 3000 || previews[0].Sandbox != "m_2a8bb714a84c1c5b95b1a2fa" {
		t.Fatalf("previews = %#v, want 1 port-3000 entry", st["previews"])
	}
}

// A run carried on in a Dev conversation is born in it: the conversation the call
// names, or the one the run it carries on is in.
func TestStart_ARunIsBornInItsConversation(t *testing.T) {
	for name, c := range map[string]struct {
		in      client.CodingStartIn
		earlier agents.Session
		want    string
	}{
		"named":            {client.CodingStartIn{Repo: "github.com/hanzo-apps/site", Prompt: "fix it", Conversation: "sess_talk"}, agents.Session{}, "sess_talk"},
		"carried on":       {client.CodingStartIn{Repo: "github.com/hanzo-apps/site", Prompt: "and the footer", After: "sess_a"}, agents.Session{ID: "sess_a", Kind: kindCoding, Repo: "hanzo-apps/site", Status: agents.StatusDone, Channel: "app", ParentID: "sess_talk", RootID: "sess_talk"}, "sess_talk"},
		"of its own":       {client.CodingStartIn{Repo: "github.com/hanzo-apps/site", Prompt: "fix it"}, agents.Session{}, ""},
		"carried on alone": {client.CodingStartIn{Repo: "github.com/hanzo-apps/site", Prompt: "and the footer", After: "sess_a"}, agents.Session{ID: "sess_a", Kind: kindCoding, Repo: "hanzo-apps/site", Status: agents.StatusDone, RootID: "sess_a"}, ""},
	} {
		t.Run(name, func(t *testing.T) {
			was := visibleRun
			visibleRun = func(_ context.Context, id string) (agents.Session, error) { return c.earlier, nil }
			t.Cleanup(func() { visibleRun = was })
			r := gitHubRun(t, map[string]string{})
			if _, err := r.startAs(t, asCaller("sub-zoe"), "sub-zoe", c.in); err != nil {
				t.Fatal(err)
			}
			r.sessions.mu.Lock()
			defer r.sessions.mu.Unlock()
			if len(r.sessions.opened) != 1 || r.sessions.opened[0].conversation != c.want {
				t.Fatalf("the run was opened %+v, want in %q", r.sessions.opened, c.want)
			}
		})
	}
}
