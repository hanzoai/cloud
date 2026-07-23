package integrations

// microsoft.go registers Microsoft Advertising (formerly Bing Ads) as an ORG OAuth
// connector. It is the Microsoft-identity-platform v2.0 authorization-code flow: one
// connection custodies an access token + refresh token (offline_access) in the org's
// KMS namespace under access_token/refresh_token; the ads reader rides them via
// integrations.TokenFor(org, "microsoft_ads", …).
//
// Creds are ENV-injected (KMS-synced): MICROSOFT_ADS_CLIENT_ID +
// MICROSOFT_ADS_CLIENT_SECRET. Microsoft offers a single ads scope
// (msads.manage) — there is no read-only variant — so that scope is the minimum to
// read reporting; it is requested honestly rather than pretending a narrower grant
// exists. Calling the Ads API additionally requires a developer token, supplied by
// the ads reader at call time (not part of custody). Linking a client's account is
// an org-admin action.

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	microsoftProvider          = "microsoft_ads"
	microsoftClientIDEnv       = "MICROSOFT_ADS_CLIENT_ID"
	microsoftClientSecretEnv   = "MICROSOFT_ADS_CLIENT_SECRET"
	microsoftAdsManagementScop = "https://ads.microsoft.com/msads.manage"
)

var (
	microsoftAuthURL  = "https://login.microsoftonline.com/common/oauth2/v2.0/authorize"
	microsoftTokenURL = "https://login.microsoftonline.com/common/oauth2/v2.0/token"
)

// microsoftScopes: the ads-manage scope (Microsoft's only ads scope), a refresh
// token (offline_access), and openid+email for the account label.
var microsoftScopes = []string{
	microsoftAdsManagementScop,
	"offline_access",
	"openid",
	"email",
}

func init() {
	register(&Provider{
		ID:           microsoftProvider,
		Name:         "Microsoft Advertising",
		Description:  "Connect Microsoft Advertising (Bing Ads) to read campaigns and performance.",
		Category:     "Advertising",
		AdminOnly:    true,
		Scopes:       microsoftScopes,
		RedirectPath: callbackPath(microsoftProvider),
		Secrets:      []string{accessSecret, refreshSecret},
		Configured:   microsoftConfigured,
		Creds:        microsoftCreds,
		Authorize:    microsoftAuthorize,
		Exchange:     microsoftExchange,
	})
}

func microsoftCreds() OAuthConfig {
	return OAuthConfig{
		ClientID:     strings.TrimSpace(os.Getenv(microsoftClientIDEnv)),
		ClientSecret: strings.TrimSpace(os.Getenv(microsoftClientSecretEnv)),
	}
}

func microsoftConfigured() bool { return microsoftCreds().ClientID != "" }

// microsoftAuthorize builds the v2.0 consent URL. response_mode=query keeps the
// code on the redirect query the generic callback reads.
func microsoftAuthorize(creds OAuthConfig, redirectURI, state string) (string, error) {
	q := url.Values{
		"client_id":     {creds.ClientID},
		"response_type": {"code"},
		"redirect_uri":  {redirectURI},
		"scope":         {strings.Join(microsoftScopes, " ")},
		"state":         {state},
		"response_mode": {"query"},
	}
	return microsoftAuthURL + "?" + q.Encode(), nil
}

type microsoftTokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
	ExpiresIn    int64  `json:"expires_in"`
	Scope        string `json:"scope"`
	Error        string `json:"error"`
	ErrorDesc    string `json:"error_description"`
}

// microsoftExchange trades the code for access + refresh tokens and seals both.
// Requires the client secret; a deployment with only the id fails honestly.
func microsoftExchange(ctx context.Context, creds OAuthConfig, redirectURI, code string) (*ExchangeResult, error) {
	if strings.TrimSpace(creds.ClientSecret) == "" {
		return nil, fmt.Errorf("microsoft ads integration is not fully configured on this deployment: MICROSOFT_ADS_CLIENT_SECRET is not set")
	}
	var r microsoftTokenResponse
	if err := oauthPostForm(ctx, microsoftProvider, microsoftTokenURL, nil, url.Values{
		"client_id":     {creds.ClientID},
		"client_secret": {creds.ClientSecret},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"grant_type":    {"authorization_code"},
		"scope":         {strings.Join(microsoftScopes, " ")},
	}, &r); err != nil {
		return nil, err
	}
	if r.Error != "" {
		return nil, fmt.Errorf("microsoft token exchange error: %s (%s)", r.Error, r.ErrorDesc)
	}
	if r.AccessToken == "" {
		return nil, fmt.Errorf("microsoft token exchange returned no access_token")
	}
	tokens := map[string]string{accessSecret: r.AccessToken}
	if r.RefreshToken != "" {
		tokens[refreshSecret] = r.RefreshToken
	}
	externalID, label := microsoftAccount(r.IDToken)
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

// microsoftAccount derives the account label from the id_token's claims (email /
// preferred_username, subject / object id). The id_token is decorative metadata,
// NOT an authentication input — the access token is the credential. Best-effort:
// absent/malformed claims yield empty labels.
func microsoftAccount(idToken string) (externalID, label string) {
	claims := jwtClaims(idToken)
	if claims == nil {
		return "", ""
	}
	label = claimString(claims, "email", "preferred_username", "upn")
	externalID = claimString(claims, "oid", "sub")
	return externalID, label
}
