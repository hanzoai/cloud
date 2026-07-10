// Package auth provides authentication utilities including IAM OAuth2/OIDC integration.
package auth

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt"
)

// Standard OAuth2/OIDC errors
var (
	ErrInvalidToken     = errors.New("iam: invalid token")
	ErrTokenExpired     = errors.New("iam: token expired")
	ErrTokenNotYetValid = errors.New("iam: token not yet valid")
	ErrInvalidIssuer    = errors.New("iam: invalid issuer")
	ErrInvalidAudience  = errors.New("iam: invalid audience")
	ErrMissingPublicKey = errors.New("iam: missing public key")
	ErrTokenExchange    = errors.New("iam: token exchange failed")
	ErrUserInfoFetch    = errors.New("iam: failed to fetch user info")
	ErrOIDCDiscovery    = errors.New("iam: OIDC discovery failed")
	ErrInvalidConfig    = errors.New("iam: invalid configuration")
)

// IAMConfig holds the OAuth2/OIDC configuration for Hanzo IAM (hanzo.id).
type IAMConfig struct {
	// Issuer is the IAM server URL (e.g., "https://id.hanzo.ai")
	Issuer string

	// ClientID is the OAuth2 client identifier
	ClientID string

	// ClientSecret is the OAuth2 client secret
	ClientSecret string

	// AcceptedAudiences is a list of additional client IDs to accept when
	// acting as a resource server. Tokens issued for any of these audiences
	// (or for ClientID) will be accepted.
	AcceptedAudiences []string

	// AcceptedIssuers is a list of additional issuer URLs to accept.
	// Useful when the IAM server is reachable under multiple domains
	// (e.g., "https://hanzo.id" and "https://iam.hanzo.ai").
	AcceptedIssuers []string

	// JwksURI overrides the JWKS endpoint URL.
	// When set, skips OIDC discovery and fetches JWKS directly from this URL.
	// Useful when the IAM discovery doc returns an HTTPS URL but the internal
	// service is only reachable via HTTP.
	JwksURI string

	// RedirectURL is the callback URL for authorization code flow
	RedirectURL string

	// Scopes to request during authorization (defaults to "openid profile email")
	Scopes []string

	// HTTPClient allows custom HTTP client (for timeouts, proxies, etc.)
	HTTPClient *http.Client
}

// DefaultScopes returns the default OIDC scopes
func DefaultScopes() []string {
	return []string{"openid", "profile", "email"}
}

// IAMClient is an OAuth2/OIDC client for Hanzo IAM.
type IAMClient struct {
	config    *IAMConfig
	discovery *OIDCDiscovery
	jwks      *JWKS
	mu        sync.RWMutex
}

// OIDCDiscovery holds the OIDC well-known configuration.
type OIDCDiscovery struct {
	Issuer                 string   `json:"issuer"`
	AuthorizationEndpoint  string   `json:"authorization_endpoint"`
	TokenEndpoint          string   `json:"token_endpoint"`
	UserinfoEndpoint       string   `json:"userinfo_endpoint"`
	JwksURI                string   `json:"jwks_uri"`
	IntrospectionEndpoint  string   `json:"introspection_endpoint"`
	RevocationEndpoint     string   `json:"revocation_endpoint"`
	ScopesSupported        []string `json:"scopes_supported"`
	ResponseTypesSupported []string `json:"response_types_supported"`
	GrantTypesSupported    []string `json:"grant_types_supported"`
}

// JWKS holds the JSON Web Key Set for token validation.
type JWKS struct {
	Keys      []JWK     `json:"keys"`
	FetchedAt time.Time `json:"-"`
}

// JWK represents a JSON Web Key.
type JWK struct {
	Kty string `json:"kty"` // Key type (RSA, EC)
	Kid string `json:"kid"` // Key ID
	Use string `json:"use"` // Key use (sig, enc)
	Alg string `json:"alg"` // Algorithm
	N   string `json:"n"`   // RSA modulus
	E   string `json:"e"`   // RSA exponent
	X   string `json:"x"`   // EC X coordinate
	Y   string `json:"y"`   // EC Y coordinate
	Crv string `json:"crv"` // EC curve
}

