package index

// This file makes the index's typed partition a GATE rather than a paragraph,
// and its WIRE a measurement rather than a claim.
//
// Two things are being held. First, that every route here is a typed op or is
// named below with the wire fact that keeps it raw — so the next route added is
// typed by default, and a route that drops out of the registry takes a
// deliberate edit carrying a reason. Second, and this is the one the dialect
// makes load-bearing: that turning fifteen assembled maps into Go types did not
// move a single byte. A Meilisearch client parses these bodies and branches on
// their codes, so "equal as JSON" is not the bar — the bar is the same bytes.

import (
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/openapi"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"

	// devmaster keys this test binary: cek opens nothing without a master and a
	// test process has no KMS.
	_ "github.com/hanzoai/cloud/internal/devmaster"
)

// untypedByDesign is the CLOSED list of index operations that are NOT typed ops,
// each with the WIRE FACT that typing it would move. Keyed the way the DOCUMENT
// writes an address, which is the identity every projection reads.
var untypedByDesign = map[string]string{
	"POST /v1/index/indexes/{uid}/documents":              reasonArrayBody,
	"PUT /v1/index/indexes/{uid}/documents":               reasonArrayBody,
	"POST /v1/index/indexes/{uid}/documents/delete-batch": reasonArrayBody,
}

// reasonArrayBody is the one reason all three share, verified against the PINNED
// zip (v1.31.0) rather than inherited as prose.
//
// Each of the three takes a TOP-LEVEL JSON ARRAY as its body — `[doc,…]` on the
// two upserts, `[id,…]` on delete-batch — and each hangs off a `:uid` path
// segment. Those two facts are individually fine and jointly fatal:
//
//   - zip binds path and query values by walking the input's STRUCT FIELDS
//     (bindURL, typed.go:317 "Only the top level is walked", and it returns early
//     for a non-struct kind), so an In declared as a slice binds no `uid` at all
//     and the write would land in whatever index the body happened to name — or
//     in none.
//   - an In declared as a struct is decoded with jsonenc.Unmarshal
//     (typed.go:487), which fails on `[` against a struct and turns today's 202
//     into a 400 on every call the JS client makes.
//
// zip DOES accept a non-struct In — hasRequestBody (openapi.go:413) says so in
// as many words, "the body IS the whole value — a list, a raw message" — so the
// array is not the problem by itself. It is the array AND the path parameter.
// The cure is upstream and small: bindURL would have to reach a named field
// through a struct that also carries the list, which is a zip capability that
// does not exist today.
//
// The cost of staying untyped is exactly three things — prose lifted from a doc
// comment, an MCP tool, a CLI command — and NOT a fourth, a document that says
// these routes take no body: openapi.Register + openapi.OneOf declare the two
// shapes each accepts, and TestTheUntypedWritesStillDeclareTheirBodies pins it.
const reasonArrayBody = "the body is a TOP-LEVEL JSON ARRAY on a :uid route. zip binds a path " +
	"parameter by walking the input's struct fields (bindURL, zip v1.31.0 typed.go:317 — it returns " +
	"early for a non-struct kind), so a slice In binds no uid; and a struct In is decoded with " +
	"jsonenc.Unmarshal (typed.go:487), which fails on '[' and turns today's 202 into a 400. Either " +
	"half alone is expressible — hasRequestBody (openapi.go:413) admits a list body — the pair is not."

// indexApp mounts the REAL Mount on a bare app, so every gate below reads the
// router the binary serves rather than a reconstruction of it.
func indexApp(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test"), DisableStartupMessage: true})
	if err := Use(app, cloud.Deps{DataDir: t.TempDir()}); err != nil {
		t.Fatalf("Use:  %v", err)
	}
	t.Cleanup(func() { _ = Shutdown() })
	return app
}

// call drives one request through the live router. org == "" sends no identity
// at all, which is the anonymous case the dialect refuses with invalid_api_key.
func call(t *testing.T, app *zip.App, method, path, org, body string) (int, []byte) {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, "http://x"+path, rdr)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if org != "" {
		req.Header.Set("X-Org-Id", org)
		req.Header.Set("X-User-Id", "u@"+org)
	}
	res, err := app.Test(req)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	out, _ := io.ReadAll(res.Body)
	return res.StatusCode, out
}

// ---- the partition ---------------------------------------------------------

