package cloud

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/hanzoai/cloud/role"
)

// Config is the cloud binary's startup configuration. Drives which
// subsystems mount, what brand surface to serve, and where data lives.
type Config struct {
	// Enable lists subsystems to mount this run. Empty = all enabled.
	// Example: --enable=iam,base,kms,commerce,ai,gateway,o11y
	Enable []string

	// Replicas is the app-tier replica count the operator injects (CLOUD_REPLICAS,
	// mirroring the Deployment's spec.replicas). 0 = unset/unmanaged. It exists to
	// enforce ONE contract: embedded IAM (clients/iamsvc) uses Beego's
	// process-local "memory" session store, so an iam-enabled cloud MUST run at a
	// single replica or login/authorize sessions are lost across replicas.
	// Validate refuses to boot iam-enabled above 1; the helm chart pins replicas=1
	// whenever "iam" is in --enable. Migrating IAM sessions to a shared store lifts
	// this.
	Replicas int

	// Brand is the white-label brand identifier.
	Brand string

	// Env is the deployment environment (mainnet|testnet|devnet) per the 3-env
	// split. Billing fires in EVERY env — test/dev meter against their own
	// sandbox commerce/Square, never free — so Env is an attribution label, not
	// a gate. Empty when the operator has not set CLOUD_ENV.
	Env string

	// Domain is the deployment's primary public domain.
	Domain string

	// IAMIssuer is the JWKS issuer for JWT validation (usually iam.hanzo.ai).
	IAMIssuer string

	// AdminOrg is the IAM org slug whose members are GLOBAL admins (IAM's
	// IsGlobalAdmin: owner == AdminOrg). The in-binary identity sanitizer grants
	// admin authority — the c.IsAdmin() that gates /v1/admin/* writes, the
	// /v1/pricing/sync trigger, and the literal "admin" tenant bucket — ONLY to a
	// validated principal from this org, never to a raw header. Env IAM_ADMIN_ORG
	// (default "admin"), matching the gateway's admin-guard.
	AdminOrg string

	// JWKSURL is the JSON Web Key Set endpoint the identity sanitizer fetches IAM
	// signing keys from. Defaults to {IAMIssuer}/v1/iam/.well-known/jwks
	// (HIP-0111); override with CLOUD_JWKS_URL.
	JWKSURL string

	// JWTAudiences is the audience allowlist the sanitizer accepts (OR semantics).
	// Defaults to the known Hanzo IAM client_ids; override with CLOUD_JWT_AUDIENCES
	// (comma-separated) or GATEWAY_ALLOWED_AUDIENCES.
	JWTAudiences []string

	// KMSMasterKeyRef is the base64-encoded 32-byte KMS master key (KEK) the
	// embedded luxfi/kms store seals every secret's DEK under. The operator
	// injects it from a K8s Secret as CLOUD_KMS_MASTER_KEY_REF; cloud reads it
	// ONLY from env (never from the store it hosts — the bootstrap chicken-and-egg)
	// and never logs it. Empty ⇒ the KMS subsystem runs fail-closed (health-only).
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

	// ListenAddr is the public HTTP listener (default :8080).
	ListenAddr string

	// ZAPListenAddr is the ZAP-RPC listener (default :9653).
	ZAPListenAddr string

	// ZAPWebOrigins is the WebSocket Origin allowlist for the browser-facing
	// /zap ZAP plane (the SPA hosts that may open a ZAP-over-WS connection).
	// Empty == same-origin only. Set via CLOUD_ZAP_WEB_ORIGINS (comma-sep).
	ZAPWebOrigins []string

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

	// SitesApex is the zone whose subdomains are PUBLIC published-site hosts
	// (`<slug>.<apex>`, default hanzo.app). The site host-router (clients/sites)
	// serves the root path space for these hosts from OUR S3, ahead of the API
	// pipeline, so a published site is a public artifact — never a tenant API call.
	// Env CLOUD_SITES_APEX.
	SitesApex string

	// SitesReserved lists subdomain labels under SitesApex that are NOT sites and
	// must fall through to the normal pipeline (real app/api hosts on the apex).
	// This is the reserved-host exclusion that stops a published site from
	// shadowing a real hanzo.app app. The empty label (apex) and "www" are always
	// reserved; these add to them. Env CLOUD_SITES_RESERVED (comma-separated).
	SitesReserved []string

	// Endpoints for out-of-process subsystems (payments, vault). Empty
	// means the subsystem is disabled OR the deployment expects a default
	// service-discovery resolution.
	PaymentsZAPAddr string
	VaultZAPAddr    string

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

	// AI inference gateway — the /v1/agents run path. Agent runs execute a real
	// chat completion through an OpenAI-compatible endpoint (the Hanzo LLM
	// gateway). This is the ONE real inference wiring: with AIAPIKey set,
	// pickAIClient returns the HTTP gateway client; without it deps.AI is the
	// fail-closed stub and a run never fabricates output.
	//
	// AIBaseURL is the gateway /v1 root (CLOUD_AI_BASE_URL, default
	// https://api.hanzo.ai/v1); the client appends /chat/completions.
	//
	// AIAPIKey is a KMS-injected virtual key (CLOUD_AI_API_KEY). It is a SECRET —
	// never logged, never printed, never read from disk here.
	//
	// AIDefaultModel is the served model an agent with no explicit model falls
	// back to (CLOUD_AI_DEFAULT_MODEL, default deepseek-v4-flash). Model routing
	// is the gateway's job; this is the ONLY cloud-side model default.
	AIBaseURL      string
	AIAPIKey       string
	AIDefaultModel string

	// AIAuthClientID / AIAuthClientSecret are the binary's OWN IAM service
	// identity (IAM_CLIENT_ID / IAM_CLIENT_SECRET). When no static AIAPIKey is
	// set, the AI client authenticates to the gateway with a client-credentials
	// (M2M) token minted from this identity and auto-refreshed — the durable
	// no-static-key path. On the Hanzo deployment the identity resolves to
	// admin/hanzo-cloud (gateway-balance-exempt), so cloud's per-org
	// ResourceMeter remains the single debit. The token endpoint is derived from
	// IAMIssuer ({issuer}/v1/iam/oauth/token). The secret is KMS-injected and
	// never logged.
	AIAuthClientID     string
	AIAuthClientSecret string

	// ZAP RPC endpoints for subsystems that are NOT enabled in this
	// process but are still needed by an enabled subsystem. Empty
	// means "no remote endpoint" — the client falls back to the
	// disabled stub which fails closed with a clear error.
	//
	// Convention: <subsystem>.<env>.<deployment>.svc:9653 — the same
	// inter-subsystem listener port the unified binary exposes. The
	// transport is hanzoai/zap, never JSON.
	IAMZAPAddr      string
	KMSZAPAddr      string
	BaseZAPAddr     string
	CommerceZAPAddr string
	AIZAPAddr       string
	O11yZAPAddr     string
	VFSZAPAddr      string
	MQZAPAddr       string

	// --- Control plane (consensus platform) — STAGE 0: parsed but INERT. ---
	//
	// These describe this instance's place in the control-plane quorum for the
	// coming Quasar-PQ consensus wiring (see controlplane_deps.go for the
	// architecture direction). In Stage 0 NOTHING reads them: no engine is
	// started, no peer is dialed, no quorum is formed. They exist so operators can
	// begin declaring control-plane topology ahead of the engine; setting any of
	// them has ZERO runtime effect today.

	// NodeID is this instance's stable identity within the control-plane quorum.
	// Env NODE_ID. Empty ⇒ unset.
	NodeID string

	// Peers is the control-plane peer set (the other nodes this instance would
	// form consensus with). Env PEERS (comma-separated). Empty ⇒ unset.
	Peers []string

	// ControlPlaneRole is this instance's control-plane role: "voter"
	// (participates in consensus) or "data" (data-plane only). Env ROLE.
	// Empty ⇒ unset. NAMED ControlPlaneRole (not Role) to avoid colliding with
	// the HA Role field above (role.Role, CLOUD_ROLE): #160 (writer/reader HA
	// split) and #163 (Stage-0 control plane) each added a `Role` to this struct
	// on separate branches, which broke the build on merge. This one is the inert
	// Stage-0 string — read but consumed by nothing until the engine is wired.
	ControlPlaneRole string

	// ControlPlaneQuorum is the number of voter nodes required to form a
	// control-plane quorum. Env CONTROL_PLANE_QUORUM. 0 ⇒ unset.
	ControlPlaneQuorum int
}

