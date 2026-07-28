# apps/o11y — embedded o11y (scoped reads + query + ingest)

cloud embeds the o11y subsystem in-process against the shared Datastore
`datastore` (cluster `insights`). Three planes, one datastore.

## ONE registration, one public concept (decomplected)

The whole plane is registered as a SINGLE subsystem — one
`RegisterWithShutdown` of the name `o11y` (order 69, `mountO11y` / `shutdownO11y`,
`HealthOwner`) in `o11y.go`. `mountO11y` performs the ordered sub-mounts in-process:
`mountEventIngest` → `mountScope` → `mountRuntime` → `mountIngest` →
`mountTraceSink`. This replaced FIVE separately-registered subsystems
(`o11yscope` 69, `o11y-runtime` 71, `o11y-event-ingest` 68, `o11y-otlp-ingest`
72, `o11y-trace-inproc` 73) whose names leaked five public concepts (five config
toggles + five `/v1/<name>/health` routes). The k8s-style ordering was an internal
impl detail. Behavior is preserved EXACTLY: every specific `/v1/o11y/*` route still
registers inside the one order-69 mount, hence BEFORE the upstream `hanzoai/o11y`
wildcard (order 70) — so Fiber's in-order match still gives the specific routes
precedence. The upstream module ALSO registers the name `o11y` (order 70, the
wildcard); the two are co-owners of ONE concept, and this order-69 entry is
`HealthOwner` so `/v1/o11y/health` is registered exactly once (by the order-70
co-entry).

## Flat, version-less public surface (one /v1/, no nested /api/vN)

The public contract is FLAT — the upstream engine version is an internal
impl detail resolved inside the handlers, never leaked into a route:

- `/v1/o11y/{logs,metrics,status}` — tenant-scoped reads (`scope.go`).
- `/v1/o11y/vm/{query,query_range}` — SuperAdmin VictoriaMetrics proxy
  (`vmproxy.go`); the upstream `api/v1/*` VM path stays INSIDE the handler.
- `/v1/o11y/{query,query_range}` — the flat builder query (`query.go`); resolves
  to the v3 engine route INTERNALLY (the version-less alias would resolve to v5,
  which 400s the v3 composite payload the console speaks), delegating to the same
  gated runtime handler the wildcard uses.
- `/v1/o11y/sessions` — the flat, org-gated LLM-obs sessions list (`sessions.go`);
  pins the runtime's `/api/sessions` route (traces grouped by `session.id` on the
  gen_ai span plane) and refuses an org-less caller at the cloud boundary. Session
  DETAIL is composed CLIENT-side (list + traces filtered by session); the runtime
  serves only the list, so there is deliberately no `/sessions/:id` route.
- `/v1/o11y/annotation-queues*` — the NATIVE human-review queues (`annotation_queues.go`
  + `annotation_store.go`); see below.
- `/v1/o11y/{services,dependency_graph,dashboards,rules,…}` — resolved by the
  upstream module's version-less alias (highest engine version wins).

## Annotation queues (native, relational) — `annotation_queues.go` + `annotation_store.go`

The o11y span plane (llmobs) has flat annotations but NO queue entity, so the
human-review queues the console's `AnnotationQueuesModule` consumes are a
cloud-NATIVE relational feature on the o11y surface. Storage is Hanzo Base/SQLite
(`{DataDir}/o11y_annotations.db`, opened through `cek.Open` — encrypted at rest on
a capable build), the eval-metastore discipline: `MaxOpenConns(1)`, org column on
every table + mandatory predicate on every query, `project` narrows within org via
`principal.ProjectScope`. NOT the datastore span plane (queues are durable config,
not append-only telemetry). Registered by `mountAnnotationQueues` inside the one
order-69 mount, so every route precedes the order-70 wildcard.

Surface (lists return the console REST envelope `{data:[…], meta:{page,limit,
totalItems,totalPages}}`; a cross-org id is a 404, never a cross-tenant read):

- `GET/POST /v1/o11y/annotation-queues` — list (org+project) / create.
- `GET/PATCH/DELETE /v1/o11y/annotation-queues/:id` — detail (+ pending/completed
  counts + embedded items) / update name·description·scoreConfigIds / delete (+ items).
- `GET/POST /v1/o11y/annotation-queues/:id/items` — list (status filter, paged) / add
  items. An item references a TRACE·OBSERVATION·SESSION (the console-friendly
  `traceId`/`observationId`/`sessionId` form maps to `objectType`+`objectId`).
