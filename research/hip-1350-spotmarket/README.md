# HIP-1350 Research Dossier: Open GPU Spot Market & Capacity Auctions (`hanzoai/cloud#14`)

## 1. Problem Statement & Protocol Design

Centralized GPU inference APIs lock buyers into fixed posted rate cards, single-operator failure domains, and opaque routing. This dossier implements and evaluates an open, permissionless GPU spot market and Akash-inspired reverse-auction marketplace (`apps/spotmarket`) licensed under `Apache-2.0 OR MIT` with zero host imports and zero Hanzo control-plane dependencies:

- **Signed Provider Offers (`SignedOffer`)**: Providers advertise hardware specs (`H100-SXM5-80GB`, `A100-80GB`, `MI300X-192GB`, `L40S-48GB`, `RTX-4090-24GB`), available VRAM, supported models, region/jurisdiction constraints, integer microdollar pricing, SLO commitments, expiry, and monotonic nonces signed over a canonical domain-separated SHA-256 commitment using `secp256k1` with strict **EIP-2 low-S** normalization and bound provider signing keys.
- **Neutral Enso Multi-Dimensional Selector (`RankCandidates`)**: Ranks eligible bids by normalized price, measured TTFT latency, observed Laplace-smoothed reliability, and explicit buyer policy constraints (model, VRAM, allowed/blocked jurisdictions, max price, max TTFT, optional verifier-backed hardware attestation). Provider `OperatorClass` labels (`hanzo` vs `independent` vs `community`) are never read by the selector; verified via bit-identical score parity tests (`ZeroHanzoPreferenceVerified = true`).
- **Reverse-Auction Lifecycle & Deterministic Ledger**: `Order -> SignedBid -> Lease -> SettlementReceipt` supporting multi-provider partial fills, mid-lease crash failover, collateral slashing on bait-and-switch or latency/throughput SLA breach, checked 128-bit integer cost/bond arithmetic, and exact integer microdollar ledger conservation (`0` microdollar drift).
- **Dual-Surface HTTP API**: Mounted on `github.com/zap-proto/zip` via strictly typed operations (`Mount`, projecting into OpenAPI/MCP/CLI/SDKs) with host-validated principal binding, plus a standalone standard-library `net/http` (`Handler`) surface.
- **Sovereignty & Permissionless Gossip**: Exposes a deterministic `GossipSnapshot` and cryptographically signed `MarketExportBundle` covering complete lease terms, pending orders/bids, provider key bindings, and receipts for offline verification and cold-start forking.

## 2. Threat Model & Explicit Verification Boundaries

Per HIP-1350 proof-hygiene requirements, this protocol never conflates cryptographic signatures, hardware attestations, and economic collateral:

| Mechanism | `VerificationClass` | What It Proves | What It Does NOT Prove |
|---|---|---|---|
| Canonical SHA-256 + `secp256k1` EIP-2 low-S ECDSA + nonce tracking | `crypto_signature` | Signer key control, bound provider identity, payload integrity, non-malleability, anti-replay, and expiry compliance | Physical GPU ownership or honest token generation |
| Bonded escrow, agreed-rate settlement enforcement, and SLA breach slashing | `economic_stake_slashing` | Deterministic buyer compensation and rapid Enso reliability demotion when a provider attempts bait-and-switch pricing, breaches latency/throughput SLOs, or crashes mid-lease | Mathematical proof of inference weights or prevention of griefing losses exceeding posted bond |
| Optional confidential-compute GPU quote (`AttestationQuoteHash`) | `hardware_attestation` | Physical enclave measurement strictly when validated by a configured `AttestationVerifier` (fails closed when unconfigured) | **Not claimed in this simulation (`0/100` hardware-TEE attested)**; buyers requiring TEE quotes can enforce `Order.RequireHardwareAttested = true` |

## 3. Experimental Method & Reproducibility

- **Deterministic Seed**: `1350` (`100` heterogeneous GPU providers across 5 regions & 6 jurisdictions, `24` hourly epochs, `293` submitted orders in baseline/primary regimes, `170402285` total requested tokens).
- **Variable Demand**: Diurnal sine demand wave, 1.8x bursty traffic peaks (epochs 7–9 and 17–19), sudden 15% fleet capacity shock (epochs 12–14), EU/Swiss jurisdictional residency policies, tight TTFT policies, and multi-provider partial-fill batch orders.
- **Provider Failures & Failover**: Stochastic mid-lease GPU crashes elevated during capacity shock epochs; Enso detects incomplete delivery, refunds undelivered escrow, slashes breach bond, demotes failing provider reliability, and fails over remaining tokens to healthy peers (which execute under the same failure and adversarial model).
- **7 Adversarial Attack Vectors**: `forged_signature`, `high_s_malleated`, `expired_offer_or_bid`, `replay_nonce`, `sybil_spam_bids`, `stale_capacity_overclaim`, and `bait_and_switch_non_delivery`.
- **Reproduce Exact Results**:

