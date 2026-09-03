package cloudflare

// relay_wire_test.go — the contract the typed ops must not move.
//
// Typing this plane was a DESCRIPTION task: every op now carries an In/Out type, so
// it reaches the OpenAPI document, the MCP tool list, the CLI and the SDKs — and the
// bytes on the wire had to stay exactly what they were. These tests pin the two
// halves of that claim: the response is still Cloudflare's own payload, and the set
// of routes that are NOT typed ops is a closed, named list rather than a drift.

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"strings"
	"testing"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud"
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

// ── the two conversions, and the wire each had to keep ──────────────────────────

// A Worker upload means two different things by the word "script": the path names
// the Worker, the body carries its code. `url:"script"` binds the first and
// `json:"script"` the second, so a `?script=` cannot become the source and a body
// `script` cannot become the name — which is the whole reason this route could not
// be typed before zip split the two tags.
func TestTheURLNamesTheScriptAndTheBodyIsItsSource(t *testing.T) {
	const source = `export default { fetch: () => new Response("ok") }`
	rec := &capture{}
	app := harness(t, map[string]string{"acme": "tok"}, rec, nil)

	code, body, _ := doReq(t, app, http.MethodPut,
		"/v1/cloudflare/workers/scripts/edge-router?script=decoy", "u1", "acme", true,
		`{"script":`+quote(source)+`,"mainModule":"worker.js"}`)
	if code != http.StatusOK {
		t.Fatalf("upload = %d, want 200; body=%s", code, body)
	}
	r, ok := rec.find("/workers/scripts/edge-router")
	if !ok {
		t.Fatalf("upload did not address the script the URL named; got %+v", rec.reqs)
	}
	if !strings.Contains(string(r.body), source) {
		t.Fatalf("the module source did not reach Cloudflare; upstream body = %s", r.body)
	}
	if _, ok := rec.find("decoy"); ok {
		t.Fatal("a `?script=` query redirected the upload away from the script its URL named")
	}
}

// The same split on the D1 query: `?database=` is not an address. The URL is,
// exactly as it is for the DELETE beside it.
func TestTheQueryCannotRedirectAStatement(t *testing.T) {
	rec := &capture{}
	app := harness(t, map[string]string{"acme": "tok"}, rec, nil)

	code, body, _ := doReq(t, app, http.MethodPost,
		"/v1/cloudflare/d1/databases/orders/query?database=decoy", "u1", "acme", true,
		`{"sql":"SELECT 1"}`)
	if code != http.StatusOK {
		t.Fatalf("query = %d, want 200; body=%s", code, body)
	}
	if _, ok := rec.find("/d1/database/orders/query"); !ok {
		t.Fatalf("query did not address the database the URL named; got %+v", rec.reqs)
	}
	if _, ok := rec.find("decoy"); ok {
		t.Fatal("a `?database=` query redirected a statement to another database")
	}
}

// THE reason D1 keeps the caller's bytes. A typed In that decoded and re-encoded
// would forward only what its struct models, and D1's own wire is wider than the
// two fields named here — `params` carries the bound values, and a batch or a
// future D1 field has to survive too. So the body reaching D1 is compared BYTE FOR
// BYTE against what the caller sent.
func TestD1ForwardsTheCallersBytes(t *testing.T) {
	const sent = `{"sql":"SELECT * FROM orders WHERE id = ?","params":[42],"batch":[{"sql":"SELECT 1"}]}`
	rec := &capture{}
	app := harness(t, map[string]string{"acme": "tok"}, rec, nil)

	if code, body, _ := doReq(t, app, http.MethodPost, "/v1/cloudflare/d1/databases/orders/query",
		"u1", "acme", true, sent); code != http.StatusOK {
		t.Fatalf("query = %d, want 200; body=%s", code, body)
	}
	r, ok := rec.find("/d1/database/orders/query")
	if !ok {
		t.Fatalf("query did not reach D1; got %+v", rec.reqs)
	}
	if got := string(r.body); got != sent {
		t.Fatalf("D1 received %s, want the caller's own bytes %s", got, sent)
	}
}

