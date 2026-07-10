package billing

import (
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/middleware"
	"github.com/hanzoai/cloud/clients/commerce/models/creditgrant"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/util/json/http"
)

type createCreditGrantRequest struct {
	UserId      string   `json:"userId"`
	Name        string   `json:"name"`
	AmountCents int64    `json:"amountCents"`
	Currency    string   `json:"currency"`
	ExpiresIn   string   `json:"expiresIn"` // Go duration string, e.g. "720h"
	Priority    int      `json:"priority"`
	Eligibility []string `json:"eligibility"`
	Tags        string   `json:"tags"`
}

// CreateCreditGrant creates a new credit grant for a user.
//
//	POST /v1/billing/credit-grants
func CreateCreditGrant(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	var req createCreditGrantRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		http.Fail(c, 400, "invalid request body", err)
		return
	}

	if req.UserId == "" {
		http.Fail(c, 400, "userId is required", nil)
		return
	}

	if req.AmountCents <= 0 {
		http.Fail(c, 400, "amountCents must be positive", nil)
		return
	}

	cur := currency.Type(strings.ToLower(req.Currency))
	if cur == "" {
		cur = "usd"
	}

	grant := creditgrant.New(db)
	grant.UserId = req.UserId
	grant.Name = req.Name
	grant.AmountCents = req.AmountCents
	grant.RemainingCents = req.AmountCents
	grant.Currency = cur
	grant.Priority = req.Priority
	grant.Eligibility = req.Eligibility
	grant.Tags = req.Tags

	if req.ExpiresIn != "" {
		dur, err := time.ParseDuration(req.ExpiresIn)
		if err != nil {
			http.Fail(c, 400, "invalid expiresIn duration", err)
			return
		}
		grant.ExpiresAt = time.Now().Add(dur)
	}

	if err := grant.Create(); err != nil {
		log.Error("Failed to create credit grant: %v", err, c)
		http.Fail(c, 500, "failed to create credit grant", err)
		return
	}

	resp := gin.H{
		"id":             grant.Id(),
		"userId":         grant.UserId,
		"name":           grant.Name,
		"amountCents":    grant.AmountCents,
		"remainingCents": grant.RemainingCents,
		"currency":       grant.Currency,
		"priority":       grant.Priority,
		"effectiveAt":    grant.EffectiveAt,
		"tags":           grant.Tags,
		"createdAt":      grant.CreatedAt,
	}
	if !grant.ExpiresAt.IsZero() {
		resp["expiresAt"] = grant.ExpiresAt
	}

	c.JSON(201, resp)
}

// ListCreditGrants lists credit grants for a user.
//
//	GET /v1/billing/credit-grants?userId=...
func ListCreditGrants(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	userId := strings.TrimSpace(c.Query("userId"))
	if userId == "" {
		http.Fail(c, 400, "userId query parameter is required", nil)
		return
	}

	rootKey := db.NewKey("synckey", "", 1, nil)
	grants := make([]*creditgrant.CreditGrant, 0)
	q := creditgrant.Query(db).Ancestor(rootKey).
		Filter("UserId=", userId)

	if _, err := q.GetAll(&grants); err != nil {
		log.Error("Failed to list credit grants: %v", err, c)
		http.Fail(c, 500, "failed to list credit grants", err)
		return
	}

	items := make([]gin.H, 0, len(grants))
	for _, g := range grants {
		item := gin.H{
			"id":             g.Id(),
			"userId":         g.UserId,
			"name":           g.Name,
			"amountCents":    g.AmountCents,
			"remainingCents": g.RemainingCents,
			"currency":       g.Currency,
			"priority":       g.Priority,
			"effectiveAt":    g.EffectiveAt,
			"tags":           g.Tags,
			"voided":         g.Voided,
			"active":         g.IsActive(),
			"createdAt":      g.CreatedAt,
		}
		if !g.ExpiresAt.IsZero() {
			item["expiresAt"] = g.ExpiresAt
		}
		items = append(items, item)
	}

	c.JSON(200, gin.H{
		"grants": items,
		"count":  len(items),
	})
}

// GetCreditBalance returns the total available credit balance for a user.
//
//	GET /v1/billing/credit-balance?userId=...
func GetCreditBalance(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	userId := strings.TrimSpace(c.Query("userId"))
	if userId == "" {
		http.Fail(c, 400, "userId query parameter is required", nil)
		return
	}

	grants, err := getActiveGrants(db, userId)
	if err != nil {
		log.Error("Failed to query credit grants: %v", err, c)
		http.Fail(c, 500, "failed to query credit balance", err)
		return
	}

	// Sum by currency
	balances := make(map[currency.Type]int64)
	for _, g := range grants {
		balances[g.Currency] += g.RemainingCents
	}

	items := make([]gin.H, 0, len(balances))
	for cur, amount := range balances {
		items = append(items, gin.H{
			"currency":  cur,
			"available": amount,
		})
	}

	c.JSON(200, gin.H{
		"userId":   userId,
		"balances": items,
	})
}

