package webhook

import (
	"bytes"
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/openapi"
	"github.com/zap-proto/zip"
)

// This file is the MEASUREMENT that typing /v1/webhook did not move its wire.
// All eight operations are typed ops — untypedByDesign is deliberately EMPTY —
// and before this pass every one of them published no summary and no
// description, which is exactly the set that projects to NOTHING: no prose, no
// MCP tool, no CLI command, no typed SDK method.

// untypedByDesign is the closed list of webhooks operations that are NOT typed
// ops. It is empty, and TestEveryRouteIsTypedOrNamed is what keeps it that way:
// a route added untyped here goes red without anyone remembering to look.
var untypedByDesign = map[string]string{}

// call issues one request. org=="" is an ANONYMOUS caller; validated=false with
// a non-empty org is the OTHER refusal the untyped gate distinguished (an org
// header with no verified principal behind it).
func call(t *testing.T, app *zip.App, method, path, org string, validated bool, body []byte) (int, []byte) {
	t.Helper()
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req := httptest.NewRequest(method, path, r)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if org != "" {
		req.Header.Set("X-Org-Id", org)
		if validated {
			req.Header.Set("X-User-Id", "u_"+org)
		}
	}
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("Test %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// webhookOps reads BOTH projections of the LIVE router at their one shared
// address form: what the document says is served, and which of those carry a
// typed registry entry. Reading the router rather than the source is what makes
// this a gate and not prose.
func webhookOps(t *testing.T) (served map[string]bool, typed map[string]*openapi.Operation) {
	t.Helper()
	app := mountApp(t)
	doc, err := openapi.Spec(app, openapi.Info{Title: "webhook", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	reg, err := openapi.Typed(app)
	if err != nil {
		t.Fatalf("typed registry: %v", err)
	}
	ours := func(p string) bool { return strings.HasPrefix(p, "/v1/webhook") }
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

// TestEveryRouteIsTypedOrNamed fails when a webhooks operation is neither a typed
// op nor one named in untypedByDesign. The two ledgers must SUM to the served
// surface, so a stale reason cannot hide behind a route that no longer exists.
func TestEveryRouteIsTypedOrNamed(t *testing.T) {
	served, typed := webhookOps(t)
	if len(served) != 8 {
		t.Fatalf("webhooks serves %d operations, not the 8 these ledgers know: %s", len(served), sortedKeys(served))
	}
	var untyped []string
	for key := range served {
		if _, ok := typed[key]; ok {
			continue
		}
		if _, named := untypedByDesign[key]; named {
			continue
		}
		untyped = append(untyped, key)
	}
	if len(untyped) > 0 {
		sort.Strings(untyped)
		t.Errorf("operation(s) with no registry entry and no reason: %s\n"+
			"A route that is not a typed op has no prose, no MCP tool, no CLI command and no typed SDK "+
			"method. Convert it (zip.Get/Post/... in routes()), or add it to untypedByDesign with the "+
			"wire fact that typing it would move.", strings.Join(untyped, ", "))
	}
	if len(typed)+len(untypedByDesign) != len(served) {
		t.Errorf("%d typed + %d named != %d served", len(typed), len(untypedByDesign), len(served))
	}
}

// TestEveryTypedOpIsDescribed proves the prose actually reached the registry.
// zipdoc lifts a handler's doc comment into zipdoc_gen.go at BUILD time, so a
// package that loses its //go:generate directive keeps compiling perfectly while
// every one of its operations goes back to publishing nothing at all — which is
// the state this whole subsystem was in.
func TestEveryTypedOpIsDescribed(t *testing.T) {
	_, typed := webhookOps(t)
	if len(typed) != 8 {
		t.Fatalf("the registry carries %d operations, want 8", len(typed))
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

// TestTheCollectionRootHasNoTrailingSlash pins failure mode #9. The untyped
// registration declared the collection as the group's EMPTY leaf, which
// normalises to "<prefix>/", so the published document, the operationId, the MCP
// tool name and every generated SDK's URL carried /v1/webhook/ for a path every
// caller addresses without the slash. The router is non-strict, so nothing broke
// on the wire — it was visible only in the artifact, which IS the product surface.
func TestTheCollectionRootHasNoTrailingSlash(t *testing.T) {
	served, _ := webhookOps(t)
	for key := range served {
		if strings.HasSuffix(key, "/") {
			t.Errorf("%s publishes a trailing slash for a path this API has never served", key)
		}
	}
	// And BOTH spellings still reach the handler, which is what makes the
	// artifact fix safe.
	app := mountApp(t)
	for _, path := range []string{"/v1/webhook", "/v1/webhook/"} {
		if code, body := call(t, app, http.MethodGet, path, "acme", true, nil); code != http.StatusOK {
			t.Errorf("GET %s want 200, got %d (%s)", path, code, body)
		}
	}
}

// TestFailsClosedWithoutAValidatedPrincipal is the tenancy claim AND the record
// of the one delta this conversion took. There is no request field for the
// tenant, so an anonymous caller reads and writes nothing — and an X-Org-Id with
// no verified principal behind it is refused just as hard, which is the case the
// forgeable-header path exists for. Both answer 401, as they always did; what
// changed is that the two branches now share one message, because
// principal.OrgFrom folds them into one answer.
func TestFailsClosedWithoutAValidatedPrincipal(t *testing.T) {
	app := mountApp(t)
	routes := []struct{ method, path string }{
		{http.MethodGet, "/v1/webhook"},
		{http.MethodPost, "/v1/webhook"},
		{http.MethodGet, "/v1/webhook/wh_x"},
		{http.MethodPut, "/v1/webhook/wh_x"},
		{http.MethodDelete, "/v1/webhook/wh_x"},
		{http.MethodGet, "/v1/webhook/wh_x/deliveries"},
		{http.MethodPost, "/v1/webhook/wh_x/test"},
		{http.MethodPost, "/v1/webhook/wh_x/secret"},
	}
	for _, r := range routes {
		var body []byte
		if r.method == http.MethodPost || r.method == http.MethodPut {
			body = []byte(`{"url":"https://acme.example/hook"}`)
		}
		// No principal at all.
		if code, got := call(t, app, r.method, r.path, "", false, body); code != http.StatusUnauthorized {
			t.Errorf("%s %s anonymous got %d, want 401 (%s)", r.method, r.path, code, got)
		}
		// An org header with NO verified principal behind it — the forged case.
		if code, got := call(t, app, r.method, r.path, "victim", false, body); code != http.StatusUnauthorized {
			t.Errorf("%s %s unvalidated org got %d, want 401 (%s)", r.method, r.path, code, got)
		}
	}
}

// TestTheQueryStringCannotRedirectAWrite is the pin on `url:"-"`. zip's binder
// fills an In field from the QUERY as well as the body, so a converted write
// silently starts accepting `?url=` — which on this route would redirect where
// an org's signed events are delivered. The untyped handler read c.Bind, which
// is the body and nothing else.
func TestTheQueryStringCannotRedirectAWrite(t *testing.T) {
	app := mountApp(t)
	code, body := call(t, app, http.MethodPost,
		"/v1/webhook?url=https://evil.example/steal&status=disabled&description=pwned", "acme", true,
		[]byte(`{"url":"https://acme.example/hook","status":"active","description":"orders"}`))
	if code != http.StatusCreated {
		t.Fatalf("create want 201, got %d (%s)", code, body)
	}
	var got Endpoint
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.URL != "https://acme.example/hook" || got.Status != "active" || got.Description != "orders" {
		t.Fatalf("the query string reached a body-only field: %+v", got)
	}
}

// TestThePathIsTheAddressingAuthority pins what the untyped handlers did with a
// body id: nothing. They read c.Param("id"); the typed ops bind it from the path
// with `json:"-"`, so a body cannot even name a second target.
func TestThePathIsTheAddressingAuthority(t *testing.T) {
	app := mountApp(t)
	_, body := call(t, app, http.MethodPost, "/v1/webhook", "acme", true,
		[]byte(`{"url":"https://acme.example/one"}`))
	var first Endpoint
	if err := json.Unmarshal(body, &first); err != nil {
		t.Fatalf("decode: %v", err)
	}
	_, body = call(t, app, http.MethodPost, "/v1/webhook", "acme", true,
		[]byte(`{"url":"https://acme.example/two"}`))
	var second Endpoint
	if err := json.Unmarshal(body, &second); err != nil {
		t.Fatalf("decode: %v", err)
	}
	code, body := call(t, app, http.MethodPut, "/v1/webhook/"+first.ID, "acme", true,
		[]byte(`{"id":"`+second.ID+`","url":"https://acme.example/renamed"}`))
	if code != http.StatusOK {
		t.Fatalf("update want 200, got %d (%s)", code, body)
	}
	var saved Endpoint
	if err := json.Unmarshal(body, &saved); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if saved.ID != first.ID {
		t.Fatalf("the body redirected the write to %s", saved.ID)
	}
}

// TestSecretsLeaveOnceEach pins the reveal-once contract across the shapes the
// conversion touched: create and rotate carry the secret, every read redacts it.
func TestSecretsLeaveOnceEach(t *testing.T) {
	app := mountApp(t)
	code, body := call(t, app, http.MethodPost, "/v1/webhook", "acme", true,
		[]byte(`{"url":"https://acme.example/hook"}`))
	if code != http.StatusCreated {
		t.Fatalf("create want 201, got %d (%s)", code, body)
	}
	var created Endpoint
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !strings.HasPrefix(created.Secret, "whsec_") {
		t.Fatalf("create did not carry the signing secret: %s", body)
	}
	for _, path := range []string{"/v1/webhook", "/v1/webhook/" + created.ID} {
		_, got := call(t, app, http.MethodGet, path, "acme", true, nil)
		if strings.Contains(string(got), "whsec_") {
			t.Errorf("GET %s leaked a signing secret: %s", path, got)
		}
	}
	code, body = call(t, app, http.MethodPost, "/v1/webhook/"+created.ID+"/secret", "acme", true, nil)
	if code != http.StatusOK {
		t.Fatalf("rotate want 200, got %d (%s)", code, body)
	}
	var rotated Endpoint
	if err := json.Unmarshal(body, &rotated); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !strings.HasPrefix(rotated.Secret, "whsec_") || rotated.Secret == created.Secret {
		t.Fatalf("rotate did not mint and reveal a NEW secret: %s", body)
	}
}

// TestListEnvelopesAndDelete pin the two Outs that replaced a map[string]any and
// the one status a nil Out carries. A struct drops any key its fields do not
// name, so the empty answers are asserted as BYTES.
func TestListEnvelopesAndDelete(t *testing.T) {
	app := mountApp(t)
	code, body := call(t, app, http.MethodGet, "/v1/webhook", "acme", true, nil)
	if code != http.StatusOK || strings.TrimSpace(string(body)) != `{"data":[]}` {
		t.Fatalf("empty list answers %d %s, want 200 {\"data\":[]}", code, body)
	}
	_, body = call(t, app, http.MethodPost, "/v1/webhook", "acme", true,
		[]byte(`{"url":"https://acme.example/hook"}`))
	var e Endpoint
	if err := json.Unmarshal(body, &e); err != nil {
		t.Fatalf("decode: %v", err)
	}
	code, body = call(t, app, http.MethodGet, "/v1/webhook/"+e.ID+"/deliveries", "acme", true, nil)
	if code != http.StatusOK || strings.TrimSpace(string(body)) != `{"data":[]}` {
		t.Fatalf("empty delivery log answers %d %s, want 200 {\"data\":[]}", code, body)
	}
	code, body = call(t, app, http.MethodDelete, "/v1/webhook/"+e.ID, "acme", true, nil)
	if code != http.StatusNoContent {
		t.Fatalf("delete want 204, got %d (%s)", code, body)
	}
	if len(bytes.TrimSpace(body)) != 0 {
		t.Fatalf("delete answered a body: %s", body)
	}
	if code, got := call(t, app, http.MethodDelete, "/v1/webhook/"+e.ID, "acme", true, nil); code != http.StatusNotFound {
		t.Fatalf("second delete want 404, got %d (%s)", code, got)
	}
}

func sortedKeys(m map[string]bool) string {
	return strings.Join(slices.Sorted(maps.Keys(m)), ", ")
}

// TestEveryPublishedFieldIsDescribed closes the half of the surface the op-level
// gates cannot see. Typing a route documents its ADDRESS and its SHAPE; the shape's
// FIELDS come from a doc comment on each one, which zipdoc lifts per field.
//
// Three facts here are load-bearing and invisible from the names. `secret` leaves
// the server exactly ONCE, on create — a caller who plans to read it back later
// finds it gone. An `httpStatus` of 0 is not a 200: it means the subscriber never
// answered at all. And `deliveries7d`/`failures7d` count SETTLED deliveries, so a
// delivery still climbing the retry ladder is in neither, which is why a busy
// endpoint can show zero of both.
//
// Presence is all a gate can check. A description restating the field's name is
// worse than none, and only a reader catches that.
func TestEveryPublishedFieldIsDescribed(t *testing.T) {
	doc, err := openapi.Spec(mountApp(t), openapi.Info{Title: "webhook", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	if doc.Components == nil || len(doc.Components.Schemas) == 0 {
		t.Fatal("webhooks publishes no schemas at all — the gate would pass vacuously")
	}
	bare, err := openapi.Bare(doc)
	if err != nil {
		t.Fatalf("bare: %v", err)
	}
	if len(bare) > 0 {
		t.Errorf("%d published schema propert(ies) with no description: %s\n"+
			"Write the field's OWN doc comment — a header above a group of fields is lifted onto "+
			"the first of them alone — then run: make -C apps/webhooks describe",
			len(bare), strings.Join(bare, ", "))
	}
}
