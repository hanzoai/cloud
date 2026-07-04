package kb

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestCatalog_ListsNativeAndPiece proves the ONE catalog surface returns BOTH the
// first-party Go connectors (github/slack/google, kind "native") AND the long-tail
// activepieces connectors (notion, kind "piece") in a single list — the user picks
// from one list and does not care which is Go and which is JS.
func TestCatalog_ListsNativeAndPiece(t *testing.T) {
	app := mountKB(t)
	code, body := req(t, app, http.MethodGet, "/v1/kb/connectors/catalog", "acme", nil)
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
	// Long-tail piece connector.
	if kinds["notion"] != "piece" {
		t.Errorf("notion kind = %q, want piece", kinds["notion"])
	}
	// One list holds both worlds.
	if len(resp.Connectors) < 4 {
		t.Errorf("catalog has %d entries, want >= 4 (3 native + notion)", len(resp.Connectors))
	}
}

// TestCatalog_RequiresPrincipal proves the catalog is refused without a validated
// principal (no X-User-Id) — an anonymous caller gets 403, not the catalog.
func TestCatalog_RequiresPrincipal(t *testing.T) {
	app := mountKB(t)
	hr := httptest.NewRequest(http.MethodGet, "/v1/kb/connectors/catalog", strings.NewReader(""))
	hr.Header.Set("X-Org-Id", "acme") // forged org, no principal (no X-User-Id)
	resp, err := app.Fiber().Test(hr)
	if err != nil {
		t.Fatalf("test: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("catalog without principal: %d, want 403", resp.StatusCode)
	}
}

// TestKindOf pins the native/piece classification.
func TestKindOf(t *testing.T) {
	for _, p := range []string{"github", "slack", "google"} {
		if kindOf(p) != "native" {
			t.Errorf("kindOf(%q) = %q, want native", p, kindOf(p))
		}
	}
	if kindOf("notion") != "piece" {
		t.Errorf("kindOf(notion) = %q, want piece", kindOf("notion"))
	}
}
