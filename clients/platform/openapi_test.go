package platform

import (
	"testing"

	"github.com/hanzoai/cloud/openapi"
)

// The platform surface's declared bodies must render in the live projection —
// from the REAL mounted routes, so a typo'd path in the init registration (or a
// route rename that strands it) fails here instead of silently dropping the
// schema from the published document.
func TestOpenAPICarriesPlatformBodies(t *testing.T) {
	doc, err := openapi.Spec(mountApp(t), openapi.Info{Title: "t", Version: "v1"})
	if err != nil {
		t.Fatalf("Spec: %v", err)
	}

	ref := func(s *openapi.Schema) string {
		if s == nil {
			return ""
		}
		return s.Ref
	}

	// Operation.RequestBody / .Responses and Components.Schemas are `any` by
	// design: two seams (Register and the typed fold) produce different, JSON
	// -identical shapes, so the document model refuses to pick one. This test
	// only ever exercises the REGISTER seam — the platform surface declares its
	// bodies explicitly — so it narrows to Register's concrete types here, once,
	// and a shape it did not build fails loudly instead of compiling to a wrong
	// field access.
	reqBody := func(t *testing.T, op *openapi.Operation) *openapi.RequestBody {
		t.Helper()
		if op == nil || op.RequestBody == nil {
			return nil
		}
		b, ok := op.RequestBody.(*openapi.RequestBody)
		if !ok {
			t.Fatalf("requestBody is %T, want *openapi.RequestBody (the Register seam)", op.RequestBody)
		}
		return b
	}
	resp := func(t *testing.T, op *openapi.Operation, status string) *openapi.Response {
		t.Helper()
		if op == nil || op.Responses == nil {
			return nil
		}
		m, ok := op.Responses.(map[string]*openapi.Response)
		if !ok {
			t.Fatalf("responses is %T, want map[string]*openapi.Response (the Register seam)", op.Responses)
		}
		return m[status]
	}
	// bodySchema is the one thing every assertion below wants: the JSON schema a
	// request or response body binds.
	media := func(c map[string]openapi.Media) *openapi.Schema { return c["application/json"].Schema }
	component := func(t *testing.T, name string) *openapi.Schema {
		t.Helper()
		v := doc.Components.Schemas[name]
		if v == nil {
			return nil
		}
		s, ok := v.(*openapi.Schema)
		if !ok {
			t.Fatalf("component %s is %T, want *openapi.Schema", name, v)
		}
		return s
	}

	// POST .../apps: requestBody is createAppReq, success body is appView.
	post := doc.Paths["/v1/platform/projects/{project}/apps"]["post"]
	if post == nil {
		t.Fatal("missing POST /v1/platform/projects/{project}/apps")
	}
	if b := reqBody(t, post); b == nil || ref(media(b.Content)) != "#/components/schemas/createAppReq" {
		t.Fatalf("requestBody = %+v, want $ref createAppReq", post.RequestBody)
	}
	if r := resp(t, post, "2XX"); r == nil || ref(media(r.Content)) != "#/components/schemas/appView" {
		t.Fatalf("responses = %+v, want 2XX $ref appView", post.Responses)
	}

	// Both sides of the contract name storageGb — the stateful-app field SDKs
	// were generating untyped before this seam existed.
	for _, name := range []string{"createAppReq", "appView"} {
		s := component(t, name)
		if s == nil {
			t.Fatalf("missing component %s", name)
		}
		p := s.Properties["storageGb"]
		if p == nil || p.Type != "integer" {
			t.Errorf("%s.storageGb = %+v, want integer", name, p)
		}
	}
	// And the rest of the declared platform shapes exist as components.
	for _, name := range []string{"projectView", "setEnvReq", "runReq", "runView", "EnvVarJSON"} {
		if component(t, name) == nil {
			t.Errorf("missing component %s", name)
		}
	}

	// PUT .../env binds setEnvReq and serves appView back.
	put := doc.Paths["/v1/platform/projects/{project}/apps/{app}/env"]["put"]
	if b := reqBody(t, put); b == nil || ref(media(b.Content)) != "#/components/schemas/setEnvReq" {
		t.Fatalf("PUT env = %+v, want requestBody $ref setEnvReq", put)
	}

	// POST /v1/run: runReq in, runView out.
	run := doc.Paths["/v1/run"]["post"]
	if b := reqBody(t, run); b == nil || ref(media(b.Content)) != "#/components/schemas/runReq" {
		t.Fatalf("POST /v1/run = %+v, want requestBody $ref runReq", run)
	}
	if r := resp(t, run, "2XX"); r == nil || ref(media(r.Content)) != "#/components/schemas/runView" {
		t.Fatalf("POST /v1/run responses = %+v, want 2XX $ref runView", run.Responses)
	}

	// Project reads: list is an array of projectView, get is one.
	list := doc.Paths["/v1/platform/projects"]["get"]
	lr := resp(t, list, "2XX")
	if list == nil || lr == nil {
		t.Fatal("GET /v1/platform/projects lost its declared response")
	}
	if s := media(lr.Content); s == nil || s.Type != "array" || s.Items == nil || s.Items.Ref != "#/components/schemas/projectView" {
		t.Fatalf("project list schema = %+v, want array of $ref projectView", s)
	}

	// NEGATIVE: a live route with no registration still renders, bare — the
	// mechanism is additive and cannot break the rest of the document.
	del := doc.Paths["/v1/platform/projects/{project}/apps/{app}"]["delete"]
	if del == nil {
		t.Fatal("unregistered DELETE route missing from the document")
	}
	if del.RequestBody != nil || del.Responses != nil {
		t.Errorf("unregistered route grew bodies it never declared: %+v", del)
	}
}
