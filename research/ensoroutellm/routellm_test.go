// Copyright 2026 Hanzo AI, Inc.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package ensoroutellm

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func buildFixtureRouter(t *testing.T) (*EnsoRouter, *TrainedWeights, []ModelArm) {
	t.Helper()
	catalog := DefaultCatalog()
	corpus := GenerateStandardCorpus(180, catalog, 424242)
	var samples []TrainingSample
	for _, item := range corpus {
		feats := ExtractFeatures(item.Request)
		for _, arm := range catalog {
			if arm.FastModeOnly {
				continue
			}
			samples = append(samples, TrainingSample{
				QueryID:  item.QueryID,
				Features: feats,
				ModelID:  arm.ID,
				Passed:   item.ArmOutcomes[arm.ID],
			})
		}
	}
	tw := FitTrainedWeights(samples, catalog, 424242)
	r := NewEnsoRouter(catalog, tw)
	r.SetOrgPolicy("org-ent", ScopePolicy{PlanTier: PlanEnterprise, HasPlanTier: true})
	return r, tw, catalog
}

// 1. Gate 1: Zero Data Retention blocks 30-day retention endpoints via request or org policy.
func TestZeroRetentionHardGate(t *testing.T) {
	r, _, catalog := buildFixtureRouter(t)
	byID := make(map[string]ModelArm)
	for _, a := range catalog {
		byID[a.ID] = a
	}

	// Request-level ZDR must never select ext-oss-70b-30d or frontier-o-reasoner-30d.
	dec, err := r.Route(RouteRequest{
		Org:                  "org-ent",
		Prompt:               "Hello! Shorten this greeting.",
		EstInputTokens:       100,
		EstOutputTokens:      50,
		RequireZeroRetention: true,
		QualityFloor:         0.70,
	}, StrategyEnsoHybridConstrained)
	if err != nil {
		t.Fatalf("unexpected route error: %v", err)
	}
	chosen := byID[dec.RoutedModel]
	if !chosen.ZeroRetention || chosen.RetentionDays > 0 {
		t.Fatalf("ZDR violation: selected %s (retention_days=%d)", chosen.ID, chosen.RetentionDays)
	}

	// Org-level ZDR cannot be bypassed even when request omits RequireZeroRetention.
	r.SetOrgPolicy("org-zdr", ScopePolicy{
		PlanTier:             PlanEnterprise,
		HasPlanTier:          true,
		RequireZeroRetention: true,
		AllowedModels:        []string{"ext-oss-70b-30d"},
	})
	_, err = r.Route(RouteRequest{
		Org:    "org-zdr",
		Prompt: "Hello!",
	}, StrategyEnsoHybridConstrained)
	if !IsRouteErrorCode(err, ErrCodeNoEligibleModel) {
		t.Fatalf("expected %s when only 30d model is in allowed pool under ZDR org, got %v", ErrCodeNoEligibleModel, err)
	}
}

// 2. Gate 2: One-model-per-turn lock & thinking_family continuity.
func TestThinkingFamilyAndTurnPinContinuity(t *testing.T) {
	r, _, catalog := buildFixtureRouter(t)
	byID := make(map[string]ModelArm)
	for _, a := range catalog {
		byID[a.ID] = a
	}

	// Active turn lock pins to exact model regardless of cheaper alternatives.
	dec, err := r.Route(RouteRequest{
		Org:             "org-ent",
		Prompt:          "Hello! Shorten greeting.",
		PinnedTurnModel: "zen-4-pro",
	}, StrategyEnsoHybridConstrained)
	if err != nil || dec.RoutedModel != "zen-4-pro" {
		t.Fatalf("expected pinned turn model zen-4-pro, got dec=%+v err=%v", dec, err)
	}

	// Requiring anthropic-extended continuity filters out zen-v4 and openai-reasoning families.
	dec2, err := r.Route(RouteRequest{
		Org:                       "org-ent",
		Prompt:                    "Continue multi-step proof.",
		PriorThinkingFamily:       "anthropic-extended",
		RequireThinkingContinuity: true,
	}, StrategyEnsoHybridConstrained)
	if err != nil {
		t.Fatalf("continuity route failed: %v", err)
	}
	if byID[dec2.RoutedModel].ThinkingFamily != "anthropic-extended" {
		t.Fatalf("expected anthropic-extended model, got %s (%s)", dec2.RoutedModel, byID[dec2.RoutedModel].ThinkingFamily)
	}

	// ThinkingOff must exclude MandatoryThinking models (zen-4-thinking).
	r.SetOrgPolicy("org-only-mandatory", ScopePolicy{
		PlanTier:      PlanEnterprise,
		HasPlanTier:   true,
		AllowedModels: []string{"zen-4-thinking"},
	})
	if _, err := r.Route(RouteRequest{
		Org:          "org-only-mandatory",
		Prompt:       "Quick chat",
		ThinkingMode: ThinkingOff,
	}, StrategyEnsoHybridConstrained); !IsRouteErrorCode(err, ErrCodeNoEligibleModel) {
		t.Fatalf("expected ThinkingOff to filter MandatoryThinking arm, got %v", err)
	}
}

// 3. HIP-1345 §2.6 cross-family reasoning block stripping & caller-slice immutability.
func TestCrossFamilyThinkingBlockSanitization(t *testing.T) {
	r, _, _ := buildFixtureRouter(t)

	origMsgs := []ConversationMessage{
		{
			Role:    "assistant",
			Content: "Step 1 answer.",
			ThinkingBlocks: []ThinkingBlock{
				{ThinkingFamily: "anthropic-extended", SourceModel: "frontier-claude-sonnet", Signature: "sig-1", Summary: "foreign trace"},
				{ThinkingFamily: "zen-v4", SourceModel: "zen-4-pro", Signature: "sig-2", Summary: "compatible trace"},
			},
		},
	}

	dec, err := r.Route(RouteRequest{
		Org:             "org-ent",
		Prompt:          "Step 2 follow-up.",
		Messages:        origMsgs,
		PinnedTurnModel: "zen-4-mini", // ThinkingFamily == "zen-v4"
	}, StrategyEnsoHybridConstrained)
	if err != nil {
		t.Fatalf("route failed: %v", err)
	}
	if dec.StrippedThinkingBlocks != 1 {
		t.Fatalf("expected 1 foreign block stripped, got %d", dec.StrippedThinkingBlocks)
	}
	if len(dec.SanitizedMessages[0].ThinkingBlocks) != 1 || dec.SanitizedMessages[0].ThinkingBlocks[0].ThinkingFamily != "zen-v4" {
		t.Fatalf("unexpected sanitized blocks: %+v", dec.SanitizedMessages[0].ThinkingBlocks)
	}
	// Caller's input slice must not be mutated in place!
	if len(origMsgs[0].ThinkingBlocks) != 2 {
		t.Fatalf("caller message slice was mutated in place: len=%d", len(origMsgs[0].ThinkingBlocks))
	}
}

