package connectors

import (
	"net/url"
	"strings"
	"testing"
)

// The catalog is DATA, so what needs proving is that every entry is well-formed
// — a malformed one is a 500 or, worse, a connect that goes somewhere it should
// not, and neither shows up until someone clicks the card.

// wanted names the connectors this deployment promises. A rename or an
// accidental deletion is a product change, so it fails here rather than being
// noticed by a customer whose card vanished.
var wanted = []string{
	"x", "linkedin", "facebook", "instagram", "threads", "pinterest", "reddit",
	"tiktok", "youtube", "twitch", "slack", "discord", "github", "google",
	"microsoft", "notion",
}

func TestCatalogCarriesEveryConnector(t *testing.T) {
	for _, id := range wanted {
		if registry[id] == nil {
			t.Errorf("connector %q missing from the registry", id)
		}
	}
	if len(registry) != len(wanted) {
		t.Errorf("registry has %d connectors, catalog names %d", len(registry), len(wanted))
	}
}

// Every provider's endpoints must be absolute https. A relative or http URL in
// a declaration is a typo that sends someone's consent — and their code — to
// the wrong place, and `url.Parse` accepts both without complaint.
func TestCatalogEndpointsAreHTTPS(t *testing.T) {
	for id, p := range registry {
		for _, sp := range catalog {
			if sp.id != id {
				continue
			}
			for label, raw := range map[string]string{
				"authURL": sp.authURL, "tokenURL": sp.tokenURL, "revokeURL": sp.revokeURL,
			} {
				if raw == "" {
					continue // revokeURL is legitimately optional
				}
				u, err := url.Parse(raw)
				if err != nil {
					t.Errorf("%s %s: %v", id, label, err)
					continue
				}
				if u.Scheme != "https" || u.Host == "" {
					t.Errorf("%s %s must be absolute https, got %q", id, label, raw)
				}
			}
		}
		_ = p
	}
}

// RedirectPath is asserted at Mount to equal /v1/connectors/{id}/callback — the
// single generic callback route. A provider that disagrees fails the boot, so
// it is worth failing here first, where the message names the provider.
func TestCatalogRedirectPathsMatchTheOneRoute(t *testing.T) {
	for id, p := range registry {
		if want := "/v1/connectors/" + id + "/callback"; p.RedirectPath != want {
			t.Errorf("%s RedirectPath = %q, want %q", id, p.RedirectPath, want)
		}
	}
}

// A connector with no credentials configured must report available=false and
// must NOT be reachable — never a card that looks live and dead-ends. The whole
// catalog is unconfigured in a test process, which makes this easy to state.
func TestCatalogUnconfiguredIsNotAvailable(t *testing.T) {
	for id, p := range registry {
		if p.Configured == nil {
			t.Errorf("%s has no Configured func", id)
			continue
		}
		if p.Configured() {
			t.Errorf("%s reports configured with no env set", id)
		}
	}
}

// Every connector must ask for at least one scope and must name the secret it
// custodies — Secrets is what disconnect deletes from KMS, so an empty one
// leaves a live token behind after the reader was told it was disconnected.
func TestCatalogDeclaresScopesAndSecrets(t *testing.T) {
	for id, p := range registry {
		if len(p.Secrets) == 0 {
			t.Errorf("%s custodies no named secret — disconnect would leave the token sealed", id)
		}
		// Notion issues a workspace token with no scope parameter; everything
		// else asks for least privilege explicitly.
		if len(p.Scopes) == 0 && id != "notion" {
			t.Errorf("%s requests no scopes", id)
		}
	}
}

// The authorize URL is built by the ONE engine, so proving it once proves it for
// every declaration: the provider's own endpoint, our redirect, the state, and
// no secret anywhere in it.
func TestAuthorizeURLShape(t *testing.T) {
	p := registry["github"]
	raw, err := p.Authorize(OAuthConfig{ClientID: "cid", ClientSecret: "sekrit"},
		"https://api.hanzo.ai/v1/connectors/github/callback", "STATE-1")
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if u.Host != "github.com" {
		t.Fatalf("authorize host = %q", u.Host)
	}
	q := u.Query()
	if q.Get("client_id") != "cid" || q.Get("state") != "STATE-1" {
		t.Fatalf("authorize query = %v", q)
	}
	if q.Get("response_type") != "code" {
		t.Fatalf("authorize must request an authorization code, got %q", q.Get("response_type"))
	}
	// THE SECRET MUST NEVER REACH THE BROWSER. The consent URL is a redirect the
	// user's browser follows, so a client_secret here is a disclosed credential.
	if strings.Contains(raw, "sekrit") {
		t.Fatalf("client_secret leaked into the authorize URL: %q", raw)
	}
}
