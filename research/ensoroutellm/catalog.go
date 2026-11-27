// Copyright 2026 Hanzo AI, Inc.
// SPDX-License-Identifier: Apache-2.0 OR MIT

// Package ensoroutellm implements a clean-room, dependency-free Go cost-quality
// learned router and capability-gate harness for hanzoai/cloud#34 and #27
// ("[Bounty #17] Quality-constrained model routing · cloud / Enso (RouteLLM)",
// tracked under HIP-1345 §2.6, HIP-0510, and HIP-1332).
//
// Upstream reference (paper & algorithmic formulation only; zero upstream
// Python code, Meta Llama-3 checkpoints, OpenAI embeddings, or Chatbot Arena
// datasets imported):
//   - I. Ong, A. Almahairi, V. Wu, W.-L. Chiang, T. Wu, J. E. Gonzalez,
//     M. W. Kadous, I. Stoica, "RouteLLM: Learning to Route LLMs with
//     Preference Data," ICLR 2025 (arXiv:2406.18665).
//     Repository: https://github.com/lm-sys/RouteLLM (Apache-2.0 code; external
//     datasets/weights excluded per license audit in README.md §1).
//
// Canonical Hanzo architecture preserved:
//   - Deterministic hard policy & capability gates execute first and can never
//     be loosened by learned scores (HIP-1332 §10, HIP-1345 §2.6).
//   - Enso/Kai routing emits structured non-generative RouteDecision packages
//     in CPU microseconds with zero third-party embedding egress (HIP-0510,
//     HIP-1332 §20).
//   - One-model-per-turn reasoning continuity and cross-family thinking-block
//     sanitization hold across multi-turn sessions (HIP-1345 §2.6).
//
// Standard library only; CGO_ENABLED=0 compatible; zero changes to go.mod.
package ensoroutellm

import (
	"fmt"
	"math"
	"sort"
)

// PlanTier orders multi-tenant subscription plans from lowest to highest
// entitlement (free < starter < pro < team < enterprise).
type PlanTier int

const (
	PlanFree PlanTier = iota
	PlanStarter
	PlanPro
	PlanTeam
	PlanEnterprise
)

func (p PlanTier) String() string {
	switch p {
	case PlanFree:
		return "free"
	case PlanStarter:
		return "starter"
	case PlanPro:
		return "pro"
	case PlanTeam:
		return "team"
	case PlanEnterprise:
		return "enterprise"
	default:
		return fmt.Sprintf("plan(%d)", int(p))
	}
}

// ParsePlanTier converts a canonical plan name into its ordered PlanTier.
func ParsePlanTier(s string) (PlanTier, error) {
	switch s {
	case "free":
		return PlanFree, nil
	case "starter":
		return PlanStarter, nil
	case "pro":
		return PlanPro, nil
	case "team":
		return PlanTeam, nil
	case "enterprise":
		return PlanEnterprise, nil
	default:
		return PlanFree, fmt.Errorf("unknown plan tier %q", s)
	}
}

// ToolChoiceMode distinguishes unforced ("none", "auto") from forced
// ("required", "named_function") tool invocation contracts (HIP-1345 §2.6).
type ToolChoiceMode string

const (
	ToolChoiceNone          ToolChoiceMode = "none"
	ToolChoiceAuto          ToolChoiceMode = "auto"
	ToolChoiceRequired      ToolChoiceMode = "required"
	ToolChoiceNamedFunction ToolChoiceMode = "named_function"
)

// IsForced reports whether the caller requires guaranteed tool-call emission.
func (t ToolChoiceMode) IsForced() bool {
	return t == ToolChoiceRequired || t == ToolChoiceNamedFunction
}

// IsValid reports whether t is a recognized ToolChoiceMode (empty defaults to none).
func (t ToolChoiceMode) IsValid() bool {
	switch t {
	case "", ToolChoiceNone, ToolChoiceAuto, ToolChoiceRequired, ToolChoiceNamedFunction:
		return true
	default:
		return false
	}
}

// ThinkingMode specifies reasoning-token requirements on a generation turn.
type ThinkingMode string

