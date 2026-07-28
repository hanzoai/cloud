package openapi

import (
	"strings"
	"testing"
)

// unregister removes a test registration so the package-level registry stays
// clean across tests. Test-only: production registrations are for life.
func unregister(path, method string) {
	regMu.Lock()
	delete(registry, opKey{method: strings.ToUpper(method), path: path})
	regMu.Unlock()
}

type widgetReq struct {
	Name      string   `json:"name"`
	StorageGB int      `json:"storageGb"`
	Tags      []string `json:"tags"`
	Nested    struct {
		URL string `json:"url"`
	} `json:"nested"`
	hidden  string // unexported: must NOT appear in the schema
	Skipped string `json:"-"` // explicitly excluded
}

type widgetView struct {
	ID        string `json:"id"`
	StorageGB int    `json:"storageGb,omitempty"`
}

// A registered route renders its declared bodies: requestBody and a 2XX
// response, both by $ref to components reflected from the Go structs.
func TestRegisterMergesDeclaredBodies(t *testing.T) {
	Register("/v1/widgets/:id", "POST", widgetReq{}, widgetView{})
	t.Cleanup(func() { unregister("/v1/widgets/:id", "POST") })

	doc, err := From([]Route{{Method: "POST", Path: "/v1/widgets/:id"}}, Info{Title: "t", Version: "v1"})
	if err != nil {
		t.Fatalf("From: %v", err)
	}
	op := doc.Paths["/v1/widgets/{id}"]["post"]
	if op == nil {
		t.Fatal("missing operation")
	}
	if op.RequestBody == nil || op.RequestBody.Content["application/json"].Schema.Ref != "#/components/schemas/widgetReq" {
		t.Fatalf("requestBody = %+v, want $ref widgetReq", op.RequestBody)
	}
	resp := op.Responses["2XX"]
	if resp == nil || resp.Content["application/json"].Schema.Ref != "#/components/schemas/widgetView" {
		t.Fatalf("responses = %+v, want 2XX $ref widgetView", op.Responses)
	}

	req := doc.Components.Schemas["widgetReq"]
	if req == nil || req.Type != "object" {
		t.Fatalf("widgetReq component = %+v", req)
	}
	if p := req.Properties["storageGb"]; p == nil || p.Type != "integer" {
		t.Errorf("storageGb = %+v, want integer", p)
	}
	if p := req.Properties["tags"]; p == nil || p.Type != "array" || p.Items == nil || p.Items.Type != "string" {
		t.Errorf("tags = %+v, want array of string", p)
	}
	if p := req.Properties["nested"]; p == nil || p.Type != "object" || p.Properties["url"] == nil {
		t.Errorf("nested = %+v, want inline object with url", p)
	}
	if _, leaked := req.Properties["hidden"]; leaked {
		t.Error("unexported field leaked into the schema; encoding/json never emits it")
	}
	if _, leaked := req.Properties["-"]; leaked {
		t.Error(`json:"-" field leaked into the schema`)
	}
	if _, leaked := req.Properties["Skipped"]; leaked {
		t.Error(`json:"-" field leaked into the schema under its Go name`)
	}
}

// The NEGATIVES that keep the mechanism one-way:
//   - a route with no registration renders exactly as before — no requestBody,
//     no responses, no panic, and no components block appears for it;
//   - a registration whose route is NOT in the router renders NOTHING — the
//     registry cannot add a path or a schema the router does not carry.
func TestUnregisteredRouteAndOrphanRegistrationBothStayHonest(t *testing.T) {
	Register("/v1/ghosts", "POST", widgetReq{}, widgetView{})
	t.Cleanup(func() { unregister("/v1/ghosts", "POST") })

	// The route table carries only an UNregistered route.
	doc, err := From([]Route{{Method: "GET", Path: "/v1/plain"}}, Info{Title: "t", Version: "v1"})
	if err != nil {
		t.Fatalf("From: %v", err)
	}
	op := doc.Paths["/v1/plain"]["get"]
	if op == nil {
		t.Fatal("unregistered route must still render")
	}
	if op.RequestBody != nil || op.Responses != nil {
		t.Errorf("unregistered route grew bodies: %+v", op)
	}
	if doc.Components != nil {
		t.Errorf("orphan registration leaked components: %+v — schemas must attach only to live routes", doc.Components)
	}
	if _, ok := doc.Paths["/v1/ghosts"]; ok {
		t.Error("orphan registration minted a path — the registry must never add routes")
	}
}

// A duplicate Register for one (method, path) is an init-time programming error
// and must refuse loudly, not let two declarations race for one operation.
func TestRegisterRefusesDuplicate(t *testing.T) {
	Register("/v1/dups", "POST", widgetReq{}, nil)
	t.Cleanup(func() { unregister("/v1/dups", "POST") })
	defer func() {
		if recover() == nil {
			t.Fatal("second Register for the same (method, path) must panic")
		}
	}()
	Register("/v1/dups", "POST", widgetReq{}, nil)
}

// Two DIFFERENT Go types claiming one component name would hand an SDK
// generator a silently merged lie; From must refuse instead.
func TestFromRefusesComponentNameCollision(t *testing.T) {
	type collide struct { // local type: same name as the sibling below
		A string `json:"a"`
	}
	first := collide{}
	second := func() any {
		type collide struct {
			B int `json:"b"`
		}
		return collide{}
	}()

	Register("/v1/collide/a", "POST", first, nil)
	Register("/v1/collide/b", "POST", second, nil)
	t.Cleanup(func() {
		unregister("/v1/collide/a", "POST")
		unregister("/v1/collide/b", "POST")
	})

	_, err := From([]Route{
		{Method: "POST", Path: "/v1/collide/a"},
		{Method: "POST", Path: "/v1/collide/b"},
	}, Info{Title: "t", Version: "v1"})
	if err == nil || !strings.Contains(err.Error(), "collide") {
		t.Fatalf("From = %v, want a component-name collision error naming %q", err, "collide")
	}
}

// One view type shared by several routes (the platform appView shape) must
// yield ONE component, referenced from each — not an error, not a copy.
func TestSharedViewYieldsOneComponent(t *testing.T) {
	Register("/v1/shared", "GET", nil, widgetView{})
	Register("/v1/shared/list", "GET", nil, []widgetView{})
	t.Cleanup(func() {
		unregister("/v1/shared", "GET")
		unregister("/v1/shared/list", "GET")
	})

	doc, err := From([]Route{
		{Method: "GET", Path: "/v1/shared"},
		{Method: "GET", Path: "/v1/shared/list"},
	}, Info{Title: "t", Version: "v1"})
	if err != nil {
		t.Fatalf("From: %v", err)
	}
	if n := len(doc.Components.Schemas); n != 1 {
		t.Fatalf("components = %d, want exactly one widgetView", n)
	}
	list := doc.Paths["/v1/shared/list"]["get"].Responses["2XX"].Content["application/json"].Schema
	if list.Type != "array" || list.Items == nil || list.Items.Ref != "#/components/schemas/widgetView" {
		t.Fatalf("list schema = %+v, want array of $ref widgetView", list)
	}
}
