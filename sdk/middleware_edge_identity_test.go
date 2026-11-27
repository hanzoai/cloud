package cloud

// THE ADDRESS LIMIT IS FOR WHO HAS NOTHING ELSE TO BE LIMITED BY.
//
// The edge runs before identity and used to charge every request to one per-IP
// count, so a signed-in customer behind one address met the anonymous flood cap
// before its own key or org ceiling was asked. These drive the whole order a
// request meets — EdgeRateLimit, SanitizeIdentity against a stub IAM, then
// ScopeRateLimit — and pin each kind of caller to the limiter that is theirs:
// anonymous to the address, a credential that is nobody to the address on every
// bucket, a validated key to its own rate and its org's.

import (
	"crypto/rsa"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zap-proto/zip"
)

// chain is the request path chainOf builds, with what a test reaches into: how many
// lookups IAM was asked, the key the JWKS publishes (to sign a real token), and the
// boundary (to read or seed its key cache).
type chain struct {
	app      *zip.App
	asked    *atomic.Int64
	key      *rsa.PrivateKey
	boundary *Boundary
}

// send1 is one request from addr on bearer, with any other headers in hdr.
func send1(t *testing.T, app *zip.App, method, path, addr, bearer string, hdr map[string]string) int {
	t.Helper()
	r := httptest.NewRequest(method, path, nil)
	r.Header.Set("X-Forwarded-For", addr)
	if bearer != "" {
		r.Header.Set("Authorization", "Bearer "+bearer)
	}
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	resp, err := app.Test(r, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

// from sends one request from addr, presenting bearer when it is not empty.
func from(t *testing.T, app *zip.App, addr, bearer string) *http.Response {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/v1/train/jobs", nil)
	r.Header.Set("X-Forwarded-For", addr)
	if bearer != "" {
		r.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := app.Test(r, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	_ = resp.Body.Close()
	return resp
}

// paced asserts resp states the ceiling limit — its limit, and a reset in unix
// seconds inside the window — and, refused, when to come back.
func paced(t *testing.T, what string, resp *http.Response, status, limit int) {
	t.Helper()
	if resp.StatusCode != status {
		t.Fatalf("%s: status %d, want %d", what, resp.StatusCode, status)
	}
	if got := resp.Header.Get("X-RateLimit-Limit"); got != strconv.Itoa(limit) {
		t.Fatalf("%s: X-RateLimit-Limit %q, want %d — the header is not the ceiling that applies", what, got, limit)
	}
	reset, err := strconv.ParseInt(resp.Header.Get("X-RateLimit-Reset"), 10, 64)
	if err != nil || reset < time.Now().Unix() || reset > time.Now().Add(61*time.Second).Unix() {
		t.Fatalf("%s: X-RateLimit-Reset %q, want unix seconds inside the minute", what, resp.Header.Get("X-RateLimit-Reset"))
	}
	if status == http.StatusTooManyRequests {
		if wait, err := strconv.Atoi(resp.Header.Get("Retry-After")); err != nil || wait < 1 || wait > 60 {
			t.Fatalf("%s: Retry-After %q, want 1..60", what, resp.Header.Get("Retry-After"))
		}
	}
}
