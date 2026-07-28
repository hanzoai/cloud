package integrations

// anthropic_oauth_test.go proves the Claude Pro/Max SUBSCRIPTION leg of
// anthropic.go against httptest stand-ins for both origins it talks to
// (ANTHROPIC_API_BASE for the usage probe, ANTHROPIC_OAUTH_BASE for the token
// endpoint; zero live network):
//
//   - Adopt proves liveness with a READ, never a rotation — the operator's local
//     CLI session must survive connecting.
//   - Adopt lands the access token in Secrets[0], so fresh()'s custody read and
//     the sk-ant-oat01- header rule both cover it with no second branch.
//   - A scope-less token (403) is refused and NOTHING is stored.
//   - Refresh demands the rotated refresh token; a response that omits it is a
//     failure, never a silent keep-the-old-one.
//   - No error on any path echoes the credential.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const (
	antOAuthAccess  = "sk-ant-oat01-" + "ACCESSaccessACCESSaccess0123456789abcdefghijklmnop"
	antOAuthRefresh = "sk-ant-ort01-" + "REFRESHrefreshREFRESHrefresh0123456789abcdefghijkl"
)

// antUsageMock stands in for GET /api/oauth/usage.
type antUsageMock struct {
	status int // 0 => 200
	calls  int
	auth   string
	beta   string
}

func newAntUsageMock(t *testing.T, m *antUsageMock) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/oauth/usage" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		m.calls++
		m.auth = r.Header.Get("Authorization")
		m.beta = r.Header.Get("anthropic-beta")
		if m.status != 0 {
			w.WriteHeader(m.status)
			_, _ = w.Write([]byte(`{"error":{"message":"scope requirement user:profile"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"five_hour":{"utilization":51,"resets_at":"2026-07-28T07:30:00Z"}}`))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("ANTHROPIC_API_BASE", srv.URL)
	return srv
}

// antTokenMock stands in for POST /v1/oauth/token.
type antTokenMock struct {
	status int    // 0 => 200
	body   string // raw response body; "" => a well-formed rotation
	calls  int
	form   string
}

func newAntTokenMock(t *testing.T, m *antTokenMock) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/oauth/token" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		m.calls++
		_ = r.ParseForm()
		m.form = r.Form.Encode()
		if m.status != 0 {
			w.WriteHeader(m.status)
			_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"refresh token revoked"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if m.body != "" {
			_, _ = w.Write([]byte(m.body))
			return
		}
		_, _ = w.Write([]byte(`{"access_token":"sk-ant-oat01-ROTATED","refresh_token":"sk-ant-ort01-ROTATED","expires_in":28800}`))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("ANTHROPIC_OAUTH_BASE", srv.URL)
	return srv
}

// TestAnthropicAdoptProvesWithReadNotRotation is the load-bearing one: adopting
// a bundle must not spend the refresh token, or connecting cloud would kill the
// operator's still-live local Claude Code session.
func TestAnthropicAdoptProvesWithReadNotRotation(t *testing.T) {
	usage := &antUsageMock{}
	newAntUsageMock(t, usage)
	token := &antTokenMock{}
	newAntTokenMock(t, token)

	res, err := anthropicAdopt(context.Background(), Bundle{
		Access:  antOAuthAccess,
		Refresh: antOAuthRefresh,
		Account: "z@hanzo.ai",
	})
	if err != nil {
		t.Fatalf("adopt: %v", err)
	}
	if usage.calls != 1 {
		t.Fatalf("usage probe calls = %d, want exactly 1", usage.calls)
	}
	if token.calls != 0 {
		t.Fatalf("token endpoint calls = %d, want 0 — adopt must not rotate", token.calls)
	}
	if got := usage.auth; got != "Bearer "+antOAuthAccess {
		t.Fatalf("probe Authorization header = %q, want the bearer access token", got)
	}
	if usage.beta != anthropicOAuthBeta {
		t.Fatalf("probe anthropic-beta = %q, want %q", usage.beta, anthropicOAuthBeta)
	}

	// Secrets[0] is the ONE access slot fresh() reads; the OAuth access token
	// must land there, not in a second oauth-only slot.
	p, ok := registry[anthropicProvider]
	if !ok {
		t.Fatal("anthropic provider not registered")
	}
	if got := res.Tokens[p.Secrets[0]]; got != antOAuthAccess {
		t.Fatalf("Secrets[0] (%q) = %q, want the access token", p.Secrets[0], got)
	}
	if got := res.Tokens[refreshSecret]; got != antOAuthRefresh {
		t.Fatalf("refresh slot = %q, want the refresh token", got)
	}
	// A non-zero expiry is what arms rotation; 0 would mean "static, never
	// rotate" and strand the connector at the first expiry.
	if res.ExpiresAt == 0 {
		t.Fatal("ExpiresAt = 0, want a non-zero expiry so fresh() rotates")
	}
	if res.ExternalID != "z@hanzo.ai" {
		t.Fatalf("ExternalID = %q, want the caller hint", res.ExternalID)
	}
}