// 4. Gate 3 & HIP-1345 §2.6 HTTP 400 mandatory-thinking / forced tool_choice parameter validation.
func TestForcedToolChoiceAndHTTP400ParameterValidation(t *testing.T) {
	r, _, catalog := buildFixtureRouter(t)
	byID := make(map[string]ModelArm)
	for _, a := range catalog {
		byID[a.ID] = a
	}

	// Forced tool_choice=required excludes ext-oss-70b-30d and zen-4-thinking.
	dec, err := r.Route(RouteRequest{
		Org:          "org-ent",
		Prompt:       "Prove eigenvalue theorem and call tool.",
		ToolChoice:   ToolChoiceRequired,
		ToolCount:    2,
		QualityFloor: 0.85,
	}, StrategyEnsoHybridConstrained)
	if err != nil {
		t.Fatalf("forced tool route failed: %v", err)
	}
	if !byID[dec.RoutedModel].SupportsForcedToolChoice {
		t.Fatalf("selected arm %s does not support forced tool_choice", dec.RoutedModel)
	}

	// Mandatory thinking + temperature=0.0 MUST fail loud with HTTP 400!
	zeroTemp := 0.0
	_, err = r.Route(RouteRequest{
		Org:          "org-ent",
		Prompt:       "Solve proof.",
		ThinkingMode: ThinkingMandatory,
		Temperature:  &zeroTemp,
	}, StrategyEnsoHybridConstrained)
	var re *RouteError
	if !IsRouteErrorCode(err, ErrCodeInvalidRequestParams) {
		t.Fatalf("expected %s on mandatory thinking + temp=0, got %v", ErrCodeInvalidRequestParams, err)
	}
	re = err.(*RouteError)
	if re.HTTPStatus != 400 {
		t.Fatalf("expected HTTP 400, got %d", re.HTTPStatus)
	}

	// Mandatory thinking + max_output_tokens < 128 -> HTTP 400.
	_, err = r.Route(RouteRequest{
		Org:             "org-ent",
		Prompt:          "Solve proof.",
		ThinkingMode:    ThinkingMandatory,
		MaxOutputTokens: 64,
	}, StrategyEnsoHybridConstrained)
	if !IsRouteErrorCode(err, ErrCodeInvalidRequestParams) {
		t.Fatalf("expected HTTP 400 on tiny max_output_tokens with mandatory thinking, got %v", err)
	}

	// Pinned mandatory-thinking arm (zen-4-thinking) + forced tool_choice -> HTTP 400.
	_, err = r.Route(RouteRequest{
		Org:             "org-ent",
		Prompt:          "Call function.",
		PinnedTurnModel: "zen-4-thinking",
		ToolChoice:      ToolChoiceNamedFunction,
	}, StrategyEnsoHybridConstrained)
	if !IsRouteErrorCode(err, ErrCodeInvalidRequestParams) {
		t.Fatalf("expected HTTP 400 on pinned mandatory-thinking + forced tool conflict, got %v", err)
	}
}

// 5. Gate 4: Plan entitlements, explicit SKU grants, and narrow-only org/project precedence.
func TestPlanEntitlementsAndNarrowOnlyPrecedence(t *testing.T) {
	r, _, catalog := buildFixtureRouter(t)
	byID := make(map[string]ModelArm)
	for _, a := range catalog {
		byID[a.ID] = a
	}

	r.SetPlatformPolicy(ScopePolicy{
		PlanTier:             PlanFree,
		HasPlanTier:          true,
		RequireZeroRetention: false,
	})
	r.SetOrgPolicy("org-pro", ScopePolicy{
		PlanTier:             PlanPro,
		HasPlanTier:          true,
		RequireZeroRetention: true,
		MaxCostMicros:        50_000,
		MinQualityFloor:      0.75,
		EntitledModels:       []string{"zen-4-max"}, // explicit SKU grant on Pro org
		BlockedProviders:     []string{"ext-batchcloud"},
	})
	// Project attempts to escalate PlanTier to Enterprise, disable ZDR, raise MaxCostMicros,
	// lower MinQualityFloor, and grant frontier-o-reasoner-30d -> ALL escalations must be ignored!
	r.SetProjectPolicy("org-pro", "proj-sandbox", ScopePolicy{
		PlanTier:             PlanEnterprise,
		HasPlanTier:          true,
		RequireZeroRetention: false,
		MaxCostMicros:        999_999,
		MinQualityFloor:      0.10,
		EntitledModels:       []string{"frontier-o-reasoner-30d"}, // NOT granted to parent org!
	})

	eff := r.EffectivePolicyFor("org-pro", "proj-sandbox")
	if eff.PlanTier != PlanPro {
		t.Fatalf("project escalated PlanTier from Pro to %s", eff.PlanTier)
	}
	if !eff.RequireZeroRetention {
		t.Fatal("project disabled org-level RequireZeroRetention")
	}
	if eff.MaxCostMicros != 50_000 {
		t.Fatalf("project loosened MaxCostMicros to %d", eff.MaxCostMicros)
	}
	if eff.MinQualityFloor != 0.75 {
		t.Fatalf("project lowered MinQualityFloor to %.2f", eff.MinQualityFloor)
	}
	if eff.EntitledModels["frontier-o-reasoner-30d"] {
		t.Fatal("project granted model not entitled at org level")
	}

	// Conversely, project CAN narrow PlanTier downward (Pro -> Free).
	r.SetProjectPolicy("org-pro", "proj-free-cap", ScopePolicy{
		PlanTier:       PlanFree,
		HasPlanTier:    true,
		EntitledModels: []string{"__none__"}, // narrows away org's zen-4-max grant
	})
	dec, err := r.Route(RouteRequest{
		Org:          "org-pro",
		Project:      "proj-free-cap",
		Prompt:       "Prove theorem by induction.",
		QualityFloor: 0.95,
	}, StrategyEnsoHybridConstrained)
	if err != nil {
		t.Fatalf("narrowed project route failed: %v", err)
	}
	if byID[dec.RoutedModel].MinPlanTier > PlanFree {
		t.Fatalf("narrowed Free project routed to higher-tier model %s", dec.RoutedModel)
	}
}

