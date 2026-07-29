// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package cloud

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// stubIAM records what the edge forwarded and answers a canned ok envelope, so a
// test can assert the org pin (what IAM would have keyed on) without a real IAM.
type stubIAM struct {
	*httptest.Server
	path  string
	query url.Values
}

func newStubIAM() *stubIAM {
	s := &stubIAM{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.path, s.query = r.URL.Path, r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","msg":"","data":[]}`))
	}))
	return s
}

// edgeApp mounts an iamEdge pointed at the stub and returns a caller that issues a
// request as a given principal (headers mirror what SanitizeIdentity mints).
func edgeApp(t *testing.T, host string) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	(&iamEdge{host: host, http: &http.Client{}}).mount(app)
	return app
}

func as(req *http.Request, org, user string, super, orgAdmin bool) *http.Request {
	req.Header.Set("X-User-Id", user)
	req.Header.Set("X-Org-Id", org)
	req.Header.Set("X-User-IsAdmin", b(super))
	req.Header.Set("X-User-IsOrgAdmin", b(orgAdmin))
	return req
}

func b(v bool) string {
	if v {
		return "true"
	}
	return "false"
}

// A tenant's read is PINNED to its own org: even with no organization param, IAM
// sees organization=<caller org>, so the permissive lister can't return every
// tenant's rows. This is the fix for "Request failed (HTTP 200)" AND the cross-
// tenant guard in one.
func TestEdgePinsOrgForTenant(t *testing.T) {
	iam := newStubIAM()
	defer iam.Close()
	app := edgeApp(t, iam.URL)

	req := as(httptest.NewRequest(http.MethodGet, "/v1/iam/get-organization-projects", nil), "maxpower", "maxpower/dave", false, true)
	res, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	if got := iam.query.Get("organization"); got != "maxpower" {
		t.Fatalf("forwarded organization = %q, want the caller's own %q (pin dropped?)", got, "maxpower")
	}
	if iam.path != "/v1/iam/get-organization-projects" {
		t.Fatalf("forwarded path = %q, want the full segment", iam.path)
	}
}

// A tenant naming ANOTHER org is refused (a super admin may cross — next test).
func TestEdgeRejectsCrossTenant(t *testing.T) {
	iam := newStubIAM()
	defer iam.Close()
	app := edgeApp(t, iam.URL)

	req := as(httptest.NewRequest(http.MethodGet, "/v1/iam/get-organization-projects?owner=acme", nil), "maxpower", "maxpower/dave", false, true)
	res, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 403 {
		t.Fatalf("status = %d, want 403 (named a foreign org)", res.StatusCode)
	}
	if iam.path != "" {
		t.Fatalf("request reached IAM (%q) — the gate must refuse BEFORE forwarding", iam.path)
	}
}

// A super admin is NOT pinned — the requested org is honored (org switch / god view).
func TestEdgeSuperAdminCrosses(t *testing.T) {
	iam := newStubIAM()
	defer iam.Close()
	app := edgeApp(t, iam.URL)

	req := as(httptest.NewRequest(http.MethodGet, "/v1/iam/get-organization-projects?organization=acme", nil), "admin", "admin/root", true, true)
	res, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	if got := iam.query.Get("organization"); got != "acme" {
		t.Fatalf("super admin organization = %q, want the requested %q (over-pinned)", got, "acme")
	}
}

// Only the allow-listed segments reach IAM; anything else is a real 404, never the SPA.
func TestEdgeAllowList(t *testing.T) {
	iam := newStubIAM()
	defer iam.Close()
	app := edgeApp(t, iam.URL)

	req := as(httptest.NewRequest(http.MethodGet, "/v1/iam/delete-everything", nil), "maxpower", "maxpower/dave", false, true)
	res, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 404 {
		t.Fatalf("status = %d, want 404 for an unknown segment", res.StatusCode)
	}
}

// No validated principal → 401 (never forwards an anonymous request to IAM).
func TestEdgeRequiresPrincipal(t *testing.T) {
	iam := newStubIAM()
	defer iam.Close()
	app := edgeApp(t, iam.URL)

	res, err := app.Fiber().Test(httptest.NewRequest(http.MethodGet, "/v1/iam/users", nil))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 401 {
		t.Fatalf("status = %d, want 401 (anonymous)", res.StatusCode)
	}
}

