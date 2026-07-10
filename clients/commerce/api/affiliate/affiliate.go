package affiliate

import (
	"errors"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/middleware"
	"github.com/hanzoai/cloud/clients/commerce/models/affiliate"
	"github.com/hanzoai/cloud/clients/commerce/models/order"
	mdlreferral "github.com/hanzoai/cloud/clients/commerce/models/referral"
	"github.com/hanzoai/cloud/clients/commerce/models/referrer"
	"github.com/hanzoai/cloud/clients/commerce/models/transaction"
	"github.com/hanzoai/cloud/clients/commerce/models/user"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
	"github.com/hanzoai/cloud/clients/commerce/util/json/http"
	"github.com/hanzoai/cloud/clients/commerce/util/rest"
)

// affiliateConnect initiates payment processor OAuth for an affiliate.
// Legacy Stripe Connect removed; affiliate payment integration pending.
func affiliateConnect(c *gin.Context) {
	http.Fail(c, 503, "affiliate payment connect not available", errors.New("payment processor connect not configured"))
}

// affiliateGetReferrals returns all referrals for an affiliate.
//
//	GET /v1/affiliate/:affiliateid/referrals
func affiliateGetReferrals(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))
	id := c.Params.ByName("affiliateid")

	referrals := make([]mdlreferral.Referral, 0)
	if _, err := mdlreferral.Query(db).Filter("Referrer.AffiliateId=", id).GetAll(&referrals); err != nil {
		http.Fail(c, 400, "Could not query referral", err)
		return
	}

	http.Render(c, 200, referrals)
}

// affiliateGetReferrers returns all referrers for an affiliate.
//
//	GET /v1/affiliate/:affiliateid/referrers
func affiliateGetReferrers(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))
	id := c.Params.ByName("affiliateid")

	referrers := make([]referrer.Referrer, 0)
	if _, err := referrer.Query(db).Filter("AffiliateId=", id).GetAll(&referrers); err != nil {
		http.Fail(c, 400, "Could not query referrer", err)
		return
	}

	http.Render(c, 200, referrers)
}

// affiliateGetOrders returns all orders attributed to an affiliate.
//
//	GET /v1/affiliate/:affiliateid/orders
func affiliateGetOrders(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))
	id := c.Params.ByName("affiliateid")

	orders := make([]order.Order, 0)
	if _, err := order.Query(db).Filter("AffiliateId=", id).GetAll(&orders); err != nil {
		http.Fail(c, 400, "Could not query order", err)
		return
	}

	http.Render(c, 200, orders)
}

// affiliateGetTransactions returns all transactions for an affiliate.
//
//	GET /v1/affiliate/:affiliateid/transactions
func affiliateGetTransactions(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))
	id := c.Params.ByName("affiliateid")

	trans := make([]transaction.Transaction, 0)
	if _, err := transaction.Query(db).Filter("Test=", false).Filter("AffiliateId=", id).GetAll(&trans); err != nil {
		http.Fail(c, 400, "Could not query transaction", err)
		return
	}

	http.Render(c, 200, trans)
}

// affiliateCreate returns the custom Create handler for affiliate CRUD.
func affiliateCreate(r *rest.Rest) func(*gin.Context) {
	return func(c *gin.Context) {
		if !r.CheckPermissions(c, "create") {
			return
		}

		db := datastore.New(c)
		aff := affiliate.New(db)

		// Decode request
		if err := json.Decode(c.Request.Body, aff); err != nil {
			r.Fail(c, 400, "Failed decode request body", err)
			return
		}

		// Affiliates can only be created for pre-existing users
		if aff.UserId == "" {
			r.Fail(c, 500, "UserId required", errors.New("UserId required"))
			return
		}

		// Find user which will be turned into affiliate
		usr := user.New(db)
		if err := usr.GetById(aff.UserId); err != nil {
			r.Fail(c, 500, "User does not exist: "+aff.UserId, err)
			return
		}

		// Don't create multiple affiliates per user
		if usr.AffiliateId != "" {
			r.Fail(c, 500, "User already is affiliate: "+usr.AffiliateId, errors.New("User already is affiliate: "+usr.AffiliateId))
			return
		}

		// Create affiliate
		if err := aff.Create(); err != nil {
			r.Fail(c, 500, "Failed to create "+r.Kind, err)
			return
		}

		// Create special referrer for affiliate
		ref := referrer.New(db)
		ref.AffiliateId = aff.Id()
		ref.UserId = usr.Id()
		if err := ref.Create(); err != nil {
			r.Fail(c, 500, "Failed to create "+r.Kind, err)
			return
		}

		// Update user with affiliate information
		usr.AffiliateId = aff.Id()
		if err := usr.Update(); err != nil {
			r.Fail(c, 500, "Failed to update user: "+usr.Id(), err)
		}

		c.Writer.Header().Add("Location", c.Request.URL.Path+"/"+aff.Id())
		r.Render(c, 201, aff)
	}
}
