package analytics

import (
	"errors"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/middleware"
	"github.com/hanzoai/cloud/clients/commerce/models/types/analytics"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
	"github.com/hanzoai/cloud/clients/commerce/util/json/http"
)

func Get(c *gin.Context) {
	org := middleware.GetOrganization(c)
	integrations := org.Analytics.UpdateShownDisabledStatus()
	http.Render(c, 200, integrations)
}

func Set(c *gin.Context) {
	org := middleware.GetOrganization(c)
	id := c.Params.ByName("organizationid")

	if id != org.Id() && id != org.Name && id != org.FullName {
		http.Fail(c, 403, "Organization Id does not match key", errors.New("Organization Id does not match key"))
		return
	}

	integrations := analytics.Analytics{}

	// Decode response body for listing
	if err := json.Decode(c.Request.Body, &integrations); err != nil {
		http.Fail(c, 400, "Failed decode request body", err)
		return
	}

	integrations.UpdateStoredDisabledStatus()

	// Update integrations
	org.Analytics = integrations

	// Save organization
	if err := org.Put(); err != nil {
		http.Fail(c, 500, "Failed to save analytics integrations", err)
	} else {
		c.Writer.Header().Add("Location", c.Request.URL.Path)
		http.Render(c, 201, integrations)
	}
}

func Update(c *gin.Context) {
	// Get organization
	org := middleware.GetOrganization(c)
	id := c.Params.ByName("organizationid")

	if id != org.Id() && id != org.Name && id != org.FullName {
		http.Fail(c, 403, "Organization Id does not match key", errors.New("Organization Id does not match key"))
		return
	}

	// Decode response body for listing
	if err := json.Decode(c.Request.Body, &org.Analytics); err != nil {
		http.Fail(c, 400, "Failed decode request body", err)
		return
	}

	org.Analytics.UpdateStoredDisabledStatus()

	if err := org.Put(); err != nil {
		http.Fail(c, 500, "Failed to save organization integrations", err)
	} else {
		c.Writer.Header().Add("Location", c.Request.URL.Path)
		http.Render(c, 201, org.Analytics)
	}
}
