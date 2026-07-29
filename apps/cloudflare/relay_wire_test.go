package cloudflare

// relay_wire_test.go — the contract the typed ops must not move.
//
// Typing this plane was a DESCRIPTION task: every op now carries an In/Out type, so
// it reaches the OpenAPI document, the MCP tool list, the CLI and the SDKs — and the
// bytes on the wire had to stay exactly what they were. These tests pin the two
// halves of that claim: the response is still Cloudflare's own payload, and the set
// of routes that are NOT typed ops is a closed, named list rather than a drift.

import (
	"net/http"
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
