package billing

import (
	"context"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/billing/allotment"
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/middleware"
	"github.com/hanzoai/cloud/clients/commerce/models/subscription"
	"github.com/hanzoai/cloud/clients/commerce/models/transaction"
	txutil "github.com/hanzoai/cloud/clients/commerce/models/transaction/util"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/util/json/http"
)

// subscriptionPlanSlug returns the plan slug of `user`'s newest active/trialing
// subscription, or "" when none. This is the user's REAL, un-spoofable
// entitlement — the SOLE authority for how much included allotment may be minted
// on their behalf. It never trusts a client-supplied plan.
//
// Subscriptions are registered ancestor-less (orm.Register without WithParent)
// and keyed by UserId, so they are queried the SAME way
// billing/trial.findTrialSubscription queries them — a bare UserId filter, NOT
// under the synckey ancestor. The prior Ancestor(synckey) filter (inherited by
// the old resolvePlanSlug) matched NOTHING, which would make the grant clamp
// reject even a legitimate self-service grant for the user's actual plan.
func subscriptionPlanSlug(db *datastore.Datastore, user string) string {
	subs := make([]*subscription.Subscription, 0)
	q := subscription.Query(db).Filter("UserId=", user)
	if _, err := q.GetAll(&subs); err != nil {
		return ""
	}

	var best *subscription.Subscription
	for _, s := range subs {
		switch s.Status {
		case subscription.Active, subscription.Trialing:
		default:
			continue
		}
		// C1-a: a PAID tier's included allotment may anchor ONLY on a
		// payment-backed subscription — never a zero-payment self-created internal
		// Active sub (CreateBillingSubscription starts one instantly). A free tier
		// (no included allotment) confers nothing to mint, so it is fine self-serve.
		slug := s.Plan.Slug
		if slug == "" {
			slug = s.PlanId
		}
		if IncludedMonthlyCents(slug) > 0 && !subscriptionPaymentBacked(s) {
			continue
		}
		if best == nil || s.PeriodStart.After(best.PeriodStart) {
			best = s
		}
	}
	if best == nil {
		return ""
	}
	if best.Plan.Slug != "" {
		return best.Plan.Slug
	}
	return best.PlanId
}

// subscriptionPaymentBacked reports whether a subscription represents a REAL paid
// relationship — one managed by an external payment provider (Stripe/Square/…),
// or one that has progressed through invoicing (a linked invoice) — as opposed to
// the zero-payment internal subscription CreateBillingSubscription starts Active
// instantly. It is the entitlement anchor for a PAID tier's minted allotment: a
// forged internal Active sub (ProviderType="internal", no collected invoice) is
// NOT payment-backed, so its higher-tier allotment can never be minted (C1-a).
func subscriptionPaymentBacked(s *subscription.Subscription) bool {
	switch strings.ToLower(strings.TrimSpace(s.ProviderType)) {
	case "stripe", "square", "paypal", "authorizenet", "authorize.net", "braintree":
		return true // a real external payment provider manages billing
	}
	// internal / bundle / unset: backed only once it has actually been invoiced.
	return strings.TrimSpace(s.CurrentInvoiceId) != ""
}

// resolvePlanSlug is the READ-side plan resolver (usage rollup): an explicit
// query param wins as a harmless projection preview, else the user's real
// subscription plan. It NEVER gates money — the mint path (GrantAllotment) uses
// planForGrant, which does not honor an unprivileged caller's plan override.
func resolvePlanSlug(db *datastore.Datastore, user, explicit string) string {
	if explicit = strings.TrimSpace(explicit); explicit != "" {
		return explicit
	}
	return subscriptionPlanSlug(db, user)
}

// planForGrant resolves the plan whose included allotment `user` may be GRANTED.
// The cents an allotment mints (IncludedMonthlyCents) is a pure function of the
// plan, so the plan itself is the money lever — an org-level admin must NOT be
// able to name a higher tier ("max") and mint its $100/mo for free. Therefore:
//
//   - a client-supplied override is honored ONLY for a privileged MINT caller
//     (the internal service token or a platform SuperAdmin — the SAME
//     principals PlatformOnly admits and the ONLY ones allowed to mint an
//     arbitrary amount via /deposit), for legitimate comps/backfills; and
//   - for EVERYONE else it is verified against the real subscription and IGNORED
//     on mismatch — the grant clamps to the user's actually-paid entitlement
//     (subscriptionPlanSlug), which yields 0 cents when there is no subscription.
//
// This makes /allotment/grant self-service-safe (an org grants its OWN allotment,
// but only ever its OWN paid plan's amount) without a blanket platform-only gate.
func planForGrant(c *gin.Context, db *datastore.Datastore, user, explicit string) string {
	sub := subscriptionPlanSlug(db, user)
	explicit = strings.TrimSpace(explicit)
	if explicit == "" {
		return sub
	}
	if middleware.MayMintMoney(c) {
		return explicit
	}
	if sub != "" && strings.EqualFold(explicit, sub) {
		return explicit
	}
	return sub
}

