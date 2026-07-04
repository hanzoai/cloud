// Package types holds the placeholder transport types AND the
// inter-subsystem client interfaces shared between cloud (the
// orchestrator) and cloud/clients (the in-process and RPC client
// implementations). Both packages reference this leaf package to
// avoid an import cycle.
//
// As subsystems ship their .zap schemas and zapc generates typed
// bindings, the placeholders here are replaced by aliases to the
// generated structs in <subsystem>/zap/gen/*.go. Until then the
// stable shape lives here so subsystem code can pin signatures
// without re-importing through cloud.
package types

import "context"

// Claims is the JWT-validated identity surface gateway hands to
// downstream subsystems per HIP-0026. Sub = JWT `sub`, Org = JWT
// `owner`, Email = JWT `email`, IsAdmin = JWT `isAdmin`.
type Claims struct {
	Sub     string
	Org     string
	Email   string
	IsAdmin bool
}

// User is the IAM-served user object.
type User struct {
	ID    string
	Email string
	Name  string
}

// Org is the IAM-served org object.
type Org struct {
	ID   string
	Slug string
	Name string
}

// DBHandle is the per-tenant database handle Base hands out.
type DBHandle interface{ Close() error }

// TenantConfig is the commerce-served tenant settings struct.
type TenantConfig struct {
	OrgID string
	Brand string
}

// LicenseEntitlement is commerce's answer to "does this org/user hold an
// active entitlement for licensed product X, and what does its plan grant?".
//
// It is the inter-subsystem transport for the entitlement-flow that gates
// licensing token issuance (commerce → licensing → engine). The licensing
// subsystem copies Features verbatim into the signed token's `features`
// list so the proprietary engine's offline release gate (hasFeatures)
// enforces exactly the plan the buyer paid for.
//
// Features is the FLAT capability list produced from the canonical
// entitlement vocabulary by the data plane's toLicenseFeatures contract
// (@hanzo/plans entitlements.mjs): licensing.engine_features verbatim,
// plus derived capability tokens (e.g. "ai.premium", "training",
// "tools.<name>"), plus scoping tokens ("licensing.app:<id>",
// "licensing.product:<id>"). Numeric quotas (tokens_per_min, seats,
// max_vms, …) ride out of band and are NOT encoded here.
type LicenseEntitlement struct {
	// ProductID is the licensed product the entitlement was checked for
	// (e.g. "engine", "engine-rocm", a plugin id).
	ProductID string
	// Active reports whether the entitlement is currently valid (paid,
	// not lapsed/cancelled). Licensing refuses to mint when false.
	Active bool
	// Plan is the resolved plan/tier id (e.g. "developer", "pro", "max",
	// "enterprise"). Surfaced for logging/audit; not load-bearing for the
	// release gate.
	Plan string
	// Features is the flat license-feature list per the toLicenseFeatures
	// vocab contract — copied verbatim into License.Features at issue.
	Features []string
	// ExpiresUnix bounds the entitlement (unix seconds, 0 = no bound). The
	// issued token's exp is clamped to it so a token never outlives the
	// entitlement.
	ExpiresUnix int64
}

// ChatRequest mirrors the AI subsystem's chat-completion request.
type ChatRequest struct {
	Model  string
	Prompt string
}

// ChatResponse mirrors the AI subsystem's chat-completion response.
type ChatResponse struct{ Content string }

// Counter / Timing / Span are the canonical o11y handles.
type Counter interface{ Inc(n int64) }
type Timing interface{ Observe(seconds float64) }
type Span interface{ End() }

// IntentRequest creates a payments intent. Commerce never sees PAN;
// it only ever passes the vault token + amount + currency.
type IntentRequest struct {
	Token       string
	Currency    string
	AmountCents int64
}

// IntentResponse acknowledges intent creation / state.
type IntentResponse struct {
	ID     string
	Status string
}

// IntentStatus is the status-poll response.
type IntentStatus struct{ Status string }

