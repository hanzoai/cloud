// Package tier defines the tiered credit system for Hanzo billing.
//
// Each IAM user has a "tier" property stored in hanzo.id (Hanzo IAM) user
// properties and propagated via JWT claims. The tier determines:
//   - maximum concurrent agents
//   - which model prefixes are allowed
//   - a daily replenishing credit allowance (the generic mechanism; the
//     Free tier's allowance is 0, see below)
//
// THERE IS NO FREE TIER. A zero-balance account is gated (the metering
// client refuses when effective available balance <= 0). Onboarding funds an
// account exactly once via the starter-credit grant (a one-time $100 Deposit,
// see billing/credit); once that is spent the account is gated until it is
// topped up. The daily-credit mechanism below is retained (correctly guarded
// by DailyCreditsCents > 0) but every registered tier sets it to 0, so it
// never grants a standing balance to anyone. Paid tiers use prepaid balances
// managed by the existing billing engine.
package tier

// Name is the canonical tier identifier stored in IAM user properties.
type Name string

const (
	Free       Name = "free"
	Starter    Name = "starter"
	Pro        Name = "pro"
	Enterprise Name = "enterprise"
)

// Config describes the billing limits for a single tier.
type Config struct {
	// Name is the canonical tier identifier.
	Name Name `json:"name"`

	// DisplayName is the human-readable tier label.
	DisplayName string `json:"displayName"`

	// MaxAgents is the maximum concurrent agents allowed.
	MaxAgents int `json:"maxAgents"`

	// DailyCreditsCents is the daily replenishing credit allowance in cents.
	// It is 0 for EVERY tier: there is no free tier, so no tier grants a
	// standing daily balance. The mechanism is kept generic (a nonzero value
	// would replenish each UTC day, non-accumulating) but is disabled by
	// configuration. Prepaid balance is managed externally by the billing engine.
	DailyCreditsCents int64 `json:"dailyCreditsCents"`

	// AllowedModels lists the model prefixes the tier may invoke.
	// A single entry of "*" means all models are allowed.
	AllowedModels []string `json:"allowedModels"`
}

// registry is the authoritative tier configuration.
var registry = map[Name]*Config{
	Free: {
		Name:        Free,
		DisplayName: "Free",
		MaxAgents:   1,
		// No free tier: a zero-balance "free" account grants NO daily credit and
		// is gated. The one-time starter grant is the only onboarding funding.
		DailyCreditsCents: 0,
		AllowedModels:     []string{"claude-sonnet", "zen3"},
	},
	Starter: {
		Name:              Starter,
		DisplayName:       "Starter",
		MaxAgents:         3,
		DailyCreditsCents: 0,
		AllowedModels:     []string{"claude-sonnet", "claude-haiku", "zen3", "zen4"},
	},
	Pro: {
		Name:              Pro,
		DisplayName:       "Pro",
		MaxAgents:         10,
		DailyCreditsCents: 0,
		AllowedModels:     []string{"*"},
	},
	Enterprise: {
		Name:              Enterprise,
		DisplayName:       "Enterprise",
		MaxAgents:         0, // 0 = unlimited
		DailyCreditsCents: 0,
		AllowedModels:     []string{"*"},
	},
}

// Get returns the Config for a given tier name. Unknown tiers fall back
// to Free so callers never receive nil.
func Get(name Name) *Config {
	if c, ok := registry[name]; ok {
		return c
	}
	return registry[Free]
}

// All returns a copy of every registered tier configuration.
func All() map[Name]*Config {
	out := make(map[Name]*Config, len(registry))
	for k, v := range registry {
		out[k] = v
	}
	return out
}

// Parse normalizes a raw string into a tier Name. Empty or unknown
// strings default to Free.
func Parse(raw string) Name {
	n := Name(raw)
	if _, ok := registry[n]; ok {
		return n
	}
	return Free
}

// IsModelAllowed checks whether the given model identifier is permitted
// under the tier's allowedModels list. Matching is prefix-based: an
// entry "claude-sonnet" matches "claude-sonnet-4-20250514".
func (c *Config) IsModelAllowed(model string) bool {
	for _, prefix := range c.AllowedModels {
		if prefix == "*" {
			return true
		}
		if len(model) >= len(prefix) && model[:len(prefix)] == prefix {
			return true
		}
	}
	return false
}

// IsUnlimitedAgents returns true if MaxAgents is 0 (unlimited).
func (c *Config) IsUnlimitedAgents() bool {
	return c.MaxAgents == 0
}

// HasDailyCredits returns true if the tier receives daily replenishing credits.
func (c *Config) HasDailyCredits() bool {
	return c.DailyCreditsCents > 0
}
