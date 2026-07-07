# clients/o11y — embedded o11y (query + ingest)

cloud embeds the o11y subsystem in-process against the shared ClickHouse
`datastore` (cluster `insights`). Two halves, one datastore:

- **Query / control plane** — `embed.go` + `o11y.go`. Constructs the SigNoz
  query runtime (`community.NewServer`) and serves `/v1/o11y/*` from this binary
  (dashboards, alerts, querier). Falls back to reverse-proxying a standalone
  o11y Deployment when disabled/failed. READS ClickHouse via clickhouse-go
  **v2.44.0** (upstream).

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
