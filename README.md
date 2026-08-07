# Hanzo Cloud

The open-source Hanzo Cloud — one Go binary that runs a complete local cloud:
a document store, a key/value store, SQL, a task queue, functions, secrets,
object storage, DNS, code search, a gateway edge, feature flags, audit, and a
plugin runtime. Every subsystem mounts from one composition root (`apps.Wire()`).

No Kubernetes. No cluster. No network. No Rust toolchain. A data directory is
the whole dependency.

## Run it

```bash
make dev
```

That builds the binary, mints a local encryption key the first time, and serves
<http://127.0.0.1:8080>. Open it — the console is embedded in the binary.

Or without make:

```bash
go build ./cmd/cloud
CLOUD_KMS_MASTER_KEY_REF=$(openssl rand -base64 32) ./cloud
```

`go build` needs nothing but Go. The feature-flag evaluator has a native Rust
implementation, but it sits behind `-tags flags_native` precisely so that a
missing Rust toolchain can never stop the binary from building; without the tag
a pure-Go fallback takes its place.

### Where it listens

`CLOUD_LISTEN` is the address (default `:8080`). `PORT` is read as a fallback
when `CLOUD_LISTEN` says nothing, so `PORT=3000 ./cloud` does what you expect.
Health and metrics are on a separate ops listener, `CLOUD_HEALTH_LISTEN`
(default `:9090`), so a load balancer probes a port the public never reaches:

```bash
curl localhost:9090/health
curl localhost:8080/v1/openapi.json     # every route this process serves
```

### The encryption key is not optional

Stores open through `cek`, which encrypts them at rest with SQLCipher. **Every**
build can encrypt — the cgo backend links `libsqlcipher`, the pure-Go backend
uses a pure-Go codec — so a build with no key refuses to open the data plane
rather than silently writing plaintext. That refusal is the point, and it is the
same code path in development and production.

`make dev` therefore mints a real key into `.dev/master.key` (gitignored, mode
0600) instead of exempting itself. If you want plaintext files to poke at,
`CLOUD_DEV_UNENCRYPTED=1` opts out of *only* the no-key case, loudly, every boot.
A malformed key still fails. Never set it where real data lives.

## The local apps

Three apps run entirely on the embedded store. They are real implementations,
not proxies to a cluster — the same addresses the hosted product serves, backed
by local SQLite.

| | |
|---|---|
| `/v1/base` | collections of JSON documents — the local store, and so also the local key/value and the local SQL |
| `/v1/tasks` | durable queue with lease/ack |
| `/v1/functions` | function registry and runner *(staged — see below)* |

There is deliberately no `/v1/kv` and no `/v1/sql`. Base is already both: a
document under a collection is the key/value store, and it is SQLite underneath.
Two more doors onto one room would be two more names to keep in agreement.

`/v1/kms` is here too, serving from an embedded `luxfi/kms` — secrets belong in
KMS locally exactly as they do in production, never in an env file.

Every operation is a **typed op**, which is why they need no separate
integration work: one declaration projects into the OpenAPI document, the MCP
tool surface, the CLI and the generated SDKs at once. An untyped route gets none
of those, which is the whole reason not to write one.

```bash
curl -X POST localhost:8080/v1/base/collections/notes -d '{"title":"hello"}'
curl localhost:8080/v1/base/collections/notes
```

### MCP

`/mcp` speaks JSON-RPC 2.0 and exposes every typed op as a tool. It is built
from the same registry as the REST routes, so a tool cannot drift from the
endpoint it calls.

```bash
curl -X POST localhost:8080/mcp \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}'
```

### Namespaces

A request carrying a validated identity operates in that identity's org. A
request without one operates in the **local namespace**, keyed by the empty
string — a key no real org can ever equal, so the two can never collide. That is
what lets the edition be useful with no identity provider running.

It also means an unauthenticated caller who can reach the port can read and write
the local namespace. `make dev` binds loopback for exactly that reason. Put
Hanzo IAM in front of it before it listens anywhere else.

### Functions is staged

`/v1/functions` executes code — it writes a function body to a temp file and runs
it with `node`, `python3` or `bash`. It is linked into every build but mounts
only when named, so it can never appear on a surface nobody asked for it on:

```bash
CLOUD_ENABLE_STAGED=functions ./cloud     # make dev does this for you
```

`iam` and `ingress` are staged the same way, for their own reasons.

## Plugins

Plugins mount from a manifest at `CLOUD_PLUGINS` and load **lazily** — the routes
exist from boot, but a plugin is built on the first request to its prefix, so a
manifest of twenty services costs nothing until they are used. `/v1/plugins`
reports which ones have actually loaded.

## Configuration

| flag | env | default | meaning |
|---|---|---|---|
| `-data-dir` | `CLOUD_DATA_DIR` | `/var/lib/cloud` | data root |
| `-listen` | `CLOUD_LISTEN` / `PORT` | `:8080` | HTTP listener |
| `-enable` | `CLOUD_ENABLE` | *(empty = all)* | subsystem allowlist |
| — | `CLOUD_ENABLE_STAGED` | — | additionally enable staged subsystems |
| — | `CLOUD_KMS_MASTER_KEY_REF` | — | base64 of 32 bytes |
| `-brand` | `CLOUD_BRAND` | `hanzo` | white-label brand |
| `-domain` | `CLOUD_DOMAIN` | `api.hanzo.ai` | primary domain |
| `-iam-issuer` | `CLOUD_IAM_ISSUER` | — | JWKS issuer |

Where a plane the private build injects is absent, the subsystem mounts
fail-closed and says so rather than pretending to work:

```
s3 subsystem mounted fail-closed: S3_ADMIN_ACCESS_KEY not set (all ops 503 until provisioned)
```

## Develop

```bash
make build          # go build, no Rust needed
make test           # go test ./...
make native         # optional: cargo build the Rust flag evaluator
make build TAGS=flags_native
make hooks          # install the pre-push guard — do this once
```

`make hooks` points git at `.githooks`, which refuses any push to `origin`
carrying commits or imports from the private enterprise edition. This repository
and that one have unrelated history and must never merge; the hook is what makes
a mistyped push a non-event instead of a licence incident.

## Licence

Apache-2.0. See [LICENSE](LICENSE).