- `PATCH /v1/o11y/annotation-queues/:id/items/:itemId` — update status (PENDING↔
  COMPLETED, stamps `completedAt`) / assignee.

`scoreConfigIds` reference eval score-configs (`/v1/evals/score-configs`), stored as
opaque bounded ids (shape-validated only). Shutdown closes the store first in
`ShutdownO11y`.

Three planes, one datastore:

- **Scoped read plane** — `scope.go` + `logs.go` + `metricsread.go` + `status.go`
  + `productmap.go` + `vmquery.go`. The org-scoped, tenant-isolated
  `/v1/o11y/{logs,metrics,status}` (order **69**, before the wildcard at 70 so
  Fiber gives it precedence). The ONE owner of these three routes — folded in from
  the retired `clients/observe` so nothing was lost:
  - **logs**: two views over ONE store. A validated SuperAdmin (`c.IsAdmin()`, ==
    `owner==admin` after SanitizeIdentity) sees the raw infra stdout stream
    (`o11y_logs`, `resources_string['app']=<workload>`); every other org sees its
    OWN request stream derived from org-tagged spans
    (`o11y_traces`, `attributes_string['hanzo.org']=<org>`).
  - **metrics**: REAL per-org RED (rate/errors/p50/p95) from org-tagged request
    spans + the org's LLM usage from `hanzo.cloud_usage`. A SuperAdmin sees the
    whole-product RED (no org predicate); usage is always the caller's own org.
  - **status**: a live in-cluster health probe (allowlisted host — SSRF boundary)
    fused with VM `up{service}` inventory.
  - **Tenant isolation**: org is `principal.Tenant(c)`, bound as a positional
    Datastore parameter (never interpolated); the product is shape-validated
    (DNS-1123) then alias-mapped (console slug → workload) then allowlisted
    (`knownServices`) — a malformed slug is a 400, an unbacked one is honest-empty.
    Reuses the shared `aiobject.DatastoreQuery` (ONE datastore client).

- **Query / control plane** — `embed.go` + `o11y.go`. Constructs the o11y
  query runtime (`community.NewServer`) and serves the rest of `/v1/o11y/*` from
  this binary (dashboards, alerts, querier) via the upstream wildcard (order 70) +
  `o11y.SetHandler` (installed by `mountRuntime` inside the one order-69 mount).
  Falls back to reverse-proxying a standalone o11y Deployment when disabled/failed.
  READS Datastore via the branded `github.com/hanzo-ds/go` **v1.0.1**. `/v1/settings/:product`
  is NOT here — it is console product config, split out to `apps/settings`.

- **Ingest / write plane** — `ingest.go`. An in-process OpenTelemetry Collector
  that folds the standalone `otel-collector` Deployment into cloud. Accepts OTLP
  (gRPC :4317, HTTP :4318) and writes spans+logs into the same Datastore the
  query plane reads (`o11y_traces` / `o11y_logs`). Trimmed pipeline:
  `otlp -> memory_limiter, resource(namespace=hanzo, env), batch -> {clickhousetraces, clickhouselogsexporter}`.
  - **OFF by default.** Enable with `CLOUD_OTLP_INGEST_ENABLED=true` (+ a
    datastore DSN). Fail-soft: any error leaves the standalone collector as the
    ingest path. Registered with a ShutdownFunc so batches flush on stop.
  - DSN rides `${env:CLOUD_OTLP_INGEST_DSN}` (envprovider) — never written to
    disk. `service.telemetry.metrics.level=none` so the collector binds ONLY
    :4317/:4318 (no :8888/:8889/:13133) — avoids the :9090 class of clash.

## Metrics ingest is DEFERRED (driver-fork conflict — do not "fix" naively)

The metrics write path (the datastore metrics exporter + the `o11yspanmetrics`
connector) is intentionally NOT embedded. That exporter references the upstream
**dd-sketch fork** of ch-go (`chproto.DD/Store/IndexMapping`), which does NOT
compile against cloud's `hanzo-ds/native` v0.72.0 / `hanzo-ds/go` v1.0.1 (verified:
`undefined: chproto.DD` etc.). The two driver lines cannot coexist in one binary
because the o11y QUERY plane pins upstream. Traces + logs exporters DO compile
against upstream and are embedded.

Consequence: `otel-collector` cannot be fully ripped yet — 16 `otel-agent` pods
(+ the logs-agent) forward metrics to it over ZAP :4319, and cloud can't persist
metrics without the fork. Full rip requires porting the metrics exporter onto
upstream ch-go (or aligning drivers). Until then the standalone collector stays
for metrics; cloud takes traces+logs.

