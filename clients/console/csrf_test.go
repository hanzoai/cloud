package console

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zap-proto/zip"
)

// req drives one request with arbitrary headers through the mounted app.
func req(t *testing.T, app *zip.App, method, path string, hdr map[string]string, body string) (int, []byte) {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	r := httptest.NewRequest(method, path, rdr)
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	resp, err := app.Fiber().Test(r)
	if err != nil {
		t.Fatalf("Test %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// csrfToken fetches a CSRF token for (user, org) from the issue endpoint.
func csrfToken(t *testing.T, app *zip.App, user, org string) string {
	t.Helper()
	code, body := req(t, app, http.MethodGet, "/v1/console/csrf",
		map[string]string{"X-User-Id": user, "X-Org-Id": org}, "")
	if code != http.StatusOK {
		t.Fatalf("GET /v1/console/csrf: want 200, got %d (%s)", code, body)
	}
	var r struct {
		CsrfToken string `json:"csrfToken"`
	}
	mustJSON(t, body, &r)
	if r.CsrfToken == "" {
		t.Fatalf("empty csrfToken: %s", body)
	}
	return r.CsrfToken
}

// TestCSRF_AmbientWriteWithoutTokenIsRefused: a cookie-authenticated (ambient) write
// with no X-CSRF-Token is 403, and IAM is never touched.
func TestCSRF_AmbientWriteWithoutTokenIsRefused(t *testing.T) {
	f := newFakeIAM()
	app := mountApp(t, f.server(t).URL, "hanzo-console", "s3cr3t")

	code, _ := req(t, app, http.MethodPost, "/v1/console/keys", map[string]string{
		"X-User-Id": "alice", "X-Org-Id": "acme",
		"Cookie": "iam_access_token=opaque-sid", // ambient credential
	}, "")
	if code != http.StatusForbidden {
		t.Fatalf("ambient write w/o CSRF token: want 403, got %d", code)
	}
	if len(f.mintedFor) != 0 {
		t.Fatalf("IAM mint reached without a CSRF token: %v", f.mintedFor)
	}
}

// TestCSRF_AmbientWriteWithValidTokenAllows: same request WITH a valid token mints.
func TestCSRF_AmbientWriteWithValidTokenAllows(t *testing.T) {
	f := newFakeIAM()
	app := mountApp(t, f.server(t).URL, "hanzo-console", "s3cr3t")

	tok := csrfToken(t, app, "alice", "acme")
	code, body := req(t, app, http.MethodPost, "/v1/console/keys", map[string]string{
		"X-User-Id": "alice", "X-Org-Id": "acme",
		"Cookie":       "iam_access_token=opaque-sid",
		"X-CSRF-Token": tok,
	}, "")
	if code != http.StatusOK {
		t.Fatalf("ambient write with valid CSRF token: want 200, got %d (%s)", code, body)
	}
	if len(f.mintedFor) != 1 || f.mintedFor[0] != "acme/alice" {
		t.Fatalf("mint should target acme/alice: %v", f.mintedFor)
	}
}

// TestCSRF_TokenBoundToIdentity: a token minted for alice cannot authorize a write as
// mallory — the MAC binds (X-User-Id, X-Org-Id).
func TestCSRF_TokenBoundToIdentity(t *testing.T) {
	f := newFakeIAM()
	app := mountApp(t, f.server(t).URL, "hanzo-console", "s3cr3t")

	aliceTok := csrfToken(t, app, "alice", "acme")
	code, _ := req(t, app, http.MethodPost, "/v1/console/keys", map[string]string{
		"X-User-Id": "mallory", "X-Org-Id": "acme", // different principal
		"Cookie":       "iam_access_token=opaque-sid",
		"X-CSRF-Token": aliceTok, // stolen/replayed token bound to alice
	}, "")
	if code != http.StatusForbidden {
		t.Fatalf("cross-identity CSRF token replay: want 403, got %d", code)
	}
	if len(f.mintedFor) != 0 {
		t.Fatalf("mint reached on a cross-identity token: %v", f.mintedFor)
	}
}

// TestCSRF_BearerAuthSkipsCSRF: an explicit Bearer credential is not CSRF-able, so no
// token is required (API/machine callers unaffected).
func TestCSRF_BearerAuthSkipsCSRF(t *testing.T) {
	f := newFakeIAM()
	app := mountApp(t, f.server(t).URL, "hanzo-console", "s3cr3t")

	code, body := req(t, app, http.MethodPost, "/v1/console/keys", map[string]string{
		"X-User-Id": "alice", "X-Org-Id": "acme",
		"Authorization": "Bearer some.jwt.token", // explicit (non-ambient) credential
		"Cookie":        "iam_access_token=opaque-sid",
	}, "")
	if code != http.StatusOK {
		t.Fatalf("Bearer write should skip CSRF and mint: want 200, got %d (%s)", code, body)
	}
	if len(f.mintedFor) != 1 {
		t.Fatalf("Bearer write should have minted: %v", f.mintedFor)
	}
}

// TestRateLimit_MintBurstThen429: the money-write per-IP cap returns 429 past the burst.
func TestRateLimit_MintBurstThen429(t *testing.T) {
	f := newFakeIAM()
	app := mountApp(t, f.server(t).URL, "hanzo-console", "s3cr3t")

	hdr := map[string]string{
		"X-User-Id": "alice", "X-Org-Id": "acme",
		"Authorization":   "Bearer j.w.t", // skip CSRF so we isolate the rate limiter
		"X-Forwarded-For": "203.0.113.7",  // stable IP key
	}
	var got429 bool
	for i := 0; i < keysWriteRatePerMin+5; i++ {
		code, _ := req(t, app, http.MethodPost, "/v1/console/keys", hdr, "")
		if code == 429 {
			got429 = true
			break
		}
		if code != http.StatusOK {
			t.Fatalf("req %d: unexpected %d", i, code)
		}
	}
	if !got429 {
		t.Fatalf("expected a 429 after the per-IP burst of %d", keysWriteRatePerMin)
	}
}
