package train

// polstore.go — the Enso policy registry in the platform's train.db: each policy's
// immutable versions, which serve, and every promotion and rollback.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/hanzoai/cloud/sdk/dialect"
)

// polSchema is the policy registry's tables, as d spells them.
func polSchema(d dialect.Dialect) string {
	return `
CREATE TABLE IF NOT EXISTS policy (
  name TEXT PRIMARY KEY,
  stack TEXT NOT NULL DEFAULT '[]',
  origin TEXT NOT NULL DEFAULT '{}',
  updated BIGINT NOT NULL
);
CREATE TABLE IF NOT EXISTS policy_version (
  name TEXT NOT NULL,
  version BIGINT NOT NULL,
  sha256 TEXT NOT NULL,
  document ` + d.Blob() + ` NOT NULL,
  plan TEXT NOT NULL,
  digest TEXT NOT NULL,
  benches TEXT NOT NULL,
  note TEXT NOT NULL DEFAULT '',
  by TEXT NOT NULL,
  at BIGINT NOT NULL,
  PRIMARY KEY (name, version)
);
CREATE UNIQUE INDEX IF NOT EXISTS policy_version_sha ON policy_version(name, sha256);
CREATE TABLE IF NOT EXISTS policy_event (
  name TEXT NOT NULL,
  seq BIGINT NOT NULL,
  action TEXT NOT NULL,
  version BIGINT NOT NULL,
  prior BIGINT NOT NULL,
  routing BIGINT NOT NULL,
  chains TEXT NOT NULL,
  note TEXT NOT NULL DEFAULT '',
  by TEXT NOT NULL,
  at BIGINT NOT NULL,
  PRIMARY KEY (name, seq)
);`
}

// registry is one policy's serving state: the versions promoted, oldest first,
// the last of which serves; and the order of each model's route before the first
// of them, which a rollback past it puts back.
type registry struct {
	name   string
	stack  []int
	origin map[string][]string
}

// top is the version that serves, 0 when none does.
func (r registry) top() int {
	if len(r.stack) == 0 {
		return 0
	}
	return r.stack[len(r.stack)-1]
}

// policyRow is one version as stored.
type policyRow struct {
	version  int
	sha256   string
	document []byte
	plan     plan
	digest   string
	benches  []string
	note     string
	by       string
	at       int64
}

// policyEvent is one promotion or rollback as stored.
type policyEvent struct {
	seq     int
	action  string
	version int
	prior   int
	routing int64
	chains  []PolicyChain
	note    string
	by      string
	at      int64
}

func (s *store) registry(ctx context.Context, name string) (registry, error) {
	r := registry{name: name, stack: []int{}, origin: map[string][]string{}}
	var stack, origin string
	err := s.db.QueryRowContext(ctx, `SELECT stack, origin FROM policy WHERE name = $1`, name).Scan(&stack, &origin)
	if err == sql.ErrNoRows {
		return r, nil
	}
	if err != nil {
		return r, err
	}
	if err := json.Unmarshal([]byte(stack), &r.stack); err != nil {
		return r, err
	}
	if err := json.Unmarshal([]byte(origin), &r.origin); err != nil {
		return r, err
	}
	return r, nil
}

// errMoved is a registry another act changed since it was read.
var errMoved = errors.New("train: the policy registry moved")

// keepOrigin writes the order a model's route had before any policy, ahead of the
// change that needs it, so a change that lands and is not recorded still leaves the
// order a rollback puts back. It keeps the stack as stored.
func (s *store) keepOrigin(ctx context.Context, r registry) error {
	origin, _ := json.Marshal(r.origin)
	_, err := s.db.ExecContext(ctx, `INSERT INTO policy (name, stack, origin, updated) VALUES ($1, '[]', $2, $3)
ON CONFLICT(name) DO UPDATE SET origin = excluded.origin`, r.name, string(origin), unix())
	return err
}

