package sync

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// store.go is the universal sync service's own storage: one SQLite file per org
// ({DataDir}/orgs/{org}/sync.db via cloud.OrgStore), the same physical isolation
// every other subsystem uses, so a sync can only ever bind endpoints within the
// caller's own org.
//
// Three tables, and each is a fact the FORGE cannot hold:
//
//	sync     one bidirectional-sync intent — two endpoints, a direction, a
//	         trigger, and the engine's cursor.
//	outcome  what the last advance did to one (repo, ref, destination): when, and
//	         whether it left a conflict. The forge holds the refs and therefore
//	         knows what a repository IS; it has nowhere to record that an upstream
//	         tried to move one backwards and was refused. Without this, a
//	         divergence is a log line that scrolls away.
//	mirror   a repository's declared outbound targets. A sync's own row states
//	         one upstream; a repository may be replicated to several, and the
//	         GitHub-App import declares one without a sync row at all.
//
// # A repository is (account, name), in both tables
//
// A repository's name is only unique WITHIN the account it belongs to upstream:
// hanzoai/ai, hanzo-apps/ai and hanzo-docs/ai are three real repositories. Keyed
// by the bare name, all three share one row — one console status for three
// repositories, and one mirror row, so the second import to declare a target
// overwrites the first's and account A's refs are then pushed into account B's
// repository under the org's own credential. See [newRepo].

// errNotFound is returned when a sync lookup misses; handlers map it to HTTP 404.
var errNotFound = errors.New("sync: not found")

// Endpoint is one side of a sync. Connector is an OPTIONAL connectorruntime
// connector id (a connected platform); when set the engine can resolve the
// transport from the connector registry. Provider is the concrete integration
// ("github"|"gitlab"|"hanzo-git"); Locator is provider-specific (a clone URL, or a
// native repo name). Provider+Locator is always sufficient; a Connector is a
// convenience over raw URLs where a connection already exists.
type Endpoint struct {
	Connector string `json:"connector,omitempty"`
	Provider  string `json:"provider"`
	Locator   string `json:"locator"`
}

// Sync is one org's sync intent + engine cursor.
type Sync struct {
	ID        string
	Org       string
	Kind      string // "git" now; "storage" | "db" | ... later
	Source    Endpoint
	Target    Endpoint
	Direction string // both | pull | push | off
	Trigger   string // webhook | poll | manual
	Cursor    string // engine state: JSON map[position]fingerprint (git: branch→sha)
	Actor     string // the identity a reconcile writes AS — the loop-guard identity
	CreatedAt int64
	UpdatedAt int64 // bumped on every reconcile — the last-synced time
}

type store struct{ db *sql.DB }

