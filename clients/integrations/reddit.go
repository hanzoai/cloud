package integrations

// reddit.go registers Reddit Ads as an ORG OAuth connector. One connection custodies
// an access token + refresh token (duration=permanent) in the org's KMS namespace;
// the ads reader rides them via integrations.TokenFor(org, "reddit_ads", …).
//
// Reddit's OAuth has two provider-specific requirements the framework tolerates: the
// token exchange authenticates the confidential client with HTTP Basic (client
// id:secret in the Authorization header, never the body/URL), and every Reddit call
// MUST carry a descriptive User-Agent (Reddit rate-limits/So blocks a default UA).
// Creds: REDDIT_ADS_CLIENT_ID + REDDIT_ADS_CLIENT_SECRET. Scope is least-privilege —
// adsread + identity (read + account label); the write scope (adsedit) is added when
// a campaign-write path is wired. Linking is an org-admin action.

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	redditProvider        = "reddit_ads"
	redditClientIDEnv     = "REDDIT_ADS_CLIENT_ID"
	redditClientSecretEnv = "REDDIT_ADS_CLIENT_SECRET"
	// redditUserAgent identifies Hanzo to Reddit on every call, per Reddit's API rules.
	redditUserAgent = "hanzo-cloud/1.0 (+https://hanzo.ai)"
)

var (
	redditAuthURL   = "https://www.reddit.com/api/v1/authorize"
	redditTokenURL  = "https://www.reddit.com/api/v1/access_token"
	redditMeURL     = "https://oauth.reddit.com/api/v1/me"
	redditRevokeURL = "https://www.reddit.com/api/v1/revoke_token"
)

// redditScopes: read + identity. The account label needs identity; adsread reads
// campaigns/reporting. adsedit (write) is intentionally absent until wired.
var redditScopes = []string{"adsread", "identity"}

func init() {
	register(&Provider{
		ID:           redditProvider,
		Name:         "Reddit Ads",
		Description:  "Connect Reddit Ads to read campaigns and performance.",
		Category:     "Advertising",
		AdminOnly:    true,
		Scopes:       redditScopes,
		RedirectPath: callbackPath(redditProvider),
		Secrets:      []string{accessSecret, refreshSecret},
		Configured:   redditConfigured,
		Creds:        redditCreds,
		Authorize:    redditAuthorize,
		Exchange:     redditExchange,
		Revoke:       redditRevoke,
	})
}

func redditCreds() OAuthConfig {
	return OAuthConfig{
		ClientID:     strings.TrimSpace(os.Getenv(redditClientIDEnv)),
		ClientSecret: strings.TrimSpace(os.Getenv(redditClientSecretEnv)),
	}
}

func redditConfigured() bool { return redditCreds().ClientID != "" }

// redditAuthorize builds the consent URL. duration=permanent requests a refresh
// token (the default, temporary, issues none).
func redditAuthorize(creds OAuthConfig, redirectURI, state string) (string, error) {
	q := url.Values{
		"client_id":     {creds.ClientID},
		"response_type": {"code"},
		"state":         {state},
		"redirect_uri":  {redirectURI},
		"duration":      {"permanent"},
		"scope":         {strings.Join(redditScopes, " ")},
	}
	return redditAuthURL + "?" + q.Encode(), nil
}

type redditTokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	Scope        string `json:"scope"`
	TokenType    string `json:"token_type"`
	Error        string `json:"error"`
}

// redditExchange trades the code for access + refresh tokens. The client is
// authenticated with HTTP Basic; the code rides the form body. Requires the secret.
func redditExchange(ctx context.Context, creds OAuthConfig, redirectURI, code string) (*ExchangeResult, error) {
	if strings.TrimSpace(creds.ClientSecret) == "" {
		return nil, fmt.Errorf("reddit ads integration is not fully configured on this deployment: REDDIT_ADS_CLIENT_SECRET is not set")
	}
	headers := map[string]string{
		"Authorization": basicAuth(creds.ClientID, creds.ClientSecret),
		"User-Agent":    redditUserAgent,
	}
	var r redditTokenResponse
	if err := oauthPostForm(ctx, redditProvider, redditTokenURL, headers, url.Values{
		"grant_type":   {"authorization_code"},
		"code":         {code},
		"redirect_uri": {redirectURI},
	}, &r); err != nil {
		return nil, err
	}
	if r.Error != "" {
		return nil, fmt.Errorf("reddit token exchange error: %s", r.Error)
	}
	if r.AccessToken == "" {
		return nil, fmt.Errorf("reddit token exchange returned no access_token")
	}
	tokens := map[string]string{accessSecret: r.AccessToken}
	if r.RefreshToken != "" {
		tokens[refreshSecret] = r.RefreshToken
	}
	externalID, label := redditAccount(ctx, r.AccessToken)
	res := &ExchangeResult{
		Tokens:       tokens,
		ExternalID:   externalID,
		AccountLabel: label,
		Scopes:       strings.Fields(r.Scope),
	}
	if r.ExpiresIn > 0 {
		res.ExpiresAt = time.Now().Add(time.Duration(r.ExpiresIn) * time.Second).Unix()
	}
	return res, nil
}

// redditAccount fetches the connected account's name + id. Bearer token in the
// header, User-Agent required. Best-effort — a miss yields empty labels.
func redditAccount(ctx context.Context, accessToken string) (externalID, label string) {
	var u struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	headers := map[string]string{
		"Authorization": "Bearer " + accessToken,
		"User-Agent":    redditUserAgent,
	}
	if err := oauthGetJSON(ctx, redditProvider, redditMeURL, headers, &u); err != nil {
		return "", ""
	}
	return u.ID, u.Name
}

// redditRevoke best-effort revokes the token at Reddit on disconnect (Basic client
// auth + form token). Never fails the disconnect.
func redditRevoke(ctx context.Context, creds OAuthConfig, token string) error {
	if strings.TrimSpace(token) == "" || strings.TrimSpace(creds.ClientID) == "" {
		return nil
	}
	headers := map[string]string{
		"Authorization": basicAuth(creds.ClientID, creds.ClientSecret),
		"User-Agent":    redditUserAgent,
	}
	return oauthPostForm(ctx, redditProvider, redditRevokeURL, headers, url.Values{
		"token":           {token},
		"token_type_hint": {"access_token"},
	}, nil)
}
