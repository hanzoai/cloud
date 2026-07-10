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
- **Composition root.** `subsystems/subsystems.go` blank-imports every subsystem
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
