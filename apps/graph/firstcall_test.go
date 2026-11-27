package graph

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/sdk"
	"github.com/zap-proto/zip"
)

// TestFirstCallWithoutAKey is the request a customer makes before they have a
// key: the documented assert and resolve, with no Authorization header, rendered
// by the error handler production installs. It is 401 — nobody is identified —
// with the scheme and its resource metadata on WWW-Authenticate, a detail naming the header to send, and
// keysUrl naming the page on the caller's own brand that mints a key.
func TestFirstCallWithoutAKey(t *testing.T) {
	app := zip.New(zip.Config{DisableStartupMessage: true, ErrorHandler: cloud.ErrorHandler})
	app.Use(cloud.Bridge())
	t.Setenv("CLOUD_DATA_DIR", t.TempDir())
	if err := Use(app, cloud.Deps{}); err != nil {
		t.Fatalf("Use: %v", err)
	}
	t.Cleanup(func() { _ = Shutdown() })

	for _, tc := range []struct{ host, path, body, keys string }{
		{"api.hanzo.ai", "/v1/graph", `{"assertions":[{"entity":"acme","relation":"owner","value":"ada","at":"2026-01-01T00:00:00Z","source":"quickstart"}]}`, "https://platform.hanzo.ai/api-keys"},
		{"api.hanzo.ai", "/v1/graph/resolve", `{"entity":"acme","relation":"owner"}`, "https://platform.hanzo.ai/api-keys"},
		{"api.lux.cloud", "/v1/graph/resolve", `{"entity":"acme","relation":"owner"}`, "https://console.lux.cloud/api-keys"},
	} {
		req, err := http.NewRequest(http.MethodPost, "http://"+tc.host+tc.path, strings.NewReader(tc.body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
		if err != nil {
			t.Fatalf("POST %s%s: %v", tc.host, tc.path, err)
		}
		var problem struct {
			Detail string `json:"detail"`
			Keys   string `json:"keysUrl"`
		}
		err = json.NewDecoder(resp.Body).Decode(&problem)
		_ = resp.Body.Close()
		if err != nil {
			t.Fatalf("POST %s%s: body is not JSON: %v", tc.host, tc.path, err)
		}
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("POST %s%s with no key: status %d, want 401", tc.host, tc.path, resp.StatusCode)
		}
		// RFC 9728 §5.1: the challenge names the protected-resource metadata on
		// the host the request was sent to.
		challenge := `Bearer resource_metadata="https://` + tc.host + `/.well-known/oauth-protected-resource"`
		if got := resp.Header.Get("WWW-Authenticate"); got != challenge {
			t.Errorf("POST %s%s with no key: WWW-Authenticate %q, want %q", tc.host, tc.path, got, challenge)
		}
		if !strings.Contains(problem.Detail, "Authorization: Bearer") {
			t.Errorf("POST %s%s with no key: detail %q does not name the header to send", tc.host, tc.path, problem.Detail)
		}
		if problem.Keys != tc.keys {
			t.Errorf("POST %s%s with no key: keysUrl %q, want %q", tc.host, tc.path, problem.Keys, tc.keys)
		}
	}
}
