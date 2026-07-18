package integrations

// cloudflare.go registers Cloudflare as an APIKEY connector on the integrations
// plane. The customer supplies a Cloudflare scoped API token — created
// least-privilege (DNS edit, zone read, Pages edit) — which is VERIFIED live via
// GET /user/tokens/verify (must be status:active) BEFORE it is sealed into the
// org's KMS namespace (/orgs/{org}/integrations/cloudflare/api_token). The store
// row holds only non-secret metadata (account id/label, the requested scopes);
// the token is never in the row, the response, or a log line.
//
// The same provider ALSO offers a browser OAuth path (Authorization Code, a
// confidential client with client_secret — the framework's OAuth pattern, no PKCE;
// see slack_link.go). A tokenless /connect starts the consent flow at Cloudflare
// and the exchanged access token is sealed to the SAME KMS coordinate as the
// apikey path (api_token), so the DNS provider layer never learns which method
// minted the token. The request picks the path: a token in the /connect body seals
// via apikey; its absence starts OAuth. The apikey path is always available; the
// OAuth leg activates only once Hanzo's registered Cloudflare OAuth app creds are
// present in ENV (CLOUDFLARE_OAUTH_CLIENT_ID/SECRET), else it answers an honest
// 503 pointing the caller at the token path. One framework, one provider, two
// credential-acquisition methods. See the connector HIP / hanzo dns wiring.

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

const (
	// cloudflareProvider is the :provider slug.
	cloudflareProvider = "cloudflare"
	// cloudflareTokenSecret is the KMS secret name the token is sealed under, at
	// /orgs/{org}/integrations/cloudflare/{name}.
	cloudflareTokenSecret = "api_token"
)

// cloudflareScopes is the least-privilege permission set a Hanzo Cloudflare token
// is expected to carry. Cloudflare's verify response does NOT disclose a token's
// scopes (a least-privilege token cannot read its own token object), so this is
// recorded as the REQUESTED/expected set — connection metadata, not a read-back.
//
// The set grows with the /v1/integrations/cloudflare asset plane, one capability at a time — a
// scope is added ONLY when its endpoint is actually callable, never speculatively.
// DNS+Pages back /v1/dns and /v1/integrations/cloudflare/pages; Workers Scripts/Routes back the
// wired /v1/integrations/cloudflare/workers. R2/KV/D1 are Phase-2 stubs (their handlers answer
// 501), so their scopes are NOT requested yet — add them alongside their wiring:
//
//	R2  → "Account:Workers R2 Storage:Edit"
//	KV  → "Account:Workers KV Storage:Edit"
//	D1  → "Account:D1:Edit"
var cloudflareScopes = []string{
	"Zone:DNS:Edit", "Zone:Read", "Account:Cloudflare Pages:Edit",
	"Account:Workers Scripts:Edit", "Zone:Workers Routes:Edit",
}

// cfHTTPClient is the shared client for Cloudflare verify/discovery calls. A tight
// timeout so a slow/hung Cloudflare never wedges a connect request.
var cfHTTPClient = &http.Client{Timeout: 15 * time.Second}

func init() {
	register(&Provider{
		ID:          cloudflareProvider,
		Name:        "Cloudflare",
		Description: "DNS, zones, and Pages via a least-privilege scoped API token.",
		Category:    "Infrastructure",
		Kind:        apiKeyKind,
		AdminOnly:   true,
		Scopes:      cloudflareScopes,
		Secrets:     []string{cloudflareTokenSecret},
		// Dual-path connector. apikey: the customer submits a scoped API token to
		// /connect (verify-before-seal). oauth: a tokenless /connect starts the
		// browser Authorization Code flow and the exchanged access token is sealed to
		// the SAME KMS coordinate (api_token), so the DNS layer is auth-agnostic.
		// Configured stays true — the apikey path is ALWAYS available; the OAuth leg
		// gates on its own app creds (Creds().ClientID) inside connect, so a missing
		// Cloudflare OAuth app degrades to an honest 503 without breaking apikey.
		Configured:   func() bool { return true },
		RedirectPath: callbackPath(cloudflareProvider),
		Creds:        cloudflareCreds,
		Authorize:    cloudflareAuthorize,
		Exchange:     cloudflareExchange,
		Verify:       cloudflareVerify,
	})
}

