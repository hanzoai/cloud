// Copyright 2023-2026 Hanzo AI Inc.
// SPDX-License-Identifier: Apache-2.0

package agenttrace

// Instrumentation scope metadata emitted in OTLP/JSON ResourceSpans.
const (
	ScopeName      = "hanzo.agenttrace"
	ScopeVersion   = "1.0.0"
	ServiceName    = "hanzo-cloud"
	ServiceVersion = "hip-1348"
)

// Subsystem enumerates the canonical Hanzo execution subsystems correlated under
// a single task_id and W3C trace_id.
type Subsystem string

const (
	SubsystemRoot      Subsystem = "root"
	SubsystemModel     Subsystem = "model"
	SubsystemRetrieval Subsystem = "retrieval"
	SubsystemMCP       Subsystem = "mcp"
	SubsystemVFS       Subsystem = "vfs"
	SubsystemSandbox   Subsystem = "sandbox"
	SubsystemBilling   Subsystem = "billing"
)

// AllSubsystems returns the six required execution subsystems expected in a
// complete end-to-end agent execution trace.
var AllSubsystems = []Subsystem{
	SubsystemModel,
	SubsystemRetrieval,
	SubsystemMCP,
	SubsystemVFS,
	SubsystemSandbox,
	SubsystemBilling,
}

// Valid reports whether s is a recognized Hanzo subsystem identifier.
func (s Subsystem) Valid() bool {
	switch s {
	case SubsystemRoot,
		SubsystemModel,
		SubsystemRetrieval,
		SubsystemMCP,
		SubsystemVFS,
		SubsystemSandbox,
		SubsystemBilling:
		return true
	default:
		return false
	}
}

// OpenTelemetry GenAI Semantic Conventions attribute keys.
// Reference: https://github.com/open-telemetry/semantic-conventions-genai/blob/main/docs/gen-ai/README.md
const (
	AttrGenAIOperationName          = "gen_ai.operation.name"
	AttrGenAISystem                 = "gen_ai.system"
	AttrGenAIRequestModel           = "gen_ai.request.model"
	AttrGenAIResponseModel          = "gen_ai.response.model"
	AttrGenAIResponseFinishReasons  = "gen_ai.response.finish_reasons"
	AttrGenAIUsageInputTokens       = "gen_ai.usage.input_tokens"
	AttrGenAIUsageOutputTokens      = "gen_ai.usage.output_tokens"
	AttrGenAIUsageCacheReadTokens   = "gen_ai.usage.cache_read.input_tokens"
	AttrGenAIUsageCacheCreateTokens = "gen_ai.usage.cache_creation.input_tokens"
	AttrGenAIToolName               = "gen_ai.tool.name"
	AttrGenAIToolCallID             = "gen_ai.tool.call.id"
	AttrGenAIToolType               = "gen_ai.tool.type"
	AttrGenAIAgentID                = "gen_ai.agent.id"
	AttrGenAIAgentName              = "gen_ai.agent.name"
	AttrGenAIPrompt                 = "gen_ai.prompt"
	AttrGenAICompletion             = "gen_ai.completion"
)

// OpenTelemetry GenAI operation names and Hanzo system identifiers.
const (
	OpInvokeAgent  = "invoke_agent"
	OpChat         = "chat"
	OpRetrieval    = "retrieval"
	OpExecuteTool  = "execute_tool"
	OpVFSOp        = "vfs_op"
	OpSandboxBoot  = "sandbox_boot"
	OpBillingMeter = "billing_meter"

	SystemHanzoKai   = "hanzo.kai"
	SystemHanzoZen   = "hanzo.zen"
	SystemHanzoEnso  = "hanzo.enso"
	SystemHanzoIndex = "hanzo.index"
	SystemHanzoGraph = "hanzo.graph"
	SystemHanzoMCP   = "hanzo.mcp"
	SystemHanzoVFS   = "hanzo.vfs"
	SystemHanzoVisor = "hanzo.visor"
	SystemHanzoCloud = "hanzo.cloud"
)

