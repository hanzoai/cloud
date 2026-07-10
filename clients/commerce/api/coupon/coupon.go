package coupon

import (
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/middleware"
	"github.com/hanzoai/cloud/clients/commerce/models/coupon"
	"github.com/hanzoai/cloud/clients/commerce/models/creditgrant"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
	"github.com/hanzoai/cloud/clients/commerce/util/json/http"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/util/permission"
	"github.com/hanzoai/cloud/clients/commerce/util/rest"
	"github.com/hanzoai/cloud/clients/commerce/util/router"
)

func getCoupon(c *gin.Context) {
	couponid := c.Params.ByName("couponid")

	db := datastore.NewNamespaced(middleware.GetContext(c))
	cpn := coupon.New(db)

	if err := cpn.GetById(couponid); err != nil {
		http.Fail(c, 404, "Failed to get coupon", err)
		return
	}

	// if cpn.Dynamic {
	// 	http.Fail(c, 400, "Failed to get dynamic coupon", nil)
	// 	return
	// }

	// Check if coupon has been used
	cpn.Enabled = cpn.Redeemable()

	http.Render(c, 200, cpn)
}

func codeFromId(c *gin.Context) {
	couponid := c.Params.ByName("couponid")
	uniqueid := c.Params.ByName("uniqueid")

	db := datastore.NewNamespaced(middleware.GetContext(c))
	cpn := coupon.New(db)
	if err := cpn.GetById(couponid); err != nil {
		http.Fail(c, 404, "Failed to get coupon", err)
		return
	}

	cpn.Code_ = cpn.CodeFromId(uniqueid)

	log.Debug("%#v", cpn)

	// Check if coupon has been used
	cpn.Enabled = cpn.Redeemable()

	http.Render(c, 200, cpn)
}

func codeFromList(c *gin.Context) {
	couponid := c.Params.ByName("couponid")

	db := datastore.NewNamespaced(middleware.GetContext(c))
	cpn := coupon.New(db)
	if err := cpn.GetById(couponid); err != nil {
		http.Fail(c, 404, "Failed to get coupon %v", err)
		return
	}

	list := make([]string, 0)

	// Decode response body to create new order
	if err := json.Decode(c.Request.Body, list); err != nil {
		http.Fail(c, 400, "Failed decode request body", err)
		return
	}

	codes := make([]string, len(list))

	for _, id := range list {
		codes = append(codes, cpn.CodeFromId(id))
	}

	http.Render(c, 200, codes)
}

// couponReward describes a reward granted by coupon redemption.
type couponReward struct {
	Type        string `json:"type"`
	Description string `json:"description"`
	Value       int    `json:"value"`
	Duration    string `json:"duration,omitempty"`
	RedeemURL   string `json:"redeemUrl"`
}

// tryfreeRewards returns the static reward set for the TRYFREE coupon.
func tryfreeRewards() []couponReward {
	return []couponReward{
		{
			Type:        "bot-trial",
			Description: "$5/mo Hanzo Bot trial",
			Value:       500,
			Duration:    "1 month",
			RedeemURL:   "https://hanzo.bot",
		},
		{
			Type:        "compute-credits",
			Description: "$5 compute credits on Hanzo Console",
			Value:       500,
			RedeemURL:   "https://console.hanzo.ai",
		},
	}
}

func validateCoupon(c *gin.Context) {
	var req struct {
		Code string `json:"code"`
	}
	if err := json.Decode(c.Request.Body, &req); err != nil {
		http.Fail(c, 400, "Invalid request body", err)
		return
	}

	db := datastore.New(c)
	cpn := coupon.New(db)
	if ok, err := cpn.Query().Filter("Code_=", req.Code).Get(); err != nil || !ok {
		http.Render(c, 200, gin.H{"valid": false, "error": "Coupon not found"})
		return
	}

	if !cpn.Redeemable() {
		http.Render(c, 200, gin.H{"valid": false, "error": "Coupon expired or fully redeemed"})
		return
	}

	// This endpoint is PUBLIC (pre-sign-up promo check), so expose ONLY the
	// fields a checkout/pricing page needs to display and apply the discount.
	// Internal accounting/attribution — used, limit, campaignId, referrerId,
	// dynamic, enabled, start/end dates — is never leaked to anonymous callers.
	result := gin.H{
		"valid": true,
		"coupon": gin.H{
			"name":          cpn.Name,
			"type":          cpn.Type,
			"code":          cpn.Code_,
			"amount":        cpn.Amount,
			"filter":        cpn.Filter,
			"once":          cpn.Once,
			"productId":     cpn.ProductId,
			"freeProductId": cpn.FreeProductId,
			"freeVariantId": cpn.FreeVariantId,
			"freeQuantity":  cpn.FreeQuantity,
		},
	}

	if cpn.Code_ == "TRYFREE" {
		result["rewards"] = tryfreeRewards()
	}

	http.Render(c, 200, result)
}

