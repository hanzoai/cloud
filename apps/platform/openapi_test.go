package platform

// The /v1/platform surface is ONE registry with N projections. These tests pin
// that: every route is a typed op, every op reaches the OpenAPI document, and the
// bodies and responses the surface declares are the ones the handlers actually
// bind and serve.
//
// They read the document back through JSON — which is how every consumer reads it
// — rather than through the emitter's in-memory Go types. That is deliberate, and
// it is what this file learned: it used to narrow to openapi.Register's concrete
// types, because the surface declared its bodies through that client and the typed
// fold builds different Go values for the same JSON. Asserting on the client meant
// asserting on WHO built the document, so converting these routes to typed ops
// broke every assertion here without one byte of the published contract changing.
// JSON is the contract; the client is an implementation detail.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/openapi"
)

// published mounts the surface and returns the document as JSON, plus the live
// route table fiber matches on.
func published(t *testing.T) (map[string]any, []string) {
	t.Helper()
	app := mountApp(t)

	var live []string
	for _, r := range app.Fiber().GetRoutes(true) {
		if r.Method == "HEAD" || r.Method == "OPTIONS" {
			continue
		}
		live = append(live, r.Method+" "+r.Path)
	}

	spec, err := openapi.Spec(app, openapi.Info{Title: "t", Version: "v1"})
	if err != nil {
		t.Fatalf("Spec: %v", err)
	}
	raw, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("document is not JSON: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("document does not decode: %v", err)
	}
	return out, live
}

// at walks a JSON document by key, returning nil at the first missing step so a
// failed lookup reports the path it wanted instead of panicking.
func at(v any, keys ...string) any {
	for _, k := range keys {
		m, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		v = m[k]
	}
	return v
}

// schemaRef is the $ref an operation's request or response body binds, or "".
func schemaRef(v any) string {
	s, _ := at(v, "$ref").(string)
	return s
}

// TestEveryRouteIsATypedOp is the invariant this conversion exists to establish:
// the document describes the router exactly. A route the registry does not know is
// a route no projection can serve — it still returns bytes, so nothing fails until
// somebody goes looking for it in an SDK that has no method for it.
func TestEveryRouteIsATypedOp(t *testing.T) {
	doc, live := published(t)

	described := map[string]bool{}
	for path, item := range doc["paths"].(map[string]any) {
		for method := range item.(map[string]any) {
			// The document writes {name} where fiber writes :name.
			fiberPath := path
			for seg := range strings.SplitSeq(path, "/") {
				if strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}") {
					fiberPath = strings.Replace(fiberPath, seg, ":"+strings.Trim(seg, "{}"), 1)
				}
			}
			described[strings.ToUpper(method)+" "+fiberPath] = true
		}
	}

	for _, r := range live {
		if !described[r] {
			t.Errorf("%s is served but is not in the OpenAPI document — it is a raw handler, "+
				"invisible to OpenAPI, MCP, the CLI and every generated SDK. Register it with "+
				"zip.Get[In, Out] and friends.", r)
		}
	}
	if len(live) == 0 {
		t.Fatal("no platform routes registered")
	}
	t.Logf("%d routes, all typed ops", len(live))
}

