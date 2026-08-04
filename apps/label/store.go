package label

// store.go is the SOURCE OF RECORD: the tenant's own append-only assertion log,
// in the tenant's own encrypted SQLite file.
//
// THE FILE IS THE TENANT BOUNDARY. cloud.OrgNamespace folds the VALIDATED org
// through the one injective slugger and names {DataDir}/orgs/{slug}/label.db;
// cloud.OrgStore opens it under that namespace's own key. There is no org column
// on any table here and there cannot be a cross-tenant read, because there is no
// statement that could express one. A tenant is not a predicate somebody
// remembered to add — it is a different file.
//
// APPEND ONLY. No statement in this package updates an assertion, and none
// deletes one except the retention sweep, which disposes of whole records rather
// than redacting fields (the same rule luxfi/aml pkg/retention already holds: a
// partially-erased compliance record is a record nobody can attest to). A
// correction is a NEW assertion that wins from the moment it became knowable —
// see resolve.go's stronger().
//
// The one mutable row in this file is `delivery`, and it holds no assertion: it
// is how far the DERIVED copy has caught up. A watermark is a fact about a
// transfer, not about the world, and the distinction is worth the separate table
// — it keeps "the record is append-only" a property of the schema rather than a
// claim in a comment.
//
// DURABLE FIRST, COLUMNAR AFTER. Every write lands here before the columnar
// mirror is attempted, and the mirror's failure is reported, never fatal. The
// warehouse is a shared, single-pod plane and /v1/event is explicitly best-effort;
// a compliance record that rode either would be lost by a bus hiccup, invisibly.
//
// "Here" is a local file, so landing is not the same as being kept: the op layer
// SHIPS this file to its durable object before it answers (label.go, state.ship).
// This file is where the record lives; that ship is what makes living here mean
// something past the next rollout.
//
// WHY IT IS ITS OWN FILE AND NOT A TABLE IN risk.db. A label's writers are mostly
// not the decision plane: commerce adjudicates the dispute, the compliance face
// closes the case, an analyst files the review. Its readers are the dataset
// materialiser and the evaluator. And its retention clock is its own — a label
// that fed an adverse action is a compliance record whose life is not the life of
// the decision that cited it. One owner per file, so a second process opening the
// decision plane's file is not a thing anyone can do by accident.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"iter"
	"strings"
	"time"
)

// schema is the whole shape of the record plane. Applied on open; idempotent, so
// a fresh file and an existing one converge.
//
// `by` is double-quoted at every use site: it is a SQLite keyword, and the wire
// field, the Go field and the column are deliberately the SAME word rather than
// three spellings of one fact.
//
// `seq` IS THE DELIVERY ORDER, AND IT IS THE STORE'S TO ASSIGN. It is the rowid
// under AUTOINCREMENT, so it is allocated inside the statement that inserts the
// row — and every statement against one tenant's file runs on ONE connection
// (orgdb.go: SetMaxOpenConns(1), the single-writer pragma set), so allocation
// order IS commit order with no interleaving available to reorder them. A row a
// reader can see therefore implies every lower seq is already visible: a cursor
// over seq cannot step over a write that had not committed when it was read.
//
// AUTOINCREMENT and not a bare rowid, because a bare rowid is max+1 and is REUSED
// after the top row is deleted. A retention sweep that empties the table would
// restart the sequence below a cursor that had advanced, and every row written
// afterwards would be born already-delivered. AUTOINCREMENT keeps the high-water
// mark in sqlite_sequence and never hands a value back.
const schema = `
CREATE TABLE IF NOT EXISTS assert (
	seq         INTEGER PRIMARY KEY AUTOINCREMENT,
	id          TEXT NOT NULL UNIQUE,
	kind        TEXT NOT NULL,
	subject     TEXT NOT NULL,
	at          INTEGER NOT NULL,
	seen        INTEGER NOT NULL,
	knowable    INTEGER NOT NULL,
	disposition TEXT NOT NULL,
	source      TEXT NOT NULL,
	evidence    TEXT NOT NULL DEFAULT '',
	"by"        TEXT NOT NULL DEFAULT '',
	confidence  REAL NOT NULL DEFAULT 0,
	hold        INTEGER NOT NULL DEFAULT 0,
	wrote       INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS assert_subject ON assert(kind, subject, at);
CREATE INDEX IF NOT EXISTS assert_at ON assert(at);
CREATE INDEX IF NOT EXISTS assert_wrote ON assert(wrote);
CREATE TABLE IF NOT EXISTS delivery (
	only INTEGER PRIMARY KEY CHECK (only = 1),
	seq  INTEGER NOT NULL
);
`