// enact writes a registry's new state and the event that made it, as one, only
// over the stack it was read with (was): errMoved when another act changed it.
func (s *store) enact(ctx context.Context, r registry, was []int, e policyEvent) error {
	stack, _ := json.Marshal(r.stack)
	prior, _ := json.Marshal(was)
	origin, _ := json.Marshal(r.origin)
	chains, _ := json.Marshal(e.chains)
	if err := dialect.Text(e.action, e.note, e.by); err != nil {
		return err
	}
	tx, err := dialect.Of(s.db).BeginWrite(ctx, s.db)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `INSERT INTO policy (name, stack, origin, updated) VALUES ($1, '[]', '{}', $2) ON CONFLICT (name) DO NOTHING`, r.name, e.at); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `UPDATE policy SET stack = $1, origin = $2, updated = $3 WHERE name = $4 AND stack = $5`,
		string(stack), string(origin), e.at, r.name, string(prior))
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n != 1 {
		return errMoved
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO policy_event (name, seq, action, version, prior, routing, chains, note, by, at)
VALUES ($1, (SELECT COALESCE(MAX(seq), 0) + 1 FROM policy_event WHERE name = $2), $3, $4, $5, $6, $7, $8, $9, $10)`,
		r.name, r.name, e.action, e.version, e.prior, e.routing, string(chains), e.note, e.by, e.at); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *store) addPolicyVersion(ctx context.Context, name string, v policyRow) error {
	if err := dialect.Text(name, v.sha256, v.digest, v.note, v.by); err != nil {
		return err
	}
	p, _ := json.Marshal(v.plan)
	b, _ := json.Marshal(v.benches)
	_, err := s.db.ExecContext(ctx, `INSERT INTO policy_version (name, version, sha256, document, plan, digest, benches, note, by, at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`, name, v.version, v.sha256, v.document, string(p), v.digest, string(b), v.note, v.by, v.at)
	return err
}

// lastPolicyVersion is the newest version's number, 0 when there is none.
func (s *store) lastPolicyVersion(ctx context.Context, name string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM policy_version WHERE name = $1`, name).Scan(&n)
	return n, err
}

const policyCols = `version, sha256, plan, digest, benches, note, by, at`

func scanPolicy(sc interface{ Scan(...any) error }, doc bool) (policyRow, error) {
	var v policyRow
	var p, b string
	dest := []any{&v.version, &v.sha256, &p, &v.digest, &b, &v.note, &v.by, &v.at}
	if doc {
		dest = append(dest, &v.document)
	}
	if err := sc.Scan(dest...); err != nil {
		return v, err
	}
	if err := json.Unmarshal([]byte(p), &v.plan); err != nil {
		return v, err
	}
	if err := json.Unmarshal([]byte(b), &v.benches); err != nil {
		return v, err
	}
	return v, nil
}

// policyVersions are a policy's versions newest first, their documents left out.
func (s *store) policyVersions(ctx context.Context, name string) ([]policyRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+policyCols+` FROM policy_version WHERE name = $1 ORDER BY version DESC`, name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []policyRow{}
	for rows.Next() {
		v, err := scanPolicy(rows, false)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// policyVersion is one version with its document, errNotFound when there is none.
func (s *store) policyVersion(ctx context.Context, name string, version int) (policyRow, error) {
	v, err := scanPolicy(s.db.QueryRowContext(ctx, `SELECT `+policyCols+`, document FROM policy_version WHERE name = $1 AND version = $2`, name, version), true)
	if err == sql.ErrNoRows {
		return v, errNotFound
	}
	return v, err
}

// policyVersionOf is the version holding a document of this digest, 0 when none.
func (s *store) policyVersionOf(ctx context.Context, name, sha string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT version FROM policy_version WHERE name = $1 AND sha256 = $2`, name, sha).Scan(&n)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return n, err
}

// policyEvents are a policy's promotions and rollbacks, newest first.
func (s *store) policyEvents(ctx context.Context, name string) ([]policyEvent, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT seq, action, version, prior, routing, chains, note, by, at FROM policy_event WHERE name = $1 ORDER BY seq DESC LIMIT 200`, name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []policyEvent{}
	for rows.Next() {
		var e policyEvent
		var c string
		if err := rows.Scan(&e.seq, &e.action, &e.version, &e.prior, &e.routing, &c, &e.note, &e.by, &e.at); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(c), &e.chains); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
