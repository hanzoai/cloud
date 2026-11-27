package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// runnerApp mounts the platform routes over a ready fake cluster so a valid
// /v1/build request reaches launchDirectBuild and returns 202.
func runnerApp(t *testing.T) *zip.App {
	t.Helper()
	app, _ := runnerAppStore(t)
	return app
}

// runnerAppStore is runnerApp for the tests that read back what a build RECORDED —
// the org a build was attributed to is a fact in the store, not in the response.
func runnerAppStore(t *testing.T) (*zip.App, *Store) {
	t.Helper()
	return runnerAppOn(t, fakeK8s())
}

// runnerAppOn is runnerAppStore over a cluster the test holds, for the tests that
// read back the Jobs a build created.
func runnerAppOn(t *testing.T, k *k8sClient) (*zip.App, *Store) {
	t.Helper()
	store, err := openStore(t.TempDir(), testMaster, testKMS)
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	s := &cloud.Service[state]{Log: luxlog.New("test"), Brand: "hanzo", State: state{store: store, k8s: k, sitesHost: "hanzo.app"}}
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	compose(app)
	routes(app, s)
	return app, store
}

// postRunnerWith POSTs /v1/build with an explicit credential set: hdrs are the
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
	req := httptest.NewRequest(http.MethodPost, "/v1/build", r)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range hdrs {
		req.Header.Set(k, v)
	}
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("Test POST /v1/build: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// postRunner POSTs /v1/build as a platform SuperAdmin acting in hanzo, so it
// reaches hanzo's namespaces and no other brand's.
func postRunner(t *testing.T, app *zip.App, body any) (int, []byte) {
	t.Helper()
	return postRunnerWith(t, app, identity("platform", "hanzo", false, true), "", body)
}

// postRunnerAs POSTs /v1/build as a VALIDATED IAM principal: it sets the
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

// No validated principal ⇒ fail closed (403). There is no shared token to present
// instead: an unauthenticated caller is simply refused, never served.
func TestRunnerBuild_NoIdentity(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := runnerApp(t)
		code, _ := postRunnerWith(t, app, nil, "", map[string]any{
			"repo": "https://github.com/hanzoai/cloud", "image": "ghcr.io/hanzoai/app:v1"})
		if code != http.StatusForbidden {
			t.Fatalf("no identity: want 403, got %d", code)
		}
	})
}

// A validated IAM org-admin builds off the ONE identity into ITS OWN org's
// registry — no shared token needed (here the server has none configured) ⇒ 202.
// This is the `hanzo build` path. The image namespace (hanzoai) matches the
// caller's org (hanzo), so H1's registry-org binding admits it.
func TestRunnerBuild_IAMAdminLaunches(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
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
	})
}

// H1 — the exact cross-org supply-chain hole RED flagged, now CLOSED: a hanzo
// org-admin trying to build into ghcr.io/luxfi/* (another brand's registry) ⇒ 403,
// even though the image is an owned registry and the caller is a valid org-admin.
// Identity does not widen the registry boundary beyond the caller's own org.
func TestRunnerBuild_IAMCrossOrgImageRejected(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := runnerApp(t)
		code, _ := postRunnerAs(t, app, "e7d7-uuid", "hanzo", true, false, map[string]any{
			"repo": "https://github.com/luxfi/wallet", "sha": "00971263b1c4e5f60718293a4b5c6d7e8f90a1b2",
			"image": "ghcr.io/luxfi/wallet-web:00971263b"})
		if code != http.StatusForbidden {
			t.Fatalf("IAM cross-org image (hanzo admin → ghcr.io/luxfi): want 403, got %d", code)
		}
	})
}

// Ad Nexus owns ghcr.io/adnexustech, so its own admin builds there and a hanzo
// admin does not. The exchange's images are built by the same door as every
// other brand's, which is what keeps there from being a second way to build.
func TestRunnerBuild_IAMAdNexusOwnsItsRegistry(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := runnerApp(t)
		code, body := postRunnerAs(t, app, "ad-uuid", "adnexus", true, false, map[string]any{
			"repo": "https://github.com/adnexustech/universe", "sha": "00971263b1c4e5f60718293a4b5c6d7e8f90a1b2",
			"image": "ghcr.io/adnexustech/adx:00971263b"})
		if code != http.StatusAccepted {
			t.Fatalf("adnexus admin → ghcr.io/adnexustech: want 202, got %d (%s)", code, body)
		}

		code, _ = postRunnerAs(t, app, "e7d7-uuid", "hanzo", true, false, map[string]any{
			"repo": "https://github.com/adnexustech/universe", "sha": "00971263b1c4e5f60718293a4b5c6d7e8f90a1b2",
			"image": "ghcr.io/adnexustech/adx:00971263b"})
		if code != http.StatusForbidden {
			t.Fatalf("hanzo admin → ghcr.io/adnexustech: want 403, got %d", code)
		}
	})
}