// cfAPIBase is Cloudflare's API v4 origin. Overridable via CLOUDFLARE_API_BASE for
// tests (an httptest server) and CF-compatible endpoints; read at call time so a
// test can set it per-process. The default is the real Cloudflare API.
func cfAPIBase() string {
	if v := strings.TrimSpace(os.Getenv("CLOUDFLARE_API_BASE")); v != "" {
		return strings.TrimRight(v, "/")
	}
	return "https://api.cloudflare.com/client/v4"
}

// cfEnvelope is the shared Cloudflare API v4 response envelope shape.
type cfEnvelope struct {
	Success bool `json:"success"`
	Errors  []struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"errors"`
}

// cfVerifyResult is GET /user/tokens/verify's result. It carries the token id +
// status only — NOT the token name or scopes.
type cfVerifyResult struct {
	cfEnvelope
	Result struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	} `json:"result"`
}

// cfAccountsResult is GET /accounts's result (best-effort account discovery).
type cfAccountsResult struct {
	cfEnvelope
	Result []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"result"`
}

// cloudflareVerify validates a Cloudflare scoped API token and returns the token
// to seal + non-secret account metadata. It FAILS CLOSED: a transport error, a
// non-2xx, an unsuccessful envelope, or a non-"active" status yields an error, so
// connect stores nothing. The returned error NEVER contains the token value.
// accountId/label are best-effort (a least-privilege token may be unable to list
// accounts); the caller-supplied AccountID hint wins when present.
func cloudflareVerify(ctx context.Context, in VerifyInput) (*ExchangeResult, error) {
	token := strings.TrimSpace(in.Token)
	if token == "" {
		return nil, fmt.Errorf("empty token")
	}
	status, tokenID, err := cfTokenStatus(ctx, token)
	if err != nil {
		return nil, err
	}
	if status != "active" {
		return nil, fmt.Errorf("cloudflare token is not active (status %q)", status)
	}
	accountID, accountLabel := cfDiscoverAccount(ctx, token)
	if hint := strings.TrimSpace(in.AccountID); hint != "" {
		accountID = hint
	}
	externalID := accountID
	if externalID == "" {
		// No account disclosed and no hint: use the token id as the stable external
		// key so the row/ResolveOrgByExternalID has a non-empty, non-colliding value.
		externalID = tokenID
	}
	return &ExchangeResult{
		Tokens:       map[string]string{cloudflareTokenSecret: token},
		ExternalID:   externalID,
		AccountLabel: accountLabel,
		Scopes:       cloudflareScopes,
	}, nil
}

// cfTokenStatus calls GET /user/tokens/verify and returns the lower-cased status +
// token id. The token rides ONLY the Authorization header — never a query or log.
func cfTokenStatus(ctx context.Context, token string) (status, tokenID string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cfAPIBase()+"/user/tokens/verify", nil)
	if err != nil {
		return "", "", fmt.Errorf("cloudflare verify: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := cfHTTPClient.Do(req)
	if err != nil {
		// Do NOT wrap err: a transport error can echo the request URL but never the
		// header, and we keep the message token-free regardless.
		return "", "", fmt.Errorf("cloudflare verify request failed")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return "", "", fmt.Errorf("cloudflare rejected the token (%d)", resp.StatusCode)
	}
	var vr cfVerifyResult
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&vr); err != nil {
		return "", "", fmt.Errorf("cloudflare verify: malformed response")
	}
	if !vr.Success {
		return "", "", fmt.Errorf("cloudflare verify unsuccessful")
	}
	return strings.ToLower(strings.TrimSpace(vr.Result.Status)), strings.TrimSpace(vr.Result.ID), nil
}

// cfDiscoverAccount best-effort resolves the first account the token can see. A
// least-privilege token may be forbidden here; that is NOT fatal (returns "","").
func cfDiscoverAccount(ctx context.Context, token string) (id, name string) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cfAPIBase()+"/accounts?per_page=1", nil)
	if err != nil {
		return "", ""
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := cfHTTPClient.Do(req)
	if err != nil {
		return "", ""
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", ""
	}
	var ar cfAccountsResult
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&ar); err != nil || !ar.Success || len(ar.Result) == 0 {
		return "", ""
	}
	return strings.TrimSpace(ar.Result[0].ID), strings.TrimSpace(ar.Result[0].Name)
}

const (
	// cloudflareOAuthClientIDEnv / cloudflareOAuthClientSecretEnv name the ENV that
	// carries Hanzo's registered Cloudflare OAuth app creds (KMS-synced, never in
	// code). Until BOTH are present the OAuth leg of /connect answers an honest 503
	// and the apikey path still works.
	cloudflareOAuthClientIDEnv     = "CLOUDFLARE_OAUTH_CLIENT_ID"
	cloudflareOAuthClientSecretEnv = "CLOUDFLARE_OAUTH_CLIENT_SECRET"
)

