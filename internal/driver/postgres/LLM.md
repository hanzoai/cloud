# LLM.md — internal/driver/postgres (the hanzo-sql engine)

Guidance for the hanzo-sql driver. Moved from the root LLM.md; cross-cutting rules stay in the root.

## The SDK interface, and where this driver plugs in

`sdk/locus` is the one way to open a per-org store: `locus.Open(ns, subsystem, dir)` (a
handle, never moves a store), `locus.Use[T](ctx, log, ns, subsystem, dir, open)` (open and
migrate, moving a file to the server first when `CLOUD_SQL_MOVE` says to), `locus.System(name,
dir)` (the deployment's own file), `locus.Single`. By default a store is the SDK's own
encrypted SQLite file (hanzoai/cek over hanzoai/sqlite). `locus.Engine` is the interface a
server driver implements; this package implements it (`engine.go`) and registers in `init`.
Linking it is what turns hanzo-sql on: `internal/host/drivers.go` imports it, every plugin
main imports `internal/driver`, and `cmd/gitimport` imports it directly. `CLOUD_SQL_DSN` set
and no driver linked is `locus.ErrNoServer` on every open, never a quiet file. The generic
vocabulary (`Place`, `Mode`, `Report`, `Online`, `Files`, `Host`, `ErrMoving`, `ErrBackoff`)
is in `sdk/locus`; `alias.go` spells it here. In the text below, `sqlpool.Org` is
`locus.Open`, `sqlpool.Store` is `locus.Use`, `sqlpool.Open` is `locus.System`, and the rest
of `sqlpool.X` is this package's `X` reached through `locus.X`.

## Prod state lives in hanzo-sql and hanzo-kv; the process holds none

Production cloud is stateless: N replicas, RollingUpdate, no PVC. Every store is a
schema on the ONE hanzo-sql; caches, leases and counters are hanzo-kv. Core
platform data is never a file shipped to S3. Embedded SQLite stays for local dev,
chosen by the absence of the DSN. The hanzo-sql backend lives in this repository
only: hanzoai/cloud, the OSS edition, stays SQLite-primary and never carries it.

- **The seam is `OrgDB` / `NewOrgStore`**, over `sqlpool.Org(ns, subsystem, dir)`. With
  `CLOUD_SQL_DSN` (a postgres:// URL) set, it returns a pool whose `search_path` is the
  store's schema: one schema per namespace x subsystem, `s_` + lowercase base32 of a
  SHA-256 over `namespace.Key` and the subsystem (54 bytes, nothing truncated), made on
  first open and listed in `public.cloud_schemas(schema, namespace, subsystem, created)`
  so sweeps enumerate stores (`sqlpool.Stores`, `sqlpool.Exists`). Every new connection
  checks `current_schema()` is the store's. Unset, it opens the SQLite file; what the
  ported stores changed in that path is named under "Ported" below, not "unchanged".
  - **A server is proven once per DSN**: a superuser role is refused; the database is
    bound to one deployment (`public.cloud_deployment`: `coord.Deployment()`, i.e.
    `POD_NAMESPACE:brand:env`, first process wins, another is refused; `CLOUD_ENV` must
    be set or nothing opens; to move a database deliberately, delete the row); a host that
    is not this machine needs `sslmode=require` (encrypts) or `verify-full` with the CA
    (also proves the server is the one named), which means ssl must be on at hanzo-sql.
    This is decided from the configuration pgx parsed, the first attempt and every
    fallback, not from the URL text: pgx reads the LAST of a repeated `sslmode`, and
    `?sslmode=require&sslmode=disable` once passed a check of the first.
    Proving is single-flight with a 3 s dial: callers who arrive during a dial, or within
    5 s of a failed one, get the last error at once; one looks again.
  - **Connections, with the arithmetic.** Inside a process one budget serves every pool
    (`sqlpool`, `CLOUD_SQL_CONNS`, default 50). But a replica is several processes: the
    host spawns a child per subsystem, each child is a binary with its own budget and its
    own administrative pool, and all inherit the host's environment, so a 50 stated once
    was 50 per child. So **`CLOUD_SQL_CONNS` in the host's environment is the REPLICA's
    budget**, and the host hands each child its share through the child's own environment
    (`manifest/sql.go`): the apps in `manifest.SQLStores` (commerce for commerce's own
    stores, finance and finance-test, treasury, flag for flags) get an equal part, at least 4; any other
    child is given 1. With a standalone process (a developer's, a test) the variable is
    that process's own. A deployment answers to
    `(replicas + surge) x CLOUD_SQL_CONNS + every other client of the server <= max_connections - headroom`:
    3 x 50 + 94 = 244 of 500 today, and a RollingUpdate that surges one replica adds 50.
    **The hard backstop is `ALTER ROLE <cloud role> CONNECTION LIMIT n` on hanzo-sql**,
    n = that sum with the surge, **applied at cutover** (not before: it bounds the role
    for every client of it). Within the budget, a pool that finds it spent waits up to
    20 s for a slot and makes room by closing idle connections of other pools, OLDEST
    first and no more than there are waiters (closing them all whenever anyone waited cost
    83% of throughput: every pool redialled at once). A connection is closable once it
    has been idle 50 ms, so one on its way to a waiter is not closed; a store that
    queries more often than that never has one that old, so a waiter that has waited a
    second also takes the least recently used idle connection whatever its age (one per
    reclaim; database/sql retries a connection that turns out dead). A closed
    connection's slot is held 15 ms so the server has dropped it before it is dialled
    again. Each store's pool is at most 4 connections, 1 kept warm, idle-closed after 30 s.
  - **commerce's own datastore is a seam store** (subsystem `commerce`, app `commerce`;
    hanzoai/commerce `db.Seam`, v1.50.201 on). It is `<CLOUD_DATA_DIR>/commerce/orgs/<org>/commerce.db`,
    one file per org plus `orgs/system` (the org records `org.Resolve` reads, the limit rows, every
    model of an org), written by the module's `db.Manager`. Cloud hands the module the seam
    (`apps/commerce/use.go` `seam`): the module asks for the store of a namespace and builds its
    own over the handle (`open(h, postgres)`), so a store is a schema on hanzo-sql with the DSN
    set, moved by `sqlpool.Move` on first open, and its file without.
    - The module's PostgreSQL store is the file's shape (`TenantColumn: "namespace"`: same columns,
      same `(id, kind, namespace)` key, a request with no namespace scoped by none, as in a file),
      so the mover carries a file's rows unchanged and one code path reads either.
    - The module is given no master key. It had keyed its files with `cek.DeriveKey(master, ns,
      "commerce")` from the master the host handed it, which is the key `sqlpool.Store` derives from
      `cek.SetMaster`, so a deployed file is read and moved as it is
      (`TestAFileTheModuleKeyedIsReadThroughTheSeamAndMoves`, codec only: `make test-codec`). A
      store still on its file is one connection (sqlpool's), not the module's dual read/write pool.
    - It is NOT `finance`/`finance-test` (cloud's books, `<CLOUD_DATA_DIR>/orgs/<org>/finance.db`:
      a different root, subsystem, opener and store; both are owned by app `commerce` in
      `SQLStores`). Commerce's own Base, `<CLOUD_DATA_DIR>/commerce/base/{data,auxiliary}.db`, does not go
      through `db.Manager` either: see "Commerce's own Base" below.
    - `SQL_URL` (the module's own shared-Postgres mode) is not read when a seam is given, and was
      never set in production. A pod still on the file after its store moved writes a dead copy:
      roll forward, as for every ported store.
    - Held by `TestCommerceKeepsItsStoresWhereTheSeamDoes` (both engines: org.Resolve and a limit
      set and read), `TestACommerceFileMovesToTheServerAsItIs` (files written, commerce stopped,
      restarted on the server with `CLOUD_SQL_MOVE=lazy`: org, limit and a merchant model read the
      same) and the module's `db/seam_test.go` (shape, idempotent open, guard).
  - **A migration needs one slot**: `Dialect.Migrate(ctx, db, func(tx) error)` runs the
    store's whole migration in one transaction on the connection that holds the lock
    (`pg_advisory_xact_lock`); nothing in it may use `db`, only `tx`. A migration that
    took the lock on one connection and ran DDL on another needed two slots, and a storm
    of first opens each held one and waited for the second.
  - **Where a store lives is a fact in the registry, not a switch** (`sqlpool.Where`). With
    the DSN set, a store of a ported subsystem (`manifest.SQLStores`, each with the app
    that opens it; also how the host knows whom to give a share of the connections) is
    read, in this order: no file under the data dir, so it is born on hanzo-sql; registry
    row `moved`, so it is on hanzo-sql and its file, if still there, is a dead copy
    nothing opens; anything else, so it is its file, exactly as without the DSN (not yet
    moved, a move that failed, or the remains of one whose process died). So the DSN
    can be set before one store has moved. A subsystem that is not ported is always its
    file (`sqlpool.File`, and an `OrgStore` of it is the file plane with the DSN set);
    the one refusal is a file for a store the registry has on the server, which would
    split it. A port adds its subsystem and app to `manifest.SQLStores` and its
    directory to `portedDirs` in `sqlgate_test.go`, in the commit that lands it.
    `public.cloud_schemas` has `state` (`moving`, `moved`, `failed`), `moving`, `moved`
    and `failed` unix times, and `error`; a row made before they existed was made on
    hanzo-sql and defaults to `moved`; `Stores` and `Exists` list `moved` only. Singletons
    that are not ported (`audit*.db`, iam, kms, tasks, pubsub) still open
    through `sqlpool.Open` (SQLite). A ported store opens through `sqlpool.Store` or an
    `OrgStore`, never `sqlpool.Org` (which opens where the store is and never moves it;
    `TestNoAppOpensAStoreWithoutTheMove`).
  - **An `OrgStore` on hanzo-sql visits no file**: no path, marker, sidecar, hydrate,
    ship loop, fence or election, and `Base.Durable` is ignored. `Sync` acks (the commit
    is the durable write). `Each` opens, uses and closes each store that is not already
    open, so a sweep of N stores holds one connection, not N. The registry is read again
    every 30 s (a store another replica made is swept; a settled one stays settled).
    `Owned(ns)` is a lease in `public.cloud_leases` (30 s term, renewed from half, judged
    by the database's clock): one replica sweeps, meters and reaps a store at a time. A
    yes carries at least half a term of margin; a replica that cannot renew says no. The
    ask is a plain SELECT (an INSERT ... ON CONFLICT burns an XID per denied ask) and a
    refusal is remembered up to 5 s, and so is a failure to ask (the answer is the same
    no, and a sweep would otherwise wait out a dead database once per row). A store that
    does not exist is not owned and takes no lease, and expired lease rows are pruned a
    day after they end. `CloseAll` sets closed, forgets its grants, then gives the leases
    up (2 s bound, dialling included; not at all for an OrgStore that never leased), in
    that order. A sweep is minutes long, so an act
    that cannot be undone asks `Owned` again immediately before it (the sandbox orphan
    reaper does, before deleting a pod; `settle` and park already did). `SayDurability`
    names the regime.
  - **Not a security boundary**: tenant isolation is each connection's `search_path`
    under one role, and `public.cloud_leases` and `public.cloud_deployment` are writable
    by that same role, so an injection in any store's SQL can rewrite a lease or the
    binding. Per-schema roles are the hardening, not yet done.
  - **Lease clocks**: the term is the database's clock and the replica trusts it by its
    own monotonic clock, which stops across a VM suspend; and after a failover to sql-1
    the two clocks' skew lengthens or shortens a term by that skew. The two seconds of
    slack and the half-term margin are what absorb them; they are not a fencing token.
  - **Durability is hanzo-sql's own.** The primary has `synchronous_standby_names =
    "sql-1"` and `synchronous_commit = on` (sql-1 streaming sync, sql-0 async), so a
    committed ledger write is on the primary and sql-1 before it is acknowledged. A
    failover must therefore promote sql-1; promoting sql-0 can lose acknowledged money.
- **One dialect.** Store SQL runs unchanged on both engines. `sdk/dialect` is
  the one place they differ (`Of(db)` reads the engine off the driver):
  - `$n` placeholders, numbers rising in order of first appearance (SQLite numbers `$n`
    by appearance, so `b = $2 AND a = $1` silently swaps them); `dialect.Marks` for IN
    lists. The rules live in `internal/sqlcheck` and are asked two ways. `sqlgate_test.go`
    reads every string literal of every ported package for `?`, `?N`, `INSERT OR <x> INTO`,
    `UPDATE OR <x>` and `REPLACE INTO` (SQL comments removed first, but not a `--` inside a
    quoted string), and follows the text a statement is assembled into through `+`, `+=`,
    constants, Sprintf and Fprintf arguments, Join, Repeat and strings.Builder writes, to
    require `$1..$n` without a gap, in order of first appearance. What a reading of the
    source cannot see (a closure, a method value, a helper's return, a struct field,
    another package's constant) is read at run time: the postgres leg of `sqltest` asks the
    same rules of every statement a test executes, as the connection receives it.
  - `INSERT ... ON CONFLICT ... DO UPDATE/NOTHING`, `RETURNING` (not `INSERT OR
    REPLACE/IGNORE`, `UPDATE OR IGNORE`, RowsAffected); an explicit id/seq column
    (`Identity()`), never `rowid`; integers are `BIGINT` (Postgres `INTEGER` is 32 bits);
    times are integer unix values set from Go (RFC3339 TEXT only where SQL never compares
    them); `->>` on a bare key, chain `->` for depth (`doc -> 'a' ->> 'b'`, never a `$.a.b`
    path), column `JSON()` (jsonb) only when SQL looks inside, else TEXT (jsonb renormalises).
  - `LIKE` is ASCII case-insensitive on SQLite and case-sensitive on Postgres: write
    `lower(col) LIKE lower($1)` where case must not matter, or query in the stored case.
    A scalar subquery names one row (`ORDER BY ... LIMIT 1`). No `PRAGMA` outside the
    SQLite opener; column existence is `Dialect.HasColumn`, not `table_info`. Postgres
    TEXT refuses NUL and invalid UTF-8, which SQLite stores: reject both at the boundary.
    After any failed statement Postgres aborts the transaction; SQLite carries on.
  - DDL types come from `Identity()/JSON()/Blob()`; a migration holds `Dialect.Migrate`
    (two pods racing `CREATE TABLE IF NOT EXISTS` fail on Postgres); a transaction that
    reads then writes starts with `Dialect.BeginWrite` (the store's write lock, READ
    COMMITTED stated; SQLite's one connection did this and is asked for nothing).
  - A table copied with explicit ids (the move, below) calls `Dialect.Resync(table, col)`
    for each `Identity()` column afterwards, or the first insert takes id 1; it errors on a
    column with no sequence on Postgres, and on a table or column that is not there on
    either.
  - **Full-text search is `dialect.Words` and `dialect.Grams`**, and a store writes
    no FTS5 and no tsvector. `d.Words(table, key, columns...)` finds rows by the words
    of their own columns, a caller's term matching as the beginning of a word (`depl`
    finds `deploy`): SQLite is an external-content FTS5 table `<table>_fts` with insert,
    delete and update triggers, built from the rows already there the one time it is
    made; PostgreSQL is a `fts tsvector GENERATED ALWAYS AS (...) STORED` column of the
    table with a GIN index, so nothing is kept in step. `d.Grams(table)` finds a
    substring (three characters or more, ignoring case) of texts the caller `Put`s under
    an integer key: FTS5 trigram, or a table with a pg_trgm GIN index. Both are
    created by `Create(ctx, tx)` inside `Dialect.Migrate` (which asks for the column, index
    or extension before making it: `ALTER TABLE` and `CREATE INDEX` lock out the table's
    writers even to find themselves already there, and a store is opened again and again),
    and both answer
    `Hits(first, terms, all)`: a subquery of `hit` (the row's key) and `score` (ascending
    is best) plus the values to bind from `$first`, which the store joins to its table
    beside every other narrowing (`JOIN (` + hits + `) ON hit = id ... ORDER BY score, id`),
    so a search is one term of a read and not a read of its own. A term is words
    (a run of letters and digits between anything else: `acme/svc/api` is acme, svc and
    api) that must be adjacent and in order; `all` requires every term, else any.
    The engines agree on the rows; they do not agree on the order of near ties (bm25 and
    a word's rarity on SQLite, ts_rank on PostgreSQL), on diacritics (SQLite folds
    `cafe` into `café`; PostgreSQL's simple dictionary does not), or on a phrase across
    two columns (a phrase stays in one column on SQLite). PostgreSQL makes
    punctuation a space before it parses, since its parser reads `acme/svc/api` as one
    file path. **`Grams` needs the `pg_trgm` extension in the database** (a trusted
    extension: `Create` installs it with `CREATE EXTENSION IF NOT EXISTS pg_trgm SCHEMA
    public` when the role may, else an administrator does it once; hanzo-sql is the
    official image, which ships it).
  - **Collation.** SQLite compares and orders text by its bytes; PostgreSQL by the
    database's collation, under which a prefix range read (`entity >= 'acme/' AND entity
    < 'acme0'`) and `ORDER BY` are not the keys and order SQLite gave (the test server
    is made with no locale and would never show it). A TEXT column that is ordered or
    read by range is declared `TEXT ` + `d.Bytewise()` (`COLLATE "C"`). A table that is
    only a primary key read as a run (`terms`) ends `) ` + `d.Clustered()` (`WITHOUT
    ROWID` on SQLite). `d.HasTable` is `HasColumn` for a table. `d.Index(name)` is
    SQLite's `INDEXED BY` where its planner must be told to walk a range, and nothing on
    PostgreSQL. `d.Join()` is `CROSS JOIN` on SQLite (read in the order written; its
    planner otherwise walks every document and probes the matches) and `JOIN` on
    PostgreSQL, whose CROSS JOIN takes no ON. `d.Analyze(ctx, tx, tables...)` after a bulk
    write refreshes PostgreSQL's statistics in that transaction (a table filled in one
    statement has none for up to a minute, and a range read joined to its documents was
    planned as a nested loop: 18 s for one word in 20,000 documents) and is nothing on
    SQLite, whose hand-tuned plans a collected `sqlite_stat1` would change.
  - **Erasure.** A DELETE leaves the bytes in a SQLite file's free pages and WAL.
    `d.Shred(ctx, tx, words...)` before the deletes (secure_delete, and each `Words`
    index drops the terms it held) and `d.Flush(ctx, db)` after the commit (a WAL
    checkpoint) are what an erasure owed in the file asks of SQLite. **PostgreSQL has no
    file for a statement to scrub**: there an erasure is its commit, and how long the
    dead row, the WAL, the replicas and the backups keep it is the server's retention.
  - **What the ports found a shared spelling for** (all in `sdk/dialect`, each tested
    on both engines):
    - A word search is `col LIKE $n ESCAPE '\'` with the pattern escaped in Go; `instr`
      and `strpos` are each one engine's. Where case must not matter, `lower(col) LIKE
      lower($n)`.
    - `string_agg(x, sep ORDER BY x)` is shared (SQLite 3.44+; both builds link 3.53);
      `GROUP_CONCAT(DISTINCT x)` is not: group by the value in a subquery first.
    - A table of records that nothing may rewrite uses `Dialect.Refuse` (`RAISE(ABORT)`
      trigger on SQLite, a plpgsql `refusal()` and a trigger on PostgreSQL, made once and
      left if present); the WHEN condition is written `IS DISTINCT FROM`, which both read.
    - What a file ordered by `rowid` is a `seq` column the store numbers itself:
      `Dialect.Sequence` adds it and numbers a file's rows once, `NextSeq(table)` is the
      expression for a new row (inside a `BeginWrite` transaction), ties fall to the
      primary key. A table with an UPDATE trigger has it lifted around that one numbering.
    - `PRAGMA user_version` and the like are rows of a table (knowledge's `marks`); a file
      keeps its old value, read once from `pragma_user_version`.
    - A cut of a text column by byte offsets is made in Go, not with `substr` over a BLOB
      cast. A boolean written to an integer column is 0 or 1. `SUM` is cast to `BIGINT`
      where it is scanned into an integer (PostgreSQL sums to numeric). A bound text
      parameter is a Go string: an int is not encoded to text.
    - `dialect.IsUnique` classifies a unique or primary-key violation from either engine;
      `Text` refuses a NUL and bytes that are not UTF-8 where a caller's text enters a
      store; `Args` numbers marks as a statement is written in pieces.
    - The source gate reads `$n` as a placeholder only where a value goes (after an
      operator, a comma, an opening parenthesis or a keyword that takes one), so a dollar
      amount in prose is not one; a `?` after a `%s` in a Sprintf is still read as one.
  - **A suite that composes neighbours that are not ported yet** mounts the store under test
    with the leg's DSN and the rest without it (`t.Setenv(sqlpool.Env, "")` around their
    `Use`, or around the request that opens their lazily made store: a wallet, the
    framework `install`), because `OrgDB` refuses an unported subsystem when the DSN is
    set. Each such line says what it waits for and goes when that store is ported.
- **Every store's tests run on both engines.** `sqltest.Engines(t, func(t, open))` runs a
  body on SQLite and, when `CLOUD_TEST_SQL_DSN` is set, on Postgres in a database made
  for the test and dropped after it (`CLOUD_SQL_DSN` is set to it for the body, so
  `OrgStore`/`OrgDB` follow). With `CI` set and no server, the postgres leg fails
  instead of skipping. `scripts/postgres.sh <cmd>` starts a throwaway Postgres 18
  around one command: the pinned official binary (sha256 checked every run, cached in a
  0700 per-user directory), no TCP (a unix socket in a 0700 run dir), the server, COMMAND
  and the run dir all go away even if the script is SIGKILLed, no root or container
  needed. Linux only; needs setpriv, unzip, xz, curl. On a workstation a SIGKILL of the
  script leaves the `go test` binary it started running until its `-timeout` (the
  server, `go test` and the run dir go; the grandchild does not): not a CI-pod concern.
  CI is `CI`, `GITHUB_ACTIONS` or `HANZO_CI`. `hanzo.yml` runs
  `go-unit` and `affected` under it. A store is ported when its suite is green on
  Postgres.
- **Ported**: `apps/treasury/ledger/sqlstore` (`seq` column orders entries; amounts stay
  decimal TEXT; on SQLite an existing book gains `seq`, a unique index and an
  `AFTER INSERT` trigger in ONE transaction on first open, numbered by rowid
  `WHERE seq = 0` so a rerun finishes a partial one; the trigger numbers rows an older
  binary inserts, so a rollback needs no data step; fresh tables use `BIGINT`; ordering
  moved from rowid to seq), `apps/flags`, `apps/marketplace`, `apps/knowledge`,
  `apps/books` (live and sandbox; money stays integer cents; `post` takes the voucher id
  from `RETURNING`; the capture and payable claims start with `BeginWrite`),
  `apps/principals`, `apps/tax` (the immutability triggers are `Dialect.Refuse`),
  `apps/legal`, `apps/x402` (one claim wins an authorization, tested with 16 writers),
  `apps/company`, `apps/world` and `apps/pref`. Legacy-file upgrades (cents, scope, seq)
  are SQLite-only by nature and their tests build the old file by hand; the seq upgrade is
  also killed with SIGKILL at nine points and reopened.
- **Ported (integrations, kms, projects, affiliates, leaderboard, limits, taxonomy, webhooks, tel, environment)**: `integrations` (apps/provider), `kms`, `projects`, `affiliates`,
  `leaderboard` (store.go only), `limits` (apps/ai/limits, opened by `ai`; zen is
  co-resident), `taxonomy`, `webhooks`, `tel`, `environment`. What they settled:
  - **Secret columns are sealed, not left to the file.** `webhooks.endpoint.secret` (the
    HMAC key) and `integrations.grants.code` (a provider code, exchange-complete on its own)
    held plaintext that only SQLCipher protected. They are sealed as fields (see "Sealed
    columns", below), bound to the row, as is the webhook endpoint's address. They were
    `sealed.` + base64 of `org.Cipher.Conceal`, which binds a namespace and a subsystem and no
    row; the migration of a SQLite file opens that form once and seals the field, and no store
    reads `sealed.` any more. A process with no master (`deps.MasterKey`) opens no such store
    (webhooks answer 503). Rolling back to the binary before sees `seal:...`; roll forward.
    kms values were already an AEAD envelope; its coordinates (path, env, name, scheme, key
    handle, policy id) sit as given on hanzo-sql, where the file hid them.
  - **`Dialect.Serialize(key)`** is `BeginWrite` for a singleton on a hot path: `limits`
    keys by payer (org/subject), because every statement names its payer and one store-wide
    lock would queue every model call on the platform. Use `BeginWrite` for a store of one
    org's rows.
  - **`portedDirs` takes a file** (`.go`) for a store among code that is not SQL: the gate
    reads every string literal, so `apps/provider` (URLs joined with `?`, a ClickHouse
    insert) and `apps/leaderboard` (ClickHouse builders) name their store files.
  - **A rowid is a column.** `projects.releases.seq` (MAX+1 under the write lock; a file's
    rows are numbered by rowid once, and an older binary's inserts, seq 0, the next time it
    opens). `limits.request` has no identity, so a release counts the payer's rows at that
    second, deletes them and puts all but one back.
  - **Collation.** `scripts/postgres.sh` runs `initdb --no-locale`, so a test cannot see
    that `ORDER BY` on a text column (an id, a slug) orders by the database's locale in
    production and bytewise on SQLite. These ports order by a number first and break ties
    on a text id; a column whose order is read as order wants `Bytewise()`.
  - Tests whose subject is a file an older binary made (legacy schemas, the pre-sealed
    secret) build it by hand and run on SQLite only (`t.Setenv(sqlpool.Env, "")`); the
    others share a database per test, so a test that opens a second store of the same
    subsystem must empty it first (`limits`' `empty`).
- **Ported: `apps/graph`** (`graph`, opened by the graph app). `assertion` is `seq` +
  `Identity()`, `entity`/`relation`/`value` `Bytewise()`, integers `BIGINT`, `confidence`
  `DOUBLE PRECISION` (PostgreSQL's REAL is 4 bytes), the search a `Words` index
  (`fts` column on PostgreSQL, so an erasure removes the terms with the row). A
  redelivered assertion is `ON CONFLICT (id) DO NOTHING RETURNING seq`, so seq has gaps on
  PostgreSQL. A file in the old shape (no `until`, an index without the update trigger)
  upgrades in place on SQLite (`TestALegacyFileIsUpgradedInPlace`). Erasure on SQLite
  is unchanged (secure_delete, the index's secure-delete, a WAL checkpoint:
  `Dialect.Shred`/`Flush`); **on PostgreSQL it is the commit**, and the dead row,
  the WAL, replicas and backups are the server's retention, which no statement
  here bounds. Search ranks by `ts_rank` there, which does not weigh a word's rarity
  as bm25 does, so `answer`'s limit can keep different rows. `apple.go` reads
  Apple's own SQLite files (`sqlite_master`), which are never a store.
- **Ported: `apps/code`** (`code`, opened by the code app). The chunks' search is a
  `Grams` index (`fts`), keyed by `chunks.id`: substrings, not words, because the regex
  tier pre-filters candidates by literal fragments of a pattern (`seErr` in `parseError`), which
  a word index would lose. Its terms are plain strings and a flag for all-or-any; a
  term of fewer than three characters finds nothing on either engine. Ranking is how many terms
  a chunk holds on PostgreSQL, bm25 on SQLite. A repo name matches its bare name and any
  `owner/name` spelling, case-folded as SQLite's LIKE folded it. A file with a NUL or bytes
  that are not UTF-8 is refused by the index op (400, naming it) and skipped by a push
  (`IndexFiles`). An old SQLite file keeps its FTS5 table, `chunk_id` column and all, and new
  rows leave that column empty (`TestALegacyFileIsUpgradedInPlace`).
- **Ported: `apps/index`** (`index`, opened by the index app, the system namespace's one
  store through `sqlpool.Org`). `terms` stays an ordinary inverted index and not a `Words`
  index: its terms are one tokenizer's folded words, identical on both engines, and a
  prefix is a range seek. `term` and `pk` are `Bytewise()` (a prefix is the range
  `[lo, hi)`), `terms` ends `Clustered()`, and the search joins documents with `Join()`.
  A batch writes documents and terms a statement to a chunk (a round trip each made a
  20,000-document upsert 40,000 of them) and a key given twice in a batch is the last
  document given. A batch of 5,000 terms or more `Analyze`s `docs` and `terms` in its
  transaction. A document whose key or user is a NUL or not UTF-8 is skipped as an
  empty key is; a uid like that is a 400. The legacy table adoption runs in the
  migration with `HasTable`. The query plan test reads SQLite's plan and runs on SQLite only.
- **Ported: the goja Base host and the four apps over it** (`goja/`, `apps/captable`,
  `apps/dataroom`, `apps/esign`, `apps/trust`; subsystems `captable`, `dataroom`, `esign`,
  `trust` for the tenants, `link_index` (dataroom) and `token_index` (esign) for the two
  cross-tenant indexes in the system namespace; four more owners for the connection
  budget). The SQL was never in this repository: each app's logic is a JS bundle from
  its own module (`hanzoai/captable`, `hanzoai/dataroom`, `hanzoai/esign` (renamed from
  `hanzoai/sign`, so the import path moved with it), `hanzoai/trust`) that issues
  statements through `__db.query/exec`. So the port is in the bundles, in the shared
  dialect, and one bundle runs on both engines: `$n` placeholders numbered by hand,
  quoted `"camelCase"` aliases (PostgreSQL folds an unquoted name), `CAST(SUM(..) AS
  BIGINT)`, text arguments always strings, `NULLS FIRST` where a nullable column orders
  who signs next. `BaseHost` opens each tenant through `cloud.OrgDB`, runs
  `BaseConfig.Migrate` (it replaced `Schema string`; it gets the `Dialect`, so trust asks
  for `Bytewise()` and esign for `Sequence()`) inside `Dialect.Migrate`, and begins every
  dispatch and every `Tx` with `Dialect.BeginWrite`, which is what keeps captable's
  COUNT+1 share-class index and a sequential signing order safe (the concurrent test fails
  without it). `__db.exec` answers `{changes}`: there is no `lastId`. A NUL or non-UTF-8
  text argument is refused at the bridge, the request answers 400 whatever the bundle did
  with the error, and the Go-side writes in `dataroom/trust` call `clean`. esign's
  `recipients` and `audit_logs` gain a `seq` (indexed) because their order was rowid's;
  `page`, `signing_order` and positions are `DOUBLE PRECISION` because the typed contract
  says the bundle keeps the caller's number. Tenant files made before keep opening
  (`TestALegacyFile...` in each app): a table that exists is not touched, and SQLite gives
  BIGINT and DOUBLE PRECISION the affinity INTEGER and REAL had. Published as captable
  v1.0.1, dataroom v1.1.8, esign v1.13.4, trust v0.2.5. A tenant opens on its
  own entry, never under the pool's mutex: callers for the same tenant share the one open,
  different tenants open in parallel, and an open tenant never waits on another's
  (`TestASlowOpenDoesNotDelayAnOpenTenant` fails with the old mutex); a failed open is
  every waiter's answer and is not kept.
- **Ported beside their warehouse: `apps/o11y`** (`o11y_annotations`, the annotation
  queues), **`apps/reference`** (`reference`, a tenant's overrides over the shared
  baseline) **and `apps/risk`** (`risk`, the shelf: resume cell, search runs, policy and
  model-value histories, the observation record, the rollup watermarks). Each package also holds ClickHouse statements with `?` placeholders, so its
  warehouse files are named in `clickhouseFiles` (sqlgate_test.go) and the gate reads the
  rest; a file that holds a statement for both engines and for ClickHouse is split before
  it is listed. In reference, `override.key` is `Bytewise()` (the list pages by it) and a
  key, note or writer that is a NUL or not UTF-8 is a 400 at the wire and refused by the
  store; the measured row-cost test reads a SQLite file and runs on SQLite only. In
  risk the rollup watermark moved from `feature.go` to `watermark.go` so that
  `feature.go` (and `baseline.go`) are warehouse alone; `observation.id` is `Bytewise()`
  (the replay and the retention cut are on `(at, id)`), REAL columns are `DOUBLE
  PRECISION`, the model-value sequence is allocated under `BeginWrite`, and the
  observation record reports `first` from `RETURNING` and not `RowsAffected`. A text
  that is a NUL or not UTF-8 is a 400 at `observe` and at `enact`. An eviction is
  `takeOut` (remove the resident and take its tenant's slot, under `p.mu`) then `retire`
  (write it down, give the slot back); the slot is what makes the tenant's own fold wait,
  because the fold reaches `resident()` from its goroutine and a residency opened in the
  gap between removal and save plants an empty model that the save cannot undo.
  `open` always did this; a test that removed the entry by hand did not and failed one
  run in forty on PostgreSQL, so it evicts through the same two steps now. The two tests
  that measure a SQLite file's pages stay on SQLite.
- **Left on SQLite, and why.** `apps/dataroom`: its tenant data is `goja.BaseStore`
  (`cek.Open`, SQL written inside the JS bundle, shared with `esign`, `captable`, `trust`);
  only its link index is a plain store, and porting that alone would leave the data it
  points at on a disk. `apps/framework`: all of its
- **Left on SQLite, and why.** `audit`: the chain head (`nextSeq`, `headHash`) lives in one
  process that holds the chain, and `Trail` finds the sibling chains by globbing the data
  directory; across replicas the head has to be read under `BeginWrite` per batch and the
  family listed from the registry, which is a design call. `apps/framework`: all of its
  SQL (`json_extract`, `pragma_table_info`, `?`) is in `hanzoai/framework`, which takes
  the handle; it ports there and is then re-pinned.
- **Ported: `apps/research`, `apps/patent`, `apps/reward`: the `hanzoai/orm` stores**
  (v0.6.45: `orm.AdaptSQL` reads the engine off the pool, so the seam's `*sql.DB` is
  handed to orm as it is, on SQLite and on hanzo-sql; the PostgreSQL adapter's
  semantics are in the orm repo's LLM.md, "PostgreSQL JSON Storage"). Research and
  patent open through `OrgStore` (a per-org `_entities` table); reward is a singleton,
  `sqlpool.Org(namespace.System(), "reward", dir)`. What the three settled:
  - orm's transactions hold the store's write lock on PostgreSQL (the same
    `current_schema() || ':write'` advisory lock as `Dialect.BeginWrite`), so the
    append clock (`loadSeq`/`putSeq`, the per-store `seq` that orders canonical
    versions) is read and advanced with no lost update across replicas and nothing to
    retry (`TestConcurrentIngestSeqMonotone` on both engines).
  - jsonb renormalises a document (spacing, key order). Research's `Meta` and
    `LibVersions` are read through `shown` (canonical form), so a response does not
    depend on the engine. A string holding a NUL is a SQLite row and a PostgreSQL
    refusal (22P05), so every research write refuses it first, 422, on both
    (`holdable`, `dialect.Document`): the batches, and the evidence, decision,
    evaluation, action, authorize, commitment, grant and artifact endpoints.
  - Research's legacy raw-SQL tables are detected with `Dialect.HasTable` (never
    `sqlite_master`); only an old SQLite file has them, `migrate_test.go` builds one.
  - **Reward on the ZAP backend is unchanged**: `IAM_STORE_BACKEND=sql` keeps reward's
    rows on hanzo-sql over ZAP (`store.Connect`, the call IAM's store makes), in the
    database IAM uses, not in a seam schema. With the DSN set and that variable unset
    the rows are in the seam's `reward` schema. Moving rows from the ZAP database into the
    schema is a data copy no one has written.
  - Reward's ClickHouse reads (`usage.go`, `profiles.go`, `referral.go`, `tenure.go`) and
    research's warehouse DDL and inserts (`datastore.go`) use `?` and are in
    `clickhouseFiles`.
  - Tests: a body that opens more than one store (patent's `newStore`) names each
    store's org apart, since on hanzo-sql one database serves every store a test opens;
    a rate limit kept per process (reward's `allow`) sees the second engine's run as the
    same member, so the run names its member by engine.
- **At rest.** The ports move SQLCipher-protected content into hanzo-sql columns. What is a
  credential or personal data is sealed ("Sealed columns", below, with the list of what is
  still plain and why); the rest rests on the volume and the network.
- **Connections.** Each port adds an owner to `manifest.SQLStores`, and each owner is
  handed at least 4 of `CLOUD_SQL_CONNS`, so the replica's budget is at least 4 times the
  owners (this batch brings them to thirteen: 52, and the default 50 does not cover it)
  before `ALTER ROLE ... CONNECTION LIMIT`.
- **Ported with `train`, `tools`, `team`, `guide`, `sync`, `validator`, `prompt`,
  `experiment`, `template` and `esign`'s token index** (`manifest.SQLStores` names each
  subsystem and its app). What they settled, for the ports that follow:
  - **A document looked inside by SQL is a `JSON()` column; the engines' JSON functions
    are not used.** `json_extract`, `json_set`, `json_each` have no spelling both read, so
    a rewrite of a stored document (train's spec reshape, team's channel facet rename) is
    Go over the few rows a `->`/`->>` query finds, and a set-membership over an array
    (which sha256s a live job reads) is Go over the specs. A publish record that SQL only
    ever read whole stays TEXT and is answered in Go.
  - **JSON text is refused before it is stored if PostgreSQL would**: `dialect.Text`
    (NUL, not UTF-8) for a TEXT column and `dialect.Document` (adds `\u0000`, not
    JSON) for a JSON one. SQLite keeps both; a value that fits on one engine is the same
    error on both.
  - **A read-then-write starts with `BeginWrite`** and a unique-violation is told by
    `ON CONFLICT DO NOTHING` + `RowsAffected`, never by the text of SQLite's error: the
    MCP server cap, a validator slot claim, a prompt's next version, a blueprint's next
    version.
  - **`LIKE` is lowered on both sides** (`lower(x) LIKE lower($n) ESCAPE '\'`); a boolean
    is 0/1 set from Go; `rowid` orderings become a named tie-break; a scalar subquery
    names one row; a subquery in FROM has an alias.
  - **A store that reads or writes another subsystem's unported store runs that test on
    SQLite alone** (guide and experiment land effects in `framework` and `research`,
    team seals its secret in `kms`), and says so on the test.
  - **Not ported**: esign's documents (the per-tenant Base of `goja.NewBase`, shared
    with dataroom, captable and trust, whose SQL is written by the signing bundle in
    SQLite's dialect).
  - **Open**: team's per-space write lock (`docStore.writing`) is a mutex of one process;
    before two replicas serve one space it has to be a database lock. Thirteen apps now
    own stores, and the default budget of 50 connections is below 13 x the floor of 4:
    either `CLOUD_SQL_CONNS` is stated per deployment or the default or the floor moves.
- **Cutover checklist** (what the first deployment with `CLOUD_SQL_DSN` needs, in order):
  `pg_trgm` in the database (the code store's substring index: `CREATE EXTENSION pg_trgm
  SCHEMA public` by an administrator, or by the cloud role if it may create objects in `public`;
  it is a trusted extension and ships with the official image hanzo-sql is built on);
  `CLOUD_ENV` in the universe values **before** `CLOUD_SQL_DSN` (the helm chart carries
  only `POD_NAMESPACE`; with no `CLOUD_ENV` every open refuses, by design); ssl on at
  hanzo-sql and `sslmode=require` or `verify-full` in the DSN; `CLOUD_SQL_ADMIN_DSN` set
  (the hanzo-sql administrator's URL, from the KMS secret `postgres`): without it
  `locus.Tenants`, which opens `studio` and `library`, refuses to open them (their pods
  do not become ready) rather than fall back to a SQLite file under each pod's data
  directory;
  `CLOUD_SQL_CONNS` set to the replica's budget per the arithmetic above; then `ALTER ROLE
  <cloud role> CONNECTION LIMIT n`. A migration that ignores a statement's error and carries on (`ALTER ... ADD
  COLUMN` that may already be there, a `DROP` that may not) aborts the whole Postgres
  transaction at the first error: port it as separate statements, each guarded by a
  `HasColumn`-style check, never as try-and-continue. Then `CLOUD_SQL_DSN` with
  `CLOUD_SQL_MOVE=sweep` (and a `CLOUD_SQL_CONNS` that leaves the movers room), one replica;
  when no registry row of a ported subsystem is `failed` or missing for a file on the
  volume, drop the claim and scale out.
- **The move: a store at a time, while cloud serves** (`sqlpool.Move`, called by
  `sqlpool.Store`; `CLOUD_SQL_MOVE`). Not one cut and not a command: `cmd/cloud` is the
  light host and links no SQLite engine, and a store's tables are the DDL its own open
  runs (Identity(), Bytewise(), JSON(), Words, Grams), which only the app that owns the
  store has. So the process that owns a store moves it, the first time it opens it.
  - **Policy.** `CLOUD_SQL_MOVE` unset: stores that are files stay files, so setting the DSN
    moves nothing. `lazy`: a store moves the first time this process opens it
    (`OrgStore.For`/`Each`, or the opener of a singleton, all through `sqlpool.Store`).
    `sweep`: lazy, and each `OrgStore` walks its subsystem's files smallest first in the
    background, one at a time across the whole deployment (`sqlpool.Slot`, an advisory
    lock), logging where it is every 30 s; opening is moving, so a store a request asked
    for in the meantime is skipped. Any other word is an error. The move is for the
    single-replica window, before the deployment scales out: set it on the deployment that
    carries the DSN, which restarts the process (Recreate), so every store is first opened
    by the process that moves it and no handle on a file outlives its move.
  - **What holds the store still.** In the process, the open itself: every `For` of the
    namespace waits on the one open in flight (up to `openWait`, a minute, then
    `sqlpool.ErrMoving`: the caller retries), so nothing writes while it runs and nothing
    holds a handle on the file. On the file, SQLite's write lock (`BEGIN IMMEDIATE`, taken
    before the first row is read and held until the registry says moved): any other writer
    of the file, in any process, waits at the lock a writer always takes. On the server, a
    session advisory lock named for the store, dropped with the connection, so two movers of
    one store cannot both run and a mover that dies leaves nothing locked.
  - **Order.** The store's own open runs on the file first (its SQLite migration, which is
    where the in-place upgrades live: a file this code never opened is in the old shape),
    then the schema is made empty and the store's own open runs on it. Every table the
    schema has is truncated (what the migration seeded is the file's rows by now), copied
    by name in foreign-key order with COPY, each identity column `Resync`ed, the tables
    analysed, then every table read back and its row count and checksum held to the file's
    (SHA-256 per row over type-tagged values, summed in 256 bits, so order is nothing and a
    duplicate does not cancel; jsonb is compared by meaning). Only then does one UPDATE
    mark the registry `moved`; before it the schema is scratch and the store is its file,
    after it the store is the schema and nothing opens the file again (it stays on the
    volume, as it was left, for the rollback below). Types follow the
    target: bigint, float, text, bytea, json(b), boolean, timestamp (kept to the microsecond
    PostgreSQL holds). A value the target cannot hold
    (NUL or invalid UTF-8 in text, text in a BIGINT, a document that is not JSON) fails the
    move naming the column. A table of the file with rows and no counterpart on the schema
    fails it too, and so does a virtual table other than the trigram index (`Grams`, carried
    from `rowid` to `id`, lowered); a column the schema dropped is reported and left behind.
  - **A failure is a store that stays a file.** The schema is dropped, the registry says
    `failed`, when and why (`error`), and the open goes on to the file, so the store serves
    as it did. It is not tried again for ten minutes (`ErrBackoff`, also a file). A move
    stopped by a signal (`CloseAll` cancels, and waits for the sweep) is not a failure: its
    row is removed. A row left at `moving` by a dead process, with no session holding the
    lock, is taken as a failure that did not get to say so, and the next mover starts over.
    A store's move is atomic and not resumable inside it; the sweep resumes by skipping
    what is `moved`.
  - **Who moves what.** A store that opens itself (a system singleton, `tools`, `reward`,
    a space's `docs` in `team`) moves at its first open in the process, in `sqlpool.Store`;
    the sweep walks `OrgStore`s only, so a store of the first kind that is never opened
    stays a file. The orm's `_entities` (reward) moves with its `timestamptz`, `jsonb` and
    boolean columns converted from the file's; `_metadata`, which the orm makes on SQLite
    alone, is left behind while empty.
  - **What the others see meanwhile.** A store that is still a file keeps its `.pending`
    marker and is swept (`Each`), counted (`Has`, `Stored`) and leased (`Owned`) like any
    store; unmoved stores are not shipped to the object store (`dur` is nil with the DSN
    set), so the volume is their only copy until they move.
  - **Rollback**, while single-replica: unset `CLOUD_SQL_MOVE` and delete the registry row
    (`DELETE FROM public.cloud_schemas WHERE schema = '<s_...>'`, found by `namespace` and
    `subsystem`) and `DROP SCHEMA ... CASCADE`; the store is its file again, and what was
    written to hanzo-sql since it moved is not carried back. Progress is
    `SELECT subsystem, state, count(*) FROM public.cloud_schemas GROUP BY 1, 2`.
  - **Connections.** A mover holds a session for its lock and a pool for the schema, and
    the sweep one more for its slot, per process: raise `CLOUD_SQL_CONNS` (a child's share
    is at least 4) for the deployment that moves, or a move waits for a slot and fails.
  - **Rate** (`BenchmarkMoveRate`: `scripts/postgres.sh go test -run '^$' -bench MoveRate
    -benchtime 1x ./sqlpool/`, built `libsqlite3` against libsqlcipher as the image is, a
    PostgreSQL on a unix socket on the same box): a 512 MB store of 4.6 KB rows (116,711
    rows) moved in 4.8 s, 105 MB/s with the read-back; 128 MB at 99 MB/s; 16 MB at 71 MB/s,
    since a store costs about 0.15 s before its first row (lock, schema, two migrations).
    That is the mover's ceiling: across the network to a hanzo-sql with a synchronous
    standby it is lower, and the rehearsal on a snapshot is what measures it. A store's
    writers wait for its move, so the pause is its size over the rate (1 GB about 10 s);
    the 61 GB is about ten minutes of copying and, at 0.15 s a store, half an hour of
    fixed cost over 11,010 files, one store at a time.
  - Encryption at rest: fields the SQLCipher files protected are KMS-wrapped columns, never
    plaintext.
- Hashed credentials (`apps/git` `runner.token_hash`/`pool.secret_hash` with their salts,
  `apps/agents` `key_hash`) are one-way and stay plain.
  5. The move, store by store, while cloud is still one replica (`cloud sqlmove`).
  6. Columns SQLCipher protected are sealed before their store moves (personal data,
     credentials: compliance, destination, books memos, legal and company documents,
     knowledge text, tax profiles).
  7. Not on the seam yet: audit (one process holds the chain head), git objects (packs and
     refs are files, not SQL; the home is S3 packs and refs in hanzo-sql). framework
     (hanzoai/framework v0.1.13) and the lists (hanzoai/newsletter v0.1.1) are ported: each
     module runs on both engines, told which by its opener.
- **Where the cut stands (measured on the prod pod, 2026-11-21).** The registry holds 15,016 stores
  `moved` over 402 subsystems and 3 `failed`; the volume's 37 GB `orgs/`, `tasks/`, the `audit*.db`,
  `base`, `iam` and `commerce` files are dead copies of them. Not on hanzo-sql yet: 128 files (the 21
  `framework` files, 1.6 GB, and the two `lists`, ported and moved by the sweep; `tracker`, `trust`,
  `dataroom`, `captable` and `esign`, ported, which move when their own open runs; `data`, `crm`, `beta`,
  `global` and `allowance` are files no code opens, per a grep of the estate, so nothing moves); the embedded
  pubsub node's JetStream store (943 MB, served to insights as `kafka:9092`; `CLOUD_PUBSUB_URL` at the
  3-node `pubsub` StatefulSet retires it); and `git/` (22 GB of packs and refs). `transit/` is the sync
  app's bare-repo cache and stays an emptyDir. Scale out when none of those holds a store.
- **Stateless rollout.** Work that must run once (schedulers, reconcilers, the
  runner drain, sweeps) holds a lease; per-org write ownership is the
  database's, not an election. Then the Deployment drops its claim and runs 3
  replicas across nodes with a PodDisruptionBudget.
- **Exclusion across replicas: three mechanisms, and which one to reach for.** Anything
  correct only because one process runs is a bug with three.
  1. *Once-only work* (a loop that sweeps, meters, settles, reconciles, sends) holds
     `Coord.Lead(name)` and runs inside `.Do` (`sdk/coord/lead.go`: one lease in the
     deployment's object store, taken by compare-and-set, renewed, taken over a term after
     its holder stops). A sweep of stores asks `OrgStore.Owned(ns)` (`public.cloud_leases`).
     Marketing's drip sweep is this (`sweepDrip`); its `sweeping` mutex only keeps a
     sweep from overlapping the next in the process that holds the drip.
  2. *A per-key write lock* is the database's: `Dialect.BeginWrite` (one writing
     transaction per store, so per space or per chain where each is its own schema),
     `Dialect.Serialize(key)` (one per key in a shared store), both advisory
     transaction locks released with the transaction, so a replica that dies frees the
     key. A lock held across work that is not a statement on that connection (a call to
     Base, a read of another store) is taken through `dialect.Held`: the transaction
     gets a context of its own, because `database/sql` rolls a transaction back when its
     context ends and a request's would end the lock under work still running. **The
     work under a lock must use only the lock's transaction on that store**: a pool of
     four connections whose writers each hold one while waiting cannot also serve the
     holder a second, which is how a lock becomes a deadlock. The key lock of a
     store whose guarded work is not the lock's own (rail calls, Base, a Kubernetes
     apply) is `internal/lock`: `Keyed.Across(ctx, db, key)` queues in this process
     first, holding no connection, then takes `lock.Across(ctx, db, key)` (`Serialize`
     through `Held`), and a `Keyed` holds at most two keys against a database at once
     (`most`), which is what leaves the pool the connections the work under them needs.
     A caller that holds a key and takes another from the same `Keyed` can wait on
     itself at that cap: marketplace's attempt key is therefore `Hold` (this process
     only), the job key `Across`. On SQLite there is one process and the lock is the
     `Keyed`'s mutex. A migration does not run `CREATE INDEX IF NOT
     EXISTS` against a table that is there: it takes the table's writers' lock to find
     the index, and every replica opens its stores while the others write.
  3. *A shared head or counter* is a row or the last row of a table, read under that
     lock; the chain is the example (below).
- **Ported: the team docs store** (`docs`, opened by the team app; `apps/team/store.go`
  only, since `account_store.go` and `public.go` are still pod-local SQLite files). One
  store per (org, space) as before, a schema on hanzo-sql. A document write is a
  read-modify-write under the space's lock; `docStore.lock` returns a view of the store
  whose reads and writes of that space are the lock's transaction, so the document and
  the counters its write moves land together or not at all, and the socket's check of a
  tx and its application (`session.hold`) are one step on every replica. `writeLock`,
  `hold` and `released` report a lock that could not be had and a commit that did not
  land, and the write is then not made. The legacy work-facet rename is done in Go inside
  the migration (it was SQLite JSON1), numbers preserved.
- Supersedes the per-org S3-ship HA below for production.

## Encryption at rest: one key per database, derived

`github.com/hanzoai/cek` is the whole of it, and cloud owns none of it:

    cek.SetMaster(k)                        // once, at boot, from KMS (master.go does this)
    cek.Open(ns, subsystem, dir)            // everywhere else

The key is DERIVED — `HKDF(master, "hanzo/cek/v1/" + ns + "/" + subsystem)` — so it
is not generated, not wrapped, not stored and not rotated in place. There is no
unwrap step, no rewrap step, no per-file key material to lose, no sidecar beside the
database and no migration path to maintain. A database is born encrypted or it does
not exist. Losing the master loses the data, which is the property you want from
encryption at rest and the reason the master lives in KMS.

`namespace` decides WHERE, from the same two values: `{dir}/orgs/{slug}/{sub}.db`
for an org, `{dir}/orgs/_platform/{sub}.db` for the deployment's own. A caller
therefore passes a DIRECTORY and a NAME, never a path — the file and its key cannot
name different things.

**Do not set connection PRAGMAs.** `busy_timeout`, `journal_mode=WAL`,
`foreign_keys=ON` and `synchronous=NORMAL` are already applied by
`github.com/hanzoai/sqlite`, per CONNECTION — which a one-shot `db.Exec` was not, since
it lands on whichever connection serves it and is lost when that connection is recycled.
Roughly fifty stores each restated those by hand; they now say nothing, and
`sqlpool_test.go` asserts the driver still delivers them so the deletion cannot rot.
`cloud.OrgDB` is `cek.Open` plus `sqlpool.Single`, nothing more.

**A test binary keys itself.** cek reads no environment; a process with no KMS mints
its own master. `import _ "github.com/hanzoai/cloud/sdk/kit/master"` in one
_test.go of the package says so, once, instead of a TestMain per package.

**A store that is open has no file yet** on the pure-Go codec, so "does this org
have a store" is the union of the open set and the disk — `OrgStore.Has`, which
`Each` and `Stored` both go through.

**Still outside the envelope:** `tasks/<org>/<namespace>.db`. `hanzoai/tasks`'s
`EmbedConfig` has no key field, so that is an upstream change.

**One app calls another with a typed op — the same op the REST route, the OpenAPI
document, the MCP tool list and the CLI are all projected from.** `plane.go` is the
whole mechanism:

    cloud.Plane()                       the app internal ops are declared on
    cloud.ServePlane(name, log)         binds zip.SocketPath(name)
    cloud.Ask[In,Out](ctx, app, op, in) is the caller
    cloud.For(ctx, org)                 states the tenant for a BACKGROUND call
    cloud.As(c, org)                    delegates THIS request's principal
    cloud.Who(ctx)                      reads the principal a handler acts for

`zip.SocketPath(name)` resolves `{ZIP_RUNTIME_DIR}/<name>.sock` and BOTH halves use
it, so a server and its callers cannot disagree about where an app lives. Cloud points
`ZIP_RUNTIME_DIR` at its own data root (`bindRuntimeDir`) rather than keeping a second
rule about where sockets go.

**The plane is a SECOND app, and that is the point.** A typed op rides every transport
its app listens on, and the host proxies edge traffic to its children over a private
socket — so neither "is this HTTP?" nor "did this arrive on a socket?" separates an
internal call from a public request. The separation is structural instead: internal ops
are declared on the plane app, which listens on exactly one address and is never mounted
on the edge router. There is no path from the internet to a plane op, the same way there
is no path to a route that was never registered. It keeps every projection — the plane
has its own OpenAPI, MCP and CLI — without publishing the gate and the secret reads into
the public document.

**Identity rides the caller, and zip carries all nine headers.** Org, project, user,
name, email, owner, isAdmin, isOrgAdmin, request-id (zip v1.18.4 — it forwarded five,
and the four it dropped were the ones a callee decides on: a billing subject prefers the
minted name over the opaque id, and platform sudo is read off owner, never off
isOrgAdmin). A background job with no request to forward states its tenant once with
`cloud.For`; an inbound request always wins over what it stated, so a job can supply an
identity and can never launder one.

The answer a caller gets is therefore two facts, not one: **`ErrNoPeer` means this
deployment does not run that app** — the router said so from the manifest it owns, or
there is no router at all — **and it is the ONLY error that may be read as "fall back".
Every other failure is an outage.** Absence inferred from a failed call is a fail-open:
a payment rail concluding nothing is priced, or (v1.801.340, three days) `apps/billing`
concluding the fleet ran no commerce, handing the prepaid balance read to an HTTP proxy
that is unset in exactly that deployment, while ai's fail-CLOSED gate answered 503
`balance_unavailable` for every paid completion. Nothing logged the reason, because after
the line that dropped the error nothing had it. Pinned by `TestStaleSocketIsNotAPeer`,
`TestWakeStartsALazyAppBehindALeftoverSocket` and
`TestBalance_APlaneOutageIsNotASplitDeploy`.

The socket is also the coarse boundary: it is 0600 and `SO_PEERCRED`-authenticated, so a
peer is already one of our own processes. It is NOT a boundary between them — never read
a caller's org as an authorization decision on its own.

**What this replaced.** `rpc.go` + `dial.go` + `payloads.go` were a second, hand-written
implementation of exactly this: an fnv-hashed method registry, hand-packed ZAP payloads
with literal byte offsets, and a capability the callee parsed and nothing verified. It
was invisible to all five projections — `kms.get`, `finance.authorize` and `iam.mailable`
had no OpenAPI entry, no MCP tool, no CLI verb and no SDK. 1,300 lines, deleted.

**The body is ZAP, end to end** (zip v1.18.6, `internal/zapenc`). The layout is derived
from the In/Out type: fields take slots in declaration order, each aligned to its own
width, and NO NAME TRAVELS — a field is its offset. So the bytes on the socket are the
bytes in memory, exactly as the hand-written codecs did it, with the schema now held by
the type instead of by matching offsets in two files. Refusals cross as ZAP too, status
intact. JSON is the BOUNDARY encoding and stays on the REST routes a browser reaches and
in the MCP envelope an agent reads; it never appears between our own processes.
`TestPlaneWireCarriesNoFieldNames` pins it: the values cross, the field names do not.

Because the layout IS the type, the compatibility rule is structural: **append fields at
the end, and only at the end.** Reordering, inserting or retyping changes the wire for
every peer.

Appending is compatible in ONE direction. An old reader of a new message stops before the
tail and never sees the new fields. A new reader of an old message reads the new fields
from bytes that hold something else: an old commerce answering a new billing read
`client.Sale.BalanceCents` as 3689065128598336883, and the reverse read
`client.SaleIn.BalanceCents` as 7593401503053668712. So both ends of an op ship in one
image, and production runs one image, one replica, `Recreate`. A rollout that ran two
images side by side (rolling update, canary, a second replica on another tag) would need
a version on the message first.

## Two engines encrypt SQLite, and only one of them is production

`hanzoai/sqlite` ships two encryption paths, and confusing them will make you
condemn a healthy backup.

**The pure-Go envelope — for builds without cgo.** No writable codec hook exists
in modernc, so it decrypts the WHOLE file into a RAM copy (`/dev/shm`), lets
SQLite work on that, and re-encrypts on close. Because it cannot fold an
encrypted sidecar, `refuseHotSidecar` (envelope.go) fails closed on any
non-empty `-wal`. Its durability is at Close/Checkpoint, not per-commit.

The consequence that matters: **a tool built without cgo will refuse thousands of
perfectly healthy databases** with "the whole-file codec cannot fold an encrypted
sidecar". That is the wrong engine for the data, not a defect in the data.

So: any backup, restore, or inspection tool for this data must link libsqlcipher
via cgo. And a backup must copy the `{db, -wal, -shm}` triple together — the
`.db` alone is missing whatever the WAL holds. Waiting for the WALs to empty is
not a step; they do not empty.
