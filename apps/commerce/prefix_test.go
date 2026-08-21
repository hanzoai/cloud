// Copyright © 2026 Hanzo AI. MIT License.

package commerce

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
// fall through to the bare /v1/* AI catch-all, which answers with the wrong
// contract. (Until the account-bridge /v1/billing/* forwarder was retired there
// was a second wrong owner in front of it, whose session gate 403'd instead; the
// two failures below are recorded under the answer they actually gave.)
//
//   - /v1/billing/webhooks       provider HMAC is the auth (Square et al); the
//     session-gated bridge 403'd it.
//   - /v1/billing/recharge  the durable cron's billing-autorecharge poke
//     (COMMERCE_SERVICE_TOKEN bearer); without the prefix the poke 403'd at the
//     bridge ("sign in to view billing") — exactly how the first live fires
//     failed, and how the commerce unfork regressed it once already.
//   - /v1/commerce                the merchant root, and every noun under it:
//     the storefront (GET /v1/commerce/store/current + the listing upsert/reads),
//     the SuperAdmin catalog and plan CRUD, the cart and the typed payment door.
//     The storefront read is the one that proved it — dropped by the unfork it
//     matched no owner, fell to the /v1/* AI balance gate and 402'd every store
//     read for a commerce-funded org (the karma outage). It is metadata, never
//     LLM inference. One root claims the family; the routing test below drives
//     the real paths rather than trusting this list to enumerate them.
func TestCommercePrefixesPinned(t *testing.T) {
	want := map[string]bool{
		"/v1/billing/recharge": false,
		"/v1/billing/webhooks": false,
		"/v1/commerce":         false,
	}
	for _, p := range Prefixes {
		if _, ok := want[p]; ok {
			want[p] = true
		}
	}
	for p, ok := range want {
		if !ok {
			t.Errorf("Prefixes missing %q — the route falls through to the wrong owner", p)
		}
	}
}

// TestStoreSurfaceRoutedToCommerceNotAIGate is the regression guard for the karma
// GET /v1/commerce/store/current → 402 outage. Because "/v1/commerce/store" is a commercePrefix, a
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
	for _, p := range Prefixes {
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
	code, body := doReq(t, app, http.MethodGet, "/v1/commerce/store/current")
	if code != http.StatusOK {
		t.Fatalf("GET /v1/commerce/store/current hit the AI balance gate (got %d, body %s) — /v1/commerce/store must be a commercePrefix so it reaches commerce, not the /v1/* catch-all", code, body)
	}

	// And a sibling store path (the listing upsert the publish edge writes) is owned too.
	if code, _ := doReq(t, app, http.MethodPut, "/v1/commerce/store/karma-store/listing/valentina"); code == http.StatusPaymentRequired {
		t.Fatalf("PUT /v1/commerce/store/:id/listing/:slug fell through to the AI balance gate (402) — the whole store surface must be commerce-owned")
	}

	// The platform-admin catalog CMS (admin.hanzo.ai's editor) must reach commerce —
	// where each handler is requireSuperAdmin-gated (anon → 401/403) — never the AI
	// /v1/* balance gate, which would 402 the editor's list/edit instead.
	if code, _ := doReq(t, app, http.MethodGet, "/v1/commerce/catalog/entries"); code == http.StatusPaymentRequired {
		t.Fatalf("GET /v1/commerce/catalog/entries fell through to the AI balance gate (402) — /v1/commerce/catalog must be a commercePrefix so the SuperAdmin CMS reaches commerce")
	}
	if code, _ := doReq(t, app, http.MethodPut, "/v1/commerce/catalog/entries/cloud-starter"); code == http.StatusPaymentRequired {
		t.Fatalf("PUT /v1/commerce/catalog/entries/:slug fell through to the AI balance gate (402) — the whole catalog CMS must be commerce-owned")
	}

	// The platform-admin plan authority CMS (increment 3a) must also reach commerce —
	// requireSuperAdmin-gated (anon → 401/403) — never the AI /v1/* 402 gate.
	if code, _ := doReq(t, app, http.MethodGet, "/v1/commerce/plans/entries"); code == http.StatusPaymentRequired {
		t.Fatalf("GET /v1/commerce/plans/entries fell through to the AI balance gate (402) — /v1/commerce/plans must be a commercePrefix so the plan authority CMS reaches commerce")
	}
	if code, _ := doReq(t, app, http.MethodPut, "/v1/commerce/plans/entries/pro"); code == http.StatusPaymentRequired {
		t.Fatalf("PUT /v1/commerce/plans/entries/:slug fell through to the AI balance gate (402) — the whole plan authority CMS must be commerce-owned")
	}
}

// doReq drives one request through the mounted app and returns (status, body).
func doReq(t *testing.T, app *zip.App, method, path string) (int, []byte) {
	t.Helper()
	resp, err := app.Test(httptest.NewRequest(method, path, nil))
	if err != nil {
		t.Fatalf("Test %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}
