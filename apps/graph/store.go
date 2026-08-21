package graph

// store.go is one tenant's assertion plane: ONE table, opened through
// cloud.OrgDB, so another organization's assertions are not in the database
// being read and no predicate can be forgotten.
//
// THERE IS NO UPDATE STATEMENT IN THIS PACKAGE, and no DELETE outside disposal.
// A retraction is an assertion, which is what lets a reader see that something
// was retracted rather than find that it is gone.

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// schema is one table. `names` is the bit that makes an edge an edge, and the
// two indexes are the two directions a walk reads.
//
// `by` is double-quoted at every use site: it is a SQLite keyword, and the wire
// field, the Go field and the column are deliberately the SAME word rather than
// three spellings of one fact.
//
// seq is AUTOINCREMENT, not a bare rowid: a bare rowid is max+1 and is REUSED
// after the top row is deleted, so a disposal sweep that emptied the table would
// restart the sequence below a cursor that had advanced.
const schema = `
CREATE TABLE IF NOT EXISTS assertion (
	seq        INTEGER PRIMARY KEY AUTOINCREMENT,
	id         TEXT NOT NULL UNIQUE,
	entity     TEXT NOT NULL,
	relation   TEXT NOT NULL,
	value      TEXT NOT NULL,
	names      INTEGER NOT NULL DEFAULT 0,
	at         INTEGER NOT NULL,
	seen       INTEGER NOT NULL,
	knowable   INTEGER NOT NULL,
	source     TEXT NOT NULL,
	evidence   TEXT NOT NULL DEFAULT '',
	"by"       TEXT NOT NULL DEFAULT '',
	confidence REAL NOT NULL DEFAULT 0,
	hold       INTEGER NOT NULL DEFAULT 0,
	wrote      INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS assertion_out ON assertion(entity, relation);
CREATE INDEX IF NOT EXISTS assertion_in ON assertion(value, relation);
CREATE INDEX IF NOT EXISTS assertion_rel ON assertion(relation);
CREATE INDEX IF NOT EXISTS assertion_wrote ON assertion(wrote);
`

// cols is the projection every read shares, in the order scan consumes. One
// spelling, so a column added to the schema is added to the readers in one place
// or in none.
const cols = `seq, id, entity, relation, value, names, at, seen, knowable, source, evidence, "by", confidence, hold, wrote`

// walkBound is the ceiling on one traversal, and it is the number
// apps/knowledge/graph.go already renders with. An unbounded walk is not a slow
// request: because the store holds one connection, it blocks every other write
// for that organization until it finishes.
const walkBound = 10000

type store struct{ db *sql.DB }

// openStore is cloud.OrgStore's open func: the file is already opened, pragma'd
// and keyed by the time it arrives here, so this only migrates.
func openStore(db *sql.DB) (*store, error) {
	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("graph: migrate: %w", err)
	}
	return &store{db: db}, nil
}