// LoadConfig reads flags + env into a Config. Flags override env.
func LoadConfig() *Config {
	cfg := &Config{
		ListenAddr:       getenv("CLOUD_LISTEN", ":8080"),
		ZAPListenAddr:    getenv("CLOUD_ZAP_LISTEN", ":9653"),
		HealthListenAddr: getenv("CLOUD_HEALTH_LISTEN", ":9090"),
		AdminListenAddr:  getenv("CLOUD_ADMIN_LISTEN", ":8081"),
		ReadBufferSize:   getenvInt("GATEWAY_READ_BUFFER_SIZE", 32768),
		SitesApex:        getenv("CLOUD_SITES_APEX", "hanzo.app"),
		SitesReserved:    splitTrim(getenv("CLOUD_SITES_RESERVED", "www,api,app,admin,mail,ftp,cdn,static,assets")),
		Brand:            getenv("CLOUD_BRAND", DefaultBrand),
		Env:              getenv("CLOUD_ENV", ""),
		Role:             role.Writer, // safe default; Serve refines + validates from CLOUD_ROLE

		Replicas:         getenvInt("CLOUD_REPLICAS", 0),
		Domain:           getenv("CLOUD_DOMAIN", "api.hanzo.ai"),
		// IAMIssuer left empty here; resolved from Brand below unless pinned.
		IAMIssuer:       getenv("CLOUD_IAM_ISSUER", ""),
		AdminOrg:        getenv("IAM_ADMIN_ORG", "admin"),
		JWKSURL:         getenv("CLOUD_JWKS_URL", ""),
		KMSMasterKeyRef: getenv("CLOUD_KMS_MASTER_KEY_REF", ""),
		KMSMPCAddr:      getenv("CLOUD_KMS_MPC_ADDR", ""),
		KMSMPCVaultID:   getenv("CLOUD_KMS_MPC_VAULT_ID", ""),
		DataDir:         getenv("CLOUD_DATA_DIR", "/var/lib/cloud"),
		PaymentsZAPAddr: getenv("CLOUD_PAYMENTS_ZAP_ADDR", ""),
		VaultZAPAddr:    getenv("CLOUD_VAULT_ZAP_ADDR", ""),
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
		AIDefaultModel:     getenv("CLOUD_AI_DEFAULT_MODEL", "deepseek-v4-flash"),
		AIAuthClientID:     getenv("IAM_CLIENT_ID", ""),
		AIAuthClientSecret: getenv("IAM_CLIENT_SECRET", ""),
		IAMZAPAddr:         getenv("CLOUD_IAM_ZAP_ADDR", ""),
		KMSZAPAddr:         getenv("CLOUD_KMS_ZAP_ADDR", ""),
		BaseZAPAddr:        getenv("CLOUD_BASE_ZAP_ADDR", ""),
		CommerceZAPAddr:    getenv("CLOUD_COMMERCE_ZAP_ADDR", ""),
		AIZAPAddr:          getenv("CLOUD_AI_ZAP_ADDR", ""),
		O11yZAPAddr:        getenv("CLOUD_O11Y_ZAP_ADDR", ""),
		VFSZAPAddr:         getenv("CLOUD_VFS_ZAP_ADDR", ""),
		MQZAPAddr:          getenv("CLOUD_MQ_ZAP_ADDR", ""),

		// Control plane (consensus platform) — STAGE 0 inert topology (see Config).
		// Read but UNUSED: no subsystem consumes these until the engine is wired.
		NodeID:             getenv("NODE_ID", ""),
		Peers:              splitTrim(getenv("PEERS", "")),
		ControlPlaneRole:   getenv("ROLE", ""),
		ControlPlaneQuorum: getenvInt("CONTROL_PLANE_QUORUM", 0),
	}

	var enableCSV string
	flag.StringVar(&enableCSV, "enable", getenv("CLOUD_ENABLE", ""), "comma-separated subsystem list (empty=all)")
	flag.StringVar(&cfg.Brand, "brand", cfg.Brand, "white-label brand")
	flag.StringVar(&cfg.Domain, "domain", cfg.Domain, "primary domain")
	flag.StringVar(&cfg.IAMIssuer, "iam-issuer", cfg.IAMIssuer, "JWKS issuer")
	flag.StringVar(&cfg.KMSMasterKeyRef, "kms-master-key-ref", cfg.KMSMasterKeyRef, "KMS master key reference")
	flag.StringVar(&cfg.DataDir, "data-dir", cfg.DataDir, "data root")
	flag.StringVar(&cfg.ListenAddr, "listen", cfg.ListenAddr, "HTTP listener")
	flag.Parse()

	if enableCSV != "" {
		for _, name := range strings.Split(enableCSV, ",") {
			if s := strings.TrimSpace(name); s != "" {
				cfg.Enable = append(cfg.Enable, s)
			}
		}
	}

	// White-label by brand (HIP-0111): when the operator does not pin
	// CLOUD_IAM_ISSUER / --iam-issuer, derive the canonical OIDC issuer from the
	// brand so a lux deployment validates against lux.id, zoo against zoo.id,
	// etc. — never silently defaulting every brand to iam.hanzo.ai.
	if cfg.IAMIssuer == "" {
		cfg.IAMIssuer = IssuerForBrand(cfg.Brand)
	}

	// JWKS endpoint for the in-binary identity sanitizer. Default follows the
	// HIP-0111 convention {IAMIssuer}/v1/iam/.well-known/jwks so a brand
	// deployment validates against its own IAM; override with CLOUD_JWKS_URL.
	if cfg.JWKSURL == "" {
		cfg.JWKSURL = strings.TrimRight(cfg.IAMIssuer, "/") + "/v1/iam/.well-known/jwks"
	}
	cfg.JWTAudiences = jwtAudiencesFromEnv()

	// Browser ZAP-over-WS Origin allowlist. Default to the console SPA hosts so
	// the console can connect cross-origin; override with CLOUD_ZAP_WEB_ORIGINS.
	zapOrigins := getenv("CLOUD_ZAP_WEB_ORIGINS",
		"console.hanzo.ai,cloud.hanzo.ai,localhost:4000")
	for _, o := range strings.Split(zapOrigins, ",") {
		if s := strings.TrimSpace(o); s != "" {
			cfg.ZAPWebOrigins = append(cfg.ZAPWebOrigins, s)
		}
	}
	return cfg
}

