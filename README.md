# Hanzo Cloud

The open-source Hanzo Cloud — one Go binary that runs a complete local cloud:
a document store, a key/value store, SQL, a task queue, functions, secrets,
object storage, DNS, code search, a gateway edge, feature flags, audit, and a
plugin runtime. Every subsystem mounts from one composition root (`apps.Wire()`).

No Kubernetes. No cluster. No network. No Rust toolchain. A data directory is
the whole dependency.

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

`CLOUD_ADMIN_LISTEN` (default `:8081`) is read, but nothing listens on it. A
second cloud on the same machine needs its own address for all three:

```bash
CLOUD_HEALTH_LISTEN=127.0.0.1:19090 CLOUD_ZAP_LISTEN=127.0.0.1:19653 CGO_ENABLED=0 make dev PORT=18080
```

### IAM and KMS

This edition has no IAM of its own. It accepts JWTs issued by Hanzo IAM and
checks them against `https://hanzo.id/v1/iam/.well-known/jwks`. `/v1/base` needs
no token. `/v1/kms/orgs/{org}/secrets` does: without one it answers 403
`no validated principal`. With a Hanzo account it works locally, for the org in
the token's `owner` claim:

```bash
TOKEN=$(hanzo auth token)        # after `hanzo auth login`
curl -H "Authorization: Bearer $TOKEN" 127.0.0.1:8080/v1/kms/orgs/<org>/secrets \
  -d '{"name":"API_KEY","env":"dev","value":"example"}'
curl -H "Authorization: Bearer $TOKEN" '127.0.0.1:8080/v1/kms/orgs/<org>/secrets/API_KEY?env=dev'
```

Secrets are sealed under the master key in `.dev/data/orgs/<org>/kms.db`. Without
a Hanzo account, or offline, KMS is not usable yet
([#2](https://github.com/hanzoai/cloud/issues/2)).

### The hanzo CLI

`hanzo network use local` points the [hanzo CLI](https://github.com/hanzoai/cli)
at `http://localhost:3690`, and `CGO_ENABLED=0 make dev PORT=3690` serves there.
The CLI still does not reach it: on a loopback network it starts a separate
`host` binary that this repository does not build, and stops with
`no local cloud host` ([#4](https://github.com/hanzoai/cloud/issues/4)). Call the
HTTP API directly until then.

## The local apps

Three apps run entirely on the embedded store. They are real implementations,
not proxies to a cluster — the same addresses the hosted product serves, backed
by local SQLite.

| | |
|---|---|
| `/v1/base` | collections of JSON documents — the local store, and so also the local key/value and the local SQL |
| `/v1/tasks` | durable queue with lease/ack |
| `/v1/functions` | function registry and runner *(staged — see below)* |
| `/v1/bot` | the protocol a control UI speaks, and under it the bot runs that are going |

There is deliberately no `/v1/kv` and no `/v1/sql`. Base is already both: a
document under a collection is the key/value store, and it is SQLite underneath.
Two more doors onto one room would be two more names to keep in agreement.

`/v1/kms` is here too, serving from an embedded `luxfi/kms` — secrets belong in
KMS locally exactly as they do in production, never in an env file. Its secret
routes need a Hanzo IAM token; see [IAM and KMS](#iam-and-kms).

A bot is a loop, and one instance of that loop is a run. `/v1/bot/runs` is where
your runs are, wherever they happen to be: start one where you have a machine
for it and that machine says so (`POST /v1/bot/runs` with `where: local`), and
it is listed, reachable, and stoppable from the same place as a run in a
sandbox. Nothing in this edition places a sandbox, so asking it to start a run
in the cloud answers 501 and says what is absent rather than minting a run
nobody is carrying out; a deployment that does have an executor hands it to the
registry (`cloud.Deps.Runs`) and its runs join the same list.

A run can park. Suspending one hands back whatever it needs to come back as
itself — a checkpoint reference, opaque here — and the gateway holds that token
without reading it, so the same run can resume on the machine it left or on
another. That is the difference between a registry and a liveness ping, and it
is why a run can move between a laptop and the cloud. Stopping is a separate
door and there is only one of it: the heartbeat refuses the status and names
`/stop`, and forgetting a run ends it through the same halt on its way to
disposing of everything it held.

`/v1/bot` itself is the protocol a control UI speaks — a WebSocket on the
upgrade, one request frame on the POST. Asking to upgrade is a fact about the
request rather than a flag beside it, so the socket needs no name of its own,
and `/v1/bot/runs` is one segment down where it cannot collide with the other
addresses published under `/v1/bot`.

The protocol answers a subset and says so: `hello-ok` carries `features.methods`,
and a UI hides any surface whose method is absent. Advertising exactly what
works is therefore the growth path rather than a compromise — a method that is
merely stubbed should not be listed at all, because a hidden palette reads as a
small server and an empty one reads as a broken one.

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
| `-domain` | `CLOUD_DOMAIN` | `api.hanzo.ai` | primary domain |
| `-iam-issuer` | `CLOUD_IAM_ISSUER` | the brand's issuer, `https://hanzo.id` for `hanzo` | JWKS issuer |

Where a plane the private build injects is absent, the subsystem mounts
fail-closed and says so rather than pretending to work:

```
s3 subsystem mounted fail-closed: S3_ADMIN_ACCESS_KEY not set (all ops 503 until provisioned)
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
carrying commits or imports from the private enterprise edition. This repository
and that one have unrelated history and must never merge; the hook is what makes
a mistyped push a non-event instead of a licence incident.

## Licence

Apache-2.0. See [LICENSE](LICENSE).
