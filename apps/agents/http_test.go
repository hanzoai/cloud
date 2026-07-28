package agents

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/types"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// mountApp mounts the agents surface with a deterministic fake AI so run() is
// exercised end-to-end over HTTP without a real gateway. Pass a nil interface
// to exercise the no-inference fail-closed path.
func mountApp(t *testing.T, ai types.AIClient) *zip.App {
	t.Helper()
	return mountAppModel(t, ai, "")
}

// mountAppModel is mountApp with an explicit deployment default model
// (deps.AIDefaultModel), so a test can exercise the empty-model → default path.
func mountAppModel(t *testing.T, ai types.AIClient, defaultModel string) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	if err := Mount(app, cloud.Deps{Logger: luxlog.New("test"), DataDir: t.TempDir(), AI: ai, AIDefaultModel: defaultModel}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	// Mount starts the scheduler goroutine when AI is non-nil and sets the global
	// `mounted` singleton; tear both down at test end so the loop goroutine can't
	// leak and clobber a later test's singleton (Red re-review LOW).
	t.Cleanup(func() { _ = Shutdown(context.Background()) })
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
		// A validated principal: the run path (money-moving) requires a non-empty
		// c.User() (X-User-Id). SanitizeIdentity sets this only from a verified
		// JWT; the test app has no sanitizer, so we inject it directly, exactly as
		// the gateway would. Empty org => no user (the anonymous 403 path).
		req.Header.Set("X-User-Id", "u-"+org)
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

// TestHTTPCreateThenGetRunByReturnedID reproduces Dave's exact flow and proves
// the id/name disconnect is fixed: create returns an id, and GETting AND running
// that agent BY THE RETURNED ID (not just the name) both resolve the SAME agent.
// Before the fix, get/run keyed the path only against the name column, so the id
// create handed back 404'd — a created agent was not runnable.
func TestHTTPCreateThenGetRunByReturnedID(t *testing.T) {
	app := mountApp(t, &fakeAI{content: "the answer"})

	// Create — capture the id the API returns (exactly what a client keeps).
	code, body := do(t, app, http.MethodPost, "/v1/agents", "maxpower",
		map[string]any{"name": "verify-run", "model": "gpt-4o-mini", "instructions": "be terse"})
	if code != http.StatusCreated {
		t.Fatalf("create want 201, got %d (%s)", code, body)
	}
	var created agentView
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatalf("create shape: %v (%s)", err, body)
	}
	if created.ID == "" || created.Name != "verify-run" {
		t.Fatalf("create must return id+name, got %+v", created)
	}
	id := created.ID

	// GET by the RETURNED ID must be 200 and the same agent (was 404 pre-fix).
	code, body = do(t, app, http.MethodGet, "/v1/agents/"+id, "maxpower", nil)
	if code != http.StatusOK {
		t.Fatalf("GET by returned id want 200, got %d (%s)", code, body)
	}
	var got agentDetail
	_ = json.Unmarshal(body, &got)
	if got.ID != id || got.Name != "verify-run" {
		t.Fatalf("GET by id resolved the wrong agent, got %+v", got.agentView)
	}

	// GET by NAME must resolve the SAME agent (both identifiers work).
	code, body = do(t, app, http.MethodGet, "/v1/agents/verify-run", "maxpower", nil)
	if code != http.StatusOK {
		t.Fatalf("GET by name want 200, got %d (%s)", code, body)
	}
	var byName agentDetail
	_ = json.Unmarshal(body, &byName)
	if byName.ID != id {
		t.Fatalf("GET by name must be the SAME agent as by id: %q vs %q", byName.ID, id)
	}

	// RUN by the RETURNED ID must execute and return real output (was 404 pre-fix).
	code, body = do(t, app, http.MethodPost, "/v1/agents/"+id+"/run", "maxpower", map[string]any{"input": "hi"})
	if code != http.StatusOK {
		t.Fatalf("run by returned id want 200, got %d (%s)", code, body)
	}
	var rv runView
	_ = json.Unmarshal(body, &rv)
	if rv.Status != "ok" || rv.Output != "the answer" {
		t.Fatalf("run by id must return the model output, got %+v", rv)
	}

	// The run recorded under the agent is visible via runs-by-id AND runs-by-name.
	code, body = do(t, app, http.MethodGet, "/v1/agents/"+id+"/runs", "maxpower", nil)
	if code != http.StatusOK || !bytes.Contains(body, []byte("the answer")) {
		t.Fatalf("runs by id want the recorded run, got %d %s", code, body)
	}
	code, body = do(t, app, http.MethodGet, "/v1/agents/verify-run/runs", "maxpower", nil)
	if code != http.StatusOK || !bytes.Contains(body, []byte("the answer")) {
		t.Fatalf("runs by name want the same recorded run, got %d %s", code, body)
	}

	// Cross-org fail-closed: acme cannot GET or run maxpower's agent BY ITS ID.
	if c2, _ := do(t, app, http.MethodGet, "/v1/agents/"+id, "acme", nil); c2 != http.StatusNotFound {
		t.Fatalf("acme GET maxpower agent by id want 404, got %d", c2)
	}
	if c2, _ := do(t, app, http.MethodPost, "/v1/agents/"+id+"/run", "acme", map[string]any{"input": "x"}); c2 != http.StatusNotFound {
		t.Fatalf("acme run maxpower agent by id want 404, got %d", c2)
	}
}

