package git

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/gitcred"
	"github.com/hanzoai/cloud/sdk"
	"github.com/zap-proto/zip"
)

// github stands in for api.github.com and records each token revoked with itself.
func github(t *testing.T) (revoked func() []string) {
	t.Helper()
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete && r.URL.Path == "/installation/token" {
			got = append(got, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	was := api
	api = srv.URL
	t.Cleanup(func() { api = was })
	return func() []string { return got }
}

// grants stands in for the provider process's mint and records who asked for what.
func grants(t *testing.T, mint func(in *client.GitHubGrantIn) (*client.GitHubGrant, error)) (asked func() []string) {
	t.Helper()
	var got []string
	was := askGrant
	askGrant = func(ctx context.Context, in *client.GitHubGrantIn) (*client.GitHubGrant, error) {
		got = append(got, zip.CallerOf(ctx).Org+" "+in.Owner+"/"+in.Repo)
		return mint(in)
	}
	t.Cleanup(func() { askGrant = was })
	return func() []string { return got }
}

func basic(user, tok string) string {
	return base64.StdEncoding.EncodeToString([]byte(user + ":" + tok))
}

// A push to GitHub carries a token the provider process minted for that one
// repository, asked for as the pushing org, and the token is revoked once the push
// is done — not before.
func TestAGitHubMirrorPushCarriesItsOrgsOwnGrant(t *testing.T) {
	revoked := github(t)
	asked := grants(t, func(in *client.GitHubGrantIn) (*client.GitHubGrant, error) {
		return &client.GitHubGrant{Token: "ghs_acme", Owner: in.Owner, Repo: in.Repo}, nil
	})
	t.Setenv(gitcred.Env("github.com"), "shared")

	hdr, end := mirrorCredential(context.Background(), "acme", MirrorTarget{Org: "acme", Repo: "widgets", Host: "github.com", URL: "https://github.com/acme-gh/widgets.git"})
	if hdr != basic("x-access-token", "ghs_acme") {
		t.Fatalf("header = %q, want the org's own grant, not the shared token", hdr)
	}
	if got := asked(); len(got) != 1 || got[0] != "acme acme-gh/widgets" {
		t.Fatalf("asked %v, want one grant for acme-gh/widgets as org acme", got)
	}
	if got := revoked(); len(got) != 0 {
		t.Fatalf("revoked %v before the push ended", got)
	}
	end()
	if got := revoked(); len(got) != 1 || got[0] != "ghs_acme" {
		t.Fatalf("revoked %v, want the minted token once", got)
	}
}

// A target is a URL the org declared, so with no grant of its own — the account
// is not connected, or no provider answers — the push carries nothing, whatever
// this deployment holds for the host. A private remote then fails closed.
func TestADeclaredTargetNeverCarriesTheDeploymentCredential(t *testing.T) {
	revoked := github(t)
	grants(t, func(*client.GitHubGrantIn) (*client.GitHubGrant, error) {
		return nil, errors.New("github grant: no connected account")
	})
	t.Setenv(gitcred.Env("github.com"), "deployment")
	t.Setenv(gitcred.Env("gitlab.com"), "deployment")

	for host, target := range map[string]string{
		"github.com": "https://github.com/someone-else/widgets.git",
		"gitlab.com": "https://gitlab.com/someone-else/widgets.git",
	} {
		hdr, end := mirrorCredential(context.Background(), "acme", MirrorTarget{Org: "acme", Repo: "widgets", Host: host, URL: target})
		end()
		if hdr != "" {
			t.Errorf("%s: a declared target carried %q", target, hdr)
		}
	}
	if got := revoked(); len(got) != 0 {
		t.Fatalf("revoked %v, but no grant was minted", got)
	}
}

// The community replica is the deployment's own repository, on its own account,
// at the name derived from the org and the project: the one target the
// deployment's credential reaches, and no grant is asked for it. Another org's
// replica slot is a declared target like any other.
func TestOnlyTheOrgsOwnReplicaCarriesTheDeploymentCredential(t *testing.T) {
	github(t)
	asked := grants(t, func(*client.GitHubGrantIn) (*client.GitHubGrant, error) {
		return nil, errors.New("github grant: no connected account")
	})
	t.Setenv(gitcred.Env("github.com"), "deployment")

	hdr, end := mirrorCredential(context.Background(), "acme", MirrorTarget{Org: "acme", Repo: "widgets", Host: "github.com", URL: "https://github.com/hanzo-community/acme_widgets.git"})
	end()
	if hdr != basic("x-access-token", "deployment") {
		t.Fatalf("the org's own replica got %q, want the deployment's credential", hdr)
	}
	if got := asked(); len(got) != 0 {
		t.Fatalf("asked %v for the deployment's own replica", got)
	}
	hdr, end = mirrorCredential(context.Background(), "acme", MirrorTarget{Org: "acme", Repo: "widgets", Host: "github.com", URL: "https://github.com/hanzo-community/globex_widgets.git"})
	end()
	if hdr != "" {
		t.Fatalf("another org's replica slot carried %q", hdr)
	}
}

// Only GitHub is asked for a grant; a GitLab target has none to carry, and a
// host off the outbound allowlist gets no credential at all.
func TestOnlyGitHubIsAskedForAGrant(t *testing.T) {
	github(t)
	asked := grants(t, func(in *client.GitHubGrantIn) (*client.GitHubGrant, error) {
		return &client.GitHubGrant{Token: "ghs_x", Owner: in.Owner, Repo: in.Repo}, nil
	})
	t.Setenv(gitcred.Env("gitlab.com"), "lab")
	t.Setenv(gitcred.Env("git.hanzo.ai"), "forge")

	if hdr, end := mirrorCredential(context.Background(), "acme", MirrorTarget{Org: "acme", Repo: "widgets", Host: "gitlab.com", URL: "https://gitlab.com/acme/widgets.git"}); hdr != "" {
		t.Fatalf("gitlab header = %q, want nothing: no grant is minted there", hdr)
	} else {
		end()
	}
	if hdr, end := mirrorCredential(context.Background(), "acme", MirrorTarget{Org: "acme", Repo: "widgets", Host: "git.hanzo.ai", URL: "https://git.hanzo.ai/acme/widgets.git"}); hdr != "" {
		t.Fatalf("a host off the allowlist got a credential: %q", hdr)
	} else {
		end()
	}
	if got := asked(); len(got) != 0 {
		t.Fatalf("asked %v, want no grant for a non-GitHub host", got)
	}
}

// A target names the org's own replica only when it is exactly the URL the
// deployment provisioned for that repository: an encoded path, or a name under
// the org's prefix that was never provisioned, is a declared target like any
// other.
func TestOwnReplicaIsAnExactMatch(t *testing.T) {
	widgets := func(url string) MirrorTarget {
		return MirrorTarget{Org: "acme", Repo: "widgets", Host: "github.com", URL: url}
	}
	for _, target := range []MirrorTarget{
		widgets("https://github.com/hanzo-community/acme_x%2f..%2fattacker%2frepo.git"),
		widgets("https://github.com/hanzo-community/acme_x%2f..%2fglobex%5fsecret.git"),
		widgets("https://github.com/hanzo-community/acme_neverprovisioned.git"),
		widgets("https://github.com/HANZO-COMMUNITY/acme_widgets.git"),
		widgets("https://github.com/hanzo-community/acme_widgets.git/"),
		{Org: "acme", Project: "site", Repo: "widgets", Host: "github.com", URL: "https://github.com/hanzo-community/acme_widgets.git"},
	} {
		if ownReplica("acme", target) {
			t.Errorf("%+v was taken for acme's own replica", target)
		}
	}
	if !ownReplica("acme", widgets("https://github.com/hanzo-community/acme_widgets.git")) {
		t.Error("acme's provisioned replica was not recognized")
	}
}

// When the org holds no GitHub App installation for the target repository but
// the caller context carries an authenticated user, mirrorCredential falls back
// to IntegrationsGitHubRun so a user who connected their personal GitHub account
// can push to repositories their account reaches, and revokes the minted token
// once the push completes.
func TestMirrorCredentialFallsBackToUserRunToken(t *testing.T) {
	revoked := github(t)
	grants(t, func(*client.GitHubGrantIn) (*client.GitHubGrant, error) {
		return nil, errors.New("github grant: no org installation")
	})
	var runAsked []string
	wasRun := askRun
	askRun = func(ctx context.Context, in *client.GitHubRunIn) (*client.GitHubRun, error) {
		who := zip.CallerOf(ctx)
		runAsked = append(runAsked, who.Org+":"+who.User+" "+in.Owner+"/"+in.Repo)
		return &client.GitHubRun{Token: "ghs_user_scoped", Owner: in.Owner, Repo: in.Repo}, nil
	}
	t.Cleanup(func() { askRun = wasRun })

	ctx := zip.WithCaller(context.Background(), zip.Caller{Org: "acme", User: "alice"})
	hdr, end := mirrorCredential(ctx, "acme", MirrorTarget{
		Org: "acme", Repo: "widgets", Host: "github.com", URL: "https://github.com/octo-alice/widgets.git",
	})
	if hdr != basic("x-access-token", "ghs_user_scoped") {
		t.Fatalf("header = %q, want user-run fallback token", hdr)
	}
	if len(runAsked) != 1 || runAsked[0] != "acme:alice octo-alice/widgets" {
		t.Fatalf("runAsked = %v, want [acme:alice octo-alice/widgets]", runAsked)
	}
	end()
	if got := revoked(); len(got) != 1 || got[0] != "ghs_user_scoped" {
		t.Fatalf("revoked = %v, want [ghs_user_scoped]", got)
	}
}

// Attaching a GitHub mirror target to a repository that already has commits on
// its default branch immediately triggers a best-effort initial push (both via
// POST /v1/git/repos/:name/targets and via EnsureMirror), while attaching to an
// empty repository does nothing.
func TestAttachTargetKicksInitialMirrorPushForNonEmptyRepo(t *testing.T) {
	app := mountApp(t)
	base := liveServer(t, app)

	type pushed struct {
		url    string
		branch string
		org    string
	}
	var calls []pushed
	wasHook := initialMirrorPushHook
	initialMirrorPushHook = func(_ *cloud.Service[state], _ context.Context, target MirrorTarget, branch string) {
		calls = append(calls, pushed{url: target.URL, branch: branch, org: target.Org})
	}
	t.Cleanup(func() { initialMirrorPushHook = wasHook })

	// 1. Empty repo: attaching a mirror target must NOT kick off a push.
	if code, b := do(t, app, http.MethodPost, "/v1/git/repos", "acme", map[string]any{"name": "empty"}); code != 201 {
		t.Fatalf("create empty: %d %s", code, b)
	}
	if code, b := do(t, app, http.MethodPost, "/v1/git/repos/empty/targets", "acme",
		map[string]any{"url": "https://github.com/acme/empty.git"}); code != 201 {
		t.Fatalf("attach empty target: %d %s", code, b)
	}
	if len(calls) != 0 {
		t.Fatalf("empty repo must not trigger initial mirror push, got %+v", calls)
	}

	// 2. Non-empty repo with commits on main: attaching via POST /targets triggers initial push.
	if code, b := do(t, app, http.MethodPost, "/v1/git/repos", "acme", map[string]any{"name": "seeded"}); code != 201 {
		t.Fatalf("create seeded: %d %s", code, b)
	}
	pushCommit(t, base, "acme", "seeded", "main", "initial commit")
	if code, b := do(t, app, http.MethodPost, "/v1/git/repos/seeded/targets", "acme",
		map[string]any{"url": "https://github.com/acme/seeded.git"}); code != 201 {
		t.Fatalf("attach seeded target: %d %s", code, b)
	}
	if len(calls) != 1 || calls[0].url != "https://github.com/acme/seeded.git" || calls[0].branch != "main" || calls[0].org != "acme" {
		t.Fatalf("POST /targets initial push = %+v, want main -> https://github.com/acme/seeded.git", calls)
	}

	// 3. EnsureMirror (used by POST /v1/provider/github/repos with link_repo) also triggers initial push.
	if err := (gitMirrorController{}).EnsureMirror(context.Background(), "acme", "", "seeded", "https://gitlab.com/acme/seeded.git", true); err != nil {
		t.Fatalf("EnsureMirror: %v", err)
	}
	if len(calls) != 2 || calls[1].url != "https://gitlab.com/acme/seeded.git" || calls[1].branch != "main" {
		t.Fatalf("EnsureMirror initial push = %+v", calls)
	}
}
