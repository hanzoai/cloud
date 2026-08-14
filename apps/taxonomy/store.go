package taxonomy

// The taxonomy STORE — two tables, one file, following the settings-store
// discipline: Hanzo Base/SQLite through sqlpool.Open, which is the ONE opener
// (born encrypted under the key cek derives from the process master, and returned
// with the single-connection cap that serializes writes against the file lock).
//
// EVERY ROW BELONGS TO AN ORG, and (owner, id) is the primary key. That composite
// is forced by tenancy rather than chosen for convenience: a global id would make
// one org's write fail because a DIFFERENT org already used that name, and a
// refusal is an observation — org A would learn that org B holds "crm" without
// ever reading a row. Two customers may each have a "crm", and neither may learn
// the other exists.
//
// ONE table, two audiences. The platform's own catalogue is the rows owned by the
// hanzo org; a customer's rows are owned by that customer. There is deliberately
// no second table for "customer taxonomy": one record, projected per audience, so
// the two answers cannot drift apart.
//
// NO SECRETS. A category label, a product name, an icon name and a route are all
// public within the audience that may see them — the platform rows are served to a
// signed-out visitor. Nothing here needs custody, and anything that did would not
// belong in this table.
//
// Lists (Tags, Brands) are stored as JSON text. SQLite has no array type, the
// lists are a handful of short slugs, and the alternative — a join table per list
// — would triple the schema to answer a question nobody asks (nothing here selects
// "every taxon with tag X" from the database; a caller's whole catalogue is one
// small read).

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/hanzoai/cloud/sqlpool"

	// The ONE Hanzo SQLite driver (registers the "sqlite" database/sql name under
	// both cgo and pure-Go build tags). Blank import registers it; importing
	// modernc directly would double-register.
	_ "github.com/hanzoai/sqlite"
)

// Store is the durable half of the taxonomy.
type Store struct {
	db *sql.DB
}

