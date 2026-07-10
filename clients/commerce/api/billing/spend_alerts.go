package billing

import (
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/middleware"
	"github.com/hanzoai/cloud/clients/commerce/models/spendalert"
	"github.com/hanzoai/cloud/clients/commerce/util/json/http"
)

type createSpendAlertRequest struct {
	UserId    string `json:"userId"`
	Title     string `json:"title"`
	Threshold int64  `json:"threshold"`
	Currency  string `json:"currency"`
	// Scope + enforcement (issue #70).
	Project      string `json:"project"`
	Service      string `json:"service"`
	Enforce      bool   `json:"enforce"`
	SoftPct      int    `json:"softPct"`
	RateLimitRpm int    `json:"rateLimitRpm"`
}

// updateSpendAlertRequest uses pointers for every mutable field so an ABSENT
// field is preserved (partial update) rather than reset — a PATCH that only flips
// Enforce must not silently wipe the Threshold or rate limit.
type updateSpendAlertRequest struct {
	Title        *string `json:"title"`
	Threshold    *int64  `json:"threshold"`
	Project      *string `json:"project"`
	Service      *string `json:"service"`
	Enforce      *bool   `json:"enforce"`
	SoftPct      *int    `json:"softPct"`
	RateLimitRpm *int    `json:"rateLimitRpm"`
}

// maxScopeRowsPerOrg bounds how many spend-alert rows an org may hold. It caps
// both storage abuse and the cost of the per-request cap authorize scan (an
// attacker must not be able to inflate the row set to make the sum error / time
// out and thereby disable enforcement). loadOrgScopes reads at most this many.
const maxScopeRowsPerOrg = 200

// billingSubject is the ONE key spend caps are stored and looked up under, on BOTH
// the writer (this file) and the reader (the cloud gate / metering client). It is
// the ORG — the resolved organization name, which equals the validated owner claim
// (X-Org-Id). This is the EXACT key the gate uses (cloud middleware_billing.go
// identityFromCtx: `user := c.Org()`; ResourceMeter.Gate: `User: org`; the metering
// client sends `?user=<org>` + `X-Org-Id: <org>`; ScopeRules lists by `X-Org-Id`).
// Keying the WRITER on the org too makes a cap and its verdict/rate-rule resolve
// IDENTICALLY — the fix for the live bug where the writer stored under the IAM sub
// UUID while the reader looked up by org, so enforcement never bound.
//
// Caps are ORG-LEVEL policy (not per-user): any member of the org manages them,
// and the org is taken from the VALIDATED X-Org-Id (via TokenRequired), NEVER from
// a client ?user=/body field — so this is IDOR-safe and cannot cross tenants (a
// different org resolves to a different namespace AND a different subject). Empty
// only when no org is resolvable (unauthenticated).
func billingSubject(c *gin.Context) string {
	return strings.TrimSpace(middleware.GetOrganization(c).Name)
}

// ownsAlert reports whether the caller may mutate this row: its owner (the org)
// must equal the caller's validated org. Cross-org is blocked twice over — the
// datastore is org-namespaced (a foreign row is unreachable by id) AND this check
// re-confirms the org. Org comes from the validated X-Org-Id, never a client field.
func ownsAlert(c *gin.Context, a *spendalert.SpendAlert) bool {
	sub := billingSubject(c)
	return sub != "" && a.UserId == sub
}

// spendAlertView is the wire shape of one row plus the DERIVED period spend for
// its scope (periodSpentCents/over/warn) — never stored, always computed from the
// deduped usage ledger for the current UTC month.
func spendAlertView(db *datastore.Datastore, test bool, a *spendalert.SpendAlert) gin.H {
	soft := a.EffectiveSoftPct()
	view := gin.H{
		"id":           a.Id(),
		"userId":       a.UserId,
		"title":        a.Title,
		"threshold":    a.Threshold,
		"currency":     a.Currency,
		"project":      a.Project,
		"service":      a.Service,
		"enforce":      a.Enforce,
		"softPct":      soft,
		"rateLimitRpm": a.RateLimitRpm,
		"triggeredAt":  a.TriggeredAt,
		"createdAt":    a.CreatedAt,
		"updatedAt":    a.UpdatedAt,
	}
	spent, err := scopeSpentCents(db, test, a.Project, a.Service)
	if err != nil {
		// Display must not fail on an aggregation hiccup — report the policy row
		// without derived spend rather than 500 the whole list.
		return view
	}
	view["periodSpentCents"] = spent
	// `over` derives from the SAME boundary as enforce (scopeExhausted) so the flag
	// can never drift from the verdict: over ⇔ a further billable request is refused.
	view["over"] = scopeExhausted(spent, a.Threshold)
	view["warn"] = a.Threshold > 0 && spent >= a.Threshold*int64(soft)/100
	return view
}

// ListSpendAlerts returns the ORG's spend alerts (budgets/caps) plus derived
// period spend. It is the console Budgets read AND the source ScopeRules uses for
// the rate-limit config, so it MUST key on the same org the writer stored under.
//
//	GET /v1/billing/spend-alerts
func ListSpendAlerts(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))
	test := org.TestMode()

	sub := billingSubject(c)
	if sub == "" {
		c.JSON(200, []gin.H{}) // no resolvable org — nothing to read.
		return
	}
	rootKey := db.NewKey("synckey", "", 1, nil)
	q := spendalert.Query(db).Ancestor(rootKey).Limit(maxScopeRowsPerOrg).Filter("UserId=", sub)

	alerts := make([]*spendalert.SpendAlert, 0)
	if _, err := q.GetAll(&alerts); err != nil {
		log.Error("Failed to list spend alerts: %v", err, c)
		http.Fail(c, 500, "failed to list spend alerts", err)
		return
	}

	items := make([]gin.H, 0, len(alerts))
	for _, a := range alerts {
		items = append(items, spendAlertView(db, test, a))
	}

	c.JSON(200, items)
}

