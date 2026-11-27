// Copyright 2026 Hanzo AI, Inc.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package ensoroutellm

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
)

func sha256LFNormalized(raw []byte) string {
	norm := bytes.ReplaceAll(raw, []byte("\r\n"), []byte("\n"))
	sum := sha256.Sum256(norm)
	return hex.EncodeToString(sum[:])
}

// HanzoPriceDef mirrors hanzoai/ai/controllers.ModelPriceDef (USD per 1M tokens).
type HanzoPriceDef struct {
	InputPerMillion   float64 `json:"input_per_million"`
	OutputPerMillion  float64 `json:"output_per_million"`
	CostInPerMillion  float64 `json:"cost_in_per_million,omitempty"`
	CostOutPerMillion float64 `json:"cost_out_per_million,omitempty"`
	Explicit          bool    `json:"-"`
}

// HanzoRouterConfig mirrors hanzoai/ai/controllers.RouterConfigDef from conf/models.yaml.
type HanzoRouterConfig struct {
	Enabled     bool                `json:"enabled"`
	Endpoint    string              `json:"endpoint,omitempty"`
	CostCeiling float64             `json:"cost_ceiling,omitempty"`
	Prefer      map[string][]string `json:"prefer"`
}

// HanzoFallbackDef mirrors hanzoai/ai/controllers.FallbackDef.
type HanzoFallbackDef struct {
	Provider      string `json:"provider"`
	Upstream      string `json:"upstream"`
	ContextWindow int    `json:"context_window,omitempty"`
	OpenerEaten   bool   `json:"opener_eaten,omitempty"`
}

// HanzoModelDef mirrors hanzoai/ai/controllers.ModelDef from conf/models.yaml.
type HanzoModelDef struct {
	ID              string             `json:"id"`
	Provider        string             `json:"provider"`
	Upstream        string             `json:"upstream"`
	OwnedBy         string             `json:"owned_by,omitempty"`
	AliasOf         string             `json:"alias_of,omitempty"`
	AliasPricing    string             `json:"alias_pricing,omitempty"`
	Premium         bool               `json:"premium,omitempty"`
	Hidden          bool               `json:"hidden,omitempty"`
	PricingOnly     bool               `json:"pricing_only,omitempty"`
	Pricing         *HanzoPriceDef     `json:"pricing,omitempty"`
	ContextWindow   int                `json:"context_window,omitempty"`
	MaxOutputTokens int                `json:"max_output_tokens,omitempty"`
	Vision          bool               `json:"vision,omitempty"`
	Tools           bool               `json:"tools,omitempty"`
	ZeroRetention   *bool              `json:"zero_retention,omitempty"`
	Outputs         []string           `json:"outputs,omitempty"`
	Fallbacks       []HanzoFallbackDef `json:"fallbacks,omitempty"`
}

// HanzoModelsConfig is the parsed canonical representation of hanzoai/ai/conf/models.yaml.
type HanzoModelsConfig struct {
	Version        int                      `json:"version"`
	PricingURL     string                   `json:"pricing_url,omitempty"`
	DefaultPricing HanzoPriceDef            `json:"default_pricing"`
	Router         HanzoRouterConfig        `json:"router"`
	Models         map[string]HanzoModelDef `json:"models"`
	SHA256         string                   `json:"sha256"`
}

// PricingSnapshotEntry carries optional canonical rates and metadata sourced
// from hanzoai/ai (conf/models.yaml + controllers/model_pricing.go) plus
// optional estimated latency priors for offline public benchmarks.
type PricingSnapshotEntry struct {
	InputPerMillion   float64 `json:"input_per_million"`
	OutputPerMillion  float64 `json:"output_per_million"`
	Provider          string  `json:"provider,omitempty"`
	ContextWindow     int     `json:"context_window,omitempty"`
	Vision            bool    `json:"vision,omitempty"`
	Tools             bool    `json:"tools,omitempty"`
	ZeroRetention     bool    `json:"zero_retention,omitempty"`
	ThinkingFamily    string  `json:"thinking_family,omitempty"`
	MandatoryThinking bool    `json:"mandatory_thinking,omitempty"`
	FastModeCapable   bool    `json:"fast_mode_capable,omitempty"`
	Premium           bool    `json:"premium,omitempty"`
	MedianTTFTMs      int     `json:"median_ttft_ms,omitempty"`
	MedianTPOTMs      float64 `json:"median_tpot_ms,omitempty"`
}

