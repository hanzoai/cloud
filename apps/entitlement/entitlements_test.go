package entitlement

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/sdk"

	// devmaster keys this test binary: cek opens nothing without a master and a
	// test process has no KMS.
	"github.com/hanzoai/cloud/internal/sqltest"
	_ "github.com/hanzoai/cloud/sdk/kit/master"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// openTestStore opens a throwaway entitlements store for one test — the ONE way
// this package's tests reach the store.
func openTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := openStore(t.TempDir())
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// ── fakes ──────────────────────────────────────────────────────────────────────

// fakeCommerce is an in-memory entitlement oracle. active[org] is the set of
// product ids the org's plan grants; anything else is not entitled. early[org] is
// whether the org's plan carries early access. err forces a transport failure so
// the fail-closed path is exercised.
type fakeCommerce struct {
	active map[string]map[string]bool
	early  map[string]bool
	err    error
	calls  int
}

func newFakeCommerce() *fakeCommerce {
	return &fakeCommerce{active: map[string]map[string]bool{}, early: map[string]bool{}}
}

func (f *fakeCommerce) grant(org, product string) {
	if f.active[org] == nil {
		f.active[org] = map[string]bool{}
	}
	f.active[org][product] = true
}

func (f *fakeCommerce) CheckEntitlement(_ context.Context, orgID, productID string) (*cloud.LicenseEntitlement, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return &cloud.LicenseEntitlement{
		ProductID:   productID,
		Active:      f.active[orgID] != nil && f.active[orgID][productID],
		Plan:        "test",
		EarlyAccess: f.early[orgID],
	}, nil
}

// ── harness ──────────────────────────────────────────────────────────────────

// compose installs what a HOST installs. A subsystem never installs cloud.Bridge
// (routes() says why): the program's composer installs it once at the root, after
// the identity check that mints the validated org and before any subsystem
// registers a route. In production that composer is serve.go. In a test the test
// IS the composer, so it owes the same thing.
func compose(app *zip.App) { app.Use(cloud.Bridge()) }

func mount(t *testing.T, commerce cloud.CommerceClient) (*zip.App, *service) {
	t.Helper()
	s := &service{store: openTestStore(t), commerce: commerce, log: luxlog.New("test")}
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	compose(app)
	// The REAL registration, not a reconstruction of it: a test that rebuilt the
	// router by hand could exercise a surface this binary does not serve.
	routes(app, s)
	return app, s
}

func send(t *testing.T, app *zip.App, req *http.Request) (int, []byte) {
	t.Helper()
	resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
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
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		store := openTestStore(t)
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
	})
}

func TestStoreApplyTransaction(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		store := openTestStore(t)
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
	})
}

// ── HTTP: principal gate fails closed ──────────────────────────────────────────

func TestForgedRequestRejected(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, _ := mount(t, newFakeCommerce())
		for _, tc := range []struct{ method, path string }{
			{"GET", "/v1/entitlement/orgs/victim"},
			{"POST", "/v1/entitlement/orgs/victim"},
		} {
			req := jsonReq(tc.method, tc.path, map[string]any{"add": []string{"engine"}})
			req.Header.Set("X-Org-Id", "victim") // forged: no X-User-Id ⇒ not validated
			code, _ := send(t, app, req)
			if code != http.StatusUnauthorized {
				t.Fatalf("%s %s: want 401 for forged (no X-User-Id), got %d", tc.method, tc.path, code)
			}
		}
	})
}

func TestCrossOrgWriteRejected(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, _ := mount(t, newFakeCommerce())
		// A validated member of "acme" tries to write "globex" — must 403.
		req := orgMember("POST", "/v1/entitlement/orgs/globex", "acme", map[string]any{"add": []string{"engine"}})
		code, body := send(t, app, req)
		if code != http.StatusForbidden {
			t.Fatalf("cross-org write: want 403, got %d (body=%s)", code, body)
		}
	})
}

