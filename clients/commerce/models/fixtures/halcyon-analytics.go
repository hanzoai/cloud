package fixtures

import (
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/models/types/analytics"
)

var _ = New("halcyon-analytics", func(c *gin.Context) *organization.Organization {
	db := datastore.New(c)

	org := organization.New(db)
	org.Name = "halcyon"
	org.GetOrCreate("Name=", org.Name)

	ga := analytics.Integration{}
	ga.Type = "google-analytics"
	ga.Id = "UA-123218175-1"
	ga.Event = ""
	ga.Sampling = 0
	ga.Disabled = false
	ga.IntegrationId = "_BNIFVhpgac"

	fb := analytics.Integration{}
	fb.Type = "facebook-pixel"
	fb.Id = "105561333280533"
	fb.Event = ""
	fb.Sampling = 0
	fb.Disabled = false
	fb.IntegrationId = "uNa4fzXu10"

	ans := analytics.Analytics{}
	ans.Integrations = append(ans.Integrations, ga, fb)
	org.Analytics = ans

	org.MustUpdate()
	return org
})
