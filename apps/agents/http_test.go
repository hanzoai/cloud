package agents

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/types"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// deadline is how long an app.Test call waits, and it is generous deliberately.
//
// fiber's default is ONE SECOND, which turns every request in this package into
// an assertion about latency that none of these tests meant to make. Under a
// whole-repo `go test ./...` this package shares a machine with every other one
// and takes about six times as long as it does alone; a create that ANSWERED 201
// in 1.36s failed as "i/o timeout" — the server was correct and the clock was
// the only thing that had gone wrong.
//
// Thirty seconds still catches a handler that never returns, which is the one
// thing a deadline here is for. A test that means to bound latency should say so
// where it means it, not inherit it from the transport's default.
var deadline = zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true}

// compose installs what the program's composer installs — cloud.Bridge, once at
// the app root. A subsystem never installs its own, so a test app owes the same
// root install; without it every org-scoped op answers a 403 no production
// program would produce.
func compose(app *zip.App) { app.Use(cloud.Bridge()) }

// mountApp mounts the agents surface with a deterministic fake AI so run() is
// exercised end-to-end over HTTP without a real gateway. Pass a nil interface
// to exercise the no-inference fail-closed path.
func mountApp(t *testing.T, ai types.AIClient) *zip.App {
	t.Helper()
	return mountAppModel(t, ai)
}

// mountAppModel mounts with a catalog-aware AI client. It took a deployment
// default model until that knob was deleted: the default is cloud.DefaultModel,
// full stop, so there is no per-deployment value left for a test to vary.
func mountAppModel(t *testing.T, ai types.AIClient) *zip.App {
	t.Helper()
	return mountAppIn(t, t.TempDir(), ai)
}

// mountAppDir mounts over an EXISTING data dir, so a test can stand up what a
// deployment already has on disk (a pre-split agents.db) and boot over it.
func mountAppDir(t *testing.T, dir string) *zip.App {
	t.Helper()
	return mountAppIn(t, dir, &fakeAI{content: "x"})
}

