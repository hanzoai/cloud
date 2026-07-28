package integrations

// linkedin.go registers LinkedIn Ads (Marketing) as an ORG OAuth connector. One
// connection custodies an access token (and a refresh token when the app is approved
// for refresh) in the org's KMS namespace; the ads reader rides them via
// integrations.TokenFor(org, "linkedin_ads", …).
//
// Standard OAuth2 authorization-code with a form token exchange. Creds:
// LINKEDIN_ADS_CLIENT_ID + LINKEDIN_ADS_CLIENT_SECRET. Scope is least-privilege —
// r_ads + r_ads_reporting (read + reporting) plus openid/profile/email for the
// account label; the write scope (rw_ads) is added when a campaign-write path is
// wired. LinkedIn issues a refresh token only to approved apps; it is sealed when
// present, absent otherwise (the access token stands alone). Linking is an org-admin
// action.

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	linkedinProvider        = "linkedin_ads"
	linkedinClientIDEnv     = "LINKEDIN_ADS_CLIENT_ID"
	linkedinClientSecretEnv = "LINKEDIN_ADS_CLIENT_SECRET"
)

var (
	linkedinAuthURL  = "https://www.linkedin.com/oauth/v2/authorization"
	linkedinTokenURL = "https://www.linkedin.com/oauth/v2/accessToken"
	linkedinMeURL    = "https://api.linkedin.com/v2/userinfo"
)

// linkedinScopes: read + reporting + identity (openid/profile/email). rw_ads (write)
// is intentionally absent until wired.
var linkedinScopes = []string{"r_ads", "r_ads_reporting", "openid", "profile", "email"}

func init() {
	register(&Provider{
		ID:           linkedinProvider,
		Name:         "LinkedIn Ads",
		Description:  "Connect LinkedIn Marketing to read ad accounts, campaigns, and reporting.",
		Category:     "Advertising",
		AdminOnly:    true,
		Scopes:       linkedinScopes,
		RedirectPath: callbackPath(linkedinProvider),
		Secrets:      []string{accessSecret, refreshSecret},
		Configured:   linkedinConfigured,
		Creds:        linkedinCreds,
		Authorize:    linkedinAuthorize,
		Exchange:     linkedinExchange,
	})
}

func linkedinCreds() OAuthConfig {
	return OAuthConfig{
		ClientID:     strings.TrimSpace(os.Getenv(linkedinClientIDEnv)),
		ClientSecret: strings.TrimSpace(os.Getenv(linkedinClientSecretEnv)),
	}
}

func linkedinConfigured() bool { return linkedinCreds().ClientID != "" }

func linkedinAuthorize(creds OAuthConfig, redirectURI, state string) (string, error) {
	q := url.Values{
		"response_type": {"code"},
		"client_id":     {creds.ClientID},
		"redirect_uri":  {redirectURI},
		"state":         {state},
		"scope":         {strings.Join(linkedinScopes, " ")},
	}
	return linkedinAuthURL + "?" + q.Encode(), nil
}

type linkedinTokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
	ExpiresIn    int64  `json:"expires_in"`
	Scope        string `json:"scope"`
	Error        string `json:"error"`
	ErrorDesc    string `json:"error_description"`
}

// linkedinExchange trades the code for tokens and seals them. Requires the secret.
func linkedinExchange(ctx context.Context, creds OAuthConfig, redirectURI, code string) (*ExchangeResult, error) {
	if strings.TrimSpace(creds.ClientSecret) == "" {
		return nil, fmt.Errorf("linkedin ads integration is not fully configured on this deployment: LINKEDIN_ADS_CLIENT_SECRET is not set")
	}
	var r linkedinTokenResponse
	if err := oauthPostForm(ctx, linkedinProvider, linkedinTokenURL, nil, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"client_id":     {creds.ClientID},
		"client_secret": {creds.ClientSecret},
	}, &r); err != nil {
		return nil, err
	}
	if r.Error != "" {
		return nil, fmt.Errorf("linkedin token exchange error: %s (%s)", r.Error, r.ErrorDesc)
	}
	if r.AccessToken == "" {
		return nil, fmt.Errorf("linkedin token exchange returned no access_token")
	}
	tokens := map[string]string{accessSecret: r.AccessToken}
	if r.RefreshToken != "" {
		tokens[refreshSecret] = r.RefreshToken
	}
	externalID, label := linkedinAccount(ctx, r.AccessToken, r.IDToken)
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

// linkedinAccount resolves the account label: the OIDC userinfo endpoint first
// (Bearer), falling back to the id_token claims. Best-effort — a miss yields empty
// labels.
func linkedinAccount(ctx context.Context, accessToken, idToken string) (externalID, label string) {
	var u struct {
		Sub   string `json:"sub"`
		Email string `json:"email"`
		Name  string `json:"name"`
	}
	headers := map[string]string{"Authorization": "Bearer " + accessToken}
	if err := oauthGetJSON(ctx, linkedinProvider, linkedinMeURL, headers, &u); err == nil && u.Sub != "" {
		label = u.Email
		if label == "" {
			label = u.Name
		}
		return u.Sub, label
	}
	if claims := jwtClaims(idToken); claims != nil {
		return claimString(claims, "sub"), claimString(claims, "email", "name")
	}
	return "", ""
}