func openStore(db *sql.DB) (*store, error) {
	s := &store{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *store) migrate() error {
	// The two ref tables gained the ACCOUNT the repository belongs to. A row
	// written before that names a repository we can no longer attribute — it is
	// the collision itself, written down — so the old shape is DROPPED rather
	// than carried forward under a guessed account. Both tables are re-derived:
	// an outcome by the next advance, a mirror by the import or the sync that
	// declares it. Keeping them would keep pushing account A's refs at account
	// B's repository, which is the whole reason the column is there.
	for _, t := range []string{"outcome", "mirror"} {
		if err := s.dropPreAccount(t); err != nil {
			return err
		}
	}
	const ddl = `
CREATE TABLE IF NOT EXISTS sync (
  id               TEXT PRIMARY KEY,
  org              TEXT NOT NULL,
  kind             TEXT NOT NULL,
  source_connector TEXT NOT NULL DEFAULT '',
  source_provider  TEXT NOT NULL,
  source_locator   TEXT NOT NULL,
  target_connector TEXT NOT NULL DEFAULT '',
  target_provider  TEXT NOT NULL,
  target_locator   TEXT NOT NULL,
  direction        TEXT NOT NULL DEFAULT 'both',
  trigger          TEXT NOT NULL DEFAULT 'webhook',
  cursor           TEXT NOT NULL DEFAULT '',
  actor            TEXT NOT NULL DEFAULT '',
  created_at       INTEGER NOT NULL,
  updated_at       INTEGER NOT NULL
);
-- One sync per (org, kind, source, target): a re-sync updates in place.
CREATE UNIQUE INDEX IF NOT EXISTS ux_sync ON sync(org, kind, source_locator, target_locator);
CREATE INDEX IF NOT EXISTS ix_sync_org ON sync(org, updated_at);
-- Resolution index: a webhook resolves by (org, kind, source_provider).
CREATE INDEX IF NOT EXISTS ix_sync_src ON sync(org, kind, source_provider);

-- What the last advance did to one ref, ON ONE DESTINATION. conflict is the
-- REASON, and empty means there is none — one column, so "resolved" is a write
-- of '' rather than a row that has to be found and deleted.
--
-- host is WHERE the ref was advanced to: '' is the forge, the canonical store,
-- and a hostname is a declared replica. Both directions are the same advance and
-- both can diverge, but they are not the same FACT — without this column a clean
-- inbound advance would clear a replica's unresolved divergence, and a replica's
-- success would clear the forge's. One row per destination, and neither can
-- speak for the other.
CREATE TABLE IF NOT EXISTS outcome (
  org      TEXT NOT NULL,
  account  TEXT NOT NULL,
  repo     TEXT NOT NULL,
  ref      TEXT NOT NULL,
  host     TEXT NOT NULL DEFAULT '',
  conflict TEXT NOT NULL DEFAULT '',
  at       INTEGER NOT NULL,
  PRIMARY KEY (org, account, repo, ref, host)
);
CREATE INDEX IF NOT EXISTS ix_outcome_repo ON outcome(org, account, repo);

-- A repository's declared outbound targets, one row per HOST: a second target
-- on a host we already push to is the same target, and the primary key says so
-- rather than a uniqueness check somewhere in the code.
CREATE TABLE IF NOT EXISTS mirror (
  org     TEXT NOT NULL,
  account TEXT NOT NULL,
  repo    TEXT NOT NULL,
  host    TEXT NOT NULL,
  url     TEXT NOT NULL,
  PRIMARY KEY (org, account, repo, host)
);
`
	if _, err := s.db.Exec(ddl); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	return nil
}

// dropPreAccount removes table if it is there WITHOUT an account column — the
// shape that could not tell two accounts' same-named repositories apart. A table
// that is absent, or already carries the column, is left alone, so this is a
// no-op on every boot after the first.
func (s *store) dropPreAccount(table string) error {
	var n int
	// The table name is a constant from the caller's own list, never an argument
	// from outside this file; the column name binds normally.
	if err := s.db.QueryRow(
		`SELECT count(*) FROM pragma_table_info(?) WHERE name='account'`, table).Scan(&n); err != nil {
		return fmt.Errorf("read %s columns: %w", table, err)
	}
	if n > 0 {
		return nil
	}
	if _, err := s.db.Exec(`DROP TABLE IF EXISTS ` + table); err != nil {
		return fmt.Errorf("drop pre-account %s: %w", table, err)
	}
	return nil
}

func (s *store) Close() error { return s.db.Close() }

const syncCols = `id,org,kind,source_connector,source_provider,source_locator,target_connector,target_provider,target_locator,direction,trigger,cursor,actor,created_at,updated_at`

func scanSync(sc interface{ Scan(...any) error }) (Sync, error) {
	var v Sync
	err := sc.Scan(&v.ID, &v.Org, &v.Kind,
		&v.Source.Connector, &v.Source.Provider, &v.Source.Locator,
		&v.Target.Connector, &v.Target.Provider, &v.Target.Locator,
		&v.Direction, &v.Trigger, &v.Cursor, &v.Actor, &v.CreatedAt, &v.UpdatedAt)
	return v, err
}

// Upsert inserts a sync, or updates the mutable fields of the existing row for the
// same (org, kind, source, target) in place — the id, cursor, and created_at are
// preserved so a re-sync never orphans a row or loses cursor state.
func (s *store) Upsert(ctx context.Context, v Sync) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO sync (`+syncCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		 ON CONFLICT(org,kind,source_locator,target_locator) DO UPDATE SET
		   source_connector=excluded.source_connector, source_provider=excluded.source_provider,
		   target_connector=excluded.target_connector, target_provider=excluded.target_provider,
		   direction=excluded.direction, trigger=excluded.trigger, actor=excluded.actor,
		   updated_at=excluded.updated_at`,
		v.ID, v.Org, v.Kind,
		v.Source.Connector, v.Source.Provider, v.Source.Locator,
		v.Target.Connector, v.Target.Provider, v.Target.Locator,
		v.Direction, v.Trigger, v.Cursor, v.Actor, v.CreatedAt, v.UpdatedAt)
	if err != nil {
		return fmt.Errorf("upsert sync: %w", err)
	}
	return nil
}

