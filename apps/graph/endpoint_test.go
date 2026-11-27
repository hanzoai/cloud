package graph

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/zap-proto/zip"
)

// The ONE way these tests reach the plane over HTTP.
//
// There were four helpers doing this — two spellings of filing an assertion and
// two of reading one back, differing only in which of them remembered to send a
// project. A test harness that can be written two ways is a harness where a test
// proves something about the spelling it happened to pick, which is how the
// project-scoped endpoint went unexercised by the read helper that predated it.

// call is the primitive: a request as the caller of one project, carrying the org
// and the validated principal every op here requires. An empty project names none,
// which is the org's default graph.
func call(t *testing.T, app *zip.App, method, path, project string, body any) (int, []byte) {
	t.Helper()
	return callAs(t, app, nil, method, path, project, body)
}

// callAs is call with the authority headers the identity boundary mints — an
// org admin, a SuperAdmin's home org — set on top. They are what an erasure is
// judged by, and nothing else here reads them.
func callAs(t *testing.T, app *zip.App, authority map[string]string, method, path, project string, body any) (int, []byte) {
	t.Helper()
	var payload io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal %s %s: %v", method, path, err)
		}
		payload = strings.NewReader(string(b))
	}
	req, err := http.NewRequest(method, "http://cloud"+path, payload)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set(zip.HeaderOrg, "acme")
	req.Header.Set(zip.HeaderUser, "acme/z@acme.test")
	if project != "" {
		req.Header.Set("X-Project-Id", project)
	}
	for k, v := range authority {
		req.Header.Set(k, v)
	}
	resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// assertFact files one assertion into the caller's graph and fails unless the
// endpoint took it.
func assertFact(t *testing.T, app *zip.App, project, entity, relation, value string, names bool) {
	t.Helper()
	assertAs(t, app, nil, project, entity, relation, value, names)
}

// assertAs is assertFact with the authority headers callAs sets: a declaration
// is filed by an org admin, and nothing else here needs one.
func assertAs(t *testing.T, app *zip.App, authority map[string]string, project, entity, relation, value string, names bool) {
	t.Helper()
	code, b := callAs(t, app, authority, http.MethodPost, "/v1/graph", project, map[string]any{
		"assertions": []map[string]any{{
			"entity": entity, "relation": relation, "value": value, "names": names,
			"at": "2026-01-01T00:00:00Z", "seen": "2026-01-01T00:00:00Z",
			"source": "test", "evidence": "test://" + entity,
		}},
	})
	if code >= 300 {
		t.Fatalf("assert %s %s in %q: status %d: %s", entity, relation, project, code, b)
	}
}

// answered is what a read or a search returned. Both endpoints answer in the read
// shape, which is why one reader serves them and why a caller can hand what it
// searched for to anything that takes assertions.
func answered(t *testing.T, app *zip.App, project, path string) []wireFact {
	t.Helper()
	code, b := call(t, app, http.MethodGet, path, project, nil)
	if code >= 300 {
		t.Fatalf("GET %s in %q: status %d: %s", path, project, code, b)
	}
	var out struct {
		Assertions []wireFact `json:"assertions"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("GET %s answered something that is not the read shape: %v: %s", path, err, b)
	}
	return out.Assertions
}

// valuesOf projects assertions to what they claim, which is what most of these
// tests actually assert about.
func valuesOf(facts []wireFact) []string {
	out := make([]string, 0, len(facts))
	for _, f := range facts {
		out = append(out, f.Value)
	}
	return out
}
