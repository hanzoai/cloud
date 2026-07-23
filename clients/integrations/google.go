package integrations

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
)

// Google is an OAuth2 provider on the SAME registry as Slack/GitHub. It custodies a
// Google access token (and, when granted, a refresh token) in the org's KMS
// namespace under the secret names "access_token"/"refresh_token" — the exact names
// the automations Google connector reads via RunContext.Token and the company import
// bridge reads via integrations.TokenFor(org,"google","access_token"). One Google
// connection, one token, both Drive (import) and Sheets (import + connector) ride it.
//
// Creds are ENV-injected (KMS-synced, never in code): GOOGLE_CLIENT_ID +
// GOOGLE_CLIENT_SECRET. Until both are present Configured() is false: the card shows
// Connect but available=false and connect returns an honest 503 — never a fake OK.
func init() {
	register(&Provider{
		ID:           "google",
		Name:         "Google",
		Description:  "Connect Google Drive & Sheets to import documents and cap tables.",
		Category:     "Productivity",
		Scopes:       googleScopes,
		RedirectPath: callbackPath("google"),
		Secrets:      []string{accessSecret, refreshSecret},
		Configured:   googleConfigured,
		Creds:        googleCreds,
		Authorize:    googleAuthorize,
		Exchange:     googleExchange,
		Revoke:       googleRevoke,
	})
}

const (
	googleClientIDEnv     = "GOOGLE_CLIENT_ID"
	googleClientSecretEnv = "GOOGLE_CLIENT_SECRET"
)

// Endpoint URLs are package vars (not consts) so a test can point the exchange /
// revoke / userinfo calls at a mock server. They are never mutated in production.
var (
	googleAuthURL     = "https://accounts.google.com/o/oauth2/v2/auth"
	googleTokenURL    = "https://oauth2.googleapis.com/token"
	googleRevokeURL   = "https://oauth2.googleapis.com/revoke"
	googleUserinfoURL = "https://www.googleapis.com/oauth2/v2/userinfo"
)

// googleScopes is the requested scope set: Drive read (document import),
// Spreadsheets read+write (cap-table import + the connector's append_row), and
// openid/email for the connection's account label.
var googleScopes = []string{
	"openid",
	"email",
	"https://www.googleapis.com/auth/drive.readonly",
	"https://www.googleapis.com/auth/spreadsheets",
}

var googleHTTP = &http.Client{Timeout: 20 * time.Second}

func googleCreds() OAuthConfig {
	return OAuthConfig{
		ClientID:     strings.TrimSpace(os.Getenv(googleClientIDEnv)),
		ClientSecret: strings.TrimSpace(os.Getenv(googleClientSecretEnv)),
	}
}

// googleConfigured gates on the client id (the card lights up before the KMS-synced
// secret lands); the secret is validated at the token exchange.
func googleConfigured() bool { return googleCreds().ClientID != "" }

// googleAuthorize builds the consent URL. access_type=offline + prompt=consent
// requests a refresh token on first authorization; include_granted_scopes keeps
// previously-granted scopes.
func googleAuthorize(creds OAuthConfig, redirectURI, state string) (string, error) {
	q := url.Values{
		"client_id":              {creds.ClientID},
		"redirect_uri":           {redirectURI},
		"response_type":          {"code"},
		"scope":                  {strings.Join(googleScopes, " ")},
		"state":                  {state},
		"access_type":            {"offline"},
		"include_granted_scopes": {"true"},
		"prompt":                 {"consent"},
	}
	return googleAuthURL + "?" + q.Encode(), nil
}

// googleTokenResponse is the OAuth token endpoint response.
type googleTokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	Scope        string `json:"scope"`
	TokenType    string `json:"token_type"`
	Error        string `json:"error"`
	ErrorDesc    string `json:"error_description"`
}

