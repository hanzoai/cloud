package esign

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/apps/goja"
	"github.com/hanzoai/cloud/internal/edge"
	"github.com/hanzoai/cloud/openapi"
	"github.com/zap-proto/zip"
)

// typed_wire_test.go is the gate on BOTH halves of this surface. The counts here
// are MEASURED off the live router rather than asserted in prose, because prose
// cannot go red: a route added untyped goes red without anyone remembering to name
// it, a reason naming a route esign no longer serves goes red too, and the two
// ledgers must sum to what the router actually serves.

// untypedByDesign is the CLOSED list of operations here that are NOT typed ops,
// each with the wire fact that keeps it out. It is EMPTY: every one of the
// thirteen operations esign serves is a typed op.
//
// That is worth stating rather than leaving as an absence, because HIP-1125 §3 is
// titled "Why nothing here is typed" and the package doc said the same. Both
// rested on one premise — every route was built by a handler FACTORY closing over
// a bundle route name, and a closure has no doc comment to lift — which is a fact
// about the FACTORY and not about the routes. Writing the ops as methods over the
// same bundle client, on the shared apps/goja kit, retired it; the HIP is wrong and
// should be corrected rather than worked around.
var untypedByDesign = map[string]string{}

// mountTyped mounts ONLY this subsystem and hands back the app, for the gates
// below. It is the REAL Mount, not a reconstruction of it, which is what makes
// these gates rather than descriptions.
func mountTyped(t *testing.T) *zip.App { return mountOn(t, t.TempDir(), newMemVFS()) }

// esignOps reads BOTH projections of the live router at their one shared address
// form: what the document says is served, and which of those carry a typed
// registry entry — the single value the OpenAPI operation, the MCP tool, the CLI
// command and the generated SDK method all come from.
func esignOps(t *testing.T) (served map[string]bool, typed map[string]string) {
	t.Helper()
	app := mountTyped(t)
	doc, err := openapi.Spec(app, openapi.Info{Title: "esign", Version: "v1"})
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
	return served, typed
}

// TestEveryRouteIsTypedOrNamed fails when an operation here is neither a typed op
// nor one named above — so the next route added is typed by default, and dropping
// one out of the registry takes a deliberate edit with a reason.
func TestEveryRouteIsTypedOrNamed(t *testing.T) {
	served, typed := esignOps(t)

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
			"A route that is not a typed op has no schema, no prose, no MCP tool, no CLI command and "+
			"no SDK method. Convert it (zip.Get/Post/... on the group in typed.go), or add it to "+
			"untypedByDesign with the wire fact that typing it would move.", strings.Join(untyped, ", "))
	}
	for key := range untypedByDesign {
		if !served[key] {
			t.Errorf("untypedByDesign names %q, which esign no longer serves", key)
		}
	}
	if got, want := len(typed)+len(untypedByDesign), len(served); got != want {
		t.Errorf("typed(%d) + named(%d) = %d, served = %d — the ledgers must partition the surface",
			len(typed), len(untypedByDesign), got, want)
	}
	if len(typed) != 13 {
		t.Errorf("typed ops = %d, want 13 — the whole esign surface. A drop here is a route that "+
			"stopped publishing its schema, its prose and its tool.", len(typed))
	}
}

// TestEveryTypedOpIsDescribed holds the prose to the same bar as the schema,
// because that prose IS the product surface: it becomes the OpenAPI description
// AND the MCP tool description a model reads to pick the tool. zipdoc_gen.go
// carries it into the binary, so an op added without regenerating shows up here as
// a nameless tool.
func TestEveryTypedOpIsDescribed(t *testing.T) {
	_, typed := esignOps(t)
	if len(typed) == 0 {
		t.Fatal("no typed esign ops in the registry at all")
	}
	for key, desc := range typed {
		if strings.TrimSpace(desc) == "" {
			t.Errorf("%s has no description — run: go generate -run zipdoc ./apps/esign/...", key)
		}
	}
}

