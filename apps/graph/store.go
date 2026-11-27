package graph

// store.go is one tenant's assertion plane: ONE table, opened through
// cloud.OrgDB, so another organization's assertions are not in the database
// being read and no predicate can be forgotten.
//
// THERE IS NO UPDATE STATEMENT IN THIS PACKAGE, and one DELETE: erase, the right
// to erasure. A retraction is an assertion, which is what lets a reader see that
// something was retracted rather than find that it is gone. Erasure is the one
// case where "gone" is the requirement, and it is kept to that case (erase.go).

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud/sdk/dialect"
)

// schema is one table. `names` is the bit that makes an edge an edge, the first
// two indexes are the two directions a walk reads, and `at` and `until` are the
// two a diff reads: what began or ended between two valid instants.
//
// `by` is double-quoted at every use site: it is a SQLite keyword, and the wire
// field, the Go field and the column are deliberately the SAME word rather than
// three spellings of one fact.
//
// seq is the engine's identity, not a bare rowid: a bare rowid is max+1 and is
// REUSED after the top row is deleted, so a disposal sweep that emptied the table
// would restart the sequence below a cursor that had advanced. On PostgreSQL a
// redelivered assertion, which collides on its digest, still spends a number, so
// seq has gaps there; it orders and is never counted.
//
// entity, relation and value are Bytewise: a prefix is read as a range of them
// (scoped, laws), and a range of text is a range of bytes only if the column
// compares by bytes.
func schema(d dialect.Dialect) string {
	return `
CREATE TABLE IF NOT EXISTS assertion (
	seq        ` + d.Identity() + `,
	id         TEXT NOT NULL UNIQUE,
	entity     TEXT NOT NULL ` + d.Bytewise() + `,
	relation   TEXT NOT NULL ` + d.Bytewise() + `,
	value      TEXT NOT NULL ` + d.Bytewise() + `,
	names      BIGINT NOT NULL DEFAULT 0,
	at         BIGINT NOT NULL,
	until      BIGINT,
	seen       BIGINT NOT NULL,
	knowable   BIGINT NOT NULL,
	source     TEXT NOT NULL,
	evidence   TEXT NOT NULL DEFAULT '',
	"by"       TEXT NOT NULL DEFAULT '',
	confidence DOUBLE PRECISION NOT NULL DEFAULT 0,
	hold       BIGINT NOT NULL DEFAULT 0,
	wrote      BIGINT NOT NULL
);
CREATE INDEX IF NOT EXISTS assertion_out ON assertion(entity, relation);
CREATE INDEX IF NOT EXISTS assertion_in ON assertion(value, relation);
CREATE INDEX IF NOT EXISTS assertion_rel ON assertion(relation);
CREATE INDEX IF NOT EXISTS assertion_wrote ON assertion(wrote);
CREATE INDEX IF NOT EXISTS assertion_knowable ON assertion(knowable);
`
}

// spans are the indexes on the valid-time columns. They are created after
// migrate, because a file written before statements could end has no `until`
// until migrate gives it one.
const spans = `
CREATE INDEX IF NOT EXISTS assertion_at ON assertion(at);
CREATE INDEX IF NOT EXISTS assertion_until ON assertion(until);
`

// cols is the projection every read shares, in the order scan consumes. One
// spelling, so a column added to the schema is added to the readers in one place
// or in none.
const cols = `seq, id, entity, relation, value, names, at, until, seen, knowable, source, evidence, "by", confidence, hold, wrote`

