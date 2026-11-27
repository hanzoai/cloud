// Copyright 2026 Hanzo AI, Inc.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package ensoroutellm

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"unicode"
)

// ErrorCode enumerates deterministic rejection reasons across Enso's policy and
// parameter validation boundary.
type ErrorCode string

const (
	ErrCodeUnauthorizedOrg      ErrorCode = "unauthorized_org"
	ErrCodeCrossTenantDenied    ErrorCode = "cross_tenant_denied"
	ErrCodeInvalidRequestParams ErrorCode = "invalid_request_parameters"
	ErrCodeBudgetExhausted      ErrorCode = "budget_exhausted"
	ErrCodeNoEligibleModel      ErrorCode = "no_eligible_model"
	ErrCodeQualityFloorUnmet    ErrorCode = "quality_floor_unachievable"
)

// GateName identifies which deterministic gate filtered out a candidate arm.
type GateName string

const (
	GateZeroRetention     GateName = "zero_retention"
	GateThinkingFamily    GateName = "thinking_family"
	GateForcedToolChoice  GateName = "forced_tool_choice"
	GateVisionCapability  GateName = "vision_capability"
	GateContextWindow     GateName = "context_window"
	GateUnavailableModel  GateName = "unavailable_model"
	GatePlanEntitlement   GateName = "plan_entitlement"
	GateFastModeCapable   GateName = "fast_mode_capability"
	GateFastModePrepaid   GateName = "fast_mode_prepaid"
	GateSLOCeiling        GateName = "slo_ceiling"
	GateRuntimeProvenance GateName = "runtime_provenance"
)

// RouteError is the structured error returned by Enso routing, feedback, and
// policy operations.
type RouteError struct {
	Code             ErrorCode           `json:"code"`
	HTTPStatus       int                 `json:"http_status"`
	Layer            string              `json:"layer,omitempty"`
	BillingTopUpHint bool                `json:"billing_top_up_hint,omitempty"`
	RejectedArms     map[string]GateName `json:"rejected_arms,omitempty"`
	Message          string              `json:"message"`
}

func (e *RouteError) Error() string {
	if e.Layer != "" {
		return fmt.Sprintf("HTTP %d %s (layer=%s): %s", e.HTTPStatus, e.Code, e.Layer, e.Message)
	}
	return fmt.Sprintf("HTTP %d %s: %s", e.HTTPStatus, e.Code, e.Message)
}

// IsRouteErrorCode checks whether err wraps a *RouteError with the target code.
func IsRouteErrorCode(err error, code ErrorCode) bool {
	var re *RouteError
	if errors.As(err, &re) {
		return re.Code == code
	}
	return false
}

// NewBudgetExhaustedRouteError constructs a HIP-1348 §1.2 compliant budget failure.
// BillingTopUpHint is true strictly when layer == "tenant_ledger".
func NewBudgetExhaustedRouteError(layer, msg string) *RouteError {
	return &RouteError{
		Code:             ErrCodeBudgetExhausted,
		HTTPStatus:       402,
		Layer:            layer,
		BillingTopUpHint: layer == "tenant_ledger",
		Message:          msg,
	}
}

// ThinkingBlock represents a model-family-bound reasoning trace block inside a
// multi-turn conversation message (HIP-1345 §2.6).
type ThinkingBlock struct {
	ThinkingFamily string `json:"thinking_family"`
	SourceModel    string `json:"source_model"`
	Signature      string `json:"signature"`
	Summary        string `json:"summary"`
}

// ConversationMessage models one message in a multi-turn agent trajectory.
type ConversationMessage struct {
	Role           string          `json:"role"`
	Content        string          `json:"content"`
	ToolCallID     string          `json:"tool_call_id,omitempty"`
	ThinkingBlocks []ThinkingBlock `json:"thinking_blocks,omitempty"`
}

func (m ConversationMessage) Clone() ConversationMessage {
	out := m
	if len(m.ThinkingBlocks) > 0 {
		out.ThinkingBlocks = make([]ThinkingBlock, len(m.ThinkingBlocks))
		copy(out.ThinkingBlocks, m.ThinkingBlocks)
	} else {
		out.ThinkingBlocks = nil
	}
	return out
}

