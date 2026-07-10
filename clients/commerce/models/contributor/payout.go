package contributor

import (
	"math"
	"sort"
)

// PayoutAllocation represents a single contributor's share of a revenue pool.
type PayoutAllocation struct {
	ContributorId string  `json:"contributorId"`
	GitLogin      string  `json:"gitLogin"`
	Component     string  `json:"component"`
	Lines         int64   `json:"lines"`
	Percent       float64 `json:"percent"`
	AmountCents   int64   `json:"amountCents"`
}

// PayoutSummary is the result of running the payout algorithm.
type PayoutSummary struct {
	TotalRevenue    int64              `json:"totalRevenue"`    // Total revenue to distribute (cents)
	PlatformShare   int64              `json:"platformShare"`   // Platform's cut (cents)
	ContributorPool int64              `json:"contributorPool"` // Pool for contributors (cents)
	Allocations     []PayoutAllocation `json:"allocations"`
}

// PayoutConfig controls the revenue sharing algorithm.
type PayoutConfig struct {
	// PlatformPercent is the platform's share before contributor distribution.
	// Default: 75% platform, 25% to contributors.
	PlatformPercent float64

	// MinPayoutCents is the minimum payout threshold per contributor.
	// Below this, credits accumulate until threshold is reached.
	MinPayoutCents int64

	// ComponentWeights allows weighting certain components higher.
	// Key is component name, value is multiplier (1.0 = normal).
	// Core infrastructure might be weighted 2x, UI components 1x, etc.
	ComponentWeights map[string]float64
}

// DefaultConfig returns the default payout configuration.
// 75/25 split: 75% to platform, 25% to OSS contributors.
func DefaultConfig() PayoutConfig {
	return PayoutConfig{
		PlatformPercent: 75.0,
		MinPayoutCents:  100, // $1 minimum
		ComponentWeights: map[string]float64{
			// Core infrastructure weighted higher
			"@hanzo/bot":       2.0,
			"@hanzo/agents":    2.0,
			"@hanzo/mcp":       1.5,
			"@hanzo/gateway":   1.5,
			"@hanzo/commerce":  1.5,
			"@hanzo/auto":      1.0,
			"@hanzo/ui":        1.0,
		},
	}
}

// CalculatePayouts runs the OSS contributor revenue sharing algorithm.
//
// Algorithm:
//  1. Take total revenue for the period
//  2. Deduct platform share (default 80%)
//  3. Remaining 25% is the contributor pool
//  4. For each software component used in the billing period:
//     a. Weight the component's revenue contribution
//     b. For each contributor to that component:
//        - Calculate their line attribution percentage
//        - Multiply by component's weighted revenue share
//  5. Aggregate per-contributor across all components
//  6. Apply minimum payout threshold
//  7. Return sorted allocations (highest first)
func CalculatePayouts(
	totalRevenueCents int64,
	contributors []Contributor,
	componentRevenue map[string]int64, // component name → revenue attributed to it
	config PayoutConfig,
) PayoutSummary {
	if config.PlatformPercent == 0 {
		config = DefaultConfig()
	}

	platformShare := int64(math.Round(float64(totalRevenueCents) * config.PlatformPercent / 100.0))
	contributorPool := totalRevenueCents - platformShare

	if contributorPool <= 0 {
		return PayoutSummary{
			TotalRevenue:    totalRevenueCents,
			PlatformShare:   totalRevenueCents,
			ContributorPool: 0,
		}
	}

	// Calculate total weighted revenue across all components
	totalWeightedRevenue := 0.0
	for component, rev := range componentRevenue {
		weight := 1.0
		if w, ok := config.ComponentWeights[component]; ok {
			weight = w
		}
		totalWeightedRevenue += float64(rev) * weight
	}

	if totalWeightedRevenue == 0 {
		return PayoutSummary{
			TotalRevenue:    totalRevenueCents,
			PlatformShare:   platformShare,
			ContributorPool: contributorPool,
		}
	}

	// Build contributor index by git login
	contributorIndex := make(map[string]*Contributor)
	for i := range contributors {
		contributorIndex[contributors[i].GitLogin] = &contributors[i]
	}

	// Calculate per-contributor allocations
	allocationMap := make(map[string]*PayoutAllocation) // contributorId → allocation

	for _, contrib := range contributors {
		if !contrib.Active || !contrib.Verified {
			continue
		}

		for _, attr := range contrib.Attributions {
			compRev, ok := componentRevenue[attr.Component]
			if !ok || compRev == 0 {
				continue
			}

			weight := 1.0
			if w, ok := config.ComponentWeights[attr.Component]; ok {
				weight = w
			}

			// This contributor's share of this component
			componentPoolShare := float64(contributorPool) *
				(float64(compRev) * weight / totalWeightedRevenue)

			// Their percentage of the component
			contributorAmount := int64(math.Round(componentPoolShare * attr.Percent / 100.0))

			if contributorAmount <= 0 {
				continue
			}

			existing, ok := allocationMap[contrib.Id()]
			if ok {
				existing.AmountCents += contributorAmount
				// Keep the largest component as the primary
				if attr.Lines > existing.Lines {
					existing.Component = attr.Component
					existing.Lines = attr.Lines
					existing.Percent = attr.Percent
				}
			} else {
				allocationMap[contrib.Id()] = &PayoutAllocation{
					ContributorId: contrib.Id(),
					GitLogin:      contrib.GitLogin,
					Component:     attr.Component,
					Lines:         attr.Lines,
					Percent:       attr.Percent,
					AmountCents:   contributorAmount,
				}
			}
		}
	}

	// Filter by minimum payout and sort by amount (descending)
	allocations := make([]PayoutAllocation, 0, len(allocationMap))
	for _, a := range allocationMap {
		if a.AmountCents >= config.MinPayoutCents {
			allocations = append(allocations, *a)
		}
	}

	sort.Slice(allocations, func(i, j int) bool {
		return allocations[i].AmountCents > allocations[j].AmountCents
	})

	return PayoutSummary{
		TotalRevenue:    totalRevenueCents,
		PlatformShare:   platformShare,
		ContributorPool: contributorPool,
		Allocations:     allocations,
	}
}
