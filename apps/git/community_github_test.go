package git

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// community_github_test.go proves the GitHub replica's decisions without a
// network or a live org: an httptest server stands in for api.github.com.

// ghCall is one request the fake API saw.
type ghCall struct {
	Method, Path string
	Body         map[string]any
}

// fakeGitHub serves the endpoints ensure and remove use. `exists` decides
// whether the repo is already there, which is the whole branch under test — and
// a repo that is DELETED stops being there, so the read-back that confirms a
// deletion is answered by the same state the delete changed.
func fakeGitHub(t *testing.T, exists bool) (*[]ghCall, func()) {
	t.Helper()
	var (
		mu    sync.Mutex
		calls = []ghCall{}
		gone  bool
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(b, &body)
		mu.Lock()
		calls = append(calls, ghCall{Method: r.Method, Path: r.URL.Path, Body: body})
		here := exists && !gone
		if r.Method == http.MethodDelete {
			gone = true
		}
		mu.Unlock()

		// GitHub serves TWO paths here and 404s everything else, and so does this:
		// a stand-in that answers whatever it is asked cannot tell a working
		// endpoint from a malformed one, which is how a create built out of the
		// repo endpoint stayed green while every real create 404ed.
		if p := r.URL.Path; p != "/orgs/hanzo-community/repos" &&
			!(strings.HasPrefix(p, "/repos/hanzo-community/") && strings.Count(p, "/") == 3) {
			w.WriteHeader(http.StatusNotFound)
			return
		}

		switch {
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		case !here && (r.Method == http.MethodPatch || r.Method == http.MethodGet):
			w.WriteHeader(http.StatusNotFound)
		case r.Method == http.MethodPatch, r.Method == http.MethodGet:
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPost:
			w.WriteHeader(http.StatusCreated)
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	old := api
	api = srv.URL
	t.Setenv(mirrorTokenStem+"_GITHUB_COM", "test-token")
	t.Cleanup(func() { api = old; srv.Close() })
	return &calls, srv.Close
}

// TestCommunityRepoIsCreatedWhenMissing: we hold admin on the community org, so
// a first publish CREATES the far-side repo rather than assuming somebody
// provisioned it. Without this the mirror would force-push at a target that does
// not exist, and fail for every project forever.
func TestCommunityRepoIsCreatedWhenMissing(t *testing.T) {
	calls, _ := fakeGitHub(t, false)

	url, err := ensure(context.Background(), "acme", "board", "a board", true)
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if want := "https://github.com/hanzo-community/acme_board.git"; url != want {
		t.Fatalf("clone url = %q, want %q", url, want)
	}
	if len(*calls) != 2 {
		t.Fatalf("want probe-then-create, got %d calls: %+v", len(*calls), *calls)
	}
	// The EXACT path, not a suffix of one. A suffix match is satisfied by
	// /repos/hanzo-community/acme_board/orgs/hanzo-community/repos — the endpoint
	// built out of the repo endpoint instead of the API root, which GitHub answers
	// 404 while this stand-in answers anything.
	probe, create := (*calls)[0], (*calls)[1]
	if probe.Method != http.MethodPatch || probe.Path != "/repos/hanzo-community/acme_board" {
		t.Fatalf("the probe must PATCH the repo itself: %+v", probe)
	}
	if create.Method != http.MethodPost || create.Path != "/orgs/hanzo-community/repos" {
		t.Fatalf("the create must POST the community org's repo list: %+v", create)
	}
	if create.Body["name"] != "acme_board" {
		t.Fatalf("repo name = %v, want acme_board (org-qualified: one flat namespace)", create.Body["name"])
	}
	if create.Body["private"] != false {
		t.Fatalf("a public project must be born public, got private=%v", create.Body["private"])
	}
}

// TestCommunityRepoIsBornPrivate: a private project's replica must never be
// briefly public. Visibility is set AT creation, not patched afterwards.
func TestCommunityRepoIsBornPrivate(t *testing.T) {
	calls, _ := fakeGitHub(t, false)

	if _, err := ensure(context.Background(), "acme", "secret", "", false); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	create := (*calls)[len(*calls)-1]
	if create.Body["private"] != true {
		t.Fatal("a private project's replica was created public, even momentarily")
	}
	if create.Body["auto_init"] != false {
		t.Fatal("auto_init must stay false: the first mirror push carries the real history")
	}
}

// TestVisibilityStaysInStepOnBothHosts is the user-visible promise: one switch
// in the console, both hosts follow. An existing replica is PATCHed — never
// re-created and never deleted, so stars, forks and issues survive a project
// going private and coming back.
func TestVisibilityStaysInStepOnBothHosts(t *testing.T) {
	for _, tc := range []struct {
		name        string
		listed      bool
		wantPrivate bool
	}{
		{"public", true, false},
		{"private", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls, _ := fakeGitHub(t, true)

			if _, err := ensure(context.Background(), "acme", "board", "", tc.listed); err != nil {
				t.Fatalf("ensure: %v", err)
			}
			if len(*calls) != 1 {
				t.Fatalf("an existing replica needs ONE patch, got %d: %+v", len(*calls), *calls)
			}
			c := (*calls)[0]
			if c.Method != http.MethodPatch {
				t.Fatalf("existing repo must be patched, not %s", c.Method)
			}
			// `visibility`, not `private`: GitHub rejects {"private":bool} on an org
			// repo with a 422 and an EMPTY error list, so a wrong field here fails
			// silently in exactly the retraction direction that must not fail.
			if _, wrong := c.Body["private"]; wrong {
				t.Fatal(`PATCH must send "visibility", not "private" — GitHub 422s the latter`)
			}
			want := "public"
			if tc.wantPrivate {
				want = "private"
			}
			if c.Body["visibility"] != want {
				t.Fatalf("visibility = %v, want %v", c.Body["visibility"], want)
			}
			if _, sent := c.Body["description"]; sent {
				t.Fatal("description must not be re-imposed: an author's own edit has to survive")
			}
		})
	}
}

// TestTwoTenantsCannotSpellOneReplica: the replica namespace is FLAT, so the
// flattening has to be injective — and injective is not cosmetic. With `-`, org
// `a-b` publishing project `c` and org `a` publishing project `b-c` spell one
// repo, so the first tenant's publish would decide who may read the second
// tenant's source, and its delete would destroy it.
func TestTwoTenantsCannotSpellOneReplica(t *testing.T) {
	one, ok := name("a", "b-c")
	if !ok {
		t.Fatal("a/b-c has no replica name")
	}
	two, ok := name("a-b", "c")
	if !ok {
		t.Fatal("a-b/c has no replica name")
	}
	if one == two {
		t.Fatalf("two tenants spell one replica: %q", one)
	}
	// The separator is what makes it injective, so it may appear in NEITHER half.
	// A pair that cannot be spelled gets no replica at all, which is the safe
	// half: nothing is published, so nothing is exposed.
	for _, tc := range []struct{ org, slug string }{
		{"", "board"}, {"acme", ""}, {"acme_x", "board"}, {"acme", "bo_ard"},
		{"acme/../hanzo", "board"}, {"acme", "board/x"},
	} {
		if got, ok := name(tc.org, tc.slug); ok {
			t.Fatalf("name(%q, %q) = %q, want a refusal", tc.org, tc.slug, got)
		}
	}
}

// TestADeletedProjectsReplicaIsClosedThenDeleted: a project that is DELETED does
// not get the treatment a project that goes private gets. Its replica is found
// by NAME, so one left behind is one the next project of that slug adopts —
// mirrored history and all — and then publishes. Closed FIRST, because a token
// that may not delete has still been told the half that leaks; and the deletion
// is confirmed by a read, because a 2xx is GitHub agreeing to do something.
func TestADeletedProjectsReplicaIsClosedThenDeleted(t *testing.T) {
	calls, _ := fakeGitHub(t, true)

	if err := remove(context.Background(), "acme", "board"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	var got []string
	for _, c := range *calls {
		got = append(got, c.Method)
		if !strings.HasSuffix(c.Path, "/repos/hanzo-community/acme_board") {
			t.Fatalf("remove touched %q, outside the project's own replica", c.Path)
		}
	}
	want := []string{http.MethodPatch, http.MethodDelete, http.MethodGet}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("remove made %v, want %v (close, delete, confirm)", got, want)
	}
	if v := (*calls)[0].Body["visibility"]; v != "private" {
		t.Fatalf("the close sent visibility=%v, want private", v)
	}
}

// TestRemovingAReplicaThatIsNotThereIsSuccess: absence is the state asked for. A
// retirement that has to be retried — the delete path retries behind the answer
// — must be able to say so by trying again, rather than failing forever on the
// attempt that already worked.
func TestRemovingAReplicaThatIsNotThereIsSuccess(t *testing.T) {
	calls, _ := fakeGitHub(t, false)
	if err := remove(context.Background(), "acme", "board"); err != nil {
		t.Fatalf("removing an absent replica: %v", err)
	}
	if len(*calls) != 1 {
		t.Fatalf("an absent replica costs ONE probe, got %d: %+v", len(*calls), *calls)
	}
}

// TestNoCredentialMeansNoReplica: a dev or test deployment must run the whole
// publish path without reaching for the network, and must NOT register a mirror
// push that could never land.
func TestNoCredentialMeansNoReplica(t *testing.T) {
	t.Setenv(mirrorTokenStem+"_GITHUB_COM", "")
	url, err := ensure(context.Background(), "acme", "board", "", true)
	if err != nil {
		t.Fatalf("unconfigured must be a no-op, got %v", err)
	}
	if url != "" {
		t.Fatalf("no credential must yield no mirror target, got %q", url)
	}
}
