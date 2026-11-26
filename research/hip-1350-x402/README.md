# Permissionless x402 Payments & Provider Settlement (`hanzoai/cloud#16`, HIP-1350)

**Issue**: [hanzoai/cloud#16](https://github.com/hanzoai/cloud/issues/16) — `p0` Permissionless x402 Payments and Provider Settlement  
**Program**: Fully Decentralized Hanzo (`HIP-1350`)  
**Implementation**: [`apps/x402`](../../apps/x402) · **Benchmark CLI**: [`cmd/x402-bench`](../../cmd/x402-bench) · **Raw Results**: [`results.json`](results.json)

---

## 1. Primary References & Full License Audit

Every protocol primitive in this contribution is a clean-room Go implementation (`SPDX-License-Identifier: Apache-2.0 OR MIT`) layered directly onto the repository's existing x402 v2 package (`apps/x402` -> `github.com/hanzoai/commerce/payment/x402`) with **zero new external Go module dependencies** (`go.mod` and `go.sum` untouched).

| Audited Component | Primary Reference / Module | License | Audit Verdict |
|---|---|---|---|
| x402 v2 HTTP Payment Protocol | `github.com/x402-foundation/x402` (`specs/x402-specification-v2.md`, `specs/transports-v2/http.md`, `specs/schemes/exact/scheme_exact_evm.md`) | **Apache-2.0** | Specification referenced cleanly; wire headers (`PAYMENT-REQUIRED`, `PAYMENT-SIGNATURE`, `PAYMENT-RESPONSE`) and Section 7 facilitator HTTP contract (`/verify`, `/settle`) reused via `github.com/hanzoai/commerce/payment/x402`. |
| ERC-3009 (`TransferWithAuthorization`) & EIP-712 | EIP-3009 / EIP-712 / FiatTokenV2 (`TRANSFER_WITH_AUTHORIZATION_TYPEHASH = 0x7c7c6cdb...`, `EIP712Domain = 0x8b73c3c6...`) | **CC0-1.0 / Apache-2.0** | Standard typehashes pinned and verified; secp256k1 ECDSA signing and `ecrecover` executed via `github.com/hanzoai/commerce/payment/x402` + `github.com/luxfi/crypto`. |
| Existing Hanzo x402 Protocol | `github.com/hanzoai/commerce/payment/x402` (`v1.50.247`, already in `go.mod`) | **MIT OR Apache-2.0** | Directly extended — **zero competing second protocol**. |
| secp256k1 & Keccak-256 | `github.com/luxfi/crypto` (`v1.20.13`, already in `go.mod`) | **BSD-3-Clause / Apache-2.0** | Already a direct dependency in `go.mod`; used for EIP-712 / Keccak-256 digests and secp256k1 signing/recovery. |
| Router & Typed Ops | `github.com/zap-proto/zip` (`v1.37.44`, already in `go.mod`) | **Apache-2.0 / MIT** | All control/inspection routes registered as typed `zip` operations under `/v1/x402/*`. |
| External Datasets / Proprietary APIs | None | N/A | Zero proprietary SaaS keys, zero static wallets, zero Circle/Hanzo runtime coupling. Ephemeral secp256k1 keys are generated at runtime in tests and benchmarks. |

---

## 2. Canonical Stack Integration & Versioned Interfaces (`x402/v1`)

Per `hanzoai/cloud#16` Rule #1 (*do not build another isolated AI stack*) and `apps/imports_test.go` (*an app imports `zip`, stdlib, and public modules, never its host*), all extensions live inside [`apps/x402`](../../apps/x402) and compose cleanly over `github.com/hanzoai/commerce/payment/x402`:

1. **`Facilitator` & `MultiFacilitator` (`apps/x402/facilitator.go`)**:
   - Aliases `protocol.Facilitator` (x402 Section 7 `Verify` / `Settle`) and implements `MultiFacilitator` across 2+ `NamedFacilitator` endpoints with `StrategyFailover` or `StrategyRoundRobin`, per-claim settlement pinning (so `Settle` always targets the facilitator that ran `Verify`), health cooldowns, and zero-downtime `SwitchFacilitators` live migration.
   - `NewCircleFacilitator` remains an optional, non-mandatory adapter over `protocol.Remote`.
   - `HTTPFacilitatorServer` provides a standalone x402 Section 7 HTTP facilitator (`POST /verify`, `POST /settle`, `POST /reverse`, `GET /health`) enforcing real EIP-712 ERC-3009 verification and atomic nonce tracking.
2. **`QuoteSigner` & `SignedPriceQuote` (`apps/x402/quote.go`)**:
   - Provider signs an exact price quote (`quoteId`, `version`, `resource`, `model`, `maxTokens`, `requestBodyHash`, `network`, `asset`, `amountAtomic`, `payTo`, `issuedAt`, `expiresAt`, `providerAddress`) using its secp256k1 key and attaches it in `PaymentRequired.Extensions["hanzo.signedQuote"]`.
   - Both the client (`AgentClient`) and server (`SettlementGuard`) cryptographically verify signature recovery, validity window, and exact SHA-256 `requestBodyHash` binding before signing funds or running inference.
3. **`NonceReplayStore` (`apps/x402/replay.go`)**:
   - Thread-safe `MemoryNonceStore` and durable append-only JSONL `NewFileNonceStore` atomically distinguish:
     - **Idempotent duplicate retry** (matching `Idempotency-Key` + `requestBodyHash` + authorization claim -> serves cached response and original `PAYMENT-RESPONSE` with `X-Idempotent-Replay: true`, charging once).
     - **Idempotency payload mismatch** (same `Idempotency-Key` with mutated request body -> `409 Conflict`).
     - **Replayed ERC-3009 signature / concurrent double-spend** -> strictly refused (`402 Payment Required` / `409 Conflict`) atomically under concurrent races.
4. **`ReceiptLedger` & Reversal Reconciliation (`apps/x402/receipt.go`)**:
   - Hash-chained (`PrevHash` -> SHA-256 `RecordHash`), secp256k1 provider-signed, monotonically sequenced (`Seq`) receipt store (`MemoryReceiptLedger` / `NewFileReceiptLedger`).
   - Failed inference (`err != nil` or HTTP `>= 400`) skips settlement, releases the unspent nonce reservation, and records a `VOIDED` receipt (`chargedAtomic = "0"`).
   - `ReconcileReversal` deterministically resolves reversals by `ReceiptID` or `(FacilitatorID, Transaction)` (rejecting cross-facilitator spoofing) and transitions the receipt to `REVERSED` or `REFUND_REQUIRED` (`chargedAtomic = "0"`).
   - `ExportJSONL` + `VerifyExportJSONL` allow any user or provider to export and independently verify the full receipt chain offline without Hanzo.
5. **Typed `zip` Operations (`apps/x402/x402.go`)**:
   - `GET /v1/x402/prices` (existing price table, unchanged)
   - `POST /v1/x402/quote` (provider-signed quote issuance bound to `requestBodyHash`)
   - `GET /v1/x402/receipts` (filterable receipt listing)
   - `GET /v1/x402/receipts/export` (sovereign JSONL export)
   - `POST /v1/x402/reconcile` (facilitator reversal reconciliation)
   - `GET /v1/x402/facilitators` (independent facilitator health & settlement telemetry)

---

## 3. Falsifiable Hypotheses, Threat Model & Proof-Claim Discipline

Per `hanzoai/cloud#16` Rule #3, we explicitly separate **cryptographic guarantees** from **economic / operational assumptions**:

- **Cryptographically proven locally (zero trusted intermediary)**:
  - Payer authorization authenticity & non-malleability (EIP-712 `TransferWithAuthorization`, secp256k1 low-`s` recovery via `protocol.Verify`).
  - Provider price & request-body integrity (`SignedPriceQuote` Keccak-256 digest + secp256k1 recovery bound to `SHA-256(body)`).
  - Offline receipt integrity & non-repudiation (SHA-256 hash chain + secp256k1 provider signature over every receipt and reversal transition).
- **Operational / facilitator trust boundaries (explicitly NOT claimed as cryptographic proofs)**:
  - On-chain token balance sufficiency and finality of ERC-3009 settlement depend on the chosen facilitator submitting `transferWithAuthorization` to the target EVM chain and the chain reaching finality. Reorgs or reverts require reversal reconciliation (`POST /v1/x402/reconcile`).
  - Cross-replica replay protection requires either a shared `NonceReplayStore` or the facilitator's atomic nonce ledger (and ultimately the ERC-3009 token contract's `authorizationState[authorizer][nonce]` mapping on-chain).
  - **External Audit Notice**: Security-critical smart contracts and production settlement keys require independent third-party review before holding real mainnet funds.

