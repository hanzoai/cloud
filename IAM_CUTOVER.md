# IAM cutover — Casdoor pod → embedded IAM in cloud (supervised)

Flip `hanzo.id`'s identity plane from the standalone Casdoor pod to the clean-room
IAM rewrite embedded in this binary (`clients/iam`, `github.com/hanzoai/iam`
**v1.33.6**). This is the last step of HIP-0106 (one binary embeds IAM + KMS + o11y).

**This is a single supervised session. Do not run it piecemeal or in the
background.** Every step below is: **action → verify → rollback**. Money/auth is on
the line — a bad flip 401s every login and every metered request.

The blocking code work is **done and shipped**:
- IAM v1.33.6 serves the legacy path aliases the deployed fleet hard-codes
  (`/v1/iam/oauth/access_token`, `/v1/iam/oauth/refresh_token`, `/v1/iam/userinfo`)
  next to the canonical paths — so no hard-coded caller 404s at the flip.
- `cloud` pins `github.com/hanzoai/iam v1.33.6` (go.mod) and compiles it in; the
  subsystem is **staged OFF** (`stagedSubsystems={"iam","ingress"}` in `config.go`).
  Until enabled, `iam_edge.go` forwards `/v1/iam/*` to the standalone Casdoor pod.

## Preconditions (verify before touching anything)

1. Live image is at or above the tag that carries embedded IAM v1.33.6
   (`universe/infra/k8s/operator/crs/cloud.yaml` `spec.image.tag`). The subsystem is
   compiled in but inert until enabled — safe to deploy ahead of the flip.
2. `spec.replicas: 1` and `strategy: Recreate` (already required — the embedded
   store is single-writer/single-open). **`config.go` refuses to boot iam-enabled
   above 1 replica.** Never scale up with iam on.
3. `CLOUD_DATA_DIR=/var/lib/cloud` on the RWO `cloud-api-data` PVC. The embedded IAM
   store is **`/var/lib/cloud/iam/iam.db`** (`clients/iam/iam.go` `paths()`).
4. You have the live Casdoor store to migrate FROM and its KMS master key:
   - encrypted sharded root: `<dir>/iam.db` + `<dir>/orgs/*/iam.db` (+ `.dek` sidecars)
   - `IAM_KMS_MASTER_KEY` = the 64-hex master key (from KMS — never an arg, never logged)
5. `migrate-v1` is built from the **iam** repo (`github.com/hanzoai/iam`,
   `cmd/migrate-v1`, same v1.33.6 tag) with a C `sqlcipher` binary on PATH (for
   `--wal-inclusive`).

## Step 1 — Migrate the live store into the embedded datadir (BEFORE any seed)

The embedded IAM seeds new-only from `init_data.json`. Migrating real rows must
happen **before** the subsystem ever seeds, or the seed masks/collides with them.
The source is opened **read-only** — the live Casdoor pod is untouched.

**1a. Dry-run = the drift/parity gate.** `--dry-run` runs the full extraction and
prints the per-entity report WITHOUT writing. Require every entity's count to match
the live source and **drift = 0** before proceeding.

```
migrate-v1 \
  --src-datadir /path/to/live/casdoor/store \
  --src-master-key-env IAM_KMS_MASTER_KEY \
  --wal-inclusive \
  --dest /var/lib/cloud/iam/iam.db \
  --dry-run
```

- `--wal-inclusive` checkpoints each shard's uncheckpointed `-wal` via the C
  sqlcipher binary → COMPLETE extraction. Without it, uncheckpointed WAL rows are a
  hard error (or, with `--ignore-wal`, silently dropped — do NOT use for a real cutover).
- `--dest` is the **verbatim `.db` path** `…/iam/iam.db`, NOT a data-dir. A data-dir
  dest writes `<dest>/iam2.db`, which cloud does **not** read (it opens `iam/iam.db`).

**Verify:** dry-run report shows expected counts for users, orgs, applications,
providers, certs; zero drift; zero errors.
**Rollback:** none needed — nothing written.

**1b. Real migration.** Same command **without** `--dry-run`, writing into the
(empty) embedded datadir on the cloud PVC. Do this while iam is still staged OFF.

**Verify:** re-run `--dry-run` against `--src /var/lib/cloud/iam/iam.db` (or open it
read-only) and confirm counts equal the source.
**Rollback:** `rm -f /var/lib/cloud/iam/iam.db*` (only the freshly-written store) and
re-run. Nothing else consumes it until Step 2.

## Step 2 — Enable the embedded IAM subsystem

`iam` is a **staged** subsystem (`config.go`): an empty `--enable` mounts everything
EXCEPT staged ones. Activate it additively (does not require re-listing every
subsystem) on `universe/infra/k8s/operator/crs/cloud.yaml` env:

```yaml
    - name: CLOUD_ENABLE_STAGED
      value: "iam"
```

Apply the CR; the operator rolls the Recreate Deployment (single pod, brief blip —
expected). On boot, `clients/iam` opens `/var/lib/cloud/iam/iam.db` (the migrated
store), seeds new-only from `init_data.json` (idempotent — real rows already present,
so seed only adds anything genuinely missing), and mounts the full `/v1/iam/*` surface
IN-PROCESS. `iam_edge.go` stops mounting (`serve.go`: `if !cfg.Enabled("iam") …`), so
there is **no double-mount** and no forward to the Casdoor pod.

