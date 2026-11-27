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
