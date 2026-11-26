// Copyright 2023-2026 Hanzo AI Inc.
// SPDX-License-Identifier: Apache-2.0

package agenttrace

import (
	"context"
	"fmt"
	"math"
	"time"
)

// TracerConfig configures a unified HIP-1348 agent execution Tracer.
type TracerConfig struct {
	ServiceName       string
	CaptureMode       ContentCaptureMode
	RingCapacity      int
	BatchSize         int
	FlushInterval     time.Duration
	BackpressureMode  BackpressureMode
	RateCard          RateCard
	DeterministicSeed uint64
	Sink              Sink
	EnableStore       bool
	StoreConfig       TraceStoreConfig
}

// Tracer emits OpenTelemetry GenAI spans across all six Hanzo subsystems under
// one unified task_id and W3C Trace Context, enforcing DLP redaction and
// non-blocking bounded ring-buffer batch export.
type Tracer struct {
	captureMode ContentCaptureMode
	rateCard    RateCard
	ids         *idGenerator
	exporter    *BatchExporter
	store       *TraceStore
}

// NewTracer constructs a Tracer and starts its background BatchExporter.
func NewTracer(cfg TracerConfig) *Tracer {
	mode := cfg.CaptureMode
	if !mode.Valid() {
		mode = CaptureRedact
	}
	rc := cfg.RateCard
	if rc == (RateCard{}) {
		rc = DefaultRateCard()
	}

	var store *TraceStore
	sink := cfg.Sink
	if cfg.EnableStore {
		store = NewTraceStore(cfg.StoreConfig)
		if sink != nil {
			sink = &MultiSink{Sinks: []Sink{store, sink}}
		} else {
			sink = store
		}
	}

	exp := NewBatchExporter(BatchExporterConfig{
		RingCapacity:     cfg.RingCapacity,
		BatchSize:        cfg.BatchSize,
		FlushInterval:    cfg.FlushInterval,
		BackpressureMode: cfg.BackpressureMode,
		ServiceName:      cfg.ServiceName,
		Sink:             sink,
	})

	return &Tracer{
		captureMode: mode,
		rateCard:    rc,
		ids:         newIDGenerator(cfg.DeterministicSeed),
		exporter:    exp,
		store:       store,
	}
}

// Store returns the attached multi-tenant TraceStore, if enabled.
func (t *Tracer) Store() *TraceStore { return t.store }

// RateCard returns the active billing reconciliation RateCard.
func (t *Tracer) RateCard() RateCard { return t.rateCard }

// CaptureMode returns the active DLP ContentCaptureMode.
func (t *Tracer) CaptureMode() ContentCaptureMode { return t.captureMode }

// Flush drains all buffered spans to the configured sink.
func (t *Tracer) Flush(ctx context.Context) error {
	return t.exporter.Flush(ctx)
}

// Shutdown drains remaining spans and stops the background exporter goroutine.
func (t *Tracer) Shutdown(ctx context.Context) error {
	return t.exporter.Shutdown(ctx)
}

// Stats returns ring buffer and batch exporter diagnostics.
func (t *Tracer) Stats() ExporterStats {
	return t.exporter.Stats()
}

func (t *Tracer) enqueue(sp Span) {
	t.exporter.Enqueue(sp)
}

