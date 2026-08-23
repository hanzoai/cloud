package cloud

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/hanzoai/cek"
	"github.com/hanzoai/cloud/internal/storagelock"
	"github.com/hanzoai/cloud/internal/writerlease"
	"github.com/hanzoai/cloud/manifest"
	"github.com/hanzoai/cloud/openapi"
	"github.com/hanzoai/cloud/role"
	"github.com/hanzoai/cloud/webui"
	"github.com/hanzoai/cloud/webui/release"
	"github.com/hanzoai/cloud/writerpin"
	"github.com/hanzoai/cloud/zapface"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// Serve boots the canonical compose root and mounts the selected subsystems.
//
// This is the ONE place the cloud-server body lives — every per-app plugin main
// (plugin/<app>/main.go) calls it, so no boot logic is duplicated per subsystem.
//
// plugins is the composition root's subsystem list (apps.Wire()), threaded
// in by the caller so cloud never imports subsystems (which would cycle). Listen
// mounts it in slice order and tears it down in reverse.
//
// enable==nil ⇒ honor cfg.Enable from flags/env (cloud mode; empty = all).
// enable!=nil ⇒ force exactly that set (single-service mode), overriding
// --enable so `hanzo kms` is unambiguous.
//
// Listen registers the HIP-0106 liveness contract (GET /v1/<name>/health for
// every enabled subsystem) before MountAll, runs the canonical middleware
// pipeline (Recover → RequestID → Logger), and shuts down gracefully on
// SIGINT/SIGTERM.
func Listen(plugins []Plugin, enable []string) error {
	// `<binary> describe <dir>` projects instead of serving. Before LoadConfig
	// AND before BootMaster because the artifacts must be a function of the code
	// alone: both read the environment, and a route set that moved with a
	// developer's shell is a spec that cannot be a golden — see describe.go.
	if dir, ok := DescribeRequested(); ok {
		return describe(plugins, dir)
	}

	// THE KEY FIRST — before config is read, before any store opens, because cek
	// refuses to open a database without it and the first open is the one that
	// would have had to be keyed. Idempotent (sync.Once); BuildDeps calls it too,
	// for callers that skip Serve.
	BootMaster(DataDir())

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

	// The pod's writer lease — the SAME call the router makes, because the rule is
	// one rule and a process's POSITION decides what it means (internal/writerlease).
	//
	// In the fleet this process is a plugin child: the router took the lease before
	// it spawned anything and stamped the environment this process was born with,
	// so Hold recognises the parent's claim and touches nothing. Run standalone —
	// `hanzo kms` against its own volume — nothing spawned it, so it IS the root of
	// its pod and takes the lease itself.
	//
	// This used to be an acquire guarded by "am I not under a router", in the body
	// that ONLY plugins run. That aimed a single-holder lock at the siblings rather
	// than at the other pod generation, and cost api.hanzo.ai four minutes of 503
	// on 2026-08-04; the guard added afterwards stopped the deadlock but left the
	// lock in the hands of nobody, since the router does not run this code at all.
	//
	// Released by the defer after app.Shutdown has run every subsystem teardown
	// hook, which is the point at which this process holds nothing open. Unset
	// CLOUD_WRITER_LEASE ⇒ this does nothing, which is what production runs.
	releaseLease, lerr := writerlease.Hold(cfg.DataDir, writerlease.DefaultWait,
		luxlog.New("cloud").New("subsystem", "writer-lease").Info)
	if lerr != nil {
		return lerr
	}
	defer func() { _ = releaseLease() }()

	deps := BuildDeps(cfg)

	// Surface the resolved role and the writer-pin backing it, WITH the reason the
	// pin was chosen. SingleWriter is correct at replicas:1 (Kubernetes is the
	// elector); a real coordination.k8s.io Lease election is opt-in via
	// CLOUD_WRITER_LEASE + the downward API, and every incomplete configuration
	// falls back and says so here rather than pretending to elect.
	pin, pinReason := writerpin.ResolveWithReason()
	luxlog.Default().Info("HA role resolved",
		"role", cfg.Role.String(),
		"writer_pin", pin.Kind(),
		"writer_pin_reason", pinReason,
		"kms_read_only", cfg.Role.IsReader())

	// Horizontal-scale shard router. When CLOUD_PEERS names >1 pod, each org is
	// pinned to its rendezvous-hash owner pod: THIS pod is the single writer for the
	// orgs it owns (writerpin.SingleWriter is correct PER SHARD), and any other org's
	// request is forwarded to its owner. nil ⇒ single-pod (no-op middleware below).
	// This is what lifts the deployment off replicas:1 without any shared RWX volume —
	// per-pod RWO PVC + org→owner routing = one writer per tenant file. See
	// shardrouter.go.
	shardRtr := newShardRouter(cfg, luxlog.Default(), deps.LiveMembers)
	if shardRtr != nil {
		luxlog.Default().Info("shard routing ENABLED (horizontal writer scale)",
			"self", shardRtr.self, "peers", shardRtr.peerIDs(),
			"writer_pin", "single-writer-per-shard")
	}

	// Telemetry bootstrap — the ONE site, and a HOST concern: every request this
	// process serves gets a span whether or not o11y is co-resident, so the host
	// installs the tracer and meter providers itself rather than borrowing them from
	// a subsystem that may now be a separate binary. Runs BEFORE MountAll — so the
	// providers exist before ai mounts and the composition root can adopt them into
	// it (apps/ai/ai.go), and so every per-app plugin entrypoint, which shares
	// this body, installs identically. Spans leave through ONE Send: Cost-0
	// to a co-resident sink when apps/o11y is linked in, the ZAP wire when it is a
	// plugin. No-op (non-nil shutdown) when no sink/endpoint is configured. See
	// telemetry.go.
	telemetryShutdown := InstallTelemetry(context.Background(), luxlog.Default(), "hanzo-cloud")

	// Data-plane encryption posture. The KEY was installed by BootMaster at the top
	// of this function (BuildDeps logs where it resolved from); this only READS
	// the outcome. Installing a key here — which is what used to happen — is after
	// BuildDeps has already opened a store, and the first open is the one that
	// would have had to be keyed. Every database is derived from that master, so a
	// process without one opens nothing rather than writing plaintext.
	if cek.HasMaster() {
		luxlog.Default().Info("data-plane encryption ACTIVE (every database keyed from the master, at rest)")
	} else {
		luxlog.Default().Warn("data-plane encryption posture: no usable key → store opens fail closed")
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
	// The per-caller half of this binary's MCP door, from the composition root
	// (Plugin.Door). Nil for every app but the tool plane, which is the only one
	// whose tools are ROWS — an org's connectors, skills, agents and the servers
	// it enabled — and therefore the only one that cannot be projected at build
	// time. The build-time half is unaffected: it is still the typed-op array,
	// still rendered once, still served as bytes.
	source, err := door(plugins)
	if err != nil {
		return err
	}

	// The app, and everything a Hanzo program carries, from the ONE constructor —
	// see app.go. This body used to build it inline, which is why the o11y binary
	// could assemble a different one by hand and be missing six of these without
	// anything saying so. procName is what this process calls itself: its single
	// subsystem's name, or "cloud" when it carries several.
	app := App(procName(plugins), cfg, deps, source)

	// Console identity = the ONE validated principal, not the embedded casibase account
	// model. When a principal is present, /v1/get-account reflects it so the operator
	// UI's SuperAdmin gate sees the same owner+isAdmin every /v1/admin/* route already
	// authorizes on (a PKCE session is not a casibase session — without this the UI
	// bounced to login despite valid admin API access). No principal → casibase path
	// unchanged. Runs BEFORE MountAll's casibase mount.
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
	auditRec, err := buildAuditRecorder(cfg, luxlog.Default(), procName(plugins))
	if err != nil {
		return fmt.Errorf("audit: %w", err)
	}
	deps.Audit = auditRec
	app.Use(AuditTrail(auditRec))

	// The stage (HIP-0139 §8): a capability that is not ga answers 404 on its own
	// prefixes to an org that does not hold the flag named for it. Nothing for the
	// seventy-nine ga rows, which compose a nil — see stage.go.
	//
	// AFTER AuditTrail, so a refusal is in the tamper-evident trail like every
	// other one, and BEFORE the limiter, the abuse sensor and the two funding gates,
	// because a request to a capability this org cannot reach must not spend its
	// rate budget or touch its balance. It is the first thing asked about the
	// ADDRESS once identity is settled, which is the right order: whether a thing
	// exists for you comes before what it would cost you.
	for _, p := range plugins {
		app.Use(Stage(p.Name))
	}

	// Per-scope rate limit (issue #70). Runs AFTER identity (needs the validated
	// principal to key on org/project/service) and AFTER audit (so a 429 is
	// recorded), and BEFORE BillingGate so an over-rate request is rejected before
	// any balance/spend-cap work. Fail-open when commerce is unreachable — a
	// rate-limit outage never blocks paid traffic. Also honors the /v1/gateway
	// per-org OrgRPM (deps.GatewayPolicy): the runtime-mutable per-org ceiling,
	// most-restrictive-wins with any commerce-configured limit. No-op only when
	// BOTH sources are absent.
	app.Use(ScopeRateLimit(deps.Metering, deps.GatewayPolicy))

	// Lifecycle defense (middleware_abuse.go). Runs AFTER ScopeRateLimit so plain
	// over-rate traffic is already 429'd and never reaches the scorer, INSIDE
	// AuditTrail so a refusal lands in the tamper-evident trail without a second
	// write, and BEFORE the two funding gates so an abusive request cannot consume
	// a balance. It keys on the CREDENTIAL, which neither limiter above can see —
	// a stolen key inside its org's normal ceiling is invisible to both. SHADOW per
	// org by default: it senses and reports, and enforces nothing until an operator
	// arms that org at PUT /v1/gateway/config.
	app.Use(AbuseGate(deps, deps.Traffic))

	// Billing gate. Sits at the (future) Auth position — after identity is
	// established by Recover/RequestID/Logger and before any subsystem mounts —
	// so every priced route is balance-gated once, at the edge, fail-closed.
	// No-op when metering is unconfigured (deps.Metering not Enabled()), so
	// it is always wired unconditionally. DefaultPrice keeps self-metering
	// subsystems (notably /v1/ai/*) at 0 to avoid double-billing.
	app.Use(BillingGate(deps.Metering, DefaultPrice))

	// Spend gate — the ONE "may this principal spend?" enforcement point. Runs AFTER
	// the gateway (so it keys on the asserted principal + owner header, never a
	// client X-Org-Id) and beside BillingGate, BEFORE MountAll so it precedes every
	// subsystem /v1/<name>/* wildcard.
	//
	// It replaces routers.Paywall, which asked only "does this org hold a paid PLAN?".
	// That question has no credit leg, so enabling it would have 402'd every prepaid
	// customer — which is why it was mounted for months and never turned on. SpendGate
	// admits on subscription OR prepaid credit (cloud.Stand), read at the wallet address
	// the DEBIT writes, and applies to the billable paths only (cloud.Billable) — LLM
	// and non-LLM resource trees alike.
	//
	// DARK by default and it must stay dark until a starter-credit path exists: there is
	// none in this binary today, so enforcing would 402 every new signup on day one. See
	// middleware_spend.go.
	//
	// Enforcement is read ONLY from the platform switch — there is no env var and no
	// Config field. It used to be `cfg.PaywallEnforced || Switch(...)`, a boot-time OR
	// that could not be turned OFF from the cockpit: the kill switch was defeated by the
	// very variable that armed the gate. clients/entitlements now registers
	// paywall_enforced with NO Env fallback for the same reason (its own
	// TestSwitchesDefaultOff pins that, and was RED against the old registration).
	// One reader, one answer, and the kill switch always wins.
	app.Use(SpendGate(deps.Commerce))

	// The same two gates, asked about the OPERATION instead of the request.
	//
	// Both of the above are middleware, and middleware reads the path the TRANSPORT
	// carried. That is the operation only over plain REST: an MCP tools/call arrives
	// as POST /mcp and a plane call as POST /.well-known/zip/op/<name>, and neither
	// names a declared surface — so both priced at zero and required no standing,
	// however the operation inside them was declared. Toll asks DefaultPrice and
	// Billable about op.Method and op.Path at zip's op-invoke client, which every
	// projection of a typed handler funnels through, so an operation costs the same
	// whichever door it came in by. It stands down for a request whose own path names
	// a declared surface, because that is exactly when the two gates above have
	// already answered — one operation, one answer. See toll.go.
	//
	// The peer sibling is a SEPARATE zip.App with its own hook (peer.go), so it gets
	// the gate explicitly. It binds no socket until something registers a peer op, so
	// this costs a struct today and closes the client the day one appears.
	app.Authorize(Toll(deps.Metering, deps.Commerce))
	app.Peer().Authorize(Toll(deps.Metering, deps.Commerce))

	// A typed op cannot write a response body — zip stamps the op's declared status
	// over a hand-written one — so it refuses through an error, and this is what turns
	// that error back into the money wire's own bytes. It is mounted at the root
	// because Toll refuses ops fleet-wide; the four subsystems that mount it on their
	// own groups are inside this one and unaffected.
	app.Use(DenyEnvelope())

	// HIP-0106 liveness contract: every enabled subsystem answers
	// GET /v1/<name>/health uniformly, registered at the compose root before
	// MountAll so it precedes subsystem /v1/<n>/* wildcards.
	//
	// A subsystem that owns its health (OwnsHealth, e.g. kms/paas/s3) serves its
	// OWN fail-closed /v1/<name>/health in Mount; skip it here so this always-ok
	// route never shadows the real probe.
	for _, p := range plugins {
		if !cfg.Enabled(p.Name) || p.OwnsHealth {
			continue
		}
		name := p.Name
		app.Get("/v1/"+name+"/health", func(c *zip.Ctx) error {
			return c.JSON(200, map[string]string{"service": name, "status": "ok"})
		})
	}

	// The binary's own health, and the ONE place it admits a plane is dead.
	//
	// A subsystem that mounts fail-closed (commerce with an unusable KV_URL, team
	// in degraded mode) keeps the process up and answers 503 on its own routes.
	// From outside that is indistinguishable from a subsystem which was simply
	// never enabled — which is how a dead revenue plane once shipped behind a green
	// pod and a green gate. Degradations() makes the difference visible.
	//
	// It answers 200 EVEN WHEN DEGRADED, on purpose. This doubles as the container
	// probe, and taking a pod out of rotation because one plane of many is broken
	// would turn a partial outage into a total one — the opposite of what
	// fail-closed mounting is for. The release smoke reads the `degraded` field and
	// refuses the image; that is the right place to say no, before it ships.
	app.Get("/v1/health", func(c *zip.Ctx) error {
		if d := Degradations(); len(d) > 0 {
			b := healthBody("degraded")
			b["degraded"] = d
			return c.JSON(200, b)
		}
		return c.JSON(200, healthBody("ok"))
	})

	if err := MountAll(app, plugins, cfg, deps); err != nil {
		return fmt.Errorf("mount: %w", err)
	}

	// Browser-facing ZAP RPC plane. console (@hanzo/gui + @zap-proto/web)
	// reaches the SAME /v1 handlers over a WebSocket carrying binary ZAP frames
	// — no second copy of any business logic: each call is replayed in-process
	// through this Fiber app (see zapface). Mounted AFTER MountAll so every /v1
	// route exists before the dispatcher captures the app.
	app.Get("/zap", zapface.Handler(app.Fiber(), zapface.Options{
		OriginPatterns: cfg.ZAPWebOrigins,
		Logger:         luxlog.Default(),
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
		openapi.Server{URL: "https://" + cfg.Domain},
	)

	// GET/POST /v1/graphql — the same route table as a graph. GET renders the
	// schema every typed op describes; POST runs a query against it. Mounted here
	// for the same reason the document above is: after MountAll, so it covers a
	// complete table.
	//
	// graphql, not graph: /v1/graph is the knowledge graph's address — assertions,
	// nodes and neighbours — and the two mean different things by the word. This
	// one is the query language over the ops.
	//
	// A field resolves through the op's own contract — validate, authorize, the
	// handler — so it reaches nothing the MCP door does not already reach. Ops
	// whose routed path carries a gated group keep their own authority check for
	// exactly that reason: the group orders the refusal, the op decides it.
	//
	// THIS PROCESS'S OPS, which in a plugin child means that child's own — the same
	// scope its own OpenAPI document above describes, and the reason both are
	// mounted here rather than in the host. The FLEET's schema at this address is
	// the host's answer (cmd/cloud, fleet.MountGraph): a child cannot see past
	// itself, and for a long time nobody claimed the address, so the app holding
	// the /v1 remainder answered the fleet's callers with a schema of its own
	// registry — one field, honestly rendered, about the wrong thing.
	app.MountGraph(openapi.GraphPath)

	// Unified console UI — the SAME binary serves the @hanzo/gui console at the web
	// root. Mounted LAST, after every /v1 route + the /zap plane + the health
	// contract, so Fiber's in-order matching gives the API precedence: real API
	// routes win, and only paths that match nothing else fall through to the SPA
	// (index.html for client-side deep links). The API namespace (/v1, /zap,
	// /healthz…) never renders as HTML — an unmatched path there is a real 404.
	// Same-origin: the console calls /v1 on its own host, so the session cookie is
	// first-party and no second origin / CORS is involved.
	//
	// The BYTES are a published site release now, not an embed (see webui/release).
	// Here — and ONLY here — a failure to load them is reported rather than fatal,
	// and the reason is a cycle rather than a preference: this body is what every
	// per-app CHILD runs, and a child cannot bootstrap the console. Loading it means
	// asking `projects` for the active release; `projects` is lazy, so only the host
	// can start it; and the kms broker — which every child, including projects,
	// takes its data-plane key from — comes up FIRST. A child that made the console
	// a precondition of its own boot would be waiting on an app that cannot exist
	// yet. So the console loads when it can, and when it cannot this process serves
	// none: the terminal handler still keeps the API namespaces honest and still
	// answers the agent door, and a console path gets a 503 that says exactly this.
	// Nothing is faked. In the fleet nothing is lost either — every path a child
	// receives is under one of its own /v1 prefixes, so a child's catch-all never
	// serves the console to a browser; the front door (cmd/cloud) owns "/", and
	// THERE the release is required.
	consoleSrc, consoleErr := release.Load(context.Background(), release.ConfigFromEnv(), luxlog.Default())
	if consoleErr != nil {
		luxlog.Default().Warn("console: no release mounted — this process serves no console UI", "err", consoleErr)
	}
	// Mounting is conditional on the load, which is what the paragraph above
	// describes: a process that could not resolve a release serves no console
	// rather than refusing to serve at all. Mounting an empty release errors, so
	// doing it unconditionally turned "serves none" into "starts none" — and the
	// API, the agent door and every /v1 route went down with a browser bundle
	// nothing headless asks for.
	if consoleErr == nil {
		if err := webui.Mount(app, release.FS(consoleSrc)); err != nil {
			return fmt.Errorf("console: %w", err)
		}
	}

	// This process's AGENT DOOR on that same plane, before the sockets bind, so a
	// caller that resolves one is answered by a door that is already there. It is
	// the same door the edge serves, at the address the fleet's own callers use:
	// see Door, which is also where the reason it cannot be the edge's is written.
	Door(app)

	// Internal plane: this app's typed ops over ZAP on its canonical unix socket
	// (plane.go). Served for every mounted app name — the ops declared during
	// Mount are live by now — so zip.DialApp(app) resolving a socket always means
	// "the app is up", and an up app answering 404 for an op means version skew:
	// two different, diagnosable facts.
	for _, p := range plugins {
		if p.Name == "" {
			continue
		}
		if stop, err := ServePlane(p.Name, luxlog.Default()); err != nil {
			luxlog.Default().Warn("plane: socket not served", "app", p.Name, "err", err)
		} else {
			defer func() { _ = stop() }()
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Keep the console current with what was published: a `hanzo sites publish`
	// reaches this process on the next poll, with no build and no restart. Nothing
	// to watch when this process mounted no release (see the mount above).
	if consoleSrc != nil {
		go consoleSrc.Watch(ctx)
	}

	// Durable ingest: embed the ONE tasks engine in-process + inject the per-org dialer
	// into ai (long github/crawl/s3 ingests run as durable workflows; upload stays
	// inline). Fail-soft — inline fallback if the engine can't start. See durable.go.
	installDurableIngest(ctx, deps, procName(plugins))

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

	addrs, ops := listenOn(cfg)

	listenErr := make(chan error, 1)
	if ops == "" {
		// Plugin child (see listenOn): the ops port belongs to the host, which
		// answers liveness for the whole fleet while its children are still cold.
		luxlog.Default().Info("listening as plugin", "addr", addrs[0], "enabled", cfg.Enable, "brand", cfg.Brand)
	} else {
		luxlog.Default().Info("listening",
			"http", cfg.ListenAddr,
			"zap", cfg.ZAPListenAddr,
			"enabled", cfg.Enable,
			"brand", cfg.Brand,
			"domain", cfg.Domain,
		)
		go func() {
			luxlog.Default().Info("health listening", "addr", ops)
			if err := healthSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				listenErr <- fmt.Errorf("health listen: %w", err)
			}
		}()
	}
	go func() { listenErr <- app.Listen(addrs...) }()

	select {
	case <-ctx.Done():
		luxlog.Default().Info("shutdown requested")
		// Graceful drain: go NotReady so peers re-elect this pod's orgs to live successors
		// (each hydrates the latest fenced snapshot, M3) BEFORE we stop serving, then pause
		// for that to propagate through the membership refresh. Only when sharding is active
		// — a single-pod deployment has no successor, so it drains immediately and relies on
		// the final ship (CloseAll) below. In-flight requests drain in app.ShutdownWithContext.
		SetDraining()
		if shardRtr != nil {
			luxlog.Default().Info("draining: NotReady, waiting for peers to re-elect owned orgs", "grace", shardDrainGrace)
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

// listenOn is the ONE decision about where this process serves: the addresses
// for the app, and the ops port for the liveness/metrics contract — empty when
// this process must not bind one.
//
// A host that composed this binary as a plugin started it with a private unix
// socket in ZIP_ADDR and is blocked in zip's waitListening until that socket
// accepts; binding cfg's fixed ports instead means the host never sees the
// child come up, kills it as failed, and the mounted prefix 502s on its first
// request. Every plugin in a fleet is handed the same cfg, so they would also
// fight over one :8080/:9653/:9090 and all but the first would die on "address
// already in use". zip.Addr is the whole plugin side of that contract and this
// is the one place cloud honours it, which is what makes every generated
// cmd/<app> binary a valid plugin without a line of its own.
func listenOn(cfg *Config) (addrs []string, ops string) {
	// BIND THE SHARED RUNTIME DIR FIRST. zip.Addr("") answers with the socket this
	// process will listen on, and it derives that from ZIP_RUNTIME_DIR — which nothing
	// had set this early, so every plugin bound a PRIVATE temp path
	// (/tmp/zip-commerce-*/commerce.sock) while every caller dialed the shared one
	// (/var/lib/cloud/run/commerce.sock). The socket file at the shared path was a
	// stale leftover, so the dial did not fail loudly as "missing" — it failed as
	// "connection refused", which reads like the callee is down rather than absent.
	//
	// Cost: every cross-process plane call was unreachable. For the money ops that is
	// fail-CLOSED, so the AI balance gate could not verify a balance and EVERY
	// completion answered 503 — chat, copilot and documents — on a pod whose ledger
	// was healthy. bindRuntimeDir is idempotent and honours an externally-set
	// ZIP_RUNTIME_DIR, so this only fills in the default the plane already assumes.
	bindRuntimeDir()
	if sock := zip.Addr(""); sock != "" {
		// ONE address, and the plain HTTP sibling beside it is zip's to make.
		//
		// A plugin answers two kinds of caller. The socket carries ZAP: one framed
		// request, one framed reply, which is every typed op and every mounted
		// route. An UPGRADED connection is neither — after the handshake there are
		// no more requests, only bytes — so a websocket cannot cross that framing,
		// and a terminal's first frame reaches an HTTP header parser as nonsense.
		//
		// So a plugin listens a second time in plain HTTP and the host relays the
		// upgrade there. zip already does exactly that: plain(addr) is addr+".http"
		// for a unix address and plainSibling serves it (transport.go:375). Naming
		// it here as well asked for the same listener twice — the second bind of
		// <sock>.http fails "address already in use", and the explicit entry is
		// itself a unix address, so zip derived <sock>.http.http from it too.
		//
		// A plugin that cannot bind exits before listening, and the ones that hold
		// the bus go first: pubsub, then kafka and amqp fail closed behind it,
		// then the host exits. So the derivation has one home, which is where
		// it always was.
		return []string{sock}, ""
	}
	// ONE app, TWO transports here, both serving the identical route surface so
	// /v1/* answers over either and WS/SSE keep working on the HTTP one:
	//
	//	:9653  — ZAP over TCP, the machine transport across hosts
	//	:8080  — HTTP, the edge/browser leg (and WS + SSE)
	//
	// The app's canonical UNIX socket is deliberately absent HERE, and belongs to
	// the plane app instead (plane.go): a typed op rides every transport its app
	// listens on, so registering the internal ops on the edge-facing app would put
	// the gate, the meter and the secret reads on :8080. Two apps, two address
	// sets, and no path from the edge to an op that was never registered on it.
	return []string{cfg.ZAPListenAddr, "http://" + cfg.ListenAddr}, cfg.HealthListenAddr
}

// healthMux is the liveness/readiness + metrics contract on the ops port
// procName names the process by what it serves. A single-app binary is that app; a
// host that mounts several is "cloud". It exists so per-process resources (the
// durable engine's port and store) can say whose they are instead of contending for
// one global name.
// procName is what this program calls itself: the name in its diagnostics, and
// the AppName its telemetry ships under — which is the `service` a per-app logs
// view keys on, and the ZAP node identity its exporter connects with.
//
// A CO-RESIDENT app routes no prefix of its own (manifest.Coresident): it mounts
// as middleware on a sibling's router, so it cannot be its own process and rides
// inside the binary of the app it wraps. It is a passenger, and a passenger does
// not make this program the fused host — the one ROUTED app it travels with is
// what this program IS.
//
// COUNTING plugins said otherwise, and plugin/ai is the only main in the fleet
// that carries a passenger (zen), so the ai process alone called itself "cloud".
// Both costs were measured on 866M live log rows:
//
//   - Its records shipped with AppName "cloud", so they stored under service
//     `cloud`, and console's Models logs view — which asks for service `ai` —
//     was empty for as long as it has existed, while ai served every inference
//     request. `service='ai'` held 46 rows ever, all /health lines from a pod
//     retired on 2026-08-09.
//   - Worse, its exporter claimed node identity `o11y-cloud-logs`, which the
//     front door already held. The wire admits ONE connection per identity, so
//     the second is refused: the records were dropped, not merely mislabelled.
//
// ai and zen were the only two of ~40 subsystems with zero log rows; every
// sibling that passes a single plugin was correct all along. So this is not a
// new rule, it is the existing one stated over what a plugin IS rather than over
// how many were passed.
func procName(plugins []Plugin) string {
	name := ""
	for _, p := range plugins {
		if manifest.Coresident(p.Name) {
			continue
		}
		if name != "" {
			// Two apps that both route, in one process: that is the fused host,
			// and no single app's name would be honest for it.
			return "cloud"
		}
		name = p.Name
	}
	if name == "" {
		return "cloud"
	}
	return name
}

// healthBody is THE health payload — the one shape every liveness surface in
// this process answers with, so a caller gets the same facts wherever it asks
// and no listener can be stamped while its siblings stay mute. /v1/health (the
// product API, which is what api.hanzo.ai serves) and /healthz, /readyz,
// /health on the ops listener all build their body here.
//
// `revision` rides on the EXISTING payload rather than on a /v1/version of its
// own: a second route is a second thing to discover, to route, to exempt from
// auth and to remember exists. "Which commit is serving me" is the same question
// as "are you healthy" asked one field further, and health is already
// unauthenticated, already probed, already in every runbook.
func healthBody(status string) map[string]any {
	return map[string]any{"status": status, "revision": Revision()}
}

// writeHealth is healthBody for the stdlib ops listener — the same map, encoded.
// The bodies here were fixed byte strings, which is precisely how a payload comes
// to carry a new field on one surface and not another: a literal cannot pick one
// up.
func writeHealth(w http.ResponseWriter, code int, status string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(healthBody(status))
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
		writeHealth(w, http.StatusOK, "ok")
	}
	mux.HandleFunc("/healthz", ok) // liveness: stays 200 while draining (finish the drain).
	// readiness: 503 once draining so K8s marks the pod NotReady — removed from endpoints
	// AND from every peer's writer election — before it stops serving (graceful handoff).
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		if Draining() {
			writeHealth(w, http.StatusServiceUnavailable, "draining")
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
