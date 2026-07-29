package compliance

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	fiber "github.com/zap-proto/fiber/v3"
	"github.com/zap-proto/zip"
)

// doRaw issues a request and returns the raw response — for the assertions that
// are about the envelope (headers, status on a non-JSON body) rather than the
// decoded JSON that do() hands back.
func doRaw(t *testing.T, app *zip.App, rq *http.Request) *http.Response {
	t.Helper()
	resp, err := app.Fiber().Test(rq, fiber.TestConfig{Timeout: testTimeout, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("Test %s %s: %v", rq.Method, rq.URL.Path, err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

// TestTypedOpsPreserveTheWire pins the envelope details the typed conversion had
// to carry over from the untyped handlers, none of which the behavior suite
// above measures: the no-store cache pin on the PII-bearing and per-org reads,
// the 1 MiB body cap on the JSON writes, the ?limit query binding, and the
// empty-body tolerance a zero In must keep.
func TestTypedOpsPreserveTheWire(t *testing.T) {
	app, _ := mount(t)
	org := "org_wire"

	// Seed two subjects so the limit assertion has something to cap.
	for _, ref := range []string{"a", "b"} {
		code, _ := do(t, app, http.MethodPost, "/v1/compliance/subjects", org,
			map[string]any{"kind": "individual", "ref": ref})
		if code != http.StatusCreated {
			t.Fatalf("seed subject %s: %d", ref, code)
		}
	}

	t.Run("no-store rides the per-org and PII reads", func(t *testing.T) {
		for _, path := range []string{
			"/v1/compliance/status",
			"/v1/compliance/subjects",
			"/v1/compliance/audit",
		} {
			rq := httptest.NewRequest(http.MethodGet, path, nil)
			rq.Header.Set("X-Org-Id", org)
			rq.Header.Set("X-User-Id", "u_"+org)
			resp := doRaw(t, app, rq)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("GET %s: %d", path, resp.StatusCode)
			}
			if cc := resp.Header.Get("Cache-Control"); cc != "no-store" {
				t.Errorf("GET %s Cache-Control = %q, want no-store", path, cc)
			}
		}
	})

	t.Run("?limit binds through the typed op", func(t *testing.T) {
		code, out := do(t, app, http.MethodGet, "/v1/compliance/subjects?limit=1", org, nil)
		if code != http.StatusOK {
			t.Fatalf("list: %d", code)
		}
		if data, _ := out["data"].([]any); len(data) != 1 {
			t.Errorf("limit=1 returned %d rows", len(out["data"].([]any)))
		}
	})

	t.Run("the 1 MiB body cap holds", func(t *testing.T) {
		rq := httptest.NewRequest(http.MethodPost, "/v1/compliance/subjects",
			bytes.NewReader(make([]byte, maxBody+1)))
		rq.Header.Set("Content-Type", "application/json")
		rq.Header.Set("X-Org-Id", org)
		rq.Header.Set("X-User-Id", "u_"+org)
		if resp := doRaw(t, app, rq); resp.StatusCode != http.StatusRequestEntityTooLarge {
			t.Errorf("oversized body: %d, want 413", resp.StatusCode)
		}
	})

	t.Run("an empty POST body still reaches the handler's own refusal", func(t *testing.T) {
		// The untyped decode() read an empty body as the zero value; zip's binder
		// does the same, so the answer stays the handler's 400 about kind — never
		// a binder error about the body's absence.
		code, out := do(t, app, http.MethodPost, "/v1/compliance/subjects", org, nil)
		if code != http.StatusBadRequest {
			t.Fatalf("empty body: %d, want 400", code)
		}
		if msg, _ := out["error"].(string); !strings.Contains(msg, "kind must be") {
			t.Errorf("empty body answered %q, want the handler's kind refusal", msg)
		}
	})
}
