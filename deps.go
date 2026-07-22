// Package cloud is the unified Hanzo Cloud binary per HIP-0106.
//
// One Go binary mounts every Hanzo-native subsystem (iam, base, kms,
// commerce, ai, gateway, o11y, vfs, mq, dns, amqp, mcp, ...) via the
// canonical Mount(app *zip.App, deps cloud.Deps) error contract. Brand,
// enabled subsystems, and org scope are deployment configuration; the
// binary is the same artifact across every white-label deployment.
//
// Per HIP-0106 — github.com/hanzoai/HIPs/blob/main/HIPs/hip-0106-unified-hanzo-cloud-binary.md.
package cloud

import (
	"github.com/hanzoai/cloud/clients/metering"
	luxlog "github.com/luxfi/log"

	"github.com/hanzoai/cloud/audit"
	"github.com/hanzoai/cloud/clients/gateway/edge"
	"github.com/hanzoai/cloud/types"
)

// Deps is the shared dependency surface passed to every subsystem's
// Mount(app, deps) function. Subsystems consume only what they need.
//
// In-process: each Client below resolves to a direct Go method-call
// implementation. Out-of-process (legacy split deploys): the same Client
// resolves to a ZAP-RPC implementation. Subsystem code does not branch
// on which mode; the interface is the contract.
type Deps struct {
	// Logger is the canonical Hanzo logger (luxfi/log). Subsystems derive
	// scoped child loggers from this.
	Logger luxlog.Logger

	// Brand is the white-label brand identifier for this deployment.
	// Values: "hanzo", "lux", "zoo", "osage", "pars", or any customer brand.
	Brand string

	// Version is the API contract/build version emitted as the X-Api-Version
	// response header (see middleware.ProductionHeaders wiring in serve.go).
	Version string

	// Env is the deployment environment (mainnet|testnet|devnet). Subsystems
	// that meter usage stamp it for per-env attribution; it never gates or
	// bypasses billing (every env bills against its own commerce ledger).
	Env string

	// Domain is the deployment's primary domain (e.g. "api.hanzo.ai",
	// "api.osage.cloud"). Subsystems use this to scope URLs in responses.
	Domain string

	// IAMIssuer is the canonical OIDC issuer (JWKS source) for this brand,
	// resolved from Brand via the white-label registry unless pinned by the
	// operator. Subsystems validate JWT `iss` + signatures against
	// {IAMIssuer}/v1/iam/.well-known/jwks (HIP-0111). One issuer per deployment.
	IAMIssuer string

	// DataDir is the per-deployment data root. Per-org SQLite files
	// land at {DataDir}/orgs/{orgSlug}/{service}.db per HIP-0302.
	DataDir string

	// AIDefaultModel is the served model a subsystem uses when a caller supplies
	// none (CLOUD_AI_DEFAULT_MODEL, default deepseek-v4-flash). It is the ONE
	// cloud-side model default, sourced from config so no subsystem hardcodes a
	// model id. The agents subsystem stores it on an agent created without an
	// explicit model, so a bot launched without a model still runs on a valid
	// catalog model. Model routing itself stays the gateway's job.
	AIDefaultModel string

	// AIFallbackModel is the reliable model the agent runner fails over to when an
	// agent's own model stays throttled after retries (CLOUD_AI_FALLBACK_MODEL,
	// default "best"). Only the autonomous agent/bot run path uses it; interactive
	// chat is untouched. Empty disables failover.
	AIFallbackModel string

	// Subsystem clients — populated by BuildDeps based on enabled subsystems.
	// Each is an interface with both in-process and ZAP-RPC implementations.
	IAM      IAMClient
	KMS      KMSClient
	Base     BaseClient
	Commerce CommerceClient
	// AI runs CHAT COMPLETIONS (a WRITE endpoint): agents, guide, crm, content,
	// sitegen, code /ask. It authenticates with the binary's IAM M2M identity — a
	// completions-capable credential — NEVER the read-only publishable (pk-) embed
	// key, which the gateway 403s on any write endpoint.
	AI AIClient
	// Embed runs EMBEDDINGS (a READ-ONLY endpoint): code-index + KB knowledge. This
	// is the ONLY consumer of the read-only publishable (pk-) key (CLOUD_AI_API_KEY),
	// the correct least-privilege credential for a read-only call. Split from AI so a
	// pk- embed key can never leak onto the completions path (the intermittent-403
	// bug). Falls back to the AI (M2M) resolution when no static embed key is set.
	Embed AIClient
	O11y  O11yClient
	VFS   VFSClient
	MQ    MQClient

	// Payments + Vault stay out-of-process (PCI scope isolation per
	// HIP-0106). These clients always resolve to ZAP-RPC implementations,
	// never in-process.
	Payments PaymentsClient
	Vault    VaultClient

	// Metering is the canonical commerce billing client used by the
	// request-edge BillingGate. It speaks net/http to commerce's billing API
	// (separate from the ZAP Commerce client above, which is for typed
	// inter-subsystem calls). Nil or not-Enabled() makes the gate a no-op.
	Metering *metering.Client

	// Audit is the tamper-evident, append-only audit trail Recorder (FedRAMP AU-*
	// / SOC 2 CC-*). Serve constructs it once, wires the AuditTrail middleware to
	// it, and hands it here so the /v1/admin/audit query + /v1/admin/audit/verify
	// endpoints read the SAME store the middleware writes. Nil makes the audit
	// middleware a no-op and the query endpoint fall back to the IAM proxy (an
	// unconfigured deployment is never blocked). See audit/ and audit_middleware.go.
	Audit *audit.Recorder

	// GatewayPolicy is the runtime-mutable edge-policy store (the /v1/gateway
	// config plane): CORS allowlist + pre-auth per-IP flood cap (platform scope)
	// and the authenticated per-org rate ceiling. BuildDeps constructs it once,
	// layered over the static env/flag defaults; the EdgeCORS/EdgeRateLimit
	// middleware read its PLATFORM policy live and ScopeRateLimit reads its
	// per-org OrgRPM, and the clients/gateway subsystem serves GET/PUT over the
	// SAME store. Never nil — New always returns a working (static-only on store
	// error) *Store, so the edge is never blocked. See clients/edge.
	GatewayPolicy *edge.Store
}

