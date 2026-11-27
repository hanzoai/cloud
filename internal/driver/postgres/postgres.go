package postgres

// postgres.go — a store on the one shared PostgreSQL (hanzo-sql) instead of a file.
//
// With CLOUD_SQL_DSN set, [Org] returns a pool on that server whose connections
// resolve every unqualified table to ONE schema: the store's. A store is a
// namespace x subsystem, exactly what named a file, so the isolation a tenant had
// from owning its own file it has from owning its own schema. The schema is named
// from a digest of that pair, which is why a name nobody typed can never be
// another tenant's, and it is listed in a registry table so a sweep can ask which
// stores exist without a directory to walk.
//
// NOTHING OF THE FILE PLANE APPLIES HERE. There is no path, no key sidecar, no
// object-store ship and no writer election: the database is the one writer and the
// one copy. OrgStore reads [DSN] once and leaves every one of those paths unvisited.
//
// A server's pools are small and give their connections back when idle. OrgStore
// already bounds how many stores it keeps open, and a closed store closes its pool,
// so the connections a process holds follow the tenants that are active in it and
// not the tenants it has ever served.

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base32"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/hanzoai/cloud/sdk/coord"
	"github.com/hanzoai/cloud/sdk/dialect"
	"github.com/hanzoai/cloud/sdk/env"
	"github.com/hanzoai/namespace"
)

// The SDK's dialect tells the engines apart by driver, and links no server driver
// of its own: this is the one that opens server handles, so it names itself.
func init() { dialect.Server(stdlib.GetDefaultDriver()) }

// The shape of one store's pool: enough connections for concurrent readers and one
// writer's transaction, one kept warm, and none held past half a minute idle.
const (
	poolMax     = 4
	poolIdle    = 1
	poolIdleFor = 30 * time.Second
	poolLife    = 30 * time.Minute
)

// registry lists every schema [Org] has made, so a sweep enumerates stores by
// querying instead of walking a disk. It lives in public: no store's search_path
// reaches it, so no store's SQL can.
const registry = "public.cloud_schemas"

// leases and bound are the registry's neighbours: who may act for a store now, and
// which deployment the database belongs to. Neither is on any store's search_path.
const (
	leases = "public.cloud_leases"
	bound  = "public.cloud_deployment"
)

// lockProvision names the lock that serialises the DDL that makes the registry itself.
// A schema takes a lock named for the schema (see ensure): two replicas opening the
// same store at once would otherwise race CREATE SCHEMA IF NOT EXISTS into a unique
// violation in the catalog, since the statement is not safe against itself, but two
// different stores have nothing to race and are made side by side.
const lockProvision = "provision"

// Schema is the PostgreSQL schema the store (ns, subsystem) lives in: "s_" and the
// lowercase base32 of a SHA-256 over its file key and subsystem. The digest is 52
// characters, so the name is 54 bytes and fits PostgreSQL's 63-byte identifier
// limit whole; nothing is truncated, so two stores cannot share a prefix.
//
// The key is namespace.Key, the rendering a file path comes from, so a namespace
// that has no file (a user, a repo) has no schema, and one that has names the same
// store on both engines.
func Schema(ns namespace.Namespace, subsystem string) (string, error) {
	key, err := namespace.Key(ns, subsystem)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(key + "\x00" + subsystem))
	enc := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(sum[:])
	return "s_" + strings.ToLower(enc), nil
}

// server is one hanzo-sql this process has proven it may use: it connects as a role
// that is not a superuser, it is bound to this deployment, and the registry is there.
type server struct {
	cfg    *pgx.ConnConfig
	admin  *sql.DB       // provisioning, leases and registry reads; idle-closing like every pool here
	made   sync.Map      // schema -> struct{}: provisioned by this process, so the round trip is paid once
	moved  sync.Map      // schema -> struct{}: registered as moved, which never reverses while this process lives
	inMove sync.Map      // schema -> struct{}: moving in this process right now
	mover  chan struct{} // 1-slot gate: at most one move runs in this process at a time
	locks  lockPool
}

// entry is what the process knows of one DSN: the server once it has been proven, or
// the last failure to prove it and when, and the dial in flight if there is one.
type entry struct {
	mu      sync.Mutex
	s       *server
	err     error
	failed  time.Time
	dialing chan struct{} // closed when the dial in flight ends; nil when none
}

