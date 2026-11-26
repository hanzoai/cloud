// Copyright 2023-2026 Hanzo AI Inc.
// SPDX-License-Identifier: Apache-2.0

package agenttrace

import (
	"fmt"
	"math"
	"sort"
)

// RateCard defines deterministic integer micro-USD unit rates used to compute
// and reconcile Cloud billing against emitted subsystem spans under one task_id.
// All arithmetic is exact integer math so billing reconciliation has zero
// floating-point drift.
type RateCard struct {
	// Model token pricing (micro-USD per 1,000 tokens).
	InputTokenMicroUSDPer1K  int64 `json:"inputTokenMicroUsdPer1k"`
	OutputTokenMicroUSDPer1K int64 `json:"outputTokenMicroUsdPer1k"`
	CachedTokenMicroUSDPer1K int64 `json:"cachedTokenMicroUsdPer1k"`

	// Sandbox compute pricing (micro-USD per millisecond of sandbox boot/compute).
	SandboxMicroUSDPerMs int64 `json:"sandboxMicroUsdPerMs"`

	// VFS / Hanzo S3 storage op pricing (micro-USD per S3 PUT/GET).
	S3PutMicroUSD int64 `json:"s3PutMicroUsd"`
	S3GetMicroUSD int64 `json:"s3GetMicroUsd"`

	// Retrieval & MCP per-call metering (micro-USD per operation).
	RetrievalCallMicroUSD int64 `json:"retrievalCallMicroUsd"`
	MCPCallMicroUSD       int64 `json:"mcpCallMicroUsd"`
}

// DefaultRateCard returns the canonical Hanzo Cloud micro-USD metering rates.
func DefaultRateCard() RateCard {
	return RateCard{
		InputTokenMicroUSDPer1K:  3_000,  // $0.003 / 1K input tokens
		OutputTokenMicroUSDPer1K: 15_000, // $0.015 / 1K output tokens
		CachedTokenMicroUSDPer1K: 300,    // $0.0003 / 1K cached tokens (90% discount)
		SandboxMicroUSDPerMs:     5,      // 5 micro-USD per sandbox compute ms
		S3PutMicroUSD:            5,      // $0.000005 / PUT
		S3GetMicroUSD:            1,      // $0.000001 / GET
		RetrievalCallMicroUSD:    25,     // $0.000025 / retrieval query
		MCPCallMicroUSD:          50,     // $0.000050 / MCP tool call
	}
}

// ModelCostMicroUSD computes deterministic micro-USD cost for a model span.
func (r RateCard) ModelCostMicroUSD(inputTokens, outputTokens, cachedTokens int64) int64 {
	if inputTokens < 0 {
		inputTokens = 0
	}
	if outputTokens < 0 {
		outputTokens = 0
	}
	if cachedTokens < 0 {
		cachedTokens = 0
	}
	uncached := inputTokens - cachedTokens
	if uncached < 0 {
		uncached = 0
	}
	inCost := ceilDiv(uncached*r.InputTokenMicroUSDPer1K, 1000)
	cacheCost := ceilDiv(cachedTokens*r.CachedTokenMicroUSDPer1K, 1000)
	outCost := ceilDiv(outputTokens*r.OutputTokenMicroUSDPer1K, 1000)
	return inCost + cacheCost + outCost
}

func ceilDiv(a, b int64) int64 {
	if a <= 0 || b <= 0 {
		return 0
	}
	return (a + b - 1) / b
}

// UsageSummary aggregates resource usage across all non-billing spans in a task.
type UsageSummary struct {
	ExpectedCostMicroUSD int64 `json:"expectedCostMicroUsd"`
	InputTokens          int64 `json:"inputTokens"`
	OutputTokens         int64 `json:"outputTokens"`
	CachedTokens         int64 `json:"cachedTokens"`
	TokenUnits           int64 `json:"tokenUnits"`
	ComputeUnits         int64 `json:"computeUnits"`
	StorageOps           int64 `json:"storageOps"`
	ModelCalls           int   `json:"modelCalls"`
	RetrievalCalls       int   `json:"retrievalCalls"`
	MCPCalls             int   `json:"mcpCalls"`
	VFSOps               int   `json:"vfsOps"`
	SandboxBoots         int   `json:"sandboxBoots"`
}