func openStore(dir string) (*Store, error) {
	db, err := sqlpool.Open("taxonomy", dir)
	if err != nil {
		return nil, err
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// display is the ORDER column under a name SQL will accept unquoted. It is also
// the honest one: the number decides where a row is shown and means nothing else.
func (s *Store) migrate() error {
	const ddl = `
CREATE TABLE IF NOT EXISTS category (
  owner   TEXT NOT NULL,
  id      TEXT NOT NULL,
  label   TEXT NOT NULL,
  summary TEXT NOT NULL DEFAULT '',
  display INTEGER NOT NULL DEFAULT 0,
  brands  TEXT NOT NULL DEFAULT '[]',
  PRIMARY KEY (owner, id)
);
CREATE TABLE IF NOT EXISTS taxon (
  owner       TEXT NOT NULL,
  id          TEXT NOT NULL,
  name        TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  category    TEXT NOT NULL,
  tags        TEXT NOT NULL DEFAULT '[]',
  icon        TEXT NOT NULL DEFAULT '',
  route       TEXT NOT NULL DEFAULT '',
  href        TEXT NOT NULL DEFAULT '',
  brands      TEXT NOT NULL DEFAULT '[]',
  display     INTEGER NOT NULL DEFAULT 0,
  published   INTEGER NOT NULL DEFAULT 1,
  PRIMARY KEY (owner, id)
);
CREATE INDEX IF NOT EXISTS taxon_by_owner ON taxon (owner, category, display, id);`
	if _, err := s.db.Exec(ddl); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	return nil
}

// Close closes the underlying database.
func (s *Store) Close() error { return s.db.Close() }

// owners is the audience of one read: the platform's rows, plus the caller's own
// when they have an org. It is the ONE place a read's visible set is decided, and
// it is built from the VALIDATED principal's org — never from a request field, so
// there is no argument a caller can pass to widen it.
func owners(org string) []any {
	if org == "" || org == platformOrg {
		return []any{platformOrg}
	}
	return []any{platformOrg, org}
}

// Categories returns the categories visible to org, in display order. The id
// breaks a tie, so two categories a person gave the same position to are still
// listed the same way on every read — a catalogue that reshuffled itself between
// two loads would read as a bug in the console.
func (s *Store) Categories(ctx context.Context, org string) ([]Category, error) {
	who := owners(org)
	q := `SELECT owner, id, label, summary, display, brands FROM category
	       WHERE owner IN (` + marks(len(who)) + `) ORDER BY display, id`
	rows, err := s.db.QueryContext(ctx, q, who...)
	if err != nil {
		return nil, fmt.Errorf("list categories: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Category
	for rows.Next() {
		var c Category
		var brands string
		if err := rows.Scan(&c.Owner, &c.ID, &c.Label, &c.Summary, &c.Order, &brands); err != nil {
			return nil, fmt.Errorf("scan category: %w", err)
		}
		c.Brands = decodeList(brands)
		out = append(out, c)
	}
	return out, rows.Err()
}

// Taxa returns the taxa visible to org, grouped by category and in display order
// within it. An empty category returns all of them; naming one narrows to it. ONE
// method, because it is one question — a second name for "the same read, filtered"
// is a second place the ordering has to stay right.
func (s *Store) Taxa(ctx context.Context, org, category string) ([]Taxon, error) {
	who := owners(org)
	q := `SELECT owner, id, name, description, category, tags, icon, route, href, brands, display, published
	        FROM taxon WHERE owner IN (` + marks(len(who)) + `)`
	args := who
	if category != "" {
		q += ` AND category=?`
		args = append(append([]any{}, who...), category)
	}
	q += ` ORDER BY category, display, id`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list taxa: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Taxon
	for rows.Next() {
		var e Taxon
		var tags, brands string
		if err := rows.Scan(&e.Owner, &e.ID, &e.Name, &e.Description, &e.Category, &tags, &e.Icon,
			&e.Route, &e.Href, &brands, &e.Order, &e.Published); err != nil {
			return nil, fmt.Errorf("scan taxon: %w", err)
		}
		e.Tags, e.Brands = decodeList(tags), decodeList(brands)
		out = append(out, e)
	}
	return out, rows.Err()
}

// PutCategory creates or replaces one category in its owner's catalogue.
func (s *Store) PutCategory(ctx context.Context, c Category) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO category (owner, id, label, summary, display, brands) VALUES (?,?,?,?,?,?)
		 ON CONFLICT(owner, id) DO UPDATE SET label=excluded.label, summary=excluded.summary,
		   display=excluded.display, brands=excluded.brands`,
		c.Owner, c.ID, c.Label, c.Summary, c.Order, encodeList(c.Brands))
	if err != nil {
		return fmt.Errorf("put category: %w", err)
	}
	return nil
}

// PutTaxon creates or replaces one taxon in its owner's catalogue.
func (s *Store) PutTaxon(ctx context.Context, e Taxon) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO taxon (owner, id, name, description, category, tags, icon, route, href, brands, display, published)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?)
		 ON CONFLICT(owner, id) DO UPDATE SET name=excluded.name, description=excluded.description,
		   category=excluded.category, tags=excluded.tags, icon=excluded.icon, route=excluded.route,
		   href=excluded.href, brands=excluded.brands, display=excluded.display, published=excluded.published`,
		e.Owner, e.ID, e.Name, e.Description, e.Category, encodeList(e.Tags), e.Icon,
		e.Route, e.Href, encodeList(e.Brands), e.Order, e.Published)
	if err != nil {
		return fmt.Errorf("put taxon: %w", err)
	}
	return nil
}

// HasCategory reports whether org can file a taxon under this category id — its
// own, or the platform's. That is the same audience the read projects, so a taxon
// can never be filed somewhere its own catalogue cannot render it.
func (s *Store) HasCategory(ctx context.Context, org, id string) (bool, error) {
	who := owners(org)
	var n int
	q := `SELECT COUNT(*) FROM category WHERE id=? AND owner IN (` + marks(len(who)) + `)`
	if err := s.db.QueryRowContext(ctx, q, append([]any{id}, who...)...).Scan(&n); err != nil {
		return false, fmt.Errorf("has category: %w", err)
	}
	return n > 0, nil
}

// CountTaxa reports how many taxa stand in the way of deleting a category, and
// WHOSE it asks about is the tenancy rule again. Deleting an org's own category
// counts that org's taxa alone — counting another tenant's would answer a question
// about data the caller may not observe. Deleting a PLATFORM category counts every
// org's, because a platform category is one a customer may have filed under, and
// only platform sudo can delete one — a scope that is cross-tenant by definition.
func (s *Store) CountTaxa(ctx context.Context, owner, category string) (int, error) {
	q, args := `SELECT COUNT(*) FROM taxon WHERE category=? AND owner=?`, []any{category, owner}
	if owner == platformOrg {
		q, args = `SELECT COUNT(*) FROM taxon WHERE category=?`, []any{category}
	}
	var n int
	if err := s.db.QueryRowContext(ctx, q, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("count taxa: %w", err)
	}
	return n, nil
}

// DeleteCategory removes one category from its owner's catalogue and reports
// whether it existed. Emptiness is the caller's precondition (CountTaxa), asked
// there because the refusal it produces is an answer about the request, not about
// the table.
func (s *Store) DeleteCategory(ctx context.Context, owner, id string) (bool, error) {
	return s.delete(ctx, `DELETE FROM category WHERE owner=? AND id=?`, owner, id)
}

// DeleteTaxon removes one taxon from its owner's catalogue and reports whether it
// existed. The owner predicate is mandatory and comes from the validated
// principal, so a delete can only ever reach the caller's own row: naming another
// org's id deletes nothing of theirs, and answers 404 because the CALLER holds no
// such row.
func (s *Store) DeleteTaxon(ctx context.Context, owner, id string) (bool, error) {
	return s.delete(ctx, `DELETE FROM taxon WHERE owner=? AND id=?`, owner, id)
}

func (s *Store) delete(ctx context.Context, q string, args ...any) (bool, error) {
	res, err := s.db.ExecContext(ctx, q, args...)
	if err != nil {
		return false, fmt.Errorf("delete: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("delete: %w", err)
	}
	return n > 0, nil
}

// Seed writes a whole catalogue in ONE transaction, and only into a store that
// holds nothing. It reports false without writing when either table already has a
// row: the seed is the FIRST-BOOT contents, never a reconciliation, because a
// seed that re-asserted itself on every restart would undo an editor's deletion
// every time the pod moved.
func (s *Store) Seed(ctx context.Context, cats []Category, taxa []Taxon) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var n int
	if err := tx.QueryRowContext(ctx,
		`SELECT (SELECT COUNT(*) FROM category) + (SELECT COUNT(*) FROM taxon)`).Scan(&n); err != nil {
		return false, fmt.Errorf("count: %w", err)
	}
	if n > 0 {
		return false, nil
	}
	for _, c := range cats {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO category (owner, id, label, summary, display, brands) VALUES (?,?,?,?,?,?)`,
			c.Owner, c.ID, c.Label, c.Summary, c.Order, encodeList(c.Brands)); err != nil {
			return false, fmt.Errorf("seed category %q: %w", c.ID, err)
		}
	}
	for _, e := range taxa {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO taxon (owner, id, name, description, category, tags, icon, route, href, brands, display, published)
			 VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
			e.Owner, e.ID, e.Name, e.Description, e.Category, encodeList(e.Tags), e.Icon,
			e.Route, e.Href, encodeList(e.Brands), e.Order, e.Published); err != nil {
			return false, fmt.Errorf("seed taxon %q: %w", e.ID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit: %w", err)
	}
	return true, nil
}

// marks renders n bind placeholders. The owner set is built here, never
// interpolated from a caller's string, so the only thing this shapes is arity.
func marks(n int) string {
	if n <= 1 {
		return "?"
	}
	s := "?"
	for range n - 1 {
		s += ",?"
	}
	return s
}

// encodeList stores a list of short slugs. A nil list and an empty one are the
// same fact — no scope, no tags — so both store as `[]` and read back as nil.
func encodeList(v []string) string {
	if len(v) == 0 {
		return "[]"
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "[]"
	}
	return string(b)
}

func decodeList(s string) []string {
	if s == "" || s == "[]" {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
