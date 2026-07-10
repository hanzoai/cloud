package oauthmiddleware

import (
	"encoding/base64"
	"errors"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/config"
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/middleware"
	"github.com/hanzoai/cloud/clients/commerce/middleware/iammiddleware"
	"github.com/hanzoai/cloud/clients/commerce/models/app"
	"github.com/hanzoai/cloud/clients/commerce/models/oauthtoken"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/models/user"
	"github.com/hanzoai/cloud/clients/commerce/util/bit"
	"github.com/hanzoai/cloud/clients/commerce/util/json/http"
	"github.com/hanzoai/cloud/clients/commerce/util/jwt"
	"github.com/hanzoai/cloud/clients/commerce/util/permission"
	"github.com/hanzoai/cloud/clients/commerce/util/session"
)

func splitAuthorization(fieldValue string) (string, string) {
	parts := strings.Split(fieldValue, " ")
	if len(parts) == 1 {
		return "", parts[0]
	}
	return parts[0], parts[1]
}

func accessTokenFromHeader(fieldValue string) string {
	method, accessToken := splitAuthorization(fieldValue)
	if method == "Basic" {
		bytes, _ := base64.StdEncoding.DecodeString(accessToken)
		return strings.Split(string(bytes), ":")[0]
	}
	return accessToken
}

func ParseToken(c *gin.Context) {
	query := c.Request.URL.Query()

	// Check for `key` query param by default
	accessToken := query.Get("key")

	// Fallback to `token` query param
	if accessToken == "" {
		accessToken = query.Get("token")
	}

	// Try to grab key from Authorization header (basic auth)
	if accessToken == "" {
		accessToken = accessTokenFromHeader(c.Request.Header.Get("Authorization"))
	}

	// If it's still not set and dev server is running, grab from session
	if accessToken == "" && config.IsDevelopment {
		value, _ := session.Get(c, "access-token")
		if tokstr, ok := value.(string); ok {
			accessToken = tokstr
		}
	}

	c.Set("access-token", accessToken)
}

// Permissions required to access route
func TokenPermits(masks ...bit.Mask) gin.HandlerFunc {
	// Any permissions acceptable by default (i.e., only valid token required)
	permissions := permission.All

	// Any arguments passed will be used as new permissions
	if len(masks) > 0 {
		permissions = permission.None
		for _, mask := range masks {
			permissions |= mask
		}
	}

	return func(c *gin.Context) {
		// Verify permissions
		if !GetPermissions(c).Has(permissions) {
			http.Fail(c, 403, "Token doesn't support this scope", errors.New("Token doesn't support this scope"))
		}
	}
}

func DecodeToken(c *gin.Context, tokString string, validationFn func(oauthtoken.Claims) error) (*organization.Organization, *app.App, *oauthtoken.Claims, bool) {
	// Peek at claims to get the org so we can use secret to verify the key
	claims := oauthtoken.Claims{}
	if err := jwt.Peek(tokString, &claims); err != nil {
		http.Fail(c, 403, "Unable to decode oauthtoken.", err)
		return nil, nil, nil, false
	}

	if validationFn != nil {
		if err := validationFn(claims); err != nil {
			http.Fail(c, 401, "Validation failed", err)
			return nil, nil, nil, false
		}
	}

	ctx := middleware.GetContext(c)
	db := datastore.New(ctx)
	org := organization.New(db)
	if err := org.GetById(claims.OrganizationName); err != nil {
		http.Fail(c, 401, "Unable to retrieve organization associated with access token: "+err.Error(), err)
		return org, nil, nil, false
	}

	// Access and Refresh Tokens falls out before the app stuff because they operate in top level namespace
	if claims.Type == oauthtoken.Refresh || claims.Type == oauthtoken.Access {
		// Verify token signature
		if err := jwt.Decode(tokString, org.SecretKey, oauthtoken.Algorithm, &claims); err != nil {
			http.Fail(c, 403, "Unable to verify oauthtoken.", err)
			return org, nil, nil, false
		}

		return org, nil, &claims, true
	}

	// Customer Tokens and API Keys need to get the App
	nsDb := datastore.New(org.Namespaced(c))
	ap := app.New(nsDb)

	if err := ap.GetById(claims.AppId); err != nil {
		http.Fail(c, 500, "App does not exist", err)
		return org, nil, nil, false
	}

	// Verify token signature
	if err := jwt.Decode(tokString, ap.SecretKey, oauthtoken.Algorithm, &claims); err != nil {
		http.Fail(c, 403, "Unable to verify oauthtoken.", err)
		return org, ap, nil, false
	}

	return org, ap, &claims, true
}

func IsAccessIssuerRevoked(c *gin.Context, claims *oauthtoken.Claims) bool {
	// No issuer
	if claims.Type != oauthtoken.Access {
		return false
	}

	db := datastore.New(c)

	tok := oauthtoken.New(db)
	if err := tok.GetById(claims.Issuer); err != nil {
		http.Fail(c, 401, "Access Denied", err)
		return true
	}

	if tok.Revoked {
		http.Fail(c, 401, "Issuer has been revoked", nil)
		return true
	}
	return false
}