// ComputeExpectedBilling computes the canonical expected billing totals directly
// from the emitted subsystem spans of a single task.
func ComputeExpectedBilling(spans []Span, rc RateCard) UsageSummary {
	var u UsageSummary
	for i := range spans {
		sp := &spans[i]
		switch sp.Subsystem {
		case SubsystemModel:
			u.ModelCalls++
			inTok, _ := sp.IntAttr(AttrGenAIUsageInputTokens)
			outTok, _ := sp.IntAttr(AttrGenAIUsageOutputTokens)
			cacheTok, ok := sp.IntAttr(AttrGenAIUsageCacheReadTokens)
			if !ok {
				cacheTok, _ = sp.IntAttr(AttrModelCachedTokens)
			}
			u.InputTokens += inTok
			u.OutputTokens += outTok
			u.CachedTokens += cacheTok
			u.TokenUnits += inTok + outTok
			if explicitCost, hasCost := sp.IntAttr(AttrModelCostMicroUSD); hasCost && explicitCost > 0 {
				u.ExpectedCostMicroUSD += explicitCost
			} else {
				u.ExpectedCostMicroUSD += rc.ModelCostMicroUSD(inTok, outTok, cacheTok)
			}
		case SubsystemRetrieval:
			u.RetrievalCalls++
			u.ComputeUnits++
			u.ExpectedCostMicroUSD += rc.RetrievalCallMicroUSD
		case SubsystemMCP:
			u.MCPCalls++
			latMs, _ := sp.FloatAttr(AttrMCPLatencyMs)
			units := int64(math.Ceil(latMs))
			if units < 1 {
				units = 1
			}
			u.ComputeUnits += units
			u.ExpectedCostMicroUSD += rc.MCPCallMicroUSD
		case SubsystemVFS:
			u.VFSOps++
			puts, _ := sp.IntAttr(AttrVFSS3PutCount)
			gets, _ := sp.IntAttr(AttrVFSS3GetCount)
			if puts < 0 {
				puts = 0
			}
			if gets < 0 {
				gets = 0
			}
			u.StorageOps += puts + gets
			u.ExpectedCostMicroUSD += puts*rc.S3PutMicroUSD + gets*rc.S3GetMicroUSD
		case SubsystemSandbox:
			u.SandboxBoots++
			bootMs, _ := sp.FloatAttr(AttrVisorBootMs)
			ms := int64(math.Ceil(bootMs))
			if ms < 1 {
				ms = 1
			}
			u.ComputeUnits += ms
			u.ExpectedCostMicroUSD += ms * rc.SandboxMicroUSDPerMs
		}
	}
	return u
}

// VerifyOptions controls optional strictness parameters for VerifyTaskCompleteness.
type VerifyOptions struct {
	RateCard                RateCard
	MaxBillingDeltaMicroUSD int64 // Default 0 (exact 1:1 reconciliation)
	ExpectedOrgID           string
	ExpectedTenantID        string
}

// CompletenessReport summarizes every HIP-1348 verification invariant for a task trace.
type CompletenessReport struct {
	TaskID               string      `json:"taskId"`
	TraceID              string      `json:"traceId"`
	OrgID                string      `json:"orgId"`
	TenantID             string      `json:"tenantId"`
	Complete             bool        `json:"complete"`
	SpanCount            int         `json:"spanCount"`
	SubsystemsPresent    []Subsystem `json:"subsystemsPresent"`
	MissingSubsystems    []Subsystem `json:"missingSubsystems,omitempty"`
	SubsystemCoverage    float64     `json:"subsystemCoverage"`
	RootSpanCount        int         `json:"rootSpanCount"`
	OrphanSpanCount      int         `json:"orphanSpanCount"`
	CycleDetected        bool        `json:"cycleDetected"`
	TaskIDConsistent     bool        `json:"taskIdConsistent"`
	TraceIDConsistent    bool        `json:"traceIdConsistent"`
	TenantConsistent     bool        `json:"tenantConsistent"`
	TimestampsValid      bool        `json:"timestampsValid"`
	BillingSpanCount     int         `json:"billingSpanCount"`
	ExpectedCostMicroUSD int64       `json:"expectedCostMicroUsd"`
	BilledCostMicroUSD   int64       `json:"billedCostMicroUsd"`
	BillingDeltaMicroUSD int64       `json:"billingDeltaMicroUsd"`
	ExpectedTokenUnits   int64       `json:"expectedTokenUnits"`
	BilledTokenUnits     int64       `json:"billedTokenUnits"`
	ExpectedComputeUnits int64       `json:"expectedComputeUnits"`
	BilledComputeUnits   int64       `json:"billedComputeUnits"`
	BillingReconciled    bool        `json:"billingReconciled"`
	SecretLeakCount      int         `json:"secretLeakCount"`
	Violations           []string    `json:"violations,omitempty"`
}