// The three ceilings on one traversal, and they bound the WORK rather than only
// the answer. Because the store holds one connection, a walk that runs long does
// not merely answer slowly — it blocks every other write for that organization
// until it finishes, so a caller-supplied depth or seed count is a caller-supplied
// hold on the org's write path.
//
// walkBound is the ceiling on nodes returned, and it is the number
// apps/knowledge/graph.go already renders with. It is also the ceiling on the
// rows one statement of a walk reads, so a node with more history than that
// stops the walk and says so rather than resolving from part of it. depthMax and
// seedMax bound the two terms the caller sizes: the hop count, and the width a
// hop starts from. Every one of them is reported when it binds, because a
// truncated walk that says nothing is a wrong answer wearing a right one.
const (
	walkBound = 10000
	depthMax  = 32
	// seedMax is the width of one statement. A hop reads its level seedMax
	// nodes at a time, and a caller's seeds or relations above it are refused by
	// name rather than handed to the engine as one oversized statement.
	seedMax = 256
)

type store struct {
	db *sql.DB
	d  dialect.Dialect
	// words is the full-text view of the same rows, in the same database, so a
	// search inherits the tenancy the assertions already have: there is no second
	// store to keep in step and nothing to re-scope on the way out. It holds no
	// copy of the text that a read could come from; every read still comes from
	// `assertion` through `cols`, because a copy would be a second place for one
	// fact to live and a way for the two to disagree.
	words dialect.Words
	// q is what every read runs on: db, or inside write the transaction a batch
	// is judged in.
	q querier
}

// querier is a handle or a transaction on it.
type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// openStore is cloud.OrgStore's open func: the database is already opened, pragma'd
// and keyed by the time it arrives here, so this only migrates. It is one migration,
// under the store's lock, so two replicas opening one store do not race it.
func openStore(db *sql.DB) (*store, error) {
	d := dialect.Of(db)
	words, err := d.Words("assertion", "seq", "entity", "relation", "value", "source", "evidence")
	if err != nil {
		return nil, fmt.Errorf("graph: index: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := d.Migrate(ctx, db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, schema(d)); err != nil {
			return err
		}
		if err := migrate(ctx, d, tx); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, spans); err != nil {
			return err
		}
		// The index is made last, from the rows the table already holds: a trigger
		// only sees what arrives after it, so a file written before the index
		// existed would search as though it were empty, which is worse than an
		// error because it answers.
		return words.Create(ctx, tx)
	}); err != nil {
		return nil, fmt.Errorf("graph: migrate: %w", err)
	}
	return &store{db: db, d: d, words: words, q: db}, nil
}

// migrate gives a file written before statements could end its `until` column.
// Every row it had is open, which is what NULL says, so no row is rewritten.
//
// NULL and not zero: zero is 1970-01-01T00:00:00Z, the end of every term that
// ran through 1969, and a sentinel that is also an instant reads those terms
// as never ending.
func migrate(ctx context.Context, d dialect.Dialect, tx *sql.Tx) error {
	has, err := d.HasColumn(ctx, tx, "assertion", "until")
	if err != nil || has {
		return err
	}
	_, err = tx.ExecContext(ctx, `ALTER TABLE assertion ADD COLUMN until BIGINT`)
	return err
}

