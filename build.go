package cloud

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/hanzoai/cloud/clients/commerce/metering"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/clients"
	"github.com/hanzoai/cloud/clients/gatewaypolicy"
	"github.com/hanzoai/cloud/clients/s3admin"
)

// BuildDeps constructs the Deps used by every subsystem's Mount(app, deps).
//
// Wiring rules per HIP-0106 inter-subsystem contract:
//
//  1. If the subsystem is enabled in this process, the Client field is
//     left nil here. The subsystem's own Mount() will install a typed
//     in-process Client into Deps via the SetClient helpers exposed by
//     this package. (Subsystem Mounts run after BuildDeps; they have
//     full access to construct their concrete implementation, and the
//     resulting object goes back into Deps for everyone else to call.)
//
//  2. If the subsystem is disabled but cfg has a non-empty ZAP RPC
//     endpoint for it, the Client field gets a ZAP-RPC stub targeting
//     that endpoint. Subsystem code calls deps.X.Foo(...) without
//     knowing the call goes over the wire.
//
//  3. If the subsystem is disabled AND there is no endpoint, the Client
//     field gets a "disabled" stub that fails closed with a clear
//     error. Mount-time consumers detect this with
//     clients.IsDisabled(err) and log a friendly "dep X needed by Y
//     not configured" message.
//
// JSON does not appear in any of these paths. Inter-subsystem calls
// are ZAP-typed Go values either via direct method dispatch (mode 1)
// or via ZAP RPC over the wire (mode 2). JSON happens only at the
// gateway/ingress edge, through the zip jsonenc helper.
//
// Payments and Vault are special: they are NEVER in-process per
// HIP-0106 solo-vault CDE. Their clients always resolve via
// clients.PaymentsRPCAt / clients.VaultRPCAt; the disabled stub fires
// when no endpoint is configured.
func BuildDeps(cfg *Config) Deps {
	logger := luxlog.New("cloud")
	logger.Info(
		"building deps",
		"brand", cfg.Brand,
		"domain", cfg.Domain,
		"iam_issuer", cfg.IAMIssuer,
		"data_dir", cfg.DataDir,
		"enabled", cfg.Enable,
	)

	deps := Deps{
		Logger:         logger,
		Brand:          cfg.Brand,
		Env:            cfg.Env,
		Domain:         cfg.Domain,
		IAMIssuer:      cfg.IAMIssuer,
		DataDir:        cfg.DataDir,
		AIDefaultModel: cfg.AIDefaultModel,
	}

	// For each subsystem: enabled → leave nil (Mount fills it); not
	// enabled + endpoint → RPC client; not enabled + no endpoint →
	// disabled stub. The plain co-resident-or-RPC-or-disabled clients share
	// ONE resolver (pick); KMS/AI/VFS keep bespoke pickers because their
	// construction genuinely differs (embedded store / gateway preference /
	// S3-admin backend). O11y's disabled stub is a no-op (telemetry going
	// nowhere is normal), not fail-closed.
	deps.IAM = pick(cfg, logger, "iam", "IAM", cfg.IAMZAPAddr, clients.IAMRPCAt, clients.DisabledIAM)
	deps.KMS = pickKMSClient(cfg, logger)
	deps.Base = pick(cfg, logger, "base", "Base", cfg.BaseZAPAddr, clients.BaseRPCAt, clients.DisabledBase)
	deps.Commerce = pickCommerceClient(cfg, logger)
	deps.AI = pickAIClient(cfg, logger)
	deps.O11y = pick(cfg, logger, "o11y", "O11y", cfg.O11yZAPAddr, clients.O11yRPCAt, clients.DisabledO11y)
	deps.VFS = pickVFSClient(cfg, logger)
	deps.MQ = pick(cfg, logger, "mq", "MQ", cfg.MQZAPAddr, clients.MQRPCAt, clients.DisabledMQ)

	// Payments and Vault never co-resident. Disabled stub when no
	// endpoint, otherwise RPC.
	deps.Payments = pickPaymentsClient(cfg, logger)
	deps.Vault = pickVaultClient(cfg, logger)

	// Billing metering client for the request-edge gate. nil-safe: when no
	// commerce URL is configured the resulting client is !Enabled() and the
	// gate is a no-op.
	deps.Metering = buildMeteringClient(cfg, logger)

	// Runtime-mutable edge-policy store (/v1/gateway config plane), layered over
	// the static env/flag defaults so an un-provisioned deployment behaves exactly
	// as the static config until an operator PUTs an override. New always returns a
	// working *Store (static-only if the SQLite file can't open), so the edge
	// middleware is never left without a policy source — a store-open error is
	// logged, not fatal.
	gp, err := gatewaypolicy.New(cfg.DataDir, cfg.AdminOrg, staticEdgePolicy(cfg))
	if err != nil {
		logger.Warn("gateway policy store degraded to static-only", "err", err)
	}
	deps.GatewayPolicy = gp

	return deps
}

