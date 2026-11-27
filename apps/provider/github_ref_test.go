package provider

// github_ref_test.go pins which refs of a repository are ones it publishes from:
// the default branch, a protected branch, a tag, a full commit those branches
// contain — and not a pull request's ref, an unprotected branch, or a commit only
// a fork holds. Every GitHub answer comes from a mock of the API the App reads.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
)

// onMain is a commit the default branch contains; onRelease one only the
// protected release branch contains; fromFork one only a fork's pull request
// holds, which GitHub still serves from the base repository.
const (
	onMain    = "1111111111111111111111111111111111111111"
	onRelease = "2222222222222222222222222222222222222222"
	fromFork  = "3333333333333333333333333333333333333333"
)

// refRepo is luxfi/node as GitHub answers for it: default branch main, a
// protected release branch, an unprotected feature branch, a tag v1.0.0, and a
// name ("both") that is an unprotected branch AND a tag.
type refRepo struct {
	mu    sync.Mutex
	paths []string
	fail  string // a path prefix that answers 500
}

func (g *refRepo) serve(t *testing.T) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		g.paths = append(g.paths, r.URL.Path)
		fail := g.fail
		g.mu.Unlock()
		p := r.URL.Path
		if fail != "" && strings.HasPrefix(p, fail) {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		switch {
		case strings.HasSuffix(p, "/access_tokens") && r.Method == http.MethodPost:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"token": "ghs_inst", "expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
			})
		case p == "/repos/luxfi/node/installation":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 77})
		case r.Header.Get("Authorization") != "Bearer ghs_inst":
			w.WriteHeader(http.StatusUnauthorized)
		case p == "/repos/luxfi/node":
			_ = json.NewEncoder(w).Encode(map[string]any{"full_name": "luxfi/node", "default_branch": "main"})
		case p == "/repos/luxfi/node/branches" && r.URL.Query().Get("protected") == "true":
			_ = json.NewEncoder(w).Encode([]map[string]any{{"name": "main"}, {"name": "release"}})
		case p == "/repos/luxfi/node/branches/release":
			_ = json.NewEncoder(w).Encode(map[string]any{"name": "release", "protected": true})
		case p == "/repos/luxfi/node/branches/feature", p == "/repos/luxfi/node/branches/both":
			_ = json.NewEncoder(w).Encode(map[string]any{"name": "x", "protected": false})
		case p == "/repos/luxfi/node/git/ref/tags/v1.0.0", p == "/repos/luxfi/node/git/ref/tags/both":
			_ = json.NewEncoder(w).Encode(map[string]any{"ref": "refs/tags/x"})
		case strings.HasPrefix(p, "/repos/luxfi/node/compare/"):
			base, head, _ := strings.Cut(strings.TrimPrefix(p, "/repos/luxfi/node/compare/"), "...")
			status := "diverged"
			switch {
			case head == onMain && base == "main", head == onRelease && base == "release":
				status = "behind"
			case head != onMain && head != onRelease && head != fromFork:
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"status": status})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	withGithubApp(t, srv)
}

func (g *refRepo) asked() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.paths...)
}

func TestGitHubRef_OnlyWhatTheRepositoryPublishesFrom(t *testing.T) {
	g := &refRepo{}
	g.serve(t)
	ctx := cloud.For(context.Background(), "lux")
	for _, tc := range []struct {
		ref, kind string
	}{
		{"main", "default"},
		{"refs/heads/main", "default"},
		{"release", "protected"},
		{"refs/heads/release", "protected"},
		{"v1.0.0", "tag"},
		{"refs/tags/v1.0.0", "tag"},
		{onMain, "commit"},
		{onRelease, "commit"},

		{"feature", ""},               // an unprotected branch is whoever pushed it
		{"refs/heads/feature", ""},    // spelled whole, the same branch
		{"both", ""},                  // buildkit resolves a branch before a tag of one name
		{"refs/pull/7/head", ""},      // a pull request's head
		{"refs/pull/7/merge", ""},     // and its merge
		{"pull/7/head", ""},           // buildkit resolves this as refs/pull/7/head
		{"refs/remotes/origin/x", ""}, // anything outside heads and tags
		{"refs/tags/missing", ""},     // a tag the repository does not carry
		{"nothing", ""},               // neither a branch nor a tag
		{fromFork, ""},                // a commit only a fork's pull request holds
		{strings.Repeat("4", 40), ""}, // a commit GitHub does not know
	} {
		got, err := planeGitHubRef(ctx, &client.GitHubRefIn{Owner: "luxfi", Repo: "node", Ref: tc.ref})
		if err != nil {
			t.Fatalf("%s: %v", tc.ref, err)
		}
		if got.Kind != tc.kind || got.Trusted != (tc.kind != "") {
			t.Errorf("%s: got {trusted:%v kind:%q}, want kind %q", tc.ref, got.Trusted, got.Kind, tc.kind)
		}
	}
}

// A pull request's ref is refused on its NAME: nothing about it is asked of the
// branch or tag APIs, so no answer GitHub gives can be read as trusting it.
func TestGitHubRef_APullRefIsRefusedWithoutAsking(t *testing.T) {
	g := &refRepo{}
	g.serve(t)
	got, err := planeGitHubRef(cloud.For(context.Background(), "lux"),
		&client.GitHubRefIn{Owner: "luxfi", Repo: "node", Ref: "refs/pull/7/head"})
	if err != nil || got.Trusted {
		t.Fatalf("a pull ref: %+v %v", got, err)
	}
	for _, p := range g.asked() {
		if strings.Contains(p, "/branches") || strings.Contains(p, "/git/ref") || strings.Contains(p, "/compare") {
			t.Fatalf("a pull ref asked GitHub %s", p)
		}
	}
}

// GitHub not answering is an error, never a trusted ref.
func TestGitHubRef_AnOutageIsNotTrust(t *testing.T) {
	g := &refRepo{fail: "/repos/luxfi/node/branches"}
	g.serve(t)
	for _, ref := range []string{"release", onMain} {
		got, err := planeGitHubRef(cloud.For(context.Background(), "lux"),
			&client.GitHubRefIn{Owner: "luxfi", Repo: "node", Ref: ref})
		if err == nil || (got != nil && got.Trusted) {
			t.Fatalf("%s: an unanswered branch read must fail, got %+v %v", ref, got, err)
		}
	}
}

// Where the App is not installed nothing can be judged, and nothing is trusted.
func TestGitHubRef_NoInstallationNoAnswer(t *testing.T) {
	g := &refRepo{}
	g.serve(t)
	got, err := planeGitHubRef(cloud.For(context.Background(), "lux"),
		&client.GitHubRefIn{Owner: "someone", Repo: "else", Ref: "main"})
	if err == nil || got != nil {
		t.Fatalf("a repository the App is not installed on: %+v %v", got, err)
	}
}

// The call needs a caller, and a ref shaped like a flag or a path escape is not
// a ref.
func TestGitHubRef_RefusesMalformedCalls(t *testing.T) {
	g := &refRepo{}
	g.serve(t)
	if _, err := planeGitHubRef(context.Background(), &client.GitHubRefIn{Owner: "luxfi", Repo: "node", Ref: "main"}); err == nil {
		t.Fatal("no org on the call must be refused")
	}
	for _, ref := range []string{"", "-main", "a..b", "main branch", "../x"} {
		if _, err := planeGitHubRef(cloud.For(context.Background(), "lux"),
			&client.GitHubRefIn{Owner: "luxfi", Repo: "node", Ref: ref}); err == nil {
			t.Errorf("ref %q must be refused", ref)
		}
	}
}
