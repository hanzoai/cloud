package books

// projection_test.go — what typing the /v1/books surface actually BOUGHT, measured.
//
// wire_test.go proves the answers did not move. That is the safety half of a typed-op
// migration, and on its own it would justify nothing: leaving every handler untyped
// also moves no answer. THIS file is the other half — the value — and it exists
// because the claim being made about books is a claim about four surfaces at once:
//
//	OpenAPI   the document every SDK repo regenerates from
//	MCP       the tool an agent picks by READING the handler's prose
//	CLI       the generated `hanzo books …` command
//	op-call   the operation id that addresses the op by name across all three
//
// An untyped route has exactly ONE of those: the HTTP route. It is in no document, is
// no agent tool, has no command, and cannot be reached by name. So "typed" is not a
// style preference here — it is the difference between a route that exists and a route
// that is part of the product. Asserting it once, over the WHOLE surface, is what keeps
// a later refactor from silently dropping a projection while every wire test stays green.
//
// The two ledgers below are exhaustive and mutually exclusive: every /v1/books route is
// in exactly one of them. That is deliberate. A route added without being declared here
// goes red, and so does a route whose typing status CHANGES — including in the good
// direction, which is the point: the exemption list is the migration's remaining debt,
// and debt nobody is forced to look at is how an exemption becomes permanent.

import (
	"encoding/json"
	"maps"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/openapi"
)

// booksOp is one typed op and the identity it carries into every projection. The CLI
// name is spelled out rather than derived: it is a PUBLISHED command name, so it is a
// contract, and a generator change that renames `books scan-book-create` should have to
// say so here.
type booksOp struct {
	method, path string
	op           string // operationId — the one token all four surfaces agree on
	cmd          string // the generated CLI command, under service "books"
}

// typedBooksOps is every /v1/books route declared as a typed op — the 20 that project.
var typedBooksOps = []booksOp{
	{http.MethodGet, "/v1/books/accounts", "get_books_accounts", "accounts-list"},
	{http.MethodGet, "/v1/books/gl", "get_books_gl", "gl-get"},
	{http.MethodGet, "/v1/books/trial", "get_books_trial", "trial-get"},
	{http.MethodGet, "/v1/books/pnl", "get_books_pnl", "pnl-get"},
	{http.MethodGet, "/v1/books/position", "get_books_position", "position-get"},
	{http.MethodGet, "/v1/books/export", "get_books_export", "export-get"},
	{http.MethodGet, "/v1/books/questions", "get_books_questions", "questions-list"},
	{http.MethodGet, "/v1/books/metrics", "get_books_metrics", "metrics-list"},
	{http.MethodGet, "/v1/books/inbox", "get_books_inbox", "inbox-get"},
	{http.MethodGet, "/v1/books/vendors", "get_books_vendors", "vendors-list"},
	{http.MethodGet, "/v1/books/rules", "get_books_rules", "rules-list"},
	{http.MethodGet, "/v1/books/transactions", "get_books_transactions", "transactions-list"},
	{http.MethodGet, "/v1/books/bank/transactions", "get_books_bank_transactions", "bank-transactions-list"},
	{http.MethodGet, "/v1/books/bank/unreconciled", "get_books_bank_unreconciled", "bank-unreconciled-get"},
	{http.MethodPost, "/v1/books/sync", "post_books_sync", "sync-create"},
	{http.MethodPost, "/v1/books/ask", "post_books_ask", "ask-create"},
	{http.MethodPost, "/v1/books/scan/book", "post_books_scan_book", "scan-book-create"},
	{http.MethodPost, "/v1/books/vendors", "post_books_vendors", "vendors-create"},
	{http.MethodPost, "/v1/books/rules", "post_books_rules", "rules-create"},
	{http.MethodPost, "/v1/books/bank/sync", "post_books_bank_sync", "bank-sync-create"},
}