// The ORDER a refusal arrives in is wire. zip decodes a typed op's body before the
// handler runs, so an input that REFUSED a body would answer 400 where this plane
// answers 403 — the org-admin gate is the first thing a mutation here meets, and a
// caller who may not query at all must not learn whether their SQL parsed.
// D1Query.UnmarshalJSON never refuses, so the order is the one it always had.
//
// The last case is the ONE thing that could not be carried back, recorded rather
// than glossed: encoding/json validates a whole document before it calls any
// Unmarshaler, so bytes that are not JSON at all are refused above the handler.
func TestD1KeepsItsOrderAndItsTolerance(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		admin      bool
		want       int
	}{
		{"a member is refused before the statement is read", `[1,2,3]`, false, http.StatusForbidden},
		{"an admin sending a shape D1 cannot use is told what is missing", `[1,2,3]`, true, http.StatusBadRequest},
		{"an admin sending no statement is told so", `{"params":[1]}`, true, http.StatusBadRequest},
		// The relay decoded `sql` alone and left the rest to D1, so a `params` D1
		// would reject reached D1 and D1 answered. It still does.
		{"a params D1 must judge still reaches D1", `{"sql":"SELECT 1","params":"notarray"}`, true, http.StatusOK},
		// The delta: not-JSON-at-all is 400 for a caller who used to read 403.
		{"bytes that are not JSON are refused above the handler", `{`, false, http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := &capture{}
			app := harness(t, map[string]string{"acme": "tok"}, rec, nil)
			code, body, _ := doReq(t, app, http.MethodPost, "/v1/cloudflare/d1/databases/orders/query",
				"u1", "acme", tc.admin, tc.body)
			if code != tc.want {
				t.Fatalf("status = %d, want %d; body=%s", code, tc.want, body)
			}
			if tc.want != http.StatusOK {
				if _, ok := rec.find("/d1/database/"); ok {
					t.Fatal("a refused statement still reached D1")
				}
			}
		})
	}
}

