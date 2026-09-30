package cloud

import (
	"context"
	"fmt"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/clients"
	"github.com/hanzoai/cloud/clients/gateway/edge"
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
		Version:        cfg.Version,
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
	// construction genuinely differs (embedded store / no co-resident gateway /
	// never nil). O11y's disabled stub is a no-op (telemetry going nowhere is
	// normal), not fail-closed.
	deps.IAM = pick(cfg, logger, "iam", "IAM", cfg.IAMZAPAddr, clients.IAMRPCAt, clients.DisabledIAM)
	deps.KMS = pickKMSClient(cfg, logger)
	deps.Base = pick(cfg, logger, "base", "Base", cfg.BaseZAPAddr, clients.BaseRPCAt, clients.DisabledBase)
	// AI (completions) and Embed (embeddings) resolve to their out-of-process ZAP
	// clients or the fail-closed stub: point them at a gateway via the ZAP
	// address, or they stay disabled (a run records an honest error, never fakes one).
	deps.AI = pickCompletionsClient(cfg, logger)
	deps.Embed = pickEmbedClient(cfg, logger)
	deps.O11y = pick(cfg, logger, "o11y", "O11y", cfg.O11yZAPAddr, clients.O11yRPCAt, clients.DisabledO11y)
	deps.VFS = pickVFSClient(cfg, logger)
	deps.MQ = pick(cfg, logger, "mq", "MQ", cfg.MQZAPAddr, clients.MQRPCAt, clients.DisabledMQ)

	// Runtime-mutable edge-policy store (/v1/gateway config plane), layered over
	// the static env/flag defaults so an un-provisioned deployment behaves exactly
	// as the static config until an operator PUTs an override. New always returns a
	// working *Store (static-only if the SQLite file can't open), so the edge
	// middleware is never left without a policy source — a store-open error is
	// logged, not fatal.
	gp, err := edge.New(cfg.DataDir, cfg.AdminOrg, staticEdgePolicy(cfg))
	if err != nil {
		logger.Warn("gateway policy store degraded to static-only", "err", err)
	}
	deps.GatewayPolicy = gp

	return deps
}

// staticEdgePolicy projects the static env/flag edge config into the boot-default
// policy the edge.Store layers runtime overrides on top of. A disabled
// per-IP limiter (CLOUD_EDGE_RATELIMIT=false) maps to PerIPRPM 0 (a live no-op).
func staticEdgePolicy(cfg *Config) edge.Policy {
	p := edge.Policy{
		CORSOrigins: cfg.CORSOrigins,
		WindowSec:   cfg.EdgeRateWindowSec,
	}
	if cfg.EdgeRateEnabled {
		p.PerIPRPM = cfg.EdgeRatePerIP
	}
	return p
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

// pickCompletionsClient resolves deps.AI — the CHAT COMPLETIONS client. The OSS
// core has no in-process inference gateway: it returns the out-of-process ZAP
// client when an address is configured (an operator points it at their own
// gateway), else the fail-closed stub — a run records an honest error, never
// fakes one.
func pickCompletionsClient(cfg *Config, log luxlog.Logger) AIClient {
	if cfg.AIZAPAddr != "" {
		log.Info("deps.AI (completions) → ZAP RPC", "addr", cfg.AIZAPAddr)
		return clients.AIRPCAt(cfg.AIZAPAddr)
	}
	log.Info("deps.AI (completions) → disabled (no gateway configured)")
	return clients.DisabledAI()
}

// pickEmbedClient resolves deps.Embed — the EMBEDDINGS client. Shares the
// completions resolution (ZAP / fail-closed) in the OSS core.
func pickEmbedClient(cfg *Config, log luxlog.Logger) AIClient {
	return pickCompletionsClient(cfg, log)
}

func pickVFSClient(cfg *Config, log luxlog.Logger) VFSClient {
	// deps.VFS is NEVER nil: nothing fills it after MountAll (Mount receives deps
	// by value), so a consumer always holds a concrete client — ZAP when an
	// endpoint is configured, else the fail-closed stub (a non-nil error, never a
	// nil deref).
	if cfg.VFSZAPAddr != "" {
		log.Info("deps.VFS → ZAP RPC", "addr", cfg.VFSZAPAddr)
		return clients.VFSRPCAt(cfg.VFSZAPAddr)
	}
	return clients.DisabledVFS()
}

// MountFunc is a subsystem's mount contract: register your routes on app, using
// deps for everything shared. Every subsystem in the fleet exports exactly this
// signature, so Wire references each one directly and the compiler checks it.
type MountFunc func(app *zip.App, deps Deps) error

// ShutdownFunc releases a subsystem's process-lifetime resources (background
// goroutines, open DB handles) on graceful shutdown. It must be idempotent and
// bounded — Serve calls it within the shutdown deadline. ctx carries that
// deadline so a slow teardown is cut off rather than hanging SIGTERM.
type ShutdownFunc func(ctx context.Context) error

// MountSpec describes one subsystem to mount. There is NO Order field: the slice
// position in apps.Wire() IS the mount order — the composition root lists
// subsystems in the exact sequence they mount (and, reversed, tear down), so order
// is data read top-to-bottom in one file, not ints scattered across the tree.
type MountSpec struct {
	Name     string
	Mount    MountFunc
	Shutdown ShutdownFunc // optional; nil means the subsystem has nothing to tear down.

	// OwnsHealth marks a subsystem that serves its OWN GET /v1/<name>/health
	// (a real, fail-closed probe). Serve's generic liveness loop skips these so
	// its always-ok route never shadows the subsystem's real probe.
	OwnsHealth bool
}

// MountAll mounts every ENABLED subsystem in specs, in slice order — the order is
// the composition root's (apps.Wire()); MountAll does NOT sort. app is the
// concrete *zip.App from Serve, handed to each MountFunc as itself.
//
// Teardown is wired HERE, at mount time: right after a subsystem mounts, its
// ShutdownFunc (if any) is registered via app.OnShutdown. zip drains those hooks
// LIFO — AFTER the listeners stop accepting and in-flight requests drain — so
// registration-at-mount yields reverse-mount teardown (a dependency mounted before
// its dependents is torn down after them) with no subsystem torn down while a
// request still uses it. Only ENABLED specs mount, so only they register a hook;
// teardown needs no separate enablement gate.
func MountAll(app *zip.App, specs []MountSpec, cfg *Config, deps Deps) error {
	logger := deps.Logger
	for _, spec := range specs {
		if !cfg.Enabled(spec.Name) {
			logger.Debug("subsystem disabled", "name", spec.Name)
			continue
		}
		if err := spec.Mount(app, deps); err != nil {
			return fmt.Errorf("mount %s: %w", spec.Name, err)
		}
		// Register teardown as a zip shutdown hook. zip runs hooks LIFO after the
		// drain (zip.App.Shutdown), so this reproduces the reverse-mount order the
		// hand-rolled reverse-loop gave — without the teardown-before-drain race.
		if spec.Shutdown != nil {
			app.OnShutdown(spec.Shutdown)
		}
		logger.Info("mounted subsystem", "name", spec.Name)
	}
	return nil
}