// TokenResponse represents the OAuth2 token response.
type TokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token,omitempty"`
	IDToken      string `json:"id_token,omitempty"`
	Scope        string `json:"scope,omitempty"`
}

// TokenError represents an OAuth2 error response.
type TokenError struct {
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description,omitempty"`
}

// IAMUserInfo represents the OIDC userinfo response from Hanzo IAM.
type IAMUserInfo struct {
	Sub           string   `json:"sub"`
	Iss           string   `json:"iss,omitempty"`
	Aud           string   `json:"aud,omitempty"`
	Name          string   `json:"preferred_username,omitempty"`
	DisplayName   string   `json:"name,omitempty"`
	Email         string   `json:"email,omitempty"`
	EmailVerified bool     `json:"email_verified,omitempty"`
	Picture       string   `json:"picture,omitempty"`
	Address       string   `json:"address,omitempty"`
	Phone         string   `json:"phone,omitempty"`
	RealName      string   `json:"real_name,omitempty"`
	IsVerified    bool     `json:"is_verified,omitempty"`
	Groups        []string  `json:"groups,omitempty"`
	Roles         FlexRoles `json:"roles,omitempty"`
	Permissions   []string  `json:"permissions,omitempty"`
}

// FlexAudience handles JWT "aud" which can be either a string or array of strings.
type FlexAudience string

func (a *FlexAudience) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		*a = FlexAudience(s)
		return nil
	}
	var arr []string
	if err := json.Unmarshal(data, &arr); err != nil {
		return err
	}
	if len(arr) > 0 {
		*a = FlexAudience(arr[0])
	}
	return nil
}

// FlexRoles handles JWT "roles" which can be either an array of strings
// (e.g. ["admin"]) or an array of role objects (e.g. [{"name":"admin",...}]).
// When role objects are encountered, only the "name" field is kept.
type FlexRoles []string

func (r *FlexRoles) UnmarshalJSON(data []byte) error {
	// Try []string first
	var arr []string
	if err := json.Unmarshal(data, &arr); err == nil {
		*r = arr
		return nil
	}
	// Try []object with "name" field
	var objs []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(data, &objs); err != nil {
		// If neither works, ignore roles silently
		*r = nil
		return nil
	}
	names := make([]string, 0, len(objs))
	for _, o := range objs {
		if o.Name != "" {
			names = append(names, o.Name)
		}
	}
	*r = names
	return nil
}

// IAMClaims represents the JWT claims from Hanzo IAM tokens.
type IAMClaims struct {
	jwt.StandardClaims

	// Override Audience to handle both string and array formats from IAM.
	Audience FlexAudience `json:"aud,omitempty"`

	// User identification
	Owner       string `json:"owner,omitempty"`
	Name        string `json:"name,omitempty"`
	DisplayName string `json:"displayName,omitempty"`
	Avatar      string `json:"avatar,omitempty"`
	Email       string `json:"email,omitempty"`
	Phone       string `json:"phone,omitempty"`

	// Token metadata
	TokenType string `json:"tokenType,omitempty"`
	Nonce     string `json:"nonce,omitempty"`
	Scope     string `json:"scope,omitempty"`
	Azp       string `json:"azp,omitempty"` // Authorized party

	// Authorization
	IsAdmin bool `json:"isAdmin,omitempty"`
	// IsGlobalAdmin is the platform-wide superadmin flag from IAM. Unlike
	// IsAdmin (an ORG-level role — an org owner has IsAdmin=true within
	// their own org), this is true ONLY for global administrators. It is the
	// one claim safe to gate cross-org actions on.
	IsGlobalAdmin bool      `json:"isGlobalAdmin,omitempty"`
	Groups        []string  `json:"groups,omitempty"`
	Roles         FlexRoles `json:"roles,omitempty"`
	Permissions   []string  `json:"permissions,omitempty"`

	// User properties (arbitrary key-value pairs from IAM).
	// The "tier" property is used for tiered billing (free/starter/pro/enterprise).
	Properties map[string]string `json:"properties,omitempty"`
}

