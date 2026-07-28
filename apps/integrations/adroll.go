package integrations

// adroll.go registers AdRoll as an ORG OAuth connector, completing the retargeting/
// display side of the ad-platform set (meta/google/microsoft/tiktok/reddit/linkedin).
// Standard OAuth2 authorization-code with a form token exchange: one connection
// custodies an access token + refresh token in the org's KMS namespace; the ads
// reader rides them via integrations.TokenFor(org, "adroll", …).
//
// Creds: ADROLL_CLIENT_ID + ADROLL_CLIENT_SECRET. AdRoll grants API access per the
// registered app (no scope list on the authorize URL, like TikTok). Linking a client's
// AdRoll account is an org-admin action.

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	adrollProvider        = "adroll"
	adrollClientIDEnv     = "ADROLL_CLIENT_ID"
	adrollClientSecretEnv = "ADROLL_CLIENT_SECRET"
)

var (
	adrollAuthURL  = "https://services.adroll.com/auth/authorize"
	adrollTokenURL = "https://services.adroll.com/auth/token"
	adrollMeURL    = "https://services.adroll.com/api/v1/organization/get"
)

func init() {
	register(&Provider{
		ID:           adrollProvider,
		Name:         "AdRoll",
		Description:  "Connect AdRoll to read retargeting and display campaign performance.",
		Category:     "Advertising",
		AdminOnly:    true,
		RedirectPath: callbackPath(adrollProvider),
		Secrets:      []string{accessSecret, refreshSecret},
		Configured:   adrollConfigured,
		Creds:        adrollCreds,
		Authorize:    adrollAuthorize,
		Exchange:     adrollExchange,
	})
}

func adrollCreds() OAuthConfig {
	return OAuthConfig{
		ClientID:     strings.TrimSpace(os.Getenv(adrollClientIDEnv)),
		ClientSecret: strings.TrimSpace(os.Getenv(adrollClientSecretEnv)),
	}
}

func adrollConfigured() bool { return adrollCreds().ClientID != "" }

func adrollAuthorize(creds OAuthConfig, redirectURI, state string) (string, error) {
	q := url.Values{
		"response_type": {"code"},
		"client_id":     {creds.ClientID},
		"redirect_uri":  {redirectURI},
		"state":         {state},
	}
	return adrollAuthURL + "?" + q.Encode(), nil
}

type adrollTokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	Scope        string `json:"scope"`
	TokenType    string `json:"token_type"`
	Error        string `json:"error"`
	ErrorDesc    string `json:"error_description"`
}

// adrollExchange trades the code for access + refresh tokens and seals both. Requires
// the client secret; a deployment with only the id fails with an honest reason.
func adrollExchange(ctx context.Context, creds OAuthConfig, redirectURI, code string) (*ExchangeResult, error) {
	if strings.TrimSpace(creds.ClientSecret) == "" {
		return nil, fmt.Errorf("adroll integration is not fully configured on this deployment: ADROLL_CLIENT_SECRET is not set")
	}
	var r adrollTokenResponse
	if err := oauthPostForm(ctx, adrollProvider, adrollTokenURL, nil, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"client_id":     {creds.ClientID},
		"client_secret": {creds.ClientSecret},
	}, &r); err != nil {
		return nil, err
	}
	if r.Error != "" {
		return nil, fmt.Errorf("adroll token exchange error: %s (%s)", r.Error, r.ErrorDesc)
	}
	if r.AccessToken == "" {
		return nil, fmt.Errorf("adroll token exchange returned no access_token")
	}
	tokens := map[string]string{accessSecret: r.AccessToken}
	if r.RefreshToken != "" {
		tokens[refreshSecret] = r.RefreshToken
	}
	externalID, label := adrollAccount(ctx, r.AccessToken)
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

// adrollAccount fetches the connected organization's id + name for the connection
// row. Bearer token in the header. Best-effort — a miss yields empty labels.
func adrollAccount(ctx context.Context, accessToken string) (externalID, label string) {
	var r struct {
		Results struct {
			EID  string `json:"eid"`
			Name string `json:"name"`
		} `json:"results"`
	}
	headers := map[string]string{"Authorization": "Bearer " + accessToken}
	if err := oauthGetJSON(ctx, adrollProvider, adrollMeURL, headers, &r); err != nil {
		return "", ""
	}
	return r.Results.EID, r.Results.Name
}
