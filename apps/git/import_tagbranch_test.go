// Copyright © 2026 Hanzo AI. MIT License.

package git

import (
	"context"
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
