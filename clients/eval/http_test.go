package eval

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/zip"
	luxlog "github.com/luxfi/log"
)

// mountApp builds the native eval surface with an IN-MEMORY telemetry store and a
// stub runner so the HTTP contract + isolation invariants are exercised end-to-end
// without a datastore or a live model gateway. It bypasses Mount's datastore wiring
// (no ClickHouse in unit tests) but uses the SAME handlers, store, and validation.
func mountApp(t *testing.T) (*zip.App, *service) {
	t.Helper()
	store, err := openStore(t.TempDir() + "/evals.db")
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	s := &service{store: store, tel: newMemTelemetry(), runner: stubRunner{}, log: luxlog.New("test")}

	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	app.Post("/v1/evals/datasets", s.createDataset)
	app.Get("/v1/evals/datasets", s.listDatasets)
	app.Get("/v1/evals/datasets/:name", s.getDataset)
	app.Delete("/v1/evals/datasets/:name", s.deleteDataset)
	app.Post("/v1/evals/dataset-items", s.createItem)
	app.Get("/v1/evals/dataset-items", s.listItems)
	app.Post("/v1/evals/evaluators", s.createEvaluator)
	app.Get("/v1/evals/evaluators", s.listEvaluators)
	app.Post("/v1/evals/score-configs", s.createScoreConfig)
	app.Get("/v1/evals/score-configs", s.listScoreConfigs)
	app.Post("/v1/evals/scores", s.createScore)
	app.Get("/v1/evals/scores", s.listScores)
	app.Get("/v1/evals/traces", s.listTraces)
	app.Post("/v1/evals/runs", s.runHandler)
	app.Get("/v1/evals/runs", s.listRuns)
	return app, s
}

// stubRunner returns a deterministic output + a fixed judge score, so run
// orchestration is testable without a live gateway. It satisfies EvalRunner.
type stubRunner struct{}

func (stubRunner) Complete(_ context.Context, authz, model string, input any) (string, error) {
	return "stub-output", nil
}

func (stubRunner) Judge(_ context.Context, authz string, judge judgeSpec, input, expected any, output string) (float64, string, error) {
	return 0.5, "stub reasoning", nil
}

// blockingRunner respects the run's deadline: Complete blocks until the context
// is cancelled, so a run wrapped in a short maxRunDuration cancels it instead of
// hanging. Used to prove the total-deadline bound (Red MED).
type blockingRunner struct{}

func (blockingRunner) Complete(ctx context.Context, authz, model string, input any) (string, error) {
	<-ctx.Done()
	return "", ctx.Err()
}

func (blockingRunner) Judge(ctx context.Context, authz string, judge judgeSpec, input, expected any, output string) (float64, string, error) {
	<-ctx.Done()
	return 0, "", ctx.Err()
}

func do(t *testing.T, app *zip.App, method, path, org string, body any) (int, []byte) {
	return doAuth(t, app, method, path, org, "", body)
}

// rawMsg wraps a pre-serialized JSON fragment so it is embedded verbatim (used to
// inject oversize raw item fields in red tests).
func rawMsg(s string) json.RawMessage { return json.RawMessage(s) }

// newReq builds a bare request (no headers) for tests that set headers by hand.
func newReq(method, path string, body io.Reader) *http.Request {
	return httptest.NewRequest(method, path, body)
}

