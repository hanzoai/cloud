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

// A validated IAM org-admin builds off the ONE identity into ITS OWN org's
// registry — no shared token needed (here the server has none configured) ⇒ 202.
// This is the `hanzo build` path. The image namespace (hanzoai) matches the
// caller's org (hanzo), so H1's registry-org binding admits it.
func TestRunnerBuild_IAMAdminLaunches(t *testing.T) {
	t.Setenv("PLATFORM_BUILD_CALLBACK_TOKEN", "")
	app := runnerApp(t)
	code, body := postRunnerAs(t, app, "e7d7-uuid", "hanzo", true, false, map[string]any{
		"repo": "https://github.com/hanzoai/app", "sha": "00971263b",
		"image": "ghcr.io/hanzoai/app-web:00971263b"})
	if code != http.StatusAccepted {
		t.Fatalf("IAM org-admin same-org build: want 202, got %d (%s)", code, body)
	}
	var resp runnerBuildResp
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode resp: %v", err)
	}
	if resp.BuildJobID == "" || resp.Image != "ghcr.io/hanzoai/app-web:00971263b" {
		t.Fatalf("unexpected resp: %+v", resp)
	}
}

// H1 — the exact cross-org supply-chain hole RED flagged, now CLOSED: a hanzo
// org-admin trying to build into ghcr.io/luxfi/* (another brand's registry) ⇒ 403,
// even though the image is an owned registry and the caller is a valid org-admin.
// Identity does not widen the registry boundary beyond the caller's own org.
func TestRunnerBuild_IAMCrossOrgImageRejected(t *testing.T) {
	t.Setenv("PLATFORM_BUILD_CALLBACK_TOKEN", "")
	app := runnerApp(t)
	code, _ := postRunnerAs(t, app, "e7d7-uuid", "hanzo", true, false, map[string]any{
		"repo": "https://github.com/luxfi/wallet", "sha": "00971263b",
		"image": "ghcr.io/luxfi/wallet-web:00971263b"})
	if code != http.StatusForbidden {
		t.Fatalf("IAM cross-org image (hanzo admin → ghcr.io/luxfi): want 403, got %d", code)
	}
}

// A lux org-admin building into ITS OWN org's registry (ghcr.io/luxfi/*) ⇒ 202.
// Proves the binding is per-org, not a hanzo-only allow: every registry brand's
// admin can push its own namespace, and only its own.
func TestRunnerBuild_IAMSameOrgLuxLaunches(t *testing.T) {
	t.Setenv("PLATFORM_BUILD_CALLBACK_TOKEN", "")
	app := runnerApp(t)
	code, body := postRunnerAs(t, app, "lx-uuid", "lux", true, false, map[string]any{
		"repo": "https://github.com/luxfi/wallet", "sha": "00971263b",
		"image": "ghcr.io/luxfi/wallet-web:00971263b"})
	if code != http.StatusAccepted {
		t.Fatalf("IAM lux admin same-org build: want 202, got %d (%s)", code, body)
	}
}

// A platform SuperAdmin (X-User-IsAdmin, the reserved admin org) MAY cross org
// registries ⇒ 202 building ghcr.io/luxfi/* — the one identity permitted to. (In
// production SuperAdmin is disabled via unset CLOUD_ADMIN_ORG; this proves the
// intended cross-org exception is wired for when it is enabled.)
func TestRunnerBuild_SuperAdminCrossOrgLaunches(t *testing.T) {
	t.Setenv("PLATFORM_BUILD_CALLBACK_TOKEN", "")
	app := runnerApp(t)
	code, body := postRunnerAs(t, app, "root-uuid", "hanzo", true, true, map[string]any{
		"repo": "https://github.com/luxfi/wallet", "sha": "00971263b",
		"image": "ghcr.io/luxfi/wallet-web:00971263b"})
	if code != http.StatusAccepted {
		t.Fatalf("SuperAdmin cross-org build: want 202, got %d (%s)", code, body)
	}
}

// An org whose brand owns NO registry namespace (e.g. adnexus) is refused on the
// IAM path for ANY owned registry ⇒ 403 — nobody pushes to a brand they do not own.
func TestRunnerBuild_IAMOrglessRegistryRejected(t *testing.T) {
	t.Setenv("PLATFORM_BUILD_CALLBACK_TOKEN", "")
	app := runnerApp(t)
	code, _ := postRunnerAs(t, app, "ad-uuid", "adnexus", true, false, map[string]any{
		"repo": "https://github.com/hanzoai/app", "image": "ghcr.io/hanzoai/app-web:v1"})
	if code != http.StatusForbidden {
		t.Fatalf("IAM adnexus admin → ghcr.io/hanzoai: want 403 (owns no namespace), got %d", code)
	}
}

