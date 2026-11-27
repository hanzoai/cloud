package domain

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hanzoai/cloud/sdk/dialect"
	// locus.Use is the ONE opener: on hanzo-sql the store is a schema of the system
	// namespace, moved there from its file when the deployment's policy says to; without
	// a DSN the database is born encrypted under the key cek derives from the process
	// master and that namespace, with the single-connection cap already applied.
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/namespace"

	// github.com/hanzoai/sqlite is the ONE Hanzo SQLite driver (registers the
	// "sqlite" database/sql name under both build tags).
	_ "github.com/hanzoai/sqlite"
)

// subsystem names the store in the system namespace; manifest.SQLStores lists it.
const subsystem = "domains"

// within bounds one statement: a purchase waits on the store only to record it.
const within = 10 * time.Second

// Holdings is the ownership store: which org holds which domain, and what it paid. It is
// the deployment's, a schema on hanzo-sql or the one file when there is no DSN, so a
// domain bought through one replica is the org's on every replica and across a restart,
// where an in-memory map answered "not registered to this org" to a renewal anywhere but
// the pod that sold the name.
//
// An org is keyed EXACTLY and a domain case-folded: a domain name is case-insensitive
// (RFC 4343) and an org name is not, so `Acme` and `acme` are two orgs and hold nothing
// of each other's.
type Holdings struct{ db *sql.DB }

// OpenHoldings opens the ownership store under dir, or on hanzo-sql.
func OpenHoldings(dir string) (*Holdings, error) {
	return locus.Use(context.Background(), nil, namespace.System(), subsystem, dir, func(db *sql.DB) (*Holdings, error) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if err := dialect.Of(db).Migrate(ctx, db, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS domain_holdings (
  org           TEXT NOT NULL,
  domain        TEXT NOT NULL,
  registered_at BIGINT NOT NULL,
  expires_at    TEXT NOT NULL DEFAULT '',
  price_cents   BIGINT NOT NULL,
  cost_cents    BIGINT NOT NULL,
  nameservers   TEXT NOT NULL DEFAULT '[]',
  order_id      BIGINT NOT NULL DEFAULT 0,
  PRIMARY KEY (org, domain)
);`)
			return err
		}); err != nil {
			return nil, fmt.Errorf("domain: migrate holdings: %w", err)
		}
		return &Holdings{db: db}, nil
	})
}

// Close closes the underlying database.
func (h *Holdings) Close() error { return h.db.Close() }

// Put upserts a record.
func (h *Holdings) Put(rec Holding) error {
	ns, err := json.Marshal(rec.Nameservers)
	if err != nil {
		return fmt.Errorf("domain: record nameservers: %w", err)
	}
	if ns == nil || string(ns) == "null" {
		ns = []byte("[]")
	}
	ctx, cancel := context.WithTimeout(context.Background(), within)
	defer cancel()
	if _, err := h.db.ExecContext(ctx,
		`INSERT INTO domain_holdings (org, domain, registered_at, expires_at, price_cents, cost_cents, nameservers, order_id)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		 ON CONFLICT(org, domain) DO UPDATE SET registered_at=excluded.registered_at, expires_at=excluded.expires_at,
		   price_cents=excluded.price_cents, cost_cents=excluded.cost_cents, nameservers=excluded.nameservers, order_id=excluded.order_id`,
		rec.Org, strings.ToLower(rec.Domain), rec.RegisteredAt, rec.ExpiresAt, rec.PriceCents, rec.CostCents, string(ns), rec.Order); err != nil {
		return fmt.Errorf("domain: record holding: %w", err)
	}
	return nil
}

const holdingColumns = `org, domain, registered_at, expires_at, price_cents, cost_cents, nameservers, order_id`

func scanHolding(row interface{ Scan(...any) error }) (Holding, error) {
	var rec Holding
	var ns string
	if err := row.Scan(&rec.Org, &rec.Domain, &rec.RegisteredAt, &rec.ExpiresAt, &rec.PriceCents, &rec.CostCents, &ns, &rec.Order); err != nil {
		return Holding{}, err
	}
	if err := json.Unmarshal([]byte(ns), &rec.Nameservers); err != nil {
		return Holding{}, fmt.Errorf("domain: holding %s nameservers: %w", rec.Domain, err)
	}
	if len(rec.Nameservers) == 0 {
		rec.Nameservers = nil
	}
	return rec, nil
}

// Get returns the record for (org, domain) and whether it exists.
func (h *Holdings) Get(org, domainName string) (Holding, bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), within)
	defer cancel()
	rec, err := scanHolding(h.db.QueryRowContext(ctx,
		`SELECT `+holdingColumns+` FROM domain_holdings WHERE org=$1 AND domain=$2`, org, strings.ToLower(domainName)))
	if errors.Is(err, sql.ErrNoRows) {
		return Holding{}, false, nil
	}
	if err != nil {
		return Holding{}, false, err
	}
	return rec, true, nil
}

// ListByOrg returns every domain the org holds, newest registration first.
func (h *Holdings) ListByOrg(org string) ([]Holding, error) {
	ctx, cancel := context.WithTimeout(context.Background(), within)
	defer cancel()
	rows, err := h.db.QueryContext(ctx,
		`SELECT `+holdingColumns+` FROM domain_holdings WHERE org=$1 ORDER BY registered_at DESC, domain`, org)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]Holding, 0)
	for rows.Next() {
		rec, err := scanHolding(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}
