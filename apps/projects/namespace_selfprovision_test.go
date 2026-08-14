package projects

import (
	"net/http"
	"testing"
)

// redNamespaceTouches returns every request, after mark, that addressed the
// community NAMESPACE itself rather than something inside it. The bare "/orgs"
// is EnsureOrg's create and "/orgs/<community>" is its existence read; a repo
// call is always "/orgs/<owner>/repos" or "/repos/...", so neither of these two
// paths can come from anywhere else.
func redNamespaceTouches(f *forgery, mark int) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, p := range f.seen[mark:] {
		if p == "/orgs" || p == "/orgs/"+community {
			out = append(out, p)
		}
	}
	return out
}

func redSeenLen(f *forgery) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.seen)
}

// TestRedGoneNeverMintsTheNamespace.
//
// EnsureOrg is reachable from apply's OPEN and SHUT branches only. GONE must
// never bring a namespace into being to remove a repository that is already not
// there: a delete that mints the org it is deleting out of would leave a fresh
// deployment holding a namespace nothing ever published into, and it would do it
// on the one path whose whole job is to take things away.
func TestRedGoneNeverMintsTheNamespace(t *testing.T) {
	app, f, _ := mountShared(t)
	s := mounted
	create(t, app, "board")
	settled(t, "the repository to be readable", func() bool { return f.readable(t, "acme_board") })
	drained(t, s)

	// Everything before this may legitimately have addressed the namespace — the
	// create path is allowed to. Only what the DELETE does is on trial.
	mark := redSeenLen(f)

	if code, body := do(t, app, http.MethodDelete, "/v1/projects/board", "acme", nil); code != http.StatusNoContent {
		t.Fatalf("delete want 204, got %d (%s)", code, body)
	}
	drained(t, s)

	if got := redNamespaceTouches(f, mark); len(got) != 0 {
		t.Fatalf("the delete path addressed the community namespace %v — GONE must not mint an org", got)
	}
	if f.exists("acme_board") {
		t.Fatal("a deleted project's source is still on the forge")
	}
}

// TestRedTheCreatePathDoesEnsureTheNamespace is the positive control for the
// test above: if the delete path's silence were silence everywhere, the first
// test would pass against a seam that had lost EnsureOrg altogether.
func TestRedTheCreatePathDoesEnsureTheNamespace(t *testing.T) {
	app, f, _ := mountShared(t)
	s := mounted
	mark := redSeenLen(f)
	create(t, app, "board")
	settled(t, "the repository to be readable", func() bool { return f.readable(t, "acme_board") })
	drained(t, s)

	if got := redNamespaceTouches(f, mark); len(got) == 0 {
		t.Fatal("the create path never addressed the community namespace: EnsureOrg is not on the publish path")
	}
}