func TestMalformedOrgAndProduct(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, _ := mount(t, newFakeCommerce())
		// Bad org label.
		code, _ := send(t, app, orgMember("GET", "/v1/entitlement/orgs/Bad_Org!", "Bad_Org!", nil))
		if code != http.StatusBadRequest {
			t.Fatalf("bad org: want 400, got %d", code)
		}
		// Bad product id in body (org member, valid org).
		fc := newFakeCommerce()
		app2, _ := mount(t, fc)
		code, _ = send(t, app2, orgMember("POST", "/v1/entitlement/orgs/acme", "acme", map[string]any{"add": []string{"NOT VALID"}}))
		if code != http.StatusBadRequest {
			t.Fatalf("bad product: want 400, got %d", code)
		}
		if fc.calls != 0 {
			t.Fatalf("commerce must not be consulted for a malformed product; calls=%d", fc.calls)
		}
	})
}

// ── HTTP: the entitlement gate ─────────────────────────────────────────────────

func TestOrgMemberCanEnableEntitledProduct(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		fc := newFakeCommerce()
		fc.grant("acme", "engine")
		app, _ := mount(t, fc)

		code, body := send(t, app, orgMember("POST", "/v1/entitlement/orgs/acme", "acme", map[string]any{"add": []string{"engine"}}))
		if code != http.StatusOK {
			t.Fatalf("enable entitled: want 200, got %d (body=%s)", code, body)
		}
		if got := decodeEnabled(t, body); len(got) != 1 || got[0] != "engine" {
			t.Fatalf("enabled = %v, want [engine]", got)
		}
		// GET reflects it.
		code, body = send(t, app, orgMember("GET", "/v1/entitlement/orgs/acme", "acme", nil))
		if code != http.StatusOK || len(decodeEnabled(t, body)) != 1 {
			t.Fatalf("get after enable: %d %s", code, body)
		}
	})
}

func TestOrgMemberCannotEnableUnentitledProduct(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		fc := newFakeCommerce() // grants nothing
		app, _ := mount(t, fc)

		code, body := send(t, app, orgMember("POST", "/v1/entitlement/orgs/acme", "acme", map[string]any{"add": []string{"engine"}}))
		if code != http.StatusPaymentRequired {
			t.Fatalf("enable unentitled: want 402, got %d (body=%s)", code, body)
		}
		// Nothing persisted.
		_, body = send(t, app, orgMember("GET", "/v1/entitlement/orgs/acme", "acme", nil))
		if len(decodeEnabled(t, body)) != 0 {
			t.Fatalf("unentitled product must not have been enabled: %s", body)
		}
	})
}

func TestSuperAdminBypassesEntitlementGate(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		fc := newFakeCommerce() // grants nothing — a super admin still succeeds
		app, _ := mount(t, fc)

		// Super admin comps "engine" to acme even though acme's plan doesn't grant it.
		code, body := send(t, app, superAdmin("POST", "/v1/entitlement/orgs/acme", map[string]any{"add": []string{"engine"}}))
		if code != http.StatusOK {
			t.Fatalf("super-admin grant: want 200, got %d (body=%s)", code, body)
		}
		if got := decodeEnabled(t, body); len(got) != 1 || got[0] != "engine" {
			t.Fatalf("enabled = %v, want [engine]", got)
		}
		if fc.calls != 0 {
			t.Fatalf("super admin must bypass commerce; calls=%d", fc.calls)
		}
	})
}

