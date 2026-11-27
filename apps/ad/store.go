package ad

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/hanzoai/cloud/sdk/dialect"
	// locus.Use is the ONE opener (it moves the store off its file first, when asked): on hanzo-sql the store is a schema of the system
	// namespace; without it the database is born encrypted under the key cek derives
	// from the process master and that namespace, with the single-connection cap
	// already applied.
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/namespace"

	// The ONE "sqlite" driver.
	_ "github.com/hanzoai/sqlite"
)

// Sentinel errors. Handlers map these to HTTP status codes:
//
//	errNotFound → 404, errConflict → 409.
var (
	errNotFound = errors.New("ads: not found")
	errConflict = errors.New("ads: already exists")
)

// Store is the ads database. ONE database — the system namespace's "ads", a schema
// on hanzo-sql or a SQLite file — holds every org's records; tenant isolation is
// the `org` column, enforced on EVERY query. This mirrors clients/crm exactly (the
// ONE storage pattern). Its SQL is the dialect both engines share
// (sdk/dialect); on SQLite MaxOpenConns(1) serializes writes against the
// single-writer file.
type Store struct {
	db *sql.DB
}

func openStore(dir string) (*Store, error) {
	return locus.Use(context.Background(), nil, namespace.System(), "ads", dir, func(db *sql.DB) (*Store, error) {
		s := &Store{db: db}
		if err := s.migrate(); err != nil {
			return nil, err
		}
		return s, nil
	})
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
  budget      BIGINT NOT NULL DEFAULT 0,
  spend       BIGINT NOT NULL DEFAULT 0,
  created_at  BIGINT NOT NULL,
  updated_at  BIGINT NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_ads_campaigns_org_updated  ON ads_campaigns(org, updated_at);
CREATE INDEX IF NOT EXISTS ix_ads_campaigns_org_status   ON ads_campaigns(org, status);
CREATE INDEX IF NOT EXISTS ix_ads_campaigns_org_platform ON ads_campaigns(org, platform);
`
	// One transaction through tx alone (Dialect.Migrate). A column is added only when
	// the table lacks it, for databases created before account/external_id existed;
	// the names are package constants (never user input), so inlining them is safe.
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	d := dialect.Of(s.db)
	return d.Migrate(ctx, s.db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, ddl); err != nil {
			return fmt.Errorf("ads migrate: %w", err)
		}
		for _, add := range []struct{ column, def string }{
			{"account", "TEXT NOT NULL DEFAULT ''"},
			{"external_id", "TEXT NOT NULL DEFAULT ''"},
		} {
			has, err := d.HasColumn(ctx, tx, "ads_campaigns", add.column)
			if err != nil {
				return fmt.Errorf("ads migrate column %s: %w", add.column, err)
			}
			if has {
				continue
			}
			if _, err := tx.ExecContext(ctx, `ALTER TABLE ads_campaigns ADD COLUMN `+add.column+` `+add.def); err != nil {
				return fmt.Errorf("ads migrate column %s: %w", add.column, err)
			}
		}
		return nil
	})
}

// Close closes the underlying database. Idempotent-safe via sql.DB.
func (s *Store) Close() error { return s.db.Close() }

// AdCampaign is an org-scoped ad campaign — the root of the ad hierarchy
// (campaign → ad sets → ads; the ad-set/ad legs hang off this client as
// follow-ups). Budget and Spend are minor units (cents). Platform is the ad
// network (meta/google/tiktok/x); Status is the lifecycle
// (draft/active/paused/completed) — both validated at the write layer against
// the fixed vocabularies in ads.go.
type AdCampaign struct {
	// ID is the campaign's server-minted handle, "camp_" + 32 hex. A create body
	// cannot choose it, and it is the id every other route addresses.
	ID string `json:"id"`
	// Org is the owning tenant, stamped from the validated principal. `json:"-"`
	// keeps it off the wire entirely: a body could otherwise name a tenant.
	Org string `json:"-"`
	// Name is the campaign's display label, and the name Meta creates the campaign
	// object under at launch. Required; trimmed and bounded to 1024 bytes.
	Name string `json:"name"`
	// Platform is the ad network: meta, google, tiktok or x, and nothing else — a
	// write naming another is 400. Empty stores as meta. Only meta executes today;
	// launching any of the other three is 501.
	Platform string `json:"platform"`
	// Account is the provider ad-account the campaign runs under, in Meta's
	// act_<id> form. Empty until the org supplies one or a launch resolves it.
	Account string `json:"account,omitempty"`
	// ExternalID is the ad network's own campaign id, written by a successful
	// launch and by nothing else — an update never touches it. Empty means this
	// campaign has never reached its network.
	ExternalID string `json:"externalId,omitempty"`
	// Status is the lifecycle: draft, active, paused or completed, and nothing else
	// — a write naming another is 400. Empty stores as draft; a successful launch
	// sets active. It records what this deployment did, not what the ad network
	// currently reports.
	Status string `json:"status"`
	// Objective is the campaign goal spelled as the provider names it
	// ("conversions", "OUTCOME_TRAFFIC"), passed through to the network verbatim at
	// launch — Meta defaults an empty one to OUTCOME_TRAFFIC. Free text, bounded to
	// 1024 bytes; no vocabulary is enforced here.
	Objective string `json:"objective"`
	// Budget is the campaign's authorized spend in MINOR units (cents). Negative
	// clamps to 0. It is the org's stored plan: a Meta launch creates the campaign
	// object only, and the delivering budget lives on the ad set.
	Budget int64 `json:"budget"`
	// Spend is spend-to-date in MINOR units (cents), as last written through create
	// or update. Negative clamps to 0. It is NOT read back from the network — that
	// is a separate insights call — so 0 means nothing was recorded here, not that
	// nothing was spent.
	Spend int64 `json:"spend"`
	// CreatedAt is when the campaign was first stored, in unix seconds. It never
	// changes, including across a full-replace update.
	CreatedAt int64 `json:"createdAt"`
	// UpdatedAt is when the row was last written, in unix seconds — set by create,
	// update and launch. Listings are ordered by it, newest first.
	UpdatedAt int64 `json:"updatedAt"`
}

const campaignCols = `id,org,name,platform,account,external_id,status,objective,budget,spend,created_at,updated_at`

func scanCampaign(sc interface{ Scan(...any) error }) (AdCampaign, error) {
	var c AdCampaign
	err := sc.Scan(&c.ID, &c.Org, &c.Name, &c.Platform, &c.Account, &c.ExternalID, &c.Status, &c.Objective,
		&c.Budget, &c.Spend, &c.CreatedAt, &c.UpdatedAt)
	return c, err
}

func (s *Store) CreateCampaign(ctx context.Context, c AdCampaign) (AdCampaign, error) {
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO ads_campaigns (`+campaignCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
		c.ID, c.Org, c.Name, c.Platform, c.Account, c.ExternalID, c.Status, c.Objective, c.Budget, c.Spend,
		c.CreatedAt, c.UpdatedAt); err != nil {
		return AdCampaign{}, fmt.Errorf("insert campaign: %w", err)
	}
	return c, nil
}

