package integrations

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// GitLab is an OAuth2 provider on the SAME registry as Slack/Google/GitHub. It
// custodies a GitLab access token (and refresh token) in the org's KMS namespace
// under "access_token"/"refresh_token" — the names the git-sync bridge reads via
// integrations.TokenFor(org,"gitlab","access_token"). One GitLab connection, one
// token; login (openid/profile/email), the API (read_api), and repo import + the
// native↔GitLab mirror (read_repository/write_repository) all ride it.
//
// Creds are ENV-injected (KMS-synced, never in code): GITLAB_CLIENT_ID +
// GITLAB_CLIENT_SECRET. Until both are present Configured() is false: the card
// shows Connect but available=false and connect returns an honest 503 — never a
// fake OK. GITLAB_URL points at a self-hosted instance (default https://gitlab.com).
//
// SCOPES: we request only the least-privilege set below. A GitLab app may be
// granted broader scopes at creation, but the token receives only the intersection
// of requested + app-allowed — so requesting the minimal set is what actually
// bounds the token, regardless of how the app was provisioned. We deliberately do
// NOT request api/sudo/admin_mode/k8s_proxy/*_runner/*_registry.
func init() {
	register(&Provider{
		ID:           "gitlab",
		Name:         "GitLab",
		Description:  "Connect GitLab to sign in, list your groups & projects, and mirror repos.",
		Category:     "Developer",
		Scopes:       gitlabScopes,
		RedirectPath: callbackPath("gitlab"),
		Secrets:      []string{accessSecret, refreshSecret},
		Configured:   gitlabConfigured,
		Creds:        gitlabCreds,
		Authorize:    gitlabAuthorize,
		Exchange:     gitlabExchange,
		Revoke:       gitlabRevoke,
	})
}

const (
	gitlabClientIDEnv     = "GITLAB_CLIENT_ID"
	gitlabClientSecretEnv = "GITLAB_CLIENT_SECRET"
	gitlabURLEnv          = "GITLAB_URL"
)

// gitlabBase is the GitLab instance base (gitlab.com or a self-hosted instance),
// from GITLAB_URL (default https://gitlab.com). A package var so a test can point
// the exchange/revoke/user calls at a mock server; never mutated in production.
var gitlabBase = func() string {
	if v := strings.TrimSpace(os.Getenv(gitlabURLEnv)); v != "" {
		return strings.TrimRight(v, "/")
	}
	return "https://gitlab.com"
}()

func gitlabAuthURL() string   { return gitlabBase + "/oauth/authorize" }
func gitlabTokenURL() string  { return gitlabBase + "/oauth/token" }
func gitlabRevokeURL() string { return gitlabBase + "/oauth/revoke" }
func gitlabUserURL() string   { return gitlabBase + "/api/v4/user" }

// gitlabScopes — least privilege: OIDC login (openid/profile/email), read the API
// to list groups/projects (read_api), and repo read+write for import + mirror.
var gitlabScopes = []string{
	"openid",
	"profile",
	"email",
	"read_api",
	"read_repository",
	"write_repository",
}

var gitlabHTTP = &http.Client{Timeout: 20 * time.Second}

func gitlabCreds() OAuthConfig {
	return OAuthConfig{
		ClientID:     strings.TrimSpace(os.Getenv(gitlabClientIDEnv)),
		ClientSecret: strings.TrimSpace(os.Getenv(gitlabClientSecretEnv)),
	}
}

// gitlabConfigured gates on the client id (the card lights up before the KMS-synced
// secret lands); the secret is validated at the token exchange.
func gitlabConfigured() bool { return gitlabCreds().ClientID != "" }

// gitlabAuthorize builds the consent URL (Authorization Code flow).
func gitlabAuthorize(creds OAuthConfig, redirectURI, state string) (string, error) {
	q := url.Values{
		"client_id":     {creds.ClientID},
		"redirect_uri":  {redirectURI},
		"response_type": {"code"},
		"scope":         {strings.Join(gitlabScopes, " ")},
		"state":         {state},
	}
	return gitlabAuthURL() + "?" + q.Encode(), nil
}

