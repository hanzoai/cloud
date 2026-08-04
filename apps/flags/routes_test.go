package flags

// The wire contract of the typed /v1/flags surface — the parts a status-code
// test would not see.
//
// PUT /v1/flags/defs/:key is the one route here whose BODY IS AN OPEN DOCUMENT:
// the store keeps it verbatim, minus the key the server forces. A typed op whose
// In were an ordinary struct would drop every field the struct does not name,
// which is silent data loss with a perfectly green 200. These tests pin the
// round trip, the key-from-the-URL rule, and the two non-object bodies the store
// refuses — including `null`, which used to panic.

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	luxlog "github.com/luxfi/log"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
)

var httpCfg = zip.TestConfig{Timeout: 10 * time.Second, FailOnTimeout: true}

// mountHTTP puts the flag surface on a bare app over a temp-dir store tree.
func mountHTTP(t *testing.T) *zip.App {
	t.Helper()
	c := newTestClient(t)
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	svc := &cloud.Service[state]{
		Base:  cloud.NewBase(cloud.Deps{Logger: luxlog.New("test")}, "flags"),
		State: state{client: c},
	}
	routes(app, svc)
	return app
}

// do drives one request. A non-empty org sets BOTH X-Org-Id and X-User-Id, which
// is the pair the identity boundary mints for a validated principal.
func do(t *testing.T, app *zip.App, method, path, org string, body []byte) (int, []byte) {
	t.Helper()
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	rq := httptest.NewRequest(method, path, r)
	if body != nil {
		rq.Header.Set("Content-Type", "application/json")
	}
	if org != "" {
		rq.Header.Set("X-Org-Id", org)
		rq.Header.Set("X-User-Id", "u_"+org)
	}
	resp, err := app.Test(rq, httpCfg)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// TestPutDefinitionKeepsTheWholeDocument is the data-loss guard: a definition
// carrying fields no Go struct in this package names must come back byte-equal.
func TestPutDefinitionKeepsTheWholeDocument(t *testing.T) {
	app := mountHTTP(t)
	const doc = `{"key":"new-editor","active":true,"filters":{"groups":[{"rollout_percentage":25,"properties":[{"key":"plan","value":"pro"}]}]},"ensure_experience_continuity":true,"deleted":false}`

	code, body := do(t, app, http.MethodPut, "/v1/flags/defs/new-editor", "acme", []byte(doc))
	if code != http.StatusOK {
		t.Fatalf("put: want 200, got %d (%s)", code, body)
	}
	var row DefRow
	if err := json.Unmarshal(body, &row); err != nil {
		t.Fatalf("decode row: %v", err)
	}
	var got, want map[string]any
	if err := json.Unmarshal(row.Definition, &got); err != nil {
		t.Fatalf("decode stored definition: %v", err)
	}
	if err := json.Unmarshal([]byte(doc), &want); err != nil {
		t.Fatalf("decode source definition: %v", err)
	}
	for k, v := range want {
		gv, ok := got[k]
		if !ok {
			t.Fatalf("stored definition dropped %q — the typed In is not carrying the whole document", k)
		}
		if !jsonEqual(gv, v) {
			t.Fatalf("stored %q = %v, want %v", k, gv, v)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("stored definition has %d fields, source had %d", len(got), len(want))
	}
}

// TestPutDefinitionKeyComesFromTheURL pins the addressing authority: the URL
// names the record, whatever the document's own "key" claims.
func TestPutDefinitionKeyComesFromTheURL(t *testing.T) {
	app := mountHTTP(t)
	code, body := do(t, app, http.MethodPut, "/v1/flags/defs/real-key",
		"acme", []byte(`{"key":"impostor","active":true}`))
	if code != http.StatusOK {
		t.Fatalf("put: want 200, got %d (%s)", code, body)
	}
	var row DefRow
	if err := json.Unmarshal(body, &row); err != nil {
		t.Fatalf("decode row: %v", err)
	}
	if row.Key != "real-key" {
		t.Fatalf("row key = %q, want the path's %q", row.Key, "real-key")
	}
	var def map[string]any
	if err := json.Unmarshal(row.Definition, &def); err != nil {
		t.Fatalf("decode definition: %v", err)
	}
	if def["key"] != "real-key" {
		t.Fatalf("stored definition key = %v, want the path's %q", def["key"], "real-key")
	}
	// And it is readable back under the URL's key, not the body's.
	if code, _ := do(t, app, http.MethodGet, "/v1/flags/defs/real-key", "acme", nil); code != http.StatusOK {
		t.Fatalf("get real-key: want 200, got %d", code)
	}
	if code, _ := do(t, app, http.MethodGet, "/v1/flags/defs/impostor", "acme", nil); code != http.StatusNotFound {
		t.Fatalf("get impostor: want 404, got %d", code)
	}
}

// TestPutDefinitionRefusesNonObjects covers the three bodies that are not a
// definition. `null` is the one that mattered: it unmarshals into a nil map
// without error, and the store's next line assigned into it.
func TestPutDefinitionRefusesNonObjects(t *testing.T) {
	app := mountHTTP(t)
	for _, tc := range []struct {
		name string
		body []byte
	}{
		{"null", []byte(`null`)},
		{"array", []byte(`[1,2,3]`)},
		{"string", []byte(`"nope"`)},
		{"empty", []byte(``)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, body := do(t, app, http.MethodPut, "/v1/flags/defs/k", "acme", tc.body)
			if code != http.StatusBadRequest {
				t.Fatalf("put %s: want 400, got %d (%s)", tc.name, code, body)
			}
		})
	}
}

// TestFlagOpsRefuseAnUnvalidatedPrincipal is the tenancy guard: the org is a
// REQUEST fact, never an In field, so a request with no validated principal
// reaches no store at all.
func TestFlagOpsRefuseAnUnvalidatedPrincipal(t *testing.T) {
	app := mountHTTP(t)
	for _, tc := range []struct {
		method, path string
		body         []byte
	}{
		{http.MethodGet, "/v1/flags/defs", nil},
		{http.MethodGet, "/v1/flags/defs/k", nil},
		{http.MethodDelete, "/v1/flags/defs/k", nil},
		{http.MethodGet, "/v1/flags/activity", nil},
	} {
		code, body := do(t, app, tc.method, tc.path, "", tc.body)
		if code != http.StatusForbidden {
			t.Fatalf("%s %s unvalidated: want 403, got %d (%s)", tc.method, tc.path, code, body)
		}
	}
	// Health is deliberately NOT gated — liveness must be probe-able.
	if code, _ := do(t, app, http.MethodGet, "/v1/flags/health", "", nil); code != http.StatusOK {
		t.Fatalf("health unvalidated: want 200, got %d", code)
	}
}

// TestDeleteDefinitionAnswers200WithTheKey pins the delete wire: this route has
// always answered 200 with {"deleted": key}, not 204.
func TestDeleteDefinitionAnswers200WithTheKey(t *testing.T) {
	app := mountHTTP(t)
	if code, body := do(t, app, http.MethodPut, "/v1/flags/defs/gone", "acme", []byte(`{"active":true}`)); code != http.StatusOK {
		t.Fatalf("seed: want 200, got %d (%s)", code, body)
	}
	code, body := do(t, app, http.MethodDelete, "/v1/flags/defs/gone", "acme", nil)
	if code != http.StatusOK {
		t.Fatalf("delete: want 200, got %d (%s)", code, body)
	}
	var out deletedOut
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Deleted != "gone" {
		t.Fatalf("deleted = %q, want %q", out.Deleted, "gone")
	}
	if code, _ := do(t, app, http.MethodDelete, "/v1/flags/defs/gone", "acme", nil); code != http.StatusNotFound {
		t.Fatalf("second delete: want 404, got %d", code)
	}
}

// TestDefinitionsAreOrgScoped proves the boundary holds across two tenants.
func TestDefinitionsAreOrgScoped(t *testing.T) {
	app := mountHTTP(t)
	if code, _ := do(t, app, http.MethodPut, "/v1/flags/defs/secret", "acme", []byte(`{"active":true}`)); code != http.StatusOK {
		t.Fatalf("acme seed failed")
	}
	if code, body := do(t, app, http.MethodGet, "/v1/flags/defs/secret", "beta", nil); code != http.StatusNotFound {
		t.Fatalf("beta reading acme's flag: want 404, got %d (%s)", code, body)
	}
	code, body := do(t, app, http.MethodGet, "/v1/flags/defs", "beta", nil)
	if code != http.StatusOK {
		t.Fatalf("beta list: want 200, got %d (%s)", code, body)
	}
	var out defsOut
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Data) != 0 {
		t.Fatalf("beta sees %d of acme's definitions", len(out.Data))
	}
}

