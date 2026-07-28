package eval

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/zap-proto/zip"
)

// doProj runs a request carrying a validated principal (X-User-Id) plus an
// explicit X-Project-Id, so the project-scoping paths (write attribution + read
// narrowing) are exercised end-to-end. A blank project omits the header (the
// default-project / whole-org view).
func doProj(t *testing.T, app *zip.App, method, path, org, project, authz string, body any) (int, []byte) {
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
		req.Header.Set("X-User-Id", "u_"+org)
	}
	if project != "" {
		req.Header.Set("X-Project-Id", project)
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

// seedRun creates a dataset + one item and runs it, so telemetry holds one trace.
func seedRun(t *testing.T, app *zip.App, org, project, dataset, authz string) {
	t.Helper()
	if code, _ := doProj(t, app, http.MethodPost, "/v1/evals/datasets", org, project, authz,
		map[string]any{"name": dataset}); code != http.StatusCreated {
		t.Fatalf("seed dataset %q: %d", dataset, code)
	}
	if code, _ := doProj(t, app, http.MethodPost, "/v1/evals/dataset-items", org, project, authz,
		map[string]any{"datasetName": dataset, "input": "2+2", "expectedOutput": "4"}); code != http.StatusCreated {
		t.Fatalf("seed item: %d", code)
	}
	if code, _ := doProj(t, app, http.MethodPost, "/v1/evals/runs", org, project, authz,
		map[string]any{"dataset": dataset, "model": "m", "runName": dataset + "-run"}); code != http.StatusOK {
		t.Fatalf("run %q: %d", dataset, code)
	}
}

// TestTraceAttributionRecorded proves a run stamps every observability dimension
// on its item trace: the session groups by run, latency is real, the credential
// is stored ONLY as a non-reversible ref, and the plaintext key never appears.
func TestTraceAttributionRecorded(t *testing.T) {
	app, _ := mountApp(t)
	const bearer = "Bearer hk-secret-key"
	seedRun(t, app, "o", "", "qa", bearer)

	code, body := doProj(t, app, http.MethodGet, "/v1/evals/traces", "o", "", "", nil)
	if code != http.StatusOK {
		t.Fatalf("list traces: %d %s", code, body)
	}
	var listed struct {
		Data []traceView `json:"data"`
	}
	if err := json.Unmarshal(body, &listed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(listed.Data) != 1 {
		t.Fatalf("want 1 trace, got %d", len(listed.Data))
	}
	tr := listed.Data[0]

	if tr.SessionID != "qa-run" {
		t.Fatalf("session must group by run: got %q, want qa-run", tr.SessionID)
	}
	if tr.LatencyMs == nil {
		t.Fatal("trace must carry a real latency (start/end were timed), got nil")
	}
	if tr.StartTime == "" || tr.EndTime == "" {
		t.Fatalf("trace must carry start/end, got start=%q end=%q", tr.StartTime, tr.EndTime)
	}

	// The credential is stored ONLY as a SHA-256 ref — never plaintext.
	want := sha256.Sum256([]byte("hk-secret-key"))
	if tr.APIKeyHash != hex.EncodeToString(want[:]) {
		t.Fatalf("apiKeyHash = %q, want sha256(hk-secret-key)", tr.APIKeyHash)
	}
	// The plaintext must not appear anywhere in the response body.
	if bytes.Contains(body, []byte("hk-secret-key")) {
		t.Fatalf("plaintext credential leaked into trace list: %s", body)
	}
}

// TestTraceProjectIsolation proves per-project narrowing on the trace list: a
// named project sees only its own traces; the default project (no header) sees
// the whole org (every project). Org stays the hard tenant boundary throughout.
func TestTraceProjectIsolation(t *testing.T) {
	app, _ := mountApp(t)
	const bearer = "Bearer hk-test"

	seedRun(t, app, "o", "alpha", "da", bearer) // project alpha
	seedRun(t, app, "o", "beta", "db", bearer)  // project beta
	seedRun(t, app, "o", "", "dd", bearer)      // default project (whole org)

	// alpha sees only its own trace.
	code, body := doProj(t, app, http.MethodGet, "/v1/evals/traces", "o", "alpha", "", nil)
	traces := unmarshalTraces(t, code, body)
	if len(traces) != 1 || traces[0].ProjectID != "alpha" || traces[0].SessionID != "da-run" {
		t.Fatalf("alpha must see exactly its own trace, got %+v", traces)
	}

	// beta sees only its own trace.
	code, body = doProj(t, app, http.MethodGet, "/v1/evals/traces", "o", "beta", "", nil)
	traces = unmarshalTraces(t, code, body)
	if len(traces) != 1 || traces[0].ProjectID != "beta" {
		t.Fatalf("beta must see exactly its own trace, got %+v", traces)
	}

	// The default project (no X-Project-Id) sees the whole org — all three traces.
	code, body = doProj(t, app, http.MethodGet, "/v1/evals/traces", "o", "", "", nil)
	traces = unmarshalTraces(t, code, body)
	if len(traces) != 3 {
		t.Fatalf("default project must see the whole org (3 traces), got %d", len(traces))
	}

	// A sibling org sees none of o's traces (org is the hard boundary).
	code, body = doProj(t, app, http.MethodGet, "/v1/evals/traces", "intruder", "alpha", "", nil)
	traces = unmarshalTraces(t, code, body)
	if len(traces) != 0 {
		t.Fatalf("cross-org read must be empty, got %d", len(traces))
	}
}

// TestHashCredential is the unit contract for the credential ref: Bearer-stripped,
// SHA-256 hex, empty in → empty out (never a hash of "").
func TestHashCredential(t *testing.T) {
	if got := hashCredential(""); got != "" {
		t.Fatalf("empty credential must yield empty ref, got %q", got)
	}
	if got := hashCredential("  "); got != "" {
		t.Fatalf("blank credential must yield empty ref, got %q", got)
	}
	want := sha256.Sum256([]byte("hk-abc"))
	if got := hashCredential("Bearer hk-abc"); got != hex.EncodeToString(want[:]) {
		t.Fatalf("hashCredential(Bearer hk-abc) = %q", got)
	}
	// Bearer-less raw token hashes the same as the Bearer-prefixed form.
	if hashCredential("hk-abc") != hashCredential("Bearer hk-abc") {
		t.Fatal("raw and Bearer-prefixed tokens must hash identically")
	}
}

func unmarshalTraces(t *testing.T, code int, body []byte) []traceView {
	t.Helper()
	if code != http.StatusOK {
		t.Fatalf("list traces: %d %s", code, body)
	}
	var listed struct {
		Data []traceView `json:"data"`
	}
	if err := json.Unmarshal(body, &listed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return listed.Data
}
