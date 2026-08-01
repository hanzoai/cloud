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
- `docker run -p 8080:8080 ghcr.io/hanzoai/cloud:vX.Y.Z` (pin a released tag)
- The `hanzo` CLI is NOT built here — it is the Rust binary in `~/work/hanzo/cli`
  (`curl hanzo.sh` · `brew install hanzoai/tap/hanzo`). This module serves `/v1`
  and ships plugins; it does not ship a CLI. See "The `hanzo` CLI" below.
- Build in MODULE mode only: `make build` / `GOWORK=off go build <named target>` —
  never workspace mode (see "Build & module graph" below). `make build` is the
  light host; `make plugin APP=<x>` is the one app you are editing; `make ship`
  is the release layout (host + the one multi-call binary — two links, not 108).
  Do not run `go build ./...` here — it links 100+ binaries at ~4.5 GiB each, and
  `make plugins` was DELETED for exactly that reason (62c7f52d).

## Key entry points
- `cmd/cloud` — the light host (server binary + ENTRYPOINT) · `plugin/<app>` — one binary per subsystem · `webui/` — embedded console
- `manifest/apps.go` — the app set; the host mounts a plugin per entry (the old
  `apps/apps.go:Wire()` composition root was deleted with the mega build, 22f4fc64)
- `deps.go` / `cloud.Deps` — process-wide handles · `apps/<name>/` — every subsystem
- `openapi/` — the document pipeline: the spec is a projection of the live router,
  and `openapi.yaml` at the root is a GOLDEN of it (written by `make openapi`,
  verified by `make test` — not a second source)
- `manifest/apps.go` — hand-authored source of truth; what `cmd/cloud` knows about the fleet

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
| `/v1/connectors` | Custody: per-user BYO external accounts | `apps/integrations` (both planes; user scope) | Shipped — 8 ops |
| `/v1/channels` | Transport: portable message envelope, DM pairing, send + inbox | `apps/channels` | Shipped — 8 ops |
| `/v1/sync` | Data: bidirectional sync engine | `apps/sync` | Shipped — 7 ops |
| `/v1/automations` | Workflows: flows/runs, goja piece runtime | `apps/automations` | Shipped — 20 ops |
| `/v1/flow` | Hanzo Flow: visual AI workflow orchestration (typed passthrough to the hanzoai/flow service; workflows CRUD + runs, org-scoped via the product's projects; the rest of the 87-path authored intent stays refused in `apps/flow/typed_wire_test.go`) | `apps/flow` | Shipped — 8 ops |
| `/v1/engine` | Hanzo Engine: the serving runtime behind Hanzo's models (typed passthrough to the hanzoai/engine deployment's management plane — model table + load state, host/GPU inventory, reachability; inference stays on ai's metered /v1 door; the cluster-manager authored intent and all shared-runtime mutations stay refused in `apps/engine/typed_wire_test.go`) | `apps/engine` | Shipped — 4 ops |
| `/v1/registry` | Hanzo Registry: management plane over the running registries — oci.hanzo.ai (distribution, IAM token auth) + pkg.hanzo.ai (verdaccio); org-namespace listings + pull-token mint; control-plane only, the OCI wire stays on oci.hanzo.ai; Harbor-shaped authored intent stays refused in `apps/registry/typed_wire_test.go` | `apps/registry` | Shipped — 6 ops |
| `/v1/auto` | Hanzo Auto: durable workflow automation (typed passthrough to the hanzoai/auto v2 service; flows CRUD + publish + durable runs on the tasks plane + piece catalog, org-scoped by the product's gateway-header contract; the rest of the 50-path authored intent stays refused in `apps/auto/typed_wire_test.go`) | `apps/auto` | Shipped — 11 ops |
| `/v1/bots` | A bot RUN on a surface | `apps/bots` | Shipped — 4 ops |
| `/v1/compute/bots` | A bot MACHINE (kind=bot + agent binding) | `apps/visor` — NOT `apps/bots` | Shipped — 5 ops |
| `/v1/tasks` | Durable engine | `apps/tasks` | Shipped — 11 ops |
| `/v1/machines` `/v1/gpus` `/v1/fleet` `/v1/clusters` `/v1/k8s` `/v1/compute` | Compute: provisioned + BYO machines, GPUs, k8s clusters | `apps/visor` (+ `apps/fleet` registry) | Shipped — 33 ops |
| `/v1/cloud` | Cloud accounts: link DO/AWS/GCP/Azure, discover native k8s clusters, fold into the fleet | `apps/venue` | Shipped — 5 ops |
| `/v1/blueprint` | Cost: OSS-template SBOM (compose→images) + compute-cost estimate | `apps/blueprint` | Shipped — 3 ops |
| `/v1/templates` | Starter kits: ONE entry per template, shapes as variants | `apps/templates` | Shipped — 2 ops |
| `/v1/iam` | Identity: users, orgs, roles | `apps/iam` | Shipped — opaque (5 wildcards relaying a nested app of 94 ops; see below) |
| `/v1/kms` | Secret custody: sealed secrets | `apps/kms` | Shipped — 7 ops |

`/v1/bots` and `/v1/compute/bots` are two nouns with two owners; the row above
pairs each with the package that REGISTERS it. Pairing `/v1/compute/bots` with
`apps/bots` is the merge "Bot is three values" (below) exists to forbid.

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

**`visor` is an agent name, not a query surface.** `apps/visor` OWNS the
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
  `hanzoai/deploy/gitops-engine` (apps/deploy uses its `pkg/utils/kube`); NO
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
by design (kms, flags, x402, plugin/kmsreseal, finance). Bundle-embed
tests (apps/tasks/ui) need `make deploy-ui` first (real bundle is gitignored).
(`apps/git` was in that list and no longer belongs: measured green under exactly
that posture — `TEST_ENV` dev key, `-tags sqlite_fts5`, `CGO_ENABLED=0` — in 18s.)

**Under cgo, `sqlite_math_functions` is a COMPILE-TIME REQUIREMENT, not a
preference.** `hanzoai/base` declares a deliberate compile error without it
(`base/core/sqlite_math_required.go`, `//go:build cgo && !sqlite_math_functions`):
its search layer emits SQL calling `acos/cos/sin/radians/sqrt`, which the cgo
sqlite has only behind that tag, so base refuses to build a binary whose SQL
surface is smaller than the code above it writes against. **47 of cloud's 306
packages reach `base/core`** (apps/git, apps/agents, apps/billing, apps/base, …
plus their `plugin/<app>` mains), so a cgo build without the tag does not compile
them. The Dockerfile carries it; the Makefile is `CGO_ENABLED=0` so it never
needed it; `hanzo.yml`'s two RAW-go steps carried neither, and that is how they
drifted — `go-vet` sets no `CGO_ENABLED` (the toolchain default is 1 wherever a C
toolchain exists, which the CI runner provisions) and `go-unit` sets it to 1
explicitly, so both failed to BUILD those 47 packages and reported
`[build failed]` where a test run was expected. Both now pass the same
`-tags "sqlite_fts5 sqlite_math_functions"` the image builds with. Re-derive the
count, never trust it: `go list ./... | xargs -P12 -I{} sh -c 'go list -deps {} |
grep -qx github.com/hanzoai/base/core && echo {}' | wc -l`.

STILL DIVERGENT, and a decision for the owner rather than a patch: `go-unit`
declares no `CLOUD_KMS_MASTER_KEY_REF`, while `make test` injects a dev key
(`TEST_ENV`) precisely so the suite has one dev posture instead of a copy per
package — and `hanzoai/ci` exports only GIT_TOKEN / S3 / registry creds into the
step, never that key. So every cek-backed package still fails there for want of
it (measured: `apps/code`, 6 tests, "cek: CLOUD_KMS_MASTER_KEY_REF is required").
The fix is one of two shapes and both are policy: give CI the dev key, or route
the step through the Makefile so the posture is declared once. Copying the key
into `hanzo.yml` would make it two declarations, which is the drift above again.

Store-heavy subsystem tests are fsync-bound, not CPU-bound. A mount opens its own
SQLite stores, so a test that mounts several subsystems commits many times, and
`t.TempDir()` under `/tmp` puts every commit behind the ext4 journal — on a box with
a concurrent build the same mount that costs milliseconds idle costs ~90s, at ~0%
CPU, blocked in `jbd2_log_wait_commit`. Point `TMPDIR` at tmpfs to measure the real
cost: `TMPDIR=/dev/shm/t GOWORK=off go test -p 1 ./apps/guide` runs the eight-seam
cross-subsystem harness (`apps/guide/drivehome_e2e_test.go`) in under a second.
Prefer one package per `go test` invocation regardless: `./...` links every main
package at once (`cmd/cloud` alone links >6GB).

## One host: `cmd/cloud` links the router, `plugin/<app>` links each subsystem

The app count is `len(manifest.Apps)` — 111 at this writing
(`grep -c '^\s*{Name: ' manifest/apps.go`). Treat every absolute below as a
measurement with provenance, not as a live count; re-measure before quoting one.

There is NO fused binary. The mega link that once dominated a release — one
binary that imported `apps` and linked every subsystem graph into a ~3108-package
monolith — is GONE (deleted at 22f4fc64 with `apps.Wire()`). `cmd/cloud` IS the
light host now, and the ENTRYPOINT the image ships: it links `zip`, the generated
`manifest`, and the light `webui` console embed and stops (**~399 packages**,
~28 MB, sub-second link), mounts each app as a `zip.Plugin`, and starts a child
on the FIRST REQUEST that reaches its prefix. An app nobody calls costs a route
entry, not a process. The apps that own a listener or a background loop
(`manifest.App.Eager`) start WITH the host instead.

Each subsystem is its OWN binary at `plugin/<app>`, linking only that app's graph
through `cloud.Serve` — never the fleet. `ls cmd/` shows exactly `cloud/`; `ls
plugin/` shows the ~116 per-app + tool dirs. A dedicated plugin binary is ~40 MB
of which ~35 MB is the core every plugin also links; that duplication is the
deliberate price of never linking the fleet union into one mega binary again.

**The host is the default.** `make build` (= `make cloud`) builds it; `make plugin
APP=<x>` builds the one app you edited; `make ship` builds the host + one binary
per app. The image ships `/cloud` (ENTRYPOINT) plus one `/plugins/<app>` beside
it, and the host resolves each plugin as a sibling file (`manifest.App.Plugin`).

The host is the FRONT DOOR, so it owns what no plugin can: it serves the
white-labelled console at `/` (the light `webui` leaf, mounted last so every app
prefix wins), threads the deployment's `--brand/--domain/--data-dir/--iam-issuer`
flags to the children as `CLOUD_*` env, and SCOPES CREDENTIALS — it scrubs the
KMS root key from its own environment and hands it to the `kms` broker child
alone (see the credz section).

The host knows three facts per app and no more — name, prefixes, eager-or-lazy —
and they live in `manifest/apps.go`, the hand-authored source of truth. `make
generate` runs `plugin/gen-app-cmds`, which reads `manifest.Apps` ONCE and
scaffolds a `plugin/<app>/main.go` for any app missing one, validating the two
are in bijection (every app has a main, every main is an app) so neither drifts.
Prefixes
come from, in order: the `PluginSpec` call's own arguments, a declared
`Prefixes:` field, then the absolute paths the app's package registers — read by
walking the call graph from the entry's Mount function (per FUNCTION, not per
package: one package may back two entries — `apps/account` did until the
`account-bridge` retirement — and a package-wide scan gives each the other's
paths). Both registration forms are read: `app.Get("/v1/x", h)` and the
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
every generated `plugin/<app>` is a valid plugin with no code of its own. Without
that, each child binds cfg's fixed `:8080/:9653/:9090`, the host never sees it
listen, and all but the first die on "address already in use".

CI pins both properties from `hanzo.yml`: `generated-current` re-runs the
generator and fails on a dirty tree; `host-is-light` fails if `cmd/cloud`'s import
graph reaches `apps` or any `apps/*`.

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
- **Reload is SuperAdmin-gated, and audited BEFORE it acts.** `apps/plugin`
  mounts four routes (apps/plugin/plugin.go:78-81): list, reload, enable, disable. Every
  mutation is SuperAdmin-gated and written to the hash-chained audit trail BEFORE
  it is reported as done; a deployment with NO durable audit store REFUSES the
  operation rather than performing an unrecorded one (apps/plugin/fleet.go:154-155).
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
  subsystem — exactly one process. `cmd/cloud` links no store and brokers nothing —
  and no longer passes the key down: it scrubs it (see host mode below).
- **Who asks**: every other `cloud` process, at the top of `Serve`, before
  `LoadConfig` and before any store opens.
- **Identity comes from the launcher** (`credz/launch`, stdlib-only leaf). The
  launcher stamps `CREDZ_TOKEN=<app>:<hex hmac-sha256(secret, app)>` into that
  ONE child's `zip.Plugin.Env` at spawn; the child presents it; the broker opens
  it with the secret it holds and gates the result on `manifest.Apps`. Claim and
  proof are one variable, so neither half can be recombined with another's. Two
  spawn sites, both per-plugin and never `os.Environ()`: `cloud.PluginSpec` (the
  launcher *is* the broker, secret minted in-process and never emitted) and
  `cmd/cloud` (mints it, stamps every child, hands `CREDZ_LAUNCH_SECRET` AND the
  root key to the `kms` child alone). `credz.Boot` reads the token once and unsets it.

  This replaces reading the peer's argv out of `/proc` (#51). `SO_PEERCRED` is
  kernel-authenticated for pid/uid but **argv is not** — a process picks its own
  `argv[0]` at `execve`, so any same-uid process could present itself as any app
  and be handed that app's bundle *including the root key*. `SO_PEERCRED` stays
  for the two things it can do: the uid check, and the pid in the audit line.
- **The limit, stated honestly**: the token is in the child's environment, which
  the same uid can read at `/proc/<pid>/environ`. So the cost of impersonating an
  app went from *nothing* to *first steal a live peer's token*, and a stolen token
  buys only the app it was stolen from — a boundary against accident and casual
  forgery, **not** against a peer that reads its neighbours. A real same-uid
  boundary means the socket becomes the credential (launcher pre-connects, passes
  the fd as an `ExtraFile`), which is a change to `zip`'s spawn contract.
- **Host mode scopes credentials** (`cmd/cloud`, the deployed entrypoint): the
  host does NOT call `credz.Boot` — importing `credz` would drag `cek` →
  modernc/sqlite + sqlcipher into the ~400-package host whose whole point is being
  small — so it does the scrub itself with the stdlib `credz/launch` leaf.
  `stampAndScrub` reads `CLOUD_KMS_MASTER_KEY_REF` and `os.Unsetenv`s it from the
  host's OWN environment (zip builds every child's env from `os.Environ()`, so a
  key left here reaches every child), stamps each child its scoped `CREDZ_TOKEN`,
  and re-injects the root key onto the `kms` broker child's `Plugin.Env` ALONE.
  Every generic child comes up with a token and NO root key and must ask the
  broker — the boundary, now the default entrypoint (`cmd/cloud/main_test.go`
  pins it: a dns child's env has `CREDZ_TOKEN` and not `CLOUD_KMS_MASTER_KEY_REF`).
- **Scope is derived, not configured** — the manifest names every app, the store
  holds every secret, and the path is built from the app the launcher stamped:

      /orgs/{adminOrg}/svc/_shared/{NAME}   every app
      /orgs/{adminOrg}/svc/{app}/{NAME}     that app only

  `{NAME}` is the environment variable the app already reads, and `credz` reads
  env `default` — the store requires `env` on every write, so provisioning is:

      POST /v1/kms/orgs/{adminOrg}/secrets
      {"path":"/svc/ai","name":"CLOUD_AI_API_KEY","env":"default","value":"sk-…"}

  No second registry and no code change to add a credential. The `billing`
  process is never handed `/svc/ai`: the path is built from the app the launcher
  stamped, so a peer cannot spell a path — only present the token for the one it
  was started as.
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

`apps/<app>/Makefile` is two lines — `APPS := <name>` and
`include ../../mk/plugin.mk`. Everything an app can be asked to do lives in that
one included file: `generate` (zipdoc lifts handler prose into `zipdoc_gen.go`;
a prerequisite of `build` — mk/plugin.mk:62 — because it is compiled IN, so
running it after the build would be too late), `build` (its own lean binary into
`./bin`), `test`, `vet`, `openapi` (its own spec subset; `openapi: build`, since
a spec generated from a stale binary is a lie), `clean`, `help`. A target written
once per app would be one place per app for them to disagree, and nobody edits a
hundred files at once. `clean` removes binaries only: `plugin/<app>/openapi.json` is
a committed artifact, like the fleet's `openapi.yaml`, and clean removes what a
build wrote, not what a build publishes.

**The per-app path is the one that has always regenerated zipdoc**, and the root
build targets do NOT (`build`, `host`, `ship`, `plugin`, `monolith`). The only
root target that runs it is `openapi` (Makefile:214); the Dockerfile carries its
own standalone pass at line 178. See "Generated and frozen artifacts" below —
this asymmetry is still live, and it is why the 15 `zipdoc_gen.go` files are
committed.

Both invocations work — `make -C apps/tasks openapi` from the root and
`cd apps/tasks && make openapi` — because `mk/plugin.mk` derives every path
from the including Makefile's own location, never from the caller's cwd. That is
what makes the OSS/private split a move rather than a rewrite: an extracted
`apps/<app>` + `plugin/<app>` + `mk/` keeps the paths intact.

`APPS` is a list and is never inferred from the directory name — four packages
are not named after their app (`zt`→zero-trust, `eval`→evals, `auditlog`→audit,
`plugin`→plugins) and `apps/account` backs two mounts. Three apps (authz,
licensing, metrics) are external modules with a `plugin/<app>` and no source
directory here; `mk/fleet.mk` runs them through the same recipe by name.
`mk/go.mk` is the toolchain contract every includer shares (GOWORK=off, TMPDIR on
disk, `-p=2`, the dev KMS key, the FTS5 tag).

## Framework doctrine

One way to do everything. Composable, orthogonal, DRY. A new subsystem is a
package under `apps/<name>` that obeys these seams — nothing more.

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
  `apps/apps.go` carries a standing comment where `apps/o11y` would be
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
    releases — and FAILS the build if a declared plugin has no `plugin/<name>`,
    rather than at a pod's first boot. **Unlinking a subsystem means building it
    somewhere else, not just deleting the import.** This is only for `PluginSpec`
    apps: under `cmd/cloud`, every OTHER app has no dedicated binary in the image
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

- **The public `/v1/openapi.json` is the HOST's, and it is the WEAVE — not a live
  router** (`cmd/cloud` `spec()` → `openapi.MountFleet`). `openapi.Mount` above is
  right for a process that IS the API; the deployed front door is not one. The
  light host mounts no subsystem, so reading its live router describes 113 proxy
  prefixes and a console catch-all — and mounting the fleet to answer a public GET
  is exactly the cost laziness exists to avoid. Left unclaimed the path was not
  unrouted but MISrouted: it fell to ai's bare `/v1`, and api.hanzo.ai published
  the ai child's own 8-path document as the whole API, 200 OK, to every SDK
  generator that read it. The host answers from `plugin.Spec` — the same committed
  subsets `surface-check` regenerates — through `openapi.Fleet`, the same
  composition that WRITES `openapi.yaml`. So the served bytes and the artifact are
  one document by construction; `cmd/cloud/openapi_test.go` asserts exactly that
  (byte equality) and that a plugin never answers the door.

- **The spec IS the router.** `openapi.Live(app)` reads
  `app.Fiber().GetRoutes(true)` — fiber's own filter drops `Use()` middleware —
  and every other function in `openapi/` is a pure function of that `[]Route`.
  There is no HAND-MAINTAINED spec and no second route registry. (`openapi.yaml`
  at the root is a checked-in GOLDEN — written by the same code path that serves
  the live document, verified on every `make test`. It is a rendering, not a
  source; `openapi.Register` adds a registry of BODIES, never of routes.) The
  drift guard is `openapi/weave_test.go` (`TestFleetIsTheWeaveOfItsApps`): it
  weaves every app's own subset (`plugin/<app>/openapi.json`, each emitted by that
  app's own binary) into the fleet document and requires it to equal `openapi.yaml`
  byte for byte — every live route appears, and two apps cannot claim one path.
  There is no fully-mounted binary left to read; it is the only test whose failure
  means the document lies. The test LOGS its size and pins only the bijection, so never quote that
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
  subsystem name: `apps/billing` also serves `/v1/finance/*`.
  - **A tag's DESCRIPTION is the owning package's synopsis** (`openapi.Synopsis`,
    openapi/synopsis.go). The owner is read from the app's own composition root —
    `plugin/<app>/main.go` imports exactly the package it mounts — because four
    apps are not named after their package (`audit`→`auditlog`, `evals`→`eval`,
    `plugins`→`plugin`, `zero-trust`→`zt`), so a name-derived guess is right 107
    times and silently wrong 4. (It was wrong 5 while `apps/account` backed a
    second app, `account-bridge`; reading the import is why that retirement cost
    nothing here.) It is the comment that OPENS `Package …`, not go/doc's
    first-file fallback: twenty packages open their alphabetically-first file with
    a note about that file (`actions.go — the two GitOps write actions`) and state
    the real package doc in `<name>.go`, so the fallback would publish a file note
    as the `deploy` product's description.
  - It is computed ONCE, when an app describes itself, and stamped into that app's
    subset as `info.description` (describe.go); the weave lifts the tag prose off
    the subsets it is already reading rather than looking the mapping up a second
    time in a second process. The fleet identity (`fleetInfo`) stays the fallback
    for a package with no doc, and the weave treats a part carrying it as having
    said nothing. **106 of 112 apps** have a package doc; the tag NAME is never
    conditional on one — the list stays a function of the document's operations,
    so a consumer enumerating products loses none.
  - **The owner is the app that answers the product's ROOT** — the shallowest path
    under `/v1/<product>` anyone serves (`prose`, openapi/weave.go). Sharing a
    product is the ORDINARY case: `/v1/plans` is the plan catalog with two rows
    kept by commerce, `/v1/s3` a provisioned add-on whose bucket data plane is
    storage, `/v1/search` and `/v1/vector` the same shape. Reading "two claimants"
    as ambiguity silenced those four products though none was ambiguous — depth
    already says which app the product IS and which merely has routes inside it.
    Where nobody is alone at the root the answer is still SILENCE: `/v1/finance`
    is billing at `/v1/finance/balance` and treasury at `/v1/finance/accounts`,
    neither above the other, so picking one would publish a coin flip as a fact.
  - Still blank, measured, and each for a stated reason — **6 of 149 tags**:
    `finance` (no app answers its root); `authz`, `licensing`, `metrics`, `logs`,
    `traces` (root owner mounts a subsystem in ANOTHER MODULE, so there is no
    package here to read). The upstream modules do carry package docs, but
    maintainer-voiced ones ("the native, prometheus-free time-series store"), and
    publishing those as a product description would be worse than silence — the
    remedy is a customer-facing package doc in hanzoai/{metrics,authz,licensing}
    plus a Synopsis that can reach a mounted module, not a string invented here.
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
  and cannot appear. Re-measured over the committed subsets (145 `/v1` products in
  `plugin/*/openapi.json`): 3 PRODUCTS are wholly opaque — `dns`, `licensing`,
  `sentry`, where the catch-all IS the product — and 20 more mix concrete ops with
  a catch-all hiding a remainder. Two names have left the first list since it was
  written, both for the same reason: a SECOND app publishes concrete paths for
  their product (`plugin/bot` beside `plugin/runtime`'s catch-all; `plugin/account`
  beside `plugin/iam`, contributing `/v1/iam/keys` and `/v1/iam/onboard`). So
  opacity is a property of a SUBSET before it is one of a product — measure the one
  you mean. Three subsets are opaque end to end, and two of them (`plugin/tasks`,
  `plugin/iam`) publish exactly a bare noun plus one wildcard.
- **`plugin/iam` is the extreme case of an opaque subset** — see
  "apps/iam (0 of 25, and why)" below. It publishes 35 operations, every
  one of them a method on one of five `app.All` wildcards relaying
  `iamserver.Handler(db)`: github.com/hanzoai/iam's ENTIRE standalone zip app —
  94 typed ops of its own — adapted to net/http and hung on a wildcard. The
  opaque class therefore has two shapes, and the difference decides what a fix
  even looks like: `bot`/`sentry` proxy to ANOTHER PROCESS, where the
  route table is genuinely not in this binary; `iam` — and `licensing`, whose
  external Mount (hanzoai/licensing mount.go) hangs its own net/http mux on one
  wildcard through `zip.AdaptNetHTTP` — proxies to a nested app IN
  this process, whose 94 ops each already carry a `WithSummary` and are already
  projected — at that nested app's own `/.well-known/openapi.json` and `/mcp`,
  which cloud's document does not read. So iam's 25 undescribed operations are
  not an absence of knowledge, they are an absence of COMPOSITION.
- **Opaque does not always mean UNKNOWABLE, and `tasks` is the case that shows
  the difference.** Its whole product is `/v1/tasks` (a 307 to `/v1/tasks/`) plus
  one `/v1/tasks/*` relay, so it publishes 28 operations and describes none —
  but the remainder behind that relay is not off in another service. It is 64
  operations in THIS process, dispatched by path SEGMENT inside
  hanzoai/tasks' own `net/http` mux, over inputs that are anonymous structs
  local to that module's handlers. So the routes cannot be typed here (there is
  no route in cloud's router to type, and re-shaping a relayed answer is a wire
  break); they become typeable in hanzoai/tasks, which owns them.
  `apps/tasks/tasks.go` records the refusal and `apps/tasks/typed_wire_test.go`
  measures each of its four legs.

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
  at init rather than letting two declarations race. Twenty-seven CALL SITES live
  there today — `apps/git` (8), `apps/platform` (7), `apps/books` (3),
  `apps/cloudflare` (3), `apps/analytics` (2), `apps/company` (2),
  `apps/destinations` (1), `apps/provisioning` (1) — and rather more registrations,
  because analytics' two sites are a RANGE over its `doors` table plus the health
  probe, so a door added there declares itself or fails
  `TestEveryDoorDeclaresItsBodies` rather than silently publishing nothing.
  Re-measure with `grep -rn 'openapi.Register(' --include='*.go' apps`. This is a
  bridge, not the destination; the destination is the typed op. What it buys is
  narrow and worth naming: an untyped route with no declaration publishes
  operationId and tags and NOTHING else, which no consumer of the document can
  distinguish from a route that takes no body and returns none — so the SDKs
  generated off it offered calls with no payload and no return type. It still
  buys no prose, no MCP tool and no CLI command; only a typed op does.
