package integrations

// tiktok.go registers TikTok Ads (TikTok for Business / Marketing API) as an ORG
// OAuth connector. One connection custodies a long-lived access token in the org's
// KMS namespace under access_token; the ads reader rides it via
// integrations.TokenFor(org, "tiktok_ads", "access_token"). TikTok issues no refresh
// token — the access token is durable — so access_token is the sole custodied secret.
//
// The flow differs from the OAuth2 norm in two provider-specific ways the framework
// already tolerates: (1) the consent URL uses app_id (not client_id) and carries no
// scope (TikTok scopes are configured on the app, granted at consent); (2) the token
// exchange is a JSON POST whose credential field is `secret` and whose grant field is
// `auth_code`. The generic callback passes the redirect `code` (current TikTok
// Marketing API returns both `code` and `auth_code`); this Exchange forwards it as
// auth_code. Creds: TIKTOK_ADS_APP_ID + TIKTOK_ADS_SECRET (app_id → ClientID so the
// framework's OAuth-configured gate holds). Linking is an org-admin action.

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
)

const (
	tiktokProvider  = "tiktok_ads"
	tiktokAppIDEnv  = "TIKTOK_ADS_APP_ID"
	tiktokSecretEnv = "TIKTOK_ADS_SECRET"
)

var (
	tiktokAuthURL  = "https://business-api.tiktok.com/portal/auth"
	tiktokTokenURL = "https://business-api.tiktok.com/open_api/v1.3/oauth2/access_token/"
)

func init() {
	register(&Provider{
		ID:           tiktokProvider,
		Name:         "TikTok Ads",
		Description:  "Connect TikTok for Business to read ad accounts, campaigns, and performance.",
		Category:     "Advertising",
		AdminOnly:    true,
		Scopes:       nil, // TikTok scopes are configured on the app, not requested in the URL.
		RedirectPath: callbackPath(tiktokProvider),
		Secrets:      []string{accessSecret},
		Configured:   tiktokConfigured,
		Creds:        tiktokCreds,
		Authorize:    tiktokAuthorize,
		Exchange:     tiktokExchange,
	})
}

func tiktokCreds() OAuthConfig {
	return OAuthConfig{
		ClientID:     strings.TrimSpace(os.Getenv(tiktokAppIDEnv)),
		ClientSecret: strings.TrimSpace(os.Getenv(tiktokSecretEnv)),
	}
}

func tiktokConfigured() bool { return tiktokCreds().ClientID != "" }

// tiktokAuthorize builds the portal consent URL. app_id is TikTok's client
// identifier; state carries the CSRF/org binding.
func tiktokAuthorize(creds OAuthConfig, redirectURI, state string) (string, error) {
	q := url.Values{
		"app_id":       {creds.ClientID},
		"redirect_uri": {redirectURI},
		"state":        {state},
	}
	return tiktokAuthURL + "?" + q.Encode(), nil
}

// tiktokTokenResponse is the wrapped Marketing API envelope: code==0 is success,
// the token material is under data.
type tiktokTokenResponse struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    struct {
		AccessToken   string   `json:"access_token"`
		AdvertiserIDs []string `json:"advertiser_ids"`
		TokenType     string   `json:"token_type"`
	} `json:"data"`
}

// tiktokExchange trades the auth code for the durable access token via a JSON POST
// (credential in the body, never the URL) and seals it. code!=0 is an honest error.
func tiktokExchange(ctx context.Context, creds OAuthConfig, _ /*redirectURI*/, code string) (*ExchangeResult, error) {
	if strings.TrimSpace(creds.ClientSecret) == "" {
		return nil, fmt.Errorf("tiktok ads integration is not fully configured on this deployment: TIKTOK_ADS_SECRET is not set")
	}
	body := map[string]string{
		"app_id":     creds.ClientID,
		"secret":     creds.ClientSecret,
		"auth_code":  code,
		"grant_type": "authorization_code",
	}
	var r tiktokTokenResponse
	if err := oauthPostJSON(ctx, tiktokProvider, tiktokTokenURL, nil, body, &r); err != nil {
		return nil, err
	}
	if r.Code != 0 {
		return nil, fmt.Errorf("tiktok token exchange error: code %d %s", r.Code, r.Message)
	}
	if r.Data.AccessToken == "" {
		return nil, fmt.Errorf("tiktok token exchange returned no access_token")
	}
	var externalID string
	if len(r.Data.AdvertiserIDs) > 0 {
		externalID = r.Data.AdvertiserIDs[0]
	}
	return &ExchangeResult{
		Tokens:       map[string]string{accessSecret: r.Data.AccessToken},
		ExternalID:   externalID,
		AccountLabel: externalID,
	}, nil
}
