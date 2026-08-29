package integrations

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/kms"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// flatten_test.go guards the GitHub webhook against the 500→4xx bug: integrations
// mounts AFTER the commerce embed, whose /v1 ErrorHandlerJSON rewrites ANY propagated
// downstream error into a hardcoded 500. cloud.Terminal writes the reject status
// in-band so it survives. It also pins the endpoint's address.

// installV1Flatten reproduces apps.mountCommerce's ErrorHandlerJSON (see the sync
// twin): a filter over /v1 that turns any propagated downstream error into 500.
//
// At the ROOT, gated on the path it claims, which is the shape production actually
// runs (commerce.commerceErrorScope). It used to hang on a /v1 group, and that
// fixture could not compose: the routes it means to wrap are registered on the root
// by Mount, so the group node held middleware over an empty subtree and zip refused
// the program — the same refusal that stopped the app this file guards.
func installV1Flatten(app *zip.App) {
	app.Use(zip.H(func(c *zip.Ctx) error {
		if !strings.HasPrefix(c.Path(), "/v1/") {
			return c.Next()
		}
		if err := c.Next(); err != nil {
			return c.Bytes(http.StatusInternalServerError, []byte(`{"error":"flattened"}`))
		}
		return nil
	}))
}

// newAppUnderFlatten mounts integrations BEHIND the /v1 flatten filter (production
// mount order: commerce before integrations in apps.Wire).
func newAppUnderFlatten(t *testing.T, kc *kms.Client) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	installV1Flatten(app) // the commerce filter is OUTERMOST: it mounts before us
	compose(app)
	deps := cloud.Deps{DataDir: t.TempDir(), Domain: "api.hanzo.ai"}
	if kc != nil {
		deps.KMS = kc
	}
	if err := Use(app, deps); err != nil {
		t.Fatalf("Use:  %v", err)
	}
	t.Cleanup(func() { _ = Shutdown(context.Background()) })
	return app
}

// TestGithubWebhookRejectsSurviveCommerceFlatten: under the commerce /v1 flatten
// filter a bad signature stays 401 and a malformed body stays 400 — never 500.
func TestGithubWebhookRejectsSurviveCommerceFlatten(t *testing.T) {
	const secret = "wh_secret_flat"
	t.Setenv(githubWebhookSecretEnv, secret)
	app := newAppUnderFlatten(t, newKMS(t))

	p := pushPayload(t, 111, "widgets", "refs/heads/main")
	if r := webhookPost(t, app, "push", "sha256=deadbeef", "", p); r.Code != http.StatusUnauthorized {
		t.Fatalf("bad-sig under flatten want 401, got %d (%s)", r.Code, r.Body)
	}
	// Well-signed over the raw bytes, but the body is not valid JSON → 400.
	bad := []byte("{not-json")
	if r := webhookPost(t, app, "push", ghSign(secret, bad), "", bad); r.Code != http.StatusBadRequest {
		t.Fatalf("malformed body under flatten want 400, got %d (%s)", r.Code, r.Body)
	}
}

// TestGithubWebhookAddress pins where the endpoint is and where it is not: the live
// path resolves to the handler (a bad signature is rejected, not 404'd), and every
// address it has left behind is gone. GitHub delivers to a URL configured in the
// App, so an address that answers 404 costs a push event; the retired ones must
// answer nothing rather than half a handler.
func TestGithubWebhookAddress(t *testing.T) {
	const secret = "wh_secret_moved"
	t.Setenv(githubWebhookSecretEnv, secret)
	app := newApp(t, newKMS(t))

	p := pushPayload(t, 111, "widgets", "refs/heads/main")
	// webhookPost targets the live path; a bad signature resolving to 401 proves it.
	if r := webhookPost(t, app, "push", "sha256=deadbeef", "", p); r.Code != http.StatusUnauthorized {
		t.Fatalf("/v1/integrations/github/webhook must resolve (bad-sig 401), got %d (%s)", r.Code, r.Body)
	}
	// The addresses it left: /v1/connector/github/webhook was the one word in the
	// estate for "an integration" that was not "integrations", and /v1/github-webhook
	// predates the namespace entirely.
	for _, gone := range []string{"/v1/connector/github/webhook", "/v1/github-webhook"} {
		rq := httptest.NewRequest(http.MethodPost, gone, bytes.NewReader(p))
		rq.Header.Set("X-GitHub-Event", "push")
		rq.Header.Set("X-Hub-Signature-256", ghSign(secret, p))
		resp, err := app.Test(rq)
		if err != nil {
			t.Fatalf("%s: %v", gone, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s must 404, got %d", gone, resp.StatusCode)
		}
	}
}