// Both converted routes answer through cfResult now, where they used to write the
// upstream bytes back themselves, so the assertion is PARITY with a sibling relay
// handed the same payload: same status, same Content-Type, same bytes.
//
// One byte-level fact is MEASURED here rather than assumed, because it is the one
// thing the conversion moved. cfResult hands Cloudflare's payload to encoding/json,
// which escapes `&`, `<` and `>` inside a string, where the old raw write passed
// those three characters through. The JSON is the same VALUE — every parser reads it
// back identically, and an integer past float64 still survives exactly, which is the
// loss that would matter — and it is what this plane's other relays have always
// answered, so the two converted routes stopped being the exception rather than
// becoming one.
func TestTheConvertedRoutesAnswerLikeTheirSiblings(t *testing.T) {
	const payload = `{"id":"abc","name":"a & b <c>","rows":9007199254740993}`
	relayed, err := json.Marshal(json.RawMessage(payload))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(relayed), "u0026") {
		t.Fatalf("this test measures the escape and the payload no longer carries one: %s", relayed)
	}
	result := func(string) (int, string) {
		return 200, `{"success":true,"errors":[],"result":` + payload + `}`
	}
	for _, tc := range []struct{ name, method, path, body string }{
		{"sibling relay", http.MethodGet, "/v1/cloudflare/zones", ""},
		{"d1 query", http.MethodPost, "/v1/cloudflare/d1/databases/orders/query", `{"sql":"SELECT 1"}`},
		{"worker upload", http.MethodPut, "/v1/cloudflare/workers/scripts/edge-router", `{"script":"x"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := harness(t, map[string]string{"acme": "tok"}, &capture{}, result)
			code, got, hdr := doReq(t, app, tc.method, tc.path, "u1", "acme", true, tc.body)
			if code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body=%s", code, got)
			}
			if want := "application/json; charset=utf-8"; hdr.Get("Content-Type") != want {
				t.Errorf("Content-Type = %q, want %q", hdr.Get("Content-Type"), want)
			}
			if got != string(relayed) {
				t.Errorf("relayed %s, want %s — a converted route must answer exactly as its siblings do",
					got, relayed)
			}
		})
	}
}

// This plane installs cloud.Bridge on its own group, ahead of its leaves, and this
// mounts on a BARE app to prove it — the composer installs one app-wide too, and
// relying on that alone is invisible until something mounts without a composer,
// which is every package test in the fleet.
//
// It drives a WRITE, because a read would pass either way and prove nothing.
// principal.OrgFrom falls back to zip.CallerOf, so a typed READ resolves its org
// off the request headers with no Bridge in sight. What only the Bridge can park is
// cloud.Request — the request itself, which authWrite needs for the org-admin
// header and resolveAccount for `?account=`. Without it every mutation here answers
// 403 to a genuine org admin.
func TestBridgeIsInstalledOnThePlanesOwnGroup(t *testing.T) {
	srv := httptest.NewServer(fakeCF(&capture{}, nil))
	t.Cleanup(srv.Close)
	t.Setenv("CLOUDFLARE_API_BASE", srv.URL)
	prev := tokenFor
	tokenFor = func(context.Context, string, string, string) ([]byte, error) { return []byte("tok"), nil }
	t.Cleanup(func() { tokenFor = prev })

	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	t.Setenv("CLOUD_DATA_DIR", t.TempDir())
	if err := Use(app, cloud.Deps{}); err != nil {
		t.Fatalf("Use:  %v", err)
	}
	code, body, _ := doReq(t, app, http.MethodPost, "/v1/cloudflare/d1/databases/orders/query",
		"u1", "acme", true, `{"sql":"SELECT 1"}`)
	if code != http.StatusOK {
		t.Fatalf("a mutation answered %d for an org admin with no composer — the group installs no "+
			"cloud.Bridge, so cloud.Request finds no request and authWrite refuses everything; body=%s",
			code, body)
	}
}

// quote renders s as a JSON string, so a test can put real module source in a body
// without hand-escaping it.
func quote(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// untypedByDesign is the CLOSED list of /v1/cloudflare operations that are NOT
// typed ops, each with the WIRE MECHANISM that stops one, cited where it lives. A
// typed op is a route PLUS a registry entry — the one value the document, the MCP
// tool, the CLI command and the SDK method all come from — so an operation missing
// from that registry is invisible to all four. These four are missing on purpose.
// Addresses are written the way the DOCUMENT writes them, which is the identity
// every projection keys on.
//
// TWO entries left this list, and both had named their own expiry without anyone
// re-reading them. The Worker upload said the path param `script` and the body
// field `script` "collide": zip's `url:` tag splits the two halves (openapi.go
// urlFieldName, typed.go bindURL) and names THIS ROUTE as the example it was added
// for. D1 said a typed In "drops every field it does not model": an In whose
// UnmarshalJSON keeps the caller's bytes drops nothing, which is what d1.go does.
// A reason that cannot fail is a reason that outlives its cause — which is why
// this list is read by a test rather than written in prose.
var untypedByDesign = map[string]string{
	"POST /v1/cloudflare/ai/run/{wildcard1}": "GREEDY WILDCARD, and a BYTE-STREAM reply. cloudflare.go " +
		"registers `/ai/run/*` because a Cloudflare model id carries slashes (@cf/meta/llama-3.1-8b-instruct, " +
		"ai.go aiModel); zip's Template leaves a pattern with no `:` unchanged, so the registry would publish " +
		"`/v1/cloudflare/ai/run/*` while cloud's router reading names it `{wildcard1}` (openapi/openapi.go), " +
		"and Fold then refuses the whole document for an op with no live route. An image or audio model also " +
		"answers BYTES under Cloudflare's own content type (ai.go runAI: SetHeader + c.Bytes), where a typed " +
		"op's only reply is c.JSON(out) (zip typed.go).",
	"GET /v1/cloudflare/kv/namespaces/{namespace}/values/{key}": "BYTE-STREAM reply. kv.go kvValueGet " +
		"relays the stored value under the content type it was written with (SetHeader + c.Bytes); a typed op " +
		"ends in c.JSON(out) (zip typed.go), which would base64 a []byte Out into a JSON document.",
	"PUT /v1/cloudflare/kv/namespaces/{namespace}/values/{key}": "RAW-BYTE request body. kv.go kvValuePut " +
		"hands c.Body() to cfUpload untouched under the caller's own Content-Type (`text/plain` when none is " +
		"sent); zip's op.invoke json-decodes every non-empty body before the handler runs (typed.go) and " +
		"there is no octet-stream request binding at the pin.",
	"POST /v1/cloudflare/pages/projects/{project}/deployments": "BODY TOLERANCE the decode order cannot " +
		"reproduce. pages.go pagesDeploy discards the decode error deliberately (`_ = json.Unmarshal`), so a " +
		"NON-EMPTY, syntactically-invalid body deploys the production branch at 200; encoding/json validates " +
		"a whole document before it calls any Unmarshaler, so no custom In can carry those bytes back and " +
		"zip answers 400. The tolerance is published in the operation's own description (cloudflare.go), so " +
		"it is a contract rather than an accident.",
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
// typed op nor one of the four above — so the next route added here is typed by
// default, and dropping one out of the registry takes a deliberate edit with a reason.
//
// It fails THREE ways, and the third is what keeps the ledger from rotting: a
// served operation nobody classified, a name this plane no longer serves, and a
// name that IS a typed op — the last being what a finished conversion looks like
// when its ledger entry is left behind.
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
	for key := range untypedByDesign {
		// The reasons must describe operations that exist, or the list is stale prose.
		if !served[key] {
			t.Errorf("untypedByDesign names %q, which this plane no longer serves", key)
		}
		// And a converted route must lose its entry in the same change, or the next
		// reader believes a mechanism the code stopped having.
		if _, ok := typed[key]; ok {
			t.Errorf("untypedByDesign names %q, which IS a typed op — delete the entry", key)
		}
	}
	// The two ledgers are the whole served surface, so neither can grow silently.
	if got, want := len(typed)+len(untypedByDesign), len(served); got != want {
		t.Errorf("%d typed + %d named = %d, but this plane serves %d operations",
			len(typed), len(untypedByDesign), got, want)
	}
}

// declaredBodies is the subset of untypedByDesign whose REQUEST is still ordinary
// JSON, mapped to the fields the document must publish for it. It cannot be a typed
// op, but it can still SAY what it takes — openapi.Register declares the body off
// the very struct the handler binds (cloudflare.go), which is what puts the shape in
// openapi.yaml and therefore in every generated SDK. The other three carry bytes
// that are not JSON at all and have nothing to declare.
var declaredBodies = map[string][]string{
	"POST /v1/cloudflare/pages/projects/{project}/deployments": {"branch"},
}

// TestUntypedJSONRoutesDeclareTheirBody fails when it loses its declaration — the
// failure mode being that a route quietly goes back to publishing no request shape
// at all, which no consumer of the document can distinguish from a route that takes
// no body.
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
// The one left belongs to the body declared with openapi.Register (the init in
// cloudflare.go). Register derives a schema by REFLECTION from the Go type, and Go
// drops comments at compile time — zipdoc, the pass that lifts field prose, walks
// zip's TYPED registrations and can never reach a type that arrives this way.
// `PagesDeploy.Branch` carries a doc comment in pages.go; reflection simply cannot
// see it. The alternative was a hand-written schema beside the struct, which is the
// drift Register exists to prevent.
//
// It held EIGHT until the Worker upload and the D1 query became typed ops, which is
// the shape of what typing buys on the response side: zipdoc reaches a typed op's
// fields, so seven descriptions that were already written in the source started
// being published without a word of new prose.
//
// Exact in BOTH directions. A bare property anywhere else is a typed op's, which
// zipdoc can describe, and goes red. An entry here that starts publishing prose goes
// red too — that is the day zip learns to lift comments through Register, and this
// ledger shrinks instead of outliving the gap.
var proseless = map[string]bool{
	"PagesDeploy.branch": true,
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
