package tier_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/hanzoai/cloud/clients/commerce/billing/tier"
)

func TestTier(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Tier Suite")
}

var _ = Describe("Tier", func() {
	Describe("Get", func() {
		It("returns the correct config for known tiers", func() {
			// No free tier: Free grants NO daily credit, so a zero-balance
			// account is gated (effectiveAvailable collapses to prepaid).
			cfg := tier.Get(tier.Free)
			Expect(cfg.Name).To(Equal(tier.Free))
			Expect(cfg.MaxAgents).To(Equal(1))
			Expect(cfg.DailyCreditsCents).To(Equal(int64(0)))

			cfg = tier.Get(tier.Pro)
			Expect(cfg.Name).To(Equal(tier.Pro))
			Expect(cfg.MaxAgents).To(Equal(10))
			Expect(cfg.DailyCreditsCents).To(Equal(int64(0)))
		})

		It("falls back to Free for unknown tiers", func() {
			cfg := tier.Get(tier.Name("nonexistent"))
			Expect(cfg.Name).To(Equal(tier.Free))
		})
	})

	Describe("Parse", func() {
		It("parses known tier strings", func() {
			Expect(tier.Parse("free")).To(Equal(tier.Free))
			Expect(tier.Parse("starter")).To(Equal(tier.Starter))
			Expect(tier.Parse("pro")).To(Equal(tier.Pro))
			Expect(tier.Parse("enterprise")).To(Equal(tier.Enterprise))
		})

		It("defaults empty or unknown strings to Free", func() {
			Expect(tier.Parse("")).To(Equal(tier.Free))
			Expect(tier.Parse("gold")).To(Equal(tier.Free))
		})
	})

	Describe("IsModelAllowed", func() {
		It("allows wildcard tiers access to all models", func() {
			cfg := tier.Get(tier.Pro)
			Expect(cfg.IsModelAllowed("claude-opus-4")).To(BeTrue())
			Expect(cfg.IsModelAllowed("anything")).To(BeTrue())
		})

		It("enforces prefix matching for restricted tiers", func() {
			cfg := tier.Get(tier.Free)
			Expect(cfg.IsModelAllowed("claude-sonnet-4-20250514")).To(BeTrue())
			Expect(cfg.IsModelAllowed("zen3-mini")).To(BeTrue())
			Expect(cfg.IsModelAllowed("claude-opus-4")).To(BeFalse())
			Expect(cfg.IsModelAllowed("gpt-4o")).To(BeFalse())
		})
	})

	Describe("HasDailyCredits", func() {
		It("is false for every tier — there is no free tier", func() {
			// The daily-credit mechanism is retained but disabled by config
			// (DailyCreditsCents == 0 everywhere), so no tier grants a standing
			// daily balance. A zero-balance account is always gated.
			Expect(tier.Get(tier.Free).HasDailyCredits()).To(BeFalse())
			Expect(tier.Get(tier.Starter).HasDailyCredits()).To(BeFalse())
			Expect(tier.Get(tier.Pro).HasDailyCredits()).To(BeFalse())
			Expect(tier.Get(tier.Enterprise).HasDailyCredits()).To(BeFalse())
		})
	})

	Describe("IsUnlimitedAgents", func() {
		It("returns true only for enterprise tier", func() {
			Expect(tier.Get(tier.Enterprise).IsUnlimitedAgents()).To(BeTrue())
			Expect(tier.Get(tier.Free).IsUnlimitedAgents()).To(BeFalse())
			Expect(tier.Get(tier.Pro).IsUnlimitedAgents()).To(BeFalse())
		})
	})

	Describe("All", func() {
		It("returns all four tiers", func() {
			all := tier.All()
			Expect(all).To(HaveLen(4))
			Expect(all).To(HaveKey(tier.Free))
			Expect(all).To(HaveKey(tier.Starter))
			Expect(all).To(HaveKey(tier.Pro))
			Expect(all).To(HaveKey(tier.Enterprise))
		})
	})
})