func mountAppIn(t *testing.T, dir string, ai types.AIClient) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	compose(app)
	t.Setenv("CLOUD_DATA_DIR", dir)
	if err := Use(app, cloud.Deps{AI: ai}); err != nil {
		t.Fatalf("Use:  %v", err)
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
	resp, err := app.Test(req, deadline)
	if err != nil {
		t.Fatalf("Test %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func TestHTTPGateIsolationAndRun(t *testing.T) {
	app := mountApp(t, &fakeAI{content: "the answer"})

	if code, _ := do(t, app, http.MethodGet, "/v1/agent", "", nil); code != http.StatusForbidden {
		t.Fatalf("no-org list want 403, got %d", code)
	}

	// maxpower creates an agent (model required).
	if code, _ := do(t, app, http.MethodPost, "/v1/agent", "maxpower",
		map[string]any{"name": "helper", "model": "gpt-4o-mini", "instructions": "be terse"}); code != http.StatusCreated {
		t.Fatalf("create want 201, got %d", code)
	}
	// Creating without a model is a 201 on cloud.DefaultModel. This asserted 400
	// ("model is required") while the default came from deps.AIDefaultModel, which
	// a hand-built test Deps left empty — but LoadConfig never did, so the 400 was
	// reachable only from a fixture and NEVER from a deployment. The test pinned a
	// state production could not be in; with the field gone there is one behaviour.
	if code, _ := do(t, app, http.MethodPost, "/v1/agent", "maxpower",
		map[string]any{"name": "nomodel"}); code != http.StatusCreated {
		t.Fatalf("create without model want 201 on the default model, got %d", code)
	}

	// List shape is {agents:[...]}. maxpower owns BOTH creates above — the
	// explicit-model one and the defaulted one — and sees neither org's rows but
	// its own.
	code, body := do(t, app, http.MethodGet, "/v1/agent", "maxpower", nil)
	var listed struct {
		Agents []agentView `json:"agents"`
	}
	_ = json.Unmarshal(body, &listed)
	names := map[string]string{}
	for _, a := range listed.Agents {
		names[a.Name] = a.Model
	}
	if code != http.StatusOK || len(listed.Agents) != 2 || names["helper"] != "gpt-4o-mini" {
		t.Fatalf("maxpower should see [helper nomodel], got %d %+v", code, listed.Agents)
	}
	if names["nomodel"] != cloud.DefaultModel {
		t.Fatalf("defaulted agent stored model %q, want %q", names["nomodel"], cloud.DefaultModel)
	}

	// run executes via the (fake) AI and returns a real recorded run.
	code, body = do(t, app, http.MethodPost, "/v1/agent/helper/run", "maxpower", map[string]any{"input": "hi"})
	if code != http.StatusOK {
		t.Fatalf("run want 200, got %d (%s)", code, body)
	}
	var rv agentRunView
	_ = json.Unmarshal(body, &rv)
	if rv.Status != "ok" || rv.Output != "the answer" {
		t.Fatalf("run should return the model output, got %+v", rv)
	}

	// The run was recorded and is org-scoped.
	code, body = do(t, app, http.MethodGet, "/v1/agent/helper/runs", "maxpower", nil)
	if code != http.StatusOK || !bytes.Contains(body, []byte("the answer")) {
		t.Fatalf("runs history want the recorded run, got %d %s", code, body)
	}

	// acme cannot see, run, or read runs for maxpower's agent.
	code, body = do(t, app, http.MethodGet, "/v1/agent", "acme", nil)
	_ = json.Unmarshal(body, &listed)
	if code != http.StatusOK || len(listed.Agents) != 0 {
		t.Fatalf("acme must see zero agents, got %d %+v", code, listed.Agents)
	}
	if code, _ := do(t, app, http.MethodPost, "/v1/agent/helper/run", "acme", map[string]any{"input": "hi"}); code != http.StatusNotFound {
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
	code, body := do(t, app, http.MethodPost, "/v1/agent", "maxpower",
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
	code, body = do(t, app, http.MethodGet, "/v1/agent/"+id, "maxpower", nil)
	if code != http.StatusOK {
		t.Fatalf("GET by returned id want 200, got %d (%s)", code, body)
	}
	var got agentDetail
	_ = json.Unmarshal(body, &got)
	if got.ID != id || got.Name != "verify-run" {
		t.Fatalf("GET by id resolved the wrong agent, got %+v", got.agentView)
	}

	// GET by NAME must resolve the SAME agent (both identifiers work).
	code, body = do(t, app, http.MethodGet, "/v1/agent/verify-run", "maxpower", nil)
	if code != http.StatusOK {
		t.Fatalf("GET by name want 200, got %d (%s)", code, body)
	}
	var byName agentDetail
	_ = json.Unmarshal(body, &byName)
	if byName.ID != id {
		t.Fatalf("GET by name must be the SAME agent as by id: %q vs %q", byName.ID, id)
	}

	// RUN by the RETURNED ID must execute and return real output (was 404 pre-fix).
	code, body = do(t, app, http.MethodPost, "/v1/agent/"+id+"/run", "maxpower", map[string]any{"input": "hi"})
	if code != http.StatusOK {
		t.Fatalf("run by returned id want 200, got %d (%s)", code, body)
	}
	var rv agentRunView
	_ = json.Unmarshal(body, &rv)
	if rv.Status != "ok" || rv.Output != "the answer" {
		t.Fatalf("run by id must return the model output, got %+v", rv)
	}

	// The run recorded under the agent is visible via runs-by-id AND runs-by-name.
	code, body = do(t, app, http.MethodGet, "/v1/agent/"+id+"/runs", "maxpower", nil)
	if code != http.StatusOK || !bytes.Contains(body, []byte("the answer")) {
		t.Fatalf("runs by id want the recorded run, got %d %s", code, body)
	}
	code, body = do(t, app, http.MethodGet, "/v1/agent/verify-run/runs", "maxpower", nil)
	if code != http.StatusOK || !bytes.Contains(body, []byte("the answer")) {
		t.Fatalf("runs by name want the same recorded run, got %d %s", code, body)
	}

	// Cross-org fail-closed: acme cannot GET or run maxpower's agent BY ITS ID.
	if c2, _ := do(t, app, http.MethodGet, "/v1/agent/"+id, "acme", nil); c2 != http.StatusNotFound {
		t.Fatalf("acme GET maxpower agent by id want 404, got %d", c2)
	}
	if c2, _ := do(t, app, http.MethodPost, "/v1/agent/"+id+"/run", "acme", map[string]any{"input": "x"}); c2 != http.StatusNotFound {
		t.Fatalf("acme run maxpower agent by id want 404, got %d", c2)
	}
}

// TestHTTPMetricsAndActivityNotShadowed proves /v1/agent/metrics and
// /v1/agent/activity resolve to their own handlers (not captured by the :name
// wildcard) and that every number is derived from REAL recorded runs.
func TestHTTPMetricsAndActivityNotShadowed(t *testing.T) {
	app := mountApp(t, &fakeAI{content: "ok"})

	// Both org-wide surfaces require a tenant, like every other route.
	if code, _ := do(t, app, http.MethodGet, "/v1/agent/metrics", "", nil); code != http.StatusForbidden {
		t.Fatalf("no-org metrics want 403, got %d", code)
	}
	if code, _ := do(t, app, http.MethodGet, "/v1/agent/activity", "", nil); code != http.StatusForbidden {
		t.Fatalf("no-org activity want 403, got %d", code)
	}

	// Empty org: honest empty shapes, NOT a 404 (proves no wildcard shadowing)
	// and NOT a fabricated trend.
	code, body := do(t, app, http.MethodGet, "/v1/agent/metrics?range=7D", "maxpower", nil)
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

	code, body = do(t, app, http.MethodGet, "/v1/agent/activity", "maxpower", nil)
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
	if code, _ := do(t, app, http.MethodPost, "/v1/agent", "maxpower",
		map[string]any{"name": "helper", "model": "gpt-4o-mini"}); code != http.StatusCreated {
		t.Fatalf("create want 201, got %d", code)
	}
	if code, _ := do(t, app, http.MethodPost, "/v1/agent/helper/run", "maxpower", map[string]any{"input": "hi"}); code != http.StatusOK {
		t.Fatalf("run want 200, got %d", code)
	}

	// Metrics now carry a real invocation series for "helper" summing to 1.
	_, body = do(t, app, http.MethodGet, "/v1/agent/metrics?range=24H", "maxpower", nil)
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
	_, body = do(t, app, http.MethodGet, "/v1/agent/activity", "maxpower", nil)
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
	_, body = do(t, app, http.MethodGet, "/v1/agent/metrics?range=24H", "acme", nil)
	_ = json.Unmarshal(body, &m)
	if len(m.Series) != 0 {
		t.Fatalf("acme must see zero series, got %+v", m.Series)
	}
	_, body = do(t, app, http.MethodGet, "/v1/agent/activity", "acme", nil)
	_ = json.Unmarshal(body, &feed)
	if len(feed.Activity) != 0 {
		t.Fatalf("acme must see zero activity, got %+v", feed.Activity)
	}
}

// TestHTTPRunWithoutAIFailsClosed: when no AI client is wired, run 503s and
// never fabricates output.
func TestHTTPRunWithoutAIFailsClosed(t *testing.T) {
	app := mountApp(t, nil)
	do(t, app, http.MethodPost, "/v1/agent", "maxpower",
		map[string]any{"name": "a", "model": "m", "instructions": "x"})
	if code, _ := do(t, app, http.MethodPost, "/v1/agent/a/run", "maxpower", map[string]any{"input": "hi"}); code != http.StatusServiceUnavailable {
		t.Fatalf("run without AI want 503, got %d", code)
	}
}
