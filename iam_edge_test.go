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

	res, err := app.Fiber().Test(httptest.NewRequest(http.MethodGet, "/v1/iam/get-users", nil))
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