// A lux org-admin building into ITS OWN org's registry (ghcr.io/luxfi/*) ⇒ 202.
// Proves the binding is per-org, not a hanzo-only allow: every registry brand's
// admin can push its own namespace, and only its own.
func TestRunnerBuild_IAMSameOrgLuxLaunches(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := runnerApp(t)
		code, body := postRunnerAs(t, app, "lx-uuid", "lux", true, false, map[string]any{
			"repo": "https://github.com/luxfi/wallet", "sha": "00971263b1c4e5f60718293a4b5c6d7e8f90a1b2",
			"image": "ghcr.io/luxfi/wallet-web:00971263b"})
		if code != http.StatusAccepted {
			t.Fatalf("IAM lux admin same-org build: want 202, got %d (%s)", code, body)
		}
	})
}

// A platform SuperAdmin is confined like every other caller: acting in hanzo it
// cannot build ghcr.io/luxfi/*, and acting in lux — the org it selected, which
// sudo may select — it can, and the build is lux's.
func TestRunnerBuild_SuperAdminBuildsOnlyWhereItActs(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, store := runnerAppStore(t)
		req := map[string]any{
			"repo": "https://github.com/luxfi/wallet", "sha": "00971263b1c4e5f60718293a4b5c6d7e8f90a1b2",
			"image": "ghcr.io/luxfi/wallet-web:00971263b"}
		code, body := postRunnerAs(t, app, "root-uuid", "hanzo", true, true, req)
		if code != http.StatusForbidden || !bytes.Contains(body, []byte("must match your organization")) {
			t.Fatalf("SuperAdmin acting in hanzo → ghcr.io/luxfi: want 403 on the org binding, got %d (%s)", code, body)
		}
		code, body = postRunnerAs(t, app, "root-uuid", "lux", false, true, req)
		if code != http.StatusAccepted {
			t.Fatalf("SuperAdmin acting in lux → ghcr.io/luxfi: want 202, got %d (%s)", code, body)
		}
		var resp runnerBuildResp
		if err := json.Unmarshal(body, &resp); err != nil {
			t.Fatalf("decode resp: %v", err)
		}
		if _, err := store.GetBuild(context.Background(), "lux", resp.BuildJobID); err != nil {
			t.Fatalf("build %s not attributed to the org the SuperAdmin acted in: %v", resp.BuildJobID, err)
		}
	})
}

// An org whose brand owns NO registry namespace (e.g. adnexus) is refused on the
// IAM path for ANY owned registry ⇒ 403 — nobody pushes to a brand they do not own.
func TestRunnerBuild_IAMOrglessRegistryRejected(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := runnerApp(t)
		code, _ := postRunnerAs(t, app, "ad-uuid", "adnexus", true, false, map[string]any{
			"repo": "https://github.com/hanzoai/app", "image": "ghcr.io/hanzoai/app-web:v1"})
		if code != http.StatusForbidden {
			t.Fatalf("IAM adnexus admin → ghcr.io/hanzoai: want 403 (owns no namespace), got %d", code)
		}
	})
}

// M1 — an `image` carrying a comma injects a BuildKit `--output` exporter attribute
// (name=…,registry.insecure=true). It must be rejected as a malformed ref ⇒ 400,
// BEFORE any registry decision reads it. Builds as the platform so the check is not
// masked by an earlier identity/registry 403.
func TestRunnerBuild_ImageInjectionRejected(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := runnerApp(t)
		for _, bad := range []string{
			"ghcr.io/hanzoai/x,registry.insecure=true",
			"ghcr.io/hanzoai/x name=y",
			"ghcr.io/hanzoai/x\",push=true",
			"ghcr.io/hanzoai/x:tag\nname=evil",
		} {
			code, _ := postRunner(t, app, map[string]any{
				"repo": "https://github.com/hanzoai/cloud", "image": bad})
			if code != http.StatusBadRequest {
				t.Fatalf("injected image %q: want 400, got %d", bad, code)
			}
		}
	})
}