// 6. Gate 5 & HIP-1348 §1.2: Fast-mode prepaid fail-closed & budget_exhausted taxonomy.
func TestFastModePrepaidFailClosedAndBudgetTaxonomy(t *testing.T) {
	r, _, _ := buildFixtureRouter(t)

	// Missing prepaid flag -> budget_exhausted (layer=tenant_ledger, BillingTopUpHint=true).
	_, err := r.Route(RouteRequest{
		Org:      "org-ent",
		Prompt:   "Low-latency query",
		FastMode: true,
	}, StrategyEnsoHybridConstrained)
	if !IsRouteErrorCode(err, ErrCodeBudgetExhausted) {
		t.Fatalf("expected %s on missing prepaid balance, got %v", ErrCodeBudgetExhausted, err)
	}
	re := err.(*RouteError)
	if re.Layer != "tenant_ledger" || !re.BillingTopUpHint {
		t.Fatalf("HIP-1348 §1.2 violation: expected layer=tenant_ledger and BillingTopUpHint=true, got %+v", re)
	}

	// Non-tenant layer must never emit BillingTopUpHint (HIP-1348 §1.2).
	gwErr := NewBudgetExhaustedRouteError("ai_gateway", "gateway cap")
	if gwErr.BillingTopUpHint {
		t.Fatal("HIP-1348 §1.2 violation: ai_gateway layer must not set BillingTopUpHint")
	}

	// Funded fast_mode selects FastModeCapable SKU (zen-4-pro-fast eligible when fast_mode=true).
	dec, err := r.Route(RouteRequest{
		Org:                  "org-ent",
		Prompt:               "Synthesize passages and cross-reference citation evidence span.",
		EstInputTokens:       2000,
		EstOutputTokens:      400,
		FastMode:             true,
		HasPrepaidBalance:    true,
		PrepaidBalanceMicros: 500_000,
		MaxLatencyMs:         1900, // requires ultra-fast zen-4-pro-fast (35 + 400*3.8 = 1555ms)!
		QualityFloor:         0.80,
	}, StrategyEnsoHybridConstrained)
	if err != nil {
		t.Fatalf("funded fast_mode route failed: %v", err)
	}
	if dec.RoutedModel != "zen-4-pro-fast" {
		t.Fatalf("expected zen-4-pro-fast under fast_mode + tight latency SLO, got %s", dec.RoutedModel)
	}
}

// 7. HIP-1345 §1.1 & §1.3 + HIP-1332 §19 provenance fields and missing-digest rejection.
func TestRuntimeProvenanceAndTenantCacheSalt(t *testing.T) {
	cat := DefaultCatalog()
	// Append an untrusted legacy pool arm missing RuntimeDigest -> must be blocked!
	cat = append(cat, ModelArm{
		ID:                       "legacy-unsigned-cheap",
		Provider:                 "legacy",
		InputUSDPerMTok:          0.01,
		OutputUSDPerMTok:         0.01,
		ZeroRetention:            true,
		SupportsForcedToolChoice: true,
		MinPlanTier:              PlanFree,
		RuntimeDigest:            "",
		CacheConnector:           "",
	})
	r := NewEnsoRouter(cat, nil)
	r.SetOrgPolicy("org-acme", ScopePolicy{
		PlanTier:      PlanFree,
		HasPlanTier:   true,
		AllowedModels: []string{"legacy-unsigned-cheap"},
	})

	if _, err := r.Route(RouteRequest{Org: "org-acme", Prompt: "Hi"}, StrategyStaticRule); !IsRouteErrorCode(err, ErrCodeNoEligibleModel) {
		t.Fatalf("expected missing RuntimeDigest arm to fail GateRuntimeProvenance, got %v", err)
	}

	// Valid route stamps salted CacheSalt ("hzo-<org>"), RuntimeDigest, CacheConnector, and revisions.
	r2, _, _ := buildFixtureRouter(t)
	dec, err := r2.Route(RouteRequest{Org: "org-ent", RequestID: "req-prov-1", Prompt: "Hello!"}, StrategyEnsoHybridConstrained)
	if err != nil {
		t.Fatalf("route failed: %v", err)
	}
	if dec.CacheSalt != "hzo-org-ent" || dec.XRoutedModelHeader != dec.RoutedModel ||
		!strings.HasPrefix(dec.RuntimeDigest, "sha256:") || dec.CacheConnector == "" ||
		!strings.HasPrefix(dec.RouterModelRevision, "enso-w-") || !strings.HasPrefix(dec.CalibrationRevision, "cal_") {
		t.Fatalf("incomplete HIP-1345/1332 provenance on RouteDecision: %+v", dec)
	}
}

// 8. Content-free reward ledger & cross-tenant isolation (HIP-0510 §2 & §7).
func TestContentFreeFeedbackLedgerAndCrossTenantIsolation(t *testing.T) {
	r, _, _ := buildFixtureRouter(t)
	r.SetOrgPolicy("org-a", ScopePolicy{PlanTier: PlanPro, HasPlanTier: true})
	r.SetOrgPolicy("org-b", ScopePolicy{PlanTier: PlanPro, HasPlanTier: true})

	sampleUserPrompt := "customer billing inquiry about monthly invoice itemization"
	if _, err := r.Route(RouteRequest{
		RequestID: "req-a-100",
		Org:       "org-a",
		Prompt:    sampleUserPrompt,
	}, StrategyEnsoHybridConstrained); err != nil {
		t.Fatalf("org-a route failed: %v", err)
	}

	// Cross-tenant feedback attachment from org-b MUST fail closed!
	if err := r.RecordFeedback("org-b", "req-a-100", 1.0); !IsRouteErrorCode(err, ErrCodeCrossTenantDenied) {
		t.Fatalf("expected cross-tenant feedback denial, got %v", err)
	}

	// Out-of-range or NaN rewards fail with HTTP 400.
	if err := r.RecordFeedback("org-a", "req-a-100", 1.5); !IsRouteErrorCode(err, ErrCodeInvalidRequestParams) {
		t.Fatalf("expected invalid reward 1.5 rejection, got %v", err)
	}
	if err := r.RecordFeedback("org-a", "req-a-100", math.NaN()); !IsRouteErrorCode(err, ErrCodeInvalidRequestParams) {
		t.Fatalf("expected NaN reward rejection, got %v", err)
	}

	// Valid org-a feedback succeeds and exported ledger contains ZERO prompt text.
	if err := r.RecordFeedback("org-a", "req-a-100", 0.9); err != nil {
		t.Fatalf("org-a feedback failed: %v", err)
	}
	ledgerA, err := r.ExportOrgLedger("org-a")
	if err != nil || len(ledgerA) != 1 || ledgerA[0].Reward == nil || *ledgerA[0].Reward != 0.9 {
		t.Fatalf("unexpected org-a ledger: %+v err=%v", ledgerA, err)
	}
	rawLedger, _ := json.Marshal(ledgerA)
	if strings.Contains(string(rawLedger), sampleUserPrompt) {
		t.Fatal("PRIVACY LEAK: prompt text appeared in serialized content-free ledger!")
	}

	ledgerB, _ := r.ExportOrgLedger("org-b")
	if len(ledgerB) != 0 {
		t.Fatalf("org-b saw %d foreign ledger entries", len(ledgerB))
	}
}