// TestAnthropicAdoptRefusesScopelessToken pins that a setup token — which
// authenticates but lacks user:profile — is refused by the subscription path
// and nothing is returned for custody.
func TestAnthropicAdoptRefusesScopelessToken(t *testing.T) {
	newAntUsageMock(t, &antUsageMock{status: http.StatusForbidden})
	token := &antTokenMock{}
	newAntTokenMock(t, token)

	res, err := anthropicAdopt(context.Background(), Bundle{Access: antOAuthAccess, Refresh: antOAuthRefresh})
	if err == nil {
		t.Fatal("adopt accepted a token lacking user:profile")
	}
	if res != nil {
		t.Fatal("adopt returned material to custody on a refused credential")
	}
	if !strings.Contains(err.Error(), "user:profile") {
		t.Fatalf("error %q does not name the missing scope", err)
	}
	assertAnthropicTokenFree(t, err)
	if token.calls != 0 {
		t.Fatalf("token endpoint calls = %d, want 0", token.calls)
	}
}

// TestAnthropicAdoptRequiresBothHalves pins the offline reject: a bundle
// missing either half never reaches the network.
func TestAnthropicAdoptRequiresBothHalves(t *testing.T) {
	usage := &antUsageMock{}
	newAntUsageMock(t, usage)

	for name, b := range map[string]Bundle{
		"no refresh": {Access: antOAuthAccess},
		"no access":  {Refresh: antOAuthRefresh},
		"neither":    {},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := anthropicAdopt(context.Background(), b); err == nil {
				t.Fatal("adopt accepted an incomplete bundle")
			}
		})
	}
	if usage.calls != 0 {
		t.Fatalf("usage probe calls = %d, want 0 — an incomplete bundle is an offline reject", usage.calls)
	}
}

// TestAnthropicRefreshRotates pins the refresh wire shape and that the observed
// expires_in wins over the assumed intake TTL.
func TestAnthropicRefreshRotates(t *testing.T) {
	token := &antTokenMock{}
	newAntTokenMock(t, token)

	res, err := anthropicRefresh(context.Background(), antOAuthRefresh)
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	for _, want := range []string{"grant_type=refresh_token", "client_id=" + anthropicOAuthClientID} {
		if !strings.Contains(token.form, want) {
			t.Fatalf("refresh form %q missing %q", token.form, want)
		}
	}
	// The refresh grant carries neither scope nor redirect_uri.
	for _, absent := range []string{"scope=", "redirect_uri="} {
		if strings.Contains(token.form, absent) {
			t.Fatalf("refresh form %q must not carry %q", token.form, absent)
		}
	}
	p := registry[anthropicProvider]
	if got := res.Tokens[p.Secrets[0]]; got != "sk-ant-oat01-ROTATED" {
		t.Fatalf("rotated access = %q", got)
	}
	if got := res.Tokens[refreshSecret]; got != "sk-ant-ort01-ROTATED" {
		t.Fatalf("rotated refresh = %q — custody must own the NEW token", got)
	}
	// expires_in=28800 must beat the 1h intake assumption.
	if delta := res.ExpiresAt - time.Now().Unix(); delta < 28000 {
		t.Fatalf("expiry delta = %ds, want ~28800 from the observed expires_in", delta)
	}
}