// gitlabTokenResponse is the OAuth token endpoint response.
type gitlabTokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	Scope        string `json:"scope"`
	TokenType    string `json:"token_type"`
	Error        string `json:"error"`
	ErrorDesc    string `json:"error_description"`
}

// gitlabExchange trades the OAuth code for tokens and seals them into KMS. The
// exchange REQUIRES the client secret; a deployment with only the id fails with an
// honest, specific reason, never a dead-end.
func gitlabExchange(ctx context.Context, creds OAuthConfig, redirectURI, code string) (*ExchangeResult, error) {
	if strings.TrimSpace(creds.ClientSecret) == "" {
		return nil, fmt.Errorf("gitlab integration is not fully configured on this deployment: GITLAB_CLIENT_SECRET is not set")
	}
	form := url.Values{
		"client_id":     {creds.ClientID},
		"client_secret": {creds.ClientSecret},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"grant_type":    {"authorization_code"},
	}
	var r gitlabTokenResponse
	if err := gitlabPostForm(ctx, gitlabTokenURL(), form, &r); err != nil {
		return nil, err
	}
	if r.Error != "" {
		return nil, fmt.Errorf("gitlab token exchange error: %s (%s)", r.Error, r.ErrorDesc)
	}
	if r.AccessToken == "" {
		return nil, fmt.Errorf("gitlab token exchange returned no access_token")
	}
	tokens := map[string]string{accessSecret: r.AccessToken}
	if r.RefreshToken != "" {
		tokens[refreshSecret] = r.RefreshToken
	}
	externalID, label := gitlabAccount(ctx, r.AccessToken)
	return &ExchangeResult{
		Tokens:       tokens,
		ExternalID:   externalID,
		AccountLabel: label,
		Scopes:       strings.Fields(r.Scope),
	}, nil
}

// gitlabAccount fetches the connected user's id + username for the connection row.
// Best-effort: a failure does not fail the connection (the tokens are already
// valid), so it returns empty labels rather than an error.
func gitlabAccount(ctx context.Context, accessToken string) (externalID, label string) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, gitlabUserURL(), nil)
	if err != nil {
		return "", ""
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := gitlabHTTP.Do(req)
	if err != nil {
		return "", ""
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil || resp.StatusCode/100 != 2 {
		return "", ""
	}
	var u struct {
		ID       int64  `json:"id"`
		Username string `json:"username"`
	}
	if json.Unmarshal(body, &u) != nil {
		return "", ""
	}
	return strconv.FormatInt(u.ID, 10), u.Username
}

// gitlabRevoke best-effort invalidates a token at GitLab on disconnect. GitLab's
// revoke endpoint requires client authentication, so we pass the app creds.
func gitlabRevoke(ctx context.Context, creds OAuthConfig, token string) error {
	if strings.TrimSpace(token) == "" {
		return nil
	}
	form := url.Values{
		"token":         {token},
		"client_id":     {creds.ClientID},
		"client_secret": {creds.ClientSecret},
	}
	return gitlabPostForm(ctx, gitlabRevokeURL(), form, &struct{}{})
}

// gitlabPostForm POSTs a form-encoded body and decodes the JSON response into out.
// Bounded body read so a hostile response can't exhaust memory.
func gitlabPostForm(ctx context.Context, endpoint string, form url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("gitlab request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := gitlabHTTP.Do(req)
	if err != nil {
		return fmt.Errorf("gitlab call: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("gitlab read: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("gitlab http %d: %s", resp.StatusCode, truncateBody(body))
	}
	if len(body) == 0 {
		return nil // revoke returns 200 with an empty body
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("gitlab decode: %w", err)
	}
	return nil
}