// PricingSnapshot is the JSON snapshot consumed alongside conf/models.yaml.
type PricingSnapshot struct {
	SourceRepo          string                          `json:"source_repo"`
	SourceCommit        string                          `json:"source_commit"`
	PricingSourceFiles  []string                        `json:"pricing_source_files,omitempty"`
	EstimatedPriorsNote string                          `json:"estimated_priors_note,omitempty"`
	Unit                string                          `json:"unit"`
	Models              map[string]PricingSnapshotEntry `json:"models"`
	SHA256              string                          `json:"sha256,omitempty"`
}

// LoadPricingSnapshotFile parses and validates a canonical Hanzo pricing snapshot JSON file.
func LoadPricingSnapshotFile(path string) (*PricingSnapshot, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read pricing snapshot %s: %w", path, err)
	}
	var snap PricingSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		return nil, fmt.Errorf("parse pricing snapshot %s: %w", path, err)
	}
	for id, entry := range snap.Models {
		if err := validateNonNegativeFinite("pricing_snapshot."+id+".input_per_million", entry.InputPerMillion); err != nil {
			return nil, err
		}
		if err := validateNonNegativeFinite("pricing_snapshot."+id+".output_per_million", entry.OutputPerMillion); err != nil {
			return nil, err
		}
		if err := validateNonNegativeFinite("pricing_snapshot."+id+".median_tpot_ms", entry.MedianTPOTMs); err != nil {
			return nil, err
		}
	}
	snap.SHA256 = sha256LFNormalized(raw)
	if snap.Models == nil {
		snap.Models = make(map[string]PricingSnapshotEntry)
	}
	return &snap, nil
}

// LoadHanzoModelsYAMLFile reads and parses a canonical hanzoai/ai conf/models.yaml file.
func LoadHanzoModelsYAMLFile(path string) (*HanzoModelsConfig, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read models.yaml %s: %w", path, err)
	}
	return LoadHanzoModelsYAML(raw)
}

