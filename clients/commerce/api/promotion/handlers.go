package promotion

import (
	"context"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/middleware"
	"github.com/hanzoai/cloud/clients/commerce/models/applicationmethod"
	"github.com/hanzoai/cloud/clients/commerce/models/campaignbudget"
	promotionModel "github.com/hanzoai/cloud/clients/commerce/models/promotion"
	"github.com/hanzoai/cloud/clients/commerce/models/promotionrule"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
	jsonhttp "github.com/hanzoai/cloud/clients/commerce/util/json/http"
	"github.com/hanzoai/cloud/clients/commerce/util/rest"
	"github.com/hanzoai/cloud/clients/commerce/util/router"
)

func Route(router router.Router, args ...gin.HandlerFunc) {
	namespaced := middleware.Namespace()

	rest.New(promotionModel.Promotion{}).Route(router, args...)
	rest.New(applicationmethod.ApplicationMethod{}).Route(router, args...)
	rest.New(promotionrule.PromotionRule{}).Route(router, args...)
	rest.New(campaignbudget.CampaignBudget{}).Route(router, args...)

	promoApi := rest.New("/promotion")
	promoApi.POST("/evaluate", append(args, namespaced, Evaluate)...)
	promoApi.Route(router, args...)
}

type evalItem struct {
	ProductId string `json:"productId"`
	VariantId string `json:"variantId"`
	Quantity  int    `json:"quantity"`
	Amount    int64  `json:"amount"`
}

type evalRequest struct {
	Items           []evalItem `json:"items"`
	CurrencyCode    string     `json:"currencyCode"`
	RegionId        string     `json:"regionId,omitempty"`
	CustomerGroupId string     `json:"customerGroupId,omitempty"`
	CartTotal       int64      `json:"cartTotal"`
}

type adjustment struct {
	PromotionId string `json:"promotionId"`
	Code        string `json:"code"`
	Amount      int64  `json:"amount"`
	Type        string `json:"type"`
}

type evalResponse struct {
	Adjustments   []adjustment `json:"adjustments"`
	TotalDiscount int64        `json:"totalDiscount"`
}

// Evaluate finds applicable promotions for a given cart/order context.
//
// It queries all active automatic promotions, checks date ranges and
// currency constraints, then calculates discount adjustments using the
// associated ApplicationMethod.
func Evaluate(c *gin.Context) {
	var req evalRequest
	if err := json.Decode(c.Request.Body, &req); err != nil {
		jsonhttp.Fail(c, 400, "Invalid request body", err)
		return
	}

	ctx := middleware.GetContext(c)
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	db := datastore.New(ctx)

	now := time.Now()
	var promotions []*promotionModel.Promotion
	q := promotionModel.Query(db).Filter("Status=", "active").Filter("IsAutomatic=", true)
	if _, err := q.GetAll(&promotions); err != nil {
		jsonhttp.Fail(c, 500, "Failed to query promotions", err)
		return
	}

	adjustments := make([]adjustment, 0)
	totalDiscount := int64(0)

	for _, promo := range promotions {
		if promo.StartsAt != nil && promo.StartsAt.After(now) {
			continue
		}
		if promo.EndsAt != nil && promo.EndsAt.Before(now) {
			continue
		}

		am := applicationmethod.New(db)
		ok, err := am.Query().Filter("PromotionId=", promo.Id()).Get()
		if err != nil || !ok {
			continue
		}

		if am.CurrencyCode != "" && am.CurrencyCode != req.CurrencyCode {
			continue
		}

		var discountAmount int64
		switch am.Type {
		case "percentage":
			if am.TargetType == "order" {
					// Value is in basis points: 1500 = 15.00%
				discountAmount = req.CartTotal * int64(am.Value) / 10000
			}
		case "fixed":
			discountAmount = int64(am.Value)
		}

		if discountAmount > 0 {
			adjustments = append(adjustments, adjustment{
				PromotionId: promo.Id(),
				Code:        promo.Code,
				Amount:      discountAmount,
				Type:        am.Type,
			})
			totalDiscount += discountAmount
		}
	}

	jsonhttp.Render(c, 200, evalResponse{
		Adjustments:   adjustments,
		TotalDiscount: totalDiscount,
	})
}