// staticEdgePolicy projects the static env/flag edge config into the boot-default
// policy the gatewaypolicy.Store layers runtime overrides on top of. A disabled
// per-IP limiter (CLOUD_EDGE_RATELIMIT=false) maps to PerIPRPM 0 (a live no-op).
func staticEdgePolicy(cfg *Config) gatewaypolicy.Policy {
	p := gatewaypolicy.Policy{
		CORSOrigins: cfg.CORSOrigins,
		WindowSec:   cfg.EdgeRateWindowSec,
	}
	if cfg.EdgeRateEnabled {
		p.PerIPRPM = cfg.EdgeRatePerIP
	}
	return p
}

// buildMeteringClient constructs the commerce metering client for BillingGate.
// An empty CommerceHTTPURL yields a not-Enabled() client (allow + no-op),
// matching the metering package's "not configured" mode, so an unconfigured
// deployment is never blocked. The token is a KMS-sourced secret supplied via
// config; it is never logged.
func buildMeteringClient(cfg *Config, log luxlog.Logger) *metering.Client {
	m, err := metering.New(metering.Config{
		BaseURL:  cfg.CommerceHTTPURL,
		Token:    cfg.CommerceServiceToken,
		Org:      cfg.Brand, // X-Org-Id default for S2S; per-request org overrides.
		FailOpen: cfg.BillingFailOpen,
	})
	if err != nil {
		// Only an unparseable URL reaches here. Fall back to a not-configured
		// client (no-op gate) rather than failing boot over billing wiring.
		log.Error("billing: invalid commerce URL, gate disabled", "err", err)
		m, _ = metering.New(metering.Config{})
	}
	if m.Enabled() {
		log.Info("billing gate enabled", "commerce_url", cfg.CommerceHTTPURL, "fail_open", cfg.BillingFailOpen)
	} else {
		log.Info("billing gate disabled (no commerce URL)")
	}
	return m
}

// pick resolves one inter-subsystem client under the HIP-0106 wiring rule shared
// by every co-resident-capable dependency: enabled in THIS process → zero value
// (nil) so the subsystem's own Mount installs the in-process client; not enabled
// but a ZAP endpoint is configured → an RPC client at that endpoint; neither →
// the fail-closed/no-op disabled stub. name is the enable-list id; label is the
// deps.<X> log tag; rpc/disabled are the client's typed constructors. This is the
// ONE implementation of that rule — KMS/AI/VFS opt out with bespoke pickers only
// because their construction genuinely differs.
func pick[T any](cfg *Config, log luxlog.Logger, name, label, zapAddr string, rpc func(string) T, disabled func() T) T {
	if cfg.Enabled(name) {
		var zero T // enabled here → Mount fills deps.<label>
		return zero
	}
	if zapAddr != "" {
		log.Info("deps."+label+" → ZAP RPC", "addr", zapAddr)
		return rpc(zapAddr)
	}
	return disabled()
}

