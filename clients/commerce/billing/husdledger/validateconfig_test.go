package husdledger

import (
	"testing"

	"github.com/hanzoai/cloud/clients/commerce/util/husd"
)

// cfgTokenKey is the shared HUSD config shape a deploy carries for the OSS
// contributor payout path: a token address + a treasury key, but NO ledger
// derivation seed. Values need only be non-empty — ValidateConfig gates on
// Configured() (token && key present), never on key validity.
func cfgTokenKey() husd.Config {
	return husd.Config{
		TokenAddress: "0xc57b7eCE2Ce2E74ef3Bc08Cfd5f5Fb41B6Ad4D66",
		TreasuryKey:  "44212ba8bfdc13aff65887ea8e6324326938b7d8f148f1b118faf6bb6baab5ef",
	}
}

// TestValidateConfig pins the boot gate across the four config states. The seed
// is the ledger's sole intent signal: token+key WITHOUT a seed is the inert
// state (must boot), and only "seed set but token/key missing" is an incoherent
// ledger config that must fail closed.
func TestValidateConfig(t *testing.T) {
	seed := []byte("0123456789abcdef0123456789abcdef") // 32 bytes, non-empty

	cases := []struct {
		name    string
		cfg     husd.Config
		seed    []byte
		wantErr bool
	}{
		{"unconfigured, no seed → boots (dev/CI)", husd.Config{}, nil, false},
		{"token+key, no seed → boots INERT (contributor payout / current prod)", cfgTokenKey(), nil, false},
		{"token+key+seed → boots (chain ledger intended)", cfgTokenKey(), seed, false},
		{"seed but token+key missing → REFUSE (fail-closed)", husd.Config{}, seed, true},
		{"seed + token only (no key) → REFUSE", husd.Config{TokenAddress: "0xabc"}, seed, true},
		{"seed + key only (no token) → REFUSE", husd.Config{TreasuryKey: "deadbeef"}, seed, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateConfig(tc.cfg, tc.seed)
			if tc.wantErr && err == nil {
				t.Fatalf("ValidateConfig: expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("ValidateConfig: expected nil, got %v", err)
			}
		})
	}
}

// TestInertWhenTokenKeyNoSeed proves the exact production posture v1.46.38 ships
// with: HUSD token+key present (the contributor-payout config) but no derivation
// seed. The boot gate MUST pass AND the ledger MUST be disabled — money-in
// handlers keep the DB credit path (chainCreditEnabled()==false), so #68 is inert
// until the seed is deliberately provisioned at the mainnet cutover.
func TestInertWhenTokenKeyNoSeed(t *testing.T) {
	cfg := cfgTokenKey()
	if err := ValidateConfig(cfg, nil); err != nil {
		t.Fatalf("boot must pass for token+key without a seed, got: %v", err)
	}
	if svc := New(cfg, nil); svc.Enabled() {
		t.Fatalf("chain ledger must be INERT (Enabled()==false) with token+key but no seed")
	}
}
