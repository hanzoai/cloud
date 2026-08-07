package git

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/zap-proto/zip"
)

// pulls_test.go holds the pull-request surface to the two claims that matter
// about it: a merge MOVED THE BRANCH, and a proposal belongs to exactly one
// tenant. Both are asserted against the repository itself — the ref is read back
// after every merge and every refusal — because a row saying "merged" is the one
// thing a merge op can produce without merging anything.

// pushTo commits one file onto a branch through the client-less push door,
// creating the repo on the first call, and returns the new commit hash.
func pushTo(t *testing.T, app *zip.App, org, repo, branch, path, content string) string {
	t.Helper()
	code, b := do(t, app, http.MethodPost, "/v1/git/repos/"+repo+"/push", org, map[string]any{
		"name": repo, "branch": branch, "message": "seed " + path,
		"files": []map[string]any{{"path": path, "content": content}},
	})
	if code != http.StatusOK {
		t.Fatalf("push %s@%s: %d %s", repo, branch, code, b)
	}
	var out struct {
		Commit string `json:"commit"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("push json: %v (%s)", err, b)
	}
	return out.Commit
}

// branchAt reads what a branch points at right now, straight off the bare repo.
// "" when the branch does not exist.
func branchAt(t *testing.T, org, repo, branch string) string {
	t.Helper()
	st, err := mounted.Load().State.storage.storer(org, "", repo)
	if err != nil {
		t.Fatalf("storer: %v", err)
	}
	ref, err := st.Reference(plumbing.NewBranchReferenceName(branch))
	if err != nil {
		return ""
	}
	return ref.Hash().String()
}

// forkBranch points a new branch at an existing commit — what `git checkout -b`
// does. corePush starts an ORPHAN commit on a branch it has never seen, so this
// is what makes a branch a real DESCENDANT of base and the fast-forward case
// reachable through the public doors.
func forkBranch(t *testing.T, org, repo, branch, at string) {
	t.Helper()
	st, err := mounted.Load().State.storage.storer(org, "", repo)
	if err != nil {
		t.Fatalf("storer: %v", err)
	}
	if err := st.SetReference(plumbing.NewHashReference(
		plumbing.NewBranchReferenceName(branch), plumbing.NewHash(at))); err != nil {
		t.Fatalf("fork %s: %v", branch, err)
	}
}

func decodePull(t *testing.T, b []byte) pullView {
	t.Helper()
	var v pullView
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("pull json: %v (%s)", err, b)
	}
	return v
}

func decodePulls(t *testing.T, b []byte) []pullView {
	t.Helper()
	var v pullList
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("pull list json: %v (%s)", err, b)
	}
	return v.Data
}

// TestPullOpenListGetMerge walks the loop the agent runs: propose a branch, find
// it waiting, read it, merge it — and proves the merge moved main to head rather
// than only writing "merged" in a row.
func TestPullOpenListGetMerge(t *testing.T) {
	app := mountApp(t)

	a := pushTo(t, app, "acme", "code", "main", "README.md", "hello")
	forkBranch(t, "acme", "code", "feature", a)
	b := pushTo(t, app, "acme", "code", "feature", "cache.go", "package cache")

	code, body := do(t, app, http.MethodPost, "/v1/git/repos/code/pulls", "acme", map[string]any{
		"title": "cache the catalog read", "body": "faster reads", "head": "feature", "base": "main",
	})
	if code != http.StatusCreated {
		t.Fatalf("open: %d %s", code, body)
	}
	pr := decodePull(t, body)
	if pr.Number != 1 || pr.State != pullOpen || pr.Head != "feature" || pr.Base != "main" {
		t.Fatalf("opened pull is %+v, want #1 open feature→main", pr)
	}
	if pr.Author != "u_acme" {
		t.Fatalf("author is %q, want the validated principal u_acme", pr.Author)
	}

	// Listed, and filterable by state.
	code, body = do(t, app, http.MethodGet, "/v1/git/repos/code/pulls?state=open", "acme", nil)
	if code != http.StatusOK || len(decodePulls(t, body)) != 1 {
		t.Fatalf("list open: %d %s", code, body)
	}
	code, body = do(t, app, http.MethodGet, "/v1/git/repos/code/pulls?state=merged", "acme", nil)
	if code != http.StatusOK || len(decodePulls(t, body)) != 0 {
		t.Fatalf("list merged before merging: %d %s", code, body)
	}

	// Read one.
	code, body = do(t, app, http.MethodGet, "/v1/git/repos/code/pulls/1", "acme", nil)
	if code != http.StatusOK {
		t.Fatalf("get: %d %s", code, body)
	}
	if got := decodePull(t, body); got.Title != "cache the catalog read" || got.State != pullOpen {
		t.Fatalf("get returned %+v", got)
	}

	// Merge. main must END UP AT head — this is the whole claim.
	if before := branchAt(t, "acme", "code", "main"); before != a {
		t.Fatalf("main is %s before merge, want %s", before, a)
	}
	code, body = do(t, app, http.MethodPost, "/v1/git/repos/code/pulls/1/merge", "acme", nil)
	if code != http.StatusOK {
		t.Fatalf("merge: %d %s", code, body)
	}
	merged := decodePull(t, body)
	if merged.State != pullMerged || merged.MergedRev != b {
		t.Fatalf("merge returned %+v, want state merged at %s", merged, b)
	}
	if after := branchAt(t, "acme", "code", "main"); after != b {
		t.Fatalf("main is %s after merge, want head %s — the ref did not move", after, b)
	}

	// The state is durable, and merging twice is refused.
	code, body = do(t, app, http.MethodGet, "/v1/git/repos/code/pulls/1", "acme", nil)
	if code != http.StatusOK || decodePull(t, body).State != pullMerged {
		t.Fatalf("get after merge: %d %s", code, body)
	}
	if code, body = do(t, app, http.MethodPost, "/v1/git/repos/code/pulls/1/merge", "acme", nil); code != http.StatusConflict {
		t.Fatalf("second merge: %d %s, want 409", code, body)
	}
}

// TestMergeRefusesNonFastForward is the honesty test. A base with commits head
// does not contain needs a three-way merge, which this does not implement — so
// it must refuse AND leave the branch exactly where it was.
func TestMergeRefusesNonFastForward(t *testing.T) {
	app := mountApp(t)

	a := pushTo(t, app, "acme", "code", "main", "README.md", "hello")
	// A branch corePush has never seen starts an orphan commit, so "other" shares
	// no history with main at all — the strongest form of diverged.
	pushTo(t, app, "acme", "code", "other", "other.go", "package other")

	code, body := do(t, app, http.MethodPost, "/v1/git/repos/code/pulls", "acme", map[string]any{
		"title": "unrelated work", "head": "other", "base": "main",
	})
	if code != http.StatusCreated {
		t.Fatalf("open: %d %s", code, body)
	}

	code, body = do(t, app, http.MethodPost, "/v1/git/repos/code/pulls/1/merge", "acme", nil)
	if code != http.StatusConflict {
		t.Fatalf("merge of a diverged branch: %d %s, want 409", code, body)
	}
	if !strings.Contains(string(body), "fast-forward") {
		t.Fatalf("refusal does not name fast-forward: %s", body)
	}
	if now := branchAt(t, "acme", "code", "main"); now != a {
		t.Fatalf("main moved to %s on a REFUSED merge, want %s untouched", now, a)
	}
	// And the proposal is still open — a refused merge answers nothing.
	code, body = do(t, app, http.MethodGet, "/v1/git/repos/code/pulls/1", "acme", nil)
	if code != http.StatusOK || decodePull(t, body).State != pullOpen {
		t.Fatalf("pull after refused merge: %d %s, want still open", code, body)
	}
}

// TestPullsAreOrgScoped proves a proposal is invisible and unmergeable outside
// the org that owns it — with both orgs holding a repo of the SAME name and a
// pull of the SAME number, so nothing but the tenant distinguishes them.
func TestPullsAreOrgScoped(t *testing.T) {
	app := mountApp(t)

	// orgA: a repo with a mergeable proposal.
	a := pushTo(t, app, "orga", "code", "main", "README.md", "A")
	forkBranch(t, "orga", "code", "feature", a)
	pushTo(t, app, "orga", "code", "feature", "a.go", "package a")
	if code, b := do(t, app, http.MethodPost, "/v1/git/repos/code/pulls", "orga", map[string]any{
		"title": "orgA work", "head": "feature", "base": "main",
	}); code != http.StatusCreated {
		t.Fatalf("orgA open: %d %s", code, b)
	}

	// orgB: same repo name, its own proposal, also #1.
	c := pushTo(t, app, "orgb", "code", "main", "README.md", "B")
	forkBranch(t, "orgb", "code", "feature", c)
	pushTo(t, app, "orgb", "code", "feature", "b.go", "package b")
	if code, b := do(t, app, http.MethodPost, "/v1/git/repos/code/pulls", "orgb", map[string]any{
		"title": "orgB work", "head": "feature", "base": "main",
	}); code != http.StatusCreated {
		t.Fatalf("orgB open: %d %s", code, b)
	}

	// orgB's list shows ONLY orgB's proposal.
	code, body := do(t, app, http.MethodGet, "/v1/git/repos/code/pulls", "orgb", nil)
	if code != http.StatusOK {
		t.Fatalf("orgB list: %d %s", code, body)
	}
	rows := decodePulls(t, body)
	if len(rows) != 1 || rows[0].Title != "orgB work" {
		t.Fatalf("orgB sees %+v — want only its own proposal", rows)
	}

	// A third org holds no repo of that name at all: every address 404s, and the
	// merge door does not answer differently from the read door.
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/v1/git/repos/code/pulls"},
		{http.MethodGet, "/v1/git/repos/code/pulls/1"},
		{http.MethodPost, "/v1/git/repos/code/pulls/1/merge"},
	} {
		if code, b := do(t, app, tc.method, tc.path, "orgc", nil); code != http.StatusNotFound {
			t.Fatalf("orgc %s %s: %d %s, want 404", tc.method, tc.path, code, b)
		}
	}

	// And orgA's branch is untouched by anything orgB or orgC did.
	if now := branchAt(t, "orga", "code", "main"); now != a {
		t.Fatalf("orgA main is %s, want %s — another tenant moved it", now, a)
	}

	// The merge orgB runs on ITS #1 must not touch orgA's #1 or orgA's branch.
	if code, b := do(t, app, http.MethodPost, "/v1/git/repos/code/pulls/1/merge", "orgb", nil); code != http.StatusOK {
		t.Fatalf("orgB merge: %d %s", code, b)
	}
	if now := branchAt(t, "orga", "code", "main"); now != a {
		t.Fatalf("orgA main moved to %s when orgB merged, want %s", now, a)
	}
	code, body = do(t, app, http.MethodGet, "/v1/git/repos/code/pulls/1", "orga", nil)
	if code != http.StatusOK || decodePull(t, body).State != pullOpen {
		t.Fatalf("orgA #1 after orgB merged its #1: %d %s, want still open", code, body)
	}
}

// TestTheURLAddressesThePullNotTheBody proves the addressing authority. zip binds
// the body first and the path LAST, so a field a caller supplies can never
// redirect an op at something the URL did not name — the same reason the org is
// read from the validated principal and never from an In field.
func TestTheURLAddressesThePullNotTheBody(t *testing.T) {
	app := mountApp(t)
	a := pushTo(t, app, "acme", "code", "main", "README.md", "hello")
	forkBranch(t, "acme", "code", "feature", a)
	pushTo(t, app, "acme", "code", "feature", "x.go", "package x")
	// A second repo, so a body naming it would be a real redirection if it worked.
	other := pushTo(t, app, "acme", "decoy", "main", "README.md", "decoy")
	forkBranch(t, "acme", "decoy", "feature", other)
	pushTo(t, app, "acme", "decoy", "feature", "y.go", "package y")

	// The body names "decoy"; the URL names "code". The URL wins.
	code, body := do(t, app, http.MethodPost, "/v1/git/repos/code/pulls", "acme", map[string]any{
		"name": "decoy", "title": "addressed by url", "head": "feature", "base": "main",
	})
	if code != http.StatusCreated {
		t.Fatalf("open: %d %s", code, body)
	}
	if got := decodePull(t, body); got.Repo != "code" {
		t.Fatalf("pull landed on %q, want the repo the URL named (code)", got.Repo)
	}
	if code, b := do(t, app, http.MethodGet, "/v1/git/repos/decoy/pulls", "acme", nil); code != http.StatusOK ||
		len(decodePulls(t, b)) != 0 {
		t.Fatalf("decoy holds a pull the body redirected to it: %d %s", code, b)
	}

	// The same for merge: a body naming another number cannot move the target.
	code, body = do(t, app, http.MethodPost, "/v1/git/repos/code/pulls/1/merge", "acme",
		map[string]any{"name": "decoy", "number": 99})
	if code != http.StatusOK {
		t.Fatalf("merge: %d %s", code, body)
	}
	if got := decodePull(t, body); got.Number != 1 || got.Repo != "code" {
		t.Fatalf("merge answered %+v, want code#1 as the URL named", got)
	}
	if now := branchAt(t, "acme", "decoy", "main"); now != other {
		t.Fatalf("decoy main moved to %s, want %s — a body field reached another repo", now, other)
	}
}

// TestPullsRequireAPrincipal proves an unauthenticated caller reaches none of the
// four ops — the org is read from the validated principal, so a request without
// one has no tenant to resolve and is refused before any store is opened.
func TestPullsRequireAPrincipal(t *testing.T) {
	app := mountApp(t)
	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/v1/git/repos/code/pulls"},
		{http.MethodGet, "/v1/git/repos/code/pulls"},
		{http.MethodGet, "/v1/git/repos/code/pulls/1"},
		{http.MethodPost, "/v1/git/repos/code/pulls/1/merge"},
	} {
		if code, b := do(t, app, tc.method, tc.path, "", nil); code != http.StatusForbidden {
			t.Fatalf("anonymous %s %s: %d %s, want 403", tc.method, tc.path, code, b)
		}
	}
}

// TestOpenPullRefusals covers what a proposal may not say. Each case must fail
// BEFORE a row exists, which the final list assertion checks.
func TestOpenPullRefusals(t *testing.T) {
	app := mountApp(t)
	a := pushTo(t, app, "acme", "code", "main", "README.md", "hello")
	forkBranch(t, "acme", "code", "feature", a)
	pushTo(t, app, "acme", "code", "feature", "x.go", "package x")

	for _, tc := range []struct {
		name string
		body map[string]any
		want int
	}{
		{"no title", map[string]any{"head": "feature", "base": "main"}, http.StatusBadRequest},
		{"head is base", map[string]any{"title": "t", "head": "main", "base": "main"}, http.StatusBadRequest},
		{"head absent", map[string]any{"title": "t", "head": "ghost", "base": "main"}, http.StatusBadRequest},
		{"base absent", map[string]any{"title": "t", "head": "feature", "base": "ghost"}, http.StatusBadRequest},
		{"head malformed", map[string]any{"title": "t", "head": "../etc", "base": "main"}, http.StatusBadRequest},
		{"title too long", map[string]any{
			"title": strings.Repeat("x", maxPullTitle+1), "head": "feature", "base": "main",
		}, http.StatusBadRequest},
	} {
		if code, b := do(t, app, http.MethodPost, "/v1/git/repos/code/pulls", "acme", tc.body); code != tc.want {
			t.Fatalf("%s: %d %s, want %d", tc.name, code, b, tc.want)
		}
	}
	// A repo the org does not have is a 404, not a 400.
	if code, b := do(t, app, http.MethodPost, "/v1/git/repos/ghost/pulls", "acme", map[string]any{
		"title": "t", "head": "feature", "base": "main",
	}); code != http.StatusNotFound {
		t.Fatalf("unknown repo: %d %s, want 404", code, b)
	}
	// A bad state filter is refused rather than silently listing everything.
	if code, b := do(t, app, http.MethodGet, "/v1/git/repos/code/pulls?state=banana", "acme", nil); code != http.StatusBadRequest {
		t.Fatalf("bad state filter: %d %s, want 400", code, b)
	}
	// Nothing above created a row.
	code, body := do(t, app, http.MethodGet, "/v1/git/repos/code/pulls", "acme", nil)
	if code != http.StatusOK || len(decodePulls(t, body)) != 0 {
		t.Fatalf("refused opens left rows behind: %d %s", code, body)
	}
}

// TestOpenPullDefaultsBaseAndRefusesADuplicate covers the two things a retried
// agent run depends on: base falls back to the repo's default branch, and
// proposing the same pair twice yields ONE thing to review.
func TestOpenPullDefaultsBaseAndRefusesADuplicate(t *testing.T) {
	app := mountApp(t)
	a := pushTo(t, app, "acme", "code", "main", "README.md", "hello")
	forkBranch(t, "acme", "code", "feature", a)
	pushTo(t, app, "acme", "code", "feature", "x.go", "package x")

	code, body := do(t, app, http.MethodPost, "/v1/git/repos/code/pulls", "acme", map[string]any{
		"title": "no base given", "head": "feature",
	})
	if code != http.StatusCreated {
		t.Fatalf("open with no base: %d %s", code, body)
	}
	if got := decodePull(t, body); got.Base != "main" {
		t.Fatalf("base defaulted to %q, want the repo default branch main", got.Base)
	}

	code, body = do(t, app, http.MethodPost, "/v1/git/repos/code/pulls", "acme", map[string]any{
		"title": "same again", "head": "feature", "base": "main",
	})
	if code != http.StatusConflict {
		t.Fatalf("duplicate open: %d %s, want 409", code, body)
	}

	// Merging the first frees the pair: the same branch may be proposed again.
	if code, b := do(t, app, http.MethodPost, "/v1/git/repos/code/pulls/1/merge", "acme", nil); code != http.StatusOK {
		t.Fatalf("merge: %d %s", code, b)
	}
	pushTo(t, app, "acme", "code", "feature", "y.go", "package y")
	if code, b := do(t, app, http.MethodPost, "/v1/git/repos/code/pulls", "acme", map[string]any{
		"title": "round two", "head": "feature", "base": "main",
	}); code != http.StatusCreated {
		t.Fatalf("re-propose after merge: %d %s, want 201", code, b)
	}
}
