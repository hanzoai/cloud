# HIP-1348: Unified Agent Execution Tracing Under One Task ID (OpenTelemetry GenAI)

**Tracking Issue**: [`hanzoai/cloud#29`](https://github.com/hanzoai/cloud/issues/29) (`[p1, hip-1348, perf, research, bounty]`)
**Status**: Reproducible Research & Bounded Clean-Room Implementation (`apps/agenttrace`, `cmd/agenttrace-bench`)
**Raw Results Artifact**: [`results.json`](./results.json)

---

## 1. Primary Reference, Summary & License Audit

### Primary Specification & Standards Referenced
1. **OpenTelemetry GenAI Semantic Conventions**: [`open-telemetry/semantic-conventions` — GenAI Docs](https://github.com/open-telemetry/semantic-conventions/tree/main/docs/gen-ai) (tracked originally via [`open-telemetry/semantic-conventions-genai`](https://github.com/open-telemetry/semantic-conventions-genai/blob/main/docs/gen-ai/README.md)).
2. **W3C Trace Context Level 1**: [`https://www.w3.org/TR/trace-context/`](https://www.w3.org/TR/trace-context/) (`traceparent` and `tracestate` wire propagation).
3. **OpenTelemetry Protocol (OTLP/JSON)**: [`opentelemetry-proto` `ExportTraceServiceRequest`](https://github.com/open-telemetry/opentelemetry-proto/blob/main/opentelemetry/proto/trace/v1/trace.proto).

### Summary
Autonomous coding and research agents in Hanzo traverse six distinct subsystems during a single user task: **Model inference** (`Enso` routing + `Zen` generation), **Retrieval** (`hanzoai/index` & `hanzoai/graph`), **MCP tools** (`hanzoai/mcp`), **VFS & Object Storage** (`hanzoai/vfs` -> `hanzoai/s3`), **Sandbox execution** (`hanzoai/visor`), and **Billing & Cloud metering** (`hanzoai/cloud`). Without unified correlation under one `task_id` and W3C `trace_id`, debugging latency regressions, auditing tool calls, and reconciling billed micro-USD cost against emitted subsystem usage remain fragmented across disjoint logs.

This submission delivers a clean-room Go implementation (`apps/agenttrace`) following OpenTelemetry GenAI Semantic Conventions (`gen_ai.operation.name`, `gen_ai.system`, `gen_ai.request.model`, `gen_ai.usage.input_tokens`, `gen_ai.usage.output_tokens`, `gen_ai.tool.name`, `gen_ai.tool.call.id`), W3C `traceparent`/`tracestate` propagation, default-on DLP redaction, fail-closed multi-tenant isolation, 1:1 integer micro-USD billing reconciliation, and a bounded non-blocking ring-buffer + batched OTLP/JSON exporter — plus a deterministic matched-baseline benchmark (`cmd/agenttrace-bench`).

### License, Dependency & Architecture Audit
- **Clean-room implementation**: All code in [`apps/agenttrace/`](../../../apps/agenttrace/) and [`cmd/agenttrace-bench/`](../../../cmd/agenttrace-bench/) is written from scratch under **Apache-2.0**. Zero third-party source code was copied or vendored.
- **Specification licenses**: OpenTelemetry Semantic Conventions & OTLP Protobuf specifications are **Apache-2.0**; W3C Trace Context is **W3C Software and Document Notice and License**.
- **Zero new dependencies**: `go.mod` and `go.sum` are completely unchanged. `apps/agenttrace` imports ONLY the Go standard library, `github.com/zap-proto/zip`, and `github.com/luxfi/log` (zero host imports, verified by `apps.TestAppsImportNothingOfTheirHost`).
- **Canonical architecture preserved**: Improves observability across `vfs -> Hanzo S3 -> Visor`, Cloud lifecycle & billing, Enso routing, Zen generation, and Kai orchestration without creating parallel subsystems.

---

## 2. Reproducible Baseline & Hanzo Hardware Methodology

### Reproduction Command
```bash
CGO_ENABLED=0 go run ./cmd/agenttrace-bench \
  --seed 1348 \
  --tasks 120 \
  --repo-root . \
  --out-json docs/research/hip-1348-otel-genai-tracing/results.json
```

### Hardware, Runtime & Corpus
- **Host environment**: `linux/amd64`, `2` vCPUs, `go1.27.1`, `runtime.LockOSThread()` pinned execution, deterministic PRNG seed `1348`.
- **Versioned `hanzoai/cloud` corpus**: `15` real tracked repository files (`161,423` bytes total; corpus SHA-256 `sha256:c9f13a2400004811d4281c2e2436193b1c887e831afc5938f32c7b7fa3e638ab`), including `go.mod`, `LLM.md`, `Makefile`, `config.go`, `serve.go`, `middleware_tracing.go`, `telemetry.go`, `apps/apps.go`, `apps/imports_test.go`, and `apps/agenttrace/*.go`.
- **Workload**: `120` deterministic multi-hop agent tasks per matched regime (`7` spans/task = `1` root `agent.task` span + `6` canonical subsystem spans = `840` spans/regime), each executing an identical CPU work unit over the real versioned corpus bytes (~`167.63 µs` median baseline task time) and injecting deterministic canary API keys (`sk-canary-...`) and PII emails (`engineer-*@hanzo.test`) to validate DLP hygiene end-to-end.
- **Three Matched Regimes Evaluated Identically**:
  1. `untraced_baseline`: Identical 6-subsystem work unit with zero tracing (`0%` completeness).
  2. `naive_sync_json_tracer`: Per-span global `sync.Mutex` + `map[string]any` + `encoding/json.Marshal` + synchronous unbuffered file write & `fsync` (`7` spans/task, no W3C DAG, no OTLP/JSON, no DLP redaction).
  3. `hanzo_agenttrace_batched`: Production `apps/agenttrace.Tracer` with W3C `traceparent`/`tracestate` propagation across hops, typed zero-interface `Attr` slices, default `CaptureRedact` DLP gate, `8,192`-slot non-blocking `RingBuffer`, `256`-span batched OTLP/JSON export with durable `fsync` (`WriterSink`), and live tenant-isolated `TraceStore` + `VerifyTaskCompleteness`.

---

## 3. Falsifiable Hypothesis, Metrics & Expected Failure Modes

### Hypothesis
> **H1**: A unified OpenTelemetry GenAI tracing plane (`apps/agenttrace`) using zero-interface typed attributes, W3C `traceparent`/`tracestate` propagation, default DLP redaction, a bounded non-blocking ring buffer, and batched OTLP/JSON export achieves:
> 1. **`100.00%` completeness** across all 6 Hanzo subsystems under one `task_id` (`0` orphan spans, `0` cycles, `100.00%` exact 1:1 micro-USD billing reconciliation, `100.00%` OTLP/JSON schema validity),
> 2. **`0` cross-tenant leaks** and **`0` secret/PII leaks** under active adversarial probing and canary credential injection, and
> 3. **Order-of-magnitude lower instrumentation overhead** (`>100×` faster p50 hot-path overhead and `>3×` fewer allocations per span) compared to a naive synchronous JSON-per-span tracer, adding `< 25 µs/task` (`< 3 µs/span`) on the median hot path.

### Falsification Criteria
H1 is falsified if:
1. Any task in `hanzo_agenttrace_batched` fails `VerifyTaskCompleteness` (`<100%` 6/6 coverage, `>0` orphan spans, `>0` billing delta micro-USD, or `<100%` OTLP/JSON batch validity).
2. Any unauthorized org query returns spans (`>0` cross-tenant leaks) or any canary API key/email appears unredacted in emitted spans (`>0` secret leaks).
3. `hanzo_agenttrace_batched` fails to beat `naive_sync_json_tracer` by at least `2×` on p50 instrumentation overhead and allocations/span.
4. Any of the three negative regimes fails to reproduce its predicted failure mode.

### Expected Failure Modes (Explicit Negative Regimes)
1. **Negative Regime A — Unredacted 64 KiB Raw Prompt/Completion Capture (`CaptureRaw`)**: Retaining raw `64 KiB` prompts + `64 KiB` completions (`130,889` bytes/task) explodes OTLP wire volume by **`19.41×`** (`144,653.6 B/task` vs `7,452.8 B/task`), heap allocation by **`10.32×`** (`59,287.9 B/span` vs `5,747.0 B/span`), and task tracing latency by **`32.99×`** (`2,234.05 µs/task` vs `67.73 µs/task`), while leaking **`60/60`** embedded canary credentials (`0` leaks under `CaptureRedact` and `CaptureHash`).
2. **Negative Regime B — Burst Overload Beyond Ring Buffer Capacity**: Driving `4,096` spans into a `256`-slot ring backed by a lagging `2 ms/batch` collector forces a hard tradeoff: non-blocking `BackpressureDropNewest` preserves hot-path p99 (`6.17 µs`, `3.664 ms` total) at the cost of **`90.63%` explicit drops** (`3,713` dropped spans surfaced via `DroppedSpans`), whereas `BackpressureBlock` achieves `0` drops but degrades hot-path p99 by **`386.48×`** (`2,384.17 µs`, `210.417 ms` total burst).
3. **Negative Regime C — Micro-Span VFS Over-Instrumentation (4 KiB Un-coalesced Reads)**: Tracing a `4 MiB` VFS artifact read as `1,024` individual `4 KiB` spans instead of `1` coalesced span (`hanzo.vfs.coalesced_ops=1024`) causes a **`512.50×`** total span cardinality explosion (`1,025` vs `2` spans/task), **`584.05×`** OTLP byte explosion (`998,593.4 B/task` vs `1,709.8 B/task`), **`326.46×`** heap explosion (`1,450,862.4 B/task` vs `4,444.3 B/task`), and **`275.34×`** latency slowdown (`2,698.50 µs/task` vs `9.80 µs/task`).

---

## 4. Bounded Implementation (`apps/agenttrace`, `apps/apps.go`, `cmd/agenttrace-bench`)

The clean-room package lives in [`apps/agenttrace/`](../../../apps/agenttrace/) and mounts declaratively in [`apps/apps.go`](../../../apps/apps.go) (`Wire()`) with **zero host imports**:

- [`apps/agenttrace/semconv.go`](../../../apps/agenttrace/semconv.go): OpenTelemetry GenAI Semantic Conventions (`gen_ai.*`) and HIP-1348 subsystem attribute constants plus typed span input structs for all 6 Hanzo subsystems (`ModelSpanInput`, `RetrievalSpanInput`, `MCPSpanInput`, `VFSSpanInput`, `SandboxSpanInput`, `BillingSpanInput`).
- [`apps/agenttrace/context.go`](../../../apps/agenttrace/context.go): Strict W3C Trace Context Level 1 (`ParseTraceparent`, `ParseTracestate`, `InjectHeaders`, `ExtractHeaders`, all-zero/version/CRLF rejection), Go `context.Context` propagation (`WithTaskContext`, `TaskContextFrom`), and pooled `ChaCha8` / deterministic `splitMix64` ID generation.
- [`apps/agenttrace/dlp.go`](../../../apps/agenttrace/dlp.go): Default-on DLP gate (`CaptureRedact`, `CaptureHash`, opt-in `CaptureRaw`), unconditional credential-key redaction (`authorization`, `api_key`, `password`, `secret`, `cookie`, etc.), and fast SIMD-prefiltered regex scrubbing for API keys (`sk-`, `hz_`, `ghp_`, `AKIA`, `AIza`, `xox*`), Bearer tokens, JWTs, PEM private keys, emails, and SSNs.
- [`apps/agenttrace/span.go`](../../../apps/agenttrace/span.go) & [`apps/agenttrace/otlp.go`](../../../apps/agenttrace/otlp.go): Zero-interface typed `Attr` slices (`AttrString`/`AttrInt64`/`AttrFloat64`/`AttrBool`) and pooled streaming RFC 8259 OTLP/JSON `ExportTraceServiceRequest` encoder/decoder (`EncodeOTLPJSON`, `DecodeOTLPJSON`).
- [`apps/agenttrace/ring.go`](../../../apps/agenttrace/ring.go) & [`apps/agenttrace/exporter.go`](../../../apps/agenttrace/exporter.go): Bounded circular `RingBuffer` (`BackpressureDropNewest` default + `BackpressureBlock` comparison mode) with atomic `DroppedSpans` / `HighWaterMark` counters, plus async `BatchExporter`, `WriterSink`, `MultiSink`, and `SlowSink`.
- [`apps/agenttrace/store.go`](../../../apps/agenttrace/store.go) & [`apps/agenttrace/verify.go`](../../../apps/agenttrace/verify.go): Bounded per-org multi-tenant `TraceStore` (fail-closed `ErrCrossTenantDenied` / `ErrCrossTenantContam`), exact integer micro-USD `RateCard` & `ComputeExpectedBilling`, and `VerifyTaskCompleteness` auditing 6/6 subsystem coverage, DAG connectivity (0 orphans, 0 cycles), 1:1 billing reconciliation, tenant consistency, and DLP secret hygiene.
- [`apps/agenttrace/tracer.go`](../../../apps/agenttrace/tracer.go): High-level `Tracer` and live `TaskMeter` ensuring emitted billing spans reconcile 1:1 with subsystem spans under one `task_id`.
- [`apps/agenttrace/http.go`](../../../apps/agenttrace/http.go) & [`apps/agenttrace/zipdoc_gen.go`](../../../apps/agenttrace/zipdoc_gen.go): 7 typed `zip.App` routes under `/v1/agenttrace` (`GET /health`, `POST /spans`, `GET /tasks`, `GET /tasks/:taskId`, `GET /tasks/:taskId/verify`, `GET /tasks/:taskId/otlp`, `GET /diagnostics`), enforcing server-stamped IAM org isolation (`principal.Acting`), `url:"-"` query-hijack protection on every POST field, and fail-closed platform SuperAdmin authorization on `/diagnostics`.
- [`cmd/agenttrace-bench/main.go`](../../../cmd/agenttrace-bench/main.go) & [`cmd/agenttrace-bench/main_test.go`](../../../cmd/agenttrace-bench/main_test.go): Reproducible benchmark runner and automated hypothesis verification test.

---

## 5. Correctness, Concurrency, Security & Regression Evidence

All verification commands pass cleanly with zero warnings and zero data races:
- `CGO_ENABLED=0 go test -count=1 ./apps`: **PASS** (`TestAppsImportNothingOfTheirHost` + `TestFanClubCloudNativeE2E` — confirms zero host imports and clean `apps.Wire()` integration).
- `go vet ./...`: **PASS** (`0` warnings).
- `go test -race -count=1 -v ./apps/agenttrace/... ./cmd/agenttrace-bench/...`: **PASS** (`10` top-level tests / `14` total test cases including subtests, `0` data races):
  - `TestW3CTraceparentAndTracestateRoundTrip`: W3C `traceparent`/`tracestate` wire round-trip and rejection of all-zero IDs, unsupported versions, uppercase/non-hex, CRLF injection, and conflicting `X-Hanzo-Task-Id`.
  - `TestUnifiedSixSubsystemTaskCompleteness`: Full 6-subsystem execution across simulated HTTP header boundaries verifying `6/6` coverage, `0` orphans, `1:1` billing (`delta = 0`), `0` secret leaks, and OTLP/JSON export.
  - `TestCompletenessDetectsOrphansCyclesMissingAndBillingMismatch` (`5` subtests): Verifies detection of orphan spans, DAG cycles, missing subsystems, `+1` micro-USD billing drift, and cross-tenant span contamination.
  - `TestDLPRedactionAndContentCaptureModes`: Verifies `CaptureRedact`, `CaptureHash`, `CaptureRaw`, credential-key redaction, error-message scrubbing, and 7 secret/PII patterns.
  - `TestMultiTenantIsolationFailClosed`: Verifies cross-org read denial, sub-org tenant denial, empty-org rejection, and cross-org span injection rejection.
  - `TestBoundedRingBufferNonBlockingDropAndBlockModes`: Verifies non-blocking drop accounting (`enqueued + dropped == emitted`) and blocking zero-drop drain.
  - `TestConcurrentWritersRaceClean`: `12` concurrent goroutines × `15` multi-subsystem tasks (`1,260` spans) under `-race` with `0` drops, `100%` completeness, and `0` cross-tenant leaks.
  - `TestOTLPJSONSchemaValidation`: Strict `DecodeOTLPJSON` round-trip validation including RFC 8259 control/quote/HTML escaping.
  - `TestHTTPMountIsolationAndOperatorGuard`: End-to-end `zip.App` HTTP surface testing unauthenticated `/health`, server-stamped org isolation (`200` own org / `403` foreign org), query-hijack immunity (`?flush=false`), DLP redaction over HTTP ingest, and SuperAdmin-only `/diagnostics` (`403` non-admin / `200` admin).
  - `TestBenchmarkEndToEndAndHypothesisGate`: Automated CI verification of all 3 matched regimes and all 3 negative regimes.

---

## 6. Before/After Measurements, Raw Results & HIP-1348 Integration Recommendation

### 6.1 Matched-Baseline Results (`seed=1348`, `120` tasks/regime, `7` spans/task, `840` spans/regime)

| Regime | p50 (µs/task) | p95 (µs/task) | p99 (µs/task) | p50 Overhead (µs/task) | Overhead (ns/span) | Wall-Time Delta (p50 / p95 / p99) | Allocs / Span | Heap B / Span | Total Wall (ms) | 6/6 Complete | Orphan Spans | Billing Reconciled | Cross-Tenant Leaks | Secret Leaks | Valid OTLP Batches |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| `untraced_baseline` | 167.63 | 216.18 | 231.15 | 0.00 | 0.0 | 0.00% / 0.00% / 0.00% | 0.00 | 0.0 | 18.350 | 0.00% | 0 | 0.00% | 0 | 0 | 0.00% |
| `naive_sync_json_tracer` | 27,663.71 | 36,002.79 | 55,321.55 | +27,496.08 | 3,928,011.0 | +16,402.84% / +16,554.08% / +23,833.18% | 27.73 | 1,224.7 | 3,482.155 | 0.00% | 840 | 0.00% | 0 | 240 | 0.00% |
| `hanzo_agenttrace_batched` | **187.48** | **408.12** | **1,696.45** | **+19.85** | **2,835.1** | +11.84% / +88.79% / +633.92% | **8.45** | 3,832.8 | **42.969** | **100.00%** | **0** | **100.00%** | **0** | **0** | **100.00% (4/4)** |

> **Contextualizing wall-time overhead**: On this synthetic `167.63 µs` pure-CPU work loop (and `2` vCPUs where background batch export shares cores with the producer), `hanzo_agenttrace_batched` adds `+19.85 µs/task` at p50 (`2,835.1 ns/span`; isolated tracer-only pass `8,111.4 ns/span` including async OTLP/JSON encoding). Against a real multi-hop agent task lasting `100 ms`–`2,000 ms`, `+19.85 µs/task` represents **`0.020%`–`0.001%`** of wall-clock execution time, while beating `naive_sync_json_tracer` by **`1,385.19×`** at p50 (`37.60×` at p99), **`81.04×`** on total durable wall time, and **`3.28×`** on allocations per span.

### 6.2 Negative Regime A — 64 KiB Raw Prompt/Completion Capture (`60` tasks, `130,889` B raw payload/task)

| Capture Mode | Mean Task (µs) | Latency vs Redact | Heap B / Span | OTLP Wire B / Task | Wire Bloat vs Redact | Secret Leaks |
|---|---:|---:|---:|---:|---:|---:|
| `CaptureRedact` (default) | **67.73** | 1.00× | **5,746.95** | **7,452.77** | 1.00× | **0** |
| `CaptureHash` (`sha256` + len) | 586.95 | 8.67× | 24,524.13 | 7,605.20 | 1.02× | **0** |
| `CaptureRaw` (64 KiB + 64 KiB) | **2,234.05** | **32.99×** | **59,287.94** | **144,653.60** | **19.41×** | **60** |

### 6.3 Negative Regime B — Burst Overload Beyond Ring Capacity (`4,096` spans -> `256`-slot ring, `2 ms/batch` sink lag)

| Backpressure Policy | Producer p50 (µs) | Producer p99 (µs) | Total Emit Wall (ms) | Dropped Spans | Drop Rate |
|---|---:|---:|---:|---:|---:|
| `BackpressureDropNewest` (default) | 0.45 | **6.17** | **3.664** | 3,713 | **90.63%** |
| `BackpressureBlock` | 0.50 | **2,384.17** (`386.48×` worse) | **210.417** (`57.43×` slower) | 0 | 0.00% |

### 6.4 Negative Regime C — VFS 4 MiB Micro-Span Over-Instrumentation (`30` tasks)

| VFS Instrumentation Granularity | Spans / Task | Mean Task (µs) | OTLP Wire B / Task | Heap B / Task |
|---|---:|---:|---:|---:|
| Coalesced VFS span (`coalesced_ops=1024`) | **2** | **9.80** | **1,709.77** | **4,444.27** |
| Un-coalesced 4 KiB micro-spans | **1,025** (`512.50×`) | **2,698.50** (`275.34×`) | **998,593.43** (`584.05×`) | **1,450,862.40** (`326.46×`) |

### 6.5 Limitations & HIP-1348 Integration Recommendation
- **Limitations**:
  1. Benchmark task timings run on a 2-vCPU VM where the background `BatchExporter` goroutine contends for CPU with the producer loop, inflating p99 relative to dedicated multi-core production nodes.
  2. The embedded `TraceStore` is an in-memory per-org bounded FIFO cache (`MaxTasksPerOrg=1024`) suitable for local dev, hot-window verification, and unit/integration testing; production long-term retention should attach `hanzoai/datastore` / ZAP-OTLP sink via `MultiSink`.
  3. Built-in DLP regex patterns cover standard API keys, JWTs, Bearer tokens, PEM keys, emails, and SSNs; domain-specific secrets should use `CaptureRedact` (default) or `CaptureHash` rather than `CaptureRaw`.
- **HIP-1348 Recommendation (Accept for Core)**:
  1. **Adopt `apps/agenttrace`** as the canonical HIP-1348 correlation contract and `/v1/agenttrace` verification/export surface in `hanzoai/cloud`, wiring `InjectHeaders`/`ExtractHeaders` into Enso, Index/Graph, MCP, VFS/S3, Visor, and Cloud billing hops.
  2. **Enforce three production guardrails in HIP-1348**: (a) keep `CaptureRedact` (or `CaptureHash`) mandatory by default — never enable `CaptureRaw` globally; (b) keep `BackpressureDropNewest` on the agent execution hot path and alert on `exporter.ring.droppedSpans > 0`; and (c) require VFS/S3 instrumentation to coalesce block I/O into operation/snapshot spans (`hanzo.vfs.coalesced_ops`) rather than emitting per-4 KiB micro-spans.
