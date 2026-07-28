# LLM.md — hanzoai/cloud

**Canonical repo.** `hanzoai/cloud` (HIP-0106) is the Open AI Cloud as ONE Go
binary + `hanzo` CLI: every Hanzo subsystem (iam, base, kms, ai, gateway,
commerce, o11y, tasks, …) mounted into a single multi-org process. The same
artifact serves `api.hanzo.ai`, `api.lux.cloud`, `api.zoo.cloud`,
`api.osage.cloud`, and every white-label reseller — brand, enabled subsystems,
and org scope are deployment configuration. This is the impl home for the cloud
control plane; the OpenAPI it emits at `GET /v1/openapi.json` is the single
source for the generated per-language SDKs.

## Role in the SDK model
- Full Cloud SDK is GENERATED from THIS binary's OpenAPI; SDK impl lives in
  `hanzo-<lang>/sdk`, docs/wrappers in `hanzoai/<lang>-sdk`, meta in `hanzoai/sdk`.
- AI/agents flagship lib is separate: Python `hanzo` (`hanzoai/python-sdk`),
  Node `@hanzo/ai` (`hanzo-js/ai`). Completeness: Python > Rust > C++ > Go.
- DRY: one impl, one place; discovery repos link OUT, never duplicate impl.
- Full spec: `~/work/hanzo/SDK-ARCHITECTURE.md`.

## Brand rules (hard)
- Hanzo is a full AI cloud, NOT a proxy — never "LLM gateway", never position vs
  LiteLLM. Zen models are our OWN family; never name upstream models.
- `/v1/` only, never `/api/`. Voice: "Hanzo — the Open AI Cloud."

## Install / run
- `docker run -p 8080:8080 ghcr.io/hanzoai/cloud:vX.Y.Z` (pin a released tag) ·
  `go install github.com/hanzoai/cloud/cmd/hanzo@latest` · `brew install hanzoai/tap/hanzo`
- Build in MODULE mode only: `make build` / `GOWORK=off go build <named target>` —
  never workspace mode (see "Build & module graph" below). `make build` is the
  light host; `make plugin APP=<x>` is the one app you are editing; `make ship`
  is the release layout (host + the one multi-call binary — two links, not 108).
  Do not run `go build ./...` here — it links 100+ binaries at ~4.5 GiB each, and
  `make plugins` was DELETED for exactly that reason (62c7f52d).

## Key entry points
- `cmd/cloud` — server binary · `cmd/hanzo` (`cli/`) — control CLI · `webui.go` — embedded console
- `apps/apps.go:Wire()` — composition root (the one ordered subsystem slice)
- `deps.go` / `cloud.Deps` — process-wide handles · `clients/<name>/` — every subsystem
- `openapi/` — the document pipeline: the spec is a projection of the live router,
  and `openapi.yaml` at the root is a GOLDEN of it (written by `make openapi`,
  verified by `make test` — not a second source)
- `manifest/apps.go` — GENERATED from `Wire()`; what `cmd/host` knows about the fleet

---

## Open Cloud planes

Spec home: HIP-0129 `hip-0129-open-cloud-planes` (hips repo). This section is a
map, not the spec. One noun, one owner, one route family. No plane reads another
plane's store; imports flow custody-ward only (channels -> integrations, never
reverse).

Tier is adjudicated by `GET /v1/openapi.json` on the deployment, never by a
branch name — a branch is deleted at merge, so a row that cites one rots into
"not built yet" and the next engineer rebuilds a shipped plane. Op counts below
are the CONCRETE operations that spec names on api.hanzo.ai; they move, the
ownership does not. A product fronted by a catch-all (`/v1/iam/*` proxies the
whole identity surface to the IAM service) would make a count a lie by omission,
so its row says opaque — same rule as "Catch-alls are opaque, by construction".

Count by path SEGMENT, never string prefix: the product is the segment after
`/v1/` (`openapi.Product`), so a `/v1/cloud*` grep also swallows `/v1/cloudflare`
and reports this 5-route plane as 29. Two products, one string prefix.

| Route | Noun | Owner | Tier |
| --- | --- | --- | --- |
| `/v1/connectors` | Custody: per-user BYO external accounts | `clients/integrations` (both planes; user scope) | Shipped — 8 ops |
| `/v1/channels` | Transport: portable message envelope, DM pairing, send + inbox | `clients/channels` | Shipped — 8 ops |
| `/v1/sync` | Data: bidirectional sync engine | `clients/sync` | Shipped — 7 ops |
| `/v1/automations` | Workflows: flows/runs, goja piece runtime | `clients/automations` | Shipped — 20 ops |
| `/v1/bots` | A bot RUN on a surface | `clients/bots` | Shipped — 4 ops |
| `/v1/compute/bots` | A bot MACHINE (kind=bot + agent binding) | `clients/visor` — NOT `clients/bots` | Shipped — 5 ops |
| `/v1/tasks` | Durable engine | `clients/tasks` | Shipped — 11 ops |
| `/v1/machines` `/v1/gpus` `/v1/fleet` `/v1/clusters` `/v1/k8s` `/v1/compute` | Compute: provisioned + BYO machines, GPUs, k8s clusters | `clients/visor` (+ `clients/fleet` registry) | Shipped — 33 ops |
| `/v1/cloud` | Cloud accounts: link DO/AWS/GCP/Azure, discover native k8s clusters, fold into the fleet | `clients/venue` | Shipped — 5 ops |
| `/v1/blueprint` | Cost: OSS-template SBOM (compose→images) + compute-cost estimate | `clients/blueprint` | Shipped — 3 ops |
| `/v1/templates` | Starter kits: ONE entry per template, shapes as variants | `clients/templates` | Shipped — 2 ops |
| `/v1/iam` | Identity: users, orgs, roles | `clients/iam` | Shipped — opaque (catch-all, see below) |
| `/v1/kms` | Secret custody: sealed secrets | `clients/kms` | Shipped — 7 ops |

`/v1/bots` and `/v1/compute/bots` are two nouns with two owners; the row above
pairs each with the package that REGISTERS it. Pairing `/v1/compute/bots` with
`clients/bots` is the merge "Bot is three values" (below) exists to forbid.

Custody invariants: secrets sealed in KMS, never in SQLite rows; verify before
store. Two scopes, two paths, one rule — the path is built from the VALIDATED
principal, never a client field:

    /orgs/{org}/users/{user}/connectors/{provider}/{label}   per-user (integrations)
    /orgs/{org}/cloud/{provider}/{label}                     per-org  (venue)

Refresh is single-flight with rotation resealing; the CLI does local browser
PKCE and posts the bundle to `POST /v1/connectors/:provider/credential`; cloud
owns device-code flows.

Transport invariants: typed actions (`command|url|select|approval`), no raw
string sniffing; pairing codes 8 chars, 1h TTL, max 3 pending per account,
owner bootstrap on first approval.

### Folding a cloud account's k8s clusters into the fleet — the ONE way

An org's own DigitalOcean/AWS/GCP clusters reach the fleet by exactly three
calls. There is no second cluster registry and no import path that skips them:

1. **Link** — `POST /v1/cloud/{provider}/accounts` (body carries the credential;
   `label` defaults to `default`). Verifies the credential LIVE, seals it in the
   org's KMS namespace, discovers the account's clusters and folds them.
2. **Re-sync** — `POST /v1/cloud/{provider}/accounts/{label}/sync` re-discovers
   and re-folds that one account. This is the ONLY refresh verb; it is
   idempotent and acts on the fleet shard recorded at link time.
3. **Read** — `GET /v1/clusters`. Discovered clusters appear here beside managed
   (visor-provisioned) and BYO (hand-pasted kubeconfig) ones and run work
   identically. `DELETE /v1/cloud/{provider}/accounts/{label}` detaches them and
   forgets the credential.

Discovery ends at `fleet.Register` — exactly where `visor.attachCluster` ends —
which is what makes "discovered" and "attached" the same kind of cluster
afterwards. Do not add a cluster store; extend the fold.

**`visor` is an agent name, not a query surface.** `clients/visor` OWNS the
compute plane, but it serves it at the nouns above (`/v1/machines`,
`/v1/clusters`, `/v1/gpus`, `/v1/fleet`, `/v1/k8s`, `/v1/compute`) — never under
`/v1/visor`. The only live `/v1/visor` route is `GET /v1/visor/health`, and that
one is not visor's: `serve.go` auto-mounts `/v1/<name>/health` for every
subsystem that does not set `OwnsHealth`. So do not look for the fleet under
`/v1/visor/*` and do not mount anything there — the node-side agent reports
presence, and presence is read back at `/v1/fleet`.

Container boundary is permanent for native-module, host-filesystem, loop-state,
and vendor-Node work (agent loop, exec/PTY, harnesses, browser, voice, codecs,
Node-bound channels, plugin SDK/loader). The Node plugin SDK is never ported to
Go; cloud extensibility is connectors/automations/tools.

Port roadmap (P1-P15) lives in HIP-0129; do not restate it here. Every claim
carries its tier, and Shipped is the only one with durable evidence — a named
package plus a route in the live spec. In flight/Planned cite a branch or a
backlog id, both of which disappear on merge, so re-check either against
`/v1/openapi.json` before believing it and promote the row when it answers.

## Build & module graph — standalone module, NOT a go.work member

