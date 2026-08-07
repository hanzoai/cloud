package git

// propose_test.go proves the ONE thing that makes "paste a link from either host
// and it works" true: which backend answers is decided by where the code lives,
// and a repository that lives on GitHub never quietly gets a forge link instead
// of the pull request its reviewers are waiting on.

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

// A repository that lives only in the forge is read at its branch's own page.
func TestProposeAnswersTheBranchPageForAForgeRepo(t *testing.T) {
	app := mountApp(t)
	if code, b := do(t, app, http.MethodPost, "/v1/git/repos", "acme", map[string]any{"name": "api"}); code != 201 {
		t.Fatalf("create repo: %d %s", code, b)
	}

	url, err := Propose(context.Background(), "acme", "", "api", "main",
		"agent/abc123def456", "api: fix the flake", "body")
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	const want = "https://api.hanzo.test/git/acme/api?ref=agent%2Fabc123def456"
	if url != want {
		t.Fatalf("branch page = %q, want %q", url, want)
	}
}

// A repository that mirrors into GitHub is proposed THERE, and when that cannot
// happen the run is told so.
//
// The failure is the assertion. There is no GitHub App connection in a test, so
// the credential cannot be minted — and the answer must be an error rather than
// the forge link the previous case returns. A run whose reviewers are on GitHub,
// handed a forge URL and told everything is fine, is the quiet wrong answer this
// seam exists to make impossible.
func TestProposeWillNotSubstituteAForgeLinkForAGitHubRepo(t *testing.T) {
	app := mountApp(t)
	if code, b := do(t, app, http.MethodPost, "/v1/git/repos", "acme", map[string]any{"name": "api"}); code != 201 {
		t.Fatalf("create repo: %d %s", code, b)
	}
	store, err := storeFor(mounted.Load(), "acme")
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	must(t, store.CreateMirror(context.Background(), MirrorTarget{
		ID: "m1", Org: "acme", Repo: "api", Host: "github.com",
		URL: "https://github.com/hanzo-inc/api.git", CreatedAt: time.Now().Unix(),
	}))

	url, err := Propose(context.Background(), "acme", "", "api", "main",
		"agent/abc123def456", "api: fix the flake", "body")
	if err == nil {
		t.Fatalf("a GitHub repo answered without a pull request: %q", url)
	}
	if url != "" {
		t.Fatalf("a failed proposal still returned an address: %q", url)
	}
	if !strings.Contains(err.Error(), "hanzo-inc") {
		t.Fatalf("the error does not name the account it could not reach: %v", err)
	}
}

// A head is checked as a BRANCH before it reaches a refspec.
//
// The load-bearing half is the first character class: the value lands in a `git
// push` argument position, where anything beginning with '-' is a flag rather
// than a branch and `--upload-pack=` is a command of the caller's choosing on
// this machine. Length and whitespace matter too; the dash is the one that turns
// data into a program.
func TestProposeRefusesAHeadThatIsNotABranch(t *testing.T) {
	mountApp(t)
	for _, head := range []string{"", "--upload-pack=touch /tmp/x", "-x", "a b", "a\nb", strings.Repeat("a", 200)} {
		if _, err := Propose(context.Background(), "acme", "", "api", "main", head, "t", "b"); err == nil {
			t.Fatalf("propose accepted %q as a branch", head)
		}
	}
}

// A project-scoped repository has no browsable page, and says so rather than
// pointing at one that would 404.
func TestProposeHasNoPageForAProjectScopedRepo(t *testing.T) {
	app := mountApp(t)
	if code, b := do(t, app, http.MethodPost, "/v1/git/repos", "acme", map[string]any{"name": "api"}); code != 201 {
		t.Fatalf("create repo: %d %s", code, b)
	}
	url, err := Propose(context.Background(), "acme", "web", "api", "main", "agent/abc123def456", "t", "b")
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	if url != "" {
		t.Fatalf("a project-scoped repo answered with %q, which is not a page", url)
	}
}

func TestGitHubRepoOf(t *testing.T) {
	for remote, want := range map[string][2]string{
		"https://github.com/hanzo-inc/cloud.git": {"hanzo-inc", "cloud"},
		"https://github.com/hanzoai/cloud":       {"hanzoai", "cloud"},
		"https://github.com/hanzoai/":            {"hanzoai", ""},
		"https://github.com/":                    {"", ""},
		"::not a url::":                          {"", ""},
	} {
		owner, name := githubRepoOf(remote)
		if owner != want[0] || name != want[1] {
			t.Fatalf("githubRepoOf(%q) = %q,%q want %q,%q", remote, owner, name, want[0], want[1])
		}
	}
}