### Declared Hypotheses

- **H1 (Fraud & Replay Detection)**: Synchronous pre-inference verification of `SignedPriceQuote`, EIP-712 `TransferWithAuthorization`, and atomic `NonceReplayStore` catches `100%` of tampered quotes, body swaps, expired quotes, bad signatures, wrong recipients, nonce replays, 16-goroutine concurrent double-spend races, and cross-facilitator reversal spoofs, while charging `$0.00` on failed inference.
- **H2 (Zero-Hanzo Decentralization & Failover)**: Running across two independent x402 Section 7 HTTP facilitators achieves `100%` Hanzo-independent settlement and sustains `100%` completion under mid-run primary facilitator outage via automatic failover.
- **H3 (Economic Boundary — L1 vs L2/Batched Settlement)**: Per-request settlement is economically viable on L2 Base for `>= $0.002` inference tickets (`< 10%` gas share), whereas sub-cent micro-prompts (`$0.0001`) on Ethereum L1 suffer `> 99.9%` gas overhead, proving L2 or batched/channel amortization is mandatory below the break-even ticket size.

---

## 4. Verification & Concurrency Test Matrix

Reproduce all tests and benchmarks locally:

```bash
CGO_ENABLED=0 go test -v ./apps -run TestAppsImportNothingOfTheirHost
go test -race -v -count=1 ./apps/x402/... ./cmd/x402-bench/...
go test -race -count=20 ./apps/x402 -run 'TestSettlementReversalAndDeterministicReconciliation|TestReplayedSignatureAndConcurrent16GoroutineRace'
CGO_ENABLED=0 go run ./cmd/x402-bench -n 200 -c 8 -out research/hip-1350-x402/results.json
```