// stagedSubsystems require EXPLICIT enablement: they are deliberately NOT part of
// the empty-Enable "mount everything" default and mount ONLY when named in
// CLOUD_ENABLE. This is the HIP-0106 staged-rollout contract, enforced in code.
//
// "iam" is staged because iamsvc.Mount boots the WHOLE Beego identity runtime via
// iamserver.InitEmbed(), which initialises process-global Beego config (web.BConfig
// / the shared AppConfig). The `ai` subsystem is a sibling casibase/casdoor fork
// linked against the SAME beego module, and reads that same process-global at its
// own bootstrap. Booting the IAM embed under the mount-all default corrupts the
// shared global so `ai` can no longer open its SQLite store — the binary crashes at
// boot with "ai: bootstrap: unable to open database file (14)" (SQLITE_CANTOPEN),
// which is exactly why every cloud release since the IAM embed (#142) failed its
// boot smoke and the fleet stayed pinned to a pre-embed image. Until that
// shared-global isolation is solved AND the fold is verified (login/authorize/
// token/jwks + the operator SSO chain), the operator activates IAM by ADDING "iam"
// to CLOUD_ENABLE explicitly; until then hanzo.id is served by the standalone iam
// pod and cloud runs iam-less exactly as it does in production today (pickIAMClient
// falls back to the remote/disabled IAM client — see build.go). ONE activation
// mechanism (the enable-list), ONE place.
var stagedSubsystems = map[string]bool{"iam": true, "ingress": true}

