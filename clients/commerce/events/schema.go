package events

// Standard commerce event subjects for NATS/JetStream.
const (
	SubjectOrderCreated    = "commerce.order.created"
	SubjectOrderCompleted  = "commerce.order.completed"
	SubjectOrderCanceled   = "commerce.order.canceled"
	SubjectOrderRefunded   = "commerce.order.refunded"
	SubjectCheckoutStarted = "commerce.checkout.started"
	SubjectCheckoutFailed  = "commerce.checkout.failed"
	SubjectPaymentReceived = "commerce.payment.received"
	SubjectCartUpdated     = "commerce.cart.updated"
	SubjectProductViewed   = "commerce.product.viewed"

	// Referral events
	SubjectReferralLinkCreated      = "commerce.referral.link_created"
	SubjectReferralClaimed          = "commerce.referral.claimed"
	SubjectReferralCreditGranted    = "commerce.referral.credit_granted"
	SubjectReferralCommissionEarned = "commerce.referral.commission_earned"
	SubjectReferralPayoutSent       = "commerce.referral.payout_sent"
	SubjectReferralTierUpgraded     = "commerce.referral.tier_upgraded"

	// Contributor events
	SubjectContributorRegistered     = "commerce.contributor.registered"
	SubjectContributorPayoutCalc     = "commerce.contributor.payout_calculated"
	SubjectContributorPayoutSent     = "commerce.contributor.payout_sent"
)

// StreamName is the JetStream stream for commerce events.
const StreamName = "COMMERCE"

// StreamSubjects defines what subjects the COMMERCE stream captures.
var StreamSubjects = []string{"commerce.>"}