// LoadHanzoModelsYAML parses the canonical hanzoai/ai/conf/models.yaml schema
// using Go stdlib only (zero go.mod dependency changes). Supports comments,
// quoted keys, flow maps ({input: 0.80, output: 4.00}), flow and block lists
// for router.prefer and outputs, indented fallback sequences, and alias_pricing
// resolution. Rejects empty, malformed, negative, or non-finite numeric fields.
func LoadHanzoModelsYAML(raw []byte) (*HanzoModelsConfig, error) {
	cfg := &HanzoModelsConfig{
		DefaultPricing: HanzoPriceDef{InputPerMillion: 1.00, OutputPerMillion: 4.00},
		Router: HanzoRouterConfig{
			Prefer: make(map[string][]string),
		},
		Models: make(map[string]HanzoModelDef),
		SHA256: sha256LFNormalized(raw),
	}

	section := ""
	subSection := ""
	routerFieldIndent := -1
	preferTaskKey := ""
	preferTaskIndent := -1
	currentModelID := ""
	modelIndent := -1
	fieldIndent := -1
	modelSubSection := "" // "fallbacks" | "pricing" | "outputs"
	sawValidSection := false

	lines := strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n")
	for lineNo, rawLine := range lines {
		cleaned := stripYAMLComment(rawLine)
		if strings.TrimSpace(cleaned) == "" {
			continue
		}
		indent := countLeadingSpaces(cleaned)
		trimmed := strings.TrimSpace(cleaned)

		if indent == 0 {
			if trimmed == "---" || trimmed == "..." {
				continue
			}
			subSection = ""
			routerFieldIndent = -1
			preferTaskKey = ""
			preferTaskIndent = -1
			currentModelID = ""
			modelIndent = -1
			fieldIndent = -1
			modelSubSection = ""

			k, v, ok := splitYAMLKV(trimmed)
			if !ok {
				return nil, fmt.Errorf("models.yaml line %d: invalid top-level entry %q", lineNo+1, trimmed)
			}
			k = unquoteYAML(k)
			section = k
			switch k {
			case "version":
				sawValidSection = true
				if v != "" {
					n, err := strconv.Atoi(unquoteYAML(v))
					if err != nil {
						return nil, fmt.Errorf("models.yaml line %d: invalid version %q: %w", lineNo+1, v, err)
					}
					cfg.Version = n
				}
			case "services", "cache", "features", "retry", "router", "default_pricing", "models":
				sawValidSection = true
			}
			continue
		}

		switch section {
		case "services":
			k, v, ok := splitYAMLKV(trimmed)
			if ok && unquoteYAML(k) == "pricing_url" {
				cfg.PricingURL = unquoteYAML(v)
			}

		case "default_pricing":
			k, v, ok := splitYAMLKV(trimmed)
			if !ok {
				continue
			}
			val, err := parseNonNegativeYAMLFloat(v, fmt.Sprintf("models.yaml line %d: default_pricing.%s", lineNo+1, k))
			if err != nil {
				return nil, err
			}
			switch unquoteYAML(k) {
			case "input_per_million", "input":
				cfg.DefaultPricing.InputPerMillion = val
			case "output_per_million", "output":
				cfg.DefaultPricing.OutputPerMillion = val
			}

		case "router":
			if routerFieldIndent < 0 {
				routerFieldIndent = indent
			}
			// Support both indented and same-indent YAML block lists under router.prefer.<task>.
			if subSection == "prefer" && preferTaskKey != "" && indent >= preferTaskIndent && strings.HasPrefix(trimmed, "- ") {
				if item := unquoteYAML(strings.TrimSpace(trimmed[2:])); item != "" {
					cfg.Router.Prefer[preferTaskKey] = append(cfg.Router.Prefer[preferTaskKey], item)
				}
				continue
			}
			if subSection == "prefer" && preferTaskIndent >= 0 && indent <= preferTaskIndent && !strings.HasPrefix(trimmed, "- ") {
				preferTaskKey = ""
				preferTaskIndent = -1
			}
			if indent == routerFieldIndent {
				k, v, ok := splitYAMLKV(trimmed)
				if !ok {
					continue
				}
				k = unquoteYAML(k)
				subSection = k
				preferTaskKey = ""
				preferTaskIndent = -1
				switch k {
				case "enabled":
					cfg.Router.Enabled = parseYAMLBool(v)
				case "endpoint":
					cfg.Router.Endpoint = unquoteYAML(v)
				case "cost_ceiling":
					if v != "" {
						f, err := parseNonNegativeYAMLFloat(v, fmt.Sprintf("models.yaml line %d: router.cost_ceiling", lineNo+1))
						if err != nil {
							return nil, err
						}
						cfg.Router.CostCeiling = f
					}
				}
			} else if indent > routerFieldIndent && subSection == "prefer" {
				k, v, ok := splitYAMLKV(trimmed)
				if !ok {
					continue
				}
				taskKey := unquoteYAML(k)
				if strings.HasPrefix(v, "[") && strings.HasSuffix(v, "]") {
					cfg.Router.Prefer[taskKey] = parseYAMLInlineList(v)
					preferTaskKey = ""
					preferTaskIndent = -1
				} else if v == "" {
					preferTaskKey = taskKey
					preferTaskIndent = indent
					cfg.Router.Prefer[taskKey] = nil
				}
			}

		case "models":
			if modelIndent < 0 {
				modelIndent = indent
			}
			if indent == modelIndent {
				k, _, ok := splitYAMLKV(trimmed)
				if !ok {
					return nil, fmt.Errorf("models.yaml line %d: invalid model key %q", lineNo+1, trimmed)
				}
				currentModelID = unquoteYAML(k)
				fieldIndent = -1
				modelSubSection = ""
				if _, exists := cfg.Models[currentModelID]; !exists {
					cfg.Models[currentModelID] = HanzoModelDef{ID: currentModelID}
				}
				continue
			}
			if currentModelID == "" {
				continue
			}
			if fieldIndent < 0 {
				fieldIndent = indent
			}
			def := cfg.Models[currentModelID]

			// Handle sequence items for fallbacks (either at fieldIndent or indented under fallbacks).
			if modelSubSection == "fallbacks" && indent >= fieldIndent && strings.HasPrefix(trimmed, "- ") {
				rest := strings.TrimSpace(trimmed[2:])
				fb := HanzoFallbackDef{}
				if k, v, ok := splitYAMLKV(rest); ok {
					if err := applyFallbackField(&fb, unquoteYAML(k), v, fmt.Sprintf("models.yaml line %d (%s)", lineNo+1, currentModelID)); err != nil {
						return nil, err
					}
				}
				def.Fallbacks = append(def.Fallbacks, fb)
				cfg.Models[currentModelID] = def
				continue
			}
			if modelSubSection == "fallbacks" && indent > fieldIndent && len(def.Fallbacks) > 0 {
				if k, v, ok := splitYAMLKV(trimmed); ok {
					if err := applyFallbackField(&def.Fallbacks[len(def.Fallbacks)-1], unquoteYAML(k), v, fmt.Sprintf("models.yaml line %d (%s)", lineNo+1, currentModelID)); err != nil {
						return nil, err
					}
					cfg.Models[currentModelID] = def
				}
				continue
			}
			// Handle block-style outputs list (- decision).
			if modelSubSection == "outputs" && indent >= fieldIndent && strings.HasPrefix(trimmed, "- ") {
				if item := unquoteYAML(strings.TrimSpace(trimmed[2:])); item != "" {
					def.Outputs = append(def.Outputs, item)
					cfg.Models[currentModelID] = def
				}
				continue
			}
			// Handle block-style pricing map. Explicit is set strictly by applyPriceField
			// when customer input/output rates are present (never by empty or COGS-only blocks).
			if modelSubSection == "pricing" && indent > fieldIndent {
				if k, v, ok := splitYAMLKV(trimmed); ok {
					if def.Pricing == nil {
						def.Pricing = &HanzoPriceDef{}
					}
					if err := applyPriceField(def.Pricing, unquoteYAML(k), v, fmt.Sprintf("models.yaml line %d (%s)", lineNo+1, currentModelID)); err != nil {
						return nil, err
					}
					cfg.Models[currentModelID] = def
				}
				continue
			}

			if indent == fieldIndent {
				modelSubSection = ""
				k, v, ok := splitYAMLKV(trimmed)
				if !ok {
					continue
				}
				k = unquoteYAML(k)
				switch k {
				case "provider":
					def.Provider = unquoteYAML(v)
				case "upstream":
					def.Upstream = unquoteYAML(v)
				case "owned_by":
					def.OwnedBy = unquoteYAML(v)
				case "alias_of":
					def.AliasOf = unquoteYAML(v)
				case "alias_pricing":
					def.AliasPricing = unquoteYAML(v)
				case "premium":
					def.Premium = parseYAMLBool(v)
				case "hidden":
					def.Hidden = parseYAMLBool(v)
				case "pricing_only":
					def.PricingOnly = parseYAMLBool(v)
				case "vision":
					def.Vision = parseYAMLBool(v)
				case "tools":
					def.Tools = parseYAMLBool(v)
				case "zero_retention":
					b := parseYAMLBool(v)
					def.ZeroRetention = &b
				case "context_window":
					n, err := parseNonNegativeYAMLInt(v, fmt.Sprintf("models.yaml line %d (%s): context_window", lineNo+1, currentModelID))
					if err != nil {
						return nil, err
					}
					def.ContextWindow = n
				case "max_output_tokens":
					n, err := parseNonNegativeYAMLInt(v, fmt.Sprintf("models.yaml line %d (%s): max_output_tokens", lineNo+1, currentModelID))
					if err != nil {
						return nil, err
					}
					def.MaxOutputTokens = n
				case "outputs":
					if v == "" {
						modelSubSection = "outputs"
					} else if strings.HasPrefix(v, "[") && strings.HasSuffix(v, "]") {
						def.Outputs = parseYAMLInlineList(v)
					}
				case "fallbacks":
					modelSubSection = "fallbacks"
				case "pricing":
					if v == "" {
						modelSubSection = "pricing"
						if def.Pricing == nil {
							def.Pricing = &HanzoPriceDef{}
						}
					} else if strings.HasPrefix(v, "{") && strings.HasSuffix(v, "}") {
						pd, err := parseYAMLInlinePriceMap(v, fmt.Sprintf("models.yaml line %d (%s)", lineNo+1, currentModelID))
						if err != nil {
							return nil, err
						}
						def.Pricing = pd
					} else {
						return nil, fmt.Errorf("models.yaml line %d (%s): unsupported pricing value %q", lineNo+1, currentModelID, v)
					}
				}
				cfg.Models[currentModelID] = def
			}
		}
	}

	if !sawValidSection || (len(cfg.Models) == 0 && len(cfg.Router.Prefer) == 0) {
		return nil, fmt.Errorf("models.yaml: missing canonical Hanzo model or router sections")
	}

	// Deterministic transitive alias_pricing and upstream context_window inheritance.
	sortedIDs := make([]string, 0, len(cfg.Models))
	for id := range cfg.Models {
		sortedIDs = append(sortedIDs, id)
	}
	sort.Strings(sortedIDs)
	for pass := 0; pass < len(sortedIDs); pass++ {
		lowerModels := make(map[string]HanzoModelDef, len(cfg.Models))
		for _, id := range sortedIDs {
			lowerModels[strings.ToLower(id)] = cfg.Models[id]
		}
		anyChanged := false
		for _, id := range sortedIDs {
			def := cfg.Models[id]
			changed := false
			if (def.Pricing == nil || !def.Pricing.Explicit) && def.AliasPricing != "" {
				if base, ok := lowerModels[strings.ToLower(def.AliasPricing)]; ok && base.Pricing != nil && base.Pricing.Explicit {
					cp := *base.Pricing
					def.Pricing = &cp
					changed = true
				}
			}
			if def.ContextWindow == 0 && def.Upstream != "" {
				if up, ok := lowerModels[strings.ToLower(def.Upstream)]; ok && up.ContextWindow > 0 {
					def.ContextWindow = up.ContextWindow
					changed = true
				}
			}
			if changed {
				cfg.Models[id] = def
				anyChanged = true
			}
		}
		if !anyChanged {
			break
		}
	}

	return cfg, nil
}

