package cli

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// TestUnlinkIdempotent — a repeat `hanzo unlink` is a no-op. The deregister POST
// hitting an already-terminal (409) or absent (404) fleet row is the desired end
// state, so runDisconnect returns nil (not an error) and says so, letting `unlink`
// be run twice safely.
func TestUnlinkIdempotent(t *testing.T) {
	for _, code := range []int{http.StatusConflict, http.StatusNotFound} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(code)
			_, _ = w.Write([]byte(`{"error":"activity terminal"}`))
		}))

		t.Setenv("HANZO_TOKEN", "test-token") // ensureToken honors this; no login needed
		var out bytes.Buffer
		cmd := &cobra.Command{}
		cmd.SetContext(context.Background())
		cmd.SetOut(&out)

		if err := runDisconnect(cmd, &Env{CloudURL: srv.URL}); err != nil {
			t.Errorf("HTTP %d: runDisconnect should be idempotent (nil), got %v", code, err)
		}
		if !strings.Contains(out.String(), "already unlinked") {
			t.Errorf("HTTP %d: want 'already unlinked' notice, got %q", code, out.String())
		}
		srv.Close()
	}
}

// TestUnlinkDeregisterErrorSurfaces — a non-terminal deregister failure (e.g. 500)
// is a real error and must be returned, not swallowed as idempotent success.
func TestUnlinkDeregisterErrorSurfaces(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"boom"}`))
	}))
	defer srv.Close()

	t.Setenv("HANZO_TOKEN", "test-token")
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	cmd.SetOut(&bytes.Buffer{})

	if err := runDisconnect(cmd, &Env{CloudURL: srv.URL}); err == nil {
		t.Fatal("a 500 deregister must surface an error, got nil")
	}
}
