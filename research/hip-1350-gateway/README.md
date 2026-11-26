# Permissionless Enso Gateway & Stateless Scheduling Protocol — HIP-1350 Dossier (`hanzoai/cloud#15`)

**Issue**: [hanzoai/cloud#15](https://github.com/hanzoai/cloud/issues/15) (`p0`, `hip-1350`, `decentralized`, `network`)  
**Packages**: [`apps/gateway`](../../apps/gateway) (host-independent `zip` app & stateless scheduler) · [`cmd/gateway-bench`](../../cmd/gateway-bench) (5-dimension loopback benchmark CLI)  
**Artifacts**: [`score_table.md`](./score_table.md) · [`raw_results.json`](./raw_results.json)  
**License**: `Apache-2.0 OR MIT` (zero new external dependencies; `go.mod` and `go.sum` untouched)

---

## 1. Architecture & Published Open Protocol

`apps/gateway` implements a clean-room, host-independent Enso inference gateway and SkyPilot-inspired stateless multi-provider scheduling protocol in pure Go (`CGO_ENABLED=0` compatible, zero host imports, verified by `TestAppsImportNothingOfTheirHost`). Any independent operator can run a compatible gateway over the same open worker pool without Hanzo-operated infrastructure:

1. **Decentralized Worker Discovery (`apps/gateway/protocol.go`, `apps/gateway/worker.go`)**:
   - Workers self-sign EIP-712 `WorkerRecord` advertisements (binding `ProviderID`, `OperatorAddress`, `EndpointURL`, open-weight `ModelSpec` SHA-256 digests, pricing, TTFT, capacity, and expiry) and publish to any `DirectoryPeerServer`.
   - Independent directory peers synchronize via open HTTP gossip (`/directory/v1/gossip`, `/directory/v1/workers`) and export offline-verifiable `DirectorySnapshot`s — zero Hanzo registry lock-in.
2. **Cryptographically Signed Quotes & Execution Receipts (`apps/gateway/protocol.go`)**:
   - Every worker issues an EIP-712 / `secp256k1` signed `InferenceQuote` (EIP-2 low-S normalized, binding price, `MaxTTFTMs`, `ModelDigest`, `Expiry`, `RequestHash`, and unique `Nonce`).
   - On completion, the worker returns an EIP-712 signed `ExecutionReceipt` binding `RequestHash`, `OutputHash` (`sha256` of returned text), token counts, `ModelDigest`, latency, and `QuoteHash`, with optional independent `GatewaySignature` countersignature.
   - Both `VerifyQuote` and `VerifyReceipt` execute completely offline with strict EIP-2 low-S enforcement and per-gateway local replay nonce rejection (`NonceCache`).
3. **Stateless Multi-Provider Scheduler (`apps/gateway/scheduler.go`)**:
   - Supports four deterministic strategies (`lowest_cost`, `lowest_ttft`, `weighted_slo_score`, `failover_resilient`) with zero shared mutable session state required across gateways.
   - Automatically fails over across competing healthy peers on mid-flight provider crash (HTTP 500), timeout, invalid quote, or invalid/corrupted execution receipt.
4. **OpenAI-Compatible Typed Operation Surface (`apps/gateway/service.go`)**:
   - Registers OpenAI-compatible routes (`GET /v1/models`, `POST /v1/chat/completions`) and protocol endpoints (`/v1/gateway/health`, `/v1/gateway/providers`, `/v1/gateway/quotes`, `/v1/gateway/receipts/verify`, `/v1/gateway/export`) once through `github.com/zap-proto/zip` (`Mount`), projecting REST, OpenAPI (`/.well-known/openapi.json`), MCP (`/mcp`), CLI, and SDKs identically across embedded `zip.App` hosts and standalone `NewApp` / `NewHTTPHandler` / `StartLoopbackServer` deployments.

---

## 2. Mandatory Acceptance Verification (Dual-Gateway Parity & Hanzo Kill Switch)

- **Dual-Gateway OpenAI Parity (Loopback HTTP)**: `true` (`HanzoGateway` at `25 bps` and `CommunityGateway` at `10 bps` served identical OpenAI `/v1/chat/completions` payloads with offline-verified signed receipts).
- **Dual-Gateway OpenAI Parity (`zip.App`)**: `true` (two independent `zip.New` apps mounting `apps/gateway` served byte-identical completion content).
- **Hanzo-Disabled Kill-Switch Acceptance (`N = 60`)**: With EVERY Hanzo-operated gateway AND directory server disabled and shut down, client requests transparently routed through the independent community gateway with **100.0% completion (`60/60`)**, **`HanzoClientCalls = 0`**, **`HanzoServerCalls = 0`**, and **`CommunityCalls = 60`**.

---

## 3. Five-Dimension Measured Benchmarks

See [`score_table.md`](./score_table.md) for the compact summary table and [`raw_results.json`](./raw_results.json) for full unrounded arrays. Key highlights measured over live loopback TCP HTTP:

- **1. Cost (`$/1M useful tokens`)**: Measured cryptographic quote + receipt verification overhead is **501.273 µs/req** (**3.6858 vCPU-s / 1M tokens** = **$0.000041 / 1M tokens** at `$0.04/vCPU-hr`). Open gateway competition reduces total cost from **$0.280741/1M** (Hanzo `25 bps`) to **$0.280321/1M** (Community `10 bps`) down to **$0.280041/1M** (self-hosted/forked `0 bps`).
- **2. Performance**: Sequential (`N = 120`): **Client TTFT (first response byte) p50 3.696 ms (p99 11.538 ms)** (provider-internal receipt TTFT p50 0.000 ms), **E2E p50 4.000 ms / p95 8.000 ms / p99 11.921 ms**, **256.4 req/s** (`34869 tok/s`), **100.0% completion**. Concurrent (`16` clients): **378.3 req/s** (`51455 tok/s`), **p99 68.006 ms**. Mid-flight crash failover (`20` drills): **100.0% completion** with **p50 2.244 ms / p99 4.388 ms** recovery.
- **3. Trust**: **7 / 7 (100.0%)** adversarial quote/receipt/directory forgery and replay cases rejected fail-closed.
- **4. Decentralization**: **100.0%** of inference requests completed with **0 Hanzo-operated gateways**.
- **5. Sovereignty**: Stateless `GatewayExportBundle` (**3601 B**) verified offline in **511.4 µs**, tampered export rejected fail-closed (`true`), and a forked third-party gateway (`Operator C`, `0 bps` fee) booted in **790.8 µs** with all Hanzo services offline, serving **100.0%** verified completions (`0` Hanzo calls).

---

## 4. Adversarial Trust Matrix & Explicit Threat-Model Scope

> **Verification Class Declaration**: Every `ExecutionReceipt` explicitly carries `verificationClass = "cryptographic_signed_commitment_sha256"`. This proves secp256k1/EIP-712 provider identity binding, quote-to-execution binding, EIP-2 signature non-malleability, replay protection, and byte-exact SHA-256 integrity between what the provider signed and what the client received. It does **not** claim hardware TEE attestation or economic dispute-game re-execution of non-deterministic floating-point GPU logits; those guarantees compose orthogonally via separate HIP-1350 settlement/escrow and attestation layers.

| # | Adversarial Scenario | Fail-Closed Caught | Observed Error |
| ---: | --- | :---: | --- |
| 1 | `forged_quote_signature_rejected` | `true` | `ErrSignerMismatch (recovered signer != declared provider)` |
| 2 | `high_s_malleated_quote_rejected` | `true` | `ErrInvalidSignature (invalid or non-canonical EIP-2 high-S signature)` |
| 3 | `expired_quote_rejected` | `true` | `ErrQuoteExpired (quote expiresAt < now)` |
| 4 | `mismatched_output_hash_receipt_rejected` | `true` | `ErrOutputHashMismatch (signed receipt OutputHash != SHA-256 of returned text)` |
| 5 | `corrupted_output_content_rejected` | `true` | `ErrOutputHashMismatch (signed receipt OutputHash != SHA-256 of returned text)` |
| 6 | `replay_nonce_rejected` | `true` | `ErrReplayNonce (duplicate quote nonce rejected by local replay guard)` |
| 7 | `tampered_worker_record_rejected` | `true` | `ErrSignerMismatch (recovered signer != declared provider)` |

---

## 5. Three Honest Negative & Boundary Regimes

### Regime 1 — `unverified_receipt_negative` (`negative_control`, `40` trials, validated=`true`)

Without receipt verification, 100.0% (40/40) of silently corrupted provider outputs slipped through undetected; with fail-closed VerifyReceipt enabled, 100.0% (40/40) were rejected with ErrOutputHashMismatch.

### Regime 2 — `single_provider_no_failover_negative` (`negative_control`, `40` trials, validated=`true`)

Under 30% provider fault injection with failover disabled, completion collapsed to 70.0% (28/40 completed, 12 failed); enabling stateless multi-provider failover restored 100.0% (40/40) completion.

### Regime 3 — `stale_quote_storm_boundary` (`boundary_regime`, `40` trials, validated=`true`)

Under rapid price churn (every 4 quotes across 4 workers), 5ms TTL forced 4.00 quotes/req (0.0% cache hit, 3.095 ms avg quote refresh) vs 1000ms TTL at 1.90 quotes/req (70.0% cache hit, 1.278 ms avg quote refresh) while maintaining 100% completion and strict single-use execution nonces.

| Quote Cache TTL (ms) | Requests | Total Worker Quotes | Quotes / Req | Cache Hit Rate | Avg Quote Refresh (ms) | Avg Total Latency (ms) | Completion |
| ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| 5 | 40 | 160 | 4.00 | 0.0% | 3.095 | 3.878 | 100.0% |
| 25 | 40 | 82 | 2.05 | 65.0% | 2.137 | 3.140 | 100.0% |
| 150 | 40 | 76 | 1.90 | 70.0% | 1.295 | 1.700 | 100.0% |
| 1000 | 40 | 76 | 1.90 | 70.0% | 1.278 | 1.654 | 100.0% |

---

## 6. Reproducibility & Open-Source Compliance

- **License**: `Apache-2.0 OR MIT` on all new source files (`apps/gateway/*.go`, `cmd/gateway-bench/*.go`).
- **Host Isolation**: `apps/gateway` imports zero packages from `internal/...` or `cmd/...` (`TestAppsImportNothingOfTheirHost` passes).
- **Zero Dependency Bloat**: `go.mod` and `go.sum` are 100% unmodified.

```bash
# Format and static checks
gofmt -s -w apps/gateway cmd/gateway-bench
CGO_ENABLED=0 go vet ./apps/gateway/... ./cmd/gateway-bench/...
CGO_ENABLED=0 go build -o /dev/null ./cmd/cloud ./cmd/gateway-bench
CGO_ENABLED=0 go test -v ./apps -run TestAppsImportNothingOfTheirHost

# Full unit, dual-gateway, kill-switch, adversarial, 16-client -race stress & negative regime tests
go test -race -v -count=1 ./apps/gateway/... ./cmd/gateway-bench/...

# Regenerate raw_results.json, score_table.md, and README.md
go run ./cmd/gateway-bench -write-dossier research/hip-1350-gateway
```
