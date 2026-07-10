// Package checkout tests — tenant resolver must withstand Host-header
// spoofing and never leak secrets through the public tenant JSON endpoint.
package checkout

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ─── Resolver ────────────────────────────────────────────────────────────

func TestResolveTenant_KnownHostname(t *testing.T) {
	r := NewStaticResolver(map[string]Tenant{
		"pay.example.com":      {Name: "examplecorp", Brand: Brand{DisplayName: "ExampleCorp"}},
		"pay.dev.example.com":  {Name: "examplecorp", Brand: Brand{DisplayName: "ExampleCorp"}},
		"pay.test.example.com": {Name: "examplecorp", Brand: Brand{DisplayName: "ExampleCorp"}},
	})

	cases := []struct {
		host, want string
	}{
		{"pay.example.com", "examplecorp"},
		{"pay.dev.example.com", "examplecorp"},
		{"pay.test.example.com", "examplecorp"},
		// Port suffix must be stripped before lookup.
		{"pay.example.com:443", "examplecorp"},
		// Case-insensitive.
		{"PAY.EXAMPLE.COM", "examplecorp"},
	}

	for _, tc := range cases {
		got, err := r.Resolve(tc.host)
		if err != nil {
			t.Errorf("Resolve(%q) err = %v, want nil", tc.host, err)
			continue
		}
		if got.Name != tc.want {
			t.Errorf("Resolve(%q).Name = %q, want %q", tc.host, got.Name, tc.want)
		}
	}
}

func TestResolveTenant_UnknownHostname(t *testing.T) {
	r := NewStaticResolver(map[string]Tenant{
		"pay.example.com": {Name: "examplecorp"},
	})

	// Arbitrary unrelated hosts MUST NOT match.
	for _, host := range []string{
		"evil.com",
		"pay.evil.com",
		"example.com.evil.com", // suffix-match attack
		"xyzpay.example.com",
		"",
	} {
		if _, err := r.Resolve(host); err != ErrUnknownTenant {
			t.Errorf("Resolve(%q) err = %v, want ErrUnknownTenant", host, err)
		}
	}
}

// An attacker setting a Host header like `pay.example.com.evil.com` must
// not be resolved as examplecorp. Exact-match only (after port/case
// normalization).
func TestResolveTenant_SuffixSpoofing(t *testing.T) {
	r := NewStaticResolver(map[string]Tenant{
		"pay.example.com": {Name: "examplecorp"},
	})
	spoofs := []string{
		"pay.example.com.attacker.test",
		"attacker.pay.example.com",
		" pay.example.com",
		"pay.example.com ",
	}
	for _, s := range spoofs {
		if _, err := r.Resolve(s); err != ErrUnknownTenant {
			t.Errorf("Resolve(%q) was accepted — expected rejection", s)
		}
	}
}

// ─── Public tenant JSON endpoint ─────────────────────────────────────────

// Public tenant JSON must include branding + public IAM config + enabled
// payment method NAMES, but MUST NEVER leak secrets: no access tokens, no
// client secrets, no webhook keys, no KMS paths.
func TestTenantJSON_NeverLeaksSecrets(t *testing.T) {
	tenant := Tenant{
		Name: "examplecorp",
		Brand: Brand{
			DisplayName:  "ExampleCorp",
			LogoURL:      "https://cdn.example.com/examplecorp.png",
			PrimaryColor: "#0ea5e9",
		},
		IAM: IAMConfig{
			Issuer:   "https://id.example.com",
			ClientID: "examplecorp-exchange-client-id",
			// These MUST NOT leak:
			ClientSecret: "secret-do-not-share",
			AdminSecret:  "even-more-secret",
		},
		Providers: []Provider{
			{Name: "square", Enabled: true, AccessToken: "EAAA-secret-token", WebhookSignatureKey: "whk-secret"},
			{Name: "braintree", Enabled: false, PrivateKey: "bt-secret"},
		},
		ReturnURLAllowlist: []string{"https://exchange.example.com"},
		Backend:            BackendConfig{URL: "https://bd.example.com", Kind: "bd"},
	}
	r := NewStaticResolver(map[string]Tenant{"pay.example.com": tenant})

	req := httptest.NewRequest(http.MethodGet, "http://pay.example.com/checkout/v1/tenant", nil)
	req.Host = "pay.example.com"
	w := httptest.NewRecorder()

	TenantJSON(r).ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	body := w.Body.String()

	// These strings MUST be absent from the payload.
	forbidden := []string{
		"secret-do-not-share",
		"even-more-secret",
		"EAAA-secret-token",
		"whk-secret",
		"bt-secret",
		"ClientSecret",
		"AdminSecret",
		"AccessToken",
		"PrivateKey",
		"WebhookSignatureKey",
	}
	for _, s := range forbidden {
		if strings.Contains(body, s) {
			t.Errorf("tenant JSON leaked %q — body:\n%s", s, body)
		}
	}

	// These fields MUST be present.
	required := []string{
		"examplecorp",
		"ExampleCorp",
		"#0ea5e9",
		"https://id.example.com",
		"examplecorp-exchange-client-id",
		"square",
	}
	for _, s := range required {
		if !strings.Contains(body, s) {
			t.Errorf("tenant JSON missing %q — body:\n%s", s, body)
		}
	}

	// Disabled providers must not show up in the enabled-methods list.
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	methods, _ := resp["providers"].([]any)
	for _, m := range methods {
		if p, ok := m.(map[string]any); ok {
			if name, _ := p["name"].(string); name == "braintree" {
				t.Errorf("disabled provider %q surfaced in public tenant JSON", name)
			}
		}
	}
}

func TestTenantJSON_UnknownHostReturns404(t *testing.T) {
	r := NewStaticResolver(map[string]Tenant{
		"pay.example.com": {Name: "examplecorp"},
	})
	req := httptest.NewRequest(http.MethodGet, "http://evil.com/checkout/v1/tenant", nil)
	req.Host = "evil.com"
	w := httptest.NewRecorder()
	TenantJSON(r).ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for unknown tenant", w.Code)
	}
	// 404 body MUST NOT echo the Host back — reflection would help
	// attackers fingerprint tenant existence.
	if strings.Contains(w.Body.String(), "evil.com") {
		t.Errorf("unknown-tenant 404 echoed Host header — body: %s", w.Body.String())
	}
}

// Host header with no mapping must not panic and must not 500.
func TestTenantJSON_MalformedHost(t *testing.T) {
	r := NewStaticResolver(map[string]Tenant{})
	for _, h := range []string{"", ":", "::8080", "\x00badhost"} {
		req := httptest.NewRequest(http.MethodGet, "http://x/checkout/v1/tenant", nil)
		req.Host = h
		w := httptest.NewRecorder()
		TenantJSON(r).ServeHTTP(w, req)
		if w.Code != http.StatusNotFound {
			t.Errorf("Host=%q: status = %d, want 404", h, w.Code)
		}
	}
}