// cols is the projection every read of the record plane shares, in the order
// scan() consumes. One spelling, so a column added to the schema is added to the
// readers in one place or in none.
const cols = `seq, id, kind, subject, at, seen, knowable, disposition, source, evidence, "by", confidence, hold, wrote`

// store is one tenant's record plane.
type store struct{ db *sql.DB }

// openStore is cloud.OrgStore's open func: the file is already opened, pragma'd
// and keyed by the time it arrives here, so this only migrates.
func openStore(db *sql.DB) (*store, error) {
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("label: migrate: %w", err)
	}
	return &store{db: db}, nil
}

func (s *store) Close() error { return s.db.Close() }

// outcome is what happened to one asserted fact. A caller redelivering a webhook
// needs to distinguish "we already had this" from "we took it", and a caller
// sending a batch needs to know WHICH member was refused and why — so the answer
// is per fact, never a count.
type outcome struct {
	ID      string
	Status  string // recorded | duplicate | refused
	Refusal string
}

const (
	recorded  = "recorded"
	duplicate = "duplicate"
	refused   = "refused"
)

// record writes one assertion, idempotently on its content digest.
//
// INSERT OR IGNORE, never INSERT OR REPLACE. The digest covers every semantic
// field, so a row that already exists is byte-identical to the one being written
// and there is nothing to update; anything that differs is a DIFFERENT assertion
// with a different digest and lands beside it. That is what "never silently
// overwrite" means mechanically rather than as a promise.
//
// It does not name `seq` — the store assigns that — and it does not name `hold`:
// a litigation hold is a fact about the record, it has its own op, and a write
// path that could set it would be a second way to place one and no way at all to
// release one.
func (s *store) record(ctx context.Context, f Fact) (outcome, error) {
	res, err := s.db.ExecContext(ctx, `
INSERT OR IGNORE INTO assert (id, kind, subject, at, seen, knowable, disposition, source, evidence, "by", confidence, wrote)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		f.ID, string(f.Kind), f.Subject, f.At.Unix(), f.Seen.Unix(), f.Knowable.Unix(),
		string(f.Disposition), string(f.Source), f.Evidence, f.By, f.Confidence,
		f.Wrote.Unix())
	if err != nil {
		return outcome{}, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return outcome{}, err
	}
	if n == 0 {
		return outcome{ID: f.ID, Status: duplicate}, nil
	}
	return outcome{ID: f.ID, Status: recorded}, nil
}

// query bounds a read of the record plane. Every field is a BOUND parameter;
// nothing here becomes SQL text.
type query struct {
	Kind    Kind
	Subject string
	Source  Source
	From    time.Time
	To      time.Time
	Limit   int
}

// maxPage bounds any single read. A tenant with ten million assertions must not
// be able to ask for all of them in one request against a single-writer file.
const maxPage = 5000

// facts reads assertions matching q, newest event first.
func (s *store) facts(ctx context.Context, q query) ([]Fact, error) {
	where, args := "1=1", []any{}
	if q.Kind != "" {
		where += " AND kind = ?"
		args = append(args, string(q.Kind))
	}
	if q.Subject != "" {
		where += " AND subject = ?"
		args = append(args, q.Subject)
	}
	if q.Source != "" {
		where += " AND source = ?"
		args = append(args, string(q.Source))
	}
	if !q.From.IsZero() {
		where += " AND at >= ?"
		args = append(args, q.From.Unix())
	}
	if !q.To.IsZero() {
		where += " AND at < ?"
		args = append(args, q.To.Unix())
	}
	limit := q.Limit
	if limit <= 0 || limit > maxPage {
		limit = maxPage
	}
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, `
SELECT `+cols+`
FROM assert WHERE `+where+` ORDER BY at DESC, seen DESC, id ASC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scan(rows)
}

// forSubjects reads every assertion about a named set of events. It is the read
// behind the resolve op, and it is bounded by the caller's subject list rather
// than by a page: resolving 500 events must return ALL of each event's
// assertions or the precedence rule would be applied to a truncated set and
// would silently return the wrong winner.
//
// It is bounded by a TOTAL row count and it REFUSES at the bound rather than
// truncating, for the same reason it cannot be paged: the precedence rule applied
// to a partial set returns a confident wrong winner. A caller that names events
// carrying more assertions than the bound gets errTooWide and can ask for fewer.
func (s *store) forSubjects(ctx context.Context, want []Fact, cap int) ([]Fact, error) {
	var out []Fact
	// Chunked so the parameter count is a property of this file rather than of
	// whichever SQLite the build linked. Three placeholders per event against a
	// host-variable ceiling that has been 999 in some builds and 32766 in others
	// is exactly the kind of limit that holds in every test and fails on one
	// deployment — and failing HERE would silently truncate the assertion set a
	// precedence rule is applied to, which returns a WRONG winner rather than an
	// error.
	for chunk := range chunks(want, subjectChunk) {
		sql := `
SELECT ` + cols + `
FROM assert WHERE `
		args := make([]any, 0, len(chunk)*3)
		for i, w := range chunk {
			if i > 0 {
				sql += " OR "
			}
			sql += "(kind = ? AND subject = ? AND at = ?)"
			args = append(args, string(w.Kind), w.Subject, w.At.Unix())
		}
		rows, err := s.db.QueryContext(ctx, sql, args...)
		if err != nil {
			return nil, err
		}
		got, err := scan(rows)
		_ = rows.Close()
		if err != nil {
			return nil, err
		}
		out = append(out, got...)
		if len(out) > cap {
			return nil, fmt.Errorf("%w: these events carry more than %d assertions", errTooWide, cap)
		}
	}
	return out, nil
}

// subjectChunk is how many events one statement names. 200 events is 600 host
// variables, comfortably inside the smallest ceiling any SQLite build ships.
const subjectChunk = 200

// chunks yields consecutive slices of at most n.
func chunks[T any](in []T, n int) iter.Seq[[]T] {
	return func(yield func([]T) bool) {
		for i := 0; i < len(in); i += n {
			j := min(i+n, len(in))
			if !yield(in[i:j]) {
				return
			}
		}
	}
}

// window reads every assertion whose event falls in [from, to). Used by coverage,
// which must fold over the WHOLE window rather than a page of it: a coverage
// number computed over a truncated set would understate what is judged and would
// do it silently, which is the one answer worse than no answer.
//
// So it cannot be paged, and therefore it REFUSES. It counts first — an indexed
// count over the same predicate — and returns errTooWide rather than materialise
// more than `cap` rows. A window a tenant can narrow is a better outcome than a
// shared pod that a single tenant can make allocate a hundred million rows.
func (s *store) window(ctx context.Context, from, to time.Time, cap int) ([]Fact, error) {
	var n int
	if err := s.db.QueryRowContext(ctx,
		`SELECT count(*) FROM assert WHERE at >= ? AND at < ?`, from.Unix(), to.Unix()).Scan(&n); err != nil {
		return nil, err
	}
	if n > cap {
		return nil, fmt.Errorf("%w: the window holds %d assertions and the bound is %d", errTooWide, n, cap)
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT `+cols+`
FROM assert WHERE at >= ? AND at < ? ORDER BY at ASC`, from.Unix(), to.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scan(rows)
}

// errTooWide names a read whose answer would only be correct if it were whole,
// asked over a set too large to hold. It is a sentinel rather than a string so
// the op layer can turn it into a refusal the caller can act on — narrow the
// window — instead of a 500 that says the plane is broken when it is not.
var errTooWide = errors.New("the read is wider than the bound")

// cursor is how far the derived copy has caught up: the seq of the LAST
// assertion it took. Zero is a tenant that has delivered nothing, which is the
// whole history pending — the correct reading for a store that predates delivery
// tracking, and why there is no migration: it re-delivers itself and the columnar
// copy collapses whatever it already held.
//
// IT IS THE STORE'S OWN POSITION AND NOT A RECONSTRUCTION OF ONE. The first cut
// of this cursor was the pair (wrote, id) over a `wrote` truncated to the second
// and an id that is a content digest. That order is not the order rows commit in:
// a write that commits AFTER a concurrent delivery has read, whose digest happens
// to sort lower inside the same second, is already BEHIND the mark the delivery
// then sets. It is never mirrored, no retry can reach it — the mark only moves
// forward — and pending() reports zero, so the hole is both permanent and
// invisible. A hole in the answer key reads as an honest customer.
//
// Nothing derived from a clock can fix that, because the defect is not resolution:
// two rows can commit inside any instant, and their digests carry no order. The
// order has to be the one the writer actually took, which is the one the writer
// assigns. seq is that value.
type cursor int64

// after is the predicate naming everything past the cursor, with its binding.
func (c cursor) after() (string, []any) { return "seq > ?", []any{int64(c)} }

// undelivered reads the assertions the derived copy does not hold yet: everything
// past the cursor, in delivery order, bounded.
func (s *store) undelivered(ctx context.Context, c cursor, limit int) ([]Fact, error) {
	where, args := c.after()
	rows, err := s.db.QueryContext(ctx, `
SELECT `+cols+`
FROM assert WHERE `+where+` ORDER BY seq ASC LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scan(rows)
}

// mark reads the delivery cursor.
func (s *store) mark(ctx context.Context) (cursor, error) {
	var c cursor
	err := s.db.QueryRowContext(ctx, `SELECT seq FROM delivery WHERE only = 1`).Scan(&c)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return c, nil
}

// advance moves the cursor to the last assertion the derived copy took. It never
// moves backwards — a bounded delivery that read an older batch must not
// un-deliver a newer one — and forward is now a comparison of two integers.
func (s *store) advance(ctx context.Context, c cursor) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO delivery (only, seq) VALUES (1, ?)
ON CONFLICT(only) DO UPDATE SET seq = max(excluded.seq, delivery.seq)`, int64(c))
	return err
}

// pending counts the assertions past the cursor — what the derived copy is still
// to take. It is reported rather than hidden: a warehouse-side training join over
// a tenant with a backlog is joining against an incomplete answer key, and a
// missing fraud label reads exactly like an honest customer.
//
// COUNTED UNDER A CAP, because this runs on a request path and a tenant that has
// never delivered has its whole history past the cursor — a bare count(*) there
// is a full scan of a single-writer file on every write and every coverage read.
// The count stops at the cap and the number saturates, which loses nothing an
// operator uses: zero means caught up, small means nearly, and anything at the
// cap means a backlog to work through. It stays EXACT where exactness is the
// point — a delivery that just succeeded must report none left, or the number is
// one an operator learns to ignore.
func (s *store) pending(ctx context.Context, c cursor, cap int) (int, error) {
	where, args := c.after()
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT count(*) FROM (SELECT 1 FROM assert WHERE `+where+` LIMIT ?)`,
		append(args, cap)...).Scan(&n)
	return n, err
}

