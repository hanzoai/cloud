package provider

// github_grant_test.go pins what a run's GitHub token can reach: one repository
// of an account the caller's org connected, with contents and pull_requests
// write and nothing else — as asked of GitHub, and as GitHub answered.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
)

// mintAnswer is GitHub's answer to a scoped token request; asked records each
// scoped request and where it was made. Every installation also mints its own
// shared token, "ghs_inst_<id>", which is what reads a repository.
type mintAnswer struct {
	status int
	body   map[string]any
	asked  []map[string]any
	paths  []string
	// where names each repository GitHub knows, by the path its OLD name answers
	// at, as {full name, the installation whose token can see it}. A transferred
	// repository's old name redirects to its id, as GitHub's does.
	where map[string][2]string
}

func serveMint(t *testing.T, a *mintAnswer) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/access_tokens") && r.Method == http.MethodPost:
			var in map[string]any
			_ = json.NewDecoder(r.Body).Decode(&in)
			if _, scoped := in["repositories"]; !scoped {
				id := strings.Split(r.URL.Path, "/")[3]
				_ = json.NewEncoder(w).Encode(map[string]any{
					"token": "ghs_inst_" + id, "expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
				})
				return
			}
			a.asked = append(a.asked, in)
			a.paths = append(a.paths, r.URL.Path)
			w.WriteHeader(a.status)
			_ = json.NewEncoder(w).Encode(a.body)
		case strings.HasPrefix(r.URL.Path, "/repos/"):
			if at, ok := a.where[r.URL.Path]; ok && at[0] != strings.TrimPrefix(r.URL.Path, "/repos/") {
				http.Redirect(w, r, "/repositories/"+at[0], http.StatusMovedPermanently)
				return
			}
			fallthrough
		case strings.HasPrefix(r.URL.Path, "/repositories/"):
			for _, at := range a.where {
				if r.URL.Path == "/repos/"+at[0] || r.URL.Path == "/repositories/"+at[0] {
					if r.Header.Get("Authorization") != "Bearer ghs_inst_"+at[1] {
						break // a private repository is invisible to other accounts' installations
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"full_name": at[0]})
					return
				}
			}
			w.WriteHeader(http.StatusNotFound)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	withGithubApp(t, srv)
}

// scoped is GitHub honouring the request exactly.
func scoped(full string) map[string]any {
	return map[string]any{
		"token":        "ghs_scoped",
		"permissions":  map[string]any{"contents": "write", "pull_requests": "write", "metadata": "read"},
		"repositories": []map[string]any{{"full_name": full}},
	}
}

// site is hanzo-apps/site where it has always been, seen by installation 555.
var site = map[string][2]string{"/repos/hanzo-apps/site": {"hanzo-apps/site", "555"}}

