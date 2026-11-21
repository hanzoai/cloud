# eBPF Runtime Enforcement for Agent Sandboxes — Reproducible Experiment (cloud#33)

**Bounty**: [hanzoai/cloud#33](https://github.com/hanzoai/cloud/issues/33) — Research Bounty (help wanted / visor / hip-1349 / p2)
**HIP**: HIP-1349 (draft, *eBPF Runtime Enforcement for Agent Sandboxes*); sandbox runtime context: HIP-1343 (Visor)
**Primary code**: <https://github.com/cilium/tetragon> (Cilium Tetragon — "eBPF-based Security Observability and Runtime Enforcement")
**Primary references**: [tetragon.io/docs](https://tetragon.io/docs/) (concepts/tracing-policy, tracing-policy/mode, getting-started/enforcement); upstream policy examples at `examples/quickstart/*_enforce.yaml` and `examples/tracingpolicy/*`

This directory is a **dependency-free Go harness** (escape-attempt matrix runner + syscall-overhead microbenchmark + result model) and the **five sandbox TracingPolicies** it validates. The kernel side is stock Tetragon; nothing here reimplements eBPF.

> **Research-only**: the committed TracingPolicies and the `/etc/shadow` / egress probe in the harness are a study scaffold — they must not be applied to production as-is (see the per-file banners and PROTOCOL.md §6 FP audit).

> **Authoring-environment honesty**: the research was authored on Windows 11 with no Linux distribution available (WSL2 present, distro not installed). **Raw kernel numbers are not committed with this PR.** What is committed: the runnable harness, five grounded policies, the exact measurement protocol (`results/PROTOCOL.md`, <30 min on any Linux ≥ 5.10 host with BTF), the raw-results schema, and a test suite that compiles and runs on every OS. A valid negative result completes the bounty contract; the harness makes the first real run a two-command affair.

---

## 1. Code & License Audit

| Item | Finding |
| --- | --- |
| Project | Cilium Tetragon — kernel-side eBPF sensors loaded from `TracingPolicy`/`TracingPolicyNamespaced` CRs; user-side `tetragon` agent + `tetra` gRPC CLI; events stream over gRPC/JSON |
| License | **Apache-2.0** (repo `LICENSE`; Apache-2.0 is on the bounty's preferred list) |
| Patent notes | Apache-2.0 includes the explicit contributor patent grant; no additional patent notices found in the repository |
| Code reuse | **No Tetragon Go code reused.** The harness is original code. The five policy YAMLs are *derived from upstream Apache-2.0 examples* (`sys_setuid.yaml`, `modules-nohost.yaml`, `quickstart/file_monitoring_enforce.yaml`, `quickstart/network_egress_cluster_enforce.yaml`) with per-file derivation headers — config, not code |
| Compatibility | hanzoai/cloud is dual-licensed Apache-2.0 OR MIT; this package carries `SPDX-License-Identifier: Apache-2.0 OR MIT` and is standard-library-only (no `go.mod` changes) |

What Tetragon contributes (from the docs and examples): in-kernel filtering (selectors evaluate in the kernel — non-matching events cost nothing), LSM/kprobe/tracepoint hook points, four enforcement actions of which this study uses **Sigkill** (child dies with SIGKILL → exit 137 observed through a shell) and **Override** (in-kernel argument override, e.g. `argError: -1` makes the syscall fail with EPERM before userspace resumes), and policy modes `monitoring` / `enforcement` (set per policy via `spec.options`, at load via `tetra tracingpolicy add --mode`, or at runtime via `tetra tp set-mode`).

## 2. Tetragon ↔ Visor (HIP-1343) Architecture Mapping

Canonical stack stays fixed: `vfs → hanzoai/s3 → visor`. Visor is the sandbox runtime; Tetragon is the *kernel enforcer* beneath it.

| Visor need | Tetragon primitive | How the harness uses it |
| --- | --- | --- |
| Agent processes must not become root | `sys_setuid` kprobe + Sigkill | `policies/sandbox-privilege-escalation.yaml` |
| Namespace surgery (nsenter) is admin-only | `sys_setns` kprobe + Sigkill | `policies/sandbox-namespace-escape.yaml` |
| No kernel-module loading from sandboxes | `security_kernel_read_file`/`load_data`/`module_request` LSM kprobes + Override+Sigkill | `policies/sandbox-kernel-modules.yaml` |
| Egress limited to the sandbox gateway | `tcp_connect` kprobe + `NotDAddr` allowlist + Sigkill | `policies/sandbox-egress-enforce.yaml` |
| Host credential material off-limits | `security_file_permission` kprobe + Prefix match + Sigkill | `policies/sandbox-sensitive-files.yaml` |
| Deployment outside Kubernetes | `tetragon --tracing-policy[-dir]` startup flags + `tetra` CLI (no CRDs required) | `results/PROTOCOL.md` §2 |
| Rollout safety | per-policy modes: monitor first, enforcement after FP audit | harness gate: `leaked==0 && false_positives==0` |
| Provenance (HIP §1.3) | enforcement events keyed by `(binary, pid-ns, action)` — map to `gen_ai.hanzo.runtime_digest` | §8 integration |

Scoping decision (false-positive control at the policy level): all five policies limit their kill scope to processes **outside the host PID namespace** (`matchNamespaces: Pid NotIn host_ns` — the pattern upstream itself uses in `modules-nohost.yaml`). Sandbox/agent processes run in their own PID namespace; host admin tools (`su`, `sudo`, `modprobe`, `nsenter`) are untouched. This is what makes enforcement deployable on a shared host without killing the host.

## 3. Falsifiable Hypotheses (stated before any run)

**H1 — enforcement overhead is small on syscall-heavy paths.** With the five sandbox policies loaded in enforcement mode on a BTF-equipped Linux ≥ 5.10 host, throughput of an 8-worker open+close microbenchmark drops by **≤ 2%** and loopback connect+close by **≤ 3%** versus agent-in-monitor (event-only) mode. Expected failure modes: hosts without BTF (cannot attach → skipped, not a failure), kernels with expensive kprobe attachment, and event storms if policies are broadened beyond the in-kernel selectors (that is why the policies filter in-kernel).

**H2 — the escape matrix is fully blocked under enforcement.** All five escape probes are killed with SIGKILL in enforcement mode (`leaked == 0`). Falsified if any single escape completes: that probe's policy is inadequate and the result is a documented negative finding for HIP-1349 (the harness exits 1 — CI-ready).

**H3 — the benign control is never blocked (zero false positives).** A process doing temp-file write/read plus a loopback dial completes under enforcement across the full audit (`false_positives == 0`). Falsified if the control or any benign audit workload (compiling Go, running the test suite) is killed: the policy set is too broad for a sandbox host and must not be promoted.

**H4 — monitor mode is observation-only.** In monitor mode all five escapes complete but appear in the agent event stream; zero kills occur. Falsified if anything is killed in monitor mode (policy misconfigured into enforcement).

Expected failure modes declared up front: H1's 2–3% thresholds are host-dependent (slow kprobe-heavy hosts may exceed — measured values are recorded either way); H2's five classes do not exhaust attacker technique space (§7 limitation); H3's audit is a bounded 5-minute sample, not proof of global false-negative freedom.

## 4. Methodology

**Escape matrix** (5 classes × 3 modes + benign control): each probe is executed from a fresh child process (`exec.Command` of the harness binary itself — no external tools, no shell), with a per-probe timeout. Outcomes classify as `blocked` (died from signal — enforcement), `completed` (ran to finish), `timeout`, `error`, `skipped` (payload needs another OS). Classification is mode-aware: a completed escape is a *leak* only in enforcement mode; in monitor mode it is the expected *observation*.

**Overhead microbench** (two workloads × 3 modes): `open-close` (open+close pairs on a temp file — the file-monitoring hook path) and `loopback-connect` (TCP connect+close to a local listener — the egress hook path). 8 workers, 30 s per pass, atomically counted ops normalized by measured wall time. Overhead % = (baseline−enforced)/baseline×100; a negative raw delta is kept and flagged `noise` rather than clamped (§3 H1).

**False-positive audit**: enforcement-mode pass must show zero kills for the benign control and for a 5-minute realistic benign workload (Go compile + test suite); `results/PROTOCOL.md` §6.

**Determinism/reproducibility**: every raw result records host (os/arch/kernel/BTF/tetragon version), UTC start, mode, per-probe exit code/signal/duration/output tails, and validates against `results/schema.json`. The matrix and bench are executable by anyone with `go build`; the result model round-trips byte-stable JSON.

## 5. Raw Results

**None committed with this PR** — no Linux host was available in the authoring environment (documented limitation, not a silent gap). `results/schema.json` and `results/PROTOCOL.md` specify exactly what the first run must produce and commit:

- `run-{none,monitor,enforcement}-<date>-<host>.json` → `schema#/definitions/run_result`
- `bench-report-<date>-<host>.json` → `schema#/definitions/bench_reports`

Gate for the harness exit code (CI-ready): `leaked == 0 && false_positives == 0 && errors == 0`; skipped probes (off-Linux) do not fail the gate.

## 6. Harness Evidence (what is testable without eBPF)

17 tests in `research/tetragonenforcement/enforcement_test.go`, all green on the authoring host (and designed to stay green on Linux CI):

- **Structure**: matrix shape (5 escape classes + 1 control, unique IDs, every escape bound to a committed policy file); `EscapeProbes` count.
- **Policy regression** (the issue's "policy-regression tests"): each committed policy must keep its kernel hook anchor (`sys_setuid`, `sys_setns`, `security_kernel_read_file`, `tcp_connect`, `security_file_permission`) and a `Sigkill` action — if Tetragon's policy language changes and a policy stops matching, this fails before any Linux run does.
- **Execution paths**: benign control completes cross-OS; per-probe deadline classifies `timeout`; Linux-only payloads report honest `skipped` off-Linux.
- **Mode-aware classification**: enforcement (completed escape = leak), monitor (completed escape = observed), none (any kill = false positive).
- **Overhead math**: percentage table incl. zero/negative baseline errors; report merging with missing-sample omission and raw-noise retention (`noise` flag, no clamping).
- **Results format**: `RunResult` JSON round-trip; `results/schema.json` parses with required definitions.
- **Bench smoke**: both workloads produce positive throughput in a 400 ms run.

Also verified: `gofmt`, `go vet ./...` clean for both windows/amd64 and (cross-compiled) linux/amd64 targets.

## 7. Limitations

1. **No raw kernel numbers in this PR.** The authoring host is Windows with no Linux distro; eBPF numbers require Linux ≥ 5.10 with BTF. The harness + protocol are the deliverable; the first run on a Linux host completes the measurement contract.
2. **Single-node scope.** No Kubernetes identity-aware selectors (pod labels, namespaces as identity) — the non-K8s model scopes by PID namespace. Multi-tenant identity enforcement is future work (likely a k8s-mode follow-up).
3. **Five escape classes, not exhaustive.** ptrace injection, `io_uring` abuse, `bpf()` syscall misuse, userfaultfd, and fs.verity/cap-environ attacks are not covered; the matrix is extensible by construction (add a `Probe` + payload + policy, tests enforce shape).
4. **SIGKILL-based containment is coarse.** Enforcement kills the offending process; state it already mutated is not rolled back (the `Override` action gives in-kernel EPERM for module loads, which is stronger). Sandbox supervisors must treat a killed agent as containment, not cleanup.
5. **Loopback-only egress allowlist.** Production sandbox egress will need the gateway CIDR(s) added to `NotDAddr` values (documented placeholder in the policy).
6. **Mode-control CLI surface may evolve** (`tetra` subcommand flags per upstream docs); the protocol references the documented commands as of v1.8.0 docs.

## 8. Integration Recommendation (Visor / HIP-1343)

1. **Deploy shape**: run the `tetragon` agent per sandbox host (non-K8s startup flags, `--tracing-policy-dir` mounted read-only with the five committed policies), coexisting with the Visor runtime — not a parallel serving path; Cloud owns lifecycle & billing, Enso routes models, Zen generates, Kai decides.
2. **Rollout ladder**: monitor mode → false-positive audit (harness gate + 5-minute benign audit) → enforcement for `privilege-escalation` and `kernel-modules` (lowest FP risk, highest blast radius) → enforcement for `sensitive-files` and `egress` after allowlist review.
3. **Provenance**: map Tetragon events to `gen_ai.hanzo.runtime_digest` fields — `(sandbox_id, policy_name, action, binary, pid_ns)` — so enforcement decisions are auditable in the existing provenance stream (HIP §1.3).
4. **Supervisor contract**: a sandboxed agent that dies with SIGKILL is *contained*; the Visor supervisor restarts it and records the event; repeated kills trip the sandbox's kill-switch (operator review).
5. **CI**: `tetragonharness escape -mode enforcement` is a merge gate (exit 0 ⇔ no leaks, no false positives); the escape matrix doubles as the sandbox regression suite for future policy changes.

## 9. HIP-1349 Status Update (draft)

- **Research deliverable** (this PR): runnable enforcement harness + five grounded sandbox policies + 17 tests + raw-results schema + <30-minute measurement protocol; hypotheses H1–H4 predeclared with expected failure modes.
- **Pending**: first Linux run produces §5 raw results and §3 verdicts; a negative verdict on any hypothesis is a valid, publishable completion per the issue contract.
- **Follow-up implementation bounty** (if maintainers open one): ship the five policies + harness gate into Visor's default sandbox profile and wire enforcement events into `runtime_digest`.

## 10. File Map

| File | Purpose |
| --- | --- |
| `enforcement.go` | Probe matrix, runner, mode-aware classification, results model |
| `bench.go` | open-close / loopback-connect microbenchmarks, overhead math |
| `payload.go` + `payload_linux.go` + `payload_other.go` | Child payloads (setuid/setns/module/egress/shadow/benign) |
| `signal_linux.go` + `signal_other.go` | Signal-death detection + kernel/BTF host info |
| `enforcement_test.go` | 17 tests (structure, policy regression, paths, math, schema) |
| `policies/*.yaml` | Five sandbox TracingPolicies (derivation-documented) |
| `results/schema.json` | JSON Schema for committed raw results |
| `results/PROTOCOL.md` | Exact <30 min protocol for a Linux host |
| `../tetragonharness/main.go` | CLI: `escape | bench | combine | payload | list` |
