package share

import (
	"context"
	"strings"
	"testing"
)

// fakeController is an in-memory controller: no network, records the last
// credential it was asked to provision.
type fakeController struct {
	configuredV bool
	tokenV      string
	overviewV   overviewResp
	tokenErr    error
}

func (f *fakeController) configured() bool { return f.configuredV }
func (f *fakeController) token(_ context.Context, _ string, _ bool) (string, error) {
	if f.tokenErr != nil {
		return "", f.tokenErr
	}
	return f.tokenV, nil
}
func (f *fakeController) overview(_ context.Context, _ string) (overviewResp, error) {
	return f.overviewV, nil
}

// TestAccountDeterminism — the per-org email + password are pure functions of
// the org, so provisioning is stateless and idempotent (same org → same creds).
func TestAccountDeterminism(t *testing.T) {
	c := &httpController{secret: []byte("k")}
	// email is per-org, deterministic, and carries an HMAC freshness suffix.
	e1, e2 := c.accountEmail("acme"), c.accountEmail("acme")
	if e1 != e2 {
		t.Error("email not deterministic")
	}
	if !strings.HasPrefix(e1, "share-acme-") || !strings.HasSuffix(e1, "@hanzo.ai") {
		t.Errorf("email shape = %q", e1)
	}
	if c.accountEmail("acme") == c.accountEmail("other") {
		t.Error("email not org-scoped")
	}
	if c.accountPassword("acme") != c.accountPassword("acme") {
		t.Error("password not deterministic")
	}
	if c.accountPassword("acme") == c.accountPassword("other") {
		t.Error("password not org-scoped")
	}
	if len(c.accountPassword("acme")) != 32 {
		t.Errorf("password length = %d, want 32", len(c.accountPassword("acme")))
	}
}

// TestConfiguredGate — an unconfigured controller reports not-configured, so the
// gate can fail closed.
func TestConfiguredGate(t *testing.T) {
	if (&httpController{adminToken: ""}).configured() {
		t.Error("empty admin token must be unconfigured")
	}
	if !(&httpController{adminToken: "x"}).configured() {
		t.Error("present admin token must be configured")
	}
}

// TestShareURL — the token renders into the public URL via the template.
func TestShareURL(t *testing.T) {
	t.Setenv("SHARE_URL_TEMPLATE", "https://{token}.share.hanzo.ai")
	if got := shareURL("abc123"); got != "https://abc123.share.hanzo.ai" {
		t.Errorf("shareURL = %q", got)
	}
}
