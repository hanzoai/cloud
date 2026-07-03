package integrations

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
)

// GitHub is provider #2, scaffolded on the SAME registry as Slack. Its APP creds
// are not injected yet, so Configured is false today: the card shows Connect but
// available=false, and connect returns an honest 503 ("not configured") — a
// deliberate scaffold, never a dead-end and never a fabricated success.
//
// Model: a GitHub *App* (not a classic OAuth app). The user is sent to the App's
// installation page; the callback returns an installation_id (+ setup_action).
// Minting an installation access token from the App private key is the #51 Sync
// seam (declared below, NOT wired) — landing it needs GITHUB_APP_ID +
// GITHUB_APP_PRIVATE_KEY and is out of scope for this scaffold.
func init() {
	register(&Provider{
		ID:           "github",
		Name:         "GitHub",
		Description:  "Install the Hanzo GitHub App to sync repositories and events.",
		Category:     "Developer",
		Scopes:       nil, // GitHub Apps use installation PERMISSIONS, not OAuth scopes.
		RedirectPath: callbackPath("github"),
		Secrets:      nil, // no token custodied until the installation-token seam lands.
		Configured:   githubConfigured,
		Creds:        githubCreds,
		Authorize:    githubAuthorize,
		Exchange:     githubExchange,
		Revoke:       nil, // App installs are removed from GitHub, not token-revoked.

		// #51 SEAM — DECLARED, NOT WIRED. When creds land, set Sync to the
		// installation-token minting + repo/event sync implementation. It is not
		// attached to any route today; leaving it nil keeps the scaffold honest.
		Sync: nil,
	})
}

const (
	githubAppSlugEnv      = "GITHUB_APP_SLUG"
	githubClientIDEnv     = "GITHUB_APP_CLIENT_ID"
	githubClientSecretEnv = "GITHUB_APP_CLIENT_SECRET"
	githubSlugKey         = "slug" // OAuthConfig.Extra key carrying the App slug
)

func githubCreds() OAuthConfig {
	return OAuthConfig{
		ClientID:     strings.TrimSpace(os.Getenv(githubClientIDEnv)),
		ClientSecret: strings.TrimSpace(os.Getenv(githubClientSecretEnv)),
		Extra:        map[string]string{githubSlugKey: strings.TrimSpace(os.Getenv(githubAppSlugEnv))},
	}
}

// githubConfigured requires all three: App slug (for the install URL) plus the
// OAuth client id/secret. Absent today → false → connect 503.
func githubConfigured() bool {
	c := githubCreds()
	return c.Extra[githubSlugKey] != "" && c.ClientID != "" && c.ClientSecret != ""
}

// githubAuthorize builds the App installation URL:
// https://github.com/apps/{slug}/installations/new?state=… . GitHub carries the
// state param through the install and returns it on the callback.
func githubAuthorize(creds OAuthConfig, _, state string) (string, error) {
	slug := creds.Extra[githubSlugKey]
	if slug == "" {
		return "", fmt.Errorf("github: GITHUB_APP_SLUG not set")
	}
	return "https://github.com/apps/" + url.PathEscape(slug) +
		"/installations/new?state=" + url.QueryEscape(state), nil
}

// githubExchange handles the App install callback. A real install delivers
// installation_id + setup_action in the query; the generic dispatcher passes the
// `code` param (present when "Request user authorization during installation" is
// enabled). This scaffold packages the identifier it is given as the ExternalID
// and custodies NO token — installation-token minting is the #51 Sync seam. It is
// unreachable in production today (Configured is false), so it fabricates nothing.
func githubExchange(_ context.Context, _ OAuthConfig, _, code string) (*ExchangeResult, error) {
	if strings.TrimSpace(code) == "" {
		return nil, fmt.Errorf("github: empty installation callback identifier")
	}
	return &ExchangeResult{
		Tokens:       map[string]string{}, // none until the installation-token seam lands
		ExternalID:   code,                // placeholder for installation_id (see #51)
		AccountLabel: "",                  // resolved from the installation account when the seam lands
	}, nil
}