// SanitizeConversationMessages enforces HIP-1345 §2.6 reasoning-state hygiene:
// when routing switches across incompatible thinking families (or onto a model
// with no thinking support), foreign ThinkingBlocks are stripped from outbound
// messages while preserving all visible text and tool outputs intact.
func SanitizeConversationMessages(msgs []ConversationMessage, selected ModelArm) ([]ConversationMessage, int) {
	if len(msgs) == 0 {
		return nil, 0
	}
	out := make([]ConversationMessage, len(msgs))
	stripped := 0
	for i, msg := range msgs {
		cp := msg.Clone()
		if len(cp.ThinkingBlocks) > 0 {
			kept := cp.ThinkingBlocks[:0]
			for _, tb := range cp.ThinkingBlocks {
				if selected.ThinkingFamily != "" && tb.ThinkingFamily == selected.ThinkingFamily {
					kept = append(kept, tb)
				} else {
					stripped++
				}
			}
			if len(kept) == 0 {
				clear(cp.ThinkingBlocks)
				cp.ThinkingBlocks = nil
			} else {
				clear(cp.ThinkingBlocks[len(kept):])
				cp.ThinkingBlocks = kept[:len(kept):len(kept)]
			}
		}
		out[i] = cp
	}
	return out, stripped
}

// RouteRequest encapsulates a single Enso routing request (`model=auto`) along
// with its capability, retention, reasoning-continuity, and budget constraints.
type RouteRequest struct {
	RequestID string `json:"request_id"`
	Org       string `json:"org"`
	Project   string `json:"project,omitempty"`

	Prompt   string                `json:"prompt"`
	Messages []ConversationMessage `json:"messages,omitempty"`
	HasMedia bool                  `json:"has_media,omitempty"`

	// PreExtractedFeatures allows offline content-free benchmark corpora to supply
	// exact FeatureVec values without redistributing third-party prompt text.
	PreExtractedFeatures *FeatureVec `json:"-"`
	// UnavailableModels marks provider/model outages for fault-injection and live health gating.
	UnavailableModels map[string]bool `json:"-"`

	EstInputTokens  int `json:"est_input_tokens"`
	EstOutputTokens int `json:"est_output_tokens"`
	MaxOutputTokens int `json:"max_output_tokens,omitempty"`

	RequireZeroRetention      bool           `json:"require_zero_retention,omitempty"`
	ThinkingMode              ThinkingMode   `json:"thinking_mode,omitempty"`
	PriorThinkingFamily       string         `json:"prior_thinking_family,omitempty"`
	RequireThinkingContinuity bool           `json:"require_thinking_continuity,omitempty"`
	PinnedTurnModel           string         `json:"pinned_turn_model,omitempty"`
	ToolChoice                ToolChoiceMode `json:"tool_choice,omitempty"`
	ToolCount                 int            `json:"tool_count,omitempty"`
	Temperature               *float64       `json:"temperature,omitempty"`

	FastMode             bool  `json:"fast_mode,omitempty"`
	HasPrepaidBalance    bool  `json:"has_prepaid_balance,omitempty"`
	PrepaidBalanceMicros int64 `json:"prepaid_balance_micros,omitempty"`

	MaxCostMicros int64 `json:"max_cost_micros,omitempty"`
	MaxLatencyMs  int   `json:"max_latency_ms,omitempty"`

	// Quality constraints:
	// - QualityFloor in [0, 1] sets an absolute minimum calibrated pass probability.
	// - QualityTolerance in [0, 1] sets max allowable drop below p_max (relative mode).
	// - QualityRetentionRatio in [0, 1] scales p_max (defaults to 1.0 when >0 tolerance set).
	QualityFloor          float64 `json:"quality_floor,omitempty"`
	QualityTolerance      float64 `json:"quality_tolerance,omitempty"`
	QualityRetentionRatio float64 `json:"quality_retention_ratio,omitempty"`
	StrictQualityFloor    bool    `json:"strict_quality_floor,omitempty"`
}

func isFinite(x float64) bool {
	return !math.IsNaN(x) && !math.IsInf(x, 0)
}

// ValidateScopeIdentifier enforces canonical, non-padded, control-char-free
// tenant org and project identifiers so whitespace padding can never miss
// policy lookups or forge unsanitized cache salts.
func ValidateScopeIdentifier(id string, allowEmpty bool) bool {
	if id == "" {
		return allowEmpty
	}
	if strings.TrimSpace(id) != id {
		return false
	}
	for _, r := range id {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return false
		}
	}
	return true
}