type grantAllotmentRequest struct {
	User string `json:"user"`
	Plan string `json:"plan"` // optional override; else resolved from subscription
}

// GrantAllotment grants the calling/target user's plan-included monthly usage
// credit for the current UTC month, idempotently.
//
//	POST /v1/billing/allotment/grant   { "user": "hanzo/alice", "plan": "pro" }
//
// The credit lands as an expiring balance deposit, so the gateway prepaid
// balance gate (available > 0) passes while the tenant is within allotment and
// fails closed once both the included credit and any purchased balance are
// exhausted. Admin token required.
func GrantAllotment(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	var req grantAllotmentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		http.Fail(c, 400, "invalid request body", err)
		return
	}
	req.User = strings.ToLower(strings.TrimSpace(req.User))
	if req.User == "" {
		http.Fail(c, 400, "user is required", nil)
		return
	}

	// MINT path: clamp the plan to the caller's REAL subscription unless the
	// caller is a privileged mint principal. An org-level admin cannot inflate
	// their allotment by naming a higher tier (was: resolvePlanSlug trusted
	// req.Plan verbatim → org-admin grants "max" $100/mo free — C1 miss).
	plan := planForGrant(c, db, req.User, req.Plan)
	cents := IncludedMonthlyCents(plan)

	res, err := allotment.Grant(db, req.User, plan, cents, time.Now(), org.TestMode())
	if err != nil {
		log.Error("Failed to grant monthly allotment for %s: %v", req.User, err, c)
		http.Fail(c, 500, "failed to grant monthly allotment", err)
		return
	}

	status := 200
	if res.Granted {
		status = 201
	}
	c.JSON(status, gin.H{
		"user":          req.User,
		"plan":          plan,
		"currency":      "usd",
		"granted":       res.Granted,
		"reason":        res.Reason,
		"amountCents":   res.AmountCents,
		"period":        res.Period,
		"transactionId": res.TransactionId,
	})
}

// grantOrgAllotments grants the monthly included allotment to every user with
// an active/trialing subscription in the given org datastore, for the UTC month
// containing `now`. Idempotent per (user, period). Returns a result per user.
// Shared by the standalone allotment-run endpoint and the billing cycle.
func grantOrgAllotments(c *gin.Context, db *datastore.Datastore, now time.Time, live bool) (granted, skipped int, results []gin.H) {
	rootKey := db.NewKey("synckey", "", 1, nil)

	subs := make([]*subscription.Subscription, 0)
	if _, err := subscription.Query(db).Ancestor(rootKey).GetAll(&subs); err != nil {
		log.Error("Failed to list subscriptions for allotment run: %v", err, c)
		return 0, 0, nil
	}

	// Collapse to one plan per user (newest active/trialing wins), so each
	// user is granted exactly once per run regardless of subscription count.
	planByUser := make(map[string]string)
	startByUser := make(map[string]time.Time)
	for _, s := range subs {
		switch s.Status {
		case subscription.Active, subscription.Trialing:
		default:
			continue
		}
		if s.UserId == "" {
			continue
		}
		slug := s.Plan.Slug
		if slug == "" {
			slug = s.PlanId
		}
		if prev, ok := startByUser[s.UserId]; !ok || s.PeriodStart.After(prev) {
			planByUser[s.UserId] = slug
			startByUser[s.UserId] = s.PeriodStart
		}
	}

	results = make([]gin.H, 0, len(planByUser))
	for user, plan := range planByUser {
		cents := IncludedMonthlyCents(plan)
		res, err := allotment.Grant(db, user, plan, cents, now, !live)
		if err != nil {
			log.Error("allotment run: grant failed for %s: %v", user, err, c)
			results = append(results, gin.H{"user": user, "plan": plan, "granted": false, "error": err.Error()})
			continue
		}
		if res.Granted {
			granted++
		} else {
			skipped++
		}
		results = append(results, gin.H{
			"user":        user,
			"plan":        plan,
			"granted":     res.Granted,
			"reason":      res.Reason,
			"amountCents": res.AmountCents,
		})
	}
	return granted, skipped, results
}

