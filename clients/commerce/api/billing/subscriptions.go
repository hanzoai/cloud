package billing

import (
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/billing/engine"
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/middleware"
	"github.com/hanzoai/cloud/clients/commerce/models/plan"
	"github.com/hanzoai/cloud/clients/commerce/models/subscription"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	types "github.com/hanzoai/cloud/clients/commerce/types"
	"github.com/hanzoai/cloud/clients/commerce/util/json/http"
)

type createSubscriptionRequest struct {
	UserId               string                 `json:"userId"`
	PlanId               string                 `json:"planId"`
	DefaultPaymentMethod string                 `json:"defaultPaymentMethod"`
	Metadata             map[string]interface{} `json:"metadata"`
}

type updateSubscriptionRequest struct {
	PlanId   string `json:"planId"`
	Prorate  bool   `json:"prorate"`
	Quantity int    `json:"quantity"`
}

type cancelSubscriptionRequest struct {
	AtPeriodEnd bool `json:"atPeriodEnd"`
}

// CreateBillingSubscription creates a new subscription and starts the billing lifecycle.
//
//	POST /v1/billing/subscriptions
func CreateBillingSubscription(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	var req createSubscriptionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		http.Fail(c, 400, "invalid request body", err)
		return
	}

	if req.UserId == "" {
		http.Fail(c, 400, "userId is required", nil)
		return
	}

	if req.PlanId == "" {
		http.Fail(c, 400, "planId is required", nil)
		return
	}

	// Fetch plan — first try DB, then fall back to static catalog.
	p := plan.New(db)
	if err := p.GetById(req.PlanId); err != nil {
		// Look up in static hanzoPlans by slug.
		var staticP *staticPlan
		for i := range hanzoPlans {
			if hanzoPlans[i].Slug == req.PlanId {
				staticP = &hanzoPlans[i]
				break
			}
		}
		if staticP == nil {
			http.Fail(c, 404, "plan not found", err)
			return
		}
		// Populate plan from static catalog and seed into DB.
		p.Slug = staticP.Slug
		p.Name = staticP.Name
		p.Description = staticP.Description
		p.Price = currency.Cents(staticP.Price)
		p.Currency = currency.Type(staticP.Currency)
		p.Interval = types.Interval(staticP.Interval)
		p.IntervalCount = staticP.IntervalCount
		p.TrialPeriodDays = staticP.TrialPeriodDays
		_ = p.Create() // best-effort; ignore dup-key errors
	}

	// C1-a: a PAID-tier subscription confers a spendable entitlement — its
	// included monthly allotment is MINTED onto the user's gateway balance — yet
	// this endpoint starts it Active instantly with ZERO payment
	// (ProviderType="internal", no CollectInvoice). An org-level admin must NOT be
	// able to self-mint a paid tier ("max" → $100/mo, recurring) by naming it
	// here; only a proven mint principal (internal service token / platform
	// SuperAdmin — e.g. cloud-api after a real payment) may create a paid-tier
	// subscription. A FREE tier (no price and no included allotment) stays
	// self-serve. Pairs with the payment-backed clamp in subscriptionPlanSlug.
	planSlug := p.Slug
	if planSlug == "" {
		planSlug = req.PlanId
	}
	if (p.Price > 0 || IncludedMonthlyCents(planSlug) > 0) && !middleware.MayMintMoney(c) {
		http.Fail(c, 403,
			"creating a paid-tier subscription requires platform-administrator or internal-service credentials", nil)
		return
	}

	// Create subscription
	sub := subscription.New(db)
	sub.UserId = req.UserId
	sub.DefaultPaymentMethod = req.DefaultPaymentMethod
	sub.ProviderType = "internal"
	sub.Quantity = 1

	if req.Metadata != nil {
		sub.Metadata = req.Metadata
	}

	// Initialize subscription lifecycle
	engine.StartSubscription(sub, p)

	if err := sub.Create(); err != nil {
		log.Error("Failed to create subscription: %v", err, c)
		http.Fail(c, 500, "failed to create subscription", err)
		return
	}

	// Bundle expansion. Plans declare their bundle list in the
	// canonical catalog (subscription.json:bundles) — e.g. "pro"
	// includes "world-pro", "team" includes "world-team", etc. Mint a
	// child zero-cost subscription for each bundled slug so the
	// downstream feature gate (world.hanzo.ai, chat, etc.) sees the
	// entitlement without the customer having to pay twice. The child
	// subscriptions reference the parent via Metadata.bundleParent so
	// cancellation can cascade them.
	bundled := bundledPlansForSlug(req.PlanId)
	for _, childSlug := range bundled {
		childPlan := plan.New(db)
		if err := childPlan.GetById(childSlug); err != nil {
			var staticChild *staticPlan
			for i := range hanzoPlans {
				if hanzoPlans[i].Slug == childSlug {
					staticChild = &hanzoPlans[i]
					break
				}
			}
			if staticChild == nil {
				log.Error("bundled plan %q not found for parent %q (skipping)", childSlug, req.PlanId, nil, c)
				continue
			}
			childPlan.Slug = staticChild.Slug
			childPlan.Name = staticChild.Name
			childPlan.Description = staticChild.Description
			childPlan.Currency = currency.Type(staticChild.Currency)
			childPlan.Interval = types.Interval(staticChild.Interval)
			childPlan.IntervalCount = staticChild.IntervalCount
			// Bundled price is forced to zero — the customer paid via
			// the parent, the child must never trigger a second charge.
			childPlan.Price = 0
			_ = childPlan.Create()
		}

		childSub := subscription.New(db)
		childSub.UserId = sub.UserId
		childSub.DefaultPaymentMethod = sub.DefaultPaymentMethod
		childSub.ProviderType = "bundle"
		childSub.Quantity = 1
		childMeta := map[string]interface{}{
			"bundleParent":     sub.Id(),
			"bundleParentPlan": req.PlanId,
		}
		for k, v := range req.Metadata {
			childMeta[k] = v
		}
		childSub.Metadata = childMeta
		engine.StartSubscription(childSub, childPlan)
		if err := childSub.Create(); err != nil {
			log.Error("Failed to create bundled subscription %q for parent %q: %v", childSlug, req.PlanId, err, c)
			continue
		}
	}

	c.JSON(201, subscriptionResponse(sub))
}