// A validated IAM principal who is NOT an admin (plain member) ⇒ 403. A login is
// necessary but not sufficient; the build is privileged and requires the admin bit.
func TestRunnerBuild_IAMNonAdminRejected(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := runnerApp(t)
		code, _ := postRunnerAs(t, app, "member-uuid", "hanzo", false, false, map[string]any{
			"repo": "https://github.com/hanzoai/cloud", "image": "ghcr.io/hanzoai/app:v1"})
		if code != http.StatusForbidden {
			t.Fatalf("IAM non-admin: want 403, got %d", code)
		}
	})
}

// A forged identity — X-Org-Id + admin headers but NO X-User-Id (the off-gateway
// bearer-less forge) ⇒ 403. principal.Validated gates on X-User-Id, which only the
// identity boundary sets from a verified credential.
func TestRunnerBuild_IAMForgedNoUserRejected(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := runnerApp(t)
		code, _ := postRunnerAs(t, app, "", "hanzo", true, true, map[string]any{
			"repo": "https://github.com/hanzoai/cloud", "image": "ghcr.io/hanzoai/app:v1"})
		if code != http.StatusForbidden {
			t.Fatalf("forged (no X-User-Id): want 403, got %d", code)
		}
	})
}

// The owned-registry allowlist still bounds the IAM path: an admin cannot push to
// a foreign registry ⇒ 403 (identity does not widen the image boundary).
func TestRunnerBuild_IAMAdminDisallowedImage(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := runnerApp(t)
		code, _ := postRunnerAs(t, app, "e7d7-uuid", "hanzo", true, false, map[string]any{
			"repo": "https://github.com/hanzoai/cloud", "image": "docker.io/evil/x:latest"})
		if code != http.StatusForbidden {
			t.Fatalf("IAM admin disallowed image: want 403, got %d", code)
		}
	})
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
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
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
	})
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
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
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
	})
}

// And it reaches ITS OWN namespace only. The image is on a registry the fabric
// owns, so the outer allowlist admits it; the org binding is what refuses it, and
// the message says which — otherwise this test would pass on the old code for the
// wrong reason (an app identity used to be refused at the endpoint as "not an admin",
// before any registry decision was reached).
func TestRunnerBuild_AppIdentityCannotCrossBrands(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
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
	})
}

// The artifact lane confines an application to its own org's forge owner, exactly
// as the image lane confines it to that org's registry namespace — one rule, two
// outputs.
func TestRunnerBuild_AppIdentityArtifactConfined(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
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
	})
}

// NO IDENTITY SPANS THE BRANDS. The platform's own SuperAdmin, acting in hanzo,
// publishes hanzo's namespaces and is refused every other brand's, so no single
// credential — however privileged — is a push credential for all three
// registries. Pinned so that widening it again is a test failure.
func TestRunnerBuild_SuperAdminConfinedToItsOrg(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := runnerApp(t)
		// None of them the cloud image: cloud is versioned by its own release lane and
		// refused here regardless of who asks.
		for image, want := range map[string]int{
			"ghcr.io/hanzoai/app:v1":   http.StatusAccepted,
			"ghcr.io/hanzo-inc/app:v1": http.StatusAccepted,
			"ghcr.io/luxfi/node:v1":    http.StatusForbidden,
			"ghcr.io/lux-cpp/node:v1":  http.StatusForbidden,
			"ghcr.io/zooai/app:v1":     http.StatusForbidden,
		} {
			code, body := postRunner(t, app, map[string]any{
				"repo": "https://github.com/hanzoai/cloud", "image": image})
			if code != want {
				t.Fatalf("platform acting in hanzo → %s: want %d, got %d (%s)", image, want, code, body)
			}
		}
	})
}

