package source

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/hanzoai/cloud/sdk/dialect"
)

// Store is one org's linked-source registry: a per-org SQLite file (HIP-0302),
// opened once by cloud.OrgStore. Two tables and no secret column: `sources`
// holds the device's PUBLIC key and what it reported about itself, and
// `source_providers` holds the state of each provider instance the source
// reaches. A provider login or key never reaches either.
//
// Every statement is predicated on `subject`, the validated person. The file is
// the org boundary and the subject is the person boundary inside it; there is no
// statement that reads by org alone, so there is no org-admin or SuperAdmin read.
type Store struct {
	db *sql.DB
	d  dialect.Dialect
}

var (
	errKind     = errors.New("source: kind cannot change after link")
	errNotFound = errors.New("source: not found")
	errRevoked  = errors.New("source: revoked")
	errCap      = errors.New("source: too many linked sources")
	errLinkRate = errors.New("source: too many links this hour")
	errTooSoon  = errors.New("source: heartbeat too soon")
	errStale    = errors.New("source: heartbeat not newer than the last")
)

const (
	// maxActive is the cap on a person's non-revoked sources.
	maxActive = 25
	// maxLinksPerHour bounds new links per person; relinking the same install is
	// not a new link.
	maxLinksPerHour = 10
	// minBeat is the least gap between two heartbeats of one source.
	minBeat = 10 * time.Second
)

// row is one linked install. Key is its public device key.
type row struct {
	ID        string
	Kind      string // desktop | browser; fixed at link
	Subject   string
	Name      string
	OS        string
	Arch      string
	Version   string
	Key       string
	Zap       string
	LastSeen  int64 // unix seconds of the last accepted heartbeat; 0 = never
	BeatAt    int64 // the `at` of the last accepted heartbeat, unix seconds
	LinkedAt  int64
	RevokedAt int64 // 0 = active
	Providers []prov
}

// prov is the reported state of one provider instance on a source.
type prov struct {
	Provider string
	Instance string
	Kind     string
	State    string
	Plan     string
	Shared   bool
}

// beat is what a heartbeat changes.
type beat struct {
	At        int64
	Version   string
	Zap       string
	Providers []prov
}

func openStore(db *sql.DB) (*Store, error) {
	s := &Store{db: db, d: dialect.Of(db)}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	err := s.d.Migrate(ctx, db, func(tx *sql.Tx) error {
		for _, ddl := range []string{`
CREATE TABLE IF NOT EXISTS sources (
  owner      TEXT NOT NULL,
  subject    TEXT NOT NULL,
  kind       TEXT NOT NULL,
  id         TEXT NOT NULL PRIMARY KEY,
  name       TEXT NOT NULL,
  os         TEXT NOT NULL,
  arch       TEXT NOT NULL,
  version    TEXT NOT NULL,
  key        TEXT NOT NULL,
  zap        TEXT NOT NULL DEFAULT '',
  last_seen  BIGINT NOT NULL DEFAULT 0,
  beat_at    BIGINT NOT NULL DEFAULT 0,
  linked_at  BIGINT NOT NULL,
  revoked_at BIGINT NOT NULL DEFAULT 0
)`, `
CREATE INDEX IF NOT EXISTS ix_sources_subject ON sources(subject, last_seen)`, `
CREATE TABLE IF NOT EXISTS source_providers (
  id       TEXT NOT NULL,
  provider TEXT NOT NULL,
  instance TEXT NOT NULL,
  kind     TEXT NOT NULL,
  state    TEXT NOT NULL,
  plan     TEXT NOT NULL DEFAULT '',
  shared   BIGINT NOT NULL DEFAULT 0,
  PRIMARY KEY (id, instance)
)`, `
CREATE TABLE IF NOT EXISTS source_cost (
  owner    TEXT NOT NULL,
  subject  TEXT NOT NULL,
  source   TEXT NOT NULL,
  day      TEXT NOT NULL,
  provider TEXT NOT NULL,
  model    TEXT NOT NULL,
  input    BIGINT NOT NULL,
  output   BIGINT NOT NULL,
  cache    BIGINT NOT NULL,
  cost_usd DOUBLE PRECISION,
  PRIMARY KEY (source, day, provider, model)
)`, `
CREATE INDEX IF NOT EXISTS ix_source_cost_subject ON source_cost(subject, day)`, `
CREATE TABLE IF NOT EXISTS source_snapshot (
  owner    TEXT NOT NULL,
  subject  TEXT NOT NULL,
  source   TEXT NOT NULL,
  provider TEXT NOT NULL,
  json     TEXT NOT NULL,
  PRIMARY KEY (source, provider)
)`, `
CREATE TABLE IF NOT EXISTS source_lease (
  source     TEXT NOT NULL,
  instance   TEXT NOT NULL,
  lease      TEXT NOT NULL,
  subject    TEXT NOT NULL,
  expires_at BIGINT NOT NULL,
  PRIMARY KEY (source, instance)
)`, `
CREATE TABLE IF NOT EXISTS provider_accounts (
  owner       TEXT NOT NULL,
  subject     TEXT NOT NULL,
  id          TEXT NOT NULL PRIMARY KEY,
  provider    TEXT NOT NULL,
  label       TEXT NOT NULL,
  key_ref     TEXT NOT NULL,
  fingerprint TEXT NOT NULL,
  created_at  BIGINT NOT NULL
)`, `
CREATE INDEX IF NOT EXISTS ix_provider_accounts_subject ON provider_accounts(subject, created_at)`} {
			if _, err := tx.ExecContext(ctx, ddl); err != nil {
				return err
			}
		}
		has, err := s.d.HasColumn(ctx, tx, "sources", "usage_at")
		if err != nil {
			return err
		}
		if !has {
			if _, err = tx.ExecContext(ctx, `ALTER TABLE sources ADD COLUMN usage_at BIGINT NOT NULL DEFAULT 0`); err != nil {
				return err
			}
		}
		// A store made before shared logins has source_providers without shared.
		has, err = s.d.HasColumn(ctx, tx, "source_providers", "shared")
		if err != nil {
			return err
		}
		if !has {
			_, err = tx.ExecContext(ctx, `ALTER TABLE source_providers ADD COLUMN shared BIGINT NOT NULL DEFAULT 0`)
		}
		return err
	})
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("source: migrate: %w", err)
	}
	return s, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

