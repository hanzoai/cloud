// Package cloud is the unified Hanzo Cloud binary per HIP-0106.
//
// One Go binary mounts every Hanzo-native subsystem (iam, base, kms,
// commerce, ai, gateway, o11y, vfs, mq, dns, amqp, mcp, ...) via the
// canonical Mount(app *zip.App, deps cloud.Deps) error contract. Brand,
// enabled subsystems, and tenant scope are deployment configuration; the
// binary is the same artifact across every white-label deployment.
//
// Per HIP-0106 — github.com/hanzoai/HIPs/blob/main/HIPs/hip-0106-unified-hanzo-cloud-binary.md.
package cloud

import (
	"context"

	luxlog "github.com/luxfi/log"
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

	// Domain is the deployment's primary domain (e.g. "api.hanzo.ai",
	// "api.osage.cloud"). Subsystems use this to scope URLs in responses.
	Domain string

	// DataDir is the per-deployment data root. Per-tenant SQLite files
	// land at {DataDir}/orgs/{orgSlug}/{service}.db per HIP-0302.
	DataDir string

	// Subsystem clients — populated by BuildDeps based on enabled subsystems.
	// Each is an interface with both in-process and ZAP-RPC implementations.
	IAM      IAMClient
	KMS      KMSClient
	Base     BaseClient
	Commerce CommerceClient
	AI       AIClient
	O11y     O11yClient
	VFS      VFSClient
	MQ       MQClient

	// Payments + Vault stay out-of-process (PCI scope isolation per
	// HIP-0106). These clients always resolve to ZAP-RPC implementations,
	// never in-process.
	Payments PaymentsClient
	Vault    VaultClient
}

// IAMClient is the inter-subsystem interface to IAM (auth, OIDC, JWKS,
// user/org management). When iam is co-resident, this is a direct
// Go call; when split, it's ZAP-RPC.
type IAMClient interface {
	VerifyJWT(ctx context.Context, bearer string) (Claims, error)
	GetUser(ctx context.Context, userID string) (*User, error)
	GetOrg(ctx context.Context, orgID string) (*Org, error)
}

// KMSClient is the inter-subsystem interface to KMS (secrets, keys).
type KMSClient interface {
	GetSecret(ctx context.Context, ref string) ([]byte, error)
	PutSecret(ctx context.Context, ref string, value []byte) error
	Sign(ctx context.Context, keyRef string, payload []byte) ([]byte, error)
}

// BaseClient is the inter-subsystem interface to Base (per-tenant
// SQLite / ZapDB, extension runtimes, hooks).
type BaseClient interface {
	// Open returns the per-tenant database handle for the given org.
	// Each org gets its own SQLite file with a per-org HKDF-derived DEK
	// from a master key in KMS, per HIP-0302.
	Open(ctx context.Context, orgID, serviceName string) (DBHandle, error)
}

// CommerceClient is the inter-subsystem interface to Commerce (checkout,
// billing, pricing). Commerce is a light router — it never sees PAN,
// only tokens + intent IDs.
type CommerceClient interface {
	GetTenantConfig(ctx context.Context, orgID string) (*TenantConfig, error)
}

// AIClient is the inter-subsystem interface to AI (LLM control plane,
// RAG, model hub). This is the renamed former hanzoai/cloud subsystem.
type AIClient interface {
	ChatCompletion(ctx context.Context, req *ChatRequest) (*ChatResponse, error)
}

// O11yClient is the inter-subsystem interface to o11y (metrics, traces,
// logs). All subsystems emit telemetry through this.
type O11yClient interface {
	Counter(name string, tags ...string) Counter
	Timing(name string, tags ...string) Timing
	Span(ctx context.Context, name string) (context.Context, Span)
}

// VFSClient is the inter-subsystem interface to vfs (the canonical
// object-store abstraction per HIP-0107).
type VFSClient interface {
	Put(ctx context.Context, key string, payload []byte) error
	Get(ctx context.Context, key string) ([]byte, error)
}

// MQClient is the inter-subsystem interface to mq (durable message queue).
type MQClient interface {
	Publish(ctx context.Context, subject string, payload []byte) error
	Subscribe(ctx context.Context, subject string, handler func([]byte) error) error
}

// PaymentsClient is the inter-subsystem interface to payments (HyperSwitch
// fork, in tokens-only mode). NEVER receives PAN — per HIP-0106 solo-vault
// CDE architecture. Always ZAP-RPC (out-of-process for PCI scope isolation).
type PaymentsClient interface {
	CreateIntent(ctx context.Context, req *IntentRequest) (*IntentResponse, error)
	ConfirmIntent(ctx context.Context, intentID string) (*IntentResponse, error)
	GetIntentStatus(ctx context.Context, intentID string) (*IntentStatus, error)
}

// VaultClient is the inter-subsystem interface to vault (PCI-CDE — the
// ONLY system that touches PAN). Always ZAP-RPC. Commerce never calls
// vault directly; payments calls vault for the actual processor charge.
type VaultClient interface {
	Charge(ctx context.Context, req *VaultChargeRequest) (*VaultChargeResponse, error)
}

// --- placeholder types (replaced by ZAP-generated types per subsystem) ---

type Claims struct {
	Sub     string
	Org     string
	Email   string
	IsAdmin bool
}

type User struct{ ID, Email, Name string }
type Org struct{ ID, Slug, Name string }
type DBHandle interface{ Close() error }
type TenantConfig struct{ OrgID, Brand string }
type ChatRequest struct{ Model, Prompt string }
type ChatResponse struct{ Content string }
type Counter interface{ Inc(n int64) }
type Timing interface{ Observe(seconds float64) }
type Span interface{ End() }
type IntentRequest struct{ Token, Currency string; AmountCents int64 }
type IntentResponse struct{ ID, Status string }
type IntentStatus struct{ Status string }
type VaultChargeRequest struct{ Token, ProcessorID, Currency string; AmountCents int64 }
type VaultChargeResponse struct{ ProcessorRef, Status string }