// EACH ORG'S CI BUILDS AS ITS OWN <org>-ci APPLICATION, and that identity reaches
// exactly the namespaces orgRegistryNamespaces gives its org. hanzo-ci, lux-ci and
// zoo-ci are client_credentials applications of hanzo, lux and zoo; none of them
// reaches a namespace of another brand.
func TestRunnerBuild_EachOrgsCIBuildsOnlyItsOwn(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := runnerApp(t)
		for _, tc := range []struct {
			org          string
			owned, other []string
		}{
			{"hanzo", []string{"ghcr.io/hanzoai/app:v1", "ghcr.io/hanzo-inc/app:v1"}, []string{"ghcr.io/luxfi/node:v1", "ghcr.io/zooai/node:v1"}},
			{"lux", []string{"ghcr.io/luxfi/node:v1", "ghcr.io/lux-cpp/node:v1", "ghcr.io/lux-gpu/kernels:v1"}, []string{"ghcr.io/hanzoai/app:v1", "ghcr.io/zooai/node:v1"}},
			{"zoo", []string{"ghcr.io/zooai/node:v1"}, []string{"ghcr.io/hanzoai/app:v1", "ghcr.io/luxfi/node:v1", "ghcr.io/lux-cpp/node:v1"}},
		} {
			who := appIdentity(tc.org+"-ci", tc.org)
			for _, image := range tc.owned {
				if code, body := postRunnerWith(t, app, who, "", map[string]any{
					"repo": "https://github.com/hanzoai/app", "image": image, "platforms": []string{"linux/amd64"}}); code != http.StatusAccepted {
					t.Errorf("%s-ci → %s: want 202, got %d (%s)", tc.org, image, code, body)
				}
			}
			for _, image := range tc.other {
				if code, body := postRunnerWith(t, app, who, "", map[string]any{
					"repo": "https://github.com/hanzoai/app", "image": image}); code != http.StatusForbidden {
					t.Errorf("%s-ci → %s: want 403, got %d (%s)", tc.org, image, code, body)
				}
			}
		}
	})
}

// THE RESERVED ORG OWNS NO NAMESPACE. An identity that resolves to `admin` —
// an application of the reserved org, or a SuperAdmin at home there that selected
// no brand — publishes nothing at all, so a build identity in the reserved org is
// refused rather than read as permission to push everywhere.
func TestRunnerBuild_ReservedOrgPublishesNothing(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := runnerApp(t)
		for _, who := range []map[string]string{appIdentity("admin-ci", "admin"), identity("provisioner", "admin", false, true)} {
			for _, image := range []string{"ghcr.io/hanzoai/app:v1", "ghcr.io/hanzo-inc/app:v1", "ghcr.io/luxfi/node:v1",
				"ghcr.io/lux-cpp/node:v1", "ghcr.io/lux-gpu/kernels:v1", "ghcr.io/zooai/node:v1", "ghcr.io/adnexustech/adx:v1"} {
				code, body := postRunnerWith(t, app, who, "", map[string]any{"repo": "https://github.com/hanzoai/app", "image": image})
				if code != http.StatusForbidden {
					t.Errorf("%s → %s: want 403, got %d (%s)", who["X-User-Id"], image, code, body)
				}
			}
		}
	})
}

// publishesFrom is luxfi/node, zooai/node and lux-cpp/node as GitHub answers for
// them: main is the default branch, v1.0.0 a tag, and trustedSHA a commit main
// contains. Everything else — a pull request's ref above all — is not a ref any
// of them publishes from.
const trustedSHA = "00971263b1c4e5f60718293a4b5c6d7e8f90a1b2"

func publishesFrom(_, _, ref string) string {
	switch ref {
	case "main":
		return "default"
	case "v1.0.0":
		return "tag"
	case trustedSHA:
		return "commit"
	}
	return ""
}

// githubReadOf reads what a build Job was handed: whether GH_READ_TOKEN is
// declared to the solve and whether it is in the env. It fails the test if the
// token reaches argv as anything but a secret id, or comes from anywhere but the
// gh-read Secret.
func githubReadOf(t *testing.T, k *k8sClient, name string) (declared, env bool) {
	t.Helper()
	job, err := k.dyn.Resource(jobsGVR).Namespace(k.buildNS).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Job %s: %v", name, err)
	}
	c := podSpec(job.Object)["containers"].([]any)[0].(map[string]any)
	for _, a := range c["command"].([]any) {
		switch a := a.(string); {
		case a == "id=GH_READ_TOKEN,env=GH_READ_TOKEN":
			declared = true
		case strings.Contains(a, "GH_READ_TOKEN"):
			t.Errorf("%s: GH_READ_TOKEN reached argv as something other than a secret id: %q", name, a)
		}
	}
	for _, e := range c["env"].([]any) {
		e := e.(map[string]any)
		if e["name"] != "GH_READ_TOKEN" {
			continue
		}
		env = true
		ref, _ := e["valueFrom"].(map[string]any)["secretKeyRef"].(map[string]any)
		if e["value"] != nil || ref["name"] != "gh-read" || ref["key"] != "gh_token" || ref["optional"] != true {
			t.Fatalf("%s: GH_READ_TOKEN must come from Secret gh-read/gh_token, optional; got %v", name, e)
		}
	}
	return declared, env
}

