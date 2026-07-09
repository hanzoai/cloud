package cloud

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/hanzoai/cloud/clients/sites"
	"github.com/hanzoai/cloud/internal/storagelock"
	"github.com/hanzoai/cloud/role"
	"github.com/hanzoai/cloud/writerpin"
	"github.com/hanzoai/cloud/zapface"
	"github.com/zap-proto/zip"
	"github.com/zap-proto/zip/middleware"
)

// Serve boots the canonical compose root and mounts the selected subsystems.
//
// This is the ONE place the cloud-server body lives. cmd/cloud (the full fused
// surface) and every `hanzo <svc>` subcommand share it; no boot logic is
// duplicated per entrypoint.
//
// enable==nil ⇒ honor cfg.Enable from flags/env (cloud mode; empty = all).
// enable!=nil ⇒ force exactly that set (single-service mode), overriding
// --enable so `hanzo kms` is unambiguous.
//
// Serve registers the HIP-0106 liveness contract (GET /v1/<name>/health for
// every enabled subsystem) before MountAll, runs the canonical middleware
// pipeline (Recover → RequestID → Logger), and shuts down gracefully on
// SIGINT/SIGTERM.
func Serve(enable []string) error {
	cfg := LoadConfig()
	if enable != nil {
		cfg.Enable = enable
	}
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("config: %w", err)
	}

	// Storage lockdown: reject leaked legacy cloud-api Postgres env so the
	// SQLite-only orchestrator never adopts a stale DATABASE_URL. One store.
	if err := storagelock.CheckEnv(os.Getenv); err != nil {
		return fmt.Errorf("storage lockdown: %w", err)
	}

	// HA role. Unset CLOUD_ROLE ⇒ Writer ⇒ byte-identical to the single-pod
	// deployment. Fail CLOSED on an explicitly-invalid role rather than guess: a
	// wrong guess either demotes the real writer or risks a second writer opening
	// the RWO stores. This gates KMS into read-only reader mode (pickKMSClient);
	// see the Red Handoff for the reader subsystems still to be gated.
	resolvedRole, roleErr := role.FromEnv()
	if roleErr != nil {
		return fmt.Errorf("ha role: %w", roleErr)
	}
	cfg.Role = resolvedRole

	deps := BuildDeps(cfg)

	// Surface the resolved role and the writer-pin backing it. The pin is
	// SingleWriter today (k8s StatefulSet replicas:1 guarantees one writer);
	// consensus (Quasar) election is stubbed (writerpin.ConsensusPin) and NOT yet
	// gating store opens — logged here so operators see the real posture.
	deps.Logger.Info("HA role resolved",
		"role", cfg.Role.String(),
		"writer_pin", writerpin.Resolve().Kind(),
		"kms_read_only", cfg.Role.IsReader())

	// ReadBufferSize raises the fasthttp header ceiling above the 4 KiB fiber
	// default so a multi-domain SSO session (admin-guard Domain=.hanzo.ai
	// cookies on every subdomain) no longer 431s legitimate requests at the
	// public edge. Env GATEWAY_READ_BUFFER_SIZE, default 32 KiB (see config.go).
	app := zip.New(zip.Config{Logger: deps.Logger, ReadBufferSize: cfg.ReadBufferSize})

	// Canonical middleware pipeline. Order matters:
	//  1. Recover         — panic → JSON 500
	//  2. RequestID       — generate / propagate X-Request-Id
	//  3. Tracing         — one OTel SERVER span per /v1/* request, over ZAP
	//  4. Logger          — request-line log
	//  5. SanitizeIdentity — establish a VALIDATED principal (see below)
	app.Use(middleware.Recover())
	app.Use(middleware.RequestID())

	// Request tracing. Sits right after RequestID (so the span carries the
	// request_id) and BEFORE identity/audit/billing/handlers, so the whole
	// authenticated pipeline nests under one span and the span CONTEXT it writes
	// via SetContext parents every downstream span (agent.run → agent.step →
	// chat) into a single trace. Spans ship over the SAME global ZAP provider the
	// log pipeline uses (cmd/cloud initTelemetry), landing in hanzoai/datastore.
	// Health/readiness/metrics + non-/v1 paths are skipped (see traceable). See
	// middleware_tracing.go.
	app.Use(TracingMiddleware())

	app.Use(middleware.Logger(deps.Logger))

	// Read-replica write guard (fail-closed). On a Reader, refuse mutating verbs
	// at the boundary so a mis-routed write can never silently persist to the
	// reader's ephemeral store and vanish on restart. ONE place gates EVERY store
	// (KMS + audit + tasks + per-tenant SQLite), not just KMS's read-only open.
	// No-op on a Writer (the default), so unset CLOUD_ROLE is byte-identical.
	if cfg.Role.IsReader() {
		app.Use(ReaderGuard())
	}

	// Public site edge (clients/sites). Installed FIRST — after Recover/RequestID/
	// Logger, BEFORE SanitizeIdentity + BillingGate — so a request whose Host is a
	// published-site host (`<slug>.hanzo.app`) is served the site's static bytes
	// from OUR S3 and returns HERE, never entering the authenticated/billed API
	// pipeline. A published site is a PUBLIC artifact: no IAM JWT, no balance gate.
	// For every other Host this middleware calls Continue() and the pipeline below
	// runs unchanged. The slug→{org,bucket,prefix} resolver is the projects store,
	// injected at its Mount via sites.SetResolver; until then a site host 404s
	// honestly. Tenant isolation (org+prefix come only from the store keyed by the
	// validated slug; object keys are rooted-clean) lives in clients/sites.
	app.Use(sites.New(sites.Config{Apex: cfg.SitesApex, Reserved: cfg.SitesReserved, SelfDomains: cfg.SitesSelfDomains}, deps.Logger).Middleware())

	// Edge policy — the "gateway role" cloud absorbs to serve the public
	// api.hanzo.ai edge directly (no KrakenD gateway hop). Runs BEFORE identity by
	// design:
	//   - EdgeCORS answers the browser OPTIONS preflight (which carries no
	//     credentials) and short-circuits it, so a preflight never reaches auth.
	//     No-op unless CLOUD_CORS_ORIGINS is set (the shared ingress owns CORS on
	//     the recommended rollout — enabling both would double the ACAO header).
	//   - EdgeRateLimit caps an ANONYMOUS per-IP flood before the JWKS/validate/
	//     downstream work it would trigger — the one gap ScopeRateLimit (which keys
	//     on the validated tenant, below) structurally can't see. Keyed on the
	//     public client IP; in-cluster direct callers (no X-Forwarded-For) are
	//     exempt, matching the standalone gateway's public-only scope. See
	//     middleware_edge.go.
	app.Use(EdgeCORS(deps.GatewayPolicy))
	app.Use(EdgeRateLimit(deps.GatewayPolicy))

	// Identity trust boundary. Runs before BillingGate (which reads c.User()/
	// c.Org()) and every subsystem, so a downstream c.IsAdmin()/c.Org()/c.User()
	// reflects a VALIDATED IAM principal — never a raw client header. This makes
	// the gateway's "X-User-IsAdmin is never client-supplied" contract hold even
	// when cloud-api is reached directly (in-cluster) instead of through the
	// gateway, closing the forgeable-admin trust boundary. The admin claim is
	// granted ONLY to a validated GLOBAL admin (owner == AdminOrg). See
	// middleware_identity.go / auth_identity.go.
	app.Use(IdentityMiddleware(cfg))

	// Audit trail (FedRAMP AU-* / SOC 2 CC-*). Runs AFTER SanitizeIdentity so the
	// actor/isAdmin it records come from a VALIDATED principal (never a raw
	// header), and BEFORE BillingGate + every subsystem so it wraps the whole
	// chain and observes the final outcome — including a billing 402/503 and an
	// admin 403 denial. It is the ONE place every security-relevant request is
	// recorded to the tamper-evident, append-only store (see audit_middleware.go /
	// audit/). A write failure fails the request CLOSED (AU-5). Constructed here
	// so the Recorder lives for the process and the /v1/admin/audit query + verify
	// endpoints (clients/admin) read the SAME store via deps.Audit.
	auditRec, err := buildAuditRecorder(cfg, deps.Logger)
	if err != nil {
		return fmt.Errorf("audit: %w", err)
	}
	deps.Audit = auditRec
	app.Use(AuditTrail(auditRec))

	// Per-scope rate limit (issue #70). Runs AFTER identity (needs the validated
	// principal to key on org/project/service) and AFTER audit (so a 429 is
	// recorded), and BEFORE BillingGate so an over-rate request is rejected before
	// any balance/spend-cap work. Fail-open when commerce is unreachable — a
	// rate-limit outage never blocks paid traffic. Also honors the /v1/gateway
	// per-org OrgRPM (deps.GatewayPolicy): the runtime-mutable per-org ceiling,
	// most-restrictive-wins with any commerce-configured limit. No-op only when
	// BOTH sources are absent.
	app.Use(ScopeRateLimit(deps.Metering, deps.GatewayPolicy))

	// Billing gate. Sits at the (future) Auth position — after identity is
	// established by Recover/RequestID/Logger and before any subsystem mounts —
	// so every priced route is balance-gated once, at the edge, fail-closed.
	// No-op when metering is unconfigured (deps.Metering not Enabled()), so
	// it is always wired unconditionally. DefaultPrice keeps self-metering
	// subsystems (notably /v1/ai/*) at 0 to avoid double-billing.
	app.Use(BillingGate(deps.Metering, DefaultPrice))

	// HIP-0106 liveness contract: every enabled subsystem answers
	// GET /v1/<name>/health uniformly, registered at the compose root before
	// MountAll so it precedes subsystem /v1/<n>/* wildcards.
	//
	// A subsystem that owns its health (OwnsHealth, e.g. kms/paas/s3) serves its
	// OWN fail-closed /v1/<name>/health in Mount; skip it here so this always-ok
	// route never shadows the real probe.
	for _, spec := range Registry {
		if !cfg.Enabled(spec.Name) || spec.OwnsHealth {
			continue
		}
		name := spec.Name
		app.Get("/v1/"+name+"/health", func(c *zip.Ctx) error {
			return c.JSON(200, map[string]string{"service": name, "status": "ok"})
		})
	}

	if err := MountAll(app, cfg, deps); err != nil {
		return fmt.Errorf("mount: %w", err)
	}

	// Browser-facing ZAP RPC plane. console (@hanzo/gui + @zap-proto/web)
	// reaches the SAME /v1 handlers over a WebSocket carrying binary ZAP frames
	// — no second copy of any business logic: each call is replayed in-process
	// through this Fiber app (see zapface). Mounted AFTER MountAll so every /v1
	// route exists before the dispatcher captures the app.
	app.Get("/zap", zapface.Handler(app.Fiber(), zapface.Options{
		OriginPatterns: cfg.ZAPWebOrigins,
		Logger:         deps.Logger,
	}))

	// Unified console UI — the SAME binary serves the @hanzo/gui console (built
	// from hanzoai/console and embedded via //go:embed) at the web root. Mounted
	// LAST, after every /v1 route + the /zap plane + the health contract, so
	// Fiber's in-order matching gives the API precedence: real API routes win,
	// and only paths that match nothing else fall through to the SPA (index.html
	// for client-side deep links). The API namespace (/v1, /zap, /healthz…) never
	// renders as HTML — an unmatched path there is a real 404. Same-origin: the
	// embedded console calls /v1 on its own host, so the session cookie is
	// first-party and no second origin / CORS is involved. See webui.go.
	if err := mountConsole(app); err != nil {
		return fmt.Errorf("console: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Durable ingest: embed the ONE tasks engine in-process + inject the per-org dialer
	// into ai (long github/crawl/s3 ingests run as durable workflows; upload stays
	// inline). Fail-soft — inline fallback if the engine can't start. See durable.go.
	wireDurableIngest(ctx, deps)

	// Health/metrics listener (HealthListenAddr, default :9090). Serves the
	// liveness/readiness contract the platform probes hit (/healthz, /readyz)
	// on a port SEPARATE from the public API, so a saturated/again-starting API
	// surface never flaps liveness. Previously HealthListenAddr was declared but
	// never bound; the operator's probes target :9090, so without this the pod
	// fails liveness and CrashLoops. Runs in its own goroutine; a bind failure
	// is fatal (propagated via listenErr) so a misconfigured port fails loud.
	healthSrv := &http.Server{
		Addr:              cfg.HealthListenAddr,
		Handler:           healthMux(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	listenErr := make(chan error, 1)
	go func() {
		deps.Logger.Info("health listening", "addr", cfg.HealthListenAddr)
		if err := healthSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			listenErr <- fmt.Errorf("health listen: %w", err)
		}
	}()
	go func() {
		deps.Logger.Info("listening",
			"http", cfg.ListenAddr,
			"zap", cfg.ZAPListenAddr,
			"enabled", cfg.Enable,
			"brand", cfg.Brand,
			"domain", cfg.Domain,
		)
		// ONE app, TWO transports: ZAP is the primary machine transport
		// (PLAINTEXT TCP over :9653 — parity with prior HTTP; needs mesh mTLS), plain HTTP the edge/browser extra. Both serve the
		// identical route surface, so /v1/* answers over either. Serve returns
		// the first listener error.
		listenErr <- app.Listen(cfg.ZAPListenAddr, "http://"+cfg.ListenAddr)
	}()

	select {
	case <-ctx.Done():
		deps.Logger.Info("shutdown requested")
	case err := <-listenErr:
		return fmt.Errorf("listen: %w", err)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = healthSrv.Shutdown(shutdownCtx)
	// Tear down subsystems that own process-lifetime resources (background
	// workers, DB handles) BEFORE the HTTP server closes, within the deadline —
	// e.g. the agents scheduler drains its in-flight runs (so a scheduled run's
	// InsertRun + debit land) and closes its store. Best-effort: a teardown error
	// is logged, not fatal, so one subsystem can't strand shutdown.
	if err := ShutdownAll(shutdownCtx, cfg); err != nil {
		deps.Logger.Warn("subsystem shutdown", "err", err)
	}
	// Close the audit store last so any in-flight append has drained through the
	// serialized writer and the SQLite file is flushed cleanly.
	if auditRec != nil {
		_ = auditRec.Close()
	}
	// Close the runtime edge-policy store (owned here, shared by the edge
	// middleware + the /v1/gateway subsystem) so its SQLite WAL flushes cleanly.
	if deps.GatewayPolicy != nil {
		_ = deps.GatewayPolicy.Close()
	}
	return app.ShutdownWithContext(shutdownCtx)
}

// healthMux is the liveness/readiness + metrics contract on the ops port
// (HIP-0113). /healthz, /readyz, /health return 200 once the process is up
// (readiness can grow a real dependency check later); /metrics exposes a
// minimal Prometheus surface so scrapes target THIS listener, not the product
// API. Kept dependency-free (stdlib only) so the ops surface never shares
// failure modes with the API stack.
func healthMux() *http.ServeMux {
	mux := http.NewServeMux()
	ok := func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}
	mux.HandleFunc("/healthz", ok)
	mux.HandleFunc("/readyz", ok)
	mux.HandleFunc("/health", ok)
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = w.Write([]byte("# HELP cloud_up 1 if the process is serving.\n# TYPE cloud_up gauge\ncloud_up 1\n"))
	})
	return mux
}