// TestOpenAPICarriesPlatformBodies is the contract itself: each declared body and
// response is the exact struct the matching handler binds or serves. It asserts
// the same paths and the same schema names it always did.
func TestOpenAPICarriesPlatformBodies(t *testing.T) {
	doc, _ := published(t)
	paths := doc["paths"].(map[string]any)
	schemas := at(doc, "components", "schemas").(map[string]any)

	body := func(op any) any { return at(op, "requestBody", "content", "application/json", "schema") }
	// answer is the success body at the code the op DECLARED — 201 for a create,
	// 202 for an accept. Asking for the real code rather than assuming 200 is the
	// whole point of declaring it: a generated client expects what the route sends.
	answer := func(op any, code string) any {
		return at(op, "responses", code, "content", "application/json", "schema")
	}

	// POST .../apps: requestBody is createAppReq, and the 201 body is appView.
	post := at(paths, "/v1/platform/projects/{project}/apps", "post")
	if post == nil {
		t.Fatal("missing POST /v1/platform/projects/{project}/apps")
	}
	if got := schemaRef(body(post)); got != "#/components/schemas/createAppReq" {
		t.Errorf("POST apps requestBody = %q, want $ref createAppReq", got)
	}
	if got := schemaRef(answer(post, "201")); got != "#/components/schemas/appView" {
		t.Errorf("POST apps 201 = %q, want $ref appView", got)
	}

	// Both sides of the contract name storageGb — the stateful-app field SDKs were
	// generating untyped before this surface declared its bodies at all.
	for _, name := range []string{"createAppReq", "appView"} {
		if p := at(schemas, name, "properties", "storageGb", "type"); p != "integer" {
			t.Errorf("%s.storageGb type = %v, want integer", name, p)
		}
	}
	// And the rest of the declared platform shapes exist as components.
	for _, name := range []string{"projectView", "setEnvReq", "runReq", "runView", "EnvVarJSON"} {
		if schemas[name] == nil {
			t.Errorf("missing component %s", name)
		}
	}

	// PUT .../env binds setEnvReq and serves appView back.
	put := at(paths, "/v1/platform/projects/{project}/apps/{app}/env", "put")
	if got := schemaRef(body(put)); got != "#/components/schemas/setEnvReq" {
		t.Errorf("PUT env requestBody = %q, want $ref setEnvReq", got)
	}
	if got := schemaRef(answer(put, "200")); got != "#/components/schemas/appView" {
		t.Errorf("PUT env 200 = %q, want $ref appView", got)
	}

	// POST /v1/platform/run: runReq in, runView out, at the 202 the route has always sent
	// and the document could not previously say.
	run := at(paths, "/v1/platform/run", "post")
	if got := schemaRef(body(run)); got != "#/components/schemas/runReq" {
		t.Errorf("POST /v1/platform/run requestBody = %q, want $ref runReq", got)
	}
	if got := schemaRef(answer(run, "202")); got != "#/components/schemas/runView" {
		t.Errorf("POST /v1/platform/run 202 = %q, want $ref runView", got)
	}

	// Project reads: the list is an array of projectView, the get is one of them.
	// A bare array is what the route serves, so a bare array is what it publishes.
	list := at(paths, "/v1/platform/projects", "get")
	ls := answer(list, "200")
	if at(ls, "type") != "array" || schemaRef(at(ls, "items")) != "#/components/schemas/projectView" {
		t.Errorf("project list schema = %v, want array of $ref projectView", ls)
	}
	one := at(paths, "/v1/platform/projects/{project}", "get")
	if got := schemaRef(answer(one, "200")); got != "#/components/schemas/projectView" {
		t.Errorf("project get schema = %q, want $ref projectView", got)
	}

	// DELETE .../apps/{app} answers 204 with NO body. Its Out is the unnamed empty
	// struct precisely so the document says that, rather than describing a body the
	// route has never sent.
	del := at(paths, "/v1/platform/projects/{project}/apps/{app}", "delete")
	if del == nil {
		t.Fatal("DELETE app missing from the document")
	}
	if b := body(del); b != nil {
		t.Errorf("DELETE app grew a request body it never accepted: %v", b)
	}
	if a := answer(del, "200"); a != nil {
		t.Errorf("DELETE app publishes a 200 body; it answers 204 with none: %v", a)
	}
}

// TestPathParametersAreDeclared pins the binding this conversion turns on: a path
// segment reaches a typed handler only by being a field of its In, and the document
// derives its parameter list from the route pattern. If those disagree, a generated
// client sends an argument the handler never reads — which is exactly how a
// converted route silently stops being addressable.
func TestPathParametersAreDeclared(t *testing.T) {
	doc, _ := published(t)

	for path, item := range doc["paths"].(map[string]any) {
		want := map[string]bool{}
		for seg := range strings.SplitSeq(path, "/") {
			if strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}") {
				want[strings.Trim(seg, "{}")] = true
			}
		}
		if len(want) == 0 {
			continue
		}
		for method, op := range item.(map[string]any) {
			got := map[string]bool{}
			params, _ := at(op, "parameters").([]any)
			for _, p := range params {
				if at(p, "in") == "path" {
					name, _ := at(p, "name").(string)
					got[name] = true
				}
			}
			for name := range want {
				if !got[name] {
					t.Errorf("%s %s: path parameter %q is templated but not declared — the In "+
						"struct has no field whose json tag names it, so the segment never binds",
						strings.ToUpper(method), path, name)
				}
			}
		}
	}
}

// TestSchemaRefsResolve guards the one way a typed op produces a BROKEN document: a
// schema name containing "/" splits into extra JSON-Pointer segments, so its $ref
// addresses nothing.
func TestSchemaRefsResolve(t *testing.T) {
	doc, _ := published(t)
	schemas, _ := at(doc, "components", "schemas").(map[string]any)
	raw, _ := json.Marshal(doc)

	for _, m := range strings.Split(string(raw), `"$ref":"`)[1:] {
		name := strings.TrimPrefix(strings.SplitN(m, `"`, 2)[0], "#/components/schemas/")
		if schemas[name] == nil {
			t.Errorf("$ref names %q, which the document does not define", name)
		}
	}
}
