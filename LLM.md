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
- Build in MODULE mode only: `make build` / `GOWORK=off go build ./...` — never
  workspace mode (see "Build & module graph" below).

## Key entry points
- `cmd/cloud` — server binary · `cmd/hanzo` (`cli/`) — control CLI · `webui.go` — embedded console
- `apps/apps.go:Wire()` — composition root (the one ordered subsystem slice)
- `deps.go` / `cloud.Deps` — process-wide handles · `clients/<name>/` — every subsystem
- `openapi/` — live spec derived from the router (no checked-in spec file)

---

## Open Cloud planes

Spec home: HIP-0129 `hip-0129-open-cloud-planes` (hips repo). This section is a
map, not the spec. One noun, one owner, one route family. No plane reads another
plane's store; imports flow custody-ward only (channels -> integrations, never
reverse).

| Route | Noun | Owner | Tier |
| --- | --- | --- | --- |
| `/v1/connectors` | Custody: per-user BYO external accounts | `clients/integrations` (extends; user scope new) | In flight (branch `feat/connectors`) |
| `/v1/channels` | Transport: portable message envelope, DM pairing, send + inbox | `clients/channels` (new) | Planned (branch `feat/channels` reserved; no transport code yet) |
| `/v1/sync` | Data: bidirectional sync engine | `clients/sync` | Shipped |
| `/v1/automations` | Workflows: flows/runs, goja piece runtime | `clients/automations` | Shipped |
| `/v1/compute/bots` | Hosting: `@hanzo/bot` Node containers | `clients/bots` | Shipped |
| `/v1/tasks` | Durable engine | `clients/tasks` | Shipped |
| `/v1/gpus` + fleet | BYO GPU presence | `clients/fleet` + `clients/visor` | Shipped |
| `/v1/cloud` | Cloud accounts: link DO/AWS/GCP/Azure, discover native k8s clusters, fold into the fleet | `clients/venue` (new) | In flight (branch `feat/cloud-account-connectors`; blue-held for red) |
| `/v1/blueprint` | Cost: OSS-template SBOM (compose→images) + compute-cost estimate | `clients/blueprint` (new) | Shipped |
| IAM | Identity: users, orgs, roles | IAM | Shipped |
| KMS | Secret custody: sealed secrets | `clients/kms` | Shipped |

Custody invariants: secrets sealed in KMS at
`/orgs/{org}/users/{user}/connectors/{provider}/{label}`, never in SQLite rows;
verify before store. Refresh is single-flight with rotation resealing; the CLI
does local browser PKCE and posts the bundle to
`POST /v1/connectors/:provider/credential`; cloud owns device-code flows.

Transport invariants: typed actions (`command|url|select|approval`), no raw
string sniffing; pairing codes 8 chars, 1h TTL, max 3 pending per account,
owner bootstrap on first approval.

Container boundary is permanent for native-module, host-filesystem, loop-state,
and vendor-Node work (agent loop, exec/PTY, harnesses, browser, voice, codecs,
Node-bound channels, plugin SDK/loader). The Node plugin SDK is never ported to
Go; cloud extensibility is connectors/automations/tools.

Port roadmap (P1-P15) lives in HIP-0129; do not restate it here. Every claim
carries its tier: Shipped (on main, named package/route), In flight (named
pre-main branch), Planned (backlog id or named reservation).

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

## Framework doctrine

One way to do everything. Composable, orthogonal, DRY. A new subsystem is a
package under `clients/<name>` that obeys these seams — nothing more.

- **Subsystem shape.** A subsystem exposes `func Mount(app *zip.App, deps cloud.Deps) error`
  and is listed in `apps.Wire()` as a `cloud.MountSpec{Name, Mount: cloud.Typed(Mount)}`
  (plus `Shutdown`/`OwnsHealth` where it owns them). `Mount` wires that subsystem's
  `/v1/<name>/*` routes onto the shared `*zip.App`; `cloud.Deps` carries the
  process-wide handles (Logger, DataDir, the subsystem `Client` seams). No
  subsystem reaches into another's internals. There is no init()-registry and no
  `cloud.Register` — subsystems do NOT self-register.