const cols = `id, subject, kind, name, os, arch, version, key, zap, last_seen, beat_at, linked_at, revoked_at`

type scanner interface{ Scan(...any) error }

func scan(r scanner) (row, error) {
	var d row
	err := r.Scan(&d.ID, &d.Subject, &d.Kind, &d.Name, &d.OS, &d.Arch, &d.Version, &d.Key, &d.Zap,
		&d.LastSeen, &d.BeatAt, &d.LinkedAt, &d.RevokedAt)
	return d, err
}

type query interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

// providers fills d.Providers from q.
func providers(ctx context.Context, q query, d *row) error {
	rows, err := q.QueryContext(ctx,
		`SELECT provider, instance, kind, state, plan, shared FROM source_providers WHERE id=$1 ORDER BY instance`, d.ID)
	if err != nil {
		return fmt.Errorf("providers: %w", err)
	}
	defer rows.Close()
	d.Providers = nil
	for rows.Next() {
		var p prov
		var shared int64
		if err := rows.Scan(&p.Provider, &p.Instance, &p.Kind, &p.State, &p.Plan, &shared); err != nil {
			return fmt.Errorf("providers: %w", err)
		}
		p.Shared = shared != 0
		d.Providers = append(d.Providers, p)
	}
	return rows.Err()
}