// ResolveTokenEstimates derives positive input and output token counts from
// caller estimates, falling back to MaxOutputTokens before the 256-token default.
func ResolveTokenEstimates(req RouteRequest) (int, int) {
	inTok := req.EstInputTokens
	if inTok <= 0 {
		inTok = EstimatePromptTokens(req.Prompt, req.Messages)
	}
	outTok := req.EstOutputTokens
	if outTok <= 0 {
		if req.MaxOutputTokens > 0 {
			outTok = req.MaxOutputTokens
		} else {
			outTok = 256
		}
	}
	return inTok, outTok
}

func inferPriorThinkingFamily(req RouteRequest) string {
	if req.PriorThinkingFamily != "" {
		return req.PriorThinkingFamily
	}
	for i := len(req.Messages) - 1; i >= 0; i-- {
		blocks := req.Messages[i].ThinkingBlocks
		for j := len(blocks) - 1; j >= 0; j-- {
			if blocks[j].ThinkingFamily != "" {
				return blocks[j].ThinkingFamily
			}
		}
	}
	return ""
}

const (
	// MaxRequestTokens caps per-request token hints so malformed payloads fail
	// loud with HTTP 400 rather than overflowing token/cost arithmetic.
	MaxRequestTokens = 10_000_000
	// MaxToolDefinitions caps per-request tool declarations.
	MaxToolDefinitions = 4096
)