func jsonEqual(a, b any) bool {
	ab, err1 := json.Marshal(a)
	bb, err2 := json.Marshal(b)
	return err1 == nil && err2 == nil && bytes.Equal(ab, bb)
}

// TestEvaluateRelaysTheEvaluatorVerbatim is the relay guard. The evaluate op's Out
// is the evaluator's OWN JSON, handed back unchanged — a typed Out that re-encoded
// it through a Go struct would drop every field the struct does not name, and the
// PostHog-shaped response is exactly what existing SDK consumers read. The empty
// store answers the engine's zero verdict, which is the shape to pin.
func TestEvaluateRelaysTheEvaluatorVerbatim(t *testing.T) {
	app := mountHTTP(t)
	for _, path := range []string{"/v1/flags", "/v1/flags/decide"} {
		code, body := do(t, app, http.MethodPost, path, "acme", []byte(`{"distinct_id":"u1"}`))
		if code != http.StatusOK {
			t.Fatalf("%s: want 200, got %d (%s)", path, code, body)
		}
		var got map[string]any
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatalf("%s: response is not a JSON object: %v (%s)", path, err, body)
		}
		for _, k := range []string{"featureFlags", "featureFlagPayloads", "errorsWhileComputingFlags"} {
			if _, ok := got[k]; !ok {
				t.Fatalf("%s: relayed response lost %q — the Out is re-encoding, not relaying (%s)", path, k, body)
			}
		}
	}
	// distinct_id is the one required field, and it is refused before the store.
	if code, _ := do(t, app, http.MethodPost, "/v1/flags", "acme", []byte(`{}`)); code != http.StatusBadRequest {
		t.Fatalf("missing distinct_id: want 400, got %d", code)
	}
	if code, _ := do(t, app, http.MethodPost, "/v1/flags", "", []byte(`{"distinct_id":"u1"}`)); code != http.StatusForbidden {
		t.Fatalf("unvalidated evaluate: want 403, got %d", code)
	}
}
