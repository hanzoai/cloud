// Package o11y is your logs, metrics and traces: ship them in, query them, chart them.
//
// It is the ONE owner of the cloud binary's observability plane — registered as a
// SINGLE `o11y` subsystem (this file's init) that internally mounts, in the
// load-bearing order, every part of the concept:
//
//	READ/SERVE plane (specific /v1/o11y/* routes, registered BEFORE the
//	hanzoai/o11y wildcard so Fiber's in-order match gives them precedence):
//	  - tenant-scoped reads  /v1/o11y/{logs,metrics,status}   (scope.go)
//	  - SuperAdmin VM proxy  /v1/o11y/vm/{query,query_range}   (vmproxy.go)
//	  - flat builder query   /v1/o11y/{query,query_range}      (query.go)
//	  - event ingest         POST /v1/event/ingestion          (event_ingest.go)
//	  - Sentry-wire ingest   POST /v1/event/{project}/envelope|store (via cloud.ObsErrorIngest)
//	RUNTIME handler the hanzoai/o11y wildcard (order 70) delegates to via
//	  o11y.SetHandler — the in-process runtime (embed.go) or a reverse-proxy
//	  fallback (this file).
//	WRITE plane (opt-in, order-independent):
//	  - OTLP ingest collector (ingest.go)
//	  - in-process trace sink (tracesink.go)
//
// Decomplection (one and one way): these were five separately-registered
// subsystems (o11yscope 69, o11y-runtime 71, o11y-event-ingest 68,
// o11y-otlp-ingest 72, o11y-trace-inproc 73) whose names leaked FIVE public
// concepts into the registry (five config toggles, five /v1/<name>/health
// routes). The k8s-style ordering was an internal impl detail. They now collapse
// to ONE registration of the name `o11y` (order 69): mountO11y performs the
// ordered sub-mounts in-process, so the PUBLIC concept is a single `o11y`.
// Behavior is preserved EXACTLY — every route registers at the same point
// relative to the order-70 wildcard as before (all inside the one order-69
// mount, so all before 70).
//
// Co-ownership: the upstream github.com/hanzoai/o11y module ALSO registers the
// name `o11y` (order 70, the wildcard route surface) from its own init. The two
// entries are co-owners of ONE public concept; this order-69 entry opts out of
// the generic HIP-0106 health route (cloud.HealthOwner) so /v1/o11y/health is
// registered EXACTLY once, by the module's order-70 co-entry.
//
// One way, two backings (mountRuntime):
//   - PRIMARY: the in-process runtime (buildEmbeddedHandler), enabled by
//     O11Y_TELEMETRYSTORE_DATASTORE_DSN. Serves telemetry from cloud itself.
//   - FALLBACK: a reverse proxy to a still-running o11y Deployment, used only when
//     the embed is disabled (no DSN) or fails to init. Fail-soft, zero downtime.
//
// Path is preserved verbatim: /v1/o11y/* reaches the o11y runtime unchanged,
// which rewrites /v1/o11y/* -> /api/* internally (see o11y app.createPublicServer).
// The gateway terminates auth and propagates identity as X-* headers; the runtime
// (embedded) or the proxy (fallback) sees the same request.
package o11y

import (
	"context"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/openapi"
	"github.com/hanzoai/o11y"
	"github.com/zap-proto/zip"
)