```bash
go run ./cmd/spotmarket-bench -seed 1350 -providers 100 -epochs 24 -write-dossier research/hip-1350-spotmarket
go test -race -v -count=1 ./apps/spotmarket/... ./cmd/spotmarket-bench/...
CGO_ENABLED=0 go test -v ./apps -run TestAppsImportNothingOfTheirHost
```

## 4. Measured Results Across All 5 HIP-1350 Dimensions

1. **Cost**: Primary decentralized spot market achieves **$0.3020 / 1M useful tokens** (explicitly including 39290 uUSD cryptographic verification overhead and 3069 uUSD settlement overhead; **$0.2954 / 1M useful tokens** net after buyer slash compensation) vs **$0.8292 / 1M useful tokens** on the fixed-price baseline — a **63.59% cost reduction** with **0 uUSD ledger conservation drift** across all 24 epochs.
2. **Performance**: Spot TTFT p50/p95/p99 = **99.09 / 124.87 / 132.95 ms** (vs fixed baseline **185.16 / 369.07 / 397.38 ms**), mean throughput = **109.62 tok/s** (vs **98.77 tok/s**), delivery-based order completion rate = **98.29%** (auction clear rate **100.00%**, token fulfillment **99.02%**), with **19/24 (79.17%)** mid-lease crash & bait-and-switch failovers recovered automatically and **24** multi-provider partial-fill orders.
3. **Trust**: **673/673 (100.00%)** injected adversarial bids, offers, and settlement bait-and-switch attempts detected and mitigated fail-closed across all 7 threat vectors (`crypto_signature` + `economic_stake_slashing`). Hardware TEE attestation is honestly reported as **0/100 attested (not claimed)**.
4. **Decentralization**: With `HanzoControlPlaneDisabled = true` and all Hanzo-labeled peers offline, **100.00%** of completed orders ran with **`HanzoCalls = 0`**, spread across **20** active winning independent/community providers (**HHI = 0.0862** vs `1.0000` centralized baseline) and `ZeroHanzoPreferenceVerified = true`.
5. **Sovereignty**: Live signed `MarketExportBundle` (**625253 bytes** covering 95 offers, 341 leases, 341 receipts, 95 peers) exported in **14.98 ms**, verified in **21.86 ms**, and cold-start forked into an isolated `Market` in **16.40 ms** (total migration **53.24 ms**) with **0 uUSD** fork ledger drift, `PostForkClearOK = true`, and `PostForkHanzoCalls = 0`.

## 5. Honest Negative & Boundary Regimes

To verify that security controls and economic limits reflect real protocol boundaries rather than optimistic modeling, three negative/boundary regimes are executed on the exact same codebase:

1. **Unverified & Unslashed Negative Control (`neg_unverified_no_slashing_control`)**: Disabling `VerifySignatures` and `EnforceSlashing` collapses adversarial detection to **24/672 (3.57%)**, drops delivery-based order completion to **93.17%** (despite **100.00%** auction clearing) and token fulfillment to **94.08%**, and drives realized cost to **$0.3214 / 1M useful tokens** (vs **$0.3020** with controls enabled) as unslashed bait-and-switch and forged lowball bids win auctions and burn buyer escrow.
2. **Fleet Capacity Saturation Boundary (`neg_fleet_capacity_saturation`)**: A 3.5x demand surge (1023 submitted orders exceeding 100-provider concurrent slots) causes spot clearing prices to surge toward buyer reserve ceilings (**$0.5250 / 1M useful tokens**, savings compressed to **36.68%**) and triggers graceful admission rejection (**0.88%** rejected, **98.83%** delivery completion) with **0 uUSD** ledger drift and 100% automatic escrow refund for unfilled orders.
3. **Ultra-Short 5s TTL Churn Boundary (`neg_ultra_short_ttl_churn`)**: Tightening offer/bid TTL to 5 seconds forces **14400** offer re-signs and **34836** signature verifications (vs **19645** in primary), multiplying verification overhead to **69672 uUSD** (vs **39290 uUSD**) and causing transient in-flight bid expiry rejections (delivery completion **98.63%**, realized cost **$0.3225 / 1M useful tokens**).

## 6. Limitations & Pre-Production Security Review

- **External Audit Required Before Real Funds**: Settlement escrow contracts, cross-chain slashing bridges, and dispute arbitration require independent third-party security review before holding real mainnet funds.
- **Inference Quality Verification**: Cryptographic signatures and SLA slashing verify offer authenticity and delivery accounting, not bit-exact model weights. Combining this spot market with verifiable compute challenges (`hanzoai/cloud#17`) or hardware TEE quotes (`AttestationClass = hardware_attestation`) closes the inference-correctness gap.
- **Full Tables & Raw Telemetry**: See [`score_table.md`](./score_table.md) and [`raw_results.json`](./raw_results.json) for complete machine-readable outputs.