// VaultChargeRequest is the payments→vault charge request. Vault is
// the only system that sees PAN — it dereferences the token and
// makes the processor call.
type VaultChargeRequest struct {
	Token       string
	ProcessorID string
	Currency    string
	AmountCents int64
}

// VaultChargeResponse is the vault→payments charge response.
type VaultChargeResponse struct {
	ProcessorRef string
	Status       string
}

// IAMClient is the inter-subsystem interface to IAM. Co-resident:
// direct Go call. Split: ZAP-RPC.
type IAMClient interface {
	VerifyJWT(ctx context.Context, bearer string) (Claims, error)
	GetUser(ctx context.Context, userID string) (*User, error)
	GetOrg(ctx context.Context, orgID string) (*Org, error)
}

// KMSClient is the inter-subsystem interface to KMS.
type KMSClient interface {
	GetSecret(ctx context.Context, ref string) ([]byte, error)
	PutSecret(ctx context.Context, ref string, value []byte) error
	Sign(ctx context.Context, keyRef string, payload []byte) ([]byte, error)
}

// BaseClient is the inter-subsystem interface to Base.
type BaseClient interface {
	Open(ctx context.Context, orgID, serviceName string) (DBHandle, error)
}

// CommerceClient is the inter-subsystem interface to Commerce.
type CommerceClient interface {
	GetTenantConfig(ctx context.Context, orgID string) (*TenantConfig, error)
	// CheckEntitlement reports whether org `orgID` holds an active
	// entitlement for licensed product `productID`, and returns the plan's
	// flat license-features per the toLicenseFeatures vocab contract. Used
	// by the licensing subsystem to gate + scope token issuance. orgID is
	// the tenant the buyer acts as (X-Org-Id); when callers only have a
	// user subject they pass it through here and commerce resolves the
	// owning org.
	CheckEntitlement(ctx context.Context, orgID, productID string) (*LicenseEntitlement, error)
}

// AIClient is the inter-subsystem interface to AI.
type AIClient interface {
	ChatCompletion(ctx context.Context, req *ChatRequest) (*ChatResponse, error)
}

// ModelLister is an OPTIONAL capability an AIClient may ALSO implement: it
// enumerates the model ids the gateway currently serves (its OpenAI-compatible
// /v1/models catalog). The agents subsystem uses it to reject a non-catalog
// model at agent create/update time with a clean 400, instead of letting the run
// surface a confusing gateway 502 for a model this gateway never served. An
// AIClient that cannot enumerate models (the disabled stub, the ZAP-RPC client)
// simply does not implement it, and callers fall back to skipping the check —
// so model validation is a best-effort UX guard, never a hard dependency.
type ModelLister interface {
	Models(ctx context.Context) ([]string, error)
}

// O11yClient is the inter-subsystem interface to o11y.
type O11yClient interface {
	Counter(name string, tags ...string) Counter
	Timing(name string, tags ...string) Timing
	Span(ctx context.Context, name string) (context.Context, Span)
}

// VFSClient is the inter-subsystem interface to vfs.
type VFSClient interface {
	Put(ctx context.Context, key string, payload []byte) error
	Get(ctx context.Context, key string) ([]byte, error)
}

// MQClient is the inter-subsystem interface to mq.
type MQClient interface {
	Publish(ctx context.Context, subject string, payload []byte) error
	Subscribe(ctx context.Context, subject string, handler func([]byte) error) error
}

// PaymentsClient is the inter-subsystem interface to payments. Always
// ZAP-RPC; never co-resident (PCI scope isolation).
type PaymentsClient interface {
	CreateIntent(ctx context.Context, req *IntentRequest) (*IntentResponse, error)
	ConfirmIntent(ctx context.Context, intentID string) (*IntentResponse, error)
	GetIntentStatus(ctx context.Context, intentID string) (*IntentStatus, error)
}

// VaultClient is the inter-subsystem interface to vault. The ONLY
// system that touches PAN. Always ZAP-RPC; never co-resident.
type VaultClient interface {
	Charge(ctx context.Context, req *VaultChargeRequest) (*VaultChargeResponse, error)
}
