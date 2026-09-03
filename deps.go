// Package cloud is the unified Hanzo Cloud binary per HIP-0106.
//
// One Go binary mounts every Hanzo-native subsystem (iam, base, kms,
// commerce, ai, gateway, o11y, vfs, mq, dns, amqp, mcp, ...) via the
// canonical Use(app cloud.Router, deps cloud.Deps) error contract. Brand,
// enabled subsystems, and org scope are deployment configuration; the
// binary is the same artifact across every white-label deployment.
//
// Per HIP-0106 — github.com/hanzoai/HIPs/blob/main/HIPs/hip-0106-unified-hanzo-cloud-binary.md.
package cloud

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/hanzoai/cloud/apps/metering"
	"github.com/hanzoai/ha"
	luxlog "github.com/luxfi/log"

	"github.com/hanzoai/cloud/apps/gateway/edge"
	"github.com/hanzoai/cloud/audit"
	"github.com/hanzoai/cloud/internal/org"
	"github.com/hanzoai/cloud/types"
)

// Deps is the shared dependency surface passed to every subsystem's
// Use(app, deps) function. Subsystems consume only what they need.
//
// In-process: each Client below resolves to a direct Go method-call
// implementation. Out-of-process (legacy split deploys): the same Client
// resolves to a ZAP-RPC implementation. Subsystem code does not branch
// on which mode; the interface is the contract.
// Secret resolves a KMS-sealed secret by reference, for a subsystem that must not
// import the KMS client type to read one. A nil KMS yields a resolver that errors,
// so a caller needing a sealed key fails closed rather than reading a zero value.
//
// It lives on Deps because Deps is what HAS the KMS. It was written twice — byte
// for byte, in apps/company and apps/compliance — which is how a helper with no
// home ends up with two, free to drift apart while both look canonical.
//
// The return type is the bare signature, not any subsystem's named alias, so this
// package stays ignorant of who consumes it and the consumers keep their own names
// for it.
func (d Deps) Secret() func(ctx context.Context, ref string) ([]byte, error) {
	if d.KMS == nil {
		return func(context.Context, string) ([]byte, error) {
			return nil, fmt.Errorf("KMS not available")
		}
	}
	return d.KMS.GetSecret
}

// SecretFromEnv resolves the KMS reference held in the environment variable
// `name`. Only the reference travels through the environment; the value stays
// sealed until this call and never appears in a manifest, a pod spec or a log.
//
// This is the layer above Secret, and it had gone the same way: loadMPCKey and
// loadSafeJWTSecret in apps/wallet are the same eight lines twice, which is what
// the note above predicts of a helper with no home. Every env-held ref wants the
// same three decisions and they are worth making once.
//
// Empty ref, no KMS, or a KMS error all yield nil rather than an error, and a
// caller treats nil as "not configured" and fails closed. That is deliberate:
// these are read at construction, where the choice is between running without a
// capability and not running at all, and a subsystem that cannot reach its key
// should refuse the operation rather than the process. The reason is logged
// once, here, so it does not have to be logged identically at every call site.
func (d Deps) SecretFromEnv(ctx context.Context, name string) []byte {
	return secretFromEnv(ctx, d.KMS, name)
}

// SecretFromEnv is the same on Base, because Base is what a mounted subsystem
// holds — Use derives one from Deps — and a helper reachable only from the
// construction side is a helper the handlers cannot call.
func (b Base) SecretFromEnv(ctx context.Context, name string) []byte {
	return secretFromEnv(ctx, b.KMS, name)
}

func secretFromEnv(ctx context.Context, kms KMSClient, name string) []byte {
	ref := strings.TrimSpace(os.Getenv(name))
	if ref == "" || kms == nil {
		return nil
	}
	value, err := kms.GetSecret(ctx, ref)
	if err != nil {
		luxlog.Default().Warn("secret ref did not resolve from KMS",
			"env", name, "ref", ref, "err", err)
		return nil
	}
	return value
}

