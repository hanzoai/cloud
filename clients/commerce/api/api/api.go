package api

import (
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/config"
	"github.com/hanzoai/cloud/clients/commerce/delay"
	"github.com/hanzoai/cloud/clients/commerce/demo/disclosure"
	"github.com/hanzoai/cloud/clients/commerce/demo/tokentransaction"
	"github.com/hanzoai/cloud/clients/commerce/middleware"
	"github.com/hanzoai/cloud/clients/commerce/models/collection"
	"github.com/hanzoai/cloud/clients/commerce/models/discount"
	"github.com/hanzoai/cloud/clients/commerce/models/movie"
	"github.com/hanzoai/cloud/clients/commerce/models/note"
	"github.com/hanzoai/cloud/clients/commerce/models/payment"
	"github.com/hanzoai/cloud/clients/commerce/models/product"
	"github.com/hanzoai/cloud/clients/commerce/models/return"
	"github.com/hanzoai/cloud/clients/commerce/models/saleschannel"
	"github.com/hanzoai/cloud/clients/commerce/models/site"
	"github.com/hanzoai/cloud/clients/commerce/models/stocklocation"
	"github.com/hanzoai/cloud/clients/commerce/models/submission"
	"github.com/hanzoai/cloud/clients/commerce/models/subscriber"
	"github.com/hanzoai/cloud/clients/commerce/models/token"
	// "github.com/hanzoai/cloud/clients/commerce/models/transaction"
	"github.com/hanzoai/cloud/clients/commerce/models/transfer"
	"github.com/hanzoai/cloud/clients/commerce/models/user"
	"github.com/hanzoai/cloud/clients/commerce/models/variant"
	"github.com/hanzoai/cloud/clients/commerce/models/wallet"
	"github.com/hanzoai/cloud/clients/commerce/models/watchlist"
	"github.com/hanzoai/cloud/clients/commerce/models/webhook"
	"github.com/hanzoai/cloud/clients/commerce/util/permission"
	"github.com/hanzoai/cloud/clients/commerce/util/rest"
	"github.com/hanzoai/cloud/clients/commerce/util/router"

	accessTokenApi "github.com/hanzoai/cloud/clients/commerce/api/accesstoken"
	accountApi "github.com/hanzoai/cloud/clients/commerce/api/account"
	affiliateApi "github.com/hanzoai/cloud/clients/commerce/api/affiliate"
	apikeyApi "github.com/hanzoai/cloud/clients/commerce/api/apikey"
	authApi "github.com/hanzoai/cloud/clients/commerce/api/auth"
	b2bApi "github.com/hanzoai/cloud/clients/commerce/api/b2b"
	billingApi "github.com/hanzoai/cloud/clients/commerce/api/billing"
	cartApi "github.com/hanzoai/cloud/clients/commerce/api/cart"
	catalogApi "github.com/hanzoai/cloud/clients/commerce/api/catalog"
	cdnApi "github.com/hanzoai/cloud/clients/commerce/api/cdn"
	checkoutApi "github.com/hanzoai/cloud/clients/commerce/api/checkout"
	costsApi "github.com/hanzoai/cloud/clients/commerce/api/costs"
	counterApi "github.com/hanzoai/cloud/clients/commerce/api/counter"
	couponApi "github.com/hanzoai/cloud/clients/commerce/api/coupon"
	customergroupApi "github.com/hanzoai/cloud/clients/commerce/api/customergroup"
	dataApi "github.com/hanzoai/cloud/clients/commerce/api/data"
	deployApi "github.com/hanzoai/cloud/clients/commerce/api/deploy"
	exchangeApi "github.com/hanzoai/cloud/clients/commerce/api/exchange"
	formApi "github.com/hanzoai/cloud/clients/commerce/api/form"
	fulfillmentApi "github.com/hanzoai/cloud/clients/commerce/api/fulfillment"
	giftcardApi "github.com/hanzoai/cloud/clients/commerce/api/giftcard"
	inventoryApi "github.com/hanzoai/cloud/clients/commerce/api/inventory"
	libraryApi "github.com/hanzoai/cloud/clients/commerce/api/library"
	namespaceApi "github.com/hanzoai/cloud/clients/commerce/api/namespace"
	notificationApi "github.com/hanzoai/cloud/clients/commerce/api/notification"
	orderApi "github.com/hanzoai/cloud/clients/commerce/api/order"
	organizationApi "github.com/hanzoai/cloud/clients/commerce/api/organization"
	pricingApi "github.com/hanzoai/cloud/clients/commerce/api/pricing"
	producttaxonomyApi "github.com/hanzoai/cloud/clients/commerce/api/producttaxonomy"
	promotionApi "github.com/hanzoai/cloud/clients/commerce/api/promotion"
	referralApi "github.com/hanzoai/cloud/clients/commerce/api/referral"
	regionApi "github.com/hanzoai/cloud/clients/commerce/api/region"
	reviewApi "github.com/hanzoai/cloud/clients/commerce/api/review"
	searchApi "github.com/hanzoai/cloud/clients/commerce/api/search"
	storeApi "github.com/hanzoai/cloud/clients/commerce/api/store"
	subscriptionApi "github.com/hanzoai/cloud/clients/commerce/api/subscription"
	taxApi "github.com/hanzoai/cloud/clients/commerce/api/tax"
	transactionApi "github.com/hanzoai/cloud/clients/commerce/api/transaction"
	userApi "github.com/hanzoai/cloud/clients/commerce/api/user"
	xdApi "github.com/hanzoai/cloud/clients/commerce/api/xd"

	dashv2Api "github.com/hanzoai/cloud/clients/commerce/api/dashv2"
	bitcoinApi "github.com/hanzoai/cloud/clients/commerce/thirdparty/bitcoin/api"
	mercuryApi "github.com/hanzoai/cloud/clients/commerce/thirdparty/mercury/api"
	paypalApi "github.com/hanzoai/cloud/clients/commerce/thirdparty/paypal/ipn"
	reamazeApi "github.com/hanzoai/cloud/clients/commerce/thirdparty/reamaze"
	shipstationApi "github.com/hanzoai/cloud/clients/commerce/thirdparty/shipstation"
	shipwireApi "github.com/hanzoai/cloud/clients/commerce/thirdparty/shipwire/api"

	// Side effect import because of cyclical dependency
	_ "github.com/hanzoai/cloud/clients/commerce/models/referrer/tasks"
)

