package campaign

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	// sqlpool.Open is the ONE opener: the database is born encrypted under the
	// key cek derives from the process master and the system namespace, and comes
	// back with the single-connection cap already applied.
	"github.com/hanzoai/cloud/sqlpool"

	// The ONE "sqlite" driver.
	_ "github.com/hanzoai/sqlite"
)

// Sentinel errors. Handlers map these to HTTP status codes:
//
//	errNotFound → 404, errConflict → 409, errChannelUnsupported → the honest
//	per-channel "no executor / provider unsupported" the fan-out records.
var (
	errNotFound           = errors.New("campaign: not found")
	errConflict           = errors.New("campaign: already exists")
	errChannelUnsupported = errors.New("campaign: channel unsupported on this deployment")
)

// Campaign lifecycle. draft is the only fully-mutable state; launch fans the
// campaign out to its channels and moves it to live (or failed if every channel
// failed); pause stops every live channel.
const (
	StatusDraft     = "draft"
	StatusScheduled = "scheduled"
	StatusLive      = "live"
	StatusPaused    = "paused"
	StatusCompleted = "completed"
	StatusFailed    = "failed"
)

// ChannelSpec status — the per-channel launch outcome recorded on the campaign.
const (
	chanPending     = "pending"     // added, not yet launched
	chanLive        = "live"        // launched on the provider
	chanPaused      = "paused"      // paused on the provider
	chanFailed      = "failed"      // launch/pause errored (Detail carries why)
	chanUnavailable = "unavailable" // no executor wired / org has not connected the connector
)

// ChannelSpec is one fan-out target on a Campaign: which kind (paid/organic/
// email), the provider platform + account it runs on, and — after launch — the
// provider-side id + status the orchestrator recorded. It carries NO credential;
// the executor resolves the org's connector token itself at launch time.
type ChannelSpec struct {
	// Kind is the channel and the identity a campaign holds at most one of: paid,
	// organic or email. It picks the executor the launch fans out to.
	Kind string `json:"kind"`
	// Platform is the provider within the kind — meta, google, x, instagram, or the
	// email provider.
	Platform string `json:"platform"`
	// Account is the provider account this channel runs under: an ad-account, a page
	// or a mailing-list id. An executor may replace it at launch with the account it
	// actually used.
	Account string `json:"account,omitempty"`
	// ExternalID is the provider-side id of the running execution, recorded by the
	// orchestrator at launch and handed back verbatim to read spend or to pause.
	// Server-owned and absent until this channel has launched; anything a caller
	// sends for it is dropped.
	ExternalID string `json:"externalId,omitempty"`
	// Status is this channel's own launch outcome, not the campaign's: pending (added,
	// never launched), live, paused, failed (Detail says why) or unavailable (no
	// executor wired on this deployment). Server-owned — a caller can never assert it.
	Status string `json:"status"`
	// Detail is the last outcome in one secret-free line — the failure reason, or
	// what the executor reported. Absent when there is nothing to explain.
	Detail string `json:"detail,omitempty"`
}

