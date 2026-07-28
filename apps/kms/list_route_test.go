package kms_test

import (
	"encoding/json"
	"testing"
)

// TestListSecretsBareRouteReachable guards the exact `GET .../secrets` list
// route against being shadowed by the `GET .../secrets/*` value-read wildcard.
// The bare list path must reach listSecrets (200 + a secrets array), never fall
// through to getSecret with an empty name (400 "secret name is required").
func TestListSecretsBareRouteReachable(t *testing.T) {
	app, _ := newApp(t, baseCfg(t, masterKeyB64(t)))

	// Seed two secrets in the org so the list has content.
	for _, n := range []string{"API_KEY", "DB_URL"} {
		body, _ := json.Marshal(map[string]string{"name": n, "value": "v-" + n, "env": "main"})
		if resp := do(t, app, "POST", "/v1/kms/secrets", "hanzo", string(body), false, nil); resp.StatusCode != 200 {
			t.Fatalf("POST %s = %d, want 200: %s", n, resp.StatusCode, readAll(resp.Body))
		}
	}

	// The bare list path (no trailing name) must hit listSecrets, not getSecret.
	resp := do(t, app, "GET", "/v1/kms/secrets?env=main", "hanzo", "", false, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("GET bare /secrets (list) = %d, want 200 — the exact list route is shadowed by /secrets/* ; body: %s",
			resp.StatusCode, readAll(resp.Body))
	}
	got := decode(t, resp.Body)
	if _, ok := got["secrets"]; !ok {
		t.Fatalf("list response has no 'secrets' key: %v", got)
	}
	if total, _ := got["total"].(float64); total < 2 {
		t.Errorf("list total = %v, want >= 2 (both seeded secrets)", got["total"])
	}

	// Also confirm the trailing-slash form of the bare list still lists.
	resp = do(t, app, "GET", "/v1/kms/secrets/?env=main", "hanzo", "", false, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("GET /secrets/ (trailing slash, list) = %d, want 200: %s", resp.StatusCode, readAll(resp.Body))
	}

	// And the value-read wildcard still works for a real name.
	resp = do(t, app, "GET", "/v1/kms/secrets/API_KEY?env=main", "hanzo", "", false, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("GET /secrets/API_KEY (value read) = %d, want 200: %s", resp.StatusCode, readAll(resp.Body))
	}
	if v, _ := decode(t, resp.Body)["value"].(string); v != "v-API_KEY" {
		t.Errorf("value read = %q, want v-API_KEY", v)
	}
}