- **Out-of-process variant.** A subsystem may run as its OWN binary without
  changing anything about it: `cloud.PluginSpec(name, zip.Plugin{…}, prefixes…)`
  returns an ordinary `MountSpec`, so where a subsystem runs stops being a
  property of its source and becomes one line at the composition root. zip starts
  `cmd/<name>` as a child on a private unix socket and forwards the path
  UNCHANGED. Today `o11y` is the only one — it is the heaviest graph in the tree
  (otel-collector, prometheus, gonum) and imported by nothing else, so unlinking
  it is pure subtraction.
  - `prefixes` is variadic because ONE plugin commonly owns several subtrees
    (`o11y` answers `/v1/o11y` AND `/v1/sentry`). Naming only the first 404s the
    rest AT THE HOST — the request never reaches the child — while the host
    starts and reports healthy. Both readers take the same list: zip routes on
    it, and `indexSubsystems` reports it to `/v1/admin/subsystems` and to the
    per-request subsystem attribution tracing hangs off.
  - The image must actually CONTAIN the binary: `zip.Load` fork/execs a sibling
    of `/cloud`, so a missing one aborts the mount and cloud never listens
    (`fork/exec /o11y: no such file`). The Dockerfile DERIVES the list by grepping
    `PluginSpec("…"` out of `apps/apps.go` rather than keeping a second copy —
    unlinking o11y without adding a build step once cost five consecutive releases.
- **Client seams.** Cross-subsystem calls go through a narrow in-process interface
  published in `types` and aliased at the provider, e.g. `commerce.Client =
  types.CommerceClient` (`GetOrgConfig` + `CheckEntitlement`). Consumers depend on
  the interface, never the implementation; the seam rides zap-proto/zip. Keep each
  interface minimal — add a method only when a consumer needs it.
- **Composition root.** `apps/apps.go:Wire()` returns `[]cloud.MountSpec` — every
  linked subsystem, in mount order, as ONE explicit slice read top-to-bottom.
  Slice position IS the order: there is no `Order` field and `MountAll`
  (build.go) does NOT sort; it iterates as-given and mounts each ENABLED spec
  (`cfg.Enabled`). To add a subsystem you add one line to `Wire()`.
  `apps/wire_test.go` freezes the sequence, so a reorder/drop/add fails there.
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

- **The spec IS the router.** `openapi.Live(app)` reads
  `app.Fiber().GetRoutes(true)` — fiber's own filter drops `Use()` middleware —
  and every other function in `openapi/` is a pure function of that `[]Route`.
  There is NO checked-in spec file to hand-maintain and no second registry. The
  drift guard is `cmd/cloud/openapi_test.go`: a BIJECTION over the fully-mounted
  `apps.Wire()` (983 operations / 692 paths / 109 products) — every live route
  appears as an operation, every operation is backed by a live route. It is the
  only test whose failure means the document lies.
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
  is the SERVICE (service.go:90), not the payload; `cloud.Typed` is an
  `any→*zip.App` mount adapter. The ONE path to schemas is zip's typed ops
  (`zip.Get[In,Out]`), which carry the In/Out types and also yield an MCP tool
  from the same registry (zip/openapi.go, zip/mcp.go — today `len(a.ops) == 0`,
  so zip's own generator emits nothing here). `GetRoutes()` is a superset of
  `app.ops`, so migrating a handler to a typed op adds schema without changing
  this pipeline.
- **Catch-alls are opaque, by construction.** `app.Post("/v1/billing/*")` proxies
  to another service, so `POST /v1/billing/deposit` is NOT a route in this process
  and cannot appear. Measured on the live table: 3 products are wholly opaque
  (`bot`, `licensing`, `sentry` — the catch-all IS the product) and 12 more mix
  concrete ops with a catch-all hiding an unknown remainder.

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

`index.Reconcile` is `index.Query`'s mirror and the only in-process WRITE seam: a
full-corpus swap (upsert everything, delete what is gone) because the truth lives
upstream and a re-run must converge.

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
Its one genuinely new finding is recorded as a gap below, not as a second system.

**Known gap: releases are never garbage-collected.** `promote` writes a new immutable
prefix per distinct content and nothing prunes them; `DeleteReleases` only runs on
project delete. Retention belongs in `clients/projects` next to `promote`. When it is
added, `activate` must also verify the bytes still exist before flipping — today
`ActivateRelease` proves only that the ROW exists, which is sufficient only while
nothing can prune the bytes out from under it.

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
- `hanzo build`          → `POST /v1/runner`  (native buildkit fabric)

`/v1/paas/*` auth mirrors `/v1/runner` (`clients/platform/runner.go`): the `guard` admits a
validated principal who is SuperAdmin OR OrgAdmin, then each handler CONFINES a non-super
caller to the platform namespaces its own validated org owns (`scopedNamespaces`, keyed on
`principal.Org` — a tenant admin can never observe/restart another org's, or a platform,
app; `?org=` cannot widen it). The rolling restart needs `patch` on `apps/deployments`
(ClusterRole/cloud, universe `infra/k8s/cloud/rbac.yaml`). There is NO `/v1/apps`,
`/v1/org/{org}/cluster`, or `/v1/platform/projects` CLI path — the first two never existed
here (TS-Dokploy contract, 404), and `/v1/platform/*` needs a co-resident IAM store this
deployment does not fold in (IAM runs as a separate svc) so it 500s; the live apps backend
is `/v1/paas`, whose board reads k8s directly with no IAM-store dependency.

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