// write files the assertions judge admits, in ONE transaction. judge reads the
// store through it, so the store a verdict is reached on is the store the insert
// changes: no write lands between the two, and two batches each admissible alone
// are never both admitted against the store without the other. It is idempotent
// by content: a redelivered assertion collides on the digest and is ignored, so
// a retrying caller appends nothing.
//
// witness runs inside the transaction too, after the inserts and before the
// commit, and is handed the assertions that became new rows, so a failure to
// record them is a rollback and never an unrecorded write — the order an
// erasure keeps (erase).
func (s *store) write(ctx context.Context, judge func(in *store) ([]Fact, error), witness func(fresh []Fact) error) (int, error) {
	tx, err := s.d.BeginWrite(ctx, s.db)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	facts, err := judge(&store{d: s.d, words: s.words, q: tx})
	if err != nil {
		return 0, err
	}
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO assertion
		(id, entity, relation, value, names, at, until, seen, knowable, source, evidence, "by", confidence, wrote)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
		ON CONFLICT (id) DO NOTHING RETURNING seq`)
	if err != nil {
		return 0, err
	}
	defer func() { _ = stmt.Close() }()

	var fresh []Fact
	for _, f := range facts {
		var seq int64
		err := stmt.QueryRowContext(ctx, f.ID, f.Entity, f.Relation, f.Value, flag(f.Names),
			f.At.UTC().Unix(), end(f.Until), f.Seen.UTC().Unix(), f.Knowable.UTC().Unix(),
			f.Source, f.Evidence, f.By, f.Confidence, f.Wrote.UTC().Unix()).Scan(&seq)
		if errors.Is(err, sql.ErrNoRows) {
			continue // the digest was already filed
		}
		if err != nil {
			return len(fresh), err
		}
		fresh = append(fresh, f)
	}
	if err := witness(fresh); err != nil {
		return 0, err
	}
	return len(fresh), tx.Commit()
}

// filter selects assertions. Every term is optional and every one is bound
// positionally, never interpolated.
type filter struct {
	Entity   string
	Relation string
	// Value matches a property's scalar byte for byte and an edge's value by
	// its key: only the row says which a value is, so both are asked.
	Value string
	// Valid bounds the read to statements begun by an instant, `at <= Valid`;
	// Known to what this plane had heard by one, `knowable <= Known`. Zero is
	// unbounded. A statement begun after an instant can neither hold there nor
	// end one that does before it, so a resolution loses nothing to either.
	Valid time.Time
	Known time.Time
	Limit int
	// Newest orders the read by the statements begun most recently, so a read
	// that hits the ceiling keeps those rather than the oldest: they are what a
	// resolution at the bound decides between.
	Newest bool
	// Match narrows to the rows whose text matches these terms (see [terms]): all
	// of them, or any one when Any is set. It is a TERM of this filter and not a
	// read of its own, so searching composes with every other narrowing here under
	// the same ceiling. It also ORDERS the read, best match first and oldest first
	// among equals, so a read that hits the ceiling keeps the rows that match
	// best rather than the rows filed first. A caller's words are text, never
	// query syntax.
	Match []string
	Any   bool
}

func (s *store) read(ctx context.Context, f filter) ([]Fact, error) {
	from := `assertion`
	var args []any
	if len(f.Match) > 0 {
		// The index's hits are joined as a table of their own publishing only a
		// key and a score. Joined whole, the index would publish its columns
		// under the table's own names, and every one of them in the unqualified
		// `cols` would be ambiguous.
		hits, hargs := s.words.Hits(1, f.Match, !f.Any)
		from = `assertion JOIN (` + hits + `) h ON h.hit = seq`
		args = append(args, hargs...)
	}
	q := `SELECT ` + cols + ` FROM ` + from + ` WHERE 1=1`
	if f.Entity != "" {
		args = append(args, f.Entity)
		q += ` AND entity = ` + mark(args)
	}
	if f.Relation != "" {
		args = append(args, f.Relation)
		q += ` AND relation = ` + mark(args)
	}
	if f.Value != "" {
		args = append(args, f.Value, key(f.Value))
		q += ` AND ((names = 0 AND value = $` + strconv.Itoa(len(args)-1) + `) OR (names = 1 AND value = ` + mark(args) + `))`
	}
	if !f.Valid.IsZero() {
		args = append(args, f.Valid.UTC().Unix())
		q += ` AND at <= ` + mark(args)
	}
	if !f.Known.IsZero() {
		args = append(args, f.Known.UTC().Unix())
		q += ` AND knowable <= ` + mark(args)
	}
	limit := f.Limit
	if limit <= 0 || limit > walkBound {
		limit = walkBound
	}
	lim, args := bound(args, limit)
	switch {
	case len(f.Match) > 0:
		q += ` ORDER BY h.score, seq` + lim
	case f.Newest:
		q += ` ORDER BY at DESC, seq DESC` + lim
	default:
		q += ` ORDER BY seq` + lim
	}
	return s.query(ctx, q, args...)
}

// terms is what a caller typed as the terms of a search: every word one term,
// all of them required, because narrowing is what more words are for. An answer's
// rank asks for any one instead, since a question is not a filter.
//
// An empty result means there was nothing to search for; the caller decides what
// to do about that, since a filter with no term matches everything.
//
// The words are text: the index quotes them for its engine, so none of them is
// query syntax, and none of the punctuation entity keys are full of (slashes,
// dashes, colons) can be an error. Each matches as the beginning of a word, because
// the caller is typing a search and expects `depl` to find `deploy`.
func terms(q string) []string { return strings.Fields(q) }

// mark is the placeholder of the argument just appended to args.
func mark(args []any) string { return `$` + strconv.Itoa(len(args)) }

// bound appends a row ceiling to args and answers the clause that reads it.
func bound(args []any, n int) (string, []any) {
	args = append(args, n)
	return ` LIMIT ` + mark(args), args
}

// flag is a boolean as the integer the column keeps.
func flag(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// hop is one in-force edge a walk crossed, from a node it had reached to the
// next one. The edge is kept whole, so a path is its provenance.
type hop struct {
	from, next string
	edge       Fact
}

// walked is what a walk reached and how it got there.
type walked struct {
	// nodes is everything reached, the seeds included, by the fewest hops that
	// reach each one and then by key.
	nodes []string
	// by is the hop each node past the seeds was FIRST reached over. Levels are
	// read in order, so following it back from any node is a shortest path.
	by        map[string]hop
	deepest   int
	truncated bool
}

// walk is a bounded traversal from a seed set, and the bound is part of the
// answer: the caller is told when it truncated rather than handed a short answer
// that looks complete.
//
// Only IN-FORCE edges are crossed: every edge a pair holds at the point, by the
// rule lives states, so an ended edge is not the graph as it stood and a
// retracted one is not there at all. A walk is a point-in-time read for free —
// the same seeds at a past point answer what the graph looked like then, as far
// as was known then.
//
// It is ONE traversal. neighbors reads what it reached; path stops it at a goal
// and reads the walk back. goal ends the walk at the level that first reaches
// it; empty walks to depth.
func (s *store) walk(ctx context.Context, seeds, relations []string, direction string, depth int, w when, goal string) (walked, error) {
	switch {
	case direction != "out" && direction != "in" && direction != "both":
		return walked{}, fmt.Errorf("direction %q is not one of out, in, both", direction)
	case len(seeds) == 0:
		return walked{}, fmt.Errorf("a walk needs at least one seed")
	case len(seeds) > seedMax:
		return walked{}, fmt.Errorf("a walk starts from at most %d seeds, not %d", seedMax, len(seeds))
	case len(relations) > seedMax:
		return walked{}, fmt.Errorf("a walk follows at most %d relations, not %d", seedMax, len(relations))
	}
	if depth <= 0 {
		depth = 1
	}
	if depth > depthMax {
		return walked{}, fmt.Errorf("a walk runs at most %d hops, not %d", depthMax, depth)
	}

	t := walked{nodes: slices.Compact(slices.Sorted(slices.Values(seeds))), by: map[string]hop{}}
	reached := make(map[string]bool, len(t.nodes))
	for _, n := range t.nodes {
		reached[n] = true
	}
	level := t.nodes
	for d := 1; d <= depth && len(level) > 0 && (goal == "" || !reached[goal]); d++ {
		hops, cut, err := s.hops(ctx, level, relations, direction, w)
		if err != nil {
			return walked{}, err
		}
		if cut {
			t.truncated = true
			break
		}
		// Sorted before they are claimed, so which of two equally short hops
		// reaches a node is decided by the edges and not by the engine's row order.
		first := map[string]hop{}
		var next []string
		for _, h := range hops {
			if reached[h.next] {
				continue
			}
			if _, ok := first[h.next]; !ok {
				first[h.next] = h
				next = append(next, h.next)
			}
		}
		slices.Sort(next)
		if room := walkBound - len(t.nodes); len(next) > room {
			next, t.truncated = next[:room], true
		}
		for _, n := range next {
			reached[n] = true
			t.by[n] = first[n]
		}
		if len(next) > 0 {
			t.nodes = append(t.nodes, next...)
			t.deepest = d
		}
		if t.truncated {
			break
		}
		level = next
	}
	return t, nil
}

// hops is every in-force edge touching a level, oriented by direction. `both` is
// the union of the two orientations rather than a third rule. cut says a read
// hit its ceiling, and then nothing it returned is trusted.
func (s *store) hops(ctx context.Context, level, relations []string, direction string, w when) ([]hop, bool, error) {
	var out []hop
	if direction != "in" {
		edges, cut, err := s.inforce(ctx, level, relations, w)
		if err != nil || cut {
			return nil, cut, err
		}
		for _, e := range edges {
			out = append(out, hop{from: e.Entity, next: e.Value, edge: e})
		}
	}
	if direction != "out" {
		// An edge INTO the level is won or lost on its own entity's pair, so the
		// entities pointing here are found first and then resolved like any other.
		from, cut, err := s.pointing(ctx, level, relations, w)
		if err != nil || cut {
			return nil, cut, err
		}
		edges, cut, err := s.inforce(ctx, from, relations, w)
		if err != nil || cut {
			return nil, cut, err
		}
		at := make(map[string]bool, len(level))
		for _, n := range level {
			at[n] = true
		}
		for _, e := range edges {
			if at[e.Value] {
				out = append(out, hop{from: e.Value, next: e.Entity, edge: e})
			}
		}
	}
	slices.SortFunc(out, func(a, b hop) int {
		return cmp.Or(cmp.Compare(a.from, b.from), cmp.Compare(a.edge.Relation, b.edge.Relation), cmp.Compare(a.next, b.next))
	})
	return out, false, nil
}

// inforce is the edges in force out of a set of entities at a point: every edge
// each of their pairs holds there. A pair that never held an edge cannot hold one
// and is not read. A pair that did is read WHOLE, properties included, because a
// property can end its edges — that is what a retraction is.
func (s *store) inforce(ctx context.Context, entities, relations []string, w when) ([]Fact, bool, error) {
	var read []Fact
	for chunk := range slices.Chunk(entities, seedMax) {
		args := append(anys(chunk), w.valid.Unix(), w.known.Unix())
		valid, known := `$`+strconv.Itoa(len(args)-1), mark(args)
		q := `SELECT ` + cols + ` FROM assertion a WHERE entity IN (` + dialect.Marks(1, len(chunk)) + `) AND at <= ` + valid + ` AND knowable <= ` + known
		if len(relations) > 0 {
			q += ` AND relation IN (` + dialect.Marks(len(args)+1, len(relations)) + `)`
			args = append(args, anys(relations)...)
		}
		q += ` AND EXISTS (SELECT 1 FROM assertion e WHERE e.entity = a.entity AND e.relation = a.relation
			AND e.names = 1 AND e.at <= ` + valid + ` AND e.knowable <= ` + known + `)`
		lim, args := bound(args, walkBound+1)
		facts, err := s.query(ctx, q+` ORDER BY seq`+lim, args...)
		if err != nil {
			return nil, false, err
		}
		if len(facts) > walkBound {
			return nil, true, nil
		}
		read = append(read, facts...)
	}
	card, err := s.arity(ctx, relationsOf(read), w.known)
	if err != nil {
		return nil, false, err
	}
	return edges(read, w, card), false, nil
}

// pointing is the entities with an edge begun and known by the point whose value
// is in the level. It decides nothing: whether that edge is in force is inforce's.
func (s *store) pointing(ctx context.Context, level, relations []string, w when) ([]string, bool, error) {
	out := map[string]bool{}
	for chunk := range slices.Chunk(level, seedMax) {
		args := append(anys(chunk), w.valid.Unix(), w.known.Unix())
		q := `SELECT DISTINCT entity FROM assertion WHERE names = 1 AND value IN (` + dialect.Marks(1, len(chunk)) + `) AND at <= $` + strconv.Itoa(len(args)-1) + ` AND knowable <= ` + mark(args)
		if len(relations) > 0 {
			q += ` AND relation IN (` + dialect.Marks(len(args)+1, len(relations)) + `)`
			args = append(args, anys(relations)...)
		}
		lim, args := bound(args, walkBound+1)
		rows, err := s.q.QueryContext(ctx, q+lim, args...)
		if err != nil {
			return nil, false, err
		}
		for rows.Next() {
			var e string
			if err := rows.Scan(&e); err != nil {
				_ = rows.Close()
				return nil, false, err
			}
			out[e] = true
		}
		err = rows.Err()
		_ = rows.Close()
		if err != nil {
			return nil, false, err
		}
		if len(out) > walkBound {
			return nil, true, nil
		}
	}
	return slices.Sorted(maps.Keys(out)), false, nil
}

// path is the shortest chain of in-force edges from one entity to another: a
// walk stopped at the goal and read back along the hops that first reached
// each node. found is false when the walk did not reach it; cut says the bound
// stopped the walk before it could.
func (s *store) path(ctx context.Context, from, to string, relations []string, direction string, w when) (edges []Fact, found, cut bool, err error) {
	t, err := s.walk(ctx, []string{from}, relations, direction, depthMax, w, to)
	if err != nil {
		return nil, false, false, err
	}
	if _, ok := t.by[to]; !ok && from != to {
		return nil, false, t.truncated, nil
	}
	for n := to; n != from; n = t.by[n].from {
		edges = append(edges, t.by[n].edge)
	}
	slices.Reverse(edges)
	return edges, true, false, nil
}

// diff is every change to a pair between two points a and b, in key order. b is
// no earlier than a on either clock.
//
// Only a pair with a statement that began or ended between a.valid and b.valid,
// or that became known between a.known and b.known, can differ — any other pair
// holds the same at both points — so those are the pairs read, at most limit of
// them, and each is resolved at both. cut says there were more pairs than that,
// or that one pair holds more statements than a single read returns.
func (s *store) diff(ctx context.Context, entity string, a, b when, limit int) ([]delta, bool, error) {
	q := `SELECT DISTINCT entity, relation FROM assertion WHERE knowable <= $1
		AND ((at > $2 AND at <= $3) OR (until > $2 AND until <= $3) OR (knowable > $4 AND knowable <= $1))`
	args := []any{b.known.Unix(), a.valid.Unix(), b.valid.Unix(), a.known.Unix()}
	if entity != "" {
		args = append(args, entity)
		q += ` AND entity = ` + mark(args)
	}
	lim, args := bound(args, limit+1)
	rows, err := s.q.QueryContext(ctx, q+` ORDER BY entity, relation`+lim, args...)
	if err != nil {
		return nil, false, err
	}
	var pairs []pair
	for rows.Next() {
		var p pair
		if err := rows.Scan(&p.entity, &p.relation); err != nil {
			_ = rows.Close()
			return nil, false, err
		}
		pairs = append(pairs, p)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return nil, false, err
	}
	cut := len(pairs) > limit
	if cut {
		pairs = pairs[:limit]
	}

	rels := make([]string, 0, len(pairs))
	for _, p := range pairs {
		rels = append(rels, p.relation)
	}
	card, err := s.arity(ctx, rels, b.known)
	if err != nil {
		return nil, false, err
	}
	var out []delta
	for _, p := range pairs {
		facts, err := s.read(ctx, filter{Entity: p.entity, Relation: p.relation, Valid: b.valid, Known: b.known, Newest: true})
		if err != nil {
			return nil, false, err
		}
		if len(facts) == walkBound {
			cut = true
		}
		out = append(out, changes(facts, a, b, card.one(facts))...)
	}
	return out, cut, nil
}

func (s *store) query(ctx context.Context, q string, args ...any) ([]Fact, error) {
	rows, err := s.q.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return scan(rows)
}

// end is an end as the store keeps it: NULL when there is none.
func end(t time.Time) sql.NullInt64 {
	return sql.NullInt64{Int64: t.UTC().Unix(), Valid: !t.IsZero()}
}

func anys(xs []string) []any {
	out := make([]any, len(xs))
	for i, x := range xs {
		out[i] = x
	}
	return out
}

func scan(rows *sql.Rows) ([]Fact, error) {
	var out []Fact
	for rows.Next() {
		var f Fact
		var at, seen, knowable, wrote int64
		var until sql.NullInt64
		if err := rows.Scan(&f.Seq, &f.ID, &f.Entity, &f.Relation, &f.Value, &f.Names,
			&at, &until, &seen, &knowable, &f.Source, &f.Evidence, &f.By, &f.Confidence, &f.Hold, &wrote); err != nil {
			return nil, err
		}
		f.At = time.Unix(at, 0).UTC()
		if until.Valid {
			f.Until = time.Unix(until.Int64, 0).UTC()
		}
		f.Seen = time.Unix(seen, 0).UTC()
		f.Knowable = time.Unix(knowable, 0).UTC()
		f.Wrote = time.Unix(wrote, 0).UTC()
		out = append(out, f)
	}
	return out, rows.Err()
}

// scoped reads every assertion under relations that is about something under a
// key prefix: its entity starts with the prefix, or it is an edge whose value
// does. It is the prefix form of naming, and each half is a range read on the
// index its direction walks, named so the planner cannot trade it for a scan of
// every row under the relation.
//
// It reads at most max+1 rows, so a caller tells a scope over max from one at
// it. A restatement diffs against everything held in its scope, and one that
// read part of it would end what it did not see, so it refuses a scope over max
// rather than read part of one.
func (s *store) scoped(ctx context.Context, prefix string, relations []string, max int) ([]Fact, error) {
	// The least key above every key with the prefix: its last byte, plus one.
	// UTF-8 has no 0xff byte, so the increment never carries.
	hi := []byte(prefix)
	hi[len(hi)-1]++
	// The second half reads the first's arguments again: $1 and $2 are the range,
	// $3 on are the relations, and the ceiling is the last.
	in := dialect.Marks(3, len(relations))
	q := `SELECT ` + cols + ` FROM assertion` + s.d.Index("assertion_out") + `
		WHERE entity >= $1 AND entity < $2 AND relation IN (` + in + `)
	UNION
	SELECT ` + cols + ` FROM assertion` + s.d.Index("assertion_in") + `
		WHERE value >= $1 AND value < $2 AND names = 1 AND relation IN (` + in + `)`
	args := append([]any{prefix, string(hi)}, anys(relations)...)
	lim, args := bound(args, max+1)
	return s.query(ctx, q+` ORDER BY seq`+lim, args...)
}

// size is how many assertions the store holds.
func (s *store) size(ctx context.Context) (int, error) {
	rows, err := s.q.QueryContext(ctx, `SELECT count(*) FROM assertion`)
	if err != nil {
		return 0, err
	}
	defer func() { _ = rows.Close() }()
	var n int
	for rows.Next() {
		if err := rows.Scan(&n); err != nil {
			return 0, err
		}
	}
	return n, rows.Err()
}

// naming is the one predicate erasure removes by: the entity as the subject, or
// as the entity an edge points at, under any of its spellings. A PROPERTY whose
// scalar happens to spell the key is not a reference to it and is left alone.
// Its arguments are the spellings, which the predicate reads twice.
func naming(spelled []string) (string, []any) {
	m := dialect.Marks(1, len(spelled))
	return `(entity IN (` + m + `) OR (names = 1 AND value IN (` + m + `)))`, anys(spelled)
}

// erase removes, whole, up to max assertions that name entity, and returns them.
// It matches every spelling the entity may have been filed under (spellings),
// so a subject filed before keys were folded is erased with the rest of it.
//
// It is ALL OR NOTHING against a hold: when any row naming the entity is held,
// it removes nothing and reports how many are, because an erasure that skipped
// the held rows would report a subject erased while the rows under litigation
// still name it.
//
// witness runs inside the transaction, after the delete and before the commit,
// so a failure to record the erasure is a rollback and never an unrecorded one.
//
// On SQLite the content leaves the FILE, not only the table: secure_delete zeroes
// the freed pages, the index's secure-delete drops the terms rather than masking
// them, and the checkpoint takes the pre-erasure frames out of the WAL. A row
// that no query returns but the file still holds is not erased. PostgreSQL has no
// file for a statement to scrub (dialect.Shred), and there the terms are in the
// row's own vector, so the commit removes the text from every read and from the
// index together; what the server's WAL, replicas and backups keep for their
// retention is the server's to bound.
func (s *store) erase(ctx context.Context, entity string, max int, witness func([]Fact) error) (gone []Fact, held, remaining int, err error) {
	tx, err := s.d.BeginWrite(ctx, s.db)
	if err != nil {
		return nil, 0, 0, err
	}
	defer func() { _ = tx.Rollback() }()

	where, args := naming(spellings(entity))
	if err := tx.QueryRowContext(ctx,
		`SELECT count(*) FROM assertion WHERE `+where+` AND hold = 1`, args...).Scan(&held); err != nil {
		return nil, 0, 0, err
	}
	if held > 0 {
		return nil, held, 0, nil
	}
	var total int
	if err := tx.QueryRowContext(ctx,
		`SELECT count(*) FROM assertion WHERE `+where, args...).Scan(&total); err != nil {
		return nil, 0, 0, err
	}
	lim, limited := bound(args, max)
	rows, err := tx.QueryContext(ctx,
		`SELECT `+cols+` FROM assertion WHERE `+where+` ORDER BY seq`+lim, limited...)
	if err != nil {
		return nil, 0, 0, err
	}
	gone, err = scan(rows)
	_ = rows.Close()
	if err != nil {
		return nil, 0, 0, err
	}

	if err := s.d.Shred(ctx, tx, s.words); err != nil {
		return nil, 0, 0, fmt.Errorf("graph: secure delete: %w", err)
	}
	// hold = 0 again, although nothing can have moved one in this transaction: the
	// statement that deletes is the statement that must refuse a held row.
	del, err := tx.PrepareContext(ctx, `DELETE FROM assertion WHERE id = $1 AND hold = 0`)
	if err != nil {
		return nil, 0, 0, err
	}
	defer func() { _ = del.Close() }()
	for _, f := range gone {
		res, err := del.ExecContext(ctx, f.ID)
		if err != nil {
			return nil, 0, 0, err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return nil, 0, 0, fmt.Errorf("graph: erase removed %d rows for one assertion", n)
		}
	}
	if err := witness(gone); err != nil {
		return nil, 0, 0, err
	}
	if err := tx.Commit(); err != nil {
		return nil, 0, 0, err
	}
	if err := s.d.Flush(ctx, s.db); err != nil {
		return gone, 0, total - len(gone), fmt.Errorf("%w: %v", errUncheckpointed, err)
	}
	return gone, 0, total - len(gone), nil
}

// errUncheckpointed says the erasure committed and the SQLite file's write-ahead
// log still holds the frames written before it. The rows are gone from every read;
// the bytes are gone at the next checkpoint. It is its own error so the op can
// say exactly that rather than "failed".
var errUncheckpointed = errors.New("graph: erased, and the write-ahead log was not checkpointed")

// Close releases the tenant's handle. cloud.OrgStore calls it on shutdown.
func (s *store) Close() error { return s.db.Close() }

// relations is the vocabulary this organization has actually asserted. What it
// has DECLARED is assertions in the same table (schema.go), which is why no
// separate schema store exists.
func (s *store) relations(ctx context.Context) ([]string, error) {
	rows, err := s.q.QueryContext(ctx, `SELECT DISTINCT relation FROM assertion ORDER BY relation LIMIT $1`, walkBound)
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
