package legal

// typed_wire_test.go pins the envelope details the typed conversion had to carry
// over, none of which the behaviour suite measures: the 1 MiB body cap (a
// package-local 413 that cloud's far larger global limit would have silently
// swallowed), the 201 on the two creates, the no-store pin on the two document
// reads, the ?limit query binding, and the CONDITIONAL keys of a document view.
//
// It also holds the route oracle and the CLOSED list of the one operation that is
// not a typed op, with the measured wire fact that keeps it out.

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/openapi"
)

// sendRaw issues one request with a VERBATIM body and content type, so the
// assertions about what a route ACCEPTS can send bytes no JSON encoder would.
func sendRaw(t *testing.T, app *zip.App, method, path, org, ctype string, body []byte) *http.Response {
	t.Helper()
	var r io.Reader
	if len(body) > 0 {
		r = bytes.NewReader(body)
	}
	rq := httptest.NewRequest(method, path, r)
	if ctype != "" {
		rq.Header.Set("Content-Type", ctype)
	}
	if org != "" {
		rq.Header.Set("X-Org-Id", org)
		rq.Header.Set("X-User-Id", "u_"+org)
	}
	resp, err := app.Test(rq, zip.TestConfig{Timeout: testTimeout, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("Test %s %s: %v", method, path, err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func TestTypedOpsPreserveTheLegalWire(t *testing.T) {
	app, _ := mount(t)
	const org = "org_wire"

	code, out := do(t, app, http.MethodPost, "/v1/legal/documents", org, map[string]any{
		"templateId": "nda",
		"data": map[string]string{
			"effective_date": "2026-01-01", "company_name": "Acme Inc.",
			"counterparty_name": "Beta LLC", "governing_law": "Delaware",
		},
	})
	if code != http.StatusCreated {
		t.Fatalf("generate = %d, want 201: %v", code, out)
	}
	doc := docFrom(out)
	id, _ := doc["id"].(string)
	if id == "" {
		t.Fatalf("generate returned no id: %v", out)
	}

	t.Run("a single read carries the body; a listing does not", func(t *testing.T) {
		if doc["body"] == nil || doc["contentType"] != "text/markdown" {
			t.Errorf("generate reply lost body/contentType: %v", doc)
		}
		// Not yet signed, not yet sent: both conditional keys stay ABSENT, exactly
		// as the map-building handler left them out.
		if _, ok := doc["esignProvider"]; ok {
			t.Errorf("esignProvider present on a draft: %v", doc)
		}
		if _, ok := doc["signedAt"]; ok {
			t.Errorf("signedAt present on an unsigned document: %v", doc)
		}

		code, out := do(t, app, http.MethodGet, "/v1/legal/documents", org, nil)
		if code != http.StatusOK {
			t.Fatalf("list = %d", code)
		}
		rows, _ := out["data"].([]any)
		if len(rows) != 1 {
			t.Fatalf("list returned %d rows, want 1", len(rows))
		}
		row := rows[0].(map[string]any)
		if _, ok := row["body"]; ok {
			t.Errorf("the listing leaked a rendered body: %v", row)
		}
		if _, ok := row["contentType"]; ok {
			t.Errorf("the listing carries contentType, which it never did: %v", row)
		}
		if out["disclaimer"] != APIDisclaimer {
			t.Errorf("the listing dropped its disclaimer")
		}
	})

	t.Run("the document reads are no-store", func(t *testing.T) {
		for _, path := range []string{"/v1/legal/documents", "/v1/legal/documents/" + id} {
			resp := sendRaw(t, app, http.MethodGet, path, org, "", nil)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("GET %s = %d", path, resp.StatusCode)
			}
			if cc := resp.Header.Get("Cache-Control"); cc != "no-store" {
				t.Errorf("GET %s Cache-Control = %q, want no-store", path, cc)
			}
		}
	})

	t.Run("the 1 MiB body cap still holds, and is not cloud's global limit", func(t *testing.T) {
		// A typed op receives its DECODED In, so a size check written inside one
		// would run after the parse it exists to precede. checkBody puts the gate
		// back in front; without it this package's 413 would silently become
		// whatever cloud's much larger global limit answers.
		big := make([]byte, maxBody+1)
		for _, tc := range []struct{ method, path string }{
			{http.MethodPost, "/v1/legal/documents"},
			{http.MethodPut, "/v1/legal/templates/nda"},
			{http.MethodPost, "/v1/legal/filings"},
			{http.MethodPost, "/v1/legal/documents/" + id + "/sign"},
		} {
			resp := sendRaw(t, app, tc.method, tc.path, org, "application/json", big)
			if resp.StatusCode != http.StatusRequestEntityTooLarge {
				t.Errorf("%s %s with an oversized body = %d, want 413", tc.method, tc.path, resp.StatusCode)
			}
		}
	})

	t.Run("403 outranks 413, and the completion is not capped", func(t *testing.T) {
		big := make([]byte, maxBody+1)
		// The org check has always run BEFORE decode, so an unvalidated caller is
		// refused as unauthenticated and never told how big its body may be.
		resp := sendRaw(t, app, http.MethodPost, "/v1/legal/documents", "", "application/json", big)
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("oversized body with no principal = %d, want 403 before 413", resp.StatusCode)
		}
		// The signature completion discards its decode error, so an oversized body
		// has always reached it and been ignored. Capping it would be a wire change
		// in the other direction.
		resp = sendRaw(t, app, http.MethodPost, "/v1/legal/documents/"+id+"/sign/complete",
			org, "application/json", big)
		if resp.StatusCode == http.StatusRequestEntityTooLarge {
			t.Errorf("the completion was capped at 413 — it has always ignored an oversized body")
		}
	})

	t.Run("an empty body is still tolerated and reaches the handler's own refusal", func(t *testing.T) {
		// decode() has always read an empty body as the zero value, so the answer
		// is the handler's 400 about the missing field — never a binder error about
		// the body's absence, and never a 413.
		code, out := do(t, app, http.MethodPost, "/v1/legal/documents", org, nil)
		if code != http.StatusBadRequest {
			t.Fatalf("empty generate body = %d, want 400", code)
		}
		// A refusal is an RFC 9457 problem document, so the sentence is `detail`.
		if msg, _ := out["detail"].(string); !strings.Contains(msg, "templateId") {
			t.Errorf("empty body answered %q, want the handler's templateId refusal", msg)
		}
	})

	t.Run("?limit binds through the typed op", func(t *testing.T) {
		if code, _ := do(t, app, http.MethodGet, "/v1/legal/documents?limit=abc", org, nil); code != http.StatusOK {
			t.Errorf("?limit=abc = %d, want 200 — an unparseable limit is not a refusal", code)
		}
		code, out := do(t, app, http.MethodGet, "/v1/legal/documents?limit=1", org, nil)
		if code != http.StatusOK {
			t.Fatalf("?limit=1 = %d", code)
		}
		if rows, _ := out["data"].([]any); len(rows) != 1 {
			t.Errorf("?limit=1 returned %d rows", len(rows))
		}
	})

	t.Run("a filing is 201 and refuses a cross-tenant document", func(t *testing.T) {
		code, out := do(t, app, http.MethodPost, "/v1/legal/filings", org,
			map[string]any{"documentIds": []string{id}, "jurisdiction": "DE"})
		if code != http.StatusCreated {
			t.Fatalf("filing = %d, want 201: %v", code, out)
		}
		if code, _ := do(t, app, http.MethodPost, "/v1/legal/filings", "other",
			map[string]any{"documentIds": []string{id}}); code != http.StatusNotFound {
			t.Errorf("cross-tenant filing = %d, want 404", code)
		}
	})

	t.Run("a sign request answers the provider handle without the body", func(t *testing.T) {
		code, out := do(t, app, http.MethodPost, "/v1/legal/documents/"+id+"/sign", org,
			map[string]any{"signers": []map[string]string{{"name": "Ada", "email": "ada@acme.com"}}})
		if code != http.StatusOK {
			t.Fatalf("sign = %d: %v", code, out)
		}
		if out["provider"] != "manual" || out["esignRef"] == nil {
			t.Errorf("sign reply = %v, want a manual provider and a ref", out)
		}
		d := docFrom(out)
		if _, ok := d["body"]; ok {
			t.Errorf("the sign reply leaked the rendered body: %v", d)
		}
		if d["esignProvider"] != "manual" {
			t.Errorf("esignProvider = %v, want manual once a request is open", d["esignProvider"])
		}
	})
}

// TestCompleteSignIgnoresAnUnparseableBody is the MEASUREMENT behind the single
// entry in untypedByDesign, kept as an assertion so the refusal stays checkable
// rather than becoming prose. completeSign DISCARDS its decode error
// (`_ = decode(c, &reqBody)`), so an unparseable body still drives the
// provider-reported completion; zip's invoke would refuse it with 400 before the
// handler ever ran.
func TestCompleteSignIgnoresAnUnparseableBody(t *testing.T) {
	app, _ := mount(t)
	const org = "org_tolerant"

	code, out := do(t, app, http.MethodPost, "/v1/legal/documents", org, map[string]any{
		"templateId": "nda",
		"data": map[string]string{
			"effective_date": "2026-01-01", "company_name": "Acme Inc.",
			"counterparty_name": "Beta LLC", "governing_law": "Delaware",
		},
	})
	if code != http.StatusCreated {
		t.Fatalf("generate = %d: %v", code, out)
	}
	id := docFrom(out)["id"].(string)
	if code, _ := do(t, app, http.MethodPost, "/v1/legal/documents/"+id+"/sign", org,
		map[string]any{"signers": []map[string]string{{"name": "Ada", "email": "a@b.c"}}}); code != http.StatusOK {
		t.Fatalf("sign request failed")
	}

	resp := sendRaw(t, app, http.MethodPost, "/v1/legal/documents/"+id+"/sign/complete",
		org, "application/json", []byte(`{not json`))
	if resp.StatusCode != http.StatusOK {
		t.Errorf("complete with an unparseable body = %d, want 200 — the handler discards the decode error",
			resp.StatusCode)
	}
}

// untypedByDesign is the CLOSED list of legal operations that are NOT typed ops,
// with the measured wire fact that keeps each out. Addresses are written the way
// the DOCUMENT writes them, which is the identity every projection keys on.
var untypedByDesign = map[string]string{
	"POST /v1/legal/documents/{id}/sign/complete": "DISCARDS its decode error " +
		"(`_ = decode(c, &reqBody)`, legal.go), so a caller may post an unparseable body and still " +
		"drive the provider-reported completion. zip's invoke refuses such a body BEFORE the handler " +
		"runs (typed.go:239), and an In cannot rescue it — encoding/json validates the whole document " +
		"before it will call a custom UnmarshalJSON. Measured: 200 untyped, 400 typed, pinned by " +
		"TestCompleteSignIgnoresAnUnparseableBody. The fix is in zip: an op needs a way to declare " +
		"that it tolerates a body it cannot parse.",
}

// legalOps reads BOTH projections of the live router at their one shared address
// form: what the document says is served, and which of those carry a typed registry
// entry. Reading the router (not the source) is what makes this a gate rather than
// prose — a route added anywhere in routes() shows up here.
func legalOps(t *testing.T) (served map[string]bool, typed map[string]string) {
	t.Helper()
	app, _ := mount(t)
	doc, err := openapi.Spec(app, openapi.Info{Title: "legal", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	reg, err := openapi.Typed(app)
	if err != nil {
		t.Fatalf("typed registry: %v", err)
	}
	ours := func(p string) bool { return strings.HasPrefix(p, "/v1/legal/") }
	served, typed = map[string]bool{}, map[string]string{}
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
			typed[key] = op.Description
		}
	}
	return served, typed
}

// TestEveryLegalRouteIsTypedOrNamed fails when a legal operation is neither a typed
// op nor the one named above — so the next route added here is typed by default,
// and dropping one out of the registry takes a deliberate edit with a reason.
func TestEveryLegalRouteIsTypedOrNamed(t *testing.T) {
	served, typed := legalOps(t)
	if len(served) == 0 {
		t.Fatal("the router serves no legal routes")
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
			"A route that is not a typed op has no schema, no prose, no MCP tool, no CLI command and "+
			"no SDK method.", strings.Join(untyped, ", "))
	}
	for key := range untypedByDesign {
		if !served[key] {
			t.Errorf("untypedByDesign names %q, which this router does not serve", key)
		}
	}
}

// TestEveryTypedLegalOpIsDescribed fails when a typed op reaches the document with
// no prose. zipdoc lifts the handler's doc comment into BOTH the OpenAPI
// description and the MCP tool description, so an op whose comment does not lift is
// an SDK method and an agent tool with nothing to read.
func TestEveryTypedLegalOpIsDescribed(t *testing.T) {
	_, typed := legalOps(t)
	if len(typed) == 0 {
		t.Fatal("no typed legal ops — the conversion is not wired")
	}
	var bare []string
	for key, desc := range typed {
		if strings.TrimSpace(desc) == "" {
			bare = append(bare, key)
		}
	}
	if len(bare) > 0 {
		sort.Strings(bare)
		t.Errorf("typed op(s) with no description: %s\n"+
			"Run: go generate -run zipdoc ./apps/legal/...", strings.Join(bare, ", "))
	}
}

// TestLegalHealthNeedsNoPrincipal pins the ONE route on this surface that does not
// read a tenant. It is the route cloud.Bridge could most easily have broken:
// Bridge parks the validated org for every other op, and if it REFUSED a request
// that carries none, the composer's root install would have turned liveness into
// a 403 — the failure mode where a subsystem reports itself down to every prober
// that (correctly) sends no tenant header.
func TestLegalHealthNeedsNoPrincipal(t *testing.T) {
	app, _ := mount(t)
	code, out := do(t, app, http.MethodGet, "/v1/legal/health", "", nil)
	if code != http.StatusOK {
		t.Fatalf("GET /health with no principal = %d, want 200", code)
	}
	if out["status"] != "ok" {
		t.Errorf("status = %v, want ok", out["status"])
	}
	if n, _ := out["templates"].(float64); int(n) != len(Builtins()) {
		t.Errorf("templates = %v, want %d", out["templates"], len(Builtins()))
	}
}

// proseless is the CLOSED list of published properties that carry NO description
// because the CLIENT they arrived through cannot carry one. Both causes below are
// zipdoc/schema-builder limitations with a line number, not fields nobody wrote:
// the prose is in the Go source in every case, and the two ends key it on
// different names.
//
// It is exact in BOTH directions. A bare property anywhere else goes red, and an
// entry here that starts publishing prose goes red too — that is the day the
// generator learns, and this ledger shrinks then rather than outliving the gap.
var proseless = map[string]bool{
	// EMBEDDED STRUCT. documentView embeds documentSummary (typed.go) so a single
	// read is the listing's shape PLUS a body, rather than two shapes that can
	// drift. encoding/json promotes the embedded fields onto the outer object and
	// zip's schema builder follows (openapi.go wireFields), so they publish as
	// documentView's OWN properties and are looked up under `documentView.<name>`
	// (structSchema). zipdoc files a field's prose under the type whose declaration
	// IS the struct literal, so the same ten comments are filed under
	// `documentSummary.<name>`. Both halves are right; the keys do not meet. The
	// prose is not missing from the document — documentSummary is published too and
	// carries all ten. Unrolling the embedding into a second copy of the ten fields
	// would close this by replacing one true statement with two that can drift.
	"documentView.category":        true,
	"documentView.createdAt":       true,
	"documentView.esignProvider":   true,
	"documentView.id":              true,
	"documentView.signedAt":        true,
	"documentView.status":          true,
	"documentView.templateId":      true,
	"documentView.templateVersion": true,
	"documentView.title":           true,
	"documentView.updatedAt":       true,

	// DEFINED TYPE. The fleet's schema namespace is flat, and three of legal's
	// domain types share a name with an already-published one, so typed.go declares
	// `type legalTemplate Template`, `type legalFiling Filing` and
	// `type legalSigner Signer` — same fields, same json tags, byte-identical wire,
	// a name that says which plane it belongs to.
	//
	// zipdoc reaches a struct's comments through its TypeSpec and returns unless
	// that spec is an *ast.StructType (internal/zipdoc/extract.go structFields); a
	// defined type's spec is an *ast.Ident, so nothing is recorded under
	// `legalTemplate.*`. Its field walk then descends into each field's TYPE, which
	// is why Field.key and Field.label do publish — but it never visits Template,
	// Filing or Signer as named types, so nothing is recorded under those names
	// either. The schema builder asks for `legalTemplate.title`, which no run of
	// zipdoc can ever produce. Same class as apps/billing's `accounts`
	// (`type accounts link.AccountsUsage`).
	//
	// The fields carry their doc comments on Template and Filing (model.go) and
	// Signer (providers.go), so the source is right and this closes the day zipdoc
	// follows a defined type to its underlying declaration. Restating the struct
	// literal under the new name would close it today by putting the same eight
	// fields in two places.
	"legalTemplate.body":          true,
	"legalTemplate.category":      true,
	"legalTemplate.counselReview": true,
	"legalTemplate.fields":        true,
	"legalTemplate.id":            true,
	"legalTemplate.origin":        true,
	"legalTemplate.title":         true,
	"legalTemplate.version":       true,

	"legalFiling.createdAt":    true,
	"legalFiling.documentIds":  true,
	"legalFiling.id":           true,
	"legalFiling.jurisdiction": true,
	"legalFiling.note":         true,
	"legalFiling.org":          true,
	"legalFiling.provider":     true,
	"legalFiling.status":       true,
	"legalFiling.updatedAt":    true,

	"legalSigner.email": true,
	"legalSigner.name":  true,
}

// TestEveryPublishedFieldIsDescribed closes the half of the surface the gates above
// cannot see. Typing a route documents its ADDRESS and its SHAPE; the shape's FIELDS
// come from a different place — a doc comment on each one, which zipdoc lifts one at
// a time — so a fully typed surface can still publish a wholly unreadable document.
//
// It matters here because almost every scalar on this surface is a closed vocabulary
// or a rule, not a label. `status` is draft | out_for_signature | signed | voided and
// deliberately has no "legally valid" member, because validity is counsel's call and
// not the platform's; a filing's `status` is manual | submitted | filed | rejected,
// where manual means NOTHING was filed and the org must go to its registered agent.
// `origin` (builtin | org) and `version` (a built-in is 1, an org's first override is
// 2) are together the only way to tell whose text a document was rendered from, which
// is why every document records templateVersion. And a merge field's `key` is what
// the body substitutes while its `label` is only a prompt for a person — send the
// label and the generation fails closed rather than rendering a blank into a contract.
//
// Presence is all a gate can check. A description restating the field's name is worse
// than none, and only a reader catches that.
func TestEveryPublishedFieldIsDescribed(t *testing.T) {
	app, _ := mount(t)
	doc, err := openapi.Spec(app, openapi.Info{Title: "legal", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	if doc.Components == nil || len(doc.Components.Schemas) == 0 {
		t.Fatal("legal publishes no schemas at all — the gate would pass vacuously")
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
			"the first of them alone — then run: make -C apps/legal describe",
			len(bare), strings.Join(bare, ", "))
	}
	if len(stale) > 0 {
		sort.Strings(stale)
		t.Errorf("proseless names propert(ies) that are gone or now described: %s\n"+
			"An exemption that outlives its cause is how a generator gap becomes permanent — "+
			"delete the entr(ies).", strings.Join(stale, ", "))
	}
}
