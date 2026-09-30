package cloud

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
)

// Config is the cloud binary's startup configuration. Drives which
// subsystems mount, what brand surface to serve, and where data lives. Every
// default is local: a laptop with no network runs with nothing set.
type Config struct {
	// Enable lists subsystems to mount this run. Empty = all enabled.
	// Example: --enable=base,kms,tasks
	Enable []string

	// EnableStaged ADDITIVELY activates staged subsystems (stagedSubsystems) on top
	// of the default set, orthogonally to the Enable allowlist, so "every default
	// subsystem PLUS functions" needs no hand-enumerated allowlist. One lever per
	// intent: Enable = "exactly this set", EnableStaged = "also turn these staged
	// ones on".
	EnableStaged []string

	// Brand is the white-label brand identifier.
	Brand string

	// Version is the API contract/build version emitted as the X-Api-Version
	// response header. Sourced from CLOUD_VERSION, else the link-time cloud.Version
	// default (see version.go).
	Version string

	// Env is a free-form environment label (CLOUD_ENV). Empty when unset.
	Env string

	// Domain is the host this deployment answers on (default localhost:8080). It
	// names the server in the OpenAPI document.
	Domain string

	// IAMIssuer is the Hanzo IAM issuer JWTs are validated against (default
	// http://127.0.0.1:8000, a local Hanzo IAM). Set CLOUD_IAM_ISSUER to accept
	// tokens from another IAM.
	IAMIssuer string

	// AdminOrg is the IAM org slug whose members are SuperAdmins (IAM's
	// IsSuperAdmin: owner == AdminOrg). The in-binary identity sanitizer grants
	// admin authority ONLY to a validated principal from this org, never to a raw
	// header. Env IAM_ADMIN_ORG (default "admin").
	AdminOrg string

	// JWKSURL is the JSON Web Key Set endpoint the identity sanitizer fetches IAM
	// signing keys from. Defaults to {IAMIssuer}/v1/iam/.well-known/jwks
	// (HIP-0111); override with CLOUD_JWKS_URL.
	JWKSURL string

	// KMSMasterKeyRef is the base64-encoded 32-byte KMS master key (KEK) the
	// embedded luxfi/kms store seals every secret's DEK under. Read ONLY from env
	// (CLOUD_KMS_MASTER_KEY_REF) and never logged. Empty ⇒ the KMS subsystem runs
	// fail-closed (health-only).
	KMSMasterKeyRef string

	// KMSMPCAddr / KMSMPCVaultID configure the MPC threshold-signing backend for
	// KMS Sign. Both empty (the default) ⇒ Sign fails closed with a clear error;
	// signing is never fabricated. Set via CLOUD_KMS_MPC_ADDR / CLOUD_KMS_MPC_VAULT_ID.
	KMSMPCAddr    string
	KMSMPCVaultID string

	// DataDir is the on-disk data root.
	DataDir string

	// ListenAddr is the public HTTP listener (default :8080).
	ListenAddr string

	// ZAPListenAddr is the ZAP-RPC listener (default 127.0.0.1:9653).
	ZAPListenAddr string

	// ZAPWebOrigins is the WebSocket Origin allowlist for the browser-facing
	// /zap ZAP plane (the SPA hosts that may open a ZAP-over-WS connection).
	// Empty == same-origin only. Set via CLOUD_ZAP_WEB_ORIGINS (comma-sep).
	ZAPWebOrigins []string

	// MarkdownDefaultPrefixes lists path prefixes whose successful JSON
	// responses default to markdown (zap-proto/md) when the caller expresses no
	// format preference. A caller always keeps the override: ?format=json or
	// Accept: application/json forces JSON. Env CLOUD_MARKDOWN_DEFAULT_PREFIXES
	// (comma-separated). See middleware_markdown.go.
	MarkdownDefaultPrefixes []string

	// Edge policy (middleware_edge.go).
	//
	// CORSOrigins is the browser-CORS allowlist for the /v1 edge. Each entry is an
	// exact origin ("http://localhost:3000"), a bare host ("localhost"), or a host
	// wildcard ("*.example.com" = apex + any subdomain). EMPTY ⇒ cloud emits NO
	// CORS headers. Reads CLOUD_CORS_ORIGINS, then GATEWAY_CORS_ORIGINS.
	CORSOrigins []string

	// EdgeRateEnabled turns on the per-client-IP edge flood cap that runs BEFORE
	// identity (CLOUD_EDGE_RATELIMIT, default true). EdgeRatePerIP requests per
	// EdgeRateWindowSec seconds are allowed per client IP (leftmost
	// X-Forwarded-For); a direct caller carries no XFF and is exempt.
	EdgeRateEnabled   bool
	EdgeRatePerIP     int
	EdgeRateWindowSec int

	// HealthListenAddr is the health/metrics listener (default 127.0.0.1:9090).
	HealthListenAddr string

	// ReadBufferSize is the fasthttp per-conn request-read buffer for the HTTP
	// edge, in bytes. fasthttp caps total request-header size at this value and
	// returns 431 above it; the framework default of 4 KiB is too small once a
	// browser carries a few session cookies. Env GATEWAY_READ_BUFFER_SIZE,
	// default 32 KiB.
	ReadBufferSize int

	// BodyLimit is the maximum request body the HTTP edge accepts, in bytes. The
	// framework default of 4 MiB caps a long-context chat request, which carries
	// its whole prompt in the body. Env GATEWAY_BODY_LIMIT, default 16 MiB.
	BodyLimit int

	// AIDefaultModel is the served model a subsystem falls back to when a caller
	// names none (CLOUD_AI_DEFAULT_MODEL). Model routing is the gateway's job;
	// this is the ONLY cloud-side model default.
	AIDefaultModel string

	// ZAP RPC endpoints for subsystems that are NOT enabled in this process but
	// are still needed by an enabled subsystem. Empty means "no remote endpoint"
	// — the client falls back to the disabled stub, which fails closed with a
	// clear error. The transport is ZAP, never JSON.
	IAMZAPAddr  string
	KMSZAPAddr  string
	BaseZAPAddr string
	AIZAPAddr   string
	O11yZAPAddr string
	VFSZAPAddr  string
	MQZAPAddr   string
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
		ListenAddr: getenv("CLOUD_LISTEN", listenDefault()),
		// LOOPBACK BY DEFAULT. The ZAP transport is plaintext TCP serving the
		// IDENTICAL route surface as HTTP, so a bare ":9653" would put
		// /v1/functions/{name}/invoke — process execution — on the LAN with no
		// credential. The default is the one that must be safe; set these
		// explicitly to listen anywhere else.
		ZAPListenAddr:    getenv("CLOUD_ZAP_LISTEN", "127.0.0.1:9653"),
		HealthListenAddr: getenv("CLOUD_HEALTH_LISTEN", "127.0.0.1:9090"),
		ReadBufferSize:   getenvInt("GATEWAY_READ_BUFFER_SIZE", 32768),
		BodyLimit:        getenvInt("GATEWAY_BODY_LIMIT", 16<<20),

		MarkdownDefaultPrefixes: splitTrim(getenv("CLOUD_MARKDOWN_DEFAULT_PREFIXES", "")),
		Brand:                   getenv("CLOUD_BRAND", DefaultBrand),
		Version:                 getenv("CLOUD_VERSION", Version),
		Env:                     getenv("CLOUD_ENV", ""),

		Domain:          getenv("CLOUD_DOMAIN", "localhost:8080"),
		IAMIssuer:       getenv("CLOUD_IAM_ISSUER", DefaultIAMIssuer),
		AdminOrg:        getenv("IAM_ADMIN_ORG", "admin"),
		JWKSURL:         getenv("CLOUD_JWKS_URL", ""),
		KMSMasterKeyRef: getenv("CLOUD_KMS_MASTER_KEY_REF", ""),
		KMSMPCAddr:      getenv("CLOUD_KMS_MPC_ADDR", ""),
		KMSMPCVaultID:   getenv("CLOUD_KMS_MPC_VAULT_ID", ""),
		DataDir:         getenv("CLOUD_DATA_DIR", "/var/lib/cloud"),
		AIDefaultModel:  getenv("CLOUD_AI_DEFAULT_MODEL", "deepseek-v4-flash"),
		IAMZAPAddr:      getenv("CLOUD_IAM_ZAP_ADDR", ""),
		KMSZAPAddr:      getenv("CLOUD_KMS_ZAP_ADDR", ""),
		BaseZAPAddr:     getenv("CLOUD_BASE_ZAP_ADDR", ""),
		AIZAPAddr:       getenv("CLOUD_AI_ZAP_ADDR", ""),
		O11yZAPAddr:     getenv("CLOUD_O11Y_ZAP_ADDR", ""),
		VFSZAPAddr:      getenv("CLOUD_VFS_ZAP_ADDR", ""),
		MQZAPAddr:       getenv("CLOUD_MQ_ZAP_ADDR", ""),
	}

	enableCSV := getenv("CLOUD_ENABLE", "")
	// Bind the CLI overrides once (see flagsOnce). A later call keeps its env-derived
	// cfg unchanged — tests set env via t.Setenv, never argv — so guarding the
	// registration loses nothing while making LoadConfig re-entrant.
	flagsOnce.Do(func() {
		flag.StringVar(&enableCSV, "enable", enableCSV, "comma-separated subsystem list (empty=all)")
		flag.StringVar(&cfg.Brand, "brand", cfg.Brand, "white-label brand")
		flag.StringVar(&cfg.Domain, "domain", cfg.Domain, "primary domain")
		flag.StringVar(&cfg.IAMIssuer, "iam-issuer", cfg.IAMIssuer, "JWKS issuer")
		flag.StringVar(&cfg.KMSMasterKeyRef, "kms-master-key-ref", cfg.KMSMasterKeyRef, "KMS master key reference")
		flag.StringVar(&cfg.DataDir, "data-dir", cfg.DataDir, "data root")
		flag.StringVar(&cfg.ListenAddr, "listen", cfg.ListenAddr, "HTTP listener")
		flag.Parse()
	})

	if enableCSV != "" {
		for _, name := range strings.Split(enableCSV, ",") {
			if s := strings.TrimSpace(name); s != "" {
				cfg.Enable = append(cfg.Enable, s)
			}
		}
	}

	// Additive staged activation (orthogonal to the Enable allowlist): turns a
	// staged subsystem (e.g. functions) on WITHOUT converting Enable into a strict
	// allowlist. Only staged names are honored — a non-staged name here is ignored
	// (it is already governed by the Enable default), so this can never widen the
	// surface beyond the staged set.
	for _, name := range strings.Split(getenv("CLOUD_ENABLE_STAGED", ""), ",") {
		if s := strings.TrimSpace(name); s != "" && stagedSubsystems[s] {
			cfg.EnableStaged = append(cfg.EnableStaged, s)
		}
	}

	// JWKS endpoint for the in-binary identity sanitizer. Default follows the
	// HIP-0111 convention {IAMIssuer}/v1/iam/.well-known/jwks; override with
	// CLOUD_JWKS_URL.
	// jwksURLFor is the ONE derivation, shared with NewTokenValidator so a
	// subsystem that verifies a token resolves the same keys this boundary does.
	if cfg.JWKSURL == "" {
		cfg.JWKSURL = jwksURLFor(cfg.IAMIssuer)
	}

	// Browser ZAP-over-WS Origin allowlist. The embedded console is same-origin;
	// localhost:4000 admits a console dev server. Override with
	// CLOUD_ZAP_WEB_ORIGINS.
	zapOrigins := getenv("CLOUD_ZAP_WEB_ORIGINS", "localhost:4000")
	for _, o := range strings.Split(zapOrigins, ",") {
		if s := strings.TrimSpace(o); s != "" {
			cfg.ZAPWebOrigins = append(cfg.ZAPWebOrigins, s)
		}
	}
	// Edge policy (middleware_edge.go). CORS default OFF; the per-IP flood cap
	// default ON at 100/1s so a protection is never dropped silently.
	cfg.CORSOrigins = splitTrim(getenv("CLOUD_CORS_ORIGINS", os.Getenv("GATEWAY_CORS_ORIGINS")))
	cfg.EdgeRateEnabled = getenvBoolDefault("CLOUD_EDGE_RATELIMIT", true)
	cfg.EdgeRatePerIP = getenvInt("CLOUD_EDGE_RATELIMIT_PER_IP", 100)
	cfg.EdgeRateWindowSec = getenvInt("CLOUD_EDGE_RATELIMIT_WINDOW_SEC", 1)
	return cfg
}