// bundledPlansForSlug returns the slugs of plans that ride along with
// a parent plan. Reads from the static catalog so we never need to hit
// the DB for plan metadata that is already baked into the binary.
func bundledPlansForSlug(slug string) []string {
	for i := range hanzoPlans {
		if hanzoPlans[i].Slug == slug {
			out := make([]string, len(hanzoPlans[i].Bundles))
			copy(out, hanzoPlans[i].Bundles)
			return out
		}
	}
	return nil
}

// ListBillingSubscriptions lists subscriptions for a user.
//
//	GET /v1/billing/subscriptions?userId=...
func ListBillingSubscriptions(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	rootKey := db.NewKey("synckey", "", 1, nil)
	subs := make([]*subscription.Subscription, 0)
	q := subscription.Query(db).Ancestor(rootKey)

	userId := strings.TrimSpace(c.Query("userId"))
	if userId != "" {
		q = q.Filter("UserId=", userId)
	}

	status := strings.TrimSpace(c.Query("status"))
	if status != "" {
		q = q.Filter("Status=", status)
	}

	if _, err := q.GetAll(&subs); err != nil {
		log.Error("Failed to list subscriptions: %v", err, c)
		http.Fail(c, 500, "failed to list subscriptions", err)
		return
	}

	items := make([]gin.H, 0, len(subs))
	for _, s := range subs {
		items = append(items, subscriptionResponse(s))
	}

	c.JSON(200, gin.H{
		"subscriptions": items,
		"count":         len(items),
	})
}

// GetBillingSubscription returns a single subscription.
//
//	GET /v1/billing/subscriptions/:id
func GetBillingSubscription(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	id := c.Param("id")
	sub := subscription.New(db)
	if err := sub.GetById(id); err != nil {
		http.Fail(c, 404, "subscription not found", err)
		return
	}

	c.JSON(200, subscriptionResponse(sub))
}

// UpdateBillingSubscription updates a subscription (plan change, quantity).
//
//	PATCH /v1/billing/subscriptions/:id
func UpdateBillingSubscription(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	id := c.Param("id")
	sub := subscription.New(db)
	if err := sub.GetById(id); err != nil {
		http.Fail(c, 404, "subscription not found", err)
		return
	}

	var req updateSubscriptionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		http.Fail(c, 400, "invalid request body", err)
		return
	}

	if req.PlanId != "" && req.PlanId != sub.PlanId {
		// Fetch new plan
		newPlan := plan.New(db)
		if err := newPlan.GetById(req.PlanId); err != nil {
			http.Fail(c, 404, "new plan not found", err)
			return
		}

		_, err := engine.ChangePlan(sub, newPlan, req.Prorate)
		if err != nil {
			http.Fail(c, 400, err.Error(), nil)
			return
		}
	}

	if req.Quantity > 0 {
		sub.Quantity = req.Quantity
	}

	if err := sub.Update(); err != nil {
		log.Error("Failed to update subscription: %v", err, c)
		http.Fail(c, 500, "failed to update subscription", err)
		return
	}

	c.JSON(200, subscriptionResponse(sub))
}

