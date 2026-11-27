package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/zap-proto/zip"
)

// github_disconnect_test.go proves an org can let go of ONE GitHub account, and
// that letting go removes the App there too unless another org still uses it.
//
// The case that motivated it: an org connected the wrong account and could not
// switch. Disconnect dropped every account at once and left the App installed on
// GitHub, so connecting the same account again landed on GitHub's Configure page,
// which returns nothing the connect can bind.

// mockUninstall serves DELETE /app/installations/{id} and records each id asked for.
func mockUninstall(t *testing.T) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var deleted []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/app/installations/") {
			if r.Header.Get("Authorization") == "" {
				t.Errorf("uninstall must be signed with the App JWT")
			}
			mu.Lock()
			deleted = append(deleted, strings.TrimPrefix(r.URL.Path, "/app/installations/"))
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	return srv, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(deleted)
	}
}

// adminPost is a POST as org's admin — what principal.IsOrgAdmin reads.
func adminPost(t *testing.T, app *zip.App, path, org string, body any) httpResult {
	t.Helper()
	b, _ := json.Marshal(body)
	rq := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b))
	rq.Header.Set("Content-Type", "application/json")
	rq.Header.Set("X-Org-Id", org)
	rq.Header.Set("X-User-Id", "u-"+org)
	rq.Header.Set("X-User-IsOrgAdmin", "true")
	resp, err := app.Test(rq, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	buf := new(bytes.Buffer)
	_, _ = buf.ReadFrom(resp.Body)
	return httpResult{Code: resp.StatusCode, Body: buf.Bytes()}
}

func bindGitHub(t *testing.T, org, login, installation string) {
	t.Helper()
	if err := mounted.State.store.Upsert(context.Background(), Connection{
		Org: org, Provider: "github", Label: login, ExternalID: installation, AccountLabel: login,
	}); err != nil {
		t.Fatalf("bind %s/%s: %v", org, login, err)
	}
}

func githubLabels(t *testing.T, org string) []string {
	t.Helper()
	var out []string
	for _, c := range Connections(org, "github") {
		out = append(out, c.Label)
	}
	return out
}

func disconnected(t *testing.T, r httpResult) disconnectOut {
	t.Helper()
	if r.Code != http.StatusOK {
		t.Fatalf("disconnect want 200, got %d (%s)", r.Code, r.Body)
	}
	var out disconnectOut
	if err := json.Unmarshal(r.Body, &out); err != nil {
		t.Fatalf("json: %v (%s)", err, r.Body)
	}
	return out
}

// TestDisconnectOneGitHubAccount drops the named account, uninstalls the App there,
// and leaves the org's other account bound and installed.
func TestDisconnectOneGitHubAccount(t *testing.T) {
	srv, deleted := mockUninstall(t)
	withGithubApp(t, srv)
	app := newApp(t, newKMS(t))
	bindGitHub(t, "acme", "acme-gh", "111")
	bindGitHub(t, "acme", "acme-labs", "222")

	out := disconnected(t, adminPost(t, app, "/v1/provider/github/disconnect", "acme", map[string]string{"account": "ACME-GH"}))
	if !out.Disconnected || !slices.Equal(out.Accounts, []string{"acme-gh"}) || !slices.Equal(out.Uninstalled, []string{"acme-gh"}) {
		t.Fatalf("want acme-gh removed and uninstalled, got %+v", out)
	}
	if got := deleted(); !slices.Equal(got, []string{"111"}) {
		t.Fatalf("want installation 111 uninstalled, got %v", got)
	}
	if got := githubLabels(t, "acme"); !slices.Equal(got, []string{"acme-labs"}) {
		t.Fatalf("the org's other account must stay bound, got %v", got)
	}
}

