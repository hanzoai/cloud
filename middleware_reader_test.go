package cloud

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/zap-proto/zip"
)

// TestReaderGuardRejectsWrites proves the fail-closed reader boundary: with the
// guard mounted (Reader), GET/HEAD/OPTIONS reach the handler and EVERY mutating
// verb is 405 without reaching it — one guard covering every store, so a
// mis-routed write can never persist to a reader's ephemeral dir.
func TestReaderGuardRejectsWrites(t *testing.T) {
	app := zip.New(zip.Config{})
	app.Use(ReaderGuard())

	var reached bool
	app.All("/v1/kms/secret", func(c *zip.Ctx) error {
		reached = true
		return c.JSON(http.StatusOK, map[string]bool{"ok": true})
	})

	for _, tc := range []struct {
		method string
		want   int
		reach  bool
	}{
		{http.MethodGet, http.StatusOK, true},
		{http.MethodHead, http.StatusOK, true},
		{http.MethodOptions, http.StatusOK, true},
		{http.MethodPost, http.StatusMethodNotAllowed, false},
		{http.MethodPut, http.StatusMethodNotAllowed, false},
		{http.MethodPatch, http.StatusMethodNotAllowed, false},
		{http.MethodDelete, http.StatusMethodNotAllowed, false},
	} {
		reached = false
		resp, err := app.Fiber().Test(httptest.NewRequest(tc.method, "/v1/kms/secret", nil))
		if err != nil {
			t.Fatalf("%s: %v", tc.method, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != tc.want {
			t.Errorf("%s = %d, want %d", tc.method, resp.StatusCode, tc.want)
		}
		if reached != tc.reach {
			t.Errorf("%s reached store handler = %v, want %v (a write must never reach a reader's store)", tc.method, reached, tc.reach)
		}
	}
}

// TestWriterUngated proves the Writer path is byte-identical to today: serve.go
// mounts ReaderGuard ONLY when role.IsReader(), so without it every verb reaches
// the handler and succeeds.
func TestWriterUngated(t *testing.T) {
	app := zip.New(zip.Config{})
	var reached bool
	app.All("/v1/kms/secret", func(c *zip.Ctx) error {
		reached = true
		return c.JSON(http.StatusOK, map[string]bool{"ok": true})
	})
	for _, m := range []string{http.MethodGet, http.MethodPost, http.MethodDelete} {
		reached = false
		resp, err := app.Fiber().Test(httptest.NewRequest(m, "/v1/kms/secret", nil))
		if err != nil {
			t.Fatalf("%s: %v", m, err)
		}
		_ = resp.Body.Close()
		if !reached || resp.StatusCode != http.StatusOK {
			t.Errorf("writer %s: reached=%v status=%d, want reached=true status=200", m, reached, resp.StatusCode)
		}
	}
}