// Enabled reports whether subsystem `name` is enabled in this config.
// Empty Enable list = all subsystems enabled, EXCEPT staged subsystems
// (stagedSubsystems), which mount only when named in Enable explicitly.
func (c *Config) Enabled(name string) bool {
	if len(c.Enable) == 0 {
		return !stagedSubsystems[name]
	}
	for _, s := range c.Enable {
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

// defaultJWTAudiences mirrors github.com/hanzoai/gateway/v2/iamauth.DefaultAudiences
// (the known Hanzo IAM client_ids — each app's `aud` is its client_id) plus
// hanzo-cloud, cloud's own session client. Forwards-only: append new client_ids,
// never remove. Non-hanzo brands set CLOUD_JWT_AUDIENCES to their own client_ids.
var defaultJWTAudiences = []string{
	"hanzo-app",
	"hanzo-console",
	"hanzo-chat",
	"hanzo-id",
	"hanzo-admin-guard",
	"hanzo-cloud",
	"cowork",
	"https://api.hanzo.ai",
}

// jwtAudiencesFromEnv resolves the JWT audience allowlist for the identity
// sanitizer. CLOUD_JWT_AUDIENCES wins; GATEWAY_ALLOWED_AUDIENCES (the gateway's
// own override, shared so both agree) is honored next; otherwise the baked
// default. The white-label brand cloud audiences (BrandAudiences: <brand>-cloud)
// are ALWAYS unioned in — baked like BrandIssuers so ONE binary accepts a
// lux/zoo/pars token (aud=<brand>-cloud) even when the env override predates the
// brands (fail-secure: only ADDS known-good brand client_ids, never an arbitrary
// aud). Never empty, so the audience check is always enforced.
func jwtAudiencesFromEnv() []string {
	base := append([]string(nil), defaultJWTAudiences...)
	for _, key := range []string{"CLOUD_JWT_AUDIENCES", "GATEWAY_ALLOWED_AUDIENCES"} {
		if list := splitTrim(os.Getenv(key)); len(list) > 0 {
			base = list
			break
		}
	}
	return unionStrings(base, BrandAudiences())
}

// unionStrings appends every value of add not already present in base, preserving
// order and dropping empties. Used to fold the always-trusted brand audiences into
// the resolved allowlist without duplicating an env-supplied entry.
func unionStrings(base, add []string) []string {
	out := append([]string(nil), base...)
	seen := make(map[string]struct{}, len(out)+len(add))
	for _, s := range out {
		seen[s] = struct{}{}
	}
	for _, s := range add {
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
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
	// Embedded IAM (clients/iamsvc) uses Beego's process-local "memory" session
	// store, so a horizontally scaled app tier would mint a login/authorize
	// session on one replica and fail to find it on the next. Refuse to boot an
	// iam-enabled cloud above a single replica. CLOUD_REPLICAS=0 (unset) is the
	// unmanaged/dev case and is allowed — the helm chart pins replicas=1 whenever
	// "iam" is in --enable, so a managed deployment always sets it. Migrate IAM
	// sessions to a shared store to lift this.
	if c.Enabled("iam") && c.Replicas > 1 {
		return fmt.Errorf("iam is enabled but CLOUD_REPLICAS=%d > 1: embedded IAM uses a process-local session store and requires replicas=1 (pin the Deployment to 1 replica or migrate IAM sessions to a shared store)", c.Replicas)
	}
	return nil
}