// GlobalAdmin reports whether these claims belong to a Hanzo PLATFORM (global)
// administrator — the ONLY predicate safe to gate cross-org / superadmin
// actions on. Two independent signals, either suffices:
//   - the explicit isGlobalAdmin claim (gateway X-User-IsGlobalAdmin); or
//   - membership in the global-admin org (owner == "admin"), the slug Hanzo
//     IAM seeds global admins into.
//
// Plain IsAdmin is deliberately excluded: it is an ORG-level role (an org owner
// carries IsAdmin=true within their own org). Gating cross-org actions on
// IsAdmin — or on an org-mintable role name like "superadmin" — is the
// privilege escalation this predicate exists to prevent. nil-safe.
func (c *IAMClaims) GlobalAdmin() bool {
	if c == nil {
		return false
	}
	return c.IsGlobalAdmin || strings.EqualFold(strings.TrimSpace(c.Owner), "admin")
}

// Tier returns the user's billing tier from the "tier" property.
// Returns an empty string if not set; callers should default to "free".
func (c *IAMClaims) Tier() string {
	if c.Properties == nil {
		return ""
	}
	return c.Properties["tier"]
}

// Valid implements jwt.Claims interface.
func (c *IAMClaims) Valid() error {
	now := time.Now().Unix()

	// Check expiration
	if c.ExpiresAt > 0 && now > c.ExpiresAt {
		return ErrTokenExpired
	}

	// Check not before
	if c.NotBefore > 0 && now < c.NotBefore {
		return ErrTokenNotYetValid
	}

	return nil
}

// NewIAMClient creates a new IAM client with the given configuration.
func NewIAMClient(config *IAMConfig) (*IAMClient, error) {
	if config == nil {
		return nil, ErrInvalidConfig
	}

	if config.Issuer == "" {
		return nil, fmt.Errorf("%w: issuer is required", ErrInvalidConfig)
	}

	if config.ClientID == "" {
		return nil, fmt.Errorf("%w: client_id is required", ErrInvalidConfig)
	}

	// Set defaults
	if len(config.Scopes) == 0 {
		config.Scopes = DefaultScopes()
	}

	if config.HTTPClient == nil {
		config.HTTPClient = &http.Client{
			Timeout: 30 * time.Second,
		}
	}

	// Normalize issuer URL (remove trailing slash)
	config.Issuer = strings.TrimRight(config.Issuer, "/")

	return &IAMClient{
		config: config,
	}, nil
}

// GetAuthorizationURL generates the OAuth2 authorization URL for login.
func (c *IAMClient) GetAuthorizationURL(state string, nonce string) (string, error) {
	discovery, err := c.getDiscovery(context.Background())
	if err != nil {
		// Fallback to constructed URL if discovery fails
		authURL := c.config.Issuer + "/v1/iam/oauth/authorize"
		return c.buildAuthURL(authURL, state, nonce), nil
	}

	return c.buildAuthURL(discovery.AuthorizationEndpoint, state, nonce), nil
}

// buildAuthURL constructs the authorization URL with parameters.
func (c *IAMClient) buildAuthURL(authEndpoint, state, nonce string) string {
	params := url.Values{
		"client_id":     {c.config.ClientID},
		"redirect_uri":  {c.config.RedirectURL},
		"response_type": {"code"},
		"scope":         {strings.Join(c.config.Scopes, " ")},
	}

	if state != "" {
		params.Set("state", state)
	}

	if nonce != "" {
		params.Set("nonce", nonce)
	}

	return authEndpoint + "?" + params.Encode()
}

