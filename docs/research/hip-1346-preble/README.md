# HIP-1346 Research Dossier: Prompt-Locality-Aware GPU Placement (Preble)

- **Tracking Issue**: [`hanzoai/cloud#28`](https://github.com/hanzoai/cloud/issues/28) (`p2, bounty, perf, research, hip-1346`)
- **Canonical Owning Repository**: `hanzoai/cloud` (`apps/preble`, `cmd/preble-bench`, `apps/apps.go`)
- **Disposition Target**: `MERGE` (bounded clean-room Apache-2.0 implementation, zero new external dependencies, full race/security/regression verification, raw benchmark receipts in [`results.json`](./results.json))

---

## 1. Primary Reference, Architecture Summary & License Audit

### Primary Paper Reference
- **Preble: Efficient Distributed Prompt Scheduling for LLM Serving** (Vikranth Srivatsa, Ziyu He, Reyna Abhyankar, Dongming Li, Yiying Zhang — **ICLR 2025**)
  - Primary link: <https://proceedings.iclr.cc/paper_files/paper/2025/hash/5bc342f48de8264779952fac378f96dc-Abstract-Conference.html>
  - **Core algorithmic insight**: Standard `least_loaded` LLM routers ignore KV-cache prefix locality, causing repeated recomputation of shared system prompts, tool schemas, and multi-turn agent context across GPU replicas. Conversely, naive `prefix_greedy` routing collapses tail latency under Zipfian hot-prefix skew by piling all requests onto a single GPU replica while sibling GPUs sit idle. Preble's **E2 (Exploitation vs. Exploration)** scheduler maintains a distributed prefix tree tracking cached KV blocks per GPU replica and balances:
    1. **Exploitation**: routing to a warm GPU replica holding a long reusable prefix to slash prefill tokens and TTFT.
    2. **Exploration & Dynamic Hot-Prefix Replication**: detecting concurrent hot-prefix skew when a warm replica's queue wait outstrips amortized replication cost and selectively replicating the hot prefix onto an underloaded replica with free KV headroom.
    3. **Eviction & Cold-Start Awareness**: modeling per-replica KV capacity, leaf-first RadixAttention LRU eviction, work-conservation under global saturation, and scale-out warmup penalties.

### Clean-Room License & Dependency Audit

| Reference / Dependency | Upstream License | Imported Code? | Audit Result |
|---|---|---|---|
| Preble (ICLR 2025) Paper | Academic publication (method only) | None — 100% clean-room Go implementation | **Clear** |
| RadixAttention Leaf-First LRU Concept | Academic publication (method only) | None — 100% clean-room Go `container/list` | **Clear** |
| `apps/preble` & `cmd/preble-bench` | Apache-2.0 | Go stdlib + existing `github.com/zap-proto/zip` & `github.com/luxfi/log` | **Zero new `go.mod` / `go.sum` entries** |

No upstream Preble, SGLang, or vLLM Python/C++ code was copied or vendored. `go.mod` and `go.sum` remain 100% untouched. Published paper numbers are **not** claimed; every measurement in Section 6 was reproduced locally by [`cmd/preble-bench`](../../../cmd/preble-bench/main.go).

---

## 2. Reproducible Baseline & Real-Repo Evaluation Corpus

To comply with `PR_CONVERGENCE.md` (public reproducibility on real data rather than synthetic-only claims), [`cmd/preble-bench`](../../../cmd/preble-bench/main.go) constructs all prompt prefixes directly from **37 real repository files** in `hanzoai/cloud` (`492,489` bytes, `214,557` lossless whitespace-preserving tokens, corpus SHA-256 `dd21b666f2a24042df5c4c12196102312da9687e7466d6ba1864befbf7d74433`, anchored to git commit `5c4e0d0eabaff45243ea223d8eaa536b199a3ce0`) combined with authentic Hanzo MCP tool-calling schemas.

All four policies execute in the **exact same discrete-event + wall-clock harness** (`preble.RunSimulation`) invoking the exact production `Scheduler.PlaceAt` and `Scheduler.CompleteAt` methods over identical arrival timestamps, token block sequences, aggregate-budget-shared decode lengths, and scale-out events:
1. `least_loaded` — shipping prefix-agnostic least-active-load GPU router baseline.
2. `round_robin` — naive stateless cyclic baseline.
3. `prefix_greedy` — pure max-prefix locality without load balancing (demonstrates hot-prefix skew collapse).
4. `preble_e2` — Preble E2 (prefix locality + load/queue model + selective hot-prefix replication + leaf-first LRU eviction awareness + cold-start awareness + work-conservation guard).

### Exact Reproduction Command
```bash
go run ./cmd/preble-bench \
  -repo . \
  -seed 1346 \
  -block-size 16 \
  -out docs/research/hip-1346-preble/results.json
```

- **Host environment**: `linux/amd64`, 2 vCPUs, `go1.27.1`, `runtime.LockOSThread()` pinned wall-clock decision timing, deterministic seed `1346`, `BlockSize = 16` tokens/block.
- **GPU model parameters**: H100-80GB class (`PrefillTokPerSec = 40,000`, aggregate `DecodeTokPerSec = 1,800` shared across simultaneous decode streams with per-stream cap `80 tok/s`, continuous-batching decode contention `alpha = 0.06`, per-replica KV budgets `40,960–131,072` tokens depending on trace).

---

## 3. Falsifiable Hypothesis, Metrics & Expected Failure Modes

### Hypothesis
> **H1**: Replacing `least_loaded` GPU routing with clean-room `preble_e2` over Hanzo agent/LLM workloads (a) reduces **p99 TTFT** by **>5×** on multi-turn shared-context agent coding and oversubscribed eviction/scale-out traces, (b) avoids `prefix_greedy`'s hot-prefix skew collapse under Zipfian 75/25 traffic while outperforming **both** `least_loaded` (>1.5× p99 TTFT) and `prefix_greedy` (>50× p99 TTFT), (c) preserves **0.00%** cross-tenant and cross-ACL KV prefix leakage with strictly bounded radix tree memory (`PeakIndexNodes <= AggregateMaxBlocks`), and (d) matches `least_loaded` p99 TTFT within **1.00×** (zero serving regression) in the explicit **0%-reuse negative regime**.

### Falsification Criteria
H1 is falsified if:
1. `preble_e2` fails to beat both `least_loaded` and `prefix_greedy` on p99 TTFT under `skewed_hot_prefix`.
2. Any cross-tenant or cross-ACL probe leaks a KV prefix hit (`>0` leaks out of 200 adversarial probes).
3. Radix tree memory exceeds `AggregateMaxBlocks` on any trace under heavy eviction or unique-prompt flooding.
4. `preble_e2` degrades p99 TTFT or GPU utilization relative to `least_loaded` on 100%-unique zero-reuse prompts.

### Expected Failure Modes (Negative Regimes)
1. **Hot-prefix skew collapse under `prefix_greedy`**: Pinning every request for a hot prefix to one GPU without load-aware replication causes catastrophic queueing (`>33 s` p99 TTFT) and collapses cluster GPU utilization to `~24%`.
2. **Zero-reuse one-shot prompts (`unique_zero_reuse_prompts`)**: When prompts share 0% prefix overlap, maintaining a cryptographically domain-separated SHA-256 prefix index adds pure CPU routing latency (`~523 µs` p50 vs `1.51 µs` for `least_loaded`) with 0% prefill savings. Although `0.52 ms` is negligible relative to `~115–145 ms` GPU prefill TTFT (`<0.4%`), operators serving strictly one-shot non-shared batch workloads can toggle `policy=least_loaded` or bypass prefix hashing when prompt length is tiny.

---

## 4. Bounded Implementation (`apps/preble`, `apps/apps.go`, `cmd/preble-bench`)

The clean-room implementation lives in [`apps/preble/`](../../../apps/preble/) and mounts declaratively in [`apps/apps.go`](../../../apps/apps.go) (`Wire()`) with **zero host imports** (verified by `TestAppsImportNothingOfTheirHost`):

- [`apps/preble/prefix.go`](../../../apps/preble/prefix.go): Tenant- and ACL-isolated radix/block prefix index (`namespaceKey{tenant, aclScope}` disjoint roots + SHA-256 domain-separated cumulative block hashing `"hanzo-preble-kv-block-v1"`), lossless whitespace-preserving raw prompt tokenization (`TokenizeText`), pin-protected per-replica LRU KV caches, **RadixAttention leaf-first LRU ordering** (guaranteeing replica-leaves evict strictly before parent blocks so `NodeCount <= AggregateMaxBlocks` holds unconditionally and namespace roots pruned during eviction are re-registered), and LRU tail reuse-value eviction estimation.
- [`apps/preble/scheduler.go`](../../../apps/preble/scheduler.go): Concurrency-safe `Scheduler` implementing `preble_e2`, `least_loaded`, `round_robin`, and `prefix_greedy` with `(tenant, requestId)`-namespaced active request tracking, continuous-batching queue modeling, physics-grounded eviction steering, scale-out cold-start awareness, concurrent hot-prefix auto-replication, work-conservation guard under cluster saturation, and explicit overflow/bounds validation.
- [`apps/preble/sim.go`](../../../apps/preble/sim.go): Deterministic discrete-event heap simulator (`RunSimulation`) with processor-sharing per-replica aggregate `DecodeTokPerSec` enforcement and dynamic completion rescheduling, exact union-of-busy-intervals GPU utilization, Jain's load-balance index, mid-trace cold-start scale-outs, and wall-clock decision latency capture.
- [`apps/preble/http.go`](../../../apps/preble/http.go) + [`apps/preble/zipdoc_gen.go`](../../../apps/preble/zipdoc_gen.go): 7 typed `zip.App` routes mounted under `/v1/preble` (`GET /health`, `GET /replicas`, `POST /replicas`, `DELETE /replicas/:id`, `POST /place`, `POST /complete`, `GET /metrics`) with zero `http.NewServeMux()`, server-stamped IAM org isolation via `principal.Acting`, fail-closed platform SuperAdmin operator authorization on shared-replica mutations (`POST /replicas`, `DELETE /replicas/:id`), `url:"-"` query-hijack protection on every POST body field, and full `zipdoc` OpenAPI/MCP descriptions.
- [`cmd/preble-bench/main.go`](../../../cmd/preble-bench/main.go) + [`cmd/preble-bench/main_test.go`](../../../cmd/preble-bench/main_test.go): Reproducible real-repo benchmark CLI and automated regression gate.

---

## 5. Correctness, Concurrency, Security & Regression Evidence

### Verification Commands Executed
- `gofmt -l apps/preble cmd/preble-bench apps/apps.go` -> clean (0 unformatted files).
- `go vet ./apps/preble/... ./cmd/preble-bench/... ./apps/...` -> pass (0 warnings).
- `go test -race -count=1 -v ./apps/preble/... ./cmd/preble-bench/...` -> **all 16 tests pass with 0 data races**:
  - `TestPrefixIndexCausalAndLRUBoundedMemory`: causal matching, 500-prompt flood confirming `NodeCount <= MaxBlocks` at every iteration.
  - `TestPrefixIndexPinnedCapacityBypass`: pin protection under 100% active KV saturation with zero memory bound breach.
  - `TestStrictCrossTenantAndACLIsolation`: 0 cross-tenant hits, 0 cross-ACL hits, `ErrRequestNotFound` on cross-tenant completion.
  - `TestPrebleE2HotPrefixReplicationBeatsPrefixGreedy`: proves auto-replication under skew beats both `prefix_greedy` and `least_loaded`.
  - `TestNegativeRegimeUniquePromptsNoDegradation`: proves 0% reuse matches `least_loaded` with zero TTFT regression.
  - `TestScaleOutColdStartAvoidance`: verifies cold-start avoidance during warmup and immediate absorption post-warmup.
  - `TestValidationAndOverflowBounds`: rejects negative/overflow KV budgets, `NaN`/`Inf` throughput, oversized prompts, invalid policies.
  - `TestSchedulerConcurrencyRaceFree`: 8 concurrent workers across 4 tenants under `-race`.
  - `TestEveryRouteIsDescribed`: verifies all 7 `/v1/preble` OpenAPI/MCP operations carry non-empty `zipdoc` descriptions.
  - `TestHTTPWireSecurityIsolationAndQueryHijackImmunity`: end-to-end `zip.App` wire tests verifying `401` unauthenticated refusals, `403` non-operator replica mutation refusals, `url:"-"` query-hijack immunity on `POST /replicas` and `POST /place`, `0` cross-org prefix hits, and `404` on cross-org `POST /complete`.
  - `TestReplicaMutationRequiresOperatorAuthorization`: verifies non-operator org B receives `403 Forbidden` on `POST`/`DELETE /v1/preble/replicas` and cannot disrupt org A's active placements.
  - `TestActiveRequestIDsNamespacedByTenant`: verifies concurrent orgs submitting identical `requestId` values succeed independently without cross-org collision or existence probing.
  - `TestReserveAndPinReRegistersPrunedNamespaceRoot`: verifies a full 2-block cache evicting a tenant's last block re-registers the namespace root so subsequent `Match` calls find the replacement block.
  - `TestSimulationEnforcesAggregateDecodeBudget`: verifies 64 simultaneous 64-token decode requests on a 1,800 tok/s replica respect the physical `>= 2,275.56 ms` aggregate decode floor while single streams stay capped at `80 tok/s`.
  - `TestTokenizeTextPreservesWhitespaceIdentity`: verifies Python indentation and horizontal/newline whitespace variants produce distinct token streams and zero fabricated KV reuse.
  - `TestRunBenchmarksReproducibleAndIsolated`: end-to-end reproducible benchmark regression gate over all 4 real-repo traces + 300 isolation/control probes.
- `CGO_ENABLED=0 go test ./apps/...` -> pass (including `TestAppsImportNothingOfTheirHost`).
- `CGO_ENABLED=0 go build ./cmd/cloud` -> pass.

### Security Review Posture (`PR_CONVERGENCE.md`)
- **Zero Cross-Tenant KV Oracle**: Every `(tenant, aclScope)` pair has a disjoint radix tree root AND SHA-256 domain-separated cumulative block digests (`"hanzo-preble-kv-block-v1" || len(tenant) || tenant || len(acl) || acl || parentHash || len(chunk) || tokens`). Cross-tenant probes verified **0 / 100 cross-tenant leaks (`0.00%`)** and **0 / 100 cross-ACL leaks (`0.00%`)** alongside **100 / 100 same-scope positive hits**.
- **Server-Stamped Identity, Operator-Gated Cluster Mutations & Query-Hijack Defense**: `/v1/preble` derives tenant exclusively from `principal.Acting(ctx)` (never from JSON body or query string); active request IDs are strictly namespaced by `(tenant, requestId)` so tenants cannot squat on or probe another org's IDs; shared GPU replica mutations (`POST /v1/preble/replicas`, `DELETE /v1/preble/replicas/:id`) fail closed unless the caller carries validated platform SuperAdmin authority (`X-User-IsAdmin: true` minted exclusively by `SanitizeIdentity`); all POST struct fields carry `url:"-"`; cross-org `POST /v1/preble/complete` answers `404`. `GET /v1/preble/health` and `GET /v1/preble/replicas` expose only aggregate numeric counters — zero tenant names or prefix keys.
- **Bounded Memory & Overflow Safety**: Leaf-first RadixAttention LRU guarantees `PeakIndexNodes <= AggregateMaxBlocks` across every trace. Hard caps enforce `MaxPromptTokens (1 Mi)`, `MaxKVBudgetTokens (1 Gi)`, `MaxReplicas (256)`, `MaxTenants (4096)`, `MaxInFlightRequests (16384)`, `maxPromptTextBytes (2 MiB)`, and `maxWarmupMs (600,000 ms)`.

---

## 6. Before/After Measurements, Negative Regime & HIP-1346 Recommendation

All numbers below are taken verbatim from [`docs/research/hip-1346-preble/results.json`](./results.json) (`seed=1346`, `go1.27.1`, `linux/amd64`, 37 real repo files = `214,557` tokens).

### 6.1 Trace 1: `multi_turn_agent_coding` (8 GPUs × 128K KV, 32 Sessions × 12 Turns = 384 Reqs)

| Policy | p50 TTFT (ms) | p95 TTFT (ms) | p99 TTFT (ms) | Cache Reuse (%) | GPU Util (%) | Evicted Blocks | Hot Repl | Dec p50 (µs) |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| `least_loaded` (baseline) | 1,345.24 | 3,703.47 | **4,170.00** | 36.10% | 95.98% | 43,448 | 0 | 1.65 |
| `round_robin` | 1,295.22 | 4,133.44 | 4,843.96 | 37.66% | 84.41% | 37,920 | 0 | 0.73 |
| `prefix_greedy` | 16.64 | 147.20 | 193.54 | 89.24% | 93.12% | 0 | 0 | 759.68 |
| **`preble_e2`** | **16.64** | **147.20** | **193.54** | **89.24%** | **93.12%** | **0** | **0** | **835.32** |

- **Result**: **`21.55×` lower p99 TTFT** vs `least_loaded` (`193.54 ms` vs `4,170.00 ms`), **+53.14 pp** higher cache reuse (`89.24%` vs `36.10%`), and **0 KV evictions** (vs `43,448` evicted blocks).

### 6.2 Trace 2: `skewed_hot_prefix` (Zipfian 75/25 Stress Test, 8 GPUs × 40K KV, 480 Reqs at ~100 req/s)

| Policy | p50 TTFT (ms) | p95 TTFT (ms) | p99 TTFT (ms) | Cache Reuse (%) | GPU Util (%) | Jain Balance | Evicted Blocks | Hot Repl | Dec p50 (µs) |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| `least_loaded` (baseline) | 91.85 | 394.51 | **486.93** | 73.51% | 94.52% | 0.9967 | 28,420 | 0 | 1.53 |
| `round_robin` | 277.80 | 1,326.08 | 1,632.56 | 74.07% | 92.42% | 1.0000 | 23,096 | 0 | 0.59 |
| `prefix_greedy` (skew collapse) | 4,596.75 | 29,729.08 | **33,054.24** | 89.42% | **24.27%** | **0.2066** | 1,572 | 0 | 646.00 |
| **`preble_e2`** | **17.66** | **184.67** | **297.34** | **87.70%** | **96.75%** | **0.9671** | **3,764** | **8** | **761.80** |

- **Result**: `prefix_greedy` collapses (`33.05 s` p99 TTFT, `24.27%` GPU utilization, Jain `0.2066`). `preble_e2` auto-replicates the hot prefix selectively (`8` replication events), achieving **`111.17×` lower p99 TTFT than `prefix_greedy`**, **`1.64×` lower p99 TTFT (and `5.20×` lower p50 TTFT) than `least_loaded`**, **+14.19 pp** higher cache reuse (`87.70%` vs `73.51%`), **`7.55×` fewer KV evictions** (`3,764` vs `28,420`), and the highest cluster GPU utilization (**`96.75%`**).

### 6.3 Trace 3: `cold_start_and_eviction` (Working Set 1.75× Initial Aggregate KV, 4→6 GPUs + 250 ms Cold Starts, 360 Reqs)

| Policy | p50 TTFT (ms) | p95 TTFT (ms) | p99 TTFT (ms) | Cache Reuse (%) | GPU Util (%) | Evicted Blocks | Hot Repl | Dec p50 (µs) |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| `least_loaded` (baseline) | 5,261.61 | 11,941.19 | **12,846.35** | 23.80% | 98.24% | 16,520 | 0 | 1.21 |
| `round_robin` | 4,087.55 | 12,595.14 | 15,651.62 | 31.39% | 74.74% | 17,384 | 0 | 0.71 |
| `prefix_greedy` | 519.61 | 1,720.88 | 1,937.19 | 71.17% | 95.70% | 17,456 | 0 | 820.91 |
| **`preble_e2`** | **1,245.55** | **1,773.82** | **1,935.14** | **62.96%** | **96.56%** | **17,096** | **29** | **867.29** |

- **Result**: **`6.64×` lower p99 TTFT** vs `least_loaded` (`1,935.14 ms` vs `12,846.35 ms`), **`8.09×` lower p99 TTFT** vs `round_robin`, and edges out `prefix_greedy` on p99 TTFT while maintaining higher GPU utilization (`96.56%` vs `95.70%`) and strict memory bounds (`PeakIndexNodes = 14,848 <= 18,432`). Note: `preble_e2`'s higher p50 (`1,246 ms` vs `prefix_greedy`'s `520 ms`) honestly reflects the cost of proactively draining queued prefixes onto freshly scaled-out cold GPUs (`gpu-4`, `gpu-5`) to flatten the p99 tail.

