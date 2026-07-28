package templates

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// mountApp mounts the templates surface on a fresh in-memory app with a temp
// store, exactly as the unified binary does.
func mountApp(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	if err := Mount(app, cloud.Deps{Logger: luxlog.New("test"), DataDir: t.TempDir()}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	t.Cleanup(func() { _ = Shutdown(t.Context()) })
	return app
}

// do issues one request. org=="" is an ANONYMOUS caller (no principal at all);
// otherwise the headers are the ones the identity boundary mints for a validated
// principal — there is no request field for the org, by design.
func do(t *testing.T, app *zip.App, method, path, org string, body any) (int, []byte) {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, r)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if org != "" {
		req.Header.Set("X-Org-Id", org)
		req.Header.Set("X-User-Id", "u_"+org)
	}
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// slugs lists the template slugs in a {data:[...]} browse response.
func slugs(t *testing.T, body []byte) map[string]string {
	t.Helper()
	var out struct {
		Data []Template `json:"data"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode list: %v (%s)", err, body)
	}
	m := map[string]string{}
	for _, tpl := range out.Data {
		m[tpl.Slug] = tpl.Org
	}
	return m
}

// TestPrivateTemplateIsOrgOnly is the whole tenancy claim, proven in BOTH
// directions: acme's private template is visible to acme, and invisible to
// another org AND to an anonymous browser — which is what the public hanzo.app
// gallery is.
func TestPrivateTemplateIsOrgOnly(t *testing.T) {
	app := mountApp(t)
	tpl := Template{Slug: "acme-internal", Title: "Acme Internal Portal", Framework: "Next.js 14", Source: "https://git.acme.example/portal"}

	if code, body := do(t, app, http.MethodPost, "/v1/templates", "acme", tpl); code != http.StatusCreated {
		t.Fatalf("publish want 201, got %d (%s)", code, body)
	}

	// Owner sees it, stamped with the SERVER's org.
	code, body := do(t, app, http.MethodGet, "/v1/templates", "acme", nil)
	if code != http.StatusOK {
		t.Fatalf("acme list want 200, got %d", code)
	}
	mine := slugs(t, body)
	if owner, ok := mine["acme-internal"]; !ok || owner != "acme" {
		t.Fatalf("acme cannot see its own template (owner=%q ok=%v)", owner, ok)
	}
	if code, _ := do(t, app, http.MethodGet, "/v1/templates/acme-internal", "acme", nil); code != http.StatusOK {
		t.Fatalf("acme get own want 200, got %d", code)
	}

	// Another org: not in the browse list, and not addressable by slug.
	code, body = do(t, app, http.MethodGet, "/v1/templates", "globex", nil)
	if code != http.StatusOK {
		t.Fatalf("globex list want 200, got %d", code)
	}
	if _, leaked := slugs(t, body)["acme-internal"]; leaked {
		t.Fatal("CROSS-ORG LEAK: acme's private template appeared in globex's catalog")
	}
	if code, _ := do(t, app, http.MethodGet, "/v1/templates/acme-internal", "globex", nil); code != http.StatusNotFound {
		t.Fatalf("globex get acme's template want 404, got %d", code)
	}

	// Anonymous — the public hanzo.app catalog. It must be the embedded gallery
	// and nothing else.
	code, body = do(t, app, http.MethodGet, "/v1/templates", "", nil)
	if code != http.StatusOK {
		t.Fatalf("public list want 200, got %d", code)
	}
	pub := slugs(t, body)
	if _, leaked := pub["acme-internal"]; leaked {
		t.Fatal("PUBLIC LEAK: a private template surfaced in the anonymous catalog")
	}
	cat, err := catalog()
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	if len(pub) != len(cat) {
		t.Fatalf("public catalog has %d entries, embedded gallery has %d — the anonymous view is not the gallery", len(pub), len(cat))
	}
	if code, _ := do(t, app, http.MethodGet, "/v1/templates/acme-internal", "", nil); code != http.StatusNotFound {
		t.Fatalf("anonymous get private want 404, got %d", code)
	}
}

// TestWritesBindOrg proves every mutation is scoped by the minted org: another
// org can neither edit nor delete a template it does not own, an anonymous
// caller cannot write at all, and a body "org" is ignored in favor of the
// server's.
func TestWritesBindOrg(t *testing.T) {
	app := mountApp(t)
	// A forged owner in the body must not decide anything.
	tpl := Template{Slug: "widget", Title: "Widget Kit", Org: "globex"}
	code, body := do(t, app, http.MethodPost, "/v1/templates", "acme", tpl)
	if code != http.StatusCreated {
		t.Fatalf("publish want 201, got %d (%s)", code, body)
	}
	var got Template
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Org != "acme" {
		t.Fatalf("body org was trusted: owner=%q want acme", got.Org)
	}

	if code, _ := do(t, app, http.MethodPost, "/v1/templates", "", tpl); code != http.StatusForbidden {
		t.Fatalf("anonymous publish want 403, got %d", code)
	}
	if code, _ := do(t, app, http.MethodPut, "/v1/templates/widget", "globex",
		Template{Title: "Hijacked"}); code != http.StatusNotFound {
		t.Fatalf("cross-org edit want 404, got %d", code)
	}
	if code, _ := do(t, app, http.MethodDelete, "/v1/templates/widget", "globex", nil); code != http.StatusNotFound {
		t.Fatalf("cross-org delete want 404, got %d", code)
	}
	// The owner's own edit + delete still work, and the row is really gone.
	if code, _ := do(t, app, http.MethodPut, "/v1/templates/widget", "acme",
		Template{Title: "Widget Kit v2"}); code != http.StatusOK {
		t.Fatalf("owner edit want 200, got %d", code)
	}
	if code, _ := do(t, app, http.MethodDelete, "/v1/templates/widget", "acme", nil); code != http.StatusNoContent {
		t.Fatalf("owner delete want 204, got %d", code)
	}
	if code, _ := do(t, app, http.MethodGet, "/v1/templates/widget", "acme", nil); code != http.StatusNotFound {
		t.Fatalf("get after delete want 404, got %d", code)
	}
}

// TestSlugStaysSingleValued proves an org cannot publish over a public gallery
// slug (which would make one slug mean two different things depending on who
// asks) and cannot publish the same slug twice.
func TestSlugStaysSingleValued(t *testing.T) {
	app := mountApp(t)
	cat, err := catalog()
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	if code, body := do(t, app, http.MethodPost, "/v1/templates", "acme",
		Template{Slug: cat[0].Slug, Title: "Shadow"}); code != http.StatusConflict {
		t.Fatalf("shadowing a public slug want 409, got %d (%s)", code, body)
	}
	if code, _ := do(t, app, http.MethodPost, "/v1/templates", "acme",
		Template{Slug: "dup", Title: "First"}); code != http.StatusCreated {
		t.Fatal("first publish must succeed")
	}
	if code, _ := do(t, app, http.MethodPost, "/v1/templates", "acme",
		Template{Slug: "dup", Title: "Second"}); code != http.StatusConflict {
		t.Fatalf("republishing an owned slug want 409, got %d", code)
	}
	// Two orgs CAN hold the same private slug — the key is (org, slug).
	if code, _ := do(t, app, http.MethodPost, "/v1/templates", "globex",
		Template{Slug: "dup", Title: "Globex's own"}); code != http.StatusCreated {
		t.Fatalf("another org's same slug want 201, got %d", code)
	}
	if code, body := do(t, app, http.MethodGet, "/v1/templates/dup", "globex", nil); code != http.StatusOK {
		t.Fatalf("globex get own dup want 200, got %d (%s)", code, body)
	}
}
