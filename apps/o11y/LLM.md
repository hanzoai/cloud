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

## The o11y pin is BLOCKED at v1.5.34 — do not bump it alone

`go.mod` pins `github.com/hanzoai/o11y v1.5.34`. v1.5.37 renamed the module's
INTERNAL route literals (`/api/vN/<rest>` → `/v1/o11y/<rest>`) and deleted
`mount.go`'s `rewriteExternalPath`. The PUBLIC contract did not move — the seam
existed precisely so `/v1/o11y/<resource>` stayed fixed while the internals
churned — so nothing in production broke, and nothing here needs "fixing" to
restore a path. What the bump does break is this package's three in-handler
forwards, which name the internal spelling:

- `sessions.go` forwards to `/api/sessions`; at v1.5.37+ that list is
  `/v1/o11y/llm/sessions` (llmobs moved under `/llm/` to stop `sessions` and
  `users` colliding with the auth/IAM nouns that already own those words).
- `query.go` forwards to `/api/v3/<resource>` — **this is the blocker**.
  v1.5.37 stopped mounting the v3 builder POST route
  (`RegisterQueryRangeV3Routes` no longer registers `/query_range`;
  `QueryRangeV3` lost its last reference, and `queryRangeV3`/`queryRangeV4`
  survive only as unreferenced methods). `/v1/o11y/query_range` is now served by
  the **v5** querier alone, and v5 refuses the console's composite with
  `unknown field "queryType" in composite query` — asserted against the pinned
  module in `query_test.go`. So the bump turns every console trace/log explorer
  into a 400. There is no cloud-only edit that avoids this: the engine the
  console speaks to no longer has a route.
- `o11y.go`'s `isHealthPath` allowlisted `/api/v{1,2}/{health,healthz,readyz,livez}`;
  v1.5.37 moved the probes to `/v1/o11y/{healthz,readyz,livez}`. **This one shipped.**
  The pin reached v1.5.46 while that list stayed, so the exemption named four
  addresses nothing served and the gate refused EVERY public op — `/version`,
  `/health`, the three probes, sign-in and the shared-dashboard reads all answered
  `403 {"status":403,"error":"no validated principal"}` at api.hanzo.ai while
  o11y.hanzo.ai served them 200. That gap is the whole reason o11y still had a door
  of its own. FIXED by deleting the list: `gate()` asks `o11y.Anonymous(method, path)`,
  which lives beside the routes it describes, and `anonymous_test.go` there fails on
  any exemption that names a path Mount does not register.

`o11y.go`'s other forward, `eventToRuntimePath` (`/v1/event/<p>/envelope|store`
→ `/v1/sentry/<p>/…`), is unaffected: both families survive the rename verbatim.
The DSN ingest wires (`/v1/o11y/api/<project>/envelope|store` and the clean
`/v1/sentry/<project>/…`) also stay — that `/api/` segment is the Sentry SDK's own
wire format, received as-is, not our spelling of a route — and they are now
`o11y.IngestWire`, exported precisely because the gateway's JWT bypass must match
it byte-for-byte.

## The published document still lacks o11y's 353 typed ops

`plugin/o11y/openapi.json` is the build-time subset the fleet document is woven
from, and it is STALE: 34 operations, including a `/v1/o11y/{wildcard1}` and three
`/api/v2` probes that v1.5.46 deleted. Regenerating it (`make -C apps/o11y describe`)
now succeeds and yields 389 operations — but the weave then refuses, and it is
right to:

    schema "Service" means different things in "ingress" and "o11y"

Six schema names collide across the fleet with DIFFERENT shapes, all six from
o11y's internal type packages: `Account` (cloudintegrationtypes), `Channel`
(alertmanagertypes), `Event` (spantypes/sentrytypes), `Host` (zeustypes),
`Service` (cloudintegrationtypes) and `TLSConfig`. The fleet's schema namespace is
flat and `openapi/weave.go` fails closed on one name with two shapes, because a
generated SDK would bind whichever it read last. Unblocking the document means
namespacing those six Go type names in hanzoai/o11y — the wire does not move, only
the type names — then regenerating the subset and reweaving `openapi.yaml`.

Unblocking it is a console change, not a cloud one: migrate `listQueryPayload`
+ `parseListRows` (hanzoai/console `src/lib/api/apm.ts`) to the v5 composite
(`{schemaVersion, requestType, compositeQuery:{queries:[…]}}`), then bump, then
DELETE `builderQueryHandler` outright — once the shapes agree the forward is an
identity rewrite of the path it is already registered on, and the order-70
wildcard serves it. Restoring the v3 route upstream is the wrong direction: it
resurrects a second spelling of one noun and undoes a deliberate deletion.

