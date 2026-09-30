# Hanzo Cloud

The open-source Hanzo Cloud local dev server — one Go binary that runs a
complete local cloud: a document store, a key/value store, SQL, a task queue,
functions, secrets, object storage, code search, a gateway edge, feature flags,
audit, and a plugin runtime. Every subsystem mounts from one composition root
(`apps.Wire()`).

No Kubernetes. No cluster. No network. No Rust toolchain. A data directory is
the whole dependency, and every default address is on this machine.

## Run locally

Requires Go 1.26.5 and `openssl`.

```bash
git clone https://github.com/hanzoai/cloud && cd cloud
CGO_ENABLED=0 make dev
```

This builds `./cloud`, writes a random key to `.dev/master.key` (mode 0600,
gitignored) the first time, and serves <http://127.0.0.1:8080> with its data in
`.dev/data`. The console is embedded.

Stores are encrypted at rest with SQLCipher, and today only the pure-Go SQLite
backend can do that, hence `CGO_ENABLED=0`. A default cgo build refuses to start
with a key ([#3](https://github.com/hanzoai/cloud/issues/3)). Keep
`.dev/master.key`: the stores open only with the key that created them.
`CLOUD_DEV_UNENCRYPTED=1` runs without a key and says so on every boot.

The same run without make, for a launcher script:

```bash
CGO_ENABLED=0 go build -o cloud ./cmd/cloud
mkdir -p .dev && (umask 077 && openssl rand -base64 32 > .dev/master.key)
CLOUD_LISTEN=127.0.0.1:8080 CLOUD_DATA_DIR=.dev/data CLOUD_ENABLE_STAGED=functions \
  CLOUD_KMS_MASTER_KEY_REF=$(cat .dev/master.key) ./cloud
```

Check it:

```bash
curl 127.0.0.1:9090/healthz
curl -X POST 127.0.0.1:8080/v1/base/collections/notes -d '{"doc":{"title":"hello"}}'
curl 127.0.0.1:8080/v1/base/collections/notes
curl 127.0.0.1:8080/v1/openapi.json     # every route this process serves
```

### Listeners

| env | default | serves |
|---|---|---|
| `CLOUD_LISTEN`, else `PORT` | `:8080`, every interface | HTTP API and console; `make dev` sets `127.0.0.1:$(PORT)` |
| `CLOUD_HEALTH_LISTEN` | `127.0.0.1:9090` | `/healthz`, `/readyz`, `/metrics` |
| `CLOUD_ZAP_LISTEN` | `127.0.0.1:9653` | ZAP, the same routes as HTTP |

A second cloud on the same machine needs its own address for all three:

```bash
CLOUD_HEALTH_LISTEN=127.0.0.1:19090 CLOUD_ZAP_LISTEN=127.0.0.1:19653 CGO_ENABLED=0 make dev PORT=18080
```

### IAM and KMS

This edition has no IAM of its own. It accepts JWTs issued by
[Hanzo IAM](https://github.com/hanzoai/iam) and checks them against
`{CLOUD_IAM_ISSUER}/v1/iam/.well-known/jwks`. The default issuer is a Hanzo IAM
on the same machine, run in its dev mode:

```bash
# in a checkout of github.com/hanzoai/iam
IAM_DEV_HOST_RELATIVE=1 go run . serve --http http://127.0.0.1:8000 --init-data init_data.json
```

`/v1/base` needs no token. `/v1/kms/orgs/{org}/secrets` does: without one it
answers 403 `no validated principal`. With a token from that IAM it works for
the org in the token's `owner` claim:

```bash
curl -H "Authorization: Bearer $TOKEN" 127.0.0.1:8080/v1/kms/orgs/<org>/secrets \
  -d '{"name":"API_KEY","env":"dev","value":"example"}'
curl -H "Authorization: Bearer $TOKEN" '127.0.0.1:8080/v1/kms/orgs/<org>/secrets/API_KEY?env=dev'
```

To accept tokens from another IAM instead, set `CLOUD_IAM_ISSUER` to its issuer
URL. Secrets are sealed under the master key in `.dev/data/orgs/<org>/kms.db`.

### The hanzo CLI

`hanzo network use local` points the [hanzo CLI](https://github.com/hanzoai/cli)
at `http://localhost:3690`, and `CGO_ENABLED=0 make dev PORT=3690` serves there.
The CLI still does not reach it: on a loopback network it starts a separate
`host` binary that this repository does not build, and stops with
`no local cloud host` ([#4](https://github.com/hanzoai/cloud/issues/4)). Call the
HTTP API directly until then.

## The local apps

These apps run entirely on the embedded store. They are real implementations,
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
KMS locally exactly as they do in production, never in an env file. Its secret
routes need a Hanzo IAM token; see [IAM and KMS](#iam-and-kms).

Every operation is a **typed op**, which is why they need no separate
integration work: one declaration projects into the OpenAPI document, the MCP
tool surface, the CLI and the generated SDKs at once. An untyped route gets none
of those, which is the whole reason not to write one.

```bash
curl -X POST 127.0.0.1:8080/v1/base/collections/notes -d '{"doc":{"title":"hello"}}'
curl 127.0.0.1:8080/v1/base/collections/notes
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
| `-domain` | `CLOUD_DOMAIN` | `localhost:8080` | host named in the OpenAPI document |
| `-iam-issuer` | `CLOUD_IAM_ISSUER` | `http://127.0.0.1:8000` | Hanzo IAM issuer (JWKS source) |

Subsystems that talk to a service beside the binary default to this machine and
fail closed when it is not there:

| subsystem | env | default |
|---|---|---|
| `/v1/s3` | `S3_ENDPOINT`, `S3_ACCESS_KEY`, `S3_SECRET_KEY` | `127.0.0.1:9000` (a local [Hanzo S3](https://github.com/hanzoai/s3)) |
| `/v1/dns` | `HANZO_DNS_URL` | `http://127.0.0.1:8443` |
| `/v1/exec` | `CODE_EXEC_UPSTREAM`, `CODE_EXEC_API_KEY` | `http://127.0.0.1:8100` |
| AI (code `/ask`) | `CLOUD_AI_ZAP_ADDR` | none — disabled |

An absent service is reported, never faked:

```
s3 subsystem mounted fail-closed: S3_ACCESS_KEY/S3_SECRET_KEY not set (all ops 503 until set)
```

## Develop

```bash
make build          # go build, no Rust needed; CGO_ENABLED=0 for a binary that can encrypt
make test           # go test ./...
make native         # optional: cargo build the Rust flag evaluator
make build TAGS=flags_native
make hooks          # install the pre-push guard — do this once
```

`make hooks` points git at `.githooks`, which refuses any push to `origin`
carrying history from another remote or an import of a non-public module. It
runs git, grep and awk only.

Benchmarks for this server live in
[hanzoai/benchmarks](https://github.com/hanzoai/benchmarks).

## Licence

Apache-2.0 OR MIT, at your option. See [LICENSE](LICENSE).