var (
	entriesMu sync.Mutex
	entries   = map[string]*entry{}
)

// failureMemory is how long a failed dial is answered from memory. A variable so this
// package's own tests can shorten it.
var failureMemory = 5 * time.Second

// dialTimeout bounds one attempt to reach a server. It is short on purpose: every
// caller of a server that does not answer waits for it, and a blackholed address would
// otherwise stall each of them for as long as the OS lets a connect hang. What is done
// once the server answers has opTimeout. A variable so this package's own tests can
// shorten it.
var dialTimeout = 3 * time.Second

// DialTimeoutEnv lengthens that bound, as a Go duration ("30s"), for a server known to
// be local and busy rather than gone. The throwaway server the tests run against shares
// its box with every package under test, and under that load it stops answering for
// seconds at a time: in CI runs 17680 and 17683 every connect on the box timed out in
// the same second, and five packages failed with it. sqltest sets it for the postgres
// leg of a test, never process-wide, so the tests here that time a dial to a server
// that never speaks still measure dialTimeout. Unset, empty, unparseable or not
// positive, the bound is dialTimeout.
const DialTimeoutEnv = "CLOUD_SQL_DIAL_TIMEOUT"

// dialBound is the bound one dial waits for the server to answer.
func dialBound() time.Duration {
	if d, err := time.ParseDuration(env.Or(DialTimeoutEnv, "")); err == nil && d > 0 {
		return d
	}
	return dialTimeout
}

// opTimeout bounds one statement of the seam's own: making a schema, asking for a
// lease. These run on a server already proven, and are allowed longer than a dial.
const opTimeout = 10 * time.Second

// serverOf is the proven server for dsn. It is single-flight: one caller dials, and
// the callers who arrive while it does are not queued behind it. A server this
// process has failed to reach is answered with that failure at once, while the one
// caller whose turn it is to look again does so; only a caller with no history at all
// waits for the first dial, and no longer than it takes to reach the server.
func serverOf(dsn string) (*server, error) {
	entriesMu.Lock()
	e := entries[dsn]
	if e == nil {
		e = &entry{}
		entries[dsn] = e
	}
	entriesMu.Unlock()

	e.mu.Lock()
	if e.s != nil {
		s := e.s
		e.mu.Unlock()
		return s, nil
	}
	if e.dialing != nil {
		if e.err != nil {
			err := e.err
			e.mu.Unlock()
			return nil, err
		}
		done := e.dialing
		e.mu.Unlock()
		<-done
		e.mu.Lock()
		s, err := e.s, e.err
		e.mu.Unlock()
		return s, err
	}
	if e.err != nil && time.Since(e.failed) < failureMemory {
		err := e.err
		e.mu.Unlock()
		return nil, err
	}
	done := make(chan struct{})
	e.dialing = done
	e.mu.Unlock()

	s, err := dial(dsn)

	e.mu.Lock()
	e.dialing = nil
	if err != nil {
		e.err, e.failed = err, time.Now()
	} else {
		e.s, e.err = s, nil
	}
	e.mu.Unlock()
	close(done)
	return s, err
}

func dial(dsn string) (*server, error) {
	u, err := url.Parse(dsn)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || (u.Host == "" && u.Query().Get("host") == "") {
		return nil, fmt.Errorf("%s must be a postgres:// URL naming a server", Env)
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("%s is not a postgres DSN: %w", Env, err)
	}
	if err := requireTLS(cfg); err != nil {
		return nil, err
	}
	if env.Or("CLOUD_ENV", "") == "" {
		return nil, errors.New("CLOUD_ENV is not set: a database is bound to the deployment that first opens it, and one bound to an unnamed environment is bound to every copy of this manifest")
	}
	s := &server{cfg: cfg, admin: pool(cfg, ""), mover: make(chan struct{}, 1)}
	// Reaching the server is the part that can hang on a blackholed address, and it has
	// the short bound; what is done once it answers is a few statements, and a server
	// that is slow because it is busy is given the time to do them.
	reach, cancel := context.WithTimeout(context.Background(), dialBound())
	err = s.admin.PingContext(reach)
	cancel()
	if err != nil {
		_ = s.admin.Close()
		return nil, fmt.Errorf("reach hanzo-sql %s: %w", u.Redacted(), err)
	}
	if err := s.prove(u.Redacted()); err != nil {
		_ = s.admin.Close()
		return nil, err
	}
	return s, nil
}

