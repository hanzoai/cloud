package tools

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/openapi"
)

// This file makes the tool plane's typed partition a GATE instead of a paragraph.
// "all but one" is prose, and prose cannot fail: a route added tomorrow as a raw
// func(*zip.Ctx) error would leave the claim standing and the route invisible to
// every projection — no schema, no description, no MCP tool, no CLI command, no
// SDK method. Here the claim is a test, so the route that falsifies it says so.

// untypedByDesign is the CLOSED list of tool-plane operations that are NOT typed
// ops, each with the wire fact that keeps it raw. The address is written the way
// the DOCUMENT writes it, which is the identity every projection keys on.
//
// It holds ONE entry now. The other was the hand-rolled MCP JSON-RPC surface at
// POST /v1/tools/mcp, and it is gone rather than typed: the fleet serves ONE MCP
// door, on the host, and this plane reaches it as a typed op (POST /v1/tools/call)
// like everything else. A JSON-RPC envelope is a transport, and there is now
// exactly one place in the fleet that speaks it.
//
// The remaining entry is wire-bound and was re-read against the PINNED zip
// (v1.18.14), not inherited as prose from an older pass.
var untypedByDesign = map[string]string{
	// The plugin builder. A FAILED build answers 422 carrying the build
	// DIAGNOSTICS as a domain body — the bundler's error, the source that failed,
	// and whether the model wrote it — which is the only thing that lets a caller
	// fix the plugin. A typed op can refuse only by RETURNING an error, which zip
	// renders as the flat HTTPError {status, code, error} (errorHandler is the only
	// path a typed op's error can take), and there is nowhere in that shape for the
	// source. Writing the body from inside the op does not escape it either: a nil
	// Out makes zip stamp cmp.Or(op.Status, 204) over the 422 (typed.go:302-306).
	// So this is a 201-or-422 pair of DIFFERENT shapes and zip has one Out and one
	// declared status per op — the multi-status gap (#78), not an oversight.
	// TestBuildFailureCarriesItsDiagnostics pins the 422 body.
	"POST /v1/plugins/build": "a failed build answers 422 carrying the build diagnostics (detail, source, " +
		"generated) as a domain body; a typed op's only refusal is a returned error, which zip renders as the " +
		"flat HTTPError with nowhere to put them — one Out and one declared status per op.",
}

// toolPlaneOps reads BOTH projections of the live router at their one shared
// address form: what the document says is served, and which of those carry a
// typed registry entry. EVERY served operation counts, so a route mounted at an
// address nobody expected is caught rather than filtered out.
func toolPlaneOps(t *testing.T) (served map[string]bool, typed map[string]string, schemas map[string]any) {
	t.Helper()
	app := newApp(t, nil)
	doc, err := openapi.Spec(app, openapi.Info{Title: "tools", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	reg, err := openapi.Typed(app)
	if err != nil {
		t.Fatalf("typed registry: %v", err)
	}
	served, typed = map[string]bool{}, map[string]string{}
	for path, item := range doc.Paths {
		for method := range item {
			served[strings.ToUpper(method)+" "+path] = true
		}
	}
	for key, op := range reg.Ops {
		typed[key] = op.Description
	}
	return served, typed, reg.Schemas
}

// TestEveryRouteIsTypedOrNamed fails when a tool-plane operation is neither a
// typed op nor named above — so the next route added here is typed by default,
// and dropping one out of the registry takes a deliberate edit carrying a reason.
func TestEveryRouteIsTypedOrNamed(t *testing.T) {
	served, typed, _ := toolPlaneOps(t)

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
			"A route that is not a typed op has no schema, no prose, no MCP tool, no CLI command and no SDK "+
			"method. Convert it (zip.Get/Post/... on the /v1 group in routes()), or add it to untypedByDesign "+
			"with the reason typing it would move the wire.", strings.Join(untyped, ", "))
	}
	// The reasons must describe operations that exist, or the list is stale prose.
	for key := range untypedByDesign {
		if !served[key] {
			t.Errorf("untypedByDesign names %q, which the tool plane no longer serves", key)
		}
		if _, ok := typed[key]; ok {
			t.Errorf("untypedByDesign names %q, which IS a typed op — delete the entry", key)
		}
	}
	// The two ledgers must SUM to the served surface: neither may quietly shrink.
	if got, want := len(typed)+len(untypedByDesign), len(served); got != want {
		t.Errorf("typed(%d) + named(%d) = %d, served = %d — the ledgers must partition the surface",
			len(typed), len(untypedByDesign), got, want)
	}
	// The MEASURED partition, so "all but one" in the docs cannot drift from the
	// binary. Changing these numbers is a deliberate edit, which is the point.
	if len(served) != 19 || len(typed) != 18 {
		t.Errorf("served = %d (want 19), typed = %d (want 18)", len(served), len(typed))
	}
}