const (
	ThinkingOff       ThinkingMode = "off"
	ThinkingAdaptive  ThinkingMode = "adaptive"
	ThinkingMandatory ThinkingMode = "mandatory"
)

// IsValid reports whether m is a recognized ThinkingMode (empty leaves thinking unconstrained).
func (m ThinkingMode) IsValid() bool {
	switch m {
	case "", ThinkingOff, ThinkingAdaptive, ThinkingMandatory:
		return true
	default:
		return false
	}
}

// ModelArm describes one servable LLM backend in the Enso routing pool,
// including pricing, latency, capability flags, retention terms, and HIP-1345
// §1.3 runtime provenance identifiers.
type ModelArm struct {
	ID                       string   `json:"id"`
	Provider                 string   `json:"provider"`
	InputUSDPerMTok          float64  `json:"input_usd_per_mtok"`
	OutputUSDPerMTok         float64  `json:"output_usd_per_mtok"`
	PriceSource              string   `json:"price_source,omitempty"`
	ZeroRetention            bool     `json:"zero_retention"`
	RetentionDays            int      `json:"retention_days"`
	ThinkingFamily           string   `json:"thinking_family"`
	MandatoryThinking        bool     `json:"mandatory_thinking"`
	SupportsForcedToolChoice bool     `json:"supports_forced_tool_choice"`
	SupportsVision           bool     `json:"supports_vision,omitempty"`
	ContextWindow            int      `json:"context_window,omitempty"`
	MinPlanTier              PlanTier `json:"min_plan_tier"`
	FastModeCapable          bool     `json:"fast_mode_capable"`
	FastModeOnly             bool     `json:"fast_mode_only"`
	MedianTTFTMs             int      `json:"median_ttft_ms"`
	MedianTPOTMs             float64  `json:"median_tpot_ms"`
	RuntimeDigest            string   `json:"runtime_digest"`
	CacheConnector           string   `json:"cache_connector"`
	Fallbacks                []string `json:"fallbacks,omitempty"`
}

// BlendedUSDPerMTok returns a standard 3:1 input-to-output blended token rate
// used for summary reporting; per-request routing uses exact input/output token
// estimates via EstimateCostMicros.
func (m ModelArm) BlendedUSDPerMTok() float64 {
	return 0.75*m.InputUSDPerMTok + 0.25*m.OutputUSDPerMTok
}

// EstimateCostMicros computes the expected request spend in micro-USD (1e-6 USD),
// rounding up to at least 1 micro-USD for any positive-price token call and
// saturating at math.MaxInt64 on extreme or non-finite inputs so large integers
// can never wrap negative and bypass budget/SLO gates.
func EstimateCostMicros(arm ModelArm, inTokens, outTokens int) int64 {
	if inTokens < 0 {
		inTokens = 0
	}
	if outTokens < 0 {
		outTokens = 0
	}
	if math.IsNaN(arm.InputUSDPerMTok) || math.IsNaN(arm.OutputUSDPerMTok) ||
		math.IsInf(arm.InputUSDPerMTok, 1) || math.IsInf(arm.OutputUSDPerMTok, 1) {
		return math.MaxInt64
	}
	usd := (float64(inTokens)*arm.InputUSDPerMTok + float64(outTokens)*arm.OutputUSDPerMTok) / 1_000_000.0
	if math.IsNaN(usd) || math.IsInf(usd, 1) {
		return math.MaxInt64
	}
	if usd <= 0 {
		return 0
	}
	microsFloat := math.Ceil(usd * 1_000_000.0)
	if math.IsNaN(microsFloat) || math.IsInf(microsFloat, 1) || microsFloat >= 9.223372036854774e18 {
		return math.MaxInt64
	}
	micros := int64(microsFloat)
	if micros < 1 {
		micros = 1
	}
	return micros
}