// ValidateRequestSemantics enforces HTTP 400 fail-loud validation on malformed
// or mutually incompatible request parameters (HIP-1345 §2.6) before scoring.
func ValidateRequestSemantics(req RouteRequest, catalogByID map[string]ModelArm) error {
	if req.Org == "" {
		return &RouteError{
			Code:       ErrCodeUnauthorizedOrg,
			HTTPStatus: 403,
			Message:    "validated tenant org is required",
		}
	}
	if !ValidateScopeIdentifier(req.Org, false) || !ValidateScopeIdentifier(req.Project, true) || !ValidateScopeIdentifier(req.RequestID, true) {
		return &RouteError{
			Code:       ErrCodeInvalidRequestParams,
			HTTPStatus: 400,
			Message:    "org, project, and request_id identifiers must not contain whitespace or control characters",
		}
	}
	if !req.ToolChoice.IsValid() {
		return &RouteError{
			Code:       ErrCodeInvalidRequestParams,
			HTTPStatus: 400,
			Message:    fmt.Sprintf("unsupported tool_choice %q", req.ToolChoice),
		}
	}
	if !req.ThinkingMode.IsValid() {
		return &RouteError{
			Code:       ErrCodeInvalidRequestParams,
			HTTPStatus: 400,
			Message:    fmt.Sprintf("unsupported thinking_mode %q", req.ThinkingMode),
		}
	}
	if req.EstInputTokens < 0 || req.EstOutputTokens < 0 || req.MaxOutputTokens < 0 || req.ToolCount < 0 ||
		req.EstInputTokens > MaxRequestTokens || req.EstOutputTokens > MaxRequestTokens ||
		req.MaxOutputTokens > MaxRequestTokens || req.ToolCount > MaxToolDefinitions {
		return &RouteError{
			Code:       ErrCodeInvalidRequestParams,
			HTTPStatus: 400,
			Message:    "token and tool counts must be within [0, ceiling]",
		}
	}
	if req.MaxCostMicros < 0 || req.MaxLatencyMs < 0 {
		return &RouteError{
			Code:       ErrCodeInvalidRequestParams,
			HTTPStatus: 400,
			Message:    "SLO cost and latency ceilings must be non-negative",
		}
	}
	if !isFinite(req.QualityFloor) || !isFinite(req.QualityTolerance) || !isFinite(req.QualityRetentionRatio) ||
		req.QualityFloor < 0 || req.QualityFloor > 1 || req.QualityTolerance < 0 || req.QualityTolerance > 1 ||
		req.QualityRetentionRatio < 0 || req.QualityRetentionRatio > 1 {
		return &RouteError{
			Code:       ErrCodeInvalidRequestParams,
			HTTPStatus: 400,
			Message:    "quality_floor, quality_tolerance, and quality_retention_ratio must be finite numbers in [0, 1]",
		}
	}
	if req.Temperature != nil && (!isFinite(*req.Temperature) || *req.Temperature < 0 || *req.Temperature > 2.0) {
		return &RouteError{
			Code:       ErrCodeInvalidRequestParams,
			HTTPStatus: 400,
			Message:    "temperature must be a finite number in [0.0, 2.0]",
		}
	}

	// HIP-1345 §2.6: Return HTTP 400 rather than silently corrupting mandatory
	// thinking calls when combined with incompatible sampling or forced tool parameters.
	if req.ThinkingMode == ThinkingMandatory {
		if req.ToolChoice.IsForced() {
			return &RouteError{
				Code:       ErrCodeInvalidRequestParams,
				HTTPStatus: 400,
				Message:    fmt.Sprintf("mandatory thinking is incompatible with forced tool_choice=%s", req.ToolChoice),
			}
		}
		if req.Temperature != nil && *req.Temperature != 1.0 {
			return &RouteError{
				Code:       ErrCodeInvalidRequestParams,
				HTTPStatus: 400,
				Message:    fmt.Sprintf("mandatory thinking requires temperature=1.0 (or unset), got %.2f", *req.Temperature),
			}
		}
		if req.MaxOutputTokens > 0 && req.MaxOutputTokens < 128 {
			return &RouteError{
				Code:       ErrCodeInvalidRequestParams,
				HTTPStatus: 400,
				Message:    fmt.Sprintf("mandatory thinking requires max_output_tokens >= 128, got %d", req.MaxOutputTokens),
			}
		}
	}

	if req.PinnedTurnModel != "" {
		pinned, exists := catalogByID[req.PinnedTurnModel]
		if !exists {
			return &RouteError{
				Code:       ErrCodeInvalidRequestParams,
				HTTPStatus: 400,
				Message:    fmt.Sprintf("unknown pinned_turn_model %q", req.PinnedTurnModel),
			}
		}
		if pinned.MandatoryThinking {
			if req.ThinkingMode == ThinkingOff {
				return &RouteError{
					Code:       ErrCodeInvalidRequestParams,
					HTTPStatus: 400,
					Message:    fmt.Sprintf("pinned mandatory-thinking model %q conflicts with thinking_mode=off", pinned.ID),
				}
			}
			if req.ToolChoice.IsForced() && !pinned.SupportsForcedToolChoice {
				return &RouteError{
					Code:       ErrCodeInvalidRequestParams,
					HTTPStatus: 400,
					Message:    fmt.Sprintf("pinned mandatory-thinking model %q cannot serve forced tool_choice=%s", pinned.ID, req.ToolChoice),
				}
			}
			if req.Temperature != nil && *req.Temperature != 1.0 {
				return &RouteError{
					Code:       ErrCodeInvalidRequestParams,
					HTTPStatus: 400,
					Message:    fmt.Sprintf("pinned mandatory-thinking model %q rejects temperature=%.2f", pinned.ID, *req.Temperature),
				}
			}
			if req.MaxOutputTokens > 0 && req.MaxOutputTokens < 128 {
				return &RouteError{
					Code:       ErrCodeInvalidRequestParams,
					HTTPStatus: 400,
					Message:    fmt.Sprintf("pinned mandatory-thinking model %q requires max_output_tokens >= 128", pinned.ID),
				}
			}
		}
	}

	if req.PreExtractedFeatures != nil {
		for d, fv := range *req.PreExtractedFeatures {
			if !isFinite(fv) || math.Abs(fv) > 1e6 {
				return &RouteError{
					Code:       ErrCodeInvalidRequestParams,
					HTTPStatus: 400,
					Message:    fmt.Sprintf("pre_extracted_features[%d] must be finite and within [-1e6, 1e6]", d),
				}
			}
		}
	}

	return nil
}

