package ask

// web_test.go — the DOOR's dispatch proof. The answer engine's own behaviour (the
// loop, ranking, the read stage, the SearchEvent wire) is proven in
// clients/answer; what /v1/ask owes is the routing decision: a `mode` goes to the
// engine, and NO mode still goes to the figure advisor, unchanged.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/types"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// webAI answers the engine's synthesis prompt with a fixed string; the plan and
// follow-up prompts get well-formed empty lists so the loop stays bounded.
type webAI struct{ answer string }

func (f *webAI) ChatCompletion(_ context.Context, req *types.ChatRequest) (*types.ChatResponse, error) {
	if strings.Contains(req.Prompt, `"queries"`) || strings.Contains(req.Prompt, `"questions"`) {
		return &types.ChatResponse{Content: `{"queries":[],"questions":[]}`}, nil
	}
	return &types.ChatResponse{Content: f.answer, TotalTokens: 50}, nil
}
func (f *webAI) Embed(context.Context, *types.EmbedRequest) ([][]float32, error) { return nil, nil }

// noNetworkSearch points bing at a local server returning empty HTML, so the
// engine's search resolves to zero sources instantly and the test never leaves
// the machine.
func noNetworkSearch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("<html><body></body></html>"))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("WEBSEARCH_BING_URL", srv.URL)
	t.Setenv("WEBSEARCH_ENGINES", "bing")
}

// TestAskWebModeDispatch proves end-to-end that a mode routes to the answer engine
// (domain:"web", real answer) — and, crucially, that the advisor's figure path is
// UNTOUCHED (a no-mode financial question still routes to books).
func TestAskWebModeDispatch(t *testing.T) {
	noNetworkSearch(t)
	app := zip.New(zip.Config{Logger: luxlog.New("test"), DisableStartupMessage: true})
	fakeBooks(app, map[string]string{"acme": "$4,200"})
	if err := Mount(app, cloud.Deps{Logger: luxlog.New("test"), DataDir: t.TempDir(), AI: &webAI{answer: "Clojure was created by Rich Hickey."}}); err != nil {
		t.Fatalf("Mount: %v", err)
	}

	// web mode → the answer engine
	body, _ := json.Marshal(AskRequest{Q: "who created clojure and why", Mode: "search"})
	req := httptest.NewRequest(http.MethodPost, "/v1/ask", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Org-Id", "acme")
	req.Header.Set("X-User-Id", "u-acme")
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("web ask: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("web mode want 200, got %d", resp.StatusCode)
	}
	var out map[string]any
	b, _ := io.ReadAll(resp.Body)
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("decode: %v (%s)", err, b)
	}
	if out["domain"] != "web" {
		t.Fatalf("web mode must report domain=web, got %v", out["domain"])
	}
	if !strings.Contains(out["answer"].(string), "Rich Hickey") {
		t.Fatalf("web answer must be the synthesized text, got %v", out["answer"])
	}

	// no-mode financial question → advisor books path is untouched
	code, r := ask(t, app, "acme", "what is my MRR?")
	if code != http.StatusOK || r.Domain != "books" {
		t.Fatalf("advisor path regressed: code=%d domain=%q", code, r.Domain)
	}
}