// untypedBooksRoutes is the EXEMPTION LEDGER: the five routes that are still raw
// handlers, each with the reason it cannot become a typed op without MOVING ITS WIRE —
// which typing, being a description task, is not allowed to do.
//
// Three of them take RAW DOCUMENT BYTES as their body (a PDF, an image, an OFX/QFX/CSV
// statement). zip's typed path decodes the body with jsonenc.Unmarshal and answers
// ErrBadRequest on failure (zip typed.go, op.invoke), so declaring any In on these
// would turn a working PDF upload into a 400. There is no In that names a file: zip
// v1.18.7 has no octet-stream/binary request declaration, and its whole OpOption set is
// WithSummary/WithTags/WithOperationID/WithStatus. Typing these needs a zip capability
// that does not exist yet, not a books change — and faking it with a custom
// UnmarshalJSON would be worse than leaving them out, because the document would then
// tell every generated SDK to send JSON to a route that eats bytes.
//
// The other two answer 501 UNCONDITIONALLY, and a typed op must declare what it returns
// on SUCCESS. Publishing an Out for a response that has never once been sent would put a
// return type in every generated SDK for a call that cannot succeed — an invented
// contract, which is worse than an undocumented route. They also do not read their body
// today, so any bytes at all are accepted; a typed op would start refusing non-JSON with
// a 400 the route has never sent. See bank_api.go for the dead link flow behind them.
//
// "Cannot be a typed op" is not "must be undocumented", and the split between these two
// groups is exactly the split between what can still be declared and what cannot. The
// three byte-body routes DO declare both halves through openapi.Register (a Binary
// request, their own response view) — see declaredRawBodies below. The two 501 stubs
// declare nothing, because there is nothing true to say.
var untypedBooksRoutes = []struct {
	method, path, why string
}{
	{http.MethodPost, "/v1/books/scan", "body is raw document bytes (PDF/image/text)"},
	{http.MethodPost, "/v1/books/inbox", "body is raw document bytes (PDF/image/text)"},
	{http.MethodPost, "/v1/books/bank/import", "body is a raw OFX/QFX/CSV statement"},
	{http.MethodPost, "/v1/books/bank/token", "answers 501 unconditionally — no success to declare"},
	{http.MethodPost, "/v1/books/bank/exchange", "answers 501 unconditionally — no success to declare"},
}

// TestEveryTypedBooksOpProjectsToAllFourSurfaces is the value assertion for the whole
// surface: each of the 20 ops reaches the document WITH ITS PROSE and a declared
// response, reaches MCP as a tool carrying that same prose, and reaches the CLI as a
// `hanzo books …` command — all under ONE operation id. A projection that silently
// stopped being derived (a generator regression, a route re-registered as a raw handler)
// goes red here even though every wire test would still pass.
func TestEveryTypedBooksOpProjectsToAllFourSurfaces(t *testing.T) {
	app := mountBooks(t)

	paths, _ := app.OpenAPISpec()["paths"].(map[string]map[string]any)
	tools := map[string]map[string]any{}
	for _, x := range app.MCPTools() {
		name, _ := x["name"].(string)
		tools[name] = x
	}
	type cmdInfo struct{ service, name string }
	cmds := map[string]cmdInfo{}
	for _, c := range app.Commands() {
		cmds[c.OperationID] = cmdInfo{c.Service, c.Name}
	}

	for _, want := range typedBooksOps {
		// ---- 1. OpenAPI ------------------------------------------------------
		item, ok := paths[want.path]
		if !ok {
			t.Errorf("%s %s: absent from the document — the op is not projected at all", want.method, want.path)
			continue
		}
		op, _ := item[strings.ToLower(want.method)].(map[string]any)
		if op == nil {
			t.Errorf("%s %s: the path is documented but this method is not", want.method, want.path)
			continue
		}
		if got, _ := op["operationId"].(string); got != want.op {
			t.Errorf("%s %s: operationId %q, want %q", want.method, want.path, got, want.op)
		}
		// Prose is PRODUCT SURFACE: it is what a human reads in the reference and
		// what a model reads to pick a tool. An op without it is a schema nobody
		// can choose correctly.
		if d, _ := op["description"].(string); strings.TrimSpace(d) == "" {
			t.Errorf("%s %s: no description — the handler's doc comment never reached the document", want.method, want.path)
		}
		if _, ok := op["responses"]; !ok {
			t.Errorf("%s %s: declares no response — the SDK has no return type to generate", want.method, want.path)
		}

		// ---- 2. MCP ----------------------------------------------------------
		tool, ok := tools[want.op]
		if !ok {
			t.Errorf("%s: no MCP tool — the op is invisible to agents", want.op)
		} else if d, _ := tool["description"].(string); strings.TrimSpace(d) == "" {
			t.Errorf("%s: MCP tool carries no description — an agent cannot tell what it does", want.op)
		}

		// ---- 3. CLI ----------------------------------------------------------
		got, ok := cmds[want.op]
		if !ok {
			t.Errorf("%s: no generated command", want.op)
		} else if got.service != "books" || got.name != want.cmd {
			t.Errorf("%s: command %q %q, want %q %q", want.op, got.service, got.name, "books", want.cmd)
		}

		// ---- 4. One op, one name across all three ----------------------------
		if tool != nil && tool["name"] != op["operationId"] {
			t.Errorf("%s %s: document says %v, tool says %v — one op must have one name",
				want.method, want.path, op["operationId"], tool["name"])
		}
	}

	// The ledger is EXHAUSTIVE, not a sample: an op added to the surface without being
	// declared above would otherwise be measured by nothing.
	var documented int
	for p, item := range paths {
		if strings.HasPrefix(p, "/v1/books") {
			documented += len(item)
		}
	}
	if documented != len(typedBooksOps) {
		t.Errorf("the document carries %d /v1/books operations, the ledger names %d — add the new op to typedBooksOps (and to untypedBooksRoutes if it is a raw handler)",
			documented, len(typedBooksOps))
	}
}