// The prose for the operations this package serves but does not OWN the shape of:
// the three probes the upstream module registers ahead of its wildcard, and the two
// wildcards themselves. A wildcard has no operation to type — that is the whole
// reason it is a wildcard — so zipdoc has nothing to lift, and without a Describe
// these ten publish an operationId and nothing else. Declared here because this
// file owns what a caller actually meets on the way through: the gate, its
// exemptions, and the runtime the request is handed to.
//
// The path keys are the FIBER patterns (`*`), which the document renders as
// {wildcard1}. A description whose route is not in the router never renders, so
// this stays additive metadata on routes that exist.
func init() {
	// --- probes: registered by the upstream module ahead of its wildcard ---
	openapi.Describe("/v1/o11y/api/v2/livez", http.MethodGet,
		"Liveness of the observability process",
		"Answers 200 unconditionally while the process is running, and asserts NOTHING about "+
			"the telemetry stores behind it. That is what makes it a liveness probe: a "+
			"container that answers this is worth leaving alive, and restarting on a store "+
			"outage would only remove the thing reporting the outage.\n\n"+
			"UNAUTHENTICATED by design, and one of exactly three /v1/o11y paths that are. It "+
			"carries no tenant data, and gating it would break the k8s probes and the external "+
			"health checks without protecting anything. Use the health probe, not this one, to "+
			"ask whether the runtime can actually serve.")
	openapi.Describe("/v1/o11y/api/v2/healthz", http.MethodGet,
		"Health of the observability runtime's services",
		"Reports whether every service in the runtime's registry is healthy, and names them "+
			"grouped by state — so a failure says WHICH component is down, not merely that "+
			"something is. An unhealthy registry answers 503, not a 200 with a false flag "+
			"inside, so a plain status check cannot read a sick runtime as well.\n\n"+
			"UNAUTHENTICATED by design, like the other two probes: it carries no tenant data "+
			"and is reached by k8s and by external checks that hold no principal.")
	openapi.Describe("/v1/o11y/api/v2/readyz", http.MethodGet,
		"Readiness of the observability runtime to serve",
		"Reports whether the runtime's registered services are healthy enough to take "+
			"traffic, and answers 503 when they are not — which is what takes a booting or "+
			"degraded replica out of the load balancer instead of letting it serve errors.\n\n"+
			"UNAUTHENTICATED by design, like the other two probes. It reads the same service "+
			"registry the health probe reads, so the two agree by construction; readiness is "+
			"the question a router asks and health is the question an operator asks.")

	// --- /v1/o11y/* — everything no specific route above claimed ---
	openapi.Describe("/v1/o11y/*", http.MethodGet,
		"Read a resource from the observability runtime",
		"Serves the observability runtime's own read surface — dashboards, alert rules, "+
			"saved views, the service and dependency inventory, and the trace, log and metric "+
			"explorers — in the runtime's own shapes, passed through unchanged.\n\n"+
			"It is the FALLTHROUGH, not the front door. Every path this repo owns the shape of "+
			"is registered ahead of it and wins the match; what reaches here is what only the "+
			"runtime knows how to answer. The public contract is flat — one /v1/, no nested "+
			"version — and the mapping onto the runtime's internal namespace happens at this "+
			"one seam, so a caller never spells an engine version.\n\n"+
			"A validated principal is required and the read is scoped to that principal's own "+
			"org, pinned server-side from its claim; a client-supplied org header never "+
			"survives ingress and there is no query parameter that widens the scope. Platform "+
			"sudo passes without an org — the admin console reads before one is selected — and "+
			"buys reach, not data: the runtime still scopes every read from the tenant it was "+
			"given, so an org-less request answers org-less, never the fleet. Before the "+
			"runtime is initialized, 503.")
	openapi.Describe("/v1/o11y/*", http.MethodPost,
		"Create a runtime object, or run a query against telemetry",
		"Carries the runtime's own writes and query posts — creating a dashboard, an alert "+
			"rule or a saved view, and running the query bodies the explorers submit — in the "+
			"runtime's own shapes, passed through unchanged.\n\n"+
			"It is the FALLTHROUGH: the builder query and the ingest routes this repo owns are "+
			"registered ahead of it and win the match. A validated, org-scoped principal is "+
			"required and the write lands in that principal's own tenant, pinned server-side.\n\n"+
			"ONE EXEMPTION, and it is deliberate: a Sentry error-ingest write presents a DSN "+
			"public key, never a Hanzo session, so those two paths bypass the principal gate "+
			"and are authenticated by the ingest verifier instead — which derives the org from "+
			"the DSN itself and fails closed. The exemption is matched by method plus prefix "+
			"plus suffix, never a broad prefix, so every read under the same subtree stays "+
			"gated. Before the runtime is initialized, 503.")
	openapi.Describe("/v1/o11y/*", http.MethodPut,
		"Replace a runtime object",
		"Replaces one of the observability runtime's own objects — a dashboard, an alert "+
			"rule, a saved view — in the runtime's own shapes, passed through unchanged. The "+
			"fallthrough for the resources only the runtime knows.\n\n"+
			"A validated, org-scoped principal is required, and the write is confined to that "+
			"principal's own tenant: the org is minted from its claim at ingress, a client copy "+
			"never survives, and nothing in the request can widen the scope. Before the runtime "+
			"is initialized, 503.")
	openapi.Describe("/v1/o11y/*", http.MethodPatch,
		"Update part of a runtime object",
		"Applies a partial update to one of the observability runtime's own objects, in the "+
			"runtime's own shapes, passed through unchanged. The fallthrough for the resources "+
			"only the runtime knows.\n\n"+
			"A validated, org-scoped principal is required, and the write is confined to that "+
			"principal's own tenant, pinned server-side from its claim. Before the runtime is "+
			"initialized, 503.")
	openapi.Describe("/v1/o11y/*", http.MethodDelete,
		"Remove a runtime object",
		"Removes one of the observability runtime's own objects — a dashboard, an alert rule, "+
			"a saved view — passing the runtime's answer through unchanged. The fallthrough "+
			"for the resources only the runtime knows.\n\n"+
			"A validated, org-scoped principal is required, and the delete is confined to that "+
			"principal's own tenant, pinned server-side from its claim, so one tenant can never "+
			"reach another's object. Before the runtime is initialized, 503.")

	// --- /v1/sentry/* — the Sentry product face over the SAME runtime ---
	openapi.Describe("/v1/sentry/*", http.MethodGet,
		"Read the caller org's errors on the Sentry surface",
		"Serves the Sentry-compatible read surface — projects, error issues and one issue's "+
			"occurrences, a single event, error logs, error-correlated traces and one trace's "+
			"waterfall, and the event-rate stats — so a Sentry client or the error console "+
			"reads its errors at the paths it already speaks.\n\n"+
			"It is the SAME runtime the observability surface serves, reached under a second "+
			"path family, and there is NO rewrite: the runtime carries these routes literally. "+
			"That is what makes this a product face rather than a translation layer. One "+
			"runtime, two path families.\n\n"+
			"A validated principal is required and the read is scoped to that principal's own "+
			"org. Errors are a tenant's OWN data, so org membership is the whole admission test "+
			"and there is deliberately no admin term on it — gating the product on platform "+
			"sudo would make the only way to see your own errors a scope that shows you "+
			"everyone's. Before the runtime is initialized, 503.")
	openapi.Describe("/v1/sentry/*", http.MethodPost,
		"Send events to the Sentry surface, or write on it",
		"Carries every write on the Sentry-compatible surface: the SDK's error ingest, and "+
			"the authenticated writes the console makes — creating a project, rotating a "+
			"project's DSN key, and running a discover query over the events plane.\n\n"+
			"THE TWO ARE AUTHENTICATED DIFFERENTLY, and that is the rule to get right. An "+
			"envelope or store submission presents a DSN public key, never a Hanzo session, so "+
			"it is exempt from the principal gate and verified by the ingest key check instead "+
			"— which derives the org from the DSN and fails closed. A keyless submission is a "+
			"401 from that verifier, not a 403 from the gate, and telling those two apart is "+
			"how you tell the hops apart. Every other write here needs a validated, org-scoped "+
			"principal, and creating or rotating requires an editor rather than a viewer.\n\n"+
			"The ingest exemption is matched by method plus prefix plus suffix, never a bare "+
			"prefix, and the project segment must be a UUID — so no read is reachable through "+
			"it. Before the runtime is initialized, 503.")
	openapi.Describe("/v1/sentry/*", http.MethodPut,
		"Move an error issue through its lifecycle",
		"The one replace on the Sentry surface: updating an error ISSUE — resolving it, "+
			"ignoring it, or assigning it — and answering the updated issue.\n\n"+
			"Nothing else here takes a replace. A project is created and deleted but never "+
			"replaced, and the event and trace planes are append-only telemetry, so an issue's "+
			"lifecycle is the only mutable state this face exposes.\n\n"+
			"Requires a validated, org-scoped principal with edit rights; a viewer is refused. "+
			"The write is confined to the org minted from that principal's claim, so an issue "+
			"id belonging to another tenant is simply not found. Before the runtime is "+
			"initialized, 503.")
	openapi.Describe("/v1/sentry/*", http.MethodPatch,
		"Not served — the Sentry surface has no partial update",
		"The Sentry face carries NO route for a partial update. The wildcard admits every "+
			"method, so this operation exists as an address, but nothing behind it answers and "+
			"a request lands on the runtime as an unrouted path.\n\n"+
			"It is documented rather than silently omitted because the useful thing to say is "+
			"where to go instead: an issue's lifecycle — resolve, ignore, assign — is a "+
			"REPLACE on that issue, not a patch, and it is the only mutable state on this "+
			"surface. A client that reaches for a partial update here is looking for that "+
			"call.")
	openapi.Describe("/v1/sentry/*", http.MethodDelete,
		"Delete a Sentry project",
		"The one delete on the Sentry surface: removing a PROJECT, answering 204. Error "+
			"issues, events and traces are not individually deletable — they are append-only "+
			"telemetry, and their lifetime is retention's business, not an API call's.\n\n"+
			"Requires a validated, org-scoped principal with edit rights; a viewer is refused. "+
			"The delete is confined to the org minted from that principal's claim, so a project "+
			"id belonging to another tenant is not found rather than removed. Deleting a "+
			"project retires the DSN that fed it, so any SDK still pointed at that key stops "+
			"being accepted. Before the runtime is initialized, 503.")
	// The methods left over. This address is bound with All(), so it publishes every
	// method this generator knows and the ones above are only the ones that DO
	// something. DescribeRest covers the remainder from the generator's own set, so a
	// method added there is covered the day it appears rather than published bare —
	// which is what a hand-copied list here had already produced for OPTIONS and TRACE.
	openapi.DescribeRest("/v1/o11y/*",
		"Not served by the observability runtime",
		"Published because this address accepts every method, but the runtime routes nothing "+
			"here: the request reaches it as an unrouted path and no telemetry is read or written.")
	openapi.DescribeRest("/v1/sentry/*",
		"Not served by the Sentry face",
		"Published because this address accepts every method, but the Sentry face routes "+
			"nothing here: the request reaches the runtime as an unrouted path and no issue, "+
			"event or trace is touched.")

}

