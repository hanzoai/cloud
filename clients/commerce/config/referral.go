package config

import (
	_ "embed"
	"encoding/json"
	"sync"
)

//go:embed referral-program.json
var referralProgramJSON []byte

var (
	referralOnce    sync.Once
	referralProgram *ReferralProgram
)

// ReferralTier represents a single tier in the referral program.
type ReferralTier struct {
	Id           string `json:"id"`
	Name         string `json:"name"`
	MinReferrals int    `json:"minReferrals"`
	MaxReferrals *int   `json:"maxReferrals"` // nil means unlimited
	Rewards      struct {
		ReferrerCreditCents int     `json:"referrerCreditCents"`
		RefereeCreditCents  int     `json:"refereeCreditCents"`
		RevenueSharePercent float64 `json:"revenueSharePercent"`
		CreditCurrency      string  `json:"creditCurrency"`
	} `json:"rewards"`
	Limits struct {
		MaxTotalCreditsCents int `json:"maxTotalCreditsCents"`
		CreditExpiryDays     int `json:"creditExpiryDays"`
	} `json:"limits"`
}

// ReferralFraudConfig holds fraud detection settings.
type ReferralFraudConfig struct {
	RequireEmailVerification bool `json:"requireEmailVerification"`
	BlockSelfReferral        bool `json:"blockSelfReferral"`
	CooldownDays             int  `json:"cooldownDays"`
	MaxReferralsPerDay       int  `json:"maxReferralsPerDay"`
	BlacklistSameIP          bool `json:"blacklistSameIP"`
}

// ReferralProgram is the parsed referral-program.json config.
type ReferralProgram struct {
	Id      string              `json:"id"`
	Name    string              `json:"name"`
	Version int                 `json:"version"`
	Active  bool                `json:"active"`
	Tiers   []ReferralTier      `json:"tiers"`
	Fraud   ReferralFraudConfig `json:"fraud"`
}

// TierForCount returns the tier matching the given referral count.
// Tiers are ordered by minReferrals ascending; the highest matching tier wins.
func (p *ReferralProgram) TierForCount(count int) ReferralTier {
	for i := len(p.Tiers) - 1; i >= 0; i-- {
		if count >= p.Tiers[i].MinReferrals {
			return p.Tiers[i]
		}
	}
	if len(p.Tiers) > 0 {
		return p.Tiers[0]
	}
	return ReferralTier{Id: "starter"}
}

// GetReferralProgram returns the parsed referral program config.
// The config is loaded once from the embedded JSON; subsequent calls return the
// cached value. Returns safe defaults if the embedded JSON fails to parse.
func GetReferralProgram() *ReferralProgram {
	referralOnce.Do(func() {
		cfg := &ReferralProgram{}
		if err := json.Unmarshal(referralProgramJSON, cfg); err != nil {
			referralProgram = &ReferralProgram{
				Fraud: ReferralFraudConfig{MaxReferralsPerDay: 50},
				Tiers: []ReferralTier{{
					Id:           "starter",
					MinReferrals: 0,
				}},
			}
			return
		}
		referralProgram = cfg
	})
	return referralProgram
}
