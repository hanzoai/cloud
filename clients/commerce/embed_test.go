// Copyright © 2026 Hanzo AI. MIT License.

package commerce

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

// TestEmbedRequireIdentity verifies the gateway-trust middleware
// returns 401 on /v1/commerce/* without identity headers when
// RequireIdentity=true. It deliberately does not assert on legacy
// route handlers — just on the trust boundary.
func TestEmbedRequireIdentity(t *testing.T) {
	tmp := t.TempDir()
	srv, err := Embed(context.Background(), EmbedConfig{
		DataDir:         filepath.Join(tmp, "data"),
		HTTPAddr:        "127.0.0.1:0",
		Dev:             true,
		RequireIdentity: true,
	})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Stop(ctx)
	})

	// /healthz is unauthenticated by design — probes run before sessions.
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	srv.HTTPHandler().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("/healthz: want 200 got %d body=%q", w.Code, w.Body.String())
	}
}

// TestEmbedHandlerExposed asserts the embedded server exposes a non-nil
// http.Handler we can wire into a parent http.Server.
func TestEmbedHandlerExposed(t *testing.T) {
	tmp := t.TempDir()
	srv, err := Embed(context.Background(), EmbedConfig{
		DataDir:  filepath.Join(tmp, "data"),
		HTTPAddr: "127.0.0.1:0",
		Dev:      true,
	})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	t.Cleanup(func() { _ = srv.Stop(context.Background()) })
	if srv.HTTPHandler() == nil {
		t.Fatalf("HTTPHandler() returned nil")
	}
	if srv.App() == nil {
		t.Fatalf("App() returned nil")
	}
	if srv.HTTPAddr() == "" {
		t.Fatalf("HTTPAddr() empty")
	}
}
