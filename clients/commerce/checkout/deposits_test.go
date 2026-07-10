package checkout

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ─── Auth required ──────────────────────────────────────────────────────

func TestDeposits_RequiresAuthHeader(t *testing.T) {
	r := NewStaticResolver(map[string]Tenant{
		"pay.example.com": {
			Name:    "examplecorp",
			Backend: BackendConfig{Kind: "bd", URL: "https://bd.example.com"},
		},
	})
	h := Deposits(r, stubForwarder(t))

	req := httptest.NewRequest(http.MethodPost, "http://pay.example.com/checkout/v1/deposits", strings.NewReader(`{"amount_cents":1000}`))
	req.Host = "pay.example.com"
	req.Header.Set("Content-Type", "application/json")
	// No Authorization header.
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 without Authorization", w.Code)
	}
}

// ─── Host-header required ───────────────────────────────────────────────

func TestDeposits_UnknownTenantReturns404(t *testing.T) {
	r := NewStaticResolver(map[string]Tenant{
		"pay.example.com": {
			Name:    "examplecorp",
			Backend: BackendConfig{Kind: "bd", URL: "https://bd.example.com"},
		},
	})
	h := Deposits(r, stubForwarder(t))

	req := httptest.NewRequest(http.MethodPost, "http://evil.com/checkout/v1/deposits", strings.NewReader(`{}`))
	req.Host = "evil.com"
	req.Header.Set("Authorization", "Bearer fake")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for unknown tenant", w.Code)
	}
}

// ─── Proxy target and auth forwarding ───────────────────────────────────

func TestDeposits_ForwardsToTenantBackend(t *testing.T) {
	captured := struct {
		url     string
		method  string
		auth    string
		body    string
		tenant  string
	}{}

	fwd := ForwarderFunc(func(req *http.Request, tenant Tenant) (*http.Response, error) {
		body, _ := io.ReadAll(req.Body)
		captured.url = req.URL.String()
		captured.method = req.Method
		captured.auth = req.Header.Get("Authorization")
		captured.body = string(body)
		captured.tenant = tenant.Name
		return &http.Response{
			StatusCode: 201,
			Body:       io.NopCloser(bytes.NewReader([]byte(`{"id":"dep_123","provider":"square","clientToken":"cbt_..."}`))),
			Header:     http.Header{"Content-Type": []string{"application/json"}},
		}, nil
	})

	r := NewStaticResolver(map[string]Tenant{
		"pay.example.com": {
			Name:    "examplecorp",
			Backend: BackendConfig{Kind: "bd", URL: "https://bd.example.com"},
		},
	})
	h := Deposits(r, fwd)

	req := httptest.NewRequest(http.MethodPost, "http://pay.example.com/checkout/v1/deposits",
		strings.NewReader(`{"amount_cents":1000,"method":"card"}`))
	req.Host = "pay.example.com"
	req.Header.Set("Authorization", "Bearer user-jwt")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != 201 {
		t.Fatalf("status = %d, want 201", w.Code)
	}
	if captured.tenant != "examplecorp" {
		t.Errorf("tenant = %q, want examplecorp", captured.tenant)
	}
	if captured.method != http.MethodPost {
		t.Errorf("method = %q, want POST", captured.method)
	}
	// Must proxy to the tenant's configured backend URL — NOT the
	// original Host. Otherwise an attacker could SSRF via Host spoofing.
	if !strings.HasPrefix(captured.url, "https://bd.example.com/") {
		t.Errorf("url = %q, want prefix https://bd.example.com/", captured.url)
	}
	// Bearer token must be forwarded so the backend can attribute the
	// deposit to a real IAM user.
	if captured.auth != "Bearer user-jwt" {
		t.Errorf("auth forwarded = %q, want Bearer user-jwt", captured.auth)
	}
	// Body must be forwarded verbatim.
	if !strings.Contains(captured.body, "amount_cents") {
		t.Errorf("body not forwarded — got %q", captured.body)
	}
}

// ─── Don't proxy when backend is unset ──────────────────────────────────

// Tenant config without a Backend URL is a misconfiguration; the handler
// must fail closed rather than fall back to some default.
func TestDeposits_FailsClosedOnMissingBackend(t *testing.T) {
	r := NewStaticResolver(map[string]Tenant{
		"pay.example.com": {
			Name: "examplecorp",
			// No Backend configured.
		},
	})
	h := Deposits(r, stubForwarder(t))

	req := httptest.NewRequest(http.MethodPost, "http://pay.example.com/checkout/v1/deposits", strings.NewReader(`{}`))
	req.Host = "pay.example.com"
	req.Header.Set("Authorization", "Bearer u")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 for tenant without backend", w.Code)
	}
}

// stubForwarder fails the test if the forwarder is ever called. Used in
// tests where we expect the handler to short-circuit before forwarding.
func stubForwarder(t *testing.T) Forwarder {
	t.Helper()
	return ForwarderFunc(func(req *http.Request, tenant Tenant) (*http.Response, error) {
		t.Fatalf("forwarder called unexpectedly: %s %s", req.Method, req.URL)
		return nil, nil
	})
}