// pickKMSClient resolves deps.KMS. When the kms subsystem is co-resident
// (Enabled("kms")) it returns the IN-PROCESS Client backed by the embedded
// luxfi/kms SecretStore under CLOUD_DATA_DIR — no external RPC. A store-open
// failure is NOT fatal to the whole binary: it falls back to the disabled stub
// (fail-closed) and logs, so a bad data dir degrades KMS rather than crashing
// every subsystem. Absent co-residency the legacy ZAP-RPC + disabled fallbacks
// apply (out-of-process KMS, or not wired).
//
// The subsystem id is "kms" (clients/kms registers it with cloud.HealthOwner so
// the generic liveness route never shadows its real /v1/kms/health, and registers
// the client factory this gate calls); this gate keys on the same id so "enabled"
// is one concept.
func pickKMSClient(cfg *Config, log luxlog.Logger) KMSClient {
	if cfg.Enabled("kms") {
		// The embedded-client constructor is registered by clients/kms in init()
		// (RegisterKMSClientFactory). cloud never imports clients/kms, so the KMS
		// library and its /v1/kms subsystem live in one package with no cloud⇄kms
		// import cycle. Absent the registration (clients/kms not linked into this
		// binary) KMS fails closed rather than pretending to host secrets.
		if kmsClientFactory == nil {
			log.Error("deps.KMS: kms enabled but no client factory registered (clients/kms not linked); failing closed")
			return clients.DisabledKMS()
		}
		c, err := kmsClientFactory(cfg, log)
		if err != nil {
			log.Error("deps.KMS: embedded KMS unavailable, failing closed", "err", err)
			return clients.DisabledKMS()
		}
		return c
	}
	if cfg.KMSZAPAddr != "" {
		log.Info("deps.KMS → ZAP RPC", "addr", cfg.KMSZAPAddr)
		return clients.KMSRPCAt(cfg.KMSZAPAddr)
	}
	return clients.DisabledKMS()
}

// kmsClientFactory constructs the embedded in-process KMS client from cloud
// Config. clients/kms registers it in init(); pickKMSClient calls it so cloud
// depends on the KMSClient interface + this hook, never the concrete kms package
// — the same inversion the subsystem Registry already uses (cloud mounts every
// subsystem it never imports). Exactly one registration.
var kmsClientFactory func(cfg *Config, log luxlog.Logger) (KMSClient, error)

// RegisterKMSClientFactory installs the embedded-KMS constructor. clients/kms
// calls this from its init(); it is the ONE inversion point that lets the KMS
// library and its /v1/kms subsystem share one package with no cloud⇄kms cycle.
func RegisterKMSClientFactory(f func(cfg *Config, log luxlog.Logger) (KMSClient, error)) {
	kmsClientFactory = f
}

// ---- git-push-to-deploy ----

// GitPushEvent describes a push that just landed on the embedded git server: the
// tenant, the repo, the branch that moved, and its new tip commit. CloneURL is the
// canonical clone URL of that repo (https://<host>/v1/git/<org>/<repo>.git) — the
// exact value an Application's RepoURL carries — so the builder can resolve which
// app (if any) tracks this branch and needs a rebuild.
type GitPushEvent struct {
	Org      string
	Project  string
	Repo     string
	Branch   string
	Commit   string
	CloneURL string
}

// pushBuilder is the registered git-push-to-deploy trigger. clients/platform
// installs it in Mount; clients/git calls OnGitPush after a push lands. The
// inversion keeps git⇄platform decoupled — git never imports platform — exactly
// like kmsClientFactory and the subsystem Registry. Exactly one registration.
var pushBuilder func(ctx context.Context, ev GitPushEvent) error

// RegisterPushBuilder installs the git-push-to-deploy trigger. clients/platform
// calls this from its Mount when co-resident; it is the ONE inversion point that
// lets the embedded git server launch a platform build with no git⇄platform cycle.
func RegisterPushBuilder(f func(ctx context.Context, ev GitPushEvent) error) {
	pushBuilder = f
}

// OnGitPush fires the registered push-to-deploy trigger for a landed push. It is a
// no-op when no builder is registered (git server running without the platform
// subsystem co-resident). Best-effort by contract: the caller must never fail the
// push the client already committed just because a build could not be triggered.
func OnGitPush(ctx context.Context, ev GitPushEvent) error {
	if pushBuilder == nil {
		return nil
	}
	return pushBuilder(ctx, ev)
}

// pickCommerceClient resolves deps.Commerce — the typed inter-subsystem client the
// entitlements/licensing tier calls (GetTenantConfig, CheckEntitlement). When the
// commerce subsystem is co-resident (Enabled("commerce"), clients/commercesvc
// mounted) it returns the IN-PROCESS client via CommerceInProcess: a direct Go
// method call, no network hop — the HIP-0106 co-resident default. Absent co-residency
// the legacy ZAP-RPC + disabled fallbacks apply (out-of-process commerce, or not
// wired), so the remote proxy seam (CLOUD_COMMERCE_ZAP_ADDR) is unchanged. The
// in-process client is trivial (org + brand for tenant config; entitlement resolution
// fail-closed pending commerce exporting it) so it lives in clients directly — no
// factory inversion is needed, unlike KMS whose store construction lives in
// clients/kms.
func pickCommerceClient(cfg *Config, log luxlog.Logger) CommerceClient {
	if cfg.Enabled("commerce") {
		log.Info("deps.Commerce → in-process (embedded commerce)", "brand", cfg.Brand)
		return clients.CommerceInProcess(clients.LocalCommerce(cfg.Brand))
	}
	if cfg.CommerceZAPAddr != "" {
		log.Info("deps.Commerce → ZAP RPC", "addr", cfg.CommerceZAPAddr)
		return clients.CommerceRPCAt(cfg.CommerceZAPAddr)
	}
	return clients.DisabledCommerce()
}