// IsNonChatModel reports whether a conf/models.yaml entry serves a non-chat
// modality (decisions, speech audio/transcripts, embeddings, image/video).
// Note: pricing_only chat models remain chat models when explicitly requested.
func IsNonChatModel(id string, def HanzoModelDef) bool {
	for _, o := range def.Outputs {
		switch strings.ToLower(o) {
		case "decision", "audio", "transcript":
			return true
		}
	}
	low := strings.ToLower(id)
	if strings.HasPrefix(low, "text-embedding-") || strings.Contains(low, "stable-diffusion") ||
		strings.Contains(low, "wan2-") || strings.HasPrefix(low, "bge-") ||
		strings.HasPrefix(low, "e5-") || strings.HasPrefix(low, "gte-") ||
		strings.HasPrefix(low, "all-mini-") || strings.HasPrefix(low, "multi-qa-") {
		return true
	}
	return false
}

// BuildCatalogArms constructs servable chat ModelArms for requestedIDs by consuming
// canonical conf/models.yaml first, supplementing OpenRouter/DO-AI aliases from
// optional snap, and falling back to cfg.DefaultPricing only when neither declares
// a rate. Capabilities default fail-closed (Vision=false, Tools=false,
// ZeroRetention=false) unless explicitly declared in conf/models.yaml or snap.
func BuildCatalogArms(cfg *HanzoModelsConfig, snap *PricingSnapshot, requestedIDs []string) ([]ModelArm, error) {
	if cfg == nil {
		return nil, fmt.Errorf("canonical HanzoModelsConfig is required")
	}

	yamlLower := make(map[string]HanzoModelDef, len(cfg.Models))
	for id, def := range cfg.Models {
		yamlLower[strings.ToLower(id)] = def
	}
	snapLower := make(map[string]PricingSnapshotEntry)
	if snap != nil {
		for id, entry := range snap.Models {
			snapLower[strings.ToLower(id)] = entry
		}
	}

	seenLower := make(map[string]bool)
	var ids []string
	if len(requestedIDs) > 0 {
		for _, id := range requestedIDs {
			clean := strings.TrimSpace(id)
			low := strings.ToLower(clean)
			if clean != "" && !seenLower[low] {
				seenLower[low] = true
				ids = append(ids, clean)
			}
		}
	} else {
		for id, def := range cfg.Models {
			low := strings.ToLower(id)
			if def.PricingOnly || def.Hidden || def.AliasOf != "" || strings.HasPrefix(low, "router:") || IsNonChatModel(id, def) {
				continue
			}
			if !seenLower[low] {
				seenLower[low] = true
				ids = append(ids, id)
			}
		}
		if snap != nil {
			for id := range snap.Models {
				low := strings.ToLower(id)
				if seenLower[low] {
					continue
				}
				if yDef, ok := yamlLower[low]; ok && (yDef.PricingOnly || yDef.Hidden || yDef.AliasOf != "" || strings.HasPrefix(low, "router:") || IsNonChatModel(id, yDef)) {
					continue
				}
				seenLower[low] = true
				ids = append(ids, id)
			}
		}
		sort.Strings(ids)
	}

	arms := make([]ModelArm, 0, len(ids))
	for _, id := range ids {
		low := strings.ToLower(id)
		yDef, hasYAML := yamlLower[low]
		if hasYAML && yDef.AliasOf != "" {
			if target, ok := yamlLower[strings.ToLower(yDef.AliasOf)]; ok {
				yDef = target
			}
		}
		if hasYAML && IsNonChatModel(id, yDef) {
			continue
		}
		sEntry, hasSnap := snapLower[low]

		// Fail-closed capability defaults: absence never fabricates Vision, Tools, or ZDR.
		arm := ModelArm{
			ID:                       id,
			Provider:                 "openrouter",
			InputUSDPerMTok:          cfg.DefaultPricing.InputPerMillion,
			OutputUSDPerMTok:         cfg.DefaultPricing.OutputPerMillion,
			PriceSource:              "default_pricing",
			ZeroRetention:            false,
			RetentionDays:            0,
			SupportsForcedToolChoice: false,
			SupportsVision:           false,
			MinPlanTier:              PlanFree,
			ContextWindow:            128000,
			MedianTTFTMs:             180,
			MedianTPOTMs:             14.0,
			RuntimeDigest:            canonicalRuntimeDigest(cfg.SHA256, id),
			CacheConnector:           "hanzo-ai-gateway/v1",
		}

		// Apply pricing snapshot metadata first (OpenRouter/DO-AI aliases outside models.yaml).
		if hasSnap {
			if sEntry.Provider != "" {
				arm.Provider = sEntry.Provider
			}
			arm.InputUSDPerMTok = sEntry.InputPerMillion
			arm.OutputUSDPerMTok = sEntry.OutputPerMillion
			arm.PriceSource = "pricing_snapshot"
			if sEntry.ContextWindow > 0 {
				arm.ContextWindow = sEntry.ContextWindow
			}
			arm.SupportsVision = sEntry.Vision
			arm.SupportsForcedToolChoice = sEntry.Tools
			arm.ZeroRetention = sEntry.ZeroRetention
			arm.ThinkingFamily = sEntry.ThinkingFamily
			arm.MandatoryThinking = sEntry.MandatoryThinking
			arm.FastModeCapable = sEntry.FastModeCapable
			if sEntry.Premium {
				arm.MinPlanTier = PlanPro
			}
			if sEntry.MedianTTFTMs > 0 {
				arm.MedianTTFTMs = sEntry.MedianTTFTMs
			}
			if sEntry.MedianTPOTMs > 0 {
				arm.MedianTPOTMs = sEntry.MedianTPOTMs
			}
		}

		// Canonical conf/models.yaml always takes precedence over snapshots (including explicit $0/$0 free routes).
		if hasYAML {
			if yDef.Provider != "" {
				arm.Provider = yDef.Provider
			}
			if yDef.Pricing != nil && yDef.Pricing.Explicit {
				arm.InputUSDPerMTok = yDef.Pricing.InputPerMillion
				arm.OutputUSDPerMTok = yDef.Pricing.OutputPerMillion
				arm.PriceSource = "models_yaml"
			}
			if yDef.ContextWindow > 0 {
				arm.ContextWindow = yDef.ContextWindow
			}
			if yDef.Vision {
				arm.SupportsVision = true
			}
			if yDef.Tools {
				arm.SupportsForcedToolChoice = true
			}
			if yDef.ZeroRetention != nil {
				arm.ZeroRetention = *yDef.ZeroRetention
			}
			if yDef.Premium && arm.MinPlanTier < PlanPro {
				arm.MinPlanTier = PlanPro
			}
			for _, fb := range yDef.Fallbacks {
				if fb.Provider != "" {
					arm.Fallbacks = append(arm.Fallbacks, fb.Provider+":"+fb.Upstream)
				}
			}
		}

		// Vision capability also inferred from canonical router.prefer.vision list.
		for _, vid := range cfg.Router.Prefer["vision"] {
			if strings.EqualFold(vid, id) {
				arm.SupportsVision = true
				break
			}
		}

		arms = append(arms, arm)
	}

	sort.Slice(arms, func(i, j int) bool {
		bi, bj := arms[i].BlendedUSDPerMTok(), arms[j].BlendedUSDPerMTok()
		if bi != bj {
			return bi < bj
		}
		return arms[i].ID < arms[j].ID
	})
	return arms, nil
}

