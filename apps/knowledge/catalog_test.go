package knowledge

import (
	"encoding/json"
	"github.com/zap-proto/zip"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/internal/sqltest"
)

// TestCatalog_ListsNativeAndScripted proves the ONE catalog surface returns BOTH the
// first-party Go connectors (github/slack/google, kind "native") AND the long-tail
// activepieces connectors (notion, kind "provider") in a single list — the user picks
// from one list and does not care which is Go and which is JS.
func TestCatalog_ListsNativeAndScripted(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountKB(t)
		code, body := req(t, app, http.MethodGet, "/v1/knowledge/connectors/catalog", "acme", nil)
		if code != http.StatusOK {
			t.Fatalf("catalog: %d %s", code, body)
		}
		var resp struct {
			Connectors []struct {
				Provider string `json:"provider"`
				Kind     string `json:"kind"`
			} `json:"connectors"`
		}
		if err := json.Unmarshal(body, &resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		kinds := map[string]string{}
		for _, c := range resp.Connectors {
			kinds[c.Provider] = c.Kind
		}
		// Native Go connectors.
		for _, p := range []string{"github", "slack", "google"} {
			if kinds[p] != "native" {
				t.Errorf("%s kind = %q, want native", p, kinds[p])
			}
		}
		// Long-tail provider connector.
		if kinds["notion"] != "provider" {
			t.Errorf("notion kind = %q, want provider", kinds["notion"])
		}
		// One list holds both worlds.
		if len(resp.Connectors) < 4 {
			t.Errorf("catalog has %d entries, want >= 4 (3 native + notion)", len(resp.Connectors))
		}
	})
}

// TestCatalog_RequiresPrincipal proves the catalog is refused without a validated
// principal (no X-User-Id) — an anonymous caller gets 401, not the catalog.
func TestCatalog_RequiresPrincipal(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountKB(t)
		hr := httptest.NewRequest(http.MethodGet, "/v1/knowledge/connectors/catalog", strings.NewReader(""))
		hr.Header.Set("X-Org-Id", "acme") // forged org, no principal (no X-User-Id)
		resp, err := app.Test(hr, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
		if err != nil {
			t.Fatalf("test: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("catalog without principal: %d, want 401", resp.StatusCode)
		}
	})
}

// TestKindOf pins the native/provider classification.
func TestKindOf(t *testing.T) {
	for _, p := range []string{"github", "slack", "google"} {
		if kindOf(p) != "native" {
			t.Errorf("kindOf(%q) = %q, want native", p, kindOf(p))
		}
	}
	if kindOf("notion") != "provider" {
		t.Errorf("kindOf(notion) = %q, want provider", kindOf("notion"))
	}
}