// requireTLS refuses a server that is not on this host unless every way of reaching
// it, as pgx has parsed the DSN, insists on TLS. The default sslmode is prefer, which
// falls back to plaintext without a word, and what crosses this connection is every
// tenant's rows and the money.
//
// It reads the parsed configuration and not the text of the URL. A DSN that says
// sslmode twice is read by pgx by its LAST word and by a reader of the query string by
// its first, and `?sslmode=require&sslmode=disable` passed a check of the text and
// went out in the clear. Whatever pgx will do is what is checked: the first attempt
// and every fallback it would make must have a TLS configuration.
func requireTLS(cfg *pgx.ConnConfig) error {
	type hop struct {
		host string
		tls  bool
	}
	hops := []hop{{cfg.Host, cfg.TLSConfig != nil}}
	for _, f := range cfg.Fallbacks {
		hops = append(hops, hop{f.Host, f.TLSConfig != nil})
	}
	for _, h := range hops {
		if !local(h.host) && !h.tls {
			return fmt.Errorf("%s names %s without sslmode=require: tenant data and money would cross the network in the clear. Turn ssl on at that server and say sslmode=require (encrypts), or sslmode=verify-full with the server's CA (encrypts and proves the server is the one named)", Env, h.host)
		}
	}
	return nil
}