// expired names what a retention sweep would dispose of, bounded, WITHOUT
// disposing of it. Identifying first is what lets the derived copy be removed
// before the record: the ids are the value both deletes bind, so the two planes
// dispose of the same rows or neither does.
//
// It measures against `wrote` — the server clock at the write — and not against
// `at` or `seen`, both of which the asserting caller supplies. A tenant that
// could age its own records out by back-dating them would be a tenant that could
// delete a compliance record on demand.
//
// It returns the ids to dispose of, how many records inside the boundary are
// under litigation hold (kept, at any age), and how many disposable records are
// still older than the boundary beyond this batch.
func (s *store) expired(ctx context.Context, before time.Time, limit int) (ids []string, held, remaining int, err error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id FROM assert WHERE wrote < ? AND hold = 0 ORDER BY wrote ASC LIMIT ?`,
		before.Unix(), limit)
	if err != nil {
		return nil, 0, 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, 0, 0, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, 0, err
	}
	var disposable int
	if err := s.db.QueryRowContext(ctx,
		`SELECT count(*) FROM assert WHERE wrote < ? AND hold = 0`, before.Unix()).Scan(&disposable); err != nil {
		return nil, 0, 0, err
	}
	if err := s.db.QueryRowContext(ctx,
		`SELECT count(*) FROM assert WHERE wrote < ? AND hold = 1`, before.Unix()).Scan(&held); err != nil {
		return nil, 0, 0, err
	}
	return ids, held, disposable - len(ids), nil
}

// remove disposes of named records, whole. It is the only statement in this
// package that deletes, it takes ids and not a predicate, and it re-asserts the
// hold in the WHERE clause: a record placed under hold between the identify and
// the delete is still not disposed of.
//
// IT REPORTS WHAT IT KEPT, and that return is the whole point of the re-assertion
// rather than an accessory to it. The disposal removes the derived copy FIRST (see
// dispose) so nothing is orphaned in the warehouse — which means a record this
// statement declines to delete has ALREADY been swept from the warehouse, its seq
// is already behind the delivery cursor, and no retry re-sends it: the record
// survives in the tenant's own file and is permanently absent from the copy a
// training join reads, with pending() answering zero. A hole in the answer key
// reads as an honest customer, and the row it happens to is the one somebody is
// litigating. Returning the kept ids is what lets the caller repair the copy and
// count the disposal honestly; discarding them made both impossible.
func (s *store) remove(ctx context.Context, ids []string) (kept []string, err error) {
	if len(ids) == 0 {
		return nil, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.PrepareContext(ctx, `DELETE FROM assert WHERE id = ? AND hold = 0`)
	if err != nil {
		return nil, err
	}
	defer stmt.Close()
	for _, id := range ids {
		res, err := stmt.ExecContext(ctx, id)
		if err != nil {
			return nil, err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return nil, err
		}
		if n == 0 {
			kept = append(kept, id)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return kept, nil
}

// byIDs reads named records whole, for the one caller that needs the assertions
// back rather than their ids: a disposal repairing the derived copy for records a
// litigation hold kept. Chunked on the same ceiling forSubjects uses, and for the
// same reason — the host-variable limit is a property of whichever SQLite the
// build linked, and this must not depend on it.
func (s *store) byIDs(ctx context.Context, ids []string) ([]Fact, error) {
	var out []Fact
	for chunk := range chunks(ids, subjectChunk) {
		holes := make([]string, len(chunk))
		args := make([]any, 0, len(chunk))
		for i, id := range chunk {
			holes[i] = "?"
			args = append(args, id)
		}
		rows, err := s.db.QueryContext(ctx,
			`SELECT `+cols+` FROM assert WHERE id IN (`+strings.Join(holes, ",")+`)`, args...)
		if err != nil {
			return nil, err
		}
		got, err := scan(rows)
		_ = rows.Close()
		if err != nil {
			return nil, err
		}
		out = append(out, got...)
	}
	return out, nil
}

// setHold places or releases a litigation hold on named records. It is the ONE
// statement in this package that changes an existing row, and what it changes is
// not the assertion: `hold` says whether retention may dispose of the record, and
// nothing about what was asserted. That is why it is not in the content digest —
// re-filing the same assertion with a hold flag produced the same id, INSERT OR
// IGNORE dropped it, and the caller was told `duplicate` while the hold it asked
// for was silently not placed. A hold that can be requested and not applied is
// worse than no hold at all: it is a compliance control that reports success.
//
// It reports how many rows CHANGED and how many of the named ids the tenant holds
// at all, so a caller can tell "already held" from "not mine" — the second being
// the case a hold placed against an id from another tenant's response would hit,
// and it lands here as `present` short of the ask rather than as any kind of
// reach into a neighbour's file. There is no file to reach: the statement runs
// against this tenant's own.
func (s *store) setHold(ctx context.Context, ids []string, on bool) (changed, present int, err error) {
	if len(ids) == 0 {
		return 0, 0, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, err
	}
	defer func() { _ = tx.Rollback() }()
	upd, err := tx.PrepareContext(ctx, `UPDATE assert SET hold = ? WHERE id = ? AND hold <> ?`)
	if err != nil {
		return 0, 0, err
	}
	defer upd.Close()
	has, err := tx.PrepareContext(ctx, `SELECT 1 FROM assert WHERE id = ?`)
	if err != nil {
		return 0, 0, err
	}
	defer has.Close()
	want := boolInt(on)
	for _, id := range ids {
		res, err := upd.ExecContext(ctx, want, id, want)
		if err != nil {
			return 0, 0, err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return 0, 0, err
		}
		changed += int(n)
		var one int
		switch err := has.QueryRowContext(ctx, id).Scan(&one); {
		case err == nil:
			present++
		case err == sql.ErrNoRows:
		default:
			return 0, 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, err
	}
	return changed, present, nil
}

// held counts the records this tenant is holding, at any age.
func (s *store) held(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM assert WHERE hold = 1`).Scan(&n)
	return n, err
}

