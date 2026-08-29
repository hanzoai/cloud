package sync

import (
	"net/http"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// flatten_test.go is the regression guard for the 500→4xx bug: sync mounts AFTER the
// commerce embed, whose /v1 ErrorHandlerJSON rewrites ANY error a downstream handler
// PROPAGATES into a hardcoded HTTP 500. Before cloud.Terminal, an unauthenticated
// /v1/sync surfaced as 500 instead of 401. These tests reproduce that filter and
// prove the reject statuses now survive it.

// installV1Flatten reproduces apps.mountCommerce's /v1 ErrorHandlerJSON: middleware
// bounded to /v1 that turns any propagated downstream error into a hardcoded 500.
// Faithful to the real commercemid.ErrorHandlerJSON (ErrorJSON writes c.Bytes(500, …));
// kept dependency-light so the guard pins the invariant, not a commerce version.
//
// It takes the ROOT form, gated by path, rather than a group at /v1: the real
// commerce /v1 node owns routes, while this reproduction carries the middleware
// alone, and a group holding middleware with no routes beneath it is a program
// zip refuses to compose.
func installV1Flatten(app *zip.App) {
	app.Use(zip.H(func(c *zip.Ctx) error {
		if c.Path() != "/v1" && !strings.HasPrefix(c.Path(), "/v1/") {
			return c.Next()
		}
		if err := c.Next(); err != nil {
			return c.Bytes(http.StatusInternalServerError, []byte(`{"error":"flattened"}`))
		}
		return nil
	}))
}

// mountSyncUnderFlatten mounts sync BEHIND the /v1 flatten filter, reproducing the
// production order: the composer's Bridge at the root first, commerce before sync.
func mountSyncUnderFlatten(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	compose(app)
	installV1Flatten(app)
	if err := Use(app, cloud.Deps{DataDir: t.TempDir()}); err != nil {
		t.Fatalf("Use:  %v", err)
	}
	t.Cleanup(func() { _ = Shutdown() })
	return app
}

// TestSyncRejectsSurviveCommerceFlatten: under the commerce /v1 flatten filter an
// unauthenticated request stays 401 (not 500) on every /v1/sync verb, and an
// authenticated validation reject stays 400.
func TestSyncRejectsSurviveCommerceFlatten(t *testing.T) {
	app := mountSyncUnderFlatten(t)

	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/v1/sync"},
		{http.MethodGet, "/v1/sync"},
		{http.MethodGet, "/v1/sync/sync_x"},
		{http.MethodPatch, "/v1/sync/sync_x"},
		{http.MethodDelete, "/v1/sync/sync_x"},
		{http.MethodPost, "/v1/sync/sync_x/run"},
	} {
		if code, body := do(t, app, tc.method, tc.path, "", nil); code != http.StatusUnauthorized {
			t.Fatalf("%s %s unauth want 401, got %d (%s)", tc.method, tc.path, code, body)
		}
	}

	// Authenticated but a validation reject (bad direction) → 400, never 500.
	if code, body := do(t, app, http.MethodPost, "/v1/sync", "acme", map[string]any{
		"source":    map[string]any{"provider": "github", "locator": widgetsURL},
		"direction": "sideways",
	}); code != http.StatusBadRequest {
		t.Fatalf("bad-direction want 400, got %d (%s)", code, body)
	}
}
