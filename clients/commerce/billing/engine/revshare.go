package engine

import (
	"math"

	"github.com/hanzoai/cloud/clients/commerce/config"
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/models/fee"
	"github.com/hanzoai/cloud/clients/commerce/models/referral"
	"github.com/hanzoai/cloud/clients/commerce/models/referrer"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
)

// tierForReferralCount returns the tier matching the given referral count.
// Delegates to the shared config package.
func tierForReferralCount(count int) config.ReferralTier {
	return config.GetReferralProgram().TierForCount(count)
}

// TrackRevenueShare checks if the paying user was referred and, if so, creates
// an affiliate Fee record for the referrer's revenue share. This is fire-and-forget;
// errors are logged but never propagated to the caller.
//
// Parameters:
//   - db: datastore scoped to the org namespace
//   - userID: the IAM user who incurred the usage charge
//   - chargeAmount: the usage charge in cents
//   - cur: the currency of the charge
//   - transactionID: the ID of the withdraw transaction (for Fee.PaymentId)
//   - test: true if the org is not live
func TrackRevenueShare(db *datastore.Datastore, userID string, chargeAmount currency.Cents, cur currency.Type, transactionID string, test bool) {
	// 1. Look up an active referral for this user.
	referrals := make([]*referral.Referral, 0, 1)
	q := referral.Query(db).
		Filter("UserId=", userID).
		Filter("Revoked=", false).
		Limit(1)

	if _, err := q.GetAll(&referrals); err != nil {
		log.Error("revshare: failed to query referral for user %s: %v", userID, err)
		return
	}
	if len(referrals) == 0 {
		return // not a referred user
	}
	ref := referrals[0]

	// 2. Look up the referrer to get affiliateId and count referrals for tier.
	referrerID := ref.Referrer.Id
	if referrerID == "" {
		return
	}

	rfr := referrer.New(db)
	if err := rfr.GetById(referrerID); err != nil {
		log.Error("revshare: failed to load referrer %s: %v", referrerID, err)
		return
	}

	// 3. Count total referrals to determine tier.
	referralCount, err := referral.Query(db).
		Filter("Referrer.Id=", referrerID).
		Count()
	if err != nil {
		log.Error("revshare: failed to count referrals for referrer %s: %v", referrerID, err)
		return
	}

	tier := tierForReferralCount(referralCount)
	if tier.Rewards.RevenueSharePercent <= 0 {
		return // this tier has no revenue share
	}

	// 4. Calculate commission amount (round down -- platform keeps remainder).
	commissionAmount := currency.Cents(math.Floor(
		float64(chargeAmount) * tier.Rewards.RevenueSharePercent / 100.0,
	))
	if commissionAmount <= 0 {
		return
	}

	// 5. Create the affiliate Fee record.
	affiliateID := rfr.AffiliateId
	if affiliateID == "" {
		// Referrer has no affiliate account; log and skip.
		log.Debug("revshare: referrer %s has no affiliateId, skipping fee creation", referrerID)
		return
	}

	// Load the affiliate to get its key for parenting the fee.
	if err := rfr.LoadAffiliate(); err != nil {
		log.Error("revshare: failed to load affiliate %s: %v", affiliateID, err)
		return
	}

	fe := fee.New(db)
	fe.Name = "Referral revenue share"
	fe.Type = fee.Affiliate
	fe.AffiliateId = affiliateID
	fe.Parent = rfr.Affiliate.Key()
	fe.Currency = cur
	fe.Amount = commissionAmount
	fe.PaymentId = transactionID
	fe.Status = fee.Pending
	fe.Live = !test
	fe.Test = test

	if err := fe.Create(); err != nil {
		log.Error("revshare: failed to create fee for referrer %s, amount %d: %v", referrerID, commissionAmount, err)
		return
	}

	log.Info("revshare: created fee %s for referrer %s (affiliate %s), amount %d %s (%.2f%% of %d)",
		fe.Id(), referrerID, affiliateID, commissionAmount, cur, tier.Rewards.RevenueSharePercent, chargeAmount)
}
