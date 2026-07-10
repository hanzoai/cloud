package user

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// OAuth2 errors
var (
	ErrOAuthInvalidGrant  = errors.New("oauth: invalid grant")
	ErrOAuthTokenExpired  = errors.New("oauth: token expired")
	ErrOAuthInvalidState  = errors.New("oauth: invalid state")
	ErrOAuthProviderError = errors.New("oauth: provider error")
	ErrOAuthNotConfigured = errors.New("oauth: provider not configured")
)

// HanzoIDConfig holds configuration for hanzo.id OAuth2
type HanzoIDConfig struct {
	ClientID     string
	ClientSecret string
	RedirectURI  string
	AuthURL      string // Default: https://hanzo.id/v1/iam/oauth/authorize
	TokenURL     string // Default: https://hanzo.id/v1/iam/oauth/access_token
	UserInfoURL  string // Default: https://hanzo.id/v1/iam/userinfo
	Scopes       []string
}

// DefaultHanzoIDConfig returns default hanzo.id configuration
func DefaultHanzoIDConfig() *HanzoIDConfig {
	return &HanzoIDConfig{
		AuthURL:     "https://hanzo.id/v1/iam/oauth/authorize",
		TokenURL:    "https://hanzo.id/v1/iam/oauth/access_token",
		UserInfoURL: "https://hanzo.id/v1/iam/userinfo",
		Scopes:      []string{"openid", "profile", "email"},
	}
}

// OAuthService handles OAuth2 authentication flows
type OAuthService struct {
	config     *HanzoIDConfig
	service    *Service
	httpClient *http.Client
}

// NewOAuthService creates a new OAuth service
func NewOAuthService(config *HanzoIDConfig, service *Service) *OAuthService {
	if config.AuthURL == "" {
		config.AuthURL = "https://hanzo.id/v1/iam/oauth/authorize"
	}
	if config.TokenURL == "" {
		config.TokenURL = "https://hanzo.id/v1/iam/oauth/access_token"
	}
	if config.UserInfoURL == "" {
		config.UserInfoURL = "https://hanzo.id/v1/iam/userinfo"
	}
	if len(config.Scopes) == 0 {
		config.Scopes = []string{"openid", "profile", "email"}
	}

	return &OAuthService{
		config:  config,
		service: service,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// AuthorizationURL generates the OAuth2 authorization URL
func (o *OAuthService) AuthorizationURL(state string) string {
	params := url.Values{}
	params.Set("client_id", o.config.ClientID)
	params.Set("redirect_uri", o.config.RedirectURI)
	params.Set("response_type", "code")
	params.Set("scope", strings.Join(o.config.Scopes, " "))
	params.Set("state", state)

	return o.config.AuthURL + "?" + params.Encode()
}

// GenerateState generates a secure random state parameter
func GenerateState() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.URLEncoding.EncodeToString(b), nil
}

// HanzoIDUserInfo represents the user info response from hanzo.id
type HanzoIDUserInfo struct {
	Sub           string `json:"sub"`
	Email         string `json:"email"`
	EmailVerified bool   `json:"email_verified"`
	Name          string `json:"name"`
	GivenName     string `json:"given_name"`
	FamilyName    string `json:"family_name"`
	Picture       string `json:"picture"`
	Locale        string `json:"locale"`

	// Extended hanzo.id fields
	WalletAddress string   `json:"wallet_address,omitempty"`
	Organizations []string `json:"organizations,omitempty"`
	Permissions   []string `json:"permissions,omitempty"`
}

// TokenResponse represents the OAuth2 token response
type TokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token,omitempty"`
	Scope        string `json:"scope,omitempty"`
	IDToken      string `json:"id_token,omitempty"`
}

// ExchangeCode exchanges an authorization code for tokens
func (o *OAuthService) ExchangeCode(ctx context.Context, code string) (*TokenResponse, error) {
	data := url.Values{}
	data.Set("grant_type", "authorization_code")
	data.Set("code", code)
	data.Set("redirect_uri", o.config.RedirectURI)
	data.Set("client_id", o.config.ClientID)
	data.Set("client_secret", o.config.ClientSecret)

	req, err := http.NewRequestWithContext(ctx, "POST", o.config.TokenURL, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := o.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("%w: %s", ErrOAuthProviderError, string(body))
	}

	var tokenResp TokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return nil, err
	}

	return &tokenResp, nil
}

// RefreshToken refreshes an access token
func (o *OAuthService) RefreshToken(ctx context.Context, refreshToken string) (*TokenResponse, error) {
	data := url.Values{}
	data.Set("grant_type", "refresh_token")
	data.Set("refresh_token", refreshToken)
	data.Set("client_id", o.config.ClientID)
	data.Set("client_secret", o.config.ClientSecret)

	req, err := http.NewRequestWithContext(ctx, "POST", o.config.TokenURL, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := o.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("%w: %s", ErrOAuthProviderError, string(body))
	}

	var tokenResp TokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return nil, err
	}

	return &tokenResp, nil
}

// GetUserInfo fetches user info from hanzo.id
func (o *OAuthService) GetUserInfo(ctx context.Context, accessToken string) (*HanzoIDUserInfo, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", o.config.UserInfoURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := o.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("%w: %s", ErrOAuthProviderError, string(body))
	}

	var userInfo HanzoIDUserInfo
	if err := json.NewDecoder(resp.Body).Decode(&userInfo); err != nil {
		return nil, err
	}

	return &userInfo, nil
}

