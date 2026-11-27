package o11y

import (
	"context"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path"
	"strings"

	"github.com/hanzoai/cloud/sdk/env"

	luxlog "github.com/luxfi/log"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/spec"
	module "github.com/hanzoai/o11y"
	"github.com/zap-proto/zip"
)

// The prose for the operations this package serves but does not OWN the shape of:
// the three native service probes hanzoai/o11y registers, and the DSN ingest
// endpoints. None has an operation to type — a probe is a bare handler and an
// envelope frame is a foreign wire — so zipdoc has nothing to lift, and without
// a Describe they publish an operationId and nothing else. Declared
// here because this file owns what a caller actually meets on the way through:
// the gate, its exemptions, and the runtime the request is handed to. The probes'
// prose cannot move into the module with the routes, because Describe lives in
// hanzoai/cloud and the module deliberately no longer imports it.
//
// THE PATH KEYS ARE ROUTE LITERALS AND MUST STAY EQUAL TO THEM. These three named
// /v1/o11y/api/v2/{livez,healthz,readyz} — the internal namespace the module used
// to rewrite onto — for as long as the exemption in gate() named the same four
// dead paths. The rewrite is gone; the probes answer at /v1/o11y/{livez,healthz,
// readyz} and the descriptions say so.
func init() {
	// --- the three native service probes (hanzoai/o11y health.go) ---
	spec.Describe("/v1/o11y/livez", http.MethodGet,
		"Liveness of the observability process",
		"Answers 200 unconditionally while the process is running, and asserts NOTHING about "+
			"the telemetry stores behind it. That is what makes it a liveness probe: a "+
			"container that answers this is worth leaving alive, and restarting on a store "+
			"outage would only remove the thing reporting the outage.\n\n"+
			"UNAUTHENTICATED by design, and one of exactly three /v1/o11y paths that are. It "+
			"carries no tenant data, and gating it would break the k8s probes and the external "+
			"health checks without protecting anything. Use the health probe, not this one, to "+
			"ask whether the runtime can actually serve.")
	spec.Describe("/v1/o11y/healthz", http.MethodGet,
		"Health of the observability runtime's services",
		"Reports whether every service in the runtime's registry is healthy, and names them "+
			"grouped by state — so a failure says WHICH component is down, not merely that "+
			"something is. An unhealthy registry answers 503, not a 200 with a false flag "+
			"inside, so a plain status check cannot read a sick runtime as well.\n\n"+
			"UNAUTHENTICATED by design, like the other two probes: it carries no tenant data "+
			"and is reached by k8s and by external checks that hold no principal.")
	spec.Describe("/v1/o11y/readyz", http.MethodGet,
		"Readiness of the observability runtime to serve",
		"Reports whether the runtime's registered services are healthy enough to take "+
			"traffic, and answers 503 when they are not — which is what takes a booting or "+
			"degraded replica out of the load balancer instead of letting it serve errors.\n\n"+
			"UNAUTHENTICATED by design, like the other two probes. It reads the same service "+
			"registry the health probe reads, so the two agree by construction; readiness is "+
			"the question a router asks and health is the question an operator asks.")

	// --- the ELEVEN escape hatches (hanzoai/o11y mountHatches) ---
	//
	// THE /v1/o11y/* WILDCARD IS GONE and its five method descriptions with it:
	// hanzoai/o11y names every route now — 353 typed ops, 11 hatches, 3 probes —
	// so there is no fallthrough left to describe, and describing an address
	// nothing serves is the same lie a wildcard was. The 353 carry their own prose
	// in their doc comments, where the shape is declared. These eleven cannot: a
	// stream, a redirect and a foreign wire each break the declaration a typed op
	// makes, which is why the module registers them by hand. Their prose lands
	// HERE rather than beside them because Describe lives in hanzoai/cloud and the
	// module deliberately no longer imports it — the one thing this side still
	// owns for those routes is what a caller reads.
	spec.Describe("/v1/o11y/logs/livetail", http.MethodGet,
		"Follow log records as they arrive",
		"Streams matching log records continuously instead of answering once, so a console "+
			"tail shows lines as they land rather than at the end of a window.\n\n"+
			"It is a STREAM, which is why it is not a typed operation: there is no single "+
			"complete value to name, and a generated client that waited for one would hang on "+
			"the first tail. Read the bounded window with the log read instead when you want an "+
			"answer rather than a feed.\n\n"+
			"A validated, org-scoped principal is required and the feed carries that "+
			"principal's own tenant only.")
	spec.Describe("/v1/o11y/query_progress", http.MethodGet,
		"Watch one running query's progress",
		"Reports how far a submitted query has got — rows scanned, bytes read, elapsed — and "+
			"HOLDS the connection until the next update rather than answering immediately.\n\n"+
			"ONE ADDRESS, TWO PROTOCOLS. Send an Upgrade and this is a websocket carrying the "+
			"same progress; send an ordinary GET and it is a long poll. The Upgrade is a "+
			"property of the request, not of the address, so the read that used to answer at "+
			"/ws/query_progress answers here.\n\n"+
			"The long poll is the whole point, and the reason this cannot be a typed "+
			"operation: an answer that arrived only when the query finished would report "+
			"progress on nothing, and an upgraded connection has no JSON response to "+
			"declare.\n\n"+
			"A validated, org-scoped principal is required; a query id belonging to another "+
			"tenant is simply not found.")
	spec.Describe("/v1/o11y/export_raw_data", http.MethodPost,
		"Export raw telemetry rows as a file",
		"Runs a query and returns its rows as a downloadable CSV or JSONL attachment, "+
			"chunked, with a trailer that says whether the export completed — so a truncated "+
			"download is detectable rather than silently short.\n\n"+
			"The answer is a file, not a value, which is why it is not a typed operation: the "+
			"body is neither JSON nor bounded. Use the query operations when you want rows in "+
			"a response.\n\n"+
			"A validated, org-scoped principal is required and the export carries that "+
			"principal's own tenant only.")
	spec.Describe("/v1/o11y/login", http.MethodGet,
		"Report why an observability sign-in did not complete",
		"Where a failed sign-in callback lands. The module builds that redirect with a "+
			"path and no host, so it can only be same-origin, and its assumption is that "+
			"the console lives beside the API. Here the console is a separate host, so "+
			"this answers as the API it belongs to rather than redirecting onward — a "+
			"hostname baked into a shared surface is how one brand's identity ends up in "+
			"front of another brand's customer.\n\n"+
			"401 with the provider's own reason, carried from the query the module "+
			"already populated. Unauthenticated by necessity: it exists precisely for the "+
			"case where no principal was established.")
}

