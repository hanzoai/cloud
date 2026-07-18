# LLM.md — hanzoai/cloud

Guidance for AI agents working in this repo. `hanzoai/cloud` (HIP-0106) is ONE Go
binary + CLI that mounts every Hanzo subsystem into a single process; the same
artifact serves `api.hanzo.ai`, `api.lux.cloud`, `api.zoo.cloud`, `api.osage.cloud`
and every white-label reseller. Brand, enabled subsystems, and org scope are
deployment configuration.

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
