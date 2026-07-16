# LLM.md — hanzoai/cloud

Guidance for AI agents working in this repo. `hanzoai/cloud` (HIP-0106) is ONE Go
binary + CLI that mounts every Hanzo subsystem into a single process; the same
artifact serves `api.hanzo.ai`, `api.lux.cloud`, `api.zoo.cloud`, `api.osage.cloud`
and every white-label reseller. Brand, enabled subsystems, and org scope are
deployment configuration.

## Framework doctrine

One way to do everything. Composable, orthogonal, DRY. A new subsystem is a
package under `clients/<name>` that obeys these seams — nothing more.

- **Subsystem shape.** A subsystem exposes `func Mount(app *zip.App, deps cloud.Deps) error`
  and self-registers at init with `cloud.Register("<name>", <order>, cloud.Typed(Mount))`
  (or `RegisterWithShutdown`). `Mount` wires that subsystem's `/v1/<name>/*` routes
  onto the shared `*zip.App`; `cloud.Deps` carries the process-wide handles
  (Logger, DataDir, the subsystem `Client` seams). No subsystem reaches into
  another's internals.
- **Client seams.** Cross-subsystem calls go through a narrow in-process interface
  published in `types` and aliased at the provider, e.g. `commerce.Client =
  types.CommerceClient` (`GetOrgConfig` + `CheckEntitlement`). Consumers depend on
  the interface, never the implementation; the seam rides zap-proto/zip. Keep each
  interface minimal — add a method only when a consumer needs it.
- **Composition root.** `apps/apps.go` blank-imports every subsystem
  (its init runs `cloud.Register`), populating `cloud.Registry`. `MountAll`
  (build.go) sorts the registry by `Order` and calls `Mount` on each ENABLED
  subsystem (`cfg.Enabled`). That ordered blank-import set IS the wiring — there
  is no separate `Wire()` function; to add a subsystem you add one import line.
- **Route precedence is a framework guarantee.** The router is zap-proto/fiber
  (zip v1.3.0). Most-specific route wins regardless of mount order; a genuine
  route CONFLICT panics at mount rather than resolving ambiguously. Subsystems may
  therefore mount in any order and still compose deterministically.
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
  resolves byte-identical patterns by first-registration with no panic, so
  visor's machine list silently answered the console's run list.
  (1) A bot RUN — a task the runtime executes on a surface — is `clients/bots` at
  `/v1/bots`. (2) A bot MACHINE — visor-provisioned compute of kind=bot plus its
  agent binding — is `clients/visor` at `/v1/compute/bots`; what it rents you is
  compute, so it nests in visor's domain. (3) The runtime SERVICE — the TS bot
  (channels/skills), never reimplemented in Go — is `clients/bot`: the `/v1/bot/*`
  passthrough for its own ops paths, plus the clients cloud calls it through
  (`RunCodingTask`, `StopRun`).
- **A bot run is a session; the runtime is an executor, never an authority.**
  `clients/bots` owns auth, tenancy, billing and the contract; it holds NO store.
  A run is recorded on the agents session plane (`agents.OpenSession`, agent
  label `bot`) so its id IS the run id and one registry serves every kind of
  agent work. Two seams (`Runs`, `Runtime`) are injected in `adapters.go` — the
  only file there that imports `agents`/`bot` — so the handlers unit-test against
  fakes. List and stop authorize against the org-scoped RECORD first and drive the
  runtime only after; a run of another tenant is not found, so a compromised
  runtime cannot widen a caller's reach.
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