// 9. HIP-0510 §3.2 Promote-or-Keep retrain gate (promotes good fit, rejects poisoned fit).
func TestRetrainPromoteOrKeepGate(t *testing.T) {
	catalog := DefaultCatalog()
	// Start from cold-start (nil baseWeights) and verify a genuine training set promotes!
	r := NewEnsoRouter(catalog, nil)
	r.SetOrgPolicy("org-ml", ScopePolicy{PlanTier: PlanEnterprise, HasPlanTier: true, RequireZeroRetention: true})

	corpus := GenerateStandardCorpus(300, catalog, 777)
	var cleanTrain []TrainingSample
	for _, task := range corpus[:220] {
		f := ExtractFeatures(task.Request)
		for _, a := range catalog {
			if a.ZeroRetention && !a.FastModeOnly {
				cleanTrain = append(cleanTrain, TrainingSample{QueryID: task.QueryID, Features: f, ModelID: a.ID, Passed: task.ArmOutcomes[a.ID]})
			}
		}
	}
	var heldOut []EvalQueryGroundTruth
	for _, task := range corpus[220:] {
		req := task.Request
		req.Org = "org-ml"
		heldOut = append(heldOut, EvalQueryGroundTruth{
			QueryID:     task.QueryID,
			Request:     req,
			ArmOutcomes: task.ArmOutcomes,
		})
	}

	v1, err := r.RetrainOrgPromoteOrKeep("org-ml", cleanTrain, heldOut, 0.5, 0.005, 888)
	if err != nil || !v1.Promoted {
		t.Fatalf("expected clean domain weights to promote over cold-start rules, got %+v err=%v", v1, err)
	}

	// Now construct inverted/poisoned training labels (marking flash=1.0 on hard math and max=0.0!)
	// -> Promote-or-Keep gate MUST reject candidate and retain v1.ActiveModelRevision!
	var poisoned []TrainingSample
	for _, s := range cleanTrain {
		inv := s
		inv.Passed = 1.0 - s.Passed
		poisoned = append(poisoned, inv)
	}
	v2, err := r.RetrainOrgPromoteOrKeep("org-ml", poisoned, heldOut, 0.5, 0.005, 999)
	if err != nil {
		t.Fatalf("retrain call failed: %v", err)
	}
	if v2.Promoted || v2.ActiveModelRevision != v1.ActiveModelRevision {
		t.Fatalf("promote-or-keep failed to block poisoned candidate: v1=%+v v2=%+v", v1, v2)
	}
}

// 10. Strict quality floor (422 rejection) vs best-effort fallback & cold-start safety.
func TestStrictQualityFloorAndColdStartFallback(t *testing.T) {
	cat := DefaultCatalog()
	cold := NewEnsoRouter(cat, nil)
	cold.SetOrgPolicy("org-cold", ScopePolicy{PlanTier: PlanPro, HasPlanTier: true, RequireZeroRetention: true})

	dec, err := cold.Route(RouteRequest{
		Org:    "org-cold",
		Prompt: "Implement mutex worker pool in golang ```go func main() {} ```",
	}, StrategyEnsoHybridConstrained)
	if err != nil || dec.FallbackUsed != "cold_start_rule" || dec.RoutedModel != "zen-4-pro" {
		t.Fatalf("expected cold_start_rule routing to zen-4-pro, got dec=%+v err=%v", dec, err)
	}

	// StrictQualityFloor=true with impossible 0.9999 floor returns 422 ErrCodeQualityFloorUnmet.
	r, _, _ := buildFixtureRouter(t)
	_, err = r.Route(RouteRequest{
		Org:                "org-ent",
		Prompt:             "Prove eigenvalue convergence.",
		QualityFloor:       0.9999,
		StrictQualityFloor: true,
	}, StrategyEnsoHybridConstrained)
	if !IsRouteErrorCode(err, ErrCodeQualityFloorUnmet) {
		t.Fatalf("expected %s on impossible strict floor, got %v", ErrCodeQualityFloorUnmet, err)
	}

	// StrictQualityFloor=false falls back deterministically to highest-quality eligible arm.
	dec2, err := r.Route(RouteRequest{
		Org:                "org-ent",
		Prompt:             "Prove eigenvalue convergence.",
		QualityFloor:       0.9999,
		StrictQualityFloor: false,
	}, StrategyEnsoHybridConstrained)
	if err != nil || dec2.FallbackUsed != "best_eligible_below_floor" {
		t.Fatalf("expected best_eligible_below_floor fallback, got dec=%+v err=%v", dec2, err)
	}
}

// 11. Concurrent multi-goroutine routing, policy mutation, feedback, and hot-swap stress.
func TestConcurrentRoutingAndHotSwapSafety(t *testing.T) {
	r, _, catalog := buildFixtureRouter(t)
	r.SetOrgPolicy("org-concurrent", ScopePolicy{PlanTier: PlanEnterprise, HasPlanTier: true, RequireZeroRetention: true})

	corpus := GenerateStandardCorpus(40, catalog, 5150)
	var samples []TrainingSample
	var heldOut []EvalQueryGroundTruth
	for i, item := range corpus {
		f := ExtractFeatures(item.Request)
		for _, a := range catalog {
			if a.ZeroRetention && !a.FastModeOnly {
				samples = append(samples, TrainingSample{QueryID: item.QueryID, Features: f, ModelID: a.ID, Passed: item.ArmOutcomes[a.ID]})
			}
		}
		if i < 15 {
			req := item.Request
			req.Org = "org-concurrent"
			heldOut = append(heldOut, EvalQueryGroundTruth{QueryID: item.QueryID, Request: req, ArmOutcomes: item.ArmOutcomes})
		}
	}

	var wg sync.WaitGroup
	start := make(chan struct{})

	// 24 concurrent reader/router + feedback goroutines
	for g := 0; g < 24; g++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			<-start
			for j := 0; j < 25; j++ {
				reqID := fmt.Sprintf("conc-%d-%d", id, j)
				dec, err := r.Route(RouteRequest{
					RequestID:            reqID,
					Org:                  "org-concurrent",
					Prompt:               corpus[(id+j)%len(corpus)].Request.Prompt,
					RequireZeroRetention: true,
				}, StrategyEnsoHybridConstrained)
				if err != nil {
					t.Errorf("concurrent route failed: %v", err)
					return
				}
				if dec.RoutedModel == "" {
					t.Error("empty RoutedModel")
					return
				}
				if err := r.RecordFeedback("org-concurrent", reqID, 0.85); err != nil {
					t.Errorf("concurrent feedback failed: %v", err)
					return
				}
			}
		}(g)
	}

	// 4 concurrent policy + promote-or-keep retrain writers
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			<-start
			r.SetProjectPolicy("org-concurrent", fmt.Sprintf("proj-%d", id), ScopePolicy{
				PlanTier:     PlanPro,
				HasPlanTier:  true,
				MaxLatencyMs: 25_000,
			})
			if _, err := r.RetrainOrgPromoteOrKeep("org-concurrent", samples, heldOut, 1.5, 0.0, uint64(9000+id)); err != nil {
				t.Errorf("concurrent retrain failed: %v", err)
			}
		}(w)
	}

	close(start)
	wg.Wait()
}

