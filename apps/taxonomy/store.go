package taxonomy

// The taxonomy STORE — two tables, one file, following the settings-store
// discipline: Hanzo Base/SQLite through sqlpool.Open, which is the ONE opener
// (born encrypted under the key cek derives from the process master, and returned
// with the single-connection cap that serializes writes against the file lock).
//
// There is no tenancy key, and that absence is the design. This is ONE catalogue
// for the whole platform — the same categories and the same products for every
// caller of every brand — so a per-org column would invite 400 divergent copies of
// a list whose whole value is that it is the same one. Per-BRAND visibility is a
// property of a row (Brands), not a separate store: a brand sees a subset of the
// one catalogue, never a catalogue of its own.
//
// NO SECRETS. A category label, a product name, an icon name and a route are all
// public by construction — the read serves them to a signed-out visitor. Nothing
// here needs custody, and anything that did would not belong in this table.
//
// Lists (Tags, Brands) are stored as JSON text. SQLite has no array type, the
// lists are a handful of short slugs, and the alternative — a join table per list
// — would triple the schema to answer a question nobody asks (nothing here selects
// "every taxon with tag X" from the database; the whole catalogue is one small
// read the caller filters).

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
  id      TEXT NOT NULL PRIMARY KEY,
  label   TEXT NOT NULL,
  summary TEXT NOT NULL DEFAULT '',
  display INTEGER NOT NULL DEFAULT 0,
  brands  TEXT NOT NULL DEFAULT '[]'
);
CREATE TABLE IF NOT EXISTS taxon (
  id          TEXT NOT NULL PRIMARY KEY,
  name        TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  category    TEXT NOT NULL,
  tags        TEXT NOT NULL DEFAULT '[]',
  icon        TEXT NOT NULL DEFAULT '',
  route       TEXT NOT NULL DEFAULT '',
  href        TEXT NOT NULL DEFAULT '',
  brands      TEXT NOT NULL DEFAULT '[]',
  display     INTEGER NOT NULL DEFAULT 0,
  published   INTEGER NOT NULL DEFAULT 1
);
CREATE INDEX IF NOT EXISTS taxon_by_category ON taxon (category, display, id);`
	if _, err := s.db.Exec(ddl); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	return nil
}

// Close closes the underlying database.
func (s *Store) Close() error { return s.db.Close() }

// Categories returns every category in display order. The id breaks a tie, so two
// categories a person gave the same position to are still listed the same way on
// every read — a catalogue that reshuffled itself between two loads would read as
// a bug in the console.
func (s *Store) Categories(ctx context.Context) ([]Category, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, label, summary, display, brands FROM category ORDER BY display, id`)
	if err != nil {
		return nil, fmt.Errorf("list categories: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Category
	for rows.Next() {
		var c Category
		var brands string
		if err := rows.Scan(&c.ID, &c.Label, &c.Summary, &c.Order, &brands); err != nil {
			return nil, fmt.Errorf("scan category: %w", err)
		}
		c.Brands = decodeList(brands)
		out = append(out, c)
	}
	return out, rows.Err()
}

// Taxa returns the taxa, grouped by category and in display order within it. An
// empty category returns all of them; naming one narrows to it. ONE method, because
// it is one question — a second name for "the same read, filtered" is a second
// place the ordering has to stay right.
func (s *Store) Taxa(ctx context.Context, category string) ([]Taxon, error) {
	const q = `SELECT id, name, description, category, tags, icon, route, href, brands, display, published
	             FROM taxon`
	rows, err := func() (*sql.Rows, error) {
		if category == "" {
			return s.db.QueryContext(ctx, q+` ORDER BY category, display, id`)
		}
		return s.db.QueryContext(ctx, q+` WHERE category=? ORDER BY display, id`, category)
	}()
	if err != nil {
		return nil, fmt.Errorf("list taxa: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Taxon
	for rows.Next() {
		var e Taxon
		var tags, brands string
		if err := rows.Scan(&e.ID, &e.Name, &e.Description, &e.Category, &tags, &e.Icon,
			&e.Route, &e.Href, &brands, &e.Order, &e.Published); err != nil {
			return nil, fmt.Errorf("scan taxon: %w", err)
		}
		e.Tags, e.Brands = decodeList(tags), decodeList(brands)
		out = append(out, e)
	}
	return out, rows.Err()
}

// PutCategory creates or replaces one category.
func (s *Store) PutCategory(ctx context.Context, c Category) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO category (id, label, summary, display, brands) VALUES (?,?,?,?,?)
		 ON CONFLICT(id) DO UPDATE SET label=excluded.label, summary=excluded.summary,
		   display=excluded.display, brands=excluded.brands`,
		c.ID, c.Label, c.Summary, c.Order, encodeList(c.Brands))
	if err != nil {
		return fmt.Errorf("put category: %w", err)
	}
	return nil
}

// PutTaxon creates or replaces one taxon.
func (s *Store) PutTaxon(ctx context.Context, e Taxon) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO taxon (id, name, description, category, tags, icon, route, href, brands, display, published)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?)
		 ON CONFLICT(id) DO UPDATE SET name=excluded.name, description=excluded.description,
		   category=excluded.category, tags=excluded.tags, icon=excluded.icon, route=excluded.route,
		   href=excluded.href, brands=excluded.brands, display=excluded.display, published=excluded.published`,
		e.ID, e.Name, e.Description, e.Category, encodeList(e.Tags), e.Icon,
		e.Route, e.Href, encodeList(e.Brands), e.Order, e.Published)
	if err != nil {
		return fmt.Errorf("put taxon: %w", err)
	}
	return nil
}

// HasCategory reports whether a category exists, which is what makes a taxon's
// category a REFERENCE rather than a free-text field.
func (s *Store) HasCategory(ctx context.Context, id string) (bool, error) {
	var n int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM category WHERE id=?`, id).Scan(&n); err != nil {
		return false, fmt.Errorf("has category: %w", err)
	}
	return n > 0, nil
}

// CountTaxa reports how many taxa are filed under a category. The delete op
// asks before removing one: a category is a label on a group, and deleting the
// label must neither silently delete the products wearing it nor leave them naming
// a category that no longer exists.
func (s *Store) CountTaxa(ctx context.Context, category string) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM taxon WHERE category=?`, category).Scan(&n); err != nil {
		return 0, fmt.Errorf("count taxa: %w", err)
	}
	return n, nil
}