// StartTask begins the root agent execution span ("invoke_agent") under a fresh
// 128-bit W3C TraceID and binds the resulting TaskContext onto the returned context.
func (t *Tracer) StartTask(ctx context.Context, taskID, orgID, tenantID, agentName string) (context.Context, *ActiveSpan, error) {
	if !ValidateIdentifier(taskID) {
		return ctx, nil, ErrInvalidTaskID
	}
	if !ValidateIdentifier(orgID) {
		return ctx, nil, ErrInvalidOrgID
	}
	if tenantID == "" {
		tenantID = orgID
	} else if !ValidateIdentifier(tenantID) {
		return ctx, nil, ErrInvalidOrgID
	}
	if agentName == "" {
		agentName = "kai-agent"
	}

	now := time.Now().UnixNano()
	tid := t.ids.nextTraceID()
	sid := t.ids.nextSpanID()

	attrs := make([]Attr, 0, 8)
	attrs = append(attrs,
		StringAttr(AttrGenAIOperationName, OpInvokeAgent),
		StringAttr(AttrGenAISystem, SystemHanzoKai),
		StringAttr(AttrGenAIAgentName, agentName),
		StringAttr(AttrHanzoTaskID, taskID),
		StringAttr(AttrHanzoOrgID, orgID),
		StringAttr(AttrHanzoTenantID, tenantID),
		StringAttr(AttrHanzoSubsystem, string(SubsystemRoot)),
		StringAttr(AttrCaptureMode, string(t.captureMode)),
	)

	sp := &ActiveSpan{
		tracer: t,
		span: Span{
			TaskID:        taskID,
			OrgID:         orgID,
			TenantID:      tenantID,
			Subsystem:     SubsystemRoot,
			TraceID:       tid,
			SpanID:        sid,
			Name:          "agent.task",
			Kind:          SpanKindServer,
			StartTimeNano: now,
			Attributes:    attrs,
		},
	}
	return WithTaskContext(ctx, sp.Context()), sp, nil
}

// startChild allocates a child span linked to the parent TaskContext in ctx.
func (t *Tracer) startChild(ctx context.Context, sub Subsystem, name string, kind SpanKind, attrCap int) (context.Context, *ActiveSpan, error) {
	parent, ok := TaskContextFrom(ctx)
	if !ok || !parent.Valid() {
		return ctx, nil, fmt.Errorf("%w: missing parent task context for %s", ErrInvalidTaskID, sub)
	}
	now := time.Now().UnixNano()
	sid := t.ids.nextSpanID()

	attrs := make([]Attr, 0, attrCap+4)
	attrs = append(attrs,
		StringAttr(AttrHanzoTaskID, parent.TaskID),
		StringAttr(AttrHanzoOrgID, parent.OrgID),
		StringAttr(AttrHanzoTenantID, parent.TenantID),
		StringAttr(AttrHanzoSubsystem, string(sub)),
	)

	sp := &ActiveSpan{
		tracer: t,
		span: Span{
			TaskID:        parent.TaskID,
			OrgID:         parent.OrgID,
			TenantID:      parent.TenantID,
			Subsystem:     sub,
			TraceID:       parent.TraceID,
			SpanID:        sid,
			ParentSpanID:  parent.SpanID,
			Name:          name,
			Kind:          kind,
			StartTimeNano: now,
			Attributes:    attrs,
		},
	}
	return WithTaskContext(ctx, sp.Context()), sp, nil
}

func (t *Tracer) appendExtraAttrs(sp *ActiveSpan, extras []Attr) {
	for _, a := range extras {
		clean, red := SanitizeAttr(a, t.captureMode)
		if red {
			sp.span.RedactedCount++
		}
		sp.span.Attributes = append(sp.span.Attributes, clean)
	}
}