// CancelBillingSubscription cancels a subscription.
//
//	POST /v1/billing/subscriptions/:id/cancel
func CancelBillingSubscription(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	id := c.Param("id")
	sub := subscription.New(db)
	if err := sub.GetById(id); err != nil {
		http.Fail(c, 404, "subscription not found", err)
		return
	}

	var req cancelSubscriptionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		// Default to cancel at period end
		req.AtPeriodEnd = true
	}

	if err := engine.CancelSubscription(sub, req.AtPeriodEnd); err != nil {
		http.Fail(c, 400, err.Error(), nil)
		return
	}

	if err := sub.Update(); err != nil {
		log.Error("Failed to cancel subscription: %v", err, c)
		http.Fail(c, 500, "failed to cancel subscription", err)
		return
	}

	c.JSON(200, subscriptionResponse(sub))
}

// ReactivateBillingSubscription reactivates a canceled subscription.
//
//	POST /v1/billing/subscriptions/:id/reactivate
func ReactivateBillingSubscription(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	id := c.Param("id")
	sub := subscription.New(db)
	if err := sub.GetById(id); err != nil {
		http.Fail(c, 404, "subscription not found", err)
		return
	}

	if err := engine.ReactivateSubscription(sub); err != nil {
		http.Fail(c, 400, err.Error(), nil)
		return
	}

	if err := sub.Update(); err != nil {
		log.Error("Failed to reactivate subscription: %v", err, c)
		http.Fail(c, 500, "failed to reactivate subscription", err)
		return
	}

	c.JSON(200, subscriptionResponse(sub))
}

// RenewBillingSubscription manually triggers a billing cycle renewal.
// Normally this would be automated by Temporal, but this endpoint allows
// manual triggering for testing and for deployments without Temporal.
//
//	POST /v1/billing/subscriptions/:id/renew
func RenewBillingSubscription(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	id := c.Param("id")
	sub := subscription.New(db)
	if err := sub.GetById(id); err != nil {
		http.Fail(c, 404, "subscription not found", err)
		return
	}

	inv, result, err := engine.RenewSubscription(c.Request.Context(), db, sub, BurnCredits)
	if err != nil {
		log.Error("Failed to renew subscription: %v", err, c)
		http.Fail(c, 500, "failed to renew subscription", err)
		return
	}

	if err := sub.Update(); err != nil {
		log.Error("Failed to update subscription after renewal: %v", err, c)
		http.Fail(c, 500, "failed to update subscription", err)
		return
	}

	c.JSON(200, gin.H{
		"subscription": subscriptionResponse(sub),
		"invoice":      invoiceResponse(inv),
		"collection":   result,
	})
}

func subscriptionResponse(sub *subscription.Subscription) gin.H {
	resp := gin.H{
		"id":                   sub.Id(),
		"userId":               sub.UserId,
		"planId":               sub.PlanId,
		"status":               sub.Status,
		"quantity":             sub.Quantity,
		"currentPeriodStart":   sub.PeriodStart,
		"currentPeriodEnd":     sub.PeriodEnd,
		"cancelAtPeriodEnd":    sub.EndCancel,
		"providerType":         sub.ProviderType,
		"defaultPaymentMethod": sub.DefaultPaymentMethod,
		"plan": gin.H{
			"id":       sub.Plan.Id(),
			"name":     sub.Plan.Name,
			"price":    sub.Plan.Price,
			"currency": sub.Plan.Currency,
			"interval": sub.Plan.Interval,
		},
		"createdAt": sub.CreatedAt,
		"updatedAt": sub.UpdatedAt,
	}

	if !sub.TrialStart.IsZero() {
		resp["trialStart"] = sub.TrialStart
		resp["trialEnd"] = sub.TrialEnd
	}
	if !sub.CanceledAt.IsZero() {
		resp["canceledAt"] = sub.CanceledAt
	}
	if !sub.Ended.IsZero() {
		resp["endedAt"] = sub.Ended
	}

	return resp
}
