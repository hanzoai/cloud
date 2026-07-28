package integrations

import "testing"

// TestAuthorizeReady pins the readiness rule for the authorize leg. Gating every
// provider on ClientID assumes the leg is OAuth2; a GitHub App install URL is
// built from the app slug and has no client id at all, so that assumption
// refused the connect flow for a credential the protocol never uses — which is
// why no installation could be mapped to an org, and every inbound webhook was
// ignored.
func TestAuthorizeReady(t *testing.T) {
	oauthCreds := func(id string) func() OAuthConfig {
		return func() OAuthConfig { return OAuthConfig{ClientID: id} }
	}
	for _, tc := range []struct {
		name string
		p    *Provider
		want bool
	}{
		{"oauth provider with a client id", &Provider{Creds: oauthCreds("abc")}, true},
		{"oauth provider without a client id", &Provider{Creds: oauthCreds("")}, false},
		{"non-oauth leg reports itself ready", &Provider{
			Creds: oauthCreds(""), AuthorizeReady: func() bool { return true }}, true},
		{"non-oauth leg reports itself unready", &Provider{
			Creds: oauthCreds("abc"), AuthorizeReady: func() bool { return false }}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := authorizeReady(tc.p); got != tc.want {
				t.Errorf("authorizeReady() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestGitHubAppIsConnectableWithoutAClientID is the regression this fixes: with
// the App fully configured (slug + id + private key) the connect flow must be
// reachable, even though githubCreds deliberately leaves ClientID empty.
func TestGitHubAppIsConnectableWithoutAClientID(t *testing.T) {
	t.Setenv(githubAppSlugEnv, "hanzo-cloud-app")
	t.Setenv(githubAppIDEnv, "62000701")
	t.Setenv(githubAppKeyEnv, "-----BEGIN RSA PRIVATE KEY-----\nnot-a-real-key\n-----END RSA PRIVATE KEY-----")

	p, ok := registry["github"]
	if !ok {
		t.Fatal("github provider is not registered")
	}
	if p.Creds().ClientID != "" {
		t.Fatal("githubCreds now sets a ClientID; this test no longer covers the App case")
	}
	if !authorizeReady(p) {
		t.Error("a fully configured GitHub App is not connectable; the install flow is gated on a client id it never uses")
	}

	// And it still fails closed when the App is genuinely unconfigured.
	t.Setenv(githubAppKeyEnv, "")
	if authorizeReady(p) {
		t.Error("an App with no private key reported ready; connect must 503 rather than offer a dead install URL")
	}
}