// DeleteCategory removes one category and reports whether it existed. Emptiness is
// the caller's precondition (CountTaxa), asked there because the refusal it
// produces is an answer about the request, not about the table.
func (s *Store) DeleteCategory(ctx context.Context, id string) (bool, error) {
	return s.delete(ctx, `DELETE FROM category WHERE id=?`, id)
}

// DeleteTaxon removes one taxon and reports whether it existed.
func (s *Store) DeleteTaxon(ctx context.Context, id string) (bool, error) {
	return s.delete(ctx, `DELETE FROM taxon WHERE id=?`, id)
}

func (s *Store) delete(ctx context.Context, q, id string) (bool, error) {
	res, err := s.db.ExecContext(ctx, q, id)
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
			`INSERT INTO category (id, label, summary, display, brands) VALUES (?,?,?,?,?)`,
			c.ID, c.Label, c.Summary, c.Order, encodeList(c.Brands)); err != nil {
			return false, fmt.Errorf("seed category %q: %w", c.ID, err)
		}
	}
	for _, e := range taxa {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO taxon (id, name, description, category, tags, icon, route, href, brands, display, published)
			 VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
			e.ID, e.Name, e.Description, e.Category, encodeList(e.Tags), e.Icon,
			e.Route, e.Href, encodeList(e.Brands), e.Order, e.Published); err != nil {
			return false, fmt.Errorf("seed taxon %q: %w", e.ID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit: %w", err)
	}
	return true, nil
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
