package integrations

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/kms"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// flatten_test.go guards the connector webhook against the 500→4xx bug: integrations
// mounts AFTER the commerce embed, whose /v1 ErrorHandlerJSON rewrites ANY propagated
// downstream error into a hardcoded 500. cloud.Terminal writes the reject status
// in-band so it survives. It also pins the route move to the /v1/connector namespace.

// installV1Flatten reproduces apps.mountCommerce's /v1 ErrorHandlerJSON (see the sync
// twin): a /v1 group middleware that turns any propagated downstream error into 500.
func installV1Flatten(app *zip.App) {
	app.Group("/v1").Use(func(c *zip.Ctx) error {
		if err := c.Next(); err != nil {
			return c.Bytes(http.StatusInternalServerError, []byte(`{"error":"flattened"}`))
		}
		return nil
	})
}

// newAppUnderFlatten mounts integrations BEHIND the /v1 flatten filter (production
// mount order: commerce before integrations in apps.Wire).
func newAppUnderFlatten(t *testing.T, kc *kms.Client) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	installV1Flatten(app)
	deps := cloud.Deps{Logger: luxlog.New("test"), DataDir: t.TempDir(), Domain: "api.hanzo.ai"}
	if kc != nil {
		deps.KMS = kc
	}
	if err := Mount(app, deps); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	t.Cleanup(func() { _ = Shutdown(context.Background()) })
	return app
}

// TestConnectorWebhookRejectsSurviveCommerceFlatten: under the commerce /v1 flatten
// filter a bad signature stays 401 and a malformed body stays 400 — never 500.
func TestConnectorWebhookRejectsSurviveCommerceFlatten(t *testing.T) {
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

// TestConnectorWebhookRouteMoved pins the /v1/connector/github/webhook namespace: the
// new path resolves to the handler (a bad signature is rejected, not 404'd) and the
// old top-level /v1/github-webhook is gone (404).
func TestConnectorWebhookRouteMoved(t *testing.T) {
	const secret = "wh_secret_moved"
	t.Setenv(githubWebhookSecretEnv, secret)
	app := newApp(t, newKMS(t))

	p := pushPayload(t, 111, "widgets", "refs/heads/main")
	// webhookPost targets the new path; a bad signature resolving to 401 proves it.
	if r := webhookPost(t, app, "push", "sha256=deadbeef", "", p); r.Code != http.StatusUnauthorized {
		t.Fatalf("new /v1/connector/github/webhook must resolve (bad-sig 401), got %d (%s)", r.Code, r.Body)
	}
	// The old path no longer exists.
	rq := httptest.NewRequest(http.MethodPost, "/v1/github-webhook", bytes.NewReader(p))
	rq.Header.Set("X-GitHub-Event", "push")
	rq.Header.Set("X-Hub-Signature-256", ghSign(secret, p))
	resp, err := app.Fiber().Test(rq)
	if err != nil {
		t.Fatalf("old path: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("old /v1/github-webhook must 404 after the move, got %d", resp.StatusCode)
	}
}
