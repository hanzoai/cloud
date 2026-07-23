package destinations

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/hanzoai/cloud/cek"
	// The ONE "sqlite" driver, kept registered for cek's no-key plaintext fallback.
	_ "github.com/hanzoai/sqlite"
)

// errNotFound is mapped to 404 by handlers.
var errNotFound = errors.New("destinations: not found")

// Store is the destinations database. ONE SQLite file ({DataDir}/destinations.db)
// holds every org's connected destinations; tenant isolation is the `org` column,
// enforced on EVERY query (the ads/integrations pattern). The row holds only the
// NON-SECRET config (measurement/pixel ids) as JSON — the API secret lives in KMS,
// never here. MaxOpenConns(1) serializes writes against the single-writer file.
type Store struct {
	db *sql.DB
}

func openStore(path string) (*Store, error) {
	db, err := cek.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %q: %w", path, err)
	}
	db.SetMaxOpenConns(1)
	for _, pragma := range []string{
		"PRAGMA busy_timeout=5000",
		"PRAGMA journal_mode=WAL",
		"PRAGMA foreign_keys=ON",
	} {
		if _, err := db.Exec(pragma); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("pragma %q: %w", pragma, err)
		}
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// migrate creates the destinations table. Idempotent. The table leads its lookup
// index with `org` so tenant isolation is a physical property, not just a WHERE.
func (s *Store) migrate() error {
	const ddl = `
CREATE TABLE IF NOT EXISTS destinations (
  org           TEXT NOT NULL,
  platform      TEXT NOT NULL,
  enabled       INTEGER NOT NULL DEFAULT 1,
  config        TEXT NOT NULL DEFAULT '{}',
  account_label TEXT NOT NULL DEFAULT '',
  connected_at  INTEGER NOT NULL,
  updated_at    INTEGER NOT NULL,
  PRIMARY KEY (org, platform)
);
CREATE INDEX IF NOT EXISTS ix_destinations_org_enabled ON destinations(org, enabled);
`
	if _, err := s.db.Exec(ddl); err != nil {
		return fmt.Errorf("destinations migrate: %w", err)
	}
	return nil
}

// Close closes the underlying database. Idempotent-safe via sql.DB.
func (s *Store) Close() error { return s.db.Close() }

// Row is one org's connected destination: the platform, whether the fan-out forwards
// to it, and the non-secret Config. The secret is NOT here (KMS).
type Row struct {
	Org          string `json:"-"`
	Platform     string `json:"platform"`
	Enabled      bool   `json:"enabled"`
	Config       Config `json:"config"`
	AccountLabel string `json:"account,omitempty"`
	ConnectedAt  int64  `json:"connectedAt"`
	UpdatedAt    int64  `json:"updatedAt"`
}

func scanRow(sc interface{ Scan(...any) error }) (Row, error) {
	var (
		r       Row
		enabled int
		cfgJSON string
	)
	if err := sc.Scan(&r.Org, &r.Platform, &enabled, &cfgJSON, &r.AccountLabel, &r.ConnectedAt, &r.UpdatedAt); err != nil {
		return Row{}, err
	}
	r.Enabled = enabled != 0
	r.Config = decodeConfig(cfgJSON)
	return r, nil
}

const rowCols = `org,platform,enabled,config,account_label,connected_at,updated_at`

// Upsert stores (or refreshes) a destination. On a re-connect the original
// connected_at is PRESERVED ("connected since"); only config/enabled/label and
// updated_at advance.
func (s *Store) Upsert(ctx context.Context, r Row) error {
	now := time.Now().Unix()
	enabled := 0
	if r.Enabled {
		enabled = 1
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO destinations (`+rowCols+`) VALUES (?,?,?,?,?,?,?)
		 ON CONFLICT(org,platform) DO UPDATE SET
		   enabled=excluded.enabled,
		   config=excluded.config,
		   account_label=excluded.account_label,
		   updated_at=excluded.updated_at`,
		r.Org, r.Platform, enabled, encodeConfig(r.Config), r.AccountLabel, now, now)
	if err != nil {
		return fmt.Errorf("upsert destination: %w", err)
	}
	return nil
}

// Get returns the destination for (org,platform). found=false (nil error) when there
// is no row.
func (s *Store) Get(ctx context.Context, org, platform string) (Row, bool, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+rowCols+` FROM destinations WHERE org=? AND platform=?`, org, platform)
	r, err := scanRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Row{}, false, nil
	}
	if err != nil {
		return Row{}, false, fmt.Errorf("get destination: %w", err)
	}
	return r, true, nil
}

// List returns every destination for org (enabled and disabled), newest first.
func (s *Store) List(ctx context.Context, org string) ([]Row, error) {
	return s.query(ctx, `SELECT `+rowCols+` FROM destinations WHERE org=? ORDER BY connected_at DESC, platform ASC`, org)
}

// ListEnabled returns the org's ENABLED destinations — the fan-out set.
func (s *Store) ListEnabled(ctx context.Context, org string) ([]Row, error) {
	return s.query(ctx, `SELECT `+rowCols+` FROM destinations WHERE org=? AND enabled=1 ORDER BY platform ASC`, org)
}

func (s *Store) query(ctx context.Context, q string, args ...any) ([]Row, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list destinations: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]Row, 0, 8)
	for rows.Next() {
		r, err := scanRow(rows)
		if err != nil {
			return nil, fmt.Errorf("scan destination: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Delete removes a destination. Reports whether a row went (idempotent caller).
func (s *Store) Delete(ctx context.Context, org, platform string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM destinations WHERE org=? AND platform=?`, org, platform)
	if err != nil {
		return false, fmt.Errorf("delete destination: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ── config JSON codec ────────────────────────────────────────────────────────

func encodeConfig(c Config) string {
	if len(c) == 0 {
		return "{}"
	}
	b, err := json.Marshal(c)
	if err != nil {
		return "{}"
	}
	return string(b)
}

func decodeConfig(s string) Config {
	if s == "" {
		return Config{}
	}
	var c Config
	if json.Unmarshal([]byte(s), &c) != nil {
		return Config{}
	}
	if c == nil {
		return Config{}
	}
	return c
}
