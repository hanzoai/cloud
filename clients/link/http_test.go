package link

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/agents"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

func mountLink(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	if err := Mount(app, cloud.Deps{Logger: luxlog.New("test"), DataDir: t.TempDir()}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	t.Cleanup(func() { _ = Shutdown(context.Background()) })
	return app
}

// req drives a request with a principal: X-Org-Id (the tenant) + X-User-Id (the
// validated subject, set only from a verified credential by SanitizeIdentity in
// prod — injected here as the gateway would). An empty user is the anonymous forge
// (org header, no validated principal) that every route must refuse.
func req(t *testing.T, app *zip.App, method, path, org, user string, body any) (int, []byte) {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	rq := httptest.NewRequest(method, path, r)
	if body != nil {
		rq.Header.Set("Content-Type", "application/json")
	}
	if org != "" {
		rq.Header.Set("X-Org-Id", org)
	}
	if user != "" {
		rq.Header.Set("X-User-Id", user)
	}
	resp, err := app.Fiber().Test(rq)
	if err != nil {
		t.Fatalf("Test %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// TestFailClosedNoPrincipal: an org header with NO validated user (the off-gateway
// forge) is refused on every route — read AND write.
func TestFailClosedNoPrincipal(t *testing.T) {
	app := mountLink(t)
	for _, tc := range []struct {
		method, path string
		body         any
	}{
		{http.MethodGet, "/v1/links", nil},
		{http.MethodGet, "/v1/links/route", nil},
		{http.MethodGet, "/v1/links/devices/m1", nil},
		{http.MethodPost, "/v1/links", map[string]any{"machine": "m1", "provider": "claude"}},
		{http.MethodDelete, "/v1/links/link_x", nil},
	} {
		if code, _ := req(t, app, tc.method, tc.path, "acme", "", tc.body); code != http.StatusForbidden {
			t.Fatalf("%s %s with no validated principal want 403, got %d", tc.method, tc.path, code)
		}
	}
}

// TestRegisterListGetRevoke drives the full happy path AND asserts the
// subscription-vs-api-key billing distinction over the wire.
func TestRegisterListGetRevoke(t *testing.T) {
	app := mountLink(t)

	// Register a subscription account (Claude Max) with a usage snapshot.
	code, body := req(t, app, http.MethodPost, "/v1/links", "acme", "alice", map[string]any{
		"machine": "m1", "host": "box", "os": "darwin",
		"provider": "claude", "account": "alice@x", "plan": "Claude Max", "kind": "subscription",
		"usage": map[string]any{"sessionPct": 42, "weeklyPct": 12, "tokens": 1000},
	})
	if code != http.StatusCreated {
		t.Fatalf("register want 201, got %d (%s)", code, body)
	}
	var sub linkView
	_ = json.Unmarshal(body, &sub)
	if sub.ID == "" || sub.Provider != "claude" || sub.Kind != "subscription" {
		t.Fatalf("register shape: %+v", sub)
	}
	// A subscription bills the PLAN — never commerce.
	if sub.Billing != BillingPlan {
		t.Fatalf("a subscription account must carry billing=plan, got %q", sub.Billing)
	}

	// Register an api-key account (bills via commerce).
	code, body = req(t, app, http.MethodPost, "/v1/links", "acme", "alice", map[string]any{
		"machine": "m1", "host": "box", "provider": "hanzo", "account": "hk-1", "kind": "apikey",
	})
	if code != http.StatusCreated {
		t.Fatalf("register apikey want 201, got %d (%s)", code, body)
	}
	var key linkView
	_ = json.Unmarshal(body, &key)
	if key.Billing != BillingCommerce {
		t.Fatalf("an api-key account must carry billing=commerce, got %q", key.Billing)
	}

	// List returns both + a device projection grouping them under m1.
	code, body = req(t, app, http.MethodGet, "/v1/links", "acme", "alice", nil)
	var list struct {
		Links   []linkView   `json:"links"`
		Devices []deviceView `json:"devices"`
	}
	_ = json.Unmarshal(body, &list)
	if code != http.StatusOK || len(list.Links) != 2 {
		t.Fatalf("list want 2 links, got %d (%s)", len(list.Links), body)
	}
	if len(list.Devices) != 1 || list.Devices[0].Machine != "m1" || len(list.Devices[0].Accounts) != 2 {
		t.Fatalf("device projection want m1 with 2 accounts, got %+v", list.Devices)
	}

	// The route plan puts the subscription first and carries the billing mode.
	code, body = req(t, app, http.MethodGet, "/v1/links/route", "acme", "alice", nil)
	var plan RoutePlan
	_ = json.Unmarshal(body, &plan)
	if code != http.StatusOK || len(plan.Candidates) != 2 {
		t.Fatalf("route want 2 candidates, got %d (%s)", len(plan.Candidates), body)
	}
	if plan.Candidates[0].Kind != KindSubscription || plan.Candidates[0].Billing != BillingPlan {
		t.Fatalf("route must prefer the subscription (plan billing), got %+v", plan.Candidates[0])
	}

	// Revoke the subscription → it drops out of the route; the api key remains.
	code, body = req(t, app, http.MethodDelete, "/v1/links/"+sub.ID, "acme", "alice", nil)
	var rev revokeResp
	_ = json.Unmarshal(body, &rev)
	if code != http.StatusOK || rev.Revoked != 1 {
		t.Fatalf("revoke want 200 revoked=1, got %d %+v", code, rev)
	}
	code, body = req(t, app, http.MethodGet, "/v1/links/route", "acme", "alice", nil)
	_ = json.Unmarshal(body, &plan)
	if len(plan.Candidates) != 1 || plan.Candidates[0].Kind != KindAPIKey {
		t.Fatalf("after revoke only the api key routes, got %+v", plan.Candidates)
	}
	// The revoked link is still listed (retained) but marked revoked.
	code, body = req(t, app, http.MethodGet, "/v1/links/"+sub.ID, "acme", "alice", nil)
	var got linkView
	_ = json.Unmarshal(body, &got)
	if code != http.StatusOK || got.Status != StatusRevoked {
		t.Fatalf("revoked link must remain gettable as revoked, got %d %q", code, got.Status)
	}
}

// TestHTTPUserAndOrgIsolation: a second user in the same org, AND a second org,
// see/get/revoke none of the first user's links.
func TestHTTPUserAndOrgIsolation(t *testing.T) {
	app := mountLink(t)
	code, body := req(t, app, http.MethodPost, "/v1/links", "acme", "alice", map[string]any{
		"machine": "m1", "provider": "claude", "account": "a", "kind": "subscription",
	})
	if code != http.StatusCreated {
		t.Fatalf("alice register want 201, got %d", code)
	}
	var al linkView
	_ = json.Unmarshal(body, &al)

	// bob (same org) sees nothing and cannot get/revoke alice's link by id.
	_, body = req(t, app, http.MethodGet, "/v1/links", "acme", "bob", nil)
	var bobList struct {
		Links []linkView `json:"links"`
	}
	_ = json.Unmarshal(body, &bobList)
	if len(bobList.Links) != 0 {
		t.Fatalf("bob must see zero links, got %d", len(bobList.Links))
	}
	if code, _ := req(t, app, http.MethodGet, "/v1/links/"+al.ID, "acme", "bob", nil); code != http.StatusNotFound {
		t.Fatalf("bob GET alice's link want 404, got %d", code)
	}
	if code, _ := req(t, app, http.MethodDelete, "/v1/links/"+al.ID, "acme", "bob", nil); code != http.StatusNotFound {
		t.Fatalf("bob DELETE alice's link want 404, got %d", code)
	}

	// evil org sees nothing either.
	_, body = req(t, app, http.MethodGet, "/v1/links", "evil", "alice", nil)
	var evilList struct {
		Links []linkView `json:"links"`
	}
	_ = json.Unmarshal(body, &evilList)
	if len(evilList.Links) != 0 {
		t.Fatalf("evil org must see zero of acme's links, got %d", len(evilList.Links))
	}

	// alice's link is intact after the foreign attempts.
	if code, _ := req(t, app, http.MethodGet, "/v1/links/"+al.ID, "acme", "alice", nil); code != http.StatusOK {
		t.Fatalf("alice's own link must survive, got %d", code)
	}
}

// TestHTTPInputValidation: a register missing a required field, a bad kind, or an
// oversized usage is a clean 400.
func TestHTTPInputValidation(t *testing.T) {
	app := mountLink(t)
	bad := []map[string]any{
		{"provider": "claude"}, // no machine
		{"machine": "m1"},      // no provider
		{"machine": "m1", "provider": "claude", "kind": "freeloader"}, // bad kind
	}
	for _, b := range bad {
		if code, _ := req(t, app, http.MethodPost, "/v1/links", "acme", "alice", b); code != http.StatusBadRequest {
			t.Fatalf("invalid register %+v want 400, got %d", b, code)
		}
	}
	// A device with no accounts is a 404.
	if code, _ := req(t, app, http.MethodGet, "/v1/links/devices/nope", "acme", "alice", nil); code != http.StatusNotFound {
		t.Fatalf("unknown device want 404, got %d", code)
	}
}

// TestRevokeStopsSessions is the end-to-end proof of the login-out contract: with
// BOTH the agents session plane and the link registry mounted, revoking a link
// stops the live sessions that ran under that account — via the REAL adapter, not
// a fake. It also proves the session↔account tag (a session carries its
// provider/account) and org isolation of the stop (another org's identical session
// is untouched).
func TestRevokeStopsSessions(t *testing.T) {
	dir := t.TempDir()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	deps := cloud.Deps{Logger: luxlog.New("test"), DataDir: dir}
	if err := agents.Mount(app, deps); err != nil {
		t.Fatalf("agents.Mount: %v", err)
	}
	t.Cleanup(func() { _ = agents.Shutdown(context.Background()) })
	if err := Mount(app, deps); err != nil {
		t.Fatalf("link.Mount: %v", err)
	}
	t.Cleanup(func() { _ = Shutdown(context.Background()) })

	// A live session tagged with the account it runs under, on host "box1".
	code, body := req(t, app, http.MethodPost, "/v1/agents/sessions", "acme", "alice", map[string]any{
		"agent": "dev", "host": "box1", "provider": "claude", "account": "alice@x",
	})
	if code != http.StatusCreated {
		t.Fatalf("register session want 201, got %d (%s)", code, body)
	}
	var sess struct {
		ID       string `json:"id"`
		Status   string `json:"status"`
		Provider string `json:"provider"`
		Account  string `json:"account"`
	}
	_ = json.Unmarshal(body, &sess)
	if sess.Provider != "claude" || sess.Account != "alice@x" {
		t.Fatalf("session must carry its account tag, got %+v", sess)
	}

	// The SAME account in ANOTHER org — must be untouched by acme's revoke.
	code, body = req(t, app, http.MethodPost, "/v1/agents/sessions", "evil", "mallory", map[string]any{
		"agent": "dev", "host": "box1", "provider": "claude", "account": "alice@x",
	})
	if code != http.StatusCreated {
		t.Fatalf("evil session want 201, got %d", code)
	}
	var evil struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(body, &evil)

	// Register the link for that account, then revoke it (log out).
	code, body = req(t, app, http.MethodPost, "/v1/links", "acme", "alice", map[string]any{
		"machine": "m1", "host": "box1", "provider": "claude", "account": "alice@x", "kind": "subscription",
	})
	if code != http.StatusCreated {
		t.Fatalf("register link want 201, got %d", code)
	}
	var l linkView
	_ = json.Unmarshal(body, &l)

	code, body = req(t, app, http.MethodDelete, "/v1/links/"+l.ID, "acme", "alice", nil)
	var rev revokeResp
	_ = json.Unmarshal(body, &rev)
	if code != http.StatusOK {
		t.Fatalf("revoke want 200, got %d (%s)", code, body)
	}
	if rev.SessionsStopped != 1 {
		t.Fatalf("revoke must stop the 1 session under that account, got %d", rev.SessionsStopped)
	}

	// The acme session is now terminal (stopped).
	_, body = req(t, app, http.MethodGet, "/v1/agents/sessions/"+sess.ID, "acme", "alice", nil)
	var after struct {
		Status string `json:"status"`
	}
	_ = json.Unmarshal(body, &after)
	if after.Status != "error" {
		t.Fatalf("the revoked account's session must be stopped (terminal), got %q", after.Status)
	}

	// The evil org's identical session is UNTOUCHED (org-scoped stop).
	_, body = req(t, app, http.MethodGet, "/v1/agents/sessions/"+evil.ID, "evil", "mallory", nil)
	var evilAfter struct {
		Status string `json:"status"`
	}
	_ = json.Unmarshal(body, &evilAfter)
	if evilAfter.Status != "running" {
		t.Fatalf("another org's session must survive acme's revoke, got %q", evilAfter.Status)
	}
}
