package integrations

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
)

// GitHub is a GitHub *App* provider on the SAME registry as Slack/Google/GitLab.
// The user is sent to the App's installation page; the callback returns an
// installation_id (NOT an OAuth code — the App must be configured WITHOUT "Request
// user authorization (OAuth) during installation", so the generic callback reads
// installation_id). The installation_id is custodied as the connection ExternalID;
// it maps the install back to the org (OrgForExternalID) for inbound webhooks, and
// a fresh short-lived installation token is minted on demand from the App private
// key (github_app.go) for every list/import/mirror — never stored.
//
// Sync is wired via DEDICATED routes (github_app.go / github_webhook.go), not the
// generic Provider.Sync hook: the repo list, import, and the inbound push webhook
// each need their own request/response shape.
//
// Creds are ENV-injected (KMS-synced, never in code). Until the App is configured
// (slug + app id + private key) Configured() is false: the card shows Connect but
// available=false and connect returns an honest 503 — never a fake OK.
func init() {
	register(&Provider{
		ID:           "github",
		Name:         "GitHub",
		Description:  "Install the Hanzo GitHub App to mirror your org's repositories into git.hanzo.ai and keep them in sync.",
		Category:     "Developer",
		Scopes:       nil, // GitHub Apps use installation PERMISSIONS, not OAuth scopes.
		RedirectPath: callbackPath("github"),
		Secrets:      nil, // no token custodied: installation tokens are minted on demand, never sealed.
		Configured:   githubConfigured,
		Creds:        githubCreds,
		Authorize:    githubAuthorize,
		Exchange:     githubExchange,
		Revoke:       nil, // App installs are removed from GitHub, not token-revoked.
	})
}

const (
	githubAppSlugEnv = "GITHUB_APP_SLUG"
	githubAppIDEnv   = "GITHUB_APP_ID"
	// githubAppKeyEnv holds the App's PEM private key (KMS-synced). Used only to
	// mint short-lived installation tokens (github_app.go); never logged.
	githubAppKeyEnv = "GITHUB_APP_PRIVATE_KEY"
	// githubWebhookSecretEnv holds the App webhook secret (KMS-synced). The inbound
	// webhook HMAC-verifies every payload with it, fail-closed (github_webhook.go).
	githubWebhookSecretEnv = "GITHUB_APP_WEBHOOK_SECRET"
	githubSlugKey          = "slug" // OAuthConfig.Extra key carrying the App slug
)

// githubCreds resolves the non-secret install config from ENV. ClientID/Secret are
// unused (the App install flow is not OAuth); the slug builds the install URL.
func githubCreds() OAuthConfig {
	return OAuthConfig{
		Extra: map[string]string{githubSlugKey: strings.TrimSpace(os.Getenv(githubAppSlugEnv))},
	}
}

// githubConfigured requires the three the install + sync need: the App slug (install
// URL), the App id, and the private key (installation-token minting). Absent ⇒ false
// ⇒ connect 503. The webhook secret is validated at webhook time (fail-closed there),
// so its absence does not hide the Connect card — inbound sync simply fails closed
// until it lands.
func githubConfigured() bool {
	return strings.TrimSpace(os.Getenv(githubAppSlugEnv)) != "" &&
		strings.TrimSpace(os.Getenv(githubAppIDEnv)) != "" &&
		strings.TrimSpace(os.Getenv(githubAppKeyEnv)) != ""
}

// githubAuthorize builds the App installation URL:
// https://github.com/apps/{slug}/installations/new?state=… . GitHub carries the
// state param through the install and returns it (with installation_id) on the callback.
func githubAuthorize(creds OAuthConfig, _, state string) (string, error) {
	slug := creds.Extra[githubSlugKey]
	if slug == "" {
		return "", fmt.Errorf("github: GITHUB_APP_SLUG not set")
	}
	return "https://github.com/apps/" + url.PathEscape(slug) +
		"/installations/new?state=" + url.QueryEscape(state), nil
}

// githubExchange handles the App install callback. The generic dispatcher passes
// the installation_id (the callback query `installation_id`, surfaced as the
// identifier when no OAuth `code` is present). It custodies the installation_id as
// the ExternalID and validates the install by fetching its account via the App JWT
// — a failure fails the connection (fail-closed), never a fabricated success. No
// token is sealed: installation tokens are minted on demand (github_app.go).
func githubExchange(ctx context.Context, _ OAuthConfig, _, identifier string) (*ExchangeResult, error) {
	instID := strings.TrimSpace(identifier)
	if instID == "" {
		return nil, fmt.Errorf("github: empty installation id")
	}
	id, err := strconv.ParseInt(instID, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("github: installation_id must be numeric")
	}
	label, err := githubInstallationAccount(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("github: could not verify installation %d: %w", id, err)
	}
	return &ExchangeResult{
		Tokens:       map[string]string{}, // none — minted on demand
		ExternalID:   instID,
		AccountLabel: label,
	}, nil
}
