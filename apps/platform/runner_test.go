package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

const testBuildTok = "s3cr3t-build-callback-token"

// runnerApp mounts the platform routes over a ready fake cluster so a valid
// /v1/platform/runner request reaches launchDirectBuild and returns 202.
func runnerApp(t *testing.T) *zip.App {
	t.Helper()
	app, _ := runnerAppStore(t)
	return app
}

// runnerAppStore is runnerApp for the tests that read back what a build RECORDED —
// the org a build was attributed to is a fact in the store, not in the response.
func runnerAppStore(t *testing.T) (*zip.App, *Store) {
	t.Helper()
	store, err := openStore(t.TempDir())
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	s := &cloud.Service[state]{Base: cloud.Base{Log: luxlog.New("test"), Brand: "hanzo"}, State: state{store: store, k8s: fakeK8s(), sitesHost: "hanzo.app"}}
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	compose(app)
	routes(app, s)
	return app, store
}

// postRunnerWith POSTs /v1/platform/runner with an explicit credential set: hdrs are the
// identity headers SanitizeIdentity mints from a signature-verified token, auth is
// the raw Authorization header value. Either, both, or neither — which is the point
// of having one helper: the interesting cases are the ones where a caller presents
// an org identity AND an ambient shared secret at the same time.
func postRunnerWith(t *testing.T, app *zip.App, hdrs map[string]string, auth string, body any) (int, []byte) {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/platform/runner", r)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range hdrs {
		req.Header.Set(k, v)
	}
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("Test POST /v1/platform/runner: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// postRunner POSTs /v1/platform/runner with an optional Bearer token.
func postRunner(t *testing.T, app *zip.App, token string, body any) (int, []byte) {
	t.Helper()
	var auth string
	if token != "" {
		auth = "Bearer " + token
	}
	return postRunnerWith(t, app, nil, auth, body)
}

// postRunnerAs POSTs /v1/platform/runner as a VALIDATED IAM principal: it sets the
// identity headers SanitizeIdentity mints from a signature-verified JWT
// (X-User-Id ⇒ principal.Validated, X-Org-Id ⇒ principal.Org, and optionally
// X-User-IsOrgAdmin / X-User-IsAdmin for the role). No Authorization bearer — this
// exercises the IAM path, not the shared-token path. In production these headers
// are unforgeable (stripped on ingress, re-minted only from validated claims); the
// harness sets them directly exactly as the tenant()-gated tests (doAs) do.
func postRunnerAs(t *testing.T, app *zip.App, user, org string, orgAdmin, superAdmin bool, body any) (int, []byte) {
	t.Helper()
	return postRunnerWith(t, app, identity(user, org, orgAdmin, superAdmin), "", body)
}

// identity is the header set SanitizeIdentity mints for a person: X-User-Id ⇒
// principal.Validated, X-Org-Id ⇒ principal.Org, and the role bits. In production
// these are unforgeable (stripped on ingress, re-minted only from validated
// claims); the harness sets them directly exactly as the tenant()-gated tests do.
func identity(user, org string, orgAdmin, superAdmin bool) map[string]string {
	h := map[string]string{}
	if user != "" {
		h["X-User-Id"] = user
	}
	if org != "" {
		h["X-Org-Id"] = org
	}
	if orgAdmin {
		h["X-User-IsOrgAdmin"] = "true"
	}
	if superAdmin {
		h["X-User-IsAdmin"] = "true"
	}
	return h
}

// appIdentity is the header set SanitizeIdentity mints for an ORGANIZATION'S OWN
// MACHINE IDENTITY — a client_credentials application acting as itself. It carries
// the org (which such a token cannot select: IAM mints it no membership set) and
// the kind, and NEITHER admin bit, because an application holds neither.
func appIdentity(app, org string) map[string]string {
	return map[string]string{"X-User-Id": org + "/" + app, "X-Org-Id": org, "X-User-IsApp": "true"}
}

// No shared token configured AND no validated principal ⇒ fail closed (403).
// (The endpoint is no longer 503-"unavailable" when the shared token is unset,
// because the IAM path is a valid credential; an unauthenticated caller is simply
// refused, never served, and an empty secret can never match an empty bearer.)
func TestRunnerBuild_NoTokenConfigured(t *testing.T) {
	t.Setenv("PLATFORM_BUILD_CALLBACK_TOKEN", "")
	app := runnerApp(t)
	code, _ := postRunner(t, app, "anything", map[string]any{
		"repo": "https://github.com/hanzoai/cloud", "image": "ghcr.io/hanzoai/app:v1"})
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
		"repo": "https://github.com/hanzoai/app", "sha": "00971263b1c4e5f60718293a4b5c6d7e8f90a1b2",
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
		"repo": "https://github.com/luxfi/wallet", "sha": "00971263b1c4e5f60718293a4b5c6d7e8f90a1b2",
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
		"repo": "https://github.com/luxfi/wallet", "sha": "00971263b1c4e5f60718293a4b5c6d7e8f90a1b2",
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
		"repo": "https://github.com/luxfi/wallet", "sha": "00971263b1c4e5f60718293a4b5c6d7e8f90a1b2",
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
		"repo": "https://github.com/hanzoai/cloud", "image": "ghcr.io/hanzoai/app:v1"})
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
		"repo": "https://github.com/hanzoai/cloud", "image": "ghcr.io/hanzoai/app:v1"})
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