// EstimateLatencyMs estimates end-to-end turn latency (TTFT + output decode),
// saturating at math.MaxInt32 on overflow or non-finite parameters.
func EstimateLatencyMs(arm ModelArm, outTokens int) int {
	if outTokens < 0 {
		outTokens = 0
	}
	ttft := arm.MedianTTFTMs
	if ttft < 0 {
		ttft = 0
	}
	if math.IsNaN(arm.MedianTPOTMs) || math.IsInf(arm.MedianTPOTMs, 1) {
		return math.MaxInt32
	}
	tpot := arm.MedianTPOTMs
	if tpot < 0 {
		tpot = 0
	}
	total := float64(ttft) + math.Round(float64(outTokens)*tpot)
	if math.IsNaN(total) || math.IsInf(total, 1) || total >= float64(math.MaxInt32) {
		return math.MaxInt32
	}
	if total < 0 {
		return 0
	}
	return int(total)
}

// DefaultCatalog returns a synthetic 10-arm unit-test fixture catalog used
// strictly for deterministic unit tests of capability gates, policy folding,
// and offline synthetic fixtures. Production and acceptance benchmarks MUST
// consume canonical hanzoai/ai conf/models.yaml via LoadHanzoModelsYAML +
// BuildCatalogArms rather than this synthetic fixture.
func DefaultCatalog() []ModelArm {
	return []ModelArm{
		{
			ID:                       "zen-4-flash",
			Provider:                 "hanzo-zen",
			InputUSDPerMTok:          0.15,
			OutputUSDPerMTok:         0.60,
			ZeroRetention:            true,
			RetentionDays:            0,
			ThinkingFamily:           "zen-v4",
			SupportsForcedToolChoice: true,
			MinPlanTier:              PlanFree,
			FastModeCapable:          true,
			MedianTTFTMs:             55,
			MedianTPOTMs:             5.5,
			RuntimeDigest:            "sha256:8f01a2c4e9b1d3047712a90c11f42e88b503c91a7740e2d11b8c6042a1900001",
			CacheConnector:           "hanzo-kv-fabric/v2",
		},
		{
			ID:                       "ext-oss-70b-30d",
			Provider:                 "ext-batchcloud",
			InputUSDPerMTok:          0.08,
			OutputUSDPerMTok:         0.30,
			ZeroRetention:            false,
			RetentionDays:            30,
			ThinkingFamily:           "",
			SupportsForcedToolChoice: false,
			MinPlanTier:              PlanFree,
			MedianTTFTMs:             125,
			MedianTPOTMs:             13.0,
			RuntimeDigest:            "sha256:8f01a2c4e9b1d3047712a90c11f42e88b503c91a7740e2d11b8c6042a1900002",
			CacheConnector:           "upstream-ephemeral/v1",
		},
		{
			ID:                       "zen-4-mini",
			Provider:                 "hanzo-zen",
			InputUSDPerMTok:          0.45,
			OutputUSDPerMTok:         1.80,
			ZeroRetention:            true,
			RetentionDays:            0,
			ThinkingFamily:           "zen-v4",
			SupportsForcedToolChoice: true,
			MinPlanTier:              PlanFree,
			FastModeCapable:          true,
			MedianTTFTMs:             85,
			MedianTPOTMs:             8.0,
			RuntimeDigest:            "sha256:8f01a2c4e9b1d3047712a90c11f42e88b503c91a7740e2d11b8c6042a1900003",
			CacheConnector:           "hanzo-kv-fabric/v2",
		},
		{
			ID:                       "zen-4-coder",
			Provider:                 "hanzo-zen",
			InputUSDPerMTok:          1.20,
			OutputUSDPerMTok:         4.80,
			ZeroRetention:            true,
			RetentionDays:            0,
			ThinkingFamily:           "zen-v4",
			SupportsForcedToolChoice: true,
			MinPlanTier:              PlanStarter,
			MedianTTFTMs:             135,
			MedianTPOTMs:             11.5,
			RuntimeDigest:            "sha256:8f01a2c4e9b1d3047712a90c11f42e88b503c91a7740e2d11b8c6042a1900004",
			CacheConnector:           "hanzo-kv-fabric/v2",
		},
		{
			ID:                       "zen-4-pro",
			Provider:                 "hanzo-zen",
			InputUSDPerMTok:          3.00,
			OutputUSDPerMTok:         12.00,
			ZeroRetention:            true,
			RetentionDays:            0,
			ThinkingFamily:           "zen-v4",
			SupportsForcedToolChoice: true,
			MinPlanTier:              PlanPro,
			MedianTTFTMs:             195,
			MedianTPOTMs:             16.0,
			RuntimeDigest:            "sha256:8f01a2c4e9b1d3047712a90c11f42e88b503c91a7740e2d11b8c6042a1900005",
			CacheConnector:           "hanzo-kv-fabric/v2",
		},
		{
			ID:                       "frontier-claude-sonnet",
			Provider:                 "anthropic-zdr",
			InputUSDPerMTok:          3.50,
			OutputUSDPerMTok:         15.00,
			ZeroRetention:            true,
			RetentionDays:            0,
			ThinkingFamily:           "anthropic-extended",
			SupportsForcedToolChoice: true,
			MinPlanTier:              PlanPro,
			MedianTTFTMs:             245,
			MedianTPOTMs:             19.0,
			RuntimeDigest:            "sha256:8f01a2c4e9b1d3047712a90c11f42e88b503c91a7740e2d11b8c6042a1900006",
			CacheConnector:           "provider-zdr-passthrough/v1",
		},
		{
			ID:                       "zen-4-thinking",
			Provider:                 "hanzo-zen",
			InputUSDPerMTok:          4.50,
			OutputUSDPerMTok:         18.00,
			ZeroRetention:            true,
			RetentionDays:            0,
			ThinkingFamily:           "zen-v4-reasoner",
			MandatoryThinking:        true,
			SupportsForcedToolChoice: false,
			MinPlanTier:              PlanPro,
			MedianTTFTMs:             380,
			MedianTPOTMs:             21.0,
			RuntimeDigest:            "sha256:8f01a2c4e9b1d3047712a90c11f42e88b503c91a7740e2d11b8c6042a1900007",
			CacheConnector:           "hanzo-kv-fabric/v2",
		},
		{
			ID:                       "zen-4-pro-fast",
			Provider:                 "hanzo-zen",
			InputUSDPerMTok:          6.00,
			OutputUSDPerMTok:         24.00,
			ZeroRetention:            true,
			RetentionDays:            0,
			ThinkingFamily:           "zen-v4",
			SupportsForcedToolChoice: true,
			MinPlanTier:              PlanPro,
			FastModeCapable:          true,
			FastModeOnly:             true,
			MedianTTFTMs:             35,
			MedianTPOTMs:             3.8,
			RuntimeDigest:            "sha256:8f01a2c4e9b1d3047712a90c11f42e88b503c91a7740e2d11b8c6042a1900008",
			CacheConnector:           "hanzo-kv-fabric/v2",
		},
		{
			ID:                       "frontier-o-reasoner-30d",
			Provider:                 "openai-standard",
			InputUSDPerMTok:          3.20,
			OutputUSDPerMTok:         12.80,
			ZeroRetention:            false,
			RetentionDays:            30,
			ThinkingFamily:           "openai-reasoning",
			SupportsForcedToolChoice: true,
			MinPlanTier:              PlanTeam,
			MedianTTFTMs:             490,
			MedianTPOTMs:             24.0,
			RuntimeDigest:            "sha256:8f01a2c4e9b1d3047712a90c11f42e88b503c91a7740e2d11b8c6042a1900009",
			CacheConnector:           "upstream-ephemeral/v1",
		},
		{
			ID:                       "zen-4-max",
			Provider:                 "hanzo-zen",
			InputUSDPerMTok:          12.00,
			OutputUSDPerMTok:         48.00,
			ZeroRetention:            true,
			RetentionDays:            0,
			ThinkingFamily:           "zen-v4",
			SupportsForcedToolChoice: true,
			MinPlanTier:              PlanEnterprise,
			FastModeCapable:          true,
			MedianTTFTMs:             290,
			MedianTPOTMs:             23.0,
			RuntimeDigest:            "sha256:8f01a2c4e9b1d3047712a90c11f42e88b503c91a7740e2d11b8c6042a1900010",
			CacheConnector:           "hanzo-kv-fabric/v2",
		},
	}
}