// count reports how many assertions the tenant holds and the oldest write. The
// retention op answers with both, so a disposal that removed nothing is
// distinguishable from a tenant that had nothing.
func (s *store) count(ctx context.Context) (int64, time.Time, error) {
	var n int64
	var oldest sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT count(*), min(wrote) FROM assert`).Scan(&n, &oldest)
	if err != nil {
		return 0, time.Time{}, err
	}
	if !oldest.Valid {
		return n, time.Time{}, nil
	}
	return n, time.Unix(oldest.Int64, 0).UTC(), nil
}

func scan(rows *sql.Rows) ([]Fact, error) {
	var out []Fact
	for rows.Next() {
		var f Fact
		var at, seen, knowable, wrote int64
		var kind, disp, src string
		var hold int
		if err := rows.Scan(&f.Seq, &f.ID, &kind, &f.Subject, &at, &seen, &knowable, &disp, &src,
			&f.Evidence, &f.By, &f.Confidence, &hold, &wrote); err != nil {
			return nil, err
		}
		f.Kind = Kind(kind)
		f.Disposition = Disposition(disp)
		f.Source = Source(src)
		f.At = time.Unix(at, 0).UTC()
		f.Seen = time.Unix(seen, 0).UTC()
		f.Knowable = time.Unix(knowable, 0).UTC()
		f.Wrote = time.Unix(wrote, 0).UTC()
		f.Hold = hold != 0
		out = append(out, f)
	}
	return out, rows.Err()
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