// pickAIClient resolves deps.AI — the client the agents subsystem runs chat
// completions through. Unlike the co-resident subsystems, there is NO in-process
// "ai" mount that fills a nil deps.AI: inference is an external gateway, so this
// must return a concrete client, never nil. (A nil deps.AI was the live bug —
// the default all-enabled config returned nil here and nothing ever filled it,
// so every /v1/agents/:name/run 503'd "inference is not configured".)
//
// Preference order:
//  1. Static-key HTTP gateway when a base URL AND a static key are configured —
//     an operator override / pre-provisioned key. The key is a KMS-injected
//     secret; only the base URL and default model are ever logged.
//  2. M2M HTTP gateway when a base URL AND the binary's IAM identity are present
//     (the durable Hanzo default): the client mints+refreshes a client-
//     credentials token from IAM_CLIENT_ID/SECRET — no static key to rotate. The
//     secret is never logged.
//  3. ZAP RPC when an addr is configured (split-deploy of a future ai subsystem).
//  4. Fail-closed stub otherwise — a run records an honest error, never fakes one.
func pickAIClient(cfg *Config, log luxlog.Logger) AIClient {
	if cfg.AIBaseURL != "" && cfg.AIAPIKey != "" {
		log.Info("deps.AI → HTTP gateway (static key)", "base_url", cfg.AIBaseURL, "default_model", cfg.AIDefaultModel)
		return clients.AIHTTPAt(cfg.AIBaseURL, cfg.AIAPIKey, cfg.AIDefaultModel)
	}
	if cfg.AIBaseURL != "" && cfg.AIAuthClientID != "" && cfg.AIAuthClientSecret != "" {
		tokenURL := aiM2MTokenURL(cfg)
		if tokenURL != "" {
			log.Info("deps.AI → HTTP gateway (IAM M2M)", "base_url", cfg.AIBaseURL,
				"token_url", tokenURL, "client_id", cfg.AIAuthClientID, "default_model", cfg.AIDefaultModel)
			return clients.AIHTTPM2M(cfg.AIBaseURL, tokenURL, cfg.AIAuthClientID, cfg.AIAuthClientSecret, cfg.AIDefaultModel)
		}
	}
	if cfg.AIZAPAddr != "" {
		log.Info("deps.AI → ZAP RPC", "addr", cfg.AIZAPAddr)
		return clients.AIRPCAt(cfg.AIZAPAddr)
	}
	log.Info("deps.AI → disabled (no CLOUD_AI_API_KEY, no IAM M2M identity, no gateway configured)")
	return clients.DisabledAI()
}

// aiM2MTokenURL resolves IAM's client_credentials endpoint the agent runner mints
// its M2M inference token at. It MUST be reachable FROM INSIDE THE CLUSTER: the
// runner runs in-cluster and the public issuer host (https://hanzo.id) is fronted
// by Cloudflare, which 403s a server-side (non-browser) loopback POST with edge
// error 1006 — so minting against the PUBLIC issuer URL fails and every
// POST /v1/agents/:ref/run 502s (root-caused 2026-07-04: in-cluster POST to
// https://hanzo.id/v1/iam/oauth/token → 403/1006, while http://iam.hanzo.svc/... → 200).
// This mirrors the KMS login-broker resolution (clients/kms) exactly — one
// split-horizon policy, no drift. Prefer, in order: an explicit override
// (CLOUD_AI_IAM_TOKEN_URL), the in-cluster IAM service base (IAM_URL — already
// wired to http://iam.hanzo.svc for JWKS), then the public issuer as a last resort
// (single-process / no split-horizon deploys). Returns "" only when no identity is
// resolvable, which keeps the M2M branch off (caller falls through to the stub).
func aiM2MTokenURL(cfg *Config) string {
	if override := strings.TrimSpace(os.Getenv("CLOUD_AI_IAM_TOKEN_URL")); override != "" {
		return override
	}
	if base := strings.TrimRight(strings.TrimSpace(os.Getenv("IAM_URL")), "/"); base != "" {
		return base + "/v1/iam/oauth/token"
	}
	if iss := strings.TrimRight(strings.TrimSpace(cfg.IAMIssuer), "/"); iss != "" {
		return iss + "/v1/iam/oauth/token"
	}
	return ""
}

