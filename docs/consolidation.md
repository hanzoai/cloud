# Go-service consolidation into the unified cloud binary (HIP-0106)

Directive: **ALL the Hanzo Go services merge into the one `cloud` binary.** This
is the execution queue. Each remaining standalone Go service has exactly one
disposition. Waves are **sequential through `cloud/go.mod`** — one wave lands,
`go mod tidy` against the pushed tag, build/test/proof, then the next — because
two waves editing `go.mod` + a shared in-process engine at once collide (see the
tasks wave below, which had to reconcile with an in-flight `durable.go`).

## Dispositions

- **merged** — already in this binary (embedded module or native `clients/*`
  subsystem). Nothing to do.
- **mount** — should become a `clients/*` subsystem via import-and-mount. Est.
  diff noted.
- **standalone** — stays its own deployment, with the reason. These are edge/
  data-plane/chain/identity tiers whose blast-radius isolation or non-`zip`
  substrate makes in-process fusion wrong, not merely large.

The contract for a `mount`: a package exposing `Mount(app *zip.App, deps
cloud.Deps) error` that registers `/v1/<name>/*` and returns; registered in
`subsystems/subsystems.go` with an order < 150 (before ai's `/v1/*` catch-all).
Heavy/legacy HTTP frameworks (Beego, Gin) can be adapted with
`zip.AdaptNetHTTP`, but dragging their ORM/auth/DB substrate into this binary is
a rewrite, not a mount — see visor.

## Wave 0 — already merged

Embedded modules (blank-imported, `Mount` via `init()`): **ai, authz, base,
commerce, licensing, metrics, o11y, vfs** — plus the in-process **KMS** secrets
plane (`clients/kmssvc`) and the ONE in-process **tasks durable engine**
(`durable.go`, shared — see Wave 1).

37 native `clients/*` subsystems already carry their product surface in-process:
admin, agents, analytics, bot, cms, console, crm, do, erp, eval, exec, framework,
functions, git, graph, help, kms, kmssvc, ml, o11y, paassvc, plan, platform,
plugin, pricing, product, projectsvc, prompt(s), provisioning, s3, security,
tasksvc, templates, visor, websearch, zt.

## Wave 1 — THIS build (tasks + visor)

| Service | Disposition | Notes |
|---|---|---|
| **tasks** (`hanzoai/tasks`, tasksd) | **merged** | Engine already embedded in-process by `durable.go` (ONE engine, loopback ZAP :19999, shared with ai ingest). This wave adds the **HTTP + UI surface** (`clients/tasksvc`, order 147) mounting that SAME engine's handlers at `/v1/tasks/*` + `/_/tasks/*` — the "consolidate the UI surface into cloud" follow-up named in `durable.go`. No second Embed. Needed a small `hanzoai/tasks` v1.46.0 (`auth.WithIdentity` in-proc identity seam + single `hanzoai/sqlite` driver). ~180 LOC + `cloud.EmbeddedTasks()` accessor. |
| **visor** (`hanzoai/visor`) | **surface merged; logic port = own wave** | The compute REST surface (`/v1/machines`, `/v1/gpus`, `/v1/clusters`) is ALREADY native in `clients/visor` (order 133), org-scoped via `principal`. It PROXIES the standalone visor for data. Visor itself is a **5.4k-LOC multi-cloud Beego + xorm + Casdoor monolith** with its own DB and auth — mounting that in-process drags Beego/xorm/Casdoor + a second DB + bare `/v1/*` route collisions into this binary (architecturally wrong, not golf). The genuine native port = replace visor's `object/*` xorm persistence with a Base store and reuse its pure-`godo` `service/{digitalocean,doks}.go` on top of cloud's existing `clients/do` — a bounded **~600–900 LOC wave of its own**, sequenced after wave 2. Visor stays standalone until then. |

## Wave 2+ — mount queue (sequential; do one, tidy, prove, next)

| Service | Disp. | Est. diff | Notes |
|---|---|---|---|
| **notify2** (`hanzoai/notify2`) | mount | S | Notifications (email/SMS/push) → `/v1/notify/*`; thin over `gomail`/`go-sms-sender`. Good next wave (small, self-contained, no exotic substrate). |
| **collab** (`hanzoai/collab`) | mount? | M | Real-time collab (`cmd/collab`). Mountable if it factors to a handler; a stateful WebSocket/CRDT tier may argue for standalone — assess the listener seam first. |
| **extract-svc** (`hanzoai/extract-svc`) | mount | S–M | Document/text extraction → `/v1/extract/*`; complements ai ingest. Likely an in-proc call, no HTTP hop. |
| **idv** (`hanzoai/idv`) | mount? | M | Identity verification (`cmd/idv`). Mount as `/v1/idv/*` IF it has no HSM/PII-isolation requirement; if it does, standalone (PCI/PII blast radius). |
| **playground** (`hanzoai/playground`) | mount | S | Dev/eval playground surface → `/v1/playground/*`; overlaps `clients/eval` — reconcile, don't duplicate. |
| **insights-go** (`hanzoai/insights-go`) | fold | S | CLI (`cmd/cli`), not a service — fold into `clients/analytics` or keep as a lib. |
| **git** (`hanzoai/git`) | merged? | — | Native S3-backed git already lives at `clients/git`. Confirm the standalone repo is superseded (large-repo hosting is the only reason to keep it) and deprecate if so. |

## Keep standalone (with reason)

| Service | Reason |
|---|---|
| **iam** (`hanzoai/iam`) | Identity control plane; isolated blast radius by design (subsystems.go: "iam → iam.hanzo.ai, isolated control plane"). The binary CONSUMES it (JWT validation), never hosts it. |
| **gateway** (`hanzoai/gateway/v2`), **ingress** | The edge — they route *to* this binary. Traefik/krakend substrate; fusing the edge into the app defeats the tier separation. |
| **kms** (`luxfi/kms`, EMBEDDED) | KMS is now embedded in this binary (`clients/kms` store + `kmssvc` `/v1/kms`, fail-closed) — the standalone KMS Deployment is collapsed into cloud. Only the MPC signing ring (`luxfi/mpc`) stays a separate node set, the root of trust. |
| **registry** (`hanzoai/registry`) | OCI distribution data plane (S3-backed fleet registry); a Docker registry protocol server, not a `/v1` control surface. |
| **s3** (`hanzoai/s3`, SeaweedFS) | Object-storage data plane. Cloud already carries the control/browse facade (`clients/s3` + provisioning); the store is a separate fleet. |
| **docdb** (`hanzoai/docdb`, FerretDB) | MongoDB-wire DB data plane over Postgres; provisioned via `clients/provisioning`, never hosted in-proc. |
| **node**, **bootnode**, **nchain** | Chain/P2P daemons (luxfi substrate) — not app surfaces. |
| **functions** (fission fork) | FaaS control plane / k8s operator; cloud carries the `clients/functions` facade, the executor fleet stays out. |
| **hsm**, **mpc**, **ldapserver** | Specialized crypto/identity infra — isolation is the point. |
| **o11y storage** (o11y-foundry, otel-collector, signoz) | Telemetry storage/query backend; cloud mounts only the `o11y` facade + reverse proxy. |

## Method (for each mount wave)

1. Find the library seam (the exported server constructor `cmd/<svc>d` wraps).
   If it demands its own listener, add a minimal exported constructor upstream
   (patch-bump, `v1.x`, push a tag — no `/v2`).
2. Add `clients/<svc>/` with `Mount(app, deps)` + `init()` `cloud.Register`.
   Reuse `principal.Tenant` for org scoping; `zip.AdaptNetHTTP` for stdlib
   handlers.
3. Bump the dep to the PUSHED tag, `go mod tidy` — **no local `replace`**.
4. `CGO_ENABLED=0 go build ./...` (production parity: the binary is CGO-off; a
   CGO-on local build double-registers the `sqlite` driver — modernc vs mattn —
   which is a pre-existing property of the graph, not a wave regression).
5. Smoke test (mount + route responds + tenant isolation) and a live-binary
   curl transcript before merging.
