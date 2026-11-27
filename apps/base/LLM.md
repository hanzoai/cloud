# LLM.md — apps/base

Guidance for apps/base. Moved from the root LLM.md; cross-cutting rules stay in the root.

## An org's Base is written over the plane (`apps/base/plane.go`, `changes.go`)

| op | what it does |
|---|---|
| `base_import` | grows the caller's org Base by a module Base ships (`{module: "crm"}`) or an import document (`{collections: [...]}`): created what is missing, never dropped or changed what exists; ordinary base collections only |
| `base_upsert` | writes up to 500 records into one collection in one transaction; a record with an id, or a `key` value that matches (an activity's `source_id`), is updated, so a replay is one record; the writes are stamped with `source` (import, email, calendar, webhook, workflow, agent, application) and the caller |
| `base_get`, `base_list` | read a record or a page (Base filter, `where`, sort, paging, expand), every field included |

Every pooled Base is opened with `apis.StoreKeyTenant = org`, so a token acting in any
other org is refused by the Base itself whatever routed the request there, and with its
record hooks bound to the bus: each committed write is `record_created`, `record_updated`
or `record_deleted` (product `base`, attributes `collection` and `record`) on the org's
event plane, through one in-memory queue and one worker, dropped and counted when the
queue is full rather than slowing the write. Base's own NATS emitter stays off here
(`NATS_URL` is unset).

A realtime stream is opened by EventSource, which sends no header, so the gateway forwards
no principal for it. `GET /v1/base/realtime?token=<grant>` with no principal is routed to
the org the grant names (`apis.StreamGrantOrg`, read without spending it) and served, and
pinned, by that org's Base like any request. The grant comes from
`POST /v1/base/realtime/token` with the caller's bearer, which the `@hanzo/base` client
(hanzo-js/base 0.2.8+) does before every connect.

## A hosted Base is two schemas on hanzo-sql and a bucket in hanzoai/s3 (`apps/base` `sql.go`, `files.go`)

Owner rule: no service keeps state on local disk. An org's hosted Base (`{DataDir}/base/{seg}/`,
and the platform's own `_platform`) kept three things on the pod's volume; none does now.

- **The move** is `locus.MoveFile` (a `Move` for an engine that opens its own file and pool): the
  schema is made empty, Base's own migrations and the org's collections (`ImportCollections` of the
  file's, which is what makes a user collection's table, in PostgreSQL's spelling) make its tables,
  every table is copied under the file's write lock, and each is read back and held to the file's row
  count and SHA-256 sum before the registry says `moved`. A failed move leaves the Base on its
  files (registry `failed`, `moveBackoff`). `Settle` opens the file's Base once, which brings it to
  this binary's shape, and closes it before the lock. Numeric columns (`NumberField`) are a new kind
  (`kNumeric`, compared as float64); `dialect.Analyze` quotes and takes mixed case (a collection
  is named as its author wrote it). Measured: 3-4 s an org of ~40 rows, almost all migrations.
- **Two movers for a module-opened Base, one copy.** commerce's own Base uses `locus.Placed` (a
  scratch schema stands in for the sibling database; data and aux are moved separately), the hosted
  Bases use `locus.MoveFile` (both databases of one Base, with `Revert` when only one moves). Both
  go through the same `move`, lock, registry, copy and proof (`move(..., how *shape)`: `how` opens
  each side where `open` cannot); folding commerce's onto `MoveFile` is the next step and changes no
  behaviour.
- **A Base has ONE dialect** (taken from its data database), so data and auxiliary are never split
  across engines. Data moves first; if auxiliary then fails `locus.Revert` puts data back (drops
  the schema, deletes the registry row; nothing has written to it yet). A mixed state (a crash
  between) is refused at open with the reason, and the next open with a policy set finishes it.
  The mover's `Make` therefore asks both databases of the same schema; the other database's tables
  are made empty in it and nothing reads them.
- **Who waits.** The pool single-flights an open PER ORG with no lock held (it was one mutex around
  every org's open): a Base being moved makes its own org's requests wait up to `openWait` (60 s)
  and then answer 503 + `Retry-After`; every other org is served. `sweep` (`CLOUD_SQL_MOVE=sweep`)
  walks the directories smallest first under `locus.Slot`, moving without keeping a Base open; it
  takes the same per-org claim, so a request and the sweep never both move an org.
- **Files.** A record's uploaded files were `{dir}/storage/` on the volume. With `S3_ADMIN_*` set
  (`sdk/object`; the S3 driver's credential) Base is handed its filesystem
  (`baseapp.Config.Files`): bucket `object.BucketName(org, "base")` (the platform's: `Reserved`),
  made on first open, keys unchanged. The credential is the host's and read at open; it is NEVER
  written to the instance's settings row, which is why this does not use Base's own `S3` setting
  (that row is plaintext JSON in the data schema). Files already on the volume are copied once at
  open (name, content type, size read back from the bucket) and `{dir}/.storage-in-s3` says so; the
  directory is left as it was (dead copy, reversible), as the `.db` files are. The bucket shows in
  the org's `/v1/s3` listing as `base`; deleting it deletes the org's uploads. Without S3 config
  (a laptop) files stay in the directory.
- **Backups** never write `{dir}/backups` where the store is configured: `filesFor` also hands Base
  `baseapp.Config.BackupFiles` (bucket `object.BucketName(org, "base-backups")`), which is where
  Base lists, serves and deletes backups. On a server engine Base REFUSES to create or restore one
  (it archives the data directory, which holds the database only on SQLite): the database is backed
  up at the server (hanzo-sql's own age-encrypted logical backup). A Base's own `Settings().S3` and
  `Settings().Backups.S3` remain the org's to set.
- **Not moved / open.** `commerce/base/{data,auxiliary}.db` (commerce's own Base, opened by the
  module, not through this pool).
- **Base's own suite is green on PostgreSQL** (hanzoai/base v1.5.111; `BASE_TEST_POSTGRES` a UTF-8
  database, packages one at a time with `-p 1`, 67 packages ok, none skipped; the fixture is loaded
  into a template database each test app clones). What it found and fixed in Base: a nested
  `RunInTransaction` is a SAVEPOINT on a server engine; `ANALYZE` is scoped to the schema's tables
  (the bare one deadlocked a migration); a bound boolean or number beside text is typed; a rule's
  stand-in row types its columns; `DISTINCT` + `ORDER BY` through a relation groups by id; a view is
  restored in dependency order; the SQL endpoint cuts several statements where the engine takes one.
  The rest were fixtures that spelled SQLite (a view's stored text, quoting, JSON key order, where
  NULL sorts, `sqlite_master`). `TestConnMemory` is a load-sensitive measurement: run it alone.
- **Tests** (`apps/base/sql_test.go`, both engines; `CLOUD_TEST_SQL_DSN` names a UTF-8 server, Base's
  migrations create an ICU collation): `TestBaseMovesToHanzoSQL` (two orgs seeded as files, a
  deployment started over them, every collection and record equal, a token signed before the move
  verifies after and is refused by the other org, a write lands on the server),
  `TestABaseOnEitherEngine`, `TestSweepMovesTheBasesNobodyOpened`, `TestAMovingBaseHoldsOnlyItsOwnOrg`,
  `TestFilesGoToTheBucketAndStayThere`, and the plane ops (`plane_test.go`: import/grow, upsert, list,
  and the CRM install, on both engines). Auth is IAM's: a Base has no password check, so "sign in" is
  the member record, its token and the settings' token secrets surviving the move.
- **Cutover**: set `CLOUD_SQL_MOVE=sweep` on the single-replica window, with `CLOUD_SQL_CONNS` at
  least 4 x owners (base is one more). ~850 MB under `/var/lib/cloud/base` moves at the mover's
  ~100 MB/s plus ~3 s of migrations a Base; the S3 copy is the part that scales with uploads.
