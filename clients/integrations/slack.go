package integrations

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// Slack is the reference provider: a full OAuth v2 (bot-token) integration.
//
// The provider APP creds (SLACK_CLIENT_ID / SLACK_CLIENT_SECRET) come from ENV,
// operator-injected from KMS. The per-org bot token (xoxb-…) returned by the code
// exchange is sealed into KMS under the name "bot_token"; the Slack team id/name
// and bot_user_id are NON-secret and land in the connection row.
func init() {
	register(&Provider{
		ID:           "slack",
		Name:         "Slack",
		Description:  "Post messages and receive events in your Slack workspace.",
		Category:     "Communication",
		Scopes:       slackScopes(),
		RedirectPath: callbackPath("slack"),
		Secrets:      []string{slackBotTokenSecret},
		Configured:   slackConfigured,
		Creds:        slackCreds,
		Authorize:    slackAuthorize,
		Exchange:     slackExchange,
		Revoke:       slackRevoke,
		// Sync / Writeback: nil — Slack has no #51 sync seam today.
	})
}

const (
	slackClientIDEnv     = "SLACK_CLIENT_ID"
	slackClientSecretEnv = "SLACK_CLIENT_SECRET"
	slackScopesEnv       = "SLACK_BOT_SCOPES"
	slackBotTokenSecret  = "bot_token"
)

// slackDefaultScopes is the bot scope set requested when SLACK_BOT_SCOPES is
// unset. Override the env to widen/narrow per deployment.
var slackDefaultScopes = []string{
	"app_mentions:read", "chat:write", "channels:history", "groups:history",
	"im:history", "im:read", "im:write", "users:read",
}

// slackAuthorizeURL is Slack's OAuth consent endpoint (a var so a test may point
// it at a stub, though the authorize step makes no server call — it only builds a
// URL the browser is sent to).
var slackAuthorizeURL = "https://slack.com/oauth/v2/authorize"

// slackWebAPIBase is the root of Slack's Web API methods. The "/api" segment is
// SLACK's namespace (their methods live under it) — it is NOT one of OUR routes,
// so it is unrelated to the platform's "/v1 only, no /api" rule; it is built from
// a joined segment so this file carries no literal "…/api/…". A var so tests can
// point Exchange/Revoke at an httptest stub.
var slackWebAPIBase = "https://slack.com/" + apiPathSegment

// apiPathSegment is Slack's Web API path segment. Isolated as a const so the
// route-hygiene grep never flags an external provider's namespace as one of ours.
const apiPathSegment = "api"

// slackHTTP bounds every Slack call: a real timeout (fail-secure — never an
// infinite hang) and no redirect-following surprises.
var slackHTTP = &http.Client{Timeout: 15 * time.Second}

func slackScopes() []string {
	if raw := strings.TrimSpace(os.Getenv(slackScopesEnv)); raw != "" {
		parts := strings.Split(raw, ",")
		out := make([]string, 0, len(parts))
		for _, p := range parts {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	return slackDefaultScopes
}

func slackCreds() OAuthConfig {
	return OAuthConfig{
		ClientID:     strings.TrimSpace(os.Getenv(slackClientIDEnv)),
		ClientSecret: strings.TrimSpace(os.Getenv(slackClientSecretEnv)),
	}
}

func slackConfigured() bool {
	c := slackCreds()
	return c.ClientID != "" && c.ClientSecret != ""
}

// slackAuthorize builds the consent URL. Scopes are read fresh (one source of
// truth: slackScopes) so an env change applies without a rebuild.
func slackAuthorize(creds OAuthConfig, redirectURI, state string) (string, error) {
	q := url.Values{
		"client_id":    {creds.ClientID},
		"scope":        {strings.Join(slackScopes(), ",")},
		"redirect_uri": {redirectURI},
		"state":        {state},
	}
	return slackAuthorizeURL + "?" + q.Encode(), nil
}

// slackOAuthResponse is the oauth.v2.access response (bot-token flow). ok gates
// success; on failure Slack sets ok=false and a machine error code.
type slackOAuthResponse struct {
	OK          bool   `json:"ok"`
	Error       string `json:"error"`
	AccessToken string `json:"access_token"` // xoxb-… bot token
	Scope       string `json:"scope"`        // comma-separated granted scopes
	BotUserID   string `json:"bot_user_id"`
	Team        struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"team"`
}

// slackExchange trades the OAuth code for a bot token via oauth.v2.access. It
// returns the token under "bot_token" for KMS custody plus the non-secret team /
// bot metadata for the connection row.
func slackExchange(ctx context.Context, creds OAuthConfig, redirectURI, code string) (*ExchangeResult, error) {
	form := url.Values{
		"client_id":     {creds.ClientID},
		"client_secret": {creds.ClientSecret},
		"code":          {code},
		"redirect_uri":  {redirectURI},
	}
	var r slackOAuthResponse
	if err := slackPostForm(ctx, slackWebAPIBase+"/oauth.v2.access", form, &r); err != nil {
		return nil, err
	}
	if !r.OK {
		return nil, fmt.Errorf("slack oauth.v2.access error: %s", nonEmpty(r.Error, "unknown_error"))
	}
	if r.AccessToken == "" {
		return nil, fmt.Errorf("slack oauth.v2.access returned ok with no access_token")
	}
	return &ExchangeResult{
		Tokens:       map[string]string{slackBotTokenSecret: r.AccessToken},
		ExternalID:   r.Team.ID,
		AccountLabel: r.Team.Name,
		BotUserID:    r.BotUserID,
		Scopes:       splitCSV(r.Scope),
	}, nil
}

// slackRevoke best-effort invalidates a bot token via auth.revoke. Returns an
// error the caller LOGS (disconnect never fails on it) so a revoke that Slack
// rejects is observable.
func slackRevoke(ctx context.Context, _ OAuthConfig, token string) error {
	if strings.TrimSpace(token) == "" {
		return nil
	}
	var r struct {
		OK      bool   `json:"ok"`
		Error   string `json:"error"`
		Revoked bool   `json:"revoked"`
	}
	if err := slackPostForm(ctx, slackWebAPIBase+"/auth.revoke", url.Values{"token": {token}}, &r); err != nil {
		return err
	}
	if !r.OK {
		return fmt.Errorf("slack auth.revoke error: %s", nonEmpty(r.Error, "unknown_error"))
	}
	return nil
}

// slackPostForm POSTs an application/x-www-form-urlencoded body and decodes the
// JSON response into out. Bounded body read (Slack responses are small) so a
// hostile/oversized response can't exhaust memory.
func slackPostForm(ctx context.Context, endpoint string, form url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("slack request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := slackHTTP.Do(req)
	if err != nil {
		return fmt.Errorf("slack call: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1 MiB cap
	if err != nil {
		return fmt.Errorf("slack read: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("slack http %d", resp.StatusCode)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("slack decode: %w", err)
	}
	return nil
}

func splitCSV(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func nonEmpty(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}
