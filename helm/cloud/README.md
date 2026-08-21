# helm/cloud

Minimal Helm chart for the unified `hanzoai/cloud` binary (HIP-0106).

## Install

```bash
helm install cloud ./helm/cloud \
  --set hanzo.iamIssuer=https://iam.hanzo.id \
  --set hanzo.brand=hanzo \
  --set hanzo.domain=api.hanzo.ai \
  --set image.tag=v0.1.0
```

## Required values

- `hanzo.iamIssuer` — OIDC issuer URL. Without this the IAM subsystem
  refuses to mount and the pod CrashLoopBackOff's.
- `CLOUD_KMS_MASTER_KEY_REF` — the data-plane root, 32 bytes base64, from KMS
  (or the ring: `CLOUD_KMS_MPC_ENDPOINT` + `_SEALED_B64` + `_KEY_ID` + `_VAULT`).
  See "what is not self-contained" below; without it one child comes up and the
  rest refuse.

## Sizing: what one node needs to run every capability

It does run all of them. Measured on `b46a68385`, one host, every capability
resident at once — linux/arm64, 20 cores, the `CGO_ENABLED=0` lane. Every number
below is from that run; re-measure before quoting one.

Read this first if you are tempted to size from `LLM.md`'s "~1.67 GB heaviest
plugin, ~1.4 GB median": those are LINKER memory while BUILDING, and they are
two orders of magnitude away from what a plugin costs while RUNNING.

### The catalogue

