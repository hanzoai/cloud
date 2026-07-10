package form

import (
	"github.com/gin-gonic/gin"

	cdn "github.com/hanzoai/cloud/clients/commerce/api/cdn/form"
	"github.com/hanzoai/cloud/clients/commerce/middleware"
	"github.com/hanzoai/cloud/clients/commerce/models/form"
	"github.com/hanzoai/cloud/clients/commerce/util/rest"
	"github.com/hanzoai/cloud/clients/commerce/util/router"
)

func Route(router router.Router, args ...gin.HandlerFunc) {
	rest.New(form.Form{}).Route(router, args...)

	f := router.Group("form")
	f.Use(middleware.AccessControl("*"))

	f.POST("/:formid/submit", handleForm)
	f.POST("/:formid/subscribe", handleForm)
	f.GET("/:formid/js", cdn.Js)

	// DEPRECATED
	m := router.Group("mailinglist")
	m.Use(middleware.AccessControl("*"))

	m.POST("/:formid/submit", handleForm)
	m.POST("/:formid/subscribe", handleForm)
	m.GET("/:formid/js", cdn.Js)
}
