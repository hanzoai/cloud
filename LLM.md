# LLM.md — hanzoai/cloud

## What this is

`hanzoai/cloud` is the unified Go binary per HIP-0106. One process,
many subsystems (iam, base, kms, commerce, ai, gateway, o11y, vfs, mq,
authz, mcp, amqp, ingress) — each registered via `init()` and
mounted at startup based on `--enable=...`. Same artifact serves
`api.hanzo.ai`, `api.osage.cloud`, `api.lux.cloud`, `api.zoo.cloud`,
and every white-label resold cloud surface.

## Storage — SQLite-only via subsystems

The orchestrator has NO storage of its own. Every subsystem owns its
per-(org, user) SQLite via `hanzoai/base` (HIP-0302). The orchestrator
is purely a fan-out: HTTP → router → subsystem.Mount() handlers.

Per the Hanzo PG → SQLite migration plan
(`~/work/hanzo/CLAUDE_PG_TO_SQLITE_MIGRATION.md`, service #4 — cloud):

### Postgres lockdown

`internal/storagelock` refuses to boot when any of these are set:

```
DATABASE_URL, DATABASE_HOST, POSTGRES_URL, POSTGRES_DSN, POSTGRES_HOST,
CLOUD_DATABASE_URL, CLOUD_POSTGRES_URL, HANZO_CLOUD_DB,
driverName (when "postgres" / "postgresql"),
dbName     (when "hanzo_cloud" exactly)
```

The `driverName` / `dbName` envs are the legacy Python `cloud-api`
deployment's knobs (see
`~/work/hanzo/universe/infra/k8s/cloud/deployment.yaml`). When that
manifest gets reused with the new Go binary by accident, this
lockdown crashes the pod loudly. The Go binary never reads these
envs; the lockdown is purely a regression guard.

`driverName=sqlite` is intentionally allowed (a transition signal an
operator might set). `dbName=anything_else` is also allowed — only the
legacy `hanzo_cloud` literal value is rejected.

Wired in `cmd/cloud/main.go` before `cloud.LoadConfig()`; 12 tests in
`internal/storagelock/storagelock_test.go`.

### Migration CLI

`cmd/migrate-pg-to-sqlite` copies a legacy `hanzo_cloud` PG database
into per-(org, user) SQLite files at `/data/<org>/<user>/cloud.sqlite`
(user-scoped) or `/data/<org>/_org/cloud.sqlite` (org-scoped).

Introspective (not schema-aware) because we don't own the legacy
`cloud-api` Python schema and it has drifted across Python/TS
versions. The migrator:

1. Reads `information_schema.tables` for the table set.
2. For each table, reads `information_schema.columns` to synthesize
   conservative SQLite DDL (`mapPGType`: int/bool → INTEGER, real/numeric
   → REAL, bytea → BLOB, everything else → TEXT).
3. Resolves routing columns: `org_id` (canonical) or `owner` (fallback),
   plus `user_id` (canonical), `user_email` or `owner_user_id` (fallbacks).
4. For each row, computes the destination file from the routing
   columns. Tokens are validated against `^[A-Za-z0-9._@+-]+$` —
   anything failing falls to the `_global` / `_org` sentinel.
5. Batched commit every 500 rows per destination handle.

Usage:

```
migrate-pg-to-sqlite \
    --src 'postgres://cloud:pass@postgres.hanzo.svc:5432/hanzo_cloud?sslmode=disable' \
    --dst /data
```

Tests in `migration/pg_to_sqlite_test.go` cover three-way routing
(user-scoped, org-scoped, no-routing), column-fallback picking,
path-traversal rejection, PG type mapping, and Options validation.

## ZAP listener

Two listeners per cloud process:

```
HTTP (cfg.ListenAddr, default :8080)   — external client surface
ZAP  (cfg.ZAPListenAddr, default :9999) — intra-Hanzo subsystem RPC
```

ZAP message-type registry (append-only):

```
 10 = control      — handshake, heartbeat (built-in)
200 = iam          — VerifyJWT, GetUser, GetOrg
201 = kms          — GetSecret, PutSecret, Sign
202 = base         — Open (per-tenant SQLite handle)
203 = commerce     — GetTenantConfig
204 = ai           — ChatCompletion
205 = o11y         — Counter, Timing, Span
206 = vfs          — Put, Get
207 = mq           — Publish, Subscribe
208 = payments     — CreateIntent, ConfirmIntent, GetIntentStatus
209 = vault        — Charge
302 = datastore    — already in use by hanzoai/datastore zap-bridge
```

Wire dispatch quirk: luxfi/zap routes on `msg.Flags() >> 8` which
truncates 16-bit msgType to its low byte. Senders write
`FinishWithFlags(msgType << 8)`.

## Build

```
GOWORK=off go build ./internal/... ./migration/... ./cmd/migrate-pg-to-sqlite/
GOWORK=off go test  ./internal/... ./migration/...
```

`cmd/cloud` itself currently has a pre-existing build break against a
stale o11y module path (`pkg/query-service/utils/times`) — unrelated
to this migration work.