// RunAllotments grants the monthly included allotment to every user with an
// active/trialing subscription in the request's organization, for the current
// UTC month. Idempotent per (user, period). Intended for the platform
// scheduler to invoke at period start (alongside the billing cycle).
//
//	POST /v1/billing/allotment/run
func RunAllotments(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	now := time.Now()
	// live = !TestMode: allotment grants land in the SAME bucket as charges/usage.
	granted, skipped, results := grantOrgAllotments(c, db, now, !org.TestMode())

	c.JSON(200, gin.H{
		"period":  allotment.Period(now),
		"granted": granted,
		"skipped": skipped,
		"results": results,
	})
}

// GetUsageRollup returns the unified plan + included-usage + consumed + overage
// + balance view for a user, for the current UTC month. This is the single
// read surface the console billing UI renders. All figures are derived from the
// same transactions the gateway's balance gate reads — no separate store.
//
//	GET /v1/billing/usage-rollup?user=hanzo/alice&plan=pro
//
// `plan` is optional; when omitted it is resolved from the user's subscription.
func GetUsageRollup(c *gin.Context) {
	org := middleware.GetOrganization(c)
	ctx := org.Namespaced(c)
	db := datastore.New(ctx)

	user := strings.ToLower(strings.TrimSpace(c.Query("user")))
	if user == "" {
		http.Fail(c, 400, "user query parameter is required", nil)
		return
	}

	plan := resolvePlanSlug(db, user, c.Query("plan"))
	now := time.Now()

	// Catalog-declared included allotment, and what is actually granted on the
	// balance this month (they match once the grant has run; before the first
	// grant the granted amount is 0 while the catalog amount shows the plan's
	// entitlement).
	includedMonthlyCents := IncludedMonthlyCents(plan)
	includedGrantedCents := allotment.GrantedCents(db, user, now, org.TestMode())

	// Consumption this UTC month (api-usage withdrawals).
	consumedCents := monthlyUsageCents(ctx, user, org.TestMode())

	includedRemaining := includedGrantedCents - consumedCents
	if includedRemaining < 0 {
		includedRemaining = 0
	}
	overageCents := consumedCents - includedGrantedCents
	if overageCents < 0 {
		overageCents = 0
	}

	// Balance exactly as the gateway gate computes it.
	var balance, holds currency.Cents
	datas, err := txutil.GetTransactionsByCurrency(ctx, user, "iam-user", currency.USD, org.TestMode())
	if err != nil {
		http.Fail(c, 500, "failed to query balance", err)
		return
	}
	if data, ok := datas.Data[currency.USD]; ok {
		balance = data.Balance
		holds = data.Holds
	}
	available := balance - holds
	if available < 0 {
		available = 0
	}

	c.JSON(200, gin.H{
		"user":     user,
		"plan":     plan,
		"currency": "usd",
		"period":   allotment.Period(now),
		"included": gin.H{
			"monthlyCents": includedMonthlyCents, // catalog entitlement
			"grantedCents": includedGrantedCents, // actually on balance this period
			"consumedCents": func() int64 {
				if consumedCents > includedGrantedCents {
					return includedGrantedCents
				}
				return consumedCents
			}(),
			"remainingCents": includedRemaining,
		},
		"consumedCents": consumedCents,
		"overageCents":  overageCents,
		"balance": gin.H{
			"balanceCents":   int64(balance),
			"holdsCents":     int64(holds),
			"availableCents": int64(available),
		},
	})
}

// monthlyUsageCents sums api-usage withdrawals for a user since the first
// instant of the current UTC month. Mirrors dailyUsageCents but over the
// billing month, so the rollup's consumption aligns with the monthly allotment
// window.
func monthlyUsageCents(ctx context.Context, user string, isTest bool) int64 {
	db := datastore.New(ctx)
	rootKey := db.NewKey("synckey", "", 1, nil)

	now := time.Now().UTC()
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)

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
		if !t.CreatedAt.Before(monthStart) {
			total += int64(t.Amount)
		}
	}
	return total
}
