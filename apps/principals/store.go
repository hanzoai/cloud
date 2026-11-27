package principals

// store.go — ONE file for the deployment, holding three kinds of record, each
// keyed by the org it is about and never read for another.
//
//	matches     an org and a designation it screened close to, every name,
//	            address or jurisdiction of the org that did, and the platform
//	            reviewer's decision on it
//	clearances  a payer's clearance to pay a payee: the decision, whole
//	statements  a TaxPrincipalCredential signed for one of an org's agents, and
//	            when and why it was withdrawn (statements.go)
//
// It is one file and not one per org because the review queue is the platform's:
// a reviewer reads every open match, whichever org it names, and that read is the
// only one here that crosses orgs — SuperAdmin's, gated before it is made.
//
// A CLEARANCE IS A RECORD, and immutability is the schema's: a trigger refuses to
// update or delete one, so a decision cannot be rewritten after the payer acted
// on it. A match's decision can be changed only by another decision, on the trail.
//
// EVERY WRITE IS ON THE TRAIL IN ITS OWN TRANSACTION: the rows are written, the
// trail takes the record, and only then does the transaction commit — so a write
// the trail refused is rolled back, and a record the trail took for a write that
// did not commit is followed by one saying so (within, and ops.unmade).

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/hanzoai/cloud/sdk/dialect"
	"github.com/hanzoai/cloud/sdk/iam"
	"github.com/hanzoai/cloud/sdk/tenure"
	"github.com/hanzoai/namespace"
)

var errNotFound = errors.New("principal: not found")

// THE COLUMNS THIS STORE SEALS (internal/org, field.go), on both engines, each bound to the
// system namespace's "principal", the table, the column and the row's id:
//
//	matches.hits, .reason, .note    every name, address or jurisdiction of the org that hit a
//	                                designation, why, and the reviewer's free text about it
//	clearances.body                 a payer's clearance to pay a payee, the decision whole
//
// No WHERE or ORDER BY reads either. Left plain: ids, the list and its reference, the
// designation entry (the list's own public text), scores, statuses, who decided and when,
// the payer and payee ids a clearance is looked up by, and statements.body, a signed
// credential the org presents to others, whose signature and not its secrecy protects it.
type store struct {
	db     *sql.DB
	d      dialect.Dialect
	fields *tenure.Fields
}

// subsystem names the store: the system namespace's "principal". The database and the
// sealer are both made from it, so a column's key and its schema cannot name two things.
const subsystem = "principal"

var (
	matchKey, matchSealed         = []string{"id"}, []string{"hits", "reason", "note"}
	clearanceKey, clearanceSealed = []string{"id"}, []string{"body"}
)

// openStore wraps the database in the store and keys the sealed columns. master is
// cloud.Deps.MasterKey; with none the store does not open, so no match or clearance is
// ever written in the clear because a key was missing.
func openStore(db *sql.DB, master []byte) (*store, error) {
	c, err := tenure.NewCipher(master)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("principal: %w", err)
	}
	fields, err := c.Fields(namespace.System(), subsystem)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("principal: %w", err)
	}
	s := &store{db: db, d: dialect.Of(db), fields: fields}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := s.d.Migrate(ctx, db, func(tx *sql.Tx) error { return s.migrate(ctx, tx) }); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("principal migrate: %w", err)
	}
	return s, nil
}

func (s *store) Close() error { return s.db.Close() }

// migrate creates the tables and carries a file an earlier build wrote forward, on the
// transaction Dialect.Migrate holds.
//
// matches and clearances are listed in the order they were written. A file ordered them
// by its rowid; an order the store keeps itself is a seq column, numbered as a row is
// written (dialect.NextSeq), and a file written before it has its rows numbered once.
// A clearance is a record the trigger will not let anyone rewrite, so the trigger is
// lifted for that one numbering and put back before the transaction ends.
func (s *store) migrate(ctx context.Context, tx *sql.Tx) error {
	d := s.d
	if _, err := tx.ExecContext(ctx, tables); err != nil {
		return err
	}
	if err := d.Sequence(ctx, tx, "matches"); err != nil {
		return err
	}
	if !d.Postgres() {
		if _, err := tx.ExecContext(ctx, `DROP TRIGGER IF EXISTS clearances_final`); err != nil {
			return err
		}
	}
	if err := d.Sequence(ctx, tx, "clearances"); err != nil {
		return err
	}
	// A SQLite file from before the columns were sealed holds them in plaintext. Seal them
	// in place, before any read and with the clearances trigger down, so the store never
	// reads a value it did not write and cmd/sqlmove copies ciphertext. hanzo-sql tables
	// are born sealed.
	if !d.Postgres() {
		for _, t := range []struct {
			table       string
			key, sealed []string
		}{{"matches", matchKey, matchSealed}, {"clearances", clearanceKey, clearanceSealed}} {
			if _, err := s.fields.UpgradeTx(ctx, tx, t.table, t.key, t.sealed); err != nil {
				return err
			}
		}
	}
	// A clearance is a record: nothing updates or deletes one.
	if err := d.Refuse(ctx, tx, "clearances_final", "clearances", "UPDATE", "", "principal: a clearance is a record and is never rewritten"); err != nil {
		return err
	}
	return d.Refuse(ctx, tx, "clearances_kept", "clearances", "DELETE", "", "principal: a clearance is a record and is never deleted")
}

