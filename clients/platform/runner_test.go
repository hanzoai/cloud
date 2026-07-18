package platform

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/hanzoai/cloud"
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
	s := &cloud.Service[state]{Base: cloud.Base{Log: luxlog.New("test"), Brand: "hanzo"}, State: state{store: store, k8s: fakeK8s(), sitesHost: "hanzo.app"}}
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	routes(app, s)
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

// postRunnerAs POSTs /v1/runner as a VALIDATED IAM principal: it sets the
// identity headers SanitizeIdentity mints from a signature-verified JWT
// (X-User-Id ⇒ principal.Validated, X-Org-Id ⇒ principal.Org, and optionally
// X-User-IsOrgAdmin / X-User-IsAdmin for the role). No Authorization bearer — this
// exercises the IAM path, not the shared-token path. In production these headers
// are unforgeable (stripped on ingress, re-minted only from validated claims); the
// harness sets them directly exactly as the tenant()-gated tests (doAs) do.
func postRunnerAs(t *testing.T, app *zip.App, user, org string, orgAdmin, superAdmin bool, body any) (int, []byte) {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/runner", r)
	req.Header.Set("Content-Type", "application/json")
	if user != "" {
		req.Header.Set("X-User-Id", user)
	}
	if org != "" {
		req.Header.Set("X-Org-Id", org)
	}
	if orgAdmin {
		req.Header.Set("X-User-IsOrgAdmin", "true")
	}
	if superAdmin {
		req.Header.Set("X-User-IsAdmin", "true")
	}
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test POST /v1/runner (IAM): %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// No shared token configured AND no validated principal ⇒ fail closed (403).
// (The endpoint is no longer 503-"unavailable" when the shared token is unset,
// because the IAM path is a valid credential; an unauthenticated caller is simply
// refused, never served, and an empty secret can never match an empty bearer.)
func TestRunnerBuild_NoTokenConfigured(t *testing.T) {
	t.Setenv("PLATFORM_BUILD_CALLBACK_TOKEN", "")
	app := runnerApp(t)
	code, _ := postRunner(t, app, "anything", map[string]any{
		"repo": "https://github.com/hanzoai/cloud", "image": "ghcr.io/hanzoai/cloud:v1"})
	if code != http.StatusForbidden {
		t.Fatalf("no token configured, no identity: want 403, got %d", code)
	}
}

// A validated IAM org-admin builds off the ONE identity — no shared token needed
// (here the server has none configured) ⇒ 202. This is the `hanzo build` path.
func TestRunnerBuild_IAMAdminLaunches(t *testing.T) {
	t.Setenv("PLATFORM_BUILD_CALLBACK_TOKEN", "")
	app := runnerApp(t)
	code, body := postRunnerAs(t, app, "e7d7-uuid", "hanzo", true, false, map[string]any{
		"repo": "https://github.com/luxfi/wallet", "sha": "00971263b",
		"image": "ghcr.io/luxfi/wallet-web:00971263b"})
	if code != http.StatusAccepted {
		t.Fatalf("IAM org-admin build: want 202, got %d (%s)", code, body)
	}
	var resp runnerBuildResp
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode resp: %v", err)
	}
	if resp.BuildJobID == "" || resp.Image != "ghcr.io/luxfi/wallet-web:00971263b" {
		t.Fatalf("unexpected resp: %+v", resp)
	}
}

// A validated IAM principal who is NOT an admin (plain member) ⇒ 403. A login is
// necessary but not sufficient; the build is privileged and requires the admin bit.
func TestRunnerBuild_IAMNonAdminRejected(t *testing.T) {
	t.Setenv("PLATFORM_BUILD_CALLBACK_TOKEN", "")
	app := runnerApp(t)
	code, _ := postRunnerAs(t, app, "member-uuid", "hanzo", false, false, map[string]any{
		"repo": "https://github.com/hanzoai/cloud", "image": "ghcr.io/hanzoai/cloud:v1"})
	if code != http.StatusForbidden {
		t.Fatalf("IAM non-admin: want 403, got %d", code)
	}
}

// A forged identity — X-Org-Id + admin headers but NO X-User-Id (the off-gateway
// bearer-less forge) ⇒ 403. principal.Validated gates on X-User-Id, which only the
// identity boundary sets from a verified credential.
func TestRunnerBuild_IAMForgedNoUserRejected(t *testing.T) {
	t.Setenv("PLATFORM_BUILD_CALLBACK_TOKEN", "")
	app := runnerApp(t)
	code, _ := postRunnerAs(t, app, "", "hanzo", true, true, map[string]any{
		"repo": "https://github.com/hanzoai/cloud", "image": "ghcr.io/hanzoai/cloud:v1"})
	if code != http.StatusForbidden {
		t.Fatalf("forged (no X-User-Id): want 403, got %d", code)
	}
}

// The owned-registry allowlist still bounds the IAM path: an admin cannot push to
// a foreign registry ⇒ 403 (identity does not widen the image boundary).
func TestRunnerBuild_IAMAdminDisallowedImage(t *testing.T) {
	t.Setenv("PLATFORM_BUILD_CALLBACK_TOKEN", "")
	app := runnerApp(t)
	code, _ := postRunnerAs(t, app, "e7d7-uuid", "hanzo", true, false, map[string]any{
		"repo": "https://github.com/hanzoai/cloud", "image": "docker.io/evil/x:latest"})
	if code != http.StatusForbidden {
		t.Fatalf("IAM admin disallowed image: want 403, got %d", code)
	}
}

// A non-SuperAdmin org-admin naming a FOREIGN organizationId ⇒ 403 (a build may be
// attributed only to the caller's own org unless they are a platform SuperAdmin).
func TestRunnerBuild_IAMForeignOrgRejected(t *testing.T) {
	t.Setenv("PLATFORM_BUILD_CALLBACK_TOKEN", "")
	app := runnerApp(t)
	code, _ := postRunnerAs(t, app, "e7d7-uuid", "hanzo", true, false, map[string]any{
		"repo": "https://github.com/luxfi/wallet", "image": "ghcr.io/luxfi/wallet-web:v1",
		"organizationId": "lux"})
	if code != http.StatusForbidden {
		t.Fatalf("IAM foreign org: want 403, got %d", code)
	}
}

// Release self-publish is reserved to the machine token: an IAM admin setting
// release:true ⇒ 403, even though they may enqueue ordinary builds.
func TestRunnerBuild_IAMReleaseRejected(t *testing.T) {
	t.Setenv("PLATFORM_BUILD_CALLBACK_TOKEN", "")
	app := runnerApp(t)
	code, _ := postRunnerAs(t, app, "e7d7-uuid", "hanzo", true, true, map[string]any{
		"repo": "https://github.com/hanzoai/cloud", "release": true,
		"image": "ghcr.io/hanzoai/cloud:v1"})
	if code != http.StatusForbidden {
		t.Fatalf("IAM release: want 403, got %d", code)
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
