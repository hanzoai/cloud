package crawl

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/openapi"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

func mount(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{DisableStartupMessage: true})
	if err := Mount(app, cloud.Deps{Logger: luxlog.New("test")}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	return app
}

func post(t *testing.T, app *zip.App, path, body string) (int, string) {
	t.Helper()
	rq := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	rq.Header.Set("Content-Type", "application/json")
	rq.Header.Set("X-API-Key", "test-service-key")
	resp, err := app.Fiber().Test(rq)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, strings.TrimSpace(string(b))
}

// TestCrawlRefusalIsTheWire measures the two facts that keep POST /v1/crawl an
// untyped handler. They are named at the registration in mount.go; this is what
// makes that prose a MEASUREMENT rather than a claim, so the day zip can express
// either one, the conversion is a test edit away instead of a re-derivation.
func TestCrawlRefusalIsTheWire(t *testing.T) {
	t.Setenv("WEBSEARCH_API_KEY", "test-service-key")
	app := mount(t)

	// (1) BODY-TOLERANT, with a DOMAIN refusal body. An unparseable body and an
	// empty url are the SAME answer, and it is this package's shape — not zip's
	// flat {status,code,error}, which is the only thing a typed op's returned
	// error can become.
	for _, body := range []string{`{"url":""}`, `not json at all`, `{`} {
		code, out := post(t, app, "/v1/crawl", body)
		if code != http.StatusBadRequest {
			t.Errorf("POST /v1/crawl %q = %d, want 400", body, code)
		}
		if out != `{"success":false,"error":"missing url"}` {
			t.Errorf("POST /v1/crawl %q body = %s, want the domain refusal", body, out)
		}
	}

	// (2) The 1 MiB io.LimitReader bound. A body past it cannot decode, so it is
	// the same 400 — and a typed op, which receives its DECODED In, could never
	// see the length that produced it.
	big := `{"url":"https://example.com","pad":"` + strings.Repeat("x", 1<<20) + `"}`
	code, out := post(t, app, "/v1/crawl", big)
	if code != http.StatusBadRequest || out != `{"success":false,"error":"missing url"}` {
		t.Errorf("a body past the 1 MiB cap = %d %s, want the 400 domain refusal", code, out)
	}
}

// TestCrawlIsServedAtOneAddressAndPublishedAtIt pins the registration fix. The
// mount used to declare `g.Post("", …)` and `g.Post("/", …)` on a
// Group("/v1/crawl"), which are the SAME route ("/v1/crawl/") — so the second was
// dead and the published path carried a trailing slash the callers do not use.
// Both URLs must keep working (the router is non-strict) AND the document must
// name the one without the slash.
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

// TestCrawlDeclaresItsBodies holds the other half of the refusal to account.
// Staying untyped costs the prose, the MCP tool and the CLI command; it must not
// also cost a document that says this route takes no body, or every generated SDK
// offers a crawl with nowhere to put the URL.
func TestCrawlDeclaresItsBodies(t *testing.T) {
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
	// RequestBody/Responses are `any` on Operation: Register builds the closed
	// shapes, the typed fold reuses zip's open maps. Assert the closed ones.
	rb, ok := op.RequestBody.(*openapi.RequestBody)
	if !ok {
		t.Fatalf("request body is %T, want the declared *openapi.RequestBody", op.RequestBody)
	}
	if _, jsonBody := rb.Content["application/json"]; !jsonBody {
		t.Errorf("request body content = %v, want application/json", rb.Content)
	}
	resp, ok := op.Responses.(map[string]*openapi.Response)
	if !ok || resp["2XX"] == nil {
		t.Errorf("POST /v1/crawl publishes no success body (responses = %#v)", op.Responses)
	}
}

func keysOf[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