// googleExchange trades the OAuth code for tokens and seals them into KMS. The
// exchange REQUIRES the client secret; a deployment with only the id fails with an
// honest, specific reason (rendered as a failure redirect), never a dead-end.
func googleExchange(ctx context.Context, creds OAuthConfig, redirectURI, code string) (*ExchangeResult, error) {
	if strings.TrimSpace(creds.ClientSecret) == "" {
		return nil, fmt.Errorf("google integration is not fully configured on this deployment: GOOGLE_CLIENT_SECRET is not set")
	}
	form := url.Values{
		"client_id":     {creds.ClientID},
		"client_secret": {creds.ClientSecret},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"grant_type":    {"authorization_code"},
	}
	var r googleTokenResponse
	if err := googlePostForm(ctx, googleTokenURL, form, &r); err != nil {
		return nil, err
	}
	if r.Error != "" {
		return nil, fmt.Errorf("google token exchange error: %s (%s)", r.Error, r.ErrorDesc)
	}
	if r.AccessToken == "" {
		return nil, fmt.Errorf("google token exchange returned no access_token")
	}
	tokens := map[string]string{accessSecret: r.AccessToken}
	// A refresh token is returned only on the first offline consent; seal it when present.
	if r.RefreshToken != "" {
		tokens[refreshSecret] = r.RefreshToken
	}
	externalID, label := googleAccount(ctx, r.AccessToken)
	return &ExchangeResult{
		Tokens:       tokens,
		ExternalID:   externalID,
		AccountLabel: label,
		Scopes:       strings.Fields(r.Scope),
	}, nil
}

// googleAccount fetches the connected account's id + email for the connection row.
// Best-effort: a userinfo failure does not fail the connection (the tokens are
// already valid), so it returns empty labels rather than an error.
func googleAccount(ctx context.Context, accessToken string) (externalID, label string) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, googleUserinfoURL, nil)
	if err != nil {
		return "", ""
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := googleHTTP.Do(req)
	if err != nil {
		return "", ""
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil || resp.StatusCode/100 != 2 {
		return "", ""
	}
	var u struct {
		ID    string `json:"id"`
		Email string `json:"email"`
	}
	if json.Unmarshal(body, &u) != nil {
		return "", ""
	}
	return u.ID, u.Email
}

// googleRevoke best-effort invalidates a token at Google on disconnect.
func googleRevoke(ctx context.Context, _ OAuthConfig, token string) error {
	if strings.TrimSpace(token) == "" {
		return nil
	}
	return googlePostForm(ctx, googleRevokeURL, url.Values{"token": {token}}, &struct{}{})
}

// googlePostForm POSTs a form-encoded body and decodes the JSON response into out.
// Bounded body read so a hostile response can't exhaust memory.
func googlePostForm(ctx context.Context, endpoint string, form url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("google request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := googleHTTP.Do(req)
	if err != nil {
		return fmt.Errorf("google call: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("google read: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		// Google returns a JSON error body; surface it (bounded) for an honest failure.
		return fmt.Errorf("google http %d: %s", resp.StatusCode, truncateBody(body))
	}
	if len(body) == 0 {
		return nil // revoke returns 200 with an empty body
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("google decode: %w", err)
	}
	return nil
}

// secretRE matches credential-shaped substrings — GitHub tokens (gho/ghp/ghr/ghs/ghu_,
// github_pat_) and a Bearer header value — so truncateBody redacts them from any
// surfaced provider error body. Defense in depth: a token rides only the request
// header and no provider echoes it back, but a reflected body must never surface one.
var secretRE = regexp.MustCompile(`(?i)(gh[oprsu]_[A-Za-z0-9_]{10,}|github_pat_[A-Za-z0-9_]+|bearer\s+[^\s"']+)`)

// truncateBody sanitizes then bounds a foreign error body for safe surfacing: it
// redacts any credential-shaped substring first, then caps the result to 256 bytes.
func truncateBody(b []byte) string {
	s := secretRE.ReplaceAllString(string(b), "[redacted]")
	const n = 256
	if len(s) > n {
		return s[:n]
	}
	return s
}
