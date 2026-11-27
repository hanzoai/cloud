package host

import (
	cloud "github.com/hanzoai/cloud/sdk"

	luxlog "github.com/luxfi/log"

	"github.com/hanzoai/cloud/apps/sites"
	"github.com/hanzoai/cloud/internal/markdown"
	"github.com/hanzoai/cloud/manifest"
	"github.com/hanzoai/cloud/preview"
	"github.com/hanzoai/cloud/sdk/brand"
	"github.com/zap-proto/zip"
	"github.com/zap-proto/zip/middleware"
)

// buildApp is the host's [cloud.App]: it returns an app carrying everything a Hanzo program must carry, in the one
// order those parts are correct in. It is the only way to obtain one: a program
// mounts its subsystem on what it gets back and never builds a zip.App itself.
//
// The point is what a caller no longer has the opportunity to forget. Identity is
// not an option a program passes, it is a property of the value it receives, so a
// program is either holding an app that identifies its callers or it is holding
// nothing. Every other member here was equally forgettable and was equally
// forgotten: the o11y binary assembled its own app and reached production with no
// panic recovery, no request id, no response-header posture, no tracing, no
// request log and no typed-op enrichment — a shape nobody chose and nobody could
// see, because there was nothing to compare it against.
//
// WHERE THE EDGE IS. Production runs ingress → gateway → the host
// (cmd/cloud) → this program. The gateway is the public edge and owns rate
// limiting for the internet. The host installs no middleware of its own —
// it routes, serves the console, threads operator flags and scopes credentials —
// so a program built here is its OWN edge and defends itself. That is why the
// browser and flood defenses are here rather than borrowed from a parent. A
// program reached over the plane socket instead trusts what its host asserted:
// the kernel answers which process is calling, and the boundary's findings travel
// with the request.
//
// name is what the program calls itself in a diagnostic. tools is the per-caller
// half of this program's agent MCP server — the tools that exist because of WHO
// is asking, which only a program holding a subsystem list can declare; everyone
// else passes nil and offers none. The server itself is not optional either way:
// see [cloud.CallerTools].
func buildApp(name string, cfg *cloud.Config, deps cloud.Deps, tools zip.Source) *zip.App {
	app := zip.New(zip.Config{
		AppName:        name,
		Logger:         luxlog.Default(),
		ReadBufferSize: cfg.ReadBufferSize,
		BodyLimit:      cfg.BodyLimit,
		MCP:            zip.MCPConfig{Source: cloud.CallerTools(tools)},
		// Cloud's refusal renderer, in place of zip's default — which reads only a
		// *zip.HTTPError and answers 500 for everything else, so a propagated 402
		// or 403 reached the console as a dead card. See errmap.go.
		ErrorHandler: cloud.ErrorHandler,
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
	//  5. SanitizeIdentity — establish a VALIDATED principal (see Identify)
	app.Use(middleware.Recover())
	app.Use(middleware.RequestID())
	// This program's own 500s are filed with Sentinel under the deployment's org
	// (faults.go, reported from ErrorHandler).
	ReportFaultsAs(cfg.Brand, name)

	// Production response-header posture — the Stripe/Cloudflare/GitHub-grade
	// signals plus a security floor, from ONE home in the framework so every
	// service inherits the same wire posture. Registered right after RequestID
	// (before the site edge and the business chain) so its headers ride out on
	// every response: success, error, 404, AND the public-site static bytes.
	//   - Server: the white-label brand of the request Host (brand.ForHostOK) — a
	//     lux/zoo caller is never served "hanzo" and no response leaks the
	//     framework name; an unmatched Host falls back to this deployment's own
	//     brand (cfg.Brand), never a framework/single-brand default.
	//   - X-Api-Version: the build version (brand-neutral key) for support correlation.
	//   - HSTS + nosniff: the always-safe security floor (no X-Frame-Options/CSP
	//     here — the console SPA owns its own framing rules).
	// X-Request-Id stays owned by RequestID above; the two compose.
	app.Use(middleware.ProductionHeaders(middleware.ProductionHeadersConfig{
		Brand:   func(host string) string { b, _ := brand.ForHostOK(host); return b },
		Neutral: cfg.Brand,
		Version: cfg.Version,
		HSTS:    true,
	}))

	// Markdown content negotiation. Registered here — outermost of the business
	// chain, just inside Recover/RequestID — so its post-Continue transform sees
	// the FINAL response body and re-serializes it via zap-proto/md when the
	// caller asked for markdown (Accept: text/markdown or ?format=md). JSON stays
	// the default for machines; cfg.MarkdownDefaultPrefixes lets designated
	// agent endpoints (/v1/code/, /v1/agent/…) default to markdown. Touches NO
	// handler and fails safe (a render error leaves the JSON intact). See
	// middleware_markdown.go.
	app.Use(markdown.Negotiation(cfg.MarkdownDefaultPrefixes))

	// Request tracing. Sits right after RequestID (so the span carries the
	// request_id) and BEFORE identity/audit/billing/handlers, so the whole
	// authenticated pipeline nests under one span and the span CONTEXT it writes
	// via SetContext parents every downstream span (agent.run → agent.step →
	// chat) into a single trace. Spans ship over the SAME global provider installed
	// by InstallTelemetry, landing in hanzoai/datastore.
	// Health/readiness/metrics + non-/v1 paths are skipped (see traceable). See
	// middleware_tracing.go.
	app.Use(cloud.TracingMiddleware())

	// No request logger is installed here: zip reports every request natively —
	// method, path, status, duration, trace and span, and the caller when the
	// environment parked one — through the app's own logger. A second line per
	// request would say less and cost the same.

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
	// The edge asks the app that owns the store when it is not in this process,
	// which in production is always: the pod boots ~25 single-app processes, so
	// the registry projects.Use writes is nil here. Co-resident still wins with
	// no hop — currentResolver prefers the in-process one.
	sites.SetFallbackResolver(planeSites{})
	app.Use(sites.New(sites.ConfigFromEnv(cfg.Domain), luxlog.Default()).Middleware())
	// A port in a sandbox is an origin of its own (apps/sandbox/preview): its
	// requests are rewritten onto the sandbox app's route.
	app.Use(preview.Middleware())
	// Inbound PayPal webhook deliveries registered at POST /v1/webhook/paypal
	// route onto commerce's sessionless verifier before identity and spend gates.
	app.Use(manifest.ProviderWebhookMiddleware())

	// Edge policy — the role this program absorbs because nothing in front of it
	// installs middleware. Runs BEFORE identity by design:
	//   - EdgeCORS answers the browser OPTIONS preflight (which carries no
	//     credentials) and short-circuits it, so a preflight never reaches auth.
	//     No-op unless CLOUD_CORS_ORIGINS is set (the shared ingress owns CORS on
	//     the recommended rollout — enabling both would double the ACAO header).
	//   - EdgeRateLimit caps an ANONYMOUS per-IP flood before the JWKS/validate/
	//     downstream work it would trigger — the one gap ScopeRateLimit (which keys
	//     on the validated org, below) structurally can't see. A request presenting
	//     a credential meets only a backstop here, and the identity boundary
	//     finishes the per-IP charge if the credential turns out to be nobody.
	//     Keyed on the public client IP; in-cluster direct callers (no
	//     X-Forwarded-For) are exempt, matching the standalone gateway's
	//     public-only scope. See middleware_edge.go.
	//
	// RATE LIMIT FIRST. EdgeCORS now resolves an unknown origin against the site-host
	// store, which in production is a plane hop, and the Origin header is chosen by
	// the caller — so an attacker rotating a fresh hostname per request would defeat
	// the answer cache and turn each inbound request into an internal one. The
	// per-IP counter is a map increment and bounds that structurally, with no second
	// mechanism to tune. The cost is that a flood of PREFLIGHTS is capped too, which
	// is the correct answer to a flood of preflights.
	// ONE BOUNDARY, ASKED TWICE: the edge asks it what a credential costs to check
	// before CORS runs, and Identify checks it. Built once so both read one cache.
	boundary := cloud.BoundaryOf(cfg)
	policy := EdgePolicy(cfg)
	app.Use(EdgeRateLimit(policy, boundary.Glance))
	app.Use(EdgeCORS(policy, cfg.IdentityHosts))
	// ONE VERDICT: for an origin EdgeCORS's allowlist admits, the Origin header is
	// taken off the request before any child — the embedded IAM, ai — can form a
	// second opinion about it. See EdgeOrigin.
	app.Use(EdgeOrigin())
	// What a cache may keep of every GET and HEAD, decided once (middleware_cache.go):
	// a credentialed answer is private, an anonymous one on a path the edge policy
	// gives a TTL is public with an ETag. Inside CORS and markdown, so the Vary
	// each of them appends is on the answer it describes.
	app.Use(EdgeCache(policy))

	cloud.IdentifyWith(app, boundary)

	// The rule, for the reason identity is here: a program is either holding an app
	// whose operations answer to it or it is holding nothing. It asks two questions
	// — was this change asked for, and does this operation cost — in that order and
	// in one place (Rule, intent.go).
	//
	// It used to be two lines in Listen, and one of them named an app with no
	// operations in it — App.Peer(), which nothing in this estate registers on —
	// while the test that kept them read the composition root AS TEXT and counted
	// them. Counting a line of source says a call is written, never that it ran, so
	// the arrangement could report a rule over a surface it had never once been
	// asked about. Constructed with the rule, the program's app cannot exist without
	// it, and a test that builds one and drives an operation measures the fact
	// instead of reading it.
	//
	// The peer sibling takes it too. It is a SEPARATE zip.App with its own rule
	// (zip peer.go), it holds no operation today, and arming it at construction is
	// what keeps it from acquiring one that is outside the rule. It binds no socket
	// until something registers on it, so this costs a struct.
	//
	// THE INTERNAL PLANE IS NOT THIS APP and is deliberately outside the rule. It is
	// a third zip.App (peer.go) listening on the pod's own socket: no browser and
	// no network client can address it, and its operations are the implementation of
	// operations the edge already priced and already answered standing for. Pricing
	// the inner hop would bill one act twice, and gating it on standing would refuse
	// the machinery that computes standing. That its addresses are outside the
	// priced surface is asked at every bind rather than assumed — peer.go, unpriced.
	rule := cloud.Rule(deps.Metering, deps.Commerce)
	app.Authorize(rule)
	app.Peer().Authorize(rule)
	return app
}