// GH_READ_TOKEN is bound to (org, github.com repo, trusted ref), deny by
// default. zooai/node built by zoo at a ref it publishes from gets the secret as
// a BuildKit secret mount; a pull request's ref, a branch nobody protects, the
// same owner/name on another host, a zoo build of any OTHER repo, a lux repo not
// on the list, and every hanzo build get neither the mount nor the env. The org
// is the credential's, the repo and ref are what the build clones, and the value
// reaches the pod only by secretKeyRef — never argv, never a build-arg.
func TestRunnerBuild_GitHubReadIsBoundToOrgRepoAndRef(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		for _, tc := range []struct {
			name, org, repo, ref, image string
			want                        bool
		}{
			{"zoo builds zooai/node at main", "zoo", "https://github.com/zooai/node", "main", "ghcr.io/zooai/node:v1", true},
			{"lux builds luxfi/node at a commit main holds", "lux", "https://github.com/luxfi/node", trustedSHA, "ghcr.io/luxfi/node:v1", true},
			{"lux builds lux-cpp/node at a tag", "lux", "https://github.com/lux-cpp/node", "v1.0.0", "ghcr.io/lux-cpp/node:v1", true},

			{"a pull request's ref", "lux", "https://github.com/luxfi/node", "refs/pull/7/head", "ghcr.io/luxfi/node:v1", false},
			{"a pull request's ref, spelled bare", "lux", "https://github.com/luxfi/node", "pull/7/head", "ghcr.io/luxfi/node:v1", false},
			{"a branch nobody protects", "zoo", "https://github.com/zooai/node", "feature", "ghcr.io/zooai/node:v1", false},
			{"a commit only a fork holds", "zoo", "https://github.com/zooai/node", "3333333333333333333333333333333333333333", "ghcr.io/zooai/node:v1", false},
			{"the same owner/name on another host", "lux", "https://gitlab.com/luxfi/node", "main", "ghcr.io/luxfi/node:v1", false},
			{"a subgroup path ending in it", "lux", "https://gitlab.com/attacker/luxfi/node", "main", "ghcr.io/luxfi/node:v1", false},
			{"zoo builds its own other repo", "zoo", "https://github.com/zooai/app", "main", "ghcr.io/zooai/app:v1", false},
			{"lux builds a repo not on the list", "lux", "https://github.com/luxfi/wallet", "main", "ghcr.io/luxfi/wallet:v1", false},
			{"hanzo builds anything", "hanzo", "https://github.com/hanzoai/app", "main", "ghcr.io/hanzoai/app:v1", false},
		} {
			t.Run(tc.name, func(t *testing.T) {
				peer := &githubPeer{trusted: publishesFrom}
				serveGitHubPeer(t, peer)
				k := fakeK8s()
				app, _ := runnerAppOn(t, k)
				code, body := postRunnerWith(t, app, appIdentity(tc.org+"-ci", tc.org), "", map[string]any{
					"repo": tc.repo, "ref": tc.ref,
					"image": tc.image, "dockerfile": "Dockerfile", "platforms": []string{"linux/amd64", "linux/arm64"}})
				if code != http.StatusAccepted {
					t.Fatalf("build: want 202, got %d (%s)", code, body)
				}
				var resp runnerBuildResp
				if err := json.Unmarshal(body, &resp); err != nil {
					t.Fatalf("decode resp: %v", err)
				}
				for _, arch := range []string{"amd64", "arm64"} {
					declared, env := githubReadOf(t, k, buildJobName(resp.BuildJobID, arch))
					if declared != tc.want || env != tc.want {
						t.Fatalf("%s: GH_READ_TOKEN declared=%v env=%v, want both %v", arch, declared, env, tc.want)
					}
				}
				// The ref is asked of GitHub only for a repository on the list; any
				// other build never names its repository or ref to the App.
				asked := peer.refsAsked()
				if _, _, listed := githubReadRepo(tc.org, tc.repo); listed {
					if len(asked) != 1 || asked[0].Ref != tc.ref {
						t.Fatalf("a listed repository's ref must be judged once as asked: %+v", asked)
					}
				} else if len(asked) != 0 {
					t.Fatalf("an unlisted repository asked GitHub about %+v", asked)
				}
			})
		}
	})
}

