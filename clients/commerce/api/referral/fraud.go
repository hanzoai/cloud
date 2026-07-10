package referral

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/auth"
	"github.com/hanzoai/cloud/clients/commerce/config"
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/models/referral"
	"github.com/hanzoai/cloud/clients/commerce/models/referrer"
)

const (
	// velocityLimit is the max referrals allowed per referrer in one hour.
	velocityLimit = 10
)

// ClaimRequest represents an incoming referral claim to validate.
type ClaimRequest struct {
	UserId    string // The new user being referred
	Email     string // The new user's email
	IP        string // The IP address of the claim request
	ReferCode string // The referrer code being used
}

// FraudChecker validates referral claims against fraud rules.
// Config is loaded from the shared config package.
type FraudChecker struct {
	cfg       *config.ReferralProgram
	db        *datastore.Datastore
	iamIssuer string
}

// NewFraudChecker creates a FraudChecker backed by the shared program config.
func NewFraudChecker(db *datastore.Datastore) *FraudChecker {
	issuer := os.Getenv("IAM_ENDPOINT")
	if issuer == "" {
		issuer = "https://hanzo.id"
	}
	return &FraudChecker{
		cfg:       loadProgramConfig(),
		db:        db,
		iamIssuer: strings.TrimRight(issuer, "/"),
	}
}

// Check runs all fraud checks against a referral claim.
// Returns (allowed, reason). If not allowed, reason describes why.
// Sets ref.Blacklisted and ref.Duplicate as appropriate side effects.
func (fc *FraudChecker) Check(c *gin.Context, ref *referrer.Referrer, claim ClaimRequest) (bool, string) {
	fraud := fc.cfg.Fraud

	// 1. Self-referral block
	if fraud.BlockSelfReferral && ref.UserId == claim.UserId {
		log.Info("fraud: self-referral blocked, referrer=%s user=%s", ref.UserId, claim.UserId, c)
		return false, "self-referral: referrer and referred user are the same"
	}

	// 2. Duplicate detection: same userId can't be referred twice
	if allowed, reason := fc.checkDuplicate(claim); !allowed {
		ref.Duplicate = true
		log.Info("fraud: %s, userId=%s", reason, claim.UserId, c)
		return false, reason
	}

	// 3. Disposable email detection
	if isDisposableEmail(claim.Email) {
		log.Info("fraud: disposable email blocked, email=%s", claim.Email, c)
		return false, "disposable email domain not allowed"
	}

	// 4. Email verification via IAM
	if fraud.RequireEmailVerification {
		if allowed, reason := fc.checkEmailVerified(c.Request.Context(), claim); !allowed {
			log.Info("fraud: %s, email=%s", reason, claim.Email, c)
			return false, reason
		}
	}

	// 5. IP blacklist: claim IP matches referrer's IP (same person)
	if fraud.BlacklistSameIP && ref.Client.Ip != "" && ref.Client.Ip == claim.IP {
		ref.Blacklisted = true
		log.Info("fraud: same-IP blocked, ip=%s referrer=%s", claim.IP, ref.Id(), c)
		return false, "claim IP matches referrer IP"
	}

	// 6. IP cooldown: no more than 1 referrer created from the same IP within cooldownDays
	if allowed, reason := fc.checkIPCooldown(claim, fraud.CooldownDays); !allowed {
		log.Info("fraud: %s, ip=%s", reason, claim.IP, c)
		return false, reason
	}

	// 7. Daily limit: no more than maxReferralsPerDay per referrer
	if allowed, reason := fc.checkDailyLimit(ref, fraud.MaxReferralsPerDay); !allowed {
		log.Info("fraud: %s, referrer=%s", reason, ref.Id(), c)
		return false, reason
	}

	// 8. Velocity check: flag if referrer gets more than velocityLimit referrals in 1 hour
	if allowed, reason := fc.checkVelocity(ref); !allowed {
		log.Info("fraud: %s, referrer=%s", reason, ref.Id(), c)
		return false, reason
	}

	log.Debug("fraud: all checks passed for referrer=%s user=%s", ref.Id(), claim.UserId, c)
	return true, ""
}

// checkDuplicate verifies the userId hasn't already been referred.
func (fc *FraudChecker) checkDuplicate(claim ClaimRequest) (bool, string) {
	refs := make([]referral.Referral, 0)
	if _, err := referral.Query(fc.db).Filter("UserId=", claim.UserId).Limit(1).GetAll(&refs); err != nil {
		log.Error("fraud: duplicate check query error: %v", err)
		// Fail open on query errors to avoid blocking legitimate users
		return true, ""
	}
	if len(refs) > 0 {
		return false, "duplicate: user has already been referred"
	}
	return true, ""
}

