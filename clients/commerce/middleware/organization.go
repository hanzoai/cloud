package middleware

import (
	"context"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/config"
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/types/accesstoken"
	"github.com/hanzoai/cloud/clients/commerce/util/session"
)

func AcquireOrganization(moduleName string) gin.HandlerFunc {
	return func(c *gin.Context) {
		u := GetCurrentUser(c)

		// How did you get this far without an organization, bruh?
		if len(u.Organizations) < 1 {
			panic("THE WORLD MAKES NO SENSE.")
		}

		log.Debug("Found user")

		// Try and re-use last organization
		orgId := session.GetString(c, "active-organization")

		// Or default to their first organization
		if orgId == "" {
			orgId = u.Organizations[0]
		}

		// Fetch organization
		db := datastore.New(c)
		org := organization.New(db)
		err := org.GetById(orgId)
		if err != nil {
			log.Warn("Unable to acquire organization.", c)
			session.Clear(c)
			c.Redirect(302, config.UrlFor(moduleName, "/login"))
			c.AbortWithStatus(302)
		} else {
			log.Debug("Organization acquired")
			c.Set("user", u)
			c.Set("organization", org)
			c.Set("active-organization", org.Id())

			session.Set(c, "active-organization", org.Id())

			// Set for our readme integration
			c.Writer.Header().Set("x-readme-id", org.Id())
			c.Writer.Header().Set("x-readme-label", org.Name)
		}
	}
}

// Namespace applies the organization's namespace to the request context.
func Namespace() gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx := GetContext(c)
		org := GetOrganization(c)
		ctx = org.Namespaced(ctx)
		c.Set("context", ctx)
	}
}

func GetOrganization(c *gin.Context) *organization.Organization {
	return c.MustGet("organization").(*organization.Organization)
}

// GetOrganizationOK returns the request organization without panicking when
// it is absent. Use this on handlers mounted outside the auth-token group
// (e.g. signature-verified webhook ingress) where no session has set an
// organization; GetOrganization would MustGet-panic there.
func GetOrganizationOK(c *gin.Context) (*organization.Organization, bool) {
	v, ok := c.Get("organization")
	if !ok {
		return nil, false
	}
	org, ok := v.(*organization.Organization)
	return org, ok
}

func GetToken(c *gin.Context) *accesstoken.AccessToken {
	return c.MustGet("token").(*accesstoken.AccessToken)
}

func GetNamespace(c *gin.Context) context.Context {
	ctx := GetContext(c)
	return GetOrganization(c).Namespaced(ctx)
}