func (s *Store) GetCampaign(ctx context.Context, org, id string) (AdCampaign, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+campaignCols+` FROM ads_campaigns WHERE org=$1 AND id=$2`, org, id)
	c, err := scanCampaign(row)
	if errors.Is(err, sql.ErrNoRows) {
		return AdCampaign{}, errNotFound
	}
	if err != nil {
		return AdCampaign{}, fmt.Errorf("get campaign: %w", err)
	}
	return c, nil
}

// ListCampaigns lists the org's campaigns, optionally filtered by status
// (status=="" means all). Most-recently-updated first.
func (s *Store) ListCampaigns(ctx context.Context, org, status string, limit int) ([]AdCampaign, error) {
	var (
		rows *sql.Rows
		err  error
	)
	if status == "" {
		rows, err = s.db.QueryContext(ctx,
			`SELECT `+campaignCols+` FROM ads_campaigns WHERE org=$1 ORDER BY updated_at DESC LIMIT $2`, org, limit)
	} else {
		rows, err = s.db.QueryContext(ctx,
			`SELECT `+campaignCols+` FROM ads_campaigns WHERE org=$1 AND status=$2 ORDER BY updated_at DESC LIMIT $3`, org, status, limit)
	}
	if err != nil {
		return nil, fmt.Errorf("list campaigns: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]AdCampaign, 0, 16)
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
func (s *Store) UpdateCampaign(ctx context.Context, c AdCampaign) (AdCampaign, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE ads_campaigns SET name=$1,platform=$2,account=$3,status=$4,objective=$5,budget=$6,spend=$7,updated_at=$8 WHERE org=$9 AND id=$10`,
		c.Name, c.Platform, c.Account, c.Status, c.Objective, c.Budget, c.Spend, c.UpdatedAt, c.Org, c.ID)
	if err != nil {
		return AdCampaign{}, fmt.Errorf("update campaign: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return AdCampaign{}, errNotFound
	}
	return s.GetCampaign(ctx, c.Org, c.ID)
}

// MarkLaunched records the provider execution on a stored campaign: its account +
// external id, and status=active. Org-scoped — a cross-tenant id affects zero
// rows (errNotFound), never a foreign mutation. This is the ONLY writer of
// external_id, so the launch link is never clobbered by a user edit.
func (s *Store) MarkLaunched(ctx context.Context, org, id, account, externalID string, updatedAt int64) (AdCampaign, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE ads_campaigns SET account=$1,external_id=$2,status='active',updated_at=$3 WHERE org=$4 AND id=$5`,
		account, externalID, updatedAt, org, id)
	if err != nil {
		return AdCampaign{}, fmt.Errorf("mark launched: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return AdCampaign{}, errNotFound
	}
	return s.GetCampaign(ctx, org, id)
}

func (s *Store) DeleteCampaign(ctx context.Context, org, id string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM ads_campaigns WHERE org=$1 AND id=$2`, org, id)
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
		 FROM ads_campaigns WHERE org=$1`, org)
	if err = row.Scan(&total, &active, &budget, &spend); err != nil {
		return 0, 0, 0, 0, fmt.Errorf("count campaigns: %w", err)
	}
	return total, active, budget, spend, nil
}
