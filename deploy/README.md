# Deploying hanzoai/cloud

Reference deployment manifests for the unified Hanzo Cloud binary
(HIP-0106). Each manifest demonstrates a different deployment topology.

| Manifest | Topology | Use case |
|----------|----------|----------|
| `compose.yml` | Compose Specification | Standalone deployment reference |

Kubernetes deploys live in universe.

## Local quick start (Hanzo CLI)

From the repository root, with the primary Hanzo CLI installed and authenticated:

```bash
make start
make status
make logs
make stop
```

This boots the CLI's current local k3s Cloud workload with the private pinned
Cloud image. It does not yet consume the Compose file, apply its environment
overrides, or implement its persistent named volume. Treat it as a disposable
development instance. The Compose Specification manifest remains a reference
for native Compose support in [Universe #187](https://github.com/hanzo-inc/universe/issues/187).
For production, use the canonical Universe GitOps deployment.

## Environment

Required:
- `HANZO_IAM_ISSUER` — OIDC issuer URL. Without it the IAM subsystem
  refuses to mount and the binary exits.

Optional (defaults shown):
- `HANZO_BRAND=hanzo`
- `HANZO_DOMAIN=api.hanzo.local`
- `HANZO_ENABLE=iam,base,kms,gateway,o11y`
- `HANZO_DATA_DIR=/var/lib/cloud`

The full subsystem list is in `manifest/apps.go`. Per HIP-0106
payments and vault NEVER co-resident — leave them off this binary
unless you understand the PCI scope implications.

## Native engine pool behind ingress and gateway

The agent scheduler is Rust, in `hanzoai/engine/hanzo-router`. Cloud composes
that runtime through its existing metered AI relay:

```text
client → ingress (TLS) → cloud gateway (IAM / org / policy)
                     → metered AI relay → private hanzo-router → hanzo-engine workers
```

Build and run from the cloud checkout (sibling `../engine` checkout required):

```bash
make -C native/engine-router test
make -C native/engine-router build
# Edit deploy/engine-pool.yaml for the actual model IDs and worker addresses.
make -C native/engine-router run
```

The router listens on `127.0.0.1:8000`. Set `ZEN_URL=http://127.0.0.1:8000`
for the AI relay's Zen provider in the **same network namespace**. An existing
provider row overrides that environment configuration; point its provider URL
at the same private root. `ENGINE_URL` is the corresponding existing setting
for the relay's own-compute/free-pool route. Neither URL includes `/v1` (the
relay appends it). Keep `CLOUD_AI_BASE_URL` pointing at the public, metered model
API. `ENGINE_UPSTREAM` remains the management plane's single-engine address;
it is a different setting. Containers on separate networks need a private
service address instead of loopback. The router is not an authentication service:
its listener, including `/v1/replicas`, must remain private to the gateway and
operators. Do not give ingress a public route directly to it.

Cloud's existing family relay preserves JSON metadata and supplies the validated
`X-Org-Id`. Use metadata to carry routing hints through it:

```json
{
  "model": "zen-coder",
  "metadata": {
    "session_id": "cc-01993",
    "agent_role": "main"
  },
  "messages": [{"role": "user", "content": "Implement the requested change"}],
  "stream": true
}
```

Use a distinct session ID for each subagent. `metadata.target_replica` can name
an explicitly configured worker ID. The private Rust listener also accepts
`X-Session-Id`, `X-Agent-Role`, and `X-Target-Replica`; the existing cloud relay
does **not** forward arbitrary custom headers, so header-only clients need
metadata injection in their client/orchestrator. No new Go scheduler or relay
implementation is introduced here.

Placement prefers explicit target, healthy session pin, recent prefix locality,
role, normalized in-flight load, weighted active-session allocation, then TTFT
EWMA. Session pins expire after 30 idle minutes; prefix hints after five. Each
table is capped at 10,000 entries per model. Keys hash the organization, available
user identity, upstream credential, model, and session or structured prompt
prefix (including tools). Prompts and credentials are not retained in the tables.
An existing session tolerates up to twice its worker's configured slots; new
sessions require a free slot. Full pools return 503 with `Retry-After: 1`.

Weights in `engine-pool.yaml` are starting estimates for session allocation;
measure these on your hardware. Capacity is the concurrent request limit, not a
claim about GPU memory. `/v1/replicas` reports capacity, in-flight requests, health,
and time-to-first-body-byte EWMA. Prefix hints come from transport-complete 2xx
responses; they are **not measured KV cache hits**. Role preferences are not a
priority queue. Engine queue depth, cache pressure, decode rate, and adaptive
capacity estimation are not yet collected.

A failed connection evicts the worker and re-pins the session to a survivor.
Recovery does not move that session back. Timeouts and partially delivered
responses are not replayed. SSE bytes pass through unchanged; completion,
stream errors, and client disconnects release the in-flight lease. Failover
requires a fresh prefill; no KV migration is attempted.

Run one router for this pool. Pins are process-local, so multiple router replicas
need upstream session affinity or a shared pin store before scaling out. This
configuration is an opt-in composition recipe: it does not change a running
cloud deployment or provision models on Spark/EVO.