// Every path that builds decides the same way. A tenant deploy of an
// Application tracking luxfi/node — the lane a git push drives — gets the
// secret at the default branch and not at a pull request's ref.
func TestTenantBuild_GitHubReadIsBoundToTheRef(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		serveGitHubPeer(t, &githubPeer{trusted: publishesFrom})
		for _, tc := range []struct {
			ref  string
			want bool
		}{
			{"main", true},
			{trustedSHA, true},
			{"refs/pull/9/head", false},
			{"feature", false},
		} {
			k := fakeK8s()
			a := Application{Slug: "node", RepoURL: "https://github.com/luxfi/node", RepoBranch: "main", Dockerfile: "Dockerfile"}
			job, err := k.launchBuildJob(context.Background(), estate, "lux", a, "ghcr.io/luxfi/node:t", tc.ref, genID("bld"))
			if err != nil {
				t.Fatalf("%s: launch: %v", tc.ref, err)
			}
			if declared, env := githubReadOf(t, k, job); declared != tc.want || env != tc.want {
				t.Errorf("%s: GH_READ_TOKEN declared=%v env=%v, want %v", tc.ref, declared, env, tc.want)
			}
		}
	})
}

// A provider that cannot be asked withholds the secret: a listed repository at
// its default branch builds without it rather than on an unjudged ref.
func TestRunnerBuild_GitHubReadWithheldWithoutAnAnswer(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		t.Setenv("ZIP_RUNTIME_DIR", t.TempDir()) // no provider listening
		k := fakeK8s()
		app, _ := runnerAppOn(t, k)
		code, body := postRunnerWith(t, app, appIdentity("zoo-ci", "zoo"), "", map[string]any{
			"repo": "https://github.com/zooai/node", "ref": "main", "image": "ghcr.io/zooai/node:v1", "dockerfile": "Dockerfile",
			"platforms": []string{"linux/amd64"}})
		if code != http.StatusAccepted {
			t.Fatalf("build: want 202, got %d (%s)", code, body)
		}
		var resp runnerBuildResp
		if err := json.Unmarshal(body, &resp); err != nil {
			t.Fatalf("decode resp: %v", err)
		}
		if declared, env := githubReadOf(t, k, buildJobName(resp.BuildJobID, "")); declared || env {
			t.Fatalf("no provider must mean no secret: declared=%v env=%v", declared, env)
		}
	})
}

// githubReadRepo itself: github.com exactly, the whole path, keyed on BOTH org
// and owner/name, case-folded on the pair, and denying by default.
func TestGitHubReadRepoIsHostOrgAndRepo(t *testing.T) {
	for _, tc := range []struct {
		org, repo string
		want      bool
	}{
		{"zoo", "https://github.com/zooai/node.git", true},
		{"zoo", "https://github.com/zooai/node", true},
		{"zoo", "https://github.com/zooai/node/", true},
		{"zoo", "https://github.com/ZooAI/Node", true},
		{"lux", "https://github.com/luxfi/node", true},
		{"lux", "https://github.com/lux-cpp/node", true},
		{"hanzo", "https://github.com/hanzo-inc/engine", true},
		{"hanzo", "https://github.com/hanzoai/git.git", true},
		{"lux", "https://github.com/hanzoai/git.git", false},
		{"hanzo", "https://git.hanzo.ai/hanzoai/git.git", false},
		{"zoo", "https://github.com/hanzo-inc/engine", false},

		{"lux", "https://gitlab.com/luxfi/node", false},
		{"lux", "https://git.hanzo.ai/luxfi/node", false},
		{"lux", "https://bitbucket.org/luxfi/node", false},
		{"lux", "https://codeload.github.com/luxfi/node", false},
		{"lux", "https://www.github.com/luxfi/node", false},
		{"lux", "https://github.com.evil.example/luxfi/node", false},
		{"lux", "https://github.com:443/luxfi/node", false},
		{"lux", "https://gitlab.com/attacker/luxfi/node", false},
		{"lux", "https://github.com/attacker/luxfi/node", false},
		{"lux", "https://github.com/luxfi/node/tree/main", false},
		{"lux", "http://github.com/luxfi/node", false},
		{"lux", "https://x@github.com/luxfi/node", false},
		{"lux", "https://github.com/lux-gpu/kernels", false},
		{"zoo", "https://github.com/luxfi/node", false},
		{"hanzo", "https://github.com/luxfi/node", false},
		{"lux", "https://github.com/luxfi/wallet", false},
		{"lux", "", false},
	} {
		if _, _, got := githubReadRepo(tc.org, tc.repo); got != tc.want {
			t.Errorf("githubReadRepo(%q, %q) = %v, want %v", tc.org, tc.repo, got, tc.want)
		}
	}
}

