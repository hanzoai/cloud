package integrations

// x.go registers X as an ORG OAuth connector, and it is the ONLY one. It is the
// OAuth 2.0 Authorization-Code flow with PKCE that X mandates. One connection
// custodies an access token + refresh token (offline.access) in the org's KMS
// namespace; a reader rides them via integrations.TokenFor(org, "x", …), which
// is the id apps/auto's X connector already asks for.
//
// There were two, this one and a catalog declaration under the id "x", both
// reaching X and disagreeing about everything that matters. The declaration
// requested offline.access — the scope whose only purpose is a refresh token —
// while the generic exchange decodes no refresh_token and names only the access
// secret, so an org that connected that card held a credential that dies in two
// hours with no way to renew it; and its PKCE verifier was a public constant
// compiled into the binary, which is the property PKCE exists to deny. One
// platform, one connector: the id is X's own name, the flow is this one.
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
// (the meta_ads discipline). Creds: X_CLIENT_ID + X_CLIENT_SECRET. Linking
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

	"github.com/hanzoai/iam/pkg/pkce"
)

const (
	xProvider        = "x"
	xClientIDEnv     = "X_CLIENT_ID"
	xClientSecretEnv = "X_CLIENT_SECRET"
)

var (
	xAuthURL   = "https://twitter.com/i/oauth2/authorize"
	xTokenURL  = "https://api.twitter.com/2/oauth2/token"
	xMeURL     = "https://api.twitter.com/2/users/me"
	xRevokeURL = "https://api.twitter.com/2/oauth2/revoke"
)

// xScopes: read + offline. tweet.write (post) is intentionally absent until a
// publish path is wired.
var xScopes = []string{"tweet.read", "users.read", "offline.access"}

func init() {
	register(&Provider{
		ID:           xProvider,
		Name:         "X",
		Description:  "Connect X to read the connected account, posts, and audience.",
		Category:     "Social",
		AdminOnly:    true,
		Scopes:       xScopes,
		RedirectPath: callbackPath(xProvider),
		Secrets:      []string{accessSecret, refreshSecret},
		Configured:   xConfigured,
		Creds:        xCreds,
		Authorize:    xAuthorize,
		Exchange:     xExchange,
		Revoke:       xRevoke,
	})
}

func xCreds() OAuthConfig {
	return OAuthConfig{
		ClientID:     strings.TrimSpace(os.Getenv(xClientIDEnv)),
		ClientSecret: strings.TrimSpace(os.Getenv(xClientSecretEnv)),
	}
}

func xConfigured() bool { return xCreds().ClientID != "" }

// xVerifier derives the PKCE code_verifier from the app credential (see file
// header). base64url(sha256(...)) is 43 chars of the RFC-7636 unreserved set — a valid
// verifier. Deriving from BOTH id and secret binds it to the specific app.
//
// SAFE ONLY FOR A CONFIDENTIAL CLIENT: this verifier is per-app-CONSTANT, not
// per-flow, so PKCE here is pure defense-in-depth — the real interception
// protection is the client_secret sent (Basic auth) on the token exchange, without
// which a leaked verifier is useless. If a PUBLIC-client or PKCE-only X
// variant is ever added (no client_secret at exchange), this constant verifier
// becomes the SOLE protection and MUST become per-flow (thread `state` into
// Exchange, random verifier per authorize) — do not reuse this as-is there. The
// residual is worth stating plainly: the authorize URL is public, so it is an
// offline oracle for a guess at the client secret. X's own secret length makes
// that impractical; an operator-chosen credential would not.
func xVerifier(creds OAuthConfig) string {
	sum := sha256.Sum256([]byte(creds.ClientSecret + "|" + creds.ClientID + "|hanzo-x-pkce-v1"))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func xAuthorize(creds OAuthConfig, redirectURI, state string) (string, error) {
	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {creds.ClientID},
		"redirect_uri":          {redirectURI},
		"scope":                 {strings.Join(xScopes, " ")},
		"state":                 {state},
		"code_challenge":        {pkce.Challenge(xVerifier(creds))},
		"code_challenge_method": {"S256"},
	}
	return xAuthURL + "?" + q.Encode(), nil
}

type xTokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	Scope        string `json:"scope"`
	TokenType    string `json:"token_type"`
	Error        string `json:"error"`
	ErrorDesc    string `json:"error_description"`
}

// xExchange trades the code for tokens and seals them. The confidential client
// authenticates with HTTP Basic; the PKCE verifier (derived from the secret) rides the
// form body. Requires the client secret.
func xExchange(ctx context.Context, creds OAuthConfig, redirectURI, code string) (*ExchangeResult, error) {
	if strings.TrimSpace(creds.ClientSecret) == "" {
		return nil, fmt.Errorf("x integration is not fully configured on this deployment: X_CLIENT_SECRET is not set")
	}
	headers := map[string]string{"Authorization": basicAuth(creds.ClientID, creds.ClientSecret)}
	var r xTokenResponse
	if err := oauthPostForm(ctx, xProvider, xTokenURL, headers, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"client_id":     {creds.ClientID},
		"code_verifier": {xVerifier(creds)},
	}, &r); err != nil {
		return nil, err
	}
	if r.Error != "" {
		return nil, fmt.Errorf("x token exchange error: %s (%s)", r.Error, r.ErrorDesc)
	}
	if r.AccessToken == "" {
		return nil, fmt.Errorf("x token exchange returned no access_token")
	}
	tokens := map[string]string{accessSecret: r.AccessToken}
	if r.RefreshToken != "" {
		tokens[refreshSecret] = r.RefreshToken
	}
	externalID, label := xAccount(ctx, r.AccessToken)
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

// xAccount fetches the connected account's id + username. Bearer token in the
// header. Best-effort — a miss yields empty labels.
func xAccount(ctx context.Context, accessToken string) (externalID, label string) {
	var r struct {
		Data struct {
			ID       string `json:"id"`
			Username string `json:"username"`
			Name     string `json:"name"`
		} `json:"data"`
	}
	headers := map[string]string{"Authorization": "Bearer " + accessToken}
	if err := oauthGetJSON(ctx, xProvider, xMeURL, headers, &r); err != nil {
		return "", ""
	}
	label = r.Data.Username
	if label == "" {
		label = r.Data.Name
	}
	return r.Data.ID, label
}

// xRevoke best-effort revokes the token at X on disconnect (Basic client auth +
// form token). Never fails the disconnect.
func xRevoke(ctx context.Context, creds OAuthConfig, token string) error {
	if strings.TrimSpace(token) == "" || strings.TrimSpace(creds.ClientID) == "" {
		return nil
	}
	headers := map[string]string{"Authorization": basicAuth(creds.ClientID, creds.ClientSecret)}
	return oauthPostForm(ctx, xProvider, xRevokeURL, headers, url.Values{
		"token":           {token},
		"token_type_hint": {"access_token"},
	}, nil)
}
