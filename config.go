package cloud

import (
	"flag"
	"fmt"
	"github.com/hanzoai/cloud/brand"
	"github.com/hanzoai/cloud/credz"
	"github.com/hanzoai/cloud/internal/datadir"
	"github.com/hanzoai/cloud/internal/edge"
	"github.com/hanzoai/cloud/role"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Config is the cloud binary's startup configuration. Drives which
// subsystems mount, what brand surface to serve, and where data lives.
type Config struct {
	// Enable lists subsystems to mount this run. Empty = all enabled.
	// Example: --enable=iam,base,kms,commerce,ai,gateway,o11y
	Enable []string

	// Replicas is the app-tier replica count the operator injects (CLOUD_REPLICAS,
	// mirroring the Deployment's spec.replicas). 0 = unset/unmanaged. It exists to
	// enforce ONE contract: embedded IAM (clients/iam) keeps its identity store as a
	// per-pod embedded SQLite file, so an iam-enabled cloud MUST run at a single
	// replica or each replica gets its OWN divergent identity store. Validate refuses
	// to boot iam-enabled above 1; the helm chart pins replicas=1 whenever "iam" is in
	// --enable. Pointing IAM at a shared external store lifts this.
	Replicas int

	// Brand is the white-label brand identifier.
	Brand string

	// Version is the API contract/build version emitted as the X-Api-Version
	// response header. Sourced from CLOUD_VERSION, else HANZO_VERSION — which the
	// operator sets on every container from the image tag it rendered, so a pod
	// can name its own build without a rebuild — else the link-time cloud.Version
	// default (see version.go).
	Version string

	// Env is the deployment environment (mainnet|testnet|devnet) per the 3-env
	// split. Billing fires in EVERY env — test/dev meter against their own
	// sandbox commerce/Square, never free — so Env is an attribution label, not
	// a gate. Empty when the operator has not set CLOUD_ENV.
	Env string

	// Domain is the deployment's primary public domain.
	Domain string

	// IAMIssuer is the JWKS issuer for JWT validation (usually iam.hanzo.ai).
	IAMIssuer string

	// AdminOrg is the IAM org slug whose members are SuperAdmins (IAM's
	// IsSuperAdmin: owner == AdminOrg). The in-binary identity sanitizer grants
	// admin authority — the c.IsAdmin() that gates /v1/admin/* writes, the
	// /v1/pricing/sync trigger, and the literal "admin" org bucket — ONLY to a
	// validated principal from this org, never to a raw header. Env IAM_ADMIN_ORG
	// (default "admin"), matching the gateway's admin-guard.
	AdminOrg string

	// JWKSURL is the JSON Web Key Set endpoint the identity sanitizer fetches IAM
	// signing keys from. Defaults to {IAMIssuer}/v1/iam/.well-known/jwks
	// (HIP-0111); override with CLOUD_JWKS_URL.
	JWKSURL string

	// KMSMasterKeyRef is the base64-encoded 32-byte KMS master key (KEK) the
	// embedded luxfi/kms store seals every secret's DEK under, and the ONE
	// credential a deployment provisions. The operator injects it from a K8s
	// Secret as CLOUD_KMS_MASTER_KEY_REF; it is never read from the store it
	// hosts (the bootstrap chicken-and-egg) and never logged. Empty ⇒ the KMS
	// subsystem runs fail-closed (health-only).
	//
	// It is sourced from credz, not from the environment: credz.Boot takes it out
	// of the environment at process start so no spawned child inherits it, and
	// holds it in memory. See rootKeyRef.
	KMSMasterKeyRef string

	// KMSMPCAddr / KMSMPCVaultID configure the MPC threshold-signing backend for
	// KMS Sign. Both empty (the default) ⇒ Sign fails closed with a clear error;
	// signing is never fabricated. Set via CLOUD_KMS_MPC_ADDR / CLOUD_KMS_MPC_VAULT_ID.
	KMSMPCAddr    string
	KMSMPCVaultID string

	// DataDir is the on-disk data root.
	DataDir string

	// Role is the HA role of this process (CLOUD_ROLE): the single Writer that
	// owns the RWO stores (default) or a read-only Reader replica. Resolved and
	// validated in Serve; defaults to Writer so an unset variable is byte-identical
	// to today's single-pod deployment.
	Role role.Role

	// WriterURL is the base URL of the single writer (CLOUD_WRITER_URL, e.g.
	// http://cloud-writer.hanzo.svc:8000). A Reader forwards EVERY request here —
	// it opens no stores and is a transparent, always-ready edge that absorbs the
	// writer's rollout gap (see reader_proxy.go). Required when Role==Reader;
	// ignored by a Writer.
	WriterURL string

	// ReaderRetryBudget bounds how long a Reader retries a request the writer
	// could not yet accept (connection refused / no ready endpoint) during a
	// writer roll, before returning 502. Dial-only retry (the request never
	// reached the writer) keeps a non-idempotent POST safe. CLOUD_READER_RETRY_BUDGET
	// (Go duration, default 25s).
	ReaderRetryBudget time.Duration

	// The writer lease is NOT a field here. CLOUD_WRITER_LEASE has exactly one
	// reader — internal/writerlease — because the answer depends on something a
	// Config cannot see: this process's position in the pod's process tree. A
	// bool parsed here would be true in every one of a pod's processes alike,
	// which is precisely the reading that took api.hanzo.ai down on 2026-08-04.

	// ShardPeers is the CLOUD_PEERS membership list ("id@addr,id2@addr2") of the
	// horizontal-scale StatefulSet: the full, STABLE set of writer pods (each a
	// cloud-N ordinal at its headless per-pod DNS cloud-N.<svc>:8000). Every org is
	// pinned to exactly one owner pod by rendezvous hashing (ha.Owner over this set),
	// so each org's per-org SQLite files are written by ONE pod only; a request whose
	// org this pod does not own is forwarded to the owner (shardrouter.go). Empty or a
	// single entry ⇒ sharding OFF, byte-identical to the single-pod deployment. The
	// set is identical on every pod (static env), so all pods agree on every org's
	// owner — no split-brain dual-writer, no routing loop.
	ShardPeers string

	// ShardSelf is THIS pod's stable id — the StatefulSet ordinal name cloud-N, from
	// CLOUD_POD_NAME (or the downward-API POD_NAME). When sharding is on it MUST be one
	// of ShardPeers' ids (Validate refuses otherwise, so a misconfigured ordinal cannot
	// black-hole every request by forwarding it away with no shard of its own).
	ShardSelf string

	// PeerSelector is the Kubernetes label selector that names this deployment's writer
	// pods (CLOUD_PEER_SELECTOR, e.g. "app.kubernetes.io/name=cloud,app.kubernetes.io/
	// instance=<release>"). When set AND running in-cluster, membership is LIVE — the
	// binary lists Ready, non-terminating pods matching it via the K8s API, so a rolling
	// upgrade's changing pod set is tracked and a draining/dead pod is never elected an
	// org's owner. Empty (dev / native-Go / not in a cluster) falls back to the STATIC
	// ShardPeers/self set — capability-detected, no on/off flag. Deployment wiring the
	// chart sets, not a feature toggle.
	PeerSelector string

	// ListenAddr is the public HTTP listener (default :8080).
	ListenAddr string

	// ZAPListenAddr is the ZAP-RPC listener (default :9653).
	ZAPListenAddr string

	// ZAPWebOrigins is the WebSocket Origin allowlist for the browser-facing
	// /zap ZAP plane (the SPA hosts that may open a ZAP-over-WS connection).
	// Empty == same-origin only. Set via CLOUD_ZAP_WEB_ORIGINS (comma-sep).
	ZAPWebOrigins []string

	// MarkdownDefaultPrefixes lists path prefixes whose successful JSON
	// responses default to markdown (zap-proto/md) when the caller expresses no
	// format preference — the agent-facing endpoints (e.g. /v1/code/, /v1/agents/).
	// A caller always keeps the override: ?format=json or Accept: application/json
	// forces JSON even here, and JSON stays the default everywhere else. Empty ==
	// JSON everywhere unless explicitly negotiated. Env CLOUD_MARKDOWN_DEFAULT_PREFIXES
	// (comma-separated). See middleware_markdown.go.
	MarkdownDefaultPrefixes []string

	// Edge policy (middleware_edge.go) — the "gateway role" cloud absorbs when it
	// serves the public api.hanzo.ai edge directly, no KrakenD hop.
	//
	// CORSOrigins is the browser-CORS allowlist for the /v1 edge. Each entry is an
	// exact origin ("https://hanzo.ai"), a bare host ("hanzo.ai"), or a host
	// wildcard ("*.hanzo.ai" = apex + any subdomain). EMPTY ⇒ CORS is OWNED
	// ELSEWHERE (the shared Traefik ingress `cors-allow-all` fronting api.hanzo.ai)
	// and cloud emits NO CORS headers — set CLOUD_CORS_ORIGINS only on a direct
	// DO-LB→cloud edge, so exactly one layer answers CORS (never both → duplicate
	// ACAO breaks the browser). Read from CLOUD_CORS_ORIGINS — one name, so a
	// deployment cannot half-configure the allowlist under a second spelling.
	CORSOrigins []string

	// EdgeRateEnabled turns on the per-client-IP edge flood cap that runs BEFORE
	// identity (CLOUD_EDGE_RATELIMIT, default true — the gateway enforced this, so
	// dropping it silently would drop a protection). EdgeRatePerIP requests per
	// EdgeRateWindowSec seconds are allowed per public client IP (leftmost
	// X-Forwarded-For); in-cluster direct callers carry no XFF and are exempt.
	// Defaults 100/1s mirror the gateway service-tier client_max_rate (strategy:ip).
	EdgeRateEnabled   bool
	EdgeRatePerIP     int
	EdgeRateWindowSec int

	// HealthListenAddr is the health/metrics listener (default :9090).
	HealthListenAddr string

	// AdminListenAddr is the admin endpoint (default :8081, gated by IAM admin).
	AdminListenAddr string

	// ReadBufferSize is the fasthttp per-conn request-read buffer for the public
	// HTTP edge (zip/fiber), in bytes. fasthttp caps total request-header size at
	// this value and returns 431 (Request Header Fields Too Large) above it. The
	// framework default is 4 KiB — too small once a multi-domain SSO session (an
	// admin-guard Domain=.hanzo.ai cookie set on EVERY subdomain) pushes a
	// browser's request headers past ~4 KiB, 431-ing legitimate requests. This
	// raises the edge ceiling to a sane 32 KiB (nginx large_client_header_buffers
	// parity). Env GATEWAY_READ_BUFFER_SIZE (shared with the gateway edge so both
	// trust boundaries agree on ONE value); tunable down if the per-conn memory
	// budget (SCALE_STANDARD §8) demands it. Internal zip services keep the 4 KiB
	// framework default — only the browser-facing edge opts up.
	ReadBufferSize int

	// BodyLimit is the maximum request body the public HTTP edge (zip/fiber)
	// accepts, in bytes. The framework default is 4 MiB — which silently caps the
	// CONTEXT WINDOW: a chat request carries its whole prompt in the body, and at
	// ~4.3 bytes/token a 1M-token prompt is ~4.3 MB. So the 1M-context models we
	// route to (deepseek-v4-pro; anything glm-5.2 overflows into past its 262,144
	// cap) could not actually be reached — fasthttp refused the body before any
	// handler ran, with the opaque 400 "Error when parsing request" that reads
	// like a malformed payload rather than a size cap. 16 MiB gives 1M tokens
	// real headroom (~3.7x) without inviting a memory-exhaustion vector: the edge
	// is authenticated + rate-limited, and fasthttp streams rather than buffering
	// per-conn. Env GATEWAY_BODY_LIMIT.
	BodyLimit int

	// The site-edge configuration (apex, reserved labels, self domains, first-party
	// sites) is NOT here: it is resolved by sites.ConfigFromEnv, in the package that
	// owns the type. Both processes that mount the edge — this one and the light
	// router that owns the public port — read that one function, so the env keys and
	// their defaults cannot drift apart again. Domain (above) is the only field of
	// this struct that feeds it.

	// Billing gate (commerce metering) — the request-edge balance gate.
	//
	// CommerceHTTPURL is the commerce service base over HTTP (the metering
	// client speaks net/http, not ZAP). Empty disables the gate entirely.
	//
	// CommerceServiceToken is the admin-scoped commerce S2S token. It is a
	// SECRET sourced from a KMS-backed secret the operator injects as
	// COMMERCE_SERVICE_TOKEN — never hard-coded or read from disk here.
	//
	// BillingFailOpen flips the gate to allow-on-error. Default is
	// fail-closed (deny when balance can't be determined), matching the
	// gateway. Set only where availability outranks billing.
	CommerceHTTPURL      string
	CommerceServiceToken string
	BillingFailOpen      bool

	// The spend gate (middleware_spend.go) is deliberately NOT configured here. It reads
	// the `paywall_enforced` platform switch and nothing else, so the admin cockpit is
	// its single source of truth and the kill switch can always disarm it. A
	// PAYWALL_ENFORCED field here ORed with the switch, which meant an env var could arm
	// a gate the cockpit could not turn off — the one failure mode a kill switch exists
	// to prevent. One reader, one answer.

	// AI inference gateway. Two DISTINCT endpoints, two DISTINCT credentials by
	// concern (see build.go pickCompletionsClient / pickEmbedClient):
	//   - CHAT COMPLETIONS (deps.AI, a WRITE endpoint) — agents/guide/crm/content/
	//     sitegen/code-ask. Authenticated by the IAM M2M identity below, NEVER by a
	//     read-only publishable (pk-) key.
	//   - EMBEDDINGS (deps.Embed, a READ-ONLY endpoint) — code-index + KB. This is
	//     the ONLY consumer of AIAPIKey (the pk- key); read-only is exactly what a
	//     publishable key may do.
	//
	// AIBaseURL is the gateway /v1 root (CLOUD_AI_BASE_URL, default
	// https://api.hanzo.ai/v1); the client appends /chat/completions or /embeddings.
	//
	// AIAPIKey is the KMS-injected static gateway key (CLOUD_AI_API_KEY ←
	// cloud-ai-embed-key). It is a SECRET — never logged, printed, or read from disk
	// here. On the Hanzo deployment it is a read-only PUBLISHABLE (pk-) key: it feeds
	// deps.Embed (read-only, valid) and is REFUSED for deps.AI completions, which the
	// gateway would 403 ("Publishable keys can only access read-only endpoints"). A
	// completions-capable secret key (sk-) set here would instead drive both.
	//
	// The default and failover MODELS are NOT configured here. CLOUD_AI_DEFAULT_MODEL
	// and CLOUD_AI_FALLBACK_MODEL are gone; the values are cloud.DefaultModel and
	// cloud.FallbackModel (model.go), one literal each. A model name is a routing
	// and pricing decision, not an address — there is nothing to connect to and
	// nothing to fail — and holding it in two places (a constant AND the
	// deployment's env) is what let production run a value the constant disagreed
	// with, undetected, until someone read both. The failover value is worse than
	// redundant: `best` is a SKU in the GATEWAY's catalog with its own server-side
	// route and fallback chain, so a knob here could only disagree with the thing
	// that actually decides. No deployment ever set it.
	AIBaseURL string
	AIAPIKey  string

	// AIAuthClientID / AIAuthClientSecret are the binary's OWN IAM service
	// identity (IAM_CLIENT_ID / IAM_CLIENT_SECRET). The completions client (deps.AI)
	// authenticates to the gateway with a client-credentials (M2M) token minted from
	// this identity and auto-refreshed — the durable no-static-key path, and the ONLY
	// completions credential when AIAPIKey is the read-only pk- embed key. On the
	// Hanzo deployment the identity resolves to admin/hanzo-cloud (gateway-balance-
	// exempt), so cloud's per-org ResourceMeter remains the single debit. The token
	// endpoint is derived from IAMIssuer ({issuer}/v1/iam/oauth/token). The secret is
	// KMS-injected and never logged.
	AIAuthClientID     string
	AIAuthClientSecret string

}

// flagsOnce guards the ONE registration of the CLI overrides on the process-global
// flag.CommandLine. LoadConfig runs once in production (main) but many times across a
// test binary (body_limit_test, brand_test, …); a second flag.StringVar of the same
// name panics ("flag redefined: enable"). Flags are a command-line concern orthogonal
// to the env resolution every call performs, so bind + parse them exactly once.
var flagsOnce sync.Once

// LoadConfig reads flags + env into a Config. Flags override env.
func LoadConfig() *Config {
	cfg := &Config{
		ListenAddr:              getenv("CLOUD_LISTEN", ":8080"),
		ZAPListenAddr:           getenv("CLOUD_ZAP_LISTEN", ":9653"),
		HealthListenAddr:        getenv("CLOUD_HEALTH_LISTEN", ":9090"),
		AdminListenAddr:         getenv("CLOUD_ADMIN_LISTEN", ":8081"),
		ReadBufferSize:          edge.ReadBufferSize(),
		BodyLimit:               edge.BodyLimit(),
		MarkdownDefaultPrefixes: splitTrim(getenv("CLOUD_MARKDOWN_DEFAULT_PREFIXES", "")),
		Brand:                   getenv("CLOUD_BRAND", DefaultBrand),
		Version:                 resolveVersion(),
		Env:                     getenv("CLOUD_ENV", ""),
		Role:                    role.Writer, // safe default; Serve refines + validates from CLOUD_ROLE

		Replicas: getenvInt("CLOUD_REPLICAS", 0),
		// Domain left empty here; derived from Brand below unless pinned — the
		// literal "api.hanzo.ai" default used to live here, which meant a lux
		// deployment that pinned nothing answered with Hanzo's host in every URL
		// it built about itself.
		Domain: getenv("CLOUD_DOMAIN", ""),
		// IAMIssuer left empty here; resolved from Brand below unless pinned.
		IAMIssuer:         getenv("CLOUD_IAM_ISSUER", ""),
		AdminOrg:          getenv("IAM_ADMIN_ORG", "admin"),
		JWKSURL:           getenv("CLOUD_JWKS_URL", ""),
		KMSMasterKeyRef:   rootKeyRef(),
		KMSMPCAddr:        getenv("CLOUD_KMS_MPC_ADDR", ""),
		KMSMPCVaultID:     getenv("CLOUD_KMS_MPC_VAULT_ID", ""),
		DataDir:           DataDir(),
		WriterURL:         strings.TrimRight(getenv("CLOUD_WRITER_URL", ""), "/"),
		ReaderRetryBudget: getenvDuration("CLOUD_READER_RETRY_BUDGET", 25*time.Second),
		ShardPeers:        getenv("CLOUD_PEERS", ""),
		ShardSelf:         firstNonEmptyStr(getenv("CLOUD_POD_NAME", ""), getenv("POD_NAME", "")),
		PeerSelector:      getenv("CLOUD_PEER_SELECTOR", ""),
		// Billing gate (KMS-backed COMMERCE_SERVICE_TOKEN; never plaintext).
		CommerceHTTPURL:      getenv("CLOUD_COMMERCE_HTTP_URL", ""),
		CommerceServiceToken: getenv("COMMERCE_SERVICE_TOKEN", ""),
		BillingFailOpen:      getenvBool("BILLING_FAIL_OPEN"),
		// AI inference gateway. CLOUD_AI_API_KEY (KMS-backed) is an optional static
		// override; absent it, the AI client authenticates via M2M using the
		// binary's own IAM identity (IAM_CLIENT_ID / IAM_CLIENT_SECRET) — no static
		// key, no expiry cliff. Never plaintext.
		AIBaseURL:          getenv("CLOUD_AI_BASE_URL", "https://api.hanzo.ai/v1"),
		AIAPIKey:           getenv("CLOUD_AI_API_KEY", ""),
		AIAuthClientID:     getenv("IAM_CLIENT_ID", ""),
		AIAuthClientSecret: getenv("IAM_CLIENT_SECRET", ""),
	}

	// THE BINARY KNOWS WHICH APP IT IS; a deployment does not restate it.
	//
	// cfg.Enable is set by Listen from the app the plugin was built as — one
	// process per app, and plugin/<app>/main.go names it. CLOUD_ENABLE/--enable
	// used to state the same set a SECOND time, from the values file, and the
	// only thing a second source of truth can add is disagreement: both devnet
	// outages on 2026-08-02 were this list naming an app that does not exist
	// ("plans") and omitting one that every other child needs ("kms"). Neither
	// is representable now. Empty still means all, which is what production has
	// always run.
	flagsOnce.Do(func() {
		flag.StringVar(&cfg.Brand, "brand", cfg.Brand, "white-label brand")
		flag.StringVar(&cfg.Domain, "domain", cfg.Domain, "primary domain")
		flag.StringVar(&cfg.IAMIssuer, "iam-issuer", cfg.IAMIssuer, "JWKS issuer")
		flag.StringVar(&cfg.KMSMasterKeyRef, "kms-master-key-ref", cfg.KMSMasterKeyRef, "KMS master key reference")
		flag.StringVar(&cfg.DataDir, "data-dir", cfg.DataDir, "data root")
		flag.StringVar(&cfg.ListenAddr, "listen", cfg.ListenAddr, "HTTP listener")
		flag.Parse()
	})

	// White-label by brand (HIP-0111): when the operator does not pin
	// CLOUD_IAM_ISSUER / --iam-issuer, derive the canonical OIDC issuer from the
	// brand so a lux deployment validates against lux.id, zoo against zoolabs.id,
	// etc. — never silently defaulting every brand to iam.hanzo.ai.
	//
	// issuerFor is THE resolution, shared with the package-level IAMIssuer()
	// (iamurl.go). That function used to read CLOUD_IAM_ISSUER raw and stop, so on
	// any deployment that let the brand supply the issuer — the documented happy
	// path — this field held "https://lux.id" while IAMIssuer() held "". The two
	// cannot differ now because there is one function.
	cfg.IAMIssuer = issuerFor(cfg.IAMIssuer, cfg.Brand)

	// The deployment's own public API host, by the same rule and for the same
	// reason: api.<the brand's apex>. The brand registry already states each
	// brand's apex (brand.Info.Domain) and already says base URLs derive from it
	// — every brand but hanzo simply never got the derivation, so an unpinned lux
	// or zoo deployment presented api.hanzo.ai as its own host.
	cfg.Domain = domainFor(cfg.Domain, cfg.Brand)

	// JWKS endpoint for the in-binary identity sanitizer. Default follows the
	// HIP-0111 convention {IAMIssuer}/v1/iam/.well-known/jwks so a brand
	// deployment validates against its own IAM; override with CLOUD_JWKS_URL.
	// JWKSURLFor is the ONE derivation, shared with NewTokenValidator so a
	// subsystem that verifies a token resolves the same keys this boundary does.
	if cfg.JWKSURL == "" {
		cfg.JWKSURL = JWKSURLFor(cfg.IAMIssuer)
	}

	// Browser ZAP-over-WS Origin allowlist. Default to the console SPA hosts so
	// the console can connect cross-origin; override with CLOUD_ZAP_WEB_ORIGINS.
	zapOrigins := getenv("CLOUD_ZAP_WEB_ORIGINS",
		"console.hanzo.ai,cloud.hanzo.ai,localhost:4000")
	for _, o := range strings.Split(zapOrigins, ",") {
		if s := strings.TrimSpace(o); s != "" {
			cfg.ZAPWebOrigins = append(cfg.ZAPWebOrigins, s)
		}
	}

	// Edge policy (middleware_edge.go). CORS default OFF (ingress owns it on the
	// recommended rollout — see Config.CORSOrigins); the per-IP flood cap default
	// ON at gateway-parity 100/1s so a protection is never dropped silently.
	cfg.CORSOrigins = splitTrim(getenv("CLOUD_CORS_ORIGINS", ""))
	cfg.EdgeRateEnabled = getenvBoolDefault("CLOUD_EDGE_RATELIMIT", true)
	cfg.EdgeRatePerIP = getenvInt("CLOUD_EDGE_RATELIMIT_PER_IP", 100)
	cfg.EdgeRateWindowSec = getenvInt("CLOUD_EDGE_RATELIMIT_WINDOW_SEC", 1)
	return cfg
}

// Enabled reports whether subsystem `name` is enabled in this config.
// An empty Enable list mounts everything; a non-empty one mounts exactly what it
// names. There is no third case.
func (c *Config) Enabled(name string) bool {
	if len(c.Enable) == 0 {
		return true
	}
	return contains(c.Enable, name)
}

func contains(list []string, name string) bool {
	for _, s := range list {
		if s == name {
			return true
		}
	}
	return false
}

func getenv(key, dflt string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return dflt
}

// DefaultDataDir is the on-disk data root when CLOUD_DATA_DIR is unset.
const DefaultDataDir = datadir.Default

// DataDir resolves the data root from the environment. It is exported and split
// out of LoadConfig because credz.Boot must find the same directory BEFORE
// LoadConfig runs — the credential broker's socket lives there, and the
// credentials have to be installed before anything reads config or opens a store.
//
// The rule itself lives in internal/datadir because the ROUTER needs it too, and
// the router cannot link this package. It takes the pod's writer lease on a file
// under this root before it spawns the children that open stores under it, so
// the two must resolve the same directory or the lock guards nothing.
func DataDir() string { return datadir.Resolve() }

// rootKeyRef resolves the root key credz holds. credz.Boot moves it out of the
// environment (so no spawned child inherits it) and into memory, which makes
// credz the single source; the raw variable is the fallback for a process that
// never Booted — a test building a Config directly — and is the same value one
// step earlier.
func rootKeyRef() string {
	if b64 := credz.KeyB64(); b64 != "" {
		return b64
	}
	return getenv(credz.RootEnv, "")
}

// resolveVersion is the single source of Config.Version, so the test and the
// boot path cannot disagree about how it is sourced.
//
// CLOUD_VERSION is the explicit override. HANZO_VERSION is set by the operator
// on every container from the image tag it rendered, which is what lets a pod
// report the build it actually is; without it the binary answers the link-time
// default and a rollout cannot be verified from outside.
func resolveVersion() string {
	return getenv("CLOUD_VERSION", getenv("HANZO_VERSION", Version))
}

// domainFor is THE resolution of the deployment's own public API host: an
// explicit pin wins, else api.<the brand's apex> from the registry. It is the
// exact shape of issuerFor, for the exact same reason — one decision, one
// function, no second place to hold a different answer.
//
// The old default was the bare literal "api.hanzo.ai", applied whatever the
// brand. IAMIssuer derived from the brand and Domain did not, so an unpinned lux
// deployment resolved issuer=https://lux.id alongside domain=api.hanzo.ai and
// went on to build every self-referential URL — OAuth redirect_uri, avatar URL,
// git clone URL — pointing at another brand's host. brand.Info.Domain already
// carried each brand's apex and its own doc already said base URLs derive from
// it; only hanzo ever got the derivation.
func domainFor(pinned, brandID string) string {
	if p := strings.TrimSpace(pinned); p != "" {
		return p
	}
	return brand.APIHost(brandID)
}

// splitTrim splits a comma-separated list, trimming and dropping empties.
func splitTrim(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// getenvBool reports whether an env var is set to a truthy value
// (true/1/yes, case-insensitive). Matches metering's envTrue semantics so the
// billing flags read consistently across products.
func getenvBool(key string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "true", "1", "yes":
		return true
	default:
		return false
	}
}

// getenvBoolDefault reads a boolean env var with an explicit default: dflt when
// unset/blank, else true for true/1/yes and false for false/0/no (matching
// getenvBool's truthy set). Lets a protection default ON while staying operator-
// disableable (CLOUD_EDGE_RATELIMIT=false), which getenvBool (default-false) can't.
func getenvBoolDefault(key string, dflt bool) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "":
		return dflt
	case "true", "1", "yes":
		return true
	default:
		return false
	}
}

