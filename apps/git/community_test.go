package git

import (
	"context"
	"errors"
	"testing"

	"github.com/hanzoai/cloud/plane"
)

// community_test.go proves git's half of the visibility client against the copy it
// actually serves: the repo behind git.hanzo.ai.
//
// The GitHub replica's half is community_github_test.go. What is proven here is
// the state machine — open, shut and GONE — and the last of those is the one
// with teeth: a repo is found by NAME, so one a deleted project left behind is
// one the next project of that slug adopts, commits and all, and publishes.

// repoAfter applies one visibility fact as the projects surface does, and answers
// what the repo looks like afterwards: whether it is there, and whether it is
// world-readable.
func repoAfter(t *testing.T, org, slug, want string) (there, public bool) {
	t.Helper()
	ctx := context.Background()
	if err := publish(ctx, org, plane.Visibility{Slug: slug, Name: slug, State: want}); err != nil {
		t.Fatalf("publish %s: %v", want, err)
	}
	s := mounted.Load()
	if s == nil {
		t.Fatal("git is not mounted")
	}
	store, err := storeFor(s, org)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	got, err := store.Get(ctx, org, "", slug)
	switch {
	case errors.Is(err, errNotFound):
		return false, false
	case err != nil:
		t.Fatalf("read repo: %v", err)
	}
	return true, got.Public
}

// A project's repo follows its project through all three states, and GONE means
// gone: a closed repo is still there to be adopted and re-opened by whoever
// claims the slug next, which is the whole reason the deletion is a deletion.
func TestARepoFollowsItsProjectThroughEveryState(t *testing.T) {
	mountApp(t)

	if there, public := repoAfter(t, "acme", "board", plane.Open); !there || !public {
		t.Fatalf("published public: there=%v public=%v, want true/true", there, public)
	}
	if there, public := repoAfter(t, "acme", "board", plane.Shut); !there || public {
		t.Fatalf("gone private: there=%v public=%v, want true/false", there, public)
	}
	if there, public := repoAfter(t, "acme", "board", plane.Open); !there || !public {
		t.Fatalf("public again: there=%v public=%v, want true/true", there, public)
	}
	if there, _ := repoAfter(t, "acme", "board", plane.Gone); there {
		t.Fatal("a deleted project's repo is still here, for the next project of that slug to adopt")
	}
	// Twice, because the delete path retries behind its own answer: an absence is
	// the state it asked for, not a failure to report forever.
	if there, _ := repoAfter(t, "acme", "board", plane.Gone); there {
		t.Fatal("the second retirement resurrected the repo")
	}
	// And a project that reclaims the slug gets a repo that has never held
	// anything else — there is nothing left to inherit.
	if there, public := repoAfter(t, "acme", "board", plane.Open); !there || !public {
		t.Fatalf("reclaimed: there=%v public=%v, want true/true", there, public)
	}
}

// A private project still gets a repo — born CLOSED, so choosing private is not
// choosing a project with nowhere to push, and there is no window in which it
// exists and is readable.
func TestAPrivateProjectsRepoIsBornClosed(t *testing.T) {
	mountApp(t)
	if there, public := repoAfter(t, "acme", "secret", plane.Shut); !there || public {
		t.Fatalf("born private: there=%v public=%v, want true/false", there, public)
	}
}

// A name this could not spell is REFUSED, and refused before anything is
// created. The op is its own trust boundary — the peer across the socket is one
// of our own processes, which is not a reason to take a name unread — and a
// create and a delete that read the same fact differently would address two
// repos, which is a delete that never deletes.
func TestAnUnspellableNameIsRefused(t *testing.T) {
	mountApp(t)
	ctx := context.Background()
	for _, slug := range []string{"", " ", "/etc/passwd", "..", "a/b", "-lead", ".git"} {
		err := publish(ctx, "acme", plane.Visibility{Slug: slug, State: plane.Open})
		if err == nil {
			t.Fatalf("publish(%q) was accepted; a name this cannot spell must be refused", slug)
		}
	}
	// The alphabet it DOES take is the REST surface's own, through the REST
	// surface's normalisation: a client's trailing ".git" names the same repo the
	// bare slug does, on the create AND on the delete.
	if err := publish(ctx, "acme", plane.Visibility{Slug: "board.git", State: plane.Open}); err != nil {
		t.Fatalf("publish(board.git): %v", err)
	}
	if there, public := repoAfter(t, "acme", "board", plane.Open); !there || !public {
		t.Fatalf("board.git addressed some other repo: there=%v public=%v", there, public)
	}
	if err := publish(ctx, "acme", plane.Visibility{Slug: "board.git", State: plane.Gone}); err != nil {
		t.Fatalf("retire(board.git): %v", err)
	}
	if there, _ := repoAfter(t, "acme", "board", plane.Gone); there {
		t.Fatal("the retirement addressed a different repo from the create")
	}
}

// An unrecognised state is CLOSED, not open. The readable bit is the one that
// cannot be taken back, so anything this does not understand — an empty fact, a
// newer peer's word — answers with it off.
func TestAnUnknownStateClosesTheRepo(t *testing.T) {
	mountApp(t)
	if there, public := repoAfter(t, "acme", "board", plane.Open); !there || !public {
		t.Fatalf("published public: there=%v public=%v, want true/true", there, public)
	}
	if there, public := repoAfter(t, "acme", "board", "who knows"); !there || public {
		t.Fatalf("unknown state: there=%v public=%v, want true/false", there, public)
	}
}
