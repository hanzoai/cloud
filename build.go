package cloud

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/hanzoai/commerce/metering"
	luxlog "github.com/luxfi/log"

	"github.com/hanzoai/cloud/clients"
	"github.com/hanzoai/cloud/clients/kms"
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
	logger.Info("building deps",
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
	// disabled stub.
	deps.IAM = pickIAMClient(cfg, logger)
	deps.KMS = pickKMSClient(cfg, logger)
	deps.Base = pickBaseClient(cfg, logger)
	deps.Commerce = pickCommerceClient(cfg, logger)
	deps.AI = pickAIClient(cfg, logger)
	deps.O11y = pickO11yClient(cfg, logger)
	deps.VFS = pickVFSClient(cfg, logger)
	deps.MQ = pickMQClient(cfg, logger)

	// Payments and Vault never co-resident. Disabled stub when no
	// endpoint, otherwise RPC.
	deps.Payments = pickPaymentsClient(cfg, logger)
	deps.Vault = pickVaultClient(cfg, logger)

	// Billing metering client for the request-edge gate. nil-safe: when no
	// commerce URL is configured the resulting client is !Enabled() and the
	// gate is a no-op.
	deps.Metering = buildMeteringClient(cfg, logger)

	return deps
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

// pickIAMClient returns the canonical IAMClient for this process.
// nil = enabled here, Mount will fill it. RPC = remote endpoint
// configured. Disabled = not enabled, no endpoint.
func pickIAMClient(cfg *Config, log luxlog.Logger) IAMClient {
	if cfg.Enabled("iam") {
		return nil
	}
	if cfg.IAMZAPAddr != "" {
		log.Info("deps.IAM → ZAP RPC", "addr", cfg.IAMZAPAddr)
		return clients.IAMRPCAt(cfg.IAMZAPAddr)
	}
	return clients.DisabledIAM()
}

// pickKMSClient resolves deps.KMS. When the kms subsystem is co-resident
// (Enabled("kmssvc")) it returns the IN-PROCESS Client backed by the embedded
// luxfi/kms SecretStore under CLOUD_DATA_DIR — no external RPC. A store-open
// failure is NOT fatal to the whole binary: it falls back to the disabled stub
// (fail-closed) and logs, so a bad data dir degrades KMS rather than crashing
// every subsystem. Absent co-residency the legacy ZAP-RPC + disabled fallbacks
// apply (out-of-process KMS, or not wired).
//
// The internal subsystem name is "kmssvc" (see clients/kmssvc.init — it avoids the
// serve.go generic-health shadow on /v1/kms/health); the client gate keys on the
// same name so "enabled" is one concept.
func pickKMSClient(cfg *Config, log luxlog.Logger) KMSClient {
	if cfg.Enabled("kmssvc") {
		c, err := kms.New(kms.Config{
			DataDir:      cfg.DataDir,
			MasterKeyB64: cfg.KMSMasterKeyRef,
			MPCAddr:      cfg.KMSMPCAddr,
			MPCVaultID:   cfg.KMSMPCVaultID,
		}, log)
		if err != nil {
			log.Error("deps.KMS: embedded KMS unavailable, failing closed", "err", err)
			return clients.DisabledKMS()
		}
		log.Info("deps.KMS → in-process (embedded luxfi/kms)", "ready", c.Ready(), "signing", c.SigningConfigured())
		return c
	}
	if cfg.KMSZAPAddr != "" {
		log.Info("deps.KMS → ZAP RPC", "addr", cfg.KMSZAPAddr)
		return clients.KMSRPCAt(cfg.KMSZAPAddr)
	}
	return clients.DisabledKMS()
}

func pickBaseClient(cfg *Config, log luxlog.Logger) BaseClient {
	if cfg.Enabled("base") {
		return nil
	}
	if cfg.BaseZAPAddr != "" {
		log.Info("deps.Base → ZAP RPC", "addr", cfg.BaseZAPAddr)
		return clients.BaseRPCAt(cfg.BaseZAPAddr)
	}
	return clients.DisabledBase()
}

func pickCommerceClient(cfg *Config, log luxlog.Logger) CommerceClient {
	if cfg.Enabled("commerce") {
		return nil
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
// This mirrors the KMS login-broker resolution (clients/kmssvc) exactly — one
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

func pickO11yClient(cfg *Config, log luxlog.Logger) O11yClient {
	if cfg.Enabled("o11y") {
		return nil
	}
	if cfg.O11yZAPAddr != "" {
		log.Info("deps.O11y → ZAP RPC", "addr", cfg.O11yZAPAddr)
		return clients.O11yRPCAt(cfg.O11yZAPAddr)
	}
	// O11y disabled-stub is no-op (not fail-closed) — telemetry
	// going nowhere is a normal mode.
	return clients.DisabledO11y()
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

func pickMQClient(cfg *Config, log luxlog.Logger) MQClient {
	if cfg.Enabled("mq") {
		return nil
	}
	if cfg.MQZAPAddr != "" {
		log.Info("deps.MQ → ZAP RPC", "addr", cfg.MQZAPAddr)
		return clients.MQRPCAt(cfg.MQZAPAddr)
	}
	return clients.DisabledMQ()
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

// MountFunc is the canonical signature every subsystem exposes per
// HIP-0106. Each Hanzo Go service ships a top-level `Mount` symbol
// matching this signature; cmd/cloud/main.go imports the package and
// calls it.
type MountFunc func(app any, deps Deps) error // app is *zip.App; using any here to avoid an import cycle in pkg/cloud

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
}

// Registry is the in-process subsystem registry. Subsystems register via
// init() functions in their respective packages OR cmd/cloud/main.go can
// explicitly enumerate them. Either pattern works.
var Registry []MountSpec

// Register adds a subsystem to the in-process registry.
func Register(name string, order int, mount MountFunc) {
	Registry = append(Registry, MountSpec{Name: name, Order: order, Mount: mount})
}

// RegisterWithShutdown adds a subsystem that owns process-lifetime resources: a
// background worker (e.g. the agents scheduler) or a DB handle that must be
// flushed. shutdown is invoked by ShutdownAll on graceful stop. This is the ONE
// way a subsystem gets a teardown — Register stays the zero-teardown default.
func RegisterWithShutdown(name string, order int, mount MountFunc, shutdown ShutdownFunc) {
	Registry = append(Registry, MountSpec{Name: name, Order: order, Mount: mount, Shutdown: shutdown})
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
// enabled subsystem.
func MountAll(app any, cfg *Config, deps Deps) error {
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
