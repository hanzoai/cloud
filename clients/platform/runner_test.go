package platform

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

const testBuildTok = "s3cr3t-build-callback-token"

// runnerApp mounts the platform routes over a ready fake cluster so a valid
// /v1/runner request reaches launchDirectBuild and returns 202.
func runnerApp(t *testing.T) *zip.App {
	t.Helper()
	store, err := openStore(filepath.Join(t.TempDir(), "platform.db"))
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	s := &svc{store: store, k8s: fakeK8s(), log: luxlog.New("test"), brand: "hanzo", sitesHost: "hanzo.app"}
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	s.routes(app)
	return app
}

// postRunner POSTs /v1/runner with an optional Bearer token.
func postRunner(t *testing.T, app *zip.App, token string, body any) (int, []byte) {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/runner", r)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test POST /v1/runner: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// unset token ⇒ fail closed (503), never an open build endpoint.
func TestRunnerBuild_NoTokenConfigured(t *testing.T) {
	t.Setenv("PLATFORM_BUILD_CALLBACK_TOKEN", "")
	app := runnerApp(t)
	code, _ := postRunner(t, app, "anything", map[string]any{
		"repo": "https://github.com/hanzoai/cloud", "image": "ghcr.io/hanzoai/cloud:v1"})
	if code != http.StatusServiceUnavailable {
		t.Fatalf("no token configured: want 503, got %d", code)
	}
}

// wrong token ⇒ 403.
func TestRunnerBuild_BadToken(t *testing.T) {
	t.Setenv("PLATFORM_BUILD_CALLBACK_TOKEN", testBuildTok)
	app := runnerApp(t)
	code, _ := postRunner(t, app, "wrong", map[string]any{
		"repo": "https://github.com/hanzoai/cloud", "image": "ghcr.io/hanzoai/cloud:v1"})
	if code != http.StatusForbidden {
		t.Fatalf("bad token: want 403, got %d", code)
	}
}

// repo/image required ⇒ 400.
func TestRunnerBuild_MissingFields(t *testing.T) {
	t.Setenv("PLATFORM_BUILD_CALLBACK_TOKEN", testBuildTok)
	app := runnerApp(t)
	code, _ := postRunner(t, app, testBuildTok, map[string]any{"repo": "https://github.com/hanzoai/cloud"})
	if code != http.StatusBadRequest {
		t.Fatalf("missing image: want 400, got %d", code)
	}
}

// image outside the owned registries ⇒ 403 (a leaked token can't push anywhere).
func TestRunnerBuild_DisallowedImage(t *testing.T) {
	t.Setenv("PLATFORM_BUILD_CALLBACK_TOKEN", testBuildTok)
	app := runnerApp(t)
	code, _ := postRunner(t, app, testBuildTok, map[string]any{
		"repo": "https://github.com/hanzoai/cloud", "image": "docker.io/evil/x:latest"})
	if code != http.StatusForbidden {
		t.Fatalf("disallowed image: want 403, got %d", code)
	}
}

// valid token + repo + owned image ⇒ 202 with a build job id.
func TestRunnerBuild_Launches(t *testing.T) {
	t.Setenv("PLATFORM_BUILD_CALLBACK_TOKEN", testBuildTok)
	app := runnerApp(t)
	code, body := postRunner(t, app, testBuildTok, map[string]any{
		"repo": "https://github.com/hanzoai/cloud", "sha": "main", "image": "ghcr.io/hanzoai/cloud:v1.2.3"})
	if code != http.StatusAccepted {
		t.Fatalf("launch: want 202, got %d (%s)", code, body)
	}
	var resp runnerBuildResp
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode resp: %v", err)
	}
	if resp.BuildJobID == "" || resp.Image != "ghcr.io/hanzoai/cloud:v1.2.3" || resp.Status != "queued" {
		t.Fatalf("unexpected resp: %+v", resp)
	}
}