// Link registers d for its subject, or refreshes the active source that already
// holds the same device key (idempotent on subject+key) and reports created=false.
// A revoked source's key may link again as a new source.
func (s *Store) Link(ctx context.Context, owner string, d row, id string, now time.Time) (row, bool, error) {
	if err := dialect.Text(d.Subject, d.Kind, d.Name, d.OS, d.Arch, d.Version, d.Key, d.Zap); err != nil {
		return row{}, false, fmt.Errorf("source: %w", err)
	}
	tx, err := s.d.BeginWrite(ctx, s.db)
	if err != nil {
		return row{}, false, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	got, err := scan(tx.QueryRowContext(ctx,
		`SELECT `+cols+` FROM sources WHERE subject=$1 AND key=$2 AND revoked_at=0`, d.Subject, d.Key))
	switch {
	case err == nil:
		if got.Kind != d.Kind {
			return row{}, false, errKind
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE sources SET name=$1, os=$2, arch=$3, version=$4, zap=$5 WHERE id=$6 AND subject=$7`,
			d.Name, d.OS, d.Arch, d.Version, d.Zap, got.ID, d.Subject); err != nil {
			return row{}, false, fmt.Errorf("relink: %w", err)
		}
		if err := providers(ctx, tx, &got); err != nil {
			return row{}, false, err
		}
		if err := tx.Commit(); err != nil {
			return row{}, false, fmt.Errorf("commit: %w", err)
		}
		got.Name, got.OS, got.Arch, got.Version, got.Zap = d.Name, d.OS, d.Arch, d.Version, d.Zap
		return got, false, nil
	case !errors.Is(err, sql.ErrNoRows):
		return row{}, false, fmt.Errorf("lookup: %w", err)
	}

	var active, recent int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sources WHERE subject=$1 AND revoked_at=0`, d.Subject).Scan(&active); err != nil {
		return row{}, false, fmt.Errorf("count: %w", err)
	}
	if active >= maxActive {
		return row{}, false, errCap
	}
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sources WHERE subject=$1 AND linked_at>$2`,
		d.Subject, now.Add(-time.Hour).Unix()).Scan(&recent); err != nil {
		return row{}, false, fmt.Errorf("count: %w", err)
	}
	if recent >= maxLinksPerHour {
		return row{}, false, errLinkRate
	}
	d.ID, d.LinkedAt = id, now.Unix()
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO sources (owner, subject, kind, id, name, os, arch, version, key, zap, linked_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		owner, d.Subject, d.Kind, d.ID, d.Name, d.OS, d.Arch, d.Version, d.Key, d.Zap, d.LinkedAt); err != nil {
		return row{}, false, fmt.Errorf("insert: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return row{}, false, fmt.Errorf("commit: %w", err)
	}
	return d, true, nil
}

// List is the subject's active sources, newest heartbeat first.
func (s *Store) List(ctx context.Context, subject string) ([]row, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+cols+` FROM sources WHERE subject=$1 AND revoked_at=0 ORDER BY last_seen DESC, linked_at DESC, id`, subject)
	if err != nil {
		return nil, fmt.Errorf("list: %w", err)
	}
	var out []row
	for rows.Next() {
		d, err := scan(rows)
		if err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("list: %w", err)
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("list: %w", err)
	}
	_ = rows.Close()
	for i := range out {
		if err := providers(ctx, s.db, &out[i]); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Get is the subject's own source by id, revoked or not. Another person's id is
// errNotFound, the same answer as an id that does not exist.
func (s *Store) Get(ctx context.Context, subject, id string) (row, error) {
	d, err := scan(s.db.QueryRowContext(ctx,
		`SELECT `+cols+` FROM sources WHERE subject=$1 AND id=$2`, subject, id))
	if errors.Is(err, sql.ErrNoRows) {
		return row{}, errNotFound
	}
	if err != nil {
		return row{}, fmt.Errorf("get: %w", err)
	}
	if err := providers(ctx, s.db, &d); err != nil {
		return row{}, err
	}
	return d, nil
}

// Revoke marks the subject's source revoked and forgets its provider rows, so
// nothing routes through it. Revoking again keeps the first revoked_at.
func (s *Store) Revoke(ctx context.Context, subject, id string, now time.Time) (row, error) {
	tx, err := s.d.BeginWrite(ctx, s.db)
	if err != nil {
		return row{}, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	d, err := scan(tx.QueryRowContext(ctx, `SELECT `+cols+` FROM sources WHERE subject=$1 AND id=$2`, subject, id))
	if errors.Is(err, sql.ErrNoRows) {
		return row{}, errNotFound
	}
	if err != nil {
		return row{}, fmt.Errorf("get: %w", err)
	}
	if d.RevokedAt == 0 {
		d.RevokedAt = now.Unix()
		if _, err := tx.ExecContext(ctx, `UPDATE sources SET revoked_at=$1 WHERE id=$2 AND subject=$3`, d.RevokedAt, id, subject); err != nil {
			return row{}, fmt.Errorf("revoke: %w", err)
		}
	}
	for _, q := range []string{`DELETE FROM source_providers WHERE id=$1`, `DELETE FROM source_snapshot WHERE source=$1`, `DELETE FROM source_lease WHERE source=$1`} {
		if _, err := tx.ExecContext(ctx, q, id); err != nil {
			return row{}, fmt.Errorf("revoke: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return row{}, fmt.Errorf("commit: %w", err)
	}
	return d, nil
}

// Beat records a heartbeat for the subject's source: version and zap as given,
// last_seen as now, and the provider set replaced whole. It refuses a revoked
// source (errRevoked), a beat under minBeat after the last (errTooSoon) and one
// whose signed `at` is not newer than the last accepted (errStale), so a captured
// beat cannot be replayed.
func (s *Store) Beat(ctx context.Context, subject, id string, b beat, now time.Time) error {
	if err := dialect.Text(b.Version, b.Zap); err != nil {
		return fmt.Errorf("source: %w", err)
	}
	tx, err := s.d.BeginWrite(ctx, s.db)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	d, err := scan(tx.QueryRowContext(ctx, `SELECT `+cols+` FROM sources WHERE subject=$1 AND id=$2`, subject, id))
	if errors.Is(err, sql.ErrNoRows) {
		return errNotFound
	}
	if err != nil {
		return fmt.Errorf("get: %w", err)
	}
	switch {
	case d.RevokedAt != 0:
		return errRevoked
	case d.LastSeen != 0 && now.Sub(time.Unix(d.LastSeen, 0)) < minBeat:
		return errTooSoon
	case b.At <= d.BeatAt:
		return errStale
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE sources SET version=$1, zap=$2, last_seen=$3, beat_at=$4 WHERE id=$5 AND subject=$6`,
		b.Version, b.Zap, now.Unix(), b.At, id, subject); err != nil {
		return fmt.Errorf("beat: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM source_providers WHERE id=$1`, id); err != nil {
		return fmt.Errorf("beat: %w", err)
	}
	for _, p := range b.Providers {
		if err := dialect.Text(p.Provider, p.Instance, p.Kind, p.State, p.Plan); err != nil {
			return fmt.Errorf("source: %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO source_providers (id, provider, instance, kind, state, plan, shared) VALUES ($1,$2,$3,$4,$5,$6,$7)`,
			id, p.Provider, p.Instance, p.Kind, p.State, p.Plan, bit(p.Shared)); err != nil {
			return fmt.Errorf("beat: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

func bit(b bool) int64 {
	if b {
		return 1
	}
	return 0
}