// A BUILD BELONGS TO THE ORGANIZATION ITS CREDENTIAL NAMES, and there is no field
// to say otherwise with. A body that carries `organizationId` names nothing: the
// recorded build is attributed to the caller's own validated org, and the request
// is neither refused for it nor honoured on it.
//
// Refusing a foreign value was the earlier shape, and it read as safe because the
// refusal was real. But it kept a caller-stated tenant in the request type, which
// means every later reader has to remember to check it. Deleting the field leaves
// nothing to check and nothing to forget.
func TestRunnerBuild_StatedOrgNamesNothing(t *testing.T) {
	t.Setenv("PLATFORM_BUILD_CALLBACK_TOKEN", "")
	app, store := runnerAppStore(t)
	code, body := postRunnerAs(t, app, "e7d7-uuid", "hanzo", true, false, map[string]any{
		"repo": "https://github.com/hanzoai/app", "image": "ghcr.io/hanzoai/app-web:v1",
		"organizationId": "lux"})
	if code != http.StatusAccepted {
		t.Fatalf("stated organizationId: want 202 (the field names nothing), got %d (%s)", code, body)
	}
	var resp runnerBuildResp
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode resp: %v", err)
	}
	// The attribution the caller could not write: the build row is the caller's org.
	if _, err := store.GetBuild(context.Background(), "hanzo", resp.BuildJobID); err != nil {
		t.Fatalf("build %s not attributed to the credential's org (hanzo): %v", resp.BuildJobID, err)
	}
	if _, err := store.GetBuild(context.Background(), "lux", resp.BuildJobID); err == nil {
		t.Fatal("the body's organizationId became the build's org — a caller named its own tenant")
	}
}

// THE ORGANIZATION'S OWN MACHINE IDENTITY BUILDS FOR ITSELF.
//
// This is the credential CI holds: a client_credentials application token, minted
// against the org's own IAM application, carrying an org it cannot select. It is
// neither a member nor an admin — authz.Claims.OrgAdmin refuses every application
// by construction, because an app is issued for a purpose and not handed an org's
// self-service surface — so asking for the admin bit asked a question no
// non-interactive credential can answer, and the only thing left that reached this
// endpoint was the fabric's shared token, which names no org at all.
func TestRunnerBuild_AppIdentityBuildsItsOwnOrg(t *testing.T) {
	t.Setenv("PLATFORM_BUILD_CALLBACK_TOKEN", "")
	app, store := runnerAppStore(t)
	code, body := postRunnerWith(t, app, appIdentity("hanzo-kms", "hanzo"), "", map[string]any{
		"repo": "https://github.com/hanzoai/app", "sha": "00971263b1c4e5f60718293a4b5c6d7e8f90a1b2",
		"image": "ghcr.io/hanzoai/app-web:00971263b"})
	if code != http.StatusAccepted {
		t.Fatalf("an org's own machine identity building its own namespace: want 202, got %d (%s)", code, body)
	}
	var resp runnerBuildResp
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode resp: %v", err)
	}
	if _, err := store.GetBuild(context.Background(), "hanzo", resp.BuildJobID); err != nil {
		t.Fatalf("build %s not attributed to the credential's org: %v", resp.BuildJobID, err)
	}
}