// 12. CPU sub-millisecond routing decision latency verification (HIP-0510).
func TestRoutingDecisionSubMillisecondLatency(t *testing.T) {
	r, _, _ := buildFixtureRouter(t)
	req := RouteRequest{
		Org:                  "org-ent",
		Prompt:               "Implement concurrent cache in golang with O(1) eviction.",
		EstInputTokens:       600,
		EstOutputTokens:      400,
		RequireZeroRetention: true,
	}
	const n = 2000
	t0 := time.Now()
	for i := 0; i < n; i++ {
		if _, err := r.Route(req, StrategyEnsoHybridConstrained); err != nil {
			t.Fatalf("route failed: %v", err)
		}
	}
	avgUs := float64(time.Since(t0).Nanoseconds()) / float64(n) / 1000.0
	if avgUs > 500.0 {
		t.Fatalf("routing latency %.2f µs/op exceeded 500 µs CPU bound", avgUs)
	}
}

// 13. Quantitative verification of research hypotheses H1-H4 on live benchmark suite.
func TestHypothesesVerdictsHold(t *testing.T) {
	rep := RunBenchmarkSuite(DefaultEvalSeed)

	byName := make(map[string]PolicyEvalMetrics)
	for _, p := range rep.DensePolicies {
		byName[p.PolicyName] = p
	}

	rel002 := byName["enso_hybrid_rel(tol=0.02)"]
	rel004 := byName["enso_hybrid_rel(tol=0.04)"]
	abs080 := byName["enso_hybrid_abs(floor=0.80)"]
	staticPol := byName["static_rule_prefer(HIP-0510)"]
	randPol := byName["random_router"]

	// H1: Calibrated Enso router retains >= 96% quality at tol=0.04 (>= 98% at
	// tol=0.02) while cutting blended $/MTok by >= 45% and USD spend by >= 30%,
	// beating both random_router and static_rule_prefer on accuracy, with ECE <= 0.03
	// and APGR >= 0.80.
	if rel002.QualityRetentionPct < 98.0 || rel004.QualityRetentionPct < 96.0 ||
		rel004.BlendedCostSavingPct < 45.0 || rel004.USDCostSavingPct < 30.0 {
		t.Fatalf("H1 cost-quality target missed: rel002=%+v rel004=%+v", rel002, rel004)
	}
	if abs080.QualityRetentionPct < 92.0 || abs080.BlendedCostSavingPct < 65.0 || abs080.USDCostSavingPct < 45.0 {
		t.Fatalf("H1 floor=0.80 savings target missed: %+v", abs080)
	}
	if rel004.Accuracy <= staticPol.Accuracy+0.05 || rel004.Accuracy <= randPol.Accuracy+0.12 ||
		rep.Calibration.ECE10Bin > 0.03 || rep.Calibration.APGR < 0.80 {
		t.Fatalf("H1 calibration/APGR/dominance target missed: cal=%+v rel004=%+v", rep.Calibration, rel004)
	}

	// H2: Under sparse partial-feedback logs (85.7% missing labels), bilinear MF +
	// Enso Hybrid achieves lower Brier error and >= +4pp higher accuracy than
	// isolated per-arm logistic predictors.
	sp := rep.SparseComparison
	if sp.EnsoHybridBrier >= sp.LogisticOnlyBrier || sp.EnsoHybridAcc < sp.LogisticOnlyAcc+0.04 {
		t.Fatalf("H2 sparse bandit advantage violated: %+v", sp)
	}

	// H3: Unconstrained cost-quality routing commits >50 capability/security gate
	// violations across the 12-scenario audit, whereas Enso's deterministic gates
	// achieve 0 violations across all 132 requests.
	if rep.GateAudit.UnconstrainedTotalViolations <= 50 || rep.GateAudit.ConstrainedTotalViolations != 0 {
		t.Fatalf("H3 capability gate audit violated: %+v", rep.GateAudit)
	}

	// H4 (Expected Failure Mode): On deceptively terse hard math queries devoid of
	// surface lexical cues, loose tolerance (0.08) under-routes relative to
	// always_strong by >= 15pp, while tightening tolerance (0.02) recovers parity.
	adv := rep.AdversarialSlice
	if adv.AlwaysStrongAccuracy-adv.LooseTol008Accuracy < 0.15 || adv.TightTol002Accuracy < adv.AlwaysStrongAccuracy-0.05 {
		t.Fatalf("H4 expected failure mode & tight-tolerance recovery not observed: %+v", adv)
	}
}

// 14. Drift lock: committed testdata/results.json must match live RunBenchmarkSuite bit-for-bit.
func TestCommittedResultsJSONMatchesLiveCode(t *testing.T) {
	path := filepath.Join("testdata", "results.json")
	committed, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	live, err := MarshalBenchmarkJSON(RunBenchmarkSuite(DefaultEvalSeed))
	if err != nil {
		t.Fatalf("marshal live suite: %v", err)
	}
	if !bytes.Equal(bytes.TrimSpace(committed), bytes.TrimSpace(live)) {
		t.Fatalf("testdata/results.json is out of sync with RunBenchmarkSuite(%d)", DefaultEvalSeed)
	}
}

