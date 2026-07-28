// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// See the License for the specific language governing permissions and
// limitations under the License.

package analytics

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zap-proto/zip"
)

// These tests cover the project-API-key → org resolution on the deprecated PostHog
// door, so keyed, bearer-less SDK traffic (posthog-js / insights-go batch) maps to a
// tenant. They drive the REAL /v1/insights/e handler through the injectable
// resolveKeyOrg seam, so no IAM is needed. The observable proxy for "resolved to a
// tenant" is "passed the credential gate" — i.e. NOT 403; without a datastore the
// handler then returns 503, so any non-403 status means the request was admitted.
//
// The resolver used to live in a per-door copy (captureTenant) whose last resort was
// the request Host; every door now shares ONE credential resolver (eventTenant) and
// there is no host fallback anywhere.

// stubResolver swaps resolveKeyOrg for the test and records the key it was handed,
// so a test asserts BOTH that projectKey extracted the right key AND that the
// credential resolver honored the resolution. Restored via t.Cleanup.
func stubResolver(t *testing.T, fn func(key string) (string, bool)) *string {
	t.Helper()
	var got string
	orig := resolveKeyOrg
	resolveKeyOrg = func(_ context.Context, key string) (string, bool) {
		got = key
		return fn(key)
	}
	t.Cleanup(func() { resolveKeyOrg = orig })
	return &got
}

// postKeyed issues POST path (with optional ?query) to the mounted app, setting an
// optional Host and headers and a raw JSON body — no middleware, mirroring the
// SanitizeIdentity-minted-header harness the other analytics tests use.
func postKeyed(t *testing.T, app *zip.App, path, host, body string, hdr map[string]string) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if host != "" {
		req.Host = host
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test POST %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

// TestKeyOrg_KeyExtractionReachesResolver: a project key presented in the
// body, the ?api_key= query, or the x-api-key header is extracted and handed to the
// resolver, and a resolved key passes the credential gate (never 403).
func TestKeyOrg_KeyExtractionReachesResolver(t *testing.T) {
	app := mountApp(t)
	cases := []struct {
		name, path, body string
		hdr              map[string]string
		wantKey          string
	}{
		{"body", "/v1/insights/e", `{"api_key":"hk-body","event":"e","distinct_id":"d"}`, nil, "hk-body"},
		{"query", "/v1/insights/e?api_key=hk-query", `{"event":"e","distinct_id":"d"}`, nil, "hk-query"},
		{"x-api-key", "/v1/insights/e", `{"event":"e","distinct_id":"d"}`, map[string]string{"x-api-key": "hk-hdr"}, "hk-hdr"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := stubResolver(t, func(string) (string, bool) { return "acme", true })
			code := postKeyed(t, app, tc.path, "", tc.body, tc.hdr)
			if *got != tc.wantKey {
				t.Fatalf("resolver handed key %q, want %q", *got, tc.wantKey)
			}
			if code == http.StatusForbidden {
				t.Fatalf("a resolved key must pass the tenant gate, got 403")
			}
		})
	}
}

// TestKeyOrg_UnresolvableKeyFailsClosed: a PRESENTED key that does not
// resolve is refused (403) EVEN on a recognized brand host — it must never fall
// through to a brand's partition (that would be a cross-tenant write).
func TestKeyOrg_UnresolvableKeyFailsClosed(t *testing.T) {
	app := mountApp(t)
	stubResolver(t, func(string) (string, bool) { return "", false }) // nothing resolves
	code := postKeyed(t, app, "/v1/insights/e", "hanzo.ai",
		`{"api_key":"hk-bad","event":"e","distinct_id":"d"}`, nil)
	if code != http.StatusForbidden {
		t.Fatalf("presented-but-unresolvable key on a brand host must 403 (fail closed), got %d", code)
	}
}

// TestKeyOrg_KeylessRequestNeverConsultsResolver: with NO key presented the key
// resolver is never consulted — the key path triggers only on a real key — and the
// request is not refused either: it takes the anonymous lane, where its custom event
// kind is dropped and reported honestly (200), never stored. It used to be admitted
// here at FULL capability into the brand org named by the Host.
func TestKeyOrg_KeylessRequestNeverConsultsResolver(t *testing.T) {
	tightenPublicRate(t, 1_000_000, 1_000_000)
	app := mountApp(t)
	stubResolver(t, func(key string) (string, bool) {
		t.Fatalf("resolver consulted for a keyless request (key=%q)", key)
		return "", false
	})
	code := postKeyed(t, app, "/v1/insights/e", "hanzo.ai",
		`{"event":"e","distinct_id":"d"}`, nil)
	if code != http.StatusOK {
		t.Fatalf("keyless PostHog event want 200 (anonymous lane, kind dropped), got %d", code)
	}
}
