package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/apps/template"
)

// The gallery is tested where it lives, on SQLite. What only this host can prove is
// the half it supplies: the identity boundary's answer to who the caller is, and the
// store on BOTH engines the deployment can run on. So the tenancy claim is run
// through this host's real adapters, once per engine.

func mount(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	// What the composer installs at the root of every program: the principal a typed
	// op reads off its context.
	app.Use(cloud.Bridge())
	t.Setenv("CLOUD_DATA_DIR", t.TempDir())
	if err := use(app, cloud.Deps{}); err != nil {
		t.Fatalf("use: %v", err)
	}
	t.Cleanup(func() { _ = template.Shutdown(t.Context()) })
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
		req.Header.Set("X-User-Id", "u_"+org)
	}
	resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func listed(t *testing.T, body []byte) map[string]string {
	t.Helper()
	var out struct {
		Data []template.StarterKit `json:"data"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode: %v (%s)", err, body)
	}
	m := map[string]string{}
	for _, k := range out.Data {
		m[k.Slug] = k.Org
	}
	return m
}

func TestPrivateKitIsOrgOnlyOnEveryEngine(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mount(t)
		kit := map[string]any{"slug": "acme-portal", "title": "Acme Portal"}

		if code, body := do(t, app, http.MethodPost, "/v1/template", "acme", kit); code != http.StatusCreated {
			t.Fatalf("publish want 201, got %d (%s)", code, body)
		}
		if code, _ := do(t, app, http.MethodPost, "/v1/template", "acme", kit); code != http.StatusConflict {
			t.Fatalf("republish want 409, got %d", code)
		}
		if code, _ := do(t, app, http.MethodPost, "/v1/template", "", kit); code != http.StatusUnauthorized {
			t.Fatalf("anonymous publish want 401, got %d", code)
		}

		_, body := do(t, app, http.MethodGet, "/v1/template", "acme", nil)
		if owner, ok := listed(t, body)["acme-portal"]; !ok || owner != "acme" {
			t.Fatalf("acme cannot see its own kit (owner=%q ok=%v)", owner, ok)
		}
		_, body = do(t, app, http.MethodGet, "/v1/template", "globex", nil)
		if _, leaked := listed(t, body)["acme-portal"]; leaked {
			t.Fatal("cross-org leak: acme's private kit is in globex's gallery")
		}
		if code, _ := do(t, app, http.MethodGet, "/v1/template/acme-portal", "globex", nil); code != http.StatusNotFound {
			t.Fatalf("globex get want 404, got %d", code)
		}
		_, body = do(t, app, http.MethodGet, "/v1/template", "", nil)
		if _, leaked := listed(t, body)["acme-portal"]; leaked {
			t.Fatal("public leak: a private kit is in the anonymous gallery")
		}

		if code, _ := do(t, app, http.MethodPut, "/v1/template/acme-portal", "globex", map[string]any{"title": "Hijacked"}); code != http.StatusNotFound {
			t.Fatalf("cross-org edit want 404, got %d", code)
		}
		if code, _ := do(t, app, http.MethodDelete, "/v1/template/acme-portal", "acme", nil); code != http.StatusNoContent {
			t.Fatalf("owner delete want 204, got %d", code)
		}
	})
}
