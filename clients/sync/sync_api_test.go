package sync

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/zap-proto/fiber/v3"
	"github.com/zap-proto/zip"
)

var testCfg = fiber.TestConfig{Timeout: 10 * time.Second, FailOnTimeout: true}

// do runs a control-plane JSON request through the Fiber test harness, carrying the
// gateway identity headers a validated principal needs.
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
		req.Header.Set("X-User-Id", "u_"+org)
	}
	resp, err := app.Fiber().Test(req, testCfg)
	if err != nil {
		t.Fatalf("Test %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

const widgetsURL = "https://github.com/acme-gh/widgets.git"

// TestSyncCRUD proves the /v1/sync control plane: create (upsert + derived git
// target), list, get, patch, run, delete, and org-scoping.
func TestSyncCRUD(t *testing.T) {
	app := mountSync(t)

	// No org → 403.
	if code, _ := do(t, app, http.MethodGet, "/v1/sync", "", nil); code != http.StatusForbidden {
		t.Fatalf("no-org list want 403, got %d", code)
	}

	// Create a github→native link (run:false so no background git work in the test).
	code, body := do(t, app, http.MethodPost, "/v1/sync", "acme", map[string]any{
		"source":    map[string]any{"provider": "github", "locator": widgetsURL},
		"direction": "both",
	})
	if code != http.StatusOK {
		t.Fatalf("create want 200, got %d (%s)", code, body)
	}
	var v syncView
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("create json: %v (%s)", err, body)
	}
	if v.ID == "" || v.Kind != "git" || v.Direction != "both" {
		t.Fatalf("unexpected link view: %+v", v)
	}
	if v.Source.Provider != "github" || v.Source.Locator != widgetsURL {
		t.Fatalf("source not preserved: %+v", v.Source)
	}
	if v.Target.Provider != provNative || v.Target.Locator != "widgets" {
		t.Fatalf("target must derive to native widgets: %+v", v.Target)
	}

	// Re-create the same source→target is an UPSERT (same id, no duplicate).
	code, body2 := do(t, app, http.MethodPost, "/v1/sync", "acme", map[string]any{
		"source":    map[string]any{"provider": "github", "locator": widgetsURL},
		"direction": "pull",
	})
	if code != http.StatusOK {
		t.Fatalf("re-create want 200, got %d (%s)", code, body2)
	}
	var v2 syncView
	_ = json.Unmarshal(body2, &v2)
	if v2.ID != v.ID || v2.Direction != "pull" {
		t.Fatalf("upsert must keep id and update direction, got %+v", v2)
	}

	// List → exactly one link for acme.
	code, body = do(t, app, http.MethodGet, "/v1/sync", "acme", nil)
	if code != http.StatusOK {
		t.Fatalf("list want 200, got %d", code)
	}
	var listed struct {
		Data []syncView `json:"data"`
	}
	_ = json.Unmarshal(body, &listed)
	if len(listed.Data) != 1 || listed.Data[0].ID != v.ID {
		t.Fatalf("acme should list exactly [%s], got %+v", v.ID, listed.Data)
	}

	// A different org sees none (physical per-org isolation).
	code, body = do(t, app, http.MethodGet, "/v1/sync", "beta", nil)
	_ = json.Unmarshal(body, &listed)
	if code != http.StatusOK || len(listed.Data) != 0 {
		t.Fatalf("beta must see zero links, got %d %+v", code, listed.Data)
	}

	// Get by id.
	if code, _ := do(t, app, http.MethodGet, "/v1/sync/"+v.ID, "acme", nil); code != http.StatusOK {
		t.Fatalf("get want 200, got %d", code)
	}
	// Get missing → 404. Get from another org → 404 (isolation).
	if code, _ := do(t, app, http.MethodGet, "/v1/sync/sync_missing", "acme", nil); code != http.StatusNotFound {
		t.Fatalf("get missing want 404, got %d", code)
	}
	if code, _ := do(t, app, http.MethodGet, "/v1/sync/"+v.ID, "beta", nil); code != http.StatusNotFound {
		t.Fatalf("cross-org get want 404, got %d", code)
	}

	// Patch the direction in place (id + endpoints preserved).
	code, body = do(t, app, http.MethodPatch, "/v1/sync/"+v.ID, "acme", map[string]any{"direction": "push"})
	if code != http.StatusOK {
		t.Fatalf("patch want 200, got %d (%s)", code, body)
	}
	var patched syncView
	_ = json.Unmarshal(body, &patched)
	if patched.ID != v.ID || patched.Direction != "push" {
		t.Fatalf("patch must keep id + set direction=push, got %+v", patched)
	}
	// Patch with a bad direction → 400.
	if code, _ := do(t, app, http.MethodPatch, "/v1/sync/"+v.ID, "acme", map[string]any{"direction": "nope"}); code != http.StatusBadRequest {
		t.Fatalf("patch bad direction want 400, got %d", code)
	}

	// Manual run → 202 accepted (the reconcile runs detached; git unmounted here, so
	// it fails closed in the background — the endpoint still queues).
	if code, _ := do(t, app, http.MethodPost, "/v1/sync/"+v.ID+"/run", "acme", nil); code != http.StatusAccepted {
		t.Fatalf("run want 202, got %d", code)
	}

	// Delete → 204, then gone.
	if code, _ := do(t, app, http.MethodDelete, "/v1/sync/"+v.ID, "acme", nil); code != http.StatusNoContent {
		t.Fatalf("delete want 204, got %d", code)
	}
	if code, _ := do(t, app, http.MethodGet, "/v1/sync/"+v.ID, "acme", nil); code != http.StatusNotFound {
		t.Fatalf("get after delete want 404, got %d", code)
	}
}

// TestSyncValidation proves the create-time guards: provider, https, host match,
// direction, kind.
func TestSyncValidation(t *testing.T) {
	app := mountSync(t)

	cases := []struct {
		name string
		body map[string]any
		want int
	}{
		{"ok", map[string]any{"source": map[string]any{"provider": "github", "locator": widgetsURL}}, http.StatusOK},
		{"bad provider", map[string]any{"source": map[string]any{"provider": "svn", "locator": widgetsURL}}, http.StatusBadRequest},
		{"not https", map[string]any{"source": map[string]any{"provider": "github", "locator": "http://github.com/a/b.git"}}, http.StatusBadRequest},
		{"host mismatch", map[string]any{"source": map[string]any{"provider": "github", "locator": "https://gitlab.com/a/b.git"}}, http.StatusBadRequest},
		{"bad direction", map[string]any{"source": map[string]any{"provider": "github", "locator": widgetsURL}, "direction": "sideways"}, http.StatusBadRequest},
		{"bad kind", map[string]any{"kind": "db", "source": map[string]any{"provider": "github", "locator": widgetsURL}}, http.StatusBadRequest},
		{"gitlab ok", map[string]any{"source": map[string]any{"provider": "gitlab", "locator": "https://gitlab.com/acme/widgets.git"}}, http.StatusOK},
	}
	for _, tc := range cases {
		code, body := do(t, app, http.MethodPost, "/v1/sync", "acme", tc.body)
		if code != tc.want {
			t.Fatalf("%s: want %d, got %d (%s)", tc.name, tc.want, code, body)
		}
	}
}