func Route(api router.Router) {
	tokenRequired := middleware.TokenRequired()
	adminRequired := middleware.TokenRequired(permission.Admin)

	// Index
	if config.IsDevelopment {
		api.GET("/", middleware.ParseToken, rest.ListRoutes())
	} else {
		api.GET("/", router.Ok)
		api.HEAD("/", router.Empty)
	}

	// Use permissive CORS policy for all API routes.
	api.Use(middleware.AccessControl("*"))
	api.OPTIONS("*wildcard", func(c *gin.Context) {
		c.Next()
	})

	// Setup routes for delay funcs
	api.POST(delay.Path, func(c *gin.Context) {
		ctx := c.Request.Context()
		delay.RunFunc(ctx, c.Writer, c.Request)
	})

	// Checkout APIs (charge, authorize, capture)
	checkoutApi.Route(api)

	subscriptionApi.Route(api)

	// Models with public RESTful API
	rest.New(collection.Collection{}).Route(api, tokenRequired)
	rest.New(discount.Discount{}).Route(api, tokenRequired)
	rest.New(movie.Movie{}).Route(api, tokenRequired)
	rest.New(note.Note{}).Route(api, tokenRequired)
	rest.New(product.Product{}).Route(api, tokenRequired)
	rest.New(return_.Return{}).Route(api, tokenRequired)
	rest.New(site.Site{}).Route(api, tokenRequired)
	rest.New(submission.Submission{}).Route(api, tokenRequired)
	rest.New(subscriber.Subscriber{}).Route(api, tokenRequired)
	// rest.New(transaction.Transaction{}).Route(api, tokenRequired)
	rest.New(transfer.Transfer{}).Route(api, tokenRequired)
	rest.New(variant.Variant{}).Route(api, tokenRequired)
	rest.New(wallet.Wallet{}).Route(api, adminRequired)
	rest.New(watchlist.Watchlist{}).Route(api, tokenRequired)
	rest.New(webhook.Webhook{}).Route(api, adminRequired)

	rest.New(saleschannel.SalesChannel{}).Route(api, tokenRequired)
	rest.New(stocklocation.StockLocation{}).Route(api, tokenRequired)

	rest.New(disclosure.Disclosure{}).Route(api, tokenRequired)
	rest.New(tokentransaction.Transaction{}).Route(api, tokenRequired)

	paymentApi := rest.New(payment.Payment{})
	paymentApi.POST("/:paymentid/refund", checkoutApi.Refund)
	paymentApi.Route(api, tokenRequired)

	accountApi.Route(api, tokenRequired)
	billingApi.Route(api, tokenRequired)
	costsApi.Route(api, tokenRequired)
	cartApi.Route(api, tokenRequired)
	couponApi.Route(api, tokenRequired)
	deployApi.Route(api, tokenRequired)
	formApi.Route(api, tokenRequired)
	inventoryApi.Route(api, tokenRequired)
	orderApi.Route(api, tokenRequired)
	referralApi.Route(api, tokenRequired)
	affiliateApi.Route(api, tokenRequired)
	regionApi.Route(api, tokenRequired)
	reviewApi.Route(api, tokenRequired)
	storeApi.Route(api, tokenRequired)
	transactionApi.Route(api, tokenRequired)
	userApi.Route(api, tokenRequired)
	pricingApi.Route(api, tokenRequired)
	promotionApi.Route(api, tokenRequired)

	// Medusa-parity admin domains. These sub-routers were fully implemented
	// (models orm.Register'd, tenant-scoped via middleware.Namespace) but never
	// wired into the /v1 bundle, so their routes 404'd in production. Wiring
	// them here completes the fulfillment/shipping, tax, customer-group,
	// publishable-api-key/RBAC, and notification admin domains.
	fulfillmentApi.Route(api, tokenRequired) // fulfillment sets/providers, shipping options/profiles, service+geo zones, ship/cancel
	taxApi.Route(api, tokenRequired)         // tax regions/rates/rules/providers + /tax/calculate
	customergroupApi.Route(api, tokenRequired)
	apikeyApi.Route(api, tokenRequired) // publishable API keys, roles, api permissions
	notificationApi.Route(api, tokenRequired)
	giftcardApi.Route(api, adminRequired)        // gift cards + idempotent redeem/void (money — admin only)
	b2bApi.Route(api, tokenRequired)             // B2B: companies, employees, quotes, approvals
	exchangeApi.Route(api, tokenRequired)        // order exchanges (return + replacement)
	producttaxonomyApi.Route(api, tokenRequired) // product options/values, categories, tags, types, return/refund reasons
	catalogApi.AdminRoute(api, adminRequired)    // platform product catalog CMS (SuperAdmin gated inside)
	// Public catalog projection GET /v1/commerce/catalog is wired on the
	// commerce public group (commerce.go) so it serves that exact path.

	// Hanzo APIs, using default namespace (internal use only)
	organizationApi.Route(api, tokenRequired)

	token := rest.New(token.Token{})
	token.DefaultNamespace = true
	token.Prefix = "/c/"
	token.Route(api, tokenRequired)

	user := rest.New(user.User{})
	user.DefaultNamespace = true
	user.Prefix = "/c/"
	user.Route(api, tokenRequired)

	searchApi.Route(api, tokenRequired)

	// Namespace API
	namespaceApi.Route(api)

	// Access token API
	accessTokenApi.Route(api)

	// OAuth API
	authApi.Route(api)

	// Reamaze custom store API endpoints
	reamazeApi.Route(api)

	// Shipstation custom store API endpoints
	shipstationApi.Route(api)

	// Shipwire custom store API endpoints
	shipwireApi.Route(api)

	// Paypal IPN
	paypalApi.Route(api)

	// Data Api
	dataApi.Route(api)

	// XDomain proxy.html
	xdApi.Route(api)

	// Routes from deprecated cdn module
	cdnApi.Route(api)

	// dashv2
	dashv2Api.Route(api)

	// Counter Api (admin only)
	counterApi.Route(api)

	// Library Api
	libraryApi.Route(api)

	// Marketing routes moved to github.com/hanzoai/marketing

	// Bitcoin webhook
	bitcoinApi.Route(api)

	// Mercury bank webhook
	mercuryApi.Route(api)

	// Extra routes registered by optional sub-modules (e.g. luxfi/cevm)
	applyExtraRoutes(api)
}
