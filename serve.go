package cloud

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/hanzoai/cloud/cek"
	"github.com/hanzoai/cloud/internal/storagelock"
	"github.com/hanzoai/cloud/openapi"
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

	deps := BuildDeps(cfg)

	// Telemetry bootstrap — the ONE site. Installs the process-global OTel tracer
	// provider when an installer is registered (cloud.RegisterTelemetryInstaller);
	// a no-op (non-nil shutdown) otherwise. Runs BEFORE MountAll, on every
	// entrypoint.
	telemetryShutdown := installTelemetry(context.Background(), "hanzo-cloud")

	// Data-plane encryption posture (cek). On an encryption-capable build a
	// missing/invalid CLOUD_KMS_MASTER_KEY_REF makes the FIRST store open fail
	// closed (MountAll aborts) — the same fail-closed stance as the KMS store; we
	// surface it here so the posture is never silent.
	// TWO QUESTIONS, AND THEY WERE BEING ASKED AS ONE. Encrypting() says a usable
	// master key is present. It does not say this BUILD can encrypt, and the
	// comment that used to stand here asserted that every backend can —
	// measured, an ordinary `go build` does not: cgo links plain SQLite unless
	// the build says -tags libsqlite3 against libsqlcipher. On such a build the
	// key is present, so the line below announced encryption, every store was
	// then written with the plaintext SQLite header, and the SECOND boot died in
	// migration with `sqlcipher_export: no such function`. Between those two
	// moments it had written customer data to disk in the clear while saying it
	// had not, which is the worst shape a privacy claim can take.
	//
	// So the capability is asked too, by making a keyed store and looking at it
	// (cek.Capable), and a key without a codec REFUSES TO BOOT. It is the same
	// fail-closed stance the rest of this posture takes; what it adds is that
	// the failure now happens before any data is written rather than after.
	if cek.Encrypting() {
		if err := cek.Capable(); err != nil {
			return fmt.Errorf("data-plane encryption: %w", err)
		}
		deps.Logger.Info("data-plane encryption ACTIVE (SQLCipher at rest, per-db DEK)")
	} else {
		deps.Logger.Warn("data-plane encryption OFF — no usable " + cek.MasterKeyEnv +
			"; store opens fail closed until one is set (or " + cek.DevUnencryptedEnv + "=1)")
	}

	// ReadBufferSize raises the fasthttp header ceiling above the 4 KiB default,
	// and BodyLimit raises the 4 MiB body default (see config.go).
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
	//  5. IdentityMiddleware — establish a VALIDATED principal (see below)
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

	// Edge policy. Runs BEFORE identity by design:
	//   - EdgeCORS answers the browser OPTIONS preflight (which carries no
	//     credentials) and short-circuits it, so a preflight never reaches auth.
	//     No-op unless CLOUD_CORS_ORIGINS is set.
	//   - EdgeRateLimit caps an ANONYMOUS per-IP flood before the JWKS/validate/
	//     downstream work it would trigger — the one gap ScopeRateLimit (which keys
	//     on the validated org, below) structurally can't see. Keyed on the
	//     forwarded client IP; a direct caller (no X-Forwarded-For) is exempt.
	//     See middleware_edge.go.
	app.Use(EdgeCORS(deps.GatewayPolicy))
	app.Use(EdgeRateLimit(deps.GatewayPolicy))

	// Identity trust boundary. Runs before every subsystem, so a downstream
	// c.IsAdmin()/c.Org()/c.User() reflects a VALIDATED IAM principal — never a
	// raw client header. The admin claim is granted ONLY to a validated
	// SuperAdmin (owner == AdminOrg). See middleware_identity.go /
	// auth_identity.go.
	app.Use(IdentityMiddleware(cfg))

	// Audit trail (FedRAMP AU-* / SOC 2 CC-*). Runs AFTER identity so the
	// actor/isAdmin it records come from a VALIDATED principal (never a raw
	// header), and BEFORE every subsystem so it wraps the whole chain and
	// observes the final outcome — including an admin 403 denial. It is the ONE place every security-relevant request is
	// recorded to the tamper-evident, append-only store (see audit_middleware.go /
	// audit/). A write failure fails the request CLOSED (AU-5). Constructed here
	// so the Recorder lives for the process and the /v1/admin/audit query + verify
	// endpoints (clients/admin) read the SAME store via deps.Audit.
	auditRec, err := buildAuditRecorder(cfg, deps.Logger)
	if err != nil {
		return fmt.Errorf("audit: %w", err)
	}
	deps.Audit = auditRec
	// The request, on its own context, so the op-invoke seam can find it — the
	// seam is handed a context and not a *zip.Ctx. It has to precede the trail:
	// what the trail reads is written through this.
	app.Use(Carry())
	app.Use(AuditTrail(auditRec))

	// Per-scope rate limit (issue #70). Runs AFTER identity (needs the validated
	// principal to key on org) and AFTER audit (so a 429 is recorded). Honors the
	// /v1/gateway per-org OrgRPM (deps.GatewayPolicy): the runtime-mutable per-org
	// ceiling an operator sets. No-op when no policy store is present.
	app.Use(ScopeRateLimit(deps.GatewayPolicy))

	// HIP-0106 liveness contract: every enabled subsystem answers
	// GET /v1/<name>/health uniformly, registered at the compose root before
	// MountAll so it precedes subsystem /v1/<n>/* wildcards.
	//
	// A subsystem that owns its health (OwnsHealth, e.g. kms/platform/s3) serves its
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
		openapi.Server{URL: serverURL(cfg.Domain)},
	)

	// Install zip's own deferred projections of the typed-op registry — the MCP
	// endpoint at /mcp, the typed OpenAPI document, the op-call plane — BEFORE the
	// console catch-all below.
	//
	// zip installs these itself, but from Listen, which is AFTER everything mounted
	// here. Fiber matches in registration order and the catch-all matches
	// everything, so a /mcp registered after it is a route the router can never
	// reach: the endpoint existed and still answered 404. Prepare is idempotent
	// (sync.Once), so Listen's own call becomes a no-op and this is purely a
	// question of who registers first.
	//
	// It has to be here rather than earlier for the same reason openapi.Mount is:
	// the projections read the COMPLETE op registry, so they run after MountAll.
	app.Prepare()

	// Mounted LAST, after every /v1 route, the /zap plane, the health contract and
	// the projections above, this registers the terminal catch-all: the embedded
	// dev console at the web root, still a JSON 404 for an unmatched /v1 path so a
	// client that expects JSON never receives an HTML shell. See webui.go.
	if err := mountConsole(app); err != nil {
		return fmt.Errorf("console: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Durable engine boot hook. The no-op default runs durable work inline; a
	// registered engine takes over. See durable.go.
	wireDurableIngest(ctx, deps)

	// The rule every operation answers to, installed once and here because this
	// is where composition ends: a rule declared after the leaves still covers
	// them, but it has to be in force before anything is served. Today it states
	// one fact — which operation an invoke is — and it is the only place that
	// can, because zip.Op is the same value on every door while the request path
	// is not (note.go).
	app.Authorize(Rule())

	// Health/metrics listener (HealthListenAddr, default 127.0.0.1:9090). Serves
	// the liveness/readiness contract (/healthz, /readyz) on a port SEPARATE from
	// the API, so a saturated API surface never flaps liveness. A bind failure is
	// fatal (propagated via listenErr) so a misconfigured port fails loud.
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
		// ONE app, TWO transports: ZAP (plaintext TCP, loopback by default) and
		// HTTP. Both serve the identical route surface, so /v1/* answers over
		// either. Serve returns the first listener error.
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
	// Flush the tracer provider FIRST — before app.ShutdownWithContext runs the
	// subsystem teardown hooks — so buffered spans drain while every sink is
	// still mounted.
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
	// BEFORE teardown means no subsystem is torn down while a request still uses
	// it. A hook error is joined into the returned error, never fatal to the others.
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
	// ONE liveness address, and it is /healthz. "/health" was a second name for the
	// same handler, so one probe answered to two spellings. A caller on the old name
	// now gets a 404 that claims no endpoint, which is the honest answer.
	mux.HandleFunc("/healthz", ok)
	mux.HandleFunc("/readyz", ok)
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = w.Write([]byte("# HELP cloud_up 1 if the process is serving.\n# TYPE cloud_up gauge\ncloud_up 1\n"))
	})
	return mux
}

// serverURL is the base URL the OpenAPI document names for domain: plain HTTP on
// a loopback host, HTTPS anywhere else.
func serverURL(domain string) string {
	host := domain
	if h, _, err := net.SplitHostPort(domain); err == nil {
		host = h
	}
	if host == "localhost" || net.ParseIP(host).IsLoopback() {
		return "http://" + domain
	}
	return "https://" + domain
}
