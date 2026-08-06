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

import (
	"context"
	"errors"
)

// ErrBlobNotFound is the sentinel a WORKING VFS backend returns from Get/Delete
// when the blob does not exist. It lets a consumer (clients/team files) tell a
// genuine miss (→ 404 / idempotent delete) apart from a backend that is
// unavailable/disabled (any OTHER error → fail closed 502) — so a missing blob
// never masquerades as an outage and a real outage never masquerades as 404.
var ErrBlobNotFound = errors.New("vfs: blob not found")

// Claims is the JWT-validated identity surface gateway hands to
// downstream subsystems per HIP-0026. Sub = JWT `sub`, Org = JWT
// `owner`, Email = JWT `email`, IsAdmin = JWT `isAdmin`, Project = JWT
// `project` (the active org sub-scope), BillingAccount = JWT
// `billing_account` (the funding account for that scope — an ATTRIBUTION
// hint; the debit account is always resolved server-side by commerce from
// the org's ProjectBinding, never trusted from a claim/header).
type Claims struct {
	Sub            string
	Org            string
	Email          string
	IsAdmin        bool
	Project        string
	BillingAccount string
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

// OrgRef is one entry of the JWT `orgs` membership-set claim — an org the
// subject may act in, with its coarse role (owner | admin | member). It is the
// local OSS shape of the identity membership reference (mirrors the IAM
// membership OrgRef) so the in-binary token validator can carry the membership
// set without importing the private IAM module.
type OrgRef struct {
	Org  string `json:"org"`
	Role string `json:"role,omitempty"`
}

// DBHandle is the per-org database handle Base hands out.
type DBHandle interface{ Close() error }

// OrgConfig is the commerce-served org settings struct.
type OrgConfig struct {
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

// ChatRequest mirrors the AI subsystem's chat-completion request. Org and Project
// are the billing SCOPE — who this inference is metered against. The metering
// decorator wrapping deps.AI reads them to authorize the org's balance/budget
// before the call and debit its billing account after, so no inference runs
// unattributed. Empty Org denotes an internal/system call with no customer to bill
// (executed, recorded unattributed) — a customer path always sets it.
type ChatRequest struct {
	Model  string
	Prompt string
	// Org is the EFFECTIVE org — the DATA scope the inner AI client reads (BYO
	// provider keys, RAG/knowledge). BillingOrg is the HOME org that PAYS (the caller's
	// X-User-Owner): for a normal caller they are equal, but a platform SuperAdmin
	// acting in another org has BillingOrg=="admin" while Org is the acted-on org, so
	// the debit lands on the admin ledger, never the org whose data is used. Empty
	// BillingOrg falls back to Org (a caller that has not split them).
	Org        string
	BillingOrg string
	Project    string
}

// ChatResponse mirrors the AI subsystem's chat-completion response. The token
// counts are surfaced so the metering decorator debits the EXACT inference cost
// rather than an estimate; they are zero when the gateway omits usage.
type ChatResponse struct {
	Content          string
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
}

// ErrUpstreamBusy marks a TRANSIENT upstream inference failure that a caller may
// safely retry — an overloaded gateway (HTTP 429/5xx) or an empty-choices /
// "Platform overloaded" response. A completion has NO side effect until it
// succeeds, so retrying it never double-charges. The AI client tags such
// failures with this sentinel (errors.Is-detectable); the agent runner tests for
// it to decide whether to retry the same model and, if still throttled, fail over
// to a reliable one. A NON-transient failure (400, auth, an unserved model) is
// never tagged, so it fails fast rather than burning retries that will repeat it.
var ErrUpstreamBusy = errors.New("upstream busy")

// EmbedRequest is the embeddings call. Inputs are embedded in order; the result is
// one vector per input, aligned by index. Org and Project are the billing SCOPE,
// identical in meaning to ChatRequest's, so embeddings meter and observe through
// the same org/project-aligned path.
type EmbedRequest struct {
	Model  string
	Inputs []string
	// Org is the EFFECTIVE (data-scope) org; BillingOrg is the HOME org that PAYS —
	// see ChatRequest. Empty BillingOrg falls back to Org.
	Org        string
	BillingOrg string
	Project    string
}

// Counter / Timing / Span are the canonical o11y handles.
type (
	Counter interface{ Inc(n int64) }
	Timing  interface{ Observe(seconds float64) }
	Span    interface{ End() }
)

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

// CommerceClient is the inter-subsystem interface to Commerce (entitlements +
// org config). Money is NOT here — a subject's prepaid balance/deposit/usage is
// the orthogonal BillingClient, so commerce (catalog/subscriptions/licensing) and
// billing (the money ledger) never braid. Every method is a DIRECT in-process call
// to the embedded commerce datastore (co-resident) or a ZAP RPC (split-deploy).
type CommerceClient interface {
	GetOrgConfig(ctx context.Context, orgID string) (*OrgConfig, error)
	// CheckEntitlement reports whether org `orgID` holds an active
	// entitlement for licensed product `productID`, and returns the plan's
	// flat license-features per the toLicenseFeatures vocab contract. Used
	// by the licensing subsystem to gate + scope token issuance. orgID is
	// the org the buyer acts as (X-Org-Id); when callers only have a
	// user subject they pass it through here and commerce resolves the
	// owning org.
	CheckEntitlement(ctx context.Context, orgID, productID string) (*LicenseEntitlement, error)
}

// DurableEngine is the OSS seam for a durable workflow/queue engine — the
// extension point the private build fills with the real hanzoai/tasks engine.
// The OSS core ships a no-op in-proc default (cloud.NoopDurable): Submit runs
// nothing durably, Signal/Query are no-ops. A subsystem that wants durable
// execution calls the engine the operator registered (cloud.RegisterDurableEngine)
// and degrades gracefully when it is the no-op — mirroring the ingest-inline
// fail-soft the private engine path already guarantees.
type DurableEngine interface {
	// Submit enqueues a durable unit of work in the given namespace (1:1 with an
	// org) and returns its handle id. The no-op default returns ("", nil).
	Submit(ctx context.Context, namespace, kind string, payload []byte) (id string, err error)
	// Signal delivers a named signal (with payload) to a running durable unit.
	Signal(ctx context.Context, namespace, id, name string, payload []byte) error
	// Query reads the current state of a durable unit.
	Query(ctx context.Context, namespace, id string) ([]byte, error)
}

// AIClient is the inter-subsystem interface to AI.
type AIClient interface {
	ChatCompletion(ctx context.Context, req *ChatRequest) (*ChatResponse, error)
	// Embed returns one vector per input text, aligned by index, from the SAME
	// gateway + credential as ChatCompletion. Embeddings therefore authenticate,
	// meter, and observe through the ONE org/project-aligned path — never a static
	// side-channel key. The EmbedRequest carries the billing scope (Org/Project)
	// exactly like ChatRequest; an empty Inputs slice returns (nil, nil).
	Embed(ctx context.Context, req *EmbedRequest) ([][]float32, error)
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

// VFSClient is the inter-subsystem interface to vfs. Delete removes the blob at
// key (idempotent — a missing key is not an error at the seam; the underlying
// hanzoai/vfs forwards to backend.Delete(ctx,key)).
type VFSClient interface {
	Put(ctx context.Context, key string, payload []byte) error
	Get(ctx context.Context, key string) ([]byte, error)
	Delete(ctx context.Context, key string) error
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

// ── Kubernetes seam ──────────────────────────────────────────────────────────
//
// K8sClient is the OSS seam for the cluster control plane, and it exists so this
// binary links no Kubernetes client at all. Two subsystems talk to a cluster —
// platform (reconciles App CRs) and validators (writes node CRs) — and both used to
// import k8s.io/client-go directly. That made "runs anywhere, no Kubernetes" a
// claim about configuration rather than a property of the build: the dependency
// was linked whether or not a cluster existed.
//
// It follows the same inversion as every other product plane here (IAMClient,
// CommerceClient, DurableEngine): the interface lives in the core, the private
// build registers a dynamic-client implementation, and the OSS default is
// unavailable-but-honest rather than absent.
//
// SHAPES. Objects cross as decoded-JSON maps — exactly what
// unstructured.Unstructured wraps, so neither side marshals. A GroupVersionResource
// crosses as three strings. The only patch shape either caller uses is an RFC-7386
// JSON merge patch, so it is named rather than parameterised.
//
// NUMBERS. A map destined for the cluster must use int64 for integers, not int:
// the dynamic client deep-copies through a JSON-shaped conversion that panics on
// plain int. Callers keep their int64(...) casts; an implementation may normalise.
type K8sClient interface {
	// Get returns one object. Missing objects yield ErrK8sNotFound.
	Get(ctx context.Context, group, version, resource, namespace, name string) (map[string]any, error)
	// List returns the objects in a namespace. limit <= 0 means unlimited. A
	// missing CRD or namespace yields ErrK8sNotFound — callers rely on being able
	// to tell "no cluster state yet" from "the request failed".
	List(ctx context.Context, group, version, resource, namespace string, limit int64) ([]map[string]any, error)
	// Create creates one object. A losing race yields ErrK8sAlreadyExists, which
	// callers treat as success.
	Create(ctx context.Context, group, version, resource, namespace string, obj map[string]any) error
	// MergePatch applies an RFC-7386 JSON merge patch.
	MergePatch(ctx context.Context, group, version, resource, namespace, name string, patch []byte) error
	// Ready reports cluster reachability and, when false, WHY. The reason is
	// surfaced verbatim to operators (the platform health route reports it), so
	// "unavailable" is never silent — the whole point of failing closed here is
	// that somebody can find out what is missing.
	Ready() (ok bool, reason string)
}

// The cluster-error sentinels. They stand in for apierrors.IsNotFound and
// friends so no caller needs the k8s error package. An implementation MUST wrap
// the underlying error (%w) rather than replacing it: callers print the original
// text into health payloads and 502 bodies, and losing it would turn a precise
// RBAC message into a shrug.
var (
	ErrK8sNotFound      = errors.New("k8s: resource not found")
	ErrK8sAlreadyExists = errors.New("k8s: resource already exists")
	ErrK8sForbidden     = errors.New("k8s: forbidden (RBAC)")
)