**Verify (still on the internal Service, before repointing the edge):**
```
kubectl -n hanzo exec deploy/cloud -- \
  curl -s localhost:8000/v1/iam/.well-known/openid-configuration | jq .issuer
# → "https://hanzo.id"
kubectl -n hanzo logs deploy/cloud | grep 'iam embedded in-process'
```
A boot failure serves fail-closed 503 on `/v1/iam/*` (cloud + every other subsystem
stay up) — it does NOT crash the binary.
**Rollback:** remove `CLOUD_ENABLE_STAGED`, re-apply the CR. The edge re-mounts and
forwards to Casdoor again. hanzo.id is unaffected (still pointed at Casdoor until Step 3).

## Step 3 — Repoint the hanzo.id identity backend at the edge

`universe/infra/k8s/ingress/routes.yaml`: router `hanzo-id-iam-api` (priority 100)
matches `Host(hanzo.id) && (PathPrefix(/v1/iam) || PathPrefix(/oauth) ||
PathPrefix(/.well-known))` → `service: iam-hanzo-ai`. Repoint that **service** from
the Casdoor pod to embedded cloud:

```yaml
    iam-hanzo-ai:
      loadBalancer:
        passHostHeader: true
        servers:
        - url: http://cloud.hanzo.svc.cluster.local:8000   # was: http://iam.hanzo.svc.cluster.local:80
```

Leave the `hanzo-id` service (`id.hanzo.svc:80`, the @hanzo/id login SPA) as-is — only
the API backend moves. The ingress file-provider applies the ConfigMap edit **HOT**
(fsnotify) — **do NOT `rollout restart deploy/ingress`** (that triggers the per-node
ACME storm / TLS outage documented in `universe/CLAUDE.md`).

**Verify:** run the Step-4 parity checks against the public host `https://hanzo.id`.
**Rollback:** revert the one `url:` back to `http://iam.hanzo.svc.cluster.local:80`;
hot-reapplies in seconds. Instant, complete rollback to Casdoor.

## Step 4 — Playwright / curl parity (drive it, don't just curl a status)

Against `https://hanzo.id` (through the repointed edge). Use Playwright for the browser
login (real interaction, not an HTTP status peek):

1. **Discovery + JWKS**: `/.well-known/openid-configuration`,
   `/v1/iam/.well-known/openid-configuration`, `/v1/iam/.well-known/jwks` — issuer
   `https://hanzo.id`, keys present.
2. **Browser login → code → token** (Playwright): `/login/oauth/authorize` → sign in
   → callback with `code` → token exchange → a verifiable JWT; the app authenticates.
3. **client_credentials** (KMS bridge / gateway guards shape) at BOTH
   `/v1/iam/oauth/token` and the alias `/v1/iam/oauth/access_token` → 200 + token.
4. **refresh** at the alias `/v1/iam/oauth/refresh_token` (the `hanzo` CLI shape) → 200
   + rotated token.
5. **userinfo** at BOTH `/v1/iam/oauth/userinfo` and the alias `/v1/iam/userinfo`
   (commerce shape) → same principal.
6. **Real callers**: force one live KMS-bridge / gateway-guard token fetch and one
   `hanzo login` + `hanzo` CLI refresh against hanzo.id → all 200.

**Accepted delta (not a regression):** BARE `/oauth/token|access_token|userinfo`
(no `/v1/iam` prefix) on hanzo.id 404 post-cutover — the rewrite serves the
`/v1/iam/…`-prefixed canonical + alias paths only, discovery advertises those, and no
live caller uses the bare form (grep-verified: every fleet caller uses `/v1/iam/oauth/*`
or an app-local `/oauth/*` proxy that rewrites to `/v1/iam/*`). Bare `/oauth/authorize`
still 302s to the login SPA via the priority-150 router (unchanged). Optionally tighten
the `hanzo-id-iam-api` rule to drop the bare `/oauth` prefix in the same edit.

**If any parity check fails: roll back Step 3 immediately** (one `url:` revert) and
diagnose with iam still embedded-but-unrouted.

## Step 5 — Retire the standalone Casdoor `iam` (final, only after parity holds)

With hanzo.id served by embedded IAM and parity green, remove the standalone Casdoor
workload. It is operator-managed via its App/CR
(`universe/infra/k8s/operator/crs/iam.yaml`, legacy
`hanzo-operator/crs/iam.yaml` + `iam-v1.yaml`). **First scale to 0** (reversible),
soak, then delete the CR + drop its basename from the Hanzo CD
`universe-crs` `include` glob (so ArgoCD stops governing it).

**Verify:** hanzo.id fully green with the Casdoor pod at 0 replicas for a full soak
(logins, token refresh, metered `/v1/*` traffic). Then delete.
**Rollback (pre-delete):** scale the Casdoor Deployment back to 1 and revert Step 3's
edge `url:` — hanzo.id is back on Casdoor in seconds. **After** the CR is deleted this
is no longer a one-step rollback (re-apply the CR from git), so hold the scale-to-0
soak until you are certain.

## Guardrails (do NOT, until this supervised session)

- Do not add `iam` to `CLOUD_ENABLE`/`CLOUD_ENABLE_STAGED` on the live CR outside this flow.
- Do not repoint `iam-hanzo-ai` before Step-2 in-process verification passes.
- Do not delete or scale down the Casdoor `iam` workload before Step-4 parity holds.
- Do not run `migrate-v1` against prod without the read-only source + a green `--dry-run`.
- Do not `rollout restart deploy/ingress` (ACME storm). The routes edit is hot-reloaded.
- Keep `replicas: 1` / `strategy: Recreate` — embedded IAM is single-writer.
