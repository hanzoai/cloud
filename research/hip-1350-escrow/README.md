# Compute Escrow, Service Guarantees, Challenges and Rewards — HIP-1350 Reference Implementation & Benchmark (`hanzoai/cloud#17`)

**Issue**: [hanzoai/cloud#17](https://github.com/hanzoai/cloud/issues/17) (`p0`, `hip-1350`, `payments`, `decentralized`)  
**Packages**: [`apps/escrow`](../../apps/escrow) (host-independent `zip` app) · [`cmd/escrow-bench`](../../cmd/escrow-bench) (end-to-end acceptance & 5-dimension benchmark CLI)  
**Raw Measured Artifact**: [`results.json`](./results.json)  
**License**: `Apache-2.0 OR MIT` (zero new external Go dependencies; `go.mod` / `go.sum` untouched)

---

## 1. Protocol Invariants & Architecture

> **Core Invariants (`hanzoai/cloud#17`)**:
> 1. *Proof of service quality (SLA/latency/availability) and proof of correct computation (objective output commitment / deterministic verification / cryptographic equivocation) are strictly separate.*
> 2. *Slashing applies ONLY to objectively defined, independently challengeable protocol violations — funds MUST NEVER be released twice or seized on a merely subjective claim.*

`apps/escrow` implements a clean-room, host-independent compute escrow and dispute engine in pure Go (`CGO_ENABLED=0` compatible, real `secp256k1` recoverable ECDSA with EIP-2 low-S enforcement, legacy Ethereum `Keccak-256`, EIP-55 checksummed addresses, and domain-separated EIP-712 typed data digests). It imports only `github.com/zap-proto/zip`, `github.com/luxfi/crypto/secp256k1`, `github.com/luxfi/log`, `golang.org/x/crypto/sha3`, and the Go standard library (`TestAppsImportNothingOfTheirHost` passes):

1. **Deterministic State Machine (`apps/escrow/engine.go`)**:
   - Lifecycle: `Quoted` -> `Funded` (buyer locks `JobPriceAtomic` + provider locks `ProviderBondAtomic`, both cryptographically authorized via EIP-712 signatures) -> `Claimed` (provider submits EIP-712 signed `CompletionClaim` committing to `OutputDigest`, `ExecutionProof`, `CompletedAt`, and `ChallengeDeadline`) -> `Challenged` (challenger locks `ChallengeBondAtomic` + objective evidence) -> absorbing terminal states `Settled` / `Slashed` / `Refunded`.
   - **Double-Payment & Double-Release Impossibility**: Terminal states are strictly absorbing (`Finalized == true`, `PayoutCount == 1`). Concurrent goroutines racing `Finalize`, `RefundMissedDeadline`, and `ResolveChallenge` are serialized atomically so exactly one payout executes and all other callers receive `ErrAlreadyFinalized`.
   - **Conservation of Funds (`VerifyConservation()`)**: Across every transition and concurrent race, `sum(balances) + sum(locked_escrows) == initialMintedSupply` holds to the exact atomic unit (`Drift == 0`, `DoublePayoutCount == 0`, `SubjectiveSlashes == 0`).
2. **Strict Separation of Proofs & Objective-Only Slashing (`apps/escrow/verifier.go`)**:
   - `ServiceGuaranteeVerifier` (`ProofDomainServiceQuality`): Evaluates only objective timestamp/liveness bounds (`ViolationMissedDeadline` — worker crash or late completion). Never inspects semantic quality.
   - `ComputationVerifier` (`ProofDomainComputationIntegrity`): Independently adjudicates `ViolationFalseCompletionClaim` via deterministic kernel recomputation (`SpecKeccak256Eval`, `SpecHashChain64`, `SpecSeededTokenTransform`) and `ViolationInvalidSignatureOrEquivocation` via quote-binding checks or two conflicting provider-signed claims for the same `JobID`.
   - **Subjective Claim Hard-Rejection**: `ViolationSubjectiveQuality` (and any unknown violation type) is fail-closed rejected by `ClassifyViolation` with `ErrSubjectiveSlashingForbidden` (HTTP `422`) before locking any bond or mutating state.
   - **Malicious Challenger Forfeiture**: When an objective challenge is opened against a valid, honest fulfillment and independent verification confirms the provider's output, 100% of `ChallengeBondAtomic` is forfeited to the honest provider alongside `JobPriceAtomic + ProviderBondAtomic`.
3. **Standalone `zip` HTTP Surface & Sovereign JSONL Audit Ledger (`apps/escrow/service.go`, `apps/escrow/ledger.go`)**:
   - Mounts `/v1/escrow/*` onto any `zip.App` with zero host imports.
   - Exports a hash-chained JSONL settlement ledger (`ExportLedgerJSONL` / `VerifyLedgerJSONLOffline`) and full portable engine snapshots (`ExportSnapshotJSONL` / `ImportSnapshotJSONL`) with offline EIP-712 signature, hash-chain, tamper, and conservation verification.

---

## 2. End-to-End Acceptance Flow (`cmd/escrow-bench`)

Executed over a live mounted `zip.App` (`/v1/escrow/*`) in `48.602 ms`:

| Step | Action | Observed Result |
| --- | --- | --- |
| 1 | Provider signs `JobQuote` + Buyer signs `QuoteAcceptance` & locks funds (`POST /v1/escrow/quotes`, `/fund`) | `job-e2e-17` locked (`250,000` buyer + `100,000` provider bond) |
| 2 | Provider executes `deterministic/hashchain64-v1` & submits signed `CompletionClaim` (`POST /v1/escrow/claim`) | `StateClaimed`, `ServiceGuaranteeProof.OnTime = true`, `ComputationProof` recorded |
| 3 | Challenger submits subjective quality dispute (`ViolationSubjectiveQuality`) (`POST /v1/escrow/challenge`) | **HTTP `422`** (`ErrSubjectiveSlashingForbidden`), zero funds seized |
| 4 | Challenge window elapses -> permissionless optimistic release (`POST /v1/escrow/finalize`) | `StateSettled`, provider final balance `750,000` (`500,000 + 250,000`) |
| 5 | Conservation audit & offline JSONL ledger verification (`GET /v1/escrow/ledger`) | `ConservationDrift = 0`, `7` ledger entries verified, `LedgerChainValid = true` |

---

## 3. Five HIP-1350 Benchmark Dimensions (Measured, `N = 500`, `CGO_ENABLED=0`)

| Dimension | Metric | Measured Value |
| --- | --- | --- |
| **1. Cost** | EVM L1 optimistic path (258k gas @ 20 gwei, ETH = \$3,000, 10k tok/job) | **\$1,548.00 / 1M useful tokens** (\$15.48 / job) |
| **1. Cost** | EVM L1 always-challenged path (466k gas @ 20 gwei, 10k tok/job) | **\$2,796.00 / 1M useful tokens** (\$27.96 / job) |
| **1. Cost** | EVM L2 optimistic path (258k gas @ 0.05 gwei eff., 10k tok/job) | **\$3.870 / 1M useful tokens** (\$0.0387 / job) |
| **1. Cost** | EVM L2 always-challenged path (466k gas @ 0.05 gwei eff., 10k tok/job) | **\$6.990 / 1M useful tokens** (\$0.0699 / job) |
| **1. Cost** | **Recommended L2 optimistic + off-chain EIP-712 claim** (190k gas @ 0.05 gwei) | **\$2.850 / 1M useful tokens** (\$0.0285 / job) |
| **1. Cost** | Local secp256k1/Keccak state-machine compute (\$0.04/vCPU-hr) | **\$0.000001 / 1M useful tokens** |
| **2. Performance** | `CreateQuote` (EIP-712 quote verify) | **p50 294.2 µs · p95 504.2 µs · p99 1,410.6 µs** (`3,018.0 ops/s`) |
| **2. Performance** | `FundEscrow` (buyer EIP-712 verify + dual atomic lock) | **p50 272.5 µs · p95 426.8 µs · p99 1,193.9 µs** (`3,387.1 ops/s`) |
| **2. Performance** | `SubmitClaim` (provider EIP-712 verify + SLA deadline check) | **p50 277.1 µs · p95 605.4 µs · p99 1,451.3 µs** (`3,013.3 ops/s`) |
| **2. Performance** | `Finalize` (window check + atomic release) | **p50 10.3 µs · p95 25.8 µs · p99 73.0 µs** (`81,825.9 ops/s`) |
| **2. Performance** | `OpenAndResolveChallenge` (EIP-712 + 64-round hash-chain verify + slash) | **p50 352.0 µs · p95 472.8 µs · p99 1,544.9 µs** (`2,683.1 ops/s`) |
| **2. Performance** | Full optimistic lifecycle (`Quote -> Fund -> Claim -> Finalize`) | **p50 874.4 µs · p95 1,764.8 µs · p99 2,481.6 µs** (`1,029.1 ops/s`, **100.0%** completion) |
| **3. Trust** | Adversarial & failure scenarios handled (0 double-spends, 0 subjective slashes, 0 drift) | **7 / 7 passed (100.0%)** |
| **4. Decentralization** | Critical protocol functions executing with **0 Hanzo-operated dependencies** | **8 / 8 (100.0%)** |
| **5. Sovereignty** | JSONL ledger (`2,620 B`) + snapshot (`6,448 B`) offline audit, migration & tamper check | **100.0% fidelity**, **0 drift**, tampered ledger & snapshot caught |

### Trust Scenario Matrix (7 / 7 Passed = 100.0%)
1. `honest_fulfillment_optimistic` — optimistic single release of payment + provider bond after challenge window expires.
2. `missed_deadline_worker_crash_recovery` — worker crashes mid-job; buyer reclaims 100% of `JobPriceAtomic` + deterministic `SLAPenaltyAtomic` from `ProviderBondAtomic`.
3. `false_completion_claim_slashed` — bogus `OutputDigest` caught by independent deterministic recomputation; 100% of provider bond slashed, buyer refunded, challenger rewarded.
4. `equivocation_double_output_slashed` — provider signing two conflicting `OutputDigest`s for the same `JobID` cryptographically slashed without trusting an oracle.
5. `malicious_challenger_bond_forfeited` — false fraud challenge against honest completion fails verification; 100% of challenger bond forfeited to honest provider.
6. `subjective_quality_claim_rejected` — `ViolationSubjectiveQuality` fail-closed rejected with `ErrSubjectiveSlashingForbidden`; zero funds seized.
7. `double_payment_concurrent_16_goroutine_race` — 16 goroutines simultaneously racing `Finalize`, `RefundMissedDeadline`, and `ResolveChallenge`; exactly 1 payout, `0` double-payments, `0` drift.

---

## 4. Three Honest Negative & Boundary Regimes

### Regime 1 — `zero_bond_griefing_negative` (Why Non-Zero Challenger Bonds Are Mandatory)
Simulating `N = 50` honest jobs attacked by a zero-balance griefer when `ChallengeBondAtomic == 0` vs `DefaultPolicy` (`ChallengeBondAtomic = 50,000`):
- **Zero-bond ablation (`RequireChallengeBond = false`)**: Attacker successfully forces **100.0%** of honest jobs into dispute adjudication with **`0` economic loss**, inflating settlement CPU latency from **19.06 µs** (optimistic finalize) to **417.66 µs** (**+2,091.3%** dispute processing overhead) and delaying every honest provider's payout.
- **Production `DefaultPolicy` (`RequireChallengeBond = true`)**: Rejects zero-bond quotes (`ErrChallengeBondRequired`), and penalizes the same 50 false challenges by forfeiting **2,500,000 atomic units** directly to the honest provider.

### Regime 2 — `unverified_instant_release_negative` (Why Challenge Windows Cannot Be Skipped)
Simulating `N = 50` dishonest providers submitting bogus `OutputDigest` claims under `ChallengeWindowSec == 0` vs optimistic `ChallengeWindowSec == 30`:
- **Instant release (`AllowInstantRelease = true`, `ChallengeWindowSec = 0`)**: **100.0%** of fraudulent claims steal buyer funds (**0.0%** fraud caught; **5,000,000 atomic units** stolen from buyers).
- **Optimistic challenge window (`ChallengeWindowSec = 30`)**: **100.0%** of false completion claims caught and slashed by independent challengers (**0 atomic units** lost by buyers).

### Regime 3 — `non_deterministic_llm_sampling_boundary` (Honest Boundary of Bitwise Output Equality)
Simulating `N = 50` stochastic `temperature > 0` floating-point LLM inference jobs where two honest executions of the same prompt naturally sample distinct token sequences (`100.0%` distinct output hashes):
- **Naive bitwise equality slashing**: Would suffer a **100.0% false-slash rate** against honest providers.
- **`EscrowEngine` boundary enforcement**: Refuses raw bitwise output-equality challenges on `ClassNonDeterministicSampling` jobs (**100.0%** rejected cleanly with `ErrNonDeterministicVerificationNeeded`, **0.0%** false slashes), while still objectively slashing **SLA/deadline failures**, **invalid signatures**, and **cryptographic double-output equivocation** (`equivocationStillSlashable = true`).
- **Architectural conclusion**: Objective computation slashing for LLM inference requires either deterministic seeds with canonical decoding, verifiable sampling/logprob commitments, or cryptographic proofs — never raw bitwise equality over stochastic floating-point runs, and never subjective quality judgments.

---

## 5. Reproduction

```bash
# Run full unit, adversarial, sovereignty, and 16-goroutine -race concurrency tests
go test -race -v -count=1 ./apps/escrow/... ./cmd/escrow-bench/...

# Verify host-isolation rule and static CGO_ENABLED=0 builds
CGO_ENABLED=0 go test -v ./apps -run TestAppsImportNothingOfTheirHost
CGO_ENABLED=0 go vet ./apps/escrow/... ./cmd/escrow-bench/...
CGO_ENABLED=0 go build -o /dev/null ./cmd/cloud ./cmd/escrow-bench

# Regenerate raw HIP-1350 benchmark JSON
CGO_ENABLED=0 go run ./cmd/escrow-bench -iters 500 -out research/hip-1350-escrow/results.json
```
