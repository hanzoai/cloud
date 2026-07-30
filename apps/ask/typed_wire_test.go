package ask

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/openapi"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// askApp mounts the door with a stubbed books contributor and a stubbed model, on
// a bare app — the same harness the behaviour suite uses.
func askApp(t *testing.T) *zip.App {
	t.Helper()
	noNetworkSearch(t)
	app := zip.New(zip.Config{Logger: luxlog.New("test"), DisableStartupMessage: true})
	fakeBooks(app, map[string]string{"acme": "$4,200"})
	if err := Mount(app, cloud.Deps{
		Logger: luxlog.New("test"), DataDir: t.TempDir(),
		AI: &webAI{answer: "Clojure was created by Rich Hickey."},
	}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	return app
}

func askRaw(t *testing.T, app *zip.App, body string, hdr map[string]string) *http.Response {
	t.Helper()
	rq := httptest.NewRequest(http.MethodPost, "/v1/ask", strings.NewReader(body))
	rq.Header.Set("Content-Type", "application/json")
	rq.Header.Set("X-Org-Id", "acme")
	rq.Header.Set("X-User-Id", "u-acme")
	for k, v := range hdr {
		rq.Header.Set(k, v)
	}
	resp, err := app.Fiber().Test(rq)
	if err != nil {
		t.Fatalf("POST /v1/ask: %v", err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

// TestAskRefusalIsTheWire measures the facts that keep POST /v1/ask an untyped
// handler. They are named at the registration in ask.go; this is what makes that
// prose a MEASUREMENT rather than a claim, so the day zip can express them the
// conversion is a test edit away instead of a re-derivation.
func TestAskRefusalIsTheWire(t *testing.T) {
	app := askApp(t)

	// (1) ONE ROUTE, TWO SUCCESS SHAPES. A typed op declares exactly one Out.
	advisor := askRaw(t, app, `{"question":"what is my MRR?"}`, nil)
	web := askRaw(t, app, `{"q":"who created clojure","mode":"search"}`, nil)
	advKeys, webKeys := keysOfBody(t, advisor), keysOfBody(t, web)
	if strings.Join(advKeys, ",") == strings.Join(webKeys, ",") {
		t.Fatalf("both branches answer %v — if the two success shapes have converged, "+
			"this route is typable and the refusal in ask.go is stale", advKeys)
	}
	wantAdvisor := []string{"answer", "domain", "figures", "followups", "sources"}
	wantWeb := []string{"answer", "domain", "figures", "follow_ups", "followups", "mode", "model", "sources"}
	sort.Strings(wantAdvisor)
	sort.Strings(wantWeb)
	if strings.Join(advKeys, ",") != strings.Join(wantAdvisor, ",") {
		t.Errorf("advisor answer keys = %v, want %v", advKeys, wantAdvisor)
	}
	if strings.Join(webKeys, ",") != strings.Join(wantWeb, ",") {
		t.Errorf("web answer keys = %v, want %v", webKeys, wantWeb)
	}

	// (2) THE WEB BRANCH STREAMS. An SSE answer is not a JSON value, and zip's
	// typed path has no Out that means "I already streamed".
	sse := askRaw(t, app, `{"q":"who created clojure","mode":"search"}`,
		map[string]string{"Accept": "text/event-stream"})
	if ct := sse.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("Accept: text/event-stream → Content-Type %q, want text/event-stream", ct)
	}
}

// TestAskDeclaresItsRequestAndNotItsResponse holds the other half of the refusal
// to account. Staying untyped costs the prose, the MCP tool and the CLI command;
// it must not also cost a document that says this route takes no body. The
// RESPONSE stays undeclared on purpose — the test above is why: one declared shape
// would be a false statement about the other branch.
func TestAskDeclaresItsRequestAndNotItsResponse(t *testing.T) {
	doc, err := openapi.Spec(askApp(t), openapi.Info{Title: "ask", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	op := doc.Paths["/v1/ask"]["post"]
	if op == nil {
		t.Fatal("the document does not carry POST /v1/ask")
	}
	rb, ok := op.RequestBody.(*openapi.RequestBody)
	if !ok {
		t.Fatalf("request body is %T, want the declared *openapi.RequestBody — without it "+
			"every generated SDK offers an ask with nowhere to put the question", op.RequestBody)
	}
	if _, jsonBody := rb.Content["application/json"]; !jsonBody {
		t.Errorf("request body content = %v, want application/json", rb.Content)
	}
	if op.Responses != nil {
		t.Errorf("responses = %#v, want none: the success body is polymorphic and a single "+
			"declared shape would be false about the branch it does not describe", op.Responses)
	}
}

func keysOfBody(t *testing.T, resp *http.Response) []string {
	t.Helper()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d (%s)", resp.StatusCode, b)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("decode %s: %v", b, err)
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
