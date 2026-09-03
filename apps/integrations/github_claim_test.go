package integrations

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/zap-proto/zip"
)

// github_claim_test.go proves the binding verb: who may bind an installation the
// App already holds, to WHICH org, and that running it twice changes nothing.
//
// The property that must never be wrong is isolation. The store's key is
// (org,provider,owner), so a binding to the wrong org is a perfectly valid row —
// nothing downstream can catch it, and the row points a repository mirror at the
// wrong tenant. The gate is therefore the only thing standing between a tenant
// and every other tenant's repositories.

// postJSON sends a typed op an In body, optionally as platform sudo.
func postJSON(t *testing.T, app *zip.App, path, org string, super bool, body any) httpResult {
	t.Helper()
	b, _ := json.Marshal(body)
	rq := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b))
	rq.Header.Set("Content-Type", "application/json")
	if org != "" {
		rq.Header.Set("X-Org-Id", org)
		rq.Header.Set("X-User-Id", "u-"+org)
	}
	if super {
		rq.Header.Set("X-User-IsAdmin", "true")
	}
	resp, err := app.Test(rq)
	if err != nil {
		t.Fatalf("Test POST %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	return httpResult{Code: resp.StatusCode, Body: raw}
}

func claimOut(t *testing.T, body []byte) githubClaimOut {
	t.Helper()
	var out githubClaimOut
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("json: %v (%s)", err, body)
	}
	return out
}

// TestTenantCannotClaim is the isolation guard. A plain org — even an admin of its
// OWN org — may not bind an account the App holds; its route to a grant is GitHub's
// consent screen. Nothing may be written by the refused call.
func TestTenantCannotClaim(t *testing.T) {
	withGithubApp(t, mockInstallations(t, twoAccounts()))
	app := newApp(t, newKMS(t))

	for _, name := range []string{"plain member", "own-org admin"} {
		rq := httptest.NewRequest(http.MethodPost, "/v1/integration/github/claim",
			bytes.NewReader([]byte(`{"all":true}`)))
		rq.Header.Set("Content-Type", "application/json")
		rq.Header.Set("X-Org-Id", "acme")
		rq.Header.Set("X-User-Id", "u-acme")
		if name == "own-org admin" {
			// The own-org admin bit must NOT be mistaken for platform sudo.
			rq.Header.Set("X-User-IsOrgAdmin", "true")
		}
		resp, err := app.Test(rq)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("%s claiming want 403, got %d", name, resp.StatusCode)
		}
	}

	// Refused means NOTHING was written: acme holds no connection, so it still
	// cannot reach a single repository.
	if conns := Connections("acme", "github"); len(conns) != 0 {
		t.Fatalf("refused claim must write no rows, got %+v", conns)
	}
	if r := req(t, app, http.MethodGet, "/v1/integration/github/repos", "acme", nil); r.Code != http.StatusConflict {
		t.Fatalf("acme should still be unconnected (409), got %d (%s)", r.Code, r.Body)
	}
}

// TestClaimBindsToCallerOrgOnly proves the target org comes from the validated
// principal, so one caller's claim can never land in another org's rows.
func TestClaimBindsToCallerOrgOnly(t *testing.T) {
	withGithubApp(t, mockInstallations(t, twoAccounts()))
	app := newApp(t, newKMS(t))

	r := postJSON(t, app, "/v1/integration/github/claim", "hanzo", true, map[string]any{"all": true})
	if r.Code != http.StatusOK {
		t.Fatalf("claim want 200, got %d (%s)", r.Code, r.Body)
	}
	if got := claimOut(t, r.Body); len(got.Claimed) != 2 {
		t.Fatalf("want both accounts claimed, got %+v", got)
	}
	if conns := Connections("hanzo", "github"); len(conns) != 2 {
		t.Fatalf("hanzo should hold 2 connections, got %d", len(conns))
	}
	// The org that did not ask holds nothing.
	if conns := Connections("acme", "github"); len(conns) != 0 {
		t.Fatalf("a claim must not touch another org, acme holds %+v", conns)
	}
}

