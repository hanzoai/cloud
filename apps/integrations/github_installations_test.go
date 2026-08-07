package integrations

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zap-proto/zip"
)

// github_installations_test.go proves who may see WHICH GitHub accounts the App is
// installed on. The App is installed across every customer, so the raw list is the
// customer list: a tenant must see only what its own org bound, and only platform
// sudo may read the whole inventory.
//
// The case that motivated it: an App granted straight from GitHub runs no connect
// flow, so no connection row exists and every org-scoped surface reports nothing —
// the console card reads "not connected" and an agent asked "which of my GitHub
// orgs do you see" has no true answer to give.

// mockInstallations serves GET /app/installations (paginated like GitHub's) plus
// the access-token mint, so the App-JWT path under test is the production one.
func mockInstallations(t *testing.T, accounts []map[string]any) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/app/installations":
			// Page 1 carries the set; later pages are empty, which ends the walk.
			if r.URL.Query().Get("page") != "1" {
				_ = json.NewEncoder(w).Encode([]map[string]any{})
				return
			}
			_ = json.NewEncoder(w).Encode(accounts)
		case strings.HasSuffix(r.URL.Path, "/access_tokens"):
			_ = json.NewEncoder(w).Encode(map[string]any{"token": "ghs_x"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// superReq is req with the platform-sudo header the identity boundary mints only
// for a validated owner == "admin" — what principal.IsSuperAdmin reads.
func superReq(t *testing.T, app *zip.App, method, path, org string) httpResult {
	t.Helper()
	rq := httptest.NewRequest(method, path, nil)
	rq.Header.Set("X-Org-Id", org)
	rq.Header.Set("X-User-Id", "u-"+org)
	rq.Header.Set("X-User-IsAdmin", "true")
	resp, err := app.Test(rq)
	if err != nil {
		t.Fatalf("Test %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return httpResult{Code: resp.StatusCode, Location: resp.Header.Get("Location"), Body: b}
}

// twoAccounts is an App installed on two orgs, neither bound to any Hanzo org —
// the out-of-band shape.
func twoAccounts() []map[string]any {
	return []map[string]any{
		{"id": 111, "repository_selection": "all", "account": map[string]any{
			"login": "hanzoai", "type": "Organization", "html_url": "https://github.com/hanzoai"}},
		{"id": 222, "repository_selection": "selected", "account": map[string]any{
			"login": "luxfi", "type": "Organization", "html_url": "https://github.com/luxfi"}},
	}
}

func installations(t *testing.T, body []byte) []githubInstallationView {
	t.Helper()
	var out githubInstallationsOut
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("json: %v (%s)", err, body)
	}
	return out.Installations
}

// TestSuperAdminSeesUnboundInstallations is the regression: platform sudo reads the
// App's whole install list even when nothing has been connected. Before the fix the
// handler returned early on len(conns)==0 and answered [] for every caller, so an
// App installed out-of-band was invisible to the platform that owns it.
func TestSuperAdminSeesUnboundInstallations(t *testing.T) {
	withGithubApp(t, mockInstallations(t, twoAccounts()))
	app := newApp(t, newKMS(t))

	r := superReq(t, app, http.MethodGet, "/v1/integrations/github/installations", "admin")
	if r.Code != http.StatusOK {
		t.Fatalf("super admin installations want 200, got %d (%s)", r.Code, r.Body)
	}
	got := installations(t, r.Body)
	if len(got) != 2 {
		t.Fatalf("super admin should see both installs, got %d: %+v", len(got), got)
	}
	if got[0].Login != "hanzoai" || got[1].Login != "luxfi" {
		t.Fatalf("want [hanzoai luxfi], got %+v", got)
	}
	// Reach comes back with the account, so a reader knows what an import covers.
	if got[0].Grant != "all" || got[1].Grant != "selected" {
		t.Fatalf("want grants [all selected], got %q %q", got[0].Grant, got[1].Grant)
	}
	// Nothing is bound, so nothing claims to be connected.
	for _, v := range got {
		if v.Connected {
			t.Fatalf("unbound install must not report connected: %+v", v)
		}
	}
}

// TestSuperAdminConnectedReflectsBinding proves `connected` still answers about the
// CALLER's org, not about the install existing.
func TestSuperAdminConnectedReflectsBinding(t *testing.T) {
	withGithubApp(t, mockInstallations(t, twoAccounts()))
	app := newApp(t, newKMS(t))
	if err := mounted.State.store.Upsert(context.Background(), Connection{
		Org: "admin", Provider: "github", ExternalID: "111", AccountLabel: "hanzoai",
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	got := installations(t, superReq(t, app, http.MethodGet, "/v1/integrations/github/installations", "admin").Body)
	if len(got) != 2 {
		t.Fatalf("want 2 installs, got %d", len(got))
	}
	if !got[0].Connected {
		t.Fatalf("bound install should report connected: %+v", got[0])
	}
	if got[1].Connected {
		t.Fatalf("unbound install must not report connected: %+v", got[1])
	}
}

// TestTenantNeverSeesOtherAccounts is the isolation guard: a plain org reads only
// what it bound, never the App's inventory. This is the property the super-admin
// breadth must not have widened.
func TestTenantNeverSeesOtherAccounts(t *testing.T) {
	withGithubApp(t, mockInstallations(t, twoAccounts()))
	app := newApp(t, newKMS(t))
	if err := mounted.State.store.Upsert(context.Background(), Connection{
		Org: "acme", Provider: "github", ExternalID: "111", AccountLabel: "hanzoai",
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	// acme bound one account → it sees exactly that one, never luxfi.
	got := installations(t, req(t, app, http.MethodGet, "/v1/integrations/github/installations", "acme", nil).Body)
	if len(got) != 1 || got[0].Login != "hanzoai" {
		t.Fatalf("tenant should see only its bound account, got %+v", got)
	}

	// beta bound nothing → it sees nothing, though two installs exist.
	if got := installations(t, req(t, app, http.MethodGet, "/v1/integrations/github/installations", "beta", nil).Body); len(got) != 0 {
		t.Fatalf("unbound tenant must see no installs, got %+v", got)
	}

	// No principal → 403, same as every org-scoped op here.
	if r := req(t, app, http.MethodGet, "/v1/integrations/github/installations", "", nil); r.Code != http.StatusForbidden {
		t.Fatalf("no-principal installations want 403, got %d", r.Code)
	}
}

// TestSuperAdminInstallationsUpstreamFailure proves the platform view fails loudly.
// For a tenant the connection rows are the answer and the App call only annotates
// them, so an outage degrades; for a super admin the App call IS the answer, and
// answering [] would read as "the App is installed nowhere".
func TestSuperAdminInstallationsUpstreamFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	withGithubApp(t, srv)
	app := newApp(t, newKMS(t))

	if r := superReq(t, app, http.MethodGet, "/v1/integrations/github/installations", "admin"); r.Code != http.StatusBadGateway {
		t.Fatalf("super admin installations on upstream failure want 502, got %d (%s)", r.Code, r.Body)
	}
}
