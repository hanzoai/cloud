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

	// POST .../apps: requestBody is createAppReq, success body is appView.
	post := doc.Paths["/v1/platform/projects/{project}/apps"]["post"]
	if post == nil {
		t.Fatal("missing POST /v1/platform/projects/{project}/apps")
	}
	if post.RequestBody == nil || ref(post.RequestBody.Content["application/json"].Schema) != "#/components/schemas/createAppReq" {
		t.Fatalf("requestBody = %+v, want $ref createAppReq", post.RequestBody)
	}
	if r := post.Responses["2XX"]; r == nil || ref(r.Content["application/json"].Schema) != "#/components/schemas/appView" {
		t.Fatalf("responses = %+v, want 2XX $ref appView", post.Responses)
	}

	// Both sides of the contract name storageGb — the stateful-app field SDKs
	// were generating untyped before this seam existed.
	for _, name := range []string{"createAppReq", "appView"} {
		s := doc.Components.Schemas[name]
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
		if doc.Components.Schemas[name] == nil {
			t.Errorf("missing component %s", name)
		}
	}

	// PUT .../env binds setEnvReq and serves appView back.
	put := doc.Paths["/v1/platform/projects/{project}/apps/{app}/env"]["put"]
	if put == nil || put.RequestBody == nil || ref(put.RequestBody.Content["application/json"].Schema) != "#/components/schemas/setEnvReq" {
		t.Fatalf("PUT env = %+v, want requestBody $ref setEnvReq", put)
	}

	// POST /v1/run: runReq in, runView out.
	run := doc.Paths["/v1/run"]["post"]
	if run == nil || run.RequestBody == nil || ref(run.RequestBody.Content["application/json"].Schema) != "#/components/schemas/runReq" {
		t.Fatalf("POST /v1/run = %+v, want requestBody $ref runReq", run)
	}
	if r := run.Responses["2XX"]; r == nil || ref(r.Content["application/json"].Schema) != "#/components/schemas/runView" {
		t.Fatalf("POST /v1/run responses = %+v, want 2XX $ref runView", run.Responses)
	}

	// Project reads: list is an array of projectView, get is one.
	list := doc.Paths["/v1/platform/projects"]["get"]
	if list == nil || list.Responses["2XX"] == nil {
		t.Fatal("GET /v1/platform/projects lost its declared response")
	}
	if s := list.Responses["2XX"].Content["application/json"].Schema; s.Type != "array" || s.Items == nil || s.Items.Ref != "#/components/schemas/projectView" {
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
