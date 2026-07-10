package store

import (
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/middleware"
	"github.com/hanzoai/cloud/clients/commerce/models/store"
	"github.com/hanzoai/cloud/clients/commerce/util/permission"
	"github.com/hanzoai/cloud/clients/commerce/util/rest"
	"github.com/hanzoai/cloud/clients/commerce/util/router"
)

func Route(router router.Router, args ...gin.HandlerFunc) {
	adminRequired := middleware.TokenRequired(permission.Admin)
	publishedRequired := middleware.TokenRequired(permission.Admin, permission.Published)
	namespaced := middleware.Namespace()

	api := rest.New(store.Store{})

	// Admin dashboard expects /store/current to return the default store.
	api.GET("/current", getCurrent)

	// Mint the org's least-privilege Published storefront read key (design
	// path b). Admin-gated + org-bound; the returned token is stored in KMS and
	// injected as HANZO_COMMERCE_STOREFRONT_TOKEN on the storefront.
	api.POST("/storefront-token", adminRequired, namespaced, mintStorefrontToken)

	// API for getting a full product/variant/bundle for a specific store
	api.POST("/:storeid/authorize", publishedRequired, namespaced, authorize)
	api.POST("/:storeid/authorize/:orderid", publishedRequired, namespaced, authorize)
	api.POST("/:storeid/capture/:orderid", publishedRequired, namespaced, capture)
	api.POST("/:storeid/charge", publishedRequired, namespaced, charge)
	api.POST("/:storeid/paypal/pay", publishedRequired, namespaced, authorize)
	api.POST("/:storeid/paypal/confirm/:payKey", publishedRequired, namespaced, confirm)
	api.POST("/:storeid/paypal/cancel/:payKey", publishedRequired, namespaced, cancel)

	// Support new checkout prefixed methods
	api.POST("/:storeid/checkout/authorize", publishedRequired, namespaced, authorize)
	api.POST("/:storeid/checkout/authorize/:orderid", publishedRequired, namespaced, authorize)
	api.POST("/:storeid/checkout/capture/:orderid", publishedRequired, namespaced, capture)
	api.POST("/:storeid/checkout/charge", publishedRequired, namespaced, charge)
	api.POST("/:storeid/checkout/paypal/pay", publishedRequired, namespaced, authorize)
	api.POST("/:storeid/checkout/paypal/confirm/:payKey", publishedRequired, namespaced, confirm)
	api.POST("/:storeid/checkout/paypal/cancel/:payKey", publishedRequired, namespaced, cancel)

	// API for getting a full product/variant/bundle for a specific store
	api.GET("/:storeid/bundle/:key", publishedRequired, namespaced, getItem("bundle"))
	api.GET("/:storeid/product/:key", publishedRequired, namespaced, getItem("product"))
	api.GET("/:storeid/variant/:key", publishedRequired, namespaced, getItem("variant"))

	// API for working with listings directly
	api.GET("/:storeid/listing", publishedRequired, namespaced, listListing)
	api.GET("/:storeid/listing/:key", publishedRequired, namespaced, getListing)

	api.POST("/:storeid/listing/:key", adminRequired, namespaced, createListing)
	api.PUT("/:storeid/listing/:key", adminRequired, namespaced, updateListing)
	api.PATCH("/:storeid/listing/:key", adminRequired, namespaced, patchListing)
	api.DELETE("/:storeid/listing/:key", adminRequired, namespaced, deleteListing)

	api.Route(router, args...)
}