// record writes assertions. It is idempotent by content: a redelivered assertion
// collides on the digest and is ignored, so a retrying caller appends nothing.
// Each member is judged on its own — one refusal does not discard the rest.
func (s *store) record(ctx context.Context, facts []Fact) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	stmt, err := tx.PrepareContext(ctx, `INSERT OR IGNORE INTO assertion
		(id, entity, relation, value, names, at, seen, knowable, source, evidence, "by", confidence, wrote)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return 0, err
	}
	defer func() { _ = stmt.Close() }()

	n := 0
	for _, f := range facts {
		res, err := stmt.ExecContext(ctx, f.ID, f.Entity, f.Relation, f.Value, f.Names,
			f.At.UTC().Unix(), f.Seen.UTC().Unix(), f.Knowable.UTC().Unix(),
			f.Source, f.Evidence, f.By, f.Confidence, f.Wrote.UTC().Unix())
		if err != nil {
			return n, err
		}
		if a, _ := res.RowsAffected(); a > 0 {
			n++
		}
	}
	return n, tx.Commit()
}

// filter selects assertions. Every term is optional and every one is bound
// positionally, never interpolated.
type filter struct {
	Entity   string
	Relation string
	Value    string
	AsOf     time.Time
	Limit    int
	// Newest orders the read by descending sequence, so a read that hits the
	// ceiling keeps the most recent assertions rather than the oldest. A
	// resolution reads this way: the table only grows, a correction is a row,
	// and the rows that decide what is in force are the last ones written.
	Newest bool
}

func (s *store) read(ctx context.Context, f filter) ([]Fact, error) {
	q := `SELECT ` + cols + ` FROM assertion WHERE 1=1`
	var args []any
	if f.Entity != "" {
		q += ` AND entity = ?`
		args = append(args, f.Entity)
	}
	if f.Relation != "" {
		q += ` AND relation = ?`
		args = append(args, f.Relation)
	}
	if f.Value != "" {
		q += ` AND value = ?`
		args = append(args, f.Value)
	}
	if !f.AsOf.IsZero() {
		q += ` AND knowable <= ?`
		args = append(args, f.AsOf.UTC().Unix())
	}
	limit := f.Limit
	if limit <= 0 || limit > walkBound {
		limit = walkBound
	}
	if f.Newest {
		q += ` ORDER BY seq DESC LIMIT ?`
	} else {
		q += ` ORDER BY seq LIMIT ?`
	}
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return scan(rows)
}

// step is one hop of a walk, named for the direction it reads.
type step struct {
	from, next string
}

var steps = map[string]step{
	"out":  {"entity", "value"},
	"in":   {"value", "entity"},
	"both": {},
}

// walk is a bounded traversal from a seed set, and the bound is part of the
// answer: the caller is told when it truncated rather than handed a short answer
// that looks complete.
//
// Only EDGES are traversed (names = 1) and only assertions knowable at the
// instant, so a walk is a point-in-time read for free: the same seeds at a past
// instant answer what the graph looked like then.
func (s *store) walk(ctx context.Context, seeds []string, relation, direction string, depth int, asOf time.Time) ([]string, int, bool, error) {
	sp, ok := steps[direction]
	if !ok {
		return nil, 0, false, fmt.Errorf("direction %q is not one of out, in, both", direction)
	}
	if len(seeds) == 0 {
		return nil, 0, false, fmt.Errorf("a walk needs at least one seed")
	}
	if depth <= 0 {
		depth = 1
	}

	var args []any
	seedSel := make([]string, 0, len(seeds))
	for _, s := range seeds {
		seedSel = append(seedSel, `SELECT ? AS node, 0 AS depth`)
		args = append(args, s)
	}

	// hop is the edge relation the walk follows, oriented by direction. `both`
	// is the union of the two orientations rather than a third rule.
	edge := func(from, next string) string {
		q := `SELECT ` + from + ` AS from_node, ` + next + ` AS next FROM assertion WHERE names = 1`
		if relation != "" {
			q += ` AND relation = ?`
		}
		if !asOf.IsZero() {
			q += ` AND knowable <= ?`
		}
		return q
	}
	bind := func() {
		if relation != "" {
			args = append(args, relation)
		}
		if !asOf.IsZero() {
			args = append(args, asOf.UTC().Unix())
		}
	}

	var hop string
	if direction == "both" {
		hop = edge("entity", "value")
		bind()
		hop += ` UNION ALL ` + edge("value", "entity")
		bind()
	} else {
		hop = edge(sp.from, sp.next)
		bind()
	}

	q := `WITH RECURSIVE walk(node, depth) AS (
		` + strings.Join(seedSel, " UNION ") + `
		UNION
		SELECT hop.next, walk.depth + 1 FROM walk
		JOIN (` + hop + `) hop ON hop.from_node = walk.node
		WHERE walk.depth < ?
	)
	SELECT node, MIN(depth) AS d FROM walk GROUP BY node ORDER BY d, node LIMIT ?`
	args = append(args, depth, walkBound+1)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, 0, false, err
	}
	defer func() { _ = rows.Close() }()

	var out []string
	deepest := 0
	for rows.Next() {
		var node string
		var d int
		if err := rows.Scan(&node, &d); err != nil {
			return nil, 0, false, err
		}
		out = append(out, node)
		if d > deepest {
			deepest = d
		}
	}
	if err := rows.Err(); err != nil {
		return nil, 0, false, err
	}
	if len(out) > walkBound {
		return out[:walkBound], deepest, true, nil
	}
	return out, deepest, false, nil
}

func scan(rows *sql.Rows) ([]Fact, error) {
	var out []Fact
	for rows.Next() {
		var f Fact
		var at, seen, knowable, wrote int64
		if err := rows.Scan(&f.Seq, &f.ID, &f.Entity, &f.Relation, &f.Value, &f.Names,
			&at, &seen, &knowable, &f.Source, &f.Evidence, &f.By, &f.Confidence, &f.Hold, &wrote); err != nil {
			return nil, err
		}
		f.At = time.Unix(at, 0).UTC()
		f.Seen = time.Unix(seen, 0).UTC()
		f.Knowable = time.Unix(knowable, 0).UTC()
		f.Wrote = time.Unix(wrote, 0).UTC()
		out = append(out, f)
	}
	return out, rows.Err()
}

// Close releases the tenant's handle. cloud.OrgStore calls it on shutdown.
func (s *store) Close() error { return s.db.Close() }

// relations is the vocabulary this organization has actually asserted. There is
// no declared vocabulary to read instead: the relations in use ARE the ontology,
// which is why no separate schema store exists.
func (s *store) relations(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT relation FROM assertion ORDER BY relation LIMIT ?`, walkBound)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var r string
		if err := rows.Scan(&r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