// TestEveryRouteIsTypedOrNamed fails when an index operation is neither a typed
// op nor named above, and fails on a stale reason naming a route the index no
// longer serves. The two ledgers must SUM to the served surface, so neither can
// drift without the other noticing.
func TestEveryRouteIsTypedOrNamed(t *testing.T) {
	app := indexApp(t)
	doc, err := openapi.Spec(app, openapi.Info{Title: "index", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	reg, err := openapi.Typed(app)
	if err != nil {
		t.Fatalf("typed registry: %v", err)
	}
	served := map[string]bool{}
	for path, item := range doc.Paths {
		if !strings.HasPrefix(path, "/v1/index") {
			continue
		}
		for method := range item {
			served[strings.ToUpper(method)+" "+path] = true
		}
	}
	if len(served) == 0 {
		t.Fatal("the index serves nothing at all — the router moved and this gate is now blind")
	}
	typed := map[string]bool{}
	for key := range reg.Ops {
		if i := strings.Index(key, " "); i > 0 && strings.HasPrefix(key[i+1:], "/v1/index") {
			typed[key] = true
		}
	}
	var untyped []string
	for key := range served {
		if typed[key] || untypedByDesign[key] != "" {
			continue
		}
		untyped = append(untyped, key)
	}
	if len(untyped) > 0 {
		sort.Strings(untyped)
		t.Errorf("untyped and unnamed: %s\nAn untyped route projects to NOTHING — no prose, no MCP tool, "+
			"no CLI command, no typed SDK method. Convert it (zip.Get/Post/... on the group), or add it to "+
			"untypedByDesign with the WIRE FACT that typing it would move.", strings.Join(untyped, ", "))
	}
	for key := range untypedByDesign {
		if !served[key] {
			t.Errorf("untypedByDesign names %s, which the index does not serve — a stale reason nobody can re-check", key)
		}
		if typed[key] {
			t.Errorf("untypedByDesign names %s, which IS a typed op — remove the reason", key)
		}
	}
	if len(typed)+len(untypedByDesign) != len(served) {
		t.Errorf("%d typed + %d named != %d served", len(typed), len(untypedByDesign), len(served))
	}
	if len(typed) != 14 || len(untypedByDesign) != 3 {
		t.Errorf("the partition moved: %d typed, %d named (was 14 + 3). Update this count deliberately, "+
			"so the prose in LLM.md and the HIP cannot drift from the binary.", len(typed), len(untypedByDesign))
	}
}

// TestEveryTypedOpIsDescribed proves the prose survived the migration into doc
// comments. zipdoc lifts it at build time, so a package whose //go:generate
// directive is missing publishes a fully typed surface that says nothing.
func TestEveryTypedOpIsDescribed(t *testing.T) {
	reg, err := openapi.Typed(indexApp(t))
	if err != nil {
		t.Fatalf("typed registry: %v", err)
	}
	for key, op := range reg.Ops {
		if !strings.Contains(key, "/v1/index") {
			continue
		}
		if strings.TrimSpace(op.Description) == "" {
			t.Errorf("%s carries no description — its doc comment did not reach zipdoc_gen.go", key)
		}
		if strings.Contains(op.Summary, "\n") {
			t.Errorf("%s publishes a multi-line summary %q — the summary is a one-line field in "+
				"OpenAPI, the CLI and every generated SDK", key, op.Summary)
		}
	}
}

// TestEveryPublishedFieldIsDescribed gates the RESPONSE side, which the op-level
// gate above cannot see. Typing a route documents its ADDRESS and its SHAPE; it
// does not document the shape's FIELDS, and a caller who can see that a task
// carries `status` and nowhere that it is always `succeeded` has been told the
// name of a thing and not the fact about it.
func TestEveryPublishedFieldIsDescribed(t *testing.T) {
	reg, err := openapi.Typed(indexApp(t))
	if err != nil {
		t.Fatalf("typed registry: %v", err)
	}
	var bare []string
	for name, raw := range reg.Schemas {
		def, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		props, _ := def["properties"].(map[string]any)
		for field, p := range props {
			s, ok := p.(map[string]any)
			if !ok {
				continue
			}
			if d, _ := s["description"].(string); strings.TrimSpace(d) == "" {
				bare = append(bare, name+"."+field)
			}
		}
	}
	if len(bare) > 0 {
		sort.Strings(bare)
		t.Errorf("%d published properties carry no description: %s\nThey reach openapi.yaml, every "+
			"generated SDK and every MCP inputSchema bare. Describe the FIELD in the Go struct — zipdoc "+
			"lifts it.", len(bare), strings.Join(bare, ", "))
	}
}

// TestTheUntypedWritesStillDeclareTheirBodies proves the three refusals cost
// three things and not a fourth. Without the Register declarations each renders
// as an operationId and a tag and NOTHING else — indistinguishable from a route
// that takes no input — so every SDK generated off the document offered a
// document upload with nowhere to put the documents.
func TestTheUntypedWritesStillDeclareTheirBodies(t *testing.T) {
	doc, err := openapi.Spec(indexApp(t), openapi.Info{Title: "index", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	for key := range untypedByDesign {
		method, path, _ := strings.Cut(key, " ")
		op := doc.Paths[path][strings.ToLower(method)]
		if op == nil {
			t.Fatalf("%s is not in the document", key)
		}
		if op.RequestBody == nil {
			t.Errorf("%s declares no request body — an SDK generated from this offers the call with "+
				"nowhere to put the documents", key)
			continue
		}
		raw, err := json.Marshal(op.RequestBody)
		if err != nil {
			t.Fatalf("marshal request body: %v", err)
		}
		// The ARRAY is what every one of these declares, because the array body is
		// the reason all three are here (reasonArrayBody). It used to read
		// `contains "oneOf"`, which is a different claim: true of the two upserts,
		// which take a single document as well as a list, and false of
		// delete-batch, which is always a list. Asserting the union made
		// delete-batch declare one — `[]string` or `[]float64` — and that is a
		// body no generator can name, so hanzo-js/sdk-fetch emitted a syntax error
		// for it and had not compiled since v8.5.102.
		if !strings.Contains(string(raw), `"type":"array"`) {
			t.Errorf("%s declares no array body (%s) — an SDK generated from this cannot send the "+
				"list the wire takes", key, raw)
		}
		if op.Responses == nil {
			t.Errorf("%s declares no response — the receipt it answers with is invisible", key)
		}
	}
}

// ---- the wire --------------------------------------------------------------

// TestTypedAnswersAreByteIdenticalToTheMaps is the measurement the dialect makes
// load-bearing. Each case is the map literal the untyped handler assembled,
// beside the typed value that replaced it: encoding/json writes a map in SORTED
// KEY ORDER, so a model whose fields are not in alphabetical json-tag order
// produces different BYTES for the same value — and a Meilisearch client that
// diffs or hashes a response would see a changed body for an unchanged fact.
func TestTypedAnswersAreByteIdenticalToTheMaps(t *testing.T) {
	for _, tc := range []struct {
		name  string
		was   any
		typed any
	}{
		{"health", map[string]string{"status": "available"}, &indexHealth{Status: "available"}},
		{"version", map[string]string{"pkgVersion": Version, "commitSha": "hanzo-cloud", "commitDate": ""},
			&indexVersion{CommitDate: "", CommitSha: "hanzo-cloud", PkgVersion: Version}},
		{"index", map[string]any{"uid": "messages", "primaryKey": "id", "createdAt": "t0", "updatedAt": "t1"},
			view(Index{UID: "messages", PrimaryKey: "id", CreatedAt: "t0", UpdatedAt: "t1"})},
		{"indexes", map[string]any{
			"results": []map[string]any{{"uid": "m", "primaryKey": "id", "createdAt": "t0", "updatedAt": "t1"}},
			"offset":  0, "limit": 1, "total": 1},
			&indexList{Limit: 1, Offset: 0, Total: 1,
				Results: []indexView{view(Index{UID: "m", PrimaryKey: "id", CreatedAt: "t0", UpdatedAt: "t1"})}}},
		{"stats", map[string]any{"databaseSize": 3,
			"indexes": map[string]any{"m": map[string]any{"numberOfDocuments": 3, "isIndexing": false}}},
			&indexStats{DatabaseSize: 3, Indexes: map[string]indexCount{"m": {NumberOfDocuments: 3}}}},
		{"settings", map[string]any{"filterableAttributes": []string{"user"}},
			&indexSettings{FilterableAttributes: []string{"user"}}},
		{"enqueued", map[string]any{"taskUid": int64(7), "indexUid": "m", "status": "enqueued",
			"type": "documentAdditionOrUpdate", "enqueuedAt": "t0"},
			&indexEnqueued{EnqueuedAt: "t0", IndexUID: "m", Status: "enqueued", TaskUID: 7,
				Type: "documentAdditionOrUpdate"}},
		{"task", map[string]any{"uid": int64(7), "status": "succeeded", "type": "documentAdditionOrUpdate",
			"enqueuedAt": "t0", "startedAt": "t0", "finishedAt": "t0"},
			&indexTask{EnqueuedAt: "t0", FinishedAt: "t0", StartedAt: "t0", Status: "succeeded",
				Type: "documentAdditionOrUpdate", UID: 7}},
		{"documents", map[string]any{"results": []json.RawMessage{json.RawMessage(`{"id":"1"}`)},
			"offset": 0, "limit": 20, "total": 1},
			&indexDocuments{Limit: 20, Offset: 0, Total: 1,
				Results: []json.RawMessage{json.RawMessage(`{"id":"1"}`)}}},
		{"hits", map[string]any{"hits": []json.RawMessage{json.RawMessage(`{"id":"1"}`)}, "query": "q",
			"processingTimeMs": int64(4), "limit": 20, "offset": 0, "estimatedTotalHits": 1},
			&indexHits{EstimatedTotalHits: 1, Hits: []json.RawMessage{json.RawMessage(`{"id":"1"}`)},
				Limit: 20, Offset: 0, ProcessingTimeMs: 4, Query: "q"}},
		{"fault", map[string]any{"message": "Index `m` not found.", "code": "index_not_found",
			"type": "invalid_request",
			"link": "https://www.meilisearch.com/docs/reference/errors/error_codes#index_not_found"},
			absent("m").body},
	} {
		t.Run(tc.name, func(t *testing.T) {
			was, err := json.Marshal(tc.was)
			if err != nil {
				t.Fatalf("marshal map: %v", err)
			}
			now, err := json.Marshal(tc.typed)
			if err != nil {
				t.Fatalf("marshal typed: %v", err)
			}
			if string(was) != string(now) {
				t.Errorf("the wire MOVED.\n was: %s\n now: %s\nDeclare the model's fields in "+
					"alphabetical json-tag order — encoding/json sorts a map's keys, so that is the "+
					"order the untyped handler emitted.", was, now)
			}
		})
	}
}

// TestTheDialectRefusalsSurviveTyping drives the live router, because a body
// built in a unit test proves the value and not the PATH it reaches the wire by:
// a typed op's error goes through zip's error handler unless envelope() catches
// it first, and that middleware is installed by registration order.
func TestTheDialectRefusalsSurviveTyping(t *testing.T) {
	app := indexApp(t)
	link := "https://www.meilisearch.com/docs/reference/errors/error_codes#"
	for _, tc := range []struct {
		name, method, path, org, body string
		status                        int
		want                          faultBody
	}{
		{name: "anonymous read", method: http.MethodGet, path: "/v1/index/indexes", status: http.StatusForbidden,
			want: faultBody{Code: "invalid_api_key", Link: link + "invalid_api_key",
				Message: "The provided API key is invalid.", Type: "auth"}},
		{name: "anonymous task", method: http.MethodGet, path: "/v1/index/tasks/1", status: http.StatusForbidden,
			want: faultBody{Code: "invalid_api_key", Link: link + "invalid_api_key",
				Message: "The provided API key is invalid.", Type: "auth"}},
		{name: "missing index", method: http.MethodGet, path: "/v1/index/indexes/ghost", org: "acme",
			status: http.StatusNotFound,
			want: faultBody{Code: "index_not_found", Link: link + "index_not_found",
				Message: "Index `ghost` not found.", Type: "invalid_request"}},
		{name: "missing index on search", method: http.MethodPost, path: "/v1/index/indexes/ghost/search",
			org: "acme", body: `{"q":"x"}`, status: http.StatusNotFound,
			want: faultBody{Code: "index_not_found", Link: link + "index_not_found",
				Message: "Index `ghost` not found.", Type: "invalid_request"}},
		// An over-long uid is the reachable spelling of a bad one: fiber never
		// matches an EMPTY segment, so the uid a route carries is always some
		// string, and `%20` arrives percent-encoded and is therefore a legal
		// (if silly) name — exactly as it was before this was typed.
		{name: "oversize uid", method: http.MethodGet,
			path: "/v1/index/indexes/" + strings.Repeat("u", maxUID+1), org: "acme",
			status: http.StatusBadRequest,
			want: faultBody{Code: "invalid_index_uid", Link: link + "invalid_index_uid",
				Message: "An index uid is required.", Type: "invalid_request"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, body := call(t, app, tc.method, tc.path, tc.org, tc.body)
			if status != tc.status {
				t.Fatalf("status %d, want %d (body %s)", status, tc.status, body)
			}
			want, err := json.Marshal(tc.want)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if strings.TrimSpace(string(body)) != string(want) {
				t.Errorf("the refusal MOVED.\n was: %s\n now: %s\nA Meilisearch client branches on "+
					"`code`; zip's own error shape has none.", want, strings.TrimSpace(string(body)))
			}
		})
	}
}

// TestWritesStillAnswer202 pins the status every write declares. zip answers 200
// unless the op says otherwise, so a WithStatus dropped in a refactor silently
// downgrades five routes for every client that checks.
func TestWritesStillAnswer202(t *testing.T) {
	app := indexApp(t)
	for _, tc := range []struct{ name, method, path, body string }{
		{"create", http.MethodPost, "/v1/index/indexes", `{"uid":"m","primaryKey":"id"}`},
		{"settings", http.MethodPatch, "/v1/index/indexes/m/settings", `{"filterableAttributes":["user"]}`},
		{"add", http.MethodPost, "/v1/index/indexes/m/documents", `[{"id":"1","title":"a"}]`},
		{"update", http.MethodPut, "/v1/index/indexes/m/documents", `[{"id":"1","title":"b"}]`},
		{"delete one", http.MethodDelete, "/v1/index/indexes/m/documents/1", ""},
		{"delete batch", http.MethodPost, "/v1/index/indexes/m/documents/delete-batch", `["1"]`},
		{"drop", http.MethodDelete, "/v1/index/indexes/m", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, body := call(t, app, tc.method, tc.path, "acme", tc.body)
			if status != http.StatusAccepted {
				t.Fatalf("status %d, want 202 (body %s)", status, body)
			}
			var got indexEnqueued
			if err := json.Unmarshal(body, &got); err != nil {
				t.Fatalf("decode receipt: %v (body %s)", err, body)
			}
			if got.Status != "enqueued" || got.TaskUID == 0 {
				t.Errorf("receipt %s is not the dialect's EnqueuedTask", body)
			}
		})
	}
}

// TestHealthFailsClosedWithBothStatuses proves the one op that answers two
// statuses over ONE shape still answers both, and that the 503 rides the same
// body — the whole reason it is a declared status rather than a returned error.
func TestHealthFailsClosedWithBothStatuses(t *testing.T) {
	app := indexApp(t)
	status, body := call(t, app, http.MethodGet, "/v1/index/health", "", "")
	if status != http.StatusOK || strings.TrimSpace(string(body)) != `{"status":"available"}` {
		t.Fatalf("open store: %d %s", status, body)
	}
	if err := mounted.State.store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	status, body = call(t, app, http.MethodGet, "/v1/index/health", "", "")
	if status != http.StatusServiceUnavailable || strings.TrimSpace(string(body)) != `{"status":"unavailable"}` {
		t.Fatalf("closed store: %d %s — an unreadable store must be taken out of rotation, not "+
			"answer an empty result set", status, body)
	}
}

// TestPagingKeepsItsDefaults is why limit and offset are STRINGS on indexPage.
// zip's setScalar leaves an unparseable value at the field's ZERO and cannot
// tell that zero from an absent one, so an int field would answer `?limit=abc`
// and a missing `?limit=` with 0 rows where this surface has always answered 20
// — while still having to honour an explicit `?limit=0`.
func TestPagingKeepsItsDefaults(t *testing.T) {
	app := indexApp(t)
	if s, b := call(t, app, http.MethodPost, "/v1/index/indexes", "acme", `{"uid":"m","primaryKey":"id"}`); s != http.StatusAccepted {
		t.Fatalf("create: %d %s", s, b)
	}
	if s, b := call(t, app, http.MethodPost, "/v1/index/indexes/m/documents", "acme", `[{"id":"1"},{"id":"2"}]`); s != http.StatusAccepted {
		t.Fatalf("seed: %d %s", s, b)
	}
	for _, tc := range []struct {
		query string
		limit int
	}{
		{"", defaultLimit},
		{"?limit=abc", defaultLimit},
		{"?limit=0", 0},
		{"?limit=1", 1},
	} {
		t.Run("limit"+tc.query, func(t *testing.T) {
			status, body := call(t, app, http.MethodGet, "/v1/index/indexes/m/documents"+tc.query, "acme", "")
			if status != http.StatusOK {
				t.Fatalf("status %d: %s", status, body)
			}
			var page indexDocuments
			if err := json.Unmarshal(body, &page); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if page.Limit != tc.limit {
				t.Errorf("limit %d, want %d — an absent or unparseable value defaults, an explicit 0 "+
					"does not", page.Limit, tc.limit)
			}
		})
	}
}

// TestTheQueryStringCannotRedirectAWrite is the wire-widening class every
// POST/PUT/PATCH conversion in the fleet has to answer for. zip's binder fills an
// In field from the QUERY as well as the body (body → query → path, increasing
// authority), so a converted write silently starts accepting `?uid=` — which on
// createIndex would create an index the body never named. The untyped handler
// read c.Bind, which is the body and nothing else; `url:"-"` restores that.
func TestTheQueryStringCannotRedirectAWrite(t *testing.T) {
	app := indexApp(t)
	if s, b := call(t, app, http.MethodPost, "/v1/index/indexes?uid=hijacked", "acme",
		`{"uid":"honest","primaryKey":"id"}`); s != http.StatusAccepted {
		t.Fatalf("create: %d %s", s, b)
	}
	status, body := call(t, app, http.MethodGet, "/v1/index/indexes", "acme", "")
	if status != http.StatusOK {
		t.Fatalf("list: %d %s", status, body)
	}
	var list indexList
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(list.Results) != 1 || list.Results[0].UID != "honest" {
		t.Errorf("the query string reached the body: %s", body)
	}
}

// TestSearchKeepsItsBodyOnly is the same class on the read that carries a body.
// `q`, `filter`, `limit` and `offset` have always been body fields, and a query
// string that could override `q` would return a different result set for the
// same call.
func TestSearchKeepsItsBodyOnly(t *testing.T) {
	app := indexApp(t)
	if s, b := call(t, app, http.MethodPost, "/v1/index/indexes", "acme", `{"uid":"m","primaryKey":"id"}`); s != http.StatusAccepted {
		t.Fatalf("create: %d %s", s, b)
	}
	if s, b := call(t, app, http.MethodPost, "/v1/index/indexes/m/documents", "acme",
		`[{"id":"1","title":"roadmap"},{"id":"2","title":"budget"}]`); s != http.StatusAccepted {
		t.Fatalf("seed: %d %s", s, b)
	}
	status, body := call(t, app, http.MethodPost, "/v1/index/indexes/m/search?q=budget&limit=1", "acme",
		`{"q":"roadmap"}`)
	if status != http.StatusOK {
		t.Fatalf("search: %d %s", status, body)
	}
	var hits indexHits
	if err := json.Unmarshal(body, &hits); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if hits.Query != "roadmap" || hits.Limit != defaultLimit {
		t.Errorf("the query string reached the body: query=%q limit=%d", hits.Query, hits.Limit)
	}
}

// TestTenantIsNeverAnInputField is the security property the whole subsystem
// rests on, asserted against the TYPES rather than against a request: an org
// that could be named on an In is a cross-tenant read the caller asserted for
// itself. Every model here is checked, so a field added tomorrow is checked too.
func TestTenantIsNeverAnInputField(t *testing.T) {
	reg, err := openapi.Typed(indexApp(t))
	if err != nil {
		t.Fatalf("typed registry: %v", err)
	}
	for name, raw := range reg.Schemas {
		def, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		props, _ := def["properties"].(map[string]any)
		for field := range props {
			switch strings.ToLower(field) {
			case "org", "owner", "tenant", "orgid", "user":
				t.Errorf("%s publishes %q — the tenant is principal.OrgFrom and never an In field",
					name, field)
			}
		}
	}
}