`cloud` is a self-contained deploy unit: its own `go.mod`, `Dockerfile`, binary.
It is intentionally NOT listed in the parent `~/work/hanzo/go.work` workspace —
that workspace deliberately excludes the heavy modules, and merging cloud's
k8s/otel dependency tree with `o11y`'s reintroduces `koanf`/`ugorji`
monolith-vs-split import ambiguities (the parent workspace is itself red on the
koanf split; that is not cloud's bug to fix).

The catch: `go` auto-discovers that parent `go.work` whenever you run a bare
`go build ./...` / `go test ./...` from inside this tree, which puts the build in
workspace mode and SILENTLY DROPS cloud's own `go.mod` directives. Those
directives are load-bearing and each fixes exactly one graph hazard:

- `replace github.com/vulcand/oxy/v2 => github.com/traefik/oxy/v2 <pseudo>` — the
  bare require is a placeholder (`v2.0.0-00010101000000-000000000000`); without
  the replace it resolves to an invalid version.
- `exclude github.com/ugorji/go <old-monolith>` — drops the pre-split monolith so
  `github.com/ugorji/go/codec` (pulled by gin) is unambiguous.
- the `k8s.io/*` staging replace block pins every staging module to the `v0.35.3`
  line. `k8s.io/kubernetes` is a GRAPH-ONLY transitive require of
  `hanzoai/deploy/gitops-engine` (clients/deploy uses its `pkg/utils/kube`); NO
  cloud package imports `k8s.io/kubernetes`, so its staging tree never compiles —
  do not "drop k8s.io/kubernetes", the pins keep the graph consistent and it is
  never built. koanf resolves to the split modules; the `koanf v1.5.0` monolith
  require is a harmless graph leaf, never imported.

So: build cloud in module mode, never workspace mode. `make build`/`test`/`vet`/
`tidy` force `GOWORK=off` (matches CI and the Dockerfile, which check out cloud
alone with no parent go.work). For a bare `go` command from this tree, prefix
`GOWORK=off`. `GOWORK=off go build ./...` and `go vet ./...` are green; `go mod
tidy` is stable. Do NOT commit a `go.work` here — it would flip the Dockerfile
(`COPY go.mod go.sum` → `go mod download` → `COPY . .`) into workspace mode after
its `-mod=readonly` download step.

Test modes: `make test` is pure-Go (`CGO_ENABLED=0`). Encrypted-at-rest OrgDB
tests (`cek`, `CLOUD_KMS_MASTER_KEY_REF` set) REQUIRE `CGO_ENABLED=1` +
libsqlcipher (`cek/cek.go` refuses to encrypt in pure-Go); those run only in the
Dockerfile's dedicated `-tags libsqlite3` CGO stage, and fail under `make test`
by design (clients/git, kms, flags, x402, cmd/kmsreseal, finance). Bundle-embed
tests (clients/tasks/ui) need `make deploy-ui` first (real bundle is gitignored).

Store-heavy subsystem tests are fsync-bound, not CPU-bound. A mount opens its own
SQLite stores, so a test that mounts several subsystems commits many times, and
`t.TempDir()` under `/tmp` puts every commit behind the ext4 journal — on a box with
a concurrent build the same mount that costs milliseconds idle costs ~90s, at ~0%
CPU, blocked in `jbd2_log_wait_commit`. Point `TMPDIR` at tmpfs to measure the real
cost: `TMPDIR=/dev/shm/t GOWORK=off go test -p 1 ./clients/guide` runs the eight-seam
cross-subsystem harness (`clients/guide/drivehome_e2e_test.go`) in under a second.
Prefer one package per `go test` invocation regardless: `./...` links every main
package at once (`cmd/cloud` alone links >6GB).

## Two hosts: `cmd/cloud` links every app, `cmd/host` links none

The app count is `len(manifest.Apps)` — 113 at `e88ea216`
(`grep -c '^\s*{Name: ' manifest/apps.go`), and the standing figures below were
each measured against a smaller fleet. Treat every absolute in this section as a
measurement with provenance, not as a live count; re-measure before quoting one.

`cmd/cloud` imports `apps` and therefore links every subsystem graph into one
binary — **3108 packages**, 212 MB, 9.5s to link with a fully warm cache (minutes
cold) at 3.8 GiB peak RSS, and a relink for every app that changes. `cmd/host` is
the same API served a different way: it links `zip` and `manifest` and stops
(**316 packages**, 19 MB, 0.6s link, 13 MB RSS), mounts each app as a
`zip.Plugin`, and starts a child on the FIRST REQUEST that reaches its prefix. An
app nobody calls costs a route entry, not a process; a woken one costs ~27 MB.

**The host is the default.** `make build` builds it; `make plugin APP=<x>` builds
the one app you edited (1.3s after a real source change).

**There is ONE artifact, invoked two ways.** A dedicated plugin binary is ~40 MB
of which ~35 MB is the core every other plugin also links, so 108 of them measure
4.41 GB of duplicated code — already stripped, `-s -w` is the default `LDFLAGS`,
there is no symbol win left in it. The multi-call binary is that core ONCE
(196 MB) and serves any app via `cloud --enable=<name>`; `manifest.MultiCall`
(manifest/plugin.go:49) is its name. That invocation is not a second mode:
`--enable` is the flag `cloud.Serve` has always taken (config.go:483), so a child
started that way is byte-for-byte the process a dedicated `cmd/<name>` binary
would be — same `Serve`, same middleware, same `ZIP_ADDR` contract. **Monolith
and plugin are not two artifacts to keep in sync; they are one artifact under two
invocations,** which is why `make ship` links two things (`host monolith`,
Makefile:120) and the image ships `/cloud` + `/host` as run modes of itself
(Dockerfile:182-194).

`make plugins` is DELETED, deliberately (62c7f52d). It linked 100+ binaries back
to back, which exhausted a tmpfs `/tmp` and OOM'd a 128 GiB box, so it had to
build sequentially at `-p=2`; the forced `TMPDIR` went with it. Do not
reintroduce it — the set it built is what the multi-call binary replaced.

The default ENTRYPOINT stays `/cloud` on measured grounds, not inertia: five apps
in one process cost 166 MB PSS, and the same five as host+children cost 388 MB,
because every child pays its own Go runtime and its own `BuildDeps`
(Dockerfile:196-200). Process isolation is worth buying deliberately for a
subsystem that needs it — never fleet-wide by default.

The host knows three facts per app and no more — name, prefixes, eager-or-lazy —
and they are DERIVED from `apps.Wire()`, never hand-maintained. `make generate`
runs `cmd/gen-app-cmds`, which reads Wire ONCE and emits both `cmd/<app>/main.go`
and `manifest/apps.go`, so an app cannot exist in one and not the other. Prefixes
come from, in order: the `PluginSpec` call's own arguments, a declared
`Prefixes:` field, then the absolute paths the app's package registers — read by
walking the call graph from the entry's Mount function (per FUNCTION, because
`clients/account` serves two Wire entries and a package-wide scan gives each the
other's paths). Both registration forms are read: `app.Get("/v1/x", h)` and the
typed `zip.Get(reg, "/v1/x", h)`. The walk resolves consts, `[]string` ranges and
concatenation, and tracks which values are Groups so `g.Get("/health")` never
becomes the prefix `/health`.

Two rules make a wrong prefix impossible rather than merely unlikely. A prefix is
never widened to a shorter ancestor (`/v1/iam/keys` stays that deep — folding it
to `/v1/iam` would hand `account` the whole identity plane), and a prefix must
start with a literal segment (git's `/:org/:repo` matches every two-segment
request in the fleet; in one binary its handler inspects the Host and falls
through, but once a request is proxied to a child there is no falling through).
An app the walk cannot reduce to prefixes gets no row and says why on stderr —
`make generate` names them. The fix for an under-reported app is one line in its
Wire entry: `Prefixes:` outranks the walk.

`cloud.Serve` honours the plugin side of the contract in ONE place, `listenOn`:
with `ZIP_ADDR` set the process serves that socket and binds no ops port, so
every generated `cmd/<app>` is a valid plugin with no code of its own. Without
that, each child binds cfg's fixed `:8080/:9653/:9090`, the host never sees it
listen, and all but the first die on "address already in use".

CI pins both properties from `hanzo.yml`: `generated-current` re-runs the
generator and fails on a dirty tree; `host-is-light` fails if `cmd/host`'s import
graph reaches `apps` or any `clients/*`.

### Where a subsystem's binary comes from — ONE ladder, and the last rung is the network

`manifest.App.Plugin()` (manifest/plugin.go:74) is the SOLE resolver, and
`apps.where(name)` (apps/apps.go:650) calls it rather than re-deriving anything:
the light host resolves the same binaries from the same rule out of the generated
manifest, and a second copy is exactly how the two come to disagree. The copy
that used to live in `apps` had already drifted — it left `Plugin.Name` empty and
did not fold `-` to `_`, so `CLOUD_ZERO_TRUST_ADDR` was unreadable from that side.
`where` now supplies the one fact the manifest cannot: `eager`.

    CLOUD_<NAME>_ADDR   already listening there — start nothing, just mount it
    CLOUD_<NAME>_BIN    this exact path, honoured as given (the operator named it)
    <dir>/<name>        a dedicated binary beside the running host
    <dir>/cloud         the multi-call binary beside it, with --enable=<name>
    CLOUD_PLUGINS       the release index (manifest/release.go) — a host whose
                        image carries NO plugin binaries at all

`<NAME>` upper-cases the app name and folds `-` to `_`. On-disk wins over the
index because it is what this host was BUILT with; a dedicated binary wins over
the multi-call one because its presence is someone's explicit intent. Both link
modes stay: a developer builds the single lean plugin they are editing and the
host prefers it (1.3s), a release ships the unified binary and the host falls
through to it.

**Eager vs lazy is a property of the WORK, not of the app.** `apps.go`'s `eager`
map (apps/apps.go:631) names the four subsystems that must start WITH the host —
`o11y` (its OTLP collector must accept spans before anything has one to send),
`pubsub` (NATS :4222), `kafka` (:9092), and `catalogsync` (a pure bus consumer
that registers no route at all, so lazily it would wait forever for a request
that never arrives). Everything absent from that map is lazy, and that is what
makes a 100+-service binary cheap: an app nobody calls costs a route entry and a
struct, not a process. The generator stamps `Eager` onto every manifest row from
this one map.

### The S3 plugin lane: `hanzo.yml binaries:` → `binaries.json` → verified fetch

`hanzo.yml` declares ONE entry for the whole fleet (hanzo.yml:32-35): `name:
cloud`, `main: ./cmd/cloud`, both linux platforms. That is deliberate and it is
the same fact the ladder rests on — every app resolves to the multi-call binary
with a different `--enable`, so the index names it once: 195 MiB published against
4520 MiB for a binary per app. `bucket: plugins` (hanzo.yml:41) publishes to
hanzoai/s3, NOT a GitHub release: artifacts and the `binaries.json` naming them
land under `<bucket>/<repo>/<tag>/`, so `CLOUD_PLUGINS` is one immutable URL per
tag and 400 MiB a release never touches a storage quota we do not own.

This lane is NOT a second builder of the cloud image. The image's `/cloud` is
cgo + libsqlcipher; a plugin runs on whatever base its host happens to be, so the
published binary is the static one (hanzo.yml:23-26).

- **No digest, no trust.** `fetch` drops any index entry missing `url` or
  `sha256` (manifest/release.go:82), and `remote` returns a `zip.Plugin` with
  both `URL` and `Sum` set (release.go:113-119). zip verifies before `chmod`, so
  fetching code stays safe to execute, and it caches BY DIGEST — restart and
  rollback touch no network.
- **A dedicated index entry beats the multi-call baseline**, same order as on
  disk: `remote` looks up `name/os/arch` first and only falls back to
  `MultiCall/os/arch` with `--enable` (release.go:103-107). Pinned by
  `TestRemote_DedicatedBeatsMultiCall` (manifest/release_test.go:177).
- **`fetch` caches SUCCESS for the life of the process, and that is a
  cache-invalidation contract, not an optimisation** (release.go:43-59). A hundred-plus apps
  resolving through here must not become a request each. Failure is deliberately
  NOT cached: a lazy plugin can first resolve minutes after boot, so caching one
  blip while the network came up would disable every plugin for the life of the
  process. So: **rewriting an index a live host has already read changes nothing
  for that host — publishing cannot push.** New bits
  reach a running process exactly two ways: restart it, or
  `zip.App.ReloadTo(name, Plugin{URL, Sum})` via
  `POST /v1/admin/plugins/:name/reload`. Any on-demand or per-org upgrade path
  must drive one of those two.
- **Reload is SuperAdmin-gated, and audited BEFORE it acts.** `clients/plugin`
  mounts four routes (clients/plugin/plugin.go:78-81): list, reload, enable, disable. Every
  mutation is SuperAdmin-gated and written to the hash-chained audit trail BEFORE
  it is reported as done; a deployment with NO durable audit store REFUSES the
  operation rather than performing an unrecorded one (clients/plugin/fleet.go:154-155).
  Reload starts the replacement and proves it LISTENING before any traffic moves,
  so a bad build leaves the old one serving and returns an error rather than a
  hole; naming a digest this host has run before IS the rollback, and costs no
  network because the digest is the cache key. Fleet scope applies one host at a
  time and STOPS at the first failure, so a build that cannot come up reaches
  exactly one host.
- **`disable` answers 503, never 404, and routes never unregister.** Removing
  routes would mutate the route table and re-adding them on enable would grow it
  without bound across cycles — keeping them registered is the invariant that
  makes reloads flat in memory. It is also the truer answer: 404 says "no such
  API" and a client may cache it and stop retrying; 503 says "this API exists and
  is down right now", which is retryable.

## Credentials: one key for the deployment, one scope per app (`credz`)

A plugin is a child process, and `zip` spawns it with `os.Environ()`. So whatever
the launcher holds, all 108 children hold — every secret in every
`/proc/<pid>/environ`, inherited by anything any of them execs. `credz` replaces
that: **one process holds the root key; every other asks it, over a unix socket,
for the credentials of the app it is.**

- **The ONE credential a deployment provisions is `CLOUD_KMS_MASTER_KEY_REF`.**
  It unseals the KMS secret store and keys the cek data plane. Every other secret
  lives *inside* that store. `credz.Boot` takes it out of the environment at
  process start and holds it in memory, so no spawned child inherits it.
- **Who brokers**: the process that read the root key from its own environment
  AND owns the sealed store (`deps.KMS` is the embedded client). That is the KMS
  subsystem — exactly one process. `cmd/host` stays light; it holds nothing.
- **Who asks**: every other `cloud` process, at the top of `Serve`, before
  `LoadConfig` and before any store opens.
- **Identity is NOT sound yet — do not merge this to main as a security boundary.**
  No token, no name in the request: the broker reads `SO_PEERCRED` and resolves
  the peer's argv through `/proc`, accepting both spawn shapes the manifest
  produces (`<dir>/<app>`, `cloud --enable=<app>`) checked against
  `manifest.Apps`. `SO_PEERCRED` is kernel-authenticated for pid/uid — **argv is
  not**. A process picks its own `argv[0]` at `execve`, so any same-uid process
  can present itself as any app and receive that app's bundle, *including the
  root key*. Demonstrated: a binary named `spoof` was granted `billing`'s and then
  `ai`'s bundle and logged as a legitimate grant both times.

  So today this partitions credentials against **accident** (107 processes stop
  carrying secrets they never use) and not against a **compromised** process. The
  fix has to come from the spawner, which is the only party that knows which app
  it started as which pid: a per-plugin nonce in `zip.Plugin.Env`, or a
  pre-connected socket passed as an `ExtraFile` — both in `manifest/plugin.go` +
  `cmd/host`.
- **Scope is derived, not configured** — the manifest names every app, the store
  holds every secret, and the path is built from the peer's identity:

      /orgs/{adminOrg}/svc/_shared/{NAME}   every app
      /orgs/{adminOrg}/svc/{app}/{NAME}     that app only

  `{NAME}` is the environment variable the app already reads, and `credz` reads
  env `default` — the store requires `env` on every write, so provisioning is:

      POST /v1/kms/orgs/{adminOrg}/secrets
      {"path":"/svc/ai","name":"CLOUD_AI_API_KEY","env":"default","value":"sk-…"}

  No second registry and no code change to add a credential. The `billing`
  process is never handed `/svc/ai` — subject to the identity caveat above, which
  is what decides whether "never handed" also means "cannot obtain".
- **The environment stays the interface**: the bundle is installed with
  `os.Setenv`, so all 108 apps keep reading `os.Getenv` unchanged — and a value
  set after `execve` never appears in `/proc/<pid>/environ`.
- **Three postures, logged at boot** (`credentials: ROOT|LEAF|DEV`, plus the
  fail-closed case). `make host` with nothing provisioned resolves DEV: a
  deterministic key through the same encrypted path as production, zero config.
- **Boundary, stated honestly**: the data-plane key is shared by every process in
  the pod, because they open the same encrypted files. `credz` scopes the
  *service* credentials. The pod is the data-plane boundary; the app is the
  credential boundary.

Ordering is load-bearing: `cek` memoizes the master key on first use, so
`credz.Boot` runs before the first store opens (top of `Serve`, and again at the
top of `BuildDeps` for callers that skip `Serve` — it is `sync.Once`). Installing
a key any later loses to the cached "no key" while the log claims success, which
is exactly the bug this replaced.

## One build contract: `mk/plugin.mk`, and an app's Makefile is its name

`clients/<app>/Makefile` is two lines — `APPS := <name>` and
`include ../../mk/plugin.mk`. Everything an app can be asked to do lives in that
one included file: `generate` (zipdoc lifts handler prose into `zipdoc_gen.go`;
a prerequisite of `build` — mk/plugin.mk:62 — because it is compiled IN, so
running it after the build would be too late), `build` (its own lean binary into
`./bin`), `test`, `vet`, `openapi` (its own spec subset; `openapi: build`, since
a spec generated from a stale binary is a lie), `clean`, `help`. A target written
once per app would be one place per app for them to disagree, and nobody edits a
hundred files at once. `clean` removes binaries only: `cmd/<app>/openapi.json` is
a committed artifact, like the fleet's `openapi.yaml`, and clean removes what a
build wrote, not what a build publishes.

**The per-app path is the one that has always regenerated zipdoc**, and the root
build targets do NOT (`build`, `host`, `ship`, `plugin`, `monolith`). The only
root target that runs it is `openapi` (Makefile:214); the Dockerfile carries its
own standalone pass at line 178. See "Generated and frozen artifacts" below —
this asymmetry is still live, and it is why the 15 `zipdoc_gen.go` files are
committed.

Both invocations work — `make -C clients/tasks openapi` from the root and
`cd clients/tasks && make openapi` — because `mk/plugin.mk` derives every path
from the including Makefile's own location, never from the caller's cwd. That is
what makes the OSS/private split a move rather than a rewrite: an extracted
`clients/<app>` + `cmd/<app>` + `mk/` keeps the paths intact.

`APPS` is a list and is never inferred from the directory name — four packages
are not named after their app (`zt`→zero-trust, `eval`→evals, `auditlog`→audit,
`plugin`→plugins) and `clients/account` backs two mounts. Three apps (authz,
licensing, metrics) are external modules with a `cmd/<app>` and no source
directory here; `mk/fleet.mk` runs them through the same recipe by name.
`mk/go.mk` is the toolchain contract every includer shares (GOWORK=off, TMPDIR on
disk, `-p=2`, the dev KMS key, the FTS5 tag).

## Framework doctrine

One way to do everything. Composable, orthogonal, DRY. A new subsystem is a
package under `clients/<name>` that obeys these seams — nothing more.

- **Subsystem shape.** A subsystem exposes
  `func Mount(app cloud.Router, deps cloud.Deps) error` — `MountFunc`
  (build.go:1002) — and is listed in `apps.Wire()` as
  `cloud.MountSpec{Name, Price, Mount}` (plus `Shutdown`/`OwnsHealth`/`Prefixes`
  where it owns them). `app` is a **Router, not the concrete `*zip.App`**, and
  that is the whole safety property: middleware a subsystem installs lands on the
  subtrees its spec declares, never over the binary. Routes still register at
  absolute paths with the same precedence. `cloud.Deps` carries the process-wide
  handles (Logger, DataDir, the subsystem `Client` seams). No subsystem reaches
  into another's internals. There is no init()-registry and no `cloud.Register` —
  subsystems do NOT self-register.
- **The field IS the grant — `App` instead of `Mount`.** A subsystem that
  genuinely gates everything sets `App func(*zip.App, Deps) error` and receives
  the bare app (`{Name: "authz", Price: cloud.Free, App: authz.Mount}`).
  `MountAll` refuses a spec carrying BOTH (build.go:1103), so scoped-or-global
  stays a decision someone made in writing. **`cloud.Global` is DELETED**
  (62c7f52d): it was a wrapper that asserted `Router` was `*zip.App` and, when it
  was not, told you to also set `Global: true` — wrapper and flag were the same
  fact, exactly 6 and 6, and the wrapper could not work without the flag. Do not
  reintroduce either. Two failure modes went with it: mounting a plugin on a
  scoped Router, and forgetting the flag, are now unrepresentable rather than
  tested.
- **`Price` is part of the declaration, and it is REQUIRED.** Every spec states
  what ONE request to its surface costs at the edge — `cloud.Free`,
  `cloud.Metered`, or a positive number of cents (price.go). The zero value is
  `Undeclared` and `apps.TestPriceDeclared` fails on it, so a new subsystem
  cannot reach main until someone answers the question in the same diff that adds
  its routes. `DefaultPrice` reads this and keeps NO table of its own.
- **Out-of-process variant.** A subsystem may run as its OWN binary without
  changing anything about it:
  `cloud.PluginSpec(name, price, zip.Plugin{…}, prefixes…)` (plugin_spec.go:40)
  returns an ordinary `MountSpec`, so where a subsystem runs stops being a
  property of its source and becomes one line at the composition root. `zip.Load`
  returns a `zip.Service` — the same type a linked-in service is — so nothing
  downstream (routing, health, shutdown ordering) can tell the difference. zip
  starts the child on a private unix socket and forwards the path UNCHANGED.
  Today `o11y` is the only one — the heaviest graph in the tree (otel-collector,
  prometheus, gonum), imported by nothing else, so unlinking it is pure
  subtraction. **Unlinking means deleting the IMPORT, not just the mount**:
  `apps/apps.go` carries a standing comment where `clients/o11y` would be
  imported, because an import there would keep its 2.7k-package graph linked
  whether or not any Wire entry referenced it.
  - `price` is POSITIONAL, ahead of the variadic prefixes, and that placement is
    forced rather than chosen. A plugin serves its prefixes from another process
    and NOTHING downstream of the spec can see what happens in there, so what the
    surface costs has to be stated by whoever decides to mount it — exactly as
    for a linked-in subsystem.
  - `prefixes` is variadic because ONE service commonly owns several route
    subtrees (`o11y` answers `/v1/o11y` AND `/v1/sentry`, both registered by the
    same `MountO11y` the child runs). Naming only the first 404s the rest AT THE
    HOST — the request never reaches the child — while the host starts and
    reports healthy. **The plugin is the unit of deployment; the subtrees it owns
    are a property of it, not a reason to declare it twice.** Nothing is
    defaulted or validated in `PluginSpec`: `zip.Load` already rejects an empty
    list by name, and restating that would put one rule in two places.
  - `PluginSpec` sets `App`, not `Mount`, because `zip.Load` registers under the
    prefixes it was given — handing it a scoped Router would nest them under the
    subsystem name and the routes would answer somewhere nobody is asking. The
    consequence: it does NOT narrow middleware, since `MountAll` builds a scope
    only for a spec that supplies `Mount`.
  - `Prefixes` is still stated on the spec, and reaches both readers from one
    place: zip routes on it, and `Declare` reads it for the boot inventory
    (`/v1/admin/subsystems`) and the per-request subsystem attribution tracing
    hangs off. Leaving it empty falls back to the `/v1/<name>` convention — which
    for a plugin owning a second subtree means that subtree's traffic is
    attributed to NOBODY.
  - The image must actually CONTAIN the binary: `zip.Load` fork/execs a sibling
    of `/cloud`, so a missing one aborts the mount and cloud never listens
    (`fork/exec /o11y: no such file`). The Dockerfile DERIVES the list by grepping
    `PluginSpec("…"` out of `apps/apps.go` rather than keeping a second copy —
    unlinking o11y without adding a build step once cost five consecutive
    releases — and FAILS the build if a declared plugin has no `cmd/<name>`,
    rather than at a pod's first boot. **Unlinking a subsystem means building it
    somewhere else, not just deleting the import.** This is only for `PluginSpec`
    apps: under `cmd/host`, every OTHER app has no dedicated binary in the image
    and resolves down the ladder to `/cloud --enable=<name>`, which is why that
    path needs no per-app build step at all.
- **Client seams.** Cross-subsystem calls go through a narrow in-process interface
  published in `types` and aliased at the provider, e.g. `commerce.Client =
  types.CommerceClient` (`GetOrgConfig` + `CheckEntitlement`). Consumers depend on
  the interface, never the implementation; the seam rides zap-proto/zip. Keep each
  interface minimal — add a method only when a consumer needs it.
- **Composition root.** `apps/apps.go:Wire()` returns `[]cloud.MountSpec` — every
  linked subsystem, in mount order, as ONE explicit slice read top-to-bottom.
  Slice position IS the order: there is no `Order` field and `MountAll`
  (build.go) does NOT sort; it iterates as-given and mounts each ENABLED spec
  (`cfg.Enabled`). To add a subsystem you add one line to `Wire()`, and teardown
  needs no separate gate: `MountAll` registers each `Shutdown` via
  `app.OnShutdown` right after that subsystem mounts, and zip drains hooks LIFO
  after the listeners stop — so registration-at-mount yields reverse-mount
  teardown with nothing torn down while a request still uses it.
  `apps/wire_test.go` freezes the sequence (name, `OwnsHealth`, has-`Shutdown`,
  global), so a reorder/drop/add fails there. **That test is a FROZEN GOLDEN, not
  an invariant: when `Wire()` legitimately changes, the fix is to update `frozen`
  in the same diff.** `meet` was added RED and then frozen; `rollingcap` says so
  in its row ("golden drifted — refrozen"). Each row carries the deleted
  order-int as provenance, and a deliberate flag change is annotated in place
  rather than silently edited — o11y's `hasShutdown` flipped true→false when it
  became a plugin, and the row explains that the host no longer owns any o11y
  resource to close.
- **Route precedence.** The router is zap-proto/fiber (zip v1.8.3). Most-specific
  route wins regardless of mount order, so subsystems may mount in any order and
  still compose deterministically. But precedence is NOT a conflict guard: two
  registrations of a byte-identical pattern do NOT panic — fiber MERGES them into
  ONE route with both handlers chained, resolving by first-registration. That is
  invisible to a `GetRoutes()` entry count (see the bots note below), and it is
  NOT distinguishable from a legitimate middleware chain: `app.Post(path, mw1,
  mw2, mw3, handler)` is one registration with four handlers (apps/commerce.go:151),
  and the whole `/v1/store/*` surface is that shape. A high handler count is
  therefore evidence of nothing on its own; only a subsystem that never chains
  middleware (bots/visor/runtime) can read `len(Handlers) > 1` as a collision.
- **Per-org data.** The ONE way any subsystem opens a per-org SQLite file is
  `cloud.OrgDB(dataDir, org, project, sub)` — or the cached `cloud.OrgStore[T]`
  (`NewOrgStore` + `For(org, project)`). Path convention:
  `{DataDir}/orgs/{org}/{sub}.db`, or `{DataDir}/orgs/{org}/projects/{project}/{sub}.db`
  when project-scoped. Isolation is PHYSICAL: a distinct `(org[, project])` is a
  distinct file. `org`/`project` MUST be the VALIDATED principal values
  (`principal.Org(c)`, `principal.Project(c)`) — never a raw body/header — and are
  folded through `SanitizeOrg`, the ONE injective org slugger. hanzoai/sqlite is
  the SOLE driver (blank-imported once, in orgdb.go); subsystems never import a
  SQLite driver themselves. The caller owns its schema/migration and Close.

## Zero-downtime HA for per-org stores (rolling-upgrade safe)

The per-org store path (`cloud.OrgStore` + `internal/org`) is HA over embedded
SQLite: `ha` decides WHO writes (HRW election + a monotone fencing round), `vfs`
FencedStore decides HOW state ships (hydrate-on-open + fenced ship to S3), and this
layer decides WHEN ownership transitions. Three orthogonal lanes; SQLite stays
embedded underneath.

- **Durability is THE path, capability-detected — no flag.** `buildDurability`
  probes at boot: no object store reachable (dev / native-Go) → local-only, same
  code path; a reachable store → `org.ProbeCAS` PROVES its conditional-PUT
  atomicity (two racing If-None-Match creates + If-Match updates, exactly one winner
  each) before fencing any tenant data. A store that can't be proven atomic fails
  SAFE to local-only + a loud alert (never fence where two writers could win one
  round). Replaced the old `CLOUD_RESEARCH_DURABLE` opt-in — the atomicity gate (H2)
  is now a self-check the binary runs.
- **Live membership (no static peer list).** `membership_k8s.go` lists Ready,
  non-terminating pods by label (`CLOUD_PEER_SELECTOR`) via the K8s API each 2s
  refresh, so a rolling upgrade's changing pod set is tracked and a draining/dead pod
  (`DeletionTimestamp` set, or NotReady) leaves the writer election at once. Out of
  cluster / no selector → static self set (`podWriterEligible` is the ONE ready gate;
  visor has the twin, the shared `hanzoai/ha/k8s` source folds them).
- **M3 live re-acquire, no restart.** A store that opened degraded (read-only) is
  promoted IN PLACE when this replica becomes the org's elected owner:
  `Durable.PendingPromotion` gates it, `TryClaim` probes the lease (CAS only, no file
  I/O), then `OrgStore.promote` quiesces the read-only handle and reopens as owner
  (Hydrate renews + CarryForward-restores under the FRESH handle — the file swap is
  why the reopen is required). The reopen claims a strictly higher round, fencing the
  prior owner — never two live writers.
- **Graceful drain.** SIGTERM → `SetDraining()` → `/readyz` 503 (drain-aware, ops
  port) → K8s marks NotReady → peers re-elect this pod's orgs to live successors
  (which hydrate via M3) → the pod stays serving a short grace, then in-flight drains
  and final state ships (`OrgStore.CloseAll`, ship-before-close). The shard router
  routes on the live set when the durable plane is on, so a draining pod's orgs go to
  the ready successor — not to the gone pod. Manifest: readiness → `/readyz` on the
  metrics port, `terminationGracePeriodSeconds` ≥ ~40s, RBAC pods:list,watch, the
  downward-API `POD_NAME`/`POD_NAMESPACE`, `CLOUD_PEER_SELECTOR` (all in `helm/cloud`).
- **Proof.** `internal/org/rollingupgrade_test.go` rolls 3 pods over 8 orgs with
  continuous writes and asserts zero lost acked writes, zero split-brain (no
  (org,round) acked by two pods), and continuous availability — across both a pod
  restart (fresh rehydrate) and an in-place ownership flap (M3, no restart).
- **Two extensibility seams (for the tiered-storage perf pass).** The fence's
  `ConditionalStore` is constructed in `buildDurability`, so a KV read/write-through
  cache (L1 over the S3 L2) wraps it as a one-line decorator. The ship mechanism is a
  swappable `snapshotCodec` (default `wholeFile`), so WAL-frame delta shipping
  replaces it without touching the fence/round. `WithCheckpoint` injects the ship
  checkpoint (`durableCheckpoint`) — the crypto envelope's re-encrypt integration
  point: on a defer-encryption-to-checkpoint backend it MUST route through the
  driver's re-encrypting Checkpoint so ship-before-ack reads FRESH ciphertext (P5).

## The route table has three projections, and the router is the source

`serve.go` composes ONE route table and projects it three ways, all after
`MountAll` so each sees a complete table: `/zap` REPLAYS the /v1 handlers
(zapface), the console RENDERS them, and `GET /v1/openapi.json` DESCRIBES them
(`openapi.Mount`). None holds a second copy of anything; none can drift.

(The document itself now has four SINKS — the live endpoint, the committed
`openapi.yaml` golden, each app binary's own subset, and the weave of those
subsets. They are four renderings of one value, not four projections; see "The
document pipeline" below.)

- **The spec IS the router.** `openapi.Live(app)` reads
  `app.Fiber().GetRoutes(true)` — fiber's own filter drops `Use()` middleware —
  and every other function in `openapi/` is a pure function of that `[]Route`.
  There is no HAND-MAINTAINED spec and no second route registry. (`openapi.yaml`
  at the root is a checked-in GOLDEN — written by the same code path that serves
  the live document, verified on every `make test`. It is a rendering, not a
  source; `openapi.Register` adds a registry of BODIES, never of routes.) The
  drift guard is `cmd/cloud/openapi_test.go`: a BIJECTION over the fully-mounted
  `apps.Wire()` — every live route appears as an operation, every operation is
  backed by a live route. It is the only test whose failure means the document
  lies. The test LOGS its size and pins only the bijection, so never quote that
  size as a fact here: it grows every time a subsystem gains a route, and a
  quoted count is stale the next week (api.hanzo.ai measured 1467 operations /
  1064 paths / 167 products against a doc that still claimed 983/692/109). Count
  the live spec when you need a number.
- **Reading the LIVE router is the only total source.** `POST /v1/kms/auth/login`
  is registered as `Group("/v1/kms/auth").Post("/login")` — no grep can find that
  path; only the assembled router knows it. And the route set is a function of
  deployment config (`cfg.Enabled`, plus internal gates like kms's `if kc != nil`),
  so **the spec VARIES PER DEPLOYMENT** — correctly: a deployment that does not
  mount admin does not advertise it. That is why the document is generated
  per-process at request time, not built once in CI.
- **The product axis is mechanical.** The first path segment after `/v1/` IS the
  product (`openapi.Product`), tagged onto each operation so a CLI can build
  `hanzo <product> <resource> <verb>` with no judgment. It is deliberately NOT the
  subsystem name: `clients/billing` also serves `/v1/finance/*`.
- **What the router CANNOT tell you — do not try to fix this in the generator.**
  Method, path, path params, and product are derivable; request/response schemas,
  query/header params, status codes, and auth are NOT. The router holds a
  `func(*zip.Ctx) error`; the request type is a LOCAL inside the handler
  (`var req secretPutRequest; json.Unmarshal(ctx.Body(), &req)`), and Go cannot
  reflect from a func value into its body. `cloud.Handle[S]` does not help — `S`
  is the SERVICE (service.go:90), not the payload. The ONE path to schemas is
  zip's typed ops (`zip.Get[In,Out]`), which carry the In/Out types and also
  yield an MCP tool and a CLI command from the same registry entry.
  `GetRoutes()` is a strict SUPERSET of `app.ops`, so migrating a handler to a
  typed op adds schema without changing this pipeline — and it needs no generator
  change, because `Fold` picks it up on the next run. **That registry is no
  longer empty**: 165 ops across 15 packages carry types today. See "The document
  pipeline" and "The typed migration" below.
  (Note: `cloud.Typed` is NOT a mount adapter — it is the per-request seam that
  carries the validated org, the request and the response status across the typed
  signature, which drops all three. typed.go.)
- **Catch-alls are opaque, by construction.** `app.Post("/v1/billing/*")` proxies
  to another service, so `POST /v1/billing/deposit` is NOT a route in this process
  and cannot appear. Measured on the live table: 3 products are wholly opaque
  (`bot`, `licensing`, `sentry` — the catch-all IS the product) and 12 more mix
  concrete ops with a catch-all hiding an unknown remainder.

## The document pipeline: ONE registry, N projections

A typed op is ONE registry entry with N projections. `zip.Get[In,Out]` (and its
Post/Put/Patch/Delete siblings) is the single registration every consumer reads —
the REST route, the OpenAPI operation's DETAIL, the MCP tool, the CLI command and
the generated SDK method all come from that one entry. An untyped route still
gets a route and a bare operation (method, path, product — all the router knows),
and nothing else: **no schema, no prose, no MCP tool, no CLI command, no SDK
method.** Nothing here is a second source; each stage is a pure function of the
one before it.

    handler doc comments  ──zipdoc─▶  zipdoc_gen.go  ──init─▶  zip.Describe
    zip.Get[In,Out]       ──────────────▶  zip's typed-op registry (app.ops)
                          ──────────────▶  a fiber route (so GetRoutes ⊇ app.ops)

    live fiber router  ──Live─▶  []Route   ──From─▶  Document   (shape)
    typed registry     ──Typed─▶ Registry  ──Fold─▶  Document   (detail)
                                                    │
      GET /v1/openapi.json · openapi.yaml · `<app> openapi` · Weave

- **`Spec` = `From` ∘ `Live`, then `Fold` over `Typed`** (openapi/openapi.go:489).
  `GetRoutes()` is a strict superset of `app.ops` — registering a typed op
  registers a fiber route too — so the router gives the TOTAL set of operations
  and the registry gives DETAIL for the subset that has any. One document, no
  gaps and no invention. The two are not rivals and never disagree, because one
  is a subset of the other by construction.
- **`openapi.Register` (openapi/register.go) is the reflection seam for untyped
  routes.** A subsystem that has not migrated can still DECLARE the payload types
  the router cannot derive: `openapi.Register(path, method, req, resp)` from its
  init, next to its route table, passing the zero value of the handler's own
  binding struct. The schema is derived by reflection from those very structs
  (json tags), so there is no hand-written schema to fall out of sync — change
  the struct and the document follows. **The registry cannot add an operation**:
  a registration whose route is not in the router simply never renders, so the
  document still cannot disagree with the router. Schemas are additive metadata
  on routes that exist. A duplicate registration for one `(method, path)` panics
  at init rather than letting two declarations race. Seven declarations live
  there today, all `clients/platform` (platform.go:248-254) — this is a bridge,
  not the destination; the destination is the typed op.
- **zipdoc is why the prose exists at all.** Go drops comments at compile time,
  so the build-time pass is the ONLY way a handler's doc comment, its field
  descriptions and its `Example:`/`Response:` lines reach the document.
  `//go:generate go run github.com/zap-proto/zip/cmd/zipdoc` sits in each typed
  package (15 of them); the tool walks the typed registrations, harvests the
  comments, and emits `zipdoc_gen.go` — `zip.Describe` calls that run at `init`
  and are therefore **compiled INTO every binary**. That is why it is a
  prerequisite of `build` and not of `openapi`: running it after the build is too
  late.
  - The Dockerfile now runs `go generate -run zipdoc ./...` before every build
    (Dockerfile:178). It did not, and the omission was measurable in production:
    **api.hanzo.ai served 1441 operations with ZERO descriptions** — exactly the
    binary `mk/plugin.mk` warns about. The SDK repos and the CLI read that
    document, so the prose never reached any of them either. `-run zipdoc` picks
    the directives out of `./...` by name, so a typed op added anywhere is
    covered and no unrelated generator fires.
- **Each app describes ITSELF: `<binary> openapi <file>`** (openapi_dump.go). An
  app's subset is generated from the app's OWN live router by the SAME
  `openapi.FleetSpec` the whole document is, over an app with only that subsystem
  mounted. It is never sliced out of the fleet spec by prefix — that would make
  the fleet the source and the app a derivative, which is backwards, and is
  exactly how a catch-all silently swallows a neighbour's routes. **Compose
  upward, never carve downward.** The mode lives on `Serve` because `Serve` is
  the single entry every app binary shares, so every one of them gets the target
  at a cost of zero per-app code. It writes a FILE, never stdout: a subsystem's own
  dependencies print to stdout at mount (hanzoai/commerce emits a sqlite-vec
  warning and GORM debug lines), which a `> file` redirect splices into the front
  of the document — 71 KB of invalid JSON. A writer whose output an unrelated
  library can corrupt is not a writer.
- **`openapi.Weave` composes the subsets, and its only contribution is the
  REFUSAL** (openapi/weave.go). Two apps may not claim one address, and two apps
  may not mean different things by one schema name. That refusal is not
  hypothetical: `clients/git`'s `/:org/:repo` catch-all was swallowing other
  apps' routes, and the manifest's call-graph walk is what caught it. The routing
  order in `Wire()` is load-bearing precisely because overlapping claims exist.
  A merge that took last-write-wins would produce a perfectly valid
  document describing a fleet nobody deploys — every generated SDK wrong, in a
  way no test could see. **A document that cannot be woven is a fleet that cannot
  be routed.** Weave does NOT arbitrate, because there is no policy for who
  should win: an overlap is a bug at the composition root, and the fix is in
  `Wire()`. `TestFleetIsTheWeaveOfItsApps` (weave_test.go:130) proves the
  composition against the golden, admitting exactly ONE class of difference —
  routes behind a PLUGIN's catch-all, which the monolith's router cannot see and
  the app's own binary can. `TestWeaveRefusesTwoAppsAtOneAddress`,
  `…OneSchemaNameWithTwoShapes` and `…CollidingOperationIDs` pin the refusals.
- **The document's IDENTITY is a value, not a literal** (openapi/fleet.go). Four
  producers write documents that must compare equal, so title/version/server live
  once and all four read them; an info block that differed would make two
  documents OF THE SAME API compare unequal over a title string. `Version` is the
  API CONTRACT version — `v1` forever, house law — never the build's:
  `cloud.Version` here would make every build differ from the committed golden.
- **`openapi.yaml` is a GOLDEN of the live router, and the ONE artifact cloud
  publishes.** `make openapi` writes it (`-update`); `make test`, and therefore
  CI, verifies it with the same test and no flag (`TestOpenAPIYAML`,
  cmd/cloud/openapi_yaml_test.go). Same code path both ways — there is no second
  generator to disagree with, and no way to change a route without either
  regenerating the file or turning the build red. It is serialised through JSON
  because JSON is what the document IS (the same value served at
  `/v1/openapi.json`); YAML is a rendering, and `encoding/json` orders object
  keys so the bytes are stable run to run.
- **SDK repos PULL; cloud does not push.** A stale spec does not stop at cloud —
  it ships wrong clients to four package registries. The repos read
  `openapi.yaml`, regenerate, and release on their own cadence.
  `make openapi OPENAPI_DIR=<checkout>` additionally drops the same document into
  a hanzoai/openapi checkout as `generated/hanzo.json`, where that repo
  aggregates, audits and generates from it. The drop is the same document written
  by the same run — one value in two places, not two sources of truth. It is
  named `hanzo`, not `cloud`, because the binary serves the WHOLE /v1 surface.

## The typed migration: one registry entry, or a route and nothing else

Measured at `e88ea216`, and re-measurable — do not trust these numbers past the
next few merges, run the commands. (They moved by two operations between the
branch point and the merge; that is the rate.)

    # typed ops (the generic package-level registrars; types are INFERRED,
    # so they read as ordinary calls — bracket syntax appears only in comments)
    grep -rEn 'zip\.(Get|Post|Put|Patch|Delete)\(' --include='*.go' . \
      | grep -v _test | grep -vE ':[0-9]+:[[:space:]]*//'          # 165, 15 pkgs

    # untyped: a METHOD call on a router/group value
    grep -rEn '\.(Get|Post|Put|Patch|Delete)\("/' --include='*.go' . \
      | grep -v _test | grep -vE ':[0-9]+:[[:space:]]*//'          # ~900, ~95 pkgs

The discriminator is `zip.X(` (package-qualified generic) versus `<receiver>.X(`
(method on `*zip.App`/Router) — NOT the presence of square brackets. The
published document is the honest denominator: `openapi.yaml` carries **1398
operations across 984 paths, of which 164 have a description.** The other ~1234
are route only — no MCP tool, no CLI command, no SDK method, no schema, no
prose.

The typed 15 are `clients/admin` and its eight sub-packages, plus `clients/git`,
`clients/integrations`, `clients/marketing`, `clients/plugin`, `clients/search`,
`clients/visor`. `clients/admin/core/typed.go` states the rule for that surface:
every `/v1/admin/*` route is a typed op.

**What compensates today, and how it dies.** hanzoai/openapi carries an AUTHORED
master, `hanzo.yaml`, which is the only source of request-body and query-parameter
SHAPE for the untyped majority — because those handlers are raw fiber handlers
and the registry has no Go type to read a schema off. The Rust CLI's
`genspec` (`~/work/hanzo/cli/src/bin/genspec.rs`) joins the two documents that
are each authoritative about half an operation: cloud's live table says WHAT IS
SERVED, the authored master says WHAT IT TAKES.

Its rule is **refute-only, at PRODUCT granularity**: if cloud's table has any
route under `/v1/<product>/`, cloud owns that product and its table is complete
for it — an authored operation the table lacks is not served, and is dropped. If
the table is SILENT about a product, cloud is not the authority over it (the
inference surface `/v1/models`, `/v1/chat/completions` is answered at the edge by
the gateway, not by this router), so nothing is refuted and the authored
operation stands. That rule is what retired the hand-maintained "this one 404s"
list: the `gateway` subtree drops out because the registry serves none of it, not
because a list says so.

**As ops go typed, their schemas appear in the registry document and the authored
half shrinks. When it reaches zero the master is dead.** Do NOT delete it first —
it is load-bearing for every product still untyped, and removing it ahead of the
migration silently strips request shapes from every generated CLI and SDK.

## Generated and frozen artifacts: what goes stale, and how you find out

- **15 `zipdoc_gen.go` files are COMMITTED, and `mk/plugin.mk:45-46` still says
  they are not.** They are tracked, not gitignored, one per typed package, 1:1
  with the `//go:generate` directives. The comment is stale prose, not a bug —
  but the reason they are committed IS live: the root build targets (`build`,
  `host`, `ship`, `plugin`, `monolith`) do not regenerate them, so a binary built
  from a fresh checkout by any of those paths would otherwise ship with no
  descriptions at all. Only `make openapi` (Makefile:214), the per-app
  `mk/plugin.mk build` chain, and the Dockerfile (line 178) run the pass.
  **Untrack them only after every build path regenerates them** — not before.
  zipdoc has a `-check` mode that writes nothing and errors on a stale file; no
  gate in this repo uses it yet, which is the other half of the same gap.
- **The wire freeze test must be updated in the same diff as `Wire()`.** It is a
  golden, not an invariant. A new subsystem lands RED until `frozen` names it.
  That is the design — the failure is the review prompt — but do not "fix" it by
  loosening the test.
- **Committed per-app subsets (`cmd/<app>/openapi.json`) go stale when routes
  change.** The weave gate catches it: `TestFleetIsTheWeaveOfItsApps` compares the
  composition against `openapi.yaml`, so an app whose subset no longer matches its
  routes fails there — and a MISSING subset fails immediately, naming the file.
  The fix is to re-emit: `make -C clients/<app> openapi` for one,
  `make -f mk/fleet.mk openapi-apps` for all of them. Never edit the JSON, and
  never relax the gate. The same test also LOGS `UNROUTED: <app> serves <path>,
  which the fleet routes nowhere` — reported rather than refused, because that one
  is a composition-root defect (a prefix missing from a `Wire()` entry) and the
  honest fleet document is the one without the route. Read those log lines; they
  are the early warning for a subtree the host will 404.
- **A count quoted in prose is stale the next week.** api.hanzo.ai once measured
  1467 operations / 1064 paths / 167 products against a doc still claiming
  983/692/109. Every number in this file is tagged with how to re-measure it;
  keep it that way.

## Cross-subsystem seams that are values, not places

- **The per-principal MCP plane is callable in-process.** `clients/automations`
  decomplects tool dispatch from its front doors: `dispatchTool` is the ONE core
  (resolve `<connector>_<action>` → run with a Token bound to the VALIDATED org),
  and TWO doors share it — the HTTP JSON-RPC handler (`POST /v1/automations/mcp`)
  and the exported `automations.InvokeTool(ctx, org, tool, args)`. A sibling
  subsystem that must ACT AS a caller (the Business AI guide's "do it for me")
  calls `InvokeTool` with `principal.Org(c)` — same 403 gate, per-org concurrency
  bound, one metered unit, one audit record as the HTTP door — so it can never
  exceed the caller's authority. Use this seam; never re-implement tool dispatch.
- **"Bot" is three values; each has one home and one namespace.** Do not merge
  them and do not let them share a route prefix — they did once, and the router
  resolves byte-identical patterns by first-registration with no panic (it MERGES
  the handlers, so counting `GetRoutes()` entries cannot see it), and visor's
  machine list silently answered the console's run list.
  (1) A bot RUN — a task the runtime executes on a surface — is `clients/bots` at
  `/v1/bots`. (2) A bot MACHINE — visor-provisioned compute of kind=bot plus its
  agent binding — is `clients/visor` at `/v1/compute/bots`; what it rents you is
  compute, so it nests in visor's domain. (3) The runtime SERVICE — the TS bot
  (channels/skills), never reimplemented in Go — is reached through
  `clients/runtime`, which is a TRANSPORT, not a domain: base address, identity,
  framing, cleartext policy, and the `/v1/bot/*` ops face. It is named for what it
  does, not for the host it dials, and it must never import `bots`/`coding` — each
  of those owns its own wire stub (`bots/wire.go`, `coding/task.go`) and speaks
  through the seam. That isolation is what makes the HIP-0106/HIP-0120 ZAP swap a
  seam swap instead of a rewrite.
- **Cloud owns policy; the runtime owns the run. Do not copy state you do not
  own.** `clients/bots` holds NO store. The sandbox lives in the bot runtime,
  keyed in the runtime's own tenant store, which is the only thing that knows
  whether a run is alive — so list and stop PROXY it, gated by cloud's
  principal/org. A cloud-side registry was tried and was wrong: it minted an id
  the runtime had never heard of, so it listed runs that did not exist and
  "stopped" runs that were never started. Isolation holds because the org is the
  validated one cloud sends, never a client's, and the runtime keys every run
  under `tenants/{org}/`.
- **Nouns, one owner each (2026-07-28).** IAM owns orgs and Projects
  (`/v1/iam/projects`); platform makes APPS and SITES under them and its
  `ProjectStore` is READ-ONLY (List/Get/Exists — re-adding Create breaks the
  build). `/v1/run` RESOLVES the org's default project (424 → IAM when absent),
  never mints one. Apps whose IAM project is gone are removed by the orphan
  reaper (`clients/platform/orphans.go`) — fails SAFE (IAM unreachable ⇒ reap
  nothing), one existence question per (org,project), volumes left behind.
- **An app declares storage** (`storageGb` on the platform Application): the
  deploy ensures an RWO claim `<slug>-data`, mounts it at `/data`, and forces
  `strategy: Recreate` (an RWO volume + rolling update deadlocks on
  Multi-Attach). The claim is never patched or deleted with the app. Stateful
  binaries should keep their store in a SUBDIRECTORY (`/data/<name>`) — the
  volume root carries ext4 `lost+found`, which version-sniffing stores reject.
- **No KMS URL names an org.** The tenant surface is `/v1/kms/secrets`; the org
  comes from the validated principal (for a switched-in SuperAdmin, X-Org-Id —
  the same one-predicate switch every subsystem honors). Cross-org over HTTP
  does not exist; in-process callers (which hold the master key) are the only
  cross-org readers. The org remains the STORE partition (`/orgs/<org>/…`).
- **Per-tenant KMS identity is minted, not runbooked.** On a missing
  `orgs/<org>/kms-auth/*` credential and with `IAM_SERVICE_TOKEN` set,
  `clients/platform` calls IAM's idempotent bootstrap upsert to create
  `<org>-platform-kms` (clientId==name==audience; a surprise clientId is
  refused, never sealed; the upsert must carry `cert-<brand>` or the minted app
  cannot SIGN and its tokens 500), seals both fields, and the sync proceeds.
  In-cluster IAM base resolution is `cloud.IAMBaseURL` — the split-horizon
  policy stated once (Cloudflare 403s server-side POSTs to the public issuer).
- **A customer IS an IAM user; marketing keeps no contact list.** Who to email is
  read IN-PROCESS from the embedded IAM (`clients/marketing/roster.go` →
  `iam/pkg/store.GetMailableUsers` over `clients/iam.DB()`), the same seam
  `clients/platform` uses for the IAM-owned Project — no HTTP hop to `/v1/iam`
  from inside the binary, IAM's `model.User` verbatim, read-only, masked. The org
  is `principal.Org`, which IS IAM's `Owner`, so an audience can only ever resolve
  its own tenant; `GetMailableUsers` REFUSES an empty org rather than falling back
  to the all-orgs view that `GetProjects` deliberately allows. IAM not co-mounted
  is a 503, never an empty audience — a send reported successful to nobody is the
  worse failure. An audience with no event filter is every mailable customer;
  with one, `matchCohort` joins the warehouse `distinct_id`s to that roster and
  COUNTS what matched nobody instead of inventing an address.
- **A product announcement is not its own engine.** It is a one-step sequence with
  an audience enrolled into it — `POST /v1/marketing/sequences/:id/enroll` takes
  `audienceId` where it takes `address` — so it inherits the drip engine's
  claimed-once delivery, the ONE send gate (`state.deliver` → suppression →
  `notify.Send`), and the signed unsubscribe footer. Never add a blast path beside
  `deliver`: the gate is only absolute because it is the only door.
- **Absence is only meaningful from a callee that could have said otherwise.**
  `runtime.ErrNotFound` (the operation ANSWERED "no such target") is separate from
  `runtime.ErrNotServed` (the operation does not exist). Conflating them makes a
  stop that cannot fail: a runtime without the route reports absent for EVERY run,
  so "already gone" becomes permanently true. A bare 404 is 502, never success.
- **The Business AI Guide (`clients/guide`, `/v1/guide/*`)** is the on-site launch
  checklist: a pure engine (`curriculum.go` — parse/validate/next-step/dependency
  gating over plain data) + per-org progress (`cloud.OrgStore[*Store]`) + an
  injectable auto-detect registry (`detect.go` — `acted` reads the agent action
  ledger, `analytics` probes the shared warehouse) + the agent (`agent.go` — drafts
  with `deps.AI`, executes the step's bound tool via `automations.InvokeTool`). The
  curriculum is a machine-readable contract (embedded `default.yaml`; org-custom via
  PUT replaces it) so `hanzoai/marketing` can author the full `checklist.yaml`
  against the same `Step`/`Curriculum` shape.
- **The EXPERIMENT is a composition, not a fourth engine (`clients/experiments`,
  `/v1/experiments`).** A/B testing is ONE value whatever the variant KIND (feature
  flag, ad creative, email subject, model id): the primitive owns only the
  experiment registry (definition + decision); it COMPOSES three planes it never
  duplicates. ASSIGNMENT = `flags.Assign(org,project,key,subject,props)` —
  subject→variant is a deterministic `engineEvaluate` (sha1 rollout hash), no second
  bucketing, no assignment store; create writes a multivariate flag def
  (`flags.PutDef`) and decide rewrites its weights to 100% for the winner
  (`flags.GetDef`+`PutDef`). MEASUREMENT = `analytics.Outcomes(...)` — one
  org-scoped `hanzo.events` query (the `eventsWhere` isolation invariant), no second
  event store; the analyze fold joins each subject's analytics outcome to its flags
  variant by `distinct_id`. EVIDENCE = `research.Record`/`research.List` — per-variant
  samples land as immutable `kind:"ab"` rows; significance (two-proportion z-test,
  `math.Erfc`, no dep) is a PURE function over them. `clients/campaign` runs a
  creative A/B by composing `experiments.Assign`/`experiments.Analyze` — it never
  reinvents assignment or evidence. Add a new variant KIND by putting a payload on
  the variant; the primitive does not care what it is.
- **The OSS-template compute cost is DERIVED from the compose, not a fourth ledger
  (`clients/blueprint`, `/v1/blueprint`).** A blueprint's `docker-compose.yml` is
  parsed to its SBOM (the bill of container images) and its services' CPU/memory
  footprint priced through ONE documented rate card (microdollars per vCPU-/GB-hour,
  DigitalOcean-droplet-derived + platform margin; tunable via
  `CLOUD_BLUEPRINT_UCPU_HR`/`_UGB_HR`). Sizing is the declared
  `deploy.resources.reservations`/`limits` (or legacy `cpus`/`mem_*`) else a default
  footprint per inferred class (db/cache/web/worker/other). `blueprint.EstimateTemplate(id)`
  returns `{sbom, vcpuHr, gbHr, microUsdPerHour, estCentsPerMonth}`: `estCentsPerMonth`
  is the "~$X/mo to run" the console shows; `microUsdPerHour` is the exact rate the
  deploy path meters the deploying org on via the SAME commerce spine `resource_billing`
  uses. The author royalty (`clients/authors`, `defaultShareBps=2000`) already accrues
  20% of a deploying org's metered spend — this plane only DEFINES the compute component
  of that spend from a real rate card; it never touches the ledger or the accrual sweep.
  Distinct from `clients/sbom` (CycloneDX packages INSIDE one image, keyed by digest);
  this is the bill of IMAGES a stack runs, keyed by template.

## Identity vocabulary is IAM-native

Identity is expressed ONLY in IAM-native nouns: **org, user, project, billing
account**. The word **"tenant" is banned** in cloud identifiers, strings,
comments, and filenames. Resolve org/project scope through `clients/principal`
(`principal.Org(c)`, `principal.Project(c)`) and user identity through `c.User()`
— all gateway-minted, JWT-validated values (X-Org-Id / X-Project-Id / X-User-Id,
HIP-0026); never read a raw request header for scope.

- **The one gated exception.** `clients/platform` derives customer-app Kubernetes
  namespaces, registry image refs, and quota/limit objects from a live `tenant-<org>`
  string prefix. Renaming that prefix orphans deployed namespaces + built images,
  so the literal `"tenant-"` string (and its directly-adjacent comment) is retained
  behind a `// NAMING(gated)` note in `clients/platform/k8s.go`. The surrounding
  identity vocabulary is org-native regardless; only the on-cluster string waits on
  an infrastructure migration.

## API keys are ONE noun (`/v1/keys`), and the type is a FIELD

`POST` creates, `DELETE` revokes, `GET` lists. `clients/account/account.go`.
`mint`, `issue` and `revoke` are HTTP methods, never path segments — the concept
previously had four names (`/v1/iam/mint-user-keys`, `/v1/iam/revoke-user-keys`,
`/v1/iam/keys`, `/v1/ingest/keys`) and the only honest one 404'd.

```
GET    /v1/keys                          -> {keys:[{type,prefix,key?,createdAt}]}   no secret
POST   /v1/keys   {"type":"publishable"} -> {type,key}    the key, ONCE
DELETE /v1/keys?type=publishable         -> {ok,type}
```

**Two types, and the type is the only thing that differs.** `secret` (`sk-`)
resolves to the USER, so it is session-equivalent and belongs on a server.
`publishable` (`pk-`) resolves to just the ORG, so it is safe in a browser bundle
and covers analytics, product insights and error capture as ONE key. They are two
IAM rows, so a user holds both and rotating the browser key does not revoke the API
key. An unrecognized type is REFUSED, never defaulted — handing an `sk-` to someone
who asked for a browser key is a credential in the wrong place.

**Do NOT spell a public resource under `/v1/iam`.** api.hanzo.ai routes `/v1/iam/*`
to the IAM service (ingress router `api-hanzo-ai-iam-api`), so anything cloud mounts
there is unreachable at the only host callers use: the request lands on IAM's Guard
and 401s. The tell is the body — `{"status":401,"error":"authentication required"}`
is IAM's Guard; cloud's own refusal is a 403. `/v1/iam/keys` survives ONLY as a thin
deprecated alias (same handlers, RFC 8594 `Deprecation` + `Link`) for the go:embed
console, which addresses cloud's own origin.

**A publishable key has its own resolve door.** `OrgForKey` (`auth_apikey.go`) sends
a `pk-` to IAM's `resolve-key` (org only, never a principal) and a secret key to
`get-user?accessKey` (the principal). IAM refuses a `pk-` at `get-user` BY DESIGN,
so routing every prefix to that one door — which is what cloud used to do — meant a
publishable key resolved to nobody and could not attribute a beacon. Separate
caches, because the two answers are different types and must not be confusable.
Requires `IAM_PUBLISHABLE_RESOLVE_APPS`, which is fail-closed.

**GET reads the KEY ROWS, never the user row.** The mint writes a key row; a read of
`schema.User.AccessKey` reports "no key" immediately after a successful POST. That is
the "key never listed" bug and it has recurred twice.

## Hanzo Company (`clients/company`, `/v1/company`)

The Stripe-Atlas-class incorporation + fundraising product: ONE formation state
machine per org. `machine.go` is the PURE core — a `transitions` table with a guard
per edge, `Advance(f, to)` the only mutator — so transitions, the payment gate, and
the skip path are unit-testable with no I/O. The HTTP surface is decomplected: ACTION
endpoints populate data (structure/founders/kyc/payment/documents/esign/genesis/
import), and ONE `POST /v1/company/advance {to}` runs the guarded transition.

Every external dependency is a narrow provider interface (`providers.go`) so the
machine composes them identically in prod and tests: billing → the shared
`ResourceMeter` ($999 one-time fee); documents → `dataroom.Ingest` (new in-proc
facade); cap table → `captable.*` (new in-proc facades: SetIncorporation /
AddStakeholders / EnsureShareClass / IssueShares / RecordRound); equity genesis →
a KMS-signed Hanzo-L1 anchor mirroring `clients/treasury` (honest pending when
unwired); KYC + state filing → honest stubs (no fabricated verification/filing).
Import path (already-incorporated orgs): Google Drive → data room, a Google Sheet →
captable, via the `google` OAuth provider now completed in `clients/integrations`
(token custodied in KMS; the automations `google` connector shares the same token).
Runbook: `docs/company-dogfood.md`.

## Deploy plane (`clients/deploy`, `/v1/deploy`)

Native ArgoCD-grade GitOps console over the operator-managed fleet, parallel to
`/v1/git`: each `hanzo.ai/v1` App CR IS the Application, and the plane OBSERVES the
operator's reconcile — `GET /v1/deploy/applications` (fleet list), `/{name}/tree`
(ownerRef resource tree + per-node health/sync), `/{name}/resource/{ref}` (live
manifest + desired-vs-live diff), `/{name}/logs`; `POST /{name}/rollback` pins the CR
image to a prior semver and `/{name}/sync` requests a reconcile. SUPERADMIN-only on
`c.IsAdmin()`, fail-closed; Secret nodes are never surfaced. `engine.go` embeds the argo
`gitops-engine` (`hanzoai/deploy/gitops-engine` v0.7.2, no replace) in-process for the
reconcile half behind `DEPLOY_ENGINE_ENABLED` (default off), with a prune-safety fuse.

## The index (`clients/index`, `/v1/index`)

The in-binary index, speaking the Meilisearch REST dialect so a Meilisearch client
repoints by changing one host. It replaced the standalone Meilisearch containers
(`chat-meilisearch`, `search-fts5`).

**Four different things, four names — do not merge them.** `hanzoai/search` is the
SEARCH PRODUCT (our own Meilisearch build, serving `search.hanzo.ai` and the docs
corpus). `clients/websearch` queries the OUTSIDE world. `clients/crawl` fetches it
(in-binary — see below; the standalone `hanzoai/crawl` service it used to call is gone).
`clients/index` is the storage primitive an application writes documents into and
queries back. It is NOT at `/v1/search`: that path belongs to the `hanzoai/ai` RAG
plane, whose `/v1/search/{name}` pattern silently swallowed this subsystem's
single-segment routes (`/health`, `/version` answered 404 in production while every
deeper route worked). `GET /v1/openapi.json` is what shows two owners of one path.

Tenancy is the point: a standalone Meilisearch has ONE
global keyspace behind a master key, so every consumer sharing an instance shares its
indexes — here the tenant is `principal.Org` and every query filters `WHERE org=?`,
so two orgs may both hold an index named `messages`. The credential is the org's
ordinary API key, because the JS client already sends `Authorization: Bearer`.

**The index is a term table, NOT FTS5, and must stay that way.** FTS5 is a
compile-time module and this binary links the SYSTEM SQLite so the SQLCipher codec is
real; that library ships `ENABLE_FTS3` + `HAS_CODEC` with no fts5, and the
`sqlite_fts5` build tag only affects the VENDORED amalgamation, so it is inert here.
An index built on FTS5 opens on a pure-Go build, passes its tests, and then cannot
create a single table in the shipped image. `terms` is keyed
`(org, uid, term, pk)` so a prefix query is an index range scan; it behaves the same
in every build lane. Verify any SQLite module against the production lane
(`-tags "libsqlite3 sqlite_fts5"` + `-lsqlcipher`) before designing on it.

The store is `{DataDir}/index.db`, and a rename must carry the WHOLE family: cek
keeps the wrapped data key beside it as `<path>.dek`, so moving the `.db` alone
strands the key and every document becomes undecryptable — data loss that presents
as an empty index.

## The cross-org catalog (`clients/catalog`, `/v1/catalog`)

Everything the fleet has built — hanzo, lux and zoo repos, plus every site this
deployment serves — as ONE searchable corpus. It owns no store: the rows live in
`clients/index` under the uid `catalog`, so relevance, paging and encryption at rest
are the index's. What catalog adds is the one thing a per-org index cannot express,
a corpus that spans orgs, and it does that with a SECOND corpus rather than a
weaker filter:

- `~catalog` is the published, world-readable corpus. The leading `~` is
  load-bearing: an org id is minted from a validated IAM owner claim and IAM org
  slugs begin with an alphanumeric, so no principal can ever BE `~catalog`.
- the caller's own org holds their private rows, read with `principal.Org` and
  nothing else. Another tenant never RUNS the query that would return them.

**There is no write route, on purpose.** The first cut had `PUT /v1/catalog` behind
`principal.IsSuperAdmin`; that gate is correct and unusable, because SuperAdmin is
human-only here and a cron would have needed a second fabric credential. Instead
`sync.go` reconciles the corpus in-process every hour (first pass delayed 90s so a
boot never waits on the network) from the public repos of the source orgs
(`CLOUD_CATALOG_ORGS`, default the fleet) and `projects.LiveSites`. A failed source
keeps the last good corpus — a GitHub outage must not prune the catalog to empty.

Which corpus a row lands in IS the tenancy rule: a public repo is public by
definition, our OWN org's live sites (`CLOUD_CATALOG_PLATFORM_ORG`, default `hanzo`)
are published because they are the demos a visitor is meant to fork, and every other
org's live sites land in that org's corpus. `TestSyncRoutesSitesByOrg` asserts the
routing itself, because that is where a customer's project would leak.

**Being ours is necessary to be published, not sufficient** (`gate.go`). Whose a
site is says nothing about whether it is worth showing, and for a while the public
catalog proved it: two deploy probes (399 and 480 bytes), the same scaffolding stub
under two slugs byte-for-byte (`vite`, `next`), and a mislabeled ACME landing page.
So `admit` READS the page each of our sites serves and refuses exactly three things:
an unbuilt scaffolding placeholder (matched on the page's collapsed visible text), a
page under a kilobyte that is also **inert**, and a body byte-identical to one
already admitted this pass. Inert is the load-bearing half — a 784-byte SPA shell and
a 682-byte redirect are both real apps, so "small" alone would have deleted them;
what has nothing to show is small AND loads no first-party script, style or frame and
goes nowhere. References to another host do not count, because we staple our own
analytics onto every page we serve. Refusal is DEMOTION: the row moves to the
platform's own corpus carrying `Note`, the reason, so a demo that leaves the public
lens can be explained. It fails open twice — an unreadable page is unjudged and
therefore admitted, and a pass that would hold MOST of the corpus has diagnosed the
reader, not the sites, so it is discarded whole. It does NOT catch two different
BUILDS of one design (same page, different bytes); that needs rendered comparison,
which a reconcile does not do.

`index.Reconcile` is `index.Query`'s mirror and the only in-process WRITE seam: a
full-corpus swap (upsert everything, delete what is gone) because the truth lives
upstream and a re-run must converge.

**A demo and its repo are ONE row.** Both key on `<org>/<name>` and so does the
index, so the site row used to OVERWRITE its own repo row and take the source link,
description, language and stars down with it — which is why every `kind=site` row
carried no `repo`. `corpus` folds instead: the site contributes what is LIVE (url,
title, last deploy), the repo what is SOURCE. `projects.LiveSites` carries
`repo_url` and `forked_from` out of the store, so a project that DECLARES its source
outranks the name match and fork lineage reaches `Entry.Template`. Two repos can
claim one id too (`hanzoai/ui` vs `hanzo-apps/ui`); `canonical` is a TOTAL order over
that — ours beats a vendored fork, then stars, then the repo URL — because
`sourceOrgs` is a map and the alternative is a row that changes its answer between
syncs.

**`forkable` means we can hand you a public source**, and it has to be able to say
no or it is a label rather than a filter. It was `true` on all 579 rows and read as
`c.Query("forkable") == "true"`, so the facet was `{"true": 579}` and
`?forkable=false` silently meant *no filter*. Now: a repo that is itself a fork of a
third-party upstream is not ours to hand over, a live demo with no public source has
nothing to hand over, and a declared `upstream` credit vetoes both. The query is
tri-state (`boolQuery`/`strconv.ParseBool` — set-true, set-false, unasked) and
`facet` counts both sides through the same loop as every other dimension.
`Entry.Forkable` is NOT `omitempty`: false is an answer.

**`origin` is what a row IS to you**, and it is the axis the two hanzo.app lanes
are cut on. The corpus flattened 579 rows into one list in which a curated starter
you fork FROM, a stranger's remix of one, somebody else's paid UI kit and
`luxfi/node` rendered identically — the labelling complaint underneath was an
information-architecture bug, because there was no field to ask the question with.
Four values, `template | community | third-party | product` (`origin.go`), all
DERIVED:

- the source table (`defaultOrgs`) now says both the brand a person browses by AND
  the lane, because both are facts about the org. `hanzo-templates` → starters,
  the apps orgs and `hanzo-community` → what people built, everything else → our
  own software. `hanzo-community` is listed before it has repos, so the auto-publish
  lane is fed the hour that lands.
- a live site is one of OUR starters' demos when the CURATED GALLERY says its slug
  is, read FORWARD through the three slugs the fork flow derives (`<slug>`,
  `<slug>-<variant>`, `<slug>-template`), so a new template files its own demo and
  a community app cannot fall in by spelling. Recorded lineage outranks that (a
  remix is a remix), and a declared `upstream` outranks everything.
- `fromRepo` lets GitHub's own `fork` bit override the address: a starter we
  vendored from somebody else is not a starter of ours.

`origin` is deliberately NOT braided with authorship. Origin says which lane;
**`org` says whose work it is** — the account that pays for a project, which the
tenancy boundary enforces and no request can forge. Keeping them separate is what
makes *community apps that are NOT ours* askable, which is the whole point of a
community lane. Both are faceted and both filter, plus `?template=<parent id>`
for one lineage — a facet nobody can act on is a rail that lies.

There used to be a third field here, an admin-gated `official` boolean, and it was
**deleted** (not deprecated) because it restated `org` and then disagreed with it:
the platform's own 74 demos were published by a script holding an ordinary org
token, so the gate refused them and this directory filed Hanzo's own work as
somebody else's. A patch had pinned the badge back on from an embedded 75-slug
manifest, which drifted out of agreement with reality within days of the template
rename. A second copy of an unforgeable fact can only ever be the wrong one.

**Visibility, not authorship, decides who appears** (`clients/projects/visibility.go`).
One axis owned by the publisher — `public` (default) or `private` — plus
`hidden`, the platform's subtractive moderation from admin.hanzo.ai. A row is
listed iff `public AND NOT hidden`, enforced in `LiveSites`' own query so a
consumer that forgets to filter cannot leak anything. Publishing is **ungated**
(a community you must be admitted to does not grow); going private is the paid
feature and rides the same `cloud.ResourceMeter` funded-org gate as hosting,
agents and functions, so an unfunded org asking for it gets a 402 rather than
being silently published. Moderation is the one admin-only field, and it is safe
to be one precisely because it only ever subtracts — the same shape as `Apex`'s
reserved-host denylist, never an allowlist.

**Third-party is attributed or NOT LISTED.** A fork holds somebody else's code
under one of our org headers. GitHub's org listing omits `parent`, so `credit`
spends one extra request per fork (a few dozen an hour against a listing pass of
about a dozen) and DROPS any fork it cannot name rather than showing it authorless
— the safe direction for *whose is this* is silence, not a guess. `NOASSERTION` is
GitHub failing to identify a licence, not a licence, so those rows carry the
upstream and state no terms. Live: 437 repos, 40 third-party, every one naming its
real parent.

## Starter kits (`clients/templates`, `/v1/templates`)

One embedded PUBLIC catalog (read-only; a customer's own templates are the second
layer, below), and **one template is one entry**. The shapes a
template ships in — FORMAT (`-html`/`-react`/`-bootstrap`), PAGE (folio's
about/contact/grid-3-fluid) and THEME — are `variants` inside that entry, chosen
at fork time: `POST /v1/projects/fork {"slug":"prism","variant":"react"}`.

Spending a slug per shape is what the catalog used to do, and it cost more than
tidiness: one portfolio kit held 26 rows, and because a multi-page kit's "variant"
IS its page-list index, two of those rows deployed demos that rendered a bare
column of links — byte-identical to each other. The catalog also carried the same
72 templates TWICE, under two naming generations of hanzoai/gallery (upstream
`brainwave`/`xora-react` vs Hanzo `synapse`/`prism-react`); they join 1:1 on the
gallery `id`, which is how 141 rows became 44.

`Template.Variant(id)` is the ONE resolution rule — no preference yields the
default shape, a single-shape template answers with itself — so no caller
branches on `len(Variants)`. A non-default shape carries its id into the derived
project slug (`prism` + `react` → `prism-react`) so two shapes coexist in an org;
`ForkedFrom` still records the template, because a shape is not another parent.
`TestVariantsAreOptionsNotSiblings` forbids the regression: no variant id may
also be a catalog slug.

### What a row has to carry (`catalog_test.go`)

Shape is not enough — a well-formed row can still be useless or dishonest, and
all three of these were true on live data:

- **`description` is not decoration.** `fork.go` copies it onto the forked
  project, so the 43 empty ones propagated into customers' project lists. Where a
  template has a live deploy the line describes what that deploy renders; where
  it has none it is written from the row's own `features`/`useCase`, so a
  description is never a claim about a page nobody looked at.
- **`source` names the REPOSITORY.** It used to be
  `gallery.hanzo.ai/templates/<slug>` — a page that 404s — and `fork.go` assigns
  it to `createReq.Repo.URL`, so a fork handed the builder an HTML error page as
  a git remote (and `Repo.Provider` came out `git`, because the host was not a
  forge). A variant resolves to its own repo where it has one (`prism-react`,
  `cipher-html`, `cipher-react`) and to the template's otherwise: a PAGE of a kit
  is not a repository.
- **`demo` is the template's OWN deploy, `<slug>.hanzo.app`, or nothing.** Seven
  rows advertised another template's deploy (Blocks → `forge.hanzo.app`, which
  renders "Streamline"; Loop → `blocks.hanzo.app`, which renders Bento v.3), so
  browsing a template showed a stranger's product. The one derived exception is
  the one fork.go already derives: a slug that is a reserved subdomain
  (`sites.IsReserved` — `metrics`) cannot BE a host, so its deploy carries the
  same `-template` suffix. `TestDemoIsTheTemplatesOwnHost` reads that predicate
  rather than listing labels, so the two cannot drift.

`framework` is deliberately NOT derived from the deploy: it is fork.go's build
hint and the repo is its source of truth.

### Two layers, not one visibility flag

Same shape as the cross-org catalog above, for the same reason. A customer must be able to
hold starter kits that are THEIRS — private to their org, forkable only by them —
next to the public gallery, and the public gallery is what an anonymous visitor
browses on hanzo.app. So the two live in different containers:

- the PUBLIC catalog is the embedded `catalog.json`, immutable, with **no write
  route**. Nothing a customer does can put a row in it.
- a customer's OWN templates are rows in `{DataDir}/templates.db` keyed by
  `principal.Org` — the gateway-minted, JWT-validated owner, never a request field
  (a body `org` is overwritten server-side). Every read binds that column.

An anonymous `GET /v1/templates` never touches the store at all, so a private
template cannot surface publicly by CONSTRUCTION rather than by a filter each
future reader must remember. `TestPrivateTemplateIsOrgOnly` asserts both
directions (owner sees it; another org and anonymous do not) and that the
anonymous view is exactly the embedded gallery, entry for entry.

A slug stays single-valued across both layers: publishing over a public slug is
409, so `slug → template` remains a function and no org can shadow the gallery.
Two DIFFERENT orgs may hold the same private slug — the key is `(org, slug)`.

`templates.Lookup(ctx, org, slug)` is the ONE door other subsystems read through
(`clients/projects`' fork resolves the caller org's own templates first, then the
gallery), so "which templates may this org use" is answered in exactly one place;
a fork of a private template records owner-qualified lineage (`acme/acme-portal`),
a fork of a gallery template records the bare public slug.

## Fetching the web (`clients/crawl`, `/v1/crawl`)

In-binary fetch + extract + markdown. It replaced a call to a standalone crawler
at `crawl.hanzo.svc.cluster.local:11235` — a name that had stopped resolving, which
nothing noticed because every caller is allowed to degrade: the answer engine's read
stage falls back to the ~600-char search snippet, so research answers silently
grounded on snippets and looked fine.

**`Fetch` and `Read` are different doors, deliberately.** `Fetch` is the pure network
primitive (URL in, `Page` out, no IO beyond the request) — testable with no store.
`Read` is what callers use: archive first, network second, keep what came back. One
door, so no caller has to remember to persist and no two can disagree about where
pages live.

Kept pages ride the ONE object seam the binary already has (`types.VFSClient` over
the shared S3 gateway) — no second client, no second bucket, no second credential.
Key is `crawl/<org>/<project>/<sha256(url)>.json`, and both halves of that path are
load-bearing:

- The URL is HASHED, never spliced in. A URL carries `/`, `?`, `#`, `%` and arbitrary
  unicode; embedding one lets a crafted URL walk out of its prefix into another
  tenant's, which is the whole isolation boundary.
- Scope comes from the VERIFIED principal, never the body, because it selects that
  prefix.
- Keyed by the REQUESTED url, not `Page.URL` (where it landed after redirects) —
  filing under the latter means a cache that never hits for exactly the pages that
  redirect.
- The scope segments are readable + digest, because sanitising alone is LOSSY:
  fold-to-`-` maps `a/b` and `a-b` onto one segment, and two orgs that collide share
  a corpus prefix. The digest decides identity; the readable half is for browsing.

SSRF is guarded IN THE DIALER, not by inspecting the hostname: a name check is
TOCTOU (DNS rebinding), and redirects re-enter the same dialer for free. The dialer
resolves, refuses if ANY resolved address is non-public, and dials the address it
checked. Storage failures are best-effort throughout — a store that is down costs a
cache hit, never the page.

## Releases are cut by a merge to main

`.github/workflows` is intentionally empty of CI. The image and its `v*` tags have ONE
owner, `clients/platform/release.go`: compute the next version → build → SMOKE the
pushed image → tag → roll out. The tag is a RECEIPT for a proven image, so a
change that breaks boot never reaches production and leaves no phantom tag.

The final step has ONE writer, and for a first-party service it is **universe git,
not the cluster**. `clients/paas.releaseService` REFUSES to patch the operator
`hanzo.ai/v1` App CR's `spec.image`: those CRs are declared in
`infra/k8s/operator/crs/` and reconciled by Hanzo CD with selfHeal, so a patch is
reverted on the next sync — the release would look applied and then silently roll
back. It validates first (DNS-1123 name, clean-semver via `splitReleaseImage`,
App exists in the namespace) so the refusal is specific rather than generic, and
names the remedy: **commit the tag to that file.**

So a green pipeline ends at `release tag minted (receipt for a pushed,
smoke-passed image)` followed by `release failed … reached: tagged`. That pair is
NOT a broken build — the image is real and proven, it simply has no declared state
pointing at it. Production moves when someone bumps `tag:` in
`universe/infra/k8s/operator/crs/cloud.yaml`. Four tags (`.267`–`.270`)
accumulated behind that once, with prod healthy on `.266` the whole time.

It used to write twice — a CR patch plus a `repository_dispatch` mirror at
`hanzoai/universe` — composed best-effort so the step passed if EITHER landed. Two
writers for one fact, and the composition HID their disagreement: patch fails,
mirror succeeds, cluster and git now describe different production states with
nothing reporting a problem. The mirror was also never running — it read
`UNIVERSE_DISPATCH_TOKEN`, never set on the deployment, so it failed closed on every
release. A rollout with nowhere to write is an ERROR: the image is built,
smoke-passed and tagged but NOT live, and a release that claims otherwise is worse
than one that fails.

### Site releases already have a lifecycle — do not build a second one

`clients/projects` owns the full versioned-release model for static sites, and it is
the ONE way:

- `<org>/.releases/<slug>/rel_<128-bit manifest digest>/` — immutable, content-
  addressed, and a SIBLING of the mutable `<org>/<slug>/` prefix, so neither a
  full-artifact deploy nor a project delete (both of which purge that subtree) can
  shred a release the pointer still names.
- `Store.ActivateRelease` — the flip is one atomic `UPDATE … WHERE EXISTS (release
  row)`, so it cannot point a site at a release that was never created, and two
  concurrent activations cannot leave the pointer disagreeing with whichever won.
  `MarkLive` deliberately does NOT touch `current_release`.
- `servePrefix` (`clients/projects/sites.go`) — the ONE read rule, re-validating the
  id against `releaseIDRE` before it can widen a prefix. An unrecognized id falls back
  to the legacy prefix, so there is no flag day and no migration.
- Rollback is activating an older id. Routes are already mounted on both site
  surfaces via `siteReleases`.

A parallel `clients/cd` + `clients/site` lifecycle (kind-agnostic `Target`, a
`CURRENT` pointer object next to the bundles) was built and then DELETED unmerged: it
re-implemented all of the above with a weaker pointer — a `v<N>` counter instead of a
content digest, and a plain PUT that could name a release whose row does not exist.
Its one genuinely new finding was a gap in THIS lifecycle, closed below rather than
answered with a second system.

**Retention is bounded, and it lives next to `promote`.** Releases used to
accumulate forever — `promote` wrote a new immutable prefix per distinct content,
nothing pruned them, and `DeleteReleases` only ran on project delete. Now every
promote that records a row calls `pruneReleases`, which reclaims what fell past
`keepReleases` (10 superseded, plus the live one). Retention at the one event that
GROWS the set means no sweeper, no schedule, and no second notion of which releases
exist. It is best-effort: a prune failure is logged and recomputed by the next
publish, never turned into a failed publish.

Two consequences, both load-bearing:

- **The live release is not a candidate at any depth.** It is excluded from the
  prune scan (so it never counts against the budget — a site parked on an ancient
  release keeps it), AND every prune `DELETE` re-checks `current_release` at delete
  time, so a rollback landing after the scan makes that delete match zero rows. Rows
  go first, bytes after — `promote`'s ordering run backwards — so a surviving row
  still means "the prefix is complete", and a crash between the two leaks objects
  nothing points at rather than serving a 404.
- **`activate` verifies BYTES, not just the row.** `ActivateRelease`'s
  `WHERE EXISTS (release row)` was sufficient only while nothing could prune bytes
  out from under a row. It now stats the release's `index.html` before the flip:
  missing row → 404 (no cross-tenant oracle), missing bytes → **410 Gone**. Without
  it, activating a reclaimed release returns 200 and takes a live site down.
  `ListReleases` and `PruneReleases` share one order (`created_at DESC, rowid DESC`)
  because `created_at` is second-granular and the menu a caller sees must be exactly
  the set retention keeps.

**Three transports, one trigger.** A push reaches `cloud.OnGitPush` — the
single-registrant seam, never a second CI — from the embedded git server
(`clients/git/smart_http.go`), the GitHub App (`/v1/connector/github/webhook`), and
the canonical forge (`/v1/git/webhook`, `clients/git/webhook.go`). The third exists
because git.hanzo.ai is a SEPARATE process: its pushes never touch our receive-pack,
so without that door the host we call canonical builds nothing and only the mirror
releases. Both webhook transports HMAC-verify fail-closed and drop bot-authored
pushes through the one `cloud.IsBotActor`, so a release cannot retrigger itself.
`clients/platform` is the only place that decides what a push MEANS: an app tracking
the repo rebuilds, and cloud's own upstream cuts a release. Cloud is the machine, so it calls the
release in-process and the build token is never handed to a caller. The trigger runs
BEFORE the token mint and the mirror, because a build reads from GitHub and must not
be lost to a sync outage.

`isReleasePush` is narrow on purpose: the release repo BY URL (an org does not
identify a repo), `main` only, a pinned commit only. Single-flight, because the
version is computed from existing tags and two overlapping runs would compute the
same one. Bot-authored pushes are excluded, so the release's own tag and mirror
pushes cannot retrigger it.

The org an inbound webhook belongs to comes from the App INSTALLATION id via the
`connections` row written by the install callback. Without that row every delivery is
acked `200 {"ignored":"unknown installation"}` and silently does nothing — a 200 on
that path is not evidence it worked; check for sync/build activity.

## The `hanzo` CLI targets THIS binary — one contract, one IAM login

The `hanzo` CLI (`cli/`) is the same unified binary; its control-plane verbs speak the
routes THIS process serves, authorized off a plain `hanzo login` (the IAM access token is
the final bearer fallback — no `--platform-token`). The ONE contract, no TS-Dokploy drift:

- `hanzo apps list|get`  → `GET /v1/paas/apps[/{app}]`  (`clients/paas` fleet drift board)
- `hanzo deploy <app>`   → `POST /v1/paas/apps/{app}/deploy` — a zero-downtime ROLLING
  RESTART (stamps the Deployment pod-template `hanzo.ai/restartedAt` annotation; never
  changes the declared TAG — that stays a git commit CD reconciles). `--env` picks the ns.
- `hanzo clusters list|get` → `GET /v1/clusters`  (`clients/visor`, tenant-scoped)
- `hanzo build`          → `POST /v1/runner`  (native buildkit fabric). With `--image` it
  builds a container image; with NO `--image` it reads the repo's own `hanzo.yml`
  (`binaries:` + `bucket:`) and builds the ARTIFACT lane instead — see below.

### `/v1/runner` builds ANY project, not only a Dockerfile

`/v1/runner` has two lanes, and a request is in exactly one of them:

- **image** (`image:`) → `launchDirectBuild` → rootless BuildKit → a pushed OCI ref.
- **artifact** (`binaries:`) → `launchArtifactBuild` (`clients/platform/artifact.go`) → a
  Job whose initContainers are ONE PER RECIPE ENTRY, each in that entry's toolchain image
  (`image:`, default `golang:1.26-bookworm`), sharing `/w`; then a publisher that hashes
  everything the recipe left in `/w/dist`, PUTs it to hanzoai/s3 and writes `binaries.json`
  last. Output: `https://s3.hanzo.ai/<bucket>/<owner>/<repo>/<tag>/binaries.json`, the same
  layout hanzoai/ci publishes to — one index, two front doors.

The recipe is the EXISTING `hanzo.yml` contract, extended by exactly two optional fields:
`run:` (a build command for any toolchain that is not Go) and `out:` (the glob of what it
produced). `main:` stays the zero-config Go lane. Nothing here is a second recipe format.

Why the split in the Job matters: `run:` is arbitrary shell by design (same trust as a
Dockerfile `RUN`), so it must never see a credential — the initContainers carry no
object-store env and no service-account token; only the publisher, which runs a constant
script, mounts `artifact-s3` (a KMSSecret; cloud holds no secrets grant in `hanzo-build`).
The publisher writes over the INTERNAL endpoint (`s3.hanzo.svc:9000`) and records the
PUBLIC URL, because `s3.hanzo.ai` is this cluster's own LoadBalancer and does not hairpin —
a pod dialling it times out. Its egress hole is `artifact-publish-egress` in universe,
selecting the pod label `hanzo.ai/publish=artifact`.

`/v1/paas/*` auth mirrors `/v1/runner` (`clients/platform/runner.go`): the `guard` admits a
validated principal who is SuperAdmin OR OrgAdmin, then each handler CONFINES a non-super
caller to the platform namespaces its own validated org owns (`scopedNamespaces`, keyed on
`principal.Org` — a tenant admin can never observe/restart another org's, or a platform,
app; `?org=` cannot widen it). The rolling restart needs `patch` on `apps/deployments`
(ClusterRole/cloud, universe `infra/k8s/cloud/rbac.yaml`). There is NO `/v1/apps` or
`/v1/org/{org}/cluster` CLI path — both are the TS-Dokploy contract, never served here
(404 live). `/v1/platform/*` IS served (29 paths live, projects + apps + sites) and no
longer 500s on a missing co-resident IAM store — it answers the ordinary gate
(`403 {"error":"X-Org-Id required"}` unauthenticated). The CLI still targets `/v1/paas`
for the apps board, which reads k8s directly with no IAM-store dependency.

## GTM: `/v1/campaign` orchestration → channels → connectors → analytics

The go-to-market stack decomplects a campaign from its execution. A **Campaign is a
VALUE** (`clients/campaign`: `{name, audience, content[], schedule, budget, channels[],
status}`) that SPANS channels; a **Channel is an EXECUTOR** (`channel.go`, the
`Channel` interface) it fans out to. The three channels are orthogonal and each
CONSUMES the connector plane via `integrations.TokenFor` — the campaign object never
touches a credential:

- **paid → `/v1/ads`** — `ads.LaunchPaid/PaidSpend/PausePaid` (`clients/ads/provider.go`)
  resolve the org's ad token (`meta_ads`/`google_ads`/… via `TokenFor(org, <id>,
  "access_token")`) and run the campaign on the provider. Meta is executed for real;
  fail-closed when the org has not connected (424). This is the ONLY place `/v1/ads`
  touches the connector plane.
- **organic → `/v1/publish`** (rename of `clients/social`) and **email → `/v1/marketing`**
  are DESIGNED follow-ons: register their executors the same way in `apps/wire_seams.go`
  (`campaign.RegisterChannel(campaign.NewChannel(kind, launch, spend, pause))`). Until
  wired, a fan-out records that channel "unavailable" (honest), never fabricated.

Channels are injected at the composition root (`apps/wire_seams.go`), the SAME
injected-function decoupling the coding dispatcher uses — `campaign` never imports
`ads`, `ads` never imports `campaign`. Fan-out (`launch.go` `fanOut`) is best-effort
per channel; the org (the ONLY tenant key) is passed verbatim to every executor, so a
campaign can only ever resolve its OWN org's token.

**Metrics = the ONE analytics plane, not a second store.** `GET /v1/campaign/:id/metrics`
reads the funnel from `analytics.CampaignMetrics(org, campaignID, variant, start, end)`
(`clients/analytics/campaign.go`) — an org+`utm_campaign`(+`utm_content`)-scoped query
over `hanzo.events`, org and campaign bound POSITIONALLY (same tenancy invariant as
every analytics query) — joined with each channel connector's reported spend
(`Channel.Spend`). Derived KPIs: CTR/CVR/CAC/ROAS. Honest-empty when the warehouse is
absent.

**Creative A/B composes the experiment primitive** (`experiment.go`), it does not
reinvent it: a creative A/B is an experiment whose variant = a creative (tagged
`utm_content`) and whose metric = the analytics read. The `AssignFunc`/`EvidenceFunc`
seams are wired at the root to the flags-assignment + evidence primitive; nil-safe
until it lands (single-creative honest default).

## GitHub → forge sync: every ref, one App, nothing per-repo

git.hanzo.ai is canonical and GitHub is its mirror, so an inbound push is an
*import*: it carries work the mirror received back to the canonical copy. The whole
configuration surface is below, because the shape people expect (a per-repo sync
setting) does not exist and should not be added.

**There is no per-repo and no per-ref configuration.** A repo is not enrolled, and a
ref is not filtered. `clients/integrations/github_webhook.go` accepts any ref under
`refs/`, and `Ref` stays a FULL ref (`refs/heads/x`, `refs/tags/v1.2.3`) the length of
the chain — webhook → `SyncEvent` → `sync.Event` → `GitInboundReq` → `inboundFastForward`
→ `GitPushEvent`. Tags matter here: a version is published by tag, so a filter on
`refs/heads/` left every release existing only on the mirror.

**Carrying tags needs no force, and that is what makes "everything" safe.** The
inbound advance uses a non-forcing refspec, so re-pointing a tag that already exists
is not a fast-forward and git refuses it — the canonical copy keeps the tag it
published. A branch delete is likewise never propagated (`ignored: branch delete not
propagated`): the mirror does not get to delete canonical history.

**A consumer that wants a branch cuts the prefix itself.** `strings.CutPrefix(ev.Ref,
"refs/heads/")` answers "is this a branch" and "what is its name" in one total step,
so a tag can never rebuild an app that tracks a branch (`clients/platform/push.go`),
and `refs/tags/main` is not `refs/heads/main` for the release check.

**Tenancy comes only from the installation id** in the HMAC-verified body — never a
header, never a client-controlled field. That is the whole tenant resolution:
`OrgForExternalID("github", installation.ID)`.

### Exactly one GitHub App — two is not redundancy

`Store.Get(ctx, org, provider)` keys a connection on `(org, "github")`: **one row per
org**, holding one installation id. The App's own identity is a single set of process
values, `GITHUB_APP_ID` + `GITHUB_APP_PRIVATE_KEY` + `GITHUB_APP_WEBHOOK_SECRET`
(KMS-synced, `clients/integrations/github.go`), used to mint short-lived installation
tokens.

So pointing a second App at the same webhook with the same secret does not double
coverage — it makes the two Apps contend for one row. The HMAC passes for both
(shared secret), but only the most recent install's id is stored, and a delivery from
the other App resolves to no org and is acknowledged `200 {"ignored": "unknown
installation"}` so GitHub does not retry-storm. The failure is therefore SILENT at
both ends: GitHub shows a green delivery, and nothing arrives. If the stored id and
the configured key belong to different Apps, token minting fails for every event
instead. Run one App; install it on every org whose repos should sync.

**Reading the live configuration is already a route — do not add a second one.**
`GET /v1/integrations` (`providerViewFor`) reports, for the `github` provider,
`available` (the App's creds are present in this process) and, when the org has a
connection, `connection.externalId` — **the stored installation id**. That id is the
one value that decides whether a delivery resolves, so comparing it against the
`installation.id` GitHub shows on a delivery is the whole diagnosis: equal ⇒ the event
lands; different ⇒ it is the silent `unknown installation` ack, and a second App is
usually why.

### The knobs that do exist

Process values, all with working defaults — none of them selects *what* syncs:

- `GITHUB_APP_ID`, `GITHUB_APP_PRIVATE_KEY`, `GITHUB_APP_WEBHOOK_SECRET`, `GITHUB_APP_SLUG`
  — the single App identity; absent ⇒ the plane reports itself unconfigured.
- `GITHUB_API_URL` — API base, for GitHub Enterprise.
- `GITHUB_IMPORT_CONCURRENCY` — parallel repo imports on first connect.
- `GIT_MIRROR_OUT_CONCURRENCY`, `GIT_MIRROR_OUT_TIMEOUT` — the outbound leg.
- `GIT_SSH_HOST`, `GIT_SSH_ADDR`, `GIT_SSH_HOST_KEY` — SSH front door; the host key is
  KMS-sourced and MUST be set when replicas > 1, or each replica presents its own.
- `GIT_SYNC_ACTOR` — the actor recorded for a sync-initiated write.

**What genuinely is per-repo is a different plane**, and naming it keeps the two from
being confused: `/v1/git/repos/:name/*` (`clients/git/subscriptions.go`) holds a
repo→Slack-channel subscription and a repo→downstream mirror target. Those are
reactor config, org-scoped like every repo route. The code index is also per-repo and
indexes the default branch only — a feature-branch push is skipped so it cannot
clobber the canonical index. None of these decide whether a ref syncs.

## The money plane runs locally, and `make e2e` proves the prepaid cycle

`commerce` is co-resident (`apps/commerce.go` → `commercemod.Embed` on cloud's own zip
app), so the billing surface, the ledger, and the gate are all one process. Two facts
about running it that are easy to get wrong in opposite directions:

**At-rest posture is a CAPABILITY question, answered once.** commerce's per-tenant money
stores open a concurrent read pool AND a serialized write pool on the same file, which
needs the LIVE libsqlcipher codec — the pure-Go codec envelope is single-writer and
cannot serve that shape. So `commerceMasterKey` gates on `sqlitedrv.CodecLinked()`, the
same predicate `cek.EnsureDevKey` uses:

- codec linked (the image: `CGO_ENABLED=1 -tags "libsqlite3 sqlite_fts5"`) ⇒ inject
  cloud's master key; commerce encrypts, and its own `resolveMasterKey` still fails
  closed if the key is absent, so production can never quietly write plaintext.
- not linked (`make build`, which is `CGO_ENABLED=0`) ⇒ hand over nothing, and commerce
  opens its documented zero-config unencrypted dev store.

Injecting regardless is not a stricter posture — on a pure-Go build it is a hard refusal
from `resolveDEK`, `Mount` never reaches `transport.SetApp`, and every S2S billing read
then falls through to the network and DNS-resolves the in-process placeholder. That is
the failure `clients/commerce/transport` documents: balance reads that answer
"Insufficient balance" on funded accounts, with DNS named as the cause.

**The gate only enforces on a kind that costs something.** `ResourceMeter.Gate`
short-circuits to allow for `costCents <= 0`, and most per-kind fees default to 0, so a
suite that does not price a kind proves nothing about billing. `e2e/run.sh` prices one
(`CLOUD_TRACKER_FEE_CENTS`) and passes the SAME number to the suite as
`E2E_TRACKER_FEE_CENTS`; spec 136 refuses to run at 0 rather than passing emptily.
`tracker` is the seam because its gated create depends on nothing but the local store —
a 402 there is the billing gate, not object storage or an exec runtime answering first
(both of which precede the gate on the deploy and invoke paths).

**A customer org arrives funded, by policy.** A new customer org holds the $5 starter
credit (`commerce billing/credit`.StarterCreditCents — the same "$5 free credit" the
developer plan advertises, a non-cash TRIAL grant tagged `starter-credit` and spendable
on non-premium metered usage only). That is why the local suite can exercise the funded
path without seeding a balance: it is the real self-service path, where a fresh org can
buy work the moment it signs up. Note the billing SUBJECT differs by org kind — a
customer org pools at the org (`acme`), while the brand org resolves per-user
(`hanzo/z`), which is `principal.Subject` → `account.Payer`, not an inconsistency.

What `make e2e` now holds: an org below the fee is refused **before** the work runs and
is not charged for the refusal; an org that can cover it is admitted and debited exactly
the fee, with exactly one usage row; draining the balance re-arms the refusal (which is
what proves the debit is real rather than cosmetic); and one org's spend never moves
another's ledger. The specs are `describe.configure({mode:'serial'})` — not by
preference, but because they all move the same balance and the suite is otherwise
`fullyParallel`.

## Encryption at rest: cek is the gate, and per-principal binding is not done yet

`cek.Open` is the ONE encryption-at-rest gate — ~50 stores, plus IAM's identity store
(`clients/iam.openStore`, which previously opened through `iamserver.OpenSQLite` and
left `iam/iam2.db` beginning with the literal `SQLite format 3` magic). If you add a
store, open it through cek; if a store is not in the envelope it has no `.dek` sidecar
beside it, and that absence is the check worth running on any new data dir:

    find $DATA_DIR -name '*.db' -printf '%P\n' | while read -r r; do
      printf '%-40s dek=%s\n' "$r" "$([ -f "$DATA_DIR/$r.dek" ] && echo yes || echo NO)"; done

**What cek binds today, and what it does not.** The KEK derives from a random per-file
id, NOT from the principal:

    KEK = HKDF-SHA256(master, lp("global") || lp(hex(fileID)))

`PrincipalOrg` and `PrincipalUser` — which `hanzoai/sqlite` provides and `commerce`
uses — appear ZERO times in cloud; `const principalType = sqlitedrv.PrincipalGlobal`
is the only one. So confidentiality between orgs DOES hold (every file has its own
random DEK under its own KEK, and one org's key cannot read another's file), but there
is no BINDING: `OrgDB` knows the validated org slug and discards it one call later at
`openOrgDB → cek.Open(path)`. A `{db,.dek}` pair is therefore valid in any org's
directory, so a PV-write adversary could swap two of our own stores between tenants.
cek's header names this as a deliberate non-goal; it stops being one the moment
tenant-isolation-under-node-compromise is in scope.

**The shape of the fix, when it is taken up.** Bind BOTH principal and file, so the
per-file KEK survives:

    KEK = DeriveKey(master, PrincipalOrg, orgSlug + "/" + hex(fileID))
    AAD = PrincipalAAD(same)

(`SanitizeOrg` guarantees the slug has no "/", so the id stays injective.) It cannot be
a flag day: every existing sidecar is wrapped under the legacy global derivation, so
open must try the principal-bound derivation, fall back to legacy on unwrap failure,
and rewrap the sidecar on success. That is safe because rewrapping touches only the
sidecar — the DEK and fileID never change and no page is rewritten, the same property
master-key rotation already relies on — and because a half-migrated fleet reads either
form. `cek.Open` grows a principal parameter (~50 call sites pass an explicit Global).

**Still outside the envelope:** `tasks/_/default.db`. `hanzoai/tasks`'s `EmbedConfig`
has no key field, so that one is an upstream change, not a cloud one.
