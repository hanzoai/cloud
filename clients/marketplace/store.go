package marketplace

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/hanzoai/cloud/cek"
	_ "github.com/hanzoai/sqlite"
)

// errNotFound is returned when a listing is absent.
var errNotFound = errors.New("marketplace: listing not found")

// Listing is one marketplace offer: a tool/agent surfaced for discovery and
// install, optionally monetized (PriceCents>0) with a seller payout Recipient
// wallet. The tool name is a registry tool (any source). Isolation is the
// (publisher_org, id) key — a publisher only ever mutates its OWN listings.
type Listing struct {
	ID           string `json:"id"`
	PublisherOrg string `json:"publisherOrg"`
	Tool         string `json:"tool"`
	Title        string `json:"title"`
	Description  string `json:"description"`
	Category     string `json:"category"`
	PriceCents   int64  `json:"priceCents"`
	Currency     string `json:"currency"`
	Recipient    string `json:"recipient"` // seller payout wallet (x402 Payee).
	Public       bool   `json:"public"`
	CreatedAt    int64  `json:"createdAt"`
}

// Store is the marketplace listing store (one SQLite file, publisher_org column,
// the shared cloud store discipline).
type Store struct {
	db *sql.DB
}

// Open opens (and migrates) the listing store at path.
func Open(path string) (*Store, error) {
	db, err := cek.Open(path)
	if err != nil {
		return nil, fmt.Errorf("marketplace: open store %q: %w", path, err)
	}
	db.SetMaxOpenConns(1)
	for _, pragma := range []string{"PRAGMA busy_timeout=5000", "PRAGMA journal_mode=WAL", "PRAGMA foreign_keys=ON"} {
		if _, err := db.Exec(pragma); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("marketplace: pragma %q: %w", pragma, err)
		}
	}
	s := &Store{db: db}
	if _, err := db.Exec(`
CREATE TABLE IF NOT EXISTS listings (
  id            TEXT NOT NULL,
  publisher_org TEXT NOT NULL,
  tool          TEXT NOT NULL,
  title         TEXT NOT NULL DEFAULT '',
  description   TEXT NOT NULL DEFAULT '',
  category      TEXT NOT NULL DEFAULT '',
  price_cents   INTEGER NOT NULL DEFAULT 0,
  currency      TEXT NOT NULL DEFAULT 'USD',
  recipient     TEXT NOT NULL DEFAULT '',
  public        INTEGER NOT NULL DEFAULT 0,
  created_at    INTEGER NOT NULL,
  PRIMARY KEY (publisher_org, id)
);
CREATE INDEX IF NOT EXISTS ix_listings_public_tool ON listings(public, tool);`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("marketplace: migrate: %w", err)
	}
	return s, nil
}

// Close closes the underlying database.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// Create inserts a listing (id generated).
func (s *Store) Create(ctx context.Context, l Listing) (Listing, error) {
	l.ID = "lst_" + randHex(8)
	l.CreatedAt = time.Now().Unix()
	if l.Currency == "" {
		l.Currency = "USD"
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO listings (id, publisher_org, tool, title, description, category, price_cents, currency, recipient, public, created_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		l.ID, l.PublisherOrg, l.Tool, l.Title, l.Description, l.Category, l.PriceCents, l.Currency, l.Recipient, boolInt(l.Public), l.CreatedAt); err != nil {
		return Listing{}, fmt.Errorf("marketplace: create: %w", err)
	}
	return l, nil
}

// ListByOrg returns a publisher's own listings, newest first.
func (s *Store) ListByOrg(ctx context.Context, org string) ([]Listing, error) {
	return s.query(ctx, `SELECT id, publisher_org, tool, title, description, category, price_cents, currency, recipient, public, created_at
		FROM listings WHERE publisher_org=? ORDER BY created_at DESC`, org)
}

// ListPublic returns every public listing (the shop), newest first, bounded.
func (s *Store) ListPublic(ctx context.Context, limit int) ([]Listing, error) {
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	return s.query(ctx, `SELECT id, publisher_org, tool, title, description, category, price_cents, currency, recipient, public, created_at
		FROM listings WHERE public=1 ORDER BY created_at DESC LIMIT ?`, limit)
}

// PublicByTool returns the cheapest public monetized listing per tool name — the
// price the Pricer seam applies at dispatch. A single scan builds the map.
func (s *Store) PublicByTool(ctx context.Context) (map[string]Listing, error) {
	rows, err := s.ListPublic(ctx, 1000)
	if err != nil {
		return nil, err
	}
	out := map[string]Listing{}
	for _, l := range rows {
		if cur, ok := out[l.Tool]; !ok || l.PriceCents < cur.PriceCents {
			out[l.Tool] = l
		}
	}
	return out, nil
}

// CheapestPublicForTool returns the cheapest public monetized listing for a tool,
// or (Listing{}, false) if none — the price the Pricer applies at dispatch. One
// indexed query (no full scan) on the dispatch hot path.
func (s *Store) CheapestPublicForTool(ctx context.Context, tool string) (Listing, bool) {
	rows, err := s.query(ctx,
		`SELECT id, publisher_org, tool, title, description, category, price_cents, currency, recipient, public, created_at
		   FROM listings WHERE public=1 AND tool=? AND price_cents>0 ORDER BY price_cents ASC LIMIT 1`, tool)
	if err != nil || len(rows) == 0 {
		return Listing{}, false
	}
	return rows[0], true
}

// Delete removes a listing for (publisher_org, id). Reports whether a row was removed.
func (s *Store) Delete(ctx context.Context, org, id string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM listings WHERE publisher_org=? AND id=?`, org, id)
	if err != nil {
		return false, fmt.Errorf("marketplace: delete: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func (s *Store) query(ctx context.Context, q string, args ...any) ([]Listing, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("marketplace: query: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Listing
	for rows.Next() {
		var l Listing
		var pub int
		if err := rows.Scan(&l.ID, &l.PublisherOrg, &l.Tool, &l.Title, &l.Description, &l.Category,
			&l.PriceCents, &l.Currency, &l.Recipient, &pub, &l.CreatedAt); err != nil {
			return nil, err
		}
		l.Public = pub != 0
		out = append(out, l)
	}
	return out, rows.Err()
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "0000000000000000"[:n*2]
	}
	return hex.EncodeToString(b)
}