func pickVFSClient(cfg *Config, log luxlog.Logger) VFSClient {
	// deps.VFS must NEVER be nil (R-7): files.go and any other VFS consumer call
	// s.vfs.Put/Get/Delete unconditionally, so a nil here is a per-request 500
	// (dishonest degradation) instead of a fail-closed 502. Unlike the
	// nil-then-Mount-fills convention other subsystems use, nothing fills deps.VFS
	// after MountAll (Mount receives deps by value), so we ALWAYS hand back a
	// concrete client.
	if cfg.VFSZAPAddr != "" {
		log.Info("deps.VFS → ZAP RPC", "addr", cfg.VFSZAPAddr)
		return clients.VFSRPCAt(cfg.VFSZAPAddr)
	}
	// Real blob backend (.97): the shared SeaweedFS S3 gateway — the canonical,
	// key-based object store, reached with the SAME S3_ADMIN_* admin identity
	// clients/s3 uses (s3admin, one construction). Present only when those creds
	// are injected; a construction failure degrades to fail-closed rather than a
	// nil deref. Team blobs (avatars/attachments) round-trip through this to the
	// team-blobs bucket, org-scoped by the caller-built key prefix.
	if admin := s3admin.New(); admin.Configured() {
		v, err := clients.NewS3VFS(admin)
		if err != nil {
			log.Error("deps.VFS → S3 construction failed; falling back to fail-closed", "err", err)
			return clients.DisabledVFS()
		}
		log.Info("deps.VFS → SeaweedFS S3", "bucket", clients.TeamBlobBucket)
		return v
	}
	// No VFS endpoint and no S3 admin creds → fail-closed stub (R-7): Put/Get/Delete
	// return a non-nil error → files answer 502, never a nil-deref 500.
	return clients.DisabledVFS()
}

func pickPaymentsClient(cfg *Config, log luxlog.Logger) PaymentsClient {
	if cfg.PaymentsZAPAddr != "" {
		log.Info("deps.Payments → ZAP RPC", "addr", cfg.PaymentsZAPAddr)
		return clients.PaymentsRPCAt(cfg.PaymentsZAPAddr)
	}
	return clients.DisabledPayments()
}

func pickVaultClient(cfg *Config, log luxlog.Logger) VaultClient {
	if cfg.VaultZAPAddr != "" {
		log.Info("deps.Vault → ZAP RPC", "addr", cfg.VaultZAPAddr)
		return clients.VaultRPCAt(cfg.VaultZAPAddr)
	}
	return clients.DisabledVault()
}

// MountFunc is the registry's mount contract. The registry stores one function
// type, and the external subsystem modules (hanzoai/ai, authz, base, commerce,
// metrics, o11y, licensing) register their mount with `app any` — so `any` is
// the load-bearing wire type here, not a shortcut: retyping it to *zip.App would
// break those pinned modules at compile time (a func(any,…) literal is not
// assignable to a func(*zip.App,…) parameter). The concrete value is always a
// *zip.App; in-repo subsystems recover it via Typed instead of hand-writing the
// assertion (see Typed).
type MountFunc func(app any, deps Deps) error

// Typed adapts a strongly-typed subsystem Mount — func(*zip.App, Deps) error,
// the signature every in-repo subsystem already exports — into the registry's
// MountFunc. It performs the *zip.App recovery in ONE place, fail-closed with a
// clear error, so no subsystem repeats the `a, ok := app.(*zip.App)` boilerplate.
// The concrete value MountAll passes is always a *zip.App, so the assertion is
// total in practice; it stays as a defensive, self-documenting guard.
func Typed(mount func(*zip.App, Deps) error) MountFunc {
	return func(app any, deps Deps) error {
		a, ok := app.(*zip.App)
		if !ok {
			return fmt.Errorf("cloud.Mount: app is %T, want *zip.App", app)
		}
		return mount(a, deps)
	}
}

