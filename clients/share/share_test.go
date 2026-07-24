package share

import (
	"context"
	"testing"
)

// fakeController is an in-memory controller: no network, records the last
// credential it was asked to provision.
type fakeController struct {
	configuredV bool
	accounts    map[string]string // email -> password (created)
	token       string
	overviewV   overviewResp
	loginErr    error
}

func (f *fakeController) configured() bool { return f.configuredV }
func (f *fakeController) ensureAccount(_ context.Context, email, password string) error {
	if f.accounts == nil {
		f.accounts = map[string]string{}
	}
	f.accounts[email] = password
	return nil
}
func (f *fakeController) login(_ context.Context, email, password string) (string, error) {
	if f.loginErr != nil {
		return "", f.loginErr
	}
	return f.token, nil
}
func (f *fakeController) overview(_ context.Context, _ string) (overviewResp, error) {
	return f.overviewV, nil
}

// TestAccountDeterminism — the per-org email + password are pure functions of
// the org, so provisioning is stateless and idempotent (same org → same creds).
func TestAccountDeterminism(t *testing.T) {
	if accountEmail("acme") != "share+acme@hanzo.ai" {
		t.Errorf("email = %q", accountEmail("acme"))
	}
	c := &httpController{secret: []byte("k")}
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

// TestPasswordForFake — a fake controller (not *httpController) gets a stable
// stand-in password so handler tests need no network.
func TestPasswordForFake(t *testing.T) {
	f := &fakeController{}
	if passwordFor(f, "acme") != passwordFor(f, "acme") {
		t.Error("fake password not stable")
	}
}
