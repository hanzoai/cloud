package functions

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/zap-proto/zip"
)

// doAdmin fires a request as a validated GLOBAL ADMIN: empty X-Org-Id but
// X-User-IsAdmin:true — the state SanitizeIdentity leaves for a global admin
// who has NOT selected an org.
func doAdmin(t *testing.T, app *zip.App, method, path string, body any) (int, []byte) {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, r)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("X-User-IsAdmin", "true")
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// TestRed_NoAdminBucketConfusion is the REGRESSION GUARD for Red HIGH-1's
// admin-bucket corollary. There is no longer a magic "admin" storage bucket: a
// global admin with no selected org is REFUSED (403) rather than dropped into a
// shared bucket, and "Admin"/"admin" are DISTINCT exact tenants (no case-fold).
func TestRed_NoAdminBucketConfusion(t *testing.T) {
	app := mountApp(t)

	// Global admin, empty org -> 403 (no bucket to write into). Per-org data
	// requires an explicit org even for admins.
	if code, _ := doAdmin(t, app, http.MethodPost, "/v1/functions",
		map[string]any{"name": "admin-only", "runtime": "python", "code": "PRIV"}); code != http.StatusForbidden {
		t.Fatalf("global admin with empty org want 403 (no admin bucket), got %d", code)
	}

	// "Admin" and "admin" are distinct exact buckets — no case-fold collision.
	if code, _ := do(t, app, http.MethodPost, "/v1/functions", "Admin",
		map[string]any{"name": "cap-a", "runtime": "python", "code": "X"}); code != http.StatusCreated {
		t.Fatalf("create under org \"Admin\" want 201, got %d", code)
	}
	code, body := do(t, app, http.MethodGet, "/v1/functions", "admin", nil)
	var listed struct {
		Functions []functionView `json:"functions"`
	}
	_ = json.Unmarshal(body, &listed)
	if code != http.StatusOK || len(listed.Functions) != 0 {
		t.Fatalf("org %q must NOT see org %q's data, got %d %+v", "admin", "Admin", code, listed.Functions)
	}
}
