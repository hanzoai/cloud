package agents

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/types"
	"github.com/zap-proto/zip"
	luxlog "github.com/luxfi/log"
)

// mountApp mounts the agents surface with a deterministic fake AI so run() is
// exercised end-to-end over HTTP without a real gateway. Pass a nil interface
// to exercise the no-inference fail-closed path.
func mountApp(t *testing.T, ai types.AIClient) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	if err := Mount(app, cloud.Deps{Logger: luxlog.New("test"), DataDir: t.TempDir(), AI: ai}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	return app
}

func do(t *testing.T, app *zip.App, method, path, org string, body any) (int, []byte) {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, r)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if org != "" {
		req.Header.Set("X-Org-Id", org)
		req.Header.Set("X-User-Id", "u_"+org) // validated principal (tenant() gates on it)
	}
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func TestHTTPGateIsolationAndRun(t *testing.T) {
	app := mountApp(t, &fakeAI{content: "the answer"})

	if code, _ := do(t, app, http.MethodGet, "/v1/agents", "", nil); code != http.StatusForbidden {
		t.Fatalf("no-org list want 403, got %d", code)
	}

	// maxpower creates an agent (model required).
	if code, _ := do(t, app, http.MethodPost, "/v1/agents", "maxpower",
		map[string]any{"name": "helper", "model": "gpt-4o-mini", "instructions": "be terse"}); code != http.StatusCreated {
		t.Fatalf("create want 201, got %d", code)
	}
	// model is required — creating without one is a 400.
	if code, _ := do(t, app, http.MethodPost, "/v1/agents", "maxpower",
		map[string]any{"name": "nomodel"}); code != http.StatusBadRequest {
		t.Fatalf("create without model want 400, got %d", code)
	}

	// List shape is {agents:[...]}.
	code, body := do(t, app, http.MethodGet, "/v1/agents", "maxpower", nil)
	var listed struct {
		Agents []agentView `json:"agents"`
	}
	_ = json.Unmarshal(body, &listed)
	if code != http.StatusOK || len(listed.Agents) != 1 || listed.Agents[0].Name != "helper" {
		t.Fatalf("maxpower should see [helper], got %d %+v", code, listed.Agents)
	}

	// run executes via the (fake) AI and returns a real recorded run.
	code, body = do(t, app, http.MethodPost, "/v1/agents/helper/run", "maxpower", map[string]any{"input": "hi"})
	if code != http.StatusOK {
		t.Fatalf("run want 200, got %d (%s)", code, body)
	}
	var rv runView
	_ = json.Unmarshal(body, &rv)
	if rv.Status != "ok" || rv.Output != "the answer" {
		t.Fatalf("run should return the model output, got %+v", rv)
	}

	// The run was recorded and is org-scoped.
	code, body = do(t, app, http.MethodGet, "/v1/agents/helper/runs", "maxpower", nil)
	if code != http.StatusOK || !bytes.Contains(body, []byte("the answer")) {
		t.Fatalf("runs history want the recorded run, got %d %s", code, body)
	}

	// acme cannot see, run, or read runs for maxpower's agent.
	code, body = do(t, app, http.MethodGet, "/v1/agents", "acme", nil)
	_ = json.Unmarshal(body, &listed)
	if code != http.StatusOK || len(listed.Agents) != 0 {
		t.Fatalf("acme must see zero agents, got %d %+v", code, listed.Agents)
	}
	if code, _ := do(t, app, http.MethodPost, "/v1/agents/helper/run", "acme", map[string]any{"input": "hi"}); code != http.StatusNotFound {
		t.Fatalf("acme run on maxpower agent want 404, got %d", code)
	}
}

// TestHTTPRunWithoutAIFailsClosed: when no AI client is wired, run 503s and
// never fabricates output.
func TestHTTPRunWithoutAIFailsClosed(t *testing.T) {
	app := mountApp(t, nil)
	do(t, app, http.MethodPost, "/v1/agents", "maxpower",
		map[string]any{"name": "a", "model": "m", "instructions": "x"})
	if code, _ := do(t, app, http.MethodPost, "/v1/agents/a/run", "maxpower", map[string]any{"input": "hi"}); code != http.StatusServiceUnavailable {
		t.Fatalf("run without AI want 503, got %d", code)
	}
}