Covered tests in [`apps/x402/permissionless_test.go`](../../apps/x402/permissionless_test.go) + [`apps/x402/x402_test.go`](../../apps/x402/x402_test.go) + [`cmd/x402-bench/main_test.go`](../../cmd/x402-bench/main_test.go):
- Standard ERC-3009 & EIP-712 typehash pinning + secp256k1 round-trip & mutation rejection (`TestERC3009StandardTypeHashesAndEIP712RoundTrip`)
- End-to-end HTTP 402 -> quote verify -> EIP-712 sign -> infer -> settle across **2 independent HTTP facilitators** (`TestEndToEndTwoIndependentHTTPFacilitators`)
- Idempotent duplicate retry (single charge, identical cached response) + payload-mismatch `409 Conflict` (`TestIdempotentDuplicateRequestNoDoubleCharge`)
- Replayed signature rejection + **16-goroutine barrier-synchronized concurrent double-spend race** under `-race` (`TestReplayedSignatureAndConcurrent16GoroutineRace`, verified `20/20` runs)
- Failed inference (`HTTP 500`) skips settlement (`$0` charged, `VOIDED` receipt) and leaves authorization valid for retry (`TestFailedInferenceSkipsSettlementChargesZero`)
- Deterministic per-facilitator settlement reversal reconciliation under frozen clock ties + cross-facilitator spoof rejection + export tamper detection (`TestSettlementReversalAndDeterministicReconciliation`, verified `20/20` runs)
- Tampered amount, tampered model, swapped request body, dropped quote, and expired quote rejection (`TestTamperedAndExpiredSignedQuoteRejected`)
- Primary facilitator outage failover + zero-downtime live `SwitchFacilitators` migration (`TestFacilitatorOutageFailoverAndZeroDowntimeMigration`)
- File-backed `NewFileNonceStore` + `NewFileReceiptLedger` persistence across restart (`TestPersistentFileNonceStoreAndReceiptLedgerRestart`)
- Multi-facilitator JSON `Load`, optional `NewCircleFacilitator` Bearer adapter, and `zip` typed ops (`TestZipTypedOpsAndMultiFacilitatorConfigLoad`)