// 15. Validate results/schema.json definitions and cross-check every required key against results.json.
func TestCommittedSchemaAndResultsContract(t *testing.T) {
	rawSchema, err := os.ReadFile(filepath.Join("results", "schema.json"))
	if err != nil {
		t.Fatalf("read schema.json: %v", err)
	}
	var schema struct {
		Schema      string                     `json:"$schema"`
		ID          string                     `json:"$id"`
		Required    []string                   `json:"required"`
		Properties  map[string]json.RawMessage `json:"properties"`
		Definitions map[string]struct {
			Required   []string                   `json:"required"`
			Properties map[string]json.RawMessage `json:"properties"`
		} `json:"definitions"`
	}
	if err := json.Unmarshal(rawSchema, &schema); err != nil {
		t.Fatalf("invalid schema.json: %v", err)
	}
	for _, rk := range schema.Required {
		if _, ok := schema.Properties[rk]; !ok {
			t.Fatalf("top-level required key %q missing from schema.properties", rk)
		}
	}
	for defName, def := range schema.Definitions {
		for _, rk := range def.Required {
			if _, ok := def.Properties[rk]; !ok {
				t.Fatalf("definition %q required key %q missing from properties", defName, rk)
			}
		}
	}

	rawRes, err := os.ReadFile(filepath.Join("testdata", "results.json"))
	if err != nil {
		t.Fatalf("read results.json: %v", err)
	}
	var resMap map[string]json.RawMessage
	if err := json.Unmarshal(rawRes, &resMap); err != nil {
		t.Fatalf("invalid results.json: %v", err)
	}
	for _, rk := range schema.Required {
		if _, ok := resMap[rk]; !ok {
			t.Fatalf("results.json missing required top-level key %q", rk)
		}
	}
}

// 16. Cross-tenant RequestID collision immunity, composite (org, project) key isolation,
// and whitespace/control-character scope identifier hardening.
func TestMultiTenantCompositeKeyAndScopeIdentifierHardening(t *testing.T) {
	r, _, _ := buildFixtureRouter(t)
	r.SetOrgPolicy("org-a", ScopePolicy{PlanTier: PlanEnterprise, HasPlanTier: true, RequireZeroRetention: true})
	r.SetOrgPolicy("org-b", ScopePolicy{PlanTier: PlanFree, HasPlanTier: true, RequireZeroRetention: true})

	// Both tenants route using the exact same RequestID ("req-shared") -> must never clobber!
	if _, err := r.Route(RouteRequest{
		RequestID: "req-shared",
		Org:       "org-a",
		Prompt:    "Implement distributed lock in golang.",
	}, StrategyEnsoHybridConstrained); err != nil {
		t.Fatalf("org-a route failed: %v", err)
	}
	if _, err := r.Route(RouteRequest{
		RequestID: "req-shared",
		Org:       "org-b",
		Prompt:    "Hello world!",
	}, StrategyEnsoHybridConstrained); err != nil {
		t.Fatalf("org-b route failed: %v", err)
	}

	if err := r.RecordFeedback("org-a", "req-shared", 0.95); err != nil {
		t.Fatalf("org-a feedback failed: %v", err)
	}
	// Re-routing the same (org-a, req-shared) must preserve the recorded reward!
	if _, err := r.Route(RouteRequest{
		RequestID: "req-shared",
		Org:       "org-a",
		Prompt:    "Implement distributed lock in golang.",
	}, StrategyEnsoHybridConstrained); err != nil {
		t.Fatalf("org-a idempotent re-route failed: %v", err)
	}

	ledA, err := r.ExportOrgLedger("org-a")
	if err != nil || len(ledA) != 1 || ledA[0].Reward == nil || *ledA[0].Reward != 0.95 {
		t.Fatalf("org-a ledger corrupted or reward overwritten: %+v err=%v", ledA, err)
	}
	ledB, err := r.ExportOrgLedger("org-b")
	if err != nil || len(ledB) != 1 || ledB[0].Reward != nil {
		t.Fatalf("org-b ledger contaminated by org-a feedback: %+v err=%v", ledB, err)
	}

	// Composite struct key immunity against "/" delimiter collisions: ("a/b", "c") vs ("a", "b/c")
	r.SetOrgPolicy("a/b", ScopePolicy{PlanTier: PlanEnterprise, HasPlanTier: true})
	r.SetOrgPolicy("a", ScopePolicy{PlanTier: PlanEnterprise, HasPlanTier: true})
	r.SetProjectPolicy("a/b", "c", ScopePolicy{PlanTier: PlanFree, HasPlanTier: true})
	r.SetProjectPolicy("a", "b/c", ScopePolicy{PlanTier: PlanPro, HasPlanTier: true})

	if got := r.EffectivePolicyFor("a/b", "c").PlanTier; got != PlanFree {
		t.Fatalf("expected PlanFree for (a/b, c), got %v", got)
	}
	if got := r.EffectivePolicyFor("a", "b/c").PlanTier; got != PlanPro {
		t.Fatalf("expected PlanPro for (a, b/c), got %v", got)
	}

	// Whitespace-padded or control-char org identifier ("org-b ") must be rejected HTTP 400
	// rather than bypassing orgPolicies["org-b"] onto platform default!
	for _, badOrg := range []string{"org-b ", " org-b", "org\tb", "org\x00b"} {
		_, err := r.Route(RouteRequest{Org: badOrg, Prompt: "test"}, StrategyEnsoHybridConstrained)
		if !IsRouteErrorCode(err, ErrCodeInvalidRequestParams) {
			t.Fatalf("expected HTTP 400 %s for malformed org %q, got %v", ErrCodeInvalidRequestParams, badOrg, err)
		}
	}
}

// 17. NaN/Inf rejection, symmetric parameter conflict checks, and unknown strategy guard.
func TestNonFiniteNumericAndSymmetricParameterRejection(t *testing.T) {
	r, _, catalog := buildFixtureRouter(t)
	r.SetOrgPolicy("org-ent", ScopePolicy{PlanTier: PlanEnterprise, HasPlanTier: true, RequireZeroRetention: true})

	nanTemp := math.NaN()
	badReqs := []RouteRequest{
		{Org: "org-ent", Prompt: "hi", QualityFloor: math.NaN()},
		{Org: "org-ent", Prompt: "hi", QualityFloor: math.Inf(1)},
		{Org: "org-ent", Prompt: "hi", QualityTolerance: math.NaN()},
		{Org: "org-ent", Prompt: "hi", QualityRetentionRatio: math.Inf(-1)},
		{Org: "org-ent", Prompt: "hi", Temperature: &nanTemp},
		// Mandatory thinking + forced tool_choice conflict (symmetric with PinnedTurnModel check)
		{Org: "org-ent", Prompt: "prove theorem", ThinkingMode: ThinkingMandatory, ToolChoice: ToolChoiceRequired},
		// Mandatory-thinking pinned model + ThinkingOff conflict
		{Org: "org-ent", Prompt: "prove theorem", PinnedTurnModel: "zen-4-thinking", ThinkingMode: ThinkingOff},
		// Mandatory-thinking pinned model + MaxOutputTokens <= MinThinkingBudgetToks
		{Org: "org-ent", Prompt: "prove theorem", PinnedTurnModel: "zen-4-thinking", MaxOutputTokens: 64},
	}
	for i, req := range badReqs {
		_, err := r.Route(req, StrategyEnsoHybridConstrained)
		if !IsRouteErrorCode(err, ErrCodeInvalidRequestParams) {
			t.Fatalf("case %d: expected %s, got %v", i, ErrCodeInvalidRequestParams, err)
		}
	}

	// Unknown RouterStrategy rejected HTTP 400
	if _, err := r.Route(RouteRequest{Org: "org-ent", Prompt: "hi"}, RouterStrategy("bogus_strategy")); !IsRouteErrorCode(err, ErrCodeInvalidRequestParams) {
		t.Fatalf("expected HTTP 400 for unknown strategy, got %v", err)
	}

	// NaN feedback rejected HTTP 400
	if err := r.RecordFeedback("org-ent", "any-id", math.NaN()); !IsRouteErrorCode(err, ErrCodeInvalidRequestParams) {
		t.Fatalf("expected HTTP 400 for NaN reward, got %v", err)
	}

	// Non-finite training samples are safely ignored without poisoning FitTrainedWeights
	nanSample := TrainingSample{
		QueryID:  "bad-q",
		Features: FeatureVec{math.NaN()},
		ModelID:  "zen-4-max",
		Passed:   math.NaN(),
	}
	tw := FitTrainedWeights([]TrainingSample{nanSample}, catalog, 1234)
	p, ok := tw.PredictQuality(FeatureVec{}, "zen-4-max", false)
	if !ok || math.IsNaN(p) || math.IsInf(p, 0) {
		t.Fatalf("FitTrainedWeights poisoned by NaN sample: p=%v ok=%v", p, ok)
	}
}