## cloud's own telemetry — split host / sink

The **host** owns the tracer + meter providers (`cloud/telemetry.go`,
`cloud.InstallTelemetry`, called by `cloud.Serve` before `MountAll` and by
`cmd/o11y` for its own process). This package owns the SINK.

That split is not cosmetic. The provider used to be BUILT here and handed to the
host through `cloud.RegisterTelemetryInstaller` from this package's `init()`.
That only worked while o11y was LINKED INTO the host. When o11y became a plugin
(`cloud.PluginSpec` → `zip.Load`, its own binary) the `init()` ran in the CHILD,
the host's installer stayed nil, `installTelemetry` became a genuine no-op, and
tracing went dark fleet-wide — including the `gen_ai` spans it adopted into the
ai module. The provider is a host concern (every request the host serves needs a
span) and now lives there; only the collector, the datastore exporter and the
SDK→pdata conversion (`spanconv.go`) stayed, because only those are coupled to
the `o11y_index_v3` schema. Host root graph cost of the move: **+6 packages**
(644 → 650 — the OTel trace SDK + `luxfi/trace`); `go.opentelemetry.io/collector`
and `hanzoai/ai/object` stay at 0 in the host.

The ai adoption (`aiobject.AdoptHostTracerProvider`) moved to
`cloud/apps/install.go`, beside the other `aiobject.Set*` calls, gated on
`cloud.TracerProviderInstalled()` — `apps/` already links ai; the host must not
(`hanzoai/ai/object` is 1270 packages).

## cloud's own spans → in-process trace sink (`tracesink.go`)

The dogfood: cloud's OWN spans (service + ai GenAI/LLM-obs) reach the embedded
Datastore trace store WITHOUT a socket, via the ZAP locality-adaptive **Router**
(`github.com/luxfi/zap`: `Router`/`InProcessInterface`/`Destination`/`Payload`).
One Send API, Cost-table routing — not a caller branch. **The router lives in the
host now** (`cloud/telemetry.go`); this package registers a handler on it.

- Seam: `cloud.RegisterTraceSink(cloud.TraceSink)` where
  `TraceSink = func(ctx, []sdktrace.ReadOnlySpan) error`. `mountTraceSink`
  registers `traceSink(exp)`; `shutdownTraceSink` registers nil. The payload is
  SDK spans, not pdata, so the conversion — and therefore the collector import —
  stays on this side.
- **Same code, both deployments.** Registration is process-local. Linked in, the
  host's `Send` to `traceDest` ("hanzo.o11y.traces") finds this handler and hands
  over the LIVE batch by value (zero serialize, zero socket, no second collector
  hop). As a plugin, the handler is registered in the CHILD, the host's router
  returns `ErrNoRoute`, and the identical `Send` falls through to the ZAP wire
  (`luxfi/trace` → this package's `zapreceiver` on :4317). Neither producer nor
  exporter branches on where o11y lives.
- The handler converts SDK spans → collector pdata **in place** (`spanconv.go`;
  no proto, no marshal, no OTLP) and writes via the REAL `dstraces` exporter
  (`ConsumeTraces`), the one writer that produces the `o11y_index_v3` schema the
  query plane reads. The pdata→SpanV3 conversion is unexported there, so the sink
  reuses the exporter as a `consumer.Traces` rather than duplicating ~90 lines of
  schema-coupled conversion.
- **OPT-IN + fail-soft.** Mounts (via `mountTraceSink` in the one order-69 mount)
  only when `O11Y_TRACES_ZAP_INPROCESS` is truthy AND a datastore DSN is set.
  `cloud.TraceInprocEnabled()` is the ONE gate, read by both the sink (whether to
  mount) and the host producer (whether to install a provider with no wire
  endpoint). Any construction error leaves cloud's spans on the wire — activating
  it can never take cloud down. Shutdown deregisters the handler then flushes the
  exporter's sending queue.
- Boot window: the provider installs before `MountAll` but the sink registers at
  mount (order 69); spans in between take the wire fallback, else surface
  `ErrNoRoute` (visible, never a silent drop). Steady state is in-process. A ZAP
  `NodeInterface` for traces slots behind the same Send call site with no producer
  change.
- Covered by `TestTracing_EndToEnd_WithO11yLinkedIn` (this pkg — real provider,
  real `RegisterTraceSink`, real `traceSink`, asserts the converted pdata and its
  resource) and the routing/Cost-table tests in `cloud/telemetry_test.go`.