const tables = `
CREATE TABLE IF NOT EXISTS matches (
  id         TEXT PRIMARY KEY,
  org        TEXT NOT NULL,
  list       TEXT NOT NULL,
  ref        TEXT NOT NULL,
  entry      TEXT NOT NULL,
  hits       TEXT NOT NULL,
  score      DOUBLE PRECISION NOT NULL,
  reason     TEXT NOT NULL,
  status     TEXT NOT NULL,
  decided_by TEXT NOT NULL DEFAULT '',
  decided_at BIGINT  NOT NULL DEFAULT 0,
  note       TEXT NOT NULL DEFAULT '',
  found_at   BIGINT  NOT NULL,
  seen_at    BIGINT  NOT NULL,
  seq        BIGINT  NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS ix_matches_org ON matches(org);
CREATE INDEX IF NOT EXISTS ix_matches_status ON matches(status, found_at);

CREATE TABLE IF NOT EXISTS clearances (
  id         TEXT PRIMARY KEY,
  payer      TEXT NOT NULL,
  payee      TEXT NOT NULL,
  allowed    BIGINT  NOT NULL,
  payer_ok   BIGINT  NOT NULL DEFAULT 0,
  body       TEXT NOT NULL,
  created_at BIGINT  NOT NULL,
  seq        BIGINT  NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS ix_clearances_payer ON clearances(payer, created_at);
CREATE INDEX IF NOT EXISTS ix_clearances_pair ON clearances(payer, payee, created_at);

-- The clearances that asked again of an earlier one, and the payment each
-- repeats: the first clearance of it. The payer's hourly bound counts payments,
-- so these count against the payment they repeat, not the payer.
CREATE TABLE IF NOT EXISTS repeats (
  id         TEXT PRIMARY KEY,
  root       TEXT NOT NULL,
  created_at BIGINT  NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_repeats_root ON repeats(root, created_at);

CREATE TABLE IF NOT EXISTS statements (
  id      TEXT PRIMARY KEY,
  org     TEXT NOT NULL,
  agent   TEXT NOT NULL,
  kind    TEXT NOT NULL,
  form    TEXT NOT NULL DEFAULT '',
  signed  BIGINT  NOT NULL DEFAULT 0,
  issued  BIGINT  NOT NULL,
  expires BIGINT  NOT NULL DEFAULT 0,
  revoked BIGINT  NOT NULL DEFAULT 0,
  reason  TEXT NOT NULL DEFAULT '',
  body    TEXT NOT NULL,
  claim   TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS ix_statements_org ON statements(org, revoked);

-- The orgs whose tax form was certified and valid when their statements were last
-- reconciled, and the form: the sweep reconciles every one of them, so an agent
-- created since is signed for, and the shop reads documented off it.
CREATE TABLE IF NOT EXISTS forms (
  org     TEXT PRIMARY KEY,
  form    TEXT NOT NULL,
  signed  BIGINT  NOT NULL,
  expires BIGINT  NOT NULL DEFAULT 0,
  at      BIGINT  NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_statements_due ON statements(expires) WHERE revoked = 0 AND expires > 0;
`

// querier is what a read needs: the database, or a transaction on it.
type querier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// errUncommitted is a transaction whose writes the trail took and the file did
// not.
var errUncommitted = errors.New("principal: the change was recorded and did not commit")

