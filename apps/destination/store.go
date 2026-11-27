package destination

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/hanzoai/cloud/sdk/dialect"
	"github.com/hanzoai/cloud/sdk/tenure"
	// locus.Use is the ONE opener (it moves the store off its file first, when asked): on hanzo-sql the store is a schema of the system
	// namespace; without it the database is born encrypted under the key cek derives
	// from the process master and that namespace, with the single-connection cap
	// already applied.
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/namespace"

	// The ONE "sqlite" driver.
	_ "github.com/hanzoai/sqlite"
)

// subsystem names the store: the system namespace's "destinations". The database and
// the sealer are both made from it, so a column's key and its schema cannot name two
// things.
const subsystem = "destinations"

// Store is the destinations database. ONE database — the system namespace's
// "destinations", a schema on hanzo-sql or a SQLite file — holds every org's
// connected destinations; tenant isolation is the `org` column, enforced on EVERY
// query (the ads/integrations pattern). The row holds only the NON-SECRET config
// (measurement/pixel ids) as JSON — the API secret lives in KMS, never here. The
// touches table holds what a visitor's browser said (click ids, browser ids, the
// agent, the page, the hashed email): personal data, SEALED (internal/org, field.go) on both
// engines, so it is ciphertext on hanzo-sql as it was in the SQLCipher file. Its SQL
// is the dialect both engines share (sdk/dialect); on SQLite MaxOpenConns(1)
// serializes writes against the single-writer file.
type Store struct {
	db     *sql.DB
	fields *tenure.Fields
}

// openStore opens the database and the sealed columns' keys for the system namespace's
// "destinations". master is cloud.Deps.MasterKey; with none the store does not open, so
// no touch is ever written in the clear because a key was missing.
func openStore(dir string, master []byte) (*Store, error) {
	c, err := tenure.NewCipher(master)
	if err != nil {
		return nil, fmt.Errorf("destinations: %w", err)
	}
	fields, err := c.Fields(namespace.System(), subsystem)
	if err != nil {
		return nil, fmt.Errorf("destinations: %w", err)
	}
	return locus.Use(context.Background(), nil, namespace.System(), subsystem, dir, func(db *sql.DB) (*Store, error) {
		s := &Store{db: db, fields: fields}
		if err := s.migrate(); err != nil {
			return nil, err
		}
		return s, nil
	})
}

// migrate creates the destinations table. Idempotent. The table leads its lookup
// index with `org` so tenant isolation is a physical property, not just a WHERE.
func (s *Store) migrate() error {
	const ddl = `
CREATE TABLE IF NOT EXISTS destinations (
  org           TEXT NOT NULL,
  platform      TEXT NOT NULL,
  enabled       BIGINT NOT NULL DEFAULT 1,
  config        TEXT NOT NULL DEFAULT '{}',
  account_label TEXT NOT NULL DEFAULT '',
  connected_at  BIGINT NOT NULL,
  updated_at    BIGINT NOT NULL,
  PRIMARY KEY (org, platform)
);
CREATE INDEX IF NOT EXISTS ix_destinations_org_enabled ON destinations(org, enabled);
` + touchDDL
	// One transaction through tx alone (Dialect.Migrate); a column is added only when
	// the table lacks it, since a failed ALTER would abort the transaction on
	// PostgreSQL.
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	d := dialect.Of(s.db)
	if err := d.Migrate(ctx, s.db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, ddl); err != nil {
			return fmt.Errorf("destinations migrate: %w", err)
		}
		for _, col := range touchColumns {
			has, err := d.HasColumn(ctx, tx, "touches", col)
			if err != nil {
				return fmt.Errorf("destinations migrate: %w", err)
			}
			if has {
				continue
			}
			if _, err := tx.ExecContext(ctx, "ALTER TABLE touches ADD COLUMN "+col+" TEXT NOT NULL DEFAULT ''"); err != nil {
				return fmt.Errorf("destinations migrate: %w", err)
			}
		}
		return nil
	}); err != nil {
		return err
	}
	// A SQLite file from before touches were sealed holds them in plaintext. Seal them in
	// place, before any read, so the store never reads a value it did not write and
	// cmd/sqlmove copies ciphertext. hanzo-sql tables are born sealed, and Upgrade leaves
	// them alone. It is a walk in small transactions of its own, after the schema has
	// committed.
	if _, err := s.fields.Upgrade(context.Background(), s.db, "touches", touchKey, touchSealed); err != nil {
		return fmt.Errorf("destinations migrate: %w", err)
	}
	return nil
}

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
		`INSERT INTO destinations (`+rowCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7)
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
	row := s.db.QueryRowContext(ctx, `SELECT `+rowCols+` FROM destinations WHERE org=$1 AND platform=$2`, org, platform)
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
	return s.query(ctx, `SELECT `+rowCols+` FROM destinations WHERE org=$1 ORDER BY connected_at DESC, platform ASC`, org)
}

// ListEnabled returns the org's ENABLED destinations — the fan-out set.
func (s *Store) ListEnabled(ctx context.Context, org string) ([]Row, error) {
	return s.query(ctx, `SELECT `+rowCols+` FROM destinations WHERE org=$1 AND enabled=1 ORDER BY platform ASC`, org)
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
	res, err := s.db.ExecContext(ctx, `DELETE FROM destinations WHERE org=$1 AND platform=$2`, org, platform)
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
