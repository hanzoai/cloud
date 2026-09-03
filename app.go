package cloud

import (
	"context"
	"encoding/json"
	"fmt"

	luxlog "github.com/luxfi/log"

	"github.com/hanzoai/cloud/apps/sites"
	"github.com/zap-proto/zip"
	"github.com/zap-proto/zip/middleware"
)

// App returns an app carrying everything a Hanzo program must carry, in the one
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
// see [callerTools].
func App(name string, cfg *Config, deps Deps, tools zip.Source) *zip.App {
	app := zip.New(zip.Config{
		AppName:        name,
		Logger:         luxlog.Default(),
		ReadBufferSize: cfg.ReadBufferSize,
		BodyLimit:      cfg.BodyLimit,
		MCP:            zip.MCPConfig{Source: callerTools(tools)},
		// Cloud's refusal renderer, in place of zip's default — which reads only a
		// *zip.HTTPError and answers 500 for everything else, so a propagated 402
		// or 403 reached the console as a dead card. See errmap.go.
		ErrorHandler: ErrorHandler,
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
	// by InstallTelemetry, landing in hanzoai/datastore.
	// Health/readiness/metrics + non-/v1 paths are skipped (see traceable). See
	// middleware_tracing.go.
	app.Use(TracingMiddleware())

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

	// Edge policy — the role this program absorbs because nothing in front of it
	// installs middleware. Runs BEFORE identity by design:
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
	//
	// RATE LIMIT FIRST. EdgeCORS now resolves an unknown origin against the site-host
	// store, which in production is a plane hop, and the Origin header is chosen by
	// the caller — so an attacker rotating a fresh hostname per request would defeat
	// the answer cache and turn each inbound request into an internal one. The
	// per-IP counter is a map increment and bounds that structurally, with no second
	// mechanism to tune. The cost is that a flood of PREFLIGHTS is capped too, which
	// is the correct answer to a flood of preflights.
	app.Use(EdgeRateLimit(deps.GatewayPolicy))
	app.Use(EdgeCORS(deps.GatewayPolicy))
	// ONE VERDICT, AND THIS IS WHERE IT IS MADE SINGULAR. EdgeCORS above is the CORS
	// authority for this binary; a child that also inspects Origin would answer the
	// same question a second time, and two Access-Control-Allow-Origin headers break
	// every preflight that reads them. So for an origin the authority ADMITTED, the
	// header is taken off the request before any child sees it — a child with no
	// Origin to read cannot form an opinion about one.
	//
	// SCOPED AND CONDITIONAL, both deliberately:
	//
	//   - only for origins already admitted, by the SAME predicate EdgeCORS used
	//     (CORSAllows — same instance, same cache), so the two cannot disagree. A
	//     denied origin keeps its header and is refused downstream exactly as before:
	//     this changes the allow path only.
	//   - only for non-upgrade requests. A socket has no preflight and no ACAO, and
	//     the origin check that guards cross-site WebSocket hijacking admits an EMPTY
	//     Origin to let CLI clients in — clearing it there would fail OPEN.
	//
	// It sits here rather than inside the child because the child is not ours to
	// reach into: this used to be an InsertFilter on ai's own router, which stopped
	// existing when ai moved to zip. Registered before the children are included, a
	// parent's middleware is what they see first — the same position, said in the one
	// composition verb zip has.
	app.Use(zip.H(func(c *zip.Ctx) error {
		origin := c.Header("Origin")
		if origin != "" && c.Header("Upgrade") == "" && CORSAllows(c.Context(), origin) {
			c.Fiber().Request().Header.Del("Origin")
		}
		return c.Continue()
	}))

	Identify(app, cfg)

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
	// a third zip.App (plane.go) listening on the pod's own socket: no browser and
	// no network client can address it, and its operations are the implementation of
	// operations the edge already priced and already answered standing for. Pricing
	// the inner hop would bill one act twice, and gating it on standing would refuse
	// the machinery that computes standing. That its addresses are outside the
	// priced surface is asked at every bind rather than assumed — plane.go, unpriced.
	rule := Rule(deps.Metering, deps.Commerce)
	app.Authorize(rule)
	app.Peer().Authorize(rule)
	return app
}

// callerTools is this program's per-caller tool half — and stating it, rather
// than leaving it nil, is what makes the agent MCP server UNCONDITIONAL.
//
// zip mounts the server only for an app that has something to project: a typed op,
// a composed plugin's catalogue, or a per-caller Source. With all three absent it
// returns before registering the route at all (zip@v1.25.1 mcp.go:99). That is
// the right default for a program nobody interrogates, and the wrong one for
// every program built here, because the fleet's MCP server ASKS EVERY COMPOSED
// SUBSYSTEM on each tools/list (surface.Ask). A subsystem whose routes are all raw
// — a reverse proxy, or a surface owned by another module — projects no typed op,
// so nothing claimed POST /mcp in its process, so the ask fell through to the
// console's terminal handler and was answered with the signpost that is correct
// only on the host: 308 → /v1/mcp, an address a child does not serve
// (webui/mcp.go:44). Thirty of the fleet's subsystems — the whole of exec, tasks,
// agent, ask, websearch, crawl, index, kms, billing, platform and twenty more —
// were reported UNREACHABLE that way while every one of them was up, healthy and
// serving its REST surface.
//
// A server whose registry is empty answers {"tools":[]}, and that is a REAL answer:
// "asked, and serves nothing" is a different fact from "could not be asked", and
// keeping those two apart is the whole of package fleet. Its hanzo.ai/unavailable
// list means nothing while a healthy subsystem has no way to say the first one.
//
// nil in, empty out. A program that declares no Plugin.Offer still HAS a
// per-caller half; it simply holds no tools. It is consulted once per tools/list
// that names an org, answers nothing, and zip then returns the same pre-rendered
// bytes it always did — the memcpy that makes tools/list free is untouched (zip
// listTools: len(mine) == 0 ⇒ the build-time array, verbatim).
func callerTools(declared zip.Source) zip.Source {
	if declared != nil {
		return declared
	}
	return noCallerTools{}
}

// noCallerTools is the per-caller half of a program that declares none: no tools
// exist because of who is asking, and a name nobody projected is nobody's.
//
// Its Call is reached only for a name the build-time catalogue did not claim, and
// it answers with the same sentence zip's own miss does — the fleet's MCP server
// never routes one here (it refuses an unlisted name itself, surface/mcp.go), so
// this is the reply to a client that guessed.
type noCallerTools struct{}

func (noCallerTools) Tools(context.Context) []map[string]any { return nil }

func (noCallerTools) Call(_ context.Context, name string, _ json.RawMessage) (any, error) {
	return nil, fmt.Errorf("unknown tool: %s", name)
}

// Identify gives an app a trustworthy answer to who is calling, and makes that
// answer reachable from every route beneath it. App does this for every program,
// which is the only reason it can no longer be skipped.
//
// The two halves are one function because each is wrong without the other, and
// wrong in a way nothing reports. IdentityMiddleware deletes the authority
// headers a client sent and re-mints them from a verified IAM token, so it runs
// first: what it produces is the only principal in the process anyone may trust.
// The enrichment then parks that principal on the request context, which is the
// only path by which a typed op reaches it — a zip.Get[In, Out] handler receives a
// context and its decoded In and nothing else. Reversed, it parks whatever the
// caller claimed for itself. Installed alone, the boundary validates a caller and
// then every typed op reads an empty org and refuses that same caller, which
// reaches the wire as a 403 from a service behaving exactly as built.
//
// That last failure is the reason this is a function rather than two lines of
// advice. It is what the o11y binary did while assembling its own app, and its
// subsystem then compensated from inside its own Mount, on a group node that
// owned no routes — a program zip refuses to compose, which is the outage.
func Identify(app *zip.App, cfg *Config) {
	// The identity trust boundary. HIP-0519 says identity is verified once, at the
	// edge, and that is the shape to reach. It rests on ONE assumption: the gateway
	// is the only ingress. That assumption does not hold here yet, and the estate's
	// own red-team probe says so — with this middleware removed, a request carrying
	// a forged X-Org-Id, X-User-Id and X-User-IsAdmin reads another org's secret
	// VALUE from the in-cluster KMS listener:
	//
	//	PROBE (b) forged org + forged X-User-Id + IsAdmin → 200 {"value":"…"}
	//
	// So this stays until service listeners are unreachable except through the
	// gateway. Removing it is a network-policy change first and a code change
	// second, and doing the code half alone is a cross-tenant secret read.
	// red_orgscope_isolation_test.go and TestAudit_AnonRequestNotAttributedToForgedOrg
	// fail the moment it is dropped; they are the gate on that work, not obstacles
	// to it.
	app.Use(IdentityMiddleware(cfg))

	// Besides the validated org, this carries the request a proxying subsystem
	// forwards identity from and the slot a creator writes 201 or 202 into. It must
	// precede every typed route, because fiber runs middleware in registration
	// order and one installed after its leaves never runs. A subsystem whose routes
	// are spread across several top-level nouns owns no single prefix to hang it
	// on, which is the other reason it belongs to whoever composes the app. See
	// typed.go.
	app.Use(Bridge())
}
