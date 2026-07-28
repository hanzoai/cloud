# kmsreseal — CR-driven KMS re-seal migration (#79)

Embeds the fleet KMS into cloud. The legacy standalone KMS (`ghcr.io/luxfi/kms`,
Deployment `kms` in ns `hanzo`) stores the fleet's secrets **UNSEALED at rest**.
cloud's embedded `/v1/kms` seals each secret with a per-secret AES-256-GCM envelope
(a fresh DEK wrapped by the env-only master key). This tool moves the secrets by an
authenticated **GET (standalone) → POST (cloud, which seals)** — a security UPGRADE,
never a raw store copy.

Driven by the **KMSSecret CRs** (`secrets.lux.network/v1`), the authoritative
`(org, path, env, key)` manifest: the standalone ZapDB is unreadable by cloud and
carries no org attribution. **Secret plaintext transits tool memory only** — never
disk, never a log; result lines carry coordinates + status, never values.

## Subcommands

    kmsreseal inventory --crs <file|-> [--kubectl] [--only-host <url>]   # offline, read-only
    kmsreseal preflight --cloud <url> [--src-audiences ..][--cloud-audiences ..]
    kmsreseal reseal    --crs <file> --src <url> --cloud <url> [--only-host <url>] [--plan]
    kmsreseal verify    --crs <file> --src <url> --cloud <url> [--only-host <url>]
    kmsreseal runbook

`reseal` is the only mutating subcommand (idempotent upserts into cloud). `--plan`
does NO network I/O. The live cutover (operator/ingress repoint, scale-down) is out
of this tool and **CTO-gated**.

## Auth model (least privilege)

Per CR, the tool authenticates with that CR's OWN `credentialsRef` machine
credential (owner==`projectSlug`), brokered at `/v1/kms/auth/login`, and reuses the
one org-bound token for both the standalone read and the cloud write. A bug cannot
cross tenants — the token itself is org-bound, and both faces gate `owner == :org`.
Tokens are cached per credential.

## Dry-run findings (2026-07, live cluster, read-only)

- **125 CRs → 530 explicit rows → 490 unique targets** (37 duplicate coordinates =
  idempotent upserts) + **12 folder-sync CRs** (empty `keys[]`, resolved by LIST).
- **8 orgs**: hanzo, hanzo-base, hanzo-commerce, hanzo-crawl, hanzo-operator,
  hanzo-search, hanzo-team, hanzo-vector.
- **4 hosts** — stage the cutover per host with `--only-host`:
  - `http://kms.hanzo.svc` — **477 explicit + 6 folders** (the main cutover)
  - `http://kms.hanzo.svc.cluster.local/api` — 1 explicit + 5 folders (same backend, `/api` base)
  - `http://kms.hanzo-devnet.svc` — 12 explicit (SEPARATE devnet KMS)
  - `http://kms.lux-kms-go.svc.cluster.local` — 1 folder (SEPARATE lux KMS)
- **G1 audience delta**: standalone `KMS_EXPECTED_AUDIENCE` = 69 app auds; cloud
  currently accepts 17 (`GATEWAY_ALLOWED_AUDIENCES`). **Must add 61**;
  `CLOUD_JWT_AUDIENCES` = the 78-entry union (shipped in universe
  `infra/k8s/operator/crs/cloud.yaml` + `cloud-reader.yaml`). Issuer needs NO change
  (cloud BrandIssuers already trusts `https://hanzo.id`).
- **Cloud `/v1/kms` is LIVE + ready**: `/v1/kms/health` = 200 `{"ready":true}`;
  no-principal read = 403; garbage-bearer read = 403 (JWT-validates, fails closed).
- **G5 (boot cycle) — SAFE**: cloud's embedded-KMS master key is a DIRECT k8s Secret
  (`cloud-kms-master-key`), NEVER KMS-synced, so cloud always boots its own KMS. The
  `KMS_ENDPOINT`/`KMS_CLIENT_*` env feed only the separate `anchorctl` CLI, and the
  treasury signer is read from the pod env (`TREASURY_ANCHOR_SIGNER_KEY`), not an
  external KMS call at boot. `cloud-api-secrets`/`commerce-secrets` ARE KMS-synced
  but `creationPolicy: Orphan` (121/125 CRs) so they persist across KMS downtime — a
  cold-start ORDERING constraint, not a hard cycle.

## Seeding wedge risk (gated prerequisite)

A folder-sync CR whose standalone path holds NO secrets would LIST empty and produce
an empty sync. `reseal` reports such a folder as `absent` ("folder empty at source")
rather than migrating nothing silently. **Before cutover, confirm each folder path
is seeded** (the 12 folder CRs above); an empty managed Secret can wedge a consumer.

## Cutover

`kmsreseal runbook` prints the ordered, rollback-safe cutover. The standalone stays
READ-ONLY the whole time (its ZapDB is never mutated) and is the instant rollback.