// ScopePolicy defines platform ("*"), per-org ("org"), or per-project
// ("org/project") routing constraints (HIP-0510 §1 & §7).
//
// Precedence: Org establishes the tenant's commercial PlanTier and EntitledModels
// over platform defaults, while all retention/SLO/block constraints narrow
// monotonically across platform -> org -> project (and project can NEVER
// escalate PlanTier above org or unblock a model/provider forbidden upstream).
type ScopePolicy struct {
	PlanTier             PlanTier `json:"plan_tier"`
	HasPlanTier          bool     `json:"has_plan_tier"`
	RequireZeroRetention bool     `json:"require_zero_retention"`
	MaxCostMicros        int64    `json:"max_cost_micros,omitempty"`
	MaxLatencyMs         int      `json:"max_latency_ms,omitempty"`
	MinQualityFloor      float64  `json:"min_quality_floor,omitempty"`
	EntitledModels       []string `json:"entitled_models,omitempty"`
	AllowedModels        []string `json:"allowed_models,omitempty"`
	BlockedModels        []string `json:"blocked_models,omitempty"`
	BlockedProviders     []string `json:"blocked_providers,omitempty"`
	TrainingOptIn        bool     `json:"training_opt_in,omitempty"`
}

func (p ScopePolicy) Clone() ScopePolicy {
	out := p
	out.EntitledModels = append([]string(nil), p.EntitledModels...)
	out.AllowedModels = append([]string(nil), p.AllowedModels...)
	out.BlockedModels = append([]string(nil), p.BlockedModels...)
	out.BlockedProviders = append([]string(nil), p.BlockedProviders...)
	return out
}