// Per-subsystem client interfaces live in cloud/types so the
// cloud/clients package can implement them without an import cycle.
// We re-export them as aliases at the cloud root so subsystem code
// keeps writing cloud.IAMClient, cloud.KMSClient, etc.

type IAMClient = types.IAMClient
type KMSClient = types.KMSClient
type BaseClient = types.BaseClient
type CommerceClient = types.CommerceClient
type AIClient = types.AIClient
type O11yClient = types.O11yClient
type VFSClient = types.VFSClient
type MQClient = types.MQClient
type PaymentsClient = types.PaymentsClient
type VaultClient = types.VaultClient

// --- placeholder types (replaced by ZAP-generated types per subsystem) ---
//
// These re-export the canonical transport shapes from cloud/types so
// subsystems and the clients package can both use them without
// pulling cloud as a dependency. As zapc generates typed bindings per
// subsystem, each alias here becomes an alias to the generated type
// in <subsystem>/zap/gen/*.go.

type Claims = types.Claims
type User = types.User
type Org = types.Org
type DBHandle = types.DBHandle

// OrgConfig and LicenseEntitlement are the two values CommerceClient's methods
// name. Both are aliased here for the same reason the interface is: a subsystem
// that implements CommerceClient (or fakes it in a test) must be able to spell
// its signature using only this package. LicenseEntitlement was aliased and
// OrgConfig was not, which made the exported interface unimplementable from
// outside without reaching into cloud/types — an omission, not a boundary.
type OrgConfig = types.OrgConfig
type LicenseEntitlement = types.LicenseEntitlement
type ChatRequest = types.ChatRequest
type ChatResponse = types.ChatResponse
type EmbedRequest = types.EmbedRequest
type Counter = types.Counter
type Timing = types.Timing
type Span = types.Span
type IntentRequest = types.IntentRequest
type IntentResponse = types.IntentResponse
type IntentStatus = types.IntentStatus
type VaultChargeRequest = types.VaultChargeRequest
type VaultChargeResponse = types.VaultChargeResponse