// 18. Policy MinQualityFloor never loosens default relative floor; MaxOutputTokens cost/latency
// ceiling honored; impossible SLO in FastMode yields 422 GateSLOCeiling instead of false 402.
func TestPolicyFloorNonLooseningAndTokenCeilingAttribution(t *testing.T) {
	r, _, _ := buildFixtureRouter(t)
	r.SetOrgPolicy("org-default-floor", ScopePolicy{
		PlanTier:             PlanEnterprise,
		HasPlanTier:          true,
		RequireZeroRetention: true,
	})
	r.SetOrgPolicy("org-low-min-floor", ScopePolicy{
		PlanTier:             PlanEnterprise,
		HasPlanTier:          true,
		RequireZeroRetention: true,
		MinQualityFloor:      0.50, // lower than maxQ - 0.04 -> must NOT loosen default floor!
	})

	decDefault, err := r.Route(RouteRequest{
		Org:    "org-default-floor",
		Prompt: "Prove by mathematical induction that symmetric positive-definite matrices have positive eigenvalues.",
	}, StrategyEnsoHybridConstrained)
	if err != nil {
		t.Fatalf("default-floor route failed: %v", err)
	}
	decMinFloor, err := r.Route(RouteRequest{
		Org:    "org-low-min-floor",
		Prompt: "Prove by mathematical induction that symmetric positive-definite matrices have positive eigenvalues.",
	}, StrategyEnsoHybridConstrained)
	if err != nil {
		t.Fatalf("low-min-floor route failed: %v", err)
	}
	if decMinFloor.EffectiveQualityFloor+1e-9 < decDefault.EffectiveQualityFloor {
		t.Fatalf("MinQualityFloor=0.50 loosened default floor from %.4f to %.4f", decDefault.EffectiveQualityFloor, decMinFloor.EffectiveQualityFloor)
	}

	// BlockedModels in child policy purges inherited EntitledModels whitelist
	parent := ScopePolicy{PlanTier: PlanEnterprise, HasPlanTier: true, EntitledModels: []string{"zen-4-flash", "zen-4-pro"}}
	child := ScopePolicy{BlockedModels: []string{"zen-4-flash"}}
	eff := FoldPolicies("org-x", "proj-y", nil, &parent, &child)
	if eff.EntitledModels["zen-4-flash"] || !eff.EntitledModels["zen-4-pro"] {
		t.Fatalf("FoldPolicies failed to purge blocked model from EntitledModels: %+v", eff.EntitledModels)
	}

	// MaxOutputTokens honored when EstOutputTokens == 0: 20,000 output tokens on FastMode
	// exceeds 500 micro-USD prepaid balance -> 402 budget_exhausted!
	_, err = r.Route(RouteRequest{
		Org:                  "org-default-floor",
		Prompt:               "Write an exhaustive book chapter.",
		MaxOutputTokens:      20_000,
		FastMode:             true,
		HasPrepaidBalance:    true,
		PrepaidBalanceMicros: 500, // 20k output tokens on zen-4-flash costs >= 12,000 micro-USD
	}, StrategyEnsoHybridConstrained)
	if !IsRouteErrorCode(err, ErrCodeBudgetExhausted) {
		t.Fatalf("expected %s when MaxOutputTokens exceeds prepaid balance, got %v", ErrCodeBudgetExhausted, err)
	}

	// FastMode=true with ample prepaid balance (1,000,000 micros) MUST fail with 422
	// ErrCodeNoEligibleModel (GateSLOCeiling) when MaxLatencyMs=1ms, NOT false 402 budget_exhausted!
	r.SetProjectPolicy("org-default-floor", "impossible-slo", ScopePolicy{MaxLatencyMs: 1})
	_, err = r.Route(RouteRequest{
		Org:                  "org-default-floor",
		Project:              "impossible-slo",
		Prompt:               "Quick ping",
		FastMode:             true,
		HasPrepaidBalance:    true,
		PrepaidBalanceMicros: 1_000_000,
	}, StrategyEnsoHybridConstrained)
	if !IsRouteErrorCode(err, ErrCodeNoEligibleModel) {
		t.Fatalf("expected 422 %s under impossible MaxLatencyMs=1ms, got %v", ErrCodeNoEligibleModel, err)
	}
}

