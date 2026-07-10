package user

import (
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/auth/password"
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/demo/tokentransaction"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/middleware"
	"github.com/hanzoai/cloud/clients/commerce/models/affiliate"
	"github.com/hanzoai/cloud/clients/commerce/models/deprecated/subscription"
	"github.com/hanzoai/cloud/clients/commerce/models/order"
	"github.com/hanzoai/cloud/clients/commerce/models/paymentmethod"
	"github.com/hanzoai/cloud/clients/commerce/models/referral"
	"github.com/hanzoai/cloud/clients/commerce/models/referrer"
	"github.com/hanzoai/cloud/clients/commerce/models/transaction/util"
	"github.com/hanzoai/cloud/clients/commerce/models/transfer"
	"github.com/hanzoai/cloud/clients/commerce/models/user"
	"github.com/hanzoai/cloud/clients/commerce/util/json/http"
	"github.com/hanzoai/cloud/clients/commerce/util/rand"
)

type Password struct {
	Password string `json:"password"`
}

func resetPassword(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))
	id := c.Params.ByName("userid")

	u := user.New(db)
	if err := u.GetById(id); err != nil {
		http.Fail(c, 400, "Could not query user", err)
		return
	}

	newPassword := rand.ShortPassword()
	if hash, err := password.Hash(newPassword); err != nil { // pragma: allowlist secret
		http.Fail(c, 400, "Password generation failed", err)
		return
	} else {
		u.PasswordHash = hash
	}

	u.MustPut()
	http.Render(c, 200, Password{Password: newPassword})
}

func getReferrals(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))
	id := c.Params.ByName("userid")

	referrals := make([]referral.Referral, 0)
	if _, err := referral.Query(db).Filter("Referrer.UserId=", id).GetAll(&referrals); err != nil {
		http.Fail(c, 400, "Could not query referral", err)
		return
	}

	http.Render(c, 200, referrals)
}

func getReferrers(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))
	id := c.Params.ByName("userid")

	referrers := make([]referrer.Referrer, 0)
	if _, err := referrer.Query(db).Filter("UserId=", id).GetAll(&referrers); err != nil {
		http.Fail(c, 400, "Could not query referrer", err)
		return
	}

	http.Render(c, 200, referrers)
}

func getPaymentMethods(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))
	id := c.Params.ByName("userid")

	paymentMethods := make([]paymentmethod.PaymentMethod, 0)
	if _, err := paymentmethod.Query(db).Filter("UserId=", id).GetAll(&paymentMethods); err != nil {
		http.Fail(c, 400, "Could not query paymentMethod", err)
		return
	}

	http.Render(c, 200, paymentMethods)
}

func getSubscriptions(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))
	id := c.Params.ByName("userid")

	subscriptions := make([]subscription.Subscription, 0)
	if _, err := subscription.Query(db).Filter("UserId=", id).GetAll(&subscriptions); err != nil {
		http.Fail(c, 400, "Could not query subscription", err)
		return
	}

	http.Render(c, 200, subscriptions)
}

func getOrders(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))
	id := c.Params.ByName("userid")

	orders := make([]order.Order, 0)
	if _, err := order.Query(db).Filter("UserId=", id).GetAll(&orders); err != nil {
		http.Fail(c, 400, "Could not query order", err)
		return
	}

	http.Render(c, 200, orders)
}

func getTransactions(c *gin.Context) {
	org := middleware.GetOrganization(c)
	ctx := org.Namespaced(c)
	id := c.Params.ByName("userid")

	res, err := util.GetTransactions(ctx, id, "user", !org.Live)
	if err != nil {
		log.Error("transaction/%v/%v error: '%v'", id, "user", err, c)
		http.Fail(c, 400, err.Error(), err)
		return
	}

	http.Render(c, 200, res)
}

func getTokenTransactions(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))
	id := c.Params.ByName("userid")

	tt := make([]*tokentransaction.Transaction, 0)
	if _, err := tokentransaction.Query(db).Filter("SendingUserId=", id).GetAll(&tt); err != nil {
		log.Error("tokentransaction/%v/%v error: '%v'", id, "user", err, c)
		http.Fail(c, 400, err.Error(), err)
	}

	tt2 := make([]*tokentransaction.Transaction, 0)
	if _, err := tokentransaction.Query(db).Filter("ReceivingUserId=", id).GetAll(&tt2); err != nil {
		log.Error("tokentransaction/%v/%v error: '%v'", id, "user", err, c)
		http.Fail(c, 400, err.Error(), err)
	}

	tt3 := append(tt, tt2...)

	http.Render(c, 200, tt3)
}

func getTransfers(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))
	id := c.Params.ByName("userid")

	usr := user.New(db)
	if err := usr.GetById(id); err != nil {
		http.Fail(c, 400, "Could not query user", err)
		return
	}

	trans := make([]transfer.Transfer, 0)
	if _, err := transfer.Query(db).Filter("AffiliateId=", usr.AffiliateId).GetAll(&trans); err != nil {
		http.Fail(c, 400, "Could not query transfer", err)
		return
	}

	http.Render(c, 200, trans)
}

func getAffiliate(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))
	id := c.Params.ByName("userid")

	usr := user.New(db)
	if err := usr.GetById(id); err != nil {
		http.Fail(c, 400, "Could not query user", err)
		return
	}

	aff := affiliate.New(db)
	if err := aff.GetById(usr.AffiliateId); err != nil {
		http.Fail(c, 400, "Could not query affiliate", err)
		return
	}

	http.Render(c, 200, aff)
}
