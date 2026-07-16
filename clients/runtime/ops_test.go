package runtime

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// TestRed_BotProxyForwardsForgedOrgNoPrincipal proves the /v1/bot/* proxy is NOT
// gated on a validated principal. It replays the exact state SanitizeIdentity
// leaves on the off-gateway forge path: X-Org-Id RESTORED to the client's forged
// value, X-User-Id EMPTY (no validated principal). A gated data-plane resolver
// (crm/kms/ml/...) answers 403 in this state. The bot proxy instead forwards the
// forged tenant to bot-gateway — which the package doc says trusts these headers
// as "the gateway-minted tenant context" — reopening the cross-tenant hole for
// every /v1/bot/* surface (billable chat, per-tenant channels/skills/agents).
//
// SECURE behavior (asserted here, FAILS today): a no-principal request must not
// hand bot-gateway a victim tenant — the proxy should 403, or at minimum strip
// the unvalidated identity so bot-gateway sees no forged org.
func TestRed_BotProxyForwardsForgedOrgNoPrincipal(t *testing.T) {
	var gotOrg atomic.Value
	gotOrg.Store("")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotOrg.Store(r.Header.Get("X-Org-Id"))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()
	t.Setenv("BOT_GATEWAY_URL", upstream.URL)

	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	if err := Mount(app, cloud.Deps{Logger: luxlog.New("test")}); err != nil {
		t.Fatalf("Mount: %v", err)
	}

	// The off-gateway forge, post-SanitizeIdentity: forged org, NO validated user.
	req := httptest.NewRequest(http.MethodGet, "/v1/bot/v1/models", nil)
	req.Header.Set("X-Org-Id", "victim") // forged; no X-User-Id → no validated principal
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("forged bot request: %v", err)
	}
	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("no-principal forged /v1/bot/* = HTTP %d, want 403 (proxy must gate like every data-plane resolver)", resp.StatusCode)
	}
	if org := gotOrg.Load().(string); org != "" {
		t.Errorf("bot-gateway received X-Org-Id=%q from a NO-PRINCIPAL forge — cross-tenant hole forwarded through cloud's /v1/bot proxy", org)
	}
}
