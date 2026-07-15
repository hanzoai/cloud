// Copyright © 2026 Hanzo AI. MIT License.

package subsystems

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// TestCommercePrefixesPinned pins the wire paths that MUST reach the commerce gin
// handler — the ones a missing prefix silently regresses because they otherwise
// fall through to another owner (the account-bridge /v1/billing/* catch-all, or
// the bare /v1/* AI catch-all) that answers with the wrong contract:
//
//   - /v1/billing/webhooks       provider HMAC is the auth (Square et al); the
//     session-gated bridge would 403 it.
//   - /v1/billing/auto-recharge  the durable cron's billing-autorecharge poke
//     (COMMERCE_SERVICE_TOKEN bearer); without the prefix the poke 403s at the
//     bridge ("sign in to view billing") — exactly how the first live fires
//     failed, and how the commerce unfork regressed it once already.
//   - /v1/store                  the bare storefront surface (GET /v1/store/current +
//     the listing upsert/reads). Dropped by the unfork, it matched no owner and fell
//     to the /v1/* AI balance gate, which 402'd every store read for a commerce-funded
//     org (the karma /v1/store/current outage). It is metadata, never LLM inference.
func TestCommercePrefixesPinned(t *testing.T) {
	want := map[string]bool{
		"/v1/billing/auto-recharge": false,
		"/v1/billing/webhooks":      false,
		"/v1/store":                 false,
	}
	for _, p := range commercePrefixes {
		if _, ok := want[p]; ok {
			want[p] = true
		}
	}
	for p, ok := range want {
		if !ok {
			t.Errorf("commercePrefixes missing %q — the route falls through to the wrong owner", p)
		}
	}
}

// TestStoreSurfaceRoutedToCommerceNotAIGate is the regression guard for the karma
// GET /v1/store/current → 402 outage. Because "/v1/store" is a commercePrefix, a
// store read is mounted on the commerce handler AHEAD of the bare /v1/* AI catch-all
// (Wire order: commerce@191 < ai@321; Fiber matches first-registered), so it resolves
// on commerce and NEVER reaches the LLM prepaid-balance gate that denied every store
// read for an org funded in commerce but $0 in the ai ledger.
func TestStoreSurfaceRoutedToCommerceNotAIGate(t *testing.T) {
	app := zip.New(zip.Config{Logger: luxlog.New("test")})

	// Mirror mountCommerce EXACTLY: app.All(prefix+"/*", handler) for each commerce
	// prefix. The stub stands in for the embedded commerce gin handler that serves
	// getCurrent (200 with the org's store).
	commerce := zip.AdaptNetHTTP(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"store":{"id":"karma-store"}}`)
	}))
	for _, p := range commercePrefixes {
		app.All(p+"/*", commerce)
	}

	// The bare /v1/* AI catch-all, mounted LAST like the real Wire, gates every
	// non-exempt /v1/* path on the caller's LLM prepaid balance → 402 for a
	// commerce-funded-but-ai-$0 org. This is the exact gate that produced the outage.
	app.Get("/v1/*", func(c *zip.Ctx) error {
		return c.JSON(http.StatusPaymentRequired, map[string]any{
			"error": map[string]string{"code": "insufficient_balance"},
		})
	})

	// The store read must resolve on the commerce handler (200), never 402 at the AI gate.
	code, body := doReq(t, app, http.MethodGet, "/v1/store/current")
	if code != http.StatusOK {
		t.Fatalf("GET /v1/store/current hit the AI balance gate (got %d, body %s) — /v1/store must be a commercePrefix so it reaches commerce, not the /v1/* catch-all", code, body)
	}

	// And a sibling store path (the listing upsert the publish edge writes) is owned too.
	if code, _ := doReq(t, app, http.MethodPut, "/v1/store/karma-store/listing/valentina"); code == http.StatusPaymentRequired {
		t.Fatalf("PUT /v1/store/:id/listing/:slug fell through to the AI balance gate (402) — the whole store surface must be commerce-owned")
	}
}

// doReq drives one request through the mounted app and returns (status, body).
func doReq(t *testing.T, app *zip.App, method, path string) (int, []byte) {
	t.Helper()
	resp, err := app.Fiber().Test(httptest.NewRequest(method, path, nil))
	if err != nil {
		t.Fatalf("Test %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}