The bump was rehearsed end to end before being refused — `plugin/o11y` built at
v1.5.38 with all three forwards updated, run against an upstream carrying
v1.5.38's route table and its REAL v5 request decoder. `/v1/o11y/sessions` → 200
and the llmobs reads → 200, so those two forwards are correct and ready; the
console's composite came back `400 unknown field "queryType" in composite query`
at the wire. That is the whole bump: two sites fix cleanly, one has no target,
and they ship together or not at all.

The rehearsal also answers whether `rewriteExternalPath` should survive. It is
o11y's (`mount.go`), never cloud's, so there is nothing here to delete — but it
was NOT an identity function at v1.5.34: an unknown path arrived upstream as
`/api/zzz-not-a-route`, rewritten. At v1.5.38 the same request arrives verbatim.
Internal and external spellings having converged is exactly what made it an
identity rewrite, which is why the rename deleted it in the same commit. The
only paths that still legitimately carry `/api/` are the Sentry SDK's own wire
form (above) — its protocol, not our route.

Worth knowing for any future seam: cloud PROPAGATES the runtime's status. A
forward to a dead internal path surfaces as a real 404/400, not a 200-shaped
envelope wrapping one, so this class of drift fails loudly at the client.

## Typed ops: 12 of the 20 routes, and why the other 8 cannot be

Every route this package OWNS the shape of is a typed op (`zip.Get[In,Out]` and
friends), declared on the `/v1/o11y` group so the prefix is part of each op's
path — one registry entry, and the OpenAPI schema, the MCP tool, the CLI command
and the SDK method all follow from it. The prose in each handler's doc comment IS
the published description: `zipdoc` lifts it into `zipdoc_gen.go` (committed;
regenerated by `make -C apps/o11y openapi` and the Dockerfile), because Go drops
comments at compile time.

- typed: `GET /v1/o11y/{logs,metrics,status}` (scope.go), the eight
  `/v1/o11y/annotation-queues*` routes, and `POST /v1/o11y/ingestion`.
- **`POST /v1/o11y/ingestion` is typed and reaches NO consumer**, because being
  typed is necessary and not sufficient: the op has to be REGISTERED in the
  process that writes the document. `mountEventIngest` returns early when
  `embeddedDSN()` is empty and again when `newDatastoreSink` cannot ping, so
  `zip.Post(g, o11yIngestLeaf, o.ingest)` never runs without a reachable Hanzo
  Datastore — and `make -C apps/o11y openapi` runs `bin/o11y openapi` with
  `GIT_SSH_ADDR` and nothing else (mk/plugin.mk). The generator says so itself:
  `o11y event ingest: no Datastore DSN; write path unmounted`. So the LLM-obs
  write path — the one route that ingests traces/observations/scores — is in
  neither `plugin/o11y/openapi.json` nor the woven `openapi.yaml`, and therefore
  has no SDK method, no MCP tool, no CLI command and no published schema. Its op,
  its In/Out and its zipdoc prose all exist and project nowhere.
  This is NOT the "spec varies per deployment" property working as intended: that
  property is honest when the generating process resembles a deployment, and this
  one resembles none — every real o11y deployment sets the DSN. Closing it is a
  BEHAVIOUR decision, not a description one, and that is why it is still open:
  `zip.Post` registers a fiber route and a registry entry inseparably
  (`registerTyped` ends in `app.fiber.Add`), so making the op visible necessarily
  makes the path stop falling through to the order-70 wildcard. Whoever takes it
  owns that wire change; typing cannot.
  MEASURED, so the size of that decision is known rather than assumed: **the
  fallthrough serves nothing.** The pinned runtime (`hanzoai/o11y v1.5.34`)
  registers no `/ingestion` route at all — its only ingest-named paths are the
  unrelated `/api/v2/gateway/ingestion_keys*` — and a no-DSN process cannot init
  the embed either, so `mountRuntime` installs the reverse-proxy fallback and the
  request lands on the same server build, which has no such route. The wire change
  on the table is therefore **404 → an honest 503**, with no working write path at
  risk; it is NOT "remote ingest stops working", which is what "stops falling
  through" reads like and is the reason this looked more expensive than it is.
  Re-measure before acting on it:

      grep -rE '"/[^"]*ingest[^"]*"' \
        "$(go env GOMODCACHE)/github.com/hanzoai/o11y@v1.5.34" --include='*.go'

  This gap is GATED too — `TestIngestOpIsTypedButUnreachableWithoutADSN` proves
  BOTH halves on the real code: it registers the op on a throwaway app and reads
  it out of zip's registry (so "it is a typed op, with prose" is measured, not
  claimed), then asserts the DSN-less `MountO11y` router does not carry it. The
  moment somebody closes it, that test goes red and names the wire change and this
  paragraph, so the decision cannot land as a silent side effect.
