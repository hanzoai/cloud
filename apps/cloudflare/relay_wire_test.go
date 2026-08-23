package cloudflare

// relay_wire_test.go — the contract the typed ops must not move.
//
// Typing this plane was a DESCRIPTION task: every op now carries an In/Out type, so
// it reaches the OpenAPI document, the MCP tool list, the CLI and the SDKs — and the
// bytes on the wire had to stay exactly what they were. These tests pin the two
// halves of that claim: the response is still Cloudflare's own payload, and the set
// of routes that are NOT typed ops is a closed, named list rather than a drift.

import (
	"encoding/json"
	"maps"
	"net/http"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/openapi"
)

// A relayed payload is Cloudflare's, whatever shape it has: an object, an array, a
// JSON null, or an integer too large for a float64 to hold. The last one is the
// one that fails silently if a relay ever decodes into `any` and re-encodes — the
// id comes back off by one — so it is pinned with an exact string match.
func TestRelayIsVerbatim(t *testing.T) {
	for _, tc := range []struct {
		name, result, want string
	}{
		{"object", `{"id":"z1","name":"acme.com"}`, `{"id":"z1","name":"acme.com"}`},
		{"array", `[{"id":"z1"},{"id":"z2"}]`, `[{"id":"z1"},{"id":"z2"}]`},
		{"null", `null`, `null`},
		{"int64 beyond float64", `{"id":9007199254740993}`, `{"id":9007199254740993}`},
		// An envelope with no result at all is the CF delete shape; it relays the
		// success acknowledgement, not a bare null.
		{"no result key", ``, emptyResult},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := &capture{}
			body := `{"success":true,"errors":[]}`
			if tc.result != "" {
				body = `{"success":true,"errors":[],"result":` + tc.result + `}`
			}
			app := harness(t, map[string]string{"acme": "tok"}, rec, func(p string) (int, string) {
				if strings.HasSuffix(p, "/zones") {
					return 200, body
				}
				return 0, ""
			})
			status, got := do(t, app, http.MethodGet, "/v1/cloudflare/zones", "u1", "acme", "")
			if status != http.StatusOK {
				t.Fatalf("status=%d body=%s", status, got)
			}
			if got != tc.want {
				t.Fatalf("relayed %s, want %s", got, tc.want)
			}
		})
	}
}

// The zone id addresses the purge; it must not leak INTO the body Cloudflare
// receives. The op's In carries both the path param and the selector, and only the
// selector is Cloudflare's — proving the In is the caller's request shape and
// PurgeCache is the upstream one.
func TestPurgeBodyCarriesOnlyTheSelector(t *testing.T) {
	const zone = "0123456789abcdef0123456789abcdef"
	rec := &capture{}
	app := harness(t, map[string]string{"acme": "tok"}, rec, nil)
	if code, _, _ := doReq(t, app, http.MethodPost, "/v1/cloudflare/zones/"+zone+"/purge",
		"u1", "acme", true, `{"purge_everything":true}`); code != http.StatusOK {
		t.Fatalf("purge = %d, want 200", code)
	}
	r, ok := rec.find("/purge_cache")
	if !ok {
		t.Fatal("purge did not reach Cloudflare")
	}
	if got := string(r.body); got != `{"purge_everything":true}` {
		t.Fatalf("upstream purge body = %s, want only the selector", got)
	}
}

// A DELETE op takes its target from the URL and carries no request body (zip v1.18+).
// Driving one with a body that names a DIFFERENT resource must not move where it
// lands: the URL is the addressing authority.
func TestDeleteAddressesFromTheURL(t *testing.T) {
	rec := &capture{}
	app := harness(t, map[string]string{"acme": "tok"}, rec, nil)
	if code, _, _ := doReq(t, app, http.MethodDelete, "/v1/cloudflare/r2/buckets/real",
		"u1", "acme", true, `{"bucket":"decoy"}`); code != http.StatusOK {
		t.Fatalf("delete = %d, want 200", code)
	}
	if _, ok := rec.find("/r2/buckets/decoy"); ok {
		t.Fatal("a request body redirected a DELETE away from the bucket its URL named")
	}
	if _, ok := rec.find("/r2/buckets/real"); !ok {
		t.Fatalf("delete did not address the URL's bucket; got %+v", rec.reqs)
	}
}