// within runs fn in one transaction and commits it. fn writes, then appends the
// trail's record last; an error from fn — the trail's refusal included — rolls
// everything back. A commit that fails after the trail took fn's record answers
// errUncommitted, for the caller to record that nothing changed.
func (s *store) within(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.d.BeginWrite(ctx, s.db)
	if err != nil {
		return fmt.Errorf("principal: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("%w: %v", errUncommitted, err)
	}
	return nil
}

// ---- matches ---------------------------------------------------------------

const (
	hitName         = "name"
	hitAddress      = "address"
	hitJurisdiction = "jurisdiction"
)

// matchRow is one stored match: one org against one designation.
type matchRow struct {
	id, org, list, ref, entry, reason, status string
	hits                                      []iam.Hit
	score                                     float64
	decidedBy, note                           string
	decidedAt, found, seen                    int64
}

const matchCols = `id, org, list, ref, entry, hits, score, reason, status, decided_by, decided_at, note, found_at, seen_at`

func (s *store) scanMatch(sc interface{ Scan(...any) error }) (*matchRow, error) {
	var r matchRow
	var hits string
	err := sc.Scan(&r.id, &r.org, &r.list, &r.ref, &r.entry, &hits, &r.score, &r.reason,
		&r.status, &r.decidedBy, &r.decidedAt, &r.note, &r.found, &r.seen)
	if err != nil {
		return nil, err
	}
	// The row key is read off the row itself, so a ciphertext carried to another match fails.
	o := s.fields.Row("matches", r.id)
	hits, r.reason, r.note = o.Open("hits", hits), o.Open("reason", r.reason), o.Open("note", r.note)
	if err := o.Err(); err != nil {
		return nil, fmt.Errorf("principal: stored match %s: %w", r.id, err)
	}
	if err := json.Unmarshal([]byte(hits), &r.hits); err != nil {
		return nil, fmt.Errorf("principal: stored match %s: %w", r.id, err)
	}
	return &r, nil
}

// Changes a sighting makes to a match.
const (
	sawNothing   = ""          // a hit the match already holds
	sawFound     = "found"     // a designation this org had no match for
	sawAdded     = "added"     // another hit on an open or confirmed match
	sawReopened  = "reopened"  // a hit a cleared namesake did not cover
	sawConfirmed = "confirmed" // a designated address, which is the designated person itself
)

// take adds one sighting to a match and says what it changed. A decision covers
// the hits the reviewer read: a confirmed match stays confirmed whatever the org
// is now called, a cleared one is reopened by a hit it did not cover, and a
// designated address confirms any match that is not already confirmed.
func (r *matchRow) take(h iam.Hit, now int64) string {
	if slices.Contains(r.hits, h) {
		return sawNothing
	}
	r.hits = append(r.hits, h)
	switch {
	case h.Kind == hitAddress && r.status != matchConfirmed:
		r.status, r.decidedBy, r.decidedAt, r.note = matchConfirmed, byAddress, now, ""
		return sawConfirmed
	case r.status == matchCleared:
		r.status, r.decidedBy, r.decidedAt, r.note = matchOpen, "", 0, ""
		return sawReopened
	}
	return sawAdded
}

// byAddress is who decided a match a designated address confirmed.
const byAddress = "exact designated address"

// sighting is one thing that screened close to a designation, for one org.
type sighting struct {
	row matchRow // the match as it is born: one hit
	hit iam.Hit
}

// fold applies sightings to an org's stored matches, in memory, and answers the
// matches as they would stand with each change it made. It is the whole rule a
// sighting follows; screen writes what it answers.
func fold(stored []matchRow, seen []sighting, now int64) ([]matchRow, []int, []string) {
	rows := slices.Clone(stored)
	var changed []int
	var what []string
	for _, s := range seen {
		i := slices.IndexFunc(rows, func(r matchRow) bool { return r.id == s.row.id })
		if i < 0 {
			rows = append(rows, s.row)
			changed, what = append(changed, len(rows)-1), append(what, sawFound)
			continue
		}
		rows[i].hits = slices.Clone(rows[i].hits)
		if w := rows[i].take(s.hit, now); w != sawNothing {
			rows[i].score, rows[i].entry, rows[i].seen = max(rows[i].score, s.row.score), s.row.entry, now
			changed, what = append(changed, i), append(what, w)
		}
	}
	return rows, changed, what
}

// ofOrg reads every match naming org.
func (s *store) ofOrg(ctx context.Context, q querier, org string) ([]matchRow, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+matchCols+` FROM matches WHERE org = $1 ORDER BY found_at, seq`, org)
	if err != nil {
		return nil, fmt.Errorf("principal: read matches: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []matchRow
	for rows.Next() {
		r, err := s.scanMatch(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// putMatch writes one match whole.
func (s *store) putMatch(ctx context.Context, tx *sql.Tx, r matchRow) error {
	hits, err := json.Marshal(r.hits)
	if err != nil {
		return err
	}
	if err := dialect.Text(r.id, r.org, r.list, r.ref, r.entry, string(hits), r.reason, r.status, r.decidedBy, r.note); err != nil {
		return fmt.Errorf("principal: record match: %w", err)
	}
	o := s.fields.Row("matches", r.id)
	sealedHits, reason, note := o.Seal("hits", string(hits)), o.Seal("reason", r.reason), o.Seal("note", r.note)
	if err := o.Err(); err != nil {
		return fmt.Errorf("principal: record match: %w", err)
	}
	_, err = tx.ExecContext(ctx,
		`INSERT INTO matches (`+matchCols+`, seq) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, `+dialect.NextSeq("matches")+`)
		 ON CONFLICT(id) DO UPDATE SET entry = excluded.entry, hits = excluded.hits, score = excluded.score,
		   reason = excluded.reason, status = excluded.status, decided_by = excluded.decided_by,
		   decided_at = excluded.decided_at, note = excluded.note, seen_at = excluded.seen_at`,
		r.id, r.org, r.list, r.ref, r.entry, sealedHits, r.score, reason, r.status,
		r.decidedBy, r.decidedAt, note, r.found, r.seen)
	if err != nil {
		return fmt.Errorf("principal: record match: %w", err)
	}
	return nil
}

func (s *store) match(ctx context.Context, q querier, id string) (*matchRow, error) {
	r, err := s.scanMatch(q.QueryRowContext(ctx, `SELECT `+matchCols+` FROM matches WHERE id = $1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("principal: read match: %w", err)
	}
	return r, nil
}

// queue lists matches with a status, oldest first; an empty status lists every
// match. It reads across orgs, and only a reviewer's op calls it.
func (s *store) queue(ctx context.Context, status string, limit int) ([]matchRow, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+matchCols+` FROM matches WHERE ($1 = '' OR status = $1) ORDER BY found_at, seq LIMIT $2`,
		status, limit)
	if err != nil {
		return nil, fmt.Errorf("principal: list matches: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []matchRow
	for rows.Next() {
		r, err := s.scanMatch(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// decide records a reviewer's decision, only over the status the reviewer read,
// so two reviewers racing on one match cannot both decide it unseen.
func (s *store) decide(ctx context.Context, tx *sql.Tx, id, from, to, by, note string, at int64) error {
	o := s.fields.Row("matches", id)
	sealed := o.Seal("note", note)
	if err := o.Err(); err != nil {
		return fmt.Errorf("principal: decide match: %w", err)
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE matches SET status = $1, decided_by = $2, decided_at = $3, note = $4 WHERE id = $5 AND status = $6`,
		to, by, at, sealed, id, from)
	if err != nil {
		return fmt.Errorf("principal: decide match: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return errMoved
	}
	return nil
}

// errMoved is a row that changed between the read and the write.
var errMoved = errors.New("principal: the match changed since it was read")

// ---- clearances ------------------------------------------------------------

func (s *store) putClearance(ctx context.Context, tx *sql.Tx, id, payer, payee string, allowed, payerOK bool, body string, at int64) error {
	if err := dialect.Text(id, payer, payee, body); err != nil {
		return fmt.Errorf("principal: record clearance: %w", err)
	}
	o := s.fields.Row("clearances", id)
	sealed := o.Seal("body", body)
	if err := o.Err(); err != nil {
		return fmt.Errorf("principal: record clearance: %w", err)
	}
	_, err := tx.ExecContext(ctx,
		`INSERT INTO clearances (id, payer, payee, allowed, payer_ok, body, created_at, seq) VALUES ($1, $2, $3, $4, $5, $6, $7, `+dialect.NextSeq("clearances")+`)`,
		id, payer, payee, flag(allowed), flag(payerOK), sealed, at)
	if err != nil {
		return fmt.Errorf("principal: record clearance: %w", err)
	}
	return nil
}

// putRepeat records that clearance id asked again of the payment root.
func putRepeat(ctx context.Context, tx *sql.Tx, id, root string, at int64) error {
	if _, err := tx.ExecContext(ctx, `INSERT INTO repeats (id, root, created_at) VALUES ($1, $2, $3)`, id, root, at); err != nil {
		return fmt.Errorf("principal: record clearance: %w", err)
	}
	return nil
}

// repeated reads the payment a clearance asked again repeats — again's own root,
// or again itself when it asked nothing again — when again is payer's own
// clearance to payee, and how often that payment was asked again in the hour to
// now.
func repeated(ctx context.Context, q querier, payer, payee, again string, now int64) (root string, n int, found bool, err error) {
	err = q.QueryRowContext(ctx, `SELECT COALESCE((SELECT root FROM repeats WHERE id = c.id), c.id)
		FROM clearances c WHERE c.id = $1 AND c.payer = $2 AND c.payee = $3`, again, payer, payee).Scan(&root)
	if errors.Is(err, sql.ErrNoRows) {
		return "", 0, false, nil
	}
	if err != nil {
		return "", 0, false, fmt.Errorf("principal: read clearances: %w", err)
	}
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM repeats WHERE root = $1 AND created_at >= $2`,
		root, now-int64(hourWindow.Seconds())).Scan(&n); err != nil {
		return "", 0, false, fmt.Errorf("principal: read clearances: %w", err)
	}
	return root, n, true, nil
}

// clearance reads one of payer's clearances; another payer's id is not found.
func (s *store) clearance(ctx context.Context, payer, id string) (string, error) {
	var body string
	err := s.db.QueryRowContext(ctx, `SELECT body FROM clearances WHERE id = $1 AND payer = $2`, id, payer).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		return "", errNotFound
	}
	if err != nil {
		return "", fmt.Errorf("principal: read clearance: %w", err)
	}
	o := s.fields.Row("clearances", id)
	body = o.Open("body", body)
	if err := o.Err(); err != nil {
		return "", fmt.Errorf("principal: read clearance: %w", err)
	}
	return body, nil
}

// clearances lists payer's clearances, newest first; a payee narrows them.
func (s *store) clearances(ctx context.Context, payer, payee string, limit int) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, body FROM clearances WHERE payer = $1 AND ($2 = '' OR payee = $2)
		 ORDER BY created_at DESC, seq DESC, id DESC LIMIT $3`, payer, payee, limit)
	if err != nil {
		return nil, fmt.Errorf("principal: list clearances: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var id, body string
		if err := rows.Scan(&id, &body); err != nil {
			return nil, fmt.Errorf("principal: scan clearance: %w", err)
		}
		o := s.fields.Row("clearances", id)
		body = o.Open("body", body)
		if err := o.Err(); err != nil {
			return nil, fmt.Errorf("principal: scan clearance: %w", err)
		}
		out = append(out, body)
	}
	return out, rows.Err()
}

// load is how much a payer has asked: its clearances in the hour to now that
// asked nothing again, the payees it first asked about in the day to now, and
// whether it has asked about payee before.
func load(ctx context.Context, q querier, payer, payee string, now int64) (hour, fresh int, known bool, err error) {
	day, since := now-int64(dayWindow.Seconds()), now-int64(hourWindow.Seconds())
	var before int
	err = q.QueryRowContext(ctx, `SELECT
		  (SELECT COUNT(*) FROM clearances WHERE payer = $1 AND created_at >= $2
		     AND id NOT IN (SELECT id FROM repeats WHERE created_at >= $2)),
		  (SELECT COUNT(DISTINCT payee) FROM clearances WHERE payer = $1 AND created_at >= $3
		     AND payee NOT IN (SELECT payee FROM clearances WHERE payer = $1 AND created_at < $3)),
		  (SELECT COUNT(*) FROM clearances WHERE payer = $1 AND payee = $4)`,
		payer, since, day, payee).Scan(&hour, &fresh, &before)
	if err != nil {
		return 0, 0, false, fmt.Errorf("principal: read clearances: %w", err)
	}
	return hour, fresh, before > 0, nil
}

// asked reports whether payer asked, since, for a clearance to pay payee on
// which the payer itself was verified and screened clear.
func (s *store) asked(ctx context.Context, payer, payee string, since int64) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM clearances WHERE payer = $1 AND payee = $2 AND payer_ok = 1 AND created_at >= $3`,
		payer, payee, since).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("principal: read clearances: %w", err)
	}
	return n > 0, nil
}

// clean trims and collapses a free-text note.
func clean(s string) string { return strings.Join(strings.Fields(s), " ") }

// flag is a boolean as the integer a column keeps it in.
func flag(b bool) int {
	if b {
		return 1
	}
	return 0
}