### 6.4 Trace 4: `unique_zero_reuse_prompts` — Explicit Negative Regime (8 GPUs × 64K KV, 320 100%-Unique Prompts)

| Policy | p50 TTFT (ms) | p95 TTFT (ms) | p99 TTFT (ms) | Cache Reuse (%) | GPU Util (%) | Evicted Blocks | Peak Nodes / Max | Dec p50 (µs) |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| `least_loaded` (baseline) | 113.15 | 145.41 | **145.41** | 0.00% | 97.69% | 33,792 | 32,768 / 32,768 | **1.51** |
| `round_robin` | 125.57 | 145.41 | 145.41 | 0.00% | 97.76% | 33,792 | 32,768 / 32,768 | 0.59 |
| `prefix_greedy` | 115.07 | 145.41 | 145.41 | 0.00% | 97.70% | 33,792 | 32,768 / 32,768 | 452.62 |
| **`preble_e2`** | **115.07** | **145.41** | **145.41** | **0.00%** | **97.61%** | **33,792** | **32,768 / 32,768** | **523.25** |

- **Negative Regime Conclusion**: When prefix reuse is `0.00%`, `preble_e2` **cleanly matches `least_loaded`** (`145.41 ms` vs `145.41 ms`, **`1.00×`**, `97.61%` vs `97.69%` GPU utilization) with zero memory blowup (`32,768 / 32,768` exact cap). The sole measurable cost is `~522 µs` (`0.52 ms`) of SHA-256 block hashing & radix-tree maintenance per 3.3K-token prompt (`0.36%` of `145 ms` TTFT).