// VerifyTaskCompleteness checks whether spans form a complete, connected,
// tenant-isolated, DLP-clean, 1:1 billing-reconciled unified trace across all
// 6 Hanzo subsystems under a single task_id and W3C trace_id.
func VerifyTaskCompleteness(spans []Span, opts VerifyOptions) CompletenessReport {
	rc := opts.RateCard
	if rc == (RateCard{}) {
		rc = DefaultRateCard()
	}

	rep := CompletenessReport{
		SpanCount:         len(spans),
		TaskIDConsistent:  true,
		TraceIDConsistent: true,
		TenantConsistent:  true,
		TimestampsValid:   true,
	}

	if len(spans) == 0 {
		rep.MissingSubsystems = append([]Subsystem(nil), AllSubsystems...)
		rep.Violations = append(rep.Violations, "empty trace: 0 spans")
		return rep
	}

	// Pick canonical root anchor (prefer the span with empty ParentSpanID).
	anchor := &spans[0]
	for i := range spans {
		if spans[i].ParentSpanID.IsZero() {
			anchor = &spans[i]
			break
		}
	}
	rep.TaskID = anchor.TaskID
	rep.TraceID = anchor.TraceID.String()
	rep.OrgID = anchor.OrgID
	rep.TenantID = anchor.TenantID

	spanIndex := make(map[SpanID]*Span, len(spans))
	presentSubs := make(map[Subsystem]bool, len(AllSubsystems))

	var billedCost, billedTokens, billedCompute int64

	for i := range spans {
		sp := &spans[i]
		if sp.TraceID.IsZero() || sp.SpanID.IsZero() {
			rep.TraceIDConsistent = false
			rep.Violations = append(rep.Violations, fmt.Sprintf("span %q has zero trace_id or span_id", sp.Name))
		}
		if _, dup := spanIndex[sp.SpanID]; dup {
			rep.Violations = append(rep.Violations, fmt.Sprintf("duplicate span_id %s", sp.SpanID.String()))
		}
		spanIndex[sp.SpanID] = sp

		if sp.TaskID == "" || sp.TaskID != rep.TaskID {
			rep.TaskIDConsistent = false
			rep.Violations = append(rep.Violations, fmt.Sprintf("span %s task_id mismatch: got %q want %q", sp.SpanID.String(), sp.TaskID, rep.TaskID))
		}
		if sp.TraceID != anchor.TraceID {
			rep.TraceIDConsistent = false
			rep.Violations = append(rep.Violations, fmt.Sprintf("span %s trace_id mismatch: got %s want %s", sp.SpanID.String(), sp.TraceID.String(), anchor.TraceID.String()))
		}
		if sp.OrgID == "" || sp.OrgID != rep.OrgID || sp.TenantID != rep.TenantID {
			rep.TenantConsistent = false
			rep.Violations = append(rep.Violations, fmt.Sprintf("span %s tenant/org contamination: got (%q,%q) want (%q,%q)", sp.SpanID.String(), sp.OrgID, sp.TenantID, rep.OrgID, rep.TenantID))
		}
		if opts.ExpectedOrgID != "" && sp.OrgID != opts.ExpectedOrgID {
			rep.TenantConsistent = false
			rep.Violations = append(rep.Violations, fmt.Sprintf("span %s org_id %q != expected %q", sp.SpanID.String(), sp.OrgID, opts.ExpectedOrgID))
		}
		if opts.ExpectedTenantID != "" && sp.TenantID != opts.ExpectedTenantID {
			rep.TenantConsistent = false
			rep.Violations = append(rep.Violations, fmt.Sprintf("span %s tenant_id %q != expected %q", sp.SpanID.String(), sp.TenantID, opts.ExpectedTenantID))
		}
		if sp.StartTimeNano <= 0 || sp.EndTimeNano < sp.StartTimeNano {
			rep.TimestampsValid = false
			rep.Violations = append(rep.Violations, fmt.Sprintf("span %s invalid timestamps (%d..%d)", sp.SpanID.String(), sp.StartTimeNano, sp.EndTimeNano))
		}
		if sp.ParentSpanID.IsZero() {
			rep.RootSpanCount++
		}

		// Verify task_id attribute consistency if present.
		if attrTask, ok := sp.StrAttr(AttrHanzoTaskID); !ok || attrTask != rep.TaskID {
			rep.TaskIDConsistent = false
			rep.Violations = append(rep.Violations, fmt.Sprintf("span %s missing or mismatched %s attribute", sp.SpanID.String(), AttrHanzoTaskID))
		}

		if sp.Subsystem != SubsystemRoot && sp.Subsystem.Valid() {
			presentSubs[sp.Subsystem] = true
		}

		if sp.Subsystem == SubsystemBilling {
			rep.BillingSpanCount++
			cost, _ := sp.IntAttr(AttrBillingCostMicroUSD)
			toks, _ := sp.IntAttr(AttrBillingTokenUnits)
			comp, _ := sp.IntAttr(AttrBillingComputeUnits)
			billedCost += cost
			billedTokens += toks
			billedCompute += comp
		}

		// DLP secret-leak audit across every attribute and status message.
		if ContainsSecret(sp.StatusMessage) {
			rep.SecretLeakCount++
			rep.Violations = append(rep.Violations, fmt.Sprintf("span %s status_message leaked secret/PII", sp.SpanID.String()))
		}
		for _, a := range sp.Attributes {
			if a.Kind != AttrString {
				continue
			}
			if isSensitiveKey(a.Key) && a.Str != redactedPlaceholder && a.Str != "" {
				rep.SecretLeakCount++
				rep.Violations = append(rep.Violations, fmt.Sprintf("span %s sensitive key %q not redacted", sp.SpanID.String(), a.Key))
			} else if ContainsSecret(a.Str) {
				rep.SecretLeakCount++
				rep.Violations = append(rep.Violations, fmt.Sprintf("span %s attribute %q leaked secret/PII", sp.SpanID.String(), a.Key))
			}
		}
	}

	// Verify parent-child DAG connectivity (0 orphans, 0 cycles, exactly 1 root).
	if rep.RootSpanCount != 1 {
		rep.Violations = append(rep.Violations, fmt.Sprintf("expected exactly 1 root span, got %d", rep.RootSpanCount))
	}
	for i := range spans {
		sp := &spans[i]
		if sp.ParentSpanID.IsZero() {
			continue
		}
		if _, ok := spanIndex[sp.ParentSpanID]; !ok {
			rep.OrphanSpanCount++
			rep.Violations = append(rep.Violations, fmt.Sprintf("orphan span %s references missing parent %s", sp.SpanID.String(), sp.ParentSpanID.String()))
			continue
		}
		// Walk parent chain to detect cycles.
		visited := make(map[SpanID]bool, 8)
		cur := sp
		for !cur.ParentSpanID.IsZero() {
			if visited[cur.SpanID] {
				rep.CycleDetected = true
				rep.Violations = append(rep.Violations, fmt.Sprintf("cycle detected at span %s", cur.SpanID.String()))
				break
			}
			visited[cur.SpanID] = true
			next, ok := spanIndex[cur.ParentSpanID]
			if !ok {
				break
			}
			cur = next
		}
	}

	// Check 6/6 Hanzo subsystem coverage.
	for _, sub := range AllSubsystems {
		if presentSubs[sub] {
			rep.SubsystemsPresent = append(rep.SubsystemsPresent, sub)
		} else {
			rep.MissingSubsystems = append(rep.MissingSubsystems, sub)
		}
	}
	sort.Slice(rep.SubsystemsPresent, func(i, j int) bool { return rep.SubsystemsPresent[i] < rep.SubsystemsPresent[j] })
	rep.SubsystemCoverage = float64(len(rep.SubsystemsPresent)) / float64(len(AllSubsystems))
	if len(rep.MissingSubsystems) > 0 {
		rep.Violations = append(rep.Violations, fmt.Sprintf("missing subsystems: %v", rep.MissingSubsystems))
	}

	// Reconcile billing 1:1 against emitted spans.
	usage := ComputeExpectedBilling(spans, rc)
	rep.ExpectedCostMicroUSD = usage.ExpectedCostMicroUSD
	rep.BilledCostMicroUSD = billedCost
	rep.BillingDeltaMicroUSD = billedCost - usage.ExpectedCostMicroUSD
	rep.ExpectedTokenUnits = usage.TokenUnits
	rep.BilledTokenUnits = billedTokens
	rep.ExpectedComputeUnits = usage.ComputeUnits
	rep.BilledComputeUnits = billedCompute

	absDelta := rep.BillingDeltaMicroUSD
	if absDelta < 0 {
		absDelta = -absDelta
	}
	if rep.BillingSpanCount > 0 &&
		absDelta <= opts.MaxBillingDeltaMicroUSD &&
		billedTokens == usage.TokenUnits &&
		billedCompute == usage.ComputeUnits {
		rep.BillingReconciled = true
	} else {
		rep.BillingReconciled = false
		rep.Violations = append(rep.Violations, fmt.Sprintf(
			"billing mismatch: spans=%d cost_delta=%d (billed=%d expected=%d) tokens=(%d/%d) compute=(%d/%d)",
			rep.BillingSpanCount, rep.BillingDeltaMicroUSD, billedCost, usage.ExpectedCostMicroUSD,
			billedTokens, usage.TokenUnits, billedCompute, usage.ComputeUnits,
		))
	}

	rep.Complete = len(rep.MissingSubsystems) == 0 &&
		rep.RootSpanCount == 1 &&
		rep.OrphanSpanCount == 0 &&
		!rep.CycleDetected &&
		rep.TaskIDConsistent &&
		rep.TraceIDConsistent &&
		rep.TenantConsistent &&
		rep.TimestampsValid &&
		rep.BillingReconciled &&
		rep.SecretLeakCount == 0 &&
		len(rep.Violations) == 0

	return rep
}