// local reports whether a host is this machine: a unix socket directory, localhost
// or a loopback address.
func local(host string) bool {
	if host == "" || host == "localhost" || strings.HasPrefix(host, "/") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// pool is a database/sql pool on cfg, shaped as above and drawing on the process's
// connection budget. schema, when set, is checked on every connection it dials.
func pool(cfg *pgx.ConnConfig, schema string) *sql.DB {
	db := sql.OpenDB(connector{inner: stdlib.GetConnector(*cfg), lim: limit(), schema: schema})
	db.SetMaxOpenConns(poolMax)
	db.SetMaxIdleConns(poolIdle)
	db.SetConnMaxIdleTime(poolIdleFor)
	db.SetConnMaxLifetime(poolLife)
	return db
}

// deployment is who this process is, as the database records it: the id the pods of a
// deployment already agree on for their shared key-value store (namespace, brand and
// environment), so two deployments that cannot share a key cannot share a database.
func deployment() string { return coord.Deployment() }

// prove connects, refuses a superuser role, binds the database to this deployment
// and makes the registry and the lease table.
//
// The role must not be a superuser. Every store in the process connects as it, and
// a superuser reaches the whole server: other services' databases, its files
// through pg_read_file, its filesystem through COPY ... TO PROGRAM. The same rule
// apps/base holds for the Base placement, for the same reason.
//
// THE DATABASE IS BOUND TO ONE DEPLOYMENT. The first process to prove it writes who it
// is, and a process that is somebody else refuses to start. A testnet pointed at
// mainnet's DSN by a copied manifest would otherwise open mainnet's books and write
// into them; the schemas are named by tenant and not by deployment, so nothing else
// would tell the two apart.
func (s *server) prove(where string) error {
	ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
	defer cancel()
	var super bool
	if err := s.admin.QueryRowContext(ctx, `SELECT rolsuper FROM pg_roles WHERE rolname = current_user`).Scan(&super); err != nil {
		return fmt.Errorf("reach hanzo-sql %s: %w", where, err)
	}
	if super {
		return fmt.Errorf("%s connects as a superuser: every store runs as this role, so it must be one that may create schemas in its database and nothing more", Env)
	}
	return s.locked(ctx, lockProvision, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS `+registry+` (
  schema    TEXT PRIMARY KEY,
  namespace TEXT NOT NULL,
  subsystem TEXT NOT NULL,
  created   BIGINT NOT NULL,
  state     TEXT NOT NULL DEFAULT 'moved',
  moving    BIGINT NOT NULL DEFAULT 0,
  moved     BIGINT NOT NULL DEFAULT 0,
  failed    BIGINT NOT NULL DEFAULT 0,
  error     TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS cloud_schemas_subsystem ON `+registry+` (subsystem);
CREATE TABLE IF NOT EXISTS `+leases+` (
  schema  TEXT PRIMARY KEY,
  holder  TEXT NOT NULL,
  expires BIGINT NOT NULL
);
CREATE TABLE IF NOT EXISTS `+bound+` (
  singleton BOOLEAN PRIMARY KEY DEFAULT true CHECK (singleton),
  identity  TEXT NOT NULL
)`); err != nil {
			return err
		}
		if err := registryState(ctx, tx); err != nil {
			return err
		}
		// Leases of holders that are gone lapse but are not deleted; a day past their end
		// they are rows nothing will ever read.
		if _, err := tx.ExecContext(ctx, `DELETE FROM `+leases+` WHERE expires < extract(epoch FROM now())::bigint - 86400`); err != nil {
			return err
		}
		me := deployment()
		if _, err := tx.ExecContext(ctx, `INSERT INTO `+bound+` (singleton, identity) VALUES (true, $1) ON CONFLICT (singleton) DO NOTHING`, me); err != nil {
			return err
		}
		var owner string
		if err := tx.QueryRowContext(ctx, `SELECT identity FROM `+bound).Scan(&owner); err != nil {
			return err
		}
		if owner != me {
			return fmt.Errorf("this hanzo-sql belongs to deployment %q and this process is %q: refusing to open another deployment's stores (to move the database deliberately, DELETE FROM %s)", owner, me, bound)
		}
		return nil
	})
}

// locked runs fn in a transaction holding the advisory lock named name.
func (s *server) locked(ctx context.Context, name string, fn func(*sql.Tx) error) error {
	tx, err := s.admin.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, name); err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// ensure makes the schema and its registry row, once per process.
func (s *server) ensure(schema string, ns namespace.Namespace, subsystem string) error {
	if _, ok := s.made.Load(schema); ok {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
	defer cancel()
	// schema is s_ and base32 from Schema, so it is a safe identifier to quote.
	if err := s.locked(ctx, schema, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `CREATE SCHEMA IF NOT EXISTS "`+schema+`"`); err != nil {
			return err
		}
		// A store made here is born on hanzo-sql, so its row says moved. A store that
		// came from a file is registered by Move, as moving, before this is ever asked.
		_, err := tx.ExecContext(ctx,
			`INSERT INTO `+registry+` (schema, namespace, subsystem, created, moved) VALUES ($1, $2, $3, $4, $4)
			 ON CONFLICT (schema) DO NOTHING`,
			schema, ns.String(), subsystem, time.Now().Unix())
		return err
	}); err != nil {
		return err
	}
	s.made.Store(schema, struct{}{})
	return nil
}

func onServer(dsn string, ns namespace.Namespace, subsystem string) (*sql.DB, error) {
	schema, err := Schema(ns, subsystem)
	if err != nil {
		return nil, err
	}
	s, err := serverOf(dsn)
	if err != nil {
		return nil, err
	}
	if err := s.ensure(schema, ns, subsystem); err != nil {
		return nil, fmt.Errorf("make the schema for %s/%s: %w", ns, subsystem, err)
	}
	return s.pool(schema), nil
}

// lockMax is how many connections the process's lock pool holds at once. A lock is held
// by a connection from its grant to its release, so this is how many locks the process
// holds against the server at a time; the rest queue in the callers (lock.Keyed holds
// at most two keys each).
const lockMax = 8

// lockPool is the process's one pool for the locks that have no store to be held on.
type lockPool struct {
	once sync.Once
	db   *sql.DB
}

// Locks is the pool a lock is held on when its caller has no store of its own: a
// package that guards a record in Base, or in a datastore that is not the seam's, has
// no connection to hold an advisory lock with. Advisory locks belong to the database
// and not to a schema, so every replica that takes the same key on this pool is
// kept apart from every other, whatever store the work under it touches.
//
// It is one pool for the process, apart from the administrative pool: a lock holds
// its connection for as long as its work lasts, and the administrative pool serves
// leases and registry reads that must not queue behind it. It draws on the process's
// connection budget like any pool and idles down the same way.
//
// With no DSN there is one process, a lock is its mutex, and Locks answers nil. An
// error means hanzo-sql is named and was not reached, and the lock cannot be had.
func Locks() (*sql.DB, error) {
	dsn := DSN()
	if dsn == "" {
		return nil, nil
	}
	s, err := serverOf(dsn)
	if err != nil {
		return nil, err
	}
	s.locks.once.Do(func() {
		s.locks.db = pool(s.cfg, "")
		s.locks.db.SetMaxOpenConns(lockMax)
	})
	return s.locks.db, nil
}

// pool is a pool whose connections resolve every table to schema.
func (s *server) pool(schema string) *sql.DB {
	cfg := s.cfg.Copy()
	cfg.RuntimeParams["search_path"] = schema
	return pool(cfg, schema)
}

// described is a pool on the schema whose statements are described where they are
// run, which is what an engine that changes its tables as it runs needs: a statement
// cached under the shape a table had before an ALTER fails against the shape it has
// now. It is the one difference from [server.pool], and it costs a round trip a
// statement, so only such an engine takes it.
func (s *server) described(schema string) *sql.DB {
	cfg := s.cfg.Copy()
	cfg.RuntimeParams["search_path"] = schema
	cfg.DefaultQueryExecMode = pgx.QueryExecModeDescribeExec
	return pool(cfg, schema)
}

// Described opens the schema of the store (ns, subsystem) on hanzo-sql for an engine
// that opens its own pools and changes its own tables: a pool of its own on each call,
// on the process's connection budget, in the schema the registry names, described as
// [server.described] says. The schema is made and registered as born there; whether
// the store should be on hanzo-sql at all is [WhereAt]'s to say, and a store still a
// file is moved by [MoveFile], not opened here.
func Described(ns namespace.Namespace, subsystem string) (*sql.DB, error) {
	dsn := DSN()
	if dsn == "" {
		return nil, fmt.Errorf("postgres: %s is not set", Env)
	}
	schema, err := Schema(ns, subsystem)
	if err != nil {
		return nil, err
	}
	s, err := serverOf(dsn)
	if err != nil {
		return nil, err
	}
	if err := s.ensure(schema, ns, subsystem); err != nil {
		return nil, fmt.Errorf("make the schema for %s/%s: %w", ns, subsystem, err)
	}
	return s.described(schema), nil
}

// Stores lists the namespaces, as the text namespace.String renders, that have a
// store for subsystem on hanzo-sql: the sweep's answer to "which entities have one",
// read from the registry. A store that is still a file, or is being moved, is not one. They are text and not namespaces because a namespace is
// only ever built in orgns.go (OrgOnServer reads one back).
func Stores(subsystem string) ([]string, error) {
	s, err := serverOf(DSN())
	if err != nil {
		return nil, err
	}
	rows, err := s.admin.Query(`SELECT namespace FROM `+registry+` WHERE subsystem = $1 AND state = 'moved' ORDER BY namespace`, subsystem)
	if err != nil {
		return nil, fmt.Errorf("list %s stores: %w", subsystem, err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var text string
		if err := rows.Scan(&text); err != nil {
			return nil, err
		}
		out = append(out, text)
	}
	return out, rows.Err()
}

// Subsystems lists the subsystems ns has a store for on hanzo-sql, sorted: the answer
// to "which stores does this entity keep", read from the registry. It is how a family
// of stores that share a name's prefix (the audit chains) is enumerated without a
// directory to glob.
func Subsystems(ns namespace.Namespace) ([]string, error) {
	s, err := serverOf(DSN())
	if err != nil {
		return nil, err
	}
	rows, err := s.admin.Query(`SELECT subsystem FROM `+registry+` WHERE namespace = $1 ORDER BY subsystem`, ns.String())
	if err != nil {
		return nil, fmt.Errorf("list the stores of %s: %w", ns, err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	return out, rows.Err()
}

// Exists reports whether the store (ns, subsystem) has been made on hanzo-sql,
// without making it: the question a read addressed by a stranger asks.
func Exists(ns namespace.Namespace, subsystem string) (bool, error) {
	schema, err := Schema(ns, subsystem)
	if err != nil {
		return false, err
	}
	s, err := serverOf(DSN())
	if err != nil {
		return false, err
	}
	if _, ok := s.made.Load(schema); ok {
		return true, nil
	}
	var one int
	err = s.admin.QueryRow(`SELECT 1 FROM `+registry+` WHERE schema = $1 AND state = 'moved'`, schema).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// Lease claims, or renews, holder's lease on the store (ns, subsystem) for ttl, and
// reports whether holder has it. A lease held by another holder is not taken until it
// has expired, so at most one replica acts for a store at a time, and one that stops
// asking loses it within ttl. Time is the database's, not either replica's clock.
//
// A denial says how long the holder has left (retry), so the caller can stop asking
// until then. The ask is a plain SELECT first: an INSERT ... ON CONFLICT locks the
// row it conflicts with and burns a transaction id even when its WHERE refuses it,
// and a replica that does not own a store asks about every one it sweeps. The write
// is only made when the lease is free, expired, or already this holder's.
func Lease(ns namespace.Namespace, subsystem, holder string, ttl time.Duration) (held bool, retry time.Duration, err error) {
	schema, err := Schema(ns, subsystem)
	if err != nil {
		return false, 0, err
	}
	s, err := serverOf(DSN())
	if err != nil {
		return false, 0, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
	defer cancel()
	var owner string
	var left int64
	err = s.admin.QueryRowContext(ctx, `SELECT holder, expires - extract(epoch FROM now())::bigint FROM `+leases+` WHERE schema = $1`, schema).Scan(&owner, &left)
	switch {
	case err == nil && owner != holder && left > 0:
		return false, time.Duration(left) * time.Second, nil
	case err != nil && !errors.Is(err, sql.ErrNoRows):
		return false, 0, err
	}
	var expires int64
	err = s.admin.QueryRowContext(ctx, `INSERT INTO `+leases+` AS l (schema, holder, expires)
		VALUES ($1, $2, extract(epoch FROM now())::bigint + $3)
		ON CONFLICT (schema) DO UPDATE SET holder = excluded.holder, expires = excluded.expires
		WHERE l.holder = excluded.holder OR l.expires < extract(epoch FROM now())::bigint
		RETURNING expires`, schema, holder, int64(ttl/time.Second)).Scan(&expires)
	if errors.Is(err, sql.ErrNoRows) { // another holder took it between the look and the write
		return false, time.Second, nil
	}
	return err == nil, 0, err
}

// unleaseWithin bounds how long giving up leases may take, dialling included. It runs
// on the way out of a process, and a database that does not answer must not hold the
// shutdown: the leases lapse by themselves within their term.
const unleaseWithin = 2 * time.Second

// Unlease gives up every lease holder has, so a replica that is shutting down hands
// its stores over now and not at the end of the term.
func Unlease(holder string) error {
	done := make(chan error, 1)
	go func() {
		s, err := serverOf(DSN())
		if err != nil {
			done <- err
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), unleaseWithin)
		defer cancel()
		_, err = s.admin.ExecContext(ctx, `DELETE FROM `+leases+` WHERE holder = $1`, holder)
		done <- err
	}()
	select {
	case err := <-done:
		return err
	case <-time.After(unleaseWithin):
		return errors.New("postgres: the leases were not given up within " + unleaseWithin.String() + "; they lapse by themselves")
	}
}

// registryState adds the columns that say where a store lives to a registry made before
// they existed. Every row such a registry holds was made on hanzo-sql, so the default
// of the state column, moved, is the truth about them. It asks before it alters:
// ALTER TABLE locks out the table's readers even to find the column already there, and
// every process proves the server once.
func registryState(ctx context.Context, tx *sql.Tx) error {
	var have int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.columns
		WHERE table_schema = 'public' AND table_name = 'cloud_schemas' AND column_name = 'state'`).Scan(&have); err != nil || have > 0 {
		return err
	}
	_, err := tx.ExecContext(ctx, `ALTER TABLE `+registry+`
  ADD COLUMN state  TEXT   NOT NULL DEFAULT 'moved',
  ADD COLUMN moving BIGINT NOT NULL DEFAULT 0,
  ADD COLUMN moved  BIGINT NOT NULL DEFAULT 0,
  ADD COLUMN failed BIGINT NOT NULL DEFAULT 0,
  ADD COLUMN error  TEXT   NOT NULL DEFAULT ''`)
	return err
}