// Hanzo unified agent execution correlation and subsystem attributes (HIP-1348).
const (
	AttrHanzoTaskID    = "hanzo.task.id"
	AttrHanzoOrgID     = "hanzo.org.id"
	AttrHanzoTenantID  = "hanzo.tenant.id"
	AttrHanzoSubsystem = "hanzo.subsystem"

	// Model calls (Enso routing + Zen generation)
	AttrModelEnsoRoute    = "hanzo.enso.route"
	AttrModelTTFTMs       = "hanzo.model.ttft_ms"
	AttrModelCachedTokens = "hanzo.model.cached_tokens"
	AttrModelCostMicroUSD = "hanzo.model.cost_micro_usd"

	// Retrieval (hanzoai/index & hanzoai/graph)
	AttrRetrievalBackend     = "hanzo.retrieval.backend"
	AttrRetrievalQueryDigest = "hanzo.retrieval.query_digest"
	AttrRetrievalTopK        = "hanzo.retrieval.top_k"
	AttrRetrievalHitCount    = "hanzo.retrieval.hit_count"
	AttrRetrievalACLScope    = "hanzo.retrieval.acl_scope"

	// MCP tools (hanzoai/mcp)
	AttrMCPServer    = "hanzo.mcp.server"
	AttrMCPStatus    = "hanzo.mcp.status"
	AttrMCPLatencyMs = "hanzo.mcp.latency_ms"

	// VFS operations (hanzoai/vfs -> hanzoai/s3)
	AttrVFSOp           = "hanzo.vfs.op"
	AttrVFSBytes        = "hanzo.vfs.bytes"
	AttrVFSS3PutCount   = "hanzo.vfs.s3_put_count"
	AttrVFSS3GetCount   = "hanzo.vfs.s3_get_count"
	AttrVFSCoalescedOps = "hanzo.vfs.coalesced_ops"
	AttrVFSPath         = "hanzo.vfs.path"

	// Sandbox startup (hanzoai/visor)
	AttrVisorSandboxID    = "hanzo.visor.sandbox_id"
	AttrVisorBootMs       = "hanzo.visor.boot_ms"
	AttrVisorImageRef     = "hanzo.visor.image_ref"
	AttrVisorPolicyDigest = "hanzo.visor.policy_digest"

	// Billing & Cloud metering (hanzoai/cloud)
	AttrBillingMeterID      = "hanzo.billing.meter_id"
	AttrBillingCostMicroUSD = "hanzo.billing.cost_micro_usd"
	AttrBillingTokenUnits   = "hanzo.billing.token_units"
	AttrBillingComputeUnits = "hanzo.billing.compute_units"
	AttrBillingStorageOps   = "hanzo.billing.storage_ops"
	AttrBillingCurrency     = "hanzo.billing.currency"

	// Content capture / DLP metadata
	AttrPromptBytes     = "hanzo.dlp.prompt_bytes"
	AttrCompletionBytes = "hanzo.dlp.completion_bytes"
	AttrCaptureMode     = "hanzo.dlp.capture_mode"
	AttrRedactedCount   = "hanzo.dlp.redacted_count"
)

// ModelSpanInput describes a model inference hop (Enso routing + Zen generation).
type ModelSpanInput struct {
	System          string
	EnsoRoute       string
	RequestModel    string
	ResponseModel   string
	FinishReason    string
	InputTokens     int64
	OutputTokens    int64
	CachedTokens    int64
	TTFTMs          float64
	Prompt          string // Gated through DLP ContentCaptureMode
	Completion      string // Gated through DLP ContentCaptureMode
	CostMicroUSD    int64  // Optional; if 0 computed from RateCard
	ExtraAttributes []Attr
}

// RetrievalSpanInput describes a retrieval hop (hanzoai/index or hanzoai/graph).
type RetrievalSpanInput struct {
	Backend         string // "index" or "graph"
	Query           string // Hashed to SHA-256 query_digest; never emitted raw
	QueryDigest     string // Optional precomputed digest
	TopK            int64
	HitCount        int64
	ACLScope        string
	ExtraAttributes []Attr
}

// MCPSpanInput describes an MCP tool invocation hop (hanzoai/mcp).
type MCPSpanInput struct {
	Server          string
	ToolName        string
	ToolCallID      string
	Status          string // "ok", "error", "denied"
	LatencyMs       float64
	ExtraAttributes []Attr
}

// VFSSpanInput describes a VFS / Hanzo S3 storage hop (hanzoai/vfs -> hanzoai/s3).
type VFSSpanInput struct {
	Op              string // "read", "write", "fsync", "snapshot", "batch"
	Path            string
	Bytes           int64
	S3PutCount      int64
	S3GetCount      int64
	CoalescedOps    int64 // >= 1; > 1 when multiple micro-reads/writes are coalesced
	ExtraAttributes []Attr
}

// SandboxSpanInput describes a sandbox boot hop (hanzoai/visor).
type SandboxSpanInput struct {
	SandboxID       string
	BootMs          float64
	ImageRef        string
	PolicyDigest    string
	ExtraAttributes []Attr
}

// BillingSpanInput describes a Cloud billing & metering span (hanzoai/cloud).
type BillingSpanInput struct {
	MeterID         string
	CostMicroUSD    int64
	TokenUnits      int64
	ComputeUnits    int64
	StorageOps      int64
	Currency        string // Defaults to "USD"
	ExtraAttributes []Attr
}