// TestEveryTypedOpIsDescribed proves the lifted prose reached the binary. That
// prose IS the product surface: it becomes the OpenAPI description AND the MCP
// tool description a model reads to pick the tool. zipdoc_gen.go is what carries
// it in, so an op added without regenerating shows up here as a nameless tool.
func TestEveryTypedOpIsDescribed(t *testing.T) {
	_, typed, _ := toolPlaneOps(t)
	if len(typed) == 0 {
		t.Fatal("no typed tool-plane ops in the registry at all")
	}
	for key, desc := range typed {
		if strings.TrimSpace(desc) == "" {
			t.Errorf("%s has no description — run: go generate -run zipdoc ./apps/tools/...", key)
		}
	}
}

// TestEveryPublishedFieldIsDescribed closes the half of the surface the op-level
// gate cannot see. Typing a route documents its ADDRESS and its SHAPE; it does not
// document the shape's FIELDS, and those come from a different place — doc comments
// on the In/Out struct fields, which zipdoc lifts per field. The row types this
// plane publishes (Tool, Skill, MCPServer, AuthoredPlugin, Price) were written as
// stores, not as documents, so every one of their properties would otherwise reach
// openapi.yaml, every generated SDK and every MCP inputSchema bare — `dispatchable`
// an unexplained boolean, `hasSecret` a flag with no statement that the VALUE is
// never returned.
func TestEveryPublishedFieldIsDescribed(t *testing.T) {
	_, _, schemas := toolPlaneOps(t)
	if len(schemas) == 0 {
		t.Fatal("no tool-plane schemas in the typed registry at all")
	}
	var bare []string
	for name, raw := range schemas {
		sch, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		props, ok := sch["properties"].(map[string]any)
		if !ok {
			continue // a scalar or an array schema has no properties to describe
		}
		for field, praw := range props {
			p, ok := praw.(map[string]any)
			if !ok {
				continue
			}
			if desc, _ := p["description"].(string); strings.TrimSpace(desc) == "" {
				bare = append(bare, name+"."+field)
			}
		}
	}
	if len(bare) > 0 {
		sort.Strings(bare)
		t.Errorf("published propert(ies) with no description: %s\n"+
			"Every field of a published schema is read by SDK users and by a model choosing a tool. Write a "+
			"doc comment on the struct field and run: go generate -run zipdoc ./apps/tools/...",
			strings.Join(bare, ", "))
	}
}

// ── the wire the one refusal protects ───────────────────────────────────────────

// TestBuildFailureCarriesItsDiagnostics is the pin under the single entry. A build
// that does not compile answers 422 with the bundler's detail, the source that
// failed and whether a model wrote it — the only thing that lets a caller fix the
// plugin, and a shape zip's flat HTTPError has nowhere to put.
func TestBuildFailureCarriesItsDiagnostics(t *testing.T) {
	app := newApp(t, nil)
	r := do(t, app, http.MethodPost, "/v1/plugins/build", "acme", map[string]any{
		"name": "broken", "source": "export const x = {",
	})
	if r.Code != 422 {
		t.Fatalf("unbuildable source = %d (%s), want 422", r.Code, r.Body)
	}
	var body map[string]any
	if err := json.Unmarshal(r.Body, &body); err != nil {
		t.Fatalf("decode 422 body: %v (%s)", err, r.Body)
	}
	for _, k := range []string{"error", "detail", "source", "generated"} {
		if _, ok := body[k]; !ok {
			t.Errorf("the 422 build body must carry %q; got %s", k, r.Body)
		}
	}
}

