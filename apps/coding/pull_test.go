package coding

// pull_test.go — opening the pull request for the branch a run pushed, as the
// step after the work: no agent run, the existing one when there is one, and a
// refusal when the branch is not there to propose.

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/apps/agents"
	"github.com/hanzoai/cloud/client"
	"github.com/zap-proto/zip"
)

const (
	pullSession = "sess_abc123def456"
	pullBranch  = "agent/fix-header"
)

// aRunToPull stands up the rig of a run on hanzo-apps/site that pushed
// agent/fix-header and ended without a pull request, and the session as the
// sessions app would show it to its person.
func aRunToPull(t *testing.T, held map[string]string, session agents.Session) *rig {
	t.Helper()
	r := newRig()
	// The forge holds the repository as a mirror of the GitHub one the run is admitted on.
	r.origin = "https://github.com/hanzo-apps/site.git"
	r.admit = pushes(map[string]string{"sub-zoe": "zoe-gh"}, nil)
	r.refs = held
	r.serve(t)
	was := visibleRun
	visibleRun = func(_ context.Context, id string) (agents.Session, error) {
		if id == session.ID {
			return session, nil
		}
		return agents.Session{}, zip.ErrNotFound("session not found")
	}
	t.Cleanup(func() { visibleRun = was })
	return r
}

func pushedRun() agents.Session {
	return agents.Session{
		ID: pullSession, Org: "hanzo", Kind: kindCoding, Agent: "hanzo", Title: "fix the header",
		Repo: "hanzoai/site", Base: "main", Branch: pullBranch,
	}
}

// The pull request opens from the session's branch with no agent run: nothing is
// leased and nothing is run in a sandbox. Its number and address come back, the
// token it was opened with is revoked, and the session records the address so
// the changes it answers carry the link.
//
// MUTATION: return before open.Open and no pull request is opened.
func TestPull_OpensTheBranchOfARunWithNoAgentRun(t *testing.T) {
	r := aRunToPull(t, map[string]string{pullBranch: theSHA}, pushedRun())

	got, err := Pull(asCaller("sub-zoe"), &client.CodingPullIn{Session: pullSession})
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if got.Number != 9 || got.URL != "https://github.com/hanzo-apps/site/pull/9" || got.Base != "main" {
		t.Fatalf("the answer is %+v, want #9 at its address, into main", got)
	}
	if len(r.pulls) != 1 || r.pulls[0]["head"] != pullBranch || r.pulls[0]["base"] != "main" {
		t.Fatalf("GitHub was asked for %v, want the run's branch into main", r.pulls)
	}
	if r.pulls[0]["draft"] != "false" {
		t.Fatalf("a pull request nobody asked to be a draft was opened as one: %v", r.pulls[0])
	}
	if !strings.Contains(r.pulls[0]["title"], "fix the header") || !strings.Contains(r.pulls[0]["body"], "fix the header") {
		t.Fatalf("the title and body do not come from the run's task: %v", r.pulls[0])
	}
	if len(r.pod.lines()) != 0 || len(r.pod.leased) != 0 {
		t.Fatalf("opening a pull request ran the agent: leased %d, ran %v", len(r.pod.leased), r.pod.lines())
	}
	if len(r.revoked) != 1 {
		t.Fatalf("the token the pull request was opened with was not revoked: %v", r.revoked)
	}
	if tagged := r.sessions.tagged(); tagged.PR != got.URL {
		t.Fatalf("the session says its pull request is %q, want %q", tagged.PR, got.URL)
	}
}

// Asking again answers the pull request the run already has and opens no second:
// the address the session recorded is the one that comes back.
//
// MUTATION: drop the early return on a recorded pull request and a second one is opened.
func TestPull_AskingTwiceOpensOne(t *testing.T) {
	x := pushedRun()
	x.PR = "https://github.com/hanzo-apps/site/pull/9"
	r := aRunToPull(t, map[string]string{pullBranch: theSHA}, x)

	got, err := Pull(asCaller("sub-zoe"), &client.CodingPullIn{Session: pullSession})
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if got.Number != 9 || got.URL != x.PR {
		t.Fatalf("the run's own pull request came back as %+v", got)
	}
	if len(r.pulls) != 0 || len(r.minted) != 0 {
		t.Fatalf("asking for the pull request a run has opened another: %v (minted %v)", r.pulls, r.minted)
	}
}