| | |
|---|---|
| rows in `manifest/apps.go` | 123 |
| mounted by the host | 122 (`zen` is middleware on `ai`'s `/v1`, so it is not a mount) |
| shown without a flag | 121 (`research`, `admission` are alpha) |
| prefixes | 175 |
| start with the host (`Eager`) | 4 — `pubsub`, `kafka`, `amqp`, `o11y`, each owning a listener |

### Memory

| | processes | RSS |
|---|---|---|
| after boot, at rest | 7 — host + 6 | 188 MiB |
| shipped 6Gi request, every prefix called | 36 — host + 35 | 1380 MiB |
| every capability resident | 123 — host + 122 | 4335 MiB |

The host process itself is 43 MiB at rest and 53 MiB routing all 122; it is a
router, and it does not grow with the fleet behind it.

At rest the host holds six, not four: the four eager, plus `kms` (the host hands
it the data-plane root and keeps it out of its own environment) and `projects`
(the console at `/` resolves its release through it).

Per child, with all 122 up: **min 23.6, p50 31.2, mean 35.1, p90 51.6, p95 58.1,
max 103.5 MiB** (`o11y`, then `commerce` 80.7, `ai` 68.5). PSS equals RSS to
within 1 MiB across the whole fleet — each plugin is its own file, so no text is
shared between them and the sum is the bill. Add them; do not discount.

### The Warm bound is what you actually deploy

`Warm() = (requests.memory − 200MiB) / 167MiB`, capped at the app count. At the
shipped 6Gi that is **35**, and the bound is enforced: after calling all 122
prefixes the host held exactly 35 children at 1380 MiB. The rest were reaped and
cold-start on their next request.

The 167 is a loaded figure from the running pod. Two independent readings agree
with each other and not with it:

| | per warm child |
|---|---|
| this run, p90, empty stores | 51.6 MiB |
| running pod, implied by 1824Mi across its 35 | 50.9 MiB |
| the constant `Warm()` divides by | 167 MiB |

So the constant is about 3.3× the observed cost, and that is why the host holds
35 of 122 rather than all of them. The whole catalogue warm costs 4335 MiB here
and ~6250 MiB at the pod's observed rate — either way, near or under the 6Gi
already reserved.

**Do not lower the constant from this measurement.** It is idle, over empty
stores, on one architecture, and the number it guards is the one the kubelet
scores eviction against. The reading that settles it is the pod's own: the warm
child count and their RSS, from inside the container. Until then, the number to
change is `requests.memory` — the ceiling follows it.

### Time

| | |
|---|---|
| boot to `/healthz` | 3.4 s, 4.5 s across two runs |
| cold start, first request to a prefix | p50 31 ms, p90 51 ms, p95 70 ms, max 527 ms (`commerce`) |
| warm | p50 1.6 ms, p90 2.4 ms, max 5.4 ms |
| describing a capability | p50 0.5 ms, and no process at all |

That last row is why lazy is affordable. The host answers the bare address
(`/v1/<name>`) out of the document the app's binary projected when it was built,
so 63 of the 122 never start a child to say what they accept. A 31 ms median
cold start is cheaper than holding a process, which is the whole argument for
`Warm` being well below the catalogue.

### CPU

122 children resident and idle cost **25 millicores** over a 60 s window. Booting
the host and cold-starting all 122 cost 3.8 CPU-seconds in total. Residency buys
memory; traffic buys CPU — which is why the HPA scales on CPU and memory is not
a signal.

### Disk and descriptors

| | |
|---|---|
| binaries | 4.2 GiB — host 17 MB plus 123 plugins at ~35 MB, each linking the same core |
| data directory, every store opened empty | 11 MiB |
| open descriptors, all resident | 3136 (host 497) |
| threads | 1736 |
| unix sockets | 369, three per child |

Give the container an `ulimit -n` above ~4096.

### Ports

| port | who | how to move it |
|---|---|---|
| 8080 | host HTTP | `--listen` / `CLOUD_LISTEN` |
| 9653 | host ZAP | `--zap` / `CLOUD_ZAP_LISTEN` |
| 4222 | `pubsub`, NATS client | `CLOUD_PUBSUB_PORT` |
| 9092 | `kafka` wire | `CLOUD_KAFKA_PORT` |
| 5672 | `amqp` wire | `CLOUD_AMQP_PORT` |
| 2222 | `git` over SSH | nothing — fixed |
| 4317, 4318 | `o11y` spans and logs, only once its sink has a datastore | nothing — fixed |

Each fixed number is a claim on the whole node, so two hosts do not share one
without moving at least three of them, and `git` and `o11y` cannot move at all.
That is the only thing on this list that stops two of these coexisting.

`marketing` and `tasks` additionally open a wildcard TCP ear on a
kernel-assigned port: a `luxfi/zap` node built with `Port` unset listens on
`":0"`. It cannot collide and nothing can find it — it costs a descriptor and an
open port on every interface, and it is reachable by nobody.

### What is not self-contained

One thing is required, and the fleet will not come up on one node without it:

- **`CLOUD_KMS_MASTER_KEY_REF`**, 32 bytes base64, or the MPC ring. With no key
  over an empty directory each process mints its own random dev key, so the
  first child to write makes the directory non-empty and every child after it
  refuses to mint rather than make its siblings' data unreadable. Measured
  without it: 17 of 122 up, the rest answering 503. The refusal is correct — the
  dev path is coherent for a single process, and this host is 123 of them.
- The **data directory path must leave room for a socket**. Children reach each
  other over AF_UNIX under `<data-dir>/run` and `sun_path` is 108 bytes; past
  that, every capability that reads a secret fails on a `connect: invalid
  argument` that says nothing about the cause.

Three capabilities start, stay resident, and answer 503 until something outside
this binary is configured. Nothing else needs anything.

| | says |
|---|---|
| `network` | networking is not configured on this deployment |
| `sbom` | datastore not connected — it wants ClickHouse |
| `exec` | code execution not configured |

`o11y` binds its OTLP ports only once its sink has a datastore, so on a node
without one those ports stay free.

### The lane this was measured on

`CGO_ENABLED=0`. The image builds `CGO_ENABLED=1 -tags "libsqlite3 sqlite_fts5"`,
where the codec is SQLCipher rather than the pure-Go envelope, and the storage
posture differs with it. The shape of the fleet is the same; the per-child figures
are worth re-reading once on the shipped lane.

## What this chart is NOT

This is a single-Deployment / single-Service / single-ConfigMap chart.
For multi-tenant / multi-CRD operator-driven topology, install
`luxfi/operator` and use the `Service` CRD instead — that's the
canonical k8s shape per HIP-0106 + HIP-0014.

For PCI workloads (payments, vault) — those subsystems are NEVER
co-resident with the rest of cloud. Run them as separate Deployments
backed by their own charts; configure their ZAP RPC endpoints into
this chart's values.
