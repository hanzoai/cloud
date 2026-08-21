package crawl

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/openapi"
	"github.com/zap-proto/zip"
)

func mount(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{DisableStartupMessage: true})
	if err := Mount(app, cloud.Deps{}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	return app
}

func post(t *testing.T, app *zip.App, path, body string) (int, string) {
	t.Helper()
	rq := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	rq.Header.Set("Content-Type", "application/json")
	rq.Header.Set("X-API-Key", "test-service-key")
	resp, err := app.Test(rq)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, strings.TrimSpace(string(b))
}

// TestCrawlWireSurvivedTyping is the conversion's whole obligation.
//
// POST /v1/crawl was a raw handler for two measured reasons, and both were real:
// it is BODY-TOLERANT with a DOMAIN refusal body, and it bounds the request at
// 1 MiB. The cost of that was the whole point of todo #190 — a raw route is in
// no registry, so this subsystem projected NO MCP tool and the fleet's one way to
// read a web page was unreachable by the agent that needed it.
//
// It is a typed op now, and NEITHER fact moved. They moved HOUSE:
//
//   - The 400 with `{"success":false,"error":"missing url"}` is stated by the
//     ANSWER, through zip's StatusCoder — the sanctioned way for an op that
//     refuses with its own body — so the document publishes 400 with that schema.
//   - The 1 MiB bound and the malformed-body tolerance are facts about BYTES, and
//     a typed op receives its decoded In. So they are asked in middleware, where
//     the bytes still are (mount.go, bounded).
//
// This test is the measurement that says so: the same three inputs, the same
// three answers, byte for byte.
func TestCrawlWireSurvivedTyping(t *testing.T) {
	t.Setenv("WEBSEARCH_API_KEY", "test-service-key")
	app := mount(t)

	// (1) BODY-TOLERANT, with a DOMAIN refusal body. An unparseable body and an
	// empty url are the SAME answer, and it is this package's shape — not zip's
	// flat {status,code,error}, which is what an op that refused by RETURNING an
	// error would send.
	for _, body := range []string{`{"url":""}`, `not json at all`, `{`} {
		code, out := post(t, app, "/v1/crawl", body)
		if code != http.StatusBadRequest {
			t.Errorf("POST /v1/crawl %q = %d, want 400", body, code)
		}
		if out != `{"success":false,"error":"missing url"}` {
			t.Errorf("POST /v1/crawl %q body = %s, want the domain refusal", body, out)
		}
	}

	// (2) The 1 MiB bound. A body past it is the same 400 — it must never become a
	// crawl that runs because the cap was dropped in the conversion.
	big := `{"url":"https://example.com","pad":"` + strings.Repeat("x", 1<<20) + `"}`
	code, out := post(t, app, "/v1/crawl", big)
	if code != http.StatusBadRequest || out != `{"success":false,"error":"missing url"}` {
		t.Errorf("a body past the 1 MiB cap = %d %s, want the 400 domain refusal", code, out)
	}
}

// TestCrawlIsClosedToAnAnonymousCaller is the gate, asked at the door a typed op
// adds rather than at the one it already had.
//
// A tools/call reaches a typed op with NO route and therefore NO middleware, so a
// gate that lived only in middleware would be no gate at all for the MCP and CLI
// projections this conversion exists to create. The decision is in the handler
// (scopeOf) for exactly that reason. Here it is measured from the HTTP side: no
// principal and no key is refused, and the refusal is not a crawl.
func TestCrawlIsClosedToAnAnonymousCaller(t *testing.T) {
	t.Setenv("WEBSEARCH_API_KEY", "test-service-key")
	app := mount(t)

	rq := httptest.NewRequest(http.MethodPost, "/v1/crawl", strings.NewReader(`{"url":"https://example.com"}`))
	rq.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(rq)
	if err != nil {
		t.Fatalf("POST /v1/crawl: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("an anonymous crawl = %d %s, want 401 — this surface dials a caller-chosen "+
			"address from inside the cluster", resp.StatusCode, body)
	}
}

// TestCrawlIsServedAtOneAddressAndPublishedAtIt pins the registration. The mount
// used to declare `g.Post("", …)` and `g.Post("/", …)` on a Group("/v1/crawl"),
// which are the SAME route ("/v1/crawl/") — so the second was dead and the
// published path carried a trailing slash the callers do not use. Both URLs must
// keep working (the router is non-strict) AND the document must name the one
// without the slash.
func TestCrawlIsServedAtOneAddressAndPublishedAtIt(t *testing.T) {
	t.Setenv("WEBSEARCH_API_KEY", "test-service-key")
	app := mount(t)

	var paths []string
	for _, r := range app.Fiber().GetRoutes() {
		if r.Method == http.MethodPost && strings.Contains(r.Path, "crawl") {
			paths = append(paths, r.Path)
		}
	}
	if len(paths) != 1 || paths[0] != "/v1/crawl" {
		t.Fatalf("router has POST %v, want exactly [/v1/crawl]", paths)
	}

	// The wire did not move: both spellings still answer.
	for _, p := range []string{"/v1/crawl", "/v1/crawl/"} {
		if code, _ := post(t, app, p, `{"url":""}`); code != http.StatusBadRequest {
			t.Errorf("POST %s = %d, want 400 — the non-strict router serves both spellings", p, code)
		}
	}
}