func connect(t *testing.T, org, owner, id string) {
	t.Helper()
	if err := mounted.State.store.Upsert(context.Background(), Connection{
		Org: org, Provider: "github", Label: owner, ExternalID: id, AccountLabel: owner,
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
}

// ONE REPOSITORY, TWO PERMISSIONS: that is the request, and nothing wider is
// accepted back.
func TestGitHubGrant_OneRepositoryTwoPermissions(t *testing.T) {
	newApp(t, newKMS(t))
	a := &mintAnswer{status: http.StatusCreated, body: scoped("hanzo-apps/site"), where: site}
	serveMint(t, a)
	connect(t, "acme", "hanzo-apps", "555")

	got, err := planeGitHubGrant(cloud.For(context.Background(), "acme"),
		&client.GitHubGrantIn{Owner: "hanzo-apps", Repo: "site"})
	if err != nil {
		t.Fatalf("grant: %v", err)
	}
	if got.Token != "ghs_scoped" || got.Owner != "hanzo-apps" || got.Repo != "site" {
		t.Fatalf("answer = %q %s/%s", got.Token, got.Owner, got.Repo)
	}
	if len(a.asked) != 1 || a.paths[0] != "/app/installations/555/access_tokens" {
		t.Fatalf("minted %d times at %v, want once on the org's installation", len(a.asked), a.paths)
	}
	repos, _ := a.asked[0]["repositories"].([]any)
	if len(repos) != 1 || repos[0] != "site" {
		t.Fatalf("the token was asked for repositories %v, want [site]", a.asked[0]["repositories"])
	}
	perms, _ := a.asked[0]["permissions"].(map[string]any)
	if len(perms) != 2 || perms["contents"] != "write" || perms["pull_requests"] != "write" {
		t.Fatalf("the token was asked for %v, want contents and pull_requests write only", perms)
	}
}

// A PLAN'S TOKEN READS AND NOTHING ELSE: the request names contents:read alone,
// and an answer that could write is refused and never leaves the process.
func TestGitHubGrant_APlanAsksToReadOnly(t *testing.T) {
	newApp(t, newKMS(t))
	read := map[string]any{
		"token":        "ghs_read",
		"permissions":  map[string]any{"contents": "read", "metadata": "read"},
		"repositories": []map[string]any{{"full_name": "hanzo-apps/site"}},
	}
	a := &mintAnswer{status: http.StatusCreated, body: read, where: site}
	serveMint(t, a)
	connect(t, "acme", "hanzo-apps", "555")

	got, err := planeGitHubGrant(cloud.For(context.Background(), "acme"),
		&client.GitHubGrantIn{Owner: "hanzo-apps", Repo: "site", Read: true})
	if err != nil || got.Token != "ghs_read" {
		t.Fatalf("grant: %+v %v", got, err)
	}
	perms, _ := a.asked[0]["permissions"].(map[string]any)
	if len(perms) != 1 || perms["contents"] != "read" {
		t.Fatalf("a plan's token was asked for %v, want contents:read alone", perms)
	}

	a.body = scoped("hanzo-apps/site") // GitHub answering with write
	if _, err := planeGitHubGrant(cloud.For(context.Background(), "acme"),
		&client.GitHubGrantIn{Owner: "hanzo-apps", Repo: "site", Read: true}); err == nil {
		t.Fatal("a token that can write must not be handed to a plan")
	}
}

// GitHub's ANSWER is held to the request. A token for another repository — the
// single-account fallback reaching a same-named one — or one wider than asked
// never leaves the process.
func TestGitHubGrant_RefusesAnAnswerWiderThanTheRequest(t *testing.T) {
	for name, body := range map[string]map[string]any{
		"another account's repository": scoped("acme-gh/site"),
		"two repositories": {
			"token":        "ghs_wide",
			"permissions":  map[string]any{"contents": "write", "pull_requests": "write"},
			"repositories": []map[string]any{{"full_name": "hanzo-apps/site"}, {"full_name": "hanzo-apps/api"}},
		},
		"a permission not asked for": {
			"token":        "ghs_wide",
			"permissions":  map[string]any{"contents": "write", "pull_requests": "write", "administration": "write"},
			"repositories": []map[string]any{{"full_name": "hanzo-apps/site"}},
		},
		"a permission missing": {
			"token":        "ghs_narrow",
			"permissions":  map[string]any{"contents": "write"},
			"repositories": []map[string]any{{"full_name": "hanzo-apps/site"}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			newApp(t, newKMS(t))
			serveMint(t, &mintAnswer{status: http.StatusCreated, body: body, where: site})
			connect(t, "acme", "hanzo-apps", "555")

			got, err := planeGitHubGrant(cloud.For(context.Background(), "acme"),
				&client.GitHubGrantIn{Owner: "hanzo-apps", Repo: "site"})
			if err == nil {
				t.Fatalf("a token GitHub scoped differently was handed out: %q", got.Token)
			}
		})
	}
}

// An account the org never connected is refused before GitHub is asked.
func TestGitHubGrant_RefusesAnAccountTheOrgDidNotConnect(t *testing.T) {
	newApp(t, newKMS(t))
	a := &mintAnswer{status: http.StatusCreated, body: scoped("hanzo-apps/site"), where: site}
	serveMint(t, a)
	connect(t, "someone-else", "hanzo-apps", "555")

	if _, err := planeGitHubGrant(cloud.For(context.Background(), "acme"),
		&client.GitHubGrantIn{Owner: "hanzo-apps", Repo: "site"}); err == nil {
		t.Fatal("an org minted on an installation another org connected")
	}
	if len(a.asked) != 0 {
		t.Fatalf("GitHub was asked for a token the org has no connection for")
	}
}

// An App that lacks a permission on the account is an owner's approval to give,
// and the refusal says so rather than working around it.
func TestGitHubGrant_NamesTheApprovalAnOwnerMustGive(t *testing.T) {
	newApp(t, newKMS(t))
	serveMint(t, &mintAnswer{status: http.StatusUnprocessableEntity, where: site,
		body: map[string]any{"message": "The permissions requested are not granted to this installation."}})
	connect(t, "acme", "hanzo-apps", "555")

	_, err := planeGitHubGrant(cloud.For(context.Background(), "acme"),
		&client.GitHubGrantIn{Owner: "hanzo-apps", Repo: "site"})
	if err == nil || !strings.Contains(err.Error(), "an owner of hanzo-apps must approve") {
		t.Fatalf("the refusal does not name the approval: %v", err)
	}
}

// A TRANSFERRED REPOSITORY is minted where it lives now. The mirror still names
// hanzoai/hanzo.ai, GitHub redirects that to hanzo-apps/hanzo.ai, and a token
// asked of the hanzoai installation would reach nothing — so the repository is
// resolved first, the installation is the new owner's, the token names the
// resolved repository, and the answer says where that is.
func TestGitHubGrant_ATransferredRepositoryIsMintedWhereItLivesNow(t *testing.T) {
	newApp(t, newKMS(t))
	a := &mintAnswer{status: http.StatusCreated, body: scoped("hanzo-apps/hanzo.ai"),
		where: map[string][2]string{"/repos/hanzoai/hanzo.ai": {"hanzo-apps/hanzo.ai", "222"}}}
	serveMint(t, a)
	connect(t, "acme", "hanzoai", "111")
	connect(t, "acme", "hanzo-apps", "222")

	got, err := planeGitHubGrant(cloud.For(context.Background(), "acme"),
		&client.GitHubGrantIn{Owner: "hanzoai", Repo: "hanzo.ai"})
	if err != nil {
		t.Fatalf("grant: %v", err)
	}
	if got.Owner != "hanzo-apps" || got.Repo != "hanzo.ai" {
		t.Fatalf("the answer names %s/%s, want where the repository lives now", got.Owner, got.Repo)
	}
	if len(a.paths) != 1 || a.paths[0] != "/app/installations/222/access_tokens" {
		t.Fatalf("minted at %v, want once on the new owner's installation", a.paths)
	}
	if repos, _ := a.asked[0]["repositories"].([]any); len(repos) != 1 || repos[0] != "hanzo.ai" {
		t.Fatalf("the token was asked for %v, want the resolved repository", a.asked[0]["repositories"])
	}

	// The same answer held against the OLD name is refused: the check compares
	// against where the repository was resolved to, not where it was asked for.
	a.body = scoped("hanzoai/hanzo.ai")
	if _, err := planeGitHubGrant(cloud.For(context.Background(), "acme"),
		&client.GitHubGrantIn{Owner: "hanzoai", Repo: "hanzo.ai"}); err == nil {
		t.Fatal("a token for the old name was accepted for the resolved repository")
	}
}
