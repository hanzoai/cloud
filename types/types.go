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
