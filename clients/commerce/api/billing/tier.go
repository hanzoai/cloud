package billing

import (
	"context"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/billing/tier"
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/middleware"
	"github.com/hanzoai/cloud/clients/commerce/middleware/iammiddleware"
	"github.com/hanzoai/cloud/clients/commerce/models/transaction"
	txutil "github.com/hanzoai/cloud/clients/commerce/models/transaction/util"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/util/json/http"
)

// GetTier returns the billing tier, limits, and effective balance for a user.
//
// For IAM-authenticated requests the tier is read from the JWT claim.
// For service-to-service calls the tier may be passed as a query parameter.
//
//	GET /v1/billing/tier?user=hanzo/alice
//
// Response includes the tier config plus the effective available balance.
// There is no free tier: a zero-balance account has effectiveAvailable == 0
// and is gated. The daily-credit term is 0 for every tier (see billing/tier);
// onboarding funds an account once via the starter-credit grant, and once
// that is spent the account is gated until it is topped up.
func GetTier(c *gin.Context) {
	org := middleware.GetOrganization(c)
	ctx := org.Namespaced(c)

	user := strings.ToLower(strings.TrimSpace(c.Query("user")))
	if user == "" {
		http.Fail(c, 400, "user query parameter is required", nil)
		return
	}

	// Resolve tier: prefer IAM claim, fall back to query param, default to free.
	tierName := tier.Free
	if iamTier := iammiddleware.GetIAMTier(c); iamTier != "" {
		tierName = tier.Parse(iamTier)
	} else if qTier := c.Query("tier"); qTier != "" {
		tierName = tier.Parse(qTier)
	}

	cfg := tier.Get(tierName)

	// Fetch the user's prepaid balance.
	cur := currency.Type("usd")
	datas, err := txutil.GetTransactionsByCurrency(ctx, user, "iam-user", cur, org.TestMode())
	if err != nil {
		http.Fail(c, 500, "failed to query balance", err)
		return
	}

	var prepaidBalance, holds currency.Cents
	if data, ok := datas.Data[cur]; ok {
		prepaidBalance = data.Balance
		holds = data.Holds
	}

	prepaidAvailable := prepaidBalance - holds
	if prepaidAvailable < 0 {
		prepaidAvailable = 0
	}

	// Daily replenishing credit. This is 0 for every tier (there is no free
	// tier), so HasDailyCredits() is false and dailyRemaining stays 0 —
	// effectiveAvailable collapses to prepaidAvailable and a zero-balance
	// account is gated. The mechanism is retained (guarded by DailyCreditsCents
	// > 0) so a tier could re-enable a daily allowance by configuration alone.
	var dailyRemaining int64
	if cfg.HasDailyCredits() {
		dailyUsed := dailyUsageCents(ctx, user, org.TestMode())
		dailyRemaining = cfg.DailyCreditsCents - dailyUsed
		if dailyRemaining < 0 {
			dailyRemaining = 0
		}
	}

	effectiveAvailable := int64(prepaidAvailable) + dailyRemaining

	c.JSON(200, gin.H{
		"user": user,
		"tier": gin.H{
			"name":              cfg.Name,
			"displayName":       cfg.DisplayName,
			"maxAgents":         cfg.MaxAgents,
			"unlimitedAgents":   cfg.IsUnlimitedAgents(),
			"dailyCreditsCents": cfg.DailyCreditsCents,
			"allowedModels":     cfg.AllowedModels,
		},
		"balance": gin.H{
			"currency":           cur,
			"prepaidAvailable":   prepaidAvailable,
			"dailyRemaining":     dailyRemaining,
			"effectiveAvailable": effectiveAvailable,
		},
	})
}

// TierCheck is a lightweight endpoint for model-access gating.
// It returns the tier config and whether a specific model is allowed,
// without computing the full balance. Used by Chat and white-label services.
//
//	GET /v1/billing/tier-check?user=hanzo/alice&model=zen4-max
func TierCheck(c *gin.Context) {
	user := strings.ToLower(strings.TrimSpace(c.Query("user")))
	if user == "" {
		http.Fail(c, 400, "user query parameter is required", nil)
		return
	}

	model := strings.TrimSpace(c.Query("model"))

	// Resolve tier: prefer IAM claim, fall back to query param, default to free.
	tierName := tier.Free
	if iamTier := iammiddleware.GetIAMTier(c); iamTier != "" {
		tierName = tier.Parse(iamTier)
	} else if qTier := c.Query("tier"); qTier != "" {
		tierName = tier.Parse(qTier)
	}

	cfg := tier.Get(tierName)

	resp := gin.H{
		"user": user,
		"tier": gin.H{
			"name":          cfg.Name,
			"displayName":   cfg.DisplayName,
			"allowedModels": cfg.AllowedModels,
			"maxAgents":     cfg.MaxAgents,
		},
	}

	if model != "" {
		resp["model"] = model
		resp["allowed"] = cfg.IsModelAllowed(model)
	}

	c.JSON(200, resp)
}

// dailyUsageCents sums the api-usage withdrawals for a user since
// midnight UTC today. This determines how much of the free-tier daily
// credit has been consumed.
func dailyUsageCents(ctx context.Context, user string, isTest bool) int64 {
	db := datastore.New(ctx)
	rootKey := db.NewKey("synckey", "", 1, nil)

	now := time.Now().UTC()
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)

	transs := make([]*transaction.Transaction, 0)
	q := transaction.Query(db).Ancestor(rootKey).
		Filter("Test=", isTest).
		Filter("SourceKind=", "iam-user").
		Filter("SourceId=", user).
		Filter("Tags=", "api-usage")

	if _, err := q.GetAll(&transs); err != nil {
		return 0
	}

	var total int64
	for _, t := range transs {
		if !t.CreatedAt.Before(todayStart) {
			total += int64(t.Amount)
		}
	}

	return total
}