func IsCustomerIssuerRevoked(c *gin.Context, org *organization.Organization, claims *oauthtoken.Claims) bool {
	// No issuer
	if claims.Type != oauthtoken.Customer {
		return false
	}

	db := datastore.New(org.Namespaced(c))

	tok := oauthtoken.New(db)
	if err := tok.GetById(claims.Issuer); err != nil {
		http.Fail(c, 401, "Access Denied", err)
		return true
	}

	if tok.Revoked {
		http.Fail(c, 401, "Issuer has been revoked", nil)
		return true
	}
	return false
}

// Parses token, default permissions check
func TokenRequired(masks ...bit.Mask) gin.HandlerFunc {
	// Any permissions acceptable by default (i.e., only valid token required)
	permissions := permission.All

	// Any arguments passed will be used as new permissions
	if len(masks) > 0 {
		permissions = permission.None
		for _, mask := range masks {
			permissions |= mask
		}
	}

	return func(c *gin.Context) {
		// IAM tokens represent authenticated Hanzo users (hanzo.id). The IAM
		// permission model differs from legacy bit-mask org keys, but a masked
		// gate must STILL enforce its masks against the gateway-minted perms —
		// mirroring middleware.TokenRequired (v1.46.5). A bare c.Next() here would
		// make TokenRequired(Admin, …) a no-op for any IAM-authenticated principal
		// (the checkout money-surface bypass class). No masks = any authenticated
		// principal. (This handler is currently unwired; converged so it can never
		// reintroduce the bypass if mounted.)
		if iammiddleware.IsIAMAuthenticated(c) {
			if len(masks) == 0 || hasScope(c, permissions) {
				c.Next()
				return
			}
			http.Fail(c, 403, "Token doesn't support this scope",
				errors.New("IAM principal lacks required permission scope"))
			return
		}

		// Parse token
		ParseToken(c)

		accessToken := GetAccessToken(c)

		// Bail if we still don't have an access token
		if accessToken == "" {
			http.Fail(c, 401, "No access token provided.", nil)
			return
		}

		org, ap, claims, ok := DecodeToken(c, accessToken, nil)
		if !ok {
			return
		}

		// Set Stuff
		switch claims.Type {
		case oauthtoken.Access, oauthtoken.Customer, oauthtoken.Api, oauthtoken.Refresh:
			c.Set("claims", claims)
		default:
			http.Fail(c, 403, "Unknown token type", nil)
			return
		}

		switch claims.Type {
		case oauthtoken.Customer:
			// Customer Token Issuer Revokation Check
			if IsCustomerIssuerRevoked(c, org, claims) {
				return
			}
		case oauthtoken.Access:
			// Access Token Issuer Revokation Check
			if IsAccessIssuerRevoked(c, claims) {
				return
			}
		}

		// Verify permissions
		if !claims.HasPermission(permissions) {
			http.Fail(c, 403, "Token does not support this scope", nil)
			return
		}

		// Whether or not we can make live calls
		org.Live = claims.HasPermission(permission.Live)

		// Save permissions in context
		c.Set("permissions", claims.Permissions)
		// Save organization in context
		c.Set("organization", org)
		// Save app in context
		if ap != nil {
			c.Set("app", ap)
		}
	}
}

func GetClaims(c *gin.Context) *oauthtoken.Claims {
	return c.MustGet("claims").(*oauthtoken.Claims)
}

func GetAccessToken(c *gin.Context) string {
	tok, ok := c.Get("access-token")
	if !ok {
		return ""
	}

	return tok.(string)
}

func GetPermissions(c *gin.Context) bit.Field {
	return c.MustGet("permissions").(bit.Field)
}

// hasScope reports whether the request's resolved permissions include the
// required mask, read defensively (no MustGet) so a gate mounted without a
// permission-setting middleware fails CLOSED. Mirrors middleware.hasScope so
// both auth stacks enforce IAM scope identically.
func hasScope(c *gin.Context, need bit.Mask) bool {
	v, ok := c.Get("permissions")
	if !ok {
		return false
	}
	f, ok := v.(bit.Field)
	return ok && f.Has(need)
}

func GetStore(c *gin.Context) *app.App {
	return c.MustGet("app").(*app.App)
}

// Site Tokens require no user id and a organization name and app name with Claims
func ApiKeyOrAccessTokenOnly(c *gin.Context) {
	claims := GetClaims(c)
	if !oauthtoken.IsApi(*claims) && !oauthtoken.IsAccess(*claims) {
		http.Fail(c, 401, "Access Denied", nil)
		return
	}
}

func AccessTokenOnly(c *gin.Context) {
	claims := GetClaims(c)
	if !oauthtoken.IsAccess(*claims) {
		http.Fail(c, 401, "Access Denied", nil)
		return
	}
}

// Site Tokens require no user id and a organization name and app name with Claims
func ApiKeyOnly(c *gin.Context) {
	claims := GetClaims(c)
	if !oauthtoken.IsApi(*claims) {
		http.Fail(c, 401, "Access Denied", nil)
		return
	}
}

// Customer Tokens require a user id and a organization name and app name with AccessClaims
func CustomerTokenOnly(c *gin.Context) {
	claims := GetClaims(c)
	if !oauthtoken.IsCustomer(*claims) {
		http.Fail(c, 401, "Access Denied", nil)
		return
	}

	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))
	u := user.New(db)

	if err := u.GetById(claims.UserId); err != nil {
		http.Fail(c, 401, "Access Denied", err)
		return
	}

	c.Set("user", u)
}

func GetUser(c *gin.Context) *user.User {
	return c.MustGet("user").(*user.User)
}
