package marketplace

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/hanzoai/cloud/apps/money"
	"github.com/hanzoai/cloud/cek"
	_ "github.com/hanzoai/sqlite"
)

// errNotFound is returned when a listing is absent.
var errNotFound = errors.New("marketplace: listing not found")

// Listing is one marketplace offer: a tool/agent surfaced for discovery and
// install, optionally monetized (Price>0) with a seller payout Recipient wallet.
// The tool name is a registry tool (any source). Isolation is the
// (publisher_org, id) key — a publisher only ever mutates its OWN listings.
//
// PublisherOrg is also the PAYEE org: a listing is paid into a wallet of the org
// that published it and no other, which is what makes a cross-org credit
// unconstructible rather than merely unlikely (wallets resolves an id only within
// the org that is asked for).
type Listing struct {
	ID           string       `json:"id"`
	PublisherOrg string       `json:"publisherOrg"`
	Tool         string       `json:"tool"`
	Title        string       `json:"title"`
	Description  string       `json:"description"`
	Category     string       `json:"category"`
	Price        money.Amount `json:"price"` // exact per-call price; 0 is free.
	Currency     string       `json:"currency"`
	Recipient    string       `json:"recipient"` // seller payout WALLET ID, in PublisherOrg.
	Public       bool         `json:"public"`
	CreatedAt    int64        `json:"createdAt"`
}

// Store is the marketplace listing store (one SQLite file, publisher_org column,
// the shared cloud store discipline).
type Store struct {
	db *sql.DB
}

// Open opens (and migrates) the listing store at path.
func Open(path string) (*Store, error) {
	db, err := cek.Open(cek.Global, path)
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
  price         TEXT NOT NULL DEFAULT '0',
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
	if err := migrateCents(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// migrateCents carries a pre-exact store forward: `price_cents INTEGER` becomes
// `price TEXT` holding the 18-decimal magnitude money.Amount stores. One cent is
// 10^16 atto, so the conversion is exact in both directions and the old column is
// dropped in the same transaction — there is no window where two columns each claim
// to be the price. A store that never had price_cents is left alone.
func migrateCents(db *sql.DB) error {
	var legacy int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM pragma_table_info('listings') WHERE name='price_cents'`).Scan(&legacy); err != nil {
		return fmt.Errorf("marketplace: inspect schema: %w", err)
	}
	if legacy == 0 {
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("marketplace: migrate price: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, stmt := range []string{
		`UPDATE listings SET price = CAST(price_cents AS TEXT) || '0000000000000000' WHERE price_cents > 0`,
		`ALTER TABLE listings DROP COLUMN price_cents`,
	} {
		if _, err := tx.Exec(stmt); err != nil {
			return fmt.Errorf("marketplace: migrate price (%s): %w", stmt, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("marketplace: migrate price: %w", err)
	}
	return nil
}

// Close closes the underlying database.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// columns is the listing projection, written once so every read decodes the same
// shape in the same order as query's Scan.
const columns = `id, publisher_org, tool, title, description, category, price, currency, recipient, public, created_at`

// Create inserts a listing (id generated).
func (s *Store) Create(ctx context.Context, l Listing) (Listing, error) {
	l.ID = "lst_" + randHex(8)
	l.CreatedAt = time.Now().Unix()
	if l.Currency == "" {
		l.Currency = "USD"
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO listings (`+columns+`) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		l.ID, l.PublisherOrg, l.Tool, l.Title, l.Description, l.Category, l.Price.AttoString(),
		l.Currency, l.Recipient, boolInt(l.Public), l.CreatedAt); err != nil {
		return Listing{}, fmt.Errorf("marketplace: create: %w", err)
	}
	return l, nil
}

// ListByOrg returns a publisher's own listings, newest first.
func (s *Store) ListByOrg(ctx context.Context, org string) ([]Listing, error) {
	return s.query(ctx, `SELECT `+columns+` FROM listings WHERE publisher_org=? ORDER BY created_at DESC`, org)
}

// ListPublic returns every public listing (the shop), newest first, bounded.
func (s *Store) ListPublic(ctx context.Context, limit int) ([]Listing, error) {
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	return s.query(ctx, `SELECT `+columns+` FROM listings WHERE public=1 ORDER BY created_at DESC LIMIT ?`, limit)
}

// PublicByTool returns the cheapest public listing per tool name — the shop-window
// overlay discovery paints onto the catalog. A single scan builds the map.
func (s *Store) PublicByTool(ctx context.Context) (map[string]Listing, error) {
	rows, err := s.ListPublic(ctx, 1000)
	if err != nil {
		return nil, err
	}
	out := map[string]Listing{}
	for _, l := range rows {
		if cur, ok := out[l.Tool]; !ok || l.Price.Cmp(cur.Price) < 0 {
			out[l.Tool] = l
		}
	}
	return out, nil
}

// CheapestPublicForTool returns the cheapest public MONETIZED listing for a tool —
// the row the x402 price table answers from. One indexed lookup (ix_listings_public_tool
// covers public+tool) on the dispatch hot path.
//
// The minimum is taken in Go, on money.Amount, not in SQL. An 18-decimal price is
// stored as its exact magnitude and $10 is 10^19 — past int64 — so ORDER BY CAST(…
// AS INTEGER) would silently mis-order the expensive half of the shop. Money is
// compared by the type that owns comparison, in one place, always.
//
// A query failure is RETURNED, never swallowed: the payment gate must fail closed on
// a blip, not conclude the tool is free.
func (s *Store) CheapestPublicForTool(ctx context.Context, tool string) (Listing, bool, error) {
	rows, err := s.query(ctx, `SELECT `+columns+` FROM listings WHERE public=1 AND tool=?`, tool)
	if err != nil {
		return Listing{}, false, err
	}
	var best Listing
	found := false
	for _, l := range rows {
		if l.Price.Sign() <= 0 {
			continue // a free listing never shadows a priced one
		}
		if !found || l.Price.Cmp(best.Price) < 0 {
			best, found = l, true
		}
	}
	return best, found, nil
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
		var price string
		if err := rows.Scan(&l.ID, &l.PublisherOrg, &l.Tool, &l.Title, &l.Description, &l.Category,
			&price, &l.Currency, &l.Recipient, &pub, &l.CreatedAt); err != nil {
			return nil, err
		}
		if l.Price, err = money.ParseInt(price); err != nil {
			return nil, fmt.Errorf("marketplace: listing %s has an unreadable price %q: %w", l.ID, price, err)
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
