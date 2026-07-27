package marketing

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	// cek opens the store encrypted at rest (migrate-on-open + shred).
	"github.com/hanzoai/cloud/cek"
	// The ONE "sqlite" driver, kept registered for cek's no-key plaintext fallback.
	_ "github.com/hanzoai/sqlite"
)

// Sentinel errors. Handlers map these to HTTP status codes (mapErr):
//
//	errNotFound → 404, errConflict → 409, errIAMUnavailable/errWarehouse → 503.
//
// The two 503s are the honest "I cannot tell you who the customers are" — a
// resolution that cannot read the identity store or the warehouse must refuse,
// never resolve to an empty audience and report a successful send to nobody.
var (
	errNotFound       = errors.New("marketing: not found")
	errConflict       = errors.New("marketing: already exists")
	errIAMUnavailable = errors.New("marketing: identity store unavailable; enable the co-resident iam subsystem")
	errWarehouse      = errors.New("marketing: analytics warehouse not configured")
)

// Store is the marketing database. ONE SQLite file ({DataDir}/marketing.db)
// holds every org's records; tenant isolation is the `org` column, enforced on
// EVERY query. This mirrors clients/crm exactly (the ONE storage pattern).
// MaxOpenConns(1) serializes writes against the single-writer file.
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

// migrate is the ONE schema orchestrator: it runs every GTM lane's idempotent
// migration in order. Each lane owns its own DDL in its own file (campaigns here,
// suppressions/sequences/audiences/promos/calendar in siblings), so a lane is
// self-contained — adding one is a new file plus a line here, never a rewrite of
// a shared blob. Every table leads its `org` column and its lookup indexes with
// `org`, making tenant isolation a physical property, not just a WHERE clause.
func (s *Store) migrate() error {
	for _, m := range []func() error{
		s.migrateCampaigns,
		s.migrateSuppressions,
		s.migrateSequences,
		s.migrateAudiences,
		s.migratePromos,
		s.migrateCalendar,
	} {
		if err := m(); err != nil {
			return err
		}
	}
	return nil
}

// migrateCampaigns creates the campaigns table. Idempotent (IF NOT EXISTS).
// scheduled_at (unix; 0 = unscheduled) carries a campaign's send time for the
// "scheduled" lifecycle state.
func (s *Store) migrateCampaigns() error {
	const ddl = `
CREATE TABLE IF NOT EXISTS marketing_campaigns (
  id            TEXT PRIMARY KEY,
  org           TEXT NOT NULL,
  name          TEXT NOT NULL,
  channel       TEXT NOT NULL DEFAULT 'email',
  status        TEXT NOT NULL DEFAULT 'draft',
  objective     TEXT NOT NULL DEFAULT '',
  budget        INTEGER NOT NULL DEFAULT 0,
  spend         INTEGER NOT NULL DEFAULT 0,
  scheduled_at  INTEGER NOT NULL DEFAULT 0,
  created_at    INTEGER NOT NULL,
  updated_at    INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_marketing_campaigns_org_updated ON marketing_campaigns(org, updated_at);
CREATE INDEX IF NOT EXISTS ix_marketing_campaigns_org_status  ON marketing_campaigns(org, status);
CREATE INDEX IF NOT EXISTS ix_marketing_campaigns_org_channel ON marketing_campaigns(org, channel);
`
	if _, err := s.db.Exec(ddl); err != nil {
		return fmt.Errorf("marketing migrate campaigns: %w", err)
	}
	// Idempotent column upgrades for a pre-existing prod table created before a
	// column was added to the DDL above. CREATE TABLE IF NOT EXISTS never alters an
	// existing table, so an old marketing_campaigns is frozen at its original schema
	// and every write of a newer column 500s ("table marketing_campaigns has no
	// column named scheduled_at"). ADD COLUMN on a column that already exists is the
	// only error swallowed (see addColumn). The store is an encrypted single-file
	// SQLite only the binary can open, so this migrate-on-open is the ONLY way an old
	// prod table gains the column.
	for _, ac := range []struct{ table, col, def string }{
		{"marketing_campaigns", "scheduled_at", "INTEGER NOT NULL DEFAULT 0"},
	} {
		if err := s.addColumn(ac.table, ac.col, ac.def); err != nil {
			return err
		}
	}
	return nil
}