- **`cloud.Bridge()` is installed by `MountO11y` on the `/v1/o11y` group, first.**
  Not optional and not redundant with `cloud.Serve`: o11y runs as its OWN process
  (`plugin/o11y/main.go` builds a bare `zip.App`), and the host's context does not
  cross the socket — so without this install every typed op here 403s a caller the
  host already validated. Pinned by
  `TestTypedOpsResolveTheirOrgThroughTheBridge`.
- The tenant is `principal.OrgFrom(ctx)` and the status probe's weaker gate is
  `principal.ValidatedFrom(ctx)` — the two facts `cloud.Bridge` parks — NEVER an
  `In` field. Platform-sudo-ness and the project scope do need the REQUEST (they
  ride in headers neither reader carries), so they go through `typed.go` — the ONE
  `cloud.Request` seam in this package, pinned in cloud's `allowedRequestUses`.

The other 8 stay untyped because typing them would MOVE the wire, which typing is
not allowed to do. **The refusals are GATED, not prose** (`typed_wire_test.go`):
`untypedByDesign` is the closed list, keyed the way the DOCUMENT writes each
address, and `TestEveryRouteIsTypedOrNamed` reads the live router of the REAL
`MountO11y` — so a route added anywhere in that mount is typed by default, and
dropping one out of the registry takes a deliberate edit with a reason. The stale
direction is gated too: a name for an operation o11y no longer serves is red.
`TestEveryTypedOpIsDescribed` holds the prose to the same bar (an op added without
regenerating `zipdoc_gen.go` is a nameless MCP tool), and
`TestUntypedRoutesKeepTheirWire` measures the three wire facts the reasons below
CLAIM — the `text/plain` receipt, the 200 over a body that is not JSON, and the
`text/plain` replay — so the refusals are evidence, not assertion. Prose cannot go
red; that is why this list was a promise until the gate existed.

- `GET /v1/o11y/vm/{query,query_range}` — return VictoriaMetrics' own status code
  and its Prometheus envelope VERBATIM (`c.Bytes(status, body)`). A typed op
  answers its declared status and marshals a Go value, so a VM 4xx would become a
  200 and the envelope would be re-shaped.
- `POST /v1/o11y/{query,query_range}` and `GET /v1/o11y/sessions` — reverse
  proxies: request body, query string, upstream status, headers and body all ride
  through untouched. There is no Go type for "whatever the runtime answered".
- `GET /v1/o11y/alerts/last` and `POST /v1/o11y/alerts/:receiver` — `text/plain`,
  not JSON, and the POST deliberately ACCEPTS an unparseable body (it is a
  delivery receipt: a body that will not parse still proves delivery, and a 400
  would make Alertmanager retry forever). A typed op would answer JSON and reject
  the malformed body.
- `ALL /v1/sentry/*` — a wildcard proxy; a wildcard has no operation to type.

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

- **Ingest / write plane** — `planesink.go`. A ZAP span receiver (:4317) and log
  receiver (:4318) decode the same wire the retired embedded collector bound, and
  a native `datastoreSink` appends prepared batches to the EVENT PLANE —
  `event.span` / `event.log`, the same 15-column envelope `event.event` /
  `event.error` / `event.metric` share. The `o11y_*` databases are closed.
  - The whole otelcol pipeline (`ingest.go` + `zapingest.go` + `spanconv.go` +
    `tracesink.go`) was DELETED, not ported: it existed to feed the SigNoz-schema
    exporters, and with the plane as the store the translation layer has no job.
  - **Bound by CAPABILITY, not a flag** — ingest runs exactly when a datastore DSN
    is configured, because the DSN is the thing it writes to. Fail-soft at every
    branch; a bad telemetry config can never take cloud down.
  - Row identity is DERIVED (a span's id is its span_id, a log line's a content
    hash salted by batch position), so ReplacingMergeTree idempotency is structural.
  - `service` is the WORKLOAD, resolved in ONE place (`planeService`) for spans and
    logs alike: `service.name`, else the wire app name, else the legacy `app` label,
    else OTel's k8s derivation (`k8s.deployment.name` → replicaset → statefulset →
    daemonset → cronjob → job → container). That last leg is not optional — the
    fleet's biggest log producer is the otel-agent's filelog receiver, which stamps
    `k8s.*` and NO `service.name`, and every reader keys on this column.