func canonicalRuntimeDigest(cfgSHA, modelID string) string {
	h := sha256.Sum256([]byte("hanzo-models-yaml:" + cfgSHA + ":" + modelID))
	return "sha256:" + hex.EncodeToString(h[:])
}

func validateNonNegativeFinite(field string, v float64) error {
	if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
		return fmt.Errorf("%s must be finite and non-negative, got %v", field, v)
	}
	return nil
}

func parseNonNegativeYAMLFloat(raw, context string) (float64, error) {
	s := unquoteYAML(raw)
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("%s: invalid float %q: %w", context, raw, err)
	}
	if err := validateNonNegativeFinite(context, f); err != nil {
		return 0, err
	}
	return f, nil
}

func stripYAMLComment(line string) string {
	inSingle := false
	inDouble := false
	escaped := false
	for i := 0; i < len(line); i++ {
		c := line[i]
		if escaped {
			escaped = false
			continue
		}
		if c == '\\' && inDouble {
			escaped = true
			continue
		}
		switch c {
		case '\'':
			if !inDouble {
				inSingle = !inSingle
			}
		case '"':
			if !inSingle {
				inDouble = !inDouble
			}
		case '#':
			// YAML 1.2: comments begin at column 0 or after whitespace outside quotes.
			if !inSingle && !inDouble && (i == 0 || line[i-1] == ' ' || line[i-1] == '\t') {
				return line[:i]
			}
		}
	}
	return line
}

