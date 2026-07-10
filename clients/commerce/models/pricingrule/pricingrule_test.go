package pricingrule

import (
	"testing"
)

func TestCalculateCost_PerUnit(t *testing.T) {
	rule := &PricingRule{
		PricingType: PerUnit,
		UnitPrice: 2, // 2 cents per unit
	}

	tests := []struct {
		name     string
		quantity int64
		want     int64
	}{
		{"zero quantity", 0, 0},
		{"single unit", 1, 2},
		{"hundred units", 100, 200},
		{"large quantity", 1000000, 2000000},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := rule.CalculateCost(tt.quantity)
			if got != tt.want {
				t.Errorf("CalculateCost(%d) = %d, want %d", tt.quantity, got, tt.want)
			}
		})
	}
}

func TestCalculateCost_Tiered(t *testing.T) {
	rule := &PricingRule{
		PricingType: Tiered,
		Tiers: []Tier{
			{UpTo: 100, Price: 10, Flat: 0},   // First 100: 10c each
			{UpTo: 500, Price: 5, Flat: 0},     // Next 500: 5c each
			{UpTo: 0, Price: 1, Flat: 100},     // Beyond: 1c each + $1 flat
		},
	}

	tests := []struct {
		name     string
		quantity int64
		want     int64
	}{
		{"zero", 0, 0},
		{"within first tier", 50, 500},           // 50 * 10
		{"exactly first tier", 100, 1000},         // 100 * 10
		{"into second tier", 200, 1500},           // (100*10) + (100*5)
		{"exactly two tiers", 600, 3500},          // (100*10) + (500*5)
		{"into third tier", 700, 3700},            // (100*10) + (500*5) + 100 + (100*1)
		{"large quantity", 1600, 4600},            // (100*10) + (500*5) + 100 + (1000*1) = 4600
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := rule.CalculateCost(tt.quantity)
			if got != tt.want {
				t.Errorf("CalculateCost(%d) = %d, want %d", tt.quantity, got, tt.want)
			}
		})
	}
}

func TestCalculateCost_TieredWithFlatFees(t *testing.T) {
	rule := &PricingRule{
		PricingType: Tiered,
		Tiers: []Tier{
			{UpTo: 10, Price: 0, Flat: 500},   // Flat $5 for first 10
			{UpTo: 0, Price: 2, Flat: 1000},    // $10 flat + 2c each after
		},
	}

	tests := []struct {
		name     string
		quantity int64
		want     int64
	}{
		{"within flat tier", 5, 500},              // flat 500 + 5*0
		{"exactly flat tier", 10, 500},             // flat 500 + 10*0
		{"into metered tier", 15, 1510},            // 500 + (1000 + 5*2)
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := rule.CalculateCost(tt.quantity)
			if got != tt.want {
				t.Errorf("CalculateCost(%d) = %d, want %d", tt.quantity, got, tt.want)
			}
		})
	}
}

func TestCalculateCost_Volume(t *testing.T) {
	rule := &PricingRule{
		PricingType: Volume,
		Tiers: []Tier{
			{UpTo: 100, Price: 10, Flat: 0},   // Up to 100: 10c each
			{UpTo: 1000, Price: 5, Flat: 0},    // 101-1000: 5c each (ALL units)
			{UpTo: 0, Price: 1, Flat: 500},     // 1001+: 1c each + $5 flat
		},
	}

	tests := []struct {
		name     string
		quantity int64
		want     int64
	}{
		{"zero", 0, 0},                            // Tier 1: 0 * 10 = 0
		{"within first tier", 50, 500},            // 50 * 10
		{"exactly first tier", 100, 1000},         // 100 * 10
		{"into second tier", 200, 1000},           // 200 * 5 (volume pricing)
		{"into third tier", 1500, 2000},           // 500 + 1500*1
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := rule.CalculateCost(tt.quantity)
			if got != tt.want {
				t.Errorf("CalculateCost(%d) = %d, want %d", tt.quantity, got, tt.want)
			}
		})
	}
}

func TestCalculateCost_EmptyTiers(t *testing.T) {
	rule := &PricingRule{
		PricingType: Tiered,
		Tiers: []Tier{},
	}

	got := rule.CalculateCost(100)
	if got != 0 {
		t.Errorf("CalculateCost with empty tiers = %d, want 0", got)
	}
}

func TestCalculateCost_VolumeEmptyTiers(t *testing.T) {
	rule := &PricingRule{
		PricingType: Volume,
		Tiers: []Tier{},
	}

	got := rule.CalculateCost(100)
	if got != 0 {
		t.Errorf("CalculateCost volume with empty tiers = %d, want 0", got)
	}
}

func TestCalculateCost_UnknownModel(t *testing.T) {
	rule := &PricingRule{
		PricingType: "unknown",
		UnitPrice: 3,
	}

	// Unknown model falls back to per-unit
	got := rule.CalculateCost(10)
	if got != 30 {
		t.Errorf("CalculateCost unknown model = %d, want 30", got)
	}
}

func TestCalculateCost_SingleInfinityTier(t *testing.T) {
	rule := &PricingRule{
		PricingType: Tiered,
		Tiers: []Tier{
			{UpTo: 0, Price: 7, Flat: 100}, // All units: 7c + $1 flat
		},
	}

	got := rule.CalculateCost(50)
	// 100 + 50*7 = 450
	if got != 450 {
		t.Errorf("CalculateCost single infinity tier = %d, want 450", got)
	}
}
