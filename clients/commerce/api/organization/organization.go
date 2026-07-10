package organization

import (
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/api/organization/analytics"
	"github.com/hanzoai/cloud/clients/commerce/api/organization/integrations"
	"github.com/hanzoai/cloud/clients/commerce/api/organization/wallet"
	"github.com/hanzoai/cloud/clients/commerce/middleware"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/util/permission"
	"github.com/hanzoai/cloud/clients/commerce/util/rest"
	"github.com/hanzoai/cloud/clients/commerce/util/router"

	. "github.com/hanzoai/cloud/clients/commerce/api/organization/newroutes"
)

func Route(router router.Router, args ...gin.HandlerFunc) {
	adminRequired := middleware.TokenRequired(permission.Admin)
	publishedRequired := middleware.TokenRequired(permission.Admin, permission.Published)
	namespaced := middleware.Namespace()

	// Fix this stuff so its secure
	api := rest.New(organization.Organization{})
	api.DefaultNamespace = true
	// Older stuff
	api.Prefix = "/c/"

	api.GET("/:organizationid/analytics", adminRequired, namespaced, analytics.Get)
	api.POST("/:organizationid/analytics", adminRequired, namespaced, analytics.Set)
	api.PUT("/:organizationid/analytics", adminRequired, namespaced, analytics.Set)
	api.PATCH("/:organizationid/analytics", adminRequired, namespaced, analytics.Update)

	api.GET("/:organizationid/integrations", adminRequired, namespaced, integrations.Get)
	api.POST("/:organizationid/integrations", adminRequired, namespaced, integrations.Upsert)
	api.PUT("/:organizationid/integrations", adminRequired, namespaced, integrations.Upsert)
	api.PATCH("/:organizationid/integrations", adminRequired, namespaced, integrations.Upsert)
	api.DELETE("/:organizationid/integrations/:integrationid", adminRequired, namespaced, integrations.Delete)

	api.GET("/:organizationid/wallet", adminRequired, namespaced, wallet.Get)
	api.GET("/:organizationid/wallet/account/:name", adminRequired, namespaced, wallet.GetAccount)
	api.POST("/:organizationid/wallet/account", adminRequired, namespaced, wallet.CreateAccount)
	api.POST("/:organizationid/wallet/send", adminRequired, namespaced, wallet.Send)

	api.Route(router, args...)

	// Newer stuff
	api2 := router.Group("organization")
	api2.GET("/publicwithdrawableaccounts", publishedRequired, GetWithdrawableAccounts)
}