// A write requires an org admin, and a scoped write must carry the caller's OWN org
// in owner + organization (an omitted/foreign one is refused before IAM).
func TestEdgeWriteGate(t *testing.T) {
	iam := newStubIAM()
	defer iam.Close()
	app := edgeApp(t, iam.URL)

	post := func(user, org string, super, orgAdmin bool, body string) int {
		req := as(httptest.NewRequest(http.MethodPost, "/v1/iam/add-project", strings.NewReader(body)), org, user, super, orgAdmin)
		req.Header.Set("Content-Type", "application/json")
		res, err := app.Fiber().Test(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		return res.StatusCode
	}

	own := `{"owner":"maxpower","organization":"maxpower","name":"p"}`
	if s := post("maxpower/dave", "maxpower", false, false, own); s != 403 {
		t.Fatalf("plain member write = %d, want 403 (org admin required)", s)
	}
	if s := post("maxpower/dave", "maxpower", false, true, `{"owner":"acme","organization":"acme","name":"p"}`); s != 403 {
		t.Fatalf("write targeting a foreign org = %d, want 403", s)
	}
	if s := post("maxpower/dave", "maxpower", false, true, own); s != 200 {
		t.Fatalf("own-org admin write = %d, want 200", s)
	}
}

// own is the pin decision in isolation.
func TestEdgeOwnPredicate(t *testing.T) {
	e := &iamEdge{}
	cases := []struct {
		v, org         string
		super, metaOK  bool
		want           bool
	}{
		{"", "maxpower", false, false, true},          // empty is a no-op
		{"maxpower", "maxpower", false, false, true},   // own org
		{"acme", "maxpower", false, false, false},      // foreign org
		{"acme", "maxpower", true, false, true},        // super crosses
		{"admin", "maxpower", false, true, true},       // metadata owner where allowed
		{"admin", "maxpower", false, false, false},     // metadata owner where NOT allowed
	}
	for _, c := range cases {
		if got := e.own(c.v, c.org, c.super, c.metaOK); got != c.want {
			t.Errorf("own(%q, %q, super=%v, meta=%v) = %v, want %v", c.v, c.org, c.super, c.metaOK, got, c.want)
		}
	}
}

// THE LEAK. get-users is an org-scoped read that is NOT org-KEYED, so the pin
// above never reached it: a tenant's team page forwarded a BARE get-users under
// cloud's ONE service credential and got back whatever org that credential
// resolves to — measured live as 262 rows, all owner=hanzo, served to every
// tenant. The pin must cover every scoped segment, not the three keyed ones.
//
// It is also the gate on IAM v1.33.31: once Scope honours-or-refuses, a bare read
// still answers from the credential's org rather than the caller's, and a super
// credential (the naive fix for the 403) makes IAM's lister drop its Owner filter
// entirely — one leak traded for a worse one. Pinning here is what makes either
// credential safe.
func TestEdgePinsOrgOnEveryScopedRead(t *testing.T) {
	for _, seg := range []string{"get-users", "get-roles", "get-user"} {
		iam := newStubIAM()
		app := edgeApp(t, iam.URL)

		req := as(httptest.NewRequest(http.MethodGet, "/v1/iam/"+seg, nil), "maxpower", "maxpower/dave", false, true)
		res, err := app.Fiber().Test(req)
		if err != nil {
			iam.Close()
			t.Fatal(err)
		}
		res.Body.Close()
		if got := iam.query.Get("owner"); got != "maxpower" {
			t.Errorf("%s forwarded owner = %q, want the caller's own %q — an unpinned read answers from the CREDENTIAL's org, not the caller's", seg, got, "maxpower")
		}
		iam.Close()
	}
}

// A super admin is still not pinned — the god view survives the pin.
func TestEdgeSuperAdminUnpinnedOnScopedRead(t *testing.T) {
	iam := newStubIAM()
	defer iam.Close()
	app := edgeApp(t, iam.URL)

	req := as(httptest.NewRequest(http.MethodGet, "/v1/iam/get-users?owner=acme", nil), "admin", "admin/root", true, true)
	res, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if got := iam.query.Get("owner"); got != "acme" {
		t.Fatalf("super admin owner = %q, want the requested %q (over-pinned)", got, "acme")
	}
}

// The same hole on the WRITE side: add-user/update-user/delete-user are scoped but
// not KEYED, so the body check never ran on them. A tenant org-admin could name a
// foreign owner in the body and have it forwarded verbatim under cloud's
// credential — a cross-tenant WRITE the moment that credential can cross.
func TestEdgeRefusesForeignOwnerInWriteBody(t *testing.T) {
	for _, seg := range []string{"add-user", "update-user", "delete-user"} {
		iam := newStubIAM()
		app := edgeApp(t, iam.URL)

		req := as(httptest.NewRequest(http.MethodPost, "/v1/iam/"+seg, strings.NewReader(`{"owner":"acme","name":"mole"}`)), "maxpower", "maxpower/dave", false, true)
		req.Header.Set("Content-Type", "application/json")
		res, err := app.Fiber().Test(req)
		if err != nil {
			iam.Close()
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != 403 {
			t.Errorf("%s with a foreign owner in the body = %d, want 403", seg, res.StatusCode)
		}
		if iam.path != "" {
			t.Errorf("%s reached IAM (%q) — refuse BEFORE forwarding", seg, iam.path)
		}
		iam.Close()
	}
}

// ...and the caller's OWN org in the body is still forwarded.
func TestEdgeAllowsOwnOrgWriteBody(t *testing.T) {
	iam := newStubIAM()
	defer iam.Close()
	app := edgeApp(t, iam.URL)

	req := as(httptest.NewRequest(http.MethodPost, "/v1/iam/add-user", strings.NewReader(`{"owner":"maxpower","name":"dave"}`)), "maxpower", "maxpower/dave", false, true)
	req.Header.Set("Content-Type", "application/json")
	res, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("own-org add-user = %d, want 200", res.StatusCode)
	}
}

// The pin must not COST the target. IAM resolves a single read's target with
// ReadTarget, which falls back to `?id=<owner>/<name>` ONLY while `?owner=` is
// empty — so setting owner alongside an id suppresses the id and the read dies as
// "id (owner/name) or name is required". A pin that breaks every id-addressed read
// is not a fix, so the owner rides IN the id: the owner half is pinned, the name
// half is preserved.
func TestEdgePinRidesInTheId(t *testing.T) {
	iam := newStubIAM()
	defer iam.Close()
	app := edgeApp(t, iam.URL)

	req := as(httptest.NewRequest(http.MethodGet, "/v1/iam/get-user?id=maxpower/dave", nil), "maxpower", "maxpower/dave", false, true)
	res, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if got := iam.query.Get("id"); got != "maxpower/dave" {
		t.Errorf("forwarded id = %q, want %q (owner half pinned, name half kept)", got, "maxpower/dave")
	}
	if got := iam.query.Get("owner"); got != "" {
		t.Errorf("also forwarded owner=%q — a non-empty owner makes IAM ignore the id and 'require' one", got)
	}
}

// A BARE `?id=dave` means different things on the two sides of this hop: cloud
// reads the whole id as an OWNER (iamEdge.owner), IAM reads it as a NAME
// (authz.ReadTarget returns ("", id) for an id with no slash). The divergence is
// real, and the STRICTER reading is cloud's — a bare id that is not the caller's
// own org is refused here and never reaches IAM, where it would have been scoped
// to the forwarding CREDENTIAL's org instead of the caller's. Pinned as behaviour
// so the divergence is not "reconciled" later by loosening this side.
func TestEdgeRefusesBareId(t *testing.T) {
	iam := newStubIAM()
	defer iam.Close()
	app := edgeApp(t, iam.URL)

	req := as(httptest.NewRequest(http.MethodGet, "/v1/iam/get-user?id=dave", nil), "maxpower", "maxpower/dave", false, true)
	res, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 403 {
		t.Fatalf("bare id = %d, want 403", res.StatusCode)
	}
	if iam.path != "" {
		t.Fatalf("bare id reached IAM (%q) — refuse BEFORE forwarding", iam.path)
	}
}
