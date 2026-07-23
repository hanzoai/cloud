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
// property, not just a WHERE clause. account + external_id link a stored campaign
// to its launched provider execution (provider.go); they are added idempotently
// so a DB created before the connector-execution edge existed gains them cleanly.
func (s *Store) migrate() error {
	const ddl = `
CREATE TABLE IF NOT EXISTS ads_campaigns (
  id          TEXT PRIMARY KEY,
  org         TEXT NOT NULL,
  name        TEXT NOT NULL,
  platform    TEXT NOT NULL DEFAULT 'meta',
  account     TEXT NOT NULL DEFAULT '',
  external_id TEXT NOT NULL DEFAULT '',
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
	// Idempotent column adds for DBs created before account/external_id existed.
	// The column names are package constants (never user input) → safe to inline.
	for col, spec := range map[string]string{
		"account":     "TEXT NOT NULL DEFAULT ''",
		"external_id": "TEXT NOT NULL DEFAULT ''",
	} {
		if err := s.addColumnIfMissing("ads_campaigns", col, spec); err != nil {
			return fmt.Errorf("ads migrate column %s: %w", col, err)
		}
	}
	return nil
}

// addColumnIfMissing ALTERs table to add col (with spec) only when absent. table/
// col/spec are package constants, so the interpolation is injection-safe. Makes
// the schema migration forward-only and idempotent across process restarts.
func (s *Store) addColumnIfMissing(table, col, spec string) error {
	rows, err := s.db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var (
			cid, notnull, pk int
			name, ctype      string
			dflt             sql.NullString
		)
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return err
		}
		if name == col {
			return nil // already present
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = s.db.Exec(`ALTER TABLE ` + table + ` ADD COLUMN ` + col + ` ` + spec)
	return err
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
	ID         string `json:"id"`
	Org        string `json:"-"`
	Name       string `json:"name"`
	Platform   string `json:"platform"`
	Account    string `json:"account,omitempty"`    // provider ad-account ref (Meta act_<id>)
	ExternalID string `json:"externalId,omitempty"` // provider campaign id after a launch
	Status     string `json:"status"`
	Objective  string `json:"objective"`
	Budget     int64  `json:"budget"`
	Spend      int64  `json:"spend"`
	CreatedAt  int64  `json:"createdAt"`
	UpdatedAt  int64  `json:"updatedAt"`
}

const campaignCols = `id,org,name,platform,account,external_id,status,objective,budget,spend,created_at,updated_at`

func scanCampaign(sc interface{ Scan(...any) error }) (Campaign, error) {
	var c Campaign
	err := sc.Scan(&c.ID, &c.Org, &c.Name, &c.Platform, &c.Account, &c.ExternalID, &c.Status, &c.Objective,
		&c.Budget, &c.Spend, &c.CreatedAt, &c.UpdatedAt)
	return c, err
}

func (s *Store) CreateCampaign(ctx context.Context, c Campaign) (Campaign, error) {
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO ads_campaigns (`+campaignCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		c.ID, c.Org, c.Name, c.Platform, c.Account, c.ExternalID, c.Status, c.Objective, c.Budget, c.Spend,
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

// UpdateCampaign edits the user-owned fields. external_id is deliberately NOT in
// the SET list: it is launch-owned (MarkLaunched sets it), so editing a campaign
// never clobbers the link to its live provider execution.
func (s *Store) UpdateCampaign(ctx context.Context, c Campaign) (Campaign, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE ads_campaigns SET name=?,platform=?,account=?,status=?,objective=?,budget=?,spend=?,updated_at=? WHERE org=? AND id=?`,
		c.Name, c.Platform, c.Account, c.Status, c.Objective, c.Budget, c.Spend, c.UpdatedAt, c.Org, c.ID)
	if err != nil {
		return Campaign{}, fmt.Errorf("update campaign: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Campaign{}, errNotFound
	}
	return s.GetCampaign(ctx, c.Org, c.ID)
}

// MarkLaunched records the provider execution on a stored campaign: its account +
// external id, and status=active. Org-scoped — a cross-tenant id affects zero
// rows (errNotFound), never a foreign mutation. This is the ONLY writer of
// external_id, so the launch link is never clobbered by a user edit.
func (s *Store) MarkLaunched(ctx context.Context, org, id, account, externalID string, updatedAt int64) (Campaign, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE ads_campaigns SET account=?,external_id=?,status='active',updated_at=? WHERE org=? AND id=?`,
		account, externalID, updatedAt, org, id)
	if err != nil {
		return Campaign{}, fmt.Errorf("mark launched: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Campaign{}, errNotFound
	}
	return s.GetCampaign(ctx, org, id)
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