// stagedSubsystems require EXPLICIT enablement: they are deliberately NOT part of
// the empty-Enable "mount everything" default and mount ONLY when named in
// CLOUD_ENABLE or CLOUD_ENABLE_STAGED (HIP-0106 staged rollout, enforced in code).
//
// "functions" RUNS CODE: its invoke op writes the stored function body to a temp
// file and executes it with node, python3 or bash. Staging it means the
// capability exists in every build but has to be named before it can be
// reached, so mounting the binary somewhere public does not silently expose an
// execution endpoint. `make dev` names it.
//
// "ingress" starts edge listeners, which a default run should not open.
var stagedSubsystems = map[string]bool{"ingress": true, "functions": true}

// Enabled reports whether subsystem `name` is enabled in this config.
// Empty Enable list = all subsystems enabled, EXCEPT staged subsystems
// (stagedSubsystems). A staged subsystem mounts only when named explicitly — in
// Enable, or (the additive, allowlist-preserving path) in EnableStaged.
func (c *Config) Enabled(name string) bool {
	// Staged subsystems are opt-in via either lever, independent of the Enable
	// default so "all + iam" needs no hand-enumerated allowlist.
	if stagedSubsystems[name] {
		return contains(c.EnableStaged, name) || contains(c.Enable, name)
	}
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

// listenDefault is the API listen address when CLOUD_LISTEN says nothing: PORT
// if the environment set one, else :8080.
//
// PORT is not a second knob for the same fact — CLOUD_LISTEN remains the one way
// to say where cloud listens, and it wins whenever it is set. PORT is read only
// as the fallback, because it is the first thing everyone tries and because the
// platforms that run a container for you (and every `PORT=3000 ./cloud` reflex)
// state the port that way and nothing else. Silently ignoring it meant the
// process came up on :8080 while the operator was watching another port and
// concluded the binary had hung.
//
// A bare number becomes ":3000". Anything else is passed through untouched, so
// PORT=127.0.0.1:3000 binds loopback and a malformed value fails loudly at the
// listener instead of being quietly rewritten into something that works.
func listenDefault() string {
	port := strings.TrimSpace(os.Getenv("PORT"))
	if port == "" {
		return ":8080"
	}
	if strings.IndexFunc(port, func(r rune) bool { return r < '0' || r > '9' }) < 0 {
		return ":" + port
	}
	return port
}

func getenv(key, dflt string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return dflt
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
// (true/1/yes, case-insensitive).
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
	return nil
}