// TestCrawlProjectsAsATool is the fact the conversion was FOR.
//
// A raw handler appends nothing to zip's op registry, and that registry is what
// every projection reads — so an untyped /v1/crawl is in no OpenAPI operation, no
// SDK method, no CLI command and no MCP tool. Asserting the handler answers
// correctly says nothing about any of that. This asks the subsystem's OWN MCP
// door, over JSON-RPC, exactly as the fleet's door asks it, and reads the answer.
func TestCrawlProjectsAsATool(t *testing.T) {
	t.Setenv("WEBSEARCH_API_KEY", "test-service-key")
	app := mount(t)

	rq := httptest.NewRequest(http.MethodPost, "/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	rq.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(rq)
	if err != nil {
		t.Fatalf("POST /mcp: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)

	var env struct {
		Result *struct {
			Tools []struct {
				Name        string `json:"name"`
				Description string `json:"description"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &env); err != nil || env.Result == nil {
		t.Fatalf("POST /mcp did not answer MCP: %d — %.200s", resp.StatusCode, raw)
	}
	for _, tool := range env.Result.Tools {
		if tool.Name != "read_page" {
			continue
		}
		if tool.Description == "" {
			t.Fatal("read_page projects with NO description — a tool a model cannot " +
				"read is a tool it will not call; run `go generate -run zipdoc ./...`")
		}
		t.Logf("read_page projects: %.90s…", tool.Description)
		return
	}
	t.Fatalf("read_page is not in this subsystem's tools/list — %.300s", raw)
}

// TestCrawlPublishesItsBodies holds the document to account. It used to assert
// that the UNTYPED route declared its shapes by hand through openapi.Register;
// the typed op declares them by construction, off the same structs the handler
// binds, so this now asserts the stronger fact — that the published operation
// carries a request body, a response AND the prose only a typed op can have.
func TestCrawlPublishesItsBodies(t *testing.T) {
	t.Setenv("WEBSEARCH_API_KEY", "test-service-key")
	doc, err := openapi.Spec(mount(t), openapi.Info{Title: "crawl", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	item, ok := doc.Paths["/v1/crawl"]
	if !ok {
		t.Fatalf("the document does not carry /v1/crawl at all; it has %v", keysOf(doc.Paths))
	}
	op := item["post"]
	if op == nil || op.RequestBody == nil {
		t.Fatal("POST /v1/crawl publishes no request body — an SDK would offer a crawl with no URL")
	}
	if op.Responses == nil {
		t.Error("POST /v1/crawl publishes no response shape")
	}
	if strings.TrimSpace(op.Summary) == "" {
		t.Error("POST /v1/crawl publishes no summary")
	}
	if strings.TrimSpace(op.Description) == "" {
		t.Error("POST /v1/crawl publishes no description — zipdoc lifts it from the handler's " +
			"doc comment; run `go generate -run zipdoc ./...` and commit zipdoc_gen.go")
	}
}

func keysOf[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestEveryPublishedFieldIsDescribed closes the half of the surface the two gates
// above cannot see. They prove the ADDRESS and the SHAPES reach the document;
// neither says anything about whether the shapes' FIELDS mean anything to a
// reader, and those come from a different place — a doc comment on each field,
// which zipdoc lifts one at a time.
//
// It matters here because `url` is a security boundary and not a parameter. The
// value is dialled from INSIDE the cluster, so which URLs are accepted is the
// whole contract of this route, and a caller who cannot see that in the document
// finds it out from a refusal instead.
//
// Presence is all a gate can check. A description restating the field's name is
// worse than none, and only a reader catches that.
func TestEveryPublishedFieldIsDescribed(t *testing.T) {
	t.Setenv("WEBSEARCH_API_KEY", "test-service-key")
	doc, err := openapi.Spec(mount(t), openapi.Info{Title: "crawl", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	if doc.Components == nil || len(doc.Components.Schemas) == 0 {
		t.Fatal("crawl publishes no schemas at all — the gate would pass vacuously")
	}
	bare, err := openapi.Bare(doc)
	if err != nil {
		t.Fatalf("bare: %v", err)
	}
	if len(bare) > 0 {
		t.Errorf("%d published schema propert(ies) with no description: %s\n"+
			"Write the field's OWN doc comment — a header above a group of fields is lifted onto "+
			"the first of them alone — then run: make -C apps/crawl describe",
			len(bare), strings.Join(bare, ", "))
	}
}