// EffectivePolicy is the resolved, immutable view evaluated by CheckCapabilityGates.
type EffectivePolicy struct {
	Org                  string
	Project              string
	PlanTier             PlanTier
	RequireZeroRetention bool
	MaxCostMicros        int64
	MaxLatencyMs         int
	MinQualityFloor      float64
	EntitledModels       map[string]bool
	AllowedModels        map[string]bool
	BlockedModels        map[string]bool
	BlockedProviders     map[string]bool
	TrainingOptIn        bool
}

// FoldPolicies resolves platform -> org -> project in narrow-only order:
//   - Org sets the tenant's authoritative PlanTier and EntitledModels grant set.
//   - Project (if present) MAY narrow PlanTier downward or intersect AllowedModels,
//     NEVER raise PlanTier above Org or grant EntitledModels not granted to Org.
//   - RequireZeroRetention is monotonic OR (once enabled at platform or org, child
//     scopes cannot disable ZDR).
//   - MaxCostMicros and MaxLatencyMs take the stricter positive minimum.
//   - MinQualityFloor takes the stricter maximum.
//   - BlockedModels and BlockedProviders take the union across all scopes.
func FoldPolicies(org, project string, platform *ScopePolicy, orgPol *ScopePolicy, projPol *ScopePolicy) EffectivePolicy {
	eff := EffectivePolicy{
		Org:              org,
		Project:          project,
		PlanTier:         PlanFree,
		EntitledModels:   make(map[string]bool),
		AllowedModels:    make(map[string]bool),
		BlockedModels:    make(map[string]bool),
		BlockedProviders: make(map[string]bool),
	}

	applyCommonNarrowing := func(sp *ScopePolicy) {
		if sp == nil {
			return
		}
		if sp.RequireZeroRetention {
			eff.RequireZeroRetention = true
		}
		if sp.MaxCostMicros > 0 && (eff.MaxCostMicros == 0 || sp.MaxCostMicros < eff.MaxCostMicros) {
			eff.MaxCostMicros = sp.MaxCostMicros
		}
		if sp.MaxLatencyMs > 0 && (eff.MaxLatencyMs == 0 || sp.MaxLatencyMs < eff.MaxLatencyMs) {
			eff.MaxLatencyMs = sp.MaxLatencyMs
		}
		if !math.IsNaN(sp.MinQualityFloor) && !math.IsInf(sp.MinQualityFloor, 0) && sp.MinQualityFloor > 0 {
			clamped := sp.MinQualityFloor
			if clamped > 1.0 {
				clamped = 1.0
			}
			if clamped > eff.MinQualityFloor {
				eff.MinQualityFloor = clamped
			}
		}
		for _, m := range sp.BlockedModels {
			eff.BlockedModels[m] = true
		}
		for _, prov := range sp.BlockedProviders {
			eff.BlockedProviders[prov] = true
		}
		if len(sp.AllowedModels) > 0 {
			next := make(map[string]bool, len(sp.AllowedModels))
			for _, m := range sp.AllowedModels {
				if len(eff.AllowedModels) == 0 || eff.AllowedModels[m] {
					next[m] = true
				}
			}
			// If intersection became empty, preserve a sentinel so nothing matches!
			if len(next) == 0 {
				next["__empty_intersection__"] = true
			}
			eff.AllowedModels = next
		}
	}

	if platform != nil {
		if platform.HasPlanTier {
			eff.PlanTier = platform.PlanTier
		}
		for _, m := range platform.EntitledModels {
			eff.EntitledModels[m] = true
		}
		applyCommonNarrowing(platform)
	}

	if orgPol != nil {
		// Org row establishes the tenant's commercial subscription tier and grants.
		if orgPol.HasPlanTier {
			eff.PlanTier = orgPol.PlanTier
		}
		for _, m := range orgPol.EntitledModels {
			if !eff.BlockedModels[m] {
				eff.EntitledModels[m] = true
			}
		}
		eff.TrainingOptIn = orgPol.TrainingOptIn
		applyCommonNarrowing(orgPol)
	}

	if projPol != nil {
		// Project sub-scope can ONLY narrow org entitlements, never escalate!
		if projPol.HasPlanTier && projPol.PlanTier < eff.PlanTier {
			eff.PlanTier = projPol.PlanTier
		}
		if len(projPol.EntitledModels) > 0 {
			narrowGrants := make(map[string]bool)
			for _, m := range projPol.EntitledModels {
				if eff.EntitledModels[m] {
					narrowGrants[m] = true
				}
			}
			eff.EntitledModels = narrowGrants
		}
		applyCommonNarrowing(projPol)
	}

	// Ensure no model blocked at any scope remains visible in EntitledModels.
	for blocked := range eff.BlockedModels {
		delete(eff.EntitledModels, blocked)
	}

	return eff
}

