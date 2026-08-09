// Copyright © 2026 Hanzo AI. MIT License.

package git

import (
	"context"
	"fmt"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// A repository whose TAG shares a name with a BRANCH must import.
//
// This is a live failure, not a hypothetical: hanzoai/2d-techdemos carries a
// branch `2017` and an annotated tag `2017`, and its import died at that branch —
//
//	cannot update ref 'refs/heads/2017': trying to write non-commit object
//	c9f64362... to branch 'refs/heads/2017'
//	! [new tag] 2017 -> 2017 (unable to update local ref)
//
// The per-branch fetch asked for refs/heads/2017:refs/heads/2017, which is
// correct, and git ALSO auto-followed the tag pointing into that history, then
// resolved the short name `2017` onto the branch ref the same refspec had just
// created. One tag shaped like a branch name failed the WHOLE repository, and it
// was reported only as a warn line — which is why the native git held a fraction
// of what had been declared for it.
//
// The fix is that a branch fetch fetches a branch: --no-tags. Tags come next, by
// fetchTags, create-only into refs/tags/* where a tag belongs. So this test also
// pins that the tag still ARRIVES — a fix that imported the branch by dropping
// the tag would pass a weaker test and lose data.
func TestImportRepoWhoseTagSharesABranchName(t *testing.T) {
	// A source repo with branch `2017` AND an annotated tag `2017`, served over
	// smart-HTTP because a local path is refused (GIT_ALLOW_PROTOCOL=http:https) —
	// the same transport a real import uses.
	//
	// The tag is ANNOTATED on purpose: a tag OBJECT is the non-commit git refused
	// to write to a branch, so a lightweight tag would not reproduce the failure.
	root := t.TempDir()
	base := serveGitHTTP(t, root)
	src := newSource(t, root, base, "demos", "v1")
	gitRun(t, src.work, "branch", "2017")
	gitRun(t, src.work, "tag", "-a", "2017", "-m", "the 2017 release")
	gitRun(t, src.work, "push", "-q", "origin", "refs/heads/2017:refs/heads/2017", "refs/tags/2017:refs/tags/2017")

	st, err := newStorage(t.TempDir())
	if err != nil {
		t.Fatalf("newStorage: %v", err)
	}
	const org, project, name = "acme", "_", "demos"
	if err := st.initBare(org, project, name, "main"); err != nil {
		t.Fatalf("initBare: %v", err)
	}

	res, err := st.importFetch(context.Background(), org, project, name, src.url, gitCred{})
	if err != nil {
		t.Fatalf("import of a repo whose tag shares a branch name: %v", err)
	}
	for _, b := range []string{"main", "2017"} {
		if _, ok := res[b]; !ok {
			t.Errorf("branch %q was not imported (got %v)", b, res)
		}
	}

	// The branch is a commit and the tag is still a tag — the two refs stayed
	// distinct rather than one overwriting the other.
	bare := st.absRepoPath(org, project, name)
	kind := func(ref string) string { return gitOut(t, "", "--git-dir="+bare, "cat-file", "-t", ref) }
	if got := kind("refs/heads/2017"); got != "commit" {
		t.Errorf("refs/heads/2017 is a %s, want commit", got)
	}
	if got := kind("refs/tags/2017"); got != "tag" {
		t.Errorf("refs/tags/2017 is a %s, want tag — the tag must still arrive", got)
	}
}

// The import path must hand the ref policy a REAL ref, or the policy is inert.
//
// This is the quieter half of the same bug and the reason it is worth a test of
// its own. checkRefPolicy builds the protected name as "refs/heads/"+default and
// recognises an agent branch by the refs/heads/agent/ prefix, so a short "main"
// matched neither: on the import path the default-branch guard and the
// agent-branch guard were both silently doing nothing. Nothing failed — a guard
// that never fires looks exactly like a guard with nothing to catch.
func TestImportGivesTheRefPolicyAFullRef(t *testing.T) {
	// What importFetch now passes, against what the policy actually compares.
	const branch = "main"
	full := "refs/heads/" + branch

	if err := checkRefPolicy([]refCommand{{Old: "abc", New: zeroOID, Ref: full}}, branch, ""); err == nil {
		t.Fatal("deleting the default branch was allowed — the guard did not see the ref")
	}
	// The shape the import used to pass. Kept as the counter-example so the
	// difference is stated, not remembered.
	if err := checkRefPolicy([]refCommand{{Old: "abc", New: zeroOID, Ref: branch}}, branch, ""); err == nil {
		t.Log("a short ref name is invisible to the default-branch guard — which is why it must never be passed")
	} else {
		t.Errorf("a short name unexpectedly matched the guard (%v); the invariant under test has moved", err)
	}
	if err := checkRefPolicy([]refCommand{{Old: "abc", New: "def", Ref: agentRefPrefix + "x"}}, branch, ""); err == nil {
		t.Fatal("rewriting an agent branch was allowed — the prefix guard did not see the ref")
	}
}

// A SECOND import of an unchanged repository must fetch NOTHING.
//
// The ls-remote advertisement already carries each branch tip, and it used to be
// parsed and thrown away — so every reconcile spawned one `git fetch` per branch to
// discover that nothing had moved. Measured on this fleet: ~7 branches per
// repository across 1685 declared syncs, roughly 11,800 fetch subprocesses and
// their network round trips per sweep, nearly all no-ops. That is why a full sweep
// took hours, which is why the fleet redeployed before it ever finished one.
//
// Counted at the source rather than timed: the served source repo's git-http-backend
// logs one upload-pack request per fetch, so the assertion is on requests, not on a
// duration that would be flaky under load.
func TestASecondImportOfAnUnchangedRepoFetchesNothing(t *testing.T) {
	root := t.TempDir()
	var uploads int64
	base := serveGitHTTPCounting(t, root, &uploads)
	src := newSource(t, root, base, "steady", "v1")
	// Several branches, because the waste was per-branch.
	for _, b := range []string{"one", "two", "three", "four"} {
		gitRun(t, src.work, "branch", b)
		gitRun(t, src.work, "push", "-q", "origin", "refs/heads/"+b+":refs/heads/"+b)
	}

	st, err := newStorage(t.TempDir())
	if err != nil {
		t.Fatalf("newStorage: %v", err)
	}
	const org, project, name = "acme", "_", "steady"
	if err := st.initBare(org, project, name, "main"); err != nil {
		t.Fatalf("initBare: %v", err)
	}

	first, err := st.importFetch(context.Background(), org, project, name, src.url, gitCred{})
	if err != nil {
		t.Fatalf("first import: %v", err)
	}
	if len(first) != 5 { // main + four
		t.Fatalf("first import took %d branches, want 5: %v", len(first), first)
	}
	afterFirst := atomic.LoadInt64(&uploads)
	if afterFirst < 2 {
		t.Fatalf("the first import made %d upload-pack requests — it must fetch, or the counter is not wired", afterFirst)
	}

	// Nothing changed upstream. The second pass must reach the same conclusion
	// without asking for a single pack.
	second, err := st.importFetch(context.Background(), org, project, name, src.url, gitCred{})
	if err != nil {
		t.Fatalf("second import: %v", err)
	}
	// EXACTLY ONE, and that one is the advertisement.
	//
	// Protocol v2 carries `ls-refs` as an upload-pack POST too, so asking the source
	// what it has costs one request — and asking is the irreducible cost of knowing
	// nothing changed. What must be zero is everything AFTER it: no pack for any of
	// the five branches, and none for tags. A fetch under v2 is a second POST on top
	// of the advertisement, so anything above 1 here is a fetch that happened.
	if got := atomic.LoadInt64(&uploads) - afterFirst; got != 1 {
		t.Errorf("second import of an unchanged repo made %d upload-pack requests, want 1 (the advertisement alone)", got)
	}
	for b, res := range second {
		if !res.NoOp {
			t.Errorf("branch %s reported %+v, want NoOp", b, res)
		}
	}

	// And a branch that DOES move is still fetched, so the skip is not blanket.
	src.commit("v2")
	third, err := st.importFetch(context.Background(), org, project, name, src.url, gitCred{})
	if err != nil {
		t.Fatalf("third import: %v", err)
	}
	if !third["main"].Applied {
		t.Errorf("main moved upstream and was not applied: %+v", third["main"])
	}
	if !third["one"].NoOp {
		t.Errorf("branch one did not move and should still be a NoOp: %+v", third["one"])
	}
}

// serveGitHTTPCounting is serveGitHTTP with a tally of PACK requests — the thing a
// fetch costs and a skipped fetch does not. Counting requests rather than timing is
// what makes the assertion stable under a loaded box.
func serveGitHTTPCounting(t *testing.T, root string, uploads *int64) string {
	t.Helper()
	execPath := gitOut(t, "", "--exec-path")
	backend := &cgi.Handler{
		Path: filepath.Join(execPath, "git-http-backend"),
		Env:  []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1"},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The ref advertisement is /info/refs; the PACK is git-upload-pack. Only the
		// second is the cost being removed, so only the second is counted.
		if strings.HasSuffix(r.URL.Path, "/git-upload-pack") {
			atomic.AddInt64(uploads, 1)
			if os.Getenv("PACKLOG") != "" {
				fmt.Printf("PACK %s ?%s\n", r.URL.Path, r.URL.RawQuery)
			}
		}
		backend.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// A second import of a repo carrying an ANNOTATED tag must also fetch nothing.
//
// Tags are skipped from the same advertisement as branches, and an annotated tag is
// the shape that could get that wrong: ls-remote lists it twice, once as the tag
// OBJECT and once peeled to its commit. This pins that both tags ARRIVE with the
// right object kinds and that a second pass still costs only the advertisement.
//
// It does NOT prove the peel filter is load-bearing — checked, and the test stays
// green without it, because git resolves the "^{}" suffix itself so either entry
// compares equal. The filter saves a subprocess, nothing more, and the comment on
// it says so.
func TestASecondImportWithAnAnnotatedTagFetchesNothing(t *testing.T) {
	root := t.TempDir()
	var uploads int64
	base := serveGitHTTPCounting(t, root, &uploads)
	src := newSource(t, root, base, "tagged", "v1")
	gitRun(t, src.work, "tag", "-a", "v1.0.0", "-m", "the release")
	gitRun(t, src.work, "tag", "lightweight")
	gitRun(t, src.work, "push", "-q", "origin", "refs/tags/v1.0.0:refs/tags/v1.0.0", "refs/tags/lightweight:refs/tags/lightweight")

	st, err := newStorage(t.TempDir())
	if err != nil {
		t.Fatalf("newStorage: %v", err)
	}
	const org, project, name = "acme", "_", "tagged"
	if err := st.initBare(org, project, name, "main"); err != nil {
		t.Fatalf("initBare: %v", err)
	}
	if _, err := st.importFetch(context.Background(), org, project, name, src.url, gitCred{}); err != nil {
		t.Fatalf("first import: %v", err)
	}

	// Both tags must have ARRIVED, and with the right object kinds — a skip that
	// works because nothing was imported would pass a weaker test.
	bare := st.absRepoPath(org, project, name)
	if got := gitOut(t, "", "--git-dir="+bare, "cat-file", "-t", "refs/tags/v1.0.0"); got != "tag" {
		t.Fatalf("annotated tag arrived as %s, want tag", got)
	}
	if got := gitOut(t, "", "--git-dir="+bare, "cat-file", "-t", "refs/tags/lightweight"); got != "commit" {
		t.Fatalf("lightweight tag arrived as %s, want commit", got)
	}

	afterFirst := atomic.LoadInt64(&uploads)
	if _, err := st.importFetch(context.Background(), org, project, name, src.url, gitCred{}); err != nil {
		t.Fatalf("second import: %v", err)
	}
	if got := atomic.LoadInt64(&uploads) - afterFirst; got != 1 {
		t.Errorf("second import made %d upload-pack requests, want 1 (the advertisement alone) — the annotated tag is being re-fetched every pass", got)
	}
}