// TestTheUntypedBooksRoutesAreLiveButProjectNothing pins the migration's remaining debt,
// in both directions.
//
// Each exempt route must still be a REAL, fail-closed route — a 401 without a principal,
// never a 404 — so the ledger cannot rot into naming paths that no longer exist. And
// each must still be absent from all three derived surfaces, which is the cost of the
// exemption stated as a measurement rather than a comment.
//
// If this goes red because a route is now typed: that is the WIN this file is waiting
// for. Move the entry from untypedBooksRoutes into typedBooksOps.
func TestTheUntypedBooksRoutesAreLiveButProjectNothing(t *testing.T) {
	app := mountBooks(t)

	paths, _ := app.OpenAPISpec()["paths"].(map[string]map[string]any)
	tools := map[string]bool{}
	for _, x := range app.MCPTools() {
		name, _ := x["name"].(string)
		tools[name] = true
	}
	cmds := map[string]bool{}
	for _, c := range app.Commands() {
		cmds[c.OperationID] = true
	}

	for _, r := range untypedBooksRoutes {
		// The route is real and fail-closed. A 404 here means the ledger is stale.
		if code, _, out := hit(t, app, r.method, r.path, "", []byte(`{}`)); code != http.StatusUnauthorized {
			t.Errorf("%s %s: status %d, want 401 — an exempt route must still be a live, fail-closed route (%s)",
				r.method, r.path, code, out)
		}
		// …and it projects nothing. The op id zip WOULD mint for it, absent everywhere.
		id := opIDFor(r.method, r.path)
		if item, ok := paths[r.path]; ok {
			if _, ok := item[strings.ToLower(r.method)]; ok {
				t.Errorf("%s %s is now a typed op (%s) — move it from untypedBooksRoutes into typedBooksOps: %s",
					r.method, r.path, id, r.why)
			}
		}
		if tools[id] {
			t.Errorf("%s is now an MCP tool — move it into typedBooksOps", id)
		}
		if cmds[id] {
			t.Errorf("%s is now a CLI command — move it into typedBooksOps", id)
		}
	}

	// Exhaustive and mutually exclusive: every /v1/books route is in exactly one ledger.
	if n := len(typedBooksOps) + len(untypedBooksRoutes); n != 25 {
		t.Errorf("the two ledgers cover %d routes, the /v1/books surface has 25 — a route was added without being declared in either", n)
	}
}

