package billing

import (
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/billing/engine"
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/middleware"
	"github.com/hanzoai/cloud/clients/commerce/models/billinginvoice"
	"github.com/hanzoai/cloud/clients/commerce/models/paymentmethod"
	"github.com/hanzoai/cloud/clients/commerce/models/subscription"
	"github.com/hanzoai/cloud/clients/commerce/util/json/http"
)

// PortalOverview returns a billing summary for the authenticated customer.
//
//	GET /v1/billing/portal/overview?customerId=...
func PortalOverview(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	customerId := c.Query("customerId")
	if customerId == "" {
		http.Fail(c, 400, "customerId is required", nil)
		return
	}

	// Get balance
	cb, _ := engine.GetOrCreateCustomerBalance(db, customerId, "usd")
	balance := int64(0)
	if cb != nil {
		balance = cb.Balance
	}

	// Count active subscriptions
	rootKey := db.NewKey("synckey", "", 1, nil)
	subs := make([]*subscription.Subscription, 0)
	sq := subscription.Query(db).Ancestor(rootKey).
		Filter("UserId=", customerId).
		Filter("Status=", string(subscription.Active))
	_, _ = sq.GetAll(&subs)

	// Count payment methods
	pms := make([]*paymentmethod.PaymentMethod, 0)
	pmq := paymentmethod.Query(db).Ancestor(rootKey).
		Filter("CustomerId=", customerId)
	_, _ = pmq.GetAll(&pms)

	c.JSON(200, gin.H{
		"customerId":          customerId,
		"balance":             balance,
		"currency":            "usd",
		"activeSubscriptions": len(subs),
		"paymentMethods":      len(pms),
	})
}

// PortalInvoices returns the customer's invoice list.
//
//	GET /v1/billing/portal/invoices?customerId=...
func PortalInvoices(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	customerId := c.Query("customerId")
	if customerId == "" {
		http.Fail(c, 400, "customerId is required", nil)
		return
	}

	rootKey := db.NewKey("synckey", "", 1, nil)
	invoices := make([]*billinginvoice.BillingInvoice, 0)
	q := billinginvoice.Query(db).Ancestor(rootKey).
		Filter("UserId=", customerId).
		Order("-Created")
	_, _ = q.GetAll(&invoices)

	results := make([]gin.H, len(invoices))
	for i, inv := range invoices {
		results[i] = gin.H{
			"id":        inv.Id(),
			"number":    inv.NumberStr,
			"status":    inv.Status,
			"amountDue": inv.AmountDue,
			"currency":  inv.Currency,
			"created":   inv.Created,
		}
	}
	c.JSON(200, results)
}

// PortalSubscriptions returns the customer's subscriptions.
//
//	GET /v1/billing/portal/subscriptions?customerId=...
func PortalSubscriptions(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	customerId := c.Query("customerId")
	if customerId == "" {
		http.Fail(c, 400, "customerId is required", nil)
		return
	}

	rootKey := db.NewKey("synckey", "", 1, nil)
	subs := make([]*subscription.Subscription, 0)
	q := subscription.Query(db).Ancestor(rootKey).
		Filter("UserId=", customerId).
		Order("-Created")
	_, _ = q.GetAll(&subs)

	results := make([]gin.H, len(subs))
	for i, s := range subs {
		results[i] = gin.H{
			"id":          s.Id(),
			"planId":      s.PlanId,
			"status":      s.Status,
			"periodStart": s.PeriodStart,
			"periodEnd":   s.PeriodEnd,
			"created":     s.Created,
		}
	}
	c.JSON(200, results)
}

// PortalPaymentMethods returns the customer's payment methods.
//
//	GET /v1/billing/portal/payment-methods?customerId=...
func PortalPaymentMethods(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	customerId := c.Query("customerId")
	if customerId == "" {
		http.Fail(c, 400, "customerId is required", nil)
		return
	}

	rootKey := db.NewKey("synckey", "", 1, nil)
	methods := make([]*paymentmethod.PaymentMethod, 0)
	q := paymentmethod.Query(db).Ancestor(rootKey).
		Filter("CustomerId=", customerId).
		Order("-Created")
	_, _ = q.GetAll(&methods)

	results := make([]map[string]interface{}, len(methods))
	for i, pm := range methods {
		results[i] = paymentMethodResponse(pm)
	}
	c.JSON(200, results)
}