// And it reaches ITS OWN namespace only. The image is on a registry the fabric
// owns, so the outer allowlist admits it; the org binding is what refuses it, and
// the message says which — otherwise this test would pass on the old code for the
// wrong reason (an app identity used to be refused at the endpoint as "not an admin",
// before any registry decision was reached).
func TestRunnerBuild_AppIdentityCannotCrossBrands(t *testing.T) {
	t.Setenv("PLATFORM_BUILD_CALLBACK_TOKEN", "")
	app := runnerApp(t)
	code, body := postRunnerWith(t, app, appIdentity("hanzo-kms", "hanzo"), "", map[string]any{
		"repo": "https://github.com/luxfi/wallet", "sha": "00971263b1c4e5f60718293a4b5c6d7e8f90a1b2",
		"image": "ghcr.io/luxfi/wallet-web:00971263b"})
	if code != http.StatusForbidden {
		t.Fatalf("hanzo's machine identity → ghcr.io/luxfi: want 403, got %d (%s)", code, body)
	}
	if !bytes.Contains(body, []byte("must match your organization")) {
		t.Fatalf("refused, but not by the org binding: %s", body)
	}
}

// The artifact lane confines an application to its own org's forge owner, exactly
// as the image lane confines it to that org's registry namespace — one rule, two
// outputs.
func TestRunnerBuild_AppIdentityArtifactConfined(t *testing.T) {
	t.Setenv("PLATFORM_BUILD_CALLBACK_TOKEN", "")
	app := runnerApp(t)
	recipe := func(repo string) map[string]any {
		return map[string]any{"repo": repo, "sha": "00971263b1c4e5f60718293a4b5c6d7e8f90a1b2",
			"tag": "v1.2.3", "binaries": []map[string]any{{"name": "cli", "run": "make dist", "out": "dist/*"}}}
	}
	if code, body := postRunnerWith(t, app, appIdentity("hanzo-kms", "hanzo"), "",
		recipe("https://github.com/hanzoai/cli")); code != http.StatusAccepted {
		t.Fatalf("hanzo's machine identity publishing a hanzoai artifact: want 202, got %d (%s)", code, body)
	}
	code, body := postRunnerWith(t, app, appIdentity("hanzo-kms", "hanzo"), "",
		recipe("https://github.com/luxfi/node"))
	if code != http.StatusForbidden || !bytes.Contains(body, []byte("must match your organization")) {
		t.Fatalf("hanzo's machine identity publishing a luxfi artifact: want 403 on the org binding, got %d (%s)", code, body)
	}
}

// AN AMBIENT SHARED SECRET DOES NOT PROMOTE AN ORG IDENTITY.
//
// A caller that names an organization is attributed to that organization and
// confined to it, whether or not the fabric's token also rode along. Reading the
// token FIRST meant the org the caller proved was discarded in favour of a secret
// that proves no org — so a request with both got fabric-wide latitude across every
// brand's registry, and nothing recorded whose build it was.
//
// The two arrive together through cloud's own boundary because they are read from
// different places: the identity comes from whichever credential callerToken
// resolves (X-Authorization, a cookie), while the build token is compared against
// the raw Authorization value — which stripBearer accepts with or without the
// scheme, so `Authorization: <token>` is not a bearer for the first reader and is
// the token for the second.
func TestRunnerBuild_AmbientTokenDoesNotPromoteOrgIdentity(t *testing.T) {
	t.Setenv("PLATFORM_BUILD_CALLBACK_TOKEN", testBuildTok)
	app := runnerApp(t)
	code, body := postRunnerWith(t, app, identity("e7d7-uuid", "hanzo", true, false), testBuildTok,
		map[string]any{"repo": "https://github.com/luxfi/wallet", "image": "ghcr.io/luxfi/wallet-web:v1"})
	if code != http.StatusForbidden {
		t.Fatalf("hanzo identity + ambient fabric token → ghcr.io/luxfi: want 403, got %d (%s)", code, body)
	}
	if !bytes.Contains(body, []byte("must match your organization")) {
		t.Fatalf("refused, but not by the org binding: %s", body)
	}
}

// THE FABRIC TOKEN KEEPS THE LATITUDE IT EXISTS FOR. It names no organization, so
// there is no org to confine it to, and it stays bounded by the owned-registry
// allowlist alone — which is how cloud's own release publishes across brands.
// Pinned so that narrowing the token path is a test failure and not a surprise.
func TestRunnerBuild_FabricTokenSpansOwnedRegistries(t *testing.T) {
	t.Setenv("PLATFORM_BUILD_CALLBACK_TOKEN", testBuildTok)
	app := runnerApp(t)
	// One image per owned namespace, and NONE of them the cloud image: cloud is
	// versioned by its own release lane and refused at this endpoint regardless of
	// who asks, so naming it here would test the exclusion rather than the span.
	for _, image := range []string{"ghcr.io/hanzoai/app:v1", "ghcr.io/luxfi/node:v1", "ghcr.io/zooai/app:v1"} {
		code, body := postRunner(t, app, testBuildTok, map[string]any{
			"repo": "https://github.com/hanzoai/cloud", "image": image})
		if code != http.StatusAccepted {
			t.Fatalf("fabric token → %s: want 202, got %d (%s)", image, code, body)
		}
	}
}

