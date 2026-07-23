package cloud

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/hanzoai/cloud/cek"
	"github.com/hanzoai/cloud/clients/sites"
	"github.com/hanzoai/cloud/internal/storagelock"
	"github.com/hanzoai/cloud/openapi"
	"github.com/hanzoai/cloud/role"
	"github.com/hanzoai/cloud/writerpin"
	"github.com/hanzoai/cloud/zapface"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
	"github.com/zap-proto/zip/middleware"
)

// Serve boots the canonical compose root and mounts the selected subsystems.
//
// This is the ONE place the cloud-server body lives. cmd/cloud (the full fused
// surface) and every `hanzo <svc>` subcommand share it; no boot logic is
// duplicated per entrypoint.
//
// specs is the composition root's subsystem list (apps.Wire()), threaded
// in by the caller so cloud never imports subsystems (which would cycle). Serve
// mounts it in slice order and tears it down in reverse.
//
// enable==nil ⇒ honor cfg.Enable from flags/env (cloud mode; empty = all).
// enable!=nil ⇒ force exactly that set (single-service mode), overriding
// --enable so `hanzo kms` is unambiguous.
//
// Serve registers the HIP-0106 liveness contract (GET /v1/<name>/health for
// every enabled subsystem) before MountAll, runs the canonical middleware
// pipeline (Recover → RequestID → Logger), and shuts down gracefully on
// SIGINT/SIGTERM.
func Serve(specs []MountSpec, enable []string) error {
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

	// Reader role: a transparent, always-ready reverse proxy to the writer. It
	// opens NO stores (the KMS ZapDB store is not RO-shareable while the writer is
	// live — clients/kms.TestConcurrentOpen_LiveWriterStoreIsNotROShareable) and
	// forwards every request to CLOUD_WRITER_URL, retrying dial-only across the
	// writer's roll gap so the edge never blips. Returns here — never reaches
	// BuildDeps. Unset CLOUD_ROLE ⇒ Writer, so this is inert by default.
	if cfg.Role.IsReader() {
		return serveReaderProxy(cfg)
	}

	// Writer role, optional lease. When CLOUD_WRITER_LEASE is set (the surge/
	// overlap roll topology), take the exclusive writer lease BEFORE opening the
	// RWO stores and release it LAST (after every store is closed) so a surge
	// writer never double-opens the exclusive-lock ZapDB/audit stores. Default
	// OFF: a Recreate single-writer never overlaps and needs no lease, so an unset
	// variable is byte-identical to today.
	if cfg.WriterLease {
		release, lerr := acquireWriterLease(cfg.DataDir, 90*time.Second, luxlog.New("cloud").New("subsystem", "writer-lease"))
		if lerr != nil {
			return fmt.Errorf("writer lease: %w", lerr)
		}
		// Released after the shutdown path closes every store (app.Shutdown's
		// subsystem teardown hooks + audit + gateway-policy) below; defer is the
		// store-close backstop that also covers early error returns (the kernel
		// reclaims on exit regardless).
		defer func() { _ = release() }()
	}

	deps := BuildDeps(cfg)

	// Surface the resolved role and the writer-pin backing it. The pin is
	// SingleWriter today (k8s StatefulSet replicas:1 guarantees one writer);
	// consensus (Quasar) election is stubbed (writerpin.ConsensusPin) and NOT yet
	// gating store opens — logged here so operators see the real posture.
	deps.Logger.Info("HA role resolved",
		"role", cfg.Role.String(),
		"writer_pin", writerpin.Resolve().Kind(),
		"kms_read_only", cfg.Role.IsReader())

	// Horizontal-scale shard router. When CLOUD_PEERS names >1 pod, each org is
	// pinned to its rendezvous-hash owner pod: THIS pod is the single writer for the
	// orgs it owns (writerpin.SingleWriter is correct PER SHARD), and any other org's
	// request is forwarded to its owner. nil ⇒ single-pod (no-op middleware below).
	// This is what lifts the deployment off replicas:1 without any shared RWX volume —
	// per-pod RWO PVC + org→owner routing = one writer per tenant file. See
	// shardrouter.go.
	shardRtr := newShardRouter(cfg, deps.Logger, deps.LiveMembers)
	if shardRtr != nil {
		deps.Logger.Info("shard routing ENABLED (horizontal writer scale)",
			"self", shardRtr.self, "peers", shardRtr.peerIDs(),
			"writer_pin", "single-writer-per-shard")
	}

	// Telemetry bootstrap — the ONE site. Install the process-global OTel tracer
	// provider (wired to the o11y in-process trace sink by clients/o11y) and ADOPT it
	// into the embedded ai module, so ai emits its gen_ai span per LLM call through the
	// SAME provider. Runs BEFORE MountAll mounts ai (ai's object.InitTelemetry reads
	// the adopted-ready flag at mount), and on EVERY entrypoint — cmd/cloud AND every
	// `hanzo <svc>` share this body — replacing the cmd/cloud-only bootstrap that left
	// `hanzo <svc>` telemetry-dark. No-op (non-nil shutdown) when clients/o11y isn't
	// linked or no sink/endpoint is configured. clients/o11y installs the concrete
	// bootstrap via cloud.RegisterTelemetryInstaller (the cycle-free inversion).
	telemetryShutdown := installTelemetry(context.Background(), "hanzo-cloud")

	// Data-plane encryption posture (cek). Every build encrypts a keyed store — the
	// live libsqlcipher codec in production, the pure-Go codec envelope in dev/CI —
	// so a store either opens keyed-and-encrypted or fails closed; there is no
	// plaintext-at-rest mode. EnsureDevKey gives a pure-Go dev/CI build with no
	// configured key a deterministic dev key so it runs encrypted with zero config;
	// a production (codec-linked) build with a missing/invalid CLOUD_KMS_MASTER_KEY_REF
	// makes the FIRST store open fail closed (MountAll aborts). It runs BEFORE the
	// posture read below, which caches the resolved key. Surfaced so it is never silent.
	switch {
	case cek.EnsureDevKey():
		deps.Logger.Warn("data-plane encryption ACTIVE with a DEV key (pure-Go build, no KMS key configured — dev/CI only)")
	case cek.Encrypting():
		deps.Logger.Info("data-plane encryption ACTIVE (SQLCipher at rest, per-db DEK)")
	default:
		deps.Logger.Warn("data-plane encryption posture: missing/invalid key on a production build → store opens fail closed")
	}

	// ReadBufferSize raises the fasthttp header ceiling above the 4 KiB fiber
	// default so a multi-domain SSO session (admin-guard Domain=.hanzo.ai
	// cookies on every subdomain) no longer 431s legitimate requests at the
	// public edge. Env GATEWAY_READ_BUFFER_SIZE, default 32 KiB (see config.go).
	//
	// BodyLimit is the same shape of bug one layer down: the framework default is
	// 4 MiB, and a full-context chat request is BIGGER than that. A 1M-token
	// prompt serializes to ~4.3 MB of JSON, so the 1M-context models we route to
	// (deepseek-v4-pro, and anything glm-5.2 overflows into) were unreachable —
	// fasthttp rejected the body before any handler ran, and its wire error is the
	// opaque 400 "Error when parsing request", which reads like a malformed
	// payload rather than a size cap. Env GATEWAY_BODY_LIMIT (see config.go).
	app := zip.New(zip.Config{
		Logger:         deps.Logger,
		ReadBufferSize: cfg.ReadBufferSize,
		BodyLimit:      cfg.BodyLimit,
		// Static Server fallback for responses the ProductionHeaders middleware
		// cannot reach — the transport's own pre-routing errors (431/400) and any
		// fiber path that bypasses the chain. Set to this deployment's brand so
		// those bytes read Server: <brand>, never the framework default "zip" or
		// "fasthttp" (zip>=v1.8.1 propagates this onto the fasthttp transport).
		// Handled responses are still branded per-Host by ProductionHeaders.
		ServerHeader: cfg.Brand,
	})

	// Canonical middleware pipeline. Order matters:
	//  1. Recover         — panic → JSON 500
	//  2. RequestID       — generate / propagate X-Request-Id
	//  3. Tracing         — one OTel SERVER span per /v1/* request, over ZAP
	//  4. Logger          — request-line log
	//  5. SanitizeIdentity — establish a VALIDATED principal (see below)
	app.Use(middleware.Recover())
	app.Use(middleware.RequestID())

	// Production response-header posture — the Stripe/Cloudflare/GitHub-grade
	// signals plus a security floor, from ONE home in the framework so every
	// service inherits the same wire posture. Registered right after RequestID
	// (before the site edge and the business chain) so its headers ride out on
	// every response: success, error, 404, AND the public-site static bytes.
	//   - Server: the white-label brand of the request Host (BrandForHostOK) — a
	//     lux/zoo caller is never served "hanzo" and no response leaks the
	//     framework name; an unmatched Host falls back to this deployment's own
	//     brand (cfg.Brand), never a framework/single-brand default.
	//   - X-Api-Version: the build version (brand-neutral key) for support correlation.
	//   - HSTS + nosniff: the always-safe security floor (no X-Frame-Options/CSP
	//     here — the console SPA owns its own framing rules).
	// X-Request-Id stays owned by RequestID above; the two compose.
	app.Use(middleware.ProductionHeaders(middleware.ProductionHeadersConfig{
		Brand:   func(host string) string { b, _ := BrandForHostOK(host); return b },
		Neutral: cfg.Brand,
		Version: cfg.Version,
		HSTS:    true,
	}))

	// Markdown content negotiation. Registered here — outermost of the business
	// chain, just inside Recover/RequestID — so its post-Continue transform sees
	// the FINAL response body and re-serializes it via zap-proto/md when the
	// caller asked for markdown (Accept: text/markdown or ?format=md). JSON stays
	// the default for machines; cfg.MarkdownDefaultPrefixes lets designated
	// agent endpoints (/v1/code/, /v1/agents/…) default to markdown. Touches NO
	// handler and fails safe (a render error leaves the JSON intact). See
	// middleware_markdown.go.
	app.Use(MarkdownNegotiation(cfg.MarkdownDefaultPrefixes))

	// Request tracing. Sits right after RequestID (so the span carries the
	// request_id) and BEFORE identity/audit/billing/handlers, so the whole
	// authenticated pipeline nests under one span and the span CONTEXT it writes
	// via SetContext parents every downstream span (agent.run → agent.step →
	// chat) into a single trace. Spans ship over the SAME global provider installed
	// above by installTelemetry, landing in hanzoai/datastore.
	// Health/readiness/metrics + non-/v1 paths are skipped (see traceable). See
	// middleware_tracing.go.
	app.Use(TracingMiddleware())

	app.Use(middleware.Logger(deps.Logger))

	// (A Reader never reaches here — it returns at serveReaderProxy above, opening
	// no stores and no middleware pipeline. This body is the Writer path only.)

	// Public site edge (clients/sites). Installed FIRST — after Recover/RequestID/
	// Logger, BEFORE SanitizeIdentity + BillingGate — so a request whose Host is a
	// published-site host (`<slug>.hanzo.app`) is served the site's static bytes
	// from OUR S3 and returns HERE, never entering the authenticated/billed API
	// pipeline. A published site is a PUBLIC artifact: no IAM JWT, no balance gate.
	// For every other Host this middleware calls Continue() and the pipeline below
	// runs unchanged. The slug→{org,bucket,prefix} resolver is the projects store,
	// injected at its Mount via sites.SetResolver; until then a site host 404s
	// honestly. Org isolation (org+prefix come only from the store keyed by the
	// validated slug; object keys are rooted-clean) lives in clients/sites.
	app.Use(sites.New(sites.Config{Apex: cfg.SitesApex, Reserved: cfg.SitesReserved, SelfDomains: cfg.SitesSelfDomains, FirstPartyApex: cfg.SitesFirstPartyApex, FirstPartySites: cfg.SitesFirstPartySites, FirstPartyOrg: cfg.SitesFirstPartyOrg}, deps.Logger).Middleware())

	// Edge policy — the "gateway role" cloud absorbs to serve the public
	// api.hanzo.ai edge directly (no KrakenD gateway hop). Runs BEFORE identity by
	// design:
	//   - EdgeCORS answers the browser OPTIONS preflight (which carries no
	//     credentials) and short-circuits it, so a preflight never reaches auth.
	//     No-op unless CLOUD_CORS_ORIGINS is set (the shared ingress owns CORS on
	//     the recommended rollout — enabling both would double the ACAO header).
	//   - EdgeRateLimit caps an ANONYMOUS per-IP flood before the JWKS/validate/
	//     downstream work it would trigger — the one gap ScopeRateLimit (which keys
	//     on the validated org, below) structurally can't see. Keyed on the
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
	// granted ONLY to a validated SuperAdmin (owner == AdminOrg). See
	// middleware_identity.go / auth_identity.go.
	app.Use(IdentityMiddleware(cfg))

	// Console identity = the ONE validated principal, not the embedded casibase account
	// model. When a principal is present, /v1/get-account reflects it so the operator
	// UI's SuperAdmin gate sees the same owner+isAdmin every /v1/admin/* route already
	// authorizes on (a PKCE session is not a casibase session — without this the UI
	// bounced to login despite valid admin API access). No principal → casibase path
	// unchanged. Runs AFTER IdentityMiddleware, BEFORE MountAll's casibase mount.
	app.Use(AccountFromPrincipal())

	// Shard router (horizontal writer scale). Runs IMMEDIATELY after SanitizeIdentity
	// — so it keys on the VALIDATED, server-minted X-Org-Id (never a raw client
	// header) — and BEFORE audit/rate-limit/billing/subsystems, so a request whose
	// org this pod does not own is forwarded to the owner and NONE of the downstream
	// per-org work (audit append, per-org rate ceiling, prepaid billing debit, every
	// per-org SQLite store) runs on the wrong pod. No-op (shardRtr==nil) on a
	// single-pod deployment: byte-identical to today. See shardrouter.go.
	if shardRtr != nil {
		app.Use(shardRtr.Middleware())
	}

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
	for _, spec := range specs {
		if !cfg.Enabled(spec.Name) || spec.OwnsHealth {
			continue
		}
		name := spec.Name
		app.Get("/v1/"+name+"/health", func(c *zip.Ctx) error {
			return c.JSON(200, map[string]string{"service": name, "status": "ok"})
		})
	}

	if err := MountAll(app, specs, cfg, deps); err != nil {
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

	// IAM edge — front the standalone Hanzo IAM at /v1/iam/* (org-scoped) so the
	// one-binary console can read org members + projects. Mounted ONLY when IAM is
	// not folded in-process (else that subsystem already owns /v1/iam/*, via
	// MountAll above — no double-mount) and an IAM origin is configured. Before the
	// console catch-all, so a real IAM segment answers JSON, not the SPA shell.
	if !cfg.Enabled("iam") && iamHost() != "" {
		newIamEdge().mount(app)
	}

	// GET /v1/openapi.json — the THIRD projection of the same route table. ZAP
	// replays the /v1 handlers, the console renders them, and this DESCRIBES
	// them; all three read the one router, so none can drift from it. Mounted
	// beside /zap and for the same reason: after MountAll, so the document is
	// generated from a complete table. What it describes is therefore exactly
	// what THIS deployment mounted — enablement scopes the spec for free.
	//
	// Unauthenticated by design (it grants no capability, and `hanzo --help`
	// must build its command tree before login) — see openapi.Mount.
	openapi.Mount(app,
		openapi.Info{
			Title:   deps.Brand + " cloud API",
			Version: deps.Version,
			Description: "Generated from the live router at request time — every operation " +
				"below is a route this process actually serves. Tagged by product: the first " +
				"path segment after /v1/.",
		},
		openapi.Server{URL: "https://" + cfg.Domain},
	)

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
		// Graceful drain: go NotReady so peers re-elect this pod's orgs to live successors
		// (each hydrates the latest fenced snapshot, M3) BEFORE we stop serving, then pause
		// for that to propagate through the membership refresh. Only when sharding is active
		// — a single-pod deployment has no successor, so it drains immediately and relies on
		// the final ship (CloseAll) below. In-flight requests drain in app.ShutdownWithContext.
		SetDraining()
		if shardRtr != nil {
			deps.Logger.Info("draining: NotReady, waiting for peers to re-elect owned orgs", "grace", shardDrainGrace)
			time.Sleep(shardDrainGrace)
		}
	case err := <-listenErr:
		return fmt.Errorf("listen: %w", err)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = healthSrv.Shutdown(shutdownCtx)
	// Flush the tracer provider FIRST — before app.ShutdownWithContext runs the o11y
	// trace sink's teardown hook (a subsystem) — so the batch processor's buffered
	// spans drain through the still-mounted in-process sink to datastore rather than
	// hitting ErrNoRoute.
	telemetryShutdown(shutdownCtx)
	// Close the audit store so any in-flight append has drained through the
	// serialized writer and the SQLite file is flushed cleanly.
	if auditRec != nil {
		_ = auditRec.Close()
	}
	// Close the runtime edge-policy store (owned here, shared by the edge
	// middleware + the /v1/gateway subsystem) so its SQLite WAL flushes cleanly.
	if deps.GatewayPolicy != nil {
		_ = deps.GatewayPolicy.Close()
	}
	// Graceful stop, owned by zip: it stops the listeners accepting, drains
	// in-flight requests, THEN runs each subsystem's teardown hook LIFO (reverse
	// mount order) — the hooks MountAll registered via app.OnShutdown. Draining
	// BEFORE teardown is the fix for the old hand-rolled reverse-loop, which tore
	// subsystems down while the listener still accepted: e.g. the agents scheduler
	// now drains its in-flight runs (InsertRun + debit land) and closes its store
	// only after requests quiesce. A hook error is joined into the returned error,
	// never fatal to the others.
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
	mux.HandleFunc("/healthz", ok) // liveness: stays 200 while draining (finish the drain).
	// readiness: 503 once draining so K8s marks the pod NotReady — removed from endpoints
	// AND from every peer's writer election — before it stops serving (graceful handoff).
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		if Draining() {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"status":"draining"}`))
			return
		}
		ok(w, r)
	})
	mux.HandleFunc("/health", ok)
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = w.Write([]byte("# HELP cloud_up 1 if the process is serving.\n# TYPE cloud_up gauge\ncloud_up 1\n"))
	})
	return mux
}
