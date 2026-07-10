package engine

import (
	"testing"

	"github.com/hanzoai/cloud/clients/commerce/config"
)

func TestTierForReferralCount_Starter(t *testing.T) {
	tier := tierForReferralCount(0)
	if tier.Id != "starter" {
		t.Fatalf("expected starter tier for 0 referrals, got %s", tier.Id)
	}
	if tier.Rewards.RevenueSharePercent != 0 {
		t.Fatalf("expected 0%% revenue share for starter, got %.2f%%", tier.Rewards.RevenueSharePercent)
	}
}

func TestTierForReferralCount_Growth(t *testing.T) {
	tier := tierForReferralCount(10)
	if tier.Id != "growth" {
		t.Fatalf("expected growth tier for 10 referrals, got %s", tier.Id)
	}
	if tier.Rewards.RevenueSharePercent != 2.5 {
		t.Fatalf("expected 2.5%% revenue share for growth, got %.2f%%", tier.Rewards.RevenueSharePercent)
	}
}

func TestTierForReferralCount_Pro(t *testing.T) {
	tier := tierForReferralCount(50)
	if tier.Id != "pro" {
		t.Fatalf("expected pro tier for 50 referrals, got %s", tier.Id)
	}
	if tier.Rewards.RevenueSharePercent != 5 {
		t.Fatalf("expected 5%% revenue share for pro, got %.2f%%", tier.Rewards.RevenueSharePercent)
	}
}

func TestTierForReferralCount_Partner(t *testing.T) {
	tier := tierForReferralCount(200)
	if tier.Id != "partner" {
		t.Fatalf("expected partner tier for 200 referrals, got %s", tier.Id)
	}
	if tier.Rewards.RevenueSharePercent != 7.5 {
		t.Fatalf("expected 7.5%% revenue share for partner, got %.2f%%", tier.Rewards.RevenueSharePercent)
	}
}

func TestTierForReferralCount_HighCount(t *testing.T) {
	tier := tierForReferralCount(1000)
	if tier.Id != "partner" {
		t.Fatalf("expected partner tier for 1000 referrals, got %s", tier.Id)
	}
}

func TestTierForReferralCount_BoundaryGrowth(t *testing.T) {
	// 9 should be starter, 10 should be growth
	tier9 := tierForReferralCount(9)
	if tier9.Id != "starter" {
		t.Fatalf("expected starter for 9 referrals, got %s", tier9.Id)
	}

	tier10 := tierForReferralCount(10)
	if tier10.Id != "growth" {
		t.Fatalf("expected growth for 10 referrals, got %s", tier10.Id)
	}
}

func TestTierForReferralCount_BoundaryPro(t *testing.T) {
	tier49 := tierForReferralCount(49)
	if tier49.Id != "growth" {
		t.Fatalf("expected growth for 49 referrals, got %s", tier49.Id)
	}

	tier50 := tierForReferralCount(50)
	if tier50.Id != "pro" {
		t.Fatalf("expected pro for 50 referrals, got %s", tier50.Id)
	}
}

func TestTierForReferralCount_BoundaryPartner(t *testing.T) {
	tier199 := tierForReferralCount(199)
	if tier199.Id != "pro" {
		t.Fatalf("expected pro for 199 referrals, got %s", tier199.Id)
	}

	tier200 := tierForReferralCount(200)
	if tier200.Id != "partner" {
		t.Fatalf("expected partner for 200 referrals, got %s", tier200.Id)
	}
}

func TestLoadReferralProgram_HasTiers(t *testing.T) {
	cfg := config.GetReferralProgram()
	if len(cfg.Tiers) != 4 {
		t.Fatalf("expected 4 tiers, got %d", len(cfg.Tiers))
	}
}
