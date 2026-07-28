package integrations

import (
	"net/url"
	"strings"
	"testing"
)

// TestGoogleDataAuthorizeScopes proves each Google data connector requests exactly its
// own least-privilege READ scope (bigquery.readonly / cloud-platform.read-only) plus
// the identity scopes, and never a write/admin Google scope — it reuses google.go's
// googleAuthorizeWith, so this pins the scope binding that distinguishes them.
func TestGoogleDataAuthorizeScopes(t *testing.T) {
	cases := map[string]string{
		"google_bigquery": "https://www.googleapis.com/auth/bigquery.readonly",
		"google_cloud":    "https://www.googleapis.com/auth/cloud-platform.read-only",
	}
	for id, wantScope := range cases {
		p := registry[id]
		raw, err := p.Authorize(OAuthConfig{ClientID: "cid"}, callbackPath(id), "st8")
		if err != nil {
			t.Fatalf("%s authorize: %v", id, err)
		}
		u, _ := url.Parse(raw)
		scope := u.Query().Get("scope")
		if !strings.Contains(scope, wantScope) {
			t.Errorf("%s scope %q missing %q", id, scope, wantScope)
		}
		// Least privilege: every googleapis grant must be a read-only variant.
		for _, s := range strings.Fields(scope) {
			if strings.HasPrefix(s, "https://www.googleapis.com/auth/") &&
				!strings.HasSuffix(s, ".readonly") && !strings.HasSuffix(s, ".read-only") {
				t.Errorf("%s scope %q includes a non-read grant %q", id, scope, s)
			}
		}
		// offline + consent so a refresh token is issued (the reuse of google's builder).
		if u.Query().Get("access_type") != "offline" {
			t.Errorf("%s must request offline access for a refresh token", id)
		}
	}
}