// TestEveryPublishedFieldIsDescribed covers the side the op-level gate cannot see.
// A typed op publishes its In's and Out's whole schema, and a property that
// reaches openapi.yaml with no description reaches every generated SDK and every
// MCP inputSchema without one too — so a reader could see that `width` is a number
// and nowhere that -1 means "the renderer chooses".
func TestEveryPublishedFieldIsDescribed(t *testing.T) {
	app := mountTyped(t)
	doc, err := openapi.Spec(app, openapi.Info{Title: "esign", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	// Through JSON, because that is the artifact: only the marshalled form is what
	// an SDK generator actually reads.
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal doc: %v", err)
	}
	var published struct {
		Components struct {
			Schemas map[string]struct {
				Properties map[string]struct {
					Description string `json:"description"`
				} `json:"properties"`
			} `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(raw, &published); err != nil {
		t.Fatalf("unmarshal doc: %v", err)
	}
	if len(published.Components.Schemas) == 0 {
		t.Fatal("no published schemas at all — a typed op must publish its In/Out")
	}
	var bare []string
	for name, schema := range published.Components.Schemas {
		for field, prop := range schema.Properties {
			if strings.TrimSpace(prop.Description) == "" {
				bare = append(bare, name+"."+field)
			}
		}
	}
	if len(bare) > 0 {
		sort.Strings(bare)
		t.Errorf("%d published schema propert(ies) with no description: %s\n"+
			"Write the field's doc comment and run: go generate -run zipdoc ./apps/esign/...",
			len(bare), strings.Join(bare, ", "))
	}
}

// TestTheSurfaceDeclaresItsBodies pins the half of the document that an
// operationId alone cannot state. Before typing, all thirteen operations published
// an id and NOTHING else — indistinguishable, to any consumer of the document,
// from a route that takes no input and returns none — so every SDK generated off
// it offered a PDF upload with nowhere to put the PDF.
//
// The five with a request body are the five that read one. `send` and `complete`
// are deliberately NOT among them: they carry no field the URL does not already
// name, and zip publishes a body only for an input that has one, so declaring
// otherwise would give every generated client an argument the route has never
// read.
func TestTheSurfaceDeclaresItsBodies(t *testing.T) {
	app := mountTyped(t)
	doc, err := openapi.Spec(app, openapi.Info{Title: "esign", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	raw, _ := json.Marshal(doc)
	var published struct {
		Paths map[string]map[string]struct {
			RequestBody *struct{} `json:"requestBody"`
			Responses   map[string]struct {
				Content map[string]struct {
					Schema map[string]any `json:"schema"`
				} `json:"content"`
			} `json:"responses"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(raw, &published); err != nil {
		t.Fatalf("unmarshal doc: %v", err)
	}

	wantBody := map[string]bool{
		"POST /v1/esign/documents":                             true,
		"POST /v1/esign/documents/{id}/recipients":             true,
		"POST /v1/esign/documents/{id}/fields":                 true,
		"POST /v1/esign/o/{org}/sign/{token}/fields/{fieldId}": true,
		"POST /v1/esign/o/{org}/sign/{token}/reject":           true,
	}
	got := 0
	for path, item := range published.Paths {
		for method, op := range item {
			key := strings.ToUpper(method) + " " + path
			if op.RequestBody != nil {
				got++
				if !wantBody[key] {
					t.Errorf("%s publishes a request body it does not read", key)
				}
			} else if wantBody[key] {
				t.Errorf("%s reads a body and publishes none — an SDK caller has nowhere to put it", key)
			}
			// Every operation answers a value, so every operation owes a response
			// schema. A 2xx with no content is what a route publishing nothing
			// looked like before this file.
			var hasSchema bool
			for code, resp := range op.Responses {
				if !strings.HasPrefix(code, "2") {
					continue
				}
				for _, c := range resp.Content {
					if len(c.Schema) > 0 {
						hasSchema = true
					}
				}
			}
			if !hasSchema {
				t.Errorf("%s publishes no response schema — every esign operation answers a value", key)
			}
		}
	}
	if got != len(wantBody) {
		t.Errorf("request bodies published = %d, want %d", got, len(wantBody))
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// the wire the typed plane replaced
// ─────────────────────────────────────────────────────────────────────────────

// seeded drives one document through create → recipient → field → send and hands
// back the app, the org, the document id and the signing token, which is the state
// every wire check below needs.
func seeded(t *testing.T) (app *zip.App, org, docID, token string) {
	t.Helper()
	app = mountTyped(t)
	org = "acme"

	pdf, err := os.ReadFile("testdata/example.pdf")
	if err != nil {
		t.Fatalf("read testdata: %v", err)
	}
	code, body := do(t, app, http.MethodPost, "/v1/esign/documents", org, map[string]any{
		"title":     "Mutual NDA",
		"pdfBase64": base64.StdEncoding.EncodeToString(pdf),
		"subject":   "Please sign",
	})
	if code != http.StatusCreated {
		t.Fatalf("create: want 201, got %d (%s)", code, body)
	}
	docID, _ = decode(t, body)["id"].(string)

	code, body = do(t, app, http.MethodPost, "/v1/esign/documents/"+docID+"/recipients", org,
		map[string]any{"email": "Dana@Example.com", "name": "Dana"})
	if code != http.StatusCreated {
		t.Fatalf("add recipient: want 201, got %d (%s)", code, body)
	}
	rec := decode(t, body)
	token, _ = rec["token"].(string)
	recID, _ := rec["id"].(string)

	code, body = do(t, app, http.MethodPost, "/v1/esign/documents/"+docID+"/fields", org,
		map[string]any{"recipientId": recID, "type": "SIGNATURE", "page": 1,
			"positionX": 72, "positionY": 640.5, "width": 180, "height": 48,
			"fieldMeta": map[string]any{"label": "Sign here", "required": true}})
	if code != http.StatusCreated {
		t.Fatalf("add field: want 201, got %d (%s)", code, body)
	}
	if code, body = do(t, app, http.MethodPost, "/v1/esign/documents/"+docID+"/send", org, nil); code != http.StatusOK {
		t.Fatalf("send: want 200, got %d (%s)", code, body)
	}
	return app, org, docID, token
}

// TestTypedAnswersAreByteIdentical pins that typing changed no answer, on the one
// comparison that can prove it: the typed op's bytes against the BUNDLE's own.
//
// The models declare their fields in alphabetical json-tag order for exactly this
// reason — the bundle's answer crosses the goja boundary as map[string]any and is
// re-marshalled by encoding/json, which sorts keys — so a field added out of order
// fails HERE rather than as drift a client notices later.
func TestTypedAnswersAreByteIdentical(t *testing.T) {
	app, org, docID, token := seeded(t)

	for _, c := range []struct {
		path, route string
		params      map[string]string
		signer      bool
	}{
		{"/v1/esign/documents", "documents.list", nil, false},
		{"/v1/esign/documents/" + docID, "documents.get", map[string]string{"id": docID}, false},
		{"/v1/esign/documents/" + docID + "/download", "documents.download", map[string]string{"id": docID}, false},
		{"/v1/esign/documents/" + docID + "/audit", "documents.audit", map[string]string{"id": docID}, false},
		{"/v1/esign/o/" + org + "/sign/" + token, "sign.view", map[string]string{"org": org, "token": token}, true},
	} {
		caller := org
		if c.signer {
			caller = "" // the signer's door takes no principal
		}
		code, typed := do(t, app, http.MethodGet, c.path, caller, nil)
		if code != http.StatusOK {
			t.Fatalf("%s: want 200, got %d (%s)", c.path, code, typed)
		}
		resp, err := mounted.State.host.Dispatch(context.Background(), org,
			goja.BaseRequest{Route: c.route, Params: c.params})
		if err != nil {
			t.Fatalf("%s: bundle dispatch: %v", c.route, err)
		}
		if string(typed) != string(resp.Body) {
			t.Errorf("%s is NOT byte-identical to the bundle it relays:\n typed:  %s\n bundle: %s",
				c.path, typed, resp.Body)
		}
	}
}

// TestGeometryKeepsTheCallersNumber is the reason a field's page and position are
// float64 rather than int.
//
// The bundle coerces with num() and does NOT round, so a fractional page is stored
// and answered as one. An int field would fail to decode that answer, and
// declaring `integer` in the schema would publish a constraint this route does not
// enforce. The value also has to survive the round trip BYTE for byte, which is
// what catches a Go float encoder disagreeing with what goja exported.
func TestGeometryKeepsTheCallersNumber(t *testing.T) {
	app := mountTyped(t)
	const org = "acme"

	pdf, err := os.ReadFile("testdata/example.pdf")
	if err != nil {
		t.Fatalf("read testdata: %v", err)
	}
	_, body := do(t, app, http.MethodPost, "/v1/esign/documents", org,
		map[string]any{"title": "T", "pdfBase64": base64.StdEncoding.EncodeToString(pdf)})
	docID, _ := decode(t, body)["id"].(string)
	_, body = do(t, app, http.MethodPost, "/v1/esign/documents/"+docID+"/recipients", org,
		map[string]any{"email": "a@b.c"})
	recID, _ := decode(t, body)["id"].(string)

	// A fractional page and a large integral coordinate: the first is the value an
	// int field could not hold, the second is where a float encoder would reach for
	// exponent notation the bundle never wrote.
	code, body := do(t, app, http.MethodPost, "/v1/esign/documents/"+docID+"/fields", org,
		map[string]any{"recipientId": recID, "type": "TEXT", "page": 1.5, "positionX": 1000000})
	if code != http.StatusCreated {
		t.Fatalf("add field: want 201, got %d (%s)", code, body)
	}
	if got := decode(t, body)["page"]; got != 1.5 {
		t.Fatalf("page came back %v, want 1.5 — the bundle does not round and neither may the type", got)
	}

	_, typed := do(t, app, http.MethodGet, "/v1/esign/documents/"+docID, org, nil)
	resp, err := mounted.State.host.Dispatch(context.Background(), org,
		goja.BaseRequest{Route: "documents.get", Params: map[string]string{"id": docID}})
	if err != nil {
		t.Fatalf("bundle dispatch: %v", err)
	}
	if string(typed) != string(resp.Body) {
		t.Errorf("geometry did not survive byte-identically:\n typed:  %s\n bundle: %s", typed, resp.Body)
	}
	if !strings.Contains(string(typed), `"positionX":1000000`) {
		t.Errorf("a large integral coordinate was re-formatted: %s", typed)
	}
}

// TestFieldMetaStaysTheCallersJSON is the reason FieldMeta is a goja.Raw on the way
// in and a json.RawMessage on the way out rather than a Scalar.
//
// A Scalar carries the same bytes but is a string KIND, so every projection would
// describe an OBJECT field as `string` — and a caller who believed that would send
// a quoted, double-encoded string. The published schema must say "any JSON", and
// the value must arrive at the bundle as the object the caller wrote.
func TestFieldMetaStaysTheCallersJSON(t *testing.T) {
	app, org, docID, _ := seeded(t)

	_, body := do(t, app, http.MethodGet, "/v1/esign/documents/"+docID, org, nil)
	var doc esignDocument
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("decode: %v (%s)", err, body)
	}
	if len(doc.Fields) != 1 {
		t.Fatalf("want one field, got %d (%s)", len(doc.Fields), body)
	}
	var meta struct {
		Label    string `json:"label"`
		Required bool   `json:"required"`
	}
	if err := json.Unmarshal(doc.Fields[0].FieldMeta, &meta); err != nil {
		t.Fatalf("fieldMeta is not the object it was sent as: %v (%s)", err, doc.Fields[0].FieldMeta)
	}
	if meta.Label != "Sign here" || !meta.Required {
		t.Fatalf("fieldMeta round-tripped wrong: %s", doc.Fields[0].FieldMeta)
	}

	// And the published schema says "any JSON" rather than a type it is not.
	app2 := mountTyped(t)
	spec, err := openapi.Spec(app2, openapi.Info{Title: "esign", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	raw, _ := json.Marshal(spec)
	var published struct {
		Components struct {
			Schemas map[string]map[string]any `json:"schemas"`
		} `json:"components"`
	}
	_ = json.Unmarshal(raw, &published)
	for _, name := range []string{"esignField", "esignFieldIn"} {
		props, _ := published.Components.Schemas[name]["properties"].(map[string]any)
		fm, _ := props["fieldMeta"].(map[string]any)
		if fm == nil {
			t.Fatalf("%s does not publish fieldMeta at all", name)
		}
		if kind, ok := fm["type"]; ok {
			t.Errorf("%s.fieldMeta publishes type %v — it is any JSON, and naming a type it is not "+
				"is what makes a client double-encode it", name, kind)
		}
	}
}

// TestRefusalKeepsTheBundlesOwnEnvelope pins that a typed op does not replace the
// bundle's vocabulary for failure with zip's. The bundle authors `{"error": …}`
// under its own status; goja.BundleErr carries both and goja.Envelope writes them
// back verbatim, so a 409 still reads exactly as it did.
func TestRefusalKeepsTheBundlesOwnEnvelope(t *testing.T) {
	app, org, docID, _ := seeded(t)

	// The document is PENDING, so adding a recipient is the bundle's own 409.
	code, body := do(t, app, http.MethodPost, "/v1/esign/documents/"+docID+"/recipients", org,
		map[string]any{"email": "late@example.com"})
	if code != http.StatusConflict {
		t.Fatalf("want 409, got %d (%s)", code, body)
	}
	if string(body) != `{"error":"recipients can only be added while DRAFT"}` {
		t.Errorf("the bundle's envelope was replaced: %s", body)
	}
}

// TestSignersDoorResolvesTheTokenNotTheClaim pins the order the whole recipient
// surface rests on: the token selects the tenant, and the org segment is only
// checked against that answer. A claim naming another tenant is the SAME 404 an
// unknown token gets, so the refusal never separates "no such token" from "not
// yours" — and no per-tenant file is touched on the way to either.
func TestSignersDoorResolvesTheTokenNotTheClaim(t *testing.T) {
	app, org, _, token := seeded(t)

	if code, body := do(t, app, http.MethodGet, "/v1/esign/o/"+org+"/sign/"+token, "", nil); code != http.StatusOK {
		t.Fatalf("the real link: want 200, got %d (%s)", code, body)
	}
	for _, c := range []struct{ name, path string }{
		{"a stranger's org claim over a real token", "/v1/esign/o/other/sign/" + token},
		{"an unknown token", "/v1/esign/o/" + org + "/sign/nope"},
	} {
		code, body := do(t, app, http.MethodGet, c.path, "", nil)
		if code != http.StatusNotFound {
			t.Errorf("%s: want 404, got %d (%s)", c.name, code, body)
		}
		if !strings.Contains(string(body), "unknown signing token") {
			t.Errorf("%s: the refusal names which failure it was: %s", c.name, body)
		}
	}
	// A validated principal for another org buys nothing here either: the door
	// reads the token, never the caller.
	if code, _ := do(t, app, http.MethodGet, "/v1/esign/o/other/sign/"+token, "other", nil); code != http.StatusNotFound {
		t.Errorf("a principal cannot redirect the signer's door: got %d", code)
	}
}

// TestTenantIsNeverAnInputField is the reason the owner ops read the org off the
// context and the signer ops keep `org` out of the body.
//
// zip binds an In field from the BODY as well as the URL, so a tenant key that was
// an In field would be a cross-tenant read the caller asserted for itself. The
// owner routes have no org field at all; the signer routes have one that is
// checked, never used to select, and carries `json:"-"` so a body cannot even
// reach it.
func TestTenantIsNeverAnInputField(t *testing.T) {
	app, org, docID, token := seeded(t)

	// A body naming another org does not move an owner write.
	code, body := do(t, app, http.MethodPost, "/v1/esign/documents", org, map[string]any{
		"title": "Redirected", "pdfBase64": "JVBERi0=", "org": "victim", "id": "forced",
	})
	if code != http.StatusCreated {
		t.Fatalf("create: want 201, got %d (%s)", code, body)
	}
	if got, _ := decode(t, body)["id"].(string); got == "forced" {
		t.Error("a body set the document id — ids are the host's to mint")
	}
	if code, _ := do(t, app, http.MethodGet, "/v1/esign/documents", "victim", nil); code == http.StatusOK {
		var out esignDocuments
		_, b := do(t, app, http.MethodGet, "/v1/esign/documents", "victim", nil)
		_ = json.Unmarshal(b, &out)
		if len(out.Documents) != 0 {
			t.Errorf("another org sees %d documents — the tenant is not the principal's", len(out.Documents))
		}
	}

	// A body naming another org does not move a signer write either: the field
	// insert lands under the token's own tenant.
	var doc esignDocument
	_, b := do(t, app, http.MethodGet, "/v1/esign/documents/"+docID, org, nil)
	_ = json.Unmarshal(b, &doc)
	code, body = do(t, app, http.MethodPost,
		"/v1/esign/o/"+org+"/sign/"+token+"/fields/"+doc.Fields[0].ID, "",
		map[string]any{"value": "Dana Lee", "org": "victim", "token": "stolen", "fieldId": "elsewhere"})
	if code != http.StatusOK {
		t.Fatalf("sign field: want 200, got %d (%s)", code, body)
	}
	var ins esignInsertion
	if err := json.Unmarshal(body, &ins); err != nil {
		t.Fatalf("decode: %v (%s)", err, body)
	}
	if ins.FieldID != doc.Fields[0].ID {
		t.Errorf("a body redirected the field write to %q — the URL is the addressing authority", ins.FieldID)
	}
}

// TestUnreadableBodiesKeepTheRelaysRefusal pins the parse refusal the relay made
// before the bundle saw anything, and the tolerance it made alongside it.
//
// The relay decoded into `any`, so it refused BYTES THAT ARE NOT JSON with a 400
// and accepted every other shape — an array, a bare number — which the bundle then
// read as an object with no keys and answered for itself. A typed op is entered
// AFTER zip has decoded, and zip's decoder refuses both, so the tolerant half is
// restated in each input's own UnmarshalJSON. Getting this wrong is not cosmetic:
// POST …/complete reads no field at all, so a decoder that refused a non-object
// body would turn today's completed signature into a 400.
func TestUnreadableBodiesKeepTheRelaysRefusal(t *testing.T) {
	app, org, docID, token := seeded(t)

	// Bytes that are not JSON: the relay's 400, on every body-carrying route.
	for _, path := range []string{
		"/v1/esign/documents",
		"/v1/esign/documents/" + docID + "/recipients",
		"/v1/esign/documents/" + docID + "/fields",
	} {
		if code, body := doRaw(t, app, http.MethodPost, path, org, []byte("{not json")); code != http.StatusBadRequest {
			t.Errorf("%s with unreadable bytes: want 400, got %d (%s)", path, code, body)
		}
	}
	// A body that IS JSON but not an object must still reach the bundle, which
	// answers its own refusal in its own envelope.
	code, body := doRaw(t, app, http.MethodPost, "/v1/esign/documents", org, []byte(`[1,2]`))
	if code != http.StatusBadRequest || !strings.Contains(string(body), "title required") {
		t.Errorf("a JSON array body must reach the bundle, which answers its own refusal; got %d (%s)", code, body)
	}
	// And on the route with no body fields at all, a non-object body is IGNORED,
	// exactly as it was — the completion still happens.
	var doc esignDocument
	_, b := do(t, app, http.MethodGet, "/v1/esign/documents/"+docID, org, nil)
	_ = json.Unmarshal(b, &doc)
	if code, body := do(t, app, http.MethodPost,
		"/v1/esign/o/"+org+"/sign/"+token+"/fields/"+doc.Fields[0].ID, "",
		map[string]any{"value": "Dana Lee"}); code != http.StatusOK {
		t.Fatalf("sign field: want 200, got %d (%s)", code, body)
	}
	if code, body := doRaw(t, app, http.MethodPost,
		"/v1/esign/o/"+org+"/sign/"+token+"/complete", "", []byte(`[1,2]`)); code != http.StatusOK {
		t.Errorf("a non-object body on a route that reads none must be ignored: got %d (%s)", code, body)
	}
	// Bytes that are not JSON are still the 400 there.
	if code, body := doRaw(t, app, http.MethodPost,
		"/v1/esign/o/"+org+"/sign/"+token+"/reject", "", []byte("{not json")); code != http.StatusBadRequest {
		t.Errorf("unreadable bytes on the signer's door: want 400, got %d (%s)", code, body)
	}
}

// TestTheBodyCapIsTheEdgesRatherThanEsigns records a live defect that typing found
// and did NOT change, because changing it is a behaviour decision and this was a
// description task.
//
// esign's maxBody is 32 MiB and the deployment's own ceiling is 16 MiB
// (internal/edge.BodyLimit, GATEWAY_BODY_LIMIT), which zip hands to fasthttp as
// MaxRequestBodySize (transport.go:594). fasthttp refuses a larger body before any
// handler runs, so esign's cap can never fire: every body it would refuse has
// already been refused, and every body it sees is under half its limit. The
// SizedIn machinery is kept anyway and kept faithful — the relay measured before it
// parsed, and reproducing that order is what makes this a typing pass rather than a
// redesign — but the number is dead, and the product consequence is real: the
// largest PDF this API accepts is what 16 MiB of base64 holds, roughly 12 MiB, not
// the 32 MiB the constant advertises.
//
// This fails the day someone lowers maxBody under the edge, which is the day the
// cap becomes reachable and the 413-before-400 order starts to matter again.
func TestTheBodyCapIsTheEdgesRatherThanEsigns(t *testing.T) {
	if maxBody <= edge.BodyLimit() {
		t.Fatalf("esign's maxBody (%d) is now at or under the edge ceiling (%d), so it can fire. "+
			"The order the relay used — tenant, then 413, then the bundle — is live again: "+
			"re-read TestUnreadableBodiesKeepTheRelaysRefusal and add the 413 case back.",
			maxBody, edge.BodyLimit())
	}
}

// TestBundleCoercionSurvivesTyping is the reason every caller-supplied field is a
// goja.Scalar rather than a Go string or number.
//
// The bundle's validators are LENIENT — str() turns a number into its digits,
// num() reads a numeric string — so a typed field of the "right" Go type would
// refuse input this route accepts today and make it accept LESS. The token is
// carried byte for byte, which leaves the bundle the only judge of it.
func TestBundleCoercionSurvivesTyping(t *testing.T) {
	app := mountTyped(t)
	const org = "acme"

	pdf, err := os.ReadFile("testdata/example.pdf")
	if err != nil {
		t.Fatalf("read testdata: %v", err)
	}
	// A NUMERIC title, which the bundle stores as its digits.
	code, body := doRaw(t, app, http.MethodPost, "/v1/esign/documents", org,
		[]byte(`{"title":12345,"pdfBase64":"`+base64.StdEncoding.EncodeToString(pdf)+`"}`))
	if code != http.StatusCreated {
		t.Fatalf("numeric title: want 201, got %d (%s)", code, body)
	}
	doc := decode(t, body)
	if doc["title"] != "12345" {
		t.Errorf("the bundle's coercion did not survive typing: title=%v", doc["title"])
	}
	docID, _ := doc["id"].(string)

	// A numeric-STRING page, which num() reads and an int field would have zeroed.
	_, body = do(t, app, http.MethodPost, "/v1/esign/documents/"+docID+"/recipients", org,
		map[string]any{"email": "a@b.c"})
	recID, _ := decode(t, body)["id"].(string)
	code, body = doRaw(t, app, http.MethodPost, "/v1/esign/documents/"+docID+"/fields", org,
		[]byte(`{"recipientId":"`+recID+`","type":"TEXT","page":"3"}`))
	if code != http.StatusCreated {
		t.Fatalf("string page: want 201, got %d (%s)", code, body)
	}
	if got := decode(t, body)["page"]; got != float64(3) {
		t.Errorf("a numeric-string page came back %v, want 3", got)
	}
}

// doRaw issues a request with a body this test controls BYTE FOR BYTE, which is
// the only way to reach the decoder paths a Go value could never produce — a body
// that is not JSON at all, and one that is JSON but not an object.
func doRaw(t *testing.T, app *zip.App, method, path, org string, body []byte) (int, []byte) {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if org != "" {
		req.Header.Set("X-Org-Id", org)
		req.Header.Set("X-User-Id", "u_"+org)
	}
	resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second})
	if err != nil {
		t.Fatalf("Test %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}
