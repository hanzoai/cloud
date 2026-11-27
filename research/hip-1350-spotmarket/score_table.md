# HIP-1350 Score Table — Open GPU Spot Market & Capacity Auctions

Generated deterministically from `go run ./cmd/spotmarket-bench -seed 1350 -providers 100 -epochs 24 -write-dossier research/hip-1350-spotmarket`.
Every figure below matches `raw_results.json` to the exact decimal.

## 1. Five-Dimension Regime Comparison

| Regime | Realized Cost ($/1M useful tok) | Savings vs Baseline | Ledger Drift (uUSD) | TTFT p50 / p95 / p99 (ms) | Mean Throughput (tok/s) | Clear / Completion Rate | Adversarial Detected | Hanzo Calls | Completion w/o Hanzo |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| `fixed_price_baseline` | $0.8292 | 0.00% | 0 | 185.16 / 369.07 / 397.38 | 98.77 | 98.63% / 98.63% | n/a | 293 | 0.00% |
| `hip1350_decentralized_spot_100p` | $0.3020 | 63.59% | 0 | 99.09 / 124.87 / 132.95 | 109.62 | 100.00% / 98.29% | 673/673 (100.00%) | 0 | 100.00% |
| `neg_unverified_no_slashing_control` | $0.3214 | 61.24% | 0 | 98.38 / 130.36 / 143.83 | 107.49 | 100.00% / 93.17% | 24/672 (3.57%) | 0 | 100.00% |
| `neg_fleet_capacity_saturation` | $0.5250 | 36.68% | 0 | 103.16 / 137.72 / 153.81 | 106.40 | 99.12% / 98.83% | 4342/4342 (100.00%) | 0 | 100.00% |
| `neg_ultra_short_ttl_churn` | $0.3225 | 61.11% | 0 | 99.51 / 134.07 / 140.50 | 108.36 | 99.66% / 98.63% | 683/683 (100.00%) | 0 | 100.00% |

## 2. Cost Decomposition (Primary Spot vs Fixed Baseline)

| Metric | Fixed-Price Baseline | Primary HIP-1350 Spot (100 Providers) |
|---|---:|---:|
| Useful Tokens Delivered | 168895109 | 168724017 |
| Gross Compute Spend (uUSD) | 140048576 | 50904212 |
| Cryptographic Verification Overhead (uUSD) | 0 | 39290 |
| Settlement Ledger Overhead (uUSD) | 0 | 3069 |
| Buyer Slashing Compensation Received (uUSD) | 0 | 1111779 |
| Realized Cost (incl. Verify + Settle, $/1M useful tok) | $0.8292 | $0.3020 |
| Net Cost After Slash Compensation ($/1M useful tok) | $0.8292 | $0.2954 |
| Realized Savings vs Baseline | 0.00% | 63.59% |
| Ledger Conservation Drift (uUSD) | 0 | 0 |

## 3. Adversarial Trust Breakdown by Verification Class (Primary Spot)

| Attack Vector | Verification Class | Injected | Detected | Detection Rate |
|---|---|---:|---:|---:|
| `forged_signature` | `crypto_signature` | 96 | 96 | 100.00% |
| `high_s_malleated` | `crypto_signature` | 96 | 96 | 100.00% |
| `expired_offer_or_bid` | `crypto_signature` | 24 | 24 | 100.00% |
| `replay_nonce` | `crypto_signature` | 67 | 67 | 100.00% |
| `sybil_spam_bids` | `crypto_signature` | 318 | 318 | 100.00% |
| `stale_capacity_overclaim` | `crypto_signature` | 69 | 69 | 100.00% |
| `bait_and_switch_non_delivery` | `economic_stake_slashing` | 3 | 3 | 100.00% |
| **Total Adversarial Events** | `crypto_signature` + `economic_stake_slashing` | **673** | **673** | **100.00%** |
| Hardware TEE Attestation (`hardware_attestation`) | `hardware_attestation` | 100 providers | 0 attested | **not claimed (0.00%)** |

## 4. Decentralization & Sovereignty Measurements

| Metric | Measured Value |
|---|---:|
| Hanzo Control-Plane Calls (`HanzoCalls`) | 0 |
| Completion Without Hanzo Infrastructure | 100.00% |
| Zero-Hanzo-Preference Parity Verified | true |
| Active Winning Providers (out of 100) | 20 |
| Provider Share Concentration (HHI, lower = more decentralized) | 0.0862 |
| Signed Export Bundle Size | 625253 bytes |
| Exported State Objects (Offers / Leases / Receipts / Peers) | 95 / 341 / 341 / 95 |
| Export / Verify / Cold-Start Import Latency | 14.98 ms / 21.86 ms / 16.40 ms (total 53.24 ms) |
| Post-Fork Conservation Drift (uUSD) | 0 |
| Post-Fork Independent Order Clear & Settle OK | true (HanzoCalls=0) |