// untypedByDesign is the CLOSED list of /v1/cloudflare operations that are NOT
// typed ops, each with the reason it cannot be one. A typed op is a route PLUS a
// registry entry — the one value the document, the MCP tool, the CLI command and
// the SDK method all come from — so an operation missing from that registry is
// invisible to all four. These six are missing on purpose. Addresses are written
// the way the DOCUMENT writes them, which is the identity every projection keys on.
var untypedByDesign = map[string]string{
	"POST /v1/cloudflare/ai/run/{wildcard1}": "the request body is whatever the chosen model takes and is " +
		"forwarded verbatim; the response is often not JSON at all (an image or audio model answers bytes " +
		"under Cloudflare's own content type), which a typed op cannot emit.",
	"GET /v1/cloudflare/kv/namespaces/{namespace}/values/{key}": "a KV value is opaque bytes under the " +
		"content type it was written with; a typed op answers JSON.",
	"PUT /v1/cloudflare/kv/namespaces/{namespace}/values/{key}": "the request body IS the stored value, " +
		"under the caller's own content type; a typed In would parse it as JSON.",
	"POST /v1/cloudflare/d1/databases/{database}/query": "the query body is forwarded to D1 VERBATIM so " +
		"params and batch fields survive; a typed In drops every field it does not model.",
	"PUT /v1/cloudflare/workers/scripts/{script}": "the path param `script` (the NAME) and the body field " +
		"`script` (the module SOURCE) collide, and zip's URL binder gives the path the last word.",
	"POST /v1/cloudflare/pages/projects/{project}/deployments": "a body this route cannot parse is IGNORED " +
		"(the deploy falls back to the production branch) where a typed In answers 400.",
}

// cloudflareOps reads BOTH projections of the live router at their one shared
// address form: what the document says is served, and which of those carry a typed
// registry entry.
func cloudflareOps(t *testing.T) (served map[string]bool, typed map[string]string) {
	t.Helper()
	rec := &capture{}
	app := harness(t, map[string]string{"acme": "tok"}, rec, nil)

	doc, err := openapi.Spec(app, openapi.Info{Title: "cloudflare", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	reg, err := openapi.Typed(app)
	if err != nil {
		t.Fatalf("typed registry: %v", err)
	}
	served, typed = map[string]bool{}, map[string]string{}
	for path, item := range doc.Paths {
		if !strings.HasPrefix(path, "/v1/cloudflare") {
			continue
		}
		for method := range item {
			served[strings.ToUpper(method)+" "+path] = true
		}
	}
	for key, op := range reg.Ops {
		if strings.Contains(key, "/v1/cloudflare") {
			typed[key] = op.Description
		}
	}
	return served, typed
}

// TestEveryRouteIsTypedOrNamed fails when a /v1/cloudflare operation is neither a
// typed op nor one of the six above — so the next route added here is typed by
// default, and dropping one out of the registry takes a deliberate edit with a reason.
func TestEveryRouteIsTypedOrNamed(t *testing.T) {
	served, typed := cloudflareOps(t)

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
			"method. Convert it (zip.Get/Post/... on the group), or add it to untypedByDesign with the reason "+
			"typing it would move the wire.", strings.Join(untyped, ", "))
	}
	// The reasons must describe operations that exist, or the list is stale prose.
	for key := range untypedByDesign {
		if !served[key] {
			t.Errorf("untypedByDesign names %q, which this plane no longer serves", key)
		}
	}
}

