package prompt

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

// compose installs what a host installs. A subsystem never installs cloud.Bridge
// (routes says why): the program's composer owns it — serve.go in production — so
// a test app owes the same install, else every org-scoped op answers 403 for a
// reason no composed program has.
func compose(app *zip.App) { app.Use(cloud.Bridge()) }

func mountApp(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	compose(app)
	if err := Use(app, cloud.Deps{DataDir: t.TempDir()}); err != nil {
		t.Fatalf("Use:  %v", err)
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
		req.Header.Set("X-User-Id", "u_"+org) // validated principal (principal.Acting gates on it)
	}
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("Test %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func TestHTTPGateIsolationAndVersioning(t *testing.T) {
	app := mountApp(t)

	if code, _ := do(t, app, http.MethodGet, "/v1/prompt", "", nil); code != http.StatusForbidden {
		t.Fatalf("no-org list want 403, got %d", code)
	}

	// maxpower creates a prompt (content field is `prompt`, per the FE contract).
	if code, _ := do(t, app, http.MethodPost, "/v1/prompt", "maxpower",
		map[string]any{"name": "greeting", "type": "text", "prompt": "hello", "tags": []string{"a"}}); code != http.StatusCreated {
		t.Fatalf("create want 201, got %d", code)
	}
	// Re-create same name → a new version.
	if code, _ := do(t, app, http.MethodPost, "/v1/prompt", "maxpower",
		map[string]any{"name": "greeting", "type": "text", "prompt": "hello v2"}); code != http.StatusCreated {
		t.Fatalf("re-create want 201, got %d", code)
	}

	// List shape is {data:[PromptMeta]} with versions [2,1].
	code, body := do(t, app, http.MethodGet, "/v1/prompt", "maxpower", nil)
	if code != http.StatusOK {
		t.Fatalf("list want 200, got %d", code)
	}
	var listed struct {
		Data []promptMeta `json:"data"`
	}
	if err := json.Unmarshal(body, &listed); err != nil {
		t.Fatalf("list json: %v (%s)", err, body)
	}
	if len(listed.Data) != 1 || listed.Data[0].Name != "greeting" {
		t.Fatalf("maxpower should see [greeting], got %+v", listed.Data)
	}
	if len(listed.Data[0].Versions) != 2 {
		t.Fatalf("greeting should have 2 versions, got %v", listed.Data[0].Versions)
	}

	// acme sees none; cannot read maxpower's prompt.
	code, body = do(t, app, http.MethodGet, "/v1/prompt", "acme", nil)
	_ = json.Unmarshal(body, &listed)
	if code != http.StatusOK || len(listed.Data) != 0 {
		t.Fatalf("acme must see zero prompts, got %d %+v", code, listed.Data)
	}
	if code, _ := do(t, app, http.MethodGet, "/v1/prompt/greeting", "acme", nil); code != http.StatusNotFound {
		t.Fatalf("acme GET maxpower prompt want 404, got %d", code)
	}

	// metrics is a real per-prompt rollup, not shadowed by :name.
	code, body = do(t, app, http.MethodGet, "/v1/prompt/metrics", "maxpower", nil)
	if code != http.StatusOK || !bytes.Contains(body, []byte("greeting")) {
		t.Fatalf("metrics want 200 with greeting, got %d %s", code, body)
	}
}