// A product in beta is the org's only by a grant: no plan lets an org admin switch
// it on, a SuperAdmin may, and turning it off stays the org's.
func TestABetaProductIsNotAnOrgsToEnable(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		fc := newFakeCommerce()
		fc.grant("acme", "crm") // even a plan that names it
		app, _ := mount(t, fc)
		installed := stubInstalls(t)

		code, body := send(t, app, orgMember("POST", "/v1/entitlement/orgs/acme", "acme", map[string]any{"add": []string{"crm"}}))
		if code != http.StatusForbidden || !bytes.Contains(body, []byte("hanzo.ai/beta")) {
			t.Fatalf("an org admin enabling the CRM: want 403 naming the beta, got %d (body=%s)", code, body)
		}
		code, body = send(t, app, superAdmin("POST", "/v1/entitlement/orgs/acme", map[string]any{"add": []string{"crm"}}))
		if code != http.StatusOK || len(decodeEnabled(t, body)) != 1 {
			t.Fatalf("a SuperAdmin granting the CRM beta: want 200, got %d (body=%s)", code, body)
		}
		// Grown in acme's Base, never the SuperAdmin's own, though the request is theirs.
		if got := installed.all(); !slices.Equal(got, []string{"acme:crm"}) {
			t.Fatalf("installs = %v, want the crm module grown in acme's Base", got)
		}
		code, body = send(t, app, orgMember("POST", "/v1/entitlement/orgs/acme", "acme", map[string]any{"remove": []string{"crm"}}))
		if code != http.StatusOK || len(decodeEnabled(t, body)) != 0 {
			t.Fatalf("the org turning its CRM off: want 200 and nothing on, got %d (body=%s)", code, body)
		}
	})
}

func TestCommerceUnavailableFailsClosedForMemberAdd(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		// commerce nil ⇒ a non-super-admin add cannot be verified ⇒ 503, never open.
		app, _ := mount(t, nil)
		code, _ := send(t, app, orgMember("POST", "/v1/entitlement/orgs/acme", "acme", map[string]any{"add": []string{"engine"}}))
		if code != http.StatusServiceUnavailable {
			t.Fatalf("member add with nil commerce: want 503, got %d", code)
		}
	})
}

func TestRemoveNeverGated(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		// Disabling requires NO entitlement check — even with commerce nil, an org
		// member may turn a product off.
		store := openTestStore(t)
		_ = store.Enable(context.Background(), "acme", "engine", "u", 1)
		s := &service{store: store, commerce: nil, log: luxlog.New("test")}
		app := zip.New(zip.Config{Logger: luxlog.New("test")})
		compose(app)
		routes(app, s)

		code, body := send(t, app, orgMember("POST", "/v1/entitlement/orgs/acme", "acme", map[string]any{"remove": []string{"engine"}}))
		if code != http.StatusOK {
			t.Fatalf("remove with nil commerce: want 200, got %d (body=%s)", code, body)
		}
		if got := decodeEnabled(t, body); len(got) != 0 {
			t.Fatalf("after remove enabled = %v, want []", got)
		}
	})
}

func TestEmptyMutationRejected(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, _ := mount(t, newFakeCommerce())
		code, _ := send(t, app, orgMember("POST", "/v1/entitlement/orgs/acme", "acme", map[string]any{}))
		if code != http.StatusBadRequest {
			t.Fatalf("empty add+remove: want 400, got %d", code)
		}
	})
}

// Overlapping batches applied at once finish, each all-or-nothing, on either engine:
// without the store's write lock, two batches that take the same rows in opposite
// orders deadlock on PostgreSQL.
func TestOverlappingAppliesDoNotDeadlock(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		store := openTestStore(t)
		ctx := context.Background()
		var wg sync.WaitGroup
		for i := range 16 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				add := []string{"alpha", "beta", "gamma", "delta"}
				if i%2 == 1 {
					add = []string{"delta", "gamma", "beta", "alpha"}
				}
				if _, err := store.Apply(ctx, "acme", add, nil, "u", 1); err != nil {
					t.Errorf("apply %d: %v", i, err)
				}
			}()
		}
		wg.Wait()
		got, err := store.List(ctx, "acme")
		if err != nil || len(got) != 4 {
			t.Fatalf("enabled = %v, %v; want the four", got, err)
		}
		if _, err := store.Apply(ctx, "acme", []string{"ok"}, nil, "bad\x00actor", 1); err == nil {
			t.Fatal("a NUL in the actor was stored")
		}
	})
}
