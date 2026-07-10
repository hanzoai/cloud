package subscription

import (
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/models/plan"
	"github.com/hanzoai/cloud/clients/commerce/models/subscription"
	"github.com/hanzoai/cloud/clients/commerce/models/user"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
)

func subscriptionRequest(c *gin.Context, org *organization.Organization) (*SubscriptionReq, error) {
	// Create AuthReq properly by calling order.New
	sr := new(SubscriptionReq)
	sr.Db = datastore.New(org.Namespaced(c))

	// Try decode request body
	if err := json.Decode(c.Request.Body, &sr); err != nil {
		log.Error("Failed to decode request body: %v\n%v", c.Request.Body, err, c)
		return nil, FailedToDecodeRequestBody
	}

	return sr, nil
}

func subscribe(c *gin.Context, org *organization.Organization) (*subscription.Subscription, *user.User, error) {
	ctx := org.Datastore().Context
	nsCtx := org.Namespaced(ctx)
	db := datastore.New(nsCtx)

	// Parse request
	sr, err := subscriptionRequest(c, org)
	if err != nil {
		return nil, nil, err
	}

	log.Debug("AuthorizationReq.User_: %#v", sr.User_, c)
	log.Debug("AuthorizationReq.Subscription_: %#v", sr.Subscription_, c)

	sub, err := sr.Subscription()
	if err != nil {
		return nil, nil, err
	}
	log.Debug("Subscription: %#v", sub, c)

	// Subscription w/o quantity defaults to 1
	if sub.Quantity < 1 {
		sub.Quantity = 1
	}

	// Try to find plan
	pln := plan.New(db)
	err = pln.GetById(sub.PlanId)
	if err != nil {
		return nil, nil, PlanDoesNotExist
	}
	log.Debug("Plan: %#v", pln, c)

	// Get user
	usr, err := sr.User()
	if err != nil {
		return nil, nil, err
	}
	log.Debug("User: %#v", usr, c)

	// Payment information
	sub.Buyer = usr.Buyer()
	log.Debug("Buyer: %#v", sub.Buyer, c)

	if org.IsTestEmail(sub.Buyer.Email) {
		sub.Test = true
	}

	// Parent subscription to user
	sub.Parent = usr.Key()
	sub.UserId = usr.Id()

	// Set plan on subscription
	sub.PlanId = pln.Id()
	sub.Plan = *pln

	// Save user and subscription
	usr.MustPut()
	sub.MustPut()

	return sub, usr, nil
}

func updateSubscribe(c *gin.Context, org *organization.Organization, sub *subscription.Subscription) (*subscription.Subscription, error) {
	ctx := org.Datastore().Context
	nsCtx := org.Namespaced(ctx)
	db := datastore.New(nsCtx)

	userId := sub.UserId

	// Try decode request body
	if err := json.Decode(c.Request.Body, &sub); err != nil {
		log.Error("Failed to decode request body: %v\n%v", c.Request.Body, err, c)
		return nil, FailedToDecodeRequestBody
	}

	if userId != sub.UserId {
		return nil, CannotChangeUser
	}

	log.Warn("Quantity %v", sub.Quantity)

	// Delete Case
	if sub.Quantity < 1 {
		return unsubscribe(c, org, sub)
	}

	pln := plan.New(db)
	err := pln.GetById(sub.PlanId)
	if err != nil {
		return nil, PlanDoesNotExist
	}
	log.Debug("Plan: %#v", pln, c)

	sub.Plan = *pln

	sub.MustPut()

	return sub, nil
}

func unsubscribe(c *gin.Context, org *organization.Organization, sub *subscription.Subscription) (*subscription.Subscription, error) {
	_ = c
	_ = org

	sub.MustPut()

	return sub, nil
}