// addColumn adds a column to an existing table, treating an already-present column
// as success (SQLite has no ADD COLUMN IF NOT EXISTS). Mirrors clients/social/store.go.
func (s *Store) addColumn(table, col, def string) error {
	_, err := s.db.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, col, def))
	if err == nil || strings.Contains(err.Error(), "duplicate column name") {
		return nil
	}
	return fmt.Errorf("marketing migrate: add %s.%s: %w", table, col, err)
}

// Close closes the underlying database. Idempotent-safe via sql.DB.
func (s *Store) Close() error { return s.db.Close() }

// Campaign is an org-scoped marketing campaign. Budget and Spend are minor units
// (cents). Channel is the delivery surface (email/sms/social/meta/google/tiktok);
// Status is the lifecycle (draft/active/paused/completed) — both validated at the
// write layer against the fixed vocabularies in marketing.go.
type Campaign struct {
	ID          string `json:"id"`
	Org         string `json:"-"`
	Name        string `json:"name"`
	Channel     string `json:"channel"`
	Status      string `json:"status"`
	Objective   string `json:"objective"`
	Budget      int64  `json:"budget"`
	Spend       int64  `json:"spend"`
	ScheduledAt int64  `json:"scheduledAt"`
	CreatedAt   int64  `json:"createdAt"`
	UpdatedAt   int64  `json:"updatedAt"`
}

const campaignCols = `id,org,name,channel,status,objective,budget,spend,scheduled_at,created_at,updated_at`

func scanCampaign(sc interface{ Scan(...any) error }) (Campaign, error) {
	var c Campaign
	err := sc.Scan(&c.ID, &c.Org, &c.Name, &c.Channel, &c.Status, &c.Objective,
		&c.Budget, &c.Spend, &c.ScheduledAt, &c.CreatedAt, &c.UpdatedAt)
	return c, err
}

func (s *Store) CreateCampaign(ctx context.Context, c Campaign) (Campaign, error) {
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO marketing_campaigns (`+campaignCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		c.ID, c.Org, c.Name, c.Channel, c.Status, c.Objective, c.Budget, c.Spend,
		c.ScheduledAt, c.CreatedAt, c.UpdatedAt); err != nil {
		return Campaign{}, fmt.Errorf("insert campaign: %w", err)
	}
	return c, nil
}

func (s *Store) GetCampaign(ctx context.Context, org, id string) (Campaign, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+campaignCols+` FROM marketing_campaigns WHERE org=? AND id=?`, org, id)
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
			`SELECT `+campaignCols+` FROM marketing_campaigns WHERE org=? ORDER BY updated_at DESC LIMIT ?`, org, limit)
	} else {
		rows, err = s.db.QueryContext(ctx,
			`SELECT `+campaignCols+` FROM marketing_campaigns WHERE org=? AND status=? ORDER BY updated_at DESC LIMIT ?`, org, status, limit)
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
		`UPDATE marketing_campaigns SET name=?,channel=?,status=?,objective=?,budget=?,spend=?,scheduled_at=?,updated_at=? WHERE org=? AND id=?`,
		c.Name, c.Channel, c.Status, c.Objective, c.Budget, c.Spend, c.ScheduledAt, c.UpdatedAt, c.Org, c.ID)
	if err != nil {
		return Campaign{}, fmt.Errorf("update campaign: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Campaign{}, errNotFound
	}
	return s.GetCampaign(ctx, c.Org, c.ID)
}

func (s *Store) DeleteCampaign(ctx context.Context, org, id string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM marketing_campaigns WHERE org=? AND id=?`, org, id)
	if err != nil {
		return false, fmt.Errorf("delete campaign: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// Counts returns the per-org campaign roll-up: total campaigns, how many are
// active, and the summed budget + spend (cents) — a real, non-fabricated summary
// for the marketing module's overview cards.
func (s *Store) Counts(ctx context.Context, org string) (total, active int, budget, spend int64, err error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*),
		        COALESCE(SUM(CASE WHEN status='active' THEN 1 ELSE 0 END),0),
		        COALESCE(SUM(budget),0),
		        COALESCE(SUM(spend),0)
		 FROM marketing_campaigns WHERE org=?`, org)
	if err = row.Scan(&total, &active, &budget, &spend); err != nil {
		return 0, 0, 0, 0, fmt.Errorf("count campaigns: %w", err)
	}
	return total, active, budget, spend, nil
}