// CheckCapabilityGates evaluates all deterministic HIP-1345 §1.3 & §2.6 hard
// gates for candidate arm against req and effective tenant policy pol.
// Returns (true, "") iff the arm is eligible to enter learned cost-quality scoring.
func CheckCapabilityGates(arm ModelArm, req RouteRequest, pol EffectivePolicy) (bool, GateName) {
	// 0. HIP-1345 §1.3 runtime provenance requirement on multi-tenant traffic.
	if strings.TrimSpace(arm.RuntimeDigest) == "" || strings.TrimSpace(arm.CacheConnector) == "" {
		return false, GateRuntimeProvenance
	}

	// 0b. Live servability / outage gate: skip disconnected or unavailable arms.
	if len(req.UnavailableModels) > 0 && req.UnavailableModels[arm.ID] {
		return false, GateUnavailableModel
	}

	inTok, outTok := ResolveTokenEstimates(req)

	// 0c. Vision and context-window capability mismatch gates.
	if req.HasMedia && !arm.SupportsVision {
		return false, GateVisionCapability
	}
	if arm.ContextWindow > 0 && inTok+outTok > arm.ContextWindow {
		return false, GateContextWindow
	}

	// 1. Zero Data Retention gate (HIP-1345 §2.6).
	if (pol.RequireZeroRetention || req.RequireZeroRetention) && (!arm.ZeroRetention || arm.RetentionDays > 0) {
		return false, GateZeroRetention
	}

	// 2. Thinking family & one-model-per-turn continuity gate (HIP-1345 §2.6).
	if req.PinnedTurnModel != "" && arm.ID != req.PinnedTurnModel {
		return false, GateThinkingFamily
	}
	priorFam := inferPriorThinkingFamily(req)
	if req.RequireThinkingContinuity && priorFam != "" && arm.ThinkingFamily != priorFam {
		return false, GateThinkingFamily
	}
	if req.ThinkingMode == ThinkingMandatory && arm.ThinkingFamily == "" {
		return false, GateThinkingFamily
	}
	if arm.MandatoryThinking {
		if req.ThinkingMode == ThinkingOff {
			return false, GateThinkingFamily
		}
		if req.Temperature != nil && *req.Temperature != 1.0 {
			return false, GateThinkingFamily
		}
		if req.MaxOutputTokens > 0 && req.MaxOutputTokens < 128 {
			return false, GateThinkingFamily
		}
	}

	// 3. Forced tool_choice compatibility gate (HIP-1345 §2.6).
	if req.ToolChoice.IsForced() && !arm.SupportsForcedToolChoice {
		return false, GateForcedToolChoice
	}

	// 4. Multi-tenant plan entitlement & org provider/model controls (HIP-0510 §1, §7).
	if pol.BlockedModels[arm.ID] || pol.BlockedProviders[arm.Provider] {
		return false, GatePlanEntitlement
	}
	if len(pol.AllowedModels) > 0 && !pol.AllowedModels[arm.ID] {
		return false, GatePlanEntitlement
	}
	if pol.PlanTier < arm.MinPlanTier && !pol.EntitledModels[arm.ID] {
		return false, GatePlanEntitlement
	}

	// 5. Fast-mode structural capability check (evaluated before SLO & prepaid so
	// GateFastModePrepaid strictly reflects insufficient prepaid balance).
	if req.FastMode {
		if !arm.FastModeCapable {
			return false, GateFastModeCapable
		}
	} else if arm.FastModeOnly {
		return false, GateFastModeCapable
	}

	// 6. Optional per-request / per-org SLO cost & latency ceilings (HIP-0510 §1).
	costMicros := EstimateCostMicros(arm, inTok, outTok)
	effMaxCost := pol.MaxCostMicros
	if req.MaxCostMicros > 0 && (effMaxCost == 0 || req.MaxCostMicros < effMaxCost) {
		effMaxCost = req.MaxCostMicros
	}
	if effMaxCost > 0 && costMicros > effMaxCost {
		return false, GateSLOCeiling
	}

	effMaxLat := pol.MaxLatencyMs
	if req.MaxLatencyMs > 0 && (effMaxLat == 0 || req.MaxLatencyMs < effMaxLat) {
		effMaxLat = req.MaxLatencyMs
	}
	if effMaxLat > 0 && EstimateLatencyMs(arm, outTok) > effMaxLat {
		return false, GateSLOCeiling
	}

	// 7. Fast-mode prepaid balance check (HIP-1348 §1.1–§1.2).
	if req.FastMode {
		if !req.HasPrepaidBalance || req.PrepaidBalanceMicros <= 0 || req.PrepaidBalanceMicros < costMicros {
			return false, GateFastModePrepaid
		}
	}

	return true, ""
}