- **A body that is not JSON is declarable too: `openapi.Register(path, method,
  openapi.Binary{}, resp)`.** Some routes can never be typed ops because they eat
  RAW BYTES — an uploaded receipt (`POST /v1/books/scan`), an OFX/QFX/CSV
  statement (`/v1/books/bank/import`), a KV value. zip's typed path decodes the
  body with `jsonenc.Unmarshal`, so declaring any `In` on one would turn a working
  PDF upload into a 400: the wire would MOVE, which a description task may not do.
  `Binary` is the honest declaration a Go struct cannot make (no struct describes
  a file) and renders OpenAPI's own spelling, `application/octet-stream` with
  `{type: string, format: binary}` — which is what an SDK generator turns into a
  file parameter. It is REQUEST-only: a byte RESPONSE is a second fact no route
  needs yet, and adding it before one asks is how one seam becomes two.
- **A POLYMORPHIC body is declarable too: `openapi.Register(path, method,
  openapi.OneOf{A{}, []A{}, B{}}, resp)`.** Same shape of fact as `Binary` — the
  honest declaration a single Go struct cannot make — for a route that accepts
  SEVERAL unrelated JSON shapes on one path. `apps/analytics`'s canonical ingest
  wire is the case that asked for it: `decodeIngest` takes a bare `Event`, a bare
  `[Event]`, and the `{batch:[…]}` envelope, and naming one of the three would have
  published an ingest API that cannot batch — which is most of what `@hanzo/event`
  does. It renders OpenAPI's own `oneOf`, each alternative reflected exactly as a
  lone request type is (so a named struct among them is the SAME component, once).
  REQUEST-only, for the reason `Binary` is.
