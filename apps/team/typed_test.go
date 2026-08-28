package team

// The wire facts a TYPED op must keep, and that no other test in this package
// pins. Typing a route is a DESCRIPTION task: the same status, the same body,
// the same headers. Everything a typed handler CANNOT state in its return value
// — a response header, an empty 204 body, a second path serving one op — is a
// fact that could be dropped silently, so each is asserted here.

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hanzoai/cloud/apps/team/token"
)

// TestTypedPlanIsUncacheable: per-tenant plan/seat data must never be cached by
// an intermediary. The header is set through cloud.Request, which is the ONE
// thing a typed op cannot express in its Out — so it is the one most likely to
// be lost, and it is a cross-tenant leak if it is.
func TestTypedPlanIsUncacheable(t *testing.T) {
	app, store := billingApp(t, nil, nil)
	if _, err := store.EnsureWorkspace(context.Background(), gateOrg, gateAcct, "Ada"); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/v1/team/billing/plan", nil)
	for k, v := range bearerFor(t, gateAcct, gateOrg) {
		req.Header.Set(k, v)
	}
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("plan = %d, want 200", resp.StatusCode)
	}
	if got := resp.Header.Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want %q", got, "no-store")
	}
}

// TestTypedDeleteBlobIsEmpty204: the front only checks response.ok, and the
// document now DECLARES 204 — so the route must answer 204 with no body at all,
// not 200 with a JSON object a typed Out would otherwise render.
func TestTypedDeleteBlobIsEmpty204(t *testing.T) {
	app := mountTeam(t)
	ctx := context.Background()
	ws, err := mounted.State.accounts.EnsureWorkspace(ctx, gateOrg, gateAcct, "Ada")
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodDelete,
		"/v1/team/files/"+ws.UUID+"/0d4f0000-0000-4000-8000-000000000001?file=0d4f0000-0000-4000-8000-000000000001", nil)
	for k, v := range bearerFor(t, gateAcct, gateOrg) {
		req.Header.Set(k, v)
	}
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete = %d, want 204", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if len(body) != 0 {
		t.Fatalf("204 body = %q, want empty", body)
	}
}

// TestTypedStatisticsServesBothPaths: the canonical path and the /api/v1/ alias
// the current transactor front still calls are TWO registry entries over ONE
// op, and both must answer. Only the alias was covered before.
func TestTypedStatisticsServesBothPaths(t *testing.T) {
	app := mountTeam(t)
	const acct, ws = "550e8400-e29b-41d4-a716-446655440000", "6ba7b810-9dad-11d1-80b4-00c04fd430c8"
	tok, err := token.Generate(acct, ws, map[string]any{"org": gateOrg}, expUnix(workspaceTokenTTL), testSecret)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"/v1/team/transactor/statistics"} {
		code, body := call(t, app, http.MethodGet, p+"?token="+tok, nil, nil)
		if code != http.StatusOK {
			t.Fatalf("GET %s = %d, want 200 (%s)", p, code, body)
		}
		// The front reads these three keys; an absent one renders an empty panel.
		if want := `{"metrics":{},"statistics":{"activeSessions":{"` + ws + `":[]}},"admin":false}`; string(body) != want {
			t.Fatalf("GET %s body = %s, want %s", p, body, want)
		}
	}
}
