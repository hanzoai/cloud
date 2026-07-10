package auth

import (
	"errors"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/auth/password"
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/middleware/oauthmiddleware"
	"github.com/hanzoai/cloud/clients/commerce/models/oauthtoken"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/models/user"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
	"github.com/hanzoai/cloud/clients/commerce/util/json/http"
)

// {
// 	"access_token":"2YotnFZFEjr1zCsicMWpAA",
// 	"token_type":"example",
// 	"expires_in":3600,
// 	"refresh_token":"tGzv3JOkF0XG5Qx2TlKWIA",
// 	"example_parameter":"example_value"
// }

type OAuthRequest struct {
	GrantType    string `json:"grant_type,omitempty"`
	Password     string `json:"password,omitempty"`
	Username     string `json:"username,omitempty"`
	ClientId     string `json:"client_id,omitempty"`
	RefreshToken string `json:"refresh_token,omitempty"`
}

type OAuthResponse struct {
	AccessToken  string `json:"access_token,omitempty"`
	TokenType    string `json:"token_type,omitempty"`
	ExpiresIn    int    `json:"expires_in,omitempty"`
	RefreshToken string `json:"refresh_token,omitempty"`
}

func credentials(c *gin.Context) {
	req := OAuthRequest{}
	grantType := ""

	if c.Request.Header.Get("content-type") == "application/json" {
		if err := json.Decode(c.Request.Body, &req); err != nil {
			log.Warn("Failed to decode request body: %v", err, c)
			http.Fail(c, 400, "Failed to decode request body", nil)
			return
		}

		grantType = req.GrantType
	} else {
		grantType = c.Request.FormValue("grant_type")
	}

	switch grantType {
	case "password":
		passwordCredentials(c, req)
	case "refresh_token":
		refreshCredentials(c, req)
	default:
		http.Fail(c, 400, "Grant Type is not supported", nil)
		return
	}
}

func passwordCredentials(c *gin.Context, req OAuthRequest) {
	var username, pw, orgId string

	if req.GrantType == "" {
		username = c.Request.FormValue("username")
		pw = c.Request.FormValue("password")
		orgId = c.Request.FormValue("client_id")
	} else {
		username = req.Username
		pw = req.Password
		orgId = req.ClientId
		// scope := c.Request.URL.Query().Get("scope")
	}

	if username == "" {
		http.Fail(c, 400, "Username required", nil)
		return
	}

	if pw == "" {
		http.Fail(c, 400, "Password required", nil)
		return
	}

	db := datastore.New(c)
	org := organization.New(db)
	if err := org.GetById(orgId); err != nil {
		http.Fail(c, 400, "Invalid organization", err)
		return
	}

	usr := user.New(db)
	if err := usr.GetByEmail(username); err != nil {
		http.Fail(c, 401, "User does not exist", err)
		return
	}

	// Check user's password
	if !password.HashAndCompare(usr.PasswordHash, pw) {
		http.Fail(c, 401, "Email or password is incorrect", errors.New("email or password is incorrect"))
		return
	}

	// If user is not enabled fail
	if !usr.Enabled {
		http.Fail(c, 401, "Account needs to be enabled", errors.New("account needs to be enabled"))
		return
	}

	tok, ok, err := org.GetReferenceToken(usr)
	if !ok {
		// log.Warn("Reference token may have been revoked: %v", err, c)
		http.Fail(c, 403, "Reference token may have been revoked", err)
		return
	}

	expiresIn := tok.AccessPeriod * 3600
	aTokString, err := tok.IssueAccessToken(usr.Id(), org.SecretKey)
	if err != nil {
		http.Fail(c, 403, "Could not issue access token", err)
		return
	}

	rTokString, err := tok.IssueRefreshToken(usr.Id(), org.SecretKey)
	if err != nil {
		http.Fail(c, 403, "Could not issue refresh token", err)
		return
	}

	resp := OAuthResponse{
		AccessToken:  aTokString,
		RefreshToken: rTokString,
		ExpiresIn:    int(expiresIn),
		TokenType:    "jwt",
	}

	http.Render(c, 200, resp)
}

func refreshCredentials(c *gin.Context, req OAuthRequest) {
	var refreshToken string

	if req.GrantType == "" {
		refreshToken = c.Request.FormValue("refresh_token")
	} else {
		refreshToken = req.RefreshToken
	}

	if refreshToken == "" {
		http.Fail(c, 400, "Refresh token required", nil)
		return
	}

	org, _, claims, ok := oauthmiddleware.DecodeToken(c, refreshToken, func(claims oauthtoken.Claims) error {
		if claims.Type != oauthtoken.Refresh {
			return errors.New("token must be type refresh")
		}
		return nil
	})

	if !ok {
		return
	}

	db := datastore.New(c)
	usr := user.New(db)
	if err := usr.GetById(claims.UserId); err != nil {
		http.Fail(c, 403, "User does not exist", err)
		return
	}

	tok, ok, err := org.GetReferenceToken(usr)
	if !ok {
		http.Fail(c, 403, "Reference token may have been revoked", err)
		return
	}

	if tok.Revoked {
		http.Fail(c, 401, "Issuer has been revoked", nil)
		return
	}

	expiresIn := tok.AccessPeriod * 100 * 3600
	aTokString, err := tok.IssueAccessToken(usr.Id(), org.SecretKey)
	if err != nil {
		http.Fail(c, 403, "Could not issue token", err)
		return
	}

	resp := OAuthResponse{
		AccessToken:  aTokString,
		RefreshToken: refreshToken,
		ExpiresIn:    int(expiresIn),
		TokenType:    "jwt",
	}

	http.Render(c, 200, resp)
}
