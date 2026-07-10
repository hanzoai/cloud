package functions

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// TestRed_OrgKeyExactIsolation is the REGRESSION GUARD for Red HIGH-1 (was
// TestRed_SanitizeOrgNotInjective + TestRed_CrossTenantViaOrgNormalization,
// which proved the vuln). The org key is now the EXACT validated org, so the
// collision classes Red exploited — case-fold, punctuation, '.'-vs-'-', 32-char
// truncation — no longer share a storage bucket. For every pair of DISTINCT org
// identifiers, a function created under one is invisible AND inaccessible to the
// other, and the victim's row survives the attacker's delete attempt.
func TestRed_OrgKeyExactIsolation(t *testing.T) {
	pairs := [][2]string{
		{"acme", "ACME"},             // case fold
		{"acme", "Acme"},             // case fold
		{"acme", "acme!"},            // trailing punct
		{"acme", "_acme_"},           // wrapping punct
		{"acme", ".acme."},           // dots
		{"team-alpha", "team.alpha"}, // '.' vs '-'
		{strings.Repeat("a", 33), strings.Repeat("a", 32) + "b"}, // 32-char truncation
		{strings.Repeat("x", 40) + "AAA", strings.Repeat("x", 40) + "BBB"},
	}
	for _, p := range pairs {
		victim, attacker := p[0], p[1]
		app := mountApp(t)
		if code, _ := do(t, app, http.MethodPost, "/v1/functions", victim,
			map[string]any{"name": "secret-fn", "runtime": "python", "code": "SENSITIVE"}); code != http.StatusCreated {
			t.Fatalf("[%q] victim create want 201, got %d", victim, code)
		}
		// Attacker (a DISTINCT org identifier) must see NOTHING.
		code, body := do(t, app, http.MethodGet, "/v1/functions", attacker, nil)
		if code != http.StatusOK {
			t.Fatalf("[%q] attacker list want 200, got %d", attacker, code)
		}
		var listed struct {
			Functions []functionView `json:"functions"`
		}
		_ = json.Unmarshal(body, &listed)
		if len(listed.Functions) != 0 {
			t.Errorf("CROSS-ORG LEAK: attacker %q sees victim %q's data: %+v", attacker, victim, listed.Functions)
		}
		// Attacker cannot GET or DELETE the victim's function.
		if code, _ := do(t, app, http.MethodGet, "/v1/functions/secret-fn", attacker, nil); code != http.StatusNotFound {
			t.Errorf("[%q->%q] attacker GET want 404, got %d", attacker, victim, code)
		}
		if code, _ := do(t, app, http.MethodDelete, "/v1/functions/secret-fn", attacker, nil); code != http.StatusNotFound {
			t.Errorf("[%q->%q] attacker DELETE want 404, got %d", attacker, victim, code)
		}
		// Victim's function survives the attacker's delete attempt.
		if code, _ := do(t, app, http.MethodGet, "/v1/functions/secret-fn", victim, nil); code != http.StatusOK {
			t.Errorf("[%q] victim function must survive, got %d", victim, code)
		}
	}
}

// TestRed_StaticRoutePrecedence (kept from Red — a positive guard): every static
// sub-route wins over :name in Fiber v3, and odd names never 500.
func TestRed_StaticRoutePrecedence(t *testing.T) {
	app := mountApp(t)
	for _, p := range []string{"/v1/functions/metrics", "/v1/functions/triggers",
		"/v1/functions/deployments", "/v1/functions/secrets"} {
		if code, body := do(t, app, http.MethodGet, p, "maxpower", nil); code != http.StatusOK {
			t.Errorf("%s want 200 (static), got %d (%s)", p, code, body)
		}
	}
	// DELETE /v1/functions/metrics -> :name del with name="metrics"; clean 404.
	if code, body := do(t, app, http.MethodDelete, "/v1/functions/metrics", "maxpower", nil); code != http.StatusNotFound {
		t.Errorf("DELETE /metrics want 404, got %d (%s)", code, body)
	}
	// Traversal-ish path segments: clean status, never 500/panic.
	for _, p := range []string{"/v1/functions/..", "/v1/functions/%2e%2e", "/v1/functions/%2F", "/v1/functions/.hidden"} {
		if code, body := do(t, app, http.MethodGet, p, "maxpower", nil); code == http.StatusInternalServerError {
			t.Errorf("%s produced 500: %s", p, body)
		}
	}
}