// TestAnthropicRefreshRejectsUnrotatedResponse pins the fail-closed rule: the
// server always rotates, so a response without a new refresh token is a
// failure. Silently keeping the old one would custody a token upstream has
// just invalidated.
func TestAnthropicRefreshRejectsUnrotatedResponse(t *testing.T) {
	newAntTokenMock(t, &antTokenMock{body: `{"access_token":"sk-ant-oat01-NEW","expires_in":28800}`})

	if _, err := anthropicRefresh(context.Background(), antOAuthRefresh); err == nil {
		t.Fatal("refresh accepted a response with no rotated refresh token")
	}
}

// TestAnthropicRefreshSurfacesProtocolError pins that upstream's error reaches
// the operator without the credential riding along.
func TestAnthropicRefreshSurfacesProtocolError(t *testing.T) {
	newAntTokenMock(t, &antTokenMock{status: http.StatusBadRequest})

	_, err := anthropicRefresh(context.Background(), antOAuthRefresh)
	if err == nil {
		t.Fatal("refresh accepted a 400")
	}
	if !strings.Contains(err.Error(), "invalid_grant") {
		t.Fatalf("error %q drops the upstream protocol code", err)
	}
	assertAnthropicTokenFree(t, err)
}

// TestAnthropicProviderDeclaresBothLegs pins the registration: the subscription
// legs exist AND the static Verify leg still does, so one connector id serves
// all three credential flavours.
func TestAnthropicProviderDeclaresBothLegs(t *testing.T) {
	p, ok := registry[anthropicProvider]
	if !ok {
		t.Fatal("anthropic provider not registered")
	}
	if p.Verify == nil || p.Adopt == nil || p.Refresh == nil {
		t.Fatalf("anthropic legs: Verify=%t Adopt=%t Refresh=%t, want all three",
			p.Verify != nil, p.Adopt != nil, p.Refresh != nil)
	}
	if p.Secrets[0] != anthropicKeySecret {
		t.Fatalf("Secrets[0] = %q, want %q — fresh() reads Secrets[0] as THE access slot",
			p.Secrets[0], anthropicKeySecret)
	}
	// The refresh slot must be custodied so disconnect deletes it.
	var hasRefresh bool
	for _, s := range p.Secrets {
		if s == refreshSecret {
			hasRefresh = true
		}
	}
	if !hasRefresh {
		t.Fatalf("Secrets = %v, missing %q — disconnect would strand it", p.Secrets, refreshSecret)
	}
}

// TestAnthropicStaticVerifyStaysUnrotatable pins that adding the subscription
// legs did NOT arm rotation for API keys: Verify still returns ExpiresAt 0, and
// fresh() reads that as "static, plain custody read".
func TestAnthropicStaticVerifyStaysUnrotatable(t *testing.T) {
	m := &antMock{}
	srv := newAntMock(t, m)
	_ = srv

	res, err := anthropicVerify(context.Background(), VerifyInput{Token: antAPIKey})
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if res.ExpiresAt != 0 {
		t.Fatalf("ExpiresAt = %d, want 0 — an API key must never be rotated", res.ExpiresAt)
	}
	if _, ok := res.Tokens[refreshSecret]; ok {
		t.Fatal("verify custodied a refresh token for a static credential")
	}
}

// assertAnthropicTokenFree fails if an error message leaks credential material.
func assertAnthropicTokenFree(t *testing.T, err error) {
	t.Helper()
	for _, secret := range []string{antOAuthAccess, antOAuthRefresh} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("error leaks a credential: %v", err)
		}
	}
}
