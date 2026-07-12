package entitlements

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/types"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// ── fakes ──────────────────────────────────────────────────────────────────────

// fakeCommerce is an in-memory entitlement oracle. active[org] is the set of
// product ids the org's plan grants; anything else is not entitled. err forces a
// transport failure so the fail-closed path is exercised.
type fakeCommerce struct {
	active map[string]map[string]bool
	err    error
	calls  int
}

func newFakeCommerce() *fakeCommerce { return &fakeCommerce{active: map[string]map[string]bool{}} }

func (f *fakeCommerce) grant(org, product string) {
	if f.active[org] == nil {
		f.active[org] = map[string]bool{}
	}
	f.active[org][product] = true
}

func (f *fakeCommerce) GetOrgConfig(_ context.Context, orgID string) (*types.OrgConfig, error) {
	return &types.OrgConfig{OrgID: orgID}, nil
}

func (f *fakeCommerce) CheckEntitlement(_ context.Context, orgID, productID string) (*types.LicenseEntitlement, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return &types.LicenseEntitlement{
		ProductID: productID,
		Active:    f.active[orgID] != nil && f.active[orgID][productID],
		Plan:      "test",
	}, nil
}

// ── harness ──────────────────────────────────────────────────────────────────

func mount(t *testing.T, commerce cloud.CommerceClient) (*zip.App, *service) {
	t.Helper()
	store, err := openStore(t.TempDir() + "/entitlements.db")
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	s := &service{store: store, commerce: commerce, log: luxlog.New("test")}
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	app.Get("/v1/orgs/:org/entitlements", s.get)
	app.Post("/v1/orgs/:org/entitlements", s.post)
	return app, s
}

func send(t *testing.T, app *zip.App, req *http.Request) (int, []byte) {
	t.Helper()
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test %s %s: %v", req.Method, req.URL.Path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// orgMember builds a request from a VALIDATED, non-super-admin principal of org
// (X-User-Id set as SanitizeIdentity would from a verified token; X-Org-Id pinned
// to the owner claim). No X-User-IsAdmin ⇒ not a super admin.
func orgMember(method, path, org string, body any) *http.Request {
	req := jsonReq(method, path, body)
	req.Header.Set("X-Org-Id", org)
	req.Header.Set("X-User-Id", "u_"+org)
	return req
}

// superAdmin builds a request from a validated SuperAdmin (X-User-IsAdmin=true,
// minted only for owner==AdminOrg). X-Org-Id is the admin org; the :org in the
// path is the org being operated on.
func superAdmin(method, path string, body any) *http.Request {
	req := jsonReq(method, path, body)
	req.Header.Set("X-Org-Id", "admin")
	req.Header.Set("X-User-Id", "admin/z")
	req.Header.Set("X-User-IsAdmin", "true")
	return req
}

func jsonReq(method, path string, body any) *http.Request {
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, r)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req
}

func decodeEnabled(t *testing.T, body []byte) []string {
	t.Helper()
	var v entitlementsView
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("decode entitlementsView: %v (body=%s)", err, body)
	}
	return v.Enabled
}

// ── store: org isolation ────────────────────────────────────────────────────

