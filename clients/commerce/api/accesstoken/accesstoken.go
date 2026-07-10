package accesstoken

import (
	"errors"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/auth/password"
	"github.com/hanzoai/cloud/clients/commerce/config"
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/middleware"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/models/user"
	"github.com/hanzoai/cloud/clients/commerce/util/json/http"
	"github.com/hanzoai/cloud/clients/commerce/util/permission"
	"github.com/hanzoai/cloud/clients/commerce/util/session"
)

func getAccessToken(c *gin.Context, id, email, pass string, test bool) {
	db := datastore.New(c)
	u := user.New(db)

	// Try to get user by email
	if err := u.GetByEmail(email); err != nil {
		http.Fail(c, 401, "Invalid email address.", nil)
		return
	}

	// Check password
	if !password.HashAndCompare(u.PasswordHash, pass) {
		http.Fail(c, 401, "Invalid password.", nil)
		return
	}

	// Get organization
	org := organization.New(db)
	if err := org.GetById(id); err != nil {
		http.Fail(c, 500, "Unable to retrieve organization", err)
		return
	}

	// Check if we have permission to create an access token
	if !(org.IsOwner(u) || org.IsAdmin(u)) {
		log.Warn("user (%v, %v) is not owner of (%v, %v)", u.Email, u.Id(), org.Name, org.Id())
		http.Fail(c, 500, "Must be owner or admin to create a new access token.", errors.New("Invalid user"))
		return
	}

	// Get proper token
	var accessToken string

	if test {
		org.RemoveToken("test-secret-key")
		accessToken = org.AddToken("test-secret-key", permission.Admin|permission.Test)
	} else {
		org.RemoveToken("live-secret-key")
		accessToken = org.AddToken("live-secret-key", permission.Admin|permission.Live)
	}

	// Save organization
	org.Put()

	// Save access token in cookie for ease of use during development
	if config.IsDevelopment {
		session.Set(c, "access-token", accessToken)
	}

	// Return access token
	http.Render(c, 200, gin.H{"status": "ok", "token": accessToken})
}

func deleteAccessToken(c *gin.Context) {
	accessToken := session.GetString(c, "access-token")

	// Save access token in cookie for ease of use during development
	if config.IsDevelopment {
		session.Delete(c, "access-token")
	}

	// Get organization for current access token
	org := middleware.GetOrganization(c)

	// Retrieve token
	tok, err := org.GetToken(accessToken)
	if err != nil {
		http.Fail(c, 500, "Invalid token", err)
		return
	}

	// Remove token
	org.RemoveToken(tok.Name)

	if err := org.Put(); err != nil {
		http.Fail(c, 500, "Unable to update organization", err)
		return
	}

	// Return access token
	http.Render(c, 200, gin.H{"status": "ok"})
}