// declaredRawBodies is the three exempt routes that still SAY what they take and
// answer, mapped to the response component the document must publish for each. The
// value is the Go view type the handler marshals on success, which is the whole point:
// the declaration is read off the code (openapi.Register in scan.go / bank_api.go), so
// it cannot drift from what the route really returns.
var declaredRawBodies = map[string]string{
	"POST /v1/books/scan":        "ScanDraft",
	"POST /v1/books/inbox":       "InboxItem",
	"POST /v1/books/bank/import": "BankTally",
}

// TestTheRawBodyRoutesDeclareBytesInAndAShapeOut is the OTHER half of the exemption:
// the three routes cannot be typed ops, but the cost of that must be exactly one
// missing thing (prose, MCP, CLI) and not four.
//
// Before this, all three published operationId and tags and NOTHING else — no request
// body, no response. That is indistinguishable, to every consumer of the document,
// from a route that takes no body and returns nothing, so an SDK generated off
// openapi.yaml offered `scan()` with nowhere to put the receipt and no return type. The
// request is declared as bytes (application/octet-stream, string/binary — OpenAPI's own
// spelling for an opaque body, and the one an SDK generator turns into a file
// parameter), and the response as the view the handler actually marshals.
//
// The two 501 stubs are asserted to declare NOTHING, in the same test, because that
// silence is a decision and not an omission: a route that has never once succeeded has
// no success body to state, and stating one would be invention.
func TestTheRawBodyRoutesDeclareBytesInAndAShapeOut(t *testing.T) {
	app := mountBooks(t)
	doc, err := openapi.Spec(app, openapi.Info{Title: "books", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}

	for key, wantComponent := range declaredRawBodies {
		method, path, _ := strings.Cut(key, " ")
		op := doc.Paths[path][strings.ToLower(method)]
		if op == nil {
			t.Errorf("%s is not served — the ledger is stale", key)
			continue
		}
		// The request: bytes, under the content type that means bytes.
		var req struct {
			Content map[string]struct {
				Schema struct {
					Type   string `json:"type"`
					Format string `json:"format"`
				} `json:"schema"`
			} `json:"content"`
		}
		remarshal(t, op.RequestBody, &req)
		media, ok := req.Content["application/octet-stream"]
		if !ok {
			t.Errorf("%s declares no application/octet-stream request — the declaration in "+
				"openapi.Register is what tells an SDK this route takes a file; have %v",
				key, slices.Sorted(maps.Keys(req.Content)))
			continue
		}
		if media.Schema.Type != "string" || media.Schema.Format != "binary" {
			t.Errorf("%s request schema = %s/%s, want string/binary",
				key, media.Schema.Type, media.Schema.Format)
		}
		// The response: the view the handler marshals, as a shared component.
		var resp map[string]struct {
			Content map[string]struct {
				Schema struct {
					Ref string `json:"$ref"`
				} `json:"schema"`
			} `json:"content"`
		}
		remarshal(t, op.Responses, &resp)
		success, ok := resp["2XX"]
		if !ok {
			t.Errorf("%s declares no success response — the SDK has no return type; have %v",
				key, slices.Sorted(maps.Keys(resp)))
			continue
		}
		wantRef := "#/components/schemas/" + wantComponent
		if got := success.Content["application/json"].Schema.Ref; got != wantRef {
			t.Errorf("%s success schema $ref = %q, want %q", key, got, wantRef)
		}
		if doc.Components == nil || doc.Components.Schemas[wantComponent] == nil {
			t.Errorf("%s $refs %s, which the document does not define", key, wantRef)
		}
	}

	// The 501 stubs declare nothing — asserted, so a later "let's document these too"
	// has to argue with a test instead of quietly inventing a contract.
	for _, key := range []string{
		"POST /v1/books/bank/token",
		"POST /v1/books/bank/exchange",
	} {
		method, path, _ := strings.Cut(key, " ")
		op := doc.Paths[path][strings.ToLower(method)]
		if op == nil {
			t.Errorf("%s is not served — the ledger is stale", key)
			continue
		}
		if op.RequestBody != nil || op.Responses != nil {
			t.Errorf("%s declares a body (%v / %v) — it answers 501 unconditionally, so any "+
				"declared shape is invented, not described", key, op.RequestBody, op.Responses)
		}
	}
}

// remarshal reads a document value the way a consumer does — through JSON — rather
// than through the structs that built it, which is the only reading that proves what an
// SDK generator will see.
func remarshal(t *testing.T, from, into any) {
	t.Helper()
	b, err := json.Marshal(from)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := json.Unmarshal(b, into); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
}

// TestBookScanCarriesItsSchemaProseAndExample is the ONE op inspected in full, so the
// suite above can assert presence cheaply while something still pins the QUALITY of a
// projection: the doc comment reaches the description verbatim, the In type reaches the
// document as a shared $ref rather than an inlined copy, the Out is a declared 200
// schema, and the doc comment's own Example rides along — which is what makes a
// reference pressable rather than merely readable.
//
// scan/book is the op to hold to that standard: it is the scanner's only write, so it is
// the books call an agent is most likely to reach for and the one a wrong description
// would do real damage with.
func TestBookScanCarriesItsSchemaProseAndExample(t *testing.T) {
	app := mountBooks(t)
	paths, _ := app.OpenAPISpec()["paths"].(map[string]map[string]any)
	op, _ := paths["/v1/books/scan/book"]["post"].(map[string]any)
	if op == nil {
		t.Fatal("no POST /v1/books/scan/book in the document")
	}

	// The prose, verbatim from the handler's doc comment (scan.go).
	desc, _ := op["description"].(string)
	for _, phrase := range []string{
		"the scanner's ONLY write",
		"idempotent by (scan, scanId)",
		"refused 409 unless",
	} {
		if !strings.Contains(desc, phrase) {
			t.Errorf("the description does not carry %q from the doc comment:\n%s", phrase, desc)
		}
	}

	body, _ := op["requestBody"].(map[string]any)
	media, _ := body["content"].(map[string]any)["application/json"].(map[string]any)
	sch, _ := media["schema"].(map[string]any)
	if ref, _ := sch["$ref"].(string); ref != "#/components/schemas/BookRequest" {
		t.Errorf("requestBody schema = %v, want a $ref to BookRequest — one definition, shared", sch)
	}
	// The Example from the doc comment, so the reference is pressable.
	if ex, ok := media["example"]; !ok {
		t.Error("no example on the request body — the doc comment's Example line did not reach the document")
	} else if b, _ := json.Marshal(ex); !strings.Contains(string(b), "a5f3c1") {
		t.Errorf("example = %s, want the doc comment's own", b)
	}

	resp, _ := op["responses"].(map[string]any)
	ok200, _ := resp["200"].(map[string]any)
	if ok200 == nil {
		t.Fatalf("no 200 declared, responses = %v", resp)
	}
	outMedia, _ := ok200["content"].(map[string]any)["application/json"].(map[string]any)
	outSch, _ := outMedia["schema"].(map[string]any)
	if ref, _ := outSch["$ref"].(string); ref != "#/components/schemas/BookResponse" {
		t.Errorf("200 schema = %v, want a $ref to BookResponse", outSch)
	}

	// The MCP tool an agent sees names the In's fields, which is how it fills them.
	var in map[string]any
	for _, x := range app.MCPTools() {
		if x["name"] == "post_books_scan_book" {
			in, _ = x["inputSchema"].(map[string]any)
		}
	}
	if in == nil {
		t.Fatal("no MCP tool post_books_scan_book")
	}
	props, _ := in["properties"].(map[string]any)
	for _, f := range []string{"scanId", "voucher", "override"} {
		if _, ok := props[f]; !ok {
			t.Errorf("the tool's inputSchema has no %q — an agent cannot fill a field it cannot see", f)
		}
	}
}

// opIDFor is the operation id zip WOULD mint for a route: lowercased method followed by
// the path with its separators flattened to underscores (zip openapi.go, defaultOpID —
// the leading slash becomes the separating underscore, which is why there is none here).
// Recomputed rather than read off a registered op, because the whole point is to look for
// ids that are NOT registered. None of the exempt paths carry a {param} or :param, so the
// brace/colon stripping defaultOpID also does has nothing to do here.
func opIDFor(method, path string) string {
	return strings.ToLower(method) + strings.ReplaceAll(path, "/", "_")
}