// defaultUpstream is the in-cluster address of the o11y runtime Deployment's
// Service (port 80 -> container 8080). Overridable via O11Y_UPSTREAM.
const defaultUpstream = "http://o11y.hanzo.svc.cluster.local:80"

func upstream() string {
	if v := strings.TrimSpace(os.Getenv("O11Y_UPSTREAM")); v != "" {
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
// Liveness/readiness endpoints are exempt: they carry NO tenant data (the o11y
// runtime itself serves them without identity — that is how the k8s pod probes
// pass), so a principal gate on them would only break unauthenticated health
// probes (admin System Health's CLOUD_O11Y_HEALTH_URL, the external o11y.* hosts,
// k8s) without protecting anything. Data routes (/v1/o11y/api/v1/query_range, …)
// stay gated.
//
// The Sentry error-ingest wire endpoints (POST /v1/o11y/api/<project>/envelope|store/)
// are ALSO exempt: they carry NO Hanzo principal by design — a Sentry SDK presents a
// DSN public key, not a hanzo.id session — and the o11y ingest handler verifies that
// key (constant-time HMAC over the platform secret, fail-closed 401/503) and derives
// the org from the DSN project segment. This is the cloud-side counterpart to the
// gateway's isErrorIngestPath JWT bypass (both stay tight: method+prefix+suffix, never
// a broad /v1/o11y/api allowlist), so the DSN-authenticated ingest reaches its own auth
// instead of being 403'd here for lacking a principal it never carries. Reads under
// /v1/o11y/api/vN/… and the Issues list/detail remain principal-gated.
func gate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isHealthPath(r.URL.Path) || isErrorIngestPath(r.Method, r.URL.Path) || isSentryIngestPath(r.Method, r.URL.Path) {
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
		if orgOf(r) == "" {
			refuse(w, "an org-scoped principal is required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// refuse writes the gate's fail-closed answer. ONE writer, so every refusal on
// this seam is the same shape (403 + JSON reason) whatever term rejected it.
func refuse(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusForbidden)
	_, _ = w.Write([]byte(`{"status":"error","msg":"` + msg + `"}`))
}

// superAdmin is the SAME platform-sudo predicate scope.go's admin() owns —
// X-User-IsAdmin, minted by SanitizeIdentity ONLY for a verified member of the
// reserved admin org and never restored from client input. Read off the request
// here because this seam is a net/http handler, not a zip.Ctx.
func superAdmin(r *http.Request) bool { return r.Header.Get("X-User-IsAdmin") == "true" }

// orgOf is the validated tenant SanitizeIdentity pinned from the principal's
// `owner` claim. A client copy never survives ingress, so this value is either
// server-minted or absent.
func orgOf(r *http.Request) string { return strings.TrimSpace(r.Header.Get("X-Org-Id")) }

// THERE IS NO QUERY-KEY ORG FILTER HERE, AND THERE MUST NOT BE ONE.
//
// A `scopeToTenant` used to sit at this seam, deleting ?org=/?orgId=/?tenant=/
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

// isSentryIngestPath reports whether r is a Hanzo Sentry error-ingest WRITE the
// runtime authenticates with a DSN key (not a Hanzo principal): POST to
// /v1/sentry/{project}/envelope/ or /v1/sentry/{project}/store/. Like isErrorIngestPath
// it matches by method + prefix + suffix — NEVER a bare /v1/sentry/ prefix — so the
// Sentry READ APIs (issues, discover, projects, logs, traces, stats) stay
// principal-gated. The {project} segment is a UUID enforced by the runtime route; the
// trailing slash is the Sentry wire form, the slash-less variant tolerated defensively.
// The gateway needs a byte-identical sibling allow-rule so the tokenless ingest it lets
// through is not then 403'd here. VERIFIED in production once the host actually mounted
// /v1/sentry: a keyless POST to /v1/sentry/<uuid>/envelope/ answers 401 "invalid ingest
// key" (text/plain, from the ingest verifier) — NOT 404 (unrouted), and NOT the 403
// {"status":"error","msg":"no validated principal"} that the READ paths still return.
// Those three responses are how you tell the hops apart if this ever regresses.
func isSentryIngestPath(method, path string) bool {
	if method != http.MethodPost {
		return false
	}
	if !strings.HasPrefix(path, "/v1/sentry/") {
		return false
	}
	return strings.HasSuffix(path, "/envelope/") || strings.HasSuffix(path, "/envelope") ||
		strings.HasSuffix(path, "/store/") || strings.HasSuffix(path, "/store")
}

// isHealthPath reports whether p is an o11y liveness/readiness endpoint. These
// are the exact paths the runtime special-cases (served unstripped, no identity),
// reached either directly or under the /v1/o11y external prefix — so we match on
// suffix rather than exact path.
func isHealthPath(p string) bool {
	return strings.HasSuffix(p, "/api/v1/health") ||
		strings.HasSuffix(p, "/api/v2/healthz") ||
		strings.HasSuffix(p, "/api/v2/readyz") ||
		strings.HasSuffix(p, "/api/v2/livez")
}

// isErrorIngestPath reports whether r is a Sentry error-ingest WRITE that the o11y
// handler authenticates itself with a DSN key (not a Hanzo principal): POST to
// /v1/o11y/api/<project>/envelope/ or /v1/o11y/api/<project>/store/. The <project>
// segment varies, so it matches by method + prefix + suffix — NEVER a bare prefix —
// so the o11y read APIs under /v1/o11y/api/vN/… and the Issues list/detail stay
// principal-gated. Byte-for-byte the gateway's isErrorIngestPath (auth_middleware.go):
// the two allowlists MUST agree so the tokenless ingest that the gateway lets through
// is not then 403'd here. The trailing slash is the Sentry protocol form; the
// slash-less variant is tolerated defensively.
func isErrorIngestPath(method, path string) bool {
	if method != http.MethodPost {
		return false
	}
	if !strings.HasPrefix(path, "/v1/o11y/api/") {
		return false
	}
	return strings.HasSuffix(path, "/envelope/") || strings.HasSuffix(path, "/envelope") ||
		strings.HasSuffix(path, "/store/") || strings.HasSuffix(path, "/store")
}

// runtimeHandler is the gated o11y runtime handler (the in-process runtime, or the
// reverse-proxy fallback) — the SAME handler the hanzoai/o11y wildcard delegates to
// via o11y.SetHandler. It is pinned here so the flat builder-query routes (query.go)
// can delegate to it directly. Set by mountRuntime; read PER-REQUEST (never at
// registration), so it is always in place by the time the first request lands.
var runtimeHandler http.Handler

// mountRuntime installs the runtime handler the order-70 wildcard delegates to.
// ONE way, two backings: prefer the in-process runtime (embed.go) that serves
// /v1/o11y/* from THIS binary against the datastore datastore, so the standalone
// o11y Deployment can retire; fall back to reverse-proxying that Deployment when the
// embed is disabled (no DSN) or fails to init — fail-soft, zero downtime. Ordering is
// not strictly required (the handler is resolved per-request); it runs inside the one
// order-69 mount, before Listen, so the handler is in place before the first request.
func mountRuntime(deps cloud.Deps) error {
	log := deps.Logger.New("subsystem", "o11y-runtime")

	if h, err := buildEmbeddedHandler(deps); err != nil {
		log.Warn("embedded o11y init failed; falling back to reverse proxy", "err", err)
	} else if h != nil {
		gh := gate(h)
		runtimeHandler = gh
		o11y.SetHandler(gh)
		// Runtime (and its ONE datastore connection) is live; start native
		// metrics ingest — opt-in, fail-soft (metrics.go).
		startNativeMetricsIngest(embeddedRuntime.TelemetryStore, log)
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
	o11y.SetHandler(gh)
	log.Info("o11y runtime handler installed (reverse proxy fallback)", "upstream", upstream())
	return nil
}

// mountSentry registers the /v1/sentry/* wildcard, forwarding every request to the
// gated runtime handler (resolved PER-REQUEST, so it is in place by first request —
// same discipline as the o11y wildcard). No path rewrite: the Sentry routes are
// literal /v1/sentry/… in the runtime. The DSN-ingest routes are principal-gate-exempt
// (isSentryIngestPath); the reads stay gated.
func mountSentry(a cloud.Router) {
	a.All("/v1/sentry/*", zip.AdaptNetHTTP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := runtimeHandler
		if h == nil {
			http.Error(w, "o11y runtime not initialized", http.StatusServiceUnavailable)
			return
		}
		h.ServeHTTP(w, r)
	})))
}

// eventToRuntimePath maps the Sentry wire on the ONE /v1/event door to its
// runtime route: POST /v1/event/<project>/envelope|store(/) onto the clean
// /v1/sentry ingest routes. ok=false for anything else — the mapping carries
// ingest ONLY, so no READ API is reachable through it.
func eventToRuntimePath(method, path string) (string, bool) {
	rest, found := strings.CutPrefix(path, "/v1/event/")
	if !found {
		return "", false
	}
	mapped := "/v1/sentry/" + rest
	return mapped, isSentryIngestPath(method, mapped)
}

// mountO11y is the ONE mount for the whole observability concept. It performs the
// ordered sub-mounts in-process so the public registry carries a single `o11y`
// name. Every cloud-native /v1/o11y/* route is registered here — inside this one
// order-69 mount, hence BEFORE the hanzoai/o11y wildcard (order 70) — so Fiber's
// in-order match gives the specific routes precedence over the runtime proxy.
func MountO11y(a *zip.App, deps cloud.Deps) error {
	// Bridge FIRST, on the subtree the typed ops live under. A typed op receives
	// only a context, so the validated org reaches it by being parked there —
	// never as an In field, which is caller-supplied and would be a cross-tenant
	// read the caller asserted for itself. fiber runs middleware in registration
	// order, so this must precede every leaf below.
	//
	// It has to be installed HERE, not only by cloud.Serve, because o11y runs as
	// its OWN process (plugin/o11y/main.go builds a bare zip.App and mounts this).
	// The host's app-wide Bridge parks the org on a context in the HOST; the
	// request crosses to this process as headers, so without this install every
	// typed op in the child would answer 403 for a caller the host had already
	// validated. Nesting under Serve's own Bridge — the fused case — is harmless:
	// the inner one is what the handler sees.
	a.Group(o11yPrefix).Use(cloud.Bridge())

	// READ/SERVE plane — specific routes before the wildcard.
	if err := mountEventIngest(a, deps); err != nil { // POST /v1/event/ingestion
		return err
	}
	mountScope(a)  // GET logs/metrics/status + vm/{query,query_range} + flat builder query + sessions
	mountAlerts(a) // POST /v1/o11y/alerts/:receiver + GET /v1/o11y/alerts/last
	// Native annotation-queues surface (SQLite metastore) — /v1/o11y/annotation-queues*.
	if err := mountAnnotationQueues(a, deps); err != nil {
		return err
	}
	// RUNTIME handler the wildcard delegates to (embed or proxy fallback).
	if err := mountRuntime(deps); err != nil {
		return err
	}
	// Hanzo Sentry product face /v1/sentry/* — the SIBLING of the /v1/o11y wildcard,
	// delegating to the SAME gated runtime handler (which carries the clean /v1/sentry
	// routes; the DSN-ingest routes are gate-exempt via isSentryIngestPath). One
	// runtime, two path families.
	//
	// Registering it here is necessary but NOT sufficient — the subtree has to be
	// reachable at both hops above this one, and it silently was not at either:
	// hanzoai/o11y had to carry the routes (the pinned v1.5.33 does —
	// o11yapiserver/sentry.go registers them), and now that o11y runs out-of-process the HOST has
	// to mount /v1/sentry as a second prefix or the request 404s before it ever
	// reaches this process — see cloud.PluginSpec in apps.Wire().
	mountSentry(a)
	// The Sentry wire on the ONE /v1/event door: POST /v1/event/{project}/envelope|store.
	// The door's owner (analytics) carries the route — the project segment is
	// variable, so no static prefix could route it here — and forwards through
	// cloud.ObsErrorIngest to this handler, which rewrites onto the /v1/sentry
	// runtime routes BEFORE the principal gate sees the path, so the existing
	// ingest exemption stays the only exemption. No /api/ segment anywhere:
	// /v1/ is the only prefix this platform speaks.
	cloud.SetObsErrorIngest(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mapped, ok := eventToRuntimePath(r.Method, r.URL.Path)
		if !ok {
			http.NotFound(w, r)
			return
		}
		h := runtimeHandler
		if h == nil {
			http.Error(w, "o11y runtime not initialized", http.StatusServiceUnavailable)
			return
		}
		r.URL.Path = mapped
		h.ServeHTTP(w, r)
	}))
	// WRITE plane — opt-in, order-independent (no /v1/o11y/* Fiber route).
	if err := mountIngest(deps); err != nil { // ZAP ingest collector (:4317)
		return err
	}
	if err := mountTraceSink(deps); err != nil { // in-process trace sink
		return err
	}
	if err := mountProbes(deps); err != nil { // fleet health probes -> hanzo_service_up
		return err
	}
	// TERMINAL sub-mount: the hanzoai/o11y module wildcard /v1/o11y/* — the runtime
	// route surface, delegating to the SAME gated handler mountRuntime installed via
	// o11y.SetHandler. Registered LAST (after every specific /v1/o11y/* route above) so
	// Fiber's in-order match gives those routes precedence over this catch-all. Folded
	// in HERE — it was a second, co-named Wire entry (o11ymod.Mount) — so
	// the observability plane is ONE `o11y` subsystem. /v1/o11y/health is unaffected:
	// it stays the generic always-ok route (the o11y Wire entry keeps OwnsHealth=false),
	// registered before MountAll and thus ahead of this wildcard.
	if err := o11y.Mount(a, deps); err != nil {
		return err
	}
	return nil
}

// shutdownO11y tears down the write-plane resources that hold process-lifetime
// connections, in REVERSE mount order — trace sink, OTLP collector, event-ingest
// Datastore — so buffered spans/logs/rows flush before exit. Best-effort: the
// first error is returned but every teardown still runs. Idempotent and nil-safe.
func ShutdownO11y(ctx context.Context) error {
	stopProbes()
	var firstErr error
	if err := shutdownAnnotationQueues(); err != nil && firstErr == nil {
		firstErr = err
	}
	if err := shutdownTraceSink(ctx); err != nil && firstErr == nil {
		firstErr = err
	}
	if err := shutdownIngest(ctx); err != nil && firstErr == nil {
		firstErr = err
	}
	if err := shutdownEventIngest(ctx); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}
