package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// captureUpstream is a stub auto engine that records the headers it receives, so tests
// can assert exactly what the proxy forwarded — the auto engine trusts X-Org-Id
// absolutely, so what reaches here IS the tenant boundary.
func captureUpstream(t *testing.T) (*httptest.Server, func() http.Header) {
	t.Helper()
	var last http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		last = r.Header.Clone()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(srv.Close)
	return srv, func() http.Header { return last }
}

// TestGate_RefusesAnonymousForge is THE core tenant test: a request with a restored
// client X-Org-Id but NO validated principal (empty X-User-Id) — the exact
// off-gateway forge — must be refused with 403 and NEVER reach the engine.
func TestGate_RefusesAnonymousForge(t *testing.T) {
	up, _ := captureUpstream(t)
	h, err := NewHandler(up.URL)
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	gated := Gate(h)

	req := httptest.NewRequest(http.MethodGet, "/v1/auto/flows", nil)
	req.Header.Set("X-Org-Id", "victim") // client-forged org, no credential
	// X-User-Id deliberately absent — no validated principal.
	rec := httptest.NewRecorder()
	gated.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("forged anon request: status = %d, want 403", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "ok") {
		t.Error("forged request reached the engine (body indicates upstream hit)")
	}
}

// TestGate_AllowsValidatedPrincipal proves a request WITH a validated principal
// (X-User-Id present, set only by SanitizeIdentity from a verified credential) passes
// the gate and reaches the engine.
func TestGate_AllowsValidatedPrincipal(t *testing.T) {
	up, seen := captureUpstream(t)
	h, err := NewHandler(up.URL)
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	gated := Gate(h)

	req := httptest.NewRequest(http.MethodGet, "/v1/auto/flows", nil)
	req.Header.Set("X-User-Id", "user-123")
	req.Header.Set("X-Org-Id", "acme")
	rec := httptest.NewRecorder()
	gated.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("validated request: status = %d, want 200", rec.Code)
	}
	if got := seen().Get("X-Org-Id"); got != "acme" {
		t.Errorf("engine saw X-Org-Id = %q, want acme", got)
	}
}

// TestProxy_StripsSmuggledAuthorityHeaders proves the Director deletes every identity
// alias an attacker might smuggle and forwards ONLY the validated trust headers — so
// an engine that trusts headers absolutely can never be handed a forged X-User-IsAdmin
// / X-Tenant-Id / X-Org and the like.
func TestProxy_StripsSmuggledAuthorityHeaders(t *testing.T) {
	up, seen := captureUpstream(t)
	h, err := NewHandler(up.URL)
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	gated := Gate(h)

	req := httptest.NewRequest(http.MethodGet, "/v1/auto/connections", nil)
	req.Header.Set("X-User-Id", "user-123")
	req.Header.Set("X-Org-Id", "acme")
	// Attacker-smuggled aliases that MUST NOT reach the engine.
	req.Header.Set("X-User-IsAdmin", "true")
	req.Header.Set("X-Tenant-Id", "victim")
	req.Header.Set("X-Org", "victim")
	req.Header.Set("X-Roles", "admin")
	rec := httptest.NewRecorder()
	gated.ServeHTTP(rec, req)

	got := seen()
	for _, h := range []string{"X-User-Isadmin", "X-Tenant-Id", "X-Org", "X-Roles"} {
		if v := got.Get(h); v != "" {
			t.Errorf("smuggled header %s reached engine with value %q", h, v)
		}
	}
	// The validated org still reaches the engine.
	if got.Get("X-Org-Id") != "acme" {
		t.Errorf("validated X-Org-Id = %q, want acme", got.Get("X-Org-Id"))
	}
}

// TestProxy_PerOrg_ForwardsValidatedOrgOnly proves two different validated tenants each
// reach the engine tagged as THEMSELVES — the per-org boundary the engine relies on.
func TestProxy_PerOrg_ForwardsValidatedOrgOnly(t *testing.T) {
	up, seen := captureUpstream(t)
	h, _ := NewHandler(up.URL)
	gated := Gate(h)

	for _, org := range []string{"acme", "globex"} {
		req := httptest.NewRequest(http.MethodGet, "/v1/auto/flows", nil)
		req.Header.Set("X-User-Id", "u")
		req.Header.Set("X-Org-Id", org)
		rec := httptest.NewRecorder()
		gated.ServeHTTP(rec, req)
		if got := seen().Get("X-Org-Id"); got != org {
			t.Errorf("engine saw org %q, want %q", got, org)
		}
	}
}

// TestProxy_PreservesPath proves /v1/auto/* is forwarded UNCHANGED (the engine
// registers its routes at that exact path).
func TestProxy_PreservesPath(t *testing.T) {
	var gotPath string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer up.Close()
	h, _ := NewHandler(up.URL)
	gated := Gate(h)

	req := httptest.NewRequest(http.MethodPost, "/v1/auto/pieces/notion/run", nil)
	req.Header.Set("X-User-Id", "u")
	req.Header.Set("X-Org-Id", "acme")
	rec := httptest.NewRecorder()
	gated.ServeHTTP(rec, req)
	if gotPath != "/v1/auto/pieces/notion/run" {
		t.Errorf("engine saw path %q, want /v1/auto/pieces/notion/run", gotPath)
	}
	_ = io.Discard
}
