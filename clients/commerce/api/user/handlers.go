package user

import (
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/api/user/wallet"
	"github.com/hanzoai/cloud/clients/commerce/middleware"
	"github.com/hanzoai/cloud/clients/commerce/models/user"
	"github.com/hanzoai/cloud/clients/commerce/util/permission"
	"github.com/hanzoai/cloud/clients/commerce/util/rest"
	"github.com/hanzoai/cloud/clients/commerce/util/router"
)

func Route(router router.Router, args ...gin.HandlerFunc) {
	readUserRequired := middleware.TokenRequired(permission.Admin, permission.ReadUser)
	writeUserRequired := middleware.TokenRequired(permission.Admin, permission.WriteUser)
	readOrderOrSubscriptionRequired := middleware.TokenRequired(permission.Admin, permission.ReadOrder)
	readReferralRequired := middleware.TokenRequired(permission.Admin, permission.ReadReferral)
	readReferrerRequired := middleware.TokenRequired(permission.Admin, permission.ReadReferrer)

	api := rest.New(user.User{})
	api.GET("/:userid/password/reset", writeUserRequired, resetPassword)
	api.GET("/:userid/orders", readUserRequired, readOrderOrSubscriptionRequired, getOrders)
	api.GET("/:userid/referrals", readUserRequired, readReferralRequired, getReferrals)
	api.GET("/:userid/referrers", readUserRequired, readReferrerRequired, getReferrers)
	api.GET("/:userid/transactions", readUserRequired, getTransactions)
	api.GET("/:userid/tokentransactions", readUserRequired, getTokenTransactions)
	api.GET("/:userid/paymentmethods", readUserRequired, getPaymentMethods)
	api.GET("/:userid/transfer", readUserRequired, getTransfers)
	api.GET("/:userid/affiliate", readUserRequired, getAffiliate)

	api.GET("/:userid/wallet", writeUserRequired, wallet.Get)
	api.GET("/:userid/wallet/account/:name", writeUserRequired, wallet.GetAccount)
	api.POST("/:userid/wallet/account", writeUserRequired, wallet.CreateAccount)
	api.POST("/:userid/wallet/pay", writeUserRequired, wallet.Send)

	api.Route(router, args...)
}
