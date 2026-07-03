package cms

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/framework"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// TestFixturesValid proves every CMS DocType is a well-formed schema and that its
// Link targets resolve WITHIN the lane — so installing the module yields a
// self-consistent content model (no dangling Link target that would 422 at write).
func TestFixturesValid(t *testing.T) {
	dts := DocTypes()
	names := map[string]framework.DocType{}
	for _, dt := range dts {
		if err := dt.Validate(); err != nil {
			t.Fatalf("fixture %q invalid: %v", dt.Name, err)
		}
		names[dt.Name] = dt
	}
	for _, dt := range dts {
		for _, f := range dt.Fields {
			if f.Fieldtype == framework.FieldLink {
				if _, ok := names[f.Options]; !ok {
					t.Fatalf("%s.%s links to %q which is not a CMS DocType", dt.Name, f.Fieldname, f.Options)
				}
			}
		}
	}
}

// TestContentModelSpec locks the content-model contract the console's generic
// DocType renderer relies on: the publishable content types, their publish status
// field (Draft/Published), the slug autoname, and the author Link.
func TestContentModelSpec(t *testing.T) {
	byName := map[string]framework.DocType{}
	for _, dt := range DocTypes() {
		if dt.Module != Module {
			t.Fatalf("%s: module want %q, got %q", dt.Name, Module, dt.Module)
		}
		byName[dt.Name] = dt
	}
	for _, want := range []string{"Author", "Media", "Page", "Post", "Article", "Navigation"} {
		if _, ok := byName[want]; !ok {
			t.Fatalf("missing CMS DocType %q", want)
		}
	}
	// The three publishable content types share the publish contract.
	for _, name := range []string{"Page", "Post", "Article"} {
		dt := byName[name]
		if dt.Autoname != "field:slug" {
			t.Fatalf("%s: autoname want field:slug, got %q", name, dt.Autoname)
		}
		status, ok := fieldOf(dt, "status")
		if !ok || status.Fieldtype != framework.FieldSelect || status.Default != "Draft" {
			t.Fatalf("%s: expected a Select status field defaulting to Draft, got %+v", name, status)
		}
		if status.Options != "Draft\nPublished" {
			t.Fatalf("%s: status options want Draft/Published, got %q", name, status.Options)
		}
		author, ok := fieldOf(dt, "author")
		if !ok || author.Fieldtype != framework.FieldLink || author.Options != "Author" {
			t.Fatalf("%s: expected an author Link to Author, got %+v", name, author)
		}
	}
	// Media is Attach-backed (the DAM).
	if file, ok := fieldOf(byName["Media"], "file"); !ok || file.Fieldtype != framework.FieldAttach || !file.Reqd {
		t.Fatalf("Media.file must be a required Attach, got %+v", file)
	}
}

func fieldOf(dt framework.DocType, name string) (framework.DocField, bool) {
	for _, f := range dt.Fields {
		if f.Fieldname == name {
			return f, true
		}
	}
	return framework.DocField{}, false
}

// TestInstallAndPublishRoundTrip drives the WHOLE content model through the real
// engine over HTTP: install the lane, create an author, create a page that links
// it (status Draft), then publish it and read it back by slug — proving publish =
// a status field and the Link integrity holds.
func TestInstallAndPublishRoundTrip(t *testing.T) {
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	if err := framework.Mount(app, cloud.Deps{Logger: luxlog.New("test"), DataDir: t.TempDir()}); err != nil {
		t.Fatalf("mount framework: %v", err)
	}
	t.Cleanup(func() { _ = framework.Shutdown() })
	const org = "acme"

	// Install the CMS lane (the caller becomes System Manager, trust-on-first-use).
	if code, body := call(t, app, http.MethodPost, "/v1/framework/modules/cms/install", org, nil); code != http.StatusOK {
		t.Fatalf("install cms want 200, got %d (%s)", code, body)
	}

	// Create an Author (hash-named) to link.
	code, body := call(t, app, http.MethodPost, "/v1/framework/Author", org, map[string]any{"name": "Ada Lovelace", "email": "ada@example.com"})
	if code != http.StatusCreated {
		t.Fatalf("create author want 201, got %d (%s)", code, body)
	}
	var author map[string]any
	_ = json.Unmarshal(body, &author)
	authorName, _ := author["name"].(string) // hash id
	if authorName == "" {
		t.Fatalf("author has no name: %s", body)
	}

	// Create a Page linking the author; slug becomes the document name; Draft.
	code, body = call(t, app, http.MethodPost, "/v1/framework/Page", org, map[string]any{
		"title": "About Us", "slug": "about-us", "body": "<h1>About</h1>", "author": authorName,
	})
	if code != http.StatusCreated {
		t.Fatalf("create page want 201, got %d (%s)", code, body)
	}
	var page map[string]any
	_ = json.Unmarshal(body, &page)
	if page["name"] != "about-us" {
		t.Fatalf("page name (autoname field:slug) want about-us, got %v", page["name"])
	}
	if page["status"] != "Draft" {
		t.Fatalf("new page status want Draft (default), got %v", page["status"])
	}

	// No published pages yet.
	if n := listCount(t, app, org, `/v1/framework/Page?filters={"status":"Published"}`); n != 0 {
		t.Fatalf("published pages want 0, got %d", n)
	}

	// Publish = set the status field.
	if code, body := call(t, app, http.MethodPut, "/v1/framework/Page/about-us", org, map[string]any{
		"title": "About Us", "slug": "about-us", "status": "Published", "author": authorName,
	}); code != http.StatusOK {
		t.Fatalf("publish page want 200, got %d (%s)", code, body)
	}
	if n := listCount(t, app, org, `/v1/framework/Page?filters={"status":"Published"}`); n != 1 {
		t.Fatalf("published pages want 1, got %d", n)
	}

	// A dangling author Link is refused (422) — Link integrity within the org.
	if code, _ := call(t, app, http.MethodPost, "/v1/framework/Page", org, map[string]any{
		"title": "Ghost", "slug": "ghost", "author": "nonexistent-author",
	}); code != http.StatusUnprocessableEntity {
		t.Fatalf("dangling author link want 422, got %d", code)
	}
}

// ---- test HTTP harness (a validated principal u_<org>) ----

func call(t *testing.T, app *zip.App, method, path, org string, body any) (int, []byte) {
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
	req.Header.Set("X-Org-Id", org)
	req.Header.Set("X-User-Id", "u_"+org) // the validated-principal signal
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func listCount(t *testing.T, app *zip.App, org, path string) int {
	t.Helper()
	code, body := call(t, app, http.MethodGet, path, org, nil)
	if code != http.StatusOK {
		t.Fatalf("list %s want 200, got %d (%s)", path, code, body)
	}
	var res struct {
		Data []map[string]any `json:"data"`
	}
	_ = json.Unmarshal(body, &res)
	return len(res.Data)
}
