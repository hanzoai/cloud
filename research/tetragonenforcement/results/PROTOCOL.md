# Measurement Protocol — cloud#33 (Tetragon enforcement)

Authoring environment note: the research was authored on Windows 11 with no
Linux distribution available (WSL2 installed, distro not). **No raw numbers
are committed with this PR.** This protocol turns any Linux host with BTF
into a full result set in under 30 minutes; commit the outputs under
`results/` named `run-<date>-<host>.json` / `bench-<date>-<host>.json`.

## 0. Prerequisites

- Linux ≥ 5.10 with BTF: `test -f /sys/kernel/btf/vmlinux && echo BTF-OK`
- Docker (for the agent) or a native Tetragon install
- Go ≥ 1.21
- Root (escape payloads deliberately attempt privileged syscalls)

## 1. Build the harness

```
go build -o bin/tetragonharness ./research/tetragonharness
./bin/tetragonharness list        # show the probe matrix
```

## 2. Start Tetragon (agent, non-Kubernetes)

Docker deployment (per upstream docs `docs/getting-started/install-docker`):

```
docker run -d --name tetragon \
  --pid=host --cgroupns=host --privileged \
  -v /sys/kernel/btf/vmlinux:/sys/kernel/btf/vmlinux:ro \
  -v /sys/fs/cgroup:/host/sys/fs/cgroup:ro \
  -v "$PWD/research/tetragonenforcement/policies:/etc/tetragon/tetragon.tp.d" \
  quay.io/cilium/tetragon:v1.8.0
```

Policy modes (per upstream docs `docs/concepts/tracing-policy/mode`):
policies load in `monitoring` mode by default; switch to enforcement with
`spec.options: {policy-mode: enforcement}` in the YAML, at load time
(`tetra tracingpolicy add --mode enforcement policy.yaml`), or at runtime
(`tetra tp set-mode`). The CLI inside the container is `tetra`.

Verify the agent sees the five policies:

```
docker exec tetragon tetra tracingpolicy list
```

## 3. Baseline (no agent influence) — mode `none`

Stop the agent, then:

```
./bin/tetragonharness bench -workload open-close -mode none -secs 30 -workers 8 -out bench-none-open-close.json
./bin/tetragonharness bench -workload loopback-connect -mode none -secs 30 -workers 8 -out bench-none-loopback.json
./bin/tetragonharness escape -mode none -out run-none.json
```

Expected `run-none.json`: all escapes `completed` (leaks by definition are
only meaningful when enforcement is active — see README §3 H2), benign
control `completed`.

## 4. Monitor pass (event-only) — mode `monitor`

Start the agent with the policies in monitoring mode. Then:

```
./bin/tetragonharness bench -workload open-close -mode monitor -secs 30 -workers 8 -out bench-monitor-open-close.json
./bin/tetragonharness bench -workload loopback-connect -mode monitor -secs 30 -workers 8 -out bench-monitor-loopback.json
./bin/tetragonharness escape -mode monitor -tetragon-version v1.8.0 -out run-monitor.json
```

Expected `run-monitor.json`: escapes `completed` (observed, not killed) and
recorded in `tetra events`; benign control `completed`; **no** kills at all
(any kill here is a false positive → gate fails).

## 5. Enforcement pass — mode `enforcement`

Switch all policies to enforcement (`tetra tp set-mode enforcement ...` or
restart with enforcement YAMLs). Then:

```
./bin/tetragonharness bench -workload open-close -mode enforcement -secs 30 -workers 8 -out bench-enf-open-close.json
./bin/tetragonharness bench -workload loopback-connect -mode enforcement -secs 30 -workers 8 -out bench-enf-loopback.json
./bin/tetragonharness escape -mode enforcement -tetragon-version v1.8.0 -out run-enforcement.json
```

Expected `run-enforcement.json`: five escapes `blocked` with `SIGKILL`
(child exit convention 137 when observed through a shell), benign control
`completed`, `leaked=0`, `false_positives=0`. The harness exits 1 on any
leak, false positive or probe error — wire it straight into CI.

## 6. False-positive audit (required)

Under enforcement, run a *benign but realistic* workload for 5 minutes —
e.g. the harness test suite itself:

```
go test ./research/...              # compiles, spawns processes, dials loopback
./bin/tetragonharness escape -probe benign-control -mode enforcement
```

Then inspect agent events for kills of benign binaries (the `tetra events`
stream shows SIGKILL actions). Expect **zero** kills outside the five
probe payload processes. Record the count and any event in the results
file notes field of your commit message.

## 7. Merge + commit

```
./bin/tetragonharness combine \
  -baseline bench-none-open-close.json -baseline bench-none-loopback.json \
  ... # see combine flags: -baseline, -monitor, -enforcement accept one file each
  -out bench-report.json
```

Commit under `results/`:

- `run-{none,monitor,enforcement}-<date>-<host>.json` (validate against `schema.json#/definitions/run_result`)
- `bench-report-<date>-<host>.json` (validate against `schema.json#/definitions/bench_reports`)

Update README §6 verdicts with the measured numbers. If H1–H4 fail on the
host, that is a valid documented result per the issue contract — record
the failure mode, do not tune the harness to force a pass.