// declaredBodies is the subset of untypedByDesign whose REQUEST is still ordinary
// JSON, mapped to the fields the document must publish for it. Those three cannot be
// typed ops, but they can still SAY what they take — openapi.Register declares the
// body off the very struct the handler binds (cloudflare.go), which is what puts the
// shape in openapi.yaml and therefore in every generated SDK. The other three carry
// bytes that are not JSON at all and have nothing to declare.
var declaredBodies = map[string][]string{
	"POST /v1/cloudflare/pages/projects/{project}/deployments": {"branch"},
	"PUT /v1/cloudflare/workers/scripts/{script}":              {"script", "mainModule", "compatibilityDate", "compatibilityFlags", "bindings"},
	"POST /v1/cloudflare/d1/databases/{database}/query":        {"sql", "params"},
}

// TestUntypedJSONRoutesDeclareTheirBody fails when one of the three loses its
// declaration — the failure mode being that a route quietly goes back to publishing
// no request shape at all, which no consumer of the document can distinguish from a
// route that takes no body.
func TestUntypedJSONRoutesDeclareTheirBody(t *testing.T) {
	rec := &capture{}
	app := harness(t, map[string]string{"acme": "tok"}, rec, nil)
	doc, err := openapi.Spec(app, openapi.Info{Title: "cloudflare", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	for key, want := range declaredBodies {
		method, path, _ := strings.Cut(key, " ")
		op := doc.Paths[path][strings.ToLower(method)]
		if op == nil {
			t.Errorf("%s is not served", key)
			continue
		}
		got := bodyProperties(t, doc, op.RequestBody)
		for _, field := range want {
			if _, ok := got[field]; !ok {
				t.Errorf("%s publishes no request field %q — the declaration in cloudflare.go's init "+
					"is what puts this route's body in openapi.yaml and every SDK generated from it; "+
					"have %v", key, field, slices.Sorted(maps.Keys(got)))
			}
		}
	}
}

// bodyProperties reads the property names off an operation's declared request body,
// resolving the $ref a named struct is published under. It goes through JSON so it
// reads the document exactly as a consumer does rather than the structs that built it
// — which is the only reading that proves what an SDK generator will see.
func bodyProperties(t *testing.T, doc *openapi.Document, body any) map[string]any {
	t.Helper()
	if body == nil {
		return nil
	}
	var shape struct {
		Content map[string]struct {
			Schema struct {
				Ref        string         `json:"$ref"`
				Properties map[string]any `json:"properties"`
			} `json:"schema"`
		} `json:"content"`
	}
	decode(t, body, &shape)
	s := shape.Content["application/json"].Schema
	if s.Ref == "" {
		return s.Properties
	}
	name := strings.TrimPrefix(s.Ref, "#/components/schemas/")
	if doc.Components == nil || doc.Components.Schemas[name] == nil {
		t.Errorf("request body $refs %q, which the document does not define", s.Ref)
		return nil
	}
	var def struct {
		Properties map[string]any `json:"properties"`
	}
	decode(t, doc.Components.Schemas[name], &def)
	return def.Properties
}

// decode round-trips one document node through JSON into out.
func decode(t *testing.T, node, out any) {
	t.Helper()
	raw, err := json.Marshal(node)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		t.Fatalf("decode: %v", err)
	}
}

// Every typed op must carry lifted prose, because that prose IS the product
// surface: it becomes the OpenAPI description AND the MCP tool description a model
// reads to pick the tool. zipdoc_gen.go is what carries it into the binary, so an
// op added without regenerating shows up here as a nameless tool.
func TestEveryTypedOpIsDescribed(t *testing.T) {
	_, typed := cloudflareOps(t)
	if len(typed) == 0 {
		t.Fatal("no typed cloudflare ops in the registry at all")
	}
	for key, desc := range typed {
		if strings.TrimSpace(desc) == "" {
			t.Errorf("%s has no description — run: go generate -run zipdoc ./apps/cloudflare/...", key)
		}
	}
}

// proseless is the CLOSED list of published properties that carry NO description,
// and it names a CLIENT rather than anyone's diligence.
//
// All eight belong to the three bodies declared with openapi.Register (the init in
// cloudflare.go). Register derives a schema by REFLECTION from the Go type, and Go
// drops comments at compile time — zipdoc, the pass that lifts field prose, walks
// zip's TYPED registrations and can never reach a type that arrives this way. Every
// one of these fields carries a doc comment in the source beside the struct the
// handler binds; reflection simply cannot see it. The alternative was a hand-written
// schema beside the struct, which is the drift Register exists to prevent.
//
// The three routes cannot be typed ops for reasons relay_wire_test.go states above:
// D1 forwards the query body verbatim, the Worker upload's `script` field and the
// `:script` path segment are two different things, and a Pages deploy IGNORES a body
// it cannot parse where a typed op would answer 400.
//
// Exact in BOTH directions. A bare property anywhere else is a typed op's, which
// zipdoc can describe, and goes red. An entry here that starts publishing prose goes
// red too — that is the day zip learns to lift comments through Register, and this
// ledger shrinks instead of outliving the gap.
var proseless = map[string]bool{
	"D1Query.params":                     true,
	"D1Query.sql":                        true,
	"PagesDeploy.branch":                 true,
	"WorkerScriptPut.bindings":           true,
	"WorkerScriptPut.compatibilityDate":  true,
	"WorkerScriptPut.compatibilityFlags": true,
	"WorkerScriptPut.mainModule":         true,
	"WorkerScriptPut.script":             true,
}

// TestEveryPublishedFieldIsDescribed closes the half of the surface the op-level
// gates cannot see. Typing a route documents its ADDRESS and its SHAPE; the shape's
// FIELDS come from a doc comment on each one, which zipdoc lifts per field.
//
// On this plane the Pages create body was the whole gap: three levels of nested
// config in which `env_vars`, `kv_namespaces`, `d1_databases` and `r2_buckets` are
// maps KEYED BY BINDING NAME — the name the deployed code reads the resource as — so
// the key carries half the meaning and no field could say so. `type` deciding
// whether a variable is stored in the clear, and a compatibility DATE pinning
// runtime behaviour rather than naming a version, are the other two a caller cannot
// guess.
//
// Presence is all a gate can check. A description restating the field's name is
// worse than none, and only a reader catches that.
func TestEveryPublishedFieldIsDescribed(t *testing.T) {
	app := harness(t, map[string]string{"acme": "tok"}, &capture{}, nil)
	doc, err := openapi.Spec(app, openapi.Info{Title: "cloudflare", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	if doc.Components == nil || len(doc.Components.Schemas) == 0 {
		t.Fatal("cloudflare publishes no schemas at all — the gate would pass vacuously")
	}
	published, err := openapi.Bare(doc)
	if err != nil {
		t.Fatalf("bare: %v", err)
	}

	var bare, stale []string
	seen := map[string]bool{}
	for _, path := range published {
		seen[path] = true
		if !proseless[path] {
			bare = append(bare, path)
		}
	}
	for path := range proseless {
		if !seen[path] {
			stale = append(stale, path)
		}
	}

	if len(bare) > 0 {
		t.Errorf("%d published schema propert(ies) with no description: %s\n"+
			"Write the field's OWN doc comment — a header above a group of fields is lifted onto "+
			"the first of them alone — then run: make -C apps/cloudflare describe",
			len(bare), strings.Join(bare, ", "))
	}
	if len(stale) > 0 {
		sort.Strings(stale)
		t.Errorf("proseless names propert(ies) that are gone or now described: %s\n"+
			"An exemption that outlives its cause is how a generator gap becomes permanent — "+
			"delete the entr(ies).", strings.Join(stale, ", "))
	}
}
