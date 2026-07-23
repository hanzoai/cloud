package integrations

// meta.go registers Meta (Facebook + Instagram) as an ORG OAuth connector on the
// integrations plane — the highest-value Account-Linking step of the Business AI
// Guide ("connect the client's Meta Business Manager"). One connection custodies a
// long-lived Meta user access token in the org's KMS namespace under "access_token";
// the ads pull, page publish, and insights readers all ride it via
// integrations.TokenFor(org, "meta_ads", "access_token").
//
// Credential acquisition is the framework's 3-legged OAuth (Authorize → Exchange),
// the same shape as google.go. On exchange the short-lived code-token is immediately
// traded for a LONG-LIVED token (fb_exchange_token, ~60 days) — Meta issues no
// refresh token, so the long-lived token IS the custodied credential and its expiry
// is recorded. Creds are ENV-injected (KMS-synced, never in code): META_APP_ID +
// META_APP_SECRET. Until both are present Configured() is false: the card shows
// Connect but available=false and connect returns an honest 503.
//
// SCOPE is least-privilege: read + insights only (ads_read/read_insights, page +
// instagram read). The write/spend scope (ads_management) is added the day a
// campaign-execution endpoint is wired, never speculatively — the same discipline
// cloudflare.go documents. Linking a client's ad account is an org-admin action.

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	metaProvider     = "meta_ads"
	metaAppIDEnv     = "META_APP_ID"
	metaAppSecretEnv = "META_APP_SECRET"
)

// Endpoint URLs are package vars (not consts) so a test can point the exchange /
// account calls at a mock server. They are never mutated in production.
var (
	metaAuthURL  = "https://www.facebook.com/v21.0/dialog/oauth"
	metaTokenURL = "https://graph.facebook.com/v21.0/oauth/access_token"
	metaMeURL    = "https://graph.facebook.com/v21.0/me"
	metaPermsURL = "https://graph.facebook.com/v21.0/me/permissions"
)

// metaScopes is the requested permission set: ads + page + instagram + insights
// READ. ads_management (campaign create/spend) is intentionally absent until the
// execution path exists.
var metaScopes = []string{
	"ads_read",
	"business_management",
	"pages_show_list",
	"pages_read_engagement",
	"read_insights",
	"instagram_basic",
}

func init() {
	register(&Provider{
		ID:           metaProvider,
		Name:         "Meta Ads",
		Description:  "Connect Meta Business Manager — Facebook & Instagram Ads and Pages — to read campaigns, audiences, and insights.",
		Category:     "Advertising",
		AdminOnly:    true,
		Scopes:       metaScopes,
		RedirectPath: callbackPath(metaProvider),
		Secrets:      []string{accessSecret},
		Configured:   metaConfigured,
		Creds:        metaCreds,
		Authorize:    metaAuthorize,
		Exchange:     metaExchange,
		Revoke:       metaRevoke,
	})
}

func metaCreds() OAuthConfig {
	return OAuthConfig{
		ClientID:     strings.TrimSpace(os.Getenv(metaAppIDEnv)),
		ClientSecret: strings.TrimSpace(os.Getenv(metaAppSecretEnv)),
	}
}

// metaConfigured gates on the app id (the card lights up before the KMS-synced
// secret lands); the secret is validated at the token exchange.
func metaConfigured() bool { return metaCreds().ClientID != "" }

// metaAuthorize builds the consent URL. Meta accepts a comma-separated scope list;
// state carries the CSRF/org binding.
func metaAuthorize(creds OAuthConfig, redirectURI, state string) (string, error) {
	q := url.Values{
		"client_id":     {creds.ClientID},
		"redirect_uri":  {redirectURI},
		"response_type": {"code"},
		"scope":         {strings.Join(metaScopes, ",")},
		"state":         {state},
	}
	return metaAuthURL + "?" + q.Encode(), nil
}

// metaTokenResponse is Meta's oauth/access_token response.
type metaTokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int64  `json:"expires_in"`
	Error       *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    int    `json:"code"`
	} `json:"error"`
}

// metaExchange trades the OAuth code for a short-lived token, then immediately
// exchanges THAT for a long-lived token (Meta issues no refresh token). The
// long-lived token is the custodied credential. Requires the app secret; a
// deployment with only the id fails with an honest, specific reason.
func metaExchange(ctx context.Context, creds OAuthConfig, redirectURI, code string) (*ExchangeResult, error) {
	if strings.TrimSpace(creds.ClientSecret) == "" {
		return nil, fmt.Errorf("meta integration is not fully configured on this deployment: META_APP_SECRET is not set")
	}
	// Leg 1: code → short-lived user token.
	var short metaTokenResponse
	if err := oauthPostForm(ctx, metaProvider, metaTokenURL, nil, url.Values{
		"client_id":     {creds.ClientID},
		"client_secret": {creds.ClientSecret},
		"redirect_uri":  {redirectURI},
		"code":          {code},
	}, &short); err != nil {
		return nil, err
	}
	if short.Error != nil {
		return nil, fmt.Errorf("meta token exchange error: %s", short.Error.Message)
	}
	if short.AccessToken == "" {
		return nil, fmt.Errorf("meta token exchange returned no access_token")
	}
	// Leg 2: short-lived → long-lived (~60 days). Best-effort: if the exchange
	// fails, fall back to the short token so the connection still lands, but never
	// fabricate an expiry.
	access, expiresIn := short.AccessToken, short.ExpiresIn
	var long metaTokenResponse
	if err := oauthPostForm(ctx, metaProvider, metaTokenURL, nil, url.Values{
		"grant_type":        {"fb_exchange_token"},
		"client_id":         {creds.ClientID},
		"client_secret":     {creds.ClientSecret},
		"fb_exchange_token": {short.AccessToken},
	}, &long); err == nil && long.Error == nil && long.AccessToken != "" {
		access, expiresIn = long.AccessToken, long.ExpiresIn
	}

	externalID, label := metaAccount(ctx, access)
	res := &ExchangeResult{
		Tokens:       map[string]string{accessSecret: access},
		ExternalID:   externalID,
		AccountLabel: label,
		Scopes:       metaScopes,
	}
	if expiresIn > 0 {
		res.ExpiresAt = time.Now().Add(time.Duration(expiresIn) * time.Second).Unix()
	}
	return res, nil
}

// metaAccount fetches the connected user's id + name for the connection row. The
// token rides the Authorization header (never the URL). Best-effort: a failure
// yields empty labels rather than an error — the token is already valid.
func metaAccount(ctx context.Context, accessToken string) (externalID, label string) {
	var u struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	headers := map[string]string{"Authorization": "Bearer " + accessToken}
	if err := oauthGetJSON(ctx, metaProvider, metaMeURL+"?fields=id,name", headers, &u); err != nil {
		return "", ""
	}
	return u.ID, u.Name
}

// metaRevoke best-effort de-authorizes the app for the user on disconnect. The
// token rides the Authorization header. A revoke failure never fails the disconnect.
func metaRevoke(ctx context.Context, _ OAuthConfig, token string) error {
	if strings.TrimSpace(token) == "" {
		return nil
	}
	req := map[string]string{"Authorization": "Bearer " + token}
	return oauthDelete(ctx, metaProvider, metaPermsURL, req)
}
