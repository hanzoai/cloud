# Full Sovereignty & Network Exit Test — HIP-1351 Reference Implementation & Benchmark (`hanzoai/cloud#20`)

**Issue**: [hanzoai/cloud#20](https://github.com/hanzoai/cloud/issues/20) (`p0`, `hip-1351`, `decentralized`, `sovereignty`)  
**Packages**: [`apps/sovereignty`](../../apps/sovereignty) (host-independent `zip` app & multi-server exit harness) · [`cmd/sovereignty-bench`](../../cmd/sovereignty-bench) (acceptance & 5-dimension benchmark CLI)  
**Raw Measured Artifact**: [`results.json`](./results.json)  
**License**: `Apache-2.0 OR MIT` (zero new external Go dependencies; `go.mod` / `go.sum` untouched)

---

## 1. Core Acceptance Invariant & Architecture

> **Core Invariant (`hanzoai/cloud#20`)**:
> *Deliberately disable Hanzo-operated gateways, registries, schedulers and infrastructure; a separately administered network still accepts new providers, discovers models, executes and verifies a request, settles payment and recovers the user's authorized data. A network that requires `api.hanzo.ai` to stay online is not decentralized at the protocol level.*

`apps/sovereignty` implements a clean-room, host-independent multi-server loopback decentralized network exit harness in pure Go (`CGO_ENABLED=0` compatible, zero network mocks). Every node runs on its own real loopback HTTP server (`net/http/httptest` / `zip.App`), using real ephemeral runtime `secp256k1` keys (EIP-2 low-S enforcement, EIP-55 addresses, domain-separated EIP-712 typed data signatures), real `SHA-256` content-addressed model storage, and real client-side `AES-256-GCM` user-sovereign encryption. It imports only `github.com/zap-proto/zip`, `github.com/luxfi/crypto/secp256k1`, `github.com/luxfi/log`, `golang.org/x/crypto/sha3`, and the Go standard library (`TestAppsImportNothingOfTheirHost` passes):

1. **Real Multi-Server Dual-Plane Topology (`apps/sovereignty/nodes.go`)**:
   - **Control Plane A (Hanzo-Operated Convenience Plane)**: Six real loopback HTTP servers (`hanzo-gateway`, `hanzo-registry`, `hanzo-mirror`, `hanzo-scheduler`, `hanzo-facilitator`, `hanzo-vault`) explicitly terminated (`Close()`) mid-drill.
   - **Control Plane B (Independent Community Operator Plane)**: Independently administered multi-peer bootstrap/discovery cluster (`BootstrapServer`), content-addressed model artifact mirror (`ArtifactMirrorServer`), stateless community job scheduler (`SchedulerServer`), EIP-712 payment facilitator (`FacilitatorServer`), and non-custodial encrypted data vault (`SovereignVaultServer`).
   - **Independent Compute Provider Workers (`ProviderNodeServer`)**: Separate HTTP inference nodes holding ephemeral `secp256k1` keys, downloading and `SHA-256`-verifying model weights before executing deterministic verifiable inference (`VerificationClass = deterministic_recomputation_sha256` — never conflating deterministic cryptographic verification with hardware attestations or economic dispute games).
2. **Real HTTP/DNS Host Blocker & Kill Switch (`apps/sovereignty/guard.go`)**:
   - Wraps a real `net/http.Transport` (`HostBlocker`) and tracks exact atomic counts of Hanzo vs. community HTTP calls before and after `KillHanzoControlPlane()`.
   - Engaging the Kill Switch immediately closes every Hanzo server listener AND blocks `api.hanzo.ai`, `*.hanzo.ai`, and every registered Hanzo loopback endpoint with `ErrHanzoHostBlocked`.
3. **Standalone `zip` HTTP Surface & Portable Fork Bundles (`apps/sovereignty/service.go`, `apps/sovereignty/drill.go`)**:
   - Mounts `/v1/sovereignty/*` (`/health`, `/drill/run`, `/drill/latest`, `/regimes/run`, `/fork/export`, `/fork/verify`) onto any `zip.App` with zero host imports.
   - Exports signed, self-verifying `NetworkForkBundle` state snapshots (`VerifyForkBundleOffline` + `HydrateFromFork`) allowing any third-party operator (Operator C) to fork or migrate the entire provider directory, model catalog, hash-chained settlement ledger, governance decisions, and encrypted user vault without contacting Hanzo.

---

## 2. Eight-Stage Blackout Acceptance Pipeline (`POST /v1/sovereignty/drill/run`)

Executed end-to-end over a live mounted `zip.App` (`/v1/sovereignty/*`) in `120.774 ms` (`exitDrill.totalDurationMs = 112.597 ms`, `HanzoCallsBeforeKill = 10`, **`HanzoCallsAfterKill = 0`**, `CommunityCallsAfterKill = 18`):

| Stage | Name | Plane | Duration (ms) | Hanzo Calls | Observed Result |
| ---: | --- | --- | ---: | ---: | --- |
| 1 | `baseline_hanzo_online` | `hanzo_convenience` | `32.845` | `10` | Baseline job settled & non-custodial AES-256-GCM workspace stored (`CustodialEscrow = false`) |
| 2 | `kill_switch_hanzo_blackout` | `blackout_transition` | `0.579` | `0` | All 6 Hanzo servers closed; `api.hanzo.ai` & gateway hard-blocked; Community Plane B live |
| 3 | `new_provider_onboarding_under_blackout` | `community_independent` | `7.916` | `0` | Brand-new `provider-bravo-post-blackout` verified weights & registered on Community Plane B |
| 4 | `decentralized_peer_and_model_discovery` | `community_independent` | `2.121` | `0` | Discovered post-blackout provider & `SHA-256`-verified `16,384 B` weights from community mirror |
| 5 | `stateless_scheduling_and_inference` | `community_independent` | `1.096` | `0` | Community scheduler dispatched `job-sovereign-exit-001` -> `128` deterministic tokens |
| 6 | `independent_offline_verification` | `community_independent` | `0.510` | `0` | Verified EIP-712 provider signature + deterministic `SHA-256` output digest offline |
| 7 | `independent_payment_settlement` | `community_independent` | `0.941` | `0` | Settled `450` atomic units via `community-op-b-facilitator` (`ConservationDrift = 0`) |
| 8 | `authorized_data_recovery_and_fork_migration` | `community_independent` | `21.024` | `0` | Recovered `151 B` workspace byte-exact, rejected unauthorized key, ratified 2/2 governance, forked `4,109 B` bundle to Operator C |

---

## 3. Five HIP-1351 Benchmark Dimensions (Measured, `N = 200` E2E, `20` RTO Drills, `CGO_ENABLED=0`)

| Dimension | Metric | Measured Value |
| --- | --- | --- |
| **1. Cost** | EVM L1 direct settlement (`65k` gas @ `20 gwei`, ETH = \$3,000, `10k` tok/job) | **\$390.00 / 1M useful tokens** (\$3.90 / job) |
| **1. Cost** | EVM L2 direct settlement (`65k` gas @ `0.05 gwei` eff., `10k` tok/job) | **\$0.975 / 1M useful tokens** (\$0.00975 / job) |
| **1. Cost** | Community facilitator + L2 batched anchor (`90k` gas / 100 jobs @ `0.05 gwei`) | **\$0.0135 / 1M useful tokens** |
| **1. Cost** | Measured local verify + decrypt + discovery CPU (`0.303 s` / 1M tok @ \$0.04/vCPU-hr) | **\$0.000003 / 1M useful tokens** |
| **1. Cost** | **Total recommended sovereign exit path (L2 batched + local verify/decrypt)** | **\$0.013503 / 1M useful tokens** |
| **2. Performance** | **Blackout Failover RTO** (`20` full multi-server kill-switch drills) | **p50 0.447 ms · p95 0.921 ms · p99 1.709 ms** (mean `0.410 ms`) |
| **2. Performance** | Decentralized peer & model discovery (`N = 200`, real HTTP + SHA-256) | **p50 1,742.1 µs · p95 3,595.1 µs · p99 4,986.8 µs** (`510.3 ops/s`) |
| **2. Performance** | Stateless scheduling & inference TTFT/exec (`N = 200`, `128` tok/req) | **p50 1,754.1 µs · p95 3,502.0 µs · p99 4,790.4 µs** (`528.3 ops/s`) |
| **2. Performance** | Independent offline receipt verification (`N = 200`, EIP-712 + SHA-256) | **p50 509.5 µs · p95 841.6 µs · p99 1,391.2 µs** (`1,818.4 ops/s`) |
| **2. Performance** | Independent community payment settlement (`N = 200`, real HTTP EIP-712) | **p50 1,020.1 µs · p95 2,554.1 µs · p99 3,064.8 µs** (`894.9 ops/s`) |
| **2. Performance** | Sovereign encrypted data vault recovery (`N = 200`, real HTTP + AES-256-GCM) | **p50 457.6 µs · p95 1,906.7 µs · p99 2,220.3 µs** (`1,908.1 ops/s`) |
| **2. Performance** | **Full post-blackout E2E workflow** (`Discover -> Infer -> Verify -> Settle -> Recover`) | **p50 5.744 ms · p95 8.412 ms · p99 10.894 ms** (`165.4 ops/s`, **`21,172.8 tok/s`**, **100.0%** completion) |
| **3. Trust** | Adversarial & failure scenarios caught fail-closed under declared threat model | **7 / 7 passed (100.0%)** |
| **4. Decentralization** | Critical protocol functions executing with **0 Hanzo calls after Kill Switch** | **8 / 8 (100.0%)**, `HanzoCallsAfterKill = 0` |
| **5. Sovereignty** | User data recovery fidelity, governance ratification, & Operator-C fork hydration | **100.0% fidelity**, `4,109 B` signed fork bundle verified, `CustodialEscrow = false`, `Drift = 0` |

### Trust Adversarial Matrix (7 / 7 Passed = 100.0%)
1. `tampered_model_artifact_sha256_rejected` — single-byte mirror bit-flip rejected fail-closed with `ErrModelDigestMismatch`.
2. `forged_or_tampered_execution_receipt_rejected` — provider output tampering caught offline via deterministic `SHA-256` recomputation (`ErrInvalidReceipt`).
3. `payment_replay_double_spend_rejected` — replayed EIP-712 `PaymentID` rejected by Community Facilitator B with HTTP `409` (`ErrReplayDetected`).
4. `unauthorized_third_party_data_key_rejected` — third-party 32-byte key rejected by AES-256-GCM tag verification (`ErrUnauthorizedDataAccess`).
5. `tampered_encrypted_vault_ciphertext_rejected` — ciphertext bit-flip rejected prior to plaintext release (`ErrUnauthorizedDataAccess`).
6. `tampered_fork_bundle_rejected` — mutated `NetworkForkBundle` rejected offline (`ErrForkBundleCorrupted`, HTTP `422` on `/v1/sovereignty/fork/verify`).
7. `blackout_hanzo_host_leak_blocked` — any attempt to reach `api.hanzo.ai` during blackout blocked at the `RoundTripper` boundary (`ErrHanzoHostBlocked`).

---

## 4. Three Honest Negative & Boundary Regimes (`50` Trials Each)

### Regime 1 — `centralized_hanzo_coupled_negative` (Negative Control)
A client and scheduler hard-coupled to `api.hanzo.ai` and Hanzo's scheduler succeed before the Kill Switch, then collapse to **0.0% completion (`0 / 50`)** after `KillHanzoControlPlane()` with **`50` blocked Hanzo calls**. This validates that the blackout drill enforces genuine protocol-level exit independence rather than passing trivially.

### Regime 2 — `missing_user_key_unrecoverable_boundary` (Non-Custodial Key-Management Trade-Off)
Because `DataCapabilityManifest.CustodialEscrow` is strictly `false` (neither Hanzo nor community operators hold a backdoor copy of the user's AES-256-GCM key), losing the sovereign decryption key results in **0.0% recovery (`0 / 50` recovered; `50 / 50` fail-closed with `ErrUnauthorizedDataAccess`)**. Users requiring social/threshold recovery must layer an explicit client-side Shamir/MPC key backup scheme rather than re-introducing custodial operator escrow.

### Regime 3 — `partitioned_bootstrap_quorum_loss` (Total Bootstrap Partition Boundary)
When every independently administered community bootstrap peer is simultaneously unreachable (`0` reachable peers), `DiscoverPeersFromBootstraps` fails closed **100.0% (`50 / 50` `ErrBootstrapQuorumLost`, `0` Hanzo fallback calls)** rather than silently falling back to an unverified registry or a centralized endpoint. Production deployments should maintain geographically and administratively diverse community bootstrap sets plus cached local peer lists.

---

## 5. Reproduction

```bash
# Run full unit, blackout exit, adversarial, 8-tenant -race concurrency, and zip HTTP tests
go test -race -v -count=1 ./apps/sovereignty/... ./cmd/sovereignty-bench/...

# Verify host-isolation rule and static CGO_ENABLED=0 builds
CGO_ENABLED=0 go test -v ./apps -run TestAppsImportNothingOfTheirHost
CGO_ENABLED=0 go vet ./apps/sovereignty/... ./cmd/sovereignty-bench/...
CGO_ENABLED=0 go build -o /dev/null ./cmd/cloud ./cmd/sovereignty-bench

# Regenerate raw HIP-1351 benchmark JSON
CGO_ENABLED=0 go run ./cmd/sovereignty-bench -iters 200 -rto-drills 20 -regime-trials 50 -out research/hip-1351-sovereignty/results.json
```