// newReqJSON builds a JSON-bodied request (no auth headers) for hand-set headers.
func newReqJSON(method, path string, body any) *http.Request {
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(method, path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	return req
}

// send runs a hand-built request through the app and returns status + body.
func send(t *testing.T, app *zip.App, req *http.Request) (int, []byte) {
	t.Helper()
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test %s %s: %v", req.Method, req.URL.Path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func doAuth(t *testing.T, app *zip.App, method, path, org, authz string, body any) (int, []byte) {
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
		// Simulate a VALIDATED principal: in prod SanitizeIdentity sets X-User-Id
		// only from a verified token. Every helper request carries one so the
		// principal gate (Red HIGH) is satisfied; the forged-header red test builds
		// its request by hand WITHOUT X-User-Id to prove the gate blocks it.
		req.Header.Set("X-User-Id", "u_"+org)
	}
	if authz != "" {
		req.Header.Set("Authorization", authz)
	}
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// TestHTTPDatasetLifecycleAndIsolation is the end-to-end contract + isolation test:
// no-org is 403; a dataset is org-scoped; a sibling org sees none of it and gets 404
// on cross-tenant reads.
func TestHTTPDatasetLifecycleAndIsolation(t *testing.T) {
	app, _ := mountApp(t)

	// No org header → 403 on every collection.
	for _, p := range []string{"/v1/evals/datasets", "/v1/evals/evaluators", "/v1/evals/score-configs"} {
		if code, _ := do(t, app, http.MethodGet, p, "", nil); code != http.StatusForbidden {
			t.Fatalf("no-org GET %s want 403, got %d", p, code)
		}
	}

	// maxpower creates a dataset + item.
	if code, _ := do(t, app, http.MethodPost, "/v1/evals/datasets", "maxpower",
		map[string]any{"name": "qa", "description": "quality"}); code != http.StatusCreated {
		t.Fatalf("create dataset want 201, got %d", code)
	}
	if code, _ := do(t, app, http.MethodPost, "/v1/evals/dataset-items", "maxpower",
		map[string]any{"datasetName": "qa", "input": "2+2", "expectedOutput": "4"}); code != http.StatusCreated {
		t.Fatalf("create item want 201, got %d", code)
	}

	// maxpower lists its one dataset.
	code, body := do(t, app, http.MethodGet, "/v1/evals/datasets", "maxpower", nil)
	var listed struct {
		Data []datasetView `json:"data"`
	}
	_ = json.Unmarshal(body, &listed)
	if code != http.StatusOK || len(listed.Data) != 1 || listed.Data[0].Name != "qa" {
		t.Fatalf("maxpower should see [qa], got %d %+v", code, listed.Data)
	}

	// acme sees none, and 404s on maxpower's dataset.
	code, body = do(t, app, http.MethodGet, "/v1/evals/datasets", "acme", nil)
	_ = json.Unmarshal(body, &listed)
	if code != http.StatusOK || len(listed.Data) != 0 {
		t.Fatalf("acme must see zero datasets, got %d %+v", code, listed.Data)
	}
	if code, _ := do(t, app, http.MethodGet, "/v1/evals/datasets/qa", "acme", nil); code != http.StatusNotFound {
		t.Fatalf("acme GET maxpower dataset want 404, got %d", code)
	}
	// acme cannot add an item to maxpower's dataset (404, not silent create).
	if code, _ := do(t, app, http.MethodPost, "/v1/evals/dataset-items", "acme",
		map[string]any{"datasetName": "qa", "input": "x"}); code != http.StatusNotFound {
		t.Fatalf("acme add item to maxpower dataset want 404, got %d", code)
	}
	// acme cannot delete maxpower's dataset.
	if code, _ := do(t, app, http.MethodDelete, "/v1/evals/datasets/qa", "acme", nil); code != http.StatusNotFound {
		t.Fatalf("acme delete maxpower dataset want 404, got %d", code)
	}
	// maxpower's dataset survives.
	if code, _ := do(t, app, http.MethodGet, "/v1/evals/datasets/qa", "maxpower", nil); code != http.StatusOK {
		t.Fatalf("maxpower dataset must survive, got %d", code)
	}
}

// TestHTTPScoreIntegrity is the score-integrity contract: the validated boundary
// rejects NaN/Inf, out-of-range against a config, and forged categorical labels —
// and a stored score is readable back only by its own org.
func TestHTTPScoreIntegrity(t *testing.T) {
	app, _ := mountApp(t)

	// A NUMERIC config in [0,1].
	if code, _ := do(t, app, http.MethodPost, "/v1/evals/score-configs", "o",
		map[string]any{"name": "quality", "dataType": "NUMERIC", "minValue": 0, "maxValue": 1}); code != http.StatusCreated {
		t.Fatalf("create score-config want 201, got %d", code)
	}

	// Valid numeric score in range → 201.
	if code, _ := do(t, app, http.MethodPost, "/v1/evals/scores", "o",
		map[string]any{"name": "quality", "value": 0.9}); code != http.StatusCreated {
		t.Fatalf("valid score want 201, got %d", code)
	}
	// Above configured max → 400.
	if code, _ := do(t, app, http.MethodPost, "/v1/evals/scores", "o",
		map[string]any{"name": "quality", "value": 2.5}); code != http.StatusBadRequest {
		t.Fatalf("out-of-range score want 400, got %d", code)
	}
	// Missing numeric value → 400.
	if code, _ := do(t, app, http.MethodPost, "/v1/evals/scores", "o",
		map[string]any{"name": "quality"}); code != http.StatusBadRequest {
		t.Fatalf("missing value want 400, got %d", code)
	}

	// A CATEGORICAL config; a forged label outside the set is rejected.
	if code, _ := do(t, app, http.MethodPost, "/v1/evals/score-configs", "o",
		map[string]any{"name": "tone", "dataType": "CATEGORICAL", "categories": []string{"good", "bad"}}); code != http.StatusCreated {
		t.Fatalf("create categorical config want 201, got %d", code)
	}
	if code, _ := do(t, app, http.MethodPost, "/v1/evals/scores", "o",
		map[string]any{"name": "tone", "stringValue": "good"}); code != http.StatusCreated {
		t.Fatalf("valid categorical want 201, got %d", code)
	}
	if code, _ := do(t, app, http.MethodPost, "/v1/evals/scores", "o",
		map[string]any{"name": "tone", "stringValue": "sarcastic"}); code != http.StatusBadRequest {
		t.Fatalf("forged categorical label want 400, got %d", code)
	}

	// Scores are org-scoped: another org sees none of o's scores.
	code, body := do(t, app, http.MethodGet, "/v1/evals/scores", "o", nil)
	var listed struct {
		Data []scoreView `json:"data"`
	}
	_ = json.Unmarshal(body, &listed)
	if code != http.StatusOK || len(listed.Data) != 2 { // 0.9 numeric + good categorical
		t.Fatalf("o should see its 2 scores, got %d %+v", code, listed.Data)
	}
	code, body = do(t, app, http.MethodGet, "/v1/evals/scores", "intruder", nil)
	_ = json.Unmarshal(body, &listed)
	if code != http.StatusOK || len(listed.Data) != 0 {
		t.Fatalf("intruder must see zero scores, got %d %+v", code, listed.Data)
	}
}

// TestHTTPRunRequiresAuthAndOwnDataset proves the run path fails closed: no bearer
// → 401; a dataset the org doesn't own → 404; and a real run over a stub runner
// scores every active item.
func TestHTTPRunRequiresAuthAndOwnDataset(t *testing.T) {
	app, _ := mountApp(t)

	// No Authorization bearer → 401 (even with a valid org + dataset).
	if code, _ := do(t, app, http.MethodPost, "/v1/evals/datasets", "o", map[string]any{"name": "qa"}); code != http.StatusCreated {
		t.Fatalf("seed dataset: %d", code)
	}
	if code, _ := do(t, app, http.MethodPost, "/v1/evals/runs", "o",
		map[string]any{"dataset": "qa", "model": "m"}); code != http.StatusUnauthorized {
		t.Fatalf("run without bearer want 401, got %d", code)
	}

	// With a bearer but a dataset the org does not own → 404.
	if code, _ := doAuth(t, app, http.MethodPost, "/v1/evals/runs", "o", "Bearer hk-test",
		map[string]any{"dataset": "does-not-exist", "model": "m"}); code != http.StatusNotFound {
		t.Fatalf("run on missing dataset want 404, got %d", code)
	}

	// Seed two active items, then run: the stub runner scores both.
	for _, in := range []string{"a", "b"} {
		if code, _ := do(t, app, http.MethodPost, "/v1/evals/dataset-items", "o",
			map[string]any{"datasetName": "qa", "input": in, "expectedOutput": in}); code != http.StatusCreated {
			t.Fatalf("seed item %q: %d", in, code)
		}
	}
	code, body := doAuth(t, app, http.MethodPost, "/v1/evals/runs", "o", "Bearer hk-test",
		map[string]any{"dataset": "qa", "model": "m"})
	if code != http.StatusOK {
		t.Fatalf("run want 200, got %d (%s)", code, body)
	}
	var sum runSummary
	if err := json.Unmarshal(body, &sum); err != nil {
		t.Fatalf("run summary json: %v", err)
	}
	if sum.Items != 2 || sum.Scored != 2 {
		t.Fatalf("run should score 2/2 items, got %+v", sum)
	}
	if sum.AvgScore != 0.5 { // stub judge returns 0.5
		t.Fatalf("avg score want 0.5, got %v", sum.AvgScore)
	}

	// The run is now a listable record, org-scoped.
	code, body = do(t, app, http.MethodGet, "/v1/evals/runs", "o", nil)
	if code != http.StatusOK || !bytes.Contains(body, []byte(`"scored":2`)) {
		t.Fatalf("run record want scored:2, got %d %s", code, body)
	}
	// The stub judge wrote 2 score events into telemetry, readable by o only.
	code, body = do(t, app, http.MethodGet, "/v1/evals/scores", "o", nil)
	var scores struct {
		Data []scoreView `json:"data"`
	}
	_ = json.Unmarshal(body, &scores)
	if code != http.StatusOK || len(scores.Data) != 2 {
		t.Fatalf("run should have written 2 score events, got %d %+v", code, scores.Data)
	}
}

// TestRunDeadlineBounded proves the total wall-clock bound (Red MED): with a tiny
// maxRunDuration and a runner that blocks, the run is CANCELLED (never hangs),
// scores nothing, returns 502, and each item carries an honest deadline error.
func TestRunDeadlineBounded(t *testing.T) {
	old := maxRunDuration
	maxRunDuration = 50 * time.Millisecond
	defer func() { maxRunDuration = old }()

	app, s := mountApp(t)
	s.runner = blockingRunner{} // handlers read s.runner per call, so this takes effect

	if code, _ := do(t, app, http.MethodPost, "/v1/evals/datasets", "o", map[string]any{"name": "qa"}); code != http.StatusCreated {
		t.Fatalf("seed dataset: %d", code)
	}
	for _, in := range []string{"a", "b"} {
		if code, _ := do(t, app, http.MethodPost, "/v1/evals/dataset-items", "o",
			map[string]any{"datasetName": "qa", "input": in, "expectedOutput": in}); code != http.StatusCreated {
			t.Fatalf("seed item %q: %d", in, code)
		}
	}

	done := make(chan struct{})
	var code int
	var body []byte
	go func() {
		code, body = doAuth(t, app, http.MethodPost, "/v1/evals/runs", "o", "Bearer hk-test",
			map[string]any{"dataset": "qa", "model": "m"})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("run did NOT respect the deadline — it hung past 5s (bound failed)")
	}

	if code != http.StatusBadGateway {
		t.Fatalf("deadline-cancelled run scores nothing → want 502, got %d (%s)", code, body)
	}
	var sum runSummary
	if err := json.Unmarshal(body, &sum); err != nil {
		t.Fatalf("summary json: %v", err)
	}
	if sum.Scored != 0 {
		t.Fatalf("a cancelled run must score 0, got %d", sum.Scored)
	}
	if len(sum.Results) == 0 || !strings.Contains(sum.Results[0].Error, "context") {
		t.Fatalf("item error should reflect the cancelled context, got %+v", sum.Results)
	}
}