// TestClaimIsIdempotent proves a second run changes nothing: the same bindings,
// reported under `already`, with connected_at preserved.
func TestClaimIsIdempotent(t *testing.T) {
	withGithubApp(t, mockInstallations(t, twoAccounts()))
	app := newApp(t, newKMS(t))

	first := claimOut(t, postJSON(t, app, "/v1/integration/github/claim", "hanzo", true,
		map[string]any{"all": true}).Body)
	if len(first.Claimed) != 2 || len(first.Already) != 0 {
		t.Fatalf("first claim should bind both, got %+v", first)
	}
	before, _, err := mounted.State.store.Get(context.Background(), "hanzo", "", "github", "hanzoai")
	if err != nil {
		t.Fatalf("get: %v", err)
	}

	second := claimOut(t, postJSON(t, app, "/v1/integration/github/claim", "hanzo", true,
		map[string]any{"all": true}).Body)
	if len(second.Claimed) != 0 || len(second.Already) != 2 {
		t.Fatalf("second claim should bind nothing, got %+v", second)
	}
	if conns := Connections("hanzo", "github"); len(conns) != 2 {
		t.Fatalf("re-claiming must not duplicate rows, got %d", len(conns))
	}
	after, _, err := mounted.State.store.Get(context.Background(), "hanzo", "", "github", "hanzoai")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if after.ConnectedAt != before.ConnectedAt {
		t.Fatalf("connected since must survive a re-claim: %d -> %d", before.ConnectedAt, after.ConnectedAt)
	}
	if after.ExternalID != "111" {
		t.Fatalf("installation id should still be 111, got %q", after.ExternalID)
	}
}

// TestClaimNamedAccounts proves a named subset binds only what was named, and that
// a name the App does not hold refuses the WHOLE call rather than half-applying it.
func TestClaimNamedAccounts(t *testing.T) {
	withGithubApp(t, mockInstallations(t, twoAccounts()))
	app := newApp(t, newKMS(t))

	// Case-insensitive, since GitHub logins are.
	got := claimOut(t, postJSON(t, app, "/v1/integration/github/claim", "lux", true,
		map[string]any{"accounts": []string{"LuxFi"}}).Body)
	if len(got.Claimed) != 1 || got.Claimed[0] != "luxfi" {
		t.Fatalf("want [luxfi] claimed, got %+v", got)
	}
	if conns := Connections("lux", "github"); len(conns) != 1 {
		t.Fatalf("only the named account binds, got %d", len(conns))
	}

	// One unknown name refuses everything — no partial write.
	r := postJSON(t, app, "/v1/integration/github/claim", "lux", true,
		map[string]any{"accounts": []string{"hanzoai", "nope"}})
	if r.Code != http.StatusBadRequest {
		t.Fatalf("unknown account want 400, got %d (%s)", r.Code, r.Body)
	}
	if conns := Connections("lux", "github"); len(conns) != 1 {
		t.Fatalf("a refused claim must write nothing, got %d rows", len(conns))
	}

	// Naming nothing at all is a 400, not a silent success.
	if r := postJSON(t, app, "/v1/integration/github/claim", "lux", true,
		map[string]any{}); r.Code != http.StatusBadRequest {
		t.Fatalf("empty claim want 400, got %d (%s)", r.Code, r.Body)
	}
}

// TestClaimRefreshesReinstalledAccount proves a stale binding self-heals: an
// account removed and reinstalled on GitHub keeps its login but gets a new
// installation id, and a row still holding the dead id mints nothing.
func TestClaimRefreshesReinstalledAccount(t *testing.T) {
	withGithubApp(t, mockInstallations(t, twoAccounts()))
	app := newApp(t, newKMS(t))
	if err := mounted.State.store.Upsert(context.Background(), Connection{
		Org: "hanzo", Provider: "github", Label: "hanzoai",
		ExternalID: "999", AccountLabel: "hanzoai", // the dead installation
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	got := claimOut(t, postJSON(t, app, "/v1/integration/github/claim", "hanzo", true,
		map[string]any{"accounts": []string{"hanzoai"}}).Body)
	if len(got.Already) != 1 {
		t.Fatalf("an existing binding reports already, got %+v", got)
	}
	after, _, err := mounted.State.store.Get(context.Background(), "hanzo", "", "github", "hanzoai")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if after.ExternalID != "111" {
		t.Fatalf("claim should refresh the installation id to 111, got %q", after.ExternalID)
	}
}

// TestClaimThenInstallationsReadConnected closes the loop: after claiming, the
// list the operator reads reports the accounts as connected.
func TestClaimThenInstallationsReadConnected(t *testing.T) {
	withGithubApp(t, mockInstallations(t, twoAccounts()))
	app := newApp(t, newKMS(t))

	postJSON(t, app, "/v1/integration/github/claim", "hanzo", true, map[string]any{"all": true})

	got := installations(t, superReq(t, app, http.MethodGet, "/v1/integration/github/installations", "hanzo").Body)
	if len(got) != 2 {
		t.Fatalf("want 2 installations, got %d", len(got))
	}
	for _, v := range got {
		if !v.Connected {
			t.Fatalf("claimed account should read connected: %+v", v)
		}
	}
}