// StartModelSpan starts a GenAI chat/inference span for Enso routing + Zen generation.
func (t *Tracer) StartModelSpan(ctx context.Context, in ModelSpanInput) (context.Context, *ActiveSpan, error) {
	cctx, sp, err := t.startChild(ctx, SubsystemModel, "gen_ai.chat", SpanKindClient, 16+len(in.ExtraAttributes))
	if err != nil {
		return ctx, nil, err
	}
	sys := in.System
	if sys == "" {
		sys = SystemHanzoZen
	}
	respModel := in.ResponseModel
	if respModel == "" {
		respModel = in.RequestModel
	}
	finish := in.FinishReason
	if finish == "" {
		finish = "stop"
	}
	cost := in.CostMicroUSD
	if cost <= 0 {
		cost = t.rateCard.ModelCostMicroUSD(in.InputTokens, in.OutputTokens, in.CachedTokens)
	}

	sp.span.Attributes = append(sp.span.Attributes,
		StringAttr(AttrGenAIOperationName, OpChat),
		StringAttr(AttrGenAISystem, sys),
		StringAttr(AttrGenAIRequestModel, in.RequestModel),
		StringAttr(AttrGenAIResponseModel, respModel),
		StringAttr(AttrGenAIResponseFinishReasons, finish),
		Int64Attr(AttrGenAIUsageInputTokens, in.InputTokens),
		Int64Attr(AttrGenAIUsageOutputTokens, in.OutputTokens),
		Int64Attr(AttrGenAIUsageCacheReadTokens, in.CachedTokens),
		Int64Attr(AttrModelCachedTokens, in.CachedTokens),
		Float64Attr(AttrModelTTFTMs, in.TTFTMs),
		Int64Attr(AttrModelCostMicroUSD, cost),
	)
	if in.EnsoRoute != "" {
		sp.span.Attributes = append(sp.span.Attributes, StringAttr(AttrModelEnsoRoute, in.EnsoRoute))
	}
	if in.Prompt != "" {
		val, red := SanitizeContent(in.Prompt, t.captureMode)
		if red {
			sp.span.RedactedCount++
		}
		sp.span.Attributes = append(sp.span.Attributes,
			StringAttr(AttrGenAIPrompt, val),
			Int64Attr(AttrPromptBytes, int64(len(in.Prompt))),
		)
	}
	if in.Completion != "" {
		val, red := SanitizeContent(in.Completion, t.captureMode)
		if red {
			sp.span.RedactedCount++
		}
		sp.span.Attributes = append(sp.span.Attributes,
			StringAttr(AttrGenAICompletion, val),
			Int64Attr(AttrCompletionBytes, int64(len(in.Completion))),
		)
	}
	t.appendExtraAttrs(sp, in.ExtraAttributes)
	return cctx, sp, nil
}

// StartRetrievalSpan starts a retrieval span for hanzoai/index or hanzoai/graph.
// Raw query text is hashed via SHA-256 and never emitted verbatim.
func (t *Tracer) StartRetrievalSpan(ctx context.Context, in RetrievalSpanInput) (context.Context, *ActiveSpan, error) {
	cctx, sp, err := t.startChild(ctx, SubsystemRetrieval, "retrieval.query", SpanKindClient, 10+len(in.ExtraAttributes))
	if err != nil {
		return ctx, nil, err
	}
	backend := in.Backend
	if backend == "" {
		backend = "index"
	}
	sys := SystemHanzoIndex
	if backend == "graph" {
		sys = SystemHanzoGraph
	}
	digest := in.QueryDigest
	if digest == "" {
		digest = DigestString(in.Query)
	}
	acl := in.ACLScope
	if acl == "" {
		acl = "org-default"
	}
	if t.captureMode != CaptureRaw {
		if cleaned, red := ScrubSecrets(acl); red {
			acl = cleaned
			sp.span.RedactedCount++
		}
	}

	sp.span.Attributes = append(sp.span.Attributes,
		StringAttr(AttrGenAIOperationName, OpRetrieval),
		StringAttr(AttrGenAISystem, sys),
		StringAttr(AttrRetrievalBackend, backend),
		StringAttr(AttrRetrievalQueryDigest, digest),
		Int64Attr(AttrRetrievalTopK, in.TopK),
		Int64Attr(AttrRetrievalHitCount, in.HitCount),
		StringAttr(AttrRetrievalACLScope, acl),
	)
	t.appendExtraAttrs(sp, in.ExtraAttributes)
	return cctx, sp, nil
}

