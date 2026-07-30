package templates

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/openapi"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// This file is the MEASUREMENT that typing the gallery did not move its wire.
// Every claim here is one the conversion could have broken silently: a status a
// typed op writes by declaration rather than by hand, a body key a struct drops
// where a map carried it, and — the one no status-code test would have caught —
// a query parameter zip's binder fills that this route has never accepted.

// galleryOps reads BOTH projections of the LIVE router at their one shared
// address form: what the document says is served, and which of those carry a
// typed registry entry with prose. Reading the router rather than the source is
// what makes this a gate and not a promise.
func galleryOps(t *testing.T) (served map[string]bool, typed map[string]*openapi.Operation) {
	t.Helper()
	app := mountApp(t)
	doc, err := openapi.Spec(app, openapi.Info{Title: "templates", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	reg, err := openapi.Typed(app)
	if err != nil {
		t.Fatalf("typed registry: %v", err)
	}
	ours := func(p string) bool { return strings.HasPrefix(p, "/v1/templates") }
	served, typed = map[string]bool{}, map[string]*openapi.Operation{}
	for path, item := range doc.Paths {
		if !ours(path) {
			continue
		}
		for method := range item {
			served[strings.ToUpper(method)+" "+path] = true
		}
	}
	for key, op := range reg.Ops {
		if i := strings.Index(key, " "); i > 0 && ours(key[i+1:]) {
			typed[key] = op
		}
	}
	return served, typed
}

// TestEveryRouteIsATypedOp is the closed ledger: the gallery serves five
// operations and all five are ops, so a sixth added untyped goes red without
// anyone remembering to look. An untyped route contributes no schema, no prose,
// no MCP tool, no CLI command and no typed SDK method — it projects to nothing.
func TestEveryRouteIsATypedOp(t *testing.T) {
	served, typed := galleryOps(t)
	if len(served) != 5 {
		t.Fatalf("the gallery serves %d operations, not the 5 this ledger knows: %s", len(served), sorted(served))
	}
	var untyped []string
	for key := range served {
		if _, ok := typed[key]; !ok {
			untyped = append(untyped, key)
		}
	}
	if len(untyped) > 0 {
		sort.Strings(untyped)
		t.Errorf("operation(s) with no registry entry: %s\n"+
			"Convert them (zip.Get/Post/... in routes()), or this API publishes a route and nothing else.",
			strings.Join(untyped, ", "))
	}
}

// TestEveryTypedOpIsDescribed proves the prose actually reached the registry.
// zipdoc lifts a handler's doc comment into zipdoc_gen.go at BUILD time, so a
// package that loses its //go:generate directive keeps compiling perfectly while
// every one of its operations goes back to publishing nothing at all.
func TestEveryTypedOpIsDescribed(t *testing.T) {
	_, typed := galleryOps(t)
	if len(typed) != 5 {
		t.Fatalf("the registry carries %d operations, want 5", len(typed))
	}
	for key, op := range typed {
		if strings.TrimSpace(op.Description) == "" {
			t.Errorf("%s publishes NO description — the OpenAPI prose and the MCP tool description are both empty", key)
		}
		if strings.TrimSpace(op.Summary) == "" {
			t.Errorf("%s publishes NO summary — the CLI command help and every SDK docstring are empty", key)
		}
		if strings.Contains(op.Summary, "\n") {
			t.Errorf("%s has a line break in its one-line summary: %q", key, op.Summary)
		}
	}
}

// TestWriteStatusesAreUnmoved pins the three success statuses. zip writes 200 by
// default and 204 for a nil Out, so a create converted without zip.WithStatus(201)
// downgrades silently — a break for every client that checks the code — and a
// delete whose Out is a NAMED empty type publishes "200 with a body" for a route
// that answers 204 with none.
func TestWriteStatusesAreUnmoved(t *testing.T) {
	app := mountApp(t)
	kit := map[string]any{"slug": "status-kit", "title": "Status Kit"}

	if code, body := do(t, app, http.MethodPost, "/v1/templates", "acme", kit); code != http.StatusCreated {
		t.Fatalf("publish want 201, got %d (%s)", code, body)
	}
	if code, body := do(t, app, http.MethodPut, "/v1/templates/status-kit", "acme", kit); code != http.StatusOK {
		t.Fatalf("replace want 200, got %d (%s)", code, body)
	}
	code, body := do(t, app, http.MethodDelete, "/v1/templates/status-kit", "acme", nil)
	if code != http.StatusNoContent {
		t.Fatalf("delete want 204, got %d (%s)", code, body)
	}
	if len(body) != 0 {
		t.Fatalf("delete answered 204 with a body: %s", body)
	}
}

// TestTheQueryStringCannotRedirectAWrite is the pin on `url:"-"`. zip's binder
// fills an In field from the QUERY as well as the body (typed.go bindURL: body,
// then query, then path), so a body field left URL-bindable silently starts
// accepting `?slug=` — and `?slug=` on a publish would create a kit under a name
// the body never asked for. The untyped handler this replaced read the body and
// nothing else, so the only wire-preserving answer is that the query is ignored.
func TestTheQueryStringCannotRedirectAWrite(t *testing.T) {
	app := mountApp(t)
	kit := map[string]any{"slug": "asked-for", "title": "Asked For"}

	if code, body := do(t, app, http.MethodPost, "/v1/templates?slug=hijacked&title=Hijacked", "acme", kit); code != http.StatusCreated {
		t.Fatalf("publish want 201, got %d (%s)", code, body)
	}
	if code, _ := do(t, app, http.MethodGet, "/v1/templates/asked-for", "acme", nil); code != http.StatusOK {
		t.Fatal("the query string redirected a publish: the kit the BODY named was not created")
	}
	if code, _ := do(t, app, http.MethodGet, "/v1/templates/hijacked", "acme", nil); code != http.StatusNotFound {
		t.Fatal("the query string redirected a publish: a kit was created under the QUERY's slug")
	}
	code, body := do(t, app, http.MethodGet, "/v1/templates/asked-for", "acme", nil)
	if code != http.StatusOK {
		t.Fatalf("get want 200, got %d", code)
	}
	var got StarterKit
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Title != "Asked For" {
		t.Fatalf("the query string overwrote a body field: title=%q, want %q", got.Title, "Asked For")
	}
}

// TestThePathOwnsTheIdentityOnReplace is the other half: on a PUT the path binds
// LAST, so a body slug can never rename a kit or redirect the write onto another
// one. That is the wire the untyped handler had — it overwrote the bound slug
// with the path param — and it is what keeps a write on the resource the URL named.
func TestThePathOwnsTheIdentityOnReplace(t *testing.T) {
	app := mountApp(t)
	for _, slug := range []string{"first", "second"} {
		if code, body := do(t, app, http.MethodPost, "/v1/templates", "acme",
			map[string]any{"slug": slug, "title": strings.ToUpper(slug)}); code != http.StatusCreated {
			t.Fatalf("publish %s want 201, got %d (%s)", slug, code, body)
		}
	}
	code, body := do(t, app, http.MethodPut, "/v1/templates/first", "acme",
		map[string]any{"slug": "second", "title": "Rewritten"})
	if code != http.StatusOK {
		t.Fatalf("replace want 200, got %d (%s)", code, body)
	}
	var got StarterKit
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Slug != "first" {
		t.Fatalf("the body renamed a kit: slug=%q, want %q", got.Slug, "first")
	}
	_, body = do(t, app, http.MethodGet, "/v1/templates/second", "acme", nil)
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Title != "SECOND" {
		t.Fatalf("a PUT on /first wrote /second: title=%q", got.Title)
	}
}

// TestCurationFieldsStayServerOwned pins the one thing the In deliberately does
// NOT carry. tier and rating are public-gallery curation; the untyped handler
// bound them and then cleared them, so a caller could always send them and never
// set them. Leaving them off the In keeps that wire AND stops every generated SDK
// offering two arguments the server discards.
func TestCurationFieldsStayServerOwned(t *testing.T) {
	app := mountApp(t)
	code, body := do(t, app, http.MethodPost, "/v1/templates", "acme",
		map[string]any{"slug": "curated", "title": "Curated", "tier": 1, "rating": 4.9})
	if code != http.StatusCreated {
		t.Fatalf("publish want 201, got %d (%s)", code, body)
	}
	var got StarterKit
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Tier != nil || got.Rating != nil {
		t.Fatalf("a caller set gallery curation: tier=%v rating=%v", got.Tier, got.Rating)
	}
}

// TestBrowseIsOneKeyedList pins the browse envelope. The op returns a struct
// where a map[string]any used to be, and a struct drops any key its fields do not
// name — so this asserts the answer is still exactly {"data": [...]}.
func TestBrowseIsOneKeyedList(t *testing.T) {
	app := mountApp(t)
	code, body := do(t, app, http.MethodGet, "/v1/templates", "", nil)
	if code != http.StatusOK {
		t.Fatalf("browse want 200, got %d", code)
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(envelope) != 1 {
		t.Fatalf("browse answers %d keys, want exactly 1 (data)", len(envelope))
	}
	if _, ok := envelope["data"]; !ok {
		t.Fatal("browse lost its data key")
	}
}

// TestTheBridgeIsInstalledAheadOfTheLeaves is the structural claim every tenancy
// test here rests on. A typed op reads its tenant off the CONTEXT, which only
// cloud.Bridge parks there — and fiber runs middleware in registration order, so
// one installed after its leaves never runs and every org-scoped op 403s. This
// mounts the REAL Mount on a bare app (no app-wide bridge, exactly as this
// package's tests have always mounted it) and proves a validated caller is served
// while an anonymous one is refused.
func TestTheBridgeIsInstalledAheadOfTheLeaves(t *testing.T) {
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	if err := Mount(app, cloud.Deps{Logger: luxlog.New("test"), DataDir: t.TempDir()}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	t.Cleanup(func() { _ = Shutdown(t.Context()) })
	if code, body := do(t, app, http.MethodPost, "/v1/templates", "acme",
		map[string]any{"slug": "bridged", "title": "Bridged"}); code != http.StatusCreated {
		t.Fatalf("a validated caller got %d (%s) — the bridge did not run, so the op saw no org", code, body)
	}
	if code, _ := do(t, app, http.MethodPost, "/v1/templates", "",
		map[string]any{"slug": "anon", "title": "Anon"}); code != http.StatusForbidden {
		t.Fatalf("an anonymous write got %d, want 403", code)
	}
}

// TestTheCollectionRootHasNoTrailingSlash pins failure mode #9: declaring a
// group's root as its EMPTY leaf names "<prefix>/", and op.Path is the identity
// the document, the operationId, the MCP tool name and every generated SDK's URL
// all read. The router is non-strict, so nothing would break on the wire — it is
// visible only in the published artifact, which is the whole product surface.
func TestTheCollectionRootHasNoTrailingSlash(t *testing.T) {
	served, _ := galleryOps(t)
	for key := range served {
		if strings.HasSuffix(key, "/") {
			t.Errorf("%s publishes a trailing slash for a path this API has never served", key)
		}
	}
}

func sorted(m map[string]bool) string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return strings.Join(out, ", ")
}