// defaultUpstream is the in-cluster address of the o11y runtime Deployment's
// Service (port 80 -> container 8080). Overridable via O11Y_UPSTREAM.
const defaultUpstream = "http://o11y.hanzo.svc.cluster.local:80"

func upstream() string {
	if v := env.Or("O11Y_UPSTREAM", ""); v != "" {
		return v
	}
	return defaultUpstream
}

// newHandler builds the reverse-proxy handler targeting the o11y runtime. Pure
// (URL in, handler out) so it is unit-testable without a live upstream.
//
// The path is forwarded UNCHANGED: the o11y runtime registers its routes at
// their exact public path (/v1/o11y/<version>/<path>) — no /api/, no rewrite.
// One and one way: the route IS the path, on both sides of this proxy.
func newHandler(rawURL string) (http.Handler, error) {
	target, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	base := proxy.Director
	proxy.Director = func(r *http.Request) {
		base(r)              // sets scheme/host to target; path unchanged
		r.Host = target.Host // upstream vhost, not api.hanzo.ai
	}
	// An unreachable upstream has to SAY SO. The default ErrorHandler answers 502
	// with an EMPTY body, so a retired Deployment behind this fallback reads as
	// `{"detail":"","status":502}` on every /v1/o11y/* request and on every error
	// envelope /v1/event relays here — a fleet whose error ingest is down, saying
	// nothing about why, which is the one failure that also hides every other one.
	//
	// The target is named to the LOG and not to the caller: it is an internal
	// address, and the caller can act on "unreachable" without learning the
	// topology.
	log := luxlog.Default().New("subsystem", "o11y-runtime")
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		log.Warn("o11y upstream unreachable", "target", target.Host, "method", r.Method, "path", r.URL.Path, "err", err)
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"detail":"the o11y runtime is not reachable","status":502,"title":"Bad Gateway","type":"about:blank"}`))
	}
	return proxy, nil
}

// gate decides, per request, WHAT SCOPE the o11y runtime may serve — it is not a
// single all-or-nothing admission test:
//
//	no validated principal            → 403 (the anonymous-forge path)
//	a member of an org                → that org, and only that org
//	a validated platform SuperAdmin   → the cross-tenant view
//
// The middle line is the product: Sentry/o11y telemetry is a TENANT's own data,
// so org membership is the whole admission test and there is deliberately no
// admin term on it. Gating the product itself on platform sudo would make the
// only way to see your own errors a scope that shows you everyone's — the same
// predicate at the wrong level. It is applied ONE LEVEL IN instead
// (scopeToTenant): SuperAdmin buys the cross-tenant selectors, nothing else.
//
// X-User-Id is set ONLY by the identity middleware from a verified credential
// (the same signal principal.Validated uses), so its presence is the
// authoritative principal term; X-Org-Id is minted the same way from the
// principal's `owner`. The bare reverse proxy forwards ALL inbound headers and
// the query string upstream, so an org-less caller is refused rather than served
// unscoped, and a member's cross-org query keys are stripped before the runtime
// can honour them.
//
// WHICH OPS ARE EXEMPT IS THE MODULE'S ANSWER, NOT OURS (module.Anonymous). This
// file used to keep its own list, and the list named /v1/o11y/api/v1/health and
// three /api/v2 siblings — the INTERNAL namespace hanzoai/o11y used to rewrite
// onto before it stopped rewriting paths at all. Four names, zero routes: the
// exemption matched nothing and this gate refused EVERY public op. /version,
// /health, the three probes and sign-in answered 403 at api.hanzo.ai while the
// standalone o11y answered 200, which is precisely why o11y still had an address
// of its own. A copy of a route fact, kept one repo away from the routes, drifts
// the moment the routes move; the fact has one home now and it is beside them.
//
// The exempt set is the runtime's own OpenAccess routes plus the two
// public-dashboard reads it gates with CheckWithoutClaims, and the rule behind
// it is this gate's own purpose read backwards: this gate exists because the
// runtime trusts X-Org-Id as gateway-minted, so an op whose gate reads no tenant
// from the request has nothing for a forged tenant to reach and gating it can
// only remove an answer. Everything else — every read of a tenant's telemetry —
// stays gated here AND at the runtime, which is one rule enforced twice, not two
// rules. Exemption is not authorization: the DSN key, the service-account key,
// the share's scope and the session cookie are each still the op's own
// admission test, applied one layer in.
func gate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if module.Anonymous(r.Method, r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		if strings.TrimSpace(r.Header.Get("X-User-Id")) == "" {
			refuse(w, "no validated principal")
			return
		}
		// The PRODUCT is org-scoped: errors, logs, traces and metrics are a
		// tenant's own telemetry, so MEMBERSHIP OF AN ORG is the whole admission
		// test. Admin-gating the product is what made sentry.hanzo.ai answer
		// "Access required — admin-only surface" to a signed-in customer whose
		// own errors were sitting in event.error. Platform sudo belongs one level
		// in, on the cross-tenant fleet view, not on the product.
		//
		// A SuperAdmin passes without an org because the admin console reads
		// before an org is selected. It buys reach, not data: the runtime still
		// scopes every read from X-Org-Id, so an org-less request returns an
		// org-less result rather than the fleet.
		if superAdmin(r) {
			next.ServeHTTP(w, r)
			return
		}
		// Logs and traces reads are SuperAdmin-only until the runtime binds org = ? on them.
		if unscopedRead(r.URL.Path) {
			refuse(w, "logs and traces reads are limited to platform administrators")
			return
		}
		if orgOf(r) == "" {
			refuse(w, "an org-scoped principal is required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// unscopedReads are the runtime addresses that read event.log or event.span
// through query builders that bind no org: the query engine, the logs face, the
// trace detail and its projections, the field catalogs, and the span-derived
// APM reads. An entry ending in "/" covers only the addresses beneath it; the
// bare /v1/o11y/traces is cloud's own org-pinned list (traces.go).
var unscopedReads = []string{
	o11yPrefix + "/query_range",
	o11yPrefix + "/variables/query",
	o11yPrefix + "/export_raw_data",
	o11yPrefix + "/logs",
	o11yPrefix + "/traces/",
	o11yPrefix + "/autocomplete",
	o11yPrefix + "/auto_complete",
	o11yPrefix + "/fields",
	o11yPrefix + "/filter_suggestions",
	o11yPrefix + "/services",
	o11yPrefix + "/service",
	o11yPrefix + "/dependency_graph",
	o11yPrefix + "/span_percentile",
	o11yPrefix + "/messaging-queues",
	o11yPrefix + "/third-party-apis",
	o11yPrefix + "/trace-funnels",
}

// unscopedRead reports whether p is one of unscopedReads or beneath one.
func unscopedRead(p string) bool {
	for _, f := range unscopedReads {
		if covers(f, p) {
			return true
		}
	}
	return false
}

// covers reports whether entry f of unscopedReads covers path p. The path is
// cleaned and lowercased first because the router matches without regard to
// case or a trailing slash.
func covers(f, p string) bool {
	p = strings.ToLower(path.Clean("/" + p))
	if strings.HasSuffix(f, "/") {
		return strings.HasPrefix(p, f)
	}
	return p == f || strings.HasPrefix(p, f+"/")
}

// refuse writes the gate's fail-closed answer. ONE writer, so every refusal on
// this client is the same shape (403 + JSON reason) whatever term rejected it.
func refuse(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusForbidden)
	_, _ = w.Write([]byte(`{"status":"error","msg":"` + msg + `"}`))
}

// superAdmin is principal.Super across an http.Handler seam: the module relay
// writes the verdict it read off the zip caller into X-User-IsAdmin on the
// request it builds, and this reads it back. SanitizeIdentity is the only writer
// of that header in this process — it strips a client's copy and mints it from
// the validated token alone (owner == "admin") — so the value here is the one
// principal.Super answers, never a caller's claim.
func superAdmin(r *http.Request) bool { return r.Header.Get("X-User-IsAdmin") == "true" }

// orgOf is the validated tenant SanitizeIdentity pinned from the principal's
// `owner` claim. A client copy never survives ingress, so this value is either
// server-minted or absent.
func orgOf(r *http.Request) string { return strings.TrimSpace(r.Header.Get("X-Org-Id")) }

// THERE IS NO QUERY-KEY ORG FILTER HERE, AND THERE MUST NOT BE ONE.
//
// A `scopeToTenant` used to sit at this client, deleting ?org=/?orgId=/?tenant=/
// ?allOrgs= from a non-admin's request "so org A cannot read org B". It was
// removed because it did nothing and hid a bypass while claiming to be a control:
//
//   - INERT. hanzoai/o11y has no query-parameter org selector to strip. Every
//     read handler takes its tenant from orgFromContext -> ClaimsFromContext,
//     set only by the iamidentn provider from the X-Org-Id this gate validated.
//     DiscoverRequest, the one POST body read, carries no org field either. The
//     eight keys were read by nothing.
//   - BYPASSABLE. Go's url.ParseQuery SKIPS any pair containing a semicolon, so
//     `?org=victim;x=1` made q.Has("org") false, nothing was stripped, and
//     RawQuery was forwarded verbatim — org=victim included. A denylist over a
//     lossy parser is not a filter. It was also case-sensitive (Org=, ORG= passed)
//     and named none of organization=, owner=, orgs=, workspace=, customerId=.
//   - LOSSY. Whenever a listed key did appear the whole query was re-parsed and
//     re-Encoded, silently dropping pairs Go rejects (?bad=%zz) and rewriting
//     %20 to + inside a caller's legitimate ?query=.
//
// Isolation is the org pin, not the query string: SanitizeIdentity deletes every
// client X-Org-*/X-User-* header at ingress and re-mints X-Org-Id from the
// validated principal's own claim, and the runtime scopes from that alone. If a
// future runtime ever DOES read an org from the query, the fix is to stop it
// reading one — not to guess the spellings here.

// The three predicates that used to live here — isHealthPath, isErrorIngestPath
// and isSentryIngestPath — are gone. They were this repo's copy of hanzoai/o11y's
// route table, and isHealthPath had already drifted into naming four paths that
// no longer existed, which is what broke the unified endpoint. The answer comes from
// the module now: module.Anonymous covers every family that serves a caller with
// no principal. Nothing here takes data in — /v1/event is the one ingest.

// runtimeHandler is the gated o11y runtime handler (the in-process runtime, or the
// reverse-proxy fallback) — the SAME handler the hanzoai/o11y wildcard delegates to
// via module.SetHandler. It is pinned here so the flat builder-query routes (query.go)
// can delegate to it directly. Set by mountRuntime; read PER-REQUEST (never at
// registration), so it is always in place by the time the first request lands.
var runtimeHandler http.Handler

// mountRuntime installs the runtime handler the order-70 wildcard delegates to.
//
// TWO BACKINGS, AND THEY ARE NOT THE SAME THING. With a datastore DSN this binary
// IS o11y: it builds the runtime in-process (embed.go) and, because it holds the
// store connection, also carries the three things only a runtime can — metrics
// ingest, this process's own metrics push, and the Sentinel consumer that files
// event.error into issues. Without one it RELAYS to the standalone o11y
// Deployment, which answers queries and carries none of those three.
//
// So the fallback is not a slower embed; it is a smaller surface, and choosing it
// is a deployment's decision rather than an accident. It also absorbs a datastore
// that is late at boot, which is why the embed failing logs and degrades instead of
// failing the mount of an Eager app.
//
// Ordering is not strictly required (the handler is resolved per-request); it runs
// inside the one order-69 mount, before Listen, so the handler is in place before
// the first request.
func mountRuntime(deps cloud.Deps) error {
	log := luxlog.Default().New("subsystem", "o11y-runtime")

	if h, err := buildEmbeddedHandler(deps); err != nil {
		// ERROR, not Warn. The fallback below reverse-proxies a Deployment this
		// design expects to RETIRE, so when the embed does not start the usual
		// outcome is not a slower o11y — it is no o11y, and no issue filing
		// either. This line is the only statement of the root cause; everything
		// downstream can say no more than "unreachable".
		log.Error("embedded o11y init failed; falling back to reverse proxy — telemetry and issue filing depend on that upstream still existing", "err", err)
	} else if h != nil {
		gh := gate(h)
		runtimeHandler = gh
		// The embedded runtime is one router that matches the request's own path,
		// so every declared address reaches the same handler: module.Whole, which is
		// what SetHandler meant before a runtime could resolve per address.
		module.SetRuntime(module.Whole(gh))
		// Runtime (and its ONE datastore connection) is live; start native
		// metrics ingest — opt-in, fail-soft (metrics.go).
		startNativeMetricsIngest(embeddedRuntime.TelemetryStore, log)
		// …and start carrying THIS process's own measurements to the same
		// store. The line above receives other processes' metrics; this one
		// sends ours. Without it cloud is the only service in the fleet whose
		// metrics exist nowhere, because its single exit was a Prometheus
		// scrape and Prometheus is gone (metricspush.go).
		startNativeMetricsPush(embeddedRuntime.TelemetryStore, log)
		// File event.error into Sentinel's issues — a durable on the bus
		// (errorsink.go). Requires the in-process runtime's Modules.Sentry, so
		// it starts only on this embed-up branch.
		installErrorSink(log)
		log.Info("o11y runtime handler installed (in-process runtime)")
		return nil
	}

	// Fallback: reverse-proxy the standalone o11y Deployment.
	h, err := newHandler(upstream())
	if err != nil {
		return err
	}
	gh := gate(h)
	runtimeHandler = gh
	// A reverse proxy has one handler and the far side selects the route, so there
	// is nothing here to resolve per address.
	module.SetRuntime(module.Whole(gh))
	log.Info("o11y runtime handler installed (reverse proxy fallback)", "upstream", upstream())
	return nil
}

// Mount composes the whole observability surface into its host as ONE app,
// through [*zip.Group.Use] — the same client apps/iam composes identity through.
// Use takes a [zip.Component], and an *App is one, so the child below is passed
// to it whole.
//
// # Why the surface is an app and not a pile of routes on the host's router
//
// A composed op arrives carrying Origin = the child's AppName, and zip qualifies
// every named type that op reaches as "<origin>.<Type>" — unconditionally, not on
// collision, so a published name is never a function of who else is in the room
// (zip schemaRegistry.nameFor). That is why identity's 91 schemas are iam.* and
// have never collided with anything.
//
// Registered straight onto the host, as this was, every one of these ops has an
// empty Origin and its types are published under their bare Go names. o11y's are
// ordinary words — Account, Channel, Event, Host, Service, TLSConfig — and so are
// books', content's, analytics', plugins' and ingress'. Six names, twelve shapes,
// one components block: a refusal at the compose (openapi/compose.go — "one name, two
// shapes: every generated SDK would bind whichever it read last"), and had it not
// been refused, an SDK binding whichever the merge read last. `make -f
// mk/fleet.mk openapi` could not run at all, so nobody could regenerate
// openapi.yaml or add an API surface and prove it.
//
// ONE origin for the whole product, not one for the module and none for the rest.
// The cloud-native reads here and hanzoai/o11y's relay table are two halves of one
// route table — that is what mountScope's "specific routes before the module's"
// invariant IS — and splitting the origin down that client would namespace half of
// o11y's types and leave the other half bare, which is two conventions for one
// product.
//
// # What the child changes, and what it must not
//
// Nothing on the wire. Use registers the child's own absolute patterns on the
// host's router, in the order the child declares them, pointing at one delegate
// that re-runs the child's router on the SAME fasthttp request. So the host's
// chain — EdgeCORS, the identity boundary, the abuse gate — still runs first and
// unchanged, and the ORDER two claimants on one address are resolved in is the
// order they are written in below, which is the order they were already in. That
// is load-bearing: cloud's org-pinned GET /v1/o11y/{logs,metrics} and POST
// /v1/o11y/query_range are the ONE owner of those three addresses (scope.go), the
// module declares them too, and first-registered is what makes the tenant-pinned
// handler the one that answers. Inside one app that stays a local fact about two
// adjacent lines instead of a global fact about the host's mount order.
//
// The child is a NAMESPACE, not a second server: it carries the host's logger and
// cloud.ErrorHandler and no policy of its own, because everything a request meets
// before its route belongs to the host. Contrast iam, whose child is a whole
// service that brings its own Guard, error handler and config.
//
// cloud.Bridge still crosses, because [zip.Ctx.SetContext] parks on the
// *fasthttp.RequestCtx and the delegate hands the child that same RequestCtx — so
// the validated org parked at /v1/o11y is the one every typed op behind it reads.
// endpoint_test.go is the end-to-end proof, over the real mount.
//
// # cloud.Bridge is not installed here
//
// No subsystem installs it. The typed ops below do need it — a zip.Get[In, Out]
// handler receives a context and its decoded In and nothing else, so the validated
// org reaches it only by being parked on that context — but the middleware belongs
// to whoever COMPOSES this app. Only a composer knows that the identity boundary
// has already run (the org is trustworthy only once [cloud.SanitizeIdentity] has
// minted it) and that no route is registered ahead of it; see the install in
// [cloud.App]. A second copy installed here could never run: each Group call makes
// a fresh node, so the install lands on a node of its own at the prefix while the
// routes live beneath the different node mountScope creates — and zip judges the
// node, not the path, so it refuses middleware that cannot reach them.
//
// # Nothing is left on the host but the child
//
// [zip.App.Declaration] expands All into the seven methods a host must route and
// leaves HEAD and OPTIONS out of that expansion — under All they are
// indistinguishable from the shadows fiber generates for a GET and for CORS, which
// a host regenerates wherever it registers the GET — so a route declared with All
// cannot reach the host intact. One route needed that exception: the /v1/sentinel
// proxy, which answered OPTIONS as a real method. It is gone — the runtime carries
// the error face at /v1/o11y/sentinel now, twelve named paths with typed ops
// instead of a wildcard — so there is no All left here and nothing to register on
// the host but the child itself. Everything with a shape to name is in the child,
// which is where it always belonged.
func Use(app cloud.Router, deps cloud.Deps) error {
	a := zip.New(zip.Config{
		AppName:      "o11y",
		Logger:       luxlog.Default(),
		ErrorHandler: cloud.ErrorHandler,
	})

	// READ/SERVE plane — specific routes before the wildcard.
	mountScope(a)  // GET logs/metrics/status + vm/{query,query_range} + flat builder query + sessions
	mountAlerts(a) // POST /v1/o11y/alerts/:receiver + GET /v1/o11y/alerts/last + GET /v1/o11y/alerts/heartbeat
	exposeFiring() // plane: the platform's firing alerts, for the admin console
	// The sign-in failure route, before the wildcard like every other specific
	// route: the module redirects a failed callback to a SAME-ORIGIN /v1/o11y/login
	// and this deployment serves its console elsewhere, so without this the redirect
	// lands on a 404.
	mountSigninOutcome(a)
	// Native human-review surface (SQLite metastore) — /v1/o11y/reviews*.
	if err := mountAnnotationQueues(a, deps); err != nil {
		return err
	}
	// RUNTIME handler the wildcard delegates to (embed or proxy fallback).
	if err := mountRuntime(deps); err != nil {
		return err
	}
	// WRITE plane — order-independent (no /v1/o11y/* Fiber route): the ZAP
	// span+log receivers and the opt-in in-process trace sink, all writing
	// event.span / event.log (planesink.go).
	if err := mountPlaneIngest(deps); err != nil {
		return err
	}
	if err := mountProbes(deps, a.Metrics()); err != nil { // fleet health probes -> hanzo_service_up
		return err
	}
	// The gauge leaves this process by being PUSHED to the telemetry store
	// (metricspush.go, started with the runtime above), not by being collected.
	// There was a Prometheus exposition on :9464 here until Prometheus was
	// retired; a listener whose only caller was a scraper that no longer exists
	// is not a way out, it is an open port.
	// PUBLIC status face GET /v1/o11y/summary — the outward projection of the gauge the
	// probes above record. After mountProbes because it reads what they write, and
	// before the terminal wildcard like every other specific route. Unauthenticated
	// and tenant-free by construction; see summary.go.
	mountSummary(a, deps)
	// TERMINAL sub-mount: the hanzoai/o11y module wildcard /v1/o11y/* — the runtime
	// route surface, delegating to the SAME gated handler mountRuntime installed via
	// module.SetHandler. Registered LAST (after every specific /v1/o11y/* route above) so
	// Fiber's in-order match gives those routes precedence over this catch-all. Folded
	// in HERE — it was a second, co-named Wire entry (o11ymod.Use) — so
	// the observability plane is ONE `o11y` subsystem. /v1/o11y/health is unaffected:
	// it stays the generic always-ok route (the o11y Wire entry keeps OwnsHealth=false),
	// registered before UseAll and thus ahead of this wildcard.
	// Mount takes the router and its own options, nothing else: a route table is a
	// value, and the router it registers into already carries this deployment's
	// logger. It used to take Deps for that one field, which made
	// github.com/hanzoai/o11y require github.com/hanzoai/cloud — a module cycle,
	// and the reason o11y's own community binary could not link its own route
	// declarations.
	//
	// NO CLAIMS. The table declares every route it owns, so two declarations at one
	// address is a refusal to compose rather than a shadow — and this host now
	// declares none of them. It briefly claimed three (GET logs, GET metrics, POST
	// query_range), which made the binary compose by making the collision explicit;
	// it did not make the collision go away. A claim SUPPRESSES the module's
	// declaration, so each one silently cost the fleet the module's real read at
	// that address, and one of the three was cloud's route into a runtime path that
	// no longer exists. What the addresses needed was to be told apart, not
	// assigned: the per-product RED read moved to /v1/o11y/product/metrics, the
	// caller-less log read was deleted, and the v3 query pin died with the v3 route
	// it named. See scope.go for the three decisions.
	// The module's own table LAST, and its ERROR returned rather than its call.
	// Folding the two functions into one made this the difference between a mounted
	// surface and a 404: a bare `return module.Use(a)` short-circuits the compose
	// below, which the outer function used to perform after this one returned.
	if err := module.Use(a); err != nil {
		return err
	}
	// The host checks every address this definition declares against the prefixes
	// o11y holds, so a module route outside them fails the mount.
	app.Use(a)
	return nil
}

// shutdownO11y tears down the write-plane resources that hold process-lifetime
// connections, in REVERSE mount order — plane ingest (trace sink + receivers),
// event-ingest Datastore — so buffered spans/logs/rows flush before exit.
// Best-effort: the first error is returned but every teardown still runs.
// Idempotent and nil-safe.
func ShutdownO11y(ctx context.Context) error {
	stopProbes()
	stopNativeMetricsPush()
	stopNativeMetricsIngest()
	// Stop the Sentinel consumer and detach the span fan-out first so nothing in
	// flight reaches a tearing-down runtime or a closing datastore connection.
	// Idempotent and nil-safe.
	clearErrorSink()
	clearSpanSink()
	var firstErr error
	if err := shutdownAnnotationQueues(); err != nil && firstErr == nil {
		firstErr = err
	}
	if err := shutdownPlaneIngest(ctx); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}