// StartMCPSpan starts an MCP tool execution span (gen_ai.operation.name=execute_tool).
func (t *Tracer) StartMCPSpan(ctx context.Context, in MCPSpanInput) (context.Context, *ActiveSpan, error) {
	cctx, sp, err := t.startChild(ctx, SubsystemMCP, "mcp.execute_tool", SpanKindClient, 10+len(in.ExtraAttributes))
	if err != nil {
		return ctx, nil, err
	}
	status := in.Status
	if status == "" {
		status = "ok"
	}
	toolName := in.ToolName
	if t.captureMode != CaptureRaw {
		if cleaned, red := ScrubSecrets(toolName); red {
			toolName = cleaned
			sp.span.RedactedCount++
		}
	}

	sp.span.Attributes = append(sp.span.Attributes,
		StringAttr(AttrGenAIOperationName, OpExecuteTool),
		StringAttr(AttrGenAISystem, SystemHanzoMCP),
		StringAttr(AttrGenAIToolName, toolName),
		StringAttr(AttrGenAIToolCallID, in.ToolCallID),
		StringAttr(AttrGenAIToolType, "mcp"),
		StringAttr(AttrMCPServer, in.Server),
		StringAttr(AttrMCPStatus, status),
		Float64Attr(AttrMCPLatencyMs, in.LatencyMs),
	)
	if status == "error" || status == "denied" {
		sp.span.Status = StatusError
	}
	t.appendExtraAttrs(sp, in.ExtraAttributes)
	return cctx, sp, nil
}

// StartVFSSpan starts a VFS / Hanzo S3 storage span (read/write/fsync/snapshot).
func (t *Tracer) StartVFSSpan(ctx context.Context, in VFSSpanInput) (context.Context, *ActiveSpan, error) {
	cctx, sp, err := t.startChild(ctx, SubsystemVFS, "vfs."+in.Op, SpanKindClient, 10+len(in.ExtraAttributes))
	if err != nil {
		return ctx, nil, err
	}
	op := in.Op
	if op == "" {
		op = "read"
	}
	coalesced := in.CoalescedOps
	if coalesced <= 0 {
		coalesced = 1
	}
	path := in.Path
	if t.captureMode != CaptureRaw && path != "" {
		if cleaned, red := ScrubSecrets(path); red {
			path = cleaned
			sp.span.RedactedCount++
		}
	}

	sp.span.Attributes = append(sp.span.Attributes,
		StringAttr(AttrGenAIOperationName, OpVFSOp),
		StringAttr(AttrGenAISystem, SystemHanzoVFS),
		StringAttr(AttrVFSOp, op),
		Int64Attr(AttrVFSBytes, in.Bytes),
		Int64Attr(AttrVFSS3PutCount, in.S3PutCount),
		Int64Attr(AttrVFSS3GetCount, in.S3GetCount),
		Int64Attr(AttrVFSCoalescedOps, coalesced),
	)
	if path != "" {
		sp.span.Attributes = append(sp.span.Attributes, StringAttr(AttrVFSPath, path))
	}
	t.appendExtraAttrs(sp, in.ExtraAttributes)
	return cctx, sp, nil
}

// StartSandboxSpan starts a sandbox startup span (hanzoai/visor).
func (t *Tracer) StartSandboxSpan(ctx context.Context, in SandboxSpanInput) (context.Context, *ActiveSpan, error) {
	cctx, sp, err := t.startChild(ctx, SubsystemSandbox, "visor.sandbox_boot", SpanKindClient, 10+len(in.ExtraAttributes))
	if err != nil {
		return ctx, nil, err
	}
	sp.span.Attributes = append(sp.span.Attributes,
		StringAttr(AttrGenAIOperationName, OpSandboxBoot),
		StringAttr(AttrGenAISystem, SystemHanzoVisor),
		StringAttr(AttrVisorSandboxID, in.SandboxID),
		Float64Attr(AttrVisorBootMs, in.BootMs),
		StringAttr(AttrVisorImageRef, in.ImageRef),
		StringAttr(AttrVisorPolicyDigest, in.PolicyDigest),
	)
	t.appendExtraAttrs(sp, in.ExtraAttributes)
	return cctx, sp, nil
}