- **`Register` cannot carry FIELD PROSE, and that is why the fleet publishes 1,627
  bare properties.** The seam derives a schema by reflection and Go drops comments,
  while zipdoc — the pass that lifts field descriptions — walks zip's TYPED
  registrations and can never reach a type that arrives this way. So every
  `Register`ed component ships shapes without descriptions: measure it with the
  snippet under failure mode #10, per plugin — admin 710, agents 209, visor 153,
  books 100, guide 90. It is the LAST thing separating a declared-but-untyped route
  from a typed one on the document surface, and it is a generator gap, not a
  diligence gap: do not "fix" it by hand-writing schemas beside the structs, which
  is the drift `Register` exists to prevent. `apps/analytics` names its twelve such
  components in a `proseless` ledger (typed_wire_test.go) that may only SHRINK — an
  entry that starts publishing prose goes red, so the day cloud learns to lift
  comments for `Register` the ledger empties instead of quietly outliving the gap.
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
  - **The lifted prose no longer carries the handler's own name.** A Go doc
    comment must open with the identifier it documents ("`GetSQL` returns one
    database"), and that identifier is Go's, not the document's: it reached the
    OpenAPI description, its derived summary, the MCP tool description an agent
    reads, and the CLI help line — naming a function no caller can see. zip
    v1.18.13 drops an EXACT leading match of the handler's own name and
    re-capitalises ("Returns one database"); prose that merely opens with a
    camel-case word keeps it. Requires zip >= v1.18.13. It strips only where the
    comment names the function EXACTLY, so a method `revokeKey` whose comment
    opens "RevokeKey revokes …" is left alone — the source is idiomatic Go either
    way, and the rest of the fleet's leading identifiers go when their comments
    match their handlers.
  - The same bug had a SECOND instance one projection over, and it outlived the
    first. zip's `mcpTools` read `op.Summary` — set only by an explicit
    `WithSummary`, which cloud uses nowhere because the doc comment is the source.
    So `zipdoc` ran, the spec got its prose, and **all 164 MCP tools still served
    an empty description over a schema whose fields said nothing.** Fixed in
    `zap-proto/zip` v1.17.6 (`mcpTools` reads the same `docFor` extraction and
    builds the input schema with `schemaOfDoc`); measured after: 164/164 tools
    described, 324 documented fields. Requires zip >= v1.17.6 — an older zip
    silently reverts the MCP plane to nameless tools while the spec still looks
    correct, which is precisely why it went unnoticed. The lesson generalises:
    when a projection reads a DIFFERENT field than its siblings, it does not fail,
    it just goes quiet.
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
  hypothetical: `apps/git`'s `/:org/:repo` catch-all was swallowing other
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
- **Every operation says what it does, and everything said is said about an
  operation** (`openapi.Complete`, openapi/prose.go, called from `Describe` in
  describe.go). Both halves fail the same way — a consumer holds an address and no
  sentence — so both are refused at the ONE producer, naming the app, the route and
  the remedy, and the artifact is simply not written. Downstream cannot repair
  this: hanzoai/cli's generated tree used to print the operation's own HTTP ROUTE
  when it had no sentence (`hanzo platform health` → "GET /v1/platform/health"),
  and nobody filed a bug because a mechanical line reads exactly like a deliberate
  one. A placeholder would be that fallback with better manners — it would travel
  into eight SDKs, the MCP tool list and docs.hanzo.ai and be no more visible in
  the one place that can fix it. The second half matters as much: `Describe`
  renders nothing when its key is not a live route, so a MIS-KEYED declaration is
  prose that was written, reviewed and silently dropped — `POST
  /v1/store/storefront-token` published an operationId and nothing else while its
  description sat under the store's old `/v1/store/token` address. An orphan is
  judged only inside the products that app publishes, because every app binary
  links cloud's core and therefore carries other subsystems' declarations.
  **1491 of 1491 operations carry prose; 0 orphans.**
- **`openapi.yaml` is a GOLDEN, woven from the per-app subsets, and the ONE
  artifact cloud publishes.** `make openapi` writes it (through the weave,
  `-weave`); `make test`, and therefore CI, verifies it with the same weave and no
  flag (`TestFleetIsTheWeaveOfItsApps`, openapi/weave_test.go). Same code path both ways — there is no second
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

## The typed migration: THE PLAYBOOK (start here before typing anything)

Worked end to end on `apps/agents/targets.go` (5 ops). Follow it and a partition
is mechanical; skip it and you will rediscover eight failure modes the hard way.

### The recipe

1. **A receiver, not a closure.** `type xOps struct{ s *cloud.Service[state] }`
   and every op a METHOD (`o.registerTarget`). A TypedHandler takes no service
   parameter, and a method value is the only bound form `cmd/zipdoc` can lift
   prose from — a closure returned by a helper is a call expression with nothing
   to read.
2. **Declare on the GROUP the subsystem already has** —
   `g := app.Group("/v1/agents")`, then `zip.Post(g, "/targets", o.register)`.
   The op's path is the prefix composed with the leaf, which is the identity every
   projection keys on, and zipdoc (zip v1.18.3+) resolves the prefix the same way,
   so the doc comments reach the document and the tool list. If it cannot resolve
   the router — a group built somewhere it cannot see — it FAILS naming the call
   rather than filing the prose under a path that does not exist. (Before v1.18.3
   it filed group-declared ops under the bare leaf and the prose vanished from
   both surfaces, silently. If you are on an older zip, bump; do not work around
   it.)
3. **Install `cloud.Bridge()` on the subsystem's group, before the leaves.** A
   typed op receives only a context, so the validated org has to be parked there.
   fiber runs middleware in registration order — one installed after its leaves
   never runs. Untyped handlers read identity off the request and hide this.
4. **Identity is NEVER an In field.** `principal.OrgFrom(ctx)` for the tenant, and
   `principal.ValidatedFrom(ctx)` when the plane has no tenant at all and the gate
   is only "is this caller signed in" (a deployment-global read — `apps/engine`'s
   shared runtime, o11y's infra-health probe). `cloud.Bridge` parks BOTH in one
   expression, so they are always set together. An In field is caller-supplied, so
   a tenant key read from one is a cross-tenant read the caller asserted for
   itself. If you need more than those two (admin-ness lives in a header), that is
   `cloud.Request(ctx)` — and it is PINNED, so add your file to
   `allowedRequestUses` with a justification or the gate fails. Note which way
   they differ before choosing: `OrgFrom` composes validated-ness AND an org, so
   it refuses a signed-in caller whose token names no home org (a machine token) —
   right for a plane with rows to scope, wrong for one without.
5. **Preserve the wire, exactly.** Same JSON shapes, same statuses. `//go:generate
   go run github.com/zap-proto/zip/cmd/zipdoc` in the package, then
   `make -C apps/<app> openapi`.
6. **Doc comments are product surface.** They ship to the OpenAPI `description`
   AND the MCP tool description — a model picks a tool by reading them. An
   `Example:` line becomes the request example. Write them true.
   The `summary` is the first sentence, and zip finds it by looking for a period
   followed by whitespace. It once looked for `". "` alone, so a first sentence
   ending at a LINE BREAK was not found and the summary fell back to the first
   line, cutting mid-sentence; zip v1.18.11's `sentenceEnd` accepts `\n`, `\t` and
   `\r` too, so a sentence may now wrap (verified: `GET /v1/plans/resolve/{id}`
   publishes a four-line first sentence whole). Still prefer a short one — the
   summary is what a tool list shows.
7. **Never EMBED a struct in an In or an Out — spell the fields at the top
   level.** zip's `structSchema` walks `t.NumField()` and skips every field
   `IsExported()` is false for, which an embedded UNEXPORTED type is (its field
   name IS the type name). `encoding/json` still PROMOTES those fields, so the
   wire carries them and the document does not — the one direction no test
   catches, because the route works perfectly. Exporting the embedded type is not
   the fix: zip then publishes it as a NESTED object property named after the
   type, which the wire does not have either. Live in the committed golden today:
   `patchTargetIn` (apps/agents/targets.go — the worked example) publishes `{id}`
   ALONE, so `PATCH /v1/agents/targets/{id}` documents none of label, kind,
   status, capacity, host, spec or metrics, in openapi.yaml, in any generated SDK,
   or in the MCP tool's inputSchema; `botView` (apps/visor/bots.go) publishes
   `{agent,binding}` and drops the 14 machine fields it embeds;
   `clusterDetailView` (apps/visor/k8s.go) publishes `{nodes}` alone. Fix those
   three by inlining their fields — or fix `structSchema` to flatten an embedded
   struct the way the decoder does, which fixes the class. The class keeps
   recurring past any enumeration: guide's `stepView` embedded `JourneyStep`
   (an EXPORTED type), so every step object in `GET /v1/guide` and the
   skip/reset ops documented a nested `{JourneyStep: {…}}` the flat wire never
   carried — found and fixed by inlining, with a reflect test
   (`TestStepViewCarriesJourneyStep`) pinning the spelled-out copy against the
   embedded source so a later JourneyStep field cannot silently drop out of the
   view. The EXPORTED direction is findable in what shipped — a property named
   exactly after the schema it $refs is the tell — and the check below reads
   the published subsets, so it cannot disagree with them. It finds two more
   today, both in `plugin/admin`: `MetricsData -> SaaSMetrics` (whose own
   comment says "the SaaS snapshot, flat") and `ServiceView -> ServiceRow`.
   The UNEXPORTED direction publishes nothing to grep for — those three above
   were found by reading — so only the zip-side flatten retires the class:

       python3 - <<'EOF'
       import json,glob
       for f in sorted(glob.glob('plugin/*/openapi.json')):
           d=json.load(open(f))
           for n,v in (d.get('components',{}).get('schemas',{}) or {}).items():
               for p,s in ((v.get('properties') or {}) if isinstance(v,dict) else {}).items():
                   if isinstance(s,dict) and s.get('$ref','').endswith('/'+p) and p[:1].isupper():
                       print(f,n,'->',p)
       EOF

### Statuses

`zip.WithStatus(201)` declares an UNCONDITIONAL success status and keys the
document's `responses`. Use it for every op that always answers 201/202.

**The conditional class — the worked example.** `registerTarget` answers 200 on
an idempotent re-link and 201 on first registration. That is correct REST and
`WithStatus` cannot express it (one declaration, one status). Those routes stay
**typed-but-shimmed**: keep `cloud.Created(ctx)` on the create branch only. Do
NOT bend the route to fit the declaration, and do NOT invent a third mechanism —
zip is getting multi-status `responses`, and these convert when it lands.

### The nine failure modes, all found the hard way

1. **A gate comparing two DERIVED artifacts agrees with itself while both are
   wrong.** `openapi-composed` compared the subsets to the golden they weave
   into; nothing regenerated from the routes. `plugin/ingress` lost 8 paths
   (`/v1/ingress/routes|services|middlewares|tls|status` + `:id` forms) — absent
   from `openapi.yaml` and therefore from every generated SDK, so no Python, Go
   or TS caller could reach the ingress API at all, with every gate green. The
   cure is `make -f mk/fleet.mk surface-check`, which REGENERATES and diffs.
   It RECURS, and the gate is what finds it: `e83d7e90` moved websearch's scrape
   to `/v1/scrape` without re-emitting `plugin/websearch/openapi.json`, so main
   published two paths nobody serves (`/v1/websearch/scrape`,
   `/v1/websearch/v1/scrape`) and omitted the one that is — caught only because an
   unrelated ingress change ran the gate. Run it before you push, not after.
   **Two more instances are live on main right now**, both found the same way (an
   unrelated o11y change ran the gate), and one is the ingress shape exactly:
   `plugin/authz/openapi.json` publishes `GET|POST|DELETE /v1/authz/policies`,
   but the pinned `hanzoai/authz v1.10.15`'s `serve.Mount` registers only
   `/v1/authz/{health,readyz,check}` — three operations no binary serves, in
   `openapi.yaml`, in every generated SDK, in the MCP tool list, and with
   `manifest/apps.go:38` still routing that prefix to a plugin that 404s it. The
   other WAS `plugin/tools/openapi.json` — **CLOSED** at the tools typing pass, which
   regenerated it from the live router. What the regeneration actually found is worth
   recording, because it is not what the note above predicted: the committed copy
   already carried the four derived tags and the literal em dash (someone had fixed
   that half), and all sixteen paths matched the router. The drift that remained was
   PROSE — sixteen operations, every one of them publishing no summary and no
   description. So a stale-golden note can go stale in the safe direction too, and the
   only way to know which is to run `make -C apps/<app> openapi` and read the diff.
   The authz half is still open; whoever owns authz next runs `make openapi` and
   commits it. Verify it yourself, it is one command:

       grep -rE '\.(Get|Post|Put|Delete)\("' \
         "$(go env GOMODCACHE)/github.com/hanzoai/authz@v1.10.15/serve/"
2. **The stale-tree pin walk-back.** `bb10586e` reverted commerce v1.49.30→29 and
   zip v1.18.1→v1.17.6 in a single-parent commit: `go get`/`go mod tidy` run in a
   tree that predated the bump, committed wholesale. It is MECHANICAL, so it will
   recur — any agent on a stale tree reproduces it. Main then held documents one
   zip version generated against a go.mod pinning another that could not produce
   them, and nothing detected it. Rebase before you regenerate.
3. **Verify what CI actually invokes before trusting a gate you add to a make
   target.** cloud's CI never ran `make test` — no `.github/workflows`, and
   `hanzo.yml` names steps directly. A gate added to `make test` protected
   nobody. `hanzo.yml` calls `surface-check` now.
4. **`git status --porcelain`, not `git diff`.** A NEW app produces a NEW
   UNTRACKED subset, invisible to a diff — the failure that matters most is the
   one a diff cannot see.
5. **The schema namespace is FLAT, and typing is what makes you enter it.** A
   typed op's Go type name IS its schema name across the WHOLE fleet, and
   `openapi.Weave` refuses one name with two shapes ("every generated SDK would
   bind whichever it read last"). An UNTYPED route contributes no schema at all,
   so the collision does not exist until you type — and it surfaces at the weave,
   not at the compiler. `apps/ingress` named its list envelope `serviceList`, which
   is already `apps/admin`'s launch board, and the weave refused the whole package.
   The fix is to qualify the VALUE with the product the namespace cannot carry
   (`ingressServices`), not to rename admin's. It recurred immediately: guide's
   `Step` (a journey step) collided with marketing's `Step` (a step of a drip
   sequence), and guide — whose schema was not yet published — is the one that
   yielded, to `JourneyStep`. Check before you name, comparing SHAPES and not just
   names, because two apps may share a name when they agree:

       python3 - <<'EOF'
       import json,glob,os,collections
       s=collections.defaultdict(dict)
       for f in glob.glob('plugin/*/openapi.json'):
           d=json.load(open(f))
           for n,v in (d.get('components',{}) or {}).get('schemas',{}).items():
               s[n][os.path.basename(os.path.dirname(f))]=json.dumps(v,sort_keys=True)
       for n,a in sorted(s.items()):
           if len(a)>1 and len(set(a.values()))>1: print(n, sorted(a))
       EOF

   Domain nouns can stay unqualified while they are unique — the weave is the gate
   when they stop being. Generic ones are the standing hazard: `agents` publishes
   `Spec`/`Metrics`/`GPU`, `plugins` publishes `Status`/`Result`/`Host`/`Time`.
6. **A route going typed RENAMES its operationId**, `_by_id` → `_id`
   (`delete_v1_ingress_routes_by_id` → `delete_v1_ingress_routes_id`): the untyped
   projection derives the id from the route pattern, a typed op from zip's
   `defaultOpID`. It is not the wire — no status, body or field name moves — but
   it IS the generated SDK's method name, so an SDK regenerated after the migration
   renames those methods. `apps/agents/targets.go` already shows both forms side by
   side on one prefix (its typed `..._targets_id` next to its untyped
   `..._targets_by_id_claim`). Take the rename; do NOT pin it back with
   `WithOperationID`, which would make one app's ids a special case.

7. **zip cannot declare a bodyless POST — CLOSED in zip v1.18.11.** `hasRequestBody`
   now publishes a body only when the In has at least one field the URL does not
   already carry, so the class the count below chased is gone for TYPED ops. Re-run
   the check against the committed subsets and it reports **9**, all of them
   `openapi.Register` declarations of a binary/no-schema body (books' 3 uploads,
   company's deck, admin's credit-grants, git's 4 pack endpoints) — not phantom SDK
   arguments. The history is kept because the LESSON survives the fix: count the
   class from the published subsets, never tally it in prose. What follows is what
   the class looked like at v1.18.6.
   `hasBody("POST")` is unconditional, so
   a typed POST whose entire input is the URL still publishes
   `requestBody: {required: true}` over its In: guide's `/steps/{id}/skip|reset`
   now say they require `{"id": …}` for a body they have never read, and
   `apps/admin` already ships two of the same (`/v1/admin/customers/{org}/suspend`,
   `…/reactivate`). The WIRE is unharmed — `bindURL` binds the path LAST, so the
   URL still names the target and a body id cannot redirect the write
   (`TestTypedStepOpsFailClosed` pins exactly that) — but the document asserts
   something false and a generated client gains an argument. Same shape of gap as
   multi-status (#78): the wire fact exists and the declaration cannot say it.
   `apps/integrations` adds SIX more, and running the check below over the
   committed subsets puts the class at **27 across 10 packages** — count it, do
   not tally it in prose, because the enumerated instances are always the ones
   somebody happened to look at. `apps/company` alone carries 6 (every one of its
   `noInput` POSTs: `documents`, `esign`, `genesis`, `kyc`, `kyc/refresh`,
   `skip`), which is the largest single share and was invisible until the check
   existed. The integrations six are `POST /v1/connectors/{id}/refresh`,
   `…/connectors/{provider}/device/{flow}/poll`,
   `…/github/repos/{repo}/pages/builds`, `…/integrations/{provider}/disconnect`
   and `…/{provider}/verify` each publish a required body whose only properties
   ARE their path params, and `…/integrations/telegram/connect` publishes a
   required body over `noArgs`, i.e. an object with no properties at all. Re-find
   them with the check below, which reads the published subsets rather than the
   source, so it cannot disagree with what shipped:

       python3 - <<'EOF'
       import json,glob
       for f in glob.glob('plugin/*/openapi.json'):
           d=json.load(open(f)); sc=d.get('components',{}).get('schemas',{})
           for p,ops in d['paths'].items():
               for m,op in ops.items():
                   if m.upper() not in ('POST','PUT','PATCH') or 'requestBody' not in op: continue
                   n=(op['requestBody'].get('content',{}).get('application/json',{})
                      .get('schema',{}) or {}).get('$ref','').split('/')[-1]
                   pr=set((sc.get(n,{}).get('properties') or {}).keys())
                   pa={q['name'] for q in op.get('parameters',[]) if q.get('in')=='path'}
                   if pr<=pa: print(f, m.upper(), p, n, sorted(pr), sorted(pa))
       EOF

8. **An `any`-valued map publishes a schema that is not thin but FALSE.** zip's
   `schemaOf` has no `reflect.Interface` case, so `map[string]any` falls to the
   default and projects `additionalProperties: {"type": "object"}` — an assertion
   that every VALUE is a JSON object. `apps/framework`'s own tests refute it on
   the way past: a document reads back `{"subject": "Ship framework",
   "docstatus": 0}`, a string and a number. `openapi.yaml` carries the claim in 15
   places. This is a WORSE failure than #78 and the bodyless POST above, and the
   difference is the one that matters: those UNDER-describe a true wire, this one
   describes a false one, so an SDK regenerated from the golden types a document
   `Dict[str, Dict]` — a shape that cannot hold one. hanzoai/openapi's authored
   master gets it right (`framework_Document`: `additionalProperties: true`), so
   the two documents genspec joins disagree about the same value today. The fix is
   one case in one function — an unconstrained element is an OPEN schema (`true`),
   not an object.

9. **An EMPTY leaf on a group names the group's path plus a slash.**
   `joinPath(prefix, "")` normalises the leaf to `"/"`, so `zip.Get(b, "", fn)`
   on `b := g.Group("/blueprint")` declares `/v1/guide/blueprint/` — and op.Path
   IS the identity every projection reads, so the document, the operationId
   (`get_v1_guide_blueprint_`), the MCP tool of that name and the URL a generated
   SDK calls all carried a trailing slash for a path this API has never served.
   The router is non-strict, so nothing broke on the wire and nothing went red;
   it was visible only in the published artifacts, next to fifteen sibling paths
   without one. `apps/guide` had already reasoned its way past the same trap one
   group up ("declaring it on g would name /v1/guide/, which this API never
   served") and walked into it one group down — which is the tell that this is
   mechanical, not a lapse. **Declare a group's ROOT on the PARENT with a
   non-empty leaf** (`zip.Get(g, "/blueprint", fn)`), and hang only the
   sub-paths off the group. Register the untyped siblings at the same address in
   the same move, or the document splits one resource across two keys.
   Grep for it: `grep -rn 'zip\.[A-Za-z]*([a-z]*, "",' --include='*.go' apps/`.

10. **A first sentence that WRAPS ships its line break into the `summary` — CLOSED
    in zip v1.18.11**, which collapses whitespace in `firstSentence` exactly as the
    fix below prescribed. Re-measured over the committed subsets at the tools typing
    pass: **0 of the 466 described operations**, down from 215. The class is recorded
    intact because it is the best example in this file of a defect nothing reads for.
    `firstSentence` (zip/openapi.go:606) returns the text up to the first `". "`
    VERBATIM — no whitespace collapse — so a doc comment whose opening sentence
    spans two source lines publishes a `summary` with a raw `\n` in it. The
    summary is a one-line field by construction: it is the OpenAPI operation
    summary, the CLI command summary (clispec.go:41, cli.go:133) and the first
    docstring line of every generated SDK method. Measured on the committed
    subsets: **215 of the 387 described operations, across 18 packages** —
    admin 34, git 20, integrations 17, visor 17, company 14, pricing 13,
    cloudflare 12, marketing 12, books 10, compliance 10, guide 10, framework 9,
    ingress 9, o11y 9, account 8, team 8, plugins 2, agents 1. Note this is a
    LARGER share than any other class here (56%), and it was uncounted because
    nothing reads the summary looking for a newline:

        python3 - <<'EOF'
        import json,glob,os,collections
        n=collections.Counter(); tot=0
        for f in sorted(glob.glob('plugin/*/openapi.json')):
            a=os.path.basename(os.path.dirname(f))
            for p,ops in json.load(open(f)).get('paths',{}).items():
                for m,op in ops.items():
                    s=isinstance(op,dict) and op.get('summary') or ''
                    if '\n' in s: n[a]+=1; tot+=1
        print(tot, n.most_common())
        EOF

    Unlike #7 and #78, the wire is not involved at all — this is prose quality
    on the surface SDK users and models read. **Fix it in `firstSentence`, with
    one whitespace collapse.** Do NOT reflow 215 doc comments so sentence one
    fits a 100-column line: that is the easy fix, it leaves the class alive for
    the next op anybody writes, and it makes cloud a special case of a general
    bug. (Playbook step 6's "keep sentence one on one line" is the WORKAROUND for
    this, not the rule — it was written when zip's fallback could cut a summary
    mid-sentence, which v1.18.6 no longer does.)

### Partitioning the remaining work

986 untyped routes across 101 packages, 76 typed. Take a whole `apps/<app>/`
tree: they are disjoint, so agents do not collide in source.

| tranche | apps | untyped |
|---|---|---|
| A | ~~integrations 47~~ (done: 22 typed, 19 refused), cloudflare 34, platform 32, projects 31, captable 31 | 128 |
| B | agents 26, ~~git 24~~ (done: 24 typed, 24 refused — four wire families, apps/git/LLM.md), ~~books 11~~ (done: 20 typed, 5 refused — 3 raw-byte uploads, 2 unconditional-501 link stubs; each named at its registration and pinned by a wire test. The refusals are now MEASURED, not asserted: apps/books/projection_test.go holds two exhaustive ledgers — the 20 ops must each reach OpenAPI-with-prose + MCP + CLI under one operation id, the 5 exempt routes must each still answer 401 (live, fail-closed) and appear in none of the three, and the two ledgers must sum to 25. Typing any of the 3 uploads needs a zip capability that does not exist: v1.18.7 decodes every typed body with jsonenc.Unmarshal and has no octet-stream/binary request declaration, so an In on a PDF upload turns 200 into 400. But the 3 uploads no longer publish NOTHING: each declares its byte request and its response view through openapi.Register + openapi.Binary — scan/ScanDraft, inbox/InboxItem, bank-import/BankTally — so the SDKs stop offering a receipt upload with nowhere to put the receipt. The 2 501 stubs declare nothing, deliberately, and a test asserts that silence), ~~o11y 11~~ (done: 12 typed, 8 refused, all wire-bound — 2 verbatim-status VM proxies, 3 reverse proxies (query/query_range/sessions), 2 text/plain Alertmanager receipts, 1 sentry wildcard; apps/o11y/LLM.md names each — the 11 counted 3 comment lines quoting `app.All("/v1/o11y/*")`, real count was 8. Re-verified independently at a later merge: still 7 registrations for those 8 operations, and every one of the 7 was re-read against its handler and re-refused. The refusals are now GATED rather than prose — `untypedByDesign` + `TestEveryRouteIsTypedOrNamed` + `TestEveryTypedOpIsDescribed` + `TestUntypedRoutesKeepTheirWire` in apps/o11y/typed_wire_test.go, reading the live router of the REAL `MountO11y` (not a reconstruction of it), so a route added anywhere in that mount is typed by default and a stale reason is red. The typed-but-unpublished `POST /v1/o11y/ingestion` is gated too, by `TestIngestOpIsTypedButUnreachableWithoutADSN`, which proves the op types cleanly AND that the DSN-less generating process does not carry it — the one behaviour decision left here cannot land silently), ~~company 22~~ (done: 20 typed, 2 refused, and the refusals are now MEASURED rather than asserted — apps/company/typed_wire_test.go holds `untypedByDesign` + `TestEveryRouteIsTypedOrNamed`, so a route added untyped here goes red without anyone remembering to name it, and a stale reason naming a route company no longer serves goes red too. Both refusals were re-verified against zip v1.18.6 source, not inherited as prose: the deck upload because `op.invoke` unconditionally `jsonenc.Unmarshal`s every non-empty body (typed.go:232) regardless of whether the In binds any field, so a PDF turns 201 into 400 and v1.18.6 has no binary request declaration to decline the decode; POST /payment because a billing denial answers the fleet-wide NESTED `{"error":{code,message}}` at 402/503 (cloud.DenyResource) while zip's `HTTPError` is a flat `{status,code,error}` and `errorHandler` is the only path a typed op's error can take — and writing the nested body from inside the op does not escape it either, since a nil Out makes zip stamp `cmp.Or(op.Status, 204)` over the 402. That one needs #78-class work: zip errors that can carry a body. The same pass closed a SEPARATE projection hole the op-level gate cannot see — 41 published schema properties across Formation, Founder, Filing, Genesis, Registration, Signer and RoundInput reached openapi.yaml, every generated SDK and every MCP inputSchema with NO description, so `equityBps` was an integer nowhere documented as basis points; `TestEveryPublishedFieldIsDescribed` now gates the response side the way the op gate covers the request side. And now that `openapi.Binary` exists (the books pass, same day), neither refusal publishes NOTHING any more: both declare their bodies through `openapi.Register` — the deck an `application/octet-stream` string/binary request plus a `deckOut` receipt, /payment no request at all (it reads none) plus the shared `formationView` — so the cost of staying untyped is exactly the three things zip's registry supplies (prose, MCP tool, CLI command) and not a fourth, a document that says the route takes no body. `TestTheUntypedRoutesStillDeclareTheirBodies` pins it. Three things remain undeclarable and are named at the registration rather than glossed: the deck's `?name=` query (the untyped projection has no vocabulary for query params), /payment's 402/503 denial bodies, and field prose on deckOut — zipdoc lifts comments off typed ops only, so Register publishes shapes without descriptions) | 50 |
| C | ~~team 20~~ (done: 9 typed, 10 refused), ~~guide 20~~ (done: 13 typed, 6 refused — 2 YAML-or-JSON document PUTs, 3 structured-409 gated transitions of which /do also streams SSE, 1 opaque merge-patch; each named at its registration, the 409/YAML wires pinned by tests), ~~crm 20~~ (done: 19 typed, 1 refused — the public intake POST; see "crm is 19 of 20" below), ~~ingress 19~~ (done, and the 19 was 18: **18 typed, 0 refused**, re-verified — the
19th was `r.Header.Get("X-Forwarded-Proto")`, see the measure below. Nothing in this
package is wire-bound: three uniform CRUD kinds behind four generic helpers, all
three DELETEs answering 204 from a `*struct{}` Out, and `TestSurfaceIsRegistered`
gates the whole surface as an EXACT set — live router == `app.Commands()` == the
18 — so a route added untyped goes red without anyone remembering to name it), ~~framework 19~~ (done: 17 typed, 2 refused — the document writes; see "apps/framework (17 of 19)" below), ~~account 19~~ (done, and the 19 was 18: **11 typed, 7 refused** — re-verified; the seven are two routes' worth of shape, GET|POST `/v1/billing/*` and the five-method `/v1/commerce/*`, and `apps/account/typed_wire_test.go` holds them as a CLOSED list so an eighth goes red. See "apps/account (11 of 18)" below) | 117 |
| D | ~~pricing 18~~ (done: 30 typed, 2 refused — both admin overlay PATCHes: one addresses a slashed model id through a greedy wildcard fiber calls `*1` and the document calls `{wildcard1}`, so the bound field and the published parameter cannot agree; the other carries an RFC 7386 merge patch stored and echoed VERBATIM, which `json.RawMessage` publishes as an array of integers and `map[string]any` reorders. The 15 "verbatim byte proxy" refusals came OFF the list: apps/goja already re-marshals the bundle's answer through Go's encoding/json, so a typed op re-marshalling the same value is byte-identical — apps/pricing/sections_wire_test.go proves it route by route), ~~ml 18~~ (done, and the 18 was 17: **10 typed, 7 refused**, every refusal wire-bound and GATED rather than asserted — `untypedByDesign` + `TestEveryRouteIsTypedOrNamed` in apps/ml/typed_wire_test.go, whose two ledgers must SUM to the served surface. The seven: the three creates (`POST /v1/ml/models`, `/v1/train/jobs`, `/v1/train/experiments`) answer 402/503 IN BAND through `cloud.DenyResource` with the fleet's nested `{"error":{"code","message"}}` contract, and a typed op can only refuse by RETURNING an error, which zip renders as the flat `{"status","code","error"}` HTTPError — a NEW refusal class, distinct from multi-status #78: a non-2xx with a DOMAIN body. Moving the gate to middleware does not rescue them either, because it would run before the body decode and turn today's 400-on-a-bad-name into a 402. `PATCH /v1/ml/models/{name}` relays an opaque RFC 7386 merge patch VERBATIM to the Kubernetes API (`map[string]any` turns `{"replicas":1000000}` into `1e+06` and patches a float over an int). `POST …/predict` returns the predictor's own status, bytes and Content-Type. The two `/health` probes answer 503 carrying the degraded REPORT as their body. One `view()` now returns the published `mlResource` for BOTH the typed reads and the untyped create/patch, so the shape cannot depend on which route served it; `TestView` asserts the marshalled BYTES, which is what proves the map→struct swap did not move the wire, and mlResource's field order is ALPHABETICAL for exactly that reason), ~~automations 18~~ (done: 14 typed, 4 refused), index 17, dataroom 17, ~~compliance 17~~ (done: 16 typed, 1 refused — the HMAC webhook: the signature is computed over the RAW body bytes and verified before any parse, and an unknown reference answers a second 200 shape; the refusal is now GATED, not prose — `untypedByDesign` + `TestEveryRouteIsTypedOrNamed` in typed_wire_test.go), affiliates 17 | 104 |
| E | ~~tools 16~~ (done: **15 served, 14 typed, 1 refused**, wire-bound and GATED rather than asserted — `untypedByDesign` + `TestEveryRouteIsTypedOrNamed` in apps/tools/typed_wire_test.go, whose two ledgers must SUM to the served surface and whose counts are pinned so the prose here cannot drift from the binary. Its 16 ops published NO summary and NO description at all before this — the whole subset was invisible to prose, MCP, the CLI and every typed SDK method. The one refusal: `POST /v1/plugins/build` answers 422 carrying the build DIAGNOSTICS (detail, source, generated) as a domain body, the ml refusal class — a typed op's only refusal is a returned error, which zip renders as the flat HTTPError with nowhere to put them. Three query filters stayed STRINGS rather than becoming bools, deliberately: these routes compare the raw value to the literal `"true"`, and zip's `setScalar` reads a bare `?activated` and `?activated=1` as true, so a bool In would return a different set of tools for the same URL — `TestActivatedFilterIsTheLiteralTrue` pins it. The pass also closed the response-side hole the op gate cannot see: Tool, Skill, MCPServer, AuthoredPlugin and Price are store rows, so all 29 of their published properties would have reached openapi.yaml, every SDK and every MCP inputSchema bare; `TestEveryPublishedFieldIsDescribed` gates them now — and it covers all 52 properties the subset publishes, not only the row types. Re-verified independently at a later pass, and the refusals were re-read against zip SOURCE rather than inherited: `op.invoke`'s unconditional `ErrBadRequest` on an unparseable body is typed.go:242 and the `cmp.Or(op.Status, 204)` a nil Out stamps is typed.go:305 — three citations in the refusal prose were off by one to three lines and are now exact, which matters because a file:line nobody can land on is how a refusal stops being re-checkable. That pass also closed the half of a refusal the op gate cannot see, the books/company answer one plane over: the untyped route rendered as an operationId and a tag and NO BODY AT ALL — indistinguishable from a route that takes no input and returns none — so every SDK generated off openapi.yaml offered a plugin build with nowhere to put the source. `openapi.Register` (apps/tools/tools.go init) now states the half that IS statable: buildRequest→buildOut, gated by `TestTheUntypedRouteStillDeclaresItsBody`. A map literal became a named struct to do it, with ALPHABETICAL fields because encoding/json writes a map in sorted key order — `TestBuildReceiptIsByteIdentical` asserts the marshalled BYTES, which is what proves naming a shape did not move it. The cost is honest and recorded: 17 of the subset's 69 published properties are now bare, because zipdoc lifts field prose off TYPED ops only and Register's reflection seam reads Go types, not comments — the same gap company recorded for deckOut. Described ops stay 14 of 15: Register buys the SHAPE, never the prose, and inventing a description for a route nobody typed would be worse than none. The SECOND refusal is now GONE rather than typed: `POST /v1/tools/mcp` was a hand-rolled JSON-RPC door, and the fleet serves ONE MCP door on the host — this plane reaches it through the typed `POST /v1/tools/call`, so the envelope, its declared shapes and its byte-identity gate all went with the route), eval 16, social 13, esign 13, link 12, functions 12, commerce 12, billing 12 | 90 |
| F | ~~plan~~ (done: **15 typed, 0 refused** — the whole `/v1/plans` surface, which published 15 addresses and NOTHING about any of them. It is apps/pricing's shape one subsystem over, so it took pricing's answer: apps/goja re-marshals the bundle's reply with Go's encoding/json before a handler sees it, so decoding and re-marshalling is byte-identical, and `apps/plan/wire_test.go` proves it against the live router for all 12 sections × 3 identity shapes plus BOTH parameterised routes over EVERY id the shipped catalog holds. Opaque catalog values are `json.RawMessage`, not `map[string]any`: zip ≥v1.18.9 asks whether a type marshals itself before asking what it is made of, so a RawMessage publishes `{}` — "any JSON", true — while `map[string]any` publishes `additionalProperties:{"type":"object"}`, which the first `"priceMonthly": 20` in the catalog refutes. Two deltas recorded and PINNED rather than glossed: Content-Type gains `; charset=utf-8` (what every zip error on the surface already sent), and a non-200 body gains zip's `status` field beside the bundle's own message — the same trade main already took for `GET /v1/pricing/model/{name}`, whose 404 is equally first-class. Latent defect found and fixed by typing: the subsystem is named `plan` and serves `/v1/plans`, so `MountPrefixes`'s `/v1/<Name>` default covered NOTHING it registers — measured, `SubsystemOf("/v1/plans")` was `""` and `PriceOf` `undeclared`, and any middleware the subsystem installed (including the typed-op Bridge) landed on `/v1/plan` and never ran. `plugin/plan/main.go` now passes `manifest.PrefixesFor("plan")`; 28 other apps' `/v1/<name>` is likewise absent from their declared prefixes — most legitimately, because they declare deeper subtrees that DO cover their routes, so re-measure per app rather than fixing the list), ~~analytics 13~~ (done: **6 typed, 7 refused**, both ledgers GATED in apps/analytics/typed_wire_test.go — `untypedByDesign` + `TestEveryRouteIsTypedOrNamed`, whose two ledgers must SUM to the served surface, so a route added untyped here goes red and a stale reason goes red too. The six are the READ lenses: `/v1/analytics/{overview,timeseries,top}`, `/v1/errors`, `/v1/insights/{events,health}`. The seven refusals are one probe and the six ingest doors, and they are MEASURED rather than asserted. `GET /v1/analytics/health` answers 503 CARRYING the degraded report as its body — the ml `/health` class — and `TestHealthStillCarriesItsReportAt503` pins the pair a typed op would have to break. The six doors share ONE admission decision (`handle`, event.go) resolved entirely from facts that never reach a typed op: the presented credential (Authorization / `x-hanzo-ingest-key` / `?ingest_key=`), the client IP and socket peer the anonymous rate caps key on, DNT/Sec-GPC, and the RAW body length that is the anonymous lane's 64 KiB→413 bound (`public.go maxPublicBytes`, invisible to a typed op and far below the fleet's global zip BodyLimit). Four of the six add a SECOND, independent blocker: the canonical wire is POLYMORPHIC (`decodeIngest` takes a bare Event object, a bare Event ARRAY, and the `{batch:[…]}`/`{events:[…]}` envelope on one path) and the team SPA's is a bare ARRAY unconditionally — bodies zip's `op.invoke` would 400 where they answer 200 today. `TestArrayBodiedDoorsStillAnswer200` is that measurement, so when zip can declare a polymorphic body the conversion is a test away rather than a re-derivation. Two latent defects surfaced and were fixed: plugin/analytics/main.go declared NO `Prefixes`, so the standalone binary's scope owned only the `/v1/<name>` default and five of the app's six prefixes were outside anything it could gate or that `cloud.Declare` could attribute — the pricing defect exactly, one app over; and `apps/analytics` had no `cloud.Bridge` of its own, relying entirely on Serve's app-wide install, which the package's own test harness does not run. A THIRD is fleet-wide and only named here: `SeriesPoint` was about to be a one-name/two-shape collision with apps/admin's `{t,value}` launch-board point, which `openapi.Weave` refuses — analytics yielded to `UsagePoint`, since its schema was not yet published. `TestEveryPublishedFieldIsDescribed` gates the response side: all 19 TYPED schemas carry per-field prose, so `errorRate` says it is a ratio and `pct` says it is a share of the WINDOW rather than of the rows returned. **Re-verified at a later pass against a MOVED surface, and every refusal held** — by then main had folded `/v1/event/collect` away and added the two Sentry doors `POST /v1/event/{project}/{envelope,store}`, so the ledger reads 6 typed / 8 refused. The reasons were re-derived from zip v1.18.11's own `typed.go` rather than inherited as prose, and each gained a blocker the first pass had not written down: ORDER. `op.invoke` decodes the body BEFORE the handler is entered, while the anonymous lane refuses 403 (capture disabled), then 429 (rate), then 413 (64 KiB) with the raw bytes still in hand and nothing parsed — so a typed op would answer 400 to a beacon that is answered 413 or 429 today. Error precedence is wire. What that pass DID change is the other half of the cost: all eight published an operationId and NOTHING else, so every generated SDK offered `post_v1_event` — the door every Hanzo product beacons to — as a call with nowhere to put the event. They declare their bodies now through `openapi.Register`, driven off the `doors` table itself so a door cannot be routed with one wire and documented with another, and the gate quantifies over `untypedByDesign` rather than over doors (`TestEveryUntypedRouteDeclaresItsBodies`) so a refusal added tomorrow owes its bodies by construction. The canonical four use the new `openapi.OneOf`, because their wire is genuinely three shapes; the two Sentry doors use `openapi.Binary` for a request that is an opaque envelope stream and declare NO response, named in `relayed` — they copy back whatever `cloud.ObsErrorIngest` installed, and publishing a shape there would be inventing one. The subset went 0→7 requestBody and 6→12 responses, 19→30 schemas, and the described count did NOT move: 6, exactly the typed ops, because prose, an MCP tool and a CLI command are the three things only zip's registry supplies. Reported as unchanged rather than counted as progress. The health probe's `map[string]any` became `healthReport` in the same move — a map's shape cannot be declared without hand-writing a schema beside it, which is the drift `Register` exists to prevent — and `TestHealthReportKeepsTheMapItReplaced` pins both bodies field-for-field. The one difference is serialization ORDER (Go struct order, not encoding/json's sorted map keys), which the JSON data model does not carry; no field name, value, presence rule or status moved. The cost is a `proseless` ledger of eleven components publishing 63 bare properties, and it may only SHRINK — it caught its own first staleness during this rebase, when main's removal of the Team door left `teamEvent` named and unpublished), the ~70 remaining packages, 1–11 routes each | ~358 |
| — | ~~iam 25~~ (done: **0 typed, 25 refused** — the whole product is five `app.All` wildcards relaying a nested app; see "apps/iam (0 of 25, and why)" above. The refusal is gated, and the pass fixed a live fail-closed hole) | 0 |

**The five-plugin pass (ask, audit, catalog, crawl, x402): 3 typed, 2 refused —
and all five published NOTHING before it.** Five one-operation subsets, every one
of them carrying neither `description` nor `summary`, i.e. the whole surface was
invisible to prose, MCP, the CLI and every typed SDK method. What it taught:

- **The wire that keeps a query filter a STRING is measurable, and it is not the
  same fact twice.** catalog's `?limit` and audit's `?pageSize` look like ints and
  are not: zip's `bindURL` leaves an unparseable value at the field's ZERO, so an
  int In cannot tell `?limit=0` (a page of nothing, which catalog serves) from
  `?limit=abc` (unset → 50). catalog's `?forkable` is TRI-state and zip reads a
  bare `?forkable` as TRUE, which is "unasked" here. Both are pinned by tests
  (`TestPagingAndForkableStayStrings`, `TestUnparseablePagingFallsBackRatherThanRefusing`)
  rather than argued, and audit's choice matches its ADMIN TWIN — `GET
  /v1/admin/audit` already publishes `pageSize`/`p` as strings, so one surface does
  not disagree with the other about the same value.
- **A map[string]any Out has a byte order, and a struct's is its DECLARATION
  order.** audit's envelope was `{"status","msg","data","data2"}` as a map, which
  encoding/json SORTS. `trailPage`'s fields are declared alphabetically for exactly
  that reason and `TestEnvelopeBytesDidNotMove` asserts the tail bytes, which is
  what makes the reason survive the next reader who thinks the order is cosmetic.
- **`Cache-Control: no-store` survives typing as ONE middleware, not as
  cloud.Request.** audit's per-tenant security events must not be cached; a typed
  op has no response value, so the header rides a group middleware that sets it on
  SUCCESS only — exactly where the untyped handler set it. Reaching for
  cloud.Request would have bought the same header at the cost of an entry in the
  pin. (x402 DOES take an entry, and the reason is different in kind: `payerOf`
  needs `principal.Ledger`, which folds in the SuperAdmin masquerade rule
  `principal.OrgFrom` cannot carry — reading the tenant through OrgFrom would have
  widened a platform admin's receipt read from their OWN ledger to the inspected
  org's.)
- **The two refusals are wire-bound and MEASURED.** `POST /v1/ask` fails three
  independent ways, each pinned by `TestAskRefusalIsTheWire`: ONE route with TWO
  success shapes (the advisor's 5-key answer vs the web engine's 8-key one), an SSE
  branch (`c.SendStreamWriter` — there is no Out that means "I already streamed"),
  and a `cloud.DenyResource` 402/503 carrying the fleet's NESTED
  `{"error":{code,message}}` where zip renders a returned error flat. `POST
  /v1/crawl` fails two: it is deliberately BODY-TOLERANT with a DOMAIN refusal body
  (a malformed body and an empty url are the same 400
  `{"success":false,"error":"missing url"}`, which `op.invoke`'s unconditional
  400-on-unparseable-body cannot express), and its 1 MiB `io.LimitReader` bound is
  invisible to a typed op. Neither publishes NOTHING any more: both declare their
  request through `openapi.Register`. crawl declares its response too; ask's is
  deliberately left undeclared, because one shape would be a FALSE statement about
  the branch it does not describe — `TestAskDeclaresItsRequestAndNotItsResponse`
  pins that silence so nobody "fixes" it.
- **Three latent defects, all found by typing.** (1) `apps/crawl` registered
  `g.Post("", …)` AND `g.Post("/", …)` on a `Group("/v1/crawl")` — the SAME route,
  since zip normalises an empty leaf to `"/"` — so the second was dead code and the
  published path was `/v1/crawl/`, a trailing slash the callers do not use, in the
  document, the operation id, the MCP tool and every generated SDK's URL. Failure
  mode #9, live. Now ONE registration at `/v1/crawl`, with both spellings still
  answering (the router is non-strict) and `TestCrawlIsServedAtOneAddressAndPublishedAtIt`
  holding it. (2) NONE of the five apps installed `cloud.Bridge` of its own — they
  relied entirely on Serve's app-wide install, which no package's test harness
  runs, so the moment any of them became a typed op the org would have been absent
  in every test and present only in production. All three converted apps install it
  on their own prefix now, ahead of their leaves. (3) `apps/ask` names its body
  `AskRequest` and its answer `AskResponse` — the SAME two names `apps/books`
  ALREADY publishes with DIFFERENT shapes. The collision did not exist while ask
  published nothing and went live the moment it declared a body; ask yielded
  (`askRequest`/`askAnswer`), which is the rule — the app whose schema is not yet
  published is the one that moves.
- **Five apps have a `plugin/<app>/main.go` and NO `apps/<app>/Makefile`, so the
  drift gate cannot see them.** `mk/fleet.mk`'s `APPDIRS` globs `apps/*/Makefile`,
  and `mk/plugin.mk`'s own header claims "an app cannot have a main and no
  Makefile" — which is false: `plugin/gen-app-cmds` writes `main.go` and never a
  Makefile. So `openapi-apps`/`openapi-check` silently skipped **bot, catalog,
  crawl, meet and zen**: five committed subsets that no gate regenerates, i.e. the
  ingress class of defect with the detector switched off. catalog's and crawl's
  Makefiles are added here (byte-identical to the generated form). **bot, meet and
  zen are still uncovered**, and the class fix is in `plugin/gen-app-cmds/main.go`:
  emit the Makefile beside the main it already writes, then run `make -f
  mk/fleet.mk openapi-check` and commit whatever drift those three have been
  hiding.
**The six-plugin pass (bots, entitlements, sbom, translate, agentskills, gateway):
11 typed, 5 refused, out of 16 operations that published NOTHING.** Same work list
rule as the pass below — every operation in `plugin/<name>/openapi.json` carrying
neither `description` nor `summary`. All six subsets were 100% undescribed before;
five of the six are now fully or mostly typed (`entitlements` 3/3, `gateway` 2/2,
`bots` 2/3, `sbom` 2/3, `translate` 2/3), and `agentskills` is 0/2 by structure. Each
package carries `untypedByDesign` + `TestEveryRouteIsTypedOrNamed` reading the REAL
mount, whose two ledgers must SUM to the served surface, so a route added untyped
here goes red and a stale reason goes red too. What it taught, beyond the counts:

- **`url:"-"` CLOSES the "path param equals body field" class.** zip v1.18.11 gives
  a field its own URL name, so `Org string \`json:"-" url:"org"\`` binds the `:org`
  segment, stays OUT of the published request body, and is invisible to the decoder
  — measured: a body `{"org":"victim"}` on `POST /v1/orgs/acme/entitlements` writes
  acme. Earlier passes recorded v1.18.6 as having no per-field opt-out; it does now.
- **A greedy wildcard is still untypable, and the reason is three published facts,
  not one.** `GET /v1/sbom/{wildcard1}`: fiber binds the capture as `*1`, a typed op
  publishes `op.Path` VERBATIM (so the address becomes `/v1/sbom/*`), and the
  parameter is then declared `in: query` rather than `in: path`. apps/pricing's
  refusal, re-measured rather than inherited.
- **Two more instances of the apps/plan prefix defect, and one of them is partial —
  which is the harder shape to see.** `plugin/agentskills` declared no `Prefixes`,
  so the `/v1/<name>` default covered NOTHING it serves (its routes are the root
  `/.well-known/agent-skills/…` convention). `plugin/entitlements` declared none
  either, and its default covered ONE of its two top-level nouns: `/v1/entitlements`
  was gated, `/v1/orgs/:org/entitlements` was not. Both now pass
  `manifest.PrefixesFor(...)`, and `apps/entitlements/typed_wire_test.go` gates the
  cover relation itself — including an assertion that the default is still NOT
  enough, so the explicit list cannot be dropped by someone who does not know why.
- **NONE of the six installed `cloud.Bridge`.** Serve installs it binary-wide so
  nothing was live-broken, but every one of these packages' own test harnesses runs
  without Serve — so a typed op added here would have 403'd in its own tests with no
  hint why. All six now install it through the SUBSYSTEM router (`app.Use`), which
  scopes it to the declared prefixes rather than the whole binary.
- **A section comment above a struct field becomes that FIELD's published prose.**
  zipdoc lifts the doc comment directly above a field, and `edge.Policy` grouped
  three fields under `// Platform-scope (admin-org row) …` — which shipped as the
  description of `cors_origins` alone. Give every published field its own comment;
  a header is not a description.
- **Two generic names were qualified BEFORE they could collide** (failure mode #5 in
  its cheap form): `bots.botView` → `BotRun` (apps/visor already publishes `botView`
  for a bot MACHINE) and `translate.Entry` → `MemoryEntry` (six packages under
  `apps/` declare a type called `Entry`). Neither was published yet, so both were
  free; the weave is only the gate once one of them is.
**The six-plugin pass (notify, product, referrals, validators, zero-trust,
blueprint): 19 typed, 4 refused, out of 23 operations that published NOTHING.**
All six subsets were at `described=0` — every operation carrying neither
`description` nor `summary`, which is exactly the set that projects to no prose,
no MCP tool, no CLI command and no typed SDK method. Now: notify 1/4, product
4/4, referrals 4/4, validators 4/4, zero-trust 4/4, blueprint 2/3. Each package
carries `untypedByDesign` + `TestEveryRouteIsTypedOrNamed` +
`TestEveryTypedOpIsDescribed` + `TestEveryPublishedFieldIsDescribed` in its own
`typed_wire_test.go`, read off the LIVE router, so the two ledgers must sum to
the served surface and a stale reason goes red too.

The 4 refusals are ONE class, and it is the one zip cannot express: **two 200
shapes at one address.** `POST /v1/notify/send{,/sms,/email}` return the bare
`SendResponse` for a single recipient and `{items:[SendResponse]}` for several
(apps/notify/notify.go handleSend); `GET /v1/blueprint/sbom` returns a bare
`Estimate` for `?template=<id>` and `{data:[Estimate]}` for none
(apps/blueprint/blueprint.go sbomRead). An op declares one Out, so either shape
would publish the other as a lie — worse than publishing none, because every
generated SDK binds it. `TestSendAnswersTwoShapes` MEASURES the notify pair
rather than asserting it, so the day zip can declare a polymorphic response the
conversion is a test away.

Three things the pass had to invent, all reusable:

- **A refusal moved ahead of the decode, without gating a sibling.** A typed op
  runs AFTER `op.invoke` unmarshals the body, so an identity check moved into the
  op answers 400 to an anonymous caller whose body is also malformed, where the
  untyped handler answered 403. `zip.App.With(mw)` composes middleware *around*
  the op and would fix it — but **zipdoc cannot resolve `With` as a router**
  (`routerPrefix` only knows `*zip.App` and a `.Group("literal")`), so it fails
  generation. The shape that works is a `g.Use(...)` on the subsystem's own group
  carrying a METHOD-scoped gate (`requireOrgOnWrite`, apps/{referrals,validators}),
  which leaves the sibling GETs — including the auto-registered `/health` — exactly
  as they were. For an admin leaf whose PREFIX belongs to another app
  (`/v1/admin/referrals` is affiliates'), the group is the exact leaf path and the
  op is declared on the App with its whole path.
- **`url:"-"` is not optional on a POST body field.** zip binds query OVER a
  decoded body, so a body field that shares a name with nothing at all still gains
  a higher-authority `?field=` twin the route never read. Every body-only field on
  a converted POST here carries it, and `TestQueryCannotOutrankTheClaimBody` pins it.
- **A query scalar is a STRING when the existing parse trims.** `strconv.Atoi(
  strings.TrimSpace(v))` accepts `?limit=%2050`; zip's `setScalar` does not, so an
  `int` field silently narrows what the route accepts. MEASURED: fiber
  percent-decodes a QUERY value (and reads `+` as a space) but does NOT decode a
  PATH segment, so the divergence is reachable on `?tokenId=` and unreachable on
  `/{tokenId}`. Both stayed strings anyway — two parse rules for one value is how
  they come to disagree (apps/validators `limitOf`/`parseTokenID`).

Latent defects found and fixed: **three more instances of the apps/plan prefix
defect** — `plugin/{product,zero-trust,referrals}/main.go` declared no
`Prefixes`, so `MountPrefixes`'s `/v1/<Name>` default covered NOTHING product
(`/v1/search-docs/*`, `/v1/vector/*`) or zero-trust (`/v1/networks`,
`/v1/mesh/services`, `/v1/edge/nodes`) serves, and covered only half of
referrals' (the two `/v1/admin/referrals/*` leaves were outside it); all three
now pass `manifest.PrefixesFor(...)`. And **apps/zt had no `cloud.Bridge` at
all** on any of its three prefixes — its own test harness mounts on a bare app
with no `Serve`, so every typed op there would have seen no org and 403'd a valid
request; `TestBridgeIsInstalledOnEveryPrefix` is the gate. Two response-side
holes closed the way the analytics pass closed its: 26 referrals properties and
12 zero-trust ones were about to reach openapi.yaml, every SDK and every MCP
inputSchema bare.

**The six-plugin pass (usage, venue, world, agent, bot, help): 19 typed, 8
refused, and all 27 published NOTHING before it.** Same work list rule — every
operation in `plugin/<name>/openapi.json` carrying neither `description` nor
`summary`. `usage` 5/5, `venue` 5/5, `help` 4/4, `world` 4 of 5, `bot` 1 of 4,
`agent` 0 of 4. Four things it taught that the earlier passes did not:

1. **The `sizedIn` trick cannot survive a SYNTAX error, and that bounds the whole
   "record it, judge it later" pattern.** captable's answer to a route that
   decides a status before it reads its body is an In whose `UnmarshalJSON`
   refuses nothing and records what it saw, so the handler can re-decide in the
   original order. It works for an oversized body and for one that parses to the
   wrong SHAPE — and NOT for bytes that are not JSON at all, because
   `json.Unmarshal` runs `checkValid` over the whole document before it invokes
   any custom Unmarshaler, so zip's `op.invoke` 400s before the input is ever
   built. Measured, not argued: `apps/help/typed_wire_test.go`'s
   `TestSyntacticallyInvalidJSONIs400Early` and `apps/venue`'s
   `TestSyntaxErrorIs400BeforeTheAdminGate`. The delta is real and tiny — a caller
   sending garbage bytes to a help center that does not exist now sees 400 instead
   of 404 — and it is the same one captable's writes.go already took. Do not claim
   the pattern preserves malformed-body ordering; it preserves the other two.
2. **A body-TOLERANT route CAN be typed, by decoding leniently rather than
   declining to decode.** `POST /v1/cloud/{provider}/accounts/{label}/sync` has
   never read its body; zip decodes one anyway. `venueAccountRef.UnmarshalJSON`
   decodes what parses and drops the error, so a stray payload stays the no-op it
   was (`TestSyncStaysBodyTolerant`). That narrows hard-won fact #4 — the
   *unconditional 400* is what blocks apps/tools' JSON-RPC door, not
   body-tolerance itself.
3. **Path params must stay `json:`-visible even when the document does not need
   them.** zip v1.18.11's `hasRequestBody` correctly publishes no requestBody for
   an input whose every field is a path param — but the MCP projection calls
   `op.invoke` with the args as the BODY and a nil path map (mcp.go), so
   `json:"-" url:"x"` would leave the MCP tool no way to name its target. The
   fleet's existing shape (`ID string \`json:"id"\`` with a comment saying the URL
   is the authority) is right, and bindURL binding path LAST is what keeps it
   safe.
4. **Two more mount-scope defects of the apps/plan shape, and one build-contract
   hole.** `plugin/venue/main.go` declared no `Prefixes`, so the standalone
   binary's scope owned only the `/v1/<name>` default — and venue is named
   "venue" and serves **/v1/cloud**, so it owned NOTHING it registers; fixed with
   `manifest.PrefixesFor("venue")`, which matters now because `cloud.Bridge` is
   what parks the org a typed op reads. Separately, `apps/bot` had **no
   Makefile** at all, so `make -C apps/bot openapi` could not run and that app's
   subset could never be regenerated by the per-app chain — despite mk/plugin.mk's
   own comment claiming "an app cannot have a main and no Makefile". `bot`'s is
   written; **`catalog`, `crawl`, `meet` and `zen` are still missing theirs**
   (`for d in plugin/*/; do a=$(basename $d); [ -d apps/$a ] && [ ! -f apps/$a/Makefile ] && echo $a; done`),
   and `plugin/gen-app-cmds` does not actually emit them.

`agent`'s four are refused for a reason no cloud edit can reach: they are
registered by **github.com/hanzoai/agent v0.1.3** (`agent.go:166-169`), not by
cloud — `apps/agent` calls `hz.Mount` and registers no route of its own, the
apps/tasks situation one module over. Two facts have to move upstream with them,
both named at the mount point: every handler resolves its caller through a
`func(*zip.Ctx) (Principal, bool)` and `POST /v1/agent` dispatches tools with the
LIVE `*zip.Ctx`, so hanzoai/agent needs a per-request bridge of its own (it
deliberately imports neither cloud nor ai); and that same route relays an upstream
4xx's status AND body verbatim (`round.go:104-110`), which is the apps/ml refusal
class and stays untyped even after the bridge lands.

**The earlier six-plugin pass (ai, destinations, dns, licensing, runtime, templates):

**The six-plugin pass (wallets, webhooks, account-bridge, ads, channels, code):
35 typed, 9 refused, and 7 of the 9 were already a recorded refusal.** The work
list came from the published subsets — every operation carrying neither
`description` nor `summary`, which is exactly the set that projects to NOTHING —
and it was 44 operations across six plugins, every one of them 100% undescribed.
Per plugin: wallets 8/8, webhooks 8/8, code 7/7, ads 6 of 7, channels 6 of 7,
account-bridge 0 of 7. Each of the five converted packages now carries
`untypedByDesign` + `TestEveryRouteIsTypedOrNamed` + `TestEveryTypedOpIsDescribed`
reading the LIVE router, whose two ledgers must SUM to the served surface.

- **`account-bridge` is apps/account's already-closed refusal, seen from the
  plugin side.** Its 7 operations are TWO registrations — `GET|POST /v1/billing/*`
  and the five-method `/v1/commerce/*` — verbatim per-tenant forwards on a greedy
  wildcard (`*1` to fiber, `{wildcard1}` to the document, so a bound In field and
  the published parameter cannot agree), already held as a CLOSED list by
  `apps/account/typed_wire_test.go`. Nothing to convert; the count is not a gap.
  **It is now zero of zero: the app is RETIRED** (below), and the closed list it
  filled is empty. A refusal that cannot be converted and cannot be deleted is
  usually a route that should not exist.
- **Three refusal classes, each measured rather than asserted.**
  `POST /v1/ads/campaigns/{id}/launch` is deliberately BODY-TOLERANT
  (`_ = c.Bind(&body)`, apps/ads/ads.go), so a malformed body launches on the
  stored account at 200 where `op.invoke`'s unconditional decode would 400 —
  `TestLaunchStillIgnoresAMalformedBody` pins it.
  `POST /v1/channels/{channel}/send` carries a PACKAGE-LOCAL 1 MiB body cap that a
  typed op never sees (cloud's global zip BodyLimit is far larger) *and*
  `DisallowUnknownFields`, which refuses a spoofed identity field LOUDLY where
  jsonenc.Unmarshal would drop it silently — `TestSendKeepsItsCapAndItsStrictness`
  pins both.
- **A route whose body OVERRIDES its query is typable — with one field per
  source.** `POST /v1/code/ask` has always read `?q=` first and let a non-empty
  body `query` win, which is the OPPOSITE of zip's body→query→path order. Spelling
  the two halves separately (`Q string json:"-" url:"q"` beside
  `Query string json:"query" url:"-"`) reproduces the original precedence instead
  of inverting it; `TestAskKeepsBodyOverQueryPrecedence` asserts all four
  combinations. Generalise it: `url:"-"` and `json:"-"` are not only opt-outs,
  they are how a route with TWO sources for one value stays declarable.
- **A query filter that 400s on a bad value must stay a STRING.** zip's
  `setScalar` silently leaves an unparseable int at the field's zero, so
  `GET /v1/channels/inbox?since=abc` would turn today's 400 into a read from the
  beginning. Where the handler DEFAULTS on a parse failure instead
  (`?limit=` on ads, webhooks and code search) an int field is wire-identical,
  because 0 is exactly the "absent or unusable" case those branches already
  answered — so this is per-route, not per-package.
- **Latent defects found by typing, all fixed here.** (1) FIVE packages had no
  `//go:generate zipdoc` directive at all, which is why 44 operations published
  nothing: the prose had nowhere to be lifted to. (2) `GET|POST /v1/webhooks/`
  published a TRAILING SLASH — failure mode #9, the group's empty leaf — for a
  collection every caller addresses without one; declaring the root on the app
  with its absolute path fixes the artifact and not the wire, and
  `TestTheCollectionRootHasNoTrailingSlash` proves BOTH spellings still reach the
  handler. (3) `apps/code`'s own test harness RECONSTRUCTED its seven routes by
  hand instead of calling the registration the binary calls, so nothing it
  asserted was evidence about the served surface; `routes()` is now a function and
  `newTestApp` calls it. (4) None of the five packages installed a `cloud.Bridge`
  of its own — each relied on Serve's app-wide install, which no package's own
  test harness runs, so the org path was untested everywhere. (5) Two one-name/
  two-shape collisions were about to enter the flat schema namespace, which
  `openapi.Weave` refuses: wallets' `Account` against books', and ads' `Campaign`
  against marketing's. Both unpublished names yielded — `WalletAccount`,
  `AdCampaign` — Go-level renames with no wire movement. A THIRD appeared at the
  REBASE, which is the lesson: apps/content landed a `channelList` for its SOCIAL
  channels while this pass was in flight, so a name that was free when it was
  chosen was taken by the time it merged. The weave caught it, channels yielded
  (`chatChannels`), and the wire key stayed `channels`. Check before you name AND
  re-check after you rebase — the namespace is fleet-wide and it moves.
- **One delta taken and recorded rather than glossed.** webhooks' `tenant` gate
  answered 401 with two different MESSAGES ("authentication required" vs "org
  scope required"); `principal.OrgFrom` folds both halves into one answer, so the
  typed ops answer one 401 naming both. Status, body shape and ordering are
  unchanged; `TestFailsClosedWithoutAValidatedPrincipal` drives both branches.

**The six-plugin pass (ai, destinations, dns, licensing, runtime, templates):
9 typed, 29 refused, and the 29 are 4 registrations.** The work list came from the
published subsets — every operation carrying neither `description` nor `summary`,
which is exactly the set that projects to NOTHING: no prose, no MCP tool, no CLI
command, no typed SDK method. It was 38 operations. Two things it taught:

- **A plugin can be 100% undescribed and still have almost nothing to convert.**
  Four of the six (`ai`, `dns`, `licensing`, `runtime`) publish 7 operations each
  that are ONE `All("/…/*")` registration apiece, exploded across the seven
  methods by the untyped projection. Each is a verbatim reverse proxy: greedy
  wildcard (`*1` to fiber, `{wildcard1}` to the document — a bound In field and
  the published parameter cannot agree), upstream status relayed through
  `c.Bytes(res.StatusCode, …)`, upstream Content-Type frequently not JSON, and no
  `All[In, Out]` registrar to hang seven ops on. Two are not even cloud's to type:
  `plugin/ai` is `github.com/hanzoai/ai`'s beego `ControllerRegister` behind
  `zip.AdaptNetHTTP`, `plugin/licensing` is `github.com/hanzoai/licensing`'s
  `http.Handler` behind the same. Each refusal is now written AT its registration
  (apps/dns/dns.go, apps/runtime/ops.go, apps/ai/ai.go) rather than only here.
- **`plugin/ai` is the largest hole in the fleet document, and it is upstream's.**
  That one wildcard stands for ~200 real routes — `/v1/chat/completions`,
  `/v1/models`, `/v1/messages` — so the AI API appears in openapi.yaml, in every
  generated SDK and in the MCP tool list as seven undescribed `{wildcard1}`
  operations and in no other form. Fixing it means declaring ops inside
  `hanzoai/ai`, next to the handlers; nothing cloud can do at the mount point
  reaches it.

`templates` went 5 of 5 (a closed ledger, `TestEveryRouteIsATypedOp` — no
refusals to name), `destinations` 4 of 5 (`untypedByDesign` +
`TestEveryRouteIsTypedOrNamed`), both reading the LIVE router of the real
`Mount`. Three findings worth carrying forward:

1. **`url:"-"` is not optional on a body field, and zip v1.18.11 is what makes it
   possible.** The binder fills an In field from the QUERY as well as the body
   (body → query → path, increasing authority), so a converted POST silently
   starts accepting `?slug=` — on templates' publish that redirects the write to a
   name the body never asked for. The untyped handler read `c.Bind`, which is the
   body and nothing else. Every body-only field on both write ops carries
   `url:"-"`; `TestTheQueryStringCannotRedirectAWrite` is the measurement. This is
   a WIRE-WIDENING class no status-code test sees, and it applies to every
   POST/PUT/PATCH conversion in the fleet.
2. **Typing is what makes you enter the flat schema namespace, and both packages
   collided on entry.** `destinations.Status` is `apps/plugins`' `Status`;
   `templates.Template` is `apps/guide`'s `Template` (a notification template).
   Neither collision existed while the routes were untyped, because an untyped
   route contributes no schema at all. The published name yields nothing and the
   unpublished one qualifies: `DestinationStatus`, `DestinationField`,
   `StarterKit` — all Go-level renames with no wire movement.
3. **Failure mode #7 (the phantom bodyless-POST requestBody) is FIXED in zip
   v1.18.11** — `hasRequestBody` (openapi.go) refines `hasBody` by skipping an
   input whose every field is already a path param. `POST /v1/destinations/
   {platform}/test` types cleanly and publishes no request body. The 27-instance
   class named above should be re-counted on the current pin, not inherited.

**The follow-on pass re-derived the same 29 refusals and found what the op-level
count cannot see: the RESPONSE side was undocumented in both converted packages.**
Re-running the work list from the published subsets reproduced the ledger exactly
— 29 undescribed operations, 5 registrations, every one re-refused against zip
v1.18.11 source rather than against the prose above (`typed.go:302-311` is the
citation: a typed op's only response path is `c.JSON(out)` under its DECLARED
status, which is what a verbatim relay cannot survive). Nothing new was
convertible. But **26 published schema properties across the four view types
carried NO description** — every property of `DestinationStatus` (the card all
five destinations routes answer with) and of `DestinationField`, and eleven of
`StarterKit` plus `Variant.source`. They reached openapi.yaml, every generated SDK
and every MCP inputSchema bare.

The split is the lesson, and it is the same one company found: the In types —
written AT the conversion — described every field, while the OUT types, which
predate it, described almost none. **Typing a route documents its ADDRESS and its
SHAPE; it does not document the shape's FIELDS.** A caller could see that a
destination card carries `connected`, `enabled` and `live` and nowhere that those
are three DIFFERENT facts (configured once / forwarding now / a credential still
resolves) — which is the whole distinction an operator acts on — or that
templates' `tier` and `rating` are public-catalog curation no request can set.
`TestEveryPublishedFieldIsDescribed` now gates both packages, mutation-checked to
fail on a single blanked description.

**Do not read this as two packages' problem.** Running the check below over the
committed subsets the moment it existed put the class at **1,601 bare published
properties across 17 packages** — admin 710, agents 209, visor 153, books 100,
guide 90, platform 69, automations 69, framework 38, marketing 29, cloudflare 28,
compliance 27, plugins 25, pricing 18, o11y 16, provisioning 12, git 7, company 1
— against 12 packages that describe every property they publish. Several of the
17 are packages this table already marks done, which is the point: the op-level
gate every finished pass installed is blind to this, so "finished" has meant the
request side only. Count it from the subsets, never tally it in prose:

    python3 - <<'EOF'
    import json,glob,os
    for f in sorted(glob.glob('plugin/*/openapi.json')):
        d=json.load(open(f)); bare=[f'{n}.{p}'
          for n,v in (d.get('components',{}).get('schemas',{}) or {}).items()
          for p,s in ((v.get('properties') or {}) if isinstance(v,dict) else {}).items()
          if isinstance(s,dict) and not s.get('description')]
        if bare: print(os.path.basename(os.path.dirname(f)), len(bare), bare[:8])
    EOF

The same pass moved `dns` and `runtime` from a refusal written in prose at the
registration to one that is GATED — `untypedByDesign` +
`TestEveryRouteIsTypedOrNamed` in `apps/{dns,runtime}/typed_wire_test.go`, reading
the live router of the real `Mount`, so a route added to either is typed by
default and a reason that stops being true goes red. `ai` and `licensing` stay
ungated on purpose: their registrations are in another module, so there is no
cloud-side route for a cloud-side gate to hold.

**The six-plugin pass (graph, meet, prefs, settings, share, admission): 8 typed,
3 refused, out of 11 operations that published NOTHING.** Small subsets, and the
finding is not in the count:

- **Five apps have a `plugin/<app>/main.go` and NO `apps/<app>/Makefile`, and
  `mk/fleet.mk` reads `APPDIRS := $(wildcard apps/*/Makefile)` — so `openapi-check`,
  the gate that regenerates the document from source and fails on drift, has never
  regenerated `plugin/{meet,bot,catalog,crawl,zen}/openapi.json`.** Five published
  subsets sit OUTSIDE the only gate that can catch failure mode #1, which is the
  eight-path ingress loss. `apps/meet/Makefile` is added here (its subset
  regenerated clean, so no drift had accumulated yet); `bot`, `catalog`, `crawl`
  and `zen` are still outside. The claim in each generated Makefile's own header —
  "Written from the same apps.Wire() parse that writes plugin/<app>/main.go, so an
  app cannot have a main and no Makefile" — is false today: `plugin/gen-app-cmds`
  scaffolds the main and writes no Makefile at all (`grep -c Makefile
  plugin/gen-app-cmds/main.go` → 0). Find them with:

      for p in plugin/*/main.go; do a=$(basename $(dirname $p)); \
        [ -d apps/$a ] && [ ! -f apps/$a/Makefile ] && echo "$a"; done

- **Failure mode #9 (the empty leaf) was live in two more places, and fixing it
  cost nothing.** `prefs` and `share` each declared their collection root as
  `g.Get("", …)` on a group, so `openapi.yaml` carried `/v1/prefs/` and
  `/v1/share/` — paths this API has never served — beside every sibling without
  one. Declaring the root on the app with its whole path (`zip.Get(zapp,
  "/v1/share", …)`) fixes the document AND keeps the operationId: the untyped
  projection derived `get_v1_share` from the slashed path and `defaultOpID` derives
  the same from the unslashed one, so no SDK method moves. The untyped PATCH
  sibling moved with it, or the document would have split one resource across two
  keys. Fiber is non-strict, so both URL forms still answer;
  `TestPrefsAnswerAtBothPathForms` and `TestSharesAnswerAtBothPathForms` pin that.
- **The three refusals are all "zip cannot state this", and each is MEASURED.**
  `POST /v1/meet/getToken` answers the raw join token as `text/plain` — the office
  client reads it with `res.text()` — and a typed op always marshals JSON.
  `GET /v1/meet/health` answers 200 or 503 with the SAME body, `ready` being the
  whole dashboard fact at both, which is the multi-status gap (#78): a typed op's
  only refusal is a returned error, rendered as zip's flat `{status,code,error}`,
  so `ready:false` would vanish from the degraded answer. `PATCH /v1/prefs` is
  three facts at once — a 16 KiB REQUEST-BYTE cap answering 413 that a typed op
  cannot see (the #2 body-cap class), an empty body and a literal `null` body each
  answering 400 where `op.invoke` skips the decode and `null` decodes to a nil map,
  and an OPEN key space whose only carrier is `map[string]any`, whose `typeName` is
  `""` so `hasRequestBody` publishes no request body at all. `apps/meet/
  typed_wire_test.go` and `apps/prefs/wire_test.go` hold those wires, so a later
  conversion has a ledger rather than a re-derivation.
- **Failure mode #8 gained one instance and it is unavoidable today.**
  `settingsReq.config` is `map[string]any` and publishes
  `additionalProperties:{"type":"object"}` — false, since a config value is
  routinely a string or a number. The alternatives all MOVE THE WIRE:
  `json.RawMessage` changes `{"config":null}` from "store `{}`" to "store `null`"
  and stops re-encoding, `map[string]json.RawMessage` changes number formatting and
  large-int precision. Wire preservation wins; the fix is the one `reflect.Interface`
  case in zip's `schemaOf` (an unconstrained element is `{}`, not an object).
- Three `cloud.Request` entries were added and each is a request FACT, not a
  tenant: graph FORWARDS the caller's `Authorization` to the indexer/graph when no
  service token is configured, prefs' isolation key is the qualified
  `<owner>/<name>` rather than the org, and admission's `?host=` default is the
  request's own Host. All three fail closed off the HTTP path. Two of them had NO
  test at all before — `apps/graph`'s forwarding and `apps/admission`'s Host
  fallback both would have degraded silently (a 200 with an anonymous upstream
  read; a 200 with `known:false` for every guard that omits the query).

Re-measure rather than trusting the table — with the ONE command below, because
the two this file used to carry were each half-right and disagreed by 83 routes:

    for d in apps/*/; do a=$(basename $d); \
      u=$(grep -rn --include='*.go' -E '\.(Get|Post|Put|Patch|Delete|All)\("(/|")' $d \
          | grep -v _test.go | grep -vE '(^|[^.[:alnum:]])zip\.(Get|Post|Put|Patch|Delete)\(' \
          | grep -vcE ':[0-9]+:[[:space:]]*//'); \
      t=$(grep -rn --include='*.go' -E 'zip\.(Get|Post|Put|Patch|Delete)[[(]' $d | grep -vc _test.go); \
      [ "$u" -gt 0 ] && printf '%s %s %s\n' "$a" "$u" "$t"; done | sort -k2 -rn

**A route is a VERB PLUS A PATH, and the measure has to say both** — otherwise it
counts values that merely share a method name. Two mistakes, both live in this
file until now, in opposite directions:

- **No path anchor** counts every `hdr.Get("Retry-After")`, `form.Get("team_id")`
  and `vm.Get("console")` as an untyped route: **83 phantoms across `apps/`**,
  which is why `integrations` read 45 when it serves 19, `platform` 32 for 30,
  `tools` 18 for 16 and — the reason this note exists — **`ingress` read 1 when it
  serves 0**, on the strength of one `r.Header.Get("X-Forwarded-Proto")` in
  `middleware.go`. This is the same miscount already documented for team ("the
  other 26 hits are `hdr.Get(\"Retry-After\")`"), rediscovered because the command
  was never fixed — a wrong count is not a documentation nit, it dispatches an
  agent at a package that has no work left in it.
- **One call site is not one route.** A registration inside a LOOP over a table
  registers as many routes as the table has rows, and every line-based count sees
  one. `apps/plan` read as ~4 untyped and served **15** — its twelve fixed
  sections were one `app.Get(b.path, …)` inside `for _, b := range fixed`. That
  under-count is why a 15-route surface sat in tranche F ("1–11 routes each"). It
  is the same lesson as the over-count and it cannot be fixed by a better regexp,
  because the number of routes is a run-time fact about the table: **trust the
  published subset, `plugin/<name>/openapi.json`, which is a projection of the
  live router.**
- **Anchoring on `("/` alone** (the other command, below) drops the EMPTY-leaf
  registrations, which are real routes at a collection root: 7 of them, in
  `apps/prefs` (2), `apps/webhooks` (2), `apps/share`, `apps/crawl`,
  `apps/destinations`. So the anchor is a path — `("/` **or** `("" ` — and the
  `//` filter is not optional either: 11 of the empty-leaf hits are comment lines
  quoting the form.
- **`grep -v 'zip\.'` is a LINE filter standing in for a syntactic one**, and it
  is the third mistake — the one that makes the table skip packages entirely. The
  discriminator wanted is the RECEIVER of the call (`zip.Get(` = typed, package-
  qualified generic; `<router>.Get(` = untyped, method on a router), but dropping
  every line that merely CONTAINS `zip.` also drops untyped registrations whose
  handler argument names the package on the same line — an inline
  `func(c *zip.Ctx) error`, or `zip.AdaptNetHTTP(`. Anchor it on the call:

      grep -vE '(^|[^.[:alnum:]])zip\.(Get|Post|Put|Patch|Delete)\('

  Measured across `apps/` at this merge: **660 by the documented command, 675
  anchored — 15 hidden routes in 9 packages** (base +2, plan +3, product +4,
  commerce/dataroom/esign/o11y/bot/websearch +1 each). Two of those read **ZERO**
  and are not zero: `apps/plan` serves 3 (`/health`, `/resolve/:id`,
  `/entitlements/:id`, plan.go:77-109) and `apps/product` serves 4
  (`/v1/search-docs/{indexes,stats}`, `/v1/vector/{collections,stats}`,
  product.go:80-141) — every registration an inline `func(c *zip.Ctx) error`, so
  the filter eats all of them and the partition table has never dispatched either
  package. This is the SAME failure the two bullets above describe, in the
  direction that costs more: a phantom sends an agent at nothing, a hidden route
  means nobody is ever sent. o11y's own 7-vs-8 was the tell (`a.All("/v1/sentry/*",
  zip.AdaptNetHTTP(…`, o11y.go:231) — one route short of the 8 its conversion
  recorded, which is how this was found.

Corrected, `apps/` holds **675** untyped route registrations (660 under the
un-anchored filter above; re-run both, a difference between them IS the list of
hidden routes). The number to trust
it against is `integrations`, whose 19 the corrected measure reproduces exactly
and independently — the count its own conversion recorded as refusals.

**Both commands are still blind to a route whose PATH is a variable**, and this
is structural, not a tuning problem: every anchor above ends in `("` or `("/`, so
`app.All(p, h)` inside `for _, p := range prefixes` matches nothing. It is not a
corner case. `apps/exec` reads **ZERO** under both commands and serves **56
published operations** — 4 prefixes x {exact, `/*`} x 7 methods — which is why it
has never appeared in a tranche despite being the largest undescribed block per
line of source in the repo. Same shape in `apps/knowledge` (9 registrations off
`p+"/search"`… , subsystem.go:61-69), `apps/iam` (iam.go:258-259, 282) and
`apps/commerce` (mount.go:551). The lesson is the one the phantom/hidden pair
already teaches, one level up: **the grep is a hint and the published subset
(`plugin/<app>/openapi.json`) is the denominator.** Count operations, not lines.

**`apps/exec` is 0 typed of 56, and that is the finished answer, not a to-do.**
It is a transparent edge: `Mount` hands all 8 paths to
`httputil.NewSingleHostReverseProxy` and the sandboxed code executor supplies
every byte, every Content-Type and every status. There is nothing here to
describe and four independent wire facts that a typed op would move — a verbatim
upstream status (`zip` answers its own declared one, typed.go:305-311), response
fields this repo never named (an `Out` drops them), a multipart `/v1/upload` body
(`zip` decodes every non-empty typed body with `jsonenc.Unmarshal`, typed.go:242)
and a byte-bodied `/v1/download/{id}` (a typed op always `c.JSON`s, typed.go:311).
The 16 `OPTIONS`/`TRACE` operations are not expressible at all: `zip` has typed
registrars for Get/Post/Put/Patch/Delete and nothing else. `openapi.Register` is
refused too rather than reached for — on this surface it could only publish a
guess at a contract this repo does not own (`{lang, code, files?}` is
`@librechat/agents`'), and `openapi.Binary`'s `application/octet-stream` is not
what a multipart envelope is. The refusal is a GATE:
`apps/exec/typed_wire_test.go` holds the closed ledger (`untypedPaths` x
`servedMethods`, crossed to 56) plus eight measurements of those wire facts
through the REAL `Mount`, so a route added here is typed by default, a stale
reason goes red, and the day one becomes typable
`TestTheSurfaceIsWhollyUndescribed` goes red and sends the next agent to this
paragraph. Two things this surface cannot express and a reader should not assume:
it has **no tenancy** — one process-wide `CODE_EXEC_API_KEY` (the gateway bypasses
these paths, so `principal.OrgFrom` is never consulted) and session ids on
`/v1/files/{sid}` and `/v1/download/{id}` are opaque and org-unscoped, so
cross-session reads are prevented by id unguessability and not by a tenant check;
and it claims three GENERIC top-level nouns (`/v1/files`, `/v1/upload`,
`/v1/download`) which outrank `ai`'s bare `/v1/*` by static-prefix specificity
regardless of mount order — harmless today (ai's files live at `/v1/ai/files`),
and a silent shadow the moment `ai` adds OpenAI's own `/v1/files`.

**Collisions, and the resolution.** Source does not collide; two artifacts do —
the regenerated `openapi.yaml` golden and `go.sum`. Both resolve the same way:
**rebase onto main, then regenerate** (`make -f mk/fleet.mk surface-check`). The
generator is deterministic, so a regenerated golden is a function of the routes,
never a merge to hand-resolve. Never hand-edit `openapi.yaml` or a
`plugin/*/openapi.json`.

## The typed migration: one registry entry, or a route and nothing else

Measured at `e88ea216`, and re-measurable — do not trust these numbers past the
next few merges, run the commands. (They moved by two operations between the
branch point and the merge; that is the rate.)

    # typed ops (the generic package-level registrars; types are INFERRED,
    # so they read as ordinary calls — bracket syntax appears only in comments)
    grep -rEn 'zip\.(Get|Post|Put|Patch|Delete)\(' --include='*.go' . \
      | grep -v _test | grep -vE ':[0-9]+:[[:space:]]*//'          # 165, 15 pkgs

    # untyped: a METHOD call on a router/group value. Same path anchor as the
    # per-app measure above — verb PLUS path, or it counts hdr.Get("…") too.
    grep -rEn '\.(Get|Post|Put|Patch|Delete|All)\("(/|")' --include='*.go' . \
      | grep -v _test | grep -vE '(^|[^.[:alnum:]])zip\.(Get|Post|Put|Patch|Delete)\(' \
      | grep -vE ':[0-9]+:[[:space:]]*//'                             # ~900, ~95 pkgs

The discriminator is `zip.X(` (package-qualified generic) versus `<receiver>.X(`
(method on `*zip.App`/Router) — NOT the presence of square brackets, and NOT the
presence of `zip.` anywhere on the line, which also eats an untyped route whose
handler is an inline `func(c *zip.Ctx) error` (15 of them, see the third bullet
under "Partitioning the remaining work"). The
published document is the honest denominator: `openapi.yaml` carries **1398
operations across 984 paths, of which 164 have a description.** The other ~1234
are route only — no MCP tool, no CLI command, no SDK method, no schema, no
prose.

The typed packages are `apps/admin` and its eight sub-packages, plus `apps/account`,
`apps/agents`, `apps/automations`, `apps/company`, `apps/compliance`, `apps/crm`, `apps/framework`,
`apps/git`, `apps/guide`, `apps/ingress`, `apps/integrations`, `apps/marketing`,
`apps/ml`, `apps/o11y`, `apps/plugin`, `apps/provisioning`, `apps/search`, `apps/team`, `apps/visor`.

`provisioning` is **21 of 28** — and the 28 were the WHOLE surface, every one of
them publishing nothing: seven kinds × four verbs, registered from a loop over
`kinds` with a computed path (`app.Post("/v1/"+k, create(s, k))`). That shape is
untypable twice over and it is the general lesson, not a provisioning quirk: a
computed path is not a constant, so `cmd/zipdoc` refuses it outright ("route path
is not a constant string, so the operation has no identity to document",
extract.go:189), and a handler returned by a FACTORY is a call expression with no
doc comment to lift. **A loop that registers N routes publishes prose for none of
them, however well the handler is commented** — so the fix is one declaration per
published operation (`apps/provisioning/typed.go`), which is what every projection
keys on anyway. The 21 reads and deletes are typed ops; the 7 creates are refused
for the `cloud.DenyResource` reason `apps/ml` and `apps/company` already carry (a
402/503 whose body is the fleet's NESTED `{"error":{code,message}}`, which a typed
op's error cannot be — `errorHandler` renders zip's flat `HTTPError`, and writing
the nested body inside the op does not escape it because a nil Out makes zip stamp
`cmp.Or(op.Status, 204)` over the 402). That refusal is GATED, not prose:
`untypedByDesign` + `TestEveryRouteIsTypedOrNamed` + `TestEveryTypedOpIsDescribed`
(apps/provisioning/typed_wire_test.go) read the router of the REAL `routes()` and
require the two ledgers to SUM to the served surface. The seven still declare
their bodies through `openapi.Register`, so `provisionRequest`/`provisionResult`
reach the document and an SDK caller has somewhere to put the name.

Two things this partition is worth reading for. **Tenancy could not go through
`principal.OrgFrom`**, and the reason is a wire fact rather than a preference:
`tenant()` folds the org through `sanitizeOrg` (the slug every physical name, S3
bucket and `tenant-<org>` namespace is keyed on — a typed read that skipped the
fold would look in a different bucket than the create wrote) and buckets an
ORG-LESS SuperAdmin under the literal `"admin"` org, which `OrgFrom` refuses
outright. So `tenantOf` reaches the request (`cloud.Request`, pinned) and asks the
SAME `tenant()` the untyped create beside it uses — one function, so the two
halves of one surface cannot key their tenancy differently. This is the `apps/ml`
shape exactly, and it is the second package where the "admin bucket" is the thing
`OrgFrom` cannot express; assume it before assuming `OrgFrom` will do. **And the
listing's Out is a NAMED SLICE** (`provisionedList []provisionedSummary`), because
the wire is a bare JSON array: wrapping it in an envelope struct would have been
the natural typed shape and a silent wire break. `TestTypedReadsKeepTheirWire`
asserts the empty listing's BYTES are `[]`, which is what proves it.
`crm` is 19 of 20, and its partition is now a GATE rather than prose:
`untypedByDesign` + `TestEveryRouteIsTypedOrNamed` (apps/crm/typed_wire_test.go)
fail the same three ways team's and git's do, so the count cannot outlive the
route that falsifies it. Its one refusal, the public Startup Program intake POST,
names a zip gap that is NOT #78 and is worth reading as its own family: **per-op
projection SCOPE**. The IP rate limit is fiber middleware, and zip's MCP arm
dispatches a `tools/call` straight into `op.invoke` (zip@v1.18.6 mcp.go:152)
while the CLI's `LocalInvoke` does the same (cli.go:427) — neither runs the
route's middleware chain, and there is no per-op way to decline a projection
(the only OpOptions are `WithSummary`, `WithTags`, `WithOperationID`,
`WithStatus`; `MCP.Disabled` is app-wide). So typing it publishes an unmetered
alias of the one deliberately metered public write in the surface — and worse
than unmetered, because `apply` never calls `tenant()`: it writes into
`intakeOrg(s)`, the deployment BRAND's pipeline, so the alias would let any
caller reaching `/mcp` inject unbounded rows into the brand's own CRM. The 64 KiB
`maxIntakeBody` cap is the second wire fact and fails the same way (`op.invoke`
unmarshals before the handler, so the cap could only run after the parse it
exists to prevent). Its 200-vs-201 split would shim, and the honeypot's third
body needs only `omitempty` — neither is what blocks it. **A route whose safety
depends on HTTP middleware cannot be projected onto transports that skip
middleware**; that wants either a per-op projection opt-out or middleware zip
runs on every arm, and it is not closable inside cloud. All five facts were
RE-READ in zip **v1.18.8** (the newest published; cloud pins v1.18.6) — it adds
ask/declare/ops/peer/tenant and changes none of them, so 19 is still this
package's floor and the refusal now cites v1.18.8 line numbers so the next agent
does not repeat the reading. Re-verifying it also surfaced a LIVE defect in the
meter the refusal leans on: the limiter is keyed on `c.Fiber().IP()`, which is
the TCP peer because zip's `fiber.Config` sets no `ProxyHeader` and no trusted
proxy, so for proxied public traffic — the only traffic it exists to bound —
every submission shares ONE 20/min bucket, together with the three staff
application routes registered after it. The defect is stated once, at
`intakeRateLimit` (apps/crm/applications.go), including why the one-line fix is
wrong: `middleware.RateLimit`'s bucket map is only ever reset, never evicted, so
keying it on real client IPs grows without bound — which is exactly why
`EdgeRateLimit` carries its own eviction instead of reusing that primitive. It is
a metering decision, not a typing one, so typing left it alone.
crm is also the worked example of the half of the surface an op-level count does
NOT measure. Typing a route documents its ADDRESS and its SHAPE, never the
shape's FIELDS: those come from doc comments on the In/Out struct FIELDS, which
zipdoc lifts per field. crm shipped fully-described REQUEST types beside RESPONSE
types with **65 bare properties** — every field of `Company`, `Contact`,
`Opportunity`, `Application`, `ScreenResult` and `StageEvent` reached
openapi.yaml, all four generated SDKs and the MCP `inputSchema`s with no
description at all, because those are store ROW types nobody had written field
prose on. A reader could see `arr` was an integer and nowhere that it was CENTS.
The row types now carry the prose and `TestEveryPublishedFieldIsDescribed` gates
it. **Check this in every package the migration touches: a package can be "100%
typed" and still publish a wholly undescribed response surface**, because the two
facts live in different places and only the op-level one is counted. The class is
fleet-scale, not a crm quirk — measured on this commit's `openapi.yaml`, **1,424
of 2,716 published properties (52%) carry no description, and 195 schemas are
100% undescribed** (worst: `appView` 26, `Wire` 21, `Node` 20, `Volume` 20,
`Totals` 18). crm is 0 of 65. Re-measure with:

    python3 -c 'import yaml;d=yaml.safe_load(open("openapi.yaml"));s=d["components"]["schemas"];
    p=[(n,k) for n,v in s.items() if isinstance(v,dict) and isinstance(v.get("properties"),dict)
    for k,f in v["properties"].items() if not (isinstance(f,dict) and str(f.get("description","")).strip())];
    print(len(p))'
`team` is 9 of 19, and its partition is a GATE rather than prose:
`untypedByDesign` (typed_wire_test.go) is the closed list of the 10 refusals —
two WebSocket upgrades (transactor, collaborator), the account JSON-RPC
envelope (refusals are HTTP 200 `{error: Status}`, INCLUDING for an unparseable
body), the tolerant cookie PUT (a body it cannot parse falls back to the bearer
where a typed In answers 400), two OAuth 302 redirects, the wallet page's
bytes, and the multipart upload / raw-bytes download — and
`TestEveryRouteIsTypedOrNamed` fails on any route that is neither typed nor
named there, so the next team route is typed by default. All ten were RE-VERIFIED
against zip v1.18.6's own source rather than against the comment that claimed
them — `op.invoke` json.Unmarshals any non-empty body BEFORE the handler
(typed.go:227, which is what turns the RPC's and the cookie PUT's tolerated
garbage into a 400), the REST arm ends in `c.JSON(out)` with no raw-bytes or
upgrade path (typed.go:303, the wallet bytes, the blob download and the two
WebSockets), and `WithStatus` panics on a non-2xx (the two 302s). v1.18.7 is
byte-identical to v1.18.6, so the floor for this package is 9 until zip gains
raw-body binding, a bytes Out, or a non-2xx status; a fleet count that keeps
listing team as 19 untyped is what sends the next agent to redo the work.
**Re-verified again at zip v1.18.8** (the newest tag), because "the floor is 9"
is a claim about a DEPENDENCY and expires when the dependency moves — the three
capabilities are still absent: `WithStatus` still panics below 200/above 299
(typed.go:110-113), the REST arm still ends in `c.JSON(out)` with no bytes or
upgrade path, and `op.invoke` still json.Unmarshals any non-empty body into `In`
before the handler and answers `ErrBadRequest` on a parse failure. v1.18.8's
diff against v1.18.7 is `app.ops`→`app.registry` plus new ask/declare/ops/peer/
tenant files and the `runtime`→`js` move; none of it touches the three. Do not
re-derive this from the prose — the check is four greps against the module cache,
and it is the only thing that can retire a refusal.
What typing this package DID surface is one route away from the ops: team's
second plane is app-level (`/collaborator` — the Team front derives both the
Y.js WebSocket and the markup-snapshot RPC from `COLLABORATOR_URL`, not from the
`/v1/team` base), and `manifest.Apps` named only `/v1/team`, so in the plugin
fleet both fell past every prefix to the console at `/`: the collaborative
editor got the HTML shell, and the TYPED collaborator RPC — published in
`openapi.yaml` and therefore in every generated SDK and the MCP tool list —
reached no app at all. team's row names `/collaborator` now and the two entries
are gone from the router oracle's `unreachable` ledger. The general lesson: a
route's typed-ness is invisible to the manifest, so an app whose surface is not
wholly under one `/v1/<name>` prefix can publish a perfect op the fleet never
delivers, and only `manifest/router_test.go` asks the router.
The second visit to team found nothing left to type and one thing left to
DESCRIBE, which is the lesson worth carrying: "9 of 19, floor reached" was true
about ops and silent about fields. team published **3 bare properties** —
`ProviderInfo.name`, `ProviderInfo.displayName`, `botMember.active` — each
reaching openapi.yaml, all four generated SDKs and the MCP `inputSchema` with no
description, for the crm reason exactly (the two facts are counted in different
places and only the op-level one was counted). `botMember.active` is the one that
cost a reader something real: it is not the agent's own `active` flag but a
DERIVED projection (`botActive`: empty/"active"/"ready" are live, archived and
retired are not), so an SDK user could see a boolean and nowhere that a retired
agent stays in the roster as an inactive member with its authorship intact.
`TestEveryPublishedFieldIsDescribed` now gates team the way it gates crm, and it
was proven to BITE by stripping the `active` prose and watching it name
`botMember.active`. **A package reporting "100% of typable routes" says nothing
about its field surface — run the field check on every package the migration
calls done**, including the ones already marked done.
`compliance` is 16 of 17 and now carries the SAME gate, which is the part worth
copying ahead of any remaining conversion: the gate reads the LIVE router
(`openapi.Spec` for what is served, `openapi.Typed` for what carries a registry
entry) rather than the source, so a route added anywhere in `routes()` surfaces
whether or not anyone remembers this file, and its second arm fails on a
`untypedByDesign` entry naming a route the app no longer serves — the refusal
list cannot rot into stale prose. Both arms were proven to BITE by emptying the
list (it named the webhook) and by adding a route that does not exist (it named
the staleness); a gate nobody has watched fail is not known to run. Its one
refusal is re-verified against zip v1.18.6's own source, not against the comment
that claimed it: the HMAC covers the EXACT received bytes
(`apps/idv/webhook.go` `Verify`: `mac.Write(body)`) which zip has already
unmarshaled into `In` before the handler runs (`typed.go:234`), so a re-encoded
`In` is not the signed value; and the route answers TWO 200 shapes (the
reconciled check, or `{"ignored": …}` for a reference it does not know) where an
op declares exactly one `Out` — unioning them would add zero-valued fields to
the no-op body, which is a wire change. Re-check it when zip gains raw-body
binding or multi-status (#78); until then 16 is this package's honest floor.
The webhook is the shape of what an untyped route COSTS, visible in the
published document: `openapi.yaml` carries
`POST /v1/compliance/verifications/webhook` with an `operationId` and a tag and
NOTHING else — no description, no requestBody, no responses — because
`openapi.go`'s generator reads prose and schema off `a.ops` (the typed registry)
and an untyped route is not in it. That is not a compliance defect to fix in
compliance; it is the fleet-wide reason the migration exists.
`apps/admin/core/typed.go` states the rule for that surface: every `/v1/admin/*`
route is a typed op. Five carry NO untyped route at all — `admin`, `marketing`,
`plugin`, `search` and now `ingress` (18 ops, converted whole in one pass).
`o11y` is the other end of the spectrum and worth reading for it: 12 of 20, with
each of the 8 refusals named in its own LLM.md — verbatim status/body proxies, a
`text/plain` receipt that must ACCEPT an unparseable body, and a wildcard.
`git` is COMPLETE at 24 typed / 24 refused, and its 24 fall into exactly four
wire families named in apps/git/LLM.md — a raw-byte HMAC webhook, the smart-HTTP
pack protocol (6), server-rendered HTML (12), and the ZAP envelope adapters (5);
its two `cloud.Plane()` ops are typed with NAMED handlers because a closure
gives zipdoc nothing to lift (the closure form shipped once and left `zipdoc
-check` red on main). Its partition is now a GATE like team's, not prose:
`untypedByDesign` + `TestEveryRouteIsTypedOrNamed`
(apps/git/typed_wire_test.go) fails three ways — a served operation neither
typed nor named, a name git no longer serves, and a name that IS a typed op —
so a "COMPLETE" claim can no longer survive the route that falsifies it. Copy
that form; prose cannot fail, which is why both git's and team's counts moved
into a test.
The list moves every few merges: RE-MEASURE per app rather than trusting it, and
note the count is a heuristic that reads `r.Header.Get("X-...")` as a route, so
read the hits before believing a non-zero remainder (ingress's last "1" is one).
`integrations` is the sharpest case of that heuristic lying: it measures 45
untyped and serves **19**, because the other 26 hits are `hdr.Get("Retry-After")`
and the `// app.Post(…)` MOUNT HANDOFF blocks each adapter file carries. It is
COMPLETE at 22 typed / 19 refused, and the 19 fall into exactly three wire
families named at its `routes()` — 8 legs that answer 302 (`zip.WithStatus`
PANICS on a non-2xx, typed.go:104), 5 that answer text/html and set `__Host-`
cookies (a typed dispatch ends in `c.JSON(out)`), and 6 inbound webhooks. The
webhooks split on WHY, and the split is the reusable part: four are signed over
the RAW received bytes (Slack/GitHub HMAC, Discord Ed25519) which the decoded In
is not, while `teams/events` and `telegram/webhook` are header-authed and refuse
for a different fact — they answer an EMPTY 200 to a body they cannot parse, and
zip's `invoke` unmarshals BEFORE the handler (typed.go:227), so typing them would
turn that 200 into a 400 and retry-storm the platform. Do not group Teams and
Telegram under "raw-byte signature": that was the prose's own error before it was
checked against the code, and it is why the taxonomy now cites line numbers.

`apps/automations` (14 of 18) converted with its wire pinned by its own HTTP
tests (403 gating, 201 create, 204 delete, the byte-identical `/pieces` alias),
and surfaced the two defects typing exists to surface: the group had NO
`cloud.Bridge` — no typed op could ever have resolved its org there — and the
package had no `//go:generate zipdoc` directive, so no prose could have reached
the document. Its four refusals each name a wire fact at the registration AND
the handler: POST `/flows/{id}/operations` answers TWO success bodies (the Flow
on CHANGE_STATUS, else the FlowVersion — one Out cannot hold both, #78's
family); POST `/runs/{id}/resume` takes an ARBITRARY JSON value (object, array,
string, number, null) delivered verbatim to the waitpoint, size-gated on the
RAW bytes; POST `/hooks/{source}/{event}` dedupes on a content hash of the RAW
received bytes and reads two contract headers (X-Idempotency-Key,
X-Causation-Depth); POST `/mcp` is JSON-RPC, which answers an unparseable body
HTTP 200 with a -32700 error object where zip's pre-handler unmarshal would
400. The MCP door's tools are not lost to the projections — `tools.Register`
publishes every connector action on the unified tool plane. The surface's one
sub-mount, `apps/connectorruntime` (POST `/connectors/{id}/run`), is typed too
(1 of 1): it had the same two defects (no Bridge, no zipdoc directive) and no
wire test at all — it now mounts its own group Bridge, so it stays
self-contained when mounted without automations, and `http_run_test.go` pins
the 403/404/422 gates and the infra-vs-piece split (an action that ran and
failed is HTTP 200 `ok:false`, never a 5xx).

`apps/guide` (13 of 19) is the worked example of the SPLIT tranche, where a
partition is not all-or-nothing. Six of its routes stay untyped and each names a
wire fact the declaration cannot yet carry: PUT `/curriculum` and PUT
`/blueprint` take a YAML-**or**-JSON document (`sigs.k8s.io/yaml`) that a typed
In would 400; PATCH `/blueprint/{collection}/{id}` takes an opaque JSON
merge-patch whose keys are the item's own AND whose explicit `null` DELETES a
key — which a pointer field cannot tell from absent, so a typed In changes the
merge, not just the schema; POST `/steps/{id}/start|done` answer a
blocked step with a structured 409 (`{error, step, blockedBy}`) that the error
envelope cannot express (#78); and POST `/steps/{id}/do` also STREAMS SSE, where
an op answers exactly one JSON value. The un-gated siblings `skip` and `reset` DO
convert — the 409 branch is unreachable for them — which is the discriminator
worth copying: split on the wire fact, not on the file.

`apps/company` (20 of 22) is the split tranche taken to its FLOOR, and it is the
one to read for what "left untyped" should cost you to claim. Its two refusals
are not "not yet looked at" — each names one missing zip capability, and neither
is closable in cloud:

- POST `/fundraise/deck` takes the deck as the raw request BODY (any content
  type, named by `?name=`). `hasBody("POST")` is unconditional (openapi.go:262)
  and `op.invoke` json.Unmarshals whatever it is handed BEFORE the handler runs
  (typed.go:227), so a typed In would answer a PDF with
  `ErrBadRequest("invalid json body")` — the conversion does not merely
  mis-DOCUMENT the route, it BREAKS it. Waits on a raw-body binding.
- POST `/payment` reads no body at all and its success path is already op-shaped
  (200 + `formationView`); only its DENIAL blocks. `cloud.DenyResource`
  (resource_billing.go:217) renders the fleet-wide
  `{"error":{"code","message"}}` (402 insufficient_balance / spend_cap_exceeded,
  503 balance_unavailable) and zip's `HTTPError` (ctx.go:184) renders a FLAT
  `{"status","code","error"}`. Same gap as guide's structured 409 (#78), and the
  shim does not reach it: `Bridge` carries a STATUS back out, never a body, so
  there is no way to type this without reshaping the error for every metered
  client.

What company DOES still ship is six instances of the bodyless-POST gap (#7) —
`documents`, `esign`, `genesis`, `kyc`, `kyc/refresh` and `skip` each take
`noInput` and therefore publish `requestBody: {required: true}` over an object
with no properties, for a body they never read. That is zip's
`hasBody("POST")` being unconditional, not a mistake in this package, and the
wire is unharmed; it is recorded here because it is the largest single share of
that class in the fleet and it converts for free the day zip can declare a
bodyless POST.

The lesson to copy is the standard of proof — the same one `integrations` arrived
at above. Both refusals were re-verified against zip v1.18.3's own source rather
than taken from the comment that claimed them, because "cannot be typed" is a
claim about a DEPENDENCY, and a dependency moves. Re-check them when zip gains
raw-body binding or multi-status/error bodies; until then the honest floor for
this package is 20, and a fleet-wide count that keeps listing company as 22
untyped is what sends the next agent to redo the work.

`apps/framework` (17 of 19) is the other split, worth reading for the opposite
reason: its two refusals are ONE missing capability rather than two wire facts.
The body of a document write IS the document's own field data — an open object the
DocType defines at run time — and typing those two takes BOTH halves of that
capability, where only the first is ever named. zip must be able to DECLARE an
open object (#8 above), AND `bindURL` must be able to BIND the URL onto one. It
cannot: it returns early unless the In is a struct (`v.Kind() != reflect.Struct`),
so an open-object In carries no `:doctype`/`:name` while a struct In carries no
document. Half the fix converts nothing, which is why the refusal is recorded in a
TEST (`rawRoutes` in `ops_projection_test.go`) with both halves named, not in
prose that only ever named one.

A cited reason nothing reads is a reason that outlives its cause, so the two
observable halves are now PINNED rather than merely cited:
`TestOpenObjectRefusalStillHolds` asserts the false document schema zip publishes
today and that an open-object In receives no `:doctype`, on the same harness where
a struct In provably does. Both assertions are wrong on purpose — either one going
red IS the signal to convert the two writes and delete the test. That is the
difference between a refusal that expires and one that rots.

Re-verified against zip v1.18.6 (the current pin) and again against v1.18.8 (the
newest published tag, two ahead of it): none of the three shipped — `schemaOf`
still has no `reflect.Interface` case, `bindURL` still returns early on a
non-struct, and `mcp.go` still invokes with a nil path map. The first re-check
surfaced a THIRD half the first two hide. Off the REST path `op.invoke` receives no path map at all — `mcpCall` and
the call plane both pass nil — so an op's URL params can reach it only as In
fields decoded from the args body. On THIS wire the body key `name` is live
data: a create body's `name` IS the requested document name
(`stringField(in, "name")`, engine ops.go), so folding `:name` into an
open-object body collides with a field the document owns. zip's open-object
binding must carry URL params OUTSIDE the body namespace, or the MCP/CLI
projections of these two ops ship ambiguous — a constraint the capability spec
has to state, and one more reason a map-In workaround under today's zip would
be worse than the route-only entries these two carry.

`apps/account` (11 of 18) is the catch-all refusal in its purest form: its seven
untyped routes are two routes' worth of shape — GET|POST `/v1/billing/*` and the
five-method `/v1/commerce/*` — each a forwarder whose path is a wildcard remainder
no named In field can bind, whose request body is never JSON-validated (zip's
`invoke` json.Unmarshals first and answers `ErrBadRequest`, typed.go:231), and
whose answer carries the upstream's own status AND body bytes (`c.Bytes(status,
raw)` — a 402 spend cap, a PDF at `invoices/{}/pdf`), where a typed dispatch
answers one declared 2xx in JSON and `WithStatus` panics on anything else.
Opaque by construction, not by omission; what they may reach is bounded by
allowlists instead of types (`billingForwardable` in billing.go,
`commerceStoreHeads` in commerce.go).

Both decisive facts are now a TEST, not a paragraph —
`TestUntypedByDesignForwardsVerbatim` (apps/account/typed_wire_test.go) drives the
live bridge against a stub upstream and asserts the 402 and the byte-identical
`%PDF-` body, so the refusal goes red if a handler stops behaving that way. The
audit that produced it also refuted "forwarded as received, at any content type":
`commerceDo` (topup.go) is a JSON transport, not a transparent proxy, and the
bridges inherit three rewrites from it — the request Content-Type is SET to
application/json whenever there is a body, no response header is returned at all
(so billing.go pins application/json over commerce's own type and drops
Content-Disposition), and the response body is truncated at 1 MiB under the
upstream's own 200. The live consequence: `GET /v1/billing/invoices/{id}/pdf`,
the one non-JSON entry in `billingForwardable`, delivers PDF bytes labelled JSON
with no filename, against a commerce that sets `application/pdf` + `attachment`
(commerce api/billing/invoice_pdf.go). Unfixed on purpose — the repair is
commerceDo returning response headers, three call sites including the top-up
money path, keeping `Cache-Control: no-store` (a tenancy property, not a content
one) — and recorded at the lines that cause it.

`apps/iam` (0 of 25, and why) is the FLOOR of the migration — the one partition
where the honest answer is that none of it converts, and the reason is worth more
than the count. Its published subset is **35** operations and ZERO described — not
the 25 a hand count reaches, and the ten-op gap is worth knowing before you measure
any `All`-mounted package: `app.All` registers NINE fiber methods and
`openapi.From` publishes SEVEN of them, so each wildcard yields
get/post/put/patch/delete **plus OPTIONS and TRACE**. All 35
are methods on five `app.All` wildcards in `safeMount` (apps/iam/iam.go): `/v1/iam`,
`/v1/iam/*`, `/login/oauth`, `/login/oauth/*` and the root `/.well-known/*`, each
handed `zip.AdaptNetHTTP(iamserver.Handler(db))` — and that handler is
`server.NewApp(db)`, github.com/hanzoai/iam's WHOLE standalone zip app (94 typed
ops: OIDC discovery/JWKS, the oauth protocol endpoints, credential login, the v2
entity CRUD, SCIM, the Casdoor verb-alias compat layer) adapted to net/http. Three
independent facts each forbid a typed op, and they are pinned by
`TestTheRelayIsWhyNothingIsTyped` rather than asserted:

1. **One registration serves an OPEN set of sub-paths.** A typed op has one path.
   Enumerating the leaves would mean cloud restating another module's route table,
   and any leaf missed 404s a path IAM serves today.
2. **The bytes, the status and the Content-Type are the nested app's own** —
   including its Guard answering `{"status":401,"error":"authentication required"}`,
   which is not cloud's error shape, and `/login/oauth`'s 302 + `Location` with no
   body. A typed op answers one declared status with one marshalled `Out`.
3. **The oauth token/introspect/revoke endpoints take
   `application/x-www-form-urlencoded`** by RFC 6749/7662/7009, and zip's
   `op.invoke` decodes every non-empty typed body with `jsonenc.Unmarshal` — so
   declaring any `In` turns a working token exchange into a 400. Same wall as
   apps/books' uploads, one content type over.

**The fix is COMPOSITION, not typing.** Unlike `bot`/`licensing`/`sentry`, whose
route tables are in another process, iam's 94 ops are in THIS process, each already
carrying a `WithSummary`, already projected by zip at the nested app's own
`/.well-known/openapi.json` and `/mcp` (94 ops, logged at mount). Cloud's
`openapi.Spec` = `From` ∘ `Live` reads cloud's fiber table, which holds the wildcard
and not what is behind it, so those 94 reach neither `openapi.yaml` nor any SDK nor
the MCP tool list. Closing it means teaching the projection to MERGE a nested app's
own document at the prefix it is mounted on — a second document source, so it is a
deliberate seam decision and not a side effect of a typing pass. Two things to
verify first, both measured: `hanzoai/iam v1.33.26` ships NO `zipdoc_gen.go` and no
`zip.Describe` call anywhere (`grep -rl zip.Describe` over the module: empty), so
those 94 ops have summaries and no descriptions and no field prose — composing
today buys shapes and one-liners, not the full product surface; and the merge has to
respect that cloud's own static `/.well-known/openapi.json` and agentskills'
`/.well-known/agent-skills/*` sit UNDER iam's root wildcard and win only because
zip's matcher takes the most specific pattern regardless of registration order
(zip specificity_test.go — checked, not assumed).

The refusal is a GATE: `apps/iam/typed_wire_test.go` holds `untypedByDesign` keyed
by PATH rather than `METHOD /path` (one `app.All` refuses for every method at once;
keying by method would state one fact six times and let five copies rot) and
`TestEveryRouteIsTypedOrNamed` expands it over the methods the document publishes
and checks the SUM — 0 typed + 35 named = 35 — so a sixth wildcard, a narrowed
wildcard, or a route added here as a raw handler all go red. The sum is DERIVED
from the live document rather than written down, which is what caught the 25-vs-35
miscount in the first place.

**`TRACE` is published, and not only here.** Reading the methods instead of
assuming them turned up a fleet-wide fact: **27 operations across 10 packages
declare `trace`** — `exec` 8, `iam` 5, `tasks` 4, `o11y` 3, `base` 2, and one each
in `ai`, `dns`, `licensing`, `runtime`, `websearch` — every one of them from an
`app.All` catch-all, because `All` means all. So `openapi.yaml`, every generated
SDK and the MCP tool list offer HTTP TRACE on ten products including the identity
plane, a method whose only use is Cross-Site Tracing and which edges normally
refuse outright. Re-find it, do not tally it:

    python3 - <<'EOF'
    import json,glob,os,collections
    c=collections.Counter()
    for f in glob.glob('plugin/*/openapi.json'):
        a=os.path.basename(os.path.dirname(f))
        for p,ops in json.load(open(f))['paths'].items():
            if 'trace' in ops: c[a]+=1
    print(sum(c.values()), c.most_common())
    EOF

Left alone here on purpose: the fix is one decision in `openapi.From`'s method
filter (or in what `All` registers), it moves the published document for ten
packages at once, and regenerating nine other subsets inside an iam change is how
a concurrent agent's work gets clobbered — failure mode 1's own warning.

**The defect typing surfaced.** `mountFailClosed` iterated `Prefixes` alone while
`safeMount` also registered the root `/.well-known/*`, so the degraded surface was
strictly SMALLER than the mounted one. The terminal handler in every plugin binary
is `webui.Mount`'s `/*` console catch-all, and `/.well-known/…` is not in the
console's `apiPrefixes`, so with IAM broken `GET /.well-known/openid-configuration`
— the FIRST call every relying party makes — answered **200 with the SPA's HTML**
instead of the honest 503, and an OIDC client parsed a web page as its discovery
document. Both halves now derive from one `patterns()` list, and
`TestFailClosedCoversEveryMountedAddress` checks the derivation as well as the
statuses. (The bare prefixes were never the hole they looked like: fiber's greedy
`/*` matches the empty remainder, so `/v1/iam/*` already answered `/v1/iam` — 
verified by probe before changing anything. safeMount still registers the bare form
because the DOCUMENT derives its paths from the route table, so without it the
resource's own address appears nowhere in the subset.)

Still open and NOT fixed here, because it is a routing decision at the composition
root rather than a description one: `manifest/router_test.go`'s `unreachable`
ledger already records `iam /.well-known/{wildcard1} -> nothing` — iam's row in
`manifest.Apps` names `/login/oauth` and `/v1/iam` only, so under the light host
OIDC discovery never reaches the iam plugin at all and falls to the console.
Fixing it is one line in `Apps` and one deletion from the ledger; it wants the
router-oracle run and an owner who is changing routing on purpose.

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

- **The `zipdoc_gen.go` files are COMMITTED, one per typed package**, 1:1 with
  the `//go:generate` directives (27 at this writing — count them, do not trust
  the number). They are tracked, not gitignored, and the reason is live: the
  root build targets (`build`, `host`, `ship`, `plugin`, `monolith`) do not
  regenerate them, so a binary built from a fresh checkout by any of those paths
  would otherwise ship with no descriptions at all. Only `make openapi`, the
  per-app `mk/plugin.mk build` chain, and the Dockerfile run the pass.
  **Untrack them only after every build path regenerates them** — not before.
  The drift is already gated: `make test` runs `zipdoc -check` (writes nothing,
  red on a stale file) PER PACKAGE — never `-check ./...`, because module-load
  and package-load extract differently and a gate must not disagree with the
  generator it polices. Do not add a second one.
- **The wire freeze test must be updated in the same diff as `Wire()`.** It is a
  golden, not an invariant. A new subsystem lands RED until `frozen` names it.
  That is the design — the failure is the review prompt — but do not "fix" it by
  loosening the test.
- **Committed per-app subsets (`plugin/<app>/openapi.json`) go stale when routes
  change.** The weave gate catches it: `TestFleetIsTheWeaveOfItsApps` compares the
  composition against `openapi.yaml`, so an app whose subset no longer matches its
  routes fails there — and a MISSING subset fails immediately, naming the file.
  The fix is to re-emit: `make -C apps/<app> describe` for one,
  `make -f mk/fleet.mk describe-apps` for all of them. Never edit the JSON, and
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

- **AN AGGREGATOR CALLS; IT DOES NOT IMPORT.** Apps are separate binaries, so a Go
  import can no longer reach another app's data — `treasury.ReserveCents()`
  compiles inside `apps/admin` and returns the zero value, because treasury never
  mounts there. Every such read was silently blank AND cost the caller the whole
  dependency graph to be blank. The ONE replacement is the internal plane
  (`plane.go` + `plane/`): a TYPED OP over ZAP on the callee's unix socket, no
  HTTP, no network fallback, no second scheme.

      bal, err := cloud.Ask[plane.BalanceIn, plane.Balance](
          cloud.As(c, org), "commerce", plane.FinanceBalance, &in)

  `As(c, org)` delegates THIS request's principal, optionally re-pointed at another
  tenant — the operator acting on someone else's books. `For(ctx, org)` states the
  tenant when there is no request to delegate from, the background-loop case. A
  missing socket is an ERROR naming the app — "that app is not running here" — never
  a zero value, which is exactly the lie the imports told. Serving side:
  `zip.Post[In,Out](cloud.Plane(), path, fn, zip.WithOperationID(plane.X))` from the
  app's Mount, `cloud.Who(ctx)` for the principal, and an ordinary `zip` error for a
  refusal the caller sees with its status intact (402 vs 403 vs 404 vs 503).
- **One socket path, named once, and it is zip's.** `zip.SocketPath(app)` →
  `{ZIP_RUNTIME_DIR}/<app>.sock`, the SAME function `ServePlane` binds and `Ask`
  resolves, so a server and its callers cannot disagree about where an app lives.
  Cloud points `ZIP_RUNTIME_DIR` at `{CLOUD_RUN_DIR | CLOUD_DATA_DIR/run |
  /run/hanzo}` once, at `bindRuntimeDir`. Directory 0700, socket 0600: the
  filesystem is the reachable surface and the kernel enforces who may connect,
  which is why a forwarded principal is sound here and would not be over a network.
- **The org rides the CALLER, never the argument.** Every org-scoped op takes its
  tenant from `cloud.Who(ctx).Org` and refuses an empty one. Do not add an org field
  to an input — a caller that can name its own org can name another tenant's books.
  Prefer making it UNREPRESENTABLE over validating it away: `plane.BalanceIn` carries
  `(subject, currency)` and has no org field at all, so there is no check to forget.
  `plane_test.go` proves this adversarially against the real transport AND
  structurally (`TestNoPlaneInputCanNameAnOrg` walks every input type by reflection),
  and the property is live — an org-less call answers `403: … no org on the call`.
- **Wire contracts live in `plane/`, imported by BOTH ends.** Typed In/Out plus the
  op names; the encoding is zip's, not ours. Putting the request/reply structs in
  the OWNING app instead is what made `apps/billing` import `apps/commerce` to name
  two structs — 1246 packages for a DTO. One definition, neither end importing the
  other. Measured: `plugin/admin` 2261 → 1115 packages, `apps/billing` 1246 → 945,
  and two boards that had been reporting zeros started reporting the truth.
- **THE FLEET HAS ONE MCP DOOR: `POST /v1/mcp`, on the HOST.** zip serves it
  (`zip.MCPConfig{Path:"/v1/mcp"}` in `cmd/cloud`), and its tool list is the union
  of every mounted plugin's BUILD-TIME catalogue — `plugin/<app>/mcp.json`, written
  by the same `<app> describe` run that writes that app's `openapi.json`, embedded
  by the leaf `plugin/embed.go` and handed to zip as `Plugin.Tools`. So `tools/list`
  is a memcpy of a constant and starts NO child; only a `tools/call` wakes one — the
  single plugin that owns the name — over ZAP on its private socket, where the
  child's OWN registry answers. There were THREE hand-rolled registries for this one
  concept (`apps/tools/http.go`, `apps/tools/builtin.go`, `apps/automations/mcp.go`)
  and the public one exposed none of the typed ops; all three are deleted. Type an
  op and it IS a tool — do not write a second JSON-RPC envelope, and note
  `manifest/mcp_test.go` turns one red (no served path may end in `/mcp`).
- **The per-tenant tool plane is ONE typed op, and callable in-process.** An org's
  connectors, functions, agents, authored skills and external MCP servers are ROWS,
  not code, so no build-time catalogue can hold them: they are reached through
  `POST /v1/tools/call` (a typed op, therefore itself a tool on the door) over the
  registry's one policy — precedence → activation → price → dispatch. Discovery is
  `GET /v1/tools`. `apps/automations` contributes to it through
  `connectorToolProvider`, whose `dispatchTool` is the ONE core (resolve
  `<connector>_<action>` → run with a Token bound to the VALIDATED org); the
  exported `automations.InvokeTool(ctx, org, tool, args)` is the same core for a
  sibling subsystem that must ACT AS a caller (the Business AI guide's "do it for
  me") — same 403 gate, per-org concurrency bound, one metered unit, one audit
  record. Use these seams; never re-implement tool dispatch.
- **"Bot" is three values; each has one home and one namespace.** Do not merge
  them and do not let them share a route prefix — they did once, and the router
  resolves byte-identical patterns by first-registration with no panic (it MERGES
  the handlers, so counting `GetRoutes()` entries cannot see it), and visor's
  machine list silently answered the console's run list.
  (1) A bot RUN — a task the runtime executes on a surface — is `apps/bots` at
  `/v1/bots`. (2) A bot MACHINE — visor-provisioned compute of kind=bot plus its
  agent binding — is `apps/visor` at `/v1/compute/bots`; what it rents you is
  compute, so it nests in visor's domain. (3) The runtime SERVICE — the TS bot
  (channels/skills), never reimplemented in Go — is reached through
  `apps/runtime`, which is a TRANSPORT, not a domain: base address, identity,
  framing, cleartext policy, and the `/v1/bot/*` ops face. It is named for what it
  does, not for the host it dials, and it must never import `bots`/`coding` — each
  of those owns its own wire stub (`bots/wire.go`, `coding/task.go`) and speaks
  through the seam. That isolation is what makes the HIP-0106/HIP-0120 ZAP swap a
  seam swap instead of a rewrite.
- **Cloud owns policy; the runtime owns the run. Do not copy state you do not
  own.** `apps/bots` holds NO store. The sandbox lives in the bot runtime,
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
  reaper (`apps/platform/orphans.go`) — fails SAFE (IAM unreachable ⇒ reap
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
  `apps/platform` calls IAM's idempotent bootstrap upsert to create
  `<org>-platform-kms` (clientId==name==audience; a surprise clientId is
  refused, never sealed; the upsert must carry `cert-<brand>` or the minted app
  cannot SIGN and its tokens 500), seals both fields, and the sync proceeds.
  In-cluster IAM base resolution is `cloud.IAMBaseURL` — the split-horizon
  policy stated once (Cloudflare 403s server-side POSTs to the public issuer).
- **A customer IS an IAM user; marketing keeps no contact list.** Who to email is
  read IN-PROCESS from the embedded IAM (`apps/marketing/roster.go` →
  `iam/pkg/store.GetMailableUsers` over `apps/iam.DB()`), the same seam
  `apps/platform` uses for the IAM-owned Project — no HTTP hop to `/v1/iam`
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
- **The Business AI Guide (`apps/guide`, `/v1/guide/*`)** is the on-site launch
  checklist: a pure engine (`curriculum.go` — parse/validate/next-step/dependency
  gating over plain data) + per-org progress (`cloud.OrgStore[*Store]`) + an
  injectable auto-detect registry (`detect.go` — `acted` reads the agent action
  ledger, `analytics` probes the shared warehouse) + the agent (`agent.go` — drafts
  with `deps.AI`, executes the step's bound tool via `automations.InvokeTool`). The
  curriculum is a machine-readable contract (embedded `default.yaml`; org-custom via
  PUT replaces it) so `hanzoai/marketing` can author the full `checklist.yaml`
  against the same `Step`/`Curriculum` shape.
- **The EXPERIMENT is a composition, not a fourth engine (`apps/experiments`,
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
  `math.Erfc`, no dep) is a PURE function over them. `apps/campaign` runs a
  creative A/B by composing `experiments.Assign`/`experiments.Analyze` — it never
  reinvents assignment or evidence. Add a new variant KIND by putting a payload on
  the variant; the primitive does not care what it is.
- **The OSS-template compute cost is DERIVED from the compose, not a fourth ledger
  (`apps/blueprint`, `/v1/blueprint`).** A blueprint's `docker-compose.yml` is
  parsed to its SBOM (the bill of container images) and its services' CPU/memory
  footprint priced through ONE documented rate card (microdollars per vCPU-/GB-hour,
  DigitalOcean-droplet-derived + platform margin; tunable via
  `CLOUD_BLUEPRINT_UCPU_HR`/`_UGB_HR`). Sizing is the declared
  `deploy.resources.reservations`/`limits` (or legacy `cpus`/`mem_*`) else a default
  footprint per inferred class (db/cache/web/worker/other). `blueprint.EstimateTemplate(id)`
  returns `{sbom, vcpuHr, gbHr, microUsdPerHour, estCentsPerMonth}`: `estCentsPerMonth`
  is the "~$X/mo to run" the console shows; `microUsdPerHour` is the exact rate the
  deploy path meters the deploying org on via the SAME commerce spine `resource_billing`
  uses. The author royalty (`apps/authors`, `defaultShareBps=2000`) already accrues
  20% of a deploying org's metered spend — this plane only DEFINES the compute component
  of that spend from a real rate card; it never touches the ledger or the accrual sweep.
  Distinct from `apps/sbom` (CycloneDX packages INSIDE one image, keyed by digest);
  this is the bill of IMAGES a stack runs, keyed by template.

## Identity vocabulary is IAM-native

Identity is expressed ONLY in IAM-native nouns: **org, user, project, billing
account**. The word **"tenant" is banned** in cloud identifiers, strings,
comments, and filenames. Resolve org/project scope through `apps/principal`
(`principal.Org(c)`, `principal.Project(c)`) and user identity through `c.User()`
— all gateway-minted, JWT-validated values (X-Org-Id / X-Project-Id / X-User-Id,
HIP-0026); never read a raw request header for scope.

- **The one gated exception.** `apps/platform` derives customer-app Kubernetes
  namespaces, registry image refs, and quota/limit objects from a live `tenant-<org>`
  string prefix. Renaming that prefix orphans deployed namespaces + built images,
  so the literal `"tenant-"` string (and its directly-adjacent comment) is retained
  behind a `// NAMING(gated)` note in `apps/platform/k8s.go`. The surrounding
  identity vocabulary is org-native regardless; only the on-cluster string waits on
  an infrastructure migration.

## API keys are ONE noun (`/v1/keys`), and the type is a FIELD

`POST` creates, `DELETE` revokes, `GET` lists. `apps/account/account.go`.
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

## Hanzo Company (`apps/company`, `/v1/company`)

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
a KMS-signed Hanzo-L1 anchor mirroring `apps/treasury` (honest pending when
unwired); KYC + state filing → honest stubs (no fabricated verification/filing).
Import path (already-incorporated orgs): Google Drive → data room, a Google Sheet →
captable, via the `google` OAuth provider now completed in `apps/integrations`
(token custodied in KMS; the automations `google` connector shares the same token).
Runbook: `docs/company-dogfood.md`.

## Deploy plane (`apps/deploy`, `/v1/deploy`)

Native ArgoCD-grade GitOps console over the operator-managed fleet, parallel to
`/v1/git`: each `hanzo.ai/v1` App CR IS the Application, and the plane OBSERVES the
operator's reconcile — `GET /v1/deploy/applications` (fleet list), `/{name}/tree`
(ownerRef resource tree + per-node health/sync), `/{name}/resource/{ref}` (live
manifest + desired-vs-live diff), `/{name}/logs`; `POST /{name}/rollback` pins the CR
image to a prior semver and `/{name}/sync` requests a reconcile. SUPERADMIN-only on
`c.IsAdmin()`, fail-closed; Secret nodes are never surfaced. `engine.go` embeds the argo
`gitops-engine` (`hanzoai/deploy/gitops-engine` v0.7.2, no replace) in-process for the
reconcile half behind `DEPLOY_ENGINE_ENABLED` (default off), with a prune-safety fuse.

## The index (`apps/index`, `/v1/index`)

The in-binary index, speaking the Meilisearch REST dialect so a Meilisearch client
repoints by changing one host. It replaced the standalone Meilisearch containers
(`chat-meilisearch`, `search-fts5`).

**Four different things, four names — do not merge them.** `hanzoai/search` is the
SEARCH PRODUCT (our own Meilisearch build, serving `search.hanzo.ai` and the docs
corpus). `apps/websearch` queries the OUTSIDE world. `apps/crawl` fetches it
(in-binary — see below; the standalone `hanzoai/crawl` service it used to call is gone).
`apps/index` is the storage primitive an application writes documents into and
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

## The cross-org catalog (`apps/catalog`, `/v1/catalog`)

Everything the fleet has built — hanzo, lux and zoo repos, plus every site this
deployment serves — as ONE searchable corpus. It owns no store: the rows live in
`apps/index` under the uid `catalog`, so relevance, paging and encryption at rest
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

**Visibility, not authorship, decides who appears** (`apps/projects/visibility.go`).
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

## Starter kits (`apps/templates`, `/v1/templates`)

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
(`apps/projects`' fork resolves the caller org's own templates first, then the
gallery), so "which templates may this org use" is answered in exactly one place;
a fork of a private template records owner-qualified lineage (`acme/acme-portal`),
a fork of a gallery template records the bare public slug.

## Fetching the web (`apps/crawl`, `/v1/crawl`)

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

## Releases are cut by a merge to main — and a release is a TRAIN

`.github/workflows` is intentionally empty of CI. **ONE file, `.hanzo/workflows/cicd.yml`,
is the whole pipeline**, and it is one `needs:` graph:

```
gate ──┐
       ├─→ image ─→ rollout ─→ reach ─→ fanout ─→ receipt
containment ─┘
```

| car | what it does | what it refuses |
|---|---|---|
| **gate** | hanzoai/ci reusable → `hanzo.yml` `test:` → `make -f mk/fleet.mk surface-check` | a route added, renamed or deleted without regenerating the document |
| **containment** | apps/controlplane is unreachable from every real binary | stub crypto in a serve binary |
| **image** | version derived ONCE → build → push → resolve → smoke | a tag naming an image that did not boot |
| **rollout** | tag → universe pin → **poll `x-api-version` until it is ours** | describing a version that is not running |
| **reach** | `openapi/reach.py` over every literal address + the MCP tool count | an address this document publishes that production does not route |
| **fanout** | `repository_dispatch: spec-update` → 9 repos, payload `(version, sha, spec_sha256)` | a projection that never heard about this release |
| **receipt** | `release.json` on the tag's GitHub Release, `if: always()` | a hole, silently |

**Why this shape.** `cicd.yml` used to gate and `deploy.yml` used to release, and
they were two files with the SAME TRIGGER. Actions cannot express `needs:` across
workflow files, so deploy built, smoked, tagged and pinned while the gate was
still running — or after it had gone red. That is measured, not hypothetical: the
drift gate was RED on main while 87 commits and 6 releases shipped in 24 hours,
and what went out was one binary serving `/v1/billing/gpu/eligibility` and
publishing `/v1/billing/gpu-eligibility`. `deploy.yml` is deleted; its jobs are
here, behind `needs:`.

**The coupler is the document, passed BY VALUE at a pinned sha.** Every car
carries `(version, sha, sha256(openapi.yaml))`. No car reads api.hanzo.ai to
GENERATE anything — at generation time the deploy has already happened, so
reading the host names whatever it is serving rather than the release that sent
it. The host is read for exactly one purpose: to prove the release is live.

**It does not roll back. It BLOCKS and RESUMES.** You cannot unpublish a package
version, so the cars are ordered by irreversibility: in-repo → registry-but-
unnamed (the tag is minted only after smoke) → production → the outside world.
Every car is idempotent on `(version, digest)`. Re-running at the same sha reuses
the version (a `v*` tag already pointing at HEAD *is* this release), treats a 422
whose ref names our sha as success, and re-probes instead of re-pushing. `concurrency:
cancel-in-progress: false` on main queues releases instead of orphaning them —
ten numbers between `.335` and `.350` are images published under a version that
was never tagged, smoked or pinned.

**`openapi/unreachable.txt` is a ratchet, not an allowlist.** Every line is a
published address production does not route, with its owner named in the header.
The file may only shrink: a 404 not listed fails the release, and a listed line
that starts answering must be deleted in the same commit. Today it holds 14
`/v1/pricing/*` lines, all owned by a Cloudflare worker that exists in no repo.

**The projections are gated, not hoped for.** MCP needs no car — the door serves
exactly the tools in the committed `plugin/*/mcp.json`, so car 3 checks the
number rather than claiming it (833 = 833 at v1.801.350). The eight clients and
the docs each run hanzoai/ci's `client:` lane, which fetches `openapi.yaml` at
the release's sha, **refuses on a digest mismatch**, regenerates, compiles itself
and its examples, writes `.spec-lock` and cuts a patch.

**Credential still to create: `FLEET_DISPATCH_TOKEN`** — a fine-grained PAT with
`contents:write` + `metadata:read` on `hanzoai/{python-sdk,js-sdk,go-sdk,rust-sdk,java-sdk,kotlin-sdk,cpp-sdk,cli,docs}`,
stored in KMS at `orgs/hanzo/secrets/deploy/FLEET_DISPATCH_TOKEN` (env `prod`),
beside `UNIVERSE_PIN_TOKEN`. Until it exists the fanout car fails loudly and
names it; that is deliberate — a release that quietly skips its projections is
the failure this train was built to end.

The image and its `v*` tags used to have a different owner, `apps/platform/release.go`:
compute the next version → build → SMOKE the pushed image → tag → roll out. The
tag is still a RECEIPT for a proven image; what moved is where the receipt is
minted.

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

`apps/projects` owns the full versioned-release model for static sites, and it is
the ONE way:

- `<org>/.releases/<slug>/rel_<128-bit manifest digest>/` — immutable, content-
  addressed, and a SIBLING of the mutable `<org>/<slug>/` prefix, so neither a
  full-artifact deploy nor a project delete (both of which purge that subtree) can
  shred a release the pointer still names.
- `Store.ActivateRelease` — the flip is one atomic `UPDATE … WHERE EXISTS (release
  row)`, so it cannot point a site at a release that was never created, and two
  concurrent activations cannot leave the pointer disagreeing with whichever won.
  `MarkLive` deliberately does NOT touch `current_release`.
- `servePrefix` (`apps/projects/sites.go`) — the ONE read rule, re-validating the
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
(`apps/git/smart_http.go`), the GitHub App (`/v1/connector/github/webhook`), and
the canonical forge (`/v1/git/webhook`, `apps/git/webhook.go`). The third exists
because git.hanzo.ai is a SEPARATE process: its pushes never touch our receive-pack,
so without that door the host we call canonical builds nothing and only the mirror
releases. Both webhook transports HMAC-verify fail-closed and drop bot-authored
pushes through the one `cloud.IsBotActor`, so a release cannot retrigger itself.
`apps/platform` is the only place that decides what a push MEANS: an app tracking
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

## The `hanzo` name is TWO binaries — the Rust CLI, and cmd/hanzo's control half

The `hanzo` fabric CLI is the RUST binary at `~/work/hanzo/cli`. Its control-plane
verbs speak the routes THIS process serves, authorized off a plain `hanzo auth login`
(the IAM access token is the final bearer fallback — no `--platform-token`).

This module also builds `cmd/hanzo` (restored in 59c6dbd5 after the 22f4fc64
deletion): a CLIENT-ONLY control binary that links `cli` and nothing else. It runs
the verbs `cli.newRootCmd` registers and hands EVERY other verb to the Rust CLI,
resolved as `hanzo-node` (or `HANZO_FABRIC_CLI`), so the one `hanzo` name is a
superset of both. It cannot mount a subsystem — serving is `cmd/cloud`'s job.

`cli.IsControlVerb` draws that line, and it must keep drawing it off the cobra tree
(`newRootCmd().Find`, cobra's own name+alias resolution). It used to be a
hand-maintained map of verbs — a SECOND source of truth for a fact the tree already
holds — and it drifted both ways and broke users. `code` and `k8s` stayed on the map
after their commands were deleted, so `hanzo code`, which the Rust CLI implements,
died with `unknown command "code" for "hanzo"` and never delegated. `completion`,
`help`, `version` and the `clusters` alias `cluster` were registered but missing from
the map, so cobra's own commands were handed to a binary that has never heard of
them. Do not reintroduce a list: adding a command to `newRootCmd` IS the whole
registration, and `TestRouterMatchesCommandTree` fails the moment the two disagree.

Four names come from cobra rather than from our `AddCommand`. `help` and `completion`
are added in `newRootCmd` (`InitDefaultHelpCmd` / `InitDefaultCompletionCmd`, both
idempotent) because otherwise Execute adds them too late for the router to see them.
`__complete` / `__completeNoDesc` are registered inside ExecuteC with no public hook,
so IsControlVerb claims them by cobra's own exported constants — they are what the
emitted completion scripts invoke, so delegating them kills TAB completion even when
`hanzo completion bash` prints a perfect script.

The CLI does not import this module and never will: its cloud surface is GENERATED
from a spec. `genspec` joins the authored master (`hanzoai/openapi` `hanzo.yaml`)
with a live route table into `spec/cloud.json`, and `genproduct` emits
`src/commands/product/generated.rs` from that. The registry can only REFUTE an
authored operation, never add one — so a route this module serves reaches no command
until `hanzoai/openapi` authors it. When a verb is missing from the CLI, author the
route there; do not hand-write the command.

- `hanzo platform fleet list|get` → `GET /v1/platform/fleet[/{app}]` (`apps/platform`
  fleet.go drift board). `--env`/`--health`/`--drift` filter it.
- `hanzo platform fleet deploy <app>` → `POST /v1/platform/fleet/{app}/deploy` — a
  zero-downtime ROLLING RESTART (stamps the Deployment pod-template
  `hanzo.ai/restartedAt` annotation; never changes the declared TAG — that stays a
  git commit CD reconciles). `--env` picks the ns.
- `hanzo cluster list|show` → `apps/visor`, tenant-scoped.

`cli/` (Go, package `cli`, ~10.4k lines) is NOT built and NOT importable by anything
here — zero importers, no `main`, no Makefile target. It is retained ONLY as the
reference for the client-side tools not yet ported to Rust: the GPU worker daemon
(`gpu.go`/`studio.go` — hardware enumeration, the claim/heartbeat loop, ComfyUI
supervision, systemd install), `agent publish`'s local half, and `engine install`.
Rust's `node join` is a one-shot registration, not that daemon. Do not add to `cli/`,
do not wire it into a build, and do not delete it until those are ported — deleting
it destroys the only spec for work that is owed.

`POST /v1/runner` (native buildkit fabric) has no CLI verb today: it is served here
but unauthored in `hanzoai/openapi`, and the bare name `runner` is already taken by
the Rust CLI's CI-runner daemon. Authoring it needs a name decision first. Called
directly, with `image:` it builds a container image; with NO `image:` it reads the
repo's own `hanzo.yml` (`binaries:` + `bucket:`) and builds the ARTIFACT lane
instead — see below.

### `/v1/runner` builds ANY project, not only a Dockerfile

`/v1/runner` has two lanes, and a request is in exactly one of them:

- **image** (`image:`) → `launchDirectBuild` → rootless BuildKit → a pushed OCI ref.
- **artifact** (`binaries:`) → `launchArtifactBuild` (`apps/platform/artifact.go`) → a
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

`/v1/platform/fleet` auth mirrors `/v1/runner` (`apps/platform/runner.go`): the `guard` admits a
validated principal who is SuperAdmin OR OrgAdmin, then each handler CONFINES a non-super
caller to the platform namespaces its own validated org owns (`scopedNamespaces`, keyed on
`principal.Org` — a tenant admin can never observe/restart another org's, or a platform,
app; `?org=` cannot widen it). The rolling restart needs `patch` on `apps/deployments`
(ClusterRole/cloud, universe `infra/k8s/cloud/rbac.yaml`). There is NO `/v1/apps` or
`/v1/org/{org}/cluster` CLI path — both are the TS-Dokploy contract, never served here
(404 live). `/v1/platform/*` IS served (projects + apps + sites + fleet) and no longer
500s on a missing co-resident IAM store — it answers the ordinary gate
(`403 {"error":"X-Org-Id required"}` unauthenticated).

`/v1/paas` no longer exists. It was a SECOND NAME for platform — the same product
answering to two prefixes, which is a duplicate definition however you route it — so it
folded into `/v1/platform/fleet`. The board is a SIBLING of `/v1/platform/projects/:p/apps`,
not a copy: `fleet` is the platform's OWN service tier (the shared services it runs on),
`projects/:p/apps` is a customer's apps. Two collections, two names, one prefix. The fleet
board reads k8s directly with no IAM-store dependency, so it stays up when the store is
not co-resident.

## GTM: `/v1/campaign` orchestration → channels → connectors → analytics

The go-to-market stack decomplects a campaign from its execution. A **Campaign is a
VALUE** (`apps/campaign`: `{name, audience, content[], schedule, budget, channels[],
status}`) that SPANS channels; a **Channel is an EXECUTOR** (`channel.go`, the
`Channel` interface) it fans out to. The three channels are orthogonal and each
CONSUMES the connector plane via `integrations.TokenFor` — the campaign object never
touches a credential:

- **paid → `/v1/ads`** — `ads.LaunchPaid/PaidSpend/PausePaid` (`apps/ads/provider.go`)
  resolve the org's ad token (`meta_ads`/`google_ads`/… via `TokenFor(org, <id>,
  "access_token")`) and run the campaign on the provider. Meta is executed for real;
  fail-closed when the org has not connected (424). This is the ONLY place `/v1/ads`
  touches the connector plane.
- **organic → `/v1/publish`** (rename of `apps/social`) and **email → `/v1/marketing`**
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
(`apps/analytics/campaign.go`) — an org+`utm_campaign`(+`utm_content`)-scoped query
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
ref is not filtered. `apps/integrations/github_webhook.go` accepts any ref under
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
so a tag can never rebuild an app that tracks a branch (`apps/platform/push.go`),
and `refs/tags/main` is not `refs/heads/main` for the release check.

**Tenancy comes only from the installation id** in the HMAC-verified body — never a
header, never a client-controlled field. That is the whole tenant resolution:
`OrgForExternalID("github", installation.ID)`.

### Exactly one GitHub App — two is not redundancy

`Store.Get(ctx, org, provider)` keys a connection on `(org, "github")`: **one row per
org**, holding one installation id. The App's own identity is a single set of process
values, `GITHUB_APP_ID` + `GITHUB_APP_PRIVATE_KEY` + `GITHUB_APP_WEBHOOK_SECRET`
(KMS-synced, `apps/integrations/github.go`), used to mint short-lived installation
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
being confused: `/v1/git/repos/:name/*` (`apps/git/subscriptions.go`) holds a
repo→Slack-channel subscription and a repo→downstream mirror target. Those are
reactor config, org-scoped like every repo route. The code index is also per-repo and
indexes the default branch only — a feature-branch push is skipped so it cannot
clobber the canonical index. None of these decide whether a ref syncs.

## The money plane runs locally, and `make e2e` proves the prepaid cycle

`commerce` is co-resident (`apps/commerce.go` → `commercemod.Embed` on cloud's own zip
app), so the billing surface, the ledger, and the gate are all one process. Two facts
about running it that are easy to get wrong in opposite directions:

**There is no /v1/billing or /v1/commerce FORWARDER any more, and forwarding is why.**
`account-bridge` was a 112th app whose whole manifest row was the two BARE stems
`/v1/billing` and `/v1/commerce`. Behind them sat `GET|POST /v1/billing/*` and a
five-method `/v1/commerce/*`, each re-dialing commerce over HTTP with the admin
`COMMERCE_SERVICE_TOKEN`. That token satisfies commerce's
`MayMintMoney = IsServiceToken || IsSuperAdmin`, so **forwarding WAS authorization**:
every path that reached commerce through it executed with PLATFORM authority, not the
caller's, and the only thing between a signed-in org member and `POST /v1/billing/deposit`
was a hand-maintained per-method allowlist that had to stay ahead of every mint route
commerce would ever add. A default-refuse table is the right shape for that job and still
the wrong job to have.

It is gone, and nothing replaced it, because the fix had already been applied seventeen
times: every endpoint on that allowlist is served NATIVELY — six by `apps/billing`, eleven
by the co-resident commerce embed — each at a manifest prefix named DEEPER than the bare
stem. The fiber fork sorts endpoint routes most-specific-first
(`zap-proto/fiber router_precedence.go`, ServeMux semantics), so every one of those routes
already won its address regardless of mount order and the wildcard behind them received
nothing. The `manifest/router_test.go` oracle is what proves it rather than asserts it: it
routes every published path through the real `zip.Load` and names the app that receives it.

Two consequences worth stating plainly. **The bare stems are now UNCLAIMED** — no row was
widened to inherit them, because a leaf nobody names is surface nobody serves, and a 404
from the `/v1` remainder is a louder failure than a catch-all that answers "sign in to view
billing" to a pricing page (which is exactly what the stems did to the whole self-service
paid path before commerce's row named each leaf). And **the ten merchant store heads**
(`product`, `order`, `user`, …) the `/v1/commerce/*` half allowed had no native handler at
all — but they had no working route either: the forwarder re-dialed `COMMERCE_URL`, which
is unset everywhere and defaults to the public edge, i.e. THIS binary, where `/v1/product`
matches nothing deeper than ai's `/v1`. Deleting it removed a hop, not a surface. The
split-deploy case belongs to `apps/commerce/transport`, whose RoundTripper dispatches
in-process when commerce is co-resident and falls back to plain HTTP when it is not —
under the native handler's own subject-pinning, with no admin token in the browser path.

`COMMERCE_SERVICE_TOKEN` is NOT dead: `apps/account/topup.go` still forwards it on the one
outbound S2S call (the HUSD wallet credit), and `IsServiceToken` compares against it to
recognise a trusted in-process caller. It is no longer attached to anything a browser can
address.

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
the failure `apps/commerce/transport` documents: balance reads that answer
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

## Encryption at rest: cek is the gate, and it binds the owner

`cek.Open(principal, path)` is the ONE encryption-at-rest gate. The principal comes
first because it is a question the caller must answer, not one it can forget:
`cek.Global` for a platform store, `cek.Org(slug)` for a tenant's. `cek.User(id)` exists
for the per-user partition, which has no store yet.

If you add a store, open it through cek. A store inside the envelope has a `.dek`
sidecar beside it; one outside does not, and this is the check worth running on any data
dir — note it looks for the SIDECAR, because on a pure-Go build the codec envelope keys
the file out of band and the database may not exist at that path at all:

    find $DATA_DIR -name '*.db' -printf '%P\n' | while read -r r; do
      printf '%-40s dek=%s\n' "$r" "$([ -f "$DATA_DIR/$r.dek" ] && echo yes || echo NO)"; done

**The derivation.** The KEK binds (owner, file) and never the path, so a store survives a
move but not a change of owner:

    tenant:   KEK = HKDF(master, lp("org") || lp(slug || "/" || hex(fileID)))
    platform: KEK = HKDF(master, lp("global") || lp(hex(fileID)))

The platform form is byte-identical to what every store on disk was written under, which
`TestGlobalDerivationIsUnchanged` asserts against an independently written reference — so
the platform fleet cannot be silently orphaned. Only tenant stores gained an owner.

**Migration is an operation, not a fallback.** `cek.Rebind(from, to, path)` rewraps one
sidecar; `cek.RebindOrgs(dataDir, platformSlug)` is the walk over `{DataDir}/orgs`. It
rewrites no database page and never opens the file, so it is safe on a store too large to
copy and a failure cannot corrupt data. Already-bound reports `ErrNotBound` and counts as
skipped, so the walk converges rather than pretending to be a transaction; a sidecar that
unwraps under NEITHER principal is a real error, because an operator must not read
corruption as success.

There is deliberately no legacy path inside `Open`. A second derivation tried on failure
would mean every open silently accepts two answers forever — which is exactly what made
the old binding unenforceable.

⚠️ **Deploy order.** A volume written before the binding must be rebound before its
tenants can open their stores. Run `RebindOrgs` against the data dir, then start.

**IAM's store is `iam/global.db`** and opens through cek like everything else. It is named
for its principal partition, not for a version — it previously opened through
`iamserver.OpenSQLite`, which has no key to give it, and sat in plaintext.

**Still outside the envelope:** `tasks/<org>/<namespace>.db`. `hanzoai/tasks`'s
`EmbedConfig` has no key field, so that is an upstream change.

## Inter-app calls: typed ops, ZAP over UDS

**One app calls another with a typed op — the same op the REST route, the OpenAPI
document, the MCP tool list and the CLI are all projected from.** `plane.go` is the
whole mechanism:

    cloud.Plane()                       the app internal ops are declared on
    cloud.ServePlane(name, log)         binds zip.SocketPath(name)
    cloud.Ask[In,Out](ctx, app, op, in) is the caller
    cloud.For(ctx, org)                 states the tenant for a BACKGROUND call
    cloud.As(c, org)                    delegates THIS request's principal
    cloud.Who(ctx)                      reads the principal a handler acts for

`zip.SocketPath(name)` resolves `{ZIP_RUNTIME_DIR}/<name>.sock` and BOTH halves use
it, so a server and its callers cannot disagree about where an app lives. Cloud points
`ZIP_RUNTIME_DIR` at its own data root (`bindRuntimeDir`) rather than keeping a second
rule about where sockets go.

**The plane is a SECOND app, and that is the point.** A typed op rides every transport
its app listens on, and the host proxies edge traffic to its children over a private
socket — so neither "is this HTTP?" nor "did this arrive on a socket?" separates an
internal call from a public request. The separation is structural instead: internal ops
are declared on the plane app, which listens on exactly one address and is never mounted
on the edge router. There is no path from the internet to a plane op, the same way there
is no path to a route that was never registered. It keeps every projection — the plane
has its own OpenAPI, MCP and CLI — without publishing the gate and the secret reads into
the public document.

**Identity rides the caller, and zip carries all nine headers.** Org, project, user,
name, email, owner, isAdmin, isOrgAdmin, request-id (zip v1.18.4 — it forwarded five,
and the four it dropped were the ones a callee decides on: a billing subject prefers the
minted name over the opaque id, and platform sudo is read off owner, never off
isOrgAdmin). A background job with no request to forward states its tenant once with
`cloud.For`; an inbound request always wins over what it stated, so a job can supply an
identity and can never launder one.

**A socket FILE is not a peer, and only the router may say an app is absent.** `Ask`
wakes a lazy app before it calls it (`reach`), and `reach` proves a LISTENER — it
connects, it does not stat. A run directory on a volume outlives the pod that wrote it,
so a leftover `<app>.sock` from a dead pod passes a stat forever; read as "the peer is
up", it suppresses the wake that would have put a listener behind it, and the app never
starts. The listener unlinks a stale path when it binds, so `reach` removes nothing —
the run dir keeps one writer.

The answer a caller gets is therefore two facts, not one: **`ErrNoPeer` means this
deployment does not run that app** — the router said so from the manifest it owns, or
there is no router at all — **and it is the ONLY error that may be read as "fall back".
Every other failure is an outage.** Absence inferred from a failed call is a fail-open:
a payment rail concluding nothing is priced, or (v1.801.340, three days) `apps/billing`
concluding the fleet ran no commerce, handing the prepaid balance read to an HTTP proxy
that is unset in exactly that deployment, while ai's fail-CLOSED gate answered 503
`balance_unavailable` for every paid completion. Nothing logged the reason, because after
the line that dropped the error nothing had it. Pinned by `TestStaleSocketIsNotAPeer`,
`TestWakeStartsALazyAppBehindALeftoverSocket` and
`TestBalance_APlaneOutageIsNotASplitDeploy`.

The socket is also the coarse boundary: it is 0600 and `SO_PEERCRED`-authenticated, so a
peer is already one of our own processes. It is NOT a boundary between them — never read
a caller's org as an authorization decision on its own.

**What this replaced.** `rpc.go` + `dial.go` + `payloads.go` were a second, hand-written
implementation of exactly this: an fnv-hashed method registry, hand-packed ZAP payloads
with literal byte offsets, and a capability the callee parsed and nothing verified. It
was invisible to all five projections — `kms.get`, `finance.authorize` and `iam.mailable`
had no OpenAPI entry, no MCP tool, no CLI verb and no SDK. 1,300 lines, deleted.

**The body is ZAP, end to end** (zip v1.18.6, `internal/zapenc`). The layout is derived
from the In/Out type: fields take slots in declaration order, each aligned to its own
width, and NO NAME TRAVELS — a field is its offset. So the bytes on the socket are the
bytes in memory, exactly as the hand-written codecs did it, with the schema now held by
the type instead of by matching offsets in two files. Refusals cross as ZAP too, status
intact. JSON is the BOUNDARY encoding and stays on the REST routes a browser reaches and
in the MCP envelope an agent reads; it never appears between our own processes.
`TestPlaneWireCarriesNoFieldNames` pins it: the values cross, the field names do not.

Because the layout IS the type, the compatibility rule is structural: **append fields at
the end, and only at the end.** Reordering, inserting or retyping changes the wire for
every peer.

Still TCP, deliberately: the tasks GATED listener (`durable.go`), because consumers in
other pods dial it and a unix socket does not leave the host. The engine's own loopback
listener IS a socket (tasks v1.52.4 `EmbedConfig.Address`), which is what removed the
free-port allocation that seven of eight children used to lose.

## After the split: a store has one owner, and everyone else asks

`cmd/cloud` is a light plugin HOST. Each app is its own binary (`plugin/<app>/main.go`
→ `cloud.Serve`), started lazily on the first request to its prefix, and a child's
`--enable` contains ONLY its own name. So `cfg.Enabled("commerce")` is false in every
process but commerce, `finance.Current()` is nil in every process but commerce, and
`iamclient.DB()` is nil in every process but iam.

`make build` builds the HOST ALONE. Use `make apps` (112 parallel targets; 53s cold,
4.3s warm) or `make ship`. With only the host present every app route answers 503,
which looks exactly like a broken product and is not one.

**THE LAW.** A store has one owner. Any other process ASKS it — it never opens it, and
it never reports "not configured" for something that is one socket away.

See "Inter-app calls" above for the mechanism. The rule is the same whatever the
transport: one owner, everyone else asks.

**Five subsystems broke this way**, each silently, each fixed by publishing a method
from the owner: the prepaid gate (which ALLOWED — every priced act became free), the
balance read (501 on funded accounts), the identity roster (campaigns mailed nobody),
secrets (a stored provider read back as "not configured"), and the welcome grant (an
org opened broke, and the paywall then refused it correctly for a reason nobody chose).
credits/usage/ledger are three projections of ONE entry list and went together.

Three rules fell out of doing it, and they are worth reusing:

- **Peer ABSENT ⇒ fall back, or stay inert. Peer ANSWERED badly ⇒ error.** A missing
  peer is the legitimate split-deploy (or no-money-plane) shape, and erroring there
  502s a deployment that is working as designed. A corrupt reply rendered as zero is a
  funded account shown as broke.
- **The billed or read ORG rides the CALLER, never the argument.** A caller that could
  name the org in a body could bill or read another tenant. `TestNoPlaneInputCanNameAnOrg`
  pins it structurally: no input type on the plane may carry an Org or Owner field.
- **Send DATA, not a rendered view.** usage and txns carry ROWS; the HTTP surface
  renders its own envelope. Sending the envelope would require the renderer to live
  with the ledger, which is an import cycle (billing already imports commerce) — the
  compiler tells you.

**Wire contracts live in `plane/`** — one leaf package imported by both ends, holding
every op's In/Out and the op names, so the halves cannot drift and neither drags the
other's dependency graph. It imports nothing of cloud's. Money crosses as `plane.Money`
(exact decimal text + currency code), never as an int.

**Money is not an int.** `hanzoai/money` is the general exact value; `apps/money` is
USD at 18 decimals so an off-chain amount and an on-chain uint256 are the same integer.
The ledger speaks the latter, so the wire carries `AttoString()` ↔ `ParseInt()` — exact
by construction. Flatten to cents only at a boundary that is already cents-shaped.
Alignment is NOT finished: ~46 `money.Amount` against ~308 `int64` cents.

**One deployment, one key.** The credz broker IS kms (`launch.Broker`), so the host
starts it first and eagerly; a child launched with a token WAITS for it and, failing
that, holds no key rather than inventing a second one. A fallback here is how a fleet
ends up running on two keys with nothing saying so.

**credz is NOT the plane with extra steps, and must not be collapsed into it.** Both
speak over a 0600 unix socket, but they prove different things and only one of them
proves identity:

    plane    answer() calls parseIdent(call.Cap) — it PARSES the capability.
             Nothing verifies it. Any co-located app can call
             Dial("commerce").For("another-tenant") and be believed.
    credz    peerPID (SO_PEERCRED, same uid) AND a launch token that opens only
             under the secret the launcher minted — so the app name is the one
             the LAUNCHER stamped, never one the caller chose.

That difference is load-bearing: it is how each child gets ITS scoped bundle and not
a sibling's. So the socket is the boundary for "one of our own processes", and it is
NOT a boundary between our own processes — which is fine for a bug-free fleet and is
worth knowing before treating a plane capability as an authorization decision. Tenancy
is enforced where a request principal is resolved, at the edge, from a validated
token; a method that re-checks the capability's org against a ref (as kms does)
catches an app asking for one tenant while acting for another, which is a BUG worth
failing on rather than an attack being repelled.

Making the plane verify would need a verifier every app holds, and only the broker
holds the launch secret today. Do not bolt on a weaker check and call it one.