func redeemCoupon(c *gin.Context) {
	var req struct {
		Code string `json:"code"`
	}
	if err := json.Decode(c.Request.Body, &req); err != nil {
		http.Fail(c, 400, "Invalid request body", err)
		return
	}

	db := datastore.New(c)
	cpn := coupon.New(db)
	if ok, err := cpn.Query().Filter("Code_=", req.Code).Get(); err != nil || !ok {
		http.Fail(c, 404, "Coupon not found", nil)
		return
	}

	if !cpn.Redeemable() {
		http.Fail(c, 400, "Coupon expired or fully redeemed", nil)
		return
	}

	// Get authenticated user ID
	userId, _ := c.Get("userId")
	uid, _ := userId.(string)
	if uid == "" {
		http.Fail(c, 401, "Authentication required", nil)
		return
	}

	// Check if user already redeemed this coupon (via credit grant tag)
	existing := make([]creditgrant.CreditGrant, 0)
	if _, err := creditgrant.Query(db).Filter("UserId=", uid).Filter("Tags=", "coupon:"+cpn.Code_).GetAll(&existing); err == nil && len(existing) > 0 {
		http.Fail(c, 400, "Coupon already redeemed", nil)
		return
	}

	rewards := make([]couponReward, 0)
	now := time.Now()

	if cpn.Code_ == "TRYFREE" {
		// Grant 1: $5 bot trial credit (eligible for bot-execution meter)
		botGrant := creditgrant.New(db)
		botGrant.UserId = uid
		botGrant.Name = "TRYFREE - Hanzo Bot Trial"
		botGrant.AmountCents = 500
		botGrant.RemainingCents = 500
		botGrant.Currency = "usd"
		botGrant.EffectiveAt = now
		botGrant.ExpiresAt = now.AddDate(0, 1, 0) // 1 month
		botGrant.Priority = 0
		botGrant.Eligibility = []string{"bot-execution"}
		botGrant.Tags = "promo,coupon:" + cpn.Code_
		botGrant.MustPut()

		// Grant 2: $5 compute credits (eligible for all compute meters)
		computeGrant := creditgrant.New(db)
		computeGrant.UserId = uid
		computeGrant.Name = "TRYFREE - Compute Credits"
		computeGrant.AmountCents = 500
		computeGrant.RemainingCents = 500
		computeGrant.Currency = "usd"
		computeGrant.EffectiveAt = now
		computeGrant.ExpiresAt = now.AddDate(0, 3, 0) // 3 months
		computeGrant.Priority = 0
		computeGrant.Eligibility = []string{"api-usage", "inference"}
		computeGrant.Tags = "promo,coupon:" + cpn.Code_
		computeGrant.MustPut()

		rewards = tryfreeRewards()
	} else {
		// Generic coupon: grant flat credit
		grant := creditgrant.New(db)
		grant.UserId = uid
		grant.Name = cpn.Name
		grant.AmountCents = int64(cpn.Amount)
		grant.RemainingCents = int64(cpn.Amount)
		grant.Currency = "usd"
		grant.EffectiveAt = now
		grant.ExpiresAt = now.AddDate(0, 1, 0)
		grant.Priority = 1
		grant.Tags = "promo,coupon:" + cpn.Code_
		grant.MustPut()

		rewards = append(rewards, couponReward{
			Type:        "credit",
			Description: cpn.Name,
			Value:       cpn.Amount,
		})
	}

	// Increment coupon usage
	cpn.Used++
	cpn.MustPut()

	log.Info("Coupon %s redeemed by user %s", cpn.Code_, uid)

	http.Render(c, 200, gin.H{
		"success": true,
		"rewards": rewards,
	})
}

func Route(router router.Router, args ...gin.HandlerFunc) {
	adminRequired := middleware.TokenRequired(permission.Admin)
	tokenRequired := middleware.TokenRequired(permission.User)
	namespaced := middleware.Namespace()

	api := rest.New(coupon.Coupon{})

	api.Get = getCoupon
	api.GET("/:couponid/code/:uniqueid", adminRequired, namespaced, codeFromId)
	api.POST("/:couponid/code", adminRequired, namespaced, codeFromList)
	// Public: the marketing pricing page (commerce.hanzo.ai/commerce) validates
	// promo codes before sign-up, so there is no user token yet. Validation is
	// read-only and never grants anything — /redeem below stays token-gated.
	api.POST("/validate", validateCoupon)
	api.POST("/redeem", tokenRequired, redeemCoupon)

	api.Route(router, args...)
}