func countLeadingSpaces(s string) int {
	n := 0
	for n < len(s) && s[n] == ' ' {
		n++
	}
	return n
}

func splitYAMLKV(trimmed string) (string, string, bool) {
	inSingle := false
	inDouble := false
	escaped := false
	for i := 0; i < len(trimmed); i++ {
		c := trimmed[i]
		if escaped {
			escaped = false
			continue
		}
		if c == '\\' && inDouble {
			escaped = true
			continue
		}
		if c == '\'' && !inDouble {
			inSingle = !inSingle
			continue
		}
		if c == '"' && !inSingle {
			inDouble = !inDouble
			continue
		}
		if c == ':' && !inSingle && !inDouble {
			if i+1 == len(trimmed) || trimmed[i+1] == ' ' || trimmed[i+1] == '\t' {
				return strings.TrimSpace(trimmed[:i]), strings.TrimSpace(trimmed[i+1:]), true
			}
		}
	}
	return "", "", false
}

func splitYAMLFlowKV(trimmed string) (string, string, bool) {
	if k, v, ok := splitYAMLKV(trimmed); ok {
		return k, v, true
	}
	// Inside YAML flow maps ({input:0.80}), accept unquoted key:value without trailing space.
	idx := strings.IndexByte(trimmed, ':')
	if idx > 0 {
		return strings.TrimSpace(trimmed[:idx]), strings.TrimSpace(trimmed[idx+1:]), true
	}
	return "", "", false
}