// campaignRecord is the top-level GTM object — a VALUE that spans channels. Budget
// is minor units (cents). Content is the ordered creative set (Content[0] is the
// active creative; the rest are A/B variants when an experiment is composed).
// Metrics are deliberately NOT a field: they are read at query time from the ONE
// analytics plane (metrics.go), never stored here.
//
// It is DECLARED under its published name and spelled Campaign everywhere else,
// rather than the reverse, because prose only reaches the document keyed by the
// name the FIELDS are declared under: zipdoc lifts a field's comment from the
// struct literal it stands in, and the schema builder looks it up under the type
// reflect names. A defined type over another struct (type campaignRecord Campaign)
// has no literal of its own, so every field of it published bare.
type campaignRecord struct {
	// ID is the campaign's server-minted handle — "cmp_" and 128 random bits — and
	// the id every other campaign call is addressed by. Never read off the wire: a
	// create that sends one has it ignored.
	ID string `json:"id"`
	// Org is the owning tenant, set from the validated bearer's owner claim. It is
	// the isolation key on every query and is deliberately NOT published: a caller
	// only ever sees their own org's campaigns, so the field would say nothing.
	Org string `json:"-"`
	// Name is the campaign's display name. Required on write, trimmed, and capped at
	// 2048 characters.
	Name string `json:"name"`
	// Audience is an opaque reference to the segment this campaign targets. It is
	// stored and echoed but not yet handed to the executors — a channel targets
	// through the provider account it runs under — so it is documentation for now.
	// Absent when never set.
	Audience string `json:"audience,omitempty"`
	// Content is the ordered creative set, at most 32, empty entries dropped.
	// Content[0] is the creative that runs; the rest are A/B variants a wired
	// experiment can assign per launch.
	Content []string `json:"content"`
	// Channels are the fan-out targets, at most one per kind and at most 12, each
	// carrying its own post-launch state. Empty means nothing to launch, which is
	// what makes a launch of this campaign a 400.
	Channels []ChannelSpec `json:"channels"`
	// ScheduleAt is when the campaign should run, in unix seconds. 0 (absent) means
	// launch immediately. It is passed to each executor; nothing in this service
	// wakes up to launch it for you.
	ScheduleAt int64 `json:"scheduleAt,omitempty"`
	// Budget is the campaign's total budget in CENTS, handed to each executor as the
	// budget for its channel. 0 means none was set.
	Budget int64 `json:"budget"`
	// Status is the lifecycle state, server-owned and never accepted from a caller.
	// Four values actually occur: draft (inert and fully mutable — nothing is sent
	// and no budget is committed), live, paused and failed. After a fan-out live
	// means AT LEAST ONE channel launched — read the channel rows for the rest —
	// and failed means none did.
	Status string `json:"status"`
	// CreatedAt is when the campaign was created, in unix seconds. Server-set.
	CreatedAt int64 `json:"createdAt"`
	// UpdatedAt is the last write in unix seconds — an edit, a launch or a pause.
	// Server-set on every save.
	UpdatedAt int64 `json:"updatedAt"`
}

// Campaign is the domain spelling of campaignRecord. An ALIAS, so it is the SAME
// type and not a second shape to keep in sync: the store, the fan-out and the
// executors read in domain language while the document keeps the name the fleet's
// flat schema namespace needs (apps/marketing already publishes an email
// "Campaign", and openapi.Weave refuses one name meaning two things).
type Campaign = campaignRecord

// Store is the campaign database. ONE SQLite file — the system namespace's
// "campaigns" — holds every org's records; tenant isolation is the `org` column,
// enforced on EVERY query. Mirrors clients/ads exactly (the ONE storage
// pattern). MaxOpenConns(1) serializes writes against the single-writer file.
type Store struct {
	db *sql.DB
}