## Metrics ingest — LANDED, natively (`metrics.go`)

Metrics were deferred once because the SigNoz metrics exporter needed a **dd-sketch
fork** of ch-go that cannot coexist with the upstream driver the query plane pins.
The fork is not needed: `startNativeMetricsIngest` runs a ZAP metric receiver
(`O11Y_METRICS_ZAP_LISTEN`, `:4319`) that decodes `MsgMetricBatch` and writes via
o11y's `pkg/datastoremetrics` over UPSTREAM ch-go, into `event.series` /
`event.metric`. Classic bucket/quantile decomposition, no DDSketch, one datastore
connection shared with the query plane.

That native writer is the shape `planesink.go` copied for spans and logs — a
receiver decodes the wire, a writer appends a prepared batch. **One write path per
signal, all four the same shape, all four on the event plane.**

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

## cloud's own spans → the plane (`planesink.go`)

The dogfood: cloud's OWN spans (service + ai GenAI/LLM-obs) reach `event.span`
WITHOUT a socket, via the ZAP locality-adaptive **Router** (`github.com/luxfi/zap`:
`Router`/`InProcessInterface`/`Destination`/`Payload`). One Send API, Cost-table
routing — not a caller branch. **The router lives in the host**
(`cloud/telemetry.go`); this package registers a handler on it.

- Seam: `cloud.RegisterTraceSink(cloud.TraceSink)` where
  `TraceSink = func(ctx, []sdktrace.ReadOnlySpan) error`. `mountPlaneIngest`
  registers it; `shutdownPlaneIngest` registers nil. The payload is SDK spans —
  and now it stays SDK spans: `sdkSpanRowsOf` renders them straight to
  `event.span` rows, one converter fewer than the retired pdata detour, sharing
  every helper (`planeOrg`, `planeService`, `planeSpanKind`, `planeStatus`) with
  the wire path.
- **Same code, both deployments — but the wire needs an ADDRESS.** Registration is
  process-local. Linked in, the host's `Send` to `traceDest` ("hanzo.o11y.traces")
  finds this handler and hands over the LIVE batch by value (zero serialize, zero
  socket). As a plugin, the handler is registered in the CHILD, the host's router
  returns `ErrNoRoute`, and the identical `Send` falls through to the ZAP wire.
  - Cloud runs its ~20 subsystems as sibling plugin PROCESSES in one pod, so
    "co-resident" means loopback for all but one of them. `wireEndpointFor`
    (host, `telemetry.go`) resolves that: explicit `OTEL_EXPORTER_ZAP_ENDPOINT`,
    else the fleet collector when a legacy OTLP endpoint declares remote intent,
    else `127.0.0.1:4317` — `planeSpanListen` — whenever
    `O11Y_TRACES_ZAP_INPROCESS` says a sink exists in this DEPLOYMENT. Without
    that last leg the siblings had neither a route nor an endpoint and every span
    they produced was dropped, which is precisely how `event.span` stayed empty
    while five migrated read paths queried it. The process HOLDING the sink is
    routed and never reaches the fallback, so it cannot self-dial.
- **Capability-bound + fail-soft.** `mountPlaneIngest` (in the one order-69 mount)
  starts when a datastore DSN is set; the in-process sink additionally honours
  `cloud.TraceInprocEnabled()` — the ONE gate, read by both the sink (whether to
  register) and the host producer (whether a wire address is even needed). Any
  construction error leaves cloud's spans on the wire; activating it can never
  take cloud down. Shutdown deregisters the handler, stops the listeners, closes
  the connection.
- Boot window: the provider installs before `MountAll` but the sink registers at
  mount (order 69); spans in between take the wire fallback, else surface
  `ErrNoRoute` (visible, never a silent drop).
- Covered by `planesink_test.go` (the pure row builders — wire span, wire log, SDK
  span, the k8s workload derivation and the filelog regression it closes, with a
  column-count guard pinning each row to its column list) and the routing /
  Cost-table / `wireEndpointFor` tests in `cloud/telemetry_test.go`.