### 6.5 Cross-Tenant & Cross-ACL Isolation Audit

| Probe Category | Probes Executed | Leaks Detected | Leak Rate | Result |
|---|---:|---:|---:|---|
| Cross-Tenant Identical Prompt (`org-A -> org-B`, same ACL name) | 100 | **0** | **0.00%** | **Pass** |
| Cross-ACL Identical Prompt (`prod-secret -> dev-public`, same org) | 100 | **0** | **0.00%** | **Pass** |
| Same-Tenant + Same-ACL Positive Control | 100 | 100 / 100 hits | — | **Pass** |

### 6.6 HIP-1346 Integration Recommendation, Rollout & Rollback Plan
- **Canonical Architecture Alignment**: Preserves `vfs -> Hanzo S3 -> Visor, Cloud owns lifecycle and billing, Enso routes models, Zen generates, Kai decides`. `apps/preble` mounts cleanly inside `hanzoai/cloud` at `/v1/preble` with zero duplicate inference gateways or auth planes: Enso model routers can consult `/v1/preble/place` (or embed `apps/preble.Scheduler` in-process with zero host dependencies) to select KV-warm Zen/vLLM/SGLang replicas while Cloud tracks lifecycle, capacity scale-out, and tenant isolation via Hanzo IAM (`principal.Acting`).
- **Next-Phase Production Hardening**: Stream live replica KV-cache eviction webhooks from vLLM/SGLang/Zen workers to reconcile exact engine-side KV state with `PrefixIndex`, and support optional pre-hashed block IDs from Enso's tokenizer fast-path to cut `~0.5 ms` SHA-256 hashing latency by >10× on multi-thousand-token prompts.
- **Rollout & Rollback**: Mounted declaratively in `apps.Wire()` with zero database migrations, zero changes to existing routes, and zero `go.mod` / `go.sum` edits. Rollback is a single-line removal of the `preble` entry in `apps.Wire()` with zero impact on any other subsystem.