type Deps struct {
	// Logger is not here. The process default is (luxlog.Default, installed by
	// BuildDeps before anything can log), so a subsystem derives its scoped child
	// from the library rather than from a field it had to be handed. Carrying it
	// meant every mount checked it for nil — a field can be nil, a package-level
	// default cannot.

	// Brand, Env, Domain and DataDir are not here. They are deployment FACTS: a
	// subsystem cannot fail to connect to a brand name, it reads the string and
	// stamps it, which is what separated them from the clients below. Carrying them
	// meant every mount took a struct of twenty things to read one string, and a
	// field can be empty where a resolver cannot. They are cloud.Brand(), cloud.Env(),
	// cloud.Domain() and cloud.DataDir() in config.go — the same move the logger, the
	// process id and the model names made before them.

	// Version is the API contract/build version emitted as the X-Api-Version
	// response header (see middleware.ProductionHeaders wiring in serve.go).
	Version string

	// Self is not here. It was — THIS process's stable id, the StatefulSet ordinal
	// or the OS hostname — with a doc comment describing how a subsystem would
	// name the replica a status came from. No subsystem ever did: it had ZERO
	// readers. The id itself is real and still resolved by selfID(cfg), which the
	// durability membership elects on; what was dead was the copy on Deps. A field
	// whose justification is written entirely in the future tense is a plan, not a
	// dependency.

	// IAMIssuer is the canonical OIDC issuer (JWKS source) for this brand,
	// resolved from Brand via the white-label registry unless pinned by the
	// operator. Subsystems validate JWT `iss` + signatures against
	// {IAMIssuer}/v1/iam/.well-known/jwks (HIP-0111). One issuer per deployment.
	IAMIssuer string

	// MasterKey is the 32-byte at-rest KEK (decoded CLOUD_KMS_MASTER_KEY_REF), for
	// subsystems that encrypt their own stores and would otherwise each need a key
	// provisioned separately. One process, one key. nil ⇒ unset/invalid, and each
	// subsystem falls back to whatever it did before.
	MasterKey []byte

	// Durable is the per-deployment HA-durability factory every OrgStore routes
	// through: the shared ha election + vfs FencedStore over the S3
	// gateway + per-org envelope Cipher. nil ⇒ local-only (no object store creds,
	// dev/single-node), and every OrgStore is exactly the pre-durability cache.
	Durable *org.Durability

	// Peers says this deployment runs MORE THAN ONE writer. It is a fact about the
	// deployment, read from the same peer list buildDurability already parses, and
	// it exists for the one question the fence cannot answer when there is no
	// fence: on the local-only path, who owns an org? See [OrgStore.Owned].
	//
	// It is stated in the POSITIVE — "there are others" — so the zero value is the
	// single-writer deployment, which owns everything it holds. A process that
	// builds its own Base (a test, a subsystem with its own composition root) is
	// then an owner by default, which is what a lone process IS; the dangerous
	// arrangement is the one that has to say so.
	Peers bool

	// LiveMembers reads the CURRENT live writer set (the SAME ha.Membership snapshot the
	// durability fencer elects over). Non-nil ONLY when the durable plane is active — the
	// shard router then routes on the live set, so a draining/dead pod's orgs go to the
	// ready successor that hydrates them (M3), not to the gone pod. nil ⇒ the router falls
	// back to the static CLOUD_PEERS set: without the durable plane a peer cannot serve
	// another pod's local-only files, so ownership must stay pinned to the ordinal (which
	// reattaches its PVC across a restart). One field gates the whole live-routing path.
	LiveMembers func() []ha.Member

	// The default and failover MODELS are not here. They were, as
	// AIDefaultModel/AIFallbackModel sourced from CLOUD_AI_DEFAULT_MODEL /
	// CLOUD_AI_FALLBACK_MODEL, and they were not dependencies: a subsystem cannot
	// fail to connect to a model name. They are a routing and pricing DECISION,
	// and the eight subsystems that read them did the identical thing — copy the
	// string onto their own state, never branching on it — which is a constant
	// wearing a struct field's clothes. They are cloud.DefaultModel and
	// cloud.FallbackModel in model.go now, one literal each. See model.go for the
	// two incidents the env knob caused.

	// Subsystem clients — populated by BuildDeps based on enabled subsystems.
	// Each is an interface with both in-process and ZAP-RPC implementations.
	IAM      IAMClient
	KMS      KMSClient
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

	// Traffic is the edge's live sensor: per-credential request cadence, path
	// spread, auth-failure rate and the verdict currently held against a caller.
	// BuildDeps constructs it once; AbuseGate (middleware_abuse.go) writes it on
	// every request and the /v1/gateway/traffic op reads the caller's OWN org's
	// slice of it. In-memory and bounded by construction — it is a sensor, not a
	// record, and it is rebuilt from live traffic within one window after a
	// restart. Nil makes the gate a no-op passthrough.
	Traffic *edge.Traffic
}

// Per-subsystem client interfaces live in cloud/types so the
// cloud/clients package can implement them without an import cycle.
// We re-export them as aliases at the cloud root so subsystem code
// keeps writing cloud.IAMClient, cloud.KMSClient, etc.

type IAMClient = types.IAMClient
type KMSClient = types.KMSClient
type CommerceClient = types.CommerceClient
type AIClient = types.AIClient
type O11yClient = types.O11yClient
type VFSClient = types.VFSClient

// --- shared transport shapes ---
//
// These re-export the canonical shapes from cloud/types so subsystems and the
// clients package can both use them without pulling cloud as a dependency.
//
// They are not placeholders waiting on a generator. The ops that DO cross a
// process boundary declare their own In/Out types in package plane, and
// plane/gen emits the typed peer client from them — that is where a wire shape
// comes from now.

type ChatRequest = types.ChatRequest
type ChatResponse = types.ChatResponse
type EmbedRequest = types.EmbedRequest
type RerankRequest = types.RerankRequest
