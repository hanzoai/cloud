// Package o11y is the ONE owner of the cloud binary's observability plane —
// registered as a SINGLE `o11y` subsystem (this file's init) that internally
// mounts, in the load-bearing order, every part of the concept:
//
//	READ/SERVE plane (specific /v1/o11y/* routes, registered BEFORE the
//	hanzoai/o11y wildcard so Fiber's in-order match gives them precedence):
//	  - tenant-scoped reads  /v1/o11y/{logs,metrics,status}   (scope.go)
//	  - SuperAdmin VM proxy  /v1/o11y/vm/{query,query_range}   (vmproxy.go)
//	  - flat builder query   /v1/o11y/{query,query_range}      (query.go)
//	  - LLM-obs event ingest POST /v1/o11y/ingestion           (event_ingest.go)
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
	"github.com/hanzoai/o11y"
	"github.com/zap-proto/zip"
)

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

// gate refuses any request that carries no validated principal before it reaches
// the o11y runtime. The bare reverse proxy forwards ALL inbound headers upstream,
// including a client-forged X-Org-Id restored on the bearer-less path; without
// this an off-gateway caller reads another tenant's telemetry/logs. X-User-Id is
// set ONLY by the identity middleware from a verified credential (the same signal
// principal.Validated uses), so its presence is the authoritative principal gate.
// Every legitimate /v1/o11y/* caller arrives through the console BFF with a
// user-bound bearer, so this refuses only the anonymous-forge path.
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
		if !isHealthPath(r.URL.Path) && !isErrorIngestPath(r.Method, r.URL.Path) && !isSentryIngestPath(r.Method, r.URL.Path) && strings.TrimSpace(r.Header.Get("X-User-Id")) == "" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"status":"error","msg":"no validated principal"}`))
			return
		}
		next.ServeHTTP(w, r)
	})
}

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

// mountO11y is the ONE mount for the whole observability concept. It performs the
// ordered sub-mounts in-process so the public registry carries a single `o11y`
// name. Every cloud-native /v1/o11y/* route is registered here — inside this one
// order-69 mount, hence BEFORE the hanzoai/o11y wildcard (order 70) — so Fiber's
// in-order match gives the specific routes precedence over the runtime proxy.
func MountO11y(a *zip.App, deps cloud.Deps) error {
	// READ/SERVE plane — specific routes before the wildcard.
	if err := mountEventIngest(a, deps); err != nil { // POST /v1/o11y/ingestion
		return err
	}
	mountScope(a) // GET logs/metrics/status + vm/{query,query_range} + flat builder query + sessions
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