// TestDisconnectKeepsAnotherOrgsInstallation is the guard: an installation two orgs
// hold stays on GitHub when one of them disconnects, so the other's mirror keeps working.
func TestDisconnectKeepsAnotherOrgsInstallation(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		srv, deleted := mockUninstall(t)
		withGithubApp(t, srv)
		app := newApp(t, newKMS(t))
		bindGitHub(t, "acme", "shared", "333")
		// A second org holding the same account is a claim (githubClaim → Share), not a connect.
		if err := mounted.State.store.Share(context.Background(), Connection{
			Org: "beta", Provider: "github", Label: "shared", ExternalID: "333", AccountLabel: "shared",
		}); err != nil {
			t.Fatalf("share beta/shared: %v", err)
		}

		out := disconnected(t, adminPost(t, app, "/v1/provider/github/disconnect", "acme", map[string]string{"account": "shared"}))
		if !slices.Equal(out.Accounts, []string{"shared"}) || len(out.Uninstalled) != 0 {
			t.Fatalf("want shared removed for acme and NOT uninstalled, got %+v", out)
		}
		if got := deleted(); len(got) != 0 {
			t.Fatalf("an installation another org holds must not be uninstalled, got %v", got)
		}
		if got := githubLabels(t, "beta"); !slices.Equal(got, []string{"shared"}) {
			t.Fatalf("beta must keep its binding, got %v", got)
		}

		// The last holder letting go removes it.
		out = disconnected(t, adminPost(t, app, "/v1/provider/github/disconnect", "beta", map[string]string{"account": "shared"}))
		if !slices.Equal(out.Uninstalled, []string{"shared"}) || !slices.Equal(deleted(), []string{"333"}) {
			t.Fatalf("the last holder's disconnect must uninstall 333, got %+v deleted=%v", out, deleted())
		}
	})
}

// TestDisconnectEveryGitHubAccount keeps the old meaning of an empty account: all of
// them go, and each is uninstalled.
func TestDisconnectEveryGitHubAccount(t *testing.T) {
	srv, deleted := mockUninstall(t)
	withGithubApp(t, srv)
	app := newApp(t, newKMS(t))
	bindGitHub(t, "acme", "acme-gh", "111")
	bindGitHub(t, "acme", "acme-labs", "222")

	out := disconnected(t, adminPost(t, app, "/v1/provider/github/disconnect", "acme", map[string]string{}))
	if len(out.Accounts) != 2 || len(out.Uninstalled) != 2 {
		t.Fatalf("want both accounts removed and uninstalled, got %+v", out)
	}
	got := deleted()
	slices.Sort(got)
	if !slices.Equal(got, []string{"111", "222"}) {
		t.Fatalf("want 111 and 222 uninstalled, got %v", got)
	}
	if got := githubLabels(t, "acme"); len(got) != 0 {
		t.Fatalf("no account may remain, got %v", got)
	}
}

// TestDisconnectUnknownGitHubAccount names an account the org does not hold: nothing
// is removed or uninstalled, and the answer stays idempotent.
func TestDisconnectUnknownGitHubAccount(t *testing.T) {
	srv, deleted := mockUninstall(t)
	withGithubApp(t, srv)
	app := newApp(t, newKMS(t))
	bindGitHub(t, "acme", "acme-gh", "111")

	out := disconnected(t, adminPost(t, app, "/v1/provider/github/disconnect", "acme", map[string]string{"account": "someone-else"}))
	if !out.Disconnected || len(out.Accounts) != 0 || len(out.Uninstalled) != 0 || len(deleted()) != 0 {
		t.Fatalf("an account the org does not hold must change nothing, got %+v deleted=%v", out, deleted())
	}
	if got := githubLabels(t, "acme"); !slices.Equal(got, []string{"acme-gh"}) {
		t.Fatalf("acme-gh must stay bound, got %v", got)
	}
}

// TestDisconnectGitHubNeedsOrgAdmin keeps the admin gate: a member cannot remove an
// account, or the App with it.
func TestDisconnectGitHubNeedsOrgAdmin(t *testing.T) {
	srv, deleted := mockUninstall(t)
	withGithubApp(t, srv)
	app := newApp(t, newKMS(t))
	bindGitHub(t, "acme", "acme-gh", "111")

	if r := req(t, app, http.MethodPost, "/v1/provider/github/disconnect", "acme", map[string]string{"account": "acme-gh"}); r.Code != http.StatusForbidden {
		t.Fatalf("a member's disconnect want 403, got %d (%s)", r.Code, r.Body)
	}
	if len(deleted()) != 0 || len(githubLabels(t, "acme")) != 1 {
		t.Fatalf("a refused disconnect must change nothing")
	}
}
