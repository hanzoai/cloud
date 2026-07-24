package webhooks

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// mountApp mounts ONLY the registry surface (no bus URL ⇒ the dispatcher is inert), so
// the CRUD tests never touch NATS.
func mountApp(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	if err := Mount(app, cloud.Deps{Logger: luxlog.New("test"), DataDir: t.TempDir()}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	t.Cleanup(func() { _ = Shutdown(nil) })
	return app
}

// do issues a request as a VALIDATED principal for org (X-User-Id present ⇒
// principal.Validated), or anonymously when org == "".
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
		req.Header.Set("X-User-Id", "u_"+org) // validated principal
	}
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// TestRegistryCRUDAndSecret proves the create→read lifecycle and the secret contract:
// the signing secret is returned ONLY on create, never on list/get.
func TestRegistryCRUDAndSecret(t *testing.T) {
	app := mountApp(t)

	// create
	code, body := do(t, app, http.MethodPost, "/v1/webhooks", "acme", map[string]any{
		"url":         "https://hooks.acme.test/in",
		"events":      []string{"commerce.order.*", "commerce.>"},
		"description": "order feed",
	})
	if code != http.StatusCreated {
		t.Fatalf("create want 201, got %d (%s)", code, body)
	}
	var created Endpoint
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatalf("create json: %v (%s)", err, body)
	}
	if created.ID == "" || created.Secret == "" {
		t.Fatalf("create must return id + secret, got %+v", created)
	}
	if created.Status != "active" || created.Org != "acme" || len(created.Events) != 2 {
		t.Fatalf("create round-trip mismatch: %+v", created)
	}

	// get: secret MUST be redacted
	code, body = do(t, app, http.MethodGet, "/v1/webhooks/"+created.ID, "acme", nil)
	if code != http.StatusOK {
		t.Fatalf("get want 200, got %d (%s)", code, body)
	}
	var got Endpoint
	_ = json.Unmarshal(body, &got)
	if got.Secret != "" {
		t.Fatalf("get must NOT return the secret, got %q", got.Secret)
	}
	if got.ID != created.ID {
		t.Fatalf("get id mismatch: %s vs %s", got.ID, created.ID)
	}

	// list: one row, secret redacted
	code, body = do(t, app, http.MethodGet, "/v1/webhooks", "acme", nil)
	if code != http.StatusOK {
		t.Fatalf("list want 200, got %d (%s)", code, body)
	}
	var listResp struct {
		Data []Endpoint `json:"data"`
	}
	_ = json.Unmarshal(body, &listResp)
	if len(listResp.Data) != 1 || listResp.Data[0].Secret != "" {
		t.Fatalf("list must return 1 endpoint with redacted secret, got %+v", listResp.Data)
	}

	// update: disable + change url; secret unchanged and still redacted
	code, body = do(t, app, http.MethodPut, "/v1/webhooks/"+created.ID, "acme", map[string]any{
		"url":    "https://hooks.acme.test/v2",
		"events": []string{"commerce.order.created"},
		"status": "disabled",
	})
	if code != http.StatusOK {
		t.Fatalf("update want 200, got %d (%s)", code, body)
	}
	var updated Endpoint
	_ = json.Unmarshal(body, &updated)
	if updated.Status != "disabled" || updated.URL != "https://hooks.acme.test/v2" || updated.Secret != "" {
		t.Fatalf("update mismatch: %+v", updated)
	}

	// delete
	code, _ = do(t, app, http.MethodDelete, "/v1/webhooks/"+created.ID, "acme", nil)
	if code != http.StatusNoContent {
		t.Fatalf("delete want 204, got %d", code)
	}
	code, _ = do(t, app, http.MethodGet, "/v1/webhooks/"+created.ID, "acme", nil)
	if code != http.StatusNotFound {
		t.Fatalf("get after delete want 404, got %d", code)
	}
}

// TestRegistryAuth proves the unauthenticated gate (401, like clients/notify) and the
// https-required + status validation rules.
func TestRegistryAuth(t *testing.T) {
	app := mountApp(t)

	// no principal → 401 on every route
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/v1/webhooks"},
		{http.MethodPost, "/v1/webhooks"},
		{http.MethodGet, "/v1/webhooks/wh_x"},
		{http.MethodPut, "/v1/webhooks/wh_x"},
		{http.MethodDelete, "/v1/webhooks/wh_x"},
	} {
		if code, _ := do(t, app, tc.method, tc.path, "", map[string]any{"url": "https://x.test/h"}); code != http.StatusUnauthorized {
			t.Fatalf("anon %s %s want 401, got %d", tc.method, tc.path, code)
		}
	}

	// http:// (cleartext) rejected
	if code, _ := do(t, app, http.MethodPost, "/v1/webhooks", "acme", map[string]any{"url": "http://x.test/h"}); code != http.StatusBadRequest {
		t.Fatalf("http:// url want 400, got %d", code)
	}
	// missing url rejected
	if code, _ := do(t, app, http.MethodPost, "/v1/webhooks", "acme", map[string]any{"events": []string{"a.b"}}); code != http.StatusBadRequest {
		t.Fatalf("missing url want 400, got %d", code)
	}
	// bad status rejected
	if code, _ := do(t, app, http.MethodPost, "/v1/webhooks", "acme", map[string]any{"url": "https://x.test/h", "status": "paused"}); code != http.StatusBadRequest {
		t.Fatalf("bad status want 400, got %d", code)
	}
}

// TestRegistryOrgIsolation proves org A can never read, mutate, or delete org B's
// endpoints — the physical per-org store makes B's rows unreachable from A.
func TestRegistryOrgIsolation(t *testing.T) {
	app := mountApp(t)

	// B creates an endpoint.
	code, body := do(t, app, http.MethodPost, "/v1/webhooks", "borg", map[string]any{
		"url": "https://hooks.borg.test/in",
	})
	if code != http.StatusCreated {
		t.Fatalf("B create want 201, got %d (%s)", code, body)
	}
	var bEndpoint Endpoint
	_ = json.Unmarshal(body, &bEndpoint)

	// A cannot see B's endpoint in its own list.
	code, body = do(t, app, http.MethodGet, "/v1/webhooks", "acme", nil)
	var aList struct {
		Data []Endpoint `json:"data"`
	}
	_ = json.Unmarshal(body, &aList)
	if code != http.StatusOK || len(aList.Data) != 0 {
		t.Fatalf("A must see 0 endpoints, got %d (%s)", len(aList.Data), body)
	}

	// A cannot GET / PUT / DELETE B's endpoint by id (404, not another org's data).
	if code, _ := do(t, app, http.MethodGet, "/v1/webhooks/"+bEndpoint.ID, "acme", nil); code != http.StatusNotFound {
		t.Fatalf("A GET B's endpoint want 404, got %d", code)
	}
	if code, _ := do(t, app, http.MethodPut, "/v1/webhooks/"+bEndpoint.ID, "acme", map[string]any{"url": "https://evil.test/h"}); code != http.StatusNotFound {
		t.Fatalf("A PUT B's endpoint want 404, got %d", code)
	}
	if code, _ := do(t, app, http.MethodDelete, "/v1/webhooks/"+bEndpoint.ID, "acme", nil); code != http.StatusNotFound {
		t.Fatalf("A DELETE B's endpoint want 404, got %d", code)
	}

	// B still has its endpoint intact.
	code, body = do(t, app, http.MethodGet, "/v1/webhooks/"+bEndpoint.ID, "borg", nil)
	if code != http.StatusOK {
		t.Fatalf("B GET own endpoint want 200, got %d (%s)", code, body)
	}
}
