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

// fakeGitHub serves the two endpoints ensureGitHubRepo uses. `exists` decides
// whether the repo is already there, which is the whole branch under test.
func fakeGitHub(t *testing.T, exists bool) (*[]ghCall, func()) {
	t.Helper()
	var mu sync.Mutex
	calls := []ghCall{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(b, &body)
		mu.Lock()
		calls = append(calls, ghCall{Method: r.Method, Path: r.URL.Path, Body: body})
		mu.Unlock()

		switch {
		case r.Method == http.MethodPatch && !exists:
			w.WriteHeader(http.StatusNotFound)
		case r.Method == http.MethodPatch:
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPost:
			w.WriteHeader(http.StatusCreated)
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	old := ghAPIBase
	ghAPIBase = srv.URL
	t.Setenv(mirrorEnvToken, "test-token")
	t.Cleanup(func() { ghAPIBase = old; srv.Close() })
	return &calls, srv.Close
}

// TestCommunityRepoIsCreatedWhenMissing: we hold admin on the community org, so
// a first publish CREATES the far-side repo rather than assuming somebody
// provisioned it. Without this the mirror would force-push at a target that does
// not exist, and fail for every project forever.
func TestCommunityRepoIsCreatedWhenMissing(t *testing.T) {
	calls, _ := fakeGitHub(t, false)

	url, err := ensureGitHubRepo(context.Background(), "acme", "board", "a board", true)
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if want := "https://github.com/hanzo-community/acme-board.git"; url != want {
		t.Fatalf("clone url = %q, want %q", url, want)
	}
	if len(*calls) != 2 {
		t.Fatalf("want probe-then-create, got %d calls: %+v", len(*calls), *calls)
	}
	create := (*calls)[1]
	if create.Method != http.MethodPost || !strings.HasSuffix(create.Path, "/orgs/hanzo-community/repos") {
		t.Fatalf("second call must create in the community org: %+v", create)
	}
	if create.Body["name"] != "acme-board" {
		t.Fatalf("repo name = %v, want acme-board (org-qualified: one flat namespace)", create.Body["name"])
	}
	if create.Body["private"] != false {
		t.Fatalf("a public project must be born public, got private=%v", create.Body["private"])
	}
}

// TestCommunityRepoIsBornPrivate: a private project's replica must never be
// briefly public. Visibility is set AT creation, not patched afterwards.
func TestCommunityRepoIsBornPrivate(t *testing.T) {
	calls, _ := fakeGitHub(t, false)

	if _, err := ensureGitHubRepo(context.Background(), "acme", "secret", "", false); err != nil {
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

			if _, err := ensureGitHubRepo(context.Background(), "acme", "board", "", tc.listed); err != nil {
				t.Fatalf("ensure: %v", err)
			}
			if len(*calls) != 1 {
				t.Fatalf("an existing replica needs ONE patch, got %d: %+v", len(*calls), *calls)
			}
			c := (*calls)[0]
			if c.Method != http.MethodPatch {
				t.Fatalf("existing repo must be patched, not %s", c.Method)
			}
			if c.Body["private"] != tc.wantPrivate {
				t.Fatalf("private = %v, want %v", c.Body["private"], tc.wantPrivate)
			}
			if _, sent := c.Body["description"]; sent {
				t.Fatal("description must not be re-imposed: an author's own edit has to survive")
			}
		})
	}
}

// TestNoCredentialMeansNoReplica: a dev or test deployment must run the whole
// publish path without reaching for the network, and must NOT register a mirror
// push that could never land.
func TestNoCredentialMeansNoReplica(t *testing.T) {
	t.Setenv(mirrorEnvToken, "")
	url, err := ensureGitHubRepo(context.Background(), "acme", "board", "", true)
	if err != nil {
		t.Fatalf("unconfigured must be a no-op, got %v", err)
	}
	if url != "" {
		t.Fatalf("no credential must yield no mirror target, got %q", url)
	}
}