// cloudflareOAuthScopes is the consent scope set — the OAuth-flow spelling of the
// same DNS/zone/pages access the apikey token path requests.
var cloudflareOAuthScopes = []string{"dns_records:edit", "zone:read", "pages:edit"}

// cfOAuthBase is Cloudflare's OAuth2 origin (…/auth authorize + …/token exchange).
// Overridable via CLOUDFLARE_OAUTH_BASE for tests and CF-compatible endpoints; read
// at call time. The default is Cloudflare's dashboard OAuth2 endpoint.
func cfOAuthBase() string {
	if v := strings.TrimSpace(os.Getenv("CLOUDFLARE_OAUTH_BASE")); v != "" {
		return strings.TrimRight(v, "/")
	}
	return "https://dash.cloudflare.com/oauth2"
}

// cloudflareCreds reads Hanzo's registered Cloudflare OAuth app creds from ENV.
// Empty ClientID => the OAuth leg is not configured (connect answers 503).
func cloudflareCreds() OAuthConfig {
	return OAuthConfig{
		ClientID:     strings.TrimSpace(os.Getenv(cloudflareOAuthClientIDEnv)),
		ClientSecret: strings.TrimSpace(os.Getenv(cloudflareOAuthClientSecretEnv)),
	}
}

// cloudflareAuthorize builds Cloudflare's consent URL (Authorization Code flow,
// confidential client — client_secret, no PKCE, matching the framework's OAuth
// providers; see slack_link.go). state is the framework's signed single-use CSRF
// token; redirectURI is /v1/integrations/cloudflare/callback.
func cloudflareAuthorize(creds OAuthConfig, redirectURI, state string) (string, error) {
	q := url.Values{
		"client_id":     {creds.ClientID},
		"response_type": {"code"},
		"redirect_uri":  {redirectURI},
		"scope":         {strings.Join(cloudflareOAuthScopes, " ")},
		"state":         {state},
	}
	return cfOAuthBase() + "/auth?" + q.Encode(), nil
}

// cfOAuthTokenResponse is Cloudflare's oauth2/token response.
type cfOAuthTokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	Scope       string `json:"scope"`
	Error       string `json:"error"`
	ErrorDesc   string `json:"error_description"`
}

// cloudflareExchange trades the OAuth code for an access token and seals it to the
// SAME KMS coordinate as the apikey path (api_token), so the DNS provider layer is
// auth-method-agnostic. It FAILS CLOSED (transport error, non-2xx, error envelope,
// or empty token → error, nothing stored) and its error NEVER contains the token
// or the authorization code value.
func cloudflareExchange(ctx context.Context, creds OAuthConfig, redirectURI, code string) (*ExchangeResult, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {creds.ClientID},
		"client_secret": {creds.ClientSecret},
		"redirect_uri":  {redirectURI},
		"code":          {code},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfOAuthBase()+"/token", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("cloudflare oauth: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := cfHTTPClient.Do(req)
	if err != nil {
		// token-free: never echo the code or the exchange target's userinfo.
		return nil, fmt.Errorf("cloudflare oauth token request failed")
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("cloudflare oauth token http %d", resp.StatusCode)
	}
	var r cfOAuthTokenResponse
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("cloudflare oauth token: malformed response")
	}
	if r.Error != "" {
		// r.Error is Cloudflare's short error code (e.g. invalid_grant), never the token.
		return nil, fmt.Errorf("cloudflare oauth token: %s", r.Error)
	}
	token := strings.TrimSpace(r.AccessToken)
	if token == "" {
		return nil, fmt.Errorf("cloudflare oauth token: no access_token")
	}
	// Best-effort account discovery with the freshly minted token (a least-priv
	// grant may be unable to list accounts — not fatal).
	accountID, accountLabel := cfDiscoverAccount(ctx, token)
	scopes := cloudflareOAuthScopes
	if granted := strings.Fields(strings.TrimSpace(r.Scope)); len(granted) > 0 {
		scopes = granted
	}
	return &ExchangeResult{
		Tokens:       map[string]string{cloudflareTokenSecret: token},
		ExternalID:   accountID,
		AccountLabel: accountLabel,
		Scopes:       scopes,
	}, nil
}