// GetCreditBalanceBreakdown returns the credit balance grouped by tag.
// Used by Chat to distinguish trial vs paid credits.
//
//	GET /v1/billing/credit-balance/breakdown?userId=...
func GetCreditBalanceBreakdown(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	userId := strings.TrimSpace(c.Query("userId"))
	if userId == "" {
		http.Fail(c, 400, "userId query parameter is required", nil)
		return
	}

	grants, err := getActiveGrants(db, userId)
	if err != nil {
		log.Error("Failed to query credit grants for breakdown: %v", err, c)
		http.Fail(c, 500, "failed to query credit balance", err)
		return
	}

	type tagBalance struct {
		Cents     int64      `json:"cents"`
		ExpiresAt *time.Time `json:"expiresAt,omitempty"`
	}

	breakdown := make(map[string]*tagBalance)
	var totalCents int64

	for _, g := range grants {
		tag := g.Tags
		if tag == "" {
			tag = "other"
		}

		tb, ok := breakdown[tag]
		if !ok {
			tb = &tagBalance{}
			breakdown[tag] = tb
		}
		tb.Cents += g.RemainingCents
		totalCents += g.RemainingCents

		// Track the earliest expiry for this tag group
		if !g.ExpiresAt.IsZero() {
			if tb.ExpiresAt == nil || g.ExpiresAt.Before(*tb.ExpiresAt) {
				exp := g.ExpiresAt
				tb.ExpiresAt = &exp
			}
		}
	}

	c.JSON(200, gin.H{
		"userId":    userId,
		"breakdown": breakdown,
		"total":     gin.H{"cents": totalCents},
	})
}

// VoidCreditGrant voids a specific credit grant, making it unusable.
//
//	POST /v1/billing/credit-grants/:id/void
func VoidCreditGrant(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	id := c.Param("id")
	if id == "" {
		http.Fail(c, 400, "grant id is required", nil)
		return
	}

	grant := creditgrant.New(db)
	if err := grant.GetById(id); err != nil {
		http.Fail(c, 404, "credit grant not found", err)
		return
	}

	if grant.Voided {
		http.Fail(c, 400, "grant is already voided", nil)
		return
	}

	grant.Voided = true
	if err := grant.Update(); err != nil {
		log.Error("Failed to void credit grant: %v", err, c)
		http.Fail(c, 500, "failed to void credit grant", err)
		return
	}

	c.JSON(200, gin.H{
		"id":     grant.Id(),
		"voided": true,
	})
}

// getActiveGrants returns active, non-expired, non-voided grants for a user,
// sorted by priority ASC then ExpiresAt ASC.
func getActiveGrants(db *datastore.Datastore, userId string) ([]*creditgrant.CreditGrant, error) {
	rootKey := db.NewKey("synckey", "", 1, nil)
	grants := make([]*creditgrant.CreditGrant, 0)
	q := creditgrant.Query(db).Ancestor(rootKey).
		Filter("UserId=", userId).
		Filter("Voided=", false)

	keys, err := q.GetAll(&grants)
	if err != nil {
		return nil, err
	}

	// Reinitialize each loaded grant so it can be updated later.
	// Raw GetAll doesn't set b.ds / b.Model.db — without Init+SetKey,
	// calling Update() will panic (m.db == nil when rebuilding the key).
	for i, g := range grants {
		g.Init(db)
		if i < len(keys) {
			g.SetKey(keys[i])
		}
	}

	// Filter to active grants only
	active := make([]*creditgrant.CreditGrant, 0, len(grants))
	for _, g := range grants {
		if g.IsActive() {
			active = append(active, g)
		}
	}

	// Sort: priority ASC, then ExpiresAt ASC (zero = last)
	sort.Slice(active, func(i, j int) bool {
		if active[i].Priority != active[j].Priority {
			return active[i].Priority < active[j].Priority
		}
		// Within same priority, burn expiring grants first
		if active[i].ExpiresAt.IsZero() {
			return false
		}
		if active[j].ExpiresAt.IsZero() {
			return true
		}
		return active[i].ExpiresAt.Before(active[j].ExpiresAt)
	})

	return active, nil
}

// BurnCredits applies the credit burn-down algorithm: deducts amount from
// active grants in priority order. Returns the remaining amount (overage)
// and the grants that were modified.
func BurnCredits(db *datastore.Datastore, userId string, amount int64, meterId string) (int64, error) {
	grants, err := getActiveGrants(db, userId)
	if err != nil {
		return amount, err
	}

	remaining := amount

	for _, g := range grants {
		if remaining <= 0 {
			break
		}

		// Check meter eligibility
		if meterId != "" && !g.IsEligibleForMeter(meterId) {
			continue
		}

		deduct := g.RemainingCents
		if deduct > remaining {
			deduct = remaining
		}

		g.RemainingCents -= deduct
		remaining -= deduct

		if err := g.Update(); err != nil {
			return remaining, err
		}
	}

	return remaining, nil
}