// ShutdownFunc releases a subsystem's process-lifetime resources (background
// goroutines, open DB handles) on graceful shutdown. It must be idempotent and
// bounded — Serve calls it within the shutdown deadline. ctx carries that
// deadline so a slow teardown is cut off rather than hanging SIGTERM.
type ShutdownFunc func(ctx context.Context) error

// MountSpec describes one subsystem registered for mounting. The Order
// is used when ordering matters for inter-subsystem deps (e.g. iam
// before authz before commerce).
type MountSpec struct {
	Name     string
	Order    int
	Mount    MountFunc
	Shutdown ShutdownFunc // optional; nil means the subsystem has nothing to tear down.

	// OwnsHealth marks a subsystem that serves its OWN GET /v1/<name>/health
	// (a real, fail-closed probe). Serve's generic liveness loop skips these so
	// its always-ok route never shadows the subsystem's real probe. Set via the
	// HealthOwner option at registration.
	OwnsHealth bool
}

// Option customizes a MountSpec at registration. It keeps Register's common
// path a two-liner while letting a subsystem opt into behavior (e.g. HealthOwner)
// without a wider signature — one registration entry point, extended by options.
type Option func(*MountSpec)

// HealthOwner declares that the subsystem serves its own /v1/<name>/health, so
// Serve's generic liveness route must not shadow it. This replaces the old
// "<name>svc" id kludge (which parked the generic route at an unrouted path):
// the id is now the clean route name and the health policy is an explicit flag.
func HealthOwner(s *MountSpec) { s.OwnsHealth = true }

// Registry is the in-process subsystem registry. Subsystems register via
// init() functions in their respective packages OR cmd/cloud/main.go can
// explicitly enumerate them. Either pattern works.
var Registry []MountSpec

// Register adds a subsystem to the in-process registry. Trailing opts customize
// the spec (e.g. cloud.HealthOwner for a subsystem that serves its own health).
func Register(name string, order int, mount MountFunc, opts ...Option) {
	spec := MountSpec{Name: name, Order: order, Mount: mount}
	for _, opt := range opts {
		opt(&spec)
	}
	Registry = append(Registry, spec)
}

// RegisterWithShutdown adds a subsystem that owns process-lifetime resources: a
// background worker (e.g. the agents scheduler) or a DB handle that must be
// flushed. shutdown is invoked by ShutdownAll on graceful stop. This is the ONE
// way a subsystem gets a teardown — Register stays the zero-teardown default.
// Trailing opts customize the spec, exactly as for Register.
func RegisterWithShutdown(name string, order int, mount MountFunc, shutdown ShutdownFunc, opts ...Option) {
	spec := MountSpec{Name: name, Order: order, Mount: mount, Shutdown: shutdown}
	for _, opt := range opts {
		opt(&spec)
	}
	Registry = append(Registry, spec)
}

// ShutdownAll tears down every ENABLED subsystem that registered a ShutdownFunc,
// in REVERSE mount order (a dependency is torn down after its dependents), best
// effort: a failure is collected and the rest still run, so one stuck subsystem
// can't strand another's flush. Serve calls this inside the shutdown deadline.
func ShutdownAll(ctx context.Context, cfg *Config) error {
	var firstErr error
	for i := len(Registry) - 1; i >= 0; i-- {
		spec := Registry[i]
		if spec.Shutdown == nil || !cfg.Enabled(spec.Name) {
			continue
		}
		if err := spec.Shutdown(ctx); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("shutdown %s: %w", spec.Name, err)
		}
	}
	return firstErr
}

// MountAll iterates the registry in order and calls Mount() on each
// enabled subsystem. app is the concrete *zip.App from Serve; the registry's
// MountFunc accepts it as `any` and in-repo subsystems recover it via Typed.
func MountAll(app *zip.App, cfg *Config, deps Deps) error {
	// Sort registry by order — bubble sort, registry is tiny.
	for i := 0; i < len(Registry); i++ {
		for j := i + 1; j < len(Registry); j++ {
			if Registry[j].Order < Registry[i].Order {
				Registry[i], Registry[j] = Registry[j], Registry[i]
			}
		}
	}

	logger := deps.Logger
	for _, spec := range Registry {
		if !cfg.Enabled(spec.Name) {
			logger.Debug("subsystem disabled", "name", spec.Name)
			continue
		}
		if err := spec.Mount(app, deps); err != nil {
			return fmt.Errorf("mount %s: %w", spec.Name, err)
		}
		logger.Info("mounted subsystem", "name", spec.Name)
	}
	return nil
}