// ExchangeCode exchanges an authorization code for tokens.
func (c *IAMClient) ExchangeCode(ctx context.Context, code string) (*TokenResponse, error) {
	tokenURL := c.config.Issuer + "/v1/iam/oauth/access_token"

	// Try to use discovered endpoint
	discovery, err := c.getDiscovery(ctx)
	if err == nil && discovery.TokenEndpoint != "" {
		tokenURL = discovery.TokenEndpoint
	}

	data := url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {c.config.ClientID},
		"client_secret": {c.config.ClientSecret},
		"code":          {code},
		"redirect_uri":  {c.config.RedirectURL},
	}

	req, err := http.NewRequestWithContext(ctx, "POST", tokenURL, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrTokenExchange, err)
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := c.config.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrTokenExchange, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("%w: failed to read response: %v", ErrTokenExchange, err)
	}

	if resp.StatusCode != http.StatusOK {
		var tokenErr TokenError
		if json.Unmarshal(body, &tokenErr) == nil && tokenErr.Error != "" {
			return nil, fmt.Errorf("%w: %s - %s", ErrTokenExchange, tokenErr.Error, tokenErr.ErrorDescription)
		}
		return nil, fmt.Errorf("%w: status %d", ErrTokenExchange, resp.StatusCode)
	}

	var tokenResp TokenResponse
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return nil, fmt.Errorf("%w: invalid response format: %v", ErrTokenExchange, err)
	}

	// Handle IAM's alternative response format
	if tokenResp.ExpiresIn <= 0 {
		// IAM returns error in access_token field when expires_in is 0 or negative
		return nil, fmt.Errorf("%w: %s", ErrTokenExchange, tokenResp.AccessToken)
	}

	return &tokenResp, nil
}

// RefreshToken exchanges a refresh token for new tokens.
func (c *IAMClient) RefreshToken(ctx context.Context, refreshToken string) (*TokenResponse, error) {
	tokenURL := c.config.Issuer + "/v1/iam/oauth/access_token"

	data := url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {c.config.ClientID},
		"client_secret": {c.config.ClientSecret},
		"refresh_token": {refreshToken},
	}

	req, err := http.NewRequestWithContext(ctx, "POST", tokenURL, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrTokenExchange, err)
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := c.config.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrTokenExchange, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("%w: failed to read response: %v", ErrTokenExchange, err)
	}

	if resp.StatusCode != http.StatusOK {
		var tokenErr TokenError
		if json.Unmarshal(body, &tokenErr) == nil && tokenErr.Error != "" {
			return nil, fmt.Errorf("%w: %s - %s", ErrTokenExchange, tokenErr.Error, tokenErr.ErrorDescription)
		}
		return nil, fmt.Errorf("%w: status %d", ErrTokenExchange, resp.StatusCode)
	}

	var tokenResp TokenResponse
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return nil, fmt.Errorf("%w: invalid response format: %v", ErrTokenExchange, err)
	}

	return &tokenResp, nil
}