// repo/image required ⇒ 400.
func TestRunnerBuild_MissingFields(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := runnerApp(t)
		code, _ := postRunner(t, app, map[string]any{"repo": "https://github.com/hanzoai/cloud"})
		if code != http.StatusBadRequest {
			t.Fatalf("missing image: want 400, got %d", code)
		}
	})
}

// image outside the owned registries ⇒ 403 (a leaked token can't push anywhere).
func TestRunnerBuild_DisallowedImage(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := runnerApp(t)
		code, _ := postRunner(t, app, map[string]any{
			"repo": "https://github.com/hanzoai/cloud", "image": "docker.io/evil/x:latest"})
		if code != http.StatusForbidden {
			t.Fatalf("disallowed image: want 403, got %d", code)
		}
	})
}

// A build that names a stage and a context directory builds that stage from that
// directory on every architecture it fans out to, and its Dockerfile path stays
// relative to the repository root.
func TestRunnerBuild_TargetAndContextReachEveryArchitecture(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		k := fakeK8s()
		app, _ := runnerAppOn(t, k)
		code, body := postRunner(t, app, map[string]any{
			"repo": "https://github.com/hanzoai/bot", "sha": "00971263b1c4e5f60718293a4b5c6d7e8f90a1b2",
			"image": "ghcr.io/hanzoai/sandbox:v1", "dockerfile": "sandbox/Dockerfile",
			"dockerTarget": "exec", "context": "./sandbox/"})
		if code != http.StatusAccepted {
			t.Fatalf("want 202, got %d (%s)", code, body)
		}
		var resp runnerBuildResp
		if err := json.Unmarshal(body, &resp); err != nil {
			t.Fatalf("decode resp: %v", err)
		}
		for _, arch := range []string{"amd64", "arm64"} {
			job, err := k.dyn.Resource(jobsGVR).Namespace(k.buildNS).Get(context.Background(), buildJobName(resp.BuildJobID, arch), metav1.GetOptions{})
			if err != nil {
				t.Fatalf("%s Job: %v", arch, err)
			}
			cmd := podSpec(job.Object)["containers"].([]any)[0].(map[string]any)["command"].([]any)
			for opt, want := range map[string]string{"target=": "exec", "contextsubdir=": "sandbox", "filename=": "sandbox/Dockerfile"} {
				if got, ok := argvOpt(cmd, opt); !ok || got != want {
					t.Errorf("%s Job: --opt %s%s (present %v), want %s", arch, opt, got, ok, want)
				}
			}
		}
	})
}