// TestTheUntypedRouteStillDeclaresItsBody is the OTHER half of a refusal.
// Staying out of zip's registry costs prose, an MCP tool, a CLI command and a
// typed SDK method — it must not also cost the SHAPE. This route rendered as an
// operationId and a tag and nothing else, which is precisely what a route taking
// no input and returning none publishes, so no consumer of the document could
// tell "takes a plugin source" from "takes nothing". openapi.Register (tools.go's
// init) states the half that IS statable; this is the gate that it stays stated.
func TestTheUntypedRouteStillDeclaresItsBody(t *testing.T) {
	app := newApp(t, nil)
	doc, err := openapi.Spec(app, openapi.Info{Title: "tools", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	// Operation.RequestBody and .Responses are `any` — the two seams build
	// JSON-identical but differently-typed shapes — so read the operation the way
	// every consumer does, through the marshalled document.
	type media struct {
		Schema struct {
			Ref string `json:"$ref"`
		} `json:"schema"`
	}
	for _, c := range []struct{ path, req, resp string }{
		{"/v1/plugins/build", "buildRequest", "buildOut"},
	} {
		raw, err := json.Marshal(doc.Paths[c.path]["post"])
		if err != nil {
			t.Fatalf("marshal POST %s: %v", c.path, err)
		}
		var op struct {
			RequestBody *struct {
				Content map[string]media `json:"content"`
			} `json:"requestBody"`
			Responses map[string]struct {
				Content map[string]media `json:"content"`
			} `json:"responses"`
		}
		if err := json.Unmarshal(raw, &op); err != nil {
			t.Fatalf("decode POST %s: %v (%s)", c.path, err, raw)
		}
		if op.RequestBody == nil {
			t.Errorf("POST %s declares no request body — openapi.Register is what tells an SDK "+
				"this route takes one; without it the document says it takes nothing", c.path)
		} else if got := op.RequestBody.Content["application/json"].Schema.Ref; got != "#/components/schemas/"+c.req {
			t.Errorf("POST %s request schema = %q, want the %s component", c.path, got, c.req)
		}
		if got := op.Responses["2XX"].Content["application/json"].Schema.Ref; got != "#/components/schemas/"+c.resp {
			t.Errorf("POST %s success response schema = %q, want the %s component", c.path, got, c.resp)
		}
	}
	// AuthoredPlugin is claimed by BOTH seams — the untyped declaration above and
	// the typed listAuthoredPlugins. Fold merges the typed schema over the other,
	// so the shared row keeps the prose zipdoc lifted; a regression here would
	// silently strip every description off it.
	//
	// Read it the way every consumer does — through the marshalled document —
	// rather than by type-asserting whichever seam happened to write the value.
	raw, err := json.Marshal(doc.Components.Schemas["AuthoredPlugin"])
	if err != nil {
		t.Fatalf("marshal AuthoredPlugin component: %v", err)
	}
	var ap struct {
		Properties map[string]struct {
			Description string `json:"description"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(raw, &ap); err != nil {
		t.Fatalf("decode AuthoredPlugin component: %v (%s)", err, raw)
	}
	if strings.TrimSpace(ap.Properties["org"].Description) == "" {
		t.Errorf("AuthoredPlugin.org lost its description — the untyped declaration must not "+
			"overwrite the typed schema for a component both seams name; got %s", raw)
	}
}

// TestBuildReceiptIsByteIdentical pins the builder's 201 body, which was a Go map
// — encoding/json writes a map in SORTED KEY order, so buildOut's fields are
// alphabetical. Asserting the marshalled BYTES is what proves naming the shape did
// not move the wire; a status-code test would pass either way.
func TestBuildReceiptIsByteIdentical(t *testing.T) {
	stored := AuthoredPlugin{ID: "p1", Org: "acme", Name: "hello", Source: "export default {}", CreatedAt: 42}
	got, err := json.Marshal(buildOut{Bytes: 12, Generated: true, Plugin: stored})
	if err != nil {
		t.Fatalf("marshal struct: %v", err)
	}
	want, err := json.Marshal(map[string]any{"plugin": stored, "generated": true, "bytes": 12})
	if err != nil {
		t.Fatalf("marshal map: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("build receipt moved:\n got %s\nwant %s", got, want)
	}
}

// ── the wire the typed ops kept ─────────────────────────────────────────────────

// TestActivatedFilterIsTheLiteralTrue pins why the three filter fields are STRINGS
// and not bools. These routes have always compared the raw query value to "true",
// so `?activated=1` and a bare `?activated` mean NO filter. zip's bindURL reads
// both as true for a bool field (setScalar: an empty value is "flag present"), so
// a bool In would silently return a different set of tools for the same URL.
func TestActivatedFilterIsTheLiteralTrue(t *testing.T) {
	app := newApp(t, nil)
	std.Register(&fakeProvider{src: SourceConnector, tools: []Tool{tool("acme_hello", SourceConnector)}})

	// Count the ONE unactivated tool the fake source contributes — the assertion is
	// on the tool under test and not on a total, so a source registered by some
	// other part of this mount cannot move it.
	count := func(path string) int {
		r := do(t, app, http.MethodGet, path, "acme", nil)
		if r.Code != 200 {
			t.Fatalf("GET %s = %d (%s)", path, r.Code, r.Body)
		}
		var out struct {
			Tools []Tool `json:"tools"`
		}
		if err := json.Unmarshal(r.Body, &out); err != nil {
			t.Fatalf("decode %s: %v (%s)", path, err, r.Body)
		}
		n := 0
		for _, tl := range out.Tools {
			if tl.Name == "acme_hello" {
				n++
			}
		}
		return n
	}
	// Nothing is activated, so only ?activated=true may filter the tool away.
	if n := count("/v1/tools"); n != 1 {
		t.Fatalf("GET /v1/tools carries acme_hello %d times, want 1", n)
	}
	if n := count("/v1/tools?activated=true"); n != 0 {
		t.Errorf("?activated=true must filter to the activated set, still saw acme_hello %d times", n)
	}
	for _, q := range []string{"?activated", "?activated=1", "?activated=TRUE", "?activated=yes"} {
		if n := count("/v1/tools" + q); n != 1 {
			t.Errorf("GET /v1/tools%s carries acme_hello %d times, want 1 — only the literal \"true\" filters", q, n)
		}
	}
}

// TestServerLifecycleKeepsItsStatuses pins the two statuses a naive conversion
// loses: a create is 201 with the stored record, and a delete is 204 with NO body
// (zip answers 204 only for a nil Out of an unnamed type), then 404 once gone.
func TestServerLifecycleKeepsItsStatuses(t *testing.T) {
	app := newApp(t, nil)
	create := do(t, app, http.MethodPost, "/v1/mcp/servers", "acme", map[string]any{
		"name": "myserver", "url": "https://mcp.example.com/rpc",
	})
	if create.Code != 201 {
		t.Fatalf("create server = %d (%s), want 201", create.Code, create.Body)
	}
	var made MCPServer
	if err := json.Unmarshal(create.Body, &made); err != nil {
		t.Fatalf("decode created server: %v (%s)", err, create.Body)
	}
	if made.ID == "" || made.Org != "acme" || made.HasSecret {
		t.Fatalf("created server = %+v, want an id, org acme and no secret", made)
	}

	// Another tenant cannot delete it: the org comes from the validated principal,
	// never from the URL, so this is a 404 and not a cross-tenant delete.
	if r := do(t, app, http.MethodDelete, "/v1/mcp/servers/"+made.ID, "evil", nil); r.Code != 404 {
		t.Fatalf("cross-tenant delete = %d (%s), want 404", r.Code, r.Body)
	}
	del := do(t, app, http.MethodDelete, "/v1/mcp/servers/"+made.ID, "acme", nil)
	if del.Code != 204 || len(del.Body) != 0 {
		t.Fatalf("delete server = %d body %q, want 204 with no body", del.Code, del.Body)
	}
	if r := do(t, app, http.MethodDelete, "/v1/mcp/servers/"+made.ID, "acme", nil); r.Code != 404 {
		t.Errorf("second delete = %d (%s), want 404", r.Code, r.Body)
	}
}

// TestSkillWriteIsTenantedByThePrincipalNotTheBody is the identity pin. The org is
// read from the validated principal cloud.Bridge parked, NEVER from an In field —
// an In field is caller-supplied, so a tenant key read from one would be a
// cross-tenant write the caller asserted for itself. A body claiming another org
// is stored under the caller's, and the other org cannot see it.
func TestSkillWriteIsTenantedByThePrincipalNotTheBody(t *testing.T) {
	app := newApp(t, nil)
	w := do(t, app, http.MethodPost, "/v1/skills", "acme", map[string]any{
		"org": "evil", "id": "smuggled", "name": "triage", "content": "# Triage", "createdAt": 1,
	})
	if w.Code != 201 {
		t.Fatalf("put skill = %d (%s), want 201", w.Code, w.Body)
	}
	var made struct {
		Skill Skill `json:"skill"`
	}
	if err := json.Unmarshal(w.Body, &made); err != nil {
		t.Fatalf("decode skill: %v (%s)", err, w.Body)
	}
	// The body's org, id and createdAt are all ignored, exactly as before typing.
	if made.Skill.Org != "acme" || made.Skill.ID != "triage" || made.Skill.CreatedAt == 1 {
		t.Fatalf("stored skill = %+v, want org acme, id triage and a server-stamped time", made.Skill)
	}
	mine := do(t, app, http.MethodGet, "/v1/skills/authored", "acme", nil)
	if !strings.Contains(string(mine.Body), "triage") {
		t.Errorf("the author's org must see its skill, got %s", mine.Body)
	}
	theirs := do(t, app, http.MethodGet, "/v1/skills/authored", "evil", nil)
	if strings.Contains(string(theirs.Body), "triage") {
		t.Errorf("the org named in the BODY must not see the skill, got %s", theirs.Body)
	}
	// And the delete is org-scoped the same way.
	if r := do(t, app, http.MethodDelete, "/v1/skills/triage", "acme", nil); r.Code != 200 {
		t.Errorf("delete skill = %d (%s), want 200", r.Code, r.Body)
	}
}

// TestTypedOpsFailClosedWithoutAPrincipal walks EVERY typed op with no validated
// principal. A typed op cannot read the request, so its tenant reaches it only
// through cloud.Bridge — and where that parks nothing, the op must refuse rather
// than run org-less. It is also the pin on the harness: without cloud.Bridge these
// would 403 even WITH an org, and the assertions below would still pass, so the
// tests above (which need a served 200) are the other half of that proof.
func TestTypedOpsFailClosedWithoutAPrincipal(t *testing.T) {
	app := newApp(t, nil)
	for _, c := range []struct {
		method, path string
		body         any
	}{
		{http.MethodGet, "/v1/tools", nil},
		{http.MethodGet, "/v1/tools/activation", nil},
		{http.MethodPut, "/v1/tools/activation", map[string]any{"activate": []string{"x"}}},
		{http.MethodGet, "/v1/skills", nil},
		{http.MethodPost, "/v1/skills", map[string]any{"name": "x", "content": "y"}},
		{http.MethodGet, "/v1/skills/authored", nil},
		{http.MethodDelete, "/v1/skills/x", nil},
		{http.MethodPost, "/v1/tools/call", map[string]any{"name": "x", "arguments": map[string]any{}}},
		{http.MethodGet, "/v1/mcp/servers", nil},
		{http.MethodPost, "/v1/mcp/servers", map[string]any{"name": "x", "url": "https://mcp.example.com"}},
		{http.MethodDelete, "/v1/mcp/servers/x", nil},
		{http.MethodGet, "/v1/plugins", nil},
		{http.MethodGet, "/v1/plugins/authored", nil},
		{http.MethodDelete, "/v1/plugins/authored/x", nil},
	} {
		if r := do(t, app, c.method, c.path, "", c.body); r.Code != 403 {
			t.Errorf("%s %s with no principal = %d (%s), want 403", c.method, c.path, r.Code, r.Body)
		}
	}
}
