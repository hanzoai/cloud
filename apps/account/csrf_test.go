package account

import (
	"fmt"
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
	resp, err := app.Test(r)
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
	code, body := req(t, app, http.MethodGet, "/v1/account/csrf",
		map[string]string{"X-User-Id": user, "X-Org-Id": org}, "")
	if code != http.StatusOK {
		t.Fatalf("GET /v1/account/csrf: want 200, got %d (%s)", code, body)
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
//
// EVERY key write, not one of them. The gate is a property of the GROUP each op is
// registered on, so it is carried — or dropped — by the registration rather than by
// anything visible at the handler, and a revoke that lost it destroys a credential
// on a cross-site forgery.
func TestCSRF_AmbientWriteWithoutTokenIsRefused(t *testing.T) {
	f := newFakeIAM()
	app := mountApp(t, f.server(t).URL, "hanzo-console", "s3cr3t")

	for _, w := range []struct{ method, path string }{
		{http.MethodPost, "/v1/account/keys"},
		{http.MethodDelete, "/v1/account/keys"},
		{http.MethodPost, "/v1/account/orgs"}, // …and the org write, on its own group
	} {
		code, _ := req(t, app, w.method, w.path, map[string]string{
			"X-User-Id": "alice", "X-Org-Id": "acme",
			"Cookie": "iam_access_token=opaque-sid", // ambient credential
		}, "")
		if code != http.StatusForbidden {
			t.Fatalf("%s %s ambient w/o CSRF token: want 403, got %d", w.method, w.path, code)
		}
	}
	if len(f.mintedFor) != 0 || len(f.revokedFor) != 0 {
		t.Fatalf("IAM reached without a CSRF token: minted=%v revoked=%v", f.mintedFor, f.revokedFor)
	}
}

// TestCSRF_AmbientWriteFromTheConsoleAllows: the same request, made by the console
// itself. What says so is Sec-Fetch-Site, which the browser sets and script cannot,
// so there is no token to mint and no key for two processes to agree on.
func TestCSRF_AmbientWriteWithValidTokenAllows(t *testing.T) {
	f := newFakeIAM()
	app := mountApp(t, f.server(t).URL, "hanzo-console", "s3cr3t")

	code, body := req(t, app, http.MethodPost, "/v1/account/keys", map[string]string{
		"X-User-Id": "alice", "X-Org-Id": "acme",
		"Cookie":         "iam_access_token=opaque-sid",
		"Sec-Fetch-Site": "same-origin",
	}, "")
	if code != http.StatusOK {
		t.Fatalf("ambient write from the console itself: want 200, got %d (%s)", code, body)
	}
	if len(f.mintedFor) != 1 || f.mintedFor[0] != "acme/alice" {
		t.Fatalf("mint should target acme/alice: %v", f.mintedFor)
	}
}

// TestCSRF_ASiblingSubdomainIsRefused is what the identity-bound MAC was really
// defending, stated directly.
//
// The old control minted a token whose MAC bound (X-User-Id, X-Org-Id), so a token
// minted by an attacker could not authorize a write carried by the VICTIM's cookie.
// That mattered because a page on a sibling *.hanzo.ai host can set a custom header
// — same-site requests are not stopped by preflight the way cross-site ones are —
// and SameSite sends the session cookie to it.
//
// Sec-Fetch-Site answers that case at the door: a sibling subdomain's request says
// `same-site`, never `same-origin`. No token, no MAC, no identity binding, and the
// forgery never reaches the handler. A write as mallory FROM mallory's own console
// is not forgery and is not refused — the old test read that as a violation because
// the token was the only thing it could measure.
func TestCSRF_ASiblingSubdomainIsRefused(t *testing.T) {
	f := newFakeIAM()
	app := mountApp(t, f.server(t).URL, "hanzo-console", "s3cr3t")

	code, _ := req(t, app, http.MethodPost, "/v1/account/keys", map[string]string{
		"X-User-Id": "alice", "X-Org-Id": "acme",
		"Cookie":         "iam_access_token=opaque-sid",
		"Sec-Fetch-Site": "same-site", // a page on another *.hanzo.ai host
	}, "")
	if code != http.StatusForbidden {
		t.Fatalf("a same-site (sibling subdomain) write: want 403, got %d", code)
	}
	if len(f.mintedFor) != 0 {
		t.Fatalf("mint reached on a sibling-subdomain forgery: %v", f.mintedFor)
	}
}

// TestCSRF_BearerAuthSkipsCSRF: an explicit Bearer credential is not CSRF-able, so no
// token is required (API/machine callers unaffected).
func TestCSRF_BearerAuthSkipsCSRF(t *testing.T) {
	f := newFakeIAM()
	app := mountApp(t, f.server(t).URL, "hanzo-console", "s3cr3t")

	code, body := req(t, app, http.MethodPost, "/v1/account/keys", map[string]string{
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

// TestRateLimit_PerPrincipalBurstThen429: the money-write cap keys on the VALIDATED
// principal (un-spoofable), returns 429 past the burst, and a DIFFERENT principal is
// NOT throttled by the first's flood — and a forged X-Forwarded-For does not reset it.
func TestRateLimit_PerPrincipalBurstThen429(t *testing.T) {
	f := newFakeIAM()
	app := mountApp(t, f.server(t).URL, "hanzo-console", "s3cr3t")

	// alice floods, sending a FRESH X-Forwarded-For each request (the old XFF keying
	// would have reset the bucket every time — it must NOT now).
	var got429 bool
	for i := range keysWriteRatePerMin + 5 {
		code, _ := req(t, app, http.MethodPost, "/v1/account/keys", map[string]string{
			"X-User-Id": "alice", "X-Org-Id": "acme",
			"Authorization":   "Bearer j.w.t",                     // skip CSRF, isolate the limiter
			"X-Forwarded-For": fmt.Sprintf("203.0.113.%d", i%250), // attacker rotates XFF
		}, "")
		if code == 429 {
			got429 = true
			break
		}
		if code != http.StatusOK {
			t.Fatalf("req %d: unexpected %d", i, code)
		}
	}
	if !got429 {
		t.Fatalf("expected 429 after alice's per-principal burst of %d (XFF rotation must not reset it)", keysWriteRatePerMin)
	}

	// bob (a different validated principal) is unaffected by alice's exhausted bucket.
	code, body := req(t, app, http.MethodPost, "/v1/account/keys", map[string]string{
		"X-User-Id": "bob", "X-Org-Id": "acme",
		"Authorization": "Bearer j.w.t",
	}, "")
	if code != http.StatusOK {
		t.Fatalf("bob must have his own bucket, not alice's: want 200, got %d (%s)", code, body)
	}
}