func unquoteYAML(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && ((s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'')) {
		return s[1 : len(s)-1]
	}
	return s
}

func parseYAMLBool(s string) bool {
	return strings.EqualFold(unquoteYAML(s), "true")
}

func parseYAMLInlineList(s string) []string {
	inner := strings.TrimSpace(s[1 : len(s)-1])
	if inner == "" {
		return nil
	}
	parts := strings.Split(inner, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if item := unquoteYAML(p); item != "" {
			out = append(out, item)
		}
	}
	return out
}

func parseYAMLInlinePriceMap(s, ctx string) (*HanzoPriceDef, error) {
	inner := strings.TrimSpace(s[1 : len(s)-1])
	pd := &HanzoPriceDef{}
	if inner == "" {
		return pd, nil
	}
	for _, part := range strings.Split(inner, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		k, v, ok := splitYAMLFlowKV(part)
		if !ok {
			return nil, fmt.Errorf("%s: malformed inline pricing entry %q", ctx, part)
		}
		if err := applyPriceField(pd, unquoteYAML(k), v, ctx); err != nil {
			return nil, err
		}
	}
	return pd, nil
}

func parseNonNegativeYAMLInt(v, ctx string) (int, error) {
	n, err := strconv.Atoi(unquoteYAML(v))
	if err != nil || n < 0 {
		return 0, fmt.Errorf("%s: expected non-negative integer, got %q", ctx, v)
	}
	return n, nil
}

func applyPriceField(pd *HanzoPriceDef, k, v, ctx string) error {
	f, err := parseNonNegativeYAMLFloat(v, ctx+".pricing."+k)
	if err != nil {
		return err
	}
	switch k {
	case "input", "input_per_million":
		pd.InputPerMillion = f
		pd.Explicit = true
	case "output", "output_per_million":
		pd.OutputPerMillion = f
		pd.Explicit = true
	case "cost_in_per_million":
		pd.CostInPerMillion = f
	case "cost_out_per_million":
		pd.CostOutPerMillion = f
	default:
		return fmt.Errorf("%s: unknown pricing key %q", ctx, k)
	}
	return nil
}

func applyFallbackField(fb *HanzoFallbackDef, k, v, ctx string) error {
	switch k {
	case "provider":
		fb.Provider = unquoteYAML(v)
	case "upstream":
		fb.Upstream = unquoteYAML(v)
	case "context_window":
		n, err := parseNonNegativeYAMLInt(v, ctx+".fallbacks.context_window")
		if err != nil {
			return err
		}
		fb.ContextWindow = n
	case "opener_eaten":
		fb.OpenerEaten = parseYAMLBool(v)
	}
	return nil
}