func TestStoreIsolation(t *testing.T) {
	store, err := openStore(t.TempDir() + "/entitlements.db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = store.Close() }()
	ctx := context.Background()

	if err := store.Enable(ctx, "acme", "engine", "u_acme", 100); err != nil {
		t.Fatalf("enable acme/engine: %v", err)
	}
	if err := store.Enable(ctx, "acme", "chat", "u_acme", 100); err != nil {
		t.Fatalf("enable acme/chat: %v", err)
	}
	if err := store.Enable(ctx, "globex", "engine", "u_globex", 100); err != nil {
		t.Fatalf("enable globex/engine: %v", err)
	}

	acme, err := store.List(ctx, "acme")
	if err != nil {
		t.Fatalf("list acme: %v", err)
	}
	if len(acme) != 2 || acme[0] != "chat" || acme[1] != "engine" { // sorted
		t.Fatalf("acme enabled = %v, want [chat engine]", acme)
	}
	// globex must NOT see acme's chat.
	globex, err := store.List(ctx, "globex")
	if err != nil {
		t.Fatalf("list globex: %v", err)
	}
	if len(globex) != 1 || globex[0] != "engine" {
		t.Fatalf("globex enabled = %v, want [engine]", globex)
	}
	// Disable is scoped + idempotent.
	if err := store.Disable(ctx, "acme", "chat"); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if err := store.Disable(ctx, "acme", "chat"); err != nil {
		t.Fatalf("disable idempotent: %v", err)
	}
	acme, _ = store.List(ctx, "acme")
	if len(acme) != 1 || acme[0] != "engine" {
		t.Fatalf("acme after disable = %v, want [engine]", acme)
	}
	// globex untouched by acme's disable.
	if g, _ := store.List(ctx, "globex"); len(g) != 1 {
		t.Fatalf("globex mutated by acme disable: %v", g)
	}
}

func TestStoreApplyTransaction(t *testing.T) {
	store, err := openStore(t.TempDir() + "/entitlements.db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = store.Close() }()
	ctx := context.Background()

	_ = store.Enable(ctx, "acme", "engine", "u", 1)
	// add chat+iam, remove engine, and a product in BOTH add+remove (remove wins).
	got, err := store.Apply(ctx, "acme", []string{"chat", "iam", "engine"}, []string{"engine"}, "u", 2)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(got) != 2 || got[0] != "chat" || got[1] != "iam" {
		t.Fatalf("apply result = %v, want [chat iam] (engine removed even though also in add)", got)
	}
}

// ── HTTP: principal gate fails closed ──────────────────────────────────────────

func TestForgedRequestRejected(t *testing.T) {
	app, _ := mount(t, newFakeCommerce())
	for _, tc := range []struct{ method, path string }{
		{"GET", "/v1/orgs/victim/entitlements"},
		{"POST", "/v1/orgs/victim/entitlements"},
	} {
		req := jsonReq(tc.method, tc.path, map[string]any{"add": []string{"engine"}})
		req.Header.Set("X-Org-Id", "victim") // forged: no X-User-Id ⇒ not validated
		code, _ := send(t, app, req)
		if code != http.StatusForbidden {
			t.Fatalf("%s %s: want 403 for forged (no X-User-Id), got %d", tc.method, tc.path, code)
		}
	}
}

func TestCrossOrgWriteRejected(t *testing.T) {
	app, _ := mount(t, newFakeCommerce())
	// A validated member of "acme" tries to write "globex" — must 403.
	req := orgMember("POST", "/v1/orgs/globex/entitlements", "acme", map[string]any{"add": []string{"engine"}})
	code, body := send(t, app, req)
	if code != http.StatusForbidden {
		t.Fatalf("cross-org write: want 403, got %d (body=%s)", code, body)
	}
}

func TestMalformedOrgAndProduct(t *testing.T) {
	app, _ := mount(t, newFakeCommerce())
	// Bad org label.
	code, _ := send(t, app, orgMember("GET", "/v1/orgs/Bad_Org!/entitlements", "Bad_Org!", nil))
	if code != http.StatusBadRequest {
		t.Fatalf("bad org: want 400, got %d", code)
	}
	// Bad product id in body (org member, valid org).
	fc := newFakeCommerce()
	app2, _ := mount(t, fc)
	code, _ = send(t, app2, orgMember("POST", "/v1/orgs/acme/entitlements", "acme", map[string]any{"add": []string{"NOT VALID"}}))
	if code != http.StatusBadRequest {
		t.Fatalf("bad product: want 400, got %d", code)
	}
	if fc.calls != 0 {
		t.Fatalf("commerce must not be consulted for a malformed product; calls=%d", fc.calls)
	}
}

// ── HTTP: the entitlement gate ─────────────────────────────────────────────────

func TestOrgMemberCanEnableEntitledProduct(t *testing.T) {
	fc := newFakeCommerce()
	fc.grant("acme", "engine")
	app, _ := mount(t, fc)

	code, body := send(t, app, orgMember("POST", "/v1/orgs/acme/entitlements", "acme", map[string]any{"add": []string{"engine"}}))
	if code != http.StatusOK {
		t.Fatalf("enable entitled: want 200, got %d (body=%s)", code, body)
	}
	if got := decodeEnabled(t, body); len(got) != 1 || got[0] != "engine" {
		t.Fatalf("enabled = %v, want [engine]", got)
	}
	// GET reflects it.
	code, body = send(t, app, orgMember("GET", "/v1/orgs/acme/entitlements", "acme", nil))
	if code != http.StatusOK || len(decodeEnabled(t, body)) != 1 {
		t.Fatalf("get after enable: %d %s", code, body)
	}
}

func TestOrgMemberCannotEnableUnentitledProduct(t *testing.T) {
	fc := newFakeCommerce() // grants nothing
	app, _ := mount(t, fc)

	code, body := send(t, app, orgMember("POST", "/v1/orgs/acme/entitlements", "acme", map[string]any{"add": []string{"engine"}}))
	if code != http.StatusPaymentRequired {
		t.Fatalf("enable unentitled: want 402, got %d (body=%s)", code, body)
	}
	// Nothing persisted.
	_, body = send(t, app, orgMember("GET", "/v1/orgs/acme/entitlements", "acme", nil))
	if len(decodeEnabled(t, body)) != 0 {
		t.Fatalf("unentitled product must not have been enabled: %s", body)
	}
}

func TestSuperAdminBypassesEntitlementGate(t *testing.T) {
	fc := newFakeCommerce() // grants nothing — a super admin still succeeds
	app, _ := mount(t, fc)

	// Super admin comps "engine" to acme even though acme's plan doesn't grant it.
	code, body := send(t, app, superAdmin("POST", "/v1/orgs/acme/entitlements", map[string]any{"add": []string{"engine"}}))
	if code != http.StatusOK {
		t.Fatalf("super-admin grant: want 200, got %d (body=%s)", code, body)
	}
	if got := decodeEnabled(t, body); len(got) != 1 || got[0] != "engine" {
		t.Fatalf("enabled = %v, want [engine]", got)
	}
	if fc.calls != 0 {
		t.Fatalf("super admin must bypass commerce; calls=%d", fc.calls)
	}
}

func TestCommerceUnavailableFailsClosedForMemberAdd(t *testing.T) {
	// commerce nil ⇒ a non-super-admin add cannot be verified ⇒ 503, never open.
	app, _ := mount(t, nil)
	code, _ := send(t, app, orgMember("POST", "/v1/orgs/acme/entitlements", "acme", map[string]any{"add": []string{"engine"}}))
	if code != http.StatusServiceUnavailable {
		t.Fatalf("member add with nil commerce: want 503, got %d", code)
	}
}

func TestRemoveNeverGated(t *testing.T) {
	// Disabling requires NO entitlement check — even with commerce nil, an org
	// member may turn a product off.
	store, err := openStore(t.TempDir() + "/entitlements.db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	_ = store.Enable(context.Background(), "acme", "engine", "u", 1)
	s := &service{store: store, commerce: nil, log: luxlog.New("test")}
	t.Cleanup(func() { _ = store.Close() })
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	app.Post("/v1/orgs/:org/entitlements", s.post)

	code, body := send(t, app, orgMember("POST", "/v1/orgs/acme/entitlements", "acme", map[string]any{"remove": []string{"engine"}}))
	if code != http.StatusOK {
		t.Fatalf("remove with nil commerce: want 200, got %d (body=%s)", code, body)
	}
	if got := decodeEnabled(t, body); len(got) != 0 {
		t.Fatalf("after remove enabled = %v, want []", got)
	}
}

func TestEmptyMutationRejected(t *testing.T) {
	app, _ := mount(t, newFakeCommerce())
	code, _ := send(t, app, orgMember("POST", "/v1/orgs/acme/entitlements", "acme", map[string]any{}))
	if code != http.StatusBadRequest {
		t.Fatalf("empty add+remove: want 400, got %d", code)
	}
}