// Get returns the sync for (org, id) or errNotFound.
func (s *store) Get(ctx context.Context, org, id string) (Sync, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+syncCols+` FROM sync WHERE org=? AND id=?`, org, id)
	v, err := scanSync(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Sync{}, errNotFound
	}
	return v, err
}

// GetByEndpoints returns the sync for a specific (org, kind, source, target) or
// errNotFound — used to read back the row an Upsert wrote (its id may be pre-existing).
func (s *store) GetByEndpoints(ctx context.Context, org, kind, sourceLocator, targetLocator string) (Sync, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+syncCols+` FROM sync WHERE org=? AND kind=? AND source_locator=? AND target_locator=?`,
		org, kind, sourceLocator, targetLocator)
	v, err := scanSync(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Sync{}, errNotFound
	}
	return v, err
}

// List returns every sync for org, most-recently-updated first.
func (s *store) List(ctx context.Context, org string) ([]Sync, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+syncCols+` FROM sync WHERE org=? ORDER BY updated_at DESC, id ASC`, org)
	if err != nil {
		return nil, fmt.Errorf("list sync: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return collect(rows)
}

// ListAll returns EVERY sync in this store regardless of org — safe because a sync
// file is physically ONE org's (OrgDB isolation), so this IS that org's full set. The
// scheduler's discovery read uses it: it opens a store by its on-disk slug path (no org
// in hand) and reads the org off each returned row.
func (s *store) ListAll(ctx context.Context) ([]Sync, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+syncCols+` FROM sync ORDER BY id ASC`)
	if err != nil {
		return nil, fmt.Errorf("list all sync: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return collect(rows)
}

// ResolveBySource returns the org's syncs of kind whose SOURCE provider matches —
// the webhook resolution set (the caller further filters by locator/repo). One
// query, index-backed.
func (s *store) ResolveBySource(ctx context.Context, org, kind, sourceProvider string) ([]Sync, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+syncCols+` FROM sync WHERE org=? AND kind=? AND source_provider=?`,
		org, kind, sourceProvider)
	if err != nil {
		return nil, fmt.Errorf("resolve sync: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return collect(rows)
}

// ListBySourceLocator returns the org's syncs (any kind) whose SOURCE locator equals
// locator — the CHAIN lookup: a sync whose target fed this locator finds the next
// hop's syncs here.
func (s *store) ListBySourceLocator(ctx context.Context, org, locator string) ([]Sync, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+syncCols+` FROM sync WHERE org=? AND source_locator=?`, org, locator)
	if err != nil {
		return nil, fmt.Errorf("chain lookup: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return collect(rows)
}

// collect scans a sync result set to a slice.
func collect(rows *sql.Rows) ([]Sync, error) {
	var out []Sync
	for rows.Next() {
		v, err := scanSync(rows)
		if err != nil {
			return nil, fmt.Errorf("scan sync: %w", err)
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// SetCursor records the engine's post-reconcile cursor and bumps updated_at (the
// last-synced time) for a sync.
func (s *store) SetCursor(ctx context.Context, org, id, cursor string, at int64) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE sync SET cursor=?, updated_at=? WHERE org=? AND id=?`, cursor, at, org, id)
	if err != nil {
		return fmt.Errorf("set cursor: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errNotFound
	}
	return nil
}

// Delete removes the sync (org, id). Reports whether a row went.
func (s *store) Delete(ctx context.Context, org, id string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM sync WHERE org=? AND id=?`, org, id)
	if err != nil {
		return false, fmt.Errorf("delete sync: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ── outcome: what the last advance did ───────────────────────────────────────

// rollup is one repository's refs folded into one answer: whether any of them is
// in conflict, and when the most recent advance of any of them happened. It is
// what the console renders per repository, and the forge cannot answer either
// half — it holds refs, not the history of what we tried to do to them.
type rollup struct {
	Conflict bool
	At       int64
}

// Record writes the outcome of one advance of r's ref TO host ("" = the forge).
// conflict is the reason a divergence was refused, or "" when the ref is in step
// — so recording success and clearing a past conflict are the SAME write, and
// there is no way to do one without the other.
func (s *store) Record(ctx context.Context, org string, r repo, ref, host, conflict string, at int64) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO outcome (org,account,repo,ref,host,conflict,at) VALUES (?,?,?,?,?,?,?)
		 ON CONFLICT(org,account,repo,ref,host) DO UPDATE SET conflict=excluded.conflict, at=excluded.at`,
		org, r.account, r.name, ref, host, conflict, at)
	if err != nil {
		return fmt.Errorf("record outcome: %w", err)
	}
	return nil
}

// States rolls every repository's refs up to one row each, for the whole org.
//
// One query rather than one per name: the file IS this org's, the console asks
// about every repository it can see at once, and a per-name read would be a
// query per repository on a page load.
//
// Every destination folds into the SAME roll-up: a repository whose replica has
// diverged is not in step, and that is the one word the console renders.
func (s *store) States(ctx context.Context, org string) (map[repo]rollup, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT account, repo, MAX(at), MAX(CASE WHEN conflict<>'' THEN 1 ELSE 0 END)
		   FROM outcome WHERE org=? GROUP BY account, repo`, org)
	if err != nil {
		return nil, fmt.Errorf("read outcomes: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[repo]rollup{}
	for rows.Next() {
		var r repo
		var at int64
		var bad int
		if err := rows.Scan(&r.account, &r.name, &at, &bad); err != nil {
			return nil, fmt.Errorf("scan outcome: %w", err)
		}
		out[r] = rollup{Conflict: bad == 1, At: at}
	}
	return out, rows.Err()
}

// ── mirror: where a repository is replicated to ──────────────────────────────

// SetMirror declares an outbound target. Keyed by HOST, so re-declaring the same
// host updates the address rather than accumulating a second target to it — and
// by ACCOUNT, so two same-named repositories from different accounts each keep
// their own target instead of the second one taking over the first's.
func (s *store) SetMirror(ctx context.Context, org string, r repo, host, url string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO mirror (org,account,repo,host,url) VALUES (?,?,?,?,?)
		 ON CONFLICT(org,account,repo,host) DO UPDATE SET url=excluded.url`,
		org, r.account, r.name, host, url)
	if err != nil {
		return fmt.Errorf("declare mirror: %w", err)
	}
	return nil
}

// DropMirror removes the target on host. Removing one that is not there is not
// an error — the caller asked for a state, and that state already holds.
func (s *store) DropMirror(ctx context.Context, org string, r repo, host string) error {
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM mirror WHERE org=? AND account=? AND repo=? AND host=?`,
		org, r.account, r.name, host); err != nil {
		return fmt.Errorf("remove mirror: %w", err)
	}
	return nil
}

// Mirrors lists a repository's declared outbound target URLs.
func (s *store) Mirrors(ctx context.Context, org string, r repo) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT url FROM mirror WHERE org=? AND account=? AND repo=? ORDER BY host ASC`,
		org, r.account, r.name)
	if err != nil {
		return nil, fmt.Errorf("list mirrors: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			return nil, fmt.Errorf("scan mirror: %w", err)
		}
		out = append(out, u)
	}
	return out, rows.Err()
}