// M1 — an `image` carrying a comma injects a BuildKit `--output` exporter attribute
// (name=…,registry.insecure=true). It must be rejected as a malformed ref ⇒ 400,
// BEFORE any registry decision reads it. Uses the machine token so the check is not
// masked by an earlier identity/registry 403.
func TestRunnerBuild_ImageInjectionRejected(t *testing.T) {
	t.Setenv("PLATFORM_BUILD_CALLBACK_TOKEN", testBuildTok)
	app := runnerApp(t)
	for _, bad := range []string{
		"ghcr.io/hanzoai/x,registry.insecure=true",
		"ghcr.io/hanzoai/x name=y",
		"ghcr.io/hanzoai/x\",push=true",
		"ghcr.io/hanzoai/x:tag\nname=evil",
	} {
		code, _ := postRunner(t, app, testBuildTok, map[string]any{
			"repo": "https://github.com/hanzoai/cloud", "image": bad})
		if code != http.StatusBadRequest {
			t.Fatalf("injected image %q: want 400, got %d", bad, code)
		}
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
// The image is the caller's OWN org registry (ghcr.io/hanzoai) so H1's registry
// binding passes and the 403 isolates the organizationId attribution check.
func TestRunnerBuild_IAMForeignOrgRejected(t *testing.T) {
	t.Setenv("PLATFORM_BUILD_CALLBACK_TOKEN", "")
	app := runnerApp(t)
	code, _ := postRunnerAs(t, app, "e7d7-uuid", "hanzo", true, false, map[string]any{
		"repo": "https://github.com/hanzoai/app", "image": "ghcr.io/hanzoai/app-web:v1",
		"organizationId": "lux"})
	if code != http.StatusForbidden {
		t.Fatalf("IAM foreign org: want 403, got %d", code)
	}
}

// RELEASING IS ORG-SCOPED, NOT CROSS-TENANT.
//
// Cutting the platform release takes the SAME authority an ordinary build takes:
// admin of an org that OWNS the registry namespace being published to. It used to
// take platform SUDO, which is a category error — SuperAdmin is the authority to
// act in an org you do NOT belong to, and publishing your own org's artifact is
// not that. The broad scope did not make it safer; it made releasing impossible
// for the engineers who own the artifact.
//
// hanzo owns `hanzoai` (orgRegistryNamespaces), and releaseImage is
// ghcr.io/hanzoai/cloud, so a hanzo org admin may cut it. Both release seams
// answer 500, so an AUTHORIZED request can only fail inside the pipeline (502) —
// reaching that proves the gate let it through, and the stub keeps the case
// hermetic.
func TestRunnerRelease_OwningOrgAdminAuthorized(t *testing.T) {
	t.Setenv("PLATFORM_BUILD_CALLBACK_TOKEN", "")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	defer swapAPIBase(srv.URL)()
	defer swapRegistryBase(srv.URL)()

	app := runnerApp(t)
	code, body := postRunnerAs(t, app, "e7d7-uuid", "hanzo", true, false, map[string]any{
		"repo": "https://github.com/hanzoai/cloud", "release": true,
		"image": "ghcr.io/hanzoai/cloud:v1"})
	if code == http.StatusForbidden {
		t.Fatalf("the owning org's admin was refused its own release: %s", body)
	}
	if code != http.StatusBadGateway {
		t.Fatalf("want the pipeline's 502 on a failing seam, got %d (%s)", code, body)
	}
	releasing.Store(false)
}

// A DIFFERENT brand's admin may not. lux owns `luxfi`, not `hanzoai`, so this is
// the same confinement that stops a lux admin pushing ghcr.io/hanzoai/* on the
// ordinary path — the release gate reuses it rather than inventing a second rule.
func TestRunnerRelease_ForeignOrgAdminRefused(t *testing.T) {
	t.Setenv("PLATFORM_BUILD_CALLBACK_TOKEN", "")
	app := runnerApp(t)
	code, body := postRunnerAs(t, app, "lux-uuid", "lux", true, false, map[string]any{
		"repo": "https://github.com/hanzoai/cloud", "release": true,
		"image": "ghcr.io/hanzoai/cloud:v1"})
	if code != http.StatusForbidden {
		t.Fatalf("a lux admin cut hanzo's release: %d (%s)", code, body)
	}
}

// A plain MEMBER of the owning org may not — owning the namespace is necessary,
// not sufficient. Admin of that org is the other half.
func TestRunnerRelease_PlainMemberRefused(t *testing.T) {
	t.Setenv("PLATFORM_BUILD_CALLBACK_TOKEN", "")
	app := runnerApp(t)
	code, body := postRunnerAs(t, app, "member-uuid", "hanzo", false, false, map[string]any{
		"repo": "https://github.com/hanzoai/cloud", "release": true,
		"image": "ghcr.io/hanzoai/cloud:v1"})
	if code != http.StatusForbidden {
		t.Fatalf("a plain member cut a release: %d (%s)", code, body)
	}
}

// The other side of that gate: a platform SuperAdmin MAY cut a release, on the
// IAM path alone, with no machine token configured. Release reads
// principal.IsSuperAdmin — the same predicate every other privileged surface
// reads (HIP-0519, "the one predicate set") — so an identity trusted with KMS and
// every tenant's data is not refused a release by a second, parallel credential.
//
// The gate is what this pins. Both release seams answer 500, so the request can
// only fail INSIDE the pipeline (502) — which it reaches solely by having been
// authorized. Stubbing them also keeps the case hermetic: an unauthorized request
// makes no outbound call, and an authorized one must not make a real one.
func TestRunnerBuild_SuperAdminReleaseAuthorized(t *testing.T) {
	t.Setenv("PLATFORM_BUILD_CALLBACK_TOKEN", "")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	defer swapAPIBase(srv.URL)()
	defer swapRegistryBase(srv.URL)()

	app := runnerApp(t)
	code, body := postRunnerAs(t, app, "root-uuid", "admin", false, true, map[string]any{
		"repo": "https://github.com/hanzoai/cloud", "release": true,
		"image": "ghcr.io/hanzoai/cloud:v1"})
	if code == http.StatusForbidden {
		t.Fatalf("SuperAdmin release: refused by the gate, want authorized (%s)", body)
	}
	if code != http.StatusBadGateway {
		t.Fatalf("SuperAdmin release: want the pipeline's 502 on a failing seam, got %d (%s)", code, body)
	}
	if releasing.Load() {
		t.Error("a failed release left the in-flight guard set; releases would be wedged forever")
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

// THE SHARED TOKEN MAY ENQUEUE A BUILD; IT MAY NOT CUT A RELEASE.
//
// PLATFORM_BUILD_CALLBACK_TOKEN is a bearer secret with no identity behind it: no
// membership, no expiry, nothing to revoke but a rotation that restarts every
// holder, and nothing in an audit log but "the token". It is a second auth system
// standing beside IAM.
//
// It stays for the ordinary build path, because git-push-to-deploy runs on it and
// removing a credential before its replacement exists breaks that. It is refused
// for the RELEASE — the operation that publishes the image the whole fleet runs —
// because that decision belongs to IAM and to nothing else.
func TestRunnerRelease_SharedTokenCannotRelease(t *testing.T) {
	t.Setenv("PLATFORM_BUILD_CALLBACK_TOKEN", "s3kr3t-fabric-token")
	app := runnerApp(t)

	// The same credential, on the same endpoint, twice — only the `release` flag
	// differs, so the flag is provably what the gate turns on.
	enqueue, body := postRunner(t, app, "s3kr3t-fabric-token", map[string]any{
		"repo": "https://github.com/hanzoai/cloud", "image": "ghcr.io/hanzoai/cloud:v1"})
	if enqueue == http.StatusForbidden {
		t.Fatalf("the fabric token lost the ordinary build path it exists for: %d (%s)", enqueue, body)
	}

	release, body := postRunner(t, app, "s3kr3t-fabric-token", map[string]any{
		"repo": "https://github.com/hanzoai/cloud", "release": true,
		"image": "ghcr.io/hanzoai/cloud:v1"})
	if release != http.StatusForbidden {
		t.Fatalf("a shared secret cut a release: %d (%s) — IAM is the only authority for it", release, body)
	}
}

// A malformed repo is refused BEFORE the 202, so a caller is never told a
// release is in flight that never launched. "cloud" parses as a URL with no
// scheme and no host, which is how a release answered 202 with an image tag and
// started nothing.
func TestRunnerRelease_RepoMustBeACloneURL(t *testing.T) {
	t.Setenv("PLATFORM_BUILD_CALLBACK_TOKEN", "")
	app := runnerApp(t)
	code, body := postRunnerAs(t, app, "e7d7-uuid", "hanzo", true, true, map[string]any{
		"repo": "cloud", "release": true})
	if code != http.StatusBadRequest {
		t.Fatalf("bare repo name: want 400, got %d (%s)", code, body)
	}
	releasing.Store(false)
}