// TestHTTPMetricsAndActivityNotShadowed proves /v1/agents/metrics and
// /v1/agents/activity resolve to their own handlers (not captured by the :name
// wildcard) and that every number is derived from REAL recorded runs.
func TestHTTPMetricsAndActivityNotShadowed(t *testing.T) {
	app := mountApp(t, &fakeAI{content: "ok"})

	// Both org-wide surfaces require a tenant, like every other route.
	if code, _ := do(t, app, http.MethodGet, "/v1/agents/metrics", "", nil); code != http.StatusForbidden {
		t.Fatalf("no-org metrics want 403, got %d", code)
	}
	if code, _ := do(t, app, http.MethodGet, "/v1/agents/activity", "", nil); code != http.StatusForbidden {
		t.Fatalf("no-org activity want 403, got %d", code)
	}

	// Empty org: honest empty shapes, NOT a 404 (proves no wildcard shadowing)
	// and NOT a fabricated trend.
	code, body := do(t, app, http.MethodGet, "/v1/agents/metrics?range=7D", "maxpower", nil)
	if code != http.StatusOK {
		t.Fatalf("metrics want 200 (not shadowed 404), got %d (%s)", code, body)
	}
	var m struct {
		Range    string       `json:"range"`
		Series   []seriesLine `json:"series"`
		Resource struct {
			CPUVcpuHours   *float64 `json:"cpuVcpuHours"`
			MemGbHours     *float64 `json:"memGbHours"`
			StorageIoBytes *float64 `json:"storageIoBytes"`
			CostCents      *float64 `json:"costCents"`
		} `json:"resource"`
	}
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("metrics shape: %v (%s)", err, body)
	}
	if m.Range != "7D" || len(m.Series) != 0 {
		t.Fatalf("empty-org metrics want range=7D, no series, got %+v", m)
	}
	if m.Resource.CPUVcpuHours != nil || m.Resource.CostCents != nil {
		t.Fatalf("resource metering is unsourced — must be null, got %+v", m.Resource)
	}

	code, body = do(t, app, http.MethodGet, "/v1/agents/activity", "maxpower", nil)
	if code != http.StatusOK {
		t.Fatalf("activity want 200 (not shadowed 404), got %d (%s)", code, body)
	}
	var empty struct {
		Activity []activityView `json:"activity"`
	}
	_ = json.Unmarshal(body, &empty)
	if len(empty.Activity) != 0 {
		t.Fatalf("empty-org activity want [], got %+v", empty.Activity)
	}

	// Seed a real agent + a real run, then the surfaces must reflect exactly it.
	if code, _ := do(t, app, http.MethodPost, "/v1/agents", "maxpower",
		map[string]any{"name": "helper", "model": "gpt-4o-mini"}); code != http.StatusCreated {
		t.Fatalf("create want 201, got %d", code)
	}
	if code, _ := do(t, app, http.MethodPost, "/v1/agents/helper/run", "maxpower", map[string]any{"input": "hi"}); code != http.StatusOK {
		t.Fatalf("run want 200, got %d", code)
	}

	// Metrics now carry a real invocation series for "helper" summing to 1.
	_, body = do(t, app, http.MethodGet, "/v1/agents/metrics?range=24H", "maxpower", nil)
	_ = json.Unmarshal(body, &m)
	if len(m.Series) != 1 || m.Series[0].Key != "helper" {
		t.Fatalf("metrics want one series for helper, got %+v", m.Series)
	}
	total := 0
	for _, p := range m.Series[0].Points {
		total += p.V
	}
	if total != 1 {
		t.Fatalf("real invocation total want 1, got %d", total)
	}

	// Activity now carries the real invoked event + the created event, newest first.
	_, body = do(t, app, http.MethodGet, "/v1/agents/activity", "maxpower", nil)
	var feed struct {
		Activity []activityView `json:"activity"`
	}
	_ = json.Unmarshal(body, &feed)
	var invoked, created bool
	for _, e := range feed.Activity {
		if e.Agent != "helper" {
			t.Fatalf("activity must be scoped to helper, got %+v", e)
		}
		switch e.Kind {
		case "invoked":
			invoked = true
		case "created":
			created = true
		}
	}
	if !invoked || !created {
		t.Fatalf("activity want a real invoked + created event, got %+v", feed.Activity)
	}

	// Cross-org isolation: acme sees none of maxpower's metrics/activity.
	_, body = do(t, app, http.MethodGet, "/v1/agents/metrics?range=24H", "acme", nil)
	_ = json.Unmarshal(body, &m)
	if len(m.Series) != 0 {
		t.Fatalf("acme must see zero series, got %+v", m.Series)
	}
	_, body = do(t, app, http.MethodGet, "/v1/agents/activity", "acme", nil)
	_ = json.Unmarshal(body, &feed)
	if len(feed.Activity) != 0 {
		t.Fatalf("acme must see zero activity, got %+v", feed.Activity)
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
