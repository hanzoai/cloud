package referral

import (
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/middleware"
	"github.com/hanzoai/cloud/clients/commerce/middleware/iammiddleware"
	"github.com/hanzoai/cloud/clients/commerce/models/creditgrant"
	"github.com/hanzoai/cloud/clients/commerce/models/referral"
	"github.com/hanzoai/cloud/clients/commerce/models/referrer"
	"github.com/hanzoai/cloud/clients/commerce/util/json/http"

	. "github.com/hanzoai/cloud/clients/commerce/types"
)

type claimRequest struct {
	Code   string `json:"code"`
	UserId string `json:"userId"`
	Email  string `json:"email"`
}

// ClaimReferral processes a referral claim: validates the code, checks fraud
// rules, creates credit grants for both parties, and records the referral.
//
//	POST /v1/referral/claim
func ClaimReferral(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	var req claimRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		http.Fail(c, 400, "invalid request body", err)
		return
	}

	if req.Code == "" {
		http.Fail(c, 400, "code is required", nil)
		return
	}

	// Determine referee userId: prefer the gateway-authenticated user
	// (claims.Subject) over the request body to prevent a referee from
	// claiming on behalf of another user. claims is always non-nil;
	// an empty Subject means the request was anonymous and we fall back
	// to the request body.
	refereeUserId := req.UserId
	if subject := iammiddleware.GetIAMClaims(c).Subject; subject != "" {
		refereeUserId = subject
	}
	if refereeUserId == "" {
		http.Fail(c, 400, "userId is required", nil)
		return
	}

	// 1. Look up referrer by code.
	ref := referrer.New(db)
	if key, ok, err := referrer.Query(db).Filter("Code=", req.Code).First(ref); err != nil {
		log.Error("Failed to query referrer by code: %v", err, c)
		http.Fail(c, 500, "failed to look up referral code", err)
		return
	} else if !ok {
		http.Fail(c, 404, "invalid referral code", nil)
		return
	} else {
		ref.Init(db)
		ref.SetKey(key)
	}

	// 2. Validate: not blacklisted.
	if ref.Blacklisted {
		http.Fail(c, 403, "referral code is not available", nil)
		return
	}

	// 3. Validate: not self-referral.
	if ref.UserId == refereeUserId {
		http.Fail(c, 403, "cannot use your own referral code", nil)
		return
	}

	// 4. Validate: not duplicate (same referee userId already claimed).
	existingReferrals := make([]*referral.Referral, 0)
	if _, err := referral.Query(db).Filter("UserId=", refereeUserId).Limit(1).GetAll(&existingReferrals); err != nil {
		log.Error("Failed to check existing referrals: %v", err, c)
		http.Fail(c, 500, "failed to check existing referrals", err)
		return
	}
	if len(existingReferrals) > 0 {
		http.Fail(c, 409, "user has already claimed a referral", nil)
		return
	}

	// 5. Validate: referrer daily limit.
	cfg := loadProgramConfig()
	todayStart := time.Now().Truncate(24 * time.Hour)
	todayReferrals := make([]*referral.Referral, 0)
	if _, err := referral.Query(db).Filter("Referrer.Id=", ref.Id()).GetAll(&todayReferrals); err != nil {
		log.Error("Failed to count daily referrals: %v", err, c)
		http.Fail(c, 500, "failed to check daily limit", err)
		return
	}
	todayCount := 0
	for _, r := range todayReferrals {
		if r.CreatedAt.After(todayStart) {
			todayCount++
		}
	}
	if todayCount >= cfg.Fraud.MaxReferralsPerDay {
		http.Fail(c, 429, "referrer has reached the daily referral limit", nil)
		return
	}

	// 6. Determine the referrer's tier based on total referral count.
	allReferrals := make([]*referral.Referral, 0)
	referralCount := 0
	if _, err := referral.Query(db).Filter("Referrer.Id=", ref.Id()).GetAll(&allReferrals); err == nil {
		referralCount = len(allReferrals)
	}
	tier := cfg.TierForCount(referralCount)

	now := time.Now()
	rootKey := db.NewKey("synckey", "", 1, nil)

	// 7. Create CreditGrant for referee.
	refereeCreditCents := int64(tier.Rewards.RefereeCreditCents)
	refereeExpiryDays := tier.Limits.CreditExpiryDays
	if refereeExpiryDays <= 0 {
		refereeExpiryDays = 90
	}

	refereeGrant := creditgrant.New(db)
	refereeGrant.Parent = rootKey
	refereeGrant.UserId = refereeUserId
	refereeGrant.Name = "Referral signup bonus"
	refereeGrant.AmountCents = refereeCreditCents
	refereeGrant.RemainingCents = refereeCreditCents
	refereeGrant.Currency = "usd"
	refereeGrant.Priority = 10
	refereeGrant.EffectiveAt = now
	refereeGrant.ExpiresAt = now.AddDate(0, 0, refereeExpiryDays)
	refereeGrant.Tags = "referral-bonus"
	refereeGrant.Metadata = Map{
		"referrerCode": req.Code,
		"referrerId":   ref.Id(),
		"tier":         tier.Id,
	}

	if err := refereeGrant.Create(); err != nil {
		log.Error("Failed to create referee credit grant: %v", err, c)
		http.Fail(c, 500, "failed to create referee credit", err)
		return
	}

	// 8. Create CreditGrant for referrer.
	referrerCreditCents := int64(tier.Rewards.ReferrerCreditCents)
	referrerExpiryDays := tier.Limits.CreditExpiryDays
	if referrerExpiryDays <= 0 {
		referrerExpiryDays = 90
	}

	referrerGrant := creditgrant.New(db)
	referrerGrant.Parent = rootKey
	referrerGrant.UserId = ref.UserId
	referrerGrant.Name = "Referral reward"
	referrerGrant.AmountCents = referrerCreditCents
	referrerGrant.RemainingCents = referrerCreditCents
	referrerGrant.Currency = "usd"
	referrerGrant.Priority = 10
	referrerGrant.EffectiveAt = now
	referrerGrant.ExpiresAt = now.AddDate(0, 0, referrerExpiryDays)
	referrerGrant.Tags = "referral-reward"
	referrerGrant.Metadata = Map{
		"refereeUserId": refereeUserId,
		"referrerCode":  req.Code,
		"tier":          tier.Id,
	}

	if err := referrerGrant.Create(); err != nil {
		log.Error("Failed to create referrer credit grant: %v", err, c)
		http.Fail(c, 500, "failed to create referrer credit", err)
		return
	}

	// 9. Create the Referral record.
	rfl := referral.New(db)
	rfl.Type = referral.NewUser
	rfl.UserId = refereeUserId
	rfl.Referrer = referral.Referrer{
		Id:          ref.Id(),
		UserId:      ref.UserId,
		AffiliateId: ref.AffiliateId,
	}

	if err := rfl.Create(); err != nil {
		log.Error("Failed to create referral record: %v", err, c)
		http.Fail(c, 500, "failed to create referral record", err)
		return
	}

	// 10. Update referrer state.
	if ref.FirstReferredAt.IsZero() {
		ref.FirstReferredAt = now
	}
	if err := ref.Update(); err != nil {
		log.Warn("Failed to update referrer state: %v", err, c)
	}

	log.Info("Referral claimed: referee=%s referrer=%s code=%s tier=%s", refereeUserId, ref.UserId, req.Code, tier.Id, c)

	c.JSON(201, gin.H{
		"referralId": rfl.Id(),
		"referrerId": ref.Id(),
		"refereeId":  refereeUserId,
		"tier":       tier.Id,
		"creditGranted": gin.H{
			"referee": gin.H{
				"grantId":    refereeGrant.Id(),
				"amountCents": refereeCreditCents,
				"expiresAt":  refereeGrant.ExpiresAt,
			},
			"referrer": gin.H{
				"grantId":    referrerGrant.Id(),
				"amountCents": referrerCreditCents,
				"expiresAt":  referrerGrant.ExpiresAt,
			},
		},
	})
}