// getenvDuration reads key as a Go duration (e.g. "25s", "1m"), returning dflt
// when unset, blank, or unparseable (a malformed override can never silently
// zero a timeout).
func getenvDuration(key string, dflt time.Duration) time.Duration {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return dflt
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return dflt
	}
	return d
}

// getenvInt reads key as a base-10 int, returning dflt when unset, blank, or
// unparseable (a malformed override can never silently zero a scale knob).
func getenvInt(key string, dflt int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return dflt
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return dflt
	}
	return n
}

// Validate returns an error if the config is missing required values.
func (c *Config) Validate() error {
	if c.Brand == "" {
		return fmt.Errorf("brand is required")
	}
	if c.Domain == "" {
		return fmt.Errorf("domain is required")
	}
	if c.DataDir == "" {
		return fmt.Errorf("data-dir is required")
	}
	// Embedded IAM (clients/iam) keeps its identity store as a per-pod embedded
	// SQLite file ({DataDir}/iam/global.db), so a horizontally scaled app tier would give
	// each replica its OWN divergent identity store — a user/session written on one
	// replica is absent on the next. Refuse to boot an iam-enabled cloud above a single
	// replica. CLOUD_REPLICAS=0 (unset) is the unmanaged/dev case and is allowed — the
	// helm chart pins replicas=1 whenever Enabled("iam") holds, which includes the
	// empty (mount-all) enable list, so a managed deployment always sets it. Point IAM
	// at a shared external store to lift this.
	if c.Enabled("iam") && c.Replicas > 1 {
		return fmt.Errorf("iam is enabled but CLOUD_REPLICAS=%d > 1: embedded IAM uses a per-pod SQLite store and requires replicas=1 (pin the Deployment to 1 replica or point IAM at a shared store)", c.Replicas)
	}
	// Horizontal shard routing (CLOUD_PEERS names >1 pod). Two fail-closed guards:
	//   1. THIS pod must be one of the peers, else it owns no shard and would forward
	//      every request away (a silent black-hole) — refuse to boot.
	//   2. Embedded IAM cannot be sharded: its per-pod SQLite identity store is local to
	//      each pod, so a login/authorize step served on one pod is unreachable on the
	//      owner pod a later request routes to. Disable iam (use external iam.hanzo.svc).
	// Both are boot errors, never guesses — a wrong shard topology must fail loud.
	if peers := parsePeers(c.ShardPeers); len(peers) >= 2 {
		if c.ShardSelf == "" {
			return fmt.Errorf("CLOUD_PEERS names %d pods but POD_NAME/CLOUD_POD_NAME is empty: a shard member must know its own ordinal id", len(peers))
		}
		if !peersContain(peers, c.ShardSelf) {
			return fmt.Errorf("shard self %q is not in CLOUD_PEERS %q: this pod is not a member of its own ring (it would forward every request away and own no shard)", c.ShardSelf, c.ShardPeers)
		}
		if c.Enabled("iam") {
			return fmt.Errorf("iam is enabled with CLOUD_PEERS shard routing: embedded IAM uses a per-pod SQLite store and cannot be sharded; disable iam (use external iam.hanzo.svc) or run a single pod")
		}
	}
	return nil
}
