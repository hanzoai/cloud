# clients/o11y — embedded o11y (scoped reads + query + ingest)

cloud embeds the o11y subsystem in-process against the shared ClickHouse
`datastore` (cluster `insights`). Three planes, one datastore:

- **Scoped read plane** — `scope.go` + `logs.go` + `metricsread.go` + `status.go`
  + `productmap.go` + `vmquery.go`. The org-scoped, tenant-isolated
  `/v1/o11y/{logs,metrics,status}` (order **69**, before the wildcard at 70 so
  Fiber gives it precedence). The ONE owner of these three routes — folded in from
  the retired `clients/observe` so nothing was lost:
  - **logs**: two views over ONE store. A validated SuperAdmin (`c.IsAdmin()`, ==
    `owner==admin` after SanitizeIdentity) sees the raw infra stdout stream
    (`signoz_logs`, `resources_string['app']=<workload>`); every other org sees its
    OWN request stream derived from org-tagged spans
    (`signoz_traces`, `attributes_string['hanzo.org']=<org>`).
  - **metrics**: REAL per-org RED (rate/errors/p50/p95) from org-tagged request
    spans + the org's LLM usage from `hanzo.cloud_usage`. A SuperAdmin sees the
    whole-product RED (no org predicate); usage is always the caller's own org.
  - **status**: a live in-cluster health probe (allowlisted host — SSRF boundary)
    fused with VM `up{service}` inventory.
  - **Tenant isolation**: org is `principal.Tenant(c)`, bound as a positional
    ClickHouse parameter (never interpolated); the product is shape-validated
    (DNS-1123) then alias-mapped (console slug → workload) then allowlisted
    (`knownServices`) — a malformed slug is a 400, an unbacked one is honest-empty.
    Reuses the shared `aiobject.DatastoreQuery` (ONE datastore client).

- **Query / control plane** — `embed.go` + `o11y.go`. Constructs the o11y
  query runtime (`community.NewServer`) and serves the rest of `/v1/o11y/*` from
  this binary (dashboards, alerts, querier) via the wildcard (order 70) +
  `o11y.SetHandler` (order 71). Falls back to reverse-proxying a standalone
  o11y Deployment when disabled/failed. READS ClickHouse via clickhouse-go
  **v2.44.0** (upstream). `/v1/settings/:product` is NOT here — it is console
  product config, split out to `clients/settings`.

- **Ingest / write plane** — `ingest.go`. An in-process OpenTelemetry Collector
  that folds the standalone `otel-collector` Deployment into cloud. Accepts OTLP
  (gRPC :4317, HTTP :4318) and writes spans+logs into the same ClickHouse the
  query plane reads (`signoz_traces` / `signoz_logs`). Trimmed pipeline:
  `otlp -> memory_limiter, resource(namespace=hanzo, env), batch -> {clickhousetraces, clickhouselogsexporter}`.
  - **OFF by default.** Enable with `CLOUD_OTLP_INGEST_ENABLED=true` (+ a
    datastore DSN). Fail-soft: any error leaves the standalone collector as the
    ingest path. Registered with a ShutdownFunc so batches flush on stop.
  - DSN rides `${env:CLOUD_OTLP_INGEST_DSN}` (envprovider) — never written to
    disk. `service.telemetry.metrics.level=none` so the collector binds ONLY
    :4317/:4318 (no :8888/:8889/:13133) — avoids the :9090 class of clash.

## Metrics ingest is DEFERRED (driver-fork conflict — do not "fix" naively)

The metrics write path (`signozclickhousemetrics` + the `o11yspanmetrics`
connector) is intentionally NOT embedded. That exporter references SigNoz's
**dd-sketch fork** of ch-go (`chproto.DD/Store/IndexMapping`), which does NOT
compile against cloud's upstream ch-go v0.71.0 / clickhouse-go v2.44.0 (verified:
`undefined: chproto.DD` etc.). The two driver lines cannot coexist in one binary
because the o11y QUERY plane pins upstream. Traces + logs exporters DO compile
against upstream and are embedded.

Consequence: `otel-collector` cannot be fully ripped yet — 16 `otel-agent` pods
(+ the logs-agent) forward metrics to it over ZAP :4319, and cloud can't persist
metrics without the fork. Full rip requires porting the metrics exporter onto
upstream ch-go (or aligning drivers). Until then the standalone collector stays
for metrics; cloud takes traces+logs.

## cloud's own telemetry transport

`cmd/cloud/telemetry.go` prefers ZAP (canonical cross-service wire). When no ZAP
endpoint is set but an OTLP endpoint is, it ships spans over OTLP-HTTP — the path
to LOOP BACK to this in-process ingest at `localhost:4318` once cutover happens.

## cloud's own spans → in-process trace sink (`tracesink.go`)

The dogfood: cloud's OWN spans (service + ai GenAI/LLM-obs) reach the embedded
ClickHouse trace store WITHOUT a socket, via the ZAP locality-adaptive **Router**
(`github.com/luxfi/zap` v1.2.1: `Router`/`InProcessInterface`/`Destination`/
`Payload`). One Send API, Cost-table routing — not a caller branch.

- `Router` (this pkg, exported) carries a Cost-0 `InProcessInterface`. cmd/cloud's
  tracer provider installs an exporter (`NewTraceExporter`) whose `otlptrace.Client`
  `Send`s every batch to `TraceDest` ("hanzo.o11y.traces"). When this sink is
  mounted the Router delivers the LIVE `[]*tracepb.ResourceSpans` by value (zero
  ZAP-wire serialize, zero socket, no second collector hop); else `ErrNoRoute` and
  the producer falls back to the ZAP wire client.
- The handler bridges SDK-exporter proto spans → collector pdata (one in-memory
  OTLP round-trip — the pdata proto pkg differs but the OTLP wire is identical) and
  writes via the REAL `chtraces` exporter (`ConsumeTraces`), the one writer that
  produces the `o11y_index_v3` schema the query plane reads. The pdata→SpanV3
  conversion is unexported, so the sink reuses the exporter as a `consumer.Traces`
  rather than duplicating ~90 lines of schema-coupled conversion.
- **OPT-IN + fail-soft.** Mounts (order 73) only when `O11Y_TRACES_ZAP_INPROCESS`
  is truthy AND a datastore DSN is set (`TraceInprocEnabled()` is the ONE gate both
  the sink and the producer read). Any construction error leaves cloud's spans on
  the wire — activating it can never take cloud down. Shutdown deregisters the
  handler then flushes the exporter's sending queue.
- Boot window: the provider installs early (initTelemetry) but the sink registers
  at mount (order 73); spans in between take the wire fallback, else surface
  ErrNoRoute (visible, never a silent plaintext downgrade). Steady state is
  in-process. A P2 ZAP `NodeInterface` for traces slots behind the same Send call
  site with no producer change.