// ValidateToken validates a JWT access token and returns the claims.
func (c *IAMClient) ValidateToken(ctx context.Context, tokenString string) (*IAMClaims, error) {
	// Parse without verification first to extract claims and key ID
	token, _, err := new(jwt.Parser).ParseUnverified(tokenString, &IAMClaims{})
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidToken, err)
	}

	claims, ok := token.Claims.(*IAMClaims)
	if !ok {
		return nil, ErrInvalidToken
	}

	// Validate issuer — accept primary Issuer and any AcceptedIssuers.
	if claims.Issuer != "" {
		issuerOK := false
		allIssuers := append([]string{c.config.Issuer}, c.config.AcceptedIssuers...)
		actualHost := strings.TrimPrefix(strings.TrimPrefix(claims.Issuer, "https://"), "http://")
		for _, iss := range allIssuers {
			expectedHost := strings.TrimPrefix(strings.TrimPrefix(iss, "https://"), "http://")
			if expectedHost == actualHost {
				issuerOK = true
				break
			}
		}
		if !issuerOK {
			return nil, fmt.Errorf("%w: expected %s, got %s", ErrInvalidIssuer, c.config.Issuer, claims.Issuer)
		}
	}

	// Validate audience (client ID).
	// Accept tokens issued for ClientID or any of the AcceptedAudiences.
	// This allows Commerce to act as a resource server accepting tokens
	// from multiple IAM client apps (e.g., billing portal, playground).
	aud := string(claims.Audience)
	validAudience := false
	if aud == c.config.ClientID || strings.HasPrefix(aud, c.config.ClientID) {
		validAudience = true
	}
	if !validAudience && claims.Azp == c.config.ClientID {
		validAudience = true
	}
	if !validAudience {
		for _, accepted := range c.config.AcceptedAudiences {
			if aud == accepted || claims.Azp == accepted {
				validAudience = true
				break
			}
		}
	}
	if !validAudience && aud != "" {
		return nil, fmt.Errorf("%w: token not issued for this client", ErrInvalidAudience)
	}

	// Verify the token signature against the IAM JWKS. REQUIRED — fail
	// closed. commerce-api is a directly-exposed trust boundary
	// (commerce-api.hanzo.ai is NOT behind hanzoai/gateway), so a token
	// whose RS256 signature we cannot verify is rejected. The prior code
	// returned UNVERIFIED claims whenever the JWKS was unreachable or the
	// kid was unknown — a forged token slipped through on any JWKS hiccup.
	jwks, err := c.getJWKS(ctx)
	if err != nil || jwks == nil {
		return nil, fmt.Errorf("%w: JWKS unavailable: %v", ErrInvalidToken, err)
	}
	kid, _ := token.Header["kid"].(string)
	key := c.findKey(jwks, kid)
	if key == nil {
		return nil, fmt.Errorf("%w: no signing key for kid %q", ErrInvalidToken, kid)
	}
	verifiedToken, err := jwt.ParseWithClaims(tokenString, &IAMClaims{}, func(t *jwt.Token) (interface{}, error) {
		// Pin RSA — never let a token pick HS256/none and turn the public
		// key into a shared HMAC secret (alg-confusion).
		if _, ok := t.Method.(*jwt.SigningMethodRSA); !ok {
			return nil, fmt.Errorf("unexpected signing method %q", t.Header["alg"])
		}
		return key, nil
	})
	if err != nil {
		return nil, fmt.Errorf("%w: signature verification failed: %v", ErrInvalidToken, err)
	}
	claims, ok = verifiedToken.Claims.(*IAMClaims)
	if !ok || !verifiedToken.Valid {
		return nil, ErrInvalidToken
	}

	// Validate time claims
	if err := claims.Valid(); err != nil {
		return nil, err
	}

	return claims, nil
}

// GetUserInfo fetches user information using an access token.
func (c *IAMClient) GetUserInfo(ctx context.Context, accessToken string) (*IAMUserInfo, error) {
	userinfoURL := c.config.Issuer + "/v1/iam/userinfo"

	// Try to use discovered endpoint
	discovery, err := c.getDiscovery(ctx)
	if err == nil && discovery.UserinfoEndpoint != "" {
		userinfoURL = discovery.UserinfoEndpoint
	}

	req, err := http.NewRequestWithContext(ctx, "GET", userinfoURL, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUserInfoFetch, err)
	}

	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")

	resp, err := c.config.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUserInfoFetch, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("%w: failed to read response: %v", ErrUserInfoFetch, err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: status %d - %s", ErrUserInfoFetch, resp.StatusCode, string(body))
	}

	var userInfo IAMUserInfo
	if err := json.Unmarshal(body, &userInfo); err != nil {
		return nil, fmt.Errorf("%w: invalid response format: %v", ErrUserInfoFetch, err)
	}

	return &userInfo, nil
}

