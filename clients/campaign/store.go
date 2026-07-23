package campaign

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	// cek opens the store encrypted at rest (migrate-on-open + shred).
	"github.com/hanzoai/cloud/cek"
	// The ONE "sqlite" driver, kept registered for cek's no-key plaintext fallback.
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
	Kind       string `json:"kind"`              // paid | organic | email
	Platform   string `json:"platform"`          // meta | google | x | instagram | (email provider)
	Account    string `json:"account,omitempty"` // provider account ref (ad-account/page/list id)
	ExternalID string `json:"externalId,omitempty"`
	Status     string `json:"status"`           // pending | live | paused | failed | unavailable
	Detail     string `json:"detail,omitempty"` // honest last-outcome detail (never a secret)
}

// Campaign is the top-level GTM object — a VALUE that spans channels. Budget is
// minor units (cents). Content is the ordered creative set (Content[0] is the
// active creative; the rest are A/B variants when an experiment is composed).
// Metrics are deliberately NOT a field: they are read at query time from the ONE
// analytics plane (metrics.go), never stored here.
type Campaign struct {
	ID         string        `json:"id"`
	Org        string        `json:"-"` // tenant key — server-set from the validated owner claim, never client
	Name       string        `json:"name"`
	Audience   string        `json:"audience,omitempty"` // segment/audience selector ref
	Content    []string      `json:"content"`            // creative(s)
	Channels   []ChannelSpec `json:"channels"`           // fan-out targets
	ScheduleAt int64         `json:"scheduleAt,omitempty"`
	Budget     int64         `json:"budget"` // cents
	Status     string        `json:"status"`
	CreatedAt  int64         `json:"createdAt"`
	UpdatedAt  int64         `json:"updatedAt"`
}

// Store is the campaign database. ONE SQLite file ({DataDir}/campaign.db) holds
// every org's records; tenant isolation is the `org` column, enforced on EVERY
// query. Mirrors clients/ads exactly (the ONE storage pattern). MaxOpenConns(1)
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