// What the caller says about the request reaches the host: its title, body, the
// branch to propose into, and whether it is a draft.
func TestPull_WhatTheCallerSaysAboutItReachesTheHost(t *testing.T) {
	r := aRunToPull(t, map[string]string{pullBranch: theSHA}, pushedRun())

	if _, err := Pull(asCaller("sub-zoe"), &client.CodingPullIn{
		Session: pullSession, Title: "Header, for review", Body: "Ready.", Base: "develop", Draft: true,
	}); err != nil {
		t.Fatalf("pull: %v", err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	want := map[string]string{"title": "Header, for review", "body": "Ready.", "base": "develop", "draft": "true", "head": pullBranch}
	if len(r.pulls) != 1 {
		t.Fatalf("GitHub was asked %d times", len(r.pulls))
	}
	for k, v := range want {
		if r.pulls[0][k] != v {
			t.Errorf("%s reached GitHub as %q, want %q", k, r.pulls[0][k], v)
		}
	}
}

// A run with nothing to propose, or whose branch is not on its host yet, is 409
// and opens nothing; a caller GitHub does not admit is refused and opens nothing.
func TestPull_RefusesWhatItCannotPropose(t *testing.T) {
	status := func(err error) int {
		var he *zip.HTTPError
		if err != nil && errors.As(err, &he) {
			return he.Status
		}
		return 0
	}

	noBranch := pushedRun()
	noBranch.Branch = ""
	for name, c := range map[string]struct {
		run    agents.Session
		held   map[string]string
		admit  bool
		status int
	}{
		"a run that pushed no branch":      {noBranch, map[string]string{}, true, http.StatusConflict},
		"a branch GitHub does not hold":    {pushedRun(), map[string]string{}, true, http.StatusConflict},
		"a caller GitHub does not admit":   {pushedRun(), map[string]string{pullBranch: theSHA}, false, http.StatusForbidden},
		"a session that is not a run here": {agents.Session{ID: pullSession, Org: "hanzo", Kind: "chat"}, map[string]string{}, true, http.StatusNotFound},
	} {
		t.Run(name, func(t *testing.T) {
			r := aRunToPull(t, c.held, c.run)
			if !c.admit {
				r.admit = nil
			}
			_, err := Pull(asCaller("sub-zoe"), &client.CodingPullIn{Session: pullSession})
			if got := status(err); got != c.status {
				t.Fatalf("answered %d (%v), want %d", got, err, c.status)
			}
			r.mu.Lock()
			defer r.mu.Unlock()
			if len(r.pulls) != 0 {
				t.Fatalf("a pull request was opened anyway: %v", r.pulls)
			}
		})
	}
}

// Pull titles the proposal from the run's actual commit subject when the caller
// supplies no explicit title, and does not double-prefix the repo name when
// falling back to a session title that already carries it.
func TestPull_TitlesFromCommitSubjectBeforeSessionPrompt(t *testing.T) {
	stored(t)
	x := pushedRun()
	x.Title = "site: Long raw prompt instructions that should not replace the commit subject"
	r := aRunToPull(t, map[string]string{pullBranch: theSHA}, x)
	if err := saveIndex(context.Background(), x.Org, x.ID, index{
		Repo: "hanzoai/site", Base: "main", Head: pullBranch,
		Commits: []client.CodingCommit{{SHA: newSHA, Message: "Add docs/LAYOUT.md overview\n\nDetailed body."}},
	}); err != nil {
		t.Fatalf("saveIndex: %v", err)
	}

	if _, err := Pull(asCaller("sub-zoe"), &client.CodingPullIn{Session: pullSession}); err != nil {
		t.Fatalf("pull: %v", err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.pulls) != 1 {
		t.Fatalf("pulls = %d, want 1", len(r.pulls))
	}
	if got, want := r.pulls[0]["title"], "site: Add docs/LAYOUT.md overview"; got != want {
		t.Fatalf("PR title = %q, want %q", got, want)
	}
}

// Who the pull request is assigned to and who is asked to review it reach
// GitHub with the pull request, and a login that is not one is refused before
// anything is opened.
func TestPull_AssigneesAndReviewersReachTheHost(t *testing.T) {
	r := aRunToPull(t, map[string]string{pullBranch: theSHA}, pushedRun())

	got, err := Pull(asCaller("sub-zoe"), &client.CodingPullIn{
		Session: pullSession, Draft: true,
		Assignees: []string{"zeekay", "Darkhorse7stars"}, Reviewers: []string{"Darkhorse7stars"},
	})
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if got.URL != "https://github.com/hanzo-apps/site/pull/9" || got.Warning != "" {
		t.Fatalf("the answer is %+v", got)
	}
	want := []string{
		"/repos/hanzo-apps/site/issues/9/assignees zeekay,Darkhorse7stars",
		"/repos/hanzo-apps/site/pulls/9/requested_reviewers Darkhorse7stars",
	}
	if strings.Join(r.people, "\n") != strings.Join(want, "\n") {
		t.Fatalf("GitHub was asked %q, want %q", r.people, want)
	}
	if len(r.pulls) != 1 || r.pulls[0]["draft"] != "true" {
		t.Fatalf("the pull request was not opened as a draft: %v", r.pulls)
	}
}

func TestPull_RefusesALoginThatIsNotOne(t *testing.T) {
	r := aRunToPull(t, map[string]string{pullBranch: theSHA}, pushedRun())
	for _, in := range []*client.CodingPullIn{
		{Session: pullSession, Assignees: []string{"../../orgs/x"}},
		{Session: pullSession, Reviewers: []string{"a b"}},
	} {
		_, err := Pull(asCaller("sub-zoe"), in)
		var ze *zip.HTTPError
		if !errors.As(err, &ze) || ze.Status != http.StatusBadRequest {
			t.Fatalf("%+v: got %v, want 400", in, err)
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.pulls) != 0 {
		t.Fatalf("a refused request opened %v", r.pulls)
	}
}

// A reviewer GitHub will not take leaves the pull request open: the answer is its
// address with a warning naming who was not added, not an error.
func TestPull_AReviewerGitHubRefusesIsAWarningNotAFailure(t *testing.T) {
	r := aRunToPull(t, map[string]string{pullBranch: theSHA}, pushedRun())
	r.mu.Lock()
	r.refusePeople = true
	r.mu.Unlock()

	got, err := Pull(asCaller("sub-zoe"), &client.CodingPullIn{Session: pullSession, Reviewers: []string{"stranger"}})
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if got.URL != "https://github.com/hanzo-apps/site/pull/9" || !strings.Contains(got.Warning, "reviewers stranger: 422") {
		t.Fatalf("the answer is %+v, want the pull request with a warning naming the reviewer", got)
	}
}
