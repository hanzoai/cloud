package integrations

// anthropic_test.go proves anthropic.go's wire protocol against an httptest
// stand-in for api.anthropic.com (ANTHROPIC_API_BASE seam; zero live network):
// setup-token vs API-key header selection on the live GET /v1/models verify,
// offline syntactic reject, verify-before-store, static (non-refreshable)
// custody, and (org,user) isolation.

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

var (
	antSetupToken = "sk-ant-oat01-" + strings.Repeat("a", 80) // >= 80 chars
	antAPIKey     = "sk-ant-api03-test-0123456789abcdef"
)

// antMock is a scriptable fake of api.anthropic.com's GET /v1/models.
type antMock struct {
	mu     sync.Mutex
	status int // 0 => 200
	seen   antSeen
}

// antSeen is the mutex-free capture of the last verify request.
type antSeen struct {
	calls                       int
	auth, apiKey, beta, version string
}

func (m *antMock) snapshot() antSeen {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.seen
}

func newAntMock(t *testing.T, m *antMock) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/models" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		m.mu.Lock()
		m.seen.calls++
		m.seen.auth = r.Header.Get("Authorization")
		m.seen.apiKey = r.Header.Get("x-api-key")
		m.seen.beta = r.Header.Get("anthropic-beta")
		m.seen.version = r.Header.Get("anthropic-version")
		status := m.status
		m.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if status != 0 {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"type":"error","error":{"type":"authentication_error"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("ANTHROPIC_API_BASE", srv.URL)
	return srv
}

func TestAnthropicSetupToken(t *testing.T) {
	m := &antMock{}
	newAntMock(t, m)
	kc := newKMS(t)
	app := newApp(t, kc)

	// Pasted setup tokens carry line wraps — ALL whitespace is stripped before
	// the syntactic gate and the live verify.
	spaced := "  sk-ant-oat01-" + strings.Repeat("a", 40) + "\n\t" + strings.Repeat("a", 40) + " \n"
	res := asOK(t, app, http.MethodPost, credPath("anthropic"), "acme", userEmail, map[string]any{"token": spaced})
	cr := decode[credResp](t, res)
	if !cr.Connected || cr.Connector == nil || cr.Connector.ID != "anthropic:default" {
		t.Fatalf("setup-token connect: %+v", cr)
	}
	got := m.snapshot()
	// Setup tokens verify as OAuth: Bearer + the oauth beta, NO x-api-key.
	if got.auth != "Bearer "+antSetupToken || got.beta != "oauth-2025-04-20" || got.version != "2023-06-01" || got.apiKey != "" {
		t.Fatalf("setup-token verify headers: %+v", got)
	}
	// ONE slot for both flavours; the normalized token is what custody holds.
	if v, err := userHas(t, kc, "acme", userEmail, "anthropic", "default", "api_key"); err != nil || string(v) != antSetupToken {
		t.Fatalf("api_key custody: %q err=%v", v, err)
	}
	if bytes.Contains(res.Body, []byte(antSetupToken)) {
		t.Fatalf("credential leaked into the connect response: %s", res.Body)
	}
}

func TestAnthropicAPIKey(t *testing.T) {
	m := &antMock{}
	newAntMock(t, m)
	kc := newKMS(t)
	app := newApp(t, kc)

	cr := decode[credResp](t, asOK(t, app, http.MethodPost, credPath("anthropic"), "acme", userUUID, map[string]any{"token": antAPIKey}))
	if cr.Connector == nil || cr.Connector.ID != "anthropic:default" {
		t.Fatalf("api-key connect: %+v", cr)
	}
	got := m.snapshot()
	// API keys verify with x-api-key: NO Authorization, NO oauth beta.
	if got.apiKey != antAPIKey || got.auth != "" || got.beta != "" || got.version != "2023-06-01" {
		t.Fatalf("api-key verify headers: %+v", got)
	}
	if v, err := userHas(t, kc, "acme", userUUID, "anthropic", "default", "api_key"); err != nil || string(v) != antAPIKey {
		t.Fatalf("api_key custody: %q err=%v", v, err)
	}
}

func TestAnthropicShortSetupTokenOffline(t *testing.T) {
	m := &antMock{}
	newAntMock(t, m)
	app := newApp(t, newKMS(t))

	res := as(t, app, http.MethodPost, credPath("anthropic"), "acme", userEmail, map[string]any{"token": "sk-ant-oat01-short"})
	if res.Code != http.StatusBadRequest {
		t.Fatalf("short setup token want 400, got %d (%s)", res.Code, res.Body)
	}
	if got := m.snapshot(); got.calls != 0 {
		t.Fatalf("syntactic reject must be OFFLINE, saw %d verify calls", got.calls)
	}
}

func TestAnthropicRejected(t *testing.T) {
	m := &antMock{status: http.StatusUnauthorized}
	newAntMock(t, m)
	kc := newKMS(t)
	app := newApp(t, kc)

	res := as(t, app, http.MethodPost, credPath("anthropic"), "acme", userEmail, map[string]any{"token": antAPIKey})
	if res.Code != http.StatusBadRequest || !bytes.Contains(res.Body, []byte("credential verification failed")) {
		t.Fatalf("rejected credential want 400, got %d (%s)", res.Code, res.Body)
	}
	// Verify-before-store: nothing sealed, no row.
	if _, err := userHas(t, kc, "acme", userEmail, "anthropic", "default", "api_key"); err == nil {
		t.Fatal("a rejected credential must not be sealed")
	}
	list := decode[listResp](t, asOK(t, app, http.MethodGet, "/v1/connectors", "acme", userEmail, nil))
	if len(list.Connectors) != 0 {
		t.Fatalf("a rejected credential must not create a row: %+v", list.Connectors)
	}
}

func TestAnthropicToken(t *testing.T) {
	m := &antMock{}
	newAntMock(t, m)
	app := newApp(t, newKMS(t))
	asOK(t, app, http.MethodPost, credPath("anthropic"), "acme", userEmail, map[string]any{"token": antAPIKey})

	// Static credential: /token serves the stored slot, no expiry.
	tk := decode[tokenResp](t, asOK(t, app, http.MethodGet, tokenPath("anthropic:default"), "acme", userEmail, nil))
	if tk.Token != antAPIKey || tk.ExpiresAt != "" {
		t.Fatalf("static token read: %+v", tk)
	}
	// No Refresh capability -> explicit 400, not a fake rotation.
	res := as(t, app, http.MethodPost, refreshPath("anthropic:default"), "acme", userEmail, nil)
	if res.Code != http.StatusBadRequest || !bytes.Contains(res.Body, []byte("refresh not supported")) {
		t.Fatalf("refresh of a static credential want 400, got %d (%s)", res.Code, res.Body)
	}
}

func TestAnthropicIsolation(t *testing.T) {
	m := &antMock{}
	newAntMock(t, m)
	kc := newKMS(t)
	app := newApp(t, kc)
	asOK(t, app, http.MethodPost, credPath("anthropic"), "orga", userEmail, map[string]any{"token": antAPIKey})

	if res := as(t, app, http.MethodGet, tokenPath("anthropic:default"), "orga", "b@else.io", nil); res.Code != http.StatusNotFound {
		t.Fatalf("same-org other-user token want 404, got %d (%s)", res.Code, res.Body)
	}
	if res := as(t, app, http.MethodGet, tokenPath("anthropic:default"), "orgb", userEmail, nil); res.Code != http.StatusNotFound {
		t.Fatalf("other-org token want 404, got %d (%s)", res.Code, res.Body)
	}
	if _, err := userHas(t, kc, "orga", "b@else.io", "anthropic", "default", "api_key"); err == nil {
		t.Fatal("another user's KMS namespace must be empty")
	}
	if _, err := userHas(t, kc, "orgb", userEmail, "anthropic", "default", "api_key"); err == nil {
		t.Fatal("another org's KMS namespace must be empty")
	}
}

func TestAnthropicAccountHint(t *testing.T) {
	m := &antMock{}
	newAntMock(t, m)
	app := newApp(t, newKMS(t))

	// /v1/models discloses no identity; the sanitized caller hint is the only
	// ExternalID source.
	cr := decode[credResp](t, asOK(t, app, http.MethodPost, credPath("anthropic"), "acme", userEmail,
		map[string]any{"token": antAPIKey, "accountId": "acct-1"}))
	if cr.Connector == nil || cr.Connector.ExternalID != "acct-1" {
		t.Fatalf("account hint must ride into externalId: %+v", cr)
	}
}