---

## 5. Reproducible 5-Dimension Benchmark Score Table

Measured live on `linux/amd64` (`go1.27.1`, `2` vCPU, `CGO_ENABLED=0`, `N = 200` paid HTTP inference calls, `C = 8` concurrent agents, `1,000` useful tokens/call @ `$0.002` = `$2.00/1M` tokens, across two independent loopback x402 HTTP facilitators). Raw JSON: [`results.json`](results.json).

| Dimension | Metric | Live Measured Result |
|---|---|---|
| **1. Cost** | Inference-only (`$/1M` useful tokens) | **`$2.0000`** (`200,000` useful tokens, `$0.4000` settled) |
| | Base L2 per-request (`$0.00018/tx` @ 65k gas) | **`$2.1800 / 1M tokens`** (`8.26%` fee share; 10% break-even ticket = `$0.0018`) |
| | Base L2 100x batched/channel amortized | **`$2.0018 / 1M tokens`** (`0.09%` fee share) |
| | Ethereum L1 per-request (`$2.925/tx` @ 15 gwei, `$3k` ETH) | **`$2,927.00 / 1M tokens`** (`99.93%` fee share; 10% break-even ticket = `$29.25`) |
| **2. Performance** | Completion Rate | **`100.00%`** (`200 / 200`) |
| | Throughput | **`329.05 req/s`** (`0.6078 s` total wall time) |
| | Paid-leg TTFT (`p50` / `p95` / `p99`) | **`16.99 ms` / `32.67 ms` / `38.06 ms`** (`+16.87 ms` p50 over `0.12 ms` unguarded loopback) |
| | Full 402 -> sign -> verify -> infer -> settle E2E (`p50` / `p95` / `p99`) | **`24.04 ms` / `41.56 ms` / `44.47 ms`** |
| **3. Trust** | Live Adversarial Fraud/Replay/Reversal Detection | **`100.00%`** (`9 / 9` probes caught, `0` double-spends in 16x race, `$0.0000` failed-infer charge) |
| **4. Decentralization** | Hanzo-Independent Share | **`100.00%`** (`100` settled via `independent-facilitator-a`, `100` via `independent-facilitator-b`, `0` Hanzo-operated, `0` mandatory Hanzo deps) |
| **5. Sovereignty** | Offline JSONL Export Verification & Live Migration | **`204 / 204`** signed entries verified (`chain_intact=true`, `signatures_intact=true`, `tamper_detected=true`); **`10 / 10`** zero-downtime live migration calls to Facilitator C with post-migration replay blocked |

---

## 6. Honest Negative & Boundary Regimes

| Regime | Setup | Measured Outcome | Engineering Takeaway |
|---|---|---|---|
| **`micro_prompt_l1_gas_dominance`** | `40` live micro-prompts (`64` tokens, `$0.0001` ticket = `100` atomic USDC units) | L1 fee share **`99.997%`** (`$45,704.69/1M tok`); L2 1x fee share **`64.29%`** (`$4.38/1M tok`); L2 100x batch **`1.77%`** (`$1.5906/1M tok`) | Per-request on-chain settlement is economically unviable on L1 and inefficient even on L2 below `$0.0018/req`. Production deployments serving sub-cent micro-prompts must use L2 + batched/channel settlement. |
| **`optimistic_unverified_settlement_negative`** | Negative control server skipping pre-inference EIP-712 verification & `NonceReplayStore`, hit by `16` concurrent replays | **`100.00%` replay drain** (`16 / 16` unauthorized executions served; `0.00%` defense) | Confirms synchronous pre-inference signature verification and atomic nonce reservation are strictly required; post-hoc settlement cannot prevent compute drain. |
| **`facilitator_outage_failover`** | `40` live paid calls (`StrategyFailover`); Facilitator A HTTP server closed after request `20` | **`100.00%` completion** (`20` pre-outage on FacA, `20` post-outage on FacB, `1` automatic failover, `0` Hanzo fallbacks) | Multi-facilitator routing eliminates single-facilitator liveness failure without falling back to any Hanzo-operated service. |
