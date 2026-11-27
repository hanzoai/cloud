# On-Chain Agent Identity, Reputation and Delegated Wallets (ERC-8004) — HIP-1350 Reference Implementation & Benchmark (`hanzoai/cloud#18`)

**Issue**: [hanzoai/cloud#18](https://github.com/hanzoai/cloud/issues/18) (`p0`, `hip-1350`, `identity`, `decentralized`)  
**Packages**: [`apps/erc8004`](../../apps/erc8004) (host-independent `zip` app) · [`cmd/erc8004-bench`](../../cmd/erc8004-bench) (end-to-end acceptance & benchmark CLI)  
**Raw Measured Artifact**: [`results.json`](./results.json)  
**License**: `Apache-2.0 OR MIT` (zero new external Go dependencies; `go.mod` / `go.sum` untouched)

---

## 1. Security Invariant & Architecture

> **Core Invariant (`hanzoai/cloud#18`)**: *Agent permissions must be enforced by the actual tools, sandboxes, and payment authorization systems: an NFT or wallet address is not security.*

`apps/erc8004` implements a clean-room, host-independent ERC-8004 stack in pure Go (`CGO_ENABLED=0` compatible, real `secp256k1` recoverable ECDSA + legacy Ethereum `Keccak-256` + EIP-55 checksummed addresses + domain-separated EIP-712 structured digests) that imports only `github.com/zap-proto/zip`, `github.com/luxfi/crypto/secp256k1`, `github.com/luxfi/log`, `golang.org/x/crypto/sha3`, and the Go standard library (`TestAppsImportNothingOfTheirHost` passes):

1. **ERC-8004 Identity Registry (`apps/erc8004/registry.go`)**: Deterministic local EVM-compatible test-chain state model with chained Keccak-256 `StateRoot`, block numbers, and ERC-8004 gas accounting. Agents register portable `AgentIdentity` records (`eip155:<chainId>:<registry>:<tokenId>`) advertising MCP (`mcpEndpoints`) and A2A (`a2aEndpoints`) endpoint descriptors and capabilities.
2. **Offline-Verifiable Signed Credentials (`VerifyCredentialOffline`)**: EIP-712 signed capability and MCP/A2A endpoint attestations verifiable completely offline with zero Hanzo or registry calls.
3. **Bounded Scoped Delegation & Real Runtime Enforcement (`apps/erc8004/enforcer.go`)**:
   - `DelegationGrant` commits cryptographically (owner EIP-712 signature) to `AllowedTools` (deny-by-default exact allowlist), `AllowedResources` (normalized sandbox URI scopes rejecting `..` path traversal), `MaxSpendAtomic` (cumulative cap), `MaxPerCallAtomic` (per-call cap), `Asset`, `ExpiresAt`, `Nonce`, and `RevocationEpoch`.
   - `ToolSandboxEnforcer` verifies agent status, owner delegation signature, delegate request signature, expiration, live O(1) revocation state, replay guard, tool allowlist, and sandbox resource scope **before invoking the underlying tool callback**.
   - `PaymentLimitEnforcer` atomically checks live revocation, replay protection, per-call cap, and cumulative spend cap under lock so concurrent goroutines racing a delegation cap can never overspend.
4. **Instant O(1) Revocation (`RevokeDelegation` & `BumpRevocationEpoch`)**: Owner-signed revocation invalidates a compromised delegate key (or an entire revocation epoch) immediately with zero grace window (`ErrDelegationRevoked` / HTTP `403`).
5. **Paid-Work-Bound Verifiable Reputation (`apps/erc8004/reputation.go`)**: `ReputationRegistry` requires dual-signed `WorkReceipt` records committing to `(PaymentTxHash, AmountAtomic > 0, ToolCallsDigest, TaskOutputHash)` before accepting client-signed ERC-8004 `ReputationFeedback`, rejecting unpaid/forged feedback, duplicate ratings, and direct self-feedback while surfacing Herfindahl-Hirschman Index (`ClientConcentrationHHI`) wash-trading risk.
6. **Sovereign Export & Migration (`WriteSnapshotJSONL` / `VerifySnapshotOffline` / `ImportSnapshot`)**: Full newline-delimited JSON export of identities, credentials, delegations, revocations, receipts, feedback, and tx log with offline cryptographic & chained `StateRoot` verification.

---

## 2. End-to-End Acceptance Flow (`cmd/erc8004-bench`)

Executed over a live mounted `zip.App` (`/v1/erc8004/*`) against the deterministic local test chain (`49.097 ms` end-to-end):

| Step | Action | Observed Result |
| --- | --- | --- |
| 1 | Register ERC-8004 identity + advertise MCP & A2A endpoints + offline credential | `eip155:31337:0x8004000000000000000000000000000000000001:1`, 1 MCP + 1 A2A endpoint, `offlineCredentialVerified = true` |
| 2 | Delegate bounded task + spend limit (`500,000` cumulative / `250,000` per-call atomic USDC) | Delegation `0x382c67...11b0` registered on test chain |
| 3 | Complete paid work within tool/sandbox & payment bounds | Tool `code.audit` executed in `sandbox://tenant-bench/repo/enforcer.go`, payment `150,000` atomic USDC authorized, dual-signed `WorkReceipt` recorded |
| 4 | Submit & offline-verify signed ERC-8004 reputation feedback | Score `98/100` bound to `WorkReceiptHash`, `feedbackOfflineValid = true` |
| 5 | Revoke compromised delegation & verify immediate fail-closed rejection | Post-revoke tool call -> HTTP `403`, post-revoke payment call -> HTTP `403` |

---

## 3. Five HIP-1350 Benchmark Dimensions (Measured, `N = 500` iterations, `CGO_ENABLED=0`)

| Dimension | Metric | Measured Value |
| --- | --- | --- |
| **1. Cost** | On-chain full lifecycle gas (L1 @ 20 gwei, ETH = \$3,000, 10k tokens/task, 1k-task reg amortization) | **\$1,398.41 / 1M useful tokens** (\$13.984 / task) |
| **1. Cost** | On-chain full lifecycle gas (EVM L2 @ 0.05 gwei eff., 10k tokens/task) | **\$3.496 / 1M useful tokens** (\$0.0350 / task) |
| **1. Cost** | **Recommended L2 + off-chain EIP-712 delegation** (0 delegation gas, 10k tokens/task) | **\$2.116 / 1M useful tokens** (\$0.0212 / task) |
| **1. Cost** | Local secp256k1/Keccak verification compute (\$0.04/vCPU-hr) | **\$0.000002 / 1M useful tokens** |
| **2. Performance** | `ToolSandboxEnforcer.Execute` (real ecrecover + scope + live revocation) | **p50 579.6 µs · p95 1,015.5 µs · p99 2,063.3 µs** (`1,587.3 ops/s`) |
| **2. Performance** | `PaymentLimitEnforcer.AuthorizePayment` (real ecrecover + atomic spend cap) | **p50 580.1 µs · p95 716.5 µs · p99 2,039.9 µs** (`1,646.5 ops/s`) |
| **2. Performance** | `VerifyCredentialOffline` (stateless EIP-712 verify) | **p50 255.4 µs · p95 331.4 µs · p99 675.8 µs** (`3,711.7 ops/s`) |
| **2. Performance** | `VerifyFeedbackOffline` (3x EIP-712 verify: worker + client ack + feedback) | **p50 799.4 µs · p95 1,213.2 µs · p99 2,269.9 µs** (`1,184.0 ops/s`) |
| **2. Performance** | Revocation propagation latency (`RevokeDelegation` -> next call rejected) | **883.0 µs** (within-bounds completion rate: **100.0%**) |
| **3. Trust** | Adversarial probe detection across all 9 threat categories | **9 / 9 caught (100.0%)** |
| **4. Decentralization** | Critical functions executing with **0 Hanzo-operated dependencies** | **8 / 8 (100.0%)** |
| **5. Sovereignty** | JSONL export (`7,446 B`) -> offline verify -> independent import -> tamper check | **100.0% migration fidelity**, revocations preserved, tampered export caught |

### Trust Probe Matrix (9/9 Caught = 100.0%)
1. `nft_only_without_scope_bypass` -> `ErrDelegationNotFound`
2. `out_of_scope_tool_call` (`shell.exec`) -> `ErrToolNotAllowed`
3. `sandbox_path_traversal` (`sandbox://tenant-a/docs/../../tenant-b/secret.key`) -> `ErrResourceNotAllowed`
4. `overspend_per_call_cap` (`80,000 > 60,000`) -> `ErrPerCallLimitExceeded`
5. `overspend_cumulative_cap` (`60,000 + 50,000 > 100,000`) -> `ErrSpendLimitExceeded`
6. `replay_attack` (replayed signed payment nonce) -> `ErrReplayDetected`
7. `forged_feedback_without_paid_receipt` -> `ErrWorkReceiptNotFound`
8. `compromised_revoked_delegation` -> `ErrDelegationRevoked`
9. `expired_delegation` -> `ErrDelegationExpired`

---

## 4. Three Honest Negative & Boundary Regimes

### Regime 1 — `nft_ownership_only_negative` (Why NFT/Wallet Ownership Alone Is Not Security)
Running 4 adversarial attempts (out-of-scope `shell.exec`, `..` sandbox traversal, 100x payment overspend, and revoked-key drain) against `NFTOnlyNegativeEnforcer` vs `EnforcementGate`:
- **`NFTOnlyNegativeEnforcer`**: **0 / 4 caught (100.0% privilege & overspend bypass)**.
- **`EnforcementGate` (`ToolSandboxEnforcer` + `PaymentLimitEnforcer`)**: **4 / 4 caught (100.0% detection)**.

### Regime 2 — `stale_cache_revocation_window` (Caching Revocation State vs Live O(1) Checks)
Simulating a compromised delegate issuing 20 requests/sec immediately after `RevokeDelegation`:

| Revocation Cache TTL | Compromised Calls Bypassed | Live Lookup Mean | Cached Lookup Mean |
| ---: | ---: | ---: | ---: |
| **0 ms (Live O(1))** | **0** | 0.047 µs | 0.071 µs |
| 50 ms | **1** | 0.039 µs | 0.051 µs |
| 500 ms | **10** | 0.074 µs | 0.043 µs |
| 5,000 ms | **100** | 0.074 µs | 0.039 µs |

Because live in-process/state-synced O(1) revocation lookup costs ~0.05 µs (four orders of magnitude smaller than a ~580 µs secp256k1 signature recovery), caching revocation checks locally buys negligible latency while opening an avoidable `floor(TTL * rate)` post-compromise attack window.

### Regime 3 — `sybil_self_feedback_wash_trading` (Honest Boundary of Pure Cryptographic Receipts)
- **Unpaid forged feedback**: **100.0% blocked** (`ErrWorkReceiptNotFound`).
- **Direct self-feedback** (agent owner/controller/worker rating themselves): **100.0% blocked** (`ErrSelfFeedbackDetected`).
- **Concentrated sockpuppet wash-trading** (1 fresh wallet pays agent 5 times): passes EIP-712 signature checks, but triggers `ClientConcentrationHHI = 1.0000` and `SybilWashTradeWarning = true`.
- **Distributed fresh-wallet wash-trading** (10 distinct sockpuppets paying 1,000 atomic USDC each): passes EIP-712 signature checks AND lowers `ClientConcentrationHHI = 0.1000` (`SybilWashTradeWarning = false`) at an L2 gas burn of only **\$0.021 / review** (**\$8.40 / review on L1**).
- **Honest architectural conclusion**: Cryptographic receipts establish *provable payment-to-review linkage*, NOT Sybil-proof reputation. Production deployments must layer economic fee burn, bonded slashable stake, or EigenTrust/PageRank client trust-graph weighting on top of ERC-8004 receipts.

---

## 5. Reproduction

```bash
# Run full unit, adversarial, sovereignty, and 16-goroutine -race concurrency tests
go test -race -v -count=1 ./apps/erc8004/... ./cmd/erc8004-bench/...

# Verify host-isolation rule and static CGO_ENABLED=0 builds
CGO_ENABLED=0 go test -v ./apps -run TestAppsImportNothingOfTheirHost
CGO_ENABLED=0 go vet ./apps/erc8004/... ./cmd/erc8004-bench/...
CGO_ENABLED=0 go build -o /dev/null ./cmd/cloud ./cmd/erc8004-bench

# Regenerate raw HIP-1350 benchmark JSON
CGO_ENABLED=0 go run ./cmd/erc8004-bench -iters 500 -out research/hip-1350-erc8004/results.json
```