// 19. Implicit PriorThinkingFamily inference from message history + opt-in global training export.
func TestImplicitThinkingContinuityAndOptInTrainingExport(t *testing.T) {
	r, _, _ := buildFixtureRouter(t)
	r.SetOrgPolicy("org-opt-in", ScopePolicy{
		PlanTier:             PlanEnterprise,
		HasPlanTier:          true,
		RequireZeroRetention: false,
		TrainingOptIn:        true,
	})
	r.SetOrgPolicy("org-opt-out", ScopePolicy{
		PlanTier:             PlanEnterprise,
		HasPlanTier:          true,
		RequireZeroRetention: false,
		TrainingOptIn:        false,
	})

	// RequireThinkingContinuity=true with PriorThinkingFamily="" infers "anthropic-extended"
	// directly from Messages[0].ThinkingBlocks!
	dec, err := r.Route(RouteRequest{
		RequestID:                 "req-inferred-family",
		Org:                       "org-opt-in",
		Prompt:                    "Continue step 2 of the proof.",
		RequireThinkingContinuity: true,
		Messages: []ConversationMessage{{
			Role:    "assistant",
			Content: "Step 1 complete.",
			ThinkingBlocks: []ThinkingBlock{{
				ThinkingFamily: "anthropic-extended",
				SourceModel:    "frontier-claude-sonnet",
				Signature:      "sig-1",
				Summary:        "step 1 trace",
			}},
		}},
	}, StrategyEnsoHybridConstrained)
	if err != nil {
		t.Fatalf("implicit continuity route failed: %v", err)
	}
	if dec.RoutedModel != "frontier-claude-sonnet" || dec.StrippedThinkingBlocks != 0 {
		t.Fatalf("expected frontier-claude-sonnet with 0 stripped blocks, got %+v", dec)
	}

	// Route on opt-out org and record feedback on both orgs
	if _, err := r.Route(RouteRequest{
		RequestID: "req-private",
		Org:       "org-opt-out",
		Prompt:    "Confidential enterprise task",
	}, StrategyEnsoHybridConstrained); err != nil {
		t.Fatalf("opt-out route failed: %v", err)
	}
	if err := r.RecordFeedback("org-opt-in", "req-inferred-family", 1.0); err != nil {
		t.Fatalf("opt-in feedback failed: %v", err)
	}
	if err := r.RecordFeedback("org-opt-out", "req-private", 1.0); err != nil {
		t.Fatalf("opt-out feedback failed: %v", err)
	}

	exported := r.ExportOptInGlobalTrainingSamples()
	if len(exported) != 1 || !strings.HasPrefix(exported[0].QueryID, "org-opt-in:") {
		t.Fatalf("ExportOptInGlobalTrainingSamples leaked opt-out org or missed opt-in org: %+v", exported)
	}
}

// 20. Overflow/wraparound saturation on EstimateCostMicros/EstimateLatencyMs, token ceilings,
// unpinned MandatoryThinking compatibility filter, extreme finite training samples, and empty heldOut gate.
func TestOverflowSaturationAndUnpinnedMandatoryThinkingHardening(t *testing.T) {
	r, _, catalog := buildFixtureRouter(t)
	r.SetOrgPolicy("org-ent", ScopePolicy{PlanTier: PlanEnterprise, HasPlanTier: true, RequireZeroRetention: true})

	// 1. Direct saturation check on astronomical token count (1e18): must saturate to MaxInt64/MaxInt32,
	// NEVER wrap negative to MinInt64 -> 1 micro-USD!
	var maxArm ModelArm
	for _, a := range catalog {
		if a.ID == "zen-4-max" {
			maxArm = a
			break
		}
	}
	if got := EstimateCostMicros(maxArm, 1_000_000_000_000_000_000, 1_000_000_000_000_000_000); got != math.MaxInt64 {
		t.Fatalf("EstimateCostMicros failed to saturate at MaxInt64: got %d", got)
	}
	if got := EstimateLatencyMs(maxArm, 1_000_000_000_000_000_000); got != math.MaxInt32 {
		t.Fatalf("EstimateLatencyMs failed to saturate at MaxInt32: got %d", got)
	}

	// 2. ValidateRequestSemantics rejects astronomical token/tool counts and whitespace-only RequestID with HTTP 400.
	if _, err := r.Route(RouteRequest{
		Org:                  "org-ent",
		Prompt:               "hi",
		EstInputTokens:       1_000_000_000_000_000_000,
		FastMode:             true,
		HasPrepaidBalance:    true,
		PrepaidBalanceMicros: 1,
	}, StrategyEnsoHybridConstrained); !IsRouteErrorCode(err, ErrCodeInvalidRequestParams) {
		t.Fatalf("expected HTTP 400 on astronomical EstInputTokens, got %v", err)
	}
	if _, err := r.Route(RouteRequest{
		RequestID: "   ",
		Org:       "org-ent",
		Prompt:    "hi",
	}, StrategyEnsoHybridConstrained); !IsRouteErrorCode(err, ErrCodeInvalidRequestParams) {
		t.Fatalf("expected HTTP 400 on whitespace-only RequestID, got %v", err)
	}

	// 3. Unpinned request with Temperature=0.2 or MaxOutputTokens=64 MUST filter out MandatoryThinking arms!
	temp02 := 0.2
	decTemp, err := r.Route(RouteRequest{
		Org:          "org-ent",
		Prompt:       "Prove theorem by mathematical induction and eigenvalue bound.",
		ThinkingMode: ThinkingAdaptive,
		Temperature:  &temp02,
	}, StrategyEnsoHybridConstrained)
	if err != nil {
		t.Fatalf("adaptive temp=0.2 route failed: %v", err)
	}
	if decTemp.RoutedModel == "zen-4-thinking" {
		t.Fatalf("unpinned request with temperature=0.2 illegally routed to mandatory-thinking arm zen-4-thinking")
	}

	decShort, err := r.Route(RouteRequest{
		Org:             "org-ent",
		Prompt:          "Prove theorem by mathematical induction and eigenvalue bound.",
		ThinkingMode:    ThinkingAdaptive,
		MaxOutputTokens: 64,
	}, StrategyEnsoHybridConstrained)
	if err != nil {
		t.Fatalf("adaptive max_output_tokens=64 route failed: %v", err)
	}
	if decShort.RoutedModel == "zen-4-thinking" {
		t.Fatalf("unpinned request with max_output_tokens=64 illegally routed to mandatory-thinking arm zen-4-thinking")
	}

	// 4. Extreme finite feature values (1e200) in TrainingSample must not overflow Hessian/SGD or panic computeWeightDigests,
	// and empty heldOut slice must NEVER promote candidate weights in RetrainOrgPromoteOrKeep!
	extremeSample := TrainingSample{
		QueryID:  "q-extreme",
		Features: FeatureVec{1e200, -1e200},
		ModelID:  "zen-4-max",
		Passed:   1.0,
	}
	tw := FitTrainedWeights([]TrainingSample{extremeSample}, catalog, 77)
	if p, ok := tw.PredictQuality(FeatureVec{}, "zen-4-max", false); !ok || !isFinite(p) {
		t.Fatalf("extreme finite sample corrupted FitTrainedWeights: p=%v ok=%v", p, ok)
	}
	verdict, err := r.RetrainOrgPromoteOrKeep("org-ent", []TrainingSample{extremeSample}, nil, 0.5, 0.0, 77)
	if err != nil || verdict.Promoted {
		t.Fatalf("empty heldOut slice must never promote candidate weights: verdict=%+v err=%v", verdict, err)
	}
}