// IntrospectToken introspects a token to check its validity.
func (c *IAMClient) IntrospectToken(ctx context.Context, token string, tokenTypeHint string) (*IntrospectionResponse, error) {
	introspectURL := c.config.Issuer + "/v1/iam/oauth/introspect"

	// Try to use discovered endpoint
	discovery, err := c.getDiscovery(ctx)
	if err == nil && discovery.IntrospectionEndpoint != "" {
		introspectURL = discovery.IntrospectionEndpoint
	}

	data := url.Values{
		"token": {token},
	}
	if tokenTypeHint != "" {
		data.Set("token_type_hint", tokenTypeHint)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", introspectURL, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.SetBasicAuth(c.config.ClientID, c.config.ClientSecret)

	resp, err := c.config.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var introspectionResp IntrospectionResponse
	if err := json.Unmarshal(body, &introspectionResp); err != nil {
		return nil, err
	}

	return &introspectionResp, nil
}

// IntrospectionResponse represents the token introspection response.
type IntrospectionResponse struct {
	Active    bool   `json:"active"`
	Scope     string `json:"scope,omitempty"`
	ClientID  string `json:"client_id,omitempty"`
	Username  string `json:"username,omitempty"`
	TokenType string `json:"token_type,omitempty"`
	Exp       int64  `json:"exp,omitempty"`
	Iat       int64  `json:"iat,omitempty"`
	Nbf       int64  `json:"nbf,omitempty"`
	Sub       string `json:"sub,omitempty"`
	Aud       string `json:"aud,omitempty"`
	Iss       string `json:"iss,omitempty"`
	Jti       string `json:"jti,omitempty"`
}

// RevokeToken revokes an access or refresh token.
func (c *IAMClient) RevokeToken(ctx context.Context, token string, tokenTypeHint string) error {
	revokeURL := c.config.Issuer + "/v1/iam/login/oauth/revoke"

	// Try to use discovered endpoint
	discovery, err := c.getDiscovery(ctx)
	if err == nil && discovery.RevocationEndpoint != "" {
		revokeURL = discovery.RevocationEndpoint
	}

	data := url.Values{
		"token":         {token},
		"client_id":     {c.config.ClientID},
		"client_secret": {c.config.ClientSecret},
	}
	if tokenTypeHint != "" {
		data.Set("token_type_hint", tokenTypeHint)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", revokeURL, strings.NewReader(data.Encode()))
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.config.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	// RFC 7009: 200 OK is success, even for invalid tokens
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("revocation failed: status %d - %s", resp.StatusCode, string(body))
	}

	return nil
}

// getDiscovery fetches the OIDC discovery document.
func (c *IAMClient) getDiscovery(ctx context.Context) (*OIDCDiscovery, error) {
	c.mu.RLock()
	if c.discovery != nil {
		c.mu.RUnlock()
		return c.discovery, nil
	}
	c.mu.RUnlock()

	c.mu.Lock()
	defer c.mu.Unlock()

	// Double-check after acquiring write lock
	if c.discovery != nil {
		return c.discovery, nil
	}

	discoveryURL := c.config.Issuer + "/.well-known/openid-configuration"

	req, err := http.NewRequestWithContext(ctx, "GET", discoveryURL, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrOIDCDiscovery, err)
	}

	resp, err := c.config.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrOIDCDiscovery, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: status %d", ErrOIDCDiscovery, resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrOIDCDiscovery, err)
	}

	var discovery OIDCDiscovery
	if err := json.Unmarshal(body, &discovery); err != nil {
		return nil, fmt.Errorf("%w: invalid document: %v", ErrOIDCDiscovery, err)
	}

	c.discovery = &discovery
	return c.discovery, nil
}

