# HIP-1349 Research Dossier: eBPF Runtime Enforcement for Agent Sandboxes (Tetragon)

- **Tracking Issue**: [`hanzoai/cloud#33`](https://github.com/hanzoai/cloud/issues/33) (`p2, bounty, research, hip-1349, test, visor`)
- **Canonical Owning Repository**: `hanzoai/cloud` (`apps/sandboxpolicy`, `cmd/sandboxpolicy-bench`, `apps/apps.go`)
- **Disposition Target**: `MERGE` (bounded clean-room Apache-2.0 implementation, zero new external dependencies, full race/security/regression verification, raw benchmark receipts in [`results.json`](./results.json))

---

## 1. Primary Reference, Architecture Summary & License Audit

### Primary Reference
- **Cilium Tetragon — eBPF-based Security Observability and Runtime Enforcement**
  - Primary repository: <https://github.com/cilium/tetragon>
  - **Core architectural insight**: Post-hoc container log scraping and userspace ptrace monitors detect escapes *after* kernel state has already been mutated and incur high context-switch overhead. Tetragon enforces declarative `TracingPolicy` specifications synchronously at kernel boundaries (syscalls, `execve` / credentials, and socket connect/bind), supporting three graduated enforcement modes (`AuditOnly` telemetry, synchronous `Override`/`EPERM` denial, and synchronous `Sigkill` termination) while emitting structured process-lineage audit events over bounded ring buffers.
  - **Visor Agent Sandbox Adaptation**: Autonomous coding agents running inside Visor sandboxes legitimately execute compilers (`go test`, `go build`), VCS (`git`), package managers (`npm`), ephemeral loopback test servers (`httptest`), and public HTTPS/DNS API calls — while untrusted prompts, dependencies, or tool invocations may attempt container breakouts (`ptrace`, `mount`, `bpf`, `open_by_handle_at`, `setns`, `clone(CLONE_NEWUSER)`), symlink/path-traversal exec escapes, capability/UID privilege escalation, or cloud metadata SSRF (`169.254.169.254`, `::ffff:169.254.169.254`, `fd00:ec2::254`, host loopback Docker/Kubernetes sockets).

### Clean-Room License & Dependency Audit

| Reference / Dependency | Upstream License | Imported Code? | Audit Result |
|---|---|---|---|
| Cilium Tetragon (`github.com/cilium/tetragon`) | Apache-2.0 (userspace) / GPL-2.0-only or Dual BSD/GPL (kernel BPF C) | **None** — 100% clean-room Go design & implementation (zero GPL or Tetragon code copied) | **Clear** |
| Linux seccomp-BPF UAPI constants (`struct sock_filter`, `seccomp_data`) | Public kernel ABI specification | Clean-room Go structs & cBPF VM in `apps/sandboxpolicy/bpf.go` | **Clear** |
| `apps/sandboxpolicy` & `cmd/sandboxpolicy-bench` | Apache-2.0 | Go stdlib + existing `github.com/zap-proto/zip` & `github.com/luxfi/log` | **Zero new `go.mod` / `go.sum` entries** |

No upstream Cilium Tetragon Go or eBPF C code was copied or vendored, avoiding any GPL-2.0 kernel-code contamination. `go.mod` and `go.sum` remain 100% untouched. Published Tetragon benchmarks are **not** claimed; every measurement in Section 6 was reproduced locally by [`cmd/sandboxpolicy-bench`](../../../cmd/sandboxpolicy-bench/main.go).

---

## 2. Reproducible Baseline & Real-Repo Evaluation Corpus

To comply with `PR_CONVERGENCE.md` (public reproducibility on real data rather than synthetic-only claims), [`cmd/sandboxpolicy-bench`](../../../cmd/sandboxpolicy-bench/main.go) anchors its benign agent workload directly to **48 real source files** sampled deterministically from `hanzoai/cloud` (corpus SHA-256 `6845fa7d089690fcba2db6dfe09287f7637395991737f7ed4f303ac8ebc4425e`, anchored to git commit `efb15e233bf21ebb9dc6b898cda61d825bc27016`) and exercises authentic Go (`go test`, `GOTMPDIR=/tmp/go-build` test binaries, `sched_getaffinity`, `faccessat2`), `git status` (`rseq`), `npm test` (`capget`), `rg`, `cat`, public HTTPS/DNS egress, and pre-bind port-0 loopback `httptest` (`127.0.0.1:0` / `[::1]:0`) operations alongside 20 concrete sandbox escape scenarios.

### Exact Reproduction Command
```bash
go run ./cmd/sandboxpolicy-bench \
  -repo . \
  -seed 1349 \
  -out docs/research/hip-1349-tetragon-sandbox/results.json
```

- **Host environment**: `linux/amd64`, 2 vCPUs, `go1.27.1`, `runtime.LockOSThread()` pinned wall-clock timing, deterministic seed `1349`.
- **Baselines evaluated identically**:
  1. Unmonitored no-op dispatch baseline (`0` policy evaluation).
  2. `AuditOnly` vs `EnforceDeny` (`EPERM`) vs `EnforceSigkill` (`SIGKILL`) over the identical 20-case escape regression matrix.
  3. Indexed O(1) syscall / root-bucketed process prefix / bitmask-indexed CIDR engine vs unindexed linear-scan baseline across `10`, `100`, and `1,000` active rules.
  4. Userspace audit-all emission (`Engine.Evaluate` + ring append) vs userspace Go cBPF VM simulation (`EvalBPF`, `AuditAllowEvents=false`) modeling ring-bypassing allowlist filtering under a `50,000 events/sec` benign syscall loop.

---

## 3. Falsifiable Hypothesis, Metrics & Expected Failure Modes

### Hypothesis
> **H1**: A clean-room Tetragon-inspired `TracingPolicy` engine (`apps/sandboxpolicy`) enforcing canonicalized architecture-aware syscall rules (compiled to classic seccomp-BPF bytecode for both `amd64` with x32 rejection and `arm64`), process execution, and network CIDR rules (a) blocks **`100.00%`** (`20/20`) of unauthorized syscall, path-traversal/symlink/privilege-escalation, and IMDS/RFC1918/IPv4-mapped-IPv6 network escape attempts under `EnforceDeny` and `EnforceSigkill` while maintaining **`100.00%`** hash-chained audit visibility and **`100.00%`** seccomp-BPF VM parity, (b) incurs **`0.00%`** (`0/240`) false-positive blocks on real benign coding-agent workloads under `EnforceSigkill` and executes real Linux/amd64 subprocesses (`true`, `git`, `sh`, compiled Go binary) under a kernel-attached `SECCOMP_MODE_FILTER`, (c) maintains constant O(1)/O(k) median evaluation latency (`< 1 µs`) across `10` to `1,000` active rules (beating unindexed linear scanning by `>5×` at `1,000` rules), and (d) avoids ring-buffer telemetry drops (`0.00%` vs `>60%` drops under `50k/s` tight syscall loops) when benign allowlisted syscalls bypass userspace audit emission.

### Falsification Criteria
H1 is falsified if:
1. Any of the 20 escape cases bypasses `EnforceDeny` or `EnforceSigkill`, or any syscall verdict diverges from the compiled seccomp-BPF bytecode executed in `EvalBPF`.
2. Any of the 240 benign agent operations is blocked under `EnforceSigkill` (`>0` false positives), or real Linux subprocesses (`true`, `git`, `sh`, compiled Go binary) fail under kernel-attached `SECCOMP_MODE_FILTER`.
3. Indexed engine median latency fails to stay flat or fails to outperform unindexed scanning by `>2×` at `1,000` active rules.
4. Any cross-tenant policy, sandbox binding, or audit event leaks across org boundaries, or any audit hash-chain fails tamper verification.

### Expected Failure Modes (Explicit Negative Regimes)
1. **Negative Regime 1 — Over-broad / Unindexed Rule Explosion**:
   - *Unindexed scaling*: Naive linear rule scanning degrades linearly (`O(N)`), exploding from `606 ns` at `10` rules to `5,996 ns` p50 (`52,731 ns` p99) at `1,000` rules (`6.81×` slower p50, `8.40×` slower p99 than indexed O(1)/bitmask-CIDR tables).
   - *Over-broad policy authoring*: Naive unanchored regexes (`(?i)(test|status|silent|workspace)`) combined with IPv4-only egress rules produce a **`37.92%`** (`91/240`) false-positive failure rate on legitimate agent builds.
2. **Negative Regime 2 — High-Frequency Tight Syscall Loop / Audit-Ring Backpressure (Userspace VM Simulation)**:
   - Streaming every benign `read`/`newfstatat` call to a userspace hash-chained audit ring at `50,000 events/sec` against a `4,096`-slot ring drained at `12,000 events/sec` triggers **`68.29%` ring drops (`34,144 / 50,000` dropped)** and **`11.98×` worse p99 latency** (`6,743 ns` vs `563 ns` in the userspace Go `EvalBPF` VM simulation). High-frequency benign syscalls **must** resolve via allowlist fast-path (`SECCOMP_RET_ALLOW`) without escalating to userspace audit rings.

---

## 4. Bounded Implementation (`apps/sandboxpolicy`, `apps/apps.go`, `cmd/sandboxpolicy-bench`)

The clean-room implementation lives in [`apps/sandboxpolicy/`](../../../apps/sandboxpolicy/) and mounts declaratively in [`apps/apps.go`](../../../apps/apps.go) (`Wire()`) with **zero host imports** (verified by `apps.TestAppsImportNothingOfTheirHost`):

- [`apps/sandboxpolicy/policy.go`](../../../apps/sandboxpolicy/policy.go): Declarative `TracingPolicy` specification, `EnforcementMode` (`audit_only`, `enforce_deny`, `enforce_sigkill`), architecture-aware `amd64` (212 syscalls) and `arm64` (188 syscalls) lookup/resolution (`LookupSyscallNumberForArch`, `DefaultVisorSandboxPolicyForArch`), 32-bit C `int` argument comparison (`eq32`/`ne32` closing upper-32-bit socket-family bypasses), per-rule deny `Errno` (`ENOSYS` on `clone3` for safe fallback to argument-inspected `clone`), runtime-init syscall coverage (`sched_getaffinity`, `rseq`, `faccessat2`, `capget`, etc.), canonical POSIX path normalization (`NormalizeCanonicalPath`, strict directory-boundary `PathMatchesPrefix`), canonical IP/CIDR normalization (`NormalizeIP`/`NormalizeCIDR` unmapping IPv4-mapped IPv6 addresses), bind-only port-0 loopback ephemeral listener support (`MatchPortZero`), full-field canonical SHA-256 policy digest (`computePolicyDigest`), deterministic priority ordering, and same-priority conflict detection (`ErrPolicyConflict`).
- [`apps/sandboxpolicy/bpf.go`](../../../apps/sandboxpolicy/bpf.go): Real kernel-ready Linux classic BPF (`struct sock_filter`) seccomp-BPF filter compiler (`CompileSeccompBPF`) enforcing `AUDIT_ARCH_X86_64` (plus explicit `nr >= __X32_SYSCALL_BIT` x32 ABI rejection) / `AUDIT_ARCH_AARCH64` in the prologue, 64-bit and 32-bit (`eq32`/`ne32`) argument filter predicates, per-rule `SECCOMP_RET_ERRNO` data (`EPERM`/`ENOSYS`), `BPF_MAXINSNS` (4096) enforcement, deterministic SHA-256 program digest, and a clean-room cBPF VM interpreter (`EvalBPF`) for cross-platform CI parity verification.
- [`apps/sandboxpolicy/audit.go`](../../../apps/sandboxpolicy/audit.go): Strictly per-tenant bounded circular ring buffer (`TenantAuditRing`) with SHA-256 hash-chaining (`"hanzo-sandboxpolicy-audit-v1"`, `VerifyAuditChain`) and configurable `RingOverwriteOldest` / `RingDropNewest` overflow telemetry.
- [`apps/sandboxpolicy/engine.go`](../../../apps/sandboxpolicy/engine.go): Concurrency-safe `Engine` with architecture-aware O(1) syscall maps, O(1) exact-path + root-segment-bucketed process prefix maps, bitmask-indexed IPv4/IPv6 CIDR tables with `MatchPortZero` support, symlink target validation, Linux capability / UID / GID / `setuid` / `no_new_privs` privilege-escalation refusal, and fail-closed defaults on unbound sandboxes or malformed events.
- [`apps/sandboxpolicy/http.go`](../../../apps/sandboxpolicy/http.go) + [`apps/sandboxpolicy/zipdoc_gen.go`](../../../apps/sandboxpolicy/zipdoc_gen.go): 8 typed `zip.App` routes mounted under `/v1/sandboxpolicy` (`GET /health`, `POST /policies`, `GET /policies`, `GET /policies/:id`, `POST /sandboxes/:id/bind`, `POST /evaluate`, `GET /audit`, `GET /metrics`) with zero `http.NewServeMux()`, server-stamped IAM org isolation via `principal.Acting`, `url:"-"` query-hijack protection on every POST body field, and full `zipdoc` OpenAPI/MCP descriptions.
- [`cmd/sandboxpolicy-bench/main.go`](../../../cmd/sandboxpolicy-bench/main.go) + [`cmd/sandboxpolicy-bench/main_test.go`](../../../cmd/sandboxpolicy-bench/main_test.go): Reproducible benchmark & policy-regression harness and automated CI gate.

---

## 5. Correctness, Concurrency, Security & Regression Evidence

### Verification Commands Executed
- `gofmt -l apps/sandboxpolicy cmd/sandboxpolicy-bench apps/apps.go` -> clean (`0` unformatted files).
- `go vet ./...` -> pass (`0` warnings).
- `go test -race -count=1 -v ./apps/sandboxpolicy/... ./cmd/sandboxpolicy-bench/...` -> **all 18 top-level tests (21 test cases including subtests) pass with 0 data races**:
  - `TestSeccompBPFCompilationAndVMParity` (`audit_only`, `enforce_deny`, `enforce_sigkill`): verifies wrong-architecture `SECCOMP_RET_KILL_PROCESS`, all argument operators (including upper 32-bit words), and `100%` parity between `EvalBPF` and `Engine.Evaluate`.
  - `TestPathNormalizationSymlinkAndPrivEscRefusal`: verifies path traversal, NUL byte, relative path, `/usr/bin2` & `/tmp/go-build-evil` boundary spoofing, symlink target escape, and all 6 privilege-escalation vectors (`Setuid`, `NewUID`, `NewGID`, `CapabilitiesAdded`, `EffectiveCaps`, `NoNewPrivs=false`).
  - `TestNetworkCIDRAndIPv4MappedIPv6Normalization`: verifies IPv4/IPv6 IMDS blocking, `::ffff:169.254.169.254` / `::ffff:10.96.0.1` / `::ffff:127.0.0.1:2375` unmapping, reverse-shell refusal, and allowed public HTTPS + ephemeral loopback binds.
  - `TestPolicyConflictAndFailClosedDefaults`: verifies unbound sandbox fail-closed refusal and atomic preservation of v1 policy when conflicting v2 compilation fails.
  - `TestAuditRingBoundedMemoryHashChainAndTamperDetection`: verifies bounded ring eviction, wrapped-window hash-chain validity, cryptographic tamper detection, and `RingDropNewest` backpressure/drain accounting.
  - `TestIndexedEqualsUnindexedEquivalence`: proves indexed lookup equals unindexed linear scan across all 3 domains.
  - `TestStrictTenantIsolationAndConcurrencyRaceFree`: 8 concurrent workers across 4 tenants under `-race` with zero cross-tenant audit leaks.
  - `TestEveryOperationAndFieldIsDescribed`: verifies all 8 `/v1/sandboxpolicy` routes and every published schema field carry non-empty `zipdoc` descriptions.
  - `TestHTTPWireSecurityIsolationAndQueryHijackImmunity`: end-to-end `zip.App` wire tests verifying `401` unauthenticated refusals, `url:"-"` query-hijack immunity on `POST /policies`, `POST /sandboxes/:id/bind`, and `POST /evaluate`, `404` on cross-org policy reads/binds, and `0` cross-org audit leaks.
  - `TestArm64SyscallResolutionAndBPFParity`: verifies arm64 `setns(268)` -> `SIGKILL`, `write(64)` / `read(63)` / `fchmodat(53)` / `sched_getaffinity(123)` / `rseq(293)` -> `ALLOW`, `clone3(435)` -> `DENY(ENOSYS)`, and fail-closed rejection of x86_64-only syscalls or mismatched name/number pairs on `arm64`.
  - `TestSocketFamily32BitWidthRejectsUpperBitBypass`: verifies `eq32` rejects `(1<<32)|AF_NETLINK`, `0xdeadbeef00000000|AF_NETLINK`, and `(1<<32)|AF_PACKET` across both `Engine.Evaluate` and `EvalBPF`.
  - `TestClone3ReturnsENOSYSForSafeFallback`: verifies `clone3` returns `DENY(ENOSYS=38)` in enforce modes and `AUDIT_ALERT` in audit mode with 100% BPF parity.
  - `TestX32SyscallBitRejectedInAmd64Prologue`: verifies `amd64` prologue rejects x32 `mount` (`0x400000a5`) and x32 `read` (`0x40000000`) with `SECCOMP_RET_KILL_PROCESS` even when `DefaultSyscallAllow=true`.
  - `TestRuntimeInitSyscallsAllowlisted`: verifies `sched_getaffinity`, `rseq`, `faccessat2`, `capget`, `ppoll`, and `pselect6` resolve and pass in both `Engine.Evaluate` and `EvalBPF`.
  - `TestLoopbackBindPortZeroAllowedWithoutRelaxingEgress`: verifies `bind` to `127.0.0.1:0` and `[::1]:0` is allowed while `egress` to port 0 and non-loopback `0.0.0.0:0` remain blocked.
  - `TestPolicyDigestCoversAllEnforcementFields`: verifies 17 distinct single-field mutations (network port/direction/protocol/matchPortZero, process arg/lineage predicates, priorities, global defaults, syscall errno/auditOnAllow) each produce unique SHA-256 `CompiledPolicy.Digest` values.
  - `TestKernelAttachedSeccompSubprocessExecution` (`seccomp_linux_test.go`, `linux/amd64`): attaches the compiled `DefaultVisorSandboxPolicy(ModeEnforceSigkill)` filter directly via `PR_SET_NO_NEW_PRIVS` + `SYS_SECCOMP(SECCOMP_SET_MODE_FILTER|TSYNC)` and verifies real Linux execution of `/usr/bin/true`, `/usr/bin/git --version`, `/bin/sh -c "echo seccomp-sh-ok"`, and a freshly compiled Go binary running goroutines + `net.Listen("tcp", "127.0.0.1:0")`, while verifying real kernel `EPERM` on `(1<<32)|AF_NETLINK`, `ENOSYS` on `clone3`, and signal termination on `ptrace`.
  - `TestRunBenchmarksRegressionAndNegativeRegimes`: end-to-end automated gate validating Matrix 1, Matrix 2, Matrix 3, and Negative Regimes 1 & 2.
- `CGO_ENABLED=0 go test ./apps` -> pass (including `TestAppsImportNothingOfTheirHost`).

### Security Review Posture (`PR_CONVERGENCE.md`)
- **Strict Tenant Isolation**: Every policy, sandbox binding, evaluation counter, and SHA-256 hash-chained audit ring is partitioned strictly by server-stamped `principal.Acting(ctx)`. Cross-org policy reads/binds return `404`; unbound sandboxes fail closed (`fail_closed_no_policy`). Public `GET /v1/sandboxpolicy/health` exposes only aggregate numeric counters — zero tenant IDs or rule payloads.
- **Architecture & ABI Hardening**: Syscall names and numbers resolve against the policy's exact target architecture (`amd64` vs `arm64`); `amd64` BPF prologues explicitly reject `nr >= 0x40000000` (x32 ABI); 32-bit C `int` socket family filters use `eq32` to block `(1<<32)|AF_NETLINK` upper-bit bypasses; `clone3` returns `ENOSYS` so runtimes fall back to register-inspected `clone`.
- **Canonicalization Before Matching & Full-Field Policy Digest**: Binary paths and resolved symlink targets are normalized via `NormalizeCanonicalPath` with strict directory-boundary matching; network IPs/CIDRs are unmapped via `netip.Addr.Unmap()`; `PolicySummary.Digest` canonically hashes all global defaults, syscall/process/network predicates, ports, directions, protocols, errnos, and BPF bytecode.
- **Query-Hijack Defense & Memory Bounds**: All POST body fields carry `url:"-"`. Hard caps enforce `MaxPolicyRules (4096)`, `BPFMaxInsns (4096)`, `MaxArgFiltersPerRule (6)`, `DefaultMaxTenants (4096)`, `DefaultMaxPoliciesPerTenant (256)`, `DefaultMaxSandboxesPerTenant (4096)`, and `MaxAuditRingCapacity (65536)`.

---

## 6. Before/After Measurements, Negative Regimes & HIP-1349 Recommendation

All measurements below are verbatim from [`docs/research/hip-1349-tetragon-sandbox/results.json`](./results.json) (`seed=1349`, `go1.27.1`, `linux/amd64`, commit `efb15e233bf21ebb9dc6b898cda61d825bc27016`).

### 6.1 Matrix 1: Boundary-Enforcement Regression (20 Escape Cases: 8 Syscall, 6 Process, 6 Network)

| Enforcement Mode | Blocked | Block Rate | `AUDIT_ALERT` | `DENY` (`EPERM`) | `SIGKILL` | Audit Visibility | Hash-Chain Valid | Seccomp-BPF VM Parity (8/8) |
|---|---:|---:|---:|---:|---:|---:|---|---|
| `audit_only` (baseline) | 0 / 20 | **0.00%** | 20 | 0 | 0 | **100.00%** | **Pass** | **100.00%** |
| `enforce_deny` (`EPERM`) | **20 / 20** | **100.00%** | 0 | 20 | 0 | **100.00%** | **Pass** | **100.00%** |
| `enforce_sigkill` | **20 / 20** | **100.00%** | 0 | 2 | 18 | **100.00%** | **Pass** | **100.00%** |

*(Under `enforce_sigkill`, 18 critical escapes terminate via `SIGKILL` while `socket(AF_PACKET)` and internal RFC1918 `10.96.0.1:443` respect their rule-level `enforce_deny` `EPERM` override — achieving `100.00%` refusal across all 20 escape vectors.)*

### 6.2 Matrix 2: Benign Agent Workload False-Positive Matrix (240 Real Operations under `enforce_sigkill`)

| Domain | Operations Tested | Allowed | False-Positive Blocks | False-Positive Rate | Seccomp-BPF Parity |
|---|---:|---:|---:|---:|---:|
| Syscall (`go`, `git`, `npm`, `sched_getaffinity`, `rseq`, `faccessat2`, `capget`, file I/O, thread `clone`, `AF_INET`) | 120 | 120 | **0** | **0.00%** | **100.00%** |
| Process (`go test`, `git status`, `npm test`, `rg`, `cat`, `GOTMPDIR` test bins) | 72 | 72 | **0** | **0.00%** | — |
| Network (public IPv4/IPv6 HTTPS `:443`, DNS `:53`, pre-bind `127.0.0.1:0` / `[::1]:0`, ephemeral loopback `httptest`) | 48 | 48 | **0** | **0.00%** | — |
| **Total** | **240** | **240** | **0** | **0.00%** | **100.00%** |

### 6.3 Matrix 3 & Negative Regime 1: Rule Scaling (`10` / `100` / `1,000` Rules) & Unindexed Explosion

- **Unmonitored Baseline**: `p50 = 27 ns`, `p95 = 28 ns`, `p99 = 44 ns`, `mean = 27.48 ns`, `11,967,771 events/sec`.

| Active Rules | Compiled BPF Insns | Indexed p50 (ns) | Indexed p99 (ns) | Indexed Throughput (ev/s) | Simulated BPF VM p50 (ns) | Unindexed p50 (ns) | Unindexed p99 (ns) | Unindexed p50 Slowdown |
|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| 10 | 15 | 686 | 10,611 | 242,903 | 57 | 606 | 6,988 | 0.88× |
| 100 | 75 | 749 | 8,658 | 370,982 | 177 | 1,474 | 6,520 | 1.97× |
| **1,000** | **675** | **881** | **6,276** | **364,465** | **1,253** | **5,996** | **52,731** | **6.81×** |

- **Negative Regime 1A Finding (Unindexed Rule Explosion)**: Indexed O(1)/bitmask-CIDR enforcement remains sub-microsecond (`686 -> 749 -> 881 ns` p50, `>360k events/sec`) across a `100×` increase in rule count. Unindexed linear scanning scales linearly to **`5,996 ns` p50 (`6.81×` slower)** and **`52,731 ns` p99 (`8.40×` slower)** at `1,000` rules, slashing throughput `4.62×` (`78,896` vs `364,465 events/sec`).
- **Negative Regime 1B Finding (Over-Broad Policy False-Positive Explosion)**: Adding an unanchored regex rule `(?i)(test|status|silent|workspace)` and dropping IPv6/port-0/ephemeral-loopback network allowances causes **`91 / 240` (`37.92%`) false-positive blocks** on legitimate agent tasks — demonstrating that runtime enforcement requires canonical path prefixes, scoped `GOTMPDIR=/tmp/go-build`, and dual-stack/port-0/ephemeral-loopback allowances rather than ad-hoc regex blocklists.

### 6.4 Negative Regime 2: High-Frequency Tight Syscall Loop (`50,000 events/sec`) & Audit-Ring Backpressure (Userspace Go cBPF VM Simulation)

> **Measurement Boundary Disclosure (`measurementMethod: "userspace_go_cbpf_vm_simulation"`)**: The `EvalBPF` row below times the **userspace Go classic-BPF VM interpreter** simulating ring-bypassing allowlist filtering (`AuditAllowEvents=false`) without attaching a kernel filter or issuing raw `read`/`newfstatat` kernel syscalls. It measures userspace VM evaluation + ring-bypass behavior relative to full userspace `Engine.Evaluate` + audit-ring emission, **not** kernel-attached seccomp syscall latency. (Real kernel-attached execution of the compiled `BPFProgram` on `true`, `git`, `sh`, and a compiled Go binary is separately verified functionally in `TestKernelAttachedSeccompSubprocessExecution`.)

| Configuration (`50,000` benign `read`/`newfstatat`, ring cap `4,096`, drain `12k/s`) | Emitted to Ring | Dropped | Drop Rate | p50 (ns) | p99 (ns) | Mean (ns) | Throughput (ev/s) |
|---|---:|---:|---:|---:|---:|---:|---:|
| **Userspace Audit-All (`Engine.Evaluate`, `AuditAllowEvents=true`)** | 50,000 | **34,144** | **68.29%** | 595 | 6,743 | 2,715.29 | 358,266 |
| **Userspace Go cBPF VM Simulation (`EvalBPF`, `AuditAllowEvents=false`, ring-bypassed)** | **0** | **0** | **0.00%** | **470** | **563** | **689.46** | **1,324,135** |

- **Negative Regime 2 Conclusion (Userspace Simulation)**: Streaming high-frequency benign syscalls (`read`, `newfstatat`) into userspace audit rings loses **`68.29%`** of events under backpressure and inflates simulated p99 latency **`11.98×`** (`6,743 ns` vs `563 ns`) — creating a denial-of-visibility hazard where benign I/O floods evict or drop genuine escape alerts. Resolving benign allowlisted syscalls on an allowlist fast-path without userspace audit-ring emission eliminates ring drops completely (**`0.00%`**) and raises simulated throughput **`3.70×`** (`1.32M` vs `358k events/sec`).

### 6.5 HIP-1349 Integration Recommendation, Rollout & Rollback Plan
- **Canonical Architecture Alignment**: Preserves `vfs -> Hanzo S3 -> Visor, Cloud owns lifecycle and billing, Enso routes models, Zen generates, Kai decides`. `apps/sandboxpolicy` lives in `hanzoai/cloud` as the control-plane policy compiler, seccomp-BPF bytecode emitter, and tenant-isolated hash-chained audit store mounted at `/v1/sandboxpolicy`. Visor sandbox launchers fetch the compiled `BPFProgram` (`GET /v1/sandboxpolicy/policies/:id`) to install `SECCOMP_MODE_FILTER` via `SYS_SECCOMP(SECCOMP_SET_MODE_FILTER, SECCOMP_FILTER_FLAG_TSYNC)` at container spawn (with `GOTMPDIR=/tmp/go-build` and `PR_SET_NO_NEW_PRIVS=1`), and stream process/network/violation hooks to `/v1/sandboxpolicy/evaluate` & `/v1/sandboxpolicy/audit` — or optionally attach Cilium Tetragon `TracingPolicy` CRDs in Kubernetes clusters using the identical three-domain schema.
- **Next-Phase Production Hardening**: Add a dedicated kernel-attached microbenchmark comparing native Linux `read`/`newfstatat` latency with vs. without `SECCOMP_MODE_FILTER` attached on target production kernels, and stream Tetragon eBPF ringbuf violation alerts on Linux 5.15+ nodes into `TenantAuditRing` while keeping benign syscall allowlists strictly in-kernel.
- **Rollout & Rollback**: Mounted declaratively in `apps.Wire()` with zero database migrations, zero changes to existing routes, and zero `go.mod` / `go.sum` edits. Rollout starts per-tenant in `mode=audit_only` to verify `0.00%` false positives on real workloads, graduates to `enforce_deny`, and then `enforce_sigkill`. Rollback is a single-line removal of the `sandboxpolicy` entry in `apps.Wire()` with zero impact on any other subsystem.
