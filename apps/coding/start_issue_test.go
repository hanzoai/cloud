package coding

// start_issue_test.go pins which branch a run writes (#820): the one a resumed run wrote, else the
// one its caller named, else its issue's, else its session's, and that two runs on one branch are one
// pull request.

import (
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/apps/agents"
	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/coord/coordtest"
)

func TestBranchForIssue(t *testing.T) {
	for _, c := range []struct{ issue, want string }{
		{"#764", "agent/issue-764"},
		{"hanzo-inc/cloud#764", "agent/issue-764-" + digestOf("hanzo-inc/cloud")},
		{"Hanzo-Inc/Cloud#764", "agent/issue-764-" + digestOf("hanzo-inc/cloud")},
		{"hanzoai/cloud#764", "agent/issue-764-" + digestOf("hanzoai/cloud")},
		{"", ""},
		{"764", ""},
		{"#", ""},
	} {
		if got := BranchForIssue(c.issue); got != c.want {
			t.Errorf("BranchForIssue(%q) = %q, want %q", c.issue, got, c.want)
		}
		if c.want != "" {
			if _, err := AgentBranch(c.want); err != nil {
				t.Errorf("%q is not a branch a run may write: %v", c.want, err)
			}
		}
	}
	if BranchForIssue("hanzo-inc/cloud#764") == BranchForIssue("hanzoai/cloud#764") || BranchForIssue("a/b#1") == BranchForIssue("#1") {
		t.Error("the same number in two repositories is one branch")
	}
}

func digestOf(repo string) string {
	b := BranchForIssue(repo + "#1")
	return strings.TrimPrefix(b, "agent/issue-1-")
}

// A run for an issue that names no branch writes that issue's, and one that names a branch writes
// that one: a caller's name is never overruled.
func TestStart_ARunForAnIssueWritesItsBranchUnlessOneIsNamed(t *testing.T) {
	for name, c := range map[string]struct {
		in   client.CodingStartIn
		want string
	}{
		"no branch, an issue":    {client.CodingStartIn{Issue: "#764"}, "agent/issue-764"},
		"a branch and an issue":  {client.CodingStartIn{Issue: "#764", Branch: "fix-header"}, "agent/fix-header"},
		"another repository's":   {client.CodingStartIn{Issue: "hanzo-inc/cloud#764"}, BranchForIssue("hanzo-inc/cloud#764")},
		"no branch and no issue": {client.CodingStartIn{}, "agent/abc123def456"},
		"a branch and no issue":  {client.CodingStartIn{Branch: "agent/fix-header"}, "agent/fix-header"},
	} {
		t.Run(name, func(t *testing.T) {
			r := gitHubRun(t, map[string]string{})
			c.in.Repo, c.in.Prompt, c.in.Pull = "github.com/hanzo-apps/site", "fix the header", true
			acc, err := r.startAs(t, asCaller("sub-zoe"), "sub-zoe", c.in)
			if err != nil {
				t.Fatalf("start: %v", err)
			}
			if acc.Branch != c.want {
				t.Fatalf("the run answered branch %q, want %q", acc.Branch, c.want)
			}
			if push, ok := r.pod.did(" push "); !ok || !strings.HasSuffix(push, " HEAD:refs/heads/"+c.want) {
				t.Fatalf("the run did not push %s:\n%s", c.want, strings.Join(r.pod.lines(), "\n"))
			}
		})
	}
}

// A retry of the work for an issue, with no branch named, meets the branch the first wrote and the
// pull request it opened: the second run carries on that branch and opens nothing.
//
// MUTATION: derive the branch from the session and the retry writes a branch of its own, and a
// second pull request.
func TestStart_ARetryForTheSameIssueIsOnePullRequest(t *testing.T) {
	r := gitHubRun(t, map[string]string{})
	r.onePull = true
	// Each run's commit is a new commit, as a run that changes something makes.
	var mu sync.Mutex
	tip, commits := theSHA, 0
	r.pod.answer = func(argv []string) client.Ran {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case slices.Contains(argv, "commit"):
			commits++
			tip = strings.Repeat("3", 39) + strconv.Itoa(commits)
		case slices.Contains(argv, "push"):
			r.mu.Lock()
			r.refs[strings.TrimPrefix(argv[len(argv)-1], "HEAD:refs/heads/")] = tip
			r.mu.Unlock()
		case slices.Contains(argv, "rev-parse"):
			return client.Ran{Stdout: tip + "\n"}
		case slices.Contains(argv, "diff"):
			return client.Ran{Stdout: " 1 file changed\n"}
		}
		return client.Ran{}
	}
	in := client.CodingStartIn{Repo: "github.com/hanzo-apps/site", Prompt: "fix the header", Issue: "#764", Pull: true}
	first, err := r.startAs(t, asCaller("sub-zoe"), "sub-zoe", in)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	second, err := r.startAs(t, asCaller("sub-zoe"), "sub-zoe", in)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if first.Branch != "agent/issue-764" || second.Branch != first.Branch {
		t.Fatalf("the runs wrote %q and %q, want two runs on agent/issue-764", first.Branch, second.Branch)
	}
	if clone := r.pod.lines(); !strings.Contains(strings.Join(clone, "\n"), " -b agent/issue-764 ") {
		t.Fatalf("the retry did not carry on the first's branch:\n%s", strings.Join(clone, "\n"))
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.pulls) != 1 || r.pulls[0]["head"] != "agent/issue-764" {
		t.Fatalf("the proposals are %v, want one for agent/issue-764", r.pulls)
	}
	opened := 0
	for _, e := range r.sessions.events {
		if strings.Contains(e.payload, `"url":"https://github.com/hanzo-apps/site/pull/9"`) {
			opened++
		}
	}
	if opened < 2 {
		t.Fatalf("%d runs ended on the pull request, want both", opened)
	}
}

// A run resumed or parked is on the branch it wrote, whatever branch the request now names.
//
// MUTATION: let the name win again and the resumed run pushes a branch nobody opened a pull request for.
func TestAResumedRunKeepsTheBranchItWrote(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		s := &depot{t: t, open: open}
		pod := coordtest.Memory(t)
		r := gitHubRun(t, map[string]string{"agent/abc": theSHA})
		keeping(t, s.replica(pod, "cloud-0", periods{}))
		x := agents.Session{ID: "sess_abc123def456", Org: "hanzo", Actor: "hanzo/sub-zoe", Kind: kindCoding, Box: "sbx_old",
			Repo: "hanzo-apps/site", Base: "trunk", Branch: "agent/abc", Status: agents.StatusRunning}
		engineOnce = sync.Once{}
		t.Cleanup(func() { engineOnce = sync.Once{} })
		if !Resume(asCaller("sub-zoe"), x, client.CodingStartIn{Prompt: "fix the header", Branch: "agent/other", Issue: "#764"}, nil) {
			t.Fatal("the run was not resumed")
		}
		flights.Lock()
		f := flights.m[x.ID]
		flights.Unlock()
		if f != nil {
			select {
			case <-f.done:
			case <-time.After(20 * time.Second):
				t.Fatal("the resumed run never ended")
			}
		}
		if push, ok := r.pod.did(" push "); !ok || !strings.HasSuffix(push, " HEAD:refs/heads/agent/abc") {
			t.Fatalf("the resumed run did not push the branch it wrote:\n%s", strings.Join(r.pod.lines(), "\n"))
		}
	})
}