// StartBillingSpan starts a Cloud billing & metering span reconciled against the
// task's emitted subsystem spans under the exact same task_id and trace_id.
func (t *Tracer) StartBillingSpan(ctx context.Context, in BillingSpanInput) (context.Context, *ActiveSpan, error) {
	cctx, sp, err := t.startChild(ctx, SubsystemBilling, "cloud.billing_meter", SpanKindInternal, 10+len(in.ExtraAttributes))
	if err != nil {
		return ctx, nil, err
	}
	cur := in.Currency
	if cur == "" {
		cur = "USD"
	}
	sp.span.Attributes = append(sp.span.Attributes,
		StringAttr(AttrGenAIOperationName, OpBillingMeter),
		StringAttr(AttrGenAISystem, SystemHanzoCloud),
		StringAttr(AttrBillingMeterID, in.MeterID),
		Int64Attr(AttrBillingCostMicroUSD, in.CostMicroUSD),
		Int64Attr(AttrBillingTokenUnits, in.TokenUnits),
		Int64Attr(AttrBillingComputeUnits, in.ComputeUnits),
		Int64Attr(AttrBillingStorageOps, in.StorageOps),
		StringAttr(AttrBillingCurrency, cur),
	)
	t.appendExtraAttrs(sp, in.ExtraAttributes)
	return cctx, sp, nil
}

// TaskMeter accumulates live subsystem usage across a task so the final Cloud
// billing span reconciles 1:1 with emitted spans without re-scanning storage.
type TaskMeter struct {
	rc           RateCard
	CostMicroUSD int64
	TokenUnits   int64
	ComputeUnits int64
	StorageOps   int64
}

// NewTaskMeter initializes a TaskMeter bound to rc.
func (t *Tracer) NewTaskMeter() *TaskMeter {
	return &TaskMeter{rc: t.rateCard}
}

// RecordModel adds a ModelSpanInput's metered cost and token units.
func (m *TaskMeter) RecordModel(in ModelSpanInput) {
	cost := in.CostMicroUSD
	if cost <= 0 {
		cost = m.rc.ModelCostMicroUSD(in.InputTokens, in.OutputTokens, in.CachedTokens)
	}
	m.CostMicroUSD += cost
	m.TokenUnits += in.InputTokens + in.OutputTokens
}

// RecordRetrieval adds a RetrievalSpanInput's metered cost and compute unit.
func (m *TaskMeter) RecordRetrieval(_ RetrievalSpanInput) {
	m.CostMicroUSD += m.rc.RetrievalCallMicroUSD
	m.ComputeUnits++
}

// RecordMCP adds an MCPSpanInput's metered cost and compute units.
func (m *TaskMeter) RecordMCP(in MCPSpanInput) {
	units := int64(math.Ceil(in.LatencyMs))
	if units < 1 {
		units = 1
	}
	m.CostMicroUSD += m.rc.MCPCallMicroUSD
	m.ComputeUnits += units
}

// RecordVFS adds a VFSSpanInput's S3 PUT/GET storage ops and cost.
func (m *TaskMeter) RecordVFS(in VFSSpanInput) {
	puts := in.S3PutCount
	if puts < 0 {
		puts = 0
	}
	gets := in.S3GetCount
	if gets < 0 {
		gets = 0
	}
	m.StorageOps += puts + gets
	m.CostMicroUSD += puts*m.rc.S3PutMicroUSD + gets*m.rc.S3GetMicroUSD
}

// RecordSandbox adds a SandboxSpanInput's boot compute units and cost.
func (m *TaskMeter) RecordSandbox(in SandboxSpanInput) {
	ms := int64(math.Ceil(in.BootMs))
	if ms < 1 {
		ms = 1
	}
	m.ComputeUnits += ms
	m.CostMicroUSD += ms * m.rc.SandboxMicroUSDPerMs
}

// BillingInput converts the accumulated TaskMeter totals into a BillingSpanInput.
func (m *TaskMeter) BillingInput(meterID string) BillingSpanInput {
	return BillingSpanInput{
		MeterID:      meterID,
		CostMicroUSD: m.CostMicroUSD,
		TokenUnits:   m.TokenUnits,
		ComputeUnits: m.ComputeUnits,
		StorageOps:   m.StorageOps,
		Currency:     "USD",
	}
}