// checkEmailVerified calls IAM to confirm the email is verified.
func (fc *FraudChecker) checkEmailVerified(ctx context.Context, claim ClaimRequest) (bool, string) {
	cfg := &auth.IAMConfig{
		Issuer: fc.iamIssuer,
		HTTPClient: &http.Client{
			Timeout: 5 * time.Second,
		},
	}

	client, err := auth.NewIAMClient(cfg)
	if err != nil {
		log.Error("fraud: failed to create IAM client: %v", err)
		// Fail open: don't block referrals if IAM is unreachable
		return true, ""
	}

	userInfo, err := client.GetUserInfo(ctx, claim.UserId)
	if err != nil {
		log.Warn("fraud: IAM email verification failed for %s: %v", claim.Email, err)
		// Fail open: IAM might be temporarily unreachable
		return true, ""
	}

	if !userInfo.EmailVerified {
		return false, "email not verified in IAM"
	}

	return true, ""
}

// checkIPCooldown ensures no more than 1 referrer created from the same IP
// within cooldownDays. Queries referrers (not referrals) because referrers
// store Client.Ip while referrals do not.
func (fc *FraudChecker) checkIPCooldown(claim ClaimRequest, cooldownDays int) (bool, string) {
	if claim.IP == "" {
		return true, ""
	}

	cutoff := time.Now().AddDate(0, 0, -cooldownDays)

	refs := make([]referrer.Referrer, 0)
	if _, err := referrer.Query(fc.db).Filter("Client.Ip=", claim.IP).GetAll(&refs); err != nil {
		log.Error("fraud: IP cooldown query error: %v", err)
		return true, ""
	}

	for _, r := range refs {
		if r.CreatedAt.After(cutoff) {
			return false, fmt.Sprintf("IP cooldown: another referral from this IP within %d days", cooldownDays)
		}
	}

	return true, ""
}

// checkDailyLimit ensures no more than maxPerDay referrals per referrer today.
func (fc *FraudChecker) checkDailyLimit(ref *referrer.Referrer, maxPerDay int) (bool, string) {
	startOfDay := time.Now().Truncate(24 * time.Hour)

	refs := make([]referral.Referral, 0)
	if _, err := referral.Query(fc.db).Filter("Referrer.Id=", ref.Id()).GetAll(&refs); err != nil {
		log.Error("fraud: daily limit query error: %v", err)
		return true, ""
	}

	count := 0
	for _, r := range refs {
		if r.CreatedAt.After(startOfDay) {
			count++
		}
	}

	if count >= maxPerDay {
		return false, fmt.Sprintf("daily limit: referrer has %d referrals today (max %d)", count, maxPerDay)
	}

	return true, ""
}

// checkVelocity flags if a referrer gets more than velocityLimit referrals in 1 hour.
func (fc *FraudChecker) checkVelocity(ref *referrer.Referrer) (bool, string) {
	oneHourAgo := time.Now().Add(-1 * time.Hour)

	refs := make([]referral.Referral, 0)
	if _, err := referral.Query(fc.db).Filter("Referrer.Id=", ref.Id()).GetAll(&refs); err != nil {
		log.Error("fraud: velocity check query error: %v", err)
		return true, ""
	}

	count := 0
	for _, r := range refs {
		if r.CreatedAt.After(oneHourAgo) {
			count++
		}
	}

	if count > velocityLimit {
		ref.Blacklisted = true
		return false, fmt.Sprintf("velocity: referrer has %d referrals in the last hour (max %d)", count, velocityLimit)
	}

	return true, ""
}

// disposableDomains is a set of common disposable/temporary email providers.
var disposableDomains = map[string]struct{}{
	"mailinator.com":         {},
	"guerrillamail.com":      {},
	"guerrillamail.net":      {},
	"tempmail.com":           {},
	"throwaway.email":        {},
	"temp-mail.org":          {},
	"fakeinbox.com":          {},
	"sharklasers.com":        {},
	"guerrillamailblock.com": {},
	"grr.la":                 {},
	"dispostable.com":        {},
	"yopmail.com":            {},
	"yopmail.fr":             {},
	"trashmail.com":          {},
	"trashmail.me":           {},
	"trashmail.net":          {},
	"mailnesia.com":          {},
	"maildrop.cc":            {},
	"discard.email":          {},
	"mailsac.com":            {},
	"10minutemail.com":       {},
	"tempinbox.com":          {},
	"burnermail.io":          {},
	"getnada.com":            {},
	"mohmal.com":             {},
	"harakirimail.com":       {},
	"emailondeck.com":        {},
	"33mail.com":             {},
	"mailcatch.com":          {},
	"mintemail.com":          {},
	"temp-mail.io":           {},
	"tempr.email":            {},
	"tempail.com":            {},
	"internxt.com":           {},
}

// isDisposableEmail checks if the email domain is a known disposable email provider.
func isDisposableEmail(email string) bool {
	parts := strings.SplitN(email, "@", 2)
	if len(parts) != 2 {
		return false
	}
	domain := strings.ToLower(parts[1])
	_, ok := disposableDomains[domain]
	return ok
}