// SortedStrings returns a sorted copy of keys with value true.
func SortedStrings(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k, v := range m {
		if v && k != "__empty_intersection__" {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// RNG is a deterministic SplitMix64 generator ensuring bit-identical synthetic
// benchmark corpora, CV folds, and tie-breaks across OS/CPU architectures.
type RNG struct {
	state uint64
}

// NewRNG initializes a SplitMix64 PRNG from seed.
func NewRNG(seed uint64) *RNG {
	return &RNG{state: seed}
}

// Uint64 advances the state and returns a 64-bit random value.
func (r *RNG) Uint64() uint64 {
	r.state += 0x9e3779b97f4a7c15
	z := r.state
	z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
	z = (z ^ (z >> 27)) * 0x94d049bb133111eb
	return z ^ (z >> 31)
}

// IntN returns a uniform integer in [0, n).
func (r *RNG) IntN(n int) int {
	if n <= 0 {
		panic("ensoroutellm: IntN called with n <= 0")
	}
	return int(r.Uint64() % uint64(n))
}

// Float64 returns a uniform float64 in [0.0, 1.0).
func (r *RNG) Float64() float64 {
	return float64(r.Uint64()>>11) / (1 << 53)
}

// NormFloat64 returns a deterministic standard normal N(0, 1) sample via Box-Muller.
func (r *RNG) NormFloat64() float64 {
	u1 := r.Float64()
	if u1 < 1e-12 {
		u1 = 1e-12
	}
	u2 := r.Float64()
	return math.Sqrt(-2.0*math.Log(u1)) * math.Cos(2.0*math.Pi*u2)
}

// Shuffle deterministically permutes n items in place.
func (r *RNG) Shuffle(n int, swap func(i, j int)) {
	for i := n - 1; i > 0; i-- {
		j := r.IntN(i + 1)
		swap(i, j)
	}
}
