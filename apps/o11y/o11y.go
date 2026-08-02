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
//	WRITE plane (order-independent):
//	  - ZAP span+log receivers + opt-in in-process trace sink, all writing
//	    the event plane (planesink.go)
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
// the three native service probes hanzoai/o11y registers, and the /v1/sentry
// wildcard this file registers itself. Neither has an operation to type — a probe
// is a bare handler and a wildcard is a wildcard — so zipdoc has nothing to lift,
// and without a Describe they publish an operationId and nothing else. Declared
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
	openapi.Describe("/v1/o11y/livez", http.MethodGet,
		"Liveness of the observability process",
		"Answers 200 unconditionally while the process is running, and asserts NOTHING about "+
			"the telemetry stores behind it. That is what makes it a liveness probe: a "+
			"container that answers this is worth leaving alive, and restarting on a store "+
			"outage would only remove the thing reporting the outage.\n\n"+
			"UNAUTHENTICATED by design, and one of exactly three /v1/o11y paths that are. It "+
			"carries no tenant data, and gating it would break the k8s probes and the external "+
			"health checks without protecting anything. Use the health probe, not this one, to "+
			"ask whether the runtime can actually serve.")
	openapi.Describe("/v1/o11y/healthz", http.MethodGet,
		"Health of the observability runtime's services",
		"Reports whether every service in the runtime's registry is healthy, and names them "+
			"grouped by state — so a failure says WHICH component is down, not merely that "+
			"something is. An unhealthy registry answers 503, not a 200 with a false flag "+
			"inside, so a plain status check cannot read a sick runtime as well.\n\n"+
			"UNAUTHENTICATED by design, like the other two probes: it carries no tenant data "+
			"and is reached by k8s and by external checks that hold no principal.")
	openapi.Describe("/v1/o11y/readyz", http.MethodGet,
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
	openapi.Describe("/v1/o11y/logs/livetail", http.MethodGet,
		"Follow log records as they arrive",
		"Streams matching log records continuously instead of answering once, so a console "+
			"tail shows lines as they land rather than at the end of a window.\n\n"+
			"It is a STREAM, which is why it is not a typed operation: there is no single "+
			"complete value to name, and a generated client that waited for one would hang on "+
			"the first tail. Read the bounded window with the log read instead when you want an "+
			"answer rather than a feed.\n\n"+
			"A validated, org-scoped principal is required and the feed carries that "+
			"principal's own tenant only.")
	openapi.Describe("/v1/o11y/query_progress", http.MethodGet,
		"Watch one running query's progress",
		"Reports how far a submitted query has got — rows scanned, bytes read, elapsed — and "+
			"HOLDS the connection until the next update rather than answering immediately.\n\n"+
			"The long poll is the whole point, and the reason this cannot be a typed "+
			"operation: an answer that arrived only when the query finished would report "+
			"progress on nothing. The websocket form of the same read is /ws/query_progress.\n\n"+
			"A validated, org-scoped principal is required; a query id belonging to another "+
			"tenant is simply not found.")
	openapi.Describe("/ws/query_progress", http.MethodGet,
		"Watch one running query's progress over a websocket",
		"The same progress read as /v1/o11y/query_progress, delivered over a websocket: the "+
			"Upgrade IS the contract, so there is no JSON response to declare and no typed "+
			"operation to make of it.\n\n"+
			"It sits outside /v1/o11y on purpose — the upgrade handshake is a transport "+
			"concern, not a resource — and it was unreachable from the composed binary until "+
			"the route table named it, because the old wildcard covered only the o11y "+
			"prefix.\n\n"+
			"A validated, org-scoped principal is required.")
	openapi.Describe("/v1/o11y/export_raw_data", http.MethodPost,
		"Export raw telemetry rows as a file",
		"Runs a query and returns its rows as a downloadable CSV or JSONL attachment, "+
			"chunked, with a trailer that says whether the export completed — so a truncated "+
			"download is detectable rather than silently short.\n\n"+
			"The answer is a file, not a value, which is why it is not a typed operation: the "+
			"body is neither JSON nor bounded. Use the query operations when you want rows in "+
			"a response.\n\n"+
			"A validated, org-scoped principal is required and the export carries that "+
			"principal's own tenant only.")
	openapi.Describe("/v1/o11y/complete/google", http.MethodGet,
		"Complete a Google sign-in",
		"The callback Google redirects a user back to after they approve the sign-in. It "+
			"exchanges the authorization code, establishes the session and answers 303 to the "+
			"console.\n\n"+
			"The answer is a Location header and no body, which is why it is not a typed "+
			"operation — declaring a JSON response for a redirect would publish a shape that "+
			"does not exist and hide the header that is the entire point.\n\n"+
			"UNAUTHENTICATED by necessity: it is how a caller GETS a principal, so requiring "+
			"one would be circular. It is not an open door — the code it carries is single-use "+
			"and verified against the provider.")
	openapi.Describe("/v1/o11y/complete/oidc", http.MethodGet,
		"Complete a generic OIDC sign-in",
		"The callback any configured OIDC provider redirects back to. Same shape and same "+
			"reasoning as the Google callback: the code is exchanged, the session is "+
			"established, and the answer is a 303 to the console rather than a body.\n\n"+
			"UNAUTHENTICATED by necessity — this is the act of obtaining a principal, and the "+
			"provider's own code is what authenticates it.")
	openapi.Describe("/v1/o11y/complete/saml", http.MethodPost,
		"Complete a SAML sign-in",
		"The assertion consumer service: the identity provider POSTs its signed assertion "+
			"here, and a valid one establishes the session and answers 303 to the console.\n\n"+
			"A redirect, not a value, so it is not a typed operation. UNAUTHENTICATED by "+
			"necessity and authenticated in fact by the assertion's signature, which is checked "+
			"against the configured provider before any session exists.")
	openapi.Describe("/v1/o11y/api/:project_id/envelope/", http.MethodPost,
		"Receive a Sentry envelope on the SDK's own DSN path",
		"Accepts an application/x-sentry-envelope frame from a Sentry SDK — the batched wire "+
			"format carrying events, sessions and attachments — and ingests it against the "+
			"project named in the path.\n\n"+
			"THE /api/ SEGMENT IS NOT OURS TO NAME. An SDK appends its own fixed "+
			"/api/<project>/envelope/ suffix to whatever DSN it is given, so this address is "+
			"the SDK's, received verbatim. We receive this shape; we do not publish it. The "+
			"clean spelling of the same wire is /v1/sentry/{project}/envelope/.\n\n"+
			"AUTHENTICATED BY THE DSN PUBLIC KEY, never a Hanzo session, and therefore exempt "+
			"from the principal gate: the ingest verifier checks the key in constant time, "+
			"fails closed, and derives the org from it. A keyless submission is a 401 from that "+
			"verifier — not a 403 from the gate, and not a 404 — which is how you tell the hops "+
			"apart. The exemption is matched by method plus prefix plus suffix, never a bare "+
			"prefix, so no read is reachable through it.")
	openapi.Describe("/v1/o11y/api/:project_id/store/", http.MethodPost,
		"Receive a single Sentry event on the SDK's own DSN path",
		"The legacy single-event form of the envelope ingest: one JSON event rather than a "+
			"framed batch, kept because SDKs in the field still send it.\n\n"+
			"Same address ownership and same authentication as the envelope route — the "+
			"/api/ segment is the SDK's, the DSN public key is the credential, the principal "+
			"gate does not apply, and a keyless submission is a 401 from the ingest verifier.")
	openapi.Describe("/v1/sentry/:project/envelope/", http.MethodPost,
		"Receive a Sentry envelope on the clean root",
		"The same envelope ingest as the DSN path, spelled the way this platform names "+
			"things: one /v1/, the product, the project. Point an SDK's DSN here and the wire "+
			"is identical.\n\n"+
			"AUTHENTICATED BY THE DSN PUBLIC KEY and exempt from the principal gate for the "+
			"same reason — a Sentry SDK has no Hanzo session to present. The project segment is "+
			"a UUID enforced by the route, and the exemption matches method plus prefix plus "+
			"suffix, so every Sentry READ (issues, discover, events, logs, traces, stats) stays "+
			"gated.")
	openapi.Describe("/v1/sentry/:project/store/", http.MethodPost,
		"Receive a single Sentry event on the clean root",
		"The legacy single-event ingest on the clean /v1/sentry root — one JSON event rather "+
			"than a framed batch. Same DSN-key authentication, same gate exemption, same "+
			"UUID-enforced project segment as the envelope route beside it.")

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
// WHICH OPS ARE EXEMPT IS THE MODULE'S ANSWER, NOT OURS (o11y.Anonymous). This
// file used to keep its own list, and the list named /v1/o11y/api/v1/health and
// three /api/v2 siblings — the INTERNAL namespace hanzoai/o11y used to rewrite
// onto before it stopped rewriting paths at all. Four names, zero routes: the
// exemption matched nothing and this gate refused EVERY public op. /version,
// /health, the three probes and sign-in answered 403 at api.hanzo.ai while the
// standalone o11y answered 200, which is precisely why o11y still had a door of
// its own. A copy of a route fact, kept one repo away from the routes, drifts
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
		if o11y.Anonymous(r.Method, r.URL.Path) {
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

// The three predicates that used to live here — isHealthPath, isErrorIngestPath
// and isSentryIngestPath — are gone. They were this repo's copy of hanzoai/o11y's
// route table, and isHealthPath had already drifted into naming four paths that
// no longer existed, which is what closed the unified door. The answer comes from
// the module now: o11y.Anonymous covers all three families, and o11y.IngestWire
// is the DSN-wire term the edge needs to match byte-for-byte.
//
// That agreement is still load-bearing. The gateway waives its JWT check on the
// ingest wires, so a request it lets through tokenless must not then be refused
// here for having no token; both sides read the same predicate now instead of
// two lists that agreed only by inspection. The three answers are still how you
// tell the hops apart if this regresses: a keyless POST to
// /v1/sentry/<uuid>/envelope/ answers 401 "invalid ingest key" (text/plain, from
// the ingest verifier) — NOT 404 (unrouted), and NOT the 403
// {"status":"error","msg":"no validated principal"} the READ paths return.

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
// (o11y.IngestWire); the reads stay gated.
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
	return mapped, o11y.IngestWire(method, mapped)
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
	// Native human-review surface (SQLite metastore) — /v1/o11y/reviews*.
	if err := mountAnnotationQueues(a, deps); err != nil {
		return err
	}
	// RUNTIME handler the wildcard delegates to (embed or proxy fallback).
	if err := mountRuntime(deps); err != nil {
		return err
	}
	// Hanzo Sentry product face /v1/sentry/* — the SIBLING of the /v1/o11y wildcard,
	// delegating to the SAME gated runtime handler (which carries the clean /v1/sentry
	// routes; the DSN-ingest routes are gate-exempt via o11y.IngestWire). One
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
	// Both obs claims on the ONE event door are published as PLANE OPS
	// (obs_rpc.go): analytics owns the route, this process owns the sink and the
	// runtime, and a package global cannot cross between two processes.
	exposeObs()
	// WRITE plane — order-independent (no /v1/o11y/* Fiber route): the ZAP
	// span+log receivers and the opt-in in-process trace sink, all writing
	// event.span / event.log (planesink.go).
	if err := mountPlaneIngest(deps); err != nil {
		return err
	}
	if err := mountProbes(deps); err != nil { // fleet health probes -> hanzo_service_up
		return err
	}
	// The way that gauge leaves this process. After mountProbes because it
	// publishes what they record, and before mountSummary because that read has no
	// answer until a collector has been able to take one. See exposition.go.
	startExposition(deps.Logger.New("subsystem", "o11y-exposition"))
	// PUBLIC status face GET /v1/summary — the outward projection of the gauge the
	// probes above record. After mountProbes because it reads what they write, and
	// before the terminal wildcard like every other specific route. Unauthenticated
	// and tenant-free by construction; see summary.go.
	mountSummary(a, deps)
	// TERMINAL sub-mount: the hanzoai/o11y module wildcard /v1/o11y/* — the runtime
	// route surface, delegating to the SAME gated handler mountRuntime installed via
	// o11y.SetHandler. Registered LAST (after every specific /v1/o11y/* route above) so
	// Fiber's in-order match gives those routes precedence over this catch-all. Folded
	// in HERE — it was a second, co-named Wire entry (o11ymod.Mount) — so
	// the observability plane is ONE `o11y` subsystem. /v1/o11y/health is unaffected:
	// it stays the generic always-ok route (the o11y Wire entry keeps OwnsHealth=false),
	// registered before MountAll and thus ahead of this wildcard.
	// Mount takes the router and nothing else: a route table is a value, and the
	// router it registers into already carries this deployment's logger. It used
	// to take Deps for that one field, which made github.com/hanzoai/o11y require
	// github.com/hanzoai/cloud — a module cycle, and the reason o11y's own
	// community binary could not link its own route declarations.
	if err := o11y.Mount(a); err != nil {
		return err
	}
	return nil
}

// shutdownO11y tears down the write-plane resources that hold process-lifetime
// connections, in REVERSE mount order — plane ingest (trace sink + receivers),
// event-ingest Datastore — so buffered spans/logs/rows flush before exit.
// Best-effort: the first error is returned but every teardown still runs.
// Idempotent and nil-safe.
func ShutdownO11y(ctx context.Context) error {
	stopProbes()
	stopExposition(ctx)
	var firstErr error
	if err := shutdownAnnotationQueues(); err != nil && firstErr == nil {
		firstErr = err
	}
	if err := shutdownPlaneIngest(ctx); err != nil && firstErr == nil {
		firstErr = err
	}
	if err := shutdownEventIngest(ctx); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}
