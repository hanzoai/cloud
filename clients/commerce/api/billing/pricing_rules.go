package billing

import (
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/middleware"
	"github.com/hanzoai/cloud/clients/commerce/models/pricingrule"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/util/json/http"
)

type createPricingRuleRequest struct {
	MeterId   string             `json:"meterId"`
	PlanId    string             `json:"planId"`
	Model     string             `json:"model"`
	Currency  string             `json:"currency"`
	UnitPrice int64              `json:"unitPrice"`
	Tiers     []pricingrule.Tier `json:"tiers"`
}

// CreatePricingRule creates a new pricing rule for a meter.
//
//	POST /v1/billing/pricing-rules
func CreatePricingRule(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	var req createPricingRuleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		http.Fail(c, 400, "invalid request body", err)
		return
	}

	if req.MeterId == "" {
		http.Fail(c, 400, "meterId is required", nil)
		return
	}

	model := pricingrule.PricingModel(strings.ToLower(req.Model))
	if model == "" {
		model = pricingrule.PerUnit
	}

	cur := currency.Type(strings.ToLower(req.Currency))
	if cur == "" {
		cur = "usd"
	}

	rule := pricingrule.New(db)
	rule.MeterId = req.MeterId
	rule.PlanId = req.PlanId
	rule.PricingType = model
	rule.Currency = cur
	rule.UnitPrice = req.UnitPrice
	rule.Tiers = req.Tiers

	if err := rule.Create(); err != nil {
		log.Error("Failed to create pricing rule: %v", err, c)
		http.Fail(c, 500, "failed to create pricing rule", err)
		return
	}

	c.JSON(201, gin.H{
		"id":        rule.Id(),
		"meterId":   rule.MeterId,
		"planId":    rule.PlanId,
		"model":     rule.PricingType,
		"currency":  rule.Currency,
		"unitPrice": rule.UnitPrice,
		"tiers":     rule.Tiers,
		"createdAt": rule.CreatedAt,
	})
}

// ListPricingRules lists pricing rules, optionally filtered by meter or plan.
//
//	GET /v1/billing/pricing-rules?meterId=...&planId=...
func ListPricingRules(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	rootKey := db.NewKey("synckey", "", 1, nil)
	rules := make([]*pricingrule.PricingRule, 0)
	q := pricingrule.Query(db).Ancestor(rootKey)

	meterId := strings.TrimSpace(c.Query("meterId"))
	if meterId != "" {
		q = q.Filter("MeterId=", meterId)
	}

	planId := strings.TrimSpace(c.Query("planId"))
	if planId != "" {
		q = q.Filter("PlanId=", planId)
	}

	if _, err := q.GetAll(&rules); err != nil {
		log.Error("Failed to list pricing rules: %v", err, c)
		http.Fail(c, 500, "failed to list pricing rules", err)
		return
	}

	items := make([]gin.H, 0, len(rules))
	for _, r := range rules {
		items = append(items, gin.H{
			"id":        r.Id(),
			"meterId":   r.MeterId,
			"planId":    r.PlanId,
			"currency":  r.Currency,
			"unitPrice": r.UnitPrice,
			"tiers":     r.Tiers,
			"createdAt": r.CreatedAt,
			"updatedAt": r.UpdatedAt,
		})
	}

	c.JSON(200, gin.H{
		"rules": items,
		"count": len(items),
	})
}

// DeletePricingRule removes a pricing rule by ID.
//
//	DELETE /v1/billing/pricing-rules/:id
func DeletePricingRule(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	id := c.Param("id")
	if id == "" {
		http.Fail(c, 400, "pricing rule id is required", nil)
		return
	}

	rule := pricingrule.New(db)
	if err := rule.GetById(id); err != nil {
		http.Fail(c, 404, "pricing rule not found", err)
		return
	}

	if err := rule.Delete(); err != nil {
		log.Error("Failed to delete pricing rule: %v", err, c)
		http.Fail(c, 500, "failed to delete pricing rule", err)
		return
	}

	c.JSON(200, gin.H{
		"id":      id,
		"deleted": true,
	})
}