// CreateSpendAlert creates a spend alert / cap. UserId is optional (an org-wide
// or project/service scope cap has none). At least one limit must be meaningful:
// a Threshold>0 (spend cap) or a RateLimitRpm>0 (rate limit).
//
//	POST /v1/billing/spend-alerts
func CreateSpendAlert(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	var req createSpendAlertRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		http.Fail(c, 400, "invalid request body", err)
		return
	}

	// Key the cap on the ORG (billingSubject) — the SAME subject the gate reads by —
	// NEVER a client-supplied body userId or ?user=. This is the load-bearing fix:
	// a cap stored under the org is found by the gate's `?user=<org>` /
	// `X-Org-Id=<org>` lookup, so enforcement binds.
	subject := billingSubject(c)
	if subject == "" {
		http.Fail(c, 401, "authentication required", nil)
		return
	}
	req.UserId = subject

	// Bound the org's row set (storage + authorize-scan-cost abuse; keeps the cap
	// fail-closed path from being drowned by an attacker-inflated row list).
	rootKey := db.NewKey("synckey", "", 1, nil)
	if n, cerr := spendalert.Query(db).Ancestor(rootKey).Limit(maxScopeRowsPerOrg + 1).Count(); cerr == nil && n >= maxScopeRowsPerOrg {
		http.Fail(c, 400, "spend-alert limit reached for this organization", nil)
		return
	}

	if req.Threshold < 0 {
		http.Fail(c, 400, "threshold must be >= 0", nil)
		return
	}
	if req.RateLimitRpm < 0 {
		http.Fail(c, 400, "rateLimitRpm must be >= 0", nil)
		return
	}
	if req.Threshold <= 0 && req.RateLimitRpm <= 0 {
		http.Fail(c, 400, "a spend cap (threshold) or a rate limit (rateLimitRpm) is required", nil)
		return
	}
	if req.SoftPct < 0 || req.SoftPct > 100 {
		http.Fail(c, 400, "softPct must be between 0 and 100", nil)
		return
	}

	cur := strings.ToLower(strings.TrimSpace(req.Currency))
	if cur == "" {
		cur = "usd"
	}

	a := spendalert.New(db)
	a.UserId = req.UserId
	a.Title = req.Title
	a.Threshold = req.Threshold
	a.Currency = cur
	a.Project = spendalert.NormalizeProject(req.Project)
	a.Service = strings.TrimSpace(req.Service)
	a.Enforce = req.Enforce
	a.SoftPct = req.SoftPct
	a.RateLimitRpm = req.RateLimitRpm

	if err := a.Create(); err != nil {
		log.Error("Failed to create spend alert: %v", err, c)
		http.Fail(c, 500, "failed to create spend alert", err)
		return
	}

	c.JSON(201, spendAlertView(db, org.TestMode(), a))
}

// UpdateSpendAlert patches an existing spend alert / cap. Only the fields present
// in the body change; the rest are preserved.
//
//	PATCH /v1/billing/spend-alerts/:id
func UpdateSpendAlert(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	id := c.Param("id")
	a := spendalert.New(db)
	if err := a.GetById(id); err != nil {
		http.Fail(c, 404, "spend alert not found", err)
		return
	}
	// Per-row ownership: refuse (as 404, not leaking existence) a row the caller
	// does not own — closes the guess-the-id IDOR on this mutating verb.
	if !ownsAlert(c, a) {
		http.Fail(c, 404, "spend alert not found", nil)
		return
	}

	var req updateSpendAlertRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		http.Fail(c, 400, "invalid request body", err)
		return
	}

	if req.Title != nil {
		a.Title = *req.Title
	}
	if req.Threshold != nil {
		if *req.Threshold < 0 {
			http.Fail(c, 400, "threshold must be >= 0", nil)
			return
		}
		a.Threshold = *req.Threshold
	}
	if req.Project != nil {
		a.Project = spendalert.NormalizeProject(*req.Project)
	}
	if req.Service != nil {
		a.Service = strings.TrimSpace(*req.Service)
	}
	if req.Enforce != nil {
		a.Enforce = *req.Enforce
	}
	if req.SoftPct != nil {
		if *req.SoftPct < 0 || *req.SoftPct > 100 {
			http.Fail(c, 400, "softPct must be between 0 and 100", nil)
			return
		}
		a.SoftPct = *req.SoftPct
	}
	if req.RateLimitRpm != nil {
		if *req.RateLimitRpm < 0 {
			http.Fail(c, 400, "rateLimitRpm must be >= 0", nil)
			return
		}
		a.RateLimitRpm = *req.RateLimitRpm
	}

	if err := a.Update(); err != nil {
		log.Error("Failed to update spend alert: %v", err, c)
		http.Fail(c, 500, "failed to update spend alert", err)
		return
	}

	c.JSON(200, spendAlertView(db, org.TestMode(), a))
}

// DeleteSpendAlert deletes a spend alert by ID.
//
//	DELETE /v1/billing/spend-alerts/:id
func DeleteSpendAlert(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	id := c.Param("id")
	a := spendalert.New(db)
	if err := a.GetById(id); err != nil {
		http.Fail(c, 404, "spend alert not found", err)
		return
	}
	// Per-row ownership: refuse a row the caller does not own (guess-the-id IDOR).
	if !ownsAlert(c, a) {
		http.Fail(c, 404, "spend alert not found", nil)
		return
	}

	if err := a.Delete(); err != nil {
		log.Error("Failed to delete spend alert: %v", err, c)
		http.Fail(c, 500, "failed to delete spend alert", err)
		return
	}

	c.JSON(204, nil)
}