// getJWKS fetches the JSON Web Key Set for token validation.
func (c *IAMClient) getJWKS(ctx context.Context) (*JWKS, error) {
	c.mu.RLock()
	// Cache JWKS for 1 hour
	if c.jwks != nil && time.Since(c.jwks.FetchedAt) < time.Hour {
		c.mu.RUnlock()
		return c.jwks, nil
	}
	c.mu.RUnlock()

	// Resolve JWKS URI. Priority:
	// 1. Explicit JwksURI config override (avoids discovery round-trip and
	//    HTTPS mismatch when the IAM service is behind an internal HTTP-only endpoint).
	// 2. OIDC discovery document's jwks_uri field.
	// 3. Fallback: Issuer + "/.well-known/jwks".
	jwksURI := c.config.JwksURI
	if jwksURI == "" {
		jwksURI = c.config.Issuer + "/.well-known/jwks"
		if discovery, err := c.getDiscovery(ctx); err == nil && discovery.JwksURI != "" {
			jwksURI = discovery.JwksURI
		}
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	// Double-check after acquiring write lock
	if c.jwks != nil && time.Since(c.jwks.FetchedAt) < time.Hour {
		return c.jwks, nil
	}

	req, err := http.NewRequestWithContext(ctx, "GET", jwksURI, nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.config.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("JWKS fetch failed: status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var jwks JWKS
	if err := json.Unmarshal(body, &jwks); err != nil {
		return nil, err
	}

	jwks.FetchedAt = time.Now()
	c.jwks = &jwks
	return c.jwks, nil
}

// findKey finds a public key in the JWKS by key ID.
func (c *IAMClient) findKey(jwks *JWKS, kid string) *rsa.PublicKey {
	for _, key := range jwks.Keys {
		if kid != "" && key.Kid != kid {
			continue
		}
		if key.Kty != "RSA" {
			continue
		}

		// Parse RSA public key from JWK
		pubKey, err := parseRSAPublicKey(key)
		if err != nil {
			continue
		}
		return pubKey
	}
	return nil
}

// parseRSAPublicKey parses an RSA public key from a JWK.
func parseRSAPublicKey(jwk JWK) (*rsa.PublicKey, error) {
	// Decode base64url-encoded modulus
	nBytes, err := base64.RawURLEncoding.DecodeString(jwk.N)
	if err != nil {
		// Try with padding
		nBytes, err = base64.URLEncoding.DecodeString(padBase64(jwk.N))
		if err != nil {
			return nil, fmt.Errorf("failed to decode modulus: %v", err)
		}
	}

	// Decode base64url-encoded exponent
	eBytes, err := base64.RawURLEncoding.DecodeString(jwk.E)
	if err != nil {
		// Try with padding
		eBytes, err = base64.URLEncoding.DecodeString(padBase64(jwk.E))
		if err != nil {
			return nil, fmt.Errorf("failed to decode exponent: %v", err)
		}
	}

	// Convert exponent bytes to int
	var e int
	for _, b := range eBytes {
		e = e<<8 + int(b)
	}

	// Convert modulus bytes to big.Int
	n := new(big.Int).SetBytes(nBytes)

	return &rsa.PublicKey{
		N: n,
		E: e,
	}, nil
}

// padBase64 adds padding to a base64 string if needed.
func padBase64(s string) string {
	switch len(s) % 4 {
	case 2:
		return s + "=="
	case 3:
		return s + "="
	}
	return s
}

// Convenience functions for common operations

// ValidateToken is a package-level convenience function.
func ValidateToken(ctx context.Context, config *IAMConfig, token string) (*IAMClaims, error) {
	client, err := NewIAMClient(config)
	if err != nil {
		return nil, err
	}
	return client.ValidateToken(ctx, token)
}

// GetUserInfo is a package-level convenience function.
func GetUserInfoFromToken(ctx context.Context, config *IAMConfig, accessToken string) (*IAMUserInfo, error) {
	client, err := NewIAMClient(config)
	if err != nil {
		return nil, err
	}
	return client.GetUserInfo(ctx, accessToken)
}

// GetAuthorizationURL is a package-level convenience function.
func GetAuthorizationURL(config *IAMConfig, state, nonce string) (string, error) {
	client, err := NewIAMClient(config)
	if err != nil {
		return "", err
	}
	return client.GetAuthorizationURL(state, nonce)
}

// ExchangeCode is a package-level convenience function.
func ExchangeCode(ctx context.Context, config *IAMConfig, code string) (*TokenResponse, error) {
	client, err := NewIAMClient(config)
	if err != nil {
		return nil, err
	}
	return client.ExchangeCode(ctx, code)
}