// The build line belongs to the org the credential names. One org past its concurrency is
// accepted and waits, never refused, while another org's build starts beside it, and every
// Job the door launches carries its caller's org — the label the line reads.
func TestRunnerBuild_LineIsTheCallersOrg(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		k := fakeK8s()
		app, _ := runnerAppOn(t, k)
		post := func(org, image string) (int, runnerBuildResp) {
			code, body := postRunnerAs(t, app, "e7d7-uuid", org, true, false, map[string]any{
				"repo": "https://github.com/hanzoai/app", "sha": "00971263b1c4e5f60718293a4b5c6d7e8f90a1b2",
				"image": image, "dockerfile": "Dockerfile", "platforms": []string{"linux/amd64"}})
			var resp runnerBuildResp
			if code == http.StatusAccepted {
				if err := json.Unmarshal(body, &resp); err != nil {
					t.Fatalf("decode resp: %v", err)
				}
			}
			return code, resp
		}
		var last runnerBuildResp
		for i := 0; i <= client.PlatformBuildsPerOrgSeed; i++ {
			code, resp := post("hanzo", "ghcr.io/hanzoai/app:v1.0."+itoa(i))
			if code != http.StatusAccepted {
				t.Fatalf("hanzo build %d: want 202, got %d", i, code)
			}
			job, err := k.dyn.Resource(jobsGVR).Namespace(k.buildNS).Get(context.Background(), buildJobName(resp.BuildJobID, ""), metav1.GetOptions{})
			if err != nil {
				t.Fatalf("hanzo build %d Job: %v", i, err)
			}
			if got := job.GetLabels()["hanzo.ai/org"]; got != "hanzo" {
				t.Fatalf("Job charged to %q, want the caller's org hanzo", got)
			}
			last = resp
		}
		if last.Status != "queued" {
			t.Fatalf("the build past the org's concurrency is %q, want queued", last.Status)
		}
		if pos, err := k.queuePosition(context.Background(), last.BuildJobID); err != nil || pos != 1 {
			t.Fatalf("queue position %d (%v), want 1", pos, err)
		}
		if code, resp := post("lux", "ghcr.io/luxfi/app:v1.0.0"); code != http.StatusAccepted {
			t.Fatalf("lux beside a full hanzo: want 202, got %d", code)
		} else if pos, _ := k.queuePosition(context.Background(), resp.BuildJobID); pos != 0 {
			t.Fatalf("lux waits at %d beside hanzo's full line; it has the whole cluster", pos)
		}
	})
}

// A stage or a context directory the build cannot honor is refused before any Job
// exists: a directory outside the repository, a target that is not a stage name,
// and either one on a build with no Dockerfile, whose frontend reads neither.
func TestRunnerBuild_TargetAndContextRefusedWhenUnbuildable(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		for name, extra := range map[string]map[string]any{
			"context outside the repository": {"dockerfile": "Dockerfile", "context": "sandbox/../.."},
			"target not a stage name":        {"dockerfile": "Dockerfile", "dockerTarget": "exec,push=true"},
			"target with no dockerfile":      {"dockerTarget": "exec"},
			"context with no dockerfile":     {"context": "sandbox"},
		} {
			k := fakeK8s()
			app, _ := runnerAppOn(t, k)
			req := map[string]any{"repo": "https://github.com/hanzoai/bot", "sha": "00971263b1c4e5f60718293a4b5c6d7e8f90a1b2", "image": "ghcr.io/hanzoai/sandbox:v1"}
			maps.Copy(req, extra)
			if code, body := postRunner(t, app, req); code != http.StatusBadRequest {
				t.Errorf("%s: want 400, got %d (%s)", name, code, body)
			}
			jobs, err := k.dyn.Resource(jobsGVR).Namespace(k.buildNS).List(context.Background(), metav1.ListOptions{})
			if err != nil {
				t.Fatalf("%s: list Jobs: %v", name, err)
			}
			if len(jobs.Items) != 0 {
				t.Errorf("%s: %d Jobs created for a refused build", name, len(jobs.Items))
			}
		}
	})
}

// valid token + repo + owned image ⇒ 202 with a build job id.
func TestRunnerBuild_Launches(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := runnerApp(t)
		code, body := postRunner(t, app, map[string]any{
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
	})
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
		{"lux", "ghcr.io/lux-cpp/node", "https://github.com/lux-cpp/node"},
		{"lux", "ghcr.io/lux-gpu/gpu-kernels", "https://github.com/lux-gpu/gpu-kernels"},
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
	// Lux's C++ and GPU orgs are Lux's alone.
	for _, org := range []string{"hanzo", "zoo", "adnexus"} {
		if imageInOrgRegistry("ghcr.io/lux-cpp/node", org) || imageInOrgRegistry("ghcr.io/lux-gpu/gpu-kernels", org) {
			t.Errorf("org %q reached a Lux C++/GPU namespace", org)
		}
		if repoOwnerInOrg("https://github.com/lux-cpp/node", org) || repoOwnerInOrg("https://github.com/lux-gpu/gpu-kernels", org) {
			t.Errorf("org %q reached a Lux C++/GPU forge owner", org)
		}
	}
}
