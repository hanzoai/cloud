// Package o11y is your logs, metrics and traces: ship them in, query them, chart them.
//
// It is the ONE owner of the cloud binary's observability plane — registered as a
// SINGLE `o11y` subsystem (this file's init) that internally mounts, in the
// load-bearing order, every part of the concept:
//
//	READ/SERVE plane (specific /v1/o11y/* routes, registered BEFORE the
//	hanzoai/o11y wildcard so Fiber's in-order match gives them precedence):
//	  - tenant-scoped reads  /v1/o11y/{logs,metrics,status}   (scope.go)
//	  - fleet availability   /v1/o11y/availability             (availability.go)
//	  - flat builder query   /v1/o11y/{query,query_range}      (query.go)
//	  - event ingest         POST /v1/event/ingestion          (event_ingest.go)
//	  - Sentinel consumer    the event.error durable that files issues (errorsink.go)
//	RUNTIME handler the hanzoai/o11y wildcard (order 70) delegates to via
//	  module.SetHandler — the in-process runtime (embed.go) or a reverse-proxy
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
// Path is preserved verbatim: /v1/o11y/* reaches the o11y runtime unchanged, and
// the runtime registers every route at its full public path and strips no prefix
// (see hanzoai/o11y app.createPublicServer). So the address a caller asks for is
// the address that matches, and an address the runtime does not serve matches
// nothing rather than something adjacent. The gateway terminates auth and
// propagates identity as X-* headers; the runtime (embedded) or the proxy
// (fallback) sees the same request.
package o11y
