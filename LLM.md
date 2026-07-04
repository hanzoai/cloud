# hanzoai/cloud — LLM.md

The unified Hanzo Cloud binary (HIP-0106): one Go process mounts every `/v1/*`
subsystem from the in-binary registry (`subsystems/subsystems.go`), backed by
embedded SQLite under `CLOUD_DATA_DIR` (`/var/lib/cloud`) via the ONE Hanzo
driver `github.com/hanzoai/sqlite` (modernc/nocgo in prod — no CGO).

## SQLite zero-downtime HA — on-stack, one way (NO LiteFS / Litestream / Postgres)

### Problem (verified)
`cloud` is a single stateful Deployment: `replicas: 1`, `strategy: Recreate`, one
RWO PVC (`cloud-api-data`) mounted at `/var/lib/cloud`. Every subsystem opens one
GLOBAL file — `tracker.db`, `agents.db`, `projects.db`, `provisioning.db`, `crm.db`,
`prompts.db`, `functions.db`, `automations.db`, `evals.db`, `git.db`, `security.db`,
`framework.db`, `platform.db`, `integrations.db`, `catalog.db`, `audit.db` (+ the
Badger `kms/` KV, + casibase `/data/cloud/cloud.db`) — tenancy is an `org` column,
`SetMaxOpenConns(1)`, WAL. Because RWO + Recreate serialize pod handoff (old pod
must release the volume before the new pod attaches), EVERY deploy is a few-second
502 on the cloud-served `/v1` subset (`/v1/tracker`, `/v1/exec`, console-keys, …).
`replicas: 2` is impossible: RWO is single-mounter and two SQLite writers corrupt.

### Decision
Two-pod primary/standby on the Hanzo-native stack. Mechanism, all first-party:
- **`github.com/hanzoai/sqlite`** — the driver (already in use, unchanged).
- **`github.com/hanzoai/replicate`** — WAL→LTX streaming to an object store
  (SeaweedFS S3, live in-cluster as `s3`/`s3-filer`/`s3-master`). Library API:
  `NewDB(path)` + `NewReplicaWithClient(db, s3.NewReplicaClient(...))`; `db.Sync`
  (DB→shadow-WAL) then `db.Replica.Sync` (→backend LTX); standby catch-up via
  `Replica.Restore(RestoreOptions{OutputPath, Follow, IntegrityCheck})`.
- **`replicate/s3.Leaser`** — single-primary election by object-store conditional
  write (the `Leaser` interface: `AcquireLease`/`RenewLease`/`ReleaseLease`). Only
  the lease holder writes. (`file.Leaser` = the dev/test analog, filesystem CAS.)
- **StatefulSet** with `volumeClaimTemplates` (per-pod PVC → no shared-RWO deadlock)
  + `RollingUpdate` + a headless Service for stable pod DNS + a **primary-only
  Service** (selector pins the lease-holder) so writes route to the writer.
- **Orderly handoff** on deploy: new pod joins as standby → `Restore(Follow)` until
  caught up → readiness passes → old primary's **preStop** quiesces writes,
  final `Sync`, releases the lease → new pod acquires the lease, final `Restore`,
  promotes to writer. Reads are served by the standby throughout ⇒ zero read gap.

`internal/org.Replicator` (whole-DB snapshot Push/Pull over `hanzoai/vfs`, HIP-0302
per-org) is the coarser per-tenant variant of the same idea; it is currently
DORMANT (no callers) and is NOT the path for the global subsystem DBs — those use
`replicate`'s per-DB WAL streaming.

### Tradeoff (honest)
`replicate` is streaming/restore, not FUSE page-replication. A SUDDEN CRASH fails
over with a bounded lag (the last LTX not yet shipped). But the blip we are fixing
is a PLANNED deploy: we control the drain, so the handoff does a final Sync +
caught-up Restore before promotion ⇒ zero data lag and zero read downtime on a
roll. This is why streaming (not LiteFS FUSE) is the correct, on-stack choice here.

### Proven (prototype: `hack/sqlite-ha-proto`)
Standalone harness on the REAL `hanzoai/sqlite` + `hanzoai/replicate`: two "pods"
sharing only the object store; a write on the primary appears on the standby; an
orderly primary handoff mid-run. Result: 60 contiguous rows (30 pre-roll on A + 30
post-roll on B), `PRAGMA integrity_check = ok`, **0 data loss at promotion**, and
**123 tight-loop reads across the handoff with 0 failures**. Run:
`cd hack/sqlite-ha-proto && GOWORK=off CGO_ENABLED=0 go run .`

### What must be built (scoped)
Operator (`~/work/hanzo/operator`, Rust `Service` kind — `build_statefulset` +
`build_headless_service` already exist and are proven by the `Datastore` kind):
1. Service→StatefulSet switch + thread `volumeClaimTemplates` (today `Service`
   always renders a Deployment with one PVC).
2. A configurable container `preStop` (today hard-coded `sleep 5`) for the
   checkpoint→final-Sync→lease-release drain hook.
3. A primary-only Service (single-pod / `role: primary` selector — no builder adds
   a single-pod selector today) for write routing.
   (The `replicate` sidecar + `replicate-restore` initContainer are ALREADY injected
   by the operator when `spec.persistence` is set — cloud's CR must adopt
   `persistence` instead of raw `volumes`.)
App (`cloud`): register each global `{DataDir}/*.db` with a `replicate.DB` (WAL
shipper); acquire/renew the `s3.Leaser` lease and stamp `role: primary` on the pod
when held; gate readiness on "caught up"; route mutating `/v1` handlers to the
primary Service (standby forwards writes); preStop = checkpoint + final Sync +
release. Migration: import the existing `/var/lib/cloud/*.db` into the object store
(first `Sync` seeds an LTX generation), verify `integrity_check` + row counts
(Dave's `tracker`/`agents`/`projects` rows) before AND after cutover.

### Staged rollout (forward-only, semver — NEVER sha; operator CR + universe main; never `kubectl set image`)
1. Prove the primitive (DONE — `hack/sqlite-ha-proto`).
2. Land operator changes (StatefulSet+VCT, configurable preStop, primary Service).
3. Wire cloud app (replicate registration, lease→role, write routing, preStop);
   fix cloud go.sum dep-rot so it builds in CI (build CI, not local docker).
4. Scratch-namespace 2-pod deploy; tight-loop `GET /v1/tracker/projects` across a
   real roll → all 200 (measured blip target: 0 / sub-second).
5. Windowed prod cutover: migrate `/var/lib/cloud` DBs into SeaweedFS, verify
   Dave's data before/after, bump `cloud` CR to the new semver on universe main.
