package referral

import (
	"testing"
)

func TestIsDisposableEmail(t *testing.T) {
	tests := []struct {
		email string
		want  bool
	}{
		{"user@mailinator.com", true},
		{"user@yopmail.com", true},
		{"user@guerrillamail.com", true},
		{"user@trashmail.net", true},
		{"user@temp-mail.org", true},
		{"user@gmail.com", false},
		{"user@hanzo.ai", false},
		{"user@company.com", false},
		{"user@outlook.com", false},
		{"invalid-no-at-sign", false},
		{"", false},
		{"user@MAILINATOR.COM", true}, // case insensitive
		{"user@Yopmail.Com", true},    // mixed case
	}

	for _, tt := range tests {
		t.Run(tt.email, func(t *testing.T) {
			got := isDisposableEmail(tt.email)
			if got != tt.want {
				t.Errorf("isDisposableEmail(%q) = %v, want %v", tt.email, got, tt.want)
			}
		})
	}
}

func TestLoadProgramConfig(t *testing.T) {
	cfg := loadProgramConfig()
	if cfg == nil {
		t.Fatal("loadProgramConfig() returned nil")
	}

	// Verify fraud config loaded from shared config package
	if cfg.Fraud.MaxReferralsPerDay != 50 {
		t.Errorf("MaxReferralsPerDay = %d, want 50", cfg.Fraud.MaxReferralsPerDay)
	}
	if cfg.Fraud.CooldownDays != 7 {
		t.Errorf("CooldownDays = %d, want 7", cfg.Fraud.CooldownDays)
	}
	if !cfg.Fraud.BlockSelfReferral {
		t.Error("BlockSelfReferral = false, want true")
	}
	if !cfg.Fraud.RequireEmailVerification {
		t.Error("RequireEmailVerification = false, want true")
	}
	if !cfg.Fraud.BlacklistSameIP {
		t.Error("BlacklistSameIP = false, want true")
	}
}

func TestTierForCount(t *testing.T) {
	cfg := loadProgramConfig()
	if cfg == nil {
		t.Fatal("loadProgramConfig() returned nil")
	}

	tests := []struct {
		count  int
		wantId string
	}{
		{0, "starter"},
		{5, "starter"},
		{9, "starter"},
		{10, "growth"},
		{49, "growth"},
		{50, "pro"},
		{199, "pro"},
		{200, "partner"},
		{1000, "partner"},
	}

	for _, tt := range tests {
		t.Run(tt.wantId, func(t *testing.T) {
			tier := cfg.TierForCount(tt.count)
			if tier.Id != tt.wantId {
				t.Errorf("TierForCount(%d) = %q, want %q", tt.count, tier.Id, tt.wantId)
			}
		})
	}
}
