package integrations

// twitter.go registers X (Twitter) as an ORG OAuth connector. It is the OAuth 2.0
// Authorization-Code flow with PKCE that X mandates. One connection custodies an
// access token + refresh token (offline.access) in the org's KMS namespace; the
// social reader rides them via integrations.TokenFor(org, "twitter", …).
//
// PKCE without a plane change. X requires a code_challenge on authorize and a matching
// code_verifier on the token exchange, but the framework's Exchange never receives the
// request state — so a per-flow verifier cannot be threaded without changing the
// shared Authorize/Exchange contract every other provider depends on. Instead the
// verifier is derived deterministically from the app's client SECRET, which BOTH
// Authorize and Exchange receive. That verifier is:
//   - server-side only: the authorize URL carries only its SHA-256 (S256) hash, never
//     the verifier itself;
//   - as secret as the client secret (an attacker who intercepts an authorization code
//     still cannot exchange it without the verifier, which is a preimage of a hash of
//     the client secret they do not hold);
//   - rotated with the client secret.
// So PKCE's interception guarantee holds, at the confidential-client trust level,
// with zero changes to the shared plane.
//
// The confidential client authenticates the token exchange with HTTP Basic
// (client_id:client_secret). Scope is least-privilege READ: tweet.read + users.read +
// offline.access; the write scope (tweet.write) is added when a publish path is wired
// (the meta_ads discipline). Creds: TWITTER_CLIENT_ID + TWITTER_CLIENT_SECRET. Linking
// a client's X account is an org-admin action.

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	twitterProvider        = "twitter"
	twitterClientIDEnv     = "TWITTER_CLIENT_ID"
	twitterClientSecretEnv = "TWITTER_CLIENT_SECRET"
)

var (
	twitterAuthURL   = "https://twitter.com/i/oauth2/authorize"
	twitterTokenURL  = "https://api.twitter.com/2/oauth2/token"
	twitterMeURL     = "https://api.twitter.com/2/users/me"
	twitterRevokeURL = "https://api.twitter.com/2/oauth2/revoke"
)

// twitterScopes: read + offline. tweet.write (post) is intentionally absent until a
// publish path is wired.
var twitterScopes = []string{"tweet.read", "users.read", "offline.access"}

func init() {
	register(&Provider{
		ID:           twitterProvider,
		Name:         "X (Twitter)",
		Description:  "Connect X (Twitter) to read the connected account, posts, and audience.",
		Category:     "Social",
		AdminOnly:    true,
		Scopes:       twitterScopes,
		RedirectPath: callbackPath(twitterProvider),
		Secrets:      []string{accessSecret, refreshSecret},
		Configured:   twitterConfigured,
		Creds:        twitterCreds,
		Authorize:    twitterAuthorize,
		Exchange:     twitterExchange,
		Revoke:       twitterRevoke,
	})
}

func twitterCreds() OAuthConfig {
	return OAuthConfig{
		ClientID:     strings.TrimSpace(os.Getenv(twitterClientIDEnv)),
		ClientSecret: strings.TrimSpace(os.Getenv(twitterClientSecretEnv)),
	}
}

func twitterConfigured() bool { return twitterCreds().ClientID != "" }

// twitterVerifier derives the PKCE code_verifier from the app credential (see file
// header). base64url(sha256(...)) is 43 chars of the RFC-7636 unreserved set — a valid
// verifier. Deriving from BOTH id and secret binds it to the specific app.
func twitterVerifier(creds OAuthConfig) string {
	sum := sha256.Sum256([]byte(creds.ClientSecret + "|" + creds.ClientID + "|hanzo-x-pkce-v1"))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// twitterChallenge is the S256 code_challenge for a verifier: base64url(sha256(v)).
func twitterChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func twitterAuthorize(creds OAuthConfig, redirectURI, state string) (string, error) {
	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {creds.ClientID},
		"redirect_uri":          {redirectURI},
		"scope":                 {strings.Join(twitterScopes, " ")},
		"state":                 {state},
		"code_challenge":        {twitterChallenge(twitterVerifier(creds))},
		"code_challenge_method": {"S256"},
	}
	return twitterAuthURL + "?" + q.Encode(), nil
}

type twitterTokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	Scope        string `json:"scope"`
	TokenType    string `json:"token_type"`
	Error        string `json:"error"`
	ErrorDesc    string `json:"error_description"`
}

// twitterExchange trades the code for tokens and seals them. The confidential client
// authenticates with HTTP Basic; the PKCE verifier (derived from the secret) rides the
// form body. Requires the client secret.
func twitterExchange(ctx context.Context, creds OAuthConfig, redirectURI, code string) (*ExchangeResult, error) {
	if strings.TrimSpace(creds.ClientSecret) == "" {
		return nil, fmt.Errorf("twitter integration is not fully configured on this deployment: TWITTER_CLIENT_SECRET is not set")
	}
	headers := map[string]string{"Authorization": basicAuth(creds.ClientID, creds.ClientSecret)}
	var r twitterTokenResponse
	if err := oauthPostForm(ctx, twitterProvider, twitterTokenURL, headers, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"client_id":     {creds.ClientID},
		"code_verifier": {twitterVerifier(creds)},
	}, &r); err != nil {
		return nil, err
	}
	if r.Error != "" {
		return nil, fmt.Errorf("twitter token exchange error: %s (%s)", r.Error, r.ErrorDesc)
	}
	if r.AccessToken == "" {
		return nil, fmt.Errorf("twitter token exchange returned no access_token")
	}
	tokens := map[string]string{accessSecret: r.AccessToken}
	if r.RefreshToken != "" {
		tokens[refreshSecret] = r.RefreshToken
	}
	externalID, label := twitterAccount(ctx, r.AccessToken)
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

// twitterAccount fetches the connected account's id + username. Bearer token in the
// header. Best-effort — a miss yields empty labels.
func twitterAccount(ctx context.Context, accessToken string) (externalID, label string) {
	var r struct {
		Data struct {
			ID       string `json:"id"`
			Username string `json:"username"`
			Name     string `json:"name"`
		} `json:"data"`
	}
	headers := map[string]string{"Authorization": "Bearer " + accessToken}
	if err := oauthGetJSON(ctx, twitterProvider, twitterMeURL, headers, &r); err != nil {
		return "", ""
	}
	label = r.Data.Username
	if label == "" {
		label = r.Data.Name
	}
	return r.Data.ID, label
}

// twitterRevoke best-effort revokes the token at X on disconnect (Basic client auth +
// form token). Never fails the disconnect.
func twitterRevoke(ctx context.Context, creds OAuthConfig, token string) error {
	if strings.TrimSpace(token) == "" || strings.TrimSpace(creds.ClientID) == "" {
		return nil
	}
	headers := map[string]string{"Authorization": basicAuth(creds.ClientID, creds.ClientSecret)}
	return oauthPostForm(ctx, twitterProvider, twitterRevokeURL, headers, url.Values{
		"token":           {token},
		"token_type_hint": {"access_token"},
	}, nil)
}
