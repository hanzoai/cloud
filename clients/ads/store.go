package ads

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	// cek opens the store encrypted at rest (migrate-on-open + shred).
	"github.com/hanzoai/cloud/cek"
	// The ONE "sqlite" driver, kept registered for cek's no-key plaintext fallback.
	_ "github.com/hanzoai/sqlite"
)

// Sentinel errors. Handlers map these to HTTP status codes:
//
//	errNotFound → 404, errConflict → 409.
var (
	errNotFound = errors.New("ads: not found")
	errConflict = errors.New("ads: already exists")
)

// Store is the ads database. ONE SQLite file ({DataDir}/ads.db) holds every
// org's records; tenant isolation is the `org` column, enforced on EVERY query.
// This mirrors clients/crm exactly (the ONE storage pattern). MaxOpenConns(1)
// serializes writes against the single-writer file.
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

// migrate creates the campaigns table. Idempotent (IF NOT EXISTS). The table
// leads its lookup indexes with `org` so tenant isolation is a physical
// property, not just a WHERE clause.
func (s *Store) migrate() error {
	const ddl = `
CREATE TABLE IF NOT EXISTS ads_campaigns (
  id          TEXT PRIMARY KEY,
  org         TEXT NOT NULL,
  name        TEXT NOT NULL,
  platform    TEXT NOT NULL DEFAULT 'meta',
  status      TEXT NOT NULL DEFAULT 'draft',
  objective   TEXT NOT NULL DEFAULT '',
  budget      INTEGER NOT NULL DEFAULT 0,
  spend       INTEGER NOT NULL DEFAULT 0,
  created_at  INTEGER NOT NULL,
  updated_at  INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_ads_campaigns_org_updated  ON ads_campaigns(org, updated_at);
CREATE INDEX IF NOT EXISTS ix_ads_campaigns_org_status   ON ads_campaigns(org, status);
CREATE INDEX IF NOT EXISTS ix_ads_campaigns_org_platform ON ads_campaigns(org, platform);
`
	if _, err := s.db.Exec(ddl); err != nil {
		return fmt.Errorf("ads migrate: %w", err)
	}
	return nil
}

// Close closes the underlying database. Idempotent-safe via sql.DB.
func (s *Store) Close() error { return s.db.Close() }

// Campaign is an org-scoped ad campaign — the root of the ad hierarchy
// (campaign → ad sets → ads; the ad-set/ad legs hang off this seam as
// follow-ups). Budget and Spend are minor units (cents). Platform is the ad
// network (meta/google/tiktok/x); Status is the lifecycle
// (draft/active/paused/completed) — both validated at the write layer against
// the fixed vocabularies in ads.go.
type Campaign struct {
	ID        string `json:"id"`
	Org       string `json:"-"`
	Name      string `json:"name"`
	Platform  string `json:"platform"`
	Status    string `json:"status"`
	Objective string `json:"objective"`
	Budget    int64  `json:"budget"`
	Spend     int64  `json:"spend"`
	CreatedAt int64  `json:"createdAt"`
	UpdatedAt int64  `json:"updatedAt"`
}

const campaignCols = `id,org,name,platform,status,objective,budget,spend,created_at,updated_at`

func scanCampaign(sc interface{ Scan(...any) error }) (Campaign, error) {
	var c Campaign
	err := sc.Scan(&c.ID, &c.Org, &c.Name, &c.Platform, &c.Status, &c.Objective,
		&c.Budget, &c.Spend, &c.CreatedAt, &c.UpdatedAt)
	return c, err
}

func (s *Store) CreateCampaign(ctx context.Context, c Campaign) (Campaign, error) {
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO ads_campaigns (`+campaignCols+`) VALUES (?,?,?,?,?,?,?,?,?,?)`,
		c.ID, c.Org, c.Name, c.Platform, c.Status, c.Objective, c.Budget, c.Spend,
		c.CreatedAt, c.UpdatedAt); err != nil {
		return Campaign{}, fmt.Errorf("insert campaign: %w", err)
	}
	return c, nil
}

func (s *Store) GetCampaign(ctx context.Context, org, id string) (Campaign, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+campaignCols+` FROM ads_campaigns WHERE org=? AND id=?`, org, id)
	c, err := scanCampaign(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Campaign{}, errNotFound
	}
	if err != nil {
		return Campaign{}, fmt.Errorf("get campaign: %w", err)
	}
	return c, nil
}

// ListCampaigns lists the org's campaigns, optionally filtered by status
// (status=="" means all). Most-recently-updated first.
func (s *Store) ListCampaigns(ctx context.Context, org, status string, limit int) ([]Campaign, error) {
	var (
		rows *sql.Rows
		err  error
	)
	if status == "" {
		rows, err = s.db.QueryContext(ctx,
			`SELECT `+campaignCols+` FROM ads_campaigns WHERE org=? ORDER BY updated_at DESC LIMIT ?`, org, limit)
	} else {
		rows, err = s.db.QueryContext(ctx,
			`SELECT `+campaignCols+` FROM ads_campaigns WHERE org=? AND status=? ORDER BY updated_at DESC LIMIT ?`, org, status, limit)
	}
	if err != nil {
		return nil, fmt.Errorf("list campaigns: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]Campaign, 0, 16)
	for rows.Next() {
		c, err := scanCampaign(rows)
		if err != nil {
			return nil, fmt.Errorf("scan campaign: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) UpdateCampaign(ctx context.Context, c Campaign) (Campaign, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE ads_campaigns SET name=?,platform=?,status=?,objective=?,budget=?,spend=?,updated_at=? WHERE org=? AND id=?`,
		c.Name, c.Platform, c.Status, c.Objective, c.Budget, c.Spend, c.UpdatedAt, c.Org, c.ID)
	if err != nil {
		return Campaign{}, fmt.Errorf("update campaign: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Campaign{}, errNotFound
	}
	return s.GetCampaign(ctx, c.Org, c.ID)
}

func (s *Store) DeleteCampaign(ctx context.Context, org, id string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM ads_campaigns WHERE org=? AND id=?`, org, id)
	if err != nil {
		return false, fmt.Errorf("delete campaign: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// Counts returns the per-org campaign roll-up: total campaigns, how many are
// active, and the summed budget + spend (cents) — a real, non-fabricated summary
// for the ads module's overview cards.
func (s *Store) Counts(ctx context.Context, org string) (total, active int, budget, spend int64, err error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*),
		        COALESCE(SUM(CASE WHEN status='active' THEN 1 ELSE 0 END),0),
		        COALESCE(SUM(budget),0),
		        COALESCE(SUM(spend),0)
		 FROM ads_campaigns WHERE org=?`, org)
	if err = row.Scan(&total, &active, &budget, &spend); err != nil {
		return 0, 0, 0, 0, fmt.Errorf("count campaigns: %w", err)
	}
	return total, active, budget, spend, nil
}