func openStore(dir string) (*Store, error) {
	// The FILE keeps the name it was created under — the rows are already in it.
	db, err := sqlpool.Open("campaign", dir)
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

// migrate creates the campaigns table. Idempotent (IF NOT EXISTS). Content and
// channels are JSON columns — the Campaign is ONE value, so it round-trips as one
// row (launch/pause rewrite the whole row atomically). The table leads its lookup
// indexes with `org` so tenant isolation is a physical property, not just a WHERE.
func (s *Store) migrate() error {
	const ddl = `
CREATE TABLE IF NOT EXISTS campaigns (
  id          TEXT PRIMARY KEY,
  org         TEXT NOT NULL,
  name        TEXT NOT NULL,
  audience    TEXT NOT NULL DEFAULT '',
  content     TEXT NOT NULL DEFAULT '[]',
  channels    TEXT NOT NULL DEFAULT '[]',
  schedule_at INTEGER NOT NULL DEFAULT 0,
  budget      INTEGER NOT NULL DEFAULT 0,
  status      TEXT NOT NULL DEFAULT 'draft',
  created_at  INTEGER NOT NULL,
  updated_at  INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_campaigns_org_updated ON campaigns(org, updated_at);
CREATE INDEX IF NOT EXISTS ix_campaigns_org_status  ON campaigns(org, status);
`
	if _, err := s.db.Exec(ddl); err != nil {
		return fmt.Errorf("campaign migrate: %w", err)
	}
	return nil
}

// Close closes the underlying database. Idempotent-safe via sql.DB.
func (s *Store) Close() error { return s.db.Close() }

const campaignCols = `id,org,name,audience,content,channels,schedule_at,budget,status,created_at,updated_at`

// scanCampaign decodes one row, unmarshalling the content + channels JSON columns.
func scanCampaign(sc interface{ Scan(...any) error }) (Campaign, error) {
	var (
		c                      Campaign
		contentJSON, chansJSON string
	)
	if err := sc.Scan(&c.ID, &c.Org, &c.Name, &c.Audience, &contentJSON, &chansJSON,
		&c.ScheduleAt, &c.Budget, &c.Status, &c.CreatedAt, &c.UpdatedAt); err != nil {
		return Campaign{}, err
	}
	c.Content = decodeContent(contentJSON)
	c.Channels = decodeChannels(chansJSON)
	return c, nil
}

func decodeContent(j string) []string {
	out := []string{}
	if j != "" {
		_ = json.Unmarshal([]byte(j), &out)
	}
	if out == nil {
		out = []string{}
	}
	return out
}

func decodeChannels(j string) []ChannelSpec {
	out := []ChannelSpec{}
	if j != "" {
		_ = json.Unmarshal([]byte(j), &out)
	}
	if out == nil {
		out = []ChannelSpec{}
	}
	return out
}

// encode marshals the JSON columns. A marshal error is impossible for these
// plain types, but is returned rather than dropped (no silent failure).
func encode(c Campaign) (contentJSON, chansJSON string, err error) {
	cb, err := json.Marshal(c.Content)
	if err != nil {
		return "", "", fmt.Errorf("encode content: %w", err)
	}
	hb, err := json.Marshal(c.Channels)
	if err != nil {
		return "", "", fmt.Errorf("encode channels: %w", err)
	}
	return string(cb), string(hb), nil
}

func (s *Store) CreateCampaign(ctx context.Context, c Campaign) (Campaign, error) {
	contentJSON, chansJSON, err := encode(c)
	if err != nil {
		return Campaign{}, err
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO campaigns (`+campaignCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		c.ID, c.Org, c.Name, c.Audience, contentJSON, chansJSON, c.ScheduleAt, c.Budget,
		c.Status, c.CreatedAt, c.UpdatedAt); err != nil {
		return Campaign{}, fmt.Errorf("insert campaign: %w", err)
	}
	return c, nil
}

func (s *Store) GetCampaign(ctx context.Context, org, id string) (Campaign, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+campaignCols+` FROM campaigns WHERE org=? AND id=?`, org, id)
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
			`SELECT `+campaignCols+` FROM campaigns WHERE org=? ORDER BY updated_at DESC LIMIT ?`, org, limit)
	} else {
		rows, err = s.db.QueryContext(ctx,
			`SELECT `+campaignCols+` FROM campaigns WHERE org=? AND status=? ORDER BY updated_at DESC LIMIT ?`, org, status, limit)
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

// Save persists the full campaign row (name, audience, content, channels,
// schedule, budget, status). It is the ONE write used by update AND by launch/
// pause (which rewrite channels + status), always org-scoped: a cross-tenant id
// affects zero rows → errNotFound, never a foreign mutation.
func (s *Store) Save(ctx context.Context, c Campaign) (Campaign, error) {
	contentJSON, chansJSON, err := encode(c)
	if err != nil {
		return Campaign{}, err
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE campaigns SET name=?,audience=?,content=?,channels=?,schedule_at=?,budget=?,status=?,updated_at=? WHERE org=? AND id=?`,
		c.Name, c.Audience, contentJSON, chansJSON, c.ScheduleAt, c.Budget, c.Status, c.UpdatedAt, c.Org, c.ID)
	if err != nil {
		return Campaign{}, fmt.Errorf("save campaign: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Campaign{}, errNotFound
	}
	return s.GetCampaign(ctx, c.Org, c.ID)
}

func (s *Store) DeleteCampaign(ctx context.Context, org, id string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM campaigns WHERE org=? AND id=?`, org, id)
	if err != nil {
		return false, fmt.Errorf("delete campaign: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// Counts returns the per-org campaign roll-up: total campaigns, how many are
// live, and the summed budget (cents) — a real, non-fabricated overview.
func (s *Store) Counts(ctx context.Context, org string) (total, live int, budget int64, err error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*),
		        COALESCE(SUM(CASE WHEN status='live' THEN 1 ELSE 0 END),0),
		        COALESCE(SUM(budget),0)
		 FROM campaigns WHERE org=?`, org)
	if err = row.Scan(&total, &live, &budget); err != nil {
		return 0, 0, 0, fmt.Errorf("count campaigns: %w", err)
	}
	return total, live, budget, nil
}