// wrong token ⇒ 403.
func TestRunnerBuild_BadToken(t *testing.T) {
	t.Setenv("PLATFORM_BUILD_CALLBACK_TOKEN", testBuildTok)
	app := runnerApp(t)
	code, _ := postRunner(t, app, "wrong", map[string]any{
		"repo": "https://github.com/hanzoai/cloud", "image": "ghcr.io/hanzoai/app:v1"})
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
		"repo": "https://github.com/hanzoai/cloud", "sha": "main", "image": "ghcr.io/hanzoai/app:v1.2.3"})
	if code != http.StatusAccepted {
		t.Fatalf("launch: want 202, got %d (%s)", code, body)
	}
	var resp runnerBuildResp
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode resp: %v", err)
	}
	if resp.BuildJobID == "" || resp.Image != "ghcr.io/hanzoai/app:v1.2.3" || resp.Status != "queued" {
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
// It stays for the ordinary build path, because git-push-to-deploy runs on it, and
// that path is the whole of what it may do.
func TestSharedTokenBuildsAndOnlyBuilds(t *testing.T) {
	t.Setenv("PLATFORM_BUILD_CALLBACK_TOKEN", "s3kr3t-fabric-token")
	app := runnerApp(t)

	enqueue, body := postRunner(t, app, "s3kr3t-fabric-token", map[string]any{
		"repo": "https://github.com/hanzoai/cloud", "image": "ghcr.io/hanzoai/app:v1"})
	if enqueue == http.StatusForbidden {
		t.Fatalf("the fabric token lost the ordinary build path it exists for: %d (%s)", enqueue, body)
	}
}

// TestRegistryOwnershipIsVerbatim: the registry-namespace lookup is keyed by the
// SAME value it is compared against — the VERBATIM validated IAM owner
// (principal.Org, "only trimmed, NEVER lowercased").
//
// It folded the org through strings.ToLower on ONE side of the comparison while
// the other side stayed verbatim. "acme" and "ACME" are DISTINCT IAM tenants
// (TestMembershipMatchIsByteExact), so a tenant self-serving an org named `Hanzo`
// — whose own RoleOwner makes IsOrgAdmin true INSIDE it — folded onto the `hanzo`
// key and inherited the `hanzoai` namespace: push over another brand's production
// images, ghcr.io/hanzoai/cloud among them — the binary every pod in the fleet
// runs. A fold on one side of an authorization comparison is not a normalization,
// it is a collision, and the collision IS the grant.
func TestRegistryOwnershipIsVerbatim(t *testing.T) {
	// The real owners keep their namespaces, on both lanes.
	for _, tc := range []struct{ org, image, repo string }{
		{"hanzo", "ghcr.io/hanzoai/cloud", "https://github.com/hanzoai/cloud"},
		{"lux", "ghcr.io/luxfi/node", "https://github.com/luxfi/node"},
		{"zoo", "ghcr.io/zooai/app", "https://github.com/zooai/app"},
	} {
		if !imageInOrgRegistry(tc.image, tc.org) {
			t.Errorf("org %q lost its own namespace for %q", tc.org, tc.image)
		}
		if !repoOwnerInOrg(tc.repo, tc.org) {
			t.Errorf("org %q lost its own forge owner for %q", tc.org, tc.repo)
		}
	}
	// A case-variant org is a DIFFERENT tenant and owns nothing here — on either
	// lane, and whatever it is admin of inside itself.
	for _, org := range []string{"Hanzo", "HANZO", "hanzO", "Lux", "ZOO"} {
		if imageInOrgRegistry("ghcr.io/hanzoai/cloud", org) {
			t.Errorf("tenant %q folded onto a brand's REGISTRY namespace — it could "+
				"overwrite that brand's production images and cut a release of the "+
				"binary the fleet runs", org)
		}
		if repoOwnerInOrg("https://github.com/hanzoai/cloud", org) {
			t.Errorf("tenant %q folded onto a brand's FORGE owner", org)
		}
	}
	// Trimming stays — whitespace is not an identity, and it never was.
	if !imageInOrgRegistry("ghcr.io/hanzoai/cloud", "  hanzo  ") {
		t.Error("surrounding whitespace changed the owner")
	}
	// Cross-brand is still refused, which is the check's original job.
	if imageInOrgRegistry("ghcr.io/hanzoai/cloud", "lux") {
		t.Error("lux reached the hanzoai namespace")
	}
}