// AuthenticateWithCode performs the full OAuth2 flow and returns/creates a user
func (o *OAuthService) AuthenticateWithCode(ctx context.Context, code string) (*User, error) {
	// Exchange code for tokens
	tokenResp, err := o.ExchangeCode(ctx, code)
	if err != nil {
		return nil, err
	}

	// Get user info from hanzo.id
	userInfo, err := o.GetUserInfo(ctx, tokenResp.AccessToken)
	if err != nil {
		return nil, err
	}

	// Try to find existing user by hanzo.id
	sysDB, err := o.service.manager.User("_system")
	if err != nil {
		return nil, err
	}

	// Look up user by hanzo.id
	index := &HanzoIDIndex{}
	_, err = sysDB.Query("hanzo_id_index").Filter("HanzoID=", userInfo.Sub).First(ctx, index)

	var user *User
	if err == nil {
		// User exists, load from their database
		user, err = o.service.Get(ctx, index.UserID)
		if err != nil {
			return nil, err
		}
	} else {
		// Try to find by email
		emailIndex := &UserEmailIndex{}
		_, err = sysDB.Query("user_email_index").Filter("Email=", userInfo.Email).First(ctx, emailIndex)

		if err == nil {
			// User exists with this email, link to hanzo.id
			user, err = o.service.Get(ctx, emailIndex.UserID)
			if err != nil {
				return nil, err
			}
		} else {
			// Create new user
			user = NewFromHanzoID(userInfo.Sub, userInfo.Email)
			user.FirstName = userInfo.GivenName
			user.LastName = userInfo.FamilyName

			if err := o.service.Create(ctx, user); err != nil {
				return nil, err
			}
		}

		// Create hanzo.id index
		hanzoIndex := &HanzoIDIndex{
			HanzoID: userInfo.Sub,
			UserID:  user.ID,
		}
		indexKey := sysDB.NewKey("hanzo_id_index", userInfo.Sub, 0, nil)
		if _, err := sysDB.Put(ctx, indexKey, hanzoIndex); err != nil {
			return nil, err
		}
	}

	// Update user with latest info from hanzo.id
	user.HanzoID = userInfo.Sub
	user.HanzoIDVerified = true
	if userInfo.Email != "" && userInfo.EmailVerified {
		user.Email = userInfo.Email
	}
	if userInfo.GivenName != "" {
		user.FirstName = userInfo.GivenName
	}
	if userInfo.FamilyName != "" {
		user.LastName = userInfo.FamilyName
	}
	if userInfo.WalletAddress != "" {
		user.KYC.LuxAddress = userInfo.WalletAddress
	}

	// Store OAuth token
	user.SetOAuthToken(OAuthToken{
		Provider:     OAuthProviderHanzo,
		AccessToken:  tokenResp.AccessToken,
		RefreshToken: tokenResp.RefreshToken,
		TokenType:    tokenResp.TokenType,
		ExpiresAt:    time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second),
		Scope:        tokenResp.Scope,
		ProviderUID:  userInfo.Sub,
	})

	// Add organizations from hanzo.id
	for _, org := range userInfo.Organizations {
		user.AddOrganization(org)
	}

	// Update the user
	if err := o.service.Update(ctx, user); err != nil {
		return nil, err
	}

	return user, nil
}

// HanzoIDIndex maps hanzo.id to user ID
type HanzoIDIndex struct {
	HanzoID string `json:"hanzoId"`
	UserID  string `json:"userId"`
}

// Kind implements db.Entity
func (i *HanzoIDIndex) Kind() string {
	return "hanzo_id_index"
}

// ValidateToken checks if a user's hanzo.id token is still valid
func (o *OAuthService) ValidateToken(ctx context.Context, user *User) error {
	token := user.GetOAuthToken(OAuthProviderHanzo)
	if token == nil {
		return ErrOAuthNotConfigured
	}

	if time.Now().After(token.ExpiresAt) {
		// Token expired, try to refresh
		if token.RefreshToken == "" {
			return ErrOAuthTokenExpired
		}

		newToken, err := o.RefreshToken(ctx, token.RefreshToken)
		if err != nil {
			return err
		}

		user.SetOAuthToken(OAuthToken{
			Provider:     OAuthProviderHanzo,
			AccessToken:  newToken.AccessToken,
			RefreshToken: newToken.RefreshToken,
			TokenType:    newToken.TokenType,
			ExpiresAt:    time.Now().Add(time.Duration(newToken.ExpiresIn) * time.Second),
			Scope:        newToken.Scope,
			ProviderUID:  token.ProviderUID,
		})

		if err := o.service.Update(ctx, user); err != nil {
			return err
		}
	}

	return nil
}

// Logout revokes the user's hanzo.id token
func (o *OAuthService) Logout(ctx context.Context, user *User) error {
	token := user.GetOAuthToken(OAuthProviderHanzo)
	if token == nil {
		return nil // No token to revoke
	}

	// Revoke token at hanzo.id (if revocation endpoint exists)
	// For now, just remove the token from the user
	user.RemoveOAuthToken(OAuthProviderHanzo)

	return o.service.Update(ctx, user)
}
